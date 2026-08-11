package artifactpublication

// Owned publication transport (child SPEC #109 / C05-06). This file
// delivers the owned transport envelope and client/harness pair for the
// Artifact Publication deep module. The transport carries the COMPLETE
// canonical C05 request (the typed PublicationIntent or PublicationQuery)
// and the original machine authority; it never re-assembles a request and
// it is never a second business authority — the receiving harness
// authenticates the machine, binds the at-least-once call, verifies the
// exact canonical digest, and dispatches through the same closed
// Mutate/Query seam as any other caller.
//
// Delivery semantics:
//   - At-least-once: a binding store retains the first envelope for its
//     scope/method/OperationID key; exact duplicate delivery replays the
//     same canonical request and the underlying operation journal returns
//     the same decision. Same OperationID/different payload is a durable
//     integrity conflict.
//   - Timeout, disconnect, claim loss, response/acknowledgement loss and
//     duplicate/out-of-order callbacks all return a typed pending /
//     reconciliation-required result and never create a second publication
//     operation or Artifact Version; the caller inspects/reconciles the
//     ORIGINAL OperationID through the same transport.
//   - Wire errors are normalized to the closed C05 safe-error surface and
//     never leak content, paths, sessions, object locators, vendors,
//     credentials, or the existence of another Personal Workspace.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
)

// OwnedTransportWireSchemaV1 is the strict versioned wire schema of the
// owned publication transport envelope.
const OwnedTransportWireSchemaV1 = "slidesmith.artifact-publication.owned-transport/v1"

type (
	// OwnedTransportMachineAuthorityID is the opaque identity of the
	// machine authority permitted to deliver C05 requests.
	OwnedTransportMachineAuthorityID string
	// OwnedTransportMethod is the closed transport method family.
	OwnedTransportMethod string
	// OwnedTransportFailure is the closed fault-injection set used by the
	// deterministic harness to prove transport edge semantics.
	OwnedTransportFailure string
	// OwnedTransportSignalResult is the closed operational signal result.
	OwnedTransportSignalResult string
	// OwnedTransportDeliveryKind reports how one delivery reached the
	// receiver (original, duplicate, or claim-loss redelivery).
	OwnedTransportDeliveryKind string
)

const (
	ownedTransportMutate OwnedTransportMethod = "publication_mutate"
	ownedTransportQuery  OwnedTransportMethod = "publication_query"

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

	OwnedTransportSignalSuccess   OwnedTransportSignalResult = "success"
	OwnedTransportSignalError     OwnedTransportSignalResult = "error"
	OwnedTransportSignalAmbiguous OwnedTransportSignalResult = "ambiguous"

	OwnedTransportDeliveryOriginal   OwnedTransportDeliveryKind = "original"
	OwnedTransportDeliveryDuplicate  OwnedTransportDeliveryKind = "duplicate"
	OwnedTransportDeliveryRedelivery OwnedTransportDeliveryKind = "redelivery"
)

// OwnedTransportMachineAuthority is the machine authorization that must be
// presented on every transport message. It is never chosen by the adapter:
// the Platform binds its identity, generation and expiry.
type OwnedTransportMachineAuthority struct {
	ID         OwnedTransportMachineAuthorityID
	Generation Generation
	ExpiresAt  Instant
}

// OwnedTransportBinding is the transport-owned retry identity. A binding
// store must atomically retain the first value for its
// scope/method/OperationID key so adapter and client restarts cannot extend
// its deadline or change the canonical request digest.
type OwnedTransportBinding struct {
	Method                     OwnedTransportMethod
	MachineAuthorityID         OwnedTransportMachineAuthorityID
	MachineAuthorityGeneration Generation
	PolicyDomainID             PolicyDomainID
	TaskID                     TaskID
	OperationID                PublicationOperationID
	CanonicalRequestDigest     Digest
	PayloadDigest              Digest
	Deadline                   Instant
	Generation                 Generation
	Fence                      Fence
	SafetyEpoch                SafetyEpoch
}

// OwnedTransportBindingStore retains the first binding for each call key.
type OwnedTransportBindingStore interface {
	LoadOrStore(OwnedTransportBinding) (binding OwnedTransportBinding, loaded bool)
}

type ownedTransportCallKey struct {
	method                 OwnedTransportMethod
	policyDomainID         PolicyDomainID
	taskID                 TaskID
	operationID            PublicationOperationID
	canonicalRequestDigest Digest
}

