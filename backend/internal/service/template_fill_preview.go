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
	provenance         templateFillSourceProvenance
	removeAll          func(string) error
}

type templateFillProjectExchange struct {
	service        *TaskService
	task           *model.Task
	staged         *stagedProjectPromotion
	markerPath     string
	expectedStatus string
	targetStatus   string
	unlock         func()
	exchanged      bool
	finished       bool
}

const (
	templateFillCommittedCleanupDir       = "template-fill-committed-cleanup"
	templateFillPendingCleanupMarkerExt   = ".pending"
	templateFillCommittedCleanupMarkerExt = ".path"
	templateFillProtectedCleanupMarkerExt = ".protected"
)

type templateFillCleanupDebt struct {
	TaskID         string `json:"task_id"`
	AttemptPath    string `json:"attempt_path"`
	ExpectedStatus string `json:"expected_status"`
	TargetStatus   string `json:"target_status"`
}

type templateFillCleanupMarkerOps struct {
	createTemp    func(string, string) (*os.File, error)
	write         func(*os.File, []byte) (int, error)
	rename        func(string, string) error
	remove        func(string) error
	syncDirectory func(string) error
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
	if err := s.sweepCommittedTemplateFillPromotions(ctx, task); err != nil {
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
	plan, err := cloneTemplateFillPlan(submitted)
	if err != nil {
		return nil, err
	}
	plan["status"] = "draft"

	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	session, err := s.newTemplateFillAPISession(ctx, task, projectPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.cleanup())
	}()

	inputs, err := discoverTemplateFillInputsWithProvenance(session.candidateProject, session.provenance)
	if err != nil {
		return nil, fmt.Errorf("discover template fill save candidate: %w", err)
	}
	if err := writeTemplateFillPlanObject(inputs.FillPlan, plan); err != nil {
		return nil, err
	}
	if _, _, err := validateTemplateFillPlanContractSnapshotWithProvenance(session.candidateProject, session.provenance); err != nil {
		return nil, fmt.Errorf("validate template fill save candidate: %w", err)
	}
	if err := removeTemplateFillFormalCheckEvidence(inputs); err != nil {
		return nil, err
	}

	validate := func(candidate string) error {
		contract, _, err := validateTemplateFillPlanContractSnapshotWithProvenance(candidate, session.provenance)
		if err != nil {
			return err
		}
		if status, _ := contract["plan_status"].(string); status != "draft" {
			return fmt.Errorf("saved template fill plan status = %q, expected %q", status, "draft")
		}
		return requireTemplateFillFormalCheckEvidenceAbsentWithProvenance(candidate, session.provenance)
	}
	exchange, err := s.beginTemplateFillProjectExchange(ctx, task, session, task.Status, validate)
	if err != nil {
		return nil, fmt.Errorf("promote template fill save candidate: %w", err)
	}
	if s.beforeTemplateFillAPICommit != nil {
		s.beforeTemplateFillAPICommit("template_fill_preview")
	}
	preview, err := s.templateFillPlanPreview(task)
	if err != nil {
		return nil, errors.Join(err, exchange.rollback())
	}
	if err := exchange.commit(ctx); err != nil {
		return nil, errors.Join(err, exchange.rollback())
	}
	return preview, nil
}

