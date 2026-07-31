package runtimeexecution

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestPostgresWorkerProtocolContextFailuresAreDependencyUnavailable(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 50, 0, 0, time.UTC)
	_, _, store, _, _, _, _ := newPostgresWorkerProtocolHarness(t, "worker_context", now, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, test := range []struct {
		name string
		call func(context.Context) error
	}{
		{name: "heartbeat", call: func(ctx context.Context) error {
			_, err := store.heartbeat(ctx, workerHeartbeat{})
			return err
		}},
		{name: "observe", call: func(ctx context.Context) error {
			_, err := store.observe(ctx, workerObserve{})
			return err
		}},
		{name: "stop", call: func(ctx context.Context) error {
			_, err := store.stop(ctx, workerStopIntent{})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, ctx := range []context.Context{nil, cancelled} {
				if err := test.call(ctx); errorCode(err) != ErrorDependencyUnavailable {
					t.Fatalf("context failure code=%v err=%v, want %v", errorCode(err), err, ErrorDependencyUnavailable)
				}
			}
		})
	}
}

func TestPostgresWorkerHeartbeatNormalizesRuntimeLoadFailureSeparatelyFromStaleState(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 55, 0, 0, time.UTC)
	db, schema, store, _, start, command, _ := newPostgresWorkerProtocolHarness(
		t, "worker_hb_load", now, nil,
	)
	if _, err := store.accept(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	current, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "worker-heartbeat-load-failure-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		StartOperationID: start.OperationID, CapsuleID: current.Capsule.CapsuleID,
		CapsuleDigest: current.Capsule.Digest, RuntimeFence: current.RuntimeFence,
		Lease: current.Lease, Node: current.Node, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: startCatalogSafetyEpoch(start),
		RequestedExpiresAt: current.Lease.ExpiresAt.Add(time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := db.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(context.Background(), `SELECT 1 FROM `+schema+`.runtime_execution_runtimes
		WHERE runtime_run_id=$1 FOR UPDATE`, start.RuntimeRunID.String()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err = store.heartbeat(ctx, heartbeat)
	if errorCode(err) != ErrorDependencyUnavailable {
		t.Fatalf("runtime load failure code=%v err=%v, want dependency unavailable", errorCode(err), err)
	}
}

func TestPostgresWorkerObserveResamplesTimeAndAuthorityAfterBackendCall(t *testing.T) {
	for _, test := range []struct {
		name         string
		prefix       string
		advanceTo    func(StartRuntimeRun, time.Time) time.Time
		observedAt   func(StartRuntimeRun, time.Time) time.Time
		wantCode     ErrorCode
		wantAccepted bool
	}{
		{
			name: "observation produced during call", prefix: "worker_obs_during",
			advanceTo:    func(_ StartRuntimeRun, now time.Time) time.Time { return now.Add(time.Second) },
			observedAt:   func(_ StartRuntimeRun, now time.Time) time.Time { return now.Add(time.Second) },
			wantAccepted: true,
		},
		{
			name: "deadline expires during call", prefix: "worker_obs_expired",
			advanceTo:  func(start StartRuntimeRun, _ time.Time) time.Time { return start.Deadline },
			observedAt: func(_ StartRuntimeRun, now time.Time) time.Time { return now },
			wantCode:   ErrorAuthorizationDenied,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 31, 12, 57, 0, 0, time.UTC)
			db, _, store, config, start, command, control := newPostgresWorkerProtocolHarness(
				t, test.prefix, now, nil,
			)
			backend := control.(*contractToolWorkerBackend)
			if _, err := store.accept(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			accepted, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			request, err := newWorkerObserve(
				workerOperationRefFromSnapshot(start, accepted), initialWorkerCursor(accepted),
			)
			if err != nil {
				t.Fatal(err)
			}
			currentTime := now
			config.Now = func() time.Time { return currentTime }
			restarted, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			backend.enqueueObservation(contractWorkerObservation{
				Kind: WorkerObservedRunning, ObservedAt: test.observedAt(start, now),
			})
			backend.beforeNextObserve(func() { currentTime = test.advanceTo(start, now) })
			result, err := restarted.observe(context.Background(), request)
			if test.wantAccepted {
				if err != nil || result.Disposition != WorkerObservationAccepted {
					t.Fatalf("observation produced during call: %+v err=%v", result, err)
				}
				return
			}
			if errorCode(err) != test.wantCode {
				t.Fatalf("post-call authority code=%v err=%v, want %v", errorCode(err), err, test.wantCode)
			}
			inspected, inspectErr := restarted.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if inspectErr != nil || inspected.Worker.Status != WorkerOperationAccepted {
				t.Fatalf("expired observation mutated projection: %+v err=%v", inspected.Worker, inspectErr)
			}
		})
	}
}

func TestPostgresWorkerAcceptIsAtomicAcrossFaultsAndResponseLoss(t *testing.T) {
	for _, test := range []struct {
		name       string
		prefix     string
		fault      PersistenceFaultPoint
		wantStored int
		wantCode   ErrorCode
	}{
		{name: "before commit", prefix: "worker_accept_pre", fault: PersistenceFaultBeforeWorkerAcceptCommit, wantStored: 0, wantCode: ErrorDependencyUnavailable},
		{name: "after commit", prefix: "worker_accept_post", fault: PersistenceFaultAfterWorkerAcceptCommit, wantStored: 1, wantCode: ErrorReconciliationRequired},
		{name: "before response", prefix: "worker_accept_resp", fault: PersistenceFaultBeforeWorkerAcceptResponse, wantStored: 1, wantCode: ErrorReconciliationRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 31, 13, 0, 0, 0, time.UTC)
			faults := &PersistenceFaultController{}
			db, schema, store, config, start, command, backend := newPostgresWorkerProtocolHarness(
				t, test.prefix, now, faults,
			)
			if err := faults.FailNextAt(test.fault); err != nil {
				t.Fatal(err)
			}
			_, err := store.accept(context.Background(), command)
			if errorCode(err) != test.wantCode {
				t.Fatalf("fault error=%v code=%v want=%v", err, errorCode(err), test.wantCode)
			}
			var acceptances int
			var disposition DispatchDisposition
			var ackLength int
			if err := db.QueryRowContext(context.Background(), `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_worker_acceptances),
				disposition, COALESCE(octet_length(ack_digest),0)
				FROM `+schema+`.runtime_execution_dispatch_delivery WHERE operation_id=$1`,
				command.OperationID.String()).Scan(&acceptances, &disposition, &ackLength); err != nil {
				t.Fatal(err)
			}
			if acceptances != test.wantStored {
				t.Fatalf("accept journal count=%d want=%d", acceptances, test.wantStored)
			}
			if test.wantStored == 0 && (disposition != DispatchClaimed || ackLength != 0) {
				t.Fatalf("pre-commit fault left dispatch partial: disposition=%v ack length=%d", disposition, ackLength)
			}
			if test.wantStored == 1 && (disposition != DispatchAcknowledged || ackLength != 32) {
				t.Fatalf("post-commit response loss did not atomically acknowledge: disposition=%v ack length=%d", disposition, ackLength)
			}

			config.Faults = nil
			restarted, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := restarted.accept(context.Background(), command)
			if err != nil || ack != newWorkerOperationAck(command) {
				t.Fatalf("replay exact Accept after fault: %+v err=%v", ack, err)
			}
			wantBackendCalls := 2
			if test.wantStored == 1 {
				wantBackendCalls = 1
			}
			if backend.acceptCount() != wantBackendCalls {
				t.Fatalf("worker backend deliveries=%d want=%d", backend.acceptCount(), wantBackendCalls)
			}
			inspected, err := restarted.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil || inspected.State != RuntimeStarting || inspected.Worker.Status != WorkerOperationAccepted {
				t.Fatalf("restarted accepted projection: %+v err=%v", inspected, err)
			}
		})
	}
}

