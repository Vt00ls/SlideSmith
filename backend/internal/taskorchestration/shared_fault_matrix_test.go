package taskorchestration_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestSharedTaskOrchestrationFaultMatrix(t *testing.T) {
	runSharedTaskOrchestrationFaultMatrix(t)
}

type sharedDecisionFaultAdapter struct {
	decide                func(context.Context, taskorchestration.TransitionIntent) (taskorchestration.TransitionDecision, error)
	query                 func(context.Context, taskorchestration.TaskQuery) (taskorchestration.TaskOrchestrationView, error)
	failBeforeCommit      func(*testing.T)
	crashAfterCommit      func(*testing.T)
	loseResponse          func(*testing.T)
	restart               func(*testing.T)
	afterDecision         func(*testing.T, taskorchestration.TransitionDecision)
	runtimeEvidenceIntent func(
		*testing.T,
		taskorchestration.IntentHeader,
		taskorchestration.EnactmentRef,
		taskorchestration.TaskOrchestrationView,
		string,
	) taskorchestration.TransitionIntent
	newDispatcher func(
		*testing.T,
		func() time.Time,
		[]taskorchestration.WorkerAuthority,
		taskorchestration.OwnedTransport,
	) taskorchestration.OutboxDispatcher
}

type sharedDecisionFaultAdapterFactory struct {
	name string
	new  func(
		*testing.T,
		time.Time,
		taskorchestration.UserAuthority,
		taskorchestration.WorkerAuthority,
	) sharedDecisionFaultAdapter
}

func runSharedTaskOrchestrationFaultMatrix(t *testing.T) {
	t.Helper()
	factories := []sharedDecisionFaultAdapterFactory{
		{name: "in_memory", new: newInMemoryDecisionFaultAdapter},
		{name: "postgres_owned_persistence", new: newPostgresDecisionFaultAdapter},
		{name: "owned_transport_evidence_to_postgres", new: newOwnedDecisionFaultAdapter},
	}
	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			t.Run("decision_crash_and_response_loss", func(t *testing.T) {
				runDecisionCrashAndResponseLossContract(t, factory)
			})
			t.Run("claim_loss_duplicate_and_out_of_order_delivery", func(t *testing.T) {
				runDeliveryFaultContract(t, factory)
			})
			t.Run("cancellation_commit_first_and_fence_first", func(t *testing.T) {
				runCancellationOrderingContract(t, factory)
			})
		})
	}
}

type sharedPendingLifecycle struct {
	adapter            sharedDecisionFaultAdapter
	owner              taskorchestration.UserAuthority
	lifecycleAuthority taskorchestration.TaskWorkspaceLifecycleAuthority
	taskName           string
	view               taskorchestration.TaskOrchestrationView
	phase              taskorchestration.PhaseRunView
	commitOperation    taskorchestration.EnactmentRef
}

