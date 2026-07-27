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
	mutations     taskorchestration.TaskOrchestration
	queries       taskorchestration.TaskOrchestrationQuery
	afterDecision func(*testing.T, taskorchestration.TransitionDecision)
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
		})
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
		taskorchestration.NewStartPinnedTaskIntent(
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

	view, err := adapter.queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "shared-contract-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query through shared read-only seam: %v", err)
	}
	if view.TaskRevision != 2 || view.LatestDecisionID != work.DecisionID ||
		len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
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
		taskorchestration.NewStartPinnedTaskIntent(
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
	runtimeRun := phase.RuntimeRuns[0]
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "shared-coordination-runtime"),
		taskorchestration.AuthorizationGeneration(1),
	)
	staleEvidence := func(
		request string,
		evidence string,
		activity taskorchestration.ActivityGeneration,
		fence taskorchestration.RuntimeFence,
	) taskorchestration.TransitionIntent {
		header := intentHeader(t, request, "shared-coordination-task", now.Add(3*time.Second))
		header.ExpectedTaskRevision = work.AcceptedTaskRevision
		header.ActivityGeneration = activity
		return taskorchestration.NewAcceptRuntimeEvidenceIntent(
			header,
			runtimeAuthority,
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, evidence),
					taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "1111111111111111111111111111111111111111111111111111111111111111"),
				),
				PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation,
				PhaseRunFence: phase.Fence, RuntimeRunID: runtimeRun.RuntimeRunID,
				OperationID: work.EnactmentRefs[0].OperationID,
				Generation:  taskorchestration.RuntimeGeneration(phase.Generation),
				Fence:       fence, SafetyEpoch: view.SafetyEpoch,
				Outcome: taskorchestration.RuntimeRunSucceeded,
			},
		)
	}
	_, err = adapter.mutations.Decide(context.Background(), staleEvidence(
		"shared-coordination-stale-generation",
		"shared-coordination-stale-generation-evidence",
		view.ActivityGeneration+1,
		taskorchestration.RuntimeFence(phase.Fence),
	))
	requireSharedDecisionError(t, err, taskorchestration.ErrorStaleAuthority)
	_, err = adapter.mutations.Decide(context.Background(), staleEvidence(
		"shared-coordination-stale-fence",
		"shared-coordination-stale-fence-evidence",
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
	validEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "shared-coordination-valid-runtime-evidence"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "5555555555555555555555555555555555555555555555555555555555555555"),
	)
	validBinding := taskorchestration.RuntimeEvidenceBinding{
		Evidence:   validEvidence,
		PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation,
		PhaseRunFence: phase.Fence, RuntimeRunID: runtimeRun.RuntimeRunID,
		OperationID: work.EnactmentRefs[0].OperationID,
		Generation:  taskorchestration.RuntimeGeneration(phase.Generation),
		Fence:       taskorchestration.RuntimeFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
		Outcome: taskorchestration.RuntimeRunSucceeded,
	}
	acceptedEvidence, err := adapter.mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(validHeader, runtimeAuthority, validBinding),
	)
	if err != nil {
		t.Fatalf("accept shared Runtime evidence: %v", err)
	}
	duplicateHeader := validHeader
	duplicateHeader.DecisionRequestID = decisionRequestID(t, "shared-coordination-duplicate-evidence")
	duplicate, err := adapter.mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(duplicateHeader, runtimeAuthority, validBinding),
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
	_ taskorchestration.UserAuthority,
	_ taskorchestration.WorkerAuthority,
) sharedTaskOrchestrationAdapter {
	t.Helper()
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create shared in-memory adapter: %v", err)
	}
	return sharedTaskOrchestrationAdapter{
		mutations: harness.Mutations,
		queries:   harness.Queries,
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
	}
}

func newSharedPostgresAdapter(
	t *testing.T,
	now time.Time,
	_ taskorchestration.UserAuthority,
	_ taskorchestration.WorkerAuthority,
) sharedTaskOrchestrationAdapter {
	t.Helper()
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	return sharedTaskOrchestrationAdapter{
		mutations: adapter,
		queries:   adapter,
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
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
					AuthorityID: authorityID(t, "shared-owned-runtime-producer"),
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
		},
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
