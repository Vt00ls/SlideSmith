package taskworkspace_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestCurrentRecoveryLineageRetainsCheckpoint(t *testing.T) {
	lifecycle, current, committed := committedCheckpointForRetention(t, taskworkspaceTestConfig(&happyDurableObject{}))

	retention, err := lifecycle.InspectCheckpointRetention(context.Background(), taskworkspace.InspectCheckpointRetentionRequest{
		PolicyDomainID:  "policy-domain-1",
		TaskID:          "task-1",
		TaskWorkspaceID: current.TaskWorkspaceID,
		CheckpointID:    committed.CheckpointID,
	})
	if err != nil {
		t.Fatalf("inspect Checkpoint retention: %v", err)
	}
	if retention.Decision != taskworkspace.CheckpointRetained {
		t.Fatalf("retention decision = %q, want %q", retention.Decision, taskworkspace.CheckpointRetained)
	}
	if len(retention.Authorities) != 1 ||
		retention.Authorities[0].Kind != taskworkspace.CheckpointRecoveryLineageAuthority {
		t.Fatalf("retention authorities = %#v, want current recovery lineage", retention.Authorities)
	}
	release := taskworkspace.ReleaseCheckpointRetentionRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                committed.CheckpointID,
		AuthorityID:                 retention.Authorities[0].ID,
		ExpectedRetentionGeneration: retention.RetentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Operation:                   taskworkspace.Operation{ID: "release-current-lineage-1"},
	}
	release.Operation.RequestDigest = release.CanonicalRequestDigest()
	_, err = lifecycle.ReleaseCheckpointRetention(context.Background(), release)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	reclaimed, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
		current, committed.CheckpointID, retention.RetentionGeneration, "reclaim-current-lineage-1",
	))
	if err != nil || reclaimed.Outcome != taskworkspace.CheckpointRetainedByAuthority ||
		len(reclaimed.Evidence.Blockers) != 1 ||
		reclaimed.Evidence.Blockers[0] != taskworkspace.CheckpointRecoveryLineageBlocker {
		t.Fatalf("current recovery lineage reclaim = %#v, err = %v", reclaimed, err)
	}
}

func TestFinalSemanticReferenceReleaseImmediatelyRemovesReachabilityAndStartsDefaultSevenDayGrace(t *testing.T) {
	now := taskworkspace.Instant(100)
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.CheckpointReclamation = mechanics
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	retention := inspectCheckpointRetention(t, lifecycle, current, committed.CheckpointID)

	release := taskworkspace.ReleaseCheckpointRetentionRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                committed.CheckpointID,
		AuthorityID:                 retention.Authorities[0].ID,
		ExpectedRetentionGeneration: retention.RetentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Operation: taskworkspace.Operation{
			ID: "release-final-checkpoint-reference-1",
		},
	}
	release.Operation.RequestDigest = release.CanonicalRequestDigest()
	released, err := lifecycle.ReleaseCheckpointRetention(context.Background(), release)
	if err != nil {
		t.Fatalf("release final Checkpoint authority: %v", err)
	}
	if released.Decision != taskworkspace.CheckpointPendingReclaim {
		t.Fatalf("retention decision = %q, want %q", released.Decision, taskworkspace.CheckpointPendingReclaim)
	}
	if len(released.Authorities) != 0 {
		t.Fatalf("released Checkpoint remains semantically reachable: %#v", released.Authorities)
	}
	wantEligibleAt := now + taskworkspace.Instant(7*24*time.Hour)
	if released.EligibleAt != wantEligibleAt {
		t.Fatalf("reclaim eligibility = %d, want seven-day default %d", released.EligibleAt, wantEligibleAt)
	}
	if mechanics.referenceReleases != 1 {
		t.Fatalf("Durable Object reference releases = %d, want 1", mechanics.referenceReleases)
	}

}

func TestFinalSemanticReleaseRemainsCommittedWhenDurableReferenceReleaseIsAmbiguous(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	mechanics := &checkpointReclamationMechanics{
		present:               true,
		referenceReleaseError: taskworkspace.ErrDurableObjectResultAmbiguous,
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Persistence = persistence
	config.CheckpointReclamation = mechanics
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	retention := inspectCheckpointRetention(t, lifecycle, current, committed.CheckpointID)
	release := taskworkspace.ReleaseCheckpointRetentionRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                committed.CheckpointID,
		AuthorityID:                 retention.Authorities[0].ID,
		ExpectedRetentionGeneration: retention.RetentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Operation:                   taskworkspace.Operation{ID: "release-final-before-ambiguous-mechanics-1"},
	}
	release.Operation.RequestDigest = release.CanonicalRequestDigest()

	_, err := lifecycle.ReleaseCheckpointRetention(context.Background(), release)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	committedRelease := inspectCheckpointRetention(t, lifecycle, current, committed.CheckpointID)
	if committedRelease.Decision != taskworkspace.CheckpointPendingReclaim ||
		len(committedRelease.Authorities) != 0 || committedRelease.EligibleAt == 0 {
		t.Fatalf("ambiguous mechanics restored business reachability: %#v", committedRelease)
	}
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-before-release-reconcile-1",
	))
	if err != nil {
		t.Fatalf("materialize current Checkpoint before release reconciliation: %v", err)
	}
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3",
		"open-after-release-ambiguity-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View before release reconciliation: %v", err)
	}
	manifest := declaredStateManifest("content-after-release-ambiguity")
	_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		current,
		view,
		manifest,
		acceptedValidationEvidence(current, view, manifest),
		"commit-after-release-ambiguity-1",
	))
	if err != nil {
		t.Fatalf("advance workspace fence before release reconciliation: %v", err)
	}
	advanced, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-release-ambiguity-1",
	))
	if err != nil {
		t.Fatalf("confirm advanced workspace fence: %v", err)
	}
	if advanced.Fence <= current.Fence {
		t.Fatalf("workspace fence did not advance: before=%d after=%d", current.Fence, advanced.Fence)
	}

	mechanics.referenceReleaseError = nil
	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.Persistence = persistence
	restartedConfig.CheckpointReclamation = mechanics
	restarted := taskworkspace.NewInMemory(restartedConfig)
	inspection, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    release.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile final semantic release: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal ||
		inspection.ReleaseCheckpointRetention == nil ||
		inspection.ReleaseCheckpointRetention.Decision != taskworkspace.CheckpointPendingReclaim ||
		mechanics.referenceReleases != 2 {
		t.Fatalf("reconciled release = %#v, durable releases = %d", inspection, mechanics.referenceReleases)
	}
}

