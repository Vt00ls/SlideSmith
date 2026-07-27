package taskorchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

type postgresDispatcher struct {
	adapter   *PostgresAdapter
	config    DispatcherConfig
	transport OwnedTransport
}

func (adapter *PostgresAdapter) NewOutboxDispatcher(
	config DispatcherConfig,
	transport OwnedTransport,
) (OutboxDispatcher, error) {
	if adapter == nil || adapter.db == nil || transport == nil || config.Now == nil ||
		config.MaxBatchSize == 0 || config.LeaseDuration <= 0 ||
		config.TransportVersion != OwnedTransportV1 || len(config.Authorities) == 0 {
		return nil, newDeliveryError(DeliveryInvalidRequest)
	}
	for _, authority := range config.Authorities {
		if !validDeliveryAuthority(authority) {
			return nil, newDeliveryError(DeliveryInvalidRequest)
		}
	}
	config.Authorities = append([]WorkerAuthority(nil), config.Authorities...)
	return &postgresDispatcher{adapter: adapter, config: config, transport: transport}, nil
}

func (value *postgresDispatcher) Claim(
	ctx context.Context,
	request DeliveryClaimRequest,
) (DeliveryClaimBatch, error) {
	if ctx == nil || ctx.Err() != nil || request.Limit == 0 ||
		!value.authorized(request.Authority) {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	limit := request.Limit
	if limit > value.config.MaxBatchSize {
		limit = value.config.MaxBatchSize
	}
	now := value.config.Now().UTC()
	if now.IsZero() {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	tx, err := value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, disposition, updated_at
	) SELECT operation_id, $1, $2 FROM %s ON CONFLICT (operation_id) DO NOTHING`,
		value.adapter.table("task_orchestration_outbox_delivery"),
		value.adapter.table("task_orchestration_outbox")), DeliveryPending, now); err != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s AS delivery SET
		disposition=$1, terminal=TRUE, lease_authority_kind=0,
		lease_authority_id='', lease_authority_generation=0,
		lease_authority_reason=0, lease_expires_at=NULL, send_started=FALSE, updated_at=$2
		FROM %s AS pending WHERE delivery.operation_id=pending.operation_id
		AND delivery.terminal=FALSE AND delivery.send_started=FALSE
		AND delivery.disposition<>$3 AND EXISTS (
			SELECT 1 FROM %s AS newer WHERE newer.task_id=pending.task_id
			AND newer.fence_kind=pending.fence_kind
			AND newer.phase_run_id=pending.phase_run_id
			AND newer.runtime_run_id=pending.runtime_run_id
			AND (newer.activity_generation>pending.activity_generation OR
				(newer.activity_generation=pending.activity_generation AND newer.fence>pending.fence))
		)`, value.adapter.table("task_orchestration_outbox_delivery"),
		value.adapter.table("task_orchestration_outbox"),
		value.adapter.table("task_orchestration_outbox")), DeliverySuperseded, now,
		DeliveryReconciliationRequired); err != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT operation_id FROM %s
		WHERE terminal=FALSE AND disposition<>$1
		AND (retry_at IS NULL OR retry_at<=$2)
		AND (lease_expires_at IS NULL OR lease_expires_at<=$2)
		ORDER BY operation_id LIMIT $3 FOR UPDATE SKIP LOCKED`,
		value.adapter.table("task_orchestration_outbox_delivery")),
		DeliveryReconciliationRequired, now, limit)
	if err != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	operationIDs := make([]OperationID, 0, limit)
	for rows.Next() {
		var operationID string
		if rows.Scan(&operationID) != nil || !validOpaqueID(operationID) {
			_ = rows.Close()
			return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
		}
		operationIDs = append(operationIDs, OperationID{value: operationID})
	}
	if err := rows.Close(); err != nil || rows.Err() != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	var recoverySafetyEpoch SafetyEpoch
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT safety_epoch FROM %s WHERE singleton=TRUE`,
		value.adapter.table("task_orchestration_recovery_state"))).Scan(&recoverySafetyEpoch); err != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	batch := DeliveryClaimBatch{Claims: make([]DeliveryClaim, 0, len(operationIDs))}
	for _, operationID := range operationIDs {
		record, err := value.loadOutbox(ctx, tx, operationID)
		if err != nil {
			return DeliveryClaimBatch{}, err
		}
		deliveryState, err := value.loadDelivery(ctx, tx, operationID, true)
		if err != nil {
			return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
		}
		if deliveryState.Disposition == DeliveryClaimed && deliveryState.SendStarted {
			deliveryState.Disposition = DeliveryReconciliationRequired
			deliveryState.Authority = authorityValue{}
			deliveryState.LeaseExpiresAt = time.Time{}
			deliveryState.SendStarted = false
			if err := value.saveDelivery(ctx, tx, operationID, deliveryState, now); err != nil {
				return DeliveryClaimBatch{}, err
			}
			continue
		}
		if recoverySafetyEpoch > record.SafetyEpoch {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
				terminal=TRUE, lease_authority_kind=0, lease_authority_id='',
				lease_authority_generation=0, lease_authority_reason=0,
				lease_expires_at=NULL, send_started=FALSE, updated_at=$2 WHERE operation_id=$3`,
				value.adapter.table("task_orchestration_outbox_delivery")),
				DeliverySuperseded, now, operationID.value); err != nil {
				return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
			}
			continue
		}
		var leaseFence DeliveryLeaseFence
		leaseExpiresAt := now.Add(value.config.LeaseDuration)
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
			lease_authority_kind=$2, lease_authority_id=$3,
			lease_authority_generation=$4, lease_authority_reason=$5,
			lease_fence=lease_fence+1, lease_expires_at=$6,
			send_started=FALSE, retry_at=NULL, deferral_reason=0, updated_at=$7
			WHERE operation_id=$8 RETURNING lease_fence`,
			value.adapter.table("task_orchestration_outbox_delivery")),
			DeliveryClaimed, request.Authority.value.kind, request.Authority.value.id.value,
			request.Authority.value.generation, request.Authority.value.reason,
			leaseExpiresAt, now, operationID.value).Scan(&leaseFence); err != nil {
			return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
		}
		transportRequest := value.transportRequest(record, request.Authority)
		transportRequest.Deadline = leaseExpiresAt
		batch.Claims = append(batch.Claims, DeliveryClaim{
			OperationID: operationID,
			Request:     transportRequest,
			LeaseFence:  leaseFence, LeaseExpiresAt: leaseExpiresAt,
		})
	}
	if value.failAt(DeliveryFaultBeforeClaimCommit) {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	if value.failAt(DeliveryFaultAfterClaimCommit) {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}
	return batch, nil
}

