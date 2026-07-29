package runtimeexecution

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInspectShowsAuthoritativeActiveLeaseAndPhysicalOccupancy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 16, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "lease-contract-task-authority", 7)
	start := standardStart(t, now, authority, "lease-contract-active")
	nodeID := startNodeID(t, "lease-contract-node")
	workerID := startWorkerAuthorityID(t, "lease-contract-worker")
	nodeAuthorityID := startNodeAuthorityID(t, "lease-contract-node-authority")
	attestationID := startNodeAttestationID(t, "lease-contract-attestation")

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now,
		IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 41, SandboxStart: 71},
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
			ExecutionNodeID: nodeID, NodeCapacityGeneration: 3, SchedulerEpoch: 2, PolicyVersion: 5,
		}},
		Nodes: []ExecutionNodeFixture{{
			ExecutionNodeID: nodeID, Generation: 3, Readiness: NodeReady,
			AttestationID: attestationID, AttestationGeneration: 4,
			AttestedAt: now.Add(-time.Second), ExpiresAt: now.Add(5 * time.Minute),
			ResourceClassID: start.ResourceClassID, ExecutionPolicyID: start.ExecutionPolicyID,
			NodeAuthorityID: nodeAuthorityID, WorkerAuthorityID: workerID, WorkerGeneration: 9,
			AuthorizationGeneration: 11, AuthorizationExpiresAt: now.Add(10 * time.Minute),
			ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
			Containment:        ContainmentEstablished, Reset: ResetCompleted,
		}},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("acquire lease through Execute: %v", err)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
	if err != nil {
		t.Fatalf("inspect active lease: %v", err)
	}
	if inspected != accepted.Snapshot {
		t.Fatalf("Execute and Inspect disagree: execute=%+v inspect=%+v", accepted.Snapshot, inspected)
	}
	if inspected.State != RuntimePreparingPrerequisites || inspected.Lease.Disposition != LeaseActive ||
		inspected.Lease.LeaseID.String() != "sandbox-lease-000041" ||
		inspected.Lease.SandboxID.String() != "sandbox-instance-000071" ||
		inspected.Lease.Generation != 1 || inspected.Lease.Fence != 1 ||
		!inspected.Lease.ExpiresAt.Equal(now.Add(90*time.Second)) ||
		inspected.Lease.WorkerAuthorityID != workerID || inspected.Lease.WorkerGeneration != 9 ||
		inspected.Lease.AuthorizationGeneration != 11 {
		t.Fatalf("Inspect lost exact lease authority: %+v", inspected.Lease)
	}
	if inspected.Node.ExecutionNodeID != nodeID || inspected.Node.Generation != 3 ||
		inspected.Node.Readiness != NodeReady || inspected.Node.AttestationID != attestationID ||
		inspected.Node.Occupancy != NodeOccupied || inspected.Node.Quarantined ||
		inspected.Node.Containment != ContainmentPending || inspected.Node.Reset != ResetRequired ||
		inspected.Capacity.Physical != PhysicalCapacityOccupied {
		t.Fatalf("Inspect lost authoritative node occupancy: node=%+v capacity=%+v", inspected.Node, inspected.Capacity)
	}
}