func runCancellationOrderingContract(
	t *testing.T,
	factory sharedDecisionFaultAdapterFactory,
) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 22, 0, 0, 0, time.UTC)

	commitFirst := newSharedPendingLifecycle(
		t, factory, now, "shared-cancel-commit-first",
	)
	cancelHeader := intentHeader(
		t, "shared-cancel-commit-first-cancel", commitFirst.taskName, now.Add(5*time.Second),
	)
	cancelHeader.ExpectedTaskRevision = commitFirst.view.TaskRevision
	cancelHeader.ActivityGeneration = commitFirst.view.ActivityGeneration
	cancelled, err := commitFirst.adapter.decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, commitFirst.owner, taskorchestration.CancelReasonUserRequested,
		),
	)
	if err != nil || cancelled.TaskProjection.CancellationState != taskorchestration.CancellationCancelling {
		t.Fatalf("accept commit-first cancellation: decision=%+v err=%v", cancelled, err)
	}
	commitFence, ok := commitFirst.commitOperation.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
	if !ok {
		t.Fatalf("pending lifecycle operation lacks typed fence: %+v", commitFirst.commitOperation)
	}
	lateCommitHeader := intentHeader(
		t, "shared-cancel-commit-first-response", commitFirst.taskName, now.Add(6*time.Second),
	)
	lateCommitHeader.ExpectedTaskRevision = cancelled.AcceptedTaskRevision
	lateCommitHeader.ActivityGeneration = commitFirst.view.ActivityGeneration
	revisionID := taskWorkspaceRevisionID(t, "shared-cancel-commit-first-revision")
	checkpoint := checkpointID(t, "shared-cancel-commit-first-checkpoint")
	committed, err := commitFirst.adapter.decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			lateCommitHeader,
			commitFirst.lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "shared-cancel-commit-first-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "6666666666666666666666666666666666666666666666666666666666666666"),
				),
				PhaseRunID:         commitFirst.phase.PhaseRunID,
				PhaseRunGeneration: commitFirst.phase.Generation,
				PhaseRunFence:      commitFirst.phase.Fence,
				OperationID:        commitFirst.commitOperation.OperationID,
				Generation:         taskorchestration.TaskWorkspaceLifecycleGeneration(commitFirst.phase.Generation),
				Fence:              commitFence, SafetyEpoch: commitFirst.view.SafetyEpoch,
				Outcome:    taskorchestration.LifecycleEvidenceCommitted,
				RevisionID: revisionID, CheckpointID: checkpoint,
			},
		),
	)
	if err != nil || committed.TaskProjection.CancellationState != taskorchestration.CancellationCancelling ||
		committed.TaskProjection.LatestRevisionID != revisionID ||
		committed.TaskProjection.LatestCheckpointID != checkpoint ||
		committed.TaskProjection.ActivityGeneration != cancelled.TaskProjection.ActivityGeneration {
		t.Fatalf("commit-first response lost committed C04 evidence or cancellation fence: decision=%+v err=%v", committed, err)
	}

	fenceFirst := newSharedPendingLifecycle(
		t, factory, now.Add(10*time.Second), "shared-cancel-fence-first",
	)
	fenceCancelHeader := intentHeader(
		t, "shared-cancel-fence-first-cancel", fenceFirst.taskName, now.Add(15*time.Second),
	)
	fenceCancelHeader.ExpectedTaskRevision = fenceFirst.view.TaskRevision
	fenceCancelHeader.ActivityGeneration = fenceFirst.view.ActivityGeneration
	fenceCancellation, err := fenceFirst.adapter.decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			fenceCancelHeader, fenceFirst.owner, taskorchestration.CancelReasonUserRequested,
		),
	)
	if err != nil {
		t.Fatalf("accept fence-first cancellation: %v", err)
	}
	var fenceOperation taskorchestration.EnactmentRef
	for _, enactment := range fenceCancellation.EnactmentRefs {
		if enactment.Kind == taskorchestration.EnactmentTaskWorkspaceLifecycle {
			fenceOperation = enactment
			break
		}
	}
	lifecycleFence, ok := fenceOperation.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
	if !ok || fenceOperation.OperationID.String() == "" {
		t.Fatalf("cancellation omitted typed C04 fence operation: %+v", fenceCancellation)
	}
	fenceEvidenceHeader := intentHeader(
		t, "shared-cancel-fence-first-evidence", fenceFirst.taskName, now.Add(16*time.Second),
	)
	fenceEvidenceHeader.ExpectedTaskRevision = fenceCancellation.AcceptedTaskRevision
	fenceEvidenceHeader.ActivityGeneration = fenceCancellation.TaskProjection.ActivityGeneration
	terminal, err := fenceFirst.adapter.decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			fenceEvidenceHeader,
			fenceFirst.lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "shared-cancel-fence-first-fenced"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "7777777777777777777777777777777777777777777777777777777777777777"),
				),
				PhaseRunID:         fenceFirst.phase.PhaseRunID,
				PhaseRunGeneration: fenceFirst.phase.Generation,
				PhaseRunFence:      fenceFirst.phase.Fence,
				OperationID:        fenceOperation.OperationID,
				Generation:         taskorchestration.TaskWorkspaceLifecycleGeneration(fenceFirst.phase.Generation),
				Fence:              lifecycleFence, SafetyEpoch: fenceFirst.view.SafetyEpoch,
				Outcome: taskorchestration.LifecycleEvidenceFenced,
			},
		),
	)
	if err != nil || terminal.TaskProjection.CancellationState != taskorchestration.CancellationCancelled {
		t.Fatalf("accept fence-first terminal evidence: decision=%+v err=%v", terminal, err)
	}
	oldCommitFence, ok := fenceFirst.commitOperation.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
	if !ok {
		t.Fatalf("old commit operation lacks typed fence: %+v", fenceFirst.commitOperation)
	}
	lateHeader := intentHeader(
		t, "shared-cancel-fence-first-late-commit", fenceFirst.taskName, now.Add(17*time.Second),
	)
	lateHeader.ExpectedTaskRevision = terminal.AcceptedTaskRevision
	lateHeader.ActivityGeneration = terminal.TaskProjection.ActivityGeneration
	lateEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "shared-cancel-fence-first-late-evidence"),
		taskorchestration.EvidenceTaskWorkspaceLifecycle,
		evidenceDigest(t, "8888888888888888888888888888888888888888888888888888888888888888"),
	)
	_, err = fenceFirst.adapter.decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			lateHeader,
			fenceFirst.lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence:           lateEvidence,
				PhaseRunID:         fenceFirst.phase.PhaseRunID,
				PhaseRunGeneration: fenceFirst.phase.Generation,
				PhaseRunFence:      fenceFirst.phase.Fence,
				OperationID:        fenceFirst.commitOperation.OperationID,
				Generation:         taskorchestration.TaskWorkspaceLifecycleGeneration(fenceFirst.phase.Generation),
				Fence:              oldCommitFence, SafetyEpoch: fenceFirst.view.SafetyEpoch,
				Outcome:      taskorchestration.LifecycleEvidenceCommitted,
				RevisionID:   taskWorkspaceRevisionID(t, "shared-cancel-fence-first-late-revision"),
				CheckpointID: checkpointID(t, "shared-cancel-fence-first-late-checkpoint"),
			},
		),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorStaleAuthority)
	afterLate, err := fenceFirst.adapter.query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, fenceFirst.taskName),
		Authority: taskorchestration.NewUserQueryAuthority(fenceFirst.owner),
	})
	if err != nil || afterLate.TaskRevision != terminal.AcceptedTaskRevision ||
		afterLate.CancellationState != taskorchestration.CancellationCancelled ||
		afterLate.LatestRevisionID.String() != "" || afterLate.LatestCheckpointID.String() != "" ||
		afterLate.LatestEvidenceDiagnostic.EvidenceID != lateEvidence.ID {
		t.Fatalf("late fence-first commit changed terminal Task: view=%+v err=%v", afterLate, err)
	}
}

