package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type sourceIntakeExpectedArtifact struct {
	Kind      string
	ObjectRel string
}

func TestPublishSourceIntakeArtifactsPublishesAndReplacesPersistedIntake(t *testing.T) {
	ctx := context.Background()
	service, repo, storage := newSourceIntakePublisherTestService(t)
	task := &model.Task{
		ID:     "task-intake",
		Title:  "Source intake",
		Status: model.TaskStatusSourceConverting,
		Route:  model.TaskRouteBeautify,
	}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	uploaded, err := storage.Save(ctx, task.ID, model.ArtifactKindSource, "original.pptx", strings.NewReader("uploaded pptx"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(ctx, &model.Artifact{
		TaskID:    task.ID,
		Kind:      model.ArtifactKindSource,
		Name:      uploaded.Name,
		Storage:   "local",
		ObjectKey: uploaded.ObjectKey,
		Size:      uploaded.Size,
		SHA256:    uploaded.SHA256,
	}); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(t.TempDir(), "project")
	files := map[string]string{
		filepath.Join("sources", "alpha.MD"):                             "# Alpha\n",
		filepath.Join("sources", "bravo.MARKDOWN"):                       "# Bravo\n",
		filepath.Join("sources", "charlie.TXT"):                          "Charlie\n",
		filepath.Join("sources", "delta.TEXT"):                           "Delta\n",
		filepath.Join("sources", "echo.CSV"):                             "name,value\necho,1\n",
		filepath.Join("sources", "foxtrot.TSV"):                          "name\tvalue\nfoxtrot\t1\n",
		filepath.Join("sources", "deck.CONVERSION_PROFILE.JSON"):         `{}`,
		filepath.Join("analysis", "source_profile.JSON"):                 `{"route":"beautify"}`,
		filepath.Join("analysis", "deck.identity.JSON"):                  `{"name":"deck"}`,
		filepath.Join("analysis", "deck.slide_library.JSON"):             `{"slides":[]}`,
		filepath.Join(".slidesmith", "contracts", "source_prepare.json"): `{"schema":"slidesmith.source_prepare_contract.v1"}`,
		filepath.Join(".slidesmith", "source_prepare_contract.json"):     `{"compatibility":true}`,
		filepath.Join("sources", "original.pptx"):                        "prepared pptx",
		filepath.Join("sources", "ignored.json"):                         `{}`,
		filepath.Join("sources", "nested", "hidden.md"):                  "# Nested\n",
		filepath.Join("analysis", "unrelated.json"):                      `{}`,
		filepath.Join("analysis", "nested", "hidden.identity.json"):      `{}`,
	}
	for rel, content := range files {
		mustWriteFile(t, filepath.Join(projectPath, rel), content)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, "sources", "directory.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, "analysis", "directory.identity.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	want := map[string]sourceIntakeExpectedArtifact{
		".slidesmith/contracts/source_prepare.json": {
			Kind:      model.ArtifactKindManifest,
			ObjectRel: "contracts/source_prepare.json",
		},
		"analysis/deck.identity.JSON": {
			Kind:      model.ArtifactKindPPTXIdentity,
			ObjectRel: "analysis/deck.identity.JSON",
		},
		"analysis/deck.slide_library.JSON": {
			Kind:      model.ArtifactKindPPTXSlideLibrary,
			ObjectRel: "analysis/deck.slide_library.JSON",
		},
		"analysis/source_profile.JSON": {
			Kind:      model.ArtifactKindSourceProfile,
			ObjectRel: "analysis/source_profile.JSON",
		},
		"sources/alpha.MD": {
			Kind:      model.ArtifactKindSourceMarkdown,
			ObjectRel: "sources/alpha.MD",
		},
		"sources/bravo.MARKDOWN": {
			Kind:      model.ArtifactKindSourceMarkdown,
			ObjectRel: "sources/bravo.MARKDOWN",
		},
		"sources/charlie.TXT": {
			Kind:      model.ArtifactKindSourceMarkdown,
			ObjectRel: "sources/charlie.TXT",
		},
		"sources/deck.CONVERSION_PROFILE.JSON": {
			Kind:      model.ArtifactKindSourceConversionProfile,
			ObjectRel: "sources/deck.CONVERSION_PROFILE.JSON",
		},
		"sources/delta.TEXT": {
			Kind:      model.ArtifactKindSourceMarkdown,
			ObjectRel: "sources/delta.TEXT",
		},
		"sources/echo.CSV": {
			Kind:      model.ArtifactKindSourceMarkdown,
			ObjectRel: "sources/echo.CSV",
		},
		"sources/foxtrot.TSV": {
			Kind:      model.ArtifactKindSourceMarkdown,
			ObjectRel: "sources/foxtrot.TSV",
		},
	}

	artifacts, err := service.publishSourceIntakeArtifacts(ctx, task, projectPath)
	if err != nil {
		t.Fatalf("publishSourceIntakeArtifacts() error = %v", err)
	}
	assertSourceIntakeArtifacts(t, artifacts, want, task, projectPath, storage)
	assertPersistedSourceIntakeArtifacts(t, repo, task, want, uploaded.ObjectKey)
	assertReturnedSourceIntakeArtifactsMatchPersistence(t, repo, task.ID, artifacts)

	if err := os.Remove(filepath.Join(projectPath, "sources", "bravo.MARKDOWN")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(projectPath, "sources", "golf.txt"), "Golf\n")
	delete(want, "sources/bravo.MARKDOWN")
	want["sources/golf.txt"] = sourceIntakeExpectedArtifact{
		Kind:      model.ArtifactKindSourceMarkdown,
		ObjectRel: "sources/golf.txt",
	}

	retried, err := service.publishSourceIntakeArtifacts(ctx, task, projectPath)
	if err != nil {
		t.Fatalf("publishSourceIntakeArtifacts() retry error = %v", err)
	}
	assertSourceIntakeArtifacts(t, retried, want, task, projectPath, storage)
	assertPersistedSourceIntakeArtifacts(t, repo, task, want, uploaded.ObjectKey)
	assertReturnedSourceIntakeArtifactsMatchPersistence(t, repo, task.ID, retried)
	var removedCount int64
	if err := repo.DB().Model(&model.Artifact{}).
		Where("task_id = ? AND object_key = ?", task.ID, "tasks/task-intake/source-intake/sources/bravo.MARKDOWN").
		Count(&removedCount).Error; err != nil {
		t.Fatal(err)
	}
	if removedCount != 0 {
		t.Fatalf("removed intake artifact rows = %d, want 0", removedCount)
	}
}

func TestPublishSourceIntakeArtifactsValidatesInputs(t *testing.T) {
	service, _, _ := newSourceIntakePublisherTestService(t)
	ctx := context.Background()
	if _, err := service.publishSourceIntakeArtifacts(ctx, nil, t.TempDir()); err == nil {
		t.Fatal("publishSourceIntakeArtifacts() nil task error = nil")
	}
	if _, err := service.publishSourceIntakeArtifacts(ctx, &model.Task{ID: "task-1"}, ""); err == nil {
		t.Fatal("publishSourceIntakeArtifacts() empty project path error = nil")
	}
}

func TestPublishSourceIntakeArtifactsRollsBackObjectsWhenRepositoryReplaceFails(t *testing.T) {
	ctx := context.Background()
	service, repo, storage := newSourceIntakePublisherTestService(t)
	task := &model.Task{ID: "task-intake-db-rollback", Route: model.TaskRouteMain}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(t.TempDir(), "project")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "alpha.md"), "old alpha\n")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "bravo.md"), "old bravo\n")
	initial, err := service.publishSourceIntakeArtifacts(ctx, task, projectPath)
	if err != nil {
		t.Fatalf("initial publishSourceIntakeArtifacts() error = %v", err)
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "source-intake")) + "/"
	if err := os.Chmod(storage.Path(prefix+"sources/alpha.md"), 0o640); err != nil {
		t.Fatal(err)
	}
	objectKeys := make([]string, 0, len(initial)+1)
	for _, artifact := range initial {
		objectKeys = append(objectKeys, artifact.ObjectKey)
	}
	objectKeys = append(objectKeys, prefix+"sources/charlie.md")
	wantObjects := captureSourceIntakeObjectStates(t, storage, objectKeys)
	wantRows := loadPersistedSourceIntakeRows(t, repo, task.ID, prefix)

	mustWriteFile(t, filepath.Join(projectPath, "sources", "alpha.md"), "new alpha\n")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "bravo.md"), "new bravo\n")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "charlie.md"), "new charlie\n")
	if err := repo.DB().Exec(`
		CREATE TRIGGER fail_source_intake_retry_insert
		BEFORE INSERT ON artifacts
		WHEN NEW.object_key LIKE '%/source-intake/%'
		BEGIN
			SELECT RAISE(ABORT, 'forced source intake retry failure');
		END;
	`).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := service.publishSourceIntakeArtifacts(ctx, task, projectPath); err == nil {
		t.Fatal("publishSourceIntakeArtifacts() retry error = nil, want repository failure")
	}
	assertPersistedSourceIntakeRowsEqual(t, repo, task.ID, prefix, wantRows)
	assertSourceIntakeObjectStates(t, storage, wantObjects)
}

