package runtimeexecution

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
)

// workerProtocol is the private C03-owned worker seam. It is deliberately
// unexported and is not a RuntimeCommand family.
type workerProtocol interface {
	accept(context.Context, workerAccept) (workerOperationAck, error)
	heartbeat(context.Context, workerHeartbeat) (workerLeaseDecision, error)
	observe(context.Context, workerObserve) (workerObservationResult, error)
	stop(context.Context, workerStopIntent) (workerStopAck, error)
}

type WorkerOperationStatus uint8

const (
	WorkerOperationNone WorkerOperationStatus = iota
	WorkerOperationAccepted
	WorkerOperationRunning
	WorkerOperationSuccessObserved
	WorkerOperationFailureObserved
)

type WorkerOperationAckID struct{ value string }

func (id WorkerOperationAckID) String() string { return id.value }

// RuntimeWorkerSnapshot is a content-free projection of private worker facts.
// It never grants the worker Runtime, Task, lease, commit, or publication
// authority.
type RuntimeWorkerSnapshot struct {
	Status                  WorkerOperationStatus
	WorkerClass             WorkerClass
	AcceptOperationID       OperationID
	OperationAckID          WorkerOperationAckID
	OperationAckDigest      Digest
	WorkerAuthorityID       WorkerAuthorityID
	WorkerGeneration        WorkerGeneration
	NodeAuthorityID         NodeAuthorityID
	ExecutionNodeID         ExecutionNodeID
	NodeGeneration          NodeGeneration
	CapsuleID               ExecutionCapsuleID
	CapsuleDigest           Digest
	AcceptedRuntimeFence    RuntimeFence
	AcceptedLeaseGeneration LeaseGeneration
	AcceptedLeaseFence      LeaseFence
	Cursor                  WorkerCursorSnapshot
	LastObservationID       WorkerObservationID
	LastObservationDigest   Digest
	LastObservationKind     WorkerObservationKind
	EvidenceCandidate       WorkerEvidenceCandidateSnapshot
	SafeFailure             WorkerSafeFailure
	Stop                    WorkerStopSnapshot
}

type workerAccept struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	RuntimeRunID            RuntimeRunID
	CapsuleID               ExecutionCapsuleID
	CapsuleDigest           Digest
	Capsule                 []byte
	WorkerClass             WorkerClass
	WorkerAuthorityID       WorkerAuthorityID
	WorkerGeneration        WorkerGeneration
	NodeAuthorityID         NodeAuthorityID
	ExecutionNodeID         ExecutionNodeID
	NodeGeneration          NodeGeneration
	SandboxLeaseID          SandboxLeaseID
	LeaseGeneration         LeaseGeneration
	LeaseFence              LeaseFence
	RuntimeFence            RuntimeFence
	AuthorizationGeneration AuthorizationGeneration
	ReleaseSafetyEpoch      ReleaseSafetyEpoch
	CatalogSafetyEpoch      CatalogSafetyEpoch
	Deadline                time.Time
	CanonicalDigest         Digest
}

func newWorkerAccept(delivery DispatchDelivery) (workerAccept, error) {
	if delivery.OperationID == (OperationID{}) || delivery.RuntimeRunID == (RuntimeRunID{}) ||
		delivery.CapsuleID == (ExecutionCapsuleID{}) || delivery.CapsuleDigest == (Digest{}) ||
		len(delivery.Capsule) == 0 || digestBytes(delivery.Capsule) != delivery.CapsuleDigest {
		return workerAccept{}, newError(ErrorInvalidRequest)
	}
	capsule, err := decodeExecutionCapsule(delivery.Capsule)
	if err != nil {
		return workerAccept{}, err
	}
	command := workerAccept{
		SchemaVersion: SchemaV1, OperationID: delivery.OperationID, RuntimeRunID: delivery.RuntimeRunID,
		CapsuleID: delivery.CapsuleID, CapsuleDigest: delivery.CapsuleDigest,
		Capsule: append([]byte(nil), delivery.Capsule...), WorkerClass: capsule.WorkerClass,
		WorkerAuthorityID: capsule.WorkerAuthorityID, WorkerGeneration: capsule.WorkerGeneration,
		NodeAuthorityID: capsule.NodeAuthorityID, ExecutionNodeID: capsule.ExecutionNodeID,
		NodeGeneration: capsule.NodeGeneration, SandboxLeaseID: capsule.SandboxLeaseID,
		LeaseGeneration: capsule.LeaseGeneration, LeaseFence: capsule.LeaseFence,
		RuntimeFence: capsule.RuntimeFence, AuthorizationGeneration: capsule.AuthorizationGeneration,
		ReleaseSafetyEpoch: capsule.ReleaseSafetyEpoch, CatalogSafetyEpoch: capsule.CatalogSafetyEpoch,
		Deadline: capsule.Deadline.UTC(),
	}
	command.CanonicalDigest = canonicalWorkerAcceptDigest(command)
	if !validWorkerAccept(command, capsule) {
		return workerAccept{}, newError(ErrorIntegrityConflict)
	}
	return command, nil
}

func canonicalWorkerAcceptDigest(command workerAccept) Digest {
	parts := []string{
		"slidesmith.runtime-execution.worker-accept/v1", fmt.Sprint(command.SchemaVersion),
		command.OperationID.String(), command.RuntimeRunID.String(), command.CapsuleID.String(),
		command.CapsuleDigest.String(), fmt.Sprint(command.WorkerClass), command.WorkerAuthorityID.String(),
		fmt.Sprint(command.WorkerGeneration), command.NodeAuthorityID.String(), command.ExecutionNodeID.String(),
		fmt.Sprint(command.NodeGeneration), command.SandboxLeaseID.String(), fmt.Sprint(command.LeaseGeneration),
		fmt.Sprint(command.LeaseFence), fmt.Sprint(command.RuntimeFence), fmt.Sprint(command.AuthorizationGeneration),
		fmt.Sprint(command.ReleaseSafetyEpoch), fmt.Sprint(command.CatalogSafetyEpoch),
		command.Deadline.UTC().Format(time.RFC3339Nano),
	}
	return digestBytes([]byte(strings.Join(parts, "\n")))
}

func validWorkerAccept(command workerAccept, capsule executionCapsule) bool {
	return command.SchemaVersion == SchemaV1 && validOpaqueID(command.OperationID.String()) &&
		validOpaqueID(command.RuntimeRunID.String()) && validOpaqueID(command.CapsuleID.String()) &&
		command.CapsuleDigest != (Digest{}) && len(command.Capsule) > 0 &&
		digestBytes(command.Capsule) == command.CapsuleDigest && command.CanonicalDigest == canonicalWorkerAcceptDigest(command) &&
		command.CapsuleID == capsule.CapsuleID && command.RuntimeRunID == capsule.RuntimeRunID &&
		command.WorkerClass == capsule.WorkerClass && command.WorkerAuthorityID == capsule.WorkerAuthorityID &&
		command.WorkerGeneration == capsule.WorkerGeneration && command.NodeAuthorityID == capsule.NodeAuthorityID &&
		command.ExecutionNodeID == capsule.ExecutionNodeID && command.NodeGeneration == capsule.NodeGeneration &&
		command.SandboxLeaseID == capsule.SandboxLeaseID && command.LeaseGeneration == capsule.LeaseGeneration &&
		command.LeaseFence == capsule.LeaseFence && command.RuntimeFence == capsule.RuntimeFence &&
		command.AuthorizationGeneration == capsule.AuthorizationGeneration &&
		command.ReleaseSafetyEpoch == capsule.ReleaseSafetyEpoch && command.CatalogSafetyEpoch == capsule.CatalogSafetyEpoch &&
		command.Deadline.Equal(capsule.Deadline)
}

type workerOperationAck struct {
	SchemaVersion     SchemaVersion
	OperationID       OperationID
	OperationAckID    WorkerOperationAckID
	RuntimeRunID      RuntimeRunID
	CapsuleID         ExecutionCapsuleID
	CapsuleDigest     Digest
	WorkerClass       WorkerClass
	WorkerAuthorityID WorkerAuthorityID
	WorkerGeneration  WorkerGeneration
	NodeAuthorityID   NodeAuthorityID
	ExecutionNodeID   ExecutionNodeID
	DurablyAccepted   bool
	CanonicalDigest   Digest
}

func newWorkerOperationAck(command workerAccept) workerOperationAck {
	ackMaterial := digestBytes([]byte("slidesmith.runtime-execution.worker-operation-ack-id/v1\n" +
		command.OperationID.String() + "\n" + command.CapsuleDigest.String()))
	ack := workerOperationAck{
		SchemaVersion: SchemaV1, OperationID: command.OperationID,
		OperationAckID: WorkerOperationAckID{value: fmt.Sprintf("worker-operation-ack-%x", ackMaterial[:12])},
		RuntimeRunID:   command.RuntimeRunID, CapsuleID: command.CapsuleID, CapsuleDigest: command.CapsuleDigest,
		WorkerClass: command.WorkerClass, WorkerAuthorityID: command.WorkerAuthorityID,
		WorkerGeneration: command.WorkerGeneration, NodeAuthorityID: command.NodeAuthorityID,
		ExecutionNodeID: command.ExecutionNodeID, DurablyAccepted: true,
	}
	ack.CanonicalDigest = canonicalWorkerOperationAckDigest(ack)
	return ack
}

func canonicalWorkerOperationAckDigest(ack workerOperationAck) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.worker-operation-ack/v1", fmt.Sprint(ack.SchemaVersion),
		ack.OperationID.String(), ack.OperationAckID.String(), ack.RuntimeRunID.String(), ack.CapsuleID.String(),
		ack.CapsuleDigest.String(), fmt.Sprint(ack.WorkerClass), ack.WorkerAuthorityID.String(),
		fmt.Sprint(ack.WorkerGeneration), ack.NodeAuthorityID.String(), ack.ExecutionNodeID.String(),
		fmt.Sprint(ack.DurablyAccepted),
	}, "\n")))
}

func validWorkerOperationAck(command workerAccept, ack workerOperationAck) bool {
	return ack.SchemaVersion == SchemaV1 && ack.OperationID == command.OperationID &&
		validOpaqueID(ack.OperationAckID.String()) && ack.RuntimeRunID == command.RuntimeRunID &&
		ack.CapsuleID == command.CapsuleID && ack.CapsuleDigest == command.CapsuleDigest &&
		ack.WorkerClass == command.WorkerClass && ack.WorkerAuthorityID == command.WorkerAuthorityID &&
		ack.WorkerGeneration == command.WorkerGeneration && ack.NodeAuthorityID == command.NodeAuthorityID &&
		ack.ExecutionNodeID == command.ExecutionNodeID && ack.DurablyAccepted &&
		ack.CanonicalDigest == canonicalWorkerOperationAckDigest(ack)
}

type workerCapabilityAdapter interface {
	workerClass() WorkerClass
	acceptCapability(context.Context, workerAccept, executionCapsule) (workerOperationAck, error)
	observeCapability(context.Context, workerObserve, executionCapsule) (workerObservation, error)
	stopCapability(context.Context, workerStopIntent, executionCapsule) (workerStopAck, error)
}

