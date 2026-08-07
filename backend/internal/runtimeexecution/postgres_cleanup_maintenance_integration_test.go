package runtimeexecution

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

func postgresCleanupMaintenanceFixture(
	t *testing.T,
	db *sql.DB,
	schema string,
	now time.Time,
	owner RuntimeAuthority,
	suffix string,
) (*PostgresAuthority, StartRuntimeRun, RuntimeDecisionFact, EvidenceRootSnapshot) {
	t.Helper()
	store := newMigratedPostgresAuthority(t, db, schema, now)
	start := standardStart(t, now, owner, suffix)
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	fixture.EvidenceRoot = EvidenceRootSnapshot{
		SchemaVersion: SchemaV1, EvidenceRootID: EvidenceRootID{value: "cleanup-maintenance-evidence-" + suffix},
		Digest: digest(230),
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-cleanup-maintenance-"+suffix)
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)
	installPostgresCleanupEvidenceRoot(t, db, schema, start.RuntimeRunID, fixture.EvidenceRoot, now)
	return store, start, fact, fixture.EvidenceRoot
}

func TestPostgresCleanupMaintenanceFullLifecycleThroughMaintain(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)
	administrator := mustAdministratorAuthority(t, "postgres-cleanup-maintenance-admin", 24)
	store, start, fact, evidenceRoot := postgresCleanupMaintenanceFixture(t, db, schema, now, administrator, "maintenance")

	runtimeRevision, runtimeFence := start.ExpectedRuntimeRevision+1, start.ExpectedRuntimeFence+1

	// Obligation-before-attempt: a physical attempt cannot be recorded before
	// the durable obligation exists.
	premature, err := NewRecordCleanupAttempt(RecordCleanupAttemptInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "maintenance-attempt-premature"),
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Attempt: cleanupDebtAttempt{
			MutationID: "maintenance-attempt-premature", DebtID: "maintenance-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: administrator, ExpectedRevision: 1, ResourceGeneration: 7, ResourceFence: 9,
			ClaimGeneration: 2, ClaimFence: 11, AttemptedAt: now.Add(time.Minute),
			NextRetryAt: now.Add(6 * time.Minute), RetryDisposition: cleanupRetryScheduled,
			FailureCategory: cleanupFailureUnavailable, LastErrorDigest: digest(231),
			Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		},
	})
	if err != nil {
		t.Fatalf("new premature attempt: %v", err)
	}
	if _, err := store.Maintain(context.Background(), premature); err == nil {
		t.Fatal("attempt before durable obligation was accepted")
	}

	create, err := NewCreateCleanupObligation(CreateCleanupObligationInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "maintenance-obligation"),
		Reason:                  CleanupObligationPostLeaseTerminal,
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Obligation: cleanupDebtCreation{
			MutationID: "maintenance-obligation", DebtID: "maintenance-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: administrator, ResourceClass: cleanupResourceSandbox,
			ResourceIdentityDigest: digest(232), ResourceGeneration: 7, ResourceFence: 9,
			Intent: cleanupIntentReclaim, CauseDecisionID: fact.DecisionID, CauseOperationID: start.OperationID,
			RetentionFactDigest: digest(233), EligibilityFactDigest: digest(234),
			CreatedAt: now.Add(-time.Minute), EligibleAt: now.Add(-time.Minute),
			Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		},
	})
	if err != nil {
		t.Fatalf("new obligation: %v", err)
	}
	created, err := store.Maintain(context.Background(), create)
	if err != nil {
		t.Fatalf("maintain create obligation: %v", err)
	}
	if created.CleanupDebt.DebtID != "maintenance-debt" || created.CleanupDebt.DebtRevision != 1 ||
		created.CleanupDebt.Status != cleanupDebtOpen || !created.CleanupDebt.Unresolved {
		t.Fatalf("created obligation lost authority: %+v", created.CleanupDebt)
	}

	// A retry attempt is recorded on the same DebtID.
	attempt, err := NewRecordCleanupAttempt(RecordCleanupAttemptInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "maintenance-attempt"),
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Attempt: cleanupDebtAttempt{
			MutationID: "maintenance-attempt", DebtID: "maintenance-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: administrator, ExpectedRevision: 1, ResourceGeneration: 7, ResourceFence: 9,
			ClaimGeneration: 3, ClaimFence: 12, AttemptedAt: now.Add(time.Minute),
			NextRetryAt: now.Add(7 * time.Minute), RetryDisposition: cleanupRetryScheduled,
			FailureCategory: cleanupFailureUnavailable, LastErrorDigest: digest(235),
			Estimation: cleanupEstimation{
				State: cleanupEstimateKnown, Method: cleanupEstimateAdapterObservation,
				Bytes: 8192, Inodes: 20, ObservedAt: now.Add(time.Minute),
			},
		},
	})
	if err != nil {
		t.Fatalf("new attempt: %v", err)
	}
	attempted, err := store.Maintain(context.Background(), attempt)
	if err != nil {
		t.Fatalf("maintain attempt: %v", err)
	}
	if attempted.CleanupDebt.DebtID != "maintenance-debt" || attempted.CleanupDebt.DebtRevision != 2 ||
		attempted.CleanupDebt.Status != cleanupDebtRetryScheduled {
		t.Fatalf("attempt lost same DebtID: %+v", attempted.CleanupDebt)
	}

	// Audited AcceptedException cannot report reclaimed capacity.
	resolvedAt := now.Add(2 * time.Minute)
	exceptionUntil := now.Add(30 * time.Minute)
	resolve, err := NewResolveCleanupDebt(ResolveCleanupDebtInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "maintenance-exception"),
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		ApprovalReference: "postgres-exception-approval",
		IncidentReference: "postgres-exception-incident",
		TicketReference:   "postgres-exception-ticket",
		Resolution: cleanupDebtResolution{
			MutationID: "maintenance-exception", DebtID: "maintenance-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: administrator, ExpectedRevision: 2, ResourceGeneration: 7, ResourceFence: 9,
			ResolvedAt: resolvedAt, Class: cleanupResolutionAcceptedException,
			Reason: cleanupResolutionAdministratorException, EvidenceRoot: evidenceRoot,
			ExceptionUntil: exceptionUntil,
		},
	})
	if err != nil {
		t.Fatalf("new exception resolution: %v", err)
	}
	excepted, err := store.Maintain(context.Background(), resolve)
	if err != nil {
		t.Fatalf("maintain exception resolution: %v", err)
	}
	if excepted.CleanupDebt.Status != cleanupDebtResolved || excepted.CleanupDebt.Unresolved ||
		excepted.CleanupDebt.CapacityReleased || excepted.CleanupDebt.ResolutionClass != cleanupResolutionAcceptedException {
		t.Fatalf("AcceptedException must not report capacity reclaimed: %+v", excepted.CleanupDebt)
	}

	// Expiry before the exception duration is rejected.
	prematureExpiry, err := NewExpireCleanupDebtException(ExpireCleanupDebtExceptionInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "maintenance-expire-premature"),
		Reason: CleanupExceptionDurationElapsed, ExpectedRuntimeRevision: runtimeRevision,
		ExpectedRuntimeFence: runtimeFence, DebtID: "maintenance-debt",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: administrator, ExpectedRevision: 3, ResourceGeneration: 7, ResourceFence: 9,
		ExpiredAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("new premature expiry: %v", err)
	}
	if _, err := store.Maintain(context.Background(), prematureExpiry); err == nil {
		t.Fatal("premature exception expiry was accepted")
	}

	// Expiry reopens the same DebtID after the duration; capacity stays held.
	expire, err := NewExpireCleanupDebtException(ExpireCleanupDebtExceptionInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "maintenance-expire"),
		Reason: CleanupExceptionDurationElapsed, ExpectedRuntimeRevision: runtimeRevision,
		ExpectedRuntimeFence: runtimeFence, DebtID: "maintenance-debt",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: administrator, ExpectedRevision: 3, ResourceGeneration: 7, ResourceFence: 9,
		ExpiredAt: exceptionUntil.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("new exception expiry: %v", err)
	}
	expired, err := store.Maintain(context.Background(), expire)
	if err != nil {
		t.Fatalf("maintain exception expiry: %v", err)
	}
	if !expired.CleanupDebt.Expired || !expired.CleanupDebt.Reopened || expired.CleanupDebt.CapacityReleased ||
		expired.CleanupDebt.Status != cleanupDebtOpen || expired.CleanupDebt.DebtID != "maintenance-debt" {
		t.Fatalf("exception expiry did not safely reopen: %+v", expired.CleanupDebt)
	}

	// Safe reopen allows a new retry attempt on the same DebtID.
	reopenAttempt, err := NewRecordCleanupAttempt(RecordCleanupAttemptInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "maintenance-attempt-after-reopen"),
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Attempt: cleanupDebtAttempt{
			MutationID: "maintenance-attempt-after-reopen", DebtID: "maintenance-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: administrator, ExpectedRevision: expired.CleanupDebt.DebtRevision,
			ResourceGeneration: 7, ResourceFence: 9, ClaimGeneration: 4, ClaimFence: 13,
			AttemptedAt: exceptionUntil.Add(2 * time.Minute), NextRetryAt: exceptionUntil.Add(8 * time.Minute),
			RetryDisposition: cleanupRetryScheduled, FailureCategory: cleanupFailureUnavailable,
			LastErrorDigest: digest(236), Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		},
	})
	if err != nil {
		t.Fatalf("new retry after reopen: %v", err)
	}
	afterReopen, err := store.Maintain(context.Background(), reopenAttempt)
	if err != nil {
		t.Fatalf("maintain retry after reopen: %v", err)
	}
	if afterReopen.CleanupDebt.DebtID != "maintenance-debt" ||
		afterReopen.CleanupDebt.DebtRevision != expired.CleanupDebt.DebtRevision+1 {
		t.Fatalf("reopened debt did not retry on same DebtID: %+v", afterReopen.CleanupDebt)
	}

	// Exact replay returns the original decision after restart, even when the
	// caller's expected Runtime revision/fence no longer matches the current
	// record (replay is decided before current-state validation).
	restarted, err := NewPostgresAuthority(db, PostgresConfig{Schema: schema, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("restart authority: %v", err)
	}
	replayed, err := restarted.Maintain(context.Background(), create)
	if err != nil {
		t.Fatalf("exact obligation replay after restart: %v", err)
	}
	if replayed.CanonicalRequestDigest != created.CanonicalRequestDigest ||
		replayed.CleanupDebt.DebtID != "maintenance-debt" || !replayed.CleanupDebt.Replayed {
		t.Fatalf("exact replay after restart = %+v err=%v", replayed, err)
	}
	staleViewReplay := create
	staleViewReplay.ExpectedRuntimeRevision = runtimeRevision + 100
	staleViewReplay.ExpectedRuntimeFence = runtimeFence + 100
	replayedStale, err := restarted.Maintain(context.Background(), staleViewReplay)
	if err != nil || !replayedStale.CleanupDebt.Replayed ||
		replayedStale.CleanupDebt.DebtRevision != replayed.CleanupDebt.DebtRevision {
		t.Fatalf("replay with stale expected revision = %+v err=%v, want retained decision", replayedStale, err)
	}
}

