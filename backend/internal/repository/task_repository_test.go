package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

func TestLockTaskRunnerProfileIsIdempotentAndImmutable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	task := &model.Task{ID: "profile-task", Title: "Profile", Status: model.TaskStatusUploaded}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	lockedAt := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	for attempt := 0; attempt < 2; attempt++ {
		locked, err := repo.LockTaskRunnerProfile(context.Background(), task.ID, []string{model.TaskStatusUploaded}, model.RunnerProfileFullPPTMaster, model.RunnerProfileSourceDeploymentDefault, lockedAt)
		if err != nil || !locked {
			t.Fatalf("lock attempt %d = %v, %v", attempt, locked, err)
		}
	}
	if _, err := repo.LockTaskRunnerProfile(context.Background(), task.ID, []string{model.TaskStatusUploaded}, model.RunnerProfileRealLite, model.RunnerProfileSourceExplicitConfig, lockedAt); !errors.Is(err, ErrTaskRunnerProfileImmutable) {
		t.Fatalf("conflicting lock error = %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RunnerProfile != model.RunnerProfileFullPPTMaster || persisted.RunnerProfileSource != model.RunnerProfileSourceDeploymentDefault || persisted.RunnerProfileLockedAt == nil || !persisted.RunnerProfileLockedAt.Equal(lockedAt) {
		t.Fatalf("persisted profile lock = %#v", persisted)
	}
}

func TestConcurrentTaskRunnerProfileLockHasSingleWinner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:runner-profile-lock?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	task := &model.Task{ID: "concurrent-profile-task", Title: "Profile", Status: model.TaskStatusUploaded}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	profiles := []string{model.RunnerProfileFullPPTMaster, model.RunnerProfileRealLite}
	results := make(chan string, len(profiles))
	var wg sync.WaitGroup
	for _, profile := range profiles {
		wg.Add(1)
		go func(profile string) {
			defer wg.Done()
			locked, err := repo.LockTaskRunnerProfile(context.Background(), task.ID, []string{model.TaskStatusUploaded}, profile, model.RunnerProfileSourceExplicitConfig, time.Now().UTC())
			if locked && err == nil {
				results <- profile
			}
		}(profile)
	}
	wg.Wait()
	close(results)
	winners := []string{}
	for profile := range results {
		winners = append(winners, profile)
	}
	if len(winners) != 1 {
		t.Fatalf("concurrent lock winners = %#v, want exactly one", winners)
	}
}

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

