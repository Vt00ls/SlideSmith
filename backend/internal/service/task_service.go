package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

	beforeCanonicalMutationPromotion func(string) error
	beforeTemplateFillAPICommit      func(string)
	beforeTemplateFillPromotionLock  func()
}

const (
	retryPhasePrepare              = "prepare"
	retryPhaseSpecGenerate         = "spec_generate"
	retryPhaseSVGExecute           = "svg_execute"
	retryPhaseQualityCheck         = "quality_check"
	retryPhaseFinalizeExport       = "finalize_export"
	retryPhaseTemplateFillPlan     = "template_fill_plan"
	retryPhaseTemplateFillCheck    = "template_fill_check"
	retryPhaseTemplateFillApply    = "template_fill_apply"
	retryPhaseTemplateFillValidate = "template_fill_validate"
	retryPhasePublish              = "publish"

	sourcePrepareAwaitingAnchorFailure = "source_prepare.awaiting_anchor_confirm"
	taskExecutionLeaseBuffer           = 5 * time.Minute
)

var errTaskStateChanged = errors.New("task status or execution claim changed")

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
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
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
	staleBefore := time.Now().UTC().Add(-s.taskExecutionLeaseDuration())
	tasks, err := s.repo.ListClaimableTasksByStatuses(ctx, []string{
		model.TaskStatusRuntimePreparing,
		model.TaskStatusSourceConverting,
		model.TaskStatusTemplateFillPlanning,
		model.TaskStatusTemplateFillChecking,
		model.TaskStatusTemplateFillApplying,
		model.TaskStatusTemplateFillValidating,
		model.TaskStatusSpecGenerating,
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
		model.TaskStatusPublishing,
	}, staleBefore, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for i := range tasks {
		claimed, err := s.processTaskOnce(ctx, tasks[i].ID)
		if err != nil {
			return processed, err
		}
		if claimed {
			processed++
		}
	}
	return processed, nil
}

func (s *TaskService) ProcessTask(ctx context.Context, taskID string) error {
	_, err := s.processTaskOnce(ctx, taskID)
	return err
}

func (s *TaskService) processTaskOnce(ctx context.Context, taskID string) (claimed bool, err error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	if !isWorkerTaskStatus(task.Status) {
		return false, nil
	}
	claimToken := uuid.NewString()
	claimedAt := time.Now().UTC()
	staleBefore := claimedAt.Add(-s.taskExecutionLeaseDuration())
	claimed, err = s.repo.ClaimTaskExecution(ctx, task.ID, task.Status, claimToken, claimedAt, staleBefore)
	if err != nil || !claimed {
		return claimed, err
	}
	task.ExecutionClaimToken = claimToken
	task.ExecutionClaimedAt = &claimedAt
	defer func() {
		_, releaseErr := s.repo.ReleaseTaskExecution(context.WithoutCancel(ctx), task.ID, claimToken)
		if releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release task execution claim: %w", releaseErr))
		}
	}()
	err = s.processClaimedTask(ctx, task)
	return true, err
}

func (s *TaskService) processClaimedTask(ctx context.Context, task *model.Task) error {
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		phase := runnerProfileFailurePhase(err)
		_ = s.failWithMetadata(ctx, task, phase, err, nil, map[string]any{
			"effective_profile": task.RunnerProfile,
			"task_status":       task.Status,
		})
		return err
	}
	switch task.Status {
	case model.TaskStatusRuntimePreparing, model.TaskStatusSourceConverting:
		return s.processPrepare(ctx, task)
	case model.TaskStatusTemplateFillPlanning:
		return s.processTemplateFillPlan(ctx, task)
	case model.TaskStatusTemplateFillChecking:
		return s.processTemplateFillCheck(ctx, task)
	case model.TaskStatusTemplateFillApplying:
		return s.processTemplateFillApply(ctx, task)
	case model.TaskStatusTemplateFillValidating:
		return s.processTemplateFillValidate(ctx, task)
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

func isWorkerTaskStatus(status string) bool {
	switch status {
	case model.TaskStatusRuntimePreparing,
		model.TaskStatusSourceConverting,
		model.TaskStatusTemplateFillPlanning,
		model.TaskStatusTemplateFillChecking,
		model.TaskStatusTemplateFillApplying,
		model.TaskStatusTemplateFillValidating,
		model.TaskStatusSpecGenerating,
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
		model.TaskStatusPublishing:
		return true
	default:
		return false
	}
}

func (s *TaskService) taskExecutionLeaseDuration() time.Duration {
	timeout := s.agentCfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return 2*timeout + taskExecutionLeaseBuffer
}

func (s *TaskService) processPrepare(ctx context.Context, task *model.Task) error {
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return err
	}
	if task.Status == model.TaskStatusRuntimePreparing {
		transitioned, err := s.transitionIfCurrent(ctx, task, model.TaskStatusSourceConverting, "Source converting", nil)
		if err != nil {
			return err
		}
		if !transitioned {
			return nil
		}
	} else if task.Status != model.TaskStatusSourceConverting {
		return fmt.Errorf("source preparation cannot resume from status %q", task.Status)
	}
	if !s.agentCfg.Enabled && task.RunnerProfile == model.RunnerProfileFullPPTMaster {
		workspace := s.resolveTaskWorkspace(task)
		report, err := s.runFullRuntimePreflight(ctx, task, workspace)
		if err == nil {
			err = fmt.Errorf("full runtime preflight failed: agent-compose is disabled")
		}
		_ = s.failWithMetadata(ctx, task, "source_prepare.full_runtime_preflight", err, nil, map[string]any{
			"effective_profile": task.RunnerProfile,
			"preflight":         report,
		})
		return err
	}
	if !s.agentCfg.Enabled {
		_, err := s.transitionIfCurrent(ctx, task, model.TaskStatusAwaitingAnchorConfirm, "Awaiting anchor confirmation", map[string]any{"runtime": "disabled"})
		return err
	}

	workspace, err := s.buildTaskWorkspace(ctx, task)
	if err != nil {
		failurePhase := "prepare.workspace"
		extra := map[string]any{}
		if task.RunnerProfile == model.RunnerProfileFullPPTMaster {
			selection, selectionErr := s.selectRoute(ctx, task)
			if selectionErr == nil && selection.Route == model.TaskRouteMain {
				failurePhase = "source_prepare.full_runtime_preflight"
				resolved := s.resolveTaskWorkspace(task)
				report, preflightErr := s.runFullRuntimePreflight(ctx, task, resolved)
				extra["preflight"] = report
				if preflightErr != nil {
					err = errors.Join(err, preflightErr)
				}
			}
		}
		_ = s.failWithMetadata(ctx, task, failurePhase, err, nil, extra)
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
	if err := s.workspaces.WriteRuntimeManifest(workspace, task, ""); err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseRouteSelect)+".runtime_manifest", err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return err
	}
	var fullPreflight *fullRuntimePreflightReport
	if selection.Route == model.TaskRouteMain && task.RunnerProfile == model.RunnerProfileFullPPTMaster {
		preflight, err := s.runFullRuntimePreflight(ctx, task, workspace)
		fullPreflight = preflight
		if err != nil {
			_ = s.failWithMetadata(ctx, task, "source_prepare.full_runtime_preflight", err, nil, map[string]any{
				"workspace_path":    workspace.HostDir,
				"effective_profile": task.RunnerProfile,
				"preflight":         preflight,
			})
			return err
		}
	}
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{
			"workspace_path":    workspace.HostDir,
			"effective_profile": task.RunnerProfile,
		})
		return err
	}
	command := fmt.Sprintf(
		"node workflows/ppt_workflow.js prepare --profile %s --sources-manifest %s --input %s --project %s",
		shellArg(s.commandRunnerProfile(task)),
		shellArg(".slidesmith/source_inputs.json"),
		shellArg(workspace.InputPath),
		shellArg(task.RuntimeProject),
	)
	phaseInput := map[string]any{
		"command":                  command,
		"workspace_path":           workspace.HostDir,
		"sources_manifest":         ".slidesmith/source_inputs.json",
		"input_path":               workspace.InputPath,
		"runner_profile":           task.RunnerProfile,
		"runner_profile_locked_at": task.RunnerProfileLockedAt,
	}
	if fullPreflight != nil {
		phaseInput["full_runtime_preflight"] = fullPreflight
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSourcePrepare, PhaseRunnerAgent, phaseInput)
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
		if errors.Is(err, errTaskStateChanged) {
			return s.recoverSourcePrepareFailure(
				ctx,
				task,
				phaseRun,
				string(PhaseSourcePrepare)+".runtime",
				err,
				run,
				runtimeRunPhaseOutput(run),
				map[string]any{"workspace_path": workspace.HostDir},
			)
		}
		failurePhase := string(PhaseSourcePrepare) + ".agent"
		stderrTail := ""
		if run != nil {
			stderrTail = run.StderrTail
		}
		if task.RunnerProfile == model.RunnerProfileFullPPTMaster && strings.Contains(strings.ToLower(err.Error()+" "+stderrTail), "full runtime preflight") {
			failurePhase = "source_prepare.full_runtime_preflight"
		}
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, failurePhase, err, run, nil)
		return err
	}
	var preparedProjectPath string
	if run.WorkspacePath != "" {
		projectPath, err := s.syncPreparedProject(ctx, task, run.WorkspacePath, workspace.HostDir)
		if err != nil {
			if errors.Is(err, errTaskStateChanged) {
				return s.recoverSourcePrepareFailure(
					ctx,
					task,
					phaseRun,
					string(PhaseSourcePrepare)+".sync",
					err,
					run,
					runtimeRunPhaseOutput(run),
					map[string]any{"workspace_path": run.WorkspacePath},
				)
			}
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
	if task.RunnerProfile == model.RunnerProfileFullPPTMaster && selection.Route == model.TaskRouteMain {
		runtimePreflight, err := validateRuntimeFullPreflightContract(preparedProjectPath, task.RunnerProfile)
		if err != nil {
			output := runtimeRunPhaseOutput(run)
			output["project_path"] = preparedProjectPath
			output["full_runtime_preflight"] = runtimePreflight
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, output, err)
			_ = s.failWithMetadata(ctx, task, "source_prepare.full_runtime_preflight", err, run, map[string]any{
				"project_path": preparedProjectPath,
				"preflight":    runtimePreflight,
			})
			return err
		}
		fullPreflight = runtimePreflight
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
	saved, err := s.repo.SaveTaskIfStatus(ctx, task, model.TaskStatusSourceConverting, task.ExecutionClaimToken)
	if err != nil {
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
	if !saved {
		cause := fmt.Errorf("source_prepare.persist_runtime: %w", errTaskStateChanged)
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), cause)
		persistedTask, reloadErr := s.repo.GetTask(context.WithoutCancel(ctx), task.ID)
		if reloadErr != nil {
			return errors.Join(cause, reloadErr)
		}
		if persistedTask.Status == model.TaskStatusCancelled || persistedTask.ExecutionClaimToken != task.ExecutionClaimToken {
			return nil
		}
		return cause
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
	output["runner_profile"] = task.RunnerProfile
	if fullPreflight != nil {
		output["full_runtime_preflight"] = fullPreflight
	}
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
		return s.failTaskAfterSourcePrepare(ctx, task, policy.FailurePhase, err, run, map[string]any{
			"workspace_path":         workspace.HostDir,
			"project_path":           preparedProjectPath,
			"route":                  selection.Route,
			"route_reason":           selection.Reason,
			"source_contract":        sourceContract,
			"route_execution_policy": policy,
			"next_spec":              policy.NextSpec,
		})
	}
	if selection.Route == model.TaskRouteTemplateFill {
		transitioned, err := s.transitionIfCurrent(ctx, task, model.TaskStatusTemplateFillPlanning, "Template fill planning", map[string]any{
			"runtime_run_id": run.ID,
			"project_path":   preparedProjectPath,
		})
		if err != nil {
			return s.failTaskAfterSourcePrepare(ctx, task, "source_prepare.template_fill_queue", err, run, map[string]any{
				"workspace_path": workspace.HostDir,
				"project_path":   preparedProjectPath,
			})
		}
		if !transitioned {
			return nil
		}
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Template fill plan queued after source prepare", map[string]any{
			"project_path": preparedProjectPath,
		})
		return nil
	}
	templateResolution, err := s.runTemplateResolve(ctx, task, workspace, preparedProjectPath)
	if err != nil {
		return s.failTaskAfterSourcePrepare(ctx, task, string(PhaseTemplateResolve), err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   preparedProjectPath,
		})
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "template_resolved", "Template resolved for task workspace", map[string]any{
		"selected_template_id": templateResolution.SelectedTemplateID,
		"template_root":        templateResolution.TemplateRoot,
		"template_lock":        templateResolution.TemplateLockPath,
	})
	transitioned, err := s.transitionIfCurrent(ctx, task, model.TaskStatusAwaitingAnchorConfirm, "Awaiting anchor confirmation", map[string]any{"runtime_run_id": run.ID})
	if err != nil {
		cause := fmt.Errorf("%s: %w", sourcePrepareAwaitingAnchorFailure, err)
		return s.failTaskAfterSourcePrepare(ctx, task, sourcePrepareAwaitingAnchorFailure, cause, run, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   preparedProjectPath,
			"target_status":  model.TaskStatusAwaitingAnchorConfirm,
		})
	}
	if !transitioned {
		return nil
	}
	return nil
}

