package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

type postgresPrerequisiteKind int16

const (
	postgresPrerequisiteRuntimeBinding postgresPrerequisiteKind = iota + 1
	postgresPrerequisiteRuntimeView
	postgresPrerequisiteImmutableInputs
)

type postgresPrerequisiteAuditEventKind int16

const (
	postgresPrerequisiteAuditIntent postgresPrerequisiteAuditEventKind = iota + 1
	postgresPrerequisiteAuditAccepted
	postgresPrerequisiteAuditRejected
	postgresPrerequisiteAuditReconciliation
)

func (authority *PostgresAuthority) advancePostgresRuntimeBindingPrerequisite(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted || decision.Snapshot.State == RuntimeTerminal ||
		decision.Snapshot.State != RuntimeWaitingForLease && decision.Snapshot.State != RuntimeReconciling &&
			decision.Snapshot.State != RuntimePreparingPrerequisites {
		return decision, nil
	}
	state := decision.Snapshot.Readiness.RuntimeBinding.State
	if state == PrerequisitePending || state == PrerequisiteAccepted ||
		state == PrerequisiteReconciliationRequired {
		request := runtimeBindingValidationRequest(start)
		fact := unavailablePrerequisiteFact(request.OperationID, request.CanonicalRequestDigest)
		if authority.runtimeBindingValidator != nil {
			observation, observationErr := authority.runtimeBindingValidator.ValidateRuntimeBinding(ctx, request)
			var factErr error
			fact, factErr = prerequisiteFactFromObservation(
				request.OperationID, request.CanonicalRequestDigest, observation, observationErr,
			)
			if factErr != nil {
				return RuntimeDecision{}, factErr
			}
		} else if decision.Snapshot.State != RuntimePreparingPrerequisites {
			return decision, nil
		}
		canonical, canonicalErr := canonicalRuntimeBindingValidationRequest(request)
		if canonicalErr != nil {
			return RuntimeDecision{}, canonicalErr
		}
		if err := authority.persistPostgresPrerequisiteFact(
			ctx, start, postgresPrerequisiteRuntimeBinding, canonical, fact, RuntimeViewBindingSnapshot{},
		); err != nil {
			return RuntimeDecision{}, err
		}
	}

	snapshot, err := authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	if err != nil {
		return RuntimeDecision{}, err
	}
	decision.Snapshot = snapshot
	return decision, nil
}

func (authority *PostgresAuthority) advancePostgresRuntimeBindingRejection(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted || decision.Snapshot.State == RuntimeTerminal ||
		decision.Snapshot.State != RuntimeWaitingForLease && decision.Snapshot.State != RuntimeReconciling {
		return decision, nil
	}
	if decision.Snapshot.Readiness.RuntimeBinding.State == PrerequisiteRejected {
		terminal, terminalErr := authority.executePostgresPreLeaseTerminal(
			ctx, start, RuntimeRejected, PreLeaseTerminalImmutableBinding, postgresTimestamp(authority.now()),
		)
		if terminalErr != nil {
			return RuntimeDecision{}, terminalErr
		}
		decision.Snapshot = terminal.Snapshot
		return decision, nil
	}
	return decision, nil
}

