package taskworkspace_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestPhysicalMaterializationExpiryPreservesAuthoritativeHistory(t *testing.T) {
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID:                      "expiry-policy-1",
		MaterializationLifetime: 10,
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)

	now = 110
	expire := taskworkspace.ExpireMaterializationRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		MaterializationID: materialized.MaterializationID,
		RevisionID:        confirmed.CurrentRevisionID,
		CheckpointID:      confirmed.CurrentCheckpointID,
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		ExpiryPolicyID:    "expiry-policy-1",
		Operation:         taskworkspace.Operation{ID: "expire-materialization-1"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	expired, err := lifecycle.ExpireMaterialization(context.Background(), expire)
	if err != nil {
		t.Fatalf("expire materialization: %v", err)
	}
	if expired.MaterializationID != materialized.MaterializationID || expired.ExpiredAt != now {
		t.Fatal("expiry result is not bound to the exact physical materialization and clock")
	}

	afterExpiry, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-expiry",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace after expiry: %v", err)
	}
	if afterExpiry != confirmed {
		t.Fatal("physical expiry changed Task Workspace identity or authoritative history")
	}

	rebuilt, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", afterExpiry, "materialize-after-expiry",
	))
	if err != nil {
		t.Fatalf("rebuild materialization: %v", err)
	}
	if rebuilt.MaterializationID == materialized.MaterializationID {
		t.Fatal("rebuild reused an expired physical materialization identity")
	}
}

func TestHistoricalMaterializationExpiresByItsExactPhysicalGeneration(t *testing.T) {
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}

	now = 300
	expired, err := lifecycle.ExpireMaterialization(
		context.Background(),
		expireMaterializationRequest(confirmed, materialized, "expire-historical-materialization"),
	)
	if err != nil {
		t.Fatalf("expire historical materialization: %v", err)
	}
	if expired.RevisionID != confirmed.CurrentRevisionID || expired.CheckpointID != confirmed.CurrentCheckpointID ||
		expired.Generation != confirmed.Generation || expired.Fence != confirmed.Fence {
		t.Fatal("expiry was not bound to the historical materialization's exact physical generation")
	}
	afterExpiry, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-historical-expiry",
	))
	if err != nil {
		t.Fatalf("confirm after historical expiry: %v", err)
	}
	if afterExpiry != current {
		t.Fatal("historical physical expiry changed authoritative history")
	}
}

func TestExpiryUsesTheResourceBoundPolicyVersionAfterModulePolicyChange(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-v1", MaterializationLifetime: 10}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-v2", MaterializationLifetime: 100}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	now = 300
	expireView := taskworkspace.ExpireRuntimeViewRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		RuntimeViewID:     view.RuntimeViewID,
		MaterializationID: materialized.MaterializationID,
		BaseRevisionID:    confirmed.CurrentRevisionID,
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		ExpiryPolicyID:    "expiry-policy-v1",
		Operation:         taskworkspace.Operation{ID: "expire-view-under-v1"},
	}
	expireView.Operation.RequestDigest = expireView.CanonicalRequestDigest()
	if _, err := restarted.ExpireRuntimeView(context.Background(), expireView); err != nil {
		t.Fatalf("expire v1 Runtime View under v2 module policy: %v", err)
	}
	expireMaterialization := expireMaterializationRequest(
		confirmed, materialized, "expire-materialization-under-v1",
	)
	expireMaterialization.ExpiryPolicyID = "expiry-policy-v1"
	expireMaterialization.Operation.RequestDigest = expireMaterialization.CanonicalRequestDigest()
	if _, err := restarted.ExpireMaterialization(context.Background(), expireMaterialization); err != nil {
		t.Fatalf("expire v1 materialization under v2 module policy: %v", err)
	}
}

func TestVersionedPolicyBoundsCallerRuntimeViewDeadline(t *testing.T) {
	now := taskworkspace.Instant(100)
	leaseCurrent := true
	lease := sandboxLeaseAuthority(
		"policy-domain-1", "task-1", "phase-run-1", "runtime-run-1", "sandbox-lease-1",
	)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID:                      "expiry-policy-1",
		MaterializationLifetime: 1_000,
		RuntimeViewLifetime:     10,
	}
	config.CurrentSandboxLeaseAuthority = func(id taskworkspace.SandboxLeaseID) (taskworkspace.SandboxLeaseAuthority, bool) {
		return lease, leaseCurrent && id == lease.ID
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	early := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-too-early",
	)
	early.ExpiresAt = 109
	early.Operation.RequestDigest = early.CanonicalRequestDigest()
	_, err := lifecycle.OpenRuntimeView(context.Background(), early)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

	late := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-too-late",
	)
	late.ExpiresAt = 250
	late.Operation.RequestDigest = late.CanonicalRequestDigest()
	view, err := lifecycle.OpenRuntimeView(context.Background(), late)
	if err != nil {
		t.Fatalf("open policy-bounded Runtime View: %v", err)
	}
	if view.ExpiresAt != 110 {
		t.Fatalf("Runtime View expiry = %d, want policy-derived deadline 110", view.ExpiresAt)
	}
	expire := taskworkspace.ExpireRuntimeViewRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		RuntimeViewID:     view.RuntimeViewID,
		MaterializationID: materialized.MaterializationID,
		BaseRevisionID:    confirmed.CurrentRevisionID,
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		ExpiryPolicyID:    "expiry-policy-1",
		Operation:         taskworkspace.Operation{ID: "expire-view-before-policy-deadline"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	leaseCurrent = false
	now = 109
	_, err = lifecycle.ExpireRuntimeView(context.Background(), expire)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
	leaseCurrent = true
	now = 110
	expire.Operation.ID = "expire-view-at-policy-deadline-with-active-lease"
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	_, err = lifecycle.ExpireRuntimeView(context.Background(), expire)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
	leaseCurrent = false
	expire.Operation.ID = "expire-view-after-lease-release"
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	if _, err := lifecycle.ExpireRuntimeView(context.Background(), expire); err != nil {
		t.Fatalf("expire Runtime View after lease release: %v", err)
	}
}

func TestRuntimeViewDeadlineDecisionSurvivesRestartAndPolicyChange(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	faultAt := taskworkspace.FaultAfterIntentPersistence
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID:                      "expiry-policy-v1",
		MaterializationLifetime: 1_000,
		RuntimeViewLifetime:     10,
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.OperationID == "open-view-under-v1" && event.Point == faultAt {
			faultAt = ""
			return errors.New("simulated crash after policy decision persistence")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	request := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-under-v1",
	)
	request.ExpiresAt = 250
	request.Operation.RequestDigest = request.CanonicalRequestDigest()

	_, err := lifecycle.OpenRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)

	now = 101
	restartedConfig := config
	restartedConfig.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID:                      "expiry-policy-v2",
		MaterializationLifetime: 1_000,
		RuntimeViewLifetime:     100,
	}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	view, err := restarted.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("resume Runtime View creation under changed module policy: %v", err)
	}
	if view.ExpiresAt != 110 {
		t.Fatalf("Runtime View expiry = %d, want persisted v1 deadline 110", view.ExpiresAt)
	}
}

func TestActiveRuntimeViewLeaseBlocksMaterializationExpiry(t *testing.T) {
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID:                      "expiry-policy-1",
		MaterializationLifetime: 10,
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	if _, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	)); err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}

	now = 110
	_, err := lifecycle.ExpireMaterialization(
		context.Background(),
		expireMaterializationRequest(confirmed, materialized, "expire-materialization-1"),
	)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
}

