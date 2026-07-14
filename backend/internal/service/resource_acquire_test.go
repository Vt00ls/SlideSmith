package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type resourceAcquireAgent struct {
	projectPath  string
	taskID       string
	calls        int
	fail         error
	stdout       string
	stderr       string
	mutatePolicy bool
}

func (*resourceAcquireAgent) Up(context.Context, AgentRunRequest) error { return nil }

func (agent *resourceAcquireAgent) Run(_ context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	agent.calls++
	if agent.fail != nil {
		exit := 1
		return &AgentRunResult{RunID: "resource-failed", Status: "failed", ExitCode: &exit, RawJSON: agent.stdout, StderrTail: agent.stderr}, agent.fail
	}
	if req.Phase != string(PhaseImageAcquire) || !strings.Contains(req.Command, "resource_runner.py") || strings.Contains(req.Command, "image_gen.py") {
		return nil, fmt.Errorf("unexpected resource request: %#v", req)
	}
	policy, err := loadResourcePolicy(agent.projectPath)
	if err != nil {
		return nil, err
	}
	mustWriteEmptyResourceManifestNoTest(agent.projectPath, agent.taskID, policy.PhaseRunID)
	if agent.mutatePolicy {
		policy.NetworkEnabled = !policy.NetworkEnabled
		policy.PolicySHA256 = ""
		policy.PolicySHA256, err = resourcePolicyDigest(policy)
		if err != nil {
			return nil, err
		}
		if err := writeJSONPretty(filepath.Join(agent.projectPath, ".slidesmith", "resource_policy.json"), policy); err != nil {
			return nil, err
		}
		manifestPath := filepath.Join(agent.projectPath, ".slidesmith", "resources_manifest.json")
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, err
		}
		var manifest resourcesManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, err
		}
		manifest.PolicySHA256 = policy.PolicySHA256
		if err := writeJSONPretty(manifestPath, manifest); err != nil {
			return nil, err
		}
	}
	exit := 0
	return &AgentRunResult{RunID: "resource-succeeded", Status: "succeeded", ExitCode: &exit}, nil
}

type resourceAcquireFixture struct {
	service     *TaskService
	repo        *repository.Repository
	task        *model.Task
	projectPath string
	agent       *resourceAcquireAgent
}

func newResourceAcquireFixture(t *testing.T, phaseEnabled bool) *resourceAcquireFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskEvent{}, &model.Artifact{}, &model.TaskRuntimeRun{}, &model.TaskPhaseRun{}, &model.TaskConfirmation{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspaces")
	runtimeProject := "resource_acquire_task"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260714")
	task := &model.Task{ID: "resource-acquire-task", Route: model.TaskRouteMain, Status: model.TaskStatusImageAcquiring, RuntimeProject: runtimeProject}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(projectPath, "design_spec.md"), "# Design Spec\n")
	mustWriteFile(t, filepath.Join(projectPath, "spec_lock.md"), "# Spec Lock\n\npage_count: 3\n")
	if err := writeJSONPretty(filepath.Join(projectPath, "confirm_ui", "result.json"), map[string]any{"page_count": 3, "image_usage": []any{"provided"}}); err != nil {
		t.Fatal(err)
	}
	mustWriteEmptyResourcePlanNoTest(projectPath, task.ID, 3)
	agent := &resourceAcquireAgent{projectPath: projectPath, taskID: task.ID}
	storage := NewLocalStorage(filepath.Join(root, "storage"))
	service := NewTaskService(repo, storage, agent, NewRuntimeWorkspacePublisher(storage), config.AgentComposeConfig{
		Enabled: true, WorkspaceRoot: workspaceRoot, RunnerProfile: model.RunnerProfileFullPPTMaster,
		FullPPTDefaultEnabled: true, ResourcePhaseEnabled: phaseEnabled,
	})
	writeRuntimeProfileManifestForTest(t, workspaceRoot, task)
	contract, err := validateSpecGenerateContract(projectPath, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindFullPhaseContract(projectPath, PhaseSpecGenerate, contract, task, service.resolveTaskWorkspace(task), "spec-runtime"); err != nil {
		t.Fatal(err)
	}
	return &resourceAcquireFixture{service: service, repo: repo, task: task, projectPath: projectPath, agent: agent}
}

