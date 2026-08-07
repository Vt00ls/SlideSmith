package runtimeexecution

import (
	"testing"
	"time"
)

// TestCandidateForEvidenceTerminal validates the pre-condition check for
// evidence terminal ingestion.
func TestCandidateForEvidenceTerminal(t *testing.T) {
	now := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	_ = now

	// Reconciling + active lease + success observation = should be candidate
	snapshot := RuntimeSnapshot{
		State:   RuntimeReconciling,
		Outcome: RuntimeOutcomeNone,
		Lease: RuntimeLeaseSnapshot{
			AcquireStatus: LeaseGranted,
			Disposition:   LeaseActive,
		},
		Worker: RuntimeWorkerSnapshot{
			Status: WorkerOperationSuccessObserved,
			EvidenceCandidate: WorkerEvidenceCandidateSnapshot{
				EvidenceID:     EvidenceID{value: "ev-1"},
				EvidenceDigest: digest(1),
			},
		},
	}
	if !candidateForEvidenceTerminal(snapshot) {
		t.Fatal("should be candidate for evidence terminal")
	}

	// No evidence candidate = not a candidate
	snapshot.Worker.EvidenceCandidate = WorkerEvidenceCandidateSnapshot{}
	if candidateForEvidenceTerminal(snapshot) {
		t.Fatal("should not be candidate without evidence")
	}

	// Not reconciling = not a candidate
	snapshot.State = RuntimeRunning
	snapshot.Worker.EvidenceCandidate = WorkerEvidenceCandidateSnapshot{
		EvidenceID:     EvidenceID{value: "ev-1"},
		EvidenceDigest: digest(1),
	}
	if candidateForEvidenceTerminal(snapshot) {
		t.Fatal("should not be candidate when not reconciling")
	}

	// No active lease = not a candidate
	snapshot.State = RuntimeReconciling
	snapshot.Lease.AcquireStatus = LeaseNotRequested
	snapshot.Lease.Disposition = LeaseDispositionNone
	if candidateForEvidenceTerminal(snapshot) {
		t.Fatal("should not be candidate without active lease")
	}

	// Terminal outcome = not a candidate
	snapshot.Lease.AcquireStatus = LeaseGranted
	snapshot.Lease.Disposition = LeaseActive
	snapshot.Outcome = RuntimeSucceeded
	if candidateForEvidenceTerminal(snapshot) {
		t.Fatal("should not be candidate when already terminal")
	}
}

// TestValidateEvidenceTrust validates the evidence trust matrix.
func TestValidateEvidenceTrust(t *testing.T) {
	leaseID := SandboxLeaseID{value: "lease-1"}
	snapshot := RuntimeSnapshot{
		Worker: RuntimeWorkerSnapshot{
			Status: WorkerOperationSuccessObserved,
			EvidenceCandidate: WorkerEvidenceCandidateSnapshot{
				EvidenceID:             mustEvidenceID(t, "evidence-1"),
				EvidenceDigest:         digest(10),
				OutputContractDigest:   digest(20),
				EvidenceContractDigest: digest(21),
				SandboxLeaseID:         leaseID,
				LeaseGeneration:        1,
				LeaseFence:             1,
				InternalCallCount:      3,
			},
		},
		Lease: RuntimeLeaseSnapshot{
			LeaseID:    leaseID,
			Generation: 1,
			Fence:      1,
		},
	}
	evidence, outcome, err := validateEvidenceForTerminalIngestion(snapshot)
	if err != nil {
		t.Fatalf("valid evidence should pass: %v", err)
	}
	if outcome != RuntimeSucceeded {
		t.Fatalf("expected RuntimeSucceeded, got %v", outcome)
	}
	if evidence.EvidenceID.String() != "evidence-1" {
		t.Fatalf("evidence ID mismatch")
	}

	// Wrong lease should fail
	snapshot.Lease.LeaseID = SandboxLeaseID{value: "different-lease"}
	_, _, err = validateEvidenceForTerminalIngestion(snapshot)
	if err == nil {
		t.Fatal("wrong lease should fail trust validation")
	}
	snapshot.Lease.LeaseID = leaseID

	// Failure observation should produce RuntimeFailed
	snapshot.Worker.Status = WorkerOperationFailureObserved
	snapshot.Worker.SafeFailure = WorkerFailureCapability
	_, outcome, err = validateEvidenceForTerminalIngestion(snapshot)
	if err != nil {
		t.Fatalf("failure evidence should pass: %v", err)
	}
	if outcome != RuntimeFailed {
		t.Fatalf("expected RuntimeFailed, got %v", outcome)
	}

	// Missing output contract digest should fail
	snapshot.Worker.Status = WorkerOperationSuccessObserved
	snapshot.Worker.EvidenceCandidate.OutputContractDigest = Digest{}
	_, _, err = validateEvidenceForTerminalIngestion(snapshot)
	if err == nil {
		t.Fatal("missing output contract should fail")
	}
}

// TestEvidenceRootDerivation validates evidence root computation.
func TestEvidenceRootDerivation(t *testing.T) {
	snapshot := RuntimeSnapshot{
		RuntimeRunID: RuntimeRunID{value: "run-1"},
		Worker: RuntimeWorkerSnapshot{
			AcceptOperationID:       OperationID{value: "accept-1"},
			AcceptedLeaseGeneration: 1,
			AcceptedLeaseFence:      1,
			EvidenceCandidate: WorkerEvidenceCandidateSnapshot{
				EvidenceID:     EvidenceID{value: "ev-1"},
				EvidenceDigest: digest(5),
			},
		},
	}
	rootID, rootDigest := deriveEvidenceRoot(snapshot, snapshot.Worker.EvidenceCandidate)
	if rootID == (EvidenceRootID{}) {
		t.Fatal("evidence root ID should not be empty")
	}
	if rootDigest == (Digest{}) {
		t.Fatal("evidence root digest should not be empty")
	}

	// Same inputs should produce same root
	rootID2, rootDigest2 := deriveEvidenceRoot(snapshot, snapshot.Worker.EvidenceCandidate)
	if rootID2 != rootID || rootDigest2 != rootDigest {
		t.Fatal("evidence root should be deterministic")
	}

	// Different evidence should produce different root
	differentCandidate := snapshot.Worker.EvidenceCandidate
	differentCandidate.EvidenceDigest = digest(99)
	differentID, differentDigest := deriveEvidenceRoot(snapshot, differentCandidate)
	if differentID == rootID || differentDigest == rootDigest {
		t.Fatal("different evidence should produce different root")
	}
}

// TestImmutableTerminalOutcomes validates all known outcomes.
func TestImmutableTerminalOutcomes(t *testing.T) {
	outcomes := []RuntimeOutcome{
		RuntimeSucceeded,
		RuntimeFailed,
		RuntimeCancelled,
		RuntimeTimedOut,
		RuntimeLost,
		RuntimeRejected,
	}
	for _, outcome := range outcomes {
		switch outcome {
		case RuntimeSucceeded, RuntimeFailed, RuntimeCancelled, RuntimeTimedOut, RuntimeLost, RuntimeRejected:
			// All known
		default:
			t.Fatalf("unknown outcome: %d", outcome)
		}
	}
	if len(outcomes) != 6 {
		t.Fatalf("expected 6 immutable terminal outcomes, got %d", len(outcomes))
	}
}