func TestInventoryScanChecksExactReferencesAcrossThePolicyDomain(t *testing.T) {
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.CheckpointReclamation = mechanics
	lifecycle, observedWorkspace, _ := committedCheckpointForRetention(t, config)

	otherWorkspace, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-2", "confirm-policy-peer-1",
	))
	if err != nil {
		t.Fatalf("confirm policy-domain peer: %v", err)
	}
	otherMaterialization, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-2", otherWorkspace, "materialize-policy-peer-1",
	))
	if err != nil {
		t.Fatalf("materialize policy-domain peer: %v", err)
	}
	otherView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-2", otherWorkspace, otherMaterialization,
		"phase-run-1", "runtime-run-1", "sandbox-lease-task-2",
		"open-policy-peer-1",
	))
	if err != nil {
		t.Fatalf("open policy-domain peer Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-policy-peer-1")
	validation := acceptedValidationEvidence(otherWorkspace, otherView, manifest)
	validation.TaskID = "task-2"
	validation.Digest = validation.CanonicalDigest()
	commit := commitRequest(
		otherWorkspace,
		otherView,
		manifest,
		validation,
		"commit-policy-peer-1",
	)
	commit.TaskID = "task-2"
	commit.Operation.RequestDigest = commit.CanonicalRequestDigest()
	otherCheckpoint, err := lifecycle.CommitRuntimeView(context.Background(), commit)
	if err != nil {
		t.Fatalf("commit policy-domain peer Checkpoint: %v", err)
	}
	mechanics.inventoryResourceID = taskworkspace.InventoryResourceID(
		otherCheckpoint.CheckpointEvidence.ManifestReference.ContentID,
	)
	for _, receipt := range otherCheckpoint.CheckpointEvidence.DurabilityReceipts {
		if receipt.ContentID == otherCheckpoint.CheckpointEvidence.ManifestReference.ContentID {
			mechanics.inventoryGenerationID = receipt.DurabilityGenerationID
			break
		}
	}
	if mechanics.inventoryGenerationID == "" {
		t.Fatal("policy-domain peer fixture omitted manifest generation")
	}

	observe := taskworkspace.ObserveCheckpointInventoryRequest{
		PolicyDomainID:  "policy-domain-1",
		TaskID:          "task-1",
		TaskWorkspaceID: observedWorkspace.TaskWorkspaceID,
		Operation:       taskworkspace.Operation{ID: "observe-policy-domain-shared-generation-1"},
	}
	observe.Operation.RequestDigest = observe.CanonicalRequestDigest()
	observation, err := lifecycle.ObserveCheckpointInventory(context.Background(), observe)
	if err != nil {
		t.Fatalf("observe policy-domain inventory: %v", err)
	}
	if observation.Kind != taskworkspace.CheckpointInventoryNoCandidate {
		t.Fatalf("cross-Task referenced generation classified as orphan: %#v", observation)
	}
}

func TestReleasedCheckpointCannotBeRestoredDuringPhysicalGrace(t *testing.T) {
	var recoveryIntent taskworkspace.AuthorizedRecoveryIntent
	durable := &happyDurableObject{}
	config := taskworkspaceTestConfig(durable)
	config.RecoveryAuthorityID = "recovery-authority-1"
	config.CurrentRecoveryIntent = func(id taskworkspace.RecoveryIntentID) (taskworkspace.AuthorizedRecoveryIntent, bool) {
		return recoveryIntent, id == recoveryIntent.ID
	}
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-forbidden-restore-1")

	recoveryIntent = authorizedCheckpointRestoreIntent(current, "restore-released-checkpoint-intent-1")
	recoveryIntent.TargetRevisionID = committed.RevisionID
	recoveryIntent.TargetCheckpointID = committed.CheckpointID
	recoveryIntent.Digest = recoveryIntent.CanonicalDigest()
	restore := taskworkspace.RestoreTaskWorkspaceRequest{
		Intent:    recoveryIntent,
		Operation: taskworkspace.Operation{ID: "restore-released-checkpoint-1"},
	}
	restore.Operation.RequestDigest = restore.CanonicalRequestDigest()
	verifiedBefore := durable.verified
	_, err := lifecycle.RestoreTaskWorkspace(context.Background(), restore)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorCheckpointNotRetained)
	if durable.verified != verifiedBefore {
		t.Fatal("released Checkpoint reached Durable Object verification during grace")
	}
}

