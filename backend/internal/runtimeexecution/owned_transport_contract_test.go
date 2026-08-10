package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

// TestOwnedWorkerTransportMachineAuthorizationAndStrictVersioning covers the
// production-shaped owned transport verification: machine authorization and
// strict versioning fail closed before any worker effect.
func TestOwnedWorkerTransportMachineAuthorizationAndStrictVersioning(t *testing.T) {
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "transport-auth-caller", 9)
	start := standardStart(t, now, authority, "transport-auth")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = ExecutionNodeID{value: "transport-node"}, 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	backend := newContractToolWorkerBackend()
	adapter, err := newToolWorkerCapabilityAdapter(validToolPlanForStart(start), backend)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "transport-auth-input-evidence", digest(246)), nil
		}),
		toolWorker: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare transport capsule: %+v err=%v", prepared, err)
	}
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
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
	envelope := OwnedWorkerTransportEnvelope{
		SchemaVersion:     OwnedWorkerTransportWireSchemaV1,
		Kind:              "worker_accept",
		OperationID:       acceptCommand.OperationID.String(),
		RuntimeRunID:      acceptCommand.RuntimeRunID.String(),
		WorkerAuthorityID: acceptCommand.WorkerAuthorityID.String(),
		WorkerGeneration:  acceptCommand.WorkerGeneration,
		NodeAuthorityID:   acceptCommand.NodeAuthorityID.String(),
		NodeGeneration:    acceptCommand.NodeGeneration,
		CanonicalDigest:   acceptCommand.CanonicalDigest,
	}
	transport := newOwnedWorkerTransport(harness.workers, func() time.Time { return now })

	// Correct machine authorization delivers and the worker accepts once.
	machine := OwnedWorkerMachineAuthorization{
		WorkerAuthorityID: acceptCommand.WorkerAuthorityID, WorkerGeneration: acceptCommand.WorkerGeneration,
		NodeAuthorityID: acceptCommand.NodeAuthorityID, NodeGeneration: acceptCommand.NodeGeneration,
	}
	if err := transport.deliverEnvelope(context.Background(), envelope, machine, acceptCommand); err != nil {
		t.Fatalf("authorized delivery: %v", err)
	}
	if backend.acceptCount() != 1 {
		t.Fatalf("authorized accept calls=%d", backend.acceptCount())
	}

	// Wrong machine authorization is denied before any worker effect.
	wrongMachine := OwnedWorkerMachineAuthorization{
		WorkerAuthorityID: WorkerAuthorityID{value: "intruder-worker"}, WorkerGeneration: 99,
		NodeAuthorityID: NodeAuthorityID{value: "intruder-node"}, NodeGeneration: 99,
	}
	if err := transport.deliverEnvelope(context.Background(), envelope, wrongMachine, acceptCommand); errorCode(err) != ErrorAuthorizationDenied {
		t.Fatalf("wrong machine auth = %v, want authorization denied", err)
	}
	if backend.acceptCount() != 1 {
		t.Fatalf("unauthorized delivery reached worker: calls=%d", backend.acceptCount())
	}

	// Unknown major version is unsupported schema.
	oldEnvelope := envelope
	oldEnvelope.SchemaVersion = "slidesmith.runtime-execution.owned-worker-transport/v0"
	if err := transport.deliverEnvelope(context.Background(), oldEnvelope, machine, acceptCommand); errorCode(err) != ErrorInvalidRequest {
		t.Fatalf("old major delivery = %v, want invalid request", err)
	}

	// Payload/identity mismatch fails closed before any worker effect.
	mismatched := envelope
	mismatched.RuntimeRunID = "runtime-intruder-run"
	if err := transport.deliverEnvelope(context.Background(), mismatched, machine, acceptCommand); errorCode(err) != ErrorIntegrityConflict {
		t.Fatalf("identity mismatch = %v, want integrity conflict", err)
	}
	if backend.acceptCount() != 1 {
		t.Fatalf("mismatched delivery reached worker: calls=%d", backend.acceptCount())
	}
}

// TestOwnedWorkerTransportAtLeastOnceAndAckLoss covers at-least-once delivery
// and ack-loss reconciliation: redelivering the exact envelope is idempotent,
// and an acknowledged operation replayed after a lost ack returns the exact
// original ack without a second worker effect.
func TestOwnedWorkerTransportAtLeastOnceAndAckLoss(t *testing.T) {
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "transport-at-least-once-caller", 9)
	start := standardStart(t, now, authority, "transport-at-least-once")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = ExecutionNodeID{value: "transport-once-node"}, 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	backend := newContractToolWorkerBackend()
	adapter, err := newToolWorkerCapabilityAdapter(validToolPlanForStart(start), backend)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, "transport-once-input-evidence", digest(247)), nil
		}),
		toolWorker: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare transport capsule: %+v err=%v", prepared, err)
	}
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
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
	envelope := OwnedWorkerTransportEnvelope{
		SchemaVersion:     OwnedWorkerTransportWireSchemaV1,
		Kind:              "worker_accept",
		OperationID:       acceptCommand.OperationID.String(),
		RuntimeRunID:      acceptCommand.RuntimeRunID.String(),
		WorkerAuthorityID: acceptCommand.WorkerAuthorityID.String(),
		WorkerGeneration:  acceptCommand.WorkerGeneration,
		NodeAuthorityID:   acceptCommand.NodeAuthorityID.String(),
		NodeGeneration:    acceptCommand.NodeGeneration,
		CanonicalDigest:   acceptCommand.CanonicalDigest,
	}
	machine := OwnedWorkerMachineAuthorization{
		WorkerAuthorityID: acceptCommand.WorkerAuthorityID, WorkerGeneration: acceptCommand.WorkerGeneration,
		NodeAuthorityID: acceptCommand.NodeAuthorityID, NodeGeneration: acceptCommand.NodeGeneration,
	}
	transport := newOwnedWorkerTransport(harness.workers, func() time.Time { return now })

	// First delivery is accepted by the worker once.
	if err := transport.deliverEnvelope(context.Background(), envelope, machine, acceptCommand); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	firstAck, err := harness.workers.accept(context.Background(), acceptCommand)
	if err != nil || !firstAck.DurablyAccepted {
		t.Fatalf("first ack: %+v err=%v", firstAck, err)
	}
	// Ack loss: redeliver the exact envelope; the worker protocol replays the
	// exact ack and the backend is not called again (at-least-once is safe).
	if err := transport.deliverEnvelope(context.Background(), envelope, machine, acceptCommand); err != nil {
		t.Fatalf("ack-loss redelivery: %v", err)
	}
	replayedAck, err := harness.workers.accept(context.Background(), acceptCommand)
	if err != nil || replayedAck != firstAck || backend.acceptCount() != 1 {
		t.Fatalf("ack-loss replay = %+v calls=%d err=%v", replayedAck, backend.acceptCount(), err)
	}
}
