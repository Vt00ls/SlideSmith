package service

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

func profileTestService(t *testing.T, cfg config.AgentComposeConfig) (*TaskService, *repository.Repository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskEvent{}, &model.TaskPhaseRun{}, &model.TaskRuntimeRun{}, &model.TaskConfirmation{}, &model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	storage := NewLocalStorage(t.TempDir())
	return NewTaskService(repo, storage, nil, NewRuntimeWorkspacePublisher(storage), cfg), repo
}

func TestStartEnabledBeautifyLocksFullProfileBeforeFormalRouteSelect(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{
		RunnerProfile:         model.RunnerProfileFullPPTMaster,
		FullPPTDefaultEnabled: false,
		BeautifyEnabled:       true,
	})
	task := &model.Task{ID: "profile-beautify", Title: "请美化 PPTX，保留页数和文字", Status: model.TaskStatusUploaded, Route: model.TaskRouteMain}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(context.Background(), &model.Artifact{TaskID: task.ID, Kind: model.ArtifactKindSource, Name: "source.pptx", ObjectKey: "tasks/profile-beautify/source/source.pptx", Storage: "local"}); err != nil {
		t.Fatal(err)
	}
	started, err := service.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.RunnerProfile != model.RunnerProfileFullPPTMaster {
		t.Fatalf("Beautify runner profile = %q", started.RunnerProfile)
	}
}

func TestStartTaskLocksFullProfileAndConfigurationChangesDoNotOverwriteIt(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{
		RunnerProfile:          "full",
		RunnerProfileExplicit:  false,
		FullPPTDefaultEnabled:  true,
		FullPPTPreflightStrict: true,
	})
	task := &model.Task{ID: "profile-start", Title: "Profile", Status: model.TaskStatusUploaded, Route: model.TaskRouteMain}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	started, err := service.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.RunnerProfile != model.RunnerProfileFullPPTMaster || started.RunnerProfileSource != model.RunnerProfileSourceDeploymentDefault || started.RunnerProfileLockedAt == nil {
		t.Fatalf("started profile lock = %#v", started)
	}
	service.agentCfg.RunnerProfile = model.RunnerProfileRealLite
	service.agentCfg.RunnerProfileExplicit = true
	service.agentCfg.FullPPTDefaultEnabled = false
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ensureTaskRunnerProfile(context.Background(), persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.RunnerProfile != model.RunnerProfileFullPPTMaster {
		t.Fatalf("configuration change overwrote locked profile: %#v", persisted)
	}
}

func TestClosedFullRolloutGateLocksNewTaskToRealLite(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{RunnerProfile: model.RunnerProfileFullPPTMaster})
	task := &model.Task{ID: "profile-gated", Title: "Profile", Status: model.TaskStatusUploaded, Route: model.TaskRouteMain}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	started, err := service.StartTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.RunnerProfile != model.RunnerProfileRealLite {
		t.Fatalf("gated profile = %q, want real-lite", started.RunnerProfile)
	}
}

func TestLegacyFullPhaseEvidenceBackfillsProfile(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{RunnerProfile: model.RunnerProfileRealLite})
	task := &model.Task{ID: "legacy-evidence", Title: "Legacy", Status: model.TaskStatusSVGGenerating, Route: model.TaskRouteMain}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePhaseRun(context.Background(), &model.TaskPhaseRun{
		TaskID: task.ID,
		Phase:  string(PhaseSpecGenerate),
		Runner: PhaseRunnerAgent,
		Status: PhaseRunStatusSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ensureTaskRunnerProfile(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if task.RunnerProfile != model.RunnerProfileFullPPTMaster || task.RunnerProfileSource != model.RunnerProfileSourceLegacyEvidence {
		t.Fatalf("legacy evidence profile = %#v", task)
	}
}

func TestLegacyRuntimeGenerateEvidenceBackfillsRealLite(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{RunnerProfile: model.RunnerProfileFullPPTMaster, FullPPTDefaultEnabled: true})
	task := &model.Task{ID: "legacy-runtime", Title: "Legacy", Status: model.TaskStatusPublishing, Route: model.TaskRouteMain}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRuntimeRun(context.Background(), &model.TaskRuntimeRun{
		TaskID:  task.ID,
		Phase:   "generate",
		Command: "node workflows/ppt_workflow.js generate --profile real-lite",
		Status:  "succeeded",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ensureTaskRunnerProfile(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if task.RunnerProfile != model.RunnerProfileRealLite || task.RunnerProfileSource != model.RunnerProfileSourceLegacyEvidence {
		t.Fatalf("legacy runtime evidence profile = %#v", task)
	}
}

func TestLegacyAdvancedTaskWithoutEvidenceRequiresMigration(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{RunnerProfile: model.RunnerProfileFullPPTMaster, FullPPTDefaultEnabled: true})
	task := &model.Task{ID: "legacy-unknown", Title: "Legacy", Status: model.TaskStatusAwaitingAnchorConfirm, Route: model.TaskRouteMain}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	err := service.ensureTaskRunnerProfile(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), failurePhaseRuntimeProfileMigrationRequired) {
		t.Fatalf("migration error = %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RunnerProfile != "" {
		t.Fatalf("ambiguous legacy task was guessed: %#v", persisted)
	}
}

func TestRuntimeManifestProfileMismatchBlocksExecution(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{WorkspaceRoot: t.TempDir()})
	task := &model.Task{ID: "profile-mismatch", Title: "Mismatch", Status: model.TaskStatusSpecGenerating, RuntimeProject: "profile_mismatch", Route: model.TaskRouteMain}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	writeRuntimeProfileManifestForTest(t, service.agentCfg.WorkspaceRoot, &model.Task{
		ID:                    task.ID,
		RuntimeProject:        task.RuntimeProject,
		Route:                 task.Route,
		RunnerProfile:         model.RunnerProfileRealLite,
		RunnerProfileSource:   task.RunnerProfileSource,
		RunnerProfileLockedAt: task.RunnerProfileLockedAt,
	})
	err := service.validateTaskRuntimeProfile(task, service.resolveTaskWorkspace(task))
	if err == nil || !strings.Contains(err.Error(), failurePhaseRuntimeProfileMismatch) {
		t.Fatalf("manifest mismatch error = %v", err)
	}
}