func TestReferenceAttachedDuringGraceSupersedesPendingReclamation(t *testing.T) {
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.CheckpointReclamation = mechanics
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-re-reference-1")

	attach := taskworkspace.AttachCheckpointRetentionRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                committed.CheckpointID,
		ExpectedRetentionGeneration: released.RetentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Authority: taskworkspace.CheckpointRetentionAuthority{
			ID:   "explicit-reference-1",
			Kind: taskworkspace.CheckpointExplicitReferenceAuthority,
		},
		Operation: taskworkspace.Operation{ID: "attach-checkpoint-reference-1"},
	}
	attach.Operation.RequestDigest = attach.CanonicalRequestDigest()
	retained, err := lifecycle.AttachCheckpointRetention(context.Background(), attach)
	if err != nil {
		t.Fatalf("attach Checkpoint reference during grace: %v", err)
	}
	if retained.Decision != taskworkspace.CheckpointRetained || retained.EligibleAt != 0 ||
		retained.RetentionGeneration <= released.RetentionGeneration {
		t.Fatalf("re-reference did not supersede pending reclamation: %#v", retained)
	}
	if len(retained.Authorities) != 1 || retained.Authorities[0] != attach.Authority {
		t.Fatalf("retention authorities = %#v, want explicit typed reference", retained.Authorities)
	}
	if mechanics.referenceReleases != 1 || mechanics.referenceAttaches != 1 {
		t.Fatalf("Durable Object reference transitions = release %d, attach %d", mechanics.referenceReleases, mechanics.referenceAttaches)
	}
}

func TestTypedCheckpointAuthoritiesBlockReclamation(t *testing.T) {
	now := taskworkspace.Instant(100)
	tests := []struct {
		name            string
		kind            taskworkspace.CheckpointRetentionAuthorityKind
		expiresAt       taskworkspace.Instant
		expectedBlocker taskworkspace.CheckpointReclamationBlocker
	}{
		{name: "explicit reference", kind: taskworkspace.CheckpointExplicitReferenceAuthority, expectedBlocker: taskworkspace.CheckpointExplicitReferenceBlocker},
		{name: "active commit lease", kind: taskworkspace.CheckpointCommitLeaseAuthority, expiresAt: 200, expectedBlocker: taskworkspace.CheckpointCommitLeaseBlocker},
		{name: "active restore lease", kind: taskworkspace.CheckpointRestoreLeaseAuthority, expiresAt: 200, expectedBlocker: taskworkspace.CheckpointRestoreLeaseBlocker},
		{name: "integrity incident", kind: taskworkspace.CheckpointIntegrityIncidentAuthority, expectedBlocker: taskworkspace.CheckpointIntegrityIncidentBlocker},
		{name: "Recovery Point pin", kind: taskworkspace.CheckpointRecoveryPointPinAuthority, expectedBlocker: taskworkspace.CheckpointRecoveryPointPinBlocker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mechanics := &checkpointReclamationMechanics{present: true}
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Now = func() taskworkspace.Instant { return now }
			config.CheckpointReclamation = mechanics
			lifecycle, current, committed := supersededCheckpointForRetention(t, config)
			released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-blocker-"+taskworkspace.OperationID(tt.kind))
			attach := taskworkspace.AttachCheckpointRetentionRequest{
				PolicyDomainID:              "policy-domain-1",
				TaskID:                      "task-1",
				TaskWorkspaceID:             current.TaskWorkspaceID,
				CheckpointID:                committed.CheckpointID,
				ExpectedRetentionGeneration: released.RetentionGeneration,
				Generation:                  current.Generation,
				Fence:                       current.Fence,
				Authority: taskworkspace.CheckpointRetentionAuthority{
					ID:        taskworkspace.CheckpointRetentionAuthorityID("authority-" + tt.kind),
					Kind:      tt.kind,
					ExpiresAt: tt.expiresAt,
				},
				Operation: taskworkspace.Operation{ID: taskworkspace.OperationID("attach-blocker-" + tt.kind)},
			}
			attach.Operation.RequestDigest = attach.CanonicalRequestDigest()
			retained, err := lifecycle.AttachCheckpointRetention(context.Background(), attach)
			if err != nil {
				t.Fatalf("attach typed Checkpoint authority: %v", err)
			}
			if retained.Decision != taskworkspace.CheckpointRetained || len(retained.Authorities) != 1 ||
				retained.Authorities[0] != attach.Authority {
				t.Fatalf("typed authority did not retain Checkpoint: %#v", retained)
			}
			result, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
				current,
				committed.CheckpointID,
				retained.RetentionGeneration,
				taskworkspace.OperationID("reclaim-blocked-by-"+tt.kind),
			))
			if err != nil || result.Outcome != taskworkspace.CheckpointRetainedByAuthority || mechanics.calls != 0 ||
				len(result.Evidence.Blockers) != 1 || result.Evidence.Blockers[0] != tt.expectedBlocker {
				t.Fatalf("typed blocker reclaim = %#v, calls = %d, err = %v", result, mechanics.calls, err)
			}
		})
	}
}