func (s *TaskService) processTemplateFillPlan(ctx context.Context, task *model.Task) error {
	workspace := s.resolveTaskWorkspace(task)
	if !s.agentCfg.Enabled {
		cause := fmt.Errorf("agent compose disabled; worker cannot run %s", PhaseTemplateFillPlan)
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillPlan, PhaseRunnerAgent, workspace, "", string(PhaseTemplateFillPlan)+".agent_disabled", cause)
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillPlan, PhaseRunnerAgent, workspace, "", string(PhaseTemplateFillPlan)+".project", err)
	}
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillPlan, PhaseRunnerAgent, workspace, projectPath, err)
	}
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillPlan, PhaseRunnerAgent, workspace, projectPath, err)
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseTemplateFillPlan, PhaseRunnerAgent, inputs)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, nil, string(PhaseTemplateFillPlan)+".phase_run", err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   projectPath,
		})
	}
	planRun, err := s.runAgent(ctx, task, string(PhaseTemplateFillPlan), AgentRunRequest{
		Prompt:      s.templateFillPlanPrompt(task, inputs),
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
		Detached:    true,
	})
	applyRuntimeRunToPhaseRun(phaseRun, planRun)
	if persistErr := s.applyTemplateFillRuntimeRunToTask(ctx, task, planRun); persistErr != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".runtime", persistErr, planRun, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   projectPath,
		})
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".agent", err, planRun, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   projectPath,
		})
	}
	planPreflightFailure := false
	validatedPlanSHA256 := ""
	projectPath, err = s.syncRuntimeProjectValidatedWithFence(ctx, task, workspace, planRun.WorkspacePath, func(stagedProjectPath string) error {
		if stagedErr := provenance.revalidateAuthoritative(); stagedErr != nil {
			planPreflightFailure = true
			return stagedErr
		}
		stagedPlanContract, stagedPlanSHA256, stagedErr := validateTemplateFillPlanContractSnapshotWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			planPreflightFailure = true
			return stagedErr
		}
		if planStatus, _ := stagedPlanContract["plan_status"].(string); planStatus != "draft" {
			planPreflightFailure = true
			return fmt.Errorf("template fill generated plan status = %q, expected %q", planStatus, "draft")
		}
		validatedPlanSHA256 = stagedPlanSHA256
		return nil
	}, func() error {
		fenceErr := provenance.revalidateAuthoritative()
		if fenceErr != nil {
			planPreflightFailure = true
		}
		return fenceErr
	})
	if err != nil {
		failurePhase := string(PhaseTemplateFillPlan) + ".sync"
		if planPreflightFailure {
			failurePhase = string(PhaseTemplateFillPlan) + ".contract"
		}
		return s.failTemplateFillPhase(ctx, task, phaseRun, failurePhase, err, planRun, map[string]any{
			"workspace_path": planRun.WorkspacePath,
			"project_path":   projectPath,
		})
	}
	planContract, canonicalPlanSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err == nil {
		if planStatus, _ := planContract["plan_status"].(string); planStatus != "draft" || canonicalPlanSHA256 != validatedPlanSHA256 {
			err = fmt.Errorf("template fill canonical plan no longer matches validated draft snapshot")
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".contract", err, planRun, map[string]any{
			"project_path": projectPath,
		})
	}

	projectRel := s.projectRel(task, projectPath)
	draftCheckCommand := fmt.Sprintf("python3 scripts/ppt_runner.py template-fill-check --project-path %s", shellArg(projectRel))
	draftCheckRun, err := s.runAgent(ctx, task, string(PhaseTemplateFillCheck), AgentRunRequest{
		Command:     draftCheckCommand,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	if persistErr := s.applyTemplateFillRuntimeRunToTask(ctx, task, draftCheckRun); persistErr != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".draft_check.runtime", persistErr, draftCheckRun, map[string]any{
			"project_path": projectPath,
			"command":      draftCheckCommand,
		})
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".draft_check.command", err, draftCheckRun, map[string]any{
			"project_path": projectPath,
			"command":      draftCheckCommand,
		})
	}
	draftCheckFailurePhase := ""
	projectPath, err = s.syncRuntimeProjectValidatedWithFence(ctx, task, workspace, draftCheckRun.WorkspacePath, func(stagedProjectPath string) error {
		if stagedErr := provenance.revalidateAuthoritative(); stagedErr != nil {
			draftCheckFailurePhase = string(PhaseTemplateFillPlan) + ".draft_check.contract"
			return stagedErr
		}
		stagedInputs, stagedErr := discoverTemplateFillInputsWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			draftCheckFailurePhase = string(PhaseTemplateFillPlan) + ".draft_check.contract"
			return stagedErr
		}
		stagedPlanSHA256, stagedErr := sha256File(stagedInputs.FillPlan)
		if stagedErr != nil {
			draftCheckFailurePhase = string(PhaseTemplateFillPlan) + ".draft_check.plan_changed"
			return stagedErr
		}
		if stagedPlanSHA256 != validatedPlanSHA256 {
			draftCheckFailurePhase = string(PhaseTemplateFillPlan) + ".draft_check.plan_changed"
			return fmt.Errorf("template fill plan changed during draft check: got %s, expected %s", stagedPlanSHA256, validatedPlanSHA256)
		}
		if _, stagedErr := validateTemplateFillCheckContractForPlanWithProvenance(stagedProjectPath, provenance, false, "draft", validatedPlanSHA256); stagedErr != nil {
			draftCheckFailurePhase = string(PhaseTemplateFillPlan) + ".draft_check.contract"
			return stagedErr
		}
		return nil
	}, func() error {
		fenceErr := provenance.revalidateAuthoritative()
		if fenceErr != nil {
			draftCheckFailurePhase = string(PhaseTemplateFillPlan) + ".draft_check.contract"
		}
		return fenceErr
	})
	if err != nil {
		failurePhase := string(PhaseTemplateFillPlan) + ".draft_check.sync"
		if draftCheckFailurePhase != "" {
			failurePhase = draftCheckFailurePhase
		}
		return s.failTemplateFillPhase(ctx, task, phaseRun, failurePhase, err, draftCheckRun, map[string]any{
			"workspace_path": draftCheckRun.WorkspacePath,
			"project_path":   projectPath,
			"plan_sha256":    validatedPlanSHA256,
		})
	}
	planContract, canonicalPlanSHA256, err = validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err == nil {
		if planStatus, _ := planContract["plan_status"].(string); planStatus != "draft" || canonicalPlanSHA256 != validatedPlanSHA256 {
			err = fmt.Errorf("template fill canonical plan changed after draft check promotion")
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".draft_check.plan_changed", err, draftCheckRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  validatedPlanSHA256,
		})
	}
	draftCheckContract, err := validateTemplateFillCheckContractForPlanWithProvenance(projectPath, provenance, false, "draft", validatedPlanSHA256)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".draft_check.contract", err, draftCheckRun, map[string]any{
			"project_path": projectPath,
		})
	}

	output := runtimeRunPhaseOutput(planRun)
	output["project_path"] = projectPath
	output["contract"] = planContract
	output["draft_check"] = runtimeRunPhaseOutput(draftCheckRun)
	output["draft_check_contract"] = draftCheckContract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".phase_run", err, draftCheckRun, map[string]any{
			"project_path": projectPath,
		})
	}
	cancelled, err := s.refreshTemplateFillTask(ctx, task)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".transition", err, draftCheckRun, map[string]any{
			"project_path": projectPath,
		})
	}
	if cancelled {
		return nil
	}
	if err := s.transition(ctx, task, model.TaskStatusAwaitingTemplateFillConfirm, "Awaiting template fill plan confirmation", map[string]any{
		"runtime_run_id":         runtimeRunID(planRun),
		"draft_check_run_id":     runtimeRunID(draftCheckRun),
		"project_path":           projectPath,
		"contract":               planContract,
		"draft_check_contract":   draftCheckContract,
		"draft_check_has_errors": templateFillCheckErrorCount(draftCheckContract) > 0,
	}); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillPlan)+".transition", err, draftCheckRun, map[string]any{
			"project_path": projectPath,
		})
	}
	return nil
}

