package runtimeexecution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestConcurrentExactStartAndCancelReplayOneDecision(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 6, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "concurrent-caller", 2)
	start := standardStart(t, now, authority, "concurrent")
	harness := harnessForStartWithDecisionID(t, now, authority, start, 900)

	starts := executeConcurrently(t, harness.Runtime, start, 16)
	for index, decision := range starts {
		if decision.Fact != starts[0].Fact || decision.Snapshot != starts[0].Snapshot {
			t.Fatalf("start %d diverged: %#v", index, decision)
		}
	}
	if starts[0].Fact.DecisionID.String() != "runtime-decision-000900" || starts[0].Snapshot.RuntimeRevision != start.ExpectedRuntimeRevision+1 {
		t.Fatalf("concurrent start allocated multiple decisions: %#v", starts[0])
	}

	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "cancel-concurrent"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: starts[0].Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: starts[0].Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        starts[0].Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancels := executeConcurrently(t, harness.Runtime, cancel, 16)
	for index, decision := range cancels {
		if decision.Fact != cancels[0].Fact || decision.Snapshot != cancels[0].Snapshot {
			t.Fatalf("cancel %d diverged: %#v", index, decision)
		}
	}
	if cancels[0].Fact.DecisionID.String() != "runtime-decision-000901" || cancels[0].Snapshot.RuntimeRevision != start.ExpectedRuntimeRevision+2 {
		t.Fatalf("concurrent cancel allocated multiple decisions: %#v", cancels[0])
	}

	var wait sync.WaitGroup
	startInspect := make(chan struct{})
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-startInspect
			for iteration := 0; iteration < 32; iteration++ {
				_, inspectErr := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
					SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
					PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
				})
				if inspectErr != nil {
					t.Errorf("inspect: %v", inspectErr)
					return
				}
			}
		}()
	}
	close(startInspect)
	wait.Wait()
}

func TestInFlightRuntimeViewOpenAcceptanceIsFencedByConcurrentCancelWithoutReplay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 22, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "inflight-open-cancel-authority", 7)
	start, grant, node := mutatingPrerequisiteStart(
		t, now, authority, "inflight-open-cancel", digest(181),
	)
	openEntered := make(chan taskworkspace.OpenRuntimeViewRequest, 1)
	releaseOpen := make(chan struct{})
	var mu sync.Mutex
	var opened []taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			opened = append(opened, request)
			mu.Unlock()
			openEntered <- request
			<-releaseOpen
			return acceptedRuntimeViewResult(request, "inflight-open-cancel-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	startResult := make(chan executeResult, 1)
	go func() {
		decision, err := harness.Runtime.Execute(context.Background(), start)
		startResult <- executeResult{decision: decision, err: err}
	}()

	var openRequest taskworkspace.OpenRuntimeViewRequest
	select {
	case openRequest = <-openEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("C04 OpenRuntimeView was not reached")
	}
	snapshot, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || snapshot.State != RuntimePreparingPrerequisites ||
		snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("in-flight open intent was not durable: %+v err=%v", snapshot, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "inflight-open-cancel-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: snapshot.Operation.Generation,
		ExpectedRuntimeFence:        snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("concurrent cancel did not commit: %+v err=%v", cancelled, err)
	}
	close(releaseOpen)
	var completed executeResult
	select {
	case completed = <-startResult:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight C04 acceptance did not reconcile after cancel")
	}
	if completed.err != nil || completed.decision.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("original Execute did not return reconciled cancellation: %+v err=%v",
			completed.decision, completed.err)
	}

	mu.Lock()
	if len(opened) != 1 || len(fenced) != 1 || opened[0] != openRequest ||
		fenced[0].RuntimeViewID != "inflight-open-cancel-view" ||
		fenced[0].RuntimeOperationID != openRequest.RuntimeOperationID ||
		fenced[0].SandboxLeaseAuthority != openRequest.SandboxLeaseAuthority ||
		fenced[0].Operation.RequestDigest != fenced[0].CanonicalRequestDigest() {
		mu.Unlock()
		t.Fatalf("late C04 acceptance was not exactly fenced: opens=%+v fences=%+v", opened, fenced)
	}
	mu.Unlock()
	if _, err := harness.Runtime.Execute(context.Background(), cancel); err != nil {
		t.Fatalf("cancel exact replay: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || len(fenced) != 1 {
		t.Fatalf("exact replay created another view or fence obligation: opens=%d fences=%d",
			len(opened), len(fenced))
	}
}

