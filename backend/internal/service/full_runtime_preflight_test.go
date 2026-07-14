package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func fullPreflightFixture(t *testing.T) (*TaskService, *model.Task, *TaskWorkspace) {
	t.Helper()
	root := t.TempDir()
	workspace := &TaskWorkspace{
		HostDir:     root,
		ComposeFile: filepath.Join(root, "agent-compose.yml"),
		SkillDir:    filepath.Join(root, "skills", "ppt-master"),
	}
	mustWriteFile(t, workspace.ComposeFile, "agents:\n  ppt_master: {}\n")
	for _, rel := range []string{
		"SKILL.md",
		filepath.Join("references", "strategist.md"),
		filepath.Join("references", "executor-base.md"),
		filepath.Join("templates", "design_spec_reference.md"),
		filepath.Join("templates", "spec_lock_reference.md"),
		filepath.Join("scripts", "project_manager.py"),
		filepath.Join("scripts", "svg_quality_checker.py"),
		filepath.Join("scripts", "finalize_svg.py"),
		filepath.Join("scripts", "svg_to_pptx.py"),
	} {
		mustWriteFile(t, filepath.Join(workspace.SkillDir, rel), "fixture\n")
	}
	mustWriteFile(t, filepath.Join(root, "scripts", "ppt_runner.py"), "print('fixture')\n")
	task := &model.Task{ID: "preflight-task", Route: model.TaskRouteMain}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	service := &TaskService{agentCfg: config.AgentComposeConfig{
		Enabled:                true,
		FullPPTPreflightStrict: true,
	}}
	return service, task, workspace
}

func TestFullRuntimePreflightWritesPassingContracts(t *testing.T) {
	service, task, workspace := fullPreflightFixture(t)
	report, err := service.runFullRuntimePreflight(context.Background(), task, workspace)
	if err != nil {
		t.Fatalf("runFullRuntimePreflight() error = %v", err)
	}
	if report.Schema != fullRuntimePreflightSchema || report.Summary.Error != 0 || report.Summary.Pass == 0 {
		t.Fatalf("preflight report = %#v", report)
	}
	for _, path := range []string{
		filepath.Join(workspace.HostDir, ".slidesmith", "full_runtime_preflight.json"),
		filepath.Join(workspace.HostDir, ".slidesmith", "contracts", "full_runtime_preflight.json"),
	} {
		assertPathExists(t, path)
	}
}

func TestFullRuntimePreflightBlocksMissingRequiredScript(t *testing.T) {
	service, task, workspace := fullPreflightFixture(t)
	missing := filepath.Join(workspace.SkillDir, "scripts", "svg_to_pptx.py")
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	report, err := service.runFullRuntimePreflight(context.Background(), task, workspace)
	if err == nil {
		t.Fatal("missing required script did not block strict preflight")
	}
	if report.Summary.Error != 1 {
		t.Fatalf("preflight error summary = %#v", report.Summary)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "svg_to_pptx" && check.Status == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing script check not reported: %#v", report.Checks)
	}
}

func TestFullRuntimePreflightRejectsTemplateSymlinkEscape(t *testing.T) {
	service, task, workspace := fullPreflightFixture(t)
	outside := filepath.Join(t.TempDir(), "outside-template")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace.SkillDir, "templates", "layouts", "escape")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	task.SelectedTemplateID = "layout:escape"
	task.TemplateLockJSON = `{"template_id":"layout:escape","kind":"layout","name":"escape"}`
	report, err := service.runFullRuntimePreflight(context.Background(), task, workspace)
	if err == nil || report.Summary.Error == 0 {
		t.Fatalf("template symlink escape report=%#v error=%v", report, err)
	}
}