type inMemoryOwnedTransportBindingStore struct {
	mu       sync.Mutex
	bindings map[ownedTransportCallKey]OwnedTransportBinding
}

// NewOwnedTransportBindingStore returns a thread-safe in-memory binding
// store with first-wins semantics.
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

// OwnedTransportAuthorityScope is the exact scope the harness authorizes
// before any dispatch. It mirrors the envelope's authoritative facts.
type OwnedTransportAuthorityScope struct {
	MachineAuthorityID         OwnedTransportMachineAuthorityID
	MachineAuthorityGeneration Generation
	PolicyDomainID             PolicyDomainID
	TaskID                     TaskID
	OperationID                PublicationOperationID
	Generation                 Generation
	Fence                      Fence
	SafetyEpoch                SafetyEpoch
}

// OwnedTransportEnvelope is the strict, versioned wire envelope. It carries
// the machine authorization, the exact OperationID, the canonical request
// digest (the business digest — delivery attempts never change it), the
// payload digest, the deadline, and the applicable generation/fence/safety
// epoch, plus the canonical serialized request payload and an HMAC
// authentication tag. It never carries a path, object key, prefix, bucket,
// mount, vendor, credential, session, signed URL, or locator.
type OwnedTransportEnvelope struct {
	SchemaVersion              string
	Method                     OwnedTransportMethod
	MachineAuthorityID         OwnedTransportMachineAuthorityID
	MachineAuthorityGeneration Generation
	PolicyDomainID             PolicyDomainID
	TaskID                     TaskID
	OperationID                PublicationOperationID
	CanonicalRequestDigest     Digest
	PayloadDigest              Digest
	Deadline                   Instant
	Generation                 Generation
	Fence                      Fence
	SafetyEpoch                SafetyEpoch
	Payload                    []byte
	AuthenticationTag          Digest
}

// OwnedTransportRequest is one round trip with its envelope.
type OwnedTransportRequest struct {
	Envelope OwnedTransportEnvelope
}

// OwnedTransportWireError is the closed, content-free safe error carried on
// the wire. It is derived from the C05 Error codes only and never carries a
// raw downstream error chain.
type OwnedTransportWireError struct {
	Code                   ErrorCode
	SafeCategory           SafeErrorCategory
	Retryable              bool
	ReconciliationRequired bool
}

// OwnedTransportResponse is the signed response of one round trip.
type OwnedTransportResponse struct {
	Method                 OwnedTransportMethod
	OperationID            PublicationOperationID
	CanonicalRequestDigest Digest
	PayloadDigest          Digest
	Payload                []byte
	Error                  *OwnedTransportWireError
	AuthenticationTag      Digest
}

// OwnedTransportCallback is the signed asynchronous delivery of the same
// result; duplicate/out-of-order callbacks are deduplicated by the
// harness's accepted-callback registry.
type OwnedTransportCallback struct {
	Method                 OwnedTransportMethod
	OperationID            PublicationOperationID
	CanonicalRequestDigest Digest
	PayloadDigest          Digest
	Payload                []byte
	Error                  *OwnedTransportWireError
	AuthenticationTag      Digest
}

// OwnedTransportOperationalSignal is the content-free operational signal
// recorded for observability. It never carries identities beyond the method
// family and the safe category.
type OwnedTransportOperationalSignal struct {
	Method                 OwnedTransportMethod
	Result                 OwnedTransportSignalResult
	SafeCategory           SafeErrorCategory
	Retryable              bool
	ReconciliationRequired bool
}

// OwnedTransportDelivery records one delivery attempt for contract
// assertions.
type OwnedTransportDelivery struct {
	Sequence        uint64
	Kind            OwnedTransportDeliveryKind
	ClaimGeneration Generation
	Method          OwnedTransportMethod
	OperationID     PublicationOperationID
}

// OwnedTransportRoundTripper is the transport boundary implemented by the
// harness (and by production-shaped transports).
type OwnedTransportRoundTripper interface {
	RoundTrip(context.Context, OwnedTransportRequest) (OwnedTransportResponse, error)
}

// OwnedTransportClientConfig configures the owned transport client.
type OwnedTransportClientConfig struct {
	Transport                 OwnedTransportRoundTripper
	MachineAuthority          OwnedTransportMachineAuthority
	AuthenticationKey         []byte
	ResponseAuthenticationKey []byte
	Now                       func() Instant
	RequestLifetime           Duration
	BindingStore              OwnedTransportBindingStore
}