func selectWorkerCapabilityAdapter(
	class WorkerClass,
	agent workerCapabilityAdapter,
	tool workerCapabilityAdapter,
) workerCapabilityAdapter {
	var selected workerCapabilityAdapter
	switch class {
	case WorkerAgent:
		selected = agent
	case WorkerTool:
		selected = tool
	default:
		return nil
	}
	if selected == nil || selected.workerClass() != class {
		return nil
	}
	return selected
}

type AgentIntentReference struct{ value string }
type AgentPromptReference struct{ value string }

type agentCapabilityPlan struct {
	RuntimeBindingID            RuntimeBindingID
	RuntimeBindingDigest        Digest
	CapabilityContractDigest    Digest
	AllowedPlatformImagesDigest Digest
	ExecutorContractDigest      Digest
	IntentReference             AgentIntentReference
	PromptReference             AgentPromptReference
	EntrypointDigest            Digest
	ActualImageDigest           Digest
	ActualExecutorDigest        Digest
	ProviderRequired            bool
}

func canonicalAgentCapabilityContractDigest(plan agentCapabilityPlan) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.agent-capability-contract/v1",
		plan.RuntimeBindingID.String(), plan.RuntimeBindingDigest.String(),
		plan.IntentReference.value, plan.PromptReference.value, plan.EntrypointDigest.String(),
	}, "\n")))
}

type agentCapabilityInvocation struct {
	RuntimeRunID               RuntimeRunID
	OperationID                OperationID
	CapsuleID                  ExecutionCapsuleID
	CapsuleDigest              Digest
	RuntimeBindingID           RuntimeBindingID
	RuntimeBindingDigest       Digest
	CapabilityContractDigest   Digest
	IntentReference            AgentIntentReference
	PromptReference            AgentPromptReference
	EntrypointDigest           Digest
	ImmutableInputManifest     ResolvedImmutableInputManifest
	OutputContractDigest       Digest
	EvidenceContractDigest     Digest
	GatewayGrantID             GatewayGrantID
	GatewayGrantGeneration     GatewayGrantGeneration
	GatewayGrantDigest         Digest
	QuotaReservationID         QuotaReservationID
	QuotaReservationGeneration QuotaReservationGeneration
	LeaseGeneration            LeaseGeneration
	LeaseFence                 LeaseFence
}

type agentWorkerBackend interface {
	acceptAgent(context.Context, agentCapabilityInvocation, workerAccept) (workerOperationAck, error)
	observeAgent(context.Context, agentCapabilityInvocation, workerObserve) (workerBackendObservation, error)
	stopAgent(context.Context, agentCapabilityInvocation, workerStopIntent) (workerStopAck, error)
}

type agentWorkerCapabilityAdapter struct {
	plan    agentCapabilityPlan
	backend agentWorkerBackend
}

func newAgentWorkerCapabilityAdapter(plan agentCapabilityPlan, backend agentWorkerBackend) (workerCapabilityAdapter, error) {
	if backend == nil || !validOpaqueID(plan.RuntimeBindingID.String()) || plan.RuntimeBindingDigest == (Digest{}) ||
		plan.CapabilityContractDigest == (Digest{}) || !validOpaqueReferenceID(plan.IntentReference.value) ||
		!validOpaqueReferenceID(plan.PromptReference.value) || plan.EntrypointDigest == (Digest{}) ||
		plan.AllowedPlatformImagesDigest == (Digest{}) || plan.ExecutorContractDigest == (Digest{}) ||
		plan.ActualImageDigest == (Digest{}) || plan.ActualExecutorDigest == (Digest{}) ||
		plan.EntrypointDigest != plan.ActualExecutorDigest ||
		plan.CapabilityContractDigest != canonicalAgentCapabilityContractDigest(plan) {
		return nil, newError(ErrorInvalidRequest)
	}
	return &agentWorkerCapabilityAdapter{plan: plan, backend: backend}, nil
}

func (*agentWorkerCapabilityAdapter) workerClass() WorkerClass { return WorkerAgent }

func (adapter *agentWorkerCapabilityAdapter) matchesCapsuleBinding(capsule executionCapsule) bool {
	plan := adapter.plan
	provider := capsule.GatewayGrantID != (GatewayGrantID{}) && capsule.GatewayGrantGeneration > 0 &&
		capsule.GatewayGrantDigest != (Digest{}) && validOpaqueID(capsule.QuotaReservationID.String()) &&
		capsule.QuotaReservationGeneration > 0
	return capsule.WorkerClass == WorkerAgent && plan.RuntimeBindingID == capsule.RuntimeBindingID &&
		plan.RuntimeBindingDigest == capsule.RuntimeBindingDigest &&
		plan.CapabilityContractDigest == canonicalAgentCapabilityContractDigest(plan) &&
		plan.CapabilityContractDigest == capsule.CapabilityContractDigest &&
		plan.AllowedPlatformImagesDigest == capsule.AllowedPlatformImagesDigest &&
		plan.ExecutorContractDigest == capsule.ExecutorContractDigest &&
		plan.ActualImageDigest == capsule.SecurityAcceptance.ActualImageDigest &&
		plan.ActualExecutorDigest == capsule.SecurityAcceptance.ActualExecutorDigest &&
		plan.ProviderRequired == provider
}

func (adapter *agentWorkerCapabilityAdapter) acceptCapability(
	ctx context.Context,
	command workerAccept,
	capsule executionCapsule,
) (workerOperationAck, error) {
	if !adapter.matchesCapsuleBinding(capsule) {
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	ack, err := adapter.backend.acceptAgent(ctx, agentInvocation(adapter.plan, capsule, command.CapsuleDigest), command)
	if err != nil {
		return workerOperationAck{}, newError(ErrorDependencyUnavailable)
	}
	return ack, nil
}

func (adapter *agentWorkerCapabilityAdapter) observeCapability(
	ctx context.Context,
	request workerObserve,
	capsule executionCapsule,
) (workerObservation, error) {
	if !adapter.matchesCapsuleBinding(capsule) {
		return workerObservation{}, newError(ErrorAuthorizationDenied)
	}
	invocation := agentInvocation(adapter.plan, capsule, request.Ref.CapsuleDigest)
	invocation.LeaseGeneration, invocation.LeaseFence = request.Ref.LeaseGeneration, request.Ref.LeaseFence
	raw, err := adapter.backend.observeAgent(ctx, invocation, request)
	if err != nil {
		return workerObservation{}, newError(ErrorDependencyUnavailable)
	}
	return normalizeWorkerObservation(request, capsule, raw)
}

func (adapter *agentWorkerCapabilityAdapter) stopCapability(
	ctx context.Context,
	intent workerStopIntent,
	capsule executionCapsule,
) (workerStopAck, error) {
	if !adapter.matchesCapsuleBinding(capsule) {
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	invocation := agentInvocation(adapter.plan, capsule, intent.CapsuleDigest)
	invocation.LeaseGeneration, invocation.LeaseFence = intent.LeaseGeneration, intent.LeaseFence
	ack, err := adapter.backend.stopAgent(ctx, invocation, intent)
	if err != nil {
		return workerStopAck{}, newError(ErrorDependencyUnavailable)
	}
	return ack, nil
}

func agentInvocation(plan agentCapabilityPlan, capsule executionCapsule, capsuleDigest Digest) agentCapabilityInvocation {
	return agentCapabilityInvocation{
		RuntimeRunID: capsule.RuntimeRunID, OperationID: capsule.OperationID,
		CapsuleID: capsule.CapsuleID, CapsuleDigest: capsuleDigest,
		RuntimeBindingID: capsule.RuntimeBindingID, RuntimeBindingDigest: capsule.RuntimeBindingDigest,
		CapabilityContractDigest: capsule.CapabilityContractDigest, IntentReference: plan.IntentReference,
		PromptReference: plan.PromptReference, EntrypointDigest: plan.EntrypointDigest,
		ImmutableInputManifest: capsule.Inputs, OutputContractDigest: capsule.OutputContractDigest,
		EvidenceContractDigest: capsule.EvidenceContractDigest, GatewayGrantID: capsule.GatewayGrantID,
		GatewayGrantGeneration: capsule.GatewayGrantGeneration, GatewayGrantDigest: capsule.GatewayGrantDigest,
		QuotaReservationID: capsule.QuotaReservationID, QuotaReservationGeneration: capsule.QuotaReservationGeneration,
		LeaseGeneration: capsule.LeaseGeneration, LeaseFence: capsule.LeaseFence,
	}
}

type ToolCapabilityKey uint8

const (
	ToolCapabilityDocumentRender ToolCapabilityKey = iota + 1
	ToolCapabilityMediaInspect
)

type ToolParameterKind uint8

const (
	ToolParametersDocumentRender ToolParameterKind = iota + 1
	ToolParametersMediaInspect
)

// toolTypedParameters is deliberately closed and contains no command, shell,
// host path, raw executable, or unregistered entrypoint field.
type toolTypedParameters struct {
	SchemaVersion         SchemaVersion
	Kind                  ToolParameterKind
	InputManifestIdentity ImmutableInputManifestIdentity
	OptionsDigest         Digest
	CanonicalDigest       Digest
}

func canonicalToolParametersDigest(parameters toolTypedParameters) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.tool-parameters/v1", fmt.Sprint(parameters.SchemaVersion),
		fmt.Sprint(parameters.Kind), parameters.InputManifestIdentity.String(), parameters.OptionsDigest.String(),
	}, "\n")))
}

func canonicalizeToolParameters(parameters toolTypedParameters) (toolTypedParameters, error) {
	parameters.CanonicalDigest = canonicalToolParametersDigest(parameters)
	if parameters.SchemaVersion != SchemaV1 ||
		(parameters.Kind != ToolParametersDocumentRender && parameters.Kind != ToolParametersMediaInspect) ||
		!validOpaqueID(parameters.InputManifestIdentity.String()) || parameters.OptionsDigest == (Digest{}) {
		return toolTypedParameters{}, newError(ErrorInvalidRequest)
	}
	return parameters, nil
}

func toolCapabilityMatchesParameters(capability ToolCapabilityKey, parameters toolTypedParameters) bool {
	return capability == ToolCapabilityDocumentRender && parameters.Kind == ToolParametersDocumentRender ||
		capability == ToolCapabilityMediaInspect && parameters.Kind == ToolParametersMediaInspect
}

type toolCapabilityPlan struct {
	RuntimeBindingID            RuntimeBindingID
	RuntimeBindingDigest        Digest
	CapabilityContractDigest    Digest
	AllowedPlatformImagesDigest Digest
	ExecutorContractDigest      Digest
	CapabilityKey               ToolCapabilityKey
	Parameters                  toolTypedParameters
	EntrypointDigest            Digest
	ActualImageDigest           Digest
	ActualExecutorDigest        Digest
	ProviderRequired            bool
}

func canonicalToolCapabilityContractDigest(plan toolCapabilityPlan) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.tool-capability-contract/v1",
		plan.RuntimeBindingID.String(), plan.RuntimeBindingDigest.String(), fmt.Sprint(plan.CapabilityKey),
		plan.Parameters.CanonicalDigest.String(), plan.EntrypointDigest.String(),
	}, "\n")))
}

