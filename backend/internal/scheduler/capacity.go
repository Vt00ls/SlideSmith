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
	if grantState != GrantBound && grantState != GrantLeaseAttached &&
		grantState != GrantTerminalNoLease && grantState != GrantReleased {
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
		TerminalDecisionID: evidence.TerminalDecisionID, RuntimeRevision: evidence.RuntimeRevision,
		RuntimeFence: evidence.RuntimeFence, SchedulerEpoch: evidence.SchedulerEpoch,
		PolicyVersion:           evidence.PolicyVersion,
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

func (authority *PostgresAuthority) ApplyPhysicalCapacityReleaseReady(
	ctx context.Context,
	evidence runtimeexecution.PhysicalCapacityReleaseReadyEvidence,
) error {
	if !validPhysicalReleaseEvidence(evidence) {
		return newError(ErrorInvalidRequest)
	}
	if authority.runtimePhysicalReleaseEvidenceFunction == "" {
		return newError(ErrorDependencyUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT "+authority.runtimePhysicalReleaseEvidenceFunction+`(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		evidence.WorkItemID.String(), evidence.AdmissionGrantID.String(), evidence.GrantGeneration,
		evidence.RuntimeRunID.String(), evidence.StartOperationID.String(), evidence.StartDigest[:],
		evidence.ReleaseOperationID.String(), evidence.ReleaseOperationDigest[:], evidence.RuntimeRevision,
		evidence.RuntimeFence, evidence.SandboxLeaseID.String(), evidence.LeaseGeneration, evidence.LeaseFence,
		evidence.SandboxID.String(), evidence.SandboxGeneration, evidence.SandboxFence,
		evidence.ExecutionNodeID.String(), evidence.NodeCapacityGeneration,
		evidence.ResetEvidenceID.String(), evidence.ResetEvidenceDigest[:]); err != nil {
		return newError(ErrorIntegrityConflict)
	}

	var state PhysicalOccupancyState
	var workItemID, grantID, runtimeRunID, startOperationID, leaseOperationID string
	var leaseID, sandboxID, nodeID, resourceClassID, executionPolicyID string
	var releaseOperationID, resetEvidenceID string
	var startDigest, leaseDigest, releaseDigest, resetDigest []byte
	var grantGeneration runtimeexecution.AdmissionGrantGeneration
	var leaseGeneration runtimeexecution.LeaseGeneration
	var leaseFence runtimeexecution.LeaseFence
	var sandboxGeneration runtimeexecution.SandboxGeneration
	var sandboxFence runtimeexecution.SandboxFence
	var nodeGeneration, schedulerEpoch, policyVersion uint64
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT state, work_item_id, admission_grant_id,
		grant_generation, runtime_run_id, start_operation_id, start_digest,
		lease_acquire_operation_id, lease_acquire_digest, sandbox_lease_id,
		lease_generation, lease_fence, sandbox_id, sandbox_generation, sandbox_fence,
		execution_node_id, node_capacity_generation,
		resource_class_id, execution_policy_id, scheduler_epoch, policy_version,
		release_operation_id, release_operation_digest, reset_evidence_id, reset_evidence_digest
		FROM %s WHERE sandbox_lease_id=$1 FOR UPDATE`, authority.table("scheduler_physical_occupancy")),
		evidence.SandboxLeaseID.String()).Scan(&state, &workItemID, &grantID, &grantGeneration,
		&runtimeRunID, &startOperationID, &startDigest, &leaseOperationID, &leaseDigest, &leaseID,
		&leaseGeneration, &leaseFence, &sandboxID, &sandboxGeneration, &sandboxFence,
		&nodeID, &nodeGeneration, &resourceClassID, &executionPolicyID,
		&schedulerEpoch, &policyVersion, &releaseOperationID, &releaseDigest, &resetEvidenceID, &resetDigest)
	if err != nil || workItemID != evidence.WorkItemID.String() || grantID != evidence.AdmissionGrantID.String() ||
		grantGeneration != evidence.GrantGeneration || runtimeRunID != evidence.RuntimeRunID.String() ||
		startOperationID != evidence.StartOperationID.String() || !bytes.Equal(startDigest, evidence.StartDigest[:]) ||
		leaseID != evidence.SandboxLeaseID.String() || leaseGeneration > evidence.LeaseGeneration ||
		leaseFence > evidence.LeaseFence || sandboxID != evidence.SandboxID.String() ||
		sandboxGeneration != evidence.SandboxGeneration || sandboxFence > evidence.SandboxFence ||
		nodeID != evidence.ExecutionNodeID.String() ||
		nodeGeneration != evidence.NodeCapacityGeneration || releaseOperationID != "" &&
		(releaseOperationID != evidence.ReleaseOperationID.String() || !bytes.Equal(releaseDigest, evidence.ReleaseOperationDigest[:]) ||
			resetEvidenceID != evidence.ResetEvidenceID.String() || !bytes.Equal(resetDigest, evidence.ResetEvidenceDigest[:])) {
		return newError(ErrorIntegrityConflict)
	}
	if state == PhysicalOccupancyReleased {
		if err := tx.Commit(); err != nil {
			return newError(ErrorDependencyUnavailable)
		}
		return nil
	}
	if state != PhysicalOccupancyHeld {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1,
		release_operation_id=$2, release_operation_digest=$3, reset_evidence_id=$4,
		reset_evidence_digest=$5, released_at=CURRENT_TIMESTAMP
		WHERE sandbox_lease_id=$6 AND state=$7`, authority.table("scheduler_physical_occupancy")),
		PhysicalOccupancyReleased, evidence.ReleaseOperationID.String(), evidence.ReleaseOperationDigest[:],
		evidence.ResetEvidenceID.String(), evidence.ResetEvidenceDigest[:], evidence.SandboxLeaseID.String(),
		PhysicalOccupancyHeld)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	result, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=CURRENT_TIMESTAMP
		WHERE admission_grant_id=$2 AND execution_node_id=$3 AND node_capacity_generation=$4
		AND grant_generation=$5 AND state=$6`, authority.table("scheduler_node_reservations")),
		ReservationReleased, evidence.AdmissionGrantID.String(), evidence.ExecutionNodeID.String(),
		evidence.NodeCapacityGeneration, evidence.GrantGeneration, ReservationLeaseAttached)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	var logicalMin, logicalMax ReservationState
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT min(state), max(state) FROM %s
		WHERE admission_grant_id=$1`, authority.table("scheduler_logical_reservations")),
		evidence.AdmissionGrantID.String()).Scan(&logicalMin, &logicalMax); err != nil || logicalMin != logicalMax {
		return newError(ErrorIntegrityConflict)
	}
	next := GrantLeaseAttached
	if logicalMin == ReservationReleased {
		next = GrantReleased
	}
	result, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=CURRENT_TIMESTAMP
		WHERE admission_grant_id=$2 AND generation=$3 AND state=$4`,
		authority.table("scheduler_admission_grants")), next, evidence.AdmissionGrantID.String(),
		evidence.GrantGeneration, GrantLeaseAttached)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	return nil
}

func validPhysicalReleaseEvidence(evidence runtimeexecution.PhysicalCapacityReleaseReadyEvidence) bool {
	return evidence.WorkItemID.String() != "" && evidence.AdmissionGrantID.String() != "" &&
		evidence.GrantGeneration > 0 && evidence.RuntimeRunID.String() != "" &&
		evidence.StartOperationID.String() != "" && evidence.StartDigest != (runtimeexecution.Digest{}) &&
		evidence.ReleaseOperationID.String() != "" && evidence.ReleaseOperationDigest != (runtimeexecution.Digest{}) &&
		evidence.RuntimeRevision > 0 && evidence.RuntimeFence > 0 && evidence.SandboxLeaseID.String() != "" &&
		evidence.LeaseGeneration > 0 && evidence.LeaseFence > 0 && evidence.SandboxID.String() != "" &&
		evidence.SandboxGeneration > 0 && evidence.SandboxFence > 0 && evidence.ExecutionNodeID.String() != "" &&
		evidence.NodeCapacityGeneration > 0 && evidence.ResetEvidenceID.String() != "" &&
		evidence.ResetEvidenceDigest != (runtimeexecution.Digest{})
}

func validRuntimeFencedEvidence(evidence runtimeexecution.RuntimeFencedOrTerminalEvidence) bool {
	return evidence.WorkItemID.String() != "" && evidence.AdmissionGrantID.String() != "" &&
		evidence.GrantGeneration > 0 && evidence.RuntimeRunID.String() != "" &&
		evidence.StartOperationID.String() != "" && evidence.StartDigest != (runtimeexecution.Digest{}) &&
		evidence.TerminalDecisionID.String() != "" && evidence.RuntimeRevision > 0 && evidence.RuntimeFence > 0 &&
		evidence.SchedulerEpoch > 0 && evidence.PolicyVersion > 0 &&
		evidence.LeaseAcquireOperationID.String() != "" && evidence.LeaseAcquireDigest != (runtimeexecution.Digest{})
}

func (authority *PostgresAuthority) lockAndValidateTerminalEvidence(
	ctx context.Context,
	tx *sql.Tx,
	evidence runtimeexecution.RuntimeFencedOrTerminalEvidence,
) (GrantState, error) {
	var state GrantState
	var operationID, runtimeRunID, boundDecisionID, leaseOperationID string
	var terminalDecisionID string
	var digest, leaseDigest []byte
	var boundRevision, terminalRevision runtimeexecution.RuntimeRevision
	var boundFence, terminalFence runtimeexecution.RuntimeFence
	var schedulerEpoch, policyVersion, terminalSchedulerEpoch, terminalPolicyVersion uint64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT state, operation_id, payload_digest,
		runtime_run_id, bound_decision_id, bound_runtime_revision, bound_runtime_fence,
		scheduler_epoch, policy_version, lease_acquire_operation_id, lease_acquire_digest,
		terminal_decision_id, terminal_runtime_revision, terminal_runtime_fence,
		terminal_scheduler_epoch, terminal_policy_version FROM %s
		WHERE admission_grant_id=$1 AND work_item_id=$2 AND generation=$3 FOR UPDATE`,
		authority.table("scheduler_admission_grants")), evidence.AdmissionGrantID.String(),
		evidence.WorkItemID.String(), evidence.GrantGeneration).Scan(&state, &operationID, &digest,
		&runtimeRunID, &boundDecisionID, &boundRevision, &boundFence, &schedulerEpoch, &policyVersion,
		&leaseOperationID, &leaseDigest, &terminalDecisionID, &terminalRevision, &terminalFence,
		&terminalSchedulerEpoch, &terminalPolicyVersion)
	if err != nil || operationID != evidence.StartOperationID.String() ||
		!bytes.Equal(digest, evidence.StartDigest[:]) || runtimeRunID != evidence.RuntimeRunID.String() ||
		boundDecisionID == "" || evidence.RuntimeRevision <= boundRevision || evidence.RuntimeFence != boundFence+1 ||
		evidence.SchedulerEpoch != schedulerEpoch || evidence.PolicyVersion != policyVersion ||
		leaseOperationID != evidence.LeaseAcquireOperationID.String() ||
		!bytes.Equal(leaseDigest, evidence.LeaseAcquireDigest[:]) {
		return 0, newError(ErrorIntegrityConflict)
	}
	if terminalDecisionID == "" {
		result, updateErr := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
			terminal_decision_id=$1, terminal_runtime_revision=$2, terminal_runtime_fence=$3,
			terminal_scheduler_epoch=$4, terminal_policy_version=$5, updated_at=CURRENT_TIMESTAMP
			WHERE admission_grant_id=$6 AND generation=$7 AND terminal_decision_id=''`,
			authority.table("scheduler_admission_grants")), evidence.TerminalDecisionID.String(),
			evidence.RuntimeRevision, evidence.RuntimeFence, evidence.SchedulerEpoch, evidence.PolicyVersion,
			evidence.AdmissionGrantID.String(), evidence.GrantGeneration)
		if updateErr != nil {
			return 0, newError(ErrorDependencyUnavailable)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return 0, newError(ErrorIntegrityConflict)
		}
	} else if terminalDecisionID != evidence.TerminalDecisionID.String() ||
		terminalRevision != evidence.RuntimeRevision || terminalFence != evidence.RuntimeFence ||
		terminalSchedulerEpoch != evidence.SchedulerEpoch || terminalPolicyVersion != evidence.PolicyVersion {
		return 0, newError(ErrorIntegrityConflict)
	}
	return state, nil
}