// OwnedTransportHarnessConfig configures the deterministic server-side
// harness over the C05 closed seam.
type OwnedTransportHarnessConfig struct {
	Core                      PublicationCore
	MachineAuthority          OwnedTransportMachineAuthority
	AuthenticationKey         []byte
	ResponseAuthenticationKey []byte
	Authorize                 func(OwnedTransportAuthorityScope) bool
	Now                       func() Instant
	RequestLifetime           Duration
	BindingStore              OwnedTransportBindingStore
}

// ownedTransportWireMutation is the canonical wire representation of one
// typed PublicationIntent. The typed payload is carried verbatim; the
// harness reconstructs the intent through the constructors and verifies
// the canonical digest and every authoritative binding, so the wire is
// never a second authority.
type ownedTransportWireMutation struct {
	Kind    PublicationIntentKind
	Header  PublicationIntentHeader
	Payload json.RawMessage
}

// ownedTransportWireRequest is the closed union of one mutation or one
// query carried by an envelope payload. Exactly one of Mutation/Query is
// set by construction.
type ownedTransportWireRequest struct {
	SchemaVersion SchemaVersion
	Mutation      *ownedTransportWireMutation
	Query         *PublicationQuery
}

type rejectWirePayload struct {
	Reason          RejectReason
	EvidenceFailure *EvidenceFailure
}
type cancelWirePayload struct{ Reason CancelReason }
type reconcileWirePayload struct{ Mode ReconcileMode }
type recordAssemblyWirePayload struct{ Assembly AssemblyReference }
type resolveDebtWirePayload struct {
	ResolutionClass   CleanupDebtResolutionClass
	Evidence          *CleanupResolutionEvidence
	ApprovalReference string
	ExpiresAt         Instant
}

type ownedTransportRequestMetadata struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
	operationID    PublicationOperationID
	requestDigest  Digest
	generation     Generation
	fence          Fence
	safetyEpoch    SafetyEpoch
}

type ownedTransportCallBinding struct {
	requestDigest Digest
	deadline      Instant
	generation    Generation
	fence         Fence
}

// Duration is a transport-relative time span in the same unit as Instant
// (diagnostic wall-clock seconds). It is a display/lease fact, never an
// authority fact.
type Duration int64

// NewOwnedTransportClient builds the owned transport client over any
// round-tripper.
func NewOwnedTransportClient(config OwnedTransportClientConfig) *OwnedTransportClient {
	responseAuthenticationKey := config.ResponseAuthenticationKey
	if len(responseAuthenticationKey) == 0 {
		responseAuthenticationKey = config.AuthenticationKey
	}
	return &OwnedTransportClient{
		transport:                 config.Transport,
		authority:                 config.MachineAuthority,
		authenticationKey:         append([]byte(nil), config.AuthenticationKey...),
		responseAuthenticationKey: append([]byte(nil), responseAuthenticationKey...),
		now:                       ownedTransportNow(config.Now),
		requestLifetime:           ownedTransportLifetime(config.RequestLifetime),
		bindingStore:              ownedTransportBindingStore(config.BindingStore),
	}
}

// OwnedTransportClient is the owned transport client over the C05 seam.
// It is the only way an owned adapter delivers a canonical C05 request
// (the typed PublicationIntent or PublicationQuery).
type OwnedTransportClient struct {
	transport                 OwnedTransportRoundTripper
	authority                 OwnedTransportMachineAuthority
	authenticationKey         []byte
	responseAuthenticationKey []byte
	now                       func() Instant
	requestLifetime           Duration
	bindingStore              OwnedTransportBindingStore
}

