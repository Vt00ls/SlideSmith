package runtimeexecution

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

func TestPostgresAuthorityReplaysRetainedDecisionWithCurrentSnapshot(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-replay-owner", 12)
	start := standardStart(t, now, owner, "postgres-replay")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	fixture.RuntimeRevision++
	fixture.State = RuntimeReconciling
	fixture.Reconciliation = ReconciliationRequiredStatus
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	original := RuntimeDecisionFact{
		DecisionID:  RuntimeDecisionID{value: "runtime-decision-postgres-replay"},
		Disposition: DecisionAccepted, OperationID: start.OperationID,
		CanonicalRequestDigest:   start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	installPostgresAcceptedStartFacts(t, db, schema, start, original, now)

	replayed, err := store.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replayed.Fact != original || replayed.Snapshot.RuntimeRevision != fixture.RuntimeRevision ||
		replayed.Snapshot.State != RuntimeReconciling || replayed.Snapshot.Reconciliation != ReconciliationRequiredStatus {
		t.Fatalf("replay = %#v, want original fact %#v and current Runtime revision %d", replayed, original, fixture.RuntimeRevision)
	}

	conflictingInput := start.StartRuntimeRunInput
	conflictingInput.Deadline = start.Deadline.Add(time.Minute)
	conflicting := mustStart(t, conflictingInput)
	_, err = store.Execute(context.Background(), conflicting)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting replay error = %T %v, want non-retryable integrity conflict", err, err)
	}

	replayedAgain, err := store.Execute(context.Background(), start)
	if err != nil || replayedAgain.Fact != original {
		t.Fatalf("conflict changed original retained Decision: decision=%#v err=%v", replayedAgain, err)
	}
}

func TestPostgresMalformedCancelIsInvalidRequest(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	store := newMigratedPostgresAuthority(t, db, schema, time.Date(2026, 7, 28, 10, 30, 0, 0, time.UTC))
	malformed := CancelRuntimeRun{CancelRuntimeRunInput: CancelRuntimeRunInput{SchemaVersion: SchemaV1}}

	_, err := store.Execute(context.Background(), malformed)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorInvalidRequest {
		t.Fatalf("malformed Cancel error = %T %v, want invalid request", err, err)
	}
}

