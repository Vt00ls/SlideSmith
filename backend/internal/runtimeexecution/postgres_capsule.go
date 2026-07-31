package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const postgresCapsuleAuditSchemaV1 = "slidesmith.runtime-execution.capsule-audit/v1"

type postgresCapsuleAuditState struct {
	SchemaVersion          string `json:"schema_version"`
	CapsuleID              string `json:"capsule_id"`
	CapsuleDigest          string `json:"capsule_digest"`
	RuntimeRunID           string `json:"runtime_run_id"`
	StartOperationID       string `json:"start_operation_id"`
	StartDigest            string `json:"start_digest"`
	DispatchOperationID    string `json:"dispatch_operation_id"`
	SandboxLeaseID         string `json:"sandbox_lease_id"`
	LeaseGeneration        uint64 `json:"lease_generation"`
	LeaseFence             uint64 `json:"lease_fence"`
	RuntimeBindingEvidence string `json:"runtime_binding_evidence"`
	InputEvidence          string `json:"input_evidence"`
	RuntimeViewEvidence    string `json:"runtime_view_evidence,omitempty"`
	GatewayEvidence        string `json:"gateway_evidence,omitempty"`
	CommittedAt            string `json:"committed_at"`
}

func (authority *PostgresAuthority) ensurePostgresExecutionCapsule(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	if decision.Snapshot.Capsule.State == CapsulePrepared {
		return decision, nil
	}
	now := postgresTimestamp(authority.now())
	if !capsulePrerequisitesReadyAt(decision.Snapshot, now) || authority.executionCapsuleResolver == nil {
		projectCapsuleReadinessAt(&decision.Snapshot, now)
		return decision, nil
	}
	resolution, err := authority.executionCapsuleResolver.ResolveExecutionCapsule(
		ctx,
		ExecutionCapsuleResolutionRequest{Start: start, Snapshot: decision.Snapshot, Now: now},
	)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	now = postgresTimestamp(authority.now())
	resolution, security, err := validateExecutionCapsuleResolution(
		start, decision.Snapshot, now, resolution,
	)
	if err != nil {
		return RuntimeDecision{}, err
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	now = postgresTimestamp(authority.now())
	if record.capsule.snapshot.State == CapsulePrepared {
		current := snapshot(record, SnapshotSchemaCurrent)
		resolution, security, err = validateExecutionCapsuleResolution(start, current, now, resolution)
		if err != nil {
			return RuntimeDecision{}, err
		}
		_, candidate, candidateDigest, err := buildExecutionCapsule(start, current, now, resolution, security)
		if err != nil {
			return RuntimeDecision{}, err
		}
		if candidateDigest != record.capsule.snapshot.Digest || !bytes.Equal(candidate, record.capsule.wire) {
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return authority.withCurrentPostgresSnapshot(ctx, start, decision)
	}
	if record.acceptedStartDigest != start.CanonicalRequestDigest ||
		record.fixture.State != RuntimePreparingPrerequisites || record.fixture.Outcome != RuntimeOutcomeNone {
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return authority.withCurrentPostgresSnapshot(ctx, start, decision)
	}
	projected := snapshot(record, SnapshotSchemaCurrent)
	if !capsulePrerequisitesReadyAt(projected, now) {
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return authority.withCurrentPostgresSnapshot(ctx, start, decision)
	}
	if err := authority.validatePostgresCurrentGatewayAuthority(ctx, tx, record, now); err != nil {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	resolution, security, err = validateExecutionCapsuleResolution(start, projected, now, resolution)
	if err != nil {
		return RuntimeDecision{}, err
	}
	capsule, canonical, capsuleDigest, err := buildExecutionCapsule(start, projected, now, resolution, security)
	if err != nil {
		return RuntimeDecision{}, err
	}
	dispatchID := dispatchOperationID(capsule.CapsuleID, capsuleDigest)
	audit := postgresCapsuleAuditState{
		SchemaVersion: postgresCapsuleAuditSchemaV1, CapsuleID: capsule.CapsuleID.String(),
		CapsuleDigest: capsuleDigest.String(), RuntimeRunID: start.RuntimeRunID.String(),
		StartOperationID: start.OperationID.String(), StartDigest: start.CanonicalRequestDigest.String(),
		DispatchOperationID: dispatchID.String(), SandboxLeaseID: projected.Lease.LeaseID.String(),
		LeaseGeneration: uint64(projected.Lease.Generation), LeaseFence: uint64(projected.Lease.Fence),
		RuntimeBindingEvidence: projected.Readiness.RuntimeBinding.EvidenceDigest.String(),
		InputEvidence:          projected.Readiness.ImmutableInputs.EvidenceDigest.String(),
		CommittedAt:            now.Format(canonicalTimeFormat),
	}
	if projected.Readiness.RuntimeView.State == PrerequisiteAccepted {
		audit.RuntimeViewEvidence = projected.Readiness.RuntimeView.EvidenceDigest.String()
	}
	if projected.Readiness.LLMGateway.State == PrerequisiteAccepted {
		audit.GatewayEvidence = projected.Readiness.LLMGateway.EvidenceDigest.String()
	}
	auditCanonical, err := json.Marshal(audit)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	auditDigest := digestBytes(auditCanonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		capsule_id, runtime_run_id, capsule_digest, capsule, dispatch_operation_id, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6)`, authority.table("runtime_execution_capsules")),
		capsule.CapsuleID.String(), start.RuntimeRunID.String(), capsuleDigest[:], canonical,
		dispatchID.String(), now); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeCapsuleAudit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		capsule_id, runtime_run_id, audit_digest, audit_state, committed_at
	) VALUES ($1,$2,$3,$4,$5)`, authority.table("runtime_execution_capsule_audit")),
		capsule.CapsuleID.String(), start.RuntimeRunID.String(), auditDigest[:], auditCanonical, now); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeCapsuleOutbox) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, capsule_id, capsule_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_dispatch_outbox")),
		dispatchID.String(), start.RuntimeRunID.String(), capsule.CapsuleID.String(), capsuleDigest[:],
		canonical, capsuleDigest[:], now); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, disposition
	) VALUES ($1,$2)`, authority.table("runtime_execution_dispatch_delivery")),
		dispatchID.String(), DispatchPending); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	record.capsule = retainedExecutionCapsule{
		snapshot: RuntimeCapsuleSnapshot{
			State: CapsulePrepared, CapsuleID: capsule.CapsuleID, Digest: capsuleDigest,
		},
		dispatchOperationID: dispatchID,
		wire:                append([]byte(nil), canonical...), decoded: capsule, disposition: DispatchPending,
	}
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease, record.capsule.snapshot)
	aggregate, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET aggregate_state=$1, updated_at=$2
		WHERE runtime_run_id=$3 AND runtime_revision=$4`, authority.table("runtime_execution_runtimes")),
		aggregate, now, start.RuntimeRunID.String(), record.fixture.RuntimeRevision)
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if authority.failAt(PersistenceFaultBeforeCapsuleCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterCapsuleCommit) ||
		authority.failAt(PersistenceFaultBeforeCapsuleResponse) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	return authority.withCurrentPostgresSnapshot(ctx, start, decision)
}

