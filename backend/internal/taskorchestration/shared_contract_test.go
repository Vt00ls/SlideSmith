package taskorchestration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestSharedTaskOrchestrationAdapterContract(t *testing.T) {
	runSharedTaskOrchestrationAdapterContract(t)
}

func TestSharedTaskOrchestrationCoordinationContract(t *testing.T) {
	runSharedTaskOrchestrationCoordinationContract(t)
}

type sharedTaskOrchestrationAdapter struct {
	mutations                taskorchestration.TaskOrchestration
	queries                  taskorchestration.TaskOrchestrationQuery
	afterDecision            func(*testing.T, taskorchestration.TransitionDecision)
	runtimeEvidence          func(*testing.T, taskorchestration.TransitionDecision) taskorchestration.RuntimeAdapterEvidence
	roundTripRuntimeEvidence func(*testing.T, taskorchestration.TransitionDecision) taskorchestration.TransitionDecision
}

type sharedTaskOrchestrationAdapterFactory struct {
	name string
	new  func(
		*testing.T,
		time.Time,
		taskorchestration.UserAuthority,
		taskorchestration.WorkerAuthority,
	) sharedTaskOrchestrationAdapter
}

func runSharedTaskOrchestrationAdapterContract(t *testing.T) {
	t.Helper()
	for _, factory := range sharedTaskOrchestrationAdapterFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			runSharedMinimalContract(t, factory)
			runSharedRuntimeRetryContract(t, factory)
			runSharedManualEditReconstructionContract(t, factory)
		})
	}
}

