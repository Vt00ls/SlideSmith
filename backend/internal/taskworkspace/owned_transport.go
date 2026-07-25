package taskworkspace

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
)

type (
	OwnedTransportMachineAuthorityID string
	OwnedTransportMethod             string
	OwnedTransportFailure            string
	OwnedTransportScopeKind          string
	OwnedTransportSignalResult       string
	OwnedTransportDeliveryKind       string
)

const (
	ownedTransportConfirmTaskWorkspace     OwnedTransportMethod = "confirm_task_workspace"
	ownedTransportMaterialize              OwnedTransportMethod = "materialize"
	ownedTransportOpenRuntimeView          OwnedTransportMethod = "open_runtime_view"
	ownedTransportCommitRuntimeView        OwnedTransportMethod = "commit_runtime_view"
	ownedTransportDiscardRuntimeView       OwnedTransportMethod = "discard_runtime_view"
	ownedTransportFenceRuntimeView         OwnedTransportMethod = "fence_runtime_view"
	ownedTransportExpireMaterialization    OwnedTransportMethod = "expire_materialization"
	ownedTransportExpireRuntimeView        OwnedTransportMethod = "expire_runtime_view"
	ownedTransportRestoreTaskWorkspace     OwnedTransportMethod = "restore_task_workspace"
	ownedTransportReconstructTaskWorkspace OwnedTransportMethod = "reconstruct_task_workspace"
	ownedTransportInspectRetention         OwnedTransportMethod = "inspect_checkpoint_retention"
	ownedTransportAttachRetention          OwnedTransportMethod = "attach_checkpoint_retention"
	ownedTransportReleaseRetention         OwnedTransportMethod = "release_checkpoint_retention"
	ownedTransportReclaimCheckpoint        OwnedTransportMethod = "reclaim_checkpoint"
	ownedTransportObserveInventory         OwnedTransportMethod = "observe_checkpoint_inventory"
	ownedTransportCreateCleanupObligation  OwnedTransportMethod = "create_cleanup_obligation"
	ownedTransportInspectCleanupDebt       OwnedTransportMethod = "inspect_cleanup_debt"
	ownedTransportClaimCleanupDebt         OwnedTransportMethod = "claim_cleanup_debt"
	ownedTransportReconcileCleanupDebt     OwnedTransportMethod = "reconcile_cleanup_debt"
	ownedTransportResolveCleanupDebt       OwnedTransportMethod = "resolve_cleanup_debt"
	ownedTransportReopenCleanupDebt        OwnedTransportMethod = "reopen_cleanup_debt"
	ownedTransportRebuildAuditDelivery     OwnedTransportMethod = "rebuild_audit_delivery"
	ownedTransportRebuildProjections       OwnedTransportMethod = "rebuild_projections"
	ownedTransportQueryDiagnostics         OwnedTransportMethod = "query_administrator_diagnostics"
	ownedTransportAcceptLegacyCleanup      OwnedTransportMethod = "accept_legacy_cleanup_obligation"
	ownedTransportInspectOperation         OwnedTransportMethod = "inspect_operation"
	ownedTransportReconcileOperation       OwnedTransportMethod = "reconcile_operation"

	OwnedTransportResponseLoss         OwnedTransportFailure = "response_loss"
	OwnedTransportDuplicateDelivery    OwnedTransportFailure = "duplicate_delivery"
	OwnedTransportOutOfOrderDelivery   OwnedTransportFailure = "out_of_order_delivery"
	OwnedTransportQueueClaimLoss       OwnedTransportFailure = "queue_claim_loss"
	OwnedTransportCallbackReplay       OwnedTransportFailure = "callback_replay"
	OwnedTransportTimeoutAfterDelivery OwnedTransportFailure = "timeout_after_delivery"
	OwnedTransportNonCanonicalPayload  OwnedTransportFailure = "non_canonical_payload"
	OwnedTransportUnsafeFailure        OwnedTransportFailure = "unsafe_dependency_failure"
	OwnedTransportForgedResponse       OwnedTransportFailure = "forged_response"
	OwnedTransportForgedCallback       OwnedTransportFailure = "forged_callback"
	OwnedTransportForgedErrorResponse  OwnedTransportFailure = "forged_error_response"
	OwnedTransportForgedErrorCallback  OwnedTransportFailure = "forged_error_callback"
	OwnedTransportUnknownWireError     OwnedTransportFailure = "unknown_wire_error"

	OwnedTransportTaskScope   OwnedTransportScopeKind = "task"
	OwnedTransportModuleScope OwnedTransportScopeKind = "module"

	OwnedTransportSignalSuccess   OwnedTransportSignalResult = "success"
	OwnedTransportSignalError     OwnedTransportSignalResult = "error"
	OwnedTransportSignalAmbiguous OwnedTransportSignalResult = "ambiguous"

	OwnedTransportDeliveryOriginal   OwnedTransportDeliveryKind = "original"
	OwnedTransportDeliveryDuplicate  OwnedTransportDeliveryKind = "duplicate"
	OwnedTransportDeliveryRedelivery OwnedTransportDeliveryKind = "redelivery"
)

type OwnedTransportMachineAuthority struct {
	ID         OwnedTransportMachineAuthorityID
	Generation Generation
	ExpiresAt  Instant
}

// OwnedTransportBinding is the transport-owned retry identity. A binding store
// must atomically retain the first value for its scope/method/OperationID key so
// adapter and client restarts cannot extend its deadline or change authority.
type OwnedTransportBinding struct {
	ScopeKind                  OwnedTransportScopeKind
	Method                     OwnedTransportMethod
	MachineAuthorityID         OwnedTransportMachineAuthorityID
	MachineAuthorityGeneration Generation
	PolicyDomainID             PolicyDomainID
	TaskID                     TaskID
	OperationID                OperationID
	CanonicalRequestDigest     Digest
	PayloadDigest              Digest
	Deadline                   Instant
	Generation                 Generation
	Fence                      Fence
}

type OwnedTransportBindingStore interface {
	LoadOrStore(OwnedTransportBinding) (binding OwnedTransportBinding, loaded bool)
}

type inMemoryOwnedTransportBindingStore struct {
	mu       sync.Mutex
	bindings map[ownedTransportCallKey]OwnedTransportBinding
}

func NewOwnedTransportBindingStore() OwnedTransportBindingStore {
	return &inMemoryOwnedTransportBindingStore{bindings: make(map[ownedTransportCallKey]OwnedTransportBinding)}
}

func (s *inMemoryOwnedTransportBindingStore) LoadOrStore(
	binding OwnedTransportBinding,
) (OwnedTransportBinding, bool) {
	key := ownedTransportBindingKey(binding)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.bindings[key]; ok {
		return existing, true
	}
	s.bindings[key] = binding
	return binding, false
}

type OwnedTransportAuthorityScope struct {
	ScopeKind                  OwnedTransportScopeKind
	MachineAuthorityID         OwnedTransportMachineAuthorityID
	MachineAuthorityGeneration Generation
	PolicyDomainID             PolicyDomainID
	TaskID                     TaskID
	OperationID                OperationID
	Generation                 Generation
	Fence                      Fence
}