func newSharedPendingLifecycle(
	t *testing.T,
	factory sharedDecisionFaultAdapterFactory,
	now time.Time,
	prefix string,
) sharedPendingLifecycle {
	t.Helper()
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, prefix+"-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, prefix+"-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, prefix+"-lifecycle"), taskorchestration.AuthorizationGeneration(1),
	)
	adapter := factory.new(t, now, owner, worker)
	taskName := prefix + "-task"
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, prefix+"-phase"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	started, err := adapter.decide(
		context.Background(),
		taskorchestration.NewStartPinnedTaskIntent(
			intentHeader(t, prefix+"-start", taskName, now), owner, pinned,
		),
	)
	if err != nil {
		t.Fatalf("start pending lifecycle Task: %v", err)
	}
	workHeader := intentHeader(t, prefix+"-work", taskName, now.Add(time.Second))
	workHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	work, err := adapter.decide(
		context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			workHeader, worker, operationID(t, prefix+"-runtime-operation"),
		),
	)
	if err != nil {
		t.Fatalf("commit pending lifecycle Runtime work: %v", err)
	}
	adapter.afterDecision(t, work)
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, taskName), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.query(context.Background(), query)
	if err != nil || len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
		t.Fatalf("query pending lifecycle phase: view=%+v err=%v", view, err)
	}
	phase := view.PhaseRuns[0]
	runtimeHeader := intentHeader(t, prefix+"-runtime", taskName, now.Add(2*time.Second))
	runtimeHeader.ExpectedTaskRevision = view.TaskRevision
	runtimeHeader.ActivityGeneration = view.ActivityGeneration
	runtimeIntent := adapter.runtimeEvidenceIntent(
		t, runtimeHeader, work.EnactmentRefs[0], view, prefix,
	)
	_, err = adapter.decide(context.Background(), runtimeIntent)
	if err != nil {
		t.Fatalf("accept pending lifecycle Runtime evidence: %v", err)
	}
	view, err = adapter.query(context.Background(), query)
	if err != nil {
		t.Fatalf("query before validation: %v", err)
	}
	validationHeader := intentHeader(t, prefix+"-validation", taskName, now.Add(3*time.Second))
	validationHeader.ExpectedTaskRevision = view.TaskRevision
	validationHeader.ActivityGeneration = view.ActivityGeneration
	validation, err := adapter.decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader,
			taskorchestration.NewValidatorAuthority(
				authorityID(t, prefix+"-validator"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, prefix+"-validation-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
				),
				PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation,
				PhaseRunFence: phase.Fence,
				Generation:    taskorchestration.ProducerGeneration(phase.Generation),
				Fence:         taskorchestration.ValidationFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
				Outcome: taskorchestration.PhaseValidationAccepted,
			},
		),
	)
	if err != nil || len(validation.EnactmentRefs) != 1 ||
		validation.EnactmentRefs[0].Kind != taskorchestration.EnactmentTaskWorkspaceLifecycle {
		t.Fatalf("validation did not produce pending lifecycle operation: decision=%+v err=%v", validation, err)
	}
	view, err = adapter.query(context.Background(), query)
	if err != nil {
		t.Fatalf("query pending lifecycle Task: %v", err)
	}
	return sharedPendingLifecycle{
		adapter: adapter, owner: owner, lifecycleAuthority: lifecycleAuthority,
		taskName: taskName, view: view, phase: phase,
		commitOperation: validation.EnactmentRefs[0],
	}
}

