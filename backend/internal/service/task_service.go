package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
)

type TaskService struct {
	repo       *repository.Repository
	storage    StorageService
	agent      AgentComposeClient
	publisher  *RuntimeWorkspacePublisher
	workspaces *RuntimeWorkspaceBuilder
	templates  *TemplateCatalogService
	agentCfg   config.AgentComposeConfig
	machine    *StateMachine
}

const (
	retryPhasePrepare        = "prepare"
	retryPhaseSpecGenerate   = "spec_generate"
	retryPhaseSVGExecute     = "svg_execute"
	retryPhaseQualityCheck   = "quality_check"
	retryPhaseFinalizeExport = "finalize_export"
	retryPhasePublish        = "publish"
)

type TaskSpecFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

type TaskSpecPreview struct {
	TaskID       string         `json:"task_id"`
	ProjectPath  string         `json:"project_path"`
	DesignSpec   TaskSpecFile   `json:"design_spec"`
	SpecLock     TaskSpecFile   `json:"spec_lock"`
	Summary      map[string]any `json:"summary"`
	Confirmation map[string]any `json:"confirmation"`
	Contract     map[string]any `json:"contract"`
}

func NewTaskService(repo *repository.Repository, storage StorageService, agent AgentComposeClient, publisher *RuntimeWorkspacePublisher, agentCfg config.AgentComposeConfig) *TaskService {
	return &TaskService{
		repo:       repo,
		storage:    storage,
		agent:      agent,
		publisher:  publisher,
		workspaces: NewRuntimeWorkspaceBuilder(agentCfg, storage),
		templates:  NewTemplateCatalogServiceWithRepository(repo, agentCfg.PPTMasterSkillDir),
		agentCfg:   agentCfg,
		machine:    NewStateMachine(),
	}
}

func (s *TaskService) CreateTask(ctx context.Context, title, templateID string) (*model.Task, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled SlideSmith task"
	}
	templateID = strings.TrimSpace(templateID)
	templateLockJSON := "{}"
	if templateID != "" {
		lock, err := s.templates.BuildTemplateLock(ctx, templateID)
		if err != nil {
			return nil, fmt.Errorf("lock template %q: %w", templateID, err)
		}
		raw, err := json.Marshal(lock)
		if err != nil {
			return nil, err
		}
		templateLockJSON = string(raw)
		templateID = lock.TemplateID
	}
	taskID := uuid.NewString()
	task := &model.Task{
		ID:                 taskID,
		Title:              title,
		Status:             model.TaskStatusCreated,
		RuntimeProject:     runtimeProjectName(taskID),
		SelectedTemplateID: templateID,
		TemplateLockJSON:   templateLockJSON,
		Route:              model.TaskRouteMain,
		RouteSelectionJSON: "{}",
	}
	if err := s.repo.CreateTask(ctx, task); err != nil {
		return nil, err
	}
	payload := map[string]any{"title": title}
	if templateID != "" {
		payload["selected_template_id"] = templateID
	}
	_ = s.event(ctx, task.ID, model.EventTypeStatus, task.Status, "Task created", payload)
	return task, nil
}

func (s *TaskService) ListTasks(ctx context.Context) ([]model.Task, error) {
	return s.repo.ListTasks(ctx)
}

func (s *TaskService) GetTask(ctx context.Context, id string) (*model.Task, error) {
	return s.repo.GetTask(ctx, id)
}

func (s *TaskService) UploadFile(ctx context.Context, taskID, filename string, reader io.Reader) (*model.Artifact, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusCreated && task.Status != model.TaskStatusUploaded {
		return nil, fmt.Errorf("cannot upload file while task status is %q", task.Status)
	}
	sourceInfo := DetectSourceKind(filename)
	if !sourceInfo.Supported {
		return nil, fmt.Errorf("unsupported source file %q: %s", filename, sourceInfo.Message)
	}
	metadataJSON, err := json.Marshal(SourceArtifactMetadata(sourceInfo))
	if err != nil {
		return nil, fmt.Errorf("encode source artifact metadata: %w", err)
	}
	stored, err := s.storage.Save(ctx, taskID, model.ArtifactKindSource, filename, reader)
	if err != nil {
		return nil, err
	}
	artifact := &model.Artifact{
		TaskID:       taskID,
		Kind:         model.ArtifactKindSource,
		Name:         stored.Name,
		Storage:      "local",
		ObjectKey:    stored.ObjectKey,
		MimeType:     stored.MimeType,
		Size:         stored.Size,
		SHA256:       stored.SHA256,
		MetadataJSON: string(metadataJSON),
	}
	if err := s.repo.CreateArtifact(ctx, artifact); err != nil {
		return nil, err
	}
	if task.Status == model.TaskStatusCreated {
		if err := s.transition(ctx, task, model.TaskStatusUploaded, "Source uploaded", map[string]any{"artifact_id": artifact.ID}); err != nil {
			return nil, err
		}
	}
	_ = s.event(ctx, taskID, model.EventTypeArtifact, model.ArtifactKindSource, "Source artifact stored", map[string]any{"artifact_id": artifact.ID, "name": artifact.Name})
	return artifact, nil
}

func (s *TaskService) StartTask(ctx context.Context, taskID string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusUploaded && task.Status != model.TaskStatusFailed {
		return nil, fmt.Errorf("task must be uploaded or failed before start, got %q", task.Status)
	}
	if task.RuntimeProject == "" {
		task.RuntimeProject = runtimeProjectName(task.ID)
	}
	now := time.Now().UTC()
	task.StartedAt = &now
	if err := s.transition(ctx, task, model.TaskStatusRuntimePreparing, "Runtime preparing", nil); err != nil {
		return nil, err
	}
	if err := s.repo.EnsureConfirmations(ctx, task.ID, defaultConfirmations()); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Prepare queued for worker", nil)
	return task, nil
}

func (s *TaskService) SubmitConfirmations(ctx context.Context, taskID string, values map[string]any) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	switch task.Status {
	case model.TaskStatusAwaitingAnchorConfirm:
		if err := s.repo.SubmitConfirmations(ctx, taskID, values); err != nil {
			return nil, err
		}
		_ = s.event(ctx, taskID, model.EventTypeConfirmation, "tier1_submitted", "Anchor confirmations submitted", map[string]any{"keys": mapKeys(values)})
		if err := s.transition(ctx, task, model.TaskStatusRealizationDeriving, "Deriving realization recommendations", nil); err != nil {
			return nil, err
		}
		if err := s.deriveRealizationConfirmations(ctx, task); err != nil {
			_ = s.fail(ctx, task, err)
			return nil, err
		}
		if err := s.transition(ctx, task, model.TaskStatusAwaitingRealizationConfirm, "Awaiting realization confirmation", nil); err != nil {
			return nil, err
		}
	case model.TaskStatusAwaitingRealizationConfirm:
		if err := s.repo.SubmitConfirmations(ctx, taskID, values); err != nil {
			return nil, err
		}
		_ = s.event(ctx, taskID, model.EventTypeConfirmation, "tier2_submitted", "Realization confirmations submitted", map[string]any{"keys": mapKeys(values)})
		if err := s.transition(ctx, task, model.TaskStatusSpecGenerating, "Spec generating", nil); err != nil {
			return nil, err
		}
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Generate queued for worker", nil)
	case model.TaskStatusAwaitingConfirm:
		if err := s.repo.SubmitConfirmations(ctx, taskID, values); err != nil {
			return nil, err
		}
		_ = s.event(ctx, taskID, model.EventTypeConfirmation, "submitted", "Confirmations submitted", map[string]any{"keys": mapKeys(values)})
		if err := s.transition(ctx, task, model.TaskStatusSpecGenerating, "Spec generating", nil); err != nil {
			return nil, err
		}
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Generate queued for worker", nil)
	default:
		return nil, fmt.Errorf("task must be awaiting confirmation before confirmation submit, got %q", task.Status)
	}
	return task, nil
}

