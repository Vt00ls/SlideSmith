package runtimeexecution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

func TestLateRuntimeViewAcceptanceHasOneClosedTerminalObligationMapping(t *testing.T) {
	t.Parallel()

	cleanup := RuntimeLeaseCleanupSnapshot{FenceRuntimeView: true}
	activeLease := RuntimeLeaseSnapshot{AcquireStatus: LeaseGranted, Disposition: LeaseActive}
	tests := []struct {
		name       string
		state      RuntimeState
		outcome    RuntimeOutcome
		cleanup    RuntimeLeaseCleanupSnapshot
		lease      RuntimeLeaseSnapshot
		obligation runtimeViewTerminalObligation
		valid      bool
	}{
		{
			name: "cancelled fence", state: RuntimeTerminal, outcome: RuntimeCancelled,
			lease: activeLease, valid: true,
			obligation: runtimeViewTerminalObligation{
				Kind: runtimeViewTerminalFence, FenceReason: taskworkspace.RuntimeViewCancelled,
			},
		},
		{
			name: "rejected discard", state: RuntimeTerminal, outcome: RuntimeRejected,
			lease: activeLease, valid: true,
			obligation: runtimeViewTerminalObligation{
				Kind: runtimeViewTerminalDiscard, DiscardReason: taskworkspace.RuntimeViewValidationRejected,
			},
		},
		{
			name: "timed out discard", state: RuntimeTerminal, outcome: RuntimeTimedOut,
			lease: activeLease, valid: true,
			obligation: runtimeViewTerminalObligation{
				Kind: runtimeViewTerminalDiscard, DiscardReason: taskworkspace.RuntimeViewRuntimeFailed,
			},
		},
		{
			name: "failed discard", state: RuntimeTerminal, outcome: RuntimeFailed,
			lease: activeLease, valid: true,
			obligation: runtimeViewTerminalObligation{
				Kind: runtimeViewTerminalDiscard, DiscardReason: taskworkspace.RuntimeViewRuntimeFailed,
			},
		},
		{
			name: "revoked fence", state: RuntimeStopping, outcome: RuntimeOutcomeNone,
			cleanup: cleanup, lease: RuntimeLeaseSnapshot{
				AcquireStatus: LeaseGranted, Disposition: LeaseRevoked,
			},
			valid: true, obligation: runtimeViewTerminalObligation{
				Kind: runtimeViewTerminalFence, FenceReason: taskworkspace.RuntimeViewRevoked,
			},
		},
		{
			name: "expired fence", state: RuntimeStopping, outcome: RuntimeOutcomeNone,
			cleanup: cleanup, lease: RuntimeLeaseSnapshot{
				AcquireStatus: LeaseGranted, Disposition: LeaseExpired,
			},
			valid: true, obligation: runtimeViewTerminalObligation{
				Kind: runtimeViewTerminalFence, FenceReason: taskworkspace.RuntimeViewTimedOut,
			},
		},
		{
			name: "successful terminal has no cleanup", state: RuntimeTerminal, outcome: RuntimeSucceeded,
			lease: activeLease,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			obligation, valid := runtimeViewTerminalObligationFor(
				test.state, test.outcome, test.cleanup, test.lease,
			)
			if valid != test.valid || obligation != test.obligation {
				t.Fatalf("terminal obligation=(%+v,%t), want (%+v,%t)",
					obligation, valid, test.obligation, test.valid)
			}
		})
	}
}

func TestReadOnlyCapsuleReadinessRequiresDurableRuntimeBindingAndImmutableInputs(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "readiness-read-only-authority", 7)
	start := standardStart(t, now, authority, "readiness-read-only")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "readiness-read-only-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	var mu sync.Mutex
	bindingCalls := 0
	inputCalls := 0
	bindingValidator := RuntimeBindingValidatorFunc(func(
		_ context.Context,
		request RuntimeBindingValidationRequest,
	) (PrerequisiteObservation, error) {
		mu.Lock()
		defer mu.Unlock()
		bindingCalls++
		authorization := request.Authorization
		if authorization.RuntimeRunID != start.RuntimeRunID || authorization.RuntimeBindingID != start.RuntimeBindingID ||
			authorization.RuntimeBindingDigest != start.RuntimeBindingDigest ||
			authorization.ExecutionLockDigest != start.ExecutionLockDigest ||
			authorization.CapabilityContractDigest != start.CapabilityContractDigest ||
			authorization.AllowedPlatformImagesDigest != start.AllowedPlatformImagesDigest ||
			authorization.ExecutorContractDigest != start.ExecutorContractDigest ||
			authorization.ReleaseSafetyEpoch != start.ReleaseSafetyEpoch || authorization.CatalogBinding != nil {
			t.Fatalf("exact Runtime Binding request drifted: %+v", request)
		}
		return acceptedPrerequisiteObservation(t, "release-binding-evidence", digest(81)), nil
	})
	inputValidator := ImmutableInputValidatorFunc(func(
		_ context.Context,
		request ImmutableInputValidationRequest,
	) (PrerequisiteObservation, error) {
		mu.Lock()
		defer mu.Unlock()
		inputCalls++
		if request.Authorization.PersonalWorkspaceID != start.PersonalWorkspaceID ||
			request.Authorization.TaskID != start.TaskID || request.Authorization.RuntimeRunID != start.RuntimeRunID ||
			request.Manifest != start.ImmutableInputManifest ||
			len(request.Inputs) != len(start.ImmutableInputs) || request.Inputs[0] != start.ImmutableInputs[0] {
			t.Fatalf("immutable input request drifted: %+v", request)
		}
		requestValue := reflect.ValueOf(request.Authorization)
		for field, want := range map[string]any{
			"RuntimeBindingID":            start.RuntimeBindingID,
			"RuntimeBindingDigest":        start.RuntimeBindingDigest,
			"ExecutionLockDigest":         start.ExecutionLockDigest,
			"CapabilityContractDigest":    start.CapabilityContractDigest,
			"AllowedPlatformImagesDigest": start.AllowedPlatformImagesDigest,
			"ExecutorContractDigest":      start.ExecutorContractDigest,
			"OutputContractDigest":        start.OutputContractDigest,
			"EvidenceContractDigest":      start.EvidenceContractDigest,
			"ReleaseSafetyEpoch":          start.ReleaseSafetyEpoch,
			"CatalogBinding":              start.CatalogBinding,
		} {
			got := requestValue.FieldByName(field)
			if !got.IsValid() || !reflect.DeepEqual(got.Interface(), want) {
				t.Fatalf("immutable input request lacks exact %s compatibility: got=%v want=%v", field, got, want)
			}
		}
		return acceptedPrerequisiteObservation(t, "input-materialization-evidence", digest(82)), nil
	})

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator,
		ImmutableInputValidator: inputValidator,
	})
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute read-only start: %v", err)
	}
	readiness := accepted.Snapshot.Readiness
	if !readiness.CapsuleReady || readiness.Lease.State != PrerequisiteAccepted ||
		readiness.RuntimeBinding.State != PrerequisiteAccepted ||
		readiness.RuntimeView.State != PrerequisiteNotApplicable ||
		readiness.ImmutableInputs.State != PrerequisiteAccepted ||
		readiness.LLMGateway.State != PrerequisiteNotApplicable {
		t.Fatalf("read-only readiness = %+v", readiness)
	}

	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed != accepted {
		t.Fatalf("exact replay = %+v err=%v, want %+v", replayed, err, accepted)
	}
	mu.Lock()
	defer mu.Unlock()
	if bindingCalls != 2 || inputCalls != 1 {
		t.Fatalf("post-lease replay did not isolate binding revalidation: binding=%d input=%d", bindingCalls, inputCalls)
	}
}

func TestCapsuleReadinessExpiresWithLeaseAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "readiness-expiry-authority", 7)
	start := standardStart(t, now, authority, "readiness-expiry")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "readiness-expiry-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "readiness-expiry-binding", digest(121)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "readiness-expiry-input", digest(122)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("ready before lease expiry: %+v err=%v", ready, err)
	}
	if err := harness.AdvanceClock(ready.Snapshot.Lease.ExpiresAt.Sub(now)); err != nil {
		t.Fatal(err)
	}
	ref := RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), ref)
	if err != nil || inspected.Readiness.CapsuleReady {
		t.Fatalf("Inspect retained readiness after lease expiry: %+v err=%v", inspected, err)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("Execute replay retained readiness after lease expiry: %+v err=%v", replayed, err)
	}
}

func TestZeroEntryImmutableInputManifestStillRequiresValidation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 23, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "empty-input-manifest-authority", 7)
	input := standardStart(t, now, authority, "empty-input-manifest").StartRuntimeRunInput
	input.ImmutableInputs = nil
	input.ImmutableInputManifest.InputCount = 0
	input.ImmutableInputManifest.TotalSizeBytes = 0
	input.ImmutableInputManifest.MaterializationEvidenceID = EvidenceID{}
	input.ImmutableInputManifest.MaterializationEvidenceDigest = Digest{}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "empty-input-manifest-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	validationCalls := 0
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "empty-input-binding", digest(123)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			_ context.Context,
			request ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			validationCalls++
			if request.Manifest != start.ImmutableInputManifest || len(request.Inputs) != 0 ||
				request.Authorization.RuntimeRunID != start.RuntimeRunID ||
				request.Authorization.RuntimeBindingDigest != start.RuntimeBindingDigest {
				t.Fatalf("zero-entry manifest authority drifted: %+v", request)
			}
			return acceptedPrerequisiteObservation(t, "empty-input-manifest-evidence", digest(124)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || validationCalls != 1 ||
		ready.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteAccepted {
		t.Fatalf("zero-entry manifest bypassed validation: %+v err=%v calls=%d", ready, err, validationCalls)
	}
}

func TestPostgresCapsuleReadinessExpiresWithExactRuntimeViewAuthority(t *testing.T) {
	current := time.Date(2026, time.July, 29, 20, 25, 0, 0, time.UTC)
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			result := acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("readiness-expiry-view"))
			result.ExpiresAt -= taskworkspace.Instant(30 * time.Second)
			return result, nil
		},
	}
	_, _, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "ready_expiry", current, func() time.Time { return current }, lifecycle, nil,
	)
	ready, err := store.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady ||
		!ready.Snapshot.RuntimeViewBinding.ExpiresAt.Before(ready.Snapshot.Lease.ExpiresAt) {
		t.Fatalf("ready before Runtime View expiry: %+v err=%v", ready, err)
	}
	current = ready.Snapshot.RuntimeViewBinding.ExpiresAt
	ref := RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	}
	inspected, err := store.Inspect(context.Background(), ref)
	if err != nil || inspected.Readiness.CapsuleReady {
		t.Fatalf("PostgreSQL Inspect retained readiness after Runtime View expiry: %+v err=%v", inspected, err)
	}
	replayed, err := store.Execute(context.Background(), start)
	if err != nil || replayed.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("PostgreSQL Execute retained readiness after Runtime View expiry: %+v err=%v", replayed, err)
	}
}