func runDecisionCrashAndResponseLossContract(
	t *testing.T,
	factory sharedDecisionFaultAdapterFactory,
) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 21, 0, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "shared-fault-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "shared-fault-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	adapter := factory.new(t, now, owner, worker)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-fault-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "shared-fault-start", "shared-fault-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start shared fault Task: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-fault-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	workHeader := intentHeader(t, "shared-fault-work", "shared-fault-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	workIntent := taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "shared-fault-work-available"),
	)
	adapter.failBeforeCommit(t)
	_, err = adapter.decide(context.Background(), workIntent)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	adapter.restart(t)
	beforeRestart, err := adapter.query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after pre-commit crash: %v", err)
	}
	if beforeRestart.TaskRevision != start.AcceptedTaskRevision ||
		beforeRestart.DecisionCount != 1 || len(beforeRestart.PhaseRuns) != 0 {
		t.Fatalf("pre-commit crash left authoritative facts: %+v", beforeRestart)
	}
	committed, err := adapter.decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("commit original request after restart: %v", err)
	}
	if committed.AcceptedTaskRevision != start.AcceptedTaskRevision+1 ||
		len(committed.EnactmentRefs) != 1 {
		t.Fatalf("post-restart decision did not commit once: %+v", committed)
	}
	adapter.afterDecision(t, committed)
	postCommitCrashIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "shared-post-commit-crash", "shared-post-commit-crash-task", now.Add(2*time.Second)),
		owner,
	)
	adapter.crashAfterCommit(t)
	_, err = adapter.decide(context.Background(), postCommitCrashIntent)
	requireSharedDecisionError(t, err, taskorchestration.ErrorReconciliationRequired)
	adapter.restart(t)
	postCommitRecovered, err := adapter.decide(context.Background(), postCommitCrashIntent)
	if err != nil || postCommitRecovered.AcceptedTaskRevision != 1 {
		t.Fatalf("recover post-commit crash by exact replay: decision=%+v err=%v", postCommitRecovered, err)
	}
	postCommitView, err := adapter.query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-post-commit-crash-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || postCommitView.TaskRevision != 1 || postCommitView.DecisionCount != 1 ||
		postCommitView.LatestDecisionID != postCommitRecovered.DecisionID {
		t.Fatalf("post-commit crash created another Decision: view=%+v err=%v", postCommitView, err)
	}

	lostIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "shared-response-loss", "shared-response-loss-task", now.Add(3*time.Second)),
		owner,
	)
	adapter.loseResponse(t)
	_, err = adapter.decide(context.Background(), lostIntent)
	requireSharedDecisionError(t, err, taskorchestration.ErrorReconciliationRequired)
	adapter.restart(t)
	recovered, err := adapter.decide(context.Background(), lostIntent)
	if err != nil {
		t.Fatalf("recover committed response loss by replay: %v", err)
	}
	replayed, err := adapter.decide(context.Background(), lostIntent)
	if err != nil || replayed.DecisionID != recovered.DecisionID ||
		replayed.AcceptedTaskRevision != recovered.AcceptedTaskRevision {
		t.Fatalf("response-loss replay changed committed Decision: replay=%+v err=%v", replayed, err)
	}
	lostView, err := adapter.query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-response-loss-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query response-loss Task: %v", err)
	}
	if lostView.TaskRevision != 1 || lostView.DecisionCount != 1 ||
		lostView.LatestDecisionID != recovered.DecisionID {
		t.Fatalf("response loss created a second business attempt: %+v", lostView)
	}
}

