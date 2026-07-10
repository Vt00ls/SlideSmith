package service

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type sourceUploadTrackingStorage struct {
	*LocalStorage
	saveCalls int
}

func (s *sourceUploadTrackingStorage) Save(ctx context.Context, taskID, kind, filename string, reader io.Reader) (*StoredObject, error) {
	s.saveCalls++
	return s.LocalStorage.Save(ctx, taskID, kind, filename, reader)
}

func TestUploadFilePersistsPPTXMetadataAndPreservesUploadEvents(t *testing.T) {
	service, repo, task, storage := sourceUploadTestService(t)
	ctx := context.Background()

	artifact, err := service.UploadFile(ctx, task.ID, "Quarterly.PPTX", strings.NewReader("pptx fixture"))
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if storage.saveCalls != 1 {
		t.Fatalf("storage Save calls = %d, want 1", storage.saveCalls)
	}
	if _, err := os.Stat(storage.Path(artifact.ObjectKey)); err != nil {
		t.Fatalf("stored source missing: %v", err)
	}

	var metadata struct {
		Schema     string     `json:"schema"`
		SourceKind SourceKind `json:"source_kind"`
		Extension  string     `json:"extension"`
		Supported  bool       `json:"supported"`
		Intake     struct {
			Markdown     bool `json:"markdown"`
			PPTXAnalysis bool `json:"pptx_analysis"`
		} `json:"intake"`
	}
	if err := json.Unmarshal([]byte(artifact.MetadataJSON), &metadata); err != nil {
		t.Fatalf("artifact metadata is not valid JSON: %v", err)
	}
	if metadata.Schema != "slidesmith.source_artifact_metadata.v1" || metadata.SourceKind != SourceKindPresentation || metadata.Extension != "pptx" || !metadata.Supported {
		t.Fatalf("artifact metadata identity = %#v", metadata)
	}
	if !metadata.Intake.Markdown || !metadata.Intake.PPTXAnalysis {
		t.Fatalf("artifact metadata intake = %#v, want markdown and pptx analysis", metadata.Intake)
	}

	persisted, err := repo.GetArtifact(ctx, task.ID, artifact.ID)
	if err != nil {
		t.Fatalf("GetArtifact() error = %v", err)
	}
	if persisted.MetadataJSON != artifact.MetadataJSON {
		t.Fatalf("persisted metadata = %q, want %q", persisted.MetadataJSON, artifact.MetadataJSON)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusUploaded {
		t.Fatalf("task status = %q, want %q", updated.Status, model.TaskStatusUploaded)
	}
	events, err := repo.ListEvents(ctx, task.ID, 0, 20)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v, want upload transition and artifact events", events)
	}
	if events[0].Type != model.EventTypeStatus || events[0].Status != model.TaskStatusUploaded || events[0].Message != "Source uploaded" {
		t.Fatalf("upload transition event = %#v", events[0])
	}
	if events[1].Type != model.EventTypeArtifact || events[1].Status != model.ArtifactKindSource || events[1].Message != "Source artifact stored" {
		t.Fatalf("artifact event = %#v", events[1])
	}
}

func TestUploadFileRejectsUnsupportedBeforeStorageAndArtifactCreation(t *testing.T) {
	service, repo, task, storage := sourceUploadTestService(t)
	ctx := context.Background()

	artifact, err := service.UploadFile(ctx, task.ID, "legacy.ppt", strings.NewReader("legacy ppt fixture"))
	if err == nil {
		t.Fatal("UploadFile() error = nil, want unsupported source error")
	}
	if artifact != nil {
		t.Fatalf("UploadFile() artifact = %#v, want nil", artifact)
	}
	if !strings.Contains(strings.ToLower(err.Error()), ".pptx") {
		t.Fatalf("UploadFile() error = %q, want resave-as-pptx guidance", err)
	}
	if storage.saveCalls != 0 {
		t.Fatalf("storage Save calls = %d, want 0", storage.saveCalls)
	}
	if _, statErr := os.Stat(storage.Root()); !os.IsNotExist(statErr) {
		t.Fatalf("storage root stat error = %v, want not exist", statErr)
	}
	var artifactCount int64
	if err := repo.DB().Model(&model.Artifact{}).Where("task_id = ?", task.ID).Count(&artifactCount).Error; err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if artifactCount != 0 {
		t.Fatalf("artifact count = %d, want 0", artifactCount)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusCreated {
		t.Fatalf("task status = %q, want %q", updated.Status, model.TaskStatusCreated)
	}
	events, err := repo.ListEvents(ctx, task.ID, 0, 20)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func sourceUploadTestService(t *testing.T) (*TaskService, *repository.Repository, *model.Task, *sourceUploadTrackingStorage) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskEvent{}, &model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	task := &model.Task{
		ID:             "task-source-upload",
		Title:          "Source upload",
		Status:         model.TaskStatusCreated,
		RuntimeProject: "task_source_upload",
	}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	storage := &sourceUploadTrackingStorage{LocalStorage: NewLocalStorage(filepath.Join(t.TempDir(), "storage"))}
	service := NewTaskService(repo, storage, nil, nil, config.AgentComposeConfig{})
	return service, repo, task, storage
}
