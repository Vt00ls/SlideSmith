package taskorchestration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
)

type postgresGatewayProbe struct {
	db       *sql.DB
	schema   string
	delegate *runtimeexecution.DeterministicGateway
	visible  bool
}

type blockingGatewayRefresh struct {
	mu       sync.Mutex
	delegate *runtimeexecution.DeterministicGateway
	entered  chan struct{}
	release  chan struct{}
	blocked  bool
}

func (adapter *blockingGatewayRefresh) DecideGatewayGrant(
	ctx context.Context,
	request runtimeexecution.GatewayGrantRequest,
) (runtimeexecution.GatewayGrantDecision, error) {
	adapter.mu.Lock()
	block := request.RequestedGeneration == 2 && !adapter.blocked
	if block {
		adapter.blocked = true
		close(adapter.entered)
	}
	adapter.mu.Unlock()
	if block {
		<-adapter.release
	}
	return adapter.delegate.DecideGatewayGrant(ctx, request)
}

func (adapter *blockingGatewayRefresh) InspectGatewayGrant(
	ctx context.Context,
	ref runtimeexecution.GatewayGrantOperationRef,
) (runtimeexecution.GatewayGrantDecision, error) {
	return adapter.delegate.InspectGatewayGrant(ctx, ref)
}

func (adapter *blockingGatewayRefresh) QueryUsageReceiptEvidence(
	ctx context.Context,
	runtimeRunID runtimeexecution.RuntimeRunID,
) (runtimeexecution.UsageReceiptEvidence, error) {
	return adapter.delegate.QueryUsageReceiptEvidence(ctx, runtimeRunID)
}

func (probe *postgresGatewayProbe) DecideGatewayGrant(
	ctx context.Context,
	request runtimeexecution.GatewayGrantRequest,
) (runtimeexecution.GatewayGrantDecision, error) {
	var outboxCount, acceptanceCount int
	err := probe.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s.runtime_execution_gateway_outbox
			WHERE operation_id=$1 AND canonical_request_digest=$2),
		(SELECT count(*) FROM %s.runtime_execution_gateway_grant_acceptances
			WHERE operation_id=$1)`, probe.schema, probe.schema),
		request.OperationID.String(), request.CanonicalRequestDigest[:]).Scan(&outboxCount, &acceptanceCount)
	if err == nil && outboxCount == 1 && acceptanceCount == 0 {
		probe.visible = true
	}
	return probe.delegate.DecideGatewayGrant(ctx, request)
}

func (probe *postgresGatewayProbe) InspectGatewayGrant(
	ctx context.Context,
	ref runtimeexecution.GatewayGrantOperationRef,
) (runtimeexecution.GatewayGrantDecision, error) {
	return probe.delegate.InspectGatewayGrant(ctx, ref)
}

func (probe *postgresGatewayProbe) QueryUsageReceiptEvidence(
	ctx context.Context,
	runtimeRunID runtimeexecution.RuntimeRunID,
) (runtimeexecution.UsageReceiptEvidence, error) {
	return probe.delegate.QueryUsageReceiptEvidence(ctx, runtimeRunID)
}

func TestPostgresGatewayRequestIsDurableBeforeCallAndResponseLossReconciles(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 0, 0, 0, time.UTC)
	system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(t, now, "durable-request")
	gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	probe := &postgresGatewayProbe{db: system.db, schema: system.schema, delegate: gateway}
	providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, probe, nil)
	if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
		t.Fatal(err)
	}
	gateway.LoseNextGrantResponse()

	accepted, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("execute provider Runtime through response loss: %v", err)
	}
	if !probe.visible {
		t.Fatal("Gateway adapter ran before its durable request was externally visible")
	}
	if accepted.Snapshot.Gateway.Status != runtimeexecution.GatewayGrantCurrent ||
		!accepted.Snapshot.Gateway.Ready || accepted.Snapshot.Gateway.CurrentGrant.Generation != 1 ||
		accepted.Snapshot.Readiness.LLMGateway.State != runtimeexecution.PrerequisiteAccepted ||
		!accepted.Snapshot.Readiness.CapsuleReady ||
		accepted.Snapshot.Usage.Disposition != runtimeexecution.UsageEvidenceMissing {
		t.Fatalf("accepted provider prerequisite = %+v readiness=%+v usage=%+v",
			accepted.Snapshot.Gateway, accepted.Snapshot.Readiness, accepted.Snapshot.Usage)
	}
	var acceptanceCount, currentGeneration int
	if err := system.db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s.runtime_execution_gateway_grant_acceptances WHERE runtime_run_id=$1),
		(SELECT grant_generation FROM %s.runtime_execution_gateway_current WHERE runtime_run_id=$1)`,
		system.schema, system.schema), work.start.RuntimeRunID.String()).Scan(&acceptanceCount, &currentGeneration); err != nil {
		t.Fatalf("inspect durable Gateway acceptance: %v", err)
	}
	if acceptanceCount != 1 || currentGeneration != 1 {
		t.Fatalf("durable Gateway acceptance count/generation = %d/%d", acceptanceCount, currentGeneration)
	}

	restarted := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, probe, nil)
	replayed, err := restarted.Execute(context.Background(), work.start)
	if err != nil || replayed.Snapshot.Gateway != accepted.Snapshot.Gateway {
		t.Fatalf("restart lost accepted Gateway prerequisite: replay=%+v err=%v want=%+v",
			replayed.Snapshot.Gateway, err, accepted.Snapshot.Gateway)
	}
}