func runDeliveryFaultContract(
	t *testing.T,
	factory sharedDecisionFaultAdapterFactory,
) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 21, 30, 0, 0, time.UTC)
	current := now.Add(2 * time.Second)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "shared-delivery-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	workerA := taskorchestration.NewWorkerAuthority(
		authorityID(t, "shared-delivery-worker-a"), taskorchestration.AuthorizationGeneration(1),
	)
	workerB := taskorchestration.NewWorkerAuthority(
		authorityID(t, "shared-delivery-worker-b"), taskorchestration.AuthorizationGeneration(1),
	)
	adapter := factory.new(t, now, owner, workerA)
	prerequisitesReady := true
	transport, err := taskorchestration.NewDeterministicOwnedTransport(
		taskorchestration.OwnedTransportConfig{
			SupportedVersion:       taskorchestration.OwnedTransportV1,
			Authorities:            []taskorchestration.WorkerAuthority{workerA, workerB},
			Now:                    func() time.Time { return current },
			PrerequisiteRetryDelay: time.Minute,
			PrerequisitesSatisfied: func(
				context.Context,
				taskorchestration.TaskID,
				taskorchestration.DeliveryPrerequisites,
			) bool {
				return prerequisitesReady
			},
		},
	)
	if err != nil {
		t.Fatalf("create shared fault transport: %v", err)
	}
	dispatcher := adapter.newDispatcher(
		t, func() time.Time { return current },
		[]taskorchestration.WorkerAuthority{workerA, workerB}, transport,
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-delivery-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	commitWork := func(taskName, requestPrefix string) taskorchestration.TransitionDecision {
		started, err := adapter.decide(
			context.Background(),
			taskorchestration.NewStartPinnedTaskIntent(
				intentHeader(t, requestPrefix+"-start", taskName, now), owner, pinned,
			),
		)
		if err != nil {
			t.Fatalf("start %s: %v", taskName, err)
		}
		header := intentHeader(t, requestPrefix+"-work", taskName, now.Add(time.Second))
		header.ExpectedTaskRevision = started.AcceptedTaskRevision
		decision, err := adapter.decide(
			context.Background(),
			taskorchestration.NewMakeWorkAvailableIntent(
				header, workerA, operationID(t, requestPrefix+"-operation"),
			),
		)
		if err != nil {
			t.Fatalf("commit %s work: %v", taskName, err)
		}
		return decision
	}

	claimLossWork := commitWork("shared-claim-loss-task", "shared-claim-loss")
	firstBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerA, Limit: 1,
	})
	if err != nil || len(firstBatch.Claims) != 1 {
		t.Fatalf("claim operation before claim loss: batch=%+v err=%v", firstBatch, err)
	}
	firstClaim := firstBatch.Claims[0]
	current = current.Add(2 * time.Minute)
	recoveredBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerB, Limit: 1,
	})
	if err != nil || len(recoveredBatch.Claims) != 1 ||
		recoveredBatch.Claims[0].OperationID != claimLossWork.EnactmentRefs[0].OperationID ||
		recoveredBatch.Claims[0].LeaseFence <= firstClaim.LeaseFence {
		t.Fatalf("recover original operation after claim loss: batch=%+v err=%v", recoveredBatch, err)
	}
	_, err = dispatcher.Deliver(context.Background(), firstClaim)
	var deliveryErr *taskorchestration.DeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr.Code() != taskorchestration.DeliveryClaimLost {
		t.Fatalf("stale claim delivery = %T %v, want claim lost", err, err)
	}
	accepted, err := dispatcher.Deliver(context.Background(), recoveredBatch.Claims[0])
	if err != nil || accepted.Disposition != taskorchestration.DeliveryAccepted ||
		accepted.OperationID != claimLossWork.EnactmentRefs[0].OperationID {
		t.Fatalf("deliver recovered operation: result=%+v err=%v", accepted, err)
	}
	duplicate, err := transport.Deliver(context.Background(), recoveredBatch.Claims[0].Request)
	if err != nil || duplicate.Outcome != taskorchestration.OwnedTransportAccepted ||
		!duplicate.Duplicate || duplicate.ResultDigest != accepted.ResultDigest {
		t.Fatalf("duplicate owned delivery changed result: response=%+v err=%v", duplicate, err)
	}

	prerequisitesReady = false
	outOfOrderWork := commitWork("shared-out-of-order-task", "shared-out-of-order")
	deferredBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerA, Limit: 1,
	})
	if err != nil || len(deferredBatch.Claims) != 1 ||
		deferredBatch.Claims[0].OperationID != outOfOrderWork.EnactmentRefs[0].OperationID {
		t.Fatalf("claim out-of-order operation: batch=%+v err=%v", deferredBatch, err)
	}
	deferred, err := dispatcher.Deliver(context.Background(), deferredBatch.Claims[0])
	if err != nil || deferred.Disposition != taskorchestration.DeliveryDeferred ||
		deferred.DeferralReason != taskorchestration.OwnedTransportPrerequisiteDeferred ||
		deferred.RetryAt.IsZero() {
		t.Fatalf("defer out-of-order operation: result=%+v err=%v", deferred, err)
	}
	current = deferred.RetryAt
	prerequisitesReady = true
	retryBatch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
		Authority: workerB, Limit: 1,
	})
	if err != nil || len(retryBatch.Claims) != 1 ||
		retryBatch.Claims[0].OperationID != outOfOrderWork.EnactmentRefs[0].OperationID {
		t.Fatalf("reclaim deferred operation: batch=%+v err=%v", retryBatch, err)
	}
	retried, err := dispatcher.Deliver(context.Background(), retryBatch.Claims[0])
	if err != nil || retried.Disposition != taskorchestration.DeliveryAccepted ||
		retried.OperationID != deferred.OperationID || retried.DeliveryCount != 2 {
		t.Fatalf("accept deferred original operation: result=%+v err=%v", retried, err)
	}
	view, err := adapter.query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-out-of-order-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || view.TaskRevision != outOfOrderWork.AcceptedTaskRevision ||
		view.LatestDecisionID != outOfOrderWork.DecisionID {
		t.Fatalf("delivery advanced Task authority: view=%+v err=%v", view, err)
	}

	reconciliationWork := commitWork("shared-reconciliation-task", "shared-reconciliation")
	reconciliationTransport, err := taskorchestration.NewDeterministicOwnedTransport(
		taskorchestration.OwnedTransportConfig{
			SupportedVersion:       taskorchestration.OwnedTransportV1,
			Authorities:            []taskorchestration.WorkerAuthority{workerA, workerB},
			Now:                    func() time.Time { return current },
			PrerequisiteRetryDelay: time.Minute,
			PrerequisitesSatisfied: acceptOwnedTransportPrerequisites,
		},
	)
	if err != nil {
		t.Fatalf("create shared reconciliation transport: %v", err)
	}
	reconciliationDispatcher := adapter.newDispatcher(
		t, func() time.Time { return current },
		[]taskorchestration.WorkerAuthority{workerA, workerB},
		&acknowledgementLosingTransport{owned: reconciliationTransport},
	)
	ambiguousBatch, err := reconciliationDispatcher.Claim(
		context.Background(),
		taskorchestration.DeliveryClaimRequest{Authority: workerA, Limit: 1},
	)
	if err != nil || len(ambiguousBatch.Claims) != 1 ||
		ambiguousBatch.Claims[0].OperationID != reconciliationWork.EnactmentRefs[0].OperationID {
		t.Fatalf("claim reconciliation operation: batch=%+v err=%v", ambiguousBatch, err)
	}
	ambiguous, err := reconciliationDispatcher.Deliver(context.Background(), ambiguousBatch.Claims[0])
	if err != nil || ambiguous.Disposition != taskorchestration.DeliveryReconciliationRequired ||
		ambiguous.OperationID != reconciliationWork.EnactmentRefs[0].OperationID {
		t.Fatalf("acknowledgement loss did not preserve ambiguity: result=%+v err=%v", ambiguous, err)
	}
	reconciled, err := reconciliationDispatcher.Reconcile(
		context.Background(),
		taskorchestration.DeliveryReconcileRequest{
			Authority: workerA, OperationID: ambiguous.OperationID,
		},
	)
	if err != nil || reconciled.Disposition != taskorchestration.DeliveryAccepted ||
		reconciled.OperationID != ambiguous.OperationID || reconciled.DeliveryCount != 1 {
		t.Fatalf("reconcile original accepted operation: result=%+v err=%v", reconciled, err)
	}
	reconciledView, err := adapter.query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-reconciliation-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || reconciledView.TaskRevision != reconciliationWork.AcceptedTaskRevision ||
		reconciledView.LatestDecisionID != reconciliationWork.DecisionID {
		t.Fatalf("reconciler advanced Task authority: view=%+v err=%v", reconciledView, err)
	}
}

