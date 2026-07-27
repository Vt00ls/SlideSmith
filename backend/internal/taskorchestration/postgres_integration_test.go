package taskorchestration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

var postgresSchemaSequence atomic.Uint64

func TestPostgresAdapterCommitsAndRecoversOwnedTaskState(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	var schedulerTable sql.NullString
	if err := db.QueryRowContext(
		context.Background(), "SELECT to_regclass($1)", schema+".scheduler_work_items",
	).Scan(&schedulerTable); err != nil {
		t.Fatalf("inspect Scheduler ownership boundary: %v", err)
	}
	if schedulerTable.Valid {
		t.Fatal("Task Orchestration migration created a Scheduler-owned Work Item table")
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-entry"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})

	start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-start", "postgres-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-work", "postgres-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	workIntent := taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-work-available"),
	)
	work, err := adapter.Decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("make work available: %v", err)
	}
	if len(work.EnactmentRefs) != 1 || len(work.AffectedPhaseRuns) != 1 {
		t.Fatal("work decision did not return its committed Phase Run and enactment")
	}

	view, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: taskID(t, "postgres-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query PostgreSQL Task: %v", err)
	}
	if view.TaskRevision != 2 || len(view.PhaseRuns) != 1 ||
		len(view.PhaseRuns[0].RuntimeRuns) != 1 {
		t.Fatal("PostgreSQL query did not restore the Task aggregate relationships")
	}
	persistence, err := adapter.InspectPersistence(context.Background(), taskorchestration.TaskQuery{
		TaskID: taskID(t, "postgres-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("inspect owned persistence: %v", err)
	}
	if persistence.DecisionCount != 2 || persistence.RevisionCount != 2 ||
		persistence.MandatoryAuditFactCount != 2 || persistence.OutboxCount != 1 ||
		persistence.PhaseRunCount != 1 || persistence.RuntimeRunCount != 1 ||
		schedulerTestWorkItemCount(t, db, schema, taskID(t, "postgres-task")) != 1 {
		t.Fatalf("unexpected committed persistence facts: %+v", persistence)
	}

	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	replayed, err := restarted.Decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if replayed.DecisionID != work.DecisionID ||
		replayed.EnactmentRefs[0].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatal("restart replay allocated a new Decision or Operation identity")
	}
	afterReplay, err := restarted.InspectPersistence(context.Background(), taskorchestration.TaskQuery{
		TaskID: taskID(t, "postgres-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("inspect after replay: %v", err)
	}
	if afterReplay != persistence {
		t.Fatalf("exact replay changed immutable persistence facts: before=%+v after=%+v", persistence, afterReplay)
	}
}

func TestPostgresRejectedEvidenceDiagnosticSurvivesRestart(t *testing.T) {
	now := time.Date(2026, time.July, 27, 14, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-diagnostic-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-diagnostic-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "postgres-diagnostic-runtime"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-diagnostic-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-diagnostic-start", "postgres-diagnostic-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start diagnostic Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-diagnostic-work", "postgres-diagnostic-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-diagnostic-work-available"),
	))
	if err != nil {
		t.Fatalf("make diagnostic work available: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-diagnostic-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query diagnostic Task: %v", err)
	}
	run := view.PhaseRuns[0]
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "postgres-stale-runtime-evidence"), taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "4444444444444444444444444444444444444444444444444444444444444444"),
	)
	staleHeader := intentHeader(t, "postgres-diagnostic-stale", "postgres-diagnostic-task", now.Add(2*time.Second))
	staleHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	_, err = adapter.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		staleHeader, runtimeAuthority, taskorchestration.RuntimeEvidenceBinding{
			Evidence: evidence, PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
			PhaseRunFence: run.Fence, RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID,
			OperationID: work.EnactmentRefs[0].OperationID,
			Generation:  taskorchestration.RuntimeGeneration(run.Generation),
			Fence:       taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	))
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleTaskRevision {
		t.Fatalf("stale evidence = %T, want typed stale revision", err)
	}

	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	restored, err := restarted.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query diagnostic after restart: %v", err)
	}
	if restored.TaskRevision != work.AcceptedTaskRevision ||
		restored.EvidenceDiagnosticCount != 1 ||
		restored.LatestEvidenceDiagnostic.EvidenceID != evidence.ID ||
		restored.LatestEvidenceDiagnostic.Disposition != taskorchestration.EvidenceDispositionNonAuthoritative ||
		restored.LatestEvidenceDiagnostic.Reason != taskorchestration.EvidenceDiagnosticStale {
		t.Fatalf("restart lost rejected evidence diagnostic: %+v", restored)
	}
}

