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

// evidenceTerminalCommand captures the validated evidence state for terminal CAS.
type evidenceTerminalCommand struct {
	OperationID             OperationID
	PersonalWorkspaceID     PersonalWorkspaceID
	TaskID                  TaskID
	PhaseRunID              PhaseRunID
	RuntimeRunID            RuntimeRunID
	ExpectedRuntimeRevision RuntimeRevision
	ExpectedStartOperationID OperationID
	ExpectedOperationGeneration OperationGeneration
	ExpectedRuntimeFence    RuntimeFence
	Authority               RuntimeAuthority
	SafetyEpoch             ReleaseSafetyEpoch
	OccurredAt              time.Time
	CanonicalRequestDigest  Digest
	Evidence                ValidatedRuntimeEvidence
	Outcome                 RuntimeOutcome
}

// ValidatedRuntimeEvidence holds evidence that has passed trust validation.
type ValidatedRuntimeEvidence struct {
	EvidenceID             EvidenceID
	EvidenceDigest         Digest
	EvidenceRootID         EvidenceRootID
	EvidenceRootDigest     Digest
	OutputContractDigest   Digest
	EvidenceContractDigest Digest
	SandboxLeaseID         SandboxLeaseID
	LeaseGeneration        LeaseGeneration
	LeaseFence             LeaseFence
	GatewayGrantID         GatewayGrantID
	GatewayGrantGeneration GatewayGrantGeneration
	GatewayGrantDigest     Digest
	InternalCallCount      uint64
	ObservedAt             time.Time
	AckDigest              Digest
	CapsuleDigest          Digest
}

type evidenceTerminalDisposition uint8

const (
	evidenceTerminalAccepted      evidenceTerminalDisposition = iota + 1
	evidenceTerminalFenced        // cancel/timeout fence already committed
	evidenceTerminalDiagnosticOnly // late success stored as diagnostic only
)

func (authority *PostgresAuthority) advancePostgresEvidenceTerminal(
	ctx context.Context,
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
) (RuntimeDecision, error) {
	if !candidateForEvidenceTerminal(snapshot) {
		return RuntimeDecision{Snapshot: snapshot}, nil
	}
	evidence, outcome, err := validateEvidenceForTerminalIngestion(snapshot)
	if err != nil {
		return RuntimeDecision{Snapshot: snapshot}, nil
	}
	operationID, digest, canonical := stableEvidenceTerminalBinding(snapshot, start.OperationID, evidence, outcome)
	binding := retainedCommandBindingValue{
		kind:        postgresEvidenceTerminalCommandKind,
		operationID: operationID, workspaceID: start.PersonalWorkspaceID,
		runtimeRunID: start.RuntimeRunID, caller: start.Authority,
		digest: digest, canonical: canonical,
		expectedOperationGeneration: snapshot.Operation.Generation,
		expectedRuntimeFence:        snapshot.RuntimeFence,
		safetyEpoch:                 start.ReleaseSafetyEpoch,
		auditAction:                 postgresAuditEvidenceTerminal,
		auditReasonCode:             uint8(outcome),
	}
	return authority.executePostgresEvidenceTerminal(ctx, evidenceTerminalCommand{
		OperationID: operationID, PersonalWorkspaceID: start.PersonalWorkspaceID,
		TaskID: start.TaskID, PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: snapshot.RuntimeRevision,
		ExpectedStartOperationID:    start.OperationID,
		ExpectedOperationGeneration: snapshot.Operation.Generation,
		ExpectedRuntimeFence:        snapshot.RuntimeFence,
		Authority:                   start.Authority,
		SafetyEpoch:                 start.ReleaseSafetyEpoch,
		OccurredAt:                  evidence.ObservedAt,
		CanonicalRequestDigest:      digest,
		Evidence:                    evidence,
		Outcome:                     outcome,
	}, binding)
}

func candidateForEvidenceTerminal(snapshot RuntimeSnapshot) bool {
	return snapshot.State == RuntimeReconciling && snapshot.Outcome == RuntimeOutcomeNone &&
		(snapshot.Worker.Status == WorkerOperationSuccessObserved ||
			snapshot.Worker.Status == WorkerOperationFailureObserved) &&
		snapshot.Lease.AcquireStatus == LeaseGranted &&
		snapshot.Lease.Disposition == LeaseActive &&
		snapshot.Worker.EvidenceCandidate != (WorkerEvidenceCandidateSnapshot{})
}

