package runtimeexecution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type postgresNoLeaseTerminalCommand struct {
	OperationID                 OperationID
	PersonalWorkspaceID         PersonalWorkspaceID
	TaskID                      TaskID
	PhaseRunID                  PhaseRunID
	RuntimeRunID                RuntimeRunID
	ExpectedRuntimeRevision     RuntimeRevision
	ExpectedStartOperationID    OperationID
	ExpectedOperationGeneration OperationGeneration
	ExpectedRuntimeFence        RuntimeFence
	Authority                   RuntimeAuthority
	CancellationReason          CancellationReason
	SafetyEpoch                 ReleaseSafetyEpoch
	OccurredAt                  time.Time
	CanonicalRequestDigest      Digest
	Outcome                     RuntimeOutcome
	PreLeaseTerminalReason      PreLeaseTerminalReason
}

// executePostgresCancel linearizes an accepted pre-lease cancellation. The
// locked Runtime row and absence of a lease root are the proof that no Sandbox
// Lease, process, or dispatch can have committed for this Runtime fence.
func (authority *PostgresAuthority) executePostgresCancel(
	ctx context.Context,
	command CancelRuntimeRun,
	binding retainedCommandBindingValue,
) (RuntimeDecision, error) {
	return authority.executePostgresNoLeaseTerminal(ctx, postgresNoLeaseTerminalCommand{
		OperationID: command.OperationID, PersonalWorkspaceID: command.PersonalWorkspaceID,
		TaskID: command.TaskID, PhaseRunID: command.PhaseRunID, RuntimeRunID: command.RuntimeRunID,
		ExpectedRuntimeRevision:     command.ExpectedRuntimeRevision,
		ExpectedStartOperationID:    command.ExpectedStartOperationID,
		ExpectedOperationGeneration: command.ExpectedOperationGeneration,
		ExpectedRuntimeFence:        command.ExpectedRuntimeFence, Authority: command.Authority,
		CancellationReason: command.Reason, SafetyEpoch: command.SafetyEpoch,
		OccurredAt: command.OccurredAt, CanonicalRequestDigest: command.CanonicalRequestDigest,
		Outcome: RuntimeCancelled,
	}, binding)
}

