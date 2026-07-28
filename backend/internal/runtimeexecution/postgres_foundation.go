package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type PersistenceFaultPoint uint8

const (
	PersistenceFaultBeforeRuntimeWrite PersistenceFaultPoint = iota + 1
	PersistenceFaultBeforeDecision
	PersistenceFaultBeforeMandatoryAudit
	PersistenceFaultAfterMandatoryAudit
	PersistenceFaultBeforeOutbox
	PersistenceFaultBeforeCommit
	PersistenceFaultAfterCommit
	PersistenceFaultBeforeResponse
)

func (point PersistenceFaultPoint) String() string {
	switch point {
	case PersistenceFaultBeforeRuntimeWrite:
		return "before_runtime_write"
	case PersistenceFaultBeforeDecision:
		return "before_decision"
	case PersistenceFaultBeforeMandatoryAudit:
		return "before_mandatory_audit"
	case PersistenceFaultAfterMandatoryAudit:
		return "after_mandatory_audit"
	case PersistenceFaultBeforeOutbox:
		return "before_outbox"
	case PersistenceFaultBeforeCommit:
		return "before_commit"
	case PersistenceFaultAfterCommit:
		return "after_commit"
	case PersistenceFaultBeforeResponse:
		return "before_response"
	default:
		return "unknown"
	}
}

type PersistenceFaultInjector interface {
	FailAt(PersistenceFaultPoint) bool
}

// PersistenceFaultController provides deterministic crash boundaries without
// exposing SQL or transaction authority.
type PersistenceFaultController struct {
	mu   sync.Mutex
	next PersistenceFaultPoint
}