type OwnedTransportEnvelope struct {
	ScopeKind                  OwnedTransportScopeKind
	Method                     OwnedTransportMethod
	MachineAuthorityID         OwnedTransportMachineAuthorityID
	MachineAuthorityGeneration Generation
	PolicyDomainID             PolicyDomainID
	TaskID                     TaskID
	OperationID                OperationID
	CanonicalRequestDigest     Digest
	PayloadDigest              Digest
	Deadline                   Instant
	Generation                 Generation
	Fence                      Fence
	Payload                    []byte
	AuthenticationTag          Digest
}

type OwnedTransportRequest struct {
	Envelope OwnedTransportEnvelope
}

type OwnedTransportWireError struct {
	Code                   ErrorCode
	SafeCategory           SafeErrorCategory
	Retryable              bool
	ReconciliationRequired bool
}

type OwnedTransportResponse struct {
	Method                 OwnedTransportMethod
	OperationID            OperationID
	CanonicalRequestDigest Digest
	PayloadDigest          Digest
	Payload                []byte
	Error                  *OwnedTransportWireError
	AuthenticationTag      Digest
}

type OwnedTransportCallback struct {
	Method                 OwnedTransportMethod
	OperationID            OperationID
	CanonicalRequestDigest Digest
	PayloadDigest          Digest
	Payload                []byte
	Error                  *OwnedTransportWireError
	AuthenticationTag      Digest
}

type OwnedTransportOperationalSignal struct {
	Method                 OwnedTransportMethod
	Result                 OwnedTransportSignalResult
	SafeCategory           SafeErrorCategory
	Retryable              bool
	ReconciliationRequired bool
}

type OwnedTransportDelivery struct {
	Sequence        uint64
	Kind            OwnedTransportDeliveryKind
	ClaimGeneration Generation
	Method          OwnedTransportMethod
	OperationID     OperationID
}

type OwnedTransportRoundTripper interface {
	RoundTrip(context.Context, OwnedTransportRequest) (OwnedTransportResponse, error)
}

type OwnedTransportClientConfig struct {
	Transport                 OwnedTransportRoundTripper
	MachineAuthority          OwnedTransportMachineAuthority
	AuthenticationKey         []byte
	ResponseAuthenticationKey []byte
	Now                       func() Instant
	RequestLifetime           Duration
	BindingStore              OwnedTransportBindingStore
}

type OwnedTransportHarnessConfig struct {
	Lifecycle                 Lifecycle
	MachineAuthority          OwnedTransportMachineAuthority
	AuthenticationKey         []byte
	ResponseAuthenticationKey []byte
	Authorize                 func(OwnedTransportAuthorityScope) bool
	Now                       func() Instant
	RequestLifetime           Duration
	BindingStore              OwnedTransportBindingStore
}

type ownedTransportCallBinding struct {
	requestDigest Digest
	deadline      Instant
	generation    Generation
	fence         Fence
}

type ownedTransportCallKey struct {
	scopeKind      OwnedTransportScopeKind
	method         OwnedTransportMethod
	policyDomainID PolicyDomainID
	taskID         TaskID
	operationID    OperationID
}

type ownedTransportCallbackKey struct {
	method                 OwnedTransportMethod
	operationID            OperationID
	canonicalRequestDigest Digest
}

type ownedTransportClient struct {
	transport                 OwnedTransportRoundTripper
	authority                 OwnedTransportMachineAuthority
	authenticationKey         []byte
	responseAuthenticationKey []byte
	now                       func() Instant
	requestLifetime           Duration
	bindingStore              OwnedTransportBindingStore
}

func NewOwnedTransportClient(config OwnedTransportClientConfig) Lifecycle {
	responseAuthenticationKey := config.ResponseAuthenticationKey
	if len(responseAuthenticationKey) == 0 {
		responseAuthenticationKey = config.AuthenticationKey
	}
	return &ownedTransportClient{
		transport:                 config.Transport,
		authority:                 config.MachineAuthority,
		authenticationKey:         append([]byte(nil), config.AuthenticationKey...),
		responseAuthenticationKey: append([]byte(nil), responseAuthenticationKey...),
		now:                       ownedTransportNow(config.Now),
		requestLifetime:           ownedTransportLifetime(config.RequestLifetime),
		bindingStore:              ownedTransportBindingStore(config.BindingStore),
	}
}

type OwnedTransportHarness struct {
	lifecycle                 Lifecycle
	authority                 OwnedTransportMachineAuthority
	authenticationKey         []byte
	responseAuthenticationKey []byte
	authorize                 func(OwnedTransportAuthorityScope) bool
	now                       func() Instant
	bindingStore              OwnedTransportBindingStore
	client                    Lifecycle

	mu                   sync.Mutex
	connected            bool
	failures             []OwnedTransportFailure
	requests             []OwnedTransportRequest
	responses            []OwnedTransportResponse
	callbacks            []OwnedTransportCallback
	acceptedCallbacks    []OwnedTransportCallback
	acceptedCallbackTags map[ownedTransportCallbackKey]Digest
	signals              []OwnedTransportOperationalSignal
	deliveries           []OwnedTransportDelivery
}

func NewOwnedTransportHarness(config OwnedTransportHarnessConfig) *OwnedTransportHarness {
	now := ownedTransportNow(config.Now)
	bindingStore := ownedTransportBindingStore(config.BindingStore)
	responseAuthenticationKey := config.ResponseAuthenticationKey
	if len(responseAuthenticationKey) == 0 {
		responseAuthenticationKey = config.AuthenticationKey
	}
	harness := &OwnedTransportHarness{
		lifecycle:                 config.Lifecycle,
		authority:                 config.MachineAuthority,
		authenticationKey:         append([]byte(nil), config.AuthenticationKey...),
		responseAuthenticationKey: append([]byte(nil), responseAuthenticationKey...),
		authorize:                 config.Authorize,
		now:                       now,
		bindingStore:              bindingStore,
		connected:                 true,
		acceptedCallbackTags:      make(map[ownedTransportCallbackKey]Digest),
	}
	harness.client = NewOwnedTransportClient(OwnedTransportClientConfig{
		Transport:                 harness,
		MachineAuthority:          config.MachineAuthority,
		AuthenticationKey:         config.AuthenticationKey,
		ResponseAuthenticationKey: responseAuthenticationKey,
		Now:                       now,
		RequestLifetime:           config.RequestLifetime,
		BindingStore:              bindingStore,
	})
	return harness
}

func (h *OwnedTransportHarness) SetConnected(connected bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = connected
}

func (h *OwnedTransportHarness) FailNext(failure OwnedTransportFailure) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures = append(h.failures, failure)
}

func (h *OwnedTransportHarness) Callbacks() []OwnedTransportCallback {
	h.mu.Lock()
	defer h.mu.Unlock()
	callbacks := make([]OwnedTransportCallback, len(h.callbacks))
	for index, callback := range h.callbacks {
		callbacks[index] = cloneOwnedTransportCallback(callback)
	}
	return callbacks
}

