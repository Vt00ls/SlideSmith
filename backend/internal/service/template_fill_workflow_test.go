package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type templateFillWorkflowAgent struct {
	projectPath                 string
	sessionRoot                 string
	requests                    []AgentRunRequest
	draftCheckErrors            int
	validateErrors              int
	applySlideCount             int
	invalidPlan                 bool
	planStatus                  string
	noOpCheck                   bool
	mutatePlanDuringCheck       bool
	mutatePlanDuringApply       bool
	mutatePlanDuringValidate    bool
	mutateReportDuringValidate  bool
	injectCandidateManifest     bool
	mutateContentDuringCheck    bool
	failPhase                   string
	onPhase                     func(string) error
	afterPhase                  func(string)
	checkSawPlan                bool
	checkReportPresentAfterPlan bool
}

func (a *templateFillWorkflowAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a *templateFillWorkflowAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	a.requests = append(a.requests, req)
	if err := os.MkdirAll(a.sessionRoot, 0o755); err != nil {
		return nil, err
	}
	sessionDir, err := os.MkdirTemp(a.sessionRoot, "session-")
	if err != nil {
		return nil, err
	}
	sessionWorkspace := filepath.Join(sessionDir, "workspace")
	if err := copyDir(ctx, req.WorkDir, sessionWorkspace); err != nil {
		return nil, err
	}
	projectRel, err := filepath.Rel(req.WorkDir, a.projectPath)
	if err != nil {
		return nil, err
	}
	if projectRel == ".." || strings.HasPrefix(projectRel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("template fill project %q is outside work dir %q", a.projectPath, req.WorkDir)
	}
	sessionProjectPath := filepath.Join(sessionWorkspace, projectRel)
	if req.Phase == string(PhaseTemplateFillCheck) && strings.HasPrefix(strings.TrimSpace(req.Command), "rm -f ") {
		_ = os.Remove(filepath.Join(sessionProjectPath, "analysis", "check_report.json"))
		_ = os.Remove(filepath.Join(sessionProjectPath, ".slidesmith", "contracts", "template_fill_check.json"))
	}
	if a.onPhase != nil {
		if err := a.onPhase(req.Phase); err != nil {
			return nil, err
		}
	}
	if a.failPhase == req.Phase {
		exitCode := 1
		return &AgentRunResult{
			RunID:         "run-" + req.Phase,
			SessionID:     filepath.Base(sessionDir),
			Status:        "failed",
			ExitCode:      &exitCode,
			WorkspacePath: sessionWorkspace,
			StderrTail:    "injected runtime failure",
		}, fmt.Errorf("injected %s failure", req.Phase)
	}
	switch req.Phase {
	case string(PhaseTemplateFillPlan):
		if _, err := os.Stat(filepath.Join(sessionProjectPath, "analysis", "check_report.json")); err == nil {
			a.checkReportPresentAfterPlan = true
		}
		planStatus := a.planStatus
		if planStatus == "" {
			planStatus = "draft"
		}
		plan := templateFillContractPlan(planStatus, 1)
		if a.invalidPlan {
			plan["schema"] = "template_fill_pptx_plan.invalid"
		}
		writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "fill_plan.json"), plan)
	case string(PhaseTemplateFillCheck):
		if _, err := os.Stat(filepath.Join(sessionProjectPath, "analysis", "fill_plan.json")); err == nil {
			a.checkSawPlan = true
		}
		if a.injectCandidateManifest {
			mustWriteFileNoTest(sessionProjectPath, filepath.Join(".slidesmith", "source_inputs.json"), `{
  "schema": "slidesmith.source_inputs.v1",
  "files": [
    {"name": "brand.pptx", "upload_path": "uploads/task-template-fill/brand.pptx"},
    {"name": "brand.md", "upload_path": "uploads/task-template-fill/brand.md"}
  ]
}`+"\n")
		}
		if a.mutateContentDuringCheck {
			mustWriteFileNoTest(sessionProjectPath, filepath.Join("sources", "brand.md"), "# Candidate-mutated content\n")
		}
		if !a.noOpCheck {
			writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "check_report.json"), map[string]any{
				"schema": "template_fill_pptx_check.v1",
				"summary": map[string]any{
					"ok":    1,
					"warn":  0,
					"error": a.draftCheckErrors,
				},
				"results": []any{},
			})
		}
		if a.mutatePlanDuringCheck {
			plan := templateFillContractPlan("confirmed", 1)
			templateFillContractFirstSlide(plan)["purpose"] = "mutated-during-check"
			writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "fill_plan.json"), plan)
		}
	case string(PhaseTemplateFillApply):
		slideCount := a.applySlideCount
		if slideCount == 0 {
			slideCount = 1
		}
		mustWritePPTXNoTest(sessionProjectPath, filepath.Join("exports", "result.pptx"), slideCount)
		if a.mutatePlanDuringApply {
			plan := templateFillContractPlan("confirmed", 1)
			templateFillContractFirstSlide(plan)["purpose"] = "mutated-during-apply"
			writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "fill_plan.json"), plan)
		}
	case string(PhaseTemplateFillValidate):
		mustWriteFileNoTest(sessionProjectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")
		writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("validation", "validate_report.json"), map[string]any{
			"schema": "template_fill_pptx_validate.v1",
			"summary": map[string]any{
				"ok":    1,
				"warn":  0,
				"error": a.validateErrors,
			},
			"results": []any{},
		})
		if a.mutatePlanDuringValidate {
			plan := templateFillContractPlan("confirmed", 1)
			templateFillContractFirstSlide(plan)["purpose"] = "mutated-during-validate"
			writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "fill_plan.json"), plan)
		}
		if a.mutateReportDuringValidate {
			writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "check_report.json"), map[string]any{
				"schema": "template_fill_pptx_check.v1",
				"summary": map[string]any{
					"ok":    2,
					"warn":  0,
					"error": 0,
				},
				"results": []any{},
			})
		}
	default:
		return nil, fmt.Errorf("unexpected template fill phase %q", req.Phase)
	}
	if a.afterPhase != nil {
		a.afterPhase(req.Phase)
	}
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-" + req.Phase,
		SessionID:     filepath.Base(sessionDir),
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: sessionWorkspace,
	}, nil
}

func TestTemplateFillPlanPromptUsesWorkspaceRelativePathsAndHardRules(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, nil)
	inputs, err := discoverTemplateFillInputs(projectPath)
	if err != nil {
		t.Fatal(err)
	}

	prompt := service.templateFillPlanPrompt(task, inputs)
	projectRel := service.projectRel(task, projectPath)
	required := []string{
		"You are building analysis/fill_plan.json for the Template Fill PPTX workflow.",
		filepath.ToSlash(filepath.Join(projectRel, "sources", "brand.pptx")),
		filepath.ToSlash(filepath.Join(projectRel, "analysis", "brand.slide_library.json")),
		filepath.ToSlash(filepath.Join(projectRel, "sources", "content.md")),
		filepath.ToSlash(filepath.Join(projectRel, "analysis", "fill_plan.json")),
		"Do not run pptx_to_svg.py, pptx_template_import.py, finalize_svg.py, or svg_to_pptx.py.",
		"Read the slide library JSON before selecting pages.",
		"Use the target story order, not the source deck order.",
		"Every planned slide must include layout_rationale.layout_pattern, why_fit, and risk.",
		"All factual content must come from the provided content source files.",
		"Write only analysis/fill_plan.json.",
		"Keep top-level status as \"draft\".",
		"Do not create PPTX exports.",
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("template fill prompt missing %q\n%s", want, prompt)
		}
	}
	for _, hostPath := range append([]string{
		inputs.ProjectPath,
		inputs.SourcePPTX,
		inputs.SlideLibrary,
		inputs.FillPlan,
	}, inputs.ContentSources...) {
		if strings.Contains(prompt, hostPath) {
			t.Fatalf("template fill prompt leaked host path %q\n%s", hostPath, prompt)
		}
	}
}

func TestProcessQueuedTasksRunsTemplateFillPlanThenSeparateDraftCheck(t *testing.T) {
	agent := &templateFillWorkflowAgent{draftCheckErrors: 1}
	service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, agent)
	agent.projectPath = projectPath

	processed, err := service.ProcessQueuedTasks(context.Background(), 1)
	if err != nil {
		t.Fatalf("ProcessQueuedTasks() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("status = %q, want %q", updated.Status, model.TaskStatusAwaitingTemplateFillConfirm)
	}
	if len(agent.requests) != 2 {
		t.Fatalf("agent requests = %d, want plan agent + draft check: %#v", len(agent.requests), agent.requests)
	}
	if agent.requests[0].Phase != string(PhaseTemplateFillPlan) || agent.requests[0].Prompt == "" || agent.requests[0].Command != "" {
		t.Fatalf("first request should be the plan agent: %#v", agent.requests[0])
	}
	projectRel := service.projectRel(task, projectPath)
	wantDraftCheck := fmt.Sprintf("python3 scripts/ppt_runner.py template-fill-check --project-path %s", shellArg(projectRel))
	if agent.requests[1].Phase != string(PhaseTemplateFillCheck) || agent.requests[1].Command != wantDraftCheck || agent.requests[1].Prompt != "" {
		t.Fatalf("draft check request = %#v, want command %q", agent.requests[1], wantDraftCheck)
	}
	if agent.checkReportPresentAfterPlan {
		t.Fatal("plan agent must not write analysis/check_report.json")
	}
	if !agent.checkSawPlan {
		t.Fatal("draft check did not observe the plan written by the plan agent")
	}
	for _, relativePath := range []string{
		filepath.Join("analysis", "fill_plan.json"),
		filepath.Join("analysis", "check_report.json"),
		filepath.Join(".slidesmith", "contracts", "template_fill_plan.json"),
		filepath.Join(".slidesmith", "contracts", "template_fill_check.json"),
	} {
		requireFileExists(t, filepath.Join(projectPath, relativePath))
	}

	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 {
		t.Fatalf("phase runs = %#v, want one planning phase run", phaseRuns)
	}
	planPhaseRun := phaseRuns[0]
	if planPhaseRun.Phase != string(PhaseTemplateFillPlan) || planPhaseRun.Runner != PhaseRunnerAgent || planPhaseRun.Status != PhaseRunStatusSucceeded {
		t.Fatalf("plan phase run = %#v", planPhaseRun)
	}
	if planPhaseRun.RuntimeRunID == "" {
		t.Fatalf("plan phase run is not linked to its agent runtime run: %#v", planPhaseRun)
	}
	if strings.Contains(planPhaseRun.OutputJSON, `"blocking_errors"`) {
		t.Fatalf("draft errors are a user gate, not a failed/blocking plan phase: %s", planPhaseRun.OutputJSON)
	}

	runtimeRuns, err := repo.ListRuntimeRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeByPhase := map[string]model.TaskRuntimeRun{}
	for _, runtimeRun := range runtimeRuns {
		runtimeByPhase[runtimeRun.Phase] = runtimeRun
		if sameFilesystemPath(runtimeRun.WorkspacePath, workspacePath) {
			t.Fatalf("runtime phase %s used persistent workspace instead of distinct session: %#v", runtimeRun.Phase, runtimeRun)
		}
	}
	planRuntimeRun, hasPlan := runtimeByPhase[string(PhaseTemplateFillPlan)]
	if !hasPlan {
		t.Fatalf("plan runtime run missing: %#v", runtimeRuns)
	}
	if planPhaseRun.RuntimeRunID != planRuntimeRun.ID {
		t.Fatalf("plan phase runtime_run_id = %q, want plan agent run %q", planPhaseRun.RuntimeRunID, planRuntimeRun.ID)
	}
	if _, hasDraftCheck := runtimeByPhase[string(PhaseTemplateFillCheck)]; !hasDraftCheck {
		t.Fatalf("separate draft check runtime run missing: %#v", runtimeRuns)
	}
}

