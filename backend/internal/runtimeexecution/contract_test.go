package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

func TestExecuteAcceptedCanonicalStartAndInspectCurrentSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 8, 0, 0, 123456789, time.FixedZone("CST", 8*60*60))
	authority := mustTaskOrchestrationAuthority(t, "task-orchestration", 7)
	start := mustStart(t, StartRuntimeRunInput{
		SchemaVersion:               SchemaV1,
		OperationID:                 mustOperationID(t, "start-operation"),
		PersonalWorkspaceID:         mustPersonalWorkspaceID(t, "workspace-1"),
		TaskID:                      mustTaskID(t, "task-1"),
		PhaseRunID:                  mustPhaseRunID(t, "phase-run-1"),
		RuntimeRunID:                mustRuntimeRunID(t, "runtime-run-1"),
		Attempt:                     1,
		ExpectedTaskRevision:        11,
		ExpectedRuntimeRevision:     3,
		ExpectedOperationGeneration: 4,
		ExpectedRuntimeFence:        5,
		Authority:                   authority,
		RuntimeBindingID:            mustRuntimeBindingID(t, "runtime-binding-1"),
		RuntimeBindingDigest:        digest(1),
		ExecutionLockDigest:         digest(2),
		CapabilityContractDigest:    digest(7),
		AllowedPlatformImagesDigest: digest(8),
		ExecutorContractDigest:      digest(9),
		ReleaseSafetyEpoch:          13,
		WorkerClass:                 WorkerAgent,
		Effect:                      EffectReadOnly,
		ImmutableInputs: []ImmutableInputBinding{
			{Identity: mustInputIdentity(t, "input-b"), Digest: digest(4), SizeBytes: 20},
			{Identity: mustInputIdentity(t, "input-a"), Digest: digest(3), SizeBytes: 10},
		},
		OutputContractDigest:   digest(5),
		EvidenceContractDigest: digest(6),
		ResourceClassID:        mustResourceClassID(t, "resource-class-1"),
		ExecutionPolicyID:      mustExecutionPolicyID(t, "execution-policy-1"),
		ProviderCapability:     ProviderCapabilityNone,
		NetworkPolicyID:        mustNetworkPolicyID(t, "network-policy-1"),
		SecretPolicyID:         mustSecretPolicyID(t, "secret-policy-1"),
		Deadline:               now.Add(30 * time.Minute),
		CancellationPolicy:     CancellationFenceFirst,
		AdmissionGrant: AdmissionGrantProof{
			AdmissionGrantID: mustAdmissionGrantID(t, "grant-1"),
			WorkItemID:       mustWorkItemID(t, "work-item-1"),
			Generation:       2,
		},
	})

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now,
		IDs: DeterministicIDConfig{DecisionStart: 40},
		Runtimes: []RuntimeFixture{{
			PersonalWorkspaceID: start.PersonalWorkspaceID,
			TaskID:              start.TaskID,
			PhaseRunID:          start.PhaseRunID,
			RuntimeRunID:        start.RuntimeRunID,
			Owner:               authority,
			TaskRevision:        11,
			RuntimeRevision:     3,
			OperationGeneration: 4,
			RuntimeFence:        5,
			SafetyEpoch:         13,
			State:               RuntimeCreated,
		}},
		AdmissionGrants: []AdmissionGrantFixture{{
			AdmissionGrantID:     start.AdmissionGrant.AdmissionGrantID,
			WorkItemID:           start.AdmissionGrant.WorkItemID,
			Generation:           start.AdmissionGrant.Generation,
			PersonalWorkspaceID:  start.PersonalWorkspaceID,
			RuntimeRunID:         start.RuntimeRunID,
			OperationID:          start.OperationID,
			CanonicalStartDigest: start.CanonicalRequestDigest,
			ExpiresAt:            now.Add(10 * time.Minute),
			Current:              true,
		}},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute start: %v", err)
	}
	if decision.Fact.Disposition != DecisionAccepted || decision.Fact.DecisionID.String() != "runtime-decision-000040" {
		t.Fatalf("unexpected acceptance: %#v", decision.Fact)
	}
	if decision.Fact.PreviousRuntimeRevision != 3 || decision.Fact.ResultingRuntimeRevision != 4 {
		t.Fatalf("unexpected revision transition: %#v", decision.Fact)
	}
	if decision.Snapshot.State != RuntimeWaitingForLease || decision.Snapshot.Outcome != RuntimeOutcomeNone {
		t.Fatalf("acceptance implied completion: %#v", decision.Snapshot)
	}
	if decision.Snapshot.Operation.Generation != 5 || decision.Snapshot.RuntimeFence != 6 {
		t.Fatalf("operation and Runtime fence were not independently advanced: %#v", decision.Snapshot)
	}
	if !decision.Snapshot.LeaseAcquireBy.Equal(now.Add(10 * time.Minute).UTC()) {
		t.Fatalf("LeaseAcquireBy = %v", decision.Snapshot.LeaseAcquireBy)
	}

	snapshot, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion:       SchemaV1,
		ProjectionVersion:   SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID:        start.RuntimeRunID,
		Authority:           authority,
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if snapshot != decision.Snapshot {
		t.Fatalf("inspect snapshot differs from Execute snapshot\nexecute: %#v\ninspect: %#v", decision.Snapshot, snapshot)
	}
}