func runSharedManualEditReconstructionContract(
	t *testing.T,
	factory sharedTaskOrchestrationAdapterFactory,
) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 20, 25, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(authorityID(t, "shared-reconstruction-owner"), 1)
	worker := taskorchestration.NewWorkerAuthority(authorityID(t, "shared-reconstruction-worker"), 1)
	adapter := factory.new(t, now, owner, worker)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-reconstruction-publication"), Kind: taskorchestration.PhasePublication,
	}})
	pinned.ExecutionLock.PipelineContract.ManualEditEntryPhase = phaseKey(t, "shared-reconstruction-edit")
	pinned.ExecutionLock.PipelineContract.ManualEditPhases = []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-reconstruction-edit"), Kind: taskorchestration.PhaseMutating,
		ValidationContract: taskorchestration.PhaseValidationAllRuntimeRunsSucceeded, RequiredRuntimeRuns: 1,
	}}
	started, err := adapter.mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "shared-reconstruction-start", "shared-reconstruction-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start shared reconstruction Task: %v", err)
	}
	workHeader := intentHeader(t, "shared-reconstruction-publication-work", "shared-reconstruction-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	publicationWork, err := adapter.mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "shared-reconstruction-publication-available"),
	))
	if err != nil {
		t.Fatalf("make shared publication available: %v", err)
	}
	adapter.afterDecision(t, publicationWork)
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, "shared-reconstruction-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.queries.Query(context.Background(), query)
	if err != nil || len(view.PhaseRuns) != 1 {
		t.Fatalf("query shared publication Phase: view=%+v err=%v", view, err)
	}
	run := view.PhaseRuns[0]
	reconstructionRef := downstreamEnactmentRef(
		t, "shared-reconstruction-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(2),
	)
	reconstructionRequest, _ := c04ReconstructionContractFixture(
		t, reconstructionRef, "shared-reconstruction-task", pinned.TaskWorkspaceID.String(), 2, 2,
	)
	reconstructionBinding, err := taskorchestration.NewTaskWorkspaceReconstructionRequestBinding(
		reconstructionRequest,
	)
	if err != nil {
		t.Fatalf("bind exact shared C04 reconstruction request: %v", err)
	}
	artifact := artifactVersionID(
		t, string(reconstructionRequest.Intent.ArtifactVersionInput.ArtifactVersionID),
	)
	publicationHeader := intentHeader(t, "shared-reconstruction-published", "shared-reconstruction-task", now.Add(2*time.Second))
	publicationHeader.ExpectedTaskRevision = publicationWork.AcceptedTaskRevision
	published, err := adapter.mutations.Decide(context.Background(), taskorchestration.NewAcceptPublicationEvidenceIntent(
		publicationHeader, pinned.Authorities.Publication, taskorchestration.PublicationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "shared-reconstruction-publication-evidence"), taskorchestration.EvidencePublication,
				evidenceDigest(t, "7474747474747474747474747474747474747474747474747474747474747474"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			OperationID: publicationWork.EnactmentRefs[0].OperationID,
			Generation:  taskorchestration.ProducerGeneration(run.Generation),
			Fence:       taskorchestration.PublicationFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.PublicationActivated, ArtifactVersionID: artifact,
		},
	))
	if err != nil {
		t.Fatalf("accept shared publication evidence: %v", err)
	}
	beginHeader := intentHeader(t, "shared-reconstruction-begin", "shared-reconstruction-task", now.Add(3*time.Second))
	beginHeader.ExpectedTaskRevision = published.AcceptedTaskRevision
	beginHeader.ActivityGeneration = 2
	beginIntent := taskorchestration.NewBeginManualEditAfterExpiryIntent(
		beginHeader, owner, artifact, reconstructionBinding,
	)
	reconstruction, err := adapter.mutations.Decide(context.Background(), beginIntent)
	if err != nil || len(reconstruction.EnactmentRefs) != 1 || len(reconstruction.AffectedPhaseRuns) != 0 {
		t.Fatalf("begin shared reconstruction: decision=%+v err=%v", reconstruction, err)
	}
	adapter.afterDecision(t, reconstruction)
	replayed, err := adapter.mutations.Decide(context.Background(), beginIntent)
	if err != nil || replayed.DecisionID != reconstruction.DecisionID ||
		replayed.EnactmentRefs[0].OperationID != reconstruction.EnactmentRefs[0].OperationID {
		t.Fatalf("shared reconstruction replay diverged: decision=%+v err=%v", replayed, err)
	}
	view, err = adapter.queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query pending shared reconstruction: %v", err)
	}
	evidenceHeader := intentHeader(t, "shared-reconstruction-evidence", "shared-reconstruction-task", now.Add(4*time.Second))
	evidenceHeader.ExpectedTaskRevision = reconstruction.AcceptedTaskRevision
	evidenceHeader.ActivityGeneration = 2
	evidence := taskorchestration.TaskWorkspaceReconstructionEvidenceBinding{
		Evidence: taskorchestration.NewEvidenceRef(
			evidenceID(t, "shared-reconstruction-evidence-ref"), taskorchestration.EvidenceTaskWorkspaceLifecycle,
			evidenceDigest(t, "7575757575757575757575757575757575757575757575757575757575757575"),
		),
		OperationID: reconstruction.EnactmentRefs[0].OperationID, ArtifactVersionID: artifact,
		RevisionID:   taskWorkspaceRevisionID(t, "shared-reconstructed-revision"),
		CheckpointID: checkpointID(t, "shared-reconstructed-checkpoint"),
		Generation:   2, Fence: 2, ObservedGeneration: 3, ObservedFence: 3,
		SafetyEpoch: view.SafetyEpoch,
	}
	accepted, err := adapter.mutations.Decide(context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceReconstructionEvidenceIntent(
			evidenceHeader, pinned.Authorities.TaskWorkspaceLifecycle, evidence,
		))
	if err != nil || len(accepted.AcceptedEvidenceRefs) != 1 || len(accepted.AffectedPhaseRuns) != 0 ||
		accepted.TaskProjection.LatestRevisionID != evidence.RevisionID ||
		accepted.TaskProjection.LatestCheckpointID != evidence.CheckpointID ||
		accepted.TaskProjection.TaskWorkspaceLifecycleGeneration != evidence.ObservedGeneration ||
		accepted.TaskProjection.TaskWorkspaceLifecycleFence != evidence.ObservedFence {
		t.Fatalf("accept shared reconstruction evidence: decision=%+v err=%v", accepted, err)
	}
	view, err = adapter.queries.Query(context.Background(), query)
	if err != nil || view.TaskWorkspaceLifecycleGeneration != evidence.ObservedGeneration ||
		view.TaskWorkspaceLifecycleFence != evidence.ObservedFence {
		t.Fatalf("persist shared reconstruction lineage: view=%+v err=%v", view, err)
	}
	manualHeader := intentHeader(t, "shared-reconstruction-manual-work", "shared-reconstruction-task", now.Add(5*time.Second))
	manualHeader.ExpectedTaskRevision = accepted.AcceptedTaskRevision
	manualHeader.ActivityGeneration = 2
	manual, err := adapter.mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		manualHeader, worker, operationID(t, "shared-reconstruction-manual-available"),
	))
	if err != nil || len(manual.EnactmentRefs) != 1 ||
		manual.EnactmentRefs[0].Kind != taskorchestration.EnactmentRuntimeExecution {
		t.Fatalf("shared manual work did not wait for reconstruction: decision=%+v err=%v", manual, err)
	}
}

