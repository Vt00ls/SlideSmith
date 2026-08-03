package runtimeexecution

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentWorkerAcceptIsDurableInspectableAndPayloadBound(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 0, 0, 0, time.UTC)
	backend := newContractAgentWorkerBackend()
	harness, start, accepted := newReadyWorkerProtocolHarness(t, now, "agent-accept", WorkerAgent, backend, nil)

	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID,
		CapsuleID:    accepted.Snapshot.Capsule.CapsuleID,
		Digest:       accepted.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatalf("claim Capsule: %v", err)
	}
	command, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatalf("construct private Accept: %v", err)
	}

	acknowledged, err := harness.workers.accept(context.Background(), command)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	replayed, err := harness.workers.accept(context.Background(), command)
	if err != nil || replayed != acknowledged || backend.acceptCount() != 1 {
		t.Fatalf("exact Accept replay = %+v err=%v calls=%d, first=%+v", replayed, err, backend.acceptCount(), acknowledged)
	}

	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatalf("Inspect accepted worker: %v", err)
	}
	if inspected.State != RuntimeStarting || inspected.Outcome != RuntimeOutcomeNone ||
		inspected.Worker.Status != WorkerOperationAccepted ||
		inspected.Worker.WorkerClass != WorkerAgent ||
		inspected.Worker.AcceptOperationID != delivery.OperationID ||
		inspected.Worker.OperationAckID != acknowledged.OperationAckID ||
		inspected.Worker.OperationAckDigest != acknowledged.CanonicalDigest {
		t.Fatalf("accepted worker projection crossed or lost authority: %+v", inspected)
	}

	conflict := command
	conflict.CapsuleDigest = digest(250)
	conflict.CanonicalDigest = canonicalWorkerAcceptDigest(conflict)
	_, err = harness.workers.accept(context.Background(), conflict)
	assertErrorCode(t, err, ErrorIntegrityConflict)
	if backend.acceptCount() != 1 {
		t.Fatalf("conflicting Accept reached worker backend: calls=%d", backend.acceptCount())
	}
}

func TestAgentCapabilityContractRejectsIntentAndPromptSubstitution(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 10, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "agent-contract-authority", 7)
	start := standardStart(t, now, authority, "agent-contract")
	plan := validAgentPlanForStart(start, "agent-contract")
	if _, err := newAgentWorkerCapabilityAdapter(plan, newContractAgentWorkerBackend()); err != nil {
		t.Fatalf("canonical Agent contract: %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(*agentCapabilityPlan)
	}{
		{name: "intent", mutate: func(changed *agentCapabilityPlan) {
			changed.IntentReference = AgentIntentReference{value: "substituted-agent-intent"}
		}},
		{name: "prompt", mutate: func(changed *agentCapabilityPlan) {
			changed.PromptReference = AgentPromptReference{value: "substituted-agent-prompt"}
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := plan
			test.mutate(&changed)
			if canonicalAgentCapabilityContractDigest(changed) == plan.CapabilityContractDigest {
				t.Fatalf("Agent capability contract omitted %s reference", test.name)
			}
			if _, err := newAgentWorkerCapabilityAdapter(changed, newContractAgentWorkerBackend()); errorCode(err) != ErrorInvalidRequest {
				t.Fatalf("retained contract accepted substituted %s: %v", test.name, err)
			}
		})
	}
}

func TestWorkerHeartbeatDelegatesToCurrentLeaseLifecycleAndRejectsStaleFence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 20, 0, 0, time.UTC)
	harness, start, accepted := newReadyWorkerProtocolHarness(
		t, now, "worker-heartbeat", WorkerAgent, newContractAgentWorkerBackend(), nil,
	)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
		Digest: accepted.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), accept); err != nil {
		t.Fatal(err)
	}
	before, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "worker-heartbeat-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		StartOperationID: start.OperationID, CapsuleID: before.Capsule.CapsuleID,
		CapsuleDigest: before.Capsule.Digest, RuntimeFence: before.RuntimeFence,
		Lease: before.Lease, Node: before.Node, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: startCatalogSafetyEpoch(start),
		RequestedExpiresAt: before.Lease.ExpiresAt.Add(time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.workers.heartbeat(context.Background(), heartbeat)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if first.SchemaVersion != SchemaV1 || first.CanonicalRequestDigest != heartbeat.CanonicalDigest ||
		first.CanonicalDigest == heartbeat.CanonicalDigest || !validWorkerLeaseDecision(first) {
		t.Fatalf("heartbeat returned an unbound decision envelope: %+v", first)
	}
	decisionMutations := []struct {
		name   string
		mutate func(*workerLeaseDecision)
	}{
		{name: "schema version", mutate: func(changed *workerLeaseDecision) { changed.SchemaVersion++ }},
		{name: "operation", mutate: func(changed *workerLeaseDecision) {
			changed.OperationID = mustOperationID(t, "different-worker-heartbeat-decision-operation")
		}},
		{name: "request digest", mutate: func(changed *workerLeaseDecision) { changed.CanonicalRequestDigest = digest(202) }},
		{name: "runtime revision", mutate: func(changed *workerLeaseDecision) { changed.RuntimeRevision++ }},
		{name: "runtime fence", mutate: func(changed *workerLeaseDecision) { changed.RuntimeFence++ }},
		{name: "lease snapshot", mutate: func(changed *workerLeaseDecision) {
			changed.Lease.AuthorizationExpiresAt = changed.Lease.AuthorizationExpiresAt.Add(time.Second)
		}},
	}
	for _, test := range decisionMutations {
		t.Run("decision "+test.name, func(t *testing.T) {
			changed := first
			test.mutate(&changed)
			if canonicalWorkerLeaseDecisionDigest(changed) == first.CanonicalDigest {
				t.Fatalf("canonical worker lease decision omitted %s", test.name)
			}
		})
	}
	replayed, err := harness.workers.heartbeat(context.Background(), heartbeat)
	if err != nil || !replayed.Replayed || replayed.Lease != first.Lease ||
		replayed.CanonicalDigest != first.CanonicalDigest || !validWorkerLeaseDecision(replayed) {
		t.Fatalf("heartbeat replay = %+v err=%v first=%+v", replayed, err, first)
	}
	if first.Lease.Generation != before.Lease.Generation+1 || first.Lease.Fence != before.Lease.Fence+1 ||
		first.RuntimeFence != before.RuntimeFence {
		t.Fatalf("heartbeat crossed lifecycle authority: before=%+v decision=%+v", before.Lease, first)
	}

	stale := heartbeat
	stale.OperationID = mustOperationID(t, "worker-heartbeat-stale")
	stale.CanonicalDigest = canonicalWorkerHeartbeatDigest(stale)
	_, err = harness.workers.heartbeat(context.Background(), stale)
	assertErrorCode(t, err, ErrorIntegrityConflict)
	after, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || after.Lease != first.Lease || after.Capacity.Physical != PhysicalCapacityOccupied {
		t.Fatalf("stale heartbeat changed lease/capacity: %+v err=%v", after, err)
	}
}

