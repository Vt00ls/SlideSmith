package service

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type blockingTemplateFillCheckAgent struct {
	projectPath string
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

func (a *blockingTemplateFillCheckAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	if req.Phase != string(PhaseTemplateFillCheck) {
		return nil, fmt.Errorf("unexpected phase %q", req.Phase)
	}
	call := a.calls.Add(1)
	if call == 1 {
		close(a.firstReady)
		<-a.release
	}
	writeTemplateFillWorkflowJSON(a.projectPath, filepath.Join("analysis", "check_report.json"), map[string]any{
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
		WorkspacePath: req.WorkDir,
	}, nil
}

func TestProcessTaskDurableClaimAllowsOnlyOneNonIdempotentRuntime(t *testing.T) {
	agent := &blockingTemplateFillCheckAgent{
		firstReady: make(chan struct{}),
		release:    make(chan struct{}),
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