func (h *OwnedTransportHarness) AcceptedCallbacks() []OwnedTransportCallback {
	h.mu.Lock()
	defer h.mu.Unlock()
	callbacks := make([]OwnedTransportCallback, len(h.acceptedCallbacks))
	for index, callback := range h.acceptedCallbacks {
		callbacks[index] = cloneOwnedTransportCallback(callback)
	}
	return callbacks
}

func (h *OwnedTransportHarness) Deliveries() []OwnedTransportDelivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	deliveries := make([]OwnedTransportDelivery, len(h.deliveries))
	copy(deliveries, h.deliveries)
	return deliveries
}

func (h *OwnedTransportHarness) Responses() []OwnedTransportResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	responses := make([]OwnedTransportResponse, len(h.responses))
	for index, response := range h.responses {
		responses[index] = cloneOwnedTransportResponse(response)
	}
	return responses
}

func (h *OwnedTransportHarness) OperationalSignals() []OwnedTransportOperationalSignal {
	h.mu.Lock()
	defer h.mu.Unlock()
	signals := make([]OwnedTransportOperationalSignal, len(h.signals))
	copy(signals, h.signals)
	return signals
}

func (h *OwnedTransportHarness) Lifecycle() Lifecycle {
	return h.client
}

func (h *OwnedTransportHarness) Requests() []OwnedTransportRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	requests := make([]OwnedTransportRequest, len(h.requests))
	for index, request := range h.requests {
		requests[index] = cloneOwnedTransportRequest(request)
	}
	return requests
}

func (h *OwnedTransportHarness) RoundTrip(
	ctx context.Context,
	request OwnedTransportRequest,
) (OwnedTransportResponse, error) {
	h.mu.Lock()
	h.requests = append(h.requests, cloneOwnedTransportRequest(request))
	connected := h.connected
	var failure OwnedTransportFailure
	if len(h.failures) > 0 {
		failure = h.failures[0]
		h.failures = h.failures[1:]
	}
	h.mu.Unlock()
	if !connected {
		return OwnedTransportResponse{}, errors.New("owned transport unavailable")
	}

	var response OwnedTransportResponse
	switch failure {
	case OwnedTransportDuplicateDelivery:
		_ = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryOriginal, 1)
		response = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryDuplicate, 1)
	case OwnedTransportOutOfOrderDelivery:
		response = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryRedelivery, 2)
		_ = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryOriginal, 1)
	case OwnedTransportQueueClaimLoss:
		_ = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryOriginal, 1)
		response = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryRedelivery, 2)
	case OwnedTransportNonCanonicalPayload:
		tampered := request.Envelope
		tampered.Payload = append(append([]byte(nil), tampered.Payload...), ' ')
		tampered.PayloadDigest = ownedTransportDigest(tampered.Payload)
		tampered.AuthenticationTag = signOwnedTransportEnvelope(tampered, h.authenticationKey)
		response = h.dispatchDelivery(ctx, tampered, OwnedTransportDeliveryOriginal, 1)
	case OwnedTransportUnsafeFailure, OwnedTransportForgedErrorResponse,
		OwnedTransportForgedErrorCallback, OwnedTransportUnknownWireError:
		h.recordDelivery(request.Envelope, OwnedTransportDeliveryOriginal, 1)
		response = ownedTransportErrorResponse(
			request.Envelope,
			errors.New("unsafe vendor path session mount locator credential content"),
		)
		if failure == OwnedTransportUnknownWireError {
			response.Error = &OwnedTransportWireError{
				Code: "unknown_wire_error", SafeCategory: SafeErrorInvalidIntent,
			}
		}
	default:
		response = h.dispatchDelivery(ctx, request.Envelope, OwnedTransportDeliveryOriginal, 1)
	}
	response.AuthenticationTag = signOwnedTransportResponse(response, h.responseAuthenticationKey)
	if failure == OwnedTransportForgedResponse {
		response.Payload = append(append([]byte(nil), response.Payload...), 'x')
		response.PayloadDigest = ownedTransportDigest(response.Payload)
	}
	if failure == OwnedTransportForgedErrorResponse {
		response.Payload = append(response.Payload, []byte("unsigned raw error payload")...)
	}
	h.recordResponseAndCallback(response, failure)
	if failure == OwnedTransportResponseLoss || failure == OwnedTransportTimeoutAfterDelivery {
		return OwnedTransportResponse{}, OwnedTransportAcknowledgementAmbiguity{}
	}
	return response, nil
}

func (h *OwnedTransportHarness) dispatchDelivery(
	ctx context.Context,
	envelope OwnedTransportEnvelope,
	kind OwnedTransportDeliveryKind,
	claimGeneration Generation,
) OwnedTransportResponse {
	h.recordDelivery(envelope, kind, claimGeneration)
	return h.dispatch(ctx, envelope)
}

func (h *OwnedTransportHarness) recordDelivery(
	envelope OwnedTransportEnvelope,
	kind OwnedTransportDeliveryKind,
	claimGeneration Generation,
) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deliveries = append(h.deliveries, OwnedTransportDelivery{
		Sequence:        uint64(len(h.deliveries) + 1),
		Kind:            kind,
		ClaimGeneration: claimGeneration,
		Method:          envelope.Method,
		OperationID:     envelope.OperationID,
	})
}

func (h *OwnedTransportHarness) dispatch(
	ctx context.Context,
	envelope OwnedTransportEnvelope,
) OwnedTransportResponse {
	if !h.authenticated(envelope) {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorOwnershipDenied})
	}
	if h.lifecycle == nil {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorRetryableUnavailable})
	}
	if envelope.Deadline <= h.now() {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorStaleAuthority})
	}
	if envelope.PayloadDigest != ownedTransportDigest(envelope.Payload) {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}
	if !h.bindEnvelope(envelope) {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}

	switch envelope.Method {
	case ownedTransportConfirmTaskWorkspace:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ConfirmTaskWorkspace)
	case ownedTransportMaterialize:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.Materialize)
	case ownedTransportOpenRuntimeView:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.OpenRuntimeView)
	case ownedTransportCommitRuntimeView:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.CommitRuntimeView)
	case ownedTransportDiscardRuntimeView:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.DiscardRuntimeView)
	case ownedTransportFenceRuntimeView:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.FenceRuntimeView)
	case ownedTransportExpireMaterialization:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ExpireMaterialization)
	case ownedTransportExpireRuntimeView:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ExpireRuntimeView)
	case ownedTransportRestoreTaskWorkspace:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.RestoreTaskWorkspace)
	case ownedTransportReconstructTaskWorkspace:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ReconstructTaskWorkspace)
	case ownedTransportInspectRetention:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.InspectCheckpointRetention)
	case ownedTransportAttachRetention:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.AttachCheckpointRetention)
	case ownedTransportReleaseRetention:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ReleaseCheckpointRetention)
	case ownedTransportReclaimCheckpoint:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ReclaimCheckpoint)
	case ownedTransportObserveInventory:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ObserveCheckpointInventory)
	case ownedTransportCreateCleanupObligation:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.CreateCleanupObligation)
	case ownedTransportInspectCleanupDebt:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.InspectCleanupDebt)
	case ownedTransportClaimCleanupDebt:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ClaimCleanupDebt)
	case ownedTransportReconcileCleanupDebt:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ReconcileCleanupDebt)
	case ownedTransportResolveCleanupDebt:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ResolveCleanupDebt)
	case ownedTransportReopenCleanupDebt:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ReopenCleanupDebt)
	case ownedTransportRebuildAuditDelivery:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.RebuildAuditDelivery)
	case ownedTransportRebuildProjections:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.RebuildProjections)
	case ownedTransportQueryDiagnostics:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.QueryAdministratorDiagnostics)
	case ownedTransportAcceptLegacyCleanup:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.AcceptLegacyCleanupObligation)
	case ownedTransportInspectOperation:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.InspectOperation)
	case ownedTransportReconcileOperation:
		return dispatchOwnedTransport(ctx, envelope, h.lifecycle.ReconcileOperation)
	default:
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorInvalidIntent})
	}
}