func (s *TaskService) processTemplateFillCheck(ctx context.Context, task *model.Task) error {
	workspace := s.resolveTaskWorkspace(task)
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillCheck, PhaseRunnerWorker, workspace, "", string(PhaseTemplateFillCheck)+".project", err)
	}
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillCheck, PhaseRunnerWorker, workspace, projectPath, err)
	}
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillCheck, PhaseRunnerWorker, workspace, projectPath, err)
	}
	_, _, planStatus, _, err := readValidatedTemplateFillPlanWithSHA256AndProvenance(projectPath, provenance)
	if err != nil {
		phaseRun, phaseRunErr := s.beginPhaseRun(ctx, task, PhaseTemplateFillCheck, PhaseRunnerWorker, inputs)
		if phaseRunErr != nil {
			return s.failTemplateFillPhase(ctx, task, nil, string(PhaseTemplateFillCheck)+".phase_run", phaseRunErr, nil, map[string]any{"project_path": projectPath})
		}
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".plan_contract", err, nil, map[string]any{"project_path": projectPath})
	}
	if planStatus == "draft" {
		projectPath, err = s.mutateCanonicalProjectClaimed(
			ctx,
			task,
			projectPath,
			func(stagedProjectPath string) error {
				stagedInputs, err := discoverTemplateFillInputsWithProvenance(stagedProjectPath, provenance)
				if err != nil {
					return err
				}
				return removeTemplateFillFormalCheckEvidence(stagedInputs)
			},
			func(stagedProjectPath string) error {
				if err := provenance.revalidateAuthoritative(); err != nil {
					return err
				}
				planContract, _, err := validateTemplateFillPlanContractSnapshotWithProvenance(stagedProjectPath, provenance)
				if err != nil {
					return err
				}
				if status, _ := planContract["plan_status"].(string); status != "draft" {
					return fmt.Errorf("template fill reconciled plan status = %q, expected %q", status, "draft")
				}
				return requireTemplateFillFormalCheckEvidenceAbsentWithProvenance(stagedProjectPath, provenance)
			},
			provenance.revalidateAuthoritative,
		)
		if errors.Is(err, errTaskStateChanged) {
			return nil
		}
		if err != nil {
			failureErr := s.failWithMetadata(ctx, task, string(PhaseTemplateFillCheck)+".cleanup", err, nil, map[string]any{
				"project_path": projectPath,
			})
			return errors.Join(err, failureErr)
		}
		transitioned, transitionErr := s.transitionIfCurrent(ctx, task, model.TaskStatusAwaitingTemplateFillConfirm, "Awaiting template fill plan confirmation", map[string]any{
			"project_path": projectPath,
			"reason":       "formal check requires confirmed plan",
		})
		if transitionErr != nil {
			return transitionErr
		}
		if transitioned {
			_ = s.event(ctx, task.ID, model.EventTypeRuntime, "template_fill_check_reconciled", "Draft plan returned to template fill confirmation", map[string]any{
				"project_path": projectPath,
			})
		}
		return nil
	}
	if !s.agentCfg.Enabled {
		cause := fmt.Errorf("agent compose disabled; worker cannot run %s", PhaseTemplateFillCheck)
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillCheck, PhaseRunnerWorker, workspace, projectPath, string(PhaseTemplateFillCheck)+".agent_disabled", cause)
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseTemplateFillCheck, PhaseRunnerWorker, inputs)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, nil, string(PhaseTemplateFillCheck)+".phase_run", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	planContract, planSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".plan_contract", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	projectRel := s.projectRel(task, projectPath)
	command := templateFillFormalCheckCommand(projectRel)
	runtimeRun, err := s.runAgent(ctx, task, string(PhaseTemplateFillCheck), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, runtimeRun)
	if persistErr := s.applyTemplateFillRuntimeRunToTask(ctx, task, runtimeRun); persistErr != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".runtime", persistErr, runtimeRun, map[string]any{
			"project_path": projectPath,
			"command":      command,
		})
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".command", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"command":      command,
		})
	}
	preflightFailurePhase := ""
	projectPath, err = s.syncRuntimeProjectValidatedWithFence(ctx, task, workspace, runtimeRun.WorkspacePath, func(stagedProjectPath string) error {
		if stagedErr := provenance.revalidateAuthoritative(); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".inputs"
			return stagedErr
		}
		stagedInputs, stagedErr := discoverTemplateFillInputsWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".inputs"
			return stagedErr
		}
		if stagedErr := requireNonEmptyFile(stagedInputs.CheckReport); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".fresh_report"
			return stagedErr
		}
		checkedPlanSHA256, stagedErr := sha256File(stagedInputs.FillPlan)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".plan_changed"
			return stagedErr
		}
		if checkedPlanSHA256 != planSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".plan_changed"
			return fmt.Errorf("template fill plan changed during formal check: got %s, expected %s", checkedPlanSHA256, planSHA256)
		}
		if _, stagedErr := validateTemplateFillCheckContractForPlanWithProvenance(stagedProjectPath, provenance, false, "confirmed", planSHA256); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".contract"
			return stagedErr
		}
		return nil
	}, func() error {
		fenceErr := provenance.revalidateAuthoritative()
		if fenceErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillCheck) + ".inputs"
		}
		return fenceErr
	})
	if err != nil {
		failurePhase := string(PhaseTemplateFillCheck) + ".sync"
		if preflightFailurePhase != "" {
			failurePhase = preflightFailurePhase
		}
		return s.failTemplateFillPhase(ctx, task, phaseRun, failurePhase, err, runtimeRun, map[string]any{
			"workspace_path": runtimeRun.WorkspacePath,
			"project_path":   projectPath,
			"plan_sha256":    planSHA256,
		})
	}
	inputs, err = discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".inputs", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	if err := requireNonEmptyFile(inputs.CheckReport); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".fresh_report", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	planContract, canonicalPlanSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".plan_changed", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	if canonicalPlanSHA256 != planSHA256 {
		cause := fmt.Errorf("template fill canonical plan changed after formal check promotion: got %s, expected %s", canonicalPlanSHA256, planSHA256)
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".plan_changed", cause, runtimeRun, map[string]any{
			"project_path":        projectPath,
			"plan_sha256":         planSHA256,
			"checked_plan_sha256": canonicalPlanSHA256,
		})
	}
	checkContract, err := validateTemplateFillCheckContractForPlanWithProvenance(projectPath, provenance, false, "confirmed", planSHA256)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".contract", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	blockingErrors := templateFillCheckErrorCount(checkContract)
	output := runtimeRunPhaseOutput(runtimeRun)
	output["project_path"] = projectPath
	output["plan_contract"] = planContract
	output["contract"] = checkContract
	output["plan_status"] = "confirmed"
	output["plan_sha256"] = planSHA256
	nextStatus := model.TaskStatusTemplateFillApplying
	message := "Template fill applying"
	blocked := blockingErrors > 0
	if blocked {
		nextStatus = model.TaskStatusAwaitingTemplateFillConfirm
		message = "Awaiting template fill plan confirmation"
	}
	if blocked {
		formalReportSHA256, err := sha256File(inputs.CheckReport)
		if err != nil {
			return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".reset_plan", err, runtimeRun, map[string]any{
				"project_path": projectPath,
			})
		}
		formalContractPath := filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json")
		formalContractSHA256, err := sha256File(formalContractPath)
		if err != nil {
			return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".reset_plan", err, runtimeRun, map[string]any{
				"project_path": projectPath,
			})
		}
		projectPath, err = s.mutateCanonicalProjectClaimed(
			ctx,
			task,
			projectPath,
			func(stagedProjectPath string) error {
				return setTemplateFillPlanStatusWithProvenance(stagedProjectPath, "draft", provenance)
			},
			func(stagedProjectPath string) error {
				if err := provenance.revalidateAuthoritative(); err != nil {
					return err
				}
				stagedPlanContract, _, err := validateTemplateFillPlanContractSnapshotWithProvenance(stagedProjectPath, provenance)
				if err != nil {
					return err
				}
				if status, _ := stagedPlanContract["plan_status"].(string); status != "draft" {
					return fmt.Errorf("template fill reset plan status = %q, expected %q", status, "draft")
				}
				stagedInputs, err := discoverTemplateFillInputsWithProvenance(stagedProjectPath, provenance)
				if err != nil {
					return err
				}
				if err := requireFileSHA256(stagedInputs.CheckReport, formalReportSHA256, "template fill formal check report"); err != nil {
					return err
				}
				stagedContractPath := filepath.Join(stagedProjectPath, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json")
				if err := requireFileSHA256(stagedContractPath, formalContractSHA256, "template fill formal check contract"); err != nil {
					return err
				}
				planContract = stagedPlanContract
				return nil
			},
			provenance.revalidateAuthoritative,
		)
		if errors.Is(err, errTaskStateChanged) {
			return nil
		}
		if err != nil {
			return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".reset_plan_contract", err, runtimeRun, map[string]any{
				"project_path": projectPath,
			})
		}
		output["plan_contract"] = planContract
		output["blocking_errors"] = blockingErrors
	}
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".phase_run", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	cancelled, err := s.refreshTemplateFillTask(ctx, task)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".transition", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	if cancelled {
		return nil
	}
	if err := s.transition(ctx, task, nextStatus, message, map[string]any{
		"runtime_run_id": runtimeRunID(runtimeRun),
		"project_path":   projectPath,
		"contract":       checkContract,
	}); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillCheck)+".transition", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	if blocked {
		_ = s.event(ctx, task.ID, model.EventTypeRuntime, "template_fill_check_blocked", "Template fill check blocked apply", map[string]any{
			"blocking_errors": blockingErrors,
			"project_path":    projectPath,
		})
	}
	return nil
}