// OwnedTransportHarness is the deterministic server-side transport: it
// authenticates the machine, validates the canonical envelope, binds the
// at-least-once call, dispatches through the closed Mutate/Query seam, and
// records deliveries/responses/callbacks/signals for contract assertions.
// It also exposes fault injection for the transport edge matrix.
type OwnedTransportHarness struct {
	core                      PublicationCore
	authority                 OwnedTransportMachineAuthority
	authenticationKey         []byte
	responseAuthenticationKey []byte
	authorize                 func(OwnedTransportAuthorityScope) bool
	now                       func() Instant
	bindingStore              OwnedTransportBindingStore
	client                    *OwnedTransportClient

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

type ownedTransportCallbackKey struct {
	method                 OwnedTransportMethod
	operationID            PublicationOperationID
	canonicalRequestDigest Digest
}

// NewOwnedTransportHarness builds a harness and its paired client over the
// C05 closed seam. The harness is also a RoundTripper for the client.
func NewOwnedTransportHarness(config OwnedTransportHarnessConfig) *OwnedTransportHarness {
	now := ownedTransportNow(config.Now)
	bindingStore := ownedTransportBindingStore(config.BindingStore)
	responseAuthenticationKey := config.ResponseAuthenticationKey
	if len(responseAuthenticationKey) == 0 {
		responseAuthenticationKey = config.AuthenticationKey
	}
	harness := &OwnedTransportHarness{
		core:                      config.Core,
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

// Client returns the paired owned transport client.
func (h *OwnedTransportHarness) Client() *OwnedTransportClient { return h.client }

// Core returns the C05 seam the harness dispatches into.
func (h *OwnedTransportHarness) Core() PublicationCore { return h.core }

// SetConnected simulates a transport disconnect.
func (h *OwnedTransportHarness) SetConnected(connected bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = connected
}

// FailNext queues one transport failure for the next round trip.
func (h *OwnedTransportHarness) FailNext(failure OwnedTransportFailure) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures = append(h.failures, failure)
}

// Requests returns a copy of every delivered request.
func (h *OwnedTransportHarness) Requests() []OwnedTransportRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	requests := make([]OwnedTransportRequest, len(h.requests))
	for index, request := range h.requests {
		requests[index] = cloneOwnedTransportRequest(request)
	}
	return requests
}

// Responses returns a copy of every produced response.
func (h *OwnedTransportHarness) Responses() []OwnedTransportResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	responses := make([]OwnedTransportResponse, len(h.responses))
	for index, response := range h.responses {
		responses[index] = cloneOwnedTransportResponse(response)
	}
	return responses
}

// Callbacks returns a copy of every delivered callback.
func (h *OwnedTransportHarness) Callbacks() []OwnedTransportCallback {
	h.mu.Lock()
	defer h.mu.Unlock()
	callbacks := make([]OwnedTransportCallback, len(h.callbacks))
	for index, callback := range h.callbacks {
		callbacks[index] = cloneOwnedTransportCallback(callback)
	}
	return callbacks
}

// AcceptedCallbacks returns a copy of every accepted (authenticated,
// canonical, non-duplicate) callback.
func (h *OwnedTransportHarness) AcceptedCallbacks() []OwnedTransportCallback {
	h.mu.Lock()
	defer h.mu.Unlock()
	callbacks := make([]OwnedTransportCallback, len(h.acceptedCallbacks))
	for index, callback := range h.acceptedCallbacks {
		callbacks[index] = cloneOwnedTransportCallback(callback)
	}
	return callbacks
}

// OperationalSignals returns a copy of every recorded signal.
func (h *OwnedTransportHarness) OperationalSignals() []OwnedTransportOperationalSignal {
	h.mu.Lock()
	defer h.mu.Unlock()
	signals := make([]OwnedTransportOperationalSignal, len(h.signals))
	copy(signals, h.signals)
	return signals
}

// Deliveries returns a copy of every delivery record.
func (h *OwnedTransportHarness) Deliveries() []OwnedTransportDelivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	deliveries := make([]OwnedTransportDelivery, len(h.deliveries))
	copy(deliveries, h.deliveries)
	return deliveries
}

// RoundTrip implements the transport boundary for the paired client.
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
		return OwnedTransportResponse{}, errors.New("owned publication transport unavailable")
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