func TestWorkerHeartbeatCanonicalDigestBindsCompleteLeaseAndNodeSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 30, 0, 0, time.UTC)
	harness, start, accepted := newReadyWorkerProtocolHarness(
		t, now, "worker-heartbeat-complete-snapshot", WorkerAgent, newContractAgentWorkerBackend(), nil,
	)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
		Digest: accepted.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), accept); err != nil {
		t.Fatal(err)
	}
	current, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "worker-heartbeat-complete-snapshot-operation"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		StartOperationID: start.OperationID, CapsuleID: current.Capsule.CapsuleID,
		CapsuleDigest: current.Capsule.Digest, RuntimeFence: current.RuntimeFence,
		Lease: current.Lease, Node: current.Node, ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: startCatalogSafetyEpoch(start),
		RequestedExpiresAt: current.Lease.ExpiresAt.Add(time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name   string
		mutate func(*workerHeartbeat)
	}{
		{name: "lease acquire status", mutate: func(value *workerHeartbeat) { value.Lease.AcquireStatus++ }},
		{name: "lease acquire operation", mutate: func(value *workerHeartbeat) {
			value.Lease.AcquireOperationID = mustOperationID(t, "different-acquire-operation")
		}},
		{name: "lease acquire digest", mutate: func(value *workerHeartbeat) { value.Lease.AcquireDigest = digest(201) }},
		{name: "lease disposition", mutate: func(value *workerHeartbeat) { value.Lease.Disposition++ }},
		{name: "lease expiry", mutate: func(value *workerHeartbeat) { value.Lease.ExpiresAt = value.Lease.ExpiresAt.Add(time.Second) }},
		{name: "sandbox identity", mutate: func(value *workerHeartbeat) { value.Lease.SandboxID = SandboxID{value: "different-sandbox"} }},
		{name: "sandbox generation", mutate: func(value *workerHeartbeat) { value.Lease.SandboxGeneration++ }},
		{name: "sandbox fence", mutate: func(value *workerHeartbeat) { value.Lease.SandboxFence++ }},
		{name: "authorization expiry", mutate: func(value *workerHeartbeat) {
			value.Lease.AuthorizationExpiresAt = value.Lease.AuthorizationExpiresAt.Add(time.Second)
		}},
		{name: "node readiness", mutate: func(value *workerHeartbeat) { value.Node.Readiness++ }},
		{name: "node attested at", mutate: func(value *workerHeartbeat) { value.Node.AttestedAt = value.Node.AttestedAt.Add(time.Second) }},
		{name: "node expiry", mutate: func(value *workerHeartbeat) { value.Node.ExpiresAt = value.Node.ExpiresAt.Add(time.Second) }},
		{name: "node occupancy", mutate: func(value *workerHeartbeat) { value.Node.Occupancy++ }},
		{name: "node quarantine", mutate: func(value *workerHeartbeat) { value.Node.Quarantined = !value.Node.Quarantined }},
		{name: "node containment", mutate: func(value *workerHeartbeat) { value.Node.Containment++ }},
		{name: "node reset", mutate: func(value *workerHeartbeat) { value.Node.Reset++ }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := heartbeat
			test.mutate(&changed)
			if canonicalWorkerHeartbeatDigest(changed) == heartbeat.CanonicalDigest {
				t.Fatalf("canonical heartbeat digest omitted %s", test.name)
			}
		})
	}
}

func TestWorkerObserveOrdersCursorAndKeepsTerminalClaimsAsEvidenceCandidates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 40, 0, 0, time.UTC)
	backend := newContractAgentWorkerBackend()
	harness, start, accepted := newReadyWorkerProtocolHarness(t, now, "worker-observe", WorkerAgent, backend, nil)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: accepted.Snapshot.Capsule.CapsuleID,
		Digest: accepted.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), accept); err != nil {
		t.Fatal(err)
	}

	starting, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	firstRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, starting), initialWorkerCursor(starting))
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.workers.observe(context.Background(), firstRequest)
	if err != nil || first.Disposition != WorkerObservationAccepted {
		t.Fatalf("running observation: %+v err=%v", first, err)
	}
	running, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || running.State != RuntimeRunning || running.Worker.Cursor.Position != 1 ||
		running.Worker.LastObservationKind != WorkerObservedRunning {
		t.Fatalf("running projection: %+v err=%v", running, err)
	}
	replayed, err := harness.workers.observe(context.Background(), firstRequest)
	if err != nil || !replayed.Replayed || replayed.Observation != first.Observation {
		t.Fatalf("observation replay: %+v err=%v first=%+v", replayed, err, first)
	}
	exactRedeliveryRequest, err := newWorkerObserve(
		workerOperationRefFromSnapshot(start, running), running.Worker.Cursor,
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		ObservationID: first.Observation.ObservationID, Kind: first.Observation.Kind,
		StreamGeneration: first.Observation.StreamGeneration, Position: first.Observation.Position,
		ObservedAt: first.Observation.ObservedAt,
	})
	exactRedelivery, err := harness.workers.observe(context.Background(), exactRedeliveryRequest)
	if err != nil || !exactRedelivery.Replayed || exactRedelivery.Disposition != WorkerObservationAccepted ||
		exactRedelivery.Observation != first.Observation {
		t.Fatalf("exact observation redelivery: %+v err=%v first=%+v", exactRedelivery, err, first)
	}
	afterExactRedelivery, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || afterExactRedelivery.RuntimeRevision != running.RuntimeRevision ||
		afterExactRedelivery.Worker != running.Worker {
		t.Fatalf("exact observation redelivery mutated projection: %+v err=%v", afterExactRedelivery, err)
	}

	backend.enqueueObservation(contractWorkerObservation{
		Kind: WorkerObservedSucceeded, ObservedAt: now,
		EvidenceID:     mustEvidenceID(t, "worker-observe-success-evidence"),
		EvidenceDigest: digest(243), InternalCallCount: 4,
	})
	terminalRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, running), running.Worker.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := harness.workers.observe(context.Background(), terminalRequest)
	if err != nil || terminal.Disposition != WorkerObservationAccepted {
		t.Fatalf("success observation: %+v err=%v", terminal, err)
	}
	observed, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	if observed.State != RuntimeReconciling || observed.Outcome != RuntimeOutcomeNone ||
		observed.Worker.Status != WorkerOperationSuccessObserved || observed.Worker.Cursor.Position != 2 ||
		observed.Worker.EvidenceCandidate.EvidenceID != terminal.Observation.Evidence.EvidenceID ||
		observed.Worker.EvidenceCandidate.InternalCallCount != 4 ||
		observed.EvidenceRoot != (EvidenceRootSnapshot{}) || observed.Capacity.Physical != PhysicalCapacityOccupied {
		t.Fatalf("worker claim bypassed #82 terminal/evidence authority: %+v", observed)
	}
}

func TestWorkerObserveBindsCurrentTaskAndRuntimeRevisions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 50, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*workerOperationRef)
	}{
		{name: "Task revision", mutate: func(ref *workerOperationRef) { ref.TaskRevision++ }},
		{name: "Runtime revision", mutate: func(ref *workerOperationRef) { ref.RuntimeRevision++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newContractAgentWorkerBackend()
			harness, start, prepared := newReadyWorkerProtocolHarness(
				t, now, "observe-revision-"+strings.ToLower(strings.ReplaceAll(test.name, " ", "-")),
				WorkerAgent, backend, nil,
			)
			delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
				Digest: prepared.Snapshot.Capsule.Digest,
			})
			if err != nil {
				t.Fatal(err)
			}
			accept, err := newWorkerAccept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.workers.accept(context.Background(), accept); err != nil {
				t.Fatal(err)
			}
			current, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			ref := workerOperationRefFromSnapshot(start, current)
			test.mutate(&ref)
			request, err := newWorkerObserve(ref, initialWorkerCursor(current))
			if err != nil {
				t.Fatal(err)
			}
			backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
			if _, err := harness.workers.observe(context.Background(), request); errorCode(err) != ErrorAuthorizationDenied {
				t.Fatalf("stale revision Observe err=%v, want authorization denied", err)
			}
			if backend.observeCount() != 0 {
				t.Fatalf("stale revision reached worker backend %d times", backend.observeCount())
			}
		})
	}
}

