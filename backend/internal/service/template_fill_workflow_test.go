package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	failPhase                   string
	onPhase                     func(string) error
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

func TestProcessQueuedTasksRunsConfirmedTemplateFillCheckApplyAndValidate(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)

	wantStatuses := []string{
		model.TaskStatusTemplateFillApplying,
		model.TaskStatusTemplateFillValidating,
		model.TaskStatusPublishing,
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
