package runtimeexecution

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCanonicalStartEncodingIsVersionedDeterministicAndExact(t *testing.T) {
	t.Parallel()

	authority := mustTaskOrchestrationAuthority(t, "auth-canonical", 3)
	deadline := time.Date(2026, 7, 28, 12, 34, 56, 123400000, time.FixedZone("plus-eight", 8*60*60))
	input := StartRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "op-canonical"),
		PersonalWorkspaceID: mustPersonalWorkspaceID(t, "workspace-canonical"),
		TaskID:              mustTaskID(t, "task-canonical"), PhaseRunID: mustPhaseRunID(t, "phase-canonical"),
		RuntimeRunID: mustRuntimeRunID(t, "runtime-canonical"), Attempt: 2,
		ExpectedTaskRevision: 15, ExpectedRuntimeRevision: 8, ExpectedOperationGeneration: 9, ExpectedRuntimeFence: 10,
		Authority: authority, RuntimeBindingID: mustRuntimeBindingID(t, "binding-canonical"),
		RuntimeBindingDigest: digest(1), ExecutionLockDigest: digest(2),
		CapabilityContractDigest: digest(8), AllowedPlatformImagesDigest: digest(9), ExecutorContractDigest: digest(10),
		ReleaseSafetyEpoch: 22,
		WorkerClass:        WorkerAgent, Effect: EffectReadOnly,
		ImmutableInputs: []ImmutableInputBinding{
			{Identity: mustInputIdentity(t, "input-b"), Digest: digest(4), SizeBytes: 20},
			{Identity: mustInputIdentity(t, "input-a"), Digest: digest(3), SizeBytes: 10},
		},
		OutputContractDigest: digest(5), EvidenceContractDigest: digest(6),
		ResourceClassID:    mustResourceClassID(t, "class-canonical"),
		ExecutionPolicyID:  mustExecutionPolicyID(t, "policy-canonical"),
		ProviderCapability: ProviderCapabilityNone,
		NetworkPolicyID:    mustNetworkPolicyID(t, "network-policy-canonical"),
		SecretPolicyID:     mustSecretPolicyID(t, "secret-policy-canonical"),
		Deadline:           deadline, CancellationPolicy: CancellationFenceFirst,
		AdmissionGrant: AdmissionGrantProof{
			AdmissionGrantID: mustAdmissionGrantID(t, "grant-canonical"),
			WorkItemID:       mustWorkItemID(t, "work-canonical"), Generation: 7,
		},
	}
	command := mustStart(t, input)

	digestHex := func(last string) string { return strings.Repeat("0", 62) + last }
	expectedJSON := fmt.Sprintf(
		`{"schema":{"major":1,"minor":0},"kind":"start_runtime_run","operation_id":"op-canonical","personal_workspace_id":"workspace-canonical","task_id":"task-canonical","phase_run_id":"phase-canonical","runtime_run_id":"runtime-canonical","attempt":2,"expected_task_revision":15,"expected_runtime_revision":8,"expected_operation_generation":9,"expected_runtime_fence":10,"authority":{"kind":"task_orchestration","id":"auth-canonical","generation":3},"runtime_binding_id":"binding-canonical","runtime_binding_digest":"%s","execution_lock_digest":"%s","capability_contract_digest":"%s","allowed_platform_images_digest":"%s","executor_contract_digest":"%s","release_safety_epoch":22,"catalog_binding":null,"worker_class":"agent","effect":"read_only","immutable_inputs":[{"identity":"input-a","digest":"%s","size_bytes":10},{"identity":"input-b","digest":"%s","size_bytes":20}],"output_contract_digest":"%s","evidence_contract_digest":"%s","runtime_view_requirement":null,"resource_class_id":"class-canonical","execution_policy_id":"policy-canonical","provider_capability":"none","provider_binding":null,"network_policy_id":"network-policy-canonical","secret_policy_id":"secret-policy-canonical","deadline":"2026-07-28T04:34:56.123400000Z","cancellation_policy":"fence_first"}`,
		digestHex("01"), digestHex("02"), digestHex("08"), digestHex("09"), digestHex("0a"),
		digestHex("03"), digestHex("04"), digestHex("05"), digestHex("06"),
	)
	expected := Digest(sha256.Sum256(append([]byte("slidesmith.runtime-execution.request/v1\n"), []byte(expectedJSON)...)))
	if command.CanonicalRequestDigest != expected {
		actualJSON, encodingErr := canonicalStartEncoding(command)
		t.Fatalf("canonical digest = %s, want %s\nactual: %s\nexpected: %s\nencoding error: %v", command.CanonicalRequestDigest.String(), expected.String(), actualJSON, expectedJSON, encodingErr)
	}

	reordered := input
	reordered.Deadline = deadline.UTC()
	reordered.ImmutableInputs = []ImmutableInputBinding{input.ImmutableInputs[1], input.ImmutableInputs[0]}
	reorderedCommand := mustStart(t, reordered)
	if reorderedCommand.CanonicalRequestDigest != command.CanonicalRequestDigest {
		t.Fatal("timezone or set order changed canonical digest")
	}

	updatedGrant := input
	updatedGrant.AdmissionGrant = AdmissionGrantProof{
		AdmissionGrantID: mustAdmissionGrantID(t, "grant-canonical-new"),
		WorkItemID:       input.AdmissionGrant.WorkItemID, Generation: 8,
	}
	if mustStart(t, updatedGrant).CanonicalRequestDigest != command.CanonicalRequestDigest {
		t.Fatal("replaceable grant identity or generation entered canonical start")
	}

	traced := input
	traced.Trace.TraceID[len(traced.Trace.TraceID)-1] = 9
	if mustStart(t, traced).CanonicalRequestDigest != command.CanonicalRequestDigest {
		t.Fatal("non-authoritative TraceID entered canonical start")
	}

	mutating := input
	mutating.Effect = EffectMutating
	mutating.RuntimeViewRequirement = &RuntimeViewRequirement{
		TaskWorkspaceID:         mustTaskWorkspaceID(t, "task-workspace-canonical"),
		MaterializationID:       mustTaskWorkspaceMaterializationID(t, "materialization-canonical"),
		BaseRevisionID:          mustTaskWorkspaceRevisionID(t, "workspace-revision-canonical"),
		LifecycleGeneration:     4,
		LifecycleFence:          5,
		ExpiryPolicy:            RuntimeViewExpiryAtDeadline,
		OpenOperationDerivation: digest(7),
	}
	if mustStart(t, mutating).CanonicalRequestDigest == command.CanonicalRequestDigest {
		t.Fatal("typed Runtime View requirement did not enter canonical start")
	}

	unknownWorker := input
	unknownWorker.WorkerClass = WorkerClass(255)
	if _, err := NewStartRuntimeRun(unknownWorker); err == nil {
		t.Fatal("unknown required worker variant did not fail closed")
	}
}

