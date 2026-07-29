package taskorchestration_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
	"github.com/slidesmith/slidesmith/backend/internal/scheduler"
	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

type postgresRuntimeAdmissionSystem struct {
	t            *testing.T
	db           *sql.DB
	schema       string
	clock        *runtimeAdmissionClock
	tasks        *taskorchestration.PostgresAdapter
	scheduling   *scheduler.PostgresAuthority
	runtime      *runtimeexecution.PostgresAuthority
	lease        runtimeexecution.LeaseAcquisitionAdapter
	faults       runtimeexecution.PersistenceFaultInjector
	nodeAttested bool
}

type admittedRuntimeWork struct {
	owner     taskorchestration.UserAuthority
	taskID    taskorchestration.TaskID
	canonical scheduler.AdmissionDecision
	start     runtimeexecution.StartRuntimeRun
}

type runtimeAdmissionClock struct {
	mu  sync.RWMutex
	now time.Time
}

type controlledLeaseAcquisitionAdapter struct {
	mu          sync.Mutex
	observation runtimeexecution.LeaseAcquisitionObservation
	calls       int
	requests    []runtimeexecution.LeaseAcquisitionRequest
}

type schedulerAcceptanceBarrier struct {
	delegate runtimeexecution.SchedulerAcceptanceParticipant
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (barrier *schedulerAcceptanceBarrier) Participate(
	ctx context.Context,
	transaction runtimeexecution.SchedulerAcceptanceTransaction,
	fact runtimeexecution.SchedulerAcceptanceFact,
) (runtimeexecution.SchedulerGrantBinding, error) {
	barrier.once.Do(func() {
		close(barrier.entered)
		<-barrier.release
	})
	return barrier.delegate.Participate(ctx, transaction, fact)
}

type secondCallLeaseBarrier struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (barrier *secondCallLeaseBarrier) ObserveLeaseAcquisition(
	_ context.Context,
	_ runtimeexecution.LeaseAcquisitionRequest,
) (runtimeexecution.LeaseAcquisitionObservation, error) {
	barrier.mu.Lock()
	barrier.calls++
	call := barrier.calls
	barrier.mu.Unlock()
	if call == 1 {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		}, nil
	}
	barrier.once.Do(func() {
		close(barrier.entered)
		<-barrier.release
	})
	return runtimeexecution.LeaseAcquisitionObservation{
		Disposition: runtimeexecution.LeaseAcquisitionReady,
	}, nil
}

func (adapter *controlledLeaseAcquisitionAdapter) ObserveLeaseAcquisition(
	_ context.Context,
	request runtimeexecution.LeaseAcquisitionRequest,
) (runtimeexecution.LeaseAcquisitionObservation, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.calls++
	adapter.requests = append(adapter.requests, request)
	return adapter.observation, nil
}

func (adapter *controlledLeaseAcquisitionAdapter) Set(
	observation runtimeexecution.LeaseAcquisitionObservation,
) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.observation = observation
}

func (adapter *controlledLeaseAcquisitionAdapter) CallCount() int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.calls
}

func (clock *runtimeAdmissionClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *runtimeAdmissionClock) Set(now time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = now.UTC()
}

func TestPostgresRuntimeAdmissionUsesOwnerPersonalWorkspaceAcrossTasks(t *testing.T) {
	now := time.Date(2026, time.July, 29, 11, 55, 0, 0, time.UTC)
	permanent := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition:      runtimeexecution.LeaseAcquisitionPermanentFailure,
			PermanentFailure: runtimeexecution.PreLeasePermanentImmutableBinding,
		}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, permanent, nil)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "shared-personal-workspace-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	first := system.enqueueAndAdmitRuntimeForOwner(t, "owner-workspace-first", owner)
	firstTerminal, err := system.runtime.Execute(context.Background(), first.start)
	if err != nil {
		t.Fatalf("terminalize first Runtime: %v", err)
	}
	assertAndConsumePostgresNoLeaseEvidence(t, system, first, firstTerminal.Snapshot)

	second := system.enqueueAndAdmitRuntimeForOwner(t, "owner-workspace-second", owner)
	if first.taskID == second.taskID || first.start.TaskID == second.start.TaskID {
		t.Fatalf("test requires distinct Tasks: first=%+v second=%+v", first, second)
	}
	if first.start.PersonalWorkspaceID != second.start.PersonalWorkspaceID {
		t.Fatalf("same User received Task-derived Personal Workspaces: first=%s second=%s",
			first.start.PersonalWorkspaceID.String(), second.start.PersonalWorkspaceID.String())
	}
}

func TestPostgresSchedulerInspectRequiresOwnerOrAdministratorScopeWithoutDisclosure(t *testing.T) {
	now := time.Date(2026, time.July, 29, 11, 57, 0, 0, time.UTC)
	system := newPostgresRuntimeAdmissionSystem(t, now, nil, nil)
	work := system.enqueueAndAdmitRuntime(t, "scheduler-inspect-scope")
	workspaceID, err := scheduler.NewPersonalWorkspaceID(work.start.PersonalWorkspaceID.String())
	if err != nil {
		t.Fatal(err)
	}
	ownerRef := scheduler.WorkItemRef{
		WorkItemID: work.canonical.WorkItemID,
		Scope:      scheduler.NewOwnerWorkItemQueryScope(workspaceID),
	}
	ownerView, err := system.scheduling.Inspect(context.Background(), ownerRef)
	if err != nil || ownerView.WorkItemID != work.canonical.WorkItemID {
		t.Fatalf("owner scope could not inspect Work Item: view=%+v err=%v", ownerView, err)
	}
	administratorRef := ownerRef
	administratorRef.Scope = scheduler.NewAdministratorWorkItemQueryScope()
	administratorView, err := system.scheduling.Inspect(context.Background(), administratorRef)
	if err != nil || administratorView != ownerView {
		t.Fatalf("administrator scope could not inspect operational Work Item: view=%+v err=%v",
			administratorView, err)
	}

	foreignWorkspaceID, err := scheduler.NewPersonalWorkspaceID("personal-workspace-foreign-owner")
	if err != nil {
		t.Fatal(err)
	}
	foreignRef := ownerRef
	foreignRef.Scope = scheduler.NewOwnerWorkItemQueryScope(foreignWorkspaceID)
	foreignView, err := system.scheduling.Inspect(context.Background(), foreignRef)
	assertSchedulerErrorCode(t, err, scheduler.ErrorAuthorizationDenied)
	if foreignView != (scheduler.WorkItemView{}) {
		t.Fatalf("foreign scope disclosed Work Item fields: %+v", foreignView)
	}
}

func TestPostgresSchedulerEnforcesEveryLogicalAdmissionLimitAgainstBoundReservations(t *testing.T) {
	now := time.Date(2026, time.July, 29, 11, 58, 0, 0, time.UTC)
	temporary := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		}, nil
	})
	for _, testCase := range []struct {
		name string
		slug string
	}{
		{name: "global", slug: "global"},
		{name: "Personal Workspace", slug: "personal-workspace"},
		{name: "worker class", slug: "worker-class"},
		{name: "Resource Class", slug: "resource-class"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			limits := scheduler.AdmissionLimits{
				Global: 4, PersonalWorkspace: 4, WorkerClass: 4, ResourceClass: 4,
			}
			switch testCase.name {
			case "global":
				limits.Global = 1
			case "Personal Workspace":
				limits.PersonalWorkspace = 1
			case "worker class":
				limits.WorkerClass = 1
			case "Resource Class":
				limits.ResourceClass = 1
			}
			system := newPostgresRuntimeAdmissionSystemWithLimits(
				t, now, 10*time.Minute, limits, 4, temporary, nil,
			)
			firstOwner := taskorchestration.NewUserAuthority(
				authorityID(t, "limit-first-owner-"+testCase.slug), taskorchestration.AuthorizationGeneration(1),
			)
			first := system.enqueueAndAdmitRuntimeForOwner(t, "limit-first-"+testCase.slug, firstOwner)
			accepted, err := system.runtime.Execute(context.Background(), first.start)
			if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
				t.Fatalf("bind first logical reservation: decision=%+v err=%v", accepted, err)
			}
			secondOwner := taskorchestration.NewUserAuthority(
				authorityID(t, "limit-second-owner-"+testCase.slug), taskorchestration.AuthorizationGeneration(1),
			)
			if testCase.name == "Personal Workspace" {
				secondOwner = firstOwner
			}
			system.enqueueRuntimeForOwner(t, "limit-second-"+testCase.slug, secondOwner)
			_, err = system.scheduling.ClaimAndAdmit(context.Background())
			assertSchedulerErrorCode(t, err, scheduler.ErrorNoEligibleWork)
			view, inspectErr := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, first))
			if inspectErr != nil || view.Grant.State != scheduler.GrantBound ||
				view.LogicalReservation != scheduler.ReservationBound {
				t.Fatalf("oversubscription rejection changed first reservation: view=%+v err=%v", view, inspectErr)
			}
		})
	}
}

func TestPostgresSchedulerAlternatesEqualWeightPersonalWorkspaces(t *testing.T) {
	now := time.Date(2026, time.July, 29, 11, 59, 0, 0, time.UTC)
	temporary := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		}, nil
	})
	system := newPostgresRuntimeAdmissionSystemWithLimits(
		t, now, 10*time.Minute,
		scheduler.AdmissionLimits{Global: 4, PersonalWorkspace: 4, WorkerClass: 4, ResourceClass: 4},
		4, temporary, nil,
	)
	ownerA := taskorchestration.NewUserAuthority(
		authorityID(t, "fairness-owner-a"), taskorchestration.AuthorizationGeneration(1),
	)
	ownerB := taskorchestration.NewUserAuthority(
		authorityID(t, "fairness-owner-b"), taskorchestration.AuthorizationGeneration(1),
	)
	for _, queued := range []struct {
		suffix string
		owner  taskorchestration.UserAuthority
	}{
		{suffix: "fairness-a1", owner: ownerA},
		{suffix: "fairness-a2", owner: ownerA},
		{suffix: "fairness-b1", owner: ownerB},
		{suffix: "fairness-b2", owner: ownerB},
	} {
		system.enqueueRuntimeForOwner(t, queued.suffix, queued.owner)
	}

	wantTasks := []string{
		"postgres-prelease-fairness-a1",
		"postgres-prelease-fairness-b1",
		"postgres-prelease-fairness-a2",
		"postgres-prelease-fairness-b2",
	}
	var admitted []string
	for range wantTasks {
		work := system.claimRuntime(t)
		admitted = append(admitted, work.start.TaskID.String())
		accepted, err := system.runtime.Execute(context.Background(), work.start)
		if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
			t.Fatalf("bind fair admission %s: decision=%+v err=%v", work.start.TaskID.String(), accepted, err)
		}
	}
	if len(admitted) != len(wantTasks) {
		t.Fatalf("admission count=%d want=%d", len(admitted), len(wantTasks))
	}
	for index := range wantTasks {
		if admitted[index] != wantTasks[index] {
			t.Fatalf("Workspace fairness order=%v want=%v", admitted, wantTasks)
		}
	}
}

