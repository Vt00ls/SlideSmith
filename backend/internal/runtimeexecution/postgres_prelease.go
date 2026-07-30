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

var (
	errPostgresLeaseRuntimeDeadlineExpired = errors.New("runtime deadline expired before lease commit")
	errPostgresLeaseAuthorityExpired       = errors.New("admission authority expired before lease commit")
)

func (authority *PostgresAuthority) advancePostgresPreLease(
	ctx context.Context,
	start StartRuntimeRun,
	startDecision RuntimeDecision,
) (RuntimeDecision, error) {
	startDecision, err := authority.advancePostgresPreLeaseTimeBounds(ctx, start, startDecision)
	if err != nil || startDecision.Snapshot.State == RuntimeTerminal {
		return startDecision, err
	}
	snapshot := startDecision.Snapshot
	if snapshot.State == RuntimeTerminal || snapshot.Lease.AcquireStatus == LeaseGranted ||
		snapshot.State != RuntimeWaitingForLease && snapshot.State != RuntimeReconciling {
		return startDecision, nil
	}
	now := postgresTimestamp(authority.now())
	if authority.leaseAcquisition == nil {
		return startDecision, nil
	}
	request := postgresLeaseAcquisitionRequest(start, snapshot)
	if !validLeaseAcquisitionRequest(request) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	observation, err := authority.leaseAcquisition.ObserveLeaseAcquisition(ctx, request)
	if err != nil {
		observation = LeaseAcquisitionObservation{Disposition: LeaseAcquisitionAmbiguousPrerequisite}
	}
	if !validLeaseAcquisitionObservation(observation) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	switch observation.Disposition {
	case LeaseAcquisitionTemporaryUnavailable:
		return authority.withCurrentPostgresSnapshot(ctx, start, startDecision)
	case LeaseAcquisitionRetryablePrerequisite, LeaseAcquisitionAmbiguousPrerequisite:
		if snapshot.State == RuntimeReconciling {
			return authority.withCurrentPostgresSnapshot(ctx, start, startDecision)
		}
		operationID := stablePreLeaseReconciliationOperation(snapshot.Lease)
		fixture := RuntimeFixture{
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Owner: start.Authority, RuntimeRevision: snapshot.RuntimeRevision,
			OperationGeneration: snapshot.Operation.Generation, RuntimeFence: snapshot.RuntimeFence,
			SafetyEpoch: start.ReleaseSafetyEpoch,
		}
		if _, err := authority.persistReconciliationFoundation(ctx, newReconciliationFoundationIntent(
			fixture, operationID, start.Authority, ReconciliationTransportAmbiguous,
		)); err != nil {
			return RuntimeDecision{}, err
		}
		return authority.withCurrentPostgresSnapshot(ctx, start, startDecision)
	case LeaseAcquisitionPermanentFailure:
		terminal, err := authority.executePostgresPreLeaseTerminal(
			ctx, start, RuntimeRejected, terminalReasonForPermanentFailure(observation.PermanentFailure), now,
		)
		if err != nil {
			return RuntimeDecision{}, err
		}
		startDecision.Snapshot = terminal.Snapshot
		return startDecision, nil
	case LeaseAcquisitionReady:
		committed, err := authority.commitPostgresPreLease(ctx, start, request)
		if errors.Is(err, errPostgresLeaseRuntimeDeadlineExpired) ||
			errors.Is(err, errPostgresLeaseAuthorityExpired) {
			outcome := RuntimeRejected
			reason := PreLeaseTerminalAdmissionAuthorityExpired
			if errors.Is(err, errPostgresLeaseRuntimeDeadlineExpired) {
				outcome = RuntimeTimedOut
				reason = PreLeaseTerminalRuntimeDeadline
			}
			terminal, terminalErr := authority.executePostgresPreLeaseTerminal(
				ctx, start, outcome, reason, postgresTimestamp(authority.now()),
			)
			if terminalErr != nil {
				return RuntimeDecision{}, terminalErr
			}
			startDecision.Snapshot = terminal.Snapshot
			return startDecision, nil
		}
		if err != nil {
			return RuntimeDecision{}, err
		}
		startDecision.Snapshot = committed
		return startDecision, nil
	default:
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
}

func (authority *PostgresAuthority) advancePostgresPreLeaseTimeBounds(
	ctx context.Context,
	start StartRuntimeRun,
	startDecision RuntimeDecision,
) (RuntimeDecision, error) {
	if startDecision.Fact.Disposition != DecisionAccepted {
		return startDecision, nil
	}
	snapshot := startDecision.Snapshot
	if snapshot.State == RuntimeTerminal || snapshot.Lease.AcquireStatus == LeaseGranted ||
		snapshot.State != RuntimeWaitingForLease && snapshot.State != RuntimeReconciling {
		return startDecision, nil
	}
	now := postgresTimestamp(authority.now())
	if outcome, reason, terminal := preLeaseTimeBoundTerminal(
		now, snapshot.Deadline, snapshot.LeaseAcquireBy,
	); terminal {
		terminal, err := authority.executePostgresPreLeaseTerminal(
			ctx, start, outcome, reason, now,
		)
		if err != nil {
			return RuntimeDecision{}, err
		}
		startDecision.Snapshot = terminal.Snapshot
		return startDecision, nil
	}
	return startDecision, nil
}

func postgresLeaseAcquisitionRequest(start StartRuntimeRun, snapshot RuntimeSnapshot) LeaseAcquisitionRequest {
	return LeaseAcquisitionRequest{
		RuntimeRunID: start.RuntimeRunID, WorkItemID: snapshot.Operation.WorkItemID,
		AdmissionGrantID: snapshot.Operation.AdmissionGrantID,
		GrantGeneration:  snapshot.Operation.GrantGeneration, StartOperationID: start.OperationID,
		StartDigest: start.CanonicalRequestDigest, OperationID: snapshot.Lease.AcquireOperationID,
		Digest: snapshot.Lease.AcquireDigest, ExecutionNodeID: snapshot.Operation.ExecutionNodeID,
		NodeCapacityGeneration: snapshot.Operation.NodeCapacityGeneration,
		ResourceClassID:        snapshot.Operation.ResourceClassID, ExecutionPolicyID: snapshot.Operation.ExecutionPolicyID,
		SchedulerEpoch: snapshot.Operation.SchedulerEpoch, PolicyVersion: snapshot.Operation.PolicyVersion,
		SafetyEpoch: start.ReleaseSafetyEpoch, RuntimeFence: snapshot.RuntimeFence,
		Deadline: snapshot.Deadline, LeaseAcquireBy: snapshot.LeaseAcquireBy,
	}
}

func (authority *PostgresAuthority) withCurrentPostgresSnapshot(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	snapshot, err := authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		return RuntimeDecision{}, err
	}
	decision.Snapshot = snapshot
	return decision, nil
}

