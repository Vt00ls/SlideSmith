package taskorchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// DiagnosticAuditFaultController is a deterministic fail-closed seam for
// proving that protected diagnostics are never returned without access audit.
type DiagnosticAuditFaultController struct {
	next atomic.Bool
}

func (controller *DiagnosticAuditFaultController) FailNext() {
	if controller != nil {
		controller.next.Store(true)
	}
}

func (controller *DiagnosticAuditFaultController) consume() bool {
	return controller != nil && controller.next.CompareAndSwap(true, false)
}

type DiagnosticReason uint8

const (
	DiagnosticReasonOperations DiagnosticReason = iota + 1
	DiagnosticReasonIntegrity
)

// AdministratorMetadataAuthority is a reason-bound metadata-only authority.
// It is deliberately a different type from mutation and User authorities.
type AdministratorMetadataAuthority struct {
	id         AuthorityID
	generation AuthorizationGeneration
	reason     DiagnosticReason
}

func NewAdministratorMetadataAuthority(
	id AuthorityID,
	generation AuthorizationGeneration,
	reason DiagnosticReason,
) AdministratorMetadataAuthority {
	return AdministratorMetadataAuthority{id: id, generation: generation, reason: reason}
}

func (authority AdministratorMetadataAuthority) valid() bool {
	return validOpaqueID(authority.id.value) && authority.generation > 0 &&
		(authority.reason == DiagnosticReasonOperations ||
			authority.reason == DiagnosticReasonIntegrity)
}

type DiagnosticLookupKind uint8

const (
	DiagnosticLookupDecision DiagnosticLookupKind = iota + 1
	DiagnosticLookupOperation
	DiagnosticLookupProjectionBacklogInspection
	DiagnosticLookupProjectionBacklogRebuild
)

type OperationalDiagnosticQuery struct {
	authority   AdministratorMetadataAuthority
	taskID      TaskID
	lookupKind  DiagnosticLookupKind
	decisionID  DecisionID
	operationID OperationID
	resultLimit uint32
}

func NewDecisionDiagnosticQuery(
	authority AdministratorMetadataAuthority,
	taskID TaskID,
	decisionID DecisionID,
) OperationalDiagnosticQuery {
	return OperationalDiagnosticQuery{
		authority: authority, taskID: taskID,
		lookupKind: DiagnosticLookupDecision, decisionID: decisionID,
	}
}

func NewOperationDiagnosticQuery(
	authority AdministratorMetadataAuthority,
	taskID TaskID,
	operationID OperationID,
) OperationalDiagnosticQuery {
	return OperationalDiagnosticQuery{
		authority: authority, taskID: taskID,
		lookupKind: DiagnosticLookupOperation, operationID: operationID,
	}
}

type DiagnosticOwner uint8

const (
	DiagnosticOwnerTaskOrchestration DiagnosticOwner = iota + 1
)

type DiagnosticNextAction uint8

const (
	DiagnosticNextActionNone DiagnosticNextAction = iota + 1
	DiagnosticNextActionDeliver
	DiagnosticNextActionReconcile
)

// OperationalDiagnosticView is content-free and mutation-free. It exposes
// exact protected correlation only through the reason-bound diagnostics seam.
type OperationalDiagnosticView struct {
	SchemaVersion        ProjectionSchemaVersion
	Owner                DiagnosticOwner
	DecisionID           DecisionID
	OperationID          OperationID
	AcceptedTaskRevision TaskRevision
	AuditFactID          AuditFactID
	EnactmentKind        EnactmentKind
	DeliveryDisposition  DeliveryDisposition
	NextAction           DiagnosticNextAction
	AccessAuditFactRef   DiagnosticAuditFactRef
}

// DiagnosticAuditFactRef proves the reason-bound protected query was recorded
// before its exact result was returned.
type DiagnosticAuditFactRef struct {
	AuditFactID     AuditFactID
	CanonicalDigest ProjectionDigest
	Outcome         DiagnosticAuditOutcome
}

type DiagnosticAuditOutcome uint8

const (
	DiagnosticAuditAccepted DiagnosticAuditOutcome = iota + 1
	DiagnosticAuditDenied
)

type OperationalDiagnostics interface {
	Diagnose(context.Context, OperationalDiagnosticQuery) (OperationalDiagnosticView, error)
}

type diagnosticEngine struct {
	persistence *memoryPersistence
	now         func() time.Time
	auditFaults *DiagnosticAuditFaultController
}