func (h *OwnedTransportHarness) recordResponseAndCallback(
	response OwnedTransportResponse,
	failure OwnedTransportFailure,
) {
	callback := OwnedTransportCallback{
		Method: response.Method, OperationID: response.OperationID,
		CanonicalRequestDigest: response.CanonicalRequestDigest,
		PayloadDigest:          response.PayloadDigest,
		Payload:                append([]byte(nil), response.Payload...),
	}
	signal := OwnedTransportOperationalSignal{Method: response.Method, Result: OwnedTransportSignalSuccess}
	if response.Error != nil {
		wireError := *response.Error
		callback.Error = &wireError
		signal.Result = OwnedTransportSignalError
		signal.SafeCategory = wireError.SafeCategory
		signal.Retryable = wireError.Retryable
		signal.ReconciliationRequired = wireError.ReconciliationRequired
	}
	if failure == OwnedTransportResponseLoss || failure == OwnedTransportTimeoutAfterDelivery {
		signal.Result = OwnedTransportSignalAmbiguous
		signal.SafeCategory = SafeErrorReconciliationRequired
		signal.ReconciliationRequired = true
	}
	callback.AuthenticationTag = signOwnedTransportCallback(callback, h.responseAuthenticationKey)
	if failure == OwnedTransportForgedCallback {
		callback.Payload = append(append([]byte(nil), callback.Payload...), 'x')
		callback.PayloadDigest = ownedTransportDigest(callback.Payload)
	}
	if failure == OwnedTransportForgedErrorCallback {
		callback.Payload = append(callback.Payload, []byte("unsigned raw error payload")...)
	}
	h.mu.Lock()
	h.responses = append(h.responses, cloneOwnedTransportResponse(response))
	h.signals = append(h.signals, signal)
	h.mu.Unlock()
	h.deliverCallback(callback)
	if failure == OwnedTransportCallbackReplay {
		h.deliverCallback(callback)
	}
}

func (h *OwnedTransportHarness) deliverCallback(callback OwnedTransportCallback) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = append(h.callbacks, cloneOwnedTransportCallback(callback))
	if !hmac.Equal(
		[]byte(callback.AuthenticationTag),
		[]byte(signOwnedTransportCallback(callback, h.responseAuthenticationKey)),
	) || callback.Method == "" || callback.OperationID == "" || callback.CanonicalRequestDigest == "" {
		return
	}
	if callback.Error == nil {
		if callback.PayloadDigest != ownedTransportDigest(callback.Payload) ||
			!validOwnedTransportCallbackPayload(callback.Method, callback.Payload) {
			return
		}
	} else {
		if len(callback.Payload) != 0 || callback.PayloadDigest != "" ||
			!validOwnedTransportWireError(*callback.Error) {
			return
		}
	}
	key := ownedTransportCallbackKey{callback.Method, callback.OperationID, callback.CanonicalRequestDigest}
	if existingTag, ok := h.acceptedCallbackTags[key]; ok {
		if hmac.Equal([]byte(existingTag), []byte(callback.AuthenticationTag)) {
			return
		}
		return
	}
	h.acceptedCallbackTags[key] = callback.AuthenticationTag
	h.acceptedCallbacks = append(h.acceptedCallbacks, cloneOwnedTransportCallback(callback))
}

type OwnedTransportAcknowledgementAmbiguity struct{}

func (OwnedTransportAcknowledgementAmbiguity) Error() string {
	return "owned transport acknowledgement is ambiguous"
}

func (OwnedTransportAcknowledgementAmbiguity) ReconciliationRequired() bool {
	return true
}

func (h *OwnedTransportHarness) authenticated(envelope OwnedTransportEnvelope) bool {
	if len(h.authenticationKey) == 0 || envelope.MachineAuthorityID != h.authority.ID ||
		envelope.MachineAuthorityGeneration != h.authority.Generation ||
		h.authority.ExpiresAt <= h.now() ||
		!hmac.Equal([]byte(envelope.AuthenticationTag), []byte(signOwnedTransportEnvelope(envelope, h.authenticationKey))) {
		return false
	}
	if h.authorize == nil {
		return false
	}
	return h.authorize(OwnedTransportAuthorityScope{
		ScopeKind:                  envelope.ScopeKind,
		MachineAuthorityID:         envelope.MachineAuthorityID,
		MachineAuthorityGeneration: envelope.MachineAuthorityGeneration,
		PolicyDomainID:             envelope.PolicyDomainID,
		TaskID:                     envelope.TaskID,
		OperationID:                envelope.OperationID,
		Generation:                 envelope.Generation,
		Fence:                      envelope.Fence,
	})
}

type ownedTransportRequestMetadata struct {
	scopeKind      OwnedTransportScopeKind
	policyDomainID PolicyDomainID
	taskID         TaskID
	operationID    OperationID
	requestDigest  Digest
	generation     Generation
	fence          Fence
}

func dispatchOwnedTransport[Request, Result any](
	ctx context.Context,
	envelope OwnedTransportEnvelope,
	invoke func(context.Context, Request) (Result, error),
) OwnedTransportResponse {
	var request Request
	if err := decodeOwnedTransportPayload(envelope.Payload, &request); err != nil {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}
	metadata, err := ownedTransportMetadata(request)
	if err != nil || metadata.scopeKind != envelope.ScopeKind ||
		metadata.policyDomainID != envelope.PolicyDomainID ||
		metadata.taskID != envelope.TaskID || metadata.operationID != envelope.OperationID ||
		metadata.requestDigest != envelope.CanonicalRequestDigest ||
		metadata.generation != envelope.Generation || metadata.fence != envelope.Fence {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}
	result, invokeErr := invoke(ctx, request)
	return encodeOwnedTransportResponse(envelope, result, invokeErr)
}

