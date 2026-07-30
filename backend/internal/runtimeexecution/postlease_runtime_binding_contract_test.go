package runtimeexecution

import (
	"context"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestPostLeaseRuntimeBindingRevocationStopsLaterPrerequisitesAndDiscardsRuntimeView(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 22, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postlease-binding-revoked-authority", 7)
	start, grant, node := mutatingPrerequisiteStart(
		t, now, authority, "postlease-binding-revoked", digest(171),
	)
	bindingCalls := 0
	openCalls := 0
	inputCalls := 0
	discardCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			return acceptedRuntimeViewResult(request, "postlease-binding-revoked-view"), nil
		},
		DiscardRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			discardCalls++
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
			bindingCalls++
			if bindingCalls == 1 {
				return acceptedPrerequisiteObservation(t, "postlease-binding-accepted", digest(172)), nil
			}
			return PrerequisiteObservation{
				Disposition: PrerequisiteObservationRejected,
				Failure:     PrerequisiteFailureRevoked,
			}, nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postlease-binding-input", digest(173)), nil
		}),
		RuntimeViewPrerequisite: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}

	ready, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || bindingCalls != 1 ||
		openCalls != 1 || inputCalls != 1 {
		t.Fatalf("initial readiness: %+v err=%v binding=%d open=%d input=%d",
			ready, err, bindingCalls, openCalls, inputCalls)
	}
	rejected, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || rejected.Snapshot.State != RuntimeTerminal ||
		rejected.Snapshot.Outcome != RuntimeRejected ||
		rejected.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteRejected ||
		rejected.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureRevoked ||
		rejected.Snapshot.Readiness.CapsuleReady ||
		rejected.Snapshot.Lease.Disposition != LeaseRevoked ||
		rejected.Snapshot.Cleanup.Status != LeaseCleanupPending ||
		bindingCalls != 2 || openCalls != 1 || inputCalls != 1 || discardCalls != 1 {
		t.Fatalf("post-lease revocation crossed prerequisite gate: %+v err=%v binding=%d open=%d input=%d discard=%d",
			rejected, err, bindingCalls, openCalls, inputCalls, discardCalls)
	}
}

func TestPostgresPostLeaseRuntimeBindingRevocationStopsLaterPrerequisitesAndDiscardsRuntimeView(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 35, 0, 0, time.UTC)
	openCalls := 0
	inputCalls := 0
	discardCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			return acceptedRuntimeViewResult(request, "postgres-postlease-binding-revoked-view"), nil
		},
		DiscardRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.DiscardRuntimeViewRequest,
		) (taskworkspace.DiscardRuntimeViewResult, error) {
			discardCalls++
			return taskworkspace.DiscardRuntimeViewResult{
				TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
				BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
				Reason: request.Reason, Generation: request.Generation, Fence: request.Fence,
				Operation: request.Operation,
			}, nil
		},
	}
	db, _, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "pl_bind_revoke", now, func() time.Time { return now }, lifecycle,
		ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postgres-postlease-binding-input", digest(174)), nil
		}),
	)
	ready, err := store.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || openCalls != 1 || inputCalls != 1 {
		t.Fatalf("initial PostgreSQL readiness: %+v err=%v open=%d input=%d",
			ready, err, openCalls, inputCalls)
	}

	revalidationCalls := 0
	config.RuntimeBindingValidator = RuntimeBindingValidatorFunc(func(
		context.Context,
		RuntimeBindingValidationRequest,
	) (PrerequisiteObservation, error) {
		revalidationCalls++
		return PrerequisiteObservation{
			Disposition: PrerequisiteObservationRejected,
			Failure:     PrerequisiteFailureRevoked,
		}, nil
	})
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := restarted.Execute(context.Background(), start)
	if err != nil || rejected.Snapshot.State != RuntimeTerminal ||
		rejected.Snapshot.Outcome != RuntimeRejected ||
		rejected.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteRejected ||
		rejected.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureRevoked ||
		rejected.Snapshot.Readiness.CapsuleReady ||
		rejected.Snapshot.Lease.Disposition != LeaseRevoked ||
		rejected.Snapshot.Cleanup.Status != LeaseCleanupPending ||
		revalidationCalls != 1 || openCalls != 1 || inputCalls != 1 || discardCalls != 1 {
		t.Fatalf("PostgreSQL post-lease revocation crossed prerequisite gate: %+v err=%v binding=%d open=%d input=%d discard=%d",
			rejected, err, revalidationCalls, openCalls, inputCalls, discardCalls)
	}
}

func TestPostLeaseAmbiguousRuntimeBindingRevocationClearsReadiness(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 22, 40, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postlease-binding-ambiguous-authority", 7)
	start := standardStart(t, now, authority, "postlease-binding-ambiguous")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "postlease-binding-ambiguous-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	bindingCalls := 0
	inputCalls := 0
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
			bindingCalls++
			if bindingCalls == 1 {
				return acceptedPrerequisiteObservation(t, "postlease-binding-before-ambiguity", digest(175)), nil
			}
			return PrerequisiteObservation{Disposition: PrerequisiteObservationAmbiguous}, nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postlease-binding-ambiguous-input", digest(176)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || bindingCalls != 1 || inputCalls != 1 {
		t.Fatalf("initial readiness before ambiguity: %+v err=%v binding=%d input=%d",
			ready, err, bindingCalls, inputCalls)
	}
	ambiguous, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || ambiguous.Snapshot.State != RuntimePreparingPrerequisites ||
		ambiguous.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired ||
		ambiguous.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureDependencyUnavailable ||
		ambiguous.Snapshot.Readiness.CapsuleReady || bindingCalls != 2 || inputCalls != 1 {
		t.Fatalf("ambiguous post-lease binding retained readiness: %+v err=%v binding=%d input=%d",
			ambiguous, err, bindingCalls, inputCalls)
	}
}