func TestWorkerObserveRejectsExpiredLeaseOrAuthorization(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 55, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		expires func(RuntimeSnapshot) time.Time
		prepare func(*invariantEngine, RuntimeRunID)
	}{
		{name: "lease", expires: func(snapshot RuntimeSnapshot) time.Time { return snapshot.Lease.ExpiresAt }},
		{
			name: "authorization",
			expires: func(snapshot RuntimeSnapshot) time.Time {
				return snapshot.Lease.AuthorizationExpiresAt
			},
			prepare: func(engine *invariantEngine, runtimeRunID RuntimeRunID) {
				engine.store.mu.Lock()
				defer engine.store.mu.Unlock()
				record := engine.store.runtimes[runtimeRunID]
				record.lease.ExpiresAt = record.lease.AuthorizationExpiresAt.Add(time.Minute)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newContractAgentWorkerBackend()
			harness, start, prepared := newReadyWorkerProtocolHarness(
				t, now, "observe-expiry-"+test.name, WorkerAgent, backend, nil,
			)
			delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
				Digest: prepared.Snapshot.Capsule.Digest,
			})
			if err != nil {
				t.Fatal(err)
			}
			accept, err := newWorkerAccept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.workers.accept(context.Background(), accept); err != nil {
				t.Fatal(err)
			}
			current, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			request, err := newWorkerObserve(workerOperationRefFromSnapshot(start, current), initialWorkerCursor(current))
			if err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(harness.workers.(*invariantEngine), start.RuntimeRunID)
			}
			if err := harness.AdvanceClock(test.expires(current).Sub(now)); err != nil {
				t.Fatal(err)
			}
			backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
			if _, err := harness.workers.observe(context.Background(), request); errorCode(err) != ErrorAuthorizationDenied {
				t.Fatalf("expired %s Observe err=%v, want authorization denied", test.name, err)
			}
			if backend.observeCount() != 0 {
				t.Fatalf("expired %s reached worker backend %d times", test.name, backend.observeCount())
			}
		})
	}
}

func TestWorkerTerminalObservationIsImmutableExceptForExactReplay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		terminal    contractWorkerObservation
		replacement WorkerObservationKind
	}{
		{
			name: "success cannot become running", replacement: WorkerObservedRunning,
			terminal: contractWorkerObservation{
				Kind: WorkerObservedSucceeded, ObservedAt: now,
				EvidenceID:     mustEvidenceID(t, "immutable-success-evidence"),
				EvidenceDigest: digest(251), InternalCallCount: 1,
			},
		},
		{
			name: "success cannot become failure", replacement: WorkerObservedFailed,
			terminal: contractWorkerObservation{
				Kind: WorkerObservedSucceeded, ObservedAt: now,
				EvidenceID:     mustEvidenceID(t, "immutable-success-opposite-evidence"),
				EvidenceDigest: digest(252), InternalCallCount: 1,
			},
		},
		{
			name: "failure cannot become success", replacement: WorkerObservedSucceeded,
			terminal: contractWorkerObservation{
				Kind: WorkerObservedFailed, ObservedAt: now,
				EvidenceID:     mustEvidenceID(t, "immutable-failure-evidence"),
				EvidenceDigest: digest(253), InternalCallCount: 1, SafeFailure: WorkerFailureCapability,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newContractAgentWorkerBackend()
			harness, start, prepared := newReadyWorkerProtocolHarness(
				t, now, "terminal-"+strings.ReplaceAll(test.name, " ", "-"), WorkerAgent, backend, nil,
			)
			delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
				Digest: prepared.Snapshot.Capsule.Digest,
			})
			if err != nil {
				t.Fatal(err)
			}
			accept, err := newWorkerAccept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.workers.accept(context.Background(), accept); err != nil {
				t.Fatal(err)
			}
			starting, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			request, err := newWorkerObserve(workerOperationRefFromSnapshot(start, starting), initialWorkerCursor(starting))
			if err != nil {
				t.Fatal(err)
			}
			backend.enqueueObservation(test.terminal)
			accepted, err := harness.workers.observe(context.Background(), request)
			if err != nil || accepted.Disposition != WorkerObservationAccepted {
				t.Fatalf("terminal observation: %+v err=%v", accepted, err)
			}
			replayed, err := harness.workers.observe(context.Background(), request)
			if err != nil || !replayed.Replayed || replayed.Observation != accepted.Observation {
				t.Fatalf("terminal exact replay: %+v err=%v", replayed, err)
			}
			terminal, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := newWorkerObserve(workerOperationRefFromSnapshot(start, terminal), terminal.Worker.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			next := contractWorkerObservation{Kind: test.replacement, ObservedAt: now}
			if test.replacement != WorkerObservedRunning {
				next.EvidenceID = mustEvidenceID(t, "replacement-"+strings.ReplaceAll(test.name, " ", "-"))
				next.EvidenceDigest, next.InternalCallCount = digest(254), 1
				if test.replacement == WorkerObservedFailed {
					next.SafeFailure = WorkerFailureCapability
				}
			}
			backend.enqueueObservation(next)
			beforeCalls := backend.observeCount()
			if _, err := harness.workers.observe(context.Background(), replacement); errorCode(err) != ErrorAuthorizationDenied {
				t.Fatalf("terminal replacement err=%v, want authorization denied", err)
			}
			if backend.observeCount() != beforeCalls {
				t.Fatalf("terminal replacement reached backend: before=%d after=%d", beforeCalls, backend.observeCount())
			}
			after, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil || after.Worker != terminal.Worker {
				t.Fatalf("terminal worker evidence was overwritten: before=%+v after=%+v err=%v", terminal.Worker, after.Worker, err)
			}
		})
	}
}

func TestWorkerStopIsExactIdempotentAndClaimsOnlyBestEffortAcceptance(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	backend := newContractAgentWorkerBackend()
	harness, start, prepared := newReadyWorkerProtocolHarness(t, now, "worker-stop", WorkerAgent, backend, nil)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), accept); err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "worker-stop-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: accepted.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: accepted.Operation.Generation, ExpectedRuntimeFence: accepted.RuntimeFence,
		Authority: start.Authority, Reason: CancellationUserRequested,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := newWorkerStopIntentFromSnapshot(
		start, cancelled.Snapshot, mustOperationID(t, "worker-stop-request"), WorkerStopCancellation, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := harness.workers.stop(context.Background(), intent)
	if err != nil || !first.BestEffortAccepted {
		t.Fatalf("Stop: %+v err=%v", first, err)
	}
	replayed, err := harness.workers.stop(context.Background(), intent)
	if err != nil || replayed != first || backend.stopCount() != 1 {
		t.Fatalf("Stop replay: %+v err=%v calls=%d first=%+v", replayed, err, backend.stopCount(), first)
	}
	conflict := intent
	conflict.Deadline = conflict.Deadline.Add(time.Second)
	conflict.CanonicalDigest = canonicalWorkerStopIntentDigest(conflict)
	_, err = harness.workers.stop(context.Background(), conflict)
	assertErrorCode(t, err, ErrorIntegrityConflict)

	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	if inspected.State != RuntimeTerminal || inspected.Outcome != RuntimeCancelled ||
		inspected.Worker.Stop.Status != WorkerStopAccepted || inspected.Worker.Stop.AckID != first.AckID ||
		inspected.Lease != cancelled.Snapshot.Lease || inspected.Node != cancelled.Snapshot.Node ||
		inspected.Cleanup != cancelled.Snapshot.Cleanup || inspected.Capacity != cancelled.Snapshot.Capacity ||
		inspected.CapacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("Stop ack claimed containment or release authority: %+v", inspected)
	}
}

