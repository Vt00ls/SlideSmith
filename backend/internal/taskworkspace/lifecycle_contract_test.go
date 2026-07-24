package taskworkspace_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestConfirmReturnsStableTaskWorkspaceIdentity(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})

	first, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	second, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-2",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace again: %v", err)
	}

	if first.TaskWorkspaceID == "" {
		t.Fatal("confirmed Task Workspace identity is empty")
	}
	if second.TaskWorkspaceID != first.TaskWorkspaceID {
		t.Fatal("the same Task received a different Task Workspace identity")
	}
}

func TestConfirmRejectsCrossPolicyDomainBindingWithoutLeakingIdentity(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	if _, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	)); err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}

	_, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"do-not-disclose-policy-domain", "task-1", "confirm-2",
	))
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorOwnershipDenied {
		t.Fatalf("cross-policy binding error = %T, want typed ownership denial", err)
	}
	if strings.Contains(err.Error(), "do-not-disclose-policy-domain") || strings.Contains(err.Error(), "task-1") {
		t.Fatal("ownership denial disclosed submitted identity")
	}
}

func TestTaskWorkspaceIdentitiesAreNotReusedAcrossTasks(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	first, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm first Task Workspace: %v", err)
	}
	second, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-2", "confirm-2",
	))
	if err != nil {
		t.Fatalf("confirm second Task Workspace: %v", err)
	}

	if first.TaskWorkspaceID == second.TaskWorkspaceID || first.CurrentRevisionID == second.CurrentRevisionID {
		t.Fatal("different Tasks reused a Task Workspace or Revision identity")
	}
}

func TestMaterializeRejectsCrossTaskWorkspaceBindingWithoutLeakingIdentity(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	first, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-do-not-disclose-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm first Task Workspace: %v", err)
	}
	if _, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-do-not-disclose-2", "confirm-2",
	)); err != nil {
		t.Fatalf("confirm second Task Workspace: %v", err)
	}
	request := materializeRequest(
		"policy-domain-1", "task-do-not-disclose-2", first, "materialize-1",
	)

	_, err = lifecycle.Materialize(context.Background(), request)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorOwnershipDenied {
		t.Fatalf("cross-Task binding error = %T, want typed ownership denial", err)
	}
	for _, secret := range []string{
		"task-do-not-disclose-1",
		"task-do-not-disclose-2",
		string(first.TaskWorkspaceID),
		string(first.CurrentRevisionID),
	} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("ownership denial disclosed an identity")
		}
	}
}

func TestMaterializeUsesTheAuthoritativeBaseRevision(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	if confirmed.CurrentRevisionID == "" || confirmed.Generation == 0 || confirmed.Fence == 0 {
		t.Fatal("confirmation omitted authoritative base facts")
	}

	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", confirmed, "materialize-1",
	))
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	if materialized.MaterializationID == "" {
		t.Fatal("materialization identity is empty")
	}
	if materialized.TaskWorkspaceID != confirmed.TaskWorkspaceID ||
		materialized.RevisionID != confirmed.CurrentRevisionID ||
		materialized.Generation != confirmed.Generation ||
		materialized.Fence != confirmed.Fence {
		t.Fatal("materialization is not bound to the authoritative base")
	}
}

func TestMaterializeExactReplayReturnsTheOriginalResult(t *testing.T) {
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		ValidationAuthorityID: "validation-authority-1",
	})
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	request := materializeRequest("policy-domain-1", "task-1", confirmed, "materialize-1")

	first, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	second, err := lifecycle.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("replay materialization: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatal("exact replay did not return the original materialization result")
	}
}

func TestOpenRuntimeViewIsolatesMutatingRuntimeRuns(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)

	first, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	second, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-2", "sandbox-lease-2", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}

	if first.RuntimeViewID == "" || second.RuntimeViewID == "" || first.RuntimeViewID == second.RuntimeViewID {
		t.Fatal("mutating Runtime Runs did not receive isolated Runtime View identities")
	}
	if first.BaseRevisionID != confirmed.CurrentRevisionID || first.RuntimeRunID != "runtime-run-1" ||
		first.Generation != confirmed.Generation || first.Fence != confirmed.Fence {
		t.Fatal("first Runtime View omitted an exact authority binding")
	}
	if second.BaseRevisionID != confirmed.CurrentRevisionID || second.RuntimeRunID != "runtime-run-2" ||
		second.Generation != confirmed.Generation || second.Fence != confirmed.Fence {
		t.Fatal("second Runtime View omitted an exact authority binding")
	}
}

func TestOpenRuntimeViewBindsRuntimeAuthorityEffectAndExpiry(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	request := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	)

	view, err := lifecycle.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}

	if view.PolicyDomainID != request.PolicyDomainID || view.TaskID != request.TaskID ||
		view.TaskWorkspaceID != request.TaskWorkspaceID || view.MaterializationID != request.MaterializationID ||
		view.BaseRevisionID != request.BaseRevisionID || view.PhaseRunID != request.PhaseRunID ||
		view.RuntimeRunID != request.RuntimeRunID || view.RuntimeOperationID != request.RuntimeOperationID ||
		view.SandboxLeaseAuthority != request.SandboxLeaseAuthority ||
		view.SandboxLeaseAuthority.EvidenceID == "" ||
		view.EffectClass != taskworkspace.RuntimeViewMutating ||
		view.ExpiresAt != request.ExpiresAt ||
		view.Generation != confirmed.Generation || view.Fence != confirmed.Fence ||
		view.Operation != request.Operation {
		t.Fatal("Runtime View omitted an exact runtime, lease, effect, expiry, generation, or fence binding")
	}
}