func TestProcessTemplateFillCheckReturnsDraftAndBlockedConfirmedPlansToGate(t *testing.T) {
	tests := []struct {
		name               string
		planStatus         string
		checkErrors        int
		wantPlanStatus     string
		wantBlockingErrors bool
		wantBlockedEvent   bool
	}{
		{
			name:               "confirmed errors return plan to draft",
			planStatus:         "confirmed",
			checkErrors:        2,
			wantPlanStatus:     "draft",
			wantBlockingErrors: true,
			wantBlockedEvent:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{draftCheckErrors: test.checkErrors}
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
			agent.projectPath = projectPath
			mustWriteTemplateFillPlan(t, projectPath, test.planStatus, 1)

			if err := service.ProcessTask(context.Background(), task.ID); err != nil {
				t.Fatalf("ProcessTask() error = %v", err)
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusAwaitingTemplateFillConfirm {
				t.Fatalf("status = %q, want %q", updated.Status, model.TaskStatusAwaitingTemplateFillConfirm)
			}
			_, _, planStatus, err := readValidatedTemplateFillPlan(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			if planStatus != test.wantPlanStatus {
				t.Fatalf("plan status = %q, want %q", planStatus, test.wantPlanStatus)
			}
			planContract := readTemplateFillContractReport(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_plan.json"))
			if planContract["plan_status"] != test.wantPlanStatus {
				t.Fatalf("plan contract status = %#v, want %q; contract=%#v", planContract["plan_status"], test.wantPlanStatus, planContract)
			}
			if len(agent.requests) != 1 || agent.requests[0].Phase != string(PhaseTemplateFillCheck) {
				t.Fatalf("formal check requests = %#v", agent.requests)
			}
			phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillCheck)
			if phaseRun.Status != PhaseRunStatusSucceeded || phaseRun.Runner != PhaseRunnerWorker {
				t.Fatalf("check phase run = %#v", phaseRun)
			}
			hasBlockingErrors := strings.Contains(phaseRun.OutputJSON, fmt.Sprintf(`"blocking_errors":%d`, test.checkErrors))
			if hasBlockingErrors != test.wantBlockingErrors {
				t.Fatalf("check phase blocking errors = %v, want %v; output=%s", hasBlockingErrors, test.wantBlockingErrors, phaseRun.OutputJSON)
			}
			events, err := repo.ListEvents(context.Background(), task.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			hasBlockedEvent := false
			for _, event := range events {
				if event.Status == "template_fill_check_blocked" {
					hasBlockedEvent = true
				}
			}
			if hasBlockedEvent != test.wantBlockedEvent {
				t.Fatalf("blocked event = %v, want %v; events=%#v", hasBlockedEvent, test.wantBlockedEvent, events)
			}
		})
	}
}

func TestTemplateFillCheckingReconcilesDraftToGateWithoutFormalRun(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	prepareTemplateFillCheckContractReport(t, projectPath)
	mustWriteFile(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"), "{}\n")

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("status = %q, want user gate", updated.Status)
	}
	if len(agent.requests) != 0 {
		t.Fatalf("draft inconsistency invoked formal checker: %#v", agent.requests)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase == string(PhaseTemplateFillCheck) {
			t.Fatalf("draft reconciliation fabricated formal phase run: %#v", phaseRun)
		}
	}
	for _, stalePath := range []string{
		filepath.Join(projectPath, "analysis", "check_report.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"),
	} {
		if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
			t.Fatalf("draft reconciliation retained stale formal evidence %s: %v", stalePath, err)
		}
	}
}

func TestTemplateFillDraftReconciliationReturnsStaleEvidenceCleanupFailure(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	prepareTemplateFillCheckContractReport(t, projectPath)
	staleContractPath := filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json")
	mustWriteFile(t, filepath.Join(staleContractPath, "keep"), "stale\n")

	err := service.ProcessTask(context.Background(), task.ID)
	if err == nil || !strings.Contains(err.Error(), "remove stale formal check evidence") {
		t.Fatalf("ProcessTask() error = %v, want stale-evidence cleanup failure", err)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_check.cleanup" {
		t.Fatalf("cleanup failure task = %#v", updated)
	}
	if len(agent.requests) != 0 {
		t.Fatalf("draft cleanup failure invoked runtime: %#v", agent.requests)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase == string(PhaseTemplateFillCheck) {
			t.Fatalf("draft cleanup failure fabricated formal phase run: %#v", phaseRun)
		}
	}
}

func TestTemplateFillBlockedCheckCannotDowngradeSuccessorCanonicalPlan(t *testing.T) {
	agent := &templateFillWorkflowAgent{draftCheckErrors: 2}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
	takeoverRan := installTemplateFillCanonicalMutationTakeover(t, service, repo, task, func() {
		successorPlan := templateFillContractPlan("confirmed", 1)
		templateFillContractFirstSlide(successorPlan)["purpose"] = "successor-confirmed-plan"
		writeTemplateFillWorkflowJSON(projectPath, filepath.Join("analysis", "fill_plan.json"), successorPlan)
		if _, err := validateTemplateFillPlanContract(projectPath); err != nil {
			t.Fatal(err)
		}
	})

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() stale blocked-check error = %v, want clean stop", err)
	}
	if !takeoverRan() {
		t.Fatal("blocked-check canonical mutation takeover hook did not run")
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusTemplateFillChecking || persisted.ExecutionClaimToken != "successor-canonical-owner" {
		t.Fatalf("stale blocked check changed successor task: %#v", persisted)
	}
	_, slides, status, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	purpose, _ := slides[0].(map[string]any)["purpose"].(string)
	if status != "confirmed" || purpose != "successor-confirmed-plan" {
		t.Fatalf("stale blocked check changed successor plan: status=%q purpose=%q", status, purpose)
	}
}

func TestTemplateFillDraftReconciliationCannotDeleteSuccessorCanonicalEvidence(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	prepareTemplateFillCheckContractReport(t, projectPath)
	contractPath := filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json")
	mustWriteFile(t, contractPath, `{"owner":"old"}`+"\n")
	successorReport := `{"owner":"successor","kind":"report"}` + "\n"
	successorContract := `{"owner":"successor","kind":"contract"}` + "\n"
	takeoverRan := installTemplateFillCanonicalMutationTakeover(t, service, repo, task, func() {
		mustWriteFile(t, filepath.Join(projectPath, "analysis", "check_report.json"), successorReport)
		mustWriteFile(t, contractPath, successorContract)
	})

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() stale draft-reconciliation error = %v, want clean stop", err)
	}
	if !takeoverRan() {
		t.Fatal("draft-reconciliation canonical mutation takeover hook did not run")
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusTemplateFillChecking || persisted.ExecutionClaimToken != "successor-canonical-owner" {
		t.Fatalf("stale draft reconciliation changed successor task: %#v", persisted)
	}
	for path, want := range map[string]string{
		filepath.Join(projectPath, "analysis", "check_report.json"): successorReport,
		contractPath: successorContract,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read successor evidence %s: %v", path, err)
		}
		if string(raw) != want {
			t.Fatalf("successor evidence %s = %q, want %q", path, raw, want)
		}
	}
}

func installTemplateFillCanonicalMutationTakeover(
	t *testing.T,
	service *TaskService,
	repo *repository.Repository,
	task *model.Task,
	writeSuccessorCanonical func(),
) func() bool {
	t.Helper()
	called := false
	service.beforeCanonicalMutationPromotion = func(string) error {
		called = true
		now := time.Now().UTC()
		staleClaimedAt := now.Add(-service.taskExecutionLeaseDuration() - time.Minute)
		if err := repo.DB().Model(&model.Task{}).Where("id = ?", task.ID).Update("execution_claimed_at", staleClaimedAt).Error; err != nil {
			return err
		}
		claimed, err := repo.ClaimTaskExecution(
			context.Background(),
			task.ID,
			model.TaskStatusTemplateFillChecking,
			"successor-canonical-owner",
			now,
			now.Add(-service.taskExecutionLeaseDuration()),
		)
		if err != nil {
			return err
		}
		if !claimed {
			return fmt.Errorf("successor could not take over canonical mutation")
		}
		writeSuccessorCanonical()
		return nil
	}
	return func() bool { return called }
}

