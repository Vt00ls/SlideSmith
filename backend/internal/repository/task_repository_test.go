package repository

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

func TestReplaceArtifactsByObjectKeyPrefixPopulatesCallerAfterCommit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	artifacts := []model.Artifact{{
		Kind:      model.ArtifactKindSourceMarkdown,
		Name:      "source.md",
		ObjectKey: "tasks/task-1/source-intake/sources/source.md",
	}}

	if err := repo.ReplaceArtifactsByObjectKeyPrefix(
		context.Background(),
		"task-1",
		"tasks/task-1/source-intake/",
		artifacts,
	); err != nil {
		t.Fatal(err)
	}
	if artifacts[0].ID == "" || artifacts[0].CreatedAt.IsZero() || artifacts[0].UpdatedAt.IsZero() {
		t.Fatalf("caller artifact lacks generated identity: %#v", artifacts[0])
	}
	if artifacts[0].TaskID != "task-1" || artifacts[0].Storage != "local" || artifacts[0].MetadataJSON != "{}" {
		t.Fatalf("caller artifact lacks persisted defaults: %#v", artifacts[0])
	}
	var persisted model.Artifact
	if err := db.First(&persisted, "id = ?", artifacts[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(artifacts[0], persisted) {
		t.Fatalf("caller artifact = %#v, persisted = %#v", artifacts[0], persisted)
	}
}

func TestReplaceArtifactsByObjectKeyPrefixDoesNotMutateCallerOnRollback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		CREATE TRIGGER fail_second_replacement
		BEFORE INSERT ON artifacts
		WHEN NEW.name = 'fail.md'
		BEGIN
			SELECT RAISE(ABORT, 'forced replacement rollback');
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	artifacts := []model.Artifact{
		{Kind: model.ArtifactKindSourceMarkdown, Name: "first.md", ObjectKey: "tasks/task-1/source-intake/sources/first.md"},
		{Kind: model.ArtifactKindSourceMarkdown, Name: "fail.md", ObjectKey: "tasks/task-1/source-intake/sources/fail.md"},
	}
	want := append([]model.Artifact(nil), artifacts...)

	if err := repo.ReplaceArtifactsByObjectKeyPrefix(
		context.Background(),
		"task-1",
		"tasks/task-1/source-intake/",
		artifacts,
	); err == nil {
		t.Fatal("ReplaceArtifactsByObjectKeyPrefix() error = nil, want rollback")
	}
	if !reflect.DeepEqual(artifacts, want) {
		t.Fatalf("caller artifacts mutated on rollback:\n got: %#v\nwant: %#v", artifacts, want)
	}
	var count int64
	if err := db.Model(&model.Artifact{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted artifacts after rollback = %d, want 0", count)
	}
}

func TestReplaceArtifactsByObjectKeyPrefixKeepsUploadedSources(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()

	uploaded := &model.Artifact{
		TaskID:    "task-1",
		Kind:      model.ArtifactKindSource,
		Name:      "input.md",
		Storage:   "local",
		ObjectKey: "tasks/task-1/source/input.md",
	}
	oldPublished := &model.Artifact{
		TaskID:    "task-1",
		Kind:      model.ArtifactKindPPTX,
		Name:      "old.pptx",
		Storage:   "local",
		ObjectKey: "tasks/task-1/artifacts/exports/old.pptx",
	}
	if err := repo.CreateArtifact(ctx, uploaded); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(ctx, oldPublished); err != nil {
		t.Fatal(err)
	}

	replacements := []model.Artifact{{
		Kind:      model.ArtifactKindPPTX,
		Name:      "new.pptx",
		Storage:   "local",
		ObjectKey: "tasks/task-1/artifacts/exports/new.pptx",
	}}
	if err := repo.ReplaceArtifactsByObjectKeyPrefix(ctx, "task-1", "tasks/task-1/artifacts/", replacements); err != nil {
		t.Fatal(err)
	}

	artifacts, err := repo.ListArtifacts(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("expected uploaded source plus replacement, got %d: %#v", len(artifacts), artifacts)
	}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		seen[artifact.ObjectKey] = true
	}
	if !seen["tasks/task-1/source/input.md"] {
		t.Fatal("uploaded source artifact was deleted")
	}
	if !seen["tasks/task-1/artifacts/exports/new.pptx"] {
		t.Fatal("new published artifact was not inserted")
	}
	if seen["tasks/task-1/artifacts/exports/old.pptx"] {
		t.Fatal("old published artifact was not replaced")
	}
}