func validateEvidenceForTerminalIngestion(
	snapshot RuntimeSnapshot,
) (ValidatedRuntimeEvidence, RuntimeOutcome, error) {
	candidate := snapshot.Worker.EvidenceCandidate
	if !validOpaqueID(candidate.EvidenceID.String()) || candidate.EvidenceDigest == (Digest{}) {
		return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
	}
	outcome := RuntimeSucceeded
	if snapshot.Worker.Status == WorkerOperationFailureObserved {
		outcome = RuntimeFailed
	}
	// Trust validation layers:
	// 1. Identity and digest integrity
	if candidate.EvidenceID == (EvidenceID{}) || candidate.EvidenceDigest == (Digest{}) {
		return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
	}
	// 2. Lease binding — evidence must be bound to the current lease
	if candidate.SandboxLeaseID != snapshot.Lease.LeaseID ||
		candidate.LeaseGeneration != snapshot.Lease.Generation ||
		candidate.LeaseFence != snapshot.Lease.Fence {
		return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
	}
	// 3. Output and evidence contract digests must match the execution capsule
	if candidate.OutputContractDigest == (Digest{}) || candidate.EvidenceContractDigest == (Digest{}) {
		return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
	}
	// 4. Gateway grant binding — if provider calls were made
	if candidate.GatewayGrantID != (GatewayGrantID{}) {
		if !validOpaqueID(candidate.GatewayGrantID.String()) ||
			candidate.GatewayGrantGeneration == 0 || candidate.GatewayGrantDigest == (Digest{}) {
			return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
		}
		if snapshot.Gateway.CurrentGrant.GatewayGrantID != candidate.GatewayGrantID ||
			snapshot.Gateway.CurrentGrant.Generation != candidate.GatewayGrantGeneration {
			return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
		}
	}
	// 5. Internal call count must be at least 1
	if candidate.InternalCallCount == 0 {
		return ValidatedRuntimeEvidence{}, RuntimeOutcomeNone, newError(ErrorIntegrityConflict)
	}

	// Derive evidence root from validated evidence
	evidenceRootID, evidenceRootDigest := deriveEvidenceRoot(snapshot, candidate)

	return ValidatedRuntimeEvidence{
		EvidenceID:             candidate.EvidenceID,
		EvidenceDigest:         candidate.EvidenceDigest,
		EvidenceRootID:         evidenceRootID,
		EvidenceRootDigest:     evidenceRootDigest,
		OutputContractDigest:   candidate.OutputContractDigest,
		EvidenceContractDigest: candidate.EvidenceContractDigest,
		SandboxLeaseID:         candidate.SandboxLeaseID,
		LeaseGeneration:        candidate.LeaseGeneration,
		LeaseFence:             candidate.LeaseFence,
		GatewayGrantID:         candidate.GatewayGrantID,
		GatewayGrantGeneration: candidate.GatewayGrantGeneration,
		GatewayGrantDigest:     candidate.GatewayGrantDigest,
		InternalCallCount:      candidate.InternalCallCount,
		ObservedAt:             time.Now().UTC(),
		AckDigest:              snapshot.Worker.OperationAckDigest,
		CapsuleDigest:          snapshot.Worker.CapsuleDigest,
	}, outcome, nil
}

func deriveEvidenceRoot(
	snapshot RuntimeSnapshot,
	candidate WorkerEvidenceCandidateSnapshot,
) (EvidenceRootID, Digest) {
	material := []byte(fmt.Sprintf(
		"slidesmith.runtime-execution.evidence-root/v1\n%s\n%s\n%s\n%s\n%d\n%d",
		snapshot.RuntimeRunID.String(), candidate.EvidenceID.String(),
		candidate.EvidenceDigest.String(), snapshot.Worker.AcceptOperationID.String(),
		snapshot.Worker.AcceptedLeaseGeneration, snapshot.Worker.AcceptedLeaseFence,
	))
	rootDigest := sha256.Sum256(material)
	rootID := EvidenceRootID{value: fmt.Sprintf("evidence-root-%x", rootDigest[:16])}
	return rootID, Digest(rootDigest)
}

