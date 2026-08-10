package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

func mustAdministratorAuthority(t *testing.T, value string, generation AuthorizationGeneration) RuntimeAuthority {
	t.Helper()
	id, err := NewAuthorityID(value)
	if err != nil {
		t.Fatal(err)
	}
	return NewAdministratorAuthority(id, generation)
}

func cleanupContractEvidenceRoot(now time.Time) EvidenceRootSnapshot {
	return EvidenceRootSnapshot{
		SchemaVersion: SchemaV1, EvidenceRootID: EvidenceRootID{value: "cleanup-contract-evidence"},
		Digest: digest(200),
	}
}

func cleanupContractProof(
	debtID string,
	runtimeRunID RuntimeRunID,
	class cleanupResolutionClass,
	reason cleanupResolutionReason,
	disposition cleanupResolutionProofDisposition,
	verifiedAt time.Time,
	mutate func(*cleanupResolutionProofState),
) cleanupResolutionProofState {
	proof := cleanupResolutionProofState{
		ProofID: "cleanup-proof-" + debtID, SchemaVersion: SchemaV1,
		IntegrityVersion: postgresCleanupResolutionProofIntegrityVersion,
		OwningModule:     postgresCleanupOwnerModule,
		DebtID:           debtID, RuntimeRunID: runtimeRunID.String(),
		ResourceClass: cleanupResourceContainment, ResourceIdentityDigest: digest(211).String(),
		ResourceGeneration: 7, ResourceFence: 9,
		ResolutionClass: class, ResolutionReason: reason, Disposition: disposition,
		EvidenceSchemaVersion: SchemaV1, EvidenceRootID: "cleanup-contract-evidence",
		EvidenceRootDigest: digest(200).String(),
		ObservedAt:         formatCleanupTime(verifiedAt), RecordedAt: formatCleanupTime(verifiedAt),
		SourceClockID:   postgresMandatoryAuditSourceClock,
		ReferencesClear: true, ContainmentClear: true,
	}
	if class == cleanupResolutionAlreadyAbsent {
		proof.ExactGenerationAbsent = true
	}
	if mutate != nil {
		mutate(&proof)
	}
	return proof
}

// cleanupContractHarness builds an in-memory harness with an accepted start
// for a Task Orchestration owned Runtime Run.
func cleanupContractHarness(
	t *testing.T,
	now time.Time,
	authority RuntimeAuthority,
	suffix string,
	proofs []cleanupResolutionProofState,
	debts []cleanupDebtRecord,
) (*DeterministicHarness, StartRuntimeRun, RuntimeDecision) {
	t.Helper()
	start := standardStart(t, now, authority, suffix)
	fixture := runtimeFixtureForStart(start, authority)
	fixture.EvidenceRoot = cleanupContractEvidenceRoot(now)
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{fixture},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
		CleanupProofs:   proofs,
		CleanupDebts:    debts,
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
	})
	if err != nil {
		t.Fatalf("new cleanup harness: %v", err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute start: %v", err)
	}
	return harness, start, accepted
}

func cleanupObligationCommand(
	t *testing.T,
	accepted RuntimeDecision,
	start StartRuntimeRun,
	debtID string,
	operationID string,
	blockers cleanupBlockerClass,
	mutate func(*cleanupDebtCreation),
) CreateCleanupObligation {
	t.Helper()
	creation := cleanupDebtCreation{
		MutationID: operationID, DebtID: debtID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority:     start.Authority,
		ResourceClass: cleanupResourceContainment, ResourceIdentityDigest: digest(211),
		ResourceGeneration: 7, ResourceFence: 9, Intent: cleanupIntentContain,
		CauseDecisionID: accepted.Fact.DecisionID, CauseOperationID: start.OperationID,
		RetentionFactDigest: digest(212), EligibilityFactDigest: digest(213),
		CreatedAt: accepted.Snapshot.Deadline.Add(-30 * time.Minute), EligibleAt: accepted.Snapshot.Deadline.Add(-30 * time.Minute),
		Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
	}
	if blockers != 0 {
		creation.Blockers = cleanupBlockerSummary{Classes: blockers, Digest: digest(214)}
	}
	if mutate != nil {
		mutate(&creation)
	}
	command, err := NewCreateCleanupObligation(CreateCleanupObligationInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, operationID),
		Reason:                  CleanupObligationPostLeaseTerminal,
		ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence:    accepted.Snapshot.RuntimeFence,
		Obligation:              creation,
	})
	if err != nil {
		t.Fatalf("new cleanup obligation: %v", err)
	}
	return command
}