func TestCanonicalStartBindsCompleteExecutionAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 7, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "complete-authority-caller", 3)
	input := standardStart(t, now, authority, "complete-authority").StartRuntimeRunInput
	input.CapabilityContractDigest = digest(40)
	input.AllowedPlatformImagesDigest = digest(41)
	input.ExecutorContractDigest = digest(42)
	input.ReleaseSafetyEpoch = 23
	input.CatalogBinding = &CatalogExecutionBinding{
		TemplateLockID:     mustTemplateLockID(t, "template-lock-complete-authority"),
		TemplateLockDigest: digest(43),
		ClosureRootDigest:  digest(44),
		SafetyEpoch:        24,
	}
	input.ProviderCapability = ProviderCapabilityRequired
	input.ProviderBinding = &ProviderExecutionBinding{
		QuotaReservationID:   mustQuotaReservationID(t, "quota-complete-authority"),
		Generation:           5,
		Mode:                 QuotaReservationObservation,
		GatewayRoutePolicyID: mustGatewayRoutePolicyID(t, "gateway-policy-complete-authority"),
	}
	input.NetworkPolicyID = mustNetworkPolicyID(t, "network-policy-complete-authority")
	input.SecretPolicyID = mustSecretPolicyID(t, "secret-policy-complete-authority")
	command := mustStart(t, input)

	tests := []struct {
		name   string
		mutate func(*StartRuntimeRunInput)
	}{
		{name: "capability contract", mutate: func(changed *StartRuntimeRunInput) { changed.CapabilityContractDigest = digest(50) }},
		{name: "allowed platform images", mutate: func(changed *StartRuntimeRunInput) { changed.AllowedPlatformImagesDigest = digest(51) }},
		{name: "executor contract", mutate: func(changed *StartRuntimeRunInput) { changed.ExecutorContractDigest = digest(52) }},
		{name: "release safety epoch", mutate: func(changed *StartRuntimeRunInput) { changed.ReleaseSafetyEpoch++ }},
		{name: "catalog binding", mutate: func(changed *StartRuntimeRunInput) { changed.CatalogBinding.ClosureRootDigest = digest(53) }},
		{name: "provider binding", mutate: func(changed *StartRuntimeRunInput) { changed.ProviderBinding.Generation++ }},
		{name: "network policy", mutate: func(changed *StartRuntimeRunInput) {
			changed.NetworkPolicyID = mustNetworkPolicyID(t, "network-policy-changed")
		}},
		{name: "secret policy", mutate: func(changed *StartRuntimeRunInput) {
			changed.SecretPolicyID = mustSecretPolicyID(t, "secret-policy-changed")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := input
			catalog := *input.CatalogBinding
			provider := *input.ProviderBinding
			changed.CatalogBinding = &catalog
			changed.ProviderBinding = &provider
			test.mutate(&changed)
			if mustStart(t, changed).CanonicalRequestDigest == command.CanonicalRequestDigest {
				t.Fatalf("%s did not enter canonical start", test.name)
			}
		})
	}
}

func mustTaskWorkspaceID(t *testing.T, value string) TaskWorkspaceID {
	t.Helper()
	id, err := NewTaskWorkspaceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTaskWorkspaceRevisionID(t *testing.T, value string) TaskWorkspaceRevisionID {
	t.Helper()
	id, err := NewTaskWorkspaceRevisionID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTaskWorkspaceMaterializationID(t *testing.T, value string) TaskWorkspaceMaterializationID {
	t.Helper()
	id, err := NewTaskWorkspaceMaterializationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