func TestPostgresOperationIdentityCannotCrossCommandKinds(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 10, 40, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-cross-kind-owner", 13)
	start := standardStart(t, now, owner, "postgres-cross-kind")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-postgres-cross-kind")
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: start.OperationID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: fixture.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: fixture.OperationGeneration, ExpectedRuntimeFence: fixture.RuntimeFence,
		Authority: owner, Reason: CancellationUserRequested,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("new cross-kind Cancel: %v", err)
	}

	_, err = store.Execute(context.Background(), cancel)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("cross-kind operation reuse error = %T %v, want non-retryable integrity conflict", err, err)
	}
	var incidents int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_integrity_incidents WHERE runtime_run_id=$1 AND operation_id=$2`,
		start.RuntimeRunID.String(), start.OperationID.String()).Scan(&incidents); err != nil || incidents != 1 {
		t.Fatalf("cross-kind conflict incidents = %d err=%v, want one durable content-free incident", incidents, err)
	}
}

func TestPostgresRetainedStartReplayRejectsGrantRebinding(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 10, 50, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-grant-rebind-owner", 13)
	start := standardStart(t, now, owner, "postgres-grant-rebind")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-postgres-grant-rebind")
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)

	reboundInput := start.StartRuntimeRunInput
	reboundInput.AdmissionGrant.AdmissionGrantID = mustAdmissionGrantID(t, "grant-postgres-rebound")
	reboundInput.AdmissionGrant.Generation++
	rebound := mustStart(t, reboundInput)
	if rebound.CanonicalRequestDigest != start.CanonicalRequestDigest {
		t.Fatal("replaceable delivery grant unexpectedly changed the canonical Start digest")
	}
	_, err := store.Execute(context.Background(), rebound)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("accepted Start grant rebinding error = %T %v, want non-retryable integrity conflict", err, err)
	}
}

func TestPostgresMandatoryAuditFailureRollsBackProtectedReconciliation(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-audit-owner", 14)
	start := standardStart(t, now, owner, "postgres-audit-rollback")
	faults := &PersistenceFaultController{}
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: faults,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-audit-rollback"), owner, ReconciliationTransportAmbiguous,
	)
	if err := faults.FailNextAt(PersistenceFaultBeforeMandatoryAudit); err != nil {
		t.Fatalf("configure mandatory-audit fault: %v", err)
	}
	_, err = store.persistReconciliationFoundation(context.Background(), intent)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorDependencyUnavailable {
		t.Fatalf("mandatory-audit fault error = %T %v, want safe unavailable", err, err)
	}

	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil {
		t.Fatalf("inspect after mandatory-audit rollback: %v", err)
	}
	if snapshot.RuntimeRevision != fixture.RuntimeRevision || snapshot.State != RuntimeWaitingForLease ||
		snapshot.Reconciliation != ReconciliationStable {
		t.Fatalf("mandatory-audit failure changed Runtime authority: %#v", snapshot)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{}) {
		t.Fatalf("mandatory-audit failure left partial authoritative facts: %+v", counts)
	}
}

func TestPostgresMandatoryAuditPersistsCompleteCanonicalFact(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 11, 30, 0, 987654321, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-complete-audit-owner", 14)
	start := standardStart(t, now, owner, "postgres-complete-audit")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-complete-audit"), owner, ReconciliationTransportAmbiguous,
	)
	decision, err := store.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("persist reconciliation foundation: %v", err)
	}
	ref := postgresMandatoryAuditRef{
		DecisionID: decision.Fact.DecisionID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID,
		OperationID:  intent.OperationID, RequestDigest: intent.CanonicalDigest, Authority: owner,
	}
	view, err := store.loadMandatoryAudit(context.Background(), ref)
	if err != nil {
		t.Fatalf("load complete mandatory AuditFact: %v", err)
	}
	if !validOpaqueID(view.State.AuditFactID) {
		t.Fatalf("mandatory AuditFact has invalid identity: %#v", view.State)
	}
	expected := postgresMandatoryAuditState{
		AuditFactID: view.State.AuditFactID, SchemaVersion: SchemaV1, IntegrityVersion: 1,
		OwningModule: "runtime_execution", DecisionID: decision.Fact.DecisionID.String(),
		RuntimeRunID: fixture.RuntimeRunID.String(), OperationID: intent.OperationID.String(),
		RequestDigest: intent.CanonicalDigest.String(), AuthorityKind: owner.kind,
		AuthorityID: owner.id.String(), AuthorityGeneration: owner.generation,
		Action: postgresAuditReconciliationRequired, Result: postgresAuditAccepted,
		ReasonCode:     uint8(ReconciliationTransportAmbiguous),
		BeforeRevision: decision.Fact.PreviousRuntimeRevision, AfterRevision: decision.Fact.ResultingRuntimeRevision,
		BeforeState: RuntimeWaitingForLease, AfterState: RuntimeReconciling,
		BeforeOperationGeneration: fixture.OperationGeneration,
		AfterOperationGeneration:  fixture.OperationGeneration,
		BeforeRuntimeFence:        fixture.RuntimeFence,
		AfterRuntimeFence:         fixture.RuntimeFence,
		AuthorizationEpoch:        owner.generation,
		BeforeSafetyEpoch:         fixture.SafetyEpoch,
		AfterSafetyEpoch:          fixture.SafetyEpoch,
		OccurredAt:                postgresTimestamp(now).Format(canonicalTimeFormat),
		RecordedAt:                postgresTimestamp(now).Format(canonicalTimeFormat),
		SourceClockID:             "platform_control_plane", EvidenceRootDigest: fixture.EvidenceRoot.Digest.String(),
		IdempotencyReference: intent.OperationID.String(), RetryDisposition: decision.Fact.Retry,
		ReconciliationDisposition: decision.Fact.Reconciliation,
	}
	wantProjection := ProjectionFact{
		DecisionID: decision.Fact.DecisionID, RuntimeRunID: fixture.RuntimeRunID,
		OperationID: intent.OperationID, CanonicalDigest: intent.CanonicalDigest,
		RuntimeRevision: decision.Fact.ResultingRuntimeRevision,
		AuditFactID:     expected.AuditFactID, AuditCanonicalDigest: view.CanonicalDigest,
		ProjectionSchemaVersion: SchemaV1,
	}
	if view.State != expected || view.CanonicalDigest == (Digest{}) || view.Projection != wantProjection {
		t.Fatalf("mandatory AuditFact view = %#v, want state=%#v and projection=%#v",
			view, expected, wantProjection)
	}

	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	replayed, err := restarted.persistReconciliationFoundation(context.Background(), intent)
	if err != nil || replayed.Fact != decision.Fact {
		t.Fatalf("restart replay = %#v err=%v, want original Decision", replayed, err)
	}
	reloaded, err := restarted.loadMandatoryAudit(context.Background(), ref)
	if err != nil || reloaded != view {
		t.Fatalf("reloaded mandatory AuditFact = %#v err=%v, want %#v", reloaded, err, view)
	}
}

func TestPostgresCleanupDebtCreationReplaysAndSurvivesRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 11, 40, 0, 123456789, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-cleanup-owner", 15)
	start := standardStart(t, now, owner, "postgres-cleanup-create")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-postgres-cleanup-create")
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)

	creation := cleanupDebtCreation{
		MutationID: "cleanup-mutation-create", DebtID: "cleanup-debt-create",
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		ResourceClass: cleanupResourceSandbox, ResourceIdentityDigest: digest(101),
		ResourceGeneration: 3, ResourceFence: 5, Intent: cleanupIntentReclaim,
		CauseDecisionID: fact.DecisionID, CauseOperationID: fact.OperationID,
		RetentionFactDigest: digest(102), EligibilityFactDigest: digest(103),
		CreatedAt: now, EligibleAt: now.Add(-time.Minute),
		Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		Blockers: cleanupBlockerSummary{
			Classes: cleanupBlockerGracePeriod | cleanupBlockerQuarantine, Digest: digest(104),
		},
		Uncontained: true,
	}
	created, err := store.createCleanupDebt(context.Background(), creation)
	if err != nil {
		t.Fatalf("create Cleanup Debt: %v", err)
	}
	if created.DebtID != creation.DebtID || created.Revision != 1 || created.Status != cleanupDebtBlocked ||
		created.OwnerModule != postgresCleanupOwnerModule || created.PersonalWorkspaceID != fixture.PersonalWorkspaceID ||
		created.TaskID != fixture.TaskID || created.PhaseRunID != fixture.PhaseRunID ||
		created.RuntimeRunID != fixture.RuntimeRunID || created.CleanupIntent != creation.Intent ||
		created.CauseDecisionID != fact.DecisionID || created.CauseOperationID != fact.OperationID ||
		created.RetentionFactDigest != creation.RetentionFactDigest ||
		created.EligibilityFactDigest != creation.EligibilityFactDigest ||
		created.CreatedAt != now.Truncate(time.Microsecond) ||
		created.EligibleAt != creation.EligibleAt.Truncate(time.Microsecond) ||
		created.FirstAttemptAt != (time.Time{}) || created.LastAttemptAt != (time.Time{}) ||
		created.Estimation.State != cleanupEstimateUnknown || created.Blockers != creation.Blockers ||
		created.RetryDisposition != cleanupRetryBlocked || !created.Unresolved || !created.Uncontained {
		t.Fatalf("created Cleanup Debt omitted authoritative bindings: %#v", created)
	}
	exact, err := store.createCleanupDebt(context.Background(), creation)
	if err != nil || exact != created {
		t.Fatalf("exact Cleanup Debt replay = %#v err=%v, want %#v", exact, err, created)
	}

	conflict := creation
	conflict.ResourceFence++
	_, err = store.createCleanupDebt(context.Background(), conflict)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting Cleanup Debt binding error = %T %v, want non-retryable integrity conflict", err, err)
	}

	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	loaded, err := restarted.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: creation.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || loaded != created {
		t.Fatalf("restart Cleanup Debt readback = %#v err=%v, want %#v", loaded, err, created)
	}
}

func TestPostgresCleanupDebtPersistsRetryEstimationBlockersAndResolution(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 12, 0, 0, 987654321, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-cleanup-lifecycle-owner", 16)
	start := standardStart(t, now, owner, "postgres-cleanup-lifecycle")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	fixture.EvidenceRoot = EvidenceRootSnapshot{
		SchemaVersion: SchemaV1, EvidenceRootID: EvidenceRootID{value: "cleanup-resolution-evidence"},
		Digest: digest(113),
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-postgres-cleanup-lifecycle")
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)

	created, err := store.createCleanupDebt(context.Background(), cleanupDebtCreation{
		MutationID: "cleanup-mutation-lifecycle-create", DebtID: "cleanup-debt-lifecycle",
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		ResourceClass: cleanupResourceContainment, ResourceIdentityDigest: digest(105),
		ResourceGeneration: 7, ResourceFence: 9, Intent: cleanupIntentContain,
		CauseDecisionID: fact.DecisionID, CauseOperationID: fact.OperationID,
		RetentionFactDigest: digest(106), EligibilityFactDigest: digest(107),
		CreatedAt: now, EligibleAt: now, Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
	})
	if err != nil {
		t.Fatalf("create lifecycle Cleanup Debt: %v", err)
	}
	firstAttemptAt := now.Add(time.Minute)
	first, err := store.recordCleanupDebtAttempt(context.Background(), cleanupDebtAttempt{
		MutationID: "cleanup-mutation-attempt-1", DebtID: created.DebtID,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		ExpectedRevision: created.Revision, ResourceGeneration: created.ResourceGeneration,
		ResourceFence: created.ResourceFence, ClaimGeneration: 2, ClaimFence: 11,
		AttemptedAt: firstAttemptAt, NextRetryAt: firstAttemptAt.Add(5 * time.Minute),
		RetryDisposition: cleanupRetryBlocked, FailureCategory: cleanupFailureUnavailable,
		LastErrorDigest: digest(108), LastErrorEvidenceReference: "cleanup-error-evidence-1",
		Estimation: cleanupEstimation{
			State: cleanupEstimateKnown, Method: cleanupEstimateAdapterObservation,
			Bytes: 4096, Inodes: 12, ObservedAt: firstAttemptAt,
		},
		Blockers: cleanupBlockerSummary{
			Classes: cleanupBlockerLease | cleanupBlockerIncident, Digest: digest(109),
		},
		Uncontained: true,
	})
	if err != nil {
		t.Fatalf("record first Cleanup Debt attempt: %v", err)
	}
	lastAttemptAt := firstAttemptAt.Add(2 * time.Minute)
	secondInput := cleanupDebtAttempt{
		MutationID: "cleanup-mutation-attempt-2", DebtID: created.DebtID,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		ExpectedRevision: first.Revision, ResourceGeneration: created.ResourceGeneration,
		ResourceFence: created.ResourceFence, ClaimGeneration: 3, ClaimFence: 12,
		AttemptedAt: lastAttemptAt, NextRetryAt: lastAttemptAt.Add(10 * time.Minute),
		RetryDisposition: cleanupRetryBlocked, FailureCategory: cleanupFailureUnavailable,
		LastErrorDigest: digest(110), LastErrorEvidenceReference: "cleanup-error-evidence-2",
		Estimation: cleanupEstimation{
			State: cleanupEstimateKnown, Method: cleanupEstimateAdapterObservation,
			Bytes: 6144, Inodes: 14, ObservedAt: lastAttemptAt,
		},
		Blockers: cleanupBlockerSummary{
			Classes: cleanupBlockerReference | cleanupBlockerQuarantine, Digest: digest(111),
		},
		Uncontained: true,
	}
	second, err := store.recordCleanupDebtAttempt(context.Background(), secondInput)
	if err != nil {
		t.Fatalf("record second Cleanup Debt attempt: %v", err)
	}
	if second.Revision != 3 || second.AttemptCount != 2 || second.ConsecutiveFailureCount != 2 ||
		second.FirstAttemptAt != firstAttemptAt.Truncate(time.Microsecond) ||
		second.LastAttemptAt != lastAttemptAt.Truncate(time.Microsecond) ||
		second.ClaimGeneration != 3 || second.ClaimFence != 12 ||
		second.RetryDisposition != cleanupRetryBlocked || second.LastErrorCategory != cleanupFailureUnavailable ||
		second.LastErrorDigest != secondInput.LastErrorDigest ||
		second.LastErrorEvidenceReference != secondInput.LastErrorEvidenceReference ||
		second.Estimation != (cleanupEstimation{
			State: cleanupEstimateKnown, Method: cleanupEstimateAdapterObservation,
			Bytes: 6144, Inodes: 14, ObservedAt: lastAttemptAt.Truncate(time.Microsecond),
		}) ||
		second.Blockers != secondInput.Blockers || !second.Unresolved || !second.Uncontained {
		t.Fatalf("Cleanup Debt retry facts were not authoritative: %#v", second)
	}
	exactSecond, err := store.recordCleanupDebtAttempt(context.Background(), secondInput)
	if err != nil || exactSecond != second {
		t.Fatalf("exact retry replay = %#v err=%v, want %#v", exactSecond, err, second)
	}
	conflictingSecond := secondInput
	conflictingSecond.LastErrorDigest = digest(112)
	_, err = store.recordCleanupDebtAttempt(context.Background(), conflictingSecond)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict {
		t.Fatalf("conflicting retry binding error = %T %v, want integrity conflict", err, err)
	}

	resolvedAt := lastAttemptAt.Add(time.Minute)
	resolution := cleanupDebtResolution{
		MutationID: "cleanup-mutation-resolve", DebtID: created.DebtID,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		ExpectedRevision: second.Revision, ResourceGeneration: created.ResourceGeneration,
		ResourceFence: created.ResourceFence, ResolvedAt: resolvedAt,
		Class: cleanupResolutionAlreadyAbsent, Reason: cleanupResolutionExactGenerationAbsent,
		EvidenceRoot: fixture.EvidenceRoot,
	}
	_, err = store.resolveCleanupDebt(context.Background(), resolution)
	var missingProofError *Error
	if !errors.As(err, &missingProofError) || missingProofError.Code() != ErrorIntegrityConflict ||
		missingProofError.RetryDisposition() != RetryNever {
		t.Fatalf("unretained cleanup evidence error = %T %v, want non-retryable integrity conflict", err, err)
	}
	stillOpen, err := store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || !stillOpen.Unresolved || stillOpen.Revision != second.Revision {
		t.Fatalf("missing resolution proof changed Cleanup Debt: %#v err=%v", stillOpen, err)
	}
	installPostgresCleanupEvidenceRoot(t, db, schema, fixture.RuntimeRunID, fixture.EvidenceRoot, resolvedAt)

	uncontainedResolution := resolution
	uncontainedResolution.Uncontained = true
	_, err = store.resolveCleanupDebt(context.Background(), uncontainedResolution)
	var unsafeDispositionError *Error
	if !errors.As(err, &unsafeDispositionError) || unsafeDispositionError.Code() != ErrorInvalidRequest ||
		unsafeDispositionError.RetryDisposition() != RetryNever {
		t.Fatalf("uncontained already-absent resolution error = %T %v, want non-retryable invalid request", err, err)
	}
	stillOpen, err = store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || !stillOpen.Unresolved || stillOpen.Revision != second.Revision {
		t.Fatalf("unsafe resolution changed Cleanup Debt: %#v err=%v", stillOpen, err)
	}

	unauthorizedException := resolution
	unauthorizedException.Class = cleanupResolutionAcceptedException
	unauthorizedException.Reason = cleanupResolutionAdministratorException
	unauthorizedException.ExceptionUntil = resolvedAt.Add(time.Hour)
	_, err = store.resolveCleanupDebt(context.Background(), unauthorizedException)
	var authorizationError *Error
	if !errors.As(err, &authorizationError) || authorizationError.Code() != ErrorAuthorizationDenied ||
		authorizationError.RetryDisposition() != RetryNever {
		t.Fatalf("Task Orchestration exception resolution error = %T %v, want non-retryable authorization denial", err, err)
	}
	_, err = store.resolveCleanupDebt(context.Background(), resolution)
	var missingClassProofError *Error
	if !errors.As(err, &missingClassProofError) || missingClassProofError.Code() != ErrorIntegrityConflict ||
		missingClassProofError.RetryDisposition() != RetryNever {
		t.Fatalf("unretained class-specific cleanup proof error = %T %v, want non-retryable integrity conflict", err, err)
	}
	stillOpen, err = store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || !stillOpen.Unresolved || stillOpen.Revision != second.Revision {
		t.Fatalf("missing class-specific proof changed Cleanup Debt: %#v err=%v", stillOpen, err)
	}
	installPostgresCleanupResolutionProof(t, db, schema, second, resolution, resolvedAt)

	faults := &PersistenceFaultController{}
	faultedStore, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return resolvedAt }, Faults: faults,
	})
	if err != nil {
		t.Fatalf("new faulted PostgreSQL authority: %v", err)
	}
	if err := faults.FailNextAt(PersistenceFaultBeforeMandatoryAudit); err != nil {
		t.Fatalf("configure cleanup mandatory-audit fault: %v", err)
	}
	_, err = faultedStore.resolveCleanupDebt(context.Background(), resolution)
	var auditFailure *Error
	if !errors.As(err, &auditFailure) || auditFailure.Code() != ErrorDependencyUnavailable {
		t.Fatalf("cleanup mandatory-audit fault error = %T %v, want safe unavailable", err, err)
	}
	stillOpen, err = store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || !stillOpen.Unresolved || stillOpen.Revision != second.Revision {
		t.Fatalf("cleanup mandatory-audit failure changed Cleanup Debt: %#v err=%v", stillOpen, err)
	}
	if err := faults.FailNextAt(PersistenceFaultAfterMandatoryAudit); err != nil {
		t.Fatalf("configure post-cleanup-audit fault: %v", err)
	}
	_, err = faultedStore.resolveCleanupDebt(context.Background(), resolution)
	var postAuditFailure *Error
	if !errors.As(err, &postAuditFailure) || postAuditFailure.Code() != ErrorDependencyUnavailable {
		t.Fatalf("post-cleanup-audit fault error = %T %v, want safe unavailable", err, err)
	}
	stillOpen, err = store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || !stillOpen.Unresolved || stillOpen.Revision != second.Revision {
		t.Fatalf("post-cleanup-audit failure changed Cleanup Debt: %#v err=%v", stillOpen, err)
	}

	resolved, err := store.resolveCleanupDebt(context.Background(), resolution)
	if err != nil {
		t.Fatalf("resolve Cleanup Debt: %v", err)
	}
	if resolved.Revision != 4 || resolved.Status != cleanupDebtResolved || resolved.Unresolved ||
		resolved.RetryDisposition != cleanupRetryNone || resolved.ResolvedAt != resolvedAt.Truncate(time.Microsecond) ||
		resolved.ResolutionClass != resolution.Class || resolved.ResolutionReason != resolution.Reason ||
		resolved.ResolutionAuthority != owner || !validOpaqueID(resolved.ResolutionAuditFactID) ||
		resolved.ResolutionEvidenceRoot != resolution.EvidenceRoot || resolved.Blockers != (cleanupBlockerSummary{}) ||
		resolved.AttemptCount != 2 || resolved.FirstAttemptAt == resolved.LastAttemptAt {
		t.Fatalf("resolved Cleanup Debt omitted final authority: %#v", resolved)
	}
	var retainedAuditDigest, retainedAuditState []byte
	if err := db.QueryRowContext(context.Background(), `SELECT canonical_digest, audit_state FROM `+schema+
		`.runtime_execution_cleanup_resolution_audit WHERE audit_fact_id=$1`, resolved.ResolutionAuditFactID).Scan(
		&retainedAuditDigest, &retainedAuditState,
	); err != nil {
		t.Fatalf("load retained cleanup resolution audit: %v", err)
	}
	auditState, auditDigest, err := decodeCleanupResolutionAudit(retainedAuditState)
	if err != nil || !bytes.Equal(retainedAuditDigest, auditDigest[:]) {
		t.Fatalf("decode retained cleanup resolution audit: state=%#v digest=%x err=%v", auditState, retainedAuditDigest, err)
	}
	wantAuditState := cleanupResolutionAuditState{
		AuditFactID: resolved.ResolutionAuditFactID, SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupResolutionAuditIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		OperationID:      resolution.MutationID, OperationDigest: auditState.OperationDigest,
		Action: postgresCleanupResolutionAuditAction, Result: postgresCleanupResolutionAuditAccepted,
		DebtID: created.DebtID, DebtRevision: resolved.Revision,
		BeforeDebtRevision: second.Revision, AfterDebtRevision: resolved.Revision,
		BeforeDebtStatus: second.Status, AfterDebtStatus: cleanupDebtResolved,
		BeforeUnresolved: true, AfterUnresolved: false,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID.String(), TaskID: fixture.TaskID.String(),
		PhaseRunID: fixture.PhaseRunID.String(), RuntimeRunID: fixture.RuntimeRunID.String(),
		ResourceClass: created.ResourceClass, ResourceIdentityDigest: created.ResourceIdentityDigest.String(),
		ResourceGeneration: created.ResourceGeneration, ResourceFence: created.ResourceFence,
		BeforeResourceGeneration: created.ResourceGeneration, AfterResourceGeneration: created.ResourceGeneration,
		BeforeResourceFence: created.ResourceFence, AfterResourceFence: created.ResourceFence,
		CleanupIntent: created.CleanupIntent, CauseDecisionID: created.CauseDecisionID.String(),
		CauseOperationID: created.CauseOperationID.String(), ResolutionClass: resolution.Class,
		ResolutionReason: resolution.Reason, ResolutionAuthorityKind: owner.kind,
		ResolutionAuthorityID: owner.id.String(), ResolutionAuthorityGeneration: owner.generation,
		BeforeRuntimeRevision: fixture.RuntimeRevision, AfterRuntimeRevision: fixture.RuntimeRevision,
		BeforeOperationGeneration: fixture.OperationGeneration, AfterOperationGeneration: fixture.OperationGeneration,
		BeforeRuntimeFence: fixture.RuntimeFence, AfterRuntimeFence: fixture.RuntimeFence,
		AuthorizationEpoch: owner.generation,
		BeforeSafetyEpoch:  fixture.SafetyEpoch, AfterSafetyEpoch: fixture.SafetyEpoch,
		EvidenceSchemaVersion: fixture.EvidenceRoot.SchemaVersion,
		EvidenceRootID:        fixture.EvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:    fixture.EvidenceRoot.Digest.String(),
		ResolutionProofID:     "cleanup-resolution-proof-already-absent",
		ResolutionProofDigest: auditState.ResolutionProofDigest,
		OccurredAt:            formatCleanupTime(resolvedAt), RecordedAt: formatCleanupTime(resolvedAt),
		SourceClockID:        postgresMandatoryAuditSourceClock,
		IdempotencyReference: resolution.MutationID, RetryDisposition: RetryNever,
		ReconciliationDisposition: ReconciliationNotRequired,
	}
	if auditState.OperationDigest == (Digest{}).String() ||
		auditState.ResolutionProofDigest == (Digest{}).String() || auditState != wantAuditState {
		t.Fatalf("cleanup resolution audit = %#v, want complete canonical fact %#v", auditState, wantAuditState)
	}
	exactResolved, err := store.resolveCleanupDebt(context.Background(), resolution)
	if err != nil || exactResolved != resolved {
		t.Fatalf("exact resolution replay = %#v err=%v, want %#v", exactResolved, err, resolved)
	}
	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return resolvedAt }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	loaded, err := restarted.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
		RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || loaded != resolved {
		t.Fatalf("resolved Cleanup Debt readback = %#v err=%v, want %#v", loaded, err, resolved)
	}
	assertIntegrityConflict := func(label string, err error) {
		t.Helper()
		var integrityError *Error
		if !errors.As(err, &integrityError) || integrityError.Code() != ErrorIntegrityConflict ||
			integrityError.RetryDisposition() != RetryNever {
			t.Fatalf("%s error = %T %v, want non-retryable integrity conflict", label, err, err)
		}
	}
	loadResolved := func() error {
		_, err := restarted.loadCleanupDebt(context.Background(), cleanupDebtRef{
			DebtID: created.DebtID, PersonalWorkspaceID: fixture.PersonalWorkspaceID,
			RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		})
		return err
	}
	replayResolution := func() error {
		_, err := restarted.resolveCleanupDebt(context.Background(), resolution)
		return err
	}

	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_immutable_mutation ON `+schema+
		`.runtime_execution_evidence_roots`); err != nil {
		t.Fatal("disable immutable evidence trigger for corruption fixture")
	}
	corruptEvidenceDigest := digest(114)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_evidence_roots
		SET digest=$1 WHERE evidence_root_id=$2`, corruptEvidenceDigest[:], fixture.EvidenceRoot.EvidenceRootID.String()); err != nil {
		t.Fatal("install corrupt cleanup evidence fixture")
	}
	assertIntegrityConflict("corrupt cleanup evidence readback", loadResolved())
	assertIntegrityConflict("corrupt cleanup evidence replay", replayResolution())
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_evidence_roots
		SET digest=$1 WHERE evidence_root_id=$2`, fixture.EvidenceRoot.Digest[:], fixture.EvidenceRoot.EvidenceRootID.String()); err != nil {
		t.Fatal("restore cleanup evidence fixture")
	}
	if replayed, err := restarted.resolveCleanupDebt(context.Background(), resolution); err != nil || replayed != resolved {
		t.Fatalf("restored cleanup evidence replay = %#v err=%v, want %#v", replayed, err, resolved)
	}
	var retainedProofDigest []byte
	if err := db.QueryRowContext(context.Background(), `SELECT canonical_digest FROM `+schema+
		`.runtime_execution_cleanup_resolution_proofs WHERE proof_id=$1`,
		"cleanup-resolution-proof-already-absent").Scan(&retainedProofDigest); err != nil {
		t.Fatalf("load retained cleanup proof digest: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_immutable_mutation ON `+schema+
		`.runtime_execution_cleanup_resolution_proofs`); err != nil {
		t.Fatal("disable immutable cleanup-proof trigger for corruption fixture")
	}
	corruptProofDigest := digest(116)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_cleanup_resolution_proofs
		SET canonical_digest=$1 WHERE proof_id=$2`, corruptProofDigest[:],
		"cleanup-resolution-proof-already-absent"); err != nil {
		t.Fatal("install corrupt cleanup-proof fixture")
	}
	assertIntegrityConflict("corrupt cleanup proof readback", loadResolved())
	assertIntegrityConflict("corrupt cleanup proof replay", replayResolution())
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_cleanup_resolution_proofs
		SET canonical_digest=$1 WHERE proof_id=$2`, retainedProofDigest,
		"cleanup-resolution-proof-already-absent"); err != nil {
		t.Fatal("restore cleanup-proof fixture")
	}
	if replayed, err := restarted.resolveCleanupDebt(context.Background(), resolution); err != nil || replayed != resolved {
		t.Fatalf("restored cleanup proof replay = %#v err=%v, want %#v", replayed, err, resolved)
	}

	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_immutable_mutation ON `+schema+
		`.runtime_execution_cleanup_resolution_audit`); err != nil {
		t.Fatal("disable immutable cleanup-audit trigger for corruption fixture")
	}
	corruptAuditDigest := digest(115)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_cleanup_resolution_audit
		SET canonical_digest=$1 WHERE audit_fact_id=$2`, corruptAuditDigest[:], resolved.ResolutionAuditFactID); err != nil {
		t.Fatal("install corrupt cleanup-audit fixture")
	}
	assertIntegrityConflict("corrupt cleanup audit readback", loadResolved())
	assertIntegrityConflict("corrupt cleanup audit replay", replayResolution())
	if _, err := db.ExecContext(context.Background(), `DELETE FROM `+schema+
		`.runtime_execution_cleanup_resolution_audit WHERE audit_fact_id=$1`, resolved.ResolutionAuditFactID); err != nil {
		t.Fatal("install missing cleanup-audit fixture")
	}
	assertIntegrityConflict("missing cleanup audit readback", loadResolved())
	assertIntegrityConflict("missing cleanup audit replay", replayResolution())
}