func TestMutatingRuntimeOpensExactlyOnePostLeaseC04View(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "readiness-mutating-authority", 7)
	input := standardStart(t, now, authority, "readiness-mutating").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "readiness-mutating-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "readiness-mutating-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "readiness-mutating-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(83),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "readiness-mutating-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	var mu sync.Mutex
	openCalls := 0
	var opened taskworkspace.OpenRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			openCalls++
			opened = request
			return taskworkspace.OpenRuntimeViewResult{
				PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
				RuntimeViewID:   "runtime-view-readiness-mutating",
				TaskWorkspaceID: request.TaskWorkspaceID, MaterializationID: request.MaterializationID,
				BaseRevisionID: request.BaseRevisionID, PhaseRunID: request.PhaseRunID,
				RuntimeRunID: request.RuntimeRunID, RuntimeOperationID: request.RuntimeOperationID,
				SandboxLeaseAuthority: request.SandboxLeaseAuthority, EffectClass: request.EffectClass,
				ExpiresAt: request.ExpiresAt, Generation: request.Generation, Fence: request.Fence,
				Operation: request.Operation,
			}, nil
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)

	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute mutating start: %v", err)
	}
	if accepted.Snapshot.Readiness.RuntimeView.State != PrerequisiteAccepted ||
		!accepted.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("mutating readiness = %+v", accepted.Snapshot.Readiness)
	}
	binding := accepted.Snapshot.RuntimeViewBinding
	mu.Lock()
	if openCalls != 1 || opened.Operation.ID == "" || opened.Operation.RequestDigest != opened.CanonicalRequestDigest() ||
		opened.RuntimeRunID != taskworkspace.RuntimeRunID(start.RuntimeRunID.String()) ||
		opened.RuntimeOperationID != taskworkspace.OperationID(start.OperationID.String()) ||
		opened.SandboxLeaseAuthority.ID != taskworkspace.SandboxLeaseID(accepted.Snapshot.Lease.LeaseID.String()) ||
		opened.SandboxLeaseAuthority.LeaseGeneration != taskworkspace.LeaseGeneration(accepted.Snapshot.Lease.Generation) ||
		opened.SandboxLeaseAuthority.LeaseFence != taskworkspace.LeaseFence(accepted.Snapshot.Lease.Fence) ||
		opened.SandboxLeaseAuthority.Digest != opened.SandboxLeaseAuthority.CanonicalDigest() {
		mu.Unlock()
		t.Fatalf("C04 open request lost exact authority: calls=%d request=%+v", openCalls, opened)
	}
	mu.Unlock()
	if binding.RuntimeViewID.String() != "runtime-view-readiness-mutating" ||
		binding.OpenOperationID.String() != string(opened.Operation.ID) ||
		binding.OpenRequestDigest.String() != string(opened.Operation.RequestDigest)[len("sha256:"):] ||
		binding.SandboxLeaseID != accepted.Snapshot.Lease.LeaseID ||
		binding.LeaseGeneration != accepted.Snapshot.Lease.Generation ||
		binding.LeaseFence != accepted.Snapshot.Lease.Fence || binding.Effect != EffectMutating {
		t.Fatalf("durable Runtime View binding = %+v", binding)
	}

	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed != accepted {
		t.Fatalf("exact replay = %+v err=%v", replayed, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if openCalls != 1 {
		t.Fatalf("exact replay opened %d Runtime Views", openCalls)
	}
}

func TestC04OpenNeverRunsBeforeSandboxLeaseCommit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "readiness-no-lease-authority", 7)
	input := standardStart(t, now, authority, "readiness-no-lease").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "readiness-no-lease-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "readiness-no-lease-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "readiness-no-lease-revision"),
		LifecycleGeneration: 2, LifecycleFence: 3,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(84),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "readiness-no-lease-node")
	grant.NodeCapacityGeneration = 1
	openCalls := 0
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionTemporaryUnavailable}, nil
		}),
		RuntimeViewPrerequisite: RuntimeViewPrerequisiteAdapter{
			OpenRuntimeViewFunc: func(context.Context, taskworkspace.OpenRuntimeViewRequest) (taskworkspace.OpenRuntimeViewResult, error) {
				openCalls++
				return taskworkspace.OpenRuntimeViewResult{}, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if openCalls != 0 || decision.Snapshot.Lease.AcquireStatus != LeaseAcquirePending ||
		decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("pre-lease C04 effect: calls=%d snapshot=%+v", openCalls, decision.Snapshot)
	}
}

func TestC04ResponseLossUsesInspectForTheOriginalOperation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 50, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-response-loss-authority", 7)
	input := standardStart(t, now, authority, "c04-response-loss").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-response-loss-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-response-loss-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-response-loss-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(90),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-response-loss-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	openCalls := 0
	inspectCalls := 0
	var retained taskworkspace.OpenRuntimeViewResult
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			retained = taskworkspace.OpenRuntimeViewResult{
				PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
				RuntimeViewID: "c04-response-loss-view", TaskWorkspaceID: request.TaskWorkspaceID,
				MaterializationID: request.MaterializationID, BaseRevisionID: request.BaseRevisionID,
				PhaseRunID: request.PhaseRunID, RuntimeRunID: request.RuntimeRunID,
				RuntimeOperationID: request.RuntimeOperationID, SandboxLeaseAuthority: request.SandboxLeaseAuthority,
				EffectClass: request.EffectClass, ExpiresAt: request.ExpiresAt,
				Generation: request.Generation, Fence: request.Fence, Operation: request.Operation,
			}
			return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired}
		},
		InspectOperationFunc: func(
			_ context.Context,
			request taskworkspace.InspectOperationRequest,
		) (taskworkspace.OperationInspection, error) {
			inspectCalls++
			if request.OperationID != retained.Operation.ID {
				t.Fatalf("inspected %q, want original %q", request.OperationID, retained.Operation.ID)
			}
			result := retained
			return taskworkspace.OperationInspection{
				Operation: retained.Operation, Disposition: taskworkspace.OperationTerminal,
				IntentState: taskworkspace.OperationIntentActivated, OpenRuntimeView: &result,
			}, nil
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !decision.Snapshot.Readiness.CapsuleReady ||
		decision.Snapshot.RuntimeViewBinding.RuntimeViewID.String() != "c04-response-loss-view" {
		t.Fatalf("response-loss reconciliation = %+v err=%v", decision, err)
	}
	if openCalls != 1 || inspectCalls != 1 {
		t.Fatalf("response loss calls: open=%d inspect=%d", openCalls, inspectCalls)
	}
}

func TestPostOpenCancelFencesTheExactC04RuntimeViewOnce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 52, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-cancel-authority", 7)
	input := standardStart(t, now, authority, "c04-cancel").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-cancel-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-cancel-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-cancel-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(96),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-cancel-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	var mu sync.Mutex
	var opened taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	discardCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			opened = request
			return acceptedRuntimeViewResult(request, "c04-cancel-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			fenced = append(fenced, request)
			return taskworkspace.FenceRuntimeViewResult{
				TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
				BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
				Reason: request.Reason, Generation: request.Generation,
				PreviousFence: request.Fence, Fence: request.Fence + 1, Operation: request.Operation,
			}, nil
		},
		DiscardRuntimeViewFunc: func(
			context.Context,
			taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			discardCalls++
			return taskworkspace.DiscardRuntimeViewResult{}, nil
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	started, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("start mutating Runtime: %+v err=%v", started, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "c04-cancel-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("cancel Runtime: %+v err=%v", cancelled, err)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil || replayed != cancelled {
		t.Fatalf("replay cancel: %+v err=%v want=%+v", replayed, err, cancelled)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fenced) != 1 || discardCalls != 0 {
		t.Fatalf("terminal calls: fence=%d discard=%d", len(fenced), discardCalls)
	}
	request := fenced[0]
	if request.RuntimeViewID != "c04-cancel-view" || request.RuntimeOperationID != opened.RuntimeOperationID ||
		request.SandboxLeaseAuthority != opened.SandboxLeaseAuthority ||
		request.SandboxLeaseAuthority.LeaseGeneration != opened.SandboxLeaseAuthority.LeaseGeneration ||
		request.SandboxLeaseAuthority.LeaseFence != opened.SandboxLeaseAuthority.LeaseFence ||
		request.Reason != taskworkspace.RuntimeViewCancelled || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		t.Fatalf("cancel did not retain exact C04 binding: open=%+v fence=%+v", opened, request)
	}
}

func TestRejectedImmutableInputsDiscardTheOpenedC04RuntimeViewOnce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 54, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-discard-authority", 7)
	input := standardStart(t, now, authority, "c04-discard").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-discard-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-discard-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-discard-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(97),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-discard-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	var mu sync.Mutex
	var opened taskworkspace.OpenRuntimeViewRequest
	var discarded []taskworkspace.DiscardRuntimeViewRequest
	fenceCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			opened = request
			return acceptedRuntimeViewResult(request, "c04-discard-view"), nil
		},
		FenceRuntimeViewFunc: func(
			context.Context,
			taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			fenceCalls++
			return taskworkspace.FenceRuntimeViewResult{}, nil
		},
		DiscardRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			discarded = append(discarded, request)
			if len(discarded) == 1 {
				return taskworkspace.DiscardRuntimeViewResult{}, &taskworkspace.Error{
					Code: taskworkspace.ErrorRetryableUnavailable,
				}
			}
			return taskworkspace.DiscardRuntimeViewResult{
				TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
				BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
				Reason: request.Reason, Generation: request.Generation, Fence: request.Fence,
				Operation: request.Operation,
			}, nil
		},
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "discard-release-evidence", digest(98)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return PrerequisiteObservation{
				Disposition: PrerequisiteObservationRejected, Failure: PrerequisiteFailureCorrupt,
			}, nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || decision.Snapshot.State != RuntimeTerminal || decision.Snapshot.Outcome != RuntimeRejected ||
		decision.Snapshot.Lease.Disposition != LeaseRevoked ||
		decision.Snapshot.Cleanup.FenceRuntimeView ||
		decision.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteRejected ||
		decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("input rejection: %+v err=%v", decision, err)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed != decision {
		t.Fatalf("replay rejected inputs: %+v err=%v want=%+v", replayed, err, decision)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(discarded) != 2 || discarded[0] != discarded[1] || fenceCalls != 0 {
		t.Fatalf("terminal calls: discard=%d fence=%d", len(discarded), fenceCalls)
	}
	request := discarded[0]
	if request.RuntimeViewID != "c04-discard-view" || request.RuntimeOperationID != opened.RuntimeOperationID ||
		request.SandboxLeaseAuthority != opened.SandboxLeaseAuthority ||
		request.Reason != taskworkspace.RuntimeViewValidationRejected || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		t.Fatalf("discard did not retain exact C04 binding: open=%+v discard=%+v", opened, request)
	}
	if _, err := harness.Runtime.Execute(context.Background(), start); err != nil || len(discarded) != 2 {
		t.Fatalf("settled discard replay redelivered: err=%v discard=%d", err, len(discarded))
	}
}

func TestNodeLossFencesTheExactOpenedC04RuntimeViewOnce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 56, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-node-loss-authority", 7)
	input := standardStart(t, now, authority, "c04-node-loss").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-node-loss-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-node-loss-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-node-loss-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(99),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-node-loss-node")
	grant.NodeCapacityGeneration = 2
	node := executionNodeFixtureForStart(t, start, grant, now)
	fencingAuthority := NewSecurityLeaseFencingAuthority(mustAuthorityID(t, "c04-node-loss-security"), 2)

	var mu sync.Mutex
	var opened taskworkspace.OpenRuntimeViewRequest
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			opened = request
			return acceptedRuntimeViewResult(request, "c04-node-loss-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			fenced = append(fenced, request)
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
			BindLeaseFencingAuthority(grant.ExecutionNodeID, fencingAuthority),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "node-loss-release-evidence", digest(100)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "node-loss-input-evidence", digest(101)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("start mutating Runtime: %+v err=%v", started, err)
	}
	lease := started.Snapshot.Lease
	fence, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "c04-node-loss-fence"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeFence: started.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: grant.ExecutionNodeID, NodeGeneration: 2, Reason: LeaseFenceNodeLost,
		Authority: fencingAuthority, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.Maintenance.Maintain(context.Background(), fence)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := harness.Maintenance.Maintain(context.Background(), fence)
	if err != nil || !replayed.Replayed || replayed.OperationID != first.OperationID {
		t.Fatalf("replay lease fence: %+v err=%v", replayed, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fenced) != 1 {
		t.Fatalf("C04 fences = %d, want 1", len(fenced))
	}
	request := fenced[0]
	if request.RuntimeViewID != "c04-node-loss-view" || request.SandboxLeaseAuthority != opened.SandboxLeaseAuthority ||
		request.Reason != taskworkspace.RuntimeViewRevoked ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		t.Fatalf("node loss C04 fence drifted: open=%+v fence=%+v", opened, request)
	}
}