type toolCapabilityInvocation struct {
	RuntimeRunID             RuntimeRunID
	OperationID              OperationID
	CapsuleID                ExecutionCapsuleID
	CapsuleDigest            Digest
	RuntimeBindingID         RuntimeBindingID
	RuntimeBindingDigest     Digest
	CapabilityContractDigest Digest
	CapabilityKey            ToolCapabilityKey
	Parameters               toolTypedParameters
	EntrypointDigest         Digest
	ImmutableInputManifest   ResolvedImmutableInputManifest
	OutputContractDigest     Digest
	EvidenceContractDigest   Digest
	GatewayGrantID           GatewayGrantID
	GatewayGrantGeneration   GatewayGrantGeneration
	GatewayGrantDigest       Digest
	LeaseGeneration          LeaseGeneration
	LeaseFence               LeaseFence
}

type toolWorkerBackend interface {
	acceptTool(context.Context, toolCapabilityInvocation, workerAccept) (workerOperationAck, error)
	observeTool(context.Context, toolCapabilityInvocation, workerObserve) (workerBackendObservation, error)
	stopTool(context.Context, toolCapabilityInvocation, workerStopIntent) (workerStopAck, error)
}

type toolWorkerCapabilityAdapter struct {
	plan    toolCapabilityPlan
	backend toolWorkerBackend
}

func newToolWorkerCapabilityAdapter(plan toolCapabilityPlan, backend toolWorkerBackend) (workerCapabilityAdapter, error) {
	parameters, err := canonicalizeToolParameters(plan.Parameters)
	plan.Parameters = parameters
	if err != nil || backend == nil || !validOpaqueID(plan.RuntimeBindingID.String()) ||
		plan.RuntimeBindingDigest == (Digest{}) || plan.CapabilityContractDigest == (Digest{}) ||
		plan.AllowedPlatformImagesDigest == (Digest{}) || plan.ExecutorContractDigest == (Digest{}) ||
		plan.CapabilityKey < ToolCapabilityDocumentRender || plan.CapabilityKey > ToolCapabilityMediaInspect ||
		!toolCapabilityMatchesParameters(plan.CapabilityKey, parameters) || plan.EntrypointDigest == (Digest{}) ||
		plan.ActualImageDigest == (Digest{}) || plan.ActualExecutorDigest == (Digest{}) ||
		plan.EntrypointDigest != plan.ActualExecutorDigest ||
		plan.CapabilityContractDigest != canonicalToolCapabilityContractDigest(plan) {
		return nil, newError(ErrorInvalidRequest)
	}
	return &toolWorkerCapabilityAdapter{plan: plan, backend: backend}, nil
}

func (*toolWorkerCapabilityAdapter) workerClass() WorkerClass { return WorkerTool }

func (adapter *toolWorkerCapabilityAdapter) invocation(capsule executionCapsule, capsuleDigest Digest) toolCapabilityInvocation {
	plan := adapter.plan
	return toolCapabilityInvocation{
		RuntimeRunID: capsule.RuntimeRunID, OperationID: capsule.OperationID,
		CapsuleID: capsule.CapsuleID, CapsuleDigest: capsuleDigest,
		RuntimeBindingID: capsule.RuntimeBindingID, RuntimeBindingDigest: capsule.RuntimeBindingDigest,
		CapabilityContractDigest: capsule.CapabilityContractDigest, CapabilityKey: plan.CapabilityKey,
		Parameters: plan.Parameters, EntrypointDigest: plan.EntrypointDigest,
		ImmutableInputManifest: capsule.Inputs, OutputContractDigest: capsule.OutputContractDigest,
		EvidenceContractDigest: capsule.EvidenceContractDigest, GatewayGrantID: capsule.GatewayGrantID,
		GatewayGrantGeneration: capsule.GatewayGrantGeneration, GatewayGrantDigest: capsule.GatewayGrantDigest,
		LeaseGeneration: capsule.LeaseGeneration, LeaseFence: capsule.LeaseFence,
	}
}

func (adapter *toolWorkerCapabilityAdapter) matchesCapsuleBinding(capsule executionCapsule) bool {
	plan := adapter.plan
	provider := capsule.GatewayGrantID != (GatewayGrantID{}) && capsule.GatewayGrantGeneration > 0 &&
		capsule.GatewayGrantDigest != (Digest{})
	return capsule.WorkerClass == WorkerTool && plan.RuntimeBindingID == capsule.RuntimeBindingID &&
		plan.RuntimeBindingDigest == capsule.RuntimeBindingDigest &&
		plan.CapabilityContractDigest == canonicalToolCapabilityContractDigest(plan) &&
		plan.CapabilityContractDigest == capsule.CapabilityContractDigest &&
		plan.AllowedPlatformImagesDigest == capsule.AllowedPlatformImagesDigest &&
		plan.ExecutorContractDigest == capsule.ExecutorContractDigest &&
		plan.Parameters.InputManifestIdentity == capsule.Inputs.Identity &&
		plan.ActualImageDigest == capsule.SecurityAcceptance.ActualImageDigest &&
		plan.ActualExecutorDigest == capsule.SecurityAcceptance.ActualExecutorDigest &&
		plan.ProviderRequired == provider
}

