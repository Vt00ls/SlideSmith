package service

import (
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

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