func (s *TaskService) CheckTemplateFillPlan(ctx context.Context, taskID string) (_ *model.Task, resultErr error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := requireTemplateFillRoute(task); err != nil {
		return nil, err
	}
	unlockAPI, err := s.lockTemplateFillAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer func() {
		if unlockAPI != nil {
			unlockAPI()
		}
	}()
	task, err = s.reloadTemplateFillAPITask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		return nil, fmt.Errorf("cannot check template fill plan while task status is %q", task.Status)
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
	if !s.agentCfg.Enabled || s.agent == nil {
		return nil, fmt.Errorf("agent compose disabled; cannot check template fill plan")
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return nil, err
	}
	planContract, planSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err != nil {
		return nil, err
	}
	if status, _ := planContract["plan_status"].(string); status != "draft" {
		return nil, fmt.Errorf("template fill draft check plan status = %q, expected %q", status, "draft")
	}
	workspace := s.resolveTaskWorkspace(task)
	projectRel := s.projectRel(task, projectPath)
	command := templateFillFormalCheckCommand(projectRel)
	unlockAPI()
	unlockAPI = nil
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
	unlockAPI, err = s.lockTemplateFillAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	persistedTask, err := s.reloadTemplateFillAPITask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if persistedTask.Status != task.Status || persistedTask.ExecutionClaimToken != task.ExecutionClaimToken {
		return nil, errTaskStateChanged
	}
	if err := provenance.revalidateAuthoritative(); err != nil {
		return nil, err
	}
	_, _, canonicalStatus, currentPlanSHA256, err := readValidatedTemplateFillPlanWithSHA256AndProvenance(projectPath, provenance)
	if err != nil {
		return nil, err
	}
	if canonicalStatus != "draft" || currentPlanSHA256 != planSHA256 {
		return nil, fmt.Errorf("template fill canonical plan changed during draft check")
	}
	validate := func(candidate string) error {
		inputs, err := discoverTemplateFillInputsWithProvenance(candidate, provenance)
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
		_, err = validateTemplateFillCheckContractForPlanWithProvenance(candidate, provenance, false, "draft", planSHA256)
		return err
	}
	projectPath, err = s.syncRuntimeProjectValidatedWithFence(
		ctx,
		task,
		workspace,
		runtimeRun.WorkspacePath,
		validate,
		provenance.revalidateAuthoritative,
	)
	if err != nil {
		return nil, err
	}
	contract, canonicalSHA256, err := validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
	if err != nil {
		return nil, err
	}
	if status, _ := contract["plan_status"].(string); status != "draft" || canonicalSHA256 != planSHA256 {
		return nil, fmt.Errorf("template fill canonical plan changed after draft check")
	}
	if _, err := validateTemplateFillCheckContractForPlanWithProvenance(projectPath, provenance, false, "draft", planSHA256); err != nil {
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
	if !templateFillPlanConfirmableTask(task) {
		return nil, fmt.Errorf("cannot confirm template fill plan while task status is %q", task.Status)
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

	session, err := s.newTemplateFillAPISession(ctx, task, projectPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.cleanup())
	}()
	if err := setTemplateFillPlanStatusWithProvenance(session.candidateProject, "confirmed", session.provenance); err != nil {
		return nil, err
	}
	validateConfirmed := func(candidate string) error {
		contract, _, err := validateTemplateFillPlanContractSnapshotWithProvenance(candidate, session.provenance)
		if err != nil {
			return err
		}
		if status, _ := contract["plan_status"].(string); status != "confirmed" {
			return fmt.Errorf("confirmed template fill plan status = %q, expected %q", status, "confirmed")
		}
		return nil
	}
	exchange, err := s.beginTemplateFillProjectExchange(ctx, task, session, model.TaskStatusTemplateFillChecking, validateConfirmed)
	if err != nil {
		return nil, err
	}
	if s.beforeTemplateFillAPICommit != nil {
		s.beforeTemplateFillAPICommit(model.TaskStatusTemplateFillChecking)
	}
	if err := s.transitionTemplateFillAtGate(ctx, task, model.TaskStatusTemplateFillChecking, "Template fill checking", map[string]any{
		"project_path": projectPath,
	}); err != nil {
		return nil, errors.Join(err, exchange.rollback())
	}
	exchange.commitAfterDB(ctx)
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
		return nil, err
	}
	session, err := s.newTemplateFillAPISession(ctx, task, projectPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, session.cleanup())
	}()
	if err := cleanupTemplateFillRegenerateOutputs(session.candidateProject); err != nil {
		return nil, err
	}
	validateClean := func(candidate string) error {
		if _, err := discoverTemplateFillInputsWithProvenance(candidate, session.provenance); err != nil {
			return err
		}
		return requireTemplateFillRegenerateOutputsAbsent(candidate)
	}
	exchange, err := s.beginTemplateFillProjectExchange(ctx, task, session, model.TaskStatusTemplateFillPlanning, validateClean)
	if err != nil {
		return nil, err
	}
	if s.beforeTemplateFillAPICommit != nil {
		s.beforeTemplateFillAPICommit(model.TaskStatusTemplateFillPlanning)
	}
	if err := s.transitionTemplateFillAtGate(ctx, task, model.TaskStatusTemplateFillPlanning, "Template fill planning", map[string]any{
		"project_path": projectPath,
		"regenerated":  true,
	}); err != nil {
		return nil, errors.Join(err, exchange.rollback())
	}
	exchange.commitAfterDB(ctx)
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
	canConfirm := planValid && planStatus == "draft" && templateFillPlanConfirmableTask(task) && checkErrors == 0
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

