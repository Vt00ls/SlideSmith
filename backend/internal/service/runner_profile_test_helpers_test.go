package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func lockRunnerProfileForTest(task *model.Task, profile string) {
	lockedAt := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	task.RunnerProfile = profile
	task.RunnerProfileSource = model.RunnerProfileSourceExplicitConfig
	task.RunnerProfileLockedAt = &lockedAt
	if task.Route == "" {
		task.Route = model.TaskRouteMain
	}
}

func mustWriteEmptyResourcePlanNoTest(projectPath, taskID string, pageCount int) {
	designSHA, err := sha256File(filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		panic(err)
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, "spec_lock.md"))
	if err != nil {
		panic(err)
	}
	confirmationSHA, err := sha256File(filepath.Join(projectPath, "confirm_ui", "result.json"))
	if err != nil {
		panic(err)
	}
	plan := resourcePlan{
		Schema: resourcePlanSchema, TaskID: taskID, PageCount: pageCount,
		SpecSHA256: designSHA, SpecLockSHA256: lockSHA, ConfirmationSHA256: confirmationSHA,
		Requirements: []resourceRequirement{},
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"), plan); err != nil {
		panic(err)
	}
}

func mustWriteEmptyResourceManifestNoTest(projectPath, taskID, phaseRunID string) {
	plan, _, err := validateResourcePlanContract(projectPath, taskID)
	if err != nil {
		panic(err)
	}
	policyPath := filepath.Join(projectPath, ".slidesmith", "resource_policy.json")
	policy, err := loadResourcePolicy(projectPath)
	if err != nil {
		policy = &resourcePolicySnapshot{
			Schema: resourcePolicySchema, TaskID: taskID, Route: model.TaskRouteMain,
			RunnerProfile: model.RunnerProfileFullPPTMaster, RunnerProfileLockedAt: time.Now().UTC().Format(time.RFC3339Nano),
			PhaseRunID: phaseRunID, PhaseEnabled: true, MaxFiles: 100,
			MaxTotalBytes: 524288000, MaxSingleBytes: 52428800, TimeoutSeconds: 1200,
			AllowedAIPaths: []string{"api"}, AllowedWebProviders: []string{"openverse", "wikimedia"},
			FallbackRules: map[string]string{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		policy.ConfirmationSHA256 = plan.ConfirmationSHA256
		policy.PolicySHA256, err = resourcePolicyDigest(policy)
		if err != nil {
			panic(err)
		}
		if err := writeJSONPretty(policyPath, policy); err != nil {
			panic(err)
		}
	}
	planSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		panic(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "resource_requirements.json"), map[string]any{
		"schema": "slidesmith.resource_requirements.v1", "task_id": taskID,
		"policy_sha256": policy.PolicySHA256, "requirements": []any{},
	}); err != nil {
		panic(err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "image_analysis.csv"), "No,Filename,Width,Height\n")
	manifest := resourcesManifest{
		Schema: resourcesManifestSchema, TaskID: taskID, Route: model.TaskRouteMain,
		RunnerProfile: model.RunnerProfileFullPPTMaster, ProjectPath: "projects/" + filepath.Base(projectPath),
		ResourcePlanSHA256: planSHA, PolicySHA256: policy.PolicySHA256,
		SpecSHA256: plan.SpecSHA256, SpecLockSHA256: plan.SpecLockSHA256,
		PhaseRunID: phaseRunID, Resources: []resourceManifestItem{},
		Summary: resourceManifestSummary{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"), manifest); err != nil {
		panic(err)
	}
}

func mustWriteEmptyResourceContractNoTest(projectPath, workspaceRoot, taskID, phaseRunID string) {
	mustWriteEmptyResourcePlanNoTest(projectPath, taskID, confirmedPageCount(projectPath))
	mustWriteEmptyResourceManifestNoTest(projectPath, taskID, phaseRunID)
	manifestSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"))
	if err != nil {
		panic(err)
	}
	planSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		panic(err)
	}
	contract := map[string]any{
		"phase": string(PhaseImageAcquire), "runner_profile": model.RunnerProfileFullPPTMaster,
		"resources_manifest_sha256": manifestSHA, "resource_plan_sha256": planSHA,
		"phase_run_id": phaseRunID,
	}
	if runtimeSHA, err := sha256File(filepath.Join(workspaceRoot, ".slidesmith", "runtime_manifest.json")); err == nil {
		contract["runtime_manifest_sha256"] = runtimeSHA
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseImageAcquire)+".json"), contract); err != nil {
		panic(err)
	}
}

func writeRuntimeProfileManifestForTest(t *testing.T, workspaceRoot string, task *model.Task) {
	t.Helper()
	workspacePath := filepath.Join(workspaceRoot, task.RuntimeProject)
	manifest := runtimeManifest{
		Schema:        "slidesmith.runtime_manifest.v2",
		SchemaVersion: 2,
		TaskID:        task.ID,
		Route:         task.Route,
		Runner: runtimeManifestRunner{
			RequestedProfile: task.RunnerProfile,
			EffectiveProfile: task.RunnerProfile,
			Source:           task.RunnerProfileSource,
			LockedAt:         task.RunnerProfileLockedAt.UTC().Format(time.RFC3339Nano),
		},
	}
	if err := writeJSONPretty(filepath.Join(workspacePath, ".slidesmith", "runtime_manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
}
