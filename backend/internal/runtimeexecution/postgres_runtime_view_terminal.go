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

type postgresRuntimeViewTerminalAuditEventKind int16

const (
	postgresRuntimeViewTerminalAuditIntent postgresRuntimeViewTerminalAuditEventKind = iota + 1
	postgresRuntimeViewTerminalAuditAccepted
	postgresRuntimeViewTerminalAuditRejected
	postgresRuntimeViewTerminalAuditReconciliation
)

func (authority *PostgresAuthority) advancePostgresRuntimeViewFence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewFenceReason,
) error {
	request, shouldDeliver, err := authority.preparePostgresRuntimeViewFence(ctx, runtimeRunID, reason)
	if err != nil || !shouldDeliver {
		return err
	}
	if err := authority.markPostgresRuntimeViewTerminalDeliveryAttempt(ctx, request.Operation.ID); err != nil {
		return err
	}
	result, terminalErr := authority.runtimeViewPrerequisite.FenceRuntimeView(ctx, request)
	if terminalErr != nil {
		result, terminalErr = inspectOrReconcileRuntimeViewFence(
			ctx, authority.runtimeViewPrerequisite, request, terminalErr,
		)
	}
	state, errorCode, stateErr := runtimeViewFenceTerminalState(request, result, terminalErr)
	if stateErr != nil {
		return stateErr
	}
	if err := authority.persistPostgresRuntimeViewTerminalState(
		ctx, runtimeRunID, runtimeViewTerminalFence, request.Operation.ID,
		digestFromTaskWorkspace(request.Operation.RequestDigest), state, errorCode,
	); err != nil {
		return err
	}
	if authority.failAt(PersistenceFaultBeforeResponse) {
		return newError(ErrorReconciliationRequired)
	}
	if state == runtimeViewTerminalAccepted || state == runtimeViewTerminalRejected {
		return authority.acknowledgePostgresRuntimeViewTerminalDelivery(ctx, request.Operation.ID)
	}
	return nil
}

func (authority *PostgresAuthority) advancePostgresRuntimeViewDiscard(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewDiscardReason,
) error {
	request, shouldDeliver, err := authority.preparePostgresRuntimeViewDiscard(ctx, runtimeRunID, reason)
	if err != nil || !shouldDeliver {
		return err
	}
	if err := authority.markPostgresRuntimeViewTerminalDeliveryAttempt(ctx, request.Operation.ID); err != nil {
		return err
	}
	result, terminalErr := authority.runtimeViewPrerequisite.DiscardRuntimeView(ctx, request)
	if terminalErr != nil {
		result, terminalErr = inspectOrReconcileRuntimeViewDiscard(
			ctx, authority.runtimeViewPrerequisite, request, terminalErr,
		)
	}
	state, errorCode, stateErr := runtimeViewDiscardTerminalState(request, result, terminalErr)
	if stateErr != nil {
		return stateErr
	}
	if err := authority.persistPostgresRuntimeViewTerminalState(
		ctx, runtimeRunID, runtimeViewTerminalDiscard, request.Operation.ID,
		digestFromTaskWorkspace(request.Operation.RequestDigest), state, errorCode,
	); err != nil {
		return err
	}
	if authority.failAt(PersistenceFaultBeforeResponse) {
		return newError(ErrorReconciliationRequired)
	}
	if state == runtimeViewTerminalAccepted || state == runtimeViewTerminalRejected {
		return authority.acknowledgePostgresRuntimeViewTerminalDelivery(ctx, request.Operation.ID)
	}
	return nil
}

func (authority *PostgresAuthority) postgresRuntimeViewTerminalRetained(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
) (bool, error) {
	var retained bool
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT EXISTS (
		SELECT 1 FROM %s WHERE runtime_run_id=$1 AND terminal_kind=$2
	)`, authority.table("runtime_execution_runtime_view_terminal_operations")),
		runtimeRunID.String(), runtimeViewTerminalFence).Scan(&retained); err != nil {
		return false, normalizeRuntimePersistenceFailure(err)
	}
	return retained, nil
}

func (authority *PostgresAuthority) postgresRetainedRuntimeViewFenceReason(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
) (taskworkspace.RuntimeViewFenceReason, bool, error) {
	var kind int16
	var canonical []byte
	err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT terminal_kind, canonical_request
		FROM %s WHERE runtime_run_id=$1`,
		authority.table("runtime_execution_runtime_view_terminal_operations")),
		runtimeRunID.String()).Scan(&kind, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return taskworkspace.RuntimeViewFenceReason(""), false, nil
	}
	if err != nil {
		return taskworkspace.RuntimeViewFenceReason(""), false, normalizeRuntimePersistenceFailure(err)
	}
	var request taskworkspace.FenceRuntimeViewRequest
	if kind != int16(runtimeViewTerminalFence) || json.Unmarshal(canonical, &request) != nil ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return taskworkspace.RuntimeViewFenceReason(""), false, newError(ErrorIntegrityConflict)
	}
	return request.Reason, true, nil
}