func cleanupAttemptCommand(
	t *testing.T,
	accepted RuntimeDecision,
	start StartRuntimeRun,
	debtID string,
	operationID string,
	expectedRevision uint64,
	claimGeneration uint64,
	blockers cleanupBlockerClass,
	mutate func(*cleanupDebtAttempt),
) RecordCleanupAttempt {
	t.Helper()
	attemptedAt := accepted.Snapshot.Deadline.Add(-25 * time.Minute)
	attempt := cleanupDebtAttempt{
		MutationID: operationID, DebtID: debtID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority, ExpectedRevision: expectedRevision,
		ResourceGeneration: 7, ResourceFence: 9, ClaimGeneration: claimGeneration, ClaimFence: claimGeneration + 9,
		AttemptedAt: attemptedAt, NextRetryAt: attemptedAt.Add(5 * time.Minute),
		FailureCategory: cleanupFailureUnavailable, LastErrorDigest: digest(215),
		Estimation: cleanupEstimation{
			State: cleanupEstimateKnown, Method: cleanupEstimateAdapterObservation,
			Bytes: 4096, Inodes: 12, ObservedAt: attemptedAt,
		},
	}
	if blockers != 0 {
		attempt.Blockers = cleanupBlockerSummary{Classes: blockers, Digest: digest(216)}
	}
	if blockers == 0 {
		attempt.RetryDisposition = cleanupRetryScheduled
	} else {
		attempt.RetryDisposition = cleanupRetryBlocked
	}
	command, err := NewRecordCleanupAttempt(RecordCleanupAttemptInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, operationID),
		ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence:    accepted.Snapshot.RuntimeFence,
		Attempt:                 attempt,
	})
	if err != nil {
		t.Fatalf("new cleanup attempt: %v", err)
	}
	return command
}

func cleanupResolutionCommand(
	t *testing.T,
	accepted RuntimeDecision,
	start StartRuntimeRun,
	debtID string,
	operationID string,
	expectedRevision uint64,
	class cleanupResolutionClass,
	reason cleanupResolutionReason,
	resolvedAt time.Time,
	exceptionUntil time.Time,
	mutate func(*cleanupDebtResolution),
) ResolveCleanupDebt {
	t.Helper()
	resolution := cleanupDebtResolution{
		MutationID: operationID, DebtID: debtID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority, ExpectedRevision: expectedRevision,
		ResourceGeneration: 7, ResourceFence: 9, ResolvedAt: resolvedAt,
		Class: class, Reason: reason, EvidenceRoot: cleanupContractEvidenceRoot(resolvedAt),
		ExceptionUntil: exceptionUntil,
	}
	if mutate != nil {
		mutate(&resolution)
	}
	command, err := NewResolveCleanupDebt(ResolveCleanupDebtInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, operationID),
		ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence:    accepted.Snapshot.RuntimeFence,
		Resolution:              resolution,
	})
	if err != nil {
		t.Fatalf("new cleanup resolution: %v", err)
	}
	return command
}