func TestC04OpenAmbiguityReconcilesTheOriginalRequestButRejectsStaleLeaseAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 58, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-open-repin-authority", 7)
	input := standardStart(t, now, authority, "c04-open-repin").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-open-repin-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-open-repin-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-open-repin-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(105),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-open-repin-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	var mu sync.Mutex
	var requests []taskworkspace.OpenRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, request)
			if len(requests) == 1 {
				return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{
					Code: taskworkspace.ErrorReconciliationRequired,
				}
			}
			return acceptedRuntimeViewResult(request, "c04-open-repin-view"), nil
		},
		InspectOperationFunc: func(
			context.Context,
			taskworkspace.InspectOperationRequest,
		) (taskworkspace.OperationInspection, error) {
			return taskworkspace.OperationInspection{}, &taskworkspace.Error{
				Code: taskworkspace.ErrorRetryableUnavailable,
			}
		},
		ReconcileOperationFunc: func(
			context.Context,
			taskworkspace.ReconcileOperationRequest,
		) (taskworkspace.OperationInspection, error) {
			return taskworkspace.OperationInspection{}, &taskworkspace.Error{
				Code: taskworkspace.ErrorRetryableUnavailable,
			}
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	first, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || first.Snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired ||
		first.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("first ambiguous open: %+v err=%v", first, err)
	}
	lease := first.Snapshot.Lease
	renew, err := NewRenewSandboxLease(RenewSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "c04-open-repin-renew"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: grant.ExecutionNodeID, NodeGeneration: NodeGeneration(grant.NodeCapacityGeneration),
		AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
		Authority: NewLeaseRenewalAuthority(
			lease.WorkerAuthorityID, lease.WorkerGeneration, lease.NodeAuthorityID, lease.AuthorizationGeneration,
		),
		ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		RequestedExpiresAt: now.Add(150 * time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := harness.Maintenance.Maintain(context.Background(), renew)
	if err != nil || renewed.Lease.Generation != lease.Generation+1 || renewed.Lease.Fence != lease.Fence+1 {
		t.Fatalf("renew ambiguous lease: %+v err=%v", renewed, err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || accepted.Snapshot.Readiness.CapsuleReady ||
		accepted.Snapshot.Readiness.RuntimeView.State != PrerequisiteAccepted ||
		accepted.Snapshot.Readiness.ImmutableInputs.State != PrerequisitePending {
		t.Fatalf("replay open after renewal: %+v err=%v", accepted, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("C04 open was repinned: %#v", requests)
	}
	if requests[1].SandboxLeaseAuthority.LeaseGeneration != taskworkspace.LeaseGeneration(lease.Generation) ||
		requests[1].SandboxLeaseAuthority.LeaseFence != taskworkspace.LeaseFence(lease.Fence) ||
		accepted.Snapshot.Lease.Generation != renewed.Lease.Generation ||
		accepted.Snapshot.RuntimeViewBinding.LeaseGeneration != lease.Generation {
		t.Fatalf("original C04 authority was not retained: request=%+v snapshot=%+v",
			requests[1], accepted.Snapshot)
	}
}

func TestC04PermanentOpenRejectionIsDurableAndNotRetried(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 58, 30, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-open-rejected-authority", 7)
	start, grant, node := mutatingPrerequisiteStart(t, now, authority, "c04-open-rejected", digest(115))
	openCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			context.Context,
			taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorStaleAuthority}
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	rejected, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || rejected.Snapshot.State != RuntimeTerminal || rejected.Snapshot.Outcome != RuntimeRejected ||
		rejected.Snapshot.Lease.Disposition != LeaseRevoked ||
		rejected.Snapshot.Cleanup.FenceRuntimeView ||
		rejected.Snapshot.Readiness.RuntimeView.State != PrerequisiteRejected ||
		rejected.Snapshot.Readiness.RuntimeView.Failure != PrerequisiteFailureStale ||
		rejected.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("permanent C04 rejection: %+v err=%v", rejected, err)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed != rejected || openCalls != 1 {
		t.Fatalf("permanent C04 rejection replay: %+v err=%v calls=%d", replayed, err, openCalls)
	}
}

func TestC04TerminalTemporaryAmbiguityReplaysExactlyAndPermanentRejectionStops(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		temporary bool
	}{
		{name: "temporary ambiguity", temporary: true},
		{name: "permanent rejection"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 20, 59, 30+index, 0, time.UTC)
			suffix := fmt.Sprintf("c04-terminal-%d", index)
			authority := mustTaskOrchestrationAuthority(t, suffix+"-authority", 7)
			start, grant, node := mutatingPrerequisiteStart(t, now, authority, suffix, digest(byte(116+index)))
			var requests []taskworkspace.FenceRuntimeViewRequest
			lifecycle := RuntimeViewPrerequisiteAdapter{
				OpenRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.OpenRuntimeViewRequest,
				) (taskworkspace.OpenRuntimeViewResult, error) {
					return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID(suffix+"-view")), nil
				},
				FenceRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.FenceRuntimeViewRequest,
				) (taskworkspace.FenceRuntimeViewResult, error) {
					requests = append(requests, request)
					if test.temporary && len(requests) == 1 {
						return taskworkspace.FenceRuntimeViewResult{}, &taskworkspace.Error{
							Code: taskworkspace.ErrorRetryableUnavailable,
						}
					}
					if !test.temporary {
						return taskworkspace.FenceRuntimeViewResult{}, &taskworkspace.Error{
							Code: taskworkspace.ErrorStaleAuthority,
						}
					}
					return acceptedFenceRuntimeViewResult(request), nil
				},
			}
			harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
			started, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatal(err)
			}
			cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
				SchemaVersion: SchemaV1, OperationID: mustOperationID(t, suffix+"-cancel"),
				PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
				PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
				ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
				ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
				ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: authority,
				Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			first, err := harness.Runtime.Execute(context.Background(), cancel)
			if err != nil || first.Snapshot.Outcome != RuntimeCancelled {
				t.Fatalf("first terminal delivery: %+v err=%v", first, err)
			}
			second, err := harness.Runtime.Execute(context.Background(), cancel)
			if err != nil || second != first {
				t.Fatalf("terminal replay: %+v err=%v want=%+v", second, err, first)
			}
			wantCalls := 1
			if test.temporary {
				wantCalls = 2
				if requests[0] != requests[1] {
					t.Fatalf("temporary terminal request was rebound: %+v", requests)
				}
			}
			if len(requests) != wantCalls {
				t.Fatalf("terminal calls=%d, want %d", len(requests), wantCalls)
			}
			if _, err := harness.Runtime.Execute(context.Background(), cancel); err != nil || len(requests) != wantCalls {
				t.Fatalf("terminal settled replay redelivered: err=%v calls=%d", err, len(requests))
			}
		})
	}
}

func TestC04FenceResponseLossInspectsTheOriginalTerminalOperation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 59, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-fence-loss-authority", 7)
	input := standardStart(t, now, authority, "c04-fence-loss").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-fence-loss-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-fence-loss-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-fence-loss-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(106),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-fence-loss-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)

	var terminal taskworkspace.FenceRuntimeViewResult
	fenceCalls := 0
	inspectCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, "c04-fence-loss-view"), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			fenceCalls++
			terminal = acceptedFenceRuntimeViewResult(request)
			return taskworkspace.FenceRuntimeViewResult{}, &taskworkspace.Error{
				Code: taskworkspace.ErrorReconciliationRequired,
			}
		},
		InspectOperationFunc: func(
			_ context.Context,
			request taskworkspace.InspectOperationRequest,
		) (taskworkspace.OperationInspection, error) {
			inspectCalls++
			if request.OperationID != terminal.Operation.ID {
				t.Fatalf("inspected %q, want terminal operation %q", request.OperationID, terminal.Operation.ID)
			}
			result := terminal
			return taskworkspace.OperationInspection{
				Operation: terminal.Operation, Disposition: taskworkspace.OperationTerminal,
				IntentState: taskworkspace.OperationIntentActivated, FenceRuntimeView: &result,
			}, nil
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	started, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "c04-fence-loss-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled || fenceCalls != 1 || inspectCalls != 1 {
		t.Fatalf("terminal response-loss reconciliation: %+v err=%v fence=%d inspect=%d",
			cancelled, err, fenceCalls, inspectCalls)
	}
	if _, err := harness.Runtime.Execute(context.Background(), cancel); err != nil || fenceCalls != 1 {
		t.Fatalf("terminal exact replay redelivered: err=%v fence=%d", err, fenceCalls)
	}
}

func TestPostOpenRuntimeDeadlineFencesC03AndDiscardsTheExactC04RuntimeView(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 21, 1, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "c04-deadline-authority", 7)
	input := standardStart(t, now, authority, "c04-deadline").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "c04-deadline-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "c04-deadline-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "c04-deadline-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(107),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(30*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "c04-deadline-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	node.ExpiresAt = now.Add(30 * time.Minute)
	node.AuthorizationExpiresAt = now.Add(30 * time.Minute)

	var opened taskworkspace.OpenRuntimeViewRequest
	var discarded []taskworkspace.DiscardRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			opened = request
			return acceptedRuntimeViewResult(request, "c04-deadline-view"), nil
		},
		DiscardRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			discarded = append(discarded, request)
			if len(discarded) == 1 {
				return taskworkspace.DiscardRuntimeViewResult{}, &taskworkspace.Error{
					Code: taskworkspace.ErrorRetryableUnavailable,
				}
			}
			return taskworkspace.DiscardRuntimeViewResult{
				TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
				BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
				Reason: request.Reason, Generation: request.Generation, Fence: request.Fence,
				Operation: request.Operation,
			}, nil
		},
	}
	harness := postLeaseHarness(t, now, authority, start, grant, node, lifecycle)
	started, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("start mutating Runtime: %+v err=%v", started, err)
	}
	if err := harness.AdvanceClock(start.Deadline.Sub(now)); err != nil {
		t.Fatal(err)
	}
	timedOut, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("deadline replay: %v", err)
	}
	if timedOut.Fact != started.Fact || timedOut.Snapshot.State != RuntimeTerminal ||
		timedOut.Snapshot.Outcome != RuntimeTimedOut ||
		timedOut.Snapshot.RuntimeFence != started.Snapshot.RuntimeFence+1 ||
		timedOut.Snapshot.Lease.Generation != started.Snapshot.Lease.Generation+1 ||
		timedOut.Snapshot.Lease.Fence != started.Snapshot.Lease.Fence+1 ||
		timedOut.Snapshot.Lease.Disposition != LeaseRevoked ||
		timedOut.Snapshot.Cleanup.Status != LeaseCleanupPending || timedOut.Snapshot.Cleanup.FenceRuntimeView ||
		timedOut.Snapshot.Capacity.LogicalRelease != LogicalCapacityReleaseReady ||
		timedOut.Snapshot.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined {
		t.Fatalf("post-open deadline did not fence first: started=%+v timeout=%+v", started.Snapshot, timedOut.Snapshot)
	}
	if len(discarded) != 1 || discarded[0].Reason != taskworkspace.RuntimeViewRuntimeFailed ||
		discarded[0].SandboxLeaseAuthority != opened.SandboxLeaseAuthority ||
		discarded[0].Operation.RequestDigest != discarded[0].CanonicalRequestDigest() {
		t.Fatalf("deadline C04 discard drifted: open=%+v discards=%+v", opened, discarded)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed != timedOut || len(discarded) != 2 || discarded[0] != discarded[1] {
		t.Fatalf("deadline replay did not resume exact C04 discard: replay=%+v err=%v discards=%+v",
			replayed, err, discarded)
	}
	if _, err := harness.Runtime.Execute(context.Background(), start); err != nil || len(discarded) != 2 {
		t.Fatalf("settled deadline discard replay redelivered: err=%v discards=%d", err, len(discarded))
	}
}

func TestRuntimeBindingRejectionsFailClosedWithoutLaterPrerequisites(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 20, 55, 0, 0, time.UTC)
	tests := []struct {
		name    string
		failure PrerequisiteFailure
	}{
		{name: "wrong release or image", failure: PrerequisiteFailureWrongBinding},
		{name: "revoked binding", failure: PrerequisiteFailureRevoked},
		{name: "stale release or catalog epoch", failure: PrerequisiteFailureStale},
		{name: "incompatible executor or catalog closure", failure: PrerequisiteFailureIncompatible},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suffix := fmt.Sprintf("binding-rejection-%d", index)
			authority := mustTaskOrchestrationAuthority(t, suffix, 7)
			start := standardStart(t, now, authority, suffix)
			grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
			grant.ExecutionNodeID = startNodeID(t, "binding-rejection-node-"+suffix)
			grant.NodeCapacityGeneration = 1
			node := executionNodeFixtureForStart(t, start, grant, now)
			leaseCalls := 0
			inputCalls := 0
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
				AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
					context.Context,
					LeaseAcquisitionRequest,
				) (LeaseAcquisitionObservation, error) {
					leaseCalls++
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
				RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
					context.Context,
					RuntimeBindingValidationRequest,
				) (PrerequisiteObservation, error) {
					return PrerequisiteObservation{
						Disposition: PrerequisiteObservationRejected, Failure: test.failure,
					}, nil
				}),
				ImmutableInputValidator: ImmutableInputValidatorFunc(func(
					context.Context,
					ImmutableInputValidationRequest,
				) (PrerequisiteObservation, error) {
					inputCalls++
					return acceptedPrerequisiteObservation(t, "should-not-run", digest(91)), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Snapshot.State != RuntimeTerminal || decision.Snapshot.Outcome != RuntimeRejected ||
				decision.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalImmutableBinding ||
				decision.Snapshot.Lease.AcquireStatus != LeaseNotRequested ||
				decision.Snapshot.Lease.Disposition != LeaseDispositionNone ||
				decision.Snapshot.Cleanup != (RuntimeLeaseCleanupSnapshot{}) ||
				decision.Snapshot.Capacity.NoLease != NoLeaseDispositionRecorded ||
				decision.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteRejected ||
				decision.Snapshot.Readiness.RuntimeBinding.Failure != test.failure ||
				decision.Snapshot.Readiness.CapsuleReady || leaseCalls != 0 || inputCalls != 0 {
				t.Fatalf("binding rejection crossed pre-lease gate: snapshot=%+v lease calls=%d input calls=%d",
					decision.Snapshot, leaseCalls, inputCalls)
			}
		})
	}
}