func (authority *PostgresAuthority) preparePostgresRuntimeViewFence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewFenceReason,
) (taskworkspace.FenceRuntimeViewRequest, bool, error) {
	if authority.runtimeViewPrerequisite == nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, nil
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, open, err := authority.loadPostgresRuntimeViewOpenForTerminal(ctx, tx, runtimeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return taskworkspace.FenceRuntimeViewRequest{}, false, nil
	}
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, err
	}
	request, requestDigest, err := runtimeViewFenceRequest(open, record.runtimeViewBinding, reason)
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
	}
	shouldDeliver, err := authority.retainPostgresRuntimeViewTerminal(
		ctx, tx, runtimeRunID, runtimeViewTerminalFence, request.Operation.ID,
		requestDigest, canonical,
	)
	if err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, err
	}
	if err := authority.commitPostgresRuntimeViewTerminalIntent(tx); err != nil {
		return taskworkspace.FenceRuntimeViewRequest{}, false, err
	}
	return request, shouldDeliver, nil
}

func (authority *PostgresAuthority) preparePostgresRuntimeViewDiscard(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	reason taskworkspace.RuntimeViewDiscardReason,
) (taskworkspace.DiscardRuntimeViewRequest, bool, error) {
	if authority.runtimeViewPrerequisite == nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, nil
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, open, err := authority.loadPostgresRuntimeViewOpenForTerminal(ctx, tx, runtimeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, nil
	}
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, err
	}
	request, requestDigest, err := runtimeViewDiscardRequest(open, record.runtimeViewBinding, reason)
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, newError(ErrorIntegrityConflict)
	}
	shouldDeliver, err := authority.retainPostgresRuntimeViewTerminal(
		ctx, tx, runtimeRunID, runtimeViewTerminalDiscard, request.Operation.ID,
		requestDigest, canonical,
	)
	if err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, err
	}
	if err := authority.commitPostgresRuntimeViewTerminalIntent(tx); err != nil {
		return taskworkspace.DiscardRuntimeViewRequest{}, false, err
	}
	return request, shouldDeliver, nil
}

