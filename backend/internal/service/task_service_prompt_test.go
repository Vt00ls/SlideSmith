package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestOneShotFullPPTMasterPromptIsNotCalledByProductionCode(t *testing.T) {
	raw, err := os.ReadFile("task_service.go")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "fullPPTMasterPrompt("); count != 0 {
		t.Fatalf("one-shot full prompt reference count = %d, want none", count)
	}
}

type prepareCommandRecordingAgent struct {
	request AgentRunRequest
}

func (a *prepareCommandRecordingAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a *prepareCommandRecordingAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	a.request = req
	sessionWorkspace, err := distinctTestAgentWorkspace(ctx, req)
	if err != nil {
		return nil, err
	}
	project := filepath.Join(sessionWorkspace, "projects", "task_template_ppt169_20260708")
	mustWriteFileNoTest(project, filepath.Join("sources", "input.md"), "# Source\n")
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-prepare",
		SessionID:     "session-prepare",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: sessionWorkspace,
	}, nil
}

func TestPrepareCommandIncludesSourceManifestAndFallbackInput(t *testing.T) {
	service, repo, task, _ := templateResolvePrepareService(t)
	agent := &prepareCommandRecordingAgent{}
	service.agent = agent

	if err := service.processPrepare(context.Background(), task); err != nil {
		t.Fatalf("processPrepare() error = %v", err)
	}

	want := "node workflows/ppt_workflow.js prepare --profile 'real-lite' --sources-manifest '.slidesmith/source_inputs.json' --input 'uploads/task-template/input.md' --project 'task_template'"
	if agent.request.Command != want {
		t.Fatalf("prepare command = %q, want %q", agent.request.Command, want)
	}

	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase == string(PhaseSourcePrepare) {
			if !strings.Contains(phaseRun.InputJSON, `"sources_manifest":".slidesmith/source_inputs.json"`) {
				t.Fatalf("source_prepare input missing sources_manifest: %s", phaseRun.InputJSON)
			}
			return
		}
	}
	t.Fatal("source_prepare phase run not found")
}

func TestSplitPPTMasterPromptsKeepPhaseBoundaries(t *testing.T) {
	service := &TaskService{
		agentCfg: config.AgentComposeConfig{WorkDir: "/workspace"},
	}
	task := &model.Task{ID: "task-1"}
	projectPath := "/workspace/projects/task_1_ppt169_20260707"

	specPrompt := service.fullPPTMasterSpecPrompt(task, projectPath)
	specRequired := []string{
		"Generate only the PPT Master strategist artifacts",
		"Create or overwrite projects/task_1_ppt169_20260707/design_spec.md",
		"Create or overwrite projects/task_1_ppt169_20260707/spec_lock.md",
		"Do not create projects/task_1_ppt169_20260707/svg_output/*.svg",
		"Do not run svg_quality_checker.py, finalize_svg.py, svg_to_pptx.py",
		"never write null or placeholders",
		"flat canonical fields prompt_or_query, source_reference",
		"stable canonical ID P01, P02",
		"must declare one required chart_template requirement and one required chart_data requirement",
		"Every chart_template requirement must have a matching chart_data requirement",
		"Never declare a chart_template for a non-data-driven timeline, roadmap, process, or decorative diagram",
		"complete purpose string verbatim from resource_plan.json",
		"do not translate, abbreviate, paraphrase, or omit the purpose",
		`use "page": 2; never use "page": "P02" or "page": "2"`,
		`fallback must be exactly one of "", "diagram", "shape", "text", "placeholder", or "omit_optional"`,
		`source_reference must be exactly the confirmed icons value followed by "/" and a safe icon name`,
		`"tabler-outline/chart-bar"`,
		`"type":"chart_data"`,
	}
	for _, want := range specRequired {
		if !strings.Contains(specPrompt, want) {
			t.Fatalf("spec prompt missing %q\n%s", want, specPrompt)
		}
	}
	if strings.Contains(specPrompt, "Create SVG pages under") {
		t.Fatalf("spec prompt should not ask for SVG output\n%s", specPrompt)
	}

	svgPrompt := service.fullPPTMasterSVGPrompt(task, projectPath)
	svgRequired := []string{
		"Generate only the PPT Master executor SVG pages",
		"Read projects/task_1_ppt169_20260707/design_spec.md",
		"Create exactly the confirmed page_count SVG pages",
		"Create projects/task_1_ppt169_20260707/notes/total.md",
		`"svg": "svg_output/01_safe_slug.svg"`,
		`"resources": []`,
		`"source_citation": {"file": "sources/input.md", "section": "Section heading"}`,
		`"plot_area": [120, 160, 1160, 620]`,
		"Never use svg_path, resource_bindings, fallback_bindings, data_hash_sha256",
		`<g id="p01-icon" data-resource-id="icon.p01.chart-bar"><image id="p01-icon-image" href="../icons/tabler-outline/chart-bar.svg"`,
		"Do not inline or copy ready image/icon SVG paths into the page",
		"an inlined icon with no href is not a valid ready-resource use",
		"Do not run svg_quality_checker.py, finalize_svg.py, svg_to_pptx.py",
		"Do not create or modify projects/task_1_ppt169_20260707/exports/",
	}
	for _, want := range svgRequired {
		if !strings.Contains(svgPrompt, want) {
			t.Fatalf("svg prompt missing %q\n%s", want, svgPrompt)
		}
	}
	if strings.Contains(svgPrompt, "Create or overwrite projects/task_1_ppt169_20260707/design_spec.md") {
		t.Fatalf("svg prompt should not ask to rewrite design spec\n%s", svgPrompt)
	}
}

