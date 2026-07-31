package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type postgresWorkerEvidenceState struct {
	EvidenceID             string                 `json:"evidence_id"`
	EvidenceDigest         Digest                 `json:"evidence_digest"`
	OutputContractDigest   Digest                 `json:"output_contract_digest"`
	EvidenceContractDigest Digest                 `json:"evidence_contract_digest"`
	SandboxLeaseID         string                 `json:"sandbox_lease_id"`
	LeaseGeneration        LeaseGeneration        `json:"lease_generation"`
	LeaseFence             LeaseFence             `json:"lease_fence"`
	GatewayGrantID         string                 `json:"gateway_grant_id"`
	GatewayGrantGeneration GatewayGrantGeneration `json:"gateway_grant_generation"`
	GatewayGrantDigest     Digest                 `json:"gateway_grant_digest"`
	InternalCallCount      uint64                 `json:"internal_call_count"`
}

type postgresWorkerObservationState struct {
	SchemaVersion      SchemaVersion               `json:"schema_version"`
	ObservationID      string                      `json:"observation_id"`
	Kind               WorkerObservationKind       `json:"kind"`
	RuntimeRunID       string                      `json:"runtime_run_id"`
	OperationID        string                      `json:"operation_id"`
	CapsuleDigest      Digest                      `json:"capsule_digest"`
	ProducerAuthority  string                      `json:"producer_authority"`
	ProducerGeneration WorkerGeneration            `json:"producer_generation"`
	StreamGeneration   uint64                      `json:"stream_generation"`
	Position           uint64                      `json:"position"`
	ObservedAt         time.Time                   `json:"observed_at"`
	Evidence           postgresWorkerEvidenceState `json:"evidence"`
	SafeFailure        WorkerSafeFailure           `json:"safe_failure"`
	CanonicalDigest    Digest                      `json:"canonical_digest"`
}

func encodePostgresWorkerObservation(observation workerObservation) ([]byte, error) {
	evidence := observation.Evidence
	return json.Marshal(postgresWorkerObservationState{
		SchemaVersion: observation.SchemaVersion, ObservationID: observation.ObservationID.String(),
		Kind: observation.Kind, RuntimeRunID: observation.RuntimeRunID.String(),
		OperationID: observation.OperationID.String(), CapsuleDigest: observation.CapsuleDigest,
		ProducerAuthority: observation.ProducerAuthority.String(), ProducerGeneration: observation.ProducerGeneration,
		StreamGeneration: observation.StreamGeneration, Position: observation.Position,
		ObservedAt: observation.ObservedAt.UTC(), SafeFailure: observation.SafeFailure,
		Evidence: postgresWorkerEvidenceState{
			EvidenceID: evidence.EvidenceID.String(), EvidenceDigest: evidence.EvidenceDigest,
			OutputContractDigest: evidence.OutputContractDigest, EvidenceContractDigest: evidence.EvidenceContractDigest,
			SandboxLeaseID: evidence.SandboxLeaseID.String(), LeaseGeneration: evidence.LeaseGeneration,
			LeaseFence: evidence.LeaseFence, GatewayGrantID: evidence.GatewayGrantID.String(),
			GatewayGrantGeneration: evidence.GatewayGrantGeneration, GatewayGrantDigest: evidence.GatewayGrantDigest,
			InternalCallCount: evidence.InternalCallCount,
		},
		CanonicalDigest: observation.CanonicalDigest,
	})
}