func TestPostgresCleanupMaintenanceMandatoryAuditRollback(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-cleanup-audit-owner", 25)
	store, start, fact, _ := postgresCleanupMaintenanceFixture(t, db, schema, now, owner, "audit-rollback")
	runtimeRevision, runtimeFence := start.ExpectedRuntimeRevision+1, start.ExpectedRuntimeFence+1

	faults := &PersistenceFaultController{}
	faulted, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: faults,
	})
	if err != nil {
		t.Fatalf("new faulted authority: %v", err)
	}
	create, err := NewCreateCleanupObligation(CreateCleanupObligationInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "audit-obligation"),
		Reason:                  CleanupObligationPostLeaseTerminal,
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Obligation: cleanupDebtCreation{
			MutationID: "audit-obligation", DebtID: "audit-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: owner, ResourceClass: cleanupResourceLease,
			ResourceIdentityDigest: digest(240), ResourceGeneration: 3, ResourceFence: 5,
			Intent: cleanupIntentReclaim, CauseDecisionID: fact.DecisionID, CauseOperationID: start.OperationID,
			RetentionFactDigest: digest(241), EligibilityFactDigest: digest(242),
			CreatedAt: now.Add(-time.Minute), EligibleAt: now.Add(-time.Minute),
			Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		},
	})
	if err != nil {
		t.Fatalf("new audit obligation: %v", err)
	}

	// A mandatory-audit failure before commit must roll back the whole
	// obligation creation (no debt row, no mutation journal, no audit row).
	if err := faults.FailNextAt(PersistenceFaultBeforeMandatoryAudit); err != nil {
		t.Fatalf("configure create audit fault: %v", err)
	}
	if _, err := faulted.Maintain(context.Background(), create); err == nil {
		t.Fatal("audit fault did not fail closed")
	}
	assertNoCleanupDebtRows(t, db, schema, "audit-debt")

	// Fresh create succeeds, then a mandatory-audit failure on the attempt
	// must leave the debt revision untouched.
	created, err := store.Maintain(context.Background(), create)
	if err != nil {
		t.Fatalf("create audit debt: %v", err)
	}
	attempt, err := NewRecordCleanupAttempt(RecordCleanupAttemptInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "audit-attempt"),
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Attempt: cleanupDebtAttempt{
			MutationID: "audit-attempt", DebtID: "audit-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: owner, ExpectedRevision: 1, ResourceGeneration: 3, ResourceFence: 5,
			ClaimGeneration: 2, ClaimFence: 6, AttemptedAt: now.Add(time.Minute),
			NextRetryAt: now.Add(5 * time.Minute), RetryDisposition: cleanupRetryScheduled,
			FailureCategory: cleanupFailureUnavailable, LastErrorDigest: digest(243),
			Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		},
	})
	if err != nil {
		t.Fatalf("new audit attempt: %v", err)
	}
	if err := faults.FailNextAt(PersistenceFaultBeforeMandatoryAudit); err != nil {
		t.Fatalf("configure attempt audit fault: %v", err)
	}
	if _, err := faulted.Maintain(context.Background(), attempt); err == nil {
		t.Fatal("attempt audit fault did not fail closed")
	}
	loaded, err := store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: "audit-debt", PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID: start.RuntimeRunID, Authority: owner,
	})
	if err != nil || loaded.Revision != created.CleanupDebt.DebtRevision {
		t.Fatalf("attempt audit rollback changed debt: %+v err=%v", loaded, err)
	}

	// Exception expiry audit failure must leave the resolved exception intact.
	resolvedAt := now.Add(2 * time.Minute)
	exceptionUntil := now.Add(30 * time.Minute)
	// The exception requires an administrator authority; a Task Orchestration
	// owner cannot even construct an AcceptedException resolution.
	resolveInput := ResolveCleanupDebtInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "audit-exception"),
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		ApprovalReference: "audit-exception-approval",
		Resolution: cleanupDebtResolution{
			MutationID: "audit-exception", DebtID: "audit-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: owner, ExpectedRevision: 1, ResourceGeneration: 3, ResourceFence: 5,
			ResolvedAt: resolvedAt, Class: cleanupResolutionAcceptedException,
			Reason: cleanupResolutionAdministratorException,
			EvidenceRoot: EvidenceRootSnapshot{
				SchemaVersion: SchemaV1, EvidenceRootID: EvidenceRootID{value: "cleanup-maintenance-evidence-audit-rollback"},
				Digest: digest(230),
			},
			ExceptionUntil: exceptionUntil,
		},
	}
	if _, err := NewResolveCleanupDebt(resolveInput); err == nil {
		t.Fatal("Task Orchestration owner constructed an AcceptedException resolution")
	}

	expire, err := NewExpireCleanupDebtException(ExpireCleanupDebtExceptionInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "audit-expire"),
		Reason: CleanupExceptionDurationElapsed, ExpectedRuntimeRevision: runtimeRevision,
		ExpectedRuntimeFence: runtimeFence, DebtID: "audit-debt",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: owner, ExpectedRevision: 1, ResourceGeneration: 3, ResourceFence: 5,
		ExpiredAt: now.Add(40 * time.Minute),
	})
	if err != nil {
		t.Fatalf("new audit expire: %v", err)
	}
	// The debt is not a resolved AcceptedException, so expiry is a conflict
	// before any audit work.
	if _, err := store.Maintain(context.Background(), expire); err == nil {
		t.Fatal("expiry of non-exception debt was accepted")
	}
}

