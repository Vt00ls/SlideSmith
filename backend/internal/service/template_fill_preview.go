package service

import (
	"bytes"
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
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type TemplateFillPlanFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

type TemplateFillPlanPreview struct {
	TaskID      string               `json:"task_id"`
	ProjectPath string               `json:"project_path"`
	Inputs      TemplateFillInputs   `json:"inputs"`
	Plan        map[string]any       `json:"plan"`
	CheckReport map[string]any       `json:"check_report"`
	Summary     map[string]any       `json:"summary"`
	PlanFile    TemplateFillPlanFile `json:"plan_file"`
	CanEdit     bool                 `json:"can_edit"`
	CanConfirm  bool                 `json:"can_confirm"`
}

type templateFillAPISession struct {
	root               string
	candidateWorkspace string
	candidateProject   string
	backupWorkspace    string
}

func (s *TaskService) GetTemplateFillPlan(ctx context.Context, taskID string) (*TemplateFillPlanPreview, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
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
	if !templateFillPlanReadableStatus(task.Status) {
		return nil, fmt.Errorf("cannot read template fill plan while task status is %q", task.Status)
	}
	unlockSnapshot, err := s.lockTemplateFillProjectSnapshot(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlockSnapshot()
	return s.templateFillPlanPreview(task)
}

func (s *TaskService) SaveTemplateFillPlan(ctx context.Context, taskID string, submitted map[string]any) (_ *TemplateFillPlanPreview, resultErr error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
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
	if !templateFillPlanEditableTask(task) {
		return nil, fmt.Errorf("cannot edit template fill plan while task status is %q and failure phase is %q", task.Status, task.FailurePhase)
	}
	releaseClaim, err := s.claimTemplateFillAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releaseClaim())
	}()
	plan, err := cloneTemplateFillPlan(submitted)
	if err != nil {
		return nil, err
	}
	plan["status"] = "draft"

	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	session, err := s.newTemplateFillAPISession(ctx, task, projectPath, true)
	if err != nil {
		return nil, err
	}
	defer session.cleanup()

	inputs, err := discoverTemplateFillInputs(session.candidateProject)
	if err != nil {
		return nil, err
	}
	if err := writeTemplateFillPlanObject(inputs.FillPlan, plan); err != nil {
		return nil, err
	}
	if _, _, err := validateTemplateFillPlanContractSnapshot(session.candidateProject); err != nil {
		return nil, err
	}
	if err := removeTemplateFillFormalCheckEvidence(inputs); err != nil {
		return nil, err
	}

	validate := func(candidate string) error {
		contract, _, err := validateTemplateFillPlanContractSnapshot(candidate)
		if err != nil {
			return err
		}
		if status, _ := contract["plan_status"].(string); status != "draft" {
			return fmt.Errorf("saved template fill plan status = %q, expected %q", status, "draft")
		}
		return requireTemplateFillFormalCheckEvidenceAbsent(candidate)
	}
	workspaceDir := filepath.Dir(filepath.Dir(projectPath))
	if _, err := s.syncPreparedProjectValidated(ctx, task, session.candidateWorkspace, workspaceDir, validate); err != nil {
		rollbackErr := s.restoreTemplateFillAPIProject(ctx, task, session, workspaceDir)
		return nil, errors.Join(err, rollbackErr)
	}
	return s.templateFillPlanPreview(task)
}

