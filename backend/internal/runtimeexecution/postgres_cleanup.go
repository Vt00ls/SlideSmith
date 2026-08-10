package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type cleanupMutationKind uint8

const (
	cleanupMutationCreate cleanupMutationKind = iota + 1
	cleanupMutationAttempt
	cleanupMutationResolve
	cleanupMutationExpire
	cleanupMutationReopen
)

type cleanupDebtRef struct {
	DebtID              string
	PersonalWorkspaceID PersonalWorkspaceID
	RuntimeRunID        RuntimeRunID
	Authority           RuntimeAuthority
}

type cleanupDebtCreation struct {
	MutationID             string
	DebtID                 string
	PersonalWorkspaceID    PersonalWorkspaceID
	RuntimeRunID           RuntimeRunID
	Authority              RuntimeAuthority
	ResourceClass          cleanupResourceClass
	ResourceIdentityDigest Digest
	ResourceGeneration     uint64
	ResourceFence          uint64
	Intent                 cleanupIntent
	CauseDecisionID        RuntimeDecisionID
	CauseOperationID       OperationID
	RetentionFactDigest    Digest
	EligibilityFactDigest  Digest
	CreatedAt              time.Time
	EligibleAt             time.Time
	Estimation             cleanupEstimation
	Blockers               cleanupBlockerSummary
	Uncontained            bool
	SeamDigest             Digest
	SeamReason             uint8
}

type cleanupDebtAttempt struct {
	MutationID                 string
	DebtID                     string
	PersonalWorkspaceID        PersonalWorkspaceID
	RuntimeRunID               RuntimeRunID
	Authority                  RuntimeAuthority
	ExpectedRevision           uint64
	ResourceGeneration         uint64
	ResourceFence              uint64
	ClaimGeneration            uint64
	ClaimFence                 uint64
	AttemptedAt                time.Time
	NextRetryAt                time.Time
	RetryDisposition           cleanupRetryDisposition
	FailureCategory            cleanupFailureCategory
	LastErrorDigest            Digest
	LastErrorEvidenceReference string
	Estimation                 cleanupEstimation
	Blockers                   cleanupBlockerSummary
	Uncontained                bool
	SeamDigest                 Digest
}

type cleanupDebtResolution struct {
	MutationID          string
	DebtID              string
	PersonalWorkspaceID PersonalWorkspaceID
	RuntimeRunID        RuntimeRunID
	Authority           RuntimeAuthority
	ExpectedRevision    uint64
	ResourceGeneration  uint64
	ResourceFence       uint64
	ResolvedAt          time.Time
	Class               cleanupResolutionClass
	Reason              cleanupResolutionReason
	EvidenceRoot        EvidenceRootSnapshot
	RemainingBlockers   cleanupBlockerSummary
	Uncontained         bool
	ExceptionUntil      time.Time
	IncidentReference   string
	TicketReference     string
	ApprovalReference   string
	SeamDigest          Digest
}

type canonicalCleanupCreation struct {
	Schema                 string                  `json:"schema"`
	MutationID             string                  `json:"mutation_id"`
	DebtID                 string                  `json:"debt_id"`
	PersonalWorkspaceID    string                  `json:"personal_workspace_id"`
	RuntimeRunID           string                  `json:"runtime_run_id"`
	AuthorityKind          AuthorityKind           `json:"authority_kind"`
	AuthorityID            string                  `json:"authority_id"`
	AuthorityGeneration    AuthorizationGeneration `json:"authority_generation"`
	ResourceClass          cleanupResourceClass    `json:"resource_class"`
	ResourceIdentityDigest string                  `json:"resource_identity_digest"`
	ResourceGeneration     uint64                  `json:"resource_generation"`
	ResourceFence          uint64                  `json:"resource_fence"`
	Intent                 cleanupIntent           `json:"cleanup_intent"`
	CauseDecisionID        string                  `json:"cause_decision_id"`
	CauseOperationID       string                  `json:"cause_operation_id"`
	RetentionFactDigest    string                  `json:"retention_fact_digest"`
	EligibilityFactDigest  string                  `json:"eligibility_fact_digest"`
	CreatedAt              string                  `json:"created_at"`
	EligibleAt             string                  `json:"eligible_at"`
	EstimateState          cleanupEstimateState    `json:"estimate_state"`
	EstimateMethod         cleanupEstimateMethod   `json:"estimate_method"`
	EstimatedBytes         uint64                  `json:"estimated_bytes"`
	EstimatedInodes        uint64                  `json:"estimated_inodes"`
	EstimateObservedAt     string                  `json:"estimate_observed_at"`
	BlockerClasses         cleanupBlockerClass     `json:"blocker_classes"`
	BlockerDigest          string                  `json:"blocker_digest"`
	Uncontained            bool                    `json:"uncontained"`
}