func stableEvidenceTerminalBinding(
	snapshot RuntimeSnapshot,
	startOperationID OperationID,
	evidence ValidatedRuntimeEvidence,
	outcome RuntimeOutcome,
) (OperationID, Digest, []byte) {
	identity := []byte(fmt.Sprintf(
		"slidesmith.runtime-execution.evidence-terminal/v1\n%s\n%s\n%s\n%s\n%d",
		snapshot.RuntimeRunID.String(), startOperationID.String(),
		snapshot.Lease.LeaseID.String(), evidence.EvidenceID.String(), outcome,
	))
	identityDigest := sha256.Sum256(identity)
	operationID := OperationID{value: fmt.Sprintf("evidence-terminal-%x", identityDigest[:16])}
	canonical := []byte(fmt.Sprintf(
		"slidesmith.runtime-execution.evidence-terminal-command/v1\n%s\n%s\n%s\n%s\n%s\n%s\n%d\n%d\n%d\n%d\n%s\n%s",
		operationID.String(), snapshot.RuntimeRunID.String(), startOperationID.String(),
		snapshot.Lease.LeaseID.String(), evidence.EvidenceID.String(), evidence.EvidenceDigest.String(),
		snapshot.Lease.Generation, snapshot.Lease.Fence, snapshot.RuntimeFence,
		outcome, snapshot.Worker.AcceptOperationID.String(), snapshot.Worker.OperationAckDigest.String(),
	))
	digest := Digest(sha256.Sum256(canonical))
	return operationID, digest, canonical
}