func installPostgresCleanupEvidenceRoot(
	t *testing.T,
	db *sql.DB,
	schema string,
	runtimeRunID RuntimeRunID,
	evidenceRoot EvidenceRootSnapshot,
	acceptedAt time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_evidence_roots (
		evidence_root_id, runtime_run_id, schema_version, digest, accepted_at
	) VALUES ($1,$2,$3,$4,$5)`, evidenceRoot.EvidenceRootID.String(), runtimeRunID.String(),
		evidenceRoot.SchemaVersion, evidenceRoot.Digest[:], postgresTimestamp(acceptedAt)); err != nil {
		t.Fatal("install retained Cleanup Debt resolution evidence")
	}
}

func installPostgresCleanupResolutionProof(
	t *testing.T,
	db *sql.DB,
	schema string,
	debt cleanupDebtRecord,
	resolution cleanupDebtResolution,
	verifiedAt time.Time,
) {
	t.Helper()
	state := cleanupResolutionProofState{
		ProofID: "cleanup-resolution-proof-already-absent", SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupResolutionProofIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		DebtID:           debt.DebtID, RuntimeRunID: debt.RuntimeRunID.String(),
		ResourceClass: debt.ResourceClass, ResourceIdentityDigest: debt.ResourceIdentityDigest.String(),
		ResourceGeneration: debt.ResourceGeneration, ResourceFence: debt.ResourceFence,
		ResolutionClass: resolution.Class, ResolutionReason: resolution.Reason,
		Disposition:           cleanupProofExactGenerationAbsent,
		EvidenceSchemaVersion: resolution.EvidenceRoot.SchemaVersion,
		EvidenceRootID:        resolution.EvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:    resolution.EvidenceRoot.Digest.String(),
		ExactGenerationAbsent: true, ReferencesClear: true, ContainmentClear: true,
		ObservedAt: formatCleanupTime(verifiedAt), RecordedAt: formatCleanupTime(verifiedAt),
		SourceClockID: postgresMandatoryAuditSourceClock,
	}
	encoded, digest, err := encodeCleanupResolutionProof(state)
	if err != nil {
		t.Fatalf("encode cleanup resolution proof: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_cleanup_resolution_proofs (
		proof_id, debt_id, runtime_run_id, resource_identity_digest, resource_generation, resource_fence,
		resolution_class, resolution_reason, proof_disposition, evidence_root_id, evidence_root_digest,
		observed_at, recorded_at, source_clock_id, canonical_digest, proof_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, state.ProofID,
		debt.DebtID, debt.RuntimeRunID.String(), debt.ResourceIdentityDigest[:], debt.ResourceGeneration,
		debt.ResourceFence, resolution.Class, resolution.Reason, state.Disposition,
		resolution.EvidenceRoot.EvidenceRootID.String(), resolution.EvidenceRoot.Digest[:],
		postgresTimestamp(verifiedAt), postgresTimestamp(verifiedAt), state.SourceClockID, digest[:], encoded); err != nil {
		t.Fatalf("install retained cleanup resolution proof: %v", err)
	}
}

func TestPostgresPermissionFailureIsNotRetryableTransport(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 11, 50, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-permission-owner", 14)
	start := standardStart(t, now, owner, "postgres-permission")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	installPostgresRuntimeFixture(t, db, schema, acceptedPostgresRuntimeFixture(start, owner, now), now)

	role := schema + "_denied"
	if _, err := db.ExecContext(context.Background(), "CREATE ROLE "+role); err != nil {
		t.Fatal("create restricted PostgreSQL role")
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "RESET ROLE")
		_, _ = db.ExecContext(context.Background(), "DROP ROLE "+role)
	})
	if _, err := db.ExecContext(context.Background(), "GRANT USAGE ON SCHEMA "+schema+" TO "+role); err != nil {
		t.Fatal("grant restricted schema access")
	}
	if _, err := db.ExecContext(context.Background(), "GRANT SELECT ON "+schema+".runtime_execution_runtimes TO "+role); err != nil {
		t.Fatal("grant restricted Runtime access")
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "SET ROLE "+role); err != nil {
		t.Fatal("activate restricted PostgreSQL role")
	}
	_, executeErr := store.Execute(context.Background(), start)
	if _, err := db.ExecContext(context.Background(), "RESET ROLE"); err != nil {
		t.Fatal("reset restricted PostgreSQL role")
	}
	var safeError *Error
	if !errors.As(executeErr, &safeError) || safeError.Code() != ErrorAuthorizationDenied ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("PostgreSQL permission failure = %T %v, want non-retryable authorization denial", executeErr, executeErr)
	}
}

func TestPostgresOutboxAcknowledgementCannotRewriteAuthorityBinding(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-outbox-owner", 15)
	start := standardStart(t, now, owner, "postgres-outbox")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-outbox"), owner, ReconciliationTransportAmbiguous,
	)
	committed, err := store.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("commit reconciliation outbox: %v", err)
	}
	before, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil {
		t.Fatalf("inspect before acknowledgement: %v", err)
	}
	payloadDigest := digestBytes(intent.canonical)
	ack := outboxAcknowledgement{
		OperationID: intent.OperationID, DecisionID: committed.Fact.DecisionID,
		CanonicalRequestDigest: intent.CanonicalDigest, PayloadDigest: payloadDigest, AckDigest: digest(91),
	}
	first, err := store.acknowledgeOutbox(context.Background(), ack)
	if err != nil || first.Disposition != OutboxAcknowledged || first.DeliveryCount != 1 {
		t.Fatalf("acknowledge outbox: view=%+v err=%v", first, err)
	}
	duplicate, err := store.acknowledgeOutbox(context.Background(), ack)
	if err != nil || duplicate != first {
		t.Fatalf("duplicate exact acknowledgement = %+v err=%v, want %+v", duplicate, err, first)
	}
	conflict := ack
	conflict.AckDigest = digest(92)
	_, err = store.acknowledgeOutbox(context.Background(), conflict)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting acknowledgement error = %T %v, want non-retryable integrity conflict", err, err)
	}
	after, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || after != before {
		t.Fatalf("acknowledgement rewrote Runtime authority: before=%#v after=%#v err=%v", before, after, err)
	}
	if first.OperationID != intent.OperationID || first.DecisionID != committed.Fact.DecisionID ||
		first.CanonicalRequestDigest != intent.CanonicalDigest || first.PayloadDigest != payloadDigest {
		t.Fatalf("acknowledgement changed immutable outbox binding: %+v", first)
	}
}

func TestPostgresProjectionFailureDoesNotRollbackCommittedAuthority(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-projection-owner", 16)
	start := standardStart(t, now, owner, "postgres-projection")
	observedCommitted := false
	var observedFact ProjectionFact
	projection := ProjectionDeliveryFunc(func(ctx context.Context, fact ProjectionFact) error {
		observedFact = fact
		var decisions int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+schema+`.runtime_execution_decisions
			WHERE decision_id=$1`, fact.DecisionID.String()).Scan(&decisions); err == nil && decisions == 1 {
			observedCommitted = true
		}
		return errors.New("postgres://projection-credential-canary/private/runtime/path")
	})
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, ProjectionDelivery: projection,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-projection"), owner, ReconciliationProjectionDelivery,
	)
	committed, err := store.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("projection failure rolled back or escaped committed Decision: %v", err)
	}
	if !observedCommitted {
		t.Fatal("projection delivery ran before the authoritative transaction committed")
	}
	if observedFact.AuditFactID == "" || observedFact.AuditCanonicalDigest == (Digest{}) ||
		observedFact.ProjectionSchemaVersion != SchemaV1 || observedFact.RuntimeRevision != committed.Snapshot.RuntimeRevision {
		t.Fatalf("projection omitted authoritative AuditFact identity: %+v", observedFact)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1}) {
		t.Fatalf("projection failure changed authoritative family counts: %+v", counts)
	}
	var auditStatus, telemetryStatus ProjectionDeliveryStatus
	var attempts uint64
	var safeFailure ProjectionSafeFailure
	var degraded bool
	if err := db.QueryRowContext(context.Background(), `SELECT audit_delivery_status,
		telemetry_delivery_status, attempt_count, last_safe_failure, degraded
		FROM `+schema+`.runtime_execution_projection_backlog WHERE fact_id=$1`,
		committed.Fact.DecisionID.String()).Scan(&auditStatus, &telemetryStatus, &attempts, &safeFailure, &degraded); err != nil {
		t.Fatal("inspect durable projection backlog")
	}
	if auditStatus != ProjectionFailed || telemetryStatus != ProjectionFailed || attempts != 1 ||
		safeFailure != ProjectionFailureUnavailable || !degraded {
		t.Fatalf("projection failure backlog = audit:%v telemetry:%v attempts:%d failure:%v degraded:%v",
			auditStatus, telemetryStatus, attempts, safeFailure, degraded)
	}
}

func TestPostgresHeartbeatCompactionPreservesCurrentAuthorityFacts(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-retention-owner", 17)
	start := standardStart(t, now, owner, "postgres-retention")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	fixture.RuntimeRevision = 9
	fixture.State = RuntimeTerminal
	fixture.Outcome = RuntimeFailed
	fixture.Lease = RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, LeaseID: SandboxLeaseID{value: "lease-retention"}, Generation: 3, Fence: 7,
	}
	fixture.EvidenceRoot = EvidenceRootSnapshot{
		SchemaVersion: SchemaV1, EvidenceRootID: EvidenceRootID{value: "evidence-root-retention"}, Digest: digest(70),
	}
	fixture.Capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionNone,
		Physical:       PhysicalCapacityUnknownOrQuarantined,
	}
	fixture.Reconciliation = ReconciliationRequiredStatus
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	original := RuntimeDecisionFact{
		DecisionID:  RuntimeDecisionID{value: "runtime-decision-retention"},
		Disposition: DecisionAccepted, OperationID: start.OperationID,
		CanonicalRequestDigest:   start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	installPostgresAcceptedStartFacts(t, db, schema, start, original, now)
	installPostgresRetentionFacts(t, db, schema, fixture, original.DecisionID, now)
	if _, err := store.createCleanupDebt(context.Background(), cleanupDebtCreation{
		MutationID: "cleanup-mutation-retention", DebtID: "cleanup-debt-retention",
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
		ResourceClass: cleanupResourceContainment, ResourceIdentityDigest: digest(71),
		ResourceGeneration: 4, ResourceFence: 8, Intent: cleanupIntentContain,
		CauseDecisionID: original.DecisionID, CauseOperationID: original.OperationID,
		RetentionFactDigest: digest(72), EligibilityFactDigest: digest(73),
		CreatedAt: now, EligibleAt: now, Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		Blockers:    cleanupBlockerSummary{Classes: cleanupBlockerQuarantine, Digest: digest(74)},
		Uncontained: true,
	}); err != nil {
		t.Fatalf("persist retained Cleanup Debt authority: %v", err)
	}

	request := heartbeatCompactionRequest{
		RuntimeRunID: fixture.RuntimeRunID, LeaseID: fixture.Lease.LeaseID,
		LeaseGeneration: fixture.Lease.Generation, LeaseFence: fixture.Lease.Fence,
		Reason: HeartbeatTerminalHistory, EvidenceRoot: fixture.EvidenceRoot,
	}
	first, err := store.compactTerminalHeartbeatHistory(context.Background(), request)
	if err != nil {
		t.Fatalf("compact terminal heartbeat history: %v", err)
	}
	if first.CompactedCount != 2 || first.PreservedConflictCount != 1 ||
		first.PreservedUncontainedCount != 1 || first.PreservedReconciliationCount != 1 ||
		first.OpenCleanupDebtCount != 1 {
		t.Fatalf("heartbeat compaction view = %+v", first)
	}
	second, err := store.compactTerminalHeartbeatHistory(context.Background(), request)
	if err != nil || second != first {
		t.Fatalf("repeat compaction = %+v err=%v, want idempotent %+v", second, err, first)
	}

	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil {
		t.Fatalf("inspect after heartbeat compaction: %v", err)
	}
	if snapshot.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined ||
		snapshot.Reconciliation != ReconciliationRequiredStatus || snapshot.Lease != fixture.Lease {
		t.Fatalf("compaction released capacity, closed reconciliation, or changed current lease: %#v", snapshot)
	}
	var currentLease, leaseConflict, uncontained, unresolvedReconciliation, openDebt, rawHeartbeat, summaries int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_roots WHERE current_fact),
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_roots WHERE conflict),
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_roots WHERE uncontained),
		(SELECT count(*) FROM `+schema+`.runtime_execution_reconciliation_obligations WHERE unresolved),
		(SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_obligations WHERE unresolved),
		(SELECT count(*) FROM `+schema+`.runtime_execution_heartbeat_history),
		(SELECT count(*) FROM `+schema+`.runtime_execution_heartbeat_compaction)`).Scan(
		&currentLease, &leaseConflict, &uncontained, &unresolvedReconciliation, &openDebt, &rawHeartbeat, &summaries,
	); err != nil {
		t.Fatal("inspect retained heartbeat authority")
	}
	if currentLease != 1 || leaseConflict != 1 || uncontained != 1 || unresolvedReconciliation != 1 ||
		openDebt != 1 || rawHeartbeat != 3 || summaries != 1 {
		t.Fatalf("compaction lost protected facts: lease=%d conflict=%d uncontained=%d reconciliation=%d debt=%d raw=%d summaries=%d",
			currentLease, leaseConflict, uncontained, unresolvedReconciliation, openDebt, rawHeartbeat, summaries)
	}
}

