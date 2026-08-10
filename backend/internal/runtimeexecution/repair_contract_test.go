package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

// TestExactRepair_ExactMatch_Accepted tests that an exact-matching repair
// candidate is accepted.
func TestExactRepair_ExactMatch_Accepted(t *testing.T) {
	t.Parallel()

	store := &testRepairStore{
		evidence: &RepairEvidence{
			RuntimeRunID:   RuntimeRunID{value: "runtime-repair-1"},
			SchemaVersion:  SchemaV1,
			EvidenceID:     EvidenceID{value: "evidence-repair-1"},
			EvidenceRootID: EvidenceRootID{value: "evidence-root-repair-1"},
			Digest:         digest(100),
			Signature:      digest(101),
			ManifestDigest: digest(102),
			ScopeDigest:    digest(103),
			Generation:     5,
			Fence:          6,
			WorkerClass:    WorkerAgent,
		},
	}
	repair := NewExactRepair(store)

	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-1"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "evidence-repair-1"},
		EvidenceRootID: EvidenceRootID{value: "evidence-root-repair-1"},
		Digest:         digest(100),
		Signature:      digest(101),
		ManifestDigest: digest(102),
		ScopeDigest:    digest(103),
		Generation:     5,
		Fence:          6,
		WorkerClass:    WorkerAgent,
		Source:         RepairSourceBackup,
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("exact match rejected: %v", result.Rejection)
	}
}

// TestExactRepair_DigestMismatch_Rejected tests that a digest mismatch fails closed.
func TestExactRepair_DigestMismatch_Rejected(t *testing.T) {
	t.Parallel()

	retained := &RepairEvidence{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-2"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "evidence-repair-2"},
		EvidenceRootID: EvidenceRootID{value: "evidence-root-repair-2"},
		Digest:         digest(200),
		Signature:      digest(201),
		ManifestDigest: digest(202),
		ScopeDigest:    digest(203),
		Generation:     7,
		Fence:          8,
		WorkerClass:    WorkerTool,
	}
	store := &testRepairStore{evidence: retained}
	repair := NewExactRepair(store)

	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-2"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "evidence-repair-2"},
		EvidenceRootID: EvidenceRootID{value: "evidence-root-repair-2"},
		Digest:         digest(255), // different digest
		Signature:      digest(201),
		ManifestDigest: digest(202),
		ScopeDigest:    digest(203),
		Generation:     7,
		Fence:          8,
		WorkerClass:    WorkerTool,
		Source:         RepairSourceRecoveryPoint,
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if result.Accepted {
		t.Fatal("digest mismatch accepted")
	}
	if result.Rejection != RepairRejectionDigestMismatch {
		t.Fatalf("unexpected rejection reason: %v", result.Rejection)
	}
}

// TestExactRepair_OrphanCandidate_Rejected tests that orphan candidates are rejected.
func TestExactRepair_OrphanCandidate_Rejected(t *testing.T) {
	t.Parallel()

	store := &testRepairStore{evidence: nil} // no retained evidence
	repair := NewExactRepair(store)

	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "orphan-runtime"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "orphan-evidence"},
		EvidenceRootID: EvidenceRootID{value: "orphan-root"},
		Digest:         digest(42),
		Signature:      digest(43),
		ManifestDigest: digest(44),
		ScopeDigest:    digest(45),
		Generation:     1,
		Fence:          1,
		WorkerClass:    WorkerAgent,
		Source:         RepairSourceBackup,
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if result.Accepted {
		t.Fatal("orphan candidate incorrectly accepted")
	}
	if result.Rejection != RepairRejectionNoRetainedEvidence {
		t.Fatalf("unexpected rejection: %v", result.Rejection)
	}
}

