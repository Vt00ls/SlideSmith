package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

// sharedFaultAdapter abstracts the deterministic in-memory harness and the
// local-development adapter behind the same fault controls so the shared fault
// matrix (Testing Decision 15) runs on both without duplication.
type sharedFaultAdapter struct {
	runtime      RuntimeExecution
	failBefore   func(*testing.T)
	crashAfter   func(*testing.T)
	loseResponse func(*testing.T)
	restart      func(*testing.T) RuntimeExecution
	prepareLease func(*testing.T, RuntimeExecution) RuntimeExecution
	runLease     func(*testing.T, RuntimeExecution) RuntimeDecision
}

func newInMemoryFaultAdapter(t *testing.T, now time.Time, authority RuntimeAuthority, start StartRuntimeRun) *sharedFaultAdapter {
	t.Helper()
	harness := harnessForStartWithDecisionID(t, now, authority, start, 500)
	return &sharedFaultAdapter{
		runtime: harness.Runtime,
		failBefore: func(t *testing.T) {
			t.Helper()
			if err := harness.FailNextAt(FaultBeforeCommit); err != nil {
				t.Fatal(err)
			}
		},
		crashAfter: func(t *testing.T) {
			t.Helper()
			if err := harness.CrashNextAt(CrashAfterCommit); err != nil {
				t.Fatal(err)
			}
		},
		loseResponse: func(*testing.T) { harness.LoseNextResponse() },
		restart: func(*testing.T) RuntimeExecution {
			return harness.Restart().Runtime
		},
	}
}

func newLocalDevelopmentFaultAdapter(t *testing.T, now time.Time, authority RuntimeAuthority, start StartRuntimeRun) *sharedFaultAdapter {
	t.Helper()
	journal, err := NewLocalDevelopmentJournal("")
	if err != nil {
		t.Fatal(err)
	}
	authorityHandle, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
		Now:      func() time.Time { return now },
		Policy:   LocalDevelopmentPolicy{LeaseDuration: time.Minute, WorkerClass: WorkerTool, NodeReady: true},
		Journal:  journal,
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{
			grantFixtureForStart(start, now.Add(15*time.Minute), true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sharedFaultAdapter{
		runtime: authorityHandle,
		failBefore: func(t *testing.T) {
			t.Helper()
			if err := authorityHandle.FailNextAt(FaultBeforeCommit); err != nil {
				t.Fatal(err)
			}
		},
		crashAfter: func(t *testing.T) {
			t.Helper()
			if err := authorityHandle.CrashNextAt(CrashAfterCommit); err != nil {
				t.Fatal(err)
			}
		},
		loseResponse: func(*testing.T) { authorityHandle.LoseNextResponse() },
		restart: func(*testing.T) RuntimeExecution {
			restarted, err := authorityHandle.Restart()
			if err != nil {
				t.Fatal(err)
			}
			return restarted
		},
	}
}

// TestSharedRuntimeExecutionFaultMatrix covers Testing Decision 15 boundaries
// on both the deterministic in-memory implementation and the local-development
// owned adapter. PostgreSQL boundaries are covered by the real-PostgreSQL
// fault suite (postgres_integration_test.go / postgres_worker_protocol tests)
// which use the PersistenceFaultController at every persistence boundary.
func TestSharedRuntimeExecutionFaultMatrix(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "shared-fault-matrix-caller", 6)
	start := standardStart(t, now, authority, "shared-fault-matrix")
	factories := []struct {
		name string
		new  func(*testing.T, time.Time, RuntimeAuthority, StartRuntimeRun) *sharedFaultAdapter
	}{
		{name: "deterministic_in_memory", new: newInMemoryFaultAdapter},
		{name: "local_development", new: newLocalDevelopmentFaultAdapter},
	}
	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Run("fault_before_commit_leaves_no_business_effect", func(t *testing.T) {
				adapter := factory.new(t, now, authority, start)
				adapter.failBefore(t)
				_, err := adapter.runtime.Execute(context.Background(), start)
				if errorCode(err) != ErrorDependencyUnavailable {
					t.Fatalf("fault before commit = %v, want dependency unavailable", err)
				}
				// The fault is one-shot; a retry on the same authority is safe.
				replayed, err := adapter.runtime.Execute(context.Background(), start)
				if err != nil || replayed.Fact.Disposition != DecisionAccepted {
					t.Fatalf("retry after fault = %#v err=%v", replayed, err)
				}
			})

			t.Run("crash_after_commit_replays_original_decision", func(t *testing.T) {
				adapter := factory.new(t, now, authority, start)
				adapter.crashAfter(t)
				_, err := adapter.runtime.Execute(context.Background(), start)
				if errorCode(err) != ErrorDependencyUnavailable {
					t.Fatalf("crash after commit = %v, want dependency unavailable", err)
				}
				restarted := adapter.restart(t)
				replayed, err := restarted.Execute(context.Background(), start)
				if err != nil || replayed.Fact.Disposition != DecisionAccepted ||
					replayed.Snapshot.State != RuntimeWaitingForLease {
					t.Fatalf("restart replay = %#v err=%v", replayed, err)
				}
			})

			t.Run("response_loss_after_commit_replays_original_decision", func(t *testing.T) {
				adapter := factory.new(t, now, authority, start)
				adapter.loseResponse(t)
				_, err := adapter.runtime.Execute(context.Background(), start)
				if errorCode(err) != ErrorReconciliationRequired {
					t.Fatalf("response loss = %v, want reconciliation required", err)
				}
				restarted := adapter.restart(t)
				replayed, err := restarted.Execute(context.Background(), start)
				if err != nil || replayed.Fact.Disposition != DecisionAccepted ||
					replayed.Snapshot.State != RuntimeWaitingForLease {
					t.Fatalf("response-loss replay = %#v err=%v", replayed, err)
				}
			})
		})
	}
}