func (s *TaskService) ProcessQueuedTasks(ctx context.Context, limit int) (int, error) {
	tasks, err := s.repo.ListTasksByStatuses(ctx, []string{
		model.TaskStatusRuntimePreparing,
		model.TaskStatusSpecGenerating,
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
		model.TaskStatusPublishing,
	}, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for i := range tasks {
		if err := s.ProcessTask(ctx, tasks[i].ID); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (s *TaskService) ProcessTask(ctx context.Context, taskID string) error {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	switch task.Status {
	case model.TaskStatusRuntimePreparing:
		return s.processPrepare(ctx, task)
	case model.TaskStatusSpecGenerating:
		return s.processGenerate(ctx, task)
	case model.TaskStatusSVGGenerating:
		return s.processFullPPTMasterQueuedPhase(ctx, task, PhaseSVGExecute)
	case model.TaskStatusQualityChecking:
		return s.processFullPPTMasterQueuedPhase(ctx, task, PhaseQualityCheck)
	case model.TaskStatusExporting:
		return s.processFullPPTMasterQueuedPhase(ctx, task, PhaseFinalizeExport)
	case model.TaskStatusPublishing:
		return s.processPublish(ctx, task, nil, map[string]any{"queued_status": model.TaskStatusPublishing})
	default:
		return nil
	}
}

func (s *TaskService) processPrepare(ctx context.Context, task *model.Task) error {
	if err := s.transition(ctx, task, model.TaskStatusSourceConverting, "Source converting", nil); err != nil {
		return err
	}
	if !s.agentCfg.Enabled {
		return s.transition(ctx, task, model.TaskStatusAwaitingAnchorConfirm, "Awaiting anchor confirmation", map[string]any{"runtime": "disabled"})
	}

	workspace, err := s.buildTaskWorkspace(ctx, task)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, "prepare.workspace", err, nil, nil)
		return err
	}
	selection, err := s.runRouteSelect(ctx, task, workspace)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseRouteSelect), err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
		})
		return err
	}
	policy := routeExecutionPolicyFor(selection)
	if !policy.Executable {
		err := fmt.Errorf("%s", policy.FailureMessage)
		_ = s.failWithMetadata(ctx, task, policy.FailurePhase, err, nil, map[string]any{
			"workspace_path":         workspace.HostDir,
			"route":                  selection.Route,
			"route_reason":           selection.Reason,
			"standalone_workflow":    selection.StandaloneWorkflow,
			"route_selection":        selection,
			"route_execution_policy": policy,
			"next_spec":              policy.NextSpec,
			"supported_routes":       policy.SupportedRoutes,
			"known_routes":           policy.KnownRoutes,
		})
		return err
	}
	command := fmt.Sprintf(
		"node workflows/ppt_workflow.js prepare --profile %s --sources-manifest %s --input %s --project %s",
		shellArg(s.commandRunnerProfile()),
		shellArg(".slidesmith/source_inputs.json"),
		shellArg(workspace.InputPath),
		shellArg(task.RuntimeProject),
	)
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSourcePrepare, PhaseRunnerAgent, map[string]any{
		"command":          command,
		"workspace_path":   workspace.HostDir,
		"sources_manifest": ".slidesmith/source_inputs.json",
		"input_path":       workspace.InputPath,
	})
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseSourcePrepare), err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return err
	}
	run, err := s.runAgent(ctx, task, "prepare", AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSourcePrepare)+".agent", err, run, nil)
		return err
	}
	var preparedProjectPath string
	if run.WorkspacePath != "" {
		projectPath, err := s.syncPreparedProject(ctx, task.ID, task.RuntimeProject, run.WorkspacePath, workspace.HostDir)
		if err != nil {
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
			_ = s.failWithMetadata(ctx, task, string(PhaseSourcePrepare)+".sync", err, run, map[string]any{"workspace_path": run.WorkspacePath})
			return err
		}
		preparedProjectPath = projectPath
		if err := s.workspaces.WriteRuntimeManifest(workspace, task, projectPath); err != nil {
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
			_ = s.failWithMetadata(ctx, task, string(PhaseSourcePrepare)+".manifest", err, run, map[string]any{"project_path": projectPath})
			return err
		}
	}
	sourceContract, err := validateSourcePrepareContractWithSourceCount(preparedProjectPath, selection.Route, workspace.SourceCount)
	if err != nil {
		output := runtimeRunPhaseOutput(run)
		output["project_path"] = preparedProjectPath
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, output, err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSourcePrepare)+".contract", err, run, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   preparedProjectPath,
			"route":          selection.Route,
		})
		return err
	}
	task.LastRuntimeRunID = run.ExternalRunID
	task.LastRuntimeSessionID = run.ExternalSessionID
	task.RuntimeWorkspacePath = run.WorkspacePath
	if err := s.repo.SaveTask(ctx, task); err != nil {
		cause := fmt.Errorf("source_prepare.persist_runtime: %w", err)
		output := runtimeRunPhaseOutput(run)
		output["project_path"] = preparedProjectPath
		return s.recoverSourcePrepareFailure(
			ctx,
			task,
			phaseRun,
			"source_prepare.persist_runtime",
			cause,
			run,
			output,
			map[string]any{
				"workspace_path": workspace.HostDir,
				"project_path":   preparedProjectPath,
				"route":          selection.Route,
			},
		)
	}
	sourceArtifacts, err := s.publishSourceIntakeArtifacts(ctx, task, preparedProjectPath)
	if err != nil {
		cause := fmt.Errorf("source_prepare.publish_intake: %w", err)
		output := runtimeRunPhaseOutput(run)
		output["project_path"] = preparedProjectPath
		return s.recoverSourcePrepareFailure(
			ctx,
			task,
			phaseRun,
			"source_prepare.publish_intake",
			cause,
			run,
			output,
			map[string]any{
				"workspace_path": workspace.HostDir,
				"project_path":   preparedProjectPath,
				"route":          selection.Route,
			},
		)
	}
	output := runtimeRunPhaseOutput(run)
	output["project_path"] = preparedProjectPath
	output["source_contract"] = sourceContract
	output["source_intake_artifact_count"] = len(sourceArtifacts)
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		cause := fmt.Errorf("source_prepare.finalize: %w", err)
		return s.recoverSourcePrepareFailure(
			ctx,
			task,
			phaseRun,
			"source_prepare.finalize",
			cause,
			run,
			output,
			map[string]any{
				"workspace_path":  workspace.HostDir,
				"project_path":    preparedProjectPath,
				"route":           selection.Route,
				"source_contract": sourceContract,
			},
		)
	}
	policy = routeExecutionPolicyFor(selection)
	if !policy.WorkflowExecutable {
		err := fmt.Errorf("%s", policy.FailureMessage)
		_ = s.failWithMetadata(ctx, task, policy.FailurePhase, err, run, map[string]any{
			"workspace_path":         workspace.HostDir,
			"project_path":           preparedProjectPath,
			"route":                  selection.Route,
			"route_reason":           selection.Reason,
			"source_contract":        sourceContract,
			"route_execution_policy": policy,
			"next_spec":              policy.NextSpec,
		})
		return err
	}
	templateResolution, err := s.runTemplateResolve(ctx, task, workspace, preparedProjectPath)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseTemplateResolve), err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   preparedProjectPath,
		})
		return err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "template_resolved", "Template resolved for task workspace", map[string]any{
		"selected_template_id": templateResolution.SelectedTemplateID,
		"template_root":        templateResolution.TemplateRoot,
		"template_lock":        templateResolution.TemplateLockPath,
	})
	return s.transition(ctx, task, model.TaskStatusAwaitingAnchorConfirm, "Awaiting anchor confirmation", map[string]any{"runtime_run_id": run.ID})
}

func (s *TaskService) recoverSourcePrepareFailure(
	ctx context.Context,
	task *model.Task,
	phaseRun *model.TaskPhaseRun,
	failurePhase string,
	cause error,
	run *model.TaskRuntimeRun,
	phaseOutput map[string]any,
	extra map[string]any,
) error {
	recoveryCtx := context.WithoutCancel(ctx)
	errs := []error{cause}
	if err := s.finishPhaseRun(recoveryCtx, phaseRun, PhaseRunStatusFailed, phaseOutput, cause); err != nil {
		errs = append(errs, fmt.Errorf("recover source_prepare phase: %w", err))
	}
	if err := s.failWithMetadata(recoveryCtx, task, failurePhase, cause, run, extra); err != nil {
		errs = append(errs, fmt.Errorf("recover source_prepare task: %w", err))
	}
	return errors.Join(errs...)
}

func (s *TaskService) processGenerate(ctx context.Context, task *model.Task) error {
	if !s.agentCfg.Enabled {
		err := fmt.Errorf("agent compose disabled; worker cannot generate")
		_ = s.failWithMetadata(ctx, task, "generate.agent_disabled", err, nil, nil)
		return err
	}
	workspace := s.resolveTaskWorkspace(task)
	if err := s.writeConfirmationResult(ctx, task); err != nil {
		_ = s.failWithMetadata(ctx, task, "confirmation_result", err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
		})
		return err
	}

	if s.useFullPPTMaster() {
		return s.processFullPPTMasterSplit(ctx, task, workspace)
	}
	return s.processLegacyCommandGenerate(ctx, task, workspace)
}

func (s *TaskService) processLegacyCommandGenerate(ctx context.Context, task *model.Task, workspace *TaskWorkspace) error {
	input := map[string]any{
		"workspace_path":     workspace.HostDir,
		"runtime_project":    task.RuntimeProject,
		"full_ppt_master":    false,
		"legacy_bundle_note": "current generate run still covers spec, svg, quality, export, and runtime publish",
	}
	if task.SelectedTemplateID != "" {
		input["selected_template_id"] = task.SelectedTemplateID
		input["template_resolution"] = ".slidesmith/template_resolution.json"
		input["template_lock"] = ".slidesmith/template_lock.json"
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSpecGenerate, PhaseRunnerLegacyAgentBundle, input)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate), err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return err
	}

	command := fmt.Sprintf(
		"node workflows/ppt_workflow.js generate --profile %s --project %s --confirmation existing",
		shellArg(s.commandRunnerProfile()),
		shellArg(task.RuntimeProject),
	)
	run, err := s.runAgent(ctx, task, "generate", AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	if err != nil {
		recovered, recoverErr := s.tryRecoverGeneratedRuntimeArtifacts(ctx, task, workspace, run, "generate.agent")
		if recovered {
			output := runtimeRunPhaseOutput(run)
			output["recovered"] = true
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil)
			return recoverErr
		}
		extra := map[string]any{
			"workspace_path": workspace.HostDir,
		}
		if recoverErr != nil {
			extra["recovery_error"] = recoverErr.Error()
		}
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "generate.agent", err, run, extra)
		return err
	}
	task.LastRuntimeRunID = run.ExternalRunID
	task.LastRuntimeSessionID = run.ExternalSessionID
	task.RuntimeWorkspacePath = run.WorkspacePath
	if err := s.repo.SaveTask(ctx, task); err != nil {
		return err
	}
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, runtimeRunPhaseOutput(run), nil); err != nil {
		return err
	}
	if err := s.recordLegacyCompletedPhaseRuns(ctx, task, run, PhaseImageAcquire, PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport); err != nil {
		return err
	}

	for _, status := range []string{
		model.TaskStatusImageAcquiring,
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
		model.TaskStatusPublishing,
	} {
		if err := s.transition(ctx, task, status, status, map[string]any{"runtime_run_id": run.ID}); err != nil {
			return err
		}
	}
	return s.processPublish(ctx, task, workspace, map[string]any{"runtime_run_id": run.ID})
}

func (s *TaskService) processFullPPTMasterSplit(ctx context.Context, task *model.Task, workspace *TaskWorkspace) error {
	return s.processFullPPTMasterFromPhase(ctx, task, workspace, PhaseSpecGenerate)
}

