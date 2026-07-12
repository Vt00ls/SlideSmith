package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type cancelBeforeTaskUpdate struct {
	db        *gorm.DB
	taskID    string
	remaining atomic.Int32
	once      sync.Once
	done      chan error
}

func installCancelBeforeTaskUpdate(t *testing.T, db *gorm.DB, taskID string) *cancelBeforeTaskUpdate {
	t.Helper()
	hook := &cancelBeforeTaskUpdate{
		db:     db,
		taskID: taskID,
		done:   make(chan error, 1),
	}
	name := "test:cancel-before-task-update:" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (model.Task{}).TableName() {
			return
		}
		for {
			remaining := hook.remaining.Load()
			if remaining <= 0 {
				return
			}
			if !hook.remaining.CompareAndSwap(remaining, remaining-1) {
				continue
			}
			if remaining-1 != 0 {
				return
			}
			hook.once.Do(func() {
				cancelledAt := time.Now().UTC()
				err := hook.db.Session(&gorm.Session{SkipHooks: true}).
					Model(&model.Task{}).
					Where("id = ?", hook.taskID).
					Updates(map[string]any{
						"status":       model.TaskStatusCancelled,
						"cancelled_at": cancelledAt,
					}).Error
				hook.done <- err
			})
			return
		}
	}); err != nil {
		t.Fatal(err)
	}
	return hook
}

func (h *cancelBeforeTaskUpdate) Arm(updateNumber int32) {
	h.remaining.Store(updateNumber)
}

func (h *cancelBeforeTaskUpdate) Wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("inject cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation hook did not run")
	}
}

func TestTemplateFillRuntimeResultCASDoesNotResurrectCancellation(t *testing.T) {
	agent := &templateFillWorkflowAgent{}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, agent)
	agent.projectPath = projectPath
	prepareTemplateFillWorkflowPhase(t, projectPath, model.TaskStatusTemplateFillApplying)
	hook := installCancelBeforeTaskUpdate(t, repo.DB(), task.ID)
	agent.onPhase = func(phase string) error {
		if phase == string(PhaseTemplateFillApply) {
			hook.Arm(1)
		}
		return nil
	}

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() cancellation loss error = %v", err)
	}
	hook.Wait(t)
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusCancelled || updated.CancelledAt == nil {
		t.Fatalf("late runtime result resurrected cancelled task: %#v", updated)
	}
	phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseTemplateFillApply)
	if phaseRun.Status == PhaseRunStatusRunning {
		t.Fatalf("cancelled runtime left active phase run: %#v", phaseRun)
	}
}

type sourcePrepareCASAgent struct {
	arm         func()
	sessionRoot string
}

func (a sourcePrepareCASAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a sourcePrepareCASAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	result, err := (successfulRoutePrepareAgent{sessionRoot: a.sessionRoot}).Run(ctx, req)
	if a.arm != nil {
		a.arm()
	}
	return result, err
}

func TestSourcePrepareCASPreservesCancellationAtResultAndRouteTransitionBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		updateNumber int32
	}{
		{name: "runtime result persistence", updateNumber: 1},
		{name: "template fill route transition", updateNumber: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, _ := routeDispatchPrepareService(t, "套用公司模板填充新内容", []model.Artifact{
				{Name: "brand_template.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/brand_template.pptx"},
				{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/content.md"},
			})
			hook := installCancelBeforeTaskUpdate(t, repo.DB(), task.ID)
			service.agent = sourcePrepareCASAgent{
				arm:         func() { hook.Arm(test.updateNumber) },
				sessionRoot: t.TempDir(),
			}

			if err := service.processPrepare(context.Background(), task); err != nil {
				t.Fatalf("processPrepare() cancellation loss error = %v", err)
			}
			hook.Wait(t)
			updated, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != model.TaskStatusCancelled || updated.CancelledAt == nil {
				t.Fatalf("source prepare boundary resurrected cancellation: %#v", updated)
			}
			phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, phaseRun := range phaseRuns {
				if phaseRun.Status == PhaseRunStatusRunning {
					t.Fatalf("source prepare cancellation left running phase: %#v", phaseRun)
				}
			}
		})
	}
}

func claimTaskForCASTest(t *testing.T, repo *repository.Repository, task *model.Task, token string) {
	t.Helper()
	claimedAt := time.Now().UTC()
	claimed, err := repo.ClaimTaskExecution(context.Background(), task.ID, task.Status, token, claimedAt, claimedAt.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("task execution claim was not acquired")
	}
	task.ExecutionClaimToken = token
	task.ExecutionClaimedAt = &claimedAt
}

