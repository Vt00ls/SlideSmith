package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const (
	failurePhaseRuntimeProfileLock              = "runtime_profile.lock"
	failurePhaseRuntimeProfileMismatch          = "runtime_profile.mismatch"
	failurePhaseRuntimeProfileMigrationRequired = "runtime_profile.migration_required"
)

func configuredRunnerProfile(cfg config.AgentComposeConfig) (string, string, error) {
	requestedValue := cfg.RunnerProfile
	if strings.TrimSpace(requestedValue) == "" {
		requestedValue = model.RunnerProfileFullPPTMaster
	}
	requested, err := config.NormalizeRunnerProfile(requestedValue)
	if err != nil {
		return "", "", err
	}
	source := model.RunnerProfileSourceDeploymentDefault
	if cfg.RunnerProfileExplicit {
		source = model.RunnerProfileSourceExplicitConfig
	}
	if requested == model.RunnerProfileFullPPTMaster && !cfg.FullPPTDefaultEnabled {
		return model.RunnerProfileRealLite, source, nil
	}
	return requested, source, nil
}

func (s *TaskService) ensureTaskRunnerProfile(ctx context.Context, task *model.Task) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	if task.RunnerProfile != "" {
		normalized, err := config.NormalizeRunnerProfile(task.RunnerProfile)
		if err != nil {
			return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileLock, err)
		}
		if normalized != task.RunnerProfile {
			return fmt.Errorf("%s: stored runner profile %q is not canonical", failurePhaseRuntimeProfileLock, task.RunnerProfile)
		}
		if task.RunnerProfileSource != "" && task.RunnerProfileLockedAt != nil {
			return nil
		}
		source := task.RunnerProfileSource
		if source == "" {
			source = model.RunnerProfileSourceLegacyEvidence
		}
		return s.lockTaskRunnerProfile(ctx, task, normalized, source)
	}

	profile, source, found, err := s.resolveLegacyRunnerProfile(ctx, task)
	if err != nil {
		return err
	}
	if !found {
		if task.Route == model.TaskRouteTemplateFill {
			profile = model.RunnerProfileNativeTemplateFill
			source = model.RunnerProfileSourceLegacyEvidence
			found = true
		} else if canLockRunnerProfileFromConfiguration(task) {
			profile, source, err = configuredRunnerProfile(s.agentCfg)
			if err != nil {
				return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileLock, err)
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s: task %s reached status %q without reliable runner profile evidence", failurePhaseRuntimeProfileMigrationRequired, task.ID, task.Status)
	}
	return s.lockTaskRunnerProfile(ctx, task, profile, source)
}

func canLockRunnerProfileFromConfiguration(task *model.Task) bool {
	switch task.Status {
	case model.TaskStatusCreated, model.TaskStatusUploaded, model.TaskStatusRuntimePreparing, model.TaskStatusSourceConverting:
		return true
	case model.TaskStatusFailed:
		phase := strings.ToLower(strings.TrimSpace(task.FailurePhase))
		return phase == "" || strings.HasPrefix(phase, "prepare") || strings.HasPrefix(phase, "source_prepare") || strings.HasPrefix(phase, "runtime_profile")
	default:
		return false
	}
}

func (s *TaskService) lockTaskRunnerProfile(ctx context.Context, task *model.Task, profile, source string) error {
	normalized, err := config.NormalizeRunnerProfile(profile)
	if err != nil {
		return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileLock, err)
	}
	lockedAt := time.Now().UTC()
	locked, err := s.repo.LockTaskRunnerProfile(ctx, task.ID, []string{task.Status}, normalized, source, lockedAt)
	if err != nil {
		return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileLock, err)
	}
	if !locked {
		return fmt.Errorf("%s: task %s status changed while locking runner profile", failurePhaseRuntimeProfileLock, task.ID)
	}
	current, err := s.repo.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("%s: reload task: %w", failurePhaseRuntimeProfileLock, err)
	}
	*task = *current
	return nil
}

func (s *TaskService) resolveLegacyRunnerProfile(ctx context.Context, task *model.Task) (string, string, bool, error) {
	workspace := s.resolveTaskWorkspace(task)
	manifestPath := filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json")
	if profile, ok, err := runnerProfileFromManifest(manifestPath); err != nil {
		return "", "", false, fmt.Errorf("read legacy runtime manifest: %w", err)
	} else if ok {
		return profile, model.RunnerProfileSourceLegacyManifest, true, nil
	}

	runs, err := s.repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		return "", "", false, err
	}
	for _, run := range runs {
		if run.Runner == PhaseRunnerLegacyAgentBundle {
			return model.RunnerProfileRealLite, model.RunnerProfileSourceLegacyEvidence, true, nil
		}
		switch PipelinePhase(run.Phase) {
		case PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport, PhasePPTXValidate:
			return model.RunnerProfileFullPPTMaster, model.RunnerProfileSourceLegacyEvidence, true, nil
		case PhaseSpecGenerate:
			if run.Runner == PhaseRunnerAgent {
				return model.RunnerProfileFullPPTMaster, model.RunnerProfileSourceLegacyEvidence, true, nil
			}
		}
	}
	runtimeRuns, err := s.repo.ListRuntimeRuns(ctx, task.ID)
	if err != nil {
		return "", "", false, err
	}
	for _, run := range runtimeRuns {
		phase := strings.ToLower(strings.TrimSpace(run.Phase))
		command := strings.ToLower(run.Command)
		switch phase {
		case string(PhaseSVGExecute), string(PhaseQualityCheck), string(PhaseFinalizeExport), string(PhasePPTXValidate):
			return model.RunnerProfileFullPPTMaster, model.RunnerProfileSourceLegacyEvidence, true, nil
		case "generate", string(PhaseSpecGenerate):
			if strings.Contains(command, "--profile full-ppt-master") {
				return model.RunnerProfileFullPPTMaster, model.RunnerProfileSourceLegacyEvidence, true, nil
			}
			return model.RunnerProfileRealLite, model.RunnerProfileSourceLegacyEvidence, true, nil
		}
	}
	return "", "", false, nil
}

