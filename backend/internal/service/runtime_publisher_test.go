package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestRuntimeWorkspacePublisherPublishesStage5Contract(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "task_1_ppt169_20260707")

	mustWriteFile(t, filepath.Join(project, "sources", "input.md"), "# Input\n")
	mustWriteFile(t, filepath.Join(project, "analysis", "source_profile.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "analysis", "deck.identity.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "analysis", "deck.slide_library.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "design_spec.md"), "# Design\n")
	mustWriteFile(t, filepath.Join(project, "spec_lock.md"), "# Lock\n")
	mustWriteFile(t, filepath.Join(project, "svg_output", "01.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(project, "svg_final", "01.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "pptx bytes\n")
	mustWriteFile(t, filepath.Join(project, "logs", "quality.log"), "ok\n")
	mustWriteFile(t, filepath.Join(workspace, ".slidesmith", "events.ndjson"), "{}\n")
	mustWriteFile(t, filepath.Join(workspace, ".slidesmith", "status.json"), "{}\n")
	mustWriteFile(t, filepath.Join(workspace, ".slidesmith", "artifacts.json"), `{"project_path":"projects/task_1_ppt169_20260707","artifacts":[{"path":"design_spec.md","filename":"design_spec.md"}]}`+"\n")

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	publisher := NewRuntimeWorkspacePublisher(storage)
	artifacts, err := publisher.Publish(context.Background(), "task-1", workspace, "v20260708T120000Z")
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	wantObjects := []string{
		"tasks/task-1/artifacts/v20260708T120000Z/source/input.md",
		"tasks/task-1/artifacts/v20260708T120000Z/analysis/source_profile.json",
		"tasks/task-1/artifacts/v20260708T120000Z/analysis/deck.identity.json",
		"tasks/task-1/artifacts/v20260708T120000Z/analysis/deck.slide_library.json",
		"tasks/task-1/artifacts/v20260708T120000Z/design_spec.md",
		"tasks/task-1/artifacts/v20260708T120000Z/spec_lock.md",
		"tasks/task-1/artifacts/v20260708T120000Z/svg_output/01.svg",
		"tasks/task-1/artifacts/v20260708T120000Z/svg_final/01.svg",
		"tasks/task-1/artifacts/v20260708T120000Z/exports/result.pptx",
		"tasks/task-1/artifacts/v20260708T120000Z/logs/quality.log",
		"tasks/task-1/artifacts/v20260708T120000Z/logs/runtime_events.ndjson",
		"tasks/task-1/artifacts/v20260708T120000Z/logs/runtime_status.json",
		"tasks/task-1/artifacts/v20260708T120000Z/manifest/runtime_artifacts.json",
	}
	byObject := map[string]model.Artifact{}
	for _, artifact := range artifacts {
		byObject[artifact.ObjectKey] = artifact
		if _, err := os.Stat(storage.Path(artifact.ObjectKey)); err != nil {
			t.Fatalf("published object %s missing on disk: %v", artifact.ObjectKey, err)
		}
	}
	for _, objectKey := range wantObjects {
		if _, ok := byObject[objectKey]; !ok {
			t.Fatalf("missing object %s in published artifacts: %#v", objectKey, artifacts)
		}
	}
	if byObject["tasks/task-1/artifacts/v20260708T120000Z/exports/result.pptx"].Kind != model.ArtifactKindPPTX {
		t.Fatalf("pptx kind = %q", byObject["tasks/task-1/artifacts/v20260708T120000Z/exports/result.pptx"].Kind)
	}
	if byObject["tasks/task-1/artifacts/v20260708T120000Z/svg_final/01.svg"].Kind != model.ArtifactKindSVGFinal {
		t.Fatalf("svg_final kind = %q", byObject["tasks/task-1/artifacts/v20260708T120000Z/svg_final/01.svg"].Kind)
	}
	if byObject["tasks/task-1/artifacts/v20260708T120000Z/source/input.md"].Kind != model.ArtifactKindSource {
		t.Fatalf("source kind = %q", byObject["tasks/task-1/artifacts/v20260708T120000Z/source/input.md"].Kind)
	}
	if byObject["tasks/task-1/artifacts/v20260708T120000Z/analysis/source_profile.json"].Kind != model.ArtifactKindSourceProfile {
		t.Fatalf("source profile kind = %q", byObject["tasks/task-1/artifacts/v20260708T120000Z/analysis/source_profile.json"].Kind)
	}
	if byObject["tasks/task-1/artifacts/v20260708T120000Z/analysis/deck.identity.json"].Kind != model.ArtifactKindPPTXIdentity {
		t.Fatalf("pptx identity kind = %q", byObject["tasks/task-1/artifacts/v20260708T120000Z/analysis/deck.identity.json"].Kind)
	}
	if byObject["tasks/task-1/artifacts/v20260708T120000Z/analysis/deck.slide_library.json"].Kind != model.ArtifactKindPPTXSlideLibrary {
		t.Fatalf("pptx slide library kind = %q", byObject["tasks/task-1/artifacts/v20260708T120000Z/analysis/deck.slide_library.json"].Kind)
	}
	for _, artifact := range artifacts {
		if artifact.PublishVersion != "v20260708T120000Z" {
			t.Fatalf("publish version = %q", artifact.PublishVersion)
		}
	}
}