func (s *TaskService) processTemplateFillApply(ctx context.Context, task *model.Task) error {
	workspace := s.resolveTaskWorkspace(task)
	if !s.agentCfg.Enabled {
		cause := fmt.Errorf("agent compose disabled; worker cannot run %s", PhaseTemplateFillApply)
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillApply, PhaseRunnerWorker, workspace, "", string(PhaseTemplateFillApply)+".agent_disabled", cause)
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillApply, PhaseRunnerWorker, workspace, "", string(PhaseTemplateFillApply)+".project", err)
	}
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillApply, PhaseRunnerWorker, workspace, projectPath, err)
	}
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillApply, PhaseRunnerWorker, workspace, projectPath, err)
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseTemplateFillApply, PhaseRunnerWorker, inputs)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, nil, string(PhaseTemplateFillApply)+".phase_run", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	planContract, planSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err == nil {
		if planStatus, _ := planContract["plan_status"].(string); planStatus != "confirmed" {
			err = fmt.Errorf("template fill apply plan status = %q, expected %q", planStatus, "confirmed")
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".plan_contract", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	checkContract, err := readTemplateFillFormalCheckEvidence(projectPath, inputs, planSHA256)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".check_contract", err, nil, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	expectedCheckReportSHA256, _ := checkContract["check_report_sha256"].(string)
	projectRel := s.projectRel(task, projectPath)
	command := fmt.Sprintf("python3 scripts/ppt_runner.py template-fill-apply --project-path %s --transition fade", shellArg(projectRel))
	runtimeRun, err := s.runAgent(ctx, task, string(PhaseTemplateFillApply), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, runtimeRun)
	if persistErr := s.applyTemplateFillRuntimeRunToTask(ctx, task, runtimeRun); persistErr != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".runtime", persistErr, runtimeRun, map[string]any{
			"project_path": projectPath,
			"command":      command,
		})
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".command", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"command":      command,
		})
	}
	preflightFailurePhase := ""
	projectPath, err = s.syncRuntimeProjectValidatedWithFence(ctx, task, workspace, runtimeRun.WorkspacePath, func(stagedProjectPath string) error {
		if stagedErr := provenance.revalidateAuthoritative(); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".inputs"
			return stagedErr
		}
		stagedInputs, stagedErr := discoverTemplateFillInputsWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".inputs"
			return stagedErr
		}
		stagedPlanSHA256, stagedErr := sha256File(stagedInputs.FillPlan)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".plan_changed"
			return stagedErr
		}
		if stagedPlanSHA256 != planSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".plan_changed"
			return fmt.Errorf("template fill plan changed during apply: got %s, expected %s", stagedPlanSHA256, planSHA256)
		}
		stagedPlanContract, validatedPlanSHA256, stagedErr := validateTemplateFillPlanContractSnapshotWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".plan_contract"
			return stagedErr
		}
		if planStatus, _ := stagedPlanContract["plan_status"].(string); planStatus != "confirmed" || validatedPlanSHA256 != planSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".plan_changed"
			return fmt.Errorf("template fill apply staged plan no longer matches confirmed snapshot")
		}
		stagedCheckContract, stagedErr := readTemplateFillFormalCheckEvidenceForReport(stagedProjectPath, stagedInputs, planSHA256, inputs.CheckReport)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".check_contract"
			return stagedErr
		}
		if stagedReportSHA256, _ := stagedCheckContract["check_report_sha256"].(string); stagedReportSHA256 != expectedCheckReportSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".check_contract"
			return fmt.Errorf("template fill apply check report sha256 = %q, expected %q", stagedReportSHA256, expectedCheckReportSHA256)
		}
		if _, stagedErr := validateTemplateFillApplyContractWithProvenance(stagedProjectPath, provenance); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".contract"
			return stagedErr
		}
		return nil
	}, func() error {
		fenceErr := provenance.revalidateAuthoritative()
		if fenceErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillApply) + ".inputs"
		}
		return fenceErr
	})
	if err != nil {
		failurePhase := string(PhaseTemplateFillApply) + ".sync"
		if preflightFailurePhase != "" {
			failurePhase = preflightFailurePhase
		}
		return s.failTemplateFillPhase(ctx, task, phaseRun, failurePhase, err, runtimeRun, map[string]any{
			"workspace_path": runtimeRun.WorkspacePath,
			"project_path":   projectPath,
			"plan_sha256":    planSHA256,
		})
	}
	inputs, err = discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".inputs", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	planContract, canonicalPlanSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err == nil {
		if planStatus, _ := planContract["plan_status"].(string); planStatus != "confirmed" || canonicalPlanSHA256 != planSHA256 {
			err = fmt.Errorf("template fill apply canonical plan no longer matches confirmed snapshot")
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".plan_changed", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	checkContract, err = readTemplateFillFormalCheckEvidence(projectPath, inputs, planSHA256)
	if err == nil {
		if canonicalReportSHA256, _ := checkContract["check_report_sha256"].(string); canonicalReportSHA256 != expectedCheckReportSHA256 {
			err = fmt.Errorf("template fill apply canonical check report sha256 = %q, expected %q", canonicalReportSHA256, expectedCheckReportSHA256)
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".check_contract", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	applyContract, err := validateTemplateFillApplyContractWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".contract", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	output := runtimeRunPhaseOutput(runtimeRun)
	output["project_path"] = projectPath
	output["plan_contract"] = planContract
	output["check_contract"] = checkContract
	output["contract"] = applyContract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".phase_run", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	cancelled, err := s.refreshTemplateFillTask(ctx, task)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".transition", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	if cancelled {
		return nil
	}
	if err := s.transition(ctx, task, model.TaskStatusTemplateFillValidating, "Template fill validating", map[string]any{
		"runtime_run_id": runtimeRunID(runtimeRun),
		"project_path":   projectPath,
		"contract":       applyContract,
	}); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillApply)+".transition", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	return nil
}

func (s *TaskService) processTemplateFillValidate(ctx context.Context, task *model.Task) error {
	workspace := s.resolveTaskWorkspace(task)
	if !s.agentCfg.Enabled {
		cause := fmt.Errorf("agent compose disabled; worker cannot run %s", PhaseTemplateFillValidate)
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillValidate, PhaseRunnerWorker, workspace, "", string(PhaseTemplateFillValidate)+".agent_disabled", cause)
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return s.failTemplateFillPreflight(ctx, task, PhaseTemplateFillValidate, PhaseRunnerWorker, workspace, "", string(PhaseTemplateFillValidate)+".project", err)
	}
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillValidate, PhaseRunnerWorker, workspace, projectPath, err)
	}
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillInputs(ctx, task, PhaseTemplateFillValidate, PhaseRunnerWorker, workspace, projectPath, err)
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseTemplateFillValidate, PhaseRunnerWorker, inputs)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, nil, string(PhaseTemplateFillValidate)+".phase_run", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	planContract, planSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err == nil {
		if planStatus, _ := planContract["plan_status"].(string); planStatus != "confirmed" {
			err = fmt.Errorf("template fill validate plan status = %q, expected %q", planStatus, "confirmed")
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".plan_contract", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	checkContract, err := readTemplateFillFormalCheckEvidence(projectPath, inputs, planSHA256)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".check_contract", err, nil, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	expectedCheckReportSHA256, _ := checkContract["check_report_sha256"].(string)
	applyContract, err := validateTemplateFillApplyContractWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".apply_contract", err, nil, map[string]any{
			"project_path": projectPath,
		})
	}
	projectRel := s.projectRel(task, projectPath)
	command := fmt.Sprintf("python3 scripts/ppt_runner.py template-fill-validate --project-path %s", shellArg(projectRel))
	runtimeRun, err := s.runAgent(ctx, task, string(PhaseTemplateFillValidate), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, runtimeRun)
	if persistErr := s.applyTemplateFillRuntimeRunToTask(ctx, task, runtimeRun); persistErr != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".runtime", persistErr, runtimeRun, map[string]any{
			"project_path": projectPath,
			"command":      command,
		})
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".command", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"command":      command,
		})
	}
	preflightFailurePhase := ""
	projectPath, err = s.syncRuntimeProjectValidatedWithFence(ctx, task, workspace, runtimeRun.WorkspacePath, func(stagedProjectPath string) error {
		if stagedErr := provenance.revalidateAuthoritative(); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".inputs"
			return stagedErr
		}
		stagedInputs, stagedErr := discoverTemplateFillInputsWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".inputs"
			return stagedErr
		}
		stagedPlanSHA256, stagedErr := sha256File(stagedInputs.FillPlan)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".plan_changed"
			return stagedErr
		}
		if stagedPlanSHA256 != planSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".plan_changed"
			return fmt.Errorf("template fill plan changed during validate: got %s, expected %s", stagedPlanSHA256, planSHA256)
		}
		stagedPlanContract, validatedPlanSHA256, stagedErr := validateTemplateFillPlanContractSnapshotWithProvenance(stagedProjectPath, provenance)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".plan_contract"
			return stagedErr
		}
		if planStatus, _ := stagedPlanContract["plan_status"].(string); planStatus != "confirmed" || validatedPlanSHA256 != planSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".plan_changed"
			return fmt.Errorf("template fill validate staged plan no longer matches confirmed snapshot")
		}
		stagedCheckContract, stagedErr := readTemplateFillFormalCheckEvidenceForReport(stagedProjectPath, stagedInputs, planSHA256, inputs.CheckReport)
		if stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".check_contract"
			return stagedErr
		}
		if stagedReportSHA256, _ := stagedCheckContract["check_report_sha256"].(string); stagedReportSHA256 != expectedCheckReportSHA256 {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".check_contract"
			return fmt.Errorf("template fill validate check report sha256 = %q, expected %q", stagedReportSHA256, expectedCheckReportSHA256)
		}
		if _, stagedErr := validateTemplateFillApplyContractWithProvenance(stagedProjectPath, provenance); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".apply_contract"
			return stagedErr
		}
		if _, stagedErr := validateTemplateFillValidateContractWithProvenance(stagedProjectPath, provenance); stagedErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".contract"
			return stagedErr
		}
		return nil
	}, func() error {
		fenceErr := provenance.revalidateAuthoritative()
		if fenceErr != nil {
			preflightFailurePhase = string(PhaseTemplateFillValidate) + ".inputs"
		}
		return fenceErr
	})
	if err != nil {
		failurePhase := string(PhaseTemplateFillValidate) + ".sync"
		if preflightFailurePhase != "" {
			failurePhase = preflightFailurePhase
		}
		return s.failTemplateFillPhase(ctx, task, phaseRun, failurePhase, err, runtimeRun, map[string]any{
			"workspace_path": runtimeRun.WorkspacePath,
			"project_path":   projectPath,
			"plan_sha256":    planSHA256,
		})
	}
	inputs, err = discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".inputs", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	planContract, canonicalPlanSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err == nil {
		if planStatus, _ := planContract["plan_status"].(string); planStatus != "confirmed" || canonicalPlanSHA256 != planSHA256 {
			err = fmt.Errorf("template fill validate canonical plan no longer matches confirmed snapshot")
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".plan_changed", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	checkContract, err = readTemplateFillFormalCheckEvidence(projectPath, inputs, planSHA256)
	if err == nil {
		if canonicalReportSHA256, _ := checkContract["check_report_sha256"].(string); canonicalReportSHA256 != expectedCheckReportSHA256 {
			err = fmt.Errorf("template fill validate canonical check report sha256 = %q, expected %q", canonicalReportSHA256, expectedCheckReportSHA256)
		}
	}
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".check_contract", err, runtimeRun, map[string]any{
			"project_path": projectPath,
			"plan_sha256":  planSHA256,
		})
	}
	applyContract, err = validateTemplateFillApplyContractWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".apply_contract", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	validateContract, err := validateTemplateFillValidateContractWithProvenance(projectPath, provenance)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".contract", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	output := runtimeRunPhaseOutput(runtimeRun)
	output["project_path"] = projectPath
	output["plan_contract"] = planContract
	output["check_contract"] = checkContract
	output["apply_contract"] = applyContract
	output["contract"] = validateContract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".phase_run", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	cancelled, err := s.refreshTemplateFillTask(ctx, task)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".transition", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	if cancelled {
		return nil
	}
	if err := s.transition(ctx, task, model.TaskStatusPublishing, "Publishing", map[string]any{
		"runtime_run_id": runtimeRunID(runtimeRun),
		"project_path":   projectPath,
		"contract":       validateContract,
	}); err != nil {
		return s.failTemplateFillPhase(ctx, task, phaseRun, string(PhaseTemplateFillValidate)+".transition", err, runtimeRun, map[string]any{
			"project_path": projectPath,
		})
	}
	return nil
}

func templateFillFormalCheckCommand(projectRel string) string {
	checkReportRel := filepath.ToSlash(filepath.Join(projectRel, "analysis", "check_report.json"))
	checkContractRel := filepath.ToSlash(filepath.Join(projectRel, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json"))
	return fmt.Sprintf(
		"rm -f %s %s && python3 scripts/ppt_runner.py template-fill-check --project-path %s",
		shellArg(checkReportRel),
		shellArg(checkContractRel),
		shellArg(projectRel),
	)
}

func templateFillCheckErrorCount(contract map[string]any) int {
	summary, ok := contract["summary"].(map[string]any)
	if !ok {
		return 0
	}
	count, _ := summary["error"].(int)
	return count
}

func removeTemplateFillFormalCheckEvidence(inputs TemplateFillInputs) error {
	paths := []string{
		inputs.CheckReport,
		filepath.Join(inputs.ProjectPath, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json"),
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale formal check evidence %s: %w", path, err)
		}
	}
	return nil
}

func requireTemplateFillFormalCheckEvidenceAbsent(projectPath string) error {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return err
	}
	return requireTemplateFillFormalCheckEvidenceAbsentWithProvenance(projectPath, provenance)
}

func requireTemplateFillFormalCheckEvidenceAbsentWithProvenance(projectPath string, provenance templateFillSourceProvenance) error {
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return err
	}
	for _, path := range []string{
		inputs.CheckReport,
		filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json"),
	} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("stale formal check evidence still exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect stale formal check evidence %s: %w", path, err)
		}
	}
	return nil
}

func requireFileSHA256(path, expectedSHA256, label string) error {
	actualSHA256, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("hash %s: %w", label, err)
	}
	if actualSHA256 != expectedSHA256 {
		return fmt.Errorf("%s sha256 = %s, expected %s", label, actualSHA256, expectedSHA256)
	}
	return nil
}

func readTemplateFillFormalCheckEvidence(projectPath string, inputs TemplateFillInputs, planSHA256 string) (map[string]any, error) {
	return readTemplateFillFormalCheckEvidenceForReport(projectPath, inputs, planSHA256, inputs.CheckReport)
}