func TestPostgresConcurrentDuplicateWorkerAcceptDispatchesBackendOnce(t *testing.T) {
	now := time.Date(2026, time.July, 31, 13, 20, 0, 0, time.UTC)
	_, _, store, _, _, command, backend := newPostgresWorkerProtocolHarness(t, "worker_accept_race", now, nil)
	const callers = 8
	acks := make(chan workerOperationAck, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ack, err := store.accept(context.Background(), command)
			acks <- ack
			errs <- err
		}()
	}
	wait.Wait()
	close(acks)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Accept: %v", err)
		}
	}
	want := newWorkerOperationAck(command)
	for ack := range acks {
		if ack != want {
			t.Fatalf("concurrent Ack=%+v want=%+v", ack, want)
		}
	}
	if backend.acceptCount() != 1 {
		t.Fatalf("concurrent duplicate reached worker backend %d times", backend.acceptCount())
	}
}

func TestPostgresWorkerObservationPersistsCursorReplayOrderingAndConflictsAcrossRestart(t *testing.T) {
	now := time.Date(2026, time.July, 31, 13, 40, 0, 0, time.UTC)
	faults := &PersistenceFaultController{}
	db, schema, store, config, start, command, backend := newPostgresWorkerProtocolHarness(
		t, "worker_observe_pg", now, faults,
	)
	if _, err := store.accept(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	starting, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	request, err := newWorkerObserve(workerOperationRefFromSnapshot(start, starting), initialWorkerCursor(starting))
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	if err := faults.FailNextAt(PersistenceFaultBeforeWorkerObservationCommit); err != nil {
		t.Fatal(err)
	}
	_, err = store.observe(context.Background(), request)
	if errorCode(err) != ErrorDependencyUnavailable {
		t.Fatalf("pre-commit observation fault=%v", err)
	}
	var observations int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_worker_observations`).Scan(&observations); err != nil || observations != 0 {
		t.Fatalf("pre-commit observation journal=%d err=%v", observations, err)
	}
	afterRollback, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || afterRollback.Worker.Cursor != (WorkerCursorSnapshot{}) {
		t.Fatalf("pre-commit observation advanced cursor: %+v err=%v", afterRollback.Worker, err)
	}

	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	first, err := store.observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || running.Worker.Cursor.Position != 1 {
		t.Fatalf("running cursor: %+v err=%v", running.Worker.Cursor, err)
	}

	gapRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, running), running.Worker.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		ObservationID: first.Observation.ObservationID, Kind: first.Observation.Kind,
		StreamGeneration: first.Observation.StreamGeneration, Position: first.Observation.Position,
		ObservedAt: first.Observation.ObservedAt,
	})
	exactRedelivery, err := store.observe(context.Background(), gapRequest)
	if err != nil || !exactRedelivery.Replayed || exactRedelivery.Disposition != WorkerObservationAccepted ||
		exactRedelivery.Observation != first.Observation {
		t.Fatalf("PostgreSQL exact observation redelivery: %+v err=%v first=%+v", exactRedelivery, err, first)
	}
	afterExactRedelivery, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || afterExactRedelivery.RuntimeRevision != running.RuntimeRevision ||
		afterExactRedelivery.Worker != running.Worker {
		t.Fatalf("PostgreSQL exact redelivery mutated projection: %+v err=%v", afterExactRedelivery, err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		ObservationID: first.Observation.ObservationID, Kind: WorkerObservedRunning, ObservedAt: now,
	})
	_, err = store.observe(context.Background(), gapRequest)
	if errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("same running observation identity with different digest=%v", err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		Kind: WorkerObservedRunning, StreamGeneration: running.Worker.Cursor.StreamGeneration,
		Position: running.Worker.Cursor.Position + 2, ObservedAt: now,
	})
	deferred, err := store.observe(context.Background(), gapRequest)
	if err != nil || deferred.Disposition != WorkerObservationDeferred {
		t.Fatalf("cursor gap=%+v err=%v", deferred, err)
	}
	afterGap, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || afterGap.Worker.Cursor != running.Worker.Cursor {
		t.Fatalf("cursor gap advanced projection: %+v err=%v", afterGap.Worker.Cursor, err)
	}

	successID := WorkerObservationID{value: "worker-observation-postgres-success"}
	backend.enqueueObservation(contractWorkerObservation{
		ObservationID: successID, Kind: WorkerObservedSucceeded, ObservedAt: now,
		EvidenceID:     mustEvidenceID(t, "postgres-worker-success-evidence"),
		EvidenceDigest: digest(247), InternalCallCount: 5,
	})
	if err := faults.FailNextAt(PersistenceFaultAfterWorkerObservationCommit); err != nil {
		t.Fatal(err)
	}
	_, err = store.observe(context.Background(), gapRequest)
	if errorCode(err) != ErrorReconciliationRequired {
		t.Fatalf("post-commit observation response loss=%v", err)
	}
	config.Faults = nil
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.observe(context.Background(), gapRequest)
	if err != nil || !replayed.Replayed || replayed.Observation.ObservationID != successID {
		t.Fatalf("restart observation replay: %+v err=%v", replayed, err)
	}
	restartedSnapshot, err := restarted.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || restartedSnapshot.Worker.Cursor.Position != 2 ||
		restartedSnapshot.Worker.EvidenceCandidate.InternalCallCount != 5 || restartedSnapshot.Outcome != RuntimeOutcomeNone {
		t.Fatalf("restart cursor/evidence projection: %+v err=%v", restartedSnapshot, err)
	}

	conflictRequest, err := newWorkerObserve(
		workerOperationRefFromSnapshot(start, restartedSnapshot), restartedSnapshot.Worker.Cursor,
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		ObservationID: successID, Kind: WorkerObservedFailed, ObservedAt: now,
		EvidenceID:     mustEvidenceID(t, "postgres-worker-conflicting-evidence"),
		EvidenceDigest: digest(248), InternalCallCount: 1, SafeFailure: WorkerFailureAmbiguous,
	})
	beforeTerminalReplacement := backend.observeCount()
	_, err = restarted.observe(context.Background(), conflictRequest)
	if errorCode(err) != ErrorAuthorizationDenied || backend.observeCount() != beforeTerminalReplacement {
		t.Fatalf("terminal observation replacement reached backend: err=%v before=%d after=%d",
			err, beforeTerminalReplacement, backend.observeCount())
	}
	if first.Observation.Position != 1 {
		t.Fatalf("first observation changed after restart: %+v", first)
	}
}

func TestPostgresWorkerHeartbeatAndLeaseRevokeRacePreservesSingleCurrentFence(t *testing.T) {
	now := time.Date(2026, time.July, 31, 14, 0, 0, 0, time.UTC)
	db, _, store, config, start, command, backend := newPostgresWorkerProtocolHarness(t, "worker_heartbeat_pg", now, nil)
	if _, err := store.accept(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	current, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-worker-heartbeat"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		StartOperationID: start.OperationID, CapsuleID: current.Capsule.CapsuleID,
		CapsuleDigest: current.Capsule.Digest, RuntimeFence: current.RuntimeFence,
		Lease: current.Lease, Node: current.Node, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: startCatalogSafetyEpoch(start),
		RequestedExpiresAt: current.Lease.ExpiresAt.Add(time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fencingAuthority := NewSecurityLeaseFencingAuthority(mustAuthorityID(t, "postgres-worker-fencing"), 3)
	config.MaintenanceAuthorities = append(config.MaintenanceAuthorities,
		BindLeaseFencingAuthority(current.Node.ExecutionNodeID, fencingAuthority))
	blocker := &blockingPersistenceFault{
		point: PersistenceFaultBeforeCommit, entered: make(chan struct{}), release: make(chan struct{}),
	}
	config.Faults = blocker
	store, err = NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	fence, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-worker-race-fence"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeFence: current.RuntimeFence, SandboxLeaseID: current.Lease.LeaseID,
		LeaseGeneration: current.Lease.Generation, LeaseFence: current.Lease.Fence,
		ExecutionNodeID: current.Node.ExecutionNodeID, NodeGeneration: current.Node.Generation,
		Reason: LeaseFenceRevoked, Authority: fencingAuthority,
		ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	type heartbeatRaceResult struct {
		decision workerLeaseDecision
		err      error
	}
	heartbeatDone := make(chan heartbeatRaceResult, 1)
	go func() {
		decision, err := store.heartbeat(context.Background(), heartbeat)
		heartbeatDone <- heartbeatRaceResult{decision: decision, err: err}
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not reach commit barrier")
	}
	fenceDone := make(chan error, 1)
	go func() {
		_, err := store.Maintain(context.Background(), fence)
		fenceDone <- err
	}()
	close(blocker.release)
	heartbeatOutcome := <-heartbeatDone
	if heartbeatOutcome.err != nil || heartbeatOutcome.decision.Lease.Generation != current.Lease.Generation+1 ||
		heartbeatOutcome.decision.Lease.Fence != current.Lease.Fence+1 ||
		heartbeatOutcome.decision.CanonicalRequestDigest != heartbeat.CanonicalDigest ||
		!validWorkerLeaseDecision(heartbeatOutcome.decision) {
		t.Fatalf("heartbeat race result: %+v err=%v", heartbeatOutcome.decision, heartbeatOutcome.err)
	}
	if err := <-fenceDone; errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("stale concurrent fence=%v", err)
	}
	replayed, err := store.heartbeat(context.Background(), heartbeat)
	if err != nil || !replayed.Replayed || replayed.Lease != heartbeatOutcome.decision.Lease ||
		replayed.CanonicalDigest != heartbeatOutcome.decision.CanonicalDigest || !validWorkerLeaseDecision(replayed) {
		t.Fatalf("heartbeat restart-safe replay: %+v err=%v", replayed, err)
	}
	renewed, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	observe, err := newWorkerObserve(workerOperationRefFromSnapshot(start, renewed), initialWorkerCursor(renewed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.observe(context.Background(), observe); err != nil {
		t.Fatalf("observation did not inherit renewed exact lease fence: %v", err)
	}
}

func TestPostgresWorkerStopWinsLateSuccessRaceAndReplaysAfterResponseLoss(t *testing.T) {
	now := time.Date(2026, time.July, 31, 14, 20, 0, 0, time.UTC)
	db, schema, store, config, start, command, backend := newPostgresWorkerProtocolHarness(
		t, "worker_stop_pg", now, nil,
	)
	if _, err := store.accept(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	accepted, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	lateRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, accepted), initialWorkerCursor(accepted))
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		Kind: WorkerObservedSucceeded, ObservedAt: now,
		EvidenceID:     mustEvidenceID(t, "postgres-late-success-evidence"),
		EvidenceDigest: digest(249), InternalCallCount: 2,
	})
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-worker-stop-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: accepted.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: accepted.Operation.Generation, ExpectedRuntimeFence: accepted.RuntimeFence,
		Authority: start.Authority, Reason: CancellationUserRequested,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newWorkerStopIntentFromSnapshot(
		start, cancelled.Snapshot, mustOperationID(t, "postgres-worker-stop"), WorkerStopCancellation, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	staleMachine := intent
	staleMachine.NodeGeneration++
	staleMachine.CanonicalDigest = canonicalWorkerStopIntentDigest(staleMachine)
	_, err = store.stop(context.Background(), staleMachine)
	if errorCode(err) != ErrorAuthorizationDenied || backend.stopCount() != 0 {
		t.Fatalf("stale machine Stop reached backend: err=%v calls=%d", err, backend.stopCount())
	}

	blocker := &blockingPersistenceFault{
		point: PersistenceFaultBeforeWorkerStopCommit, entered: make(chan struct{}), release: make(chan struct{}),
	}
	store.faults = blocker
	type stopResult struct {
		ack workerStopAck
		err error
	}
	stopDone := make(chan stopResult, 1)
	go func() {
		ack, err := store.stop(context.Background(), intent)
		stopDone <- stopResult{ack: ack, err: err}
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not reach commit barrier")
	}
	lateDone := make(chan error, 1)
	go func() {
		_, err := store.observe(context.Background(), lateRequest)
		lateDone <- err
	}()
	close(blocker.release)
	stopped := <-stopDone
	if stopped.err != nil || !stopped.ack.BestEffortAccepted {
		t.Fatalf("Stop race result: %+v err=%v", stopped.ack, stopped.err)
	}
	if err := <-lateDone; errorCode(err) != ErrorAuthorizationDenied {
		t.Fatalf("late success crossed Stop/cancel fence: %v", err)
	}
	if backend.observeCount() != 0 {
		t.Fatalf("late fenced success reached backend %d times", backend.observeCount())
	}

	config.Faults = &PersistenceFaultController{}
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Faults.(*PersistenceFaultController).FailNextAt(PersistenceFaultBeforeWorkerStopResponse); err != nil {
		t.Fatal(err)
	}
	// The exact replay is resolved from the journal before any response fault,
	// so a previously committed Stop remains available without another backend call.
	replayed, err := restarted.stop(context.Background(), intent)
	if err != nil || replayed != stopped.ack || backend.stopCount() != 1 {
		t.Fatalf("Stop replay after restart: %+v err=%v calls=%d", replayed, err, backend.stopCount())
	}
	inspected, err := restarted.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || inspected.Worker.Stop.Status != WorkerStopAccepted ||
		inspected.Capacity != cancelled.Snapshot.Capacity ||
		inspected.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("Stop projection claimed release: %+v err=%v", inspected, err)
	}
	var stops int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_worker_stops`).Scan(&stops); err != nil || stops != 1 {
		t.Fatalf("Stop journal=%d err=%v", stops, err)
	}
}

