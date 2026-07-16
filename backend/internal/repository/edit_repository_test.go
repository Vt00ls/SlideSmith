package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

func newEditRepositoryTest(t *testing.T) *Repository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.Task{}, &model.Artifact{}, &model.TaskArtifactVersion{}, &model.TaskEditSession{}, &model.TaskEditRun{}); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func activeVersionForTest(t *testing.T, repo *Repository, taskID, version, manifest string, activatedAt time.Time) *model.TaskArtifactVersion {
	t.Helper()
	if _, err := repo.GetTask(context.Background(), taskID); errors.Is(err, ErrNotFound) {
		if err := repo.CreateTask(context.Background(), &model.Task{ID: taskID, Title: taskID, Status: model.TaskStatusCompleted}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	row := &model.TaskArtifactVersion{
		TaskID: taskID, Version: version, Source: model.ArtifactVersionSourceGeneration,
		ArtifactManifestSHA256: manifest, ActivatedAt: &activatedAt,
	}
	if err := repo.UpsertActiveArtifactVersion(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestArtifactVersionRegistryOrdersActiveAndHidesStaging(t *testing.T) {
	repo := newEditRepositoryTest(t)
	ctx := context.Background()
	oldTime := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(time.Hour)
	activeVersionForTest(t, repo, "task-a", "v1", "manifest-v1", oldTime)
	activeVersionForTest(t, repo, "task-a", "v2", "manifest-v2", newTime)
	if err := repo.CreateArtifactVersion(ctx, &model.TaskArtifactVersion{
		TaskID: "task-a", Version: "v3", Status: model.ArtifactVersionStatusStaging,
		Source: model.ArtifactVersionSourceManualEdit, ArtifactManifestSHA256: "manifest-v3",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListArtifactVersions(ctx, "task-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Version != "v2" || rows[1].Version != "v1" {
		t.Fatalf("active versions = %#v", rows)
	}
	if _, err := repo.GetArtifactVersion(ctx, "task-a", "v3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("staging GetArtifactVersion() error = %v, want not found", err)
	}
	if _, err := repo.GetArtifactVersion(ctx, "task-b", "v2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-task GetArtifactVersion() error = %v, want not found", err)
	}
}

func TestListArtifactsUsesRegistryLatestRatherThanArtifactCreatedAt(t *testing.T) {
	repo := newEditRepositoryTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	activeVersionForTest(t, repo, "task-a", "v1", "manifest-v1", now)
	activeVersionForTest(t, repo, "task-a", "v2", "manifest-v2", now.Add(time.Hour))
	for _, artifact := range []*model.Artifact{
		{ID: "v1-newer-row", TaskID: "task-a", Kind: model.ArtifactKindPPTX, ObjectKey: "tasks/task-a/artifacts/v1/v1.pptx", PublishVersion: "v1", CreatedAt: now.Add(4 * time.Hour)},
		{ID: "v2-older-row", TaskID: "task-a", Kind: model.ArtifactKindPPTX, ObjectKey: "tasks/task-a/artifacts/v2/v2.pptx", PublishVersion: "v2", CreatedAt: now},
	} {
		if err := repo.CreateArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := repo.ListArtifacts(ctx, "task-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PublishVersion != "v2" {
		t.Fatalf("latest artifacts = %#v, want registry v2", rows)
	}
	pptx, err := repo.LatestPPTXArtifact(ctx, "task-a")
	if err != nil || pptx.PublishVersion != "v2" {
		t.Fatalf("LatestPPTXArtifact() = %#v, %v", pptx, err)
	}
}

func TestEditSessionCreationCASFreezeAndClaim(t *testing.T) {
	repo := newEditRepositoryTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	activeVersionForTest(t, repo, "task-a", "v1", "manifest-v1", now)
	session := &model.TaskEditSession{
		TaskID: "task-a", BasePublishVersion: "v1", BaseArtifactManifestSHA256: "manifest-v1",
		BaseSVGInventorySHA256: "svg-v1", DraftJSON: `{}`, DraftSHA256: "draft-1",
	}
	if err := repo.CreateEditSession(ctx, session, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateEditSession(ctx, &model.TaskEditSession{
		TaskID: "task-a", BasePublishVersion: "v1", BaseArtifactManifestSHA256: "manifest-v1",
		BaseSVGInventorySHA256: "svg-v1", DraftSHA256: "other",
	}, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active session error = %v, want conflict", err)
	}
	updated, err := repo.SaveEditSessionDraft(ctx, "task-a", session.ID, 1, `{"pages":[]}`, "draft-2")
	if err != nil || updated.Revision != 2 || updated.DraftSHA256 != "draft-2" {
		t.Fatalf("SaveEditSessionDraft() = %#v, %v", updated, err)
	}
	if _, err := repo.SaveEditSessionDraft(ctx, "task-a", session.ID, 1, `{}`, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale draft save error = %v, want conflict", err)
	}
	frozen, err := repo.FreezeEditSession(ctx, "task-a", session.ID, 2, "draft-2")
	if err != nil || frozen.Status != model.EditSessionStatusQueued || frozen.FrozenRevision != 2 {
		t.Fatalf("FreezeEditSession() = %#v, %v", frozen, err)
	}
	idempotent, err := repo.FreezeEditSession(ctx, "task-a", session.ID, 2, "draft-2")
	if err != nil || idempotent.Status != model.EditSessionStatusQueued {
		t.Fatalf("idempotent FreezeEditSession() = %#v, %v", idempotent, err)
	}
	if _, err := repo.SaveEditSessionDraft(ctx, "task-a", session.ID, 2, `{}`, "late"); !errors.Is(err, ErrLocked) {
		t.Fatalf("frozen draft save error = %v, want locked", err)
	}
	claimedAt := time.Now().UTC()
	claimed, err := repo.ClaimEditSession(ctx, "task-a", session.ID, "owner", claimedAt, claimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("ClaimEditSession() = %v, %v", claimed, err)
	}
	claimed, err = repo.ClaimEditSession(ctx, "task-a", session.ID, "other", claimedAt, claimedAt.Add(-time.Hour))
	if err != nil || claimed {
		t.Fatalf("second ClaimEditSession() = %v, %v", claimed, err)
	}
}

func TestFreezeEditSessionRejectsStaleParentAndKeepsLatest(t *testing.T) {
	repo := newEditRepositoryTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	activeVersionForTest(t, repo, "task-a", "v1", "manifest-v1", now)
	session := &model.TaskEditSession{
		TaskID: "task-a", BasePublishVersion: "v1", BaseArtifactManifestSHA256: "manifest-v1",
		BaseSVGInventorySHA256: "svg-v1", DraftSHA256: "draft-v1",
	}
	if err := repo.CreateEditSession(ctx, session, 1); err != nil {
		t.Fatal(err)
	}
	activeVersionForTest(t, repo, "task-a", "v2", "manifest-v2", now.Add(time.Hour))
	if _, err := repo.FreezeEditSession(ctx, "task-a", session.ID, 1, "draft-v1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale FreezeEditSession() error = %v, want conflict", err)
	}
	persisted, err := repo.GetEditSession(ctx, "task-a", session.ID)
	if err != nil || persisted.Status != model.EditSessionStatusStale {
		t.Fatalf("stale session = %#v, %v", persisted, err)
	}
}
