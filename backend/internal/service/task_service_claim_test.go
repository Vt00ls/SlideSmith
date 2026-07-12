package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type blockingTemplateFillCheckAgent struct {
	projectPath string
	sessionRoot string
	calls       atomic.Int32
	firstReady  chan struct{}
	release     chan struct{}
}

func TestTaskExecutionLeaseCoversAgentUpAndRunTimeouts(t *testing.T) {
	service := &TaskService{agentCfg: config.AgentComposeConfig{Timeout: 10 * time.Minute}}
	want := 25 * time.Minute
	if got := service.taskExecutionLeaseDuration(); got != want {
		t.Fatalf("lease duration = %s, want %s", got, want)
	}
}

func (a *blockingTemplateFillCheckAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a *blockingTemplateFillCheckAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	if req.Phase != string(PhaseTemplateFillCheck) {
		return nil, fmt.Errorf("unexpected phase %q", req.Phase)
	}
	call := a.calls.Add(1)
	if call == 1 {
		close(a.firstReady)
		<-a.release
	}
	sessionDir, err := os.MkdirTemp(a.sessionRoot, "claim-session-")
	if err != nil {
		return nil, err
	}
	sessionWorkspace := filepath.Join(sessionDir, "workspace")
	if err := copyDir(ctx, req.WorkDir, sessionWorkspace); err != nil {
		return nil, err
	}
	projectRel, err := filepath.Rel(req.WorkDir, a.projectPath)
	if err != nil {
		return nil, err
	}
	sessionProjectPath := filepath.Join(sessionWorkspace, projectRel)
	writeTemplateFillWorkflowJSON(sessionProjectPath, filepath.Join("analysis", "check_report.json"), map[string]any{
		"schema":  "template_fill_pptx_check.v1",
		"summary": map[string]any{"ok": 1, "warn": 0, "error": 0},
		"results": []any{},
	})
	exitCode := 0
	return &AgentRunResult{
		RunID:         fmt.Sprintf("run-check-%d", call),
		SessionID:     fmt.Sprintf("session-check-%d", call),
		Status:        "succeeded",
		ExitCode:      &exitCode,
		WorkspacePath: sessionWorkspace,
	}, nil
}

func TestProcessTaskDurableClaimAllowsOnlyOneNonIdempotentRuntime(t *testing.T) {
	agent := &blockingTemplateFillCheckAgent{
		firstReady:  make(chan struct{}),
		release:     make(chan struct{}),
		sessionRoot: t.TempDir(),
	}
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillChecking, agent)
	agent.projectPath = projectPath
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", 1)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- service.ProcessTask(context.Background(), task.ID)
	}()
	<-agent.firstReady

	secondErr := service.ProcessTask(context.Background(), task.ID)
	close(agent.release)
	firstErr := <-firstDone
	if firstErr != nil {
		t.Fatalf("first ProcessTask() error = %v", firstErr)
	}
	if secondErr != nil {
		t.Fatalf("second ProcessTask() error = %v", secondErr)
	}
	if calls := agent.calls.Load(); calls != 1 {
		t.Fatalf("formal checker runtime calls = %d, want 1", calls)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ExecutionClaimToken != "" || updated.ExecutionClaimedAt != nil {
		t.Fatalf("completed worker did not release durable claim: %#v", updated)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkRuns := 0
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase == string(PhaseTemplateFillCheck) {
			checkRuns++
		}
	}
	if checkRuns != 1 {
		t.Fatalf("formal check phase runs = %d, want 1; all=%#v", checkRuns, phaseRuns)
	}
}

func TestProcessQueuedTasksTakesOverExpiredSourceConvertingClaim(t *testing.T) {
	service, repo, task, _ := routeDispatchPrepareService(t, "套用公司模板填充新内容", []model.Artifact{
		{Name: "brand_template.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/brand_template.pptx"},
		{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/content.md"},
	})
	staleClaimedAt := time.Now().UTC().Add(-service.taskExecutionLeaseDuration() - time.Minute)
	if err := repo.DB().Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status":                model.TaskStatusSourceConverting,
		"execution_claim_token": "expired-worker-token",
		"execution_claimed_at":  staleClaimedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	processed, err := service.ProcessQueuedTasks(context.Background(), 1)
	if err != nil {
		t.Fatalf("ProcessQueuedTasks() error = %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want expired source-converting task takeover", processed)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusTemplateFillPlanning {
		t.Fatalf("status = %q, want %q", updated.Status, model.TaskStatusTemplateFillPlanning)
	}
	if updated.ExecutionClaimToken != "" || updated.ExecutionClaimedAt != nil {
		t.Fatalf("takeover worker did not release claim: %#v", updated)
	}
}

func TestSyncRuntimeProjectRejectsStaleClaimAfterSuccessorPromotion(t *testing.T) {
	service, repo, task, projectPath, workspacePath := newTemplateFillWorkflowService(t, model.TaskStatusTemplateFillApplying, nil)
	oldClaimedAt := time.Now().UTC().Add(-service.taskExecutionLeaseDuration() - time.Minute)
	claimed, err := repo.ClaimTaskExecution(context.Background(), task.ID, task.Status, "old-claim", oldClaimedAt, oldClaimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim old worker = %v, %v", claimed, err)
	}
	oldTask := *task
	oldTask.ExecutionClaimToken = "old-claim"
	oldTask.ExecutionClaimedAt = &oldClaimedAt

	successorClaimedAt := time.Now().UTC()
	claimed, err = repo.ClaimTaskExecution(context.Background(), task.ID, task.Status, "successor-claim", successorClaimedAt, successorClaimedAt.Add(-service.taskExecutionLeaseDuration()))
	if err != nil || !claimed {
		t.Fatalf("claim successor worker = %v, %v", claimed, err)
	}
	successorTask := *task
	successorTask.ExecutionClaimToken = "successor-claim"
	successorTask.ExecutionClaimedAt = &successorClaimedAt

	makeSession := func(name, purpose string) string {
		sessionWorkspace := filepath.Join(t.TempDir(), name, "workspace")
		if err := copyDir(context.Background(), workspacePath, sessionWorkspace); err != nil {
			t.Fatal(err)
		}
		plan := templateFillContractPlan("confirmed", 1)
		templateFillContractFirstSlide(plan)["purpose"] = purpose
		writeTemplateFillWorkflowJSON(filepath.Join(sessionWorkspace, "projects", filepath.Base(projectPath)), filepath.Join("analysis", "fill_plan.json"), plan)
		return sessionWorkspace
	}
	successorSession := makeSession("successor", "successor-plan")
	oldSession := makeSession("old", "stale-plan")
	workspace := service.resolveTaskWorkspace(&successorTask)
	if _, err := service.syncRuntimeProject(context.Background(), &successorTask, workspace, successorSession); err != nil {
		t.Fatalf("successor promotion error = %v", err)
	}
	if _, err := service.syncRuntimeProject(context.Background(), &oldTask, workspace, oldSession); !errors.Is(err, errTaskStateChanged) {
		t.Fatalf("stale promotion error = %v, want task state changed", err)
	}

	_, slides, _, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	purpose, _ := slides[0].(map[string]any)["purpose"].(string)
	if purpose != "successor-plan" {
		t.Fatalf("canonical plan purpose = %q, want successor-plan", purpose)
	}
	info, err := os.Lstat(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("canonical project is not a real directory: %v", info.Mode())
	}
	promotionRoot := filepath.Join(workspacePath, ".slidesmith", "project-promotions")
	entries, err := os.ReadDir(promotionRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("promotion staging leaked entries: %#v", entries)
	}
}