type sourceIntakePartialCopyFailStorage struct {
	*LocalStorage
	callCount int
	failAt    int
}

func (s *sourceIntakePartialCopyFailStorage) CopyFileToObject(ctx context.Context, objectKey, sourcePath string) (*StoredObject, error) {
	s.callCount++
	if s.callCount != s.failAt {
		return s.LocalStorage.CopyFileToObject(ctx, objectKey, sourcePath)
	}
	targetPath := s.Path(objectKey)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(targetPath, []byte("partial replacement"), 0o600); err != nil {
		return nil, err
	}
	if err := os.Chmod(targetPath, 0o600); err != nil {
		return nil, err
	}
	return nil, errors.New("forced partial source intake copy failure")
}

func TestPublishSourceIntakeArtifactsRollsBackObjectsWhenCopyFailsMidPublish(t *testing.T) {
	ctx := context.Background()
	service, repo, storage := newSourceIntakePublisherTestService(t)
	task := &model.Task{ID: "task-intake-copy-rollback", Route: model.TaskRouteMain}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	projectPath := filepath.Join(t.TempDir(), "project")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "alpha.md"), "old alpha\n")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "bravo.md"), "old bravo\n")
	initial, err := service.publishSourceIntakeArtifacts(ctx, task, projectPath)
	if err != nil {
		t.Fatalf("initial publishSourceIntakeArtifacts() error = %v", err)
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "source-intake")) + "/"
	if err := os.Chmod(storage.Path(prefix+"sources/bravo.md"), 0o640); err != nil {
		t.Fatal(err)
	}
	objectKeys := make([]string, 0, len(initial))
	for _, artifact := range initial {
		objectKeys = append(objectKeys, artifact.ObjectKey)
	}
	wantObjects := captureSourceIntakeObjectStates(t, storage, objectKeys)
	wantRows := loadPersistedSourceIntakeRows(t, repo, task.ID, prefix)

	mustWriteFile(t, filepath.Join(projectPath, "sources", "alpha.md"), "new alpha\n")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "bravo.md"), "new bravo\n")
	service.storage = &sourceIntakePartialCopyFailStorage{LocalStorage: storage, failAt: 2}

	if _, err := service.publishSourceIntakeArtifacts(ctx, task, projectPath); err == nil {
		t.Fatal("publishSourceIntakeArtifacts() retry error = nil, want copy failure")
	}
	assertPersistedSourceIntakeRowsEqual(t, repo, task.ID, prefix, wantRows)
	assertSourceIntakeObjectStates(t, storage, wantObjects)
}