func TestCheckpointReclaimWaitsForGraceThenDeletesExactGenerationsIdempotently(t *testing.T) {
	now := taskworkspace.Instant(100)
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.CheckpointReclamation = mechanics
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-reclaim-1")

	beforeGrace := reclaimCheckpointRequest(current, committed.CheckpointID, released.RetentionGeneration, "reclaim-before-grace-1")
	retained, err := lifecycle.ReclaimCheckpoint(context.Background(), beforeGrace)
	if err != nil {
		t.Fatalf("evaluate reclaim before grace: %v", err)
	}
	if retained.Outcome != taskworkspace.CheckpointRetainedByAuthority || mechanics.calls != 0 ||
		len(retained.Evidence.Blockers) != 1 || retained.Evidence.Blockers[0] != taskworkspace.CheckpointGraceBlocker {
		t.Fatalf("reclaim before grace = %#v, mechanics calls = %d", retained, mechanics.calls)
	}

	now = released.EligibleAt
	reclaim := reclaimCheckpointRequest(current, committed.CheckpointID, released.RetentionGeneration, "reclaim-after-grace-1")
	reclaimed, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
	if err != nil {
		t.Fatalf("reclaim Checkpoint after grace: %v", err)
	}
	if reclaimed.Outcome != taskworkspace.CheckpointReclaimed || mechanics.calls != 1 {
		t.Fatalf("reclaim result = %#v, mechanics calls = %d", reclaimed, mechanics.calls)
	}
	if reclaimed.Evidence.Digest == "" || reclaimed.Evidence.Digest != reclaimed.Evidence.CanonicalDigest() {
		t.Fatal("reclaim result lacks canonical evidence")
	}
	if len(mechanics.requests[0].Resources) != 2 {
		t.Fatalf("exact physical generations = %#v, want manifest and member", mechanics.requests[0].Resources)
	}
	for _, resource := range mechanics.requests[0].Resources {
		if resource.ContentID == "" || resource.ReferenceID == "" || resource.ReceiptID == "" || resource.GenerationID == "" {
			t.Fatalf("reclaim omitted exact resource identity/generation: %#v", resource)
		}
	}
	retentionAfterReclaim := inspectCheckpointRetention(t, lifecycle, current, committed.CheckpointID)
	if retentionAfterReclaim.Decision != taskworkspace.CheckpointPhysicallyReclaimed {
		t.Fatalf("retention state after reclaim = %#v", retentionAfterReclaim)
	}

	repeated := reclaimCheckpointRequest(current, committed.CheckpointID, released.RetentionGeneration, "reclaim-repeated-1")
	alreadyAbsent, err := lifecycle.ReclaimCheckpoint(context.Background(), repeated)
	if err != nil {
		t.Fatalf("repeat Checkpoint reclaim: %v", err)
	}
	if alreadyAbsent.Outcome != taskworkspace.CheckpointAlreadyAbsent || mechanics.calls != 2 ||
		alreadyAbsent.Evidence.Digest == "" || alreadyAbsent.Evidence.Digest != alreadyAbsent.Evidence.CanonicalDigest() {
		t.Fatalf("repeated reclaim = %#v, mechanics calls = %d", alreadyAbsent, mechanics.calls)
	}
	if alreadyAbsent.Evidence.MechanicsEvidenceDigest == "" ||
		alreadyAbsent.Evidence.PriorEvidenceDigest != reclaimed.Evidence.Digest {
		t.Fatalf("repeated reclaim lacks current and prior evidence linkage: %#v", alreadyAbsent.Evidence)
	}
}

func TestRetentionPolicyShorteningDoesNotRetroactivelyAccelerateExistingEligibility(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Persistence = persistence
	config.CheckpointReclamation = mechanics
	config.CheckpointRetentionPolicy = taskworkspace.CheckpointRetentionPolicy{
		ID:               "retention-policy-long",
		ReclamationGrace: 100,
	}
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-under-long-policy-1")
	if released.EligibleAt != 200 || released.PolicyID != "retention-policy-long" {
		t.Fatalf("initial eligibility = %#v", released)
	}

	now = 120
	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.Persistence = persistence
	restartedConfig.CheckpointReclamation = mechanics
	restartedConfig.CheckpointRetentionPolicy = taskworkspace.CheckpointRetentionPolicy{
		ID:               "retention-policy-short",
		ReclamationGrace: 10,
	}
	restarted := taskworkspace.NewInMemory(restartedConfig)
	retention := inspectCheckpointRetention(t, restarted, current, committed.CheckpointID)
	if retention.EligibleAt != released.EligibleAt || retention.PolicyID != released.PolicyID {
		t.Fatalf("shorter policy changed existing eligibility: before=%#v after=%#v", released, retention)
	}

	now = 130
	result, err := restarted.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
		current, committed.CheckpointID, retention.RetentionGeneration, "reclaim-under-shortened-policy-1",
	))
	if err != nil {
		t.Fatalf("evaluate reclaim under shortened policy: %v", err)
	}
	if result.Outcome != taskworkspace.CheckpointRetainedByAuthority || mechanics.calls != 0 {
		t.Fatalf("shortened policy accelerated reclaim: result=%#v calls=%d", result, mechanics.calls)
	}
}

func TestGracePeriodReReferenceMakesQueuedReclaimIntentStale(t *testing.T) {
	mechanics := &checkpointReclamationMechanics{present: true}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.CheckpointReclamation = mechanics
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-race-1")
	queued := reclaimCheckpointRequest(current, committed.CheckpointID, released.RetentionGeneration, "queued-reclaim-before-race-1")

	attach := taskworkspace.AttachCheckpointRetentionRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                committed.CheckpointID,
		ExpectedRetentionGeneration: released.RetentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Authority: taskworkspace.CheckpointRetentionAuthority{
			ID:   "race-winning-reference-1",
			Kind: taskworkspace.CheckpointExplicitReferenceAuthority,
		},
		Operation: taskworkspace.Operation{ID: "attach-race-winning-reference-1"},
	}
	attach.Operation.RequestDigest = attach.CanonicalRequestDigest()
	if _, err := lifecycle.AttachCheckpointRetention(context.Background(), attach); err != nil {
		t.Fatalf("attach race-winning reference: %v", err)
	}

	_, err := lifecycle.ReclaimCheckpoint(context.Background(), queued)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	if mechanics.calls != 0 {
		t.Fatal("stale queued reclaim reached physical mechanics")
	}
}