func TestPostgresCanonicalCancellationUsesControlLaneWithoutPoisoningStartAdmission(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	temporary := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		}, nil
	})
	system := newPostgresRuntimeAdmissionSystemWithLimits(
		t, now, 10*time.Minute,
		scheduler.AdmissionLimits{Global: 2, PersonalWorkspace: 2, WorkerClass: 2, ResourceClass: 2},
		2, temporary, nil,
	)
	system.enableRuntimeCancellationParticipant(t)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "canonical-cancel-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	running := system.enqueueAndAdmitRuntimeForOwner(t, "canonical-cancel-running", owner)
	accepted, err := system.runtime.Execute(context.Background(), running.start)
	if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
		t.Fatalf("accept Runtime before canonical cancellation: decision=%+v err=%v", accepted, err)
	}
	beforeCancel, err := system.tasks.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: running.taskID, Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query Task before cancellation: %v", err)
	}
	cancelHeader := intentHeader(
		t, "canonical-cancel-request", running.taskID.String(), now.Add(2*time.Second),
	)
	cancelHeader.ExpectedTaskRevision = beforeCancel.TaskRevision
	cancelDecision, err := system.tasks.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	))
	if err != nil || len(cancelDecision.EnactmentRefs) == 0 {
		t.Fatalf("Task cancellation did not enqueue canonical control work: decision=%+v err=%v", cancelDecision, err)
	}
	afterCancel, err := system.tasks.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: running.taskID, Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || afterCancel.TaskRevision != cancelDecision.AcceptedTaskRevision {
		t.Fatalf("Task Query lost accepted cancellation decision: view=%+v err=%v", afterCancel, err)
	}

	otherOwner := taskorchestration.NewUserAuthority(
		authorityID(t, "canonical-cancel-other-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	system.enqueueRuntimeForOwner(t, "canonical-cancel-other-start", otherOwner)
	otherStart := system.claimRuntime(t)
	if otherStart.start.TaskID.String() != "postgres-prelease-canonical-cancel-other-start" {
		t.Fatalf("cancellation poisoned Start admission: admitted=%s", otherStart.start.TaskID.String())
	}

	cancellation, err := system.scheduling.ClaimCancellation(context.Background())
	if err != nil {
		t.Fatalf("claim canonical cancellation control work: %v", err)
	}
	cancel, err := runtimeexecution.ParseCanonicalCancelPayload(
		cancellation.CanonicalPayload, runtimeexecution.Digest(cancellation.PayloadDigest),
	)
	if err != nil || cancel.RuntimeRunID != running.start.RuntimeRunID ||
		cancel.PersonalWorkspaceID != running.start.PersonalWorkspaceID {
		t.Fatalf("Scheduler changed canonical cancellation: cancel=%+v err=%v", cancel, err)
	}
	cancelled, err := system.runtime.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled {
		t.Fatalf("execute canonical cancellation: decision=%+v err=%v", cancelled, err)
	}
	inspected := inspectPostgresRuntime(t, system.runtime, running.start)
	if inspected != cancelled.Snapshot {
		t.Fatalf("C03 Inspect lost canonical cancellation: got=%+v want=%+v", inspected, cancelled.Snapshot)
	}
	cancelRef := scheduler.WorkItemRef{
		WorkItemID: cancellation.WorkItemID,
		Scope:      scheduler.NewOwnerWorkItemQueryScope(cancellation.PersonalWorkspaceID),
	}
	cancelView, err := system.scheduling.Inspect(context.Background(), cancelRef)
	if err != nil || cancelView.State != scheduler.WorkItemAccepted ||
		cancelView.Grant != (scheduler.GrantView{}) || cancelView.LogicalReservation != 0 ||
		cancelView.SelectedNodeReservation != 0 {
		t.Fatalf("Scheduler did not atomically consume cancellation Work Item: view=%+v err=%v", cancelView, err)
	}
}

func TestPostgresBoundSelectedNodeReservationBlocksAnotherAdmission(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	system := newPostgresRuntimeAdmissionSystem(t, now,
		runtimeexecution.LeaseAcquisitionAdapterFunc(func(
			context.Context,
			runtimeexecution.LeaseAcquisitionRequest,
		) (runtimeexecution.LeaseAcquisitionObservation, error) {
			return runtimeexecution.LeaseAcquisitionObservation{
				Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
			}, nil
		}), nil)

	first := system.enqueueAndAdmitRuntime(t, "bound-capacity-first")
	accepted, err := system.runtime.Execute(context.Background(), first.start)
	if err != nil {
		t.Fatalf("accept first Runtime Start: %v", err)
	}
	if accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
		t.Fatalf("first Runtime did not retain its bound reservation: %+v", accepted.Snapshot)
	}
	firstView, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, first))
	if err != nil {
		t.Fatalf("inspect first bound Work Item: %v", err)
	}
	if firstView.Grant.State != scheduler.GrantBound ||
		firstView.SelectedNodeReservation != scheduler.ReservationBound {
		t.Fatalf("first admission did not remain bound: %+v", firstView)
	}

	system.enqueueRuntime(t, "bound-capacity-second")
	_, err = system.scheduling.ClaimAndAdmit(context.Background())
	var schedulingError *scheduler.Error
	if !errors.As(err, &schedulingError) || schedulingError.Code() != scheduler.ErrorNoEligibleWork {
		t.Fatalf("second admission while the only node slot is Bound = %T %v, want no eligible work", err, err)
	}
}

func TestPostgresStartAcceptedBoundFaultMatrixIsAtomic(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 30, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name      string
		fault     runtimeexecution.PersistenceFaultPoint
		wantCode  runtimeexecution.ErrorCode
		committed bool
	}{
		{name: "before Runtime write", fault: runtimeexecution.PersistenceFaultBeforeRuntimeWrite, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before Decision", fault: runtimeexecution.PersistenceFaultBeforeDecision, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before mandatory audit", fault: runtimeexecution.PersistenceFaultBeforeMandatoryAudit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after mandatory audit", fault: runtimeexecution.PersistenceFaultAfterMandatoryAudit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before owned outbox", fault: runtimeexecution.PersistenceFaultBeforeOutbox, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before commit", fault: runtimeexecution.PersistenceFaultBeforeCommit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after commit", fault: runtimeexecution.PersistenceFaultAfterCommit, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
		{name: "before response", fault: runtimeexecution.PersistenceFaultBeforeResponse, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			faults := &runtimeexecution.PersistenceFaultController{}
			adapter := &controlledLeaseAcquisitionAdapter{
				observation: runtimeexecution.LeaseAcquisitionObservation{
					Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
				},
			}
			system := newPostgresRuntimeAdmissionSystem(t, now, adapter, faults)
			work := system.enqueueAndAdmitRuntime(t, "start-atomic-fault")
			before, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
			if err != nil || before.State != scheduler.WorkItemDelivering ||
				before.Grant.State != scheduler.GrantReservedUnbound ||
				before.LogicalReservation != scheduler.ReservationReservedUnbound ||
				before.SelectedNodeReservation != scheduler.ReservationReservedUnbound {
				t.Fatalf("precondition is not Delivering/ReservedUnbound: view=%+v err=%v", before, err)
			}
			if err := faults.FailNextAt(testCase.fault); err != nil {
				t.Fatal(err)
			}
			_, err = system.runtime.Execute(context.Background(), work.start)
			assertRuntimeExecutionErrorCode(t, err, testCase.wantCode)

			after, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
			if err != nil {
				t.Fatalf("inspect Scheduler after Start fault: %v", err)
			}
			runtimeSnapshot, runtimeErr := system.runtime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
				SchemaVersion:       runtimeexecution.SchemaV1,
				ProjectionVersion:   runtimeexecution.SnapshotSchemaCurrent,
				PersonalWorkspaceID: work.start.PersonalWorkspaceID,
				RuntimeRunID:        work.start.RuntimeRunID,
				Authority:           work.start.Authority,
			})
			if testCase.committed {
				if runtimeErr != nil {
					t.Fatalf("committed Start was not inspectable: %v", runtimeErr)
				}
				assertPostgresStartAcceptedAndBound(t, system, work, runtimeSnapshot)
			} else {
				assertRuntimeExecutionErrorCode(t, runtimeErr, runtimeexecution.ErrorAuthorizationDenied)
				if runtimeSnapshot != (runtimeexecution.RuntimeSnapshot{}) ||
					after.State != scheduler.WorkItemDelivering ||
					after.Grant.State != scheduler.GrantReservedUnbound ||
					after.LogicalReservation != scheduler.ReservationReservedUnbound ||
					after.SelectedNodeReservation != scheduler.ReservationReservedUnbound {
					t.Fatalf("pre-commit Start fault left Accepted/Bound half-state: scheduler=%+v runtime=%+v",
						after, runtimeSnapshot)
				}
			}

			restarted := system.restartRuntime(t)
			replayed, err := restarted.Execute(context.Background(), work.start)
			if err != nil {
				t.Fatalf("reconcile exact Start after restart: %v", err)
			}
			assertPostgresStartAcceptedAndBound(t, system, work, replayed.Snapshot)
			if replayed.Fact.OperationID != work.start.OperationID ||
				replayed.Fact.CanonicalRequestDigest != work.start.CanonicalRequestDigest {
				t.Fatalf("restart did not retain the exact Start Decision: %+v", replayed.Fact)
			}
			replayedAgain, err := restarted.Execute(context.Background(), work.start)
			if err != nil || replayedAgain.Fact != replayed.Fact ||
				replayedAgain.Snapshot.Operation != replayed.Snapshot.Operation ||
				replayedAgain.Snapshot.Lease != replayed.Snapshot.Lease {
				t.Fatalf("exact Start replay changed Decision or binding: first=%+v second=%+v err=%v",
					replayed, replayedAgain, err)
			}
		})
	}
}

func TestPostgresStartRequestLookupFaultPreservesOriginalDecision(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 40, 0, 0, time.UTC)
	faults := &runtimeexecution.PersistenceFaultController{}
	adapter := &controlledLeaseAcquisitionAdapter{
		observation: runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		},
	}
	system := newPostgresRuntimeAdmissionSystem(t, now, adapter, faults)
	work := system.enqueueAndAdmitRuntime(t, "start-request-lookup-fault")
	accepted, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("accept Start before request lookup fault: %v", err)
	}
	callsBeforeFault := adapter.CallCount()
	if err := faults.FailNextAt(runtimeexecution.PersistenceFaultBeforeRequestLookup); err != nil {
		t.Fatal(err)
	}
	_, err = system.runtime.Execute(context.Background(), work.start)
	assertRuntimeExecutionErrorCode(t, err, runtimeexecution.ErrorDependencyUnavailable)
	if after := inspectPostgresRuntime(t, system.runtime, work.start); after != accepted.Snapshot ||
		adapter.CallCount() != callsBeforeFault {
		t.Fatalf("request lookup fault changed accepted authority or reached adapter: snapshot=%+v calls=%d/%d",
			after, adapter.CallCount(), callsBeforeFault)
	}
	restarted := system.restartRuntime(t)
	replayed, err := restarted.Execute(context.Background(), work.start)
	if err != nil || replayed.Fact != accepted.Fact || replayed.Snapshot != accepted.Snapshot {
		t.Fatalf("restart did not replay original Decision after request lookup fault: replay=%+v err=%v",
			replayed, err)
	}
}