func TestOpenRuntimeViewExactReplayDoesNotCreateAnotherView(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	request := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	)

	first, err := lifecycle.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	replayed, err := lifecycle.OpenRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("replay Runtime View open: %v", err)
	}
	if replayed != first {
		t.Fatal("exact replay did not return the original Runtime View")
	}

	changed := request
	changed.RuntimeRunID = "runtime-run-2"
	changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
	_, err = lifecycle.OpenRuntimeView(context.Background(), changed)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityConflict {
		t.Fatalf("changed replay error = %T, want typed integrity conflict", err)
	}
}

func TestCommitValidatedRuntimeViewProducesRevisionAndCheckpoint(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, evidence, "commit-1")

	committed, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}

	if committed.RevisionID == "" || committed.RevisionID == confirmed.CurrentRevisionID {
		t.Fatal("changed state did not produce a resulting Task Workspace Revision")
	}
	if committed.CheckpointID == "" {
		t.Fatal("commit did not produce a Checkpoint")
	}
	if committed.TaskWorkspaceID != confirmed.TaskWorkspaceID ||
		committed.BaseRevisionID != confirmed.CurrentRevisionID ||
		committed.PredecessorRevisionID != confirmed.CurrentRevisionID ||
		committed.ManifestDigest != manifest.Digest ||
		committed.ValidationEvidenceID != evidence.ID ||
		committed.ValidationEvidenceDigest != evidence.Digest ||
		committed.ContentEvidenceRoot == "" || committed.DurabilityEvidenceRoot == "" ||
		committed.Generation != confirmed.Generation || committed.PreviousFence != confirmed.Fence ||
		committed.Fence <= committed.PreviousFence {
		t.Fatal("commit result omitted required lineage, manifest, evidence, generation, or fence bindings")
	}
	if committed.Operation.ID != request.Operation.ID ||
		committed.Operation.RequestDigest != request.Operation.RequestDigest {
		t.Fatal("commit result omitted the OperationID or canonical request digest")
	}
	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-commit",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace after commit: %v", err)
	}
	if committed.Fence != current.Fence || committed.RevisionID != current.CurrentRevisionID {
		t.Fatal("commit result did not return the current authoritative Revision and fence")
	}
}

func TestReadOnlyRuntimeViewCannotCommitOrAdvanceRevision(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	openRequest := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-read-only", "sandbox-lease-read-only", "open-view-1",
	)
	openRequest.EffectClass = taskworkspace.RuntimeViewReadOnly
	openRequest.Operation.RequestDigest = openRequest.CanonicalRequestDigest()
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRequest)
	if err != nil {
		t.Fatalf("open read-only Runtime View: %v", err)
	}
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)

	result, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, evidence, "commit-read-only",
	))
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorEffectDenied {
		t.Fatalf("read-only commit error = %T/%v, want typed effect denial", err, err)
	}
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("read-only Runtime View returned authoritative mutation identities")
	}

	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-read-only",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace after rejected read-only commit: %v", err)
	}
	if current.CurrentRevisionID != confirmed.CurrentRevisionID {
		t.Fatal("read-only Runtime View advanced the authoritative Revision")
	}
}

func TestDiscardDoesNotAdvanceRevisionOrAffectAnotherRuntimeView(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}

	discarded, err := lifecycle.DiscardRuntimeView(context.Background(), discardRequest(
		confirmed, firstView, taskworkspace.RuntimeViewValidationRejected, "discard-view-1",
	))
	if err != nil {
		t.Fatalf("discard first Runtime View: %v", err)
	}
	if discarded.CurrentRevisionID != confirmed.CurrentRevisionID ||
		discarded.RuntimeViewID != firstView.RuntimeViewID {
		t.Fatal("discard changed authoritative Revision or returned the wrong Runtime View")
	}

	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-discard",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace after discard: %v", err)
	}
	if current.CurrentRevisionID != confirmed.CurrentRevisionID {
		t.Fatal("discard advanced the authoritative Revision")
	}

	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, secondView, manifest)
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, secondView, manifest, evidence, "commit-view-2",
	)); err != nil {
		t.Fatalf("commit unaffected Runtime View: %v", err)
	}
}

func TestRuntimeFailureCanDiscardAfterSandboxLeaseExpiry(t *testing.T) {
	now := taskworkspace.Instant(100)
	lifecycle := newLifecycleWithHarness(func() taskworkspace.Instant { return now }, nil)
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	now = 301

	if _, err := lifecycle.DiscardRuntimeView(context.Background(), discardRequest(
		confirmed, view, taskworkspace.RuntimeViewRuntimeFailed, "discard-expired-view",
	)); err != nil {
		t.Fatalf("discard Runtime-failed view after lease expiry: %v", err)
	}
}