func TestPostgresWorkerStopFaultsRollbackOrReplayExactAck(t *testing.T) {
	for _, test := range []struct {
		name       string
		prefix     string
		fault      PersistenceFaultPoint
		wantStored int
		wantCode   ErrorCode
	}{
		{name: "before commit", prefix: "worker_stop_pre", fault: PersistenceFaultBeforeWorkerStopCommit, wantStored: 0, wantCode: ErrorDependencyUnavailable},
		{name: "before response", prefix: "worker_stop_resp", fault: PersistenceFaultBeforeWorkerStopResponse, wantStored: 1, wantCode: ErrorReconciliationRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 31, 14, 40, 0, 0, time.UTC)
			faults := &PersistenceFaultController{}
			db, schema, store, config, start, command, backend := newPostgresWorkerProtocolHarness(
				t, test.prefix, now, faults,
			)
			if _, err := store.accept(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			intent := cancelPostgresWorkerForStop(t, store, start, now, "stop-fault-"+test.prefix)
			if err := faults.FailNextAt(test.fault); err != nil {
				t.Fatal(err)
			}
			_, err := store.stop(context.Background(), intent)
			if errorCode(err) != test.wantCode {
				t.Fatalf("Stop fault=%v code=%v want=%v", err, errorCode(err), test.wantCode)
			}
			var stops int
			if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
				`.runtime_execution_worker_stops`).Scan(&stops); err != nil || stops != test.wantStored {
				t.Fatalf("Stop journal=%d want=%d err=%v", stops, test.wantStored, err)
			}
			config.Faults = nil
			restarted, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := restarted.stop(context.Background(), intent)
			if err != nil || ack != newWorkerStopAck(intent) {
				t.Fatalf("Stop retry/replay: %+v err=%v", ack, err)
			}
			wantCalls := 2
			if test.wantStored == 1 {
				wantCalls = 1
			}
			if backend.stopCount() != wantCalls {
				t.Fatalf("Stop backend calls=%d want=%d", backend.stopCount(), wantCalls)
			}
		})
	}
}