func TestPostgresInspectProjectsReservationDriftWithoutPersistingGatewayStaleness(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 5, 0, 0, time.UTC)
	system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(t, now, "inspect-drift")
	gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, nil)
	if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
		t.Fatal(err)
	}
	accepted, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare current PostgreSQL Gateway: snapshot=%+v err=%v", accepted.Snapshot, err)
	}
	quotaTable := system.schema + ".issue78_quota_reservations"
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s
		SET generation=generation+1 WHERE quota_reservation_id=$1`, quotaTable),
		work.start.ProviderBinding.QuotaReservationID.String()); err != nil {
		t.Fatal(err)
	}
	ref := runtimeexecution.RuntimeRunRef{
		SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		Authority: work.start.Authority,
	}
	stale, err := providerRuntime.Inspect(context.Background(), ref)
	if err != nil || stale.Gateway.Status != runtimeexecution.GatewayGrantStale || stale.Gateway.Ready ||
		stale.Readiness.LLMGateway.State != runtimeexecution.PrerequisitePending || stale.Readiness.CapsuleReady {
		t.Fatalf("PostgreSQL Inspect projected stale Reservation as ready: snapshot=%+v err=%v", stale, err)
	}
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s
		SET generation=4 WHERE quota_reservation_id=$1`, quotaTable),
		work.start.ProviderBinding.QuotaReservationID.String()); err != nil {
		t.Fatal(err)
	}
	restored, err := providerRuntime.Inspect(context.Background(), ref)
	if err != nil || restored.Gateway.Status != runtimeexecution.GatewayGrantCurrent || !restored.Gateway.Ready ||
		!restored.Readiness.CapsuleReady {
		t.Fatalf("PostgreSQL Inspect persisted stale projection: snapshot=%+v err=%v", restored, err)
	}
}