func (authority *PostgresAuthority) loadPostgresExecutionCapsule(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
) error {
	if record == nil {
		return newError(ErrorIntegrityConflict)
	}
	var capsuleID, dispatchOperationID, startOperationID, startWorkspaceID string
	var capsuleDigest, capsuleWire, outboxCapsuleDigest, outboxPayload, outboxPayloadDigest []byte
	var auditDigest, auditState, startDigest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT capsule.capsule_id, capsule.capsule_digest,
		capsule.capsule, capsule.dispatch_operation_id, audit.audit_digest, audit.audit_state,
		outbox.capsule_digest, outbox.payload, outbox.payload_digest,
		start_request.operation_id, start_request.canonical_request_digest,
		start_request.personal_workspace_id
		FROM %s AS capsule
		JOIN %s AS audit ON audit.capsule_id=capsule.capsule_id
		JOIN %s AS outbox ON outbox.operation_id=capsule.dispatch_operation_id
		JOIN %s AS start_request ON start_request.runtime_run_id=capsule.runtime_run_id
			AND start_request.command_kind=$2
		WHERE capsule.runtime_run_id=$1`, authority.table("runtime_execution_capsules"),
		authority.table("runtime_execution_capsule_audit"),
		authority.table("runtime_execution_dispatch_outbox"),
		authority.table("runtime_execution_requests")), record.fixture.RuntimeRunID.String(), CommandStartRuntimeRun).Scan(
		&capsuleID, &capsuleDigest, &capsuleWire, &dispatchOperationID, &auditDigest, &auditState,
		&outboxCapsuleDigest, &outboxPayload, &outboxPayloadDigest,
		&startOperationID, &startDigest, &startWorkspaceID,
	)
	if err == sql.ErrNoRows {
		if record.readiness.CapsuleReady {
			return newError(ErrorIntegrityConflict)
		}
		return nil
	}
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	wantDigest := digestBytes(capsuleWire)
	decoded, decodeErr := decodeExecutionCapsule(capsuleWire)
	var audit postgresCapsuleAuditState
	auditDecoder := json.NewDecoder(bytes.NewReader(auditState))
	auditDecoder.DisallowUnknownFields()
	auditErr := auditDecoder.Decode(&audit)
	if auditErr == nil {
		auditErr = ensureJSONEOF(auditDecoder)
	}
	canonicalAudit, canonicalAuditErr := json.Marshal(audit)
	wantAuditDigest := digestBytes(canonicalAudit)
	_, runtimeBindingEvidenceErr := digestFromCanonicalText(audit.RuntimeBindingEvidence)
	_, inputEvidenceErr := digestFromCanonicalText(audit.InputEvidence)
	runtimeViewEvidenceErr := optionalAuditEvidenceDigest(audit.RuntimeViewEvidence, decoded.Effect == EffectMutating)
	gatewayEvidenceErr := optionalAuditEvidenceDigest(audit.GatewayEvidence, decoded.GatewayGrantID != (GatewayGrantID{}))
	_, committedAtErr := time.Parse(canonicalTimeFormat, audit.CommittedAt)
	if decodeErr != nil || auditErr != nil || canonicalAuditErr != nil || decoded.CapsuleID.String() != capsuleID ||
		!validOpaqueID(dispatchOperationID) ||
		decoded.PersonalWorkspaceID != record.fixture.PersonalWorkspaceID || decoded.TaskID != record.fixture.TaskID ||
		decoded.PhaseRunID != record.fixture.PhaseRunID || decoded.RuntimeRunID != record.fixture.RuntimeRunID ||
		decoded.OperationID.String() != startOperationID ||
		!bytes.Equal(startDigest, decoded.CanonicalRequestDigest[:]) ||
		startWorkspaceID != record.fixture.PersonalWorkspaceID.String() ||
		!bytes.Equal(capsuleDigest, wantDigest[:]) || !bytes.Equal(outboxCapsuleDigest, wantDigest[:]) ||
		!bytes.Equal(outboxPayload, capsuleWire) || !bytes.Equal(outboxPayloadDigest, wantDigest[:]) ||
		!bytes.Equal(auditDigest, wantAuditDigest[:]) || audit.SchemaVersion != postgresCapsuleAuditSchemaV1 ||
		audit.CapsuleID != capsuleID || audit.CapsuleDigest != wantDigest.String() ||
		audit.RuntimeRunID != record.fixture.RuntimeRunID.String() || audit.DispatchOperationID != dispatchOperationID ||
		audit.StartOperationID != decoded.OperationID.String() ||
		audit.StartDigest != decoded.CanonicalRequestDigest.String() || audit.SandboxLeaseID != decoded.SandboxLeaseID.String() ||
		audit.LeaseGeneration != uint64(decoded.LeaseGeneration) || audit.LeaseFence != uint64(decoded.LeaseFence) ||
		runtimeBindingEvidenceErr != nil || inputEvidenceErr != nil || runtimeViewEvidenceErr != nil ||
		gatewayEvidenceErr != nil || committedAtErr != nil {
		return newError(ErrorIntegrityConflict)
	}
	record.acceptedStart.OperationID = decoded.OperationID
	record.acceptedStartDigest = decoded.CanonicalRequestDigest
	record.capsule = retainedExecutionCapsule{
		snapshot: RuntimeCapsuleSnapshot{
			State: CapsulePrepared, CapsuleID: ExecutionCapsuleID{value: capsuleID}, Digest: wantDigest,
		},
		dispatchOperationID: OperationID{value: dispatchOperationID},
		wire:                append([]byte(nil), capsuleWire...), decoded: decoded,
	}
	return nil
}

func optionalAuditEvidenceDigest(value string, required bool) error {
	if value == "" {
		if required {
			return newError(ErrorIntegrityConflict)
		}
		return nil
	}
	if !required {
		return newError(ErrorIntegrityConflict)
	}
	_, err := digestFromCanonicalText(value)
	return err
}

func (authority *PostgresAuthority) ClaimDispatch(
	ctx context.Context,
	request DispatchClaimRequest,
) (DispatchDelivery, error) {
	if ctx == nil || ctx.Err() != nil {
		return DispatchDelivery{}, newError(ErrorDependencyUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return DispatchDelivery{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, request.RuntimeRunID)
	if err == sql.ErrNoRows {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return DispatchDelivery{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.capsule.snapshot.State != CapsulePrepared ||
		request.CapsuleID != record.capsule.snapshot.CapsuleID || request.Digest != record.capsule.snapshot.Digest {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	now := postgresTimestamp(authority.now())
	projected := snapshot(record, SnapshotSchemaCurrent)
	if !capsulePrerequisitesReadyAt(projected, now) || !executionCapsuleDispatchCurrent(record, now) {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	if err := authority.validatePostgresCurrentGatewayAuthority(ctx, tx, record, now); err != nil {
		return DispatchDelivery{}, newError(ErrorAuthorizationDenied)
	}
	var disposition DispatchDisposition
	var deliveryCount uint64
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`UPDATE %s SET
		disposition=$1, delivery_count=delivery_count+1,
		first_attempt_at=COALESCE(first_attempt_at,$2), last_attempt_at=$2
		WHERE operation_id=$3 AND disposition IN ($4,$5)
		RETURNING disposition, delivery_count`, authority.table("runtime_execution_dispatch_delivery")),
		DispatchClaimed, now, record.capsule.dispatchOperationID.String(),
		DispatchPending, DispatchClaimed).Scan(&disposition, &deliveryCount)
	if err == sql.ErrNoRows {
		return DispatchDelivery{}, newError(ErrorIntegrityConflict)
	}
	if err != nil {
		return DispatchDelivery{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return DispatchDelivery{}, normalizeRuntimePersistenceFailure(err)
	}
	return DispatchDelivery{
		OperationID: record.capsule.dispatchOperationID, RuntimeRunID: request.RuntimeRunID,
		CapsuleID: record.capsule.snapshot.CapsuleID, CapsuleDigest: record.capsule.snapshot.Digest,
		Capsule: append([]byte(nil), record.capsule.wire...), Disposition: disposition,
		DeliveryCount: deliveryCount,
	}, nil
}

func (authority *PostgresAuthority) validatePostgresCurrentGatewayAuthority(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	now time.Time,
) error {
	if record.gateway.Applicability != GatewayPrerequisiteRequired {
		return nil
	}
	grant := record.gateway.CurrentGrant
	if err := authority.verifyPostgresGatewayCurrent(
		ctx, tx, record.fixture.RuntimeRunID, record.gateway.OperationID,
		grant.GatewayGrantID, grant.Generation,
	); err != nil {
		return err
	}
	reservation, err := authority.validatePostgresQuotaReservationFact(ctx, tx, QuotaReservationValidationFact{
		QuotaReservationID: grant.QuotaReservationID, Generation: grant.QuotaReservationGeneration,
		Mode: grant.QuotaReservationMode, PersonalWorkspaceID: record.fixture.PersonalWorkspaceID,
		TaskID: record.fixture.TaskID, PhaseRunID: record.fixture.PhaseRunID,
		AuthorizationGeneration: grant.OwnerAuthorityGeneration, Capability: ProviderCapabilityRequired,
		GatewayRoutePolicyID:         grant.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: grant.GatewayRoutePolicyGeneration,
		CapabilityScope:              grant.CapabilityScope, ValidAt: now.UTC(),
	})
	if err != nil || grant.ExpiresAt.After(reservation.ExpiresAt) {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) AcknowledgeDispatch(
	ctx context.Context,
	request DispatchAcknowledgementRequest,
) (DispatchAcknowledgement, error) {
	if ctx == nil || ctx.Err() != nil {
		return DispatchAcknowledgement{}, newError(ErrorDependencyUnavailable)
	}
	if request.AckDigest == (Digest{}) {
		return DispatchAcknowledgement{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return DispatchAcknowledgement{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, request.RuntimeRunID)
	if err == sql.ErrNoRows {
		return DispatchAcknowledgement{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return DispatchAcknowledgement{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.capsule.snapshot.State != CapsulePrepared ||
		request.OperationID != record.capsule.dispatchOperationID ||
		request.CapsuleID != record.capsule.snapshot.CapsuleID ||
		request.CapsuleDigest != record.capsule.snapshot.Digest {
		return DispatchAcknowledgement{}, newError(ErrorAuthorizationDenied)
	}
	var disposition DispatchDisposition
	var retainedAck []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT disposition, ack_digest
		FROM %s WHERE operation_id=$1 FOR UPDATE`, authority.table("runtime_execution_dispatch_delivery")),
		request.OperationID.String()).Scan(&disposition, &retainedAck)
	if err == sql.ErrNoRows {
		return DispatchAcknowledgement{}, newError(ErrorIntegrityConflict)
	}
	if err != nil {
		return DispatchAcknowledgement{}, normalizeRuntimePersistenceFailure(err)
	}
	if disposition == DispatchAcknowledged {
		if !bytes.Equal(retainedAck, request.AckDigest[:]) {
			return DispatchAcknowledgement{}, newError(ErrorIntegrityConflict)
		}
		if err := tx.Commit(); err != nil {
			return DispatchAcknowledgement{}, normalizeRuntimePersistenceFailure(err)
		}
		return postgresDispatchAcknowledgement(record, request.AckDigest), nil
	}
	if disposition != DispatchClaimed || len(retainedAck) != 0 {
		return DispatchAcknowledgement{}, newError(ErrorIntegrityConflict)
	}
	now := postgresTimestamp(authority.now())
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		disposition=$1, ack_digest=$2, acknowledged_at=$3
		WHERE operation_id=$4 AND disposition=$5 AND ack_digest IS NULL`,
		authority.table("runtime_execution_dispatch_delivery")),
		DispatchAcknowledged, request.AckDigest[:], now, request.OperationID.String(), DispatchClaimed)
	if err != nil {
		return DispatchAcknowledgement{}, normalizeRuntimePersistenceFailure(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return DispatchAcknowledgement{}, newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return DispatchAcknowledgement{}, normalizeRuntimePersistenceFailure(err)
	}
	return postgresDispatchAcknowledgement(record, request.AckDigest), nil
}

func postgresDispatchAcknowledgement(record *runtimeRecord, ackDigest Digest) DispatchAcknowledgement {
	return DispatchAcknowledgement{
		OperationID:   record.capsule.dispatchOperationID,
		RuntimeRunID:  record.fixture.RuntimeRunID,
		CapsuleID:     record.capsule.snapshot.CapsuleID,
		CapsuleDigest: record.capsule.snapshot.Digest,
		AckDigest:     ackDigest, Disposition: DispatchAcknowledged,
	}
}
