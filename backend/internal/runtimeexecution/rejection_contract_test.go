package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

func TestExpiredRuntimeDeadlineIsDurablyRejected(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "expired-deadline-caller", 6)
	input := standardStart(t, now, authority, "expired-deadline").StartRuntimeRunInput
	input.Deadline = now.Add(-time.Nanosecond)
	start := mustStart(t, input)
	harness := harnessForStart(t, now, authority, start)

	rejected := executeDecision(t, harness, start)
	if rejected.Fact.Disposition != DecisionRejected || rejected.Fact.Rejection != CommandRejectionPolicy {
		t.Fatalf("expired Runtime deadline decision = %#v", rejected.Fact)
	}
	assertNoBusinessEffect(t, harness, start, start.ExpectedRuntimeRevision)

	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed.Fact != rejected.Fact || replayed.Snapshot != rejected.Snapshot {
		t.Fatalf("expired Runtime deadline replay = %#v, err=%v", replayed, err)
	}
}

func TestStartRequiresTaskOrchestrationAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 47, 0, 0, time.UTC)
	id, err := NewAuthorityID("administrator-start-caller")
	if err != nil {
		t.Fatal(err)
	}
	administrator := NewAdministratorAuthority(id, 6)
	start := standardStart(t, now, administrator, "administrator-authority")
	harness := harnessForStart(t, now, administrator, start)

	assertErrorCode(t, executeError(harness, start), ErrorAuthorizationDenied)
}

func TestTerminalRuntimeNeverAcceptsAnotherStart(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 50, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "terminal-restart-caller", 6)
	start := standardStart(t, now, authority, "terminal-restart")
	terminal := runtimeFixtureForStart(start, authority)
	terminal.State = RuntimeTerminal
	terminal.Outcome = RuntimeSucceeded
	terminal.Operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: mustOperationID(t, "terminal-original-operation"),
		Digest: digest(30), Generation: start.ExpectedOperationGeneration,
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{terminal},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(10*time.Minute), true)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	rejected := executeDecision(t, harness, start)
	if rejected.Fact.Disposition != DecisionRejected || rejected.Fact.Rejection != CommandRejectionPolicy {
		t.Fatalf("terminal Runtime start decision = %#v", rejected.Fact)
	}
	if rejected.Snapshot.RuntimeRevision != terminal.RuntimeRevision || rejected.Snapshot.State != RuntimeTerminal ||
		rejected.Snapshot.Outcome != RuntimeSucceeded || rejected.Snapshot.Operation != terminal.Operation {
		t.Fatalf("terminal Runtime was reopened: %#v", rejected.Snapshot)
	}

	replayed := executeDecision(t, harness, start)
	if replayed.Fact != rejected.Fact || replayed.Snapshot != rejected.Snapshot {
		t.Fatalf("terminal Runtime rejection replay = %#v", replayed)
	}
}

func TestDecisionIncludesExistingTerminalEvidenceIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 52, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "terminal-evidence-caller", 6)
	start := standardStart(t, now, authority, "terminal-evidence")
	terminal := runtimeFixtureForStart(start, authority)
	terminal.State = RuntimeTerminal
	terminal.Outcome = RuntimeSucceeded
	terminal.TerminalEvidenceID = mustEvidenceID(t, "terminal-evidence-1")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{terminal},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(10*time.Minute), true)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	rejected := executeDecision(t, harness, start)
	if rejected.Fact.TerminalEvidenceID != terminal.TerminalEvidenceID {
		t.Fatalf("terminal evidence identity = %q", rejected.Fact.TerminalEvidenceID.String())
	}
}

