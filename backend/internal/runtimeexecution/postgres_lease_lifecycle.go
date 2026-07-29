package runtimeexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	postgresMaintenanceRenew int16 = iota + 1
	postgresMaintenanceFence
	postgresMaintenanceReset
	postgresMaintenanceAttestNode
)

type postgresExecutionNodeRecord struct {
	ExecutionNodeFixture
}

type postgresMaintenanceLeaseState struct {
	AcquireStatus           LeaseAcquireStatus      `json:"acquire_status"`
	AcquireOperationID      string                  `json:"acquire_operation_id"`
	AcquireDigest           Digest                  `json:"acquire_digest"`
	LeaseID                 string                  `json:"lease_id"`
	Generation              LeaseGeneration         `json:"generation"`
	Fence                   LeaseFence              `json:"fence"`
	Disposition             LeaseDisposition        `json:"disposition"`
	ExpiresAt               time.Time               `json:"expires_at"`
	SandboxID               string                  `json:"sandbox_id"`
	SandboxGeneration       SandboxGeneration       `json:"sandbox_generation"`
	SandboxFence            SandboxFence            `json:"sandbox_fence"`
	WorkerAuthorityID       string                  `json:"worker_authority_id"`
	WorkerGeneration        WorkerGeneration        `json:"worker_generation"`
	NodeAuthorityID         string                  `json:"node_authority_id"`
	AuthorizationGeneration AuthorizationGeneration `json:"authorization_generation"`
	AuthorizationExpiresAt  time.Time               `json:"authorization_expires_at"`
}

type postgresMaintenanceDecisionState struct {
	OperationID                  string                               `json:"operation_id"`
	CanonicalRequestDigest       Digest                               `json:"canonical_request_digest"`
	RuntimeRevision              RuntimeRevision                      `json:"runtime_revision"`
	RuntimeFence                 RuntimeFence                         `json:"runtime_fence"`
	Lease                        postgresMaintenanceLeaseState        `json:"lease"`
	Node                         postgresRuntimeNodeState             `json:"node"`
	Cleanup                      postgresLeaseCleanupState            `json:"cleanup"`
	PhysicalCapacityReleaseReady postgresPhysicalReleaseEvidenceState `json:"physical_capacity_release_ready"`
}

type postgresMaintenanceAuditState struct {
	SchemaVersion          SchemaVersion                   `json:"schema_version"`
	CommandKind            int16                           `json:"command_kind"`
	OperationID            string                          `json:"operation_id"`
	CanonicalRequestDigest Digest                          `json:"canonical_request_digest"`
	RuntimeRunID           string                          `json:"runtime_run_id"`
	ExecutionNodeID        string                          `json:"execution_node_id"`
	AuthorityKind          RuntimeMaintenanceAuthorityKind `json:"authority_kind"`
	AuthorityID            string                          `json:"authority_id"`
	AuthorityGeneration    AuthorizationGeneration         `json:"authority_generation"`
	BeforeRuntimeRevision  RuntimeRevision                 `json:"before_runtime_revision"`
	AfterRuntimeRevision   RuntimeRevision                 `json:"after_runtime_revision"`
	BeforeRuntimeFence     RuntimeFence                    `json:"before_runtime_fence"`
	AfterRuntimeFence      RuntimeFence                    `json:"after_runtime_fence"`
	OccurredAt             time.Time                       `json:"occurred_at"`
}