func TestTemplateFillFormalCheckRejectsStaleReportAndPlanMutation(t *testing.T) {
	tests := []struct {
		name             string
		configure        func(*templateFillWorkflowAgent)
		wantFailurePhase string
	}{
		{
			name: "no-op checker cannot reuse draft report",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.noOpCheck = true
			},
			wantFailurePhase: "template_fill_check.fresh_report",
		},
		{
			name: "plan changed during checker run",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.mutatePlanDuringCheck = true
			},
			wantFailurePhase: "template_fill_check.plan_changed",
		},
		{
			name: "checker command failure",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.failPhase = string(PhaseTemplateFillCheck)
			},
			wantFailurePhase: "template_fill_check.command",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			test.configure(agent)
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
			agent.projectPath = projectPath
			mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
			prepareTemplateFillCheckContractReport(t, projectPath)
			writeTemplateFillFormalCheckEvidence(t, projectPath)
			canonicalPlanBefore, err := os.ReadFile(filepath.Join(projectPath, "analysis", "fill_plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			canonicalReportBefore, err := os.ReadFile(filepath.Join(projectPath, "analysis", "check_report.json"))
			if err != nil {
				t.Fatal(err)
			}
			canonicalContractBefore, err := os.ReadFile(filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"))
			if err != nil {
				t.Fatal(err)
			}

			if err := service.ProcessTask(context.Background(), task.ID); err == nil {
				t.Fatal("ProcessTask() error = nil, want formal freshness failure")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusFailed || updated.FailurePhase != test.wantFailurePhase {
				t.Fatalf("formal check failure task = %#v", updated)
			}
			phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillCheck)
			if phaseRun.Status != PhaseRunStatusFailed {
				t.Fatalf("formal freshness phase run = %#v", phaseRun)
			}
			canonicalPlanAfter, err := os.ReadFile(filepath.Join(projectPath, "analysis", "fill_plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(canonicalPlanAfter) != string(canonicalPlanBefore) {
				t.Fatalf("rejected formal session overwrote canonical plan\nbefore=%s\nafter=%s", canonicalPlanBefore, canonicalPlanAfter)
			}
			canonicalReportAfter, err := os.ReadFile(filepath.Join(projectPath, "analysis", "check_report.json"))
			if err != nil {
				t.Fatal(err)
			}
			canonicalContractAfter, err := os.ReadFile(filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(canonicalReportAfter) != string(canonicalReportBefore) || string(canonicalContractAfter) != string(canonicalContractBefore) {
				t.Fatal("rejected formal session changed canonical formal evidence")
			}
		})
	}
}

func TestProcessQueuedTasksRunsConfirmedTemplateFillCheckApplyValidateAndPublish(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "source_profile.json"), "{}\n")
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.identity.json"), "{}\n")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "source_prepare.json"), "{}\n")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "template_fill_plan.json"), "{}\n")

	wantStatuses := []string{
		model.TaskStatusTemplateFillApplying,
		model.TaskStatusTemplateFillValidating,
		model.TaskStatusPublishing,
		model.TaskStatusCompleted,
	}
	for _, wantStatus := range wantStatuses {
		processed, err := service.ProcessQueuedTasks(context.Background(), 1)
		if err != nil {
			t.Fatalf("ProcessQueuedTasks() error = %v", err)
		}
		if processed != 1 {
			t.Fatalf("processed = %d, want 1", processed)
		}
		updated, err := repo.GetTask(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Status != wantStatus {
			t.Fatalf("status = %q, want %q", updated.Status, wantStatus)
		}
	}

	projectRel := service.projectRel(task, projectPath)
	wantCommands := map[string]string{
		string(PhaseTemplateFillCheck):    templateFillFormalCheckCommand(projectRel),
		string(PhaseTemplateFillApply):    fmt.Sprintf("python3 scripts/ppt_runner.py template-fill-apply --project-path %s --transition fade", shellArg(projectRel)),
		string(PhaseTemplateFillValidate): fmt.Sprintf("python3 scripts/ppt_runner.py template-fill-validate --project-path %s", shellArg(projectRel)),
	}
	if len(agent.requests) != len(wantCommands) {
		t.Fatalf("requests = %#v", agent.requests)
	}
	for _, request := range agent.requests {
		if request.Command != wantCommands[request.Phase] {
			t.Fatalf("command for %s = %q, want %q", request.Phase, request.Command, wantCommands[request.Phase])
		}
		for _, forbidden := range []string{"pptx_to_svg.py", "pptx_template_import.py", "finalize_svg.py", "svg_to_pptx.py", "--force"} {
			if strings.Contains(request.Command, forbidden) {
				t.Fatalf("template fill command contains forbidden %q: %s", forbidden, request.Command)
			}
		}
	}
	for _, phase := range []PipelinePhase{PhaseTemplateFillCheck, PhaseTemplateFillApply, PhaseTemplateFillValidate} {
		phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, phase)
		if phaseRun.Status != PhaseRunStatusSucceeded || phaseRun.Runner != PhaseRunnerWorker || phaseRun.RuntimeRunID == "" {
			t.Fatalf("phase %s run = %#v", phase, phaseRun)
		}
		if sameFilesystemPath(phaseRun.WorkspacePath, workspacePath) {
			t.Fatalf("phase %s did not run in a distinct session workspace: %#v", phase, phaseRun)
		}
		requireFileExists(t, filepath.Join(projectPath, ".slidesmith", "contracts", string(phase)+".json"))
	}
	checkContract := readTemplateFillContractReport(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"))
	if checkContract["plan_status"] != "confirmed" {
		t.Fatalf("formal check plan status = %#v", checkContract["plan_status"])
	}
	planSHA, ok := checkContract["plan_sha256"].(string)
	if !ok || len(planSHA) != 64 {
		t.Fatalf("formal check plan_sha256 = %#v", checkContract["plan_sha256"])
	}
	checkPhaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillCheck)
	if !strings.Contains(checkPhaseRun.OutputJSON, `"plan_status":"confirmed"`) || !strings.Contains(checkPhaseRun.OutputJSON, `"plan_sha256":"`+planSHA+`"`) {
		t.Fatalf("formal check phase evidence missing plan digest/status: %s", checkPhaseRun.OutputJSON)
	}
	publishPhaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhasePublish)
	if publishPhaseRun.Status != PhaseRunStatusSucceeded || publishPhaseRun.Runner != PhaseRunnerPublisher {
		t.Fatalf("publish phase run = %#v", publishPhaseRun)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusCompleted || updated.CompletedAt == nil {
		t.Fatalf("completed template fill task = %#v", updated)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		for _, forbidden := range []PipelinePhase{PhaseSpecGenerate, PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport} {
			if phaseRun.Phase == string(forbidden) {
				t.Fatalf("template fill publish entered main-route phase %s: %#v", forbidden, phaseRuns)
			}
		}
	}
	for _, forbiddenPath := range []string{"design_spec.md", "spec_lock.md", "svg_output", "svg_final"} {
		if _, err := os.Stat(filepath.Join(projectPath, forbiddenPath)); !os.IsNotExist(err) {
			t.Fatalf("template fill publish created main-route path %s, err=%v", forbiddenPath, err)
		}
	}
	artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{
		model.ArtifactKindTemplateFillPlan:           false,
		model.ArtifactKindTemplateFillCheckReport:    false,
		model.ArtifactKindTemplateFillValidateReport: false,
		model.ArtifactKindTemplateFillReadback:       false,
		model.ArtifactKindPPTX:                       false,
		model.ArtifactKindSource:                     false,
		model.ArtifactKindSourceProfile:              false,
		model.ArtifactKindPPTXIdentity:               false,
		model.ArtifactKindPPTXSlideLibrary:           false,
	}
	wantObjectSuffixes := map[string]bool{
		"/contracts/source_prepare.json":         false,
		"/contracts/template_fill_plan.json":     false,
		"/contracts/template_fill_check.json":    false,
		"/contracts/template_fill_apply.json":    false,
		"/contracts/template_fill_validate.json": false,
		"/contracts/publish.json":                false,
		"/contracts/final.json":                  false,
	}
	for _, artifact := range artifacts {
		if _, ok := wantKinds[artifact.Kind]; ok {
			wantKinds[artifact.Kind] = true
		}
		objectKey := filepath.ToSlash(artifact.ObjectKey)
		for suffix := range wantObjectSuffixes {
			if strings.HasSuffix(objectKey, suffix) {
				wantObjectSuffixes[suffix] = true
			}
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("completed template fill artifacts missing kind %q: %#v", kind, artifacts)
		}
	}
	for suffix, found := range wantObjectSuffixes {
		if !found {
			t.Fatalf("completed template fill artifacts missing %s: %#v", suffix, artifacts)
		}
	}
}

func TestTemplateFillPublishRejectsMissingKindBeforePersistence(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	if err := os.Remove(filepath.Join(projectPath, "validation", "readback.md")); err != nil {
		t.Fatal(err)
	}
	storage := service.storage.(*LocalStorage)
	tracking := &publishCleanupTrackingStorage{StorageService: storage}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)

	err := service.ProcessTask(context.Background(), task.ID)
	if err == nil {
		t.Fatal("ProcessTask() error = nil, want missing readback rejection")
	}
	if !strings.Contains(err.Error(), model.ArtifactKindTemplateFillReadback) {
		t.Fatalf("ProcessTask() error = %q, want missing readback kind", err)
	}
	updated, getErr := repo.GetTask(context.Background(), task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "publish" {
		t.Fatalf("failed template fill publish = %#v", updated)
	}
	var publishedCount int64
	if err := repo.DB().Model(&model.Artifact{}).Where("task_id = ? AND publish_version <> ''", task.ID).Count(&publishedCount).Error; err != nil {
		t.Fatal(err)
	}
	if publishedCount != 0 {
		t.Fatalf("pre-persistence rejection stored %d artifacts, want 0", publishedCount)
	}
	if len(tracking.copiedObjectKeys) == 0 {
		t.Fatal("missing-artifact contract failure did not reach object copy")
	}
	for _, objectKey := range tracking.copiedObjectKeys {
		if _, statErr := os.Stat(storage.Path(objectKey)); !os.IsNotExist(statErr) {
			t.Fatalf("pre-persistence contract failure leaked object %s, err=%v", objectKey, statErr)
		}
	}
}