func (authority *PostgresAuthority) advancePostgresPrerequisites(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	if obligation, required := runtimeViewTerminalObligationFor(
		decision.Snapshot.State, decision.Snapshot.Outcome,
		decision.Snapshot.Cleanup, decision.Snapshot.Lease,
	); required && decision.Snapshot.State == RuntimeTerminal {
		if start.Effect == EffectMutating &&
			(decision.Snapshot.Readiness.RuntimeView.State == PrerequisiteAccepted ||
				decision.Snapshot.Readiness.RuntimeView.State == PrerequisiteRejected) {
			if err := authority.repairPostgresRuntimeViewOpenDeliveryAck(
				ctx, start.RuntimeRunID, decision.Snapshot.Readiness.RuntimeView,
				decision.Snapshot.RuntimeViewBinding,
			); err != nil {
				return RuntimeDecision{}, err
			}
		}
		var err error
		switch obligation.Kind {
		case runtimeViewTerminalFence:
			err = authority.advancePostgresRuntimeViewFence(
				ctx, start.RuntimeRunID, obligation.FenceReason,
			)
		case runtimeViewTerminalDiscard:
			err = authority.advancePostgresRuntimeViewDiscard(
				ctx, start.RuntimeRunID, obligation.DiscardReason,
			)
		default:
			err = newError(ErrorIntegrityConflict)
		}
		if err != nil {
			return RuntimeDecision{}, err
		}
		return decision, nil
	}
	if decision.Snapshot.State == RuntimePreparingPrerequisites &&
		decision.Snapshot.Lease.AcquireStatus == LeaseGranted &&
		decision.Snapshot.Lease.Disposition == LeaseActive &&
		!authority.now().Before(decision.Snapshot.Deadline) {
		terminal, err := authority.executePostgresPostLeaseDeadline(ctx, start, decision.Snapshot)
		if err != nil {
			return RuntimeDecision{}, err
		}
		if err := authority.advancePostgresRuntimeViewDiscard(
			ctx, start.RuntimeRunID, taskworkspace.RuntimeViewRuntimeFailed,
		); err != nil {
			return RuntimeDecision{}, err
		}
		decision.Snapshot = terminal.Snapshot
		return decision, nil
	}
	if decision.Snapshot.State != RuntimePreparingPrerequisites ||
		decision.Snapshot.Lease.AcquireStatus != LeaseGranted || decision.Snapshot.Lease.Disposition != LeaseActive {
		return decision, nil
	}

	snapshot, err := authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	if err != nil {
		return RuntimeDecision{}, err
	}
	if snapshot.Readiness.RuntimeBinding.State == PrerequisiteRejected {
		return authority.finishPostgresPostLeasePrerequisiteRejection(
			ctx, start, decision, snapshot, postLeaseRuntimeBindingRejected,
			snapshot.Readiness.RuntimeBinding,
		)
	}
	if start.Effect == EffectMutating &&
		(snapshot.Readiness.RuntimeView.State == PrerequisiteAccepted ||
			snapshot.Readiness.RuntimeView.State == PrerequisiteRejected) {
		if err := authority.repairPostgresRuntimeViewOpenDeliveryAck(
			ctx, start.RuntimeRunID, snapshot.Readiness.RuntimeView, snapshot.RuntimeViewBinding,
		); err != nil {
			return RuntimeDecision{}, err
		}
	}
	if prerequisiteSatisfied(snapshot.Readiness.RuntimeBinding) && start.Effect == EffectMutating &&
		(snapshot.Readiness.RuntimeView.State == PrerequisitePending ||
			snapshot.Readiness.RuntimeView.State == PrerequisiteReconciliationRequired) &&
		authority.runtimeViewPrerequisite != nil {
		request, requestDigest, prepareErr := authority.preparePostgresRuntimeViewOpen(ctx, start)
		if prepareErr != nil {
			return RuntimeDecision{}, prepareErr
		}
		if err := authority.markPostgresPrerequisiteDeliveryAttempt(ctx, request.Operation.ID); err != nil {
			return RuntimeDecision{}, err
		}
		result, openErr := authority.runtimeViewPrerequisite.OpenRuntimeView(ctx, request)
		if openErr != nil {
			result, openErr = inspectOrReconcileRuntimeViewOpen(ctx, authority.runtimeViewPrerequisite, request, openErr)
		}
		fact, binding, factErr := runtimeViewFactFromResult(request, requestDigest, result, openErr)
		if factErr != nil {
			return RuntimeDecision{}, factErr
		}
		canonical, canonicalErr := json.Marshal(request)
		if canonicalErr != nil {
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
		if err := authority.persistPostgresPrerequisiteFact(
			ctx, start, postgresPrerequisiteRuntimeView, canonical, fact, binding,
		); err != nil {
			return RuntimeDecision{}, err
		}
		if authority.failAt(PersistenceFaultBeforeResponse) {
			return RuntimeDecision{}, newError(ErrorReconciliationRequired)
		}
		if fact.State == PrerequisiteAccepted || fact.State == PrerequisiteRejected {
			if err := authority.acknowledgePostgresPrerequisiteDelivery(ctx, request.Operation.ID); err != nil {
				return RuntimeDecision{}, err
			}
		}
		if fact.State == PrerequisiteAccepted {
			if err := authority.reconcilePostgresRuntimeViewAfterOpen(ctx, start); err != nil {
				return RuntimeDecision{}, err
			}
		}
	}

	snapshot, err = authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	if err != nil {
		return RuntimeDecision{}, err
	}
	if snapshot.Readiness.RuntimeView.State == PrerequisiteRejected {
		return authority.finishPostgresPostLeasePrerequisiteRejection(
			ctx, start, decision, snapshot, postLeaseRuntimeViewRejected,
			snapshot.Readiness.RuntimeView,
		)
	}
	if prerequisiteSatisfied(snapshot.Readiness.RuntimeBinding) &&
		runtimeViewPrerequisiteSatisfied(
			snapshot.Readiness.RuntimeView, snapshot.RuntimeViewBinding, snapshot.Lease,
		) &&
		(snapshot.Readiness.ImmutableInputs.State == PrerequisitePending ||
			snapshot.Readiness.ImmutableInputs.State == PrerequisiteReconciliationRequired) &&
		authority.immutableInputValidator != nil {
		request := immutableInputValidationRequest(start)
		observation, observationErr := authority.immutableInputValidator.ValidateImmutableInputs(ctx, request)
		fact, factErr := prerequisiteFactFromObservation(
			request.OperationID, request.CanonicalRequestDigest, observation, observationErr,
		)
		if factErr != nil {
			return RuntimeDecision{}, factErr
		}
		fact = nonEnumeratingImmutableInputFact(fact)
		canonical, canonicalErr := canonicalImmutableInputValidationRequest(request)
		if canonicalErr != nil {
			return RuntimeDecision{}, canonicalErr
		}
		if err := authority.persistPostgresPrerequisiteFact(
			ctx, start, postgresPrerequisiteImmutableInputs, canonical, fact, RuntimeViewBindingSnapshot{},
		); err != nil {
			return RuntimeDecision{}, err
		}
	}

	snapshot, err = authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	if err != nil {
		return RuntimeDecision{}, err
	}
	if snapshot.Readiness.ImmutableInputs.State == PrerequisiteRejected {
		return authority.finishPostgresPostLeasePrerequisiteRejection(
			ctx, start, decision, snapshot, postLeaseImmutableInputsRejected,
			snapshot.Readiness.ImmutableInputs,
		)
	}

	return authority.withCurrentPostgresSnapshot(ctx, start, decision)
}

func (authority *PostgresAuthority) reconcilePostgresRuntimeViewAfterOpen(
	ctx context.Context,
	start StartRuntimeRun,
) error {
	snapshot, err := authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		return err
	}
	if snapshot.State == RuntimeStopping && snapshot.Outcome == RuntimeOutcomeNone &&
		snapshot.Cleanup.FenceRuntimeView {
		reason, retained, retainedErr := authority.postgresRetainedRuntimeViewFenceReason(
			ctx, start.RuntimeRunID,
		)
		if retainedErr != nil {
			return retainedErr
		}
		if !retained {
			obligation, required := runtimeViewTerminalObligationFor(
				snapshot.State, snapshot.Outcome, snapshot.Cleanup, snapshot.Lease,
			)
			if !required || obligation.Kind != runtimeViewTerminalFence {
				return newError(ErrorIntegrityConflict)
			}
			reason = obligation.FenceReason
		}
		return authority.advancePostgresRuntimeViewFence(ctx, start.RuntimeRunID, reason)
	}
	if snapshot.State != RuntimeTerminal {
		return nil
	}
	obligation, required := runtimeViewTerminalObligationFor(
		snapshot.State, snapshot.Outcome, snapshot.Cleanup, snapshot.Lease,
	)
	if !required {
		return newError(ErrorIntegrityConflict)
	}
	switch obligation.Kind {
	case runtimeViewTerminalFence:
		return authority.advancePostgresRuntimeViewFence(
			ctx, start.RuntimeRunID, obligation.FenceReason,
		)
	case runtimeViewTerminalDiscard:
		return authority.advancePostgresRuntimeViewDiscard(
			ctx, start.RuntimeRunID, obligation.DiscardReason,
		)
	default:
		return newError(ErrorIntegrityConflict)
	}
}

