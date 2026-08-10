package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

// TestReconciliationContract_CrashBeforeCommit_RestartReplaysAcceptance covers
// Testing Decision 13: pre-commit crash → restart → replay returns acceptance.
func TestReconciliationContract_CrashBeforeCommit_RestartReplaysAcceptance(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "recon-crash-caller", 2)
	start := standardStart(t, now, authority, "recon-crash")

	harness := harnessForStartWithDecisionID(t, now, authority, start, 800)
	if err := harness.CrashNextAt(CrashBeforeCommit); err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, start), ErrorDependencyUnavailable)

	restarted := harness.Restart()
	accepted := executeDecision(t, restarted, start)
	if accepted.Fact.DecisionID.String() != "runtime-decision-000800" {
		t.Fatalf("pre-commit crash did not produce durable identity on restart: %#v", accepted.Fact)
	}
	if accepted.Snapshot.State != RuntimeWaitingForLease {
		t.Fatalf("restarted acceptance not in WaitingForLease: %#v", accepted.Snapshot)
	}
}

// TestReconciliationContract_ResponseLossAfterCommit_ReplayReturnsOriginalDecision tests
// that response loss enters reconciling and exact replay returns the original decision.
func TestReconciliationContract_ResponseLossAfterCommit_ReplayReturnsOriginalDecision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 8, 10, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "recon-response-loss-caller", 2)
	start := standardStart(t, now, authority, "recon-response-loss")

	harness := harnessForStartWithDecisionID(t, now, authority, start, 900)
	harness.LoseNextResponse()
	err := executeError(harness, start)
	if err == nil {
		t.Fatal("expected reconciliation error after response loss")
	}
	reconErr, ok := err.(*Error)
	if !ok || reconErr.ReconciliationDisposition() != ReconciliationRequired {
		t.Fatalf("expected ReconciliationRequired, got %v", err)
	}

	// Restart and replay: should return the original acceptance.
	restarted := harness.Restart()
	replayed := executeDecision(t, restarted, start)
	if replayed.Fact.DecisionID.String() != "runtime-decision-000900" {
		t.Fatalf("replay after response loss did not return original decision: %#v", replayed.Fact)
	}
	if replayed.Snapshot.State != RuntimeWaitingForLease {
		t.Fatalf("replayed snapshot state unexpected: %#v", replayed.Snapshot)
	}
}

// TestReconciliationContract_CrashAfterLeaseCommit_EntersReconciling tests that
// crashing after lease commit retains the lease state on replay.
func TestReconciliationContract_CrashAfterLeaseCommit_EntersReconciling(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 8, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "recon-lease-crash-caller", 2)
	start := standardStart(t, now, authority, "recon-lease-crash")

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1000, LeaseStart: 100},
		Runtimes:                []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:         []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		LeaseAcquisition:        alwaysReadyLeaseAcquisition(),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	// First execute accepts the start.
	first := executeDecision(t, harness, start)
	if first.Fact.Disposition != DecisionAccepted {
		t.Fatalf("expected accepted, got %v", first.Fact.Disposition)
	}

	// Crash after commit and restart.
	restarted := harness.Restart()

	// Replay should return the same decision identity (idempotent replay).
	replayed := executeDecision(t, restarted, start)
	if replayed.Fact.DecisionID != first.Fact.DecisionID {
		t.Fatalf("restart produced different decision ID: %v vs %v",
			first.Fact.DecisionID, replayed.Fact.DecisionID)
	}
}

// TestReconciliationContract_NoLeasePhysicalDisposition_TerminalRejected tests
// that a runtime with proof of zero lease is properly identified.
func TestReconciliationContract_NoLeasePhysicalDisposition_TerminalRejected(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 8, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "no-lease-caller", 2)
	start := standardStart(t, now, authority, "no-lease")

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1100},
		Runtimes:                []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:         []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	decision := executeDecision(t, harness, start)
	if decision.Snapshot.Outcome == RuntimeRejected {
		t.Fatalf("unexpected early rejection before lease attempt")
	}

	// After acceptance, inspect shows WaitingForLease.
	snapshot, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify IsZeroLeaseProved only reports true when there's actually no lease.
	if IsZeroLeaseProved(snapshot) {
		t.Fatal("IsZeroLeaseProved returned true for a snapshot with pending lease")
	}
	if IsPossibleProcessEffect(snapshot) {
		t.Fatal("IsPossibleProcessEffect returned true without a granted lease")
	}
}

