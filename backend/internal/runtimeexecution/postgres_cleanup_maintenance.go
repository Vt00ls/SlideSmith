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
	postgresCleanupMutationAuditSchema           = "slidesmith.runtime-execution.cleanup-mutation-audit/v1"
	postgresCleanupMutationAuditDomain           = postgresCleanupMutationAuditSchema + "\n"
	postgresCleanupMutationAuditIntegrityVersion = 1
	postgresCleanupMutationAuditAction           = 1
	postgresCleanupMutationAuditAccepted         = 1
)

type cleanupAuditKind uint8

const (
	cleanupAuditCreate cleanupAuditKind = iota + 1
	cleanupAuditAttempt
	cleanupAuditExpire
	cleanupAuditReopen
)

// cleanupMutationAuditState is the mandatory content-free audit fact bound to
// every protected Cleanup Debt mutation (create/attempt/expire/reopen). It
// never contains a path, locator, credential, content, or free-form error.
type cleanupMutationAuditState struct {
	AuditFactID            string                  `json:"audit_fact_id"`
	SchemaVersion          SchemaVersion           `json:"schema_version"`
	IntegrityVersion       uint16                  `json:"integrity_version"`
	OwningModule           string                  `json:"owning_module"`
	OperationID            string                  `json:"operation_id"`
	OperationDigest        string                  `json:"operation_digest"`
	SeamDigest             string                  `json:"seam_digest,omitempty"`
	Action                 uint8                   `json:"action"`
	Result                 uint8                   `json:"result"`
	MutationKind           cleanupAuditKind        `json:"mutation_kind"`
	Reason                 uint8                   `json:"reason"`
	DebtID                 string                  `json:"debt_id"`
	DebtRevision           uint64                  `json:"debt_revision"`
	BeforeDebtRevision     uint64                  `json:"before_debt_revision"`
	AfterDebtRevision      uint64                  `json:"after_debt_revision"`
	BeforeDebtStatus       cleanupDebtStatus       `json:"before_debt_status"`
	AfterDebtStatus        cleanupDebtStatus       `json:"after_debt_status"`
	BeforeUnresolved       bool                    `json:"before_unresolved"`
	AfterUnresolved        bool                    `json:"after_unresolved"`
	PersonalWorkspaceID    string                  `json:"personal_workspace_id"`
	TaskID                 string                  `json:"task_id"`
	PhaseRunID             string                  `json:"phase_run_id"`
	RuntimeRunID           string                  `json:"runtime_run_id"`
	ResourceClass          cleanupResourceClass    `json:"resource_class"`
	ResourceIdentityDigest string                  `json:"resource_identity_digest"`
	ResourceGeneration     uint64                  `json:"resource_generation"`
	ResourceFence          uint64                  `json:"resource_fence"`
	CleanupIntent          cleanupIntent           `json:"cleanup_intent"`
	CauseDecisionID        string                  `json:"cause_decision_id"`
	CauseOperationID       string                  `json:"cause_operation_id"`
	RetryDisposition       cleanupRetryDisposition `json:"retry_disposition"`
	FailureCategory        cleanupFailureCategory  `json:"failure_category"`
	ClaimGeneration        uint64                  `json:"claim_generation"`
	ClaimFence             uint64                  `json:"claim_fence"`
	BlockerClasses         cleanupBlockerClass     `json:"blocker_classes"`
	BlockerDigest          string                  `json:"blocker_digest"`
	Uncontained            bool                    `json:"uncontained"`
	EstimateState          cleanupEstimateState    `json:"estimate_state"`
	EstimatedBytes         uint64                  `json:"estimated_bytes"`
	EstimatedInodes        uint64                  `json:"estimated_inodes"`
	ResolutionClass        cleanupResolutionClass  `json:"resolution_class"`
	ResolutionReason       cleanupResolutionReason `json:"resolution_reason"`
	ExceptionUntil         string                  `json:"exception_until"`
	OccurredAt             string                  `json:"occurred_at"`
	RecordedAt             string                  `json:"recorded_at"`
	SourceClockID          string                  `json:"source_clock_id"`
	IdempotencyReference   string                  `json:"idempotency_reference"`
	SafeErrorCode          ErrorCode               `json:"safe_error_code"`
}

