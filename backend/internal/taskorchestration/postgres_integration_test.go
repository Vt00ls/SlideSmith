package taskorchestration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
	"github.com/slidesmith/slidesmith/backend/internal/scheduler"
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

	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	startIntent := minimalPinnedStartIntent(t,
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
	startIntent := minimalPinnedStartIntent(t,
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
	startA, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	startB, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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

func TestPostgresMandatoryAuditFactPersistsTheCompleteAuthoritativeEnvelope(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 45, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
	})
	owner := taskorchestration.NewUserAuthority(authorityID(t, "postgres-audit-envelope-owner"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-audit-envelope-publication"), Kind: taskorchestration.PhasePublication,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-audit-envelope-start", "postgres-audit-envelope-task", now),
		owner, pinned,
	))
	if err != nil {
		t.Fatalf("start audited Task: %v", err)
	}
	administrator := taskorchestration.NewAdministratorAuthority(
		authorityID(t, "postgres-audit-envelope-administrator"), 2,
		taskorchestration.AdministratorReasonSafety,
	)
	cancelHeader := intentHeader(
		t, "postgres-audit-envelope-cancel", "postgres-audit-envelope-task", now.Add(time.Second),
	)
	cancelHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	cancelled, err := adapter.Decide(context.Background(), taskorchestration.NewCancelTaskByAdministratorIntent(
		cancelHeader, administrator, taskorchestration.CancelReasonSafety,
	))
	if err != nil {
		t.Fatalf("commit reason-bound administrative cancel: %v", err)
	}
	fact := cancelled.MandatoryAuditFactRef
	if fact.SchemaVersion != taskorchestration.AuditSchemaV1 ||
		fact.IntegrityVersion != taskorchestration.AuditIntegrityV1 ||
		fact.CanonicalDigest == (taskorchestration.AuditFactDigest{}) ||
		fact.OwningModule != taskorchestration.AuditModuleTaskOrchestration ||
		fact.DecisionID != cancelled.DecisionID || fact.TaskID != cancelled.TaskProjection.TaskID ||
		fact.Action != taskorchestration.IntentCancelTask || fact.Result != taskorchestration.AuditAccepted ||
		fact.AuthorityKind != taskorchestration.AuthorityAdministrator ||
		fact.AuthorityID != authorityID(t, "postgres-audit-envelope-administrator") ||
		fact.AuthorizationGeneration != 2 ||
		fact.AuthorityReason != taskorchestration.AdministratorReasonSafety ||
		fact.ReasonCode != taskorchestration.AuditReasonAdministratorSafety ||
		fact.BeforeTaskRevision != 1 || fact.AfterTaskRevision != 2 ||
		fact.BeforeStatus != taskorchestration.TaskReady || fact.AfterStatus != taskorchestration.TaskCancelled ||
		fact.BeforeActivityGeneration != 1 || fact.AfterActivityGeneration != 2 ||
		fact.RecoveryGeneration != 0 || fact.BeforeSafetyEpoch != 1 || fact.AfterSafetyEpoch != 1 ||
		fact.IdempotencyDecisionRequestID != cancelHeader.DecisionRequestID ||
		!fact.OccurredAt.Equal(cancelHeader.OccurredAt) || !fact.RecordedAt.Equal(now) ||
		fact.SourceClock != taskorchestration.AuditSourceTaskOrchestrationClock ||
		len(fact.EvidenceRefs) != 0 || len(fact.EnactmentRefs) != 0 ||
		fact.CanonicalDigest != taskorchestration.MandatoryAuditFactDigest(fact) {
		t.Fatalf("mandatory audit envelope is incomplete: %+v", fact)
	}

	var persistedDigest, persistedState []byte
	err = db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT canonical_digest, audit_state
		FROM %s.task_orchestration_mandatory_audit_facts WHERE audit_fact_id=$1`, schema),
		fact.AuditFactID.String(),
	).Scan(&persistedDigest, &persistedState)
	if err != nil {
		t.Fatalf("read authoritative audit row: %v", err)
	}
	var stored map[string]any
	if json.Unmarshal(persistedState, &stored) != nil ||
		!bytes.Equal(persistedDigest, fact.CanonicalDigest[:]) ||
		stored["AuthorityReason"] != float64(taskorchestration.AdministratorReasonSafety) ||
		stored["BeforeTaskRevision"] != float64(1) || stored["AfterTaskRevision"] != float64(2) {
		t.Fatalf("authoritative PostgreSQL audit row lost required bindings: %s", persistedState)
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
	start, err := good.Decide(context.Background(), verifiedPinnedStartIntent(t,
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

func TestPostgresRuntimeWorkItemBindsCompleteCanonicalC03RequestInTaskTransaction(t *testing.T) {
	now := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	var captured taskorchestration.SchedulerEnqueueFact
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now },
		SchedulerParticipant: taskorchestration.SchedulerTransactionalParticipantFunc(func(
			ctx context.Context,
			transaction taskorchestration.SchedulerTransaction,
			fact taskorchestration.SchedulerEnqueueFact,
		) error {
			captured = fact
			return transaction.Enqueue(ctx)
		}),
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-c03-binding-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-c03-binding-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-c03-binding-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	started, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-c03-binding-start", "postgres-c03-binding-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start Task: %v", err)
	}
	header := intentHeader(t, "postgres-c03-binding-work", "postgres-c03-binding-task", now.Add(time.Second))
	header.ExpectedTaskRevision = started.AcceptedTaskRevision
	decision, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		header, worker, operationID(t, "postgres-c03-binding-availability"),
	))
	if err != nil {
		t.Fatalf("make Runtime work available: %v", err)
	}
	if captured.Kind != taskorchestration.EnactmentRuntimeExecution ||
		captured.OperationID != decision.EnactmentRefs[0].OperationID || len(captured.CanonicalPayload) == 0 {
		t.Fatalf("restricted Scheduler fact lacks exact C03 request: %+v", captured)
	}
	var request struct {
		Kind         string `json:"kind"`
		OperationID  string `json:"operation_id"`
		TaskID       string `json:"task_id"`
		PhaseRunID   string `json:"phase_run_id"`
		RuntimeRunID string `json:"runtime_run_id"`
	}
	if err := json.Unmarshal(captured.CanonicalPayload, &request); err != nil {
		t.Fatalf("decode canonical C03 request: %v", err)
	}
	if request.Kind != "start_runtime_run" || request.OperationID != captured.OperationID.String() ||
		request.TaskID != captured.TaskID.String() || request.PhaseRunID != captured.PhaseRunID.String() ||
		request.RuntimeRunID != captured.RuntimeRunID.String() {
		t.Fatalf("canonical C03 binding = %+v, Scheduler fact = %+v", request, captured)
	}
	wantDigest := sha256.Sum256(append(
		[]byte("slidesmith.runtime-execution.request/v1\n"), captured.CanonicalPayload...,
	))
	if !bytes.Equal(captured.PayloadDigest[:], wantDigest[:]) {
		t.Fatalf("Work Item digest %x != canonical C03 digest %x", captured.PayloadDigest, wantDigest)
	}
	var persisted []byte
	if err := db.QueryRowContext(context.Background(), `SELECT canonical_payload FROM `+schema+
		`.task_orchestration_outbox WHERE operation_id=$1`, captured.OperationID.String()).Scan(&persisted); err != nil {
		t.Fatalf("read Task-owned canonical payload: %v", err)
	}
	if !bytes.Equal(persisted, captured.CanonicalPayload) {
		t.Fatal("Task outbox and Scheduler participant observed different C03 payloads")
	}
}

func TestPostgresSchedulerClaimAndAdmitIsTheUniqueGenerationAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 29, 9, 30, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
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
		Schema: schema,
		Now:    func() time.Time { return now },
		Admission: scheduler.LocalAdmissionConfig{
			SchedulerEpoch: 1,
			PolicyVersion:  1,
			GrantTTL:       time.Minute,
			Node: scheduler.ExecutionNodeConfig{
				ExecutionNodeID:       nodeID,
				CapacityGeneration:    1,
				ResourceClassID:       resourceClassID,
				ExecutionPolicyID:     executionPolicyID,
				AvailableRuntimeSlots: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("create Scheduler authority: %v", err)
	}
	if err := scheduling.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate Scheduler authority: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now:                      func() time.Time { return now },
		SchedulerParticipant:     scheduling.TaskEnqueueParticipant(),
		SchedulerEnqueueFunction: scheduling.TaskEnqueueFunction(),
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "postgres-scheduler-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "postgres-scheduler-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-scheduler-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract: taskorchestration.PhaseValidationAllRuntimeRunsSucceeded, RequiredRuntimeRuns: 1,
	}})
	started, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "postgres-scheduler-start", "postgres-scheduler-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start Task: %v", err)
	}
	header := intentHeader(t, "postgres-scheduler-work", "postgres-scheduler-task", now.Add(time.Second))
	header.ExpectedTaskRevision = started.AcceptedTaskRevision
	if _, err := adapter.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		header, worker, operationID(t, "postgres-scheduler-availability"),
	)); err != nil {
		t.Fatalf("make Runtime work available: %v", err)
	}

	first, err := scheduling.ClaimAndAdmit(context.Background())
	if err != nil {
		t.Fatalf("claim and admit: %v", err)
	}
	replayed, err := scheduling.ClaimAndAdmit(context.Background())
	if err != nil {
		t.Fatalf("replay claim and admit: %v", err)
	}
	if replayed.WorkItemID != first.WorkItemID || replayed.Grant != first.Grant ||
		replayed.OperationID != first.OperationID || !bytes.Equal(replayed.CanonicalPayload, first.CanonicalPayload) {
		t.Fatalf("exact delivery replay allocated new authority:\nfirst=%+v\nreplay=%+v", first, replayed)
	}

	now = first.Grant.ExpiresAt.Add(time.Nanosecond)
	rotated, err := scheduling.ClaimAndAdmit(context.Background())
	if err != nil {
		t.Fatalf("rotate expired unbound grant: %v", err)
	}
	if rotated.WorkItemID != first.WorkItemID || rotated.OperationID != first.OperationID ||
		rotated.PayloadDigest != first.PayloadDigest || !bytes.Equal(rotated.CanonicalPayload, first.CanonicalPayload) ||
		rotated.Grant.Generation != first.Grant.Generation+1 || rotated.Grant.AdmissionGrantID == first.Grant.AdmissionGrantID {
		t.Fatalf("grant rotation changed Task authority or failed to advance generation:\nfirst=%+v\nrotated=%+v", first, rotated)
	}
	view, err := scheduling.Inspect(context.Background(), scheduler.WorkItemRef{WorkItemID: first.WorkItemID})
	if err != nil {
		t.Fatalf("inspect admitted Work Item: %v", err)
	}
	if view.State != scheduler.WorkItemDelivering || view.Grant.State != scheduler.GrantReservedUnbound ||
		view.Grant.Generation != rotated.Grant.Generation {
		t.Fatalf("Scheduler inspection lost current unbound generation: %+v", view)
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
		t.Fatalf("bind authenticated grant to canonical Task request: %v", err)
	}
	runtime, err := runtimeexecution.NewPostgresAuthority(db, runtimeexecution.PostgresConfig{
		Schema:                      schema,
		Now:                         func() time.Time { return now },
		SchedulerParticipant:        scheduling.RuntimeAcceptanceParticipant(),
		SchedulerAcceptanceFunction: scheduling.RuntimeAcceptanceFunction(),
	})
	if err != nil {
		t.Fatalf("create Runtime Execution authority: %v", err)
	}
	if err := runtime.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate Runtime Execution authority: %v", err)
	}
	accepted, err := runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute fresh C03 Start: %v", err)
	}
	inspected, err := runtime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	if err != nil {
		t.Fatalf("inspect accepted C03 Start: %v", err)
	}
	if inspected != accepted.Snapshot || inspected.State != runtimeexecution.RuntimeWaitingForLease ||
		inspected.RuntimeFence != start.ExpectedRuntimeFence+1 ||
		inspected.Operation.OperationID != start.OperationID ||
		inspected.Operation.Digest != start.CanonicalRequestDigest ||
		inspected.Operation.AdmissionGrantID != start.AdmissionGrant.AdmissionGrantID ||
		inspected.Operation.GrantGeneration != start.AdmissionGrant.Generation ||
		inspected.Lease.AcquireOperationID.String() == "" || inspected.Lease.AcquireDigest == (runtimeexecution.Digest{}) ||
		!inspected.LeaseAcquireBy.Equal(rotated.Grant.ExpiresAt) {
		t.Fatalf("fresh C03 Start lost atomic binding: %+v", inspected)
	}
	view, err = scheduling.Inspect(context.Background(), scheduler.WorkItemRef{WorkItemID: first.WorkItemID})
	if err != nil {
		t.Fatalf("inspect accepted Scheduler Work Item: %v", err)
	}
	if view.State != scheduler.WorkItemAccepted || view.Grant.State != scheduler.GrantBound ||
		view.Grant.Generation != rotated.Grant.Generation {
		t.Fatalf("C03 acceptance did not atomically bind exact Scheduler generation: %+v", view)
	}
	replayedStart, err := runtime.Execute(context.Background(), start)
	if err != nil || replayedStart.Fact != accepted.Fact || replayedStart.Snapshot != accepted.Snapshot {
		t.Fatalf("exact C03 replay changed accepted authority: replay=%+v err=%v", replayedStart, err)
	}
	cancelOperationID, err := runtimeexecution.NewOperationID("cancel-" + start.OperationID.String())
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := runtimeexecution.NewCancelRuntimeRun(runtimeexecution.CancelRuntimeRunInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: cancelOperationID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: inspected.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: inspected.Operation.Generation, ExpectedRuntimeFence: inspected.RuntimeFence,
		Authority: start.Authority, Reason: runtimeexecution.CancellationUserRequested,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("construct canonical C03 Cancel: %v", err)
	}
	cancelled, err := runtime.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatalf("execute pre-lease C03 Cancel: %v", err)
	}
	if cancelled.Snapshot.State != runtimeexecution.RuntimeTerminal ||
		cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled ||
		cancelled.Snapshot.RuntimeFence != inspected.RuntimeFence+1 ||
		cancelled.Snapshot.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityReleaseReady ||
		cancelled.Snapshot.Capacity.NoLease != runtimeexecution.NoLeaseDispositionRecorded ||
		cancelled.Snapshot.Capacity.Physical != runtimeexecution.PhysicalCapacityNotApplicable ||
		cancelled.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal == (runtimeexecution.RuntimeFencedOrTerminalEvidence{}) ||
		cancelled.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition == (runtimeexecution.NoLeasePhysicalDispositionEvidence{}) ||
		cancelled.Snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (runtimeexecution.PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("pre-lease Cancel produced incomplete or fabricated evidence: %+v", cancelled.Snapshot)
	}
	if err := scheduling.ApplyRuntimeFencedOrTerminal(
		context.Background(), cancelled.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal,
	); err != nil {
		t.Fatalf("apply logical release evidence: %v", err)
	}
	view, err = scheduling.Inspect(context.Background(), scheduler.WorkItemRef{WorkItemID: first.WorkItemID})
	if err != nil {
		t.Fatal(err)
	}
	if view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationBound || view.Grant.State != scheduler.GrantBound {
		t.Fatalf("logical evidence crossed into selected-node capacity: %+v", view)
	}
	if err := scheduling.ApplyNoLeasePhysicalDisposition(
		context.Background(), cancelled.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition,
	); err != nil {
		t.Fatalf("apply exact no-lease selected-node disposition: %v", err)
	}
	view, err = scheduling.Inspect(context.Background(), scheduler.WorkItemRef{WorkItemID: first.WorkItemID})
	if err != nil {
		t.Fatal(err)
	}
	if view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationReleased || view.Grant.State != scheduler.GrantReleased {
		t.Fatalf("separate capacity dispositions did not converge exactly: %+v", view)
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
	decision, err := adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "postgres-observer-start", "postgres-observer-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit with failing projection observer: %v", err)
	}
	if decision.AcceptedTaskRevision != 1 || observations.Load() != 0 {
		t.Fatal("projection delivery ran in the protected Decision path")
	}
	if _, err := adapter.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			taskorchestration.NewAdministratorMetadataAuthority(
				authorityID(t, "postgres-observer-projection-administrator"),
				taskorchestration.AuthorizationGeneration(1),
				taskorchestration.DiagnosticReasonOperations,
			),
			taskID(t, "postgres-observer-task"), 1,
		),
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
			start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	start, err := firstAdapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
		minimalPinnedStartIntent(t,
			intentHeader(t, "postgres-independent-start-a", "postgres-independent-task-a", now), owners[0],
		),
		minimalPinnedStartIntent(t,
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
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "postgres-evidence-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
	if len(work.MandatoryAuditFactRef.EnactmentRefs) != len(work.EnactmentRefs) ||
		len(work.MandatoryAuditFactRef.EnactmentRefs) != 1 {
		t.Fatalf("work audit fact omitted enactment bindings: %+v", work.MandatoryAuditFactRef)
	}
	assertPostgresMandatoryAuditRefs(t, db, schema, work.MandatoryAuditFactRef)
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
		evidenceHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
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
	if !reflect.DeepEqual(accepted.MandatoryAuditFactRef.EvidenceRefs, accepted.AcceptedEvidenceRefs) {
		t.Fatalf("evidence audit fact omitted accepted evidence: %+v", accepted.MandatoryAuditFactRef)
	}
	assertPostgresMandatoryAuditRefs(t, db, schema, accepted.MandatoryAuditFactRef)
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
		duplicateHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
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

func assertPostgresMandatoryAuditRefs(
	t *testing.T,
	db *sql.DB,
	schema string,
	fact taskorchestration.AuditFactRef,
) {
	t.Helper()
	var persistedState []byte
	if err := db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT audit_state
		FROM %s.task_orchestration_mandatory_audit_facts WHERE audit_fact_id=$1`, schema),
		fact.AuditFactID.String(),
	).Scan(&persistedState); err != nil {
		t.Fatalf("read mandatory audit refs: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(persistedState, &stored); err != nil {
		t.Fatalf("decode mandatory audit refs: %v", err)
	}
	var expectedEvidence []map[string]any
	for _, ref := range fact.EvidenceRefs {
		expectedEvidence = append(expectedEvidence, map[string]any{
			"ID": ref.ID.String(), "Kind": ref.Kind, "Digest": ref.Digest,
		})
	}
	var expectedEnactments []map[string]any
	for _, ref := range fact.EnactmentRefs {
		expectedEnactments = append(expectedEnactments, map[string]any{
			"OperationID": ref.OperationID.String(), "Kind": ref.Kind,
			"PayloadDigest": ref.PayloadDigest, "ActivityGeneration": ref.ActivityGeneration,
			"FenceKind": ref.FenceKind, "Fence": ref.Fence, "CausationID": ref.CausationID.String(),
		})
	}
	if !reflect.DeepEqual(stored["EvidenceRefs"], normalizedJSONValue(t, expectedEvidence)) ||
		!reflect.DeepEqual(stored["EnactmentRefs"], normalizedJSONValue(t, expectedEnactments)) {
		t.Fatalf("PostgreSQL audit refs differ from authoritative fact: stored=%s fact=%+v", persistedState, fact)
	}
}

func normalizedJSONValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode expected JSON value: %v", err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatalf("normalize expected JSON value: %v", err)
	}
	return normalized
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
	if _, err := adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
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
	_, err = adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
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
	start, err := adapter.Decide(context.Background(), verifiedPinnedStartIntent(t,
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
		pinned.Authorities.Runtime,
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
	commitBinding := exactC04CommitRequestBinding(
		t, "postgres-lifecycle-task", run.PhaseRunID, pinned.TaskWorkspaceID,
		"postgres-lifecycle-commit-operation",
		taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
		taskorchestration.TaskWorkspaceLifecycleFence(run.Fence), view.LatestRevisionID,
	)
	validationDecision, err := adapter.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
		validationHeader,
		pinned.Authorities.Validator,
		taskorchestration.ValidationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "postgres-lifecycle-validation-evidence"),
				taskorchestration.EvidencePhaseValidation,
				evidenceDigest(t, "2222222222222222222222222222222222222222222222222222222222222222"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			Generation: taskorchestration.ProducerGeneration(run.Generation),
			Fence:      taskorchestration.ValidationFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: commitBinding,
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
		pinned.Authorities.TaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "postgres-lifecycle-commit-evidence"),
				taskorchestration.EvidenceTaskWorkspaceLifecycle,
				evidenceDigest(t, "3333333333333333333333333333333333333333333333333333333333333333"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			OperationID:        validationDecision.EnactmentRefs[0].OperationID,
			Generation:         taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
			Fence:              taskorchestration.TaskWorkspaceLifecycleFence(run.Fence),
			ObservedGeneration: taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
			ObservedFence:      taskorchestration.TaskWorkspaceLifecycleFence(run.Fence + 1),
			SafetyEpoch:        view.SafetyEpoch,
			Outcome:            taskorchestration.TaskWorkspaceLifecycleCommitted,
			RevisionID:         revisionID, CheckpointID: checkpoint,
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
		canonical_payload bytea NOT NULL,
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
		p_canonical_payload bytea,
		p_activity_generation bigint,
		p_fence_kind smallint,
		p_fence bigint,
		p_causation_id text
	) RETURNS void LANGUAGE SQL AS $scheduler_function$
		INSERT INTO `+schema+`.scheduler_test_owned_work_items (
			work_item_id, operation_id, task_id, phase_run_id, runtime_run_id, decision_id,
			task_revision, kind, payload_digest, canonical_payload, activity_generation, fence_kind, fence,
			causation_id, priority_class, state, enqueued_at
		) VALUES (
			'scheduler-test-' || p_operation_id, p_operation_id, p_task_id, p_phase_run_id,
			p_runtime_run_id, p_decision_id, p_task_revision, p_kind, p_payload_digest, p_canonical_payload,
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