func (authority *PostgresAuthority) executePostgresEvidenceTerminal(
	ctx context.Context,
	command evidenceTerminalCommand,
	binding retainedCommandBindingValue,
) (RuntimeDecision, error) {
	if command.Outcome != RuntimeSucceeded && command.Outcome != RuntimeFailed {
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

	// Replay check
	if replay, found, replayErr := authority.lookupPostgresCommandReplay(ctx, tx, record, binding); replayErr != nil {
		return RuntimeDecision{}, replayErr
	} else if found {
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return replay, nil
	}

	// Guard: must be in reconciling state with no outcome
	if record.fixture.State != RuntimeReconciling || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.fixture.RuntimeRevision != command.ExpectedRuntimeRevision ||
		record.operation.OperationID != command.ExpectedStartOperationID ||
		record.fixture.OperationGeneration != command.ExpectedOperationGeneration ||
		record.fixture.RuntimeFence != command.ExpectedRuntimeFence ||
		record.fixture.SafetyEpoch != command.SafetyEpoch ||
		command.Authority.generation != record.fixture.Owner.generation {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	// Guard: must have active lease
	hasActiveLease := record.lease.AcquireStatus == LeaseGranted && record.lease.Disposition == LeaseActive
	if !hasActiveLease || record.cancellation.Status != CancellationNotRequested {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	// Guard: worker must have reported success or failure
	if (record.worker.Status != WorkerOperationSuccessObserved &&
		record.worker.Status != WorkerOperationFailureObserved) ||
		record.worker.EvidenceCandidate.EvidenceID == (EvidenceID{}) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	// Validate evidence trust against current record state
	if !validateEvidenceTrustAgainstRecord(record, command.Evidence) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	// Validate lease root count — must have exactly one lease
	var leaseRootCount uint64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1) +
		(SELECT count(*) FROM %s WHERE runtime_run_id=$1)`,
		authority.table("runtime_execution_lease_roots"),
		authority.table("runtime_execution_prelease_leases")), command.RuntimeRunID.String()).Scan(&leaseRootCount); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if leaseRootCount != 1 {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	// Evidence root — ensure it exists
	evidenceRoot := EvidenceRootSnapshot{
		SchemaVersion:  SchemaV1,
		EvidenceRootID: command.Evidence.EvidenceRootID,
		Digest:         command.Evidence.EvidenceRootDigest,
	}
	if err := authority.ensurePostgresEvidenceRoot(ctx, tx, command.RuntimeRunID, evidenceRoot); err != nil {
		return RuntimeDecision{}, err
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

	// Terminal CAS: advance revision, fence, and set terminal outcome
	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeTerminal
	record.fixture.Outcome = command.Outcome
	record.fixture.TerminalEvidenceID = command.Evidence.EvidenceID
	record.operation.OperationID = command.OperationID
	record.operation.Digest = command.CanonicalRequestDigest
	record.operation.Generation = record.fixture.OperationGeneration
	record.evidenceRoot = evidenceRoot

	// Advance lease — revoke after terminal success/failure
	record.lease.Generation++
	record.lease.Fence++
	record.lease.SandboxFence++
	record.lease.Disposition = LeaseRevoked

	// Update node — mark as unknown occupancy, quarantined
	record.node.Occupancy = NodeOccupancyUnknown
	record.node.Quarantined = true
	record.node.Containment = ContainmentPending
	record.node.Reset = ResetRequired

	// Cleanup obligations
	record.cleanup = RuntimeLeaseCleanupSnapshot{
		Status:                 LeaseCleanupPending,
		OperationID:            command.OperationID,
		CanonicalRequestDigest: command.CanonicalRequestDigest,
		StopMainProcess:        true,
		StopChildProcesses:     true,
		RevokeSecrets:          true,
		RemoveNetwork:          true,
		FenceRuntimeView:       false,
		ReconcileContainment:   true,
	}
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionNone,
		Physical:       PhysicalCapacityUnknownOrQuarantined,
	}
	record.reconciliation = ReconciliationStable

	baseEvidence := RuntimeFencedOrTerminalEvidence{
		WorkItemID:              record.operation.WorkItemID,
		AdmissionGrantID:        record.operation.AdmissionGrantID,
		GrantGeneration:         record.operation.GrantGeneration,
		RuntimeRunID:            command.RuntimeRunID,
		StartOperationID:        command.ExpectedStartOperationID,
		StartDigest:             record.operation.Digest,
		TerminalDecisionID:      decisionID,
		RuntimeRevision:         record.fixture.RuntimeRevision,
		RuntimeFence:            record.fixture.RuntimeFence,
		SchedulerEpoch:          record.operation.SchedulerEpoch,
		PolicyVersion:           record.operation.PolicyVersion,
		LeaseAcquireOperationID: record.lease.AcquireOperationID,
		LeaseAcquireDigest:      record.lease.AcquireDigest,
	}
	record.capacityEvidence = RuntimeCapacityEvidenceSnapshot{
		RuntimeFencedOrTerminal: baseEvidence,
	}

	fact := RuntimeDecisionFact{
		DecisionID: decisionID, Disposition: DecisionAccepted,
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		PreviousRuntimeRevision: previousRevision, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: RuntimeTerminal, OutcomeAtDecision: command.Outcome,
		TerminalEvidenceID: command.Evidence.EvidenceID,
		Retry:              RetryNever, Reconciliation: ReconciliationNotRequired,
	}

	// Execute the CAS update
	aggregateState, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	committedAt := postgresTimestamp(command.OccurredAt)
	// CAS: only update if state is still RuntimeReconciling and outcome is RuntimeOutcomeNone
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET runtime_revision=$1,
		operation_generation=$2, runtime_fence=$3, runtime_state=$4, runtime_outcome=$5,
		terminal_evidence_id=$6, aggregate_state=$7, updated_at=$8
		WHERE runtime_run_id=$9 AND runtime_revision=$10 AND operation_generation=$11
		AND runtime_fence=$12 AND runtime_state IN ($13,$14) AND runtime_outcome=$15`,
		authority.table("runtime_execution_runtimes")),
		record.fixture.RuntimeRevision, record.fixture.OperationGeneration,
		record.fixture.RuntimeFence, record.fixture.State, record.fixture.Outcome,
		command.Evidence.EvidenceID.String(), aggregateState, committedAt,
		command.RuntimeRunID.String(), previousRevision, previousOperationGeneration,
		previousFence, RuntimeReconciling, RuntimeRunning, RuntimeOutcomeNone)
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		// CAS failed — this means cancel/timeout may have already committed.
		// Store evidence as diagnostic only.
		if err := authority.storeDiagnosticOnlyEvidence(ctx, tx, command, evidenceRoot, committedAt); err != nil {
			return RuntimeDecision{}, err
		}
		fact.Disposition = DecisionRejected
		fact.OutcomeAtDecision = RuntimeOutcomeNone
		fact.Retry = RetryNever
		fact.Reconciliation = ReconciliationNotRequired
		diagnosticDecision, err := authority.buildDiagnosticDecision(ctx, tx, command, fact, evidenceRoot, committedAt)
		if err != nil {
			return RuntimeDecision{}, err
		}
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return diagnosticDecision, nil
	}

	// Update lease and node in same transaction
	if err := authority.updatePostgresLeaseLifecycle(ctx, tx, record.lease, command.RuntimeRunID); err != nil {
		return RuntimeDecision{}, err
	}
	leasedNode, err := authority.loadPostgresNodeForUpdate(ctx, tx, record.operation.ExecutionNodeID)
	if err != nil {
		return RuntimeDecision{}, err
	}
	leasedNode.Occupancy = NodeOccupancyUnknown
	leasedNode.Quarantined = true
	leasedNode.Containment = ContainmentPending
	leasedNode.Reset = ResetRequired
	record.node = nodeSnapshot(leasedNode.ExecutionNodeFixture)
	if err := authority.updatePostgresNode(ctx, tx, leasedNode, committedAt); err != nil {
		return RuntimeDecision{}, err
	}
	if authority.failAt(PersistenceFaultBeforeDecision) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}

	// Persist decision
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

	// Mandatory audit
	auditID := fmt.Sprintf("runtime-audit-postgres-%020d", sequence)
	auditState := newPostgresMandatoryAuditState(postgresMandatoryAuditInput{
		AuditFactID: auditID, Action: binding.auditAction, ReasonCode: binding.auditReasonCode,
		Decision: fact, RuntimeRunID: command.RuntimeRunID, RequestDigest: command.CanonicalRequestDigest,
		Authority: command.Authority, BeforeState: beforeState, AfterState: RuntimeTerminal,
		BeforeOperationGeneration: previousOperationGeneration,
		AfterOperationGeneration:  record.fixture.OperationGeneration,
		BeforeRuntimeFence:        previousFence, AfterRuntimeFence: record.fixture.RuntimeFence,
		PolicyEpoch:       record.operation.SchedulerEpoch,
		BeforeSafetyEpoch: command.SafetyEpoch, AfterSafetyEpoch: command.SafetyEpoch,
		OccurredAt: committedAt, RecordedAt: committedAt, EvidenceRoot: evidenceRoot,
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

	// Outbox — standard outbox, capacity outbox, and new task evidence outbox
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

	// Capacity outbox
	runtimeFencedBytes, err := json.Marshal(record.capacityEvidence.RuntimeFencedOrTerminal)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	noLeaseBytes := []byte("null")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		terminal_decision_id, runtime_run_id, work_item_id, admission_grant_id,
		grant_generation, runtime_fenced_or_terminal, no_lease_physical_disposition,
		physical_capacity_release_ready, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,$8)`, authority.table("runtime_execution_capacity_outbox")),
		decisionID.String(), command.RuntimeRunID.String(), record.operation.WorkItemID.String(),
		record.operation.AdmissionGrantID.String(), record.operation.GrantGeneration,
		runtimeFencedBytes, noLeaseBytes, committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}

	// Task evidence outbox — bridge for Task Orchestration consumption
	evidenceOutboxBytes, err := json.Marshal(TaskEvidenceOutboxRecord{
		TaskID:                  command.TaskID.String(),
		PhaseRunID:              command.PhaseRunID.String(),
		RuntimeRunID:            command.RuntimeRunID.String(),
		EvidenceID:              command.Evidence.EvidenceID.String(),
		EvidenceDigest:          command.Evidence.EvidenceDigest.String(),
		EvidenceRootID:          command.Evidence.EvidenceRootID.String(),
		EvidenceRootDigest:      command.Evidence.EvidenceRootDigest.String(),
		Outcome:                 command.Outcome,
		RuntimeRevision:         record.fixture.RuntimeRevision,
		RuntimeFence:            record.fixture.RuntimeFence,
		LeaseID:                 record.lease.LeaseID.String(),
		LeaseGeneration:         record.lease.Generation,
		LeaseFence:              record.lease.Fence,
		ObservedAt:              command.Evidence.ObservedAt,
		DecisionID:              decisionID.String(),
	})
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	evidencePayloadDigest := digestBytes(evidenceOutboxBytes)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, decision_id, runtime_run_id, evidence_id,
		payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7)`, authority.table("runtime_execution_task_evidence_outbox")),
		command.OperationID.String(), decisionID.String(), command.RuntimeRunID.String(),
		command.Evidence.EvidenceID.String(), evidenceOutboxBytes, evidencePayloadDigest[:], committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (operation_id, disposition)
		VALUES ($1,$2)`, authority.table("runtime_execution_task_evidence_outbox_delivery")),
		command.OperationID.String(), OutboxPending); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}

	// Lease cleanup obligations
	cleanupDigest := digestBytes(append([]byte("slidesmith.runtime-execution.lease-cleanup/v1\n"), binding.canonical...))
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, sandbox_lease_id, lease_generation, lease_fence,
		sandbox_id, sandbox_generation, sandbox_fence, stop_main_process,
		stop_child_processes, revoke_secrets, remove_network, fence_runtime_view,
		reconcile_containment, canonical_digest, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,TRUE,TRUE,TRUE,FALSE,TRUE,$9,$10)`,
		authority.table("runtime_execution_lease_cleanup_obligations")), command.OperationID.String(),
		command.RuntimeRunID.String(), record.lease.LeaseID.String(), record.lease.Generation,
		record.lease.Fence, record.lease.SandboxID.String(), record.lease.SandboxGeneration,
		record.lease.SandboxFence, cleanupDigest[:], committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}

	// Projection backlog
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		fact_id, audit_fact_id, audit_canonical_digest, fact_revision,
		projection_schema_version, audit_delivery_status, telemetry_delivery_status, degraded
	) VALUES ($1,$2,$3,$4,$5,$6,$6,FALSE)`, authority.table("runtime_execution_projection_backlog")),
		decisionID.String(), auditID, auditDigest[:], record.fixture.RuntimeRevision,
		SchemaV1, ProjectionPending); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
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
	}, auditState)
	if authority.failAt(PersistenceFaultBeforeResponse) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

// storeDiagnosticOnlyEvidence records late evidence when a terminal fence
// (cancel/timeout) already committed. The evidence is retained as diagnostic
// and cannot progress the Task.
func (authority *PostgresAuthority) storeDiagnosticOnlyEvidence(
	ctx context.Context,
	tx *sql.Tx,
	command evidenceTerminalCommand,
	evidenceRoot EvidenceRootSnapshot,
	committedAt time.Time,
) error {
	payload := map[string]any{
		"evidence_id":         command.Evidence.EvidenceID.String(),
		"evidence_digest":     command.Evidence.EvidenceDigest.String(),
		"evidence_root_id":    command.Evidence.EvidenceRootID.String(),
		"evidence_root_digest": command.Evidence.EvidenceRootDigest.String(),
		"outcome":             command.Outcome,
		"observed_at":         command.Evidence.ObservedAt,
		"diagnostic_only":     true,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return newError(ErrorIntegrityConflict)
	}
	payloadDigest := digestBytes(payloadBytes)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		operation_id, runtime_run_id, evidence_id, evidence_root_id,
		payload, payload_digest, diagnostic_only, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,TRUE,$7)`,
		authority.table("runtime_execution_diagnostic_evidence")),
		command.OperationID.String(), command.RuntimeRunID.String(),
		command.Evidence.EvidenceID.String(), command.Evidence.EvidenceRootID.String(),
		payloadBytes, payloadDigest[:], committedAt); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