func TestPostgresPostLeaseAmbiguousRuntimeBindingRevocationClearsReadiness(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 45, 0, 0, time.UTC)
	openCalls := 0
	inputCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			return acceptedRuntimeViewResult(request, "postgres-postlease-binding-ambiguous-view"), nil
		},
	}
	db, _, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "pl_bind_amb", now, func() time.Time { return now }, lifecycle,
		ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postgres-postlease-binding-ambiguous-input", digest(177)), nil
		}),
	)
	ready, err := store.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || openCalls != 1 || inputCalls != 1 {
		t.Fatalf("initial PostgreSQL readiness before ambiguity: %+v err=%v open=%d input=%d",
			ready, err, openCalls, inputCalls)
	}
	revalidationCalls := 0
	config.RuntimeBindingValidator = RuntimeBindingValidatorFunc(func(
		context.Context,
		RuntimeBindingValidationRequest,
	) (PrerequisiteObservation, error) {
		revalidationCalls++
		return PrerequisiteObservation{Disposition: PrerequisiteObservationAmbiguous}, nil
	})
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := restarted.Execute(context.Background(), start)
	if err != nil || ambiguous.Snapshot.State != RuntimePreparingPrerequisites ||
		ambiguous.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired ||
		ambiguous.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureDependencyUnavailable ||
		ambiguous.Snapshot.Readiness.CapsuleReady || revalidationCalls != 1 ||
		openCalls != 1 || inputCalls != 1 {
		t.Fatalf("ambiguous PostgreSQL post-lease binding retained readiness: %+v err=%v binding=%d open=%d input=%d",
			ambiguous, err, revalidationCalls, openCalls, inputCalls)
	}
}

func TestPostLeaseMissingRuntimeBindingValidatorDurablyClearsReadiness(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 22, 50, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "postlease-binding-validator-missing-authority", 7)
	start := standardStart(t, now, authority, "postlease-binding-validator-missing")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "postlease-binding-validator-missing-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	inputCalls := 0
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
			return acceptedPrerequisiteObservation(t, "postlease-binding-before-validator-loss", digest(178)), nil
		}),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postlease-binding-validator-missing-input", digest(179)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || inputCalls != 1 {
		t.Fatalf("initial readiness before validator loss: %+v err=%v input=%d", ready, err, inputCalls)
	}
	engine, ok := harness.Runtime.(*invariantEngine)
	if !ok {
		t.Fatal("deterministic harness does not expose its Runtime Execution adapter")
	}
	engine.runtimeBindingValidator = nil
	failedClosed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || failedClosed.Snapshot.State != RuntimePreparingPrerequisites ||
		failedClosed.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired ||
		failedClosed.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureDependencyUnavailable ||
		failedClosed.Snapshot.Readiness.CapsuleReady || inputCalls != 1 {
		t.Fatalf("missing post-lease validator retained readiness: %+v err=%v input=%d",
			failedClosed, err, inputCalls)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || inspected.Readiness.RuntimeBinding != failedClosed.Snapshot.Readiness.RuntimeBinding ||
		inspected.Readiness.CapsuleReady {
		t.Fatalf("missing-validator fact was not durable: %+v err=%v", inspected, err)
	}
}

func TestPostgresPostLeaseMissingRuntimeBindingValidatorDurablyClearsReadiness(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 55, 0, 0, time.UTC)
	openCalls := 0
	inputCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			return acceptedRuntimeViewResult(request, "postgres-postlease-binding-validator-missing-view"), nil
		},
	}
	db, _, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "pl_bind_no_val", now, func() time.Time { return now }, lifecycle,
		ImmutableInputValidatorFunc(func(
			context.Context,
			ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			inputCalls++
			return acceptedPrerequisiteObservation(t, "postgres-postlease-binding-validator-missing-input", digest(180)), nil
		}),
	)
	ready, err := store.Execute(context.Background(), start)
	if err != nil || !ready.Snapshot.Readiness.CapsuleReady || openCalls != 1 || inputCalls != 1 {
		t.Fatalf("initial PostgreSQL readiness before validator loss: %+v err=%v open=%d input=%d",
			ready, err, openCalls, inputCalls)
	}
	config.RuntimeBindingValidator = nil
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	failedClosed, err := restarted.Execute(context.Background(), start)
	if err != nil || failedClosed.Snapshot.State != RuntimePreparingPrerequisites ||
		failedClosed.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired ||
		failedClosed.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureDependencyUnavailable ||
		failedClosed.Snapshot.Readiness.CapsuleReady || openCalls != 1 || inputCalls != 1 {
		t.Fatalf("missing PostgreSQL post-lease validator retained readiness: %+v err=%v open=%d input=%d",
			failedClosed, err, openCalls, inputCalls)
	}
	inspected, err := restarted.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || inspected.Readiness.RuntimeBinding != failedClosed.Snapshot.Readiness.RuntimeBinding ||
		inspected.Readiness.CapsuleReady {
		t.Fatalf("missing-validator PostgreSQL fact was not durable: %+v err=%v", inspected, err)
	}
}
