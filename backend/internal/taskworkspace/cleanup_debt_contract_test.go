package taskworkspace_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestCleanupObligationIsDurableAndUniqueBeforePhysicalAttempt(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	cleanup := &exactGenerationCleanupDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Cleanup = cleanup
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-debt-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}

	request := createCleanupObligationRequest(confirmed, "create-cleanup-debt-1")
	created, err := lifecycle.CreateCleanupObligation(context.Background(), request)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}
	if created.DebtID == "" || created.State != taskworkspace.CleanupDebtOpen {
		t.Fatalf("created cleanup debt = %#v, want durable open DebtID", created)
	}
	if cleanup.inspectCalls != 0 || cleanup.cleanupCalls != 0 {
		t.Fatal("physical cleanup was consulted before the obligation was persisted")
	}

	replayed, err := lifecycle.CreateCleanupObligation(context.Background(), request)
	if err != nil {
		t.Fatalf("exact replay cleanup obligation: %v", err)
	}
	if replayed.DebtID != created.DebtID {
		t.Fatalf("exact replay DebtID = %q, want %q", replayed.DebtID, created.DebtID)
	}
	duplicateDelivery := request
	duplicateDelivery.Operation.ID = "duplicate-delivery-cleanup-debt-1"
	duplicateDelivery.Operation.RequestDigest = duplicateDelivery.CanonicalRequestDigest()
	duplicated, err := lifecycle.CreateCleanupObligation(context.Background(), duplicateDelivery)
	if err != nil || duplicated.DebtID != created.DebtID {
		t.Fatalf("duplicate delivery created another debt: %#v, err = %v", duplicated, err)
	}
	foreignOwner := request
	foreignOwner.Owner = taskworkspace.CleanupOwnerDurableObject
	foreignOwner.Operation.ID = "foreign-owner-cleanup-debt-1"
	foreignOwner.Operation.RequestDigest = foreignOwner.CanonicalRequestDigest()
	if _, err := lifecycle.CreateCleanupObligation(context.Background(), foreignOwner); err == nil {
		t.Fatal("C04 absorbed Durable Object-owned cleanup debt")
	}

	restartedConfig := config
	restarted := taskworkspace.NewInMemory(restartedConfig)
	inspected, err := restarted.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		DebtID:         created.DebtID,
	})
	if err != nil {
		t.Fatalf("inspect cleanup debt after restart: %v", err)
	}
	if inspected.DebtID != created.DebtID || inspected.State != taskworkspace.CleanupDebtOpen {
		t.Fatalf("persisted cleanup debt = %#v", inspected)
	}
}

func TestExpiredCleanupClaimRetriesTheSameDebtAndAdvancesGeneration(t *testing.T) {
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Cleanup = &exactGenerationCleanupDouble{}
	config.CleanupRetryPolicy = taskworkspace.CleanupRetryPolicy{
		ClaimLifetime:  10,
		InitialBackoff: 5,
		MaximumBackoff: 40,
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-claim-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-claim-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}

	firstRequest := claimCleanupDebtRequest(created, 0, "claim-cleanup-debt-1")
	first, err := lifecycle.ClaimCleanupDebt(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("claim cleanup debt: %v", err)
	}
	if first.DebtID != created.DebtID || first.State != taskworkspace.CleanupDebtClaimed ||
		first.RetryGeneration != 1 || first.ClaimGeneration != 1 || first.ClaimID == "" ||
		first.ClaimExpiresAt != 110 {
		t.Fatalf("first claim = %#v", first)
	}
	exactReplay, err := lifecycle.ClaimCleanupDebt(context.Background(), firstRequest)
	if err != nil || exactReplay.ClaimID != first.ClaimID || exactReplay.RetryGeneration != 1 {
		t.Fatalf("exact claim replay = %#v, err = %v", exactReplay, err)
	}

	now = 110
	second, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		first, first.RetryGeneration, "claim-cleanup-debt-after-expiry-1",
	))
	if err != nil {
		t.Fatalf("claim cleanup debt after expiry: %v", err)
	}
	if second.DebtID != created.DebtID || second.ClaimID == first.ClaimID ||
		second.RetryGeneration != 2 || second.ClaimGeneration != 2 || second.AttemptCount != 0 ||
		second.CurrentBackoff != 0 || second.NextRetryAt != created.NextRetryAt ||
		second.Capacity.Bytes.Known || second.Capacity.Inodes.Known {
		t.Fatalf("claim-expiry retry changed debt identity or evidence: %#v", second)
	}
}

func TestCleanupRevalidatesExactAuthorityBeforeReclaimAndPreservesUnknownCapacity(t *testing.T) {
	now := taskworkspace.Instant(100)
	cleanup := &exactGenerationCleanupDouble{
		inspectionDisposition: taskworkspace.CleanupInspectionEligible,
		attemptOutcome:        taskworkspace.CleanupReclaimed,
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Cleanup = cleanup
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-revalidate-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-revalidate-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}
	claimed, err := lifecycle.ClaimCleanupDebt(
		context.Background(), claimCleanupDebtRequest(created, 0, "claim-cleanup-revalidate-debt-1"),
	)
	if err != nil {
		t.Fatalf("claim cleanup debt: %v", err)
	}

	stale := reconcileCleanupDebtRequest(claimed, "reconcile-cleanup-stale-generation-1")
	stale.Generation++
	stale.Operation.RequestDigest = stale.CanonicalRequestDigest()
	_, err = lifecycle.ReconcileCleanupDebt(context.Background(), stale)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	if cleanup.inspectCalls != 0 || cleanup.cleanupCalls != 0 {
		t.Fatal("stale lifecycle authority reached the cleanup adapter")
	}

	reconciled, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
		claimed, "reconcile-cleanup-current-generation-1",
	))
	if err != nil {
		t.Fatalf("reconcile cleanup debt: %v", err)
	}
	if cleanup.inspectCalls != 1 || cleanup.cleanupCalls != 1 || cleanup.events[0] != "inspect" ||
		cleanup.events[1] != "reclaim" {
		t.Fatalf("cleanup adapter events = %#v, want exact inspect before reclaim", cleanup.events)
	}
	if reconciled.State != taskworkspace.CleanupDebtResolved ||
		reconciled.Resolution != taskworkspace.CleanupReclaimed || reconciled.AttemptCount != 1 {
		t.Fatalf("reconciled cleanup debt = %#v", reconciled)
	}
	if reconciled.Capacity.Bytes.Known || reconciled.Capacity.Inodes.Known {
		t.Fatalf("unknown capacity was reported as known: %#v", reconciled.Capacity)
	}
}