func (authority *PostgresAuthority) finishPostgresPostLeasePrerequisiteRejection(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
	snapshot RuntimeSnapshot,
	cause postLeaseTerminalCause,
	fact PrerequisiteFact,
) (RuntimeDecision, error) {
	terminal, err := authority.executePostgresPostLeasePrerequisiteRejection(
		ctx, start, snapshot, cause, fact,
	)
	if err != nil {
		return RuntimeDecision{}, err
	}
	if err := authority.advancePostgresRuntimeViewDiscard(
		ctx, start.RuntimeRunID, cause.runtimeViewDiscardReason(),
	); err != nil {
		return RuntimeDecision{}, err
	}
	decision.Snapshot = terminal.Snapshot
	return decision, nil
}

func (authority *PostgresAuthority) repairPostgresRuntimeViewOpenDeliveryAck(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	fact PrerequisiteFact,
	binding RuntimeViewBindingSnapshot,
) error {
	if fact.State != PrerequisiteAccepted && fact.State != PrerequisiteRejected {
		return newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	var operationID string
	var requestDigest, canonical, factState []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, request_digest, canonical_request, fact_state
		FROM %s WHERE runtime_run_id=$1 AND prerequisite_kind=$2 FOR SHARE`,
		authority.table("runtime_execution_prerequisite_operations")), runtimeRunID.String(),
		postgresPrerequisiteRuntimeView).Scan(&operationID, &requestDigest, &canonical, &factState)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	var retainedFact postgresPrerequisiteFactState
	var open taskworkspace.OpenRuntimeViewRequest
	if json.Unmarshal(factState, &retainedFact) != nil || json.Unmarshal(canonical, &open) != nil ||
		operationID != fact.OperationID.String() || !bytes.Equal(requestDigest, fact.RequestDigest[:]) ||
		prerequisiteFactSnapshotFromPostgres(retainedFact) != fact ||
		open.Operation.ID != taskworkspace.OperationID(operationID) ||
		open.Operation.RequestDigest != open.CanonicalRequestDigest() ||
		digestFromTaskWorkspace(open.Operation.RequestDigest) != fact.RequestDigest ||
		fact.State == PrerequisiteAccepted && !validRuntimeViewOpenBinding(open, binding) ||
		fact.State == PrerequisiteRejected && binding != (RuntimeViewBindingSnapshot{}) {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
		acknowledged_at=coalesce(acknowledged_at,$2)
		WHERE operation_id=$3 AND disposition IN ($4,$1)`,
		authority.table("runtime_execution_prerequisite_outbox_delivery")),
		OutboxAcknowledged, postgresTimestamp(authority.now()), fact.OperationID.String(), OutboxPending)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

