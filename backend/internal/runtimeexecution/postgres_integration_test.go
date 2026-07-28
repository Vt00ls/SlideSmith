package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

func TestPostgresAuthorityReplaysRetainedDecisionWithCurrentSnapshot(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-replay-owner", 12)
	start := standardStart(t, now, owner, "postgres-replay")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	fixture.RuntimeRevision++
	fixture.State = RuntimeReconciling
	fixture.Reconciliation = ReconciliationRequiredStatus
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	original := RuntimeDecisionFact{
		DecisionID:  RuntimeDecisionID{value: "runtime-decision-postgres-replay"},
		Disposition: DecisionAccepted, OperationID: start.OperationID,
		CanonicalRequestDigest:   start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	installPostgresAcceptedStartFacts(t, db, schema, start, original, now)

	replayed, err := store.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replayed.Fact != original || replayed.Snapshot.RuntimeRevision != fixture.RuntimeRevision ||
		replayed.Snapshot.State != RuntimeReconciling || replayed.Snapshot.Reconciliation != ReconciliationRequiredStatus {
		t.Fatalf("replay = %#v, want original fact %#v and current Runtime revision %d", replayed, original, fixture.RuntimeRevision)
	}

	conflictingInput := start.StartRuntimeRunInput
	conflictingInput.Deadline = start.Deadline.Add(time.Minute)
	conflicting := mustStart(t, conflictingInput)
	_, err = store.Execute(context.Background(), conflicting)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting replay error = %T %v, want non-retryable integrity conflict", err, err)
	}

	replayedAgain, err := store.Execute(context.Background(), start)
	if err != nil || replayedAgain.Fact != original {
		t.Fatalf("conflict changed original retained Decision: decision=%#v err=%v", replayedAgain, err)
	}
}

func TestPostgresMalformedCancelIsInvalidRequest(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	store := newMigratedPostgresAuthority(t, db, schema, time.Date(2026, 7, 28, 10, 30, 0, 0, time.UTC))
	malformed := CancelRuntimeRun{CancelRuntimeRunInput: CancelRuntimeRunInput{SchemaVersion: SchemaV1}}

	_, err := store.Execute(context.Background(), malformed)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorInvalidRequest {
		t.Fatalf("malformed Cancel error = %T %v, want invalid request", err, err)
	}
}

func TestPostgresMandatoryAuditFailureRollsBackProtectedReconciliation(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-audit-owner", 14)
	start := standardStart(t, now, owner, "postgres-audit-rollback")
	faults := &PersistenceFaultController{}
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: faults,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-audit-rollback"), owner, ReconciliationTransportAmbiguous,
	)
	if err := faults.FailNextAt(PersistenceFaultBeforeMandatoryAudit); err != nil {
		t.Fatalf("configure mandatory-audit fault: %v", err)
	}
	_, err = store.persistReconciliationFoundation(context.Background(), intent)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorDependencyUnavailable {
		t.Fatalf("mandatory-audit fault error = %T %v, want safe unavailable", err, err)
	}

	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil {
		t.Fatalf("inspect after mandatory-audit rollback: %v", err)
	}
	if snapshot.RuntimeRevision != fixture.RuntimeRevision || snapshot.State != RuntimeWaitingForLease ||
		snapshot.Reconciliation != ReconciliationStable {
		t.Fatalf("mandatory-audit failure changed Runtime authority: %#v", snapshot)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{}) {
		t.Fatalf("mandatory-audit failure left partial authoritative facts: %+v", counts)
	}
}

func TestPostgresOutboxAcknowledgementCannotRewriteAuthorityBinding(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-outbox-owner", 15)
	start := standardStart(t, now, owner, "postgres-outbox")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-outbox"), owner, ReconciliationTransportAmbiguous,
	)
	committed, err := store.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("commit reconciliation outbox: %v", err)
	}
	before, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil {
		t.Fatalf("inspect before acknowledgement: %v", err)
	}
	payloadDigest := digestBytes(intent.canonical)
	ack := outboxAcknowledgement{
		OperationID: intent.OperationID, DecisionID: committed.Fact.DecisionID,
		CanonicalRequestDigest: intent.CanonicalDigest, PayloadDigest: payloadDigest, AckDigest: digest(91),
	}
	first, err := store.acknowledgeOutbox(context.Background(), ack)
	if err != nil || first.Disposition != OutboxAcknowledged || first.DeliveryCount != 1 {
		t.Fatalf("acknowledge outbox: view=%+v err=%v", first, err)
	}
	duplicate, err := store.acknowledgeOutbox(context.Background(), ack)
	if err != nil || duplicate != first {
		t.Fatalf("duplicate exact acknowledgement = %+v err=%v, want %+v", duplicate, err, first)
	}
	conflict := ack
	conflict.AckDigest = digest(92)
	_, err = store.acknowledgeOutbox(context.Background(), conflict)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting acknowledgement error = %T %v, want non-retryable integrity conflict", err, err)
	}
	after, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || after != before {
		t.Fatalf("acknowledgement rewrote Runtime authority: before=%#v after=%#v err=%v", before, after, err)
	}
	if first.OperationID != intent.OperationID || first.DecisionID != committed.Fact.DecisionID ||
		first.CanonicalRequestDigest != intent.CanonicalDigest || first.PayloadDigest != payloadDigest {
		t.Fatalf("acknowledgement changed immutable outbox binding: %+v", first)
	}
}