func (engine *diagnosticEngine) Diagnose(
	ctx context.Context,
	query OperationalDiagnosticQuery,
) (OperationalDiagnosticView, error) {
	if engine == nil || engine.persistence == nil || ctx == nil || ctx.Err() != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOperationalDiagnosticQuery(query) {
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	engine.persistence.mu.Lock()
	defer engine.persistence.mu.Unlock()
	var view OperationalDiagnosticView
	switch query.lookupKind {
	case DiagnosticLookupDecision:
		for _, committed := range engine.persistence.decisions {
			if committed.decision.DecisionID == query.decisionID &&
				committed.decision.TaskProjection.TaskID == query.taskID {
				view = diagnosticViewFromDecision(committed.decision)
				break
			}
		}
	case DiagnosticLookupOperation:
		if record, ok := engine.persistence.outbox[query.operationID]; ok {
			if record.TaskID != query.taskID {
				break
			}
			for _, committed := range engine.persistence.decisions {
				if committed.decision.DecisionID != record.DecisionID {
					continue
				}
				view = diagnosticViewFromDecision(committed.decision)
				view.OperationID = record.OperationID
				view.EnactmentKind = record.Kind
				view.DeliveryDisposition = DeliveryPending
				if state, exists := engine.persistence.deliveries[record.OperationID]; exists &&
					state.Disposition != 0 {
					view.DeliveryDisposition = state.Disposition
				}
				view.NextAction = diagnosticNextAction(view.DeliveryDisposition)
				break
			}
		}
	}
	if engine.auditFaults.consume() {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	sequence := engine.persistence.nextDiagnosticAuditSequence
	engine.persistence.nextDiagnosticAuditSequence++
	outcome := DiagnosticAuditAccepted
	if view.DecisionID == (DecisionID{}) {
		outcome = DiagnosticAuditDenied
	}
	auditRef := diagnosticAuditFactRef(sequence, query, outcome, engine.now().UTC())
	engine.persistence.diagnosticAudits[auditRef.AuditFactID] = auditRef
	if outcome == DiagnosticAuditDenied {
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	view.AccessAuditFactRef = auditRef
	return view, nil
}

func (adapter *PostgresAdapter) Diagnose(
	ctx context.Context,
	query OperationalDiagnosticQuery,
) (OperationalDiagnosticView, error) {
	if adapter == nil || adapter.db == nil || ctx == nil || ctx.Err() != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOperationalDiagnosticQuery(query) {
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	tx, err := adapter.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	var encoded []byte
	var operationID string
	var kind EnactmentKind
	var disposition DeliveryDisposition
	var queryErr error
	switch query.lookupKind {
	case DiagnosticLookupDecision:
		queryErr = tx.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT decision_state FROM %s WHERE decision_id=$1 AND task_id=$2",
			adapter.table("task_orchestration_decisions"),
		), query.decisionID.value, query.taskID.value).Scan(&encoded)
	case DiagnosticLookupOperation:
		queryErr = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT decision.decision_state,
			outbox.operation_id, outbox.kind, COALESCE(delivery.disposition, $2)
			FROM %s AS outbox
			JOIN %s AS decision ON decision.decision_id=outbox.decision_id
			LEFT JOIN %s AS delivery ON delivery.operation_id=outbox.operation_id
			WHERE outbox.operation_id=$1 AND outbox.task_id=$3`,
			adapter.table("task_orchestration_outbox"),
			adapter.table("task_orchestration_decisions"),
			adapter.table("task_orchestration_outbox_delivery"),
		), query.operationID.value, DeliveryPending, query.taskID.value).Scan(
			&encoded, &operationID, &kind, &disposition,
		)
	default:
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	if errors.Is(queryErr, sql.ErrNoRows) {
		if adapter.diagnosticAuditFaults.consume() {
			return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
		}
		if _, err := adapter.recordDiagnosticAudit(
			ctx, tx, query, DiagnosticAuditDenied,
		); err != nil {
			return OperationalDiagnosticView{}, err
		}
		if err := tx.Commit(); err != nil {
			return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
		}
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	if queryErr != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	var state postgresDecisionState
	if json.Unmarshal(encoded, &state) != nil {
		return OperationalDiagnosticView{}, newPersistenceError(PersistenceStateCorrupt)
	}
	decision := state.decision()
	if !validPersistedDecision(decision) {
		return OperationalDiagnosticView{}, newPersistenceError(PersistenceStateCorrupt)
	}
	view := diagnosticViewFromDecision(decision)
	if query.lookupKind == DiagnosticLookupOperation {
		if operationID != query.operationID.value || !validEnactmentKind(kind) ||
			disposition < DeliveryPending || disposition > DeliveryIntegrityConflict {
			return OperationalDiagnosticView{}, newPersistenceError(PersistenceStateCorrupt)
		}
		view.OperationID = OperationID{value: operationID}
		view.EnactmentKind = kind
		view.DeliveryDisposition = disposition
		view.NextAction = diagnosticNextAction(disposition)
	}
	if adapter.diagnosticAuditFaults.consume() {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	auditRef, err := adapter.recordDiagnosticAudit(
		ctx, tx, query, DiagnosticAuditAccepted,
	)
	if err != nil {
		return OperationalDiagnosticView{}, err
	}
	if err := tx.Commit(); err != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	view.AccessAuditFactRef = auditRef
	return view, nil
}

func validOperationalDiagnosticQuery(query OperationalDiagnosticQuery) bool {
	if !query.authority.valid() || !validOpaqueID(query.taskID.value) {
		return false
	}
	switch query.lookupKind {
	case DiagnosticLookupDecision:
		return validOpaqueID(query.decisionID.value) && query.operationID == (OperationID{}) &&
			query.resultLimit == 0
	case DiagnosticLookupOperation:
		return validOpaqueID(query.operationID.value) && query.decisionID == (DecisionID{}) &&
			query.resultLimit == 0
	case DiagnosticLookupProjectionBacklogInspection,
		DiagnosticLookupProjectionBacklogRebuild:
		return query.decisionID == (DecisionID{}) && query.operationID == (OperationID{}) &&
			query.resultLimit > 0 && query.resultLimit <= 100
	default:
		return false
	}
}

func diagnosticAuditFactRef(
	sequence uint64,
	query OperationalDiagnosticQuery,
	outcome DiagnosticAuditOutcome,
	recordedAt time.Time,
) DiagnosticAuditFactRef {
	id := AuditFactID{value: fmt.Sprintf("diagnostic-audit-fact-%06d", sequence)}
	projection := diagnosticAuditProjection(id, query, outcome, recordedAt)
	return DiagnosticAuditFactRef{
		AuditFactID: id, CanonicalDigest: projection.CanonicalDigest, Outcome: outcome,
	}
}

func diagnosticAuditProjection(
	auditFactID AuditFactID,
	query OperationalDiagnosticQuery,
	outcome DiagnosticAuditOutcome,
	recordedAt time.Time,
) ExternalAuditProjection {
	projection := ExternalAuditProjection{
		SchemaVersion:           ProjectionSchemaV1,
		FactKind:                ExternalAuditDiagnosticAccessFact,
		AuditFactID:             auditFactID,
		TaskID:                  query.taskID,
		DecisionID:              query.decisionID,
		OperationID:             query.operationID,
		AuthorityID:             query.authority.id,
		AuthorizationGeneration: query.authority.generation,
		DiagnosticLookup:        query.lookupKind,
		DiagnosticReason:        query.authority.reason,
		DiagnosticOutcome:       outcome,
		ResultLimit:             query.resultLimit,
		RecordedAt:              recordedAt,
	}
	projection.CanonicalDigest = ExternalAuditProjectionDigest(projection)
	return projection
}

func (adapter *PostgresAdapter) recordDiagnosticAudit(
	ctx context.Context,
	tx *sql.Tx,
	query OperationalDiagnosticQuery,
	outcome DiagnosticAuditOutcome,
) (DiagnosticAuditFactRef, error) {
	var sequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+
		adapter.table("task_orchestration_diagnostic_audit_sequence")+"')").Scan(&sequence); err != nil {
		return DiagnosticAuditFactRef{}, newError(ErrorDependencyUnavailable)
	}
	recordedAt := adapter.now().UTC()
	auditRef := diagnosticAuditFactRef(sequence, query, outcome, recordedAt)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, canonical_digest, task_id, lookup_kind, decision_id,
		operation_id, result_limit, authority_id, authority_generation, reason, outcome, recorded_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		adapter.table("task_orchestration_diagnostic_audit_facts")),
		auditRef.AuditFactID.value, auditRef.CanonicalDigest[:], query.taskID.value,
		query.lookupKind, query.decisionID.value, query.operationID.value,
		query.resultLimit, query.authority.id.value, query.authority.generation,
		query.authority.reason, outcome, recordedAt,
	); err != nil {
		return DiagnosticAuditFactRef{}, newError(ErrorDependencyUnavailable)
	}
	return auditRef, nil
}

func diagnosticViewFromDecision(decision TransitionDecision) OperationalDiagnosticView {
	return OperationalDiagnosticView{
		SchemaVersion:        ProjectionSchemaV1,
		Owner:                DiagnosticOwnerTaskOrchestration,
		DecisionID:           decision.DecisionID,
		AcceptedTaskRevision: decision.AcceptedTaskRevision,
		AuditFactID:          decision.MandatoryAuditFactRef.AuditFactID,
		NextAction:           DiagnosticNextActionNone,
	}
}

func diagnosticNextAction(disposition DeliveryDisposition) DiagnosticNextAction {
	switch disposition {
	case DeliveryPending, DeliveryClaimed, DeliveryBackpressured, DeliveryDeferred:
		return DiagnosticNextActionDeliver
	case DeliveryReconciliationRequired:
		return DiagnosticNextActionReconcile
	default:
		return DiagnosticNextActionNone
	}
}