func TestPostgresSnapshotMismatchFailsClosedAsCorruptPersistence(t *testing.T) {
	now := time.Date(2026, time.July, 27, 14, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-corrupt-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	startIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "postgres-corrupt-start", "postgres-corrupt-task", now), owner,
	)
	if _, err := adapter.Decide(context.Background(), startIntent); err != nil {
		t.Fatalf("start corruption-check Task: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		"UPDATE "+schema+".task_orchestration_tasks SET revision=revision+1 WHERE task_id=$1",
		taskID(t, "postgres-corrupt-task").String(),
	); err != nil {
		t.Fatalf("inject relational/snapshot mismatch: %v", err)
	}
	_, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-corrupt-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	var persistenceError *taskorchestration.PersistenceError
	if !errors.As(err, &persistenceError) ||
		persistenceError.Code() != taskorchestration.PersistenceStateCorrupt {
		t.Fatalf("snapshot mismatch = %T, want typed corrupt persistence error", err)
	}
	_, err = adapter.Decide(context.Background(), startIntent)
	if !errors.As(err, &persistenceError) ||
		persistenceError.Code() != taskorchestration.PersistenceStateCorrupt {
		t.Fatalf("exact replay over snapshot mismatch = %T, want typed corrupt persistence error", err)
	}
}

func TestPostgresMissingTaskSnapshotWithJournalFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 27, 14, 45, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-missing-snapshot-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	startIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "postgres-missing-snapshot-start", "postgres-missing-snapshot-task", now), owner,
	)
	if _, err := adapter.Decide(context.Background(), startIntent); err != nil {
		t.Fatalf("start missing-snapshot Task: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		"DELETE FROM "+schema+".task_orchestration_tasks WHERE task_id=$1",
		taskID(t, "postgres-missing-snapshot-task").String(),
	); err != nil {
		t.Fatalf("inject missing Task snapshot: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-missing-snapshot-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	_, queryErr := adapter.Query(context.Background(), query)
	var persistenceError *taskorchestration.PersistenceError
	if !errors.As(queryErr, &persistenceError) ||
		persistenceError.Code() != taskorchestration.PersistenceStateCorrupt {
		t.Fatalf("missing snapshot query = %T, want typed corrupt persistence error", queryErr)
	}
	_, replayErr := adapter.Decide(context.Background(), startIntent)
	if !errors.As(replayErr, &persistenceError) ||
		persistenceError.Code() != taskorchestration.PersistenceStateCorrupt {
		t.Fatalf("missing snapshot replay = %T, want typed corrupt persistence error", replayErr)
	}
}

func TestPostgresOperationIdentityCollisionFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 27, 15, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-operation-collision-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	ownerA := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-operation-owner-a"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-operation-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	startA, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-operation-start-a", "postgres-operation-task-a", now), ownerA, pinned,
	))
	if err != nil {
		t.Fatalf("start first operation Task: %v", err)
	}
	workAHeader := intentHeader(t, "postgres-operation-work-a", "postgres-operation-task-a", now.Add(time.Second))
	workAHeader.ExpectedTaskRevision = startA.AcceptedTaskRevision
	workA, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workAHeader, worker, operationID(t, "postgres-operation-offer-a"),
	))
	if err != nil {
		t.Fatalf("commit first operation: %v", err)
	}

	ownerB := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-operation-owner-b"), taskorchestration.AuthorizationGeneration(1),
	)
	startB, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-operation-start-b", "postgres-operation-task-b", now), ownerB, pinned,
	))
	if err != nil {
		t.Fatalf("start second operation Task: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		"SELECT setval('"+schema+".task_orchestration_operation_blocks', 2, false)",
	); err != nil {
		t.Fatalf("inject operation identity collision: %v", err)
	}
	workBHeader := intentHeader(t, "postgres-operation-work-b", "postgres-operation-task-b", now.Add(2*time.Second))
	workBHeader.ExpectedTaskRevision = startB.AcceptedTaskRevision
	_, err = adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workBHeader, worker, operationID(t, "postgres-operation-offer-b"),
	))
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorIntegrityConflict {
		t.Fatalf("OperationID collision = %T, want typed integrity conflict", err)
	}
	viewB, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-operation-task-b"),
		Authority: taskorchestration.NewUserQueryAuthority(ownerB),
	})
	if err != nil {
		t.Fatalf("query losing collision Task: %v", err)
	}
	if viewB.TaskRevision != startB.AcceptedTaskRevision || viewB.PhaseRunCount != 0 {
		t.Fatalf("OperationID collision committed a partial Decision: %+v", viewB)
	}
	if workA.EnactmentRefs[0].OperationID.String() == "" {
		t.Fatal("first committed OperationID unexpectedly empty")
	}
}