func TestCleanupMaintenanceObligationBeforeAttemptAndRetryLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "cleanup-lifecycle-owner", 17)
	harness, start, accepted := cleanupContractHarness(t, now, authority, "lifecycle", nil, nil)

	// Attempt before any durable obligation is rejected.
	premature, err := NewRecordCleanupAttempt(RecordCleanupAttemptInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "premature-attempt"),
		ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence:    accepted.Snapshot.RuntimeFence,
		Attempt: cleanupDebtAttempt{
			MutationID: "premature-attempt", DebtID: "cleanup-debt-lifecycle",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
			Authority: authority, ExpectedRevision: 1, ResourceGeneration: 7, ResourceFence: 9,
			ClaimGeneration: 1, ClaimFence: 10, AttemptedAt: now, NextRetryAt: now.Add(time.Minute),
			RetryDisposition: cleanupRetryScheduled, FailureCategory: cleanupFailureUnavailable,
			LastErrorDigest: digest(215), Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		},
	})
	if err != nil {
		t.Fatalf("new premature attempt: %v", err)
	}
	_, err = harness.Maintenance.Maintain(context.Background(), premature)
	assertRuntimeLifecycleErrorCode(t, err, ErrorAuthorizationDenied)

	// Durable obligation before any physical attempt.
	create := cleanupObligationCommand(t, accepted, start, "cleanup-debt-lifecycle", "obligation-1",
		cleanupBlockerGracePeriod|cleanupBlockerQuarantine, nil)
	created, err := harness.Maintenance.Maintain(context.Background(), create)
	if err != nil {
		t.Fatalf("create obligation: %v", err)
	}
	if created.CleanupDebt.DebtID != "cleanup-debt-lifecycle" || created.CleanupDebt.DebtRevision != 1 ||
		created.CleanupDebt.Status != cleanupDebtBlocked || !created.CleanupDebt.Unresolved ||
		created.CleanupDebt.RetryDisposition != cleanupRetryBlocked || created.CleanupDebt.CapacityReleased {
		t.Fatalf("created obligation lost authority: %+v", created.CleanupDebt)
	}

	// First blocked attempt records the safe error, estimation, and blockers.
	first := cleanupAttemptCommand(t, accepted, start, "cleanup-debt-lifecycle", "attempt-1",
		1, 2, cleanupBlockerLease|cleanupBlockerIncident, nil)
	firstDecision, err := harness.Maintenance.Maintain(context.Background(), first)
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if firstDecision.CleanupDebt.DebtRevision != 2 || firstDecision.CleanupDebt.Status != cleanupDebtBlocked ||
		firstDecision.CleanupDebt.RetryDisposition != cleanupRetryBlocked {
		t.Fatalf("first attempt did not retain same DebtID and advance: %+v", firstDecision.CleanupDebt)
	}

	// Stale expected revision (drift) is a conflict and does not change the debt.
	stale := cleanupAttemptCommand(t, accepted, start, "cleanup-debt-lifecycle", "attempt-stale",
		1, 3, 0, nil)
	if _, err := harness.Maintenance.Maintain(context.Background(), stale); err == nil {
		t.Fatal("stale expected revision was accepted")
	} else {
		assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	}

	// Claim loss: a retry re-uses the same DebtID with a fresh claim generation.
	second := cleanupAttemptCommand(t, accepted, start, "cleanup-debt-lifecycle", "attempt-2",
		2, 3, 0, nil)
	secondDecision, err := harness.Maintenance.Maintain(context.Background(), second)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if secondDecision.CleanupDebt.DebtID != "cleanup-debt-lifecycle" ||
		secondDecision.CleanupDebt.DebtRevision != 3 || secondDecision.CleanupDebt.Status != cleanupDebtRetryScheduled ||
		secondDecision.CleanupDebt.RetryDisposition != cleanupRetryScheduled {
		t.Fatalf("claim-loss retry lost same DebtID: %+v", secondDecision.CleanupDebt)
	}

	// Exact attempt replay returns the retained decision.
	replayed, err := harness.Maintenance.Maintain(context.Background(), second)
	if err != nil {
		t.Fatalf("exact attempt replay: %v", err)
	}
	if !replayed.CleanupDebt.Replayed || replayed.CleanupDebt.DebtRevision != 3 {
		t.Fatalf("exact attempt replay did not retain decision: %+v", replayed.CleanupDebt)
	}

	// The debt survives inspection through the protected diagnostics seam.
	view, err := harness.Diagnostics.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth,
		Authority: mustAdministratorAuthority(t, "cleanup-diag-admin", 21),
		Lookup:    DiagnosticLookupCleanupDebt, DebtID: "cleanup-debt-lifecycle",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Bounded: true,
	})
	if err != nil {
		t.Fatalf("diagnose cleanup debt: %v", err)
	}
	if view.Debt == nil || view.Debt.DebtID != "cleanup-debt-lifecycle" || view.Debt.Status != cleanupDebtRetryScheduled ||
		view.Debt.AttemptCount != 2 || view.Debt.LastError != cleanupFailureUnavailable {
		t.Fatalf("diagnostic view lost debt authority: %+v", view.Debt)
	}
}