func readTemplateFillFormalCheckEvidenceForReport(projectPath string, inputs TemplateFillInputs, planSHA256, expectedReportPath string) (map[string]any, error) {
	contractPath := filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json")
	contract, err := readTemplateFillJSONObject(contractPath, "template fill formal check contract")
	if err != nil {
		return nil, err
	}
	if phase, _ := contract["phase"].(string); phase != string(PhaseTemplateFillCheck) {
		return nil, fmt.Errorf("template fill formal check contract phase = %q, expected %q", phase, PhaseTemplateFillCheck)
	}
	if planStatus, _ := contract["plan_status"].(string); planStatus != "confirmed" {
		return nil, fmt.Errorf("template fill formal check plan status = %q, expected %q", planStatus, "confirmed")
	}
	if checkedPlanSHA, _ := contract["plan_sha256"].(string); checkedPlanSHA != planSHA256 {
		return nil, fmt.Errorf("template fill formal check plan sha256 = %q, expected %q", checkedPlanSHA, planSHA256)
	}
	if reportPath, _ := contract["check_report"].(string); reportPath != expectedReportPath {
		return nil, fmt.Errorf("template fill formal check report = %q, expected %q", reportPath, expectedReportPath)
	}
	summary, err := templateFillSummary(contract, "template fill formal check contract", "ok", "warn", "error")
	if err != nil {
		return nil, err
	}
	if summary["error"].(int) != 0 {
		return nil, fmt.Errorf("template fill formal check summary.error = %d", summary["error"])
	}
	reportSHA256, _ := contract["check_report_sha256"].(string)
	currentReportSHA256, err := sha256File(inputs.CheckReport)
	if err != nil {
		return nil, fmt.Errorf("hash template fill formal check report: %w", err)
	}
	if reportSHA256 == "" || currentReportSHA256 != reportSHA256 {
		return nil, fmt.Errorf("template fill formal check report sha256 = %q, current %q", reportSHA256, currentReportSHA256)
	}
	return contract, nil
}

func templateFillInputFailureMetadata(projectPath string) map[string]any {
	sourceFiles := make([]string, 0)
	entries, err := os.ReadDir(filepath.Join(projectPath, "sources"))
	if err == nil {
		for _, entry := range entries {
			if _, ok := pptxDeckExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
				continue
			}
			sourceFiles = append(sourceFiles, filepath.ToSlash(filepath.Join("sources", entry.Name())))
		}
	}
	return map[string]any{
		"pptx_count":         len(sourceFiles),
		"presentation_count": len(sourceFiles),
		"source_files":       sourceFiles,
	}
}

func (s *TaskService) failTemplateFillInputs(
	ctx context.Context,
	task *model.Task,
	phase PipelinePhase,
	runner string,
	workspace *TaskWorkspace,
	projectPath string,
	cause error,
) error {
	phaseRun, err := s.beginPhaseRun(ctx, task, phase, runner, map[string]any{
		"workspace_path":  workspace.HostDir,
		"project_path":    projectPath,
		"discovery_error": cause.Error(),
	})
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, nil, string(phase)+".phase_run", err, nil, map[string]any{
			"workspace_path": workspace.HostDir,
			"project_path":   projectPath,
		})
	}
	extra := templateFillInputFailureMetadata(projectPath)
	extra["workspace_path"] = workspace.HostDir
	extra["project_path"] = projectPath
	return s.failTemplateFillPhase(ctx, task, phaseRun, string(phase)+".inputs", cause, nil, extra)
}

func (s *TaskService) failTemplateFillPreflight(
	ctx context.Context,
	task *model.Task,
	phase PipelinePhase,
	runner string,
	workspace *TaskWorkspace,
	projectPath string,
	failurePhase string,
	cause error,
) error {
	input := map[string]any{
		"workspace_path": workspace.HostDir,
	}
	if projectPath != "" {
		input["project_path"] = projectPath
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, phase, runner, input)
	if err != nil {
		return s.failTemplateFillPhase(ctx, task, nil, string(phase)+".phase_run", err, nil, input)
	}
	return s.failTemplateFillPhase(ctx, task, phaseRun, failurePhase, cause, nil, input)
}

func setTemplateFillPlanStatus(projectPath, status string) error {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return err
	}
	return setTemplateFillPlanStatusWithProvenance(projectPath, status, provenance)
}

func setTemplateFillPlanStatusWithProvenance(projectPath, status string, provenance templateFillSourceProvenance) error {
	if status != "draft" && status != "confirmed" {
		return fmt.Errorf("unsupported template fill plan status %q", status)
	}
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return err
	}
	plan, err := readTemplateFillJSONObject(inputs.FillPlan, "template fill plan")
	if err != nil {
		return err
	}
	plan["status"] = status
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode template fill plan: %w", err)
	}
	temporaryPath := inputs.FillPlan + ".status.tmp"
	if err := os.WriteFile(temporaryPath, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write template fill plan status: %w", err)
	}
	if err := os.Rename(temporaryPath, inputs.FillPlan); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace template fill plan status: %w", err)
	}
	return nil
}

func (s *TaskService) applyTemplateFillRuntimeRunToTask(ctx context.Context, task *model.Task, runtimeRun *model.TaskRuntimeRun) error {
	expectedStatus := task.Status
	if runtimeRun != nil {
		if runtimeRun.ExternalRunID != "" {
			task.LastRuntimeRunID = runtimeRun.ExternalRunID
		}
		if runtimeRun.ExternalSessionID != "" {
			task.LastRuntimeSessionID = runtimeRun.ExternalSessionID
		}
		if runtimeRun.WorkspacePath != "" {
			task.RuntimeWorkspacePath = runtimeRun.WorkspacePath
		}
	}
	return s.saveTaskIfCurrent(ctx, task, expectedStatus)
}

func (s *TaskService) saveTaskIfCurrent(ctx context.Context, task *model.Task, expectedStatus string) error {
	saved, err := s.repo.SaveTaskIfStatus(ctx, task, expectedStatus, task.ExecutionClaimToken)
	if err != nil {
		return err
	}
	if !saved {
		return errTaskStateChanged
	}
	return nil
}

func (s *TaskService) refreshTemplateFillTask(ctx context.Context, task *model.Task) (bool, error) {
	persistedTask, err := s.repo.GetTask(context.WithoutCancel(ctx), task.ID)
	if err != nil {
		return false, err
	}
	if persistedTask.ExecutionClaimToken != task.ExecutionClaimToken {
		return false, errTaskStateChanged
	}
	*task = *persistedTask
	return persistedTask.Status == model.TaskStatusCancelled, nil
}

func (s *TaskService) failTemplateFillPhase(
	ctx context.Context,
	task *model.Task,
	phaseRun *model.TaskPhaseRun,
	failurePhase string,
	cause error,
	runtimeRun *model.TaskRuntimeRun,
	extra map[string]any,
) error {
	recoveryCtx := context.WithoutCancel(ctx)
	errs := []error{cause}
	if err := s.finishPhaseRun(recoveryCtx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(runtimeRun), cause); err != nil {
		errs = append(errs, fmt.Errorf("persist %s phase failure: %w", failurePhase, err))
	}
	persistedTask, err := s.repo.GetTask(recoveryCtx, task.ID)
	if err != nil {
		errs = append(errs, fmt.Errorf("reload task after %s failure: %w", failurePhase, err))
		return errors.Join(errs...)
	}
	if persistedTask.ExecutionClaimToken != task.ExecutionClaimToken {
		return nil
	}
	if persistedTask.Status == model.TaskStatusCancelled {
		if errors.Is(cause, errTaskStateChanged) {
			return nil
		}
		return errors.Join(errs...)
	}
	if errors.Is(cause, errTaskStateChanged) {
		return nil
	}
	if err := s.failWithMetadata(recoveryCtx, persistedTask, failurePhase, cause, runtimeRun, extra); err != nil {
		errs = append(errs, fmt.Errorf("persist task failure for %s: %w", failurePhase, err))
	}
	return errors.Join(errs...)
}

func (s *TaskService) failTaskAfterSourcePrepare(
	ctx context.Context,
	task *model.Task,
	failurePhase string,
	cause error,
	run *model.TaskRuntimeRun,
	extra map[string]any,
) error {
	recoveryCtx := context.WithoutCancel(ctx)
	expectedClaimToken := task.ExecutionClaimToken
	if err := s.failWithMetadata(recoveryCtx, task, failurePhase, cause, run, extra); err != nil {
		errs := []error{cause, fmt.Errorf("persist post-source-prepare task failure: %w", err)}
		persistedTask, reloadErr := s.repo.GetTask(recoveryCtx, task.ID)
		if reloadErr != nil {
			errs = append(errs, fmt.Errorf("reload post-source-prepare task: %w", reloadErr))
			return errors.Join(errs...)
		}
		if persistedTask.ExecutionClaimToken != expectedClaimToken || persistedTask.Status == model.TaskStatusCancelled {
			if errors.Is(err, errTaskStateChanged) {
				return nil
			}
			return errors.Join(errs...)
		}
		if retryErr := s.failWithMetadata(recoveryCtx, persistedTask, failurePhase, cause, run, extra); retryErr != nil {
			errs = append(errs, fmt.Errorf("retry post-source-prepare task failure: %w", retryErr))
		}
		return errors.Join(errs...)
	}
	return cause
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
		if errors.Is(err, errTaskStateChanged) {
			return nil
		}
		errs = append(errs, fmt.Errorf("recover source_prepare task: %w", err))
	}
	return errors.Join(errs...)
}

func (s *TaskService) processGenerate(ctx context.Context, task *model.Task) error {
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		_ = s.failWithMetadata(ctx, task, runnerProfileFailurePhase(err), err, nil, map[string]any{"task_status": task.Status})
		return err
	}
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

	if s.useFullPPTMaster(task) {
		return s.processFullPPTMasterSplit(ctx, task, workspace)
	}
	return s.processLegacyCommandGenerate(ctx, task, workspace)
}