func TestPostgresReconciliationReusesVerifiedOutboxOperation(t *testing.T) {
	now := time.Date(2026, time.July, 27, 15, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-reconcile-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-reconcile-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-reconcile-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-reconcile-start", "postgres-reconcile-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start reconciliation Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-reconcile-work", "postgres-reconcile-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-reconcile-offer"),
	))
	if err != nil {
		t.Fatalf("commit reconciliation source operation: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-reconcile-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	before, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect reconciliation baseline: %v", err)
	}
	beforeScheduler := schedulerTestWorkItemCount(t, db, schema, query.TaskID)
	reconcileHeader := intentHeader(t, "postgres-reconcile-request", "postgres-reconcile-task", now.Add(2*time.Second))
	reconcileHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	reconciled, err := adapter.Decide(context.Background(), taskorchestration.NewReconcileEnactmentIntent(
		reconcileHeader, worker, work.EnactmentRefs[0].OperationID,
		taskorchestration.ReconciliationFence(1),
	))
	if err != nil {
		t.Fatalf("reconcile committed operation: %v", err)
	}
	if len(reconciled.EnactmentRefs) != 1 ||
		reconciled.EnactmentRefs[0].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatal("reconciliation did not return the original OperationID")
	}
	after, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect reconciliation result: %v", err)
	}
	if after.DecisionCount != before.DecisionCount+1 ||
		after.RevisionCount != before.RevisionCount+1 ||
		after.MandatoryAuditFactCount != before.MandatoryAuditFactCount+1 ||
		after.OutboxCount != before.OutboxCount ||
		schedulerTestWorkItemCount(t, db, schema, query.TaskID) != beforeScheduler {
		t.Fatalf("reconciliation created a new Operation or Scheduler Work Item: before=%+v after=%+v", before, after)
	}
}

func TestPostgresMandatoryAuditFailureRollsBackProtectedDecisionAndOutbox(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	faults := &taskorchestration.PersistenceFaultController{}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, Faults: faults,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-audit-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-audit-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-audit-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-audit-start", "postgres-audit-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start protected Task: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID: taskID(t, "postgres-audit-task"), Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	baseline, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect baseline: %v", err)
	}
	if err := faults.FailNextAt(taskorchestration.PersistenceFaultBeforeMandatoryAudit); err != nil {
		t.Fatalf("inject mandatory audit failure: %v", err)
	}
	workHeader := intentHeader(t, "postgres-audit-work", "postgres-audit-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	workIntent := taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-audit-work-available"),
	)
	_, err = adapter.Decide(context.Background(), workIntent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorDependencyUnavailable {
		t.Fatalf("mandatory audit failure = %T, want safe dependency error", err)
	}
	afterFailure, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect after mandatory audit failure: %v", err)
	}
	if afterFailure != baseline {
		t.Fatalf("mandatory audit failure left protected facts: before=%+v after=%+v", baseline, afterFailure)
	}
	view, err := adapter.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query after mandatory audit failure: %v", err)
	}
	if view.TaskRevision != start.AcceptedTaskRevision || len(view.PhaseRuns) != 0 {
		t.Fatal("mandatory audit failure changed the Task aggregate")
	}

	committed, err := adapter.Decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("retry rolled-back request: %v", err)
	}
	if committed.AcceptedTaskRevision != start.AcceptedTaskRevision+1 || len(committed.EnactmentRefs) != 1 {
		t.Fatal("rolled-back request did not commit normally after audit recovery")
	}
}

func TestPostgresSchedulerParticipantFailureRollsBackTaskAndWorkItem(t *testing.T) {
	now := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	good := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-scheduler-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-scheduler-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-scheduler-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := good.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-scheduler-start", "postgres-scheduler-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start Scheduler participant Task: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-scheduler-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	baseline, err := good.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect Scheduler baseline: %v", err)
	}
	baselineScheduler := schedulerTestWorkItemCount(t, db, schema, query.TaskID)
	failing := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) },
		SchedulerParticipant: taskorchestration.SchedulerTransactionalParticipantFunc(func(
			ctx context.Context,
			transaction taskorchestration.SchedulerTransaction,
			_ taskorchestration.SchedulerEnqueueFact,
		) error {
			if err := transaction.Enqueue(ctx); err != nil {
				return err
			}
			return errors.New("postgres://credential-canary/internal-scheduler-table")
		}),
	})
	workHeader := intentHeader(t, "postgres-scheduler-work", "postgres-scheduler-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	workIntent := taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-scheduler-work-available"),
	)
	_, err = failing.Decide(context.Background(), workIntent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorQueueRejected {
		t.Fatalf("Scheduler participant failure = %T, want safe queue rejection", err)
	}
	if strings.Contains(err.Error(), "credential-canary") || strings.Contains(err.Error(), "scheduler-table") {
		t.Fatal("Scheduler participant error leaked adapter-private detail")
	}
	afterFailure, err := good.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect after Scheduler failure: %v", err)
	}
	if afterFailure != baseline {
		t.Fatalf("Scheduler failure left partial Task or Work Item facts: before=%+v after=%+v", baseline, afterFailure)
	}
	if schedulerTestWorkItemCount(t, db, schema, query.TaskID) != baselineScheduler {
		t.Fatal("Scheduler participant failure committed its independently owned Work Item")
	}
	if _, err := good.Decide(context.Background(), workIntent); err != nil {
		t.Fatalf("commit request after Scheduler participant recovery: %v", err)
	}
}