func (adapter *toolWorkerCapabilityAdapter) acceptCapability(
	ctx context.Context,
	command workerAccept,
	capsule executionCapsule,
) (workerOperationAck, error) {
	if !adapter.matchesCapsuleBinding(capsule) {
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	ack, err := adapter.backend.acceptTool(ctx, adapter.invocation(capsule, command.CapsuleDigest), command)
	if err != nil {
		return workerOperationAck{}, newError(ErrorDependencyUnavailable)
	}
	return ack, nil
}

func (adapter *toolWorkerCapabilityAdapter) observeCapability(
	ctx context.Context,
	request workerObserve,
	capsule executionCapsule,
) (workerObservation, error) {
	if !adapter.matchesCapsuleBinding(capsule) {
		return workerObservation{}, newError(ErrorAuthorizationDenied)
	}
	invocation := adapter.invocation(capsule, request.Ref.CapsuleDigest)
	invocation.LeaseGeneration, invocation.LeaseFence = request.Ref.LeaseGeneration, request.Ref.LeaseFence
	raw, err := adapter.backend.observeTool(ctx, invocation, request)
	if err != nil {
		return workerObservation{}, newError(ErrorDependencyUnavailable)
	}
	return normalizeWorkerObservation(request, capsule, raw)
}

func (adapter *toolWorkerCapabilityAdapter) stopCapability(
	ctx context.Context,
	intent workerStopIntent,
	capsule executionCapsule,
) (workerStopAck, error) {
	if !adapter.matchesCapsuleBinding(capsule) {
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	invocation := adapter.invocation(capsule, intent.CapsuleDigest)
	invocation.LeaseGeneration, invocation.LeaseFence = intent.LeaseGeneration, intent.LeaseFence
	ack, err := adapter.backend.stopTool(ctx, invocation, intent)
	if err != nil {
		return workerStopAck{}, newError(ErrorDependencyUnavailable)
	}
	return ack, nil
}

func (engine *invariantEngine) accept(ctx context.Context, command workerAccept) (workerOperationAck, error) {
	if ctx == nil || ctx.Err() != nil || engine.controls.isCrashed() {
		return workerOperationAck{}, newError(ErrorDependencyUnavailable)
	}
	capsule, err := decodeExecutionCapsule(command.Capsule)
	if err != nil || !validWorkerAccept(command, capsule) {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}

	engine.store.mu.Lock()
	record := engine.store.runtimes[command.RuntimeRunID]
	if record != nil && record.workerAcceptance != nil {
		retained := *record.workerAcceptance
		engine.store.mu.Unlock()
		if retained.OperationID != command.OperationID || retained.CapsuleDigest != command.CapsuleDigest ||
			retained.RuntimeRunID != command.RuntimeRunID {
			return workerOperationAck{}, newError(ErrorIntegrityConflict)
		}
		return retained, nil
	}
	adapter := selectWorkerCapabilityAdapter(command.WorkerClass, engine.agentWorker, engine.toolWorker)
	if record == nil || adapter == nil || !workerAcceptCurrent(record, command, capsule, engine.clock.current()) {
		engine.store.mu.Unlock()
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	engine.store.mu.Unlock()

	ack, err := adapter.acceptCapability(ctx, command, capsule)
	if err != nil {
		return workerOperationAck{}, err
	}
	if !validWorkerOperationAck(command, ack) {
		return workerOperationAck{}, newError(ErrorIntegrityConflict)
	}

	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record = engine.store.runtimes[command.RuntimeRunID]
	if record != nil && record.workerAcceptance != nil {
		retained := *record.workerAcceptance
		if retained != ack {
			return workerOperationAck{}, newError(ErrorIntegrityConflict)
		}
		return retained, nil
	}
	if record == nil || !workerAcceptCurrent(record, command, capsule, engine.clock.current()) {
		return workerOperationAck{}, newError(ErrorAuthorizationDenied)
	}
	applyWorkerAcceptance(record, command, ack)
	return ack, nil
}

func applyWorkerAcceptance(record *runtimeRecord, command workerAccept, ack workerOperationAck) {
	record.capsule.disposition = DispatchAcknowledged
	record.capsule.ackDigest = ack.CanonicalDigest
	record.fixture.RuntimeRevision++
	record.fixture.State = RuntimeStarting
	record.worker = workerSnapshotFromAcceptance(command, ack)
	record.workerAcceptance = &ack
}

func workerAcceptCurrent(record *runtimeRecord, command workerAccept, capsule executionCapsule, now time.Time) bool {
	if record == nil || record.capsule.snapshot.State != CapsulePrepared ||
		record.capsule.dispatchOperationID != command.OperationID || record.capsule.snapshot.CapsuleID != command.CapsuleID ||
		record.capsule.snapshot.Digest != command.CapsuleDigest || !bytes.Equal(record.capsule.wire, command.Capsule) ||
		record.capsule.disposition != DispatchClaimed || record.fixture.State != RuntimePreparingPrerequisites ||
		record.fixture.Outcome != RuntimeOutcomeNone || !executionCapsuleDispatchCurrent(record, now.UTC()) {
		return false
	}
	return capsule.RuntimeRunID == record.fixture.RuntimeRunID && capsule.OperationID == record.operation.OperationID &&
		capsule.CanonicalRequestDigest == record.operation.Digest && capsule.SandboxLeaseID == record.lease.LeaseID &&
		capsule.LeaseGeneration == record.lease.Generation && capsule.LeaseFence == record.lease.Fence &&
		capsule.RuntimeFence == record.fixture.RuntimeFence && capsule.WorkerAuthorityID == record.lease.WorkerAuthorityID &&
		capsule.WorkerGeneration == record.lease.WorkerGeneration && capsule.NodeAuthorityID == record.lease.NodeAuthorityID &&
		capsule.ExecutionNodeID == record.node.ExecutionNodeID && capsule.NodeGeneration == record.node.Generation
}

func workerSnapshotFromAcceptance(command workerAccept, ack workerOperationAck) RuntimeWorkerSnapshot {
	return RuntimeWorkerSnapshot{
		Status: WorkerOperationAccepted, WorkerClass: command.WorkerClass,
		AcceptOperationID: command.OperationID, OperationAckID: ack.OperationAckID,
		OperationAckDigest: ack.CanonicalDigest, WorkerAuthorityID: command.WorkerAuthorityID,
		WorkerGeneration: command.WorkerGeneration, NodeAuthorityID: command.NodeAuthorityID,
		ExecutionNodeID: command.ExecutionNodeID, NodeGeneration: command.NodeGeneration,
		CapsuleID: command.CapsuleID, CapsuleDigest: command.CapsuleDigest,
		AcceptedRuntimeFence: command.RuntimeFence, AcceptedLeaseGeneration: command.LeaseGeneration,
		AcceptedLeaseFence: command.LeaseFence,
	}
}

func knownRuntimeWorkerSnapshot(worker RuntimeWorkerSnapshot) bool {
	if worker == (RuntimeWorkerSnapshot{}) {
		return true
	}
	if worker.Status < WorkerOperationAccepted || worker.Status > WorkerOperationFailureObserved ||
		worker.WorkerClass < WorkerAgent || worker.WorkerClass > WorkerTool ||
		!validOpaqueID(worker.AcceptOperationID.String()) || !validOpaqueID(worker.OperationAckID.String()) ||
		worker.OperationAckDigest == (Digest{}) || !validOpaqueID(worker.WorkerAuthorityID.String()) ||
		worker.WorkerGeneration == 0 || !validOpaqueID(worker.NodeAuthorityID.String()) ||
		!validOpaqueID(worker.ExecutionNodeID.String()) || worker.NodeGeneration == 0 ||
		!validOpaqueID(worker.CapsuleID.String()) || worker.CapsuleDigest == (Digest{}) ||
		worker.AcceptedRuntimeFence == 0 || worker.AcceptedLeaseGeneration == 0 || worker.AcceptedLeaseFence == 0 ||
		!knownWorkerStopSnapshot(worker.Stop) {
		return false
	}
	if worker.Status == WorkerOperationAccepted {
		return worker.Cursor == (WorkerCursorSnapshot{}) && worker.LastObservationID == (WorkerObservationID{}) &&
			worker.LastObservationDigest == (Digest{}) && worker.LastObservationKind == 0 &&
			worker.EvidenceCandidate == (WorkerEvidenceCandidateSnapshot{}) && worker.SafeFailure == WorkerFailureNone
	}
	if !knownWorkerCursor(worker.Cursor) || !validOpaqueID(worker.LastObservationID.String()) ||
		worker.LastObservationDigest == (Digest{}) || worker.LastObservationKind < WorkerObservedRunning ||
		worker.LastObservationKind > WorkerObservedFailed {
		return false
	}
	if worker.Status == WorkerOperationRunning {
		return worker.LastObservationKind == WorkerObservedRunning &&
			worker.EvidenceCandidate == (WorkerEvidenceCandidateSnapshot{}) && worker.SafeFailure == WorkerFailureNone
	}
	if !knownWorkerEvidenceCandidate(worker.EvidenceCandidate) {
		return false
	}
	return worker.Status == WorkerOperationSuccessObserved && worker.LastObservationKind == WorkerObservedSucceeded &&
		worker.SafeFailure == WorkerFailureNone ||
		worker.Status == WorkerOperationFailureObserved && worker.LastObservationKind == WorkerObservedFailed &&
			worker.SafeFailure >= WorkerFailureCapability && worker.SafeFailure <= WorkerFailureAmbiguous
}

type workerHeartbeatInput struct {
	SchemaVersion       SchemaVersion
	OperationID         OperationID
	PersonalWorkspaceID PersonalWorkspaceID
	RuntimeRunID        RuntimeRunID
	StartOperationID    OperationID
	CapsuleID           ExecutionCapsuleID
	CapsuleDigest       Digest
	RuntimeFence        RuntimeFence
	Lease               RuntimeLeaseSnapshot
	Node                RuntimeNodeSnapshot
	ReleaseSafetyEpoch  ReleaseSafetyEpoch
	CatalogSafetyEpoch  CatalogSafetyEpoch
	RequestedExpiresAt  time.Time
	OccurredAt          time.Time
}

type workerHeartbeat struct {
	workerHeartbeatInput
	CanonicalDigest Digest
}

func newWorkerHeartbeat(input workerHeartbeatInput) (workerHeartbeat, error) {
	input.Lease.ExpiresAt = input.Lease.ExpiresAt.UTC()
	input.Lease.AuthorizationExpiresAt = input.Lease.AuthorizationExpiresAt.UTC()
	input.Node.AttestedAt = input.Node.AttestedAt.UTC()
	input.Node.ExpiresAt = input.Node.ExpiresAt.UTC()
	input.RequestedExpiresAt = input.RequestedExpiresAt.UTC()
	input.OccurredAt = input.OccurredAt.UTC()
	heartbeat := workerHeartbeat{workerHeartbeatInput: input}
	heartbeat.CanonicalDigest = canonicalWorkerHeartbeatDigest(heartbeat)
	if !validWorkerHeartbeat(heartbeat) {
		return workerHeartbeat{}, newError(ErrorInvalidRequest)
	}
	return heartbeat, nil
}

func canonicalWorkerHeartbeatDigest(heartbeat workerHeartbeat) Digest {
	input := heartbeat.workerHeartbeatInput
	canonical := []string{
		"slidesmith.runtime-execution.worker-heartbeat/v1", fmt.Sprint(input.SchemaVersion),
		input.OperationID.String(), input.PersonalWorkspaceID.String(), input.RuntimeRunID.String(),
		input.StartOperationID.String(), input.CapsuleID.String(), input.CapsuleDigest.String(),
		fmt.Sprint(input.RuntimeFence),
	}
	canonical = append(canonical, canonicalRuntimeLeaseSnapshot(input.Lease)...)
	canonical = append(canonical, canonicalRuntimeNodeSnapshot(input.Node)...)
	canonical = append(canonical, fmt.Sprint(input.ReleaseSafetyEpoch),
		fmt.Sprint(input.CatalogSafetyEpoch), input.RequestedExpiresAt.UTC().Format(time.RFC3339Nano),
		input.OccurredAt.UTC().Format(time.RFC3339Nano),
	)
	return digestBytes([]byte(strings.Join(canonical, "\n")))
}

func canonicalRuntimeLeaseSnapshot(lease RuntimeLeaseSnapshot) []string {
	return []string{
		fmt.Sprint(lease.AcquireStatus), lease.AcquireOperationID.String(), lease.AcquireDigest.String(),
		lease.LeaseID.String(), fmt.Sprint(lease.Generation), fmt.Sprint(lease.Fence), fmt.Sprint(lease.Disposition),
		lease.ExpiresAt.UTC().Format(time.RFC3339Nano), lease.SandboxID.String(), fmt.Sprint(lease.SandboxGeneration),
		fmt.Sprint(lease.SandboxFence), lease.WorkerAuthorityID.String(), fmt.Sprint(lease.WorkerGeneration),
		lease.NodeAuthorityID.String(), fmt.Sprint(lease.AuthorizationGeneration),
		lease.AuthorizationExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

func canonicalRuntimeNodeSnapshot(node RuntimeNodeSnapshot) []string {
	return []string{
		node.ExecutionNodeID.String(), fmt.Sprint(node.Generation), fmt.Sprint(node.Readiness),
		node.AttestationID.String(), fmt.Sprint(node.AttestationGeneration),
		node.AttestedAt.UTC().Format(time.RFC3339Nano), node.ExpiresAt.UTC().Format(time.RFC3339Nano),
		fmt.Sprint(node.Occupancy), fmt.Sprint(node.Quarantined), fmt.Sprint(node.Containment), fmt.Sprint(node.Reset),
	}
}

func validWorkerHeartbeat(heartbeat workerHeartbeat) bool {
	input := heartbeat.workerHeartbeatInput
	return input.SchemaVersion == SchemaV1 && validOpaqueID(input.OperationID.String()) &&
		validOpaqueID(input.PersonalWorkspaceID.String()) && validOpaqueID(input.RuntimeRunID.String()) &&
		validOpaqueID(input.StartOperationID.String()) && validOpaqueID(input.CapsuleID.String()) &&
		input.CapsuleDigest != (Digest{}) && input.RuntimeFence > 0 &&
		knownLeaseLifecycleSnapshot(input.Lease) && input.Lease.AcquireStatus == LeaseGranted &&
		validOpaqueID(input.Lease.AcquireOperationID.String()) && input.Lease.AcquireDigest != (Digest{}) &&
		input.Lease.Disposition == LeaseActive &&
		validOpaqueID(input.Lease.LeaseID.String()) && input.Lease.Generation > 0 && input.Lease.Fence > 0 &&
		validOpaqueID(input.Lease.WorkerAuthorityID.String()) && input.Lease.WorkerGeneration > 0 &&
		validOpaqueID(input.Lease.NodeAuthorityID.String()) && input.Lease.AuthorizationGeneration > 0 &&
		knownNodeSnapshot(input.Node) && input.Node.Readiness == NodeReady && !input.Node.Quarantined &&
		input.Node.Occupancy == NodeOccupied && validOpaqueID(input.Node.ExecutionNodeID.String()) && input.Node.Generation > 0 &&
		validOpaqueID(input.Node.AttestationID.String()) && input.Node.AttestationGeneration > 0 &&
		input.ReleaseSafetyEpoch > 0 && !input.RequestedExpiresAt.IsZero() && !input.OccurredAt.IsZero() &&
		!input.OccurredAt.Before(input.Node.AttestedAt) && input.OccurredAt.Before(input.Lease.ExpiresAt) &&
		input.OccurredAt.Before(input.Lease.AuthorizationExpiresAt) && input.OccurredAt.Before(input.Node.ExpiresAt) &&
		input.RequestedExpiresAt.After(input.Lease.ExpiresAt) &&
		!input.RequestedExpiresAt.After(input.Lease.AuthorizationExpiresAt) &&
		!input.RequestedExpiresAt.After(input.Node.ExpiresAt) &&
		heartbeat.CanonicalDigest == canonicalWorkerHeartbeatDigest(heartbeat)
}

type workerLeaseDecision struct {
	SchemaVersion          SchemaVersion
	OperationID            OperationID
	CanonicalRequestDigest Digest
	RuntimeRevision        RuntimeRevision
	RuntimeFence           RuntimeFence
	Lease                  RuntimeLeaseSnapshot
	CanonicalDigest        Digest
	Replayed               bool
}

func newWorkerLeaseDecision(
	heartbeat workerHeartbeat,
	runtimeRevision RuntimeRevision,
	runtimeFence RuntimeFence,
	lease RuntimeLeaseSnapshot,
	replayed bool,
) workerLeaseDecision {
	decision := workerLeaseDecision{
		SchemaVersion: SchemaV1, OperationID: heartbeat.OperationID,
		CanonicalRequestDigest: heartbeat.CanonicalDigest, RuntimeRevision: runtimeRevision,
		RuntimeFence: runtimeFence, Lease: lease, Replayed: replayed,
	}
	decision.CanonicalDigest = canonicalWorkerLeaseDecisionDigest(decision)
	return decision
}

func canonicalWorkerLeaseDecisionDigest(decision workerLeaseDecision) Digest {
	canonical := []string{
		"slidesmith.runtime-execution.worker-lease-decision/v1", fmt.Sprint(decision.SchemaVersion),
		decision.OperationID.String(), decision.CanonicalRequestDigest.String(),
		fmt.Sprint(decision.RuntimeRevision), fmt.Sprint(decision.RuntimeFence),
	}
	canonical = append(canonical, canonicalRuntimeLeaseSnapshot(decision.Lease)...)
	return digestBytes([]byte(strings.Join(canonical, "\n")))
}

func validWorkerLeaseDecision(decision workerLeaseDecision) bool {
	return decision.SchemaVersion == SchemaV1 && validOpaqueID(decision.OperationID.String()) &&
		decision.CanonicalRequestDigest != (Digest{}) && decision.RuntimeRevision > 0 && decision.RuntimeFence > 0 &&
		knownLeaseLifecycleSnapshot(decision.Lease) && decision.Lease.AcquireStatus == LeaseGranted &&
		decision.Lease.Disposition == LeaseActive && decision.CanonicalDigest != (Digest{}) &&
		decision.CanonicalDigest == canonicalWorkerLeaseDecisionDigest(decision)
}

type retainedWorkerHeartbeat struct {
	digest   Digest
	decision workerLeaseDecision
}

func (engine *invariantEngine) heartbeat(
	ctx context.Context,
	heartbeat workerHeartbeat,
) (workerLeaseDecision, error) {
	if ctx == nil || ctx.Err() != nil || engine.controls.isCrashed() {
		return workerLeaseDecision{}, newError(ErrorDependencyUnavailable)
	}
	if !validWorkerHeartbeat(heartbeat) {
		return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Lock()
	if retained, exists := engine.store.workerHeartbeats[heartbeat.OperationID]; exists {
		engine.store.mu.Unlock()
		if retained.digest != heartbeat.CanonicalDigest {
			return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
		}
		decision := retained.decision
		decision.Replayed = true
		return decision, nil
	}
	record := engine.store.runtimes[heartbeat.RuntimeRunID]
	if !workerHeartbeatCurrent(record, heartbeat, engine.clock.current()) {
		engine.store.mu.Unlock()
		return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Unlock()

	renewal, err := NewRenewSandboxLease(RenewSandboxLeaseInput{
		SchemaVersion: heartbeat.SchemaVersion, OperationID: heartbeat.OperationID,
		PersonalWorkspaceID: heartbeat.PersonalWorkspaceID, RuntimeRunID: heartbeat.RuntimeRunID,
		SandboxLeaseID: heartbeat.Lease.LeaseID, LeaseGeneration: heartbeat.Lease.Generation,
		LeaseFence: heartbeat.Lease.Fence, ExecutionNodeID: heartbeat.Node.ExecutionNodeID,
		NodeGeneration: heartbeat.Node.Generation, AttestationID: heartbeat.Node.AttestationID,
		AttestationGeneration: heartbeat.Node.AttestationGeneration,
		Authority: NewLeaseRenewalAuthority(
			heartbeat.Lease.WorkerAuthorityID, heartbeat.Lease.WorkerGeneration,
			heartbeat.Lease.NodeAuthorityID, heartbeat.Lease.AuthorizationGeneration,
		),
		ReleaseSafetyEpoch: heartbeat.ReleaseSafetyEpoch, CatalogSafetyEpoch: heartbeat.CatalogSafetyEpoch,
		RequestedExpiresAt: heartbeat.RequestedExpiresAt, OccurredAt: heartbeat.OccurredAt,
	})
	if err != nil {
		return workerLeaseDecision{}, err
	}
	maintained, err := engine.Maintain(ctx, renewal)
	if err != nil {
		return workerLeaseDecision{}, err
	}
	decision := newWorkerLeaseDecision(
		heartbeat, maintained.RuntimeRevision, maintained.RuntimeFence, maintained.Lease, maintained.Replayed,
	)
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists := engine.store.workerHeartbeats[heartbeat.OperationID]; exists {
		if retained.digest != heartbeat.CanonicalDigest || retained.decision.Lease != decision.Lease {
			return workerLeaseDecision{}, newError(ErrorIntegrityConflict)
		}
		decision = retained.decision
		decision.Replayed = true
		return decision, nil
	}
	engine.store.workerHeartbeats[heartbeat.OperationID] = retainedWorkerHeartbeat{
		digest: heartbeat.CanonicalDigest, decision: decision,
	}
	return decision, nil
}

func workerHeartbeatCurrent(record *runtimeRecord, heartbeat workerHeartbeat, now time.Time) bool {
	if record == nil || record.worker.Status < WorkerOperationAccepted || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.fixture.State == RuntimeTerminal || record.fixture.PersonalWorkspaceID != heartbeat.PersonalWorkspaceID ||
		record.fixture.RuntimeFence != heartbeat.RuntimeFence || record.operation.OperationID != heartbeat.StartOperationID ||
		record.capsule.snapshot.CapsuleID != heartbeat.CapsuleID || record.capsule.snapshot.Digest != heartbeat.CapsuleDigest ||
		record.lease != heartbeat.Lease || record.node != heartbeat.Node ||
		record.fixture.SafetyEpoch != heartbeat.ReleaseSafetyEpoch ||
		record.catalogSafetyEpoch != heartbeat.CatalogSafetyEpoch || !now.UTC().Before(record.deadline) {
		return false
	}
	return record.lease.Disposition == LeaseActive && record.node.Readiness == NodeReady && !record.node.Quarantined
}

type WorkerObservationID struct{ value string }

func (id WorkerObservationID) String() string { return id.value }

type WorkerObservationKind uint8

const (
	WorkerObservedRunning WorkerObservationKind = iota + 1
	WorkerObservedSucceeded
	WorkerObservedFailed
)

type WorkerSafeFailure uint8

const (
	WorkerFailureNone WorkerSafeFailure = iota
	WorkerFailureCapability
	WorkerFailureDependency
	WorkerFailureResource
	WorkerFailureDeadline
	WorkerFailureCancelled
	WorkerFailureAmbiguous
)

type WorkerObservationDisposition uint8

const (
	WorkerObservationAccepted WorkerObservationDisposition = iota + 1
	WorkerObservationDeferred
)

// WorkerCursorSnapshot is an opaque, content-free position. Its producer and
// stream binding prevent a cursor from standing in for operation authority.
type WorkerCursorSnapshot struct {
	OperationID        OperationID
	ProducerAuthority  WorkerAuthorityID
	ProducerGeneration WorkerGeneration
	StreamGeneration   uint64
	Position           uint64
}

// WorkerEvidenceCandidateSnapshot is evidence input for #82. It is not an
// EvidenceRoot and cannot make a Runtime terminal.
type WorkerEvidenceCandidateSnapshot struct {
	EvidenceID             EvidenceID
	EvidenceDigest         Digest
	OutputContractDigest   Digest
	EvidenceContractDigest Digest
	SandboxLeaseID         SandboxLeaseID
	LeaseGeneration        LeaseGeneration
	LeaseFence             LeaseFence
	GatewayGrantID         GatewayGrantID
	GatewayGrantGeneration GatewayGrantGeneration
	GatewayGrantDigest     Digest
	InternalCallCount      uint64
}

type workerOperationRef struct {
	SchemaVersion           SchemaVersion
	PersonalWorkspaceID     PersonalWorkspaceID
	TaskID                  TaskID
	TaskRevision            TaskRevision
	PhaseRunID              PhaseRunID
	RuntimeRunID            RuntimeRunID
	RuntimeRevision         RuntimeRevision
	StartOperationID        OperationID
	AcceptOperationID       OperationID
	CapsuleID               ExecutionCapsuleID
	CapsuleDigest           Digest
	OperationAckID          WorkerOperationAckID
	OperationAckDigest      Digest
	WorkerClass             WorkerClass
	WorkerAuthorityID       WorkerAuthorityID
	WorkerGeneration        WorkerGeneration
	NodeAuthorityID         NodeAuthorityID
	ExecutionNodeID         ExecutionNodeID
	NodeGeneration          NodeGeneration
	SandboxLeaseID          SandboxLeaseID
	LeaseGeneration         LeaseGeneration
	LeaseFence              LeaseFence
	RuntimeFence            RuntimeFence
	AuthorizationGeneration AuthorizationGeneration
	ReleaseSafetyEpoch      ReleaseSafetyEpoch
	CatalogSafetyEpoch      CatalogSafetyEpoch
	Deadline                time.Time
}

func workerOperationRefFromSnapshot(start StartRuntimeRun, snapshot RuntimeSnapshot) workerOperationRef {
	return workerOperationRef{
		SchemaVersion: SchemaV1, PersonalWorkspaceID: start.PersonalWorkspaceID,
		TaskID: start.TaskID, TaskRevision: start.ExpectedTaskRevision,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, RuntimeRevision: snapshot.RuntimeRevision,
		StartOperationID: start.OperationID, AcceptOperationID: snapshot.Worker.AcceptOperationID,
		CapsuleID: snapshot.Worker.CapsuleID, CapsuleDigest: snapshot.Worker.CapsuleDigest,
		OperationAckID: snapshot.Worker.OperationAckID, OperationAckDigest: snapshot.Worker.OperationAckDigest,
		WorkerClass: snapshot.Worker.WorkerClass, WorkerAuthorityID: snapshot.Worker.WorkerAuthorityID,
		WorkerGeneration: snapshot.Worker.WorkerGeneration, NodeAuthorityID: snapshot.Worker.NodeAuthorityID,
		ExecutionNodeID: snapshot.Worker.ExecutionNodeID, NodeGeneration: snapshot.Worker.NodeGeneration,
		SandboxLeaseID: snapshot.Lease.LeaseID, LeaseGeneration: snapshot.Lease.Generation,
		LeaseFence: snapshot.Lease.Fence, RuntimeFence: snapshot.RuntimeFence,
		AuthorizationGeneration: snapshot.Lease.AuthorizationGeneration,
		ReleaseSafetyEpoch:      start.ReleaseSafetyEpoch, CatalogSafetyEpoch: startCatalogSafetyEpochValue(start),
		Deadline: snapshot.Deadline.UTC(),
	}
}

func startCatalogSafetyEpochValue(start StartRuntimeRun) CatalogSafetyEpoch {
	if start.CatalogBinding == nil {
		return 0
	}
	return start.CatalogBinding.SafetyEpoch
}

func initialWorkerCursor(snapshot RuntimeSnapshot) WorkerCursorSnapshot {
	return WorkerCursorSnapshot{
		OperationID:        snapshot.Worker.AcceptOperationID,
		ProducerAuthority:  snapshot.Worker.WorkerAuthorityID,
		ProducerGeneration: snapshot.Worker.WorkerGeneration,
		StreamGeneration:   1,
	}
}

type workerObserve struct {
	SchemaVersion   SchemaVersion
	Ref             workerOperationRef
	Cursor          WorkerCursorSnapshot
	CanonicalDigest Digest
}

func newWorkerObserve(ref workerOperationRef, cursor WorkerCursorSnapshot) (workerObserve, error) {
	ref.Deadline = ref.Deadline.UTC()
	request := workerObserve{SchemaVersion: SchemaV1, Ref: ref, Cursor: cursor}
	request.CanonicalDigest = canonicalWorkerObserveDigest(request)
	if !validWorkerObserve(request) {
		return workerObserve{}, newError(ErrorInvalidRequest)
	}
	return request, nil
}

func canonicalWorkerObserveDigest(request workerObserve) Digest {
	ref, cursor := request.Ref, request.Cursor
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.worker-observe/v1", fmt.Sprint(request.SchemaVersion),
		fmt.Sprint(ref.SchemaVersion), ref.PersonalWorkspaceID.String(), ref.TaskID.String(), fmt.Sprint(ref.TaskRevision),
		ref.PhaseRunID.String(), ref.RuntimeRunID.String(), fmt.Sprint(ref.RuntimeRevision),
		ref.StartOperationID.String(), ref.AcceptOperationID.String(),
		ref.CapsuleID.String(), ref.CapsuleDigest.String(), ref.OperationAckID.String(), ref.OperationAckDigest.String(),
		fmt.Sprint(ref.WorkerClass), ref.WorkerAuthorityID.String(), fmt.Sprint(ref.WorkerGeneration),
		ref.NodeAuthorityID.String(), ref.ExecutionNodeID.String(), fmt.Sprint(ref.NodeGeneration),
		ref.SandboxLeaseID.String(), fmt.Sprint(ref.LeaseGeneration), fmt.Sprint(ref.LeaseFence),
		fmt.Sprint(ref.RuntimeFence), fmt.Sprint(ref.AuthorizationGeneration), fmt.Sprint(ref.ReleaseSafetyEpoch),
		fmt.Sprint(ref.CatalogSafetyEpoch), ref.Deadline.Format(time.RFC3339Nano), cursor.OperationID.String(),
		cursor.ProducerAuthority.String(), fmt.Sprint(cursor.ProducerGeneration), fmt.Sprint(cursor.StreamGeneration),
		fmt.Sprint(cursor.Position),
	}, "\n")))
}

func validWorkerObserve(request workerObserve) bool {
	ref, cursor := request.Ref, request.Cursor
	return request.SchemaVersion == SchemaV1 && ref.SchemaVersion == SchemaV1 &&
		validOpaqueID(ref.PersonalWorkspaceID.String()) && validOpaqueID(ref.TaskID.String()) &&
		ref.TaskRevision > 0 && validOpaqueID(ref.PhaseRunID.String()) && validOpaqueID(ref.RuntimeRunID.String()) &&
		ref.RuntimeRevision > 0 &&
		validOpaqueID(ref.StartOperationID.String()) && validOpaqueID(ref.AcceptOperationID.String()) &&
		validOpaqueID(ref.CapsuleID.String()) && ref.CapsuleDigest != (Digest{}) &&
		validOpaqueID(ref.OperationAckID.String()) && ref.OperationAckDigest != (Digest{}) &&
		ref.WorkerClass >= WorkerAgent && ref.WorkerClass <= WorkerTool &&
		validOpaqueID(ref.WorkerAuthorityID.String()) && ref.WorkerGeneration > 0 &&
		validOpaqueID(ref.NodeAuthorityID.String()) && validOpaqueID(ref.ExecutionNodeID.String()) && ref.NodeGeneration > 0 &&
		validOpaqueID(ref.SandboxLeaseID.String()) && ref.LeaseGeneration > 0 && ref.LeaseFence > 0 &&
		ref.RuntimeFence > 0 && ref.AuthorizationGeneration > 0 && ref.ReleaseSafetyEpoch > 0 && !ref.Deadline.IsZero() &&
		cursor.OperationID == ref.AcceptOperationID && cursor.ProducerAuthority == ref.WorkerAuthorityID &&
		cursor.ProducerGeneration == ref.WorkerGeneration && cursor.StreamGeneration > 0 &&
		request.CanonicalDigest == canonicalWorkerObserveDigest(request)
}

type workerBackendObservation struct {
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

type workerObservation struct {
	SchemaVersion      SchemaVersion
	ObservationID      WorkerObservationID
	Kind               WorkerObservationKind
	RuntimeRunID       RuntimeRunID
	OperationID        OperationID
	CapsuleDigest      Digest
	ProducerAuthority  WorkerAuthorityID
	ProducerGeneration WorkerGeneration
	StreamGeneration   uint64
	Position           uint64
	ObservedAt         time.Time
	Evidence           WorkerEvidenceCandidateSnapshot
	SafeFailure        WorkerSafeFailure
	CanonicalDigest    Digest
}

func normalizeWorkerObservation(
	request workerObserve,
	capsule executionCapsule,
	raw workerBackendObservation,
) (workerObservation, error) {
	if raw.StreamGeneration == 0 {
		raw.StreamGeneration = request.Cursor.StreamGeneration
	}
	if raw.Position == 0 {
		raw.Position = request.Cursor.Position + 1
	}
	if raw.ObservationID == (WorkerObservationID{}) {
		material := digestBytes([]byte(fmt.Sprintf("slidesmith.runtime-execution.worker-observation-id/v1\n%s\n%s\n%d\n%d",
			request.Ref.AcceptOperationID.String(), request.Ref.WorkerAuthorityID.String(), raw.StreamGeneration, raw.Position)))
		raw.ObservationID = WorkerObservationID{value: fmt.Sprintf("worker-observation-%x", material[:12])}
	}
	observation := workerObservation{
		SchemaVersion: SchemaV1, ObservationID: raw.ObservationID, Kind: raw.Kind,
		RuntimeRunID: request.Ref.RuntimeRunID, OperationID: request.Ref.AcceptOperationID,
		CapsuleDigest: request.Ref.CapsuleDigest, ProducerAuthority: request.Ref.WorkerAuthorityID,
		ProducerGeneration: request.Ref.WorkerGeneration, StreamGeneration: raw.StreamGeneration,
		Position: raw.Position, ObservedAt: raw.ObservedAt.UTC(), SafeFailure: raw.SafeFailure,
	}
	if raw.Kind == WorkerObservedSucceeded || raw.Kind == WorkerObservedFailed {
		observation.Evidence = WorkerEvidenceCandidateSnapshot{
			EvidenceID: raw.EvidenceID, EvidenceDigest: raw.EvidenceDigest,
			OutputContractDigest: capsule.OutputContractDigest, EvidenceContractDigest: capsule.EvidenceContractDigest,
			SandboxLeaseID: request.Ref.SandboxLeaseID, LeaseGeneration: request.Ref.LeaseGeneration,
			LeaseFence: request.Ref.LeaseFence, GatewayGrantID: capsule.GatewayGrantID,
			GatewayGrantGeneration: capsule.GatewayGrantGeneration, GatewayGrantDigest: capsule.GatewayGrantDigest,
			InternalCallCount: raw.InternalCallCount,
		}
	}
	observation.CanonicalDigest = canonicalWorkerObservationDigest(observation)
	if !validWorkerObservation(request, observation) {
		return workerObservation{}, newError(ErrorIntegrityConflict)
	}
	return observation, nil
}

func canonicalWorkerObservationDigest(observation workerObservation) Digest {
	evidence := observation.Evidence
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.worker-observation/v1", fmt.Sprint(observation.SchemaVersion),
		observation.ObservationID.String(), fmt.Sprint(observation.Kind), observation.RuntimeRunID.String(),
		observation.OperationID.String(), observation.CapsuleDigest.String(), observation.ProducerAuthority.String(),
		fmt.Sprint(observation.ProducerGeneration), fmt.Sprint(observation.StreamGeneration), fmt.Sprint(observation.Position),
		observation.ObservedAt.Format(time.RFC3339Nano), evidence.EvidenceID.String(), evidence.EvidenceDigest.String(),
		evidence.OutputContractDigest.String(), evidence.EvidenceContractDigest.String(), evidence.SandboxLeaseID.String(),
		fmt.Sprint(evidence.LeaseGeneration), fmt.Sprint(evidence.LeaseFence), evidence.GatewayGrantID.String(),
		fmt.Sprint(evidence.GatewayGrantGeneration), evidence.GatewayGrantDigest.String(), fmt.Sprint(evidence.InternalCallCount),
		fmt.Sprint(observation.SafeFailure),
	}, "\n")))
}

