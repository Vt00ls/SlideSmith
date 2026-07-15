package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func (s *TaskService) processResourceAcquire(ctx context.Context, task *model.Task) error {
	if !s.useFullPPTMaster(task) || !isFullSVGRoute(task.Route) {
		err := fmt.Errorf("resource acquisition requires a locked full-ppt-master full SVG route task")
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".unsupported_profile", err, nil, nil)
		return err
	}
	workspace := s.resolveTaskWorkspace(task)
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{"started_from_phase": string(PhaseImageAcquire)})
		return err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".project", err, nil, nil)
		return err
	}
	if _, err := validateExistingSpecContract(projectPath, task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".spec_contract", err, nil, map[string]any{"project_path": projectPath})
		return err
	}
	if _, _, err := validateResourcePlanContract(projectPath, task.ID); err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".plan", err, nil, map[string]any{"project_path": projectPath})
		return err
	}
	expectedPlanSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".plan", err, nil, map[string]any{"project_path": projectPath})
		return err
	}
	input := fullPhaseInput(task, workspace, projectPath, PhaseImageAcquire, PhaseSpecGenerate)
	input["resource_plan_sha256"] = expectedPlanSHA
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseImageAcquire, PhaseRunnerWorker, input)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire), err, nil, map[string]any{"project_path": projectPath})
		return err
	}
	policy, err := s.writeResourcePolicySnapshot(task, projectPath, phaseRun.ID)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, nil, err)
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".policy", err, nil, map[string]any{"project_path": projectPath})
		return err
	}
	input["resource_policy_sha256"] = policy.PolicySHA256
	input["network_enabled"] = policy.NetworkEnabled
	input["web_image_enabled"] = policy.WebImageEnabled
	input["ai_image_enabled"] = policy.AIImageEnabled
	phaseRun.InputJSON = encodeJSON(input)
	if err := s.repo.SavePhaseRun(ctx, phaseRun); err != nil {
		return err
	}
	if !policy.PhaseEnabled {
		err := fmt.Errorf("resource_phase_disabled")
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, map[string]any{"policy_sha256": policy.PolicySHA256}, err)
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".policy", err, nil, map[string]any{"policy_sha256": policy.PolicySHA256})
		return err
	}
	projectRel := s.projectRel(task, projectPath)
	command := fmt.Sprintf(
		"python3 scripts/resource_runner.py %s --skill-root %s --phase-run-id %s",
		shellArg(projectRel), shellArg("skills/ppt-master"), shellArg(phaseRun.ID),
	)
	run, runErr := s.runAgent(ctx, task, string(PhaseImageAcquire), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	if runErr != nil && run != nil {
		sanitizeResourceRuntimeRun(run)
		_ = s.saveRuntimeRun(context.WithoutCancel(ctx), run)
	}
	applyRuntimeRunToPhaseRun(phaseRun, run)
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		if !errors.Is(persistErr, errTaskStateChanged) {
			_ = s.failWithMetadata(context.WithoutCancel(ctx), task, string(PhaseImageAcquire)+".runtime", persistErr, run, map[string]any{"workspace_path": workspace.HostDir})
		}
		return persistErr
	}
	if run != nil && run.WorkspacePath != "" {
		if syncedPath, syncErr := s.syncRuntimeProject(ctx, task, workspace, run.WorkspacePath); syncErr == nil {
			projectPath = syncedPath
		} else if runErr == nil {
			runErr = syncErr
		}
	}
	if runErr != nil {
		safeRunErr := fmt.Errorf("resource runtime command failed")
		_ = s.publishResourcePhaseArtifacts(context.WithoutCancel(ctx), task, projectPath, phaseRun.ID, true)
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), safeRunErr)
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".command", safeRunErr, resourceFailureRun(run), map[string]any{
			"project_path":  projectPath,
			"policy_sha256": policy.PolicySHA256,
		})
		return runErr
	}
	if _, err := validateExistingSpecContract(projectPath, task, workspace); err != nil {
		_ = s.publishResourcePhaseArtifacts(context.WithoutCancel(ctx), task, projectPath, phaseRun.ID, true)
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".upstream_mutation", err, resourceFailureRun(run), map[string]any{"project_path": projectPath})
		return err
	}
	contract, err := validateResourceManifestContractWithBindings(projectPath, task, phaseRun.ID, expectedPlanSHA, policy.PolicySHA256)
	if err == nil {
		contract, err = bindFullPhaseContract(projectPath, PhaseImageAcquire, contract, task, workspace, runtimeRunID(run))
	}
	if err != nil {
		_ = s.publishResourcePhaseArtifacts(context.WithoutCancel(ctx), task, projectPath, phaseRun.ID, true)
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".contract", err, resourceFailureRun(run), map[string]any{"project_path": projectPath})
		return err
	}
	if err := s.publishResourcePhaseArtifacts(ctx, task, projectPath, phaseRun.ID, false); err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, contract, err)
		_ = s.failWithMetadata(ctx, task, string(PhaseImageAcquire)+".publish", err, resourceFailureRun(run), map[string]any{"project_path": projectPath})
		return err
	}
	output := fullPhaseOutput(task, run, projectPath, PhaseImageAcquire)
	output["contract"] = contract
	output["resources_manifest"] = ".slidesmith/resources_manifest.json"
	output["resources_manifest_sha256"] = contract["resources_manifest_sha256"]
	output["resource_summary"] = contract["resource_summary"]
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return err
	}
	if err := s.transition(ctx, task, model.TaskStatusSVGGenerating, "SVG executing", map[string]any{
		"resource_phase_run_id":     phaseRun.ID,
		"resources_manifest_sha256": contract["resources_manifest_sha256"],
		"resource_summary":          contract["resource_summary"],
	}); err != nil {
		return err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "resource_ready", "Resource manifest validated; SVG generation queued", map[string]any{
		"phase_run_id":              phaseRun.ID,
		"resources_manifest_sha256": contract["resources_manifest_sha256"],
	})
	return nil
}

func sanitizeResourceRuntimeRun(run *model.TaskRuntimeRun) {
	if run == nil {
		return
	}
	run.RawResponse = ""
	run.StderrTail = ""
	run.ErrorMessage = "resource runtime command failed"
	run.FailureMetadata = encodeAnyJSON(map[string]any{
		"phase":         run.FailurePhase,
		"error_message": run.ErrorMessage,
	})
}

// Resource provider output can contain prompts, URLs with credentials, or
// provider diagnostics. Persist runtime identity for audit while withholding
// stdout/stderr tails from task failure metadata and API responses.
func resourceFailureRun(run *model.TaskRuntimeRun) *model.TaskRuntimeRun {
	if run == nil {
		return nil
	}
	clean := *run
	clean.StderrTail = ""
	clean.RawResponse = ""
	clean.ErrorMessage = "resource runtime command failed"
	return &clean
}
