package scheduler

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
)

func (authority *PostgresAuthority) ApplyRuntimeFencedOrTerminal(
	ctx context.Context,
	evidence runtimeexecution.RuntimeFencedOrTerminalEvidence,
) error {
	if !validRuntimeFencedEvidence(evidence) {
		return newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	grantState, err := authority.lockAndValidateTerminalEvidence(ctx, tx, evidence)
	if err != nil {
		return err
	}
	if grantState != GrantBound && grantState != GrantTerminalNoLease && grantState != GrantReleased {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=CURRENT_TIMESTAMP
		WHERE admission_grant_id=$2 AND grant_generation=$3 AND state=$4`,
		authority.table("scheduler_logical_reservations")), ReservationReleased,
		evidence.AdmissionGrantID.String(), evidence.GrantGeneration, ReservationBound)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 0 && rows != 4 {
		return newError(ErrorIntegrityConflict)
	}
	var nodeState ReservationState
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT state FROM %s WHERE admission_grant_id=$1`,
		authority.table("scheduler_node_reservations")), evidence.AdmissionGrantID.String()).Scan(&nodeState); err != nil {
		return newError(ErrorIntegrityConflict)
	}
	if nodeState == ReservationReleased {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=CURRENT_TIMESTAMP
			WHERE admission_grant_id=$2 AND generation=$3 AND state IN ($4,$5)`,
			authority.table("scheduler_admission_grants")), GrantReleased,
			evidence.AdmissionGrantID.String(), evidence.GrantGeneration, GrantBound, GrantTerminalNoLease); err != nil {
			return newError(ErrorDependencyUnavailable)
		}
	}
	if err := tx.Commit(); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	return nil
}

func (authority *PostgresAuthority) ApplyNoLeasePhysicalDisposition(
	ctx context.Context,
	evidence runtimeexecution.NoLeasePhysicalDispositionEvidence,
) error {
	base := runtimeexecution.RuntimeFencedOrTerminalEvidence{
		WorkItemID: evidence.WorkItemID, AdmissionGrantID: evidence.AdmissionGrantID,
		GrantGeneration: evidence.GrantGeneration, RuntimeRunID: evidence.RuntimeRunID,
		StartOperationID: evidence.StartOperationID, StartDigest: evidence.StartDigest,
		TerminalDecisionID: evidence.TerminalDecisionID, RuntimeFence: evidence.RuntimeFence,
		LeaseAcquireOperationID: evidence.LeaseAcquireOperationID, LeaseAcquireDigest: evidence.LeaseAcquireDigest,
	}
	if !validRuntimeFencedEvidence(base) || evidence.ExecutionNodeID.String() == "" ||
		evidence.NodeCapacityGeneration == 0 {
		return newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	grantState, err := authority.lockAndValidateTerminalEvidence(ctx, tx, base)
	if err != nil {
		return err
	}
	if grantState != GrantBound && grantState != GrantTerminalNoLease && grantState != GrantReleased {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=CURRENT_TIMESTAMP
		WHERE admission_grant_id=$2 AND execution_node_id=$3 AND node_capacity_generation=$4
		AND grant_generation=$5 AND state=$6`, authority.table("scheduler_node_reservations")),
		ReservationReleased, evidence.AdmissionGrantID.String(), evidence.ExecutionNodeID.String(),
		evidence.NodeCapacityGeneration, evidence.GrantGeneration, ReservationBound)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows > 1 {
		return newError(ErrorIntegrityConflict)
	}
	if rows == 0 {
		var retained ReservationState
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT state FROM %s WHERE admission_grant_id=$1
			AND execution_node_id=$2 AND node_capacity_generation=$3 AND grant_generation=$4`,
			authority.table("scheduler_node_reservations")), evidence.AdmissionGrantID.String(),
			evidence.ExecutionNodeID.String(), evidence.NodeCapacityGeneration, evidence.GrantGeneration).Scan(&retained); err != nil ||
			retained != ReservationReleased {
			return newError(ErrorIntegrityConflict)
		}
	}
	var logicalMin, logicalMax ReservationState
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT min(state), max(state) FROM %s
		WHERE admission_grant_id=$1`, authority.table("scheduler_logical_reservations")),
		evidence.AdmissionGrantID.String()).Scan(&logicalMin, &logicalMax); err != nil || logicalMin != logicalMax {
		return newError(ErrorIntegrityConflict)
	}
	nextGrantState := GrantTerminalNoLease
	if logicalMin == ReservationReleased {
		nextGrantState = GrantReleased
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=CURRENT_TIMESTAMP
		WHERE admission_grant_id=$2 AND generation=$3 AND state IN ($4,$5,$6)`,
		authority.table("scheduler_admission_grants")), nextGrantState,
		evidence.AdmissionGrantID.String(), evidence.GrantGeneration, GrantBound,
		GrantTerminalNoLease, GrantReleased); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	return nil
}

func validRuntimeFencedEvidence(evidence runtimeexecution.RuntimeFencedOrTerminalEvidence) bool {
	return evidence.WorkItemID.String() != "" && evidence.AdmissionGrantID.String() != "" &&
		evidence.GrantGeneration > 0 && evidence.RuntimeRunID.String() != "" &&
		evidence.StartOperationID.String() != "" && evidence.StartDigest != (runtimeexecution.Digest{}) &&
		evidence.TerminalDecisionID.String() != "" && evidence.RuntimeFence > 0 &&
		evidence.LeaseAcquireOperationID.String() != "" && evidence.LeaseAcquireDigest != (runtimeexecution.Digest{})
}

func (authority *PostgresAuthority) lockAndValidateTerminalEvidence(
	ctx context.Context,
	tx *sql.Tx,
	evidence runtimeexecution.RuntimeFencedOrTerminalEvidence,
) (GrantState, error) {
	var state GrantState
	var operationID, runtimeRunID, boundDecisionID, leaseOperationID string
	var digest, leaseDigest []byte
	var boundFence runtimeexecution.RuntimeFence
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT state, operation_id, payload_digest,
		runtime_run_id, bound_decision_id, bound_runtime_fence,
		lease_acquire_operation_id, lease_acquire_digest FROM %s
		WHERE admission_grant_id=$1 AND work_item_id=$2 AND generation=$3 FOR UPDATE`,
		authority.table("scheduler_admission_grants")), evidence.AdmissionGrantID.String(),
		evidence.WorkItemID.String(), evidence.GrantGeneration).Scan(&state, &operationID, &digest,
		&runtimeRunID, &boundDecisionID, &boundFence, &leaseOperationID, &leaseDigest)
	if err != nil || operationID != evidence.StartOperationID.String() ||
		!bytes.Equal(digest, evidence.StartDigest[:]) || runtimeRunID != evidence.RuntimeRunID.String() ||
		boundDecisionID == "" || evidence.RuntimeFence <= boundFence ||
		leaseOperationID != evidence.LeaseAcquireOperationID.String() ||
		!bytes.Equal(leaseDigest, evidence.LeaseAcquireDigest[:]) {
		return 0, newError(ErrorIntegrityConflict)
	}
	return state, nil
}
