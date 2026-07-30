package runtimeexecution

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExactStartReplayAfterCancelReturnsOriginalFactAndCurrentSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "runtime-caller", 3)
	start := standardStart(t, now, authority, "replay")
	harness := harnessForStart(t, now, authority, start)

	original, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute start: %v", err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion:               SchemaV1,
		OperationID:                 mustOperationID(t, "cancel-replay"),
		PersonalWorkspaceID:         start.PersonalWorkspaceID,
		TaskID:                      start.TaskID,
		PhaseRunID:                  start.PhaseRunID,
		RuntimeRunID:                start.RuntimeRunID,
		ExpectedRuntimeRevision:     original.Snapshot.RuntimeRevision,
		ExpectedStartOperationID:    start.OperationID,
		ExpectedOperationGeneration: original.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        original.Snapshot.RuntimeFence,
		Authority:                   authority,
		Reason:                      CancellationUserRequested,
		SafetyEpoch:                 start.ReleaseSafetyEpoch,
		OccurredAt:                  now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("new cancel: %v", err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatalf("execute cancel: %v", err)
	}
	if cancelled.Fact.Disposition != DecisionAccepted || cancelled.Snapshot.Outcome != RuntimeCancelled {
		t.Fatalf("cancel did not terminalize before a lease: %#v", cancelled)
	}
	if cancelled.Snapshot.Capacity.LogicalRelease != LogicalCapacityReleaseReady ||
		cancelled.Snapshot.Capacity.NoLease != NoLeaseDispositionRecorded ||
		cancelled.Snapshot.Capacity.Physical != PhysicalCapacityNotApplicable {
		t.Fatalf("cancel capacity facts were conflated: %#v", cancelled.Snapshot.Capacity)
	}

	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("replay start: %v", err)
	}
	if replayed.Fact != original.Fact {
		t.Fatalf("replay changed original fact\noriginal: %#v\nreplay: %#v", original.Fact, replayed.Fact)
	}
	if replayed.Snapshot != cancelled.Snapshot {
		t.Fatalf("replay did not return current snapshot\ncancel: %#v\nreplay: %#v", cancelled.Snapshot, replayed.Snapshot)
	}
	if replayed.Fact.StateAtDecision != RuntimeWaitingForLease || replayed.Fact.OutcomeAtDecision != RuntimeOutcomeNone {
		t.Fatalf("original decision now implies synchronous completion: %#v", replayed.Fact)
	}

	conflicting := start
	conflicting.Deadline = conflicting.Deadline.Add(time.Minute)
	conflicting.CanonicalRequestDigest, err = computeStartDigest(conflicting)
	if err != nil {
		t.Fatalf("canonicalize conflict: %v", err)
	}
	_, err = harness.Runtime.Execute(context.Background(), conflicting)
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code() != ErrorIntegrityConflict {
		t.Fatalf("same operation/different payload error = %v", err)
	}

	replayedAgain, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayedAgain.Fact != original.Fact || replayedAgain.Snapshot != cancelled.Snapshot {
		t.Fatalf("conflict altered original binding: decision=%#v err=%v", replayedAgain, err)
	}
}

func TestStaleCancelReplayAfterLaterRevisionReturnsOriginalRejectionAndCurrentSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 1, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "stale-cancel-caller", 3)
	start := standardStart(t, now, authority, "stale-cancel")
	harness := harnessForStart(t, now, authority, start)
	accepted := executeDecision(t, harness, start)

	stale, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "cancel-stale-binding"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision:  start.ExpectedRuntimeRevision,
		ExpectedStartOperationID: start.OperationID, ExpectedOperationGeneration: accepted.Snapshot.Operation.Generation,
		ExpectedRuntimeFence: accepted.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleDecision := executeDecision(t, harness, stale)
	if staleDecision.Fact.Disposition != DecisionRejected || staleDecision.Fact.Rejection != CommandRejectionStaleRevision ||
		staleDecision.Snapshot.RuntimeRevision != accepted.Snapshot.RuntimeRevision {
		t.Fatalf("stale cancel changed Runtime: %#v", staleDecision)
	}

	validInput := stale.CancelRuntimeRunInput
	validInput.OperationID = mustOperationID(t, "cancel-current-binding")
	validInput.ExpectedRuntimeRevision = accepted.Snapshot.RuntimeRevision
	valid, err := NewCancelRuntimeRun(validInput)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := executeDecision(t, harness, valid)
	replayed := executeDecision(t, harness, stale)
	if replayed.Fact != staleDecision.Fact || replayed.Snapshot != cancelled.Snapshot {
		t.Fatalf("stale replay = %#v, want fact=%#v snapshot=%#v", replayed, staleDecision.Fact, cancelled.Snapshot)
	}
}

