package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type prepareCommandRecordingAgent struct {
	request AgentRunRequest
}

func (a *prepareCommandRecordingAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a *prepareCommandRecordingAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	a.request = req
	project := filepath.Join(req.WorkDir, "projects", "task_template_ppt169_20260708")
	mustWriteFileNoTest(project, filepath.Join("sources", "input.md"), "# Source\n")
	exitCode := 0
	return &AgentRunResult{
		RunID:         "run-prepare",
		SessionID:     "session-prepare",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: req.WorkDir,
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

func TestFullPPTMasterPromptPreservesSkillBoundaries(t *testing.T) {
	service := &TaskService{
		agentCfg: config.AgentComposeConfig{WorkDir: "/workspace"},
	}
	task := &model.Task{ID: "task-1"}

	prompt := service.fullPPTMasterPrompt(task, "/workspace/projects/task_1_ppt169_20260707")

	required := []string{
		"Read .slidesmith/runtime_manifest.json first",
		"skills/ppt-master/SKILL.md",
		"Follow the PPT Master mandatory serial workflow rules",
		"Match the confirmed page_count exactly",
		"SVG pages must be hand-authored one page at a time",
		"Do not write or run Python/Node/shell generators",
		"python3 skills/ppt-master/scripts/svg_quality_checker.py",
		"python3 skills/ppt-master/scripts/finalize_svg.py",
		"python3 skills/ppt-master/scripts/svg_to_pptx.py",
		"python3 scripts/ppt_runner.py publish",
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}

	forbidden := []string{
		"small Python scripts to write",
		"target 3 slides unless",
	}
	for _, needle := range forbidden {
		if strings.Contains(prompt, needle) {
			t.Fatalf("prompt contains forbidden phrase %q\n%s", needle, prompt)
		}
	}
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
