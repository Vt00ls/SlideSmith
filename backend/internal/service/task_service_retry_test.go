package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

func TestNormalizeRetryPhaseSupportsSplitPhases(t *testing.T) {
	tests := []struct {
		name         string
		requested    string
		failurePhase string
		want         string
	}{
		{name: "legacy generate alias", requested: "generate", want: retryPhaseSpecGenerate},
		{name: "svg explicit", requested: "svg_execute", want: retryPhaseSVGExecute},
		{name: "quality explicit", requested: "quality_check", want: retryPhaseQualityCheck},
		{name: "export explicit", requested: "finalize_export", want: retryPhaseFinalizeExport},
		{name: "auto svg failure", requested: "auto", failurePhase: "svg_execute.contract", want: retryPhaseSVGExecute},
		{name: "auto quality failure", requested: "", failurePhase: "quality_check.command", want: retryPhaseQualityCheck},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRetryPhase(tt.requested, tt.failurePhase)
			if err != nil {
				t.Fatalf("normalizeRetryPhase() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeRetryPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRetryQualityCheckCleansOnlyDownstreamArtifacts(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhaseQualityCheck))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusQualityChecking {
		t.Fatalf("status = %q, want quality_checking", updated.Status)
	}
	assertPathExists(t, filepath.Join(projectPath, "design_spec.md"))
	assertPathExists(t, filepath.Join(projectPath, "spec_lock.md"))
	assertPathExists(t, filepath.Join(projectPath, "svg_output", "01.svg"))
	assertPathMissing(t, filepath.Join(projectPath, ".slidesmith", "quality_report.json"))
	assertPathMissing(t, filepath.Join(projectPath, "svg_final"))
	assertPathMissing(t, filepath.Join(projectPath, "exports"))

	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.FailurePhase != "" || latest.ErrorMessage != "" {
		t.Fatalf("failure fields not cleared: phase=%q error=%q", latest.FailurePhase, latest.ErrorMessage)
	}
}

func TestProcessTaskContinuesFromQualityCheckRetry(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseQualityCheck)); err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", updated.Status)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range phaseRuns {
		seen[run.Phase] = true
	}
	if seen[string(PhaseSpecGenerate)] || seen[string(PhaseSVGExecute)] {
		t.Fatalf("quality retry should not rerun spec/svg phases: %#v", phaseRuns)
	}
	for _, phase := range []PipelinePhase{PhaseQualityCheck, PhaseFinalizeExport, PhasePublish} {
		if !seen[string(phase)] {
			t.Fatalf("phase %s missing after quality retry: %#v", phase, phaseRuns)
		}
	}
}

func TestProcessTaskContinuesFromFinalizeExportRetry(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.FailurePhase = "finalize_export.contract"
	task.ErrorMessage = "pptx missing"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhaseFinalizeExport))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusExporting {
		t.Fatalf("status = %q, want exporting", updated.Status)
	}
	assertPathExists(t, filepath.Join(projectPath, "svg_output", "01.svg"))
	assertPathExists(t, filepath.Join(projectPath, "notes", "total.md"))
	assertPathExists(t, filepath.Join(projectPath, ".slidesmith", "quality_report.json"))
	assertPathMissing(t, filepath.Join(projectPath, "svg_final"))
	assertPathMissing(t, filepath.Join(projectPath, "exports"))

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", latest.Status)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range phaseRuns {
		seen[run.Phase] = true
	}
	for _, phase := range []PipelinePhase{PhaseSpecGenerate, PhaseSVGExecute, PhaseQualityCheck} {
		if seen[string(phase)] {
			t.Fatalf("finalize retry should not rerun %s: %#v", phase, phaseRuns)
		}
	}
	for _, phase := range []PipelinePhase{PhaseFinalizeExport, PhasePublish} {
		if !seen[string(phase)] {
			t.Fatalf("phase %s missing after finalize retry: %#v", phase, phaseRuns)
		}
	}
}

func TestProcessTaskContinuesFromPublishRetryAfterPlatformArtifactsDeleted(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.FailurePhase = "publish"
	task.ErrorMessage = "platform artifacts deleted"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(context.Background(), &model.Artifact{
		TaskID:         task.ID,
		Kind:           model.ArtifactKindPPTX,
		Name:           "deleted.pptx",
		ObjectKey:      "tasks/task-retry/artifacts/deleted/exports/deleted.pptx",
		PublishVersion: "deleted",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DB().Where("task_id = ?", task.ID).Delete(&model.Artifact{}).Error; err != nil {
		t.Fatal(err)
	}

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhasePublish))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusPublishing {
		t.Fatalf("status = %q, want publishing", updated.Status)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", latest.Status)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 || phaseRuns[0].Phase != string(PhasePublish) || phaseRuns[0].Status != PhaseRunStatusSucceeded {
		t.Fatalf("publish retry should only create succeeded publish phase run: %#v", phaseRuns)
	}
	artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasPPTX := false
	for _, artifact := range artifacts {
		if artifact.Kind == model.ArtifactKindPPTX {
			hasPPTX = true
		}
	}
	if !hasPPTX {
		t.Fatalf("publish retry did not recreate pptx artifact: %#v", artifacts)
	}
}

func retryTestService(t *testing.T) (*TaskService, *repository.Repository, *model.Task, string) {
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
	runtimeProject := "task_retry"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260708")
	task := &model.Task{
		ID:              "task-retry",
		Title:           "Retry split phase",
		Status:          model.TaskStatusFailed,
		RuntimeProject:  runtimeProject,
		FailurePhase:    "quality_check.command",
		ErrorMessage:    "quality failed",
		FailureMetadata: `{"phase":"quality_check.command"}`,
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
	return service, repo, task, projectPath
}

func mustWriteRetryProjectFiles(projectPath string) {
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "input.md"), "# Source\n")
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "# Design Spec\n")
	mustWriteFileNoTest(projectPath, "spec_lock.md", "# Spec Lock\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "01.svg"), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "02.svg"), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "03.svg"), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("notes", "total.md"), "# Notes\n")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "quality_report.json"), `{"errors":1}`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_final", "01.svg"), `<svg></svg>`+"\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "stale.pptx"), 3)
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path %s to exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path %s to be missing, err=%v", path, err)
	}
}
