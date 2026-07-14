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

func addPublishableResourceToAcquireFixture(t *testing.T, fixture *resourceAcquireFixture, phaseRunID string) resourceManifestItem {
	t.Helper()
	manifestPath := filepath.Join(fixture.projectPath, ".slidesmith", "resources_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest resourcesManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	output := writeResourcePNG(t, fixture.projectPath, "images/publishable.png")
	item := resourceManifestItem{
		ID: "res-publishable", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "user",
		Status: "ready", Attempt: 1, CacheKey: "publishable-cache", Publishable: true,
		Input:  map[string]any{"prompt_or_query_sha256": strings.Repeat("a", 64), "internal_prompt": "must never publish"},
		Output: output, Provenance: map[string]any{"source_url": "https://example.test/private?token=secret"},
	}
	manifest.PhaseRunID = phaseRunID
	manifest.Resources = []resourceManifestItem{item}
	manifest.Summary = resourceManifestSummary{Total: 1, Ready: 1, Bytes: output.Size}
	if err := writeJSONPretty(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestPublishResourcePhaseArtifactsPublishesOnlyManifestBoundAssets(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	phaseRunID := "resource-publisher-ready"
	item := addPublishableResourceToAcquireFixture(t, fixture, phaseRunID)
	mustWriteFile(t, filepath.Join(fixture.projectPath, "images", "unregistered-secret.png"), "not in manifest")
	if err := fixture.service.publishResourcePhaseArtifacts(context.Background(), fixture.task, fixture.projectPath, phaseRunID, false); err != nil {
		t.Fatalf("publishResourcePhaseArtifacts() error = %v", err)
	}
	artifacts, err := fixture.repo.ListArtifactsByObjectKeyPrefix(context.Background(), fixture.task.ID, "tasks/"+fixture.task.ID+"/resources/")
	if err != nil {
		t.Fatal(err)
	}
	foundAsset := false
	for _, artifact := range artifacts {
		if strings.Contains(artifact.ObjectKey, "unregistered-secret") || strings.Contains(artifact.ObjectKey, "internal_prompt") {
			t.Fatalf("publisher leaked unregistered/internal resource: %#v", artifact)
		}
		if artifact.Kind == model.ArtifactKindResourceAsset {
			foundAsset = true
			if artifact.SHA256 != item.Output.SHA256 || artifact.Name != "publishable.png" {
				t.Fatalf("resource asset binding = %#v", artifact)
			}
		}
	}
	if !foundAsset {
		t.Fatalf("publishable manifest resource missing: %#v", artifacts)
	}
}

func TestPublishResourcePhaseArtifactsDiagnosticsExcludeBinaryAssets(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	phaseRunID := "resource-publisher-diagnostic"
	addPublishableResourceToAcquireFixture(t, fixture, phaseRunID)
	if err := fixture.service.publishResourcePhaseArtifacts(context.Background(), fixture.task, fixture.projectPath, phaseRunID, true); err != nil {
		t.Fatal(err)
	}
	artifacts, err := fixture.repo.ListArtifactsByObjectKeyPrefix(context.Background(), fixture.task.ID, "tasks/"+fixture.task.ID+"/resources/")
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Kind == model.ArtifactKindResourceAsset || artifact.Kind == model.ArtifactKindChartData || artifact.Kind == model.ArtifactKindChartTemplate {
			t.Fatalf("diagnostic publisher exposed binary asset: %#v", artifact)
		}
	}
}

func TestPublishResourcePhaseArtifactsRejectsManifestHashMismatch(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	phaseRunID := "resource-publisher-tamper"
	addPublishableResourceToAcquireFixture(t, fixture, phaseRunID)
	manifestPath := filepath.Join(fixture.projectPath, ".slidesmith", "resources_manifest.json")
	var manifest resourcesManifest
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Resources[0].Output.SHA256 = strings.Repeat("0", 64)
	if err := writeJSONPretty(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	err = fixture.service.publishResourcePhaseArtifacts(context.Background(), fixture.task, fixture.projectPath, phaseRunID, false)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("publisher tamper error = %v", err)
	}
}
