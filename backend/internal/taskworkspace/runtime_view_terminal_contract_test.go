package taskworkspace_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestConcurrentExactCommitReplayReturnsOneTerminalResult(t *testing.T) {
	reached := make(chan struct{}, 2)
	release := make(chan struct{})
	lifecycle := newLifecycleWithTerminalHook(func(attempt taskworkspace.RuntimeViewTerminalAttempt) {
		if attempt.OperationID == "commit-1" {
			reached <- struct{}{}
			<-release
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
	request := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-1")
	type outcome struct {
		result taskworkspace.CommitRuntimeViewResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, commitErr := lifecycle.CommitRuntimeView(context.Background(), request)
			outcomes <- outcome{result: result, err: commitErr}
		}()
	}
	<-reached
	<-reached
	close(release)

	first := <-outcomes
	second := <-outcomes
	if first.err != nil || second.err != nil {
		t.Fatal("concurrent exact replay did not return terminal success to both callers")
	}
	if !reflect.DeepEqual(first.result, second.result) || first.result.CheckpointID == "" {
		t.Fatal("concurrent exact replay created or returned a different terminal result")
	}
}

func TestDiscardAndFenceOperationPayloadConflictsAreTyped(t *testing.T) {
	t.Run("discard", func(t *testing.T) {
		lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
		request := discardRequest(confirmed, view, taskworkspace.RuntimeViewValidationRejected, "discard-1")
		if _, err := lifecycle.DiscardRuntimeView(context.Background(), request); err != nil {
			t.Fatalf("discard Runtime View: %v", err)
		}
		changed := request
		changed.Reason = taskworkspace.RuntimeViewRuntimeFailed
		changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
		_, err := lifecycle.DiscardRuntimeView(context.Background(), changed)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})

	t.Run("fence", func(t *testing.T) {
		lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
		request := fenceRequest(confirmed, view, taskworkspace.RuntimeViewCancelled, "fence-1")
		if _, err := lifecycle.FenceRuntimeView(context.Background(), request); err != nil {
			t.Fatalf("fence Runtime View: %v", err)
		}
		changed := request
		changed.Reason = taskworkspace.RuntimeViewRevoked
		changed.Operation.RequestDigest = changed.CanonicalRequestDigest()
		_, err := lifecycle.FenceRuntimeView(context.Background(), changed)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorIntegrityConflict)
	})
}

func TestCrossScopeAndUnknownRuntimeViewEvidenceFailClosedWithoutDisclosure(t *testing.T) {
	const canary = "/private/path/session-mount-s3-locator-vendor-credential"
	lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
	manifest := declaredStateManifest("content-1")
	base := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-cross-scope")

	foreign, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy"+canary, "task"+canary, "confirm-foreign",
	))
	if err != nil {
		t.Fatalf("confirm foreign Task Workspace: %v", err)
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
	if crossErr.Error() != unknownErr.Error() {
		t.Fatal("cross-scope and unknown authority produced distinguishable errors")
	}
	if crossResult.RevisionID != "" || crossResult.CheckpointID != "" ||
		unknownResult.RevisionID != "" || unknownResult.CheckpointID != "" {
		t.Fatal("ownership denial returned authoritative mutation identities")
	}

	crossEvidence := base
	crossEvidence.Operation.ID = "commit-cross-evidence"
	crossEvidence.ValidationEvidence.PolicyDomainID = "policy" + canary
	crossEvidence.ValidationEvidence.TaskID = "task" + canary
	crossEvidence.ValidationEvidence.Digest = crossEvidence.ValidationEvidence.CanonicalDigest()
	crossEvidence.Operation.RequestDigest = crossEvidence.CanonicalRequestDigest()
	_, evidenceErr := lifecycle.CommitRuntimeView(context.Background(), crossEvidence)
	assertLifecycleErrorCode(t, evidenceErr, taskworkspace.ErrorIntegrityFailure)

	formatted := fmt.Sprint(crossResult, crossErr, unknownResult, unknownErr, evidenceErr)
	for _, fragment := range strings.Split(strings.TrimPrefix(canary, "/"), "-") {
		if strings.Contains(strings.ToLower(formatted), strings.ToLower(fragment)) {
			t.Fatal("lifecycle denial disclosed a protected canary fragment")
		}
	}
}

