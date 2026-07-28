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
	postgresCleanupResolutionAuditSchema           = "slidesmith.runtime-execution.cleanup-resolution-audit/v1"
	postgresCleanupResolutionAuditDomain           = postgresCleanupResolutionAuditSchema + "\n"
	postgresCleanupResolutionAuditIntegrityVersion = 1
	postgresCleanupResolutionAuditAction           = 1
	postgresCleanupResolutionAuditAccepted         = 1
)

type cleanupResolutionAuditState struct {
	AuditFactID                   string                  `json:"audit_fact_id"`
	SchemaVersion                 SchemaVersion           `json:"schema_version"`
	IntegrityVersion              uint16                  `json:"integrity_version"`
	OwningModule                  string                  `json:"owning_module"`
	Action                        uint8                   `json:"action"`
	Result                        uint8                   `json:"result"`
	DebtID                        string                  `json:"debt_id"`
	DebtRevision                  uint64                  `json:"debt_revision"`
	PersonalWorkspaceID           string                  `json:"personal_workspace_id"`
	TaskID                        string                  `json:"task_id"`
	PhaseRunID                    string                  `json:"phase_run_id"`
	RuntimeRunID                  string                  `json:"runtime_run_id"`
	ResourceClass                 cleanupResourceClass    `json:"resource_class"`
	ResourceIdentityDigest        string                  `json:"resource_identity_digest"`
	ResourceGeneration            uint64                  `json:"resource_generation"`
	ResourceFence                 uint64                  `json:"resource_fence"`
	CleanupIntent                 cleanupIntent           `json:"cleanup_intent"`
	CauseDecisionID               string                  `json:"cause_decision_id"`
	CauseOperationID              string                  `json:"cause_operation_id"`
	ResolutionClass               cleanupResolutionClass  `json:"resolution_class"`
	ResolutionReason              cleanupResolutionReason `json:"resolution_reason"`
	ResolutionAuthorityKind       AuthorityKind           `json:"resolution_authority_kind"`
	ResolutionAuthorityID         string                  `json:"resolution_authority_id"`
	ResolutionAuthorityGeneration AuthorizationGeneration `json:"resolution_authority_generation"`
	EvidenceSchemaVersion         SchemaVersion           `json:"evidence_schema_version"`
	EvidenceRootID                string                  `json:"evidence_root_id"`
	EvidenceRootDigest            string                  `json:"evidence_root_digest"`
	BlockerClasses                cleanupBlockerClass     `json:"blocker_classes"`
	BlockerDigest                 string                  `json:"blocker_digest"`
	Uncontained                   bool                    `json:"uncontained"`
	OccurredAt                    string                  `json:"occurred_at"`
	RecordedAt                    string                  `json:"recorded_at"`
	ExceptionUntil                string                  `json:"exception_until"`
	IdempotencyReference          string                  `json:"idempotency_reference"`
}

func cleanupResolutionAuditFactID(mutationDigest Digest) string {
	return "cleanup-resolution-audit-" + mutationDigest.String()
}

func newCleanupResolutionAuditState(record cleanupDebtRecord) cleanupResolutionAuditState {
	return cleanupResolutionAuditState{
		AuditFactID: record.ResolutionAuditFactID, SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupResolutionAuditIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		Action:           postgresCleanupResolutionAuditAction,
		Result:           postgresCleanupResolutionAuditAccepted,
		DebtID:           record.DebtID, DebtRevision: record.Revision,
		PersonalWorkspaceID: record.PersonalWorkspaceID.String(), TaskID: record.TaskID.String(),
		PhaseRunID: record.PhaseRunID.String(), RuntimeRunID: record.RuntimeRunID.String(),
		ResourceClass: record.ResourceClass, ResourceIdentityDigest: record.ResourceIdentityDigest.String(),
		ResourceGeneration: record.ResourceGeneration, ResourceFence: record.ResourceFence,
		CleanupIntent: record.CleanupIntent, CauseDecisionID: record.CauseDecisionID.String(),
		CauseOperationID: record.CauseOperationID.String(), ResolutionClass: record.ResolutionClass,
		ResolutionReason: record.ResolutionReason, ResolutionAuthorityKind: record.ResolutionAuthority.kind,
		ResolutionAuthorityID:         record.ResolutionAuthority.id.String(),
		ResolutionAuthorityGeneration: record.ResolutionAuthority.generation,
		EvidenceSchemaVersion:         record.ResolutionEvidenceRoot.SchemaVersion,
		EvidenceRootID:                record.ResolutionEvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:            record.ResolutionEvidenceRoot.Digest.String(),
		BlockerClasses:                record.Blockers.Classes,
		BlockerDigest:                 formatOptionalCleanupDigest(record.Blockers.Digest),
		Uncontained:                   record.Uncontained,
		OccurredAt:                    formatCleanupTime(record.ResolvedAt),
		RecordedAt:                    formatCleanupTime(record.ResolvedAt),
		ExceptionUntil:                formatOptionalCleanupTime(record.ResolutionExpiresAt),
		IdempotencyReference:          record.LastMutationID,
	}
}