func TestPostgresCommitResponseLossReplaysOriginalDecisionAfterRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 15, 0, 0, 123456789, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-response-loss-owner", 18)
	start := standardStart(t, now, owner, "postgres-response-loss")
	faults := &PersistenceFaultController{}
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: faults,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-response-loss"), owner, ReconciliationTransportAmbiguous,
	)
	if err := faults.FailNextAt(PersistenceFaultBeforeResponse); err != nil {
		t.Fatalf("configure response-loss fault: %v", err)
	}
	_, err = store.persistReconciliationFoundation(context.Background(), intent)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorReconciliationRequired {
		t.Fatalf("response-loss error = %T %v, want reconciliation required", err, err)
	}

	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	replayed, err := restarted.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("replay after response loss: %v", err)
	}
	if replayed.Fact.DecisionID.String() != "runtime-decision-postgres-00000000000000000001" ||
		replayed.Fact.OperationID != intent.OperationID || replayed.Fact.CanonicalRequestDigest != intent.CanonicalDigest ||
		replayed.Snapshot.RuntimeRevision != fixture.RuntimeRevision+1 || replayed.Snapshot.State != RuntimeReconciling ||
		replayed.Snapshot.Operation != fixture.Operation {
		t.Fatalf("response-loss replay allocated or rewrote authority facts: %#v", replayed)
	}
	replayedAgain, err := restarted.persistReconciliationFoundation(context.Background(), intent)
	if err != nil || replayedAgain != replayed {
		t.Fatalf("second exact replay = %#v err=%v, want %#v", replayedAgain, err, replayed)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1}) {
		t.Fatalf("response-loss retry duplicated authoritative facts: %+v", counts)
	}
}

