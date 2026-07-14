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