func decodePostgresWorkerObservation(encoded []byte) (workerObservation, error) {
	var state postgresWorkerObservationState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil {
		return workerObservation{}, newPersistenceError(PersistenceStateCorrupt)
	}
	evidence := state.Evidence
	observation := workerObservation{
		SchemaVersion: state.SchemaVersion, ObservationID: WorkerObservationID{value: state.ObservationID},
		Kind: state.Kind, RuntimeRunID: RuntimeRunID{value: state.RuntimeRunID},
		OperationID: OperationID{value: state.OperationID}, CapsuleDigest: state.CapsuleDigest,
		ProducerAuthority: WorkerAuthorityID{value: state.ProducerAuthority}, ProducerGeneration: state.ProducerGeneration,
		StreamGeneration: state.StreamGeneration, Position: state.Position, ObservedAt: state.ObservedAt.UTC(),
		Evidence: WorkerEvidenceCandidateSnapshot{
			EvidenceID: EvidenceID{value: evidence.EvidenceID}, EvidenceDigest: evidence.EvidenceDigest,
			OutputContractDigest: evidence.OutputContractDigest, EvidenceContractDigest: evidence.EvidenceContractDigest,
			SandboxLeaseID: SandboxLeaseID{value: evidence.SandboxLeaseID}, LeaseGeneration: evidence.LeaseGeneration,
			LeaseFence: evidence.LeaseFence, GatewayGrantID: GatewayGrantID{value: evidence.GatewayGrantID},
			GatewayGrantGeneration: evidence.GatewayGrantGeneration, GatewayGrantDigest: evidence.GatewayGrantDigest,
			InternalCallCount: evidence.InternalCallCount,
		},
		SafeFailure: state.SafeFailure, CanonicalDigest: state.CanonicalDigest,
	}
	if observation.CanonicalDigest != canonicalWorkerObservationDigest(observation) {
		return workerObservation{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return observation, nil
}

func (authority *PostgresAuthority) accept(ctx context.Context, command workerAccept) (workerOperationAck, error) {
	if ctx == nil || ctx.Err() != nil {
		return workerOperationAck{}, newError(ErrorDependencyUnavailable)
	}
	capsule, err := decodeExecutionCapsule(command.Capsule)
	if err != nil || !validWorkerAccept(command, capsule) {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return workerOperationAck{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return workerOperationAck{}, normalizeRuntimePersistenceFailure(err)
	}
	if replay, found, replayErr := authority.replayPostgresWorkerAccept(ctx, tx, record, command); found {
		if replayErr == nil {
			replayErr = normalizePostgresCommitFailure(tx.Commit())
		}
		return replay, replayErr
	}
	var disposition DispatchDisposition
	var deliveryCount uint64
	var retainedDispatchAck []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT disposition, delivery_count, ack_digest
		FROM %s WHERE operation_id=$1 FOR UPDATE`, authority.table("runtime_execution_dispatch_delivery")),
		command.OperationID.String()).Scan(&disposition, &deliveryCount, &retainedDispatchAck)
	if err != nil {
		return workerOperationAck{}, normalizeRuntimePersistenceFailure(err)
	}
	record.capsule.disposition, record.capsule.deliveryCount = disposition, deliveryCount
	if len(retainedDispatchAck) != 0 {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	adapter := selectWorkerCapabilityAdapter(command.WorkerClass, authority.agentWorker, authority.toolWorker)
	now := postgresTimestamp(authority.now())
	if adapter == nil || !workerAcceptCurrent(record, command, capsule, now) {
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	ack, err := adapter.acceptCapability(ctx, command, capsule)
	if err != nil {
		return workerOperationAck{}, err
	}
	if !validWorkerOperationAck(command, ack) {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	now = postgresTimestamp(authority.now())
	if !workerAcceptCurrent(record, command, capsule, now) {
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	previousRevision, previousFence, previousState := record.fixture.RuntimeRevision, record.fixture.RuntimeFence, record.fixture.State
	applyWorkerAcceptance(record, command, ack)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, request_digest, capsule_digest, ack_id, ack_digest, accepted_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_worker_acceptances")),
		command.OperationID.String(), command.RuntimeRunID.String(), command.CanonicalDigest[:], command.CapsuleDigest[:],
		ack.OperationAckID.String(), ack.CanonicalDigest[:], now); err != nil {
		return workerOperationAck{}, normalizeRuntimePersistenceFailure(err)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1, ack_digest=$2,
		acknowledged_at=$3 WHERE operation_id=$4 AND disposition=$5 AND ack_digest IS NULL`,
		authority.table("runtime_execution_dispatch_delivery")), DispatchAcknowledged, ack.CanonicalDigest[:], now,
		command.OperationID.String(), DispatchClaimed)
	if err != nil {
		return workerOperationAck{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	if err := authority.updatePostgresRuntimeAggregate(ctx, tx, record, previousRevision, previousFence, previousState, now); err != nil {
		return workerOperationAck{}, err
	}
	if authority.failAt(PersistenceFaultBeforeWorkerAcceptCommit) {
		return workerOperationAck{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return workerOperationAck{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterWorkerAcceptCommit) || authority.failAt(PersistenceFaultBeforeWorkerAcceptResponse) {
		return workerOperationAck{}, newError(ErrorReconciliationRequired)
	}
	return ack, nil
}

func (authority *PostgresAuthority) replayPostgresWorkerAccept(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	command workerAccept,
) (workerOperationAck, bool, error) {
	var requestDigest, capsuleDigest, ackDigest []byte
	var operationID, ackID string
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, request_digest, capsule_digest, ack_id, ack_digest
		FROM %s WHERE runtime_run_id=$1 FOR UPDATE`, authority.table("runtime_execution_worker_acceptances")),
		command.RuntimeRunID.String()).Scan(&operationID, &requestDigest, &capsuleDigest, &ackID, &ackDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return workerOperationAck{}, false, nil
	}
	if err != nil {
		return workerOperationAck{}, true, normalizeRuntimePersistenceFailure(err)
	}
	ack := newWorkerOperationAck(command)
	if operationID != command.OperationID.String() || !bytes.Equal(requestDigest, command.CanonicalDigest[:]) ||
		!bytes.Equal(capsuleDigest, command.CapsuleDigest[:]) || ackID != ack.OperationAckID.String() ||
		!bytes.Equal(ackDigest, ack.CanonicalDigest[:]) || record.worker.OperationAckID != ack.OperationAckID ||
		record.worker.OperationAckDigest != ack.CanonicalDigest {
		return workerOperationAck{}, true, newError(ErrorIntegrityConflict)
	}
	return ack, true, nil
}

func (authority *PostgresAuthority) heartbeat(ctx context.Context, heartbeat workerHeartbeat) (workerLeaseDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return workerLeaseDecision{}, newError(ErrorDependencyUnavailable)
	}
	if !validWorkerHeartbeat(heartbeat) {
		return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return workerLeaseDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	var retainedDigest []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT request_digest FROM %s WHERE operation_id=$1 FOR UPDATE`,
		authority.table("runtime_execution_worker_heartbeat_requests")), heartbeat.OperationID.String()).Scan(&retainedDigest)
	replayed := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return workerLeaseDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if replayed {
		if !bytes.Equal(retainedDigest, heartbeat.CanonicalDigest[:]) {
			return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
		}
	} else {
		record, loadErr := authority.loadRuntimeForUpdate(ctx, tx, heartbeat.RuntimeRunID)
		if errors.Is(loadErr, sql.ErrNoRows) {
			return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
		}
		if loadErr != nil {
			return workerLeaseDecision{}, normalizeRuntimePersistenceFailure(loadErr)
		}
		if !workerHeartbeatCurrent(record, heartbeat, postgresTimestamp(authority.now())) {
			return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			operation_id, runtime_run_id, request_digest, created_at
		) VALUES ($1,$2,$3,$4)`, authority.table("runtime_execution_worker_heartbeat_requests")),
			heartbeat.OperationID.String(), heartbeat.RuntimeRunID.String(), heartbeat.CanonicalDigest[:],
			postgresTimestamp(authority.now())); err != nil {
			return workerLeaseDecision{}, normalizeRuntimePersistenceFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return workerLeaseDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	renewal, err := NewRenewSandboxLease(RenewSandboxLeaseInput{
		SchemaVersion: heartbeat.SchemaVersion, OperationID: heartbeat.OperationID,
		PersonalWorkspaceID: heartbeat.PersonalWorkspaceID, RuntimeRunID: heartbeat.RuntimeRunID,
		SandboxLeaseID: heartbeat.Lease.LeaseID, LeaseGeneration: heartbeat.Lease.Generation,
		LeaseFence: heartbeat.Lease.Fence, ExecutionNodeID: heartbeat.Node.ExecutionNodeID,
		NodeGeneration: heartbeat.Node.Generation, AttestationID: heartbeat.Node.AttestationID,
		AttestationGeneration: heartbeat.Node.AttestationGeneration,
		Authority: NewLeaseRenewalAuthority(heartbeat.Lease.WorkerAuthorityID, heartbeat.Lease.WorkerGeneration,
			heartbeat.Lease.NodeAuthorityID, heartbeat.Lease.AuthorizationGeneration),
		ReleaseSafetyEpoch: heartbeat.ReleaseSafetyEpoch, CatalogSafetyEpoch: heartbeat.CatalogSafetyEpoch,
		RequestedExpiresAt: heartbeat.RequestedExpiresAt, OccurredAt: heartbeat.OccurredAt,
	})
	if err != nil {
		return workerLeaseDecision{}, err
	}
	maintained, err := authority.Maintain(ctx, renewal)
	if err != nil {
		return workerLeaseDecision{}, err
	}
	return newWorkerLeaseDecision(
		heartbeat, maintained.RuntimeRevision, maintained.RuntimeFence, maintained.Lease,
		replayed || maintained.Replayed,
	), nil
}

func (authority *PostgresAuthority) observe(ctx context.Context, request workerObserve) (workerObservationResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return workerObservationResult{}, newError(ErrorDependencyUnavailable)
	}
	if !validWorkerObserve(request) {
		return workerObservationResult{}, newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return workerObservationResult{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, request.Ref.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return workerObservationResult{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return workerObservationResult{}, normalizeRuntimePersistenceFailure(err)
	}
	if replay, found, replayErr := authority.replayPostgresWorkerObservation(ctx, tx, request); found {
		if replayErr == nil {
			replayErr = normalizePostgresCommitFailure(tx.Commit())
		}
		return replay, replayErr
	}
	adapter := selectWorkerCapabilityAdapter(request.Ref.WorkerClass, authority.agentWorker, authority.toolWorker)
	now := postgresTimestamp(authority.now())
	if adapter == nil || !workerObserveCurrent(record, request, now) {
		return workerObservationResult{}, newError(ErrorAuthorizationDenied)
	}
	observation, err := adapter.observeCapability(ctx, request, record.capsule.decoded)
	if err != nil {
		return workerObservationResult{}, err
	}
	now = postgresTimestamp(authority.now())
	if !workerObserveCurrent(record, request, now) ||
		!validWorkerObservation(request, observation) || observation.ObservedAt.After(now) {
		return workerObservationResult{}, newError(ErrorAuthorizationDenied)
	}
	var retainedState, retainedDigest []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT observation_state, observation_digest FROM %s WHERE observation_id=$1`,
		authority.table("runtime_execution_worker_observations")), observation.ObservationID.String()).Scan(
		&retainedState, &retainedDigest,
	)
	if err == nil {
		retained, decodeErr := decodePostgresWorkerObservation(retainedState)
		if decodeErr != nil || !validWorkerObservation(request, retained) ||
			!bytes.Equal(retainedDigest, retained.CanonicalDigest[:]) ||
			retained.CanonicalDigest != observation.CanonicalDigest {
			return workerObservationResult{}, newError(ErrorIntegrityConflict)
		}
		if err := tx.Commit(); err != nil {
			return workerObservationResult{}, normalizeRuntimePersistenceFailure(err)
		}
		return workerObservationResult{
			Disposition: WorkerObservationAccepted, Observation: retained, Replayed: true,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return workerObservationResult{}, normalizeRuntimePersistenceFailure(err)
	}
	wantPosition := request.Cursor.Position + 1
	if observation.Position > wantPosition {
		return workerObservationResult{Disposition: WorkerObservationDeferred, Observation: observation}, nil
	}
	if observation.Position != wantPosition {
		return workerObservationResult{}, newError(ErrorIntegrityConflict)
	}
	encoded, err := encodePostgresWorkerObservation(observation)
	if err != nil {
		return workerObservationResult{}, newError(ErrorIntegrityConflict)
	}
	previousRevision, previousFence, previousState := record.fixture.RuntimeRevision, record.fixture.RuntimeFence, record.fixture.State
	applyWorkerObservation(record, observation)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		observation_id, runtime_run_id, operation_id, query_digest, observation_digest,
		stream_generation, position, observation_state, observed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, authority.table("runtime_execution_worker_observations")),
		observation.ObservationID.String(), observation.RuntimeRunID.String(), observation.OperationID.String(),
		request.CanonicalDigest[:], observation.CanonicalDigest[:], observation.StreamGeneration,
		observation.Position, encoded, observation.ObservedAt); err != nil {
		return workerObservationResult{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := authority.updatePostgresRuntimeAggregate(ctx, tx, record, previousRevision, previousFence, previousState, now); err != nil {
		return workerObservationResult{}, err
	}
	if authority.failAt(PersistenceFaultBeforeWorkerObservationCommit) {
		return workerObservationResult{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return workerObservationResult{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterWorkerObservationCommit) || authority.failAt(PersistenceFaultBeforeWorkerObservationResponse) {
		return workerObservationResult{}, newError(ErrorReconciliationRequired)
	}
	return workerObservationResult{Disposition: WorkerObservationAccepted, Observation: observation}, nil
}

func (authority *PostgresAuthority) replayPostgresWorkerObservation(
	ctx context.Context,
	tx *sql.Tx,
	request workerObserve,
) (workerObservationResult, bool, error) {
	var encoded, digest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT observation_state, observation_digest FROM %s
		WHERE runtime_run_id=$1 AND query_digest=$2 FOR UPDATE`, authority.table("runtime_execution_worker_observations")),
		request.Ref.RuntimeRunID.String(), request.CanonicalDigest[:]).Scan(&encoded, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return workerObservationResult{}, false, nil
	}
	if err != nil {
		return workerObservationResult{}, true, normalizeRuntimePersistenceFailure(err)
	}
	observation, err := decodePostgresWorkerObservation(encoded)
	if err != nil || !bytes.Equal(digest, observation.CanonicalDigest[:]) || !validWorkerObservation(request, observation) {
		return workerObservationResult{}, true, newError(ErrorIntegrityConflict)
	}
	return workerObservationResult{Disposition: WorkerObservationAccepted, Observation: observation, Replayed: true}, true, nil
}

func (authority *PostgresAuthority) stop(ctx context.Context, intent workerStopIntent) (workerStopAck, error) {
	if ctx == nil || ctx.Err() != nil {
		return workerStopAck{}, newError(ErrorDependencyUnavailable)
	}
	if !validWorkerStopIntent(intent) {
		return workerStopAck{}, newError(ErrorIntegrityConflict)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return workerStopAck{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, intent.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return workerStopAck{}, normalizeRuntimePersistenceFailure(err)
	}
	if replay, found, replayErr := authority.replayPostgresWorkerStop(ctx, tx, record, intent); found {
		if replayErr == nil {
			replayErr = normalizePostgresCommitFailure(tx.Commit())
		}
		return replay, replayErr
	}
	adapter := selectWorkerCapabilityAdapter(intent.WorkerClass, authority.agentWorker, authority.toolWorker)
	now := postgresTimestamp(authority.now())
	if adapter == nil || !workerStopCurrent(record, intent, now) {
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	ack, err := adapter.stopCapability(ctx, intent, record.capsule.decoded)
	if err != nil {
		return workerStopAck{}, err
	}
	if !validWorkerStopAck(intent, ack) {
		return workerStopAck{}, newError(ErrorIntegrityConflict)
	}
	now = postgresTimestamp(authority.now())
	if !workerStopCurrent(record, intent, now) {
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	previousRevision, previousFence, previousState := record.fixture.RuntimeRevision, record.fixture.RuntimeFence, record.fixture.State
	applyWorkerStopAcceptance(record, intent, ack)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, original_operation_id, request_digest, ack_id, ack_digest, accepted_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_worker_stops")),
		intent.OperationID.String(), intent.RuntimeRunID.String(), intent.OriginalOperationID.String(),
		intent.CanonicalDigest[:], ack.AckID.String(), ack.CanonicalDigest[:], now); err != nil {
		return workerStopAck{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := authority.updatePostgresRuntimeAggregate(ctx, tx, record, previousRevision, previousFence, previousState, now); err != nil {
		return workerStopAck{}, err
	}
	if authority.failAt(PersistenceFaultBeforeWorkerStopCommit) {
		return workerStopAck{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return workerStopAck{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterWorkerStopCommit) || authority.failAt(PersistenceFaultBeforeWorkerStopResponse) {
		return workerStopAck{}, newError(ErrorReconciliationRequired)
	}
	return ack, nil
}

func (authority *PostgresAuthority) replayPostgresWorkerStop(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	intent workerStopIntent,
) (workerStopAck, bool, error) {
	var requestDigest, ackDigest []byte
	var runtimeRunID, originalOperationID, ackID string
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, original_operation_id, request_digest, ack_id, ack_digest
		FROM %s WHERE operation_id=$1 FOR UPDATE`, authority.table("runtime_execution_worker_stops")),
		intent.OperationID.String()).Scan(&runtimeRunID, &originalOperationID, &requestDigest, &ackID, &ackDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return workerStopAck{}, false, nil
	}
	if err != nil {
		return workerStopAck{}, true, normalizeRuntimePersistenceFailure(err)
	}
	ack := newWorkerStopAck(intent)
	if runtimeRunID != intent.RuntimeRunID.String() || originalOperationID != intent.OriginalOperationID.String() ||
		!bytes.Equal(requestDigest, intent.CanonicalDigest[:]) || ackID != ack.AckID.String() ||
		!bytes.Equal(ackDigest, ack.CanonicalDigest[:]) || record.worker.Stop.AckID != ack.AckID ||
		record.worker.Stop.AckDigest != ack.CanonicalDigest {
		return workerStopAck{}, true, newError(ErrorIntegrityConflict)
	}
	return ack, true, nil
}

func normalizePostgresCommitFailure(err error) error {
	if err == nil {
		return nil
	}
	return normalizeRuntimePersistenceFailure(err)
}