func runSharedRuntimeRetryContract(t *testing.T, factory sharedTaskOrchestrationAdapterFactory) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 20, 15, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(authorityID(t, "shared-runtime-retry-owner"), 1)
	worker := taskorchestration.NewWorkerAuthority(authorityID(t, "shared-runtime-retry-worker"), 1)
	adapter := factory.new(t, now, owner, worker)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-runtime-retry-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1, RetryEligible: true,
	}})
	started, err := adapter.mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "shared-runtime-retry-start", "shared-runtime-retry-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start shared Runtime retry Task: %v", err)
	}
	workHeader := intentHeader(t, "shared-runtime-retry-work", "shared-runtime-retry-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	work, err := adapter.mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "shared-runtime-retry-work-available"),
	))
	if err != nil {
		t.Fatalf("make shared Runtime retry work available: %v", err)
	}
	adapter.afterDecision(t, work)
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-runtime-retry-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query shared Runtime retry scope: %v", err)
	}
	phase := view.PhaseRuns[0]
	failedRuntime := phase.RuntimeRuns[0]
	failedHeader := intentHeader(t, "shared-runtime-retry-failed", "shared-runtime-retry-task", now.Add(2*time.Second))
	failedHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	failed, err := adapter.mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		failedHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "shared-runtime-retry-failed-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "7171717171717171717171717171717171717171717171717171717171717171"),
			),
			PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation, PhaseRunFence: phase.Fence,
			RuntimeRunID: failedRuntime.RuntimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:      taskorchestration.RuntimeFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunFailed,
		},
	))
	if err != nil {
		t.Fatalf("accept shared failed Runtime evidence: %v", err)
	}
	retryHeader := intentHeader(t, "shared-runtime-retry-command", "shared-runtime-retry-task", now.Add(3*time.Second))
	retryHeader.ExpectedTaskRevision = failed.AcceptedTaskRevision
	retryIntent := taskorchestration.NewRetryRuntimeRunIntent(
		retryHeader, worker, phase.PhaseRunID, failedRuntime.RuntimeRunID,
	)
	retried, err := adapter.mutations.Decide(context.Background(), retryIntent)
	if err != nil {
		t.Fatalf("commit shared Runtime retry: %v", err)
	}
	adapter.afterDecision(t, retried)
	view, err = adapter.queries.Query(context.Background(), query)
	if err != nil || len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 2 ||
		view.PhaseRuns[0].PhaseRunID != phase.PhaseRunID ||
		view.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID != failedRuntime.RuntimeRunID ||
		view.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunFailed ||
		view.PhaseRuns[0].RuntimeRuns[1].Outcome != taskorchestration.RuntimeRunPending ||
		len(retried.EnactmentRefs) != 1 ||
		retried.EnactmentRefs[0].OperationID == work.EnactmentRefs[0].OperationID {
		t.Fatalf("shared Runtime retry contract diverged: decision=%+v view=%+v err=%v", retried, view, err)
	}
	replayed, err := adapter.mutations.Decide(context.Background(), retryIntent)
	if err != nil || replayed.DecisionID != retried.DecisionID ||
		replayed.EnactmentRefs[0].OperationID != retried.EnactmentRefs[0].OperationID {
		t.Fatalf("shared Runtime retry replay diverged: decision=%+v err=%v", replayed, err)
	}
}