func TestPostgresAcceptedBoundGrantCannotRotateRequeueOrRebind(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 45, 0, 0, time.UTC)
	adapter := &controlledLeaseAcquisitionAdapter{
		observation: runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		},
	}
	system := newPostgresRuntimeAdmissionSystemWithGrantTTL(t, now, 2*time.Minute, adapter, nil)
	first := system.enqueueAndAdmitRuntime(t, "accepted-bound-conflict")
	system.clock.Set(first.canonical.Grant.ExpiresAt.Add(time.Second))
	rotated, err := system.scheduling.ClaimAndAdmit(context.Background())
	if err != nil || rotated.Grant.Generation != first.canonical.Grant.Generation+1 ||
		rotated.WorkItemID != first.canonical.WorkItemID ||
		rotated.Grant.AdmissionGrantID == first.canonical.Grant.AdmissionGrantID {
		t.Fatalf("unbound expiry did not rotate the exact Work Item grant: first=%+v rotated=%+v err=%v",
			first.canonical, rotated, err)
	}
	grantID, err := runtimeexecution.NewAdmissionGrantID(rotated.Grant.AdmissionGrantID.String())
	if err != nil {
		t.Fatal(err)
	}
	workItemID, err := runtimeexecution.NewWorkItemID(rotated.WorkItemID.String())
	if err != nil {
		t.Fatal(err)
	}
	start, err := runtimeexecution.BindCanonicalStartPayload(
		rotated.CanonicalPayload,
		runtimeexecution.Digest(rotated.PayloadDigest),
		runtimeexecution.AdmissionGrantProof{
			AdmissionGrantID: grantID,
			WorkItemID:       workItemID,
			Generation:       runtimeexecution.AdmissionGrantGeneration(rotated.Grant.Generation),
		},
	)
	if err != nil {
		t.Fatalf("bind rotated grant to canonical Start: %v", err)
	}
	work := admittedRuntimeWork{
		owner: first.owner, taskID: first.taskID, canonical: rotated, start: start,
	}
	accepted, err := system.runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("accept rotated grant: %v", err)
	}
	assertPostgresStartAcceptedAndBound(t, system, work, accepted.Snapshot)
	acceptedView, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := system.runtime.Execute(context.Background(), start)
	if err != nil || replayed.Fact != accepted.Fact || replayed.Snapshot != accepted.Snapshot {
		t.Fatalf("same exact grant generation did not replay: accepted=%+v replay=%+v err=%v",
			accepted, replayed, err)
	}
	conflictingGrantID, err := runtimeexecution.NewAdmissionGrantID("conflicting-accepted-grant")
	if err != nil {
		t.Fatal(err)
	}
	conflictingWorkItemID, err := runtimeexecution.NewWorkItemID("conflicting-accepted-work-item")
	if err != nil {
		t.Fatal(err)
	}
	higherStart := start
	higherStart.AdmissionGrant = runtimeexecution.AdmissionGrantProof{
		AdmissionGrantID: conflictingGrantID,
		WorkItemID:       start.AdmissionGrant.WorkItemID,
		Generation:       start.AdmissionGrant.Generation + 1,
	}
	higherReplay, err := system.runtime.Execute(context.Background(), higherStart)
	if err != nil || higherReplay.Fact != accepted.Fact || higherReplay.Snapshot != accepted.Snapshot {
		t.Fatalf("newer redundant grant did not return the original Decision: replay=%+v err=%v",
			higherReplay, err)
	}
	for _, conflict := range []struct {
		name  string
		proof runtimeexecution.AdmissionGrantProof
	}{
		{
			name: "stale generation",
			proof: runtimeexecution.AdmissionGrantProof{
				AdmissionGrantID: start.AdmissionGrant.AdmissionGrantID,
				WorkItemID:       start.AdmissionGrant.WorkItemID,
				Generation:       start.AdmissionGrant.Generation - 1,
			},
		},
		{
			name: "mismatched grant ID",
			proof: runtimeexecution.AdmissionGrantProof{
				AdmissionGrantID: conflictingGrantID,
				WorkItemID:       start.AdmissionGrant.WorkItemID,
				Generation:       start.AdmissionGrant.Generation,
			},
		},
		{
			name: "mismatched Work Item",
			proof: runtimeexecution.AdmissionGrantProof{
				AdmissionGrantID: start.AdmissionGrant.AdmissionGrantID,
				WorkItemID:       conflictingWorkItemID,
				Generation:       start.AdmissionGrant.Generation,
			},
		},
	} {
		t.Run(conflict.name, func(t *testing.T) {
			conflictingStart := start
			conflictingStart.AdmissionGrant = conflict.proof
			_, err := system.runtime.Execute(context.Background(), conflictingStart)
			assertRuntimeExecutionErrorCode(t, err, runtimeexecution.ErrorIntegrityConflict)
			afterRuntime := inspectPostgresRuntime(t, system.runtime, start)
			afterView, inspectErr := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
			if inspectErr != nil || afterRuntime != accepted.Snapshot || afterView != acceptedView {
				t.Fatalf("grant conflict changed original Accepted/Bound authority: runtime=%+v scheduler=%+v err=%v",
					afterRuntime, afterView, inspectErr)
			}
		})
	}

	system.clock.Set(rotated.Grant.ExpiresAt.Add(time.Second))
	_, err = system.scheduling.ClaimAndAdmit(context.Background())
	var schedulingError *scheduler.Error
	if !errors.As(err, &schedulingError) || schedulingError.Code() != scheduler.ErrorNoEligibleWork {
		t.Fatalf("Accepted Work Item was eligible for requeue/re-admission after expiry: %T %v", err, err)
	}
	afterExpiryRuntime := inspectPostgresRuntime(t, system.runtime, start)
	afterExpiryView, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil || afterExpiryRuntime != accepted.Snapshot || afterExpiryView != acceptedView ||
		afterExpiryView.State != scheduler.WorkItemAccepted || afterExpiryView.Grant.State != scheduler.GrantBound {
		t.Fatalf("accepted grant expiry unbound, requeued, rebound, or re-admitted work: runtime=%+v scheduler=%+v err=%v",
			afterExpiryRuntime, afterExpiryView, err)
	}
}

func TestPostgresPostBindPreLeaseMatrixAndCapacityEvidenceSeparation(t *testing.T) {
	now := time.Date(2026, time.July, 29, 13, 0, 0, 0, time.UTC)
	testCases := []struct {
		name          string
		observation   runtimeexecution.LeaseAcquisitionObservation
		wantState     runtimeexecution.RuntimeState
		wantOutcome   runtimeexecution.RuntimeOutcome
		wantReason    runtimeexecution.PreLeaseTerminalReason
		wantLease     runtimeexecution.LeaseAcquireStatus
		wantReconcile runtimeexecution.ReconciliationStatus
		wantTerminal  bool
	}{
		{
			name: "temporary same-node-generation unavailable",
			observation: runtimeexecution.LeaseAcquisitionObservation{
				Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
			},
			wantState:     runtimeexecution.RuntimeWaitingForLease,
			wantLease:     runtimeexecution.LeaseAcquirePending,
			wantReconcile: runtimeexecution.ReconciliationStable,
		},
		{
			name: "retryable Reservation prerequisite",
			observation: runtimeexecution.LeaseAcquisitionObservation{
				Disposition: runtimeexecution.LeaseAcquisitionRetryablePrerequisite,
			},
			wantState:     runtimeexecution.RuntimeReconciling,
			wantLease:     runtimeexecution.LeaseAcquireReconciliationRequired,
			wantReconcile: runtimeexecution.ReconciliationRequiredStatus,
		},
		{
			name: "ambiguous authorization prerequisite",
			observation: runtimeexecution.LeaseAcquisitionObservation{
				Disposition: runtimeexecution.LeaseAcquisitionAmbiguousPrerequisite,
			},
			wantState:     runtimeexecution.RuntimeReconciling,
			wantLease:     runtimeexecution.LeaseAcquireReconciliationRequired,
			wantReconcile: runtimeexecution.ReconciliationRequiredStatus,
		},
		postgresPermanentPreLeaseCase("stale node generation",
			runtimeexecution.PreLeasePermanentStaleNodeGeneration,
			runtimeexecution.PreLeaseTerminalStaleNodeGeneration),
		postgresPermanentPreLeaseCase("permanently ineligible node",
			runtimeexecution.PreLeasePermanentNodeIneligible,
			runtimeexecution.PreLeaseTerminalNodeIneligible),
		postgresPermanentPreLeaseCase("permanent Reservation failure",
			runtimeexecution.PreLeasePermanentReservation,
			runtimeexecution.PreLeaseTerminalReservation),
		postgresPermanentPreLeaseCase("permanent authorization failure",
			runtimeexecution.PreLeasePermanentAuthorization,
			runtimeexecution.PreLeaseTerminalAuthorization),
		postgresPermanentPreLeaseCase("Resource Class mismatch",
			runtimeexecution.PreLeasePermanentResourceClass,
			runtimeexecution.PreLeaseTerminalResourceClass),
		postgresPermanentPreLeaseCase("Execution Policy mismatch",
			runtimeexecution.PreLeasePermanentExecutionPolicy,
			runtimeexecution.PreLeaseTerminalExecutionPolicy),
		postgresPermanentPreLeaseCase("Scheduler policy mismatch",
			runtimeexecution.PreLeasePermanentSchedulerPolicy,
			runtimeexecution.PreLeaseTerminalSchedulerPolicy),
		postgresPermanentPreLeaseCase("Scheduler epoch mismatch",
			runtimeexecution.PreLeasePermanentSchedulerEpoch,
			runtimeexecution.PreLeaseTerminalSchedulerEpoch),
		postgresPermanentPreLeaseCase("release safety mismatch",
			runtimeexecution.PreLeasePermanentReleaseSafety,
			runtimeexecution.PreLeaseTerminalReleaseSafety),
		postgresPermanentPreLeaseCase("catalog safety mismatch",
			runtimeexecution.PreLeasePermanentCatalogSafety,
			runtimeexecution.PreLeaseTerminalCatalogSafety),
		postgresPermanentPreLeaseCase("immutable binding mismatch",
			runtimeexecution.PreLeasePermanentImmutableBinding,
			runtimeexecution.PreLeaseTerminalImmutableBinding),
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			adapter := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
				context.Context,
				runtimeexecution.LeaseAcquisitionRequest,
			) (runtimeexecution.LeaseAcquisitionObservation, error) {
				return testCase.observation, nil
			})
			system := newPostgresRuntimeAdmissionSystem(t, now, adapter, nil)
			work := system.enqueueAndAdmitRuntime(t, "matrix")

			decision, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil {
				t.Fatalf("execute Start through C03: %v", err)
			}
			if decision.Fact.Disposition != runtimeexecution.DecisionAccepted ||
				decision.Fact.OperationID != work.start.OperationID ||
				decision.Fact.CanonicalRequestDigest != work.start.CanonicalRequestDigest ||
				decision.Snapshot.State != testCase.wantState ||
				decision.Snapshot.Outcome != testCase.wantOutcome ||
				decision.Snapshot.PreLeaseTerminalReason != testCase.wantReason ||
				decision.Snapshot.Lease.AcquireStatus != testCase.wantLease ||
				decision.Snapshot.Reconciliation != testCase.wantReconcile {
				t.Fatalf("pre-lease result = %+v", decision)
			}
			assertPostgresStartAcceptedAndBound(t, system, work, decision.Snapshot)

			inspected := inspectPostgresRuntime(t, system.runtime, work.start)
			if inspected != decision.Snapshot {
				t.Fatalf("Inspect result differs from Execute: inspected=%+v execute=%+v", inspected, decision.Snapshot)
			}
			restarted, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
				Schema: system.schema, Now: system.clock.Now,
				SchedulerParticipant:        system.scheduling.RuntimeAcceptanceParticipant(),
				SchedulerAcceptanceFunction: system.scheduling.RuntimeAcceptanceFunction(),
				LeaseAcquisition:            adapter,
			})
			if err != nil {
				t.Fatalf("restart Runtime Execution: %v", err)
			}
			if restored := inspectPostgresRuntime(t, restarted, work.start); restored != decision.Snapshot {
				t.Fatalf("restart reconstructed %+v, want %+v", restored, decision.Snapshot)
			}

			replayed, err := restarted.Execute(context.Background(), work.start)
			if err != nil || replayed.Fact != decision.Fact || replayed.Snapshot != decision.Snapshot {
				t.Fatalf("exact Start replay changed authority: replay=%+v err=%v", replayed, err)
			}
			if testCase.wantTerminal {
				assertAndConsumePostgresNoLeaseEvidence(t, system, work, decision.Snapshot)
			} else if decision.Snapshot.CapacityEvidence != (runtimeexecution.RuntimeCapacityEvidenceSnapshot{}) ||
				decision.Snapshot.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityHeld ||
				decision.Snapshot.Capacity.NoLease != runtimeexecution.NoLeaseDispositionNone ||
				decision.Snapshot.Capacity.Physical != runtimeexecution.PhysicalCapacityNotApplicable {
				t.Fatalf("non-terminal pre-lease row released capacity: %+v", decision.Snapshot)
			}
		})
	}
}

