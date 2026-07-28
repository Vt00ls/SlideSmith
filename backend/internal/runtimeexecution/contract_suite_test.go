package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

type RuntimeExecutionContractFixture struct {
	Now       time.Time
	Authority RuntimeAuthority
	Start     StartRuntimeRun
}

type RuntimeExecutionContractFactory func(*testing.T, RuntimeExecutionContractFixture) RuntimeExecution

// RunRuntimeExecutionContract is shared by every adapter that implements the
// public Runtime Execution seam.
func RunRuntimeExecutionContract(t *testing.T, factory RuntimeExecutionContractFactory) {
	t.Helper()
	t.Run("accepted start is inspectable and exactly replayable", func(t *testing.T) {
		now := time.Date(2026, 7, 28, 6, 0, 0, 0, time.UTC)
		authority := mustTaskOrchestrationAuthority(t, "shared-contract-caller", 8)
		start := standardStart(t, now, authority, "shared-contract")
		runtime := factory(t, RuntimeExecutionContractFixture{Now: now, Authority: authority, Start: start})

		accepted, err := runtime.Execute(context.Background(), start)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		inspected, err := runtime.Inspect(context.Background(), RuntimeRunRef{
			SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
		})
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if inspected != accepted.Snapshot || inspected.State != RuntimeWaitingForLease {
			t.Fatalf("accepted snapshot = %#v, inspected = %#v", accepted.Snapshot, inspected)
		}
		replayed, err := runtime.Execute(context.Background(), start)
		if err != nil || replayed != accepted {
			t.Fatalf("exact replay = %#v, err=%v", replayed, err)
		}
	})
}

func TestDeterministicInMemoryRuntimeExecutionContract(t *testing.T) {
	t.Parallel()
	RunRuntimeExecutionContract(t, func(t *testing.T, fixture RuntimeExecutionContractFixture) RuntimeExecution {
		t.Helper()
		harness := harnessForStart(t, fixture.Now, fixture.Authority, fixture.Start)
		return harness.Runtime
	})
}