func TestPostgresProjectionObserverFailureDoesNotRollBackCommittedDecision(t *testing.T) {
	now := time.Date(2026, time.July, 27, 10, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	var observations atomic.Uint64
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: taskorchestration.ExternalAuditProjectionSinkFunc(func(
				context.Context,
				taskorchestration.ExternalAuditProjection,
			) error {
				observations.Add(1)
				return errors.New("external-audit-credential-canary")
			}),
			Telemetry: taskorchestration.DecisionTelemetryProjectionSinkFunc(func(
				context.Context,
				taskorchestration.DecisionTelemetryProjection,
			) error {
				return nil
			}),
		},
	)
	if err != nil {
		t.Fatalf("create projection adapter: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now:                func() time.Time { return now },
		ProjectionDelivery: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-observer-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "postgres-observer-start", "postgres-observer-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit with failing projection observer: %v", err)
	}
	if decision.AcceptedTaskRevision != 1 || observations.Load() != 0 {
		t.Fatal("projection delivery ran in the protected Decision path")
	}
	if _, err := adapter.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.ProjectionDeliveryRebuildRequest{Limit: 1},
	); err != nil || observations.Load() != 1 {
		t.Fatalf("asynchronous projection delivery attempt: observations=%d err=%v", observations.Load(), err)
	}
	persistence, err := adapter.InspectPersistence(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-observer-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("inspect observer Task: %v", err)
	}
	if persistence.DecisionCount != 1 || persistence.RevisionCount != 1 ||
		persistence.MandatoryAuditFactCount != 1 {
		t.Fatalf("projection failure rolled back committed authority: %+v", persistence)
	}
}

func TestPostgresCommitAndResponseCrashBoundariesRecoverByExactReplay(t *testing.T) {
	for _, test := range []struct {
		name       string
		faultPoint taskorchestration.PersistenceFaultPoint
		committed  bool
	}{
		{name: "before_commit", faultPoint: taskorchestration.PersistenceFaultBeforeCommit},
		{name: "after_commit", faultPoint: taskorchestration.PersistenceFaultAfterCommit, committed: true},
		{name: "before_response", faultPoint: taskorchestration.PersistenceFaultBeforeResponse, committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 27, 11, 0, 0, 0, time.UTC)
			db, schema := isolatedPostgresSchema(t)
			faults := &taskorchestration.PersistenceFaultController{}
			adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
				Now: func() time.Time { return now }, Faults: faults,
			})
			owner := taskorchestration.NewUserAuthority(
				authorityID(t, "postgres-crash-owner"), taskorchestration.AuthorizationGeneration(1),
			)
			worker := taskorchestration.NewWorkerAuthority(
				authorityID(t, "postgres-crash-worker"), taskorchestration.AuthorizationGeneration(1),
			)
			pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
				Key: phaseKey(t, "postgres-crash-phase"), Kind: taskorchestration.PhaseNonMutating,
				ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
				RequiredRuntimeRuns: 1,
			}})
			start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
				intentHeader(t, "postgres-crash-start", "postgres-crash-task", now), owner, pinned,
			))
			if err != nil {
				t.Fatalf("start crash-boundary Task: %v", err)
			}
			query := taskorchestration.TaskQuery{
				TaskID:    taskID(t, "postgres-crash-task"),
				Authority: taskorchestration.NewUserQueryAuthority(owner),
			}
			baseline, err := adapter.InspectPersistence(context.Background(), query)
			if err != nil {
				t.Fatalf("inspect crash baseline: %v", err)
			}
			baselineScheduler := schedulerTestWorkItemCount(t, db, schema, query.TaskID)
			workHeader := intentHeader(t, "postgres-crash-work", "postgres-crash-task", now.Add(time.Second))
			workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
			workIntent := taskorchestration.NewMakeWorkAvailableIntent(
				workHeader, worker, operationID(t, "postgres-crash-work-available"),
			)
			if err := faults.FailNextAt(test.faultPoint); err != nil {
				t.Fatalf("inject %s fault: %v", test.name, err)
			}
			_, err = adapter.Decide(context.Background(), workIntent)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) {
				t.Fatalf("%s error = %T, want typed decision error", test.name, err)
			}
			wantCode := taskorchestration.ErrorDependencyUnavailable
			if test.committed {
				wantCode = taskorchestration.ErrorReconciliationRequired
			}
			if decisionError.Code() != wantCode {
				t.Fatalf("%s error code = %v, want %v", test.name, decisionError.Code(), wantCode)
			}

			restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
				Now: func() time.Time { return now.Add(time.Minute) },
			})
			beforeReplay, err := restarted.InspectPersistence(context.Background(), query)
			if err != nil {
				t.Fatalf("inspect %s before replay: %v", test.name, err)
			}
			if test.committed {
				if beforeReplay.DecisionCount != baseline.DecisionCount+1 ||
					beforeReplay.OutboxCount != baseline.OutboxCount+1 ||
					schedulerTestWorkItemCount(t, db, schema, query.TaskID) != baselineScheduler+1 {
					t.Fatalf("%s did not retain the all-or-nothing committed transaction: %+v", test.name, beforeReplay)
				}
			} else if beforeReplay != baseline {
				t.Fatalf("crash before commit retained transaction facts: before=%+v after=%+v", baseline, beforeReplay)
			}
			replayed, err := restarted.Decide(context.Background(), workIntent)
			if err != nil {
				t.Fatalf("replay %s request after restart: %v", test.name, err)
			}
			again, err := restarted.Decide(context.Background(), workIntent)
			if err != nil {
				t.Fatalf("repeat %s exact replay: %v", test.name, err)
			}
			if replayed.DecisionID != again.DecisionID ||
				replayed.EnactmentRefs[0].OperationID != again.EnactmentRefs[0].OperationID {
				t.Fatalf("%s replay allocated new durable identities", test.name)
			}
			afterReplay, err := restarted.InspectPersistence(context.Background(), query)
			if err != nil {
				t.Fatalf("inspect %s after replay: %v", test.name, err)
			}
			if afterReplay.DecisionCount != baseline.DecisionCount+1 ||
				afterReplay.OutboxCount != baseline.OutboxCount+1 ||
				schedulerTestWorkItemCount(t, db, schema, query.TaskID) != baselineScheduler+1 {
				t.Fatalf("%s exact replay duplicated durable facts: %+v", test.name, afterReplay)
			}
		})
	}
}