func (s *TaskService) processFullPPTMasterQueuedPhase(ctx context.Context, task *model.Task, phase PipelinePhase) error {
	if !s.agentCfg.Enabled {
		err := fmt.Errorf("agent compose disabled; worker cannot run %s", phase)
		_ = s.failWithMetadata(ctx, task, string(phase)+".agent_disabled", err, nil, nil)
		return err
	}
	if !s.useFullPPTMaster() {
		err := fmt.Errorf("phase retry %s requires full-ppt-master profile", phase)
		_ = s.failWithMetadata(ctx, task, string(phase)+".unsupported_profile", err, nil, nil)
		return err
	}
	return s.processFullPPTMasterFromPhase(ctx, task, s.resolveTaskWorkspace(task), phase)
}

func (s *TaskService) processFullPPTMasterFromPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, startPhase PipelinePhase) error {
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(startPhase)+".project", err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
		})
		return err
	}
	if startPhase == PhaseSpecGenerate {
		if err := cleanupFullPPTMasterOutputsForRetry(projectPath, PhaseSpecGenerate); err != nil {
			_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate)+".cleanup", err, nil, map[string]any{
				"project_path": projectPath,
			})
			return err
		}
	}

	if startPhase == PhaseSpecGenerate {
		specRun, nextProjectPath, err := s.runFullPPTMasterSpecPhase(ctx, task, workspace, projectPath)
		if err != nil {
			return err
		}
		projectPath = nextProjectPath
		if shouldPauseForSpecPreview(projectPath) {
			if err := s.transition(ctx, task, model.TaskStatusAwaitingSpecConfirm, "Awaiting spec confirmation", map[string]any{
				"runtime_run_id": specRun.ID,
				"project_path":   projectPath,
				"refine_spec":    true,
			}); err != nil {
				return err
			}
			_ = s.event(ctx, task.ID, model.EventTypeConfirmation, "spec_preview_ready", "Spec preview is ready for confirmation", map[string]any{
				"project_path": projectPath,
			})
			return nil
		}
		if err := s.queueSVGAfterSpecApproval(ctx, task, projectPath, specRun.ID); err != nil {
			return err
		}
		startPhase = PhaseSVGExecute
	}

	if startPhase == PhaseSVGExecute {
		svgRun, nextProjectPath, err := s.runFullPPTMasterSVGPhase(ctx, task, workspace, projectPath)
		if err != nil {
			return err
		}
		projectPath = nextProjectPath
		if err := s.transition(ctx, task, model.TaskStatusQualityChecking, "Quality checking", map[string]any{
			"runtime_run_id": svgRun.ID,
		}); err != nil {
			return err
		}
		startPhase = PhaseQualityCheck
	}

	if startPhase == PhaseQualityCheck {
		qualityRun, nextProjectPath, err := s.runFullPPTMasterQualityPhase(ctx, task, workspace, projectPath)
		if err != nil {
			return err
		}
		projectPath = nextProjectPath
		if err := s.transition(ctx, task, model.TaskStatusExporting, "Exporting", map[string]any{
			"runtime_run_id": qualityRun.ID,
		}); err != nil {
			return err
		}
		startPhase = PhaseFinalizeExport
	}

	if startPhase == PhaseFinalizeExport {
		exportRun, nextProjectPath, err := s.runFullPPTMasterExportPhase(ctx, task, workspace, projectPath)
		if err != nil {
			return err
		}
		projectPath = nextProjectPath
		if err := s.transition(ctx, task, model.TaskStatusPublishing, "Publishing", map[string]any{
			"runtime_run_id": exportRun.ID,
		}); err != nil {
			return err
		}
		return s.processPublish(ctx, task, workspace, map[string]any{
			"runtime_run_id": exportRun.ID,
			"split_generate": true,
			"project_path":   projectPath,
		})
	}

	if startPhase == PhasePublish {
		return s.processPublish(ctx, task, workspace, map[string]any{
			"split_generate": true,
			"project_path":   projectPath,
		})
	}

	return fmt.Errorf("unsupported full-ppt-master start phase %q", startPhase)
}

func (s *TaskService) queueSVGAfterSpecApproval(ctx context.Context, task *model.Task, projectPath, previousRuntimeRunID string) error {
	if err := s.transition(ctx, task, model.TaskStatusImageAcquiring, "Image acquire skipped", map[string]any{
		"runtime_run_id": previousRuntimeRunID,
		"reason":         "image acquisition not split in milestone 2",
	}); err != nil {
		return err
	}
	if err := s.recordSkippedPhaseRun(ctx, task, PhaseImageAcquire, PhaseRunnerWorker, map[string]any{
		"reason":                  "image acquisition not split in milestone 2",
		"project_path":            projectPath,
		"previous_runtime_run_id": previousRuntimeRunID,
	}); err != nil {
		return err
	}
	return s.transition(ctx, task, model.TaskStatusSVGGenerating, "SVG executing", map[string]any{
		"previous_runtime_run_id": previousRuntimeRunID,
		"project_path":            projectPath,
	})
}

func (s *TaskService) runFullPPTMasterSpecPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string) (*model.TaskRuntimeRun, string, error) {
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSpecGenerate, PhaseRunnerAgent, templateResolvePhaseInput(task, workspace, projectPath))
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate), err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return nil, projectPath, err
	}
	run, err := s.runAgent(ctx, task, string(PhaseSpecGenerate), AgentRunRequest{
		Prompt:      s.fullPPTMasterSpecPrompt(task, projectPath),
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
		Detached:    true,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	_ = s.applyRuntimeRunToTask(ctx, task, run)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate)+".agent", err, run, map[string]any{"workspace_path": workspace.HostDir})
		return run, projectPath, err
	}
	projectPath, err = s.syncRuntimeProject(ctx, task, workspace, run.WorkspacePath)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate)+".sync", err, run, map[string]any{"workspace_path": run.WorkspacePath})
		return run, projectPath, err
	}
	contract, err := validateSpecGenerateContract(projectPath)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate)+".contract", err, run, map[string]any{"project_path": projectPath})
		return run, projectPath, err
	}
	output := runtimeRunPhaseOutput(run)
	output["project_path"] = projectPath
	output["contract"] = contract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return run, projectPath, err
	}
	return run, projectPath, nil
}

func (s *TaskService) runFullPPTMasterSVGPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string) (*model.TaskRuntimeRun, string, error) {
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSVGExecute, PhaseRunnerAgent, map[string]any{
		"workspace_path":  workspace.HostDir,
		"project_path":    projectPath,
		"runtime_project": task.RuntimeProject,
	})
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseSVGExecute), err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return nil, projectPath, err
	}
	run, err := s.runAgent(ctx, task, string(PhaseSVGExecute), AgentRunRequest{
		Prompt:      s.fullPPTMasterSVGPrompt(task, projectPath),
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
		Detached:    true,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	_ = s.applyRuntimeRunToTask(ctx, task, run)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSVGExecute)+".agent", err, run, map[string]any{"workspace_path": workspace.HostDir})
		return run, projectPath, err
	}
	projectPath, err = s.syncRuntimeProject(ctx, task, workspace, run.WorkspacePath)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSVGExecute)+".sync", err, run, map[string]any{"workspace_path": run.WorkspacePath})
		return run, projectPath, err
	}
	contract, err := validateSVGExecuteContract(projectPath)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSVGExecute)+".contract", err, run, map[string]any{"project_path": projectPath})
		return run, projectPath, err
	}
	output := runtimeRunPhaseOutput(run)
	output["project_path"] = projectPath
	output["contract"] = contract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return run, projectPath, err
	}
	return run, projectPath, nil
}

func (s *TaskService) runFullPPTMasterQualityPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string) (*model.TaskRuntimeRun, string, error) {
	projectRel := s.projectRel(task, projectPath)
	command := fmt.Sprintf("python3 skills/ppt-master/scripts/svg_quality_checker.py %s", shellArg(projectRel))
	return s.runFullPPTMasterCommandPhase(ctx, task, workspace, projectPath, PhaseQualityCheck, command, validateQualityCheckContract)
}

func (s *TaskService) runFullPPTMasterExportPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string) (*model.TaskRuntimeRun, string, error) {
	projectRel := s.projectRel(task, projectPath)
	command := fmt.Sprintf(
		"python3 skills/ppt-master/scripts/finalize_svg.py %s --quiet && python3 skills/ppt-master/scripts/svg_to_pptx.py %s --no-notes -t none && python3 scripts/ppt_runner.py publish --project-path %s",
		shellArg(projectRel),
		shellArg(projectRel),
		shellArg(projectRel),
	)
	return s.runFullPPTMasterCommandPhase(ctx, task, workspace, projectPath, PhaseFinalizeExport, command, validatePPTXExportContract)
}

func (s *TaskService) runFullPPTMasterCommandPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string, phase PipelinePhase, command string, validate func(string) (map[string]any, error)) (*model.TaskRuntimeRun, string, error) {
	phaseRun, err := s.beginPhaseRun(ctx, task, phase, PhaseRunnerWorker, map[string]any{
		"workspace_path": workspace.HostDir,
		"project_path":   projectPath,
		"command":        command,
	})
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(phase), err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return nil, projectPath, err
	}
	run, err := s.runAgent(ctx, task, string(phase), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	_ = s.applyRuntimeRunToTask(ctx, task, run)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(phase)+".command", err, run, map[string]any{"workspace_path": workspace.HostDir})
		return run, projectPath, err
	}
	projectPath, err = s.syncRuntimeProject(ctx, task, workspace, run.WorkspacePath)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(phase)+".sync", err, run, map[string]any{"workspace_path": run.WorkspacePath})
		return run, projectPath, err
	}
	output := runtimeRunPhaseOutput(run)
	output["project_path"] = projectPath
	if validate != nil {
		contract, err := validate(projectPath)
		if err != nil {
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, output, err)
			_ = s.failWithMetadata(ctx, task, string(phase)+".contract", err, run, map[string]any{"project_path": projectPath})
			return run, projectPath, err
		}
		output["contract"] = contract
	}
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return run, projectPath, err
	}
	return run, projectPath, nil
}

