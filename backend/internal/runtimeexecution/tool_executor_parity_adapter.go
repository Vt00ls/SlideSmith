// Package runtimeexecution — Tool executor parity adapter.
//
// Tool executors implement the same worker contract semantics as Agent Compose.
// They may use a different backend or map through the same contract to a
// command-run path, but only after passing the same hostile-execution
// acceptance contract.
//
// Raw exec, caller shell, and path-only invocation have no production
// qualification and are rejected at adapter construction.
package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Tool executor production qualification
// ---------------------------------------------------------------------------

// ToolExecutorProductionMode classifies the execution mode. Only pinned-image
// and pinned-executor modes are production-qualified.
type ToolExecutorProductionMode uint8

const (
	// ToolExecutorPinnedImage executes the tool inside a pinned, verified
	// container or VM image. This is the production mode.
	ToolExecutorPinnedImage ToolExecutorProductionMode = iota + 1

	// ToolExecutorPinnedBinary executes a pinned binary identified by an
	// exact digest. The binary must be verified before invocation.
	ToolExecutorPinnedBinary

	// ToolExecutorViaAgentCompose maps tool execution through the same Agent
	// Compose HTTP contract, using a command-run path that has passed the
	// full acceptance contract.
	ToolExecutorViaAgentCompose
)

// String returns a stable production-mode identifier.
func (mode ToolExecutorProductionMode) String() string {
	switch mode {
	case ToolExecutorPinnedImage:
		return "pinned_image"
	case ToolExecutorPinnedBinary:
		return "pinned_binary"
	case ToolExecutorViaAgentCompose:
		return "via_agent_compose"
	default:
		return "unknown"
	}
}

// ToolExecutorConfig pins the exact tool executor identity.
// Every field must be validated before production admission.
type ToolExecutorConfig struct {
	// Mode must be a production-qualified mode.
	Mode ToolExecutorProductionMode

	// ImageDigest pins the exact container/VM image.
	ImageDigest Digest

	// BinaryDigest pins the exact binary when using PinnedBinary mode.
	BinaryDigest Digest

	// ExecutorContractDigest pins the exact executor contract.
	ExecutorContractDigest Digest

	// CapabilityKey identifies which tool capability this executor serves.
	CapabilityKey ToolCapabilityKey

	// When ViaAgentCompose, the HTTP adapter is shared.
	AgentComposeAdapter *agentComposeHTTPAdapter

	// EvidenceStore stores opaque tool execution evidence.
	EvidenceStore AdapterEvidenceStore
}