func standardStart(t *testing.T, now time.Time, authority RuntimeAuthority, suffix string) StartRuntimeRun {
	t.Helper()
	return mustStart(t, StartRuntimeRunInput{
		SchemaVersion:               SchemaV1,
		OperationID:                 mustOperationID(t, "start-"+suffix),
		PersonalWorkspaceID:         mustPersonalWorkspaceID(t, "workspace-"+suffix),
		TaskID:                      mustTaskID(t, "task-"+suffix),
		PhaseRunID:                  mustPhaseRunID(t, "phase-"+suffix),
		RuntimeRunID:                mustRuntimeRunID(t, "runtime-"+suffix),
		Attempt:                     1,
		ExpectedTaskRevision:        10,
		ExpectedRuntimeRevision:     2,
		ExpectedOperationGeneration: 3,
		ExpectedRuntimeFence:        4,
		Authority:                   authority,
		RuntimeBindingID:            mustRuntimeBindingID(t, "binding-"+suffix),
		RuntimeBindingDigest:        digest(10),
		ExecutionLockDigest:         digest(11),
		CapabilityContractDigest:    digest(15),
		AllowedPlatformImagesDigest: digest(16),
		ExecutorContractDigest:      digest(17),
		ReleaseSafetyEpoch:          9,
		WorkerClass:                 WorkerTool,
		Effect:                      EffectReadOnly,
		ImmutableInputManifest: ImmutableInputManifestBinding{
			Identity: mustInputManifestIdentity(t, "input-manifest-"+suffix), SchemaVersion: SchemaV1,
			Digest: digest(18), TotalSizeBytes: 17, InputCount: 1,
			MaterializationEvidenceID:     mustEvidenceID(t, "input-materialization-"+suffix),
			MaterializationEvidenceDigest: digest(19),
		},
		ImmutableInputs:        []ImmutableInputBinding{{Identity: mustInputIdentity(t, "input-"+suffix), Digest: digest(12), SizeBytes: 17}},
		OutputContractDigest:   digest(13),
		EvidenceContractDigest: digest(14),
		ResourceClassID:        mustResourceClassID(t, "class-"+suffix),
		ExecutionPolicyID:      mustExecutionPolicyID(t, "policy-"+suffix),
		ProviderCapability:     ProviderCapabilityNone,
		NetworkPolicyID:        mustNetworkPolicyID(t, "network-"+suffix),
		SecretPolicyID:         mustSecretPolicyID(t, "secret-"+suffix),
		Deadline:               now.Add(20 * time.Minute),
		CancellationPolicy:     CancellationFenceFirst,
		AdmissionGrant: AdmissionGrantProof{
			AdmissionGrantID: mustAdmissionGrantID(t, "grant-"+suffix),
			WorkItemID:       mustWorkItemID(t, "work-"+suffix),
			Generation:       2,
		},
	})
}

func harnessForStart(t *testing.T, now time.Time, authority RuntimeAuthority, start StartRuntimeRun) *DeterministicHarness {
	t.Helper()
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now,
		IDs: DeterministicIDConfig{DecisionStart: 1},
		Runtimes: []RuntimeFixture{{
			PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
			PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: authority,
			TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision,
			OperationGeneration: start.ExpectedOperationGeneration, RuntimeFence: start.ExpectedRuntimeFence,
			SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeCreated,
		}},
		AdmissionGrants: []AdmissionGrantFixture{{
			AdmissionGrantID: start.AdmissionGrant.AdmissionGrantID, WorkItemID: start.AdmissionGrant.WorkItemID,
			Generation: start.AdmissionGrant.Generation, PersonalWorkspaceID: start.PersonalWorkspaceID,
			RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
			CanonicalStartDigest: start.CanonicalRequestDigest, ExpiresAt: now.Add(15 * time.Minute), Current: true,
		}},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	return harness
}