func TestGenerationCatalogValidationReceivesOnlyTheExactTemplateLockClosureAndEpoch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 21, 3, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "catalog-exact-authority", 7)
	input := standardStart(t, now, authority, "catalog-exact").StartRuntimeRunInput
	input.CatalogBinding = &CatalogExecutionBinding{
		TemplateLockID:     mustTemplateLockID(t, "catalog-exact-template-lock"),
		TemplateLockDigest: digest(111), ClosureRootDigest: digest(112), SafetyEpoch: 19,
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "catalog-exact-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	node.CatalogSafetyEpoch = start.CatalogBinding.SafetyEpoch
	validationCalls := 0
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			_ context.Context,
			request RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			validationCalls++
			catalogBinding := request.Authorization.CatalogBinding
			if catalogBinding == nil || *catalogBinding != *start.CatalogBinding ||
				catalogBinding.TemplateLockID != start.CatalogBinding.TemplateLockID ||
				catalogBinding.TemplateLockDigest != digest(111) ||
				catalogBinding.ClosureRootDigest != digest(112) ||
				catalogBinding.SafetyEpoch != 19 {
				t.Fatalf("exact catalog binding drifted: %+v", catalogBinding)
			}
			return acceptedPrerequisiteObservation(t, "catalog-exact-evidence", digest(113)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "catalog-exact-input-evidence", digest(114)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !decision.Snapshot.Readiness.CapsuleReady || validationCalls != 1 {
		t.Fatalf("exact catalog readiness: %+v err=%v calls=%d", decision, err, validationCalls)
	}
}

func TestPrerequisitePortsExposeNoFloatingSelectorsCommitOrCleanupAuthority(t *testing.T) {
	t.Parallel()

	validationType := reflect.TypeOf(RuntimeBindingValidationRequest{})
	requestFields := map[string]bool{
		"OperationID": true, "CanonicalRequestDigest": true, "Authorization": true,
	}
	authorizationType := reflect.TypeOf(RuntimeBindingAuthorization{})
	authorizationFields := map[string]bool{
		"PersonalWorkspaceID": true,
		"TaskID":              true, "PhaseRunID": true, "RuntimeRunID": true,
		"RuntimeBindingID": true, "RuntimeBindingDigest": true, "ExecutionLockDigest": true,
		"CapabilityContractDigest": true, "AllowedPlatformImagesDigest": true,
		"ExecutorContractDigest": true, "OutputContractDigest": true,
		"EvidenceContractDigest": true, "ReleaseSafetyEpoch": true, "CatalogBinding": true,
	}
	for index := 0; index < validationType.NumField(); index++ {
		field := validationType.Field(index)
		if !requestFields[field.Name] {
			t.Fatalf("exact Runtime Binding request exposes non-intent field %q", field.Name)
		}
	}
	if validationType.NumField() != len(requestFields) {
		t.Fatalf("Runtime Binding request fields=%d, want %d", validationType.NumField(), len(requestFields))
	}
	for index := 0; index < authorizationType.NumField(); index++ {
		field := authorizationType.Field(index)
		if !authorizationFields[field.Name] {
			t.Fatalf("Runtime Binding authorization exposes non-intent field %q", field.Name)
		}
	}
	if authorizationType.NumField() != len(authorizationFields) {
		t.Fatalf("Runtime Binding authorization fields=%d, want %d",
			authorizationType.NumField(), len(authorizationFields))
	}
	port := reflect.TypeOf((*RuntimeViewPrerequisitePort)(nil)).Elem()
	for _, required := range []string{
		"OpenRuntimeView", "FenceRuntimeView", "DiscardRuntimeView", "InspectOperation", "ReconcileOperation",
	} {
		if _, exists := port.MethodByName(required); !exists {
			t.Fatalf("C04 prerequisite port is missing %s", required)
		}
	}
	for _, prohibited := range []string{
		"CommitRuntimeView", "CreateCleanupObligation", "ClaimCleanupDebt", "ResolveCleanupDebt",
	} {
		if _, exists := port.MethodByName(prohibited); exists {
			t.Fatalf("C03 prerequisite port exposes prohibited C04 authority %s", prohibited)
		}
	}
	for _, contract := range []reflect.Type{
		reflect.TypeOf(RuntimeViewRequirement{}),
		reflect.TypeOf(RuntimeBindingValidationRequest{}),
		reflect.TypeOf(ImmutableInputValidationRequest{}),
		reflect.TypeOf(taskworkspace.OpenRuntimeViewRequest{}),
		reflect.TypeOf(taskworkspace.OpenRuntimeViewResult{}),
		reflect.TypeOf(taskworkspace.FenceRuntimeViewRequest{}),
		reflect.TypeOf(taskworkspace.FenceRuntimeViewResult{}),
		reflect.TypeOf(taskworkspace.DiscardRuntimeViewRequest{}),
		reflect.TypeOf(taskworkspace.DiscardRuntimeViewResult{}),
	} {
		for index := 0; index < contract.NumField(); index++ {
			name := strings.ToLower(contract.Field(index).Name)
			for _, prohibited := range []string{"content", "path", "mount", "locator", "session"} {
				if strings.Contains(name, prohibited) {
					t.Fatalf("%s exposes prohibited %s field %q", contract, prohibited, contract.Field(index).Name)
				}
			}
		}
	}
}

func TestImmutableInputIntegrityRejectionsRemainSafeAndNotReady(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 21, 5, 0, 0, time.UTC)
	tests := []PrerequisiteFailure{
		PrerequisiteFailureMissing,
		PrerequisiteFailureCorrupt,
		PrerequisiteFailureDuplicate,
		PrerequisiteFailureCrossScope,
	}
	publicFailures := make(map[PrerequisiteFailure]struct{})
	for _, failure := range tests {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			authority := mustTaskOrchestrationAuthority(t, fmt.Sprintf("input-rejection-%d", failure), 7)
			start := standardStart(t, now, authority, fmt.Sprintf("input-rejection-%d", failure))
			grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
			grant.ExecutionNodeID = startNodeID(t, fmt.Sprintf("input-rejection-node-%d", failure))
			grant.NodeCapacityGeneration = 1
			node := executionNodeFixtureForStart(t, start, grant, now)
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
				AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
					context.Context,
					LeaseAcquisitionRequest,
				) (LeaseAcquisitionObservation, error) {
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
				RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
					context.Context,
					RuntimeBindingValidationRequest,
				) (PrerequisiteObservation, error) {
					return acceptedPrerequisiteObservation(t, "input-rejection-release-evidence", digest(92)), nil
				}),
				ImmutableInputValidator: ImmutableInputValidatorFunc(func(
					context.Context,
					ImmutableInputValidationRequest,
				) (PrerequisiteObservation, error) {
					return PrerequisiteObservation{
						Disposition: PrerequisiteObservationRejected, Failure: failure,
					}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteRejected ||
				decision.Snapshot.Readiness.CapsuleReady {
				t.Fatalf("input rejection = %+v", decision.Snapshot.Readiness)
			}
			publicFailures[decision.Snapshot.Readiness.ImmutableInputs.Failure] = struct{}{}
		})
	}
	if len(publicFailures) != 1 {
		t.Fatalf("public input rejection categories enumerate validator detail: %+v", publicFailures)
	}
	for _, internalFailure := range tests {
		if _, exposed := publicFailures[internalFailure]; exposed {
			t.Fatalf("public input rejection exposes internal category %v", internalFailure)
		}
	}
}

func TestProviderCapableRunKeepsGatewayExtensionExplicitlyUnsatisfied(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 21, 10, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "provider-readiness-authority", 7)
	input := standardStart(t, now, authority, "provider-readiness").StartRuntimeRunInput
	input.ProviderCapability = ProviderCapabilityRequired
	input.ProviderBinding = &ProviderExecutionBinding{
		QuotaReservationID:           mustQuotaReservationID(t, "provider-readiness-reservation"),
		Generation:                   3,
		Mode:                         QuotaReservationObservation,
		GatewayRoutePolicyID:         mustGatewayRoutePolicyID(t, "provider-readiness-route"),
		GatewayRoutePolicyGeneration: 2,
		CapabilityScope:              ProviderScopeTextGeneration,
		RoutePolicyExpiresAt:         now.Add(5 * time.Minute),
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "provider-readiness-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	reservation := QuotaReservationFixture{
		QuotaReservationID: start.ProviderBinding.QuotaReservationID,
		Generation:         start.ProviderBinding.Generation, Mode: start.ProviderBinding.Mode,
		State: QuotaReservationActive, PersonalWorkspaceID: start.PersonalWorkspaceID,
		TaskID: start.TaskID, PhaseRunID: start.PhaseRunID,
		AuthorizationGeneration:      start.Authority.generation,
		Capability:                   ProviderCapabilityRequired,
		GatewayRoutePolicyID:         start.ProviderBinding.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: start.ProviderBinding.GatewayRoutePolicyGeneration,
		CapabilityScope:              start.ProviderBinding.CapabilityScope,
		ValidFrom:                    now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute),
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		QuotaReservations: []QuotaReservationFixture{reservation},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "provider-release-evidence", digest(93)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "provider-input-evidence", digest(94)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Snapshot.Readiness.Lease.State != PrerequisiteAccepted ||
		decision.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteAccepted ||
		decision.Snapshot.Readiness.RuntimeView.State != PrerequisiteNotApplicable ||
		decision.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteAccepted ||
		decision.Snapshot.Readiness.LLMGateway.State != PrerequisitePending ||
		decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("provider LLMGateway extension collapsed: %+v", decision.Snapshot.Readiness)
	}
}

func postLeaseHarness(
	t *testing.T,
	now time.Time,
	authority RuntimeAuthority,
	start StartRuntimeRun,
	grant AdmissionGrantFixture,
	node ExecutionNodeFixture,
	lifecycle RuntimeViewPrerequisitePort,
) *DeterministicHarness {
	t.Helper()
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "release-evidence-"+start.RuntimeRunID.String(), digest(85)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "input-evidence-"+start.RuntimeRunID.String(), digest(86)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func mutatingPrerequisiteStart(
	t *testing.T,
	now time.Time,
	authority RuntimeAuthority,
	suffix string,
	derivation Digest,
) (StartRuntimeRun, AdmissionGrantFixture, ExecutionNodeFixture) {
	t.Helper()
	input := standardStart(t, now, authority, suffix).StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, suffix+"-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, suffix+"-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, suffix+"-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: derivation,
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, suffix+"-node")
	grant.NodeCapacityGeneration = 1
	return start, grant, executionNodeFixtureForStart(t, start, grant, now)
}