func (s *TaskService) applyRuntimeRunToTask(ctx context.Context, task *model.Task, run *model.TaskRuntimeRun) error {
	if run == nil {
		return nil
	}
	if run.ExternalRunID != "" {
		task.LastRuntimeRunID = run.ExternalRunID
	}
	if run.ExternalSessionID != "" {
		task.LastRuntimeSessionID = run.ExternalSessionID
	}
	if run.WorkspacePath != "" {
		task.RuntimeWorkspacePath = run.WorkspacePath
	}
	return s.repo.SaveTask(ctx, task)
}

func cleanupFullPPTMasterOutputsForRetry(projectPath string, phase PipelinePhase) error {
	paths := []string{
		filepath.Join(projectPath, ".slidesmith", "artifacts.json"),
		filepath.Join(projectPath, ".slidesmith-artifacts.json"),
		filepath.Join(filepath.Dir(filepath.Dir(projectPath)), ".slidesmith", "artifacts.json"),
	}
	switch phase {
	case PhaseSpecGenerate:
		paths = append(paths,
			filepath.Join(projectPath, "design_spec.md"),
			filepath.Join(projectPath, "spec_lock.md"),
			filepath.Join(projectPath, ".slidesmith", "spec_contract.json"),
			filepath.Join(projectPath, ".slidesmith", "contracts"),
			filepath.Join(projectPath, ".slidesmith", "quality_report.json"),
			filepath.Join(projectPath, "svg_output"),
			filepath.Join(projectPath, "notes"),
			filepath.Join(projectPath, "svg_final"),
			filepath.Join(projectPath, "exports"),
		)
	case PhaseSVGExecute:
		paths = append(paths,
			filepath.Join(projectPath, ".slidesmith", "quality_report.json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseSVGExecute)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseQualityCheck)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseFinalizeExport)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhasePublish)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", "final.json"),
			filepath.Join(projectPath, "svg_output"),
			filepath.Join(projectPath, "notes"),
			filepath.Join(projectPath, "svg_final"),
			filepath.Join(projectPath, "exports"),
		)
	case PhaseQualityCheck:
		paths = append(paths,
			filepath.Join(projectPath, ".slidesmith", "quality_report.json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseQualityCheck)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseFinalizeExport)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhasePublish)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", "final.json"),
			filepath.Join(projectPath, "svg_final"),
			filepath.Join(projectPath, "exports"),
		)
	case PhaseFinalizeExport:
		paths = append(paths,
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseFinalizeExport)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhasePublish)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", "final.json"),
			filepath.Join(projectPath, "svg_final"),
			filepath.Join(projectPath, "exports"),
		)
	case PhasePublish:
		paths = append(paths,
			filepath.Join(projectPath, ".slidesmith", "contracts", string(PhasePublish)+".json"),
			filepath.Join(projectPath, ".slidesmith", "contracts", "final.json"),
		)
	default:
		return fmt.Errorf("unsupported cleanup phase %q", phase)
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func (s *TaskService) processPublish(ctx context.Context, task *model.Task, workspace *TaskWorkspace, payload map[string]any) error {
	if workspace == nil {
		workspace = s.resolveTaskWorkspace(task)
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhasePublish, PhaseRunnerPublisher, map[string]any{
		"workspace_path":          task.RuntimeWorkspacePath,
		"resolved_workspace_path": workspace.HostDir,
	})
	if err != nil {
		return err
	}
	contract, err := s.publishRuntimeArtifacts(ctx, task, workspace)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, map[string]any{
			"workspace_path":          task.RuntimeWorkspacePath,
			"resolved_workspace_path": workspace.HostDir,
		}, err)
		_ = s.failWithMetadata(ctx, task, "publish", err, nil, map[string]any{
			"workspace_path":          task.RuntimeWorkspacePath,
			"resolved_workspace_path": workspace.HostDir,
		})
		return err
	}
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, map[string]any{
		"workspace_path":          task.RuntimeWorkspacePath,
		"resolved_workspace_path": workspace.HostDir,
		"contract":                contract,
	}, nil); err != nil {
		return err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["contract"] = contract
	now := time.Now().UTC()
	task.CompletedAt = &now
	return s.transition(ctx, task, model.TaskStatusCompleted, "Task completed", payload)
}

func (s *TaskService) publishRuntimeArtifacts(ctx context.Context, task *model.Task, workspace *TaskWorkspace) (map[string]any, error) {
	type publishRoot struct {
		Path        string
		Source      string
		SessionID   string
		ProjectPath string
	}
	var roots []publishRoot
	seen := map[string]bool{}
	addRoot := func(root, source, sessionID, projectPath string) {
		root = strings.TrimSpace(root)
		if root == "" || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, publishRoot{
			Path:        root,
			Source:      source,
			SessionID:   sessionID,
			ProjectPath: projectPath,
		})
	}
	addRoot(task.RuntimeWorkspacePath, "task_runtime_workspace", task.LastRuntimeSessionID, "")
	if workspace != nil {
		addRoot(workspace.HostDir, "task_workspace", "", "")
	}
	if candidates, err := s.findGeneratedRuntimeWorkspaceCandidates(ctx, task); err == nil {
		for _, candidate := range candidates {
			addRoot(candidate.WorkspacePath, "agent_compose_session", candidate.SessionID, candidate.ProjectPath)
		}
	} else {
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "recovery_scan_failed", "Runtime recovery scan failed", map[string]any{
			"error": err.Error(),
		})
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("runtime workspace path is empty")
	}

	var lastErr error
	publishVersion := publishVersionName()
	for _, root := range roots {
		published, err := s.publisher.Publish(ctx, task.ID, root.Path, publishVersion)
		if err != nil {
			lastErr = err
			continue
		}
		projectPath := root.ProjectPath
		if projectPath == "" {
			projectPath, _ = discoverRuntimeProjectPath(root.Path)
		}
		if projectPath == "" {
			lastErr = fmt.Errorf("published runtime project path not found for workspace %s", root.Path)
			continue
		}
		publishContract, err := buildPublishedArtifactsContract(projectPath, s.storage, published, publishVersion)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := writeContractReport(projectPath, string(PhasePublish), publishContract); err != nil {
			lastErr = err
			continue
		}
		publishContractArtifact, err := s.copyContractReportArtifact(ctx, task.ID, projectPath, publishVersion, string(PhasePublish))
		if err != nil {
			lastErr = err
			continue
		}
		published = append(published, publishContractArtifact)
		finalContract := buildFinalTaskContract(projectPath, publishContract)
		if _, err := writeContractReport(projectPath, "final", finalContract); err != nil {
			lastErr = err
			continue
		}
		finalContractArtifact, err := s.copyContractReportArtifact(ctx, task.ID, projectPath, publishVersion, "final")
		if err != nil {
			lastErr = err
			continue
		}
		published = append(published, finalContractArtifact)
		if root.Source == "agent_compose_session" {
			task.RuntimeWorkspacePath = root.Path
			task.LastRuntimeSessionID = root.SessionID
			if err := s.repo.SaveTask(ctx, task); err != nil {
				return nil, err
			}
		}
		prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", publishVersion)) + "/"
		if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, prefix, published); err != nil {
			return nil, err
		}
		persisted, err := s.repo.ListArtifactsByPublishVersion(ctx, task.ID, publishVersion)
		if err != nil {
			return nil, err
		}
		if len(persisted) != len(published) {
			return nil, fmt.Errorf("persisted artifact count = %d, expected %d", len(persisted), len(published))
		}
		if _, err := buildPublishedArtifactsContract(projectPath, s.storage, persisted, publishVersion); err != nil {
			return nil, fmt.Errorf("final persisted artifact check failed: %w", err)
		}
		contract := map[string]any{
			"publish":                  publishContract,
			"final":                    finalContract,
			"publish_version":          publishVersion,
			"persisted_artifact_count": len(persisted),
		}
		if err := s.event(ctx, task.ID, model.EventTypeArtifact, "published", "Runtime artifacts published", map[string]any{
			"count":            len(published),
			"workspace_path":   root.Path,
			"workspace_source": root.Source,
			"session_id":       root.SessionID,
			"project_path":     root.ProjectPath,
			"storage_prefix":   prefix,
			"publish_version":  publishVersion,
			"contract":         contract,
		}); err != nil {
			return nil, err
		}
		return contract, nil
	}
	return nil, fmt.Errorf("publish runtime artifacts: %w", lastErr)
}

func (s *TaskService) copyContractReportArtifact(ctx context.Context, taskID, projectPath, publishVersion, name string) (model.Artifact, error) {
	sourcePath := filepath.Join(projectPath, ".slidesmith", "contracts", name+".json")
	objectKey := filepath.ToSlash(filepath.Join("tasks", taskID, "artifacts", publishVersion, "contracts", name+".json"))
	stored, err := s.storage.CopyFileToObject(ctx, objectKey, sourcePath)
	if err != nil {
		return model.Artifact{}, err
	}
	return model.Artifact{
		TaskID:         taskID,
		Kind:           model.ArtifactKindManifest,
		Name:           stored.Name,
		Storage:        "local",
		ObjectKey:      stored.ObjectKey,
		MimeType:       stored.MimeType,
		Size:           stored.Size,
		SHA256:         stored.SHA256,
		PublishVersion: publishVersion,
	}, nil
}

func publishVersionName() string {
	return "v" + time.Now().UTC().Format("20060102T150405.000000000Z")
}