func TestPublishSourceIntakeArtifactsCompletesPrepareWithCount(t *testing.T) {
	service, repo, task, _ := templateResolvePrepareService(t)
	ctx := context.Background()

	if err := service.processPrepare(ctx, task); err != nil {
		t.Fatalf("processPrepare() error = %v", err)
	}
	phaseRuns, err := repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase != string(PhaseSourcePrepare) {
			continue
		}
		if phaseRun.Status != PhaseRunStatusSucceeded {
			t.Fatalf("source_prepare status = %q, want succeeded", phaseRun.Status)
		}
		var output struct {
			SourceContract            map[string]any `json:"source_contract"`
			SourceIntakeArtifactCount int            `json:"source_intake_artifact_count"`
		}
		if err := json.Unmarshal([]byte(phaseRun.OutputJSON), &output); err != nil {
			t.Fatalf("invalid source_prepare output: %v", err)
		}
		if output.SourceContract == nil {
			t.Fatalf("source_prepare output missing source_contract: %s", phaseRun.OutputJSON)
		}
		if output.SourceIntakeArtifactCount != 2 {
			t.Fatalf("source_intake_artifact_count = %d, want 2; output=%s", output.SourceIntakeArtifactCount, phaseRun.OutputJSON)
		}
		return
	}
	t.Fatal("source_prepare phase run not found")
}

