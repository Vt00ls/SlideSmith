package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type svgRecoveryAgent struct {
	projectPath string
	taskID      string
	prompts     int
	commands    int
}

type svgUpstreamMutationAgent struct {
	projectPath string
}

func (*svgUpstreamMutationAgent) Up(context.Context, AgentRunRequest) error { return nil }

func (agent *svgUpstreamMutationAgent) Run(_ context.Context, _ AgentRunRequest) (*AgentRunResult, error) {
	mustWriteFileNoTest(agent.projectPath, filepath.Join("analysis", "chart_usage.json"), `{"schema":"slidesmith.chart_usage.v1","tampered":true}`+"\n")
	exitCode := 0
	return &AgentRunResult{RunID: "quality-mutated", Status: "succeeded", ExitCode: &exitCode}, nil
}

func (*svgRecoveryAgent) Up(context.Context, AgentRunRequest) error { return nil }

func (agent *svgRecoveryAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	if req.Prompt != "" {
		agent.prompts++
		return nil, fmt.Errorf("authoring agent must not run during SVG recovery")
	}
	if req.Command == "" {
		return nil, fmt.Errorf("recovery command is empty")
	}
	agent.commands++
	writeValidSVGBundleNoTest(agent.projectPath, agent.taskID, 3)
	exitCode := 0
	return &AgentRunResult{RunID: "svg-recovery-inspector", Status: "succeeded", ExitCode: &exitCode}, nil
}

func prepareSVGRecoveryFixture(t *testing.T) (*TaskService, *model.Task, string, *svgRecoveryAgent) {
	t.Helper()
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	if err := os.RemoveAll(filepath.Join(projectPath, "exports")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectPath, "svg_final")); err != nil {
		t.Fatal(err)
	}
	task.Status = model.TaskStatusSVGGenerating
	task.FailurePhase = ""
	task.ErrorMessage = ""
	task.FailureMetadata = "{}"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	agent := &svgRecoveryAgent{projectPath: projectPath, taskID: task.ID}
	service.agent = agent
	return service, task, projectPath, agent
}

func TestSVGRecoveryReusesValidatedInventoryWithoutAgentOrInspector(t *testing.T) {
	service, task, projectPath, agent := prepareSVGRecoveryFixture(t)
	workspace := service.resolveTaskWorkspace(task)
	run, nextProjectPath, err := service.runFullPPTMasterSVGPhase(context.Background(), task, workspace, projectPath)
	if err != nil {
		t.Fatalf("runFullPPTMasterSVGPhase() error = %v", err)
	}
	if run != nil || nextProjectPath != projectPath || agent.prompts != 0 || agent.commands != 0 {
		t.Fatalf("validated recovery reran runtime: run=%#v project=%s prompts=%d commands=%d", run, nextProjectPath, agent.prompts, agent.commands)
	}
	phaseRuns, err := service.repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil || len(phaseRuns) != 1 || phaseRuns[0].Runner != PhaseRunnerWorker || phaseRuns[0].Status != PhaseRunStatusSucceeded {
		t.Fatalf("validated recovery phase runs = %#v, %v", phaseRuns, err)
	}
}

func TestSVGRecoveryRunsInspectorOnlyWhenInventoriesAreMissing(t *testing.T) {
	service, task, projectPath, agent := prepareSVGRecoveryFixture(t)
	for _, path := range []string{
		filepath.Join(projectPath, "analysis", "svg_inventory.json"),
		filepath.Join(projectPath, "analysis", "notes_inventory.json"),
	} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	workspace := service.resolveTaskWorkspace(task)
	run, _, err := service.runFullPPTMasterSVGPhase(context.Background(), task, workspace, projectPath)
	if err != nil {
		t.Fatalf("runFullPPTMasterSVGPhase() error = %v", err)
	}
	if run == nil || agent.prompts != 0 || agent.commands != 1 {
		t.Fatalf("inspector-only recovery = run %#v prompts=%d commands=%d", run, agent.prompts, agent.commands)
	}
	for _, path := range []string{
		filepath.Join(projectPath, "analysis", "svg_inventory.json"),
		filepath.Join(projectPath, "analysis", "notes_inventory.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("recovery inventory missing: %s (%v)", path, err)
		}
	}
}