func (s *TaskService) CheckTemplateFillPlan(ctx context.Context, taskID string) (_ *model.Task, resultErr error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
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
	if task.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		return nil, fmt.Errorf("cannot check template fill plan while task status is %q", task.Status)
	}
	releaseClaim, err := s.claimTemplateFillAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releaseClaim())
	}()
	if !s.agentCfg.Enabled || s.agent == nil {
		return nil, fmt.Errorf("agent compose disabled; cannot check template fill plan")
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	planContract, planSHA256, err := validateTemplateFillPlanContractSnapshot(projectPath)
	if err != nil {
		return nil, err
	}
	if status, _ := planContract["plan_status"].(string); status != "draft" {
		return nil, fmt.Errorf("template fill draft check plan status = %q, expected %q", status, "draft")
	}
	workspace := s.resolveTaskWorkspace(task)
	projectRel := s.projectRel(task, projectPath)
	command := templateFillFormalCheckCommand(projectRel)
	runtimeRun, err := s.runAgent(ctx, task, string(PhaseTemplateFillCheck), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	if err != nil {
		return nil, err
	}
	if runtimeRun == nil || strings.TrimSpace(runtimeRun.WorkspacePath) == "" {
		return nil, fmt.Errorf("template fill draft check did not return a distinct session workspace")
	}
	validate := func(candidate string) error {
		inputs, err := discoverTemplateFillInputs(candidate)
		if err != nil {
			return err
		}
		checkedSHA256, err := sha256File(inputs.FillPlan)
		if err != nil {
			return err
		}
		if checkedSHA256 != planSHA256 {
			return fmt.Errorf("template fill plan changed during draft check: got %s, expected %s", checkedSHA256, planSHA256)
		}
		_, err = validateTemplateFillCheckContractForPlan(candidate, false, "draft", planSHA256)
		return err
	}
	projectPath, err = s.syncRuntimeProjectValidated(ctx, task, workspace, runtimeRun.WorkspacePath, validate)
	if err != nil {
		return nil, err
	}
	contract, canonicalSHA256, err := validateTemplateFillPlanContractSnapshot(projectPath)
	if err != nil {
		return nil, err
	}
	if status, _ := contract["plan_status"].(string); status != "draft" || canonicalSHA256 != planSHA256 {
		return nil, fmt.Errorf("template fill canonical plan changed after draft check")
	}
	if _, err := validateTemplateFillCheckContractForPlan(projectPath, false, "draft", planSHA256); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) ConfirmTemplateFillPlan(ctx context.Context, taskID string) (_ *model.Task, resultErr error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
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
	if task.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		return nil, fmt.Errorf("cannot confirm template fill plan while task status is %q", task.Status)
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
		return nil, err
	}
	_, _, planStatus, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		return nil, err
	}
	if planStatus != "draft" {
		return nil, fmt.Errorf("cannot confirm template fill plan with status %q", planStatus)
	}
	checkErrors, err := currentTemplateFillCheckErrors(projectPath)
	if err != nil {
		return nil, err
	}
	if checkErrors > 0 {
		return nil, fmt.Errorf("cannot confirm template fill plan with %d check errors", checkErrors)
	}

	session, err := s.newTemplateFillAPISession(ctx, task, projectPath, true)
	if err != nil {
		return nil, err
	}
	defer session.cleanup()
	if err := setTemplateFillPlanStatus(session.candidateProject, "confirmed"); err != nil {
		return nil, err
	}
	validateConfirmed := func(candidate string) error {
		contract, _, err := validateTemplateFillPlanContractSnapshot(candidate)
		if err != nil {
			return err
		}
		if status, _ := contract["plan_status"].(string); status != "confirmed" {
			return fmt.Errorf("confirmed template fill plan status = %q, expected %q", status, "confirmed")
		}
		return nil
	}
	workspaceDir := filepath.Dir(filepath.Dir(projectPath))
	if _, err := s.syncPreparedProjectValidated(ctx, task, session.candidateWorkspace, workspaceDir, validateConfirmed); err != nil {
		rollbackErr := s.restoreTemplateFillAPIProject(ctx, task, session, workspaceDir)
		return nil, errors.Join(err, rollbackErr)
	}
	if s.beforeTemplateFillAPITransition != nil {
		s.beforeTemplateFillAPITransition(model.TaskStatusTemplateFillChecking)
	}
	if err := s.transitionTemplateFillAtGate(ctx, task, model.TaskStatusTemplateFillChecking, "Template fill checking", map[string]any{
		"project_path": projectPath,
	}); err != nil {
		rollbackErr := s.restoreTemplateFillAPIProject(ctx, task, session, workspaceDir)
		return nil, errors.Join(err, rollbackErr)
	}
	return task, nil
}