func TestWorkerStopReasonMustMatchAuthoritativeCause(t *testing.T) {
	t.Parallel()

	baseNow := time.Date(2026, time.July, 31, 12, 10, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		wantReason WorkerStopReason
		fence      LeaseFenceReason
		cancel     bool
	}{
		{name: "cancellation", wantReason: WorkerStopCancellation, cancel: true},
		{name: "lease revoked", wantReason: WorkerStopLeaseRevoked, fence: LeaseFenceRevoked},
		{name: "lease expired", wantReason: WorkerStopDeadline, fence: LeaseFenceExpired},
		{name: "node lost", wantReason: WorkerStopNodeLost, fence: LeaseFenceNodeLost},
	} {
		t.Run(test.name, func(t *testing.T) {
			suffix := "stop-reason-" + strings.ReplaceAll(test.name, " ", "-")
			backend := newContractAgentWorkerBackend()
			harness, start, prepared := newReadyWorkerProtocolHarness(
				t, baseNow, suffix, WorkerAgent, backend, nil,
			)
			delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
				Digest: prepared.Snapshot.Capsule.Digest,
			})
			if err != nil {
				t.Fatal(err)
			}
			accept, err := newWorkerAccept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := harness.workers.accept(context.Background(), accept); err != nil {
				t.Fatal(err)
			}
			current, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			occurredAt := baseNow
			if test.cancel {
				cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
					SchemaVersion: SchemaV1, OperationID: mustOperationID(t, suffix+"-cancel"),
					PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
					PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
					ExpectedRuntimeRevision: current.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
					ExpectedOperationGeneration: current.Operation.Generation, ExpectedRuntimeFence: current.RuntimeFence,
					Authority: start.Authority, Reason: CancellationUserRequested,
					SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: occurredAt,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := harness.Runtime.Execute(context.Background(), cancel); err != nil {
					t.Fatal(err)
				}
			} else {
				if test.fence == LeaseFenceExpired {
					occurredAt = current.Lease.ExpiresAt
					if err := harness.AdvanceClock(occurredAt.Sub(baseNow)); err != nil {
						t.Fatal(err)
					}
				}
				fence, err := NewFenceSandboxLease(FenceSandboxLeaseInput{
					SchemaVersion: SchemaV1, OperationID: mustOperationID(t, suffix+"-fence"),
					PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
					ExpectedRuntimeFence: current.RuntimeFence, SandboxLeaseID: current.Lease.LeaseID,
					LeaseGeneration: current.Lease.Generation, LeaseFence: current.Lease.Fence,
					ExecutionNodeID: current.Node.ExecutionNodeID, NodeGeneration: current.Node.Generation,
					Reason: test.fence, Authority: workerTestFencingAuthority(t, suffix),
					ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: occurredAt,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := harness.Maintenance.Maintain(context.Background(), fence); err != nil {
					t.Fatal(err)
				}
			}
			stopping, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			for reason := WorkerStopCancellation; reason <= WorkerStopNodeLost; reason++ {
				if reason == test.wantReason {
					continue
				}
				intent, err := newWorkerStopIntentFromSnapshot(
					start, stopping, mustOperationID(t, suffix+"-mismatch-"+fmt.Sprint(reason)),
					reason, occurredAt.Add(time.Minute),
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := harness.workers.stop(context.Background(), intent); errorCode(err) != ErrorAuthorizationDenied {
					t.Fatalf("state %s accepted mismatched reason %v: %v", test.name, reason, err)
				}
			}
			if backend.stopCount() != 0 {
				t.Fatalf("mismatched reasons reached backend %d times", backend.stopCount())
			}
			intent, err := newWorkerStopIntentFromSnapshot(
				start, stopping, mustOperationID(t, suffix+"-matching"), test.wantReason, occurredAt.Add(time.Minute),
			)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := harness.workers.stop(context.Background(), intent)
			if err != nil || !ack.BestEffortAccepted || backend.stopCount() != 1 {
				t.Fatalf("truthful %s Stop: %+v err=%v calls=%d", test.name, ack, err, backend.stopCount())
			}
		})
	}
}

func TestToolWorkerUsesIndependentBackendAndClosedTypedCapabilityPayload(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 20, 0, 0, time.UTC)
	backend := newContractToolWorkerBackend()
	harness, start, prepared := newReadyWorkerProtocolHarness(t, now, "tool-worker", WorkerTool, nil, backend)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), command); err != nil {
		t.Fatalf("Tool Accept: %v", err)
	}
	inspected, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || inspected.Worker.WorkerClass != WorkerTool || backend.acceptCount() != 1 {
		t.Fatalf("Tool backend independence: snapshot=%+v calls=%d err=%v", inspected, backend.acceptCount(), err)
	}

	assertExactStructFieldTypes(t, reflect.TypeOf(toolTypedParameters{}), []permittedFieldType{
		{reflect.TypeOf(SchemaVersion(0)), 1}, {reflect.TypeOf(ToolParameterKind(0)), 1},
		{reflect.TypeOf(ImmutableInputManifestIdentity{}), 1}, {reflect.TypeOf(Digest{}), 2},
	})
	assertExactStructFieldTypes(t, reflect.TypeOf(toolCapabilityPlan{}), []permittedFieldType{
		{reflect.TypeOf(RuntimeBindingID{}), 1}, {reflect.TypeOf(Digest{}), 7},
		{reflect.TypeOf(ToolCapabilityKey(0)), 1}, {reflect.TypeOf(toolTypedParameters{}), 1},
		{reflect.TypeOf(false), 1},
	})
	assertExactStructFieldTypes(t, reflect.TypeOf(agentCapabilityPlan{}), []permittedFieldType{
		{reflect.TypeOf(RuntimeBindingID{}), 1}, {reflect.TypeOf(Digest{}), 7},
		{reflect.TypeOf(AgentIntentReference{}), 1}, {reflect.TypeOf(AgentPromptReference{}), 1},
		{reflect.TypeOf(false), 1},
	})
	assertExactStructFieldTypes(t, reflect.TypeOf(toolCapabilityInvocation{}), []permittedFieldType{
		{reflect.TypeOf(RuntimeRunID{}), 1}, {reflect.TypeOf(OperationID{}), 1},
		{reflect.TypeOf(ExecutionCapsuleID{}), 1}, {reflect.TypeOf(Digest{}), 7},
		{reflect.TypeOf(RuntimeBindingID{}), 1}, {reflect.TypeOf(ToolCapabilityKey(0)), 1},
		{reflect.TypeOf(toolTypedParameters{}), 1}, {reflect.TypeOf(ResolvedImmutableInputManifest{}), 1},
		{reflect.TypeOf(GatewayGrantID{}), 1}, {reflect.TypeOf(GatewayGrantGeneration(0)), 1},
		{reflect.TypeOf(LeaseGeneration(0)), 1}, {reflect.TypeOf(LeaseFence(0)), 1},
	})
	assertExactStructFieldTypes(t, reflect.TypeOf(agentCapabilityInvocation{}), []permittedFieldType{
		{reflect.TypeOf(RuntimeRunID{}), 1}, {reflect.TypeOf(OperationID{}), 1},
		{reflect.TypeOf(ExecutionCapsuleID{}), 1}, {reflect.TypeOf(Digest{}), 7},
		{reflect.TypeOf(RuntimeBindingID{}), 1}, {reflect.TypeOf(AgentIntentReference{}), 1},
		{reflect.TypeOf(AgentPromptReference{}), 1}, {reflect.TypeOf(ResolvedImmutableInputManifest{}), 1},
		{reflect.TypeOf(GatewayGrantID{}), 1}, {reflect.TypeOf(GatewayGrantGeneration(0)), 1},
		{reflect.TypeOf(QuotaReservationID{}), 1}, {reflect.TypeOf(QuotaReservationGeneration(0)), 1},
		{reflect.TypeOf(LeaseGeneration(0)), 1}, {reflect.TypeOf(LeaseFence(0)), 1},
	})

	assertExactInterfaceSignatures(t, reflect.TypeOf((*toolWorkerBackend)(nil)).Elem(), []reflect.Type{
		reflect.TypeOf((func(context.Context, toolCapabilityInvocation, workerAccept) (workerOperationAck, error))(nil)),
		reflect.TypeOf((func(context.Context, toolCapabilityInvocation, workerObserve) (workerBackendObservation, error))(nil)),
		reflect.TypeOf((func(context.Context, toolCapabilityInvocation, workerStopIntent) (workerStopAck, error))(nil)),
	})
	assertExactInterfaceSignatures(t, reflect.TypeOf((*agentWorkerBackend)(nil)).Elem(), []reflect.Type{
		reflect.TypeOf((func(context.Context, agentCapabilityInvocation, workerAccept) (workerOperationAck, error))(nil)),
		reflect.TypeOf((func(context.Context, agentCapabilityInvocation, workerObserve) (workerBackendObservation, error))(nil)),
		reflect.TypeOf((func(context.Context, agentCapabilityInvocation, workerStopIntent) (workerStopAck, error))(nil)),
	})
}