func TestHarnessRejectsInvalidTerminalEvidenceFixture(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 53, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "invalid-terminal-evidence-caller", 6)
	start := standardStart(t, now, authority, "invalid-terminal-evidence")
	base := runtimeFixtureForStart(start, authority)
	evidenceID := mustEvidenceID(t, "terminal-evidence-1")

	tests := map[string]func(*RuntimeFixture){
		"nonterminal Runtime": func(fixture *RuntimeFixture) {
			fixture.TerminalEvidenceID = evidenceID
		},
		"terminal Runtime without outcome": func(fixture *RuntimeFixture) {
			fixture.State = RuntimeTerminal
			fixture.TerminalEvidenceID = evidenceID
		},
		"malformed evidence identity": func(fixture *RuntimeFixture) {
			fixture.State = RuntimeTerminal
			fixture.Outcome = RuntimeSucceeded
			fixture.TerminalEvidenceID = EvidenceID{value: "invalid evidence"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := base
			mutate(&fixture)
			_, err := NewDeterministicHarness(HarnessConfig{Now: now, Runtimes: []RuntimeFixture{fixture}})
			assertErrorCode(t, err, ErrorInvalidRequest)
		})
	}
}

func TestNonCreatedRuntimeNeverAcceptsAnotherStart(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 4, 55, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "non-created-restart-caller", 6)
	start := standardStart(t, now, authority, "non-created-restart")
	existing := runtimeFixtureForStart(start, authority)
	existing.State = RuntimeWaitingForLease
	existing.Operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: mustOperationID(t, "non-created-original-operation"),
		Digest: digest(31), Generation: start.ExpectedOperationGeneration,
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{existing},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(10*time.Minute), true)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	rejected := executeDecision(t, harness, start)
	if rejected.Fact.Disposition != DecisionRejected || rejected.Fact.Rejection != CommandRejectionPolicy ||
		rejected.Snapshot.RuntimeRevision != existing.RuntimeRevision || rejected.Snapshot.State != RuntimeWaitingForLease ||
		rejected.Snapshot.Operation != existing.Operation {
		t.Fatalf("non-created Runtime accepted another start: %#v", rejected)
	}
}

func TestCommandRejectionIsNotTerminalRuntimeRejected(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 5, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "rejection-caller", 6)
	commandRejected := standardStart(t, now, authority, "command-rejected")
	terminal := schemaRuntimeFixture(t, authority, "terminal-rejected")
	terminal.State = RuntimeTerminal
	terminal.Outcome = RuntimeRejected
	terminal.Operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: mustOperationID(t, "accepted-terminal-start"),
		Digest: digest(31), Generation: 8,
		AdmissionGrantID: mustAdmissionGrantID(t, "accepted-terminal-grant"), GrantGeneration: 3,
	}
	terminal.Deadline = now.Add(20 * time.Minute)
	terminal.LeaseAcquireBy = now.Add(10 * time.Minute)
	terminal.Capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionRecorded,
		Physical:       PhysicalCapacityNotApplicable,
	}

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now,
		Runtimes: []RuntimeFixture{
			runtimeFixtureForStart(commandRejected, authority),
			terminal,
		},
		AdmissionGrants: []AdmissionGrantFixture{
			grantFixtureForStart(commandRejected, now.Add(-time.Second), true),
		},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	rejected := executeDecision(t, harness, commandRejected)
	if rejected.Fact.Disposition != DecisionRejected || rejected.Fact.Rejection != CommandRejectionPolicy ||
		rejected.Snapshot.State != RuntimeCreated || rejected.Snapshot.Outcome != RuntimeOutcomeNone {
		t.Fatalf("command rejection fabricated a terminal outcome: %#v", rejected)
	}
	terminalSnapshot := inspectRuntime(t, harness, terminal, authority, SchemaV1, SnapshotSchemaCurrent)
	if terminalSnapshot.State != RuntimeTerminal || terminalSnapshot.Outcome != RuntimeRejected ||
		terminalSnapshot.Operation.Status != OperationBound || terminalSnapshot.Capacity.NoLease != NoLeaseDispositionRecorded {
		t.Fatalf("terminal Rejected lacks accepted/no-lease facts: %#v", terminalSnapshot)
	}
}
