package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

func TestGetTemplateFillPlanReturnsCompletePreview(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 3, 2, 1)

	preview, err := service.GetTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTemplateFillPlan() error = %v", err)
	}
	if preview.TaskID != task.ID || !sameTemplateFillPreviewFile(t, preview.ProjectPath, projectPath) {
		t.Fatalf("preview identity = %#v", preview)
	}
	if preview.Plan["status"] != "draft" || preview.CheckReport["schema"] != "template_fill_pptx_check.v1" {
		t.Fatalf("preview documents = plan %#v report %#v", preview.Plan, preview.CheckReport)
	}
	if preview.PlanFile.Name != "fill_plan.json" || !sameTemplateFillPreviewFile(t, preview.PlanFile.Path, filepath.Join(projectPath, "analysis", "fill_plan.json")) {
		t.Fatalf("plan file = %#v", preview.PlanFile)
	}
	if preview.PlanFile.Content == "" || preview.PlanFile.Size != int64(len(preview.PlanFile.Content)) || preview.PlanFile.UpdatedAt == "" {
		t.Fatalf("plan file metadata = %#v", preview.PlanFile)
	}
	inputJSON, err := json.Marshal(preview.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	var serializedInputs map[string]any
	if err := json.Unmarshal(inputJSON, &serializedInputs); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"project_path", "source_pptx", "slide_library", "fill_plan", "check_report",
		"validate_report", "readback", "export_base", "content_sources",
	} {
		if _, ok := serializedInputs[field]; !ok {
			t.Fatalf("serialized inputs missing %q: %s", field, inputJSON)
		}
	}
	wantSummary := map[string]any{
		"plan_status":          "draft",
		"planned_slide_count":  1,
		"source_pptx_name":     "brand.pptx",
		"content_source_count": 1,
		"check_ok":             3,
		"check_warn":           2,
		"check_error":          1,
	}
	for key, want := range wantSummary {
		if got := preview.Summary[key]; got != want {
			t.Fatalf("summary[%q] = %#v, want %#v; summary=%#v", key, got, want, preview.Summary)
		}
	}
	if !preview.CanEdit || preview.CanConfirm {
		t.Fatalf("permissions = edit %v confirm %v, want true/false", preview.CanEdit, preview.CanConfirm)
	}
}

func TestGetTemplateFillPlanRejectsWrongRouteStatusAndMissingTask(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.Task)
		id     string
	}{
		{name: "wrong route", mutate: func(task *model.Task) { task.Route = model.TaskRouteMain }},
		{name: "disallowed status", mutate: func(task *model.Task) { task.Status = model.TaskStatusUploaded }},
		{name: "missing task", id: "missing-task"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
			mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
			if test.mutate != nil {
				test.mutate(task)
				if err := repo.SaveTask(context.Background(), task); err != nil {
					t.Fatal(err)
				}
			}
			id := test.id
			if id == "" {
				id = task.ID
			}
			if _, err := service.GetTemplateFillPlan(context.Background(), id); err == nil {
				t.Fatal("GetTemplateFillPlan() error = nil")
			} else if test.name == "missing task" && !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("GetTemplateFillPlan() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestSaveTemplateFillPlanForcesDraftClearsStaleCheckAndCanConfirm(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 0, 0, 4)
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "template_fill_check.json"), "stale contract\n")
	plan := templateFillContractPlan("confirmed", 1)
	templateFillContractFirstSlide(plan)["purpose"] = "saved purpose"

	preview, err := service.SaveTemplateFillPlan(context.Background(), task.ID, plan)
	if err != nil {
		t.Fatalf("SaveTemplateFillPlan() error = %v", err)
	}
	if preview.Plan["status"] != "draft" || !preview.CanEdit || !preview.CanConfirm {
		t.Fatalf("saved preview = status %#v edit %v confirm %v", preview.Plan["status"], preview.CanEdit, preview.CanConfirm)
	}
	if plan["status"] != "confirmed" {
		t.Fatalf("SaveTemplateFillPlan mutated caller plan status = %#v", plan["status"])
	}
	for _, path := range []string{
		filepath.Join(projectPath, "analysis", "check_report.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("stale check evidence still exists at %s: %v", path, err)
		}
	}
	_, slides, status, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatalf("readValidatedTemplateFillPlan() error = %v", err)
	}
	if status != "draft" || slides[0].(map[string]any)["purpose"] != "saved purpose" {
		t.Fatalf("saved plan = status %q slides %#v", status, slides)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
		t.Fatalf("save leaked execution claim = token %q claimed_at %v", persisted.ExecutionClaimToken, persisted.ExecutionClaimedAt)
	}
}