func (s *TaskService) writeConfirmationResult(ctx context.Context, task *model.Task) error {
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return err
	}
	confirmations, err := s.repo.ListConfirmations(ctx, task.ID)
	if err != nil {
		return err
	}
	values := map[string]any{}
	for _, confirmation := range confirmations {
		var value any
		if confirmation.ValueJSON != "" && confirmation.ValueJSON != "null" {
			if err := json.Unmarshal([]byte(confirmation.ValueJSON), &value); err != nil {
				return fmt.Errorf("parse confirmation %s value: %w", confirmation.Key, err)
			}
		}
		if value == nil && confirmation.Recommendation != "" {
			value = confirmation.Recommendation
		}
		values[confirmation.Key] = value
	}

	result := map[string]any{
		"canvas":             firstValueString(values, []string{"canvas", "aspect_ratio"}, "ppt169"),
		"page_count":         firstValueString(values, []string{"page_count", "slide_count"}, "3"),
		"audience":           valueString(values, "audience", ""),
		"content_divergence": valueString(values, "content_divergence", valueString(values, "content_depth", "balanced")),
		"mode":               valueString(values, "mode", valueString(values, "content_depth", "balanced")),
		"visual_style":       valueString(values, "visual_style", "business"),
		"color":              valueString(values, "color", ""),
		"icons":              valueString(values, "icons", "tabler-outline"),
		"typography":         valueString(values, "typography", ""),
		"delivery_purpose":   valueString(values, "delivery_purpose", valueString(values, "export_quality", "balanced")),
		"formula_policy":     valueString(values, "formula_policy", "none"),
		"image_usage":        confirmationImageUsage(values),
		"image_notes":        valueString(values, "image_notes", ""),
		"generation_mode":    valueString(values, "generation_mode", "continuous"),
		"refine_spec":        valueBool(values, "refine_spec", false),
		"language":           valueString(values, "language", "zh-CN"),
		"stage":              "final",
		"status":             "confirmed",
		"confirmed_at":       time.Now().UTC().Format(time.RFC3339Nano),
		"submitted_values":   values,
	}
	if lock, ok, err := decodeTemplateLock(task.TemplateLockJSON); err != nil {
		return err
	} else if ok {
		result["selected_template_id"] = lock.TemplateID
		result["template_lock"] = lock
	}

	target := filepath.Join(projectPath, "confirm_ui", "result.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(target, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeConfirmation, "result_written", "Confirmation result written to runtime project", map[string]any{"path": target})
}

func shouldPauseForSpecPreview(projectPath string) bool {
	return valueBool(readJSONMap(filepath.Join(projectPath, "confirm_ui", "result.json")), "refine_spec", false)
}

func readSpecPreviewFile(path string) (TaskSpecFile, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return TaskSpecFile{}, fmt.Errorf("required spec file not found: %s", path)
	}
	if err != nil {
		return TaskSpecFile{}, err
	}
	if !info.Mode().IsRegular() {
		return TaskSpecFile{}, fmt.Errorf("required spec path is not a regular file: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return TaskSpecFile{}, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return TaskSpecFile{}, fmt.Errorf("required spec file is empty: %s", path)
	}
	return TaskSpecFile{
		Name:      filepath.Base(path),
		Path:      path,
		Content:   string(raw),
		Size:      info.Size(),
		UpdatedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
	}, nil
}

func readJSONMap(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func specPreviewSummary(confirmation, contract map[string]any) map[string]any {
	summary := map[string]any{}
	for _, key := range []string{
		"page_count",
		"canvas",
		"language",
		"audience",
		"mode",
		"visual_style",
		"color",
		"typography",
		"icons",
		"image_usage",
		"generation_mode",
		"refine_spec",
		"selected_template_id",
	} {
		if value, ok := confirmation[key]; ok {
			summary[key] = value
		}
	}
	if value, ok := contract["page_count"]; ok {
		summary["page_count"] = value
	}
	if value, ok := contract["checked_at"]; ok {
		summary["checked_at"] = value
	}
	return summary
}

func (s *TaskService) deriveRealizationConfirmations(ctx context.Context, task *model.Task) error {
	confirmations, err := s.repo.ListConfirmations(ctx, task.ID)
	if err != nil {
		return err
	}
	values := confirmationValues(confirmations)
	derived := tier2Confirmations(values)
	if err := s.repo.UpsertConfirmationDefinitions(ctx, task.ID, derived); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeConfirmation, "tier2_derived", "Realization recommendations derived from anchor confirmations", map[string]any{
		"anchor_keys": tier1ConfirmationKeys,
		"tier2_keys":  tier2ConfirmationKeys,
	})
}

func (s *TaskService) CancelTask(ctx context.Context, taskID string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	task.CancelledAt = &now
	if err := s.transition(ctx, task, model.TaskStatusCancelled, "Task cancelled", nil); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) RetryTask(ctx context.Context, taskID, phase string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusFailed {
		return nil, fmt.Errorf("only failed tasks can be retried")
	}
	phase, err = normalizeRetryPhase(phase, task.FailurePhase)
	if err != nil {
		return nil, err
	}
	switch phase {
	case retryPhasePrepare:
		return s.retryPrepare(ctx, task)
	case retryPhaseSpecGenerate:
		return s.retryPipelinePhase(ctx, task, PhaseSpecGenerate, model.TaskStatusSpecGenerating)
	case retryPhaseSVGExecute:
		return s.retryPipelinePhase(ctx, task, PhaseSVGExecute, model.TaskStatusSVGGenerating)
	case retryPhaseQualityCheck:
		return s.retryPipelinePhase(ctx, task, PhaseQualityCheck, model.TaskStatusQualityChecking)
	case retryPhaseFinalizeExport:
		return s.retryPipelinePhase(ctx, task, PhaseFinalizeExport, model.TaskStatusExporting)
	case retryPhasePublish:
		return s.retryPublish(ctx, task)
	default:
		return nil, fmt.Errorf("unsupported retry phase %q", phase)
	}
}

func (s *TaskService) ContinueTask(ctx context.Context, taskID, phase string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusAwaitingSpecConfirm {
		return nil, fmt.Errorf("task must be awaiting spec confirmation before continue, got %q", task.Status)
	}
	value := strings.ToLower(strings.TrimSpace(phase))
	if value == "" {
		value = string(PhaseSVGExecute)
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, fmt.Errorf("cannot continue before prepared project exists: %w", err)
	}
	switch value {
	case "svg", "svg_execute", "svg_generating":
		if _, err := validateSpecGenerateContract(projectPath); err != nil {
			return nil, fmt.Errorf("spec preview contract failed: %w", err)
		}
		if err := s.queueSVGAfterSpecApproval(ctx, task, projectPath, task.LastRuntimeRunID); err != nil {
			return nil, err
		}
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "SVG generation queued after spec confirmation", map[string]any{
			"project_path": projectPath,
		})
	case "spec", "spec_generate", "regenerate":
		if err := cleanupFullPPTMasterOutputsForRetry(projectPath, PhaseSpecGenerate); err != nil {
			return nil, fmt.Errorf("cleanup before spec regenerate: %w", err)
		}
		if err := s.transition(ctx, task, model.TaskStatusSpecGenerating, "Spec regenerate queued", map[string]any{
			"project_path": projectPath,
		}); err != nil {
			return nil, err
		}
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Spec regeneration queued from preview", map[string]any{
			"project_path": projectPath,
		})
	default:
		return nil, fmt.Errorf("unsupported continue phase %q", phase)
	}
	return task, nil
}

func (s *TaskService) GetSpecPreview(ctx context.Context, taskID string) (*TaskSpecPreview, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	designSpec, err := readSpecPreviewFile(filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		return nil, err
	}
	specLock, err := readSpecPreviewFile(filepath.Join(projectPath, "spec_lock.md"))
	if err != nil {
		return nil, err
	}
	confirmation := readJSONMap(filepath.Join(projectPath, "confirm_ui", "result.json"))
	contract := readJSONMap(filepath.Join(projectPath, ".slidesmith", "spec_contract.json"))
	return &TaskSpecPreview{
		TaskID:       task.ID,
		ProjectPath:  projectPath,
		DesignSpec:   designSpec,
		SpecLock:     specLock,
		Summary:      specPreviewSummary(confirmation, contract),
		Confirmation: confirmation,
		Contract:     contract,
	}, nil
}

func (s *TaskService) retryPrepare(ctx context.Context, task *model.Task) (*model.Task, error) {
	if task.RuntimeProject == "" {
		task.RuntimeProject = runtimeProjectName(task.ID)
	}
	if err := s.repo.EnsureConfirmations(ctx, task.ID, defaultConfirmations()); err != nil {
		return nil, err
	}
	if task.StartedAt == nil {
		now := time.Now().UTC()
		task.StartedAt = &now
	}
	if err := s.transition(ctx, task, model.TaskStatusRuntimePreparing, "Retry queued from prepare", map[string]any{"retry_phase": retryPhasePrepare}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Prepare retry queued for worker", map[string]any{"retry_phase": retryPhasePrepare})
	return task, nil
}

func (s *TaskService) retryPipelinePhase(ctx context.Context, task *model.Task, phase PipelinePhase, status string) (*model.Task, error) {
	if phase != PhaseSpecGenerate && !s.useFullPPTMaster() {
		return nil, fmt.Errorf("cannot retry %s with runner profile %q", phase, s.commandRunnerProfile())
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, fmt.Errorf("cannot retry %s before prepared project exists: %w", phase, err)
	}
	if err := cleanupFullPPTMasterOutputsForRetry(projectPath, phase); err != nil {
		return nil, fmt.Errorf("cleanup before retry %s: %w", phase, err)
	}
	if err := s.transition(ctx, task, status, "Retry queued from "+string(phase), map[string]any{
		"retry_phase":  string(phase),
		"project_path": projectPath,
	}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Phase retry queued for worker", map[string]any{
		"retry_phase":  string(phase),
		"project_path": projectPath,
	})
	return task, nil
}

func (s *TaskService) retryPublish(ctx context.Context, task *model.Task) (*model.Task, error) {
	if _, err := s.findPersistentProjectPath(task); err != nil {
		return nil, fmt.Errorf("cannot retry publish before prepared project exists: %w", err)
	}
	if err := s.transition(ctx, task, model.TaskStatusPublishing, "Retry queued from publish", map[string]any{"retry_phase": retryPhasePublish}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Publish retry queued for worker", map[string]any{"retry_phase": retryPhasePublish})
	return task, nil
}

func normalizeRetryPhase(requested, failurePhase string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(requested))
	if value == "" || value == "auto" {
		return inferRetryPhase(failurePhase), nil
	}
	switch value {
	case "prepare", "runtime_prepare", "runtime_preparing", "source", "source_converting":
		return retryPhasePrepare, nil
	case "confirmation", "confirmation_result", "confirmation_result_write", "write_confirmation", "spec", "generate", "generation", "spec_generating", "spec_generate":
		return retryPhaseSpecGenerate, nil
	case "svg", "svg_generating", "svg_execute":
		return retryPhaseSVGExecute, nil
	case "quality", "quality_checking", "quality_check":
		return retryPhaseQualityCheck, nil
	case "export", "exporting", "finalize", "finalize_export":
		return retryPhaseFinalizeExport, nil
	case "publish", "publishing", "artifact_publish":
		return retryPhasePublish, nil
	default:
		return "", fmt.Errorf("unsupported retry phase %q", requested)
	}
}