func TestPostgresPreLeaseAuthorityExpiryDeadlineAndCancelAreDistinct(t *testing.T) {
	now := time.Date(2026, time.July, 29, 14, 0, 0, 0, time.UTC)
	temporary := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		}, nil
	})

	for _, testCase := range []struct {
		name        string
		grantTTL    time.Duration
		advanceTo   func(runtimeexecution.RuntimeSnapshot) time.Time
		wantOutcome runtimeexecution.RuntimeOutcome
		wantReason  runtimeexecution.PreLeaseTerminalReason
	}{
		{
			name:     "bound authority expires before Runtime deadline",
			grantTTL: 10 * time.Minute,
			advanceTo: func(snapshot runtimeexecution.RuntimeSnapshot) time.Time {
				return snapshot.LeaseAcquireBy
			},
			wantOutcome: runtimeexecution.RuntimeRejected,
			wantReason:  runtimeexecution.PreLeaseTerminalAdmissionAuthorityExpired,
		},
		{
			name:     "Runtime deadline wins when grant authority reaches the same cap",
			grantTTL: time.Hour,
			advanceTo: func(snapshot runtimeexecution.RuntimeSnapshot) time.Time {
				return snapshot.Deadline
			},
			wantOutcome: runtimeexecution.RuntimeTimedOut,
			wantReason:  runtimeexecution.PreLeaseTerminalRuntimeDeadline,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			system := newPostgresRuntimeAdmissionSystemWithGrantTTL(
				t, now, testCase.grantTTL, temporary, nil,
			)
			work := system.enqueueAndAdmitRuntime(t, "clock-terminal")
			accepted, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
				t.Fatalf("accept waiting Runtime: decision=%+v err=%v", accepted, err)
			}
			leaseOperation := accepted.Snapshot.Lease.AcquireOperationID
			leaseDigest := accepted.Snapshot.Lease.AcquireDigest
			system.clock.Set(testCase.advanceTo(accepted.Snapshot))
			terminal, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil || terminal.Fact != accepted.Fact ||
				terminal.Snapshot.Outcome != testCase.wantOutcome ||
				terminal.Snapshot.PreLeaseTerminalReason != testCase.wantReason ||
				terminal.Snapshot.Lease.AcquireOperationID != leaseOperation ||
				terminal.Snapshot.Lease.AcquireDigest != leaseDigest {
				t.Fatalf("clock terminal = %+v err=%v", terminal, err)
			}
			assertPostgresStartAcceptedAndBound(t, system, work, terminal.Snapshot)
			assertAndConsumePostgresNoLeaseEvidence(t, system, work, terminal.Snapshot)
		})
	}

	t.Run("authorized cancel", func(t *testing.T) {
		system := newPostgresRuntimeAdmissionSystem(t, now, temporary, nil)
		work := system.enqueueAndAdmitRuntime(t, "cancel-terminal")
		accepted, err := system.runtime.Execute(context.Background(), work.start)
		if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
			t.Fatalf("accept waiting Runtime: decision=%+v err=%v", accepted, err)
		}
		cancelOperationID, err := runtimeexecution.NewOperationID("cancel-" + work.start.OperationID.String())
		if err != nil {
			t.Fatal(err)
		}
		cancel, err := runtimeexecution.NewCancelRuntimeRun(runtimeexecution.CancelRuntimeRunInput{
			SchemaVersion:               runtimeexecution.SchemaV1,
			OperationID:                 cancelOperationID,
			PersonalWorkspaceID:         work.start.PersonalWorkspaceID,
			TaskID:                      work.start.TaskID,
			PhaseRunID:                  work.start.PhaseRunID,
			RuntimeRunID:                work.start.RuntimeRunID,
			ExpectedRuntimeRevision:     accepted.Snapshot.RuntimeRevision,
			ExpectedStartOperationID:    work.start.OperationID,
			ExpectedOperationGeneration: accepted.Snapshot.Operation.Generation,
			ExpectedRuntimeFence:        accepted.Snapshot.RuntimeFence,
			Authority:                   work.start.Authority,
			Reason:                      runtimeexecution.CancellationUserRequested,
			SafetyEpoch:                 work.start.ReleaseSafetyEpoch,
			OccurredAt:                  now.Add(2 * time.Minute),
		})
		if err != nil {
			t.Fatalf("construct pre-lease Cancel: %v", err)
		}
		cancelled, err := system.runtime.Execute(context.Background(), cancel)
		if err != nil || cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled ||
			cancelled.Snapshot.PreLeaseTerminalReason != runtimeexecution.PreLeaseTerminalNone ||
			cancelled.Snapshot.Cancellation.Status != runtimeexecution.CancellationAccepted ||
			cancelled.Snapshot.Cancellation.OperationID != cancel.OperationID {
			t.Fatalf("pre-lease Cancel = %+v err=%v", cancelled, err)
		}
		replayedCancel, err := system.runtime.Execute(context.Background(), cancel)
		if err != nil || replayedCancel != cancelled {
			t.Fatalf("Cancel replay = %+v err=%v, want %+v", replayedCancel, err, cancelled)
		}
		replayedStart, err := system.runtime.Execute(context.Background(), work.start)
		if err != nil || replayedStart.Fact != accepted.Fact || replayedStart.Snapshot != cancelled.Snapshot {
			t.Fatalf("Start replay after Cancel = %+v err=%v", replayedStart, err)
		}
		assertPostgresStartAcceptedAndBound(t, system, work, cancelled.Snapshot)
		assertAndConsumePostgresNoLeaseEvidence(t, system, work, cancelled.Snapshot)
	})
}

func TestPostgresBindCancelAndDeadlineRacesUseOneAcceptedBinding(t *testing.T) {
	now := time.Date(2026, time.July, 29, 14, 30, 0, 0, time.UTC)
	temporary := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		}, nil
	})
	type executeResult struct {
		decision runtimeexecution.RuntimeDecision
		err      error
	}

	t.Run("bind and cancel", func(t *testing.T) {
		system := newPostgresRuntimeAdmissionSystem(t, now, temporary, nil)
		work := system.enqueueAndAdmitRuntime(t, "bind-cancel-race")
		barrier := &schedulerAcceptanceBarrier{
			delegate: system.scheduling.RuntimeAcceptanceParticipant(),
			entered:  make(chan struct{}), release: make(chan struct{}),
		}
		system.replaceRuntimeSchedulerParticipant(t, barrier)
		cancel := newPostgresPreLeaseCancel(t, work.start,
			work.start.ExpectedRuntimeRevision+1,
			work.start.ExpectedOperationGeneration+1,
			work.start.ExpectedRuntimeFence+1,
			now.Add(time.Minute),
		)

		bindResult := make(chan executeResult, 1)
		go func() {
			decision, err := system.runtime.Execute(context.Background(), work.start)
			bindResult <- executeResult{decision: decision, err: err}
		}()
		<-barrier.entered
		_, err := system.runtime.Execute(context.Background(), cancel)
		assertRuntimeExecutionErrorCode(t, err, runtimeexecution.ErrorAuthorizationDenied)
		close(barrier.release)
		bound := <-bindResult
		if bound.err != nil || bound.decision.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
			t.Fatalf("bind side of cancel race did not commit exactly once: result=%+v", bound)
		}
		cancelled, err := system.runtime.Execute(context.Background(), cancel)
		if err != nil || cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled ||
			cancelled.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseNotRequested {
			t.Fatalf("durable cancel did not converge after bind: decision=%+v err=%v", cancelled, err)
		}
		assertPostgresStartAcceptedAndBound(t, system, work, cancelled.Snapshot)
		assertAndConsumePostgresNoLeaseEvidence(t, system, work, cancelled.Snapshot)
	})

	t.Run("bind and deadline", func(t *testing.T) {
		system := newPostgresRuntimeAdmissionSystem(t, now, temporary, nil)
		work := system.enqueueAndAdmitRuntime(t, "bind-deadline-race")
		barrier := &schedulerAcceptanceBarrier{
			delegate: system.scheduling.RuntimeAcceptanceParticipant(),
			entered:  make(chan struct{}), release: make(chan struct{}),
		}
		system.replaceRuntimeSchedulerParticipant(t, barrier)
		bindResult := make(chan executeResult, 1)
		go func() {
			decision, err := system.runtime.Execute(context.Background(), work.start)
			bindResult <- executeResult{decision: decision, err: err}
		}()
		<-barrier.entered
		system.clock.Set(work.start.Deadline)
		close(barrier.release)
		result := <-bindResult
		if result.err != nil || result.decision.Fact.OperationID != work.start.OperationID ||
			result.decision.Snapshot.Outcome != runtimeexecution.RuntimeTimedOut ||
			result.decision.Snapshot.PreLeaseTerminalReason != runtimeexecution.PreLeaseTerminalRuntimeDeadline ||
			result.decision.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseNotRequested {
			t.Fatalf("deadline did not converge behind the accepted bind: result=%+v", result)
		}
		assertPostgresStartAcceptedAndBound(t, system, work, result.decision.Snapshot)
		assertAndConsumePostgresNoLeaseEvidence(t, system, work, result.decision.Snapshot)
	})
}

func TestPostgresLeaseCancelAndDeadlineRacesCommitZeroOrOneLease(t *testing.T) {
	now := time.Date(2026, time.July, 29, 14, 45, 0, 0, time.UTC)
	type executeResult struct {
		decision runtimeexecution.RuntimeDecision
		err      error
	}

	t.Run("lease and cancel", func(t *testing.T) {
		barrier := &secondCallLeaseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
		system := newPostgresRuntimeAdmissionSystem(t, now, barrier, nil)
		work := system.enqueueAndAdmitRuntime(t, "lease-cancel-race")
		accepted, err := system.runtime.Execute(context.Background(), work.start)
		if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
			t.Fatalf("accept Runtime before lease/cancel race: decision=%+v err=%v", accepted, err)
		}
		cancel := newPostgresPreLeaseCancel(t, work.start,
			accepted.Snapshot.RuntimeRevision,
			accepted.Snapshot.Operation.Generation,
			accepted.Snapshot.RuntimeFence,
			now.Add(time.Minute),
		)
		leaseResult := make(chan executeResult, 1)
		go func() {
			decision, err := system.runtime.Execute(context.Background(), work.start)
			leaseResult <- executeResult{decision: decision, err: err}
		}()
		<-barrier.entered
		cancelled, err := system.runtime.Execute(context.Background(), cancel)
		if err != nil || cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled {
			t.Fatalf("cancel did not win while lease observation was in flight: decision=%+v err=%v", cancelled, err)
		}
		close(barrier.release)
		leaseAttempt := <-leaseResult
		if leaseAttempt.err != nil || leaseAttempt.decision.Fact != accepted.Fact ||
			leaseAttempt.decision.Snapshot != cancelled.Snapshot ||
			leaseAttempt.decision.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseNotRequested {
			t.Fatalf("late lease observation created a second outcome: result=%+v", leaseAttempt)
		}
		assertPostgresStartAcceptedAndBound(t, system, work, cancelled.Snapshot)
		assertAndConsumePostgresNoLeaseEvidence(t, system, work, cancelled.Snapshot)
	})

	t.Run("lease and deadline", func(t *testing.T) {
		barrier := &secondCallLeaseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
		system := newPostgresRuntimeAdmissionSystem(t, now, barrier, nil)
		work := system.enqueueAndAdmitRuntime(t, "lease-deadline-race")
		accepted, err := system.runtime.Execute(context.Background(), work.start)
		if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
			t.Fatalf("accept Runtime before lease/deadline race: decision=%+v err=%v", accepted, err)
		}
		leaseResult := make(chan executeResult, 1)
		go func() {
			decision, err := system.runtime.Execute(context.Background(), work.start)
			leaseResult <- executeResult{decision: decision, err: err}
		}()
		<-barrier.entered
		system.clock.Set(accepted.Snapshot.Deadline)
		deadline, err := system.runtime.Execute(context.Background(), work.start)
		if err != nil || deadline.Fact != accepted.Fact ||
			deadline.Snapshot.Outcome != runtimeexecution.RuntimeTimedOut ||
			deadline.Snapshot.PreLeaseTerminalReason != runtimeexecution.PreLeaseTerminalRuntimeDeadline {
			t.Fatalf("deadline did not win while lease observation was in flight: decision=%+v err=%v", deadline, err)
		}
		close(barrier.release)
		leaseAttempt := <-leaseResult
		if leaseAttempt.err != nil || leaseAttempt.decision.Fact != accepted.Fact ||
			leaseAttempt.decision.Snapshot != deadline.Snapshot ||
			leaseAttempt.decision.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseNotRequested {
			t.Fatalf("late lease observation crossed the deadline outcome: result=%+v", leaseAttempt)
		}
		assertPostgresStartAcceptedAndBound(t, system, work, deadline.Snapshot)
		assertAndConsumePostgresNoLeaseEvidence(t, system, work, deadline.Snapshot)
	})

	for _, testCase := range []struct {
		name        string
		advanceTo   func(runtimeexecution.RuntimeSnapshot) time.Time
		wantOutcome runtimeexecution.RuntimeOutcome
		wantReason  runtimeexecution.PreLeaseTerminalReason
	}{
		{
			name: "lease observation crosses admission authority expiry",
			advanceTo: func(snapshot runtimeexecution.RuntimeSnapshot) time.Time {
				return snapshot.LeaseAcquireBy
			},
			wantOutcome: runtimeexecution.RuntimeRejected,
			wantReason:  runtimeexecution.PreLeaseTerminalAdmissionAuthorityExpired,
		},
		{
			name: "lease observation crosses Runtime deadline",
			advanceTo: func(snapshot runtimeexecution.RuntimeSnapshot) time.Time {
				return snapshot.Deadline
			},
			wantOutcome: runtimeexecution.RuntimeTimedOut,
			wantReason:  runtimeexecution.PreLeaseTerminalRuntimeDeadline,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			barrier := &secondCallLeaseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			system := newPostgresRuntimeAdmissionSystem(t, now, barrier, nil)
			work := system.enqueueAndAdmitRuntime(t, "lease-clock-linearization")
			accepted, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
				t.Fatalf("accept Runtime before clock crossing: decision=%+v err=%v", accepted, err)
			}
			leaseResult := make(chan executeResult, 1)
			go func() {
				decision, err := system.runtime.Execute(context.Background(), work.start)
				leaseResult <- executeResult{decision: decision, err: err}
			}()
			<-barrier.entered
			system.clock.Set(testCase.advanceTo(accepted.Snapshot))
			close(barrier.release)
			result := <-leaseResult
			if result.err != nil || result.decision.Fact != accepted.Fact ||
				result.decision.Snapshot.Outcome != testCase.wantOutcome ||
				result.decision.Snapshot.PreLeaseTerminalReason != testCase.wantReason ||
				result.decision.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseNotRequested {
				t.Fatalf("lease linearized after its clock authority: result=%+v", result)
			}
			assertPostgresStartAcceptedAndBound(t, system, work, result.decision.Snapshot)
			assertAndConsumePostgresNoLeaseEvidence(t, system, work, result.decision.Snapshot)
		})
	}
}