func TestCommitAndDiscardUseOneFirstWriterWinsTerminalDecision(t *testing.T) {
	t.Run("discard linearizes before commit", func(t *testing.T) {
		commitReached := make(chan struct{}, 1)
		releaseCommit := make(chan struct{})
		lifecycle := newLifecycleWithTerminalHook(func(attempt taskworkspace.RuntimeViewTerminalAttempt) {
			if attempt.OperationID == "commit-1" {
				commitReached <- struct{}{}
				<-releaseCommit
			}
		})
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		commit := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-1")
		commitDone := make(chan error, 1)
		go func() {
			_, commitErr := lifecycle.CommitRuntimeView(context.Background(), commit)
			commitDone <- commitErr
		}()
		<-commitReached

		discard := discardRequest(confirmed, view, taskworkspace.RuntimeViewValidationRejected, "discard-1")
		first, err := lifecycle.DiscardRuntimeView(context.Background(), discard)
		if err != nil {
			t.Fatalf("discard Runtime View: %v", err)
		}
		close(releaseCommit)
		assertLifecycleErrorCode(t, <-commitDone, taskworkspace.ErrorViewTerminalConflict)

		replayed, err := lifecycle.DiscardRuntimeView(context.Background(), discard)
		if err != nil {
			t.Fatalf("replay discard: %v", err)
		}
		if !reflect.DeepEqual(replayed, first) {
			t.Fatal("discard exact replay did not return its original terminal result")
		}
	})

	t.Run("commit linearizes before discard", func(t *testing.T) {
		discardReached := make(chan struct{}, 1)
		releaseDiscard := make(chan struct{})
		lifecycle := newLifecycleWithTerminalHook(func(attempt taskworkspace.RuntimeViewTerminalAttempt) {
			if attempt.OperationID == "discard-1" {
				discardReached <- struct{}{}
				<-releaseDiscard
			}
		})
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		discard := discardRequest(confirmed, view, taskworkspace.RuntimeViewRuntimeFailed, "discard-1")
		discardDone := make(chan error, 1)
		go func() {
			_, discardErr := lifecycle.DiscardRuntimeView(context.Background(), discard)
			discardDone <- discardErr
		}()
		<-discardReached

		manifest := declaredStateManifest("content-1")
		commit := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-1")
		first, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("commit Runtime View: %v", err)
		}
		close(releaseDiscard)
		assertLifecycleErrorCode(t, <-discardDone, taskworkspace.ErrorViewTerminalConflict)

		replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("replay commit: %v", err)
		}
		if !reflect.DeepEqual(replayed, first) {
			t.Fatal("commit exact replay did not return its original terminal result")
		}
	})
}

func TestConcurrentUnchangedMutatingViewsHaveOneAuthoritativeWinner(t *testing.T) {
	firstReached := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	lifecycle := newLifecycleWithTerminalHook(func(attempt taskworkspace.RuntimeViewTerminalAttempt) {
		if attempt.OperationID == "commit-1" {
			firstReached <- struct{}{}
			<-releaseFirst
		}
	})
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}
	emptyManifest := taskworkspace.DeclaredStateManifest{}
	emptyManifest.Digest = emptyManifest.CanonicalDigest()
	firstEvidence := acceptedValidationEvidence(confirmed, firstView, emptyManifest)
	secondEvidence := acceptedValidationEvidence(confirmed, secondView, emptyManifest)
	secondEvidence.ID = "validation-evidence-2"
	secondEvidence.Digest = secondEvidence.CanonicalDigest()
	firstCommit := commitRequest(confirmed, firstView, emptyManifest, firstEvidence, "commit-1")
	secondCommit := commitRequest(confirmed, secondView, emptyManifest, secondEvidence, "commit-2")
	firstDone := make(chan error, 1)
	go func() {
		_, commitErr := lifecycle.CommitRuntimeView(context.Background(), firstCommit)
		firstDone <- commitErr
	}()
	<-firstReached

	winner, err := lifecycle.CommitRuntimeView(context.Background(), secondCommit)
	if err != nil {
		t.Fatalf("commit winning Runtime View: %v", err)
	}
	close(releaseFirst)
	assertLifecycleErrorCode(t, <-firstDone, taskworkspace.ErrorStaleAuthority)
	if winner.RevisionID != confirmed.CurrentRevisionID || winner.CheckpointID == "" {
		t.Fatal("unchanged winner did not preserve Revision while creating its one Checkpoint")
	}
}