func stablePreLeaseReconciliationOperation(lease RuntimeLeaseSnapshot) OperationID {
	digest := digestBytes([]byte(
		"slidesmith.runtime-execution.pre-lease-reconciliation/v1\n" +
			lease.AcquireOperationID.String() + "\n" + lease.AcquireDigest.String(),
	))
	return OperationID{value: fmt.Sprintf("prelease-reconcile-%x", digest[:16])}
}

type postgresLeaseCommitState struct {
	SchemaVersion           SchemaVersion            `json:"schema_version"`
	RuntimeRunID            string                   `json:"runtime_run_id"`
	StartOperationID        string                   `json:"start_operation_id"`
	StartDigest             Digest                   `json:"start_digest"`
	LeaseAcquireOperationID string                   `json:"lease_acquire_operation_id"`
	LeaseAcquireDigest      Digest                   `json:"lease_acquire_digest"`
	AdmissionGrantID        string                   `json:"admission_grant_id"`
	WorkItemID              string                   `json:"work_item_id"`
	GrantGeneration         AdmissionGrantGeneration `json:"grant_generation"`
	ExecutionNodeID         string                   `json:"execution_node_id"`
	NodeCapacityGeneration  uint64                   `json:"node_capacity_generation"`
	ResourceClassID         string                   `json:"resource_class_id"`
	ExecutionPolicyID       string                   `json:"execution_policy_id"`
	SchedulerEpoch          uint64                   `json:"scheduler_epoch"`
	PolicyVersion           uint64                   `json:"policy_version"`
	SafetyEpoch             ReleaseSafetyEpoch       `json:"safety_epoch"`
}