func sharedTaskOrchestrationAdapterFactories() []sharedTaskOrchestrationAdapterFactory {
	return []sharedTaskOrchestrationAdapterFactory{
		{name: "in_memory", new: newSharedInMemoryAdapter},
		{name: "postgres_owned_persistence", new: newSharedPostgresAdapter},
		{name: "owned_transport_evidence_to_postgres", new: newSharedOwnedTransportAdapter},
	}
}

func runSharedMinimalContract(t *testing.T, factory sharedTaskOrchestrationAdapterFactory) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 20, 0, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "shared-contract-owner"),
		taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "shared-contract-worker"),
		taskorchestration.AuthorizationGeneration(1),
	)
	adapter := factory.new(t, now, owner, worker)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-contract-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})

	started, err := adapter.mutations.Decide(
		context.Background(),
		verifiedPinnedStartIntent(t,
			intentHeader(t, "shared-contract-start", "shared-contract-task", now),
			owner,
			pinned,
		),
	)
	if err != nil {
		t.Fatalf("start through shared Decide seam: %v", err)
	}
	if started.PreviousTaskRevision != 0 || started.AcceptedTaskRevision != 1 ||
		started.TaskProjection.TaskRevision != 1 {
		t.Fatalf("start decision omitted committed revision: %+v", started)
	}

	workHeader := intentHeader(
		t, "shared-contract-work", "shared-contract-task", now.Add(time.Second),
	)
	workHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	work, err := adapter.mutations.Decide(
		context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			workHeader, worker, operationID(t, "shared-contract-work-available"),
		),
	)
	if err != nil {
		t.Fatalf("make work available through shared Decide seam: %v", err)
	}
	if work.AcceptedTaskRevision != 2 || len(work.AffectedPhaseRuns) != 1 ||
		len(work.EnactmentRefs) != 1 {
		t.Fatalf("work decision omitted committed public facts: %+v", work)
	}
	adapter.afterDecision(t, work)
	evidenceDecision := adapter.roundTripRuntimeEvidence(t, work)

	view, err := adapter.queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-contract-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query through shared read-only seam: %v", err)
	}
	if view.TaskRevision != 3 || view.LatestDecisionID != evidenceDecision.DecisionID ||
		len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 ||
		view.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunSucceeded {
		t.Fatalf("shared Query did not return committed aggregate: %+v", view)
	}
}

func runSharedTaskOrchestrationCoordinationContract(t *testing.T) {
	t.Helper()
	for _, factory := range sharedTaskOrchestrationAdapterFactories() {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			runSharedCoordinationContract(t, factory)
		})
	}
}