func TestPostgresGatewayLateReceiptPersistsWithoutReopeningTerminalRuntime(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 15, 0, 0, time.UTC)
	system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(t, now, "late-receipt")
	gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, nil)
	if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
		t.Fatal(err)
	}
	accepted, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatal(err)
	}
	call := postgresGatewayCallForSnapshot(t, "issue-78-call-late-receipt", accepted.Snapshot, work.start, now)
	callDecision, err := gateway.AcceptGatewayCall(context.Background(), call)
	if err != nil {
		t.Fatalf("accept Gateway Call: %v", err)
	}

	cancelOperationID, _ := runtimeexecution.NewOperationID("issue-78-cancel-late-receipt")
	cancel, err := runtimeexecution.NewCancelRuntimeRun(runtimeexecution.CancelRuntimeRunInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: cancelOperationID,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, TaskID: work.start.TaskID,
		PhaseRunID: work.start.PhaseRunID, RuntimeRunID: work.start.RuntimeRunID,
		ExpectedRuntimeRevision:     accepted.Snapshot.RuntimeRevision,
		ExpectedStartOperationID:    work.start.OperationID,
		ExpectedOperationGeneration: accepted.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        accepted.Snapshot.RuntimeFence, Authority: work.start.Authority,
		Reason: runtimeexecution.CancellationUserRequested, SafetyEpoch: work.start.ReleaseSafetyEpoch,
		OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := providerRuntime.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled {
		t.Fatalf("cancel provider Runtime: decision=%+v err=%v", cancelled, err)
	}
	if _, err := gateway.AcceptGatewayCall(context.Background(), postgresGatewayCallForSnapshot(
		t, "issue-78-call-after-cancel", accepted.Snapshot, work.start, now,
	)); runtimeExecutionErrorCode(err) != runtimeexecution.ErrorIntegrityConflict {
		t.Fatalf("new Call after cancel error = %v", err)
	}
	receiptID, _ := runtimeexecution.NewUsageReceiptID("issue-78-usage-receipt-late")
	settlement, err := runtimeexecution.NewGatewayAttemptSettlement(runtimeexecution.GatewayAttemptSettlementInput{
		GatewayAttemptID: callDecision.GatewayAttemptID, UsageReceiptID: receiptID,
		Disposition: runtimeexecution.UsageEvidenceKnown, ObservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := gateway.SettleGatewayAttempt(context.Background(), settlement)
	if err != nil {
		t.Fatalf("settle accepted Attempt after cancel: %v", err)
	}
	wantRoot, err := runtimeexecution.NewUsageReceiptReferenceSet([]runtimeexecution.UsageReceiptReference{reference})
	if err != nil {
		t.Fatal(err)
	}
	late, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil || late.Snapshot.State != runtimeexecution.RuntimeTerminal ||
		late.Snapshot.Outcome != runtimeexecution.RuntimeCancelled ||
		late.Snapshot.RuntimeRevision != cancelled.Snapshot.RuntimeRevision ||
		late.Snapshot.Usage.Disposition != runtimeexecution.UsageEvidenceKnown ||
		late.Snapshot.Usage.Receipts != wantRoot {
		t.Fatalf("late receipt changed terminal authority or was lost: snapshot=%+v err=%v", late.Snapshot, err)
	}
	var persistedReceipts int
	if err := system.db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT count(*) FROM %s.runtime_execution_usage_receipts
		WHERE runtime_run_id=$1`, system.schema), work.start.RuntimeRunID.String()).Scan(&persistedReceipts); err != nil || persistedReceipts != 1 {
		t.Fatalf("persisted typed Usage Receipt count = %d, err=%v", persistedReceipts, err)
	}
	restarted := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, nil)
	inspected, err := restarted.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		Authority: work.start.Authority,
	})
	if err != nil || inspected.Usage != late.Snapshot.Usage || inspected.Outcome != runtimeexecution.RuntimeCancelled {
		t.Fatalf("restart lost late Usage evidence: inspected=%+v err=%v", inspected, err)
	}
}

func TestPostgresGatewayRecoveryReadOnlyFencesNewCallsButAcceptedAttemptSettles(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 20, 0, 0, time.UTC)
	system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(t, now, "recovery-read-only")
	gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	recovery := runtimeexecution.GatewayRecoverySnapshot{
		Generation: 7, Mode: runtimeexecution.GatewayRecoveryWritable, ExpiresAt: now.Add(9 * time.Minute),
	}
	recoveryAuthority := runtimeexecution.GatewayRecoveryAuthorityFunc(func(context.Context) (
		runtimeexecution.GatewayRecoverySnapshot,
		error,
	) {
		return recovery, nil
	})
	providerRuntime := newPostgresProviderRuntime(
		t, system, quotaParticipant, quotaFunction, gateway, nil, recoveryAuthority,
	)
	if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
		t.Fatal(err)
	}
	accepted, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatal(err)
	}
	firstCall := postgresGatewayCallForSnapshot(t, "issue-78-call-before-recovery-read-only",
		accepted.Snapshot, work.start, now)
	firstDecision, err := gateway.AcceptGatewayCall(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	recovery.Generation++
	recovery.Mode = runtimeexecution.GatewayRecoveryDegradedReadOnly
	secondCall := postgresGatewayCallForSnapshot(t, "issue-78-call-after-recovery-read-only",
		accepted.Snapshot, work.start, now)
	if _, err := gateway.AcceptGatewayCall(context.Background(), secondCall); runtimeExecutionErrorCode(err) != runtimeexecution.ErrorIntegrityConflict {
		t.Fatalf("recovery read-only new Call error = %v", err)
	}
	receiptID, _ := runtimeexecution.NewUsageReceiptID("issue-78-usage-receipt-recovery-read-only")
	settlement, err := runtimeexecution.NewGatewayAttemptSettlement(runtimeexecution.GatewayAttemptSettlementInput{
		GatewayAttemptID: firstDecision.GatewayAttemptID, UsageReceiptID: receiptID,
		Disposition: runtimeexecution.UsageEvidenceKnown, ObservedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := gateway.SettleGatewayAttempt(context.Background(), settlement); err != nil ||
		receipt.Disposition != runtimeexecution.UsageEvidenceKnown {
		t.Fatalf("accepted Attempt did not settle after recovery read-only: receipt=%+v err=%v", receipt, err)
	}
}

func TestPostgresGatewayRefreshRetainsOriginalOperationAcrossAuthorityDrift(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 30, 0, 0, time.UTC)
	system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(t, now, "refresh-cas")
	gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingGatewayRefresh{
		delegate: gateway, entered: make(chan struct{}), release: make(chan struct{}),
	}
	providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, blocking, nil)
	if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
		t.Fatal(err)
	}
	initial, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil || initial.Snapshot.Gateway.CurrentGrant.Generation != 1 {
		t.Fatalf("initial Gateway grant: snapshot=%+v err=%v", initial.Snapshot.Gateway, err)
	}
	system.clock.Set(now.Add(45 * time.Second))
	type executeResult struct {
		decision runtimeexecution.RuntimeDecision
		err      error
	}
	staleResult := make(chan executeResult, 1)
	go func() {
		decision, executeErr := providerRuntime.Execute(context.Background(), work.start)
		staleResult <- executeResult{decision: decision, err: executeErr}
	}()
	<-blocking.entered
	newReservationExpiry := now.Add(70 * time.Second)
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.issue78_quota_reservations
		SET expires_at=$1 WHERE quota_reservation_id=$2`, system.schema),
		newReservationExpiry, work.start.ProviderBinding.QuotaReservationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := providerRuntime.Execute(context.Background(), work.start); runtimeExecutionErrorCode(err) != runtimeexecution.ErrorIntegrityConflict {
		close(blocking.release)
		t.Fatalf("authority drift replaced retained refresh: err=%v", err)
	}
	close(blocking.release)
	stale := <-staleResult
	if code := runtimeExecutionErrorCode(stale.err); code != runtimeexecution.ErrorIntegrityConflict {
		t.Fatalf("stale refresh response error = %v", stale.err)
	}
	inspected, err := providerRuntime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		Authority: work.start.Authority,
	})
	if err != nil || inspected.Gateway.Status != runtimeexecution.GatewayGrantPending ||
		inspected.Gateway.Ready || inspected.Gateway.CurrentGrant.Generation != 1 {
		t.Fatalf("authority drift or stale response replaced current generation: inspected=%+v err=%v",
			inspected.Gateway, err)
	}
	var outboxCount, acceptanceCount, currentGeneration int
	if err := system.db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s.runtime_execution_gateway_outbox WHERE runtime_run_id=$1),
		(SELECT count(*) FROM %s.runtime_execution_gateway_grant_acceptances WHERE runtime_run_id=$1),
		(SELECT grant_generation FROM %s.runtime_execution_gateway_current WHERE runtime_run_id=$1)`,
		system.schema, system.schema, system.schema), work.start.RuntimeRunID.String()).Scan(
		&outboxCount, &acceptanceCount, &currentGeneration); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 2 || acceptanceCount != 1 || currentGeneration != 1 {
		t.Fatalf("refresh persistence outbox/acceptance/current = %d/%d/%d", outboxCount, acceptanceCount, currentGeneration)
	}
}

func TestPostgresGatewayRequestAndAcceptanceFaultsConvergeByReplay(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		point runtimeexecution.PersistenceFaultPoint
		want  runtimeexecution.ErrorCode
	}{
		{name: "before request commit", point: runtimeexecution.PersistenceFaultBeforeGatewayRequestCommit,
			want: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after request commit", point: runtimeexecution.PersistenceFaultAfterGatewayRequestCommit,
			want: runtimeexecution.ErrorReconciliationRequired},
		{name: "before acceptance commit", point: runtimeexecution.PersistenceFaultBeforeGatewayAcceptanceCommit,
			want: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after acceptance commit", point: runtimeexecution.PersistenceFaultAfterGatewayAcceptanceCommit,
			want: runtimeexecution.ErrorReconciliationRequired},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 23, 40, 0, 0, time.UTC)
			system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(
				t, now, "fault-"+issue76Slug(testCase.name),
			)
			gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			faults := &runtimeexecution.PersistenceFaultController{}
			if err := faults.FailNextAt(testCase.point); err != nil {
				t.Fatal(err)
			}
			providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, faults)
			if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
				t.Fatal(err)
			}
			_, err = providerRuntime.Execute(context.Background(), work.start)
			if code := runtimeExecutionErrorCode(err); code != testCase.want {
				t.Fatalf("fault error = %v, want code %v", err, testCase.want)
			}
			replayed, err := providerRuntime.Execute(context.Background(), work.start)
			if err != nil || replayed.Snapshot.Gateway.Status != runtimeexecution.GatewayGrantCurrent ||
				replayed.Snapshot.Gateway.CurrentGrant.Generation != 1 {
				t.Fatalf("fault replay did not converge: snapshot=%+v err=%v", replayed.Snapshot.Gateway, err)
			}
			var outboxCount, acceptanceCount, currentGeneration int
			if err := system.db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT
				(SELECT count(*) FROM %s.runtime_execution_gateway_outbox WHERE runtime_run_id=$1),
				(SELECT count(*) FROM %s.runtime_execution_gateway_grant_acceptances WHERE runtime_run_id=$1),
				(SELECT grant_generation FROM %s.runtime_execution_gateway_current WHERE runtime_run_id=$1)`,
				system.schema, system.schema, system.schema), work.start.RuntimeRunID.String()).Scan(
				&outboxCount, &acceptanceCount, &currentGeneration); err != nil {
				t.Fatal(err)
			}
			if outboxCount != 1 || acceptanceCount != 1 || currentGeneration != 1 {
				t.Fatalf("fault replay persistence = %d/%d/%d", outboxCount, acceptanceCount, currentGeneration)
			}
		})
	}
}

