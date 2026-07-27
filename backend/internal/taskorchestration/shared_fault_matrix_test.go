package taskorchestration_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestSharedTaskOrchestrationFaultMatrix(t *testing.T) {
	runSharedTaskOrchestrationFaultMatrix(t)
}

type sharedDecisionFaultAdapter struct {
	decide           func(context.Context, taskorchestration.TransitionIntent) (taskorchestration.TransitionDecision, error)
	query            func(context.Context, taskorchestration.TaskQuery) (taskorchestration.TaskOrchestrationView, error)
	failBeforeCommit func(*testing.T)
	loseResponse     func(*testing.T)
	restart          func(*testing.T)
	afterDecision    func(*testing.T, taskorchestration.TransitionDecision)
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
			runDecisionCrashAndResponseLossContract(t, factory)
		})
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

	lostIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "shared-response-loss", "shared-response-loss-task", now.Add(2*time.Second)),
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
		failBeforeCommit: func(t *testing.T) {
			t.Helper()
			if err := current.CrashNextAt(taskorchestration.CrashBeforeCommit); err != nil {
				t.Fatalf("inject in-memory pre-commit crash: %v", err)
			}
		},
		loseResponse: func(*testing.T) { current.LoseNextResponse() },
		restart:      func(*testing.T) { current = current.Restart() },
		afterDecision: func(*testing.T, taskorchestration.TransitionDecision) {
		},
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
		failBeforeCommit: func(t *testing.T) {
			t.Helper()
			if err := faults.FailNextAt(taskorchestration.PersistenceFaultBeforeCommit); err != nil {
				t.Fatalf("inject PostgreSQL pre-commit crash: %v", err)
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
