package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const fullRuntimePreflightSchema = "slidesmith.full_runtime_preflight.v1"

type fullRuntimePreflightCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type fullRuntimePreflightSummary struct {
	Pass    int `json:"pass"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
}

type fullRuntimePreflightReport struct {
	Schema           string                      `json:"schema"`
	EffectiveProfile string                      `json:"effective_profile"`
	Checks           []fullRuntimePreflightCheck `json:"checks"`
	Summary          fullRuntimePreflightSummary `json:"summary"`
	CheckedAt        string                      `json:"checked_at"`
}

func (s *TaskService) runFullRuntimePreflight(ctx context.Context, task *model.Task, workspace *TaskWorkspace) (*fullRuntimePreflightReport, error) {
	if workspace == nil {
		workspace = s.resolveTaskWorkspace(task)
	}
	report := &fullRuntimePreflightReport{
		Schema:           fullRuntimePreflightSchema,
		EffectiveProfile: task.RunnerProfile,
		CheckedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
	add := func(name, path string, required bool, err error) {
		check := fullRuntimePreflightCheck{Name: name, Required: required, Path: filepath.ToSlash(path)}
		switch {
		case err == nil:
			check.Status = "pass"
			report.Summary.Pass++
		case required:
			check.Status = "error"
			check.Message = err.Error()
			report.Summary.Error++
		default:
			check.Status = "warning"
			check.Message = err.Error()
			report.Summary.Warning++
		}
		report.Checks = append(report.Checks, check)
	}

	if s.agentCfg.Enabled {
		add("agent_compose_enabled", "", true, nil)
	} else {
		add("agent_compose_enabled", "", true, fmt.Errorf("agent-compose is disabled"))
	}
	add("compose_file", workspace.ComposeFile, true, requireReadableRegularFile(workspace.ComposeFile))
	for _, item := range []struct {
		name string
		rel  string
	}{
		{"ppt_master_skill", "SKILL.md"},
		{"strategist_reference", filepath.Join("references", "strategist.md")},
		{"executor_reference", filepath.Join("references", "executor-base.md")},
		{"design_spec_reference", filepath.Join("templates", "design_spec_reference.md")},
		{"spec_lock_reference", filepath.Join("templates", "spec_lock_reference.md")},
		{"project_manager", filepath.Join("scripts", "project_manager.py")},
		{"analyze_images", filepath.Join("scripts", "analyze_images.py")},
		{"icon_sync", filepath.Join("scripts", "icon_sync.py")},
		{"latex_render", filepath.Join("scripts", "latex_render.py")},
		{"image_search", filepath.Join("scripts", "image_search.py")},
		{"image_gen", filepath.Join("scripts", "image_gen.py")},
		{"slice_images", filepath.Join("scripts", "slice_images.py")},
		{"svg_quality_checker", filepath.Join("scripts", "svg_quality_checker.py")},
		{"finalize_svg", filepath.Join("scripts", "finalize_svg.py")},
		{"svg_to_pptx", filepath.Join("scripts", "svg_to_pptx.py")},
	} {
		path := filepath.Join(workspace.SkillDir, item.rel)
		add(item.name, path, true, requireReadableRegularFile(path))
	}
	add("publisher_script", filepath.Join(workspace.HostDir, "scripts", "ppt_runner.py"), true, requireReadableRegularFile(filepath.Join(workspace.HostDir, "scripts", "ppt_runner.py")))
	add("resource_runner", filepath.Join(workspace.HostDir, "scripts", "resource_runner.py"), true, requireReadableRegularFile(filepath.Join(workspace.HostDir, "scripts", "resource_runner.py")))

	add("runtime_python3", "python3", false, fmt.Errorf("validated inside the agent runtime during full prepare"))
	add("runtime_python_imports", "pptx,PIL", false, fmt.Errorf("validated inside the agent runtime during full prepare"))

	projectsDir := filepath.Join(workspace.HostDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		add("workspace_project_root", projectsDir, true, err)
	} else if file, err := os.CreateTemp(projectsDir, ".preflight-*"); err != nil {
		add("workspace_project_root", projectsDir, true, err)
	} else {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		add("workspace_project_root", projectsDir, true, nil)
	}

	if lock, ok, err := decodeTemplateLock(task.TemplateLockJSON); err != nil {
		add("template_lock", ".slidesmith/template_lock.json", true, err)
	} else if ok {
		root := filepath.Join(workspace.HostDir, filepath.FromSlash(templateLockTemplateRoot(lock)))
		add("template_root", root, true, requireContainedDirectory(workspace.SkillDir, root))
	}

	if err := writeFullRuntimePreflightReport(workspace, report); err != nil {
		return report, fmt.Errorf("write full runtime preflight report: %w", err)
	}
	if report.Summary.Error > 0 && s.agentCfg.FullPPTPreflightStrict {
		return report, fmt.Errorf("full runtime preflight found %d required capability error(s)", report.Summary.Error)
	}
	return report, nil
}

func writeFullRuntimePreflightReport(workspace *TaskWorkspace, report *fullRuntimePreflightReport) error {
	if workspace == nil {
		return fmt.Errorf("workspace is nil")
	}
	paths := []string{
		filepath.Join(workspace.HostDir, ".slidesmith", "full_runtime_preflight.json"),
		filepath.Join(workspace.HostDir, ".slidesmith", "contracts", "full_runtime_preflight.json"),
	}
	for _, path := range paths {
		if err := writeJSONPretty(path, report); err != nil {
			return err
		}
	}
	return nil
}

func requireReadableRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("not a non-empty regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func requireContainedDirectory(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path is outside allowed root")
	}
	info, err := os.Stat(pathAbs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return err
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resolved path escapes allowed root")
	}
	return nil
}

func validateRuntimeFullPreflightContract(projectPath, effectiveProfile string) (*fullRuntimePreflightReport, error) {
	path := filepath.Join(projectPath, ".slidesmith", "contracts", "full_runtime_preflight.json")
	if err := requireReadableRegularFile(path); err != nil {
		return nil, fmt.Errorf("runtime full preflight contract: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var report fullRuntimePreflightReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	if report.Schema != fullRuntimePreflightSchema {
		return nil, fmt.Errorf("runtime full preflight schema = %q", report.Schema)
	}
	if report.EffectiveProfile != effectiveProfile {
		return nil, fmt.Errorf("runtime full preflight profile = %q, expected %q", report.EffectiveProfile, effectiveProfile)
	}
	if report.Summary.Error != 0 {
		return &report, fmt.Errorf("runtime full preflight has %d required errors", report.Summary.Error)
	}
	return &report, nil
}
