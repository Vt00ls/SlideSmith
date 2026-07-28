package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const (
	postgresCleanupResolutionProofSchema           = "slidesmith.runtime-execution.cleanup-resolution-proof/v1"
	postgresCleanupResolutionProofDomain           = postgresCleanupResolutionProofSchema + "\n"
	postgresCleanupResolutionProofIntegrityVersion = 1
)

type cleanupResolutionProofDisposition uint8

const (
	cleanupProofDeletionOrReset cleanupResolutionProofDisposition = iota + 1
	cleanupProofExactGenerationAbsent
	cleanupProofRetainedByAuthority
)

type cleanupResolutionProofState struct {
	ProofID                    string                            `json:"proof_id"`
	SchemaVersion              SchemaVersion                     `json:"schema_version"`
	IntegrityVersion           uint16                            `json:"integrity_version"`
	OwningModule               string                            `json:"owning_module"`
	DebtID                     string                            `json:"debt_id"`
	RuntimeRunID               string                            `json:"runtime_run_id"`
	ResourceClass              cleanupResourceClass              `json:"resource_class"`
	ResourceIdentityDigest     string                            `json:"resource_identity_digest"`
	ResourceGeneration         uint64                            `json:"resource_generation"`
	ResourceFence              uint64                            `json:"resource_fence"`
	ResolutionClass            cleanupResolutionClass            `json:"resolution_class"`
	ResolutionReason           cleanupResolutionReason           `json:"resolution_reason"`
	Disposition                cleanupResolutionProofDisposition `json:"disposition"`
	EvidenceSchemaVersion      SchemaVersion                     `json:"evidence_schema_version"`
	EvidenceRootID             string                            `json:"evidence_root_id"`
	EvidenceRootDigest         string                            `json:"evidence_root_digest"`
	DeletionOrResetProven      bool                              `json:"deletion_or_reset_proven"`
	ExactGenerationAbsent      bool                              `json:"exact_generation_absent"`
	ReferencesClear            bool                              `json:"references_clear"`
	ContainmentClear           bool                              `json:"containment_clear"`
	RetainingAuthorityFactRoot string                            `json:"retaining_authority_fact_root"`
	ObservedAt                 string                            `json:"observed_at"`
	RecordedAt                 string                            `json:"recorded_at"`
	SourceClockID              string                            `json:"source_clock_id"`
}

type cleanupResolutionProofView struct {
	State           cleanupResolutionProofState
	CanonicalDigest Digest
}