func TestCleanupMaintenanceResolutionsRequireExactEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	resolvedAt := now.Add(2 * time.Minute)

	proofs := []cleanupResolutionProofState{
		cleanupContractProof("cleanup-debt-absent", mustRuntimeRunID(t, "runtime-resolve"), cleanupResolutionAlreadyAbsent,
			cleanupResolutionExactGenerationAbsent, cleanupProofExactGenerationAbsent, resolvedAt, nil),
		cleanupContractProof("cleanup-debt-reclaimed", mustRuntimeRunID(t, "runtime-resolve"), cleanupResolutionReclaimed,
			cleanupResolutionCleanupProven, cleanupProofDeletionOrReset, resolvedAt, func(proof *cleanupResolutionProofState) {
				proof.DeletionOrResetProven = true
				proof.ReferencesClear = true
				proof.ContainmentClear = true
			}),
		cleanupContractProof("cleanup-debt-retained", mustRuntimeRunID(t, "runtime-resolve"), cleanupResolutionRetainedByAuthority,
			cleanupResolutionCurrentAuthorityRetention, cleanupProofRetainedByAuthority, resolvedAt, func(proof *cleanupResolutionProofState) {
				proof.RetainingAuthorityFactRoot = digest(217).String()
			}),
	}
	authority := mustTaskOrchestrationAuthority(t, "cleanup-resolve-owner", 18)
	harness, start, accepted := cleanupContractHarness(t, now, authority, "resolve", proofs, nil)

	// AlreadyAbsent with exact adapter proof.
	absent := cleanupObligationCommand(t, accepted, start, "cleanup-debt-absent", "obligation-absent", 0, nil)
	absentDecision, err := harness.Maintenance.Maintain(context.Background(), absent)
	if err != nil {
		t.Fatalf("create absent debt: %v", err)
	}
	resolveAbsent := cleanupResolutionCommand(t, accepted, start, "cleanup-debt-absent", "resolve-absent",
		absentDecision.CleanupDebt.DebtRevision, cleanupResolutionAlreadyAbsent,
		cleanupResolutionExactGenerationAbsent, resolvedAt, time.Time{}, nil)
	resolvedAbsent, err := harness.Maintenance.Maintain(context.Background(), resolveAbsent)
	if err != nil {
		t.Fatalf("resolve absent debt: %v", err)
	}
	if resolvedAbsent.CleanupDebt.Status != cleanupDebtResolved || resolvedAbsent.CleanupDebt.Unresolved ||
		!resolvedAbsent.CleanupDebt.CapacityReleased {
		t.Fatalf("already-absent resolution did not release capacity: %+v", resolvedAbsent.CleanupDebt)
	}

	// Reclaimed with deletion-or-reset proof.
	reclaimed := cleanupObligationCommand(t, accepted, start, "cleanup-debt-reclaimed", "obligation-reclaimed", 0, nil)
	reclaimedDecision, err := harness.Maintenance.Maintain(context.Background(), reclaimed)
	if err != nil {
		t.Fatalf("create reclaimed debt: %v", err)
	}
	resolveReclaimed := cleanupResolutionCommand(t, accepted, start, "cleanup-debt-reclaimed", "resolve-reclaimed",
		reclaimedDecision.CleanupDebt.DebtRevision, cleanupResolutionReclaimed,
		cleanupResolutionCleanupProven, resolvedAt, time.Time{}, nil)
	resolvedReclaimed, err := harness.Maintenance.Maintain(context.Background(), resolveReclaimed)
	if err != nil {
		t.Fatalf("resolve reclaimed debt: %v", err)
	}
	if resolvedReclaimed.CleanupDebt.Status != cleanupDebtResolved || !resolvedReclaimed.CleanupDebt.CapacityReleased {
		t.Fatalf("reclaimed resolution lost capacity release: %+v", resolvedReclaimed.CleanupDebt)
	}

	// RetainedByAuthority keeps capacity (a reference/lease still exists).
	retained := cleanupObligationCommand(t, accepted, start, "cleanup-debt-retained", "obligation-retained",
		cleanupBlockerReference, nil)
	retainedDecision, err := harness.Maintenance.Maintain(context.Background(), retained)
	if err != nil {
		t.Fatalf("create retained debt: %v", err)
	}
	resolveRetained := cleanupResolutionCommand(t, accepted, start, "cleanup-debt-retained", "resolve-retained",
		retainedDecision.CleanupDebt.DebtRevision, cleanupResolutionRetainedByAuthority,
		cleanupResolutionCurrentAuthorityRetention, resolvedAt, time.Time{}, func(resolution *cleanupDebtResolution) {
			resolution.RemainingBlockers = cleanupBlockerSummary{Classes: cleanupBlockerReference, Digest: digest(218)}
		})
	resolvedRetained, err := harness.Maintenance.Maintain(context.Background(), resolveRetained)
	if err != nil {
		t.Fatalf("resolve retained debt: %v", err)
	}
	if resolvedRetained.CleanupDebt.Status != cleanupDebtResolved || resolvedRetained.CleanupDebt.CapacityReleased {
		t.Fatalf("retained resolution must not release capacity: %+v", resolvedRetained.CleanupDebt)
	}

	// Missing exact proof cannot close a debt.
	unproven := cleanupObligationCommand(t, accepted, start, "cleanup-debt-unproven", "obligation-unproven", 0, nil)
	unprovenDecision, err := harness.Maintenance.Maintain(context.Background(), unproven)
	if err != nil {
		t.Fatalf("create unproven debt: %v", err)
	}
	resolveUnproven := cleanupResolutionCommand(t, accepted, start, "cleanup-debt-unproven", "resolve-unproven",
		unprovenDecision.CleanupDebt.DebtRevision, cleanupResolutionAlreadyAbsent,
		cleanupResolutionExactGenerationAbsent, resolvedAt, time.Time{}, nil)
	if _, err := harness.Maintenance.Maintain(context.Background(), resolveUnproven); err == nil {
		t.Fatal("unproven resolution closed a debt")
	} else {
		assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	}

	// A path/marker disappearance cannot be expressed: AlreadyAbsent without
	// the exact-generation proof is invalid before any adapter fact.
	unsafeAbsent := cleanupObligationCommand(t, accepted, start, "cleanup-debt-unsafe", "obligation-unsafe", 0, nil)
	unsafeDecision, err := harness.Maintenance.Maintain(context.Background(), unsafeAbsent)
	if err != nil {
		t.Fatalf("create unsafe debt: %v", err)
	}
	resolveUnsafe := cleanupResolutionCommand(t, accepted, start, "cleanup-debt-unsafe", "resolve-unsafe",
		unsafeDecision.CleanupDebt.DebtRevision, cleanupResolutionAlreadyAbsent,
		cleanupResolutionExactGenerationAbsent, resolvedAt, time.Time{}, nil)
	unsafeInput := resolveUnsafe.ResolveCleanupDebtInput
	unsafeInput.Resolution.Uncontained = true
	if _, err := NewResolveCleanupDebt(unsafeInput); err == nil {
		t.Fatal("uncontained already-absent resolution was accepted")
	}
}