func (s *TaskService) processLegacyCommandGenerate(ctx context.Context, task *model.Task, workspace *TaskWorkspace) error {
	if task.RunnerProfile != model.RunnerProfileRealLite && task.RunnerProfile != model.RunnerProfileSmoke {
		return fmt.Errorf("legacy generate requires real-lite or smoke runner profile, got %q", task.RunnerProfile)
	}
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{"workspace_path": workspace.HostDir})
		return err
	}
	input := map[string]any{
		"workspace_path":     workspace.HostDir,
		"runtime_project":    task.RuntimeProject,
		"full_ppt_master":    false,
		"legacy_bundle_note": "current generate run still covers spec, svg, quality, export, and runtime publish",
		"runner_profile":     task.RunnerProfile,
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
		shellArg(s.commandRunnerProfile(task)),
		shellArg(task.RuntimeProject),
	)
	run, err := s.runAgent(ctx, task, "generate", AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	if err != nil {
		recovered, recoverErr := s.tryRecoverGeneratedRuntimeArtifacts(ctx, task, workspace, run, phaseRun, "generate.agent")
		if recovered {
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
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		if !errors.Is(persistErr, errTaskStateChanged) {
			_ = s.failWithMetadata(context.WithoutCancel(ctx), task, "generate.runtime", persistErr, run, map[string]any{"workspace_path": workspace.HostDir})
		}
		return persistErr
	}
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, runtimeRunPhaseOutput(run), nil); err != nil {
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
	if !s.useFullPPTMaster(task) {
		err := fmt.Errorf("phase retry %s requires full-ppt-master profile", phase)
		_ = s.failWithMetadata(ctx, task, string(phase)+".unsupported_profile", err, nil, nil)
		return err
	}
	return s.processFullPPTMasterFromPhase(ctx, task, s.resolveTaskWorkspace(task), phase)
}

func (s *TaskService) processFullPPTMasterFromPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, startPhase PipelinePhase) error {
	if !s.useFullPPTMaster(task) {
		err := fmt.Errorf("full phase %s requires a locked full-ppt-master main task", startPhase)
		_ = s.failWithMetadata(ctx, task, string(startPhase)+".unsupported_profile", err, nil, map[string]any{"effective_profile": task.RunnerProfile})
		return err
	}
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{
			"workspace_path":     workspace.HostDir,
			"effective_profile":  task.RunnerProfile,
			"started_from_phase": string(startPhase),
		})
		return err
	}
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
	if !s.useFullPPTMaster(task) || task.Route != model.TaskRouteMain {
		return fmt.Errorf("image acquire compatibility skip requires full-ppt-master main route")
	}
	if err := s.transition(ctx, task, model.TaskStatusImageAcquiring, "Image acquire skipped", map[string]any{
		"runtime_run_id": previousRuntimeRunID,
		"reason":         "resource acquisition is deferred to SPEC-05",
		"implementation": "deferred_to_SPEC05",
		"runner_profile": task.RunnerProfile,
	}); err != nil {
		return err
	}
	if err := s.recordSkippedPhaseRun(ctx, task, PhaseImageAcquire, PhaseRunnerWorker, map[string]any{
		"reason":                  "resource acquisition is deferred to SPEC-05",
		"implementation":          "deferred_to_SPEC05",
		"acquired":                false,
		"runner_profile":          task.RunnerProfile,
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
	input := fullPhaseInput(task, workspace, projectPath, PhaseSpecGenerate)
	for key, value := range templateResolvePhaseInput(task, workspace, projectPath) {
		input[key] = value
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSpecGenerate, PhaseRunnerAgent, input)
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
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		if !errors.Is(persistErr, errTaskStateChanged) {
			_ = s.failWithMetadata(context.WithoutCancel(ctx), task, string(PhaseSpecGenerate)+".runtime", persistErr, run, map[string]any{"workspace_path": workspace.HostDir})
		}
		return run, projectPath, persistErr
	}
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
	if err == nil {
		contract, err = bindFullPhaseContract(projectPath, PhaseSpecGenerate, contract, task, workspace, runtimeRunID(run))
	}
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSpecGenerate)+".contract", err, run, map[string]any{"project_path": projectPath})
		return run, projectPath, err
	}
	output := fullPhaseOutput(task, run, projectPath, PhaseSpecGenerate)
	output["contract"] = contract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return run, projectPath, err
	}
	return run, projectPath, nil
}

func (s *TaskService) runFullPPTMasterSVGPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string) (*model.TaskRuntimeRun, string, error) {
	if _, err := validateExistingSpecContract(projectPath, task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseSVGExecute)+".spec_contract", err, nil, map[string]any{"project_path": projectPath})
		return nil, projectPath, err
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseSVGExecute, PhaseRunnerAgent, fullPhaseInput(task, workspace, projectPath, PhaseSVGExecute, PhaseSpecGenerate))
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
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		if !errors.Is(persistErr, errTaskStateChanged) {
			_ = s.failWithMetadata(context.WithoutCancel(ctx), task, string(PhaseSVGExecute)+".runtime", persistErr, run, map[string]any{"workspace_path": workspace.HostDir})
		}
		return run, projectPath, persistErr
	}
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
	if err == nil {
		contract, err = bindFullPhaseContract(projectPath, PhaseSVGExecute, contract, task, workspace, runtimeRunID(run))
	}
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseSVGExecute)+".contract", err, run, map[string]any{"project_path": projectPath})
		return run, projectPath, err
	}
	output := fullPhaseOutput(task, run, projectPath, PhaseSVGExecute)
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
		"python3 skills/ppt-master/scripts/finalize_svg.py %s --quiet && python3 skills/ppt-master/scripts/svg_to_pptx.py %s --no-notes -t none",
		shellArg(projectRel),
		shellArg(projectRel),
	)
	return s.runFullPPTMasterCommandPhase(ctx, task, workspace, projectPath, PhaseFinalizeExport, command, validatePPTXExportContract)
}

func (s *TaskService) runFullPPTMasterCommandPhase(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string, phase PipelinePhase, command string, validate func(string) (map[string]any, error)) (*model.TaskRuntimeRun, string, error) {
	upstream := []PipelinePhase{PhaseSVGExecute}
	upstreamContractPhase := PhaseSVGExecute
	if phase == PhaseFinalizeExport {
		upstream = []PipelinePhase{PhaseSVGExecute, PhaseQualityCheck}
		upstreamContractPhase = PhaseQualityCheck
	}
	if _, err := validateFullSVGUpstreamContract(projectPath, upstreamContractPhase, task); err != nil {
		_ = s.failWithMetadata(ctx, task, string(phase)+".upstream_contract", err, nil, map[string]any{"project_path": projectPath})
		return nil, projectPath, err
	}
	input := fullPhaseInput(task, workspace, projectPath, phase, upstream...)
	input["command"] = command
	phaseRun, err := s.beginPhaseRun(ctx, task, phase, PhaseRunnerWorker, input)
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
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		if !errors.Is(persistErr, errTaskStateChanged) {
			_ = s.failWithMetadata(context.WithoutCancel(ctx), task, string(phase)+".runtime", persistErr, run, map[string]any{"workspace_path": workspace.HostDir})
		}
		return run, projectPath, persistErr
	}
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
	output := fullPhaseOutput(task, run, projectPath, phase)
	if validate != nil {
		contract, err := validate(projectPath)
		if err == nil {
			contract, err = bindFullPhaseContract(projectPath, phase, contract, task, workspace, runtimeRunID(run))
		}
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
	expectedStatus := task.Status
	if run.ExternalRunID != "" {
		task.LastRuntimeRunID = run.ExternalRunID
	}
	if run.ExternalSessionID != "" {
		task.LastRuntimeSessionID = run.ExternalSessionID
	}
	if run.WorkspacePath != "" {
		task.RuntimeWorkspacePath = run.WorkspacePath
	}
	return s.saveTaskIfCurrent(ctx, task, expectedStatus)
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
	if err := inspectFullPPTMasterRetryCleanupPaths(projectPath, paths); err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func inspectFullPPTMasterRetryCleanupPaths(projectPath string, paths []string) error {
	if err := requireRealProjectDirectory(projectPath, "full PPT Master retry project"); err != nil {
		return err
	}
	projectRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}
	workspaceRoot, err := filepath.Abs(filepath.Dir(filepath.Dir(projectPath)))
	if err != nil {
		return err
	}
	for _, path := range paths {
		candidate, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		allowedRoot := projectRoot
		if !pathWithinRoot(projectRoot, candidate) {
			allowedRoot = workspaceRoot
		}
		if candidate == allowedRoot || !pathWithinRoot(allowedRoot, candidate) {
			return fmt.Errorf("full PPT Master retry output %s is outside allowed roots", path)
		}
		relative, err := filepath.Rel(allowedRoot, candidate)
		if err != nil {
			return err
		}
		current := allowedRoot
		for index, part := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("full PPT Master retry output must not contain a symlink: %s", current)
			}
			if index < len(strings.Split(relative, string(filepath.Separator)))-1 && !info.IsDir() {
				return fmt.Errorf("full PPT Master retry output parent must be a directory: %s", current)
			}
		}
	}
	return nil
}

func cleanupTemplateFillOutputsForRetry(projectPath string, phase PipelinePhase) error {
	paths, err := templateFillRetryOutputPaths(projectPath, phase)
	if err != nil {
		return err
	}
	if err := inspectTemplateFillRetryCleanupPaths(projectPath, paths); err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove template fill retry output %s: %w", path, err)
		}
	}
	return nil
}

func requireTemplateFillRetryOutputsAbsent(projectPath string, phase PipelinePhase) error {
	paths, err := templateFillRetryOutputPaths(projectPath, phase)
	if err != nil {
		return err
	}
	if err := inspectTemplateFillRetryCleanupPaths(projectPath, paths); err != nil {
		return err
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("template fill retry output still exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect template fill retry output %s: %w", path, err)
		}
	}
	return nil
}

func templateFillRetryOutputPaths(projectPath string, phase PipelinePhase) ([]string, error) {
	contractPath := func(contract PipelinePhase) string {
		return filepath.Join(projectPath, ".slidesmith", "contracts", string(contract)+".json")
	}
	publishContract := contractPath(PhasePublish)
	finalContract := filepath.Join(projectPath, ".slidesmith", "contracts", "final.json")
	switch phase {
	case PhaseTemplateFillPlan:
		return []string{
			filepath.Join(projectPath, "analysis", "fill_plan.json"),
			filepath.Join(projectPath, "analysis", "check_report.json"),
			contractPath(PhaseTemplateFillPlan),
			contractPath(PhaseTemplateFillCheck),
			contractPath(PhaseTemplateFillApply),
			contractPath(PhaseTemplateFillValidate),
			publishContract,
			finalContract,
			filepath.Join(projectPath, "exports"),
			filepath.Join(projectPath, "validation"),
		}, nil
	case PhaseTemplateFillCheck:
		return []string{
			filepath.Join(projectPath, "analysis", "check_report.json"),
			contractPath(PhaseTemplateFillCheck),
			contractPath(PhaseTemplateFillApply),
			contractPath(PhaseTemplateFillValidate),
			publishContract,
			finalContract,
			filepath.Join(projectPath, "exports"),
			filepath.Join(projectPath, "validation"),
		}, nil
	case PhaseTemplateFillApply:
		return []string{
			contractPath(PhaseTemplateFillApply),
			contractPath(PhaseTemplateFillValidate),
			publishContract,
			finalContract,
			filepath.Join(projectPath, "exports"),
			filepath.Join(projectPath, "validation"),
		}, nil
	case PhaseTemplateFillValidate:
		return []string{
			contractPath(PhaseTemplateFillValidate),
			publishContract,
			finalContract,
			filepath.Join(projectPath, "validation"),
		}, nil
	case PhasePublish:
		return []string{publishContract, finalContract}, nil
	default:
		return nil, fmt.Errorf("unsupported template fill cleanup phase %q", phase)
	}
}

func inspectTemplateFillRetryCleanupPaths(projectPath string, paths []string) error {
	if err := requireRealProjectDirectory(projectPath, "template fill retry project"); err != nil {
		return err
	}
	root, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("resolve template fill retry project: %w", err)
	}
	for _, path := range paths {
		candidate, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve template fill retry output %s: %w", path, err)
		}
		relativePath, err := filepath.Rel(root, candidate)
		if err != nil || relativePath == "." || !pathWithinRoot(root, candidate) {
			return fmt.Errorf("template fill retry output %s is outside project %s", path, projectPath)
		}
		current := root
		parts := strings.Split(relativePath, string(filepath.Separator))
		for index, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return fmt.Errorf("inspect template fill retry output %s: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("template fill retry output must not contain a symlink: %s", current)
			}
			if index < len(parts)-1 && !info.IsDir() {
				return fmt.Errorf("template fill retry output parent must be a directory: %s", current)
			}
		}
	}
	return nil
}

