package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestBuildBeautifyLockIsImmutableAndIdempotent(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	contract := fixture.validatePlan(t)
	lock, err := BuildBeautifyLock(fixture.projectPath, fixture.taskID, contract.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if lock.SlideCount != 2 || len(lock.Slides) != 2 || lock.Slides[1].SourceSlide != 2 || lock.Slides[1].OutputPage != 2 {
		t.Fatalf("lock = %#v", lock)
	}
	second, err := BuildBeautifyLock(fixture.projectPath, fixture.taskID, contract.PlanSHA256)
	if err != nil || second.LockedAt != lock.LockedAt {
		t.Fatalf("idempotent lock = %#v, error = %v", second, err)
	}
	if _, err := BuildBeautifyLock(fixture.projectPath, fixture.taskID, strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable error = %v", err)
	}
}

func TestValidateBeautifyLockRejectsUpstreamMutation(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	fixture.buildLock(t)
	writeBeautifyTestFile(t, filepath.Join(fixture.projectPath, "confirm_ui", "result.json"), `{"mutated":true}`)
	if _, err := ValidateBeautifyLock(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateBeautifyLockRejectsFrozenSourceImageMutation(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	image := addBeautifySourceImageToFixture(t, fixture)
	fixture.buildLock(t)
	writeBeautifyTestFile(t, filepath.Join(fixture.projectPath, filepath.FromSlash(image.SourcePath)), "mutated-source-image")
	if _, err := ValidateBeautifyLock(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("source image lock error = %v", err)
	}
}

func TestBuildBeautifyLockRequiresConfirmedPlan(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	fixture.plan.Status = "draft"
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	contract := fixture.validatePlan(t)
	if _, err := BuildBeautifyLock(fixture.projectPath, fixture.taskID, contract.PlanSHA256); err == nil || !strings.Contains(err.Error(), "confirmed") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfirmationResultWritePolicyPreservesImmutableBeautifyLock(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	fixture.buildLock(t)
	task := &model.Task{ID: fixture.taskID, Route: model.TaskRouteBeautify}

	writeResult, err := confirmationResultWritePolicyForGenerate(task, fixture.projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if writeResult {
		t.Fatal("locked Beautify confirmation must not be rewritten during generate or retry")
	}

	writeBeautifyTestFile(t, filepath.Join(fixture.projectPath, "confirm_ui", "result.json"), `{"mutated":true}`)
	if _, err := confirmationResultWritePolicyForGenerate(task, fixture.projectPath); err == nil || !strings.Contains(err.Error(), "immutable Beautify confirmation") {
		t.Fatalf("stale lock policy error = %v", err)
	}
}

func TestConfirmationResultWritePolicyAllowsPreLockAndNonBeautifyWrites(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	for _, task := range []*model.Task{
		{ID: fixture.taskID, Route: model.TaskRouteBeautify},
		{ID: fixture.taskID, Route: model.TaskRouteMain},
	} {
		writeResult, err := confirmationResultWritePolicyForGenerate(task, fixture.projectPath)
		if err != nil || !writeResult {
			t.Fatalf("route %q writeResult = %v, error = %v", task.Route, writeResult, err)
		}
	}
}