type sourceIntakeCopyFailStorage struct {
	*LocalStorage
}

func (s *sourceIntakeCopyFailStorage) CopyFileToObject(context.Context, string, string) (*StoredObject, error) {
	return nil, errors.New("forced source intake copy failure")
}

func TestPublishSourceIntakeArtifactsFailureFailsPreparePhase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*TaskService, *repository.Repository)
	}{
		{
			name: "copy",
			setup: func(service *TaskService, _ *repository.Repository) {
				storage, ok := service.storage.(*LocalStorage)
				if !ok {
					t.Fatalf("storage type = %T, want *LocalStorage", service.storage)
				}
				service.storage = &sourceIntakeCopyFailStorage{LocalStorage: storage}
			},
		},
		{
			name: "repository",
			setup: func(_ *TaskService, repo *repository.Repository) {
				if err := repo.DB().Exec(`
					CREATE TRIGGER fail_source_intake_insert
					BEFORE INSERT ON artifacts
					WHEN NEW.object_key LIKE '%/source-intake/%'
					BEGIN
						SELECT RAISE(ABORT, 'forced source intake repository failure');
					END;
				`).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, workspaceRoot := templateResolvePrepareService(t)
			test.setup(service, repo)

			err := service.processPrepare(context.Background(), task)
			if err == nil {
				t.Fatal("processPrepare() error = nil, want source intake publish failure")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusFailed {
				t.Fatalf("status = %q, want failed", updated.Status)
			}
			if updated.FailurePhase != "source_prepare.publish_intake" {
				t.Fatalf("failure phase = %q, want source_prepare.publish_intake", updated.FailurePhase)
			}
			var metadata map[string]any
			if err := json.Unmarshal([]byte(updated.FailureMetadata), &metadata); err != nil {
				t.Fatalf("invalid failure metadata: %v", err)
			}
			wantProjectPath := filepath.Join(workspaceRoot, task.RuntimeProject, "projects", "task_template_ppt169_20260708")
			for key, want := range map[string]string{
				"workspace_path": filepath.Join(workspaceRoot, task.RuntimeProject),
				"project_path":   wantProjectPath,
				"route":          model.TaskRouteMain,
			} {
				if metadata[key] != want {
					t.Fatalf("metadata[%q] = %#v, want %q; metadata=%#v", key, metadata[key], want, metadata)
				}
			}
			phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, phaseRun := range phaseRuns {
				if phaseRun.Phase == string(PhaseSourcePrepare) {
					if phaseRun.Status != PhaseRunStatusFailed {
						t.Fatalf("source_prepare status = %q, want failed", phaseRun.Status)
					}
					return
				}
			}
			t.Fatal("source_prepare phase run not found")
		})
	}
}

func newSourceIntakePublisherTestService(t *testing.T) (*TaskService, *repository.Repository, *LocalStorage) {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "source-intake.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	return &TaskService{repo: repo, storage: storage}, repo, storage
}