func (s *TaskService) processPublish(ctx context.Context, task *model.Task, workspace *TaskWorkspace, payload map[string]any) error {
	if workspace == nil {
		workspace = s.resolveTaskWorkspace(task)
	}
	if task.Route == model.TaskRouteMain && task.RunnerProfile == model.RunnerProfileFullPPTMaster {
		if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
			_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{"workspace_path": workspace.HostDir})
			return err
		}
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhasePublish, PhaseRunnerPublisher, map[string]any{
		"workspace_path":           task.RuntimeWorkspacePath,
		"resolved_workspace_path":  workspace.HostDir,
		"runner_profile":           task.RunnerProfile,
		"runner_profile_locked_at": task.RunnerProfileLockedAt,
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
		"runner_profile":          task.RunnerProfile,
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
	if task.Route == model.TaskRouteTemplateFill {
		if workspace == nil {
			return nil, fmt.Errorf("task workspace is empty")
		}
		canonicalProjectPath, err := s.findPersistentProjectPath(task)
		if err != nil {
			return nil, fmt.Errorf("resolve canonical Template Fill project: %w", err)
		}
		addRoot(workspace.HostDir, "task_workspace", "", canonicalProjectPath)
	} else {
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
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("runtime workspace path is empty")
	}

	var lastErr error
	publishVersion := publishVersionName()
	publishOne := func(root publishRoot) (_ map[string]any, terminal bool, resultErr error) {
		attempt, err := newPublishRollbackAttempt(task.ID, publishVersion)
		if err != nil {
			return nil, true, err
		}
		defer func() {
			if attempt.armed {
				cleanupErr := s.cleanupFailedPublishAttempt(ctx, attempt)
				if cleanupErr != nil {
					terminal = true
					resultErr = errors.Join(resultErr, cleanupErr)
				}
			}
		}()

		var published []model.Artifact
		if task.Route == model.TaskRouteTemplateFill {
			published, err = s.publisher.PublishProject(ctx, task.ID, root.Path, root.ProjectPath, publishVersion)
		} else {
			published, err = s.publisher.Publish(ctx, task.ID, root.Path, publishVersion)
		}
		if err != nil {
			return nil, errors.Is(err, ErrRuntimePublishCleanupIncomplete), err
		}
		if err := attempt.addArtifacts(published); err != nil {
			return nil, false, err
		}
		projectPath := root.ProjectPath
		if projectPath == "" {
			projectPath, _ = discoverRuntimeProjectPath(root.Path)
		}
		if projectPath == "" {
			return nil, false, fmt.Errorf("published runtime project path not found for workspace %s", root.Path)
		}
		publishContract, err := buildPublishedArtifactsContract(projectPath, s.storage, published, publishVersion, task.Route)
		if err != nil {
			return nil, false, err
		}
		if _, err := writeContractReport(projectPath, string(PhasePublish), publishContract); err != nil {
			return nil, false, err
		}
		publishContractObjectKey := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", publishVersion, "contracts", string(PhasePublish)+".json"))
		if err := attempt.addObjectKey(publishContractObjectKey); err != nil {
			return nil, false, err
		}
		publishContractArtifact, err := s.copyContractReportArtifact(ctx, task.ID, projectPath, publishVersion, string(PhasePublish))
		if err != nil {
			return nil, false, err
		}
		if err := attempt.addArtifact(publishContractArtifact); err != nil {
			return nil, false, err
		}
		published = append(published, publishContractArtifact)
		finalContract := buildFinalTaskContract(projectPath, publishContract)
		if _, err := writeContractReport(projectPath, "final", finalContract); err != nil {
			return nil, false, err
		}
		finalContractObjectKey := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", publishVersion, "contracts", "final.json"))
		if err := attempt.addObjectKey(finalContractObjectKey); err != nil {
			return nil, false, err
		}
		finalContractArtifact, err := s.copyContractReportArtifact(ctx, task.ID, projectPath, publishVersion, "final")
		if err != nil {
			return nil, false, err
		}
		if err := attempt.addArtifact(finalContractArtifact); err != nil {
			return nil, false, err
		}
		published = append(published, finalContractArtifact)
		if root.Source == "agent_compose_session" {
			expectedStatus := task.Status
			task.RuntimeWorkspacePath = root.Path
			task.LastRuntimeSessionID = root.SessionID
			if err := s.saveTaskIfCurrent(ctx, task, expectedStatus); err != nil {
				return nil, true, err
			}
		}
		prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", publishVersion)) + "/"
		for index := range published {
			if published[index].ID == "" {
				published[index].ID = uuid.NewString()
			}
			attempt.addArtifactID(published[index].ID)
		}
		if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, prefix, published); err != nil {
			return nil, true, err
		}
		persisted, err := s.repo.ListArtifactsByPublishVersion(ctx, task.ID, publishVersion)
		if err != nil {
			return nil, true, fmt.Errorf("list persisted publish artifacts: %w", err)
		}
		attempt.addBoundPersistedArtifactIDs(persisted)
		if len(persisted) != len(published) {
			return nil, true, fmt.Errorf("persisted artifact count = %d, expected %d", len(persisted), len(published))
		}
		if _, err := buildPublishedArtifactsContract(projectPath, s.storage, persisted, publishVersion, task.Route); err != nil {
			return nil, true, fmt.Errorf("final persisted artifact check failed: %w", err)
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
			return nil, true, err
		}
		attempt.disarm()
		return contract, true, nil
	}

	for _, root := range roots {
		contract, terminal, err := publishOne(root)
		if err != nil {
			if terminal {
				return nil, err
			}
			lastErr = err
			continue
		}
		return contract, nil
	}
	return nil, fmt.Errorf("publish runtime artifacts: %w", lastErr)
}

type publishRollbackAttempt struct {
	taskID         string
	publishVersion string
	objectKeys     map[string]struct{}
	artifactIDs    map[string]struct{}
	armed          bool
}

func newPublishRollbackAttempt(taskID, publishVersion string) (*publishRollbackAttempt, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("publish rollback task id is empty")
	}
	cleanVersion, err := cleanPublishVersion(publishVersion)
	if err != nil {
		return nil, err
	}
	return &publishRollbackAttempt{
		taskID:         taskID,
		publishVersion: cleanVersion,
		objectKeys:     map[string]struct{}{},
		artifactIDs:    map[string]struct{}{},
		armed:          true,
	}, nil
}

func (attempt *publishRollbackAttempt) addObjectKey(objectKey string) error {
	if attempt == nil || !attempt.armed {
		return fmt.Errorf("publish rollback attempt is not armed")
	}
	normalized := filepath.ToSlash(strings.TrimSpace(objectKey))
	clean, err := cleanObjectKey(normalized)
	if err != nil || clean != normalized {
		return fmt.Errorf("publish rollback object key is not canonical: %q", objectKey)
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", attempt.taskID, "artifacts", attempt.publishVersion)) + "/"
	if !strings.HasPrefix(clean, prefix) {
		return fmt.Errorf("publish rollback object key %s is outside exact attempt", clean)
	}
	relativePath := strings.TrimPrefix(clean, prefix)
	if _, err := cleanArtifactRel(relativePath); err != nil {
		return fmt.Errorf("publish rollback object key %s is invalid: %w", clean, err)
	}
	attempt.objectKeys[clean] = struct{}{}
	return nil
}

func (attempt *publishRollbackAttempt) addArtifact(artifact model.Artifact) error {
	return attempt.addObjectKey(artifact.ObjectKey)
}

func (attempt *publishRollbackAttempt) addArtifacts(artifacts []model.Artifact) error {
	for _, artifact := range artifacts {
		if err := attempt.addArtifact(artifact); err != nil {
			return err
		}
	}
	return nil
}

func (attempt *publishRollbackAttempt) addArtifactID(artifactID string) {
	if attempt == nil || !attempt.armed {
		return
	}
	if artifactID = strings.TrimSpace(artifactID); artifactID != "" {
		attempt.artifactIDs[artifactID] = struct{}{}
	}
}

func (attempt *publishRollbackAttempt) addBoundPersistedArtifactIDs(artifacts []model.Artifact) {
	if attempt == nil || !attempt.armed {
		return
	}
	for _, artifact := range artifacts {
		objectKey := filepath.ToSlash(strings.TrimSpace(artifact.ObjectKey))
		if artifact.TaskID != attempt.taskID {
			continue
		}
		if _, ok := attempt.objectKeys[objectKey]; !ok {
			continue
		}
		attempt.addArtifactID(artifact.ID)
	}
}

func (attempt *publishRollbackAttempt) disarm() {
	if attempt != nil {
		attempt.armed = false
	}
}

func (s *TaskService) cleanupFailedPublishAttempt(ctx context.Context, attempt *publishRollbackAttempt) error {
	if attempt == nil || !attempt.armed {
		return nil
	}
	attempt.armed = false
	cleanupCtx := context.WithoutCancel(ctx)
	var cleanupErr error
	artifactIDs := make([]string, 0, len(attempt.artifactIDs))
	for artifactID := range attempt.artifactIDs {
		artifactIDs = append(artifactIDs, artifactID)
	}
	sort.Strings(artifactIDs)
	if len(artifactIDs) > 0 {
		if err := s.repo.DeleteArtifactsByIDsOrObjectKeyPrefix(cleanupCtx, attempt.taskID, "", artifactIDs); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete failed publish attempt %s exact rows: %w", attempt.publishVersion, err))
		}
	}

	objectKeys := make([]string, 0, len(attempt.objectKeys))
	for objectKey := range attempt.objectKeys {
		objectKeys = append(objectKeys, objectKey)
	}
	sort.Strings(objectKeys)
	for _, objectKey := range objectKeys {
		if err := s.storage.DeleteObject(cleanupCtx, objectKey); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete failed publish object %s: %w", objectKey, err))
		}
	}
	return cleanupErr
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
	if task.Route == model.TaskRouteTemplateFill {
		unlock, err := s.lockTemplateFillAPI(ctx, task)
		if err != nil {
			return nil, err
		}
		defer unlock()
		task, err = s.repo.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
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
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
	}
	phase, err = normalizeRetryPhase(phase, task.FailurePhase)
	if err != nil {
		return nil, err
	}
	switch phase {
	case retryPhaseTemplateFillPlan:
		return s.retryTemplateFillPhase(ctx, task, PhaseTemplateFillPlan, model.TaskStatusTemplateFillPlanning)
	case retryPhaseTemplateFillCheck:
		return s.retryTemplateFillPhase(ctx, task, PhaseTemplateFillCheck, model.TaskStatusTemplateFillChecking)
	case retryPhaseTemplateFillApply:
		return s.retryTemplateFillPhase(ctx, task, PhaseTemplateFillApply, model.TaskStatusTemplateFillApplying)
	case retryPhaseTemplateFillValidate:
		return s.retryTemplateFillPhase(ctx, task, PhaseTemplateFillValidate, model.TaskStatusTemplateFillValidating)
	case retryPhasePublish:
		if task.Route == model.TaskRouteTemplateFill {
			return s.retryTemplateFillPhase(ctx, task, PhasePublish, model.TaskStatusPublishing)
		}
	}
	if task.Route == model.TaskRouteTemplateFill && phase != retryPhasePrepare {
		return nil, fmt.Errorf("retry phase %q is not valid for task route %q", phase, task.Route)
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
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
	}
	if !s.useFullPPTMaster(task) {
		return nil, fmt.Errorf("spec continue requires a locked full-ppt-master main task")
	}
	workspace := s.resolveTaskWorkspace(task)
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		return nil, err
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
		if _, err := validateExistingSpecContract(projectPath, task, workspace); err != nil {
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
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
	}
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
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
	}
	if !s.useFullPPTMaster(task) {
		return nil, fmt.Errorf("cannot retry %s with runner profile %q", phase, task.RunnerProfile)
	}
	if err := s.validateTaskRuntimeProfile(task, s.resolveTaskWorkspace(task)); err != nil {
		return nil, err
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

func (s *TaskService) retryTemplateFillPhase(ctx context.Context, task *model.Task, phase PipelinePhase, status string) (_ *model.Task, resultErr error) {
	if err := requireTemplateFillRoute(task); err != nil {
		return nil, err
	}
	unlock, err := s.lockTemplateFillAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlock()
	task, err = s.reloadTemplateFillAPITask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusFailed {
		return nil, fmt.Errorf("only failed tasks can be retried")
	}
	if err := s.sweepTemplateFillAPISessions(task); err != nil {
		return nil, err
	}
	if err := s.sweepCommittedTemplateFillPromotions(ctx, task); err != nil {
		return nil, err
	}
	releaseClaim, err := s.claimTemplateFillAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releaseClaim())
	}()
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, fmt.Errorf("cannot retry %s before prepared project exists: %w", phase, err)
	}
	session, err := s.newTemplateFillAPISession(ctx, task, projectPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.cleanup())
	}()
	if err := cleanupTemplateFillOutputsForRetry(session.candidateProject, phase); err != nil {
		return nil, fmt.Errorf("cleanup before retry %s: %w", phase, err)
	}
	validateClean := func(candidate string) error {
		if _, err := discoverTemplateFillInputsWithProvenance(candidate, session.provenance); err != nil {
			return err
		}
		return requireTemplateFillRetryOutputsAbsent(candidate, phase)
	}
	exchange, err := s.beginTemplateFillProjectExchange(ctx, task, session, status, validateClean)
	if err != nil {
		return nil, err
	}
	if s.beforeTemplateFillAPICommit != nil {
		s.beforeTemplateFillAPICommit(status)
	}
	if err := s.transitionTemplateFillAtGate(ctx, task, status, "Retry queued from "+string(phase), map[string]any{
		"retry_phase":  string(phase),
		"project_path": projectPath,
	}); err != nil {
		return nil, errors.Join(err, exchange.rollback())
	}
	exchange.commitAfterDB(ctx)
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Template fill phase retry queued for worker", map[string]any{
		"retry_phase":  string(phase),
		"project_path": projectPath,
	})
	return task, nil
}

