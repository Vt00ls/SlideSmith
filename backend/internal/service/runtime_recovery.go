package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type generatedRuntimeWorkspace struct {
	WorkspacePath      string
	ProjectPath        string
	SessionID          string
	LatestArtifactPath string
	LatestArtifactTime time.Time
	HasManifest        bool
}

func (s *TaskService) tryRecoverGeneratedRuntimeArtifacts(ctx context.Context, task *model.Task, workspace *TaskWorkspace, run *model.TaskRuntimeRun, reason string) (bool, error) {
	candidates, err := s.findGeneratedRuntimeWorkspaceCandidates(ctx, task)
	if err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		return false, nil
	}
	candidate := candidates[0]
	task.RuntimeWorkspacePath = candidate.WorkspacePath
	task.LastRuntimeSessionID = candidate.SessionID
	if run != nil {
		if run.ExternalRunID != "" {
			task.LastRuntimeRunID = run.ExternalRunID
		}
	}
	if err := s.repo.SaveTask(ctx, task); err != nil {
		return true, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "recovered", "Recovered generated artifacts from runtime workspace", map[string]any{
		"reason":               reason,
		"workspace_path":       candidate.WorkspacePath,
		"project_path":         candidate.ProjectPath,
		"session_id":           candidate.SessionID,
		"latest_artifact_path": candidate.LatestArtifactPath,
		"latest_artifact_at":   candidate.LatestArtifactTime.Format(time.RFC3339Nano),
		"has_manifest":         candidate.HasManifest,
	})
	if err := s.recordLegacyCompletedPhaseRuns(ctx, task, run, PhaseImageAcquire, PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport); err != nil {
		return true, err
	}
	for _, status := range []string{
		model.TaskStatusImageAcquiring,
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
		model.TaskStatusPublishing,
	} {
		if err := s.transition(ctx, task, status, status, map[string]any{
			"recovered":      true,
			"workspace_path": candidate.WorkspacePath,
			"session_id":     candidate.SessionID,
		}); err != nil {
			return true, err
		}
	}
	return true, s.processPublish(ctx, task, workspace, map[string]any{
		"recovered":      true,
		"workspace_path": candidate.WorkspacePath,
		"session_id":     candidate.SessionID,
	})
}

func (s *TaskService) findGeneratedRuntimeWorkspaceCandidates(ctx context.Context, task *model.Task) ([]generatedRuntimeWorkspace, error) {
	sessionsDir := s.agentComposeSessionsDir()
	if sessionsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent-compose sessions dir %s: %w", sessionsDir, err)
	}
	runtimeProject := task.RuntimeProject
	if runtimeProject == "" {
		runtimeProject = runtimeProjectName(task.ID)
	}
	var candidates []generatedRuntimeWorkspace
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		sessionID := entry.Name()
		workspacePath := filepath.Join(sessionsDir, sessionID, "workspace")
		projectCandidates, err := runtimeProjectCandidates(workspacePath, runtimeProject)
		if err != nil {
			return nil, err
		}
		for _, projectPath := range projectCandidates {
			candidate, ok, err := generatedWorkspaceCandidate(workspacePath, projectPath, sessionID)
			if err != nil {
				return nil, err
			}
			if ok {
				candidates = append(candidates, candidate)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LatestArtifactTime.After(candidates[j].LatestArtifactTime)
	})
	return candidates, nil
}

func (s *TaskService) agentComposeSessionsDir() string {
	root := strings.TrimSpace(s.agentCfg.SessionDataRoot)
	if root == "" {
		return ""
	}
	if filepath.Base(filepath.Clean(root)) == "sessions" {
		return root
	}
	return filepath.Join(root, "sessions")
}

func runtimeProjectCandidates(workspacePath, runtimeProject string) ([]string, error) {
	projectsDir := filepath.Join(workspacePath, "projects")
	var candidates []string
	matches, err := filepath.Glob(filepath.Join(projectsDir, runtimeProject+"_ppt169_*"))
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, matches...)
	direct := filepath.Join(projectsDir, runtimeProject)
	if info, err := os.Stat(direct); err == nil && info.IsDir() {
		candidates = append(candidates, direct)
	}
	return candidates, nil
}

func generatedWorkspaceCandidate(workspacePath, projectPath, sessionID string) (generatedRuntimeWorkspace, bool, error) {
	latestPPTX, latestPPTXTime, err := newestRegularFile(filepath.Join(projectPath, "exports", "*.pptx"))
	if err != nil {
		return generatedRuntimeWorkspace{}, false, err
	}
	if latestPPTX == "" {
		return generatedRuntimeWorkspace{}, false, nil
	}
	hasManifest := fileExists(filepath.Join(workspacePath, ".slidesmith", "artifacts.json")) ||
		fileExists(filepath.Join(projectPath, ".slidesmith", "artifacts.json"))
	hasContract := fileExists(filepath.Join(projectPath, "design_spec.md")) ||
		fileExists(filepath.Join(projectPath, "spec_lock.md"))
	if !hasManifest && !hasContract {
		return generatedRuntimeWorkspace{}, false, nil
	}
	return generatedRuntimeWorkspace{
		WorkspacePath:      workspacePath,
		ProjectPath:        projectPath,
		SessionID:          sessionID,
		LatestArtifactPath: latestPPTX,
		LatestArtifactTime: latestPPTXTime,
		HasManifest:        hasManifest,
	}, true, nil
}

func newestRegularFile(pattern string) (string, time.Time, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", time.Time{}, err
	}
	var newest string
	var newestTime time.Time
	for _, match := range matches {
		info, err := os.Stat(match)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", time.Time{}, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = match
			newestTime = info.ModTime()
		}
	}
	return newest, newestTime, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