func TestPostgresUsageEvidenceFaultsConvergeWithoutTerminalMutation(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		point     runtimeexecution.PersistenceFaultPoint
		want      runtimeexecution.ErrorCode
		committed bool
	}{
		{name: "before evidence commit", point: runtimeexecution.PersistenceFaultBeforeUsageEvidenceCommit,
			want: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after evidence commit", point: runtimeexecution.PersistenceFaultAfterUsageEvidenceCommit,
			want: runtimeexecution.ErrorReconciliationRequired, committed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 23, 50, 0, 0, time.UTC)
			system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(
				t, now, "usage-fault-"+issue76Slug(testCase.name),
			)
			gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, nil)
			if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
				t.Fatal(err)
			}
			accepted, err := providerRuntime.Execute(context.Background(), work.start)
			if err != nil {
				t.Fatal(err)
			}
			call := postgresGatewayCallForSnapshot(t, "issue-78-call-"+issue76Slug(testCase.name),
				accepted.Snapshot, work.start, now)
			callDecision, err := gateway.AcceptGatewayCall(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			receiptID, _ := runtimeexecution.NewUsageReceiptID("issue-78-receipt-" + issue76Slug(testCase.name))
			settlement, _ := runtimeexecution.NewGatewayAttemptSettlement(runtimeexecution.GatewayAttemptSettlementInput{
				GatewayAttemptID: callDecision.GatewayAttemptID, UsageReceiptID: receiptID,
				Disposition: runtimeexecution.UsageEvidenceEstimated, ObservedAt: now,
			})
			reference, err := gateway.SettleGatewayAttempt(context.Background(), settlement)
			if err != nil {
				t.Fatal(err)
			}
			wantRoot, _ := runtimeexecution.NewUsageReceiptReferenceSet([]runtimeexecution.UsageReceiptReference{reference})
			faults := &runtimeexecution.PersistenceFaultController{}
			if err := faults.FailNextAt(testCase.point); err != nil {
				t.Fatal(err)
			}
			faulted := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, faults)
			_, err = faulted.Execute(context.Background(), work.start)
			if code := runtimeExecutionErrorCode(err); code != testCase.want {
				t.Fatalf("Usage evidence fault error = %v, want %v", err, testCase.want)
			}
			afterFault, err := faulted.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
				SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
				PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
				Authority: work.start.Authority,
			})
			if err != nil {
				t.Fatal(err)
			}
			if testCase.committed {
				if afterFault.Usage.Disposition != runtimeexecution.UsageEvidenceEstimated ||
					afterFault.Usage.Receipts != wantRoot {
					t.Fatalf("post-commit evidence fault lost evidence: %+v", afterFault.Usage)
				}
			} else if afterFault.Usage.Disposition != runtimeexecution.UsageEvidenceMissing ||
				afterFault.Usage.Receipts.Count != 0 {
				t.Fatalf("pre-commit evidence fault left partial evidence: %+v", afterFault.Usage)
			}
			replayed, err := faulted.Execute(context.Background(), work.start)
			if err != nil || replayed.Snapshot.Usage.Disposition != runtimeexecution.UsageEvidenceEstimated ||
				replayed.Snapshot.Usage.Receipts != wantRoot ||
				replayed.Snapshot.RuntimeRevision != accepted.Snapshot.RuntimeRevision {
				t.Fatalf("Usage evidence replay did not converge: snapshot=%+v err=%v", replayed.Snapshot, err)
			}
		})
	}
}