func cleanupMutationAuditFactID(mutationDigest Digest) string {
	return "cleanup-mutation-audit-" + mutationDigest.String()
}

func newCleanupMutationAuditState(
	kind cleanupAuditKind,
	reason uint8,
	seamDigest Digest,
	before cleanupDebtRecord,
	after cleanupDebtRecord,
	operationDigest Digest,
	occurredAt time.Time,
) cleanupMutationAuditState {
	state := cleanupMutationAuditState{
		AuditFactID: cleanupMutationAuditFactID(operationDigest), SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupMutationAuditIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		OperationID:      after.LastMutationID, OperationDigest: operationDigest.String(),
		Action: postgresCleanupMutationAuditAction, Result: postgresCleanupMutationAuditAccepted,
		MutationKind: kind, Reason: reason,
		DebtID: after.DebtID, DebtRevision: after.Revision,
		BeforeDebtRevision: before.Revision, AfterDebtRevision: after.Revision,
		BeforeDebtStatus: before.Status, AfterDebtStatus: after.Status,
		BeforeUnresolved: before.Unresolved, AfterUnresolved: after.Unresolved,
		PersonalWorkspaceID: after.PersonalWorkspaceID.String(), TaskID: after.TaskID.String(),
		PhaseRunID: after.PhaseRunID.String(), RuntimeRunID: after.RuntimeRunID.String(),
		ResourceClass: after.ResourceClass, ResourceIdentityDigest: after.ResourceIdentityDigest.String(),
		ResourceGeneration: after.ResourceGeneration, ResourceFence: after.ResourceFence,
		CleanupIntent: after.CleanupIntent, CauseDecisionID: after.CauseDecisionID.String(),
		CauseOperationID: after.CauseOperationID.String(), RetryDisposition: after.RetryDisposition,
		FailureCategory: after.LastErrorCategory, ClaimGeneration: after.ClaimGeneration,
		ClaimFence: after.ClaimFence, BlockerClasses: after.Blockers.Classes,
		BlockerDigest: formatOptionalCleanupDigest(after.Blockers.Digest), Uncontained: after.Uncontained,
		EstimateState: after.Estimation.State, EstimatedBytes: after.Estimation.Bytes,
		EstimatedInodes: after.Estimation.Inodes,
		OccurredAt:      formatCleanupTime(occurredAt),
		RecordedAt:      formatCleanupTime(occurredAt),
		SourceClockID:    postgresMandatoryAuditSourceClock,
		IdempotencyReference: after.LastMutationID,
	}
	if kind == cleanupAuditExpire || kind == cleanupAuditReopen {
		state.ResolutionClass = before.ResolutionClass
		state.ResolutionReason = before.ResolutionReason
		state.ExceptionUntil = formatOptionalCleanupTime(before.ResolutionExpiresAt)
	} else {
		state.ResolutionClass = after.ResolutionClass
		state.ResolutionReason = after.ResolutionReason
		state.ExceptionUntil = formatOptionalCleanupTime(after.ResolutionExpiresAt)
	}
	if seamDigest != (Digest{}) {
		state.SeamDigest = seamDigest.String()
	}
	return state
}