func TestPostgresCleanupDebtSameResourceCannotBeDuplicated(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 30, 17, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-cleanup-unique-owner", 26)
	store, start, fact, _ := postgresCleanupMaintenanceFixture(t, db, schema, now, owner, "unique-resource")
	runtimeRevision, runtimeFence := start.ExpectedRuntimeRevision+1, start.ExpectedRuntimeFence+1

	create := func(debtID, mutationID string) CreateCleanupObligation {
		t.Helper()
		command, err := NewCreateCleanupObligation(CreateCleanupObligationInput{
			SchemaVersion:           SchemaV1,
			OperationID:             mustOperationID(t, mutationID),
			Reason:                  CleanupObligationContainmentRequired,
			ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
			Obligation: cleanupDebtCreation{
				MutationID: mutationID, DebtID: debtID,
				PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
				Authority: owner, ResourceClass: cleanupResourceContainment,
				ResourceIdentityDigest: digest(250), ResourceGeneration: 4, ResourceFence: 6,
				Intent: cleanupIntentContain, CauseDecisionID: fact.DecisionID, CauseOperationID: start.OperationID,
				RetentionFactDigest: digest(251), EligibilityFactDigest: digest(252),
				CreatedAt: now.Add(-time.Minute), EligibleAt: now.Add(-time.Minute),
				Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
			},
		})
		if err != nil {
			t.Fatalf("new unique-resource obligation: %v", err)
		}
		return command
	}
	if _, err := store.Maintain(context.Background(), create("unique-debt-1", "unique-mutation-1")); err != nil {
		t.Fatalf("create first unique-resource debt: %v", err)
	}
	// The same opaque resource cannot create a second DebtID in C03 (the
	// "same resource, one module" rule).
	if _, err := store.Maintain(context.Background(), create("unique-debt-2", "unique-mutation-2")); err == nil {
		t.Fatal("duplicate resource DebtID was accepted")
	}
}