func TestOpenRuntimeViewRejectsInvalidRuntimeAndLeaseAuthority(t *testing.T) {
	tests := []struct {
		name string
		code taskworkspace.ErrorCode
		now  taskworkspace.Instant
		edit func(*taskworkspace.OpenRuntimeViewRequest)
	}{
		{
			name: "unknown effect class",
			code: taskworkspace.ErrorInvalidIntent,
			edit: func(request *taskworkspace.OpenRuntimeViewRequest) {
				request.EffectClass = "unknown"
			},
		},
		{
			name: "view expiry exceeds lease authority",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.OpenRuntimeViewRequest) {
				request.ExpiresAt = 301
			},
		},
		{
			name: "lease authority is expired",
			code: taskworkspace.ErrorStaleAuthority,
			now:  301,
			edit: func(*taskworkspace.OpenRuntimeViewRequest) {},
		},
		{
			name: "caller minted lease fence",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.OpenRuntimeViewRequest) {
				request.SandboxLeaseAuthority.LeaseFence++
				request.SandboxLeaseAuthority.Digest = request.SandboxLeaseAuthority.CanonicalDigest()
			},
		},
		{
			name: "different Runtime Operation",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.OpenRuntimeViewRequest) {
				request.RuntimeOperationID = "runtime-operation-not-authorized"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := test.now
			if now == 0 {
				now = 100
			}
			lifecycle := newLifecycleWithHarness(func() taskworkspace.Instant { return now }, nil)
			confirmed, materialized := materializedTaskUsing(t, lifecycle)
			request := openRuntimeViewRequest(
				"policy-domain-1", "task-1", confirmed, materialized,
				"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-1",
			)
			test.edit(&request)
			request.Operation.RequestDigest = request.CanonicalRequestDigest()

			result, err := lifecycle.OpenRuntimeView(context.Background(), request)
			assertLifecycleErrorCode(t, err, test.code)
			if result.RuntimeViewID != "" || result.TaskWorkspaceID != "" {
				t.Fatal("rejected Runtime View open returned authority identities")
			}
		})
	}
}

func TestOpenRuntimeViewCannotEscalateReadOnlyLeaseAuthority(t *testing.T) {
	lifecycle := newLifecycle()
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	request := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-read-only", "sandbox-lease-read-only", "open-view-read-only",
	)
	if request.SandboxLeaseAuthority.EffectClass != taskworkspace.RuntimeViewReadOnly ||
		request.EffectClass != taskworkspace.RuntimeViewMutating {
		t.Fatal("test fixture did not create an attempted read-only authority escalation")
	}

	_, err := lifecycle.OpenRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
}

func TestCommitRejectsStaleRuntimeAndLeaseAuthority(t *testing.T) {
	tests := []struct {
		name string
		code taskworkspace.ErrorCode
		edit func(*taskworkspace.CommitRuntimeViewRequest, *taskworkspace.Instant)
	}{
		{
			name: "different Runtime Operation",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest, _ *taskworkspace.Instant) {
				request.RuntimeOperationID = "runtime-operation-not-authorized"
			},
		},
		{
			name: "different lease evidence",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest, _ *taskworkspace.Instant) {
				request.SandboxLeaseAuthority.LeaseFence++
				request.SandboxLeaseAuthority.Digest = request.SandboxLeaseAuthority.CanonicalDigest()
			},
		},
		{
			name: "expired Runtime View",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(_ *taskworkspace.CommitRuntimeViewRequest, now *taskworkspace.Instant) {
				*now = 201
			},
		},
		{
			name: "expired Sandbox Lease",
			code: taskworkspace.ErrorStaleAuthority,
			edit: func(_ *taskworkspace.CommitRuntimeViewRequest, now *taskworkspace.Instant) {
				*now = 301
			},
		},
		{
			name: "validation evidence has another Runtime Operation",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest, _ *taskworkspace.Instant) {
				request.ValidationEvidence.RuntimeOperationID = "runtime-operation-not-validated"
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
			},
		},
		{
			name: "validation evidence has another lease authority",
			code: taskworkspace.ErrorIntegrityFailure,
			edit: func(request *taskworkspace.CommitRuntimeViewRequest, _ *taskworkspace.Instant) {
				request.ValidationEvidence.SandboxLeaseAuthorityDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			manifest := declaredStateManifest("content-1")
			request := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-1")
			test.edit(&request, &now)
			request.Operation.RequestDigest = request.CanonicalRequestDigest()

			result, err := lifecycle.CommitRuntimeView(context.Background(), request)
			var lifecycleError *taskworkspace.Error
			if !errors.As(err, &lifecycleError) || lifecycleError.Code != test.code {
				t.Fatalf("commit error = %T/%v, want code %q", err, err, test.code)
			}
			if result.RevisionID != "" || result.CheckpointID != "" {
				t.Fatal("rejected commit returned authoritative mutation identities")
			}
		})
	}
}