func (authority *PostgresAuthority) executePostgresNoLeaseTerminal(
	ctx context.Context,
	command postgresNoLeaseTerminalCommand,
	binding retainedCommandBindingValue,
) (RuntimeDecision, error) {
	if command.Outcome != RuntimeCancelled && command.Outcome != RuntimeRejected && command.Outcome != RuntimeTimedOut ||
		command.Outcome == RuntimeCancelled && (command.CancellationReason < CancellationUserRequested ||
			command.CancellationReason > CancellationAdministratorRequested) ||
		command.Outcome != RuntimeCancelled && command.PreLeaseTerminalReason == PreLeaseTerminalNone {
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !authorized(record, command.PersonalWorkspaceID, command.Authority) {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if record.fixture.TaskID != command.TaskID || record.fixture.PhaseRunID != command.PhaseRunID {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if replay, found, replayErr := authority.lookupPostgresCommandReplay(ctx, tx, record, binding); replayErr != nil {
		return RuntimeDecision{}, replayErr
	} else if found {
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}

	startBinding := record.operation
	leaseBinding := record.lease
	leasedCancel := command.Outcome == RuntimeCancelled && leaseBinding.AcquireStatus == LeaseGranted &&
		leaseBinding.Disposition == LeaseActive
	validLeaseBinding := validAcceptedStartBinding(startBinding, leaseBinding)
	if leasedCancel {
		validLeaseBinding = validGrantedLeaseBinding(startBinding, leaseBinding)
	}
	if record.fixture.RuntimeRevision != command.ExpectedRuntimeRevision ||
		startBinding.OperationID != command.ExpectedStartOperationID ||
		record.fixture.OperationGeneration != command.ExpectedOperationGeneration ||
		record.fixture.RuntimeFence != command.ExpectedRuntimeFence ||
		record.fixture.SafetyEpoch != command.SafetyEpoch ||
		command.Authority.generation != record.fixture.Owner.generation ||
		!validLeaseBinding {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	validState := record.fixture.State == RuntimeWaitingForLease || record.fixture.State == RuntimeReconciling
	if leasedCancel {
		validState = record.fixture.State >= RuntimePreparingPrerequisites && record.fixture.State < RuntimeTerminal
	}
	wantPhysical := PhysicalCapacityNotApplicable
	if leasedCancel {
		wantPhysical = PhysicalCapacityOccupied
	}
	if !validState ||
		record.fixture.Outcome != RuntimeOutcomeNone ||
		record.cancellation.Status != CancellationNotRequested ||
		record.capacity.LogicalRelease != LogicalCapacityHeld ||
		record.capacity.NoLease != NoLeaseDispositionNone ||
		record.capacity.Physical != wantPhysical ||
		record.capacityEvidence != (RuntimeCapacityEvidenceSnapshot{}) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	var leaseRootCount uint64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1) +
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1)`,
		authority.table("runtime_execution_lease_roots"),
		authority.table("runtime_execution_prelease_leases")), command.RuntimeRunID.String()).Scan(&leaseRootCount); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	wantLeaseRoots := uint64(0)
	if leasedCancel {
		wantLeaseRoots = 1
	}
	if leaseRootCount != wantLeaseRoots {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	var leasedNode *postgresExecutionNodeRecord
	if leasedCancel {
		leasedNode, err = authority.loadPostgresNodeForUpdate(ctx, tx, startBinding.ExecutionNodeID)
		if err != nil || leasedNode.ActiveRuntimeRunID != command.RuntimeRunID ||
			leasedNode.ActiveLeaseID != leaseBinding.LeaseID || leasedNode.Occupancy != NodeOccupied {
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
	}
	if authority.failAt(PersistenceFaultBeforeRuntimeWrite) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	previousRevision := record.fixture.RuntimeRevision
	previousOperationGeneration := record.fixture.OperationGeneration
	previousFence := record.fixture.RuntimeFence
	beforeState := record.fixture.State
	var sequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("runtime_execution_decision_sequence")+"')").Scan(&sequence); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	decisionID := RuntimeDecisionID{value: fmt.Sprintf("runtime-decision-postgres-%020d", sequence)}

	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeTerminal
	record.fixture.Outcome = command.Outcome
	record.operation = startBinding
	record.operation.OperationID = command.OperationID
	record.operation.Digest = command.CanonicalRequestDigest
	record.operation.Generation = record.fixture.OperationGeneration
	if command.Outcome == RuntimeCancelled {
		record.cancellation = RuntimeCancellationSnapshot{
			Status: CancellationAccepted, OperationID: command.OperationID,
			Reason: command.CancellationReason, AcceptedAt: command.OccurredAt.UTC(),
		}
	}
	record.preLeaseTerminalReason = command.PreLeaseTerminalReason
	if leasedCancel {
		record.lease.Generation++
		record.lease.Fence++
		record.lease.SandboxFence++
		record.lease.Disposition = LeaseRevoked
		leasedNode.Occupancy = NodeOccupancyUnknown
		leasedNode.Quarantined = true
		leasedNode.Containment = ContainmentPending
		leasedNode.Reset = ResetRequired
		record.node = nodeSnapshot(leasedNode.ExecutionNodeFixture)
		record.cleanup = RuntimeLeaseCleanupSnapshot{
			Status: LeaseCleanupPending, OperationID: command.OperationID,
			CanonicalRequestDigest: command.CanonicalRequestDigest, StopMainProcess: true,
			StopChildProcesses: true, RevokeSecrets: true, RemoveNetwork: true,
			FenceRuntimeView: true, ReconcileContainment: true,
		}
		record.capacity = RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityReleaseReady, NoLease: NoLeaseDispositionNone,
			Physical: PhysicalCapacityUnknownOrQuarantined,
		}
	} else {
		record.lease.AcquireStatus = LeaseNotRequested
		record.capacity = RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityReleaseReady,
			NoLease:        NoLeaseDispositionRecorded,
			Physical:       PhysicalCapacityNotApplicable,
		}
	}
	baseEvidence := RuntimeFencedOrTerminalEvidence{
		WorkItemID: startBinding.WorkItemID, AdmissionGrantID: startBinding.AdmissionGrantID,
		GrantGeneration: startBinding.GrantGeneration, RuntimeRunID: command.RuntimeRunID,
		StartOperationID: command.ExpectedStartOperationID, StartDigest: startBinding.Digest,
		TerminalDecisionID: decisionID, RuntimeRevision: record.fixture.RuntimeRevision,
		RuntimeFence: record.fixture.RuntimeFence, SchedulerEpoch: startBinding.SchedulerEpoch,
		PolicyVersion:           startBinding.PolicyVersion,
		LeaseAcquireOperationID: leaseBinding.AcquireOperationID, LeaseAcquireDigest: leaseBinding.AcquireDigest,
	}
	record.capacityEvidence = RuntimeCapacityEvidenceSnapshot{RuntimeFencedOrTerminal: baseEvidence}
	if !leasedCancel {
		record.capacityEvidence.NoLeasePhysicalDisposition = NoLeasePhysicalDispositionEvidence{
			WorkItemID: baseEvidence.WorkItemID, AdmissionGrantID: baseEvidence.AdmissionGrantID,
			GrantGeneration: baseEvidence.GrantGeneration, RuntimeRunID: baseEvidence.RuntimeRunID,
			StartOperationID: baseEvidence.StartOperationID, StartDigest: baseEvidence.StartDigest,
			TerminalDecisionID: baseEvidence.TerminalDecisionID, RuntimeRevision: baseEvidence.RuntimeRevision,
			RuntimeFence: baseEvidence.RuntimeFence, SchedulerEpoch: baseEvidence.SchedulerEpoch,
			PolicyVersion:           baseEvidence.PolicyVersion,
			LeaseAcquireOperationID: baseEvidence.LeaseAcquireOperationID,
			LeaseAcquireDigest:      baseEvidence.LeaseAcquireDigest,
			ExecutionNodeID:         startBinding.ExecutionNodeID,
			NodeCapacityGeneration:  startBinding.NodeCapacityGeneration,
		}
	}
	record.reconciliation = ReconciliationStable
	fact := RuntimeDecisionFact{
		DecisionID: decisionID, Disposition: DecisionAccepted,
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		PreviousRuntimeRevision: previousRevision, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: RuntimeTerminal, OutcomeAtDecision: command.Outcome,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	aggregateState, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	committedAt := postgresTimestamp(command.OccurredAt)
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET runtime_revision=$1,
		operation_generation=$2, runtime_fence=$3, runtime_state=$4, runtime_outcome=$5,
		aggregate_state=$6, updated_at=$7 WHERE runtime_run_id=$8 AND runtime_revision=$9
		AND operation_generation=$10 AND runtime_fence=$11 AND runtime_state=$12
		AND runtime_outcome=$13`, authority.table("runtime_execution_runtimes")),
		record.fixture.RuntimeRevision, record.fixture.OperationGeneration, record.fixture.RuntimeFence,
		record.fixture.State, record.fixture.Outcome, aggregateState, committedAt, command.RuntimeRunID.String(),
		previousRevision, previousOperationGeneration, previousFence, beforeState, RuntimeOutcomeNone)
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if leasedCancel {
		if err := authority.updatePostgresLeaseLifecycle(ctx, tx, record.lease, command.RuntimeRunID); err != nil {
			return RuntimeDecision{}, err
		}
		if err := authority.updatePostgresNode(ctx, tx, leasedNode, committedAt); err != nil {
			return RuntimeDecision{}, err
		}
	}
	if authority.failAt(PersistenceFaultBeforeDecision) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	decisionState, err := encodePostgresDecisionFact(fact)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_decisions")),
		decisionID.String(), command.RuntimeRunID.String(), command.OperationID.String(),
		command.CanonicalRequestDigest[:], previousRevision, record.fixture.RuntimeRevision,
		decisionState, committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		personal_workspace_id, runtime_run_id, command_kind, operation_id,
		canonical_request_digest, canonical_request, decision_id
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_requests")),
		command.PersonalWorkspaceID.String(), command.RuntimeRunID.String(), binding.kind,
		command.OperationID.String(), command.CanonicalRequestDigest[:], binding.canonical,
		decisionID.String()); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		runtime_run_id, runtime_revision, decision_id, aggregate_state
	) VALUES ($1,$2,$3,$4)`, authority.table("runtime_execution_revisions")),
		command.RuntimeRunID.String(), record.fixture.RuntimeRevision, decisionID.String(), aggregateState); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultBeforeMandatoryAudit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	auditID := fmt.Sprintf("runtime-audit-postgres-%020d", sequence)
	auditState := newPostgresMandatoryAuditState(postgresMandatoryAuditInput{
		AuditFactID: auditID, Action: binding.auditAction, ReasonCode: binding.auditReasonCode,
		Decision: fact, RuntimeRunID: command.RuntimeRunID, RequestDigest: command.CanonicalRequestDigest,
		Authority: command.Authority, BeforeState: beforeState, AfterState: RuntimeTerminal,
		BeforeOperationGeneration: previousOperationGeneration,
		AfterOperationGeneration:  record.fixture.OperationGeneration,
		BeforeRuntimeFence:        previousFence, AfterRuntimeFence: record.fixture.RuntimeFence,
		PolicyEpoch:       startBinding.SchedulerEpoch,
		BeforeSafetyEpoch: command.SafetyEpoch, AfterSafetyEpoch: command.SafetyEpoch,
		OccurredAt: committedAt, RecordedAt: committedAt,
	})
	auditBytes, err := auditState.encode()
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	auditDigest, err := auditState.canonicalDigest()
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, decision_id, runtime_run_id, operation_id, request_digest,
		schema_version, integrity_version, owning_module, canonical_digest,
		authority_kind, authority_id, authority_generation, action, result,
		before_revision, after_revision, occurred_at, recorded_at, source_clock_id, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		authority.table("runtime_execution_mandatory_audit")), auditID, decisionID.String(),
		command.RuntimeRunID.String(), command.OperationID.String(), command.CanonicalRequestDigest[:],
		auditState.SchemaVersion, auditState.IntegrityVersion, auditState.OwningModule, auditDigest[:],
		auditState.AuthorityKind, auditState.AuthorityID, auditState.AuthorityGeneration,
		auditState.Action, auditState.Result, auditState.BeforeRevision, auditState.AfterRevision,
		committedAt, committedAt, auditState.SourceClockID, auditBytes); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) || authority.failAt(PersistenceFaultBeforeOutbox) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	scopeDigest := authorityScopeDigest(command.PersonalWorkspaceID, command.RuntimeRunID)
	payloadDigest := digestBytes(binding.canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, decision_id, runtime_run_id, canonical_request_digest,
		authority_scope_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_outbox")),
		command.OperationID.String(), decisionID.String(), command.RuntimeRunID.String(),
		command.CanonicalRequestDigest[:], scopeDigest[:], binding.canonical, payloadDigest[:], committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_outbox_delivery")),
		command.OperationID.String(), OutboxPending); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if leasedCancel {
		cleanupDigest := digestBytes(append([]byte("slidesmith.runtime-execution.lease-cleanup/v1\n"), binding.canonical...))
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			operation_id, runtime_run_id, sandbox_lease_id, lease_generation, lease_fence,
			sandbox_id, sandbox_generation, sandbox_fence, stop_main_process,
			stop_child_processes, revoke_secrets, remove_network, fence_runtime_view,
			reconcile_containment, canonical_digest, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,TRUE,TRUE,TRUE,TRUE,TRUE,$9,$10)`,
			authority.table("runtime_execution_lease_cleanup_obligations")), command.OperationID.String(),
			command.RuntimeRunID.String(), record.lease.LeaseID.String(), record.lease.Generation,
			record.lease.Fence, record.lease.SandboxID.String(), record.lease.SandboxGeneration,
			record.lease.SandboxFence, cleanupDigest[:], committedAt); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
	}
	persistedEvidence := postgresCapacityEvidenceFromSnapshot(record.capacityEvidence)
	runtimeFencedBytes, err := json.Marshal(persistedEvidence.RuntimeFencedOrTerminal)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	noLeaseBytes, err := json.Marshal(persistedEvidence.NoLeasePhysicalDisposition)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		terminal_decision_id, runtime_run_id, work_item_id, admission_grant_id,
		grant_generation, runtime_fenced_or_terminal, no_lease_physical_disposition,
		physical_capacity_release_ready, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,$8)`, authority.table("runtime_execution_capacity_outbox")),
		decisionID.String(), command.RuntimeRunID.String(), startBinding.WorkItemID.String(),
		startBinding.AdmissionGrantID.String(), startBinding.GrantGeneration,
		runtimeFencedBytes, noLeaseBytes, committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		fact_id, audit_fact_id, audit_canonical_digest, fact_revision,
		projection_schema_version, audit_delivery_status, telemetry_delivery_status, degraded
	) VALUES ($1,$2,$3,$4,$5,$6,$6,FALSE)`, authority.table("runtime_execution_projection_backlog")),
		decisionID.String(), auditID, auditDigest[:], record.fixture.RuntimeRevision,
		SchemaV1, ProjectionPending); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if command.Outcome == RuntimeCancelled && authority.schedulerCancellationParticipant != nil {
		cancellationFact := SchedulerCancellationFact{
			OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
			RuntimeRunID: command.RuntimeRunID, DecisionID: decisionID,
			RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
			AcceptedAt: committedAt,
		}
		cancellationTransaction := &postgresSchedulerCancellationTransaction{
			tx: tx, function: authority.schedulerCancellationFunction, fact: cancellationFact,
		}
		if err := authority.schedulerCancellationParticipant.ParticipateCancellation(
			ctx, cancellationTransaction, cancellationFact,
		); err != nil || !cancellationTransaction.called {
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
	}
	if authority.failAt(PersistenceFaultBeforeCommit) || authority.failAt(PersistenceFaultBeforeNoLeaseCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}

	decision := RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}
	if authority.failAt(PersistenceFaultAfterCommit) || authority.failAt(PersistenceFaultAfterNoLeaseCommit) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	authority.deliverProjection(ctx, ProjectionFact{
		DecisionID: decisionID, RuntimeRunID: command.RuntimeRunID, OperationID: command.OperationID,
		CanonicalDigest: command.CanonicalRequestDigest, RuntimeRevision: record.fixture.RuntimeRevision,
		AuditFactID: auditID, AuditCanonicalDigest: auditDigest, ProjectionSchemaVersion: SchemaV1,
	})
	if authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func (authority *PostgresAuthority) executePostgresPreLeaseTerminal(
	ctx context.Context,
	start StartRuntimeRun,
	outcome RuntimeOutcome,
	reason PreLeaseTerminalReason,
	occurredAt time.Time,
) (RuntimeDecision, error) {
	if outcome != RuntimeRejected && outcome != RuntimeTimedOut || reason == PreLeaseTerminalNone {
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	snapshot, err := authority.Inspect(ctx, RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		return RuntimeDecision{}, err
	}
	if snapshot.State == RuntimeTerminal {
		return RuntimeDecision{Snapshot: snapshot}, nil
	}
	operationID, digest, canonical := stablePostgresPreLeaseTerminalBinding(snapshot.Lease, outcome, reason)
	binding := retainedCommandBindingValue{
		kind: postgresPreLeaseTerminalCommandKind, operationID: operationID,
		workspaceID: start.PersonalWorkspaceID, runtimeRunID: start.RuntimeRunID,
		caller: start.Authority, digest: digest, canonical: canonical,
		expectedOperationGeneration: snapshot.Operation.Generation,
		expectedRuntimeFence:        snapshot.RuntimeFence, safetyEpoch: start.ReleaseSafetyEpoch,
		auditAction: postgresAuditPreLeaseTerminal, auditReasonCode: uint8(reason),
	}
	return authority.executePostgresNoLeaseTerminal(ctx, postgresNoLeaseTerminalCommand{
		OperationID: operationID, PersonalWorkspaceID: start.PersonalWorkspaceID,
		TaskID: start.TaskID, PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: snapshot.Operation.Generation,
		ExpectedRuntimeFence:        snapshot.RuntimeFence, Authority: start.Authority,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: occurredAt,
		CanonicalRequestDigest: digest, Outcome: outcome, PreLeaseTerminalReason: reason,
	}, binding)
}

func validAcceptedStartBinding(operation RuntimeOperationBinding, lease RuntimeLeaseSnapshot) bool {
	validLeaseStatus := lease.AcquireStatus == LeaseAcquirePending ||
		lease.AcquireStatus == LeaseAcquireReconciliationRequired
	return operation.Status == OperationBound && validOpaqueID(operation.OperationID.String()) &&
		operation.Digest != (Digest{}) && operation.Generation > 0 &&
		validOpaqueID(operation.AdmissionGrantID.String()) && validOpaqueID(operation.WorkItemID.String()) &&
		operation.GrantGeneration > 0 && validOpaqueID(operation.ExecutionNodeID.String()) &&
		operation.NodeCapacityGeneration > 0 && validOpaqueID(operation.ResourceClassID.String()) &&
		validOpaqueID(operation.ExecutionPolicyID.String()) && operation.SchedulerEpoch > 0 &&
		operation.PolicyVersion > 0 && validLeaseStatus &&
		validOpaqueID(lease.AcquireOperationID.String()) && lease.AcquireDigest != (Digest{}) &&
		lease.LeaseID == (SandboxLeaseID{}) && lease.Generation == 0 && lease.Fence == 0
}

func validGrantedLeaseBinding(operation RuntimeOperationBinding, lease RuntimeLeaseSnapshot) bool {
	return operation.Status == OperationBound && validOpaqueID(operation.OperationID.String()) &&
		operation.Digest != (Digest{}) && operation.Generation > 0 &&
		validOpaqueID(operation.AdmissionGrantID.String()) && validOpaqueID(operation.WorkItemID.String()) &&
		operation.GrantGeneration > 0 && validOpaqueID(operation.ExecutionNodeID.String()) &&
		operation.NodeCapacityGeneration > 0 && validOpaqueID(operation.ResourceClassID.String()) &&
		validOpaqueID(operation.ExecutionPolicyID.String()) && operation.SchedulerEpoch > 0 && operation.PolicyVersion > 0 &&
		lease.AcquireStatus == LeaseGranted && lease.Disposition == LeaseActive &&
		validOpaqueID(lease.AcquireOperationID.String()) && lease.AcquireDigest != (Digest{}) &&
		validOpaqueID(lease.LeaseID.String()) && lease.Generation > 0 && lease.Fence > 0 &&
		validOpaqueID(lease.SandboxID.String()) && lease.SandboxGeneration > 0 && lease.SandboxFence > 0
}