func (s *TaskService) retryPublish(ctx context.Context, task *model.Task) (*model.Task, error) {
	if task.Route == model.TaskRouteMain && task.RunnerProfile == model.RunnerProfileFullPPTMaster {
		if err := s.validateTaskRuntimeProfile(task, s.resolveTaskWorkspace(task)); err != nil {
			return nil, err
		}
	}
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
	case "template_fill_plan", "fill_plan", "plan", "template_fill_planning":
		return retryPhaseTemplateFillPlan, nil
	case "template_fill_check", "fill_check", "check", "template_fill_checking":
		return retryPhaseTemplateFillCheck, nil
	case "template_fill_apply", "fill_apply", "apply", "template_fill_applying":
		return retryPhaseTemplateFillApply, nil
	case "template_fill_validate", "fill_validate", "validate", "template_fill_validating":
		return retryPhaseTemplateFillValidate, nil
	case "publish", "publishing", "artifact_publish":
		return retryPhasePublish, nil
	default:
		return "", fmt.Errorf("unsupported retry phase %q", requested)
	}
}

func inferRetryPhase(failurePhase string) string {
	value := strings.ToLower(strings.TrimSpace(failurePhase))
	switch {
	case strings.HasPrefix(value, "prepare"),
		strings.HasPrefix(value, "source"),
		strings.HasPrefix(value, "route_select"),
		strings.HasPrefix(value, string(PhaseTemplateResolve)):
		return retryPhasePrepare
	case strings.HasPrefix(value, string(PhaseSVGExecute)), strings.HasPrefix(value, "svg"):
		return retryPhaseSVGExecute
	case strings.HasPrefix(value, string(PhaseQualityCheck)), strings.HasPrefix(value, "quality"):
		return retryPhaseQualityCheck
	case strings.HasPrefix(value, string(PhaseFinalizeExport)), strings.HasPrefix(value, "export"), strings.HasPrefix(value, "finalize"):
		return retryPhaseFinalizeExport
	case strings.HasPrefix(value, string(PhaseTemplateFillPlan)):
		return retryPhaseTemplateFillPlan
	case strings.HasPrefix(value, string(PhaseTemplateFillCheck)):
		return retryPhaseTemplateFillCheck
	case strings.HasPrefix(value, string(PhaseTemplateFillApply)):
		return retryPhaseTemplateFillApply
	case strings.HasPrefix(value, string(PhaseTemplateFillValidate)):
		return retryPhaseTemplateFillValidate
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
	transitioned, err := s.transitionIfCurrent(ctx, task, to, message, payload)
	if err != nil {
		return err
	}
	if !transitioned {
		return errTaskStateChanged
	}
	return nil
}

func (s *TaskService) transitionIfCurrent(ctx context.Context, task *model.Task, to, message string, payload map[string]any) (bool, error) {
	expectedStatus := task.Status
	if err := s.machine.Validate(expectedStatus, to); err != nil {
		return false, err
	}
	task.Status = to
	task.ErrorMessage = ""
	task.FailurePhase = ""
	task.FailureMetadata = "{}"
	saved, err := s.repo.SaveTaskIfStatus(ctx, task, expectedStatus, task.ExecutionClaimToken)
	if err != nil {
		return false, err
	}
	if !saved {
		return false, nil
	}
	return true, s.event(ctx, task.ID, model.EventTypeStatus, to, message, payload)
}

func (s *TaskService) fail(ctx context.Context, task *model.Task, cause error) error {
	return s.failWithMetadata(ctx, task, "", cause, nil, nil)
}

func (s *TaskService) failWithMetadata(ctx context.Context, task *model.Task, phase string, cause error, run *model.TaskRuntimeRun, extra map[string]any) error {
	expectedStatus := task.Status
	if phase == "" && run != nil {
		phase = run.FailurePhase
		if phase == "" {
			phase = run.Phase
		}
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["effective_profile"] = task.RunnerProfile
	extra["task_status"] = expectedStatus
	metadata := buildFailureMetadata(phase, cause, run, extra)
	task.ErrorMessage = cause.Error()
	task.FailurePhase = phase
	task.FailureMetadata = encodeJSON(metadata)
	if err := s.machine.Validate(expectedStatus, model.TaskStatusFailed); err != nil {
		return err
	}
	task.Status = model.TaskStatusFailed
	saved, err := s.repo.SaveTaskIfStatus(ctx, task, expectedStatus, task.ExecutionClaimToken)
	if err != nil {
		return err
	}
	if !saved {
		return errTaskStateChanged
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

func (s *TaskService) syncPreparedProject(ctx context.Context, task *model.Task, workspacePath, targetWorkspaceDir string) (string, error) {
	return s.syncPreparedProjectValidated(ctx, task, workspacePath, targetWorkspaceDir, nil)
}

func (s *TaskService) syncPreparedProjectValidated(
	ctx context.Context,
	task *model.Task,
	workspacePath string,
	targetWorkspaceDir string,
	validate func(string) error,
) (targetProject string, err error) {
	return s.syncPreparedProjectValidatedWithFence(ctx, task, workspacePath, targetWorkspaceDir, validate, nil)
}

func (s *TaskService) syncPreparedProjectValidatedWithFence(
	ctx context.Context,
	task *model.Task,
	workspacePath string,
	targetWorkspaceDir string,
	validate func(string) error,
	validateAuthoritative func() error,
) (targetProject string, err error) {
	if task == nil || task.RuntimeProject == "" || workspacePath == "" {
		return "", nil
	}
	staged, err := s.stagePreparedProject(ctx, task, workspacePath, targetWorkspaceDir)
	if err != nil {
		return "", err
	}
	defer func() {
		if staged.retainRecovery {
			return
		}
		err = errors.Join(err, staged.cleanup())
	}()
	if validate != nil {
		if err := validate(staged.projectPath); err != nil {
			return "", err
		}
	}
	targetProject = staged.targetPath
	if !staged.noOp {
		targetProject, err = s.promoteStagedProjectValidatedWithFence(ctx, task, staged, validate, validateAuthoritative)
		if err != nil {
			return targetProject, err
		}
	}
	return targetProject, nil
}

func (s *TaskService) syncRuntimeProject(ctx context.Context, task *model.Task, workspace *TaskWorkspace, runtimeWorkspacePath string) (string, error) {
	return s.syncRuntimeProjectValidated(ctx, task, workspace, runtimeWorkspacePath, nil)
}

func (s *TaskService) syncRuntimeProjectValidated(
	ctx context.Context,
	task *model.Task,
	workspace *TaskWorkspace,
	runtimeWorkspacePath string,
	validate func(string) error,
) (string, error) {
	return s.syncRuntimeProjectValidatedWithFence(ctx, task, workspace, runtimeWorkspacePath, validate, nil)
}

func (s *TaskService) syncRuntimeProjectValidatedWithFence(
	ctx context.Context,
	task *model.Task,
	workspace *TaskWorkspace,
	runtimeWorkspacePath string,
	validate func(string) error,
	validateAuthoritative func() error,
) (string, error) {
	if strings.TrimSpace(runtimeWorkspacePath) == "" || workspace == nil {
		projectPath, err := s.findPersistentProjectPath(task)
		if err != nil {
			return "", err
		}
		if validate != nil {
			return "", fmt.Errorf("validated runtime project promotion requires a distinct session workspace")
		}
		return projectPath, nil
	}
	return s.syncPreparedProjectValidatedWithFence(ctx, task, runtimeWorkspacePath, workspace.HostDir, validate, validateAuthoritative)
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
	claimToken, taskStatus := taskRunOwnership(task)
	run := &model.TaskRuntimeRun{
		ID:                  uuid.NewString(),
		TaskID:              task.ID,
		ExecutionClaimToken: claimToken,
		TaskStatus:          taskStatus,
		Runtime:             "agent-compose",
		Agent:               s.agentCfg.Agent,
		Phase:               phase,
		Command:             commandForRecord,
		Status:              "running",
		StartedAt:           &started,
	}
	if err := s.repo.CreateRuntimeRun(ctx, run); err != nil {
		if errors.Is(err, repository.ErrTaskExecutionClaimLost) {
			return nil, errTaskStateChanged
		}
		return nil, err
	}
	if err := s.agent.Up(ctx, req); err != nil {
		finished := time.Now().UTC()
		run.FinishedAt = &finished
		setRuntimeRunFailure(run, phase+".runtime_up", err)
		_ = s.saveRuntimeRun(ctx, run)
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
		_ = s.saveRuntimeRun(ctx, run)
		return run, err
	}
	if err := s.saveRuntimeRun(ctx, run); err != nil {
		return run, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, run.Status, "Agent Compose run finished", map[string]any{
		"phase":      phase,
		"run_id":     run.ExternalRunID,
		"session_id": run.ExternalSessionID,
	})
	return run, nil
}

func (s *TaskService) saveRuntimeRun(ctx context.Context, run *model.TaskRuntimeRun) error {
	err := s.repo.SaveRuntimeRun(ctx, run)
	if !errors.Is(err, repository.ErrTaskExecutionClaimLost) {
		return err
	}
	_, abandonErr := s.repo.AbandonRuntimeRun(context.WithoutCancel(ctx), run, "task execution ownership changed before runtime completion")
	return errors.Join(errTaskStateChanged, abandonErr)
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

func (s *TaskService) commandRunnerProfile(task *model.Task) string {
	if task == nil {
		return ""
	}
	if task.Route == model.TaskRouteTemplateFill || task.RunnerProfile == model.RunnerProfileNativeTemplateFill {
		return "prepare-only"
	}
	return task.RunnerProfile
}

func (s *TaskService) useFullPPTMaster(task *model.Task) bool {
	return task != nil && task.Route != model.TaskRouteTemplateFill && task.RunnerProfile == model.RunnerProfileFullPPTMaster
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
