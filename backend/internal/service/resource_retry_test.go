package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestRetryResourcePhasePreservesValidatedCacheAndCleansDownstream(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.FailurePhase = "image_acquire.ai"
	if err := repo.DB().Save(task).Error; err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(projectPath, "images", "ready-cache.png")
	mustWriteFile(t, cachePath, "validated cache")
	manifestPath := filepath.Join(projectPath, ".slidesmith", "resources_manifest.json")
	beforeManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	publishedPath := filepath.Join(projectPath, "exports", "published.pptx")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "published.pptx"), 3)
	stored, err := service.storage.CopyFileToObject(context.Background(), "tasks/task-retry/artifacts/v-old/exports/published.pptx", publishedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(context.Background(), &model.Artifact{
		TaskID: task.ID, Kind: model.ArtifactKindPPTX, Name: "published.pptx", Storage: "local",
		ObjectKey: stored.ObjectKey, MimeType: stored.MimeType, Size: stored.Size, SHA256: stored.SHA256, PublishVersion: "v-old",
	}); err != nil {
		t.Fatal(err)
	}

	retried, err := service.RetryTask(context.Background(), task.ID, "image_acquire")
	if err != nil {
		t.Fatalf("RetryTask(image_acquire) error = %v", err)
	}
	if retried.Status != model.TaskStatusImageAcquiring || retried.RunnerProfile != model.RunnerProfileFullPPTMaster {
		t.Fatalf("resource retry task = %#v", retried)
	}
	assertPathExists(t, cachePath)
	afterManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterManifest) != string(beforeManifest) {
		t.Fatal("resource retry changed reusable canonical manifest before runner reconciliation")
	}
	if _, err := os.Stat(service.storage.Path(stored.ObjectKey)); !os.IsNotExist(err) {
		t.Fatalf("downstream published object still exists: %v", err)
	}
	published, err := repo.ListArtifactsByObjectKeyPrefix(context.Background(), task.ID, "tasks/task-retry/artifacts/")
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Fatalf("downstream published rows still exist: %#v", published)
	}
	for _, path := range []string{
		filepath.Join(projectPath, "svg_output"), filepath.Join(projectPath, "notes"),
		filepath.Join(projectPath, "svg_final"), filepath.Join(projectPath, "exports"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "image_acquire.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "svg_execute.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("downstream retry output still exists: %s (err=%v)", path, err)
		}
	}
}

func TestRetryResourcePhaseRejectsStalePlanBeforeCleanup(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.FailurePhase = "image_acquire.contract"
	if err := repo.DB().Save(task).Error; err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(projectPath, "design_spec.md"), "# changed after plan\n")
	_, err := service.RetryTask(context.Background(), task.ID, "image_acquire")
	if err == nil || !strings.Contains(err.Error(), "stale spec contract") {
		t.Fatalf("stale retry error = %v", err)
	}
	assertPathExists(t, filepath.Join(projectPath, "svg_output", "01.svg"))
	persisted, getErr := repo.GetTask(context.Background(), task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.Status != model.TaskStatusFailed {
		t.Fatalf("stale retry changed task status: %#v", persisted)
	}
}

func TestNormalizeAndInferResourceRetryPhase(t *testing.T) {
	for _, requested := range []string{"resource", "resources", "image", "image_acquiring", "image_acquire"} {
		phase, err := normalizeRetryPhase(requested, "")
		if err != nil || phase != retryPhaseImageAcquire {
			t.Fatalf("normalizeRetryPhase(%q) = %q, %v", requested, phase, err)
		}
	}
	for _, failure := range []string{"image_acquire.ai", "resource.policy", "image_acquiring"} {
		if got := inferRetryPhase(failure); got != retryPhaseImageAcquire {
			t.Fatalf("inferRetryPhase(%q) = %q", failure, got)
		}
	}
}