func TestCommitAndCancellationFenceOrderAtOneTerminalLinearization(t *testing.T) {
	t.Run("cancellation fence linearizes before commit", func(t *testing.T) {
		commitReached := make(chan struct{}, 1)
		releaseCommit := make(chan struct{})
		lifecycle := newLifecycleWithTerminalHook(func(attempt taskworkspace.RuntimeViewTerminalAttempt) {
			if attempt.OperationID == "commit-1" {
				commitReached <- struct{}{}
				<-releaseCommit
			}
		})
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		commit := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-1")
		commitDone := make(chan error, 1)
		go func() {
			_, commitErr := lifecycle.CommitRuntimeView(context.Background(), commit)
			commitDone <- commitErr
		}()
		<-commitReached

		fence := fenceRequest(confirmed, view, taskworkspace.RuntimeViewCancelled, "fence-1")
		fenced, err := lifecycle.FenceRuntimeView(context.Background(), fence)
		if err != nil {
			t.Fatalf("fence cancelled Runtime View: %v", err)
		}
		if fenced.Fence <= confirmed.Fence || fenced.Generation != confirmed.Generation {
			t.Fatal("cancellation did not advance the fence while preserving generation")
		}
		close(releaseCommit)
		assertLifecycleErrorCode(t, <-commitDone, taskworkspace.ErrorStaleAuthority)

		replayed, err := lifecycle.FenceRuntimeView(context.Background(), fence)
		if err != nil {
			t.Fatalf("replay cancellation fence: %v", err)
		}
		if replayed != fenced {
			t.Fatal("fence exact replay did not return its original terminal result")
		}
	})

	t.Run("commit linearizes before cancellation fence", func(t *testing.T) {
		fenceReached := make(chan struct{}, 1)
		releaseFence := make(chan struct{})
		lifecycle := newLifecycleWithTerminalHook(func(attempt taskworkspace.RuntimeViewTerminalAttempt) {
			if attempt.OperationID == "fence-1" {
				fenceReached <- struct{}{}
				<-releaseFence
			}
		})
		confirmed, materialized := materializedTaskUsing(t, lifecycle)
		view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
			"policy-domain-1", "task-1", confirmed, materialized,
			"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
		))
		if err != nil {
			t.Fatalf("open Runtime View: %v", err)
		}
		fence := fenceRequest(confirmed, view, taskworkspace.RuntimeViewCancelled, "fence-1")
		fenceDone := make(chan error, 1)
		go func() {
			_, fenceErr := lifecycle.FenceRuntimeView(context.Background(), fence)
			fenceDone <- fenceErr
		}()
		<-fenceReached

		manifest := declaredStateManifest("content-1")
		commit := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-1")
		committed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("commit Runtime View: %v", err)
		}
		close(releaseFence)
		assertLifecycleErrorCode(t, <-fenceDone, taskworkspace.ErrorViewTerminalConflict)

		replayed, err := lifecycle.CommitRuntimeView(context.Background(), commit)
		if err != nil {
			t.Fatalf("replay commit after cancellation race: %v", err)
		}
		if !reflect.DeepEqual(replayed, committed) {
			t.Fatal("pre-fence commit exact replay did not return its existing success")
		}
	})
}

func TestCommitExactReplayReturnsTheOriginalTerminalResult(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, evidence, "commit-1")

	first, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}
	second, err := lifecycle.CommitRuntimeView(context.Background(), request)
	if err != nil {
		t.Fatalf("replay committed Runtime View: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatal("exact replay did not return the original terminal commit result")
	}
}

func TestCommitAllowsOnlyOneWriterFromTheSameBaseRevision(t *testing.T) {
	lifecycle, confirmed, materialized := materializedTask(t)
	firstView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
	))
	if err != nil {
		t.Fatalf("open first Runtime View: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}

	firstManifest := declaredStateManifest("content-1")
	firstEvidence := acceptedValidationEvidence(confirmed, firstView, firstManifest)
	if _, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, firstView, firstManifest, firstEvidence, "commit-1",
	)); err != nil {
		t.Fatalf("commit first Runtime View: %v", err)
	}

	secondManifest := declaredStateManifest("content-2")
	secondEvidence := acceptedValidationEvidence(confirmed, secondView, secondManifest)
	secondEvidence.ID = "validation-evidence-2"
	secondEvidence.Digest = secondEvidence.CanonicalDigest()
	result, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, secondView, secondManifest, secondEvidence, "commit-2",
	))
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorStaleAuthority {
		t.Fatalf("second writer error = %T/%v, want typed stale authority", err, err)
	}
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("stale writer returned authoritative result identities")
	}
}

func TestOperationIDDifferentCanonicalDigestReturnsTypedIntegrityConflict(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, view, manifest)
	request := commitRequest(confirmed, view, manifest, evidence, "commit-1")
	if _, err := lifecycle.CommitRuntimeView(context.Background(), request); err != nil {
		t.Fatalf("commit validated Runtime View: %v", err)
	}

	changed := request
	changed.DeclaredStateManifest = declaredStateManifest("content-2")
	changed.ValidationEvidence.ManifestDigest = changed.DeclaredStateManifest.Digest
	changed.ValidationEvidence.Digest = changed.ValidationEvidence.CanonicalDigest()
	changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
	_, err := lifecycle.CommitRuntimeView(context.Background(), changed)
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != taskworkspace.ErrorIntegrityConflict {
		t.Fatalf("OperationID reuse error = %T, want typed integrity conflict", err)
	}
}