func newInMemoryDecisionFaultAdapter(
	t *testing.T,
	now time.Time,
	_ taskorchestration.UserAuthority,
	_ taskorchestration.WorkerAuthority,
) sharedDecisionFaultAdapter {
	t.Helper()
	current, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create in-memory fault adapter: %v", err)
	}
	return sharedDecisionFaultAdapter{
		decide: func(ctx context.Context, intent taskorchestration.TransitionIntent) (
			taskorchestration.TransitionDecision,
			error,
		) {
			return current.Mutations.Decide(ctx, intent)
		},
		query: func(ctx context.Context, query taskorchestration.TaskQuery) (
			taskorchestration.TaskOrchestrationView,
			error,
		) {
			return current.Queries.Query(ctx, query)
		},
		newDispatcher: func(
			t *testing.T,
			now func() time.Time,
			authorities []taskorchestration.WorkerAuthority,
			transport taskorchestration.OwnedTransport,
		) taskorchestration.OutboxDispatcher {
			t.Helper()
			dispatcher, err := current.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
				Now: now, MaxBatchSize: 1, LeaseDuration: time.Minute,
				TransportVersion: taskorchestration.OwnedTransportV1,
				Authorities:      authorities,
			}, transport)
			if err != nil {
				t.Fatalf("create shared in-memory dispatcher: %v", err)
			}
			return dispatcher
		},
		failBeforeCommit: func(t *testing.T) {
			t.Helper()
			if err := current.CrashNextAt(taskorchestration.CrashBeforeCommit); err != nil {
				t.Fatalf("inject in-memory pre-commit crash: %v", err)
			}
		},
		crashAfterCommit: func(t *testing.T) {
			t.Helper()
			if err := current.CrashNextAt(taskorchestration.CrashAfterCommit); err != nil {
				t.Fatalf("inject in-memory post-commit crash: %v", err)
			}
		},
		loseResponse: func(*testing.T) { current.LoseNextResponse() },
		restart:      func(*testing.T) { current = current.Restart() },
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
		runtimeEvidenceIntent: directSharedFaultRuntimeEvidenceIntent,
	}
}

