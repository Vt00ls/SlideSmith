package service

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type resourceManifestFixture struct {
	projectPath string
	task        *model.Task
	phaseRunID  string
	policy      *resourcePolicySnapshot
	manifest    resourcesManifest
}

func newResourceManifestFixture(t *testing.T, requirement resourceRequirement, item resourceManifestItem) *resourceManifestFixture {
	t.Helper()
	projectPath, plan := resourcePlanTestProject(t, []resourceRequirement{requirement}, []any{"provided", "web", "ai"})
	task := &model.Task{ID: plan.TaskID, Route: model.TaskRouteMain}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	phaseRunID := "resource-phase-test"
	service, repo := profileTestService(t, config.AgentComposeConfig{ResourcePhaseEnabled: true})
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	policy, err := service.writeResourcePolicySnapshot(task, projectPath, phaseRunID)
	if err != nil {
		t.Fatal(err)
	}
	planSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "resource_requirements.json"), map[string]any{
		"schema": "slidesmith.resource_requirements.v1", "task_id": task.ID,
		"policy_sha256": policy.PolicySHA256, "requirements": []any{map[string]any{"id": requirement.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(projectPath, "analysis", "image_analysis.csv"), "No,Filename,Width,Height\n")
	manifest := resourcesManifest{
		Schema: resourcesManifestSchema, TaskID: task.ID, Route: model.TaskRouteMain,
		RunnerProfile: task.RunnerProfile, ProjectPath: "projects/" + filepath.Base(projectPath),
		ResourcePlanSHA256: planSHA, PolicySHA256: policy.PolicySHA256,
		SpecSHA256: plan.SpecSHA256, SpecLockSHA256: plan.SpecLockSHA256,
		PhaseRunID: phaseRunID, Resources: []resourceManifestItem{item},
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	fixture := &resourceManifestFixture{projectPath: projectPath, task: task, phaseRunID: phaseRunID, policy: policy, manifest: manifest}
	fixture.recomputeSummary()
	fixture.write(t)
	return fixture
}

func (fixture *resourceManifestFixture) recomputeSummary() {
	summary := resourceManifestSummary{Total: len(fixture.manifest.Resources)}
	for _, item := range fixture.manifest.Resources {
		switch item.Status {
		case "ready":
			summary.Ready++
			if item.Output != nil {
				summary.Bytes += item.Output.Size
			}
		case "degraded":
			summary.Degraded++
		case "failed":
			summary.Failed++
			if item.Required {
				summary.RequiredFailed++
			}
		case "planned", "pending", "running", "stale":
			summary.Pending++
		}
	}
	fixture.manifest.Summary = summary
}

func (fixture *resourceManifestFixture) write(t *testing.T) {
	t.Helper()
	if err := writeJSONPretty(filepath.Join(fixture.projectPath, ".slidesmith", "resources_manifest.json"), fixture.manifest); err != nil {
		t.Fatal(err)
	}
}

func writeResourcePNG(t *testing.T, projectPath, relative string) *resourceManifestOutput {
	t.Helper()
	path := filepath.Join(projectPath, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	return &resourceManifestOutput{Path: relative, MimeType: "image/png", Size: info.Size(), SHA256: sha, Width: 2, Height: 1}
}

func readyManifestRequirement() (resourceRequirement, resourceManifestItem) {
	requirement := resourceRequirement{
		ID: "res-ready", Page: 1, Type: "image", Purpose: "hero", Required: true,
		AcquireVia: "user", OutputName: "ready.png", SourceReference: "sources/ready.png",
	}
	item := resourceManifestItem{
		ID: requirement.ID, Page: requirement.Page, Type: requirement.Type, Purpose: requirement.Purpose,
		Required: requirement.Required, AcquireVia: requirement.AcquireVia, Status: "ready", Attempt: 1,
		Input: map[string]any{}, Provenance: map[string]any{}, CacheKey: "cache-ready", Publishable: true,
	}
	return requirement, item
}

func TestValidateResourceManifestContractAcceptsReadyResource(t *testing.T) {
	requirement, item := readyManifestRequirement()
	fixture := newResourceManifestFixture(t, requirement, item)
	fixture.manifest.Resources[0].Output = writeResourcePNG(t, fixture.projectPath, "images/ready.png")
	fixture.recomputeSummary()
	fixture.write(t)
	contract, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID)
	if err != nil {
		t.Fatalf("validateResourceManifestContract() error = %v", err)
	}
	if contract["resources_manifest_sha256"] == "" || contract["phase_run_id"] != fixture.phaseRunID {
		t.Fatalf("resource manifest contract = %#v", contract)
	}
	assertPathExists(t, filepath.Join(fixture.projectPath, ".slidesmith", "contracts", "image_acquire.json"))
}

func TestValidateResourceManifestContractAcceptsApprovedOptionalDegradation(t *testing.T) {
	requirement := resourceRequirement{ID: "res-web", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "web", Fallback: "diagram", OutputName: "web.png"}
	item := resourceManifestItem{
		ID: requirement.ID, Page: requirement.Page, Type: requirement.Type, Purpose: requirement.Purpose,
		AcquireVia: requirement.AcquireVia, Status: "degraded", Attempt: 1, CacheKey: "cache-web",
		Input: map[string]any{}, Provenance: map[string]any{}, Fallback: resourceManifestFallback{Type: "diagram", Reason: "policy_denied"},
	}
	fixture := newResourceManifestFixture(t, requirement, item)
	if _, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID); err != nil {
		t.Fatalf("approved degradation rejected: %v", err)
	}
}

func TestValidateResourceManifestContractBlocksRequiredFailedAndNonTerminal(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "failed", want: "remains failed"},
		{status: "pending", want: "remains non-terminal"},
		{status: "stale", want: "remains non-terminal"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			requirement, item := readyManifestRequirement()
			item.Status = test.status
			item.Error = map[string]any{"code": "fixture_failure", "message": "safe error"}
			fixture := newResourceManifestFixture(t, requirement, item)
			_, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateResourceManifestContractRejectsUnapprovedDegradation(t *testing.T) {
	requirement := resourceRequirement{ID: "res-web", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "web", OutputName: "web.png"}
	item := resourceManifestItem{
		ID: requirement.ID, Page: 1, Type: "image", Purpose: "hero", AcquireVia: "web", Status: "degraded",
		Attempt: 1, CacheKey: "cache-web", Input: map[string]any{}, Provenance: map[string]any{},
		Fallback: resourceManifestFallback{Type: "diagram", Reason: "policy_denied"},
	}
	fixture := newResourceManifestFixture(t, requirement, item)
	_, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID)
	if err == nil {
		t.Fatal("manifest accepted a fallback not approved by resource plan")
	}
}

func TestValidateResourceManifestContractRejectsOutputTamperingAndPolicyOverflow(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *resourceManifestFixture)
		want   string
	}{
		{name: "path escape", mutate: func(t *testing.T, fixture *resourceManifestFixture) {
			fixture.manifest.Resources[0].Output.Path = "../outside.png"
		}, want: "escapes project"},
		{name: "sha mismatch", mutate: func(t *testing.T, fixture *resourceManifestFixture) {
			fixture.manifest.Resources[0].Output.SHA256 = strings.Repeat("0", 64)
		}, want: "SHA-256 mismatch"},
		{name: "mime mismatch", mutate: func(t *testing.T, fixture *resourceManifestFixture) {
			fixture.manifest.Resources[0].Output.MimeType = "image/jpeg"
		}, want: "MIME"},
		{name: "dimensions mismatch", mutate: func(t *testing.T, fixture *resourceManifestFixture) {
			fixture.manifest.Resources[0].Output.Width = 99
		}, want: "dimensions mismatch"},
		{name: "single file overflow", mutate: func(t *testing.T, fixture *resourceManifestFixture) {
			fixture.policy.MaxSingleBytes = 1
			fixture.policy.PolicySHA256 = ""
			fixture.policy.PolicySHA256, _ = resourcePolicyDigest(fixture.policy)
			if err := writeJSONPretty(filepath.Join(fixture.projectPath, ".slidesmith", "resource_policy.json"), fixture.policy); err != nil {
				t.Fatal(err)
			}
			fixture.manifest.PolicySHA256 = fixture.policy.PolicySHA256
		}, want: "policy overflow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirement, item := readyManifestRequirement()
			fixture := newResourceManifestFixture(t, requirement, item)
			fixture.manifest.Resources[0].Output = writeResourcePNG(t, fixture.projectPath, "images/ready.png")
			test.mutate(t, fixture)
			fixture.recomputeSummary()
			fixture.write(t)
			_, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateResourceOutputRejectsUnsafeSVG(t *testing.T) {
	projectPath := t.TempDir()
	path := filepath.Join(projectPath, "images", "unsafe.svg")
	mustWriteFile(t, path, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	output := &resourceManifestOutput{Path: "images/unsafe.svg", MimeType: "image/svg+xml", Size: info.Size(), SHA256: sha}
	if err := validateResourceOutput(projectPath, output, 1024*1024); err == nil || !strings.Contains(err.Error(), "script or external") {
		t.Fatalf("unsafe SVG error = %v", err)
	}
}

func TestValidateResourceManifestContractEnforcesPurposeFallbackAndProjectBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*resourceManifestFixture)
	}{
		{name: "purpose", mutate: func(fixture *resourceManifestFixture) { fixture.manifest.Resources[0].Purpose = "changed" }},
		{name: "fallback", mutate: func(fixture *resourceManifestFixture) { fixture.manifest.Resources[0].Fallback.Type = "shape" }},
		{name: "project", mutate: func(fixture *resourceManifestFixture) { fixture.manifest.ProjectPath = "projects/other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirement, item := readyManifestRequirement()
			fixture := newResourceManifestFixture(t, requirement, item)
			fixture.manifest.Resources[0].Output = writeResourcePNG(t, fixture.projectPath, "images/ready.png")
			test.mutate(fixture)
			fixture.recomputeSummary()
			fixture.write(t)
			if _, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID); err == nil {
				t.Fatalf("manifest %s binding mismatch was accepted", test.name)
			}
		})
	}
}

func TestValidateResourceManifestContractRejectsPolicyFromAnotherRoute(t *testing.T) {
	requirement, item := readyManifestRequirement()
	fixture := newResourceManifestFixture(t, requirement, item)
	fixture.manifest.Resources[0].Output = writeResourcePNG(t, fixture.projectPath, "images/ready.png")
	fixture.policy.Route = model.TaskRouteBeautify
	fixture.policy.PolicySHA256 = ""
	var err error
	fixture.policy.PolicySHA256, err = resourcePolicyDigest(fixture.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONPretty(filepath.Join(fixture.projectPath, ".slidesmith", "resource_policy.json"), fixture.policy); err != nil {
		t.Fatal(err)
	}
	fixture.manifest.PolicySHA256 = fixture.policy.PolicySHA256
	fixture.recomputeSummary()
	fixture.write(t)
	if _, err := validateResourceManifestContract(fixture.projectPath, fixture.task, fixture.phaseRunID); err == nil || !strings.Contains(err.Error(), "route") {
		t.Fatalf("policy route binding error = %v", err)
	}
}
