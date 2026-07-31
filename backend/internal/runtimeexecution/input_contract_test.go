package runtimeexecution

import (
	"testing"
	"time"
)

func TestResolvedImmutableInputManifestFailsClosedAcrossIntegrityScopeAndFreshnessMatrix(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 11, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "input-contract-authority", 7)
	input := standardStart(t, now, authority, "input-contract").StartRuntimeRunInput
	secondIdentity := mustInputIdentity(t, "input-contract-second")
	input.ImmutableInputs = append(input.ImmutableInputs, ImmutableInputBinding{
		Identity: secondIdentity, Digest: digest(121), SizeBytes: 3,
	})
	input.ImmutableInputManifest.InputCount = 2
	input.ImmutableInputManifest.TotalSizeBytes += 3
	start := mustStart(t, input)
	snapshot := capsuleResolutionSnapshot(t, start, now)
	resolution, err := (deterministicCapsuleResolver{}).ResolveExecutionCapsule(
		nil, ExecutionCapsuleResolutionRequest{Start: start, Snapshot: snapshot, Now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateResolvedImmutableInputs(start, snapshot, now, resolution.Inputs); err != nil {
		t.Fatalf("valid input manifest: %v", err)
	}

	tests := []struct {
		name string
		edit func(*ResolvedImmutableInputManifest)
		code CapsuleInputValidationCode
	}{
		{name: "missing entry", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries = value.Entries[:1]
		}, code: CapsuleInputValidationMissing},
		{name: "duplicate entry", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[1] = value.Entries[0]
		}, code: CapsuleInputValidationDuplicate},
		{name: "unexpected substitution", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].Identity = mustInputIdentity(t, "substituted-input")
		}, code: CapsuleInputValidationMissing},
		{name: "corrupt evidence", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].MaterializationEvidenceDigest = Digest{}
		}, code: CapsuleInputValidationIntegrity},
		{name: "object-store locator disguised as read capability", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].ReadCapabilityID = InputReadCapabilityID{value: "s3://private-bucket/object-key"}
		}, code: CapsuleInputValidationIntegrity},
		{name: "materialization identity substitution", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].MaterializedIdentity = secondIdentity
		}, code: CapsuleInputValidationIntegrity},
		{name: "materialization digest substitution", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].MaterializedDigest = digest(122)
		}, code: CapsuleInputValidationIntegrity},
		{name: "cross Workspace", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].AuthorityScope.PersonalWorkspaceID = mustPersonalWorkspaceID(t, "other-workspace")
		}, code: CapsuleInputValidationScope},
		{name: "cross Task", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].AuthorityScope.TaskID = mustTaskID(t, "other-task")
		}, code: CapsuleInputValidationScope},
		{name: "expired capability", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].ExpiresAt = now
		}, code: CapsuleInputValidationStale},
		{name: "capability outlives lease authorization", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].ExpiresAt = snapshot.Lease.AuthorizationExpiresAt.Add(time.Second)
		}, code: CapsuleInputValidationStale},
		{name: "stale lease generation", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].LeaseGeneration++
		}, code: CapsuleInputValidationStale},
		{name: "stale lease fence", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].LeaseFence++
		}, code: CapsuleInputValidationStale},
		{name: "stale authorization generation", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].AuthorizationGeneration++
		}, code: CapsuleInputValidationStale},
		{name: "missing materialization generation", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].MaterializationGeneration = 0
		}, code: CapsuleInputValidationIntegrity},
		{name: "host path", edit: func(value *ResolvedImmutableInputManifest) {
			value.Entries[0].LogicalLocation = "/host/private/input"
		}, code: CapsuleInputValidationPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := resolution.Inputs
			manifest.Entries = append([]ResolvedImmutableInput(nil), resolution.Inputs.Entries...)
			test.edit(&manifest)
			_, err := validateResolvedImmutableInputs(start, snapshot, now, manifest)
			failure, ok := err.(*CapsuleInputValidationError)
			if !ok || failure.Code() != test.code {
				t.Fatalf("error=%T %v, want code %v", err, err, test.code)
			}
		})
	}
}

func capsuleResolutionSnapshot(t *testing.T, start StartRuntimeRun, now time.Time) RuntimeSnapshot {
	t.Helper()
	lease := RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, LeaseID: SandboxLeaseID{value: "input-contract-lease"},
		Generation: 3, Fence: 4, Disposition: LeaseActive, ExpiresAt: now.Add(10 * time.Minute),
		AuthorizationGeneration: 5, AuthorizationExpiresAt: now.Add(8 * time.Minute),
	}
	return RuntimeSnapshot{
		SchemaVersion: SnapshotSchemaCurrent, RuntimeRunID: start.RuntimeRunID,
		RuntimeFence: start.ExpectedRuntimeFence, Deadline: start.Deadline, Lease: lease,
		Node: RuntimeNodeSnapshot{
			ExecutionNodeID: startNodeID(t, "input-contract-node"), Generation: 2,
			Readiness: NodeReady, AttestationID: mustNodeAttestationID(t, "input-contract-attestation"),
			AttestationGeneration: 2, ExpiresAt: now.Add(10 * time.Minute),
		},
	}
}
