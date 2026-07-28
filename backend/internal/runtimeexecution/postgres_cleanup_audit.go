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
	AuditFactID                   string                    `json:"audit_fact_id"`
	SchemaVersion                 SchemaVersion             `json:"schema_version"`
	IntegrityVersion              uint16                    `json:"integrity_version"`
	OwningModule                  string                    `json:"owning_module"`
	DecisionID                    string                    `json:"decision_id"`
	OperationID                   string                    `json:"operation_id"`
	OperationDigest               string                    `json:"operation_digest"`
	Action                        uint8                     `json:"action"`
	Result                        uint8                     `json:"result"`
	DebtID                        string                    `json:"debt_id"`
	DebtRevision                  uint64                    `json:"debt_revision"`
	BeforeDebtRevision            uint64                    `json:"before_debt_revision"`
	AfterDebtRevision             uint64                    `json:"after_debt_revision"`
	BeforeDebtStatus              cleanupDebtStatus         `json:"before_debt_status"`
	AfterDebtStatus               cleanupDebtStatus         `json:"after_debt_status"`
	BeforeUnresolved              bool                      `json:"before_unresolved"`
	AfterUnresolved               bool                      `json:"after_unresolved"`
	PersonalWorkspaceID           string                    `json:"personal_workspace_id"`
	TaskID                        string                    `json:"task_id"`
	PhaseRunID                    string                    `json:"phase_run_id"`
	RuntimeRunID                  string                    `json:"runtime_run_id"`
	ResourceClass                 cleanupResourceClass      `json:"resource_class"`
	ResourceIdentityDigest        string                    `json:"resource_identity_digest"`
	ResourceGeneration            uint64                    `json:"resource_generation"`
	ResourceFence                 uint64                    `json:"resource_fence"`
	BeforeResourceGeneration      uint64                    `json:"before_resource_generation"`
	AfterResourceGeneration       uint64                    `json:"after_resource_generation"`
	BeforeResourceFence           uint64                    `json:"before_resource_fence"`
	AfterResourceFence            uint64                    `json:"after_resource_fence"`
	CleanupIntent                 cleanupIntent             `json:"cleanup_intent"`
	CauseDecisionID               string                    `json:"cause_decision_id"`
	CauseOperationID              string                    `json:"cause_operation_id"`
	ResolutionClass               cleanupResolutionClass    `json:"resolution_class"`
	ResolutionReason              cleanupResolutionReason   `json:"resolution_reason"`
	ResolutionAuthorityKind       AuthorityKind             `json:"resolution_authority_kind"`
	ResolutionAuthorityID         string                    `json:"resolution_authority_id"`
	ResolutionAuthorityGeneration AuthorizationGeneration   `json:"resolution_authority_generation"`
	BeforeRuntimeRevision         RuntimeRevision           `json:"before_runtime_revision"`
	AfterRuntimeRevision          RuntimeRevision           `json:"after_runtime_revision"`
	BeforeOperationGeneration     OperationGeneration       `json:"before_operation_generation"`
	AfterOperationGeneration      OperationGeneration       `json:"after_operation_generation"`
	BeforeRuntimeFence            RuntimeFence              `json:"before_runtime_fence"`
	AfterRuntimeFence             RuntimeFence              `json:"after_runtime_fence"`
	PolicyEpoch                   uint64                    `json:"policy_epoch"`
	AuthorizationEpoch            AuthorizationGeneration   `json:"authorization_epoch"`
	RecoveryGeneration            uint64                    `json:"recovery_generation"`
	BeforeSafetyEpoch             ReleaseSafetyEpoch        `json:"before_safety_epoch"`
	AfterSafetyEpoch              ReleaseSafetyEpoch        `json:"after_safety_epoch"`
	EvidenceSchemaVersion         SchemaVersion             `json:"evidence_schema_version"`
	EvidenceRootID                string                    `json:"evidence_root_id"`
	EvidenceRootDigest            string                    `json:"evidence_root_digest"`
	ResolutionProofID             string                    `json:"resolution_proof_id"`
	ResolutionProofDigest         string                    `json:"resolution_proof_digest"`
	BlockerClasses                cleanupBlockerClass       `json:"blocker_classes"`
	BlockerDigest                 string                    `json:"blocker_digest"`
	Uncontained                   bool                      `json:"uncontained"`
	OccurredAt                    string                    `json:"occurred_at"`
	RecordedAt                    string                    `json:"recorded_at"`
	SourceClockID                 string                    `json:"source_clock_id"`
	ExceptionUntil                string                    `json:"exception_until"`
	IncidentReference             string                    `json:"incident_reference"`
	TicketReference               string                    `json:"ticket_reference"`
	ApprovalReference             string                    `json:"approval_reference"`
	IdempotencyReference          string                    `json:"idempotency_reference"`
	RetryDisposition              RetryDisposition          `json:"retry_disposition"`
	ReconciliationDisposition     ReconciliationDisposition `json:"reconciliation_disposition"`
	SupersedingAuditFactID        string                    `json:"superseding_audit_fact_id"`
	SafeErrorCode                 ErrorCode                 `json:"safe_error_code"`
}

