package taskorchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

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
)

type OperationalDiagnosticQuery struct {
	authority   AdministratorMetadataAuthority
	lookupKind  DiagnosticLookupKind
	decisionID  DecisionID
	operationID OperationID
}

func NewDecisionDiagnosticQuery(
	authority AdministratorMetadataAuthority,
	decisionID DecisionID,
) OperationalDiagnosticQuery {
	return OperationalDiagnosticQuery{
		authority: authority, lookupKind: DiagnosticLookupDecision, decisionID: decisionID,
	}
}

func NewOperationDiagnosticQuery(
	authority AdministratorMetadataAuthority,
	operationID OperationID,
) OperationalDiagnosticQuery {
	return OperationalDiagnosticQuery{
		authority: authority, lookupKind: DiagnosticLookupOperation, operationID: operationID,
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
}

type OperationalDiagnostics interface {
	Diagnose(context.Context, OperationalDiagnosticQuery) (OperationalDiagnosticView, error)
}

type diagnosticEngine struct {
	persistence *memoryPersistence
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
	switch query.lookupKind {
	case DiagnosticLookupDecision:
		for _, committed := range engine.persistence.decisions {
			if committed.decision.DecisionID == query.decisionID {
				return diagnosticViewFromDecision(committed.decision), nil
			}
		}
	case DiagnosticLookupOperation:
		if record, ok := engine.persistence.outbox[query.operationID]; ok {
			for _, committed := range engine.persistence.decisions {
				if committed.decision.DecisionID != record.DecisionID {
					continue
				}
				view := diagnosticViewFromDecision(committed.decision)
				view.OperationID = record.OperationID
				view.EnactmentKind = record.Kind
				view.DeliveryDisposition = DeliveryPending
				if state, exists := engine.persistence.deliveries[record.OperationID]; exists &&
					state.Disposition != 0 {
					view.DeliveryDisposition = state.Disposition
				}
				view.NextAction = diagnosticNextAction(view.DeliveryDisposition)
				return view, nil
			}
		}
	}
	return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
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
	var encoded []byte
	var operationID string
	var kind EnactmentKind
	var disposition DeliveryDisposition
	var err error
	switch query.lookupKind {
	case DiagnosticLookupDecision:
		err = adapter.db.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT decision_state FROM %s WHERE decision_id=$1",
			adapter.table("task_orchestration_decisions"),
		), query.decisionID.value).Scan(&encoded)
	case DiagnosticLookupOperation:
		err = adapter.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT decision.decision_state,
			outbox.operation_id, outbox.kind, COALESCE(delivery.disposition, $2)
			FROM %s AS outbox
			JOIN %s AS decision ON decision.decision_id=outbox.decision_id
			LEFT JOIN %s AS delivery ON delivery.operation_id=outbox.operation_id
			WHERE outbox.operation_id=$1`,
			adapter.table("task_orchestration_outbox"),
			adapter.table("task_orchestration_decisions"),
			adapter.table("task_orchestration_outbox_delivery"),
		), query.operationID.value, DeliveryPending).Scan(
			&encoded, &operationID, &kind, &disposition,
		)
	default:
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
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
	return view, nil
}

func validOperationalDiagnosticQuery(query OperationalDiagnosticQuery) bool {
	if !query.authority.valid() {
		return false
	}
	switch query.lookupKind {
	case DiagnosticLookupDecision:
		return validOpaqueID(query.decisionID.value) && query.operationID == (OperationID{})
	case DiagnosticLookupOperation:
		return validOpaqueID(query.operationID.value) && query.decisionID == (DecisionID{})
	default:
		return false
	}
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