func TestProcessResourceAcquireSucceedsAndQueuesSVGWithoutSkip(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err != nil {
		t.Fatalf("processResourceAcquire() error = %v", err)
	}
	persisted, err := fixture.repo.GetTask(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusSVGGenerating || fixture.agent.calls != 1 {
		t.Fatalf("resource acquire result task=%#v calls=%d", persisted, fixture.agent.calls)
	}
	runs, err := fixture.repo.ListPhaseRuns(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Phase != string(PhaseImageAcquire) || runs[0].Status != PhaseRunStatusSucceeded {
		t.Fatalf("resource phase runs = %#v", runs)
	}
	if strings.Contains(runs[0].OutputJSON, "skipped") {
		t.Fatalf("resource phase retained compatibility skip: %s", runs[0].OutputJSON)
	}
	for _, path := range []string{
		filepath.Join(fixture.projectPath, ".slidesmith", "resource_policy.json"),
		filepath.Join(fixture.projectPath, "analysis", "resource_requirements.json"),
		filepath.Join(fixture.projectPath, ".slidesmith", "resources_manifest.json"),
		filepath.Join(fixture.projectPath, ".slidesmith", "contracts", "image_acquire.json"),
	} {
		assertPathExists(t, path)
	}
	artifacts, err := fixture.repo.ListArtifacts(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{
		model.ArtifactKindResourcePlan: false, model.ArtifactKindResourcePolicy: false,
		model.ArtifactKindResourceRequirements: false, model.ArtifactKindResourceManifest: false,
		model.ArtifactKindImageAnalysis: false,
	}
	for _, artifact := range artifacts {
		if _, ok := wantKinds[artifact.Kind]; ok {
			wantKinds[artifact.Kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("resource artifact %q missing: %#v", kind, artifacts)
		}
	}
}

func TestProcessResourceAcquireFailsClosedWhenPhaseDisabled(t *testing.T) {
	fixture := newResourceAcquireFixture(t, false)
	err := fixture.service.processResourceAcquire(context.Background(), fixture.task)
	if err == nil || !strings.Contains(err.Error(), "resource_phase_disabled") {
		t.Fatalf("disabled phase error = %v", err)
	}
	if fixture.agent.calls != 0 {
		t.Fatalf("disabled resource phase invoked runtime %d time(s)", fixture.agent.calls)
	}
	persisted, getErr := fixture.repo.GetTask(context.Background(), fixture.task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "image_acquire.policy" {
		t.Fatalf("disabled phase task = %#v", persisted)
	}
	runs, listErr := fixture.repo.ListPhaseRuns(context.Background(), fixture.task.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 1 || runs[0].Status != PhaseRunStatusFailed {
		t.Fatalf("disabled phase runs = %#v", runs)
	}
}

func TestProcessResourceAcquireRedactsProviderTailsFromFailureMetadata(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	fixture.agent.fail = fmt.Errorf("provider_failed")
	fixture.agent.stdout = "prompt=confidential product launch"
	fixture.agent.stderr = "Authorization: Bearer secret-token"
	if err := fixture.service.processResourceAcquire(context.Background(), fixture.task); err == nil {
		t.Fatal("provider failure was not returned")
	}
	persisted, err := fixture.repo.GetTask(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted.FailureMetadata, "confidential product launch") || strings.Contains(persisted.FailureMetadata, "secret-token") {
		t.Fatalf("resource failure metadata leaked provider output: %s", persisted.FailureMetadata)
	}
}

func TestProcessResourceAcquireRejectsRuntimePolicyMutation(t *testing.T) {
	fixture := newResourceAcquireFixture(t, true)
	fixture.agent.mutatePolicy = true
	err := fixture.service.processResourceAcquire(context.Background(), fixture.task)
	if err == nil || !strings.Contains(err.Error(), "resource policy changed during resource phase") {
		t.Fatalf("policy mutation error = %v", err)
	}
	persisted, getErr := fixture.repo.GetTask(context.Background(), fixture.task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "image_acquire.contract" {
		t.Fatalf("policy mutation task = %#v", persisted)
	}
}
