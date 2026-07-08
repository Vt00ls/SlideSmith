package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type templateResolution struct {
	SchemaVersion      int          `json:"schema_version"`
	TaskID             string       `json:"task_id"`
	Status             string       `json:"status"`
	ProjectPath        string       `json:"project_path,omitempty"`
	SelectedTemplateID string       `json:"selected_template_id,omitempty"`
	TemplateRoot       string       `json:"template_root,omitempty"`
	TemplateLockPath   string       `json:"template_lock_path,omitempty"`
	TemplateLock       TemplateLock `json:"template_lock,omitempty"`
	ResolvedAt         string       `json:"resolved_at"`
}

func (s *TaskService) runTemplateResolve(ctx context.Context, task *model.Task, workspace *TaskWorkspace, projectPath string) (*templateResolution, error) {
	input := map[string]any{
		"task_id":              task.ID,
		"workspace_path":       workspace.HostDir,
		"project_path":         projectPath,
		"selected_template_id": task.SelectedTemplateID,
		"has_template_lock":    task.TemplateLockJSON != "" && task.TemplateLockJSON != "{}",
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseTemplateResolve, PhaseRunnerRule, input)
	if err != nil {
		return nil, err
	}
	resolution, err := resolveWorkspaceTemplate(task, workspace, projectPath)
	if err == nil {
		err = writeJSONPretty(filepath.Join(workspace.HostDir, ".slidesmith", "template_resolution.json"), resolution)
	}
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, nil, err)
		return nil, err
	}
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, resolution, nil); err != nil {
		return nil, err
	}
	return resolution, nil
}

func resolveWorkspaceTemplate(task *model.Task, workspace *TaskWorkspace, projectPath string) (*templateResolution, error) {
	projectRel := ""
	if projectPath != "" {
		if rel, err := filepath.Rel(workspace.HostDir, projectPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			projectRel = filepath.ToSlash(rel)
		} else {
			projectRel = projectPath
		}
	}
	resolution := &templateResolution{
		SchemaVersion: 1,
		TaskID:        task.ID,
		Status:        "unlocked",
		ProjectPath:   projectRel,
		ResolvedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	lock, ok, err := decodeTemplateLock(task.TemplateLockJSON)
	if err != nil {
		return nil, err
	}
	if !ok {
		if task.SelectedTemplateID != "" {
			return nil, fmt.Errorf("selected_template_id %q is set but template_lock_json is empty", task.SelectedTemplateID)
		}
		return resolution, nil
	}
	rootRel := templateLockTemplateRoot(lock)
	if rootRel == "" {
		return nil, fmt.Errorf("cannot resolve template root for %q", lock.TemplateID)
	}
	rootPath := filepath.Join(workspace.HostDir, filepath.FromSlash(rootRel))
	if info, err := os.Stat(rootPath); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return nil, fmt.Errorf("resolved template root missing %s: %w", rootPath, err)
	}
	lockPath := filepath.Join(workspace.HostDir, ".slidesmith", "template_lock.json")
	if info, err := os.Stat(lockPath); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return nil, fmt.Errorf("template lock file missing %s: %w", lockPath, err)
	}
	resolution.Status = "resolved"
	resolution.SelectedTemplateID = lock.TemplateID
	resolution.TemplateRoot = rootRel
	resolution.TemplateLockPath = ".slidesmith/template_lock.json"
	resolution.TemplateLock = lock
	return resolution, nil
}

func templateResolvePhaseInput(task *model.Task, workspace *TaskWorkspace, projectPath string) map[string]any {
	input := map[string]any{
		"workspace_path":  workspace.HostDir,
		"project_path":    projectPath,
		"runtime_project": task.RuntimeProject,
	}
	if task.SelectedTemplateID != "" {
		input["selected_template_id"] = task.SelectedTemplateID
		input["template_resolution"] = ".slidesmith/template_resolution.json"
		input["template_lock"] = ".slidesmith/template_lock.json"
	}
	return input
}