func TestPostgresPrerequisiteFactsOutboxAndReadinessSurviveRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_prereq_test")
	now := time.Date(2026, time.July, 29, 21, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postgres-prerequisite-authority", 9)
	input := standardStart(t, now, authority, "postgres-prerequisite").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "postgres-prerequisite-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "postgres-prerequisite-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "postgres-prerequisite-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(87),
	}
	start := mustStart(t, input)
	leaseOperationID, leaseDigest := stableLeaseAcquireBinding(start)
	lease := RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, AcquireOperationID: leaseOperationID, AcquireDigest: leaseDigest,
		LeaseID: SandboxLeaseID{value: "postgres-prerequisite-lease"}, Generation: 1, Fence: 1,
		Disposition: LeaseActive, ExpiresAt: now.Add(90 * time.Second),
		SandboxID: SandboxID{value: "postgres-prerequisite-sandbox"}, SandboxGeneration: 1, SandboxFence: 1,
		WorkerAuthorityID: WorkerAuthorityID{value: "postgres-prerequisite-worker"}, WorkerGeneration: 1,
		NodeAuthorityID:         NodeAuthorityID{value: "postgres-prerequisite-node-authority"},
		AuthorizationGeneration: 1, AuthorizationExpiresAt: now.Add(5 * time.Minute),
	}
	readiness := initialRuntimeReadiness(start)
	readiness.Lease = leasePrerequisiteFact(lease)
	fixture := acceptedPostgresRuntimeFixture(start, authority, now)
	fixture.RuntimeRevision++
	fixture.State = RuntimePreparingPrerequisites
	fixture.Operation.WorkItemID = start.AdmissionGrant.WorkItemID
	fixture.Operation.ExecutionNodeID = ExecutionNodeID{value: "postgres-prerequisite-node"}
	fixture.Operation.NodeCapacityGeneration = 1
	fixture.Operation.ResourceClassID = start.ResourceClassID
	fixture.Operation.ExecutionPolicyID = start.ExecutionPolicyID
	fixture.Operation.SchedulerEpoch = 1
	fixture.Operation.PolicyVersion = 1
	fixture.Lease = lease
	fixture.Node = RuntimeNodeSnapshot{
		ExecutionNodeID: fixture.Operation.ExecutionNodeID, Generation: 1, Readiness: NodeReady,
		AttestationID: NodeAttestationID{value: "postgres-prerequisite-attestation"}, AttestationGeneration: 1,
		AttestedAt: now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute), Occupancy: NodeOccupied,
		Containment: ContainmentPending, Reset: ResetRequired,
	}
	fixture.Capacity.Physical = PhysicalCapacityOccupied
	fixture.Readiness = readiness

	var mu sync.Mutex
	bindingCalls := 0
	inputCalls := 0
	openCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			openCalls++
			return taskworkspace.OpenRuntimeViewResult{
				PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
				RuntimeViewID: "postgres-prerequisite-view", TaskWorkspaceID: request.TaskWorkspaceID,
				MaterializationID: request.MaterializationID, BaseRevisionID: request.BaseRevisionID,
				PhaseRunID: request.PhaseRunID, RuntimeRunID: request.RuntimeRunID,
				RuntimeOperationID: request.RuntimeOperationID, SandboxLeaseAuthority: request.SandboxLeaseAuthority,
				EffectClass: request.EffectClass, ExpiresAt: request.ExpiresAt,
				Generation: request.Generation, Fence: request.Fence, Operation: request.Operation,
			}, nil
		},
	}
	config := PostgresConfig{
		Schema: schema, Now: func() time.Time { return now },
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			mu.Lock()
			defer mu.Unlock()
			bindingCalls++
			return acceptedPrerequisiteObservation(t, "postgres-release-evidence", digest(88)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			mu.Lock()
			defer mu.Unlock()
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postgres-input-evidence", digest(89)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	}
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-postgres-prerequisite")
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)
	persistPostgresAcceptedRuntimeBindingFact(t, store, start)

	accepted, err := store.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute PostgreSQL prerequisites: %v", err)
	}
	if !accepted.Snapshot.Readiness.CapsuleReady ||
		accepted.Snapshot.RuntimeViewBinding.RuntimeViewID.String() != "postgres-prerequisite-view" {
		t.Fatalf("PostgreSQL readiness = %+v view=%+v", accepted.Snapshot.Readiness, accepted.Snapshot.RuntimeViewBinding)
	}
	var operations, audits, outbox, acknowledged, deliveryCount int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_audit),
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_outbox_delivery WHERE disposition=$1),
		(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_prerequisite_outbox_delivery)`,
		OutboxAcknowledged).Scan(&operations, &audits, &outbox, &acknowledged, &deliveryCount); err != nil {
		t.Fatal(err)
	}
	if operations != 3 || audits != 4 || outbox != 1 || acknowledged != 1 || deliveryCount != 1 {
		t.Fatalf("durable prerequisite families: operations=%d audits=%d outbox=%d acknowledged=%d deliveries=%d",
			operations, audits, outbox, acknowledged, deliveryCount)
	}

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed != accepted {
		t.Fatalf("PostgreSQL replay after restart = %+v err=%v", replayed, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if bindingCalls != 3 || inputCalls != 1 || openCalls != 1 {
		t.Fatalf("restart did not isolate binding revalidation: binding=%d input=%d open=%d", bindingCalls, inputCalls, openCalls)
	}
}

func TestPostgresPostOpenCancelCommitsC04FenceOutboxBeforeDeliveryAndSurvivesRestart(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_view_term_test")
	now := time.Date(2026, time.July, 29, 21, 20, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postgres-view-terminal-authority", 9)
	input := standardStart(t, now, authority, "postgres-view-terminal").StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, "postgres-view-terminal-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, "postgres-view-terminal-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, "postgres-view-terminal-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(102),
	}
	start := mustStart(t, input)
	leaseOperationID, leaseDigest := stableLeaseAcquireBinding(start)
	lease := RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, AcquireOperationID: leaseOperationID, AcquireDigest: leaseDigest,
		LeaseID: SandboxLeaseID{value: "postgres-view-terminal-lease"}, Generation: 1, Fence: 1,
		Disposition: LeaseActive, ExpiresAt: now.Add(90 * time.Second),
		SandboxID: SandboxID{value: "postgres-view-terminal-sandbox"}, SandboxGeneration: 1, SandboxFence: 1,
		WorkerAuthorityID: WorkerAuthorityID{value: "postgres-view-terminal-worker"}, WorkerGeneration: 1,
		NodeAuthorityID:         NodeAuthorityID{value: "postgres-view-terminal-node-authority"},
		AuthorizationGeneration: 1, AuthorizationExpiresAt: now.Add(5 * time.Minute),
	}
	readiness := initialRuntimeReadiness(start)
	readiness.Lease = leasePrerequisiteFact(lease)
	fixture := acceptedPostgresRuntimeFixture(start, authority, now)
	fixture.RuntimeRevision++
	fixture.State = RuntimePreparingPrerequisites
	fixture.Operation.WorkItemID = start.AdmissionGrant.WorkItemID
	fixture.Operation.ExecutionNodeID = ExecutionNodeID{value: "postgres-view-terminal-node"}
	fixture.Operation.NodeCapacityGeneration = 1
	fixture.Operation.ResourceClassID = start.ResourceClassID
	fixture.Operation.ExecutionPolicyID = start.ExecutionPolicyID
	fixture.Operation.SchedulerEpoch = 1
	fixture.Operation.PolicyVersion = 1
	fixture.Lease = lease
	fixture.Node = RuntimeNodeSnapshot{
		ExecutionNodeID: fixture.Operation.ExecutionNodeID, Generation: 1, Readiness: NodeReady,
		AttestationID: NodeAttestationID{value: "postgres-view-terminal-attestation"}, AttestationGeneration: 1,
		AttestedAt: now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute), Occupancy: NodeOccupied,
		Containment: ContainmentPending, Reset: ResetRequired,
	}
	fixture.Capacity.Physical = PhysicalCapacityOccupied
	fixture.Readiness = readiness

	var mu sync.Mutex
	fenceCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, "postgres-view-terminal-view"), nil
		},
		FenceRuntimeViewFunc: func(
			ctx context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			defer mu.Unlock()
			fenceCalls++
			var operations, audits, outbox, pending int
			if err := db.QueryRowContext(ctx, `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_audit),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery
				 WHERE disposition=$1)`, OutboxPending).Scan(&operations, &audits, &outbox, &pending); err != nil {
				t.Fatalf("inspect committed terminal obligation: %v", err)
			}
			if operations != 1 || audits != 1 || outbox != 1 || pending != 1 {
				t.Fatalf("terminal obligation before delivery: operations=%d audits=%d outbox=%d pending=%d",
					operations, audits, outbox, pending)
			}
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	config := PostgresConfig{
		Schema: schema, Now: func() time.Time { return now },
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "postgres-terminal-release-evidence", digest(103)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "postgres-terminal-input-evidence", digest(104)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	}
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-postgres-view-terminal")
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)
	installPostgresActiveLeaseForPrerequisiteTest(t, db, schema, fixture, now)
	persistPostgresAcceptedRuntimeBindingFact(t, store, start)

	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute prerequisites: %+v err=%v", started, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-view-terminal-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.Execute(context.Background(), cancel)
	if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("cancel: %+v err=%v", cancelled, err)
	}
	var acknowledged, deliveries, terminalAudits int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery
		 WHERE disposition=$1),
		(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_audit)`, OutboxAcknowledged).
		Scan(&acknowledged, &deliveries, &terminalAudits); err != nil {
		t.Fatal(err)
	}
	if acknowledged != 1 || deliveries != 1 || terminalAudits != 2 {
		t.Fatalf("terminal delivery: acknowledged=%d deliveries=%d audits=%d",
			acknowledged, deliveries, terminalAudits)
	}

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), cancel)
	if err != nil || replayed != cancelled {
		t.Fatalf("cancel replay after restart: %+v err=%v want=%+v", replayed, err, cancelled)
	}
	mu.Lock()
	defer mu.Unlock()
	if fenceCalls != 1 {
		t.Fatalf("restart delivered %d C04 fences, want 1", fenceCalls)
	}
}

func TestPostgresRuntimeViewTerminalIntentFaultsRollbackOrResumeAfterRestart(t *testing.T) {
	tests := []struct {
		point         PersistenceFaultPoint
		wantError     ErrorCode
		wantCommitted bool
	}{
		{point: PersistenceFaultBeforeMandatoryAudit, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultAfterMandatoryAudit, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultBeforeOutbox, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultBeforeCommit, wantError: ErrorDependencyUnavailable},
		{point: PersistenceFaultAfterCommit, wantError: ErrorReconciliationRequired, wantCommitted: true},
	}
	for _, test := range tests {
		t.Run(test.point.String(), func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 21, 35, 0, 0, time.UTC)
			caseID := fmt.Sprintf("term_fault_%d", test.point)
			var mu sync.Mutex
			var fenceRequests []taskworkspace.FenceRuntimeViewRequest
			lifecycle := RuntimeViewPrerequisiteAdapter{
				OpenRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.OpenRuntimeViewRequest,
				) (taskworkspace.OpenRuntimeViewResult, error) {
					return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("terminal-fault-view")), nil
				},
				FenceRuntimeViewFunc: func(
					_ context.Context,
					request taskworkspace.FenceRuntimeViewRequest,
				) (taskworkspace.FenceRuntimeViewResult, error) {
					mu.Lock()
					defer mu.Unlock()
					fenceRequests = append(fenceRequests, request)
					return acceptedFenceRuntimeViewResult(request), nil
				},
			}
			db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
				t, caseID, now, func() time.Time { return now }, lifecycle, nil,
			)
			started, err := store.Execute(context.Background(), start)
			if err != nil || !started.Snapshot.Readiness.CapsuleReady {
				t.Fatalf("execute prerequisites: %+v err=%v", started, err)
			}
			cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
				SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "terminal-fault-cancel-"+caseID),
				PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
				PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
				ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
				ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
				ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: start.Authority,
				Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}

			// Commit the C03 cancellation first so the shared fault injector can
			// target only the retained C04 terminal obligation on exact replay.
			c03OnlyConfig := config
			c03OnlyConfig.RuntimeViewPrerequisite = nil
			c03Only, err := NewPostgresAuthority(db, c03OnlyConfig)
			if err != nil {
				t.Fatal(err)
			}
			cancelled, err := c03Only.Execute(context.Background(), cancel)
			if err != nil || cancelled.Snapshot.Outcome != RuntimeCancelled {
				t.Fatalf("commit C03 cancel: %+v err=%v", cancelled, err)
			}

			faults := &PersistenceFaultController{}
			faultedConfig := config
			faultedConfig.Faults = faults
			faulted, err := NewPostgresAuthority(db, faultedConfig)
			if err != nil {
				t.Fatal(err)
			}
			if err := faults.FailNextAt(test.point); err != nil {
				t.Fatal(err)
			}
			_, err = faulted.Execute(context.Background(), cancel)
			assertErrorCode(t, err, test.wantError)

			var operations, audits, outbox, deliveries, deliveryCount int
			if err := db.QueryRowContext(context.Background(), `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_audit),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery),
				(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery)`).
				Scan(&operations, &audits, &outbox, &deliveries, &deliveryCount); err != nil {
				t.Fatal(err)
			}
			wantRows := 0
			if test.wantCommitted {
				wantRows = 1
			}
			if operations != wantRows || audits != wantRows || outbox != wantRows ||
				deliveries != wantRows || deliveryCount != 0 {
				t.Fatalf("terminal intent after fault: operations=%d audits=%d outbox=%d deliveries=%d attempts=%d wantRows=%d",
					operations, audits, outbox, deliveries, deliveryCount, wantRows)
			}
			mu.Lock()
			if len(fenceRequests) != 0 {
				t.Fatalf("fault delivered C04 fence before restart: %+v", fenceRequests)
			}
			mu.Unlock()

			restarted, err := NewPostgresAuthority(db, config)
			if err != nil {
				t.Fatal(err)
			}
			replayed, err := restarted.Execute(context.Background(), cancel)
			if err != nil || replayed != cancelled {
				t.Fatalf("resume terminal obligation: %+v err=%v want=%+v", replayed, err, cancelled)
			}
			mu.Lock()
			if len(fenceRequests) != 1 || fenceRequests[0].Reason != taskworkspace.RuntimeViewCancelled {
				t.Fatalf("resumed C04 fence requests: %+v", fenceRequests)
			}
			mu.Unlock()
			if err := db.QueryRowContext(context.Background(), `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_audit),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$1),
				(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery)`,
				OutboxAcknowledged).Scan(&operations, &audits, &outbox, &deliveries, &deliveryCount); err != nil {
				t.Fatal(err)
			}
			if operations != 1 || audits != 2 || outbox != 1 || deliveries != 1 || deliveryCount != 1 {
				t.Fatalf("resumed terminal durability: operations=%d audits=%d outbox=%d ack=%d attempts=%d",
					operations, audits, outbox, deliveries, deliveryCount)
			}
		})
	}
}