func TestCleanupMaintenanceAcceptedExceptionExpiryAndSafeReopen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC)
	administrator := mustAdministratorAuthority(t, "cleanup-exception-admin", 19)
	start := standardStart(t, now, administrator, "exception")
	fixture := runtimeFixtureForStart(start, administrator)
	fixture.EvidenceRoot = cleanupContractEvidenceRoot(now)
	acceptedFact := retainedAcceptedStartFact(start, "runtime-decision-cleanup-exception")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1},
		Runtimes:        []RuntimeFixture{fixture},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
		RetainedStartFacts: []RetainedStartFactFixture{{
			RuntimeRunID: start.RuntimeRunID, DecisionID: acceptedFact.DecisionID,
			OperationID: start.OperationID, Digest: start.CanonicalRequestDigest,
		}},
	})
	if err != nil {
		t.Fatalf("new exception harness: %v", err)
	}
	accepted := RuntimeDecision{
		Fact: acceptedFact,
		Snapshot: RuntimeSnapshot{
			RuntimeRunID: start.RuntimeRunID, RuntimeRevision: fixture.RuntimeRevision,
			RuntimeFence: fixture.RuntimeFence, Deadline: start.Deadline,
		},
	}

	create := cleanupObligationCommand(t, accepted, start, "cleanup-debt-exception", "obligation-exception", 0, nil)
	created, err := harness.Maintenance.Maintain(context.Background(), create)
	if err != nil {
		t.Fatalf("create exception debt: %v", err)
	}
	exceptionUntil := now.Add(30 * time.Minute)
	resolve := cleanupResolutionCommand(t, accepted, start, "cleanup-debt-exception", "resolve-exception",
		created.CleanupDebt.DebtRevision, cleanupResolutionAcceptedException,
		cleanupResolutionAdministratorException, now.Add(2*time.Minute), exceptionUntil, func(resolution *cleanupDebtResolution) {
			resolution.ApprovalReference = "exception-approval-contract"
			resolution.IncidentReference = "incident-contract"
			resolution.TicketReference = "ticket-contract"
		})
	excepted, err := harness.Maintenance.Maintain(context.Background(), resolve)
	if err != nil {
		t.Fatalf("resolve exception debt: %v", err)
	}
	if excepted.CleanupDebt.Status != cleanupDebtResolved || excepted.CleanupDebt.Unresolved ||
		excepted.CleanupDebt.CapacityReleased || excepted.CleanupDebt.ResolutionClass != cleanupResolutionAcceptedException ||
		!excepted.CleanupDebt.ExceptionUntil.Equal(exceptionUntil) {
		t.Fatalf("AcceptedException must not report reclaimed capacity: %+v", excepted.CleanupDebt)
	}

	// Expiry before the exception duration is rejected.
	prematureExpiry, err := NewExpireCleanupDebtException(ExpireCleanupDebtExceptionInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "expire-premature"),
		Reason: CleanupExceptionDurationElapsed, ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence: accepted.Snapshot.RuntimeFence, DebtID: "cleanup-debt-exception",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: administrator, ExpectedRevision: excepted.CleanupDebt.DebtRevision,
		ResourceGeneration: 7, ResourceFence: 9, ExpiredAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("new premature expiry: %v", err)
	}
	if _, err := harness.Maintenance.Maintain(context.Background(), prematureExpiry); err == nil {
		t.Fatal("premature exception expiry was accepted")
	} else {
		assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	}

	// After the duration, expiry reopens the obligation; capacity is not
	// released and the same DebtID is retried.
	expiry, err := NewExpireCleanupDebtException(ExpireCleanupDebtExceptionInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "expire-exception"),
		Reason: CleanupExceptionDurationElapsed, ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence: accepted.Snapshot.RuntimeFence, DebtID: "cleanup-debt-exception",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: administrator, ExpectedRevision: excepted.CleanupDebt.DebtRevision,
		ResourceGeneration: 7, ResourceFence: 9, ExpiredAt: exceptionUntil.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("new exception expiry: %v", err)
	}
	expired, err := harness.Maintenance.Maintain(context.Background(), expiry)
	if err != nil {
		t.Fatalf("expire exception: %v", err)
	}
	if !expired.CleanupDebt.Expired || !expired.CleanupDebt.Reopened || expired.CleanupDebt.CapacityReleased ||
		expired.CleanupDebt.Status != cleanupDebtOpen || !expired.CleanupDebt.Unresolved ||
		expired.CleanupDebt.DebtID != "cleanup-debt-exception" || expired.CleanupDebt.ResolutionClass != 0 {
		t.Fatalf("exception expiry did not safely reopen: %+v", expired.CleanupDebt)
	}

	// A retry attempt after expiry works on the same DebtID.
	retry := cleanupAttemptCommand(t, accepted, start, "cleanup-debt-exception", "attempt-after-reopen",
		expired.CleanupDebt.DebtRevision, 5, 0, nil)
	retried, err := harness.Maintenance.Maintain(context.Background(), retry)
	if err != nil {
		t.Fatalf("attempt after safe reopen: %v", err)
	}
	if retried.CleanupDebt.DebtID != "cleanup-debt-exception" || retried.CleanupDebt.DebtRevision != expired.CleanupDebt.DebtRevision+1 ||
		retried.CleanupDebt.Status != cleanupDebtRetryScheduled {
		t.Fatalf("reopened debt did not retry on same DebtID: %+v", retried.CleanupDebt)
	}
}