func (authority *PostgresAuthority) installPostgresMaintenanceAuthorities(
	ctx context.Context,
	tx *sql.Tx,
) error {
	for _, binding := range authority.maintenanceAuthorities {
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s AS current_authority (
			execution_node_id, authority_kind, authority_id, authority_generation, updated_at
		) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (execution_node_id, authority_kind) DO UPDATE SET
			authority_id=EXCLUDED.authority_id,
			authority_generation=EXCLUDED.authority_generation,
			updated_at=EXCLUDED.updated_at
		WHERE (current_authority.authority_id=EXCLUDED.authority_id AND
			current_authority.authority_generation=EXCLUDED.authority_generation)
			OR EXCLUDED.authority_generation>current_authority.authority_generation`,
			authority.table("runtime_execution_maintenance_authorities")),
			binding.executionNodeID.String(), binding.caller.kind, binding.caller.id.String(),
			binding.caller.generation, postgresTimestamp(authority.now()))
		if err != nil {
			return normalizeRuntimePersistenceFailure(err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			return newPersistenceError(PersistenceIntegrityConflict)
		}
	}
	return nil
}

func maintenanceLeaseState(value RuntimeLeaseSnapshot) postgresMaintenanceLeaseState {
	return postgresMaintenanceLeaseState{
		AcquireStatus: value.AcquireStatus, AcquireOperationID: value.AcquireOperationID.String(),
		AcquireDigest: value.AcquireDigest, LeaseID: value.LeaseID.String(), Generation: value.Generation,
		Fence: value.Fence, Disposition: value.Disposition, ExpiresAt: value.ExpiresAt.UTC(),
		SandboxID: value.SandboxID.String(), SandboxGeneration: value.SandboxGeneration,
		SandboxFence: value.SandboxFence, WorkerAuthorityID: value.WorkerAuthorityID.String(),
		WorkerGeneration: value.WorkerGeneration, NodeAuthorityID: value.NodeAuthorityID.String(),
		AuthorizationGeneration: value.AuthorizationGeneration,
		AuthorizationExpiresAt:  value.AuthorizationExpiresAt.UTC(),
	}
}

func leaseSnapshotFromMaintenance(value postgresMaintenanceLeaseState) RuntimeLeaseSnapshot {
	return RuntimeLeaseSnapshot{
		AcquireStatus: value.AcquireStatus, AcquireOperationID: OperationID{value: value.AcquireOperationID},
		AcquireDigest: value.AcquireDigest, LeaseID: SandboxLeaseID{value: value.LeaseID},
		Generation: value.Generation, Fence: value.Fence, Disposition: value.Disposition,
		ExpiresAt: value.ExpiresAt.UTC(), SandboxID: SandboxID{value: value.SandboxID},
		SandboxGeneration: value.SandboxGeneration, SandboxFence: value.SandboxFence,
		WorkerAuthorityID: WorkerAuthorityID{value: value.WorkerAuthorityID}, WorkerGeneration: value.WorkerGeneration,
		NodeAuthorityID:         NodeAuthorityID{value: value.NodeAuthorityID},
		AuthorizationGeneration: value.AuthorizationGeneration,
		AuthorizationExpiresAt:  value.AuthorizationExpiresAt.UTC(),
	}
}

func postgresNodeState(value RuntimeNodeSnapshot) postgresRuntimeNodeState {
	return postgresRuntimeNodeState{
		ExecutionNodeID: value.ExecutionNodeID.String(), Generation: value.Generation,
		Readiness: value.Readiness, AttestationID: value.AttestationID.String(),
		AttestationGeneration: value.AttestationGeneration, AttestedAt: value.AttestedAt.UTC(),
		ExpiresAt: value.ExpiresAt.UTC(), Occupancy: value.Occupancy, Quarantined: value.Quarantined,
		Containment: value.Containment, Reset: value.Reset,
	}
}

func nodeSnapshotFromPostgres(value postgresRuntimeNodeState) RuntimeNodeSnapshot {
	return RuntimeNodeSnapshot{
		ExecutionNodeID: ExecutionNodeID{value: value.ExecutionNodeID}, Generation: value.Generation,
		Readiness: value.Readiness, AttestationID: NodeAttestationID{value: value.AttestationID},
		AttestationGeneration: value.AttestationGeneration, AttestedAt: value.AttestedAt.UTC(),
		ExpiresAt: value.ExpiresAt.UTC(), Occupancy: value.Occupancy, Quarantined: value.Quarantined,
		Containment: value.Containment, Reset: value.Reset,
	}
}

func postgresCleanupState(value RuntimeLeaseCleanupSnapshot) postgresLeaseCleanupState {
	return postgresLeaseCleanupState{
		Status: value.Status, OperationID: value.OperationID.String(),
		CanonicalRequestDigest: value.CanonicalRequestDigest, StopMainProcess: value.StopMainProcess,
		StopChildProcesses: value.StopChildProcesses, RevokeSecrets: value.RevokeSecrets,
		RemoveNetwork: value.RemoveNetwork, FenceRuntimeView: value.FenceRuntimeView,
		ReconcileContainment: value.ReconcileContainment,
	}
}

func cleanupSnapshotFromPostgres(value postgresLeaseCleanupState) RuntimeLeaseCleanupSnapshot {
	return RuntimeLeaseCleanupSnapshot{
		Status: value.Status, OperationID: OperationID{value: value.OperationID},
		CanonicalRequestDigest: value.CanonicalRequestDigest, StopMainProcess: value.StopMainProcess,
		StopChildProcesses: value.StopChildProcesses, RevokeSecrets: value.RevokeSecrets,
		RemoveNetwork: value.RemoveNetwork, FenceRuntimeView: value.FenceRuntimeView,
		ReconcileContainment: value.ReconcileContainment,
	}
}

func encodePostgresMaintenanceDecision(decision RuntimeMaintenanceDecision) ([]byte, error) {
	physical := postgresCapacityEvidenceFromSnapshot(RuntimeCapacityEvidenceSnapshot{
		PhysicalCapacityReleaseReady: decision.PhysicalCapacityReleaseReady,
	}).PhysicalCapacityReleaseReady
	return json.Marshal(postgresMaintenanceDecisionState{
		OperationID: decision.OperationID.String(), CanonicalRequestDigest: decision.CanonicalRequestDigest,
		RuntimeRevision: decision.RuntimeRevision, RuntimeFence: decision.RuntimeFence,
		Lease: maintenanceLeaseState(decision.Lease), Node: postgresNodeState(decision.Node),
		Cleanup: postgresCleanupState(decision.Cleanup), PhysicalCapacityReleaseReady: physical,
	})
}

func decodePostgresMaintenanceDecision(encoded []byte) (RuntimeMaintenanceDecision, error) {
	var state postgresMaintenanceDecisionState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil {
		return RuntimeMaintenanceDecision{}, newPersistenceError(PersistenceStateCorrupt)
	}
	physical := capacityEvidenceSnapshotFromPostgres(postgresCapacityEvidenceState{
		PhysicalCapacityReleaseReady: state.PhysicalCapacityReleaseReady,
	}).PhysicalCapacityReleaseReady
	decision := RuntimeMaintenanceDecision{
		OperationID: OperationID{value: state.OperationID}, CanonicalRequestDigest: state.CanonicalRequestDigest,
		RuntimeRevision: state.RuntimeRevision, RuntimeFence: state.RuntimeFence,
		Lease: leaseSnapshotFromMaintenance(state.Lease), Node: nodeSnapshotFromPostgres(state.Node),
		Cleanup: cleanupSnapshotFromPostgres(state.Cleanup), PhysicalCapacityReleaseReady: physical,
	}
	if !validPostgresMaintenanceDecision(decision) {
		return RuntimeMaintenanceDecision{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return decision, nil
}

func validPostgresMaintenanceDecision(decision RuntimeMaintenanceDecision) bool {
	if !validOpaqueID(decision.OperationID.String()) || decision.CanonicalRequestDigest == (Digest{}) {
		return false
	}
	if decision.RuntimeRevision == 0 || decision.RuntimeFence == 0 {
		return decision.RuntimeRevision == 0 && decision.RuntimeFence == 0 &&
			decision.Lease == (RuntimeLeaseSnapshot{}) && decision.Cleanup == (RuntimeLeaseCleanupSnapshot{}) &&
			decision.PhysicalCapacityReleaseReady == (PhysicalCapacityReleaseReadyEvidence{}) &&
			decision.Node != (RuntimeNodeSnapshot{}) && knownNodeSnapshot(decision.Node) &&
			decision.Node.Readiness == NodeReady && !decision.Node.Quarantined &&
			decision.Node.Occupancy == NodeUnoccupied && decision.Node.Containment == ContainmentEstablished &&
			decision.Node.Reset == ResetCompleted
	}
	if !knownLeaseLifecycleSnapshot(decision.Lease) || decision.Lease.Disposition == LeaseDispositionNone ||
		!knownNodeSnapshot(decision.Node) || decision.Node == (RuntimeNodeSnapshot{}) ||
		!knownLeaseCleanupSnapshot(decision.Cleanup) ||
		!knownPhysicalReleaseEvidence(decision.PhysicalCapacityReleaseReady) {
		return false
	}
	switch decision.Lease.Disposition {
	case LeaseActive:
		return decision.Cleanup == (RuntimeLeaseCleanupSnapshot{}) &&
			decision.PhysicalCapacityReleaseReady == (PhysicalCapacityReleaseReadyEvidence{})
	case LeaseRevoked, LeaseExpired:
		return decision.Cleanup.Status == LeaseCleanupPending &&
			decision.PhysicalCapacityReleaseReady == (PhysicalCapacityReleaseReadyEvidence{})
	case LeaseReleased:
		return decision.Cleanup.Status == LeaseCleanupCompleted &&
			decision.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{})
	default:
		return false
	}
}

func (authority *PostgresAuthority) loadPostgresNodeForUpdate(
	ctx context.Context,
	tx *sql.Tx,
	nodeID ExecutionNodeID,
) (*postgresExecutionNodeRecord, error) {
	var node postgresExecutionNodeRecord
	var resetDigest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT execution_node_id, node_generation, readiness,
		attestation_id, attestation_generation, attested_at, expires_at, resource_class_id,
		execution_policy_id, node_authority_id, worker_authority_id, worker_generation,
		authorization_generation, authorization_expires_at, release_safety_epoch,
		catalog_safety_epoch, occupancy, quarantined, containment, reset_status,
		active_runtime_run_id, active_lease_id, last_sandbox_generation, last_sandbox_fence,
		last_reset_evidence_id, last_reset_evidence_digest FROM %s WHERE execution_node_id=$1 FOR UPDATE`,
		authority.table("runtime_execution_nodes")), nodeID.String()).Scan(
		&node.ExecutionNodeID.value, &node.Generation, &node.Readiness, &node.AttestationID.value,
		&node.AttestationGeneration, &node.AttestedAt, &node.ExpiresAt, &node.ResourceClassID.value,
		&node.ExecutionPolicyID.value, &node.NodeAuthorityID.value, &node.WorkerAuthorityID.value,
		&node.WorkerGeneration, &node.AuthorizationGeneration, &node.AuthorizationExpiresAt,
		&node.ReleaseSafetyEpoch, &node.CatalogSafetyEpoch, &node.Occupancy, &node.Quarantined,
		&node.Containment, &node.Reset, &node.ActiveRuntimeRunID.value, &node.ActiveLeaseID.value,
		&node.LastSandboxGeneration, &node.LastSandboxFence, &node.LastResetEvidenceID.value, &resetDigest)
	if err != nil {
		return nil, err
	}
	if len(resetDigest) != 0 && len(resetDigest) != sha256.Size {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	copy(node.LastResetEvidenceDigest[:], resetDigest)
	node.AttestedAt = node.AttestedAt.UTC()
	node.ExpiresAt = node.ExpiresAt.UTC()
	node.AuthorizationExpiresAt = node.AuthorizationExpiresAt.UTC()
	return &node, nil
}