func (s *TaskService) newTemplateFillAPISession(ctx context.Context, task *model.Task, projectPath string) (*templateFillAPISession, error) {
	workspace := s.resolveTaskWorkspace(task)
	canonicalProject := filepath.Join(workspace.HostDir, "projects", filepath.Base(projectPath))
	if !sameFilesystemPath(projectPath, canonicalProject) {
		return nil, fmt.Errorf("template fill API project is not canonical: %s", projectPath)
	}
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(workspace.HostDir, ".slidesmith", "template-fill-api-sessions", uuid.NewString())
	candidateWorkspace := filepath.Join(root, "candidate")
	candidateProject := filepath.Join(candidateWorkspace, "projects", filepath.Base(projectPath))
	session := &templateFillAPISession{
		root:               root,
		candidateWorkspace: candidateWorkspace,
		candidateProject:   candidateProject,
		provenance:         provenance,
		removeAll:          os.RemoveAll,
	}
	if err := os.MkdirAll(filepath.Dir(candidateProject), 0o755); err != nil {
		return nil, err
	}
	if err := copyProjectDirectoryStrict(ctx, projectPath, candidateProject); err != nil {
		return nil, errors.Join(err, session.cleanup())
	}
	if err := provenance.validateCandidate(candidateProject); err != nil {
		return nil, errors.Join(err, session.cleanup())
	}
	return session, nil
}

func (session *templateFillAPISession) cleanup() error {
	if session == nil || session.root == "" {
		return nil
	}
	removeAll := session.removeAll
	if removeAll == nil {
		removeAll = os.RemoveAll
	}
	if err := removeAll(session.root); err != nil {
		return fmt.Errorf("remove template fill API session %s: %w", session.root, err)
	}
	session.root = ""
	return nil
}

