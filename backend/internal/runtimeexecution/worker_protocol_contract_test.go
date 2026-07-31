package runtimeexecution

import (
	"context"
	"errors"
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
	replayed, err := harness.workers.heartbeat(context.Background(), heartbeat)
	if err != nil || !replayed.Replayed || replayed.Lease != first.Lease {
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

	parameterType := reflect.TypeOf(toolTypedParameters{})
	for index := 0; index < parameterType.NumField(); index++ {
		field := parameterType.Field(index)
		name := strings.ToLower(field.Name)
		if field.Type.Kind() == reflect.String || strings.Contains(name, "shell") || strings.Contains(name, "command") ||
			strings.Contains(name, "exec") || strings.Contains(name, "path") || strings.Contains(name, "host") {
			t.Fatalf("arbitrary execution surface leaked into typed Tool parameters: %s %s", field.Name, field.Type)
		}
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
	wrongCapability.CapabilityContractDigest = digest(220)
	engine.toolWorker, err = newToolWorkerCapabilityAdapter(wrongCapability, backend)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.workers.accept(context.Background(), command)
	if errorCode(err) != ErrorAuthorizationDenied || backend.acceptCount() != 0 {
		t.Fatalf("wrong capability reached Tool backend: err=%v calls=%d", err, backend.acceptCount())
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

type contractAgentWorkerBackend struct {
	mu               sync.Mutex
	accepts          int
	observes         int
	observations     []contractWorkerObservation
	stops            int
	nextObserveError error
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
	if invocation.RuntimeRunID != command.RuntimeRunID || invocation.CapsuleID != command.CapsuleID ||
		invocation.CapsuleDigest != command.CapsuleDigest || invocation.LeaseGeneration != command.LeaseGeneration ||
		invocation.LeaseFence != command.LeaseFence {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	return newWorkerOperationAck(command), nil
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
	if invocation.RuntimeRunID != command.RuntimeRunID || invocation.CapsuleID != command.CapsuleID ||
		invocation.CapsuleDigest != command.CapsuleDigest || invocation.LeaseGeneration != command.LeaseGeneration ||
		invocation.LeaseFence != command.LeaseFence {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}
	return newWorkerOperationAck(command), nil
}

func (backend *contractToolWorkerBackend) observeTool(
	_ context.Context,
	invocation toolCapabilityInvocation,
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

func (backend *contractToolWorkerBackend) stopTool(
	_ context.Context,
	invocation toolCapabilityInvocation,
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
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID, grant.NodeCapacityGeneration = startNodeID(t, suffix+"-node"), 1
	node := executionNodeFixtureForStart(t, start, grant, now)
	var agentAdapter workerCapabilityAdapter
	var toolAdapter workerCapabilityAdapter
	if class == WorkerAgent {
		var err error
		agentAdapter, err = newAgentWorkerCapabilityAdapter(agentCapabilityPlan{
			RuntimeBindingID: start.RuntimeBindingID, RuntimeBindingDigest: start.RuntimeBindingDigest,
			CapabilityContractDigest:    start.CapabilityContractDigest,
			AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
			ExecutorContractDigest:      start.ExecutorContractDigest,
			IntentReference:             AgentIntentReference{value: suffix + "-agent-intent"},
			PromptReference:             AgentPromptReference{value: suffix + "-prompt-reference"},
			EntrypointDigest:            digestBytes([]byte("actual-executor")), ActualImageDigest: digestBytes([]byte("actual-image")),
			ActualExecutorDigest: digestBytes([]byte("actual-executor")),
		}, agentBackend)
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

func validToolPlanForStart(start StartRuntimeRun) toolCapabilityPlan {
	return toolCapabilityPlan{
		RuntimeBindingID: start.RuntimeBindingID, RuntimeBindingDigest: start.RuntimeBindingDigest,
		CapabilityContractDigest:    start.CapabilityContractDigest,
		AllowedPlatformImagesDigest: start.AllowedPlatformImagesDigest,
		ExecutorContractDigest:      start.ExecutorContractDigest,
		CapabilityKey:               ToolCapabilityDocumentRender,
		Parameters: toolTypedParameters{
			SchemaVersion: SchemaV1, Kind: ToolParametersDocumentRender,
			InputManifestIdentity: start.ImmutableInputManifest.Identity, OptionsDigest: digest(244),
		},
		EntrypointDigest:  digestBytes([]byte("actual-executor")),
		ActualImageDigest: digestBytes([]byte("actual-image")), ActualExecutorDigest: digestBytes([]byte("actual-executor")),
	}
}