func TestExpiredRuntimeViewRejectsLateCommitWithoutChangingHistory(t *testing.T) {
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{
		ID:                      "expiry-policy-1",
		MaterializationLifetime: 1_000,
		RuntimeViewLifetime:     100,
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}

	now = view.SandboxLeaseAuthority.ExpiresAt
	expire := taskworkspace.ExpireRuntimeViewRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		RuntimeViewID:     view.RuntimeViewID,
		MaterializationID: materialized.MaterializationID,
		BaseRevisionID:    confirmed.CurrentRevisionID,
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		ExpiryPolicyID:    "expiry-policy-1",
		Operation:         taskworkspace.Operation{ID: "expire-view-1"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	if _, err := lifecycle.ExpireRuntimeView(context.Background(), expire); err != nil {
		t.Fatalf("expire Runtime View: %v", err)
	}

	manifest := declaredStateManifest("content-1")
	commit := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"late-commit-after-view-expiry",
	)
	_, err = lifecycle.CommitRuntimeView(context.Background(), commit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)

	afterExpiry, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-view-expiry",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace after view expiry: %v", err)
	}
	if afterExpiry != confirmed {
		t.Fatal("Runtime View expiry changed authoritative history")
	}
}

func TestPreRecoveryRuntimeViewExpiresByItsExactPhysicalGeneration(t *testing.T) {
	now := taskworkspace.Instant(100)
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		firstView,
		manifest,
		acceptedValidationEvidence(confirmed, firstView, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit first Runtime View: %v", err)
	}
	beforeRecovery, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-before-recovery",
	))
	if err != nil {
		t.Fatalf("confirm before recovery: %v", err)
	}
	beforeRecoveryMaterialization, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", beforeRecovery, "materialize-before-recovery",
	))
	if err != nil {
		t.Fatalf("materialize before recovery: %v", err)
	}
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", beforeRecovery, beforeRecoveryMaterialization,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-pre-recovery-view",
	))
	if err != nil {
		t.Fatalf("open pre-recovery Runtime View: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(beforeRecovery, "recovery-intent-1")
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-current-1"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	if _, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore); err != nil {
		t.Fatalf("restore Task Workspace: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-restore",
	))
	if err != nil {
		t.Fatalf("confirm after restore: %v", err)
	}

	now = 300
	expire := taskworkspace.ExpireRuntimeViewRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   beforeRecovery.TaskWorkspaceID,
		RuntimeViewID:     view.RuntimeViewID,
		MaterializationID: beforeRecoveryMaterialization.MaterializationID,
		BaseRevisionID:    beforeRecovery.CurrentRevisionID,
		Generation:        beforeRecovery.Generation,
		Fence:             beforeRecovery.Fence,
		ExpiryPolicyID:    "default-expiry-policy",
		Operation:         taskworkspace.Operation{ID: "expire-pre-recovery-view"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	expired, err := lifecycle.ExpireRuntimeView(context.Background(), expire)
	if err != nil {
		t.Fatalf("expire pre-recovery Runtime View: %v", err)
	}
	if expired.Generation != beforeRecovery.Generation || expired.Fence != beforeRecovery.Fence {
		t.Fatal("Runtime View expiry was not bound to the view's exact physical generation")
	}
	afterExpiry, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-pre-recovery-view-expiry",
	))
	if err != nil {
		t.Fatalf("confirm after pre-recovery view expiry: %v", err)
	}
	if afterExpiry != current {
		t.Fatal("pre-recovery Runtime View expiry changed authoritative history")
	}
}

func TestNodeIndependentRestoreReconstructsCurrentRevisionInANewRecoveryGeneration(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &happyDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	))
	if err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm committed Task Workspace: %v", err)
	}
	currentMaterialization, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-current",
	))
	if err != nil {
		t.Fatalf("materialize committed Revision: %v", err)
	}

	now = 110
	if _, err := lifecycle.ExpireMaterialization(
		context.Background(),
		expireMaterializationRequest(current, currentMaterialization, "expire-current"),
	); err != nil {
		t.Fatalf("expire current materialization: %v", err)
	}

	intent := authorizedCheckpointRestoreIntent(current, "recovery-intent-1")
	restartedConfig := taskworkspaceTestConfig(durable)
	restartedConfig.Persistence = persistence
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.ExpiryPolicy = config.ExpiryPolicy
	restartedConfig.RecoveryAuthorityID = "recovery-authority-1"
	restartedConfig.CurrentRecoveryIntents = []taskworkspace.AuthorizedRecoveryIntent{intent}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    intent,
		Operation: taskworkspace.Operation{ID: "restore-current-1"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	restored, err := restarted.RestoreTaskWorkspace(context.Background(), restore)
	if err != nil {
		t.Fatalf("restore Task Workspace: %v", err)
	}
	if restored.MaterializationID == currentMaterialization.MaterializationID {
		t.Fatal("node-independent restore reused the expired physical identity")
	}
	if restored.TaskWorkspaceID != current.TaskWorkspaceID || restored.RevisionID != committed.RevisionID ||
		restored.CheckpointID != committed.CheckpointID {
		t.Fatal("restore changed Task Workspace identity or authoritative history")
	}
	if restored.Generation != current.Generation+1 || restored.Fence != current.Fence+1 {
		t.Fatal("restore did not advance the recovery generation and fence")
	}

	afterRestore, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-restore",
	))
	if err != nil {
		t.Fatalf("confirm after restore: %v", err)
	}
	if afterRestore.TaskWorkspaceID != current.TaskWorkspaceID ||
		afterRestore.CurrentRevisionID != current.CurrentRevisionID ||
		afterRestore.CurrentCheckpointID != current.CurrentCheckpointID ||
		afterRestore.Generation != restored.Generation || afterRestore.Fence != restored.Fence {
		t.Fatal("authoritative state after restore is inconsistent")
	}
}

func TestRestoreUsesAuthorizedOlderExactRevisionCheckpointWithoutRewritingCurrentAuthority(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	durable := &happyDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	firstManifest := declaredStateManifest("content-1")
	older, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		firstView,
		firstManifest,
		acceptedValidationEvidence(confirmed, firstView, firstManifest),
		"commit-older",
	))
	if err != nil {
		t.Fatalf("commit older Revision: %v", err)
	}
	afterOlder, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-older",
	))
	if err != nil {
		t.Fatalf("confirm after older Revision: %v", err)
	}
	olderMaterialization, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", afterOlder, "materialize-older",
	))
	if err != nil {
		t.Fatalf("materialize older Revision: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", afterOlder, olderMaterialization,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}
	secondManifest := declaredStateManifest("content-2")
	newer, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		afterOlder,
		secondView,
		secondManifest,
		acceptedValidationEvidence(afterOlder, secondView, secondManifest),
		"commit-newer",
	))
	if err != nil {
		t.Fatalf("commit newer Revision: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	if current.CurrentRevisionID != newer.RevisionID || current.CurrentCheckpointID != newer.CheckpointID {
		t.Fatal("test setup did not establish a newer current authority")
	}

	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-older")
	recoveryIntent.TargetRevisionID = older.RevisionID
	recoveryIntent.TargetCheckpointID = older.CheckpointID
	recoveryIntent.Digest = recoveryIntent.CanonicalDigest()
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-authorized-older"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	restored, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
	if err != nil {
		t.Fatalf("restore authorized older target: %v", err)
	}
	if restored.RevisionID != older.RevisionID || restored.CheckpointID != older.CheckpointID {
		t.Fatal("restore did not use the exact Platform-selected older target")
	}
	if restored.CurrentRevisionID != current.CurrentRevisionID ||
		restored.CurrentCheckpointID != current.CurrentCheckpointID {
		t.Fatal("older physical restore rewrote current authoritative history")
	}
	if restored.Generation != current.Generation+1 || restored.Fence != current.Fence+1 {
		t.Fatal("older physical restore did not advance recovery generation and fence")
	}
	afterRestore, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-older-restore",
	))
	if err != nil {
		t.Fatalf("confirm after older restore: %v", err)
	}
	if afterRestore.CurrentRevisionID != current.CurrentRevisionID ||
		afterRestore.CurrentCheckpointID != current.CurrentCheckpointID {
		t.Fatal("authorized older target became current without a validated commit")
	}
}