func TestCleanupRejectsStaleAdapterFenceBeforePhysicalAttempt(t *testing.T) {
	cleanup := &exactGenerationCleanupDouble{
		inspectionDisposition: taskworkspace.CleanupInspectionEligible,
		mutateInspection: func(evidence *taskworkspace.CleanupInspectionEvidence) {
			evidence.Fence++
		},
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Cleanup = cleanup
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-stale-adapter-fence-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-stale-adapter-fence-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}
	claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		created, 0, "claim-stale-adapter-fence-debt-1",
	))
	if err != nil {
		t.Fatalf("claim cleanup debt: %v", err)
	}
	result, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
		claimed, "reconcile-stale-adapter-fence-debt-1",
	))
	if err != nil {
		t.Fatalf("record stale adapter evidence: %v", err)
	}
	if result.State != taskworkspace.CleanupDebtRetryScheduled ||
		result.LastFailureCategory != taskworkspace.CleanupFailureInvalidEvidence ||
		result.SafeFailureEvidenceRoot == "" || result.AttemptCount != 0 || cleanup.cleanupCalls != 0 {
		t.Fatalf("stale adapter fence result = %#v, physical calls = %d", result, cleanup.cleanupCalls)
	}
}

func TestCleanupAdapterFailureRetainsSafeFailureEvidence(t *testing.T) {
	config := taskworkspaceTestConfig(&happyDurableObject{})
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-adapter-failure-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-adapter-failure-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}
	claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		created, 0, "claim-cleanup-adapter-failure-debt-1",
	))
	if err != nil {
		t.Fatalf("claim cleanup debt: %v", err)
	}
	result, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
		claimed, "reconcile-cleanup-adapter-failure-debt-1",
	))
	if err != nil {
		t.Fatalf("record cleanup adapter failure: %v", err)
	}
	if result.State != taskworkspace.CleanupDebtRetryScheduled ||
		result.LastFailureCategory != taskworkspace.CleanupFailureAdapterUnavailable ||
		result.SafeFailureEvidenceRoot == "" || result.AttemptCount != 0 {
		t.Fatalf("cleanup adapter failure result = %#v", result)
	}
}

func TestCleanupPhysicalIntentIsJournaledBeforeAdapterAction(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	cleanup := &exactGenerationCleanupDouble{
		inspectionDisposition: taskworkspace.CleanupInspectionEligible,
		attemptOutcome:        taskworkspace.CleanupReclaimed,
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Cleanup = cleanup
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterIntentPersistence &&
			event.OperationID == "reconcile-journaled-cleanup-debt-1" {
			return errors.New("stop after cleanup intent persistence")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-journaled-cleanup-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-journaled-cleanup-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}
	claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		created, 0, "claim-journaled-cleanup-debt-1",
	))
	if err != nil {
		t.Fatalf("claim cleanup debt: %v", err)
	}
	reconcileRequest := reconcileCleanupDebtRequest(claimed, "reconcile-journaled-cleanup-debt-1")
	if _, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileRequest); err == nil {
		t.Fatal("cleanup continued after the journal persistence fault")
	}
	if cleanup.inspectCalls != 0 || cleanup.cleanupCalls != 0 {
		t.Fatal("cleanup adapter was called before its operation intent was durably inspectable")
	}
	inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    reconcileRequest.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationPending ||
		inspection.IntentState != taskworkspace.OperationIntentPersisted ||
		inspection.Operation.RequestDigest != reconcileRequest.Operation.RequestDigest {
		t.Fatalf("journaled cleanup operation = %#v, err = %v", inspection, err)
	}

	restarted := taskworkspace.NewInMemory(config)
	inspection, err = restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    reconcileRequest.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationTerminal || cleanup.cleanupCalls != 1 {
		t.Fatalf("reconciled journaled cleanup operation = %#v, calls = %d, err = %v", inspection, cleanup.cleanupCalls, err)
	}
	debt, err := restarted.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		DebtID:         created.DebtID,
	})
	if err != nil || debt.State != taskworkspace.CleanupDebtResolved || debt.Resolution != taskworkspace.CleanupReclaimed {
		t.Fatalf("reconciled cleanup debt = %#v, err = %v", debt, err)
	}
}