func TestUnchangedContentCommitCreatesADistinctCheckpoint(t *testing.T) {
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	firstEvidence := acceptedValidationEvidence(confirmed, view, manifest)
	first, err := lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, view, manifest, firstEvidence, "commit-1",
	))
	if err != nil {
		t.Fatalf("commit changed state: %v", err)
	}

	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-2",
	))
	if err != nil {
		t.Fatalf("confirm current Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", current, "materialize-2",
	))
	if err != nil {
		t.Fatalf("materialize current Revision: %v", err)
	}
	secondView, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", current, materialized,
		"phase-run-2", "runtime-run-2", "sandbox-lease-3", "open-view-2",
	))
	if err != nil {
		t.Fatalf("open second Runtime View: %v", err)
	}
	secondManifest := declaredStateManifest("content-1")
	secondEvidence := acceptedValidationEvidence(current, secondView, secondManifest)
	secondEvidence.ID = "validation-evidence-2"
	secondEvidence.Digest = secondEvidence.CanonicalDigest()
	secondRequest := commitRequest(current, secondView, secondManifest, secondEvidence, "commit-2")
	second, err := lifecycle.CommitRuntimeView(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("commit unchanged state: %v", err)
	}

	if second.RevisionID != first.RevisionID {
		t.Fatal("unchanged content unexpectedly created a different Task Workspace Revision")
	}
	if second.CheckpointID == "" || second.CheckpointID == first.CheckpointID {
		t.Fatal("unchanged-content commit did not create a distinct Checkpoint")
	}
	if second.PredecessorRevisionID == second.RevisionID {
		t.Fatal("unchanged-content commit reported a self-predecessor")
	}
}

func TestCommitFailsClosedUnlessEveryAuthorityAndIntegrityBindingIsExact(t *testing.T) {
	tests := []struct {
		name string
		code taskworkspace.ErrorCode
		edit func(*taskworkspace.CommitRuntimeViewRequest)
	}{
		{
			name: "missing Runtime View",
			code: taskworkspace.ErrorInvalidIntent,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.RuntimeViewID = ""
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different Runtime View",
			code: taskworkspace.ErrorOwnershipDenied,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.RuntimeViewID = "runtime-view-not-owned"
				request.ValidationEvidence.RuntimeViewID = request.RuntimeViewID
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different base Revision",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.BaseRevisionID = "revision-not-current"
				request.ExpectedCurrentRevision = request.BaseRevisionID
				request.ValidationEvidence.BaseRevisionID = request.BaseRevisionID
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different current Revision",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ExpectedCurrentRevision = "revision-not-current"
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different generation",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.Generation++
				request.ValidationEvidence.Generation = request.Generation
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different fence",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.Fence++
				request.ValidationEvidence.Fence = request.Fence
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "missing validation evidence",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ValidationEvidence = taskworkspace.ValidationEvidence{}
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "different validation authority",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ValidationEvidence.ValidationAuthorityID = "validation-authority-2"
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "validation not accepted",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.ValidationEvidence.Decision = "rejected"
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "manifest digest mismatch",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.DeclaredStateManifest.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				request.ValidationEvidence.ManifestDigest = request.DeclaredStateManifest.Digest
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "duplicate declared member",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.DeclaredStateManifest.Members = append(
					request.DeclaredStateManifest.Members,
					request.DeclaredStateManifest.Members[0],
				)
				request.DeclaredStateManifest.Digest = request.DeclaredStateManifest.CanonicalDigest()
				request.ValidationEvidence.ManifestDigest = request.DeclaredStateManifest.Digest
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
				request.Operation.RequestDigest = request.CanonicalRequestDigest()
			},
		},
		{
			name: "missing canonical request digest",
			code: taskworkspace.ErrorInvalidIntent,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest) {
				request.Operation.RequestDigest = ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
			manifest := declaredStateManifest("content-1")
			evidence := acceptedValidationEvidence(confirmed, view, manifest)
			request := commitRequest(confirmed, view, manifest, evidence, "commit-1")
			test.edit(&request)

			result, err := lifecycle.CommitRuntimeView(context.Background(), request)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != test.code {
				t.Fatalf("commit error = %T/%v, want code %q", err, err, test.code)
			}
			if result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("rejected commit returned authoritative result identities")
			}
		})
	}
}