// dispatch authenticates, validates and binds one envelope, then invokes
// the closed Mutate/Query seam. It is the ONLY way the transport reaches
// the C05 authority; there is no second mutation surface.
func (h *OwnedTransportHarness) dispatch(
	ctx context.Context,
	envelope OwnedTransportEnvelope,
) OwnedTransportResponse {
	if !h.authenticated(envelope) {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorOwnershipDenied})
	}
	if h.core == nil {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorRetryableUnavailable})
	}
	if envelope.Deadline <= h.now() {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorStaleAuthority})
	}
	if envelope.PayloadDigest != ownedTransportDigest(envelope.Payload) {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}
	var wire ownedTransportWireRequest
	if err := decodeOwnedTransportPayload(envelope.Payload, &wire); err != nil {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}
	if !h.bindEnvelope(envelope) {
		return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
	}
	if wire.Mutation != nil {
		if envelope.Method != ownedTransportMutate {
			return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
		}
		intent, err := ownedTransportReconstructIntent(*wire.Mutation)
		if err != nil {
			return ownedTransportErrorResponse(envelope, &Error{Code: ErrorInvalidIntent})
		}
		if !verifyMutationEnvelope(*wire.Mutation, intent, envelope) {
			return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
		}
		result, mutateErr := h.core.Mutate(ctx, intent)
		return encodeOwnedTransportResponse(envelope, result, mutateErr)
	}
	if wire.Query != nil {
		if envelope.Method != ownedTransportQuery {
			return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
		}
		if !verifyQueryEnvelope(wire.Query, envelope) {
			return ownedTransportErrorResponse(envelope, &Error{Code: ErrorIntegrityConflict})
		}
		result, queryErr := h.core.Query(ctx, *wire.Query)
		return encodeOwnedTransportResponse(envelope, result, queryErr)
	}
	return ownedTransportErrorResponse(envelope, &Error{Code: ErrorInvalidIntent})
}