func TestAmbiguousCleanupStaysOpenUntilExactGenerationInspectionReconcilesIt(t *testing.T) {
	now := taskworkspace.Instant(100)
	cleanup := &exactGenerationCleanupDouble{
		inspectionSequence: []taskworkspace.CleanupInspectionDisposition{
			taskworkspace.CleanupInspectionEligible,
			taskworkspace.CleanupInspectionAlreadyAbsent,
		},
		attemptOutcome: taskworkspace.CleanupReclaimed,
		attemptError:   taskworkspace.ErrCleanupResultAmbiguous,
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Cleanup = cleanup
	config.CleanupRetryPolicy = taskworkspace.CleanupRetryPolicy{
		ClaimLifetime:  10,
		InitialBackoff: 5,
		MaximumBackoff: 40,
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-ambiguous-cleanup-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-ambiguous-cleanup-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}
	claimed, err := lifecycle.ClaimCleanupDebt(
		context.Background(), claimCleanupDebtRequest(created, 0, "claim-ambiguous-cleanup-debt-1"),
	)
	if err != nil {
		t.Fatalf("claim cleanup debt: %v", err)
	}
	firstRequest := reconcileCleanupDebtRequest(claimed, "reconcile-ambiguous-cleanup-debt-1")
	ambiguous, err := lifecycle.ReconcileCleanupDebt(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("record ambiguous cleanup result: %v", err)
	}
	if ambiguous.State != taskworkspace.CleanupDebtRetryScheduled || ambiguous.Resolution != "" ||
		ambiguous.AttemptCount != 1 || ambiguous.CurrentBackoff != 5 || ambiguous.NextRetryAt != 105 ||
		ambiguous.LastFailureCategory != taskworkspace.CleanupFailureAmbiguous ||
		ambiguous.SafeFailureEvidenceRoot == "" {
		t.Fatalf("ambiguous cleanup debt = %#v", ambiguous)
	}
	if cleanup.inspectCalls != 1 || cleanup.cleanupCalls != 1 {
		t.Fatalf("ambiguous cleanup calls = inspect %d, reclaim %d", cleanup.inspectCalls, cleanup.cleanupCalls)
	}

	replayed, err := lifecycle.ReconcileCleanupDebt(context.Background(), firstRequest)
	if err != nil || replayed.DebtID != created.DebtID || cleanup.inspectCalls != 1 || cleanup.cleanupCalls != 1 {
		t.Fatalf("ambiguous exact replay = %#v, err = %v, calls = %d/%d", replayed, err, cleanup.inspectCalls, cleanup.cleanupCalls)
	}

	now = 105
	cleanup.attemptError = nil
	retryClaim, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		ambiguous, ambiguous.RetryGeneration, "claim-after-ambiguous-cleanup-1",
	))
	if err != nil {
		t.Fatalf("claim ambiguous cleanup retry: %v", err)
	}
	now = 115
	retryAfterClaimLoss, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		retryClaim, retryClaim.RetryGeneration, "claim-after-ambiguous-cleanup-claim-loss-1",
	))
	if err != nil {
		t.Fatalf("claim ambiguous cleanup after claim loss: %v", err)
	}
	if retryAfterClaimLoss.RetryGeneration != retryClaim.RetryGeneration+1 ||
		retryAfterClaimLoss.AttemptCount != ambiguous.AttemptCount ||
		retryAfterClaimLoss.CurrentBackoff != ambiguous.CurrentBackoff ||
		retryAfterClaimLoss.NextRetryAt != ambiguous.NextRetryAt ||
		retryAfterClaimLoss.LastFailureCategory != ambiguous.LastFailureCategory ||
		retryAfterClaimLoss.SafeFailureEvidenceRoot != ambiguous.SafeFailureEvidenceRoot {
		t.Fatalf("claim loss discarded retry evidence: before=%#v after=%#v", ambiguous, retryAfterClaimLoss)
	}
	reconciled, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
		retryAfterClaimLoss, "inspect-after-ambiguous-cleanup-1",
	))
	if err != nil {
		t.Fatalf("reconcile ambiguous cleanup by exact inspection: %v", err)
	}
	if reconciled.DebtID != created.DebtID || reconciled.State != taskworkspace.CleanupDebtResolved ||
		reconciled.Resolution != taskworkspace.CleanupAlreadyAbsent || reconciled.AttemptCount != 1 ||
		cleanup.inspectCalls != 2 || cleanup.cleanupCalls != 1 {
		t.Fatalf("ambiguous cleanup reconciliation = %#v, calls = %d/%d", reconciled, cleanup.inspectCalls, cleanup.cleanupCalls)
	}
}