func replaceTaskClaimForCASTest(t *testing.T, repo *repository.Repository, taskID, token string) {
	t.Helper()
	claimedAt := time.Now().UTC()
	if err := repo.DB().Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{
		"execution_claim_token": token,
		"execution_claimed_at":  claimedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestApplyRuntimeRunToTaskRejectsReplacementClaimToken(t *testing.T) {
	service, repo, task, _, _ := newTemplateFillWorkflowService(t, model.TaskStatusSVGGenerating, nil)
	claimTaskForCASTest(t, repo, task, "original-token")
	replaceTaskClaimForCASTest(t, repo, task.ID, "successor-token")
	run := &model.TaskRuntimeRun{
		ExternalRunID:     "late-run",
		ExternalSessionID: "late-session",
		WorkspacePath:     "/late/workspace",
	}

	err := service.applyRuntimeRunToTask(context.Background(), task, run)
	if !errors.Is(err, errTaskStateChanged) {
		t.Fatalf("applyRuntimeRunToTask() error = %v, want task state changed", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "successor-token" {
		t.Fatalf("late runtime result adopted replacement token: %#v", persisted)
	}
	if persisted.LastRuntimeRunID != "" || persisted.LastRuntimeSessionID != "" || persisted.RuntimeWorkspacePath == "/late/workspace" {
		t.Fatalf("late runtime result overwrote successor metadata: %#v", persisted)
	}
}

func TestFailTaskAfterSourcePrepareDoesNotAdoptReplacementClaimToken(t *testing.T) {
	service, repo, task, _, _ := newTemplateFillWorkflowService(t, model.TaskStatusSourceConverting, nil)
	claimTaskForCASTest(t, repo, task, "original-token")
	replaceTaskClaimForCASTest(t, repo, task.ID, "successor-token")

	_ = service.failTaskAfterSourcePrepare(context.Background(), task, "source_prepare.test", errors.New("injected failure"), nil, nil)
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "successor-token" || persisted.Status != model.TaskStatusSourceConverting {
		t.Fatalf("failure retry adopted replacement claim: %#v", persisted)
	}
}

func TestRecoverSourcePrepareFailureStopsOnClaimLoss(t *testing.T) {
	service, repo, task, _, _ := newTemplateFillWorkflowService(t, model.TaskStatusSourceConverting, nil)
	claimTaskForCASTest(t, repo, task, "original-token")
	replaceTaskClaimForCASTest(t, repo, task.ID, "successor-token")

	err := service.recoverSourcePrepareFailure(
		context.Background(),
		task,
		nil,
		"source_prepare.test",
		errors.New("injected failure"),
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("recoverSourcePrepareFailure() claim loss error = %v, want clean stop", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionClaimToken != "successor-token" || persisted.Status != model.TaskStatusSourceConverting {
		t.Fatalf("source prepare recovery changed successor task: %#v", persisted)
	}
}

type legacyGenerateCASAgent struct {
	arm func()
}

func (a *legacyGenerateCASAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a *legacyGenerateCASAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	if a.arm != nil {
		a.arm()
	}
	exitCode := 0
	return &AgentRunResult{
		RunID:         "late-generate-run",
		SessionID:     "late-generate-session",
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: req.WorkDir,
	}, nil
}

func TestLegacyGenerateRuntimeResultDoesNotResurrectCancellation(t *testing.T) {
	agent := &legacyGenerateCASAgent{}
	service, repo, task, _, _ := newTemplateFillWorkflowService(t, model.TaskStatusSpecGenerating, agent)
	hook := installCancelBeforeTaskUpdate(t, repo.DB(), task.ID)
	agent.arm = func() { hook.Arm(1) }

	processErr := service.ProcessTask(context.Background(), task.ID)
	hook.Wait(t)
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusCancelled || persisted.CancelledAt == nil {
		t.Fatalf("legacy runtime result resurrected cancellation (ProcessTask error = %v): %#v", processErr, persisted)
	}
	if processErr != nil && !errors.Is(processErr, errTaskStateChanged) {
		t.Fatalf("ProcessTask() error = %v, want nil or task state changed", processErr)
	}
	phaseRun := requireSingleTemplateFillPhaseRun(t, repo, task.ID, PhaseSpecGenerate)
	if phaseRun.Status == PhaseRunStatusRunning {
		t.Fatalf("cancelled legacy runtime left active phase run: %#v", phaseRun)
	}
}

func TestWorkerServiceDoesNotUseUnconditionalTaskSaves(t *testing.T) {
	for _, name := range []string{"task_service.go", "runtime_recovery.go", "route_select.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), ".repo.SaveTask(") {
			t.Errorf("%s contains an unconditional worker-reachable task save", name)
		}
	}
}
