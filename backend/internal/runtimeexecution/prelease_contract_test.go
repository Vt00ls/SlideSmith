package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestPostgresPreLeaseTerminalDigestBindsExactCanonicalBytes(t *testing.T) {
	t.Parallel()

	lease := RuntimeLeaseSnapshot{
		AcquireOperationID: mustOperationID(t, "terminal-canonical-lease-acquire"),
		AcquireDigest:      digest(77),
	}
	operationID, got, canonical := stablePostgresPreLeaseTerminalBinding(
		lease, RuntimeRejected, PreLeaseTerminalImmutableBinding,
	)
	want := Digest(sha256.Sum256(canonical))
	if operationID.String() == "" || got != want {
		t.Fatalf("terminal binding digest does not cover exact canonical bytes: operation=%s got=%s want=%s canonical=%q",
			operationID.String(), got.String(), want.String(), canonical)
	}
}

func TestDeterministicPostBindPreLeaseObservationMatrixUsesOneStableLeaseOperation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		observation   LeaseAcquisitionObservation
		wantState     RuntimeState
		wantOutcome   RuntimeOutcome
		wantReason    PreLeaseTerminalReason
		wantReconcile ReconciliationStatus
		wantNoLease   bool
	}{
		{name: "temporary same-node-generation unavailable", observation: LeaseAcquisitionObservation{Disposition: LeaseAcquisitionTemporaryUnavailable}, wantState: RuntimeWaitingForLease, wantReconcile: ReconciliationStable},
		{name: "retryable Reservation", observation: LeaseAcquisitionObservation{Disposition: LeaseAcquisitionRetryablePrerequisite}, wantState: RuntimeReconciling, wantReconcile: ReconciliationRequiredStatus},
		{name: "ambiguous authorization", observation: LeaseAcquisitionObservation{Disposition: LeaseAcquisitionAmbiguousPrerequisite}, wantState: RuntimeReconciling, wantReconcile: ReconciliationRequiredStatus},
		{name: "stale node generation", observation: permanentPreLeaseObservation(PreLeasePermanentStaleNodeGeneration), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalStaleNodeGeneration, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "ineligible node", observation: permanentPreLeaseObservation(PreLeasePermanentNodeIneligible), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalNodeIneligible, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "Reservation failure", observation: permanentPreLeaseObservation(PreLeasePermanentReservation), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalReservation, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "authorization failure", observation: permanentPreLeaseObservation(PreLeasePermanentAuthorization), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalAuthorization, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "Resource Class failure", observation: permanentPreLeaseObservation(PreLeasePermanentResourceClass), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalResourceClass, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "Execution Policy failure", observation: permanentPreLeaseObservation(PreLeasePermanentExecutionPolicy), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalExecutionPolicy, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "Scheduler policy failure", observation: permanentPreLeaseObservation(PreLeasePermanentSchedulerPolicy), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalSchedulerPolicy, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "Scheduler epoch failure", observation: permanentPreLeaseObservation(PreLeasePermanentSchedulerEpoch), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalSchedulerEpoch, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "release safety failure", observation: permanentPreLeaseObservation(PreLeasePermanentReleaseSafety), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalReleaseSafety, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "catalog safety failure", observation: permanentPreLeaseObservation(PreLeasePermanentCatalogSafety), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalCatalogSafety, wantReconcile: ReconciliationStable, wantNoLease: true},
		{name: "immutable binding failure", observation: permanentPreLeaseObservation(PreLeasePermanentImmutableBinding), wantState: RuntimeTerminal, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalImmutableBinding, wantReconcile: ReconciliationStable, wantNoLease: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			authority := mustTaskOrchestrationAuthority(t, "prelease-authority-"+safeTestID(testCase.name), 2)
			start := standardStart(t, now, authority, "prelease-"+safeTestID(testCase.name))
			adapter := &recordingLeaseAcquisitionAdapter{observation: testCase.observation}
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now: now, Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
				AdmissionGrants:  []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(10*time.Minute), true)},
				LeaseAcquisition: adapter,
			})
			if err != nil {
				t.Fatal(err)
			}

			decision, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatalf("execute Start: %v", err)
			}
			if adapter.calls != 1 || adapter.requests[0].OperationID.String() == "" ||
				adapter.requests[0].OperationID != decision.Snapshot.Lease.AcquireOperationID ||
				adapter.requests[0].Digest != decision.Snapshot.Lease.AcquireDigest {
				t.Fatalf("pre-lease adapter did not receive exact stable operation: requests=%+v snapshot=%+v", adapter.requests, decision.Snapshot)
			}
			if decision.Snapshot.State != testCase.wantState || decision.Snapshot.Outcome != testCase.wantOutcome ||
				decision.Snapshot.PreLeaseTerminalReason != testCase.wantReason ||
				decision.Snapshot.Reconciliation != testCase.wantReconcile {
				t.Fatalf("pre-lease outcome = %+v", decision.Snapshot)
			}
			if testCase.wantNoLease {
				assertProvenNoLeaseTerminal(t, decision.Snapshot)
			} else if decision.Snapshot.CapacityEvidence != (RuntimeCapacityEvidenceSnapshot{}) ||
				decision.Snapshot.Capacity.LogicalRelease != LogicalCapacityHeld ||
				decision.Snapshot.Capacity.NoLease != NoLeaseDispositionNone {
				t.Fatalf("non-terminal observation released capacity: %+v", decision.Snapshot)
			}
			stableOperation := decision.Snapshot.Lease.AcquireOperationID
			stableDigest := decision.Snapshot.Lease.AcquireDigest
			replayed, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil || replayed.Fact != decision.Fact ||
				replayed.Snapshot.Lease.AcquireOperationID != stableOperation ||
				replayed.Snapshot.Lease.AcquireDigest != stableDigest {
				t.Fatalf("exact replay changed lease operation: replay=%+v err=%v", replayed, err)
			}
		})
	}
}