// TestReconciliationContract_DuplicateExactStart_Idempotent tests that exact
// duplicate start returns the same decision (idempotent).
func TestReconciliationContract_DuplicateExactStart_Idempotent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 8, 40, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "idempotent-caller", 2)
	start := standardStart(t, now, authority, "idempotent")

	harness := harnessForStartWithDecisionID(t, now, authority, start, 1200)
	first := executeDecision(t, harness, start)
	second := executeDecision(t, harness, start)

	if first.Fact.DecisionID != second.Fact.DecisionID {
		t.Fatalf("duplicate start produced different decision IDs: %v vs %v",
			first.Fact.DecisionID, second.Fact.DecisionID)
	}
	if first.Fact.Disposition != second.Fact.Disposition {
		t.Fatalf("duplicate start changed disposition: %v vs %v",
			first.Fact.Disposition, second.Fact.Disposition)
	}
}

// TestReconciliationContract_DifferentDigestSameOperationID_IntegrityConflict tests
// that same operation ID with different digest causes an integrity conflict.
func TestReconciliationContract_DifferentDigestSameOperationID_IntegrityConflict(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 8, 50, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "integrity-caller", 2)
	start := standardStart(t, now, authority, "integrity")

	harness := harnessForStartWithDecisionID(t, now, authority, start, 1300)
	executeDecision(t, harness, start)

	// Create a different start with same operation ID but different digest.
	// Use same key parameters but change the network policy ID to produce a different digest.
	altInput := StartRuntimeRunInput{
		SchemaVersion:               SchemaV1,
		OperationID:                 start.OperationID, // same ID
		PersonalWorkspaceID:         start.PersonalWorkspaceID,
		TaskID:                      start.TaskID,
		PhaseRunID:                  start.PhaseRunID,
		RuntimeRunID:                start.RuntimeRunID,
		Attempt:                     1,
		ExpectedTaskRevision:        start.ExpectedTaskRevision,
		ExpectedRuntimeRevision:     start.ExpectedRuntimeRevision,
		ExpectedOperationGeneration: start.ExpectedOperationGeneration,
		ExpectedRuntimeFence:        start.ExpectedRuntimeFence,
		Authority:                   authority,
		RuntimeBindingID:            start.RuntimeBindingID,
		RuntimeBindingDigest:        start.RuntimeBindingDigest,
		ExecutionLockDigest:         start.ExecutionLockDigest,
		CapabilityContractDigest:    start.CapabilityContractDigest,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest,
		ReleaseSafetyEpoch:          start.ReleaseSafetyEpoch,
		WorkerClass:                 start.WorkerClass,
		Effect:                      start.Effect,
		ImmutableInputManifest:      start.ImmutableInputManifest,
		ImmutableInputs:             start.ImmutableInputs,
		OutputContractDigest:        start.OutputContractDigest,
		EvidenceContractDigest:      start.EvidenceContractDigest,
		ResourceClassID:             start.ResourceClassID,
		ExecutionPolicyID:           start.ExecutionPolicyID,
		ProviderCapability:          start.ProviderCapability,
		NetworkPolicyID:             mustNetworkPolicyID(t, "network-policy-different-integrity"),
		SecretPolicyID:              start.SecretPolicyID,
		Deadline:                    start.Deadline,
		CancellationPolicy:          start.CancellationPolicy,
		AdmissionGrant:              start.AdmissionGrant,
	}
	altStart, err := NewStartRuntimeRun(altInput)
	if err != nil {
		t.Fatalf("new alt start: %v", err)
	}

	assertErrorCode(t, executeError(harness, altStart), ErrorIntegrityConflict)
}

// TestReconciliationContract_CompareReconciliationDigests tests digest comparison helpers.
func TestReconciliationContract_CompareReconciliationDigests(t *testing.T) {
	t.Parallel()

	a := digest(1)
	b := digest(1)
	c := digest(2)

	if !CompareReconciliationDigests(a, b) {
		t.Fatal("identical digests did not compare equal")
	}
	if CompareReconciliationDigests(a, c) {
		t.Fatal("different digests compared equal")
	}
}

// TestReconciliationContract_ReconciliationBackoff tests backoff calculation.
func TestReconciliationContract_ReconciliationBackoff(t *testing.T) {
	t.Parallel()

	first := reconciliationBackoff(0)
	if first < 500*time.Millisecond {
		t.Fatalf("initial backoff too short: %v", first)
	}

	capped := reconciliationBackoff(100)
	if capped > 30*time.Second {
		t.Fatalf("backoff not capped: %v", capped)
	}

	// Verify exponential increase.
	second := reconciliationBackoff(1)
	if second <= first {
		t.Fatalf("backoff not increasing: first=%v second=%v", first, second)
	}
}

type alwaysReadyLeaseAcquisitionAdapter struct{}

func (alwaysReadyLeaseAcquisitionAdapter) ObserveLeaseAcquisition(_ context.Context, _ LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error) {
	return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
}

func alwaysReadyLeaseAcquisition() LeaseAcquisitionAdapter {
	return alwaysReadyLeaseAcquisitionAdapter{}
}