func TestCommitValidatesExactBindingBeforeDisclosingTerminalState(t *testing.T) {
	for _, edit := range []func(*taskworkspace.CommitRuntimeViewRequest){
		func(request *taskworkspace.CommitRuntimeViewRequest) {
			request.RuntimeOperationID = "runtime-operation-not-authorized"
		},
		func(request *taskworkspace.CommitRuntimeViewRequest) {
			request.SandboxLeaseAuthority.LeaseFence++
			request.SandboxLeaseAuthority.Digest = request.SandboxLeaseAuthority.CanonicalDigest()
		},
	} {
		lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
		if _, err := lifecycle.DiscardRuntimeView(context.Background(), discardRequest(
			confirmed, view, taskworkspace.RuntimeViewRuntimeFailed, "discard-1",
		)); err != nil {
			t.Fatalf("discard Runtime View: %v", err)
		}
		manifest := declaredStateManifest("content-1")
		request := commitRequest(confirmed, view, manifest, acceptedValidationEvidence(confirmed, view, manifest), "commit-after-discard")
		edit(&request)
		request.Operation.RequestDigest = request.CanonicalRequestDigest()

		_, err := lifecycle.CommitRuntimeView(context.Background(), request)
		assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	}
}

func TestHistoricalSandboxLeaseAuthorityCannotOpenRuntimeView(t *testing.T) {
	oldAuthority := sandboxLeaseAuthority(
		"policy-domain-1", "task-1", "phase-run-1", "runtime-run-1", "sandbox-lease-1",
	)
	currentAuthority := oldAuthority
	currentAuthority.EvidenceID = "lease-evidence-current"
	currentAuthority.LeaseFence++
	currentAuthority.Digest = currentAuthority.CanonicalDigest()
	lifecycle := taskworkspace.NewInMemory(taskworkspace.InMemoryConfig{
		SandboxLeaseAuthorityID: "sandbox-lease-authority-1",
		CurrentSandboxLeaseAuthorities: []taskworkspace.SandboxLeaseAuthority{
			oldAuthority,
			currentAuthority,
		},
		Now: func() taskworkspace.Instant { return 100 },
	})
	confirmed, materialized := materializedTaskUsing(t, lifecycle)
	request := openRuntimeViewRequest(
		"policy-domain-1", "task-1", confirmed, materialized,
		"phase-run-1", "runtime-run-1", "sandbox-lease-1", "open-view-with-old-lease",
	)

	_, err := lifecycle.OpenRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
}

func TestCommitRejectsSandboxLeaseAuthoritySupersededAfterViewOpen(t *testing.T) {
	current := sandboxLeaseAuthority(
		"policy-domain-1", "task-1", "phase-run-1", "runtime-run-1", "sandbox-lease-1",
	)
	config := taskworkspaceTestConfig(&happyDurableObject{})
	config.CurrentSandboxLeaseAuthority = func(id taskworkspace.SandboxLeaseID) (taskworkspace.SandboxLeaseAuthority, bool) {
		if id != current.ID {
			return taskworkspace.SandboxLeaseAuthority{}, false
		}
		return current, true
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

	current.EvidenceID = "lease-evidence-superseding"
	current.LeaseFence++
	current.Digest = current.CanonicalDigest()
	manifest := declaredStateManifest("content-1")
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-with-superseded-lease",
	)

	result, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("superseded Sandbox Lease authority returned mutation identities")
	}
}