func TestProviderCapableLeaseRequiresExactActiveQuotaReservation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 16, 30, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name      string
		suffix    string
		mutate    func(*QuotaReservationFixture)
		missing   bool
		wantLease bool
	}{
		{name: "missing", suffix: "missing", missing: true},
		{name: "inactive", suffix: "inactive", mutate: func(reservation *QuotaReservationFixture) { reservation.State = QuotaReservationInactive }},
		{name: "wrong generation", suffix: "wrong-generation", mutate: func(reservation *QuotaReservationFixture) { reservation.Generation++ }},
		{name: "wrong mode", suffix: "wrong-mode", mutate: func(reservation *QuotaReservationFixture) { reservation.Mode = QuotaReservationEnforced }},
		{name: "cross scope", suffix: "cross-scope", mutate: func(reservation *QuotaReservationFixture) {
			reservation.PersonalWorkspaceID = mustPersonalWorkspaceID(t, "foreign-workspace")
		}},
		{name: "expired", suffix: "expired", mutate: func(reservation *QuotaReservationFixture) { reservation.ExpiresAt = now }},
		{name: "active exact", suffix: "active-exact", wantLease: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			authority := mustTaskOrchestrationAuthority(t, "provider-task-authority-"+testCase.suffix, 7)
			base := standardStart(t, now, authority, "provider-"+testCase.suffix)
			input := base.StartRuntimeRunInput
			input.ProviderCapability = ProviderCapabilityRequired
			input.ProviderBinding = &ProviderExecutionBinding{
				QuotaReservationID: mustQuotaReservationID(t, "reservation-"+testCase.suffix),
				Generation:         4, Mode: QuotaReservationObservation,
				GatewayRoutePolicyID: mustGatewayRoutePolicyID(t, "gateway-route-"+testCase.suffix),
			}
			start := mustStart(t, input)
			grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
			grant.ExecutionNodeID = startNodeID(t, "provider-node-"+testCase.suffix)
			grant.NodeCapacityGeneration = 1
			reservation := QuotaReservationFixture{
				QuotaReservationID: start.ProviderBinding.QuotaReservationID,
				Generation:         start.ProviderBinding.Generation, Mode: start.ProviderBinding.Mode,
				State: QuotaReservationActive, PersonalWorkspaceID: start.PersonalWorkspaceID,
				PhaseRunID: start.PhaseRunID, Capability: start.ProviderCapability,
				ValidFrom: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute),
			}
			if testCase.mutate != nil {
				testCase.mutate(&reservation)
			}
			var reservations []QuotaReservationFixture
			if !testCase.missing {
				reservations = []QuotaReservationFixture{reservation}
			}
			harness, err := NewDeterministicHarness(HarnessConfig{
				Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
				Runtimes:          []RuntimeFixture{runtimeFixtureForStart(start, authority)},
				AdmissionGrants:   []AdmissionGrantFixture{grant},
				Nodes:             []ExecutionNodeFixture{executionNodeFixtureForStart(t, start, grant, now)},
				QuotaReservations: reservations,
				LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
					context.Context,
					LeaseAcquisitionRequest,
				) (LeaseAcquisitionObservation, error) {
					return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
				}),
			})
			if err != nil {
				t.Fatalf("new harness: %v", err)
			}
			decision, err := harness.Runtime.Execute(context.Background(), start)
			if err != nil {
				t.Fatalf("execute provider start: %v", err)
			}
			if testCase.wantLease {
				if decision.Snapshot.Lease.Disposition != LeaseActive || decision.Snapshot.State != RuntimePreparingPrerequisites {
					t.Fatalf("exact active Reservation did not authorize lease: %+v", decision.Snapshot)
				}
				return
			}
			if decision.Snapshot.State != RuntimeTerminal || decision.Snapshot.Outcome != RuntimeRejected ||
				decision.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalReservation ||
				decision.Snapshot.Lease.Disposition != LeaseDispositionNone ||
				decision.Snapshot.Capacity.NoLease != NoLeaseDispositionRecorded ||
				decision.Snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
				t.Fatalf("invalid Reservation did not fail closed as proven no-lease: %+v", decision.Snapshot)
			}
		})
	}
}

func TestLeaseAcquireRejectsStaleNodeCatalogSafetyEpoch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 16, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "catalog-lease-authority", 7)
	input := standardStart(t, now, authority, "catalog-lease").StartRuntimeRunInput
	input.CatalogBinding = &CatalogExecutionBinding{
		TemplateLockID:     mustTemplateLockID(t, "catalog-lease-template-lock"),
		TemplateLockDigest: digest(71), ClosureRootDigest: digest(72), SafetyEpoch: 13,
	}
	start := mustStart(t, input)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "catalog-lease-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	node.CatalogSafetyEpoch = start.CatalogBinding.SafetyEpoch - 1
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
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute with stale node catalog epoch: %v", err)
	}
	if decision.Snapshot.State != RuntimeTerminal || decision.Snapshot.Outcome != RuntimeRejected ||
		decision.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalNodeIneligible ||
		decision.Snapshot.Lease.Disposition != LeaseDispositionNone ||
		decision.Snapshot.Capacity.Physical != PhysicalCapacityNotApplicable {
		t.Fatalf("stale node catalog epoch did not fail closed before lease: %+v", decision.Snapshot)
	}
}

