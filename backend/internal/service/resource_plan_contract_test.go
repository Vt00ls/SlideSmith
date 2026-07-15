package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func resourcePlanTestProject(t *testing.T, requirements []resourceRequirement, imageUsage any) (string, resourcePlan) {
	t.Helper()
	projectPath := t.TempDir()
	ids := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		ids = append(ids, requirement.ID)
	}
	declarationRows := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		declarationRows = append(declarationRows, "| "+requirement.ID+" | "+strconv.Itoa(requirement.Page)+" | "+requirement.Purpose+" |")
	}
	declarations := strings.Join(declarationRows, "\n")
	mustWriteFile(t, filepath.Join(projectPath, "design_spec.md"), "# Design Spec\n\n"+declarations+"\n")
	mustWriteFile(t, filepath.Join(projectPath, "spec_lock.md"), "# Spec Lock\n\npage_count: 3\n"+declarations+"\n")
	confirmation := map[string]any{
		"page_count": 3, "image_usage": imageUsage, "image_ai_path": "api",
		"icons": "tabler-outline", "formula_policy": "render-all",
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "confirm_ui", "result.json"), confirmation); err != nil {
		t.Fatal(err)
	}
	designSHA, err := sha256File(filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, "spec_lock.md"))
	if err != nil {
		t.Fatal(err)
	}
	confirmationSHA, err := sha256File(filepath.Join(projectPath, "confirm_ui", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := resourcePlan{
		Schema: resourcePlanSchema, TaskID: "resource-plan-task", PageCount: 3,
		SpecSHA256: designSHA, SpecLockSHA256: lockSHA, ConfirmationSHA256: confirmationSHA,
		Requirements: requirements,
	}
	writeResourcePlanForTest(t, projectPath, plan)
	return projectPath, plan
}

func writeResourcePlanForTest(t *testing.T, projectPath string, plan resourcePlan) {
	t.Helper()
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"), plan); err != nil {
		t.Fatal(err)
	}
}

func TestValidateResourcePlanContractAcceptsValidPlan(t *testing.T) {
	requirements := []resourceRequirement{
		{ID: "res-user-photo", Page: 1, Type: "image", Purpose: "hero", Required: true, AcquireVia: "user", OutputName: "hero.png", SourceReference: "sources/hero.png"},
		{ID: "res-formula", Page: 2, Type: "formula", Purpose: "equation", Required: true, AcquireVia: "formula", OutputName: "equation.svg", Expression: "E = mc^2"},
		{ID: "res-chart-data", Page: 3, Type: "chart_data", Purpose: "trend", AcquireVia: "source", OutputName: "trend.json", SourceReference: "sources/data.csv", Citation: map[string]any{"source": "data.csv"}},
	}
	projectPath, _ := resourcePlanTestProject(t, requirements, []any{"provided"})
	plan, contract, err := validateResourcePlanContract(projectPath, "resource-plan-task")
	if err != nil {
		t.Fatalf("validateResourcePlanContract() error = %v", err)
	}
	if len(plan.Requirements) != len(requirements) || contract["resource_count"] != len(requirements) {
		t.Fatalf("resource plan contract = %#v", contract)
	}
	if contract["resource_plan_sha256"] == "" {
		t.Fatal("resource plan contract omitted SHA-256")
	}
}

func TestValidateResourcePlanContractAllowsLockedLayoutWithEmptyRequirements(t *testing.T) {
	projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{}, nil)
	plan, contract, err := validateResourcePlanContract(projectPath, "resource-plan-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requirements) != 0 || contract["resource_count"] != 0 {
		t.Fatalf("empty locked-layout resource plan = %#v", contract)
	}
}

func TestValidateResourcePlanContractRejectsTemplateLayoutShellWithActionableError(t *testing.T) {
	requirement := resourceRequirement{
		ID: "template.p01.cover", Page: 1, Type: "template", Purpose: "Whole cover layout shell",
		Required: true, AcquireVia: "template", OutputName: "cover.svg", SourceReference: "layouts/cover.svg",
	}
	projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
	_, _, err := validateResourcePlanContract(projectPath, "resource-plan-task")
	if err == nil {
		t.Fatal("type template layout shell was accepted")
	}
	for _, expected := range []string{"template.p01.cover", `unsupported type "template"`, "template_lock/template_resolution", "template_asset", "acquire_via"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("actionable template boundary error missing %q: %v", expected, err)
		}
	}
}