func mustStart(t *testing.T, input StartRuntimeRunInput) StartRuntimeRun {
	t.Helper()
	command, err := NewStartRuntimeRun(input)
	if err != nil {
		t.Fatalf("new start: %v", err)
	}
	return command
}

func mustTaskOrchestrationAuthority(t *testing.T, value string, generation AuthorizationGeneration) RuntimeAuthority {
	t.Helper()
	id, err := NewAuthorityID(value)
	if err != nil {
		t.Fatal(err)
	}
	return NewTaskOrchestrationAuthority(id, generation)
}

func mustOperationID(t *testing.T, value string) OperationID {
	t.Helper()
	id, err := NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustPersonalWorkspaceID(t *testing.T, value string) PersonalWorkspaceID {
	t.Helper()
	id, err := NewPersonalWorkspaceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTaskID(t *testing.T, value string) TaskID {
	t.Helper()
	id, err := NewTaskID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustPhaseRunID(t *testing.T, value string) PhaseRunID {
	t.Helper()
	id, err := NewPhaseRunID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustRuntimeRunID(t *testing.T, value string) RuntimeRunID {
	t.Helper()
	id, err := NewRuntimeRunID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustRuntimeBindingID(t *testing.T, value string) RuntimeBindingID {
	t.Helper()
	id, err := NewRuntimeBindingID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustInputIdentity(t *testing.T, value string) ImmutableInputIdentity {
	t.Helper()
	id, err := NewImmutableInputIdentity(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustResourceClassID(t *testing.T, value string) ResourceClassID {
	t.Helper()
	id, err := NewResourceClassID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustExecutionPolicyID(t *testing.T, value string) ExecutionPolicyID {
	t.Helper()
	id, err := NewExecutionPolicyID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustAdmissionGrantID(t *testing.T, value string) AdmissionGrantID {
	t.Helper()
	id, err := NewAdmissionGrantID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustWorkItemID(t *testing.T, value string) WorkItemID {
	t.Helper()
	id, err := NewWorkItemID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustEvidenceID(t *testing.T, value string) EvidenceID {
	t.Helper()
	id, err := NewEvidenceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTemplateLockID(t *testing.T, value string) TemplateLockID {
	t.Helper()
	id, err := NewTemplateLockID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustQuotaReservationID(t *testing.T, value string) QuotaReservationID {
	t.Helper()
	id, err := NewQuotaReservationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustGatewayRoutePolicyID(t *testing.T, value string) GatewayRoutePolicyID {
	t.Helper()
	id, err := NewGatewayRoutePolicyID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustNetworkPolicyID(t *testing.T, value string) NetworkPolicyID {
	t.Helper()
	id, err := NewNetworkPolicyID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustSecretPolicyID(t *testing.T, value string) SecretPolicyID {
	t.Helper()
	id, err := NewSecretPolicyID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func digest(last byte) Digest {
	var value Digest
	value[len(value)-1] = last
	return value
}