func TestPostgresConcurrentWritersProduceOneExpectedRevisionWinner(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	firstAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	secondAdapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Millisecond) },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-concurrent-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-concurrent-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-concurrent-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := firstAdapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-concurrent-start", "postgres-concurrent-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start concurrent-writer Task: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-concurrent-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	baseline, err := firstAdapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect concurrent baseline: %v", err)
	}
	baselineScheduler := schedulerTestWorkItemCount(t, db, schema, query.TaskID)
	makeIntent := func(request, availability string, occurredAt time.Time) taskorchestration.TransitionIntent {
		header := intentHeader(t, request, "postgres-concurrent-task", occurredAt)
		header.ExpectedTaskRevision = start.AcceptedTaskRevision
		return taskorchestration.NewMakeWorkAvailableIntent(
			header, worker, operationID(t, availability),
		)
	}
	intents := []taskorchestration.TransitionIntent{
		makeIntent("postgres-concurrent-work-a", "postgres-concurrent-available-a", now.Add(time.Second)),
		makeIntent("postgres-concurrent-work-b", "postgres-concurrent-available-b", now.Add(2*time.Second)),
	}
	adapters := []*taskorchestration.PostgresAdapter{firstAdapter, secondAdapter}
	startWriting := make(chan struct{})
	results := make(chan error, len(adapters))
	for index, adapter := range adapters {
		go func(adapter *taskorchestration.PostgresAdapter, intent taskorchestration.TransitionIntent) {
			<-startWriting
			_, err := adapter.Decide(context.Background(), intent)
			results <- err
		}(adapter, intents[index])
	}
	close(startWriting)
	var successes, staleRevisions int
	for range adapters {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var decisionError *taskorchestration.Error
		if errors.As(err, &decisionError) && decisionError.Code() == taskorchestration.ErrorStaleTaskRevision {
			staleRevisions++
			continue
		}
		t.Fatalf("concurrent writer returned unexpected error: %v", err)
	}
	if successes != 1 || staleRevisions != 1 {
		t.Fatalf("concurrent results: successes=%d stale=%d, want one each", successes, staleRevisions)
	}
	after, err := firstAdapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect concurrent result: %v", err)
	}
	if after.DecisionCount != baseline.DecisionCount+1 ||
		after.RevisionCount != baseline.RevisionCount+1 ||
		after.MandatoryAuditFactCount != baseline.MandatoryAuditFactCount+1 ||
		after.OutboxCount != baseline.OutboxCount+1 ||
		schedulerTestWorkItemCount(t, db, schema, query.TaskID) != baselineScheduler+1 {
		t.Fatalf("losing concurrent writer left authoritative facts: before=%+v after=%+v", baseline, after)
	}
}