func TestProviderCapableToolWorkerLifecyclePreservesGatewayLineage(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 30, 0, 0, time.UTC)
	backend := newContractToolWorkerBackend()
	harness, start, prepared := newReadyProviderToolWorkerProtocolHarness(t, now, "provider-tool-worker", backend)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), accept); err != nil {
		t.Fatalf("provider Tool Accept: %v", err)
	}
	starting, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil || !starting.Gateway.Ready || starting.Gateway.CurrentGrant.GatewayGrantID == (GatewayGrantID{}) {
		t.Fatalf("provider Tool missing current Gateway grant: %+v err=%v", starting.Gateway, err)
	}
	backend.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
	runningRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, starting), initialWorkerCursor(starting))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.observe(context.Background(), runningRequest); err != nil {
		t.Fatalf("provider Tool running: %v", err)
	}
	running, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	if err != nil {
		t.Fatal(err)
	}
	backend.enqueueObservation(contractWorkerObservation{
		Kind: WorkerObservedSucceeded, ObservedAt: now,
		EvidenceID:     mustEvidenceID(t, "provider-tool-worker-evidence"),
		EvidenceDigest: digest(249), InternalCallCount: 2,
	})
	successRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, running), running.Worker.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.observe(context.Background(), successRequest); err != nil {
		t.Fatalf("provider Tool success evidence: %v", err)
	}
	candidate, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
	grant := candidate.Gateway.CurrentGrant
	evidence := candidate.Worker.EvidenceCandidate
	if err != nil || candidate.Worker.Status != WorkerOperationSuccessObserved ||
		evidence.GatewayGrantID != grant.GatewayGrantID || evidence.GatewayGrantGeneration != grant.Generation ||
		evidence.GatewayGrantDigest != grant.CanonicalDigest || evidence.InternalCallCount != 2 {
		t.Fatalf("provider Tool Gateway lineage/evidence: candidate=%+v grant=%+v err=%v", evidence, grant, err)
	}
	cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "provider-tool-worker-cancel"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision: candidate.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
		ExpectedOperationGeneration: candidate.Operation.Generation, ExpectedRuntimeFence: candidate.RuntimeFence,
		Authority: start.Authority, Reason: CancellationUserRequested,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := newWorkerStopIntentFromSnapshot(
		start, cancelled.Snapshot, mustOperationID(t, "provider-tool-worker-stop"),
		WorkerStopCancellation, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.stop(context.Background(), stop); err != nil || backend.stopCount() != 1 {
		t.Fatalf("provider Tool Stop: err=%v calls=%d", err, backend.stopCount())
	}
}

func TestProviderCapableAgentWorkerInvocationBindsGatewayAndQuotaLineage(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 35, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "provider-agent-worker-authority", 7)
	initial, reservation, _, _ := providerGatewayFixture(t, now, authority, "provider-agent-worker")
	plan := validAgentPlanForStart(initial, "provider-agent-worker")
	input := initial.StartRuntimeRunInput
	input.WorkerClass = WorkerAgent
	input.CapabilityContractDigest = plan.CapabilityContractDigest
	start := mustStart(t, input)
	plan = validAgentPlanForStart(start, "provider-agent-worker")
	plan.ProviderRequired = true
	backend := newContractAgentWorkerBackend()
	adapter, err := newAgentWorkerCapabilityAdapter(plan, backend)
	if err != nil {
		t.Fatal(err)
	}
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = startNodeID(t, "provider-agent-worker-node"), 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, "provider-agent-worker")
	recovery := writableGatewayRecoveryAuthority(now)
	var harness *DeterministicHarness
	gateway, err := NewDeterministicGateway(func() time.Time {
		if harness == nil {
			return now
		}
		return harness.clock.current()
	})
	if err != nil {
		t.Fatal(err)
	}
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
		GatewayGrants: gateway, GatewayRecovery: recovery,
		GatewayCallAuthority: gatewayCallExternalAuthorityForStart(recovery, start),
		agentWorker:          adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare provider Agent Capsule: %+v err=%v", prepared, err)
	}
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	accept, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.workers.accept(context.Background(), accept); err != nil {
		t.Fatalf("provider Agent Accept: %v", err)
	}
	invocation := backend.lastAcceptInvocation()
	currentGrant := prepared.Snapshot.Gateway.CurrentGrant
	if invocation.GatewayGrantID != currentGrant.GatewayGrantID ||
		invocation.GatewayGrantGeneration != currentGrant.Generation ||
		invocation.GatewayGrantDigest != currentGrant.CanonicalDigest ||
		invocation.QuotaReservationID != start.ProviderBinding.QuotaReservationID ||
		invocation.QuotaReservationGeneration != start.ProviderBinding.Generation {
		t.Fatalf("provider Agent invocation lost Gateway/Quota lineage: invocation=%+v grant=%+v", invocation, currentGrant)
	}
}