func TestPostgresSchedulerCapacityEvidenceBridgeRejectsConflictsAndSeparatesAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 29, 14, 55, 0, 0, time.UTC)
	permanent := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition:      runtimeexecution.LeaseAcquisitionPermanentFailure,
			PermanentFailure: runtimeexecution.PreLeasePermanentImmutableBinding,
		}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, permanent, nil)
	work := system.enqueueAndAdmitRuntime(t, "scheduler-evidence-conflicts")
	terminal, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil || terminal.Snapshot.Outcome != runtimeexecution.RuntimeRejected {
		t.Fatalf("create proven no-lease Runtime: decision=%+v err=%v", terminal, err)
	}
	fenced := terminal.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal
	noLease := terminal.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition
	before, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil || before.LogicalReservation != scheduler.ReservationBound ||
		before.SelectedNodeReservation != scheduler.ReservationBound || before.Grant.State != scheduler.GrantBound {
		t.Fatalf("terminal evidence precondition lost bound capacity: view=%+v err=%v", before, err)
	}
	wrongRuntimeID, err := runtimeexecution.NewRuntimeRunID("wrong-evidence-runtime")
	if err != nil {
		t.Fatal(err)
	}
	wrongWorkItemID, err := runtimeexecution.NewWorkItemID("wrong-evidence-work-item")
	if err != nil {
		t.Fatal(err)
	}
	wrongGrantID, err := runtimeexecution.NewAdmissionGrantID("wrong-evidence-grant")
	if err != nil {
		t.Fatal(err)
	}
	wrongGeneration := fenced
	wrongGeneration.GrantGeneration++
	staleFence := fenced
	staleFence.RuntimeFence--
	wrongRuntime := fenced
	wrongRuntime.RuntimeRunID = wrongRuntimeID
	wrongWorkItem := fenced
	wrongWorkItem.WorkItemID = wrongWorkItemID
	wrongGrant := fenced
	wrongGrant.AdmissionGrantID = wrongGrantID
	for _, conflict := range []struct {
		name     string
		evidence runtimeexecution.RuntimeFencedOrTerminalEvidence
	}{
		{name: "grant generation", evidence: wrongGeneration},
		{name: "stale Runtime fence", evidence: staleFence},
		{name: "Runtime", evidence: wrongRuntime},
		{name: "Work Item", evidence: wrongWorkItem},
		{name: "Admission Grant", evidence: wrongGrant},
	} {
		t.Run("reject mismatched "+conflict.name, func(t *testing.T) {
			err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(), conflict.evidence)
			assertSchedulerErrorCode(t, err, scheduler.ErrorIntegrityConflict)
			after, inspectErr := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
			if inspectErr != nil || after != before {
				t.Fatalf("conflicting logical evidence changed capacity: before=%+v after=%+v err=%v",
					before, after, inspectErr)
			}
		})
	}

	wrongNodeID, err := runtimeexecution.NewExecutionNodeID("wrong-evidence-node")
	if err != nil {
		t.Fatal(err)
	}
	wrongNode := noLease
	wrongNode.ExecutionNodeID = wrongNodeID
	err = system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), wrongNode)
	assertSchedulerErrorCode(t, err, scheduler.ErrorIntegrityConflict)
	afterWrongNode, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil || afterWrongNode != before {
		t.Fatalf("mismatched node evidence changed capacity: before=%+v after=%+v err=%v",
			before, afterWrongNode, err)
	}

	if err := system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), noLease); err != nil {
		t.Fatalf("apply exact no-lease disposition first: %v", err)
	}
	if err := system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), noLease); err != nil {
		t.Fatalf("replay exact no-lease disposition: %v", err)
	}
	physicalOnly, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil || physicalOnly.LogicalReservation != scheduler.ReservationBound ||
		physicalOnly.SelectedNodeReservation != scheduler.ReservationReleased ||
		physicalOnly.Grant.State != scheduler.GrantTerminalNoLease {
		t.Fatalf("no-lease evidence released logical counters by itself: view=%+v err=%v", physicalOnly, err)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(), fenced); err != nil {
		t.Fatalf("apply exact logical disposition second: %v", err)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(), fenced); err != nil {
		t.Fatalf("replay exact logical disposition: %v", err)
	}
	fullyReleased, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil || fullyReleased.LogicalReservation != scheduler.ReservationReleased ||
		fullyReleased.SelectedNodeReservation != scheduler.ReservationReleased ||
		fullyReleased.Grant.State != scheduler.GrantReleased {
		t.Fatalf("separate evidence did not converge idempotently: view=%+v err=%v", fullyReleased, err)
	}
}

func TestPostgresSchedulerCapacityEvidenceRetainsExactTerminalIdentityAcrossEvidenceClasses(t *testing.T) {
	now := time.Date(2026, time.July, 29, 14, 57, 0, 0, time.UTC)
	permanent := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition:      runtimeexecution.LeaseAcquisitionPermanentFailure,
			PermanentFailure: runtimeexecution.PreLeasePermanentImmutableBinding,
		}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, permanent, nil)
	work := system.enqueueAndAdmitRuntime(t, "scheduler-retained-terminal-identity")
	terminal, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil || terminal.Snapshot.Outcome != runtimeexecution.RuntimeRejected {
		t.Fatalf("create first proven no-lease Runtime: decision=%+v err=%v", terminal, err)
	}

	noLease := terminal.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition
	if err := system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), noLease); err != nil {
		t.Fatalf("retain exact no-lease terminal identity: %v", err)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(
		context.Background(), terminal.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal,
	); err != nil {
		t.Fatalf("release first exact logical terminal identity: %v", err)
	}
	foreignWork := system.enqueueAndAdmitRuntime(t, "scheduler-foreign-terminal-identity")
	foreignTerminal, err := system.runtime.Execute(context.Background(), foreignWork.start)
	if err != nil || foreignTerminal.Snapshot.Outcome != runtimeexecution.RuntimeRejected {
		t.Fatalf("create foreign proven no-lease Runtime: decision=%+v err=%v", foreignTerminal, err)
	}

	baseFenced := terminal.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal
	alteredDecision := baseFenced
	alteredDecision.TerminalDecisionID = foreignTerminal.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal.TerminalDecisionID
	alteredRevision := baseFenced
	alteredRevision.RuntimeRevision++
	alteredFence := baseFenced
	alteredFence.RuntimeFence++
	alteredEpoch := baseFenced
	alteredEpoch.SchedulerEpoch++
	alteredPolicy := baseFenced
	alteredPolicy.PolicyVersion++
	for _, testCase := range []struct {
		name     string
		evidence runtimeexecution.RuntimeFencedOrTerminalEvidence
	}{
		{name: "terminal Decision", evidence: alteredDecision},
		{name: "Runtime revision", evidence: alteredRevision},
		{name: "Runtime fence", evidence: alteredFence},
		{name: "Scheduler epoch", evidence: alteredEpoch},
		{name: "policy version", evidence: alteredPolicy},
	} {
		t.Run("logical rejects altered "+testCase.name, func(t *testing.T) {
			err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(), testCase.evidence)
			assertSchedulerErrorCode(t, err, scheduler.ErrorIntegrityConflict)
		})
	}

	baseNoLease := terminal.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition
	alteredNoLeaseDecision := baseNoLease
	alteredNoLeaseDecision.TerminalDecisionID = foreignTerminal.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal.TerminalDecisionID
	alteredNoLeaseRevision := baseNoLease
	alteredNoLeaseRevision.RuntimeRevision++
	alteredNoLeaseFence := baseNoLease
	alteredNoLeaseFence.RuntimeFence++
	alteredNoLeaseEpoch := baseNoLease
	alteredNoLeaseEpoch.SchedulerEpoch++
	alteredNoLeasePolicy := baseNoLease
	alteredNoLeasePolicy.PolicyVersion++
	for _, testCase := range []struct {
		name     string
		evidence runtimeexecution.NoLeasePhysicalDispositionEvidence
	}{
		{name: "terminal Decision", evidence: alteredNoLeaseDecision},
		{name: "Runtime revision", evidence: alteredNoLeaseRevision},
		{name: "Runtime fence", evidence: alteredNoLeaseFence},
		{name: "Scheduler epoch", evidence: alteredNoLeaseEpoch},
		{name: "policy version", evidence: alteredNoLeasePolicy},
	} {
		t.Run("no-lease rejects altered "+testCase.name, func(t *testing.T) {
			err := system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), testCase.evidence)
			assertSchedulerErrorCode(t, err, scheduler.ErrorIntegrityConflict)
		})
	}

	view, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil || view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationReleased ||
		view.Grant.State != scheduler.GrantReleased {
		t.Fatalf("altered terminal identity changed retained capacity: view=%+v err=%v", view, err)
	}
}