func assertSourceIntakeArtifacts(
	t *testing.T,
	artifacts []model.Artifact,
	want map[string]sourceIntakeExpectedArtifact,
	task *model.Task,
	projectPath string,
	storage *LocalStorage,
) {
	t.Helper()
	wantPaths := make([]string, 0, len(want))
	for path := range want {
		wantPaths = append(wantPaths, path)
	}
	sort.Strings(wantPaths)
	if len(artifacts) != len(wantPaths) {
		t.Fatalf("artifact count = %d, want %d: %#v", len(artifacts), len(wantPaths), artifacts)
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "source-intake")) + "/"
	for i, sourceRel := range wantPaths {
		artifact := artifacts[i]
		expected := want[sourceRel]
		wantObjectKey := prefix + expected.ObjectRel
		if artifact.ObjectKey != wantObjectKey || artifact.Kind != expected.Kind {
			t.Fatalf("artifact[%d] = {object=%q kind=%q}, want {object=%q kind=%q}", i, artifact.ObjectKey, artifact.Kind, wantObjectKey, expected.Kind)
		}
		if artifact.TaskID != task.ID || artifact.Storage != "local" {
			t.Fatalf("artifact[%d] identity = %#v", i, artifact)
		}
		if artifact.PublishVersion != "" {
			t.Fatalf("artifact[%d] publish version = %q, want empty", i, artifact.PublishVersion)
		}
		var metadata map[string]string
		if err := json.Unmarshal([]byte(artifact.MetadataJSON), &metadata); err != nil {
			t.Fatalf("artifact[%d] metadata is invalid: %v", i, err)
		}
		wantMetadata := map[string]string{
			"schema":                "slidesmith.source_intake_artifact_metadata.v1",
			"source_phase":          "source_prepare",
			"project_relative_path": sourceRel,
			"route":                 task.Route,
		}
		if !reflect.DeepEqual(metadata, wantMetadata) {
			t.Fatalf("artifact[%d] metadata = %#v, want %#v", i, metadata, wantMetadata)
		}
		stored, err := os.ReadFile(storage.Path(artifact.ObjectKey))
		if err != nil {
			t.Fatalf("read stored artifact %q: %v", artifact.ObjectKey, err)
		}
		source, err := os.ReadFile(filepath.Join(projectPath, filepath.FromSlash(sourceRel)))
		if err != nil {
			t.Fatalf("read source artifact %q: %v", sourceRel, err)
		}
		if !reflect.DeepEqual(stored, source) {
			t.Fatalf("stored artifact %q differs from source %q", artifact.ObjectKey, sourceRel)
		}
	}
}

func assertPersistedSourceIntakeArtifacts(
	t *testing.T,
	repo *repository.Repository,
	task *model.Task,
	want map[string]sourceIntakeExpectedArtifact,
	uploadObjectKey string,
) {
	t.Helper()
	var persisted []model.Artifact
	if err := repo.DB().Where("task_id = ?", task.ID).Order("object_key ASC").Find(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if len(persisted) != len(want)+1 {
		t.Fatalf("persisted artifact count = %d, want %d: %#v", len(persisted), len(want)+1, persisted)
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "source-intake")) + "/"
	wantSourceRelByObject := make(map[string]string, len(want))
	for sourceRel, expected := range want {
		wantSourceRelByObject[prefix+expected.ObjectRel] = sourceRel
	}
	countByObject := make(map[string]int, len(persisted))
	for _, artifact := range persisted {
		countByObject[artifact.ObjectKey]++
		if artifact.ObjectKey == uploadObjectKey {
			continue
		}
		sourceRel, ok := wantSourceRelByObject[artifact.ObjectKey]
		if !ok {
			t.Fatalf("unexpected persisted intake artifact: %#v", artifact)
		}
		if artifact.Kind != want[sourceRel].Kind {
			t.Fatalf("persisted artifact %q kind = %q, want %q", artifact.ObjectKey, artifact.Kind, want[sourceRel].Kind)
		}
		if artifact.PublishVersion != "" {
			t.Fatalf("persisted intake publish version = %q, want empty: %#v", artifact.PublishVersion, artifact)
		}
		var metadata map[string]string
		if err := json.Unmarshal([]byte(artifact.MetadataJSON), &metadata); err != nil {
			t.Fatalf("persisted artifact %q metadata is invalid: %v", artifact.ObjectKey, err)
		}
		wantMetadata := map[string]string{
			"schema":                "slidesmith.source_intake_artifact_metadata.v1",
			"source_phase":          "source_prepare",
			"project_relative_path": sourceRel,
			"route":                 task.Route,
		}
		if !reflect.DeepEqual(metadata, wantMetadata) {
			t.Fatalf("persisted artifact %q metadata = %#v, want %#v", artifact.ObjectKey, metadata, wantMetadata)
		}
	}
	if countByObject[uploadObjectKey] != 1 {
		t.Fatalf("uploaded source count = %d, want 1; persisted=%#v", countByObject[uploadObjectKey], persisted)
	}
	for _, expected := range want {
		objectKey := prefix + expected.ObjectRel
		if countByObject[objectKey] != 1 {
			t.Fatalf("persisted object %q count = %d, want 1; persisted=%#v", objectKey, countByObject[objectKey], persisted)
		}
	}
}

