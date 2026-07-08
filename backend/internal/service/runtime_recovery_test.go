package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

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
