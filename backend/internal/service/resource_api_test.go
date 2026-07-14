package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetResourcesReturnsSafeManifestSummaryAndArtifactIDs(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	phaseRunID := "resource-api-ready"
	item := addPublishableResourceToAcquireFixture(t, fixture, phaseRunID)
	if err := fixture.service.publishResourcePhaseArtifacts(context.Background(), fixture.task, fixture.projectPath, phaseRunID, false); err != nil {
		t.Fatal(err)
	}
	view, err := fixture.service.GetResources(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.TaskID != fixture.task.ID || view.PhaseStatus != PhaseRunStatusSucceeded || view.Summary.Total != 1 || view.Summary.Ready != 1 {
		t.Fatalf("resource API view = %#v", view)
	}
	if len(view.Resources) != 1 || view.Resources[0].ID != item.ID || view.Resources[0].ArtifactID == "" {
		t.Fatalf("resource API items = %#v", view.Resources)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, secret := range []string{"images/publishable.png", "internal_prompt", "must never publish", "example.test", "token=secret", fixture.projectPath} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("resource API leaked %q: %s", secret, encoded)
		}
	}
}

func TestGetResourcesDoesNotReturnPreviousTaskData(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	other := *fixture.task
	other.ID = "resource-api-other-task"
	other.RuntimeProject = "resource_api_other"
	other.RuntimeWorkspacePath = ""
	if err := fixture.repo.CreateTask(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	view, err := fixture.service.GetResources(context.Background(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.TaskID != other.ID || len(view.Resources) != 0 || view.Summary.Total != 0 || view.ManifestHash != "" {
		t.Fatalf("cross-task resource data leaked: %#v", view)
	}
}

func TestSafeResourceAPIErrorRedactsProviderDetails(t *testing.T) {
	code, message := safeResourceAPIError("provider_failed", "Authorization: Bearer secret-token; prompt=confidential launch")
	if code != "provider_failed" || message != "resource_error" {
		t.Fatalf("safeResourceAPIError() = %q, %q", code, message)
	}
	code, message = safeResourceAPIError("POLICY_DENIED", "policy_denied")
	if code != "policy_denied" || message != "policy_denied" {
		t.Fatalf("safe code normalization = %q, %q", code, message)
	}
}

func TestGetResourcesRejectsManifestForAnotherTask(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.projectPath, ".slidesmith", "resources_manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest resourcesManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.TaskID = "another-task"
	if err := writeJSONPretty(path, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.GetResources(context.Background(), fixture.task.ID); err == nil || !strings.Contains(err.Error(), "binding mismatch") {
		t.Fatalf("cross-task manifest error = %v", err)
	}
}
