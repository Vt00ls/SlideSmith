package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type forbiddenPrepareAgent struct{}

func (a forbiddenPrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a forbiddenPrepareAgent) Run(context.Context, AgentRunRequest) (*AgentRunResult, error) {
	return nil, fmt.Errorf("prepare agent should not run for unsupported route")
}

func TestProcessPrepareStopsTemplateFillBeforeSourcePrepare(t *testing.T) {
	service, repo, task, _ := routeDispatchPrepareService(t, "套用公司模板填充新内容", []model.Artifact{
		{Name: "brand_template.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/brand_template.pptx"},
		{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/content.md"},
	})
	err := service.processPrepare(context.Background(), task)
	if err == nil {
		t.Fatal("processPrepare should fail for unsupported template-fill route")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed {
		t.Fatalf("status = %q, want failed", updated.Status)
	}
	if updated.Route != model.TaskRouteTemplateFill {
		t.Fatalf("route = %q, want template-fill", updated.Route)
	}
	if updated.FailurePhase != routeFailureUnsupportedWorkflow {
		t.Fatalf("failure phase = %q, want %q", updated.FailurePhase, routeFailureUnsupportedWorkflow)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	statusByPhase := map[string]string{}
	outputByPhase := map[string]string{}
	for _, run := range phaseRuns {
		statusByPhase[run.Phase] = run.Status
		outputByPhase[run.Phase] = run.OutputJSON
	}
	if statusByPhase[string(PhaseRouteSelect)] != PhaseRunStatusSucceeded {
		t.Fatalf("route_select status = %q", statusByPhase[string(PhaseRouteSelect)])
	}
	if _, ok := statusByPhase[string(PhaseSourcePrepare)]; ok {
		t.Fatalf("source_prepare should not run: %#v", phaseRuns)
	}
	var routeOutput map[string]any
	if err := json.Unmarshal([]byte(outputByPhase[string(PhaseRouteSelect)]), &routeOutput); err != nil {
		t.Fatalf("invalid route_select output: %v", err)
	}
	if _, ok := routeOutput["selection"].(map[string]any); !ok {
		t.Fatalf("route_select output missing selection: %s", outputByPhase[string(PhaseRouteSelect)])
	}
	if _, ok := routeOutput["execution_policy"].(map[string]any); !ok {
		t.Fatalf("route_select output missing execution_policy: %s", outputByPhase[string(PhaseRouteSelect)])
	}
}

func TestProcessPrepareStopsBeautifyBeforeSourcePrepare(t *testing.T) {
	service, repo, task, _ := routeDispatchPrepareService(t, "请美化 PPTX，保留页数和文字", []model.Artifact{
		{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/original.pptx"},
	})
	err := service.processPrepare(context.Background(), task)
	if err == nil {
		t.Fatal("processPrepare should fail for unsupported beautify route")
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Route != model.TaskRouteBeautify {
		t.Fatalf("route = %q, want beautify", updated.Route)
	}
	if updated.FailurePhase != routeFailureUnsupportedWorkflow {
		t.Fatalf("failure phase = %q, want %q", updated.FailurePhase, routeFailureUnsupportedWorkflow)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range phaseRuns {
		if run.Phase == string(PhaseSourcePrepare) {
			t.Fatalf("source_prepare should not run: %#v", phaseRuns)
		}
	}
}

func routeDispatchPrepareService(t *testing.T, title string, artifacts []model.Artifact) (*TaskService, *repository.Repository, *model.Task, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
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
	tmp := t.TempDir()
	seed := filepath.Join(tmp, "seed")
	mustWriteFile(t, filepath.Join(seed, "scripts", "ppt_runner.py"), "print('runner')\n")
	mustWriteFile(t, filepath.Join(seed, "workflows", "ppt_workflow.js"), "console.log('workflow')\n")
	skillDir := filepath.Join(tmp, "ppt-master")
	mustWriteFile(t, filepath.Join(skillDir, "SKILL.md"), "# PPT Master\n")
	mustWriteFile(t, filepath.Join(skillDir, "scripts", "svg_to_pptx.py"), "print('export')\n")
	mustWriteFile(t, filepath.Join(skillDir, "templates", "README.md"), "templates\n")

	taskID := "task-route"
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	for i := range artifacts {
		name := artifacts[i].Name
		if name == "" {
			name = filepath.Base(artifacts[i].ObjectKey)
		}
		mustWriteFile(t, storage.Path(artifacts[i].ObjectKey), "source\n")
		artifacts[i].TaskID = taskID
		artifacts[i].Name = name
		if artifacts[i].Storage == "" {
			artifacts[i].Storage = "local"
		}
	}
	task := &model.Task{
		ID:                 taskID,
		Title:              title,
		Status:             model.TaskStatusRuntimePreparing,
		RuntimeProject:     "task_route",
		Route:              model.TaskRouteMain,
		RouteSelectionJSON: "{}",
	}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	for i := range artifacts {
		if err := repo.CreateArtifact(context.Background(), &artifacts[i]); err != nil {
			t.Fatal(err)
		}
	}
	workspaceRoot := filepath.Join(tmp, "workspaces")
	service := NewTaskService(
		repo,
		storage,
		forbiddenPrepareAgent{},
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