func TestSaveTemplateFillPlanPreservesPriorPlanAndEvidenceOnInvalidContract(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 1, 0, 0)
	contractPath := filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "template_fill_check.json"), "stale contract\n")
	planPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	wantPlan := mustReadTemplateFillPreviewFile(t, planPath)

	invalid := templateFillContractPlan("confirmed", 1)
	invalid["schema"] = "template_fill_pptx_plan.invalid"
	if _, err := service.SaveTemplateFillPlan(context.Background(), task.ID, invalid); err == nil {
		t.Fatal("SaveTemplateFillPlan() error = nil")
	}
	if got := mustReadTemplateFillPreviewFile(t, planPath); !bytes.Equal(got, wantPlan) {
		t.Fatalf("prior plan changed after invalid save\ngot: %s\nwant: %s", got, wantPlan)
	}
	for _, path := range []string{filepath.Join(projectPath, "analysis", "check_report.json"), contractPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("prior check evidence %s was not preserved: %v", path, err)
		}
	}
}

func TestSaveTemplateFillPlanPreservesPriorPlanOnFilesystemFailure(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	planPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	wantPlan := mustReadTemplateFillPreviewFile(t, planPath)
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts"), "blocks contract directory\n")

	if _, err := service.SaveTemplateFillPlan(context.Background(), task.ID, templateFillContractPlan("confirmed", 1)); err == nil {
		t.Fatal("SaveTemplateFillPlan() error = nil")
	}
	if got := mustReadTemplateFillPreviewFile(t, planPath); !bytes.Equal(got, wantPlan) {
		t.Fatalf("prior plan changed after filesystem failure\ngot: %s\nwant: %s", got, wantPlan)
	}
}

func TestSaveTemplateFillPlanRestoresPriorPlanWhenPromotionCleanupFails(t *testing.T) {
	service, _, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	planPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	wantPlan := mustReadTemplateFillPreviewFile(t, planPath)
	lockedDir := filepath.Join(projectPath, "locked")
	mustWriteFileNoTest(projectPath, filepath.Join("locked", "keep.txt"), "keep\n")
	if err := os.Chmod(lockedDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(workspacePath, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	updated := templateFillContractPlan("confirmed", 1)
	templateFillContractFirstSlide(updated)["purpose"] = "must not survive failed promotion cleanup"

	if _, err := service.SaveTemplateFillPlan(context.Background(), task.ID, updated); err == nil {
		t.Fatal("SaveTemplateFillPlan() error = nil, want promotion cleanup failure")
	}
	if got := mustReadTemplateFillPreviewFile(t, planPath); !bytes.Equal(got, wantPlan) {
		t.Fatalf("prior plan was not restored after promotion cleanup failure\ngot: %s\nwant: %s", got, wantPlan)
	}
}

func TestSaveTemplateFillPlanAllowsOnlyEditableTask(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		failurePhase string
		wantOK       bool
	}{
		{name: "awaiting gate", status: model.TaskStatusAwaitingTemplateFillConfirm, wantOK: true},
		{name: "failed check", status: model.TaskStatusFailed, failurePhase: "template_fill_check.contract", wantOK: true},
		{name: "failed plan", status: model.TaskStatusFailed, failurePhase: "template_fill_plan.contract"},
		{name: "checking", status: model.TaskStatusTemplateFillChecking},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, test.status, nil)
			task.FailurePhase = test.failurePhase
			if err := repo.SaveTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
			_, err := service.SaveTemplateFillPlan(context.Background(), task.ID, templateFillContractPlan("confirmed", 1))
			if test.wantOK && err != nil {
				t.Fatalf("SaveTemplateFillPlan() error = %v", err)
			}
			if !test.wantOK && err == nil {
				t.Fatal("SaveTemplateFillPlan() error = nil")
			}
		})
	}
}

