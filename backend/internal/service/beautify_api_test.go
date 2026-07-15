package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestBeautifyPlanAPICASSaveRejectsFrozenMutationAndConfirmsImmutableLock(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	fixture.plan.Status = "draft"
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	fixture.validatePlan(t)

	workspaceRoot := t.TempDir()
	service, repo := profileTestService(t, config.AgentComposeConfig{
		WorkspaceRoot:   workspaceRoot,
		BeautifyEnabled: true,
	})
	workspace := filepath.Join(workspaceRoot, fixture.taskID)
	projectPath := filepath.Join(workspace, "projects", fixture.taskID)
	if err := copyDir(context.Background(), fixture.projectPath, projectPath); err != nil {
		t.Fatal(err)
	}
	task := &model.Task{
		ID: fixture.taskID, Title: "Beautify API", Status: model.TaskStatusAwaitingBeautifyConfirm,
		Route: model.TaskRouteBeautify, RuntimeProject: fixture.taskID, RuntimeWorkspacePath: workspace,
		RouteSelectionJSON: `{"route":"beautify","capability_snapshot":{"captured":true,"beautify_enabled":true,"beautify_fidelity_strict":true}}`,
	}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	preview, err := service.GetBeautifyPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.TaskID != task.ID || !preview.CanEdit || !preview.CanConfirm || preview.Source.SlideCount != 2 {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	candidate := beautifyPlanMapForTest(t, preview.Plan)
	slides := candidate["slides"].([]any)
	slides[0].(map[string]any)["layout_strategy"] = "clarify_hierarchy"
	saved, err := service.SaveBeautifyPlan(context.Background(), task.ID, candidate, preview.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 2 || saved.Plan.Slides[0].LayoutStrategy != "clarify_hierarchy" {
		t.Fatalf("saved preview = %#v", saved)
	}

	frozenMutation := beautifyPlanMapForTest(t, saved.Plan)
	frozenMutation["slide_count"] = float64(3)
	if _, err := service.SaveBeautifyPlan(context.Background(), task.ID, frozenMutation, saved.PlanSHA256); err == nil {
		t.Fatal("frozen slide_count mutation was accepted")
	}
	unknownInjection := beautifyPlanMapForTest(t, saved.Plan)
	unknownInjection["command"] = "rm -rf /"
	if _, err := service.SaveBeautifyPlan(context.Background(), task.ID, unknownInjection, saved.PlanSHA256); err == nil {
		t.Fatal("unknown command injection was accepted")
	}

	confirmed, err := service.ConfirmBeautifyPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != model.TaskStatusSpecGenerating {
		t.Fatalf("confirmed status = %q", confirmed.Status)
	}
	lock, err := ValidateBeautifyLock(projectPath, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Revision != 2 || lock.SlideCount != 2 {
		t.Fatalf("lock = %#v", lock)
	}
	if _, err := service.SaveBeautifyPlan(context.Background(), task.ID, candidate, saved.PlanSHA256); err == nil {
		t.Fatal("confirmed Beautify lock remained editable")
	}
}

func TestBeautifyConfirmationsAreSourceSeededPageLockedAndRouteToPlan(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 4)
	service, repo := profileTestService(t, config.AgentComposeConfig{BeautifyEnabled: true})
	task := &model.Task{ID: fixture.taskID, Title: "Confirm", Status: model.TaskStatusAwaitingAnchorConfirm, Route: model.TaskRouteBeautify}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := service.seedBeautifyConfirmations(context.Background(), task, fixture.projectPath); err != nil {
		t.Fatal(err)
	}
	anchored, err := service.SubmitConfirmations(context.Background(), task.ID, map[string]any{
		"canvas": "ppt169", "language": "zh-CN", "audience": "source audience",
		"content_divergence": "verbatim", "delivery_purpose": "balanced",
		"mode": "source-structure", "visual_style": "source-replica",
	})
	if err != nil {
		t.Fatal(err)
	}
	if anchored.Status != model.TaskStatusAwaitingRealizationConfirm {
		t.Fatalf("Tier1 Beautify status = %q", anchored.Status)
	}
	confirmations, err := repo.ListConfirmations(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]model.TaskConfirmation{}
	for _, confirmation := range confirmations {
		byKey[confirmation.Key] = confirmation
	}
	if byKey["page_count"].Recommendation != "4" || byKey["visual_style"].Recommendation != "source-replica" || byKey["image_usage"].Recommendation != "provided" {
		t.Fatalf("source-seeded confirmations = %#v", byKey)
	}
	if _, err := service.SubmitConfirmations(context.Background(), task.ID, map[string]any{"page_count": 5}); err == nil {
		t.Fatal("Beautify page_count edit was accepted")
	}
	updated, err := service.SubmitConfirmations(context.Background(), task.ID, map[string]any{
		"color": "source observed palette", "typography": "source theme fonts", "image_usage": "provided",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusBeautifyPlanning {
		t.Fatalf("Tier2 Beautify status = %q", updated.Status)
	}
}

func TestBeautifyPlanAPIRecoversConfirmedLockWithoutAllowingMutationOrRegenerate(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	fixture.plan.Status = "draft"
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	fixture.validatePlan(t)

	workspaceRoot := t.TempDir()
	service, repo := profileTestService(t, config.AgentComposeConfig{WorkspaceRoot: workspaceRoot, BeautifyEnabled: true})
	workspace := filepath.Join(workspaceRoot, fixture.taskID)
	projectPath := filepath.Join(workspace, "projects", fixture.taskID)
	if err := copyDir(context.Background(), fixture.projectPath, projectPath); err != nil {
		t.Fatal(err)
	}
	task := &model.Task{
		ID: fixture.taskID, Title: "Beautify recovery", Status: model.TaskStatusAwaitingBeautifyConfirm,
		Route: model.TaskRouteBeautify, RuntimeProject: fixture.taskID, RuntimeWorkspacePath: workspace,
	}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmBeautifyPlan(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")
	lockSHA, err := sha256File(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	recovering, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovering.Status = model.TaskStatusAwaitingBeautifyConfirm
	if err := repo.SaveTask(context.Background(), recovering); err != nil {
		t.Fatal(err)
	}
	preview, err := service.GetBeautifyPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanEdit || !preview.CanConfirm || preview.Plan.Status != "confirmed" {
		t.Fatalf("recovery preview = %#v", preview)
	}
	candidate := beautifyPlanMapForTest(t, preview.Plan)
	if _, err := service.SaveBeautifyPlan(context.Background(), task.ID, candidate, preview.PlanSHA256); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("save with lock error = %v", err)
	}
	if current, _ := sha256File(lockPath); current != lockSHA {
		t.Fatalf("Save changed confirmed lock: %s != %s", current, lockSHA)
	}
	recovered, err := service.ConfirmBeautifyPlan(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != model.TaskStatusSpecGenerating {
		t.Fatalf("recovered status = %q", recovered.Status)
	}

	recovered.Status = model.TaskStatusFailed
	if err := repo.SaveTask(context.Background(), recovered); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegenerateBeautifyPlan(context.Background(), task.ID); err == nil || !strings.Contains(err.Error(), "explicit new revision") {
		t.Fatalf("regenerate with lock error = %v", err)
	}
	if current, _ := sha256File(lockPath); current != lockSHA {
		t.Fatalf("Regenerate changed confirmed lock: %s != %s", current, lockSHA)
	}
}

func beautifyPlanMapForTest(t *testing.T, plan BeautifyPlanDocument) map[string]any {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