func ownedTransportMetadata(request any) (ownedTransportRequestMetadata, error) {
	switch request := request.(type) {
	case ConfirmTaskWorkspaceRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), 0, 0,
		)
	case MaterializeRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case OpenRuntimeViewRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case CommitRuntimeViewRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case DiscardRuntimeViewRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case FenceRuntimeViewRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ExpireMaterializationRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ExpireRuntimeViewRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case RestoreTaskWorkspaceRequest:
		return ownedTransportOperationMetadata(
			request.Intent.PolicyDomainID, request.Intent.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Intent.Generation, request.Intent.Fence,
		)
	case ReconstructTaskWorkspaceRequest:
		return ownedTransportOperationMetadata(
			request.Intent.PolicyDomainID, request.Intent.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Intent.Generation, request.Intent.Fence,
		)
	case InspectCheckpointRetentionRequest:
		return ownedTransportQueryMetadata(
			request.PolicyDomainID, request.TaskID,
			OperationID("inspect-retention:"+string(request.CheckpointID)),
			"inspect_checkpoint_retention", request,
		), nil
	case AttachCheckpointRetentionRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ReleaseCheckpointRetentionRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ReclaimCheckpointRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ObserveCheckpointInventoryRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), 0, 0,
		)
	case CreateCleanupObligationRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case InspectCleanupDebtRequest:
		return ownedTransportQueryMetadata(
			request.PolicyDomainID, request.TaskID,
			OperationID("inspect-cleanup-debt:"+string(request.DebtID)),
			"inspect_cleanup_debt", request,
		), nil
	case ClaimCleanupDebtRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), 0, 0,
		)
	case ReconcileCleanupDebtRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ResolveCleanupDebtRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case ReopenCleanupDebtRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case AuditDeliveryRebuildRequest:
		return ownedTransportModuleQueryMetadata(
			"rebuild-audit-delivery",
			"rebuild_audit_delivery", request,
		), nil
	case ProjectionRebuildRequest:
		return ownedTransportModuleQueryMetadata(
			OperationID("rebuild-projections:"+strconv.FormatUint(uint64(request.SchemaRevision), 10)),
			"rebuild_projections", request,
		), nil
	case QueryAdministratorDiagnosticsRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), 0, 0,
		)
	case AcceptLegacyCleanupObligationRequest:
		return ownedTransportOperationMetadata(
			request.PolicyDomainID, request.TaskID, request.Operation,
			request.CanonicalRequestDigest(), request.Generation, request.Fence,
		)
	case InspectOperationRequest:
		return ownedTransportQueryMetadata(
			request.PolicyDomainID, request.TaskID, request.OperationID,
			"inspect_operation", request,
		), nil
	case ReconcileOperationRequest:
		return ownedTransportQueryMetadata(
			request.PolicyDomainID, request.TaskID, request.OperationID,
			"reconcile_operation", request,
		), nil
	default:
		return ownedTransportRequestMetadata{}, &Error{Code: ErrorInvalidIntent}
	}
}

func ownedTransportOperationMetadata(
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operation Operation,
	canonicalRequestDigest Digest,
	generation Generation,
	fence Fence,
) (ownedTransportRequestMetadata, error) {
	if operation.ID == "" || operation.RequestDigest == "" ||
		operation.RequestDigest != canonicalRequestDigest {
		return ownedTransportRequestMetadata{}, &Error{Code: ErrorIntegrityConflict}
	}
	return ownedTransportRequestMetadata{
		scopeKind:      OwnedTransportTaskScope,
		policyDomainID: policyDomainID,
		taskID:         taskID,
		operationID:    operation.ID,
		requestDigest:  canonicalRequestDigest,
		generation:     generation,
		fence:          fence,
	}, nil
}

func ownedTransportQueryMetadata(
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operationID OperationID,
	kind string,
	request any,
) ownedTransportRequestMetadata {
	return ownedTransportRequestMetadata{
		scopeKind:      OwnedTransportTaskScope,
		policyDomainID: policyDomainID,
		taskID:         taskID,
		operationID:    operationID,
		requestDigest: canonicalDigest(struct {
			Kind    string
			Request any
		}{kind, request}),
	}
}

func ownedTransportModuleQueryMetadata(
	operationID OperationID,
	kind string,
	request any,
) ownedTransportRequestMetadata {
	metadata := ownedTransportQueryMetadata("", "", operationID, kind, request)
	metadata.scopeKind = OwnedTransportModuleScope
	return metadata
}