func TestLeaseRenewalIsFencedIdempotentAndInspectable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 17, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "renew-task-authority", 7)
	start := standardStart(t, now, authority, "renew")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "renew-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)}, AdmissionGrants: []AdmissionGrantFixture{grant},
		Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	lease := acquired.Snapshot.Lease
	renewAuthority := NewLeaseRenewalAuthority(
		lease.WorkerAuthorityID, lease.WorkerGeneration, lease.NodeAuthorityID, lease.AuthorizationGeneration,
	)
	renew, err := NewRenewSandboxLease(RenewSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "renew-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: grant.ExecutionNodeID, NodeGeneration: NodeGeneration(grant.NodeCapacityGeneration),
		AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
		Authority: renewAuthority, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		RequestedExpiresAt: now.Add(150 * time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("new renewal: %v", err)
	}

	first, err := harness.Maintenance.Maintain(context.Background(), renew)
	if err != nil {
		t.Fatalf("renew current lease: %v", err)
	}
	if first.Lease.Generation != 2 || first.Lease.Fence != 2 ||
		!first.Lease.ExpiresAt.Equal(now.Add(150*time.Second)) || first.Replayed {
		t.Fatalf("renewal did not advance exact authority: %+v", first)
	}
	replayed, err := harness.Maintenance.Maintain(context.Background(), renew)
	if err != nil || replayed != (RuntimeMaintenanceDecision{
		OperationID: first.OperationID, CanonicalRequestDigest: first.CanonicalRequestDigest,
		RuntimeRevision: first.RuntimeRevision, RuntimeFence: first.RuntimeFence,
		Lease: first.Lease, Node: first.Node, Replayed: true,
	}) {
		t.Fatalf("exact renewal replay = %+v err=%v, first=%+v", replayed, err, first)
	}

	stale, err := NewRenewSandboxLease(RenewSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "stale-renew-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: grant.ExecutionNodeID, NodeGeneration: NodeGeneration(grant.NodeCapacityGeneration),
		AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
		Authority: renewAuthority, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		RequestedExpiresAt: now.Add(180 * time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Maintenance.Maintain(context.Background(), stale)
	assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
	if err != nil || inspected.Lease != first.Lease || inspected.Node != first.Node {
		t.Fatalf("stale renewal changed authority: snapshot=%+v err=%v first=%+v", inspected, err, first)
	}
}

func TestPostLeaseCancelFencesAuthorityBeforeCleanupAndDoesNotClaimPhysicalRelease(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 29, 17, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "post-lease-cancel-authority", 7)
	start := standardStart(t, now, authority, "post-lease-cancel")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "post-lease-cancel-node")
	grant.NodeCapacityGeneration = 1
	node := executionNodeFixtureForStart(t, start, grant, now)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "post-lease-cancel-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision:     acquired.Snapshot.RuntimeRevision,
		ExpectedStartOperationID:    start.OperationID,
		ExpectedOperationGeneration: acquired.Snapshot.Operation.Generation,
		ExpectedRuntimeFence:        acquired.Snapshot.RuntimeFence, Authority: authority,
		Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatalf("cancel leased Runtime: %v", err)
	}
	if cancelled.Snapshot.State != RuntimeTerminal || cancelled.Snapshot.Outcome != RuntimeCancelled ||
		cancelled.Snapshot.RuntimeFence != acquired.Snapshot.RuntimeFence+1 ||
		cancelled.Snapshot.Lease.Disposition != LeaseRevoked ||
		cancelled.Snapshot.Lease.Generation != acquired.Snapshot.Lease.Generation+1 ||
		cancelled.Snapshot.Lease.Fence != acquired.Snapshot.Lease.Fence+1 ||
		cancelled.Snapshot.Node.Occupancy != NodeOccupancyUnknown || !cancelled.Snapshot.Node.Quarantined ||
		cancelled.Snapshot.Cleanup.Status != LeaseCleanupPending ||
		cancelled.Snapshot.Capacity.LogicalRelease != LogicalCapacityReleaseReady ||
		cancelled.Snapshot.Capacity.NoLease != NoLeaseDispositionNone ||
		cancelled.Snapshot.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined ||
		cancelled.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal.RuntimeRunID != start.RuntimeRunID ||
		cancelled.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition != (NoLeasePhysicalDispositionEvidence{}) ||
		cancelled.Snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("post-lease cancel crossed fencing/evidence authority: %+v", cancelled.Snapshot)
	}
}