func validWorkerObservation(request workerObserve, observation workerObservation) bool {
	if observation.SchemaVersion != SchemaV1 || !validOpaqueID(observation.ObservationID.String()) ||
		observation.Kind < WorkerObservedRunning || observation.Kind > WorkerObservedFailed ||
		observation.RuntimeRunID != request.Ref.RuntimeRunID || observation.OperationID != request.Ref.AcceptOperationID ||
		observation.CapsuleDigest != request.Ref.CapsuleDigest || observation.ProducerAuthority != request.Ref.WorkerAuthorityID ||
		observation.ProducerGeneration != request.Ref.WorkerGeneration || observation.StreamGeneration != request.Cursor.StreamGeneration ||
		observation.Position == 0 || observation.ObservedAt.IsZero() ||
		observation.CanonicalDigest != canonicalWorkerObservationDigest(observation) {
		return false
	}
	if observation.Kind == WorkerObservedRunning {
		return observation.Evidence == (WorkerEvidenceCandidateSnapshot{}) && observation.SafeFailure == WorkerFailureNone
	}
	evidence := observation.Evidence
	if !validOpaqueID(evidence.EvidenceID.String()) || evidence.EvidenceDigest == (Digest{}) ||
		evidence.OutputContractDigest == (Digest{}) || evidence.EvidenceContractDigest == (Digest{}) ||
		evidence.SandboxLeaseID != request.Ref.SandboxLeaseID || evidence.LeaseGeneration != request.Ref.LeaseGeneration ||
		evidence.LeaseFence != request.Ref.LeaseFence || evidence.InternalCallCount == 0 {
		return false
	}
	provider := evidence.GatewayGrantID != (GatewayGrantID{}) || evidence.GatewayGrantGeneration != 0 || evidence.GatewayGrantDigest != (Digest{})
	if provider && (!validOpaqueID(evidence.GatewayGrantID.String()) || evidence.GatewayGrantGeneration == 0 || evidence.GatewayGrantDigest == (Digest{})) {
		return false
	}
	if observation.Kind == WorkerObservedSucceeded {
		return observation.SafeFailure == WorkerFailureNone
	}
	return observation.SafeFailure >= WorkerFailureCapability && observation.SafeFailure <= WorkerFailureAmbiguous
}