func TestPostgresProjectionFailureDoesNotRollbackCommittedAuthority(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-projection-owner", 16)
	start := standardStart(t, now, owner, "postgres-projection")
	observedCommitted := false
	projection := ProjectionDeliveryFunc(func(ctx context.Context, fact ProjectionFact) error {
		var decisions int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+schema+`.runtime_execution_decisions
			WHERE decision_id=$1`, fact.DecisionID.String()).Scan(&decisions); err == nil && decisions == 1 {
			observedCommitted = true
		}
		return errors.New("postgres://projection-credential-canary/private/runtime/path")
	})
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, ProjectionDelivery: projection,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-projection"), owner, ReconciliationProjectionDelivery,
	)
	committed, err := store.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("projection failure rolled back or escaped committed Decision: %v", err)
	}
	if !observedCommitted {
		t.Fatal("projection delivery ran before the authoritative transaction committed")
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1}) {
		t.Fatalf("projection failure changed authoritative family counts: %+v", counts)
	}
	var auditStatus, telemetryStatus ProjectionDeliveryStatus
	var attempts uint64
	var safeFailure ProjectionSafeFailure
	var degraded bool
	if err := db.QueryRowContext(context.Background(), `SELECT audit_delivery_status,
		telemetry_delivery_status, attempt_count, last_safe_failure, degraded
		FROM `+schema+`.runtime_execution_projection_backlog WHERE fact_id=$1`,
		committed.Fact.DecisionID.String()).Scan(&auditStatus, &telemetryStatus, &attempts, &safeFailure, &degraded); err != nil {
		t.Fatal("inspect durable projection backlog")
	}
	if auditStatus != ProjectionFailed || telemetryStatus != ProjectionFailed || attempts != 1 ||
		safeFailure != ProjectionFailureUnavailable || !degraded {
		t.Fatalf("projection failure backlog = audit:%v telemetry:%v attempts:%d failure:%v degraded:%v",
			auditStatus, telemetryStatus, attempts, safeFailure, degraded)
	}
}