func cleanupResolutionAuditFactID(mutationDigest Digest) string {
	return "cleanup-resolution-audit-" + mutationDigest.String()
}

func newCleanupResolutionAuditState(
	before cleanupDebtRecord,
	after cleanupDebtRecord,
	runtime *runtimeRecord,
	operationDigest Digest,
	proof cleanupResolutionProofView,
) cleanupResolutionAuditState {
	return cleanupResolutionAuditState{
		AuditFactID: after.ResolutionAuditFactID, SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupResolutionAuditIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		OperationID:      after.LastMutationID,
		OperationDigest:  operationDigest.String(),
		Action:           postgresCleanupResolutionAuditAction,
		Result:           postgresCleanupResolutionAuditAccepted,
		DebtID:           after.DebtID, DebtRevision: after.Revision,
		BeforeDebtRevision: before.Revision, AfterDebtRevision: after.Revision,
		BeforeDebtStatus: before.Status, AfterDebtStatus: after.Status,
		BeforeUnresolved: before.Unresolved, AfterUnresolved: after.Unresolved,
		PersonalWorkspaceID: after.PersonalWorkspaceID.String(), TaskID: after.TaskID.String(),
		PhaseRunID: after.PhaseRunID.String(), RuntimeRunID: after.RuntimeRunID.String(),
		ResourceClass: after.ResourceClass, ResourceIdentityDigest: after.ResourceIdentityDigest.String(),
		ResourceGeneration: after.ResourceGeneration, ResourceFence: after.ResourceFence,
		BeforeResourceGeneration: before.ResourceGeneration, AfterResourceGeneration: after.ResourceGeneration,
		BeforeResourceFence: before.ResourceFence, AfterResourceFence: after.ResourceFence,
		CleanupIntent: after.CleanupIntent, CauseDecisionID: after.CauseDecisionID.String(),
		CauseOperationID: after.CauseOperationID.String(), ResolutionClass: after.ResolutionClass,
		ResolutionReason: after.ResolutionReason, ResolutionAuthorityKind: after.ResolutionAuthority.kind,
		ResolutionAuthorityID:         after.ResolutionAuthority.id.String(),
		ResolutionAuthorityGeneration: after.ResolutionAuthority.generation,
		BeforeRuntimeRevision:         runtime.fixture.RuntimeRevision,
		AfterRuntimeRevision:          runtime.fixture.RuntimeRevision,
		BeforeOperationGeneration:     runtime.fixture.OperationGeneration,
		AfterOperationGeneration:      runtime.fixture.OperationGeneration,
		BeforeRuntimeFence:            runtime.fixture.RuntimeFence,
		AfterRuntimeFence:             runtime.fixture.RuntimeFence,
		AuthorizationEpoch:            after.ResolutionAuthority.generation,
		BeforeSafetyEpoch:             runtime.fixture.SafetyEpoch,
		AfterSafetyEpoch:              runtime.fixture.SafetyEpoch,
		EvidenceSchemaVersion:         after.ResolutionEvidenceRoot.SchemaVersion,
		EvidenceRootID:                after.ResolutionEvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:            after.ResolutionEvidenceRoot.Digest.String(),
		ResolutionProofID:             proof.State.ProofID,
		ResolutionProofDigest:         proof.CanonicalDigest.String(),
		BlockerClasses:                after.Blockers.Classes,
		BlockerDigest:                 formatOptionalCleanupDigest(after.Blockers.Digest),
		Uncontained:                   after.Uncontained,
		OccurredAt:                    formatCleanupTime(after.ResolvedAt),
		RecordedAt:                    formatCleanupTime(after.ResolvedAt),
		SourceClockID:                 postgresMandatoryAuditSourceClock,
		ExceptionUntil:                formatOptionalCleanupTime(after.ResolutionExpiresAt),
		IdempotencyReference:          after.LastMutationID,
		RetryDisposition:              RetryNever,
		ReconciliationDisposition:     ReconciliationNotRequired,
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
		state.DecisionID != "" || !validOpaqueID(state.OperationID) || !validDigestText(state.OperationDigest) ||
		state.AuditFactID != cleanupResolutionAuditFactID(parseCleanupAuditDigestOrZero(state.OperationDigest)) ||
		state.Action != postgresCleanupResolutionAuditAction || state.Result != postgresCleanupResolutionAuditAccepted ||
		!validOpaqueID(state.DebtID) || state.DebtRevision == 0 || state.BeforeDebtRevision == 0 ||
		state.AfterDebtRevision != state.DebtRevision || state.BeforeDebtRevision+1 != state.AfterDebtRevision ||
		state.BeforeDebtStatus < cleanupDebtOpen || state.BeforeDebtStatus >= cleanupDebtResolved ||
		state.AfterDebtStatus != cleanupDebtResolved || !state.BeforeUnresolved || state.AfterUnresolved ||
		!validOpaqueID(state.PersonalWorkspaceID) || !validOpaqueID(state.TaskID) ||
		!validOpaqueID(state.PhaseRunID) || !validOpaqueID(state.RuntimeRunID) ||
		state.ResourceClass < cleanupResourceProcess || state.ResourceClass > cleanupResourceReset ||
		!validDigestText(state.ResourceIdentityDigest) || state.ResourceGeneration == 0 || state.ResourceFence == 0 ||
		state.BeforeResourceGeneration != state.ResourceGeneration ||
		state.AfterResourceGeneration != state.ResourceGeneration ||
		state.BeforeResourceFence != state.ResourceFence || state.AfterResourceFence != state.ResourceFence ||
		state.CleanupIntent < cleanupIntentReclaim || state.CleanupIntent > cleanupIntentReset ||
		!validOpaqueID(state.CauseDecisionID) || !validOpaqueID(state.CauseOperationID) ||
		state.ResolutionClass < cleanupResolutionReclaimed ||
		state.ResolutionClass > cleanupResolutionAcceptedException ||
		state.ResolutionReason < cleanupResolutionCleanupProven ||
		state.ResolutionReason > cleanupResolutionAdministratorException ||
		!validAuthority(RuntimeAuthority{id: AuthorityID{value: state.ResolutionAuthorityID},
			generation: state.ResolutionAuthorityGeneration, kind: state.ResolutionAuthorityKind}) ||
		state.BeforeRuntimeRevision == 0 || state.AfterRuntimeRevision != state.BeforeRuntimeRevision ||
		state.BeforeOperationGeneration == 0 ||
		state.AfterOperationGeneration != state.BeforeOperationGeneration ||
		state.BeforeRuntimeFence == 0 || state.AfterRuntimeFence != state.BeforeRuntimeFence ||
		state.AuthorizationEpoch != state.ResolutionAuthorityGeneration ||
		state.BeforeSafetyEpoch == 0 || state.AfterSafetyEpoch != state.BeforeSafetyEpoch ||
		state.EvidenceSchemaVersion != SchemaV1 || !validOpaqueID(state.EvidenceRootID) ||
		!validDigestText(state.EvidenceRootDigest) || !validOpaqueID(state.ResolutionProofID) ||
		!validDigestText(state.ResolutionProofDigest) || state.SourceClockID != postgresMandatoryAuditSourceClock ||
		!validOptionalCleanupAuditReference(state.IncidentReference) ||
		!validOptionalCleanupAuditReference(state.TicketReference) ||
		!validOptionalCleanupAuditReference(state.ApprovalReference) ||
		state.IdempotencyReference != state.OperationID || state.RetryDisposition != RetryNever ||
		state.ReconciliationDisposition != ReconciliationNotRequired ||
		state.SupersedingAuditFactID != "" || state.SafeErrorCode != 0 ||
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
	if state.ResolutionClass == cleanupResolutionAcceptedException {
		return state.ResolutionAuthorityKind == AuthorityAdministrator && state.ApprovalReference != ""
	}
	return state.ApprovalReference == ""
}

func validOptionalCleanupAuditReference(value string) bool {
	return value == "" || validOpaqueID(value)
}

func parseCleanupAuditDigestOrZero(value string) Digest {
	digest, ok := parseCleanupDigest(value, true)
	if !ok {
		return Digest{}
	}
	return digest
}

func cleanupResolutionAuditMatchesRecord(
	state cleanupResolutionAuditState,
	before cleanupDebtRecord,
	after cleanupDebtRecord,
	runtime *runtimeRecord,
	operationDigest Digest,
	proof cleanupResolutionProofView,
) bool {
	return runtime != nil && state == newCleanupResolutionAuditState(before, after, runtime, operationDigest, proof)
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
	before cleanupDebtRecord,
	after cleanupDebtRecord,
	runtime *runtimeRecord,
	operationDigest Digest,
	proof cleanupResolutionProofView,
) error {
	state := newCleanupResolutionAuditState(before, after, runtime, operationDigest, proof)
	encoded, digest, err := encodeCleanupResolutionAudit(state)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, debt_id, runtime_run_id, operation_id, operation_digest, resource_identity_digest,
		resource_generation, resource_fence, resolution_class, resolution_reason,
		authority_kind, authority_id, authority_generation, before_debt_revision, after_debt_revision,
		before_debt_status, after_debt_status, evidence_root_id, evidence_root_digest,
		resolution_proof_id, resolution_proof_digest, occurred_at, recorded_at,
		source_clock_id, canonical_digest, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
		authority.table("runtime_execution_cleanup_resolution_audit")), state.AuditFactID,
		after.DebtID, after.RuntimeRunID.String(), state.OperationID, operationDigest[:],
		after.ResourceIdentityDigest[:], after.ResourceGeneration, after.ResourceFence,
		after.ResolutionClass, after.ResolutionReason, after.ResolutionAuthority.kind,
		after.ResolutionAuthority.id.String(), after.ResolutionAuthority.generation,
		before.Revision, after.Revision, before.Status, after.Status,
		after.ResolutionEvidenceRoot.EvidenceRootID.String(), after.ResolutionEvidenceRoot.Digest[:],
		proof.State.ProofID, proof.CanonicalDigest[:], after.ResolvedAt, after.ResolvedAt,
		state.SourceClockID, digest[:], encoded)
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
	before, operationDigest, err := authority.loadCleanupResolutionPredecessor(ctx, tx, record)
	if err != nil {
		return err
	}
	return authority.verifyPendingCleanupResolutionAuthority(
		ctx, tx, before, record, runtime, operationDigest,
	)
}