func knownWorkerCursor(cursor WorkerCursorSnapshot) bool {
	return validOpaqueID(cursor.OperationID.String()) && validOpaqueID(cursor.ProducerAuthority.String()) &&
		cursor.ProducerGeneration > 0 && cursor.StreamGeneration > 0 && cursor.Position > 0
}

func knownWorkerEvidenceCandidate(evidence WorkerEvidenceCandidateSnapshot) bool {
	if !validOpaqueID(evidence.EvidenceID.String()) || evidence.EvidenceDigest == (Digest{}) ||
		evidence.OutputContractDigest == (Digest{}) || evidence.EvidenceContractDigest == (Digest{}) ||
		!validOpaqueID(evidence.SandboxLeaseID.String()) || evidence.LeaseGeneration == 0 || evidence.LeaseFence == 0 ||
		evidence.InternalCallCount == 0 {
		return false
	}
	provider := evidence.GatewayGrantID != (GatewayGrantID{}) || evidence.GatewayGrantGeneration != 0 || evidence.GatewayGrantDigest != (Digest{})
	return !provider || validOpaqueID(evidence.GatewayGrantID.String()) && evidence.GatewayGrantGeneration > 0 && evidence.GatewayGrantDigest != (Digest{})
}

type workerObservationResult struct {
	Disposition WorkerObservationDisposition
	Observation workerObservation
	Replayed    bool
}