type sourceIntakeObjectState struct {
	Bytes  []byte
	Mode   os.FileMode
	Exists bool
}

func captureSourceIntakeObjectStates(t *testing.T, storage *LocalStorage, objectKeys []string) map[string]sourceIntakeObjectState {
	t.Helper()
	states := make(map[string]sourceIntakeObjectState, len(objectKeys))
	for _, objectKey := range objectKeys {
		path := storage.Path(objectKey)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			states[objectKey] = sourceIntakeObjectState{}
			continue
		}
		if err != nil {
			t.Fatalf("inspect object %q: %v", objectKey, err)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read object %q: %v", objectKey, err)
		}
		states[objectKey] = sourceIntakeObjectState{
			Bytes:  contents,
			Mode:   info.Mode(),
			Exists: true,
		}
	}
	return states
}

func assertSourceIntakeObjectStates(t *testing.T, storage *LocalStorage, want map[string]sourceIntakeObjectState) {
	t.Helper()
	objectKeys := make([]string, 0, len(want))
	for objectKey := range want {
		objectKeys = append(objectKeys, objectKey)
	}
	sort.Strings(objectKeys)
	for _, objectKey := range objectKeys {
		got := captureSourceIntakeObjectStates(t, storage, []string{objectKey})[objectKey]
		if !reflect.DeepEqual(got, want[objectKey]) {
			t.Fatalf("object %q state = %#v, want %#v", objectKey, got, want[objectKey])
		}
	}
}

func loadPersistedSourceIntakeRows(t *testing.T, repo *repository.Repository, taskID, prefix string) []model.Artifact {
	t.Helper()
	var artifacts []model.Artifact
	if err := repo.DB().
		Where("task_id = ? AND object_key LIKE ?", taskID, prefix+"%").
		Order("object_key ASC").
		Find(&artifacts).Error; err != nil {
		t.Fatal(err)
	}
	return artifacts
}

func assertPersistedSourceIntakeRowsEqual(
	t *testing.T,
	repo *repository.Repository,
	taskID string,
	prefix string,
	want []model.Artifact,
) {
	t.Helper()
	got := loadPersistedSourceIntakeRows(t, repo, taskID, prefix)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted source intake rows changed after failed publish:\n got: %#v\nwant: %#v", got, want)
	}
}

func assertReturnedSourceIntakeArtifactsMatchPersistence(
	t *testing.T,
	repo *repository.Repository,
	taskID string,
	returned []model.Artifact,
) {
	t.Helper()
	prefix := filepath.ToSlash(filepath.Join("tasks", taskID, "source-intake")) + "/"
	persisted := loadPersistedSourceIntakeRows(t, repo, taskID, prefix)
	if len(returned) != len(persisted) {
		t.Fatalf("returned artifact count = %d, persisted count = %d", len(returned), len(persisted))
	}
	returnedByObjectKey := make(map[string]model.Artifact, len(returned))
	for _, artifact := range returned {
		if artifact.ID == "" || artifact.CreatedAt.IsZero() || artifact.UpdatedAt.IsZero() {
			t.Fatalf("returned artifact lacks persisted identity: %#v", artifact)
		}
		returnedByObjectKey[artifact.ObjectKey] = artifact
	}
	for _, artifact := range persisted {
		returnedArtifact, ok := returnedByObjectKey[artifact.ObjectKey]
		if !ok {
			t.Fatalf("persisted artifact %q missing from return value", artifact.ObjectKey)
		}
		if !reflect.DeepEqual(returnedArtifact, artifact) {
			t.Fatalf("returned artifact %q = %#v, persisted = %#v", artifact.ObjectKey, returnedArtifact, artifact)
		}
	}
}