func (s *TaskService) beginTemplateFillProjectExchange(
	ctx context.Context,
	task *model.Task,
	session *templateFillAPISession,
	targetStatus string,
	validate func(string) error,
) (*templateFillProjectExchange, error) {
	if session == nil {
		return nil, fmt.Errorf("template fill API session is required")
	}
	if err := session.provenance.revalidateAuthoritative(); err != nil {
		return nil, err
	}
	if err := session.provenance.validateCandidate(session.candidateProject); err != nil {
		return nil, err
	}
	workspace := s.resolveTaskWorkspace(task)
	staged, err := s.stagePreparedProject(ctx, task, session.candidateWorkspace, workspace.HostDir)
	if err != nil {
		return nil, errors.Join(err, session.cleanup())
	}
	cleanupStaged := func(cause error) error {
		return errors.Join(cause, staged.cleanup())
	}
	if staged.noOp {
		return nil, cleanupStaged(fmt.Errorf("template fill API exchange requires a distinct staged project"))
	}
	if validate != nil {
		if err := validate(staged.projectPath); err != nil {
			return nil, cleanupStaged(err)
		}
	}
	if err := session.cleanup(); err != nil {
		return nil, cleanupStaged(err)
	}
	if s.beforeTemplateFillPromotionLock != nil {
		s.beforeTemplateFillPromotionLock()
	}

	lockPath := filepath.Join(filepath.Dir(staged.promotionRoot), "project-promotions.lock")
	unlock, err := acquireProjectPromotionLock(ctx, lockPath)
	if err != nil {
		return nil, cleanupStaged(err)
	}
	exchange := &templateFillProjectExchange{
		service:        s,
		task:           task,
		staged:         staged,
		expectedStatus: task.Status,
		targetStatus:   targetStatus,
		unlock:         unlock,
	}
	fail := func(cause error) (*templateFillProjectExchange, error) {
		unlock()
		exchange.finished = true
		cleanupErr := staged.cleanup()
		if cleanupErr == nil {
			exchange.removeMarker()
		} else if exchange.markerPath != "" {
			cleanupErr = errors.Join(cleanupErr, exchange.activateCommitMarker())
		}
		return nil, errors.Join(cause, cleanupErr)
	}
	matched, err := s.repo.RenewTaskExecutionClaim(ctx, task.ID, task.Status, task.ExecutionClaimToken)
	if err != nil {
		return fail(err)
	}
	if !matched {
		return fail(errTaskStateChanged)
	}
	if err := session.provenance.revalidateAuthoritative(); err != nil {
		return fail(fmt.Errorf("revalidate template fill provenance under promotion lock: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := requireRealProjectDirectory(staged.projectPath, "staged template fill API project"); err != nil {
		return fail(err)
	}
	if err := requireRealProjectDirectory(staged.targetPath, "canonical template fill API project"); err != nil {
		return fail(err)
	}
	markerPath, err := writeTemplateFillPendingCleanupMarker(staged, task.ID, task.Status, targetStatus)
	if err != nil {
		return fail(err)
	}
	exchange.markerPath = markerPath
	exchangeDirectories := staged.exchangeDirectories
	if exchangeDirectories == nil {
		exchangeDirectories = atomicExchangeDirectories
	}
	if err := exchangeDirectories(staged.projectPath, staged.targetPath); err != nil {
		return fail(fmt.Errorf("atomically exchange template fill API project: %w", err))
	}
	staged.recoveryPath = staged.projectPath
	exchange.exchanged = true
	if validate != nil {
		if err := validate(staged.targetPath); err != nil {
			rollbackErr := exchange.rollback()
			return nil, errors.Join(fmt.Errorf("revalidate template fill API canonical project: %w", err), rollbackErr)
		}
	}
	return exchange, nil
}

func (exchange *templateFillProjectExchange) rollback() error {
	if exchange == nil || exchange.finished {
		return nil
	}
	defer exchange.finishUnlock()
	if !exchange.exchanged {
		cleanupErr := exchange.staged.cleanup()
		if cleanupErr == nil {
			exchange.removeMarker()
		} else {
			cleanupErr = errors.Join(cleanupErr, exchange.activateCommitMarker())
		}
		return cleanupErr
	}
	exchange.staged.retainRecovery = true
	if err := exchange.activateProtectedMarker(); err != nil {
		return err
	}
	exchangeDirectories := exchange.staged.exchangeDirectories
	if exchangeDirectories == nil {
		exchangeDirectories = atomicExchangeDirectories
	}
	if err := exchangeDirectories(exchange.staged.projectPath, exchange.staged.targetPath); err != nil {
		return fmt.Errorf(
			"restore template fill API canonical project without DB (old canonical retained at %s): %w",
			exchange.staged.projectPath,
			err,
		)
	}
	exchange.exchanged = false
	exchange.staged.recoveryPath = ""
	exchange.staged.retainRecovery = false
	if err := exchange.activateCommitMarker(); err != nil {
		return err
	}
	cleanupErr := exchange.staged.cleanup()
	if cleanupErr == nil {
		exchange.removeMarker()
	} else {
		cleanupErr = errors.Join(cleanupErr, exchange.activateCommitMarker())
	}
	return cleanupErr
}

func (exchange *templateFillProjectExchange) commit(ctx context.Context) error {
	if exchange == nil || exchange.finished {
		return nil
	}
	if err := exchange.activateCommitMarker(); err != nil {
		return err
	}
	exchange.finishCommitted(ctx, nil)
	return nil
}

func (exchange *templateFillProjectExchange) commitAfterDB(ctx context.Context) {
	if exchange == nil || exchange.finished {
		return
	}
	markerErr := exchange.activateCommitMarker()
	exchange.finishCommitted(ctx, markerErr)
}

func (exchange *templateFillProjectExchange) activateCommitMarker() error {
	if exchange == nil || exchange.markerPath == "" {
		return fmt.Errorf("template fill cleanup marker is unavailable")
	}
	extension := filepath.Ext(exchange.markerPath)
	if extension == templateFillCommittedCleanupMarkerExt {
		return nil
	}
	if extension != templateFillPendingCleanupMarkerExt && extension != templateFillProtectedCleanupMarkerExt {
		return fmt.Errorf("commit template fill cleanup marker in state %q", extension)
	}
	committedPath := strings.TrimSuffix(exchange.markerPath, extension) + templateFillCommittedCleanupMarkerExt
	if err := os.Rename(exchange.markerPath, committedPath); err != nil {
		return fmt.Errorf("activate committed template fill cleanup marker: %w", err)
	}
	exchange.markerPath = committedPath
	if err := syncTemplateFillCleanupMarkerDirectory(filepath.Dir(committedPath)); err != nil {
		return fmt.Errorf("sync committed template fill cleanup marker: %w", err)
	}
	return nil
}

func (exchange *templateFillProjectExchange) activateProtectedMarker() error {
	if exchange == nil || exchange.markerPath == "" {
		return fmt.Errorf("template fill cleanup marker is unavailable")
	}
	extension := filepath.Ext(exchange.markerPath)
	if extension == templateFillProtectedCleanupMarkerExt {
		return nil
	}
	if extension != templateFillPendingCleanupMarkerExt && extension != templateFillCommittedCleanupMarkerExt {
		return fmt.Errorf("protect template fill cleanup marker in state %q", extension)
	}
	protectedPath := strings.TrimSuffix(exchange.markerPath, extension) + templateFillProtectedCleanupMarkerExt
	if err := os.Rename(exchange.markerPath, protectedPath); err != nil {
		return fmt.Errorf("activate protected template fill cleanup marker: %w", err)
	}
	exchange.markerPath = protectedPath
	if err := syncTemplateFillCleanupMarkerDirectory(filepath.Dir(protectedPath)); err != nil {
		return fmt.Errorf("sync protected template fill cleanup marker: %w", err)
	}
	return nil
}

func (exchange *templateFillProjectExchange) finishCommitted(ctx context.Context, markerErr error) {
	cleanupErr := exchange.staged.cleanup()
	if cleanupErr == nil {
		exchange.removeMarker()
	}
	exchange.finishUnlock()
	if cleanupErr != nil {
		_ = exchange.service.event(context.WithoutCancel(ctx), exchange.task.ID, model.EventTypeRuntime, "template_fill_cleanup_pending", "Template fill cleanup pending", map[string]any{
			"path":          exchange.staged.attemptRoot,
			"marker_error":  errorString(markerErr),
			"cleanup_error": errorString(cleanupErr),
		})
	}
}

func (exchange *templateFillProjectExchange) removeMarker() {
	if exchange == nil || exchange.markerPath == "" {
		return
	}
	_ = os.Remove(exchange.markerPath)
	_ = os.Remove(filepath.Dir(exchange.markerPath))
	exchange.markerPath = ""
}

func (exchange *templateFillProjectExchange) finishUnlock() {
	if exchange == nil || exchange.finished {
		return
	}
	exchange.finished = true
	if exchange.unlock != nil {
		exchange.unlock()
	}
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

func (s *TaskService) sweepTemplateFillAPISessions(task *model.Task) error {
	workspace := s.resolveTaskWorkspace(task)
	root := filepath.Join(workspace.HostDir, ".slidesmith", "template-fill-api-sessions")
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect stale template fill API sessions: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("template fill API sessions root must be a real directory: %s", root)
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove stale template fill API sessions %s: %w", root, err)
	}
	return nil
}

func (s *TaskService) sweepCommittedTemplateFillPromotions(ctx context.Context, task *model.Task) error {
	workspace := s.resolveTaskWorkspace(task)
	promotionRoot := filepath.Join(workspace.HostDir, ".slidesmith", "project-promotions")
	debtRoot := filepath.Join(workspace.HostDir, ".slidesmith", templateFillCommittedCleanupDir)
	unlock, err := s.lockTemplateFillProjectSnapshot(ctx, task)
	if err != nil {
		return err
	}
	defer unlock()
	info, err := os.Lstat(debtRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect committed template fill cleanup debt: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("committed template fill cleanup debt must be a real directory: %s", debtRoot)
	}
	markers, err := os.ReadDir(debtRoot)
	if err != nil {
		return fmt.Errorf("read committed template fill cleanup debt: %w", err)
	}
	for _, marker := range markers {
		if marker.IsDir() {
			continue
		}
		markerPath := filepath.Join(debtRoot, marker.Name())
		markerInfo, err := os.Lstat(markerPath)
		if err != nil {
			return fmt.Errorf("inspect committed template fill cleanup marker %s: %w", markerPath, err)
		}
		if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
			return fmt.Errorf("committed template fill cleanup marker must be a regular non-symlinked file: %s", markerPath)
		}
		if filepath.Ext(markerPath) != templateFillCommittedCleanupMarkerExt {
			continue
		}
		rawDebt, err := os.ReadFile(markerPath)
		if err != nil {
			return fmt.Errorf("read committed template fill cleanup marker %s: %w", markerPath, err)
		}
		var debt templateFillCleanupDebt
		if err := json.Unmarshal(rawDebt, &debt); err != nil {
			return fmt.Errorf("parse template fill cleanup marker %s: %w", markerPath, err)
		}
		if debt.TaskID != task.ID {
			return fmt.Errorf("template fill cleanup marker task = %q, expected %q", debt.TaskID, task.ID)
		}
		attemptPath := debt.AttemptPath
		claimPath := filepath.Dir(attemptPath)
		if !pathWithinRoot(promotionRoot, attemptPath) || !strings.HasPrefix(filepath.Base(claimPath), "template-fill-api-") {
			return fmt.Errorf("committed template fill cleanup path is invalid: %s", attemptPath)
		}
		if err := os.RemoveAll(attemptPath); err != nil {
			return fmt.Errorf("retry committed template fill promotion cleanup %s: %w", attemptPath, err)
		}
		if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove committed template fill cleanup marker %s: %w", markerPath, err)
		}
		_ = os.Remove(claimPath)
	}
	_ = os.Remove(debtRoot)
	_ = os.Remove(promotionRoot)
	return nil
}