func TestSaveTemplateFillPlanRecoversStaleAPIExecutionClaim(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	staleClaimedAt := time.Now().UTC().Add(-service.taskExecutionLeaseDuration() - time.Minute)
	task.ExecutionClaimToken = "abandoned-template-fill-api-claim"
	task.ExecutionClaimedAt = &staleClaimedAt
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SaveTemplateFillPlan(context.Background(), task.ID, templateFillContractPlan("confirmed", 1)); err != nil {
		t.Fatalf("SaveTemplateFillPlan() error = %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
		t.Fatalf("stale claim was not recovered and released: token %q claimed_at %v", persisted.ExecutionClaimToken, persisted.ExecutionClaimedAt)
	}
}

func TestCheckTemplateFillPlanRefreshesDraftAtUserGateWithoutFormalPhase(t *testing.T) {
	agent := &templateFillWorkflowAgent{draftCheckErrors: 2}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 9, 9, 9)

	updated, err := service.CheckTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("CheckTemplateFillPlan() error = %v", err)
	}
	if updated.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("status = %q, want awaiting_template_fill_confirm", updated.Status)
	}
	if updated.ExecutionClaimToken != "" || updated.ExecutionClaimedAt != nil {
		t.Fatalf("returned task leaked released claim = token %q claimed_at %v", updated.ExecutionClaimToken, updated.ExecutionClaimedAt)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("persisted status = %q, want awaiting_template_fill_confirm", persisted.Status)
	}
	if persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
		t.Fatalf("check leaked execution claim = token %q claimed_at %v", persisted.ExecutionClaimToken, persisted.ExecutionClaimedAt)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 0 {
		t.Fatalf("check endpoint created formal phase runs: %#v", phaseRuns)
	}
	if len(agent.requests) != 1 || agent.requests[0].Phase != string(PhaseTemplateFillCheck) {
		t.Fatalf("agent requests = %#v, want one draft check", agent.requests)
	}
	for _, req := range agent.requests {
		if req.Phase == string(PhaseTemplateFillApply) {
			t.Fatalf("check endpoint triggered apply: %#v", agent.requests)
		}
	}
	_, _, status, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != "draft" {
		t.Fatalf("plan status = %q, want draft", status)
	}
	preview, err := service.GetTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Summary["check_error"] != 2 || preview.CanConfirm {
		t.Fatalf("refreshed preview = summary %#v can_confirm %v", preview.Summary, preview.CanConfirm)
	}
}

func TestConfirmTemplateFillPlanSetsConfirmedAndQueuesCheck(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)

	updated, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v", err)
	}
	if updated.Status != model.TaskStatusTemplateFillChecking {
		t.Fatalf("status = %q, want template_fill_checking", updated.Status)
	}
	_, _, status, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != "confirmed" {
		t.Fatalf("plan status = %q, want confirmed", status)
	}
}

func TestConfirmTemplateFillPlanRejectsCurrentCheckErrors(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 0, 0, 1)
	wantPlan := mustReadTemplateFillPreviewFile(t, filepath.Join(projectPath, "analysis", "fill_plan.json"))

	if _, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID); err == nil {
		t.Fatal("ConfirmTemplateFillPlan() error = nil")
	}
	if got := mustReadTemplateFillPreviewFile(t, filepath.Join(projectPath, "analysis", "fill_plan.json")); !bytes.Equal(got, wantPlan) {
		t.Fatalf("plan changed despite check errors\ngot: %s\nwant: %s", got, wantPlan)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("persisted status = %q", persisted.Status)
	}
}

func TestConfirmTemplateFillPlanRestoresPlanWhenDBTransitionFails(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	planPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	wantPlan := mustReadTemplateFillPreviewFile(t, planPath)
	injected := errors.New("injected task transition failure")
	installTemplateFillTransitionFailure(t, repo.DB(), model.TaskStatusTemplateFillChecking, injected)

	if _, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID); !errors.Is(err, injected) {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v, want injected failure", err)
	}
	if got := mustReadTemplateFillPreviewFile(t, planPath); !bytes.Equal(got, wantPlan) {
		t.Fatalf("plan was not restored after DB failure\ngot: %s\nwant: %s", got, wantPlan)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("persisted status = %q, want awaiting_template_fill_confirm", persisted.Status)
	}
}

func TestConfirmTemplateFillPlanRestoresPlanWhenDatabaseRemainsUnavailable(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	planPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	wantPlan := mustReadTemplateFillPreviewFile(t, planPath)
	injected := errors.New("database unavailable after canonical exchange")
	installTemplateFillPersistentDatabaseFailure(t, repo.DB(), model.TaskStatusTemplateFillChecking, injected)

	if _, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID); !errors.Is(err, injected) {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v, want database unavailable", err)
	}
	if got := mustReadTemplateFillPreviewFile(t, planPath); !bytes.Equal(got, wantPlan) {
		t.Fatalf("plan was not restored without DB-backed rollback\ngot: %s\nwant: %s", got, wantPlan)
	}
}

func TestFailedTemplateFillCheckTaskCanSaveAndConfirmAgain(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "template_fill_check.contract"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 0, 0, 3)

	preview, err := service.SaveTemplateFillPlan(context.Background(), task.ID, templateFillContractPlan("confirmed", 1))
	if err != nil {
		t.Fatalf("SaveTemplateFillPlan() error = %v", err)
	}
	if !preview.CanEdit || !preview.CanConfirm {
		t.Fatalf("saved failed-check preview = can_edit %v can_confirm %v, want true/true", preview.CanEdit, preview.CanConfirm)
	}
	updated, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v", err)
	}
	if updated.Status != model.TaskStatusTemplateFillChecking {
		t.Fatalf("status = %q, want template_fill_checking", updated.Status)
	}
}

