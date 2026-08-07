// Package runtimeexecution — durable adapter normalization contract.
//
// Sync, poll, callback, and queue adapters converge on a single durable
// asynchronous owned adapter contract. Vendor/transport differences cannot
// change Runtime lifecycle, idempotency, fencing, or evidence semantics.
package runtimeexecution

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Adapter kind taxonomy
// ---------------------------------------------------------------------------

// TransportKind classifies the downstream delivery contract of an adapter.
// Every adapter normalizes to the same C03 invariant engine regardless of kind.
type TransportKind uint8

const (
	TransportSync     TransportKind = iota + 1 // synchronous call with caller timeout
	TransportPoll                              // client polls for completion
	TransportCallback                          // external producer calls back with evidence
	TransportQueue                             // at-least-once queue delivery
)

func (kind TransportKind) String() string {
	switch kind {
	case TransportSync:
		return "sync"
	case TransportPoll:
		return "poll"
	case TransportCallback:
		return "callback"
	case TransportQueue:
		return "queue"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Durable operation identity — shared by every adapter
// ---------------------------------------------------------------------------

// ExternalOperationHandle is the opaque, adapter-owned reference to an
// external operation. It is never interpreted by C03 and never enters
// authoritative PostgreSQL business records.
type ExternalOperationHandle struct {
	Raw   string
	Opaque string
}

// ExternalOperationCursor is an opaque position that a poll or observe
// adapter persists before issuing the next downstream request. It is never
// business authority and is bound to the adapter producer.
type ExternalOperationCursor struct {
	ProducerAuthority  WorkerAuthorityID
	ProducerGeneration WorkerGeneration
	StreamGeneration   uint64
	Position           uint64
	OpaquePayload      string
}

func (cursor ExternalOperationCursor) CanonicalDigest() Digest {
	parts := []string{
		"slidesmith.runtime-execution.external-operation-cursor/v1",
		cursor.ProducerAuthority.String(),
		fmt.Sprint(cursor.ProducerGeneration),
		fmt.Sprint(cursor.StreamGeneration),
		fmt.Sprint(cursor.Position),
		cursor.OpaquePayload,
	}
	return digestBytes([]byte(strings.Join(parts, "\n")))
}

// ---------------------------------------------------------------------------
// Adapter durable contract — one interface for all transport kinds
// ---------------------------------------------------------------------------

// DurableAdapterBinding records the stable identity the adapter must preserve
// across restarts, delivery loss, and reconciliation.
type DurableAdapterBinding struct {
	Transport          TransportKind
	OperationID        OperationID
	CanonicalDigest    Digest
	ExternalHandle     ExternalOperationHandle
	Cursor             ExternalOperationCursor
	PersistedAt        time.Time
	Reconcilable       bool
}

// AdapterNormalizedError is the closed safe error category that every adapter
// must use. Raw vendor, provider, or transport details are never retained in
// this error; they may only appear in protected opaque evidence roots.
type AdapterNormalizedError struct {
	Code           AdapterErrorCode
	Retry          RetryDisposition
	Reconciliation ReconciliationDisposition
	EvidenceRoot   EvidenceRootID // opaque reference; no raw detail leaked here
}

func (failure *AdapterNormalizedError) Error() string {
	if failure == nil {
		return "adapter rejected request"
	}
	return fmt.Sprintf("adapter error: %s", failure.Code.String())
}

// IsAdapterError reports whether an error was produced by an adapter and
// carries a safe normalized code.
func IsAdapterError(err error) bool {
	var normalized *AdapterNormalizedError
	return errors.As(err, &normalized)
}

// AdapterErrorCode enumerates the closed safe categories that every adapter
// normalizes to. Raw vendor/provider/transport details are never exposed
// through these codes.
type AdapterErrorCode uint8

const (
	AdapterErrorNone AdapterErrorCode = iota

	// Authorization / identity
	AdapterErrorUnauthorized        // producer or caller not authorized
	AdapterErrorStaleIdentity       // stale worker/node/generation/fence

	// Integrity / binding
	AdapterErrorIntegrityConflict   // digest, operation, or binding mismatch
	AdapterErrorStaleBinding        // stale lease, fence, or safety epoch
	AdapterErrorDuplicateOperation  // same-key operation already observed

	// Transport / dependency
	AdapterErrorTransportUnavailable // sync timeout, poll interruption, queue claim loss
	AdapterErrorCallbackUnavailable  // callback endpoint unreachable
	AdapterErrorDependencyUnavailable // prerequisite not ready

	// Execution
	AdapterErrorCapabilityFailure   // agent or tool reported failure
	AdapterErrorDeadlineExceeded    // operation exceeded deadline
	AdapterErrorCancelled           // operation was cancelled
	AdapterErrorAmbiguous           // operation outcome cannot be determined

	// Evidence
	AdapterErrorCorruptEvidence     // evidence digest, schema, or producer mismatch
	AdapterErrorMissingEvidence     // required evidence not present
	AdapterErrorStaleEvidence       // evidence from stale generation/fence
)

func (code AdapterErrorCode) String() string {
	switch code {
	case AdapterErrorNone:
		return "none"
	case AdapterErrorUnauthorized:
		return "unauthorized"
	case AdapterErrorStaleIdentity:
		return "stale_identity"
	case AdapterErrorIntegrityConflict:
		return "integrity_conflict"
	case AdapterErrorStaleBinding:
		return "stale_binding"
	case AdapterErrorDuplicateOperation:
		return "duplicate_operation"
	case AdapterErrorTransportUnavailable:
		return "transport_unavailable"
	case AdapterErrorCallbackUnavailable:
		return "callback_unavailable"
	case AdapterErrorDependencyUnavailable:
		return "dependency_unavailable"
	case AdapterErrorCapabilityFailure:
		return "capability_failure"
	case AdapterErrorDeadlineExceeded:
		return "deadline_exceeded"
	case AdapterErrorCancelled:
		return "cancelled"
	case AdapterErrorAmbiguous:
		return "ambiguous"
	case AdapterErrorCorruptEvidence:
		return "corrupt_evidence"
	case AdapterErrorMissingEvidence:
		return "missing_evidence"
	case AdapterErrorStaleEvidence:
		return "stale_evidence"
	default:
		return "unknown"
	}
}

// NewAdapterError constructs a safe normalized error from an error code.
// The optional evidenceRoot pins opaque diagnostic evidence; it must never
// contain raw vendor/provider/transport details that could leak content,
// credentials, or identities.
func NewAdapterError(code AdapterErrorCode, evidenceRoot EvidenceRootID) *AdapterNormalizedError {
	failure := &AdapterNormalizedError{
		Code:         code,
		EvidenceRoot: evidenceRoot,
	}
	switch code {
	case AdapterErrorTransportUnavailable, AdapterErrorCallbackUnavailable,
		AdapterErrorDependencyUnavailable:
		failure.Retry = RetryAfterDependency
	case AdapterErrorAmbiguous:
		failure.Retry = RetrySameRequest
		failure.Reconciliation = ReconciliationRequired
	case AdapterErrorNone, AdapterErrorUnauthorized, AdapterErrorStaleIdentity,
		AdapterErrorIntegrityConflict, AdapterErrorStaleBinding,
		AdapterErrorDuplicateOperation, AdapterErrorCapabilityFailure,
		AdapterErrorDeadlineExceeded, AdapterErrorCancelled,
		AdapterErrorCorruptEvidence, AdapterErrorMissingEvidence,
		AdapterErrorStaleEvidence:
		failure.Retry = RetryNever
	}
	return failure
}

// ---------------------------------------------------------------------------
// Sync adapter contract
// ---------------------------------------------------------------------------

// SyncAdapterRequest is the typed durable request a sync adapter must bind
// before making any downstream call.
type SyncAdapterRequest struct {
	OperationID     OperationID
	CanonicalDigest Digest
	Payload         []byte
	Deadline        time.Time
	CallerTimeout   time.Duration
}

// SyncAdapterResponse is the normalized sync response.
type SyncAdapterResponse struct {
	OperationID     OperationID
	CanonicalDigest Digest
	ExternalHandle  ExternalOperationHandle
	Result          []byte
	Completed       bool
	NormalizedError *AdapterNormalizedError
}

// SyncAdapter is a synchronous worker adapter. It must durable-bind the stable
// operation before the call and support Inspect/Observe after caller timeout.
type SyncAdapter interface {
	// BindSync durably records the stable operation identity before any
	// downstream call. The returned binding must survive process restart.
	BindSync(ctx context.Context, request SyncAdapterRequest) (DurableAdapterBinding, error)

	// InvokeSync performs the synchronous downstream call. Caller timeout
	// (context deadline) must not be treated as downstream completion; the
	// adapter must enter reconciliation instead.
	InvokeSync(ctx context.Context, binding DurableAdapterBinding) (SyncAdapterResponse, error)

	// InspectSync observes the external operation after caller timeout.
	// It must not fabricate success or failure from missing observation.
	InspectSync(ctx context.Context, binding DurableAdapterBinding) (SyncAdapterResponse, error)

	TransportKind() TransportKind
}

// ---------------------------------------------------------------------------
// Poll adapter contract
// ---------------------------------------------------------------------------

// PollAdapterRequest is the typed request a poll adapter persists before
// scheduling the first poll.
type PollAdapterRequest struct {
	OperationID     OperationID
	CanonicalDigest Digest
	Payload         []byte
	PollInterval    time.Duration
	Deadline        time.Time
}

// PollAdapterObservation records the external state at each poll cycle.
type PollAdapterObservation struct {
	ExternalHandle  ExternalOperationHandle
	Cursor          ExternalOperationCursor
	Completed       bool
	Result          []byte
	NormalizedError *AdapterNormalizedError
}

// PollAdapter persists an opaque external operation and cursor before any poll.
// Poll loss or interruption enters reconciliation instead of fabricating
// completion.
type PollAdapter interface {
	// BindPoll durably records the operation and initial cursor before the
	// first poll cycle.
	BindPoll(ctx context.Context, request PollAdapterRequest) (DurableAdapterBinding, error)

	// PollOnce executes one polling cycle. It must persist the cursor before
	// returning and must not treat poll interruption as terminal.
	PollOnce(ctx context.Context, binding DurableAdapterBinding) (PollAdapterObservation, error)

	TransportKind() TransportKind
}

// ---------------------------------------------------------------------------
// Callback adapter contract
// ---------------------------------------------------------------------------

// CallbackAdapterRequest records the durable identity that a callback
// producer must echo in every callback.
type CallbackAdapterRequest struct {
	OperationID     OperationID
	CanonicalDigest Digest
	CallbackURL     string
	Deadline        time.Time
}

// CallbackNotification is the external callback received from the producer.
// It must be authenticated and validated before acceptance.
type CallbackNotification struct {
	OperationID        OperationID
	Digest             Digest
	ProducerAuth       WorkerAuthorityID
	ProducerGeneration WorkerGeneration
	RuntimeFence       RuntimeFence
	LeaseGeneration    LeaseGeneration
	LeaseFence         LeaseFence
	Order              uint64
	Payload            []byte
	Signature          []byte
}

// CallbackAdapter authenticates and deduplicates callbacks, maps them to the
// exact operation and fence, and treats them as evidence rather than business
// authorization. Duplicate, out-of-order, unauthorized, or corrupt callbacks
// are rejected with a safe normalized error.
type CallbackAdapter interface {
	// BindCallback durably records the expected callback identity so
	// duplicate or stale callbacks can be detected.
	BindCallback(ctx context.Context, request CallbackAdapterRequest) (DurableAdapterBinding, error)

	// VerifyCallback authenticates a received callback against the durable
	// binding. It validates producer auth, operation/digest/generation/fence,
	// and order. It does not mutate business authority.
	VerifyCallback(ctx context.Context, binding DurableAdapterBinding, notification CallbackNotification) (CallbackVerification, error)

	TransportKind() TransportKind
}

// CallbackVerification is the normalized result of callback authentication.
type CallbackVerification struct {
	Accepted        bool
	Duplicate       bool
	OutOfOrder      bool
	NormalizedError *AdapterNormalizedError
}

// ---------------------------------------------------------------------------
// Queue adapter contract
// ---------------------------------------------------------------------------

// QueueAdapterRequest is the typed request a queue adapter delivers
// at-least-once and acknowledges only after durable C03 acceptance.
type QueueAdapterRequest struct {
	OperationID     OperationID
	CanonicalDigest Digest
	Payload         []byte
	DeliveryTag     string
	Deadline        time.Time
}

// QueueAdapterDelivery records an at-least-once delivery attempt.
type QueueAdapterDelivery struct {
	OperationID     OperationID
	CanonicalDigest Digest
	Payload         []byte
	DeliveryTag     string
	DeliveryCount   uint64
	Redelivered     bool
}

// QueueAdapterAck confirms durable acceptance to the broker.
type QueueAdapterAck struct {
	DeliveryTag  string
	Accepted     bool
	RequeueDelay time.Duration
}

// QueueAdapter uses at-least-once delivery and acknowledges only after
// durable start acceptance. Claim loss or duplicate delivery enters
// reconciliation instead of fabricating a new outcome.
type QueueAdapter interface {
	// BindQueue durably records the expected delivery identity before
	// acknowledging the broker.
	BindQueue(ctx context.Context, request QueueAdapterRequest) (DurableAdapterBinding, error)

	// ReceiveDelivery receives one at-least-once delivery from the broker.
	ReceiveDelivery(ctx context.Context, binding DurableAdapterBinding) (QueueAdapterDelivery, error)

	// AckDelivery acknowledges (or nacks) the delivery to the broker.
	// Ack is emitted only after durable C03 start acceptance.
	AckDelivery(ctx context.Context, delivery QueueAdapterDelivery, ack QueueAdapterAck) error

	TransportKind() TransportKind
}

// ---------------------------------------------------------------------------
// Opaque evidence root — adapter evidence storage
// ---------------------------------------------------------------------------

// AdapterEvidenceRoot is an opaque container for raw vendor/provider/transport
// evidence. Its contents are never interpreted by C03 business logic and never
// appear in authoritative PostgreSQL records. Only the EvidenceRootID and
// digest participate in the C03 evidence tree.
type AdapterEvidenceRoot struct {
	ID             EvidenceRootID
	Digest         Digest
	SchemaVersion  SchemaVersion
	AdapterKind    TransportKind
	OperationID    OperationID
	OpaquePayload  []byte
	NormalizedCode AdapterErrorCode
	StoredAt       time.Time
}

// AdapterEvidenceStore is an opaque persistence seam for adapter evidence.
// It is intentionally separate from the C03 authoritative store.
type AdapterEvidenceStore interface {
	StoreEvidence(ctx context.Context, root AdapterEvidenceRoot) error
	LoadEvidence(ctx context.Context, id EvidenceRootID) (AdapterEvidenceRoot, error)
}

// NormalizeExternalEvidence produces an AdapterEvidenceRoot from raw external
// adapter bytes. The raw payload is stored opaquely; C03 only trusts the
// normalized code and digest.
func NormalizeExternalEvidence(
	adapterKind TransportKind,
	operationID OperationID,
	rawPayload []byte,
	normalizedCode AdapterErrorCode,
) (AdapterEvidenceRoot, error) {
	if len(rawPayload) == 0 || normalizedCode == AdapterErrorNone {
		return AdapterEvidenceRoot{}, newError(ErrorInvalidRequest)
	}
	root := AdapterEvidenceRoot{
		SchemaVersion:  SchemaV1,
		AdapterKind:    adapterKind,
		OperationID:    operationID,
		OpaquePayload:  append([]byte(nil), rawPayload...),
		NormalizedCode: normalizedCode,
		StoredAt:       time.Now().UTC(),
	}
	root.Digest = adapterEvidenceRootDigest(root)
	root.ID = adapterEvidenceRootID(root.Digest)
	return root, nil
}

func adapterEvidenceRootDigest(root AdapterEvidenceRoot) Digest {
	return digestBytes([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.adapter-evidence-root/v1",
		fmt.Sprint(root.SchemaVersion),
		fmt.Sprint(root.AdapterKind),
		root.OperationID.String(),
		hex.EncodeToString(root.OpaquePayload),
		fmt.Sprint(root.NormalizedCode),
		root.StoredAt.UTC().Format(time.RFC3339Nano),
	}, "\n")))
}