type canonicalCleanupAttempt struct {
	Schema                     string                  `json:"schema"`
	MutationID                 string                  `json:"mutation_id"`
	DebtID                     string                  `json:"debt_id"`
	PersonalWorkspaceID        string                  `json:"personal_workspace_id"`
	RuntimeRunID               string                  `json:"runtime_run_id"`
	AuthorityKind              AuthorityKind           `json:"authority_kind"`
	AuthorityID                string                  `json:"authority_id"`
	AuthorityGeneration        AuthorizationGeneration `json:"authority_generation"`
	ExpectedRevision           uint64                  `json:"expected_revision"`
	ResourceGeneration         uint64                  `json:"resource_generation"`
	ResourceFence              uint64                  `json:"resource_fence"`
	ClaimGeneration            uint64                  `json:"claim_generation"`
	ClaimFence                 uint64                  `json:"claim_fence"`
	AttemptedAt                string                  `json:"attempted_at"`
	NextRetryAt                string                  `json:"next_retry_at"`
	RetryDisposition           cleanupRetryDisposition `json:"retry_disposition"`
	FailureCategory            cleanupFailureCategory  `json:"failure_category"`
	LastErrorDigest            string                  `json:"last_error_digest"`
	LastErrorEvidenceReference string                  `json:"last_error_evidence_reference"`
	EstimateState              cleanupEstimateState    `json:"estimate_state"`
	EstimateMethod             cleanupEstimateMethod   `json:"estimate_method"`
	EstimatedBytes             uint64                  `json:"estimated_bytes"`
	EstimatedInodes            uint64                  `json:"estimated_inodes"`
	EstimateObservedAt         string                  `json:"estimate_observed_at"`
	BlockerClasses             cleanupBlockerClass     `json:"blocker_classes"`
	BlockerDigest              string                  `json:"blocker_digest"`
	Uncontained                bool                    `json:"uncontained"`
}

type canonicalCleanupResolution struct {
	Schema                  string                  `json:"schema"`
	MutationID              string                  `json:"mutation_id"`
	DebtID                  string                  `json:"debt_id"`
	PersonalWorkspaceID     string                  `json:"personal_workspace_id"`
	RuntimeRunID            string                  `json:"runtime_run_id"`
	AuthorityKind           AuthorityKind           `json:"authority_kind"`
	AuthorityID             string                  `json:"authority_id"`
	AuthorityGeneration     AuthorizationGeneration `json:"authority_generation"`
	ExpectedRevision        uint64                  `json:"expected_revision"`
	ResourceGeneration      uint64                  `json:"resource_generation"`
	ResourceFence           uint64                  `json:"resource_fence"`
	ResolvedAt              string                  `json:"resolved_at"`
	ResolutionClass         cleanupResolutionClass  `json:"resolution_class"`
	ResolutionReason        cleanupResolutionReason `json:"resolution_reason"`
	EvidenceSchema          SchemaVersion           `json:"evidence_schema"`
	EvidenceRootID          string                  `json:"evidence_root_id"`
	EvidenceRootDigest      string                  `json:"evidence_root_digest"`
	RemainingBlockerClasses cleanupBlockerClass     `json:"remaining_blocker_classes"`
	RemainingBlockerDigest  string                  `json:"remaining_blocker_digest"`
	Uncontained             bool                    `json:"uncontained"`
	ExceptionUntil          string                  `json:"exception_until"`
	IncidentReference       string                  `json:"incident_reference,omitempty"`
	TicketReference         string                  `json:"ticket_reference,omitempty"`
	ApprovalReference       string                  `json:"approval_reference,omitempty"`
}