func TestCheckpointRestoreMaterializesImmutableInputsFromIndependentReadOnlyCapabilities(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	reconstruction := &reconstructionInputDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	))
	if err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-with-inputs")
	recoveryIntent.ReadOnlyInputs = []taskworkspace.ReadOnlyInputCapability{
		readOnlyInputCapability(taskworkspace.ImmutableInputRuntimeRelease, "runtime-release-1"),
		readOnlyInputCapability(taskworkspace.ImmutableInputTemplateVersion, "template-version-1"),
		readOnlyInputCapability(taskworkspace.ImmutableInputResourceBundle, "resource-bundle-1"),
		readOnlyInputCapability(taskworkspace.ImmutableInputSourceMaterial, "source-material-1"),
	}
	recoveryIntent.Digest = recoveryIntent.CanonicalDigest()
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-with-read-only-inputs"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	restored, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
	if err != nil {
		t.Fatalf("restore with immutable inputs: %v", err)
	}
	if len(restored.ReadOnlyInputs) != len(recoveryIntent.ReadOnlyInputs) ||
		reconstruction.inputMaterializations != len(recoveryIntent.ReadOnlyInputs) {
		t.Fatal("restore omitted independent immutable input materializations")
	}
	for index, input := range restored.ReadOnlyInputs {
		if input.Access != taskworkspace.InputAccessReadOnly ||
			input.CapabilityID != recoveryIntent.ReadOnlyInputs[index].ID {
			t.Fatal("restore did not preserve exact read-only capability binding")
		}
	}
	for _, member := range committed.CheckpointEvidence.Manifest.Members {
		if member.Class != taskworkspace.StateMemberTaskOwnedMutable {
			t.Fatal("immutable input was copied into the Checkpoint")
		}
	}
	afterRestore, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-restore-with-inputs",
	))
	if err != nil {
		t.Fatalf("confirm after restore: %v", err)
	}
	restoredView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", afterRestore,
		taskworkspace.MaterializeResult{MaterializationID: restored.MaterializationID},
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-restored-view",
	))
	if err != nil {
		t.Fatalf("open restored Runtime View: %v", err)
	}
	if !reflect.DeepEqual(restoredView.ReadOnlyInputs, restored.ReadOnlyInputs) {
		t.Fatal("restored Runtime View lost immutable input capability materializations")
	}
}

func TestCheckpointRestoreFailsClosedForMissingOrCorruptExactDependency(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*happyDurableObject, *taskworkspace.AuthorizedRecoveryIntent)
	}{
		{
			name: "missing exact Checkpoint",
			configure: func(_ *happyDurableObject, intent *taskworkspace.AuthorizedRecoveryIntent) {
				intent.TargetRevisionID = "revision-missing"
				intent.TargetCheckpointID = "checkpoint-missing"
				intent.Digest = intent.CanonicalDigest()
			},
		},
		{
			name: "corrupt verified Checkpoint content",
			configure: func(durable *happyDurableObject, _ *taskworkspace.AuthorizedRecoveryIntent) {
				durable.verifyMutate = func(content *taskworkspace.VerifiedCheckpointContent) {
					content.Manifest.Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
			durable := &happyDurableObject{}
			config := taskworkspaceTestConfig(durable)
			config.RecoveryAuthorityID = "recovery-authority-1"
			config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
				return recoveryIntent, recoveryIntent.ID == id
			}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, materialized := materializedTaskUsing(t, lifecycle)
			view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
				"policy-domain-1", "task-1", confirmed, materialized,
				"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
			))
			if err != nil {
				t.Fatalf("open Runtime View: %v", err)
			}
			manifest := declaredStateManifest("content-1")
			if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
				confirmed,
				view,
				manifest,
				acceptedValidationEvidence(confirmed, view, manifest),
				"commit-1",
			)); err != nil {
				t.Fatalf("commit Runtime View: %v", err)
			}
			current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-current",
			))
			if err != nil {
				t.Fatalf("confirm current Task Workspace: %v", err)
			}
			recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-fail-closed")
			test.configure(durable, &recoveryIntent)
			restore := taskworkspace.RestoreTaskWorkspaceRequest{
				Intent:    recoveryIntent,
				Operation: taskworkspace.Operation{ID: "restore-fail-closed"},
			}
			restore.Operation.RequestDigest = restore.CanonicalRequestDigest()

			result, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
			if result.MaterializationID != "" {
				t.Fatal("failed restore returned physical materialization authority")
			}
			afterFailure, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-after-failed-restore",
			))
			if err != nil {
				t.Fatalf("confirm after failed restore: %v", err)
			}
			if afterFailure != current {
				t.Fatal("missing or corrupt dependency advanced workspace authority")
			}
		})
	}
}

func TestCheckpointRestoreRejectsIncompleteOrTamperedAuthorizedTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*taskworkspace.AuthorizedRecoveryIntent)
	}{
		{
			name: "incomplete exact target",
			mutate: func(intent *taskworkspace.AuthorizedRecoveryIntent) {
				intent.TargetCheckpointID = ""
				intent.Digest = intent.CanonicalDigest()
			},
		},
		{
			name: "target changed after authorization",
			mutate: func(intent *taskworkspace.AuthorizedRecoveryIntent) {
				intent.TargetRevisionID = "revision-not-authorized"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.RecoveryAuthorityID = "recovery-authority-1"
			config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
				return recoveryIntent, recoveryIntent.ID == id
			}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, materialized := materializedTaskUsing(t, lifecycle)
			view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
				"policy-domain-1", "task-1", confirmed, materialized,
				"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
			))
			if err != nil {
				t.Fatalf("open Runtime View: %v", err)
			}
			manifest := declaredStateManifest("content-1")
			if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
				confirmed,
				view,
				manifest,
				acceptedValidationEvidence(confirmed, view, manifest),
				"commit-1",
			)); err != nil {
				t.Fatalf("commit Runtime View: %v", err)
			}
			current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-current",
			))
			if err != nil {
				t.Fatalf("confirm current Task Workspace: %v", err)
			}
			recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-invalid-target")
			requestIntent := recoveryIntent
			test.mutate(&requestIntent)
			restore := taskworkspace.RestoreTaskWorkspaceRequest{
				Intent:    requestIntent,
				Operation: taskworkspace.Operation{ID: "restore-invalid-target"},
			}
			restore.Operation.RequestDigest = restore.CanonicalRequestDigest()

			_, err = lifecycle.RestoreTaskWorkspace(context.Background(), restore)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorInvalidIntent)
			afterFailure, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-after-invalid-target",
			))
			if err != nil {
				t.Fatalf("confirm after invalid target: %v", err)
			}
			if afterFailure != current {
				t.Fatal("incomplete or tampered restore target advanced authority")
			}
		})
	}
}

func TestRestoreRejectsEveryPreRecoveryWriterAsStale(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	durable := &happyDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	firstManifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		firstView,
		firstManifest,
		acceptedValidationEvidence(confirmed, firstView, firstManifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit first Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	currentMaterialization, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-current",
	))
	if err != nil {
		t.Fatalf("materialize current Revision: %v", err)
	}
	staleView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, currentMaterialization,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-stale-view",
	))
	if err != nil {
		t.Fatalf("open pre-recovery Runtime View: %v", err)
	}

	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-1")
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-current-1"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	if _, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore); err != nil {
		t.Fatalf("restore Task Workspace: %v", err)
	}

	lateManifest := declaredStateManifest("content-2")
	lateCommit := commitRequest(
		current,
		staleView,
		lateManifest,
		acceptedValidationEvidence(current, staleView, lateManifest),
		"late-pre-recovery-commit",
	)
	_, err = lifecycle.CommitRuntimeView(context.Background(), lateCommit)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
}