func (engine *invariantEngine) observe(ctx context.Context, request workerObserve) (workerObservationResult, error) {
	if ctx == nil || ctx.Err() != nil || engine.controls.isCrashed() {
		return workerObservationResult{}, newError(ErrorDependencyUnavailable)
	}
	if !validWorkerObserve(request) {
		return workerObservationResult{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[request.Ref.RuntimeRunID]
	if record != nil && record.workerObservationQueries != nil {
		if retained, exists := record.workerObservationQueries[request.CanonicalDigest]; exists {
			engine.store.mu.Unlock()
			retained.Replayed = true
			return retained, nil
		}
	}
	adapter := selectWorkerCapabilityAdapter(request.Ref.WorkerClass, engine.agentWorker, engine.toolWorker)
	if adapter == nil || !workerObserveCurrent(record, request, engine.clock.current()) {
		engine.store.mu.Unlock()
		return workerObservationResult{}, newError(ErrorAuthorizationDenied)
	}
	capsule := record.capsule.decoded
	engine.store.mu.Unlock()

	observation, err := adapter.observeCapability(ctx, request, capsule)
	if err != nil {
		return workerObservationResult{}, err
	}

	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record = engine.store.runtimes[request.Ref.RuntimeRunID]
	if !workerObserveCurrent(record, request, engine.clock.current()) ||
		!validWorkerObservation(request, observation) || observation.ObservedAt.After(engine.clock.current()) {
		return workerObservationResult{}, newError(ErrorAuthorizationDenied)
	}
	if record.workerObservations == nil {
		record.workerObservations = make(map[WorkerObservationID]workerObservation)
	}
	if retained, exists := record.workerObservations[observation.ObservationID]; exists {
		if retained.CanonicalDigest != observation.CanonicalDigest {
			return workerObservationResult{}, newError(ErrorIntegrityConflict)
		}
		return workerObservationResult{}, newError(ErrorIntegrityConflict)
	}
	wantPosition := request.Cursor.Position + 1
	if observation.Position > wantPosition {
		return workerObservationResult{Disposition: WorkerObservationDeferred, Observation: observation}, nil
	}
	if observation.Position != wantPosition {
		return workerObservationResult{}, newError(ErrorIntegrityConflict)
	}
	result := workerObservationResult{Disposition: WorkerObservationAccepted, Observation: observation}
	if record.workerObservationQueries == nil {
		record.workerObservationQueries = make(map[Digest]workerObservationResult)
	}
	record.workerObservationQueries[request.CanonicalDigest] = result
	record.workerObservations[observation.ObservationID] = observation
	applyWorkerObservation(record, observation)
	return result, nil
}

func applyWorkerObservation(record *runtimeRecord, observation workerObservation) {
	record.fixture.RuntimeRevision++
	record.worker.Cursor = WorkerCursorSnapshot{
		OperationID: observation.OperationID, ProducerAuthority: observation.ProducerAuthority,
		ProducerGeneration: observation.ProducerGeneration, StreamGeneration: observation.StreamGeneration,
		Position: observation.Position,
	}
	record.worker.LastObservationID = observation.ObservationID
	record.worker.LastObservationDigest = observation.CanonicalDigest
	record.worker.LastObservationKind = observation.Kind
	record.worker.EvidenceCandidate = observation.Evidence
	record.worker.SafeFailure = observation.SafeFailure
	switch observation.Kind {
	case WorkerObservedRunning:
		record.worker.Status = WorkerOperationRunning
		record.fixture.State = RuntimeRunning
	case WorkerObservedSucceeded:
		record.worker.Status = WorkerOperationSuccessObserved
		record.fixture.State = RuntimeReconciling
	case WorkerObservedFailed:
		record.worker.Status, record.fixture.State = WorkerOperationFailureObserved, RuntimeReconciling
	}
}

func workerObserveCurrent(record *runtimeRecord, request workerObserve, now time.Time) bool {
	if record == nil || (record.worker.Status != WorkerOperationAccepted && record.worker.Status != WorkerOperationRunning) ||
		record.fixture.Outcome != RuntimeOutcomeNone ||
		record.fixture.State == RuntimeTerminal || record.fixture.State == RuntimeStopping || !now.UTC().Before(record.deadline) ||
		record.fixture.PersonalWorkspaceID != request.Ref.PersonalWorkspaceID || record.fixture.TaskID != request.Ref.TaskID ||
		record.fixture.TaskRevision != request.Ref.TaskRevision || record.fixture.PhaseRunID != request.Ref.PhaseRunID ||
		record.fixture.RuntimeRunID != request.Ref.RuntimeRunID || record.fixture.RuntimeRevision != request.Ref.RuntimeRevision ||
		record.operation.OperationID != request.Ref.StartOperationID || record.capsule.dispatchOperationID != request.Ref.AcceptOperationID ||
		record.capsule.snapshot.CapsuleID != request.Ref.CapsuleID || record.capsule.snapshot.Digest != request.Ref.CapsuleDigest ||
		record.worker.OperationAckID != request.Ref.OperationAckID || record.worker.OperationAckDigest != request.Ref.OperationAckDigest ||
		record.worker.WorkerClass != request.Ref.WorkerClass || record.lease.WorkerAuthorityID != request.Ref.WorkerAuthorityID ||
		record.lease.WorkerGeneration != request.Ref.WorkerGeneration || record.lease.NodeAuthorityID != request.Ref.NodeAuthorityID ||
		record.node.ExecutionNodeID != request.Ref.ExecutionNodeID || record.node.Generation != request.Ref.NodeGeneration ||
		record.lease.LeaseID != request.Ref.SandboxLeaseID || record.lease.Generation != request.Ref.LeaseGeneration ||
		record.lease.Fence != request.Ref.LeaseFence || record.fixture.RuntimeFence != request.Ref.RuntimeFence ||
		record.lease.AuthorizationGeneration != request.Ref.AuthorizationGeneration ||
		record.fixture.SafetyEpoch != request.Ref.ReleaseSafetyEpoch || record.catalogSafetyEpoch != request.Ref.CatalogSafetyEpoch ||
		!record.deadline.Equal(request.Ref.Deadline) || record.lease.Disposition != LeaseActive ||
		!now.UTC().Before(record.lease.ExpiresAt) || !now.UTC().Before(record.lease.AuthorizationExpiresAt) ||
		record.node.Readiness != NodeReady || record.node.Quarantined {
		return false
	}
	wantCursor := initialWorkerCursor(snapshot(record, SnapshotSchemaCurrent))
	if record.worker.Status != WorkerOperationAccepted {
		wantCursor = record.worker.Cursor
	}
	return request.Cursor == wantCursor
}

type WorkerStopReason uint8

const (
	WorkerStopCancellation WorkerStopReason = iota + 1
	WorkerStopLeaseRevoked
	WorkerStopDeadline
	WorkerStopNodeLost
)

type WorkerStopStatus uint8

const (
	WorkerStopNone WorkerStopStatus = iota
	WorkerStopAccepted
)

type WorkerStopAckID struct{ value string }

func (id WorkerStopAckID) String() string { return id.value }

// WorkerStopSnapshot records only acceptance of a best-effort request. It has
// no fields capable of representing process, containment, revocation, C04, or
// capacity-release completion.
type WorkerStopSnapshot struct {
	Status          WorkerStopStatus
	OperationID     OperationID
	AckID           WorkerStopAckID
	AckDigest       Digest
	Reason          WorkerStopReason
	RuntimeFence    RuntimeFence
	LeaseGeneration LeaseGeneration
	LeaseFence      LeaseFence
}

type workerStopIntent struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	PersonalWorkspaceID     PersonalWorkspaceID
	TaskID                  TaskID
	PhaseRunID              PhaseRunID
	RuntimeRunID            RuntimeRunID
	StartOperationID        OperationID
	OriginalOperationID     OperationID
	CapsuleID               ExecutionCapsuleID
	CapsuleDigest           Digest
	WorkerClass             WorkerClass
	WorkerAuthorityID       WorkerAuthorityID
	WorkerGeneration        WorkerGeneration
	NodeAuthorityID         NodeAuthorityID
	ExecutionNodeID         ExecutionNodeID
	NodeGeneration          NodeGeneration
	SandboxLeaseID          SandboxLeaseID
	LeaseGeneration         LeaseGeneration
	LeaseFence              LeaseFence
	RuntimeFence            RuntimeFence
	AuthorizationGeneration AuthorizationGeneration
	ReleaseSafetyEpoch      ReleaseSafetyEpoch
	CatalogSafetyEpoch      CatalogSafetyEpoch
	Reason                  WorkerStopReason
	Deadline                time.Time
	RuntimeDeadline         time.Time
	CanonicalDigest         Digest
}

func newWorkerStopIntentFromSnapshot(
	start StartRuntimeRun,
	snapshot RuntimeSnapshot,
	operationID OperationID,
	reason WorkerStopReason,
	deadline time.Time,
) (workerStopIntent, error) {
	intent := workerStopIntent{
		SchemaVersion: SchemaV1, OperationID: operationID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID, PhaseRunID: start.PhaseRunID,
		RuntimeRunID: start.RuntimeRunID, StartOperationID: start.OperationID,
		OriginalOperationID: snapshot.Worker.AcceptOperationID,
		CapsuleID:           snapshot.Worker.CapsuleID, CapsuleDigest: snapshot.Worker.CapsuleDigest,
		WorkerClass: snapshot.Worker.WorkerClass, WorkerAuthorityID: snapshot.Worker.WorkerAuthorityID,
		WorkerGeneration: snapshot.Worker.WorkerGeneration, NodeAuthorityID: snapshot.Worker.NodeAuthorityID,
		ExecutionNodeID: snapshot.Worker.ExecutionNodeID, NodeGeneration: snapshot.Worker.NodeGeneration,
		SandboxLeaseID: snapshot.Lease.LeaseID, LeaseGeneration: snapshot.Lease.Generation,
		LeaseFence: snapshot.Lease.Fence, RuntimeFence: snapshot.RuntimeFence,
		AuthorizationGeneration: snapshot.Lease.AuthorizationGeneration,
		ReleaseSafetyEpoch:      start.ReleaseSafetyEpoch, CatalogSafetyEpoch: startCatalogSafetyEpochValue(start),
		Reason: reason, Deadline: deadline.UTC(), RuntimeDeadline: snapshot.Deadline.UTC(),
	}
	intent.CanonicalDigest = canonicalWorkerStopIntentDigest(intent)
	if !validWorkerStopIntent(intent) {
		return workerStopIntent{}, newError(ErrorInvalidRequest)
	}
	return intent, nil
}

func canonicalWorkerStopIntentDigest(intent workerStopIntent) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.worker-stop-intent/v1", fmt.Sprint(intent.SchemaVersion),
		intent.OperationID.String(), intent.PersonalWorkspaceID.String(), intent.TaskID.String(), intent.PhaseRunID.String(),
		intent.RuntimeRunID.String(), intent.StartOperationID.String(), intent.OriginalOperationID.String(),
		intent.CapsuleID.String(), intent.CapsuleDigest.String(), fmt.Sprint(intent.WorkerClass),
		intent.WorkerAuthorityID.String(), fmt.Sprint(intent.WorkerGeneration), intent.NodeAuthorityID.String(),
		intent.ExecutionNodeID.String(), fmt.Sprint(intent.NodeGeneration), intent.SandboxLeaseID.String(),
		fmt.Sprint(intent.LeaseGeneration), fmt.Sprint(intent.LeaseFence), fmt.Sprint(intent.RuntimeFence),
		fmt.Sprint(intent.AuthorizationGeneration), fmt.Sprint(intent.ReleaseSafetyEpoch), fmt.Sprint(intent.CatalogSafetyEpoch),
		fmt.Sprint(intent.Reason), intent.Deadline.Format(time.RFC3339Nano), intent.RuntimeDeadline.Format(time.RFC3339Nano),
	}, "\n")))
}