func TestPostgresRuntimeCodecRoundTripsAdmissionTerminalAndCapacityEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 29, 14, 58, 0, 0, time.UTC)
	adapter := &controlledLeaseAcquisitionAdapter{
		observation: runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
		},
	}
	system := newPostgresRuntimeAdmissionSystem(t, now, adapter, nil)
	work := system.enqueueAndAdmitRuntime(t, "codec-round-trip")
	accepted, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("accept Runtime for codec round trip: %v", err)
	}
	waiting := accepted.Snapshot
	if waiting.Operation.Status != runtimeexecution.OperationBound ||
		waiting.Operation.OperationID != work.start.OperationID ||
		waiting.Operation.Digest != work.start.CanonicalRequestDigest ||
		waiting.Operation.Generation != work.start.ExpectedOperationGeneration+1 ||
		waiting.Operation.AdmissionGrantID != work.start.AdmissionGrant.AdmissionGrantID ||
		waiting.Operation.WorkItemID != work.start.AdmissionGrant.WorkItemID ||
		waiting.Operation.GrantGeneration != work.start.AdmissionGrant.Generation ||
		waiting.Operation.ExecutionNodeID.String() != work.canonical.Grant.ExecutionNodeID.String() ||
		waiting.Operation.NodeCapacityGeneration != uint64(work.canonical.Grant.NodeCapacityGeneration) ||
		waiting.Operation.ResourceClassID != work.start.ResourceClassID ||
		waiting.Operation.ExecutionPolicyID != work.start.ExecutionPolicyID ||
		waiting.Operation.SchedulerEpoch != uint64(work.canonical.Grant.SchedulerEpoch) ||
		waiting.Operation.PolicyVersion != uint64(work.canonical.Grant.PolicyVersion) ||
		waiting.Lease.AcquireStatus != runtimeexecution.LeaseAcquirePending ||
		waiting.Lease.AcquireOperationID.String() == "" || waiting.Lease.AcquireDigest == (runtimeexecution.Digest{}) ||
		!waiting.Deadline.Equal(work.start.Deadline) ||
		!waiting.LeaseAcquireBy.Equal(work.canonical.Grant.ExpiresAt) ||
		waiting.CapacityEvidence != (runtimeexecution.RuntimeCapacityEvidenceSnapshot{}) {
		t.Fatalf("accepted admission binding is incomplete before round trip: %+v", waiting)
	}
	restarted := system.restartRuntime(t)
	if restored := inspectPostgresRuntime(t, restarted, work.start); restored != waiting {
		t.Fatalf("codec changed complete admission binding: got=%+v want=%+v", restored, waiting)
	}

	adapter.Set(runtimeexecution.LeaseAcquisitionObservation{
		Disposition:      runtimeexecution.LeaseAcquisitionPermanentFailure,
		PermanentFailure: runtimeexecution.PreLeasePermanentImmutableBinding,
	})
	terminal, err := restarted.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("terminalize Runtime for codec round trip: %v", err)
	}
	snapshot := terminal.Snapshot
	fenced := snapshot.CapacityEvidence.RuntimeFencedOrTerminal
	noLease := snapshot.CapacityEvidence.NoLeasePhysicalDisposition
	if snapshot.State != runtimeexecution.RuntimeTerminal || snapshot.Outcome != runtimeexecution.RuntimeRejected ||
		snapshot.PreLeaseTerminalReason != runtimeexecution.PreLeaseTerminalImmutableBinding ||
		snapshot.Operation.OperationID == work.start.OperationID ||
		snapshot.Operation.AdmissionGrantID != waiting.Operation.AdmissionGrantID ||
		snapshot.Operation.WorkItemID != waiting.Operation.WorkItemID ||
		snapshot.Operation.GrantGeneration != waiting.Operation.GrantGeneration ||
		snapshot.Operation.ExecutionNodeID != waiting.Operation.ExecutionNodeID ||
		snapshot.Operation.NodeCapacityGeneration != waiting.Operation.NodeCapacityGeneration ||
		snapshot.Operation.ResourceClassID != waiting.Operation.ResourceClassID ||
		snapshot.Operation.ExecutionPolicyID != waiting.Operation.ExecutionPolicyID ||
		snapshot.Operation.SchedulerEpoch != waiting.Operation.SchedulerEpoch ||
		snapshot.Operation.PolicyVersion != waiting.Operation.PolicyVersion ||
		fenced.WorkItemID != work.start.AdmissionGrant.WorkItemID ||
		fenced.AdmissionGrantID != work.start.AdmissionGrant.AdmissionGrantID ||
		fenced.GrantGeneration != work.start.AdmissionGrant.Generation ||
		fenced.RuntimeRunID != work.start.RuntimeRunID ||
		fenced.StartOperationID != work.start.OperationID ||
		fenced.StartDigest != work.start.CanonicalRequestDigest ||
		fenced.TerminalDecisionID.String() == "" || fenced.RuntimeRevision != snapshot.RuntimeRevision ||
		fenced.RuntimeFence != snapshot.RuntimeFence || fenced.SchedulerEpoch != waiting.Operation.SchedulerEpoch ||
		fenced.PolicyVersion != waiting.Operation.PolicyVersion ||
		fenced.LeaseAcquireOperationID != waiting.Lease.AcquireOperationID ||
		fenced.LeaseAcquireDigest != waiting.Lease.AcquireDigest ||
		noLease.WorkItemID != fenced.WorkItemID || noLease.AdmissionGrantID != fenced.AdmissionGrantID ||
		noLease.GrantGeneration != fenced.GrantGeneration || noLease.RuntimeRunID != fenced.RuntimeRunID ||
		noLease.StartOperationID != fenced.StartOperationID || noLease.StartDigest != fenced.StartDigest ||
		noLease.TerminalDecisionID != fenced.TerminalDecisionID || noLease.RuntimeRevision != fenced.RuntimeRevision ||
		noLease.RuntimeFence != fenced.RuntimeFence || noLease.SchedulerEpoch != fenced.SchedulerEpoch ||
		noLease.PolicyVersion != fenced.PolicyVersion ||
		noLease.LeaseAcquireOperationID != fenced.LeaseAcquireOperationID ||
		noLease.LeaseAcquireDigest != fenced.LeaseAcquireDigest ||
		noLease.ExecutionNodeID != waiting.Operation.ExecutionNodeID ||
		noLease.NodeCapacityGeneration != waiting.Operation.NodeCapacityGeneration ||
		snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (runtimeexecution.PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("terminal codec lost binding, reason, or separated evidence: %+v", snapshot)
	}
	restartedAgain := system.restartRuntime(t)
	if restored := inspectPostgresRuntime(t, restartedAgain, work.start); restored != snapshot {
		t.Fatalf("codec changed terminal capacity evidence: got=%+v want=%+v", restored, snapshot)
	}
}

func TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease(t *testing.T) {
	now := time.Date(2026, time.July, 29, 15, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name      string
		fault     runtimeexecution.PersistenceFaultPoint
		wantCode  runtimeexecution.ErrorCode
		committed bool
	}{
		{
			name:     "crash before lease commit",
			fault:    runtimeexecution.PersistenceFaultBeforeLeaseCommit,
			wantCode: runtimeexecution.ErrorDependencyUnavailable,
		},
		{
			name:      "response loss after lease commit",
			fault:     runtimeexecution.PersistenceFaultAfterLeaseCommit,
			wantCode:  runtimeexecution.ErrorReconciliationRequired,
			committed: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter := &controlledLeaseAcquisitionAdapter{
				observation: runtimeexecution.LeaseAcquisitionObservation{
					Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
				},
			}
			faults := &runtimeexecution.PersistenceFaultController{}
			system := newPostgresRuntimeAdmissionSystem(t, now, adapter, faults)
			work := system.enqueueAndAdmitRuntime(t, "lease-fault")
			accepted, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
				t.Fatalf("accept waiting Runtime: decision=%+v err=%v", accepted, err)
			}
			stableOperation := accepted.Snapshot.Lease.AcquireOperationID
			stableDigest := accepted.Snapshot.Lease.AcquireDigest
			adapter.Set(runtimeexecution.LeaseAcquisitionObservation{
				Disposition: runtimeexecution.LeaseAcquisitionReady,
			})
			if err := faults.FailNextAt(testCase.fault); err != nil {
				t.Fatal(err)
			}
			_, err = system.runtime.Execute(context.Background(), work.start)
			assertRuntimeExecutionErrorCode(t, err, testCase.wantCode)

			afterFault := inspectPostgresRuntime(t, system.runtime, work.start)
			assertPostgresStartAcceptedAndBound(t, system, work, afterFault)
			if testCase.committed {
				assertExactlyOnePostgresPreLease(t, afterFault, stableOperation, stableDigest)
			} else if afterFault.State != runtimeexecution.RuntimeWaitingForLease ||
				afterFault.Lease.AcquireStatus != runtimeexecution.LeaseAcquirePending ||
				afterFault.Lease.LeaseID.String() != "" ||
				afterFault.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityHeld ||
				afterFault.Capacity.NoLease != runtimeexecution.NoLeaseDispositionNone ||
				afterFault.Capacity.Physical != runtimeexecution.PhysicalCapacityNotApplicable ||
				afterFault.CapacityEvidence != (runtimeexecution.RuntimeCapacityEvidenceSnapshot{}) {
				t.Fatalf("pre-commit fault did not prove zero lease while retaining reservations: %+v", afterFault)
			}

			restarted := system.restartRuntime(t)
			replayed, err := restarted.Execute(context.Background(), work.start)
			if err != nil || replayed.Fact != accepted.Fact {
				t.Fatalf("lease reconciliation replay = %+v err=%v", replayed, err)
			}
			assertExactlyOnePostgresPreLease(t, replayed.Snapshot, stableOperation, stableDigest)
			leaseID := replayed.Snapshot.Lease.LeaseID
			callsAfterReconciliation := adapter.CallCount()
			replayedAgain, err := restarted.Execute(context.Background(), work.start)
			if err != nil || replayedAgain.Fact != accepted.Fact ||
				replayedAgain.Snapshot.Lease.LeaseID != leaseID ||
				adapter.CallCount() != callsAfterReconciliation {
				t.Fatalf("exact replay allocated or attempted another lease: replay=%+v err=%v calls=%d/%d",
					replayedAgain, err, adapter.CallCount(), callsAfterReconciliation)
			}
		})
	}
}

func TestPostgresNoLeaseTerminalFaultMatrixIsAllOrNone(t *testing.T) {
	now := time.Date(2026, time.July, 29, 16, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name      string
		fault     runtimeexecution.PersistenceFaultPoint
		wantCode  runtimeexecution.ErrorCode
		committed bool
	}{
		{name: "before mandatory audit", fault: runtimeexecution.PersistenceFaultBeforeMandatoryAudit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after mandatory audit", fault: runtimeexecution.PersistenceFaultAfterMandatoryAudit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before owned outbox", fault: runtimeexecution.PersistenceFaultBeforeOutbox, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before generic commit", fault: runtimeexecution.PersistenceFaultBeforeCommit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before dedicated no-lease commit", fault: runtimeexecution.PersistenceFaultBeforeNoLeaseCommit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after generic commit", fault: runtimeexecution.PersistenceFaultAfterCommit, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
		{name: "after dedicated no-lease commit", fault: runtimeexecution.PersistenceFaultAfterNoLeaseCommit, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
		{name: "before response", fault: runtimeexecution.PersistenceFaultBeforeResponse, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adapter := &controlledLeaseAcquisitionAdapter{
				observation: runtimeexecution.LeaseAcquisitionObservation{
					Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
				},
			}
			faults := &runtimeexecution.PersistenceFaultController{}
			system := newPostgresRuntimeAdmissionSystem(t, now, adapter, faults)
			work := system.enqueueAndAdmitRuntime(t, "no-lease-fault")
			accepted, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil || accepted.Snapshot.State != runtimeexecution.RuntimeWaitingForLease {
				t.Fatalf("accept waiting Runtime: decision=%+v err=%v", accepted, err)
			}
			adapter.Set(runtimeexecution.LeaseAcquisitionObservation{
				Disposition:      runtimeexecution.LeaseAcquisitionPermanentFailure,
				PermanentFailure: runtimeexecution.PreLeasePermanentImmutableBinding,
			})
			if err := faults.FailNextAt(testCase.fault); err != nil {
				t.Fatal(err)
			}
			_, err = system.runtime.Execute(context.Background(), work.start)
			assertRuntimeExecutionErrorCode(t, err, testCase.wantCode)

			afterFault := inspectPostgresRuntime(t, system.runtime, work.start)
			assertPostgresStartAcceptedAndBound(t, system, work, afterFault)
			if testCase.committed {
				if afterFault.Outcome != runtimeexecution.RuntimeRejected ||
					afterFault.PreLeaseTerminalReason != runtimeexecution.PreLeaseTerminalImmutableBinding {
					t.Fatalf("post-commit response fault lost terminal authority: %+v", afterFault)
				}
			} else if afterFault != accepted.Snapshot {
				t.Fatalf("pre-commit fault left a partial no-lease terminal: got=%+v want=%+v", afterFault, accepted.Snapshot)
			}

			restarted := system.restartRuntime(t)
			replayed, err := restarted.Execute(context.Background(), work.start)
			if err != nil || replayed.Fact != accepted.Fact ||
				replayed.Snapshot.Outcome != runtimeexecution.RuntimeRejected ||
				replayed.Snapshot.PreLeaseTerminalReason != runtimeexecution.PreLeaseTerminalImmutableBinding {
				t.Fatalf("no-lease reconciliation replay = %+v err=%v", replayed, err)
			}
			assertAndConsumePostgresNoLeaseEvidence(t, system, work, replayed.Snapshot)
		})
	}
}

func (system *postgresRuntimeAdmissionSystem) restartRuntime(t *testing.T) *runtimeexecution.PostgresAuthority {
	t.Helper()
	restarted, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
		Schema: system.schema, Now: system.clock.Now, Faults: system.faults,
		SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
		SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
		SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
		SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
		LeaseAcquisition:                    system.lease,
	})
	if err != nil {
		t.Fatalf("restart Runtime Execution: %v", err)
	}
	return restarted
}

func (system *postgresRuntimeAdmissionSystem) replaceRuntimeSchedulerParticipant(
	t *testing.T,
	participant runtimeexecution.SchedulerAcceptanceParticipant,
) {
	t.Helper()
	restarted, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
		Schema: system.schema, Now: system.clock.Now, Faults: system.faults,
		SchedulerParticipant:                participant,
		SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
		SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
		SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
		LeaseAcquisition:                    system.lease,
	})
	if err != nil {
		t.Fatalf("replace Runtime Scheduler participant: %v", err)
	}
	system.runtime = restarted
}