func writeTemplateFillPendingCleanupMarker(staged *stagedProjectPromotion, taskID, expectedStatus, targetStatus string) (string, error) {
	if staged == nil || staged.attemptRoot == "" || staged.promotionRoot == "" {
		return "", fmt.Errorf("template fill committed cleanup staging paths are unavailable")
	}
	debtRoot := filepath.Join(filepath.Dir(staged.promotionRoot), templateFillCommittedCleanupDir)
	if err := os.MkdirAll(debtRoot, 0o700); err != nil {
		return "", fmt.Errorf("create committed template fill cleanup debt directory: %w", err)
	}
	markerPath := filepath.Join(debtRoot, filepath.Base(staged.attemptRoot)+templateFillPendingCleanupMarkerExt)
	rawDebt, err := json.Marshal(templateFillCleanupDebt{
		TaskID:         taskID,
		AttemptPath:    staged.attemptRoot,
		ExpectedStatus: expectedStatus,
		TargetStatus:   targetStatus,
	})
	if err != nil {
		return "", fmt.Errorf("encode pending template fill cleanup marker: %w", err)
	}
	if err := writeTemplateFillCleanupMarkerAtomically(
		markerPath,
		append(rawDebt, '\n'),
		defaultTemplateFillCleanupMarkerOps(),
	); err != nil {
		return "", fmt.Errorf("write pending template fill cleanup marker: %w", err)
	}
	return markerPath, nil
}