func runSharedCoordinationContract(t *testing.T, factory sharedTaskOrchestrationAdapterFactory) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 20, 30, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "shared-coordination-owner"),
		taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "shared-coordination-worker"),
		taskorchestration.AuthorizationGeneration(1),
	)
	adapter := factory.new(t, now, owner, worker)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-coordination-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	started, err := adapter.mutations.Decide(context.Background(),
		verifiedPinnedStartIntent(t,
			intentHeader(t, "shared-coordination-start", "shared-coordination-task", now),
			owner, pinned,
		))
	if err != nil {
		t.Fatalf("start shared coordination Task: %v", err)
	}
	workHeader := intentHeader(
		t, "shared-coordination-work", "shared-coordination-task", now.Add(time.Second),
	)
	workHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	workIntent := taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "shared-coordination-work-available"),
	)
	work, err := adapter.mutations.Decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("commit shared coordination work: %v", err)
	}
	adapter.afterDecision(t, work)

	replayed, err := adapter.mutations.Decide(context.Background(), workIntent)
	if err != nil || replayed.DecisionID != work.DecisionID ||
		replayed.AcceptedTaskRevision != work.AcceptedTaskRevision ||
		replayed.EnactmentRefs[0].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatalf("exact replay changed committed facts: decision=%+v err=%v", replayed, err)
	}

	conflictHeader := intentHeader(
		t, "shared-coordination-work", "shared-coordination-task", now.Add(time.Second),
	)
	conflictHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	_, err = adapter.mutations.Decide(
		context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			conflictHeader, worker, operationID(t, "shared-coordination-rebound"),
		),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorIntegrityConflict)

	staleRevisionHeader := intentHeader(
		t, "shared-coordination-stale-revision", "shared-coordination-task", now.Add(2*time.Second),
	)
	staleRevisionHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	_, err = adapter.mutations.Decide(context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			staleRevisionHeader, worker, operationID(t, "shared-coordination-stale-revision-op"),
		))
	requireSharedDecisionError(t, err, taskorchestration.ErrorStaleTaskRevision)

	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-coordination-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query shared coordination Task: %v", err)
	}
	if len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
		t.Fatalf("shared coordination phase binding missing: %+v", view)
	}
	phase := view.PhaseRuns[0]
	runtimeEvidence := adapter.runtimeEvidence(t, work)
	staleEvidence := func(
		request string,
		activity taskorchestration.ActivityGeneration,
		fence taskorchestration.RuntimeFence,
	) taskorchestration.TransitionIntent {
		header := intentHeader(t, request, "shared-coordination-task", now.Add(3*time.Second))
		header.ExpectedTaskRevision = work.AcceptedTaskRevision
		header.ActivityGeneration = activity
		candidate := runtimeEvidence
		candidate.ActivityGeneration = activity
		candidate.Fence = fence
		intent, intentErr := candidate.Intent(header)
		if intentErr != nil {
			t.Fatalf("translate shared Runtime evidence through public Intent seam: %v", intentErr)
		}
		return intent
	}
	_, err = adapter.mutations.Decide(context.Background(), staleEvidence(
		"shared-coordination-stale-generation",
		view.ActivityGeneration+1,
		taskorchestration.RuntimeFence(phase.Fence),
	))
	requireSharedDecisionError(t, err, taskorchestration.ErrorStaleAuthority)
	_, err = adapter.mutations.Decide(context.Background(), staleEvidence(
		"shared-coordination-stale-fence",
		view.ActivityGeneration,
		taskorchestration.RuntimeFence(phase.Fence+1),
	))
	requireSharedDecisionError(t, err, taskorchestration.ErrorStaleAuthority)

	after, err := adapter.queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after stale shared coordination inputs: %v", err)
	}
	if after.TaskRevision != work.AcceptedTaskRevision ||
		after.LatestDecisionID != work.DecisionID || after.DecisionCount != 2 ||
		after.EvidenceDiagnosticCount != 2 {
		t.Fatalf("replay/conflict/stale inputs changed Task authority: %+v", after)
	}
	validHeader := intentHeader(
		t, "shared-coordination-valid-evidence", "shared-coordination-task", now.Add(4*time.Second),
	)
	validHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	validHeader.ActivityGeneration = view.ActivityGeneration
	validIntent, err := runtimeEvidence.Intent(validHeader)
	if err != nil {
		t.Fatalf("translate valid shared Runtime evidence through public Intent seam: %v", err)
	}
	acceptedEvidence, err := adapter.mutations.Decide(
		context.Background(),
		validIntent,
	)
	if err != nil {
		t.Fatalf("accept shared Runtime evidence: %v", err)
	}
	duplicateHeader := validHeader
	duplicateHeader.DecisionRequestID = decisionRequestID(t, "shared-coordination-duplicate-evidence")
	duplicateIntent, err := runtimeEvidence.Intent(duplicateHeader)
	if err != nil {
		t.Fatalf("translate duplicate shared Runtime evidence through public Intent seam: %v", err)
	}
	duplicate, err := adapter.mutations.Decide(
		context.Background(),
		duplicateIntent,
	)
	if err != nil || duplicate.DecisionID != acceptedEvidence.DecisionID ||
		duplicate.AcceptedTaskRevision != acceptedEvidence.AcceptedTaskRevision {
		t.Fatalf("duplicate exact evidence changed Decision: duplicate=%+v err=%v", duplicate, err)
	}
	afterDuplicate, err := adapter.queries.Query(context.Background(), query)
	if err != nil || afterDuplicate.TaskRevision != acceptedEvidence.AcceptedTaskRevision ||
		afterDuplicate.DecisionCount != 3 {
		t.Fatalf("duplicate evidence advanced Task twice: view=%+v err=%v", afterDuplicate, err)
	}
}