func TestNodeLossFencingAuthorizesOnlyLogicalCapacityRelease(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 17, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "node-loss-task-authority", 7)
	start := standardStart(t, now, authority, "node-loss")
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, "node-loss-node")
	grant.NodeCapacityGeneration = 2
	node := executionNodeFixtureForStart(t, start, grant, now)
	securityID := mustAuthorityID(t, "node-loss-security-authority")
	fencingAuthority := NewSecurityLeaseFencingAuthority(securityID, 2)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	lease := acquired.Snapshot.Lease
	fence, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "node-loss-fence-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeFence: acquired.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: grant.ExecutionNodeID, NodeGeneration: 2, Reason: LeaseFenceNodeLost,
		Authority: fencingAuthority, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Maintenance.Maintain(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, authority))
	if err != nil {
		t.Fatal(err)
	}
	evidence := inspected.CapacityEvidence.RuntimeFencedOrTerminal
	if inspected.Capacity.LogicalRelease != LogicalCapacityReleaseReady ||
		inspected.Capacity.NoLease != NoLeaseDispositionNone ||
		inspected.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined ||
		inspected.Node.Readiness != NodeUnavailable || !inspected.Node.Quarantined ||
		evidence.RuntimeRunID != start.RuntimeRunID || evidence.RuntimeFence != inspected.RuntimeFence ||
		evidence.RuntimeRevision != inspected.RuntimeRevision || evidence.TerminalDecisionID.String() == "" ||
		inspected.CapacityEvidence.NoLeasePhysicalDisposition != (NoLeasePhysicalDispositionEvidence{}) ||
		inspected.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("node loss crossed logical/physical evidence authority: %+v", inspected)
	}
}