func validWorkerStopIntent(intent workerStopIntent) bool {
	return intent.SchemaVersion == SchemaV1 && validOpaqueID(intent.OperationID.String()) &&
		validOpaqueID(intent.PersonalWorkspaceID.String()) && validOpaqueID(intent.TaskID.String()) &&
		validOpaqueID(intent.PhaseRunID.String()) && validOpaqueID(intent.RuntimeRunID.String()) &&
		validOpaqueID(intent.StartOperationID.String()) && validOpaqueID(intent.OriginalOperationID.String()) &&
		validOpaqueID(intent.CapsuleID.String()) && intent.CapsuleDigest != (Digest{}) &&
		intent.WorkerClass >= WorkerAgent && intent.WorkerClass <= WorkerTool &&
		validOpaqueID(intent.WorkerAuthorityID.String()) && intent.WorkerGeneration > 0 &&
		validOpaqueID(intent.NodeAuthorityID.String()) && validOpaqueID(intent.ExecutionNodeID.String()) && intent.NodeGeneration > 0 &&
		validOpaqueID(intent.SandboxLeaseID.String()) && intent.LeaseGeneration > 0 && intent.LeaseFence > 0 &&
		intent.RuntimeFence > 0 && intent.AuthorizationGeneration > 0 && intent.ReleaseSafetyEpoch > 0 &&
		intent.Reason >= WorkerStopCancellation && intent.Reason <= WorkerStopNodeLost &&
		!intent.Deadline.IsZero() && !intent.RuntimeDeadline.IsZero() &&
		intent.CanonicalDigest == canonicalWorkerStopIntentDigest(intent)
}

type workerStopAck struct {
	SchemaVersion       SchemaVersion
	OperationID         OperationID
	AckID               WorkerStopAckID
	RuntimeRunID        RuntimeRunID
	OriginalOperationID OperationID
	CapsuleID           ExecutionCapsuleID
	CapsuleDigest       Digest
	RuntimeFence        RuntimeFence
	LeaseGeneration     LeaseGeneration
	LeaseFence          LeaseFence
	BestEffortAccepted  bool
	CanonicalDigest     Digest
}

func newWorkerStopAck(intent workerStopIntent) workerStopAck {
	material := digestBytes([]byte("slidesmith.runtime-execution.worker-stop-ack-id/v1\n" +
		intent.OperationID.String() + "\n" + intent.CanonicalDigest.String()))
	ack := workerStopAck{
		SchemaVersion: SchemaV1, OperationID: intent.OperationID,
		AckID:        WorkerStopAckID{value: fmt.Sprintf("worker-stop-ack-%x", material[:12])},
		RuntimeRunID: intent.RuntimeRunID, OriginalOperationID: intent.OriginalOperationID,
		CapsuleID: intent.CapsuleID, CapsuleDigest: intent.CapsuleDigest, RuntimeFence: intent.RuntimeFence,
		LeaseGeneration: intent.LeaseGeneration, LeaseFence: intent.LeaseFence, BestEffortAccepted: true,
	}
	ack.CanonicalDigest = canonicalWorkerStopAckDigest(ack)
	return ack
}

func canonicalWorkerStopAckDigest(ack workerStopAck) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.worker-stop-ack/v1", fmt.Sprint(ack.SchemaVersion), ack.OperationID.String(),
		ack.AckID.String(), ack.RuntimeRunID.String(), ack.OriginalOperationID.String(), ack.CapsuleID.String(),
		ack.CapsuleDigest.String(), fmt.Sprint(ack.RuntimeFence), fmt.Sprint(ack.LeaseGeneration),
		fmt.Sprint(ack.LeaseFence), fmt.Sprint(ack.BestEffortAccepted),
	}, "\n")))
}

func validWorkerStopAck(intent workerStopIntent, ack workerStopAck) bool {
	return ack.SchemaVersion == SchemaV1 && ack.OperationID == intent.OperationID && validOpaqueID(ack.AckID.String()) &&
		ack.RuntimeRunID == intent.RuntimeRunID && ack.OriginalOperationID == intent.OriginalOperationID &&
		ack.CapsuleID == intent.CapsuleID && ack.CapsuleDigest == intent.CapsuleDigest &&
		ack.RuntimeFence == intent.RuntimeFence && ack.LeaseGeneration == intent.LeaseGeneration &&
		ack.LeaseFence == intent.LeaseFence && ack.BestEffortAccepted &&
		ack.CanonicalDigest == canonicalWorkerStopAckDigest(ack)
}

type retainedWorkerStop struct {
	digest Digest
	ack    workerStopAck
}

func (engine *invariantEngine) stop(ctx context.Context, intent workerStopIntent) (workerStopAck, error) {
	if ctx == nil || ctx.Err() != nil || engine.controls.isCrashed() {
		return workerStopAck{}, newError(ErrorDependencyUnavailable)
	}
	if !validWorkerStopIntent(intent) {
		return workerStopAck{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[intent.RuntimeRunID]
	if record != nil && record.workerStops != nil {
		if retained, exists := record.workerStops[intent.OperationID]; exists {
			engine.store.mu.Unlock()
			if retained.digest != intent.CanonicalDigest {
				return workerStopAck{}, newError(ErrorIntegrityConflict)
			}
			return retained.ack, nil
		}
	}
	adapter := selectWorkerCapabilityAdapter(intent.WorkerClass, engine.agentWorker, engine.toolWorker)
	if adapter == nil || !workerStopCurrent(record, intent, engine.clock.current()) {
		engine.store.mu.Unlock()
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	capsule := record.capsule.decoded
	engine.store.mu.Unlock()

	ack, err := adapter.stopCapability(ctx, intent, capsule)
	if err != nil {
		return workerStopAck{}, err
	}
	if !validWorkerStopAck(intent, ack) {
		return workerStopAck{}, newError(ErrorIntegrityConflict)
	}

	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record = engine.store.runtimes[intent.RuntimeRunID]
	if record != nil && record.workerStops != nil {
		if retained, exists := record.workerStops[intent.OperationID]; exists {
			if retained.digest != intent.CanonicalDigest || retained.ack != ack {
				return workerStopAck{}, newError(ErrorIntegrityConflict)
			}
			return retained.ack, nil
		}
	}
	if !workerStopCurrent(record, intent, engine.clock.current()) {
		return workerStopAck{}, newError(ErrorAuthorizationDenied)
	}
	if record.workerStops == nil {
		record.workerStops = make(map[OperationID]retainedWorkerStop)
	}
	record.workerStops[intent.OperationID] = retainedWorkerStop{digest: intent.CanonicalDigest, ack: ack}
	applyWorkerStopAcceptance(record, intent, ack)
	return ack, nil
}

func applyWorkerStopAcceptance(record *runtimeRecord, intent workerStopIntent, ack workerStopAck) {
	record.fixture.RuntimeRevision++
	record.worker.Stop = WorkerStopSnapshot{
		Status: WorkerStopAccepted, OperationID: intent.OperationID, AckID: ack.AckID,
		AckDigest: ack.CanonicalDigest, Reason: intent.Reason, RuntimeFence: intent.RuntimeFence,
		LeaseGeneration: intent.LeaseGeneration, LeaseFence: intent.LeaseFence,
	}
}

func workerStopCurrent(record *runtimeRecord, intent workerStopIntent, now time.Time) bool {
	if record == nil || record.worker.Status < WorkerOperationAccepted || !now.UTC().Before(intent.Deadline) ||
		record.fixture.PersonalWorkspaceID != intent.PersonalWorkspaceID || record.fixture.TaskID != intent.TaskID ||
		record.fixture.PhaseRunID != intent.PhaseRunID || record.fixture.RuntimeRunID != intent.RuntimeRunID ||
		record.acceptedStart.OperationID != intent.StartOperationID || record.worker.AcceptOperationID != intent.OriginalOperationID ||
		record.capsule.snapshot.CapsuleID != intent.CapsuleID || record.capsule.snapshot.Digest != intent.CapsuleDigest ||
		record.worker.WorkerClass != intent.WorkerClass || record.lease.WorkerAuthorityID != intent.WorkerAuthorityID ||
		record.lease.WorkerGeneration != intent.WorkerGeneration || record.lease.NodeAuthorityID != intent.NodeAuthorityID ||
		record.node.ExecutionNodeID != intent.ExecutionNodeID || record.node.Generation != intent.NodeGeneration ||
		record.lease.LeaseID != intent.SandboxLeaseID || record.lease.Generation != intent.LeaseGeneration ||
		record.lease.Fence != intent.LeaseFence || record.fixture.RuntimeFence != intent.RuntimeFence ||
		record.lease.AuthorizationGeneration != intent.AuthorizationGeneration ||
		record.fixture.SafetyEpoch != intent.ReleaseSafetyEpoch || record.catalogSafetyEpoch != intent.CatalogSafetyEpoch ||
		!record.deadline.Equal(intent.RuntimeDeadline) {
		return false
	}
	terminalOrStopping := record.fixture.State == RuntimeStopping && record.fixture.Outcome == RuntimeOutcomeNone ||
		record.fixture.State == RuntimeTerminal && record.fixture.Outcome != RuntimeOutcomeNone
	if !terminalOrStopping {
		return false
	}
	switch intent.Reason {
	case WorkerStopCancellation:
		return record.cancellation.Status == CancellationAccepted && record.lease.Disposition == LeaseRevoked
	case WorkerStopLeaseRevoked:
		return record.cancellation.Status != CancellationAccepted && record.lease.Disposition == LeaseRevoked &&
			record.node.Readiness == NodeReady && record.node.Quarantined
	case WorkerStopDeadline:
		return record.cancellation.Status != CancellationAccepted && record.lease.Disposition == LeaseExpired &&
			!now.UTC().Before(record.lease.ExpiresAt)
	case WorkerStopNodeLost:
		return record.cancellation.Status != CancellationAccepted && record.lease.Disposition == LeaseRevoked &&
			record.node.Readiness == NodeUnavailable && record.node.Quarantined
	default:
		return false
	}
}

func knownWorkerStopSnapshot(stop WorkerStopSnapshot) bool {
	if stop == (WorkerStopSnapshot{}) {
		return true
	}
	return stop.Status == WorkerStopAccepted && validOpaqueID(stop.OperationID.String()) &&
		validOpaqueID(stop.AckID.String()) && stop.AckDigest != (Digest{}) &&
		stop.Reason >= WorkerStopCancellation && stop.Reason <= WorkerStopNodeLost &&
		stop.RuntimeFence > 0 && stop.LeaseGeneration > 0 && stop.LeaseFence > 0
}