func TestPostgresHeartbeatCompactionPreservesCurrentAuthorityFacts(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-retention-owner", 17)
	start := standardStart(t, now, owner, "postgres-retention")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	fixture.RuntimeRevision = 9
	fixture.State = RuntimeTerminal
	fixture.Outcome = RuntimeFailed
	fixture.Lease = RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, LeaseID: SandboxLeaseID{value: "lease-retention"}, Generation: 3, Fence: 7,
	}
	fixture.EvidenceRoot = EvidenceRootSnapshot{
		SchemaVersion: SchemaV1, EvidenceRootID: EvidenceRootID{value: "evidence-root-retention"}, Digest: digest(70),
	}
	fixture.Capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionNone,
		Physical:       PhysicalCapacityUnknownOrQuarantined,
	}
	fixture.Reconciliation = ReconciliationRequiredStatus
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	original := RuntimeDecisionFact{
		DecisionID:  RuntimeDecisionID{value: "runtime-decision-retention"},
		Disposition: DecisionAccepted, OperationID: start.OperationID,
		CanonicalRequestDigest:   start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	installPostgresAcceptedStartFacts(t, db, schema, start, original, now)
	installPostgresRetentionFacts(t, db, schema, fixture, original.DecisionID, now)

	request := heartbeatCompactionRequest{
		RuntimeRunID: fixture.RuntimeRunID, LeaseID: fixture.Lease.LeaseID,
		LeaseGeneration: fixture.Lease.Generation, LeaseFence: fixture.Lease.Fence,
		Reason: HeartbeatTerminalHistory, EvidenceRoot: fixture.EvidenceRoot,
	}
	first, err := store.compactTerminalHeartbeatHistory(context.Background(), request)
	if err != nil {
		t.Fatalf("compact terminal heartbeat history: %v", err)
	}
	if first.CompactedCount != 2 || first.PreservedConflictCount != 1 ||
		first.PreservedUncontainedCount != 1 || first.PreservedReconciliationCount != 1 ||
		first.OpenCleanupDebtCount != 1 {
		t.Fatalf("heartbeat compaction view = %+v", first)
	}
	second, err := store.compactTerminalHeartbeatHistory(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("repeat compaction = %+v err=%v, want idempotent %+v", second, err, first)
	}

	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil {
		t.Fatalf("inspect after heartbeat compaction: %v", err)
	}
	if snapshot.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined ||
		snapshot.Reconciliation != ReconciliationRequiredStatus || snapshot.Lease != fixture.Lease {
		t.Fatalf("compaction released capacity, closed reconciliation, or changed current lease: %#v", snapshot)
	}
	var currentLease, leaseConflict, uncontained, unresolvedReconciliation, openDebt, rawHeartbeat, summaries int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_roots WHERE current_fact),
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_roots WHERE conflict),
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_roots WHERE uncontained),
		(SELECT count(*) FROM `+schema+`.runtime_execution_reconciliation_obligations WHERE unresolved),
		(SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_obligations WHERE unresolved),
		(SELECT count(*) FROM `+schema+`.runtime_execution_heartbeat_history),
		(SELECT count(*) FROM `+schema+`.runtime_execution_heartbeat_compaction)`).Scan(
		&currentLease, &leaseConflict, &uncontained, &unresolvedReconciliation, &openDebt, &rawHeartbeat, &summaries,
	); err != nil {
		t.Fatal("inspect retained heartbeat authority")
	}
	if currentLease != 1 || leaseConflict != 1 || uncontained != 1 || unresolvedReconciliation != 1 ||
		openDebt != 1 || rawHeartbeat != 3 || summaries != 1 {
		t.Fatalf("compaction lost protected facts: lease=%d conflict=%d uncontained=%d reconciliation=%d debt=%d raw=%d summaries=%d",
			currentLease, leaseConflict, uncontained, unresolvedReconciliation, openDebt, rawHeartbeat, summaries)
	}
}

func TestPostgresCommitResponseLossReplaysOriginalDecisionAfterRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 15, 0, 0, 123456789, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-response-loss-owner", 18)
	start := standardStart(t, now, owner, "postgres-response-loss")
	faults := &PersistenceFaultController{}
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: faults,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-response-loss"), owner, ReconciliationTransportAmbiguous,
	)
	if err := faults.FailNextAt(PersistenceFaultBeforeResponse); err != nil {
		t.Fatalf("configure response-loss fault: %v", err)
	}
	_, err = store.persistReconciliationFoundation(context.Background(), intent)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorReconciliationRequired {
		t.Fatalf("response-loss error = %T %v, want reconciliation required", err, err)
	}

	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	replayed, err := restarted.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("replay after response loss: %v", err)
	}
	if replayed.Fact.DecisionID.String() != "runtime-decision-postgres-00000000000000000001" ||
		replayed.Fact.OperationID != intent.OperationID || replayed.Fact.CanonicalRequestDigest != intent.CanonicalDigest ||
		replayed.Snapshot.RuntimeRevision != fixture.RuntimeRevision+1 || replayed.Snapshot.State != RuntimeReconciling ||
		replayed.Snapshot.Operation != fixture.Operation {
		t.Fatalf("response-loss replay allocated or rewrote authority facts: %#v", replayed)
	}
	replayedAgain, err := restarted.persistReconciliationFoundation(context.Background(), intent)
	if err != nil || replayedAgain != replayed {
		t.Fatalf("second exact replay = %#v err=%v, want %#v", replayedAgain, err, replayed)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1}) {
		t.Fatalf("response-loss retry duplicated authoritative facts: %+v", counts)
	}
}

func TestPostgresConcurrentWritersProduceOneRuntimeRevisionWinner(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-concurrent-owner", 19)
	start := standardStart(t, now, owner, "postgres-concurrent")
	first := newMigratedPostgresAuthority(t, db, schema, now)
	second, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("new second PostgreSQL writer: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intents := []reconciliationFoundationIntent{
		newReconciliationFoundationIntent(fixture, mustOperationID(t, "reconcile-concurrent-a"), owner, ReconciliationTransportAmbiguous),
		newReconciliationFoundationIntent(fixture, mustOperationID(t, "reconcile-concurrent-b"), owner, ReconciliationProjectionDelivery),
	}
	stores := []*PostgresAuthority{first, second}
	startBarrier := make(chan struct{})
	type result struct {
		decision RuntimeDecision
		err      error
	}
	results := make(chan result, len(stores))
	var writers sync.WaitGroup
	for index := range stores {
		writers.Add(1)
		go func(store *PostgresAuthority, intent reconciliationFoundationIntent) {
			defer writers.Done()
			<-startBarrier
			decision, executeErr := store.persistReconciliationFoundation(context.Background(), intent)
			results <- result{decision: decision, err: executeErr}
		}(stores[index], intents[index])
	}
	close(startBarrier)
	writers.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		if result.err == nil {
			winners++
			if result.decision.Snapshot.RuntimeRevision != fixture.RuntimeRevision+1 {
				t.Fatalf("winner revision = %d", result.decision.Snapshot.RuntimeRevision)
			}
			continue
		}
		var safeError *Error
		if errors.As(result.err, &safeError) && safeError.Code() == ErrorIntegrityConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent writer returned unsafe error: %T %v", result.err, result.err)
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent writers: winners=%d conflicts=%d", winners, conflicts)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1}) {
		t.Fatalf("concurrent writers left split authority: %+v", counts)
	}
}

func TestPostgresPreCommitFaultsLeaveNoAuthoritativePartialState(t *testing.T) {
	faultPoints := []PersistenceFaultPoint{
		PersistenceFaultBeforeRuntimeWrite,
		PersistenceFaultBeforeDecision,
		PersistenceFaultAfterMandatoryAudit,
		PersistenceFaultBeforeOutbox,
		PersistenceFaultBeforeCommit,
	}
	for _, faultPoint := range faultPoints {
		t.Run(faultPoint.String(), func(t *testing.T) {
			db, schema := testpostgres.Open(t, "runtime_execution_test")
			now := time.Date(2026, 7, 28, 17, 0, 0, 0, time.UTC)
			owner := mustTaskOrchestrationAuthority(t, "postgres-precommit-owner", 20)
			start := standardStart(t, now, owner, "postgres-precommit-"+faultPoint.String())
			faults := &PersistenceFaultController{}
			store, err := NewPostgresAuthority(db, PostgresConfig{
				Schema: schema, Now: func() time.Time { return now }, Faults: faults,
			})
			if err != nil {
				t.Fatalf("new PostgreSQL authority: %v", err)
			}
			if err := store.Migrate(context.Background()); err != nil {
				t.Fatalf("prepare PostgreSQL authority schema: %v", err)
			}
			fixture := acceptedPostgresRuntimeFixture(start, owner, now)
			installPostgresRuntimeFixture(t, db, schema, fixture, now)
			intent := newReconciliationFoundationIntent(
				fixture, mustOperationID(t, "reconcile-precommit-"+faultPoint.String()), owner,
				ReconciliationTransportAmbiguous,
			)
			if err := faults.FailNextAt(faultPoint); err != nil {
				t.Fatalf("configure pre-commit fault: %v", err)
			}
			_, err = store.persistReconciliationFoundation(context.Background(), intent)
			var safeError *Error
			if !errors.As(err, &safeError) || safeError.Code() != ErrorDependencyUnavailable {
				t.Fatalf("pre-commit fault error = %T %v, want safe unavailable", err, err)
			}
			counts := postgresFoundationCounts(t, db, schema)
			if counts != (foundationCounts{}) {
				t.Fatalf("pre-commit fault left partial facts: %+v", counts)
			}
			snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
				SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
				PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
			})
			if err != nil || snapshot.RuntimeRevision != fixture.RuntimeRevision ||
				snapshot.State != RuntimeWaitingForLease || snapshot.Reconciliation != ReconciliationStable {
				t.Fatalf("pre-commit fault changed Runtime authority: %#v err=%v", snapshot, err)
			}
		})
	}
}

func TestPostgresFoundationDoesNotCreateAcceptedBoundHalfState(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-no-half-state-owner", 21)
	start := standardStart(t, now, owner, "postgres-no-half-state")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: owner,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision,
		OperationGeneration: start.ExpectedOperationGeneration, RuntimeFence: start.ExpectedRuntimeFence,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeCreated,
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	_, err := store.Execute(context.Background(), start)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorDependencyUnavailable ||
		safeError.RetryDisposition() != RetryAfterDependency {
		t.Fatalf("fresh Start before #75 = %T %v, want dependency-gated safe error", err, err)
	}
	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || snapshot.State != RuntimeCreated || snapshot.Operation.Status != OperationUnbound ||
		snapshot.Lease.AcquireStatus != LeaseNotRequested || snapshot.RuntimeRevision != fixture.RuntimeRevision {
		t.Fatalf("#74 created a half Accepted/Bound state: %#v err=%v", snapshot, err)
	}
	if counts := postgresFoundationCounts(t, db, schema); counts != (foundationCounts{}) {
		t.Fatalf("fresh Start created #75 authority facts: %+v", counts)
	}
}

func TestPostgresWritersForDifferentRuntimesUseRowLevelConcurrency(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 19, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-row-lock-owner", 22)
	firstStart := standardStart(t, now, owner, "postgres-row-lock-a")
	secondStart := standardStart(t, now, owner, "postgres-row-lock-b")
	firstFixture := acceptedPostgresRuntimeFixture(firstStart, owner, now)
	secondFixture := acceptedPostgresRuntimeFixture(secondStart, owner, now)
	blocker := &blockingPersistenceFault{
		point: PersistenceFaultBeforeDecision, entered: make(chan struct{}), release: make(chan struct{}),
	}
	first, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: blocker,
	})
	if err != nil {
		t.Fatalf("new first row-lock writer: %v", err)
	}
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	second, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("new second row-lock writer: %v", err)
	}
	installPostgresRuntimeFixture(t, db, schema, firstFixture, now)
	installPostgresRuntimeFixture(t, db, schema, secondFixture, now)
	firstIntent := newReconciliationFoundationIntent(
		firstFixture, mustOperationID(t, "reconcile-row-lock-a"), owner, ReconciliationTransportAmbiguous,
	)
	secondIntent := newReconciliationFoundationIntent(
		secondFixture, mustOperationID(t, "reconcile-row-lock-b"), owner, ReconciliationTransportAmbiguous,
	)
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	firstResult := make(chan executeResult, 1)
	go func() {
		decision, executeErr := first.persistReconciliationFoundation(context.Background(), firstIntent)
		firstResult <- executeResult{decision: decision, err: executeErr}
	}()
	<-blocker.entered
	secondResult := make(chan executeResult, 1)
	go func() {
		decision, executeErr := second.persistReconciliationFoundation(context.Background(), secondIntent)
		secondResult <- executeResult{decision: decision, err: executeErr}
	}()
	select {
	case result := <-secondResult:
		if result.err != nil || result.decision.Snapshot.RuntimeRunID != secondFixture.RuntimeRunID {
			t.Fatalf("different Runtime writer failed behind unrelated row lock: decision=%#v err=%v", result.decision, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("different Runtime writer was blocked by a non-row-level authority lock")
	}
	close(blocker.release)
	result := <-firstResult
	if result.err != nil || result.decision.Snapshot.RuntimeRunID != firstFixture.RuntimeRunID {
		t.Fatalf("first row-lock writer: decision=%#v err=%v", result.decision, result.err)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 2, Requests: 2, Revisions: 2, AuditFacts: 2, Outbox: 2, Reconciliation: 2, Projection: 2}) {
		t.Fatalf("row-level writers left incomplete facts: %+v", counts)
	}
}

func TestPostgresCorruptPersistenceFailsClosedWithoutPrivateDetail(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-corrupt-owner", 23)
	start := standardStart(t, now, owner, "postgres-corrupt")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: owner,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision,
		OperationGeneration: start.ExpectedOperationGeneration, RuntimeFence: start.ExpectedRuntimeFence,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeCreated,
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	canaries := []string{
		"postgres://authority:credential@database/runtime",
		"/private/runtime/path",
		"SELECT secret FROM " + schema + ".runtime_execution_runtimes",
	}
	corrupt := []byte(`{"unknown_required_field":"` + canaries[0] + `","path":"` + canaries[1] + `"}`)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_runtimes
		SET aggregate_state=$1 WHERE runtime_run_id=$2`, corrupt, fixture.RuntimeRunID.String()); err != nil {
		t.Fatal("install corrupt PostgreSQL state")
	}
	_, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever || safeError.ReconciliationDisposition() != ReconciliationNotRequired {
		t.Fatalf("corrupt PostgreSQL state error = %T %v, want closed non-retryable integrity error", err, err)
	}
	for _, canary := range canaries {
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("safe persistence error leaked %q", canary)
		}
	}
	if strings.Contains(err.Error(), schema) || strings.Contains(err.Error(), "runtime_execution_runtimes") {
		t.Fatal("safe persistence error leaked schema or table detail")
	}
}