func TestPostgresWritersForDifferentTasksDoNotContendOnRecoveryState(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 15, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapters := []*taskorchestration.PostgresAdapter{
		newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{Now: func() time.Time { return now }}),
		newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{Now: func() time.Time { return now }}),
	}
	owners := []taskorchestration.UserAuthority{
		taskorchestration.NewUserAuthority(
			authorityID(t, "postgres-independent-owner-a"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.NewUserAuthority(
			authorityID(t, "postgres-independent-owner-b"), taskorchestration.AuthorizationGeneration(1),
		),
	}
	intents := []taskorchestration.TransitionIntent{
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "postgres-independent-start-a", "postgres-independent-task-a", now), owners[0],
		),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "postgres-independent-start-b", "postgres-independent-task-b", now), owners[1],
		),
	}
	startWriting := make(chan struct{})
	results := make(chan error, len(adapters))
	for index, adapter := range adapters {
		go func(adapter *taskorchestration.PostgresAdapter, intent taskorchestration.TransitionIntent) {
			<-startWriting
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := adapter.Decide(ctx, intent)
			results <- err
		}(adapter, intents[index])
	}
	close(startWriting)
	for range adapters {
		if err := <-results; err != nil {
			t.Fatalf("independent Task writer failed: %v", err)
		}
	}
	for index, owner := range owners {
		view, err := adapters[index].Query(context.Background(), taskorchestration.TaskQuery{
			TaskID:    taskID(t, fmt.Sprintf("postgres-independent-task-%c", 'a'+rune(index))),
			Authority: taskorchestration.NewUserQueryAuthority(owner),
		})
		if err != nil || view.TaskRevision != 1 {
			t.Fatalf("independent Task %d was not committed: view=%+v err=%v", index, view, err)
		}
	}
}

func TestPostgresPersistsEvidenceAndReplaysAfterRevisionAdvances(t *testing.T) {
	now := time.Date(2026, time.July, 27, 12, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-evidence-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-evidence-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "postgres-evidence-runtime"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-evidence-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-evidence-start", "postgres-evidence-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start evidence Task: %v", err)
	}
	workHeader := intentHeader(t, "postgres-evidence-work", "postgres-evidence-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	workIntent := taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-evidence-work-available"),
	)
	work, err := adapter.Decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("make evidence work available: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-evidence-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query evidence Phase: %v", err)
	}
	run := view.PhaseRuns[0]
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "postgres-runtime-evidence"), taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	)
	evidenceHeader := intentHeader(t, "postgres-evidence-runtime-result", "postgres-evidence-task", now.Add(2*time.Second))
	evidenceHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	evidenceIntent := taskorchestration.NewAcceptRuntimeEvidenceIntent(
		evidenceHeader, runtimeAuthority, taskorchestration.RuntimeEvidenceBinding{
			Evidence: evidence, PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
			PhaseRunFence: run.Fence, RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID,
			OperationID: work.EnactmentRefs[0].OperationID,
			Generation:  taskorchestration.RuntimeGeneration(run.Generation),
			Fence:       taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)
	accepted, err := adapter.Decide(context.Background(), evidenceIntent)
	if err != nil {
		t.Fatalf("accept Runtime evidence: %v", err)
	}
	if len(accepted.AcceptedEvidenceRefs) != 1 ||
		accepted.AcceptedEvidenceRefs[0].ID != evidence.ID {
		t.Fatal("evidence decision did not report its committed evidence reference")
	}
	committedFacts, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect evidence persistence: %v", err)
	}
	if committedFacts.EvidenceRefCount != 1 || committedFacts.RuntimeRunCount != 1 {
		t.Fatalf("evidence/runtime relationships were not persisted: %+v", committedFacts)
	}

	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	restored, err := restarted.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query evidence after restart: %v", err)
	}
	if restored.TaskRevision != accepted.AcceptedTaskRevision ||
		restored.PhaseRuns[0].Outcome != taskorchestration.PhaseRunRunning ||
		restored.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunSucceeded {
		t.Fatal("restart lost evidence state or let Runtime success complete the Phase")
	}
	replayedWork, err := restarted.Decide(context.Background(), workIntent)
	if err != nil {
		t.Fatalf("replay old-revision work request: %v", err)
	}
	if replayedWork.DecisionID != work.DecisionID {
		t.Fatal("old-revision exact replay did not return the original Decision")
	}
	duplicateHeader := evidenceHeader
	duplicateHeader.DecisionRequestID = decisionRequestID(t, "postgres-evidence-runtime-duplicate")
	duplicateEvidenceIntent := taskorchestration.NewAcceptRuntimeEvidenceIntent(
		duplicateHeader, runtimeAuthority, taskorchestration.RuntimeEvidenceBinding{
			Evidence: evidence, PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
			PhaseRunFence: run.Fence, RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID,
			OperationID: work.EnactmentRefs[0].OperationID,
			Generation:  taskorchestration.RuntimeGeneration(run.Generation),
			Fence:       taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)
	duplicate, err := restarted.Decide(context.Background(), duplicateEvidenceIntent)
	if err != nil {
		t.Fatalf("replay exact evidence under a new request identity: %v", err)
	}
	if duplicate.DecisionID != accepted.DecisionID {
		t.Fatal("exact evidence replay allocated a new Decision identity")
	}
	afterReplay, err := restarted.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect after evidence replay: %v", err)
	}
	if afterReplay != committedFacts {
		t.Fatalf("command/evidence replay changed durable facts: before=%+v after=%+v", committedFacts, afterReplay)
	}
}