func (authority *PostgresAuthority) updatePostgresNode(
	ctx context.Context,
	tx *sql.Tx,
	node *postgresExecutionNodeRecord,
	updatedAt time.Time,
) error {
	var resetDigest any
	if node.LastResetEvidenceDigest != (Digest{}) {
		resetDigest = node.LastResetEvidenceDigest[:]
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET node_generation=$1,
		readiness=$2, attestation_id=$3, attestation_generation=$4, attested_at=$5,
		expires_at=$6, resource_class_id=$7, execution_policy_id=$8, node_authority_id=$9,
		worker_authority_id=$10, worker_generation=$11, authorization_generation=$12,
		authorization_expires_at=$13, release_safety_epoch=$14, catalog_safety_epoch=$15,
		occupancy=$16, quarantined=$17, containment=$18, reset_status=$19,
		active_runtime_run_id=$20, active_lease_id=$21, last_sandbox_generation=$22,
		last_sandbox_fence=$23, last_reset_evidence_id=$24, last_reset_evidence_digest=$25,
		updated_at=$26 WHERE execution_node_id=$27`, authority.table("runtime_execution_nodes")),
		node.Generation, node.Readiness, node.AttestationID.String(), node.AttestationGeneration,
		node.AttestedAt, node.ExpiresAt, node.ResourceClassID.String(), node.ExecutionPolicyID.String(),
		node.NodeAuthorityID.String(), node.WorkerAuthorityID.String(), node.WorkerGeneration,
		node.AuthorizationGeneration, node.AuthorizationExpiresAt, node.ReleaseSafetyEpoch,
		node.CatalogSafetyEpoch, node.Occupancy, node.Quarantined, node.Containment, node.Reset,
		node.ActiveRuntimeRunID.String(), node.ActiveLeaseID.String(), node.LastSandboxGeneration,
		node.LastSandboxFence, node.LastResetEvidenceID.String(), resetDigest, updatedAt, node.ExecutionNodeID.String())
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) Maintain(
	ctx context.Context,
	command RuntimeMaintenanceCommand,
) (RuntimeMaintenanceDecision, error) {
	if ctx == nil || ctx.Err() != nil || command == nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorDependencyUnavailable)
	}
	switch typed := command.(type) {
	case RenewSandboxLease:
		canonical, valid := canonicalRenewSandboxLease(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return authority.renewPostgresSandboxLease(ctx, typed, canonical)
	case FenceSandboxLease:
		canonical, valid := canonicalFenceSandboxLease(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return authority.fencePostgresSandboxLease(ctx, typed, canonical)
	case ConfirmSandboxReset:
		canonical, valid := canonicalConfirmSandboxReset(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return authority.confirmPostgresSandboxReset(ctx, typed, canonical)
	case AttestExecutionNode:
		canonical, valid := canonicalAttestExecutionNode(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return authority.attestPostgresExecutionNode(ctx, typed, canonical)
	default:
		return RuntimeMaintenanceDecision{}, newError(ErrorInvalidRequest)
	}
}

func (authority *PostgresAuthority) replayPostgresMaintenance(
	ctx context.Context,
	tx *sql.Tx,
	operationID OperationID,
	digest Digest,
	canonical []byte,
) (RuntimeMaintenanceDecision, bool, error) {
	var retainedDigest, retainedCanonical, decisionState []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT canonical_request_digest,
		canonical_request, decision_state FROM %s WHERE operation_id=$1`,
		authority.table("runtime_execution_maintenance_operations")), operationID.String()).Scan(
		&retainedDigest, &retainedCanonical, &decisionState)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeMaintenanceDecision{}, false, nil
	}
	if err != nil {
		return RuntimeMaintenanceDecision{}, false, normalizeRuntimePersistenceFailure(err)
	}
	if !bytes.Equal(retainedDigest, digest[:]) || !bytes.Equal(retainedCanonical, canonical) {
		return RuntimeMaintenanceDecision{}, true, newError(ErrorIntegrityConflict)
	}
	decision, err := decodePostgresMaintenanceDecision(decisionState)
	if err != nil || decision.OperationID != operationID || decision.CanonicalRequestDigest != digest {
		return RuntimeMaintenanceDecision{}, true, newError(ErrorIntegrityConflict)
	}
	decision.Replayed = true
	return decision, true, nil
}