func TestPostgresCorruptMandatoryAuditFailsClosedOnReplay(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 20, 30, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-corrupt-audit-owner", 23)
	start := standardStart(t, now, owner, "postgres-corrupt-audit")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := RuntimeDecisionFact{
		DecisionID:  RuntimeDecisionID{value: "runtime-decision-postgres-corrupt-audit"},
		Disposition: DecisionAccepted, OperationID: start.OperationID,
		CanonicalRequestDigest:   start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)

	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_immutable_mutation ON `+schema+
		`.runtime_execution_mandatory_audit`); err != nil {
		t.Fatal("disable immutable trigger for corruption fixture")
	}
	corruptDigest := digest(99)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_mandatory_audit
		SET canonical_digest=$1 WHERE decision_id=$2`, corruptDigest[:], fact.DecisionID.String()); err != nil {
		t.Fatal("install corrupt mandatory-audit fixture")
	}

	_, err := store.Execute(context.Background(), start)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("corrupt mandatory-audit replay error = %T %v, want non-retryable integrity conflict", err, err)
	}
	if strings.Contains(err.Error(), schema) || strings.Contains(err.Error(), "runtime_execution_mandatory_audit") {
		t.Fatal("corrupt mandatory-audit error leaked schema or table detail")
	}
}

func TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 21, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-outbox-conflict-owner", 24)
	start := standardStart(t, now, owner, "postgres-outbox-conflict")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	operationID := mustOperationID(t, "reconcile-outbox-conflict")
	originalIntent := newReconciliationFoundationIntent(
		fixture, operationID, owner, ReconciliationTransportAmbiguous,
	)
	original, err := store.persistReconciliationFoundation(context.Background(), originalIntent)
	if err != nil {
		t.Fatalf("commit original outbox: %v", err)
	}
	want := readPostgresOutboxBinding(t, db, schema, operationID)

	exact, err := store.persistReconciliationFoundation(context.Background(), originalIntent)
	if err != nil || exact != original {
		t.Fatalf("exact duplicate = %#v err=%v, want %#v", exact, err, original)
	}
	conflictingIntent := newReconciliationFoundationIntent(
		fixture, operationID, owner, ReconciliationProjectionDelivery,
	)
	_, err = store.persistReconciliationFoundation(context.Background(), conflictingIntent)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting outbox binding error = %T %v, want non-retryable integrity conflict", err, err)
	}
	got := readPostgresOutboxBinding(t, db, schema, operationID)
	if got.OperationID != want.OperationID || got.DecisionID != want.DecisionID || got.RuntimeRunID != want.RuntimeRunID ||
		!bytes.Equal(got.CanonicalDigest, want.CanonicalDigest) || !bytes.Equal(got.ScopeDigest, want.ScopeDigest) ||
		!bytes.Equal(got.Payload, want.Payload) || !bytes.Equal(got.PayloadDigest, want.PayloadDigest) {
		t.Fatalf("conflicting duplicate rewrote immutable outbox: before=%+v after=%+v", want, got)
	}
	if counts := postgresFoundationCounts(t, db, schema); counts != (foundationCounts{
		Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1,
	}) {
		t.Fatalf("duplicate/conflict created additional facts: %+v", counts)
	}
}