func (authority *PostgresAuthority) buildDiagnosticDecision(
	ctx context.Context,
	tx *sql.Tx,
	command evidenceTerminalCommand,
	fact RuntimeDecisionFact,
	evidenceRoot EvidenceRootSnapshot,
	committedAt time.Time,
) (RuntimeDecision, error) {
	// Fetch current record state for snapshot
	record, err := authority.loadRuntimeForUpdate(ctx, tx, command.RuntimeRunID)
	if err != nil {
		return RuntimeDecision{}, err
	}
	fact.StateAtDecision = record.fixture.State
	fact.OutcomeAtDecision = record.fixture.Outcome

	decisionState, err := encodePostgresDecisionFact(fact)
	if err != nil {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
		authority.table("runtime_execution_decisions")),
		fact.DecisionID.String(), command.RuntimeRunID.String(), command.OperationID.String(),
		command.CanonicalRequestDigest[:], fact.PreviousRuntimeRevision,
		fact.ResultingRuntimeRevision, decisionState, committedAt); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	return RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
}

func validateEvidenceTrustAgainstRecord(record *runtimeRecord, evidence ValidatedRuntimeEvidence) bool {
	if record == nil || record.worker.EvidenceCandidate == (WorkerEvidenceCandidateSnapshot{}) {
		return false
	}
	candidate := record.worker.EvidenceCandidate
	// Evidence must match the worker's reported candidate
	if candidate.EvidenceID != evidence.EvidenceID || candidate.EvidenceDigest != evidence.EvidenceDigest {
		return false
	}
	// Lease binding must match
	if evidence.SandboxLeaseID != record.lease.LeaseID ||
		evidence.LeaseGeneration != record.lease.Generation ||
		evidence.LeaseFence != record.lease.Fence {
		return false
	}
	// Capsule and ack must match
	if evidence.AckDigest != record.worker.OperationAckDigest ||
		evidence.CapsuleDigest != record.worker.CapsuleDigest {
		return false
	}
	return true
}

