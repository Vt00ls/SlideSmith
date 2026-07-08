package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

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