func TestRevokeResetAndPoolReuseRequireCompleteCurrentEvidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 18, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "reuse-task-authority", 7)
	firstStart := standardStart(t, now, authority, "reuse-first")
	secondStart := standardStart(t, now, authority, "reuse-second")
	nodeID := startNodeID(t, "reuse-node")
	firstGrant := grantFixtureForStart(firstStart, now.Add(10*time.Minute), true)
	firstGrant.ExecutionNodeID, firstGrant.NodeCapacityGeneration = nodeID, 5
	secondGrant := grantFixtureForStart(secondStart, now.Add(10*time.Minute), true)
	secondGrant.ExecutionNodeID, secondGrant.NodeCapacityGeneration = nodeID, 5
	node := executionNodeFixtureForStart(t, firstStart, firstGrant, now)
	securityAuthorityID := mustAuthorityID(t, "reuse-security")
	cleanupAuthorityID := mustAuthorityID(t, "reuse-reset-authority")
	recoveryAuthorityID := mustAuthorityID(t, "reuse-recovery-authority")
	fenceAuthority := NewSecurityLeaseFencingAuthority(securityAuthorityID, 3)
	resetAuthority := NewSandboxResetAuthority(cleanupAuthorityID, 4)
	attestationAuthority := NewRecoveryNodeAttestationAuthority(recoveryAuthorityID, 5)
	node.ResourceClassID = secondStart.ResourceClassID
	node.ExecutionPolicyID = secondStart.ExecutionPolicyID
	firstStart.StartRuntimeRunInput.ResourceClassID = node.ResourceClassID
	firstStart.StartRuntimeRunInput.ExecutionPolicyID = node.ExecutionPolicyID
	firstStart = mustStart(t, firstStart.StartRuntimeRunInput)
	firstGrant.CanonicalStartDigest = firstStart.CanonicalRequestDigest
	firstGrant.OperationID = firstStart.OperationID

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes: []RuntimeFixture{
			runtimeFixtureForStart(firstStart, authority), runtimeFixtureForStart(secondStart, authority),
		},
		AdmissionGrants: []AdmissionGrantFixture{firstGrant, secondGrant}, Nodes: []ExecutionNodeFixture{node},
		MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
			BindLeaseFencingAuthority(nodeID, fenceAuthority),
			BindSandboxResetAuthority(nodeID, resetAuthority),
			BindNodeAttestationAuthority(nodeID, attestationAuthority),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.Runtime.Execute(context.Background(), firstStart)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	lease := first.Snapshot.Lease
	wrongFenceAuthority := NewSecurityLeaseFencingAuthority(mustAuthorityID(t, "reuse-wrong-security"), 3)
	staleFenceAuthority := NewSecurityLeaseFencingAuthority(securityAuthorityID, 2)
	for _, rejectedAuthority := range []LeaseFencingAuthority{wrongFenceAuthority, staleFenceAuthority} {
		input := FenceSandboxLeaseInput{
			SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "reuse-rejected-revoke-"+rejectedAuthority.id.String()),
			PersonalWorkspaceID: firstStart.PersonalWorkspaceID, RuntimeRunID: firstStart.RuntimeRunID,
			ExpectedRuntimeFence: first.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
			LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
			ExecutionNodeID: nodeID, NodeGeneration: 5, Reason: LeaseFenceRevoked,
			Authority: rejectedAuthority, ReleaseSafetyEpoch: firstStart.ReleaseSafetyEpoch, OccurredAt: now,
		}
		rejected, constructErr := NewFenceSandboxLease(input)
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := harness.Maintenance.Maintain(context.Background(), rejected)
		assertRuntimeLifecycleErrorCode(t, maintainErr, ErrorAuthorizationDenied)
	}
	beforeFence, err := harness.Runtime.Inspect(context.Background(), runtimeRef(firstStart, authority))
	if err != nil || beforeFence != first.Snapshot {
		t.Fatalf("rejected fencing authority changed Runtime: snapshot=%+v err=%v", beforeFence, err)
	}
	revoke, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "reuse-revoke"),
		PersonalWorkspaceID: firstStart.PersonalWorkspaceID, RuntimeRunID: firstStart.RuntimeRunID,
		ExpectedRuntimeFence: first.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: nodeID, NodeGeneration: 5, Reason: LeaseFenceRevoked,
		Authority: fenceAuthority, ReleaseSafetyEpoch: firstStart.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := harness.Maintenance.Maintain(context.Background(), revoke)
	if err != nil {
		t.Fatalf("revoke lease: %v", err)
	}
	if fenced.Lease.Disposition != LeaseRevoked || fenced.Lease.Generation != lease.Generation+1 ||
		fenced.Lease.Fence != lease.Fence+1 || fenced.RuntimeFence != first.Snapshot.RuntimeFence+1 ||
		fenced.Node.Occupancy != NodeOccupancyUnknown || !fenced.Node.Quarantined ||
		fenced.Cleanup.Status != LeaseCleanupPending || !fenced.Cleanup.StopMainProcess ||
		!fenced.Cleanup.StopChildProcesses || !fenced.Cleanup.RevokeSecrets ||
		!fenced.Cleanup.RemoveNetwork || !fenced.Cleanup.FenceRuntimeView ||
		!fenced.Cleanup.ReconcileContainment {
		t.Fatalf("revoke did not fence before immutable cleanup obligations: %+v", fenced)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(firstStart, authority))
	if err != nil || inspected.State != RuntimeStopping || inspected.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined ||
		inspected.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("revoke released capacity before reset: snapshot=%+v err=%v", inspected, err)
	}

	resetEvidenceID := mustEvidenceID(t, "reuse-reset-evidence")
	resetInput := ConfirmSandboxResetInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "reuse-reset"),
		PersonalWorkspaceID: firstStart.PersonalWorkspaceID, RuntimeRunID: firstStart.RuntimeRunID,
		ExpectedRuntimeFence: fenced.RuntimeFence, SandboxLeaseID: fenced.Lease.LeaseID,
		LeaseGeneration: fenced.Lease.Generation, LeaseFence: fenced.Lease.Fence,
		SandboxID: fenced.Lease.SandboxID, SandboxGeneration: fenced.Lease.SandboxGeneration,
		SandboxFence: fenced.Lease.SandboxFence, ExecutionNodeID: nodeID, NodeGeneration: 5,
		Authority:  resetAuthority,
		EvidenceID: resetEvidenceID, EvidenceDigest: digest(91), ProcessStopped: true,
		ChildProcessesStopped: true, SecretsRevoked: true, NetworkRemoved: true,
		ContainmentEstablished: true, ResetCompleted: true, NoUnresolvedOccupancy: true,
		NoStaleWorkerAuthority: true, NoPriorTaskBytes: true, NoPriorSecrets: true,
		NoWritableCacheMutations: true, NoLogsOrTranscripts: true, NoPriorEvidence: true,
		NoProcessState: true, NoNetworkState: true, NoPriorOperationIdentities: true, OccurredAt: now,
	}
	incomplete := resetInput
	incomplete.NoPriorTaskBytes = false
	incompleteReset, err := NewConfirmSandboxReset(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Maintenance.Maintain(context.Background(), incompleteReset)
	assertRuntimeLifecycleErrorCode(t, err, ErrorIntegrityConflict)
	stillFenced, err := harness.Runtime.Inspect(context.Background(), runtimeRef(firstStart, authority))
	if err != nil || stillFenced.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined {
		t.Fatalf("incomplete reset changed capacity: snapshot=%+v err=%v", stillFenced, err)
	}
	for index, rejectedAuthority := range []SandboxResetAuthority{
		NewSandboxResetAuthority(mustAuthorityID(t, "reuse-wrong-cleanup"), 4),
		NewSandboxResetAuthority(cleanupAuthorityID, 3),
	} {
		rejectedInput := resetInput
		rejectedInput.OperationID = mustOperationID(t, fmt.Sprintf("reuse-rejected-reset-%d", index))
		rejectedInput.Authority = rejectedAuthority
		rejected, constructErr := NewConfirmSandboxReset(rejectedInput)
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := harness.Maintenance.Maintain(context.Background(), rejected)
		assertRuntimeLifecycleErrorCode(t, maintainErr, ErrorAuthorizationDenied)
	}

	reset, err := NewConfirmSandboxReset(resetInput)
	if err != nil {
		t.Fatal(err)
	}
	released, err := harness.Maintenance.Maintain(context.Background(), reset)
	if err != nil {
		t.Fatalf("confirm complete reset: %v", err)
	}
	releaseEvidence := released.PhysicalCapacityReleaseReady
	if released.Lease.Disposition != LeaseReleased || released.Cleanup.Status != LeaseCleanupCompleted ||
		released.Node.Occupancy != NodeUnoccupied || !released.Node.Quarantined ||
		releaseEvidence.RuntimeRunID != firstStart.RuntimeRunID || releaseEvidence.SandboxLeaseID != lease.LeaseID ||
		releaseEvidence.ExecutionNodeID != nodeID || releaseEvidence.ResetEvidenceID != resetEvidenceID ||
		releaseEvidence.ResetEvidenceDigest != digest(91) {
		t.Fatalf("complete reset did not publish exact physical release evidence: %+v", released)
	}

	attest, err := NewAttestExecutionNode(AttestExecutionNodeInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "reuse-node-return"),
		Authority:       attestationAuthority,
		ExecutionNodeID: nodeID, NodeGeneration: 5,
		AttestationID: startNodeAttestationID(t, "reuse-return-attestation"), AttestationGeneration: 2,
		AttestedAt: now, ExpiresAt: now.Add(5 * time.Minute), ResourceClassID: node.ResourceClassID,
		ExecutionPolicyID: node.ExecutionPolicyID, NodeAuthorityID: node.NodeAuthorityID,
		WorkerAuthorityID: node.WorkerAuthorityID, WorkerGeneration: node.WorkerGeneration + 1,
		AuthorizationGeneration: node.AuthorizationGeneration + 1,
		AuthorizationExpiresAt:  now.Add(10 * time.Minute), ReleaseSafetyEpoch: firstStart.ReleaseSafetyEpoch,
		ResetEvidenceID: resetEvidenceID, ResetEvidenceDigest: digest(91), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, rejectedAuthority := range []NodeAttestationAuthority{
		NewRecoveryNodeAttestationAuthority(mustAuthorityID(t, "reuse-wrong-recovery"), 5),
		NewRecoveryNodeAttestationAuthority(recoveryAuthorityID, 4),
	} {
		rejectedInput := attest.AttestExecutionNodeInput
		rejectedInput.OperationID = mustOperationID(t, fmt.Sprintf("reuse-rejected-attestation-%d", index))
		rejectedInput.Authority = rejectedAuthority
		rejected, constructErr := NewAttestExecutionNode(rejectedInput)
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := harness.Maintenance.Maintain(context.Background(), rejected)
		assertRuntimeLifecycleErrorCode(t, maintainErr, ErrorAuthorizationDenied)
	}
	returned, err := harness.Maintenance.Maintain(context.Background(), attest)
	if err != nil || returned.Node.Readiness != NodeReady || returned.Node.Quarantined {
		t.Fatalf("fresh current-generation return did not restore readiness: decision=%+v err=%v", returned, err)
	}

	second, err := harness.Runtime.Execute(context.Background(), secondStart)
	if err != nil {
		t.Fatalf("acquire second lease after complete reset: %v", err)
	}
	if second.Snapshot.Lease.LeaseID == lease.LeaseID || second.Snapshot.Lease.SandboxID == lease.SandboxID ||
		second.Snapshot.Lease.SandboxGeneration <= lease.SandboxGeneration ||
		second.Snapshot.Lease.SandboxFence <= lease.SandboxFence ||
		second.Snapshot.Lease.Disposition != LeaseActive {
		t.Fatalf("pool reuse crossed lease identity or fence: first=%+v second=%+v", lease, second.Snapshot.Lease)
	}
}