func ownedTransportCall[Request, Result any](
	ctx context.Context,
	client *ownedTransportClient,
	method OwnedTransportMethod,
	request Request,
	metadata ownedTransportRequestMetadata,
) (Result, error) {
	var zero Result
	if client.transport == nil || client.authority.ID == "" || client.authority.Generation == 0 ||
		metadata.operationID == "" || metadata.requestDigest == "" ||
		(metadata.scopeKind == OwnedTransportTaskScope &&
			(metadata.policyDomainID == "" || metadata.taskID == "")) ||
		(metadata.scopeKind == OwnedTransportModuleScope &&
			(metadata.policyDomainID != "" || metadata.taskID != "")) ||
		(metadata.scopeKind != OwnedTransportTaskScope && metadata.scopeKind != OwnedTransportModuleScope) {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	payloadDigest := ownedTransportDigest(payload)
	binding, err := client.bindCall(method, metadata, payloadDigest)
	if err != nil {
		return zero, err
	}
	envelope := OwnedTransportEnvelope{
		ScopeKind:                  metadata.scopeKind,
		Method:                     method,
		MachineAuthorityID:         client.authority.ID,
		MachineAuthorityGeneration: client.authority.Generation,
		PolicyDomainID:             metadata.policyDomainID,
		TaskID:                     metadata.taskID,
		OperationID:                metadata.operationID,
		CanonicalRequestDigest:     metadata.requestDigest,
		PayloadDigest:              payloadDigest,
		Deadline:                   binding.deadline,
		Generation:                 metadata.generation,
		Fence:                      metadata.fence,
		Payload:                    payload,
	}
	envelope.AuthenticationTag = signOwnedTransportEnvelope(envelope, client.authenticationKey)
	response, transportErr := client.transport.RoundTrip(ctx, OwnedTransportRequest{Envelope: envelope})
	if transportErr != nil {
		var ambiguous interface{ ReconciliationRequired() bool }
		var timeout interface{ Timeout() bool }
		if (errors.As(transportErr, &ambiguous) && ambiguous.ReconciliationRequired()) ||
			errors.Is(transportErr, context.DeadlineExceeded) ||
			(errors.As(transportErr, &timeout) && timeout.Timeout()) {
			return zero, &Error{Code: ErrorReconciliationRequired}
		}
		return zero, &Error{Code: ErrorRetryableUnavailable}
	}
	if !hmac.Equal(
		[]byte(response.AuthenticationTag),
		[]byte(signOwnedTransportResponse(response, client.responseAuthenticationKey)),
	) {
		return zero, &Error{Code: ErrorIntegrityConflict}
	}
	if response.Method != method || response.OperationID != metadata.operationID ||
		response.CanonicalRequestDigest != metadata.requestDigest {
		return zero, &Error{Code: ErrorIntegrityConflict}
	}
	if response.Error != nil {
		if len(response.Payload) != 0 || response.PayloadDigest != "" {
			return zero, &Error{Code: ErrorIntegrityConflict}
		}
		return zero, decodeOwnedTransportError(*response.Error)
	}
	if response.PayloadDigest != ownedTransportDigest(response.Payload) ||
		decodeOwnedTransportPayload(response.Payload, &zero) != nil {
		return zero, &Error{Code: ErrorIntegrityConflict}
	}
	return zero, nil
}

func callOwnedTransport[Request, Result any](
	ctx context.Context,
	client *ownedTransportClient,
	method OwnedTransportMethod,
	request Request,
) (Result, error) {
	var zero Result
	metadata, err := ownedTransportMetadata(request)
	if err != nil {
		return zero, err
	}
	return ownedTransportCall[Request, Result](ctx, client, method, request, metadata)
}

func (c *ownedTransportClient) bindCall(
	method OwnedTransportMethod,
	metadata ownedTransportRequestMetadata,
	payloadDigest Digest,
) (ownedTransportCallBinding, error) {
	deadline := c.now() + Instant(c.requestLifetime)
	if c.authority.ExpiresAt < deadline {
		deadline = c.authority.ExpiresAt
	}
	candidate := OwnedTransportBinding{
		ScopeKind:                  metadata.scopeKind,
		Method:                     method,
		MachineAuthorityID:         c.authority.ID,
		MachineAuthorityGeneration: c.authority.Generation,
		PolicyDomainID:             metadata.policyDomainID,
		TaskID:                     metadata.taskID,
		OperationID:                metadata.operationID,
		CanonicalRequestDigest:     metadata.requestDigest,
		PayloadDigest:              payloadDigest,
		Deadline:                   deadline,
		Generation:                 metadata.generation,
		Fence:                      metadata.fence,
	}
	bound, _ := c.bindingStore.LoadOrStore(candidate)
	if !sameOwnedTransportBindingIdentity(bound, candidate) {
		return ownedTransportCallBinding{}, &Error{Code: ErrorIntegrityConflict}
	}
	return ownedTransportCallBinding{
		requestDigest: bound.CanonicalRequestDigest,
		deadline:      bound.Deadline,
		generation:    bound.Generation,
		fence:         bound.Fence,
	}, nil
}

func (c *ownedTransportClient) ConfirmTaskWorkspace(
	ctx context.Context,
	request ConfirmTaskWorkspaceRequest,
) (ConfirmTaskWorkspaceResult, error) {
	return callOwnedTransport[ConfirmTaskWorkspaceRequest, ConfirmTaskWorkspaceResult](
		ctx, c, ownedTransportConfirmTaskWorkspace, request,
	)
}

func ownedTransportNow(now func() Instant) func() Instant {
	if now != nil {
		return now
	}
	return func() Instant { return 1 }
}

func ownedTransportLifetime(lifetime Duration) Duration {
	if lifetime > 0 {
		return lifetime
	}
	return 60_000_000_000
}

func ownedTransportBindingStore(store OwnedTransportBindingStore) OwnedTransportBindingStore {
	if store != nil {
		return store
	}
	return NewOwnedTransportBindingStore()
}

func ownedTransportBindingKey(binding OwnedTransportBinding) ownedTransportCallKey {
	return ownedTransportCallKey{
		binding.ScopeKind,
		binding.Method,
		binding.PolicyDomainID,
		binding.TaskID,
		binding.OperationID,
	}
}

func sameOwnedTransportBindingIdentity(first, second OwnedTransportBinding) bool {
	return first.ScopeKind == second.ScopeKind && first.Method == second.Method &&
		first.MachineAuthorityID == second.MachineAuthorityID &&
		first.MachineAuthorityGeneration == second.MachineAuthorityGeneration &&
		first.PolicyDomainID == second.PolicyDomainID && first.TaskID == second.TaskID &&
		first.OperationID == second.OperationID &&
		first.CanonicalRequestDigest == second.CanonicalRequestDigest &&
		first.PayloadDigest == second.PayloadDigest && first.Generation == second.Generation &&
		first.Fence == second.Fence
}

func (h *OwnedTransportHarness) bindEnvelope(envelope OwnedTransportEnvelope) bool {
	candidate := OwnedTransportBinding{
		ScopeKind:                  envelope.ScopeKind,
		Method:                     envelope.Method,
		MachineAuthorityID:         envelope.MachineAuthorityID,
		MachineAuthorityGeneration: envelope.MachineAuthorityGeneration,
		PolicyDomainID:             envelope.PolicyDomainID,
		TaskID:                     envelope.TaskID,
		OperationID:                envelope.OperationID,
		CanonicalRequestDigest:     envelope.CanonicalRequestDigest,
		PayloadDigest:              envelope.PayloadDigest,
		Deadline:                   envelope.Deadline,
		Generation:                 envelope.Generation,
		Fence:                      envelope.Fence,
	}
	bound, _ := h.bindingStore.LoadOrStore(candidate)
	return sameOwnedTransportBindingIdentity(bound, candidate) && bound.Deadline == candidate.Deadline
}

func cloneOwnedTransportRequest(request OwnedTransportRequest) OwnedTransportRequest {
	request.Envelope.Payload = append([]byte(nil), request.Envelope.Payload...)
	return request
}

func cloneOwnedTransportResponse(response OwnedTransportResponse) OwnedTransportResponse {
	response.Payload = append([]byte(nil), response.Payload...)
	if response.Error != nil {
		wireError := *response.Error
		response.Error = &wireError
	}
	return response
}

func cloneOwnedTransportCallback(callback OwnedTransportCallback) OwnedTransportCallback {
	callback.Payload = append([]byte(nil), callback.Payload...)
	if callback.Error != nil {
		wireError := *callback.Error
		callback.Error = &wireError
	}
	return callback
}

func decodeOwnedTransportPayload(payload []byte, target any) error {
	if err := json.Unmarshal(payload, target); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, payload) {
		return &Error{Code: ErrorIntegrityConflict}
	}
	return nil
}