func (system *postgresRuntimeAdmissionSystem) enableRuntimeCancellationParticipant(t *testing.T) {
	t.Helper()
	restarted, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
		Schema: system.schema, Now: system.clock.Now, Faults: system.faults,
		SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
		SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
		SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
		SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
		SchedulerCancellationParticipant:    system.scheduling.RuntimeCancellationParticipant(),
		SchedulerCancellationFunction:       system.scheduling.RuntimeCancellationFunction(),
		LeaseAcquisition:                    system.lease,
	})
	if err != nil {
		t.Fatalf("enable Runtime cancellation participant: %v", err)
	}
	system.runtime = restarted
}

func newPostgresPreLeaseCancel(
	t *testing.T,
	start runtimeexecution.StartRuntimeRun,
	runtimeRevision runtimeexecution.RuntimeRevision,
	operationGeneration runtimeexecution.OperationGeneration,
	runtimeFence runtimeexecution.RuntimeFence,
	occurredAt time.Time,
) runtimeexecution.CancelRuntimeRun {
	t.Helper()
	operationID, err := runtimeexecution.NewOperationID("cancel-" + start.OperationID.String())
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := runtimeexecution.NewCancelRuntimeRun(runtimeexecution.CancelRuntimeRunInput{
		SchemaVersion:               runtimeexecution.SchemaV1,
		OperationID:                 operationID,
		PersonalWorkspaceID:         start.PersonalWorkspaceID,
		TaskID:                      start.TaskID,
		PhaseRunID:                  start.PhaseRunID,
		RuntimeRunID:                start.RuntimeRunID,
		ExpectedRuntimeRevision:     runtimeRevision,
		ExpectedStartOperationID:    start.OperationID,
		ExpectedOperationGeneration: operationGeneration,
		ExpectedRuntimeFence:        runtimeFence,
		Authority:                   start.Authority,
		Reason:                      runtimeexecution.CancellationUserRequested,
		SafetyEpoch:                 start.ReleaseSafetyEpoch,
		OccurredAt:                  occurredAt,
	})
	if err != nil {
		t.Fatalf("construct pre-lease Cancel: %v", err)
	}
	return cancel
}

func assertRuntimeExecutionErrorCode(t *testing.T, err error, want runtimeexecution.ErrorCode) {
	t.Helper()
	var runtimeError *runtimeexecution.Error
	if !errors.As(err, &runtimeError) || runtimeError.Code() != want {
		t.Fatalf("Runtime error = %T %v, want code %v", err, err, want)
	}
}

func assertSchedulerErrorCode(t *testing.T, err error, want scheduler.ErrorCode) {
	t.Helper()
	var schedulingError *scheduler.Error
	if !errors.As(err, &schedulingError) || schedulingError.Code() != want {
		t.Fatalf("Scheduler error = %T %v, want code %v", err, err, want)
	}
}

func schedulerOwnerWorkItemRef(t *testing.T, work admittedRuntimeWork) scheduler.WorkItemRef {
	t.Helper()
	personalWorkspaceID, err := scheduler.NewPersonalWorkspaceID(work.start.PersonalWorkspaceID.String())
	if err != nil {
		t.Fatal(err)
	}
	return scheduler.WorkItemRef{
		WorkItemID: work.canonical.WorkItemID,
		Scope:      scheduler.NewOwnerWorkItemQueryScope(personalWorkspaceID),
	}
}

func assertExactlyOnePostgresPreLease(
	t *testing.T,
	snapshot runtimeexecution.RuntimeSnapshot,
	wantOperation runtimeexecution.OperationID,
	wantDigest runtimeexecution.Digest,
) {
	t.Helper()
	if snapshot.State != runtimeexecution.RuntimePreparingPrerequisites ||
		snapshot.Outcome != runtimeexecution.RuntimeOutcomeNone ||
		snapshot.Lease.AcquireStatus != runtimeexecution.LeaseGranted ||
		snapshot.Lease.AcquireOperationID != wantOperation ||
		snapshot.Lease.AcquireDigest != wantDigest ||
		snapshot.Lease.LeaseID.String() == "" || snapshot.Lease.Generation != 1 || snapshot.Lease.Fence != 1 ||
		snapshot.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityHeld ||
		snapshot.Capacity.NoLease != runtimeexecution.NoLeaseDispositionNone ||
		snapshot.Capacity.Physical != runtimeexecution.PhysicalCapacityOccupied ||
		snapshot.CapacityEvidence != (runtimeexecution.RuntimeCapacityEvidenceSnapshot{}) {
		t.Fatalf("Runtime does not expose exactly one committed pre-lease: %+v", snapshot)
	}
}

func postgresPermanentPreLeaseCase(
	name string,
	failure runtimeexecution.PreLeasePermanentFailure,
	reason runtimeexecution.PreLeaseTerminalReason,
) struct {
	name          string
	observation   runtimeexecution.LeaseAcquisitionObservation
	wantState     runtimeexecution.RuntimeState
	wantOutcome   runtimeexecution.RuntimeOutcome
	wantReason    runtimeexecution.PreLeaseTerminalReason
	wantLease     runtimeexecution.LeaseAcquireStatus
	wantReconcile runtimeexecution.ReconciliationStatus
	wantTerminal  bool
} {
	return struct {
		name          string
		observation   runtimeexecution.LeaseAcquisitionObservation
		wantState     runtimeexecution.RuntimeState
		wantOutcome   runtimeexecution.RuntimeOutcome
		wantReason    runtimeexecution.PreLeaseTerminalReason
		wantLease     runtimeexecution.LeaseAcquireStatus
		wantReconcile runtimeexecution.ReconciliationStatus
		wantTerminal  bool
	}{
		name: name,
		observation: runtimeexecution.LeaseAcquisitionObservation{
			Disposition:      runtimeexecution.LeaseAcquisitionPermanentFailure,
			PermanentFailure: failure,
		},
		wantState:     runtimeexecution.RuntimeTerminal,
		wantOutcome:   runtimeexecution.RuntimeRejected,
		wantReason:    reason,
		wantLease:     runtimeexecution.LeaseNotRequested,
		wantReconcile: runtimeexecution.ReconciliationStable,
		wantTerminal:  true,
	}
}

func assertPostgresStartAcceptedAndBound(
	t *testing.T,
	system *postgresRuntimeAdmissionSystem,
	work admittedRuntimeWork,
	snapshot runtimeexecution.RuntimeSnapshot,
) {
	t.Helper()
	view, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil {
		t.Fatalf("inspect Scheduler Work Item: %v", err)
	}
	currentStartBinding := snapshot.State == runtimeexecution.RuntimeTerminal ||
		snapshot.Operation.OperationID == work.start.OperationID &&
			snapshot.Operation.Digest == work.start.CanonicalRequestDigest
	wantGrantState := scheduler.GrantBound
	wantNodeReservation := scheduler.ReservationBound
	wantPhysical := scheduler.PhysicalOccupancyNone
	if snapshot.Lease.AcquireStatus == runtimeexecution.LeaseGranted {
		wantGrantState = scheduler.GrantLeaseAttached
		wantNodeReservation = scheduler.ReservationLeaseAttached
		wantPhysical = scheduler.PhysicalOccupancyHeld
	}
	if view.State != scheduler.WorkItemAccepted || view.Grant.State != wantGrantState ||
		view.Grant.Generation != work.canonical.Grant.Generation ||
		view.LogicalReservation != scheduler.ReservationBound ||
		view.SelectedNodeReservation != wantNodeReservation || view.PhysicalOccupancy != wantPhysical ||
		!currentStartBinding ||
		snapshot.Operation.AdmissionGrantID != work.start.AdmissionGrant.AdmissionGrantID ||
		snapshot.Operation.WorkItemID != work.start.AdmissionGrant.WorkItemID ||
		snapshot.Operation.GrantGeneration != work.start.AdmissionGrant.Generation ||
		snapshot.Lease.AcquireOperationID.String() == "" ||
		snapshot.Lease.AcquireDigest == (runtimeexecution.Digest{}) ||
		snapshot.RuntimeFence < work.start.ExpectedRuntimeFence+1 ||
		snapshot.LeaseAcquireBy.After(snapshot.Deadline) {
		t.Fatalf("Start/Accepted/Bound atomic binding is incomplete: scheduler=%+v runtime=%+v", view, snapshot)
	}
}

func assertAndConsumePostgresNoLeaseEvidence(
	t *testing.T,
	system *postgresRuntimeAdmissionSystem,
	work admittedRuntimeWork,
	snapshot runtimeexecution.RuntimeSnapshot,
) {
	t.Helper()
	fenced := snapshot.CapacityEvidence.RuntimeFencedOrTerminal
	noLease := snapshot.CapacityEvidence.NoLeasePhysicalDisposition
	if snapshot.State != runtimeexecution.RuntimeTerminal || snapshot.Outcome == runtimeexecution.RuntimeLost ||
		snapshot.Lease.AcquireStatus != runtimeexecution.LeaseNotRequested ||
		snapshot.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityReleaseReady ||
		snapshot.Capacity.NoLease != runtimeexecution.NoLeaseDispositionRecorded ||
		snapshot.Capacity.Physical != runtimeexecution.PhysicalCapacityNotApplicable ||
		fenced == (runtimeexecution.RuntimeFencedOrTerminalEvidence{}) ||
		noLease == (runtimeexecution.NoLeasePhysicalDispositionEvidence{}) ||
		snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (runtimeexecution.PhysicalCapacityReleaseReadyEvidence{}) ||
		fenced.WorkItemID != work.start.AdmissionGrant.WorkItemID ||
		fenced.AdmissionGrantID != work.start.AdmissionGrant.AdmissionGrantID ||
		fenced.GrantGeneration != work.start.AdmissionGrant.Generation ||
		fenced.StartOperationID != work.start.OperationID ||
		fenced.StartDigest != work.start.CanonicalRequestDigest ||
		noLease.ExecutionNodeID != snapshot.Operation.ExecutionNodeID ||
		noLease.NodeCapacityGeneration != snapshot.Operation.NodeCapacityGeneration {
		t.Fatalf("terminal row lacks exact no-lease evidence: %+v", snapshot)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(), fenced); err != nil {
		t.Fatalf("apply logical disposition: %v", err)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(), fenced); err != nil {
		t.Fatalf("replay logical disposition: %v", err)
	}
	view, err := system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil {
		t.Fatal(err)
	}
	if view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationBound ||
		view.Grant.State != scheduler.GrantBound {
		t.Fatalf("logical evidence crossed into node disposition: %+v", view)
	}
	if err := system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), noLease); err != nil {
		t.Fatalf("apply no-lease disposition: %v", err)
	}
	if err := system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), noLease); err != nil {
		t.Fatalf("replay no-lease disposition: %v", err)
	}
	view, err = system.scheduling.Inspect(context.Background(), schedulerOwnerWorkItemRef(t, work))
	if err != nil {
		t.Fatal(err)
	}
	if view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationReleased ||
		view.Grant.State != scheduler.GrantReleased {
		t.Fatalf("separate dispositions did not converge exactly: %+v", view)
	}
}

func inspectPostgresRuntime(
	t *testing.T,
	runtime runtimeexecution.RuntimeExecution,
	start runtimeexecution.StartRuntimeRun,
) runtimeexecution.RuntimeSnapshot {
	t.Helper()
	snapshot, err := runtime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion:       runtimeexecution.SchemaV1,
		ProjectionVersion:   runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID:        start.RuntimeRunID,
		Authority:           start.Authority,
	})
	if err != nil {
		t.Fatalf("Inspect Runtime: %v", err)
	}
	return snapshot
}

func newPostgresRuntimeAdmissionSystem(
	t *testing.T,
	now time.Time,
	leaseAcquisition runtimeexecution.LeaseAcquisitionAdapter,
	faults runtimeexecution.PersistenceFaultInjector,
) *postgresRuntimeAdmissionSystem {
	t.Helper()
	return newPostgresRuntimeAdmissionSystemWithGrantTTL(
		t, now, 10*time.Minute, leaseAcquisition, faults,
	)
}

func newPostgresRuntimeAdmissionSystemWithGrantTTL(
	t *testing.T,
	now time.Time,
	grantTTL time.Duration,
	leaseAcquisition runtimeexecution.LeaseAcquisitionAdapter,
	faults runtimeexecution.PersistenceFaultInjector,
) *postgresRuntimeAdmissionSystem {
	t.Helper()
	return newPostgresRuntimeAdmissionSystemWithLimits(
		t, now, grantTTL,
		scheduler.AdmissionLimits{Global: 1, PersonalWorkspace: 1, WorkerClass: 1, ResourceClass: 1},
		1, leaseAcquisition, faults,
	)
}