func TestUnknownInventoryAndMechanicsStateCannotAuthorizeReclamation(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(*taskworkspace.CheckpointContentReclamationEvidence)
		expectedBlocker taskworkspace.CheckpointReclamationBlocker
	}{
		{
			name: "unknown inventory",
			mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
				evidence.Outcome = taskworkspace.CheckpointRetainedByAuthority
				evidence.InventoryState = taskworkspace.CheckpointInventoryUnknown
			},
			expectedBlocker: taskworkspace.CheckpointUnknownInventoryBlocker,
		},
		{
			name: "unknown durable reference state",
			mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
				evidence.Outcome = taskworkspace.CheckpointRetainedByAuthority
				evidence.ReferenceState = taskworkspace.CheckpointMechanicsUnknown
				evidence.InventoryState = taskworkspace.CheckpointInventoryPresent
			},
			expectedBlocker: taskworkspace.CheckpointUnknownStateBlocker,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := taskworkspace.Instant(100)
			mechanics := &checkpointReclamationMechanics{present: true, mutate: tt.mutate}
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Now = func() taskworkspace.Instant { return now }
			config.CheckpointReclamation = mechanics
			lifecycle, current, committed := supersededCheckpointForRetention(t, config)
			released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-unknown-"+taskworkspace.OperationID(tt.name))
			now = released.EligibleAt

			result, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
				current, committed.CheckpointID, released.RetentionGeneration,
				taskworkspace.OperationID("reclaim-with-unknown-"+tt.name),
			))
			if err != nil || result.Outcome != taskworkspace.CheckpointRetainedByAuthority ||
				len(result.Evidence.Blockers) != 1 || result.Evidence.Blockers[0] != tt.expectedBlocker {
				t.Fatalf("unknown state reclaim = %#v, err = %v", result, err)
			}
		})
	}

	t.Run("unknown inventory cannot claim AlreadyAbsent", func(t *testing.T) {
		now := taskworkspace.Instant(100)
		mechanics := &checkpointReclamationMechanics{
			present: true,
			mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
				evidence.Outcome = taskworkspace.CheckpointAlreadyAbsent
				evidence.InventoryState = taskworkspace.CheckpointInventoryUnknown
			},
		}
		config := taskworkspaceTestConfig(&happyDurableObject{})
		config.Now = func() taskworkspace.Instant { return now }
		config.CheckpointReclamation = mechanics
		lifecycle, current, committed := supersededCheckpointForRetention(t, config)
		released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-false-absence-1")
		now = released.EligibleAt
		_, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
			current, committed.CheckpointID, released.RetentionGeneration, "reclaim-false-absence-1",
		))
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
	})
}

func TestInventoryScanOnlyProducesNonAuthorizingOrphanCandidateOrExplicitUnknown(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		state                   taskworkspace.CheckpointInventoryState
		authoritativelyRetained bool
		wantKind                taskworkspace.CheckpointInventoryObservationKind
	}{
		{name: "present referenced generation", state: taskworkspace.CheckpointInventoryPresent, authoritativelyRetained: true, wantKind: taskworkspace.CheckpointInventoryNoCandidate},
		{name: "present unreferenced generation", state: taskworkspace.CheckpointInventoryPresent, wantKind: taskworkspace.CheckpointOrphanCandidate},
		{name: "unknown inventory", state: taskworkspace.CheckpointInventoryUnknown, wantKind: taskworkspace.CheckpointInventoryUnknownObservation},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mechanics := &checkpointReclamationMechanics{present: true, inventoryState: tt.state}
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.CheckpointReclamation = mechanics
			lifecycle, current, committed := committedCheckpointForRetention(t, config)
			if tt.authoritativelyRetained {
				mechanics.inventoryResourceID = taskworkspace.InventoryResourceID(
					committed.CheckpointEvidence.ManifestReference.ContentID,
				)
				for _, receipt := range committed.CheckpointEvidence.DurabilityReceipts {
					if receipt.ContentID == committed.CheckpointEvidence.ManifestReference.ContentID {
						mechanics.inventoryGenerationID = receipt.DurabilityGenerationID
						break
					}
				}
				if mechanics.inventoryGenerationID == "" {
					t.Fatal("fixture omitted authoritative manifest generation")
				}
			}
			observe := taskworkspace.ObserveCheckpointInventoryRequest{
				PolicyDomainID:  "policy-domain-1",
				TaskID:          "task-1",
				TaskWorkspaceID: current.TaskWorkspaceID,
				Operation:       taskworkspace.Operation{ID: taskworkspace.OperationID("observe-inventory-" + tt.name)},
			}
			observe.Operation.RequestDigest = observe.CanonicalRequestDigest()
			observation, err := lifecycle.ObserveCheckpointInventory(context.Background(), observe)
			if err != nil {
				t.Fatalf("observe Checkpoint inventory: %v", err)
			}
			if observation.Kind != tt.wantKind || observation.State != tt.state || observation.EvidenceDigest == "" {
				t.Fatalf("inventory observation = %#v", observation)
			}

			retention := inspectCheckpointRetention(t, lifecycle, current, committed.CheckpointID)
			result, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
				current, committed.CheckpointID, retention.RetentionGeneration,
				taskworkspace.OperationID("reclaim-after-inventory-"+tt.name),
			))
			if err != nil || result.Outcome != taskworkspace.CheckpointRetainedByAuthority || mechanics.calls != 0 {
				t.Fatalf("inventory observation authorized reclaim: result=%#v calls=%d err=%v", result, mechanics.calls, err)
			}
		})
	}
}