func TestPostgresConcurrentWritersProduceOneRuntimeRevisionWinner(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-concurrent-owner", 19)
	start := standardStart(t, now, owner, "postgres-concurrent")
	first := newMigratedPostgresAuthority(t, db, schema, now)
	second, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("new second PostgreSQL writer: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intents := []reconciliationFoundationIntent{
		newReconciliationFoundationIntent(fixture, mustOperationID(t, "reconcile-concurrent-a"), owner, ReconciliationTransportAmbiguous),
		newReconciliationFoundationIntent(fixture, mustOperationID(t, "reconcile-concurrent-b"), owner, ReconciliationProjectionDelivery),
	}
	stores := []*PostgresAuthority{first, second}
	startBarrier := make(chan struct{})
	type result struct {
		decision RuntimeDecision
		err      error
	}
	results := make(chan result, len(stores))
	var writers sync.WaitGroup
	for index := range stores {
		writers.Add(1)
		go func(store *PostgresAuthority, intent reconciliationFoundationIntent) {
			defer writers.Done()
			<-startBarrier
			decision, executeErr := store.persistReconciliationFoundation(context.Background(), intent)
			results <- result{decision: decision, err: executeErr}
		}(stores[index], intents[index])
	}
	close(startBarrier)
	writers.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		if result.err == nil {
			winners++
			if result.decision.Snapshot.RuntimeRevision != fixture.RuntimeRevision+1 {
				t.Fatalf("winner revision = %d", result.decision.Snapshot.RuntimeRevision)
			}
			continue
		}
		var safeError *Error
		if errors.As(result.err, &safeError) && safeError.Code() == ErrorIntegrityConflict {
			conflicts++
			continue
		}
		t.Fatalf("concurrent writer returned unsafe error: %T %v", result.err, result.err)
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent writers: winners=%d conflicts=%d", winners, conflicts)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1}) {
		t.Fatalf("concurrent writers left split authority: %+v", counts)
	}
}