func (controller *PersistenceFaultController) FailNextAt(point PersistenceFaultPoint) error {
	if point < PersistenceFaultBeforeRuntimeWrite || point > PersistenceFaultBeforeResponse {
		return newPersistenceError(PersistenceInvalidConfiguration)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.next = point
	return nil
}

func (controller *PersistenceFaultController) FailAt(point PersistenceFaultPoint) bool {
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.next != point {
		return false
	}
	controller.next = 0
	return true
}

type ReconciliationReason uint8

const (
	ReconciliationTransportAmbiguous ReconciliationReason = iota + 1
	ReconciliationProjectionDelivery
)

const postgresReconciliationCommandKind int16 = 100

type reconciliationFoundationIntent struct {
	PersonalWorkspaceID         PersonalWorkspaceID
	RuntimeRunID                RuntimeRunID
	ExpectedRuntimeRevision     RuntimeRevision
	ExpectedOperationGeneration OperationGeneration
	ExpectedRuntimeFence        RuntimeFence
	OperationID                 OperationID
	Authority                   RuntimeAuthority
	Reason                      ReconciliationReason
	CanonicalDigest             Digest
	canonical                   []byte
}

type canonicalReconciliationFoundation struct {
	Schema                      string `json:"schema"`
	PersonalWorkspaceID         string `json:"personal_workspace_id"`
	RuntimeRunID                string `json:"runtime_run_id"`
	ExpectedRuntimeRevision     uint64 `json:"expected_runtime_revision"`
	ExpectedOperationGeneration uint64 `json:"expected_operation_generation"`
	ExpectedRuntimeFence        uint64 `json:"expected_runtime_fence"`
	OperationID                 string `json:"operation_id"`
	AuthorityKind               uint8  `json:"authority_kind"`
	AuthorityID                 string `json:"authority_id"`
	AuthorityGeneration         uint64 `json:"authority_generation"`
	Reason                      uint8  `json:"reason"`
}

func newReconciliationFoundationIntent(
	fixture RuntimeFixture,
	operationID OperationID,
	authority RuntimeAuthority,
	reason ReconciliationReason,
) reconciliationFoundationIntent {
	canonical, _ := json.Marshal(canonicalReconciliationFoundation{
		Schema:              "slidesmith.runtime-execution.reconciliation-foundation/v1",
		PersonalWorkspaceID: fixture.PersonalWorkspaceID.String(), RuntimeRunID: fixture.RuntimeRunID.String(),
		ExpectedRuntimeRevision:     uint64(fixture.RuntimeRevision),
		ExpectedOperationGeneration: uint64(fixture.OperationGeneration), ExpectedRuntimeFence: uint64(fixture.RuntimeFence),
		OperationID: operationID.String(), AuthorityKind: uint8(authority.kind), AuthorityID: authority.id.String(),
		AuthorityGeneration: uint64(authority.generation), Reason: uint8(reason),
	})
	return reconciliationFoundationIntent{
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID,
		ExpectedRuntimeRevision: fixture.RuntimeRevision, ExpectedOperationGeneration: fixture.OperationGeneration,
		ExpectedRuntimeFence: fixture.RuntimeFence, OperationID: operationID, Authority: authority, Reason: reason,
		CanonicalDigest: digestBytes(canonical), canonical: canonical,
	}
}

func validReconciliationFoundationIntent(intent reconciliationFoundationIntent) bool {
	return validOpaqueID(intent.PersonalWorkspaceID.String()) && validOpaqueID(intent.RuntimeRunID.String()) &&
		validOpaqueID(intent.OperationID.String()) && validAuthority(intent.Authority) &&
		intent.ExpectedRuntimeRevision > 0 && intent.ExpectedOperationGeneration > 0 && intent.ExpectedRuntimeFence > 0 &&
		intent.Reason >= ReconciliationTransportAmbiguous && intent.Reason <= ReconciliationProjectionDelivery &&
		intent.CanonicalDigest != (Digest{}) && intent.CanonicalDigest == digestBytes(intent.canonical)
}

func (authority *PostgresAuthority) persistReconciliationFoundation(
	ctx context.Context,
	intent reconciliationFoundationIntent,
) (RuntimeDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if !validReconciliationFoundationIntent(intent) {
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, intent.RuntimeRunID)
	if err == sql.ErrNoRows || err == nil && !authorized(record, intent.PersonalWorkspaceID, intent.Authority) {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if replay, found, err := authority.lookupFoundationReplay(ctx, tx, record, intent); err != nil {
		return RuntimeDecision{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}
	if record.fixture.RuntimeRevision != intent.ExpectedRuntimeRevision ||
		record.fixture.OperationGeneration != intent.ExpectedOperationGeneration ||
		record.fixture.RuntimeFence != intent.ExpectedRuntimeFence {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State != RuntimeWaitingForLease && record.fixture.State != RuntimeReconciling ||
		record.fixture.Outcome != RuntimeOutcomeNone || record.operation.Status != OperationBound ||
		record.lease.AcquireStatus == LeaseGranted {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if authority.failAt(PersistenceFaultBeforeRuntimeWrite) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	previousRevision := record.fixture.RuntimeRevision
	beforeState := record.fixture.State
	beforeSafetyEpoch := record.fixture.SafetyEpoch
	record.fixture.RuntimeRevision++
	record.fixture.State = RuntimeReconciling
	record.reconciliation = ReconciliationRequiredStatus
	fixture := fixtureFromRuntimeRecord(record)
	aggregateState, err := encodePostgresRuntimeFixture(fixture)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET runtime_revision=$1, runtime_state=$2,
		aggregate_state=$3, updated_at=$4 WHERE runtime_run_id=$5 AND runtime_revision=$6`,
		authority.table("runtime_execution_runtimes")), record.fixture.RuntimeRevision, record.fixture.State,
		aggregateState, postgresTimestamp(authority.now()), intent.RuntimeRunID.String(), previousRevision)
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if authority.failAt(PersistenceFaultBeforeDecision) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	var sequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("runtime_execution_decision_sequence")+"')").Scan(&sequence); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	decisionID := RuntimeDecisionID{value: fmt.Sprintf("runtime-decision-postgres-%020d", sequence)}
	fact := RuntimeDecisionFact{
		DecisionID: decisionID, Disposition: DecisionAccepted, OperationID: intent.OperationID,
		CanonicalRequestDigest: intent.CanonicalDigest, PreviousRuntimeRevision: previousRevision,
		ResultingRuntimeRevision: record.fixture.RuntimeRevision, StateAtDecision: RuntimeReconciling,
		OutcomeAtDecision: RuntimeOutcomeNone, Retry: RetrySameRequest, Reconciliation: ReconciliationRequired,
	}
	decisionState, err := encodePostgresDecisionFact(fact)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	committedAt := postgresTimestamp(authority.now())
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_decisions")),
		decisionID.String(), intent.RuntimeRunID.String(), intent.OperationID.String(), intent.CanonicalDigest[:],
		previousRevision, record.fixture.RuntimeRevision, decisionState, committedAt); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		personal_workspace_id, runtime_run_id, command_kind, operation_id, canonical_request_digest,
		canonical_request, decision_id
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_requests")),
		intent.PersonalWorkspaceID.String(), intent.RuntimeRunID.String(), postgresReconciliationCommandKind,
		intent.OperationID.String(), intent.CanonicalDigest[:], intent.canonical, decisionID.String()); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		runtime_run_id, runtime_revision, decision_id, aggregate_state
	) VALUES ($1,$2,$3,$4)`, authority.table("runtime_execution_revisions")),
		intent.RuntimeRunID.String(), record.fixture.RuntimeRevision, decisionID.String(), aggregateState); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	auditID := "runtime-audit-postgres-" + fmt.Sprintf("%020d", sequence)
	auditState := newPostgresMandatoryAuditState(postgresMandatoryAuditInput{
		AuditFactID: auditID, Action: postgresAuditReconciliationRequired,
		ReasonCode: uint8(intent.Reason), Decision: fact, RuntimeRunID: intent.RuntimeRunID,
		RequestDigest: intent.CanonicalDigest, Authority: intent.Authority,
		BeforeState: beforeState, AfterState: record.fixture.State,
		BeforeOperationGeneration: record.fixture.OperationGeneration,
		AfterOperationGeneration:  record.fixture.OperationGeneration,
		BeforeRuntimeFence:        record.fixture.RuntimeFence,
		AfterRuntimeFence:         record.fixture.RuntimeFence,
		BeforeSafetyEpoch:         beforeSafetyEpoch, AfterSafetyEpoch: record.fixture.SafetyEpoch,
		OccurredAt: committedAt, RecordedAt: committedAt, EvidenceRoot: record.evidenceRoot,
	})
	auditStateBytes, err := auditState.encode()
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	auditDigest, err := auditState.canonicalDigest()
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, decision_id, runtime_run_id, operation_id, request_digest,
		schema_version, integrity_version, owning_module, canonical_digest,
		authority_kind, authority_id, authority_generation, action, result,
		before_revision, after_revision, occurred_at, recorded_at, source_clock_id, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		authority.table("runtime_execution_mandatory_audit")),
		auditState.AuditFactID, decisionID.String(), intent.RuntimeRunID.String(), intent.OperationID.String(),
		intent.CanonicalDigest[:], auditState.SchemaVersion, auditState.IntegrityVersion, auditState.OwningModule,
		auditDigest[:], auditState.AuthorityKind, auditState.AuthorityID, auditState.AuthorityGeneration,
		auditState.Action, auditState.Result, auditState.BeforeRevision, auditState.AfterRevision,
		committedAt, committedAt, auditState.SourceClockID, auditStateBytes); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) || authority.failAt(PersistenceFaultBeforeOutbox) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	scopeDigest := authorityScopeDigest(intent.PersonalWorkspaceID, intent.RuntimeRunID)
	payloadDigest := digestBytes(intent.canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, decision_id, runtime_run_id, canonical_request_digest,
		authority_scope_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_outbox")),
		intent.OperationID.String(), decisionID.String(), intent.RuntimeRunID.String(), intent.CanonicalDigest[:],
		scopeDigest[:], intent.canonical, payloadDigest[:], committedAt); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_outbox_delivery")), intent.OperationID.String(), OutboxPending); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	var evidenceDigest any
	if record.evidenceRoot.EvidenceRootID != (EvidenceRootID{}) {
		evidenceDigest = record.evidenceRoot.Digest[:]
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, decision_id, owner_authority_kind, owner_authority_id,
		owner_authority_generation, runtime_revision, operation_generation, runtime_fence,
		reason, status, result, first_recorded_at, last_recorded_at, observation_count,
		unresolved, next_retry_at, safe_failure_count, stale_evidence_count,
		evidence_root_id, evidence_root_digest
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,1,TRUE,$13,0,0,$14,$15)`,
		authority.table("runtime_execution_reconciliation_obligations")),
		intent.OperationID.String(), intent.RuntimeRunID.String(), decisionID.String(),
		intent.Authority.kind, intent.Authority.id.String(), intent.Authority.generation,
		record.fixture.RuntimeRevision, record.fixture.OperationGeneration, record.fixture.RuntimeFence,
		intent.Reason, ReconciliationObligationOpen, DecisionAccepted, committedAt,
		record.evidenceRoot.EvidenceRootID.String(), evidenceDigest); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		fact_id, audit_fact_id, audit_canonical_digest, fact_revision,
		projection_schema_version, audit_delivery_status, telemetry_delivery_status, degraded
	) VALUES ($1,$2,$3,$4,$5,$6,$6,TRUE)`, authority.table("runtime_execution_projection_backlog")),
		decisionID.String(), auditID, auditDigest[:], record.fixture.RuntimeRevision,
		SchemaV1, ProjectionPending); err != nil {
		return RuntimeDecision{}, normalizeFoundationWriteFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	decision := RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}
	if authority.failAt(PersistenceFaultAfterCommit) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	authority.deliverProjection(ctx, ProjectionFact{
		DecisionID: decisionID, RuntimeRunID: intent.RuntimeRunID, OperationID: intent.OperationID,
		CanonicalDigest: intent.CanonicalDigest, RuntimeRevision: record.fixture.RuntimeRevision,
		AuditFactID: auditID, AuditCanonicalDigest: auditDigest, ProjectionSchemaVersion: SchemaV1,
	})
	if authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func postgresTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func (authority *PostgresAuthority) loadRuntimeForUpdate(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
) (*runtimeRecord, error) {
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id, aggregate_state
		FROM %s WHERE runtime_run_id=$1 FOR UPDATE`, authority.table("runtime_execution_runtimes")), runtimeRunID.String())
	return scanPostgresRuntimeRecord(row, runtimeRunID)
}

func (authority *PostgresAuthority) lookupFoundationReplay(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	intent reconciliationFoundationIntent,
) (RuntimeDecision, bool, error) {
	var retainedDigest, retainedCanonical []byte
	var retainedWorkspaceID, decisionID, retainedGrantID, retainedWorkItemID string
	var retainedKind int16
	var retainedGrantGeneration AdmissionGrantGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, command_kind, canonical_request_digest,
		canonical_request, decision_id, admission_grant_id, admission_work_item_id,
		admission_grant_generation FROM %s WHERE runtime_run_id=$1 AND operation_id=$2`,
		authority.table("runtime_execution_requests")), intent.RuntimeRunID.String(), intent.OperationID.String()).Scan(
		&retainedWorkspaceID, &retainedKind, &retainedDigest, &retainedCanonical, &decisionID,
		&retainedGrantID, &retainedWorkItemID, &retainedGrantGeneration)
	if err == sql.ErrNoRows {
		return RuntimeDecision{}, false, nil
	}
	if err != nil {
		return RuntimeDecision{}, false, normalizeRuntimePersistenceFailure(err)
	}
	if retainedWorkspaceID != intent.PersonalWorkspaceID.String() || retainedKind != postgresReconciliationCommandKind ||
		!bytes.Equal(retainedDigest, intent.CanonicalDigest[:]) || !bytes.Equal(retainedCanonical, intent.canonical) ||
		retainedGrantID != "" || retainedWorkItemID != "" || retainedGrantGeneration != 0 {
		return RuntimeDecision{}, false, newError(ErrorIntegrityConflict)
	}
	binding := retainedCommandBindingValue{
		kind: postgresReconciliationCommandKind, operationID: intent.OperationID,
		workspaceID: intent.PersonalWorkspaceID, runtimeRunID: intent.RuntimeRunID,
		caller: intent.Authority, digest: intent.CanonicalDigest, canonical: intent.canonical,
		expectedOperationGeneration: intent.ExpectedOperationGeneration,
		expectedRuntimeFence:        intent.ExpectedRuntimeFence, safetyEpoch: record.fixture.SafetyEpoch,
		auditAction: postgresAuditReconciliationRequired, auditReasonCode: uint8(intent.Reason),
	}
	fact, err := authority.loadRetainedDecision(ctx, tx, decisionID, binding)
	if err != nil {
		return RuntimeDecision{}, false, err
	}
	return RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}, true, nil
}