func encodeCleanupResolutionProof(state cleanupResolutionProofState) ([]byte, Digest, error) {
	if !validCleanupResolutionProofState(state) {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return encoded, digestBytes(append([]byte(postgresCleanupResolutionProofDomain), encoded...)), nil
}

func decodeCleanupResolutionProof(encoded []byte) (cleanupResolutionProofState, Digest, error) {
	var state cleanupResolutionProofState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil ||
		!validCleanupResolutionProofState(state) {
		return cleanupResolutionProofState{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		return cleanupResolutionProofState{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return state, digestBytes(append([]byte(postgresCleanupResolutionProofDomain), canonical...)), nil
}

func validCleanupResolutionProofState(state cleanupResolutionProofState) bool {
	if !validOpaqueID(state.ProofID) || state.SchemaVersion != SchemaV1 ||
		state.IntegrityVersion != postgresCleanupResolutionProofIntegrityVersion ||
		state.OwningModule != postgresCleanupOwnerModule || !validOpaqueID(state.DebtID) ||
		!validOpaqueID(state.RuntimeRunID) ||
		state.ResourceClass < cleanupResourceProcess || state.ResourceClass > cleanupResourceReset ||
		!validDigestText(state.ResourceIdentityDigest) || state.ResourceGeneration == 0 || state.ResourceFence == 0 ||
		state.ResolutionClass < cleanupResolutionReclaimed ||
		state.ResolutionClass > cleanupResolutionRetainedByAuthority ||
		state.ResolutionReason < cleanupResolutionCleanupProven ||
		state.ResolutionReason > cleanupResolutionCurrentAuthorityRetention ||
		state.EvidenceSchemaVersion != SchemaV1 || !validOpaqueID(state.EvidenceRootID) ||
		!validDigestText(state.EvidenceRootDigest) || state.SourceClockID != postgresMandatoryAuditSourceClock {
		return false
	}
	observedAt, err := time.Parse(canonicalTimeFormat, state.ObservedAt)
	if err != nil {
		return false
	}
	recordedAt, err := time.Parse(canonicalTimeFormat, state.RecordedAt)
	if err != nil || recordedAt.Before(observedAt) {
		return false
	}
	switch state.ResolutionClass {
	case cleanupResolutionReclaimed:
		return state.ResolutionReason == cleanupResolutionCleanupProven &&
			state.Disposition == cleanupProofDeletionOrReset && state.DeletionOrResetProven &&
			!state.ExactGenerationAbsent && state.ReferencesClear && state.ContainmentClear &&
			state.RetainingAuthorityFactRoot == ""
	case cleanupResolutionAlreadyAbsent:
		return state.ResolutionReason == cleanupResolutionExactGenerationAbsent &&
			state.Disposition == cleanupProofExactGenerationAbsent && !state.DeletionOrResetProven &&
			state.ExactGenerationAbsent && state.ReferencesClear && state.ContainmentClear &&
			state.RetainingAuthorityFactRoot == ""
	case cleanupResolutionRetainedByAuthority:
		return state.ResolutionReason == cleanupResolutionCurrentAuthorityRetention &&
			state.Disposition == cleanupProofRetainedByAuthority && !state.DeletionOrResetProven &&
			!state.ExactGenerationAbsent && validDigestText(state.RetainingAuthorityFactRoot)
	default:
		return false
	}
}

func cleanupResolutionProofMatchesRecord(state cleanupResolutionProofState, record cleanupDebtRecord) bool {
	return state.DebtID == record.DebtID && state.RuntimeRunID == record.RuntimeRunID.String() &&
		state.ResourceClass == record.ResourceClass &&
		state.ResourceIdentityDigest == record.ResourceIdentityDigest.String() &&
		state.ResourceGeneration == record.ResourceGeneration && state.ResourceFence == record.ResourceFence &&
		state.ResolutionClass == record.ResolutionClass && state.ResolutionReason == record.ResolutionReason &&
		state.EvidenceSchemaVersion == record.ResolutionEvidenceRoot.SchemaVersion &&
		state.EvidenceRootID == record.ResolutionEvidenceRoot.EvidenceRootID.String() &&
		state.EvidenceRootDigest == record.ResolutionEvidenceRoot.Digest.String() &&
		state.ObservedAt == formatCleanupTime(record.ResolvedAt) &&
		state.RecordedAt == formatCleanupTime(record.ResolvedAt)
}

func (authority *PostgresAuthority) verifyRetainedCleanupResolutionProof(
	ctx context.Context,
	tx *sql.Tx,
	record cleanupDebtRecord,
) (cleanupResolutionProofView, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT proof_id, runtime_run_id, resource_identity_digest,
		resource_generation, resource_fence, resolution_reason, proof_disposition,
		evidence_root_digest, canonical_digest, proof_state FROM %s
		WHERE debt_id=$1 AND resolution_class=$2 AND evidence_root_id=$3`,
		authority.table("runtime_execution_cleanup_resolution_proofs")), record.DebtID,
		record.ResolutionClass, record.ResolutionEvidenceRoot.EvidenceRootID.String())
	if err != nil {
		return cleanupResolutionProofView{}, normalizeRuntimePersistenceFailure(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return cleanupResolutionProofView{}, normalizeRuntimePersistenceFailure(err)
		}
		return cleanupResolutionProofView{}, newError(ErrorIntegrityConflict)
	}
	var proofID, runtimeRunID string
	var resourceIdentityDigest, evidenceRootDigest, canonicalDigest, encoded []byte
	var resourceGeneration, resourceFence uint64
	var resolutionReason cleanupResolutionReason
	var disposition cleanupResolutionProofDisposition
	if err := rows.Scan(&proofID, &runtimeRunID, &resourceIdentityDigest, &resourceGeneration,
		&resourceFence, &resolutionReason, &disposition, &evidenceRootDigest, &canonicalDigest, &encoded); err != nil {
		return cleanupResolutionProofView{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows.Next() {
		return cleanupResolutionProofView{}, newError(ErrorIntegrityConflict)
	}
	if err := rows.Err(); err != nil {
		return cleanupResolutionProofView{}, normalizeRuntimePersistenceFailure(err)
	}
	state, wantDigest, err := decodeCleanupResolutionProof(encoded)
	if err != nil || proofID != state.ProofID || runtimeRunID != record.RuntimeRunID.String() ||
		!bytes.Equal(resourceIdentityDigest, record.ResourceIdentityDigest[:]) ||
		resourceGeneration != record.ResourceGeneration || resourceFence != record.ResourceFence ||
		resolutionReason != record.ResolutionReason || disposition != state.Disposition ||
		!bytes.Equal(evidenceRootDigest, record.ResolutionEvidenceRoot.Digest[:]) ||
		!bytes.Equal(canonicalDigest, wantDigest[:]) || !cleanupResolutionProofMatchesRecord(state, record) {
		return cleanupResolutionProofView{}, newError(ErrorIntegrityConflict)
	}
	return cleanupResolutionProofView{State: state, CanonicalDigest: wantDigest}, nil
}
