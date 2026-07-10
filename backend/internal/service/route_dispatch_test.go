package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type successfulRoutePrepareAgent struct{}

func (successfulRoutePrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (successfulRoutePrepareAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	project := filepath.Join(req.WorkDir, "projects", "task_route_ppt169_20260708")
	mustWriteFileNoTest(project, filepath.Join("sources", "deck.pptx"), "pptx")
	mustWriteFileNoTest(project, filepath.Join("sources", "deck.md"), "# Deck\n")
	mustWriteFileNoTest(project, filepath.Join("sources", "content.md"), "# Content\n")
	mustWriteFileNoTest(project, filepath.Join("analysis", "source_profile.json"), `{}`)
	mustWriteFileNoTest(project, filepath.Join("analysis", "deck.identity.json"), `{}`)
	mustWriteFileNoTest(project, filepath.Join("analysis", "deck.slide_library.json"), `{}`)
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-route-prepare",
		SessionID:     "session-route-prepare",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: req.WorkDir,
	}, nil
}

func TestProcessPrepareRunsSourcePrepareBeforeBlockingUnsupportedWorkflow(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		artifacts []model.Artifact
		wantRoute string
		wantSpec  string
	}{
		{
			name:  "template-fill",
			title: "套用公司模板填充新内容",
			artifacts: []model.Artifact{
				{Name: "brand_template.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/brand_template.pptx"},
				{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/content.md"},
			},
			wantRoute: model.TaskRouteTemplateFill,
			wantSpec:  "SPEC-03-Template-Fill-PPTX.md",
		},
		{
			name:  "beautify",
			title: "请美化 PPTX，保留页数和文字",
			artifacts: []model.Artifact{
				{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/original.pptx"},
			},
			wantRoute: model.TaskRouteBeautify,
			wantSpec:  "SPEC-04-Beautify-PPTX.md",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, workspaceRoot := routeDispatchPrepareService(t, test.title, test.artifacts)
			err := service.processPrepare(context.Background(), task)
			if err == nil {
				t.Fatal("processPrepare should fail for a workflow not enabled in SPEC-02")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusFailed {
				t.Fatalf("status = %q, want failed", updated.Status)
			}
			if updated.Route != test.wantRoute {
				t.Fatalf("route = %q, want %q", updated.Route, test.wantRoute)
			}
			if updated.FailurePhase != "source_prepare.workflow_not_enabled" {
				t.Fatalf("failure phase = %q, want source_prepare.workflow_not_enabled", updated.FailurePhase)
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
				t.Fatalf("route_select status = %q, runs=%#v", statusByPhase[string(PhaseRouteSelect)], phaseRuns)
			}
			if statusByPhase[string(PhaseSourcePrepare)] != PhaseRunStatusSucceeded {
				t.Fatalf("source_prepare should run before workflow_not_enabled: %#v", phaseRuns)
			}
			if _, ok := statusByPhase[string(PhaseTemplateResolve)]; ok {
				t.Fatalf("template_resolve should not run: %#v", phaseRuns)
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

			var metadata map[string]any
			if err := json.Unmarshal([]byte(updated.FailureMetadata), &metadata); err != nil {
				t.Fatalf("invalid failure metadata: %v", err)
			}
			wantWorkspace := filepath.Join(workspaceRoot, task.RuntimeProject)
			wantProject := filepath.Join(wantWorkspace, "projects", "task_route_ppt169_20260708")
			for key, want := range map[string]string{
				"workspace_path": wantWorkspace,
				"project_path":   wantProject,
				"route":          test.wantRoute,
				"next_spec":      test.wantSpec,
			} {
				if metadata[key] != want {
					t.Fatalf("metadata[%q] = %#v, want %q; metadata=%#v", key, metadata[key], want, metadata)
				}
			}
			if metadata["route_reason"] == "" {
				t.Fatalf("failure metadata missing route_reason: %#v", metadata)
			}
			if _, ok := metadata["source_contract"].(map[string]any); !ok {
				t.Fatalf("failure metadata missing source_contract: %#v", metadata)
			}
			policyMetadata, ok := metadata["route_execution_policy"].(map[string]any)
			if !ok {
				t.Fatalf("failure metadata missing route_execution_policy: %#v", metadata)
			}
			if policyMetadata["workflow_executable"] != false || policyMetadata["unsupported_after"] != string(PhaseSourcePrepare) {
				t.Fatalf("unexpected policy metadata: %#v", policyMetadata)
			}

			artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantKinds := map[string]bool{
				model.ArtifactKindSourceProfile:    false,
				model.ArtifactKindPPTXIdentity:     false,
				model.ArtifactKindPPTXSlideLibrary: false,
			}
			for _, artifact := range artifacts {
				if _, ok := wantKinds[artifact.Kind]; ok {
					wantKinds[artifact.Kind] = true
				}
			}
			for kind, found := range wantKinds {
				if !found {
					t.Fatalf("persisted source-intake artifact kind %q not found: %#v", kind, artifacts)
				}
			}
		})
	}
}

func routeDispatchPrepareService(t *testing.T, title string, artifacts []model.Artifact) (*TaskService, *repository.Repository, *model.Task, string) {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "route-dispatch.sqlite")), &gorm.Config{
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
		successfulRoutePrepareAgent{},
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