func TestPostgresRuntimeViewTerminalFinalStateRepairsPendingAckAfterRestart(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 37, 0, 0, time.UTC)
	var mu sync.Mutex
	var fenced []taskworkspace.FenceRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("terminal-ack-repair-view")), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			mu.Lock()
			fenced = append(fenced, request)
			mu.Unlock()
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "term_ack", now, func() time.Time { return now }, lifecycle, nil,
	)
	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute prerequisites: %+v err=%v", started, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "terminal-ack-repair-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutC04 := config
	withoutC04.RuntimeViewPrerequisite = nil
	c03Only, err := NewPostgresAuthority(db, withoutC04)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := c03Only.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatalf("commit C03 cancel: %v", err)
	}

	faults := &PersistenceFaultController{}
	faultedConfig := config
	faultedConfig.Faults = faults
	faulted, err := NewPostgresAuthority(db, faultedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := faults.FailNextAt(PersistenceFaultBeforeResponse); err != nil {
		t.Fatal(err)
	}
	_, err = faulted.Execute(context.Background(), cancel)
	assertErrorCode(t, err, ErrorReconciliationRequired)
	var terminalState int16
	var pending int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT terminal_state FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$1)`,
		OutboxPending).Scan(&terminalState, &pending); err != nil {
		t.Fatal(err)
	}
	if terminalState != int16(runtimeViewTerminalAccepted) || pending != 1 {
		t.Fatalf("crash window state=%d pending=%d, want accepted/pending", terminalState, pending)
	}
	mu.Lock()
	if len(fenced) != 1 {
		mu.Unlock()
		t.Fatalf("C04 fences before restart=%d, want 1", len(fenced))
	}
	mu.Unlock()

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), cancel)
	if err != nil || replayed != cancelled {
		t.Fatalf("repair terminal ack: %+v err=%v want=%+v", replayed, err, cancelled)
	}
	var acknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$1`, OutboxAcknowledged).
		Scan(&acknowledged); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if acknowledged != 1 || len(fenced) != 1 {
		t.Fatalf("restart ack repair: acknowledged=%d fences=%d", acknowledged, len(fenced))
	}
}

func TestPostgresRuntimeViewOpenFinalFactRepairsPendingAckAfterRestart(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 38, 0, 0, time.UTC)
	var mu sync.Mutex
	openCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			mu.Lock()
			openCalls++
			mu.Unlock()
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("open-ack-repair-view")), nil
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "open_ack", now, func() time.Time { return now }, lifecycle, nil,
	)
	withoutC04 := config
	withoutC04.RuntimeViewPrerequisite = nil
	preparer, err := NewPostgresAuthority(db, withoutC04)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := preparer.Execute(context.Background(), start)
	if err != nil || pending.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteAccepted ||
		pending.Snapshot.Readiness.RuntimeView.State != PrerequisitePending {
		t.Fatalf("prepare C04 pending runtime: %+v err=%v", pending, err)
	}

	faults := &PersistenceFaultController{}
	faultedConfig := config
	faultedConfig.Faults = faults
	faulted, err := NewPostgresAuthority(db, faultedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := faults.FailNextAt(PersistenceFaultBeforeResponse); err != nil {
		t.Fatal(err)
	}
	_, err = faulted.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorReconciliationRequired)
	var factState []byte
	var pendingDeliveries int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT fact_state FROM `+schema+`.runtime_execution_prerequisite_operations WHERE prerequisite_kind=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_outbox_delivery WHERE disposition=$2)`,
		postgresPrerequisiteRuntimeView, OutboxPending).Scan(&factState, &pendingDeliveries); err != nil {
		t.Fatal(err)
	}
	var retained postgresPrerequisiteFactState
	if json.Unmarshal(factState, &retained) != nil || retained.State != PrerequisiteAccepted || pendingDeliveries != 1 {
		t.Fatalf("C04 Open crash window: fact=%s pending=%d", factState, pendingDeliveries)
	}
	mu.Lock()
	if openCalls != 1 {
		mu.Unlock()
		t.Fatalf("C04 Open calls before restart=%d, want 1", openCalls)
	}
	mu.Unlock()

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	converged, err := restarted.Execute(context.Background(), start)
	if err != nil || !converged.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("repair C04 Open ack: %+v err=%v", converged, err)
	}
	var acknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_prerequisite_outbox_delivery WHERE disposition=$1`, OutboxAcknowledged).
		Scan(&acknowledged); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if acknowledged != 1 || openCalls != 1 {
		t.Fatalf("restart Open ack repair: acknowledged=%d opens=%d", acknowledged, openCalls)
	}
}

func TestPostgresPrerequisiteAndRuntimeViewTerminalBindingsRejectRebinding(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 38, 0, 0, time.UTC)
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("binding-lock-view")), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "binding_lock", now, func() time.Time { return now }, lifecycle, nil,
	)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration with prerequisite bindings: %v", err)
	}
	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute prerequisites: %+v err=%v", started, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "binding-lock-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Execute(context.Background(), cancel); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	for _, mutation := range []struct {
		statement string
		args      []any
	}{
		{statement: `UPDATE ` + schema + `.runtime_execution_prerequisite_operations SET canonical_request=$1`, args: []any{[]byte("rebound")}},
		{statement: `DELETE FROM ` + schema + `.runtime_execution_prerequisite_operations`},
		{statement: `UPDATE ` + schema + `.runtime_execution_prerequisite_audit SET canonical_digest=$1`, args: []any{make([]byte, 32)}},
		{statement: `UPDATE ` + schema + `.runtime_execution_prerequisite_outbox SET payload=$1`, args: []any{[]byte("rebound")}},
		{statement: `UPDATE ` + schema + `.runtime_execution_runtime_view_terminal_operations SET canonical_request=$1`, args: []any{[]byte("rebound")}},
		{statement: `DELETE FROM ` + schema + `.runtime_execution_runtime_view_terminal_operations`},
		{statement: `UPDATE ` + schema + `.runtime_execution_runtime_view_terminal_audit SET canonical_digest=$1`, args: []any{make([]byte, 32)}},
		{statement: `UPDATE ` + schema + `.runtime_execution_runtime_view_terminal_outbox SET payload=$1`, args: []any{[]byte("rebound")}},
	} {
		assertPostgresMutationRejected(t, db, mutation.statement, mutation.args...)
	}
}

func TestPostgresPrerequisiteAuditConflictFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 39, 0, 0, time.UTC)
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("audit-conflict-view")), nil
		},
	}
	db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "audit_conflict", now, func() time.Time { return now }, lifecycle, nil,
	)
	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute prerequisites: %+v err=%v", started, err)
	}

	inputFact := started.Snapshot.Readiness.ImmutableInputs
	inputFact.State = PrerequisiteReconciliationRequired
	inputFact.EvidenceID = EvidenceID{}
	inputFact.EvidenceDigest = Digest{}
	inputFact.Failure = PrerequisiteFailureDependencyUnavailable
	factState, err := json.Marshal(postgresPrerequisiteFactFromSnapshot(inputFact))
	if err != nil {
		t.Fatal(err)
	}
	var aggregateBytes []byte
	if err := db.QueryRowContext(context.Background(), `SELECT aggregate_state FROM `+schema+`.runtime_execution_runtimes WHERE runtime_run_id=$1`,
		start.RuntimeRunID.String()).Scan(&aggregateBytes); err != nil {
		t.Fatal(err)
	}
	var aggregate postgresRuntimeState
	if err := json.Unmarshal(aggregateBytes, &aggregate); err != nil {
		t.Fatal(err)
	}
	aggregate.Readiness.ImmutableInputs = postgresPrerequisiteFactFromSnapshot(inputFact)
	aggregate.Readiness.CapsuleReady = false
	aggregateBytes, err = json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS reject_immutable_mutation ON `+schema+
		`.runtime_execution_prerequisite_audit`); err != nil {
		t.Fatal(err)
	}
	corruptDigest := digest(211)
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_prerequisite_audit
		SET canonical_digest=$1 WHERE operation_id=$2 AND event_kind=$3`,
		corruptDigest[:], inputFact.OperationID.String(), postgresPrerequisiteAuditAccepted); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_prerequisite_operations
		SET fact_state=$1 WHERE operation_id=$2`, factState, inputFact.OperationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_runtimes
		SET aggregate_state=$1 WHERE runtime_run_id=$2`, aggregateBytes, start.RuntimeRunID.String()); err != nil {
		t.Fatal(err)
	}

	_, err = store.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorIntegrityConflict)
}

func TestPostgresCompletedRuntimeViewTerminalBindingTamperFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 41, 0, 0, time.UTC)
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("terminal-tamper-view")), nil
		},
		FenceRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.FenceRuntimeViewRequest,
		) (taskworkspace.FenceRuntimeViewResult, error) {
			return acceptedFenceRuntimeViewResult(request), nil
		},
	}
	db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "term_tamper", now, func() time.Time { return now }, lifecycle, nil,
	)
	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute prerequisites: %+v err=%v", started, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "terminal-tamper-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: start.Authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Execute(context.Background(), cancel); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER reject_runtime_view_terminal_operation_rebinding ON `+schema+
		`.runtime_execution_runtime_view_terminal_operations`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE `+schema+`.runtime_execution_runtime_view_terminal_operations
		SET canonical_request=$1`, []byte("rebound-terminal-request")); err != nil {
		t.Fatal(err)
	}

	_, err = store.Execute(context.Background(), cancel)
	assertErrorCode(t, err, ErrorIntegrityConflict)
}