func inferRetryPhase(failurePhase string) string {
	value := strings.ToLower(strings.TrimSpace(failurePhase))
	switch {
	case strings.HasPrefix(value, "prepare"), strings.HasPrefix(value, "source"), strings.HasPrefix(value, "route_select"):
		return retryPhasePrepare
	case strings.HasPrefix(value, string(PhaseSVGExecute)), strings.HasPrefix(value, "svg"):
		return retryPhaseSVGExecute
	case strings.HasPrefix(value, string(PhaseQualityCheck)), strings.HasPrefix(value, "quality"):
		return retryPhaseQualityCheck
	case strings.HasPrefix(value, string(PhaseFinalizeExport)), strings.HasPrefix(value, "export"), strings.HasPrefix(value, "finalize"):
		return retryPhaseFinalizeExport
	case strings.HasPrefix(value, "publish"):
		return retryPhasePublish
	default:
		return retryPhaseSpecGenerate
	}
}

func (s *TaskService) ListEvents(ctx context.Context, taskID string, afterSeq int64, limit int) ([]model.TaskEvent, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.ListEvents(ctx, taskID, afterSeq, limit)
}

func (s *TaskService) ListRuntimeRuns(ctx context.Context, taskID string) ([]model.TaskRuntimeRun, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.ListRuntimeRuns(ctx, taskID)
}

func (s *TaskService) ListPhaseRuns(ctx context.Context, taskID string) ([]model.TaskPhaseRun, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.ListPhaseRuns(ctx, taskID)
}

func (s *TaskService) ListArtifacts(ctx context.Context, taskID string) ([]model.Artifact, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.ListArtifacts(ctx, taskID)
}

func (s *TaskService) ListConfirmations(ctx context.Context, taskID string) ([]model.TaskConfirmation, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	confirmations, err := s.repo.ListConfirmations(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return filterConfirmationsForStatus(confirmations, task.Status), nil
}

func (s *TaskService) LatestPPTX(ctx context.Context, taskID string) (*model.Artifact, string, error) {
	artifact, err := s.repo.LatestPPTXArtifact(ctx, taskID)
	if err != nil {
		return nil, "", err
	}
	return artifact, s.storage.Path(artifact.ObjectKey), nil
}

func (s *TaskService) ArtifactFile(ctx context.Context, taskID, artifactID string) (*model.Artifact, string, error) {
	artifact, err := s.repo.GetArtifact(ctx, taskID, artifactID)
	if err != nil {
		return nil, "", err
	}
	return artifact, s.storage.Path(artifact.ObjectKey), nil
}

func (s *TaskService) transition(ctx context.Context, task *model.Task, to, message string, payload map[string]any) error {
	if err := s.machine.Validate(task.Status, to); err != nil {
		return err
	}
	task.Status = to
	task.ErrorMessage = ""
	task.FailurePhase = ""
	task.FailureMetadata = "{}"
	if err := s.repo.SaveTask(ctx, task); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeStatus, to, message, payload)
}

func (s *TaskService) fail(ctx context.Context, task *model.Task, cause error) error {
	return s.failWithMetadata(ctx, task, "", cause, nil, nil)
}

func (s *TaskService) failWithMetadata(ctx context.Context, task *model.Task, phase string, cause error, run *model.TaskRuntimeRun, extra map[string]any) error {
	if phase == "" && run != nil {
		phase = run.FailurePhase
		if phase == "" {
			phase = run.Phase
		}
	}
	metadata := buildFailureMetadata(phase, cause, run, extra)
	task.ErrorMessage = cause.Error()
	task.FailurePhase = phase
	task.FailureMetadata = encodeJSON(metadata)
	if err := s.machine.Validate(task.Status, model.TaskStatusFailed); err != nil {
		return err
	}
	task.Status = model.TaskStatusFailed
	if err := s.repo.SaveTask(ctx, task); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeError, model.TaskStatusFailed, cause.Error(), metadata)
}

func (s *TaskService) event(ctx context.Context, taskID, eventType, status, message string, payload map[string]any) error {
	encoded := "{}"
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	return s.repo.AppendEvent(ctx, &model.TaskEvent{
		TaskID:  taskID,
		Type:    eventType,
		Status:  status,
		Message: message,
		Payload: encoded,
	})
}

func buildFailureMetadata(phase string, cause error, run *model.TaskRuntimeRun, extra map[string]any) map[string]any {
	payload := map[string]any{
		"phase":         phase,
		"error_message": cause.Error(),
	}
	if run != nil {
		payload["runtime_run_id"] = run.ID
		payload["runtime_phase"] = run.Phase
		payload["runtime_status"] = run.Status
		payload["external_run_id"] = run.ExternalRunID
		payload["external_session_id"] = run.ExternalSessionID
		payload["workspace_path"] = run.WorkspacePath
		payload["stderr_tail"] = run.StderrTail
		if run.ExitCode != nil {
			payload["exit_code"] = *run.ExitCode
		}
	}
	for key, value := range extra {
		payload[key] = value
	}
	return payload
}