func TestConfirmTemplateFillPlanRejectsNonCheckFailedTask(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "template_fill_plan.contract"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)

	if _, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID); err == nil {
		t.Fatal("ConfirmTemplateFillPlan() error = nil for non-check failed task")
	}
}

func TestConfirmTemplateFillPlanSerializesConcurrentSaveThroughTransition(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	confirmAtPromotion := make(chan struct{})
	releaseConfirm := make(chan struct{})
	service.beforeTemplateFillAPICommit = func(targetStatus string) {
		if targetStatus == model.TaskStatusTemplateFillChecking {
			close(confirmAtPromotion)
			<-releaseConfirm
		}
	}

	confirmDone := make(chan error, 1)
	go func() {
		_, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID)
		confirmDone <- err
	}()
	<-confirmAtPromotion

	savePlan := templateFillContractPlan("draft", 1)
	templateFillContractFirstSlide(savePlan)["purpose"] = "concurrent save"
	saveDone := make(chan error, 1)
	go func() {
		_, err := service.SaveTemplateFillPlan(context.Background(), task.ID, savePlan)
		saveDone <- err
	}()
	select {
	case err := <-saveDone:
		close(releaseConfirm)
		<-confirmDone
		t.Fatalf("concurrent save completed inside confirm transaction with error %v", err)
	case <-time.After(300 * time.Millisecond):
		close(releaseConfirm)
	}
	if err := <-confirmDone; err != nil {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v", err)
	}
	if err := <-saveDone; err == nil {
		t.Fatal("concurrent SaveTemplateFillPlan() error = nil after confirm changed task status")
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusTemplateFillChecking {
		t.Fatalf("persisted status = %q, want template_fill_checking", persisted.Status)
	}
	plan, err := readTemplateFillJSONObject(filepath.Join(projectPath, "analysis", "fill_plan.json"), "template fill plan")
	if err != nil {
		t.Fatal(err)
	}
	if plan["status"] != "confirmed" || plan["slides"].([]any)[0].(map[string]any)["purpose"] == "concurrent save" {
		t.Fatalf("canonical plan after concurrent confirm/save = %#v", plan)
	}
}

func TestGetTemplateFillPlanSerializesWithConfirmTransaction(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	confirmAtTransition := make(chan struct{})
	releaseConfirm := make(chan struct{})
	service.beforeTemplateFillAPICommit = func(targetStatus string) {
		if targetStatus == model.TaskStatusTemplateFillChecking {
			close(confirmAtTransition)
			<-releaseConfirm
		}
	}
	confirmDone := make(chan error, 1)
	go func() {
		_, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID)
		confirmDone <- err
	}()
	<-confirmAtTransition

	getDone := make(chan error, 1)
	go func() {
		_, err := service.GetTemplateFillPlan(context.Background(), task.ID)
		getDone <- err
	}()
	select {
	case err := <-getDone:
		close(releaseConfirm)
		<-confirmDone
		t.Fatalf("GET completed inside confirm filesystem/DB transaction with error %v", err)
	case <-time.After(300 * time.Millisecond):
		close(releaseConfirm)
	}
	if err := <-confirmDone; err != nil {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v", err)
	}
	if err := <-getDone; err != nil {
		t.Fatalf("GetTemplateFillPlan() error = %v", err)
	}
}

func TestGetTemplateFillPlanWaitsForCanonicalProjectPromotion(t *testing.T) {
	service, _, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	unlockPromotion, err := acquireProjectPromotionLock(context.Background(), filepath.Join(workspacePath, ".slidesmith", "project-promotions.lock"))
	if err != nil {
		t.Fatal(err)
	}
	getDone := make(chan error, 1)
	go func() {
		_, err := service.GetTemplateFillPlan(context.Background(), task.ID)
		getDone <- err
	}()
	select {
	case err := <-getDone:
		unlockPromotion()
		t.Fatalf("GET completed while canonical project promotion lock was held with error %v", err)
	case <-time.After(300 * time.Millisecond):
		unlockPromotion()
	}
	if err := <-getDone; err != nil {
		t.Fatalf("GetTemplateFillPlan() error = %v", err)
	}
}

func TestConfirmTemplateFillPlanSerializesCancellationThroughTransition(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	confirmAtTransition := make(chan struct{})
	releaseConfirm := make(chan struct{})
	service.beforeTemplateFillAPICommit = func(targetStatus string) {
		if targetStatus == model.TaskStatusTemplateFillChecking {
			close(confirmAtTransition)
			<-releaseConfirm
		}
	}
	confirmDone := make(chan error, 1)
	go func() {
		_, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID)
		confirmDone <- err
	}()
	<-confirmAtTransition

	cancelDone := make(chan error, 1)
	go func() {
		_, err := service.CancelTask(context.Background(), task.ID)
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		close(releaseConfirm)
		<-confirmDone
		t.Fatalf("cancellation completed inside confirm filesystem/DB transaction with error %v", err)
	case <-time.After(300 * time.Millisecond):
		close(releaseConfirm)
	}
	if err := <-confirmDone; err != nil {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusCancelled {
		t.Fatalf("persisted status = %q, want cancelled", persisted.Status)
	}
	_, _, status, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != "confirmed" {
		t.Fatalf("cancelled task plan status = %q, want confirmed after serialized confirm", status)
	}
}

