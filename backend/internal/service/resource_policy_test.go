package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestWriteResourcePolicySnapshotCapturesConfirmationAndDeploymentPolicy(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{
		ResourcePhaseEnabled:   true,
		ResourceNetworkEnabled: true,
		ResourceWebEnabled:     true,
		ResourceAIEnabled:      true,
		ResourceFormulaNetwork: true,
		ResourceAIPaths:        "host-native, api, API",
		ResourceWebProviders:   "wikimedia,openverse",
		ResourceAIProviders:    "openai,azure-openai",
		ResourceMaxFiles:       12,
		ResourceMaxTotalBytes:  2048,
		ResourceMaxSingleBytes: 1024,
		ResourceTimeout:        3 * time.Minute,
	})
	task := &model.Task{ID: "resource-policy-task", Route: model.TaskRouteMain}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	projectPath := t.TempDir()
	if err := writeJSONPretty(filepath.Join(projectPath, "confirm_ui", "result.json"), map[string]any{
		"page_count": 3, "image_usage": []any{"provided", "web", "ai"}, "image_ai_path": "api",
		"icons": "tabler-outline", "formula_policy": "mixed",
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := service.writeResourcePolicySnapshot(task, projectPath, "phase-resource-1")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.PhaseEnabled || !policy.NetworkEnabled || !policy.WebImageEnabled || !policy.AIImageEnabled || !policy.FormulaNetworkEnabled {
		t.Fatalf("deployment switches missing from policy: %#v", policy)
	}
	if strings.Join(policy.ConfirmationImageSources, ",") != "ai,provided,web" || strings.Join(policy.AllowedAIPaths, ",") != "api,host-native" {
		t.Fatalf("normalized policy lists = %#v", policy)
	}
	if policy.IconLibrary != "tabler-outline" || policy.FormulaPolicy != "mixed" {
		t.Fatalf("confirmation resource policy = %#v", policy)
	}
	if policy.MaxFiles != 12 || policy.MaxTotalBytes != 2048 || policy.MaxSingleBytes != 1024 || policy.TimeoutSeconds != 180 {
		t.Fatalf("policy limits = %#v", policy)
	}
	loaded, err := loadResourcePolicy(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PolicySHA256 == "" || loaded.PolicySHA256 != policy.PolicySHA256 || loaded.PhaseRunID != "phase-resource-1" {
		t.Fatalf("loaded policy = %#v", loaded)
	}
}

func TestResourcePolicyDefaultsOfflineAndFailClosed(t *testing.T) {
	service, repo := profileTestService(t, config.AgentComposeConfig{})
	task := &model.Task{ID: "resource-policy-default", Route: model.TaskRouteMain}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	projectPath := t.TempDir()
	if err := writeJSONPretty(filepath.Join(projectPath, "confirm_ui", "result.json"), map[string]any{"page_count": 1, "image_usage": []any{"web", "ai"}}); err != nil {
		t.Fatal(err)
	}
	policy, err := service.writeResourcePolicySnapshot(task, projectPath, "phase-default")
	if err != nil {
		t.Fatal(err)
	}
	if policy.PhaseEnabled || policy.NetworkEnabled || policy.WebImageEnabled || policy.AIImageEnabled || policy.FormulaNetworkEnabled {
		t.Fatalf("default policy is not offline/fail-closed: %#v", policy)
	}
	if policy.MaxFiles != 100 || policy.MaxTotalBytes != 524288000 || policy.MaxSingleBytes != 52428800 || policy.TimeoutSeconds != 1200 {
		t.Fatalf("default policy limits = %#v", policy)
	}
}

func TestResourcePolicyRejectsProfileAndDigestTampering(t *testing.T) {
	service, _ := profileTestService(t, config.AgentComposeConfig{ResourcePhaseEnabled: true})
	projectPath := t.TempDir()
	if err := writeJSONPretty(filepath.Join(projectPath, "confirm_ui", "result.json"), map[string]any{"page_count": 1}); err != nil {
		t.Fatal(err)
	}
	invalid := &model.Task{ID: "resource-policy-invalid", Route: model.TaskRouteMain}
	lockRunnerProfileForTest(invalid, model.RunnerProfileRealLite)
	if _, err := service.writeResourcePolicySnapshot(invalid, projectPath, "phase-invalid"); err == nil {
		t.Fatal("real-lite task unexpectedly received resource policy")
	}

	valid := &model.Task{ID: "resource-policy-valid", Route: model.TaskRouteMain}
	lockRunnerProfileForTest(valid, model.RunnerProfileFullPPTMaster)
	if _, err := service.writeResourcePolicySnapshot(valid, projectPath, "phase-valid"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectPath, ".slidesmith", "resource_policy.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"max_files": 100`, `"max_files": 101`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadResourcePolicy(projectPath); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tamper error = %v", err)
	}
}