func encodeJSON(value map[string]any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func (s *TaskService) buildTaskWorkspace(ctx context.Context, task *model.Task) (*TaskWorkspace, error) {
	if s.workspaces == nil {
		s.workspaces = NewRuntimeWorkspaceBuilder(s.agentCfg, s.storage)
	}
	artifacts, err := s.repo.ListArtifacts(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	var sources []model.Artifact
	for _, artifact := range artifacts {
		if artifact.Kind == model.ArtifactKindSource {
			sources = append(sources, artifact)
		}
	}
	workspace, err := s.workspaces.Build(ctx, task, sources)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"workspace":        workspace.HostDir,
		"compose_file":     workspace.ComposeFile,
		"cli_compose_file": workspace.CLIComposeFile,
		"skill_dir":        workspace.SkillDir,
	}
	if task.SelectedTemplateID != "" {
		payload["selected_template_id"] = task.SelectedTemplateID
		payload["template_lock"] = ".slidesmith/template_lock.json"
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "workspace_built", "Task runtime workspace built", payload)
	return workspace, nil
}

func (s *TaskService) resolveTaskWorkspace(task *model.Task) *TaskWorkspace {
	if s.workspaces == nil {
		s.workspaces = NewRuntimeWorkspaceBuilder(s.agentCfg, s.storage)
	}
	return s.workspaces.Resolve(task)
}

func (s *TaskService) syncPreparedProject(ctx context.Context, taskID, runtimeProject, workspacePath, targetWorkspaceDir string) (string, error) {
	if runtimeProject == "" || workspacePath == "" {
		return "", nil
	}
	sourceProjectsDir := filepath.Join(workspacePath, "projects")
	matches, err := filepath.Glob(filepath.Join(sourceProjectsDir, runtimeProject+"_ppt169_*"))
	if err != nil {
		return "", err
	}
	direct := filepath.Join(sourceProjectsDir, runtimeProject)
	if _, err := os.Stat(direct); err == nil {
		matches = append(matches, direct)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("prepared project not found for %s in %s", runtimeProject, sourceProjectsDir)
	}
	sourceProject := newestPath(matches)
	targetProject := filepath.Join(targetWorkspaceDir, "projects", filepath.Base(sourceProject))
	if sameFilesystemPath(sourceProject, targetProject) {
		return targetProject, nil
	}
	if err := os.RemoveAll(targetProject); err != nil {
		return "", err
	}
	if err := copyDir(ctx, sourceProject, targetProject); err != nil {
		return "", err
	}
	return targetProject, s.event(ctx, taskID, model.EventTypeRuntime, "workspace_synced", "Prepared runtime project synced to task workspace", map[string]any{
		"source": sourceProject,
		"target": targetProject,
	})
}

func (s *TaskService) syncRuntimeProject(ctx context.Context, task *model.Task, workspace *TaskWorkspace, runtimeWorkspacePath string) (string, error) {
	if strings.TrimSpace(runtimeWorkspacePath) == "" || workspace == nil {
		return s.findPersistentProjectPath(task)
	}
	return s.syncPreparedProject(ctx, task.ID, task.RuntimeProject, runtimeWorkspacePath, workspace.HostDir)
}

func (s *TaskService) findPersistentProjectPath(task *model.Task) (string, error) {
	runtimeProject := task.RuntimeProject
	if runtimeProject == "" {
		return "", fmt.Errorf("runtime project is empty")
	}
	workspace := s.resolveTaskWorkspace(task)
	projectsDir := filepath.Join(workspace.HostDir, "projects")
	matches, err := filepath.Glob(filepath.Join(projectsDir, runtimeProject+"_ppt169_*"))
	if err != nil {
		return "", err
	}
	direct := filepath.Join(projectsDir, runtimeProject)
	if _, err := os.Stat(direct); err == nil {
		matches = append(matches, direct)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("runtime project not found for %s in %s", runtimeProject, projectsDir)
	}
	return newestPath(matches), nil
}

func sameFilesystemPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil {
		a = absA
	}
	if errB == nil {
		b = absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func newestPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	newest := paths[0]
	newestInfo, newestErr := os.Stat(newest)
	for _, path := range paths[1:] {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if newestErr != nil || info.ModTime().After(newestInfo.ModTime()) {
			newest = path
			newestInfo = info
			newestErr = nil
		}
	}
	return newest
}

func copyDir(ctx context.Context, source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		return copyFile(path, targetPath, info.Mode())
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func (s *TaskService) runAgent(ctx context.Context, task *model.Task, phase string, req AgentRunRequest) (*model.TaskRuntimeRun, error) {
	started := time.Now().UTC()
	req.Phase = phase
	commandForRecord := req.Command
	if commandForRecord == "" && req.Prompt != "" {
		commandForRecord = "[prompt]\n" + req.Prompt
	}
	run := &model.TaskRuntimeRun{
		ID:        uuid.NewString(),
		TaskID:    task.ID,
		Runtime:   "agent-compose",
		Agent:     s.agentCfg.Agent,
		Phase:     phase,
		Command:   commandForRecord,
		Status:    "running",
		StartedAt: &started,
	}
	if err := s.repo.CreateRuntimeRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.agent.Up(ctx, req); err != nil {
		finished := time.Now().UTC()
		run.FinishedAt = &finished
		setRuntimeRunFailure(run, phase+".runtime_up", err)
		_ = s.repo.SaveRuntimeRun(ctx, run)
		return run, err
	}
	result, err := s.agent.Run(ctx, req)
	if result == nil {
		result = &AgentRunResult{}
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	run.ExternalRunID = result.RunID
	run.ExternalSessionID = result.SessionID
	run.WorkspacePath = result.WorkspacePath
	run.RawResponse = result.RawJSON
	run.ExitCode = result.ExitCode
	run.StderrTail = result.StderrTail
	run.Status = result.Status
	if run.Status == "" {
		run.Status = "succeeded"
	}
	if err != nil {
		setRuntimeRunFailure(run, phase+".agent", err)
		_ = s.repo.SaveRuntimeRun(ctx, run)
		return run, err
	}
	if err := s.repo.SaveRuntimeRun(ctx, run); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, run.Status, "Agent Compose run finished", map[string]any{
		"phase":      phase,
		"run_id":     run.ExternalRunID,
		"session_id": run.ExternalSessionID,
	})
	return run, nil
}

func setRuntimeRunFailure(run *model.TaskRuntimeRun, phase string, cause error) {
	run.Status = "failed"
	run.ErrorMessage = cause.Error()
	run.FailurePhase = phase
	if run.StderrTail == "" {
		run.StderrTail = agentErrorStderrTail(cause)
	}
	run.FailureMetadata = encodeJSON(buildFailureMetadata(phase, cause, run, nil))
}

func runtimeProjectName(taskID string) string {
	value := strings.ToLower(taskID)
	value = strings.ReplaceAll(value, "-", "_")
	return "task_" + value
}

func (s *TaskService) normalizedRunnerProfile() string {
	switch strings.ToLower(strings.TrimSpace(s.agentCfg.RunnerProfile)) {
	case "smoke", "real-lite", "full", "full-ppt-master":
		return strings.ToLower(strings.TrimSpace(s.agentCfg.RunnerProfile))
	default:
		return "real-lite"
	}
}

func (s *TaskService) commandRunnerProfile() string {
	switch s.normalizedRunnerProfile() {
	case "smoke":
		return "smoke"
	default:
		return "real-lite"
	}
}

func (s *TaskService) useFullPPTMaster() bool {
	switch s.normalizedRunnerProfile() {
	case "full", "full-ppt-master":
		return true
	default:
		return false
	}
}

func (s *TaskService) fullPPTMasterSpecPrompt(task *model.Task, projectPath string) string {
	projectRel := s.projectRel(task, projectPath)
	return fmt.Sprintf(`You are running inside the SlideSmith PPT Master runtime workspace.

Goal:
Generate only the PPT Master strategist artifacts for SlideSmith task %s.

Hard boundaries:
- Use the already prepared project directory: %s
- Read .slidesmith/runtime_manifest.json first.
- If .slidesmith/template_lock.json exists, read it and treat its selected template as immutable for this task.
- Read .slidesmith/template_resolution.json when present and use its template_root as the selected template package.
- Read the core PPT Master skill declared by runtime_manifest.json: skills/ppt-master/SKILL.md.
- Read strategist references when present: skills/ppt-master/references/strategist.md, skills/ppt-master/templates/design_spec_reference.md, and skills/ppt-master/templates/spec_lock_reference.md.
- Prefer the selected template package listed in runtime_manifest.json template_roots when deriving visual structure, colors, page roles, and style.
- Treat %s/sources/ as the source material. Keep facts grounded in those files.
- Read %s/confirm_ui/result.json and follow the confirmed canvas, page_count, language, audience, mode, visual_style, color, typography, icons, formula_policy, image_usage, generation_mode, and refine_spec.
- Do not ask the user questions. The confirmation file is final for this run.
- Do not create a different project unless %s is missing.
- You must actually execute shell commands and write files in the workspace. A text-only explanation is a failed run.

Required output contract:
1. Create or overwrite %s/design_spec.md with a real PPT Master design specification.
2. Create or overwrite %s/spec_lock.md and keep it consistent with design_spec.md.
3. The spec must match the confirmed page_count exactly when it is a valid integer from 3 to 10. If it is missing or invalid, use 3 slides.

Strict prohibitions for this phase:
- Do not create %s/svg_output/*.svg.
- Do not create or modify %s/svg_final/.
- Do not create or modify %s/exports/.
- Do not run svg_quality_checker.py, finalize_svg.py, svg_to_pptx.py, or scripts/ppt_runner.py publish.
- Do not write or run Python/Node/shell generators for SVG pages.

Before stopping:
- Verify with shell commands that %s/design_spec.md and %s/spec_lock.md exist and are non-empty.
- Verify that %s/svg_output has no .svg files.
`, task.ID, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel)
}

func (s *TaskService) fullPPTMasterSVGPrompt(task *model.Task, projectPath string) string {
	projectRel := s.projectRel(task, projectPath)
	return fmt.Sprintf(`You are running inside the SlideSmith PPT Master runtime workspace.

Goal:
Generate only the PPT Master executor SVG pages for SlideSmith task %s.

Hard boundaries:
- Use the already prepared project directory: %s
- Read .slidesmith/runtime_manifest.json first.
- If .slidesmith/template_lock.json exists, read it and treat its selected template as immutable for this task.
- Read .slidesmith/template_resolution.json when present and use its template_root as the selected template package.
- Read the core PPT Master skill declared by runtime_manifest.json: skills/ppt-master/SKILL.md.
- Read %s/design_spec.md and %s/spec_lock.md before writing any SVG.
- Prefer the selected template package listed in runtime_manifest.json template_roots when executing page layout and style.
- Read executor references when present: skills/ppt-master/references/executor-base.md and skills/ppt-master/references/shared-standards.md.
- Treat %s/sources/ as the source material. Keep facts grounded in those files.
- Read %s/confirm_ui/result.json and follow the confirmed canvas, page_count, language, visual_style, color, typography, icons, formula_policy, image_usage, and generation_mode.
- Do not ask the user questions. The confirmation file and spec files are final for this run.
- You must actually execute shell commands and write files in the workspace. A text-only explanation is a failed run.

Required output contract:
1. Create exactly the confirmed page_count SVG pages under %s/svg_output/.
2. Create %s/notes/total.md.
3. Re-read %s/spec_lock.md before generating each page.
4. Author SVG pages one page at a time. Do not write or run Python/Node/shell generators that loop over page data, template SVG pages, or batch-generate SVG files.

Strict prohibitions for this phase:
- Do not change the strategy in %s/design_spec.md.
- Do not change final confirmations in %s/confirm_ui/result.json.
- Do not run svg_quality_checker.py, finalize_svg.py, svg_to_pptx.py, or scripts/ppt_runner.py publish.
- Do not create or modify %s/svg_final/.
- Do not create or modify %s/exports/.

Before stopping:
- Verify with shell commands that the SVG count under %s/svg_output/ equals confirmed page_count.
- Verify that %s/notes/total.md exists and is non-empty.
- Verify that %s/exports/ contains no .pptx files.
`, task.ID, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel)
}

func (s *TaskService) fullPPTMasterPrompt(task *model.Task, projectPath string) string {
	projectRel := s.projectRel(task, projectPath)

	return fmt.Sprintf(`You are running inside the SlideSmith PPT Master runtime workspace.

Goal:
Generate the final PPTX for SlideSmith task %s by using the full PPT Master workflow, not the SlideSmith real-lite mock generator.

Hard boundaries:
- Use the already prepared project directory: %s
- Read .slidesmith/runtime_manifest.json first.
- If .slidesmith/template_lock.json exists, read it and treat its selected template as immutable for this task.
- Read .slidesmith/template_resolution.json when present and use its template_root as the selected template package.
- Read the core PPT Master skill declared by runtime_manifest.json: skills/ppt-master/SKILL.md.
- Follow the PPT Master mandatory serial workflow rules from that workspace skill.
- Prefer the selected template package listed in runtime_manifest.json template_roots when deriving style and executing page layouts.
- Use workspace scripts from skills/ppt-master/scripts. If and only if a required workspace script is missing, fall back to /opt/ppt-master/skills/ppt-master/scripts and explicitly log that fallback.
- Treat %s/sources/ as the source material. Keep facts grounded in those files.
- Read %s/confirm_ui/result.json and follow the confirmed canvas, page_count, language, audience, mode, visual_style, color, typography, icons, formula_policy, image_usage, generation_mode, and refine_spec.
- Do not ask the user questions. The confirmation file is the final user confirmation for this run.
- Do not create a different project unless %s is missing.
- Keep all final artifacts inside %s.
- You must actually execute shell commands and write files in the workspace. A text-only explanation is a failed run.
- Match the confirmed page_count exactly when it is a valid integer from 3 to 10. If it is missing or invalid, generate 3 slides.
- SVG pages must be hand-authored one page at a time. Do not write or run Python/Node/shell generators that loop over page data, template SVG pages, or batch-generate SVG files.

Required output contract:
1. Create or overwrite %s/design_spec.md with a real PPT Master design specification.
2. Create or overwrite %s/spec_lock.md and keep it consistent with design_spec.md.
3. Create SVG pages under %s/svg_output/.
4. Run PPT Master quality/finalization/export commands:
   - python3 skills/ppt-master/scripts/svg_quality_checker.py %s
   - python3 skills/ppt-master/scripts/finalize_svg.py %s --quiet
   - python3 skills/ppt-master/scripts/svg_to_pptx.py %s --no-notes -t none
5. Run python3 scripts/ppt_runner.py publish --project-path %s so .slidesmith/artifacts.json is written for the platform.
6. Verify with shell commands that at least one PPTX exists under %s/exports/ and that .slidesmith/artifacts.json exists before stopping.

Implementation notes:
- Prefer local icons/shapes and deterministic SVG composition for this MVP run.
- If external image acquisition is unavailable, continue with icons, diagrams, charts, and structured visual blocks rather than failing only because images are unavailable.
- Keep slide count aligned with confirm_ui/result.json when feasible.
- If a command fails, inspect the error, fix the project artifacts, and rerun the failed command before stopping.
- Do not print long script help output or large source excerpts. Read files quietly, then act.
- Use shell heredocs when writing design_spec.md, spec_lock.md, and individual SVG files; author each SVG file explicitly rather than generating SVGs from code.
- Keep each SVG simple and valid, using local text, shapes, and inline icon-like line drawings; avoid external images for this run.
`, task.ID, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel, projectRel)
}

func (s *TaskService) projectRel(task *model.Task, projectPath string) string {
	projectRel := filepath.ToSlash(filepath.Join("projects", filepath.Base(projectPath)))
	workspace := s.resolveTaskWorkspace(task)
	if rel, err := filepath.Rel(workspace.HostDir, projectPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		projectRel = filepath.ToSlash(rel)
	}
	return projectRel
}

func shellArg(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func valueString(values map[string]any, key, fallback string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return fallback
		}
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func firstValueString(values map[string]any, keys []string, fallback string) string {
	for _, key := range keys {
		value := valueString(values, key, "")
		if value != "" {
			return value
		}
	}
	return fallback
}

func valueBool(values map[string]any, key string, fallback bool) bool {
	value, ok := values[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "1", "on":
			return true
		case "false", "no", "0", "off":
			return false
		default:
			return fallback
		}
	default:
		return fallback
	}
}

func confirmationImageUsage(values map[string]any) []string {
	value, ok := values["image_usage"]
	if !ok || value == nil {
		value = values["asset_strategy"]
	}
	switch typed := value.(type) {
	case []string:
		return normalizeImageUsage(typed)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, fmt.Sprint(item))
		}
		return normalizeImageUsage(items)
	case string:
		return normalizeImageUsage([]string{typed})
	default:
		return []string{"none"}
	}
}