func TestPostgresGatewayAndUsageAuthorityFactsAreImmutable(t *testing.T) {
	now := time.Date(2026, time.July, 29, 23, 55, 0, 0, time.UTC)
	system, work, quotaParticipant, quotaFunction := newPostgresProviderGatewayFixture(t, now, "immutable-facts")
	gateway, err := runtimeexecution.NewDeterministicGateway(system.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	providerRuntime := newPostgresProviderRuntime(t, system, quotaParticipant, quotaFunction, gateway, nil)
	if err := gateway.BindRuntimeAuthority(providerRuntime); err != nil {
		t.Fatal(err)
	}
	accepted, err := providerRuntime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatal(err)
	}
	call := postgresGatewayCallForSnapshot(t, "issue-78-call-immutable-facts", accepted.Snapshot, work.start, now)
	callDecision, err := gateway.AcceptGatewayCall(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	receiptID, _ := runtimeexecution.NewUsageReceiptID("issue-78-receipt-immutable-facts")
	settlement, _ := runtimeexecution.NewGatewayAttemptSettlement(runtimeexecution.GatewayAttemptSettlementInput{
		GatewayAttemptID: callDecision.GatewayAttemptID, UsageReceiptID: receiptID,
		Disposition: runtimeexecution.UsageEvidenceKnown, ObservedAt: now,
	})
	if _, err := gateway.SettleGatewayAttempt(context.Background(), settlement); err != nil {
		t.Fatal(err)
	}
	if _, err := providerRuntime.Execute(context.Background(), work.start); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		fmt.Sprintf(`UPDATE %s.runtime_execution_gateway_outbox SET created_at=created_at`, system.schema),
		fmt.Sprintf(`UPDATE %s.runtime_execution_gateway_grant_acceptances SET accepted_at=accepted_at`, system.schema),
		fmt.Sprintf(`UPDATE %s.runtime_execution_usage_receipts SET recorded_at=recorded_at`, system.schema),
		fmt.Sprintf(`UPDATE %s.runtime_execution_usage_evidence_roots SET recorded_at=recorded_at`, system.schema),
	} {
		if _, err := system.db.ExecContext(context.Background(), mutation); err == nil {
			t.Fatalf("immutable authority mutation succeeded: %s", mutation)
		}
	}
	inspected, err := providerRuntime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		Authority: work.start.Authority,
	})
	if err != nil || inspected.Gateway.Status != runtimeexecution.GatewayGrantCurrent ||
		inspected.Usage.Disposition != runtimeexecution.UsageEvidenceKnown {
		t.Fatalf("immutable fact probes changed authority: snapshot=%+v err=%v", inspected, err)
	}
}