func TestBeautifyStrategistAndExecutorPromptsEnforceFrozenSourceContract(t *testing.T) {
	service := &TaskService{agentCfg: config.AgentComposeConfig{WorkDir: "/workspace"}}
	task := &model.Task{ID: "beautify-task", Route: model.TaskRouteBeautify}
	projectPath := "/workspace/projects/beautify_task_ppt169_20260715"
	strategist := service.fullPPTMasterStrategistPrompt(task, projectPath)
	for _, expected := range []string{
		"Beautify task", ".slidesmith/beautify_lock.json", "P01..PN", "verbatim",
		"never add, remove, merge, split, reorder", "type \"template\" is forbidden",
		"must not use template_asset", `acquire_via "source"`, "requirements: []",
		"frozen table cell", "chart category", "required source image occurrence",
		`The only acquire_via values are "user", "template", "icon", "formula", "chart_template", "source", "web", "ai", "slice", and "placeholder"`,
		`acquire_via "render" is invalid and forbidden`,
		`chart_template->chart_template`, `chart_data->source`,
		`fallback must be exactly one of "", "diagram", "shape", "text", "placeholder", or "omit_optional"`,
		`required frozen source-deck image must use fallback ""`,
		`Never write "source placeholder"`,
		`plain-text declaration line in both markdown files`,
		`- resource image.p01.source | P01 | purpose=Required source image occurrence p01.image.01 for P01`,
		`resource ID must be a clean unquoted token`,
		`Raw JSON such as {"id":"image.p01.source",...} is not a valid Markdown declaration`,
		`JSON examples below are exclusively for .slidesmith/resource_plan.json`,
		`"type":"image"`, `"fallback":""`,
		`"type":"chart_template"`, `"acquire_via":"chart_template"`,
		`"type":"chart_data"`, `"acquire_via":"source"`,
		"skills/ppt-master/templates/charts/charts_index.json",
	} {
		if !strings.Contains(strategist, expected) {
			t.Fatalf("Beautify strategist prompt missing %q", expected)
		}
	}
	executor := service.fullPPTMasterExecutorPrompt(task, projectPath)
	for _, expected := range []string{
		"Beautify task", ".slidesmith/beautify_lock.json", `data-source-slide="NN"`,
		`data-beautify-lock-hash`, "Never change content", "never split the page",
		"exact frozen cell grid", "exact frozen categories", "required source image occurrence",
		`prefix the manifest path with "../"`, `Never paste the bare project-relative manifest path into href`,
		`notes/total.md must contain exactly one ordered section per page`, `"## PNN | Heading"`,
		`"## P01 | Cover"`, `headings must use exactly two # characters and one | separator`,
		`deck-level prose report without these per-page sections is invalid`,
		`"schema":"slidesmith.svg_resource_usage.v1"`, `"resources_manifest_sha256":"<live manifest SHA-256>"`,
		`"pages":[{"page_id":"P01"`, `"svg_sha256":"<live SVG SHA-256>"`, `resources: []`,
		`"schema":"slidesmith.chart_usage.v1"`, `"verification_mode":"direct-calc"`,
		`"source_citation":{"file":"sources/source.pptx"`, `"plot_area":[150,214,790,620]`,
		`"comparisons":[{"element_id":"bar_q1","attribute":"height"`,
		`direct-calc and formula-verify require a non-empty comparisons array`,
		`computed from the bound chart data and actual SVG geometry`,
		`series is an array of series-name strings`, `data-chart-data-resource-id`,
		`data-chart-template-resource-id`, `must not substitute generic data-resource-id`,
		`Do not use <style> elements, class attributes, CSS selectors`,
		`Every font-size value must be a unitless numeric px value`, `font-size="28px"`,
		`Never display a raster image wider or taller than the intrinsic width/height`,
		"Do not invent usages, resources_manifest path, task_id, lock_sha256, owner_id, data_href, or template_href fields",
	} {
		if !strings.Contains(executor, expected) {
			t.Fatalf("Beautify executor prompt missing %q", expected)
		}
	}
	if strategist == service.fullPPTMasterSpecPrompt(task, projectPath) || executor == service.fullPPTMasterSVGPrompt(task, projectPath) {
		t.Fatal("Beautify route reused the main prompt")
	}
}

func TestBeautifyPlanPromptPinsExactMachineSchema(t *testing.T) {
	service := &TaskService{agentCfg: config.AgentComposeConfig{WorkDir: "/workspace"}}
	task := &model.Task{ID: "beautify-task", Route: model.TaskRouteBeautify}
	prompt := service.fullPPTMasterBeautifyPlanPrompt(task, "/workspace/projects/beautify_task_ppt169_20260715")
	for _, expected := range []string{
		`"task_id": "beautify-task"`,
		`"revision": 1`,
		`"source": "source-replica"`,
		`"canvas_override": false`,
		`"accepted_risks": ["risk.id"]`,
		`"risks": ["risk.id"]`,
		`"global_ignored": []`,
		"Every array field must be a JSON array, never null",
		"arrays of risk ID strings only; objects are forbidden",
		"objects with exactly string id and reason fields",
		"do not embed canvas, theme, palette, fonts, sizes, confirmations",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Beautify plan prompt missing %q\n%s", expected, prompt)
		}
	}
}