// TestExactRepair_InvalidSource_Rejected tests that path/session/log/process
// sources are rejected. Since those sources don't have RepairSource constants,
// we test with an out-of-range source value.
func TestExactRepair_InvalidSource_Rejected(t *testing.T) {
	t.Parallel()

	store := &testRepairStore{
		evidence: &RepairEvidence{
			RuntimeRunID:   RuntimeRunID{value: "runtime-repair-3"},
			SchemaVersion:  SchemaV1,
			EvidenceID:     EvidenceID{value: "ev-3"},
			EvidenceRootID: EvidenceRootID{value: "root-3"},
			Digest:         digest(50),
			Signature:      digest(51),
			ManifestDigest: digest(52),
			ScopeDigest:    digest(53),
			Generation:     3,
			Fence:          3,
			WorkerClass:    WorkerAgent,
		},
	}
	repair := NewExactRepair(store)

	// Source 99 is not a valid repair source (not Backup/ControlledSource/RecoveryPoint).
	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-3"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "ev-3"},
		EvidenceRootID: EvidenceRootID{value: "root-3"},
		Digest:         digest(50),
		Signature:      digest(51),
		ManifestDigest: digest(52),
		ScopeDigest:    digest(53),
		Generation:     3,
		Fence:          3,
		WorkerClass:    WorkerAgent,
		Source:         RepairSource(99),
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if result.Accepted {
		t.Fatal("invalid source accepted")
	}
	if result.Rejection != RepairRejectionInvalidSource {
		t.Fatalf("expected InvalidSource, got %v", result.Rejection)
	}
}

// TestExactRepair_SchemaMismatch_Rejected tests schema version mismatch.
func TestExactRepair_SchemaMismatch_Rejected(t *testing.T) {
	t.Parallel()

	retained := &RepairEvidence{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-4"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "ev-4"},
		EvidenceRootID: EvidenceRootID{value: "root-4"},
		Digest:         digest(60),
		Signature:      digest(61),
		ManifestDigest: digest(62),
		ScopeDigest:    digest(63),
		Generation:     1,
		Fence:          1,
		WorkerClass:    WorkerAgent,
	}
	store := &testRepairStore{evidence: retained}
	repair := NewExactRepair(store)

	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-4"},
		SchemaVersion:  SnapshotSchemaCapsule, // different schema
		EvidenceID:     EvidenceID{value: "ev-4"},
		EvidenceRootID: EvidenceRootID{value: "root-4"},
		Digest:         digest(60),
		Signature:      digest(61),
		ManifestDigest: digest(62),
		ScopeDigest:    digest(63),
		Generation:     1,
		Fence:          1,
		WorkerClass:    WorkerAgent,
		Source:         RepairSourceControlledSource,
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if result.Accepted {
		t.Fatal("schema mismatch accepted")
	}
	if result.Rejection != RepairRejectionSchemaMismatch {
		t.Fatalf("expected SchemaMismatch, got %v", result.Rejection)
	}
}

// TestExactRepair_IdentityMismatch_Rejected tests identity mismatch.
func TestExactRepair_IdentityMismatch_Rejected(t *testing.T) {
	t.Parallel()

	retained := &RepairEvidence{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-5"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "ev-5"},
		EvidenceRootID: EvidenceRootID{value: "root-5"},
		Digest:         digest(70),
		Signature:      digest(71),
		ManifestDigest: digest(72),
		ScopeDigest:    digest(73),
		Generation:     1,
		Fence:          1,
		WorkerClass:    WorkerAgent,
	}
	store := &testRepairStore{evidence: retained}
	repair := NewExactRepair(store)

	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-DIFFERENT"}, // different
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "ev-5"},
		EvidenceRootID: EvidenceRootID{value: "root-5"},
		Digest:         digest(70),
		Signature:      digest(71),
		ManifestDigest: digest(72),
		ScopeDigest:    digest(73),
		Generation:     1,
		Fence:          1,
		WorkerClass:    WorkerAgent,
		Source:         RepairSourceBackup,
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if result.Accepted {
		t.Fatal("identity mismatch accepted")
	}
	if result.Rejection != RepairRejectionIdentityMismatch {
		t.Fatalf("expected IdentityMismatch, got %v", result.Rejection)
	}
}

// TestExactRepair_FenceMismatch_Rejected tests fence mismatch.
func TestExactRepair_FenceMismatch_Rejected(t *testing.T) {
	t.Parallel()

	retained := &RepairEvidence{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-6"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "ev-6"},
		EvidenceRootID: EvidenceRootID{value: "root-6"},
		Digest:         digest(80),
		Signature:      digest(81),
		ManifestDigest: digest(82),
		ScopeDigest:    digest(83),
		Generation:     9,
		Fence:          10,
		WorkerClass:    WorkerAgent,
	}
	store := &testRepairStore{evidence: retained}
	repair := NewExactRepair(store)

	candidate := RepairCandidate{
		RuntimeRunID:   RuntimeRunID{value: "runtime-repair-6"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "ev-6"},
		EvidenceRootID: EvidenceRootID{value: "root-6"},
		Digest:         digest(80),
		Signature:      digest(81),
		ManifestDigest: digest(82),
		ScopeDigest:    digest(83),
		Generation:     9,
		Fence:          99, // different fence
		WorkerClass:    WorkerAgent,
		Source:         RepairSourceBackup,
	}

	result, err := repair.Repair(context.Background(), candidate)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if result.Accepted {
		t.Fatal("fence mismatch accepted")
	}
	if result.Rejection != RepairRejectionFenceMismatch {
		t.Fatalf("expected FenceMismatch, got %v", result.Rejection)
	}
}