func postgresGatewayCallForSnapshot(
	t *testing.T,
	value string,
	snapshot runtimeexecution.RuntimeSnapshot,
	start runtimeexecution.StartRuntimeRun,
	acceptedAt time.Time,
) runtimeexecution.GatewayCallRequest {
	t.Helper()
	callID, _ := runtimeexecution.NewGatewayCallID(value)
	grant := snapshot.Gateway.CurrentGrant
	request, err := runtimeexecution.NewGatewayCallRequest(runtimeexecution.GatewayCallRequestInput{
		GatewayCallID: callID, RuntimeRunID: start.RuntimeRunID, StartOperationID: start.OperationID,
		GatewayGrantID: grant.GatewayGrantID, GatewayGrantGeneration: grant.Generation,
		LeaseID: grant.LeaseID, LeaseGeneration: grant.LeaseGeneration, LeaseFence: grant.LeaseFence,
		RuntimeFence: grant.RuntimeFence, QuotaReservationID: grant.QuotaReservationID,
		QuotaReservationGeneration: grant.QuotaReservationGeneration, QuotaReservationMode: grant.QuotaReservationMode,
		GatewayRoutePolicyID:         grant.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: grant.GatewayRoutePolicyGeneration,
		Capability:                   runtimeexecution.ProviderScopeTextGeneration, AcceptedAt: acceptedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func runtimeExecutionErrorCode(err error) runtimeexecution.ErrorCode {
	var typed *runtimeexecution.Error
	if errors.As(err, &typed) {
		return typed.Code()
	}
	return 0
}

func newPostgresProviderRuntime(
	t *testing.T,
	system *postgresRuntimeAdmissionSystem,
	quotaParticipant runtimeexecution.QuotaReservationParticipant,
	quotaFunction string,
	gateway runtimeexecution.GatewayGrantAdapter,
	faults runtimeexecution.PersistenceFaultInjector,
	gatewayRecovery ...runtimeexecution.GatewayRecoveryAuthority,
) *runtimeexecution.PostgresAuthority {
	t.Helper()
	inputEvidenceID, err := runtimeexecution.NewEvidenceID("issue-78-immutable-input-evidence")
	if err != nil {
		t.Fatal(err)
	}
	recoveryExpiresAt := system.clock.Now().Add(9 * time.Minute)
	recoveryAuthority := runtimeexecution.GatewayRecoveryAuthority(runtimeexecution.GatewayRecoveryAuthorityFunc(func(context.Context) (
		runtimeexecution.GatewayRecoverySnapshot,
		error,
	) {
		return runtimeexecution.GatewayRecoverySnapshot{
			Generation: 7, Mode: runtimeexecution.GatewayRecoveryWritable,
			ExpiresAt: recoveryExpiresAt,
		}, nil
	}))
	if len(gatewayRecovery) > 1 {
		t.Fatal("at most one Gateway recovery authority is supported")
	}
	if len(gatewayRecovery) == 1 {
		recoveryAuthority = gatewayRecovery[0]
	}
	callAuthority, ok := recoveryAuthority.(runtimeexecution.GatewayCallExternalAuthority)
	if !ok {
		callAuthority = runtimeexecution.GatewayCallExternalAuthorityFunc(func(
			ctx context.Context,
			fact runtimeexecution.GatewayCallExternalAuthorityFact,
			accept runtimeexecution.GatewayCallAcceptance,
		) error {
			current, inspectErr := recoveryAuthority.InspectGatewayRecovery(ctx)
			if inspectErr != nil || current.Generation != fact.RecoveryGeneration || current.Mode != fact.RecoveryMode ||
				fact.GatewayRoutePolicyID.String() == "" || fact.GatewayRoutePolicyGeneration == 0 ||
				fact.CapabilityScope == 0 || !fact.GrantExpiresAt.After(fact.ValidAt) ||
				fact.GrantExpiresAt.After(current.ExpiresAt) {
				return errors.New("Gateway Call external authority conflict")
			}
			return accept()
		})
	}
	authority, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
		Schema: system.schema, Now: system.clock.Now, Faults: faults,
		SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
		SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
		SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
		SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
		LeaseAcquisition:                    system.lease,
		RuntimeBindingValidator:             acceptedRuntimeBindingValidatorForTaskOrchestrationTest(t),
		ImmutableInputValidator: runtimeexecution.ImmutableInputValidatorFunc(func(
			context.Context,
			runtimeexecution.ImmutableInputValidationRequest,
		) (runtimeexecution.PrerequisiteObservation, error) {
			return runtimeexecution.PrerequisiteObservation{
				Disposition: runtimeexecution.PrerequisiteObservationAccepted,
				EvidenceID:  inputEvidenceID, EvidenceDigest: runtimeexecution.Digest{31: 78},
			}, nil
		}),
		QuotaReservationParticipant: quotaParticipant,
		QuotaReservationFunction:    quotaFunction,
		GatewayGrants:               gateway,
		GatewayRecovery:             recoveryAuthority,
		GatewayCallAuthority:        callAuthority,
		GatewayGrantLifetime:        time.Minute,
	})
	if err != nil {
		t.Fatalf("create provider Runtime authority: %v", err)
	}
	if err := authority.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate provider Runtime authority: %v", err)
	}
	return authority
}

