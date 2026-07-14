package service

import (
	"context"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestQualityAPIReturnsTaskScopedSafeSummary(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	writePassingQualityReportsNoTest(projectPath, task.ID, "quality-api")
	task.Status = model.TaskStatusCompleted
	task.FailurePhase = ""
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	quality, err := service.GetQuality(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetQuality() error = %v", err)
	}
	if quality.TaskID != task.ID || quality.Decision != "pass" || quality.CurrentGate != string(PhasePublish) {
		t.Fatalf("quality API = %#v", quality)
	}
	if len(quality.Findings) != 0 || quality.WarningBadge != 0 {
		t.Fatalf("quality API leaked unexpected findings: %#v", quality)
	}
}
