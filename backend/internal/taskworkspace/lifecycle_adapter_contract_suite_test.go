package taskworkspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestTaskWorkspaceLifecycleExternalContract(t *testing.T) {
	for _, adapter := range lifecycleContractAdapters() {
		adapter := adapter
		t.Run(adapter.name, func(t *testing.T) {
			runLifecycleAdapterExternalContract(t, adapter)
		})
	}
}

func runLifecycleAdapterExternalContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	runLifecycleIdentityCommitAndTerminalContract(t, adapter)
	runLifecycleIntegrityContract(t, adapter)
	runLifecycleResponseLossReconciliationContract(t, adapter)
	runLifecycleOwnershipAndFenceContract(t, adapter)
	runLifecycleExpiryAndRestoreContract(t, adapter)
	runLifecycleReconstructionContract(t, adapter)
	runLifecycleRetentionAndReclaimContract(t, adapter)
	runLifecycleRetentionRaceContract(t, adapter)
	runLifecycleCleanupDebtContract(t, adapter)
	runLifecycleAmbiguousCleanupRetryContract(t, adapter)
	runLifecycleMandatoryAuditContract(t, adapter)
	runLifecycleProjectionAndNonLeakageContract(t, adapter)
}

func runLifecycleAmbiguousCleanupRetryContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("ambiguous cleanup retains evidence and retries the same debt", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.CleanupRetryPolicy = taskworkspace.CleanupRetryPolicy{
			ClaimLifetime: 20, InitialBackoff: 10, MaximumBackoff: 100,
		}
		lifecycle, recoverCleanup := adapter.newAmbiguousCleanup(t, config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-ambiguous-cleanup-contract",
		))
		if err != nil {
			t.Fatalf("confirm cleanup workspace: %v", err)
		}
		debt, err := lifecycle.CreateCleanupObligation(context.Background(),
			createCleanupObligationRequest(confirmed, "create-ambiguous-cleanup-contract"))
		if err != nil {
			t.Fatalf("create cleanup obligation: %v", err)
		}
		claimed, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
			debt, debt.RetryGeneration, "claim-ambiguous-cleanup-contract",
		))
		if err != nil {
			t.Fatalf("claim cleanup debt: %v", err)
		}
		reconcile := reconcileCleanupDebtRequest(claimed, "reconcile-ambiguous-cleanup-contract")
		retry, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil {
			t.Fatalf("record ambiguous cleanup result: %v", err)
		}
		if retry.State != taskworkspace.CleanupDebtRetryScheduled ||
			retry.LastFailureCategory != taskworkspace.CleanupFailureAmbiguous ||
			retry.SafeFailureEvidenceRoot == "" || retry.DebtID != debt.DebtID {
			t.Fatalf("ambiguous cleanup retry = %#v", retry)
		}
		replayed, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil || !reflect.DeepEqual(replayed, retry) {
			t.Fatal("ambiguous cleanup exact replay changed evidence or DebtID")
		}

		recoverCleanup()
		now = retry.NextRetryAt
		reclaimedClaim, err := lifecycle.ClaimCleanupDebt(context.Background(), claimCleanupDebtRequest(
			retry, retry.RetryGeneration, "claim-ambiguous-cleanup-retry-contract",
		))
		if err != nil {
			t.Fatalf("reclaim retry generation: %v", err)
		}
		resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcileCleanupDebtRequest(
			reclaimedClaim, "reconcile-ambiguous-cleanup-retry-contract",
		))
		if err != nil || resolved.State != taskworkspace.CleanupDebtResolved ||
			resolved.Resolution != taskworkspace.CleanupAlreadyAbsent || resolved.DebtID != debt.DebtID {
			t.Fatalf("ambiguous cleanup retry resolution = %#v, err = %v", resolved, err)
		}
	})
}

func runLifecycleRetentionRaceContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("re-reference makes queued exact-generation cleanup stale", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.CheckpointRetentionPolicy = taskworkspace.CheckpointRetentionPolicy{
			ID: "contract-race-retention", ReclamationGrace: 10,
		}
		config.CheckpointReclamation = &checkpointReclamationMechanics{present: true}
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
			ID: "expiry-policy-1", MaterializationLifetime: 1, RuntimeViewLifetime: 100,
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-retention-race", "materialize-retention-race-base",
			"open-retention-race-1",
		)
		firstManifest := declaredStateManifest("content-1")
		older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, firstManifest, acceptedValidationEvidence(confirmed, view, firstManifest),
			"commit-retention-race-1",
		))
		if err != nil {
			t.Fatalf("commit older Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-retention-race-current-1",
		))
		if err != nil {
			t.Fatalf("confirm first committed state: %v", err)
		}
		physical, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-retention-race-1",
		))
		if err != nil {
			t.Fatalf("materialize older Checkpoint: %v", err)
		}
		materializedAuthority := current
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, physical,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-retention-race-2",
		))
		if err != nil {
			t.Fatalf("open second Runtime View: %v", err)
		}
		secondManifest := declaredStateManifest("content-2")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current, secondView, secondManifest, acceptedValidationEvidence(current, secondView, secondManifest),
			"commit-retention-race-2",
		)); err != nil {
			t.Fatalf("commit newer Checkpoint: %v", err)
		}
		now = 1_000
		if _, err := lifecycle.ExpireMaterialization(context.Background(), expireMaterializationRequest(
			materializedAuthority, physical, "expire-retention-race-materialization",
		)); err != nil {
			t.Fatalf("expire superseded physical materialization: %v", err)
		}
		current, err = lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-retention-race-current-2",
		))
		if err != nil {
			t.Fatalf("confirm superseding state: %v", err)
		}
		released := releaseFinalCheckpointAuthority(
			t, lifecycle, current, older.CheckpointID, "release-retention-race",
		)
		queued := reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "queued-retention-race",
		)
		attach := taskworkspace.AttachCheckpointRetentionRequest{
			PolicyDomainID: "policy-domain-1", TaskID: "task-1",
			TaskWorkspaceID: current.TaskWorkspaceID, CheckpointID: older.CheckpointID,
			ExpectedRetentionGeneration: released.RetentionGeneration,
			Generation:                  current.Generation, Fence: current.Fence,
			Authority: taskworkspace.CheckpointRetentionAuthority{
				ID: "contract-race-reference", Kind: taskworkspace.CheckpointExplicitReferenceAuthority,
			},
			Operation: taskworkspace.Operation{ID: "attach-retention-race"},
		}
		attach.Operation.RequestDigest = attach.CanonicalRequestDigest()
		if _, err := lifecycle.AttachCheckpointRetention(context.Background(), attach); err != nil {
			t.Fatalf("attach race-winning retention reference: %v", err)
		}
		_, err = lifecycle.ReclaimCheckpoint(context.Background(), queued)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	})
}

func runLifecycleOwnershipAndFenceContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("cross-scope evidence is non-leaking ownership denial", func(t *testing.T) {
		const canary = "/private/path/session-mount-object-locator-credential"
		lifecycle := adapter.new(t)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-cross-contract", "materialize-cross-contract", "open-cross-contract",
		)
		manifest := declaredStateManifest("content-1")
		base := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-cross-contract",
		)
		foreign, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy"+canary, "task"+canary, "confirm-foreign-contract",
		))
		if err != nil {
			t.Fatalf("confirm foreign workspace: %v", err)
		}
		cross := base
		cross.PolicyDomainID = "policy" + canary
		cross.TaskID = "task" + canary
		cross.TaskWorkspaceID = foreign.TaskWorkspaceID
		cross.Operation.RequestDigest = cross.CanonicalRequestDigest()
		crossResult, crossErr := lifecycle.CommitRuntimeView(context.Background(), cross)
		assertLifecycleErrorCode(t, crossErr, taskworkspace.ErrorOwnershipDenied)

		unknown := base
		unknown.PolicyDomainID = "policy" + canary
		unknown.TaskID = "unknown-task" + canary
		unknown.TaskWorkspaceID = "unknown-workspace" + canary
		unknown.Operation.RequestDigest = unknown.CanonicalRequestDigest()
		unknownResult, unknownErr := lifecycle.CommitRuntimeView(context.Background(), unknown)
		assertLifecycleErrorCode(t, unknownErr, taskworkspace.ErrorOwnershipDenied)
		if crossErr.Error() != unknownErr.Error() || crossResult.RevisionID != "" ||
			unknownResult.RevisionID != "" || strings.Contains(crossErr.Error(), canary) {
			t.Fatal("cross-scope evidence disclosed authority or returned lifecycle identity")
		}
	})

	t.Run("expired lease and recovery generation reject stale writers", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-expired-contract", "materialize-expired-contract", "open-expired-contract",
		)
		now = 300
		manifest := declaredStateManifest("content-1")
		_, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-expired-contract",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

		fresh := adapter.new(t)
		freshConfirmed, freshView := openRuntimeViewWithLifecycle(
			t, fresh, "task-1", "confirm-recovery-fence-contract",
			"materialize-recovery-fence-contract", "open-recovery-fence-contract",
		)
		if _, err := fresh.FenceRuntimeView(context.Background(), fenceRequest(
			freshConfirmed, freshView, taskworkspace.RuntimeViewRecoveryGenerationAdvanced,
			"advance-recovery-contract",
		)); err != nil {
			t.Fatalf("advance recovery generation: %v", err)
		}
		manifest = declaredStateManifest("content-1")
		_, err = fresh.CommitRuntimeView(context.Background(), commitRequest(
			freshConfirmed, freshView, manifest,
			acceptedValidationEvidence(freshConfirmed, freshView, manifest),
			"commit-pre-recovery-contract",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	})
}

func runLifecycleResponseLossReconciliationContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("response loss reconciles and exact replay activates once", func(t *testing.T) {
		faulted := false
		config := taskworkspaceTestConfig(nil)
		config.FaultHook = func(event taskworkspace.FaultEvent) error {
			if !faulted && event.OperationID == "commit-response-loss-contract" &&
				event.Point == taskworkspace.FaultBeforeResponse {
				faulted = true
				return errors.New("simulated response loss")
			}
			return nil
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-response-loss-contract",
			"materialize-response-loss-contract", "open-response-loss-contract",
		)
		manifest := declaredStateManifest("content-1")
		request := commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-response-loss-contract",
		)
		result, err := lifecycle.CommitRuntimeView(context.Background(), request)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
		if !faulted || result.RevisionID != "" || result.CheckpointID != "" {
			t.Fatal("response loss exposed an unacknowledged terminal result")
		}
		inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
			PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
			OperationID: request.Operation.ID,
		})
		if err != nil || inspection.Disposition != taskworkspace.OperationTerminal ||
			inspection.CommitRuntimeView == nil {
			t.Fatalf("response-loss inspection = %#v, err = %v", inspection, err)
		}
		replayed, err := lifecycle.CommitRuntimeView(context.Background(), request)
		if err != nil || !reflect.DeepEqual(replayed, *inspection.CommitRuntimeView) ||
			replayed.RevisionID == "" || replayed.CheckpointID == "" {
			t.Fatalf("response-loss replay = %#v, err = %v", replayed, err)
		}
	})
}

func runLifecycleMandatoryAuditContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("mandatory cleanup audit fails closed", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		administrator := platformAdministratorAuthority(now)
		audit := &cleanupAuditDouble{failure: errors.New("audit unavailable")}
		externalAudit := &auditDeliveryDouble{}
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.CleanupAudit = audit
		config.AuditDelivery = externalAudit
		config.PlatformAdministratorAuthorityID = "platform-administrator-authority-1"
		config.CurrentPlatformAdministratorAuthority = func(
			id taskworkspace.PlatformAdministratorID,
		) (taskworkspace.PlatformAdministratorAuthority, bool) {
			return administrator, id == administrator.ID
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-audit-contract"),
		)
		if err != nil {
			t.Fatalf("confirm workspace for mandatory audit: %v", err)
		}
		debt, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(confirmed, "create-audit-contract"),
		)
		if err != nil {
			t.Fatalf("create Cleanup Debt for mandatory audit: %v", err)
		}
		_, err = lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			debt, administrator, "resolve-audit-contract-failed",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
		stillOpen, err := lifecycle.InspectCleanupDebt(context.Background(), taskworkspace.InspectCleanupDebtRequest{
			PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, DebtID: debt.DebtID,
		})
		if err != nil || stillOpen.State != taskworkspace.CleanupDebtOpen || stillOpen.Resolution != "" {
			t.Fatalf("mandatory audit failure changed Cleanup Debt: %#v, err = %v", stillOpen, err)
		}

		audit.failure = nil
		resolved, err := lifecycle.ResolveCleanupDebt(context.Background(), resolveAcceptedExceptionRequest(
			debt, administrator, "resolve-audit-contract",
		))
		if err != nil || resolved.State != taskworkspace.CleanupDebtResolved ||
			resolved.Resolution != taskworkspace.CleanupAcceptedException ||
			resolved.ResolutionAuditEvidenceRoot == "" || audit.calls != 2 || externalAudit.calls != 1 {
			t.Fatalf("audited Cleanup Debt resolution = %#v, err = %v", resolved, err)
		}
		backlog, err := lifecycle.RebuildAuditDelivery(
			context.Background(), taskworkspace.AuditDeliveryRebuildRequest{},
		)
		if err != nil || !backlog.Pending.Known || backlog.Pending.Value != 0 ||
			!backlog.Delivered.Known || backlog.Delivered.Value != 1 || len(backlog.Evidence) != 1 {
			t.Fatalf("rebuilt audit delivery backlog = %#v, err = %v", backlog, err)
		}
	})
}

func runLifecycleReconstructionContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("reconstruct exact Artifact Version without changing authoritative history", func(t *testing.T) {
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		reconstruction := &reconstructionInputDouble{}
		config := taskworkspaceTestConfig(nil)
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.ReconstructionInput = reconstruction
		config.CurrentRecoveryIntent = func(
			id taskworkspace.RecoveryIntentID,
		) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, recoveryIntent.ID == id
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-reconstruct-1",
		))
		if err != nil {
			t.Fatalf("open reconstruction base: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-contract-reconstruct-1",
		)); err != nil {
			t.Fatalf("commit reconstruction base: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-contract-reconstruct-1"),
		)
		if err != nil {
			t.Fatalf("confirm reconstruction base: %v", err)
		}
		recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-contract-artifact-1")
		request := taskworkspace.ReconstructTaskWorkspaceRequest{
			Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "reconstruct-contract-artifact-1"},
		}
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		reconstructed, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
		if err != nil || reconstructed.TaskWorkspaceID != current.TaskWorkspaceID ||
			reconstructed.CurrentRevisionID != current.CurrentRevisionID ||
			reconstructed.CurrentCheckpointID != current.CurrentCheckpointID ||
			reconstructed.Generation != current.Generation+1 || reconstructed.Fence != current.Fence+1 ||
			reconstruction.artifactVerifications != 1 {
			t.Fatalf("reconstruct exact Artifact Version = %#v, err = %v", reconstructed, err)
		}
		replayed, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
		if err != nil || !reflect.DeepEqual(replayed, reconstructed) || reconstruction.artifactVerifications != 1 {
			t.Fatalf("reconstruction replay = %#v, err = %v", replayed, err)
		}
	})
}

func runLifecycleIdentityCommitAndTerminalContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("identity commit discard idempotency and fencing", func(t *testing.T) {
		lifecycle := adapter.new(t)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		reconfirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-again"),
		)
		if err != nil || reconfirmed.TaskWorkspaceID != confirmed.TaskWorkspaceID {
			t.Fatal("Task Workspace identity was not stable")
		}
		firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-contract-view-1",
		))
		if err != nil {
			t.Fatalf("open first Runtime View: %v", err)
		}
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-contract-view-2",
		))
		if err != nil || secondView.RuntimeViewID == firstView.RuntimeViewID {
			t.Fatal("mutating Runtime Views were not isolated")
		}
		manifest := declaredStateManifest("content-1")
		validation := acceptedValidationEvidence(confirmed, firstView, manifest)
		commit := commitRequest(confirmed, firstView, manifest, validation, "commit-contract-1")
		first, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("commit first Runtime View: %v", err)
		}
		replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil || !reflect.DeepEqual(replayed, first) {
			t.Fatal("exact commit replay changed the terminal decision")
		}
		conflictManifest := declaredStateManifest("content-2")
		conflict := commitRequest(
			confirmed, firstView, conflictManifest,
			acceptedValidationEvidence(confirmed, firstView, conflictManifest),
			string(commit.Operation.ID),
		)
		_, err = lifecycle.CommitRuntimeView(context.Background(), conflict)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)

		secondManifest := declaredStateManifest("content-2")
		secondValidation := acceptedValidationEvidence(confirmed, secondView, secondManifest)
		_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, secondView, secondManifest, secondValidation, "commit-contract-stale",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

		fresh := adapter.new(t)
		freshConfirmed, freshMaterialized := materializedTaskUsing(t, fresh)
		view, err := fresh.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", freshConfirmed, freshMaterialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-discard-contract",
		))
		if err != nil {
			t.Fatalf("open discard Runtime View: %v", err)
		}
		discard := discardRequest(freshConfirmed, view, taskworkspace.RuntimeViewValidationRejected, "discard-contract")
		firstDiscard, err := fresh.DiscardRuntimeView(context.Background(), discard)
		if err != nil {
			t.Fatalf("discard Runtime View: %v", err)
		}
		replayedDiscard, err := fresh.DiscardRuntimeView(context.Background(), discard)
		if err != nil || !reflect.DeepEqual(replayedDiscard, firstDiscard) {
			t.Fatal("exact discard replay changed the terminal decision")
		}
		manifest = declaredStateManifest("content-1")
		_, err = fresh.CommitRuntimeView(context.Background(), commitRequest(
			freshConfirmed, view, manifest, acceptedValidationEvidence(freshConfirmed, view, manifest),
			"commit-after-discard-contract",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorViewTerminalConflict)
	})
}

func runLifecycleIntegrityContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	for _, test := range []struct {
		name string
		edit func(*taskworkspace.DeclaredStateManifest)
	}{
		{"parent traversal", func(manifest *taskworkspace.DeclaredStateManifest) {
			manifest.Members[0].LogicalMember = "../escape"
		}},
		{"symbolic link", func(manifest *taskworkspace.DeclaredStateManifest) {
			manifest.Members[0].Type = taskworkspace.StateMemberSymbolicLink
		}},
		{"unsafe member type", func(manifest *taskworkspace.DeclaredStateManifest) {
			manifest.Members[0].Type = "device"
		}},
	} {
		test := test
		t.Run("integrity "+test.name, func(t *testing.T) {
			lifecycle := adapter.new(t)
			confirmed, view := openRuntimeViewWithLifecycle(
				t, lifecycle, "task-1", "confirm-integrity", "materialize-integrity", "open-integrity",
			)
			manifest := declaredStateManifest("content-1")
			test.edit(&manifest)
			manifest.Digest = manifest.CanonicalDigest()
			result, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
				confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
				"commit-integrity-"+strings.ReplaceAll(test.name, " ", "-"),
			))
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
			if result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("unsafe member returned authoritative identities")
			}
		})
	}
}

func runLifecycleExpiryAndRestoreContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("expire and restore exact Checkpoint", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
			ID: "expiry-policy-1", MaterializationLifetime: 10, RuntimeViewLifetime: 10,
		}
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, recoveryIntent.ID == id && id != ""
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-restore", "materialize-restore-base", "open-restore",
		)
		manifest := declaredStateManifest("content-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-restore",
		))
		if err != nil {
			t.Fatalf("commit restore Checkpoint: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-restore-current"),
		)
		if err != nil {
			t.Fatalf("confirm current state: %v", err)
		}
		materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-restore-current",
		))
		if err != nil {
			t.Fatalf("materialize committed state: %v", err)
		}
		now = 110
		if _, err := lifecycle.ExpireMaterialization(
			context.Background(), expireMaterializationRequest(current, materialized, "expire-contract"),
		); err != nil {
			t.Fatalf("expire materialization: %v", err)
		}
		recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-contract")
		restore := taskworkspace.RestoreTaskWorkspaceRequest{
			Intent: recoveryIntent, Operation: taskworkspace.Operation{ID: "restore-contract"},
		}
		restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
		restored, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
		if err != nil {
			t.Fatalf("restore exact Checkpoint: %v", err)
		}
		if restored.TaskWorkspaceID != current.TaskWorkspaceID || restored.RevisionID != committed.RevisionID ||
			restored.CheckpointID != committed.CheckpointID || restored.Generation != current.Generation+1 ||
			restored.Fence != current.Fence+1 || restored.MaterializationID == materialized.MaterializationID {
			t.Fatal("restore changed history, reused physical identity, or omitted the new recovery fence")
		}
	})
}

func runLifecycleRetentionAndReclaimContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("retention grace and exact-generation reclaim", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(nil)
		config.Now = func() taskworkspace.Instant { return now }
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 1, RuntimeViewLifetime: 100}
		config.CheckpointRetentionPolicy = taskworkspace.CheckpointRetentionPolicy{
			ID: "contract-retention", ReclamationGrace: 10,
		}
		config.CheckpointReclamation = &checkpointReclamationMechanics{present: true}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-reclaim", "materialize-reclaim-base", "open-reclaim-1",
		)
		firstManifest := declaredStateManifest("content-1")
		older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, firstManifest, acceptedValidationEvidence(confirmed, view, firstManifest),
			"commit-reclaim-1",
		))
		if err != nil {
			t.Fatalf("commit older Checkpoint: %v", err)
		}
		current, _ := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reclaim-current-1"),
		)
		physical, err := lifecycle.Materialize(context.Background(), materializeRequest(
			"policy-domain-1", "task-1", current, "materialize-reclaim-1",
		))
		if err != nil {
			t.Fatalf("materialize older Checkpoint: %v", err)
		}
		secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", current, physical,
			"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-reclaim-2",
		))
		if err != nil {
			t.Fatalf("open second Runtime View: %v", err)
		}
		secondManifest := declaredStateManifest("content-2")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			current, secondView, secondManifest, acceptedValidationEvidence(current, secondView, secondManifest),
			"commit-reclaim-2",
		)); err != nil {
			t.Fatalf("commit newer Checkpoint: %v", err)
		}
		now = 1_000
		if _, err := lifecycle.ExpireMaterialization(
			context.Background(), expireMaterializationRequest(current, physical, "expire-reclaim-materialization"),
		); err != nil {
			t.Fatalf("expire old physical materialization: %v", err)
		}
		current, _ = lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-reclaim-current-2"),
		)
		released := releaseFinalCheckpointAuthority(
			t, lifecycle, current, older.CheckpointID, "release-reclaim-contract",
		)
		now = released.EligibleAt
		reclaim := reclaimCheckpointRequest(
			current, older.CheckpointID, released.RetentionGeneration, "reclaim-contract",
		)
		first, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
		if err != nil {
			t.Fatalf("reclaim exact Checkpoint generation: %v", err)
		}
		if first.Outcome != taskworkspace.CheckpointReclaimed && first.Outcome != taskworkspace.CheckpointAlreadyAbsent {
			t.Fatalf("reclaim outcome = %q", first.Outcome)
		}
		replayed, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
		if err != nil || !reflect.DeepEqual(replayed, first) {
			t.Fatal("repeated exact-generation reclaim was not idempotent")
		}
	})
}

func runLifecycleCleanupDebtContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("Cleanup Debt exact replay and resolution", func(t *testing.T) {
		config := taskworkspaceTestConfig(nil)
		config.Cleanup = &exactGenerationCleanupDouble{
			inspectionDisposition: taskworkspace.CleanupInspectionAlreadyAbsent,
		}
		lifecycle := adapter.newConfigured(t, config)
		confirmed, err := lifecycle.ConfirmTaskWorkspace(
			context.Background(), confirmRequest("policy-domain-1", "task-1", "confirm-cleanup-contract"),
		)
		if err != nil {
			t.Fatalf("confirm workspace for cleanup: %v", err)
		}
		debt, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-contract"),
		)
		if err != nil {
			t.Fatalf("create Cleanup Debt: %v", err)
		}
		createReplay, err := lifecycle.CreateCleanupObligation(
			context.Background(), createCleanupObligationRequest(confirmed, "create-cleanup-contract"),
		)
		if err != nil || createReplay.DebtID != debt.DebtID {
			t.Fatalf("Cleanup Debt creation replay = %#v, err = %v", createReplay, err)
		}
		claimed, err := lifecycle.ClaimCleanupDebt(
			context.Background(), claimCleanupDebtRequest(debt, debt.RetryGeneration, "claim-cleanup-contract"),
		)
		if err != nil {
			t.Fatalf("claim Cleanup Debt: %v", err)
		}
		reconcile := reconcileCleanupDebtRequest(claimed, "reconcile-cleanup-contract")
		resolved, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil {
			t.Fatalf("reconcile Cleanup Debt: %v", err)
		}
		if resolved.State != taskworkspace.CleanupDebtResolved ||
			(resolved.Resolution != taskworkspace.CleanupAlreadyAbsent && resolved.Resolution != taskworkspace.CleanupReclaimed) {
			t.Fatalf("Cleanup Debt resolution = %#v", resolved)
		}
		replayed, err := lifecycle.ReconcileCleanupDebt(context.Background(), reconcile)
		if err != nil || !reflect.DeepEqual(replayed, resolved) {
			t.Fatal("Cleanup Debt reconciliation exact replay changed its result")
		}
	})
}

func runLifecycleProjectionAndNonLeakageContract(t *testing.T, adapter lifecycleContractAdapter) {
	t.Helper()
	t.Run("projection outage and non-leakage", func(t *testing.T) {
		projection := &failingLifecycleProjection{err: errors.New("path-do-not-disclose/session-do-not-disclose")}
		config := taskworkspaceTestConfig(nil)
		config.Projection = projection
		lifecycle := adapter.newConfigured(t, config)
		confirmed, view := openRuntimeViewWithLifecycle(
			t, lifecycle, "task-1", "confirm-projection", "materialize-projection", "open-projection",
		)
		manifest := declaredStateManifest("content-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest),
			"commit-projection-contract",
		))
		if err != nil {
			t.Fatalf("projection outage rolled back committed lifecycle fact: %v", err)
		}
		if len(projection.envelopes) == 0 {
			t.Fatal("committed lifecycle fact was not offered to the projection")
		}
		encoded, err := json.Marshal(struct {
			Result      taskworkspace.CommitRuntimeViewResult
			Projections []taskworkspace.ProjectionEnvelope
		}{committed, projection.envelopes})
		if err != nil {
			t.Fatalf("encode external evidence: %v", err)
		}
		for _, forbidden := range []string{"path-do-not-disclose", "session-do-not-disclose", "object-key", "bucket", "mount"} {
			if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
				t.Fatalf("external lifecycle evidence leaked %q", forbidden)
			}
		}
	})
}

type failingLifecycleProjection struct {
	err       error
	envelopes []taskworkspace.ProjectionEnvelope
}

func (p *failingLifecycleProjection) Project(_ context.Context, envelope taskworkspace.ProjectionEnvelope) error {
	p.envelopes = append(p.envelopes, envelope)
	return p.err
}

func (p *failingLifecycleProjection) Rebuild(
	_ context.Context,
	_ taskworkspace.ProjectionSchemaRevision,
	envelopes []taskworkspace.ProjectionEnvelope,
) error {
	p.envelopes = append(p.envelopes, envelopes...)
	return p.err
}
