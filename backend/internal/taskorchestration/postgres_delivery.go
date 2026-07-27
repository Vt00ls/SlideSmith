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
		if deliveryState.Disposition == DeliveryClaimed && deliveryState.DeliveryCount > 0 {
			deliveryState.Disposition = DeliveryReconciliationRequired
			deliveryState.Authority = authorityValue{}
			deliveryState.LeaseExpiresAt = time.Time{}
			if err := value.saveDelivery(ctx, tx, operationID, deliveryState, now); err != nil {
				return DeliveryClaimBatch{}, err
			}
			continue
		}
		if recoverySafetyEpoch > record.SafetyEpoch {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET disposition=$1,
				terminal=TRUE, lease_authority_kind=0, lease_authority_id='',
				lease_authority_generation=0, lease_authority_reason=0,
				lease_expires_at=NULL, updated_at=$2 WHERE operation_id=$3`,
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
			retry_at=NULL, deferral_reason=0, updated_at=$7
			WHERE operation_id=$8 RETURNING lease_fence`,
			value.adapter.table("task_orchestration_outbox_delivery")),
			DeliveryClaimed, request.Authority.value.kind, request.Authority.value.id.value,
			request.Authority.value.generation, request.Authority.value.reason,
			leaseExpiresAt, now, operationID.value).Scan(&leaseFence); err != nil {
			return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
		}
		batch.Claims = append(batch.Claims, DeliveryClaim{
			OperationID: operationID,
			Request:     value.transportRequest(record, request.Authority),
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
	return DeliveryClaim{
		OperationID: request.OperationID,
		Request:     value.transportRequest(record, request.Authority),
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
	request := value.transportRequest(record, authority)
	if value.failAt(DeliveryFaultBeforeSend) {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	state, err = value.markSendStarted(ctx, claim, authority)
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
		return value.finishClaim(finishContext, claim, authority, func(state *memoryDeliveryState) bool {
			state.Disposition = DeliveryReconciliationRequired
			return true
		}, false)
	}
	switch response.Outcome {
	case OwnedTransportAccepted:
		if response.ResultDigest == (DeliveryResultDigest{}) {
			return value.finishAmbiguous(finishContext, claim, authority)
		}
		return value.finishClaim(finishContext, claim, authority, func(state *memoryDeliveryState) bool {
			state.Disposition = DeliveryAccepted
			state.ResultDigest = response.ResultDigest
			state.Terminal = true
			return true
		}, true)
	case OwnedTransportBackpressured:
		if response.RetryAt.Location() != time.UTC || !response.RetryAt.After(value.config.Now().UTC()) {
			return value.finishAmbiguous(finishContext, claim, authority)
		}
		return value.finishClaim(finishContext, claim, authority, func(state *memoryDeliveryState) bool {
			state.Disposition = DeliveryBackpressured
			state.RetryAt = response.RetryAt
			return true
		}, false)
	case OwnedTransportDeferred:
		if response.DeferralReason != OwnedTransportPrerequisiteDeferred ||
			response.RetryAt.Location() != time.UTC || !response.RetryAt.After(value.config.Now().UTC()) {
			return value.finishAmbiguous(finishContext, claim, authority)
		}
		return value.finishClaim(finishContext, claim, authority, func(state *memoryDeliveryState) bool {
			state.Disposition = DeliveryDeferred
			state.DeferralReason = response.DeferralReason
			state.RetryAt = response.RetryAt
			return true
		}, false)
	case OwnedTransportSuperseded:
		return value.finishTerminal(finishContext, claim, authority, DeliverySuperseded)
	case OwnedTransportPoisoned, OwnedTransportUnsupportedVersion, OwnedTransportUnauthorized:
		return value.finishTerminal(finishContext, claim, authority, DeliveryPoisoned)
	case OwnedTransportIntegrityConflict:
		return value.finishTerminal(finishContext, claim, authority, DeliveryIntegrityConflict)
	default:
		_ = state
		return value.finishAmbiguous(finishContext, claim, authority)
	}
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
	switch response.Outcome {
	case OwnedTransportAccepted:
		if response.ResultDigest == (DeliveryResultDigest{}) {
			return deliveryResultFromState(request.OperationID, current), nil
		}
		current.Disposition = DeliveryAccepted
		current.ResultDigest = response.ResultDigest
		current.Terminal = true
	case OwnedTransportUnknown:
		current.Disposition = DeliveryPending
	case OwnedTransportSuperseded:
		current.Disposition = DeliverySuperseded
		current.Terminal = true
	case OwnedTransportIntegrityConflict:
		current.Disposition = DeliveryIntegrityConflict
		current.Terminal = true
	case OwnedTransportPoisoned, OwnedTransportUnsupportedVersion, OwnedTransportUnauthorized:
		current.Disposition = DeliveryPoisoned
		current.Terminal = true
	case OwnedTransportBackpressured:
		if response.RetryAt.Location() != time.UTC ||
			!response.RetryAt.After(value.config.Now().UTC()) {
			return deliveryResultFromState(request.OperationID, current), nil
		}
		current.Disposition = DeliveryBackpressured
		current.RetryAt = response.RetryAt
	case OwnedTransportDeferred:
		if response.DeferralReason != OwnedTransportPrerequisiteDeferred ||
			response.RetryAt.Location() != time.UTC ||
			!response.RetryAt.After(value.config.Now().UTC()) {
			return deliveryResultFromState(request.OperationID, current), nil
		}
		current.Disposition = DeliveryDeferred
		current.DeferralReason = response.DeferralReason
		current.RetryAt = response.RetryAt
	default:
		return deliveryResultFromState(request.OperationID, current), nil
	}
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
		state.LeaseFence != claim.LeaseFence || !state.LeaseExpiresAt.After(now) {
		return memoryDeliveryState{}, newDeliveryError(DeliveryClaimLost)
	}
	state.DeliveryCount++
	if err := value.saveDelivery(ctx, tx, claim.OperationID, state, now); err != nil {
		return memoryDeliveryState{}, err
	}
	if err := tx.Commit(); err != nil {
		return memoryDeliveryState{}, newDeliveryError(DeliveryUnavailable)
	}
	return state, nil
}

func (value *postgresDispatcher) finishAmbiguous(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
) (DeliveryResult, error) {
	return value.finishClaim(ctx, claim, authority, func(state *memoryDeliveryState) bool {
		state.Disposition = DeliveryReconciliationRequired
		return true
	}, false)
}

func (value *postgresDispatcher) finishTerminal(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
	disposition DeliveryDisposition,
) (DeliveryResult, error) {
	return value.finishClaim(ctx, claim, authority, func(state *memoryDeliveryState) bool {
		state.Disposition = disposition
		state.Terminal = true
		return true
	}, false)
}

func (value *postgresDispatcher) finishClaim(
	ctx context.Context,
	claim DeliveryClaim,
	authority WorkerAuthority,
	apply func(*memoryDeliveryState) bool,
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
	if !apply(&state) {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
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
		lease_expires_at, delivery_count, terminal, result_digest, retry_at,
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
		lease_expires_at, delivery_count, terminal, result_digest, retry_at,
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
		&authorityReason, &leaseFence, &leaseExpiresAt, &deliveryCount, &state.Terminal,
		&resultDigest, &retryAt, &state.DeferralReason, &reconcileFence)
	if err != nil {
		return memoryDeliveryState{}, err
	}
	if deliveryCount > math.MaxUint32 || leaseFence == 0 && leaseExpiresAt.Valid ||
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
		lease_fence=$6, lease_expires_at=$7, delivery_count=$8, terminal=$9,
		result_digest=$10, retry_at=$11, deferral_reason=$12,
		reconcile_fence=$13, updated_at=$14 WHERE operation_id=$15`,
		value.adapter.table("task_orchestration_outbox_delivery")),
		state.Disposition, state.Authority.kind, state.Authority.id.value,
		state.Authority.generation, state.Authority.reason, state.LeaseFence,
		leaseExpiresAt, state.DeliveryCount, state.Terminal, resultDigest, retryAt,
		state.DeferralReason, state.ReconcileFence, updatedAt.UTC(), operationID.value)
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