func TestManualEditReconstructsFromExactArtifactVersionAndReturnsValidatedExportEvidence(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	durable := &happyDurableObject{}
	reconstruction := &reconstructionInputDouble{}
	config := taskworkspaceTestConfig(durable)
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	firstManifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		firstView,
		firstManifest,
		acceptedValidationEvidence(confirmed, firstView, firstManifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit first Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}

	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-artifact-1")
	reconstruct := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-manual-edit-1"},
	}
	reconstruct.Operation.RequestDigest = reconstruct.CanonicalRequestDigest()
	reconstructed, err := lifecycle.ReconstructTaskWorkspace(context.Background(), reconstruct)
	if err != nil {
		t.Fatalf("reconstruct Task Workspace from Artifact Version: %v", err)
	}
	if reconstructed.TaskWorkspaceID != current.TaskWorkspaceID ||
		reconstructed.CurrentRevisionID != current.CurrentRevisionID ||
		reconstructed.CurrentCheckpointID != current.CurrentCheckpointID {
		t.Fatal("Artifact Version reconstruction changed authoritative history")
	}
	if reconstructed.ArtifactVersionID != recoveryIntent.ArtifactVersionInput.ArtifactVersionID ||
		reconstructed.ArtifactManifestDigest != recoveryIntent.ArtifactVersionInput.ManifestDigest {
		t.Fatal("reconstruction did not consume the exact Platform-selected Artifact Version")
	}
	if reconstructed.Generation != current.Generation+1 || reconstructed.Fence != current.Fence+1 {
		t.Fatal("manual-edit reconstruction did not advance recovery generation and fence")
	}
	if len(reconstructed.ReadOnlyInputs) != len(recoveryIntent.ReadOnlyInputs) {
		t.Fatal("reconstruction omitted immutable input materializations")
	}
	for index, input := range reconstructed.ReadOnlyInputs {
		if input.Access != taskworkspace.InputAccessReadOnly ||
			input.CapabilityID != recoveryIntent.ReadOnlyInputs[index].ID {
			t.Fatal("immutable input was not materialized through its independent read-only capability")
		}
	}

	afterReconstruction, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-reconstruction",
	))
	if err != nil {
		t.Fatalf("confirm after reconstruction: %v", err)
	}
	reconstructedMaterialization := taskworkspace.MaterializeResult{
		MaterializationID: reconstructed.MaterializationID,
	}
	manualEditView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", afterReconstruction, reconstructedMaterialization,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-manual-edit-view",
	))
	if err != nil {
		t.Fatalf("open manual-edit Runtime View: %v", err)
	}
	if len(manualEditView.ReadOnlyInputs) != len(reconstructed.ReadOnlyInputs) {
		t.Fatal("Runtime View did not retain independent immutable input capabilities")
	}

	manualEditManifest := declaredStateManifest("content-2")
	committed, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		afterReconstruction,
		manualEditView,
		manualEditManifest,
		acceptedValidationEvidence(afterReconstruction, manualEditView, manualEditManifest),
		"commit-manual-edit-1",
	))
	if err != nil {
		t.Fatalf("commit manual edit: %v", err)
	}
	if committed.RevisionID == current.CurrentRevisionID || committed.CheckpointID == current.CurrentCheckpointID {
		t.Fatal("successful manual edit did not create a new Revision and Checkpoint")
	}
	if committed.ValidatedExportEvidence.ID == "" ||
		committed.ValidatedExportEvidence.PublicationAuthorityID != recoveryIntent.PublicationAuthorityID ||
		committed.ValidatedExportEvidence.SourceArtifactVersionID != recoveryIntent.ArtifactVersionInput.ArtifactVersionID ||
		committed.ValidatedExportEvidence.RevisionID != committed.RevisionID ||
		committed.ValidatedExportEvidence.CheckpointID != committed.CheckpointID ||
		committed.ValidatedExportEvidence.ManifestDigest != committed.ManifestDigest {
		t.Fatal("manual edit did not return exact location-independent validated export evidence")
	}
	for _, member := range committed.CheckpointEvidence.Manifest.Members {
		if member.Class != taskworkspace.StateMemberTaskOwnedMutable {
			t.Fatal("immutable reconstruction input was copied into the Checkpoint")
		}
	}
}

func TestRuntimeViewReplayIsIsolatedFromCallerMutationOfReadOnlyInputs(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = &reconstructionInputDouble{}
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-replay-isolation")
	reconstruct := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-replay-isolation"},
	}
	reconstruct.Operation.RequestDigest = reconstruct.CanonicalRequestDigest()
	reconstructed, err := lifecycle.ReconstructTaskWorkspace(context.Background(), reconstruct)
	if err != nil {
		t.Fatalf("reconstruct Task Workspace: %v", err)
	}
	afterReconstruction, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-reconstruction",
	))
	if err != nil {
		t.Fatalf("confirm after reconstruction: %v", err)
	}
	open := openRuntimeViewRequest(
		"policy-domain-1", "task-1", afterReconstruction,
		taskworkspace.MaterializeResult{MaterializationID: reconstructed.MaterializationID},
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-replay-isolation",
	)
	first, err := lifecycle.OpenRuntimeView(context.Background(), open)
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	wantCapability := first.ReadOnlyInputs[0].CapabilityID
	first.ReadOnlyInputs[0].CapabilityID = "caller-mutated-capability"

	replayed, err := lifecycle.OpenRuntimeView(context.Background(), open)
	if err != nil {
		t.Fatalf("replay Runtime View open: %v", err)
	}
	if replayed.ReadOnlyInputs[0].CapabilityID != wantCapability {
		t.Fatal("caller mutation corrupted exact Runtime View replay")
	}
}

func TestRecoveryDegradedReadOnlyFenceRejectsReconstructionBeforeInputMaterialization(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	reconstruction := &reconstructionInputDouble{}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-read-only")
	recoveryIntent.Mode = taskworkspace.RecoveryModeDegradedReadOnly
	recoveryIntent.Digest = recoveryIntent.CanonicalDigest()
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-read-only"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()

	_, err = lifecycle.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorRecoveryReadOnly)
	if reconstruction.artifactVerifications != 0 || reconstruction.inputMaterializations != 0 {
		t.Fatal("read-only fence allowed reconstruction input materialization")
	}
}

func TestRecoveryDegradedReadOnlyFenceRejectsCheckpointRestoreBeforeDependencyVerification(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	durable := &happyDurableObject{}
	reconstruction := &reconstructionInputDouble{}
	config := taskworkspaceTestConfig(durable)
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-read-only-restore")
	recoveryIntent.ReadOnlyInputs = []taskworkspace.ReadOnlyInputCapability{
		readOnlyInputCapability(taskworkspace.ImmutableInputRuntimeRelease, "runtime-release-1"),
	}
	recoveryIntent.Mode = taskworkspace.RecoveryModeDegradedReadOnly
	recoveryIntent.Digest = recoveryIntent.CanonicalDigest()
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-read-only"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()

	_, err = lifecycle.RestoreTaskWorkspace(context.Background(), restore)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorRecoveryReadOnly)
	if durable.verified != 0 || reconstruction.inputMaterializations != 0 {
		t.Fatal("read-only recovery fence allowed Checkpoint dependency work")
	}
	after, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-read-only-restore",
	))
	if err != nil {
		t.Fatalf("confirm after read-only restore rejection: %v", err)
	}
	if after != current {
		t.Fatal("read-only restore rejection advanced workspace authority")
	}
}