func TestCheckpointReclaimReconcilesDeterministicallyAfterPreActionFault(t *testing.T) {
	now := taskworkspace.Instant(100)
	persistence := taskworkspace.NewInMemoryPersistence()
	mechanics := &checkpointReclamationMechanics{present: true}
	faulted := false
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.Persistence = persistence
	config.CheckpointReclamation = mechanics
	config.FaultHook = func(event taskworkspace.FaultEvent) error {
		if event.Point == taskworkspace.FaultBeforeCheckpointReclaim && !faulted {
			faulted = true
			return errors.New("injected pre-reclaim crash")
		}
		return nil
	}
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-reclaim-fault-1")
	now = released.EligibleAt
	reclaim := reclaimCheckpointRequest(current, committed.CheckpointID, released.RetentionGeneration, "reclaim-with-pre-action-fault-1")

	_, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaim)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorReconciliationRequired)
	if mechanics.calls != 0 {
		t.Fatal("pre-action fault still reached physical reclaim")
	}

	restartedConfig := taskworkspaceTestConfig(&happyDurableObject{})
	restartedConfig.Now = func() taskworkspace.Instant { return now }
	restartedConfig.Persistence = persistence
	restartedConfig.CheckpointReclamation = mechanics
	restarted := taskworkspace.NewInMemory(restartedConfig)
	inspection, err := restarted.ReconcileOperation(context.Background(), taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: "policy-domain-1",
		TaskID:         "task-1",
		OperationID:    reclaim.Operation.ID,
	})
	if err != nil {
		t.Fatalf("reconcile Checkpoint reclaim: %v", err)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal || inspection.ReclaimCheckpoint == nil ||
		inspection.ReclaimCheckpoint.Outcome != taskworkspace.CheckpointReclaimed || mechanics.calls != 1 {
		t.Fatalf("reconciled reclaim = %#v, mechanics calls = %d", inspection, mechanics.calls)
	}
	replayed, err := restarted.ReclaimCheckpoint(context.Background(), reclaim)
	if err != nil || replayed.Outcome != taskworkspace.CheckpointReclaimed || mechanics.calls != 1 {
		t.Fatalf("replayed reclaim = %#v, calls = %d, err = %v", replayed, mechanics.calls, err)
	}
}

func TestCheckpointContentMechanicsPortCannotChooseSemanticRetentionOrUsePhysicalHeuristics(t *testing.T) {
	port := reflect.TypeOf((*taskworkspace.CheckpointReclamationPort)(nil)).Elem()
	allowedMethods := map[string]bool{
		"AttachCheckpointReferences":  true,
		"ReleaseCheckpointReferences": true,
		"ReclaimCheckpointContent":    true,
		"ObserveCheckpointInventory":  true,
	}
	if port.NumMethod() != len(allowedMethods) {
		t.Fatalf("Checkpoint mechanics methods = %d, want %d closed capabilities", port.NumMethod(), len(allowedMethods))
	}
	for index := 0; index < port.NumMethod(); index++ {
		if !allowedMethods[port.Method(index).Name] {
			t.Fatalf("Checkpoint mechanics exposes semantic authority method %q", port.Method(index).Name)
		}
	}

	for _, contractType := range []reflect.Type{
		reflect.TypeOf(taskworkspace.CheckpointContentReferenceTransitionRequest{}),
		reflect.TypeOf(taskworkspace.ReclaimCheckpointContentRequest{}),
		reflect.TypeOf(taskworkspace.ObserveCheckpointContentInventoryRequest{}),
	} {
		for index := 0; index < contractType.NumField(); index++ {
			name := strings.ToLower(contractType.Field(index).Name)
			for _, forbidden := range []string{"authorities", "eligibleat", "retentionpolicy", "grace", "refcount", "path", "mtime", "directory", "telemetry", "listing"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("mechanics request %s exposes forbidden semantic/heuristic field %q", contractType.Name(), contractType.Field(index).Name)
				}
			}
		}
	}
}

func TestCheckpointReclaimRejectsStaleRetentionGenerationWorkspaceGenerationAndFence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*taskworkspace.ReclaimCheckpointRequest)
	}{
		{name: "retention generation", mutate: func(request *taskworkspace.ReclaimCheckpointRequest) { request.ExpectedRetentionGeneration++ }},
		{name: "workspace generation", mutate: func(request *taskworkspace.ReclaimCheckpointRequest) { request.Generation++ }},
		{name: "fence", mutate: func(request *taskworkspace.ReclaimCheckpointRequest) { request.Fence++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := taskworkspace.Instant(100)
			mechanics := &checkpointReclamationMechanics{present: true}
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Now = func() taskworkspace.Instant { return now }
			config.CheckpointReclamation = mechanics
			lifecycle, current, committed := supersededCheckpointForRetention(t, config)
			released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-stale-"+taskworkspace.OperationID(tt.name))
			now = released.EligibleAt
			request := reclaimCheckpointRequest(
				current, committed.CheckpointID, released.RetentionGeneration,
				taskworkspace.OperationID("reclaim-stale-"+tt.name),
			)
			tt.mutate(&request)
			request.Operation.RequestDigest = request.CanonicalRequestDigest()
			_, err := lifecycle.ReclaimCheckpoint(context.Background(), request)
			assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
			if mechanics.calls != 0 {
				t.Fatal("stale reclaim reached physical mechanics")
			}
		})
	}
}

func TestCheckpointReclaimRejectsMismatchedExactGenerationEvidence(t *testing.T) {
	now := taskworkspace.Instant(100)
	mechanics := &checkpointReclamationMechanics{
		present: true,
		mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
			evidence.ExactGenerationRoot = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
	}
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = func() taskworkspace.Instant { return now }
	config.CheckpointReclamation = mechanics
	lifecycle, current, committed := supersededCheckpointForRetention(t, config)
	released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-generation-mismatch-1")
	now = released.EligibleAt
	_, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
		current, committed.CheckpointID, released.RetentionGeneration, "reclaim-generation-mismatch-1",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityFailure)
}