func (authority *PostgresAuthority) loadPostgresRuntimeViewOpenForTerminal(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
) (*runtimeRecord, taskworkspace.OpenRuntimeViewRequest, error) {
	record, err := authority.loadRuntimeForUpdate(ctx, tx, runtimeRunID)
	if err != nil {
		return nil, taskworkspace.OpenRuntimeViewRequest{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.readiness.RuntimeView.State != PrerequisiteAccepted ||
		record.runtimeViewBinding == (RuntimeViewBindingSnapshot{}) {
		return nil, taskworkspace.OpenRuntimeViewRequest{}, sql.ErrNoRows
	}
	var operationID string
	var requestDigest, canonical []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, request_digest, canonical_request
		FROM %s WHERE runtime_run_id=$1 AND prerequisite_kind=$2 FOR SHARE`,
		authority.table("runtime_execution_prerequisite_operations")),
		runtimeRunID.String(), postgresPrerequisiteRuntimeView).Scan(&operationID, &requestDigest, &canonical)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, taskworkspace.OpenRuntimeViewRequest{}, newError(ErrorIntegrityConflict)
		}
		return nil, taskworkspace.OpenRuntimeViewRequest{}, normalizeRuntimePersistenceFailure(err)
	}
	var open taskworkspace.OpenRuntimeViewRequest
	if json.Unmarshal(canonical, &open) != nil || open.Operation.ID != taskworkspace.OperationID(operationID) ||
		open.Operation.RequestDigest != open.CanonicalRequestDigest() ||
		!bytes.Equal(requestDigest, record.runtimeViewBinding.OpenRequestDigest[:]) ||
		!validRuntimeViewOpenBinding(open, record.runtimeViewBinding) {
		return nil, taskworkspace.OpenRuntimeViewRequest{}, newError(ErrorIntegrityConflict)
	}
	return record, open, nil
}

func (authority *PostgresAuthority) retainPostgresRuntimeViewTerminal(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
	kind runtimeViewTerminalKind,
	operationID taskworkspace.OperationID,
	requestDigest Digest,
	canonical []byte,
) (bool, error) {
	var retainedOperationID string
	var retainedKind int16
	var retainedDigest, retainedCanonical []byte
	var retainedState int16
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, terminal_kind, request_digest,
		canonical_request, terminal_state FROM %s WHERE runtime_run_id=$1 FOR UPDATE`,
		authority.table("runtime_execution_runtime_view_terminal_operations")), runtimeRunID.String()).Scan(
		&retainedOperationID, &retainedKind, &retainedDigest, &retainedCanonical, &retainedState,
	)
	if err == nil {
		if retainedOperationID != string(operationID) || retainedKind != int16(kind) ||
			!bytes.Equal(retainedDigest, requestDigest[:]) || !bytes.Equal(retainedCanonical, canonical) ||
			retainedState < int16(runtimeViewTerminalPending) ||
			retainedState > int16(runtimeViewTerminalReconciliationRequired) {
			return false, newError(ErrorIntegrityConflict)
		}
		if retainedState == int16(runtimeViewTerminalAccepted) || retainedState == int16(runtimeViewTerminalRejected) {
			if err := authority.repairPostgresRuntimeViewTerminalDeliveryAck(ctx, tx, operationID); err != nil {
				return false, err
			}
			return false, nil
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, normalizeRuntimePersistenceFailure(err)
	}
	now := postgresTimestamp(authority.now())
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, terminal_kind, request_digest, canonical_request,
		terminal_state, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		authority.table("runtime_execution_runtime_view_terminal_operations")),
		operationID, runtimeRunID.String(), kind, requestDigest[:], canonical,
		runtimeViewTerminalPending, now); err != nil {
		return false, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return false, newError(ErrorDependencyUnavailable)
	}
	if err := authority.insertPostgresRuntimeViewTerminalAudit(
		ctx, tx, runtimeRunID, kind, operationID, requestDigest,
		postgresRuntimeViewTerminalAuditIntent, runtimeViewTerminalPending, "", now,
	); err != nil {
		return false, err
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) ||
		authority.failAt(PersistenceFaultBeforeOutbox) {
		return false, newError(ErrorDependencyUnavailable)
	}
	payloadDigest := digestBytes(canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, terminal_kind, request_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_runtime_view_terminal_outbox")),
		operationID, runtimeRunID.String(), kind, requestDigest[:], canonical, payloadDigest[:], now); err != nil {
		return false, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_runtime_view_terminal_outbox_delivery")),
		operationID, OutboxPending); err != nil {
		return false, normalizeRuntimePersistenceFailure(err)
	}
	return true, nil
}

func (authority *PostgresAuthority) commitPostgresRuntimeViewTerminalIntent(tx *sql.Tx) error {
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterCommit) {
		return newError(ErrorReconciliationRequired)
	}
	return nil
}