type canonicalCatalogExecutionBinding struct {
	TemplateLockID     string
	TemplateLockDigest string
	ClosureRootDigest  string
	SafetyEpoch        CatalogSafetyEpoch
}

type canonicalRuntimeBindingAuthorization struct {
	PersonalWorkspaceID         string
	TaskID                      string
	PhaseRunID                  string
	RuntimeRunID                string
	RuntimeBindingID            string
	RuntimeBindingDigest        string
	ExecutionLockDigest         string
	CapabilityContractDigest    string
	AllowedPlatformImagesDigest string
	ExecutorContractDigest      string
	OutputContractDigest        string
	EvidenceContractDigest      string
	ReleaseSafetyEpoch          ReleaseSafetyEpoch
	CatalogBinding              *canonicalCatalogExecutionBinding
}

func canonicalRuntimeBindingAuthorizationValue(
	authorization RuntimeBindingAuthorization,
) canonicalRuntimeBindingAuthorization {
	var catalog *canonicalCatalogExecutionBinding
	if authorization.CatalogBinding != nil {
		catalog = &canonicalCatalogExecutionBinding{
			TemplateLockID:     authorization.CatalogBinding.TemplateLockID.String(),
			TemplateLockDigest: authorization.CatalogBinding.TemplateLockDigest.String(),
			ClosureRootDigest:  authorization.CatalogBinding.ClosureRootDigest.String(),
			SafetyEpoch:        authorization.CatalogBinding.SafetyEpoch,
		}
	}
	return canonicalRuntimeBindingAuthorization{
		PersonalWorkspaceID: authorization.PersonalWorkspaceID.String(), TaskID: authorization.TaskID.String(),
		PhaseRunID: authorization.PhaseRunID.String(), RuntimeRunID: authorization.RuntimeRunID.String(),
		RuntimeBindingID:            authorization.RuntimeBindingID.String(),
		RuntimeBindingDigest:        authorization.RuntimeBindingDigest.String(),
		ExecutionLockDigest:         authorization.ExecutionLockDigest.String(),
		CapabilityContractDigest:    authorization.CapabilityContractDigest.String(),
		AllowedPlatformImagesDigest: authorization.AllowedPlatformImagesDigest.String(),
		ExecutorContractDigest:      authorization.ExecutorContractDigest.String(),
		OutputContractDigest:        authorization.OutputContractDigest.String(),
		EvidenceContractDigest:      authorization.EvidenceContractDigest.String(),
		ReleaseSafetyEpoch:          authorization.ReleaseSafetyEpoch, CatalogBinding: catalog,
	}
}

func canonicalRuntimeBindingValidationRequest(request RuntimeBindingValidationRequest) ([]byte, error) {
	encoded, err := json.Marshal(struct {
		OperationID            string
		CanonicalRequestDigest string
		Authorization          canonicalRuntimeBindingAuthorization
	}{
		OperationID: request.OperationID.String(), CanonicalRequestDigest: request.CanonicalRequestDigest.String(),
		Authorization: canonicalRuntimeBindingAuthorizationValue(request.Authorization),
	})
	if err != nil {
		return nil, newError(ErrorIntegrityConflict)
	}
	return encoded, nil
}

