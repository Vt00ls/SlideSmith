package runtimeexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
)

// ExactRepair owns the repair authority. It accepts only exact matching
// identity, schema, digest, signature, manifest, scope, generation, and fence.
// Orphan output, path, session, log, or process evidence is never adopted as
// success.
type ExactRepair struct {
	store RepairStore
}

// RepairStore is the persistence contract for exact repair operations.
type RepairStore interface {
	// LoadEvidenceForRepair loads the retained evidence root for a runtime run.
	LoadEvidenceForRepair(ctx context.Context, runtimeRunID RuntimeRunID) (*RepairEvidence, error)

	// VerifyExactMatch checks that the repair candidate exactly matches the
	// retained authoritative evidence. Returns nil if it matches, or an
	// error describing the mismatch.
	VerifyExactMatch(ctx context.Context, retained RepairEvidence, candidate RepairCandidate) error

	// ApplyExactRepair persists the repaired evidence, but only if it exactly
	// matches the retained identity, schema, digest, and scope.
	ApplyExactRepair(ctx context.Context, runtimeRunID RuntimeRunID, evidence RepairEvidence) error

	// RecordRepairRejection records an orphan or mismatched repair attempt
	// for diagnostic/audit purposes without adopting it.
	RecordRepairRejection(ctx context.Context, runtimeRunID RuntimeRunID, candidate RepairCandidate, reason RepairRejectionReason) error
}

// RepairEvidence is the retained authoritative evidence for exact matching.
type RepairEvidence struct {
	RuntimeRunID    RuntimeRunID
	SchemaVersion   SchemaVersion
	EvidenceID      EvidenceID
	EvidenceRootID  EvidenceRootID
	Digest          Digest
	Signature       Digest
	ManifestDigest  Digest
	ScopeDigest     Digest
	Generation      OperationGeneration
	Fence           RuntimeFence
	WorkerClass     WorkerClass
	CanonicalDigest Digest
}

// RepairCandidate is a proposed repair that must exactly match the retained
// evidence.
type RepairCandidate struct {
	RuntimeRunID   RuntimeRunID
	SchemaVersion  SchemaVersion
	EvidenceID     EvidenceID
	EvidenceRootID EvidenceRootID
	Digest         Digest
	Signature      Digest
	ManifestDigest Digest
	ScopeDigest    Digest
	Generation     OperationGeneration
	Fence          RuntimeFence
	WorkerClass    WorkerClass
	Source         RepairSource
}

// RepairSource describes where the candidate came from (must never be
// path/session/log/process).
type RepairSource uint8

const (
	RepairSourceBackup           RepairSource = iota + 1 // exact backup copy
	RepairSourceControlledSource                         // controlled external source with exact match
	RepairSourceRecoveryPoint                            // from a verified Recovery Point
)

// RepairRejectionReason explains why an exact repair was rejected.
type RepairRejectionReason uint8

const (
	RepairRejectionNone               RepairRejectionReason = iota
	RepairRejectionOrphan                                   // no retained evidence for this identity
	RepairRejectionDigestMismatch                           // digest doesn't match retained
	RepairRejectionSchemaMismatch                           // schema version doesn't match
	RepairRejectionIdentityMismatch                         // identity doesn't match
	RepairRejectionScopeMismatch                            // scope/personal workspace doesn't match
	RepairRejectionGenerationMismatch                       // generation doesn't match
	RepairRejectionFenceMismatch                            // fence doesn't match
	RepairRejectionSignatureMismatch                        // signature doesn't match
	RepairRejectionManifestMismatch                         // manifest digest doesn't match
	RepairRejectionInvalidSource                            // source is path/session/log/process
	RepairRejectionNoRetainedEvidence                       // no authoritative retained evidence
)

// RepairResult reports the outcome of a repair attempt.
type RepairResult struct {
	Accepted  bool
	Rejection RepairRejectionReason
	Evidence  *RepairEvidence
}

// NewExactRepair creates a new exact repair authority.
func NewExactRepair(store RepairStore) *ExactRepair {
	return &ExactRepair{store: store}
}