type postgresOutboxBinding struct {
	OperationID     string
	DecisionID      string
	RuntimeRunID    string
	CanonicalDigest []byte
	ScopeDigest     []byte
	Payload         []byte
	PayloadDigest   []byte
}

func readPostgresOutboxBinding(
	t *testing.T,
	db *sql.DB,
	schema string,
	operationID OperationID,
) postgresOutboxBinding {
	t.Helper()
	var binding postgresOutboxBinding
	if err := db.QueryRowContext(context.Background(), `SELECT operation_id, decision_id, runtime_run_id,
		canonical_request_digest, authority_scope_digest, payload, payload_digest
		FROM `+schema+`.runtime_execution_outbox WHERE operation_id=$1`, operationID.String()).Scan(
		&binding.OperationID, &binding.DecisionID, &binding.RuntimeRunID, &binding.CanonicalDigest,
		&binding.ScopeDigest, &binding.Payload, &binding.PayloadDigest,
	); err != nil {
		t.Fatal("inspect immutable PostgreSQL outbox binding")
	}
	return binding
}

type blockingPersistenceFault struct {
	point   PersistenceFaultPoint
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (fault *blockingPersistenceFault) FailAt(point PersistenceFaultPoint) bool {
	if point != fault.point {
		return false
	}
	fault.once.Do(func() {
		close(fault.entered)
		<-fault.release
	})
	return false
}

func TestPostgresAuthorityReconstructsRuntimeSnapshotAfterRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postgres-owner", 11)
	fixture := RuntimeFixture{
		PersonalWorkspaceID: mustPersonalWorkspaceID(t, "postgres-workspace"),
		TaskID:              mustTaskID(t, "postgres-task"),
		PhaseRunID:          mustPhaseRunID(t, "postgres-phase"),
		RuntimeRunID:        mustRuntimeRunID(t, "postgres-runtime"),
		Owner:               authority,
		TaskRevision:        7,
		RuntimeRevision:     3,
		OperationGeneration: 5,
		RuntimeFence:        9,
		SafetyEpoch:         13,
		State:               RuntimeCreated,
		Deadline:            now.Add(30 * time.Minute),
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld,
			NoLease:        NoLeaseDispositionNone,
			Physical:       PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}

	authorityStore := newMigratedPostgresAuthority(t, db, schema, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	ref := RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: authority,
	}
	want, err := authorityStore.Inspect(context.Background(), ref)
	if err != nil {
		t.Fatalf("inspect seeded Runtime: %v", err)
	}

	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	got, err := restarted.Inspect(context.Background(), ref)
	if err != nil {
		t.Fatalf("inspect after restart: %v", err)
	}
	if got != want || got.RuntimeRunID != fixture.RuntimeRunID || got.RuntimeRevision != fixture.RuntimeRevision ||
		got.RuntimeFence != fixture.RuntimeFence || got.State != RuntimeCreated {
		t.Fatalf("restart reconstructed %#v, want %#v", got, want)
	}
}