func TestFullPPTMasterSpecPromptDefinesTemplateResourceBoundary(t *testing.T) {
	service := &TaskService{}
	task := &model.Task{ID: "prompt-task", Route: model.TaskRouteMain}
	prompt := service.fullPPTMasterSpecPrompt(task, "/workspace/projects/prompt-task")
	for _, expected := range []string{
		"locked template", "layout shells", "requirements: []", "type \"template\" is invalid",
		"image, illustration_sheet, illustration_slice, icon, formula, chart_template, chart_data, template_asset, and placeholder",
		"template_resolution.template_root", "acquire_via \"template\"",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("main strategist prompt missing %q", expected)
		}
	}
}

func TestBindGeneratedResourcePlanHashesUsesLiveFilesAndPreservesRequirements(t *testing.T) {
	requirement := resourceRequirement{
		ID: "res-web-hero", Page: 1, Type: "image", Purpose: "cover hero",
		AcquireVia: "web", Fallback: "diagram", OutputName: "hero.png",
		PromptOrQuery: "secure offline presentation pipeline",
		Placement:     "right-side hero field with diagram fallback",
		Citation:      "strategist declaration only",
		Parameters:    "professional editorial photography; no AI substitution",
	}
	projectPath, plan := resourcePlanTestProject(t, []resourceRequirement{requirement}, []any{"web"})
	plan.SpecSHA256 = ""
	plan.SpecLockSHA256 = ""
	plan.ConfirmationSHA256 = ""
	writeResourcePlanForTest(t, projectPath, plan)

	if err := bindGeneratedResourcePlanHashes(projectPath, plan.TaskID); err != nil {
		t.Fatalf("bindGeneratedResourcePlanHashes() error = %v", err)
	}
	bound, _, err := validateResourcePlanContract(projectPath, plan.TaskID)
	if err != nil {
		t.Fatalf("bound resource plan contract error = %v", err)
	}
	if len(bound.Requirements) != 1 || bound.Requirements[0].PromptOrQuery != requirement.PromptOrQuery {
		t.Fatalf("bound requirements = %#v", bound.Requirements)
	}
}

func TestValidateResourcePlanContractAcceptsStringChartCitation(t *testing.T) {
	requirement := resourceRequirement{
		ID: "res-chart-data", Page: 2, Type: "chart_data", Purpose: "trend",
		AcquireVia: "source", OutputName: "trend.json", SourceReference: "sources/data.csv",
		Citation: "sources/data.csv table 1",
	}
	projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
	if _, _, err := validateResourcePlanContract(projectPath, "resource-plan-task"); err != nil {
		t.Fatalf("string chart citation contract error = %v", err)
	}
}

func TestMarkdownResourceDeclarationMatchesSplitLockSections(t *testing.T) {
	markdown := `## images
- res-cover-hero: optional web image | purpose=cover hero

## resource_requirements
- res-cover-hero: optional web image for P01 cover hero | type=image | fallback=diagram
`
	item := resourceRequirement{ID: "res-cover-hero", Page: 1, Purpose: "cover hero"}
	if !markdownResourceDeclarationMatches(markdown, item) {
		t.Fatal("split spec lock declaration did not match stable ID, page, and purpose")
	}
	item.Purpose = "different purpose"
	if markdownResourceDeclarationMatches(markdown, item) {
		t.Fatal("split spec lock declaration matched the wrong purpose")
	}
}