func TestSchedulerAuthorityCannotManufactureExecutionNodeTruth(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 29, 18, 30, 0, 0, time.UTC)
	_, err := NewAttestExecutionNode(AttestExecutionNodeInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "scheduler-node-attestation"),
		Authority: NodeAttestationAuthority{
			kind: MaintenanceAuthorityScheduler, id: mustAuthorityID(t, "scheduler-node-authority"), generation: 7,
		},
		ExecutionNodeID: startNodeID(t, "scheduler-attested-node"), NodeGeneration: 1,
		AttestationID: startNodeAttestationID(t, "scheduler-attestation"), AttestationGeneration: 1,
		AttestedAt: now, ExpiresAt: now.Add(5 * time.Minute),
		ResourceClassID:   mustResourceClassID(t, "scheduler-resource-class"),
		ExecutionPolicyID: mustExecutionPolicyID(t, "scheduler-execution-policy"),
		NodeAuthorityID:   startNodeAuthorityID(t, "scheduler-node-runtime-authority"),
		WorkerAuthorityID: startWorkerAuthorityID(t, "scheduler-worker-authority"), WorkerGeneration: 1,
		AuthorizationGeneration: 1, AuthorizationExpiresAt: now.Add(5 * time.Minute),
		ReleaseSafetyEpoch: 1, CatalogSafetyEpoch: 1,
		ResetEvidenceID: mustEvidenceID(t, "scheduler-reset-evidence"), ResetEvidenceDigest: digest(92),
		OccurredAt: now,
	})
	assertRuntimeLifecycleErrorCode(t, err, ErrorInvalidRequest)
}

func assertRuntimeLifecycleErrorCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code() != want {
		t.Fatalf("error = %T %v, want Runtime Execution code %v", err, err, want)
	}
}

func runtimeRef(start StartRuntimeRun, authority RuntimeAuthority) RuntimeRunRef {
	return RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	}
}

func startNodeID(t *testing.T, value string) ExecutionNodeID {
	t.Helper()
	id, err := NewExecutionNodeID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustAuthorityID(t *testing.T, value string) AuthorityID {
	t.Helper()
	id, err := NewAuthorityID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startWorkerAuthorityID(t *testing.T, value string) WorkerAuthorityID {
	t.Helper()
	id, err := NewWorkerAuthorityID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startNodeAuthorityID(t *testing.T, value string) NodeAuthorityID {
	t.Helper()
	id, err := NewNodeAuthorityID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startNodeAttestationID(t *testing.T, value string) NodeAttestationID {
	t.Helper()
	id, err := NewNodeAttestationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