func TestListArtifactsReturnsLatestPublishedVersion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()

	uploaded := &model.Artifact{
		TaskID:    "task-1",
		Kind:      model.ArtifactKindSource,
		Name:      "input.md",
		Storage:   "local",
		ObjectKey: "tasks/task-1/source/input.md",
	}
	oldPublished := &model.Artifact{
		TaskID:         "task-1",
		Kind:           model.ArtifactKindPPTX,
		Name:           "old.pptx",
		Storage:        "local",
		ObjectKey:      "tasks/task-1/artifacts/v1/exports/old.pptx",
		PublishVersion: "v1",
	}
	latestPublished := &model.Artifact{
		TaskID:         "task-1",
		Kind:           model.ArtifactKindPPTX,
		Name:           "new.pptx",
		Storage:        "local",
		ObjectKey:      "tasks/task-1/artifacts/v2/exports/new.pptx",
		PublishVersion: "v2",
	}
	for _, artifact := range []*model.Artifact{uploaded, oldPublished, latestPublished} {
		if err := repo.CreateArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}

	artifacts, err := repo.ListArtifacts(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		seen[artifact.ObjectKey] = true
	}
	if !seen["tasks/task-1/source/input.md"] {
		t.Fatal("uploaded source artifact was not returned")
	}
	if !seen["tasks/task-1/artifacts/v2/exports/new.pptx"] {
		t.Fatal("latest published artifact was not returned")
	}
	if seen["tasks/task-1/artifacts/v1/exports/old.pptx"] {
		t.Fatal("older published artifact should be hidden from default list")
	}
}

func TestListRuntimeRunsOrdersNewestFirst(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.TaskRuntimeRun{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()

	oldRun := &model.TaskRuntimeRun{TaskID: "task-1", Phase: "prepare", Command: "prepare", Status: "succeeded"}
	newRun := &model.TaskRuntimeRun{TaskID: "task-1", Phase: "generate", Command: "generate", Status: "failed"}
	if err := repo.CreateRuntimeRun(ctx, oldRun); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := repo.CreateRuntimeRun(ctx, newRun); err != nil {
		t.Fatal(err)
	}

	runs, err := repo.ListRuntimeRuns(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].ID != newRun.ID {
		t.Fatalf("newest run should be first, got %s", runs[0].ID)
	}
}

func TestCreatePhaseRunIncrementsAttemptsAndListsChronologically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.TaskPhaseRun{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()

	first := &model.TaskPhaseRun{TaskID: "task-1", Phase: "spec_generate", Runner: "agent", Status: "running"}
	second := &model.TaskPhaseRun{TaskID: "task-1", Phase: "spec_generate", Runner: "agent", Status: "running"}
	otherPhase := &model.TaskPhaseRun{TaskID: "task-1", Phase: "svg_execute", Runner: "agent", Status: "running"}
	if err := repo.CreatePhaseRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := repo.CreatePhaseRun(ctx, second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := repo.CreatePhaseRun(ctx, otherPhase); err != nil {
		t.Fatal(err)
	}

	if first.Attempt != 1 || second.Attempt != 2 || otherPhase.Attempt != 1 {
		t.Fatalf("unexpected attempts: first=%d second=%d other=%d", first.Attempt, second.Attempt, otherPhase.Attempt)
	}
	runs, err := repo.ListPhaseRuns(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 phase runs, got %d", len(runs))
	}
	if runs[0].ID != first.ID || runs[1].ID != second.ID || runs[2].ID != otherPhase.ID {
		t.Fatalf("phase runs should be chronological, got %#v", runs)
	}
}

func TestSaveTaskIfStatusUsesCompareAndSwapAndPreservesExecutionClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()

	task := &model.Task{
		ID:             "task-cas",
		Title:          "CAS",
		Status:         model.TaskStatusTemplateFillChecking,
		RuntimeProject: "task_cas",
	}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := repo.ClaimTaskExecution(ctx, task.ID, task.Status, "claim-1", now, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("initial execution claim was not acquired")
	}

	loaded, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Status = model.TaskStatusTemplateFillApplying
	loaded.LastRuntimeRunID = "runtime-1"
	saved, err := repo.SaveTaskIfStatus(ctx, loaded, model.TaskStatusTemplateFillChecking, "claim-1")
	if err != nil {
		t.Fatal(err)
	}
	if !saved {
		t.Fatal("expected-status save did not update task")
	}
	persisted, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusTemplateFillApplying || persisted.LastRuntimeRunID != "runtime-1" {
		t.Fatalf("CAS task fields = %#v", persisted)
	}
	if persisted.ExecutionClaimToken != "claim-1" || persisted.ExecutionClaimedAt == nil {
		t.Fatalf("task CAS cleared durable claim: %#v", persisted)
	}
}

func TestSaveTaskIfStatusCannotResurrectCancelledTask(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()
	task := &model.Task{
		ID:             "task-cancel-race",
		Title:          "Cancel race",
		Status:         model.TaskStatusTemplateFillApplying,
		RuntimeProject: "task_cancel_race",
	}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	stale, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelledAt := time.Now().UTC()
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status":       model.TaskStatusCancelled,
		"cancelled_at": cancelledAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	stale.LastRuntimeRunID = "late-runtime"
	saved, err := repo.SaveTaskIfStatus(ctx, stale, model.TaskStatusTemplateFillApplying, "")
	if err != nil {
		t.Fatal(err)
	}
	if saved {
		t.Fatal("stale expected-status save overwrote cancellation")
	}
	persisted, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusCancelled || persisted.LastRuntimeRunID != "" || persisted.CancelledAt == nil {
		t.Fatalf("cancelled task was resurrected: %#v", persisted)
	}
}

func TestSaveTaskIfStatusRejectsWorkerWhoseStaleClaimWasReplaced(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()
	task := &model.Task{
		ID:             "task-fenced-worker",
		Title:          "Fenced worker",
		Status:         model.TaskStatusTemplateFillApplying,
		RuntimeProject: "task_fenced_worker",
	}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldClaimedAt := now.Add(-2 * time.Hour)
	claimed, err := repo.ClaimTaskExecution(ctx, task.ID, task.Status, "old-token", oldClaimedAt, oldClaimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("old claim = %v, error = %v", claimed, err)
	}
	staleWorker, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err = repo.ClaimTaskExecution(ctx, task.ID, task.Status, "new-token", now, now.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("new claim = %v, error = %v", claimed, err)
	}
	staleWorker.LastRuntimeRunID = "late-old-worker-result"
	saved, err := repo.SaveTaskIfStatus(ctx, staleWorker, task.Status, "old-token")
	if err != nil {
		t.Fatal(err)
	}
	if saved {
		t.Fatal("old worker persisted after its claim was fenced")
	}
	persisted, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "new-token" || persisted.LastRuntimeRunID != "" {
		t.Fatalf("stale worker changed task after takeover: %#v", persisted)
	}
	released, err := repo.ReleaseTaskExecution(ctx, task.ID, "old-token")
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("old worker released successor's claim")
	}
	persisted, err = repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "new-token" {
		t.Fatalf("successor claim was cleared by old worker: %#v", persisted)
	}
}