func normalizeImageUsage(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		switch value {
		case "", "none", "no-images", "icons-only":
			value = "none"
		case "web-images", "web-sourced":
			value = "web"
		case "generated-images", "generated", "ai-generated":
			value = "ai"
		case "user", "provided":
			value = "provided"
		}
		if value == "none" {
			return []string{"none"}
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

func confirmationValues(confirmations []model.TaskConfirmation) map[string]any {
	values := map[string]any{}
	for _, confirmation := range confirmations {
		var value any
		if confirmation.ValueJSON != "" && confirmation.ValueJSON != "null" {
			if err := json.Unmarshal([]byte(confirmation.ValueJSON), &value); err != nil {
				value = nil
			}
		}
		if value == nil && confirmation.Recommendation != "" {
			value = confirmation.Recommendation
		}
		values[confirmation.Key] = value
	}
	return values
}

var tier1ConfirmationKeys = []string{
	"canvas",
	"language",
	"audience",
	"content_divergence",
	"delivery_purpose",
	"mode",
	"visual_style",
}

var tier2ConfirmationKeys = []string{
	"page_count",
	"color",
	"typography",
	"icons",
	"formula_policy",
	"image_usage",
	"image_notes",
	"generation_mode",
	"refine_spec",
}

func filterConfirmationsForStatus(confirmations []model.TaskConfirmation, status string) []model.TaskConfirmation {
	switch status {
	case model.TaskStatusAwaitingAnchorConfirm:
		return filterConfirmationsByKeys(confirmations, tier1ConfirmationKeys)
	case model.TaskStatusRealizationDeriving, model.TaskStatusAwaitingRealizationConfirm:
		return filterConfirmationsByKeys(confirmations, tier2ConfirmationKeys)
	default:
		return confirmations
	}
}

func filterConfirmationsByKeys(confirmations []model.TaskConfirmation, keys []string) []model.TaskConfirmation {
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	out := make([]model.TaskConfirmation, 0, len(keys))
	for _, confirmation := range confirmations {
		if allowed[confirmation.Key] {
			out = append(out, confirmation)
		}
	}
	return out
}

func tier2Confirmations(anchorValues map[string]any) []model.TaskConfirmation {
	visualStyle := valueString(anchorValues, "visual_style", "business")
	mode := valueString(anchorValues, "mode", "pyramid")
	deliveryPurpose := valueString(anchorValues, "delivery_purpose", "balanced")
	pageCount := recommendedPageCount(mode, deliveryPurpose)
	color := recommendedColor(visualStyle)
	typography := recommendedTypography(deliveryPurpose)
	imageUsage := recommendedImageUsage(visualStyle)
	imageNotes := "优先使用结构化图形和图标；如选择 AI 或 Web 图片，仅用于封面、章节页或需要强视觉证据的页面。"
	return []model.TaskConfirmation{
		{Key: "page_count", Label: "页数", Required: true, OptionsJSON: `["3","5","8","10"]`, Recommendation: pageCount},
		{Key: "color", Label: "色彩方案", Required: true, OptionsJSON: colorOptions(visualStyle), Recommendation: color},
		{Key: "typography", Label: "字体与字号", Required: true, OptionsJSON: typographyOptions(deliveryPurpose), Recommendation: typography},
		{Key: "icons", Label: "图标体系", Required: true, OptionsJSON: `["tabler-outline","lucide-outline","emoji","none"]`, Recommendation: "tabler-outline"},
		{Key: "formula_policy", Label: "公式处理", Required: true, OptionsJSON: `["none","mixed","render-all","text-only"]`, Recommendation: "none"},
		{Key: "image_usage", Label: "图片来源", Required: true, OptionsJSON: `["none","provided","web","ai"]`, Recommendation: imageUsage},
		{Key: "image_notes", Label: "图片策略说明", Required: false, Recommendation: imageNotes},
		{Key: "generation_mode", Label: "生成模式", Required: true, OptionsJSON: `["continuous","split"]`, Recommendation: "continuous"},
		{Key: "refine_spec", Label: "先审查设计规格", Required: true, OptionsJSON: `["false","true"]`, Recommendation: "false"},
	}
}

func recommendedPageCount(mode, deliveryPurpose string) string {
	if deliveryPurpose == "text" {
		return "8"
	}
	if deliveryPurpose == "presentation" {
		return "5"
	}
	if mode == "briefing" {
		return "3"
	}
	return "5"
}

func recommendedColor(visualStyle string) string {
	switch visualStyle {
	case "dark-tech":
		return "深色科技：#101820 / #1F7A8C / #F2C14E"
	case "editorial":
		return "编辑质感：#F7F3EA / #24323F / #B54B3A"
	case "swiss-minimal":
		return "瑞士极简：#F7F7F2 / #27313B / #C46A2D"
	default:
		return "商务克制：#F4F7F8 / #263238 / #1E7A82"
	}
}

func colorOptions(visualStyle string) string {
	recommended := recommendedColor(visualStyle)
	options := []string{
		recommended,
		"高对比商务：#FFFFFF / #18202A / #2F665D",
		"温和编辑：#FBF6EF / #30343B / #C46A2D",
	}
	raw, _ := json.Marshal(options)
	return string(raw)
}

func recommendedTypography(deliveryPurpose string) string {
	switch deliveryPurpose {
	case "text":
		return "Noto Sans CJK SC + Inter，正文 20px"
	case "presentation":
		return "Noto Sans CJK SC + Inter，正文 32px"
	default:
		return "Noto Sans CJK SC + Inter，正文 24px"
	}
}

func typographyOptions(deliveryPurpose string) string {
	options := []string{
		recommendedTypography(deliveryPurpose),
		"Source Han Serif SC + Inter，标题更有报告感",
		"Noto Sans CJK SC + IBM Plex Sans，适合数据型页面",
	}
	raw, _ := json.Marshal(options)
	return string(raw)
}

func recommendedImageUsage(visualStyle string) string {
	switch visualStyle {
	case "editorial", "photo-editorial":
		return "web"
	case "dark-tech":
		return "ai"
	default:
		return "none"
	}
}

func defaultConfirmations() []model.TaskConfirmation {
	confirmations := []model.TaskConfirmation{
		{Key: "canvas", Label: "画布", Required: true, OptionsJSON: `["ppt169","ppt43"]`, Recommendation: "ppt169"},
		{Key: "language", Label: "语言", Required: true, OptionsJSON: `["zh-CN","en-US"]`, Recommendation: "zh-CN"},
		{Key: "audience", Label: "目标受众", Required: true, Recommendation: "面向业务评审者，强调结论、证据和落地步骤。"},
		{Key: "content_divergence", Label: "内容改写边界", Required: false, Recommendation: "在忠于资料事实的前提下，允许重组结构并补充表达。"},
		{Key: "delivery_purpose", Label: "阅读场景", Required: true, OptionsJSON: `["text","balanced","presentation"]`, Recommendation: "balanced"},
		{Key: "mode", Label: "叙事模式", Required: true, OptionsJSON: `["pyramid","briefing","narrative","instructional"]`, Recommendation: "pyramid"},
		{Key: "visual_style", Label: "视觉风格", Required: true, OptionsJSON: `["business","swiss-minimal","editorial","dark-tech"]`, Recommendation: "business"},
	}
	return append(confirmations, tier2Confirmations(confirmationValues(confirmations))...)
}