func (s *TaskService) RegenerateTemplateFillPlan(ctx context.Context, taskID string) (_ *model.Task, resultErr error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
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
	if task.Status != model.TaskStatusAwaitingTemplateFillConfirm && task.Status != model.TaskStatusFailed {
		return nil, fmt.Errorf("cannot regenerate template fill plan while task status is %q", task.Status)
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
		return nil, err
	}
	session, err := s.newTemplateFillAPISession(ctx, task, projectPath, true)
	if err != nil {
		return nil, err
	}
	defer session.cleanup()
	if err := cleanupTemplateFillRegenerateOutputs(session.candidateProject); err != nil {
		return nil, err
	}
	validateClean := func(candidate string) error {
		if _, err := discoverTemplateFillInputs(candidate); err != nil {
			return err
		}
		return requireTemplateFillRegenerateOutputsAbsent(candidate)
	}
	workspaceDir := filepath.Dir(filepath.Dir(projectPath))
	if _, err := s.syncPreparedProjectValidated(ctx, task, session.candidateWorkspace, workspaceDir, validateClean); err != nil {
		rollbackErr := s.restoreTemplateFillAPIProject(ctx, task, session, workspaceDir)
		return nil, errors.Join(err, rollbackErr)
	}
	if s.beforeTemplateFillAPITransition != nil {
		s.beforeTemplateFillAPITransition(model.TaskStatusTemplateFillPlanning)
	}
	if err := s.transitionTemplateFillAtGate(ctx, task, model.TaskStatusTemplateFillPlanning, "Template fill planning", map[string]any{
		"project_path": projectPath,
		"regenerated":  true,
	}); err != nil {
		rollbackErr := s.restoreTemplateFillAPIProject(ctx, task, session, workspaceDir)
		return nil, errors.Join(err, rollbackErr)
	}
	return task, nil
}

func (s *TaskService) templateFillPlanPreview(task *model.Task) (*TemplateFillPlanPreview, error) {
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		return nil, err
	}
	plan, rawPlan, info, err := readTemplateFillPlanFile(inputs.FillPlan)
	if err != nil {
		return nil, err
	}
	checkReport := map[string]any{}
	if _, err := os.Lstat(inputs.CheckReport); err == nil {
		checkReport, err = readTemplateFillJSONObject(inputs.CheckReport, "template fill check report")
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect template fill check report: %w", err)
	}

	planStatus, _ := plan["status"].(string)
	plannedSlideCount := 0
	if slides, ok := plan["slides"].([]any); ok {
		plannedSlideCount = len(slides)
	}
	checkOK, checkWarn, checkErrors, err := templateFillPreviewCheckSummary(checkReport)
	if err != nil {
		return nil, err
	}
	_, _, validatedStatus, planValidationErr := readValidatedTemplateFillPlan(projectPath)
	planValid := planValidationErr == nil
	if planValid {
		planStatus = validatedStatus
	}
	canEdit := templateFillPlanEditableTask(task)
	canConfirm := planValid && planStatus == "draft" && task.Status == model.TaskStatusAwaitingTemplateFillConfirm && checkErrors == 0
	return &TemplateFillPlanPreview{
		TaskID:      task.ID,
		ProjectPath: inputs.ProjectPath,
		Inputs:      inputs,
		Plan:        plan,
		CheckReport: checkReport,
		Summary: map[string]any{
			"plan_status":          planStatus,
			"planned_slide_count":  plannedSlideCount,
			"source_pptx_name":     filepath.Base(inputs.SourcePPTX),
			"content_source_count": len(inputs.ContentSources),
			"check_ok":             checkOK,
			"check_warn":           checkWarn,
			"check_error":          checkErrors,
		},
		PlanFile: TemplateFillPlanFile{
			Name:      filepath.Base(inputs.FillPlan),
			Path:      inputs.FillPlan,
			Content:   string(rawPlan),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
		},
		CanEdit:    canEdit,
		CanConfirm: canConfirm,
	}, nil
}

func (s *TaskService) newTemplateFillAPISession(ctx context.Context, task *model.Task, projectPath string, withBackup bool) (*templateFillAPISession, error) {
	workspace := s.resolveTaskWorkspace(task)
	canonicalProject := filepath.Join(workspace.HostDir, "projects", filepath.Base(projectPath))
	if !sameFilesystemPath(projectPath, canonicalProject) {
		return nil, fmt.Errorf("template fill API project is not canonical: %s", projectPath)
	}
	root := filepath.Join(workspace.HostDir, ".slidesmith", "template-fill-api-sessions", uuid.NewString())
	candidateWorkspace := filepath.Join(root, "candidate")
	candidateProject := filepath.Join(candidateWorkspace, "projects", filepath.Base(projectPath))
	if err := os.MkdirAll(filepath.Dir(candidateProject), 0o755); err != nil {
		return nil, err
	}
	if err := copyProjectDirectoryStrict(ctx, projectPath, candidateProject); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	session := &templateFillAPISession{
		root:               root,
		candidateWorkspace: candidateWorkspace,
		candidateProject:   candidateProject,
	}
	if !withBackup {
		return session, nil
	}
	backupWorkspace := filepath.Join(root, "backup")
	backupProject := filepath.Join(backupWorkspace, "projects", filepath.Base(projectPath))
	if err := os.MkdirAll(filepath.Dir(backupProject), 0o755); err != nil {
		session.cleanup()
		return nil, err
	}
	if err := copyProjectDirectoryStrict(ctx, projectPath, backupProject); err != nil {
		session.cleanup()
		return nil, err
	}
	session.backupWorkspace = backupWorkspace
	return session, nil
}

