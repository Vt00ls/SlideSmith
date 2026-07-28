package runtimeexecution

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestConcurrentExactStartAndCancelReplayOneDecision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 6, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "concurrent-caller", 2)
	start := standardStart(t, now, authority, "concurrent")
	harness := harnessForStartWithDecisionID(t, now, authority, start, 900)

	starts := executeConcurrently(t, harness.Runtime, start, 16)
	for index, decision := range starts {
		if decision.Fact != starts[0].Fact || decision.Snapshot != starts[0].Snapshot {
			t.Fatalf("start %d diverged: %#v", index, decision)
		}
	}
	if starts[0].Fact.DecisionID.String() != "runtime-decision-000900" || starts[0].Snapshot.RuntimeRevision != start.ExpectedRuntimeRevision+1 {
		t.Fatalf("concurrent start allocated multiple decisions: %#v", starts[0])
	}

	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "cancel-concurrent"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: starts[0].Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: starts[0].Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        starts[0].Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancels := executeConcurrently(t, harness.Runtime, cancel, 16)
	for index, decision := range cancels {
		if decision.Fact != cancels[0].Fact || decision.Snapshot != cancels[0].Snapshot {
			t.Fatalf("cancel %d diverged: %#v", index, decision)
		}
	}
	if cancels[0].Fact.DecisionID.String() != "runtime-decision-000901" || cancels[0].Snapshot.RuntimeRevision != start.ExpectedRuntimeRevision+2 {
		t.Fatalf("concurrent cancel allocated multiple decisions: %#v", cancels[0])
	}

	var wait sync.WaitGroup
	startInspect := make(chan struct{})
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-startInspect
			for iteration := 0; iteration < 32; iteration++ {
				_, inspectErr := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
					SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
					PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
				})
				if inspectErr != nil {
					t.Errorf("inspect: %v", inspectErr)
					return
				}
			}
		}()
	}
	close(startInspect)
	wait.Wait()
}

func executeConcurrently(t *testing.T, runtime RuntimeExecution, command RuntimeCommand, count int) []RuntimeDecision {
	t.Helper()
	decisions := make([]RuntimeDecision, count)
	errorsByIndex := make([]error, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range decisions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			decisions[index], errorsByIndex[index] = runtime.Execute(context.Background(), command)
		}()
	}
	close(start)
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("execute %d: %v", index, err)
		}
	}
	return decisions
}
