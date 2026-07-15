package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestPPTXValidateContractPassesAndRejectsPPTXMutation(t *testing.T) {
	_, _, _, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	if _, err := validatePPTXExportContract(projectPath); err != nil {
		t.Fatalf("validate export fixture: %v", err)
	}
	writePassingPPTXValidateReportsNoTest(projectPath, "task-retry", "validate-contract")
	if _, err := validatePPTXValidateContractForRun(projectPath, "validate-contract"); err != nil {
		t.Fatalf("validatePPTXValidateContractForRun() error = %v", err)
	}
	file, err := os.OpenFile(filepath.Join(projectPath, "exports", "stale.pptx"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("tampered")
	_ = file.Close()
	if _, err := validatePPTXValidateContractForRun(projectPath, "validate-contract"); err == nil {
		t.Fatal("mutated canonical PPTX was accepted")
	}
}

func TestRetryPPTXValidatePreservesExportAndCleansOnlyValidateOutputs(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	if _, err := validatePPTXExportContract(projectPath); err != nil {
		t.Fatal(err)
	}
	writePassingPPTXValidateReportsNoTest(projectPath, task.ID, "old-validate")
	task.FailurePhase = "pptx_validate.render"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	retried, err := service.RetryTask(context.Background(), task.ID, string(PhasePPTXValidate))
	if err != nil {
		t.Fatalf("RetryTask(pptx_validate) error = %v", err)
	}
	if retried.Status != model.TaskStatusPPTXValidating {
		t.Fatalf("retry status = %q", retried.Status)
	}
	assertPathExists(t, filepath.Join(projectPath, "exports", "stale.pptx"))
	assertPathExists(t, filepath.Join(projectPath, "validation", "quality_summary.json"))
	assertPathMissing(t, filepath.Join(projectPath, "validation", "pptx_validate_report.json"))
	assertPathMissing(t, filepath.Join(projectPath, ".slidesmith", "contracts", "pptx_validate.json"))
}

func TestPublishQualityGateFailsClosedWhenEnabled(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.Status = model.TaskStatusPublishing
	task.FailurePhase = ""
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	service.agentCfg.PPTXValidateEnabled = true
	if err := os.Remove(filepath.Join(projectPath, ".slidesmith", "contracts", "pptx_validate.json")); err != nil {
		t.Fatal(err)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err == nil {
		t.Fatal("publish without PPTX validate contract succeeded")
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "publish.quality_gate" {
		t.Fatalf("publish gate failure = %#v", persisted)
	}
}
