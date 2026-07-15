package service

import (
	"context"
	"encoding/json"
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

func TestQualityAPIHistoricalCompletedTaskSerializesEmptyCollections(t *testing.T) {
	service, repo, task, _ := retryTestService(t)
	task.Status = model.TaskStatusCompleted
	task.FailurePhase = ""
	task.ErrorMessage = ""
	task.FailureMetadata = "{}"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	quality, err := service.GetQuality(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetQuality() error = %v", err)
	}
	if quality.Findings == nil || quality.ChartReceipts == nil || quality.RenderArtifactIDs == nil || quality.AllowedRetryPhases == nil {
		t.Fatalf("historical quality collections must be non-nil: %#v", quality)
	}

	raw, err := json.Marshal(quality)
	if err != nil {
		t.Fatalf("marshal quality: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode quality JSON: %v", err)
	}
	for _, field := range []string{"findings", "chart_receipts", "render_artifact_ids", "allowed_retry_phases"} {
		if got := string(payload[field]); got != "[]" {
			t.Fatalf("%s = %s, want []", field, got)
		}
	}
}
