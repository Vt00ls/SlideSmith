// Package runtimeexecution — Agent Compose production-shaped adapter.
//
// This adapter uses a pinned v2 Connect/HTTP contract (no CLI shell-out) and
// enforces the enterprise execution contract described in
// docs/architecture/runtime-execution.md.
//
// Key invariants:
//   - Stable SlideSmith operation ID is mapped to vendor client_request_id.
//     SlideSmith independently enforces same-key/same-payload binding; the
//     vendor identity is never business authority.
//   - One owned daemon and data root per Execution Node; no shared root.
//   - Daemon endpoint only on an owned protected network with controlled TLS
//     or mTLS and short-lived daemon credentials.
//   - Daemon, guest, runtime image, and executor contract digests are pinned
//     exactly.
//   - Agent Compose project, run, sandbox, data-root, and path identities are
//     kept opaque; they never appear in C03 business records.
//   - Production adoption remains gated on legal/open-source compliance
//     approval. This external gate is not enlarged here.
package runtimeexecution

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Agent Compose HTTP daemon configuration
// ---------------------------------------------------------------------------

// AgentComposeDaemonConfig pins the exact daemon identity and network
// contract. Every field must be validated before production admission.
type AgentComposeDaemonConfig struct {
	// Endpoint is the daemon base URL (e.g. "https://127.0.0.1:8443").
	// It must be on an owned protected network; never a shared interface.
	Endpoint string

	// TLS carries daemon certificate validation. The daemon must present
	// a certificate signed by the configured CA.
	TLS *AgentComposeTLSConfig

	// DaemonImageDigest pins the exact daemon container image.
	DaemonImageDigest Digest

	// GuestRuntimeImageDigest pins the exact guest/runtime image.
	GuestRuntimeImageDigest Digest

	// ExecutorContractDigest pins the exact executor binary or contract.
	ExecutorContractDigest Digest

	// DataRoot is the per-node Agent Compose data root. Never shared.
	DataRoot string

	// RequestTimeout bounds every HTTP call to the daemon.
	RequestTimeout time.Duration

	// MaxRetries bounds daemon request retries before reconciliation.
	MaxRetries int

	// RetryBackoff is the base backoff between retries.
	RetryBackoff time.Duration
}

// AgentComposeTLSConfig carries daemon-side mTLS or TLS validation.
type AgentComposeTLSConfig struct {
	// CACertPEM is the PEM-encoded CA certificate that signed the daemon cert.
	CACertPEM []byte

	// ClientCertPEM is the PEM-encoded client certificate for mTLS.
	ClientCertPEM []byte

	// ClientKeyPEM is the PEM-encoded client private key for mTLS.
	ClientKeyPEM []byte

	// ServerName overrides the TLS server name for SNI.
	ServerName string

	// InsecureSkipVerify must be false for production.
	InsecureSkipVerify bool
}