func TestIncompleteArtifactReconstructionEvidenceFailsClosed(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	reconstruction := &reconstructionInputDouble{omitArtifactOperationBinding: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	lifecycle := taskworkspace.NewInMemory(config)
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-incomplete")
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-incomplete"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()

	result, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
	if result.MaterializationID != "" {
		t.Fatal("incomplete reconstruction evidence returned a materialization")
	}
	after, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-incomplete",
	))
	if err != nil {
		t.Fatalf("confirm after rejected reconstruction: %v", err)
	}
	if after != current {
		t.Fatal("incomplete reconstruction evidence advanced workspace authority")
	}
}

func TestArtifactReconstructionFailsClosedForMissingOrCorruptReadOnlyDependency(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*reconstructionInputDouble)
	}{
		{
			name: "missing immutable dependency",
			configure: func(input *reconstructionInputDouble) {
				input.inputError = errors.New("immutable dependency missing")
			},
		},
		{
			name: "corrupt immutable dependency evidence",
			configure: func(input *reconstructionInputDouble) {
				input.mutateReadOnlyInput = func(materialized *taskworkspace.ReadOnlyInputMaterialization) {
					materialized.Access = taskworkspace.InputAccess("writable")
					materialized.Digest = materialized.CanonicalDigest()
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
			reconstruction := &reconstructionInputDouble{}
			test.configure(reconstruction)
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.RecoveryAuthorityID = "recovery-authority-1"
			config.ReconstructionInput = reconstruction
			config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
				return recoveryIntent, recoveryIntent.ID == id
			}
			lifecycle := taskworkspace.NewInMemory(config)
			current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-1",
			))
			if err != nil {
				t.Fatalf("confirm Task Workspace: %v", err)
			}
			recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-input-failure")
			request := taskworkspace.ReconstructTaskWorkspaceRequest{
				Intent:    recoveryIntent,
				Operation: taskworkspace.Operation{ID: "reconstruct-input-failure"},
			}
			request.Operation.RequestDigest = request.CanonicalRequestDigest()

			result, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
			if result.MaterializationID != "" {
				t.Fatal("failed dependency materialization returned workspace authority")
			}
			calls := reconstruction.inputMaterializations
			_, err = lifecycle.ReconstructTaskWorkspace(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
			if reconstruction.inputMaterializations != calls {
				t.Fatal("terminal dependency failure replay repeated physical input work")
			}
			after, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
				"policy-domain-1", "task-1", "confirm-after-input-failure",
			))
			if err != nil {
				t.Fatalf("confirm after dependency failure: %v", err)
			}
			if after != current {
				t.Fatal("missing or corrupt immutable dependency advanced workspace authority")
			}
		})
	}
}

func TestReconstructionReplayAfterModuleRestartDoesNotAdvanceRecoveryTwice(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	reconstruction := &reconstructionInputDouble{}
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	lostResponse := true
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if lostResponse && event.Point == taskworkspace.FaultBeforeResponse && event.OperationID == "reconstruct-response-loss" {
			lostResponse = false
			return errors.New("response lost")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-replay")
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-response-loss"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()

	_, err = lifecycle.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	afterLoss, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-loss",
	))
	if err != nil {
		t.Fatalf("confirm after response loss: %v", err)
	}
	if afterLoss.Generation != current.Generation+1 || afterLoss.Fence != current.Fence+1 {
		t.Fatal("reconstruction did not linearize before response loss")
	}
	verificationCalls := reconstruction.artifactVerifications
	inputCalls := reconstruction.inputMaterializations

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.RecoveryAuthorityID = "recovery-authority-1"
	restartedConfig.ReconstructionInput = reconstruction
	restartedConfig.CurrentRecoveryIntents = []taskworkspace.AuthorizedRecoveryIntent{recoveryIntent}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	replayed, err := restarted.ReconstructTaskWorkspace(context.Background(), request)
	if err != nil {
		t.Fatalf("replay reconstruction after restart: %v", err)
	}
	replayedAgain, err := restarted.ReconstructTaskWorkspace(context.Background(), request)
	if err != nil {
		t.Fatalf("replay reconstruction again: %v", err)
	}
	if !reflect.DeepEqual(replayedAgain, replayed) {
		t.Fatal("exact replay did not return the original reconstruction result")
	}
	if reconstruction.artifactVerifications != verificationCalls || reconstruction.inputMaterializations != inputCalls {
		t.Fatal("replay repeated physical reconstruction input work")
	}
	afterReplay, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-replay",
	))
	if err != nil {
		t.Fatalf("confirm after replay: %v", err)
	}
	if afterReplay.Generation != afterLoss.Generation || afterReplay.Fence != afterLoss.Fence {
		t.Fatal("replay advanced recovery authority a second time")
	}
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect reconstruction operation: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal || inspection.ReconstructTaskWorkspace == nil ||
		!reflect.DeepEqual(*inspection.ReconstructTaskWorkspace, replayed) {
		t.Fatal("operation inspection omitted terminal reconstruction projection")
	}
}

func TestArtifactReconstructionReconcilesPersistedIntentAfterModuleRestart(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	reconstruction := &reconstructionInputDouble{}
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterIntentPersistence && event.OperationID == "reconstruct-persisted-intent" {
			return errors.New("crash after reconstruction intent")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-reconcile")
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-persisted-intent"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	_, err = lifecycle.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if reconstruction.artifactVerifications != 0 || reconstruction.inputMaterializations != 0 {
		t.Fatal("reconstruction acted before persisted intent recovery")
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.RecoveryAuthorityID = "recovery-authority-1"
	restartedConfig.ReconstructionInput = reconstruction
	restartedConfig.CurrentRecoveryIntents = []taskworkspace.AuthorizedRecoveryIntent{recoveryIntent}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile Artifact Version reconstruction: %v", err)
	}
	if reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.ReconstructTaskWorkspace == nil ||
		reconciled.ReconstructTaskWorkspace.RecoveryIntentID != recoveryIntent.ID {
		t.Fatal("reconciliation omitted terminal Artifact Version reconstruction result")
	}
	after, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-reconstruction-reconcile",
	))
	if err != nil {
		t.Fatalf("confirm after reconstruction reconciliation: %v", err)
	}
	if after.Generation != current.Generation+1 || after.Fence != current.Fence+1 ||
		after.CurrentRevisionID != current.CurrentRevisionID || after.CurrentCheckpointID != current.CurrentCheckpointID {
		t.Fatal("reconciled reconstruction did not preserve history in one new recovery generation")
	}
}

func TestArtifactReconstructionReconcilesAfterCrashBeforeAuthoritativeTransaction(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	reconstruction := &reconstructionInputDouble{}
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = reconstruction
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultBeforeAuthoritativeTransaction &&
			event.OperationID == "reconstruct-before-transaction" {
			return errors.New("crash before reconstruction transaction")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-before-transaction")
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "reconstruct-before-transaction"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	_, err = lifecycle.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	afterCrash, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-reconstruction-crash",
	))
	if err != nil {
		t.Fatalf("confirm after reconstruction crash: %v", err)
	}
	if afterCrash != current {
		t.Fatal("crash before authoritative transaction advanced recovery authority")
	}
	inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect interrupted reconstruction: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.IntentState != taskworkspace.OperationIntentVerified {
		t.Fatal("verified reconstruction did not remain reconciliation-required before activation")
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.RecoveryAuthorityID = "recovery-authority-1"
	restartedConfig.ReconstructionInput = reconstruction
	restartedConfig.CurrentRecoveryIntents = []taskworkspace.AuthorizedRecoveryIntent{recoveryIntent}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile reconstruction after transaction crash: %v", err)
	}
	if reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.ReconstructTaskWorkspace == nil {
		t.Fatal("reconciliation omitted activated reconstruction result")
	}
}

func TestPendingCommitOperationBlocksMaterializationExpiry(t *testing.T) {
	now := taskworkspace.Instant(100)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterIntentPersistence && event.OperationID == "pending-commit" {
			return errors.New("pause after commit intent")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"pending-commit",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)

	now = 300
	_, err = lifecycle.ExpireMaterialization(
		context.Background(),
		expireMaterializationRequest(confirmed, materialized, "expire-during-commit"),
	)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
}