func TestRetrySVGPreservesRecoverableAuthoredBundleForInspector(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	for _, path := range []string{
		filepath.Join(projectPath, "analysis", "svg_inventory.json"),
		filepath.Join(projectPath, "analysis", "notes_inventory.json"),
	} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	task.FailurePhase = "svg_execute.bundle"
	task.ErrorMessage = "inspector session failed"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	beforeSVG, err := sha256File(filepath.Join(projectPath, "svg_output", "01_page_01.svg"))
	if err != nil {
		t.Fatal(err)
	}
	beforeNotes, err := sha256File(filepath.Join(projectPath, "notes", "total.md"))
	if err != nil {
		t.Fatal(err)
	}

	retried, err := service.RetryTask(context.Background(), task.ID, retryPhaseSVGExecute)
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if retried.Status != model.TaskStatusSVGGenerating {
		t.Fatalf("retry status = %q", retried.Status)
	}
	for path, want := range map[string]string{
		filepath.Join(projectPath, "svg_output", "01_page_01.svg"): beforeSVG,
		filepath.Join(projectPath, "notes", "total.md"):            beforeNotes,
	} {
		got, err := sha256File(path)
		if err != nil || got != want {
			t.Fatalf("preserved authored input %s hash = %q, %v; want %q", path, got, err, want)
		}
	}
	for _, path := range []string{
		filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseSVGExecute)+".json"),
		filepath.Join(projectPath, ".slidesmith", "quality_report.json"),
		filepath.Join(projectPath, "exports"),
		filepath.Join(projectPath, "validation"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("downstream retry output still exists: %s (%v)", path, err)
		}
	}
	events, err := repo.ListEvents(context.Background(), task.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundRecovery := false
	for _, event := range events {
		if event.Type == model.EventTypeRuntime && event.Status == "queued" && strings.Contains(event.Payload, `"inspector_only_recovery":true`) {
			foundRecovery = true
		}
	}
	if !foundRecovery {
		t.Fatalf("retry recovery event missing: %#v", events)
	}
}

func TestSVGRecoveryRejectsStaleInventoryWithoutRerunningAgent(t *testing.T) {
	service, task, projectPath, agent := prepareSVGRecoveryFixture(t)
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "01_page_01.svg"), `<svg xmlns="http://www.w3.org/2000/svg"></svg>`+"\n")
	workspace := service.resolveTaskWorkspace(task)
	if _, _, err := service.runFullPPTMasterSVGPhase(context.Background(), task, workspace, projectPath); err == nil {
		t.Fatal("stale SVG recovery error = nil")
	}
	if agent.prompts != 0 || agent.commands != 0 {
		t.Fatalf("stale recovery reran runtime: prompts=%d commands=%d", agent.prompts, agent.commands)
	}
	persisted, err := service.repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "svg_execute.contract_stale" {
		t.Fatalf("stale recovery task = %#v", persisted)
	}
}

func TestQualityPhaseRejectsBundleMutationAfterCommandSync(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.Status = model.TaskStatusQualityChecking
	task.FailurePhase = ""
	task.ErrorMessage = ""
	task.FailureMetadata = "{}"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	service.agent = &svgUpstreamMutationAgent{projectPath: projectPath}
	workspace := service.resolveTaskWorkspace(task)
	if _, _, err := service.runFullPPTMasterQualityPhase(context.Background(), task, workspace, projectPath); err == nil {
		t.Fatal("quality phase accepted an upstream sidecar mutation")
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "quality_check.upstream_mutation" {
		t.Fatalf("mutating quality task = %#v", persisted)
	}
}
