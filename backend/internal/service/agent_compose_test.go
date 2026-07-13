package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
)

func TestAgentComposeCLIClientRunDetachedPollsInspect(t *testing.T) {
	tmp := t.TempDir()
	counterPath := filepath.Join(tmp, "inspect-count")
	cliPath := filepath.Join(tmp, "agent-compose-stub.sh")
	script := fmt.Sprintf(`#!/bin/sh
counter=%q
case " $* " in
  *" run ppt_master "*)
    printf '%%s\n' '{"run_id":"run-1","status":"running","session_id":"session-1"}'
    ;;
  *" inspect run run-1 "*)
    count=0
    if [ -f "$counter" ]; then
      count=$(cat "$counter")
    fi
    count=$((count + 1))
    printf '%%s' "$count" > "$counter"
    if [ "$count" -lt 2 ]; then
      printf '%%s\n' '{"run_id":"run-1","status":"running","session_id":"session-1"}'
    else
      printf '%%s\n' '{"run_id":"run-1","status":"succeeded","session_id":"session-1","exit_code":0}'
    fi
    ;;
  *)
    printf 'unexpected args: %%s\n' "$*" >&2
    exit 9
    ;;
esac
`, counterPath)
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub CLI: %v", err)
	}

	client := NewAgentComposeCLIClient(config.AgentComposeConfig{
		Enabled:         true,
		CLI:             cliPath,
		Agent:           "ppt_master",
		SessionDataRoot: "/data",
		Timeout:         5 * time.Second,
	})
	result, err := client.Run(context.Background(), AgentRunRequest{
		Phase:        "generate",
		Prompt:       "make slides",
		Detached:     true,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("run detached: %v", err)
	}
	if result.RunID != "run-1" {
		t.Fatalf("run id = %q, want run-1", result.RunID)
	}
	if result.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if result.WorkspacePath != "/data/sessions/session-1/workspace" {
		t.Fatalf("workspace path = %q", result.WorkspacePath)
	}
	rawCount, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if string(rawCount) != "2" {
		t.Fatalf("inspect count = %q, want 2", string(rawCount))
	}
}

func TestParseAgentRunResultReadsNestedFields(t *testing.T) {
	result := parseAgentRunResult([]byte(`{
		"run": {
			"run_id": "run-2",
			"session_id": "session-2",
			"status": "failed",
			"exit_code": 7,
			"error": "boom"
		}
	}`))
	if result.RunID != "run-2" || result.SessionID != "session-2" || result.Status != "failed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatalf("exit code = %v, want 7", result.ExitCode)
	}
	if result.ErrorMessage != "boom" {
		t.Fatalf("error message = %q, want boom", result.ErrorMessage)
	}
}
