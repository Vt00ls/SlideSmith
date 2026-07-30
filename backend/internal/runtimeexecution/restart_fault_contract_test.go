package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

func TestDeterministicHarnessRestartFaultsAndClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "fault-caller", 2)
	start := standardStart(t, now, authority, "fault")
	harness := harnessForStartWithDecisionID(t, now, authority, start, 500)

	if err := harness.FailNextAt(FaultBeforeValidation); err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, start), ErrorDependencyUnavailable)
	assertNoBusinessEffect(t, harness, start, start.ExpectedRuntimeRevision)

	if err := harness.FailNextAt(FaultBeforeCommit); err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, start), ErrorDependencyUnavailable)
	assertNoBusinessEffect(t, harness, start, start.ExpectedRuntimeRevision)

	harness.LoseNextResponse()
	assertErrorCode(t, executeError(harness, start), ErrorReconciliationRequired)
	committed, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	if err != nil || committed.State != RuntimeWaitingForLease || committed.RuntimeRevision != start.ExpectedRuntimeRevision+1 {
		t.Fatalf("post-commit response loss did not retain acceptance: snapshot=%#v err=%v", committed, err)
	}

	restarted := harness.Restart()
	replayed := executeDecision(t, restarted, start)
	if replayed.Fact.DecisionID.String() != "runtime-decision-000500" || replayed.Snapshot != committed {
		t.Fatalf("restart did not replay durable acceptance: %#v", replayed)
	}

	clockStart := standardStart(t, now, authority, "clock")
	clockHarness := harnessForStartWithGrantExpiry(t, now, authority, clockStart, now.Add(5*time.Minute))
	if err := clockHarness.AdvanceClock(5*time.Minute + time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	expired := executeDecision(t, clockHarness, clockStart)
	if expired.Fact.Disposition != DecisionRejected || expired.Fact.Rejection != CommandRejectionPolicy {
		t.Fatalf("controlled clock did not expire grant: %#v", expired.Fact)
	}
}

func TestResponseLossAfterDurableRejectionReplaysOriginalDecision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "rejection-response-loss-caller", 2)
	start := standardStart(t, now, authority, "rejection-response-loss")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 600},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(-time.Nanosecond), true)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	harness.LoseNextResponse()
	assertErrorCode(t, executeError(harness, start), ErrorReconciliationRequired)
	assertNoBusinessEffect(t, harness, start, start.ExpectedRuntimeRevision)

	restarted := harness.Restart()
	replayed := executeDecision(t, restarted, start)
	if replayed.Fact.DecisionID.String() != "runtime-decision-000600" ||
		replayed.Fact.Disposition != DecisionRejected || replayed.Fact.Rejection != CommandRejectionPolicy {
		t.Fatalf("durable rejection replay = %#v", replayed.Fact)
	}
}

func TestCrashBeforeCommitRequiresRestartAndLeavesNoRequestBinding(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "crash-caller", 2)
	start := standardStart(t, now, authority, "crash")
	harness := harnessForStartWithDecisionID(t, now, authority, start, 700)
	if err := harness.CrashNextAt(CrashBeforeCommit); err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, start), ErrorDependencyUnavailable)
	assertErrorCode(t, executeError(harness, start), ErrorDependencyUnavailable)

	restarted := harness.Restart()
	accepted := executeDecision(t, restarted, start)
	if accepted.Fact.DecisionID.String() != "runtime-decision-000700" {
		t.Fatalf("pre-commit crash allocated durable identity: %#v", accepted.Fact)
	}
}

func harnessForStartWithDecisionID(
	t *testing.T,
	now time.Time,
	authority RuntimeAuthority,
	start StartRuntimeRun,
	decisionStart uint64,
) *DeterministicHarness {
	t.Helper()
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: decisionStart},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	return harness
}

func harnessForStartWithGrantExpiry(
	t *testing.T,
	now time.Time,
	authority RuntimeAuthority,
	start StartRuntimeRun,
	expiresAt time.Time,
) *DeterministicHarness {
	t.Helper()
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:         []AdmissionGrantFixture{grantFixtureForStart(start, expiresAt, true)},
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	return harness
}
