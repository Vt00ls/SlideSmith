package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestPublishSVGBundleArtifactsPublishesOnlyPassedBundleWithMetadata(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	contract, err := validateSVGBundleContract(projectPath, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", "svg_execute.json"), contract); err != nil {
		t.Fatal(err)
	}
	if err := service.publishSVGBundleArtifacts(context.Background(), task, projectPath, "phase-svg-1", contract); err != nil {
		t.Fatalf("publishSVGBundleArtifacts() error = %v", err)
	}
	artifacts, err := repo.ListArtifactsByObjectKeyPrefix(context.Background(), task.ID, "tasks/"+task.ID+"/svg-bundle/")
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{
		model.ArtifactKindSVGOutput: false, model.ArtifactKindSVGInventory: false,
		model.ArtifactKindSVGResourceUsage: false, model.ArtifactKindChartUsage: false,
		model.ArtifactKindNotesInventory: false, model.ArtifactKindSpeakerNotes: false,
	}
	for _, artifact := range artifacts {
		if _, ok := wantKinds[artifact.Kind]; ok {
			wantKinds[artifact.Kind] = true
		}
		if _, err := os.Stat(service.storage.Path(artifact.ObjectKey)); err != nil {
			t.Fatalf("published SVG artifact missing: %v", err)
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(artifact.MetadataJSON), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["contract_passed"] != true || metadata["diagnostic"] != false || metadata["phase_run_id"] != "phase-svg-1" {
			t.Fatalf("unsafe SVG artifact metadata: %#v", metadata)
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("SVG bundle artifact kind %s missing: %#v", kind, artifacts)
		}
	}
}

func TestCleanupSVGBundleArtifactsRemovesRowsAndObjects(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	contract, err := validateSVGBundleContract(projectPath, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", "svg_execute.json"), contract); err != nil {
		t.Fatal(err)
	}
	if err := service.publishSVGBundleArtifacts(context.Background(), task, projectPath, "phase-svg-clean", contract); err != nil {
		t.Fatal(err)
	}
	artifacts, err := repo.ListArtifactsByObjectKeyPrefix(context.Background(), task.ID, "tasks/"+task.ID+"/svg-bundle/")
	if err != nil || len(artifacts) == 0 {
		t.Fatalf("published artifacts = %#v, %v", artifacts, err)
	}
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, service.storage.Path(artifact.ObjectKey))
	}
	if err := service.cleanupSVGBundleArtifacts(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := repo.ListArtifactsByObjectKeyPrefix(context.Background(), task.ID, "tasks/"+task.ID+"/svg-bundle/")
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining artifacts = %#v, %v", remaining, err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("SVG bundle object still exists: %s (%v)", path, err)
		}
	}
}

func TestGetSVGBundleReturnsSafePassedSummaryAndRejectsStalePreview(t *testing.T) {
	service, _, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	contract, err := validateSVGBundleContract(projectPath, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", "svg_execute.json"), contract); err != nil {
		t.Fatal(err)
	}
	if err := service.publishSVGBundleArtifacts(context.Background(), task, projectPath, "phase-svg-api", contract); err != nil {
		t.Fatal(err)
	}
	view, err := service.GetSVGBundle(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Passed || view.PageCount != 3 || len(view.Pages) != 3 || view.Pages[0].ArtifactID == "" || !view.Pages[0].NotesPresent {
		t.Fatalf("SVG bundle view = %#v", view)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), projectPath) || strings.Contains(string(raw), "<svg") {
		t.Fatalf("SVG bundle API leaked path/XML: %s", raw)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "01_page_01.svg"), `<svg xmlns="http://www.w3.org/2000/svg"></svg>`+"\n")
	stale, err := service.GetSVGBundle(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Passed || len(stale.Pages) != 0 || len(stale.Errors) == 0 || stale.Errors[len(stale.Errors)-1] != "svg_execute.contract_stale" {
		t.Fatalf("stale SVG bundle view = %#v", stale)
	}
}