func TestAgentAndToolWorkersShareLifecycleFenceEvidenceReplayAndSafeErrorContract(t *testing.T) {
	for _, test := range []struct {
		name  string
		class WorkerClass
	}{
		{name: "Agent", class: WorkerAgent},
		{name: "Tool", class: WorkerTool},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 31, 12, 40, 0, 0, time.UTC)
			var agentBackend *contractAgentWorkerBackend
			var toolBackend *contractToolWorkerBackend
			var control contractWorkerBackendControl
			if test.class == WorkerAgent {
				agentBackend = newContractAgentWorkerBackend()
				control = agentBackend
			} else {
				toolBackend = newContractToolWorkerBackend()
				control = toolBackend
			}
			harness, start, prepared := newReadyWorkerProtocolHarness(
				t, now, "shared-"+strings.ToLower(test.name), test.class, agentBackend, toolBackend,
			)
			delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
				RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
				Digest: prepared.Snapshot.Capsule.Digest,
			})
			if err != nil {
				t.Fatal(err)
			}
			accept, err := newWorkerAccept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			firstAck, err := harness.workers.accept(context.Background(), accept)
			if err != nil {
				t.Fatal(err)
			}
			replayedAck, err := harness.workers.accept(context.Background(), accept)
			if err != nil || replayedAck != firstAck || control.acceptCount() != 1 {
				t.Fatalf("shared Accept replay: %+v err=%v calls=%d", replayedAck, err, control.acceptCount())
			}

			starting, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			heartbeat, err := newWorkerHeartbeat(workerHeartbeatInput{
				SchemaVersion:       SchemaV1,
				OperationID:         mustOperationID(t, "shared-heartbeat-"+strings.ToLower(test.name)),
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
			heartbeatDecision, err := harness.workers.heartbeat(context.Background(), heartbeat)
			if err != nil || !validWorkerLeaseDecision(heartbeatDecision) {
				t.Fatalf("shared Heartbeat: %+v err=%v", heartbeatDecision, err)
			}
			heartbeatReplay, err := harness.workers.heartbeat(context.Background(), heartbeat)
			if err != nil || !heartbeatReplay.Replayed ||
				heartbeatReplay.CanonicalDigest != heartbeatDecision.CanonicalDigest {
				t.Fatalf("shared Heartbeat replay: %+v err=%v", heartbeatReplay, err)
			}
			staleHeartbeat := heartbeat
			staleHeartbeat.OperationID = mustOperationID(t, "shared-heartbeat-stale-"+strings.ToLower(test.name))
			staleHeartbeat.CanonicalDigest = canonicalWorkerHeartbeatDigest(staleHeartbeat)
			if _, err := harness.workers.heartbeat(context.Background(), staleHeartbeat); errorCode(err) != ErrorIntegrityConflict {
				t.Fatalf("shared stale Heartbeat: %v", err)
			}
			starting, err = harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil || starting.Lease != heartbeatDecision.Lease {
				t.Fatalf("shared renewed lease projection: %+v err=%v", starting.Lease, err)
			}
			observe, err := newWorkerObserve(workerOperationRefFromSnapshot(start, starting), initialWorkerCursor(starting))
			if err != nil {
				t.Fatal(err)
			}
			canary := "raw-provider-error-secret-prompt-host-path"
			control.failNextObserve(errors.New(canary))
			control.enqueueObservation(contractWorkerObservation{Kind: WorkerObservedRunning, ObservedAt: now})
			_, err = harness.workers.observe(context.Background(), observe)
			if errorCode(err) != ErrorDependencyUnavailable || strings.Contains(err.Error(), canary) {
				t.Fatalf("unsafe backend error escaped: %v", err)
			}
			runningResult, err := harness.workers.observe(context.Background(), observe)
			if err != nil || runningResult.Disposition != WorkerObservationAccepted {
				t.Fatalf("running after safe retry: %+v err=%v", runningResult, err)
			}
			runningReplay, err := harness.workers.observe(context.Background(), observe)
			if err != nil || !runningReplay.Replayed || runningReplay.Observation != runningResult.Observation ||
				control.observeCount() != 2 {
				t.Fatalf("shared Observe replay: %+v err=%v calls=%d", runningReplay, err, control.observeCount())
			}

			running, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil {
				t.Fatal(err)
			}
			control.enqueueObservation(contractWorkerObservation{
				Kind: WorkerObservedFailed, ObservedAt: now,
				EvidenceID:     mustEvidenceID(t, "shared-failure-evidence-"+strings.ToLower(test.name)),
				EvidenceDigest: digest(245), InternalCallCount: 3, SafeFailure: WorkerFailureCapability,
			})
			terminalRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, running), running.Worker.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := harness.workers.observe(context.Background(), terminalRequest)
			if err != nil || terminal.Observation.SafeFailure != WorkerFailureCapability {
				t.Fatalf("typed failure observation: %+v err=%v", terminal, err)
			}
			candidate, err := harness.Runtime.Inspect(context.Background(), runtimeRef(start, start.Authority))
			if err != nil || candidate.State != RuntimeReconciling || candidate.Outcome != RuntimeOutcomeNone ||
				candidate.Worker.EvidenceCandidate.OutputContractDigest != start.OutputContractDigest ||
				candidate.Worker.EvidenceCandidate.EvidenceContractDigest != start.EvidenceContractDigest ||
				candidate.Worker.EvidenceCandidate.LeaseFence != candidate.Lease.Fence ||
				candidate.EvidenceRoot != (EvidenceRootSnapshot{}) {
				t.Fatalf("shared evidence candidate crossed authority: %+v err=%v", candidate, err)
			}

			lateRequest, err := newWorkerObserve(workerOperationRefFromSnapshot(start, candidate), candidate.Worker.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
				SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "shared-cancel-"+strings.ToLower(test.name)),
				PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
				PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
				ExpectedRuntimeRevision: candidate.RuntimeRevision, ExpectedStartOperationID: start.OperationID,
				ExpectedOperationGeneration: candidate.Operation.Generation, ExpectedRuntimeFence: candidate.RuntimeFence,
				Authority: start.Authority, Reason: CancellationUserRequested,
				SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			cancelled, err := harness.Runtime.Execute(context.Background(), cancel)
			if err != nil {
				t.Fatal(err)
			}
			beforeLateCalls := control.observeCount()
			_, err = harness.workers.observe(context.Background(), lateRequest)
			if errorCode(err) != ErrorAuthorizationDenied || control.observeCount() != beforeLateCalls {
				t.Fatalf("late observation crossed cancel fence: err=%v calls=%d", err, control.observeCount())
			}

			stop, err := newWorkerStopIntentFromSnapshot(
				start, cancelled.Snapshot, mustOperationID(t, "shared-stop-"+strings.ToLower(test.name)),
				WorkerStopCancellation, now.Add(time.Minute),
			)
			if err != nil {
				t.Fatal(err)
			}
			stopAck, err := harness.workers.stop(context.Background(), stop)
			if err != nil {
				t.Fatal(err)
			}
			stopReplay, err := harness.workers.stop(context.Background(), stop)
			if err != nil || stopReplay != stopAck || control.stopCount() != 1 {
				t.Fatalf("shared Stop replay: %+v err=%v calls=%d", stopReplay, err, control.stopCount())
			}
		})
	}
}