func TestPostgresPreCommitFaultsLeaveNoAuthoritativePartialState(t *testing.T) {
	faultPoints := []PersistenceFaultPoint{
		PersistenceFaultBeforeRuntimeWrite,
		PersistenceFaultBeforeDecision,
		PersistenceFaultAfterMandatoryAudit,
		PersistenceFaultBeforeOutbox,
		PersistenceFaultBeforeCommit,
	}
	for _, faultPoint := range faultPoints {
		t.Run(faultPoint.String(), func(t *testing.T) {
			db, schema := testpostgres.Open(t, "runtime_execution_test")
			now := time.Date(2026, 7, 28, 17, 0, 0, 0, time.UTC)
			owner := mustTaskOrchestrationAuthority(t, "postgres-precommit-owner", 20)
			start := standardStart(t, now, owner, "postgres-precommit-"+faultPoint.String())
			faults := &PersistenceFaultController{}
			store, err := NewPostgresAuthority(db, PostgresConfig{
				Schema: schema, Now: func() time.Time { return now }, Faults: faults,
			})
			if err != nil {
				t.Fatalf("new PostgreSQL authority: %v", err)
			}
			if err := store.Migrate(context.Background()); err != nil {
				t.Fatalf("prepare PostgreSQL authority schema: %v", err)
			}
			fixture := acceptedPostgresRuntimeFixture(start, owner, now)
			installPostgresRuntimeFixture(t, db, schema, fixture, now)
			intent := newReconciliationFoundationIntent(
				fixture, mustOperationID(t, "reconcile-precommit-"+faultPoint.String()), owner,
				ReconciliationTransportAmbiguous,
			)
			if err := faults.FailNextAt(faultPoint); err != nil {
				t.Fatalf("configure pre-commit fault: %v", err)
			}
			_, err = store.persistReconciliationFoundation(context.Background(), intent)
			var safeError *Error
			if !errors.As(err, &safeError) || safeError.Code() != ErrorDependencyUnavailable {
				t.Fatalf("pre-commit fault error = %T %v, want safe unavailable", err, err)
			}
			counts := postgresFoundationCounts(t, db, schema)
			if counts != (foundationCounts{}) {
				t.Fatalf("pre-commit fault left partial facts: %+v", counts)
			}
			snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
				SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
				PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
			})
			if err != nil || snapshot.RuntimeRevision != fixture.RuntimeRevision ||
				snapshot.State != RuntimeWaitingForLease || snapshot.Reconciliation != ReconciliationStable {
				t.Fatalf("pre-commit fault changed Runtime authority: %#v err=%v", snapshot, err)
			}
		})
	}
}

func TestPostgresFoundationDoesNotCreateAcceptedBoundHalfState(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-no-half-state-owner", 21)
	start := standardStart(t, now, owner, "postgres-no-half-state")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: owner,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision,
		OperationGeneration: start.ExpectedOperationGeneration, RuntimeFence: start.ExpectedRuntimeFence,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeCreated,
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	_, err := store.Execute(context.Background(), start)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorDependencyUnavailable ||
		safeError.RetryDisposition() != RetryAfterDependency {
		t.Fatalf("fresh Start before #75 = %T %v, want dependency-gated safe error", err, err)
	}
	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	if err != nil || snapshot.State != RuntimeCreated || snapshot.Operation.Status != OperationUnbound ||
		snapshot.Lease.AcquireStatus != LeaseNotRequested || snapshot.RuntimeRevision != fixture.RuntimeRevision {
		t.Fatalf("#74 created a half Accepted/Bound state: %#v err=%v", snapshot, err)
	}
	if counts := postgresFoundationCounts(t, db, schema); counts != (foundationCounts{}) {
		t.Fatalf("fresh Start created #75 authority facts: %+v", counts)
	}
}