func (session *templateFillAPISession) cleanup() {
	if session != nil && session.root != "" {
		_ = os.RemoveAll(session.root)
	}
}

func (s *TaskService) restoreTemplateFillAPIProject(ctx context.Context, task *model.Task, session *templateFillAPISession, workspaceDir string) error {
	if session == nil || session.backupWorkspace == "" {
		return fmt.Errorf("template fill API rollback backup is unavailable")
	}
	returnStatus := task.Status
	task.Status = taskTemplateFillPreviousStatus(task)
	_, err := s.syncPreparedProjectValidated(context.WithoutCancel(ctx), task, session.backupWorkspace, workspaceDir, nil)
	if err != nil {
		task.Status = returnStatus
	}
	return err
}

func taskTemplateFillPreviousStatus(task *model.Task) string {
	if task.Status == model.TaskStatusTemplateFillChecking || task.Status == model.TaskStatusTemplateFillPlanning {
		if task.FailurePhase != "" {
			return model.TaskStatusFailed
		}
		return model.TaskStatusAwaitingTemplateFillConfirm
	}
	return task.Status
}

func (s *TaskService) transitionTemplateFillAtGate(ctx context.Context, task *model.Task, target, message string, payload map[string]any) error {
	expectedStatus := task.Status
	previousError := task.ErrorMessage
	previousFailurePhase := task.FailurePhase
	previousFailureMetadata := task.FailureMetadata
	if err := s.machine.Validate(expectedStatus, target); err != nil {
		return err
	}
	task.Status = target
	task.ErrorMessage = ""
	task.FailurePhase = ""
	task.FailureMetadata = "{}"
	saved, err := s.repo.SaveTaskIfStatus(ctx, task, expectedStatus, task.ExecutionClaimToken)
	if err != nil || !saved {
		task.Status = expectedStatus
		task.ErrorMessage = previousError
		task.FailurePhase = previousFailurePhase
		task.FailureMetadata = previousFailureMetadata
		if err != nil {
			return err
		}
		return errTaskStateChanged
	}
	_ = s.event(ctx, task.ID, model.EventTypeStatus, target, message, payload)
	return nil
}

func requireTemplateFillRoute(task *model.Task) error {
	if task.Route != model.TaskRouteTemplateFill {
		return fmt.Errorf("task route must be %q, got %q", model.TaskRouteTemplateFill, task.Route)
	}
	return nil
}

func (s *TaskService) lockTemplateFillAPI(ctx context.Context, task *model.Task) (func(), error) {
	workspace := s.resolveTaskWorkspace(task)
	return acquireProjectPromotionLock(ctx, filepath.Join(workspace.HostDir, ".slidesmith", "template-fill-api.lock"))
}

func (s *TaskService) lockTemplateFillProjectSnapshot(ctx context.Context, task *model.Task) (func(), error) {
	workspace := s.resolveTaskWorkspace(task)
	return acquireProjectPromotionLock(ctx, filepath.Join(workspace.HostDir, ".slidesmith", "project-promotions.lock"))
}

func (s *TaskService) reloadTemplateFillAPITask(ctx context.Context, taskID string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := requireTemplateFillRoute(task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) claimTemplateFillAPI(ctx context.Context, task *model.Task) (func() error, error) {
	claimToken := "template-fill-api-" + uuid.NewString()
	claimedAt := time.Now().UTC()
	claimed, err := s.repo.ClaimTaskExecution(
		ctx,
		task.ID,
		task.Status,
		claimToken,
		claimedAt,
		claimedAt.Add(-s.taskExecutionLeaseDuration()),
	)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errTaskStateChanged
	}
	task.ExecutionClaimToken = claimToken
	task.ExecutionClaimedAt = &claimedAt
	return func() error {
		released, err := s.repo.ReleaseTaskExecution(context.WithoutCancel(ctx), task.ID, claimToken)
		if err != nil {
			return fmt.Errorf("release template fill API execution claim: %w", err)
		}
		if !released {
			return fmt.Errorf("release template fill API execution claim: %w", errTaskStateChanged)
		}
		task.ExecutionClaimToken = ""
		task.ExecutionClaimedAt = nil
		return nil
	}, nil
}