func TestPendingReconstructionOperationBlocksMaterializationExpiry(t *testing.T) {
	now := taskworkspace.Instant(100)
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.ReconstructionInput = &reconstructionInputDouble{}
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterIntentPersistence && event.OperationID == "pending-reconstruction" {
			return errors.New("pause after reconstruction intent")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	recoveryIntent = authorizedArtifactReconstructionIntent(confirmed, "recovery-intent-pending")
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "pending-reconstruction"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	_, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)

	now = 110
	_, err = lifecycle.ExpireMaterialization(
		context.Background(),
		expireMaterializationRequest(confirmed, materialized, "expire-during-reconstruction"),
	)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
}

func TestPendingRestoreOperationBlocksMaterializationExpiry(t *testing.T) {
	now := taskworkspace.Instant(100)
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterIntentPersistence && event.OperationID == "pending-restore" {
			return errors.New("pause after restore intent")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	currentMaterialization, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-current",
	))
	if err != nil {
		t.Fatalf("materialize current Task Workspace: %v", err)
	}
	currentView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, currentMaterialization,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-current-view",
	))
	if err != nil {
		t.Fatalf("open current Runtime View: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-pending-restore")
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "pending-restore"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	_, err = lifecycle.RestoreTaskWorkspace(context.Background(), restore)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)

	now = 110
	_, err = lifecycle.ExpireMaterialization(
		context.Background(),
		expireMaterializationRequest(current, currentMaterialization, "expire-during-restore"),
	)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)

	now = 300
	expireView := taskworkspace.ExpireRuntimeViewRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   current.TaskWorkspaceID,
		RuntimeViewID:     currentView.RuntimeViewID,
		MaterializationID: currentMaterialization.MaterializationID,
		BaseRevisionID:    current.CurrentRevisionID,
		Generation:        current.Generation,
		Fence:             current.Fence,
		ExpiryPolicyID:    "expiry-policy-1",
		Operation:         taskworkspace.Operation{ID: "expire-view-during-restore"},
	}
	expireView.Operation.RequestDigest = expireView.CanonicalRequestDigest()
	_, err = lifecycle.ExpireRuntimeView(context.Background(), expireView)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
}

func TestIntegrityIncidentAndExplicitRetentionReferenceBlockExpiry(t *testing.T) {
	for _, test := range []struct {
		name       string
		protection taskworkspace.ExpiryProtection
	}{
		{
			name: "integrity incident",
			protection: taskworkspace.ExpiryProtection{
				IntegrityIncidentID: "integrity-incident-1",
			},
		},
		{
			name: "explicit retention reference",
			protection: taskworkspace.ExpiryProtection{
				RetentionReferenceID: "retention-reference-1",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := taskworkspace.Instant(100)
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Now = func() taskworkspace.Instant { return now }
			config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
			config.ExpiryProtection = expiryProtectionDouble{protection: test.protection}
			lifecycle := taskworkspace.NewInMemory(config)
			confirmed, materialized := materializedTaskUsing(t, lifecycle)

			now = 110
			_, err := lifecycle.ExpireMaterialization(
				context.Background(),
				expireMaterializationRequest(confirmed, materialized, "expire-protected"),
			)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorExpiryBlocked)
		})
	}
}

func TestMaterializationExpiryExactReplaySurvivesModuleRestart(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	now = 110
	request := expireMaterializationRequest(confirmed, materialized, "expire-replay")
	first, err := lifecycle.ExpireMaterialization(context.Background(), request)
	if err != nil {
		t.Fatalf("expire materialization: %v", err)
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.ExpiryPolicy = config.ExpiryPolicy
	restarted := taskworkspace.NewInMemory(restartedConfig)
	replayed, err := restarted.ExpireMaterialization(context.Background(), request)
	if err != nil {
		t.Fatalf("replay materialization expiry after restart: %v", err)
	}
	if replayed != first {
		t.Fatal("expiry replay did not return the original terminal result")
	}
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect materialization expiry: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal || inspection.ExpireMaterialization == nil ||
		*inspection.ExpireMaterialization != first {
		t.Fatal("operation inspection omitted terminal materialization expiry projection")
	}
}

func TestRuntimeViewExpiryExactReplaySurvivesModuleRestart(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	lostResponse := true
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if lostResponse && event.Point == taskworkspace.FaultBeforeResponse && event.OperationID == "expire-view-response-loss" {
			lostResponse = false
			return errors.New("response lost")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	now = 300
	expire := taskworkspace.ExpireRuntimeViewRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		RuntimeViewID:     view.RuntimeViewID,
		MaterializationID: materialized.MaterializationID,
		BaseRevisionID:    confirmed.CurrentRevisionID,
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		ExpiryPolicyID:    "default-expiry-policy",
		Operation:         taskworkspace.Operation{ID: "expire-view-response-loss"},
	}
	expire.Operation.RequestDigest = expire.CanonicalRequestDigest()
	_, err = lifecycle.ExpireRuntimeView(context.Background(), expire)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	afterLoss, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-view-expiry-loss",
	))
	if err != nil {
		t.Fatalf("confirm after Runtime View expiry response loss: %v", err)
	}
	if afterLoss != confirmed {
		t.Fatal("Runtime View expiry response loss changed authoritative history")
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restarted := taskworkspace.NewInMemory(restartedConfig)
	firstReplay, err := restarted.ExpireRuntimeView(context.Background(), expire)
	if err != nil {
		t.Fatalf("replay Runtime View expiry after restart: %v", err)
	}
	secondReplay, err := restarted.ExpireRuntimeView(context.Background(), expire)
	if err != nil {
		t.Fatalf("replay Runtime View expiry again: %v", err)
	}
	if secondReplay != firstReplay {
		t.Fatal("Runtime View expiry replay did not return the original terminal result")
	}
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    expire.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect Runtime View expiry: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal || inspection.ExpireRuntimeView == nil ||
		*inspection.ExpireRuntimeView != firstReplay {
		t.Fatal("operation inspection omitted terminal Runtime View expiry projection")
	}
}

func TestMaterializationExpiryReconcilesAfterCrashBeforePhysicalAction(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Persistence = persistence
	config.Now = func() taskworkspace.Instant { return now }
	config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultBeforePhysicalExpiry && event.OperationID == "expire-before-physical-crash" {
			return errors.New("crash before physical expiry")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	now = 110
	request := expireMaterializationRequest(confirmed, materialized, "expire-before-physical-crash")
	_, err := lifecycle.ExpireMaterialization(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	inspection, err := lifecycle.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect interrupted expiry: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationReconciliationRequired ||
		inspection.IntentState != taskworkspace.OperationIntentActing || inspection.ExpireMaterialization != nil {
		t.Fatal("interrupted expiry did not retain a typed reconciliation-required intent")
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Persistence = persistence
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.ExpiryPolicy = config.ExpiryPolicy
	restarted := taskworkspace.NewInMemory(restartedConfig)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile materialization expiry: %v", err)
	}
	if reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.ExpireMaterialization == nil ||
		reconciled.ExpireMaterialization.MaterializationID != materialized.MaterializationID {
		t.Fatal("reconciliation omitted terminal materialization expiry result")
	}
	after, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-expiry-reconcile",
	))
	if err != nil {
		t.Fatalf("confirm after expiry reconciliation: %v", err)
	}
	if after != confirmed {
		t.Fatal("reconciled materialization expiry changed authoritative history")
	}
}