func validOwnedTransportCallbackPayload(method OwnedTransportMethod, payload []byte) bool {
	switch method {
	case ownedTransportConfirmTaskWorkspace:
		return canonicalOwnedTransportResult[ConfirmTaskWorkspaceResult](payload)
	case ownedTransportMaterialize:
		return canonicalOwnedTransportResult[MaterializeResult](payload)
	case ownedTransportOpenRuntimeView:
		return canonicalOwnedTransportResult[OpenRuntimeViewResult](payload)
	case ownedTransportCommitRuntimeView:
		return canonicalOwnedTransportResult[CommitRuntimeViewResult](payload)
	case ownedTransportDiscardRuntimeView:
		return canonicalOwnedTransportResult[DiscardRuntimeViewResult](payload)
	case ownedTransportFenceRuntimeView:
		return canonicalOwnedTransportResult[FenceRuntimeViewResult](payload)
	case ownedTransportExpireMaterialization:
		return canonicalOwnedTransportResult[ExpireMaterializationResult](payload)
	case ownedTransportExpireRuntimeView:
		return canonicalOwnedTransportResult[ExpireRuntimeViewResult](payload)
	case ownedTransportRestoreTaskWorkspace:
		return canonicalOwnedTransportResult[RestoreTaskWorkspaceResult](payload)
	case ownedTransportReconstructTaskWorkspace:
		return canonicalOwnedTransportResult[ReconstructTaskWorkspaceResult](payload)
	case ownedTransportInspectRetention, ownedTransportAttachRetention, ownedTransportReleaseRetention:
		return canonicalOwnedTransportResult[CheckpointRetention](payload)
	case ownedTransportReclaimCheckpoint:
		return canonicalOwnedTransportResult[CheckpointReclamation](payload)
	case ownedTransportObserveInventory:
		return canonicalOwnedTransportResult[CheckpointInventoryObservation](payload)
	case ownedTransportCreateCleanupObligation, ownedTransportInspectCleanupDebt,
		ownedTransportClaimCleanupDebt, ownedTransportReconcileCleanupDebt,
		ownedTransportResolveCleanupDebt, ownedTransportReopenCleanupDebt,
		ownedTransportAcceptLegacyCleanup:
		return canonicalOwnedTransportResult[CleanupDebt](payload)
	case ownedTransportRebuildAuditDelivery:
		return canonicalOwnedTransportResult[AuditDeliveryBacklog](payload)
	case ownedTransportRebuildProjections:
		return canonicalOwnedTransportResult[ProjectionRebuildResult](payload)
	case ownedTransportQueryDiagnostics:
		return canonicalOwnedTransportResult[AdministratorDiagnostics](payload)
	case ownedTransportInspectOperation, ownedTransportReconcileOperation:
		return canonicalOwnedTransportResult[OperationInspection](payload)
	default:
		return false
	}
}

func canonicalOwnedTransportResult[Result any](payload []byte) bool {
	var result Result
	return decodeOwnedTransportPayload(payload, &result) == nil
}

func encodeOwnedTransportResponse[Result any](
	envelope OwnedTransportEnvelope,
	result Result,
	err error,
) OwnedTransportResponse {
	if err != nil {
		return ownedTransportErrorResponse(envelope, err)
	}
	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityFailure})
	}
	return OwnedTransportResponse{
		Method:                 envelope.Method,
		OperationID:            envelope.OperationID,
		CanonicalRequestDigest: envelope.CanonicalRequestDigest,
		PayloadDigest:          ownedTransportDigest(payload),
		Payload:                payload,
	}
}

func ownedTransportErrorResponse(envelope OwnedTransportEnvelope, err error) OwnedTransportResponse {
	lifecycleError := normalizeLifecycleError(err)
	return OwnedTransportResponse{
		Method:                 envelope.Method,
		OperationID:            envelope.OperationID,
		CanonicalRequestDigest: envelope.CanonicalRequestDigest,
		Error: &OwnedTransportWireError{
			Code:                   lifecycleError.Code,
			SafeCategory:           lifecycleError.SafeCategory(),
			Retryable:              lifecycleError.Retryable(),
			ReconciliationRequired: lifecycleError.ReconciliationRequired(),
		},
	}
}

func decodeOwnedTransportError(wire OwnedTransportWireError) error {
	if !validOwnedTransportWireError(wire) {
		return &Error{Code: ErrorIntegrityConflict}
	}
	return normalizeLifecycleError(&Error{Code: wire.Code})
}

func validOwnedTransportWireError(wire OwnedTransportWireError) bool {
	if !knownLifecycleErrorCode(wire.Code) {
		return false
	}
	expected := normalizeLifecycleError(&Error{Code: wire.Code})
	return wire.SafeCategory == expected.SafeCategory() &&
		wire.Retryable == expected.Retryable() &&
		wire.ReconciliationRequired == expected.ReconciliationRequired()
}

func ownedTransportDigest(payload []byte) Digest {
	digest := sha256.Sum256(payload)
	return Digest("sha256:" + hex.EncodeToString(digest[:]))
}