func TestTemplateFillPublishRollsBackPreDBContractFailures(t *testing.T) {
	tests := []struct {
		name             string
		configureProject func(*testing.T, string)
		failCopyContains string
		wantError        string
	}{
		{
			name: "publish contract write",
			configureProject: func(t *testing.T, projectPath string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(projectPath, ".slidesmith", "contracts", "publish.json"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "publish.json",
		},
		{
			name: "final contract write",
			configureProject: func(t *testing.T, projectPath string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(projectPath, ".slidesmith", "contracts", "final.json"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "final.json",
		},
		{
			name:             "publish contract copy write then error",
			failCopyContains: "/contracts/publish.json",
			wantError:        "injected publish copy write-then-error",
		},
		{
			name:             "final contract copy write then error",
			failCopyContains: "/contracts/final.json",
			wantError:        "injected publish copy write-then-error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
			prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
			if test.configureProject != nil {
				test.configureProject(t, projectPath)
			}
			storage := service.storage.(*LocalStorage)
			previous := seedPreviousTemplateFillPublishForRollbackTest(t, repo, storage, projectPath, task.ID)
			tracking := &publishCleanupTrackingStorage{
				StorageService:     storage,
				failCopyContains:   test.failCopyContains,
				failCopyAfterWrite: test.failCopyContains != "",
			}
			service.storage = tracking
			service.publisher = NewRuntimeWorkspacePublisher(tracking)

			err := service.ProcessTask(context.Background(), task.ID)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ProcessTask() error = %v, want %q", err, test.wantError)
			}
			assertFailedPublishAttemptCleaned(t, repo, storage, task.ID, tracking, previous)
		})
	}
}

func TestTemplateFillPublishRollsBackExactAttemptOnDBReplaceFailure(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	storage := service.storage.(*LocalStorage)
	previous := seedPreviousTemplateFillPublishForRollbackTest(t, repo, storage, projectPath, task.ID)
	tracking := &publishCleanupTrackingStorage{StorageService: storage}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)
	if err := repo.DB().Exec(`
		CREATE TRIGGER fail_new_publish_artifact_insert
		BEFORE INSERT ON artifacts
		WHEN NEW.publish_version <> 'v20260712T120000Z'
		BEGIN
			SELECT RAISE(ABORT, 'injected publish DB replace failure');
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}

	err := service.ProcessTask(context.Background(), task.ID)
	if err == nil || !strings.Contains(err.Error(), "injected publish DB replace failure") {
		t.Fatalf("ProcessTask() error = %v, want DB replace failure", err)
	}
	assertFailedPublishAttemptCleaned(t, repo, storage, task.ID, tracking, previous)
}

func TestTemplateFillPublishRollsBackExactAttemptOnDBListFailure(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	storage := service.storage.(*LocalStorage)
	previous := seedPreviousTemplateFillPublishForRollbackTest(t, repo, storage, projectPath, task.ID)
	tracking := &publishCleanupTrackingStorage{StorageService: storage}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)
	injected := false
	callbackName := "inject_publish_artifact_list_failure"
	if err := repo.DB().Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if !injected && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "artifacts" {
			injected = true
			tx.AddError(errors.New("injected persisted publish list failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}

	err := service.ProcessTask(context.Background(), task.ID)
	if removeErr := repo.DB().Callback().Query().Remove(callbackName); removeErr != nil {
		t.Fatal(removeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "injected persisted publish list failure") {
		t.Fatalf("ProcessTask() error = %v, want DB list failure", err)
	}
	if !injected {
		t.Fatal("artifact list fault was not injected")
	}
	assertFailedPublishAttemptCleaned(t, repo, storage, task.ID, tracking, previous)
}

func TestTemplateFillPublishCleanupIgnoresCancelledRequestContext(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	storage := service.storage.(*LocalStorage)
	previous := seedPreviousTemplateFillPublishForRollbackTest(t, repo, storage, projectPath, task.ID)
	ctx, cancel := context.WithCancel(context.Background())
	tracking := &publishCleanupTrackingStorage{
		StorageService:          storage,
		cancel:                  cancel,
		cancelAfterCopyContains: "/validation/validate_report.json",
	}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)

	err := service.ProcessTask(ctx, task.ID)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessTask() error = %v, want context cancellation", err)
	}
	assertFailedPublishAttemptCleaned(t, repo, storage, task.ID, tracking, previous)
	for _, cancelled := range tracking.deleteContextCancelled {
		if cancelled {
			t.Fatal("publish cleanup reused cancelled request context")
		}
	}
}

func TestTemplateFillPublishDoesNotFallBackFromCanonicalProject(t *testing.T) {
	tests := []struct {
		name               string
		breakCanonical     func(*testing.T, string)
		configureFallbacks func(*testing.T, *TaskService, *repository.Repository, *model.Task)
	}{
		{
			name: "missing canonical does not use runtime workspace",
			breakCanonical: func(t *testing.T, projectPath string) {
				t.Helper()
				if err := os.RemoveAll(projectPath); err != nil {
					t.Fatal(err)
				}
			},
			configureFallbacks: func(t *testing.T, service *TaskService, repo *repository.Repository, task *model.Task) {
				t.Helper()
				staleWorkspace := filepath.Join(t.TempDir(), "stale-runtime")
				staleProject := filepath.Join(staleWorkspace, "projects", task.RuntimeProject+"_ppt169_20260712")
				prepareTemplateFillPublishedProjectForTest(t, staleProject, 2)
				task.RuntimeWorkspacePath = staleWorkspace
				task.LastRuntimeSessionID = "stale-runtime-session"
				if err := repo.SaveTask(context.Background(), task); err != nil {
					t.Fatal(err)
				}
				service.agentCfg.SessionDataRoot = ""
			},
		},
		{
			name: "rejected canonical does not use recovery session",
			breakCanonical: func(t *testing.T, projectPath string) {
				t.Helper()
				if err := os.Remove(filepath.Join(projectPath, "validation", "readback.md")); err != nil {
					t.Fatal(err)
				}
			},
			configureFallbacks: func(t *testing.T, service *TaskService, repo *repository.Repository, task *model.Task) {
				t.Helper()
				recoveryRoot := t.TempDir()
				recoveryProject := filepath.Join(
					recoveryRoot,
					"sessions",
					"stale-recovery-session",
					"workspace",
					"projects",
					task.RuntimeProject+"_ppt169_20260712",
				)
				prepareTemplateFillPublishedProjectForTest(t, recoveryProject, 2)
				mustWriteFileNoTest(recoveryProject, filepath.Join(".slidesmith", "artifacts.json"), `{"project_path":".","artifacts":[]}`+"\n")
				task.RuntimeWorkspacePath = ""
				task.LastRuntimeSessionID = "canonical-session"
				if err := repo.SaveTask(context.Background(), task); err != nil {
					t.Fatal(err)
				}
				service.agentCfg.SessionDataRoot = recoveryRoot
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
			prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
			test.breakCanonical(t, projectPath)
			test.configureFallbacks(t, service, repo, task)

			err := service.ProcessTask(context.Background(), task.ID)
			if err == nil {
				t.Fatal("ProcessTask() error = nil, want canonical-only Template Fill publish failure")
			}
			updated, getErr := repo.GetTask(context.Background(), task.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "publish" {
				t.Fatalf("failed canonical-only Template Fill publish = %#v", updated)
			}
			if updated.LastRuntimeSessionID != task.LastRuntimeSessionID {
				t.Fatalf("runtime session changed from %q to fallback %q", task.LastRuntimeSessionID, updated.LastRuntimeSessionID)
			}
			var publishedCount int64
			if err := repo.DB().Model(&model.Artifact{}).Where("task_id = ? AND publish_version <> ''", task.ID).Count(&publishedCount).Error; err != nil {
				t.Fatal(err)
			}
			if publishedCount != 0 {
				t.Fatalf("canonical-only rejection stored %d published artifacts, want 0", publishedCount)
			}
		})
	}
}

func TestTemplateFillPublishRejectsInvalidBindingsAfterPersistenceAndCleansFailedVersion(t *testing.T) {
	tests := []struct {
		name             string
		trigger          string
		wantError        string
		preservePrevious bool
	}{
		{
			name: "wrong canonical kind",
			trigger: `
				CREATE TRIGGER corrupt_template_fill_readback_kind
				AFTER INSERT ON artifacts
				WHEN NEW.kind = 'template_fill_readback'
				BEGIN
					UPDATE artifacts SET kind = 'other' WHERE id = NEW.id;
				END;
			`,
			wantError:        "identity does not match inserted artifact",
			preservePrevious: true,
		},
		{
			name: "swapped canonical kinds",
			trigger: `
				CREATE TRIGGER swap_template_fill_validation_kinds
				AFTER INSERT ON artifacts
				WHEN NEW.kind = 'template_fill_readback'
				BEGIN
					UPDATE artifacts SET kind = 'template_fill_readback'
					WHERE task_id = NEW.task_id AND publish_version = NEW.publish_version
					  AND kind = 'template_fill_validate_report';
					UPDATE artifacts SET kind = 'template_fill_validate_report' WHERE id = NEW.id;
				END;
			`,
			wantError: "identity does not match inserted artifact",
		},
		{
			name: "duplicate canonical path",
			trigger: `
				CREATE TRIGGER duplicate_template_fill_plan
				AFTER INSERT ON artifacts
				WHEN NEW.object_key LIKE '%/contracts/final.json'
				BEGIN
					INSERT INTO artifacts (
						id, task_id, kind, name, storage, object_key, mime_type, size,
						sha256, publish_version, metadata_json, created_at, updated_at
					)
					SELECT id || '-duplicate', task_id, kind, name, storage, object_key, mime_type, size,
						sha256, publish_version, metadata_json, created_at, updated_at
					FROM artifacts
					WHERE task_id = NEW.task_id AND publish_version = NEW.publish_version
					  AND object_key LIKE '%/analysis/fill_plan.json';
					DELETE FROM artifacts
					WHERE task_id = NEW.task_id AND publish_version = NEW.publish_version
					  AND object_key LIKE '%/analysis/check_report.json';
				END;
			`,
			wantError: "persisted artifact count",
		},
		{
			name: "case variant canonical path",
			trigger: `
				CREATE TRIGGER case_variant_template_fill_plan
				AFTER INSERT ON artifacts
				WHEN NEW.kind = 'template_fill_plan'
				BEGIN
					UPDATE artifacts
					SET object_key = replace(object_key, '/analysis/fill_plan.json', '/analysis/Fill_Plan.json')
					WHERE id = NEW.id;
				END;
			`,
			wantError: "identity does not match inserted artifact",
		},
		{
			name: "near match canonical path",
			trigger: `
				CREATE TRIGGER near_match_template_fill_readback
				AFTER INSERT ON artifacts
				WHEN NEW.kind = 'template_fill_readback'
				BEGIN
					UPDATE artifacts SET object_key = object_key || '.bak' WHERE id = NEW.id;
				END;
			`,
			wantError: "identity does not match inserted artifact",
		},
		{
			name: "case variant pptx path",
			trigger: `
				CREATE TRIGGER case_variant_template_fill_pptx
				AFTER INSERT ON artifacts
				WHEN NEW.kind = 'pptx'
				BEGIN
					UPDATE artifacts
					SET object_key = substr(object_key, 1, length(object_key) - 5) || '.PPTX'
					WHERE id = NEW.id;
				END;
			`,
			wantError: "identity does not match inserted artifact",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
			prepareTemplateFillPublishedProjectForTest(t, projectPath, 3)
			storage, ok := service.storage.(*LocalStorage)
			if !ok {
				t.Fatalf("storage = %T, want *LocalStorage", service.storage)
			}

			var previous []model.Artifact
			const previousVersion = "v20260712T120000Z"
			if test.preservePrevious {
				previous = copyTemplateFillPublishedArtifactsForTaskTest(t, storage, projectPath, task.ID, previousVersion)
				if _, err := buildPublishedArtifactsContract(projectPath, storage, previous, previousVersion, model.TaskRouteTemplateFill); err != nil {
					t.Fatalf("previous version is not valid: %v", err)
				}
				previousPrefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", previousVersion)) + "/"
				if err := repo.ReplaceArtifactsByObjectKeyPrefix(context.Background(), task.ID, previousPrefix, previous); err != nil {
					t.Fatal(err)
				}
			}

			tracking := &publishCleanupTrackingStorage{StorageService: storage}
			service.storage = tracking
			service.publisher = NewRuntimeWorkspacePublisher(tracking)
			if err := repo.DB().Exec(test.trigger).Error; err != nil {
				t.Fatal(err)
			}

			err := service.ProcessTask(context.Background(), task.ID)
			if err == nil {
				t.Fatal("ProcessTask() error = nil, want post-persistence binding rejection")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ProcessTask() error = %q, want transactional identity rejection %q", err, test.wantError)
			}
			updated, getErr := repo.GetTask(context.Background(), task.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "publish" {
				t.Fatalf("failed template fill persisted recheck = %#v", updated)
			}

			failedVersion := singlePublishVersionForTest(t, tracking.copiedObjectKeys)
			failedRows, listErr := repo.ListArtifactsByPublishVersion(context.Background(), task.ID, failedVersion)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(failedRows) != 0 {
				t.Fatalf("failed publish version %s retained rows: %#v", failedVersion, failedRows)
			}
			for _, objectKey := range tracking.copiedObjectKeys {
				if _, statErr := os.Stat(storage.Path(objectKey)); !os.IsNotExist(statErr) {
					t.Fatalf("failed publish object %s remains, err=%v", objectKey, statErr)
				}
			}

			if test.preservePrevious {
				persistedPrevious, listErr := repo.ListArtifactsByPublishVersion(context.Background(), task.ID, previousVersion)
				if listErr != nil {
					t.Fatal(listErr)
				}
				if len(persistedPrevious) != len(previous) {
					t.Fatalf("previous version rows = %d, want %d", len(persistedPrevious), len(previous))
				}
				for _, artifact := range previous {
					if _, statErr := os.Stat(storage.Path(artifact.ObjectKey)); statErr != nil {
						t.Fatalf("previous version object %s was removed: %v", artifact.ObjectKey, statErr)
					}
				}
				latest, listErr := repo.ListArtifacts(context.Background(), task.ID)
				if listErr != nil {
					t.Fatal(listErr)
				}
				if len(latest) != len(previous) {
					t.Fatalf("latest visible artifacts = %#v, want preserved previous version", latest)
				}
			}
		})
	}
}

func TestTemplateFillPublishSurfacesObjectCleanupFailureAndContinuesCompensation(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	storage := service.storage.(*LocalStorage)
	tracking := &publishCleanupTrackingStorage{
		StorageService:     storage,
		failDeleteContains: "/analysis/fill_plan.json",
	}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)
	if err := repo.DB().Exec(`
		CREATE TRIGGER corrupt_template_fill_readback_kind_for_cleanup
		AFTER INSERT ON artifacts
		WHEN NEW.kind = 'template_fill_readback'
		BEGIN
			UPDATE artifacts SET kind = 'other' WHERE id = NEW.id;
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}

	err := service.ProcessTask(context.Background(), task.ID)
	if err == nil || !strings.Contains(err.Error(), "injected publish object cleanup failure") {
		t.Fatalf("ProcessTask() error = %v, want surfaced object cleanup failure", err)
	}
	failedVersion := singlePublishVersionForTest(t, tracking.copiedObjectKeys)
	failedRows, listErr := repo.ListArtifactsByPublishVersion(context.Background(), task.ID, failedVersion)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(failedRows) != 0 {
		t.Fatalf("failed publish rows remain after object cleanup error: %#v", failedRows)
	}
	if len(tracking.deletedObjectKeys) != len(tracking.copiedObjectKeys) {
		t.Fatalf("object cleanup attempts = %d, want %d: %#v", len(tracking.deletedObjectKeys), len(tracking.copiedObjectKeys), tracking.deletedObjectKeys)
	}
	for _, objectKey := range tracking.copiedObjectKeys {
		_, statErr := os.Stat(storage.Path(objectKey))
		if strings.Contains(objectKey, tracking.failDeleteContains) {
			if statErr != nil {
				t.Fatalf("injected failed-delete object %s unexpectedly missing: %v", objectKey, statErr)
			}
			continue
		}
		if !os.IsNotExist(statErr) {
			t.Fatalf("cleanup skipped object %s after another delete failed, err=%v", objectKey, statErr)
		}
	}
}

func TestTemplateFillPublishCleanupDeletesInsertedRowsWhoseVersionWasMutated(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	storage := service.storage.(*LocalStorage)

	const previousVersion = "v20260712T120000Z"
	previous := copyTemplateFillPublishedArtifactsForTaskTest(t, storage, projectPath, task.ID, previousVersion)
	if _, err := buildPublishedArtifactsContract(projectPath, storage, previous, previousVersion, model.TaskRouteTemplateFill); err != nil {
		t.Fatalf("previous version is not valid: %v", err)
	}
	previousPrefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", previousVersion)) + "/"
	if err := repo.ReplaceArtifactsByObjectKeyPrefix(context.Background(), task.ID, previousPrefix, previous); err != nil {
		t.Fatal(err)
	}
	previousRows, err := repo.ListArtifactsByPublishVersion(context.Background(), task.ID, previousVersion)
	if err != nil {
		t.Fatal(err)
	}
	previousVisible, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	previousBytes := make(map[string][]byte, len(previousRows))
	for _, artifact := range previousRows {
		raw, readErr := os.ReadFile(storage.Path(artifact.ObjectKey))
		if readErr != nil {
			t.Fatal(readErr)
		}
		previousBytes[artifact.ObjectKey] = raw
	}

	tracking := &publishCleanupTrackingStorage{StorageService: storage}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)
	if err := repo.DB().Exec(`
		CREATE TRIGGER move_new_readback_to_previous_publish_version
		AFTER INSERT ON artifacts
		WHEN NEW.kind = 'template_fill_readback'
		BEGIN
			UPDATE artifacts
			SET publish_version = 'v20260712T120000Z'
			WHERE id = NEW.id;
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}

	err = service.ProcessTask(context.Background(), task.ID)
	if err == nil || !strings.Contains(err.Error(), "identity does not match inserted artifact") {
		t.Fatalf("ProcessTask() error = %v, want post-insert version mutation rejection", err)
	}
	failedVersion := singlePublishVersionForTest(t, tracking.copiedObjectKeys)
	failedPrefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", failedVersion)) + "/"
	var failedPrefixRows int64
	if err := repo.DB().Model(&model.Artifact{}).
		Where("task_id = ? AND object_key LIKE ?", task.ID, failedPrefix+"%").
		Count(&failedPrefixRows).Error; err != nil {
		t.Fatal(err)
	}
	if failedPrefixRows != 0 {
		t.Fatalf("failed publish prefix retained %d rows, want 0", failedPrefixRows)
	}
	for _, objectKey := range tracking.copiedObjectKeys {
		if _, statErr := os.Stat(storage.Path(objectKey)); !os.IsNotExist(statErr) {
			t.Fatalf("failed publish object %s remains, err=%v", objectKey, statErr)
		}
	}

	previousRowsAfter, err := repo.ListArtifactsByPublishVersion(context.Background(), task.ID, previousVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previousRowsAfter, previousRows) {
		t.Fatalf("previous version rows changed:\n before=%#v\n  after=%#v", previousRows, previousRowsAfter)
	}
	previousVisibleAfter, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previousVisibleAfter, previousVisible) {
		t.Fatalf("previous version visibility changed:\n before=%#v\n  after=%#v", previousVisible, previousVisibleAfter)
	}
	for objectKey, wantBytes := range previousBytes {
		gotBytes, readErr := os.ReadFile(storage.Path(objectKey))
		if readErr != nil {
			t.Fatalf("read previous object %s: %v", objectKey, readErr)
		}
		if !reflect.DeepEqual(gotBytes, wantBytes) {
			t.Fatalf("previous object %s bytes changed", objectKey)
		}
	}
}

func TestTemplateFillPublishCleanupJoinsExactRowAndObjectErrors(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusPublishing, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	storage := service.storage.(*LocalStorage)

	const previousVersion = "v20260712T120000Z"
	previous := copyTemplateFillPublishedArtifactsForTaskTest(t, storage, projectPath, task.ID, previousVersion)
	previousPrefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", previousVersion)) + "/"
	if err := repo.ReplaceArtifactsByObjectKeyPrefix(context.Background(), task.ID, previousPrefix, previous); err != nil {
		t.Fatal(err)
	}
	tracking := &publishCleanupTrackingStorage{
		StorageService:     storage,
		failDeleteContains: "/analysis/fill_plan.json",
	}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)
	if err := repo.DB().Exec(`
		CREATE TRIGGER fail_exact_inserted_row_cleanup
		BEFORE DELETE ON artifacts
		WHEN OLD.publish_version <> 'v20260712T120000Z'
		  AND OLD.object_key LIKE '%/validation/readback.md'
		BEGIN
			SELECT RAISE(ABORT, 'injected exact inserted row cleanup failure');
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}
	injectedListFailure := false
	callbackName := "inject_joined_publish_cleanup_list_failure"
	if err := repo.DB().Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if injectedListFailure || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "artifacts" {
			return
		}
		if _, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter); inTransaction {
			return
		}
		injectedListFailure = true
		tx.AddError(errors.New("injected persisted publish list failure"))
	}); err != nil {
		t.Fatal(err)
	}

	err := service.ProcessTask(context.Background(), task.ID)
	if removeErr := repo.DB().Callback().Query().Remove(callbackName); removeErr != nil {
		t.Fatal(removeErr)
	}
	for _, want := range []string{
		"injected persisted publish list failure",
		"injected exact inserted row cleanup failure",
		"injected publish object cleanup failure",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("ProcessTask() error = %v, want joined error %q", err, want)
		}
	}
	if !injectedListFailure {
		t.Fatal("persisted publish list failure was not injected")
	}
	if len(tracking.deletedObjectKeys) != len(tracking.copiedObjectKeys) {
		t.Fatalf("object cleanup attempts = %d, want %d after row cleanup failure", len(tracking.deletedObjectKeys), len(tracking.copiedObjectKeys))
	}
	for _, artifact := range previous {
		var count int64
		if err := repo.DB().Model(&model.Artifact{}).
			Where("task_id = ? AND id = ?", task.ID, artifact.ID).
			Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("legitimate previous row %s count = %d, want 1", artifact.ID, count)
		}
	}
}

