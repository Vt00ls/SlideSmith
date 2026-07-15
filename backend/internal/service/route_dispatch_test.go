package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type successfulRoutePrepareAgent struct {
	sessionRoot string
}

func (successfulRoutePrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a successfulRoutePrepareAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	if err := os.MkdirAll(a.sessionRoot, 0o755); err != nil {
		return nil, err
	}
	sessionDir, err := os.MkdirTemp(a.sessionRoot, "route-session-")
	if err != nil {
		return nil, err
	}
	sessionWorkspace := filepath.Join(sessionDir, "workspace")
	if err := copyDir(ctx, req.WorkDir, sessionWorkspace); err != nil {
		return nil, err
	}
	project := filepath.Join(sessionWorkspace, "projects", "task_route_ppt169_20260708")
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
		WorkspacePath: sessionWorkspace,
	}, nil
}

func TestProcessPrepareDispatchesRouteAfterSourcePrepare(t *testing.T) {
	tests := []struct {
		name             string
		title            string
		artifacts        []model.Artifact
		wantRoute        string
		wantStatus       string
		wantFailurePhase string
	}{
		{
			name:  "template-fill",
			title: "套用公司模板填充新内容",
			artifacts: []model.Artifact{
				{Name: "brand_template.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/brand_template.pptx"},
				{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/content.md"},
			},
			wantRoute:  model.TaskRouteTemplateFill,
			wantStatus: model.TaskStatusTemplateFillPlanning,
		},
		{
			name:  "beautify",
			title: "请美化 PPTX，保留页数和文字",
			artifacts: []model.Artifact{
				{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/original.pptx"},
			},
			wantRoute:        model.TaskRouteBeautify,
			wantStatus:       model.TaskStatusFailed,
			wantFailurePhase: "source_prepare.workflow_not_enabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, workspaceRoot := routeDispatchPrepareService(t, test.title, test.artifacts)
			err := service.processPrepare(context.Background(), task)
			if test.wantFailurePhase == "" && err != nil {
				t.Fatalf("processPrepare() error = %v", err)
			}
			if test.wantFailurePhase != "" && err == nil {
				t.Fatal("processPrepare should fail for a workflow that is not enabled")
			}
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", updated.Status, test.wantStatus)
			}
			if updated.Route != test.wantRoute {
				t.Fatalf("route = %q, want %q", updated.Route, test.wantRoute)
			}
			if updated.FailurePhase != test.wantFailurePhase {
				t.Fatalf("failure phase = %q, want %q", updated.FailurePhase, test.wantFailurePhase)
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

			wantWorkspace := filepath.Join(workspaceRoot, task.RuntimeProject)
			wantProject := filepath.Join(wantWorkspace, "projects", "task_route_ppt169_20260708")
			if test.wantFailurePhase != "" {
				var metadata map[string]any
				if err := json.Unmarshal([]byte(updated.FailureMetadata), &metadata); err != nil {
					t.Fatalf("invalid failure metadata: %v", err)
				}
				for key, want := range map[string]string{
					"workspace_path": wantWorkspace,
					"project_path":   wantProject,
					"route":          test.wantRoute,
				} {
					if metadata[key] != want {
						t.Fatalf("metadata[%q] = %#v, want %q; metadata=%#v", key, metadata[key], want, metadata)
					}
				}
				if metadata["next_spec"] != "" {
					t.Fatalf("metadata next_spec must not expose stale SPEC handoff: %#v", metadata)
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

func TestProcessPrepareQueuesEnabledBeautifyInventoryWithoutTemplateResolve(t *testing.T) {
	service, repo, task, _ := routeDispatchPrepareService(t, "请美化 PPTX，保留页数和文字", []model.Artifact{
		{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/original.pptx"},
	})
	service.agentCfg.BeautifyEnabled = true
	if err := service.processPrepare(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Route != model.TaskRouteBeautify || updated.Status != model.TaskStatusBeautifyInventoryBuilding {
		t.Fatalf("enabled Beautify dispatch = route %q status %q", updated.Route, updated.Status)
	}
	runs, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Phase == string(PhaseTemplateResolve) {
			t.Fatalf("Beautify must not run platform template resolve: %#v", runs)
		}
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
		successfulRoutePrepareAgent{sessionRoot: filepath.Join(tmp, "sessions")},
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