func newPostgresDecisionFaultAdapter(
	t *testing.T,
	now time.Time,
	_ taskorchestration.UserAuthority,
	_ taskorchestration.WorkerAuthority,
) sharedDecisionFaultAdapter {
	t.Helper()
	db, schema := isolatedPostgresSchema(t)
	faults := &taskorchestration.PersistenceFaultController{}
	current := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, Faults: faults,
	})
	return sharedPostgresDecisionFaultAdapter(t, now, db, schema, faults, &current)
}

func newOwnedDecisionFaultAdapter(
	t *testing.T,
	now time.Time,
	_ taskorchestration.UserAuthority,
	worker taskorchestration.WorkerAuthority,
) sharedDecisionFaultAdapter {
	t.Helper()
	db, schema := isolatedPostgresSchema(t)
	faults := &taskorchestration.PersistenceFaultController{}
	current := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, Faults: faults,
	})
	result := sharedPostgresDecisionFaultAdapter(t, now, db, schema, faults, &current)
	evidencePort := &sharedRuntimeEvidencePort{}
	evidenceAdapter := taskorchestration.NewRuntimeEvidenceAdapter(
		evidencePort, sharedPrerequisiteAuthority{},
	)
	deliveryNow := now.Add(5 * time.Second)
	transport, err := taskorchestration.NewDeterministicOwnedTransport(
		taskorchestration.OwnedTransportConfig{
			SupportedVersion:       taskorchestration.OwnedTransportV1,
			Authorities:            []taskorchestration.WorkerAuthority{worker},
			Now:                    func() time.Time { return deliveryNow },
			PrerequisiteRetryDelay: time.Minute,
			PrerequisitesSatisfied: func(
				context.Context,
				taskorchestration.TaskID,
				taskorchestration.DeliveryPrerequisites,
			) bool {
				return true
			},
		},
	)
	if err != nil {
		t.Fatalf("create fault-matrix owned transport: %v", err)
	}
	result.afterDecision = func(t *testing.T, decision taskorchestration.TransitionDecision) {
		t.Helper()
		dispatcher, err := current.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
			Now: func() time.Time { return deliveryNow }, MaxBatchSize: 1,
			LeaseDuration: time.Minute, TransportVersion: taskorchestration.OwnedTransportV1,
			Authorities: []taskorchestration.WorkerAuthority{worker},
		}, transport)
		if err != nil {
			t.Fatalf("create fault-matrix owned dispatcher: %v", err)
		}
		batch, err := dispatcher.Claim(context.Background(), taskorchestration.DeliveryClaimRequest{
			Authority: worker, Limit: 1,
		})
		if err != nil || len(batch.Claims) != 1 ||
			batch.Claims[0].OperationID != decision.EnactmentRefs[0].OperationID {
			t.Fatalf("claim original operation after decision restart: batch=%+v err=%v", batch, err)
		}
		delivered, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
		if err != nil || delivered.Disposition != taskorchestration.DeliveryAccepted {
			t.Fatalf("deliver original operation after decision restart: result=%+v err=%v", delivered, err)
		}
	}
	result.runtimeEvidenceIntent = func(
		t *testing.T,
		header taskorchestration.IntentHeader,
		ref taskorchestration.EnactmentRef,
		view taskorchestration.TaskOrchestrationView,
		prefix string,
	) taskorchestration.TransitionIntent {
		t.Helper()
		phase := view.PhaseRuns[0]
		runtimeFence, ok := ref.Fence.(taskorchestration.RuntimeFence)
		if !ok {
			t.Fatalf("fault-matrix Runtime enactment has non-Runtime fence: %+v", ref)
		}
		record := taskorchestration.RuntimeEvidenceRecord{
			SchemaVersion: taskorchestration.EvidenceSchemaV1,
			EvidenceID:    evidenceID(t, prefix+"-owned-runtime-evidence"),
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, prefix+"-owned-runtime-producer"),
				Generation:  taskorchestration.AuthorizationGeneration(1),
			},
			TaskID: view.TaskID, PhaseRunID: phase.PhaseRunID,
			PhaseRunGeneration: phase.Generation, PhaseRunFence: phase.Fence,
			RuntimeRunID:                 phase.RuntimeRuns[0].RuntimeRunID,
			RuntimeBindingID:             downstreamRuntimeBindingID(t, prefix+"-owned-runtime-binding"),
			RuntimeBindingDigest:         evidenceDigest(t, "7777777777777777777777777777777777777777777777777777777777777777"),
			ImmutableInputManifestDigest: evidenceDigest(t, "8888888888888888888888888888888888888888888888888888888888888888"),
			ExecutionNodeID:              downstreamExecutionNodeID(t, prefix+"-owned-runtime-node"),
			SandboxLeaseID:               downstreamSandboxLeaseID(t, prefix+"-owned-runtime-lease"),
			OutputManifestDigest:         evidenceDigest(t, "9999999999999999999999999999999999999999999999999999999999999999"),
			OperationID:                  ref.OperationID, ActivityGeneration: ref.ActivityGeneration,
			Generation: taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:      runtimeFence, SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		}
		record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
		evidencePort.record = record
		evidence, err := evidenceAdapter.Enact(context.Background(), ref)
		if err != nil {
			t.Fatalf("fault-matrix owned Runtime evidence adapter: %v", err)
		}
		intent, err := evidence.Intent(header)
		if err != nil {
			t.Fatalf("fault-matrix owned Runtime evidence Intent: %v", err)
		}
		return intent
	}
	return result
}