func newPostgresRuntimeAdmissionSystemWithLimits(
	t *testing.T,
	now time.Time,
	grantTTL time.Duration,
	limits scheduler.AdmissionLimits,
	availableRuntimeSlots uint64,
	leaseAcquisition runtimeexecution.LeaseAcquisitionAdapter,
	faults runtimeexecution.PersistenceFaultInjector,
) *postgresRuntimeAdmissionSystem {
	t.Helper()
	db, schema := isolatedPostgresSchema(t)
	clock := &runtimeAdmissionClock{now: now.UTC()}
	nodeID, err := scheduler.NewExecutionNodeID("issue-75-node")
	if err != nil {
		t.Fatal(err)
	}
	resourceClassID, err := scheduler.NewResourceClassID("resource-class-runtime-release-v1")
	if err != nil {
		t.Fatal(err)
	}
	executionPolicyID, err := scheduler.NewExecutionPolicyID("execution-policy-runtime-release-v1")
	if err != nil {
		t.Fatal(err)
	}
	scheduling, err := scheduler.NewPostgresAuthority(db, scheduler.PostgresConfig{
		Schema:                                 schema,
		Now:                                    clock.Now,
		RuntimeNodeFactFunction:                schema + ".runtime_execution_read_scheduler_node_fact",
		RuntimePhysicalReleaseEvidenceFunction: schema + ".runtime_execution_validate_physical_release",
		Admission: scheduler.LocalAdmissionConfig{
			SchedulerEpoch: 1,
			PolicyVersion:  1,
			GrantTTL:       grantTTL,
			Limits:         limits,
			Node: scheduler.ExecutionNodeConfig{
				ExecutionNodeID:       nodeID,
				CapacityGeneration:    1,
				ResourceClassID:       resourceClassID,
				ExecutionPolicyID:     executionPolicyID,
				AvailableRuntimeSlots: availableRuntimeSlots,
			},
		},
	})
	if err != nil {
		t.Fatalf("create Scheduler authority: %v", err)
	}
	if err := scheduling.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate Scheduler authority: %v", err)
	}
	tasks := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now:                      clock.Now,
		SchedulerParticipant:     scheduling.TaskEnqueueParticipant(),
		SchedulerEnqueueFunction: scheduling.TaskEnqueueFunction(),
	})
	runtime, err := runtimeexecution.NewPostgresAuthority(db, runtimeexecution.PostgresConfig{
		Schema: schema, Now: clock.Now, Faults: faults,
		SchedulerParticipant:                scheduling.RuntimeAcceptanceParticipant(),
		SchedulerAcceptanceFunction:         scheduling.RuntimeAcceptanceFunction(),
		SchedulerLeaseAttachmentParticipant: scheduling.RuntimeLeaseAttachmentParticipant(),
		SchedulerLeaseAttachmentFunction:    scheduling.RuntimeLeaseAttachmentFunction(),
		LeaseAcquisition:                    leaseAcquisition,
	})
	if err != nil {
		t.Fatalf("create Runtime Execution authority: %v", err)
	}
	if err := runtime.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate Runtime Execution authority: %v", err)
	}
	system := &postgresRuntimeAdmissionSystem{
		t: t, db: db, schema: schema, clock: clock,
		tasks: tasks, scheduling: scheduling, runtime: runtime,
		lease: leaseAcquisition, faults: faults,
	}
	system.bootstrapConfiguredRuntimeNode(t, nodeID.String(), resourceClassID.String(), executionPolicyID.String())
	return system
}

func (system *postgresRuntimeAdmissionSystem) bootstrapConfiguredRuntimeNode(
	t *testing.T,
	nodeValue string,
	resourceClassValue string,
	executionPolicyValue string,
) {
	t.Helper()
	nodeID, err := runtimeexecution.NewExecutionNodeID(nodeValue)
	if err != nil {
		t.Fatal(err)
	}
	resourceClassID, err := runtimeexecution.NewResourceClassID(resourceClassValue)
	if err != nil {
		t.Fatal(err)
	}
	executionPolicyID, err := runtimeexecution.NewExecutionPolicyID(executionPolicyValue)
	if err != nil {
		t.Fatal(err)
	}
	attestationID, _ := runtimeexecution.NewNodeAttestationID("issue-76-bootstrap-node-attestation")
	nodeAuthorityID, _ := runtimeexecution.NewNodeAuthorityID("issue-76-bootstrap-node-authority")
	workerAuthorityID, _ := runtimeexecution.NewWorkerAuthorityID("issue-76-bootstrap-worker-authority")
	operationID, _ := runtimeexecution.NewOperationID("issue-76-bootstrap-node")
	resetEvidenceID, _ := runtimeexecution.NewEvidenceID("issue-76-bootstrap-reset-evidence")
	now := system.clock.Now()
	attest, err := runtimeexecution.NewAttestExecutionNode(runtimeexecution.AttestExecutionNodeInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: operationID, ExecutionNodeID: nodeID,
		NodeGeneration: 1, AttestationID: attestationID, AttestationGeneration: 1,
		AttestedAt: now, ExpiresAt: now.Add(24 * time.Hour), ResourceClassID: resourceClassID,
		ExecutionPolicyID: executionPolicyID, NodeAuthorityID: nodeAuthorityID,
		WorkerAuthorityID: workerAuthorityID, WorkerGeneration: 1, AuthorizationGeneration: 1,
		AuthorizationExpiresAt: now.Add(24 * time.Hour), ReleaseSafetyEpoch: 1,
		ResetEvidenceID: resetEvidenceID, ResetEvidenceDigest: runtimeexecution.Digest{31: 1}, OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("construct configured node attestation: %v", err)
	}
	if _, err := system.runtime.Maintain(context.Background(), attest); err != nil {
		t.Fatalf("bootstrap configured Runtime Execution node: %v", err)
	}
	system.nodeAttested = true
}

func (system *postgresRuntimeAdmissionSystem) enqueueRuntime(t *testing.T, suffix string) taskorchestration.UserAuthority {
	t.Helper()
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-prelease-"+suffix+"-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	system.enqueueRuntimeForOwner(t, suffix, owner)
	return owner
}

func (system *postgresRuntimeAdmissionSystem) enqueueRuntimeForOwner(
	t *testing.T,
	suffix string,
	owner taskorchestration.UserAuthority,
) {
	t.Helper()
	now := system.clock.Now()
	taskValue := "postgres-prelease-" + suffix
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, taskValue+"-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, taskValue+"-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	started, err := system.tasks.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, taskValue+"-start", taskValue, now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start Task %s: %v", suffix, err)
	}
	header := intentHeader(t, taskValue+"-work", taskValue, now.Add(time.Second))
	header.ExpectedTaskRevision = started.AcceptedTaskRevision
	if _, err := system.tasks.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		header, worker, operationID(t, taskValue+"-availability"),
	)); err != nil {
		t.Fatalf("make Runtime work available for %s: %v", suffix, err)
	}
	taskID := taskID(t, taskValue)
	view, err := system.tasks.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: taskID, Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query Task %s after enqueue: %v", suffix, err)
	}
	if len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
		t.Fatalf("Task %s did not expose one pre-created Runtime Run: %+v", suffix, view)
	}
}

func (system *postgresRuntimeAdmissionSystem) enqueueAndAdmitRuntime(t *testing.T, suffix string) admittedRuntimeWork {
	t.Helper()
	owner := system.enqueueRuntime(t, suffix)
	return system.admitRuntime(t, suffix, owner)
}

func (system *postgresRuntimeAdmissionSystem) enqueueAndAdmitRuntimeForOwner(
	t *testing.T,
	suffix string,
	owner taskorchestration.UserAuthority,
) admittedRuntimeWork {
	t.Helper()
	system.enqueueRuntimeForOwner(t, suffix, owner)
	return system.admitRuntime(t, suffix, owner)
}

func (system *postgresRuntimeAdmissionSystem) admitRuntime(
	t *testing.T,
	suffix string,
	owner taskorchestration.UserAuthority,
) admittedRuntimeWork {
	t.Helper()
	work := system.claimRuntime(t)
	wantTaskID := taskID(t, "postgres-prelease-"+suffix)
	if work.taskID != wantTaskID {
		t.Fatalf("claim and admit Runtime %s selected Task %s", suffix, work.start.TaskID.String())
	}
	work.owner = owner
	return work
}

func (system *postgresRuntimeAdmissionSystem) claimRuntime(t *testing.T) admittedRuntimeWork {
	t.Helper()
	decision, err := system.scheduling.ClaimAndAdmit(context.Background())
	if err != nil {
		t.Fatalf("claim and admit Runtime: %v", err)
	}
	grantID, err := runtimeexecution.NewAdmissionGrantID(decision.Grant.AdmissionGrantID.String())
	if err != nil {
		t.Fatal(err)
	}
	workItemID, err := runtimeexecution.NewWorkItemID(decision.WorkItemID.String())
	if err != nil {
		t.Fatal(err)
	}
	start, err := runtimeexecution.BindCanonicalStartPayload(
		decision.CanonicalPayload,
		runtimeexecution.Digest(decision.PayloadDigest),
		runtimeexecution.AdmissionGrantProof{
			AdmissionGrantID: grantID,
			WorkItemID:       workItemID,
			Generation:       runtimeexecution.AdmissionGrantGeneration(decision.Grant.Generation),
		},
	)
	if err != nil {
		t.Fatalf("bind canonical Runtime Start: %v", err)
	}
	admitted := admittedRuntimeWork{
		taskID:    taskID(t, decision.TaskID),
		canonical: decision, start: start,
	}
	system.ensureRuntimeNodeAttested(t, admitted)
	return admitted
}

func (system *postgresRuntimeAdmissionSystem) ensureRuntimeNodeAttested(t *testing.T, work admittedRuntimeWork) {
	t.Helper()
	if system.nodeAttested {
		return
	}
	nodeID, err := runtimeexecution.NewExecutionNodeID(work.canonical.Grant.ExecutionNodeID.String())
	if err != nil {
		t.Fatal(err)
	}
	attestationID, err := runtimeexecution.NewNodeAttestationID("issue-76-bootstrap-node-attestation")
	if err != nil {
		t.Fatal(err)
	}
	nodeAuthorityID, err := runtimeexecution.NewNodeAuthorityID("issue-76-bootstrap-node-authority")
	if err != nil {
		t.Fatal(err)
	}
	workerAuthorityID, err := runtimeexecution.NewWorkerAuthorityID("issue-76-bootstrap-worker-authority")
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := runtimeexecution.NewOperationID("issue-76-bootstrap-node")
	if err != nil {
		t.Fatal(err)
	}
	resetEvidenceID, err := runtimeexecution.NewEvidenceID("issue-76-bootstrap-reset-evidence")
	if err != nil {
		t.Fatal(err)
	}
	catalogEpoch := runtimeexecution.CatalogSafetyEpoch(0)
	if work.start.CatalogBinding != nil {
		catalogEpoch = work.start.CatalogBinding.SafetyEpoch
	}
	now := system.clock.Now()
	attest, err := runtimeexecution.NewAttestExecutionNode(runtimeexecution.AttestExecutionNodeInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: operationID, ExecutionNodeID: nodeID,
		NodeGeneration: runtimeexecution.NodeGeneration(work.canonical.Grant.NodeCapacityGeneration),
		AttestationID:  attestationID, AttestationGeneration: 1, AttestedAt: now,
		ExpiresAt: now.Add(24 * time.Hour), ResourceClassID: work.start.ResourceClassID,
		ExecutionPolicyID: work.start.ExecutionPolicyID, NodeAuthorityID: nodeAuthorityID,
		WorkerAuthorityID: workerAuthorityID, WorkerGeneration: 1, AuthorizationGeneration: 1,
		AuthorizationExpiresAt: now.Add(24 * time.Hour), ReleaseSafetyEpoch: work.start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: catalogEpoch, ResetEvidenceID: resetEvidenceID,
		ResetEvidenceDigest: runtimeexecution.Digest{31: 1}, OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("construct bootstrap node attestation: %v", err)
	}
	if _, err := system.runtime.Maintain(context.Background(), attest); err != nil {
		t.Fatalf("attest Runtime Execution node: %v", err)
	}
	system.nodeAttested = true
}