func (authority *PostgresAuthority) repairPostgresRuntimeViewTerminalDeliveryAck(
	ctx context.Context,
	tx *sql.Tx,
	operationID taskworkspace.OperationID,
) error {
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
		acknowledged_at=coalesce(acknowledged_at,$2)
		WHERE operation_id=$3 AND disposition IN ($4,$1)`,
		authority.table("runtime_execution_runtime_view_terminal_outbox_delivery")),
		OutboxAcknowledged, postgresTimestamp(authority.now()), operationID, OutboxPending)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) persistPostgresRuntimeViewTerminalState(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	kind runtimeViewTerminalKind,
	operationID taskworkspace.OperationID,
	requestDigest Digest,
	state runtimeViewTerminalState,
	errorCode taskworkspace.ErrorCode,
) error {
	if state < runtimeViewTerminalAccepted || state > runtimeViewTerminalReconciliationRequired {
		return newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	var retainedRuntimeRunID string
	var retainedKind, retainedState int16
	var retainedDigest []byte
	var retainedErrorCode string
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, terminal_kind, request_digest,
		terminal_state, error_code FROM %s WHERE operation_id=$1 FOR UPDATE`,
		authority.table("runtime_execution_runtime_view_terminal_operations")), operationID).Scan(
		&retainedRuntimeRunID, &retainedKind, &retainedDigest, &retainedState, &retainedErrorCode,
	)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if retainedRuntimeRunID != runtimeRunID.String() || retainedKind != int16(kind) ||
		!bytes.Equal(retainedDigest, requestDigest[:]) {
		return newError(ErrorIntegrityConflict)
	}
	if retainedState == int16(runtimeViewTerminalAccepted) || retainedState == int16(runtimeViewTerminalRejected) {
		if retainedState != int16(state) || retainedErrorCode != string(errorCode) {
			return newError(ErrorIntegrityConflict)
		}
		return tx.Commit()
	}
	now := postgresTimestamp(authority.now())
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET terminal_state=$1,
		error_code=$2, updated_at=$3 WHERE operation_id=$4 AND request_digest=$5`,
		authority.table("runtime_execution_runtime_view_terminal_operations")),
		state, string(errorCode), now, operationID, requestDigest[:])
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	eventKind := postgresRuntimeViewTerminalAuditReconciliation
	if state == runtimeViewTerminalAccepted {
		eventKind = postgresRuntimeViewTerminalAuditAccepted
	} else if state == runtimeViewTerminalRejected {
		eventKind = postgresRuntimeViewTerminalAuditRejected
	}
	if err := authority.insertPostgresRuntimeViewTerminalAudit(
		ctx, tx, runtimeRunID, kind, operationID, requestDigest,
		eventKind, state, errorCode, now,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) insertPostgresRuntimeViewTerminalAudit(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
	kind runtimeViewTerminalKind,
	operationID taskworkspace.OperationID,
	requestDigest Digest,
	eventKind postgresRuntimeViewTerminalAuditEventKind,
	state runtimeViewTerminalState,
	errorCode taskworkspace.ErrorCode,
	recordedAt time.Time,
) error {
	auditState, err := json.Marshal(struct {
		RuntimeRunID  string
		Kind          runtimeViewTerminalKind
		OperationID   string
		RequestDigest string
		EventKind     postgresRuntimeViewTerminalAuditEventKind
		State         runtimeViewTerminalState
		ErrorCode     taskworkspace.ErrorCode
	}{
		RuntimeRunID: runtimeRunID.String(), Kind: kind, OperationID: string(operationID),
		RequestDigest: requestDigest.String(), EventKind: eventKind, State: state, ErrorCode: errorCode,
	})
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	auditDigest := digestBytes(auditState)
	auditID := fmt.Sprintf("runtime-view-terminal-audit-%s-%d", operationID, eventKind)
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_id, operation_id, runtime_run_id, terminal_kind, event_kind,
		request_digest, terminal_state, error_code, canonical_digest, recorded_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	ON CONFLICT (operation_id, event_kind) DO NOTHING`,
		authority.table("runtime_execution_runtime_view_terminal_audit")),
		auditID, operationID, runtimeRunID.String(), kind, eventKind, requestDigest[:],
		state, string(errorCode), auditDigest[:], recordedAt)
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
	var retainedRuntimeRunID string
	var retainedKind, retainedState int16
	var retainedDigest, retainedCanonicalDigest []byte
	var retainedErrorCode string
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, terminal_kind, request_digest,
		terminal_state, error_code, canonical_digest FROM %s WHERE operation_id=$1 AND event_kind=$2`,
		authority.table("runtime_execution_runtime_view_terminal_audit")), operationID, eventKind).Scan(
		&retainedRuntimeRunID, &retainedKind, &retainedDigest, &retainedState,
		&retainedErrorCode, &retainedCanonicalDigest,
	)
	if err != nil || retainedRuntimeRunID != runtimeRunID.String() || retainedKind != int16(kind) ||
		!bytes.Equal(retainedDigest, requestDigest[:]) || retainedState != int16(state) ||
		retainedErrorCode != string(errorCode) || !bytes.Equal(retainedCanonicalDigest, auditDigest[:]) {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) markPostgresRuntimeViewTerminalDeliveryAttempt(
	ctx context.Context,
	operationID taskworkspace.OperationID,
) error {
	result, err := authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET delivery_count=delivery_count+1,
		last_attempt_at=$1 WHERE operation_id=$2 AND disposition=$3`,
		authority.table("runtime_execution_runtime_view_terminal_outbox_delivery")),
		postgresTimestamp(authority.now()), operationID, OutboxPending)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) acknowledgePostgresRuntimeViewTerminalDelivery(
	ctx context.Context,
	operationID taskworkspace.OperationID,
) error {
	result, err := authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
		acknowledged_at=$2 WHERE operation_id=$3 AND disposition=$4`,
		authority.table("runtime_execution_runtime_view_terminal_outbox_delivery")),
		OutboxAcknowledged, postgresTimestamp(authority.now()), operationID, OutboxPending)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows > 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}