func TestDurableReferenceLeaseAndQuarantineMechanicsBlockReclamation(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(*taskworkspace.CheckpointContentReclamationEvidence)
		expectedBlocker taskworkspace.CheckpointReclamationBlocker
	}{
		{
			name: "durable reference",
			mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
				evidence.Outcome = taskworkspace.CheckpointRetainedByAuthority
				evidence.ReferenceState = taskworkspace.CheckpointMechanicsBlocked
				evidence.InventoryState = taskworkspace.CheckpointInventoryPresent
			},
			expectedBlocker: taskworkspace.CheckpointDurableReferenceBlocker,
		},
		{
			name: "durable lease",
			mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
				evidence.Outcome = taskworkspace.CheckpointRetainedByAuthority
				evidence.LeaseState = taskworkspace.CheckpointMechanicsBlocked
				evidence.InventoryState = taskworkspace.CheckpointInventoryPresent
			},
			expectedBlocker: taskworkspace.CheckpointDurableLeaseBlocker,
		},
		{
			name: "quarantine",
			mutate: func(evidence *taskworkspace.CheckpointContentReclamationEvidence) {
				evidence.Outcome = taskworkspace.CheckpointRetainedByAuthority
				evidence.QuarantineState = taskworkspace.CheckpointMechanicsBlocked
				evidence.InventoryState = taskworkspace.CheckpointInventoryPresent
			},
			expectedBlocker: taskworkspace.CheckpointQuarantineBlocker,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := taskworkspace.Instant(100)
			mechanics := &checkpointReclamationMechanics{present: true, mutate: tt.mutate}
			config := taskworkspaceTestConfig(&happyDurableObject{})
			config.Now = func() taskworkspace.Instant { return now }
			config.CheckpointReclamation = mechanics
			lifecycle, current, committed := supersededCheckpointForRetention(t, config)
			released := releaseFinalCheckpointAuthority(t, lifecycle, current, committed.CheckpointID, "release-before-mechanics-blocker-"+taskworkspace.OperationID(tt.name))
			now = released.EligibleAt
			result, err := lifecycle.ReclaimCheckpoint(context.Background(), reclaimCheckpointRequest(
				current, committed.CheckpointID, released.RetentionGeneration,
				taskworkspace.OperationID("reclaim-mechanics-blocked-"+tt.name),
			))
			if err != nil || result.Outcome != taskworkspace.CheckpointRetainedByAuthority ||
				len(result.Evidence.Blockers) != 1 || result.Evidence.Blockers[0] != tt.expectedBlocker {
				t.Fatalf("mechanics blocker reclaim = %#v, err = %v", result, err)
			}
		})
	}
}

func committedCheckpointForRetention(
	t *testing.T,
	config taskworkspace.InMemoryConfig,
) (taskworkspace.Lifecycle, taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.CommitRuntimeViewResult) {
	t.Helper()
	lifecycle := taskworkspace.NewInMemory(config)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-retention-view-1",
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
		"commit-retention-checkpoint-1",
	))
	if err != nil {
		t.Fatalf("commit Checkpoint: %v", err)
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-retention-current-1",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	return lifecycle, current, committed
}

func supersededCheckpointForRetention(
	t *testing.T,
	config taskworkspace.InMemoryConfig,
) (taskworkspace.Lifecycle, taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.CommitRuntimeViewResult) {
	t.Helper()
	if config.CheckpointReclamation == nil {
		config.CheckpointReclamation = &checkpointReclamationMechanics{present: true}
	}
	lifecycle, current, older := committedCheckpointForRetention(t, config)
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-retention-checkpoint-2",
	))
	if err != nil {
		t.Fatalf("materialize current Checkpoint: %v", err)
	}
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-retention-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-2")
	_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		current,
		view,
		manifest,
		acceptedValidationEvidence(current, view, manifest),
		"commit-retention-checkpoint-2",
	))
	if err != nil {
		t.Fatalf("commit second Checkpoint: %v", err)
	}
	current, err = lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-retention-current-2",
	))
	if err != nil {
		t.Fatalf("confirm second current Checkpoint: %v", err)
	}
	return lifecycle, current, older
}

func inspectCheckpointRetention(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
	current taskworkspace.ConfirmTaskWorkspaceResult,
	checkpointID taskworkspace.CheckpointID,
) taskworkspace.CheckpointRetention {
	t.Helper()
	retention, err := lifecycle.InspectCheckpointRetention(context.Background(), taskworkspace.InspectCheckpointRetentionRequest{
		PolicyDomainID:  "policy-domain-1",
		TaskID:          "task-1",
		TaskWorkspaceID: current.TaskWorkspaceID,
		CheckpointID:    checkpointID,
	})
	if err != nil {
		t.Fatalf("inspect Checkpoint retention: %v", err)
	}
	return retention
}

func releaseFinalCheckpointAuthority(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
	current taskworkspace.ConfirmTaskWorkspaceResult,
	checkpointID taskworkspace.CheckpointID,
	operationID taskworkspace.OperationID,
) taskworkspace.CheckpointRetention {
	t.Helper()
	retention := inspectCheckpointRetention(t, lifecycle, current, checkpointID)
	release := taskworkspace.ReleaseCheckpointRetentionRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                checkpointID,
		AuthorityID:                 retention.Authorities[0].ID,
		ExpectedRetentionGeneration: retention.RetentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Operation:                   taskworkspace.Operation{ID: operationID},
	}
	release.Operation.RequestDigest = release.CanonicalRequestDigest()
	released, err := lifecycle.ReleaseCheckpointRetention(context.Background(), release)
	if err != nil {
		t.Fatalf("release final Checkpoint authority: %v", err)
	}
	return released
}