func TestPostgresInFlightRuntimeViewOpenAcceptanceIsFencedByConcurrentCancelWithoutReplay(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 22, 0, 0, time.UTC)
	openEntered := make(chan taskworkspace.OpenRuntimeViewRequest, 1)
	releaseOpen := make(chan struct{})
	var mu sync.Mutex
	var opened []taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			opened = append(opened, request)
			mu.Unlock()
			openEntered <- request
			<-releaseOpen
			return acceptedRuntimeViewResult(request, "postgres-inflight-open-cancel-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "pgopencxl", now, func() time.Time { return now }, lifecycle, nil,
	)
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	startResult := make(chan executeResult, 1)
	go func() {
		decision, err := store.Execute(context.Background(), start)
		startResult <- executeResult{decision: decision, err: err}
	}()

	var openRequest taskworkspace.OpenRuntimeViewRequest
	select {
	case openRequest = <-openEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL C04 OpenRuntimeView was not reached")
	}
	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || snapshot.State != RuntimePreparingPrerequisites ||
		snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("PostgreSQL in-flight open intent was not durable: %+v err=%v", snapshot, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-inflight-open-cancel-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: snapshot.Operation.Generation,
		ExpectedRuntimeFence:        snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("concurrent PostgreSQL cancel did not commit: %+v err=%v", cancelled, err)
	}
	close(releaseOpen)
	var completed executeResult
	select {
	case completed = <-startResult:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight PostgreSQL C04 acceptance did not reconcile after cancel")
	}
	if completed.err != nil || completed.decision.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("original PostgreSQL Execute did not return reconciled cancellation: %+v err=%v",
			completed.decision, completed.err)
	}

	mu.Lock()
	if len(opened) != 1 || len(fenced) != 1 || opened[0] != openRequest ||
		fenced[0].RuntimeViewID != "postgres-inflight-open-cancel-view" ||
		fenced[0].RuntimeOperationID != openRequest.RuntimeOperationID ||
		fenced[0].SandboxLeaseAuthority != openRequest.SandboxLeaseAuthority ||
		fenced[0].Operation.RequestDigest != fenced[0].CanonicalRequestDigest() {
		mu.Unlock()
		t.Fatalf("late PostgreSQL C04 acceptance was not exactly fenced: opens=%+v fences=%+v", opened, fenced)
	}
	mu.Unlock()
	var openOperations, terminalOperations, terminalOutbox, terminalAcknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_operations WHERE prerequisite_kind=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$2)`,
		postgresPrerequisiteRuntimeView, OutboxAcknowledged).
		Scan(&openOperations, &terminalOperations, &terminalOutbox, &terminalAcknowledged); err != nil {
		t.Fatal(err)
	}
	if openOperations != 1 || terminalOperations != 1 || terminalOutbox != 1 || terminalAcknowledged != 1 {
		t.Fatalf("late-open exact obligations: open=%d terminal=%d outbox=%d acknowledged=%d",
			openOperations, terminalOperations, terminalOutbox, terminalAcknowledged)
	}
	if _, err := store.Execute(context.Background(), cancel); err != nil {
		t.Fatalf("PostgreSQL cancel exact replay: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || len(fenced) != 1 {
		t.Fatalf("PostgreSQL exact replay created another view or fence obligation: opens=%d fences=%d",
			len(opened), len(fenced))
	}
}

func TestPostgresLateRuntimeViewAcceptanceRetainsCancelFenceAcrossFaultAndRestart(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 24, 0, 0, time.UTC)
	openEntered := make(chan taskworkspace.OpenRuntimeViewRequest, 1)
	releaseOpen := make(chan struct{})
	var mu sync.Mutex
	var opened []taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			opened = append(opened, request)
			mu.Unlock()
			openEntered <- request
			<-releaseOpen
			return acceptedRuntimeViewResult(request, "postgres-faulted-late-open-cancel-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "pgfaultlatecxl", now, func() time.Time { return now }, lifecycle, nil,
	)
	faults := &PersistenceFaultController{}
	faultedConfig := config
	faultedConfig.Faults = faults
	faulted, err := NewPostgresAuthority(db, faultedConfig)
	if err != nil {
		t.Fatal(err)
	}
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	startResult := make(chan executeResult, 1)
	go func() {
		decision, executeErr := faulted.Execute(context.Background(), start)
		startResult <- executeResult{decision: decision, err: executeErr}
	}()

	var openRequest taskworkspace.OpenRuntimeViewRequest
	select {
	case openRequest = <-openEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL C04 OpenRuntimeView was not reached")
	}
	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || snapshot.State != RuntimePreparingPrerequisites ||
		snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("PostgreSQL in-flight open intent was not durable: %+v err=%v", snapshot, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-faulted-late-open-cancel-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: snapshot.Operation.Generation,
		ExpectedRuntimeFence:        snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("concurrent PostgreSQL cancel did not commit: %+v err=%v", cancelled, err)
	}
	if err := faults.FailNextAt(PersistenceFaultBeforeResponse); err != nil {
		t.Fatal(err)
	}
	close(releaseOpen)
	select {
	case completed := <-startResult:
		assertErrorCode(t, completed.err, ErrorReconciliationRequired)
	case <-time.After(5 * time.Second):
		t.Fatal("faulted late PostgreSQL C04 acceptance did not return")
	}

	var openOperations, terminalOperations, terminalOutbox, terminalAttempts int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_operations WHERE prerequisite_kind=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
		(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery)`,
		postgresPrerequisiteRuntimeView).
		Scan(&openOperations, &terminalOperations, &terminalOutbox, &terminalAttempts); err != nil {
		t.Fatal(err)
	}
	if openOperations != 1 || terminalOperations != 1 || terminalOutbox != 1 || terminalAttempts != 0 {
		t.Fatalf("atomic late-open obligations before restart: open=%d terminal=%d outbox=%d attempts=%d",
			openOperations, terminalOperations, terminalOutbox, terminalAttempts)
	}
	mu.Lock()
	if len(opened) != 1 || len(fenced) != 0 || opened[0] != openRequest {
		mu.Unlock()
		t.Fatalf("fault boundary delivered or recreated C04 authority: opens=%+v fences=%+v", opened, fenced)
	}
	mu.Unlock()

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("restart did not deliver retained late-open fence: %+v err=%v", replayed, err)
	}
	var openAcknowledged, terminalAcknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_operations WHERE prerequisite_kind=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_outbox_delivery WHERE operation_id=$3 AND disposition=$2),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$2),
		(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery)`,
		postgresPrerequisiteRuntimeView, OutboxAcknowledged, openRequest.Operation.ID).
		Scan(&openOperations, &terminalOperations, &terminalOutbox, &openAcknowledged,
			&terminalAcknowledged, &terminalAttempts); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if openOperations != 1 || terminalOperations != 1 || terminalOutbox != 1 ||
		openAcknowledged != 1 || terminalAcknowledged != 1 || terminalAttempts != 1 ||
		len(opened) != 1 || len(fenced) != 1 ||
		fenced[0].Reason != taskworkspace.RuntimeViewCancelled ||
		fenced[0].RuntimeViewID != "postgres-faulted-late-open-cancel-view" ||
		fenced[0].RuntimeOperationID != openRequest.RuntimeOperationID ||
		fenced[0].SandboxLeaseAuthority != openRequest.SandboxLeaseAuthority ||
		fenced[0].Operation.RequestDigest != fenced[0].CanonicalRequestDigest() {
		t.Fatalf("restart late-open exactness: open=%d terminal=%d outbox=%d openAck=%d terminalAck=%d attempts=%d opens=%+v fences=%+v",
			openOperations, terminalOperations, terminalOutbox, openAcknowledged,
			terminalAcknowledged, terminalAttempts, opened, fenced)
	}
}

func TestInFlightRuntimeViewOpenAcceptanceIsFencedByConcurrentLeaseRevokeWithoutReplay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 22, 24, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "inflight-open-revoke-authority", 7)
	start, grant, node := mutatingPrerequisiteStart(
		t, now, authority, "inflight-open-revoke", digest(182),
	)
	fencingAuthority := NewRecoveryLeaseFencingAuthority(
		mustAuthorityID(t, "inflight-open-revoke-fencing-authority"), 3,
	)
	resetAuthority := NewSandboxResetAuthority(
		mustAuthorityID(t, "inflight-open-revoke-reset-authority"), 4,
	)
	openEntered := make(chan taskworkspace.OpenRuntimeViewRequest, 1)
	releaseOpen := make(chan struct{})
	var mu sync.Mutex
	var opened []taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			opened = append(opened, request)
			mu.Unlock()
			openEntered <- request
			<-releaseOpen
			return acceptedRuntimeViewResult(request, "inflight-open-revoke-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
			BindLeaseFencingAuthority(grant.ExecutionNodeID, fencingAuthority),
			BindSandboxResetAuthority(grant.ExecutionNodeID, resetAuthority),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "inflight-open-revoke-release-evidence", digest(183)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "inflight-open-revoke-input-evidence", digest(184)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	startResult := make(chan executeResult, 1)
	go func() {
		decision, err := harness.Runtime.Execute(context.Background(), start)
		startResult <- executeResult{decision: decision, err: err}
	}()

	var openRequest taskworkspace.OpenRuntimeViewRequest
	select {
	case openRequest = <-openEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("C04 OpenRuntimeView was not reached before lease revoke")
	}
	snapshot, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || snapshot.State != RuntimePreparingPrerequisites ||
		snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("in-flight open intent was not durable before revoke: %+v err=%v", snapshot, err)
	}
	fence := concurrentNodeLossFence(t, start, snapshot, fencingAuthority, "inflight-open-revoke-operation", now)
	revoked, err := harness.Maintenance.Maintain(context.Background(), fence)
	if err != nil || revoked.Lease.Disposition != LeaseRevoked {
		t.Fatalf("concurrent lease revoke did not commit: %+v err=%v", revoked, err)
	}
	stopping, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	reset := concurrentCompleteReset(
		t, start, stopping, resetAuthority, "inflight-open-revoke-reset", now,
	)
	if _, err := harness.Maintenance.Maintain(context.Background(), reset); err == nil {
		t.Fatal("sandbox reset released capacity while C04 open remained in flight")
	} else {
		assertErrorCode(t, err, ErrorIntegrityConflict)
	}
	stillStopping, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || stillStopping.Lease.Disposition != LeaseRevoked ||
		stillStopping.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined {
		t.Fatalf("rejected reset lost lease-fence authority: %+v err=%v", stillStopping, err)
	}
	close(releaseOpen)
	var completed executeResult
	select {
	case completed = <-startResult:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight C04 acceptance did not reconcile after lease revoke")
	}
	if completed.err != nil || completed.decision.Snapshot.State != RuntimeStopping ||
		completed.decision.Snapshot.Lease.Disposition != LeaseRevoked {
		t.Fatalf("original Execute did not return reconciled lease revoke: %+v err=%v",
			completed.decision, completed.err)
	}

	mu.Lock()
	if len(opened) != 1 || len(fenced) != 1 || opened[0] != openRequest ||
		fenced[0].RuntimeViewID != "inflight-open-revoke-view" ||
		fenced[0].RuntimeOperationID != openRequest.RuntimeOperationID ||
		fenced[0].SandboxLeaseAuthority != openRequest.SandboxLeaseAuthority ||
		fenced[0].Reason != taskworkspace.RuntimeViewRevoked ||
		fenced[0].Operation.RequestDigest != fenced[0].CanonicalRequestDigest() {
		mu.Unlock()
		t.Fatalf("late C04 acceptance was not exactly fenced after revoke: opens=%+v fences=%+v", opened, fenced)
	}
	mu.Unlock()
	if _, err := harness.Maintenance.Maintain(context.Background(), fence); err != nil {
		t.Fatalf("lease revoke exact replay: %v", err)
	}
	if released, err := harness.Maintenance.Maintain(context.Background(), reset); err != nil ||
		released.Lease.Disposition != LeaseReleased {
		t.Fatalf("sandbox reset did not unblock after exact C04 fence: %+v err=%v", released, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || len(fenced) != 1 {
		t.Fatalf("lease revoke replay created another view or fence obligation: opens=%d fences=%d",
			len(opened), len(fenced))
	}
}

func TestPostgresInFlightRuntimeViewOpenAcceptanceIsFencedByConcurrentLeaseRevokeWithoutReplay(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 26, 0, 0, time.UTC)
	const schemaPrefix = "pgopenrevoke"
	fencingAuthority := NewRecoveryLeaseFencingAuthority(
		mustAuthorityID(t, "postgres-inflight-open-revoke-fencing-authority"), 3,
	)
	resetAuthority := NewSandboxResetAuthority(
		mustAuthorityID(t, "postgres-inflight-open-revoke-reset-authority"), 4,
	)
	openEntered := make(chan taskworkspace.OpenRuntimeViewRequest, 1)
	releaseOpen := make(chan struct{})
	var mu sync.Mutex
	var opened []taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			opened = append(opened, request)
			mu.Unlock()
			openEntered <- request
			<-releaseOpen
			return acceptedRuntimeViewResult(request, "postgres-inflight-open-revoke-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, schemaPrefix, now, func() time.Time { return now }, lifecycle, nil,
	)
	nodeID := ExecutionNodeID{value: schemaPrefix + "-node"}
	config.MaintenanceAuthorities = []RuntimeMaintenanceAuthorityBinding{
		BindLeaseFencingAuthority(nodeID, fencingAuthority),
		BindSandboxResetAuthority(nodeID, resetAuthority),
	}
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	startResult := make(chan executeResult, 1)
	go func() {
		decision, err := store.Execute(context.Background(), start)
		startResult <- executeResult{decision: decision, err: err}
	}()

	var openRequest taskworkspace.OpenRuntimeViewRequest
	select {
	case openRequest = <-openEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("PostgreSQL C04 OpenRuntimeView was not reached before lease revoke")
	}
	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || snapshot.State != RuntimePreparingPrerequisites ||
		snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("PostgreSQL in-flight open intent was not durable before revoke: %+v err=%v", snapshot, err)
	}
	fence := concurrentNodeLossFence(
		t, start, snapshot, fencingAuthority, "postgres-inflight-open-revoke-operation", now,
	)
	revoked, err := store.Maintain(context.Background(), fence)
	if err != nil || revoked.Lease.Disposition != LeaseRevoked {
		t.Fatalf("concurrent PostgreSQL lease revoke did not commit: %+v err=%v", revoked, err)
	}
	stopping, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	reset := concurrentCompleteReset(
		t, start, stopping, resetAuthority, "postgres-inflight-open-revoke-reset", now,
	)
	if _, err := store.Maintain(context.Background(), reset); err == nil {
		t.Fatal("PostgreSQL sandbox reset released capacity while C04 open remained in flight")
	} else {
		assertErrorCode(t, err, ErrorIntegrityConflict)
	}
	stillStopping, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || stillStopping.Lease.Disposition != LeaseRevoked ||
		stillStopping.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined {
		t.Fatalf("rejected PostgreSQL reset lost lease-fence authority: %+v err=%v", stillStopping, err)
	}
	close(releaseOpen)
	var completed executeResult
	select {
	case completed = <-startResult:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight PostgreSQL C04 acceptance did not reconcile after lease revoke")
	}
	if completed.err != nil || completed.decision.Snapshot.State != RuntimeStopping ||
		completed.decision.Snapshot.Lease.Disposition != LeaseRevoked {
		t.Fatalf("original PostgreSQL Execute did not return reconciled lease revoke: %+v err=%v",
			completed.decision, completed.err)
	}

	mu.Lock()
	if len(opened) != 1 || len(fenced) != 1 || opened[0] != openRequest ||
		fenced[0].RuntimeViewID != "postgres-inflight-open-revoke-view" ||
		fenced[0].RuntimeOperationID != openRequest.RuntimeOperationID ||
		fenced[0].SandboxLeaseAuthority != openRequest.SandboxLeaseAuthority ||
		fenced[0].Reason != taskworkspace.RuntimeViewRevoked ||
		fenced[0].Operation.RequestDigest != fenced[0].CanonicalRequestDigest() {
		mu.Unlock()
		t.Fatalf("late PostgreSQL C04 acceptance was not exactly fenced after revoke: opens=%+v fences=%+v", opened, fenced)
	}
	mu.Unlock()
	var openOperations, terminalOperations, terminalOutbox, terminalAcknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_operations WHERE prerequisite_kind=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$2)`,
		postgresPrerequisiteRuntimeView, OutboxAcknowledged).
		Scan(&openOperations, &terminalOperations, &terminalOutbox, &terminalAcknowledged); err != nil {
		t.Fatal(err)
	}
	if openOperations != 1 || terminalOperations != 1 || terminalOutbox != 1 || terminalAcknowledged != 1 {
		t.Fatalf("late-open revoke obligations: open=%d terminal=%d outbox=%d acknowledged=%d",
			openOperations, terminalOperations, terminalOutbox, terminalAcknowledged)
	}
	if _, err := store.Maintain(context.Background(), fence); err != nil {
		t.Fatalf("PostgreSQL lease revoke exact replay: %v", err)
	}
	if released, err := store.Maintain(context.Background(), reset); err != nil ||
		released.Lease.Disposition != LeaseReleased {
		t.Fatalf("PostgreSQL sandbox reset did not unblock after exact C04 fence: %+v err=%v", released, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || len(fenced) != 1 {
		t.Fatalf("PostgreSQL lease revoke replay created another view or fence obligation: opens=%d fences=%d",
			len(opened), len(fenced))
	}
}

func concurrentNodeLossFence(
	t *testing.T,
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	authority LeaseFencingAuthority,
	operationID string,
	now time.Time,
) FenceSandboxLease {
	t.Helper()
	fence, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, operationID),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeFence: snapshot.RuntimeFence, SandboxLeaseID: snapshot.Lease.LeaseID,
		LeaseGeneration: snapshot.Lease.Generation, LeaseFence: snapshot.Lease.Fence,
		ExecutionNodeID: snapshot.Operation.ExecutionNodeID,
		NodeGeneration:  NodeGeneration(snapshot.Operation.NodeCapacityGeneration),
		Reason:          LeaseFenceNodeLost, Authority: authority,
		ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fence
}