func TestTaskExecutionClaimIsExclusiveReleasableAndStaleRecoverable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()
	task := &model.Task{
		ID:             "task-lease",
		Title:          "Lease",
		Status:         model.TaskStatusTemplateFillPlanning,
		RuntimeProject: "task_lease",
	}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := repo.ClaimTaskExecution(ctx, task.ID, task.Status, "claim-1", now, now.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim-1 = %v, error = %v", claimed, err)
	}
	claimed, err = repo.ClaimTaskExecution(ctx, task.ID, task.Status, "claim-2", now.Add(time.Minute), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("second worker acquired a live claim")
	}
	released, err := repo.ReleaseTaskExecution(ctx, task.ID, "claim-2")
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("non-owner release cleared another worker's claim")
	}
	released, err = repo.ReleaseTaskExecution(ctx, task.ID, "claim-1")
	if err != nil || !released {
		t.Fatalf("owner release = %v, error = %v", released, err)
	}

	oldClaimedAt := now.Add(-2 * time.Hour)
	claimed, err = repo.ClaimTaskExecution(ctx, task.ID, task.Status, "stale-claim", oldClaimedAt, oldClaimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("stale initial claim = %v, error = %v", claimed, err)
	}
	claimed, err = repo.ClaimTaskExecution(ctx, task.ID, task.Status, "recovered-claim", now, now.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("stale recovery claim = %v, error = %v", claimed, err)
	}
	persisted, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "recovered-claim" || persisted.ExecutionClaimedAt == nil || !persisted.ExecutionClaimedAt.Equal(now) {
		t.Fatalf("recovered claim = %#v", persisted)
	}
}