func fixtureFromRuntimeRecord(record *runtimeRecord) RuntimeFixture {
	fixture := record.fixture
	fixture.Operation = record.operation
	fixture.Lease = record.lease
	fixture.Deadline = record.deadline
	fixture.LeaseAcquireBy = record.leaseAcquireBy
	fixture.Cancellation = record.cancellation
	fixture.EvidenceRoot = record.evidenceRoot
	fixture.Capacity = record.capacity
	fixture.Reconciliation = record.reconciliation
	return fixture
}

func normalizeFoundationWriteFailure(failure error) error {
	return normalizeRuntimePersistenceFailure(failure)
}

type OutboxDisposition uint8

const (
	OutboxPending OutboxDisposition = iota + 1
	OutboxAcknowledged
)

type outboxAcknowledgement struct {
	OperationID            OperationID
	DecisionID             RuntimeDecisionID
	CanonicalRequestDigest Digest
	PayloadDigest          Digest
	AckDigest              Digest
}

type outboxDeliveryView struct {
	OperationID            OperationID
	DecisionID             RuntimeDecisionID
	CanonicalRequestDigest Digest
	PayloadDigest          Digest
	Disposition            OutboxDisposition
	DeliveryCount          uint64
	AckDigest              Digest
}

func (authority *PostgresAuthority) acknowledgeOutbox(
	ctx context.Context,
	acknowledgement outboxAcknowledgement,
) (outboxDeliveryView, error) {
	if ctx == nil || ctx.Err() != nil {
		return outboxDeliveryView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOpaqueID(acknowledgement.OperationID.String()) ||
		!validOpaqueID(acknowledgement.DecisionID.String()) ||
		acknowledgement.CanonicalRequestDigest == (Digest{}) || acknowledgement.PayloadDigest == (Digest{}) ||
		acknowledgement.AckDigest == (Digest{}) {
		return outboxDeliveryView{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return outboxDeliveryView{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	var decisionID string
	var canonicalDigest, payloadDigest, retainedAck []byte
	var disposition OutboxDisposition
	var deliveryCount uint64
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT outbox.decision_id,
		outbox.canonical_request_digest, outbox.payload_digest,
		delivery.disposition, delivery.delivery_count, delivery.ack_digest
		FROM %s AS outbox JOIN %s AS delivery ON delivery.operation_id=outbox.operation_id
		WHERE outbox.operation_id=$1 FOR UPDATE`, authority.table("runtime_execution_outbox"),
		authority.table("runtime_execution_outbox_delivery")), acknowledgement.OperationID.String()).
		Scan(&decisionID, &canonicalDigest, &payloadDigest, &disposition, &deliveryCount, &retainedAck)
	if err == sql.ErrNoRows {
		return outboxDeliveryView{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return outboxDeliveryView{}, normalizeRuntimePersistenceFailure(err)
	}
	if decisionID != acknowledgement.DecisionID.String() ||
		!bytes.Equal(canonicalDigest, acknowledgement.CanonicalRequestDigest[:]) ||
		!bytes.Equal(payloadDigest, acknowledgement.PayloadDigest[:]) {
		return outboxDeliveryView{}, newError(ErrorIntegrityConflict)
	}
	if disposition == OutboxAcknowledged {
		if !bytes.Equal(retainedAck, acknowledgement.AckDigest[:]) {
			return outboxDeliveryView{}, newError(ErrorIntegrityConflict)
		}
		if err := tx.Commit(); err != nil {
			return outboxDeliveryView{}, normalizeRuntimePersistenceFailure(err)
		}
		return outboxDeliveryView{
			OperationID: acknowledgement.OperationID, DecisionID: acknowledgement.DecisionID,
			CanonicalRequestDigest: acknowledgement.CanonicalRequestDigest, PayloadDigest: acknowledgement.PayloadDigest,
			Disposition: disposition, DeliveryCount: deliveryCount, AckDigest: acknowledgement.AckDigest,
		}, nil
	}
	if disposition != OutboxPending || len(retainedAck) != 0 {
		return outboxDeliveryView{}, newError(ErrorIntegrityConflict)
	}
	now := postgresTimestamp(authority.now())
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1, ack_digest=$2,
		delivery_count=1, first_attempt_at=$3, last_attempt_at=$3, acknowledged_at=$3
		WHERE operation_id=$4 AND disposition=$5`, authority.table("runtime_execution_outbox_delivery")),
		OutboxAcknowledged, acknowledgement.AckDigest[:], now, acknowledgement.OperationID.String(), OutboxPending)
	if err != nil {
		return outboxDeliveryView{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return outboxDeliveryView{}, newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return outboxDeliveryView{}, normalizeRuntimePersistenceFailure(err)
	}
	return outboxDeliveryView{
		OperationID: acknowledgement.OperationID, DecisionID: acknowledgement.DecisionID,
		CanonicalRequestDigest: acknowledgement.CanonicalRequestDigest, PayloadDigest: acknowledgement.PayloadDigest,
		Disposition: OutboxAcknowledged, DeliveryCount: 1, AckDigest: acknowledgement.AckDigest,
	}, nil
}

type ReconciliationObligationStatus uint8

const (
	ReconciliationObligationOpen ReconciliationObligationStatus = iota + 1
	ReconciliationObligationResolved
)

type ProjectionDeliveryStatus uint8

const (
	ProjectionPending ProjectionDeliveryStatus = iota + 1
	ProjectionDelivered
	ProjectionFailed
)

type ProjectionSafeFailure uint8

const (
	ProjectionFailureNone ProjectionSafeFailure = iota
	ProjectionFailureUnavailable
)

// ProjectionFact is content-free and carries only authoritative correlation.
// Delivery occurs strictly after the protected PostgreSQL commit.
type ProjectionFact struct {
	DecisionID              RuntimeDecisionID
	RuntimeRunID            RuntimeRunID
	OperationID             OperationID
	CanonicalDigest         Digest
	RuntimeRevision         RuntimeRevision
	AuditFactID             string
	AuditCanonicalDigest    Digest
	ProjectionSchemaVersion SchemaVersion
}

type ProjectionDelivery interface {
	Deliver(context.Context, ProjectionFact) error
}

type ProjectionDeliveryFunc func(context.Context, ProjectionFact) error

func (function ProjectionDeliveryFunc) Deliver(ctx context.Context, fact ProjectionFact) error {
	return function(ctx, fact)
}

func (authority *PostgresAuthority) deliverProjection(ctx context.Context, fact ProjectionFact) {
	if authority.projection == nil {
		return
	}
	deliveryError := authority.projection.Deliver(ctx, fact)
	status := ProjectionDelivered
	safeFailure := ProjectionFailureNone
	degraded := false
	if deliveryError != nil {
		status = ProjectionFailed
		safeFailure = ProjectionFailureUnavailable
		degraded = true
	}
	now := postgresTimestamp(authority.now())
	var deliveredAt any
	if status == ProjectionDelivered {
		deliveredAt = now
	}
	_, _ = authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		audit_delivery_status=$1, telemetry_delivery_status=$1,
		attempt_count=attempt_count+1, last_safe_failure=$2, degraded=$3,
		first_attempt_at=COALESCE(first_attempt_at,$4), last_attempt_at=$4,
		delivered_at=COALESCE($5,delivered_at)
		WHERE fact_id=$6 AND audit_fact_id=$7 AND audit_canonical_digest=$8
		AND fact_revision=$9 AND projection_schema_version=$10`, authority.table("runtime_execution_projection_backlog")),
		status, safeFailure, degraded, now, deliveredAt, fact.DecisionID.String(), fact.AuditFactID,
		fact.AuditCanonicalDigest[:], fact.RuntimeRevision, fact.ProjectionSchemaVersion)
}