func TestPublicLifecycleContractAndErrorsDoNotExposePhysicalDetails(t *testing.T) {
	banned := []string{
		"path",
		"session",
		"mount",
		"locator",
		"objectlocator",
		"objectkey",
		"bucket",
		"vendor",
		"credential",
		"sdk",
		"filesystem",
	}
	seen := map[reflect.Type]bool{}
	var inspectType func(reflect.Type)
	inspectType = func(contractType reflect.Type) {
		for contractType.Kind() == reflect.Pointer || contractType.Kind() == reflect.Slice || contractType.Kind() == reflect.Array {
			contractType = contractType.Elem()
		}
		if seen[contractType] {
			return
		}
		seen[contractType] = true
		if contractType.PkgPath() != "github.com/slidesmith/slidesmith/backend/internal/taskworkspace" {
			return
		}
		assertSafeContractName(t, contractType.Name(), banned)
		if contractType.Kind() != reflect.Struct {
			return
		}
		for index := 0; index < contractType.NumField(); index++ {
			field := contractType.Field(index)
			assertSafeContractName(t, field.Name, banned)
			inspectType(field.Type)
		}
	}

	lifecycleType := reflect.TypeOf((*taskworkspace.Lifecycle)(nil)).Elem()
	for methodIndex := 0; methodIndex < lifecycleType.NumMethod(); methodIndex++ {
		method := lifecycleType.Method(methodIndex)
		assertSafeContractName(t, method.Name, banned)
		for inputIndex := 0; inputIndex < method.Type.NumIn(); inputIndex++ {
			inspectType(method.Type.In(inputIndex))
		}
		for outputIndex := 0; outputIndex < method.Type.NumOut(); outputIndex++ {
			inspectType(method.Type.Out(outputIndex))
		}
	}
	durableObjectType := reflect.TypeOf((*taskworkspace.DurableObjectPort)(nil)).Elem()
	if durableObjectType.NumMethod() != 2 {
		t.Fatal("Durable Object port exposes authority beyond prepare and verification mechanics")
	}
	for methodIndex := 0; methodIndex < durableObjectType.NumMethod(); methodIndex++ {
		method := durableObjectType.Method(methodIndex)
		if method.Name != "PrepareCheckpoint" && method.Name != "VerifyCheckpoint" {
			t.Fatal("Durable Object port exposes listing, adoption, retention, release, or business selection authority")
		}
		assertSafeContractName(t, method.Name, banned)
		for inputIndex := 0; inputIndex < method.Type.NumIn(); inputIndex++ {
			inspectType(method.Type.In(inputIndex))
		}
		for outputIndex := 0; outputIndex < method.Type.NumOut(); outputIndex++ {
			inspectType(method.Type.Out(outputIndex))
		}
	}
	inspectType(reflect.TypeOf(taskworkspace.InMemoryConfig{}))
	inspectType(reflect.TypeOf(taskworkspace.Error{}))

	for _, code := range []taskworkspace.ErrorCode{
		taskworkspace.ErrorInvalidIntent,
		taskworkspace.ErrorIntegrityConflict,
		taskworkspace.ErrorIntegrityFailure,
		taskworkspace.ErrorOwnershipDenied,
		taskworkspace.ErrorStaleAuthority,
		taskworkspace.ErrorViewTerminalConflict,
		taskworkspace.ErrorEffectDenied,
		taskworkspace.ErrorReconciliationRequired,
	} {
		message := (&taskworkspace.Error{Code: code}).Error()
		for _, term := range banned {
			if strings.Contains(strings.ToLower(message), term) {
				t.Fatal("ordinary lifecycle error exposes a physical implementation term")
			}
		}
	}
}

func assertSafeContractName(t *testing.T, name string, banned []string) {
	t.Helper()
	normalized := strings.ToLower(name)
	for _, term := range banned {
		if strings.Contains(normalized, term) {
			t.Fatal("public lifecycle contract exposes a physical implementation term")
		}
	}
}