func encodeCleanupMutationAudit(state cleanupMutationAuditState) ([]byte, Digest, error) {
	if !validCleanupMutationAuditState(state) {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return encoded, digestBytes(append([]byte(postgresCleanupMutationAuditDomain), encoded...)), nil
}

func decodeCleanupMutationAudit(encoded []byte) (cleanupMutationAuditState, Digest, error) {
	var state cleanupMutationAuditState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil ||
		!validCleanupMutationAuditState(state) {
		return cleanupMutationAuditState{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		return cleanupMutationAuditState{}, Digest{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return state, digestBytes(append([]byte(postgresCleanupMutationAuditDomain), canonical...)), nil
}

func validCleanupMutationAuditState(state cleanupMutationAuditState) bool {
	if !validOpaqueID(state.AuditFactID) || state.SchemaVersion != SchemaV1 ||
		state.IntegrityVersion != postgresCleanupMutationAuditIntegrityVersion ||
		state.OwningModule != postgresCleanupOwnerModule || !validOpaqueID(state.OperationID) ||
		!validDigestText(state.OperationDigest) ||
		state.AuditFactID != cleanupMutationAuditFactID(parseCleanupAuditDigestOrZero(state.OperationDigest)) ||
		(state.SeamDigest != "" && !validDigestText(state.SeamDigest)) ||
		state.Action != postgresCleanupMutationAuditAction || state.Result != postgresCleanupMutationAuditAccepted ||
		state.MutationKind < cleanupAuditCreate || state.MutationKind > cleanupAuditReopen ||
		!validOpaqueID(state.DebtID) || state.AfterDebtRevision == 0 ||
		state.BeforeDebtRevision+1 != state.AfterDebtRevision ||
		!validOpaqueID(state.PersonalWorkspaceID) || !validOpaqueID(state.TaskID) ||
		!validOpaqueID(state.PhaseRunID) || !validOpaqueID(state.RuntimeRunID) ||
		state.ResourceClass < cleanupResourceProcess || state.ResourceClass > cleanupResourceReset ||
		!validDigestText(state.ResourceIdentityDigest) || state.ResourceGeneration == 0 || state.ResourceFence == 0 ||
		state.CleanupIntent < cleanupIntentReclaim || state.CleanupIntent > cleanupIntentReset ||
		!validOpaqueID(state.CauseDecisionID) || !validOpaqueID(state.CauseOperationID) ||
		state.RetryDisposition < cleanupRetryNone || state.RetryDisposition > cleanupRetryBlocked ||
		state.FailureCategory < cleanupFailureNone || state.FailureCategory > cleanupFailureIntegrityConflict ||
		state.BlockerClasses&^cleanupBlockerMask != 0 || state.EstimateState < cleanupEstimateUnknown ||
		state.EstimateState > cleanupEstimateKnown ||
		(state.ResolutionClass != 0 && (state.ResolutionClass < cleanupResolutionReclaimed ||
			state.ResolutionClass > cleanupResolutionAcceptedException)) ||
		(state.ResolutionReason != 0 && (state.ResolutionReason < cleanupResolutionCleanupProven ||
			state.ResolutionReason > cleanupResolutionAdministratorException)) ||
		state.SourceClockID != postgresMandatoryAuditSourceClock ||
		state.IdempotencyReference != state.OperationID || state.SafeErrorCode != 0 {
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
	if _, err := parseCleanupTime(state.ExceptionUntil, false); err != nil {
		return false
	}
	switch state.MutationKind {
	case cleanupAuditCreate:
		return state.BeforeDebtRevision == 0 && state.BeforeDebtStatus == 0 && !state.BeforeUnresolved &&
			state.AfterDebtRevision == 1 && state.AfterUnresolved &&
			state.RetryDisposition != cleanupRetryNone
	case cleanupAuditAttempt:
		return state.BeforeDebtRevision > 0 && state.BeforeUnresolved &&
			state.BeforeDebtStatus != cleanupDebtResolved && state.AfterUnresolved &&
			state.Reason == uint8(state.FailureCategory) && state.RetryDisposition != cleanupRetryNone
	case cleanupAuditExpire, cleanupAuditReopen:
		return state.BeforeDebtRevision > 0 && state.BeforeUnresolved == false &&
			state.BeforeDebtStatus == cleanupDebtResolved &&
			state.ResolutionClass == cleanupResolutionAcceptedException &&
			state.ResolutionReason == cleanupResolutionAdministratorException &&
			state.AfterUnresolved && state.AfterDebtStatus != cleanupDebtResolved
	default:
		return false
	}
}

func (authority *PostgresAuthority) insertCleanupMutationAudit(
	ctx context.Context,
	tx *sql.Tx,
	kind cleanupAuditKind,
	reason uint8,
	seamDigest Digest,
	before cleanupDebtRecord,
	after cleanupDebtRecord,
	operationDigest Digest,
	occurredAt time.Time,
) error {
	state := newCleanupMutationAuditState(kind, reason, seamDigest, before, after, operationDigest, occurredAt)
	encoded, digest, err := encodeCleanupMutationAudit(state)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	var seam []byte
	if seamDigest != (Digest{}) {
		seam = seamDigest[:]
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, debt_id, runtime_run_id, operation_id, operation_digest, seam_digest,
		mutation_kind, before_debt_revision, after_debt_revision, before_debt_status, after_debt_status,
		resource_identity_digest, resource_generation, resource_fence, retry_disposition,
		failure_category, blocker_classes, uncontained, estimate_state, estimated_bytes,
		estimated_inodes, resolution_class, resolution_reason, occurred_at, recorded_at,
		source_clock_id, canonical_digest, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`,
		authority.table("runtime_execution_cleanup_mutation_audit")), state.AuditFactID,
		after.DebtID, after.RuntimeRunID.String(), state.OperationID, operationDigest[:], seam,
		kind, before.Revision, after.Revision, before.Status, after.Status,
		after.ResourceIdentityDigest[:], after.ResourceGeneration, after.ResourceFence,
		after.RetryDisposition, after.LastErrorCategory, after.Blockers.Classes, after.Uncontained,
		after.Estimation.State, after.Estimation.Bytes, after.Estimation.Inodes,
		after.ResolutionClass, after.ResolutionReason, occurredAt, occurredAt,
		state.SourceClockID, digest[:], encoded)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) expireCleanupDebtException(
	ctx context.Context,
	expiry cleanupDebtExpiry,
) (cleanupDebtRecord, error) {
	return authority.reopenResolvedCleanupDebt(ctx, cleanupDebtExpiryLike{
		MutationID: expiry.MutationID, DebtID: expiry.DebtID,
		PersonalWorkspaceID: expiry.PersonalWorkspaceID, RuntimeRunID: expiry.RuntimeRunID,
		Authority: expiry.Authority, ExpectedRevision: expiry.ExpectedRevision,
		ResourceGeneration: expiry.ResourceGeneration, ResourceFence: expiry.ResourceFence,
		SeamDigest: expiry.SeamDigest, MutationKind: cleanupMutationExpire, AuditKind: cleanupAuditExpire,
		Reason: uint8(expiry.Reason), At: expiry.ExpiredAt,
		ExpectedRuntimeRevision: expiry.ExpectedRuntimeRevision, ExpectedRuntimeFence: expiry.ExpectedRuntimeFence,
	})
}

func (authority *PostgresAuthority) reopenCleanupDebt(
	ctx context.Context,
	reopen cleanupDebtReopen,
) (cleanupDebtRecord, error) {
	return authority.reopenResolvedCleanupDebt(ctx, cleanupDebtExpiryLike{
		MutationID: reopen.MutationID, DebtID: reopen.DebtID,
		PersonalWorkspaceID: reopen.PersonalWorkspaceID, RuntimeRunID: reopen.RuntimeRunID,
		Authority: reopen.Authority, ExpectedRevision: reopen.ExpectedRevision,
		ResourceGeneration: reopen.ResourceGeneration, ResourceFence: reopen.ResourceFence,
		SeamDigest: reopen.SeamDigest, MutationKind: cleanupMutationReopen, AuditKind: cleanupAuditReopen,
		Reason: uint8(reopen.Reason), At: reopen.ReopenedAt,
		ExpectedRuntimeRevision: reopen.ExpectedRuntimeRevision, ExpectedRuntimeFence: reopen.ExpectedRuntimeFence,
	})
}

type cleanupDebtExpiry struct {
	MutationID             string
	DebtID                 string
	PersonalWorkspaceID    PersonalWorkspaceID
	RuntimeRunID           RuntimeRunID
	Authority              RuntimeAuthority
	ExpectedRevision       uint64
	ResourceGeneration     uint64
	ResourceFence          uint64
	ExpiredAt              time.Time
	Reason                 CleanupExceptionExpiryReason
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence   RuntimeFence
	SeamDigest             Digest
}

type cleanupDebtReopen struct {
	MutationID             string
	DebtID                 string
	PersonalWorkspaceID    PersonalWorkspaceID
	RuntimeRunID           RuntimeRunID
	Authority              RuntimeAuthority
	ExpectedRevision       uint64
	ResourceGeneration     uint64
	ResourceFence          uint64
	ReopenedAt             time.Time
	Reason                 CleanupReopenReason
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence   RuntimeFence
	SeamDigest             Digest
}

type cleanupDebtExpiryLike struct {
	MutationID             string
	DebtID                 string
	PersonalWorkspaceID    PersonalWorkspaceID
	RuntimeRunID           RuntimeRunID
	Authority              RuntimeAuthority
	ExpectedRevision       uint64
	ResourceGeneration     uint64
	ResourceFence          uint64
	SeamDigest             Digest
	MutationKind           cleanupMutationKind
	AuditKind              cleanupAuditKind
	Reason                 uint8
	At                     time.Time
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedRuntimeFence   RuntimeFence
}

func (authority *PostgresAuthority) reopenResolvedCleanupDebt(
	ctx context.Context,
	operation cleanupDebtExpiryLike,
) (cleanupDebtRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	operation.At = postgresTimestamp(operation.At)
	mutationDigest, err := cleanupReopenResolutionDigest(operation)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	runtime, err := authority.authorizeCleanupMutation(ctx, tx, cleanupDebtRef{
		DebtID: operation.DebtID, PersonalWorkspaceID: operation.PersonalWorkspaceID,
		RuntimeRunID: operation.RuntimeRunID, Authority: operation.Authority,
	})
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if replay, found, err := authority.loadCleanupMutationReplay(
		ctx, tx, operation.DebtID, operation.MutationID, mutationDigest, operation.SeamDigest,
	); err != nil {
		return cleanupDebtRecord{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}
	if operation.ExpectedRuntimeRevision != 0 &&
		(operation.ExpectedRuntimeRevision != runtime.fixture.RuntimeRevision ||
			operation.ExpectedRuntimeFence != runtime.fixture.RuntimeFence) {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	record, found, err := authority.loadCleanupDebtRow(ctx, tx, operation.DebtID, true)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if !found {
		return cleanupDebtRecord{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(operation.PersonalWorkspaceID, operation.RuntimeRunID,
		operation.Authority, operation.ExpectedRevision, operation.ResourceGeneration,
		operation.ResourceFence, record) || record.Status != cleanupDebtResolved ||
		record.ResolutionClass != cleanupResolutionAcceptedException ||
		operation.At.Before(record.ResolutionExpiresAt) {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	before := record
	reopened, err := reopenCleanupDebtRecord(record, operation.At, operation.At, operation.MutationID)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if err := authority.insertCleanupMutationAudit(ctx, tx, operation.AuditKind, operation.Reason,
		operation.SeamDigest, before, reopened, mutationDigest, operation.At); err != nil {
		return cleanupDebtRecord{}, err
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	return authority.commitCleanupDebtMutation(ctx, tx, operation.MutationKind, operation.MutationID,
		operation.ExpectedRevision, reopened, mutationDigest, operation.SeamDigest)
}

func cleanupReopenResolutionDigest(operation cleanupDebtExpiryLike) (Digest, error) {
	if !validOpaqueID(operation.MutationID) || !validOpaqueID(operation.DebtID) ||
		!validOpaqueID(operation.PersonalWorkspaceID.String()) || !validOpaqueID(operation.RuntimeRunID.String()) ||
		!validAuthority(operation.Authority) || operation.ExpectedRevision == 0 ||
		operation.ResourceGeneration == 0 || operation.ResourceFence == 0 || operation.At.IsZero() ||
		operation.Reason == 0 || (operation.MutationKind != cleanupMutationExpire &&
			operation.MutationKind != cleanupMutationReopen) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	domain := "slidesmith.runtime-execution.cleanup-mutation-reopen/v1"
	if operation.MutationKind == cleanupMutationExpire {
		domain = "slidesmith.runtime-execution.cleanup-mutation-expire/v1"
	}
	payload := canonicalCleanupReopenResolution{
		Schema: domain, OperationID: operation.MutationID, DebtID: operation.DebtID,
		PersonalWorkspaceID: operation.PersonalWorkspaceID.String(),
		RuntimeRunID:        operation.RuntimeRunID.String(), AuthorityKind: operation.Authority.kind,
		AuthorityID: operation.Authority.id.String(),
		AuthorityGeneration: operation.Authority.generation, ExpectedRevision: operation.ExpectedRevision,
		ResourceGeneration: operation.ResourceGeneration, ResourceFence: operation.ResourceFence,
		Reason: operation.Reason, OccurredAt: formatCleanupTime(operation.At),
	}
	return cleanupMutationDigest(payload.Schema, payload)
}

type canonicalCleanupReopenResolution struct {
	Schema              string                  `json:"schema"`
	OperationID         string                  `json:"operation_id"`
	DebtID              string                  `json:"debt_id"`
	PersonalWorkspaceID string                  `json:"personal_workspace_id"`
	RuntimeRunID        string                  `json:"runtime_run_id"`
	AuthorityKind       AuthorityKind           `json:"authority_kind"`
	AuthorityID         string                  `json:"authority_id"`
	AuthorityGeneration AuthorizationGeneration `json:"authority_generation"`
	ExpectedRevision    uint64                  `json:"expected_revision"`
	ResourceGeneration  uint64                  `json:"resource_generation"`
	ResourceFence       uint64                  `json:"resource_fence"`
	Reason              uint8                   `json:"reason"`
	OccurredAt          string                  `json:"occurred_at"`
}

func reopenCleanupDebtRecord(
	debt cleanupDebtRecord,
	reopenedAt time.Time,
	nextRetryAt time.Time,
	mutationID string,
) (cleanupDebtRecord, error) {
	reopened := debt
	reopened.Revision++
	reopened.Status = cleanupDebtOpen
	reopened.Unresolved = true
	reopened.RetryDisposition = cleanupRetryReady
	if reopened.Blockers.Classes != 0 {
		reopened.Status = cleanupDebtBlocked
		reopened.RetryDisposition = cleanupRetryBlocked
	}
	reopened.NextRetryAt = nextRetryAt
	reopened.ResolvedAt = time.Time{}
	reopened.ResolutionClass = 0
	reopened.ResolutionReason = 0
	reopened.ResolutionAuthority = RuntimeAuthority{}
	reopened.ResolutionAuditFactID = ""
	reopened.ResolutionEvidenceRoot = EvidenceRootSnapshot{}
	reopened.ResolutionExpiresAt = time.Time{}
	reopened.ResolutionIncidentReference = ""
	reopened.ResolutionTicketReference = ""
	reopened.ResolutionApprovalReference = ""
	reopened.LastMutationID = mutationID
	reopened = normalizeCleanupDebtRecord(reopened)
	if !validCleanupDebtRecord(reopened) {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	return reopened, nil
}

func resolutionReleasesCapacity(record cleanupDebtRecord) bool {
	return record.Status == cleanupDebtResolved &&
		(record.ResolutionClass == cleanupResolutionReclaimed ||
			record.ResolutionClass == cleanupResolutionAlreadyAbsent)
}

func (authority *PostgresAuthority) validateCleanupMaintenanceRuntime(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	workspaceID PersonalWorkspaceID,
	authorityValue RuntimeAuthority,
	expectedRevision RuntimeRevision,
	expectedFence RuntimeFence,
) (*runtimeRecord, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForRead(ctx, tx, runtimeRunID)
	if err == sql.ErrNoRows || err == nil &&
		(record.fixture.PersonalWorkspaceID != workspaceID || record.fixture.Owner != authorityValue) {
		return nil, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	if expectedRevision != record.fixture.RuntimeRevision || expectedFence != record.fixture.RuntimeFence {
		return nil, newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	return record, nil
}

func (authority *PostgresAuthority) replayCleanupDebtMaintenance(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	debtID string,
	mutationID string,
	mutationDigest Digest,
	seamDigest Digest,
) (RuntimeMaintenanceDecision, bool, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return RuntimeMaintenanceDecision{}, false, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, found, err := authority.loadCleanupMutationReplay(ctx, tx, debtID, mutationID, mutationDigest, seamDigest)
	if err != nil {
		return RuntimeMaintenanceDecision{}, false, err
	}
	if !found {
		return RuntimeMaintenanceDecision{}, false, nil
	}
	runtime, runtimeErr := authority.loadRuntimeForRead(ctx, tx, runtimeRunID)
	if err := tx.Commit(); err != nil {
		return RuntimeMaintenanceDecision{}, false, normalizeRuntimePersistenceFailure(err)
	}
	debt := cleanupDebtDecisionFromRecord(record, resolutionReleasesCapacity(record))
	debt.Replayed = true
	decision := RuntimeMaintenanceDecision{
		OperationID: OperationID{value: mutationID}, CanonicalRequestDigest: seamDigest,
		CleanupDebt: debt,
	}
	if runtimeErr == nil && runtime != nil {
		decision.RuntimeRevision = runtime.fixture.RuntimeRevision
		decision.RuntimeFence = runtime.fixture.RuntimeFence
	}
	return decision, true, nil
}

func (authority *PostgresAuthority) createCleanupObligationMaintenance(
	ctx context.Context,
	command CreateCleanupObligation,
) (RuntimeMaintenanceDecision, error) {
	creation := command.Obligation
	creation.SeamDigest = command.CanonicalRequestDigest
	creation.SeamReason = uint8(command.Reason)
	mutationDigest, err := cleanupCreationDigest(creation)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	// Replay must be decided before any current-state validation so that an
	// exact replay after a lease fence or other runtime revision advance
	// returns the original decision instead of a stale-revision conflict.
	if decision, found, err := authority.replayCleanupDebtMaintenance(ctx, creation.RuntimeRunID,
		creation.DebtID, creation.MutationID, mutationDigest, command.CanonicalRequestDigest); found {
		return decision, err
	}
	runtime, err := authority.validateCleanupMaintenanceRuntime(ctx, creation.RuntimeRunID,
		creation.PersonalWorkspaceID, creation.Authority, command.ExpectedRuntimeRevision, command.ExpectedRuntimeFence)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	created, err := authority.createCleanupDebt(ctx, creation)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: runtime.fixture.RuntimeRevision, RuntimeFence: runtime.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(created, false),
	}, nil
}

func (authority *PostgresAuthority) recordCleanupAttemptMaintenance(
	ctx context.Context,
	command RecordCleanupAttempt,
) (RuntimeMaintenanceDecision, error) {
	attempt := command.Attempt
	attempt.SeamDigest = command.CanonicalRequestDigest
	mutationDigest, err := cleanupAttemptDigest(attempt)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if decision, found, err := authority.replayCleanupDebtMaintenance(ctx, attempt.RuntimeRunID,
		attempt.DebtID, attempt.MutationID, mutationDigest, command.CanonicalRequestDigest); found {
		return decision, err
	}
	runtime, err := authority.validateCleanupMaintenanceRuntime(ctx, attempt.RuntimeRunID,
		attempt.PersonalWorkspaceID, attempt.Authority, command.ExpectedRuntimeRevision, command.ExpectedRuntimeFence)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	recorded, err := authority.recordCleanupDebtAttempt(ctx, attempt)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: runtime.fixture.RuntimeRevision, RuntimeFence: runtime.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(recorded, false),
	}, nil
}

func (authority *PostgresAuthority) resolveCleanupDebtMaintenance(
	ctx context.Context,
	command ResolveCleanupDebt,
) (RuntimeMaintenanceDecision, error) {
	resolution := command.Resolution
	resolution.SeamDigest = command.CanonicalRequestDigest
	mutationDigest, err := cleanupResolutionDigest(resolution)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if decision, found, err := authority.replayCleanupDebtMaintenance(ctx, resolution.RuntimeRunID,
		resolution.DebtID, resolution.MutationID, mutationDigest, command.CanonicalRequestDigest); found {
		return decision, err
	}
	runtime, err := authority.validateCleanupMaintenanceRuntime(ctx, resolution.RuntimeRunID,
		resolution.PersonalWorkspaceID, resolution.Authority, command.ExpectedRuntimeRevision, command.ExpectedRuntimeFence)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	resolved, err := authority.resolveCleanupDebt(ctx, resolution)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: runtime.fixture.RuntimeRevision, RuntimeFence: runtime.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(resolved, resolutionReleasesCapacity(resolved)),
	}, nil
}

func (authority *PostgresAuthority) expireCleanupDebtExceptionMaintenance(
	ctx context.Context,
	command ExpireCleanupDebtException,
) (RuntimeMaintenanceDecision, error) {
	input := command.ExpireCleanupDebtExceptionInput
	expiry := cleanupDebtExpiry{
		MutationID: input.OperationID.String(), DebtID: input.DebtID,
		PersonalWorkspaceID: input.PersonalWorkspaceID, RuntimeRunID: input.RuntimeRunID,
		Authority: input.Authority, ExpectedRevision: input.ExpectedRevision,
		ResourceGeneration: input.ResourceGeneration, ResourceFence: input.ResourceFence,
		ExpiredAt: input.ExpiredAt, Reason: input.Reason,
		ExpectedRuntimeRevision: input.ExpectedRuntimeRevision, ExpectedRuntimeFence: input.ExpectedRuntimeFence,
		SeamDigest: command.CanonicalRequestDigest,
	}
	mutationDigest, err := cleanupReopenResolutionDigest(cleanupDebtExpiryLike{
		MutationID: expiry.MutationID, DebtID: expiry.DebtID,
		PersonalWorkspaceID: expiry.PersonalWorkspaceID, RuntimeRunID: expiry.RuntimeRunID,
		Authority: expiry.Authority, ExpectedRevision: expiry.ExpectedRevision,
		ResourceGeneration: expiry.ResourceGeneration, ResourceFence: expiry.ResourceFence,
		MutationKind: cleanupMutationExpire, Reason: uint8(expiry.Reason), At: expiry.ExpiredAt,
	})
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if decision, found, err := authority.replayCleanupDebtMaintenance(ctx, input.RuntimeRunID,
		input.DebtID, input.OperationID.String(), mutationDigest, command.CanonicalRequestDigest); found {
		return decision, err
	}
	runtime, err := authority.validateCleanupMaintenanceRuntime(ctx, input.RuntimeRunID,
		input.PersonalWorkspaceID, input.Authority, input.ExpectedRuntimeRevision, input.ExpectedRuntimeFence)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	reopened, err := authority.expireCleanupDebtException(ctx, expiry)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	decision := cleanupDebtDecisionFromRecord(reopened, false)
	decision.Expired = true
	decision.Reopened = true
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: runtime.fixture.RuntimeRevision, RuntimeFence: runtime.fixture.RuntimeFence,
		CleanupDebt: decision,
	}, nil
}

func (authority *PostgresAuthority) reopenCleanupDebtMaintenance(
	ctx context.Context,
	command ReopenCleanupDebt,
) (RuntimeMaintenanceDecision, error) {
	input := command.ReopenCleanupDebtInput
	reopen := cleanupDebtReopen{
		MutationID: input.OperationID.String(), DebtID: input.DebtID,
		PersonalWorkspaceID: input.PersonalWorkspaceID, RuntimeRunID: input.RuntimeRunID,
		Authority: input.Authority, ExpectedRevision: input.ExpectedRevision,
		ResourceGeneration: input.ResourceGeneration, ResourceFence: input.ResourceFence,
		ReopenedAt: input.ReopenedAt, Reason: input.Reason,
		ExpectedRuntimeRevision: input.ExpectedRuntimeRevision, ExpectedRuntimeFence: input.ExpectedRuntimeFence,
		SeamDigest: command.CanonicalRequestDigest,
	}
	mutationDigest, err := cleanupReopenResolutionDigest(cleanupDebtExpiryLike{
		MutationID: reopen.MutationID, DebtID: reopen.DebtID,
		PersonalWorkspaceID: reopen.PersonalWorkspaceID, RuntimeRunID: reopen.RuntimeRunID,
		Authority: reopen.Authority, ExpectedRevision: reopen.ExpectedRevision,
		ResourceGeneration: reopen.ResourceGeneration, ResourceFence: reopen.ResourceFence,
		MutationKind: cleanupMutationReopen, Reason: uint8(reopen.Reason), At: reopen.ReopenedAt,
	})
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if decision, found, err := authority.replayCleanupDebtMaintenance(ctx, input.RuntimeRunID,
		input.DebtID, input.OperationID.String(), mutationDigest, command.CanonicalRequestDigest); found {
		return decision, err
	}
	runtime, err := authority.validateCleanupMaintenanceRuntime(ctx, input.RuntimeRunID,
		input.PersonalWorkspaceID, input.Authority, input.ExpectedRuntimeRevision, input.ExpectedRuntimeFence)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	reopened, err := authority.reopenCleanupDebt(ctx, reopen)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	decision := cleanupDebtDecisionFromRecord(reopened, false)
	decision.Reopened = true
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: runtime.fixture.RuntimeRevision, RuntimeFence: runtime.fixture.RuntimeFence,
		CleanupDebt: decision,
	}, nil
}

