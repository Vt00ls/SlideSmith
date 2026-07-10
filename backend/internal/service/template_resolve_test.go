package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type templateResolvePrepareAgent struct{}

func (a templateResolvePrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a templateResolvePrepareAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	project := filepath.Join(req.WorkDir, "projects", "task_template_ppt169_20260708")
	mustWriteFileNoTest(project, filepath.Join("sources", "input.md"), "# Source\n")
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-prepare",
		SessionID:     "session-prepare",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: req.WorkDir,
	}, nil
}

func TestProcessPrepareRecordsTemplateResolvePhase(t *testing.T) {
	service, repo, task, workspaceRoot := templateResolvePrepareService(t)
	ctx := context.Background()

	if err := service.processPrepare(ctx, task); err != nil {
		t.Fatalf("processPrepare() error = %v", err)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusAwaitingAnchorConfirm {
		t.Fatalf("status = %q, want awaiting_anchor_confirm", updated.Status)
	}
	if updated.Route != model.TaskRouteMain {
		t.Fatalf("route = %q, want %q", updated.Route, model.TaskRouteMain)
	}
	if updated.RouteSelectionJSON == "" || updated.RouteSelectionJSON == "{}" {
		t.Fatalf("route selection was not persisted")
	}

	phaseRuns, err := repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	statusByPhase := map[string]string{}
	outputByPhase := map[string]string{}
	for _, run := range phaseRuns {
		statusByPhase[run.Phase] = run.Status
		outputByPhase[run.Phase] = run.OutputJSON
	}
	for _, phase := range []PipelinePhase{PhaseRouteSelect, PhaseSourcePrepare, PhaseTemplateResolve} {
		if statusByPhase[string(phase)] != PhaseRunStatusSucceeded {
			t.Fatalf("phase %s status = %q, runs=%#v", phase, statusByPhase[string(phase)], phaseRuns)
		}
	}
	if strings.Index(phaseOrderString(phaseRuns), string(PhaseSourcePrepare)) > strings.Index(phaseOrderString(phaseRuns), string(PhaseTemplateResolve)) {
		t.Fatalf("template_resolve should run after source_prepare: %#v", phaseRuns)
	}

	var resolution templateResolution
	if err := json.Unmarshal([]byte(outputByPhase[string(PhaseTemplateResolve)]), &resolution); err != nil {
		t.Fatalf("invalid template_resolve output: %v", err)
	}
	if resolution.Status != "resolved" || resolution.SelectedTemplateID != "layout:government_blue" {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
	if resolution.TemplateRoot != "skills/ppt-master/templates/layouts/government_blue" {
		t.Fatalf("template root = %q", resolution.TemplateRoot)
	}

	workspace := filepath.Join(workspaceRoot, task.RuntimeProject)
	assertPathExists(t, filepath.Join(workspace, ".slidesmith", "template_resolution.json"))
	assertPathExists(t, filepath.Join(workspace, ".slidesmith", "template_lock.json"))
	rawResolution, err := os.ReadFile(filepath.Join(workspace, ".slidesmith", "template_resolution.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawResolution), `"project_path": "projects/task_template_ppt169_20260708"`) {
		t.Fatalf("resolution missing project path:\n%s", rawResolution)
	}
}

func TestSpecGeneratePhaseInputReferencesTemplateResolution(t *testing.T) {
	service, repo, task, workspaceRoot := templateResolvePrepareService(t)
	ctx := context.Background()
	if err := service.processPrepare(ctx, task); err != nil {
		t.Fatalf("processPrepare() error = %v", err)
	}
	workspace := filepath.Join(workspaceRoot, task.RuntimeProject)
	projectPath := filepath.Join(workspace, "projects", "task_template_ppt169_20260708")
	task.Status = model.TaskStatusSpecGenerating
	if err := repo.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	phaseRun, err := service.beginPhaseRun(ctx, task, PhaseSpecGenerate, PhaseRunnerAgent, templateResolvePhaseInput(task, service.resolveTaskWorkspace(task), projectPath))
	if err != nil {
		t.Fatal(err)
	}
	if phaseRun == nil {
		t.Fatal("phase run is nil")
	}
	if !strings.Contains(phaseRun.InputJSON, `"selected_template_id":"layout:government_blue"`) {
		t.Fatalf("spec input missing selected template: %s", phaseRun.InputJSON)
	}
	if !strings.Contains(phaseRun.InputJSON, `"template_resolution":".slidesmith/template_resolution.json"`) {
		t.Fatalf("spec input missing template resolution: %s", phaseRun.InputJSON)
	}
}

func templateResolvePrepareService(t *testing.T) (*TaskService, *repository.Repository, *model.Task, string) {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "template-resolve.sqlite")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Task{},
		&model.TaskEvent{},
		&model.Artifact{},
		&model.TaskRuntimeRun{},
		&model.TaskPhaseRun{},
		&model.TaskConfirmation{},
	); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	seed := filepath.Join(tmp, "seed")
	mustWriteFile(t, filepath.Join(seed, "scripts", "ppt_runner.py"), "print('runner')\n")
	mustWriteFile(t, filepath.Join(seed, "workflows", "ppt_workflow.js"), "console.log('workflow')\n")
	skillDir := buildTemplateCatalogFixture(t)
	mustWriteFile(t, filepath.Join(skillDir, "SKILL.md"), "# PPT Master\n")
	mustWriteFile(t, filepath.Join(skillDir, "scripts", "svg_to_pptx.py"), "print('export')\n")
	catalog := NewTemplateCatalogService(skillDir)
	lock, err := catalog.BuildTemplateLock(context.Background(), "layout:government_blue")
	if err != nil {
		t.Fatal(err)
	}
	rawLock, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	sourceKey := filepath.ToSlash(filepath.Join("tasks", "task-template", "source", "input.md"))
	mustWriteFile(t, storage.Path(sourceKey), "# Source\n")
	task := &model.Task{
		ID:                 "task-template",
		Title:              "Template resolve",
		Status:             model.TaskStatusSourceConverting,
		RuntimeProject:     "task_template",
		SelectedTemplateID: lock.TemplateID,
		TemplateLockJSON:   string(rawLock),
	}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(context.Background(), &model.Artifact{
		TaskID:    task.ID,
		Kind:      model.ArtifactKindSource,
		Name:      "input.md",
		ObjectKey: sourceKey,
		Storage:   "local",
	}); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(tmp, "workspaces")
	service := NewTaskService(
		repo,
		storage,
		templateResolvePrepareAgent{},
		NewRuntimeWorkspacePublisher(storage),
		config.AgentComposeConfig{
			Enabled:           true,
			WorkDir:           seed,
			ComposeFile:       "/data/work/agent-compose.yml",
			WorkspaceRoot:     workspaceRoot,
			PPTMasterSkillDir: skillDir,
			RunnerProfile:     "real-lite",
			Agent:             "ppt_master",
			RuntimeImage:      "slidesmith/ppt-master-runtime:dev",
		},
	)
	return service, repo, task, workspaceRoot
}

func phaseOrderString(runs []model.TaskPhaseRun) string {
	parts := make([]string, 0, len(runs))
	for _, run := range runs {
		parts = append(parts, run.Phase)
	}
	return strings.Join(parts, ">")
}