func newMigratedPostgresAuthority(
	t *testing.T,
	db *sql.DB,
	schema string,
	now time.Time,
) *PostgresAuthority {
	t.Helper()
	authority, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := authority.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	return authority
}

func installPostgresRuntimeFixture(
	t *testing.T,
	db *sql.DB,
	schema string,
	fixture RuntimeFixture,
	now time.Time,
) {
	t.Helper()
	state, err := encodePostgresRuntimeFixture(fixture)
	if err != nil {
		t.Fatalf("encode PostgreSQL Runtime fixture: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_runtimes (
		runtime_run_id, personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id,
		aggregate_state, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		fixture.RuntimeRunID.String(), fixture.PersonalWorkspaceID.String(), fixture.TaskID.String(), fixture.PhaseRunID.String(),
		fixture.Owner.id.String(), fixture.Owner.generation, fixture.Owner.kind,
		fixture.TaskRevision, fixture.RuntimeRevision, fixture.OperationGeneration, fixture.RuntimeFence,
		fixture.SafetyEpoch, fixture.State, fixture.Outcome, fixture.TerminalEvidenceID.String(), state, now.UTC())
	if err != nil {
		t.Fatal("install PostgreSQL Runtime fixture")
	}
}

func acceptedPostgresRuntimeFixture(start StartRuntimeRun, owner RuntimeAuthority, now time.Time) RuntimeFixture {
	return RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: owner,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision + 1,
		OperationGeneration: start.ExpectedOperationGeneration + 1, RuntimeFence: start.ExpectedRuntimeFence + 1,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeWaitingForLease,
		Operation: RuntimeOperationBinding{
			Status: OperationBound, OperationID: start.OperationID, Digest: start.CanonicalRequestDigest,
			Generation: start.ExpectedOperationGeneration + 1, AdmissionGrantID: start.AdmissionGrant.AdmissionGrantID,
			GrantGeneration: start.AdmissionGrant.Generation,
		},
		Lease:    RuntimeLeaseSnapshot{AcquireStatus: LeaseAcquirePending},
		Deadline: start.Deadline, LeaseAcquireBy: now.Add(15 * time.Minute),
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}
}