func encodePostgresLeaseCommit(request LeaseAcquisitionRequest) ([]byte, error) {
	return json.Marshal(postgresLeaseCommitState{
		SchemaVersion: SchemaV1, RuntimeRunID: request.RuntimeRunID.String(),
		StartOperationID: request.StartOperationID.String(), StartDigest: request.StartDigest,
		LeaseAcquireOperationID: request.OperationID.String(), LeaseAcquireDigest: request.Digest,
		AdmissionGrantID: request.AdmissionGrantID.String(), WorkItemID: request.WorkItemID.String(),
		GrantGeneration: request.GrantGeneration, ExecutionNodeID: request.ExecutionNodeID.String(),
		NodeCapacityGeneration: request.NodeCapacityGeneration,
		ResourceClassID:        request.ResourceClassID.String(), ExecutionPolicyID: request.ExecutionPolicyID.String(),
		SchedulerEpoch: request.SchedulerEpoch, PolicyVersion: request.PolicyVersion,
		SafetyEpoch: request.SafetyEpoch,
	})
}

func (authority *PostgresAuthority) commitPostgresPreLease(
	ctx context.Context,
	start StartRuntimeRun,
	request LeaseAcquisitionRequest,
) (RuntimeSnapshot, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := authority.loadRuntimeForUpdate(ctx, tx, start.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !authorized(record, start.PersonalWorkspaceID, start.Authority) {
		return RuntimeSnapshot{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.lease.AcquireStatus == LeaseGranted {
		if err := authority.validateRetainedPostgresLease(ctx, tx, record, request); err != nil {
			return RuntimeSnapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
		return snapshot(record, SnapshotSchemaCurrent), nil
	}
	if record.fixture.State == RuntimeTerminal {
		if err := tx.Commit(); err != nil {
			return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
		}
		return snapshot(record, SnapshotSchemaCurrent), nil
	}
	committedAt := postgresTimestamp(authority.now())
	if !committedAt.Before(record.deadline) {
		return RuntimeSnapshot{}, errPostgresLeaseRuntimeDeadlineExpired
	}
	if !committedAt.Before(record.leaseAcquireBy) {
		return RuntimeSnapshot{}, errPostgresLeaseAuthorityExpired
	}
	if record.fixture.State != RuntimeWaitingForLease && record.fixture.State != RuntimeReconciling ||
		record.fixture.Outcome != RuntimeOutcomeNone || !validAcceptedStartBinding(record.operation, record.lease) ||
		!postgresLeaseRequestMatchesRecord(start, request, record) ||
		record.capacity != (RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone,
			Physical: PhysicalCapacityNotApplicable,
		}) || record.capacityEvidence != (RuntimeCapacityEvidenceSnapshot{}) {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if authority.schedulerLeaseAttachmentParticipant == nil {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	node, err := authority.loadPostgresNodeForUpdate(ctx, tx, record.operation.ExecutionNodeID)
	if err != nil || !postgresNodeEligibleForLease(node, record, committedAt) {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if start.ProviderCapability == ProviderCapabilityRequired {
		if start.ProviderBinding == nil || authority.quotaReservationParticipant == nil {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
		reservationFact := QuotaReservationValidationFact{
			QuotaReservationID: start.ProviderBinding.QuotaReservationID,
			Generation:         start.ProviderBinding.Generation, Mode: start.ProviderBinding.Mode,
			PersonalWorkspaceID: start.PersonalWorkspaceID, PhaseRunID: start.PhaseRunID,
			Capability: start.ProviderCapability, ValidAt: committedAt,
		}
		reservationTransaction := &postgresQuotaReservationTransaction{
			tx: tx, function: authority.quotaReservationFunction, fact: reservationFact,
		}
		if err := authority.quotaReservationParticipant.ParticipateQuotaReservation(
			ctx, reservationTransaction, reservationFact,
		); err != nil || !reservationTransaction.called {
			return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
		}
	} else if start.ProviderCapability != ProviderCapabilityNone {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if authority.failAt(PersistenceFaultBeforeLeaseCommit) {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	previousRevision := record.fixture.RuntimeRevision
	beforeState := record.fixture.State
	var decisionSequence, leaseSequence, sandboxSequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("runtime_execution_decision_sequence")+"')").Scan(&decisionSequence); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("runtime_execution_sandbox_lease_sequence")+"')").Scan(&leaseSequence); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("runtime_execution_sandbox_sequence")+"')").Scan(&sandboxSequence); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	decisionID := RuntimeDecisionID{value: fmt.Sprintf("runtime-decision-postgres-%020d", decisionSequence)}
	leaseID := SandboxLeaseID{value: fmt.Sprintf("sandbox-lease-postgres-%020d", leaseSequence)}
	sandboxID := SandboxID{value: fmt.Sprintf("sandbox-instance-postgres-%020d", sandboxSequence)}
	record.fixture.RuntimeRevision++
	record.fixture.State = RuntimePreparingPrerequisites
	record.lease.AcquireStatus = LeaseGranted
	record.lease.LeaseID = leaseID
	record.lease.Generation = 1
	record.lease.Fence = 1
	record.lease.Disposition = LeaseActive
	record.lease.ExpiresAt = earliestTime(committedAt.Add(90*time.Second), record.deadline,
		record.leaseAcquireBy, node.ExpiresAt, node.AuthorizationExpiresAt)
	record.lease.SandboxID = sandboxID
	record.lease.SandboxGeneration = node.LastSandboxGeneration + 1
	record.lease.SandboxFence = node.LastSandboxFence + 1
	record.lease.WorkerAuthorityID = node.WorkerAuthorityID
	record.lease.WorkerGeneration = node.WorkerGeneration
	record.lease.NodeAuthorityID = node.NodeAuthorityID
	record.lease.AuthorizationGeneration = node.AuthorizationGeneration
	record.lease.AuthorizationExpiresAt = node.AuthorizationExpiresAt
	node.Occupancy = NodeOccupied
	node.Containment = ContainmentPending
	node.Reset = ResetRequired
	node.ActiveRuntimeRunID = record.fixture.RuntimeRunID
	node.ActiveLeaseID = leaseID
	record.node = nodeSnapshot(node.ExecutionNodeFixture)
	record.capacity.Physical = PhysicalCapacityOccupied
	record.reconciliation = ReconciliationStable
	if record.readiness == (RuntimeReadinessSnapshot{}) {
		record.readiness = initialRuntimeReadiness(start)
	}
	record.readiness.Lease = leasePrerequisiteFact(record.lease)
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	fact := RuntimeDecisionFact{
		DecisionID: decisionID, Disposition: DecisionAccepted,
		OperationID: request.OperationID, CanonicalRequestDigest: request.Digest,
		PreviousRuntimeRevision: previousRevision, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: RuntimePreparingPrerequisites, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	attachmentFact := SchedulerLeaseAttachmentFact{
		WorkItemID: request.WorkItemID, AdmissionGrantID: request.AdmissionGrantID,
		GrantGeneration: request.GrantGeneration, RuntimeRunID: request.RuntimeRunID,
		StartOperationID: request.StartOperationID, StartDigest: request.StartDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		LeaseAcquireOperationID: request.OperationID, LeaseAcquireDigest: request.Digest,
		SandboxLeaseID: leaseID, LeaseGeneration: record.lease.Generation, LeaseFence: record.lease.Fence,
		SandboxID: record.lease.SandboxID, SandboxGeneration: record.lease.SandboxGeneration,
		SandboxFence:    record.lease.SandboxFence,
		ExecutionNodeID: request.ExecutionNodeID, NodeCapacityGeneration: request.NodeCapacityGeneration,
		ResourceClassID: request.ResourceClassID, ExecutionPolicyID: request.ExecutionPolicyID,
		SchedulerEpoch: request.SchedulerEpoch, PolicyVersion: request.PolicyVersion, AttachedAt: committedAt,
	}
	attachmentTransaction := &postgresSchedulerLeaseAttachmentTransaction{
		tx: tx, function: authority.schedulerLeaseAttachmentFunction, fact: attachmentFact,
	}
	if err := authority.schedulerLeaseAttachmentParticipant.ParticipateLeaseAttachment(
		ctx, attachmentTransaction, attachmentFact,
	); err != nil || !attachmentTransaction.called {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	canonical, err := encodePostgresLeaseCommit(request)
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	aggregateState, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET runtime_revision=$1,
		runtime_state=$2, aggregate_state=$3, updated_at=$4 WHERE runtime_run_id=$5
		AND runtime_revision=$6 AND runtime_state=$7 AND runtime_outcome=$8`,
		authority.table("runtime_execution_runtimes")), record.fixture.RuntimeRevision,
		record.fixture.State, aggregateState, committedAt, start.RuntimeRunID.String(),
		previousRevision, beforeState, RuntimeOutcomeNone)
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if err := authority.updatePostgresNode(ctx, tx, node, committedAt); err != nil {
		return RuntimeSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		lease_acquire_operation_id, lease_acquire_digest, runtime_run_id,
		start_operation_id, start_digest, work_item_id, admission_grant_id,
		grant_generation, execution_node_id, node_capacity_generation,
		resource_class_id, execution_policy_id, scheduler_epoch, policy_version,
		safety_epoch, sandbox_lease_id, lease_generation, lease_fence,
		lease_disposition, lease_expires_at, sandbox_id, sandbox_generation, sandbox_fence,
		worker_authority_id, worker_generation, node_authority_id,
		authorization_generation, authorization_expires_at, catalog_safety_epoch, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
		$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
		authority.table("runtime_execution_prelease_leases")), request.OperationID.String(), request.Digest[:],
		request.RuntimeRunID.String(), request.StartOperationID.String(), request.StartDigest[:],
		request.WorkItemID.String(), request.AdmissionGrantID.String(), request.GrantGeneration,
		request.ExecutionNodeID.String(), request.NodeCapacityGeneration, request.ResourceClassID.String(),
		request.ExecutionPolicyID.String(), request.SchedulerEpoch, request.PolicyVersion,
		request.SafetyEpoch, leaseID.String(), record.lease.Generation, record.lease.Fence,
		record.lease.Disposition, record.lease.ExpiresAt, record.lease.SandboxID.String(),
		record.lease.SandboxGeneration, record.lease.SandboxFence, record.lease.WorkerAuthorityID.String(),
		record.lease.WorkerGeneration, record.lease.NodeAuthorityID.String(),
		record.lease.AuthorizationGeneration, record.lease.AuthorizationExpiresAt,
		record.catalogSafetyEpoch, committedAt); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	decisionState, err := encodePostgresDecisionFact(fact)
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_decisions")),
		decisionID.String(), start.RuntimeRunID.String(), request.OperationID.String(), request.Digest[:],
		previousRevision, record.fixture.RuntimeRevision, decisionState, committedAt); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		personal_workspace_id, runtime_run_id, command_kind, operation_id,
		canonical_request_digest, canonical_request, decision_id
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_requests")),
		start.PersonalWorkspaceID.String(), start.RuntimeRunID.String(), postgresLeaseCommitCommandKind,
		request.OperationID.String(), request.Digest[:], canonical, decisionID.String()); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		runtime_run_id, runtime_revision, decision_id, aggregate_state
	) VALUES ($1,$2,$3,$4)`, authority.table("runtime_execution_revisions")),
		start.RuntimeRunID.String(), record.fixture.RuntimeRevision, decisionID.String(), aggregateState); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	auditID := fmt.Sprintf("runtime-audit-postgres-%020d", decisionSequence)
	auditState := newPostgresMandatoryAuditState(postgresMandatoryAuditInput{
		AuditFactID: auditID, Action: postgresAuditLeaseCommitted, Decision: fact,
		RuntimeRunID: start.RuntimeRunID, RequestDigest: request.Digest, Authority: start.Authority,
		BeforeState: beforeState, AfterState: record.fixture.State,
		BeforeOperationGeneration: record.fixture.OperationGeneration,
		AfterOperationGeneration:  record.fixture.OperationGeneration,
		BeforeRuntimeFence:        record.fixture.RuntimeFence, AfterRuntimeFence: record.fixture.RuntimeFence,
		PolicyEpoch: request.SchedulerEpoch, BeforeSafetyEpoch: request.SafetyEpoch,
		AfterSafetyEpoch: request.SafetyEpoch, OccurredAt: committedAt, RecordedAt: committedAt,
	})
	auditBytes, err := auditState.encode()
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	auditDigest, err := auditState.canonicalDigest()
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, decision_id, runtime_run_id, operation_id, request_digest,
		schema_version, integrity_version, owning_module, canonical_digest,
		authority_kind, authority_id, authority_generation, action, result,
		before_revision, after_revision, occurred_at, recorded_at, source_clock_id, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		authority.table("runtime_execution_mandatory_audit")), auditID, decisionID.String(),
		start.RuntimeRunID.String(), request.OperationID.String(), request.Digest[:], auditState.SchemaVersion,
		auditState.IntegrityVersion, auditState.OwningModule, auditDigest[:], auditState.AuthorityKind,
		auditState.AuthorityID, auditState.AuthorityGeneration, auditState.Action, auditState.Result,
		auditState.BeforeRevision, auditState.AfterRevision, committedAt, committedAt,
		auditState.SourceClockID, auditBytes); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) || authority.failAt(PersistenceFaultBeforeOutbox) {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	scopeDigest := authorityScopeDigest(start.PersonalWorkspaceID, start.RuntimeRunID)
	payloadDigest := digestBytes(canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, decision_id, runtime_run_id, canonical_request_digest,
		authority_scope_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_outbox")),
		request.OperationID.String(), decisionID.String(), start.RuntimeRunID.String(), request.Digest[:],
		scopeDigest[:], canonical, payloadDigest[:], committedAt); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_outbox_delivery")),
		request.OperationID.String(), OutboxPending); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		fact_id, audit_fact_id, audit_canonical_digest, fact_revision,
		projection_schema_version, audit_delivery_status, telemetry_delivery_status, degraded
	) VALUES ($1,$2,$3,$4,$5,$6,$6,FALSE)`, authority.table("runtime_execution_projection_backlog")),
		decisionID.String(), auditID, auditDigest[:], record.fixture.RuntimeRevision,
		SchemaV1, ProjectionPending); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeCommit) || authority.failAt(PersistenceFaultBeforeLeaseCommit) {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	committed := snapshot(record, SnapshotSchemaCurrent)
	if authority.failAt(PersistenceFaultAfterCommit) || authority.failAt(PersistenceFaultAfterLeaseCommit) {
		return RuntimeSnapshot{}, newError(ErrorReconciliationRequired)
	}
	authority.deliverProjection(ctx, ProjectionFact{
		DecisionID: decisionID, RuntimeRunID: start.RuntimeRunID, OperationID: request.OperationID,
		CanonicalDigest: request.Digest, RuntimeRevision: record.fixture.RuntimeRevision,
		AuditFactID: auditID, AuditCanonicalDigest: auditDigest, ProjectionSchemaVersion: SchemaV1,
	})
	if authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeSnapshot{}, newError(ErrorReconciliationRequired)
	}
	return committed, nil
}

func postgresLeaseRequestMatchesRecord(
	start StartRuntimeRun,
	request LeaseAcquisitionRequest,
	record *runtimeRecord,
) bool {
	return request.RuntimeRunID == start.RuntimeRunID && request.StartOperationID == start.OperationID &&
		request.StartDigest == start.CanonicalRequestDigest && request.OperationID == record.lease.AcquireOperationID &&
		request.Digest == record.lease.AcquireDigest && request.WorkItemID == record.operation.WorkItemID &&
		request.AdmissionGrantID == record.operation.AdmissionGrantID &&
		request.GrantGeneration == record.operation.GrantGeneration &&
		request.ExecutionNodeID == record.operation.ExecutionNodeID &&
		request.NodeCapacityGeneration == record.operation.NodeCapacityGeneration &&
		request.ResourceClassID == record.operation.ResourceClassID &&
		request.ExecutionPolicyID == record.operation.ExecutionPolicyID &&
		request.SchedulerEpoch == record.operation.SchedulerEpoch &&
		request.PolicyVersion == record.operation.PolicyVersion && request.SafetyEpoch == record.fixture.SafetyEpoch &&
		request.RuntimeFence == record.fixture.RuntimeFence && request.Deadline.Equal(record.deadline) &&
		request.LeaseAcquireBy.Equal(record.leaseAcquireBy)
}

func postgresNodeEligibleForLease(
	node *postgresExecutionNodeRecord,
	record *runtimeRecord,
	now time.Time,
) bool {
	return node != nil && node.Generation == NodeGeneration(record.operation.NodeCapacityGeneration) &&
		node.Readiness == NodeReady && !node.Quarantined && node.Occupancy == NodeUnoccupied &&
		node.Containment == ContainmentEstablished && node.Reset == ResetCompleted &&
		!node.AttestedAt.After(now) && now.Before(node.ExpiresAt) && now.Before(node.AuthorizationExpiresAt) &&
		node.ResourceClassID == record.operation.ResourceClassID &&
		node.ExecutionPolicyID == record.operation.ExecutionPolicyID &&
		node.ReleaseSafetyEpoch == record.fixture.SafetyEpoch &&
		node.CatalogSafetyEpoch == record.catalogSafetyEpoch && validOpaqueID(node.WorkerAuthorityID.String()) &&
		node.WorkerGeneration > 0 && validOpaqueID(node.NodeAuthorityID.String()) &&
		node.AuthorizationGeneration > 0
}

func (authority *PostgresAuthority) validateRetainedPostgresLease(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	request LeaseAcquisitionRequest,
) error {
	var operationID, runtimeRunID, startOperationID, workItemID, grantID, nodeID string
	var resourceClassID, executionPolicyID, leaseID, sandboxID string
	var workerAuthorityID, nodeAuthorityID string
	var acquireDigest, startDigest []byte
	var grantGeneration AdmissionGrantGeneration
	var nodeGeneration, schedulerEpoch, policyVersion uint64
	var safetyEpoch ReleaseSafetyEpoch
	var leaseGeneration LeaseGeneration
	var leaseFence LeaseFence
	var disposition LeaseDisposition
	var leaseExpiresAt, authorizationExpiresAt time.Time
	var sandboxGeneration SandboxGeneration
	var sandboxFence SandboxFence
	var workerGeneration WorkerGeneration
	var authorizationGeneration AuthorizationGeneration
	var catalogSafetyEpoch CatalogSafetyEpoch
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT lease_acquire_operation_id,
		lease_acquire_digest, runtime_run_id, start_operation_id, start_digest, work_item_id,
		admission_grant_id, grant_generation, execution_node_id, node_capacity_generation,
		resource_class_id, execution_policy_id, scheduler_epoch, policy_version, safety_epoch,
		sandbox_lease_id, lease_generation, lease_fence, lease_disposition, lease_expires_at,
		sandbox_id, sandbox_generation, sandbox_fence, worker_authority_id, worker_generation,
		node_authority_id, authorization_generation, authorization_expires_at, catalog_safety_epoch
		FROM %s WHERE runtime_run_id=$1`,
		authority.table("runtime_execution_prelease_leases")), request.RuntimeRunID.String()).Scan(
		&operationID, &acquireDigest, &runtimeRunID, &startOperationID, &startDigest, &workItemID,
		&grantID, &grantGeneration, &nodeID, &nodeGeneration, &resourceClassID, &executionPolicyID,
		&schedulerEpoch, &policyVersion, &safetyEpoch, &leaseID, &leaseGeneration, &leaseFence,
		&disposition, &leaseExpiresAt, &sandboxID, &sandboxGeneration, &sandboxFence,
		&workerAuthorityID, &workerGeneration, &nodeAuthorityID, &authorizationGeneration,
		&authorizationExpiresAt, &catalogSafetyEpoch)
	if err != nil || operationID != request.OperationID.String() || !bytes.Equal(acquireDigest, request.Digest[:]) ||
		runtimeRunID != request.RuntimeRunID.String() || startOperationID != request.StartOperationID.String() ||
		!bytes.Equal(startDigest, request.StartDigest[:]) || workItemID != request.WorkItemID.String() ||
		grantID != request.AdmissionGrantID.String() || grantGeneration != request.GrantGeneration ||
		nodeID != request.ExecutionNodeID.String() || nodeGeneration != request.NodeCapacityGeneration ||
		resourceClassID != request.ResourceClassID.String() || executionPolicyID != request.ExecutionPolicyID.String() ||
		schedulerEpoch != request.SchedulerEpoch || policyVersion != request.PolicyVersion || safetyEpoch != request.SafetyEpoch ||
		leaseID != record.lease.LeaseID.String() || leaseGeneration != record.lease.Generation ||
		leaseFence != record.lease.Fence || disposition != record.lease.Disposition ||
		!leaseExpiresAt.Equal(record.lease.ExpiresAt) || sandboxID != record.lease.SandboxID.String() ||
		sandboxGeneration != record.lease.SandboxGeneration || sandboxFence != record.lease.SandboxFence ||
		workerAuthorityID != record.lease.WorkerAuthorityID.String() || workerGeneration != record.lease.WorkerGeneration ||
		nodeAuthorityID != record.lease.NodeAuthorityID.String() ||
		authorizationGeneration != record.lease.AuthorizationGeneration ||
		!authorizationExpiresAt.Equal(record.lease.AuthorizationExpiresAt) ||
		catalogSafetyEpoch != record.catalogSafetyEpoch {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}