func adapterEvidenceRootID(digest Digest) EvidenceRootID {
	return EvidenceRootID{value: fmt.Sprintf("adapter-evidence-%x", digest[:12])}
}

// ---------------------------------------------------------------------------
// Adapter contract validation helpers
// ---------------------------------------------------------------------------

// ValidDurableBinding reports whether a binding carries the minimum fields
// for reconciliation.
func ValidDurableBinding(binding DurableAdapterBinding) bool {
	return binding.Transport >= TransportSync && binding.Transport <= TransportQueue &&
		validOpaqueID(binding.OperationID.String()) &&
		binding.CanonicalDigest != (Digest{}) &&
		!binding.PersistedAt.IsZero()
}

// ValidateCallbackNotification checks that a callback carries the required
// producer auth, operation identity, and fence fields. It does not check the
// signature or verify against a durable binding.
func ValidateCallbackNotification(notification CallbackNotification) *AdapterNormalizedError {
	if !validOpaqueID(notification.OperationID.String()) || notification.Digest == (Digest{}) {
		return NewAdapterError(AdapterErrorCorruptEvidence, EvidenceRootID{})
	}
	if !validOpaqueID(notification.ProducerAuth.String()) || notification.ProducerGeneration == 0 {
		return NewAdapterError(AdapterErrorUnauthorized, EvidenceRootID{})
	}
	if notification.RuntimeFence == 0 || notification.LeaseGeneration == 0 || notification.LeaseFence == 0 {
		return NewAdapterError(AdapterErrorStaleBinding, EvidenceRootID{})
	}
	if len(notification.Signature) == 0 {
		return NewAdapterError(AdapterErrorCorruptEvidence, EvidenceRootID{})
	}
	return nil
}

// NormalizeCallbackError converts a callback verification failure into a safe
// adapter error with an opaque evidence root.
func NormalizeCallbackError(
	ctx context.Context,
	notification CallbackNotification,
	code AdapterErrorCode,
	store AdapterEvidenceStore,
) *AdapterNormalizedError {
	raw, err := json.Marshal(notification)
	if err != nil {
		return NewAdapterError(code, EvidenceRootID{})
	}
	root, err := NormalizeExternalEvidence(TransportCallback, notification.OperationID, raw, code)
	if err != nil {
		return NewAdapterError(code, EvidenceRootID{})
	}
	if store != nil {
		_ = store.StoreEvidence(ctx, root)
	}
	return NewAdapterError(code, root.ID)
}