func TestPostgresDecisionRequestCannotBeReboundToDifferentPayload(t *testing.T) {
	now := time.Date(2026, time.July, 27, 13, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-integrity-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	originalHeader := intentHeader(t, "postgres-integrity-request", "postgres-integrity-task", now)
	if _, err := adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		originalHeader, owner,
	)); err != nil {
		t.Fatalf("commit original idempotent request: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-integrity-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	baseline, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect integrity baseline: %v", err)
	}
	changedHeader := originalHeader
	changedHeader.OccurredAt = now.Add(time.Second)
	_, err = adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		changedHeader, owner,
	))
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorIntegrityConflict {
		t.Fatalf("same-key/different-payload = %T, want typed integrity conflict", err)
	}
	after, err := adapter.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect integrity conflict: %v", err)
	}
	if after != baseline {
		t.Fatalf("integrity conflict changed durable facts: before=%+v after=%+v", baseline, after)
	}
}

func TestPostgresPersistsOpaqueLifecycleRevisionAndCheckpointEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 27, 13, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-lifecycle-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-lifecycle-phase"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "postgres-lifecycle-start", "postgres-lifecycle-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start lifecycle Task: %v", err)
	}
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-lifecycle-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	workHeader := intentHeader(t, "postgres-lifecycle-work", "postgres-lifecycle-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "postgres-lifecycle-work-available"),
	))
	if err != nil {
		t.Fatalf("make lifecycle work available: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "postgres-lifecycle-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	view, err := adapter.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query lifecycle run: %v", err)
	}
	run := view.PhaseRuns[0]
	runtimeHeader := intentHeader(t, "postgres-lifecycle-runtime", "postgres-lifecycle-task", now.Add(2*time.Second))
	runtimeHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	runtimeDecision, err := adapter.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		runtimeHeader,
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "postgres-lifecycle-runtime-authority"),
			taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "postgres-lifecycle-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "1111111111111111111111111111111111111111111111111111111111111111"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.RuntimeGeneration(run.Generation),
			Fence:      taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	))
	if err != nil {
		t.Fatalf("accept lifecycle Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "postgres-lifecycle-validation", "postgres-lifecycle-task", now.Add(3*time.Second))
	validationHeader.ExpectedTaskRevision = runtimeDecision.AcceptedTaskRevision
	validationDecision, err := adapter.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
		validationHeader,
		taskorchestration.NewValidatorAuthority(
			authorityID(t, "postgres-lifecycle-validator"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.ValidationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "postgres-lifecycle-validation-evidence"),
				taskorchestration.EvidencePhaseValidation,
				evidenceDigest(t, "2222222222222222222222222222222222222222222222222222222222222222"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			Generation: taskorchestration.ProducerGeneration(run.Generation),
			Fence:      taskorchestration.ValidationFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.PhaseValidationAccepted,
		},
	))
	if err != nil {
		t.Fatalf("accept lifecycle validation evidence: %v", err)
	}
	if len(validationDecision.EnactmentRefs) != 1 ||
		validationDecision.EnactmentRefs[0].Kind != taskorchestration.EnactmentTaskWorkspaceLifecycle {
		t.Fatal("validation did not commit the opaque Task Workspace Lifecycle enactment")
	}
	lifecycleHeader := intentHeader(t, "postgres-lifecycle-commit", "postgres-lifecycle-task", now.Add(4*time.Second))
	lifecycleHeader.ExpectedTaskRevision = validationDecision.AcceptedTaskRevision
	revisionID := taskWorkspaceRevisionID(t, "postgres-lifecycle-revision")
	checkpoint := checkpointID(t, "postgres-lifecycle-checkpoint")
	committed, err := adapter.Decide(context.Background(), taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
		lifecycleHeader,
		taskorchestration.NewTaskWorkspaceLifecycleAuthority(
			authorityID(t, "postgres-lifecycle-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "postgres-lifecycle-commit-evidence"),
				taskorchestration.EvidenceTaskWorkspaceLifecycle,
				evidenceDigest(t, "3333333333333333333333333333333333333333333333333333333333333333"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			OperationID: validationDecision.EnactmentRefs[0].OperationID,
			Generation:  taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
			Fence:       taskorchestration.TaskWorkspaceLifecycleFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome:    taskorchestration.TaskWorkspaceLifecycleCommitted,
			RevisionID: revisionID, CheckpointID: checkpoint,
		},
	))
	if err != nil {
		t.Fatalf("accept opaque lifecycle evidence: %v", err)
	}
	if committed.TaskProjection.LatestRevisionID != revisionID ||
		committed.TaskProjection.LatestCheckpointID != checkpoint {
		t.Fatal("lifecycle decision did not project its exact Revision/Checkpoint evidence")
	}

	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	restored, err := restarted.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query lifecycle Task after restart: %v", err)
	}
	if restored.LatestRevisionID != revisionID || restored.LatestCheckpointID != checkpoint ||
		restored.PhaseRuns[0].RevisionID != revisionID ||
		restored.PhaseRuns[0].CheckpointID != checkpoint ||
		restored.PhaseRuns[0].Outcome != taskorchestration.PhaseRunSucceeded {
		t.Fatal("restart lost opaque lifecycle evidence or Phase outcome")
	}
	facts, err := restarted.InspectPersistence(context.Background(), query)
	if err != nil {
		t.Fatalf("inspect lifecycle persistence: %v", err)
	}
	if facts.EvidenceRefCount != 3 || facts.PhaseRunCount != 1 || facts.RuntimeRunCount != 1 {
		t.Fatalf("lifecycle evidence relationships were not durably retained: %+v", facts)
	}
}