func installPostgresAcceptedStartFacts(
	t *testing.T,
	db *sql.DB,
	schema string,
	start StartRuntimeRun,
	fact RuntimeDecisionFact,
	now time.Time,
) {
	t.Helper()
	decisionState, err := encodePostgresDecisionFact(fact)
	if err != nil {
		t.Fatalf("encode retained Decision: %v", err)
	}
	canonicalPayload, err := canonicalStartEncoding(start)
	if err != nil {
		t.Fatalf("encode retained canonical Start: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("begin retained Decision fixture")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_decisions (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		fact.DecisionID.String(), start.RuntimeRunID.String(), start.OperationID.String(), start.CanonicalRequestDigest[:],
		fact.PreviousRuntimeRevision, fact.ResultingRuntimeRevision, decisionState, now.UTC()); err != nil {
		t.Fatal("install retained Decision fixture")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_requests (
		personal_workspace_id, runtime_run_id, command_kind, operation_id,
		canonical_request_digest, canonical_request, decision_id,
		admission_grant_id, admission_grant_generation
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		start.PersonalWorkspaceID.String(), start.RuntimeRunID.String(), CommandStartRuntimeRun, start.OperationID.String(),
		start.CanonicalRequestDigest[:], canonicalPayload, fact.DecisionID.String(),
		start.AdmissionGrant.AdmissionGrantID.String(), start.AdmissionGrant.Generation); err != nil {
		t.Fatal("install retained request fixture")
	}
	auditDigest := mandatoryAuditCanonicalDigest(
		fact.DecisionID, start.RuntimeRunID, start.OperationID, start.CanonicalRequestDigest,
		start.Authority, fact.PreviousRuntimeRevision, fact.ResultingRuntimeRevision, now.UTC(),
	)
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_mandatory_audit (
		audit_fact_id, decision_id, runtime_run_id, operation_id, request_digest, canonical_digest,
		authority_kind, authority_id, authority_generation, before_revision, after_revision, recorded_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		"runtime-audit-postgres-replay", fact.DecisionID.String(), start.RuntimeRunID.String(), start.OperationID.String(),
		start.CanonicalRequestDigest[:], auditDigest[:], start.Authority.kind, start.Authority.id.String(), start.Authority.generation,
		fact.PreviousRuntimeRevision, fact.ResultingRuntimeRevision, now.UTC()); err != nil {
		t.Fatal("install retained mandatory audit fixture")
	}
	payloadDigest := digestBytes(canonicalPayload)
	scopeDigest := authorityScopeDigest(start.PersonalWorkspaceID, start.RuntimeRunID)
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_outbox (
		operation_id, decision_id, runtime_run_id, canonical_request_digest,
		authority_scope_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		start.OperationID.String(), fact.DecisionID.String(), start.RuntimeRunID.String(), start.CanonicalRequestDigest[:],
		scopeDigest[:], canonicalPayload, payloadDigest[:], now.UTC()); err != nil {
		t.Fatal("install retained outbox fixture")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("commit retained Decision fixture")
	}
}

func installPostgresRetentionFacts(
	t *testing.T,
	db *sql.DB,
	schema string,
	fixture RuntimeFixture,
	decisionID RuntimeDecisionID,
	now time.Time,
) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("begin PostgreSQL retention fixtures")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_evidence_roots (
		evidence_root_id, runtime_run_id, schema_version, digest, accepted_at
	) VALUES ($1,$2,$3,$4,$5)`, fixture.EvidenceRoot.EvidenceRootID.String(), fixture.RuntimeRunID.String(),
		fixture.EvidenceRoot.SchemaVersion, fixture.EvidenceRoot.Digest[:], now.UTC()); err != nil {
		t.Fatal("install retained evidence root")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_lease_roots (
		runtime_run_id, lease_id, lease_generation, lease_fence, current_fact,
		conflict, uncontained, evidence_root_id, evidence_root_digest, updated_at
	) VALUES ($1,$2,$3,$4,TRUE,TRUE,TRUE,$5,$6,$7)`, fixture.RuntimeRunID.String(), fixture.Lease.LeaseID.String(),
		fixture.Lease.Generation, fixture.Lease.Fence, fixture.EvidenceRoot.EvidenceRootID.String(),
		fixture.EvidenceRoot.Digest[:], now.UTC()); err != nil {
		t.Fatal("install current lease authority root")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_reconciliation_obligations (
		operation_id, runtime_run_id, decision_id, reason, status,
		first_recorded_at, last_recorded_at, observation_count, unresolved,
		evidence_root_id, evidence_root_digest
	) VALUES ($1,$2,$3,$4,$5,$6,$6,1,TRUE,$7,$8)`, "reconcile-retention", fixture.RuntimeRunID.String(),
		decisionID.String(), ReconciliationTransportAmbiguous, ReconciliationObligationOpen, now.UTC(),
		fixture.EvidenceRoot.EvidenceRootID.String(), fixture.EvidenceRoot.Digest[:]); err != nil {
		t.Fatal("install unresolved reconciliation authority")
	}
	resourceDigest := digest(71)
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_cleanup_obligations (
		debt_id, runtime_run_id, resource_identity_digest, resource_generation,
		resource_fence, status, unresolved, uncontained, first_recorded_at,
		last_recorded_at, attempt_count, safe_reason, evidence_root_id, evidence_root_digest
	) VALUES ($1,$2,$3,$4,$5,$6,TRUE,TRUE,$7,$7,1,$8,$9,$10)`, "cleanup-debt-retention",
		fixture.RuntimeRunID.String(), resourceDigest[:], uint64(4), uint64(8), CleanupObligationOpen,
		now.UTC(), CleanupReasonUncontained, fixture.EvidenceRoot.EvidenceRootID.String(), fixture.EvidenceRoot.Digest[:]); err != nil {
		t.Fatal("install unresolved Cleanup Debt authority")
	}
	for sequence, flags := range []struct {
		terminal, conflict, uncontained, unresolved bool
	}{
		{terminal: true},
		{terminal: true},
		{conflict: true},
		{uncontained: true},
		{unresolved: true},
	} {
		observedAt := now.Add(time.Duration(sequence) * time.Second)
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_heartbeat_history (
			runtime_run_id, heartbeat_sequence, lease_id, lease_generation, lease_fence,
			observed_at, reason, evidence_root_id, evidence_root_digest,
			terminal_history, conflict, uncontained, unresolved_reconciliation
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, fixture.RuntimeRunID.String(), sequence+1,
			fixture.Lease.LeaseID.String(), fixture.Lease.Generation, fixture.Lease.Fence, observedAt,
			HeartbeatTerminalHistory, fixture.EvidenceRoot.EvidenceRootID.String(), fixture.EvidenceRoot.Digest[:],
			flags.terminal, flags.conflict, flags.uncontained, flags.unresolved); err != nil {
			t.Fatal("install heartbeat retention history")
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("commit PostgreSQL retention fixtures")
	}
}

type foundationCounts struct {
	Decisions      int
	Requests       int
	Revisions      int
	AuditFacts     int
	Outbox         int
	Reconciliation int
	Projection     int
}

func postgresFoundationCounts(t *testing.T, db *sql.DB, schema string) foundationCounts {
	t.Helper()
	var counts foundationCounts
	err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_decisions),
		(SELECT count(*) FROM `+schema+`.runtime_execution_requests),
		(SELECT count(*) FROM `+schema+`.runtime_execution_revisions),
		(SELECT count(*) FROM `+schema+`.runtime_execution_mandatory_audit),
		(SELECT count(*) FROM `+schema+`.runtime_execution_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_reconciliation_obligations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_projection_backlog)`).Scan(
		&counts.Decisions, &counts.Requests, &counts.Revisions, &counts.AuditFacts,
		&counts.Outbox, &counts.Reconciliation, &counts.Projection,
	)
	if err != nil {
		t.Fatal("inspect PostgreSQL foundation fact counts")
	}
	return counts
}