func (authority *PostgresAuthority) validatePostgresMaintenanceCaller(
	ctx context.Context,
	tx *sql.Tx,
	executionNodeID ExecutionNodeID,
	caller maintenanceCallerAuthority,
) error {
	var authorityID string
	var generation AuthorizationGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT authority_id, authority_generation FROM %s
		WHERE execution_node_id=$1 AND authority_kind=$2 FOR SHARE`,
		authority.table("runtime_execution_maintenance_authorities")),
		executionNodeID.String(), caller.kind).Scan(&authorityID, &generation)
	if errors.Is(err, sql.ErrNoRows) || err == nil &&
		(authorityID != caller.id.String() || generation != caller.generation) {
		return newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) persistPostgresMaintenance(
	ctx context.Context,
	tx *sql.Tx,
	kind int16,
	canonical []byte,
	decision RuntimeMaintenanceDecision,
	runtimeRunID RuntimeRunID,
	nodeID ExecutionNodeID,
	caller maintenanceCallerAuthority,
	beforeRevision RuntimeRevision,
	beforeFence RuntimeFence,
	occurredAt time.Time,
) error {
	decisionState, err := encodePostgresMaintenanceDecision(decision)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, command_kind, canonical_request_digest, runtime_run_id,
		execution_node_id, canonical_request, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_maintenance_operations")),
		decision.OperationID.String(), kind, decision.CanonicalRequestDigest[:], runtimeRunID.String(),
		nodeID.String(), canonical, decisionState, occurredAt); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return newError(ErrorDependencyUnavailable)
	}
	audit := postgresMaintenanceAuditState{
		SchemaVersion: SchemaV1, CommandKind: kind, OperationID: decision.OperationID.String(),
		CanonicalRequestDigest: decision.CanonicalRequestDigest, RuntimeRunID: runtimeRunID.String(),
		ExecutionNodeID: nodeID.String(), AuthorityKind: caller.kind, AuthorityID: caller.id.String(),
		AuthorityGeneration: caller.generation, BeforeRuntimeRevision: beforeRevision,
		AfterRuntimeRevision: decision.RuntimeRevision, BeforeRuntimeFence: beforeFence,
		AfterRuntimeFence: decision.RuntimeFence, OccurredAt: occurredAt.UTC(),
	}
	auditBytes, err := json.Marshal(audit)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	auditDigest := Digest(sha256.Sum256(auditBytes))
	auditID := "runtime-maintenance-audit-" + decision.OperationID.String()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			audit_id, operation_id, command_kind, canonical_request_digest, runtime_run_id,
			execution_node_id, authority_kind, authority_id, authority_generation,
			before_runtime_revision, after_runtime_revision,
			before_runtime_fence, after_runtime_fence, occurred_at, canonical_digest, audit_state
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		authority.table("runtime_execution_maintenance_audit")), auditID, decision.OperationID.String(),
		kind, decision.CanonicalRequestDigest[:], runtimeRunID.String(), nodeID.String(), caller.kind,
		caller.id.String(), caller.generation, beforeRevision, decision.RuntimeRevision, beforeFence,
		decision.RuntimeFence, occurredAt, auditDigest[:], auditBytes); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) || authority.failAt(PersistenceFaultBeforeOutbox) {
		return newError(ErrorDependencyUnavailable)
	}
	payloadDigest := digestBytes(canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, canonical_request_digest, runtime_run_id, execution_node_id,
		payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_maintenance_outbox")),
		decision.OperationID.String(), decision.CanonicalRequestDigest[:], runtimeRunID.String(),
		nodeID.String(), canonical, payloadDigest[:], occurredAt); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) updatePostgresRuntimeAggregate(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	previousRevision RuntimeRevision,
	previousFence RuntimeFence,
	previousState RuntimeState,
	updatedAt time.Time,
) error {
	aggregate, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET runtime_revision=$1,
		runtime_fence=$2, runtime_state=$3, runtime_outcome=$4, aggregate_state=$5, updated_at=$6
		WHERE runtime_run_id=$7 AND runtime_revision=$8 AND runtime_fence=$9 AND runtime_state=$10`,
		authority.table("runtime_execution_runtimes")), record.fixture.RuntimeRevision,
		record.fixture.RuntimeFence, record.fixture.State, record.fixture.Outcome, aggregate, updatedAt,
		record.fixture.RuntimeRunID.String(), previousRevision, previousFence, previousState)
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) updatePostgresLeaseLifecycle(
	ctx context.Context,
	tx *sql.Tx,
	lease RuntimeLeaseSnapshot,
	runtimeRunID RuntimeRunID,
) error {
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET lease_generation=$1,
		lease_fence=$2, lease_disposition=$3, lease_expires_at=$4, sandbox_fence=$5
		WHERE runtime_run_id=$6 AND sandbox_lease_id=$7`,
		authority.table("runtime_execution_prelease_leases")), lease.Generation, lease.Fence,
		lease.Disposition, lease.ExpiresAt, lease.SandboxFence, runtimeRunID.String(), lease.LeaseID.String())
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) renewPostgresSandboxLease(
	ctx context.Context,
	command RenewSandboxLease,
	canonical []byte,
) (RuntimeMaintenanceDecision, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, replayErr := authority.replayPostgresMaintenance(ctx, tx, command.OperationID,
		command.CanonicalRequestDigest, canonical); found {
		if replayErr == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				replayErr = normalizeRuntimePersistenceFailure(commitErr)
			}
		}
		return replay, replayErr
	}
	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	node, err := authority.loadPostgresNodeForUpdate(ctx, tx, command.ExecutionNodeID)
	if err != nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	now := postgresTimestamp(authority.now())
	lease := record.lease
	workerAuthority := command.Authority
	if record.fixture.State == RuntimeTerminal || lease.AcquireStatus != LeaseGranted ||
		lease.Disposition != LeaseActive || lease.LeaseID != command.SandboxLeaseID ||
		lease.Generation != command.LeaseGeneration || lease.Fence != command.LeaseFence ||
		lease.WorkerAuthorityID != workerAuthority.workerAuthorityID ||
		lease.WorkerGeneration != workerAuthority.workerGeneration ||
		lease.NodeAuthorityID != workerAuthority.nodeAuthorityID ||
		lease.AuthorizationGeneration != workerAuthority.authorizationGeneration ||
		command.ReleaseSafetyEpoch != record.fixture.SafetyEpoch ||
		command.CatalogSafetyEpoch != record.catalogSafetyEpoch ||
		node.ExecutionNodeID != record.operation.ExecutionNodeID || node.Generation != command.NodeGeneration ||
		node.AttestationID != command.AttestationID ||
		node.AttestationGeneration != command.AttestationGeneration || node.Readiness != NodeReady ||
		node.Quarantined || node.Occupancy != NodeOccupied ||
		node.ActiveRuntimeRunID != command.RuntimeRunID || node.ActiveLeaseID != command.SandboxLeaseID ||
		!now.Before(node.ExpiresAt) || !now.Before(node.AuthorizationExpiresAt) ||
		!now.Before(lease.ExpiresAt) || !now.Before(lease.AuthorizationExpiresAt) || command.OccurredAt.After(now) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	maximumExpiry := earliestTime(record.deadline, record.leaseAcquireBy, node.ExpiresAt,
		node.AuthorizationExpiresAt, lease.AuthorizationExpiresAt)
	if !command.RequestedExpiresAt.After(lease.ExpiresAt) || command.RequestedExpiresAt.After(maximumExpiry) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	previousRevision, previousFence, previousState := record.fixture.RuntimeRevision,
		record.fixture.RuntimeFence, record.fixture.State
	record.fixture.RuntimeRevision++
	record.lease.Generation++
	record.lease.Fence++
	record.lease.ExpiresAt = command.RequestedExpiresAt
	record.node = nodeSnapshot(node.ExecutionNodeFixture)
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		Lease: record.lease, Node: record.node, Cleanup: record.cleanup,
	}
	if err := authority.updatePostgresRuntimeAggregate(ctx, tx, record, previousRevision, previousFence,
		previousState, now); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.updatePostgresLeaseLifecycle(ctx, tx, record.lease, command.RuntimeRunID); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.persistPostgresMaintenance(ctx, tx, postgresMaintenanceRenew, canonical, decision,
		command.RuntimeRunID, command.ExecutionNodeID, command.Authority.caller(), previousRevision, previousFence,
		command.OccurredAt); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return RuntimeMaintenanceDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterCommit) || authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeMaintenanceDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func (authority *PostgresAuthority) fencePostgresSandboxLease(
	ctx context.Context,
	command FenceSandboxLease,
	canonical []byte,
) (RuntimeMaintenanceDecision, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, replayErr := authority.replayPostgresMaintenance(ctx, tx, command.OperationID,
		command.CanonicalRequestDigest, canonical); found {
		if replayErr == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				replayErr = normalizeRuntimePersistenceFailure(commitErr)
			}
		}
		return replay, replayErr
	}
	if err := authority.validatePostgresMaintenanceCaller(ctx, tx, command.ExecutionNodeID,
		command.Authority.caller()); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	node, err := authority.loadPostgresNodeForUpdate(ctx, tx, command.ExecutionNodeID)
	if err != nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	now := postgresTimestamp(authority.now())
	if !validLeaseFenceTransition(record, &node.ExecutionNodeFixture, command, now) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	previousRevision, previousFence, previousState := record.fixture.RuntimeRevision,
		record.fixture.RuntimeFence, record.fixture.State
	decision := applyLeaseFenceTransition(record, &node.ExecutionNodeFixture, command)
	if err := authority.updatePostgresRuntimeAggregate(ctx, tx, record, previousRevision, previousFence,
		previousState, now); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.updatePostgresLeaseLifecycle(ctx, tx, record.lease, command.RuntimeRunID); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.updatePostgresNode(ctx, tx, node, now); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.persistPostgresMaintenance(ctx, tx, postgresMaintenanceFence, canonical, decision,
		command.RuntimeRunID, command.ExecutionNodeID, command.Authority.caller(), previousRevision, previousFence,
		command.OccurredAt); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	cleanupDigest := digestBytes(append([]byte("slidesmith.runtime-execution.lease-cleanup/v1\n"), canonical...))
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, sandbox_lease_id, lease_generation, lease_fence,
		sandbox_id, sandbox_generation, sandbox_fence, stop_main_process,
		stop_child_processes, revoke_secrets, remove_network, fence_runtime_view,
		reconcile_containment, canonical_digest, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,TRUE,TRUE,TRUE,TRUE,TRUE,$9,$10)`,
		authority.table("runtime_execution_lease_cleanup_obligations")), command.OperationID.String(),
		command.RuntimeRunID.String(), record.lease.LeaseID.String(), record.lease.Generation,
		record.lease.Fence, record.lease.SandboxID.String(), record.lease.SandboxGeneration,
		record.lease.SandboxFence, cleanupDigest[:], now); err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return RuntimeMaintenanceDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterCommit) || authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeMaintenanceDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func (authority *PostgresAuthority) confirmPostgresSandboxReset(
	ctx context.Context,
	command ConfirmSandboxReset,
	canonical []byte,
) (RuntimeMaintenanceDecision, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, replayErr := authority.replayPostgresMaintenance(ctx, tx, command.OperationID,
		command.CanonicalRequestDigest, canonical); found {
		if replayErr == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				replayErr = normalizeRuntimePersistenceFailure(commitErr)
			}
		}
		return replay, replayErr
	}
	if err := authority.validatePostgresMaintenanceCaller(ctx, tx, command.ExecutionNodeID,
		command.Authority.caller()); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	node, err := authority.loadPostgresNodeForUpdate(ctx, tx, command.ExecutionNodeID)
	if err != nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	now := postgresTimestamp(authority.now())
	if !validSandboxResetTransition(record, &node.ExecutionNodeFixture, command, now) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	previousRevision, previousFence, previousState := record.fixture.RuntimeRevision,
		record.fixture.RuntimeFence, record.fixture.State
	decision := applySandboxResetTransition(record, &node.ExecutionNodeFixture, command)
	release := decision.PhysicalCapacityReleaseReady
	if err := authority.updatePostgresRuntimeAggregate(ctx, tx, record, previousRevision, previousFence,
		previousState, now); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.updatePostgresLeaseLifecycle(ctx, tx, record.lease, command.RuntimeRunID); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.updatePostgresNode(ctx, tx, node, now); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if err := authority.persistPostgresMaintenance(ctx, tx, postgresMaintenanceReset, canonical, decision,
		command.RuntimeRunID, command.ExecutionNodeID, command.Authority.caller(), previousRevision, previousFence,
		command.OccurredAt); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		release_operation_id, release_operation_digest, work_item_id, admission_grant_id,
		grant_generation, runtime_run_id, start_operation_id, start_digest, runtime_revision,
		runtime_fence, sandbox_lease_id, lease_generation, lease_fence, sandbox_id,
		sandbox_generation, sandbox_fence, execution_node_id, node_capacity_generation,
		reset_evidence_id, reset_evidence_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		authority.table("runtime_execution_physical_release_evidence")), release.ReleaseOperationID.String(),
		release.ReleaseOperationDigest[:], release.WorkItemID.String(), release.AdmissionGrantID.String(),
		release.GrantGeneration, release.RuntimeRunID.String(), release.StartOperationID.String(),
		release.StartDigest[:], release.RuntimeRevision, release.RuntimeFence, release.SandboxLeaseID.String(),
		release.LeaseGeneration, release.LeaseFence, release.SandboxID.String(), release.SandboxGeneration,
		release.SandboxFence, release.ExecutionNodeID.String(), release.NodeCapacityGeneration,
		release.ResetEvidenceID.String(), release.ResetEvidenceDigest[:], now); err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return RuntimeMaintenanceDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterCommit) || authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeMaintenanceDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func (authority *PostgresAuthority) attestPostgresExecutionNode(
	ctx context.Context,
	command AttestExecutionNode,
	canonical []byte,
) (RuntimeMaintenanceDecision, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, replayErr := authority.replayPostgresMaintenance(ctx, tx, command.OperationID,
		command.CanonicalRequestDigest, canonical); found {
		if replayErr == nil {
			if commitErr := tx.Commit(); commitErr != nil {
				replayErr = normalizeRuntimePersistenceFailure(commitErr)
			}
		}
		return replay, replayErr
	}
	if err := authority.validatePostgresMaintenanceCaller(ctx, tx, command.ExecutionNodeID,
		command.Authority.caller()); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	now := postgresTimestamp(authority.now())
	if command.AttestedAt.After(now) || !now.Before(command.ExpiresAt) ||
		!now.Before(command.AuthorizationExpiresAt) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	node, err := authority.loadPostgresNodeForUpdate(ctx, tx, command.ExecutionNodeID)
	fresh := errors.Is(err, sql.ErrNoRows)
	if err != nil && !fresh {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if fresh {
		node = &postgresExecutionNodeRecord{ExecutionNodeFixture: ExecutionNodeFixture{
			ExecutionNodeID: command.ExecutionNodeID, Generation: command.NodeGeneration,
			Readiness: NodeReady, AttestationID: command.AttestationID,
			AttestationGeneration: command.AttestationGeneration, AttestedAt: command.AttestedAt,
			ExpiresAt: command.ExpiresAt, ResourceClassID: command.ResourceClassID,
			ExecutionPolicyID: command.ExecutionPolicyID, NodeAuthorityID: command.NodeAuthorityID,
			WorkerAuthorityID: command.WorkerAuthorityID, WorkerGeneration: command.WorkerGeneration,
			AuthorizationGeneration: command.AuthorizationGeneration,
			AuthorizationExpiresAt:  command.AuthorizationExpiresAt,
			ReleaseSafetyEpoch:      command.ReleaseSafetyEpoch, CatalogSafetyEpoch: command.CatalogSafetyEpoch,
			Occupancy: NodeUnoccupied, Containment: ContainmentEstablished, Reset: ResetCompleted,
			LastResetEvidenceID: command.ResetEvidenceID, LastResetEvidenceDigest: command.ResetEvidenceDigest,
		}}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			execution_node_id, node_generation, readiness, attestation_id, attestation_generation,
			attested_at, expires_at, resource_class_id, execution_policy_id, node_authority_id,
			worker_authority_id, worker_generation, authorization_generation,
			authorization_expires_at, release_safety_epoch, catalog_safety_epoch, occupancy,
			quarantined, containment, reset_status, last_reset_evidence_id,
			last_reset_evidence_digest, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,FALSE,$18,$19,$20,$21,$22)`,
			authority.table("runtime_execution_nodes")), command.ExecutionNodeID.String(), command.NodeGeneration,
			NodeReady, command.AttestationID.String(), command.AttestationGeneration, command.AttestedAt,
			command.ExpiresAt, command.ResourceClassID.String(), command.ExecutionPolicyID.String(),
			command.NodeAuthorityID.String(), command.WorkerAuthorityID.String(), command.WorkerGeneration,
			command.AuthorizationGeneration, command.AuthorizationExpiresAt, command.ReleaseSafetyEpoch,
			command.CatalogSafetyEpoch, NodeUnoccupied, ContainmentEstablished, ResetCompleted,
			command.ResetEvidenceID.String(), command.ResetEvidenceDigest[:], now); err != nil {
			return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
		}
	} else {
		if !node.Quarantined || node.Occupancy != NodeUnoccupied || command.NodeGeneration < node.Generation ||
			command.NodeGeneration == node.Generation && command.AttestationGeneration <= node.AttestationGeneration ||
			command.ResetEvidenceID != node.LastResetEvidenceID ||
			command.ResetEvidenceDigest != node.LastResetEvidenceDigest ||
			command.ResourceClassID != node.ResourceClassID ||
			command.ExecutionPolicyID != node.ExecutionPolicyID ||
			command.ReleaseSafetyEpoch != node.ReleaseSafetyEpoch {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		node.Generation = command.NodeGeneration
		node.Readiness = NodeReady
		node.AttestationID = command.AttestationID
		node.AttestationGeneration = command.AttestationGeneration
		node.AttestedAt = command.AttestedAt
		node.ExpiresAt = command.ExpiresAt
		node.NodeAuthorityID = command.NodeAuthorityID
		node.WorkerAuthorityID = command.WorkerAuthorityID
		node.WorkerGeneration = command.WorkerGeneration
		node.AuthorizationGeneration = command.AuthorizationGeneration
		node.AuthorizationExpiresAt = command.AuthorizationExpiresAt
		node.CatalogSafetyEpoch = command.CatalogSafetyEpoch
		node.Quarantined = false
		node.Containment = ContainmentEstablished
		node.Reset = ResetCompleted
		if err := authority.updatePostgresNode(ctx, tx, node, now); err != nil {
			return RuntimeMaintenanceDecision{}, err
		}
	}
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		Node: nodeSnapshot(node.ExecutionNodeFixture),
	}
	if err := authority.persistPostgresMaintenance(ctx, tx, postgresMaintenanceAttestNode, canonical, decision,
		RuntimeRunID{}, command.ExecutionNodeID, command.Authority.caller(), 0, 0, command.OccurredAt); err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return RuntimeMaintenanceDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeMaintenanceDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterCommit) || authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeMaintenanceDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}
