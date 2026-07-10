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