func TestPostgresWritersForDifferentRuntimesUseRowLevelConcurrency(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 19, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-row-lock-owner", 22)
	firstStart := standardStart(t, now, owner, "postgres-row-lock-a")
	secondStart := standardStart(t, now, owner, "postgres-row-lock-b")
	firstFixture := acceptedPostgresRuntimeFixture(firstStart, owner, now)
	secondFixture := acceptedPostgresRuntimeFixture(secondStart, owner, now)
	blocker := &blockingPersistenceFault{
		point: PersistenceFaultBeforeDecision, entered: make(chan struct{}), release: make(chan struct{}),
	}
	first, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: blocker,
	})
	if err != nil {
		t.Fatalf("new first row-lock writer: %v", err)
	}
	if err := first.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	second, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("new second row-lock writer: %v", err)
	}
	installPostgresRuntimeFixture(t, db, schema, firstFixture, now)
	installPostgresRuntimeFixture(t, db, schema, secondFixture, now)
	firstIntent := newReconciliationFoundationIntent(
		firstFixture, mustOperationID(t, "reconcile-row-lock-a"), owner, ReconciliationTransportAmbiguous,
	)
	secondIntent := newReconciliationFoundationIntent(
		secondFixture, mustOperationID(t, "reconcile-row-lock-b"), owner, ReconciliationTransportAmbiguous,
	)
	type executeResult struct {
		decision RuntimeDecision
		err      error
	}
	firstResult := make(chan executeResult, 1)
	go func() {
		decision, executeErr := first.persistReconciliationFoundation(context.Background(), firstIntent)
		firstResult <- executeResult{decision: decision, err: executeErr}
	}()
	<-blocker.entered
	secondResult := make(chan executeResult, 1)
	go func() {
		decision, executeErr := second.persistReconciliationFoundation(context.Background(), secondIntent)
		secondResult <- executeResult{decision: decision, err: executeErr}
	}()
	select {
	case result := <-secondResult:
		if result.err != nil || result.decision.Snapshot.RuntimeRunID != secondFixture.RuntimeRunID {
			t.Fatalf("different Runtime writer failed behind unrelated row lock: decision=%#v err=%v", result.decision, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("different Runtime writer was blocked by a non-row-level authority lock")
	}
	close(blocker.release)
	result := <-firstResult
	if result.err != nil || result.decision.Snapshot.RuntimeRunID != firstFixture.RuntimeRunID {
		t.Fatalf("first row-lock writer: decision=%#v err=%v", result.decision, result.err)
	}
	counts := postgresFoundationCounts(t, db, schema)
	if counts != (foundationCounts{Decisions: 2, Requests: 2, Revisions: 2, AuditFacts: 2, Outbox: 2, Reconciliation: 2, Projection: 2}) {
		t.Fatalf("row-level writers left incomplete facts: %+v", counts)
	}
}

func TestPostgresCorruptPersistenceFailsClosedWithoutPrivateDetail(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-corrupt-owner", 23)
	start := standardStart(t, now, owner, "postgres-corrupt")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: owner,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision,
		OperationGeneration: start.ExpectedOperationGeneration, RuntimeFence: start.ExpectedRuntimeFence,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeCreated,
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	canaries := []string{
		"postgres://authority:credential@database/runtime",
		"/private/runtime/path",
		"SELECT secret FROM " + schema + ".runtime_execution_runtimes",
	}
	corrupt := []byte(`{"unknown_required_field":"` + canaries[0] + `","path":"` + canaries[1] + `"}`)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_runtimes
		SET aggregate_state=$1 WHERE runtime_run_id=$2`, corrupt, fixture.RuntimeRunID.String()); err != nil {
		t.Fatal("install corrupt PostgreSQL state")
	}
	_, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: owner,
	})
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever || safeError.ReconciliationDisposition() != ReconciliationNotRequired {
		t.Fatalf("corrupt PostgreSQL state error = %T %v, want closed non-retryable integrity error", err, err)
	}
	for _, canary := range canaries {
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("safe persistence error leaked %q", canary)
		}
	}
	if strings.Contains(err.Error(), schema) || strings.Contains(err.Error(), "runtime_execution_runtimes") {
		t.Fatal("safe persistence error leaked schema or table detail")
	}
}

func TestPostgresCorruptMandatoryAuditFailsClosedOnReplay(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 20, 30, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-corrupt-audit-owner", 23)
	start := standardStart(t, now, owner, "postgres-corrupt-audit")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := RuntimeDecisionFact{
		DecisionID:  RuntimeDecisionID{value: "runtime-decision-postgres-corrupt-audit"},
		Disposition: DecisionAccepted, OperationID: start.OperationID,
		CanonicalRequestDigest:   start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)

	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_immutable_mutation ON `+schema+
		`.runtime_execution_mandatory_audit`); err != nil {
		t.Fatal("disable immutable trigger for corruption fixture")
	}
	corruptDigest := digest(99)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_mandatory_audit
		SET canonical_digest=$1 WHERE decision_id=$2`, corruptDigest[:], fact.DecisionID.String()); err != nil {
		t.Fatal("install corrupt mandatory-audit fixture")
	}
	_, loadErr := store.loadMandatoryAudit(context.Background(), postgresMandatoryAuditRef{
		DecisionID: fact.DecisionID, PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
		RequestDigest: start.CanonicalRequestDigest, Authority: owner,
	})
	var loadSafeError *Error
	if !errors.As(loadErr, &loadSafeError) || loadSafeError.Code() != ErrorIntegrityConflict ||
		loadSafeError.RetryDisposition() != RetryNever {
		t.Fatalf("corrupt mandatory-audit loader error = %T %v, want non-retryable integrity conflict", loadErr, loadErr)
	}

	_, err := store.Execute(context.Background(), start)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("corrupt mandatory-audit replay error = %T %v, want non-retryable integrity conflict", err, err)
	}
	if strings.Contains(err.Error(), schema) || strings.Contains(err.Error(), "runtime_execution_mandatory_audit") {
		t.Fatal("corrupt mandatory-audit error leaked schema or table detail")
	}
}

func TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 21, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-outbox-conflict-owner", 24)
	start := standardStart(t, now, owner, "postgres-outbox-conflict")
	store := newMigratedPostgresAuthority(t, db, schema, now)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	operationID := mustOperationID(t, "reconcile-outbox-conflict")
	originalIntent := newReconciliationFoundationIntent(
		fixture, operationID, owner, ReconciliationTransportAmbiguous,
	)
	original, err := store.persistReconciliationFoundation(context.Background(), originalIntent)
	if err != nil {
		t.Fatalf("commit original outbox: %v", err)
	}
	want := readPostgresOutboxBinding(t, db, schema, operationID)

	exact, err := store.persistReconciliationFoundation(context.Background(), originalIntent)
	if err != nil || exact != original {
		t.Fatalf("exact duplicate = %#v err=%v, want %#v", exact, err, original)
	}
	conflictingIntent := newReconciliationFoundationIntent(
		fixture, operationID, owner, ReconciliationProjectionDelivery,
	)
	_, err = store.persistReconciliationFoundation(context.Background(), conflictingIntent)
	var safeError *Error
	if !errors.As(err, &safeError) || safeError.Code() != ErrorIntegrityConflict ||
		safeError.RetryDisposition() != RetryNever {
		t.Fatalf("conflicting outbox binding error = %T %v, want non-retryable integrity conflict", err, err)
	}
	got := readPostgresOutboxBinding(t, db, schema, operationID)
	if got.OperationID != want.OperationID || got.DecisionID != want.DecisionID || got.RuntimeRunID != want.RuntimeRunID ||
		!bytes.Equal(got.CanonicalDigest, want.CanonicalDigest) || !bytes.Equal(got.ScopeDigest, want.ScopeDigest) ||
		!bytes.Equal(got.Payload, want.Payload) || !bytes.Equal(got.PayloadDigest, want.PayloadDigest) {
		t.Fatalf("conflicting duplicate rewrote immutable outbox: before=%+v after=%+v", want, got)
	}
	if counts := postgresFoundationCounts(t, db, schema); counts != (foundationCounts{
		Decisions: 1, Requests: 1, Revisions: 1, AuditFacts: 1, Outbox: 1, Reconciliation: 1, Projection: 1,
	}) {
		t.Fatalf("duplicate/conflict created additional facts: %+v", counts)
	}
}

type postgresOutboxBinding struct {
	OperationID     string
	DecisionID      string
	RuntimeRunID    string
	CanonicalDigest []byte
	ScopeDigest     []byte
	Payload         []byte
	PayloadDigest   []byte
}

func readPostgresOutboxBinding(
	t *testing.T,
	db *sql.DB,
	schema string,
	operationID OperationID,
) postgresOutboxBinding {
	t.Helper()
	var binding postgresOutboxBinding
	if err := db.QueryRowContext(context.Background(), `SELECT operation_id, decision_id, runtime_run_id,
		canonical_request_digest, authority_scope_digest, payload, payload_digest
		FROM `+schema+`.runtime_execution_outbox WHERE operation_id=$1`, operationID.String()).Scan(
		&binding.OperationID, &binding.DecisionID, &binding.RuntimeRunID, &binding.CanonicalDigest,
		&binding.ScopeDigest, &binding.Payload, &binding.PayloadDigest,
	); err != nil {
		t.Fatal("inspect immutable PostgreSQL outbox binding")
	}
	return binding
}

type blockingPersistenceFault struct {
	point   PersistenceFaultPoint
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (fault *blockingPersistenceFault) FailAt(point PersistenceFaultPoint) bool {
	if point != fault.point {
		return false
	}
	fault.once.Do(func() {
		close(fault.entered)
		<-fault.release
	})
	return false
}

func TestPostgresAuthorityReconstructsRuntimeSnapshotAfterRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postgres-owner", 11)
	fixture := RuntimeFixture{
		PersonalWorkspaceID: mustPersonalWorkspaceID(t, "postgres-workspace"),
		TaskID:              mustTaskID(t, "postgres-task"),
		PhaseRunID:          mustPhaseRunID(t, "postgres-phase"),
		RuntimeRunID:        mustRuntimeRunID(t, "postgres-runtime"),
		Owner:               authority,
		TaskRevision:        7,
		RuntimeRevision:     3,
		OperationGeneration: 5,
		RuntimeFence:        9,
		SafetyEpoch:         13,
		State:               RuntimeCreated,
		Deadline:            now.Add(30 * time.Minute),
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld,
			NoLease:        NoLeaseDispositionNone,
			Physical:       PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}

	authorityStore := newMigratedPostgresAuthority(t, db, schema, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	ref := RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: authority,
	}
	want, err := authorityStore.Inspect(context.Background(), ref)
	if err != nil {
		t.Fatalf("inspect seeded Runtime: %v", err)
	}

	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart PostgreSQL authority: %v", err)
	}
	got, err := restarted.Inspect(context.Background(), ref)
	if err != nil {
		t.Fatalf("inspect after restart: %v", err)
	}
	if got != want || got.RuntimeRunID != fixture.RuntimeRunID || got.RuntimeRevision != fixture.RuntimeRevision ||
		got.RuntimeFence != fixture.RuntimeFence || got.State != RuntimeCreated {
		t.Fatalf("restart reconstructed %#v, want %#v", got, want)
	}
}

func newMigratedPostgresAuthority(
	t *testing.T,
	db *sql.DB,
	schema string,
	now time.Time,
) *PostgresAuthority {
	t.Helper()
	authority, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := authority.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	return authority
}

func retainedAcceptedStartFact(start StartRuntimeRun, decisionID string) RuntimeDecisionFact {
	return RuntimeDecisionFact{
		DecisionID: RuntimeDecisionID{value: decisionID}, Disposition: DecisionAccepted,
		OperationID: start.OperationID, CanonicalRequestDigest: start.CanonicalRequestDigest,
		PreviousRuntimeRevision:  start.ExpectedRuntimeRevision,
		ResultingRuntimeRevision: start.ExpectedRuntimeRevision + 1,
		StateAtDecision:          RuntimeWaitingForLease, OutcomeAtDecision: RuntimeOutcomeNone,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
}

func installPostgresRuntimeFixture(
	t *testing.T,
	db *sql.DB,
	schema string,
	fixture RuntimeFixture,
	now time.Time,
) {
	t.Helper()
	state, err := encodePostgresRuntimeFixture(fixture)
	if err != nil {
		t.Fatalf("encode PostgreSQL Runtime fixture: %v", err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_runtimes (
		runtime_run_id, personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id,
		aggregate_state, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		fixture.RuntimeRunID.String(), fixture.PersonalWorkspaceID.String(), fixture.TaskID.String(), fixture.PhaseRunID.String(),
		fixture.Owner.id.String(), fixture.Owner.generation, fixture.Owner.kind,
		fixture.TaskRevision, fixture.RuntimeRevision, fixture.OperationGeneration, fixture.RuntimeFence,
		fixture.SafetyEpoch, fixture.State, fixture.Outcome, fixture.TerminalEvidenceID.String(), state, now.UTC())
	if err != nil {
		t.Fatal("install PostgreSQL Runtime fixture")
	}
}

func acceptedPostgresRuntimeFixture(start StartRuntimeRun, owner RuntimeAuthority, now time.Time) RuntimeFixture {
	return RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: owner,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision + 1,
		OperationGeneration: start.ExpectedOperationGeneration + 1, RuntimeFence: start.ExpectedRuntimeFence + 1,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeWaitingForLease,
		Operation: RuntimeOperationBinding{
			Status: OperationBound, OperationID: start.OperationID, Digest: start.CanonicalRequestDigest,
			Generation: start.ExpectedOperationGeneration + 1, AdmissionGrantID: start.AdmissionGrant.AdmissionGrantID,
			GrantGeneration: start.AdmissionGrant.Generation,
		},
		Lease:    RuntimeLeaseSnapshot{AcquireStatus: LeaseAcquirePending},
		Deadline: start.Deadline, LeaseAcquireBy: now.Add(15 * time.Minute),
		Capacity: RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
		},
		Reconciliation: ReconciliationStable,
	}
}

func installPostgresAcceptedStartFacts(
	t *testing.T,
	db *sql.DB,
	schema string,
	start StartRuntimeRun,
	fact RuntimeDecisionFact,
	now time.Time,
) {
	t.Helper()
	decisionState, err := encodePostgresDecisionFact(fact)
	if err != nil {
		t.Fatalf("encode retained Decision: %v", err)
	}
	canonicalPayload, err := canonicalStartEncoding(start)
	if err != nil {
		t.Fatalf("encode retained canonical Start: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("begin retained Decision fixture")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_decisions (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		fact.DecisionID.String(), start.RuntimeRunID.String(), start.OperationID.String(), start.CanonicalRequestDigest[:],
		fact.PreviousRuntimeRevision, fact.ResultingRuntimeRevision, decisionState, now.UTC()); err != nil {
		t.Fatal("install retained Decision fixture")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_requests (
		personal_workspace_id, runtime_run_id, command_kind, operation_id,
		canonical_request_digest, canonical_request, decision_id,
		admission_grant_id, admission_work_item_id, admission_grant_generation
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		start.PersonalWorkspaceID.String(), start.RuntimeRunID.String(), CommandStartRuntimeRun, start.OperationID.String(),
		start.CanonicalRequestDigest[:], canonicalPayload, fact.DecisionID.String(),
		start.AdmissionGrant.AdmissionGrantID.String(), start.AdmissionGrant.WorkItemID.String(),
		start.AdmissionGrant.Generation); err != nil {
		t.Fatal("install retained request fixture")
	}
	auditState := newPostgresMandatoryAuditState(postgresMandatoryAuditInput{
		AuditFactID: "runtime-audit-postgres-replay", Action: postgresAuditStartAccepted,
		Decision: fact, RuntimeRunID: start.RuntimeRunID, RequestDigest: start.CanonicalRequestDigest,
		Authority: start.Authority, BeforeState: RuntimeCreated, AfterState: fact.StateAtDecision,
		BeforeOperationGeneration: start.ExpectedOperationGeneration,
		AfterOperationGeneration:  start.ExpectedOperationGeneration + 1,
		BeforeRuntimeFence:        start.ExpectedRuntimeFence,
		AfterRuntimeFence:         start.ExpectedRuntimeFence + 1,
		BeforeSafetyEpoch:         start.ReleaseSafetyEpoch, AfterSafetyEpoch: start.ReleaseSafetyEpoch,
		OccurredAt: now, RecordedAt: now,
	})
	auditStateBytes, err := auditState.encode()
	if err != nil {
		t.Fatal("encode retained mandatory AuditFact")
	}
	auditDigest, err := auditState.canonicalDigest()
	if err != nil {
		t.Fatal("digest retained mandatory AuditFact")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_mandatory_audit (
		audit_fact_id, decision_id, runtime_run_id, operation_id, request_digest,
		schema_version, integrity_version, owning_module, canonical_digest,
		authority_kind, authority_id, authority_generation, action, result,
		before_revision, after_revision, occurred_at, recorded_at, source_clock_id, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		auditState.AuditFactID, fact.DecisionID.String(), start.RuntimeRunID.String(), start.OperationID.String(),
		start.CanonicalRequestDigest[:], auditState.SchemaVersion, auditState.IntegrityVersion, auditState.OwningModule,
		auditDigest[:], auditState.AuthorityKind, auditState.AuthorityID, auditState.AuthorityGeneration,
		auditState.Action, auditState.Result, auditState.BeforeRevision, auditState.AfterRevision,
		postgresTimestamp(now), postgresTimestamp(now), auditState.SourceClockID, auditStateBytes); err != nil {
		t.Fatal("install retained mandatory audit fixture")
	}
	payloadDigest := digestBytes(canonicalPayload)
	scopeDigest := authorityScopeDigest(start.PersonalWorkspaceID, start.RuntimeRunID)
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_outbox (
		operation_id, decision_id, runtime_run_id, canonical_request_digest,
		authority_scope_digest, payload, payload_digest, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		start.OperationID.String(), fact.DecisionID.String(), start.RuntimeRunID.String(), start.CanonicalRequestDigest[:],
		scopeDigest[:], canonicalPayload, payloadDigest[:], now.UTC()); err != nil {
		t.Fatal("install retained outbox fixture")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_projection_backlog (
		fact_id, audit_fact_id, audit_canonical_digest, fact_revision,
		projection_schema_version, audit_delivery_status, telemetry_delivery_status, degraded
	) VALUES ($1,$2,$3,$4,$5,$6,$6,TRUE)`, fact.DecisionID.String(), auditState.AuditFactID,
		auditDigest[:], fact.ResultingRuntimeRevision, SchemaV1, ProjectionPending); err != nil {
		t.Fatal("install retained projection fixture")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("commit retained Decision fixture")
	}
}