func signOwnedTransportEnvelope(envelope OwnedTransportEnvelope, key []byte) Digest {
	canonical, _ := json.Marshal(struct {
		ScopeKind                  OwnedTransportScopeKind
		Method                     OwnedTransportMethod
		MachineAuthorityID         OwnedTransportMachineAuthorityID
		MachineAuthorityGeneration Generation
		PolicyDomainID             PolicyDomainID
		TaskID                     TaskID
		OperationID                OperationID
		CanonicalRequestDigest     Digest
		PayloadDigest              Digest
		Deadline                   Instant
		Generation                 Generation
		Fence                      Fence
	}{
		envelope.ScopeKind,
		envelope.Method,
		envelope.MachineAuthorityID,
		envelope.MachineAuthorityGeneration,
		envelope.PolicyDomainID,
		envelope.TaskID,
		envelope.OperationID,
		envelope.CanonicalRequestDigest,
		envelope.PayloadDigest,
		envelope.Deadline,
		envelope.Generation,
		envelope.Fence,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return Digest("hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)))
}

func signOwnedTransportResponse(response OwnedTransportResponse, key []byte) Digest {
	canonical, _ := json.Marshal(struct {
		Kind                   string
		Method                 OwnedTransportMethod
		OperationID            OperationID
		CanonicalRequestDigest Digest
		PayloadDigest          Digest
		Error                  *OwnedTransportWireError
	}{
		"response",
		response.Method,
		response.OperationID,
		response.CanonicalRequestDigest,
		response.PayloadDigest,
		response.Error,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return Digest("hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)))
}

func signOwnedTransportCallback(callback OwnedTransportCallback, key []byte) Digest {
	canonical, _ := json.Marshal(struct {
		Kind                   string
		Method                 OwnedTransportMethod
		OperationID            OperationID
		CanonicalRequestDigest Digest
		PayloadDigest          Digest
		Error                  *OwnedTransportWireError
	}{
		"callback",
		callback.Method,
		callback.OperationID,
		callback.CanonicalRequestDigest,
		callback.PayloadDigest,
		callback.Error,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return Digest("hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)))
}

func (c *ownedTransportClient) Materialize(ctx context.Context, request MaterializeRequest) (MaterializeResult, error) {
	return callOwnedTransport[MaterializeRequest, MaterializeResult](ctx, c, ownedTransportMaterialize, request)
}
func (c *ownedTransportClient) OpenRuntimeView(ctx context.Context, request OpenRuntimeViewRequest) (OpenRuntimeViewResult, error) {
	return callOwnedTransport[OpenRuntimeViewRequest, OpenRuntimeViewResult](ctx, c, ownedTransportOpenRuntimeView, request)
}
func (c *ownedTransportClient) CommitRuntimeView(ctx context.Context, request CommitRuntimeViewRequest) (CommitRuntimeViewResult, error) {
	return callOwnedTransport[CommitRuntimeViewRequest, CommitRuntimeViewResult](ctx, c, ownedTransportCommitRuntimeView, request)
}
func (c *ownedTransportClient) DiscardRuntimeView(ctx context.Context, request DiscardRuntimeViewRequest) (DiscardRuntimeViewResult, error) {
	return callOwnedTransport[DiscardRuntimeViewRequest, DiscardRuntimeViewResult](ctx, c, ownedTransportDiscardRuntimeView, request)
}
func (c *ownedTransportClient) FenceRuntimeView(ctx context.Context, request FenceRuntimeViewRequest) (FenceRuntimeViewResult, error) {
	return callOwnedTransport[FenceRuntimeViewRequest, FenceRuntimeViewResult](ctx, c, ownedTransportFenceRuntimeView, request)
}
func (c *ownedTransportClient) ExpireMaterialization(ctx context.Context, request ExpireMaterializationRequest) (ExpireMaterializationResult, error) {
	return callOwnedTransport[ExpireMaterializationRequest, ExpireMaterializationResult](ctx, c, ownedTransportExpireMaterialization, request)
}
func (c *ownedTransportClient) ExpireRuntimeView(ctx context.Context, request ExpireRuntimeViewRequest) (ExpireRuntimeViewResult, error) {
	return callOwnedTransport[ExpireRuntimeViewRequest, ExpireRuntimeViewResult](ctx, c, ownedTransportExpireRuntimeView, request)
}
func (c *ownedTransportClient) RestoreTaskWorkspace(ctx context.Context, request RestoreTaskWorkspaceRequest) (RestoreTaskWorkspaceResult, error) {
	return callOwnedTransport[RestoreTaskWorkspaceRequest, RestoreTaskWorkspaceResult](ctx, c, ownedTransportRestoreTaskWorkspace, request)
}
func (c *ownedTransportClient) ReconstructTaskWorkspace(ctx context.Context, request ReconstructTaskWorkspaceRequest) (ReconstructTaskWorkspaceResult, error) {
	return callOwnedTransport[ReconstructTaskWorkspaceRequest, ReconstructTaskWorkspaceResult](ctx, c, ownedTransportReconstructTaskWorkspace, request)
}
func (c *ownedTransportClient) InspectCheckpointRetention(ctx context.Context, request InspectCheckpointRetentionRequest) (CheckpointRetention, error) {
	return callOwnedTransport[InspectCheckpointRetentionRequest, CheckpointRetention](ctx, c, ownedTransportInspectRetention, request)
}
func (c *ownedTransportClient) AttachCheckpointRetention(ctx context.Context, request AttachCheckpointRetentionRequest) (CheckpointRetention, error) {
	return callOwnedTransport[AttachCheckpointRetentionRequest, CheckpointRetention](ctx, c, ownedTransportAttachRetention, request)
}
func (c *ownedTransportClient) ReleaseCheckpointRetention(ctx context.Context, request ReleaseCheckpointRetentionRequest) (CheckpointRetention, error) {
	return callOwnedTransport[ReleaseCheckpointRetentionRequest, CheckpointRetention](ctx, c, ownedTransportReleaseRetention, request)
}
func (c *ownedTransportClient) ReclaimCheckpoint(ctx context.Context, request ReclaimCheckpointRequest) (CheckpointReclamation, error) {
	return callOwnedTransport[ReclaimCheckpointRequest, CheckpointReclamation](ctx, c, ownedTransportReclaimCheckpoint, request)
}
func (c *ownedTransportClient) ObserveCheckpointInventory(ctx context.Context, request ObserveCheckpointInventoryRequest) (CheckpointInventoryObservation, error) {
	return callOwnedTransport[ObserveCheckpointInventoryRequest, CheckpointInventoryObservation](ctx, c, ownedTransportObserveInventory, request)
}
func (c *ownedTransportClient) CreateCleanupObligation(ctx context.Context, request CreateCleanupObligationRequest) (CleanupDebt, error) {
	return callOwnedTransport[CreateCleanupObligationRequest, CleanupDebt](ctx, c, ownedTransportCreateCleanupObligation, request)
}
func (c *ownedTransportClient) InspectCleanupDebt(ctx context.Context, request InspectCleanupDebtRequest) (CleanupDebt, error) {
	return callOwnedTransport[InspectCleanupDebtRequest, CleanupDebt](ctx, c, ownedTransportInspectCleanupDebt, request)
}
func (c *ownedTransportClient) ClaimCleanupDebt(ctx context.Context, request ClaimCleanupDebtRequest) (CleanupDebt, error) {
	return callOwnedTransport[ClaimCleanupDebtRequest, CleanupDebt](ctx, c, ownedTransportClaimCleanupDebt, request)
}
func (c *ownedTransportClient) ReconcileCleanupDebt(ctx context.Context, request ReconcileCleanupDebtRequest) (CleanupDebt, error) {
	return callOwnedTransport[ReconcileCleanupDebtRequest, CleanupDebt](ctx, c, ownedTransportReconcileCleanupDebt, request)
}
func (c *ownedTransportClient) ResolveCleanupDebt(ctx context.Context, request ResolveCleanupDebtRequest) (CleanupDebt, error) {
	return callOwnedTransport[ResolveCleanupDebtRequest, CleanupDebt](ctx, c, ownedTransportResolveCleanupDebt, request)
}
func (c *ownedTransportClient) ReopenCleanupDebt(ctx context.Context, request ReopenCleanupDebtRequest) (CleanupDebt, error) {
	return callOwnedTransport[ReopenCleanupDebtRequest, CleanupDebt](ctx, c, ownedTransportReopenCleanupDebt, request)
}
func (c *ownedTransportClient) RebuildAuditDelivery(ctx context.Context, request AuditDeliveryRebuildRequest) (AuditDeliveryBacklog, error) {
	return callOwnedTransport[AuditDeliveryRebuildRequest, AuditDeliveryBacklog](ctx, c, ownedTransportRebuildAuditDelivery, request)
}
func (c *ownedTransportClient) RebuildProjections(ctx context.Context, request ProjectionRebuildRequest) (ProjectionRebuildResult, error) {
	return callOwnedTransport[ProjectionRebuildRequest, ProjectionRebuildResult](ctx, c, ownedTransportRebuildProjections, request)
}
func (c *ownedTransportClient) QueryAdministratorDiagnostics(ctx context.Context, request QueryAdministratorDiagnosticsRequest) (AdministratorDiagnostics, error) {
	return callOwnedTransport[QueryAdministratorDiagnosticsRequest, AdministratorDiagnostics](ctx, c, ownedTransportQueryDiagnostics, request)
}
func (c *ownedTransportClient) AcceptLegacyCleanupObligation(ctx context.Context, request AcceptLegacyCleanupObligationRequest) (CleanupDebt, error) {
	return callOwnedTransport[AcceptLegacyCleanupObligationRequest, CleanupDebt](ctx, c, ownedTransportAcceptLegacyCleanup, request)
}
func (c *ownedTransportClient) InspectOperation(ctx context.Context, request InspectOperationRequest) (OperationInspection, error) {
	return callOwnedTransport[InspectOperationRequest, OperationInspection](ctx, c, ownedTransportInspectOperation, request)
}
func (c *ownedTransportClient) ReconcileOperation(ctx context.Context, request ReconcileOperationRequest) (OperationInspection, error) {
	return callOwnedTransport[ReconcileOperationRequest, OperationInspection](ctx, c, ownedTransportReconcileOperation, request)
}