func TestPostgresPrerequisiteAndRuntimeViewErrorsRetainNoSensitiveDetails(t *testing.T) {
	const canary = "secret-content /private/runtime/path mount://credential object-locator session-token cross-workspace-exists"
	now := time.Date(2026, time.July, 29, 21, 43, 0, 0, time.UTC)
	t.Run("exact Runtime Binding validator", func(t *testing.T) {
		db, schema, config, _, start, _, _ := newPostgresWaitingRuntimeBindingRejection(
			t, "nonleak_bind", now, time.Time{}, nil, PrerequisiteFailureIncompatible, false,
		)
		config.RuntimeBindingValidator = RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return PrerequisiteObservation{}, errors.New(canary)
		})
		store, err := NewPostgresAuthority(db, config)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := store.Execute(context.Background(), start)
		if err != nil || decision.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired {
			t.Fatalf("raw binding failure: %+v err=%v", decision, err)
		}
		assertNoSensitivePrerequisiteDetails(t, db, schema, decision, err, canary)
	})

	t.Run("C04 open", func(t *testing.T) {
		lifecycle := RuntimeViewPrerequisiteAdapter{
			OpenRuntimeViewFunc: func(
				context.Context,
				taskworkspace.OpenRuntimeViewRequest,
			) (taskworkspace.OpenRuntimeViewResult, error) {
				return taskworkspace.OpenRuntimeViewResult{}, errors.New(canary)
			},
			InspectOperationFunc: func(
				context.Context,
				taskworkspace.InspectOperationRequest,
			) (taskworkspace.OperationInspection, error) {
				return taskworkspace.OperationInspection{}, errors.New(canary)
			},
			ReconcileOperationFunc: func(
				context.Context,
				taskworkspace.ReconcileOperationRequest,
			) (taskworkspace.OperationInspection, error) {
				return taskworkspace.OperationInspection{}, errors.New(canary)
			},
		}
		db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
			t, "nonleak_open", now, func() time.Time { return now }, lifecycle, nil,
		)
		decision, err := store.Execute(context.Background(), start)
		if err != nil || decision.Snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired {
			t.Fatalf("raw C04 open failure: %+v err=%v", decision, err)
		}
		assertNoSensitivePrerequisiteDetails(t, db, schema, decision, err, canary)
	})

	t.Run("immutable input validator", func(t *testing.T) {
		lifecycle := RuntimeViewPrerequisiteAdapter{
			OpenRuntimeViewFunc: func(
				_ context.Context,
				request taskworkspace.OpenRuntimeViewRequest,
			) (taskworkspace.OpenRuntimeViewResult, error) {
				return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("nonleak-input-view")), nil
			},
		}
		validator := ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return PrerequisiteObservation{}, errors.New(canary)
		})
		db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
			t, "nonleak_input", now, func() time.Time { return now }, lifecycle, validator,
		)
		decision, err := store.Execute(context.Background(), start)
		if err != nil || decision.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteReconciliationRequired {
			t.Fatalf("raw input failure: %+v err=%v", decision, err)
		}
		assertNoSensitivePrerequisiteDetails(t, db, schema, decision, err, canary)
	})

	t.Run("C04 terminal", func(t *testing.T) {
		lifecycle := RuntimeViewPrerequisiteAdapter{
			OpenRuntimeViewFunc: func(
				_ context.Context,
				request taskworkspace.OpenRuntimeViewRequest,
			) (taskworkspace.OpenRuntimeViewResult, error) {
				return acceptedRuntimeViewResult(request, taskworkspace.RuntimeViewID("nonleak-terminal-view")), nil
			},
			FenceRuntimeViewFunc: func(
				context.Context,
				taskworkspace.FenceRuntimeViewRequest,
			) (taskworkspace.FenceRuntimeViewResult, error) {
				return taskworkspace.FenceRuntimeViewResult{}, errors.New(canary)
			},
			InspectOperationFunc: func(
				context.Context,
				taskworkspace.InspectOperationRequest,
			) (taskworkspace.OperationInspection, error) {
				return taskworkspace.OperationInspection{}, errors.New(canary)
			},
			ReconcileOperationFunc: func(
				context.Context,
				taskworkspace.ReconcileOperationRequest,
			) (taskworkspace.OperationInspection, error) {
				return taskworkspace.OperationInspection{}, errors.New(canary)
			},
		}
		db, schema, store, _, start := newPostgresReadyMutatingPrerequisiteRuntime(
			t, "nonleak_term", now, func() time.Time { return now }, lifecycle, nil,
		)
		started, err := store.Execute(context.Background(), start)
		if err != nil || !started.Snapshot.Readiness.CapsuleReady {
			t.Fatalf("execute prerequisites: %+v err=%v", started, err)
		}
		cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
			SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "nonleak-terminal-cancel"),
			PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
			PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
			ExpectedRuntimeRevision: started.Snapshot.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
			ExpectedOperationGeneration: started.Snapshot.Operation.Generation,
			ExpectedRuntimeFence:        started.Snapshot.RuntimeFence, Authority: start.Authority,
			Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		decision, err := store.Execute(context.Background(), cancel)
		if err != nil || decision.Snapshot.Outcome != RuntimeCancelled {
			t.Fatalf("raw terminal failure: %+v err=%v", decision, err)
		}
		assertNoSensitivePrerequisiteDetails(t, db, schema, decision, err, canary)
	})
}

func TestPostgresPostOpenRuntimeDeadlinePersistsC03AndExactC04Discard(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 40, 0, 0, time.UTC)
	current := now
	var opened taskworkspace.OpenRuntimeViewRequest
	var discarded []taskworkspace.DiscardRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			opened = request
			return acceptedRuntimeViewResult(request, "postgres-deadline-view"), nil
		},
		DiscardRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			discarded = append(discarded, request)
			if len(discarded) == 1 {
				return taskworkspace.DiscardRuntimeViewResult{}, &taskworkspace.Error{
					Code: taskworkspace.ErrorRetryableUnavailable,
				}
			}
			return taskworkspace.DiscardRuntimeViewResult{
				TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
				BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
				Reason: request.Reason, Generation: request.Generation, Fence: request.Fence,
				Operation: request.Operation,
			}, nil
		},
	}
	db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "runtime_deadline", now, func() time.Time { return current }, lifecycle, nil,
	)
	started, err := store.Execute(context.Background(), start)
	if err != nil || !started.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("execute PostgreSQL prerequisites: %+v err=%v", started, err)
	}
	current = start.Deadline
	timedOut, err := store.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute PostgreSQL deadline: %v", err)
	}
	if timedOut.Fact != started.Fact || timedOut.Snapshot.State != RuntimeTerminal ||
		timedOut.Snapshot.Outcome != RuntimeTimedOut ||
		timedOut.Snapshot.RuntimeFence != started.Snapshot.RuntimeFence+1 ||
		timedOut.Snapshot.Lease.Generation != started.Snapshot.Lease.Generation+1 ||
		timedOut.Snapshot.Lease.Fence != started.Snapshot.Lease.Fence+1 ||
		timedOut.Snapshot.Lease.Disposition != LeaseRevoked ||
		timedOut.Snapshot.Cleanup.Status != LeaseCleanupPending || timedOut.Snapshot.Cleanup.FenceRuntimeView {
		t.Fatalf("PostgreSQL deadline did not fence C03: started=%+v timeout=%+v",
			started.Snapshot, timedOut.Snapshot)
	}
	if len(discarded) != 1 || discarded[0].Reason != taskworkspace.RuntimeViewRuntimeFailed ||
		discarded[0].SandboxLeaseAuthority != opened.SandboxLeaseAuthority {
		t.Fatalf("PostgreSQL deadline C04 discard drifted: open=%+v discards=%+v", opened, discarded)
	}
	var terminalOutbox, terminalPending, deadlineDecision, cleanupObligation int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_requests WHERE command_kind=$2),
		(SELECT count(*) FROM `+schema+`.runtime_execution_lease_cleanup_obligations)`,
		OutboxPending, postgresPostLeaseDeadlineCommandKind).
		Scan(&terminalOutbox, &terminalPending, &deadlineDecision, &cleanupObligation); err != nil {
		t.Fatal(err)
	}
	if terminalOutbox != 1 || terminalPending != 1 || deadlineDecision != 1 || cleanupObligation != 1 {
		t.Fatalf("PostgreSQL deadline durability: terminalOutbox=%d pending=%d decision=%d cleanup=%d",
			terminalOutbox, terminalPending, deadlineDecision, cleanupObligation)
	}
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed != timedOut || len(discarded) != 2 || discarded[0] != discarded[1] {
		t.Fatalf("deadline replay after restart: %+v err=%v discards=%+v", replayed, err, discarded)
	}
	if _, err := restarted.Execute(context.Background(), start); err != nil || len(discarded) != 2 {
		t.Fatalf("settled deadline discard replay redelivered: err=%v discards=%d", err, len(discarded))
	}
	var terminalAcknowledged int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+schema+
		`.runtime_execution_runtime_view_terminal_outbox_delivery WHERE disposition=$1`,
		OutboxAcknowledged).Scan(&terminalAcknowledged); err != nil || terminalAcknowledged != 1 {
		t.Fatalf("settled deadline discard acknowledgement=%d err=%v", terminalAcknowledged, err)
	}
}

func TestPostgresRejectedImmutableInputsCommitC04DiscardOutboxBeforeDelivery(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 45, 0, 0, time.UTC)
	var opened taskworkspace.OpenRuntimeViewRequest
	var discarded []taskworkspace.DiscardRuntimeViewRequest
	var db *sql.DB
	var schema string
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			opened = request
			return acceptedRuntimeViewResult(request, "postgres-discard-view"), nil
		},
		DiscardRuntimeViewFunc: func(
			ctx context.Context,
			request taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			discarded = append(discarded, request)
			var operations, audits, outbox, pending int
			if err := db.QueryRowContext(ctx, `SELECT
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_audit),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery
				 WHERE disposition=$1)`, OutboxPending).Scan(&operations, &audits, &outbox, &pending); err != nil {
				t.Fatalf("inspect committed discard obligation: %v", err)
			}
			if operations != 1 || audits != len(discarded) || outbox != 1 || pending != 1 {
				t.Fatalf("discard obligation before delivery: operations=%d audits=%d outbox=%d pending=%d",
					operations, audits, outbox, pending)
			}
			if len(discarded) == 1 {
				return taskworkspace.DiscardRuntimeViewResult{}, &taskworkspace.Error{
					Code: taskworkspace.ErrorRetryableUnavailable,
				}
			}
			return taskworkspace.DiscardRuntimeViewResult{
				TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
				BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
				Reason: request.Reason, Generation: request.Generation, Fence: request.Fence,
				Operation: request.Operation,
			}, nil
		},
	}
	rejectInputs := ImmutableInputValidatorFunc(func(
		context.Context,
		ImmutableInputValidationRequest,
	) (PrerequisiteObservation, error) {
		return PrerequisiteObservation{
			Disposition: PrerequisiteObservationRejected, Failure: PrerequisiteFailureCorrupt,
		}, nil
	})
	var store *PostgresAuthority
	var start StartRuntimeRun
	db, schema, store, _, start = newPostgresReadyMutatingPrerequisiteRuntime(
		t, "runtime_discard", now, func() time.Time { return now }, lifecycle, rejectInputs,
	)
	decision, err := store.Execute(context.Background(), start)
	if err != nil || decision.Snapshot.State != RuntimeTerminal || decision.Snapshot.Outcome != RuntimeRejected ||
		decision.Snapshot.Lease.Disposition != LeaseRevoked ||
		decision.Snapshot.Cleanup.FenceRuntimeView ||
		decision.Snapshot.Readiness.ImmutableInputs.State != PrerequisiteRejected ||
		decision.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("PostgreSQL input rejection: %+v err=%v", decision, err)
	}
	if len(discarded) != 1 {
		t.Fatalf("PostgreSQL initial discard calls=%d, want 1", len(discarded))
	}
	firstDiscard := discarded[0]
	if _, err := store.Execute(context.Background(), start); err != nil {
		t.Fatalf("PostgreSQL retry ambiguous discard: %v", err)
	}
	if len(discarded) != 2 || discarded[0] != discarded[1] ||
		firstDiscard.RuntimeViewID != "postgres-discard-view" ||
		firstDiscard.SandboxLeaseAuthority != opened.SandboxLeaseAuthority ||
		firstDiscard.Reason != taskworkspace.RuntimeViewValidationRejected ||
		firstDiscard.Operation.RequestDigest != firstDiscard.CanonicalRequestDigest() {
		t.Fatalf("PostgreSQL discard drifted: open=%+v discard=%+v", opened, discarded)
	}
	var acknowledged, deliveries, c03CleanupDebt int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery
		 WHERE disposition=$1),
		(SELECT coalesce(sum(delivery_count),0) FROM `+schema+`.runtime_execution_runtime_view_terminal_outbox_delivery),
		(SELECT count(*) FROM `+schema+`.runtime_execution_cleanup_obligations)`, OutboxAcknowledged).
		Scan(&acknowledged, &deliveries, &c03CleanupDebt); err != nil {
		t.Fatal(err)
	}
	if acknowledged != 1 || deliveries != 2 || c03CleanupDebt != 0 {
		t.Fatalf("discard delivery/debt ownership: acknowledged=%d deliveries=%d c03Debt=%d",
			acknowledged, deliveries, c03CleanupDebt)
	}
	if _, err := store.Execute(context.Background(), start); err != nil || len(discarded) != 2 {
		t.Fatalf("discard exact replay redelivered: err=%v discard=%d", err, len(discarded))
	}
}