func (authority *PostgresAuthority) loadCleanupResolutionPredecessor(
	ctx context.Context,
	tx *sql.Tx,
	after cleanupDebtRecord,
) (cleanupDebtRecord, Digest, error) {
	var encodedDigest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT mutation_digest FROM %s
		WHERE debt_id=$1 AND mutation_id=$2 AND mutation_kind=$3 AND result_revision=$4`,
		authority.table("runtime_execution_cleanup_mutations")), after.DebtID, after.LastMutationID,
		cleanupMutationResolve, after.Revision).Scan(&encodedDigest)
	if err != nil {
		if err == sql.ErrNoRows {
			return cleanupDebtRecord{}, Digest{}, newError(ErrorIntegrityConflict)
		}
		return cleanupDebtRecord{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	var operationDigest Digest
	if len(encodedDigest) != len(operationDigest) {
		return cleanupDebtRecord{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	copy(operationDigest[:], encodedDigest)

	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT result_state FROM %s
		WHERE debt_id=$1 AND result_revision=$2`, authority.table("runtime_execution_cleanup_mutations")),
		after.DebtID, after.Revision-1)
	if err != nil {
		return cleanupDebtRecord{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return cleanupDebtRecord{}, Digest{}, normalizeRuntimePersistenceFailure(err)
		}
		return cleanupDebtRecord{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	var encodedState []byte
	if err := rows.Scan(&encodedState); err != nil {
		return cleanupDebtRecord{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows.Next() {
		return cleanupDebtRecord{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	if err := rows.Err(); err != nil {
		return cleanupDebtRecord{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	before, _, err := decodeCleanupDebtRecord(encodedState)
	if err != nil || before.DebtID != after.DebtID || before.Revision+1 != after.Revision ||
		before.Status == cleanupDebtResolved || !before.Unresolved || before.LastMutationID == after.LastMutationID {
		return cleanupDebtRecord{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	return before, operationDigest, nil
}

func (authority *PostgresAuthority) verifyPendingCleanupResolutionAuthority(
	ctx context.Context,
	tx *sql.Tx,
	before cleanupDebtRecord,
	after cleanupDebtRecord,
	runtime *runtimeRecord,
	operationDigest Digest,
) error {
	if err := authority.verifyRetainedCleanupResolutionEvidence(
		ctx, tx, runtime, after.ResolutionEvidenceRoot,
	); err != nil {
		return err
	}
	proof, err := authority.verifyRetainedCleanupResolutionProof(ctx, tx, after)
	if err != nil {
		return err
	}
	var debtID, runtimeRunID, operationID, authorityID, evidenceRootID, proofID, sourceClockID string
	var retainedOperationDigest, resourceIdentityDigest, evidenceRootDigest, proofDigest []byte
	var canonicalDigest, encoded []byte
	var resourceGeneration, resourceFence, beforeDebtRevision, afterDebtRevision uint64
	var beforeDebtStatus, afterDebtStatus cleanupDebtStatus
	var resolutionClass cleanupResolutionClass
	var resolutionReason cleanupResolutionReason
	var authorityKind AuthorityKind
	var authorityGeneration AuthorizationGeneration
	var occurredAt, recordedAt time.Time
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT debt_id, runtime_run_id, operation_id, operation_digest,
		resource_identity_digest,
		resource_generation, resource_fence, resolution_class, resolution_reason,
		authority_kind, authority_id, authority_generation, before_debt_revision, after_debt_revision,
		before_debt_status, after_debt_status, evidence_root_id, evidence_root_digest,
		resolution_proof_id, resolution_proof_digest, occurred_at, recorded_at,
		source_clock_id, canonical_digest, audit_state
		FROM %s WHERE audit_fact_id=$1`, authority.table("runtime_execution_cleanup_resolution_audit")),
		after.ResolutionAuditFactID).Scan(&debtID, &runtimeRunID, &operationID, &retainedOperationDigest,
		&resourceIdentityDigest,
		&resourceGeneration, &resourceFence, &resolutionClass, &resolutionReason,
		&authorityKind, &authorityID, &authorityGeneration, &beforeDebtRevision, &afterDebtRevision,
		&beforeDebtStatus, &afterDebtStatus, &evidenceRootID, &evidenceRootDigest,
		&proofID, &proofDigest, &occurredAt, &recordedAt, &sourceClockID, &canonicalDigest, &encoded)
	if err != nil {
		if err == sql.ErrNoRows {
			return newError(ErrorIntegrityConflict)
		}
		return normalizeRuntimePersistenceFailure(err)
	}
	state, wantDigest, err := decodeCleanupResolutionAudit(encoded)
	if err != nil || debtID != after.DebtID || runtimeRunID != after.RuntimeRunID.String() ||
		operationID != after.LastMutationID || !bytes.Equal(retainedOperationDigest, operationDigest[:]) ||
		!bytes.Equal(resourceIdentityDigest, after.ResourceIdentityDigest[:]) ||
		resourceGeneration != after.ResourceGeneration || resourceFence != after.ResourceFence ||
		resolutionClass != after.ResolutionClass || resolutionReason != after.ResolutionReason ||
		authorityKind != after.ResolutionAuthority.kind || authorityID != after.ResolutionAuthority.id.String() ||
		authorityGeneration != after.ResolutionAuthority.generation || beforeDebtRevision != before.Revision ||
		afterDebtRevision != after.Revision || beforeDebtStatus != before.Status || afterDebtStatus != after.Status ||
		evidenceRootID != after.ResolutionEvidenceRoot.EvidenceRootID.String() ||
		!bytes.Equal(evidenceRootDigest, after.ResolutionEvidenceRoot.Digest[:]) ||
		proofID != proof.State.ProofID || !bytes.Equal(proofDigest, proof.CanonicalDigest[:]) ||
		!occurredAt.Equal(after.ResolvedAt) || !recordedAt.Equal(after.ResolvedAt) ||
		sourceClockID != postgresMandatoryAuditSourceClock || !bytes.Equal(canonicalDigest, wantDigest[:]) ||
		!cleanupResolutionAuditMatchesRecord(state, before, after, runtime, operationDigest, proof) {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}
