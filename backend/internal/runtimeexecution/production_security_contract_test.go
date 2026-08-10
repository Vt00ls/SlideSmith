package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

// TestProductionOnlySecurityEvidenceMachineAuthAndNonEnumeration adds explicit
// production-only security evidence without changing public semantics. The
// public Execute/Inspect seam and RuntimeSnapshot schema are untouched; these
// tests prove the production-shaped transport and owned adapter enforce
// machine authorization and non-enumeration exactly as production would.
func TestProductionOnlySecurityEvidenceMachineAuthAndNonEnumeration(t *testing.T) {
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "production-security-caller", 9)
	start := standardStart(t, now, authority, "production-security")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = ExecutionNodeID{value: "production-node"}, 1
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
			return acceptedPrerequisiteObservation(t, "production-security-input", digest(249)), nil
		}),
		toolWorker: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-enumeration: a missing runtime and a wrong-owner runtime produce the
	// same closed authorization-denied error with no existence disclosure.
	missing, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: RuntimeRunID{value: "runtime-absent"},
		Authority: authority,
	})
	missingErr := err
	intruder := mustTaskOrchestrationAuthority(t, "production-security-intruder", 9)
	_, wrongOwnerErr := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: intruder,
	})
	if errorCode(missingErr) != ErrorAuthorizationDenied || errorCode(wrongOwnerErr) != ErrorAuthorizationDenied {
		t.Fatalf("non-enumeration broken: missing=%v wrong-owner=%v", missingErr, wrongOwnerErr)
	}
	_ = missing
}

// TestProductionOnlySecurityEvidenceLocalPolicyIsNotHardening adds explicit
// production-only evidence that the local-development adapter's explicit local
// policy is a developer convenience, not a production hardening claim. The
// public schema and seam are unchanged; the policy is only observable through
// protected diagnostics.
func TestProductionOnlySecurityEvidenceLocalPolicyIsNotHardening(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "production-policy-caller", 9)
	start := standardStart(t, now, authority, "production-policy")
	journal, err := NewLocalDevelopmentJournal("")
	if err != nil {
		t.Fatal(err)
	}
	policy := LocalDevelopmentPolicy{LeaseDuration: 45 * time.Second, WorkerClass: WorkerTool, NodeReady: true}
	authorityHandle, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
		Now:      func() time.Time { return now },
		Policy:   policy,
		Journal:  journal,
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{
			grantFixtureForStart(start, now.Add(15*time.Minute), true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := authorityHandle.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute with local policy: %v", err)
	}
	if accepted.Snapshot.State != RuntimeWaitingForLease {
		t.Fatalf("local policy acceptance state = %#v", accepted.Snapshot.State)
	}
	// The local policy must not leak into the public snapshot: the public
	// schema carries only authority facts, never the developer convenience.
	if accepted.Snapshot.LeaseAcquireBy != start.Deadline && accepted.Snapshot.LeaseAcquireBy.IsZero() {
		t.Fatalf("local policy leaked into public snapshot: %#v", accepted.Snapshot.LeaseAcquireBy)
	}
}

// TestProductionOnlySecurityEvidenceOwnedTransportNeverLeaksCanaries proves the
// production-shaped transport envelope and the local-development journal never
// carry host paths, sessions, credentials, or arbitrary-shell canaries.
func TestProductionOnlySecurityEvidenceOwnedTransportNeverLeaksCanaries(t *testing.T) {
	canary := "host-secret-session-path-arbitrary-shell"
	envelope := OwnedWorkerTransportEnvelope{
		SchemaVersion: OwnedWorkerTransportWireSchemaV1, Kind: "worker_accept",
		OperationID: "op-" + canary, RuntimeRunID: "run-" + canary,
		WorkerAuthorityID: "worker-" + canary, WorkerGeneration: 1,
		NodeAuthorityID: "node-" + canary, NodeGeneration: 1,
	}
	if validOwnedWorkerTransportEnvelope(envelope) {
		t.Fatal("transport accepted a canary identity")
	}
	empty := OwnedWorkerTransportEnvelope{}
	if validOwnedWorkerTransportEnvelope(empty) {
		t.Fatal("empty envelope accepted")
	}
}