func isolatedPostgresSchema(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("SLIDESMITH_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("SLIDESMITH_TEST_POSTGRES_DSN is required for real PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf(
		"taskorchestration_test_%d_%d",
		time.Now().UnixNano(), postgresSchemaSequence.Add(1),
	)
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupContext, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
	})
	return db, schema
}

func newPostgresAdapter(
	t *testing.T,
	db *sql.DB,
	schema string,
	config taskorchestration.PostgresConfig,
) *taskorchestration.PostgresAdapter {
	t.Helper()
	ensureSchedulerTestTable(t, db, schema)
	if config.SchedulerParticipant == nil {
		config.SchedulerParticipant = schedulerTestParticipant(schema)
	}
	if config.SchedulerEnqueueFunction == "" {
		config.SchedulerEnqueueFunction = schema + ".scheduler_test_offer_work"
	}
	config.Schema = schema
	adapter, err := taskorchestration.NewPostgresAdapter(db, config)
	if err != nil {
		t.Fatalf("create PostgreSQL adapter: %v", err)
	}
	if err := adapter.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate PostgreSQL adapter: %v", err)
	}
	return adapter
}

func ensureSchedulerTestTable(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS `+schema+`.scheduler_test_owned_work_items (
		work_item_id text PRIMARY KEY,
		operation_id text NOT NULL UNIQUE,
		task_id text NOT NULL,
		phase_run_id text NOT NULL,
		runtime_run_id text NOT NULL,
		decision_id text NOT NULL,
		task_revision bigint NOT NULL,
		kind smallint NOT NULL,
		payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
		activity_generation bigint NOT NULL,
		fence_kind smallint NOT NULL,
		fence bigint NOT NULL,
		causation_id text NOT NULL,
		priority_class text NOT NULL CHECK (priority_class = 'standard'),
		state text NOT NULL CHECK (state = 'offered'),
		enqueued_at timestamptz NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create Scheduler-owned contract table: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `CREATE OR REPLACE FUNCTION `+schema+`.scheduler_test_offer_work(
		p_operation_id text,
		p_task_id text,
		p_phase_run_id text,
		p_runtime_run_id text,
		p_decision_id text,
		p_task_revision bigint,
		p_kind smallint,
		p_payload_digest bytea,
		p_activity_generation bigint,
		p_fence_kind smallint,
		p_fence bigint,
		p_causation_id text
	) RETURNS void LANGUAGE SQL AS $scheduler_function$
		INSERT INTO `+schema+`.scheduler_test_owned_work_items (
			work_item_id, operation_id, task_id, phase_run_id, runtime_run_id, decision_id,
			task_revision, kind, payload_digest, activity_generation, fence_kind, fence,
			causation_id, priority_class, state, enqueued_at
		) VALUES (
			'scheduler-test-' || p_operation_id, p_operation_id, p_task_id, p_phase_run_id,
			p_runtime_run_id, p_decision_id, p_task_revision, p_kind, p_payload_digest,
			p_activity_generation, p_fence_kind, p_fence, p_causation_id,
			'standard', 'offered', CURRENT_TIMESTAMP
		)
	$scheduler_function$`)
	if err != nil {
		t.Fatalf("create Scheduler-owned enqueue function: %v", err)
	}
}

func schedulerTestParticipant(_ string) taskorchestration.SchedulerTransactionalParticipant {
	return taskorchestration.SchedulerTransactionalParticipantFunc(func(
		ctx context.Context,
		transaction taskorchestration.SchedulerTransaction,
		_ taskorchestration.SchedulerEnqueueFact,
	) error {
		return transaction.Enqueue(ctx)
	})
}

func schedulerTestWorkItemCount(
	t *testing.T,
	db *sql.DB,
	schema string,
	taskID taskorchestration.TaskID,
) uint64 {
	t.Helper()
	var count uint64
	if err := db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+schema+".scheduler_test_owned_work_items WHERE task_id=$1",
		taskID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count Scheduler-owned Work Items: %v", err)
	}
	return count
}