func TestCheckpointRestoreReplayAfterModuleRestartDoesNotAdvanceRecoveryTwice(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &happyDurableObject{}
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	lostResponse := true
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if lostResponse && event.Point == taskworkspace.FaultBeforeResponse && event.OperationID == "restore-response-loss" {
			lostResponse = false
			return errors.New("response lost")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-replay")
	request := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-response-loss"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()

	_, err = lifecycle.RestoreTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	verifyCalls := durable.verified
	afterLoss, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-loss",
	))
	if err != nil {
		t.Fatalf("confirm after response loss: %v", err)
	}
	if afterLoss.Generation != current.Generation+1 || afterLoss.Fence != current.Fence+1 {
		t.Fatal("restore did not linearize before response loss")
	}

	restartedConfig := taskworkspaceTestConfig(durable)
	restartedConfig.Persistence = persistence
	restartedConfig.RecoveryAuthorityID = "recovery-authority-1"
	restartedConfig.CurrentRecoveryIntents = []taskworkspace.AuthorizedRecoveryIntent{recoveryIntent}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	firstReplay, err := restarted.RestoreTaskWorkspace(context.Background(), request)
	if err != nil {
		t.Fatalf("replay restore after restart: %v", err)
	}
	secondReplay, err := restarted.RestoreTaskWorkspace(context.Background(), request)
	if err != nil {
		t.Fatalf("replay restore again: %v", err)
	}
	if !reflect.DeepEqual(secondReplay, firstReplay) {
		t.Fatal("exact restore replay did not return the original result")
	}
	if durable.verified != verifyCalls {
		t.Fatal("restore replay repeated durable Checkpoint verification")
	}
	afterReplay, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-replay",
	))
	if err != nil {
		t.Fatalf("confirm after restore replay: %v", err)
	}
	if afterReplay.Generation != afterLoss.Generation || afterReplay.Fence != afterLoss.Fence {
		t.Fatal("restore replay advanced recovery authority twice")
	}
	inspection, err := restarted.InspectOperation(context.Background(), taskworkspace.InspectOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("inspect restore operation: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal || inspection.RestoreTaskWorkspace == nil ||
		!reflect.DeepEqual(*inspection.RestoreTaskWorkspace, firstReplay) {
		t.Fatal("operation inspection omitted terminal restore projection")
	}
}

func TestCheckpointRestoreReconcilesPersistedIntentAfterModuleRestart(t *testing.T) {
	persistence := taskworkspace.NewInMemoryPersistence()
	durable := &happyDurableObject{}
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	config := taskworkspaceTestConfig(durable)
	config.Persistence = persistence
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, recoveryIntent.ID == id
	}
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultAfterIntentPersistence && event.OperationID == "restore-persisted-intent" {
			return errors.New("crash after restore intent")
		}
		return nil
	}
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-1",
	)); err != nil {
		t.Fatalf("commit Runtime View: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-current",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-reconcile")
	request := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-persisted-intent"},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	_, err = lifecycle.RestoreTaskWorkspace(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if durable.verified != 0 {
		t.Fatal("restore verified dependencies before persisted intent recovery")
	}

	restartedConfig := taskworkspaceTestConfig(durable)
	restartedConfig.Persistence = persistence
	restartedConfig.RecoveryAuthorityID = "recovery-authority-1"
	restartedConfig.CurrentRecoveryIntents = []taskworkspace.AuthorizedRecoveryIntent{recoveryIntent}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	reconciled, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    request.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile Checkpoint restore: %v", err)
	}
	if reconciled.Disposition != taskworkspace.OperationTerminal || reconciled.RestoreTaskWorkspace == nil ||
		reconciled.RestoreTaskWorkspace.RecoveryIntentID != recoveryIntent.ID {
		t.Fatal("reconciliation omitted terminal Checkpoint restore result")
	}
	if durable.verified != 1 {
		t.Fatal("reconciled restore did not verify exact Checkpoint dependencies once")
	}
	after, err := restarted.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-restore-reconcile",
	))
	if err != nil {
		t.Fatalf("confirm after restore reconciliation: %v", err)
	}
	if after.Generation != current.Generation+1 || after.Fence != current.Fence+1 ||
		after.CurrentRevisionID != current.CurrentRevisionID || after.CurrentCheckpointID != current.CurrentCheckpointID {
		t.Fatal("reconciled restore did not preserve history in one new recovery generation")
	}
}

func TestExpiryAndRecoveryOperationsRejectSameOperationIDDifferentCanonicalDigest(t *testing.T) {
	t.Run("materialization expiry", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		config.ExpiryPolicy = taskworkspace.ExpiryPolicy{ID: "expiry-policy-1", MaterializationLifetime: 10}
		lifecycle := taskworkspace.NewInMemory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		now = 110
		request := expireMaterializationRequest(confirmed, materialized, "expire-digest-conflict")
		if _, err := lifecycle.ExpireMaterialization(context.Background(), request); err != nil {
			t.Fatalf("expire materialization: %v", err)
		}
		changed := request
		changed.Fence++
		changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
		_, err := lifecycle.ExpireMaterialization(context.Background(), changed)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("Runtime View expiry", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		lifecycle := taskworkspace.NewInMemory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		now = 300
		request := taskworkspace.ExpireRuntimeViewRequest{
			PolicyDomainID:    "policy-domain-1",
			TaskID:            "task-1",
			TaskWorkspaceID:   confirmed.TaskWorkspaceID,
			RuntimeViewID:     view.RuntimeViewID,
			MaterializationID: materialized.MaterializationID,
			BaseRevisionID:    confirmed.CurrentRevisionID,
			Generation:        confirmed.Generation,
			Fence:             confirmed.Fence,
			ExpiryPolicyID:    "default-expiry-policy",
			Operation:         taskworkspace.Operation{ID: "expire-view-digest-conflict"},
		}
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		if _, err := lifecycle.ExpireRuntimeView(context.Background(), request); err != nil {
			t.Fatalf("expire Runtime View: %v", err)
		}
		changed := request
		changed.Generation++
		changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
		_, err = lifecycle.ExpireRuntimeView(context.Background(), changed)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("Artifact Version reconstruction", func(t *testing.T) {
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.ReconstructionInput = &reconstructionInputDouble{}
		config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, recoveryIntent.ID == id
		}
		lifecycle := taskworkspace.NewInMemory(config)
		current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-1",
		))
		if err != nil {
			t.Fatalf("confirm Task Workspace: %v", err)
		}
		recoveryIntent = authorizedArtifactReconstructionIntent(current, "recovery-intent-digest-conflict")
		request := taskworkspace.ReconstructTaskWorkspaceRequest{
			Intent:    recoveryIntent,
			Operation: taskworkspace.Operation{ID: "reconstruct-digest-conflict"},
		}
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		if _, err := lifecycle.ReconstructTaskWorkspace(context.Background(), request); err != nil {
			t.Fatalf("reconstruct Task Workspace: %v", err)
		}
		changed := request
		changed.Intent.ExpiresAt++
		changed.Intent.Digest = changed.Intent.CanonicalDigest()
		changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
		_, err = lifecycle.ReconstructTaskWorkspace(context.Background(), changed)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("Checkpoint restore", func(t *testing.T) {
		var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.RecoveryAuthorityID = "recovery-authority-1"
		config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
			return recoveryIntent, recoveryIntent.ID == id
		}
		lifecycle := taskworkspace.NewInMemory(config)
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
			confirmed,
			view,
			manifest,
			acceptedValidationEvidence(confirmed, view, manifest),
			"commit-1",
		)); err != nil {
			t.Fatalf("commit Runtime View: %v", err)
		}
		current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
			"policy-domain-1", "task-1", "confirm-current",
		))
		if err != nil {
			t.Fatalf("confirm current Task Workspace: %v", err)
		}
		recoveryIntent = authorizedCheckpointRestoreIntent(current, "recovery-intent-digest-conflict")
		request := taskworkspace.RestoreTaskWorkspaceRequest{
			Intent:    recoveryIntent,
			Operation: taskworkspace.Operation{ID: "restore-digest-conflict"},
		}
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		if _, err := lifecycle.RestoreTaskWorkspace(context.Background(), request); err != nil {
			t.Fatalf("restore Task Workspace: %v", err)
		}
		changed := request
		changed.Intent.ExpiresAt++
		changed.Intent.Digest = changed.Intent.CanonicalDigest()
		changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
		_, err = lifecycle.RestoreTaskWorkspace(context.Background(), changed)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})
}