func TestTemplateFillPhaseFailuresPersistConcreteMetadataAndFailedRuns(t *testing.T) {
	tests := []struct {
		name             string
		status           string
		phase            PipelinePhase
		wantFailurePhase string
		configure        func(*templateFillWorkflowAgent)
	}{
		{
			name:             "planning contract",
			status:           model.TaskStatusTemplateFillPlanning,
			phase:            PhaseTemplateFillPlan,
			wantFailurePhase: "template_fill_plan.contract",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.invalidPlan = true
			},
		},
		{
			name:             "checking command",
			status:           model.TaskStatusTemplateFillChecking,
			phase:            PhaseTemplateFillCheck,
			wantFailurePhase: "template_fill_check.command",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.failPhase = string(PhaseTemplateFillCheck)
			},
		},
		{
			name:             "applying contract",
			status:           model.TaskStatusTemplateFillApplying,
			phase:            PhaseTemplateFillApply,
			wantFailurePhase: "template_fill_apply.contract",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.applySlideCount = 2
			},
		},
		{
			name:             "validating contract",
			status:           model.TaskStatusTemplateFillValidating,
			phase:            PhaseTemplateFillValidate,
			wantFailurePhase: "template_fill_validate.contract",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.validateErrors = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			test.configure(agent)
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, test.status, agent)
			agent.projectPath = projectPath
			prepareTemplateFillWorkflowPhase(t, projectPath, test.status)

			if err := service.ProcessTask(context.Background(), task.ID); err == nil {
				t.Fatal("ProcessTask() error = nil, want phase failure")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusFailed {
				t.Fatalf("status = %q, want failed", updated.Status)
			}
			if updated.FailurePhase != test.wantFailurePhase {
				t.Fatalf("failure phase = %q, want %q", updated.FailurePhase, test.wantFailurePhase)
			}
			var metadata map[string]any
			if err := json.Unmarshal([]byte(updated.FailureMetadata), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata["phase"] != test.wantFailurePhase || metadata["error_message"] == "" {
				t.Fatalf("failure metadata = %#v", metadata)
			}
			phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, test.phase)
			if phaseRun.Status != PhaseRunStatusFailed || phaseRun.ErrorMessage == "" {
				t.Fatalf("failed phase run = %#v", phaseRun)
			}
		})
	}
}

