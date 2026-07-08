package service

import (
	"archive/zip"
	"context"
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

type splitGenerateFakeAgent struct {
	projectPath string
}

func (a splitGenerateFakeAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a splitGenerateFakeAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	switch req.Phase {
	case string(PhaseSpecGenerate):
		mustWriteFileNoTest(a.projectPath, "design_spec.md", "# Design Spec\n\nSlides: 3\n")
		mustWriteFileNoTest(a.projectPath, "spec_lock.md", "# Spec Lock\n\npage_count: 3\n")
	case string(PhaseSVGExecute):
		for _, name := range []string{"01.svg", "02.svg", "03.svg"} {
			mustWriteFileNoTest(a.projectPath, filepath.Join("svg_output", name), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
		}
		mustWriteFileNoTest(a.projectPath, filepath.Join("notes", "total.md"), "# Notes\n")
	case string(PhaseQualityCheck):
		mustWriteFileNoTest(a.projectPath, filepath.Join("logs", "quality.log"), "ok\n")
	case string(PhaseFinalizeExport):
		for _, name := range []string{"01.svg", "02.svg", "03.svg"} {
			mustWriteFileNoTest(a.projectPath, filepath.Join("svg_final", name), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
		}
		mustWritePPTXNoTest(a.projectPath, filepath.Join("exports", "result.pptx"), 3)
	default:
		return &AgentRunResult{Status: "failed"}, nil
	}
	exitCode := 0
	return &AgentRunResult{
		RunID:    "run-" + req.Phase,
		Status:   "succeeded",
		ExitCode: &exitCode,
	}, nil
}

func TestProcessFullPPTMasterSplitCompletesWithSeparatePhaseRuns(t *testing.T) {
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
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	workspaceRoot := filepath.Join(tmp, "workspaces")
	runtimeProject := "task_1"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260708")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "input.md"), "# Source\n")

	task := &model.Task{
		ID:             "task-1",
		Title:          "Split generate",
		Status:         model.TaskStatusSpecGenerating,
		RuntimeProject: runtimeProject,
	}
	ctx := context.Background()
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	service := NewTaskService(
		repo,
		storage,
		splitGenerateFakeAgent{projectPath: projectPath},
		NewRuntimeWorkspacePublisher(storage),
		config.AgentComposeConfig{
			Enabled:       true,
			RunnerProfile: "full-ppt-master",
			WorkspaceRoot: workspaceRoot,
		},
	)
	if err := service.processGenerate(ctx, task); err != nil {
		t.Fatalf("processGenerate() error = %v", err)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", updated.Status)
	}

	phaseRuns, err := repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	statusByPhase := map[string]string{}
	runnerByPhase := map[string]string{}
	for _, run := range phaseRuns {
		statusByPhase[run.Phase] = run.Status
		runnerByPhase[run.Phase] = run.Runner
	}
	for _, phase := range []PipelinePhase{PhaseSpecGenerate, PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport, PhasePublish} {
		if statusByPhase[string(phase)] != PhaseRunStatusSucceeded {
			t.Fatalf("phase %s status = %q, runs=%#v", phase, statusByPhase[string(phase)], phaseRuns)
		}
	}
	if runnerByPhase[string(PhaseSpecGenerate)] != PhaseRunnerAgent {
		t.Fatalf("spec runner = %q", runnerByPhase[string(PhaseSpecGenerate)])
	}
	if runnerByPhase[string(PhaseSVGExecute)] != PhaseRunnerAgent {
		t.Fatalf("svg runner = %q", runnerByPhase[string(PhaseSVGExecute)])
	}
	if statusByPhase[string(PhaseImageAcquire)] != PhaseRunStatusSkipped {
		t.Fatalf("image_acquire status = %q", statusByPhase[string(PhaseImageAcquire)])
	}

	runtimeRuns, err := repo.ListRuntimeRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	seenRuntimePhase := map[string]bool{}
	for _, run := range runtimeRuns {
		seenRuntimePhase[run.Phase] = true
	}
	for _, phase := range []PipelinePhase{PhaseSpecGenerate, PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport} {
		if !seenRuntimePhase[string(phase)] {
			t.Fatalf("runtime phase %s missing, runs=%#v", phase, runtimeRuns)
		}
	}

	artifacts, err := repo.ListArtifacts(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasPPTX := false
	hasPublishContract := false
	hasFinalContract := false
	for _, artifact := range artifacts {
		if artifact.Kind == model.ArtifactKindPPTX {
			hasPPTX = true
			if _, err := os.Stat(storage.Path(artifact.ObjectKey)); err != nil {
				t.Fatalf("published pptx missing: %v", err)
			}
		}
		if strings.HasSuffix(artifact.ObjectKey, "/contracts/publish.json") {
			hasPublishContract = true
		}
		if strings.HasSuffix(artifact.ObjectKey, "/contracts/final.json") {
			hasFinalContract = true
		}
	}
	if !hasPPTX {
		t.Fatalf("published artifacts missing pptx: %#v", artifacts)
	}
	if !hasPublishContract || !hasFinalContract {
		t.Fatalf("published artifacts missing contract reports: publish=%v final=%v artifacts=%#v", hasPublishContract, hasFinalContract, artifacts)
	}
}

func TestProcessFullPPTMasterSplitPausesForSpecPreviewAndContinues(t *testing.T) {
	service, repo, task := splitPreviewTestService(t)
	ctx := context.Background()
	if err := repo.SubmitConfirmations(ctx, task.ID, map[string]any{
		"page_count":   "3",
		"refine_spec":  "true",
		"canvas":       "ppt169",
		"color":        "商务克制",
		"typography":   "系统默认",
		"icons":        "tabler-outline",
		"image_usage":  "none",
		"visual_style": "business",
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.processGenerate(ctx, task); err != nil {
		t.Fatalf("processGenerate() error = %v", err)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusAwaitingSpecConfirm {
		t.Fatalf("status = %q, want awaiting_spec_confirm", updated.Status)
	}

	preview, err := service.GetSpecPreview(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetSpecPreview() error = %v", err)
	}
	if preview.DesignSpec.Content == "" || preview.SpecLock.Content == "" {
		t.Fatalf("spec preview missing content: %#v", preview)
	}
	if preview.Summary["page_count"] == nil {
		t.Fatalf("spec preview missing page_count summary: %#v", preview.Summary)
	}

	phaseRuns, err := repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range phaseRuns {
		seen[run.Phase] = true
	}
	if !seen[string(PhaseSpecGenerate)] {
		t.Fatalf("spec phase missing: %#v", phaseRuns)
	}
	if seen[string(PhaseSVGExecute)] || seen[string(PhaseQualityCheck)] || seen[string(PhaseFinalizeExport)] {
		t.Fatalf("downstream phases should not run before spec confirmation: %#v", phaseRuns)
	}

	continued, err := service.ContinueTask(ctx, task.ID, string(PhaseSVGExecute))
	if err != nil {
		t.Fatalf("ContinueTask() error = %v", err)
	}
	if continued.Status != model.TaskStatusSVGGenerating {
		t.Fatalf("continued status = %q, want svg_generating", continued.Status)
	}
	if err := service.ProcessTask(ctx, task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	completed, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", completed.Status)
	}
}

func TestSpecPreviewCanQueueSpecRegenerate(t *testing.T) {
	service, repo, task := splitPreviewTestService(t)
	ctx := context.Background()
	if err := repo.SubmitConfirmations(ctx, task.ID, map[string]any{"refine_spec": "true", "page_count": "3"}); err != nil {
		t.Fatal(err)
	}
	if err := service.processGenerate(ctx, task); err != nil {
		t.Fatalf("processGenerate() error = %v", err)
	}
	updated, err := repo.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusAwaitingSpecConfirm {
		t.Fatalf("status = %q, want awaiting_spec_confirm", updated.Status)
	}
	continued, err := service.ContinueTask(ctx, task.ID, string(PhaseSpecGenerate))
	if err != nil {
		t.Fatalf("ContinueTask(spec_generate) error = %v", err)
	}
	if continued.Status != model.TaskStatusSpecGenerating {
		t.Fatalf("status = %q, want spec_generating", continued.Status)
	}
	projectPath, err := service.findPersistentProjectPath(continued)
	if err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, filepath.Join(projectPath, "design_spec.md"))
	assertPathMissing(t, filepath.Join(projectPath, "spec_lock.md"))
}

func splitPreviewTestService(t *testing.T) (*TaskService, *repository.Repository, *model.Task) {
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
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	workspaceRoot := filepath.Join(tmp, "workspaces")
	runtimeProject := "task_preview"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260708")
	mustWriteFile(t, filepath.Join(projectPath, "sources", "input.md"), "# Source\n")

	task := &model.Task{
		ID:             "task-preview",
		Title:          "Spec preview",
		Status:         model.TaskStatusSpecGenerating,
		RuntimeProject: runtimeProject,
	}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	service := NewTaskService(
		repo,
		storage,
		splitGenerateFakeAgent{projectPath: projectPath},
		NewRuntimeWorkspacePublisher(storage),
		config.AgentComposeConfig{
			Enabled:       true,
			RunnerProfile: "full-ppt-master",
			WorkspaceRoot: workspaceRoot,
		},
	)
	return service, repo, task
}

func mustWriteFileNoTest(root, rel, content string) {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		panic(err)
	}
}

func mustWritePPTXNoTest(root, rel string, slideCount int) {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	file, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	defer writer.Close()
	for i := 1; i <= slideCount; i++ {
		entry, err := writer.Create(fmt.Sprintf("ppt/slides/slide%d.xml", i))
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write([]byte(`<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`)); err != nil {
			panic(err)
		}
	}
}