func (authority *PostgresAuthority) ensurePostgresEvidenceRoot(
	ctx context.Context,
	tx *sql.Tx,
	runtimeRunID RuntimeRunID,
	root EvidenceRootSnapshot,
) error {
	var retainedDigest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT digest FROM %s
		WHERE evidence_root_id=$1 AND runtime_run_id=$2 FOR SHARE`,
		authority.table("runtime_execution_evidence_roots")),
		root.EvidenceRootID.String(), runtimeRunID.String()).Scan(&retainedDigest)
	if err == nil {
		if !bytes.Equal(retainedDigest, root.Digest[:]) {
			return newError(ErrorIntegrityConflict)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return normalizeRuntimePersistenceFailure(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		evidence_root_id, runtime_run_id, schema_version, digest, accepted_at
	) VALUES ($1,$2,$3,$4,$5)`, authority.table("runtime_execution_evidence_roots")),
		root.EvidenceRootID.String(), runtimeRunID.String(), root.SchemaVersion,
		root.Digest[:], postgresTimestamp(authority.now())); err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
}

// TaskEvidenceOutboxRecord is the typed bridge record the Task Orchestration
// consumer uses to observe evidence without copying C03 state machine logic.
type TaskEvidenceOutboxRecord struct {
	TaskID             string         `json:"task_id"`
	PhaseRunID         string         `json:"phase_run_id"`
	RuntimeRunID       string         `json:"runtime_run_id"`
	EvidenceID         string         `json:"evidence_id"`
	EvidenceDigest     string         `json:"evidence_digest"`
	EvidenceRootID     string         `json:"evidence_root_id"`
	EvidenceRootDigest  string         `json:"evidence_root_digest"`
	Outcome            RuntimeOutcome `json:"outcome"`
	RuntimeRevision    RuntimeRevision `json:"runtime_revision"`
	RuntimeFence       RuntimeFence    `json:"runtime_fence"`
	LeaseID            string         `json:"lease_id"`
	LeaseGeneration    LeaseGeneration `json:"lease_generation"`
	LeaseFence         LeaseFence     `json:"lease_fence"`
	ObservedAt         time.Time      `json:"observed_at"`
	DecisionID         string         `json:"decision_id"`
}