func (authority *PostgresAuthority) createCleanupDebt(
	ctx context.Context,
	creation cleanupDebtCreation,
) (cleanupDebtRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	creation.CreatedAt = postgresTimestamp(creation.CreatedAt)
	creation.EligibleAt = postgresTimestamp(creation.EligibleAt)
	creation.Estimation = normalizedCleanupEstimation(creation.Estimation)
	mutationDigest, err := cleanupCreationDigest(creation)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	runtime, err := authority.authorizeCleanupMutation(ctx, tx, cleanupDebtRef{
		DebtID: creation.DebtID, PersonalWorkspaceID: creation.PersonalWorkspaceID,
		RuntimeRunID: creation.RuntimeRunID, Authority: creation.Authority,
	})
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if replay, found, err := authority.loadCleanupMutationReplay(
		ctx, tx, creation.DebtID, creation.MutationID, mutationDigest, creation.SeamDigest,
	); err != nil {
		return cleanupDebtRecord{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}
	if _, found, err := authority.loadCleanupDebtRow(ctx, tx, creation.DebtID, true); err != nil {
		return cleanupDebtRecord{}, err
	} else if found {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	var causeCount uint64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s
		WHERE decision_id=$1 AND runtime_run_id=$2 AND operation_id=$3`, authority.table("runtime_execution_decisions")),
		creation.CauseDecisionID.String(), creation.RuntimeRunID.String(), creation.CauseOperationID.String()).Scan(&causeCount); err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	if causeCount != 1 {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	status := cleanupDebtOpen
	retry := cleanupRetryReady
	if creation.Blockers.Classes != 0 {
		status = cleanupDebtBlocked
		retry = cleanupRetryBlocked
	}
	record := cleanupDebtRecord{
		DebtID: creation.DebtID, Revision: 1, OwnerModule: postgresCleanupOwnerModule,
		PersonalWorkspaceID: runtime.fixture.PersonalWorkspaceID, TaskID: runtime.fixture.TaskID,
		PhaseRunID: runtime.fixture.PhaseRunID, RuntimeRunID: runtime.fixture.RuntimeRunID,
		OwnerAuthority: runtime.fixture.Owner, ResourceClass: creation.ResourceClass,
		ResourceIdentityDigest: creation.ResourceIdentityDigest, ResourceGeneration: creation.ResourceGeneration,
		ResourceFence: creation.ResourceFence, CleanupIntent: creation.Intent,
		CauseDecisionID: creation.CauseDecisionID, CauseOperationID: creation.CauseOperationID,
		RetentionFactDigest: creation.RetentionFactDigest, EligibilityFactDigest: creation.EligibilityFactDigest,
		Status: status, Unresolved: true, Uncontained: creation.Uncontained,
		CreatedAt: creation.CreatedAt, EligibleAt: creation.EligibleAt, RetryDisposition: retry,
		Estimation: creation.Estimation, Blockers: creation.Blockers, LastMutationID: creation.MutationID,
	}
	state, recordDigest, err := encodeCleanupDebtRecord(record)
	if err != nil {
		return cleanupDebtRecord{}, newError(ErrorInvalidRequest)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		debt_id, personal_workspace_id, task_id, phase_run_id, runtime_run_id, owner_module,
		resource_class, resource_identity_digest, resource_generation, resource_fence,
		cleanup_intent, cause_decision_id, cause_operation_id, retention_fact_digest,
		eligibility_fact_digest, debt_revision, status, unresolved, uncontained,
		canonical_digest, debt_state, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		authority.table("runtime_execution_cleanup_obligations")), record.DebtID,
		record.PersonalWorkspaceID.String(), record.TaskID.String(), record.PhaseRunID.String(), record.RuntimeRunID.String(),
		record.OwnerModule, record.ResourceClass, record.ResourceIdentityDigest[:], record.ResourceGeneration,
		record.ResourceFence, record.CleanupIntent, record.CauseDecisionID.String(), record.CauseOperationID.String(),
		record.RetentionFactDigest[:], record.EligibilityFactDigest[:], record.Revision, record.Status,
		record.Unresolved, record.Uncontained, recordDigest[:], state, postgresTimestamp(authority.now())); err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := authority.insertCleanupMutation(ctx, tx, cleanupMutationCreate, creation.MutationID,
		record, mutationDigest, creation.SeamDigest, recordDigest, state); err != nil {
		return cleanupDebtRecord{}, err
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if err := authority.insertCleanupMutationAudit(ctx, tx, cleanupAuditCreate, creation.SeamReason,
		creation.SeamDigest, cleanupDebtRecord{}, record, mutationDigest, creation.CreatedAt); err != nil {
		return cleanupDebtRecord{}, err
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	return record, nil
}

func (authority *PostgresAuthority) recordCleanupDebtAttempt(
	ctx context.Context,
	attempt cleanupDebtAttempt,
) (cleanupDebtRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	attempt.AttemptedAt = postgresTimestamp(attempt.AttemptedAt)
	attempt.NextRetryAt = postgresTimestamp(attempt.NextRetryAt)
	attempt.Estimation = normalizedCleanupEstimation(attempt.Estimation)
	mutationDigest, err := cleanupAttemptDigest(attempt)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := authority.authorizeCleanupMutation(ctx, tx, cleanupDebtRef{
		DebtID: attempt.DebtID, PersonalWorkspaceID: attempt.PersonalWorkspaceID,
		RuntimeRunID: attempt.RuntimeRunID, Authority: attempt.Authority,
	}); err != nil {
		return cleanupDebtRecord{}, err
	}
	if replay, found, err := authority.loadCleanupMutationReplay(
		ctx, tx, attempt.DebtID, attempt.MutationID, mutationDigest, attempt.SeamDigest,
	); err != nil {
		return cleanupDebtRecord{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}
	record, found, err := authority.loadCleanupDebtRow(ctx, tx, attempt.DebtID, true)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if !found {
		return cleanupDebtRecord{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(attempt.PersonalWorkspaceID, attempt.RuntimeRunID,
		attempt.Authority, attempt.ExpectedRevision, attempt.ResourceGeneration, attempt.ResourceFence, record) ||
		record.Status == cleanupDebtResolved || attempt.AttemptedAt.Before(record.CreatedAt) {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	before := record
	record.Revision++
	if record.AttemptCount == 0 {
		record.FirstAttemptAt = attempt.AttemptedAt
	}
	record.LastAttemptAt = attempt.AttemptedAt
	record.NextRetryAt = attempt.NextRetryAt
	record.AttemptCount++
	record.ConsecutiveFailureCount++
	record.ClaimGeneration = attempt.ClaimGeneration
	record.ClaimFence = attempt.ClaimFence
	record.RetryDisposition = attempt.RetryDisposition
	record.LastErrorCategory = attempt.FailureCategory
	record.LastErrorDigest = attempt.LastErrorDigest
	record.LastErrorEvidenceReference = attempt.LastErrorEvidenceReference
	record.Estimation = attempt.Estimation
	record.Blockers = attempt.Blockers
	record.Uncontained = attempt.Uncontained
	record.LastMutationID = attempt.MutationID
	if attempt.Blockers.Classes == 0 {
		record.Status = cleanupDebtRetryScheduled
	} else {
		record.Status = cleanupDebtBlocked
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if err := authority.insertCleanupMutationAudit(ctx, tx, cleanupAuditAttempt, uint8(attempt.FailureCategory),
		attempt.SeamDigest, before, record, mutationDigest, attempt.AttemptedAt); err != nil {
		return cleanupDebtRecord{}, err
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	return authority.commitCleanupDebtMutation(ctx, tx, cleanupMutationAttempt, attempt.MutationID,
		attempt.ExpectedRevision, record, mutationDigest, attempt.SeamDigest)
}

func (authority *PostgresAuthority) resolveCleanupDebt(
	ctx context.Context,
	resolution cleanupDebtResolution,
) (cleanupDebtRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	resolution.ResolvedAt = postgresTimestamp(resolution.ResolvedAt)
	resolution.ExceptionUntil = optionalPostgresTimestamp(resolution.ExceptionUntil)
	mutationDigest, err := cleanupResolutionDigest(resolution)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	runtime, err := authority.authorizeCleanupMutation(ctx, tx, cleanupDebtRef{
		DebtID: resolution.DebtID, PersonalWorkspaceID: resolution.PersonalWorkspaceID,
		RuntimeRunID: resolution.RuntimeRunID, Authority: resolution.Authority,
	})
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if replay, found, err := authority.loadCleanupMutationReplay(
		ctx, tx, resolution.DebtID, resolution.MutationID, mutationDigest, resolution.SeamDigest,
	); err != nil {
		return cleanupDebtRecord{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}
	record, found, err := authority.loadCleanupDebtRow(ctx, tx, resolution.DebtID, true)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if !found {
		return cleanupDebtRecord{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(resolution.PersonalWorkspaceID, resolution.RuntimeRunID,
		resolution.Authority, resolution.ExpectedRevision, resolution.ResourceGeneration,
		resolution.ResourceFence, record) || record.Status == cleanupDebtResolved {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	if err := authority.verifyRetainedCleanupResolutionEvidence(ctx, tx, runtime, resolution.EvidenceRoot); err != nil {
		return cleanupDebtRecord{}, err
	}
	before := record
	record.Revision++
	record.Status = cleanupDebtResolved
	record.Unresolved = false
	record.Uncontained = resolution.Uncontained
	record.NextRetryAt = time.Time{}
	record.RetryDisposition = cleanupRetryNone
	record.Blockers = resolution.RemainingBlockers
	record.ResolvedAt = resolution.ResolvedAt
	record.ResolutionClass = resolution.Class
	record.ResolutionReason = resolution.Reason
	record.ResolutionAuthority = resolution.Authority
	record.ResolutionAuditFactID = cleanupResolutionAuditFactID(mutationDigest)
	record.ResolutionEvidenceRoot = resolution.EvidenceRoot
	record.ResolutionExpiresAt = resolution.ExceptionUntil
	record.ResolutionIncidentReference = resolution.IncidentReference
	record.ResolutionTicketReference = resolution.TicketReference
	record.ResolutionApprovalReference = resolution.ApprovalReference
	record.LastMutationID = resolution.MutationID
	var proof cleanupResolutionProofView
	if record.ResolutionClass != cleanupResolutionAcceptedException {
		proof, err = authority.verifyRetainedCleanupResolutionProof(ctx, tx, record)
		if err != nil {
			return cleanupDebtRecord{}, err
		}
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if err := authority.insertCleanupResolutionAudit(
		ctx, tx, before, record, runtime, mutationDigest, proof,
	); err != nil {
		return cleanupDebtRecord{}, err
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if err := authority.verifyPendingCleanupResolutionAuthority(
		ctx, tx, before, record, runtime, mutationDigest,
	); err != nil {
		return cleanupDebtRecord{}, err
	}
	return authority.commitCleanupDebtMutation(ctx, tx, cleanupMutationResolve, resolution.MutationID,
		resolution.ExpectedRevision, record, mutationDigest, resolution.SeamDigest)
}

func (authority *PostgresAuthority) loadCleanupDebt(
	ctx context.Context,
	ref cleanupDebtRef,
) (cleanupDebtRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return cleanupDebtRecord{}, newError(ErrorDependencyUnavailable)
	}
	if !validCleanupDebtRef(ref) {
		return cleanupDebtRecord{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	runtime, err := authority.loadRuntimeForRead(ctx, tx, ref.RuntimeRunID)
	if err == sql.ErrNoRows || err == nil && !authorized(runtime, ref.PersonalWorkspaceID, ref.Authority) {
		return cleanupDebtRecord{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	record, found, err := authority.loadCleanupDebtRow(ctx, tx, ref.DebtID, false)
	if err != nil {
		return cleanupDebtRecord{}, err
	}
	if !found || !cleanupRecordMatchesRuntime(record, runtime) {
		return cleanupDebtRecord{}, newError(ErrorAuthorizationDenied)
	}
	if err := tx.Commit(); err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	return record, nil
}

func validCleanupDebtRef(ref cleanupDebtRef) bool {
	return validOpaqueID(ref.DebtID) && validOpaqueID(ref.PersonalWorkspaceID.String()) &&
		validOpaqueID(ref.RuntimeRunID.String()) && validAuthority(ref.Authority)
}

func (authority *PostgresAuthority) authorizeCleanupMutation(
	ctx context.Context,
	tx *sql.Tx,
	ref cleanupDebtRef,
) (*runtimeRecord, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, newError(ErrorDependencyUnavailable)
	}
	if !validCleanupDebtRef(ref) {
		return nil, newError(ErrorInvalidRequest)
	}
	runtime, err := authority.loadRuntimeForUpdate(ctx, tx, ref.RuntimeRunID)
	if err == sql.ErrNoRows || err == nil && !authorized(runtime, ref.PersonalWorkspaceID, ref.Authority) {
		return nil, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return nil, normalizeRuntimePersistenceFailure(err)
	}
	return runtime, nil
}

func (authority *PostgresAuthority) loadRuntimeForRead(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
) (*runtimeRecord, error) {
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id, aggregate_state
		FROM %s WHERE runtime_run_id=$1`, authority.table("runtime_execution_runtimes")), runtimeRunID.String())
	return scanPostgresRuntimeRecord(row, runtimeRunID)
}