// Repair attempts to apply an exact repair. It fails closed: the candidate
// must exactly match the retained evidence in identity, schema, digest,
// signature, manifest, scope, generation, and fence.
func (repair *ExactRepair) Repair(
	ctx context.Context,
	candidate RepairCandidate,
) (RepairResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return RepairResult{}, newError(ErrorDependencyUnavailable)
	}

	// Reject any candidate from an invalid source (path/session/log/process).
	if !validRepairSource(candidate.Source) {
		_ = repair.store.RecordRepairRejection(ctx, candidate.RuntimeRunID, candidate, RepairRejectionInvalidSource)
		return RepairResult{Accepted: false, Rejection: RepairRejectionInvalidSource}, nil
	}

	// Load retained evidence.
	retained, err := repair.store.LoadEvidenceForRepair(ctx, candidate.RuntimeRunID)
	if err != nil {
		return RepairResult{}, err
	}
	if retained == nil {
		_ = repair.store.RecordRepairRejection(ctx, candidate.RuntimeRunID, candidate, RepairRejectionNoRetainedEvidence)
		return RepairResult{Accepted: false, Rejection: RepairRejectionNoRetainedEvidence}, nil
	}

	// Exact match verification.
	rejection := verifyExactMatch(retained, candidate)
	if rejection != RepairRejectionNone {
		_ = repair.store.RecordRepairRejection(ctx, candidate.RuntimeRunID, candidate, rejection)
		return RepairResult{Accepted: false, Rejection: rejection}, nil
	}

	// Persist-side verification.
	if err := repair.store.VerifyExactMatch(ctx, *retained, candidate); err != nil {
		_ = repair.store.RecordRepairRejection(ctx, candidate.RuntimeRunID, candidate, RepairRejectionDigestMismatch)
		return RepairResult{Accepted: false, Rejection: RepairRejectionDigestMismatch}, nil
	}

	// Apply the repair.
	if err := repair.store.ApplyExactRepair(ctx, candidate.RuntimeRunID, *retained); err != nil {
		return RepairResult{}, err
	}

	return RepairResult{Accepted: true, Evidence: retained}, nil
}

// verifyExactMatch checks every field of the candidate against the retained
// evidence. Returns the first rejection reason found, or None.
func verifyExactMatch(retained *RepairEvidence, candidate RepairCandidate) RepairRejectionReason {
	if retained.RuntimeRunID != candidate.RuntimeRunID {
		return RepairRejectionIdentityMismatch
	}
	if retained.EvidenceID != candidate.EvidenceID {
		return RepairRejectionIdentityMismatch
	}
	if retained.EvidenceRootID != candidate.EvidenceRootID {
		return RepairRejectionIdentityMismatch
	}
	if retained.SchemaVersion != candidate.SchemaVersion {
		return RepairRejectionSchemaMismatch
	}
	if !CompareReconciliationDigests(retained.Digest, candidate.Digest) {
		return RepairRejectionDigestMismatch
	}
	if !CompareReconciliationDigests(retained.Signature, candidate.Signature) {
		return RepairRejectionSignatureMismatch
	}
	if !CompareReconciliationDigests(retained.ManifestDigest, candidate.ManifestDigest) {
		return RepairRejectionManifestMismatch
	}
	if !CompareReconciliationDigests(retained.ScopeDigest, candidate.ScopeDigest) {
		return RepairRejectionScopeMismatch
	}
	if retained.Generation != candidate.Generation {
		return RepairRejectionGenerationMismatch
	}
	if retained.Fence != candidate.Fence {
		return RepairRejectionFenceMismatch
	}
	if retained.WorkerClass != candidate.WorkerClass {
		return RepairRejectionIdentityMismatch
	}
	return RepairRejectionNone
}

// validRepairSource returns true only for accepted backup, controlled source,
// or recovery point sources. Path, session, log, and process sources are
// explicitly rejected.
func validRepairSource(source RepairSource) bool {
	return source == RepairSourceBackup ||
		source == RepairSourceControlledSource ||
		source == RepairSourceRecoveryPoint
}

// IsOrphanCandidate returns true when there is no retained evidence for the
// candidate's identity, making it an orphan that must not be adopted.
func IsOrphanCandidate(retained *RepairEvidence) bool {
	return retained == nil || retained.RuntimeRunID == (RuntimeRunID{})
}

// canonicalRepairDigest produces a canonical digest for exact repair matching.
func canonicalRepairDigest(evidence RepairEvidence) Digest {
	payload := fmt.Sprintf("slidesmith.exact-repair/v1\n%s\n%d\n%s\n%s",
		evidence.RuntimeRunID.String(),
		evidence.SchemaVersion,
		evidence.EvidenceID.String(),
		evidence.Digest.String(),
	)
	return Digest(sha256.Sum256([]byte(payload)))
}

// CompareRepairEvidence returns true if two repair evidence records are
// exactly equal in all fields.
func CompareRepairEvidence(a, b RepairEvidence) bool {
	return a.RuntimeRunID == b.RuntimeRunID &&
		a.SchemaVersion == b.SchemaVersion &&
		a.EvidenceID == b.EvidenceID &&
		a.EvidenceRootID == b.EvidenceRootID &&
		CompareReconciliationDigests(a.Digest, b.Digest) &&
		CompareReconciliationDigests(a.Signature, b.Signature) &&
		CompareReconciliationDigests(a.ManifestDigest, b.ManifestDigest) &&
		CompareReconciliationDigests(a.ScopeDigest, b.ScopeDigest) &&
		a.Generation == b.Generation &&
		a.Fence == b.Fence &&
		a.WorkerClass == b.WorkerClass &&
		bytes.Equal(a.CanonicalDigest[:], b.CanonicalDigest[:])
}

// EnsureRepairInterface checks compile-time interface satisfaction.
var _ interface {
	Repair(ctx context.Context, candidate RepairCandidate) (RepairResult, error)
} = (*ExactRepair)(nil)