// TestReconcilerIntegration_ReconcilerCreatedFromHarness tests that the
// harness exposes a working ReconciliationStateMachine.
func TestReconcilerIntegration_ReconcilerCreatedFromHarness(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "reconciler-int-caller", 2)
	start := standardStart(t, now, authority, "reconciler-int")

	harness := harnessForStartWithDecisionID(t, now, authority, start, 2000)
	executeDecision(t, harness, start)

	reconciler := harness.Reconciler()
	if reconciler == nil {
		t.Fatal("Reconciler() returned nil")
	}

	// Inspect should work with a proper ref.
	snapshot, obligation, err := reconciler.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion:       SchemaV1,
		ProjectionVersion:   SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID:        start.RuntimeRunID,
		Authority:           authority,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if snapshot.State != RuntimeWaitingForLease {
		t.Fatalf("unexpected state: %v", snapshot.State)
	}
	if obligation != nil {
		t.Fatal("no obligation expected for non-reconciling runtime")
	}
}

// TestRepairHelpers tests the helper functions in repair.go.
func TestRepairHelpers(t *testing.T) {
	t.Parallel()

	// IsOrphanCandidate
	if !IsOrphanCandidate(nil) {
		t.Fatal("nil evidence not identified as orphan")
	}
	if !IsOrphanCandidate(&RepairEvidence{}) {
		t.Fatal("empty evidence not identified as orphan")
	}

	// validRepairSource
	if !validRepairSource(RepairSourceBackup) {
		t.Fatal("backup source rejected")
	}
	if !validRepairSource(RepairSourceControlledSource) {
		t.Fatal("controlled source rejected")
	}
	if !validRepairSource(RepairSourceRecoveryPoint) {
		t.Fatal("recovery point source rejected")
	}
	if validRepairSource(RepairSource(0)) {
		t.Fatal("zero source accepted")
	}
	if validRepairSource(RepairSource(99)) {
		t.Fatal("invalid source accepted")
	}

	// CompareRepairEvidence
	a := RepairEvidence{
		RuntimeRunID:   RuntimeRunID{value: "r1"},
		SchemaVersion:  SchemaV1,
		EvidenceID:     EvidenceID{value: "e1"},
		Digest:         digest(1),
		Signature:      digest(2),
		ManifestDigest: digest(3),
		ScopeDigest:    digest(4),
		Generation:     1,
		Fence:          1,
		WorkerClass:    WorkerAgent,
	}
	a.CanonicalDigest = canonicalRepairDigest(a)

	b := a
	b.CanonicalDigest = canonicalRepairDigest(b)

	if !CompareRepairEvidence(a, b) {
		t.Fatal("identical evidence not equal")
	}

	c := a
	c.Fence = 99
	if CompareRepairEvidence(a, c) {
		t.Fatal("different evidence incorrectly equal")
	}

	// canonicalRepairDigest determinism
	d1 := canonicalRepairDigest(a)
	d2 := canonicalRepairDigest(a)
	if d1 != d2 {
		t.Fatal("canonical repair digest not deterministic")
	}
}

// testRepairStore is a minimal RepairStore for testing ExactRepair.
type testRepairStore struct {
	evidence   *RepairEvidence
	rejections []RepairCandidate
}

func (store *testRepairStore) LoadEvidenceForRepair(_ context.Context, _ RuntimeRunID) (*RepairEvidence, error) {
	return store.evidence, nil
}

func (store *testRepairStore) VerifyExactMatch(_ context.Context, _ RepairEvidence, _ RepairCandidate) error {
	return nil
}

func (store *testRepairStore) ApplyExactRepair(_ context.Context, _ RuntimeRunID, _ RepairEvidence) error {
	return nil
}

func (store *testRepairStore) RecordRepairRejection(_ context.Context, _ RuntimeRunID, candidate RepairCandidate, _ RepairRejectionReason) error {
	store.rejections = append(store.rejections, candidate)
	return nil
}