func cleanupMutationMatchesRecord(
	workspaceID PersonalWorkspaceID,
	runtimeRunID RuntimeRunID,
	caller RuntimeAuthority,
	expectedRevision uint64,
	resourceGeneration uint64,
	resourceFence uint64,
	record cleanupDebtRecord,
) bool {
	return record.PersonalWorkspaceID == workspaceID && record.RuntimeRunID == runtimeRunID &&
		record.OwnerAuthority == caller && record.Revision == expectedRevision &&
		record.ResourceGeneration == resourceGeneration && record.ResourceFence == resourceFence
}

func cleanupRecordMatchesRuntime(record cleanupDebtRecord, runtime *runtimeRecord) bool {
	return runtime != nil && record.PersonalWorkspaceID == runtime.fixture.PersonalWorkspaceID &&
		record.TaskID == runtime.fixture.TaskID && record.PhaseRunID == runtime.fixture.PhaseRunID &&
		record.RuntimeRunID == runtime.fixture.RuntimeRunID && record.OwnerAuthority == runtime.fixture.Owner
}

func (authority *PostgresAuthority) commitCleanupDebtMutation(
	ctx context.Context,
	tx *sql.Tx,
	kind cleanupMutationKind,
	mutationID string,
	expectedRevision uint64,
	record cleanupDebtRecord,
	mutationDigest Digest,
	seamDigest Digest,
) (cleanupDebtRecord, error) {
	state, recordDigest, err := encodeCleanupDebtRecord(record)
	if err != nil {
		return cleanupDebtRecord{}, newError(ErrorInvalidRequest)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET debt_revision=$1, status=$2,
		unresolved=$3, uncontained=$4, canonical_digest=$5, debt_state=$6, updated_at=$7
		WHERE debt_id=$8 AND debt_revision=$9`, authority.table("runtime_execution_cleanup_obligations")),
		record.Revision, record.Status, record.Unresolved, record.Uncontained, recordDigest[:], state,
		postgresTimestamp(authority.now()), record.DebtID, expectedRevision)
	if err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return cleanupDebtRecord{}, newError(ErrorIntegrityConflict)
	}
	if err := authority.insertCleanupMutation(ctx, tx, kind, mutationID, record,
		mutationDigest, seamDigest, recordDigest, state); err != nil {
		return cleanupDebtRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return cleanupDebtRecord{}, normalizeRuntimePersistenceFailure(err)
	}
	return record, nil
}

func (authority *PostgresAuthority) insertCleanupMutation(
	ctx context.Context,
	tx *sql.Tx,
	kind cleanupMutationKind,
	mutationID string,
	record cleanupDebtRecord,
	mutationDigest Digest,
	seamDigest Digest,
	recordDigest Digest,
	state []byte,
) error {
	var seam []byte
	if seamDigest != (Digest{}) {
		seam = seamDigest[:]
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		debt_id, mutation_id, mutation_kind, mutation_digest, seam_digest, result_revision,
		result_digest, result_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, authority.table("runtime_execution_cleanup_mutations")),
		record.DebtID, mutationID, kind, mutationDigest[:], seam, record.Revision,
		recordDigest[:], state, postgresTimestamp(authority.now()))
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) loadCleanupMutationReplay(
	ctx context.Context,
	tx *sql.Tx,
	debtID string,
	mutationID string,
	mutationDigest Digest,
	seamDigest Digest,
) (cleanupDebtRecord, bool, error) {
	var retainedMutationDigest, retainedSeamDigest, retainedResultDigest, retainedState []byte
	var retainedRevision uint64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT mutation_digest, seam_digest, result_revision,
		result_digest, result_state FROM %s WHERE debt_id=$1 AND mutation_id=$2`,
		authority.table("runtime_execution_cleanup_mutations")), debtID, mutationID).Scan(
		&retainedMutationDigest, &retainedSeamDigest, &retainedRevision, &retainedResultDigest, &retainedState)
	if err == sql.ErrNoRows {
		return cleanupDebtRecord{}, false, nil
	}
	if err != nil {
		return cleanupDebtRecord{}, false, normalizeRuntimePersistenceFailure(err)
	}
	if !bytes.Equal(retainedMutationDigest, mutationDigest[:]) {
		return cleanupDebtRecord{}, false, newError(ErrorIntegrityConflict)
	}
	if seamDigest != (Digest{}) {
		if len(retainedSeamDigest) == 0 || !bytes.Equal(retainedSeamDigest, seamDigest[:]) {
			return cleanupDebtRecord{}, false, newError(ErrorIntegrityConflict)
		}
	} else if len(retainedSeamDigest) != 0 {
		return cleanupDebtRecord{}, false, newError(ErrorIntegrityConflict)
	}
	record, recordDigest, err := decodeCleanupDebtRecord(retainedState)
	if err != nil || record.DebtID != debtID || record.LastMutationID != mutationID ||
		record.Revision != retainedRevision || !bytes.Equal(retainedResultDigest, recordDigest[:]) {
		return cleanupDebtRecord{}, false, newError(ErrorIntegrityConflict)
	}
	if record.Status == cleanupDebtResolved {
		if err := authority.verifyCleanupResolutionAuthority(ctx, tx, record); err != nil {
			return cleanupDebtRecord{}, false, err
		}
	}
	return record, true, nil
}