func requireSharedDecisionError(t *testing.T, err error, code taskorchestration.ErrorCode) {
	t.Helper()
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != code {
		t.Fatalf("decision error = %T %v, want code %v", err, err, code)
	}
}

func newSharedInMemoryAdapter(
	t *testing.T,
	now time.Time,
	owner taskorchestration.UserAuthority,
	_ taskorchestration.WorkerAuthority,
) sharedTaskOrchestrationAdapter {
	t.Helper()
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create shared in-memory adapter: %v", err)
	}
	runtimeEvidence := directSharedRuntimeEvidence(owner, harness.Queries)
	return sharedTaskOrchestrationAdapter{
		mutations: harness.Mutations,
		queries:   harness.Queries,
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
		runtimeEvidence: runtimeEvidence,
		roundTripRuntimeEvidence: sharedRuntimeEvidenceRoundTrip(
			now, harness.Mutations, runtimeEvidence,
		),
	}
}

func newSharedPostgresAdapter(
	t *testing.T,
	now time.Time,
	owner taskorchestration.UserAuthority,
	_ taskorchestration.WorkerAuthority,
) sharedTaskOrchestrationAdapter {
	t.Helper()
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	runtimeEvidence := directSharedRuntimeEvidence(owner, adapter)
	return sharedTaskOrchestrationAdapter{
		mutations: adapter,
		queries:   adapter,
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
		runtimeEvidence: runtimeEvidence,
		roundTripRuntimeEvidence: sharedRuntimeEvidenceRoundTrip(
			now, adapter, runtimeEvidence,
		),
	}
}