func TestBindGeneratedResourcePlanHashesDoesNotRepairSemanticEnvelope(t *testing.T) {
	projectPath, plan := resourcePlanTestProject(t, nil, nil)
	plan.TaskID = "different-task"
	plan.SpecSHA256 = ""
	writeResourcePlanForTest(t, projectPath, plan)
	before, err := os.ReadFile(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bindGeneratedResourcePlanHashes(projectPath, "resource-plan-task"); err == nil || !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("semantic envelope error = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("hash binder modified an invalid semantic envelope")
	}
}

func TestValidateResourcePlanContractRejectsDuplicateAndInvalidIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want string
	}{
		{name: "duplicate", ids: []string{"res-hero", "res-hero"}, want: "duplicate id"},
		{name: "invalid", ids: []string{"Res/Hero"}, want: "invalid id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirements := make([]resourceRequirement, 0, len(test.ids))
			for index, id := range test.ids {
				requirements = append(requirements, resourceRequirement{ID: id, Page: index + 1, Type: "image", Purpose: "hero", AcquireVia: "placeholder", OutputName: id + ".png"})
			}
			projectPath, _ := resourcePlanTestProject(t, requirements, nil)
			_, _, err := validateResourcePlanContract(projectPath, "resource-plan-task")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateResourcePlanContractRejectsDuplicateOutputNames(t *testing.T) {
	requirements := []resourceRequirement{
		{ID: "res-one", Page: 1, Type: "image", Purpose: "one", AcquireVia: "placeholder", OutputName: "shared.png"},
		{ID: "res-two", Page: 2, Type: "image", Purpose: "two", AcquireVia: "placeholder", OutputName: "SHARED.PNG"},
	}
	projectPath, _ := resourcePlanTestProject(t, requirements, nil)
	if _, _, err := validateResourcePlanContract(projectPath, "resource-plan-task"); err == nil || !strings.Contains(err.Error(), "share output_name") {
		t.Fatalf("duplicate output error = %v", err)
	}
}

func TestValidateResourcePlanContractRejectsPageAndHashMismatch(t *testing.T) {
	requirement := resourceRequirement{ID: "res-hero", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "placeholder", OutputName: "hero.png"}
	projectPath, plan := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)

	plan.PageCount = 2
	writeResourcePlanForTest(t, projectPath, plan)
	if _, _, err := validateResourcePlanContract(projectPath, plan.TaskID); err == nil || !strings.Contains(err.Error(), "page_count") {
		t.Fatalf("page mismatch error = %v", err)
	}

	plan.PageCount = 3
	plan.SpecSHA256 = strings.Repeat("0", 64)
	writeResourcePlanForTest(t, projectPath, plan)
	if _, _, err := validateResourcePlanContract(projectPath, plan.TaskID); err == nil || !strings.Contains(err.Error(), "hash binding mismatch") {
		t.Fatalf("hash mismatch error = %v", err)
	}
}

func TestValidateResourcePlanContractEnforcesConfirmationForWebAndAI(t *testing.T) {
	for _, acquireVia := range []string{"web", "ai"} {
		t.Run(acquireVia, func(t *testing.T) {
			requirement := resourceRequirement{ID: "res-" + acquireVia, Page: 1, Type: "image", Purpose: "hero", AcquireVia: acquireVia, OutputName: acquireVia + ".png", Fallback: "diagram"}
			projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{requirement}, []any{"provided"})
			_, _, err := validateResourcePlanContract(projectPath, "resource-plan-task")
			if err == nil || !strings.Contains(err.Error(), "not allowed by confirmation") {
				t.Fatalf("confirmation error = %v", err)
			}
		})
	}
}

func TestValidateResourcePlanContractRejectsUnsafeOutputName(t *testing.T) {
	requirement := resourceRequirement{ID: "res-unsafe", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "placeholder", OutputName: "../outside.png"}
	projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
	_, _, err := validateResourcePlanContract(projectPath, "resource-plan-task")
	if err == nil || !strings.Contains(err.Error(), "safe basename") {
		t.Fatalf("unsafe output error = %v", err)
	}
}