func (authority *PostgresAuthority) loadCleanupDebtRow(
	ctx context.Context,
	tx *sql.Tx,
	debtID string,
	forUpdate bool,
) (cleanupDebtRecord, bool, error) {
	query := fmt.Sprintf(`SELECT personal_workspace_id, task_id, phase_run_id, runtime_run_id,
		owner_module, resource_class, resource_identity_digest, resource_generation,
		resource_fence, cleanup_intent, cause_decision_id, cause_operation_id,
		retention_fact_digest, eligibility_fact_digest, debt_revision, status,
		unresolved, uncontained, canonical_digest, debt_state
		FROM %s WHERE debt_id=$1`, authority.table("runtime_execution_cleanup_obligations"))
	if forUpdate {
		query += " FOR UPDATE"
	}
	var workspaceID, taskID, phaseRunID, runtimeRunID, ownerModule string
	var causeDecisionID, causeOperationID string
	var resourceClass cleanupResourceClass
	var resourceIdentityDigest, retentionFactDigest, eligibilityFactDigest []byte
	var resourceGeneration, resourceFence, revision uint64
	var intent cleanupIntent
	var status cleanupDebtStatus
	var unresolved, uncontained bool
	var canonicalDigest, state []byte
	err := tx.QueryRowContext(ctx, query, debtID).Scan(
		&workspaceID, &taskID, &phaseRunID, &runtimeRunID, &ownerModule,
		&resourceClass, &resourceIdentityDigest, &resourceGeneration, &resourceFence,
		&intent, &causeDecisionID, &causeOperationID, &retentionFactDigest,
		&eligibilityFactDigest, &revision, &status, &unresolved, &uncontained,
		&canonicalDigest, &state)
	if err == sql.ErrNoRows {
		return cleanupDebtRecord{}, false, nil
	}
	if err != nil {
		return cleanupDebtRecord{}, false, normalizeRuntimePersistenceFailure(err)
	}
	record, wantDigest, err := decodeCleanupDebtRecord(state)
	if err != nil || record.DebtID != debtID || record.PersonalWorkspaceID.String() != workspaceID ||
		record.TaskID.String() != taskID || record.PhaseRunID.String() != phaseRunID ||
		record.RuntimeRunID.String() != runtimeRunID || record.OwnerModule != ownerModule ||
		record.ResourceClass != resourceClass || !bytes.Equal(resourceIdentityDigest, record.ResourceIdentityDigest[:]) ||
		record.ResourceGeneration != resourceGeneration || record.ResourceFence != resourceFence ||
		record.CleanupIntent != intent || record.CauseDecisionID.String() != causeDecisionID ||
		record.CauseOperationID.String() != causeOperationID ||
		!bytes.Equal(retentionFactDigest, record.RetentionFactDigest[:]) ||
		!bytes.Equal(eligibilityFactDigest, record.EligibilityFactDigest[:]) || record.Revision != revision ||
		record.Status != status || record.Unresolved != unresolved || record.Uncontained != uncontained ||
		!bytes.Equal(canonicalDigest, wantDigest[:]) {
		return cleanupDebtRecord{}, false, newError(ErrorIntegrityConflict)
	}
	if record.Status == cleanupDebtResolved {
		if err := authority.verifyCleanupResolutionAuthority(ctx, tx, record); err != nil {
			return cleanupDebtRecord{}, false, err
		}
	}
	return record, true, nil
}