func TestCleanupMaintenanceCrossModuleDebtDuplicationRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "cleanup-cross-module-owner", 20)
	start := standardStart(t, now, authority, "cross-module")
	fixture := runtimeFixtureForStart(start, authority)
	foreignDebt := cleanupDebtRecord{
		DebtID: "shared-debt-id", Revision: 1, OwnerModule: "c04",
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, OwnerAuthority: authority,
		ResourceClass: cleanupResourceSandbox, ResourceIdentityDigest: digest(219),
		ResourceGeneration: 3, ResourceFence: 5, CleanupIntent: cleanupIntentReclaim,
		CauseDecisionID: RuntimeDecisionID{value: "runtime-decision-c04"}, CauseOperationID: mustOperationID(t, "c04-operation"),
		RetentionFactDigest: digest(220), EligibilityFactDigest: digest(221),
		Status: cleanupDebtOpen, Unresolved: true, RetryDisposition: cleanupRetryReady,
		Estimation: cleanupEstimation{State: cleanupEstimateUnknown},
		CreatedAt:  now.Add(-time.Hour), EligibleAt: now.Add(-time.Hour), LastMutationID: "c04-mutation",
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1},
		Runtimes:        []RuntimeFixture{fixture},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
		CleanupDebts:    []cleanupDebtRecord{foreignDebt},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
	})
	if err != nil {
		t.Fatalf("new cross-module harness: %v", err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute start: %v", err)
	}

	// C03 cannot create a second DebtID that C04 already owns.
	duplicate := cleanupObligationCommand(t, accepted, start, "shared-debt-id", "obligation-duplicate", 0, nil)
	if _, err := harness.Maintenance.Maintain(context.Background(), duplicate); err == nil {
		t.Fatal("C03 duplicated a C04-owned DebtID")
	} else {
		assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	}

	// A distinct C03 DebtID is unaffected.
	distinct := cleanupObligationCommand(t, accepted, start, "cleanup-debt-c03", "obligation-c03", 0, nil)
	if _, err := harness.Maintenance.Maintain(context.Background(), distinct); err != nil {
		t.Fatalf("C03 obligation with distinct DebtID: %v", err)
	}
}