func newPostgresProviderGatewayFixture(
	t *testing.T,
	now time.Time,
	suffix string,
) (*postgresRuntimeAdmissionSystem, admittedRuntimeWork, runtimeexecution.QuotaReservationParticipant, string) {
	t.Helper()
	ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
	work := system.enqueueAndAdmitRuntime(t, "issue-78-provider-"+suffix)
	reservationID, _ := runtimeexecution.NewQuotaReservationID("issue-78-reservation-" + suffix)
	routePolicyID, _ := runtimeexecution.NewGatewayRoutePolicyID("issue-78-route-policy-" + suffix)
	input := work.start.StartRuntimeRunInput
	input.ProviderCapability = runtimeexecution.ProviderCapabilityRequired
	input.ProviderBinding = &runtimeexecution.ProviderExecutionBinding{
		QuotaReservationID: reservationID, Generation: 4,
		Mode: runtimeexecution.QuotaReservationObservation, GatewayRoutePolicyID: routePolicyID,
		GatewayRoutePolicyGeneration: 3, CapabilityScope: runtimeexecution.ProviderScopeTextGeneration,
		RoutePolicyExpiresAt: now.Add(8 * time.Minute),
	}
	providerStart, err := runtimeexecution.NewStartRuntimeRun(input)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := runtimeexecution.CanonicalStartPayload(providerStart)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.scheduler_work_items
		SET payload_digest=$1, canonical_payload=$2 WHERE work_item_id=$3`, system.schema),
		providerStart.CanonicalRequestDigest[:], canonical, work.canonical.WorkItemID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.scheduler_admission_grants
		SET payload_digest=$1 WHERE admission_grant_id=$2`, system.schema),
		providerStart.CanonicalRequestDigest[:], work.canonical.Grant.AdmissionGrantID.String()); err != nil {
		t.Fatal(err)
	}
	work.start = providerStart

	quotaTable := system.schema + ".issue78_quota_reservations"
	quotaFunction := system.schema + ".issue78_validate_quota_reservation"
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`CREATE TABLE %s (
		quota_reservation_id text PRIMARY KEY, generation bigint NOT NULL, mode smallint NOT NULL,
		state smallint NOT NULL, personal_workspace_id text NOT NULL, task_id text NOT NULL,
		phase_run_id text NOT NULL, authorization_generation bigint NOT NULL,
		capability smallint NOT NULL, gateway_route_policy_id text NOT NULL,
		gateway_route_policy_generation bigint NOT NULL, capability_scope bigint NOT NULL,
		valid_from timestamptz NOT NULL, expires_at timestamptz NOT NULL
	)`, quotaTable)); err != nil {
		t.Fatal(err)
	}
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`CREATE FUNCTION %s(
		p_id text, p_generation bigint, p_mode smallint, p_workspace text, p_task text,
		p_phase text, p_authorization_generation bigint, p_capability smallint,
		p_route_policy_id text, p_route_policy_generation bigint,
		p_capability_scope bigint, p_valid_at timestamptz
	) RETURNS timestamptz LANGUAGE plpgsql AS $quota$
	DECLARE retained %s%%ROWTYPE;
	BEGIN
		SELECT * INTO retained FROM %s WHERE quota_reservation_id=p_id FOR SHARE;
		IF retained.quota_reservation_id IS NULL OR retained.generation <> p_generation OR
			retained.mode <> p_mode OR retained.state <> 1 OR retained.personal_workspace_id <> p_workspace OR
			retained.task_id <> p_task OR retained.phase_run_id <> p_phase OR
			retained.authorization_generation <> p_authorization_generation OR retained.capability <> p_capability OR
			retained.gateway_route_policy_id <> p_route_policy_id OR
			retained.gateway_route_policy_generation <> p_route_policy_generation OR
			retained.capability_scope <> p_capability_scope OR retained.valid_from > p_valid_at OR
			retained.expires_at <= p_valid_at THEN
			RAISE EXCEPTION 'quota reservation binding conflict' USING ERRCODE = '23000';
		END IF;
		RETURN retained.expires_at;
	END $quota$`, quotaFunction, quotaTable, quotaTable)); err != nil {
		t.Fatal(err)
	}
	if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`INSERT INTO %s
		VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, quotaTable),
		reservationID.String(), 4, runtimeexecution.QuotaReservationObservation,
		providerStart.PersonalWorkspaceID.String(), providerStart.TaskID.String(), providerStart.PhaseRunID.String(),
		providerStart.Authority.AuthorizationGeneration(), runtimeexecution.ProviderCapabilityRequired,
		routePolicyID.String(), uint64(3), runtimeexecution.ProviderScopeTextGeneration,
		now.Add(-time.Minute), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	participant := runtimeexecution.QuotaReservationParticipantFunc(func(
		ctx context.Context,
		transaction runtimeexecution.QuotaReservationValidationTransaction,
		_ runtimeexecution.QuotaReservationValidationFact,
	) (runtimeexecution.QuotaReservationValidationResult, error) {
		return transaction.ValidateQuotaReservation(ctx)
	})
	return system, work, participant, quotaFunction
}

var _ runtimeexecution.GatewayGrantAdapter = (*postgresGatewayProbe)(nil)
var _ runtimeexecution.UsageReceiptEvidenceSource = (*postgresGatewayProbe)(nil)
var _ runtimeexecution.GatewayGrantAdapter = (*blockingGatewayRefresh)(nil)
var _ runtimeexecution.UsageReceiptEvidenceSource = (*blockingGatewayRefresh)(nil)
