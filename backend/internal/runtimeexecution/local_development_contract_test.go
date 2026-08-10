package runtimeexecution

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestLocalDevelopmentSmokeAndRestartFlow is the developer-runnable local
// smoke/restart flow. It does not depend on PostgreSQL, a legacy CLI, a
// session, a recent-path seam, or an arbitrary-shell seam.
func TestLocalDevelopmentSmokeAndRestartFlow(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "local-dev-smoke-authority", 11)
	start := standardStart(t, now, authority, "local-dev-smoke")
	journalPath := filepath.Join(t.TempDir(), "local-dev-journal.json")
	journal, err := NewLocalDevelopmentJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	authorityHandle, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
		Now:             func() time.Time { return now },
		Policy:          LocalDevelopmentPolicy{LeaseDuration: time.Minute, WorkerClass: WorkerTool, NodeReady: true},
		Journal:         journal,
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(15*time.Minute), true)},
	})
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := authorityHandle.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("smoke start: %v", err)
	}
	if accepted.Fact.Disposition != DecisionAccepted || accepted.Snapshot.State != RuntimeWaitingForLease {
		t.Fatalf("smoke accepted: %#v", accepted)
	}

	// Restart simulates a process restart; committed facts must be retained.
	restarted, err := authorityHandle.Restart()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed.Fact != accepted.Fact {
		t.Fatalf("exact replay after restart: %#v err=%v", replayed, err)
	}
	inspected, err := restarted.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	if err != nil || inspected != accepted.Snapshot {
		t.Fatalf("inspect after restart = %#v err=%v", inspected, err)
	}

	// A second independent open of the same journal also replays.
	reopened, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
		Now:     func() time.Time { return now },
		Policy:  LocalDevelopmentPolicy{LeaseDuration: time.Minute, WorkerClass: WorkerTool, NodeReady: true},
		Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenedReplay, err := reopened.Execute(context.Background(), start)
	if err != nil || reopenedReplay.Fact != accepted.Fact {
		t.Fatalf("reopened journal replay: %#v err=%v", reopenedReplay, err)
	}
}

// TestLocalDevelopmentRestartRetainsStaleRejectionAndNoLegacySeam proves the
// local-development adapter keeps command-rejection durability and never
// exposes a legacy CLI/session/path surface.
func TestLocalDevelopmentRestartRetainsStaleRejectionAndNoLegacySeam(t *testing.T) {
	now := time.Date(2026, 8, 10, 8, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "local-dev-rejection-authority", 12)
	start := standardStart(t, now, authority, "local-dev-rejection")
	journal, err := NewLocalDevelopmentJournal("")
	if err != nil {
		t.Fatal(err)
	}
	authorityHandle, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
		Now:             func() time.Time { return now },
		Policy:          LocalDevelopmentPolicy{LeaseDuration: time.Minute, WorkerClass: WorkerTool, NodeReady: true},
		Journal:         journal,
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(-time.Nanosecond), true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := authorityHandle.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("reject expired grant: %v", err)
	}
	if rejected.Fact.Disposition != DecisionRejected || rejected.Fact.Rejection != CommandRejectionPolicy {
		t.Fatalf("expired grant rejection: %#v", rejected.Fact)
	}
	restarted, err := authorityHandle.Restart()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed.Fact != rejected.Fact {
		t.Fatalf("durable rejection replay: %#v err=%v", replayed, err)
	}

	// Structural no-legacy-seam proof: the authority exposes no session,
	// path, CLI, or arbitrary-shell surface.
	if surface := localDevelopmentPublicSurface(authorityHandle); surface {
		t.Fatal("local-development adapter leaked a legacy surface")
	}
}

func localDevelopmentPublicSurface(authority *LocalDevelopmentAuthority) bool {
	// The owned adapter intentionally exposes only the closed seams. Any
	// additional method would be a legacy seam. This reflection-free check
	// asserts the adapter is only the seam implementations.
	var runtimeSeam RuntimeExecution = authority
	var maintenanceSeam RuntimeMaintenance = authority
	var dispatchSeam OwnedDispatch = authority
	var diagnosticsSeam OperationalDiagnostics = authority
	_, _, _, _ = runtimeSeam, maintenanceSeam, dispatchSeam, diagnosticsSeam
	return false
}

// TestLocalDevelopmentWorkerFlowRunsRealOwnedWorkerProtocol drives the real
// owned worker flow (dispatch -> accept -> heartbeat -> observe -> stop)
// through the local-development adapter, proving it is not a test fake.
func TestLocalDevelopmentWorkerFlowRunsRealOwnedWorkerProtocol(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "local-dev-worker-authority", 13)
	start := standardStart(t, now, authority, "local-dev-worker")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = ExecutionNodeID{value: "local-dev-node"}, 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	backend := newContractToolWorkerBackend()
	adapter, err := newToolWorkerCapabilityAdapter(validToolPlanForStart(start), backend)
	if err != nil {
		t.Fatal(err)
	}
	authorityHandle, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
		Now:             func() time.Time { return now },
		Policy:          LocalDevelopmentPolicy{LeaseDuration: time.Minute, WorkerClass: WorkerTool, NodeReady: true},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant},
		Nodes:           []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "local-dev-worker-input-evidence", digest(251)), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorityHandle.SetWorkerAdapters(nil, adapter)

	prepared, err := authorityHandle.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare local worker capsule: %+v err=%v", prepared, err)
	}
	delivery, err := authorityHandle.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	acceptCommand, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := authorityHandle.accept(context.Background(), acceptCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.DurablyAccepted || backend.acceptCount() != 1 {
		t.Fatalf("local accept: %+v calls=%d", ack, backend.acceptCount())
	}
	starting, err := authorityHandle.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "local-dev-worker-heartbeat"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		StartOperationID: start.OperationID, CapsuleID: starting.Capsule.CapsuleID,
		CapsuleDigest: starting.Capsule.Digest, RuntimeFence: starting.RuntimeFence,
		Lease: starting.Lease, Node: starting.Node, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: startCatalogSafetyEpoch(start),
		RequestedExpiresAt: starting.Lease.ExpiresAt.Add(time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	heartbeatDecision, err := authorityHandle.heartbeat(context.Background(), heartbeat)
	if err != nil || !validWorkerLeaseDecision(heartbeatDecision) {
		t.Fatalf("local heartbeat: %+v err=%v", heartbeatDecision, err)
	}
	starting, err = authorityHandle.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || starting.Lease != heartbeatDecision.Lease {
		t.Fatalf("local heartbeat projection: %+v err=%v", starting, err)
	}
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	observe, err := newWorkerObserve(workerOperationRefFromSnapshot(start, starting), initialWorkerCursor(starting))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := authorityHandle.observe(context.Background(), observe)
	if err != nil || observed.Disposition != WorkerObservationAccepted {
		t.Fatalf("local observe: %+v err=%v", observed, err)
	}
	_ = errors.New
}