func TestRegenerateTemplateFillPlanCleansDownstreamAndQueuesPlanning(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	paths := writeTemplateFillDownstreamOutputs(t, projectPath)

	updated, err := service.RegenerateTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("RegenerateTemplateFillPlan() error = %v", err)
	}
	if updated.Status != model.TaskStatusTemplateFillPlanning {
		t.Fatalf("status = %q, want template_fill_planning", updated.Status)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("downstream output still exists at %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(projectPath, "sources", "brand.pptx")); err != nil {
		t.Fatalf("source was removed: %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusTemplateFillPlanning {
		t.Fatalf("persisted status = %q", persisted.Status)
	}
}

func TestRegenerateTemplateFillPlanRestoresOutputsWhenDBTransitionFails(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	paths := writeTemplateFillDownstreamOutputs(t, projectPath)
	want := snapshotTemplateFillPreviewPaths(t, paths)
	injected := errors.New("injected regenerate transition failure")
	installTemplateFillTransitionFailure(t, repo.DB(), model.TaskStatusTemplateFillPlanning, injected)

	if _, err := service.RegenerateTemplateFillPlan(context.Background(), task.ID); !errors.Is(err, injected) {
		t.Fatalf("RegenerateTemplateFillPlan() error = %v, want injected failure", err)
	}
	for path, wantBytes := range want {
		if got := mustReadTemplateFillPreviewFile(t, path); !bytes.Equal(got, wantBytes) {
			t.Fatalf("output %s was not restored\ngot: %s\nwant: %s", path, got, wantBytes)
		}
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("persisted status = %q, want awaiting_template_fill_confirm", persisted.Status)
	}
}

func TestRegenerateTemplateFillPlanRestoresOutputsWhenDatabaseRemainsUnavailable(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	paths := writeTemplateFillDownstreamOutputs(t, projectPath)
	want := snapshotTemplateFillPreviewPaths(t, paths)
	injected := errors.New("database unavailable after regenerate exchange")
	installTemplateFillPersistentDatabaseFailure(t, repo.DB(), model.TaskStatusTemplateFillPlanning, injected)

	if _, err := service.RegenerateTemplateFillPlan(context.Background(), task.ID); !errors.Is(err, injected) {
		t.Fatalf("RegenerateTemplateFillPlan() error = %v, want database unavailable", err)
	}
	for path, wantBytes := range want {
		if got := mustReadTemplateFillPreviewFile(t, path); !bytes.Equal(got, wantBytes) {
			t.Fatalf("output %s was not restored without DB-backed rollback\ngot: %s\nwant: %s", path, got, wantBytes)
		}
	}
}

func TestSaveTemplateFillPlanRestoresPriorProjectWhenPreviewReadFails(t *testing.T) {
	service, _, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 1, 2, 3)
	contractPath := filepath.Join(projectPath, ".slidesmith", "contracts", "template_fill_check.json")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "template_fill_check.json"), "stale contract\n")
	planPath := filepath.Join(projectPath, "analysis", "fill_plan.json")
	wantPlan := mustReadTemplateFillPreviewFile(t, planPath)
	wantReport := mustReadTemplateFillPreviewFile(t, filepath.Join(projectPath, "analysis", "check_report.json"))
	wantContract := mustReadTemplateFillPreviewFile(t, contractPath)
	service.beforeTemplateFillAPICommit = func(point string) {
		if point == "template_fill_preview" {
			_ = os.Remove(planPath)
		}
	}
	updated := templateFillContractPlan("confirmed", 1)
	templateFillContractFirstSlide(updated)["purpose"] = "must roll back after preview failure"

	if _, err := service.SaveTemplateFillPlan(context.Background(), task.ID, updated); err == nil {
		t.Fatal("SaveTemplateFillPlan() error = nil, want post-promotion preview read failure")
	}
	for path, want := range map[string][]byte{
		planPath: wantPlan,
		filepath.Join(projectPath, "analysis", "check_report.json"): wantReport,
		contractPath: wantContract,
	} {
		if got := mustReadTemplateFillPreviewFile(t, path); !bytes.Equal(got, want) {
			t.Fatalf("prior file %s was not restored\ngot: %s\nwant: %s", path, got, want)
		}
	}
}