func installPostgresRetentionFacts(
	t *testing.T,
	db *sql.DB,
	schema string,
	fixture RuntimeFixture,
	decisionID RuntimeDecisionID,
	now time.Time,
) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("begin PostgreSQL retention fixtures")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_evidence_roots (
		evidence_root_id, runtime_run_id, schema_version, digest, accepted_at
	) VALUES ($1,$2,$3,$4,$5)`, fixture.EvidenceRoot.EvidenceRootID.String(), fixture.RuntimeRunID.String(),
		fixture.EvidenceRoot.SchemaVersion, fixture.EvidenceRoot.Digest[:], now.UTC()); err != nil {
		t.Fatal("install retained evidence root")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_lease_roots (
		runtime_run_id, lease_id, lease_generation, lease_fence, current_fact,
		conflict, uncontained, evidence_root_id, evidence_root_digest, updated_at
	) VALUES ($1,$2,$3,$4,TRUE,TRUE,TRUE,$5,$6,$7)`, fixture.RuntimeRunID.String(), fixture.Lease.LeaseID.String(),
		fixture.Lease.Generation, fixture.Lease.Fence, fixture.EvidenceRoot.EvidenceRootID.String(),
		fixture.EvidenceRoot.Digest[:], now.UTC()); err != nil {
		t.Fatal("install current lease authority root")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_reconciliation_obligations (
		operation_id, runtime_run_id, decision_id, owner_authority_kind, owner_authority_id,
		owner_authority_generation, runtime_revision, operation_generation, runtime_fence,
		reason, status, result, first_recorded_at, last_recorded_at, observation_count,
		unresolved, next_retry_at, safe_failure_count, stale_evidence_count,
		evidence_root_id, evidence_root_digest
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,1,TRUE,$13,0,0,$14,$15)`,
		"reconcile-retention", fixture.RuntimeRunID.String(), decisionID.String(),
		fixture.Owner.kind, fixture.Owner.id.String(), fixture.Owner.generation,
		fixture.RuntimeRevision, fixture.OperationGeneration, fixture.RuntimeFence,
		ReconciliationTransportAmbiguous, ReconciliationObligationOpen, DecisionAccepted, now.UTC(),
		fixture.EvidenceRoot.EvidenceRootID.String(), fixture.EvidenceRoot.Digest[:]); err != nil {
		t.Fatal("install unresolved reconciliation authority")
	}
	for sequence, flags := range []struct {
		terminal, conflict, uncontained, unresolved bool
	}{
		{terminal: true},
		{terminal: true},
		{conflict: true},
		{uncontained: true},
		{unresolved: true},
	} {
		observedAt := now.Add(time.Duration(sequence) * time.Second)
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_heartbeat_history (
			runtime_run_id, heartbeat_sequence, lease_id, lease_generation, lease_fence,
			observed_at, reason, evidence_root_id, evidence_root_digest,
			terminal_history, conflict, uncontained, unresolved_reconciliation
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, fixture.RuntimeRunID.String(), sequence+1,
			fixture.Lease.LeaseID.String(), fixture.Lease.Generation, fixture.Lease.Fence, observedAt,
			HeartbeatTerminalHistory, fixture.EvidenceRoot.EvidenceRootID.String(), fixture.EvidenceRoot.Digest[:],
			flags.terminal, flags.conflict, flags.uncontained, flags.unresolved); err != nil {
			t.Fatal("install heartbeat retention history")
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("commit PostgreSQL retention fixtures")
	}
}

type foundationCounts struct {
	Decisions      int
	Requests       int
	Revisions      int
	AuditFacts     int
	Outbox         int
	Reconciliation int
	Projection     int
}

func postgresFoundationCounts(t *testing.T, db *sql.DB, schema string) foundationCounts {
	t.Helper()
	var counts foundationCounts
	err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_decisions),
		(SELECT count(*) FROM `+schema+`.runtime_execution_requests),
		(SELECT count(*) FROM `+schema+`.runtime_execution_revisions),
		(SELECT count(*) FROM `+schema+`.runtime_execution_mandatory_audit),
		(SELECT count(*) FROM `+schema+`.runtime_execution_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_reconciliation_obligations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_projection_backlog)`).Scan(
		&counts.Decisions, &counts.Requests, &counts.Revisions, &counts.AuditFacts,
		&counts.Outbox, &counts.Reconciliation, &counts.Projection,
	)
	if err != nil {
		t.Fatal("inspect PostgreSQL foundation fact counts")
	}
	return counts
}