func TestToolWorkerFailsClosedForCapabilityEntrypointParameterSchemaAndGatewayMismatch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 12, 55, 0, 0, time.UTC)
	backend := newContractToolWorkerBackend()
	harness, start, prepared := newReadyWorkerProtocolHarness(t, now, "tool-negative", WorkerTool, nil, backend)
	delivery, err := harness.Dispatch.ClaimDispatch(context.Background(), DispatchClaimRequest{
		RuntimeRunID: start.RuntimeRunID, CapsuleID: prepared.Snapshot.Capsule.CapsuleID,
		Digest: prepared.Snapshot.Capsule.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := newWorkerAccept(delivery)
	if err != nil {
		t.Fatal(err)
	}
	engine := harness.workers.(*invariantEngine)

	wrongCapability := validToolPlanForStart(start)
	wrongCapability.CapabilityKey = ToolCapabilityMediaInspect
	wrongCapability.Parameters.Kind = ToolParametersMediaInspect
	if _, err := newToolWorkerCapabilityAdapter(wrongCapability, backend); errorCode(err) != ErrorInvalidRequest {
		t.Fatalf("capability key substitution retained copied contract digest: %v", err)
	}

	wrongOptions := validToolPlanForStart(start)
	wrongOptions.Parameters.OptionsDigest = digest(220)
	if _, err := newToolWorkerCapabilityAdapter(wrongOptions, backend); errorCode(err) != ErrorInvalidRequest {
		t.Fatalf("Tool options substitution retained copied contract digest: %v", err)
	}

	wrongEntrypoint := validToolPlanForStart(start)
	wrongEntrypoint.EntrypointDigest = digest(221)
	if _, err := newToolWorkerCapabilityAdapter(wrongEntrypoint, backend); errorCode(err) != ErrorInvalidRequest {
		t.Fatalf("wrong private entrypoint accepted: %v", err)
	}
	wrongParameters := validToolPlanForStart(start)
	wrongParameters.Parameters.Kind = ToolParametersMediaInspect
	if _, err := newToolWorkerCapabilityAdapter(wrongParameters, backend); errorCode(err) != ErrorInvalidRequest {
		t.Fatalf("capability/parameter schema mismatch accepted: %v", err)
	}

	providerRequired := validToolPlanForStart(start)
	providerRequired.ProviderRequired = true
	engine.toolWorker, err = newToolWorkerCapabilityAdapter(providerRequired, backend)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.workers.accept(context.Background(), command)
	if errorCode(err) != ErrorAuthorizationDenied || backend.acceptCount() != 0 {
		t.Fatalf("provider-capable Tool bypassed Gateway lineage: err=%v calls=%d", err, backend.acceptCount())
	}

	unknownSchema := command
	unknownSchema.SchemaVersion = NewSchemaVersion(2, 0)
	unknownSchema.CanonicalDigest = canonicalWorkerAcceptDigest(unknownSchema)
	_, err = harness.workers.accept(context.Background(), unknownSchema)
	if errorCode(err) != ErrorIntegrityConflict || backend.acceptCount() != 0 {
		t.Fatalf("unknown worker protocol major did not fail closed: err=%v calls=%d", err, backend.acceptCount())
	}
}

func startCatalogSafetyEpoch(start StartRuntimeRun) CatalogSafetyEpoch {
	if start.CatalogBinding == nil {
		return 0
	}
	return start.CatalogBinding.SafetyEpoch
}

type permittedFieldType struct {
	typeOf reflect.Type
	count  int
}

func assertExactStructFieldTypes(t *testing.T, envelope reflect.Type, permitted []permittedFieldType) {
	t.Helper()
	if envelope.Kind() != reflect.Struct {
		t.Fatalf("worker protocol envelope kind=%v, want struct", envelope.Kind())
	}
	want := make(map[reflect.Type]int, len(permitted))
	for _, field := range permitted {
		if field.typeOf == nil || field.count <= 0 || want[field.typeOf] != 0 {
			t.Fatalf("invalid closed field-type allowlist for %s: %+v", envelope, permitted)
		}
		want[field.typeOf] = field.count
	}
	got := make(map[reflect.Type]int, len(want))
	for index := 0; index < envelope.NumField(); index++ {
		field := envelope.Field(index)
		if field.Anonymous || want[field.Type] == 0 {
			t.Fatalf("worker protocol envelope %s has unpermitted structural field type %s", envelope, field.Type)
		}
		got[field.Type]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worker protocol envelope %s field-type multiset=%v, want %v", envelope, got, want)
	}
}

func assertExactInterfaceSignatures(t *testing.T, backend reflect.Type, permitted []reflect.Type) {
	t.Helper()
	if backend.Kind() != reflect.Interface {
		t.Fatalf("worker backend kind=%v, want interface", backend.Kind())
	}
	want := make(map[reflect.Type]int, len(permitted))
	for _, signature := range permitted {
		if signature == nil || signature.Kind() != reflect.Func || want[signature] != 0 {
			t.Fatalf("invalid closed method-signature allowlist for %s: %v", backend, permitted)
		}
		want[signature] = 1
	}
	got := make(map[reflect.Type]int, len(want))
	for index := 0; index < backend.NumMethod(); index++ {
		signature := backend.Method(index).Type
		if want[signature] == 0 {
			t.Fatalf("worker backend %s has unpermitted method signature %s", backend, signature)
		}
		got[signature]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worker backend %s method-signature multiset=%v, want %v", backend, got, want)
	}
}

type contractAgentWorkerBackend struct {
	mu               sync.Mutex
	accepts          int
	observes         int
	observations     []contractWorkerObservation
	stops            int
	nextObserveError error
	lastAccept       agentCapabilityInvocation
}

type contractWorkerBackendControl interface {
	enqueueObservation(contractWorkerObservation)
	failNextObserve(error)
	acceptCount() int
	observeCount() int
	stopCount() int
}

type contractWorkerObservation struct {
	ObservationID     WorkerObservationID
	Kind              WorkerObservationKind
	StreamGeneration  uint64
	Position          uint64
	ObservedAt        time.Time
	EvidenceID        EvidenceID
	EvidenceDigest    Digest
	InternalCallCount uint64
	SafeFailure       WorkerSafeFailure
}

func newContractAgentWorkerBackend() *contractAgentWorkerBackend {
	return &contractAgentWorkerBackend{}
}

func (backend *contractAgentWorkerBackend) acceptAgent(
	_ context.Context,
	invocation agentCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.accepts++
	backend.lastAccept = invocation
	if invocation.RuntimeRunID != command.RuntimeRunID || invocation.CapsuleID != command.CapsuleID ||
		invocation.CapsuleDigest != command.CapsuleDigest || invocation.LeaseGeneration != command.LeaseGeneration ||
		invocation.LeaseFence != command.LeaseFence {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	return newWorkerOperationAck(command), nil
}

func (backend *contractAgentWorkerBackend) lastAcceptInvocation() agentCapabilityInvocation {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.lastAccept
}

func (backend *contractAgentWorkerBackend) acceptCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.accepts
}

func (backend *contractAgentWorkerBackend) enqueueObservation(observation contractWorkerObservation) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.observations = append(backend.observations, observation)
}

func (backend *contractAgentWorkerBackend) observeAgent(
	_ context.Context,
	invocation agentCapabilityInvocation,
	request workerObserve,
) (workerBackendObservation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.observes++
	if backend.nextObserveError != nil {
		err := backend.nextObserveError
		backend.nextObserveError = nil
		return workerBackendObservation{}, err
	}
	if invocation.RuntimeRunID != request.Ref.RuntimeRunID || invocation.CapsuleID != request.Ref.CapsuleID ||
		invocation.CapsuleDigest != request.Ref.CapsuleDigest || invocation.LeaseGeneration != request.Ref.LeaseGeneration ||
		invocation.LeaseFence != request.Ref.LeaseFence || len(backend.observations) == 0 {
		return workerBackendObservation{}, newError(ErrorIntegrityConflict)
	}
	next := backend.observations[0]
	backend.observations = backend.observations[1:]
	return workerBackendObservation{
		ObservationID: next.ObservationID, Kind: next.Kind, StreamGeneration: next.StreamGeneration,
		Position: next.Position, ObservedAt: next.ObservedAt, EvidenceID: next.EvidenceID,
		EvidenceDigest: next.EvidenceDigest, InternalCallCount: next.InternalCallCount,
		SafeFailure: next.SafeFailure,
	}, nil
}

func (backend *contractAgentWorkerBackend) failNextObserve(err error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.nextObserveError = err
}

func (backend *contractAgentWorkerBackend) observeCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.observes
}

func (backend *contractAgentWorkerBackend) stopAgent(
	_ context.Context,
	invocation agentCapabilityInvocation,
	intent workerStopIntent,
) (workerStopAck, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.stops++
	if invocation.RuntimeRunID != intent.RuntimeRunID || invocation.CapsuleID != intent.CapsuleID ||
		invocation.CapsuleDigest != intent.CapsuleDigest || invocation.LeaseGeneration != intent.LeaseGeneration ||
		invocation.LeaseFence != intent.LeaseFence {
		return workerStopAck{}, newError(ErrorIntegrityConflict)
	}
	return newWorkerStopAck(intent), nil
}

func (backend *contractAgentWorkerBackend) stopCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.stops
}

type contractToolWorkerBackend struct {
	mu               sync.Mutex
	accepts          int
	observes         int
	stops            int
	observations     []contractWorkerObservation
	nextObserveError error
	beforeAccept     func()
	beforeObserve    func()
	beforeStop       func()
}

func newContractToolWorkerBackend() *contractToolWorkerBackend { return &contractToolWorkerBackend{} }

func (backend *contractToolWorkerBackend) acceptTool(
	_ context.Context,
	invocation toolCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.accepts++
	if backend.beforeAccept != nil {
		beforeAccept := backend.beforeAccept
		backend.beforeAccept = nil
		beforeAccept()
	}
	if invocation.RuntimeRunID != command.RuntimeRunID || invocation.CapsuleID != command.CapsuleID ||
		invocation.CapsuleDigest != command.CapsuleDigest || invocation.LeaseGeneration != command.LeaseGeneration ||
		invocation.LeaseFence != command.LeaseFence {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	return newWorkerOperationAck(command), nil
}

func (backend *contractToolWorkerBackend) beforeNextAccept(before func()) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.beforeAccept = before
}

func (backend *contractToolWorkerBackend) observeTool(
	_ context.Context,
	invocation toolCapabilityInvocation,
	request workerObserve,
) (workerBackendObservation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.observes++
	if backend.beforeObserve != nil {
		beforeObserve := backend.beforeObserve
		backend.beforeObserve = nil
		beforeObserve()
	}
	if backend.nextObserveError != nil {
		err := backend.nextObserveError
		backend.nextObserveError = nil
		return workerBackendObservation{}, err
	}
	if invocation.RuntimeRunID != request.Ref.RuntimeRunID || invocation.CapsuleID != request.Ref.CapsuleID ||
		invocation.CapsuleDigest != request.Ref.CapsuleDigest || invocation.LeaseGeneration != request.Ref.LeaseGeneration ||
		invocation.LeaseFence != request.Ref.LeaseFence || len(backend.observations) == 0 {
		return workerBackendObservation{}, newError(ErrorIntegrityConflict)
	}
	next := backend.observations[0]
	backend.observations = backend.observations[1:]
	return workerBackendObservation{
		ObservationID: next.ObservationID, Kind: next.Kind, StreamGeneration: next.StreamGeneration,
		Position: next.Position, ObservedAt: next.ObservedAt, EvidenceID: next.EvidenceID,
		EvidenceDigest: next.EvidenceDigest, InternalCallCount: next.InternalCallCount,
		SafeFailure: next.SafeFailure,
	}, nil
}