func canonicalImmutableInputValidationRequest(request ImmutableInputValidationRequest) ([]byte, error) {
	type canonicalImmutableInput struct {
		Identity string
		Digest   string
		Size     uint64
	}
	inputs := make([]canonicalImmutableInput, len(request.Inputs))
	for index, input := range request.Inputs {
		inputs[index].Identity = input.Identity.String()
		inputs[index].Digest = input.Digest.String()
		inputs[index].Size = input.SizeBytes
	}
	encoded, err := json.Marshal(struct {
		OperationID             string
		CanonicalRequestDigest  string
		Authorization           canonicalRuntimeBindingAuthorization
		ManifestIdentity        string
		ManifestSchema          SchemaVersion
		ManifestDigest          string
		ManifestTotalSize       uint64
		ManifestInputCount      uint64
		MaterializationEvidence string
		MaterializationDigest   string
		Inputs                  []canonicalImmutableInput
	}{
		OperationID: request.OperationID.String(), CanonicalRequestDigest: request.CanonicalRequestDigest.String(),
		Authorization:    canonicalRuntimeBindingAuthorizationValue(request.Authorization),
		ManifestIdentity: request.Manifest.Identity.String(), ManifestSchema: request.Manifest.SchemaVersion,
		ManifestDigest: request.Manifest.Digest.String(), ManifestTotalSize: request.Manifest.TotalSizeBytes,
		ManifestInputCount:      request.Manifest.InputCount,
		MaterializationEvidence: request.Manifest.MaterializationEvidenceID.String(),
		MaterializationDigest:   request.Manifest.MaterializationEvidenceDigest.String(), Inputs: inputs,
	})
	if err != nil {
		return nil, newError(ErrorIntegrityConflict)
	}
	return encoded, nil
}

func (authority *PostgresAuthority) preparePostgresRuntimeViewOpen(
	ctx context.Context,
	start StartRuntimeRun,
) (taskworkspace.OpenRuntimeViewRequest, Digest, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	if !authorized(record, start.PersonalWorkspaceID, start.Authority) ||
		record.acceptedStartDigest != start.CanonicalRequestDigest || record.fixture.State != RuntimePreparingPrerequisites ||
		record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive || start.RuntimeViewRequirement == nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}

	var retainedOperationID string
	var retainedDigest, retainedCanonical []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, request_digest, canonical_request
		FROM %s WHERE runtime_run_id=$1 AND prerequisite_kind=$2`,
		authority.table("runtime_execution_prerequisite_operations")), start.RuntimeRunID.String(),
		postgresPrerequisiteRuntimeView).Scan(&retainedOperationID, &retainedDigest, &retainedCanonical)
	if err == nil {
		var retained taskworkspace.OpenRuntimeViewRequest
		if json.Unmarshal(retainedCanonical, &retained) != nil {
			return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
		}
		retainedRequestDigest := digestFromTaskWorkspace(retained.Operation.RequestDigest)
		if retained.Operation.ID != taskworkspace.OperationID(retainedOperationID) ||
			retained.Operation.RequestDigest != retained.CanonicalRequestDigest() ||
			!bytes.Equal(retainedDigest, retainedRequestDigest[:]) {
			return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
		}
		requestDigest, digestErr := parseTaskWorkspaceDigest(retained.Operation.RequestDigest)
		if digestErr != nil {
			return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, digestErr
		}
		if err := tx.Commit(); err != nil {
			return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
		}
		return retained, requestDigest, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}

	request, requestDigest, requestErr := runtimeViewOpenRequest(start, record.lease)
	if requestErr != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, requestErr
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	fact := PrerequisiteFact{
		State:       PrerequisiteReconciliationRequired,
		OperationID: OperationID{value: string(request.Operation.ID)}, RequestDigest: requestDigest,
		Failure: PrerequisiteFailureDependencyUnavailable,
	}
	record.readiness.RuntimeView = fact
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	aggregate, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, newError(ErrorIntegrityConflict)
	}
	factState, _ := json.Marshal(postgresPrerequisiteFactFromSnapshot(fact))
	viewState, _ := json.Marshal(postgresRuntimeViewBindingState{})
	now := postgresTimestamp(authority.now())
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, prerequisite_kind, request_digest, canonical_request,
		fact_state, view_binding, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, authority.table("runtime_execution_prerequisite_operations")),
		request.Operation.ID, start.RuntimeRunID.String(), postgresPrerequisiteRuntimeView,
		requestDigest[:], canonical, factState, viewState, now); err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := authority.insertPostgresPrerequisiteAudit(
		ctx, tx, start.RuntimeRunID, postgresPrerequisiteRuntimeView,
		postgresPrerequisiteAuditIntent, fact, now,
	); err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, err
	}
	payloadDigest := digestBytes(canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, prerequisite_kind, request_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_prerequisite_outbox")),
		request.Operation.ID, start.RuntimeRunID.String(), postgresPrerequisiteRuntimeView,
		requestDigest[:], canonical, payloadDigest[:], now); err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_prerequisite_outbox_delivery")),
		request.Operation.ID, OutboxPending); err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET aggregate_state=$1, updated_at=$2
		WHERE runtime_run_id=$3 AND runtime_revision=$4`, authority.table("runtime_execution_runtimes")),
		aggregate, now, start.RuntimeRunID.String(), record.fixture.RuntimeRevision); err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return taskworkspace.OpenRuntimeViewRequest{}, Digest{}, normalizeRuntimePersistenceFailure(err)
	}
	return request, requestDigest, nil
}