func TestRegenerateTemplateFillPlanPreservesOutputsWhenCleanupCannotBeStaged(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	paths := writeTemplateFillDownstreamOutputs(t, projectPath)
	symlinkPath := filepath.Join(projectPath, "exports", "unsafe-link")
	if err := os.Symlink(filepath.Join(projectPath, "sources", "brand.pptx"), symlinkPath); err != nil {
		t.Fatal(err)
	}

	if _, err := service.RegenerateTemplateFillPlan(context.Background(), task.ID); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("RegenerateTemplateFillPlan() error = %v, want no-follow staging failure", err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("output %s changed after cleanup failure: %v", path, err)
		}
	}
	if _, err := os.Lstat(symlinkPath); err != nil {
		t.Fatalf("unsafe member was touched after cleanup failure: %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusAwaitingTemplateFillConfirm {
		t.Fatalf("persisted status = %q", persisted.Status)
	}
}

func TestTemplateFillAPISessionCleanupFailureIsObservableAndRetriable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api-session")
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	session := &templateFillAPISession{root: root}

	if err := session.cleanup(); err == nil {
		t.Fatal("cleanup() error = nil for retained unreadable session")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("failed cleanup did not retain a retriable session: %v", err)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := session.cleanup(); err != nil {
		t.Fatalf("cleanup() retry error = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("session still exists after successful retry: %v", err)
	}
}

func TestCommittedTemplateFillCleanupFailureIsDurableAndRetried(t *testing.T) {
	service, _, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	var retainedAttempt string
	var locked string
	service.beforeTemplateFillAPICommit = func(point string) {
		if point != model.TaskStatusTemplateFillChecking {
			return
		}
		matches, err := filepath.Glob(filepath.Join(workspacePath, ".slidesmith", "project-promotions", "template-fill-api-*", "*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("promotion attempts = %#v, error = %v", matches, err)
		}
		retainedAttempt = matches[0]
		locked = filepath.Join(retainedAttempt, "project", "locked-cleanup")
		if err := os.MkdirAll(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	updated, err := service.ConfirmTemplateFillPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ConfirmTemplateFillPlan() error = %v; committed result must stay successful", err)
	}
	if updated.Status != model.TaskStatusTemplateFillChecking {
		t.Fatalf("status = %q, want template_fill_checking", updated.Status)
	}
	markers, err := filepath.Glob(filepath.Join(workspacePath, ".slidesmith", templateFillCommittedCleanupDir, "*.path"))
	if err != nil || len(markers) != 1 {
		t.Fatalf("committed cleanup markers = %#v, error = %v", markers, err)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetTemplateFillPlan(context.Background(), task.ID); err != nil {
		t.Fatalf("GetTemplateFillPlan() cleanup retry error = %v", err)
	}
	if _, err := os.Stat(retainedAttempt); !os.IsNotExist(err) {
		t.Fatalf("committed promotion cleanup debt still exists: %v", err)
	}
	if _, err := os.Stat(markers[0]); !os.IsNotExist(err) {
		t.Fatalf("committed cleanup marker still exists: %v", err)
	}
}

func TestRollbackExchangeFailureMarkerCannotDeleteRetainedCanonical(t *testing.T) {
	service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	unlockAPI, err := service.lockTemplateFillAPI(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockAPI()
	releaseClaim, err := service.claimTemplateFillAPI(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseClaim() }()
	session, err := service.newTemplateFillAPISession(context.Background(), task, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := service.beginTemplateFillProjectExchange(context.Background(), task, session, model.TaskStatusTemplateFillChecking, nil)
	if err != nil {
		t.Fatal(err)
	}
	recoveryPath := exchange.staged.projectPath
	injected := errors.New("injected direct rollback exchange failure")
	protectedBeforeRollbackAttempt := false
	exchange.staged.exchangeDirectories = func(string, string) error {
		markers, globErr := filepath.Glob(filepath.Join(workspacePath, ".slidesmith", templateFillCommittedCleanupDir, "*.protected"))
		protectedBeforeRollbackAttempt = globErr == nil && len(markers) == 1
		return injected
	}
	if err := exchange.rollback(); !errors.Is(err, injected) {
		t.Fatalf("rollback() error = %v, want injected failure", err)
	}
	if !protectedBeforeRollbackAttempt {
		t.Fatal("rollback exchange was attempted before durable protected marker activation")
	}
	if _, err := os.Stat(recoveryPath); err != nil {
		t.Fatalf("rollback recovery is absent before sweep: %v", err)
	}
	protectedMarkers, err := filepath.Glob(filepath.Join(workspacePath, ".slidesmith", templateFillCommittedCleanupDir, "*.protected"))
	if err != nil || len(protectedMarkers) != 1 {
		t.Fatalf("protected rollback recovery markers = %#v, error = %v", protectedMarkers, err)
	}
	task.Status = model.TaskStatusTemplateFillChecking
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sweepCommittedTemplateFillPromotions(context.Background(), reloaded); err != nil {
		t.Fatalf("sweepCommittedTemplateFillPromotions() error = %v", err)
	}
	if _, err := os.Stat(recoveryPath); err != nil {
		t.Fatalf("sweeper deleted retained rollback recovery: %v", err)
	}
	if _, err := os.Stat(protectedMarkers[0]); err != nil {
		t.Fatalf("sweeper deleted protected rollback recovery marker: %v", err)
	}
}

func TestPendingTemplateFillCleanupMarkerIsNotSweptFromTaskStatus(t *testing.T) {
	service, _, task, _, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	promotionRoot := filepath.Join(workspacePath, ".slidesmith", "project-promotions")
	attemptRoot := filepath.Join(promotionRoot, "template-fill-api-restart", "attempt")
	staged := &stagedProjectPromotion{
		promotionRoot: promotionRoot,
		attemptRoot:   attemptRoot,
	}
	if err := os.MkdirAll(filepath.Join(attemptRoot, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath, err := writeTemplateFillPendingCleanupMarker(
		staged,
		task.ID,
		model.TaskStatusAwaitingTemplateFillConfirm,
		model.TaskStatusTemplateFillChecking,
	)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = model.TaskStatusTemplateFillChecking

	if err := service.sweepCommittedTemplateFillPromotions(context.Background(), task); err != nil {
		t.Fatalf("sweepCommittedTemplateFillPromotions() error = %v", err)
	}
	if _, err := os.Stat(attemptRoot); err != nil {
		t.Fatalf("sweeper inferred pending recovery was disposable from task status: %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("sweeper deleted pending recovery marker: %v", err)
	}
}

func TestWriteTemplateFillCleanupMarkerAtomicallyDoesNotPublishPartialMarker(t *testing.T) {
	markerDirectory := t.TempDir()
	markerPath := filepath.Join(markerDirectory, "attempt.pending")
	injected := errors.New("injected marker write failure")
	ops := defaultTemplateFillCleanupMarkerOps()
	ops.write = func(file *os.File, raw []byte) (int, error) {
		written, err := file.Write(raw[:len(raw)/2])
		if err != nil {
			return written, err
		}
		return written, injected
	}

	err := writeTemplateFillCleanupMarkerAtomically(markerPath, []byte(`{"task_id":"task-template-fill"}`+"\n"), ops)
	if !errors.Is(err, injected) {
		t.Fatalf("writeTemplateFillCleanupMarkerAtomically() error = %v, want injected failure", err)
	}
	if _, err := os.Lstat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("partial final cleanup marker was published: %v", err)
	}
	entries, err := os.ReadDir(markerDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary cleanup marker was not removed: %#v", entries)
	}
}

func TestWriteTemplateFillCleanupMarkerAtomicallyPreservesExistingMarkerOnRenameFailure(t *testing.T) {
	markerDirectory := t.TempDir()
	markerPath := filepath.Join(markerDirectory, "attempt.pending")
	existing := []byte(`{"task_id":"existing"}` + "\n")
	if err := os.WriteFile(markerPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected marker rename failure")
	ops := defaultTemplateFillCleanupMarkerOps()
	ops.rename = func(string, string) error { return injected }

	err := writeTemplateFillCleanupMarkerAtomically(markerPath, []byte(`{"task_id":"replacement"}`+"\n"), ops)
	if !errors.Is(err, injected) {
		t.Fatalf("writeTemplateFillCleanupMarkerAtomically() error = %v, want injected failure", err)
	}
	retained, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, existing) {
		t.Fatalf("existing cleanup marker = %q, want %q", retained, existing)
	}
	entries, err := os.ReadDir(markerDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(markerPath) {
		t.Fatalf("temporary cleanup marker was not removed: %#v", entries)
	}
}

func TestWriteTemplateFillCleanupMarkerAtomicallyReportsTemporaryRemovalFailure(t *testing.T) {
	markerDirectory := t.TempDir()
	markerPath := filepath.Join(markerDirectory, "attempt.pending")
	writeFailure := errors.New("injected marker write failure")
	removeFailure := errors.New("injected marker temporary removal failure")
	ops := defaultTemplateFillCleanupMarkerOps()
	ops.write = func(*os.File, []byte) (int, error) { return 0, writeFailure }
	var temporaryPath string
	ops.remove = func(path string) error {
		temporaryPath = path
		return removeFailure
	}

	err := writeTemplateFillCleanupMarkerAtomically(markerPath, []byte(`{"task_id":"task-template-fill"}`+"\n"), ops)
	if !errors.Is(err, writeFailure) || !errors.Is(err, removeFailure) {
		t.Fatalf("writeTemplateFillCleanupMarkerAtomically() error = %v, want write and removal failures", err)
	}
	if _, err := os.Lstat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("partial final cleanup marker was published: %v", err)
	}
	if temporaryPath == "" {
		t.Fatal("temporary cleanup marker removal was not attempted")
	}
	if err := os.Remove(temporaryPath); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackCleanupFailureActivatesRetriableMarker(t *testing.T) {
	service, _, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusAwaitingTemplateFillConfirm, nil)
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	unlockAPI, err := service.lockTemplateFillAPI(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockAPI()
	releaseClaim, err := service.claimTemplateFillAPI(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseClaim() }()
	session, err := service.newTemplateFillAPISession(context.Background(), task, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := service.beginTemplateFillProjectExchange(context.Background(), task, session, model.TaskStatusTemplateFillChecking, nil)
	if err != nil {
		t.Fatal(err)
	}
	attemptPath := exchange.staged.attemptRoot
	injected := errors.New("injected discarded candidate cleanup failure")
	exchange.staged.removeAll = func(string) error { return injected }
	if err := exchange.rollback(); !errors.Is(err, injected) {
		t.Fatalf("rollback() error = %v, want cleanup failure", err)
	}
	markers, err := filepath.Glob(filepath.Join(workspacePath, ".slidesmith", templateFillCommittedCleanupDir, "*.path"))
	if err != nil || len(markers) != 1 {
		t.Fatalf("retriable rollback cleanup markers = %#v, error = %v", markers, err)
	}
	if err := service.sweepCommittedTemplateFillPromotions(context.Background(), task); err != nil {
		t.Fatalf("sweepCommittedTemplateFillPromotions() error = %v", err)
	}
	if _, err := os.Stat(attemptPath); !os.IsNotExist(err) {
		t.Fatalf("discarded candidate still exists after cleanup retry: %v", err)
	}
}

func writeTemplateFillPreviewCheckReport(t *testing.T, projectPath string, ok, warn, checkErrors int) {
	t.Helper()
	mustWriteTemplateFillContractJSON(t, projectPath, filepath.Join("analysis", "check_report.json"), map[string]any{
		"schema":  "template_fill_pptx_check.v1",
		"summary": map[string]any{"ok": ok, "warn": warn, "error": checkErrors},
		"results": []any{},
	})
}

func mustReadTemplateFillPreviewFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sameTemplateFillPreviewFile(t *testing.T, left, right string) bool {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(leftInfo, rightInfo)
}

func installTemplateFillTransitionFailure(t *testing.T, db *gorm.DB, targetStatus string, injected error) {
	t.Helper()
	name := "test:fail-template-fill-transition-" + strings.ReplaceAll(targetStatus, "_", "-")
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		task, ok := tx.Statement.Dest.(*model.Task)
		if ok && task.Status == targetStatus {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(name)
	})
}

func installTemplateFillPersistentDatabaseFailure(t *testing.T, db *gorm.DB, targetStatus string, injected error) {
	t.Helper()
	name := "test:persistent-template-fill-db-failure-" + strings.ReplaceAll(targetStatus, "_", "-")
	armed := false
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if task, ok := tx.Statement.Dest.(*model.Task); ok && task.Status == targetStatus {
			armed = true
		}
		if armed {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(name)
	})
}

func writeTemplateFillDownstreamOutputs(t *testing.T, projectPath string) []string {
	t.Helper()
	mustWriteTemplateFillPlan(t, projectPath, "draft", 1)
	writeTemplateFillPreviewCheckReport(t, projectPath, 1, 0, 0)
	paths := []string{
		filepath.Join(projectPath, "analysis", "fill_plan.json"),
		filepath.Join(projectPath, "analysis", "check_report.json"),
	}
	for _, name := range []string{"template_fill_plan.json", "template_fill_check.json", "template_fill_apply.json", "template_fill_validate.json"} {
		path := filepath.Join(projectPath, ".slidesmith", "contracts", name)
		mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", name), name+"\n")
		paths = append(paths, path)
	}
	exportPath := filepath.Join(projectPath, "exports", "result.pptx")
	validatePath := filepath.Join(projectPath, "validation", "validate_report.json")
	mustWriteFileNoTest(projectPath, filepath.Join("exports", "result.pptx"), "pptx\n")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "validate_report.json"), "validate\n")
	paths = append(paths, exportPath, validatePath)
	return paths
}

func snapshotTemplateFillPreviewPaths(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte, len(paths))
	for _, path := range paths {
		snapshot[path] = mustReadTemplateFillPreviewFile(t, path)
	}
	return snapshot
}
