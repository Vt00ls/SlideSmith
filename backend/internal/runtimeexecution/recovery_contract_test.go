package runtimeexecution

import (
	"testing"
	"time"
)

// TestRecoveryContract_ClassifyZeroLease_Rejected tests that a runtime with
// zero-lease proof is classified as Rejected after restore, not Lost.
func TestRecoveryContract_ClassifyZeroLease_Rejected(t *testing.T) {
	t.Parallel()

	classification := ClassifyPostRestore(
		RuntimeWaitingForLease, // state
		RuntimeOutcomeNone,     // outcome
		LeaseNotRequested,      // leaseStatus
		LeaseDispositionNone,   // leaseDisposition
		false,                  // hasProcessEvidence
		true,                   // hasNoLeaseDisposition
		RuntimeFence(5),        // preRestoreFence
		RuntimeFence(100),      // newFence
	)

	if classification != RestoreClassificationZeroLeaseRejected {
		t.Fatalf("zero-lease run not classified as Rejected: %v", classification)
	}
}

// TestRecoveryContract_ClassifyAmbiguousLease_Reconcile tests that a runtime
// with an ambiguous lease transaction is classified for reconciliation.
func TestRecoveryContract_ClassifyAmbiguousLease_Reconcile(t *testing.T) {
	t.Parallel()

	classification := ClassifyPostRestore(
		RuntimeWaitingForLease,
		RuntimeOutcomeNone,
		LeaseAcquirePending, // lease in progress
		LeaseDispositionNone,
		false,
		false,
		RuntimeFence(5),
		RuntimeFence(100),
	)

	if classification != RestoreClassificationAmbiguousReconcile {
		t.Fatalf("ambiguous lease not classified for reconciliation: %v", classification)
	}
}

// TestRecoveryContract_ClassifyReconciliationRequired_Reconcile tests that
// reconciliation-required lease status leads to reconciliation.
func TestRecoveryContract_ClassifyReconciliationRequired_Reconcile(t *testing.T) {
	t.Parallel()

	classification := ClassifyPostRestore(
		RuntimeReconciling,
		RuntimeOutcomeNone,
		LeaseAcquireReconciliationRequired,
		LeaseDispositionNone,
		false,
		false,
		RuntimeFence(5),
		RuntimeFence(100),
	)

	if classification != RestoreClassificationAmbiguousReconcile {
		t.Fatalf("lease reconciliation-required not classified for reconciliation: %v", classification)
	}
}

// TestRecoveryContract_ClassifyPossibleProcessEffect_Lost tests that a runtime
// with a granted lease and process evidence is classified as Lost.
func TestRecoveryContract_ClassifyPossibleProcessEffect_Lost(t *testing.T) {
	t.Parallel()

	classification := ClassifyPostRestore(
		RuntimeRunning,
		RuntimeOutcomeNone,
		LeaseGranted,
		LeaseActive,
		true, // hasProcessEvidence
		false,
		RuntimeFence(5),
		RuntimeFence(100),
	)

	if classification != RestoreClassificationPossibleEffectLost {
		t.Fatalf("possible process effect not classified as Lost: %v", classification)
	}
}

// TestRecoveryContract_ClassifyAlreadyTerminal_NoChange tests that already
// terminal runtimes are not reclassified.
func TestRecoveryContract_ClassifyAlreadyTerminal_NoChange(t *testing.T) {
	t.Parallel()

	classification := ClassifyPostRestore(
		RuntimeTerminal,
		RuntimeSucceeded,
		LeaseNotRequested,
		LeaseReleased,
		false,
		false,
		RuntimeFence(5),
		RuntimeFence(100),
	)

	if classification != RestoreClassificationAlreadyTerminal {
		t.Fatalf("terminal runtime reclassified: %v", classification)
	}
}

// TestRecoveryContract_ClassifyFenced_BehindFence tests that runtimes behind
// the recovery fence are classified as fenced.
func TestRecoveryContract_ClassifyFenced_BehindFence(t *testing.T) {
	t.Parallel()

	// preRestoreFence >= newFence means fence is already ahead.
	classification := ClassifyPostRestore(
		RuntimeReconciling,
		RuntimeOutcomeNone,
		LeaseNotRequested,
		LeaseDispositionNone,
		false,
		false,
		RuntimeFence(200),
		RuntimeFence(100),
	)

	if classification != RestoreClassificationFenced {
		t.Fatalf("behind-fence runtime not classified as fenced: %v", classification)
	}
}