func encodeCleanupResolutionAudit(state cleanupResolutionAuditState) ([]byte, Digest, error) {
	if !validCleanupResolutionAuditState(state) {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return encoded, digestBytes(append([]byte(postgresCleanupResolutionAuditDomain), encoded...)), nil
}

func decodeCleanupResolutionAudit(encoded []byte) (cleanupResolutionAuditState, Digest, error) {
	var state cleanupResolutionAuditState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil ||
		!validCleanupResolutionAuditState(state) {
		return cleanupResolutionAuditState{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		return cleanupResolutionAuditState{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return state, digestBytes(append([]byte(postgresCleanupResolutionAuditDomain), canonical...)), nil
}

func validCleanupResolutionAuditState(state cleanupResolutionAuditState) bool {
	if !validOpaqueID(state.AuditFactID) || state.SchemaVersion != SchemaV1 ||
		state.IntegrityVersion != postgresCleanupResolutionAuditIntegrityVersion ||
		state.OwningModule != postgresCleanupOwnerModule ||
		state.Action != postgresCleanupResolutionAuditAction || state.Result != postgresCleanupResolutionAuditAccepted ||
		!validOpaqueID(state.DebtID) || state.DebtRevision == 0 ||
		!validOpaqueID(state.PersonalWorkspaceID) || !validOpaqueID(state.TaskID) ||
		!validOpaqueID(state.PhaseRunID) || !validOpaqueID(state.RuntimeRunID) ||
		state.ResourceClass < cleanupResourceProcess || state.ResourceClass > cleanupResourceReset ||
		!validDigestText(state.ResourceIdentityDigest) || state.ResourceGeneration == 0 || state.ResourceFence == 0 ||
		state.CleanupIntent < cleanupIntentReclaim || state.CleanupIntent > cleanupIntentReset ||
		!validOpaqueID(state.CauseDecisionID) || !validOpaqueID(state.CauseOperationID) ||
		state.ResolutionClass < cleanupResolutionReclaimed ||
		state.ResolutionClass > cleanupResolutionAcceptedException ||
		state.ResolutionReason < cleanupResolutionCleanupProven ||
		state.ResolutionReason > cleanupResolutionAdministratorException ||
		!validAuthority(RuntimeAuthority{id: AuthorityID{value: state.ResolutionAuthorityID},
			generation: state.ResolutionAuthorityGeneration, kind: state.ResolutionAuthorityKind}) ||
		state.EvidenceSchemaVersion != SchemaV1 || !validOpaqueID(state.EvidenceRootID) ||
		!validDigestText(state.EvidenceRootDigest) || !validOpaqueID(state.IdempotencyReference) ||
		state.BlockerClasses&^cleanupBlockerMask != 0 {
		return false
	}
	blockerDigest, ok := parseCleanupDigest(state.BlockerDigest, false)
	if !ok || !validCleanupBlockers(cleanupBlockerSummary{Classes: state.BlockerClasses, Digest: blockerDigest}) {
		return false
	}
	occurredAt, err := time.Parse(canonicalTimeFormat, state.OccurredAt)
	if err != nil {
		return false
	}
	recordedAt, err := time.Parse(canonicalTimeFormat, state.RecordedAt)
	if err != nil || recordedAt.Before(occurredAt) {
		return false
	}
	exceptionUntil, err := parseCleanupTime(state.ExceptionUntil, false)
	if err != nil {
		return false
	}
	record := cleanupDebtRecord{
		Status: cleanupDebtResolved, ResolutionClass: state.ResolutionClass,
		ResolutionReason: state.ResolutionReason, Uncontained: state.Uncontained,
		Blockers:   cleanupBlockerSummary{Classes: state.BlockerClasses, Digest: blockerDigest},
		ResolvedAt: occurredAt, ResolutionExpiresAt: exceptionUntil,
	}
	if !validCleanupResolutionDisposition(record) {
		return false
	}
	return state.ResolutionClass != cleanupResolutionAcceptedException ||
		state.ResolutionAuthorityKind == AuthorityAdministrator
}

func cleanupResolutionAuditMatchesRecord(state cleanupResolutionAuditState, record cleanupDebtRecord) bool {
	return state.AuditFactID == record.ResolutionAuditFactID && state.DebtID == record.DebtID &&
		state.DebtRevision == record.Revision && state.PersonalWorkspaceID == record.PersonalWorkspaceID.String() &&
		state.TaskID == record.TaskID.String() && state.PhaseRunID == record.PhaseRunID.String() &&
		state.RuntimeRunID == record.RuntimeRunID.String() && state.ResourceClass == record.ResourceClass &&
		state.ResourceIdentityDigest == record.ResourceIdentityDigest.String() &&
		state.ResourceGeneration == record.ResourceGeneration && state.ResourceFence == record.ResourceFence &&
		state.CleanupIntent == record.CleanupIntent && state.CauseDecisionID == record.CauseDecisionID.String() &&
		state.CauseOperationID == record.CauseOperationID.String() &&
		state.ResolutionClass == record.ResolutionClass && state.ResolutionReason == record.ResolutionReason &&
		state.ResolutionAuthorityKind == record.ResolutionAuthority.kind &&
		state.ResolutionAuthorityID == record.ResolutionAuthority.id.String() &&
		state.ResolutionAuthorityGeneration == record.ResolutionAuthority.generation &&
		state.EvidenceSchemaVersion == record.ResolutionEvidenceRoot.SchemaVersion &&
		state.EvidenceRootID == record.ResolutionEvidenceRoot.EvidenceRootID.String() &&
		state.EvidenceRootDigest == record.ResolutionEvidenceRoot.Digest.String() &&
		state.BlockerClasses == record.Blockers.Classes &&
		state.BlockerDigest == formatOptionalCleanupDigest(record.Blockers.Digest) &&
		state.Uncontained == record.Uncontained && state.OccurredAt == formatCleanupTime(record.ResolvedAt) &&
		state.RecordedAt == formatCleanupTime(record.ResolvedAt) &&
		state.ExceptionUntil == formatOptionalCleanupTime(record.ResolutionExpiresAt) &&
		state.IdempotencyReference == record.LastMutationID
}

func (authority *PostgresAuthority) verifyRetainedCleanupResolutionEvidence(
	ctx context.Context,
	tx *sql.Tx,
	runtime *runtimeRecord,
	evidenceRoot EvidenceRootSnapshot,
) error {
	if runtime == nil || runtime.evidenceRoot != evidenceRoot || !knownEvidenceRoot(evidenceRoot) ||
		evidenceRoot.EvidenceRootID == (EvidenceRootID{}) {
		return newError(ErrorIntegrityConflict)
	}
	var retained uint64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s
		WHERE evidence_root_id=$1 AND runtime_run_id=$2 AND schema_version=$3 AND digest=$4`,
		authority.table("runtime_execution_evidence_roots")), evidenceRoot.EvidenceRootID.String(),
		runtime.fixture.RuntimeRunID.String(), evidenceRoot.SchemaVersion, evidenceRoot.Digest[:]).Scan(&retained)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if retained != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) insertCleanupResolutionAudit(
	ctx context.Context,
	tx *sql.Tx,
	record cleanupDebtRecord,
) error {
	state := newCleanupResolutionAuditState(record)
	encoded, digest, err := encodeCleanupResolutionAudit(state)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, debt_id, runtime_run_id, resource_identity_digest,
		resource_generation, resource_fence, resolution_class, resolution_reason,
		authority_kind, authority_id, authority_generation, evidence_root_id,
		evidence_root_digest, occurred_at, recorded_at, canonical_digest, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		authority.table("runtime_execution_cleanup_resolution_audit")), state.AuditFactID,
		record.DebtID, record.RuntimeRunID.String(), record.ResourceIdentityDigest[:],
		record.ResourceGeneration, record.ResourceFence, record.ResolutionClass, record.ResolutionReason,
		record.ResolutionAuthority.kind, record.ResolutionAuthority.id.String(),
		record.ResolutionAuthority.generation, record.ResolutionEvidenceRoot.EvidenceRootID.String(),
		record.ResolutionEvidenceRoot.Digest[:], record.ResolvedAt, record.ResolvedAt, digest[:], encoded)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) verifyCleanupResolutionAuthority(
	ctx context.Context,
	tx *sql.Tx,
	record cleanupDebtRecord,
) error {
	runtime, err := authority.loadRuntimeForRead(ctx, tx, record.RuntimeRunID)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if !cleanupRecordMatchesRuntime(record, runtime) {
		return newError(ErrorIntegrityConflict)
	}
	if err := authority.verifyRetainedCleanupResolutionEvidence(
		ctx, tx, runtime, record.ResolutionEvidenceRoot,
	); err != nil {
		return err
	}
	var debtID, runtimeRunID, authorityID, evidenceRootID string
	var resourceIdentityDigest, evidenceRootDigest, canonicalDigest, encoded []byte
	var resourceGeneration, resourceFence uint64
	var resolutionClass cleanupResolutionClass
	var resolutionReason cleanupResolutionReason
	var authorityKind AuthorityKind
	var authorityGeneration AuthorizationGeneration
	var occurredAt, recordedAt time.Time
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT debt_id, runtime_run_id, resource_identity_digest,
		resource_generation, resource_fence, resolution_class, resolution_reason,
		authority_kind, authority_id, authority_generation, evidence_root_id,
		evidence_root_digest, occurred_at, recorded_at, canonical_digest, audit_state
		FROM %s WHERE audit_fact_id=$1`, authority.table("runtime_execution_cleanup_resolution_audit")),
		record.ResolutionAuditFactID).Scan(&debtID, &runtimeRunID, &resourceIdentityDigest,
		&resourceGeneration, &resourceFence, &resolutionClass, &resolutionReason,
		&authorityKind, &authorityID, &authorityGeneration, &evidenceRootID,
		&evidenceRootDigest, &occurredAt, &recordedAt, &canonicalDigest, &encoded)
	if err != nil {
		if err == sql.ErrNoRows {
			return newError(ErrorIntegrityConflict)
		}
		return normalizeRuntimePersistenceFailure(err)
	}
	state, wantDigest, err := decodeCleanupResolutionAudit(encoded)
	if err != nil || debtID != record.DebtID || runtimeRunID != record.RuntimeRunID.String() ||
		!bytes.Equal(resourceIdentityDigest, record.ResourceIdentityDigest[:]) ||
		resourceGeneration != record.ResourceGeneration || resourceFence != record.ResourceFence ||
		resolutionClass != record.ResolutionClass || resolutionReason != record.ResolutionReason ||
		authorityKind != record.ResolutionAuthority.kind || authorityID != record.ResolutionAuthority.id.String() ||
		authorityGeneration != record.ResolutionAuthority.generation ||
		evidenceRootID != record.ResolutionEvidenceRoot.EvidenceRootID.String() ||
		!bytes.Equal(evidenceRootDigest, record.ResolutionEvidenceRoot.Digest[:]) ||
		!occurredAt.Equal(record.ResolvedAt) || !recordedAt.Equal(record.ResolvedAt) ||
		!bytes.Equal(canonicalDigest, wantDigest[:]) || !cleanupResolutionAuditMatchesRecord(state, record) {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}
