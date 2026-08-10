package runtimeexecution

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRaceGateConcurrentExecuteInspectAndDeadlineTimer covers Testing Decision
// 22 components "Execute/Inspect" and "deadline timer" with real concurrency:
// many goroutines run Execute/Inspect while the controlled clock advances
// toward (not past) the grant/deadline boundary, then past it to prove the
// deadline timer fences without creating a second decision.
func TestRaceGateConcurrentExecuteInspectAndDeadlineTimer(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "race-exec-inspect-caller", 4)
	start := standardStart(t, now, authority, "race-exec-inspect")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{
			grantFixtureForStart(start, now.Add(2*time.Minute), true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	startGate := make(chan struct{})
	for index := 0; index < 24; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-startGate
			for iteration := 0; iteration < 64; iteration++ {
				_, _ = harness.Runtime.Execute(context.Background(), start)
				_, _ = harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
					SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
					PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
					Authority: authority,
				})
			}
		}()
	}
	close(startGate)
	// Advance the clock while Execute/Inspect run so the deadline timer races
	// with admission. Stay inside the grant lifetime so the first accepted
	// decision remains durable.
	if err := harness.AdvanceClock(90 * time.Second); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	inspected, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one decision may ever be produced for one Runtime Run.
	if inspected.RuntimeRevision != start.ExpectedRuntimeRevision+1 {
		t.Fatalf("concurrent Execute+deadline race produced wrong revision: %#v", inspected)
	}
}

// TestRaceGateDeadlineTimerTimesOutPendingLease covers the "deadline timer"
// component on a runtime that is accepted but never receives a lease: advancing
// the clock past LeaseAcquireBy must produce a no-lease terminal TimedOut, not
// a second acceptance.
func TestRaceGateDeadlineTimerTimesOutPendingLease(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 10, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "race-deadline-caller", 4)
	start := standardStart(t, now, authority, "race-deadline")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{
			grantFixtureForStart(start, now.Add(30*time.Minute), true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || accepted.Snapshot.State != RuntimeWaitingForLease {
		t.Fatalf("accepted pending lease: %+v err=%v", accepted, err)
	}
	// LeaseAcquireBy = earlier(deadline, grantExpiry) = deadline (now+20m).
	// Advance past the Runtime deadline while staying inside the grant.
	if err := harness.AdvanceClock(21 * time.Minute); err != nil {
		t.Fatal(err)
	}
	// Concurrent deadline evaluation plus replay must produce exactly one
	// no-lease terminal decision.
	decisions := executeConcurrently(t, harness.Runtime, start, 16)
	terminal := decisions[0]
	if terminal.Fact.Disposition != DecisionAccepted ||
		terminal.Snapshot.State != RuntimeTerminal || terminal.Snapshot.Outcome != RuntimeTimedOut {
		t.Fatalf("deadline timer outcome = %#v", terminal)
	}
	for index, decision := range decisions {
		if decision.Fact != terminal.Fact || decision.Snapshot != terminal.Snapshot {
			t.Fatalf("deadline decision %d diverged: %#v", index, decision)
		}
	}
	if terminal.Snapshot.Capacity.NoLease != NoLeaseDispositionRecorded {
		t.Fatalf("deadline timer did not record no-lease disposition: %#v", terminal.Snapshot.Capacity)
	}
}

// TestRaceGateConcurrentWorkerObservationAndLeaseRenewal covers Testing
// Decision 22 components "worker observation", "lease renewal", and "terminal
// ingestion" with real concurrency.
func TestRaceGateConcurrentWorkerObservationAndLeaseRenewal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "race-worker-caller", 7)
	start := standardStart(t, now, authority, "race-worker")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = ExecutionNodeID{value: "race-node"}, 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	backend := newContractToolWorkerBackend()
	adapter, err := newToolWorkerCapabilityAdapter(validToolPlanForStart(start), backend)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
			BindLeaseFencingAuthority(node.ExecutionNodeID, workerTestFencingAuthority(t, "race-worker")),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "race-worker-input-evidence", digest(243)), nil
		}),
		toolWorker: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare race worker capsule: %+v err=%v", prepared, err)
	}
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	acceptCommand, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), acceptCommand); err != nil {
		t.Fatal(err)
	}
	current, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "race-worker-heartbeat"),
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
	observe, err := newWorkerObserve(workerOperationRefFromSnapshot(start, current), initialWorkerCursor(current))
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedFailed, ObservedAt: now,
		EvidenceID: mustEvidenceID(t, "race-worker-evidence"), EvidenceDigest: digest(244),
		InternalCallCount: 2, SafeFailure: WorkerFailureCapability,
	})

	var wait sync.WaitGroup
	startGate := make(chan struct{})
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-startGate
			for iteration := 0; iteration < 32; iteration++ {
				_, _ = harness.workers.heartbeat(context.Background(), heartbeat)
				_, _ = harness.workers.observe(context.Background(), observe)
				_, _ = harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			}
		}()
	}
	close(startGate)
	wait.Wait()
	final, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	if final.Worker.Status == WorkerOperationNone {
		t.Fatalf("worker observation race left no worker status: %#v", final.Worker)
	}
	if backend.observeCount() > 3 {
		t.Fatalf("worker backend observed too many times: %d", backend.observeCount())
	}
}

// TestRaceGateConcurrentReconciliationAndCleanupClaim covers Testing Decision
// 22 components "reconciliation" and "cleanup claim" with real concurrency.
func TestRaceGateConcurrentReconciliationAndCleanupClaim(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "race-recon-cleanup-caller", 7)
	start := standardStart(t, now, authority, "race-recon-cleanup")
	harness := harnessForStartWithDecisionID(t, now, authority, start, 900)
	accepted := executeDecision(t, harness, start)
	cleanup, err := cleanupObligationForRace(t, accepted, start, "race-recon-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	startGate := make(chan struct{})
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-startGate
			for iteration := 0; iteration < 32; iteration++ {
				_, _ = harness.Maintenance.Maintain(context.Background(), cleanup)
				_, _ = harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
					SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
					PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
					Authority: authority,
				})
			}
		}()
	}
	close(startGate)
	wait.Wait()
	inspected, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inspected.RuntimeRevision != accepted.Snapshot.RuntimeRevision {
		t.Fatalf("cleanup claim race changed Runtime authority: %#v", inspected)
	}
}

func cleanupObligationForRace(t *testing.T, accepted RuntimeDecision, start StartRuntimeRun, suffix string) (CreateCleanupObligation, error) {
	t.Helper()
	operationID := mustOperationID(t, suffix+"-operation")
	creation := cleanupDebtCreation{
		MutationID: operationID.String(), DebtID: suffix + "-debt",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority:     start.Authority,
		ResourceClass: cleanupResourceContainment, ResourceIdentityDigest: digest(231),
		ResourceGeneration: 7, ResourceFence: 9, Intent: cleanupIntentContain,
		CauseDecisionID: accepted.Fact.DecisionID, CauseOperationID: start.OperationID,
		RetentionFactDigest: digest(232), EligibilityFactDigest: digest(233),
		CreatedAt: accepted.Snapshot.Deadline.Add(-30 * time.Minute), EligibleAt: accepted.Snapshot.Deadline.Add(-30 * time.Minute),
		Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
	}
	return NewCreateCleanupObligation(CreateCleanupObligationInput{
		SchemaVersion:           SchemaV1,
		OperationID:             operationID,
		Reason:                  CleanupObligationPostLeaseTerminal,
		ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence:    accepted.Snapshot.RuntimeFence,
		Obligation:              creation,
	})
}