func TestTemplateFillApplyDoesNotOverwriteCancelledTask(t *testing.T) {
	agent := &templateFillWorkflowAgent{failPhase: string(PhaseTemplateFillApply)}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, agent)
	agent.projectPath = projectPath
	prepareTemplateFillWorkflowPhase(t, projectPath, model.TaskStatusTemplateFillApplying)
	agent.onPhase = func(phase string) error {
		if phase != string(PhaseTemplateFillApply) {
			return nil
		}
		_, err := service.CancelTask(context.Background(), task.ID)
		return err
	}

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() cancellation loss error = %v", err)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusCancelled {
		t.Fatalf("status = %q, want cancelled", updated.Status)
	}
	phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillApply)
	if phaseRun.Status != PhaseRunStatusFailed {
		t.Fatalf("cancelled apply phase run = %#v", phaseRun)
	}
}

func TestTemplateFillApplyRejectsCheckErrorsBeforeRuntimeCommand(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
	writeTemplateFillWorkflowJSON(projectPath, filepath.Join("analysis", "check_report.json"), map[string]any{
		"schema": "template_fill_pptx_check.v1",
		"summary": map[string]any{
			"ok":    0,
			"warn":  0,
			"error": 1,
		},
		"results": []any{},
	})
	writeTemplateFillFormalCheckEvidence(t, projectPath)

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want apply preflight failure")
	}
	if len(agent.requests) != 0 {
		t.Fatalf("apply runtime must not run with check errors: %#v", agent.requests)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_apply.check_contract" {
		t.Fatalf("task after apply preflight = %#v", updated)
	}
	phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillApply)
	if phaseRun.Status != PhaseRunStatusFailed {
		t.Fatalf("apply preflight phase run = %#v", phaseRun)
	}
}

func TestTemplateFillApplyRejectsDraftPlanBeforeRuntimeOrExportMutation(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	prepareTemplateFillCheckContractReport(t, projectPath)
	mustWriteFile(t, filepath.Join(projectPath, "exports", "sentinel.txt"), "preserve\n")

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want draft apply preflight failure")
	}
	if len(agent.requests) != 0 {
		t.Fatalf("draft plan invoked mutating apply runtime: %#v", agent.requests)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_apply.plan_contract" {
		t.Fatalf("draft apply task = %#v", updated)
	}
	phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillApply)
	if phaseRun.Status != PhaseRunStatusFailed {
		t.Fatalf("draft apply phase run = %#v", phaseRun)
	}
	raw, err := os.ReadFile(filepath.Join(projectPath, "exports", "sentinel.txt"))
	if err != nil || string(raw) != "preserve\n" {
		t.Fatalf("apply preflight mutated sentinel: %q, error=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(projectPath, "exports", "result.pptx")); !os.IsNotExist(err) {
		t.Fatalf("draft apply created result.pptx: %v", err)
	}
}