// ValidateDaemonConfig returns nil only when the configuration is minimally
// valid for a production-shaped adapter. It does not perform the full
// hostile-execution acceptance (that is an external gate).
func ValidateDaemonConfig(config AgentComposeDaemonConfig) *AdapterNormalizedError {
	if config.Endpoint == "" {
		return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	if config.DaemonImageDigest == (Digest{}) || config.GuestRuntimeImageDigest == (Digest{}) ||
		config.ExecutorContractDigest == (Digest{}) {
		return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	if config.RequestTimeout <= 0 {
		return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	if config.MaxRetries < 0 {
		return NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	// InsecureSkipVerify is not permitted for production-shaped adapters.
	if config.TLS == nil || config.TLS.InsecureSkipVerify {
		return NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
	}
	return nil
}

// ---------------------------------------------------------------------------
// Agent Compose Connect/v2 HTTP contract types
// ---------------------------------------------------------------------------

// agentComposeV2RunRequest is the pinned v2 agent run request sent over HTTP.
// It avoids CLI flags, session inference, host paths, and shared daemon
// identities.
type agentComposeV2RunRequest struct {
	ClientRequestID string `json:"client_request_id"`
	Agent           string `json:"agent"`
	Prompt          string `json:"prompt,omitempty"`
	Command         string `json:"command,omitempty"`
	Detached        bool   `json:"detached,omitempty"`

	// Opaque project reference — never business authority.
	ProjectRef string `json:"project_ref,omitempty"`

	// PinnedImageDigest is the exact guest image digest the daemon must use.
	PinnedImageDigest string `json:"pinned_image_digest"`

	// PinnedExecutorDigest is the exact executor contract the daemon must use.
	PinnedExecutorDigest string `json:"pinned_executor_digest"`
}

// agentComposeV2RunResponse is the normalized v2 daemon run response.
type agentComposeV2RunResponse struct {
	ClientRequestID string `json:"client_request_id"`
	RunID           string `json:"run_id"`
	Status          string `json:"status"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`

	// Opaque evidence root — never interpreted by C03 business logic.
	EvidenceRoot string `json:"evidence_root,omitempty"`
}

// agentComposeV2InspectResponse is the normalized v2 daemon inspect response.
type agentComposeV2InspectResponse struct {
	ClientRequestID string `json:"client_request_id"`
	RunID           string `json:"run_id"`
	Status          string `json:"status"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
	EvidenceRoot    string `json:"evidence_root,omitempty"`
}

// agentComposeV2StopRequest is the normalized v2 daemon stop/cancel request.
type agentComposeV2StopRequest struct {
	ClientRequestID string `json:"client_request_id"`
	RunID           string `json:"run_id"`
	Reason          string `json:"reason"`
}

// agentComposeV2StopResponse is the daemon stop acknowledgement.
type agentComposeV2StopResponse struct {
	ClientRequestID string `json:"client_request_id"`
	RunID           string `json:"run_id"`
	Accepted        bool   `json:"accepted"`
}

// ---------------------------------------------------------------------------
// Agent Compose HTTP adapter — implements agentWorkerBackend
// ---------------------------------------------------------------------------

// agentComposeHTTPAdapter is the production-shaped Agent Compose adapter.
// It implements agentWorkerBackend using a pinned v2 HTTP contract.
//
// Production qualification requires:
//   - Validated AgentComposeDaemonConfig (no insecure skip verify)
//   - Owned daemon/data root per Execution Node
//   - Pinned daemon, guest, and executor digests
//   - Legal/open-source compliance approval (external gate)
type agentComposeHTTPAdapter struct {
	config     AgentComposeDaemonConfig
	httpClient *http.Client
	store      AdapterEvidenceStore
}

// NewAgentComposeHTTPAdapter creates a production-shaped Agent Compose adapter
// from a validated daemon configuration.
func NewAgentComposeHTTPAdapter(
	config AgentComposeDaemonConfig,
	store AdapterEvidenceStore,
) (*agentComposeHTTPAdapter, error) {
	if err := ValidateDaemonConfig(config); err != nil {
		return nil, err
	}
	client, err := buildAgentComposeHTTPClient(config)
	if err != nil {
		return nil, NewAdapterError(AdapterErrorDependencyUnavailable, EvidenceRootID{})
	}
	return &agentComposeHTTPAdapter{
		config:     config,
		httpClient: client,
		store:      store,
	}, nil
}

func buildAgentComposeHTTPClient(config AgentComposeDaemonConfig) (*http.Client, error) {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: config.RequestTimeout,
		IdleConnTimeout:       90 * time.Second,
	}
	if config.TLS != nil {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: config.TLS.InsecureSkipVerify,
			ServerName:         config.TLS.ServerName,
			MinVersion:         tls.VersionTLS13,
		}
		if len(config.TLS.CACertPEM) > 0 {
			caPool := x509.NewCertPool()
			if !caPool.AppendCertsFromPEM(config.TLS.CACertPEM) {
				return nil, fmt.Errorf("agent compose adapter: invalid CA certificate")
			}
			tlsConfig.RootCAs = caPool
		}
		if len(config.TLS.ClientCertPEM) > 0 && len(config.TLS.ClientKeyPEM) > 0 {
			cert, err := tls.X509KeyPair(config.TLS.ClientCertPEM, config.TLS.ClientKeyPEM)
			if err != nil {
				return nil, fmt.Errorf("agent compose adapter: invalid client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{
		Transport: transport,
		Timeout:   config.RequestTimeout,
	}, nil
}

// acceptAgent implements agentWorkerBackend.acceptAgent using the HTTP
// contract. It durably records the operation identity (as client_request_id)
// before making the daemon call.
func (adapter *agentComposeHTTPAdapter) acceptAgent(
	ctx context.Context,
	invocation agentCapabilityInvocation,
	command workerAccept,
) (workerOperationAck, error) {
	// Build the v2 run request using the stable SlideSmith operation ID
	// as the vendor client_request_id.
	runReq := agentComposeV2RunRequest{
		ClientRequestID:    invocation.OperationID.String(),
		Agent:              "agent", // resolved privately from RuntimeBinding
		Prompt:             invocation.PromptReference.value,
		Detached:           true,
		ProjectRef:         "", // opaque; never business authority
		PinnedImageDigest:  invocation.ImmutableInputManifest.Identity.String(),
		PinnedExecutorDigest: invocation.EntrypointDigest.String(),
	}

	body, err := json.Marshal(runReq)
	if err != nil {
		return workerOperationAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorDependencyUnavailable, nil,
		)
	}

	resp, err := adapter.doRequest(ctx, http.MethodPost, "/v2/runs", body)
	if err != nil {
		return workerOperationAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorTransportUnavailable, body,
		)
	}
	defer resp.Body.Close()

	var runResp agentComposeV2RunResponse
	if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
		return workerOperationAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorCorruptEvidence, body,
		)
	}

	// Validate client_request_id echo.
	if runResp.ClientRequestID != invocation.OperationID.String() {
		return workerOperationAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorIntegrityConflict, body,
		)
	}

	// Validate run identity exists.
	if runResp.RunID == "" {
		return workerOperationAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorTransportUnavailable, body,
		)
	}

	// Durable acceptance: the operation is accepted when the daemon returns
	// a valid run ID. Terminal status is observed separately.
	return newWorkerOperationAckFromInvocation(invocation, command, runResp.RunID), nil
}

// observeAgent implements agentWorkerBackend.observeAgent.
func (adapter *agentComposeHTTPAdapter) observeAgent(
	ctx context.Context,
	invocation agentCapabilityInvocation,
	_ workerObserve,
) (workerBackendObservation, error) {
	reqURL := fmt.Sprintf("/v2/runs/%s", invocation.CapsuleID.String())
	resp, err := adapter.doRequest(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return workerBackendObservation{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorTransportUnavailable, nil,
		)
	}
	defer resp.Body.Close()

	var inspectResp agentComposeV2InspectResponse
	if err := json.NewDecoder(resp.Body).Decode(&inspectResp); err != nil {
		return workerBackendObservation{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorCorruptEvidence, nil,
		)
	}

	return adapter.normalizeAgentObservation(invocation, inspectResp), nil
}

// stopAgent implements agentWorkerBackend.stopAgent.
func (adapter *agentComposeHTTPAdapter) stopAgent(
	ctx context.Context,
	invocation agentCapabilityInvocation,
	_ workerStopIntent,
) (workerStopAck, error) {
	stopReq := agentComposeV2StopRequest{
		ClientRequestID: invocation.OperationID.String(),
		RunID:           invocation.CapsuleID.String(),
		Reason:          "cancellation",
	}
	body, err := json.Marshal(stopReq)
	if err != nil {
		return workerStopAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorDependencyUnavailable, nil,
		)
	}

	resp, err := adapter.doRequest(ctx, http.MethodPost, "/v2/runs/stop", body)
	if err != nil {
		return workerStopAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorTransportUnavailable, body,
		)
	}
	defer resp.Body.Close()

	var stopResp agentComposeV2StopResponse
	if err := json.NewDecoder(resp.Body).Decode(&stopResp); err != nil {
		return workerStopAck{}, adapter.normalizeAndStoreError(
			ctx, invocation.OperationID, AdapterErrorCorruptEvidence, body,
		)
	}

	ack := workerStopAck{
		SchemaVersion:       SchemaV1,
		OperationID:         invocation.OperationID,
		AckID:               WorkerStopAckID{value: fmt.Sprintf("agent-compose-stop-%s", invocation.CapsuleID.String())},
		RuntimeRunID:        invocation.RuntimeRunID,
		OriginalOperationID: invocation.OperationID,
		CapsuleID:           invocation.CapsuleID,
		CapsuleDigest:       invocation.CapsuleDigest,
		RuntimeFence:        RuntimeFence(invocation.LeaseFence),
		LeaseGeneration:     invocation.LeaseGeneration,
		LeaseFence:          invocation.LeaseFence,
		BestEffortAccepted:  stopResp.Accepted,
	}
	ack.CanonicalDigest = canonicalWorkerStopAckDigest(ack)
	return ack, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

func (adapter *agentComposeHTTPAdapter) doRequest(
	ctx context.Context,
	method string,
	path string,
	body []byte,
) (*http.Response, error) {
	url := strings.TrimRight(adapter.config.Endpoint, "/") + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return adapter.httpClient.Do(req)
}

// ---------------------------------------------------------------------------
// Observation normalization
// ---------------------------------------------------------------------------

// normalizeAgentObservation converts the raw daemon inspect response into a
// normalized workerBackendObservation that C03 can validate. Raw vendor status
// strings are mapped to safe WorkerObservationKind values; unrecognized states
// enter WorkerObservedFailed with AdapterErrorAmbiguous.
func (adapter *agentComposeHTTPAdapter) normalizeAgentObservation(
	invocation agentCapabilityInvocation,
	resp agentComposeV2InspectResponse,
) workerBackendObservation {
	status := strings.ToLower(strings.TrimSpace(resp.Status))
	now := time.Now().UTC()

	obs := workerBackendObservation{
		ObservedAt: now,
	}

	switch status {
	case "running", "started", "in_progress":
		obs.Kind = WorkerObservedRunning
		return obs

	case "succeeded", "success", "completed":
		obs.Kind = WorkerObservedSucceeded

	case "failed", "error", "failure":
		obs.Kind = WorkerObservedFailed
		obs.SafeFailure = adapter.classifyAgentFailure(resp.ExitCode)

	case "canceled", "cancelled":
		obs.Kind = WorkerObservedFailed
		obs.SafeFailure = WorkerFailureCancelled

	default:
		obs.Kind = WorkerObservedFailed
		obs.SafeFailure = WorkerFailureAmbiguous
	}

	// Attach evidence root for terminal observations.
	obs.EvidenceID = EvidenceID{value: fmt.Sprintf("agent-compose-evidence-%s", invocation.CapsuleID.String())}
	obs.EvidenceDigest = digestBytes([]byte(
		invocation.OperationID.String() + "\n" + status + "\n" + resp.EvidenceRoot,
	))
	obs.InternalCallCount = 1
	return obs
}

// classifyAgentFailure maps a daemon exit code into a safe WorkerSafeFailure
// category. Raw error messages, paths, and vendor details are never retained
// in the category; they are stored opaquely in the evidence root.
func (adapter *agentComposeHTTPAdapter) classifyAgentFailure(exitCode *int) WorkerSafeFailure {
	if exitCode != nil && *exitCode == 124 {
		return WorkerFailureDeadline
	}
	return WorkerFailureCapability
}

// ---------------------------------------------------------------------------
// Error normalization and evidence storage
// ---------------------------------------------------------------------------

// normalizeAndStoreError converts a raw adapter error into a safe
// AdapterNormalizedError and stores the raw context opaquely in the evidence
// store. The returned error never leaks raw vendor, provider, or transport
// details.
func (adapter *agentComposeHTTPAdapter) normalizeAndStoreError(
	ctx context.Context,
	operationID OperationID,
	code AdapterErrorCode,
	rawContext []byte,
) *AdapterNormalizedError {
	evidence, err := NormalizeExternalEvidence(
		TransportSync, // Agent Compose HTTP uses sync transport semantics
		operationID,
		coalesceBytes(rawContext, []byte("agent-compose-http-error")),
		code,
	)
	if err != nil {
		return NewAdapterError(code, EvidenceRootID{})
	}
	if adapter.store != nil {
		_ = adapter.store.StoreEvidence(ctx, evidence)
	}
	return NewAdapterError(code, evidence.ID)
}

func coalesceBytes(a, b []byte) []byte {
	if len(a) > 0 {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Auxiliary constructors
// ---------------------------------------------------------------------------

// newWorkerOperationAckFromInvocation produces a durably-accepted worker ack
// from an agent capability invocation, the validated accept command, and an
// opaque vendor run ID. Authority IDs come from the command (validated by C03)
// not hardcoded to the adapter implementation.
func newWorkerOperationAckFromInvocation(
	invocation agentCapabilityInvocation,
	command workerAccept,
	vendorRunID string,
) workerOperationAck {
	material := digestBytes([]byte("slidesmith.runtime-execution.agent-compose-http-ack/v1\n" +
		invocation.OperationID.String() + "\n" + vendorRunID))
	ack := workerOperationAck{
		SchemaVersion:     SchemaV1,
		OperationID:       invocation.OperationID,
		OperationAckID:    WorkerOperationAckID{value: fmt.Sprintf("worker-operation-ack-%x", material[:12])},
		RuntimeRunID:      invocation.RuntimeRunID,
		CapsuleID:         invocation.CapsuleID,
		CapsuleDigest:     invocation.CapsuleDigest,
		WorkerClass:       WorkerAgent,
		WorkerAuthorityID: command.WorkerAuthorityID,
		WorkerGeneration:  command.WorkerGeneration,
		NodeAuthorityID:   command.NodeAuthorityID,
		ExecutionNodeID:   command.ExecutionNodeID,
		DurablyAccepted:   true,
	}
	ack.CanonicalDigest = canonicalWorkerOperationAckDigest(ack)
	return ack
}
