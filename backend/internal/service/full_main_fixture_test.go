package service

import (
	"context"
	"encoding/json"
	"fmt"
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

type fullMainFixture struct {
	Name       string   `json:"name"`
	PageCount  int      `json:"page_count"`
	RefineSpec bool     `json:"refine_spec"`
	Sources    []string `json:"sources"`
}

type fullMainFixtureAgent struct {
	projectPath string
	pageCount   int
	t           *testing.T
}

func (a *fullMainFixtureAgent) Up(context.Context, AgentRunRequest) error { return nil }

func (a *fullMainFixtureAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	switch req.Phase {
	case string(PhaseSpecGenerate):
		mustWriteFileNoTest(a.projectPath, "design_spec.md", fmt.Sprintf("# Design Spec\n\nSlides: %d\n", a.pageCount))
		mustWriteFileNoTest(a.projectPath, "spec_lock.md", fmt.Sprintf("# Spec Lock\n\npage_count: %d\n", a.pageCount))
	case string(PhaseSVGExecute):
		for index := 1; index <= a.pageCount; index++ {
			mustWriteFileNoTest(a.projectPath, filepath.Join("svg_output", fmt.Sprintf("%02d.svg", index)), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
		}
		mustWriteFileNoTest(a.projectPath, filepath.Join("notes", "total.md"), "# Notes\n")
	case string(PhaseQualityCheck):
		mustWriteFileNoTest(a.projectPath, filepath.Join("logs", "quality.log"), "ok\n")
	case string(PhaseFinalizeExport):
		if strings.Contains(req.Command, "ppt_runner.py publish") {
			a.t.Fatalf("finalize export command called runtime publish: %s", req.Command)
		}
		for index := 1; index <= a.pageCount; index++ {
			mustWriteFileNoTest(a.projectPath, filepath.Join("svg_final", fmt.Sprintf("%02d.svg", index)), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
		}
		mustWritePPTXNoTest(a.projectPath, filepath.Join("exports", "result.pptx"), a.pageCount)
	default:
		return nil, fmt.Errorf("unexpected fixture phase %s", req.Phase)
	}
	exitCode := 0
	return &AgentRunResult{RunID: "fixture-" + req.Phase, Status: "succeeded", ExitCode: &exitCode}, nil
}

func TestFullMainFixedFixtures(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "..", "runtime", "ppt-master-agent", "fixtures")
	for _, name := range []string{"full-local-text", "full-multi-source", "full-spec-preview"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtureRoot, name, "fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			var fixture fullMainFixture
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			if fixture.Name != name || fixture.PageCount < 3 || len(fixture.Sources) == 0 {
				t.Fatalf("invalid fixture descriptor: %#v", fixture)
			}

			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&model.Task{}, &model.TaskEvent{}, &model.Artifact{}, &model.TaskRuntimeRun{}, &model.TaskPhaseRun{}, &model.TaskConfirmation{}); err != nil {
				t.Fatal(err)
			}
			repo := repository.New(db)
			tmp := t.TempDir()
			storage := NewLocalStorage(filepath.Join(tmp, "storage"))
			workspaceRoot := filepath.Join(tmp, "workspaces")
			runtimeProject := "task_" + strings.ReplaceAll(name, "-", "_")
			projectPath := filepath.Join(workspaceRoot, runtimeProject, "projects", runtimeProject+"_ppt169_20260713")
			for _, source := range fixture.Sources {
				mustWriteFileNoTest(projectPath, filepath.Join("sources", source), "fixture source\n")
			}
			mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), fmt.Sprintf(`{"page_count":%d,"refine_spec":%t}`+"\n", fixture.PageCount, fixture.RefineSpec))
			task := &model.Task{ID: name, Title: fixture.Name, Status: model.TaskStatusSpecGenerating, RuntimeProject: runtimeProject, Route: model.TaskRouteMain}
			lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
			if err := repo.CreateTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			if err := repo.EnsureConfirmations(context.Background(), task.ID, defaultConfirmations()); err != nil {
				t.Fatal(err)
			}
			if err := repo.SubmitConfirmations(context.Background(), task.ID, map[string]any{
				"page_count":  fmt.Sprint(fixture.PageCount),
				"refine_spec": fmt.Sprint(fixture.RefineSpec),
			}); err != nil {
				t.Fatal(err)
			}
			service := NewTaskService(repo, storage, &fullMainFixtureAgent{projectPath: projectPath, pageCount: fixture.PageCount, t: t}, NewRuntimeWorkspacePublisher(storage), config.AgentComposeConfig{
				Enabled:               true,
				RunnerProfile:         model.RunnerProfileFullPPTMaster,
				FullPPTDefaultEnabled: true,
				WorkspaceRoot:         workspaceRoot,
			})
			writeRuntimeProfileManifestForTest(t, workspaceRoot, task)
			if err := service.processGenerate(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			if fixture.RefineSpec {
				waiting, err := repo.GetTask(context.Background(), task.ID)
				if err != nil || waiting.Status != model.TaskStatusAwaitingSpecConfirm {
					t.Fatalf("spec preview state = %#v, %v", waiting, err)
				}
				if _, err := service.ContinueTask(context.Background(), task.ID, string(PhaseSVGExecute)); err != nil {
					t.Fatal(err)
				}
				if err := service.ProcessTask(context.Background(), task.ID); err != nil {
					t.Fatal(err)
				}
			}
			completed, err := repo.GetTask(context.Background(), task.ID)
			if err != nil || completed.Status != model.TaskStatusCompleted || completed.RunnerProfile != model.RunnerProfileFullPPTMaster {
				t.Fatalf("completed fixture task = %#v, %v", completed, err)
			}
			phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			statusByPhase := map[string]string{}
			for _, run := range phaseRuns {
				statusByPhase[run.Phase] = run.Status
				if run.Phase == string(PhaseImageAcquire) && !strings.Contains(run.OutputJSON, `"implementation":"deferred_to_SPEC05"`) {
					t.Fatalf("image acquire skip lacks SPEC-05 metadata: %s", run.OutputJSON)
				}
				if run.Phase != string(PhaseImageAcquire) && !strings.Contains(run.InputJSON, model.RunnerProfileFullPPTMaster) {
					t.Fatalf("phase %s input missing locked profile: %s", run.Phase, run.InputJSON)
				}
			}
			for _, phase := range []PipelinePhase{PhaseSpecGenerate, PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport, PhasePublish} {
				if statusByPhase[string(phase)] != PhaseRunStatusSucceeded {
					t.Fatalf("phase %s status = %q", phase, statusByPhase[string(phase)])
				}
			}
			if statusByPhase[string(PhaseImageAcquire)] != PhaseRunStatusSkipped {
				t.Fatalf("image acquire status = %q", statusByPhase[string(PhaseImageAcquire)])
			}
			if _, err := countPPTXSlides(filepath.Join(projectPath, "exports", "result.pptx")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