func reclaimCheckpointRequest(
	current taskworkspace.ConfirmTaskWorkspaceResult,
	checkpointID taskworkspace.CheckpointID,
	retentionGeneration taskworkspace.RetentionGeneration,
	operationID taskworkspace.OperationID,
) taskworkspace.ReclaimCheckpointRequest {
	request := taskworkspace.ReclaimCheckpointRequest{
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             current.TaskWorkspaceID,
		CheckpointID:                checkpointID,
		ExpectedRetentionGeneration: retentionGeneration,
		Generation:                  current.Generation,
		Fence:                       current.Fence,
		Operation:                   taskworkspace.Operation{ID: operationID},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

type checkpointReclamationMechanics struct {
	present               bool
	calls                 int
	requests              []taskworkspace.ReclaimCheckpointContentRequest
	results               map[taskworkspace.OperationID]taskworkspace.CheckpointContentReclamationEvidence
	mutate                func(*taskworkspace.CheckpointContentReclamationEvidence)
	inventoryState        taskworkspace.CheckpointInventoryState
	inventoryResourceID   taskworkspace.InventoryResourceID
	inventoryGenerationID taskworkspace.DurabilityGenerationID
	referenceReleaseError error
	referenceReleases     int
	referenceAttaches     int
}

func (m *checkpointReclamationMechanics) ReleaseCheckpointReferences(
	_ context.Context,
	request taskworkspace.CheckpointContentReferenceTransitionRequest,
) (taskworkspace.CheckpointContentReferenceTransitionEvidence, error) {
	m.referenceReleases++
	if m.referenceReleaseError != nil {
		return taskworkspace.CheckpointContentReferenceTransitionEvidence{}, m.referenceReleaseError
	}
	return checkpointReferenceTransitionEvidence(request, taskworkspace.CheckpointContentReferencesReleased), nil
}

func (m *checkpointReclamationMechanics) AttachCheckpointReferences(
	_ context.Context,
	request taskworkspace.CheckpointContentReferenceTransitionRequest,
) (taskworkspace.CheckpointContentReferenceTransitionEvidence, error) {
	m.referenceAttaches++
	return checkpointReferenceTransitionEvidence(request, taskworkspace.CheckpointContentReferencesAttached), nil
}

func checkpointReferenceTransitionEvidence(
	request taskworkspace.CheckpointContentReferenceTransitionRequest,
	state taskworkspace.CheckpointContentReferenceState,
) taskworkspace.CheckpointContentReferenceTransitionEvidence {
	evidence := taskworkspace.CheckpointContentReferenceTransitionEvidence{
		ID:                  taskworkspace.EvidenceID("reference-transition-evidence-" + request.Operation.ID),
		PolicyDomainID:      request.PolicyDomainID,
		TaskID:              request.TaskID,
		TaskWorkspaceID:     request.TaskWorkspaceID,
		CheckpointID:        request.CheckpointID,
		RetentionGeneration: request.RetentionGeneration,
		ExactGenerationRoot: request.ExactGenerationRoot,
		State:               state,
		Generation:          request.Generation,
		Fence:               request.Fence,
		OperationID:         request.Operation.ID,
		ObservedAt:          1,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence
}

func (m *checkpointReclamationMechanics) ObserveCheckpointInventory(
	_ context.Context,
	request taskworkspace.ObserveCheckpointContentInventoryRequest,
) (taskworkspace.CheckpointContentInventoryEvidence, error) {
	state := m.inventoryState
	if state == "" {
		state = taskworkspace.CheckpointInventoryPresent
	}
	resourceID := m.inventoryResourceID
	if resourceID == "" {
		resourceID = "opaque-inventory-resource-1"
	}
	generationID := m.inventoryGenerationID
	if generationID == "" {
		generationID = "opaque-inventory-generation-1"
	}
	evidence := taskworkspace.CheckpointContentInventoryEvidence{
		ID:              taskworkspace.EvidenceID("inventory-evidence-" + request.Operation.ID),
		PolicyDomainID:  request.PolicyDomainID,
		TaskID:          request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID,
		ResourceID:      resourceID,
		GenerationID:    generationID,
		State:           state,
		OperationID:     request.Operation.ID,
		ObservedAt:      1,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence, nil
}

func (m *checkpointReclamationMechanics) ReclaimCheckpointContent(
	_ context.Context,
	request taskworkspace.ReclaimCheckpointContentRequest,
) (taskworkspace.CheckpointContentReclamationEvidence, error) {
	m.calls++
	m.requests = append(m.requests, request)
	if result, ok := m.results[request.Operation.ID]; ok {
		return result, nil
	}
	outcome := taskworkspace.CheckpointAlreadyAbsent
	if m.present {
		outcome = taskworkspace.CheckpointReclaimed
		m.present = false
	}
	evidence := taskworkspace.CheckpointContentReclamationEvidence{
		ID:                  taskworkspace.EvidenceID("physical-reclaim-evidence-" + request.Operation.ID),
		PolicyDomainID:      request.PolicyDomainID,
		TaskID:              request.TaskID,
		TaskWorkspaceID:     request.TaskWorkspaceID,
		CheckpointID:        request.CheckpointID,
		RetentionGeneration: request.RetentionGeneration,
		ExactGenerationRoot: request.ExactGenerationRoot,
		ReferenceState:      taskworkspace.CheckpointMechanicsClear,
		LeaseState:          taskworkspace.CheckpointMechanicsClear,
		QuarantineState:     taskworkspace.CheckpointMechanicsClear,
		InventoryState:      taskworkspace.CheckpointInventoryAbsent,
		Outcome:             outcome,
		Generation:          request.Generation,
		Fence:               request.Fence,
		OperationID:         request.Operation.ID,
		ObservedAt:          1,
	}
	if m.mutate != nil {
		m.mutate(&evidence)
	}
	evidence.Digest = evidence.CanonicalDigest()
	if m.results == nil {
		m.results = make(map[taskworkspace.OperationID]taskworkspace.CheckpointContentReclamationEvidence)
	}
	m.results[request.Operation.ID] = evidence
	return evidence, nil
}