func defaultTemplateFillCleanupMarkerOps() templateFillCleanupMarkerOps {
	return templateFillCleanupMarkerOps{
		createTemp: os.CreateTemp,
		write: func(file *os.File, raw []byte) (int, error) {
			return file.Write(raw)
		},
		rename:        os.Rename,
		remove:        os.Remove,
		syncDirectory: syncTemplateFillCleanupMarkerDirectory,
	}
}

func writeTemplateFillCleanupMarkerAtomically(
	markerPath string,
	raw []byte,
	ops templateFillCleanupMarkerOps,
) (resultErr error) {
	defaults := defaultTemplateFillCleanupMarkerOps()
	if ops.createTemp == nil {
		ops.createTemp = defaults.createTemp
	}
	if ops.write == nil {
		ops.write = defaults.write
	}
	if ops.rename == nil {
		ops.rename = defaults.rename
	}
	if ops.remove == nil {
		ops.remove = defaults.remove
	}
	if ops.syncDirectory == nil {
		ops.syncDirectory = defaults.syncDirectory
	}

	markerDirectory := filepath.Dir(markerPath)
	temporary, err := ops.createTemp(markerDirectory, "."+filepath.Base(markerPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create cleanup marker temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); resultErr == nil && closeErr != nil {
				resultErr = closeErr
			}
		}
		if removeErr := ops.remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove cleanup marker temporary file: %w", removeErr))
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set cleanup marker temporary file mode: %w", err)
	}
	written, err := ops.write(temporary, raw)
	if err != nil {
		return fmt.Errorf("write cleanup marker temporary file: %w", err)
	}
	if written != len(raw) {
		return fmt.Errorf("write cleanup marker temporary file: %w", io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync cleanup marker temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close cleanup marker temporary file: %w", err)
	}
	closed = true
	if err := ops.rename(temporaryPath, markerPath); err != nil {
		return fmt.Errorf("publish cleanup marker: %w", err)
	}
	if err := ops.syncDirectory(markerDirectory); err != nil {
		return fmt.Errorf("sync cleanup marker directory: %w", err)
	}
	return nil
}

func syncTemplateFillCleanupMarkerDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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

func templateFillPlanConfirmableTask(task *model.Task) bool {
	return templateFillPlanEditableTask(task)
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