func cancelPostgresWorkerForStop(
	t *testing.T,
	store *PostgresAuthority,
	start StartRuntimeRun,
	now time.Time,
	suffix string,
) workerStopIntent {
	t.Helper()
	current, err := store.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, suffix+"-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: current.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: current.Operation.Generation, ExpectedRuntimeFence: current.RuntimeFence,
		Authority: start.Authority, Reason: CancellationUserRequested,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newWorkerStopIntentFromSnapshot(
		start, cancelled.Snapshot, mustOperationID(t, suffix+"-request"), WorkerStopCancellation, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func newPostgresWorkerProtocolHarness(
	t *testing.T,
	prefix string,
	now time.Time,
	faults PersistenceFaultInjector,
) (*sql.DB, string, *PostgresAuthority, PostgresConfig, StartRuntimeRun, workerAccept, contractWorkerBackendControl) {
	t.Helper()
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID(prefix+"-view")), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, prefix, now, func() time.Time { return now }, lifecycle, nil,
	)
	backend := newContractToolWorkerBackend()
	adapter, err := newToolWorkerCapabilityAdapter(validToolPlanForStart(start), backend)
	if err != nil {
		t.Fatal(err)
	}
	config.Faults, config.toolWorker = faults, adapter
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare PostgreSQL worker Capsule: %+v err=%v", prepared, err)
	}
	delivery, err := store.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	return db, schema, store, config, start, command, backend
}