func newSharedOwnedTransportAdapter(
	t *testing.T,
	now time.Time,
	owner taskorchestration.UserAuthority,
	worker taskorchestration.WorkerAuthority,
) sharedTaskOrchestrationAdapter {
	t.Helper()
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	deliveryNow := now.Add(2 * time.Second)
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
		t.Fatalf("create shared owned transport: %v", err)
	}
	dispatcher, err := adapter.NewOutboxDispatcher(taskorchestration.DispatcherConfig{
		Now:              func() time.Time { return deliveryNow },
		MaxBatchSize:     1,
		LeaseDuration:    time.Minute,
		TransportVersion: taskorchestration.OwnedTransportV1,
		Authorities:      []taskorchestration.WorkerAuthority{worker},
	}, transport)
	if err != nil {
		t.Fatalf("create shared PostgreSQL dispatcher: %v", err)
	}
	evidencePort := &sharedRuntimeEvidencePort{}
	evidenceAdapter := taskorchestration.NewRuntimeEvidenceAdapter(
		evidencePort, sharedPrerequisiteAuthority{},
	)
	runtimeEvidence := func(
		t *testing.T,
		decision taskorchestration.TransitionDecision,
	) taskorchestration.RuntimeAdapterEvidence {
		t.Helper()
		view, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
			TaskID:    decision.TaskProjection.TaskID,
			Authority: taskorchestration.NewUserQueryAuthority(owner),
		})
		if err != nil || len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
			t.Fatalf("query Runtime evidence scope: view=%+v err=%v", view, err)
		}
		ref := decision.EnactmentRefs[0]
		phase := view.PhaseRuns[0]
		runtimeFence, ok := ref.Fence.(taskorchestration.RuntimeFence)
		if !ok {
			t.Fatalf("Runtime enactment has non-Runtime fence: %+v", ref)
		}
		record := taskorchestration.RuntimeEvidenceRecord{
			SchemaVersion: taskorchestration.EvidenceSchemaV1,
			EvidenceID:    evidenceID(t, "shared-owned-runtime-evidence"),
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, "generation-runtime-authority"),
				Generation:  taskorchestration.AuthorizationGeneration(1),
			},
			TaskID:                       decision.TaskProjection.TaskID,
			PhaseRunID:                   phase.PhaseRunID,
			PhaseRunGeneration:           phase.Generation,
			PhaseRunFence:                phase.Fence,
			RuntimeRunID:                 phase.RuntimeRuns[0].RuntimeRunID,
			RuntimeBindingID:             downstreamRuntimeBindingID(t, "shared-owned-runtime-binding"),
			RuntimeBindingDigest:         evidenceDigest(t, "2222222222222222222222222222222222222222222222222222222222222222"),
			ImmutableInputManifestDigest: evidenceDigest(t, "3333333333333333333333333333333333333333333333333333333333333333"),
			ExecutionNodeID:              downstreamExecutionNodeID(t, "shared-owned-runtime-node"),
			SandboxLeaseID:               downstreamSandboxLeaseID(t, "shared-owned-runtime-lease"),
			OutputManifestDigest:         evidenceDigest(t, "4444444444444444444444444444444444444444444444444444444444444444"),
			OperationID:                  ref.OperationID,
			ActivityGeneration:           ref.ActivityGeneration,
			Generation:                   taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:                        runtimeFence,
			SafetyEpoch:                  view.SafetyEpoch,
			Outcome:                      taskorchestration.RuntimeRunSucceeded,
		}
		record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
		evidencePort.record = record
		evidence, err := evidenceAdapter.Enact(context.Background(), ref)
		if err != nil || evidence.OperationID != ref.OperationID ||
			evidence.Evidence.ID != record.EvidenceID ||
			evidence.Evidence.Digest != record.EvidenceDigest {
			t.Fatalf("enact through owned Runtime evidence adapter: evidence=%+v err=%v", evidence, err)
		}
		return evidence
	}
	return sharedTaskOrchestrationAdapter{
		mutations: adapter,
		queries:   adapter,
		afterDecision: func(t *testing.T, decision taskorchestration.TransitionDecision) {
			t.Helper()
			batch, err := dispatcher.Claim(
				context.Background(),
				taskorchestration.DeliveryClaimRequest{Authority: worker, Limit: 1},
			)
			if err != nil || len(batch.Claims) != 1 {
				t.Fatalf("claim shared committed enactment: count=%d err=%v", len(batch.Claims), err)
			}
			if batch.Claims[0].OperationID != decision.EnactmentRefs[0].OperationID {
				t.Fatal("owned transport changed the committed OperationID")
			}
			delivered, err := dispatcher.Deliver(context.Background(), batch.Claims[0])
			if err != nil || delivered.Disposition != taskorchestration.DeliveryAccepted {
				t.Fatalf("deliver shared committed enactment: result=%+v err=%v", delivered, err)
			}
		},
		runtimeEvidence: runtimeEvidence,
		roundTripRuntimeEvidence: sharedRuntimeEvidenceRoundTrip(
			now, adapter, runtimeEvidence,
		),
	}
}

