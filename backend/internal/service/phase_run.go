package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
)

const (
	PhaseRunStatusRunning   = "running"
	PhaseRunStatusSucceeded = "succeeded"
	PhaseRunStatusFailed    = "failed"
	PhaseRunStatusSkipped   = "skipped"
)

func (s *TaskService) beginPhaseRun(ctx context.Context, task *model.Task, phase PipelinePhase, runner string, input any) (*model.TaskPhaseRun, error) {
	if _, ok := PipelinePhaseDefinitionFor(phase); !ok {
		return nil, nil
	}
	now := time.Now().UTC()
	claimToken, taskStatus := taskRunOwnership(task)
	run := &model.TaskPhaseRun{
		TaskID:              task.ID,
		ExecutionClaimToken: claimToken,
		TaskStatus:          taskStatus,
		Phase:               string(phase),
		Runner:              runner,
		Status:              PhaseRunStatusRunning,
		StartedAt:           &now,
		InputJSON:           encodeAnyJSON(input),
	}
	if err := s.repo.CreatePhaseRun(ctx, run); err != nil {
		if errors.Is(err, repository.ErrTaskExecutionClaimLost) {
			return nil, errTaskStateChanged
		}
		return nil, err
	}
	return run, nil
}

func taskRunOwnership(task *model.Task) (string, string) {
	if task == nil || task.ExecutionClaimToken == "" {
		return "", ""
	}
	return task.ExecutionClaimToken, task.Status
}

func (s *TaskService) finishPhaseRun(ctx context.Context, run *model.TaskPhaseRun, status string, output any, cause error) error {
	if run == nil {
		return nil
	}
	now := time.Now().UTC()
	run.Status = status
	run.FinishedAt = &now
	if output != nil {
		run.OutputJSON = encodeAnyJSON(output)
	}
	if cause != nil {
		run.ErrorMessage = cause.Error()
		run.FailureMetadata = encodeAnyJSON(map[string]any{
			"phase":         run.Phase,
			"error_message": cause.Error(),
		})
	}
	err := s.repo.SavePhaseRun(ctx, run)
	if !errors.Is(err, repository.ErrTaskExecutionClaimLost) {
		return err
	}
	_, abandonErr := s.repo.AbandonPhaseRun(context.WithoutCancel(ctx), run, "task execution ownership changed before phase completion")
	return errors.Join(errTaskStateChanged, abandonErr)
}

func applyRuntimeRunToPhaseRun(phaseRun *model.TaskPhaseRun, runtimeRun *model.TaskRuntimeRun) {
	if phaseRun == nil || runtimeRun == nil {
		return
	}
	phaseRun.RuntimeRunID = runtimeRun.ID
	phaseRun.RuntimeSessionID = runtimeRun.ExternalSessionID
	phaseRun.WorkspacePath = runtimeRun.WorkspacePath
}

func runtimeRunPhaseOutput(runtimeRun *model.TaskRuntimeRun) map[string]any {
	if runtimeRun == nil {
		return map[string]any{}
	}
	return map[string]any{
		"runtime_run_id":        runtimeRun.ID,
		"runtime_phase":         runtimeRun.Phase,
		"runtime_status":        runtimeRun.Status,
		"external_run_id":       runtimeRun.ExternalRunID,
		"external_session_id":   runtimeRun.ExternalSessionID,
		"workspace_path":        runtimeRun.WorkspacePath,
		"runtime_failure_phase": runtimeRun.FailurePhase,
	}
}

func (s *TaskService) recordLegacyCompletedPhaseRuns(ctx context.Context, task *model.Task, runtimeRun *model.TaskRuntimeRun, phases ...PipelinePhase) error {
	for _, phase := range phases {
		phaseRun, err := s.beginPhaseRun(ctx, task, phase, PhaseRunnerLegacyAgentBundle, map[string]any{
			"bundled_by":     "generate",
			"runtime_run_id": runtimeRunID(runtimeRun),
		})
		if err != nil {
			return err
		}
		applyRuntimeRunToPhaseRun(phaseRun, runtimeRun)
		output := runtimeRunPhaseOutput(runtimeRun)
		output["legacy_bundled"] = true
		if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *TaskService) recordSkippedPhaseRun(ctx context.Context, task *model.Task, phase PipelinePhase, runner string, output any) error {
	phaseRun, err := s.beginPhaseRun(ctx, task, phase, runner, nil)
	if err != nil {
		return err
	}
	return s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSkipped, output, nil)
}

func runtimeRunID(run *model.TaskRuntimeRun) string {
	if run == nil {
		return ""
	}
	return run.ID
}

func encodeAnyJSON(value any) string {
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