func TestReplaceArtifactsByObjectKeyPrefixRollsBackPersistedIdentityDrift(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()
	unrelatedB := &model.Artifact{ID: "unrelated-b", TaskID: "task-b", Kind: model.ArtifactKindPPTX, ObjectKey: "tasks/task-b/artifacts/v-old/exports/old.pptx", PublishVersion: "v-old"}
	oldA := &model.Artifact{ID: "old-a", TaskID: "task-a", Kind: model.ArtifactKindPPTX, ObjectKey: "tasks/task-a/artifacts/v-old/exports/old.pptx", PublishVersion: "v-old"}
	for _, artifact := range []*model.Artifact{unrelatedB, oldA} {
		if err := repo.CreateArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`
		CREATE TRIGGER mutate_new_publish_identity
		AFTER INSERT ON artifacts
		WHEN NEW.id = 'attempt-a'
		BEGIN
			UPDATE artifacts
			SET task_id = 'task-b',
				publish_version = 'v-mutated',
				kind = 'other',
				object_key = 'tasks/task-b/artifacts/v-mutated/hijacked.bin'
			WHERE id = NEW.id;
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}
	replacements := []model.Artifact{{
		ID:             "attempt-a",
		TaskID:         "task-a",
		Kind:           model.ArtifactKindPPTX,
		ObjectKey:      "tasks/task-a/artifacts/v-new/exports/new.pptx",
		PublishVersion: "v-new",
	}}
	prefix := "tasks/task-a/artifacts/v-new/"

	err = repo.ReplaceArtifactsByObjectKeyPrefix(ctx, "task-a", prefix, replacements)
	if err == nil || !strings.Contains(err.Error(), "persisted artifact") {
		t.Fatalf("ReplaceArtifactsByObjectKeyPrefix() error = %v, want persisted identity drift rejection", err)
	}
	var attemptCount int64
	if err := db.Model(&model.Artifact{}).Where("id = ?", "attempt-a").Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("drifted attempt row count = %d, want transaction rollback", attemptCount)
	}
	for _, id := range []string{unrelatedB.ID, oldA.ID} {
		var count int64
		if err := db.Model(&model.Artifact{}).Where("id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("unrelated/old row %s count = %d, want 1", id, count)
		}
	}
}

func TestDeleteArtifactsByIDsOrObjectKeyPrefixUsesGlobalExactIDsAndTaskScopedPrefix(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()
	artifacts := []*model.Artifact{
		{ID: "moved-attempt", TaskID: "task-b", ObjectKey: "tasks/task-b/artifacts/v-mutated/hijacked.bin"},
		{ID: "unrelated-b", TaskID: "task-b", ObjectKey: "tasks/task-b/artifacts/v-old/keep.bin"},
		{ID: "prefix-a", TaskID: "task-a", ObjectKey: "tasks/task-a/artifacts/v-new/a.bin"},
		{ID: "prefix-b", TaskID: "task-b", ObjectKey: "tasks/task-a/artifacts/v-new/b.bin"},
		{ID: "moved-combined", TaskID: "task-b", ObjectKey: "tasks/task-b/artifacts/v-mutated/combined.bin"},
		{ID: "prefix-combined-a", TaskID: "task-a", ObjectKey: "tasks/task-a/artifacts/v-combined/a.bin"},
		{ID: "prefix-combined-b", TaskID: "task-b", ObjectKey: "tasks/task-a/artifacts/v-combined/b.bin"},
	}
	for _, artifact := range artifacts {
		if err := repo.CreateArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.DeleteArtifactsByIDsOrObjectKeyPrefix(ctx, "task-a", "", []string{"moved-attempt"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteArtifactsByIDsOrObjectKeyPrefix(ctx, "task-a", "tasks/task-a/artifacts/v-new/", nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteArtifactsByIDsOrObjectKeyPrefix(
		ctx,
		"task-a",
		"tasks/task-a/artifacts/v-combined/",
		[]string{"moved-combined"},
	); err != nil {
		t.Fatal(err)
	}

	wantPresent := map[string]bool{
		"moved-attempt":     false,
		"unrelated-b":       true,
		"prefix-a":          false,
		"prefix-b":          true,
		"moved-combined":    false,
		"prefix-combined-a": false,
		"prefix-combined-b": true,
	}
	for id, present := range wantPresent {
		var count int64
		if err := db.Model(&model.Artifact{}).Where("id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if (count == 1) != present {
			t.Fatalf("artifact %s count = %d, want present=%v", id, count, present)
		}
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

func newClaimAttemptRepository(t *testing.T) (*Repository, *model.Task, *model.TaskPhaseRun, *model.TaskRuntimeRun) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskPhaseRun{}, &model.TaskRuntimeRun{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	ctx := context.Background()
	task := &model.Task{ID: "task-attempt-owner", Title: "attempt owner", Status: model.TaskStatusTemplateFillApplying}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now().UTC().Add(-2 * time.Hour)
	claimed, err := repo.ClaimTaskExecution(ctx, task.ID, task.Status, "old-claim", claimedAt, claimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim old worker = %v, %v", claimed, err)
	}
	phaseRun := &model.TaskPhaseRun{
		TaskID:              task.ID,
		Phase:               "template_fill_apply",
		Runner:              "worker",
		Status:              "running",
		ExecutionClaimToken: "old-claim",
		TaskStatus:          task.Status,
		StartedAt:           &claimedAt,
	}
	if err := repo.CreatePhaseRun(ctx, phaseRun); err != nil {
		t.Fatal(err)
	}
	runtimeRun := &model.TaskRuntimeRun{
		TaskID:              task.ID,
		Phase:               "template_fill_apply",
		Command:             "apply",
		Status:              "running",
		ExecutionClaimToken: "old-claim",
		TaskStatus:          task.Status,
		StartedAt:           &claimedAt,
	}
	if err := repo.CreateRuntimeRun(ctx, runtimeRun); err != nil {
		t.Fatal(err)
	}
	if err := repo.DB().Model(&model.Task{}).
		Where("id = ? AND execution_claim_token = ?", task.ID, "old-claim").
		Updates(map[string]any{"execution_claimed_at": claimedAt, "updated_at": claimedAt}).Error; err != nil {
		t.Fatal(err)
	}
	return repo, task, phaseRun, runtimeRun
}

func claimSuccessorForAttemptTest(t *testing.T, repo *Repository, task *model.Task) {
	t.Helper()
	now := time.Now().UTC()
	claimed, err := repo.ClaimTaskExecution(context.Background(), task.ID, task.Status, "successor-claim", now, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("successor did not take over expired claim")
	}
}

func TestClaimTaskExecutionAbandonsRunningPhaseAndRuntimeAttempts(t *testing.T) {
	repo, task, phaseRun, runtimeRun := newClaimAttemptRepository(t)
	claimSuccessorForAttemptTest(t, repo, task)

	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 || phaseRuns[0].ID != phaseRun.ID || phaseRuns[0].Status != "failed" || phaseRuns[0].FinishedAt == nil {
		t.Fatalf("takeover did not abandon phase run: %#v", phaseRuns)
	}
	if !strings.Contains(phaseRuns[0].FailureMetadata, "execution_claim_takeover") {
		t.Fatalf("takeover phase metadata = %q", phaseRuns[0].FailureMetadata)
	}
	runtimeRuns, err := repo.ListRuntimeRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRuns) != 1 || runtimeRuns[0].ID != runtimeRun.ID || runtimeRuns[0].Status != "failed" || runtimeRuns[0].FinishedAt == nil {
		t.Fatalf("takeover did not abandon runtime run: %#v", runtimeRuns)
	}
	if !strings.Contains(runtimeRuns[0].FailureMetadata, "execution_claim_takeover") {
		t.Fatalf("takeover runtime metadata = %q", runtimeRuns[0].FailureMetadata)
	}
}

func TestStaleClaimCannotCreateOwnedAttemptsAfterTakeover(t *testing.T) {
	repo, task, _, _ := newClaimAttemptRepository(t)
	claimSuccessorForAttemptTest(t, repo, task)

	now := time.Now().UTC()
	phaseRun := &model.TaskPhaseRun{
		TaskID:              task.ID,
		Phase:               "template_fill_apply",
		Runner:              "worker",
		Status:              "running",
		ExecutionClaimToken: "old-claim",
		TaskStatus:          task.Status,
		StartedAt:           &now,
	}
	if err := repo.CreatePhaseRun(context.Background(), phaseRun); err == nil {
		t.Fatal("stale phase creation error = nil")
	}
	runtimeRun := &model.TaskRuntimeRun{
		TaskID:              task.ID,
		Phase:               "template_fill_apply",
		Command:             "apply",
		Status:              "running",
		ExecutionClaimToken: "old-claim",
		TaskStatus:          task.Status,
		StartedAt:           &now,
	}
	if err := repo.CreateRuntimeRun(context.Background(), runtimeRun); err == nil {
		t.Fatal("stale runtime creation error = nil")
	}

	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 {
		t.Fatalf("stale worker created phase run: %#v", phaseRuns)
	}
	runtimeRuns, err := repo.ListRuntimeRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRuns) != 1 {
		t.Fatalf("stale worker created runtime run: %#v", runtimeRuns)
	}
}

func TestAbandonedAttemptsRejectStaleCompletion(t *testing.T) {
	repo, task, phaseRun, runtimeRun := newClaimAttemptRepository(t)
	claimSuccessorForAttemptTest(t, repo, task)

	finishedAt := time.Now().UTC()
	phaseRun.Status = "succeeded"
	phaseRun.FinishedAt = &finishedAt
	if err := repo.SavePhaseRun(context.Background(), phaseRun); err == nil {
		t.Fatal("stale phase completion error = nil")
	}
	runtimeRun.Status = "succeeded"
	runtimeRun.FinishedAt = &finishedAt
	if err := repo.SaveRuntimeRun(context.Background(), runtimeRun); err == nil {
		t.Fatal("stale runtime completion error = nil")
	}

	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 || phaseRuns[0].Status != "failed" {
		t.Fatalf("stale worker overwrote abandoned phase evidence: %#v", phaseRuns)
	}
	runtimeRuns, err := repo.ListRuntimeRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRuns) != 1 || runtimeRuns[0].Status != "failed" {
		t.Fatalf("stale worker overwrote abandoned runtime evidence: %#v", runtimeRuns)
	}
}

func TestSaveTaskIfStatusUsesCompareAndSwapAndPreservesExecutionClaim(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskPhaseRun{}, &model.TaskRuntimeRun{}); err != nil {
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
	if err := db.AutoMigrate(&model.Task{}, &model.TaskPhaseRun{}, &model.TaskRuntimeRun{}); err != nil {
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
	if err := db.AutoMigrate(&model.Task{}, &model.TaskPhaseRun{}, &model.TaskRuntimeRun{}); err != nil {
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