type expiryProtectionDouble struct {
	protection taskworkspace.ExpiryProtection
}

func (d expiryProtectionDouble) InspectExpiryProtection(
	_ context.Context,
	_ taskworkspace.InspectExpiryProtectionRequest,
) (taskworkspace.ExpiryProtection, error) {
	return d.protection, nil
}

func authorizedArtifactReconstructionIntent(
	current taskworkspace.ConfirmTaskWorkspaceResult,
	intentID string,
) taskworkspace.AuthorizedRecoveryIntent {
	artifactInput := taskworkspace.ArtifactVersionReconstructionInput{
		ID:                     "artifact-input-capability-1",
		PublicationAuthorityID: "publication-authority-1",
		PolicyDomainID:         "policy-domain-1",
		TaskID:                 "task-1",
		ArtifactVersionID:      "artifact-version-7",
		ManifestDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpiresAt:              200,
	}
	artifactInput.Digest = artifactInput.CanonicalDigest()
	readOnlyInputs := []taskworkspace.ReadOnlyInputCapability{
		readOnlyInputCapability(taskworkspace.ImmutableInputRuntimeRelease, "runtime-release-1"),
		readOnlyInputCapability(taskworkspace.ImmutableInputTemplateVersion, "template-version-1"),
		readOnlyInputCapability(taskworkspace.ImmutableInputResourceBundle, "resource-bundle-1"),
		readOnlyInputCapability(taskworkspace.ImmutableInputSourceMaterial, "source-material-1"),
	}
	intent := taskworkspace.AuthorizedRecoveryIntent{
		ID:                          taskworkspace.RecoveryIntentID(intentID),
		RecoveryAuthorityID:         "recovery-authority-1",
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		TargetKind:                  taskworkspace.RecoveryTargetArtifactVersion,
		ExpectedCurrentRevisionID:   current.CurrentRevisionID,
		ExpectedCurrentCheckpointID: current.CurrentCheckpointID,
		ArtifactVersionInput:        artifactInput,
		ReadOnlyInputs:              readOnlyInputs,
		PublicationAuthorityID:      "publication-authority-1",
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Mode:                        taskworkspace.RecoveryModeWritable,
		ExpiresAt:                   200,
	}
	intent.Digest = intent.CanonicalDigest()
	return intent
}

func readOnlyInputCapability(
	kind taskworkspace.ImmutableInputKind,
	inputID string,
) taskworkspace.ReadOnlyInputCapability {
	capability := taskworkspace.ReadOnlyInputCapability{
		ID:             taskworkspace.ReadOnlyInputCapabilityID("read-only-capability-" + inputID),
		AuthorityID:    "immutable-input-authority-1",
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		Kind:           kind,
		InputID:        taskworkspace.ImmutableInputID(inputID),
		ManifestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpiresAt:      200,
	}
	capability.Digest = capability.CanonicalDigest()
	return capability
}

type reconstructionInputDouble struct {
	artifactVerifications        int
	inputMaterializations        int
	omitArtifactOperationBinding bool
	inputError                   error
	mutateReadOnlyInput          func(*taskworkspace.ReadOnlyInputMaterialization)
}

func (d *reconstructionInputDouble) VerifyArtifactVersion(
	_ context.Context,
	request taskworkspace.VerifyArtifactVersionReconstructionRequest,
) (taskworkspace.ArtifactVersionReconstructionEvidence, error) {
	d.artifactVerifications++
	evidence := taskworkspace.ArtifactVersionReconstructionEvidence{
		ID:                     taskworkspace.EvidenceID("artifact-reconstruction-evidence-" + request.ArtifactVersionInput.ID),
		PublicationAuthorityID: request.ArtifactVersionInput.PublicationAuthorityID,
		PolicyDomainID:         request.ArtifactVersionInput.PolicyDomainID,
		TaskID:                 request.ArtifactVersionInput.TaskID,
		ArtifactVersionID:      request.ArtifactVersionInput.ArtifactVersionID,
		ManifestDigest:         request.ArtifactVersionInput.ManifestDigest,
		InputCapabilityID:      request.ArtifactVersionInput.ID,
		ContentEvidenceRoot:    "artifact-content-evidence-root-1",
		Decision:               taskworkspace.ReconstructionInputVerified,
		RecoveryIntentID:       request.RecoveryIntentID,
		Generation:             request.Generation,
		Fence:                  request.Fence,
		OperationID:            request.Operation.ID,
	}
	if d.omitArtifactOperationBinding {
		evidence.OperationID = ""
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence, nil
}

func (d *reconstructionInputDouble) MaterializeReadOnlyInput(
	_ context.Context,
	request taskworkspace.MaterializeReadOnlyInputRequest,
) (taskworkspace.ReadOnlyInputMaterialization, error) {
	d.inputMaterializations++
	if d.inputError != nil {
		return taskworkspace.ReadOnlyInputMaterialization{}, d.inputError
	}
	result := taskworkspace.ReadOnlyInputMaterialization{
		ID:             taskworkspace.ReadOnlyInputMaterializationID(fmt.Sprintf("read-only-input-%s", request.Capability.ID)),
		CapabilityID:   request.Capability.ID,
		Kind:           request.Capability.Kind,
		InputID:        request.Capability.InputID,
		ManifestDigest: request.Capability.ManifestDigest,
		EvidenceID:     taskworkspace.EvidenceID("input-evidence-" + request.Capability.ID),
		Access:         taskworkspace.InputAccessReadOnly,
		Generation:     request.Generation,
		Fence:          request.Fence,
	}
	result.Digest = result.CanonicalDigest()
	if d.mutateReadOnlyInput != nil {
		d.mutateReadOnlyInput(&result)
	}
	return result, nil
}

func authorizedCheckpointRestoreIntent(
	current taskworkspace.ConfirmTaskWorkspaceResult,
	intentID string,
) taskworkspace.AuthorizedRecoveryIntent {
	intent := taskworkspace.AuthorizedRecoveryIntent{
		ID:                          taskworkspace.RecoveryIntentID(intentID),
		RecoveryAuthorityID:         "recovery-authority-1",
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		TargetKind:                  taskworkspace.RecoveryTargetCheckpoint,
		ExpectedCurrentRevisionID:   current.CurrentRevisionID,
		ExpectedCurrentCheckpointID: current.CurrentCheckpointID,
		TargetRevisionID:            current.CurrentRevisionID,
		TargetCheckpointID:          current.CurrentCheckpointID,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Mode:                        taskworkspace.RecoveryModeWritable,
		ExpiresAt:                   200,
	}
	intent.Digest = intent.CanonicalDigest()
	return intent
}

func expireMaterializationRequest(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	materialized taskworkspace.MaterializeResult,
	operationID string,
) taskworkspace.ExpireMaterializationRequest {
	request := taskworkspace.ExpireMaterializationRequest{
		PolicyDomainID:    "policy-domain-1",
		TaskID:            "task-1",
		TaskWorkspaceID:   confirmed.TaskWorkspaceID,
		MaterializationID: materialized.MaterializationID,
		RevisionID:        confirmed.CurrentRevisionID,
		CheckpointID:      confirmed.CurrentCheckpointID,
		Generation:        confirmed.Generation,
		Fence:             confirmed.Fence,
		ExpiryPolicyID:    "expiry-policy-1",
		Operation:         taskworkspace.Operation{ID: taskworkspace.OperationID(operationID)},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}