func TestTemplateFillApplyRejectsSessionPlanMutationBeforeCanonicalPromotion(t *testing.T) {
	agent := &templateFillWorkflowAgent{mutatePlanDuringApply: true}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, agent)
	agent.projectPath = projectPath
	prepareTemplateFillWorkflowPhase(t, projectPath, model.TaskStatusTemplateFillApplying)
	canonicalPlanBefore, err := os.ReadFile(filepath.Join(projectPath, "analysis", "fill_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(projectPath, "exports", "sentinel.txt"), "preserve\n")

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want session plan mutation failure")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_apply.plan_changed" {
		t.Fatalf("apply session mutation task = %#v", updated)
	}
	canonicalPlanAfter, err := os.ReadFile(filepath.Join(projectPath, "analysis", "fill_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalPlanAfter) != string(canonicalPlanBefore) {
		t.Fatalf("rejected apply session overwrote canonical plan\nbefore=%s\nafter=%s", canonicalPlanBefore, canonicalPlanAfter)
	}
	if _, err := os.Stat(filepath.Join(projectPath, "exports", "result.pptx")); !os.IsNotExist(err) {
		t.Fatalf("rejected apply session promoted result.pptx: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(projectPath, "exports", "sentinel.txt"))
	if err != nil || string(raw) != "preserve\n" {
		t.Fatalf("rejected apply session changed canonical sentinel: %q, error=%v", raw, err)
	}
}

func TestTemplateFillPlanRejectsInvalidSessionBeforeCanonicalPromotion(t *testing.T) {
	agent := &templateFillWorkflowAgent{invalidPlan: true}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, agent)
	agent.projectPath = projectPath

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want invalid plan failure")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_plan.contract" {
		t.Fatalf("invalid plan task = %#v", updated)
	}
	if _, err := os.Stat(filepath.Join(projectPath, "analysis", "fill_plan.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid plan session changed canonical plan: %v", err)
	}
}

func TestTemplateFillDraftCheckRejectsSessionPlanMutationBeforeCanonicalPromotion(t *testing.T) {
	agent := &templateFillWorkflowAgent{mutatePlanDuringCheck: true}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, agent)
	agent.projectPath = projectPath

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want draft-check plan mutation failure")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_plan.draft_check.plan_changed" {
		t.Fatalf("draft-check mutation task = %#v", updated)
	}
	_, slides, status, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	purpose, _ := slides[0].(map[string]any)["purpose"].(string)
	if status != "draft" || purpose != "slide-1" {
		t.Fatalf("rejected draft-check session changed canonical plan: status=%q purpose=%q", status, purpose)
	}
	if _, err := os.Stat(filepath.Join(projectPath, "analysis", "check_report.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected draft-check session promoted report: %v", err)
	}
}

func TestTemplateFillValidateRejectsFormalEvidenceMutationBeforeCanonicalPromotion(t *testing.T) {
	tests := []struct {
		name             string
		configure        func(*templateFillWorkflowAgent)
		wantFailurePhase string
	}{
		{
			name: "plan changed",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.mutatePlanDuringValidate = true
			},
			wantFailurePhase: "template_fill_validate.plan_changed",
		},
		{
			name: "formal report changed",
			configure: func(agent *templateFillWorkflowAgent) {
				agent.mutateReportDuringValidate = true
			},
			wantFailurePhase: "template_fill_validate.check_contract",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			test.configure(agent)
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillValidating, agent)
			agent.projectPath = projectPath
			prepareTemplateFillWorkflowPhase(t, projectPath, model.TaskStatusTemplateFillValidating)
			canonicalPlanBefore, err := os.ReadFile(filepath.Join(projectPath, "analysis", "fill_plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			canonicalReportBefore, err := os.ReadFile(filepath.Join(projectPath, "analysis", "check_report.json"))
			if err != nil {
				t.Fatal(err)
			}

			if err := service.ProcessTask(context.Background(), task.ID); err == nil {
				t.Fatal("ProcessTask() error = nil, want validate evidence mutation failure")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusFailed || updated.FailurePhase != test.wantFailurePhase {
				t.Fatalf("validate evidence mutation task = %#v", updated)
			}
			canonicalPlanAfter, err := os.ReadFile(filepath.Join(projectPath, "analysis", "fill_plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			canonicalReportAfter, err := os.ReadFile(filepath.Join(projectPath, "analysis", "check_report.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(canonicalPlanAfter) != string(canonicalPlanBefore) || string(canonicalReportAfter) != string(canonicalReportBefore) {
				t.Fatal("rejected validate session changed canonical formal evidence")
			}
			for _, relativePath := range []string{filepath.Join("validation", "readback.md"), filepath.Join("validation", "validate_report.json")} {
				if _, err := os.Stat(filepath.Join(projectPath, relativePath)); !os.IsNotExist(err) {
					t.Fatalf("rejected validate session promoted %s: %v", relativePath, err)
				}
			}
		})
	}
}

func TestTemplateFillWorkerPromotionRevalidatesProvenanceUnderLock(t *testing.T) {
	tests := []struct {
		name   string
		status string
	}{
		{name: "plan", status: model.TaskStatusTemplateFillPlanning},
		{name: "check", status: model.TaskStatusTemplateFillChecking},
		{name: "apply", status: model.TaskStatusTemplateFillApplying},
		{name: "validate", status: model.TaskStatusTemplateFillValidating},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, test.status, agent)
			agent.projectPath = projectPath
			prepareTemplateFillWorkflowPhase(t, projectPath, test.status)

			staged := make(chan struct{})
			releasePromotionAttempt := make(chan struct{})
			var blockFirstPromotion sync.Once
			service.beforeTemplateFillPromotionLock = func() {
				blockFirstPromotion.Do(func() {
					close(staged)
					<-releasePromotionAttempt
				})
			}
			done := make(chan error, 1)
			go func() { done <- service.ProcessTask(context.Background(), task.ID) }()
			select {
			case <-staged:
			case err := <-done:
				t.Fatalf("worker completed before staged promotion: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for staged Template Fill worker promotion")
			}

			lockPath := filepath.Join(workspacePath, ".slidesmith", "project-promotions.lock")
			unlockPromotion, err := acquireProjectPromotionLock(context.Background(), lockPath)
			if err != nil {
				t.Fatal(err)
			}
			close(releasePromotionAttempt)
			mutatedPath := filepath.Join(projectPath, "sources", "content.md")
			mutatedBytes := []byte("# Canonical content mutated while worker promotion waited\n")
			if err := os.WriteFile(mutatedPath, mutatedBytes, 0o644); err != nil {
				unlockPromotion()
				t.Fatal(err)
			}
			unlockPromotion()

			if err := <-done; err == nil || !strings.Contains(strings.ToLower(err.Error()), "provenance") {
				t.Fatalf("ProcessTask() error = %v, want under-lock provenance rejection", err)
			}
			if got, err := os.ReadFile(mutatedPath); err != nil || !reflect.DeepEqual(got, mutatedBytes) {
				t.Fatalf("stale worker candidate overwrote canonical mutation: got=%q error=%v", got, err)
			}
			persisted, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != model.TaskStatusFailed {
				t.Fatalf("status = %q, want failed", persisted.Status)
			}
			assertTemplateFillPromotionRaceCleaned(t, workspacePath)
		})
	}
}

func TestTemplateFillPlanDraftCheckPromotionRevalidatesProvenanceUnderLock(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, agent)
	agent.projectPath = projectPath

	stagedSecondPromotion := make(chan struct{})
	releaseSecondPromotion := make(chan struct{})
	promotionAttempt := 0
	service.beforeTemplateFillPromotionLock = func() {
		promotionAttempt++
		if promotionAttempt == 2 {
			close(stagedSecondPromotion)
			<-releaseSecondPromotion
		}
	}
	done := make(chan error, 1)
	go func() { done <- service.ProcessTask(context.Background(), task.ID) }()
	select {
	case <-stagedSecondPromotion:
	case err := <-done:
		t.Fatalf("planning worker completed before second staged promotion: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for staged draft-check promotion")
	}

	lockPath := filepath.Join(workspacePath, ".slidesmith", "project-promotions.lock")
	unlockPromotion, err := acquireProjectPromotionLock(context.Background(), lockPath)
	if err != nil {
		t.Fatal(err)
	}
	close(releaseSecondPromotion)
	mutatedPath := filepath.Join(projectPath, "sources", "content.md")
	mutatedBytes := []byte("# Canonical content mutated during draft-check promotion\n")
	if err := os.WriteFile(mutatedPath, mutatedBytes, 0o644); err != nil {
		unlockPromotion()
		t.Fatal(err)
	}
	unlockPromotion()

	if err := <-done; err == nil || !strings.Contains(strings.ToLower(err.Error()), "provenance") {
		t.Fatalf("ProcessTask() error = %v, want second-promotion provenance rejection", err)
	}
	if got, err := os.ReadFile(mutatedPath); err != nil || !reflect.DeepEqual(got, mutatedBytes) {
		t.Fatalf("stale draft-check candidate overwrote canonical mutation: got=%q error=%v", got, err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "template_fill_plan.draft_check.contract" {
		t.Fatalf("task after draft-check promotion race = %#v", persisted)
	}
	if promotionAttempt != 2 || len(agent.requests) != 2 {
		t.Fatalf("promotion attempts = %d, agent requests = %d; want direct second promotion coverage", promotionAttempt, len(agent.requests))
	}
	assertTemplateFillPromotionRaceCleaned(t, workspacePath)
}

func TestTemplateFillCanonicalMutationPromotionsRevalidateProvenanceUnderLock(t *testing.T) {
	tests := []struct {
		name              string
		configure         func(*testing.T, *templateFillWorkflowAgent, string)
		targetPromotion   int
		wantFailurePhase  string
		wantAgentRequests int
	}{
		{
			name: "draft reconciliation cleanup",
			configure: func(t *testing.T, _ *templateFillWorkflowAgent, projectPath string) {
				mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
				prepareTemplateFillCheckContractReport(t, projectPath)
				mustWriteFile(t, filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"), "{}\n")
			},
			targetPromotion:   1,
			wantFailurePhase:  "template_fill_check.cleanup",
			wantAgentRequests: 0,
		},
		{
			name: "blocked check plan reset",
			configure: func(t *testing.T, agent *templateFillWorkflowAgent, projectPath string) {
				agent.draftCheckErrors = 2
				mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
			},
			targetPromotion:   2,
			wantFailurePhase:  "template_fill_check.reset_plan_contract",
			wantAgentRequests: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
			agent.projectPath = projectPath
			test.configure(t, agent, projectPath)

			stagedTargetPromotion := make(chan struct{})
			releaseTargetPromotion := make(chan struct{})
			promotionAttempt := 0
			service.beforeTemplateFillPromotionLock = func() {
				promotionAttempt++
				if promotionAttempt == test.targetPromotion {
					close(stagedTargetPromotion)
					<-releaseTargetPromotion
				}
			}
			done := make(chan error, 1)
			go func() { done <- service.ProcessTask(context.Background(), task.ID) }()
			select {
			case <-stagedTargetPromotion:
			case err := <-done:
				t.Fatalf("worker completed before canonical mutation promotion %d: %v", test.targetPromotion, err)
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for canonical mutation promotion %d", test.targetPromotion)
			}

			lockPath := filepath.Join(workspacePath, ".slidesmith", "project-promotions.lock")
			unlockPromotion, err := acquireProjectPromotionLock(context.Background(), lockPath)
			if err != nil {
				t.Fatal(err)
			}
			close(releaseTargetPromotion)
			mutatedPath := filepath.Join(projectPath, "sources", "content.md")
			mutatedBytes := []byte("# Canonical content mutated during canonical mutation promotion\n")
			if err := os.WriteFile(mutatedPath, mutatedBytes, 0o644); err != nil {
				unlockPromotion()
				t.Fatal(err)
			}
			unlockPromotion()

			if err := <-done; err == nil || !strings.Contains(strings.ToLower(err.Error()), "provenance") {
				t.Fatalf("ProcessTask() error = %v, want canonical mutation provenance rejection", err)
			}
			if got, err := os.ReadFile(mutatedPath); err != nil || !reflect.DeepEqual(got, mutatedBytes) {
				t.Fatalf("stale canonical mutation overwrote authoritative source: got=%q error=%v", got, err)
			}
			persisted, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != test.wantFailurePhase {
				t.Fatalf("task after canonical mutation race = %#v", persisted)
			}
			if promotionAttempt != test.targetPromotion || len(agent.requests) != test.wantAgentRequests {
				t.Fatalf("promotion attempts = %d, agent requests = %d; want %d/%d", promotionAttempt, len(agent.requests), test.targetPromotion, test.wantAgentRequests)
			}
			assertTemplateFillPromotionRaceCleaned(t, workspacePath)
		})
	}
}

func TestTemplateFillPlanInputFailureRecordsPhaseRunAndPresentationMetadata(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, agent)
	agent.projectPath = projectPath
	mustWriteFile(t, filepath.Join(projectPath, "sources", "second.pptx"), "pptx")

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want multiple presentation failure")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_plan.inputs" {
		t.Fatalf("task after input failure = %#v", updated)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(updated.FailureMetadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["pptx_count"] != float64(2) {
		t.Fatalf("pptx_count = %#v, want 2; metadata=%#v", metadata["pptx_count"], metadata)
	}
	sourceFiles, ok := metadata["source_files"].([]any)
	if !ok || len(sourceFiles) != 2 || sourceFiles[0] != "sources/brand.pptx" || sourceFiles[1] != "sources/second.pptx" {
		t.Fatalf("source_files = %#v, want deterministic PPTX list; metadata=%#v", metadata["source_files"], metadata)
	}
	phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillPlan)
	if phaseRun.Status != PhaseRunStatusFailed || phaseRun.Runner != PhaseRunnerAgent {
		t.Fatalf("input failure phase run = %#v", phaseRun)
	}
	if len(agent.requests) != 0 {
		t.Fatalf("plan agent should not run for invalid inputs: %#v", agent.requests)
	}
}

func TestTemplateFillPlanningRejectsAgentPlanThatSkipsDraftGate(t *testing.T) {
	agent := &templateFillWorkflowAgent{planStatus: "confirmed"}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillPlanning, agent)
	agent.projectPath = projectPath

	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("ProcessTask() error = nil, want confirmed plan rejection")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != "template_fill_plan.contract" {
		t.Fatalf("task after confirmed agent plan = %#v", updated)
	}
	if len(agent.requests) != 1 || agent.requests[0].Phase != string(PhaseTemplateFillPlan) {
		t.Fatalf("draft check must not run for confirmed agent plan: %#v", agent.requests)
	}
}

func TestTemplateFillWorkerInputFailuresRecordTheirPhaseRuns(t *testing.T) {
	tests := []struct {
		status string
		phase  PipelinePhase
	}{
		{status: model.TaskStatusTemplateFillChecking, phase: PhaseTemplateFillCheck},
		{status: model.TaskStatusTemplateFillApplying, phase: PhaseTemplateFillApply},
		{status: model.TaskStatusTemplateFillValidating, phase: PhaseTemplateFillValidate},
	}
	for _, test := range tests {
		t.Run(string(test.phase), func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, test.status, agent)
			agent.projectPath = projectPath
			prepareTemplateFillWorkflowPhase(t, projectPath, test.status)
			if err := os.Remove(filepath.Join(projectPath, "sources", "content.md")); err != nil {
				t.Fatal(err)
			}

			if err := service.ProcessTask(context.Background(), task.ID); err == nil {
				t.Fatal("ProcessTask() error = nil, want damaged input failure")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantFailurePhase := string(test.phase) + ".inputs"
			if updated.Status != model.TaskStatusFailed || updated.FailurePhase != wantFailurePhase {
				t.Fatalf("task after %s input failure = %#v", test.phase, updated)
			}
			phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, test.phase)
			if phaseRun.Status != PhaseRunStatusFailed {
				t.Fatalf("%s input phase run = %#v", test.phase, phaseRun)
			}
			if len(agent.requests) != 0 {
				t.Fatalf("runtime must not run with damaged inputs: %#v", agent.requests)
			}
		})
	}
}