func (backend *contractToolWorkerBackend) beforeNextObserve(before func()) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.beforeObserve = before
}

func (backend *contractToolWorkerBackend) stopTool(
	_ context.Context,
	invocation toolCapabilityInvocation,
	intent workerStopIntent,
) (workerStopAck, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.stops++
	if backend.beforeStop != nil {
		beforeStop := backend.beforeStop
		backend.beforeStop = nil
		beforeStop()
	}
	if invocation.RuntimeRunID != intent.RuntimeRunID || invocation.CapsuleID != intent.CapsuleID ||
		invocation.CapsuleDigest != intent.CapsuleDigest || invocation.LeaseGeneration != intent.LeaseGeneration ||
		invocation.LeaseFence != intent.LeaseFence {
		return workerStopAck{}, newError(ErrorIntegrityConflict)
	}
	return newWorkerStopAck(intent), nil
}

func (backend *contractToolWorkerBackend) beforeNextStop(before func()) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.beforeStop = before
}

func (backend *contractToolWorkerBackend) acceptCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.accepts
}

func (backend *contractToolWorkerBackend) enqueueObservation(observation contractWorkerObservation) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.observations = append(backend.observations, observation)
}

func (backend *contractToolWorkerBackend) failNextObserve(err error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.nextObserveError = err
}

func (backend *contractToolWorkerBackend) observeCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.observes
}

func (backend *contractToolWorkerBackend) stopCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.stops
}

func newReadyWorkerProtocolHarness(
	t *testing.T,
	now time.Time,
	suffix string,
	class WorkerClass,
	agentBackend agentWorkerBackend,
	toolBackend toolWorkerBackend,
) (*DeterministicHarness, StartRuntimeRun, RuntimeDecision) {
	t.Helper()
	authority := mustTaskOrchestrationAuthority(t, suffix+"-authority", 7)
	start := standardStart(t, now, authority, suffix)
	if class != start.WorkerClass {
		input := start.StartRuntimeRunInput
		input.WorkerClass = class
		var err error
		start, err = NewStartRuntimeRun(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	if class == WorkerAgent {
		input := start.StartRuntimeRunInput
		input.CapabilityContractDigest = validAgentPlanForStart(start, suffix).CapabilityContractDigest
		var err error
		start, err = NewStartRuntimeRun(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = startNodeID(t, suffix+"-node"), 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	var agentAdapter workerCapabilityAdapter
	var toolAdapter workerCapabilityAdapter
	if class == WorkerAgent {
		var err error
		agentAdapter, err = newAgentWorkerCapabilityAdapter(validAgentPlanForStart(start, suffix), agentBackend)
		if err != nil {
			t.Fatal(err)
		}
	}
	if class == WorkerTool {
		var err error
		toolAdapter, err = newToolWorkerCapabilityAdapter(validToolPlanForStart(start), toolBackend)
		if err != nil {
			t.Fatal(err)
		}
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grant}, Nodes: []ExecutionNodeFixture{node},
		MaintenanceAuthorities: []RuntimeMaintenanceAuthorityBinding{
			BindLeaseFencingAuthority(node.ExecutionNodeID, workerTestFencingAuthority(t, suffix)),
		},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: acceptedRuntimeBindingValidatorForTest(t),
		ImmutableInputValidator: ImmutableInputValidatorFunc(func(
			context.Context, ImmutableInputValidationRequest,
		) (PrerequisiteObservation, error) {
			return acceptedPrerequisiteObservation(t, suffix+"-input-evidence", digest(242)), nil
		}),
		agentWorker: agentAdapter, toolWorker: toolAdapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !accepted.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare worker Capsule: %+v err=%v", accepted, err)
	}
	return harness, start, accepted
}

func workerTestFencingAuthority(t *testing.T, suffix string) LeaseFencingAuthority {
	t.Helper()
	return NewSecurityLeaseFencingAuthority(mustAuthorityID(t, suffix+"-worker-fencing"), 3)
}

func newReadyProviderToolWorkerProtocolHarness(
	t *testing.T,
	now time.Time,
	suffix string,
	backend toolWorkerBackend,
) (*DeterministicHarness, StartRuntimeRun, RuntimeDecision) {
	t.Helper()
	authority := mustTaskOrchestrationAuthority(t, suffix+"-authority", 7)
	start, reservation, admission, node := providerGatewayFixture(t, now, authority, suffix)
	plan := validToolPlanForStart(start)
	plan.ProviderRequired = true
	adapter, err := newToolWorkerCapabilityAdapter(plan, backend)
	if err != nil {
		t.Fatal(err)
	}
	bindingValidator, inputValidator := acceptedGatewayPrerequisiteValidators(t, suffix)
	recovery := writableGatewayRecoveryAuthority(now)
	var harness *DeterministicHarness
	gateway, err := NewDeterministicGateway(func() time.Time {
		if harness == nil {
			return now
		}
		return harness.clock.current()
	})
	if err != nil {
		t.Fatal(err)
	}
	harness, err = NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 1, LeaseStart: 1, SandboxStart: 1},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{admission}, QuotaReservations: []QuotaReservationFixture{reservation},
		Nodes: []ExecutionNodeFixture{node},
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context, LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: bindingValidator, ImmutableInputValidator: inputValidator,
		GatewayGrants: gateway, GatewayRecovery: recovery,
		GatewayCallAuthority: gatewayCallExternalAuthorityForStart(recovery, start),
		toolWorker:           adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || !prepared.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("prepare provider Tool Capsule: %+v err=%v", prepared, err)
	}
	return harness, start, prepared
}

func validToolPlanForStart(start StartRuntimeRun) toolCapabilityPlan {
	parameters := toolTypedParameters{
		SchemaVersion: SchemaV1, Kind: ToolParametersDocumentRender,
		InputManifestIdentity: start.ImmutableInputManifest.Identity, OptionsDigest: digest(244),
	}
	parameters.CanonicalDigest = canonicalToolParametersDigest(parameters)
	plan := toolCapabilityPlan{
		RuntimeBindingID: start.RuntimeBindingID, RuntimeBindingDigest: start.RuntimeBindingDigest,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest,
		CapabilityKey:               ToolCapabilityDocumentRender,
		Parameters:                  parameters,
		EntrypointDigest:            digestBytes([]byte("actual-executor")),
		ActualImageDigest:           digestBytes([]byte("actual-image")), ActualExecutorDigest: digestBytes([]byte("actual-executor")),
	}
	plan.CapabilityContractDigest = canonicalToolCapabilityContractDigest(plan)
	return plan
}

func validAgentPlanForStart(start StartRuntimeRun, suffix string) agentCapabilityPlan {
	plan := agentCapabilityPlan{
		RuntimeBindingID: start.RuntimeBindingID, RuntimeBindingDigest: start.RuntimeBindingDigest,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest,
		IntentReference:             AgentIntentReference{value: suffix + "-agent-intent"},
		PromptReference:             AgentPromptReference{value: suffix + "-prompt-reference"},
		EntrypointDigest:            digestBytes([]byte("actual-executor")),
		ActualImageDigest:           digestBytes([]byte("actual-image")),
		ActualExecutorDigest:        digestBytes([]byte("actual-executor")),
	}
	plan.CapabilityContractDigest = canonicalAgentCapabilityContractDigest(plan)
	return plan
}