func TestCleanupDistinguishesBlockedQuarantinedAndRetainedByAuthority(t *testing.T) {
	tests := []struct {
		name        string
		cleanup     *exactGenerationCleanupDouble
		wantState   taskworkspace.CleanupDebtState
		wantResult  taskworkspace.CleanupResolutionClass
		wantBlocker taskworkspace.CleanupBlocker
	}{
		{
			name: "grace remains blocked",
			cleanup: &exactGenerationCleanupDouble{
				inspectionDisposition: taskworkspace.CleanupInspectionBlocked,
				graceState:            taskworkspace.CleanupAuthorityBlocked,
				blockers:              []taskworkspace.CleanupBlocker{taskworkspace.CleanupGraceBlocker},
			},
			wantState:   taskworkspace.CleanupDebtBlocked,
			wantBlocker: taskworkspace.CleanupGraceBlocker,
		},
		{
			name: "quarantine remains blocked",
			cleanup: &exactGenerationCleanupDouble{
				inspectionDisposition: taskworkspace.CleanupInspectionBlocked,
				quarantineState:       taskworkspace.CleanupAuthorityBlocked,
				blockers:              []taskworkspace.CleanupBlocker{taskworkspace.CleanupQuarantineBlocker},
			},
			wantState:   taskworkspace.CleanupDebtBlocked,
			wantBlocker: taskworkspace.CleanupQuarantineBlocker,
		},
		{
			name: "current reference closes as retained",
			cleanup: &exactGenerationCleanupDouble{
				inspectionDisposition: taskworkspace.CleanupInspectionRetainedByAuthority,
				referenceState:        taskworkspace.CleanupAuthorityBlocked,
				blockers:              []taskworkspace.CleanupBlocker{taskworkspace.CleanupReferenceBlocker},
			},
			wantState:   taskworkspace.CleanupDebtResolved,
			wantResult:  taskworkspace.CleanupRetainedByAuthority,
			wantBlocker: taskworkspace.CleanupReferenceBlocker,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Cleanup = tt.cleanup
			config.CleanupRetryPolicy = taskworkspace.CleanupRetryPolicy{InitialBackoff: 5}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-cleanup-blocker-workspace-"+tt.name,
			))
			if err != nil {
				t.Fatalf("confirm Task Workspace: %v", err)
			}
			created, err := lifecycle.CreateCleanupObligation(context.Background(), createCleanupObligationRequest(
				confirmed, "create-cleanup-blocker-"+taskworkspace.OperationID(tt.name),
			))
			if err != nil {
				t.Fatalf("create cleanup obligation: %v", err)
			}
			claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
				created, 0, "claim-cleanup-blocker-"+taskworkspace.OperationID(tt.name),
			))
			if err != nil {
				t.Fatalf("claim cleanup debt: %v", err)
			}
			result, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
				claimed, "reconcile-cleanup-blocker-"+taskworkspace.OperationID(tt.name),
			))
			if err != nil {
				t.Fatalf("reconcile cleanup debt: %v", err)
			}
			if result.State != tt.wantState || result.Resolution != tt.wantResult ||
				len(result.Blockers) != 1 || result.Blockers[0] != tt.wantBlocker || tt.cleanup.cleanupCalls != 0 {
				t.Fatalf("cleanup blocker result = %#v, physical calls = %d", result, tt.cleanup.cleanupCalls)
			}
		})
	}
}

func TestAcceptedExceptionFailsClosedOnAuditAndRequiresExplicitReopenAfterExpiry(t *testing.T) {
	now := taskworkspace.Instant(100)
	administrator := platformAdministratorAuthority(now)
	audit := &cleanupAuditDouble{failure: errors.New("audit unavailable")}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Cleanup = &exactGenerationCleanupDouble{}
	config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
	config.CurrentPlatformAdministratorAuthority = func(
		id taskworkspace.PlatformAdministratorID,
	) (taskworkspace.PlatformAdministratorAuthority, bool) {
		return administrator, id == administrator.ID
	}
	config.CleanupAudit = audit
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-cleanup-exception-workspace-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	created, err := lifecycle.CreateCleanupObligation(
		context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-exception-debt-1"),
	)
	if err != nil {
		t.Fatalf("create cleanup obligation: %v", err)
	}

	failed := resolveAcceptedExceptionRequest(
		created, administrator, "resolve-cleanup-exception-without-audit-1",
	)
	_, err = lifecycle.ResolveCleanupDebt(context.Background(), failed)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
	stillOpen, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: created.PolicyDomainID,
		TaskID:         created.TaskID,
		DebtID:         created.DebtID,
	})
	if err != nil || stillOpen.State != taskworkspace.CleanupDebtOpen || stillOpen.Resolution != "" {
		t.Fatalf("mandatory-audit failure changed debt: %#v, err = %v", stillOpen, err)
	}

	audit.failure = nil
	audit.skipCommit = true
	_, err = lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
		created, administrator, "resolve-cleanup-exception-without-atomic-commit-1",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
	stillOpen, err = lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: created.PolicyDomainID,
		TaskID:         created.TaskID,
		DebtID:         created.DebtID,
	})
	if err != nil || stillOpen.State != taskworkspace.CleanupDebtOpen || stillOpen.Resolution != "" {
		t.Fatalf("uncommitted mandatory audit changed debt: %#v, err = %v", stillOpen, err)
	}

	audit.skipCommit = false
	audit.failureAfterCommit = errors.New("audit response lost after authoritative commit")
	accepted, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
		created, administrator, "resolve-cleanup-exception-1",
	))
	if err != nil {
		t.Fatalf("accept cleanup exception: %v", err)
	}
	if accepted.State != taskworkspace.CleanupDebtResolved ||
		accepted.Resolution != taskworkspace.CleanupAcceptedException ||
		accepted.ResolvedByAdministratorID != administrator.ID ||
		accepted.ClosedReason != taskworkspace.CleanupExceptionUnsafeToReclaim ||
		accepted.ResolutionDuration != 20 || accepted.ExceptionExpiresAt != 120 ||
		accepted.ResolutionAuditEvidenceRoot == "" || accepted.ResolutionEvidenceRoot == "" ||
		accepted.ReclaimedCapacity.Bytes.Known || accepted.ReclaimedCapacity.Inodes.Known {
		t.Fatalf("accepted exception = %#v", accepted)
	}
	audit.failureAfterCommit = nil

	now = 120
	expired, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: accepted.PolicyDomainID,
		TaskID:         accepted.TaskID,
		DebtID:         accepted.DebtID,
	})
	if err != nil || expired.State != taskworkspace.CleanupDebtResolved || !expired.ExceptionExpired {
		t.Fatalf("expired exception changed without explicit decision: %#v, err = %v", expired, err)
	}
	reopened, err := lifecycle.ReopenCleanupDebt(context.Background(), reopenCleanupDebtRequest(
		expired, administrator, "reopen-expired-cleanup-exception-1",
	))
	if err != nil {
		t.Fatalf("reopen expired cleanup exception: %v", err)
	}
	if reopened.DebtID != created.DebtID || reopened.State != taskworkspace.CleanupDebtOpen ||
		reopened.Resolution != "" || reopened.ExceptionExpired || reopened.RetryGeneration != accepted.RetryGeneration+1 ||
		audit.calls != 4 {
		t.Fatalf("explicitly reopened exception = %#v, audit calls = %d", reopened, audit.calls)
	}
}