func directSharedRuntimeEvidence(
	owner taskorchestration.UserAuthority,
	queries taskorchestration.TaskOrchestrationQuery,
) func(*testing.T, taskorchestration.TransitionDecision) taskorchestration.RuntimeAdapterEvidence {
	return func(t *testing.T, decision taskorchestration.TransitionDecision) taskorchestration.RuntimeAdapterEvidence {
		t.Helper()
		view, err := queries.Query(context.Background(), taskorchestration.TaskQuery{
			TaskID:    decision.TaskProjection.TaskID,
			Authority: taskorchestration.NewUserQueryAuthority(owner),
		})
		if err != nil || len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
			t.Fatalf("query direct Runtime evidence scope: view=%+v err=%v", view, err)
		}
		phase := view.PhaseRuns[0]
		ref := decision.EnactmentRefs[0]
		runtimeFence, ok := ref.Fence.(taskorchestration.RuntimeFence)
		if !ok {
			t.Fatalf("direct Runtime enactment has non-Runtime fence: %+v", ref)
		}
		evidence := taskorchestration.RuntimeAdapterEvidence{
			SchemaVersion: taskorchestration.EvidenceSchemaV1,
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "shared-direct-runtime-evidence"),
				taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "6666666666666666666666666666666666666666666666666666666666666666"),
			),
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, "generation-runtime-authority"),
				Generation:  taskorchestration.AuthorizationGeneration(1),
			},
			TaskID:     decision.TaskProjection.TaskID,
			PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation,
			PhaseRunFence: phase.Fence, RuntimeRunID: phase.RuntimeRuns[0].RuntimeRunID,
			OperationID: ref.OperationID, ActivityGeneration: ref.ActivityGeneration,
			Generation: taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:      runtimeFence, SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		}
		return evidence
	}
}

func sharedRuntimeEvidenceRoundTrip(
	now time.Time,
	mutations taskorchestration.TaskOrchestration,
	runtimeEvidence func(*testing.T, taskorchestration.TransitionDecision) taskorchestration.RuntimeAdapterEvidence,
) func(*testing.T, taskorchestration.TransitionDecision) taskorchestration.TransitionDecision {
	return func(t *testing.T, decision taskorchestration.TransitionDecision) taskorchestration.TransitionDecision {
		t.Helper()
		evidence := runtimeEvidence(t, decision)
		header := intentHeader(
			t, "shared-runtime-evidence-decision",
			decision.TaskProjection.TaskID.String(), now.Add(3*time.Second),
		)
		header.ExpectedTaskRevision = decision.AcceptedTaskRevision
		header.ActivityGeneration = evidence.ActivityGeneration
		intent, err := evidence.Intent(header)
		if err != nil {
			t.Fatalf("translate shared Runtime evidence through public Intent seam: %v", err)
		}
		accepted, err := mutations.Decide(context.Background(), intent)
		if err != nil {
			t.Fatalf("accept direct Runtime evidence through Decide seam: %v", err)
		}
		return accepted
	}
}

type sharedRuntimeEvidencePort struct {
	record taskorchestration.RuntimeEvidenceRecord
}

func (port *sharedRuntimeEvidencePort) EnactRuntime(
	context.Context,
	taskorchestration.EnactmentRef,
) (taskorchestration.RuntimeEvidenceRecord, error) {
	return port.record, nil
}

type sharedPrerequisiteAuthority struct{}

func (sharedPrerequisiteAuthority) ExpectedPrerequisites(
	context.Context,
	taskorchestration.EnactmentRef,
) ([]taskorchestration.EvidencePrerequisite, error) {
	return nil, nil
}