func acceptedPrerequisiteObservation(t *testing.T, evidence string, evidenceDigest Digest) PrerequisiteObservation {
	t.Helper()
	evidenceID, err := NewEvidenceID(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return PrerequisiteObservation{
		Disposition: PrerequisiteObservationAccepted,
		EvidenceID:  evidenceID, EvidenceDigest: evidenceDigest,
	}
}

func installPostgresActiveLeaseForPrerequisiteTest(
	t *testing.T,
	db interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	},
	schema string,
	fixture RuntimeFixture,
	now time.Time,
) {
	t.Helper()
	node := fixture.Node
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_nodes (
		execution_node_id, node_generation, readiness, attestation_id, attestation_generation,
		attested_at, expires_at, resource_class_id, execution_policy_id, node_authority_id,
		worker_authority_id, worker_generation, authorization_generation, authorization_expires_at,
		release_safety_epoch, catalog_safety_epoch, occupancy, quarantined, containment, reset_status,
		active_runtime_run_id, active_lease_id, last_sandbox_generation, last_sandbox_fence, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,FALSE,$18,$19,$20,$21,$22,$23,$24)`,
		node.ExecutionNodeID.String(), node.Generation, node.Readiness, node.AttestationID.String(),
		node.AttestationGeneration, node.AttestedAt, node.ExpiresAt, fixture.Operation.ResourceClassID.String(),
		fixture.Operation.ExecutionPolicyID.String(), fixture.Lease.NodeAuthorityID.String(),
		fixture.Lease.WorkerAuthorityID.String(), fixture.Lease.WorkerGeneration,
		fixture.Lease.AuthorizationGeneration, fixture.Lease.AuthorizationExpiresAt, fixture.SafetyEpoch,
		fixture.CatalogSafetyEpoch, node.Occupancy, node.Containment, node.Reset, fixture.RuntimeRunID.String(),
		fixture.Lease.LeaseID.String(), fixture.Lease.SandboxGeneration, fixture.Lease.SandboxFence, now); err != nil {
		t.Fatalf("install active PostgreSQL node: %v", err)
	}
	lease := fixture.Lease
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_prelease_leases (
		lease_acquire_operation_id, lease_acquire_digest, runtime_run_id, start_operation_id, start_digest,
		work_item_id, admission_grant_id, grant_generation, execution_node_id, node_capacity_generation,
		resource_class_id, execution_policy_id, scheduler_epoch, policy_version, safety_epoch,
		sandbox_lease_id, lease_generation, lease_fence, lease_disposition, lease_expires_at,
		sandbox_id, sandbox_generation, sandbox_fence, worker_authority_id, worker_generation,
		node_authority_id, authorization_generation, authorization_expires_at, catalog_safety_epoch, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
		lease.AcquireOperationID.String(), lease.AcquireDigest[:], fixture.RuntimeRunID.String(),
		fixture.Operation.OperationID.String(), fixture.Operation.Digest[:], fixture.Operation.WorkItemID.String(),
		fixture.Operation.AdmissionGrantID.String(), fixture.Operation.GrantGeneration,
		fixture.Operation.ExecutionNodeID.String(), fixture.Operation.NodeCapacityGeneration,
		fixture.Operation.ResourceClassID.String(), fixture.Operation.ExecutionPolicyID.String(),
		fixture.Operation.SchedulerEpoch, fixture.Operation.PolicyVersion, fixture.SafetyEpoch,
		lease.LeaseID.String(), lease.Generation, lease.Fence, lease.Disposition, lease.ExpiresAt,
		lease.SandboxID.String(), lease.SandboxGeneration, lease.SandboxFence,
		lease.WorkerAuthorityID.String(), lease.WorkerGeneration, lease.NodeAuthorityID.String(),
		lease.AuthorizationGeneration, lease.AuthorizationExpiresAt, fixture.CatalogSafetyEpoch, now); err != nil {
		t.Fatalf("install active PostgreSQL lease: %v", err)
	}
}

func newPostgresReadyMutatingPrerequisiteRuntime(
	t *testing.T,
	schemaPrefix string,
	now time.Time,
	nowFunc func() time.Time,
	lifecycle RuntimeViewPrerequisitePort,
	inputValidator ImmutableInputValidator,
) (*sql.DB, string, *PostgresAuthority, PostgresConfig, StartRuntimeRun) {
	t.Helper()
	db, schema := testpostgres.Open(t, schemaPrefix)
	authority := mustTaskOrchestrationAuthority(t, schemaPrefix+"-authority", 9)
	input := standardStart(t, now, authority, schemaPrefix).StartRuntimeRunInput
	input.Effect = EffectMutating
	input.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:     mustTaskWorkspaceID(t, schemaPrefix+"-workspace"),
		MaterializationID:   mustTaskWorkspaceMaterializationID(t, schemaPrefix+"-materialization"),
		BaseRevisionID:      mustTaskWorkspaceRevisionID(t, schemaPrefix+"-revision"),
		LifecycleGeneration: 4, LifecycleFence: 5,
		ExpiryPolicy: RuntimeViewExpiryAtDeadline, OpenOperationDerivation: digest(108),
	}
	start := mustStart(t, input)
	leaseOperationID, leaseDigest := stableLeaseAcquireBinding(start)
	lease := RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, AcquireOperationID: leaseOperationID, AcquireDigest: leaseDigest,
		LeaseID: SandboxLeaseID{value: schemaPrefix + "-lease"}, Generation: 1, Fence: 1,
		Disposition: LeaseActive, ExpiresAt: now.Add(90 * time.Second),
		SandboxID: SandboxID{value: schemaPrefix + "-sandbox"}, SandboxGeneration: 1, SandboxFence: 1,
		WorkerAuthorityID: WorkerAuthorityID{value: schemaPrefix + "-worker"}, WorkerGeneration: 1,
		NodeAuthorityID:         NodeAuthorityID{value: schemaPrefix + "-node-authority"},
		AuthorizationGeneration: 1, AuthorizationExpiresAt: now.Add(30 * time.Minute),
	}
	readiness := initialRuntimeReadiness(start)
	readiness.Lease = leasePrerequisiteFact(lease)
	fixture := acceptedPostgresRuntimeFixture(start, authority, now)
	fixture.RuntimeRevision++
	fixture.State = RuntimePreparingPrerequisites
	fixture.Operation.WorkItemID = start.AdmissionGrant.WorkItemID
	fixture.Operation.ExecutionNodeID = ExecutionNodeID{value: schemaPrefix + "-node"}
	fixture.Operation.NodeCapacityGeneration = 1
	fixture.Operation.ResourceClassID = start.ResourceClassID
	fixture.Operation.ExecutionPolicyID = start.ExecutionPolicyID
	fixture.Operation.SchedulerEpoch = 1
	fixture.Operation.PolicyVersion = 1
	fixture.Lease = lease
	fixture.Node = RuntimeNodeSnapshot{
		ExecutionNodeID: fixture.Operation.ExecutionNodeID, Generation: 1, Readiness: NodeReady,
		AttestationID: NodeAttestationID{value: schemaPrefix + "-attestation"}, AttestationGeneration: 1,
		AttestedAt: now.Add(-time.Minute), ExpiresAt: now.Add(30 * time.Minute), Occupancy: NodeOccupied,
		Containment: ContainmentPending, Reset: ResetRequired,
	}
	fixture.Capacity.Physical = PhysicalCapacityOccupied
	fixture.Readiness = readiness
	if inputValidator == nil {
		inputValidator = ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, schemaPrefix+"-input-evidence", digest(110)), nil
		})
	}
	config := PostgresConfig{
		Schema: schema, Now: nowFunc,
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, schemaPrefix+"-release-evidence", digest(109)), nil
		}),
		ImmutableInputValidator: inputValidator,
		RuntimeViewPrerequisite: lifecycle,
	}
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	fact := retainedAcceptedStartFact(start, "runtime-decision-"+schemaPrefix)
	installPostgresAcceptedStartFacts(t, db, schema, start, fact, now)
	installPostgresActiveLeaseForPrerequisiteTest(t, db, schema, fixture, now)
	persistPostgresAcceptedRuntimeBindingFact(t, store, start)
	return db, schema, store, config, start
}

func persistPostgresAcceptedRuntimeBindingFact(
	t *testing.T,
	store *PostgresAuthority,
	start StartRuntimeRun,
) {
	t.Helper()
	request := runtimeBindingValidationRequest(start)
	if store.runtimeBindingValidator == nil {
		t.Fatal("Runtime Binding validator is required by active-lease prerequisite fixture")
	}
	observation, observationErr := store.runtimeBindingValidator.ValidateRuntimeBinding(context.Background(), request)
	canonical, err := canonicalRuntimeBindingValidationRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := prerequisiteFactFromObservation(
		request.OperationID,
		request.CanonicalRequestDigest,
		observation,
		observationErr,
	)
	if err != nil || fact.State != PrerequisiteAccepted {
		t.Fatal(err)
	}
	if err := store.persistPostgresPrerequisiteFact(
		context.Background(), start, postgresPrerequisiteRuntimeBinding, canonical, fact,
		RuntimeViewBindingSnapshot{},
	); err != nil {
		t.Fatal(err)
	}
}

func acceptedRuntimeViewResult(
	request taskworkspace.OpenRuntimeViewRequest,
	runtimeViewID taskworkspace.RuntimeViewID,
) taskworkspace.OpenRuntimeViewResult {
	return taskworkspace.OpenRuntimeViewResult{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, RuntimeViewID: runtimeViewID,
		TaskWorkspaceID: request.TaskWorkspaceID, MaterializationID: request.MaterializationID,
		BaseRevisionID: request.BaseRevisionID, PhaseRunID: request.PhaseRunID,
		RuntimeRunID: request.RuntimeRunID, RuntimeOperationID: request.RuntimeOperationID,
		SandboxLeaseAuthority: request.SandboxLeaseAuthority, EffectClass: request.EffectClass,
		ExpiresAt: request.ExpiresAt, Generation: request.Generation, Fence: request.Fence,
		Operation: request.Operation,
	}
}

func acceptedFenceRuntimeViewResult(
	request taskworkspace.FenceRuntimeViewRequest,
) taskworkspace.FenceRuntimeViewResult {
	return taskworkspace.FenceRuntimeViewResult{
		TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
		BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
		Reason: request.Reason, Generation: request.Generation,
		PreviousFence: request.Fence, Fence: request.Fence + 1, Operation: request.Operation,
	}
}

func assertPostgresMutationRejected(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(context.Background(), statement, args...); err == nil {
		t.Fatalf("PostgreSQL mutation unexpectedly succeeded: %s", statement)
	}
}

func assertNoSensitivePrerequisiteDetails(
	t *testing.T,
	db *sql.DB,
	schema string,
	decision RuntimeDecision,
	err error,
	canary string,
) {
	t.Helper()
	public := fmt.Sprintf("%+v %v", decision, err)
	if strings.Contains(public, canary) {
		t.Fatalf("public Runtime decision leaked sensitive prerequisite details: %s", public)
	}
	var retained string
	query := `SELECT concat_ws('|',
		coalesce((SELECT string_agg(aggregate_state::text,'') FROM ` + schema + `.runtime_execution_runtimes),''),
		coalesce((SELECT string_agg(convert_from(canonical_request,'UTF8') || fact_state::text || view_binding::text,'') FROM ` + schema + `.runtime_execution_prerequisite_operations),''),
		coalesce((SELECT string_agg(fact_state::text,'') FROM ` + schema + `.runtime_execution_prerequisite_audit),''),
		coalesce((SELECT string_agg(convert_from(payload,'UTF8'),'') FROM ` + schema + `.runtime_execution_prerequisite_outbox),''),
		coalesce((SELECT string_agg(convert_from(canonical_request,'UTF8') || error_code,'') FROM ` + schema + `.runtime_execution_runtime_view_terminal_operations),''),
		coalesce((SELECT string_agg(error_code,'') FROM ` + schema + `.runtime_execution_runtime_view_terminal_audit),''),
		coalesce((SELECT string_agg(convert_from(payload,'UTF8'),'') FROM ` + schema + `.runtime_execution_runtime_view_terminal_outbox),'')
	)`
	if err := db.QueryRowContext(context.Background(), query).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(retained, canary) {
		t.Fatalf("PostgreSQL prerequisite state leaked sensitive details: %s", retained)
	}
}