func TestLegacyCleanupAdapterAcceptsOnlyPostCutoverOpaqueC04Obligations(t *testing.T) {
	cutover := commitCutoverAuthority()
	currentCutover := cutover
	cleanup := &exactGenerationCleanupDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = taskworkspace.NewInMemoryPersistence()
	config.Cleanup = cleanup
	config.LegacyMigrationAuthorityID = "legacy-migration-authority-1"
	config.CurrentCommitCutoverAuthority = func(
		id taskworkspace.CommitCutoverAuthorityID,
	) (taskworkspace.CommitCutoverAuthority, bool) {
		return currentCutover, id == currentCutover.ID
	}
	lifecycle := taskworkspace.NewInMemory(config)
	before, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-before-legacy-cleanup-obligation-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}

	base := legacyCleanupObligationRequest(before, cutover, "accept-legacy-cleanup-obligation-1")
	rejected := []struct {
		name   string
		mutate func(*taskworkspace.AcceptLegacyCleanupObligationRequest)
	}{
		{
			name: "pre-cutover",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				currentCutover.CommittedAt = 0
				currentCutover.Digest = currentCutover.CanonicalDigest()
				request.CommitCutoverAuthority = currentCutover
			},
		},
		{
			name: "unauthorized authority",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.CommitCutoverAuthority.AuthorityID = "untrusted-migration-authority"
				request.CommitCutoverAuthority.Digest = request.CommitCutoverAuthority.CanonicalDigest()
			},
		},
		{
			name: "cutover generation mismatch",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.CutoverGeneration++
			},
		},
		{
			name: "locator-derived authority",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.SourceAuthority = taskworkspace.LegacyCleanupSourceAuthority("legacy_locator_authority")
			},
		},
		{
			name: "path-bearing resource identity",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceID = "/legacy/workspaces/task-1"
			},
		},
		{
			name: "session-bearing resource identity",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceID = "legacy-session-42"
			},
		},
		{
			name: "locator-bearing resource generation",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceGeneration = "file://legacy-cleanup-locator"
			},
		},
		{
			name: "marker-derived resource identity",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceID = "legacy-cleanup-marker-1"
			},
		},
		{
			name: "mtime-derived resource identity",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceID = "legacy-mtime-1"
			},
		},
		{
			name: "last-run-derived resource identity",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceID = "legacy-last-run-state-1"
			},
		},
		{
			name: "log-derived resource identity",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.ResourceID = "legacy-log-1"
			},
		},
		{
			name: "foreign cleanup owner",
			mutate: func(request *taskworkspace.AcceptLegacyCleanupObligationRequest) {
				request.Owner = taskworkspace.CleanupOwnerRuntimeExecution
			},
		},
	}
	for index, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			currentCutover = cutover
			request := base
			request.Operation.ID = taskworkspace.OperationID(fmt.Sprintf("reject-legacy-cleanup-%d", index))
			tt.mutate(&request)
			request.Operation.RequestDigest = request.CanonicalRequestDigest()
			_, err := lifecycle.AcceptLegacyCleanupObligation(context.Background(), request)
			if err == nil {
				t.Fatal("untrusted legacy cleanup obligation was accepted")
			}
		})
	}

	currentCutover = cutover
	accepted, err := lifecycle.AcceptLegacyCleanupObligation(context.Background(), base)
	if err != nil {
		t.Fatalf("accept post-cutover cleanup obligation: %v", err)
	}
	restarted := taskworkspace.NewInMemory(config)
	replayed, err := restarted.AcceptLegacyCleanupObligation(context.Background(), base)
	if err != nil || replayed.DebtID != accepted.DebtID || accepted.State != taskworkspace.CleanupDebtOpen ||
		accepted.LegacyObligationNumber != 41 || cleanup.inspectCalls != 0 || cleanup.cleanupCalls != 0 {
		t.Fatalf("accepted legacy cleanup obligation = %#v, replay = %#v, err = %v", accepted, replayed, err)
	}
	after, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-legacy-cleanup-obligation-1",
	))
	if err != nil || after != before {
		t.Fatalf("legacy cleanup obligation changed current workspace authority: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestCheckpointSemanticCleanupPersistsDebtBeforePhysicalReclaim(t *testing.T) {
	now := taskworkspace.Instant(100)
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.CheckpointReclamation = mechanics
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultBeforeCheckpointReclaim {
			return errors.New("stop before physical reclaim")
		}
		return nil
	}
	lifecycle, current, checkpoint := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(
		t, lifecycle, current, checkpoint.CheckpointID, "release-before-cleanup-debt-persistence-1",
	)
	now = released.EligibleAt

	pending, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
		current, checkpoint.CheckpointID, released.RetentionGeneration,
		"reclaim-checkpoint-with-persisted-cleanup-debt-1",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if pending.CleanupDebtID == "" || mechanics.calls != 0 {
		t.Fatalf("pre-reclaim result = %#v, mechanics calls = %d", pending, mechanics.calls)
	}
	debt, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		DebtID:         pending.CleanupDebtID,
	})
	if err != nil {
		t.Fatalf("inspect Checkpoint cleanup debt: %v", err)
	}
	if debt.State != taskworkspace.CleanupDebtOpen || debt.Owner != taskworkspace.CleanupOwnerC04 ||
		debt.ResourceClass != taskworkspace.CleanupCheckpointSemantic || debt.AttemptCount != 0 {
		t.Fatalf("Checkpoint cleanup debt before physical attempt = %#v", debt)
	}
}