func TestCleanupMaintenanceExactReplayAndReasonConflict(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "cleanup-replay-owner", 21)
	harness, start, accepted := cleanupContractHarness(t, now, authority, "replay", nil, nil)

	create := cleanupObligationCommand(t, accepted, start, "cleanup-debt-replay", "obligation-replay", 0, nil)
	first, err := harness.Maintenance.Maintain(context.Background(), create)
	if err != nil {
		t.Fatalf("create replay debt: %v", err)
	}
	// Exact replay returns the original decision even when the caller's
	// expected Runtime revision/fence has since advanced (replay wins over
	// current-state validation).
	replayed, err := harness.Maintenance.Maintain(context.Background(), create)
	wantReplay := first.CleanupDebt
	wantReplay.Replayed = true
	if err != nil || replayed.CanonicalRequestDigest != first.CanonicalRequestDigest ||
		replayed.CleanupDebt != wantReplay {
		t.Fatalf("exact obligation replay = %+v err=%v first=%+v", replayed, err, first.CleanupDebt)
	}
	staleView := create
	staleView.ExpectedRuntimeRevision = accepted.Snapshot.RuntimeRevision + 50
	staleView.ExpectedRuntimeFence = accepted.Snapshot.RuntimeFence + 50
	replayedStale, err := harness.Maintenance.Maintain(context.Background(), staleView)
	if err != nil || !replayedStale.CleanupDebt.Replayed || replayedStale.CleanupDebt != wantReplay {
		t.Fatalf("replay with stale expected revision = %+v err=%v, want retained decision", replayedStale, err)
	}
	// Same idempotency key with a different reason is a conflict, never a
	// silently different operation.
	conflictingReason, err := NewCreateCleanupObligation(CreateCleanupObligationInput{
		SchemaVersion:           SchemaV1,
		OperationID:             mustOperationID(t, "obligation-replay"),
		Reason:                  CleanupObligationNodeLost,
		ExpectedRuntimeRevision: accepted.Snapshot.RuntimeRevision,
		ExpectedRuntimeFence:    accepted.Snapshot.RuntimeFence,
		Obligation:              create.Obligation,
	})
	if err != nil {
		t.Fatalf("new conflicting obligation: %v", err)
	}
	if _, err := harness.Maintenance.Maintain(context.Background(), conflictingReason); err == nil {
		t.Fatal("same key with different reason was accepted")
	} else {
		assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	}
}