// TestRecoveryContract_ValidRestoreDecision tests restore decision validation.
func TestRecoveryContract_ValidRestoreDecision(t *testing.T) {
	t.Parallel()

	valid := RestoreDecision{
		RecoveryPointID:     "rp-001",
		NewGeneration:       2,
		NewFence:            2,
		NewSafetyEpoch:      2,
		PreviousGeneration:  1,
		PreviousFence:       1,
		PreviousSafetyEpoch: 1,
		Mode:                OperationalReadOnly,
		DecidedAt:           time.Now(),
	}
	if !validRestoreDecision(valid) {
		t.Fatal("valid restore decision rejected")
	}

	invalidGen := valid
	invalidGen.NewGeneration = 0
	if validRestoreDecision(invalidGen) {
		t.Fatal("zero generation accepted")
	}

	invalidFence := valid
	invalidFence.NewFence = invalidFence.PreviousFence
	if validRestoreDecision(invalidFence) {
		t.Fatal("non-increasing fence accepted")
	}

	invalidMode := valid
	invalidMode.Mode = OperationalMode(99)
	if validRestoreDecision(invalidMode) {
		t.Fatal("invalid mode accepted")
	}

	emptyRP := valid
	emptyRP.RecoveryPointID = ""
	if validRestoreDecision(emptyRP) {
		t.Fatal("empty recovery point ID accepted")
	}
}

// TestRecoveryContract_OperationalModeNames tests mode name conversion.
func TestRecoveryContract_OperationalModeNames(t *testing.T) {
	t.Parallel()

	if operationalModeName(OperationalReadOnly) != "read_only" {
		t.Fatalf("unexpected read_only name: %s", operationalModeName(OperationalReadOnly))
	}
	if operationalModeName(OperationalFullReady) != "full_ready" {
		t.Fatalf("unexpected full_ready name: %s", operationalModeName(OperationalFullReady))
	}
	if operationalModeName(OperationalMode(99)) != "" {
		t.Fatal("invalid mode returned non-empty name")
	}
}

// TestRecoveryContract_CanonicalRecoveryDigest tests that canonical digests
// are deterministic.
func TestRecoveryContract_CanonicalRecoveryDigest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	decision := RestoreDecision{
		RecoveryPointID:     "rp-test",
		NewGeneration:       3,
		NewFence:            3,
		NewSafetyEpoch:      3,
		PreviousGeneration:  2,
		PreviousFence:       2,
		PreviousSafetyEpoch: 2,
		Mode:                OperationalFullReady,
		DecidedAt:           now,
	}

	first := canonicalRecoveryDigest(decision)
	second := canonicalRecoveryDigest(decision)

	if first != second {
		t.Fatal("canonical digest not deterministic")
	}

	// Different decision produces different digest.
	alt := decision
	alt.NewGeneration = 4
	altDigest := canonicalRecoveryDigest(alt)
	if first == altDigest {
		t.Fatal("different decisions produced same canonical digest")
	}
}

// TestRecoveryContract_ZeroLeaseProofed tests IsZeroLeaseProved helper.
func TestRecoveryContract_ZeroLeaseProofed(t *testing.T) {
	t.Parallel()

	snapshot := RuntimeSnapshot{
		Lease: RuntimeLeaseSnapshot{
			AcquireStatus: LeaseNotRequested,
			Disposition:   LeaseDispositionNone,
		},
		Capacity: RuntimeCapacitySnapshot{
			NoLease: NoLeaseDispositionRecorded,
		},
	}
	if !IsZeroLeaseProved(snapshot) {
		t.Fatal("zero-lease snapshot not recognized")
	}

	snapshotWithLease := snapshot
	snapshotWithLease.Lease.AcquireStatus = LeaseGranted
	if IsZeroLeaseProved(snapshotWithLease) {
		t.Fatal("leased snapshot incorrectly identified as zero-lease")
	}
}

// TestRecoveryContract_PossibleProcessEffectDetection tests process effect detection.
func TestRecoveryContract_PossibleProcessEffectDetection(t *testing.T) {
	t.Parallel()

	snapshot := RuntimeSnapshot{
		Lease: RuntimeLeaseSnapshot{
			AcquireStatus: LeaseGranted,
			Disposition:   LeaseActive,
		},
		Worker: RuntimeWorkerSnapshot{
			Status: WorkerOperationAccepted,
		},
	}
	if !IsPossibleProcessEffect(snapshot) {
		t.Fatal("active lease with accepted worker not identified as possible process effect")
	}

	snapshotNoWorker := snapshot
	snapshotNoWorker.Worker.Status = WorkerOperationNone
	if IsPossibleProcessEffect(snapshotNoWorker) {
		t.Fatal("no-worker snapshot incorrectly identified as possible process effect")
	}
}