func runnerProfileFromManifest(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var payload struct {
		Runner struct {
			EffectiveProfile string `json:"effective_profile"`
		} `json:"runner"`
		EffectiveProfile string `json:"effective_profile"`
		RunnerProfile    string `json:"runner_profile"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", false, err
	}
	value := payload.Runner.EffectiveProfile
	if value == "" {
		value = payload.EffectiveProfile
	}
	if value == "" {
		value = payload.RunnerProfile
	}
	if strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	profile, err := config.NormalizeRunnerProfile(value)
	if err != nil {
		return "", false, err
	}
	return profile, true, nil
}

func (s *TaskService) validateTaskRuntimeProfile(task *model.Task, workspace *TaskWorkspace) error {
	if task == nil || strings.TrimSpace(task.RunnerProfile) == "" || task.RunnerProfileLockedAt == nil {
		return fmt.Errorf("%s: task runner profile is not locked", failurePhaseRuntimeProfileMismatch)
	}
	if workspace == nil {
		workspace = s.resolveTaskWorkspace(task)
	}
	manifestPath := filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json")
	profile, ok, err := runnerProfileFromManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileMismatch, err)
	}
	if !ok {
		return fmt.Errorf("%s: runtime manifest has no effective runner profile", failurePhaseRuntimeProfileMismatch)
	}
	if profile != task.RunnerProfile {
		return fmt.Errorf("%s: runtime manifest profile %q does not match task lock %q", failurePhaseRuntimeProfileMismatch, profile, task.RunnerProfile)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileMismatch, err)
	}
	var manifest runtimeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("%s: %w", failurePhaseRuntimeProfileMismatch, err)
	}
	if manifest.Schema == "slidesmith.runtime_manifest.v2" {
		if manifest.TaskID != task.ID {
			return fmt.Errorf("%s: runtime manifest task %q does not match %q", failurePhaseRuntimeProfileMismatch, manifest.TaskID, task.ID)
		}
		if manifest.Route != task.Route {
			return fmt.Errorf("%s: runtime manifest route %q does not match task route %q", failurePhaseRuntimeProfileMismatch, manifest.Route, task.Route)
		}
		lockedAt, err := time.Parse(time.RFC3339Nano, manifest.Runner.LockedAt)
		if err != nil || !lockedAt.Equal(task.RunnerProfileLockedAt.UTC()) {
			return fmt.Errorf("%s: runtime manifest lock timestamp does not match task lock", failurePhaseRuntimeProfileMismatch)
		}
	}
	return nil
}

func runnerProfileFailurePhase(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, phase := range []string{
		failurePhaseRuntimeProfileMismatch,
		failurePhaseRuntimeProfileMigrationRequired,
		failurePhaseRuntimeProfileLock,
	} {
		if strings.Contains(message, phase) {
			return phase
		}
	}
	if errors.Is(err, context.Canceled) {
		return failurePhaseRuntimeProfileLock
	}
	return failurePhaseRuntimeProfileLock
}

func fullPhaseInput(task *model.Task, workspace *TaskWorkspace, projectPath string, startedFrom PipelinePhase, upstream ...PipelinePhase) map[string]any {
	input := map[string]any{
		"task_id":            task.ID,
		"route":              task.Route,
		"runner_profile":     task.RunnerProfile,
		"project_path":       projectPath,
		"runtime_project":    task.RuntimeProject,
		"started_from_phase": string(startedFrom),
		"upstream_contracts": []string{},
	}
	if task.RunnerProfileLockedAt != nil {
		input["runner_profile_locked_at"] = task.RunnerProfileLockedAt.UTC().Format(time.RFC3339Nano)
	}
	if workspace != nil {
		input["workspace_path"] = workspace.HostDir
		if sha, err := sha256File(filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json")); err == nil {
			input["runtime_manifest_sha256"] = sha
		}
		for key, relativePath := range map[string]string{
			"skill_lock_sha256":    filepath.Join(".slidesmith", "skill_lock.json"),
			"template_lock_sha256": filepath.Join(".slidesmith", "template_lock.json"),
		} {
			if sha, err := sha256File(filepath.Join(workspace.HostDir, relativePath)); err == nil {
				input[key] = sha
			}
		}
	}
	contracts := make([]string, 0, len(upstream))
	for _, phase := range upstream {
		contracts = append(contracts, filepath.ToSlash(filepath.Join(".slidesmith", "contracts", string(phase)+".json")))
	}
	input["upstream_contracts"] = contracts
	return input
}

func fullPhaseOutput(task *model.Task, run *model.TaskRuntimeRun, projectPath string, startedFrom PipelinePhase) map[string]any {
	output := runtimeRunPhaseOutput(run)
	output["runner_profile"] = task.RunnerProfile
	output["project_path"] = projectPath
	output["started_from_phase"] = string(startedFrom)
	return output
}