func TestPostgresOperationalDiagnosticsReadOnlyNonEnumerating(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-diag-owner", 27)
	administrator := mustAdministratorAuthority(t, "postgres-diag-admin", 28)
	store, start, fact, _ := postgresCleanupMaintenanceFixture(t, db, schema, now, owner, "diag")
	runtimeRevision, runtimeFence := start.ExpectedRuntimeRevision+1, start.ExpectedRuntimeFence+1

	create, err := NewCreateCleanupObligation(CreateCleanupObligationInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "diag-obligation"),
		Reason:                  CleanupObligationResetRequired,
		ExpectedRuntimeRevision: runtimeRevision, ExpectedRuntimeFence: runtimeFence,
		Obligation: cleanupDebtCreation{
			MutationID: "diag-obligation", DebtID: "diag-debt",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: owner, ResourceClass: cleanupResourceReset,
			ResourceIdentityDigest: digest(60), ResourceGeneration: 2, ResourceFence: 4,
			Intent: cleanupIntentReset, CauseDecisionID: fact.DecisionID, CauseOperationID: start.OperationID,
			RetentionFactDigest: digest(61), EligibilityFactDigest: digest(62),
			CreatedAt: now.Add(-time.Minute), EligibleAt: now.Add(-time.Minute),
			Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
			Blockers: cleanupBlockerSummary{
				Classes: cleanupBlockerQuarantine, Digest: digest(63),
			},
		},
	})
	if err != nil {
		t.Fatalf("new diag obligation: %v", err)
	}
	if _, err := store.Maintain(context.Background(), create); err != nil {
		t.Fatalf("create diag debt: %v", err)
	}

	// Non-administrator diagnostics are denied.
	if _, err := store.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: owner,
		Lookup: DiagnosticLookupCleanupDebt, DebtID: "diag-debt",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Bounded: true,
	}); err == nil {
		t.Fatal("non-administrator diagnostics were accepted")
	}
	// Enumerating queries are denied.
	if _, err := store.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: administrator,
		Lookup: DiagnosticLookupCleanupDebt, PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID: start.RuntimeRunID, Bounded: true,
	}); err == nil {
		t.Fatal("enumerating diagnostic query was accepted")
	}
	// Exact administrator query returns a bounded content-free view.
	view, err := store.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: administrator,
		Lookup: DiagnosticLookupCleanupDebt, DebtID: "diag-debt",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Bounded: true,
	})
	if err != nil {
		t.Fatalf("diagnose diag debt: %v", err)
	}
	if view.Debt == nil || view.Debt.DebtID != "diag-debt" || view.Debt.Status != cleanupDebtBlocked ||
		view.Debt.Blockers != cleanupBlockerQuarantine {
		t.Fatalf("diagnostic view lost debt facts: %+v", view.Debt)
	}
	// Diagnostics are read-only: the debt revision is unchanged.
	after, err := store.loadCleanupDebt(context.Background(), cleanupDebtRef{
		DebtID: "diag-debt", PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID: start.RuntimeRunID, Authority: owner,
	})
	if err != nil || after.Revision != 1 {
		t.Fatalf("diagnostics mutated the debt: %+v err=%v", after, err)
	}

	// Execution node diagnostics are non-enumerating: an unknown node does not
	// disclose existence and is denied.
	_, nodeErr := store.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonNodeHealth, Authority: administrator,
		Lookup: DiagnosticLookupExecutionNode, ExecutionNodeID: startNodeID(t, "postgres-diag-node"), Bounded: true,
	})
	var nodeSafeError *Error
	if !errors.As(nodeErr, &nodeSafeError) || nodeSafeError.Code() != ErrorAuthorizationDenied {
		t.Fatalf("unknown node diagnostics error = %T %v, want non-enumerating authorization denial", nodeErr, nodeErr)
	}
}

func assertNoCleanupDebtRows(t *testing.T, db *sql.DB, schema string, debtID string) {
	t.Helper()
	var partial int
	if err := db.QueryRowContext(context.Background(),
		`SELECT (SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_obligations WHERE debt_id=$1) +
		 (SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_mutations WHERE debt_id=$1) +
		 (SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_mutation_audit WHERE debt_id=$1) +
		 (SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_resolution_audit WHERE debt_id=$1)`,
		debtID).Scan(&partial); err != nil {
		t.Fatal(err)
	}
	if partial != 0 {
		t.Fatalf("mandatory-audit rollback left %d partial cleanup rows", partial)
	}
}