func cleanupCreationDigest(creation cleanupDebtCreation) (Digest, error) {
	if !validOpaqueID(creation.MutationID) || !validOpaqueID(creation.DebtID) ||
		!validOpaqueID(creation.PersonalWorkspaceID.String()) || !validOpaqueID(creation.RuntimeRunID.String()) ||
		!validAuthority(creation.Authority) || creation.ResourceClass < cleanupResourceProcess ||
		creation.ResourceClass > cleanupResourceReset || creation.ResourceIdentityDigest == (Digest{}) ||
		creation.ResourceGeneration == 0 || creation.ResourceFence == 0 || creation.Intent < cleanupIntentReclaim ||
		creation.Intent > cleanupIntentReset || !validOpaqueID(creation.CauseDecisionID.String()) ||
		!validOpaqueID(creation.CauseOperationID.String()) || creation.RetentionFactDigest == (Digest{}) ||
		creation.EligibilityFactDigest == (Digest{}) || creation.CreatedAt.IsZero() || creation.EligibleAt.IsZero() ||
		!validCleanupEstimation(creation.Estimation) || !validCleanupBlockers(creation.Blockers) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	payload := canonicalCleanupCreation{
		Schema: "slidesmith.runtime-execution.cleanup-mutation-create/v1", MutationID: creation.MutationID,
		DebtID: creation.DebtID, PersonalWorkspaceID: creation.PersonalWorkspaceID.String(),
		RuntimeRunID: creation.RuntimeRunID.String(), AuthorityKind: creation.Authority.kind,
		AuthorityID: creation.Authority.id.String(), AuthorityGeneration: creation.Authority.generation,
		ResourceClass: creation.ResourceClass, ResourceIdentityDigest: creation.ResourceIdentityDigest.String(),
		ResourceGeneration: creation.ResourceGeneration, ResourceFence: creation.ResourceFence,
		Intent: creation.Intent, CauseDecisionID: creation.CauseDecisionID.String(),
		CauseOperationID: creation.CauseOperationID.String(), RetentionFactDigest: creation.RetentionFactDigest.String(),
		EligibilityFactDigest: creation.EligibilityFactDigest.String(), CreatedAt: formatCleanupTime(creation.CreatedAt),
		EligibleAt: formatCleanupTime(creation.EligibleAt), EstimateState: creation.Estimation.State,
		EstimateMethod: creation.Estimation.Method, EstimatedBytes: creation.Estimation.Bytes,
		EstimatedInodes: creation.Estimation.Inodes, EstimateObservedAt: formatOptionalCleanupTime(creation.Estimation.ObservedAt),
		BlockerClasses: creation.Blockers.Classes, BlockerDigest: formatOptionalCleanupDigest(creation.Blockers.Digest),
		Uncontained: creation.Uncontained,
	}
	return cleanupMutationDigest(payload.Schema, payload)
}