// Mutate delivers one complete canonical C05 mutation intent through the
// owned transport.
func (c *OwnedTransportClient) Mutate(
	ctx context.Context,
	intent PublicationIntent,
) (PublicationDecision, error) {
	var zero PublicationDecision
	if intent == nil || !intent.valid() {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	header := intent.header()
	wire, err := ownedTransportMutationWire(intent)
	if err != nil {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	return ownedTransportCall[PublicationDecision](
		ctx, c, ownedTransportMutate, wire, ownedTransportRequestMetadata{
			policyDomainID: header.PolicyDomainID,
			taskID:         header.TaskID,
			operationID:    header.Operation.ID,
			requestDigest:  header.Operation.RequestDigest,
			generation:     header.Generation,
			fence:          header.Fence,
			safetyEpoch:    header.SafetyEpoch,
		},
	)
}

// Query delivers one pure read-only PublicationQuery through the owned
// transport.
func (c *OwnedTransportClient) Query(
	ctx context.Context,
	query PublicationQuery,
) (PublicationView, error) {
	var zero PublicationView
	if !validQueryKind(query.Kind) || query.PolicyDomainID == "" || query.TaskID == "" {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	wire, err := ownedTransportQueryWire(query)
	if err != nil {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	return ownedTransportCall[PublicationView](
		ctx, c, ownedTransportQuery, wire, ownedTransportRequestMetadata{
			policyDomainID: query.PolicyDomainID,
			taskID:         query.TaskID,
			operationID:    ownedTransportQueryOperationID(query),
			requestDigest:  ownedTransportQueryDigest(query),
		},
	)
}

func ownedTransportCall[Result any](
	ctx context.Context,
	client *OwnedTransportClient,
	method OwnedTransportMethod,
	wire ownedTransportWireRequest,
	metadata ownedTransportRequestMetadata,
) (Result, error) {
	var zero Result
	if client.transport == nil || client.authority.ID == "" || client.authority.Generation == 0 ||
		metadata.operationID == "" || metadata.requestDigest == "" ||
		metadata.policyDomainID == "" || metadata.taskID == "" {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	payloadDigest := ownedTransportDigest(payload)
	binding, err := client.bindCall(method, metadata, payloadDigest)
	if err != nil {
		return zero, err
	}
	envelope := OwnedTransportEnvelope{
		SchemaVersion:              OwnedTransportWireSchemaV1,
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
		SafetyEpoch:                metadata.safetyEpoch,
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

func (c *OwnedTransportClient) bindCall(
	method OwnedTransportMethod,
	metadata ownedTransportRequestMetadata,
	payloadDigest Digest,
) (ownedTransportCallBinding, error) {
	deadline := c.now() + Instant(c.requestLifetime)
	if c.authority.ExpiresAt < deadline {
		deadline = c.authority.ExpiresAt
	}
	candidate := OwnedTransportBinding{
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
		SafetyEpoch:                metadata.safetyEpoch,
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

func (h *OwnedTransportHarness) bindEnvelope(envelope OwnedTransportEnvelope) bool {
	candidate := OwnedTransportBinding{
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
		SafetyEpoch:                envelope.SafetyEpoch,
	}
	bound, _ := h.bindingStore.LoadOrStore(candidate)
	return sameOwnedTransportBindingIdentity(bound, candidate) && bound.Deadline == candidate.Deadline
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
		MachineAuthorityID:         envelope.MachineAuthorityID,
		MachineAuthorityGeneration: envelope.MachineAuthorityGeneration,
		PolicyDomainID:             envelope.PolicyDomainID,
		TaskID:                     envelope.TaskID,
		OperationID:                envelope.OperationID,
		Generation:                 envelope.Generation,
		Fence:                      envelope.Fence,
		SafetyEpoch:                envelope.SafetyEpoch,
	})
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

// OwnedTransportAcknowledgementAmbiguity reports that the delivery may have
// committed but the acknowledgement was lost; the caller must inspect or
// reconcile the ORIGINAL OperationID.
type OwnedTransportAcknowledgementAmbiguity struct{}

func (OwnedTransportAcknowledgementAmbiguity) Error() string {
	return "owned publication transport acknowledgement is ambiguous"
}

func (OwnedTransportAcknowledgementAmbiguity) ReconciliationRequired() bool { return true }

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
		binding.Method,
		binding.PolicyDomainID,
		binding.TaskID,
		binding.OperationID,
		binding.CanonicalRequestDigest,
	}
}

// sameOwnedTransportBindingIdentity reports whether two bindings protect
// the same exact canonical request. Two different canonical requests under
// one OperationID (prepare, verify, activate) are distinct bindings; a
// redelivery that keeps the same OperationID and canonical digest but
// changes the payload bytes fails closed.
func sameOwnedTransportBindingIdentity(first, second OwnedTransportBinding) bool {
	return first.Method == second.Method &&
		first.MachineAuthorityID == second.MachineAuthorityID &&
		first.MachineAuthorityGeneration == second.MachineAuthorityGeneration &&
		first.PolicyDomainID == second.PolicyDomainID && first.TaskID == second.TaskID &&
		first.OperationID == second.OperationID &&
		first.CanonicalRequestDigest == second.CanonicalRequestDigest &&
		first.PayloadDigest == second.PayloadDigest &&
		first.Generation == second.Generation && first.Fence == second.Fence &&
		first.SafetyEpoch == second.SafetyEpoch
}

// ownedTransportMutationWire builds the canonical wire payload of one typed
// intent. The canonical business digest of the reconstructed intent must
// equal the envelope digest; delivery attempts never change it.
func ownedTransportMutationWire(intent PublicationIntent) (ownedTransportWireRequest, error) {
	header := intent.header()
	var payload any
	switch typed := intent.(type) {
	case PreparePublication:
		payload = PreparePublicationPayload{
			ContractID: typed.ContractID, Kind: typed.Kind, Parent: typed.Parent,
			PhaseRunID: typed.PhaseRunID, Members: typed.Members, Staging: typed.Staging,
			RequiredChannels: typed.RequiredChannels, RuntimeRefs: typed.RuntimeRefs,
			ValidationRef: typed.ValidationRef, C04CommitRef: typed.C04CommitRef,
			ContentCapabilityRefs: typed.ContentCapabilityRefs,
		}
	case VerifyPublication:
		payload = VerifyPublicationPayload{
			RuntimeEvidence: typed.RuntimeEvidence, ValidationEvidence: typed.ValidationEvidence,
			C04CommitEvidence: typed.C04CommitEvidence, ContentCapabilities: typed.ContentCapabilities,
		}
	case ActivatePublication:
		payload = nil
	case RejectPublication:
		payload = rejectWirePayload{Reason: typed.Reason, EvidenceFailure: typed.EvidenceFailure}
	case CancelPublication:
		payload = cancelWirePayload{Reason: typed.Reason}
	case ReconcilePublication:
		payload = reconcileWirePayload{Mode: typed.Mode}
	case RecordResidueAssembly:
		payload = recordAssemblyWirePayload{Assembly: typed.Assembly}
	case ReleaseResidue:
		payload = nil
	case ResolveCleanupDebt:
		payload = resolveDebtWirePayload{
			ResolutionClass: typed.ResolutionClass, Evidence: typed.Evidence,
			ApprovalReference: typed.ApprovalReference, ExpiresAt: typed.ExpiresAt,
		}
	default:
		return ownedTransportWireRequest{}, &Error{Code: ErrorInvalidIntent}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ownedTransportWireRequest{}, &Error{Code: ErrorInvalidIntent}
	}
	return ownedTransportWireRequest{
		SchemaVersion: header.SchemaVersion,
		Mutation: &ownedTransportWireMutation{
			Kind: intent.kind(), Header: header, Payload: json.RawMessage(encoded),
		},
	}, nil
}

// ownedTransportQueryWire builds the canonical wire payload of one query.
func ownedTransportQueryWire(query PublicationQuery) (ownedTransportWireRequest, error) {
	return ownedTransportWireRequest{
		SchemaVersion: SchemaV1,
		Query:         &query,
	}, nil
}

// ownedTransportReconstructIntent rebuilds the typed intent from the wire
// through the public constructors. The wire can never assemble a request:
// it only carries the exact payload and header the caller already fixed.
func ownedTransportReconstructIntent(wire ownedTransportWireMutation) (PublicationIntent, error) {
	switch wire.Kind {
	case IntentPreparePublication:
		var payload PreparePublicationPayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		return NewPreparePublication(wire.Header, payload), nil
	case IntentVerifyPublication:
		var payload VerifyPublicationPayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		return NewVerifyPublication(wire.Header, payload), nil
	case IntentActivatePublication:
		if string(bytes.TrimSpace(wire.Payload)) != "null" {
			return nil, &Error{Code: ErrorInvalidIntent}
		}
		return NewActivatePublication(wire.Header), nil
	case IntentRejectPublication:
		var payload rejectWirePayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		return NewRejectPublication(wire.Header, payload.Reason, payload.EvidenceFailure), nil
	case IntentCancelPublication:
		var payload cancelWirePayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		return NewCancelPublication(wire.Header, payload.Reason), nil
	case IntentReconcilePublication:
		var payload reconcileWirePayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		return NewReconcilePublication(wire.Header, payload.Mode), nil
	case IntentRecordResidueAssembly:
		var payload recordAssemblyWirePayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		return NewRecordResidueAssembly(wire.Header, payload.Assembly), nil
	case IntentReleaseResidue:
		if string(bytes.TrimSpace(wire.Payload)) != "null" {
			return nil, &Error{Code: ErrorInvalidIntent}
		}
		return NewReleaseResidue(wire.Header), nil
	case IntentResolveCleanupDebt:
		var payload resolveDebtWirePayload
		if err := json.Unmarshal(wire.Payload, &payload); err != nil {
			return nil, err
		}
		if payload.Evidence != nil {
			return NewResolveCleanupDebtEvidence(wire.Header, payload.ResolutionClass, *payload.Evidence), nil
		}
		return NewResolveCleanupDebtException(wire.Header, payload.ApprovalReference, payload.ExpiresAt), nil
	default:
		return nil, &Error{Code: ErrorInvalidIntent}
	}
}

// verifyMutationEnvelope proves the reconstructed intent carries exactly the
// envelope's authoritative facts: policy domain, Task, OperationID, the
// business canonical digest, generation, fence and safety epoch.
func verifyMutationEnvelope(
	wire ownedTransportWireMutation,
	intent PublicationIntent,
	envelope OwnedTransportEnvelope,
) bool {
	header := intent.header()
	if header.PolicyDomainID != envelope.PolicyDomainID || header.TaskID != envelope.TaskID ||
		header.Operation.ID != envelope.OperationID ||
		header.Operation.RequestDigest != envelope.CanonicalRequestDigest ||
		CanonicalRequestDigest(intent) != envelope.CanonicalRequestDigest ||
		header.Generation != envelope.Generation || header.Fence != envelope.Fence ||
		header.SafetyEpoch != envelope.SafetyEpoch {
		return false
	}
	return wire.Kind == intent.kind()
}

// verifyQueryEnvelope proves the query carries exactly the envelope's
// authoritative facts and that the wire digest matches.
func verifyQueryEnvelope(query *PublicationQuery, envelope OwnedTransportEnvelope) bool {
	if query.PolicyDomainID != envelope.PolicyDomainID || query.TaskID != envelope.TaskID ||
		ownedTransportQueryOperationID(*query) != envelope.OperationID ||
		ownedTransportQueryDigest(*query) != envelope.CanonicalRequestDigest {
		return false
	}
	return validQueryKind(query.Kind)
}

// ownedTransportQueryOperationID derives the transport-level call identity
// of a read-only query. Operation-scoped queries keep the ORIGINAL
// OperationID so response-loss inspection/reconciliation always returns to
// the original operation.
func ownedTransportQueryOperationID(query PublicationQuery) PublicationOperationID {
	subject := string(query.TaskID)
	switch query.Kind {
	case QueryOperation, QueryResidue, QueryCleanupDebt:
		subject = string(query.OperationID)
	case QueryCandidate, QueryExactVersion:
		subject = string(query.ArtifactVersionID)
	case QueryExactMember, QueryResolveContentTarget, QueryVerifyContentTarget,
		QueryIssueC04ReconstructionCapability, QueryVerifyC04ReconstructionCapability:
		subject = string(query.ArtifactVersionID) + ":" + string(query.ArtifactID)
	}
	return PublicationOperationID("publication-query:" + string(query.Kind) + ":" + subject)
}

// ownedTransportQueryDigest is the deterministic digest of one query. The
// same query always produces the same digest, so retries are idempotent.
func ownedTransportQueryDigest(query PublicationQuery) Digest {
	encoded, err := json.Marshal(query)
	if err != nil {
		return Digest("")
	}
	return ownedTransportDigest(encoded)
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
	publicationError := normalizeError(err)
	return OwnedTransportResponse{
		Method:                 envelope.Method,
		OperationID:            envelope.OperationID,
		CanonicalRequestDigest: envelope.CanonicalRequestDigest,
		Error: &OwnedTransportWireError{
			Code:                   publicationError.Code,
			SafeCategory:           publicationError.SafeCategory(),
			Retryable:              publicationError.Retryable(),
			ReconciliationRequired: publicationError.ReconciliationRequired(),
		},
	}
}

func decodeOwnedTransportError(wire OwnedTransportWireError) error {
	if !validOwnedTransportWireError(wire) {
		return &Error{Code: ErrorIntegrityConflict}
	}
	return &Error{Code: wire.Code}
}

func validOwnedTransportWireError(wire OwnedTransportWireError) bool {
	if !knownErrorCode(wire.Code) {
		return false
	}
	expected := &Error{Code: wire.Code}
	return wire.SafeCategory == expected.SafeCategory() &&
		wire.Retryable == expected.Retryable() &&
		wire.ReconciliationRequired == expected.ReconciliationRequired()
}

func validOwnedTransportCallbackPayload(method OwnedTransportMethod, payload []byte) bool {
	switch method {
	case ownedTransportMutate:
		return canonicalOwnedTransportResult[PublicationDecision](payload)
	case ownedTransportQuery:
		return canonicalOwnedTransportResult[PublicationView](payload)
	default:
		return false
	}
}

func canonicalOwnedTransportResult[Result any](payload []byte) bool {
	var result Result
	return decodeOwnedTransportPayload(payload, &result) == nil
}

// decodeOwnedTransportPayload decodes and re-encodes the payload so only
// the canonical JSON encoding is accepted (non-canonical wire fails
// closed).
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

func ownedTransportDigest(payload []byte) Digest {
	digest := sha256.Sum256(payload)
	return Digest("sha256:" + hex.EncodeToString(digest[:]))
}

func signOwnedTransportEnvelope(envelope OwnedTransportEnvelope, key []byte) Digest {
	canonical, _ := json.Marshal(struct {
		Kind                       string
		SchemaVersion              string
		Method                     OwnedTransportMethod
		MachineAuthorityID         OwnedTransportMachineAuthorityID
		MachineAuthorityGeneration Generation
		PolicyDomainID             PolicyDomainID
		TaskID                     TaskID
		OperationID                PublicationOperationID
		CanonicalRequestDigest     Digest
		PayloadDigest              Digest
		Deadline                   Instant
		Generation                 Generation
		Fence                      Fence
		SafetyEpoch                SafetyEpoch
	}{
		"envelope",
		envelope.SchemaVersion,
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
		envelope.SafetyEpoch,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return Digest("hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)))
}

func signOwnedTransportResponse(response OwnedTransportResponse, key []byte) Digest {
	canonical, _ := json.Marshal(struct {
		Kind                   string
		Method                 OwnedTransportMethod
		OperationID            PublicationOperationID
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
		OperationID            PublicationOperationID
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