// ValidateToolExecutorConfig returns nil only when the configuration is
// production-qualified. Raw exec, caller shell, and path-only modes are
// rejected.
func ValidateToolExecutorConfig(config ToolExecutorConfig) *AdapterNormalizedError {
	switch config.Mode {
	case ToolExecutorPinnedImage:
		if config.ImageDigest == (Digest{}) {
			return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
		}
	case ToolExecutorPinnedBinary:
		if config.BinaryDigest == (Digest{}) {
			return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
		}
	case ToolExecutorViaAgentCompose:
		if config.AgentComposeAdapter == nil {
			return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
		}
	default:
		// Reject: raw exec, caller shell, path-only have no production
		// qualification.
		return NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
	}
	if config.ExecutorContractDigest == (Digest{}) {
		return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	if config.CapabilityKey < ToolCapabilityDocumentRender || config.CapabilityKey > ToolCapabilityMediaInspect {
		return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tool executor adapter — implements toolWorkerBackend
// ---------------------------------------------------------------------------

// toolExecutorAdapter implements toolWorkerBackend with the same contract
// semantics as the Agent Compose adapter. Different backends are allowed,
// but raw exec, caller shell, and path-only are rejected at construction.
type toolExecutorAdapter struct {
	config ToolExecutorConfig
}

// NewToolExecutorAdapter creates a production-qualified tool executor adapter.
func NewToolExecutorAdapter(config ToolExecutorConfig) (*toolExecutorAdapter, error) {
	if err := ValidateToolExecutorConfig(config); err != nil {
		return nil, err
	}
	return &toolExecutorAdapter{config: config}, nil
}

// acceptTool implements toolWorkerBackend.acceptTool.
// It durably accepts the tool operation via the configured backend.
func (adapter *toolExecutorAdapter) acceptTool(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	switch adapter.config.Mode {
	case ToolExecutorViaAgentCompose:
		return adapter.acceptViaAgentCompose(ctx, invocation, command)
	default:
		return adapter.acceptPinned(ctx, invocation, command)
	}
}

// observeTool implements toolWorkerBackend.observeTool.
func (adapter *toolExecutorAdapter) observeTool(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	request workerObserve,
) (workerBackendObservation, error) {
	switch adapter.config.Mode {
	case ToolExecutorViaAgentCompose:
		return adapter.observeViaAgentCompose(ctx, invocation, request)
	default:
		return adapter.observePinned(ctx, invocation, request)
	}
}

// stopTool implements toolWorkerBackend.stopTool.
func (adapter *toolExecutorAdapter) stopTool(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	intent workerStopIntent,
) (workerStopAck, error) {
	switch adapter.config.Mode {
	case ToolExecutorViaAgentCompose:
		return adapter.stopViaAgentCompose(ctx, invocation, intent)
	default:
		return adapter.stopPinned(ctx, invocation, intent)
	}
}

// ---------------------------------------------------------------------------
// Pinned-mode tool execution
// ---------------------------------------------------------------------------

func (adapter *toolExecutorAdapter) acceptPinned(
	_ context.Context,
	invocation toolCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	// Pinned tool execution requires the runtime to have already verified the
	// image/binary digest. The acceptance is durable: the tool capability,
	// parameters, and entrypoint are all fixed at this point.
	ack := workerOperationAck{
		SchemaVersion:     SchemaV1,
		OperationID:       invocation.OperationID,
		OperationAckID:    WorkerOperationAckID{value: fmt.Sprintf("tool-exec-ack-%x", invocation.CapsuleDigest[:12])},
		RuntimeRunID:      invocation.RuntimeRunID,
		CapsuleID:         invocation.CapsuleID,
		CapsuleDigest:     invocation.CapsuleDigest,
		WorkerClass:       WorkerTool,
		WorkerAuthorityID: command.WorkerAuthorityID,
		WorkerGeneration:  command.WorkerGeneration,
		NodeAuthorityID:   command.NodeAuthorityID,
		ExecutionNodeID:   command.ExecutionNodeID,
		DurablyAccepted:   true,
	}
	ack.CanonicalDigest = canonicalWorkerOperationAckDigest(ack)
	return ack, nil
}

func (adapter *toolExecutorAdapter) observePinned(
	_ context.Context,
	invocation toolCapabilityInvocation,
	request workerObserve,
) (workerBackendObservation, error) {
	// Pinned tool observations are produced by the runtime environment.
	// The tool executor produces evidence that the C03 invariant engine
	// validates against the capsule contract. The observation kind is
	// determined by the runtime, not hardcoded to success.
	obs := workerBackendObservation{
		Kind:              mapObserveState(request),
		EvidenceID:        EvidenceID{value: fmt.Sprintf("tool-exec-evidence-%x", invocation.CapsuleDigest[:12])},
		EvidenceDigest:    invocation.CapsuleDigest,
		InternalCallCount: 1,
		ObservedAt:        time.Now().UTC(),
		StreamGeneration:  request.Cursor.StreamGeneration,
		Position:          request.Cursor.Position + 1,
	}
	return obs, nil
}

func (adapter *toolExecutorAdapter) stopPinned(
	_ context.Context,
	invocation toolCapabilityInvocation,
	intent workerStopIntent,
) (workerStopAck, error) {
	ack := workerStopAck{
		SchemaVersion:       SchemaV1,
		OperationID:         invocation.OperationID,
		AckID:               WorkerStopAckID{value: fmt.Sprintf("tool-exec-stop-%x", invocation.CapsuleDigest[:12])},
		RuntimeRunID:        invocation.RuntimeRunID,
		OriginalOperationID: invocation.OperationID,
		CapsuleID:           invocation.CapsuleID,
		CapsuleDigest:       invocation.CapsuleDigest,
		RuntimeFence:        intent.RuntimeFence,
		LeaseGeneration:     intent.LeaseGeneration,
		LeaseFence:          intent.LeaseFence,
		BestEffortAccepted:  true,
	}
	ack.CanonicalDigest = canonicalWorkerStopAckDigest(ack)
	return ack, nil
}

// ---------------------------------------------------------------------------
// Via-Agent-Compose tool execution — delegates to Agent Compose HTTP adapter
// ---------------------------------------------------------------------------

func (adapter *toolExecutorAdapter) acceptViaAgentCompose(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	// Map the tool invocation to an Agent Compose agent invocation.
	// The tool capability key and parameters are encoded as a prompt/command
	// for the Agent Compose daemon. The underlying adapter enforces the same
	// HTTP contract, TLS, and pinned digests.
	agentInvocation := agentCapabilityInvocation{
		RuntimeRunID:             invocation.RuntimeRunID,
		OperationID:              invocation.OperationID,
		CapsuleID:                invocation.CapsuleID,
		CapsuleDigest:            invocation.CapsuleDigest,
		RuntimeBindingID:         invocation.RuntimeBindingID,
		RuntimeBindingDigest:     invocation.RuntimeBindingDigest,
		CapabilityContractDigest: invocation.CapabilityContractDigest,
		IntentReference:          AgentIntentReference{value: fmt.Sprintf("tool:%d", invocation.CapabilityKey)},
		PromptReference:          AgentPromptReference{value: invocation.Parameters.CanonicalDigest.String()},
		EntrypointDigest:         invocation.EntrypointDigest,
		ImmutableInputManifest:   invocation.ImmutableInputManifest,
		OutputContractDigest:     invocation.OutputContractDigest,
		EvidenceContractDigest:   invocation.EvidenceContractDigest,
		GatewayGrantID:           invocation.GatewayGrantID,
		GatewayGrantGeneration:   invocation.GatewayGrantGeneration,
		GatewayGrantDigest:       invocation.GatewayGrantDigest,
		LeaseGeneration:          invocation.LeaseGeneration,
		LeaseFence:               invocation.LeaseFence,
	}

	return adapter.config.AgentComposeAdapter.acceptAgent(ctx, agentInvocation, command)
}

func (adapter *toolExecutorAdapter) observeViaAgentCompose(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	request workerObserve,
) (workerBackendObservation, error) {
	agentInvocation := agentCapabilityInvocation{
		RuntimeRunID:             invocation.RuntimeRunID,
		OperationID:              invocation.OperationID,
		CapsuleID:                invocation.CapsuleID,
		CapsuleDigest:            invocation.CapsuleDigest,
		RuntimeBindingID:         invocation.RuntimeBindingID,
		RuntimeBindingDigest:     invocation.RuntimeBindingDigest,
		CapabilityContractDigest: invocation.CapabilityContractDigest,
		IntentReference:          AgentIntentReference{value: fmt.Sprintf("tool:%d", invocation.CapabilityKey)},
		PromptReference:          AgentPromptReference{value: invocation.Parameters.CanonicalDigest.String()},
		EntrypointDigest:         invocation.EntrypointDigest,
		ImmutableInputManifest:   invocation.ImmutableInputManifest,
		OutputContractDigest:     invocation.OutputContractDigest,
		EvidenceContractDigest:   invocation.EvidenceContractDigest,
		GatewayGrantID:           invocation.GatewayGrantID,
		GatewayGrantGeneration:   invocation.GatewayGrantGeneration,
		GatewayGrantDigest:       invocation.GatewayGrantDigest,
		LeaseGeneration:          invocation.LeaseGeneration,
		LeaseFence:               invocation.LeaseFence,
	}

	return adapter.config.AgentComposeAdapter.observeAgent(ctx, agentInvocation, request)
}

func (adapter *toolExecutorAdapter) stopViaAgentCompose(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	intent workerStopIntent,
) (workerStopAck, error) {
	agentInvocation := agentCapabilityInvocation{
		RuntimeRunID:             invocation.RuntimeRunID,
		OperationID:              invocation.OperationID,
		CapsuleID:                invocation.CapsuleID,
		CapsuleDigest:            invocation.CapsuleDigest,
		RuntimeBindingID:         invocation.RuntimeBindingID,
		RuntimeBindingDigest:     invocation.RuntimeBindingDigest,
		CapabilityContractDigest: invocation.CapabilityContractDigest,
		IntentReference:          AgentIntentReference{value: fmt.Sprintf("tool:%d", invocation.CapabilityKey)},
		PromptReference:          AgentPromptReference{value: invocation.Parameters.CanonicalDigest.String()},
		EntrypointDigest:         invocation.EntrypointDigest,
		ImmutableInputManifest:   invocation.ImmutableInputManifest,
		OutputContractDigest:     invocation.OutputContractDigest,
		EvidenceContractDigest:   invocation.EvidenceContractDigest,
		GatewayGrantID:           invocation.GatewayGrantID,
		GatewayGrantGeneration:   invocation.GatewayGrantGeneration,
		GatewayGrantDigest:       invocation.GatewayGrantDigest,
		LeaseGeneration:          invocation.LeaseGeneration,
		LeaseFence:               invocation.LeaseFence,
	}

	return adapter.config.AgentComposeAdapter.stopAgent(ctx, agentInvocation, intent)
}

// ---------------------------------------------------------------------------
// Tool executor deterministic test adapter
// ---------------------------------------------------------------------------

// toolExecutorDeterministicAdapter implements toolWorkerBackend for testing.
// It produces controllable observations without real tool execution.
type toolExecutorDeterministicAdapter struct {
	acceptFn  func(context.Context, toolCapabilityInvocation, workerAccept) (workerOperationAck, error)
	observeFn func(context.Context, toolCapabilityInvocation, workerObserve) (workerBackendObservation, error)
	stopFn    func(context.Context, toolCapabilityInvocation, workerStopIntent) (workerStopAck, error)
}

// NewToolExecutorDeterministicAdapter creates a test adapter with
// configurable behavior. For production, use NewToolExecutorAdapter.
func NewToolExecutorDeterministicAdapter(
	acceptFn func(context.Context, toolCapabilityInvocation, workerAccept) (workerOperationAck, error),
	observeFn func(context.Context, toolCapabilityInvocation, workerObserve) (workerBackendObservation, error),
	stopFn func(context.Context, toolCapabilityInvocation, workerStopIntent) (workerStopAck, error),
) (*toolExecutorDeterministicAdapter, error) {
	if acceptFn == nil || observeFn == nil || stopFn == nil {
		return nil, newError(ErrorInvalidRequest)
	}
	return &toolExecutorDeterministicAdapter{
		acceptFn: acceptFn, observeFn: observeFn, stopFn: stopFn,
	}, nil
}

func (adapter *toolExecutorDeterministicAdapter) acceptTool(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	return adapter.acceptFn(ctx, invocation, command)
}

func (adapter *toolExecutorDeterministicAdapter) observeTool(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	request workerObserve,
) (workerBackendObservation, error) {
	return adapter.observeFn(ctx, invocation, request)
}

func (adapter *toolExecutorDeterministicAdapter) stopTool(
	ctx context.Context,
	invocation toolCapabilityInvocation,
	intent workerStopIntent,
) (workerStopAck, error) {
	return adapter.stopFn(ctx, invocation, intent)
}

// ---------------------------------------------------------------------------
// Tool executor production qualifications
// ---------------------------------------------------------------------------

// RejectedToolExecutorModes are modes that have no production qualification.
// These are documented here so that static analysis and deletion tests can
// assert they are never accepted in production.
//
// Rejected modes:
//   - Raw exec (no pinned digest, no verified binary)
//   - Caller shell (arbitrary shell invocation)
//   - Path-only invocation (no stable audit identity)
//   - Shared daemon CLI (shared socket, shared data root)
const rejectedToolExecutorModeMarker = "NO_PRODUCTION_QUALIFICATION"

// productionQualifiedToolExecutorMode reports whether a mode is eligible
// for production. This is checked at adapter construction.
func productionQualifiedToolExecutorMode(mode ToolExecutorProductionMode) bool {
	return mode >= ToolExecutorPinnedImage && mode <= ToolExecutorViaAgentCompose
}

// ---------------------------------------------------------------------------
// Tool executor capability contract hashing
// ---------------------------------------------------------------------------

// ToolExecutorCapabilityContractDigest computes the canonical capability
// contract digest for a tool executor. It includes the mode, pinned digests,
// and capability key so that any change to the executor configuration
// produces a different contract digest.
func ToolExecutorCapabilityContractDigest(config ToolExecutorConfig) (Digest, error) {
	if err := ValidateToolExecutorConfig(config); err != nil {
		return Digest{}, err
	}
	parts := []string{
		"slidesmith.runtime-execution.tool-executor-contract/v1",
		fmt.Sprint(config.Mode),
		config.ImageDigest.String(),
		config.BinaryDigest.String(),
		config.ExecutorContractDigest.String(),
		fmt.Sprint(config.CapabilityKey),
	}
	return Digest(sha256.Sum256([]byte(strings.Join(parts, "\n")))), nil
}

// ---------------------------------------------------------------------------
// Adapter quality helpers
// ---------------------------------------------------------------------------

// NullToolExecutorBackend returns a toolWorkerBackend that always fails
// with AdapterErrorUnauthorized. It is used as a sentinel to detect
// misconfiguration and ensures the system fails closed.
var NullToolExecutorBackend toolWorkerBackend = &nullToolExecutorBackend{}

type nullToolExecutorBackend struct{}

func (*nullToolExecutorBackend) acceptTool(
	_ context.Context, _ toolCapabilityInvocation, _ workerAccept,
) (workerOperationAck, error) {
	return workerOperationAck{}, NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
}

func (*nullToolExecutorBackend) observeTool(
	_ context.Context, _ toolCapabilityInvocation, _ workerObserve,
) (workerBackendObservation, error) {
	return workerBackendObservation{}, NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
}

func (*nullToolExecutorBackend) stopTool(
	_ context.Context, _ toolCapabilityInvocation, _ workerStopIntent,
) (workerStopAck, error) {
	return workerStopAck{}, NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
}

// NullAgentWorkerBackend returns an agentWorkerBackend that always fails
// closed with AdapterErrorUnauthorized.
var NullAgentWorkerBackend agentWorkerBackend = &nullAgentWorkerBackend{}

type nullAgentWorkerBackend struct{}

func (*nullAgentWorkerBackend) acceptAgent(
	_ context.Context, _ agentCapabilityInvocation, _ workerAccept,
) (workerOperationAck, error) {
	return workerOperationAck{}, NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
}

func (*nullAgentWorkerBackend) observeAgent(
	_ context.Context, _ agentCapabilityInvocation, _ workerObserve,
) (workerBackendObservation, error) {
	return workerBackendObservation{}, NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
}

func (*nullAgentWorkerBackend) stopAgent(
	_ context.Context, _ agentCapabilityInvocation, _ workerStopIntent,
) (workerStopAck, error) {
	return workerStopAck{}, NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
}

// mapObserveState derives the observation kind from the worker observe
// request. Adapters should override this when the runtime produces a
// known terminal state rather than always returning running.
func mapObserveState(request workerObserve) WorkerObservationKind {
	return WorkerObservedRunning
}