func cleanupAttemptDigest(attempt cleanupDebtAttempt) (Digest, error) {
	if !validOpaqueID(attempt.MutationID) || !validOpaqueID(attempt.DebtID) ||
		!validOpaqueID(attempt.PersonalWorkspaceID.String()) || !validOpaqueID(attempt.RuntimeRunID.String()) ||
		!validAuthority(attempt.Authority) || attempt.ExpectedRevision == 0 || attempt.ResourceGeneration == 0 ||
		attempt.ResourceFence == 0 || attempt.ClaimGeneration == 0 || attempt.ClaimFence == 0 ||
		attempt.AttemptedAt.IsZero() || attempt.NextRetryAt.IsZero() || !attempt.NextRetryAt.After(attempt.AttemptedAt) ||
		attempt.FailureCategory < cleanupFailureUnavailable || attempt.FailureCategory > cleanupFailureIntegrityConflict ||
		attempt.LastErrorDigest == (Digest{}) ||
		(attempt.LastErrorEvidenceReference != "" && !validOpaqueID(attempt.LastErrorEvidenceReference)) ||
		!validCleanupEstimation(attempt.Estimation) || !validCleanupBlockers(attempt.Blockers) ||
		(attempt.Blockers.Classes == 0 && attempt.RetryDisposition != cleanupRetryScheduled) ||
		(attempt.Blockers.Classes != 0 && attempt.RetryDisposition != cleanupRetryBlocked) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	payload := canonicalCleanupAttempt{
		Schema: "slidesmith.runtime-execution.cleanup-mutation-attempt/v1", MutationID: attempt.MutationID,
		DebtID: attempt.DebtID, PersonalWorkspaceID: attempt.PersonalWorkspaceID.String(),
		RuntimeRunID: attempt.RuntimeRunID.String(), AuthorityKind: attempt.Authority.kind,
		AuthorityID: attempt.Authority.id.String(), AuthorityGeneration: attempt.Authority.generation,
		ExpectedRevision: attempt.ExpectedRevision, ResourceGeneration: attempt.ResourceGeneration,
		ResourceFence: attempt.ResourceFence, ClaimGeneration: attempt.ClaimGeneration, ClaimFence: attempt.ClaimFence,
		AttemptedAt: formatCleanupTime(attempt.AttemptedAt), NextRetryAt: formatCleanupTime(attempt.NextRetryAt),
		RetryDisposition: attempt.RetryDisposition, FailureCategory: attempt.FailureCategory,
		LastErrorDigest: attempt.LastErrorDigest.String(), LastErrorEvidenceReference: attempt.LastErrorEvidenceReference,
		EstimateState: attempt.Estimation.State, EstimateMethod: attempt.Estimation.Method,
		EstimatedBytes: attempt.Estimation.Bytes, EstimatedInodes: attempt.Estimation.Inodes,
		EstimateObservedAt: formatOptionalCleanupTime(attempt.Estimation.ObservedAt),
		BlockerClasses:     attempt.Blockers.Classes, BlockerDigest: formatOptionalCleanupDigest(attempt.Blockers.Digest),
		Uncontained: attempt.Uncontained,
	}
	return cleanupMutationDigest(payload.Schema, payload)
}