func TestCheckpointSemanticCleanupRetriesOneDebtAndCompletesOriginalOperation(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	mechanics := &checkpointReclamationMechanics{present: true}
	faulted := false
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Persistence = persistence
	config.CheckpointReclamation = mechanics
	config.CleanupRetryPolicy = taskworkspace.CleanupRetryPolicy{
		ClaimLifetime:  5,
		InitialBackoff: 10,
		MaximumBackoff: 40,
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterCheckpointReclaim && !faulted {
			faulted = true
			return errors.New("lose Checkpoint reclaim result")
		}
		return nil
	}
	lifecycle, current, checkpoint := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(
		t, lifecycle, current, checkpoint.CheckpointID, "release-before-checkpoint-cleanup-retry-1",
	)
	now = released.EligibleAt
	reclaim := reclaimCheckpointRequest(
		current, checkpoint.CheckpointID, released.RetentionGeneration,
		"reclaim-checkpoint-with-ambiguous-cleanup-1",
	)
	pending, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if pending.CleanupDebtID == "" || mechanics.calls != 1 {
		t.Fatalf("ambiguous Checkpoint reclaim = %#v, calls = %d", pending, mechanics.calls)
	}
	debt, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		DebtID:         pending.CleanupDebtID,
	})
	if err != nil || debt.State != taskworkspace.CleanupDebtRetryScheduled || debt.AttemptCount != 1 ||
		debt.RetryGeneration != 1 || debt.NextRetryAt != now+10 || debt.SafeFailureEvidenceRoot == "" {
		t.Fatalf("ambiguous Checkpoint cleanup debt = %#v, err = %v", debt, err)
	}

	restarted := taskworkspace.NewInMemory(config)
	inspection, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    reclaim.Operation.ID,
	})
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if inspection.Disposition != taskworkspace.OperationReconciliationRequired || mechanics.calls != 1 {
		t.Fatalf("Checkpoint cleanup ignored backoff: inspection=%#v calls=%d", inspection, mechanics.calls)
	}

	now = debt.NextRetryAt
	claimed, err := restarted.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
		debt, debt.RetryGeneration, "claim-checkpoint-cleanup-after-ambiguity-1",
	))
	if err != nil {
		t.Fatalf("claim Checkpoint cleanup debt after backoff: %v", err)
	}
	resolved, err := restarted.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
		claimed, "reconcile-checkpoint-cleanup-after-ambiguity-1",
	))
	if err != nil || resolved.State != taskworkspace.CleanupDebtResolved ||
		resolved.Resolution != taskworkspace.CleanupAlreadyAbsent || resolved.AttemptCount != 2 ||
		resolved.RetryGeneration != 2 || mechanics.calls != 2 {
		t.Fatalf("reconciled Checkpoint cleanup debt = %#v, calls = %d, err = %v", resolved, mechanics.calls, err)
	}
	inspection, err = restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    reclaim.Operation.ID,
	})
	if err != nil || inspection.Disposition != taskworkspace.OperationTerminal ||
		inspection.ReclaimCheckpoint == nil || inspection.ReclaimCheckpoint.CleanupDebtID != pending.CleanupDebtID ||
		inspection.ReclaimCheckpoint.Outcome != taskworkspace.CheckpointAlreadyAbsent {
		t.Fatalf("original Checkpoint reclaim after debt reconciliation = %#v, err = %v", inspection, err)
	}
	retention := inspectCheckpointRetention(t, restarted, current, checkpoint.CheckpointID)
	if retention.Decision != taskworkspace.CheckpointPhysicallyReclaimed {
		t.Fatalf("Checkpoint semantic fact after cleanup reconciliation = %#v", retention)
	}
}

