package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func lockRunnerProfileForTest(task *model.Task, profile string) {
	lockedAt := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	task.RunnerProfile = profile
	task.RunnerProfileSource = model.RunnerProfileSourceExplicitConfig
	task.RunnerProfileLockedAt = &lockedAt
	if task.Route == "" {
		task.Route = model.TaskRouteMain
	}
}

func writeRuntimeProfileManifestForTest(t *testing.T, workspaceRoot string, task *model.Task) {
	t.Helper()
	workspacePath := filepath.Join(workspaceRoot, task.RuntimeProject)
	manifest := runtimeManifest{
		Schema:        "slidesmith.runtime_manifest.v2",
		SchemaVersion: 2,
		TaskID:        task.ID,
		Route:         task.Route,
		Runner: runtimeManifestRunner{
			RequestedProfile: task.RunnerProfile,
			EffectiveProfile: task.RunnerProfile,
			Source:           task.RunnerProfileSource,
			LockedAt:         task.RunnerProfileLockedAt.UTC().Format(time.RFC3339Nano),
		},
	}
	if err := writeJSONPretty(filepath.Join(workspacePath, ".slidesmith", "runtime_manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
}