func TestOperationalDiagnosticsReadOnlyContentFreeNonEnumerating(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	administrator := mustAdministratorAuthority(t, "cleanup-diag-admin", 22)
	authority := mustTaskOrchestrationAuthority(t, "cleanup-diag-owner", 23)
	harness, start, accepted := cleanupContractHarness(t, now, authority, "diag", nil, nil)
	create := cleanupObligationCommand(t, accepted, start, "cleanup-debt-diag", "obligation-diag",
		cleanupBlockerQuarantine, nil)
	if _, err := harness.Maintenance.Maintain(context.Background(), create); err != nil {
		t.Fatalf("create diag debt: %v", err)
	}

	// Administrator-only: a non-administrator authority cannot diagnose.
	nonAdmin := OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: authority,
		Lookup: DiagnosticLookupCleanupDebt, DebtID: "cleanup-debt-diag",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Bounded: true,
	}
	if _, err := harness.Diagnostics.Diagnose(context.Background(), nonAdmin); err == nil {
		t.Fatal("non-administrator diagnostics were accepted")
	}

	// Non-enumerating: a lookup without an exact reference is rejected.
	for name, query := range map[string]OperationalDiagnosticQuery{
		"no debt id": {
			SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: administrator,
			Lookup: DiagnosticLookupCleanupDebt, PersonalWorkspaceID: start.PersonalWorkspaceID,
			RuntimeRunID: start.RuntimeRunID, Bounded: true,
		},
		"no node id": {
			SchemaVersion: SchemaV1, Reason: DiagnosticReasonNodeHealth, Authority: administrator,
			Lookup: DiagnosticLookupExecutionNode, Bounded: true,
		},
		"no runtime id": {
			SchemaVersion: SchemaV1, Reason: DiagnosticReasonCapacityInvestigation, Authority: administrator,
			Lookup: DiagnosticLookupRuntimeLease, PersonalWorkspaceID: start.PersonalWorkspaceID, Bounded: true,
		},
		"unbounded": {
			SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: administrator,
			Lookup: DiagnosticLookupCleanupDebt, DebtID: "cleanup-debt-diag",
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := harness.Diagnostics.Diagnose(context.Background(), query); err == nil {
				t.Fatalf("enumerating/unbounded diagnostic query was accepted")
			}
		})
	}

	// Exact administrator query returns a bounded content-free view.
	view, err := harness.Diagnostics.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: administrator,
		Lookup: DiagnosticLookupCleanupDebt, DebtID: "cleanup-debt-diag",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Bounded: true,
	})
	if err != nil {
		t.Fatalf("diagnose exact debt: %v", err)
	}
	if view.Debt == nil || view.Debt.DebtID != "cleanup-debt-diag" || view.Debt.Status != cleanupDebtBlocked ||
		view.Debt.Blockers != cleanupBlockerQuarantine || view.Debt.EstimateState != cleanupEstimateUnknown {
		t.Fatalf("content-free diagnostic view lost debt facts: %+v", view.Debt)
	}

	// Runtime lease diagnostics are read-only projections.
	runtimeView, err := harness.Diagnostics.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCapacityInvestigation, Authority: administrator,
		Lookup: DiagnosticLookupRuntimeLease, RuntimeRunID: start.RuntimeRunID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, Bounded: true,
	})
	if err != nil {
		t.Fatalf("diagnose runtime lease: %v", err)
	}
	if runtimeView.Runtime == nil || runtimeView.Runtime.RuntimeRunID != start.RuntimeRunID ||
		runtimeView.Runtime.RuntimeRevision == 0 {
		t.Fatalf("runtime diagnostic view incomplete: %+v", runtimeView.Runtime)
	}

	// Execution node diagnostics expose readiness/quarantine facts only.
	nodeID := startNodeID(t, "cleanup-diag-node")
	nodeStart := standardStart(t, now, authority, "diag-node")
	nodeGrant := grantFixtureForStart(nodeStart, now.Add(15*time.Minute), true)
	nodeGrant.ExecutionNodeID = nodeID
	nodeGrant.NodeCapacityGeneration = 1
	nodeHarness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(nodeStart, authority)},
		AdmissionGrants: []AdmissionGrantFixture{nodeGrant},
		Nodes:           []ExecutionNodeFixture{executionNodeFixtureForStart(t, nodeStart, nodeGrant, now)},
	})
	if err != nil {
		t.Fatalf("new node harness: %v", err)
	}
	nodeView, err := nodeHarness.Diagnostics.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonNodeHealth, Authority: administrator,
		Lookup: DiagnosticLookupExecutionNode, ExecutionNodeID: nodeID, Bounded: true,
	})
	if err != nil {
		t.Fatalf("diagnose execution node: %v", err)
	}
	if nodeView.Node == nil || nodeView.Node.ExecutionNodeID != nodeID ||
		nodeView.Node.Readiness != NodeReady || nodeView.Node.Quarantined {
		t.Fatalf("node diagnostic view incomplete: %+v", nodeView.Node)
	}

	// Diagnostics never mutate: the debt is unchanged after queries.
	diagnosed, err := harness.Diagnostics.Diagnose(context.Background(), OperationalDiagnosticQuery{
		SchemaVersion: SchemaV1, Reason: DiagnosticReasonCleanupHealth, Authority: administrator,
		Lookup: DiagnosticLookupCleanupDebt, DebtID: "cleanup-debt-diag",
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Bounded: true,
	})
	if err != nil || diagnosed.Debt == nil || diagnosed.Debt.DebtRevision != 1 {
		t.Fatalf("diagnostics mutated the debt: %+v err=%v", diagnosed, err)
	}
}
