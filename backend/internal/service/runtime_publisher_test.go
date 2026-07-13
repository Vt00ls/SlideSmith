package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type runtimePublisherWriteThenErrorStorage struct {
	StorageService
	failAt             int
	failDeleteContains string
	attemptedObjects   []string
	deletedObjects     []string
}

func (s *runtimePublisherWriteThenErrorStorage) CopyFileToObject(ctx context.Context, objectKey, sourcePath string) (*StoredObject, error) {
	s.attemptedObjects = append(s.attemptedObjects, objectKey)
	stored, err := s.StorageService.CopyFileToObject(ctx, objectKey, sourcePath)
	if err != nil {
		return stored, err
	}
	if len(s.attemptedObjects) == s.failAt {
		return nil, errors.New("injected runtime publish write-then-error")
	}
	return stored, nil
}

func (s *runtimePublisherWriteThenErrorStorage) DeleteObject(ctx context.Context, objectKey string) error {
	s.deletedObjects = append(s.deletedObjects, objectKey)
	if s.failDeleteContains != "" && strings.Contains(objectKey, s.failDeleteContains) {
		return errors.New("injected runtime publish rollback delete failure")
	}
	return s.StorageService.DeleteObject(ctx, objectKey)
}

func TestRuntimeWorkspacePublisherRollsBackExactObjectsOnWriteThenError(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "task-1")
	mustWriteFile(t, filepath.Join(project, "analysis", "fill_plan.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "pptx bytes\n")
	local := NewLocalStorage(filepath.Join(tmp, "storage"))
	fault := &runtimePublisherWriteThenErrorStorage{StorageService: local, failAt: 2}

	_, err := NewRuntimeWorkspacePublisher(fault).PublishProject(context.Background(), "task-1", workspace, project, "v-write-error")
	if err == nil || !strings.Contains(err.Error(), "write-then-error") {
		t.Fatalf("PublishProject() error = %v, want injected write-then-error", err)
	}
	if len(fault.attemptedObjects) != 2 {
		t.Fatalf("attempted objects = %#v, want two exact keys", fault.attemptedObjects)
	}
	for _, objectKey := range fault.attemptedObjects {
		if _, statErr := os.Stat(local.Path(objectKey)); !os.IsNotExist(statErr) {
			t.Fatalf("partial publish object %s remains, err=%v", objectKey, statErr)
		}
	}
}

func TestRuntimeWorkspacePublisherMarksPartialCopyRollbackIncompleteWhenDeleteFails(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "task-1")
	mustWriteFile(t, filepath.Join(project, "analysis", "fill_plan.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "pptx bytes\n")
	local := NewLocalStorage(filepath.Join(tmp, "storage"))
	fault := &runtimePublisherWriteThenErrorStorage{
		StorageService:     local,
		failAt:             2,
		failDeleteContains: "/analysis/fill_plan.json",
	}

	_, err := NewRuntimeWorkspacePublisher(fault).PublishProject(context.Background(), "task-1", workspace, project, "v-delete-error")
	for _, want := range []string{
		"write-then-error",
		"runtime publish cleanup incomplete",
		"injected runtime publish rollback delete failure",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("PublishProject() error = %v, want %q", err, want)
		}
	}
	if !errors.Is(err, ErrRuntimePublishCleanupIncomplete) {
		t.Fatalf("PublishProject() error = %v, want cleanup-incomplete classification", err)
	}
	if len(fault.deletedObjects) != 2 {
		t.Fatalf("rollback deletes = %#v, want both attempted object keys", fault.deletedObjects)
	}
	for _, objectKey := range fault.attemptedObjects {
		_, statErr := os.Stat(local.Path(objectKey))
		if strings.Contains(objectKey, fault.failDeleteContains) {
			if statErr != nil {
				t.Fatalf("failed-delete object %s unexpectedly missing: %v", objectKey, statErr)
			}
			continue
		}
		if !os.IsNotExist(statErr) {
			t.Fatalf("rollback skipped object %s after delete failure: %v", objectKey, statErr)
		}
	}
}

func TestRuntimeWorkspacePublisherAllowsAbsoluteProjectInsideWorkspace(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "inside")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "pptx bytes\n")
	mustWriteFile(t, filepath.Join(workspace, ".slidesmith", "artifacts.json"), `{"project_path":`+mustJSON(t, project)+`,"artifacts":[]}`+"\n")

	publisher := NewRuntimeWorkspacePublisher(NewLocalStorage(filepath.Join(tmp, "storage")))
	artifacts, err := publisher.Publish(context.Background(), "task-1", workspace, "v1")
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !hasPublishedArtifactObjectRel(artifacts, "exports/result.pptx") {
		t.Fatalf("published artifacts = %#v, want exports/result.pptx", artifacts)
	}
}

func TestRuntimeWorkspacePublisherRejectsAbsoluteProjectOutsideWorkspace(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(tmp, "outside-project")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "outside pptx bytes\n")
	mustWriteFile(t, filepath.Join(workspace, ".slidesmith", "artifacts.json"), `{"project_path":`+mustJSON(t, project)+`,"artifacts":[]}`+"\n")

	publisher := NewRuntimeWorkspacePublisher(NewLocalStorage(filepath.Join(tmp, "storage")))
	_, err := publisher.Publish(context.Background(), "task-1", workspace, "v1")
	if err == nil {
		t.Fatal("Publish() error = nil, want outside-workspace project rejection")
	}
	if !strings.Contains(err.Error(), "outside runtime workspace") {
		t.Fatalf("Publish() error = %q, want outside runtime workspace", err)
	}
}