func (authority *PostgresAuthority) persistPostgresPrerequisiteFact(
	ctx context.Context,
	start StartRuntimeRun,
	kind postgresPrerequisiteKind,
	canonical []byte,
	fact PrerequisiteFact,
	binding RuntimeViewBindingSnapshot,
) error {
	if !knownPrerequisiteFact(fact) || len(canonical) == 0 {
		return newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	lateRuntimeViewAcceptance := validPostgresLateRuntimeViewAcceptance(record, kind, fact, binding)
	if !authorized(record, start.PersonalWorkspaceID, start.Authority) ||
		(record.acceptedStartDigest != start.CanonicalRequestDigest ||
			!validPostgresPrerequisitePersistenceStage(record, kind)) &&
			!lateRuntimeViewAcceptance {
		return newError(ErrorIntegrityConflict)
	}
	if lateRuntimeViewAcceptance {
		if err := authority.validatePostgresRetainedStartForLateRuntimeView(ctx, tx, start); err != nil {
			return err
		}
	}
	var retainedOperationID string
	var retainedDigest, retainedCanonical, retainedFactState []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, request_digest, canonical_request, fact_state
		FROM %s WHERE runtime_run_id=$1 AND prerequisite_kind=$2 FOR UPDATE`,
		authority.table("runtime_execution_prerequisite_operations")), start.RuntimeRunID.String(), kind).
		Scan(&retainedOperationID, &retainedDigest, &retainedCanonical, &retainedFactState)
	if err == nil {
		if retainedOperationID != fact.OperationID.String() || !bytes.Equal(retainedDigest, fact.RequestDigest[:]) ||
			!bytes.Equal(retainedCanonical, canonical) {
			return newError(ErrorIntegrityConflict)
		}
		var retainedState postgresPrerequisiteFactState
		if json.Unmarshal(retainedFactState, &retainedState) != nil {
			return newError(ErrorIntegrityConflict)
		}
		retainedFact := prerequisiteFactSnapshotFromPostgres(retainedState)
		if retainedFact.State == PrerequisiteRejected ||
			kind != postgresPrerequisiteRuntimeBinding && retainedFact.State == PrerequisiteAccepted {
			if retainedFact != fact {
				return newError(ErrorIntegrityConflict)
			}
			return tx.Commit()
		}
		if kind == postgresPrerequisiteRuntimeBinding && retainedFact.State == PrerequisiteAccepted &&
			fact.State == PrerequisiteAccepted {
			if retainedFact != fact {
				return newError(ErrorIntegrityConflict)
			}
			return tx.Commit()
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return normalizeRuntimePersistenceFailure(err)
	}

	switch kind {
	case postgresPrerequisiteRuntimeBinding:
		record.readiness.RuntimeBinding = fact
	case postgresPrerequisiteRuntimeView:
		record.readiness.RuntimeView = fact
		if fact.State == PrerequisiteAccepted {
			record.runtimeViewBinding = binding
		}
	case postgresPrerequisiteImmutableInputs:
		record.readiness.ImmutableInputs = fact
	default:
		return newError(ErrorIntegrityConflict)
	}
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	factState, _ := json.Marshal(postgresPrerequisiteFactFromSnapshot(fact))
	viewState, _ := json.Marshal(postgresRuntimeViewBindingFromSnapshot(binding))
	now := postgresTimestamp(authority.now())
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			operation_id, runtime_run_id, prerequisite_kind, request_digest, canonical_request,
			fact_state, view_binding, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, authority.table("runtime_execution_prerequisite_operations")),
			fact.OperationID.String(), start.RuntimeRunID.String(), kind, fact.RequestDigest[:], canonical,
			factState, viewState, now); err != nil {
			return normalizeRuntimePersistenceFailure(err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET fact_state=$1, view_binding=$2, updated_at=$3
			WHERE operation_id=$4 AND request_digest=$5`, authority.table("runtime_execution_prerequisite_operations")),
			factState, viewState, now, fact.OperationID.String(), fact.RequestDigest[:]); err != nil {
			return normalizeRuntimePersistenceFailure(err)
		}
	}
	eventKind := postgresPrerequisiteAuditReconciliation
	if fact.State == PrerequisiteAccepted {
		eventKind = postgresPrerequisiteAuditAccepted
	} else if fact.State == PrerequisiteRejected {
		eventKind = postgresPrerequisiteAuditRejected
	}
	if err := authority.insertPostgresPrerequisiteAudit(ctx, tx, start.RuntimeRunID, kind, eventKind, fact, now); err != nil {
		return err
	}
	if lateRuntimeViewAcceptance {
		var open taskworkspace.OpenRuntimeViewRequest
		if json.Unmarshal(canonical, &open) != nil ||
			!validRuntimeViewOpenBinding(open, binding) {
			return newError(ErrorIntegrityConflict)
		}
		obligation, valid := runtimeViewTerminalObligationFor(
			record.fixture.State, record.fixture.Outcome, record.cleanup, record.lease,
		)
		if !valid {
			return newError(ErrorIntegrityConflict)
		}
		retained, retainErr := retainRuntimeViewTerminalObligation(open, binding, obligation)
		if retainErr != nil {
			return retainErr
		}
		var operationID taskworkspace.OperationID
		var terminalCanonical []byte
		var encodeErr error
		switch retained.Kind {
		case runtimeViewTerminalFence:
			operationID = retained.FenceRequest.Operation.ID
			terminalCanonical, encodeErr = json.Marshal(retained.FenceRequest)
		case runtimeViewTerminalDiscard:
			operationID = retained.DiscardRequest.Operation.ID
			terminalCanonical, encodeErr = json.Marshal(retained.DiscardRequest)
		default:
			return newError(ErrorIntegrityConflict)
		}
		if encodeErr != nil {
			return newError(ErrorIntegrityConflict)
		}
		shouldDeliver, retainErr := authority.retainPostgresRuntimeViewTerminal(
			ctx, tx, start.RuntimeRunID, retained.Kind,
			operationID, retained.RequestDigest, terminalCanonical,
		)
		if retainErr != nil {
			return retainErr
		}
		if !shouldDeliver {
			return newError(ErrorIntegrityConflict)
		}
	}
	aggregate, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET aggregate_state=$1, updated_at=$2
		WHERE runtime_run_id=$3 AND runtime_revision=$4`, authority.table("runtime_execution_runtimes")),
		aggregate, now, start.RuntimeRunID.String(), record.fixture.RuntimeRevision)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) validatePostgresRetainedStartForLateRuntimeView(
	ctx context.Context,
	tx *sql.Tx,
	start StartRuntimeRun,
) error {
	var workspaceID string
	var commandKind int16
	var requestDigest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, command_kind,
		canonical_request_digest FROM %s WHERE runtime_run_id=$1 AND operation_id=$2`,
		authority.table("runtime_execution_requests")),
		start.RuntimeRunID.String(), start.OperationID.String()).Scan(
		&workspaceID, &commandKind, &requestDigest,
	)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if workspaceID != start.PersonalWorkspaceID.String() ||
		commandKind != int16(CommandStartRuntimeRun) ||
		!bytes.Equal(requestDigest, start.CanonicalRequestDigest[:]) {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func validPostgresLateRuntimeViewAcceptance(
	record *runtimeRecord,
	kind postgresPrerequisiteKind,
	fact PrerequisiteFact,
	binding RuntimeViewBindingSnapshot,
) bool {
	if kind != postgresPrerequisiteRuntimeView ||
		fact.State != PrerequisiteAccepted || binding == (RuntimeViewBindingSnapshot{}) ||
		record.runtimeViewBinding != (RuntimeViewBindingSnapshot{}) ||
		record.readiness.RuntimeView.State != PrerequisiteReconciliationRequired ||
		record.readiness.RuntimeView.OperationID != fact.OperationID ||
		record.readiness.RuntimeView.RequestDigest != fact.RequestDigest {
		return false
	}
	terminal := record.fixture.State == RuntimeTerminal &&
		(record.fixture.Outcome == RuntimeCancelled || record.fixture.Outcome == RuntimeRejected ||
			record.fixture.Outcome == RuntimeTimedOut || record.fixture.Outcome == RuntimeFailed)
	stopping := record.fixture.State == RuntimeStopping && record.fixture.Outcome == RuntimeOutcomeNone &&
		record.cleanup.FenceRuntimeView && record.lease.AcquireStatus == LeaseGranted &&
		(record.lease.Disposition == LeaseRevoked || record.lease.Disposition == LeaseExpired)
	return terminal || stopping
}

func validPostgresPrerequisitePersistenceStage(
	record *runtimeRecord,
	kind postgresPrerequisiteKind,
) bool {
	postLease := record.fixture.State == RuntimePreparingPrerequisites &&
		record.lease.AcquireStatus == LeaseGranted && record.lease.Disposition == LeaseActive
	if kind != postgresPrerequisiteRuntimeBinding {
		return postLease
	}
	preLease := (record.fixture.State == RuntimeWaitingForLease || record.fixture.State == RuntimeReconciling) &&
		(record.lease.AcquireStatus == LeaseAcquirePending ||
			record.lease.AcquireStatus == LeaseAcquireReconciliationRequired) &&
		record.lease.Disposition == LeaseDispositionNone
	return preLease || postLease
}

func (authority *PostgresAuthority) insertPostgresPrerequisiteAudit(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
	kind postgresPrerequisiteKind,
	eventKind postgresPrerequisiteAuditEventKind,
	fact PrerequisiteFact,
	recordedAt time.Time,
) error {
	factState, err := json.Marshal(postgresPrerequisiteFactFromSnapshot(fact))
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	auditDigest := digestBytes(append(append([]byte(nil), fact.RequestDigest[:]...), factState...))
	auditID := fmt.Sprintf("prerequisite-audit-%s-%d", fact.OperationID.String(), eventKind)
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_id, operation_id, runtime_run_id, prerequisite_kind, event_kind,
		request_digest, fact_state, canonical_digest, recorded_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (operation_id, event_kind) DO NOTHING`,
		authority.table("runtime_execution_prerequisite_audit")), auditID, fact.OperationID.String(),
		runtimeRunID.String(), kind, eventKind, fact.RequestDigest[:], factState, auditDigest[:], recordedAt)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return newError(ErrorIntegrityConflict)
	}
	if rows == 1 {
		return nil
	}
	var retainedAuditID, retainedRuntimeRunID string
	var retainedKind postgresPrerequisiteKind
	var retainedRequestDigest, retainedFactState, retainedCanonicalDigest []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT audit_id, runtime_run_id, prerequisite_kind,
		request_digest, fact_state, canonical_digest FROM %s
		WHERE operation_id=$1 AND event_kind=$2`, authority.table("runtime_execution_prerequisite_audit")),
		fact.OperationID.String(), eventKind).Scan(
		&retainedAuditID, &retainedRuntimeRunID, &retainedKind, &retainedRequestDigest,
		&retainedFactState, &retainedCanonicalDigest,
	)
	var retainedState postgresPrerequisiteFactState
	if err != nil || json.Unmarshal(retainedFactState, &retainedState) != nil ||
		retainedAuditID != auditID || retainedRuntimeRunID != runtimeRunID.String() || retainedKind != kind ||
		!bytes.Equal(retainedRequestDigest, fact.RequestDigest[:]) ||
		prerequisiteFactSnapshotFromPostgres(retainedState) != fact ||
		!bytes.Equal(retainedCanonicalDigest, auditDigest[:]) {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) markPostgresPrerequisiteDeliveryAttempt(
	ctx context.Context,
	operationID taskworkspace.OperationID,
) error {
	result, err := authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET delivery_count=delivery_count+1,
		last_attempt_at=$1 WHERE operation_id=$2 AND disposition=$3`,
		authority.table("runtime_execution_prerequisite_outbox_delivery")),
		postgresTimestamp(authority.now()), operationID, OutboxPending)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) acknowledgePostgresPrerequisiteDelivery(
	ctx context.Context,
	operationID taskworkspace.OperationID,
) error {
	result, err := authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
		acknowledged_at=$2 WHERE operation_id=$3 AND disposition=$4`,
		authority.table("runtime_execution_prerequisite_outbox_delivery")), OutboxAcknowledged,
		postgresTimestamp(authority.now()), operationID, OutboxPending)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows > 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}
