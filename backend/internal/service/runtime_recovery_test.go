package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type failedLegacyRecoveryAgent struct {
	arm func()
}

func (*failedLegacyRecoveryAgent) Up(context.Context, AgentRunRequest) error {
	return nil
}

func (a *failedLegacyRecoveryAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	exitCode := 1
	result := &AgentRunResult{
		RunID:         "failed-legacy-generate",
		SessionID:     "failed-legacy-session",
		Status:        "failed",
		ExitCode:      &exitCode,
		WorkspacePath: req.WorkDir,
		StderrTail:    "injected legacy generation failure",
	}
	if a.arm != nil {
		a.arm()
	}
	return result, errors.New("injected legacy generation failure")
}

func TestFindGeneratedRuntimeWorkspaceCandidates(t *testing.T) {
	root := t.TempDir()
	task := &model.Task{
		ID:             "abc",
		RuntimeProject: "task_abc",
	}
	service := &TaskService{
		agentCfg: config.AgentComposeConfig{SessionDataRoot: root},
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	createRuntimeCandidate(t, root, "old-session", "task_abc_ppt169_20260708", oldTime, true)
	createRuntimeCandidate(t, root, "new-session", "task_abc_ppt169_20260709", newTime, true)
	createRuntimeCandidate(t, root, "other-session", "task_other_ppt169_20260709", time.Now(), true)
	createRuntimeCandidate(t, root, "partial-session", "task_abc_ppt169_20260710", time.Now(), false)

	candidates, err := service.findGeneratedRuntimeWorkspaceCandidates(context.Background(), task)
	if err != nil {
		t.Fatalf("find candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2: %+v", len(candidates), candidates)
	}
	if candidates[0].SessionID != "new-session" {
		t.Fatalf("first session = %q, want new-session", candidates[0].SessionID)
	}
	if candidates[1].SessionID != "old-session" {
		t.Fatalf("second session = %q, want old-session", candidates[1].SessionID)
	}
}

func TestProcessLegacyGenerateRecoveryFinishesSpecPhaseBeforeAdvancing(t *testing.T) {
	service, repo, task, _, _ := newTemplateFillWorkflowService(t, model.TaskStatusSpecGenerating, &failedLegacyRecoveryAgent{})
	recoveryRoot := t.TempDir()
	service.agentCfg.SessionDataRoot = recoveryRoot
	createPublishableRuntimeCandidate(t, recoveryRoot, "recovered-session", task.RuntimeProject+"_ppt169_20260713")

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() recovery error = %v", err)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusCompleted {
		t.Fatalf("recovered task status = %q, want %q", persisted.Status, model.TaskStatusCompleted)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	specRuns := 0
	for _, phaseRun := range phaseRuns {
		if phaseRun.Status == PhaseRunStatusRunning {
			t.Fatalf("recovery left running phase: %#v", phaseRun)
		}
		if phaseRun.Phase != string(PhaseSpecGenerate) {
			continue
		}
		specRuns++
		if phaseRun.Status != PhaseRunStatusSucceeded || phaseRun.FinishedAt == nil || !strings.Contains(phaseRun.OutputJSON, `"recovered":true`) {
			t.Fatalf("recovered spec phase = %#v, want succeeded recovered output", phaseRun)
		}
	}
	if specRuns != 1 {
		t.Fatalf("spec phase runs = %d, want 1; all=%#v", specRuns, phaseRuns)
	}
}

func TestLegacyGenerateRecoveryStopsWhenSpecPhaseCannotBeOwned(t *testing.T) {
	agent := &failedLegacyRecoveryAgent{}
	service, repo, task, _, _ := newTemplateFillWorkflowService(t, model.TaskStatusSpecGenerating, agent)
	recoveryRoot := t.TempDir()
	service.agentCfg.SessionDataRoot = recoveryRoot
	createPublishableRuntimeCandidate(t, recoveryRoot, "recovered-session", task.RuntimeProject+"_ppt169_20260713")

	hook := installCancelBeforeTaskUpdate(t, repo.DB(), task.ID)
	agent.arm = func() { hook.Arm(3) }

	processErr := service.ProcessTask(context.Background(), task.ID)
	hook.Wait(t)
	phaseRuns, phaseRunsErr := repo.ListPhaseRuns(context.Background(), task.ID)
	if phaseRunsErr != nil {
		t.Fatal(phaseRunsErr)
	}
	if !errors.Is(processErr, errTaskStateChanged) {
		t.Fatalf("ProcessTask() error = %v, want recovered spec ownership loss; phases=%#v", processErr, phaseRuns)
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusCancelled || persisted.CancelledAt == nil {
		t.Fatalf("recovered spec ownership loss did not preserve cancellation: %#v", persisted)
	}
	specRuns := 0
	for _, phaseRun := range phaseRuns {
		if phaseRun.Phase != string(PhaseSpecGenerate) {
			t.Fatalf("downstream phase created after recovered spec ownership loss: %#v", phaseRun)
		}
		specRuns++
		if phaseRun.Status == PhaseRunStatusRunning || phaseRun.Status == PhaseRunStatusSucceeded {
			t.Fatalf("unowned recovered spec phase remained active or succeeded: %#v", phaseRun)
		}
	}
	if specRuns != 1 {
		t.Fatalf("spec phase runs = %d, want 1; all=%#v", specRuns, phaseRuns)
	}
}

func createRuntimeCandidate(t *testing.T, root, sessionID, projectName string, artifactTime time.Time, withContract bool) {
	t.Helper()
	projectPath := filepath.Join(root, "sessions", sessionID, "workspace", "projects", projectName)
	exportsDir := filepath.Join(projectPath, "exports")
	if err := os.MkdirAll(exportsDir, 0o755); err != nil {
		t.Fatalf("mkdir exports: %v", err)
	}
	pptxPath := filepath.Join(exportsDir, "result.pptx")
	if err := os.WriteFile(pptxPath, []byte("pptx"), 0o644); err != nil {
		t.Fatalf("write pptx: %v", err)
	}
	if err := os.Chtimes(pptxPath, artifactTime, artifactTime); err != nil {
		t.Fatalf("chtime pptx: %v", err)
	}
	if !withContract {
		return
	}
	if err := os.WriteFile(filepath.Join(projectPath, "design_spec.md"), []byte("# spec\n"), 0o644); err != nil {
		t.Fatalf("write design spec: %v", err)
	}
}

func createPublishableRuntimeCandidate(t *testing.T, root, sessionID, projectName string) {
	t.Helper()
	projectPath := filepath.Join(root, "sessions", sessionID, "workspace", "projects", projectName)
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 3)
	mustWriteFile(t, filepath.Join(projectPath, "design_spec.md"), "# recovered spec\n")
	mustWriteFile(t, filepath.Join(projectPath, "confirm_ui", "result.json"), `{"page_count":"3"}`+"\n")
	if err := os.Chtimes(
		filepath.Join(projectPath, "exports", "result.pptx"),
		time.Now().Add(-time.Minute),
		time.Now().Add(-time.Minute),
	); err != nil {
		t.Fatal(fmt.Errorf("set recovered export time: %w", err))
	}
}