func sharedPostgresDecisionFaultAdapter(
	t *testing.T,
	now time.Time,
	db *sql.DB,
	schema string,
	faults *taskorchestration.PersistenceFaultController,
	current **taskorchestration.PostgresAdapter,
) sharedDecisionFaultAdapter {
	t.Helper()
	return sharedDecisionFaultAdapter{
		decide: func(ctx context.Context, intent taskorchestration.TransitionIntent) (
			taskorchestration.TransitionDecision,
			error,
		) {
			return (*current).Decide(ctx, intent)
		},
		query: func(ctx context.Context, query taskorchestration.TaskQuery) (
			taskorchestration.TaskOrchestrationView,
			error,
		) {
			return (*current).Query(ctx, query)
		},
		runtimeEvidenceIntent: directSharedFaultRuntimeEvidenceIntent,
		newDispatcher: func(
			t *testing.T,
			now func() time.Time,
			authorities []taskorchestration.WorkerAuthority,
			transport taskorchestration.OwnedTransport,
		) taskorchestration.OutboxDispatcher {
			t.Helper()
			dispatcher, err := (*current).NewOutboxDispatcher(taskorchestration.DispatcherConfig{
				Now: now, MaxBatchSize: 1, LeaseDuration: time.Minute,
				TransportVersion: taskorchestration.OwnedTransportV1,
				Authorities:      authorities,
			}, transport)
			if err != nil {
				t.Fatalf("create shared PostgreSQL dispatcher: %v", err)
			}
			return dispatcher
		},
		failBeforeCommit: func(t *testing.T) {
			t.Helper()
			if err := faults.FailNextAt(taskorchestration.PersistenceFaultBeforeCommit); err != nil {
				t.Fatalf("inject PostgreSQL pre-commit crash: %v", err)
			}
		},
		crashAfterCommit: func(t *testing.T) {
			t.Helper()
			if err := faults.FailNextAt(taskorchestration.PersistenceFaultAfterCommit); err != nil {
				t.Fatalf("inject PostgreSQL post-commit crash: %v", err)
			}
		},
		loseResponse: func(t *testing.T) {
			t.Helper()
			if err := faults.FailNextAt(taskorchestration.PersistenceFaultBeforeResponse); err != nil {
				t.Fatalf("inject PostgreSQL response loss: %v", err)
			}
		},
		restart: func(*testing.T) {
			*current = newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
				Now: func() time.Time { return now }, Faults: faults,
			})
		},
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
	}
}

func directSharedFaultRuntimeEvidenceIntent(
	t *testing.T,
	header taskorchestration.IntentHeader,
	ref taskorchestration.EnactmentRef,
	view taskorchestration.TaskOrchestrationView,
	prefix string,
) taskorchestration.TransitionIntent {
	t.Helper()
	phase := view.PhaseRuns[0]
	return taskorchestration.NewAcceptRuntimeEvidenceIntent(
		header,
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, prefix+"-runtime"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, prefix+"-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "9999999999999999999999999999999999999999999999999999999999999999"),
			),
			PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation,
			PhaseRunFence: phase.Fence, RuntimeRunID: phase.RuntimeRuns[0].RuntimeRunID,
			OperationID: ref.OperationID,
			Generation:  taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:       taskorchestration.RuntimeFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)
}