func concurrentCompleteReset(
	t *testing.T,
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	authority SandboxResetAuthority,
	operationID string,
	now time.Time,
) ConfirmSandboxReset {
	t.Helper()
	reset, err := NewConfirmSandboxReset(ConfirmSandboxResetInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, operationID),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeFence: snapshot.RuntimeFence, SandboxLeaseID: snapshot.Lease.LeaseID,
		LeaseGeneration: snapshot.Lease.Generation, LeaseFence: snapshot.Lease.Fence,
		SandboxID: snapshot.Lease.SandboxID, SandboxGeneration: snapshot.Lease.SandboxGeneration,
		SandboxFence: snapshot.Lease.SandboxFence, ExecutionNodeID: snapshot.Operation.ExecutionNodeID,
		NodeGeneration: NodeGeneration(snapshot.Operation.NodeCapacityGeneration), Authority: authority,
		EvidenceID: mustEvidenceID(t, operationID+"-evidence"), EvidenceDigest: digest(185),
		ProcessStopped: true, ChildProcessesStopped: true, SecretsRevoked: true,
		NetworkRemoved: true, ContainmentEstablished: true, ResetCompleted: true,
		NoUnresolvedOccupancy: true, NoStaleWorkerAuthority: true, NoPriorTaskBytes: true,
		NoPriorSecrets: true, NoWritableCacheMutations: true, NoLogsOrTranscripts: true,
		NoPriorEvidence: true, NoProcessState: true, NoNetworkState: true,
		NoPriorOperationIdentities: true, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reset
}