func TestDeterministicPreLeaseAuthorityExpiryAndRuntimeDeadlineAreDistinct(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 10, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name        string
		grantExpiry time.Time
		advance     time.Duration
		wantOutcome RuntimeOutcome
		wantReason  PreLeaseTerminalReason
	}{
		{name: "bound authority expiry", grantExpiry: now.Add(5 * time.Minute), advance: 5 * time.Minute, wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalAdmissionAuthorityExpired},
		{name: "Runtime deadline", grantExpiry: now.Add(30 * time.Minute), advance: 20 * time.Minute, wantOutcome: RuntimeTimedOut, wantReason: PreLeaseTerminalRuntimeDeadline},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			authority := mustTaskOrchestrationAuthority(t, "clock-authority-"+safeTestID(testCase.name), 3)
			start := standardStart(t, now, authority, "clock-"+safeTestID(testCase.name))
			harness := harnessForStartWithGrantExpiry(t, now, authority, start, testCase.grantExpiry)
			accepted := executeDecision(t, harness, start)
			if accepted.Snapshot.State != RuntimeWaitingForLease {
				t.Fatalf("fresh Start did not wait for lease: %+v", accepted.Snapshot)
			}
			if err := harness.AdvanceClock(testCase.advance); err != nil {
				t.Fatal(err)
			}
			replayed, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil || replayed.Fact != accepted.Fact || replayed.Snapshot.Outcome != testCase.wantOutcome ||
				replayed.Snapshot.PreLeaseTerminalReason != testCase.wantReason {
				t.Fatalf("clock terminal = %+v err=%v", replayed, err)
			}
			assertProvenNoLeaseTerminal(t, replayed.Snapshot)
		})
	}
}