func TestValidateResourcePlanContractEnforcesFormulaChartAndSliceRules(t *testing.T) {
	tests := []struct {
		name         string
		requirements []resourceRequirement
		want         string
	}{
		{name: "formula expression", requirements: []resourceRequirement{{ID: "res-formula", Page: 1, Type: "formula", Purpose: "formula", AcquireVia: "formula", OutputName: "formula.svg"}}, want: "no expression"},
		{name: "chart source", requirements: []resourceRequirement{{ID: "res-chart", Page: 1, Type: "chart_data", Purpose: "chart", AcquireVia: "source", OutputName: "chart.json", Data: map[string]any{"value": 1}, Citation: map[string]any{"source": "generated"}}}, want: "project sources"},
		{name: "chart citation", requirements: []resourceRequirement{{ID: "res-chart", Page: 1, Type: "chart_data", Purpose: "chart", AcquireVia: "source", OutputName: "chart.json", SourceReference: "sources/data.csv"}}, want: "source citation"},
		{name: "slice parent", requirements: []resourceRequirement{{ID: "res-slice", Page: 1, Type: "illustration_slice", Purpose: "slice", AcquireVia: "slice", OutputName: "slice.png", ParentID: "res-missing"}}, want: "slice parent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath, _ := resourcePlanTestProject(t, test.requirements, nil)
			_, _, err := validateResourcePlanContract(projectPath, "resource-plan-task")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateResourcePlanContractEnforcesConfirmedFormulaAndIconPolicies(t *testing.T) {
	t.Run("formula text-only", func(t *testing.T) {
		requirement := resourceRequirement{ID: "res-formula", Page: 1, Type: "formula", Purpose: "equation", AcquireVia: "formula", OutputName: "formula.svg", Expression: "x=1"}
		projectPath, plan := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
		confirmationPath := filepath.Join(projectPath, "confirm_ui", "result.json")
		if err := writeJSONPretty(confirmationPath, map[string]any{"page_count": 3, "formula_policy": "text-only", "icons": "tabler-outline"}); err != nil {
			t.Fatal(err)
		}
		confirmationSHA, _ := sha256File(confirmationPath)
		plan.ConfirmationSHA256 = confirmationSHA
		writeResourcePlanForTest(t, projectPath, plan)
		if _, _, err := validateResourcePlanContract(projectPath, plan.TaskID); err == nil || !strings.Contains(err.Error(), "formula_policy") {
			t.Fatalf("formula policy error = %v", err)
		}
	})
	t.Run("icon library", func(t *testing.T) {
		requirement := resourceRequirement{ID: "res-icon", Page: 1, Type: "icon", Purpose: "signal", AcquireVia: "icon", OutputName: "signal.svg", SourceReference: "lucide-outline/signal"}
		projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
		if _, _, err := validateResourcePlanContract(projectPath, "resource-plan-task"); err == nil || !strings.Contains(err.Error(), "confirmed icon library") {
			t.Fatalf("icon policy error = %v", err)
		}
	})
}

func TestValidateResourcePlanContractRequiresMarkdownAndSpecLockConsistency(t *testing.T) {
	requirement := resourceRequirement{ID: "res-hero", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "placeholder", OutputName: "hero.png"}
	projectPath, plan := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
	mustWriteFile(t, filepath.Join(projectPath, "spec_lock.md"), "# Spec Lock\n\npage_count: 3\n")
	lockSHA, err := sha256File(filepath.Join(projectPath, "spec_lock.md"))
	if err != nil {
		t.Fatal(err)
	}
	plan.SpecLockSHA256 = lockSHA
	writeResourcePlanForTest(t, projectPath, plan)
	_, _, err = validateResourcePlanContract(projectPath, plan.TaskID)
	if err == nil || !strings.Contains(err.Error(), "not consistently declared") {
		t.Fatalf("markdown consistency error = %v", err)
	}
}

func TestResourcePlanRejectsUnknownJSONFields(t *testing.T) {
	requirement := resourceRequirement{ID: "res-hero", Page: 1, Type: "image", Purpose: "hero", AcquireVia: "placeholder", OutputName: "hero.png"}
	projectPath, _ := resourcePlanTestProject(t, []resourceRequirement{requirement}, nil)
	path := filepath.Join(projectPath, ".slidesmith", "resource_plan.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["unexpected"] = true
	if err := writeJSONPretty(path, value); err != nil {
		t.Fatal(err)
	}
	// Forward-compatible top-level fields are intentionally ignored; the canonical
	// schema/version and all security-sensitive fields remain strictly validated.
	if _, _, err := validateResourcePlanContract(projectPath, "resource-plan-task"); err != nil {
		t.Fatalf("forward-compatible field rejected: %v", err)
	}
}