func TestCommitRevalidatesSandboxLeaseAuthorityAfterDurablePreparation(t *testing.T) {
	current := sandboxLeaseAuthority(
		"policy-domain-1", "task-1", "phase-run-1", "runtime-run-1", "sandbox-lease-1",
	)
	durable := &happyDurableObject{
		mutate: func(*taskworkspace.VerifiedCheckpointContent) {
			current.EvidenceID = "lease-evidence-superseding-during-prepare"
			current.LeaseFence++
			current.Digest = current.CanonicalDigest()
		},
	}
	config := taskworkspaceTestConfig(durable)
	config.CurrentSandboxLeaseAuthority = func(id taskworkspace.SandboxLeaseID) (taskworkspace.SandboxLeaseAuthority, bool) {
		if id != current.ID {
			return taskworkspace.SandboxLeaseAuthority{}, false
		}
		return current, true
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
	request := commitRequest(
		confirmed,
		view,
		manifest,
		acceptedValidationEvidence(confirmed, view, manifest),
		"commit-racing-lease-supersession",
	)

	result, err := lifecycle.CommitRuntimeView(context.Background(), request)
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
	if result.RevisionID != "" || result.CheckpointID != "" {
		t.Fatal("lease supersession during durable preparation returned mutation identities")
	}
	workspace, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-lease-supersession",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace: %v", err)
	}
	if workspace.CurrentRevisionID != confirmed.CurrentRevisionID || workspace.CurrentCheckpointID != confirmed.CurrentCheckpointID ||
		workspace.Fence != confirmed.Fence {
		t.Fatal("lease supersession during durable preparation activated authoritative state")
	}
}

func TestCancellationTimeoutAndRevocationAdvanceRuntimeViewFence(t *testing.T) {
	for _, reason := range []taskworkspace.RuntimeViewFenceReason{
		taskworkspace.RuntimeViewCancelled,
		taskworkspace.RuntimeViewTimedOut,
		taskworkspace.RuntimeViewRevoked,
	} {
		t.Run(string(reason), func(t *testing.T) {
			lifecycle, confirmed, _, view := openedRuntimeView(t, "runtime-run-1", "open-view-1")
			request := fenceRequest(confirmed, view, reason, "fence-1")

			fenced, err := lifecycle.FenceRuntimeView(context.Background(), request)
			if err != nil {
				t.Fatalf("fence Runtime View: %v", err)
			}
			if fenced.PreviousFence != confirmed.Fence || fenced.Fence <= confirmed.Fence ||
				fenced.Generation != confirmed.Generation || fenced.CurrentRevisionID != confirmed.CurrentRevisionID {
				t.Fatal("runtime terminal intent did not advance only the C04 fence")
			}
		})
	}
}

func TestRecoveryGenerationAdvanceFencesEveryPreRecoveryRuntimeView(t *testing.T) {
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

	advanced, err := lifecycle.FenceRuntimeView(context.Background(), fenceRequest(
		confirmed, firstView, taskworkspace.RuntimeViewRecoveryGenerationAdvanced, "advance-recovery-generation",
	))
	if err != nil {
		t.Fatalf("advance recovery generation: %v", err)
	}
	if advanced.Generation <= confirmed.Generation || advanced.Fence <= confirmed.Fence ||
		advanced.CurrentRevisionID != confirmed.CurrentRevisionID {
		t.Fatal("recovery generation advance changed Revision or failed to advance generation and fence")
	}

	current, err := lifecycle.ConfirmTaskWorkspace(context.Background(), confirmRequest(
		"policy-domain-1", "task-1", "confirm-after-recovery-generation",
	))
	if err != nil {
		t.Fatalf("confirm Task Workspace after recovery generation advance: %v", err)
	}
	if current.Generation != advanced.Generation || current.Fence != advanced.Fence ||
		current.CurrentRevisionID != confirmed.CurrentRevisionID {
		t.Fatal("current Task Workspace authority does not reflect recovery generation advance")
	}

	manifest := declaredStateManifest("content-1")
	evidence := acceptedValidationEvidence(confirmed, secondView, manifest)
	_, err = lifecycle.CommitRuntimeView(context.Background(), commitRequest(
		confirmed, secondView, manifest, evidence, "late-pre-recovery-commit",
	))
	assertLifecycleErrorCode(t, err, taskworkspace.ErrorStaleAuthority)
}