func createCleanupObligationRequest(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	operationID taskworkspace.OperationID,
) taskworkspace.CreateCleanupObligationRequest {
	request := taskworkspace.CreateCleanupObligationRequest{
		PolicyDomainID:     "policy-domain-1",
		TaskID:             "task-1",
		TaskWorkspaceID:    confirmed.TaskWorkspaceID,
		Owner:              taskworkspace.CleanupOwnerC04,
		ResourceClass:      taskworkspace.CleanupWorkspaceResidue,
		ResourceID:         "opaque-cleanup-resource-1",
		ResourceGeneration: "opaque-cleanup-generation-1",
		Generation:         confirmed.Generation,
		Fence:              confirmed.Fence,
		Capacity: taskworkspace.CleanupCapacity{
			Bytes:  taskworkspace.UnknownCleanupQuantity(),
			Inodes: taskworkspace.UnknownCleanupQuantity(),
		},
		EligibilityEvidenceRoot: taskworkspace.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Operation:               taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func claimCleanupDebtRequest(
	debt taskworkspace.CleanupDebt,
	expectedRetryGeneration taskworkspace.CleanupRetryGeneration,
	operationID taskworkspace.OperationID,
) taskworkspace.ClaimCleanupDebtRequest {
	request := taskworkspace.ClaimCleanupDebtRequest{
		PolicyDomainID:          debt.PolicyDomainID,
		TaskID:                  debt.TaskID,
		DebtID:                  debt.DebtID,
		ExpectedRetryGeneration: expectedRetryGeneration,
		Operation:               taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func reconcileCleanupDebtRequest(
	debt taskworkspace.CleanupDebt,
	operationID taskworkspace.OperationID,
) taskworkspace.ReconcileCleanupDebtRequest {
	request := taskworkspace.ReconcileCleanupDebtRequest{
		PolicyDomainID:  debt.PolicyDomainID,
		TaskID:          debt.TaskID,
		DebtID:          debt.DebtID,
		ClaimID:         debt.ClaimID,
		ClaimGeneration: debt.ClaimGeneration,
		RetryGeneration: debt.RetryGeneration,
		Generation:      debt.Generation,
		Fence:           debt.Fence,
		Operation:       taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func platformAdministratorAuthority(now taskworkspace.Instant) taskworkspace.PlatformAdministratorAuthority {
	authority := taskworkspace.PlatformAdministratorAuthority{
		ID:           "platform-administrator-1",
		AuthorityID:  "platform-administrator-authority-1",
		Generation:   3,
		ExpiresAt:    now + 1_000,
		EvidenceRoot: taskworkspace.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
	authority.Digest = authority.CanonicalDigest()
	return authority
}

func resolveAcceptedExceptionRequest(
	debt taskworkspace.CleanupDebt,
	authority taskworkspace.PlatformAdministratorAuthority,
	operationID taskworkspace.OperationID,
) taskworkspace.ResolveCleanupDebtRequest {
	request := taskworkspace.ResolveCleanupDebtRequest{
		PolicyDomainID:          debt.PolicyDomainID,
		TaskID:                  debt.TaskID,
		DebtID:                  debt.DebtID,
		ExpectedRetryGeneration: debt.RetryGeneration,
		Generation:              debt.Generation,
		Fence:                   debt.Fence,
		Resolution:              taskworkspace.CleanupAcceptedException,
		AdministratorAuthority:  authority,
		ClosedReason:            taskworkspace.CleanupExceptionUnsafeToReclaim,
		Duration:                20,
		EvidenceRoot: taskworkspace.Digest(
			"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		),
		Operation: taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func reopenCleanupDebtRequest(
	debt taskworkspace.CleanupDebt,
	authority taskworkspace.PlatformAdministratorAuthority,
	operationID taskworkspace.OperationID,
) taskworkspace.ReopenCleanupDebtRequest {
	request := taskworkspace.ReopenCleanupDebtRequest{
		PolicyDomainID:               debt.PolicyDomainID,
		TaskID:                       debt.TaskID,
		DebtID:                       debt.DebtID,
		ExpectedResolutionGeneration: debt.ResolutionGeneration,
		Generation:                   debt.Generation,
		Fence:                        debt.Fence,
		AdministratorAuthority:       authority,
		EvidenceRoot: taskworkspace.Digest(
			"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		),
		Operation: taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func commitCutoverAuthority() taskworkspace.CommitCutoverAuthority {
	authority := taskworkspace.CommitCutoverAuthority{
		ID:                "commit-cutover-authority-1",
		MigrationID:       "legacy-migration-1",
		AuthorityID:       "legacy-migration-authority-1",
		CutoverGeneration: 7,
		Fence:             9,
		EvidenceRoot: taskworkspace.Digest(
			"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		),
		CommittedAt: 90,
	}
	authority.Digest = authority.CanonicalDigest()
	return authority
}

func legacyCleanupObligationRequest(
	workspace taskworkspace.ConfirmTaskWorkspaceResult,
	cutover taskworkspace.CommitCutoverAuthority,
	operationID taskworkspace.OperationID,
) taskworkspace.AcceptLegacyCleanupObligationRequest {
	request := taskworkspace.AcceptLegacyCleanupObligationRequest{
		PolicyDomainID:         "policy-domain-1",
		TaskID:                 "task-1",
		TaskWorkspaceID:        workspace.TaskWorkspaceID,
		LegacyObligationNumber: 41,
		Owner:                  taskworkspace.CleanupOwnerC04,
		ResourceClass:          taskworkspace.CleanupWorkspaceResidue,
		ResourceID:             "opaque-legacy-cleanup-resource-1",
		ResourceGeneration:     "opaque-legacy-cleanup-generation-1",
		SourceAuthority:        taskworkspace.LegacyCleanupOpaqueObligation,
		CutoverGeneration:      cutover.CutoverGeneration,
		Generation:             workspace.Generation,
		Fence:                  workspace.Fence,
		Capacity: taskworkspace.CleanupCapacity{
			Bytes:  taskworkspace.UnknownCleanupQuantity(),
			Inodes: taskworkspace.UnknownCleanupQuantity(),
		},
		EligibilityEvidenceRoot: taskworkspace.Digest(
			"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		),
		CommitCutoverAuthority: cutover,
		Operation:              taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

type exactGenerationCleanupDouble struct {
	inspectCalls          int
	cleanupCalls          int
	events                []string
	inspectionDisposition taskworkspace.CleanupInspectionDisposition
	inspectionSequence    []taskworkspace.CleanupInspectionDisposition
	attemptOutcome        taskworkspace.CleanupResolutionClass
	attemptError          error
	referenceState        taskworkspace.CleanupAuthorityState
	leaseState            taskworkspace.CleanupAuthorityState
	graceState            taskworkspace.CleanupAuthorityState
	incidentState         taskworkspace.CleanupAuthorityState
	quarantineState       taskworkspace.CleanupAuthorityState
	blockers              []taskworkspace.CleanupBlocker
	mutateInspection      func(*taskworkspace.CleanupInspectionEvidence)
}

func (d *exactGenerationCleanupDouble) InspectCleanup(
	_ context.Context,
	request taskworkspace.CleanupInspectionRequest,
) (taskworkspace.CleanupInspectionEvidence, error) {
	d.inspectCalls++
	d.events = append(d.events, "inspect")
	evidence := taskworkspace.CleanupInspectionEvidence{
		ID:                 taskworkspace.CleanupEvidenceID("cleanup-inspection-" + request.Operation.ID),
		PolicyDomainID:     request.PolicyDomainID,
		TaskID:             request.TaskID,
		TaskWorkspaceID:    request.TaskWorkspaceID,
		DebtID:             request.DebtID,
		Owner:              request.Owner,
		ResourceClass:      request.ResourceClass,
		ResourceID:         request.ResourceID,
		ResourceGeneration: request.ResourceGeneration,
		RetryGeneration:    request.RetryGeneration,
		Generation:         request.Generation,
		Fence:              request.Fence,
		ReferenceState:     cleanupStateOrClear(d.referenceState),
		LeaseState:         cleanupStateOrClear(d.leaseState),
		GraceState:         cleanupStateOrClear(d.graceState),
		IncidentState:      cleanupStateOrClear(d.incidentState),
		QuarantineState:    cleanupStateOrClear(d.quarantineState),
		Disposition:        d.nextInspectionDisposition(),
		Blockers:           append([]taskworkspace.CleanupBlocker(nil), d.blockers...),
		Capacity: taskworkspace.CleanupCapacity{
			Bytes:  taskworkspace.UnknownCleanupQuantity(),
			Inodes: taskworkspace.UnknownCleanupQuantity(),
		},
		ObservedAt: 100,
	}
	evidence.Digest = evidence.CanonicalDigest()
	if d.mutateInspection != nil {
		d.mutateInspection(&evidence)
		evidence.Digest = evidence.CanonicalDigest()
	}
	return evidence, nil
}

func (d *exactGenerationCleanupDouble) ReclaimCleanup(
	_ context.Context,
	request taskworkspace.CleanupAttemptRequest,
) (taskworkspace.CleanupAttemptEvidence, error) {
	d.cleanupCalls++
	d.events = append(d.events, "reclaim")
	if d.attemptError != nil {
		return taskworkspace.CleanupAttemptEvidence{}, d.attemptError
	}
	evidence := taskworkspace.CleanupAttemptEvidence{
		ID:                       taskworkspace.CleanupEvidenceID("cleanup-attempt-" + request.Operation.ID),
		PolicyDomainID:           request.PolicyDomainID,
		TaskID:                   request.TaskID,
		TaskWorkspaceID:          request.TaskWorkspaceID,
		DebtID:                   request.DebtID,
		Owner:                    request.Owner,
		ResourceClass:            request.ResourceClass,
		ResourceID:               request.ResourceID,
		ResourceGeneration:       request.ResourceGeneration,
		RetryGeneration:          request.RetryGeneration,
		Generation:               request.Generation,
		Fence:                    request.Fence,
		InspectionEvidenceDigest: request.InspectionEvidenceDigest,
		Outcome:                  d.attemptOutcome,
		Capacity: taskworkspace.CleanupCapacity{
			Bytes:  taskworkspace.UnknownCleanupQuantity(),
			Inodes: taskworkspace.UnknownCleanupQuantity(),
		},
		ObservedAt: 100,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence, nil
}

func (d *exactGenerationCleanupDouble) nextInspectionDisposition() taskworkspace.CleanupInspectionDisposition {
	if len(d.inspectionSequence) == 0 {
		return d.inspectionDisposition
	}
	index := d.inspectCalls - 1
	if index >= len(d.inspectionSequence) {
		index = len(d.inspectionSequence) - 1
	}
	return d.inspectionSequence[index]
}

func cleanupStateOrClear(state taskworkspace.CleanupAuthorityState) taskworkspace.CleanupAuthorityState {
	if state == "" {
		return taskworkspace.CleanupAuthorityClear
	}
	return state
}

type cleanupAuditDouble struct {
	calls              int
	failure            error
	failureAfterCommit error
	skipCommit         bool
}

func (a *cleanupAuditDouble) RecordRequired(
	_ context.Context,
	transaction taskworkspace.CleanupAuditTransaction,
) error {
	a.calls++
	if a.failure != nil {
		return a.failure
	}
	intent := transaction.Intent()
	evidence := taskworkspace.CleanupAuditEvidence{
		ID:                   taskworkspace.CleanupAuditEvidenceID("cleanup-audit-" + intent.Operation.ID),
		Action:               intent.Action,
		DebtID:               intent.DebtID,
		AdministratorID:      intent.AdministratorAuthority.ID,
		AuthorityGeneration:  intent.AdministratorAuthority.Generation,
		Resolution:           intent.Resolution,
		ClosedReason:         intent.ClosedReason,
		Duration:             intent.Duration,
		DecisionEvidenceRoot: intent.DecisionEvidenceRoot,
		ResolutionGeneration: intent.ResolutionGeneration,
		OperationID:          intent.Operation.ID,
		RecordedAt:           100,
	}
	evidence.Digest = evidence.CanonicalDigest()
	if a.skipCommit {
		return nil
	}
	if err := transaction.Commit(evidence); err != nil {
		return err
	}
	return a.failureAfterCommit
}