func (value *postgresDispatcher) Heartbeat(
	ctx context.Context,
	request DeliveryHeartbeatRequest,
) (DeliveryClaim, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryClaim{}, newDeliveryError(DeliveryUnavailable)
	}
	if !value.authorized(request.Authority) || !validOpaqueID(request.OperationID.value) ||
		request.LeaseFence == 0 {
		return DeliveryClaim{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	now := value.config.Now().UTC()
	tx, err := value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryClaim{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := value.loadDelivery(ctx, tx, request.OperationID, true)
	if err != nil || state.Authority != request.Authority.value ||
		state.LeaseFence != request.LeaseFence || state.Terminal ||
		!state.LeaseExpiresAt.After(now) {
		return DeliveryClaim{}, newDeliveryError(DeliveryClaimLost)
	}
	record, err := value.loadOutbox(ctx, tx, request.OperationID)
	if err != nil {
		return DeliveryClaim{}, err
	}
	state.LeaseExpiresAt = now.Add(value.config.LeaseDuration)
	if err := value.saveDelivery(ctx, tx, request.OperationID, state, now); err != nil {
		return DeliveryClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryClaim{}, newDeliveryError(DeliveryUnavailable)
	}
	transportRequest := value.transportRequest(record, request.Authority)
	transportRequest.Deadline = state.LeaseExpiresAt
	return DeliveryClaim{
		OperationID: request.OperationID,
		Request:     transportRequest,
		LeaseFence:  state.LeaseFence, LeaseExpiresAt: state.LeaseExpiresAt,
	}, nil
}

func (value *postgresDispatcher) Deliver(
	ctx context.Context,
	claim DeliveryClaim,
) (DeliveryResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	authority := claim.Request.Authority
	if !value.authorized(authority) || !validOpaqueID(claim.OperationID.value) ||
		claim.LeaseFence == 0 {
		return DeliveryResult{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	record, state, err := value.beginDelivery(ctx, claim, authority)
	if err != nil {
		return DeliveryResult{}, err
	}
	if state.SendStarted {
		return value.finishClaim(ctx, claim, authority, func(state *memoryDeliveryState) {
			state.Disposition = DeliveryReconciliationRequired
		}, false)
	}
	if deliveryCanBeSuperseded(state) {
		superseded, err := value.supersededBeforeSend(ctx, record)
		if err != nil {
			return DeliveryResult{}, err
		}
		if superseded {
			return value.finishClaim(ctx, claim, authority, func(state *memoryDeliveryState) {
				state.Disposition = DeliverySuperseded
				state.Terminal = true
			}, false)
		}
	}
	request := value.transportRequest(record, authority)
	request.Deadline = state.LeaseExpiresAt
	if value.failAt(DeliveryFaultBeforeSend) {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	_, err = value.markSendStarted(ctx, claim, authority)
	if err != nil {
		return DeliveryResult{}, err
	}
	response, transportErr := value.transport.Deliver(ctx, request)
	if value.failAt(DeliveryFaultAfterSend) {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	finishContext := ctx
	if ctx.Err() != nil {
		finishContext = context.WithoutCancel(ctx)
	}
	if transportErr != nil || response.Version != request.Version ||
		response.OperationID != request.OperationID {
		return value.finishClaim(finishContext, claim, authority, func(state *memoryDeliveryState) {
			state.Disposition = DeliveryReconciliationRequired
		}, false)
	}
	outcome, ok := normalizeDeliveryOutcome(response, value.config.Now().UTC(), deliveryOutcomeFromSend)
	if !ok {
		return value.finishAmbiguous(finishContext, claim, authority)
	}
	return value.finishClaim(finishContext, claim, authority, func(state *memoryDeliveryState) {
		applyNormalizedDeliveryOutcome(state, outcome)
	}, outcome.disposition == DeliveryAccepted)
}

func (value *postgresDispatcher) Inspect(
	ctx context.Context,
	request DeliveryInspectionRequest,
) (DeliveryView, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryView{}, newDeliveryError(DeliveryUnavailable)
	}
	if !value.authorized(request.Authority) || !validOpaqueID(request.OperationID.value) {
		return DeliveryView{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	var exists bool
	if err := value.adapter.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT TRUE FROM %s WHERE operation_id=$1`,
		value.adapter.table("task_orchestration_outbox")), request.OperationID.value).Scan(&exists); err != nil {
		return DeliveryView{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	state, err := value.loadDeliveryFromDB(ctx, request.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryView{OperationID: request.OperationID, Disposition: DeliveryPending}, nil
	}
	if err != nil {
		return DeliveryView{}, newDeliveryError(DeliveryUnavailable)
	}
	return deliveryViewFromState(request.OperationID, state), nil
}

func (value *postgresDispatcher) Reconcile(
	ctx context.Context,
	request DeliveryReconcileRequest,
) (DeliveryResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	if !value.authorized(request.Authority) || !validOpaqueID(request.OperationID.value) {
		return DeliveryResult{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	tx, err := value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := value.loadDelivery(ctx, tx, request.OperationID, true)
	if err != nil || state.Terminal || state.Disposition != DeliveryReconciliationRequired {
		return DeliveryResult{}, newDeliveryError(DeliveryInvalidRequest)
	}
	state.ReconcileFence++
	reconcileFence := state.ReconcileFence
	if err := value.saveDelivery(ctx, tx, request.OperationID, state, value.config.Now().UTC()); err != nil {
		return DeliveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	response, transportErr := value.transport.Inspect(ctx, OwnedTransportInspection{
		Version: value.config.TransportVersion, Authority: request.Authority,
		OperationID: request.OperationID,
	})
	if transportErr != nil || response.Version != value.config.TransportVersion ||
		response.OperationID != request.OperationID {
		return deliveryResultFromState(request.OperationID, state), nil
	}
	tx, err = value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := value.loadDelivery(ctx, tx, request.OperationID, true)
	if err != nil || current.Terminal || current.Disposition != DeliveryReconciliationRequired ||
		current.ReconcileFence != reconcileFence {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	outcome, ok := normalizeDeliveryOutcome(
		response, value.config.Now().UTC(), deliveryOutcomeFromInspection,
	)
	if !ok {
		return deliveryResultFromState(request.OperationID, current), nil
	}
	applyNormalizedDeliveryOutcome(&current, outcome)
	if err := value.saveDelivery(ctx, tx, request.OperationID, current, value.config.Now().UTC()); err != nil {
		return DeliveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	return deliveryResultFromState(request.OperationID, current), nil
}

func (value *postgresDispatcher) beginDelivery(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
) (authoritativeOutboxRecord, memoryDeliveryState, error) {
	now := value.config.Now().UTC()
	tx, err := value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return authoritativeOutboxRecord{}, memoryDeliveryState{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := value.loadDelivery(ctx, tx, claim.OperationID, true)
	if err != nil || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence || !state.LeaseExpiresAt.After(now) {
		return authoritativeOutboxRecord{}, memoryDeliveryState{}, newDeliveryError(DeliveryClaimLost)
	}
	record, err := value.loadOutbox(ctx, tx, claim.OperationID)
	if err != nil {
		return authoritativeOutboxRecord{}, memoryDeliveryState{}, err
	}
	if err := tx.Commit(); err != nil {
		return authoritativeOutboxRecord{}, memoryDeliveryState{}, newDeliveryError(DeliveryUnavailable)
	}
	return record, state, nil
}

func (value *postgresDispatcher) markSendStarted(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
) (memoryDeliveryState, error) {
	now := value.config.Now().UTC()
	tx, err := value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return memoryDeliveryState{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := value.loadDelivery(ctx, tx, claim.OperationID, true)
	if err != nil || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence || !state.LeaseExpiresAt.After(now) ||
		state.SendStarted {
		return memoryDeliveryState{}, newDeliveryError(DeliveryClaimLost)
	}
	state.DeliveryCount++
	state.SendStarted = true
	if err := value.saveDelivery(ctx, tx, claim.OperationID, state, now); err != nil {
		return memoryDeliveryState{}, err
	}
	if err := tx.Commit(); err != nil {
		return memoryDeliveryState{}, newDeliveryError(DeliveryUnavailable)
	}
	return state, nil
}

func (value *postgresDispatcher) supersededBeforeSend(
	ctx context.Context,
	record authoritativeOutboxRecord,
) (bool, error) {
	fenceKind, fence := postgresFenceValue(record.Fence)
	var superseded bool
	err := value.adapter.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT EXISTS (
		SELECT 1 FROM %s WHERE task_id=$1 AND fence_kind=$2
		AND phase_run_id=$3 AND runtime_run_id=$4
		AND (activity_generation>$5 OR (activity_generation=$5 AND fence>$6))
	) OR EXISTS (
		SELECT 1 FROM %s WHERE singleton=TRUE AND safety_epoch>$7
	)`, value.adapter.table("task_orchestration_outbox"),
		value.adapter.table("task_orchestration_recovery_state")),
		record.TaskID.value, fenceKind, record.PhaseRunID.value, record.RuntimeRunID.value,
		record.ActivityGeneration, fence, record.SafetyEpoch).Scan(&superseded)
	if err != nil {
		return false, newDeliveryError(DeliveryUnavailable)
	}
	return superseded, nil
}

func (value *postgresDispatcher) finishAmbiguous(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
) (DeliveryResult, error) {
	return value.finishClaim(ctx, claim, authority, func(state *memoryDeliveryState) {
		state.Disposition = DeliveryReconciliationRequired
	}, false)
}

func (value *postgresDispatcher) finishClaim(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
	apply func(*memoryDeliveryState),
	requireLiveLease bool,
) (DeliveryResult, error) {
	now := value.config.Now().UTC()
	tx, err := value.adapter.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	state, err := value.loadDelivery(ctx, tx, claim.OperationID, true)
	if err != nil || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence || requireLiveLease && !state.LeaseExpiresAt.After(now) {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	apply(&state)
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
	state.SendStarted = false
	if value.failAt(DeliveryFaultBeforeDispositionCommit) {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	if err := value.saveDelivery(ctx, tx, claim.OperationID, state, now); err != nil {
		return DeliveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	if value.failAt(DeliveryFaultAfterDispositionCommit) {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	return deliveryResultFromState(claim.OperationID, state), nil
}

func (value *postgresDispatcher) loadOutbox(
	ctx context.Context,
	tx *sql.Tx,
	operationID OperationID,
) (authoritativeOutboxRecord, error) {
	var record authoritativeOutboxRecord
	var operation, decision, task, phaseRun, runtimeRun, causation string
	var kind EnactmentKind
	var payloadDigest []byte
	var activityGeneration ActivityGeneration
	var safetyEpoch SafetyEpoch
	var fenceKind EnactmentFenceKind
	var fence uint64
	var prerequisites []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT operation_id, decision_id, task_id,
		phase_run_id, runtime_run_id, kind, payload_digest, activity_generation,
		safety_epoch, fence_kind, fence, causation_id, prerequisite_bindings, committed_at
		FROM %s WHERE operation_id=$1`, value.adapter.table("task_orchestration_outbox")),
		operationID.value).Scan(&operation, &decision, &task, &phaseRun, &runtimeRun, &kind,
		&payloadDigest, &activityGeneration, &safetyEpoch, &fenceKind, &fence,
		&causation, &prerequisites, &record.CommittedAt)
	if err != nil || len(payloadDigest) != 32 {
		return authoritativeOutboxRecord{}, newDeliveryError(DeliveryUnavailable)
	}
	copy(record.PayloadDigest[:], payloadDigest)
	record.OperationID = OperationID{value: operation}
	record.DecisionID = DecisionID{value: decision}
	record.TaskID = TaskID{value: task}
	record.PhaseRunID = PhaseRunID{value: phaseRun}
	record.RuntimeRunID = RuntimeRunID{value: runtimeRun}
	record.Kind = kind
	record.ActivityGeneration = activityGeneration
	record.SafetyEpoch = safetyEpoch
	record.Fence = postgresFenceRef(fenceKind, fence)
	record.CausationID = CausationID{value: causation}
	var binding struct {
		DecisionID          string   `json:"decision_id"`
		TaskRevision        uint64   `json:"task_revision"`
		AcceptedEvidenceIDs []string `json:"accepted_evidence_ids"`
	}
	if json.Unmarshal(prerequisites, &binding) != nil || binding.DecisionID != decision ||
		binding.TaskRevision == 0 {
		return authoritativeOutboxRecord{}, newDeliveryError(DeliveryUnavailable)
	}
	record.Prerequisites.TaskRevision = TaskRevision(binding.TaskRevision)
	record.Prerequisites.AcceptedEvidenceIDs = make([]EvidenceID, len(binding.AcceptedEvidenceIDs))
	for index, evidenceID := range binding.AcceptedEvidenceIDs {
		record.Prerequisites.AcceptedEvidenceIDs[index] = EvidenceID{value: evidenceID}
	}
	request := value.transportRequest(record, value.config.Authorities[0])
	request.Deadline = record.CommittedAt.UTC()
	if operationID != record.OperationID || !validOwnedTransportRequest(request) {
		return authoritativeOutboxRecord{}, newDeliveryError(DeliveryUnavailable)
	}
	return record, nil
}

func (value *postgresDispatcher) loadDelivery(
	ctx context.Context,
	tx *sql.Tx,
	operationID OperationID,
	forUpdate bool,
) (memoryDeliveryState, error) {
	query := fmt.Sprintf(`SELECT disposition, lease_authority_kind, lease_authority_id,
		lease_authority_generation, lease_authority_reason, lease_fence,
		lease_expires_at, delivery_count, send_started, terminal, result_digest, retry_at,
		deferral_reason, reconcile_fence FROM %s WHERE operation_id=$1`,
		value.adapter.table("task_orchestration_outbox_delivery"))
	if forUpdate {
		query += " FOR UPDATE"
	}
	return scanPostgresDeliveryState(tx.QueryRowContext(ctx, query, operationID.value))
}

func (value *postgresDispatcher) loadDeliveryFromDB(
	ctx context.Context,
	operationID OperationID,
) (memoryDeliveryState, error) {
	return scanPostgresDeliveryState(value.adapter.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		disposition, lease_authority_kind, lease_authority_id,
		lease_authority_generation, lease_authority_reason, lease_fence,
		lease_expires_at, delivery_count, send_started, terminal, result_digest, retry_at,
		deferral_reason, reconcile_fence FROM %s WHERE operation_id=$1`,
		value.adapter.table("task_orchestration_outbox_delivery")), operationID.value))
}

type postgresRowScanner interface {
	Scan(...any) error
}

func scanPostgresDeliveryState(scanner postgresRowScanner) (memoryDeliveryState, error) {
	var state memoryDeliveryState
	var authorityKind AuthorityKind
	var authorityID string
	var authorityGeneration AuthorizationGeneration
	var authorityReason AdministratorReason
	var leaseFence, deliveryCount, reconcileFence uint64
	var leaseExpiresAt, retryAt sql.NullTime
	var resultDigest []byte
	err := scanner.Scan(&state.Disposition, &authorityKind, &authorityID, &authorityGeneration,
		&authorityReason, &leaseFence, &leaseExpiresAt, &deliveryCount, &state.SendStarted, &state.Terminal,
		&resultDigest, &retryAt, &state.DeferralReason, &reconcileFence)
	if err != nil {
		return memoryDeliveryState{}, err
	}
	if deliveryCount > math.MaxUint32 || leaseFence == 0 && leaseExpiresAt.Valid ||
		state.SendStarted && (!leaseExpiresAt.Valid || state.Disposition != DeliveryClaimed) ||
		len(resultDigest) != 0 && len(resultDigest) != 32 {
		return memoryDeliveryState{}, newDeliveryError(DeliveryUnavailable)
	}
	state.Authority = authorityValue{
		kind: authorityKind, id: AuthorityID{value: authorityID},
		generation: authorityGeneration, reason: authorityReason,
	}
	state.LeaseFence = DeliveryLeaseFence(leaseFence)
	state.DeliveryCount = uint32(deliveryCount)
	state.ReconcileFence = reconcileFence
	if leaseExpiresAt.Valid {
		state.LeaseExpiresAt = leaseExpiresAt.Time.UTC()
	}
	if retryAt.Valid {
		state.RetryAt = retryAt.Time.UTC()
	}
	if len(resultDigest) == 32 {
		copy(state.ResultDigest[:], resultDigest)
	}
	return state, nil
}

func (value *postgresDispatcher) saveDelivery(
	ctx context.Context,
	tx *sql.Tx,
	operationID OperationID,
	state memoryDeliveryState,
	updatedAt time.Time,
) error {
	var leaseExpiresAt any
	if !state.LeaseExpiresAt.IsZero() {
		leaseExpiresAt = state.LeaseExpiresAt.UTC()
	}
	var retryAt any
	if !state.RetryAt.IsZero() {
		retryAt = state.RetryAt.UTC()
	}
	var resultDigest any
	if state.ResultDigest != (DeliveryResultDigest{}) {
		resultDigest = state.ResultDigest[:]
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
		lease_authority_kind=$2, lease_authority_id=$3,
		lease_authority_generation=$4, lease_authority_reason=$5,
		lease_fence=$6, lease_expires_at=$7, delivery_count=$8, send_started=$9,
		terminal=$10, result_digest=$11, retry_at=$12, deferral_reason=$13,
		reconcile_fence=$14, updated_at=$15 WHERE operation_id=$16`,
		value.adapter.table("task_orchestration_outbox_delivery")),
		state.Disposition, state.Authority.kind, state.Authority.id.value,
		state.Authority.generation, state.Authority.reason, state.LeaseFence,
		leaseExpiresAt, state.DeliveryCount, state.SendStarted, state.Terminal, resultDigest,
		retryAt, state.DeferralReason, state.ReconcileFence, updatedAt.UTC(), operationID.value)
	if err != nil {
		return newDeliveryError(DeliveryUnavailable)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return newDeliveryError(DeliveryUnavailable)
	}
	return nil
}

func (value *postgresDispatcher) transportRequest(
	record authoritativeOutboxRecord,
	authority WorkerAuthority,
) OwnedTransportRequest {
	fenceKind, fence := postgresFenceValue(record.Fence)
	return OwnedTransportRequest{
		Version: value.config.TransportVersion, Authority: authority,
		OperationID: record.OperationID, DecisionID: record.DecisionID, TaskID: record.TaskID,
		PhaseRunID: record.PhaseRunID, RuntimeRunID: record.RuntimeRunID, Kind: record.Kind,
		PayloadDigest: record.PayloadDigest, ActivityGeneration: record.ActivityGeneration,
		SafetyEpoch: record.SafetyEpoch, FenceKind: fenceKind, Fence: fence,
		CausationID: record.CausationID,
		Prerequisites: DeliveryPrerequisites{
			TaskRevision:        record.Prerequisites.TaskRevision,
			AcceptedEvidenceIDs: cloneEvidenceIDs(record.Prerequisites.AcceptedEvidenceIDs),
		},
	}
}

func (value *postgresDispatcher) authorized(authority WorkerAuthority) bool {
	if !validDeliveryAuthority(authority) {
		return false
	}
	for _, allowed := range value.config.Authorities {
		if allowed.value == authority.value {
			return true
		}
	}
	return false
}

func (value *postgresDispatcher) failAt(point DeliveryFaultPoint) bool {
	return value.config.Faults != nil && value.config.Faults.FailAt(point)
}

func deliveryViewFromState(operationID OperationID, state memoryDeliveryState) DeliveryView {
	return DeliveryView{
		OperationID: operationID, Disposition: state.Disposition,
		ResultDigest: state.ResultDigest, DeliveryCount: state.DeliveryCount,
		Terminal: state.Terminal, RetryAt: state.RetryAt,
		DeferralReason: state.DeferralReason, LeaseFence: state.LeaseFence,
		LeaseExpiresAt: state.LeaseExpiresAt,
	}
}