func templateFillPlanReadableStatus(status string) bool {
	switch status {
	case model.TaskStatusAwaitingTemplateFillConfirm,
		model.TaskStatusTemplateFillChecking,
		model.TaskStatusTemplateFillApplying,
		model.TaskStatusTemplateFillValidating,
		model.TaskStatusPublishing,
		model.TaskStatusCompleted,
		model.TaskStatusFailed:
		return true
	default:
		return false
	}
}

func templateFillPlanEditableTask(task *model.Task) bool {
	return task.Status == model.TaskStatusAwaitingTemplateFillConfirm ||
		(task.Status == model.TaskStatusFailed && strings.HasPrefix(task.FailurePhase, string(PhaseTemplateFillCheck)))
}

func cloneTemplateFillPlan(submitted map[string]any) (map[string]any, error) {
	if submitted == nil {
		return nil, fmt.Errorf("template fill plan is required")
	}
	raw, err := json.Marshal(submitted)
	if err != nil {
		return nil, fmt.Errorf("encode template fill plan: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var plan map[string]any
	if err := decoder.Decode(&plan); err != nil {
		return nil, fmt.Errorf("decode template fill plan: %w", err)
	}
	return plan, nil
}

func writeTemplateFillPlanObject(path string, plan map[string]any) (resultErr error) {
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode template fill plan: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".fill-plan-candidate-*.json")
	if err != nil {
		return fmt.Errorf("create template fill plan candidate: %w", err)
	}
	temporaryPath := file.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); closeErr != nil && resultErr == nil {
				resultErr = closeErr
			}
		}
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write template fill plan candidate: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync template fill plan candidate: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close template fill plan candidate: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace template fill plan: %w", err)
	}
	return nil
}

func readTemplateFillPlanFile(path string) (map[string]any, []byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read template fill plan: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, nil, fmt.Errorf("read template fill plan: path is not a regular non-symlinked file: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read template fill plan: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, nil, fmt.Errorf("parse template fill plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, nil, nil, fmt.Errorf("parse template fill plan: multiple JSON values")
		}
		return nil, nil, nil, fmt.Errorf("parse template fill plan: %w", err)
	}
	plan, ok := value.(map[string]any)
	if !ok {
		return nil, nil, nil, fmt.Errorf("template fill plan must be a JSON object")
	}
	return plan, raw, info, nil
}

func templateFillPreviewCheckSummary(report map[string]any) (int, int, int, error) {
	if len(report) == 0 {
		return 0, 0, 0, nil
	}
	if schema, _ := report["schema"].(string); schema != "template_fill_pptx_check.v1" {
		return 0, 0, 0, fmt.Errorf("template fill check report schema = %#v, expected %q", report["schema"], "template_fill_pptx_check.v1")
	}
	summary, err := templateFillSummary(report, "template fill check report", "ok", "warn", "error")
	if err != nil {
		return 0, 0, 0, err
	}
	return summary["ok"].(int), summary["warn"].(int), summary["error"].(int), nil
}

func currentTemplateFillCheckErrors(projectPath string) (int, error) {
	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		return 0, err
	}
	report, err := readTemplateFillJSONObject(inputs.CheckReport, "template fill check report")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	_, _, checkErrors, err := templateFillPreviewCheckSummary(report)
	return checkErrors, err
}

func cleanupTemplateFillRegenerateOutputs(projectPath string) error {
	for _, path := range templateFillRegenerateOutputPaths(projectPath) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect template fill downstream output %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("template fill downstream output must not be a symlink: %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove template fill downstream output %s: %w", path, err)
		}
	}
	return nil
}

func requireTemplateFillRegenerateOutputsAbsent(projectPath string) error {
	for _, path := range templateFillRegenerateOutputPaths(projectPath) {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("template fill downstream output still exists: %s", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect template fill downstream output %s: %w", path, err)
		}
	}
	return nil
}

func templateFillRegenerateOutputPaths(projectPath string) []string {
	return []string{
		filepath.Join(projectPath, "analysis", "fill_plan.json"),
		filepath.Join(projectPath, "analysis", "check_report.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillPlan)+".json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillCheck)+".json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillApply)+".json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseTemplateFillValidate)+".json"),
		filepath.Join(projectPath, "exports"),
		filepath.Join(projectPath, "validation"),
	}
}
