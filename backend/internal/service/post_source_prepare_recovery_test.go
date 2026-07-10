package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type postSourcePrepareSaveFault struct {
	name   string
	cancel bool
}

type templateResolveFailureAfterPrepareAgent struct{}

func (templateResolveFailureAfterPrepareAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (templateResolveFailureAfterPrepareAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	result, err := (templateResolvePrepareAgent{}).Run(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(filepath.Join(req.WorkDir, ".slidesmith", "template_lock.json")); err != nil {
		return nil, err
	}
	return result, nil
}

func TestProcessPrepareRecoversUnsupportedWorkflowTaskWriteAfterSourcePrepare(t *testing.T) {
	tests := []postSourcePrepareSaveFault{
		{name: "context_cancellation", cancel: true},
		{name: "one_shot_save_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, _ := routeDispatchPrepareService(t, "请美化 PPTX，保留页数和文字", []model.Artifact{
				{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-route/source/original.pptx"},
			})
			var ctx context.Context = context.Background()
			cancel := func() {}
			if test.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			t.Cleanup(cancel)
			injected := installPostSourcePrepareTaskSaveFault(
				t,
				repo,
				"unsupported_"+test.name,
				func(candidate *model.Task) bool {
					return candidate.Status == model.TaskStatusFailed && candidate.FailurePhase == "source_prepare.workflow_not_enabled"
				},
				cancel,
				test.cancel,
			)

			err := service.processPrepare(ctx, task)
			if err == nil {
				t.Fatal("processPrepare() error = nil, want unsupported workflow failure")
			}
			if !injected() {
				t.Fatal("post-source-prepare task save fault was not injected")
			}
			if test.cancel {
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("context error = %v, want canceled", ctx.Err())
				}
			} else {
				for _, want := range []string{
					"route beautify source intake is complete",
					"forced post-source-prepare task save failure",
				} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("processPrepare() error missing %q: %v", want, err)
					}
				}
			}
			assertPostSourcePrepareTaskRecovered(t, service, repo, task.ID, "source_prepare.workflow_not_enabled")
		})
	}
}

func TestProcessPrepareRecoversAwaitingAnchorTransitionAfterSourcePrepare(t *testing.T) {
	tests := []postSourcePrepareSaveFault{
		{name: "context_cancellation", cancel: true},
		{name: "one_shot_save_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, _ := templateResolvePrepareService(t)
			var ctx context.Context = context.Background()
			cancel := func() {}
			if test.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			t.Cleanup(cancel)
			injected := installPostSourcePrepareTaskSaveFault(
				t,
				repo,
				"awaiting_anchor_"+test.name,
				func(candidate *model.Task) bool {
					return candidate.Status == model.TaskStatusAwaitingAnchorConfirm
				},
				cancel,
				test.cancel,
			)

			err := service.processPrepare(ctx, task)
			if err == nil {
				t.Fatal("processPrepare() error = nil, want awaiting-anchor transition failure")
			}
			if !injected() {
				t.Fatal("awaiting-anchor task save fault was not injected")
			}
			if test.cancel && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("context error = %v, want canceled", ctx.Err())
			}
			assertPostSourcePrepareTaskRecovered(t, service, repo, task.ID, "source_prepare.awaiting_anchor_confirm")
		})
	}
}

func TestProcessPrepareRecoversTemplateResolutionTaskWriteAfterSourcePrepare(t *testing.T) {
	service, repo, task, _ := templateResolvePrepareService(t)
	service.agent = templateResolveFailureAfterPrepareAgent{}
	injected := installPostSourcePrepareTaskSaveFault(
		t,
		repo,
		"template_resolution_one_shot_save_failure",
		func(candidate *model.Task) bool {
			return candidate.Status == model.TaskStatusFailed && candidate.FailurePhase == string(PhaseTemplateResolve)
		},
		func() {},
		false,
	)

	err := service.processPrepare(context.Background(), task)
	if err == nil {
		t.Fatal("processPrepare() error = nil, want template resolution failure")
	}
	if !injected() {
		t.Fatal("template-resolution task save fault was not injected")
	}
	for _, want := range []string{"template lock file missing", "forced post-source-prepare task save failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("processPrepare() error missing %q: %v", want, err)
		}
	}
	assertPostSourcePrepareTaskRecovered(t, service, repo, task.ID, string(PhaseTemplateResolve))
}

func installPostSourcePrepareTaskSaveFault(
	t *testing.T,
	repo *repository.Repository,
	name string,
	matches func(*model.Task) bool,
	cancel context.CancelFunc,
	cancelOnly bool,
) func() bool {
	t.Helper()
	injected := false
	if err := repo.DB().Callback().Update().Before("gorm:update").Register("test:"+name, func(db *gorm.DB) {
		candidate, ok := db.Statement.Dest.(*model.Task)
		if !ok || injected || !matches(candidate) {
			return
		}
		injected = true
		if cancelOnly {
			cancel()
			return
		}
		db.AddError(errors.New("forced post-source-prepare task save failure"))
	}); err != nil {
		t.Fatal(err)
	}
	return func() bool { return injected }
}

func assertPostSourcePrepareTaskRecovered(
	t *testing.T,
	service *TaskService,
	repo *repository.Repository,
	taskID string,
	wantFailurePhase string,
) {
	t.Helper()
	updated, err := repo.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusFailed || updated.FailurePhase != wantFailurePhase {
		t.Fatalf("task recovery = {status:%q phase:%q}, want failed/%s", updated.Status, updated.FailurePhase, wantFailurePhase)
	}
	assertSourcePreparePhaseStatus(t, repo, taskID, PhaseRunStatusSucceeded)
	prefix := filepath.ToSlash(filepath.Join("tasks", taskID, "source-intake")) + "/"
	if rows := loadPersistedSourceIntakeRows(t, repo, taskID, prefix); len(rows) == 0 {
		t.Fatal("source intake publication should remain committed after task-only recovery")
	}
	if _, err := service.RetryTask(context.Background(), taskID, retryPhasePrepare); err != nil {
		t.Fatalf("prepare retry after post-source-prepare recovery error = %v", err)
	}
}