func confirmRequest(policyDomainID, taskID, operationID string) taskworkspace.ConfirmTaskWorkspaceRequest {
	request := taskworkspace.ConfirmTaskWorkspaceRequest{
		PolicyDomainID: taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:         taskworkspace.TaskID(taskID),
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func materializeRequest(
	policyDomainID, taskID string,
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	operationID string,
) taskworkspace.MaterializeRequest {
	request := taskworkspace.MaterializeRequest{
		PolicyDomainID:  taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:          taskworkspace.TaskID(taskID),
		TaskWorkspaceID: confirmed.TaskWorkspaceID,
		RevisionID:      confirmed.CurrentRevisionID,
		CheckpointID:    confirmed.CurrentCheckpointID,
		Generation:      confirmed.Generation,
		Fence:           confirmed.Fence,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func materializedTask(t *testing.T) (
	taskworkspace.Lifecycle,
	taskworkspace.ConfirmTaskWorkspaceResult,
	taskworkspace.MaterializeResult,
) {
	t.Helper()
	lifecycle := newLifecycle()
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	return lifecycle, confirmed, materialized
}

func materializedTaskUsing(
	t *testing.T,
	lifecycle taskworkspace.Lifecycle,
) (taskworkspace.ConfirmTaskWorkspaceResult, taskworkspace.MaterializeResult) {
	t.Helper()
	confirmed, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-1",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	materialized, err := lifecycle.Materialize(context.Background(), materializeRequest(
		"policy-domain-1", "task-1", confirmed, "materialize-1",
	))
	if err != nil {
		t.Fatalf("materialize Task Workspace: %v", err)
	}
	return confirmed, materialized
}

func openRuntimeViewRequest(
	policyDomainID, taskID string,
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	materialized taskworkspace.MaterializeResult,
	phaseRunID, runtimeRunID, sandboxLeaseID, operationID string,
) taskworkspace.OpenRuntimeViewRequest {
	request := taskworkspace.OpenRuntimeViewRequest{
		PolicyDomainID:     taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:             taskworkspace.TaskID(taskID),
		TaskWorkspaceID:    confirmed.TaskWorkspaceID,
		MaterializationID:  materialized.MaterializationID,
		BaseRevisionID:     confirmed.CurrentRevisionID,
		PhaseRunID:         taskworkspace.PhaseRunID(phaseRunID),
		RuntimeRunID:       taskworkspace.RuntimeRunID(runtimeRunID),
		RuntimeOperationID: taskworkspace.OperationID("runtime-operation-" + runtimeRunID),
		SandboxLeaseAuthority: sandboxLeaseAuthority(
			policyDomainID, taskID, phaseRunID, runtimeRunID, sandboxLeaseID,
		),
		EffectClass: taskworkspace.RuntimeViewMutating,
		ExpiresAt:   200,
		Generation:  confirmed.Generation,
		Fence:       confirmed.Fence,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func openedRuntimeView(
	t *testing.T,
	runtimeRunID, operationID string,
) (
	taskworkspace.Lifecycle,
	taskworkspace.ConfirmTaskWorkspaceResult,
	taskworkspace.MaterializeResult,
	taskworkspace.OpenRuntimeViewResult,
) {
	t.Helper()
	lifecycle, confirmed, materialized := materializedTask(t)
	view, err := lifecycle.OpenRuntimeView(context.Background(), openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", runtimeRunID, "sandbox-lease-1", operationID,
	))
	if err != nil {
		t.Fatalf("open Runtime View: %v", err)
	}
	return lifecycle, confirmed, materialized, view
}

func declaredStateManifest(contentID string) taskworkspace.DeclaredStateManifest {
	contentDigest, size := declaredContentFacts(contentID)
	manifest := taskworkspace.DeclaredStateManifest{
		Members: []taskworkspace.DeclaredStateMember{
			{
				ID:            "state-member-1",
				LogicalMember: "state/deck.json",
				Type:          taskworkspace.StateMemberRegularFile,
				Mode:          0o600,
				Class:         taskworkspace.StateMemberTaskOwnedMutable,
				ContentDigest: contentDigest,
				Size:          size,
			},
		},
	}
	manifest.Digest = manifest.CanonicalDigest()
	return manifest
}

func declaredContentFacts(contentID string) (taskworkspace.Digest, uint64) {
	if contentID == "content-2" {
		return "sha256:1dde25249fd4b6cbedb58974a4e89c06c5741fee860b2e7faf35cd9bfd3debaf", 20
	}
	return "sha256:c23e70927230be9d39b8237ab27c9a45cec5e1dafac3941a1dabf1df748656ca", 20
}

func newLifecycle() taskworkspace.Lifecycle {
	return newLifecycleWithTerminalHook(nil)
}

func newLifecycleWithTerminalHook(hook func(taskworkspace.RuntimeViewTerminalAttempt)) taskworkspace.Lifecycle {
	return newLifecycleWithHarness(func() taskworkspace.Instant { return 100 }, hook)
}

func newLifecycleWithHarness(
	now func() taskworkspace.Instant,
	hook func(taskworkspace.RuntimeViewTerminalAttempt),
) taskworkspace.Lifecycle {
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.Now = now
	config.BeforeRuntimeViewTerminal = hook
	return taskworkspace.NewInMemory(config)
}

func newLifecycleWithDurableObject(durable taskworkspace.DurableObjectPort) taskworkspace.Lifecycle {
	return taskworkspace.NewInMemory(taskworkspaceTestConfig(durable))
}

func taskworkspaceTestConfig(durable taskworkspace.DurableObjectPort) taskworkspace.InMemoryConfig {
	return taskworkspace.InMemoryConfig{
		ValidationAuthorityID:   "validation-authority-1",
		DurabilityAuthorityID:   "durability-authority-1",
		DurableObject:           durable,
		SandboxLeaseAuthorityID: "sandbox-lease-authority-1",
		Now:                     func() taskworkspace.Instant { return 100 },
		CurrentSandboxLeaseAuthorities: []taskworkspace.SandboxLeaseAuthority{
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-1", "runtime-run-1", "sandbox-lease-1"),
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-1", "runtime-run-2", "sandbox-lease-2"),
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-2", "runtime-run-2", "sandbox-lease-3"),
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-1", "runtime-run-read-only", "sandbox-lease-read-only"),
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-1", "runtime-run-1", "sandbox-lease-task-1"),
			sandboxLeaseAuthority("policy-domain-1", "task-2", "phase-run-1", "runtime-run-1", "sandbox-lease-task-2"),
			sandboxLeaseAuthority("policy-domain-1", "task-1", "phase-run-one", "runtime-run-one", "sandbox-lease-one"),
			sandboxLeaseAuthority("policy-domain-1", "task-2", "phase-run-two", "runtime-run-two", "sandbox-lease-two"),
			sandboxLeaseAuthority("policy-domain-2", "task-3", "phase-run-three", "runtime-run-three", "sandbox-lease-three"),
		},
	}
}

func assertLifecycleErrorCode(t *testing.T, err error, code taskworkspace.ErrorCode) {
	t.Helper()
	var lifecycleError *taskworkspace.Error
	if !errors.As(err, &lifecycleError) || lifecycleError.Code != code {
		t.Fatalf("lifecycle error = %T/%v, want code %q", err, err, code)
	}
}

func sandboxLeaseAuthority(
	policyDomainID, taskID, phaseRunID, runtimeRunID, sandboxLeaseID string,
) taskworkspace.SandboxLeaseAuthority {
	effectClass := taskworkspace.RuntimeViewMutating
	if sandboxLeaseID == "sandbox-lease-read-only" {
		effectClass = taskworkspace.RuntimeViewReadOnly
	}
	authority := taskworkspace.SandboxLeaseAuthority{
		ID:                 taskworkspace.SandboxLeaseID(sandboxLeaseID),
		EvidenceID:         taskworkspace.EvidenceID("lease-evidence-" + sandboxLeaseID),
		AuthorityID:        "sandbox-lease-authority-1",
		PolicyDomainID:     taskworkspace.PolicyDomainID(policyDomainID),
		TaskID:             taskworkspace.TaskID(taskID),
		PhaseRunID:         taskworkspace.PhaseRunID(phaseRunID),
		RuntimeRunID:       taskworkspace.RuntimeRunID(runtimeRunID),
		RuntimeOperationID: taskworkspace.OperationID("runtime-operation-" + runtimeRunID),
		EffectClass:        effectClass,
		LeaseGeneration:    7,
		LeaseFence:         11,
		ExpiresAt:          300,
	}
	authority.Digest = authority.CanonicalDigest()
	return authority
}

func acceptedValidationEvidence(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	view taskworkspace.OpenRuntimeViewResult,
	manifest taskworkspace.DeclaredStateManifest,
) taskworkspace.ValidationEvidence {
	evidence := taskworkspace.ValidationEvidence{
		ID:                          "validation-evidence-1",
		ValidationAuthorityID:       "validation-authority-1",
		PolicyDomainID:              "policy-domain-1",
		TaskID:                      "task-1",
		TaskWorkspaceID:             confirmed.TaskWorkspaceID,
		RuntimeViewID:               view.RuntimeViewID,
		BaseRevisionID:              confirmed.CurrentRevisionID,
		PhaseRunID:                  view.PhaseRunID,
		RuntimeRunID:                view.RuntimeRunID,
		RuntimeOperationID:          view.RuntimeOperationID,
		SandboxLeaseAuthorityDigest: view.SandboxLeaseAuthority.Digest,
		ManifestDigest:              manifest.Digest,
		Generation:                  confirmed.Generation,
		Fence:                       confirmed.Fence,
		Decision:                    taskworkspace.ValidationAccepted,
	}
	evidence.Digest = evidence.CanonicalDigest()
	return evidence
}

func commitRequest(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	view taskworkspace.OpenRuntimeViewResult,
	manifest taskworkspace.DeclaredStateManifest,
	evidence taskworkspace.ValidationEvidence,
	operationID string,
) taskworkspace.CommitRuntimeViewRequest {
	request := taskworkspace.CommitRuntimeViewRequest{
		PolicyDomainID:          "policy-domain-1",
		TaskID:                  "task-1",
		TaskWorkspaceID:         confirmed.TaskWorkspaceID,
		RuntimeViewID:           view.RuntimeViewID,
		RuntimeOperationID:      view.RuntimeOperationID,
		SandboxLeaseAuthority:   view.SandboxLeaseAuthority,
		BaseRevisionID:          confirmed.CurrentRevisionID,
		ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation:              confirmed.Generation,
		Fence:                   confirmed.Fence,
		ValidationEvidence:      evidence,
		DeclaredStateManifest:   manifest,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func discardRequest(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	view taskworkspace.OpenRuntimeViewResult,
	reason taskworkspace.RuntimeViewDiscardReason,
	operationID string,
) taskworkspace.DiscardRuntimeViewRequest {
	request := taskworkspace.DiscardRuntimeViewRequest{
		PolicyDomainID:          "policy-domain-1",
		TaskID:                  "task-1",
		TaskWorkspaceID:         confirmed.TaskWorkspaceID,
		RuntimeViewID:           view.RuntimeViewID,
		RuntimeOperationID:      view.RuntimeOperationID,
		SandboxLeaseAuthority:   view.SandboxLeaseAuthority,
		BaseRevisionID:          view.BaseRevisionID,
		ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation:              confirmed.Generation,
		Fence:                   confirmed.Fence,
		Reason:                  reason,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}

func fenceRequest(
	confirmed taskworkspace.ConfirmTaskWorkspaceResult,
	view taskworkspace.OpenRuntimeViewResult,
	reason taskworkspace.RuntimeViewFenceReason,
	operationID string,
) taskworkspace.FenceRuntimeViewRequest {
	request := taskworkspace.FenceRuntimeViewRequest{
		PolicyDomainID:          "policy-domain-1",
		TaskID:                  "task-1",
		TaskWorkspaceID:         confirmed.TaskWorkspaceID,
		RuntimeViewID:           view.RuntimeViewID,
		RuntimeOperationID:      view.RuntimeOperationID,
		SandboxLeaseAuthority:   view.SandboxLeaseAuthority,
		BaseRevisionID:          view.BaseRevisionID,
		ExpectedCurrentRevision: confirmed.CurrentRevisionID,
		Generation:              confirmed.Generation,
		Fence:                   confirmed.Fence,
		Reason:                  reason,
		Operation: taskworkspace.Operation{
			ID: taskworkspace.OperationID(operationID),
		},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request
}