func cleanupResolutionDigest(resolution cleanupDebtResolution) (Digest, error) {
	if !validOpaqueID(resolution.MutationID) || !validOpaqueID(resolution.DebtID) ||
		!validOpaqueID(resolution.PersonalWorkspaceID.String()) || !validOpaqueID(resolution.RuntimeRunID.String()) ||
		!validAuthority(resolution.Authority) || resolution.ExpectedRevision == 0 ||
		resolution.ResourceGeneration == 0 || resolution.ResourceFence == 0 || resolution.ResolvedAt.IsZero() ||
		resolution.Class < cleanupResolutionReclaimed || resolution.Class > cleanupResolutionAcceptedException ||
		resolution.Reason < cleanupResolutionCleanupProven || resolution.Reason > cleanupResolutionAdministratorException ||
		!knownEvidenceRoot(resolution.EvidenceRoot) || resolution.EvidenceRoot.EvidenceRootID == (EvidenceRootID{}) ||
		!validCleanupBlockers(resolution.RemainingBlockers) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	if resolution.Class == cleanupResolutionAcceptedException && resolution.Authority.kind != AuthorityAdministrator {
		return Digest{}, newError(ErrorAuthorizationDenied)
	}
	if resolution.Class == cleanupResolutionAcceptedException &&
		(!validOpaqueID(resolution.ApprovalReference) || resolution.ExceptionUntil.IsZero() ||
			!resolution.ExceptionUntil.After(resolution.ResolvedAt)) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	if resolution.Class != cleanupResolutionAcceptedException &&
		(resolution.ApprovalReference != "" || resolution.IncidentReference != "" || resolution.TicketReference != "") {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	if resolution.IncidentReference != "" && !validOpaqueID(resolution.IncidentReference) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	if resolution.TicketReference != "" && !validOpaqueID(resolution.TicketReference) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	candidate := cleanupDebtRecord{
		Status: cleanupDebtResolved, Unresolved: false, RetryDisposition: cleanupRetryNone,
		ResolvedAt: resolution.ResolvedAt, ResolutionClass: resolution.Class, ResolutionReason: resolution.Reason,
		ResolutionAuthority: resolution.Authority, ResolutionAuditFactID: "pending-cleanup-resolution-audit",
		ResolutionEvidenceRoot: resolution.EvidenceRoot, ResolutionExpiresAt: resolution.ExceptionUntil,
		Blockers: resolution.RemainingBlockers, Uncontained: resolution.Uncontained,
		ResolutionIncidentReference: resolution.IncidentReference,
		ResolutionTicketReference:   resolution.TicketReference,
		ResolutionApprovalReference: resolution.ApprovalReference,
	}
	if !validCleanupResolutionDisposition(candidate) {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	payload := canonicalCleanupResolution{
		Schema: "slidesmith.runtime-execution.cleanup-mutation-resolution/v1", MutationID: resolution.MutationID,
		DebtID: resolution.DebtID, PersonalWorkspaceID: resolution.PersonalWorkspaceID.String(),
		RuntimeRunID: resolution.RuntimeRunID.String(), AuthorityKind: resolution.Authority.kind,
		AuthorityID: resolution.Authority.id.String(), AuthorityGeneration: resolution.Authority.generation,
		ExpectedRevision: resolution.ExpectedRevision, ResourceGeneration: resolution.ResourceGeneration,
		ResourceFence: resolution.ResourceFence, ResolvedAt: formatCleanupTime(resolution.ResolvedAt),
		ResolutionClass: resolution.Class, ResolutionReason: resolution.Reason,
		EvidenceSchema:          resolution.EvidenceRoot.SchemaVersion,
		EvidenceRootID:          resolution.EvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:      resolution.EvidenceRoot.Digest.String(),
		RemainingBlockerClasses: resolution.RemainingBlockers.Classes,
		RemainingBlockerDigest:  formatOptionalCleanupDigest(resolution.RemainingBlockers.Digest),
		Uncontained:             resolution.Uncontained, ExceptionUntil: formatOptionalCleanupTime(resolution.ExceptionUntil),
		IncidentReference: resolution.IncidentReference,
		TicketReference:   resolution.TicketReference,
		ApprovalReference: resolution.ApprovalReference,
	}
	return cleanupMutationDigest(payload.Schema, payload)
}

func cleanupMutationDigest(domain string, payload any) (Digest, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Digest{}, newError(ErrorInvalidRequest)
	}
	return digestBytes(append([]byte(domain+"\n"), encoded...)), nil
}
