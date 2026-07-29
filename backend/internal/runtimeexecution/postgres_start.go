package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type postgresSchedulerAcceptanceTransaction struct {
	tx       *sql.Tx
	function string
	fact     SchedulerAcceptanceFact
	called   bool
}

func (transaction *postgresSchedulerAcceptanceTransaction) AcceptAndBind(
	ctx context.Context,
) (SchedulerGrantBinding, error) {
	if transaction.called || ctx == nil || ctx.Err() != nil {
		return SchedulerGrantBinding{}, newError(ErrorDependencyUnavailable)
	}
	fact := transaction.fact
	var binding SchedulerGrantBinding
	err := transaction.tx.QueryRowContext(ctx, "SELECT * FROM "+transaction.function+`(
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		fact.WorkItemID.String(), fact.AdmissionGrantID.String(), fact.GrantGeneration,
		fact.OperationID.String(), fact.CanonicalRequestDigest[:], fact.RuntimeRunID.String(),
		fact.DecisionID.String(), fact.AcceptedRuntimeRevision, fact.RuntimeFence,
		fact.LeaseAcquireOperationID.String(), fact.LeaseAcquireDigest[:], fact.RuntimeDeadline,
		fact.ResourceClassID.String(), fact.ExecutionPolicyID.String(), fact.AcceptedAt,
	).Scan(&binding.ExecutionNodeID.value, &binding.NodeCapacityGeneration,
		&binding.ResourceClassID.value, &binding.ExecutionPolicyID.value,
		&binding.SchedulerEpoch, &binding.PolicyVersion, &binding.GrantExpiresAt, &binding.LeaseAcquireBy)
	if err != nil {
		return SchedulerGrantBinding{}, newError(ErrorIntegrityConflict)
	}
	transaction.called = true
	binding.GrantExpiresAt = binding.GrantExpiresAt.UTC()
	binding.LeaseAcquireBy = binding.LeaseAcquireBy.UTC()
	if !validOpaqueID(binding.ExecutionNodeID.String()) || binding.NodeCapacityGeneration == 0 ||
		binding.ResourceClassID != fact.ResourceClassID || binding.ExecutionPolicyID != fact.ExecutionPolicyID ||
		binding.SchedulerEpoch == 0 || binding.PolicyVersion == 0 ||
		!binding.GrantExpiresAt.After(fact.AcceptedAt) ||
		!binding.LeaseAcquireBy.Equal(earlier(binding.GrantExpiresAt, fact.RuntimeDeadline)) {
		return SchedulerGrantBinding{}, newError(ErrorIntegrityConflict)
	}
	return binding, nil
}

func (authority *PostgresAuthority) executePostgresStart(
	ctx context.Context,
	command StartRuntimeRun,
	binding retainedCommandBindingValue,
) (RuntimeDecision, error) {
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	freshRuntime := errors.Is(err, sql.ErrNoRows)
	if err != nil && !freshRuntime {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if !freshRuntime {
		if !authorized(record, command.PersonalWorkspaceID, command.Authority) ||
			record.fixture.TaskID != command.TaskID || record.fixture.PhaseRunID != command.PhaseRunID {
			return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
		}
		if authority.failAt(PersistenceFaultBeforeRequestLookup) {
			return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
		}
		if replay, found, replayErr := authority.lookupPostgresCommandReplay(ctx, tx, record, binding); replayErr != nil {
			return RuntimeDecision{}, replayErr
		} else if found {
			if err := tx.Commit(); err != nil {
				return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
			}
			return replay, nil
		}
	}
	if authority.schedulerParticipant == nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if freshRuntime {
		record = &runtimeRecord{
			fixture: RuntimeFixture{
				PersonalWorkspaceID: command.PersonalWorkspaceID, TaskID: command.TaskID,
				PhaseRunID: command.PhaseRunID, RuntimeRunID: command.RuntimeRunID, Owner: command.Authority,
				TaskRevision: command.ExpectedTaskRevision, RuntimeRevision: command.ExpectedRuntimeRevision,
				OperationGeneration: command.ExpectedOperationGeneration, RuntimeFence: command.ExpectedRuntimeFence,
				SafetyEpoch: command.ReleaseSafetyEpoch, State: RuntimeCreated, Outcome: RuntimeOutcomeNone,
			},
			bindings: make(map[OperationID]Digest), decisions: make(map[decisionAttemptKey]RuntimeDecisionFact),
			operation: RuntimeOperationBinding{Status: OperationUnbound},
			lease:     RuntimeLeaseSnapshot{AcquireStatus: LeaseNotRequested},
			capacity: RuntimeCapacitySnapshot{
				LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone,
				Physical: PhysicalCapacityNotApplicable,
			},
			reconciliation: ReconciliationStable,
		}
	}
	if record.fixture.TaskRevision != command.ExpectedTaskRevision ||
		record.fixture.RuntimeRevision != command.ExpectedRuntimeRevision ||
		record.fixture.OperationGeneration != command.ExpectedOperationGeneration ||
		record.fixture.RuntimeFence != command.ExpectedRuntimeFence ||
		record.fixture.SafetyEpoch != command.ReleaseSafetyEpoch ||
		record.fixture.State != RuntimeCreated || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.operation.Status != OperationUnbound || record.lease.AcquireStatus != LeaseNotRequested {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if authority.failAt(PersistenceFaultBeforeRuntimeWrite) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	previousRevision := record.fixture.RuntimeRevision
	previousOperationGeneration := record.fixture.OperationGeneration
	previousFence := record.fixture.RuntimeFence
	var sequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("runtime_execution_decision_sequence")+"')").Scan(&sequence); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	decisionID := RuntimeDecisionID{value: fmt.Sprintf("runtime-decision-postgres-%020d", sequence)}
	leaseOperationID, leaseDigest := stableLeaseAcquireBinding(command)
	fact := RuntimeDecisionFact{
		DecisionID: decisionID, Disposition: DecisionAccepted, OperationID: command.OperationID,
		CanonicalRequestDigest: command.CanonicalRequestDigest, PreviousRuntimeRevision: previousRevision,
		ResultingRuntimeRevision: previousRevision + 1, StateAtDecision: RuntimeWaitingForLease,
		OutcomeAtDecision: RuntimeOutcomeNone, Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	acceptedAt := postgresTimestamp(authority.now())
	schedulerFact := SchedulerAcceptanceFact{
		WorkItemID: command.AdmissionGrant.WorkItemID, AdmissionGrantID: command.AdmissionGrant.AdmissionGrantID,
		GrantGeneration: command.AdmissionGrant.Generation, OperationID: command.OperationID,
		CanonicalRequestDigest: command.CanonicalRequestDigest, RuntimeRunID: command.RuntimeRunID,
		DecisionID: decisionID, AcceptedRuntimeRevision: fact.ResultingRuntimeRevision,
		RuntimeFence: previousFence + 1, LeaseAcquireOperationID: leaseOperationID,
		LeaseAcquireDigest: leaseDigest, RuntimeDeadline: command.Deadline.UTC(),
		ResourceClassID: command.ResourceClassID, ExecutionPolicyID: command.ExecutionPolicyID,
		AcceptedAt: acceptedAt,
	}
	schedulerTransaction := &postgresSchedulerAcceptanceTransaction{
		tx: tx, function: authority.schedulerAcceptanceFunction, fact: schedulerFact,
	}
	schedulerBinding, err := authority.schedulerParticipant.Participate(ctx, schedulerTransaction, schedulerFact)
	if err != nil || !schedulerTransaction.called {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	record.fixture.RuntimeRevision = fact.ResultingRuntimeRevision
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeWaitingForLease
	record.operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: command.OperationID, Digest: command.CanonicalRequestDigest,
		Generation: record.fixture.OperationGeneration, AdmissionGrantID: command.AdmissionGrant.AdmissionGrantID,
		WorkItemID: command.AdmissionGrant.WorkItemID, GrantGeneration: command.AdmissionGrant.Generation,
		ExecutionNodeID:        schedulerBinding.ExecutionNodeID,
		NodeCapacityGeneration: schedulerBinding.NodeCapacityGeneration,
		ResourceClassID:        schedulerBinding.ResourceClassID, ExecutionPolicyID: schedulerBinding.ExecutionPolicyID,
		SchedulerEpoch: schedulerBinding.SchedulerEpoch, PolicyVersion: schedulerBinding.PolicyVersion,
	}
	record.lease = RuntimeLeaseSnapshot{
		AcquireStatus: LeaseAcquirePending, AcquireOperationID: leaseOperationID, AcquireDigest: leaseDigest,
	}
	record.deadline = command.Deadline.UTC()
	if command.CatalogBinding != nil {
		record.catalogSafetyEpoch = command.CatalogBinding.SafetyEpoch
	}
	record.leaseAcquireBy = schedulerBinding.LeaseAcquireBy
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
	}
	record.reconciliation = ReconciliationStable
	aggregateState, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if freshRuntime {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			runtime_run_id, personal_workspace_id, task_id, phase_run_id, owner_authority_id,
			owner_authority_generation, owner_authority_kind, task_revision, runtime_revision,
			operation_generation, runtime_fence, safety_epoch, runtime_state, runtime_outcome,
			terminal_evidence_id, aggregate_state, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'',$15,$16)`,
			authority.table("runtime_execution_runtimes")), command.RuntimeRunID.String(),
			command.PersonalWorkspaceID.String(), command.TaskID.String(), command.PhaseRunID.String(),
			command.Authority.id.String(), command.Authority.generation, command.Authority.kind,
			command.ExpectedTaskRevision, record.fixture.RuntimeRevision, record.fixture.OperationGeneration,
			record.fixture.RuntimeFence, record.fixture.SafetyEpoch, record.fixture.State, record.fixture.Outcome,
			aggregateState, acceptedAt)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET runtime_revision=$1,
			operation_generation=$2, runtime_fence=$3, runtime_state=$4, runtime_outcome=$5,
			aggregate_state=$6, updated_at=$7 WHERE runtime_run_id=$8 AND runtime_revision=$9
			AND operation_generation=$10 AND runtime_fence=$11 AND runtime_state=$12`,
			authority.table("runtime_execution_runtimes")), record.fixture.RuntimeRevision,
			record.fixture.OperationGeneration, record.fixture.RuntimeFence, record.fixture.State,
			record.fixture.Outcome, aggregateState, acceptedAt, command.RuntimeRunID.String(),
			previousRevision, previousOperationGeneration, previousFence, RuntimeCreated)
		if err == nil {
			rows, rowsErr := result.RowsAffected()
			if rowsErr != nil || rows != 1 {
				return RuntimeDecision{}, newError(ErrorIntegrityConflict)
			}
		}
	}
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
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
		decisionState, acceptedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	canonical, err := canonicalStartEncoding(command)
	if err != nil {
		return RuntimeDecision{}, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		personal_workspace_id, runtime_run_id, command_kind, operation_id, canonical_request_digest,
		canonical_request, decision_id, admission_grant_id, admission_work_item_id, admission_grant_generation
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, authority.table("runtime_execution_requests")),
		command.PersonalWorkspaceID.String(), command.RuntimeRunID.String(), CommandStartRuntimeRun,
		command.OperationID.String(), command.CanonicalRequestDigest[:], canonical, decisionID.String(),
		command.AdmissionGrant.AdmissionGrantID.String(), command.AdmissionGrant.WorkItemID.String(),
		command.AdmissionGrant.Generation); err != nil {
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
		AuditFactID: auditID, Action: postgresAuditStartAccepted, Decision: fact,
		RuntimeRunID: command.RuntimeRunID, RequestDigest: command.CanonicalRequestDigest,
		Authority: command.Authority, BeforeState: RuntimeCreated, AfterState: RuntimeWaitingForLease,
		BeforeOperationGeneration: previousOperationGeneration,
		AfterOperationGeneration:  record.fixture.OperationGeneration,
		BeforeRuntimeFence:        previousFence, AfterRuntimeFence: record.fixture.RuntimeFence,
		PolicyEpoch:       schedulerBinding.SchedulerEpoch,
		BeforeSafetyEpoch: command.ReleaseSafetyEpoch, AfterSafetyEpoch: command.ReleaseSafetyEpoch,
		OccurredAt: acceptedAt, RecordedAt: acceptedAt,
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
		acceptedAt, acceptedAt, auditState.SourceClockID, auditBytes); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if authority.failAt(PersistenceFaultAfterMandatoryAudit) || authority.failAt(PersistenceFaultBeforeOutbox) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	scopeDigest := authorityScopeDigest(command.PersonalWorkspaceID, command.RuntimeRunID)
	payloadDigest := digestBytes(canonical)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, decision_id, runtime_run_id, canonical_request_digest,
		authority_scope_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, authority.table("runtime_execution_outbox")),
		command.OperationID.String(), decisionID.String(), command.RuntimeRunID.String(),
		command.CanonicalRequestDigest[:], scopeDigest[:], canonical, payloadDigest[:], acceptedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_outbox_delivery")),
		command.OperationID.String(), OutboxPending); err != nil {
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
	if authority.failAt(PersistenceFaultBeforeCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	decision := RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}
	if authority.failAt(PersistenceFaultAfterCommit) {
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

func (authority *PostgresAuthority) lookupPostgresCommandReplay(
	ctx context.Context,
	tx *sql.Tx,
	record *runtimeRecord,
	binding retainedCommandBindingValue,
) (RuntimeDecision, bool, error) {
	var retainedDigest, retainedCanonical []byte
	var retainedWorkspaceID, decisionID, retainedGrantID, retainedWorkItemID string
	var retainedKind int16
	var retainedGrantGeneration AdmissionGrantGeneration
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, command_kind,
		canonical_request_digest, canonical_request, decision_id, admission_grant_id,
		admission_work_item_id, admission_grant_generation FROM %s
		WHERE runtime_run_id=$1 AND operation_id=$2`, authority.table("runtime_execution_requests")),
		binding.runtimeRunID.String(), binding.operationID.String()).Scan(&retainedWorkspaceID,
		&retainedKind, &retainedDigest, &retainedCanonical, &decisionID, &retainedGrantID,
		&retainedWorkItemID, &retainedGrantGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeDecision{}, false, nil
	}
	if err != nil {
		return RuntimeDecision{}, false, normalizeRuntimePersistenceFailure(err)
	}
	coreMatches := retainedWorkspaceID == binding.workspaceID.String() && retainedKind == binding.kind &&
		bytes.Equal(retainedDigest, binding.digest[:]) && bytes.Equal(retainedCanonical, binding.canonical)
	exactGrant := retainedGrantID == binding.admissionGrantID.String() &&
		retainedWorkItemID == binding.admissionWorkItemID.String() &&
		retainedGrantGeneration == binding.admissionGrantGeneration
	newerRedundantGrant := retainedKind == int16(CommandStartRuntimeRun) &&
		retainedWorkItemID == binding.admissionWorkItemID.String() &&
		binding.admissionGrantGeneration > retainedGrantGeneration
	if !coreMatches || !exactGrant && !newerRedundantGrant {
		if err := authority.recordIntegrityIncident(ctx, tx, binding, retainedKind, retainedDigest,
			retainedGrantID, retainedWorkItemID, retainedGrantGeneration); err != nil {
			return RuntimeDecision{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, false, normalizeRuntimePersistenceFailure(err)
		}
		return RuntimeDecision{}, false, newError(ErrorIntegrityConflict)
	}
	fact, err := authority.loadRetainedDecision(ctx, tx, decisionID, binding)
	if err != nil {
		return RuntimeDecision{}, false, err
	}
	return RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}, true, nil
}

var _ SchedulerAcceptanceTransaction = (*postgresSchedulerAcceptanceTransaction)(nil)
var _ = time.Time{}