func TestDeterministicLeaseCommitResponseLossReplaysExactlyOneLease(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 11, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "lease-commit-authority", 4)
	start := standardStart(t, now, authority, "lease-commit")
	adapter := &recordingLeaseAcquisitionAdapter{observation: LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}}
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "execution-node-"+grant.AdmissionGrantID.String())
	grant.NodeCapacityGeneration = 1
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 300, LeaseStart: 700, SandboxStart: 800},
		Runtimes:         []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants:  []AdmissionGrantFixture{grant},
		Nodes:            []ExecutionNodeFixture{executionNodeFixtureForStart(t, start, grant, now)},
		LeaseAcquisition: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.FailNextAt(FaultAfterLeaseCommit); err != nil {
		t.Fatal(err)
	}
	_, err = harness.Runtime.Execute(context.Background(), start)
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code() != ErrorReconciliationRequired {
		t.Fatalf("post-lease-commit response loss = %T %v", err, err)
	}
	restarted := harness.Restart()
	replayed, err := restarted.Runtime.Execute(context.Background(), start)
	if err != nil || replayed.Snapshot.State != RuntimePreparingPrerequisites ||
		replayed.Snapshot.Lease.AcquireStatus != LeaseGranted ||
		replayed.Snapshot.Lease.LeaseID.String() != "sandbox-lease-000700" ||
		replayed.Snapshot.Lease.Generation != 1 || replayed.Snapshot.Lease.Fence != 1 ||
		replayed.Snapshot.Capacity.Physical != PhysicalCapacityOccupied ||
		replayed.Snapshot.CapacityEvidence != (RuntimeCapacityEvidenceSnapshot{}) {
		t.Fatalf("lease replay = %+v err=%v", replayed, err)
	}
	if adapter.calls != 1 {
		t.Fatalf("replay attempted a second physical acquisition: calls=%d", adapter.calls)
	}
}

func executionNodeFixtureForStart(
	t *testing.T,
	start StartRuntimeRun,
	grant AdmissionGrantFixture,
	now time.Time,
) ExecutionNodeFixture {
	t.Helper()
	return ExecutionNodeFixture{
		ExecutionNodeID: grant.ExecutionNodeID, Generation: NodeGeneration(grant.NodeCapacityGeneration),
		Readiness: NodeReady, AttestationID: startNodeAttestationID(t, "attestation-"+start.RuntimeRunID.String()),
		AttestationGeneration: 1, AttestedAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute),
		ResourceClassID: start.ResourceClassID, ExecutionPolicyID: start.ExecutionPolicyID,
		NodeAuthorityID:   startNodeAuthorityID(t, "node-authority-"+start.RuntimeRunID.String()),
		WorkerAuthorityID: startWorkerAuthorityID(t, "worker-authority-"+start.RuntimeRunID.String()),
		WorkerGeneration:  1, AuthorizationGeneration: start.Authority.generation,
		AuthorizationExpiresAt: now.Add(10 * time.Minute), ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		Containment: ContainmentEstablished, Reset: ResetCompleted,
	}
}

type recordingLeaseAcquisitionAdapter struct {
	observation LeaseAcquisitionObservation
	calls       int
	requests    []LeaseAcquisitionRequest
}

func (adapter *recordingLeaseAcquisitionAdapter) ObserveLeaseAcquisition(
	_ context.Context,
	request LeaseAcquisitionRequest,
) (LeaseAcquisitionObservation, error) {
	adapter.calls++
	adapter.requests = append(adapter.requests, request)
	return adapter.observation, nil
}

func permanentPreLeaseObservation(reason PreLeasePermanentFailure) LeaseAcquisitionObservation {
	return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionPermanentFailure, PermanentFailure: reason}
}

func assertProvenNoLeaseTerminal(t *testing.T, snapshot RuntimeSnapshot) {
	t.Helper()
	if snapshot.State != RuntimeTerminal || snapshot.Outcome == RuntimeLost ||
		snapshot.Lease.AcquireStatus != LeaseNotRequested || snapshot.Lease.LeaseID != (SandboxLeaseID{}) ||
		snapshot.Capacity.LogicalRelease != LogicalCapacityReleaseReady ||
		snapshot.Capacity.NoLease != NoLeaseDispositionRecorded ||
		snapshot.Capacity.Physical != PhysicalCapacityNotApplicable ||
		snapshot.CapacityEvidence.RuntimeFencedOrTerminal == (RuntimeFencedOrTerminalEvidence{}) ||
		snapshot.CapacityEvidence.NoLeasePhysicalDisposition == (NoLeasePhysicalDispositionEvidence{}) ||
		snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("not an exact proven-no-lease terminal: %+v", snapshot)
	}
}

func safeTestID(value string) string {
	result := make([]byte, 0, len(value))
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' {
			result = append(result, character)
			continue
		}
		result = append(result, '-')
	}
	return string(result)
}