func TestRuntimeWorkspacePublisherRejectsManifestArtifactSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "task-1")
	outside := filepath.Join(tmp, "secret.pptx")
	mustWriteFile(t, outside, "secret host bytes\n")
	if err := os.MkdirAll(filepath.Join(project, "exports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "exports", "result.pptx")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(workspace, ".slidesmith", "artifacts.json"), `{"project_path":"projects/task-1","artifacts":[{"path":"exports/result.pptx"}]}`+"\n")

	publisher := NewRuntimeWorkspacePublisher(NewLocalStorage(filepath.Join(tmp, "storage")))
	_, err := publisher.Publish(context.Background(), "task-1", workspace, "v1")
	if err == nil {
		t.Fatal("Publish() error = nil, want manifest artifact symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Publish() error = %q, want symlink rejection", err)
	}
}

func TestRuntimeWorkspacePublisherRejectsFallbackRootSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "task-1")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "pptx bytes\n")
	mustWriteFile(t, filepath.Join(tmp, "secret.log"), "secret host bytes\n")
	if err := os.MkdirAll(filepath.Join(project, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "secret.log"), filepath.Join(project, "logs", "leak.log")); err != nil {
		t.Fatal(err)
	}

	publisher := NewRuntimeWorkspacePublisher(NewLocalStorage(filepath.Join(tmp, "storage")))
	_, err := publisher.Publish(context.Background(), "task-1", workspace, "v1")
	if err == nil {
		t.Fatal("Publish() error = nil, want fallback-root symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Publish() error = %q, want symlink rejection", err)
	}
}

func TestRuntimeWorkspacePublisherRejectsSymlinkedProjectsDiscoveryRoot(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	outsideProjects := filepath.Join(tmp, "outside-projects")
	project := filepath.Join(outsideProjects, "escaped-project")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "outside pptx bytes\n")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideProjects, filepath.Join(workspace, "projects")); err != nil {
		t.Fatal(err)
	}

	publisher := NewRuntimeWorkspacePublisher(NewLocalStorage(filepath.Join(tmp, "storage")))
	_, err := publisher.Publish(context.Background(), "task-1", workspace, "v1")
	if err == nil {
		t.Fatal("Publish() error = nil, want symlinked projects discovery root rejection")
	}
	if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "outside runtime workspace") {
		t.Fatalf("Publish() error = %q, want projects discovery containment rejection", err)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func hasPublishedArtifactObjectRel(artifacts []model.Artifact, objectRel string) bool {
	suffix := "/" + filepath.ToSlash(objectRel)
	for _, artifact := range artifacts {
		if strings.HasSuffix(filepath.ToSlash(artifact.ObjectKey), suffix) {
			return true
		}
	}
	return false
}

func TestArtifactKindFromRuntimePathTemplateFill(t *testing.T) {
	tests := map[string]string{
		"analysis/fill_plan.json":         model.ArtifactKindTemplateFillPlan,
		"analysis/check_report.json":      model.ArtifactKindTemplateFillCheckReport,
		"validation/validate_report.json": model.ArtifactKindTemplateFillValidateReport,
		"validation/readback.md":          model.ArtifactKindTemplateFillReadback,
	}
	for path, want := range tests {
		if got := artifactKindFromRuntimePath(path); got != want {
			t.Fatalf("artifactKindFromRuntimePath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestRuntimeWorkspacePublisherPublishesTemplateFillArtifacts(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "workspace")
	project := filepath.Join(workspace, "projects", "task-template-fill")
	mustWriteFile(t, filepath.Join(project, "analysis", "fill_plan.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "analysis", "check_report.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "validation", "validate_report.json"), "{}\n")
	mustWriteFile(t, filepath.Join(project, "validation", "readback.md"), "## Slide 1\n")
	mustWriteFile(t, filepath.Join(project, "exports", "result.pptx"), "pptx bytes\n")

	publisher := NewRuntimeWorkspacePublisher(NewLocalStorage(filepath.Join(tmp, "storage")))
	artifacts, err := publisher.Publish(context.Background(), "task-1", workspace, "v1")
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	want := map[string]string{
		"analysis/fill_plan.json":         model.ArtifactKindTemplateFillPlan,
		"analysis/check_report.json":      model.ArtifactKindTemplateFillCheckReport,
		"validation/validate_report.json": model.ArtifactKindTemplateFillValidateReport,
		"validation/readback.md":          model.ArtifactKindTemplateFillReadback,
	}
	for rel, kind := range want {
		var found *model.Artifact
		for index := range artifacts {
			if strings.HasSuffix(filepath.ToSlash(artifacts[index].ObjectKey), "/"+rel) {
				found = &artifacts[index]
				break
			}
		}
		if found == nil {
			t.Fatalf("published artifacts missing %s: %#v", rel, artifacts)
		}
		if found.Kind != kind {
			t.Fatalf("artifact %s kind = %q, want %q", rel, found.Kind, kind)
		}
	}
}

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