func TestTemplateFillPreflightFailuresAlwaysFinishPhaseRuns(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		phase     PipelinePhase
		disabled  bool
		removeDir bool
		wantPhase string
	}{
		{name: "plan agent disabled", status: model.TaskStatusTemplateFillPlanning, phase: PhaseTemplateFillPlan, disabled: true, wantPhase: "template_fill_plan.agent_disabled"},
		{name: "check agent disabled", status: model.TaskStatusTemplateFillChecking, phase: PhaseTemplateFillCheck, disabled: true, wantPhase: "template_fill_check.agent_disabled"},
		{name: "apply agent disabled", status: model.TaskStatusTemplateFillApplying, phase: PhaseTemplateFillApply, disabled: true, wantPhase: "template_fill_apply.agent_disabled"},
		{name: "validate agent disabled", status: model.TaskStatusTemplateFillValidating, phase: PhaseTemplateFillValidate, disabled: true, wantPhase: "template_fill_validate.agent_disabled"},
		{name: "plan project missing", status: model.TaskStatusTemplateFillPlanning, phase: PhaseTemplateFillPlan, removeDir: true, wantPhase: "template_fill_plan.project"},
		{name: "check project missing", status: model.TaskStatusTemplateFillChecking, phase: PhaseTemplateFillCheck, removeDir: true, wantPhase: "template_fill_check.project"},
		{name: "apply project missing", status: model.TaskStatusTemplateFillApplying, phase: PhaseTemplateFillApply, removeDir: true, wantPhase: "template_fill_apply.project"},
		{name: "validate project missing", status: model.TaskStatusTemplateFillValidating, phase: PhaseTemplateFillValidate, removeDir: true, wantPhase: "template_fill_validate.project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &templateFillWorkflowAgent{}
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, test.status, agent)
			agent.projectPath = projectPath
			prepareTemplateFillWorkflowPhase(t, projectPath, test.status)
			if test.disabled {
				service.agentCfg.Enabled = false
			}
			if test.removeDir {
				if err := os.RemoveAll(projectPath); err != nil {
					t.Fatal(err)
				}
			}

			if err := service.ProcessTask(context.Background(), task.ID); err == nil {
				t.Fatal("ProcessTask() error = nil, want preflight failure")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusFailed || updated.FailurePhase != test.wantPhase {
				t.Fatalf("preflight task = %#v", updated)
			}
			phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, test.phase)
			if phaseRun.Status != PhaseRunStatusFailed || phaseRun.FinishedAt == nil {
				t.Fatalf("preflight phase run = %#v", phaseRun)
			}
			if len(agent.requests) != 0 {
				t.Fatalf("preflight failure invoked runtime: %#v", agent.requests)
			}
		})
	}
}

func newTemplateFillWorkflowService(t *testing.T, status string, agent AgentComposeClient) (*TaskService, *repository.Repository, *model.Task, string, string) {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "template-fill.sqlite")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Task{},
		&model.TaskEvent{},
		&model.Artifact{},
		&model.TaskRuntimeRun{},
		&model.TaskPhaseRun{},
		&model.TaskConfirmation{},
	); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	workspaceRoot := filepath.Join(tmp, "workspaces")
	runtimeProject := "task_template_fill"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260713")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "brand.pptx"), "pptx")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "content.md"), "# Content\n")
	mustWriteFile(t, filepath.Join(projectPath, "analysis", "brand.slide_library.json"), `{
  "slides": [
    {"slide_index": 1},
    {"slide_index": 2}
  ]
}`+"\n")
	task := &model.Task{
		ID:                   "task-template-fill",
		Title:                "套用公司模板填充新内容",
		Status:               status,
		Route:                model.TaskRouteTemplateFill,
		RuntimeProject:       runtimeProject,
		RuntimeWorkspacePath: workspacePath,
	}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if agent == nil {
		agent = &templateFillWorkflowAgent{projectPath: projectPath}
	}
	if workflowAgent, ok := agent.(*templateFillWorkflowAgent); ok {
		workflowAgent.sessionRoot = filepath.Join(tmp, "agent-sessions")
	}
	service := NewTaskService(
		repo,
		storage,
		agent,
		NewRuntimeWorkspacePublisher(storage),
		config.AgentComposeConfig{
			Enabled:       true,
			WorkspaceRoot: workspaceRoot,
			Agent:         "ppt_master",
		},
	)
	return service, repo, task, projectPath, workspacePath
}

type publishCleanupTrackingStorage struct {
	StorageService
	attemptedObjectKeys     []string
	copiedObjectKeys        []string
	deletedObjectKeys       []string
	deleteContextCancelled  []bool
	failCopyContains        string
	failCopyAfterWrite      bool
	failDeleteContains      string
	cancel                  context.CancelFunc
	cancelAfterCopyContains string
}

func (s *publishCleanupTrackingStorage) CopyFileToObject(ctx context.Context, objectKey, sourcePath string) (*StoredObject, error) {
	s.attemptedObjectKeys = append(s.attemptedObjectKeys, objectKey)
	if s.failCopyContains != "" && strings.Contains(objectKey, s.failCopyContains) && !s.failCopyAfterWrite {
		return nil, fmt.Errorf("injected publish copy failure for %s", objectKey)
	}
	stored, err := s.StorageService.CopyFileToObject(ctx, objectKey, sourcePath)
	if err == nil {
		s.copiedObjectKeys = append(s.copiedObjectKeys, stored.ObjectKey)
	}
	if err == nil && s.cancel != nil && strings.Contains(objectKey, s.cancelAfterCopyContains) {
		s.cancel()
	}
	if err == nil && s.failCopyContains != "" && strings.Contains(objectKey, s.failCopyContains) && s.failCopyAfterWrite {
		return nil, fmt.Errorf("injected publish copy write-then-error for %s", objectKey)
	}
	return stored, err
}

func (s *publishCleanupTrackingStorage) DeleteObject(ctx context.Context, objectKey string) error {
	s.deletedObjectKeys = append(s.deletedObjectKeys, objectKey)
	s.deleteContextCancelled = append(s.deleteContextCancelled, ctx.Err() != nil)
	if s.failDeleteContains != "" && strings.Contains(objectKey, s.failDeleteContains) {
		return fmt.Errorf("injected publish object cleanup failure for %s", objectKey)
	}
	return s.StorageService.DeleteObject(ctx, objectKey)
}

type previousTemplateFillPublishSnapshot struct {
	rows    []model.Artifact
	objects map[string][]byte
}

func seedPreviousTemplateFillPublishForRollbackTest(
	t *testing.T,
	repo *repository.Repository,
	storage *LocalStorage,
	projectPath, taskID string,
) previousTemplateFillPublishSnapshot {
	t.Helper()
	const previousVersion = "v20260712T120000Z"
	previous := copyTemplateFillPublishedArtifactsForTaskTest(t, storage, projectPath, taskID, previousVersion)
	prefix := filepath.ToSlash(filepath.Join("tasks", taskID, "artifacts", previousVersion)) + "/"
	if err := repo.ReplaceArtifactsByObjectKeyPrefix(context.Background(), taskID, prefix, previous); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListArtifactsByPublishVersion(context.Background(), taskID, previousVersion)
	if err != nil {
		t.Fatal(err)
	}
	objects := make(map[string][]byte, len(rows))
	for _, artifact := range rows {
		raw, err := os.ReadFile(storage.Path(artifact.ObjectKey))
		if err != nil {
			t.Fatal(err)
		}
		objects[artifact.ObjectKey] = raw
	}
	return previousTemplateFillPublishSnapshot{rows: rows, objects: objects}
}

func assertFailedPublishAttemptCleaned(
	t *testing.T,
	repo *repository.Repository,
	storage *LocalStorage,
	taskID string,
	tracking *publishCleanupTrackingStorage,
	previous previousTemplateFillPublishSnapshot,
) {
	t.Helper()
	if len(tracking.attemptedObjectKeys) == 0 {
		t.Fatal("publish failure did not attempt any object keys")
	}
	for _, objectKey := range tracking.attemptedObjectKeys {
		if _, err := os.Stat(storage.Path(objectKey)); !os.IsNotExist(err) {
			t.Fatalf("failed publish object %s remains, err=%v", objectKey, err)
		}
	}
	previousIDs := make([]string, 0, len(previous.rows))
	for _, artifact := range previous.rows {
		previousIDs = append(previousIDs, artifact.ID)
	}
	var newRows int64
	query := repo.DB().Model(&model.Artifact{}).Where("task_id = ?", taskID)
	if len(previousIDs) > 0 {
		query = query.Where("id NOT IN ?", previousIDs)
	}
	if err := query.Count(&newRows).Error; err != nil {
		t.Fatal(err)
	}
	if newRows != 0 {
		t.Fatalf("failed publish retained %d new rows", newRows)
	}
	rowsAfter, err := repo.ListArtifactsByPublishVersion(context.Background(), taskID, "v20260712T120000Z")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rowsAfter, previous.rows) {
		t.Fatalf("previous publish rows changed:\n before=%#v\n  after=%#v", previous.rows, rowsAfter)
	}
	for objectKey, want := range previous.objects {
		got, err := os.ReadFile(storage.Path(objectKey))
		if err != nil {
			t.Fatalf("read previous publish object %s: %v", objectKey, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("previous publish object %s changed", objectKey)
		}
	}
}

func singlePublishVersionForTest(t *testing.T, objectKeys []string) string {
	t.Helper()
	versions := map[string]bool{}
	for _, objectKey := range objectKeys {
		parts := strings.Split(filepath.ToSlash(objectKey), "/")
		if len(parts) < 5 || parts[0] != "tasks" || parts[2] != "artifacts" {
			t.Fatalf("unexpected published object key %q", objectKey)
		}
		versions[parts[3]] = true
	}
	if len(versions) != 1 {
		t.Fatalf("published object versions = %#v, want exactly one", versions)
	}
	for version := range versions {
		return version
	}
	return ""
}

func writeTemplateFillWorkflowJSON(root, relativePath string, value any) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	mustWriteFileNoTest(root, relativePath, string(raw)+"\n")
}

func prepareTemplateFillWorkflowPhase(t *testing.T, projectPath, status string) {
	t.Helper()
	switch status {
	case model.TaskStatusTemplateFillPlanning:
		return
	case model.TaskStatusTemplateFillChecking:
		mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
	case model.TaskStatusTemplateFillApplying:
		mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
		prepareTemplateFillCheckContractReport(t, projectPath)
		writeTemplateFillFormalCheckEvidence(t, projectPath)
	case model.TaskStatusTemplateFillValidating:
		mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)
		prepareTemplateFillCheckContractReport(t, projectPath)
		writeTemplateFillFormalCheckEvidence(t, projectPath)
		mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 1)
	default:
		t.Fatalf("unsupported template fill test status %q", status)
	}
}

func writeTemplateFillFormalCheckEvidence(t *testing.T, projectPath string) {
	t.Helper()
	planSHA256, err := sha256File(filepath.Join(projectPath, "analysis", "fill_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateTemplateFillCheckContractForPlan(projectPath, false, "confirmed", planSHA256); err != nil {
		t.Fatal(err)
	}
}

func requireSingleTemplateFillPhaseRun(t *testing.T, repo *repository.Repository, taskID string, phase PipelinePhase) model.TaskPhaseRun {
	t.Helper()
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var matching []model.TaskPhaseRun
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase == string(phase) {
			matching = append(matching, phaseRun)
		}
	}
	if len(matching) != 1 {
		t.Fatalf("phase %s runs = %#v, want exactly one; all=%#v", phase, matching, phaseRuns)
	}
	return matching[0]
}