func TestInMemoryLateRuntimeViewAcceptanceAtomicallyRetainsFenceBeforeReset(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 22, 28, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "atomic-late-open-authority", 7)
	start, grant, node := mutatingPrerequisiteStart(t, now, authority, "atomic-late-open", digest(186))
	fencingAuthority := NewRecoveryLeaseFencingAuthority(
		mustAuthorityID(t, "atomic-late-open-fencing-authority"), 3,
	)
	resetAuthority := NewSandboxResetAuthority(
		mustAuthorityID(t, "atomic-late-open-reset-authority"), 4,
	)
	var opened taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			opened = request
			return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{
				Code: taskworkspace.ErrorReconciliationRequired,
			}
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			fenced = append(fenced, request)
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
			BindLeaseFencingAuthority(grant.ExecutionNodeID, fencingAuthority),
			BindSandboxResetAuthority(grant.ExecutionNodeID, resetAuthority),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "atomic-late-open-release-evidence", digest(187)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "atomic-late-open-input-evidence", digest(188)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || pending.Snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("prepare ambiguous open: %+v err=%v", pending, err)
	}
	fence := concurrentNodeLossFence(
		t, start, pending.Snapshot, fencingAuthority, "atomic-late-open-revoke", now,
	)
	if _, err := harness.Maintenance.Maintain(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	result := acceptedRuntimeViewResult(opened, "atomic-late-open-view")
	fact, binding, err := runtimeViewFactFromResult(
		opened, digestFromTaskWorkspace(opened.Operation.RequestDigest), result, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := harness.Runtime.(*invariantEngine)
	if err := engine.persistInMemoryRuntimeViewFact(
		start, opened, digestFromTaskWorkspace(opened.Operation.RequestDigest), fact, binding,
	); err != nil {
		t.Fatal(err)
	}
	harness.store.mu.Lock()
	retainedTerminal := harness.store.runtimes[start.RuntimeRunID].runtimeViewTerminal
	harness.store.mu.Unlock()
	if retainedTerminal == nil || retainedTerminal.Kind != runtimeViewTerminalFence ||
		retainedTerminal.FenceRequest.Reason != taskworkspace.RuntimeViewRevoked {
		t.Fatalf("late acceptance was visible without exact retained fence: %+v", retainedTerminal)
	}
	accepted, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	reset := concurrentCompleteReset(t, start, accepted, resetAuthority, "atomic-late-open-reset", now)
	if released, err := harness.Maintenance.Maintain(context.Background(), reset); err != nil ||
		released.Lease.Disposition != LeaseReleased {
		t.Fatalf("reset with retained terminal obligation: %+v err=%v", released, err)
	}
	if err := engine.reconcileInMemoryRuntimeViewAfterOpen(context.Background(), start.RuntimeRunID); err != nil {
		t.Fatalf("deliver retained fence after reset: %v", err)
	}
	if len(fenced) != 1 || fenced[0] != retainedTerminal.FenceRequest {
		t.Fatalf("retained fence delivery drifted: retained=%+v delivered=%+v", retainedTerminal, fenced)
	}
}

func TestPostgresLateRuntimeViewAcceptanceAtomicallyRetainsFenceBeforeReset(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 30, 0, 0, time.UTC)
	const schemaPrefix = "pgatomiclateopen"
	fencingAuthority := NewRecoveryLeaseFencingAuthority(
		mustAuthorityID(t, "postgres-atomic-late-open-fencing-authority"), 3,
	)
	resetAuthority := NewSandboxResetAuthority(
		mustAuthorityID(t, "postgres-atomic-late-open-reset-authority"), 4,
	)
	var opened taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			opened = request
			return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{
				Code: taskworkspace.ErrorReconciliationRequired,
			}
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			fenced = append(fenced, request)
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, schemaPrefix, now, func() time.Time { return now }, lifecycle, nil,
	)
	nodeID := ExecutionNodeID{value: schemaPrefix + "-node"}
	config.MaintenanceAuthorities = []RuntimeMaintenanceAuthorityBinding{
		BindLeaseFencingAuthority(nodeID, fencingAuthority),
		BindSandboxResetAuthority(nodeID, resetAuthority),
	}
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Execute(context.Background(), start)
	if err != nil || pending.Snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
		t.Fatalf("prepare ambiguous PostgreSQL open: %+v err=%v", pending, err)
	}
	fence := concurrentNodeLossFence(
		t, start, pending.Snapshot, fencingAuthority, "postgres-atomic-late-open-revoke", now,
	)
	if _, err := store.Maintain(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	result := acceptedRuntimeViewResult(opened, "postgres-atomic-late-open-view")
	requestDigest := digestFromTaskWorkspace(opened.Operation.RequestDigest)
	fact, binding, err := runtimeViewFactFromResult(opened, requestDigest, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(opened)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.persistPostgresPrerequisiteFact(
		context.Background(), start, postgresPrerequisiteRuntimeView, canonical, fact, binding,
	); err != nil {
		t.Fatal(err)
	}
	var terminalOperations int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_runtime_view_terminal_operations WHERE runtime_run_id=$1`,
		start.RuntimeRunID.String()).Scan(&terminalOperations); err != nil {
		t.Fatal(err)
	}
	if terminalOperations != 1 {
		t.Fatalf("late PostgreSQL acceptance was visible with %d retained terminal obligations", terminalOperations)
	}
	accepted, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	reset := concurrentCompleteReset(t, start, accepted, resetAuthority, "postgres-atomic-late-open-reset", now)
	if released, err := store.Maintain(context.Background(), reset); err != nil ||
		released.Lease.Disposition != LeaseReleased {
		t.Fatalf("PostgreSQL reset with retained terminal obligation: %+v err=%v", released, err)
	}
	if err := store.reconcilePostgresRuntimeViewAfterOpen(context.Background(), start); err != nil {
		t.Fatalf("deliver retained PostgreSQL fence after reset: %v", err)
	}
	if len(fenced) != 1 || fenced[0].Reason != taskworkspace.RuntimeViewRevoked ||
		fenced[0].RuntimeViewID != "postgres-atomic-late-open-view" {
		t.Fatalf("retained PostgreSQL fence delivery drifted: %+v", fenced)
	}
}

func executeConcurrently(t *testing.T, runtime RuntimeExecution, command RuntimeCommand, count int) []RuntimeDecision {
	t.Helper()
	decisions := make([]RuntimeDecision, count)
	errorsByIndex := make([]error, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range decisions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			decisions[index], errorsByIndex[index] = runtime.Execute(context.Background(), command)
		}()
	}
	close(start)
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("execute %d: %v", index, err)
		}
	}
	return decisions
}

func TestConcurrentPostgresStartRetainsOneExactRuntimeViewIdentity(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 10, 0, 0, time.UTC)
	var mu sync.Mutex
	var opened []taskworkspace.OpenRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			opened = append(opened, request)
			mu.Unlock()
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("concurrent-open-view")), nil
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "conc_open", now, func() time.Time { return now }, lifecycle, nil,
	)
	withoutC04 := config
	withoutC04.RuntimeViewPrerequisite = nil
	preparer, err := NewPostgresAuthority(db, withoutC04)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := preparer.Execute(context.Background(), start)
	if err != nil || pending.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteAccepted ||
		pending.Snapshot.Readiness.RuntimeView.State != PrerequisitePending {
		t.Fatalf("prepare C04 pending runtime: %+v err=%v", pending, err)
	}

	stores := make([]RuntimeExecution, 12)
	for index := range stores {
		store, err := NewPostgresAuthority(db, config)
		if err != nil {
			t.Fatal(err)
		}
		stores[index] = store
	}
	_, concurrentErrors := executeAcrossRuntimesConcurrently(stores, start)
	assertConcurrentPostgresRetryErrors(t, concurrentErrors)
	converged, err := stores[0].Execute(context.Background(), start)
	if err != nil || !converged.Snapshot.Readiness.CapsuleReady ||
		converged.Snapshot.RuntimeViewBinding.RuntimeViewID.String() != "concurrent-open-view" {
		t.Fatalf("converge concurrent C04 open: %+v err=%v", converged, err)
	}

	mu.Lock()
	if len(opened) == 0 {
		mu.Unlock()
		t.Fatal("concurrent exact replay never delivered C04 open")
	}
	first := opened[0]
	for index, request := range opened[1:] {
		if !reflect.DeepEqual(request, first) {
			mu.Unlock()
			t.Fatalf("C04 open request %d drifted: got=%+v want=%+v", index+1, request, first)
		}
	}
	mu.Unlock()
	var operations, identities int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*), count(DISTINCT operation_id)
		FROM `+schema+`.runtime_execution_prerequisite_operations WHERE prerequisite_kind=$1`,
		postgresPrerequisiteRuntimeView).Scan(&operations, &identities); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || identities != 1 || converged.Snapshot.RuntimeViewBinding.OpenOperationID.String() != string(first.Operation.ID) {
		t.Fatalf("concurrent C04 identity: operations=%d identities=%d binding=%+v request=%+v",
			operations, identities, converged.Snapshot.RuntimeViewBinding, first)
	}
}

func TestConcurrentPostgresCancelReplaysOneExactRuntimeViewTerminalRequest(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 12, 0, 0, time.UTC)
	var mu sync.Mutex
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("concurrent-terminal-view")), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "conc_term", now, func() time.Time { return now }, lifecycle, nil,
	)
	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute prerequisites: %+v err=%v", started, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "concurrent-terminal-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutC04 := config
	withoutC04.RuntimeViewPrerequisite = nil
	c03Only, err := NewPostgresAuthority(db, withoutC04)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := c03Only.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("commit C03 cancel: %+v err=%v", cancelled, err)
	}

	stores := make([]RuntimeExecution, 12)
	for index := range stores {
		store, err := NewPostgresAuthority(db, config)
		if err != nil {
			t.Fatal(err)
		}
		stores[index] = store
	}
	_, concurrentErrors := executeAcrossRuntimesConcurrently(stores, cancel)
	assertConcurrentPostgresRetryErrors(t, concurrentErrors)
	converged, err := stores[0].Execute(context.Background(), cancel)
	if err != nil || converged != cancelled {
		t.Fatalf("converge concurrent C04 terminal: %+v err=%v want=%+v", converged, err, cancelled)
	}

	mu.Lock()
	if len(fenced) == 0 {
		mu.Unlock()
		t.Fatal("concurrent exact replay never delivered C04 terminal request")
	}
	first := fenced[0]
	for index, request := range fenced[1:] {
		if !reflect.DeepEqual(request, first) {
			mu.Unlock()
			t.Fatalf("C04 terminal request %d drifted: got=%+v want=%+v", index+1, request, first)
		}
	}
	mu.Unlock()
	var operations, identities, acknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(DISTINCT operation_id) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$1)`,
		OutboxAcknowledged).Scan(&operations, &identities, &acknowledged); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || identities != 1 || acknowledged != 1 {
		t.Fatalf("concurrent C04 terminal identity: operations=%d identities=%d acknowledged=%d",
			operations, identities, acknowledged)
	}
}

func executeAcrossRuntimesConcurrently(
	runtimes []RuntimeExecution,
	command RuntimeCommand,
) ([]RuntimeDecision, []error) {
	decisions := make([]RuntimeDecision, len(runtimes))
	errorsByIndex := make([]error, len(runtimes))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range runtimes {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			decisions[index], errorsByIndex[index] = runtimes[index].Execute(context.Background(), command)
		}(index)
	}
	close(start)
	wait.Wait()
	return decisions, errorsByIndex
}

func assertConcurrentPostgresRetryErrors(t *testing.T, errorsByIndex []error) {
	t.Helper()
	for index, err := range errorsByIndex {
		if err == nil {
			continue
		}
		var safeError *Error
		if !errors.As(err, &safeError) ||
			safeError.Code() != ErrorDependencyUnavailable && safeError.Code() != ErrorReconciliationRequired {
			t.Fatalf("concurrent PostgreSQL writer %d returned %T %v", index, err, err)
		}
	}
}
