package taskorchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// OwnedTransportVersion identifies the versioned, content-free delivery
// envelope exchanged with an owned downstream adapter.
type OwnedTransportVersion uint32

const OwnedTransportV1 OwnedTransportVersion = 1 << 16

func (version OwnedTransportVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version OwnedTransportVersion) Minor() uint16 { return uint16(version) }

type DeliveryPrerequisites struct {
	TaskRevision        TaskRevision
	AcceptedEvidenceIDs []EvidenceID
}

type OwnedTransportRequest struct {
	Version            OwnedTransportVersion
	Authority          WorkerAuthority
	OperationID        OperationID
	DecisionID         DecisionID
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	RuntimeRunID       RuntimeRunID
	Kind               EnactmentKind
	PayloadDigest      EnactmentPayloadDigest
	ActivityGeneration ActivityGeneration
	SafetyEpoch        SafetyEpoch
	FenceKind          EnactmentFenceKind
	Fence              uint64
	CausationID        CausationID
	Prerequisites      DeliveryPrerequisites
}

type DeliveryResultDigest [32]byte

func ParseDeliveryResultDigest(value string) (DeliveryResultDigest, error) {
	var digest DeliveryResultDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return DeliveryResultDigest{}, newDeliveryError(DeliveryInvalidRequest)
	}
	copy(digest[:], decoded)
	return digest, nil
}

func (digest DeliveryResultDigest) String() string { return hex.EncodeToString(digest[:]) }

type OwnedTransportOutcome uint8

const (
	OwnedTransportAccepted OwnedTransportOutcome = iota + 1
	OwnedTransportIntegrityConflict
	OwnedTransportUnknown
	OwnedTransportUnsupportedVersion
	OwnedTransportUnauthorized
	OwnedTransportPoisoned
	OwnedTransportBackpressured
	OwnedTransportSuperseded
	OwnedTransportDeferred
)

type OwnedTransportDeferralReason uint8

const (
	OwnedTransportPrerequisiteDeferred OwnedTransportDeferralReason = iota + 1
)

type OwnedTransportResponse struct {
	Version        OwnedTransportVersion
	OperationID    OperationID
	Outcome        OwnedTransportOutcome
	ResultDigest   DeliveryResultDigest
	Duplicate      bool
	RetryAt        time.Time
	DeferralReason OwnedTransportDeferralReason
}

type OwnedTransportInspection struct {
	Version     OwnedTransportVersion
	Authority   WorkerAuthority
	OperationID OperationID
}

type OwnedTransport interface {
	Deliver(context.Context, OwnedTransportRequest) (OwnedTransportResponse, error)
	Inspect(context.Context, OwnedTransportInspection) (OwnedTransportResponse, error)
}

type OwnedTransportConfig struct {
	SupportedVersion OwnedTransportVersion
	Authorities      []WorkerAuthority
}

type OwnedTransportErrorCode uint8

const (
	OwnedTransportInvalidConfiguration OwnedTransportErrorCode = iota + 1
	OwnedTransportUnavailable
)

type OwnedTransportError struct {
	code OwnedTransportErrorCode
}

func (err *OwnedTransportError) Error() string {
	if err != nil && err.code == OwnedTransportUnavailable {
		return "owned transport is unavailable"
	}
	return "owned transport configuration is invalid"
}

func (err *OwnedTransportError) Code() OwnedTransportErrorCode {
	if err == nil {
		return OwnedTransportInvalidConfiguration
	}
	return err.code
}

type ownedTransportJournalRecord struct {
	binding  [32]byte
	response OwnedTransportResponse
}

// DeterministicOwnedTransport is an owned, restartable protocol harness. It
// records only content-free envelope bindings and terminal results.
type DeterministicOwnedTransport struct {
	config OwnedTransportConfig
	state  *deterministicOwnedTransportState
}

type deterministicOwnedTransportState struct {
	mu      sync.Mutex
	journal map[OperationID]ownedTransportJournalRecord
	latest  map[ownedTransportScope]ownedTransportOrder
}

type ownedTransportScope struct {
	taskID       TaskID
	fenceKind    EnactmentFenceKind
	phaseRunID   PhaseRunID
	runtimeRunID RuntimeRunID
}

type ownedTransportOrder struct {
	activityGeneration ActivityGeneration
	fence              uint64
}

func NewDeterministicOwnedTransport(
	config OwnedTransportConfig,
) (*DeterministicOwnedTransport, error) {
	if config.SupportedVersion != OwnedTransportV1 || len(config.Authorities) == 0 {
		return nil, &OwnedTransportError{code: OwnedTransportInvalidConfiguration}
	}
	for _, authority := range config.Authorities {
		if !validDeliveryAuthority(authority) {
			return nil, &OwnedTransportError{code: OwnedTransportInvalidConfiguration}
		}
	}
	config.Authorities = append([]WorkerAuthority(nil), config.Authorities...)
	return &DeterministicOwnedTransport{
		config: config,
		state: &deterministicOwnedTransportState{
			journal: make(map[OperationID]ownedTransportJournalRecord),
			latest:  make(map[ownedTransportScope]ownedTransportOrder),
		},
	}, nil
}

// Restart creates a new adapter around the same durable owned journal.
func (transport *DeterministicOwnedTransport) Restart() *DeterministicOwnedTransport {
	if transport == nil {
		return nil
	}
	config := transport.config
	config.Authorities = append([]WorkerAuthority(nil), config.Authorities...)
	return &DeterministicOwnedTransport{config: config, state: transport.state}
}

func (transport *DeterministicOwnedTransport) Deliver(
	ctx context.Context,
	request OwnedTransportRequest,
) (OwnedTransportResponse, error) {
	if ctx == nil || ctx.Err() != nil {
		return OwnedTransportResponse{}, &OwnedTransportError{code: OwnedTransportUnavailable}
	}
	if request.Version != transport.config.SupportedVersion {
		return OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: request.OperationID,
			Outcome: OwnedTransportUnsupportedVersion,
		}, nil
	}
	if !transport.authorized(request.Authority) {
		return OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: request.OperationID,
			Outcome: OwnedTransportUnauthorized,
		}, nil
	}
	if !validOwnedTransportRequest(request) {
		return OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: request.OperationID,
			Outcome: OwnedTransportPoisoned,
		}, nil
	}
	binding, err := ownedTransportBinding(request)
	if err != nil {
		return OwnedTransportResponse{}, &OwnedTransportError{code: OwnedTransportUnavailable}
	}
	transport.state.mu.Lock()
	defer transport.state.mu.Unlock()
	if committed, exists := transport.state.journal[request.OperationID]; exists {
		if committed.binding != binding {
			return OwnedTransportResponse{
				Version: transport.config.SupportedVersion, OperationID: request.OperationID,
				Outcome: OwnedTransportIntegrityConflict,
			}, nil
		}
		response := committed.response
		response.Duplicate = true
		return response, nil
	}
	scope := ownedTransportScope{
		taskID: request.TaskID, fenceKind: request.FenceKind,
		phaseRunID: request.PhaseRunID, runtimeRunID: request.RuntimeRunID,
	}
	order := ownedTransportOrder{
		activityGeneration: request.ActivityGeneration, fence: request.Fence,
	}
	if latest, exists := transport.state.latest[scope]; exists && order.before(latest) {
		response := OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: request.OperationID,
			Outcome: OwnedTransportSuperseded,
		}
		transport.state.journal[request.OperationID] = ownedTransportJournalRecord{
			binding: binding, response: response,
		}
		return response, nil
	}
	result := sha256.Sum256(append([]byte("owned-transport-result-v1|"), binding[:]...))
	response := OwnedTransportResponse{
		Version: transport.config.SupportedVersion, OperationID: request.OperationID,
		Outcome: OwnedTransportAccepted, ResultDigest: DeliveryResultDigest(result),
	}
	transport.state.journal[request.OperationID] = ownedTransportJournalRecord{
		binding: binding, response: response,
	}
	if latest, exists := transport.state.latest[scope]; !exists || latest.before(order) {
		transport.state.latest[scope] = order
	}
	return response, nil
}

func (order ownedTransportOrder) before(other ownedTransportOrder) bool {
	return order.activityGeneration < other.activityGeneration ||
		order.activityGeneration == other.activityGeneration && order.fence < other.fence
}

func (transport *DeterministicOwnedTransport) Inspect(
	ctx context.Context,
	inspection OwnedTransportInspection,
) (OwnedTransportResponse, error) {
	if ctx == nil || ctx.Err() != nil {
		return OwnedTransportResponse{}, &OwnedTransportError{code: OwnedTransportUnavailable}
	}
	if inspection.Version != transport.config.SupportedVersion {
		return OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: inspection.OperationID,
			Outcome: OwnedTransportUnsupportedVersion,
		}, nil
	}
	if !transport.authorized(inspection.Authority) || !validOpaqueID(inspection.OperationID.value) {
		return OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: inspection.OperationID,
			Outcome: OwnedTransportUnauthorized,
		}, nil
	}
	transport.state.mu.Lock()
	defer transport.state.mu.Unlock()
	committed, exists := transport.state.journal[inspection.OperationID]
	if !exists {
		return OwnedTransportResponse{
			Version: transport.config.SupportedVersion, OperationID: inspection.OperationID,
			Outcome: OwnedTransportUnknown,
		}, nil
	}
	response := committed.response
	response.Duplicate = true
	return response, nil
}

func (transport *DeterministicOwnedTransport) authorized(authority WorkerAuthority) bool {
	for _, allowed := range transport.config.Authorities {
		if allowed.value == authority.value {
			return true
		}
	}
	return false
}

func validOwnedTransportRequest(request OwnedTransportRequest) bool {
	if request.Version != OwnedTransportV1 || !validDeliveryAuthority(request.Authority) ||
		!validOpaqueID(request.OperationID.value) || !validOpaqueID(request.DecisionID.value) ||
		!validOpaqueID(request.TaskID.value) || !validOptionalOpaqueID(request.PhaseRunID.value) ||
		!validOptionalOpaqueID(request.RuntimeRunID.value) || !validEnactmentKind(request.Kind) ||
		request.PayloadDigest == (EnactmentPayloadDigest{}) || request.ActivityGeneration == 0 ||
		request.SafetyEpoch == 0 ||
		!validEnactmentFenceKind(request.FenceKind) || request.Fence == 0 ||
		!validOpaqueID(request.CausationID.value) || request.Prerequisites.TaskRevision == 0 {
		return false
	}
	for _, evidenceID := range request.Prerequisites.AcceptedEvidenceIDs {
		if !validOpaqueID(evidenceID.value) {
			return false
		}
	}
	return true
}

func validEnactmentKind(kind EnactmentKind) bool {
	return kind >= EnactmentRuntimeExecution && kind <= EnactmentPresentConfirmationGate
}

func validEnactmentFenceKind(kind EnactmentFenceKind) bool {
	return kind >= EnactmentFenceRuntimeExecution && kind <= EnactmentFenceConfirmation
}

func ownedTransportBinding(request OwnedTransportRequest) ([32]byte, error) {
	evidenceIDs := make([]string, 0, len(request.Prerequisites.AcceptedEvidenceIDs))
	for _, evidenceID := range request.Prerequisites.AcceptedEvidenceIDs {
		evidenceIDs = append(evidenceIDs, evidenceID.value)
	}
	sort.Strings(evidenceIDs)
	encoded, err := json.Marshal(struct {
		Version             OwnedTransportVersion
		AuthorityKind       AuthorityKind
		AuthorityID         string
		AuthorityGeneration AuthorizationGeneration
		OperationID         string
		DecisionID          string
		TaskID              string
		PhaseRunID          string
		RuntimeRunID        string
		Kind                EnactmentKind
		PayloadDigest       string
		ActivityGeneration  ActivityGeneration
		SafetyEpoch         SafetyEpoch
		FenceKind           EnactmentFenceKind
		Fence               uint64
		CausationID         string
		TaskRevision        TaskRevision
		AcceptedEvidenceIDs []string
	}{
		Version: request.Version, AuthorityKind: request.Authority.value.kind,
		AuthorityID:         request.Authority.value.id.value,
		AuthorityGeneration: request.Authority.value.generation,
		OperationID:         request.OperationID.value, DecisionID: request.DecisionID.value,
		TaskID: request.TaskID.value, PhaseRunID: request.PhaseRunID.value,
		RuntimeRunID: request.RuntimeRunID.value, Kind: request.Kind,
		PayloadDigest:      request.PayloadDigest.String(),
		ActivityGeneration: request.ActivityGeneration, SafetyEpoch: request.SafetyEpoch,
		FenceKind: request.FenceKind,
		Fence:     request.Fence, CausationID: request.CausationID.value,
		TaskRevision: request.Prerequisites.TaskRevision, AcceptedEvidenceIDs: evidenceIDs,
	})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

type DispatcherConfig struct {
	Now              func() time.Time
	MaxBatchSize     uint32
	LeaseDuration    time.Duration
	TransportVersion OwnedTransportVersion
	Authorities      []WorkerAuthority
	Faults           DeliveryFaultInjector
}

type DeliveryFaultPoint uint8

const (
	DeliveryFaultBeforeClaimCommit DeliveryFaultPoint = iota + 1
	DeliveryFaultAfterClaimCommit
	DeliveryFaultBeforeSend
	DeliveryFaultAfterSend
	DeliveryFaultBeforeDispositionCommit
	DeliveryFaultAfterDispositionCommit
)

type DeliveryFaultInjector interface {
	FailAt(DeliveryFaultPoint) bool
}

type DeliveryFaultController struct {
	mu   sync.Mutex
	next DeliveryFaultPoint
}

func (controller *DeliveryFaultController) FailNextAt(point DeliveryFaultPoint) error {
	if point < DeliveryFaultBeforeClaimCommit || point > DeliveryFaultAfterDispositionCommit {
		return newDeliveryError(DeliveryInvalidRequest)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.next = point
	return nil
}

func (controller *DeliveryFaultController) FailAt(point DeliveryFaultPoint) bool {
	if controller == nil {
		return false
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.next != point {
		return false
	}
	controller.next = 0
	return true
}

type DeliveryErrorCode uint8

const (
	DeliveryInvalidRequest DeliveryErrorCode = iota + 1
	DeliveryAuthorizationDenied
	DeliveryClaimLost
	DeliveryUnavailable
)

// DeliveryError never retains transport, persistence, or caller-supplied
// detail.
type DeliveryError struct {
	code DeliveryErrorCode
}

func (err *DeliveryError) Error() string {
	if err == nil {
		return "outbox delivery request is invalid"
	}
	switch err.code {
	case DeliveryAuthorizationDenied:
		return "outbox delivery authority is denied"
	case DeliveryClaimLost:
		return "outbox delivery claim is no longer owned"
	case DeliveryUnavailable:
		return "outbox delivery is unavailable"
	default:
		return "outbox delivery request is invalid"
	}
}

func (err *DeliveryError) Code() DeliveryErrorCode {
	if err == nil {
		return DeliveryInvalidRequest
	}
	return err.code
}

func newDeliveryError(code DeliveryErrorCode) *DeliveryError {
	return &DeliveryError{code: code}
}

type DeliveryLeaseFence uint64

type DeliveryClaimRequest struct {
	Authority WorkerAuthority
	Limit     uint32
}

type DeliveryHeartbeatRequest struct {
	Authority   WorkerAuthority
	OperationID OperationID
	LeaseFence  DeliveryLeaseFence
}

type DeliveryClaim struct {
	OperationID    OperationID
	Request        OwnedTransportRequest
	LeaseFence     DeliveryLeaseFence
	LeaseExpiresAt time.Time
}

type DeliveryClaimBatch struct {
	Claims []DeliveryClaim
}

type DeliveryDisposition uint8

const (
	DeliveryPending DeliveryDisposition = iota + 1
	DeliveryClaimed
	DeliveryAccepted
	DeliveryReconciliationRequired
	DeliveryBackpressured
	DeliverySuperseded
	DeliveryPoisoned
	DeliveryDeferred
	DeliveryIntegrityConflict
)

type DeliveryResult struct {
	OperationID    OperationID
	Disposition    DeliveryDisposition
	ResultDigest   DeliveryResultDigest
	DeliveryCount  uint32
	RetryAt        time.Time
	DeferralReason OwnedTransportDeferralReason
}

type DeliveryInspectionRequest struct {
	Authority   WorkerAuthority
	OperationID OperationID
}

type DeliveryReconcileRequest struct {
	Authority   WorkerAuthority
	OperationID OperationID
}

type DeliveryView struct {
	OperationID    OperationID
	Disposition    DeliveryDisposition
	ResultDigest   DeliveryResultDigest
	DeliveryCount  uint32
	Terminal       bool
	RetryAt        time.Time
	DeferralReason OwnedTransportDeferralReason
	LeaseFence     DeliveryLeaseFence
	LeaseExpiresAt time.Time
}

// OutboxDispatcher exposes delivery behavior without exposing the
// authoritative outbox representation or adapter-private claim state.
type OutboxDispatcher interface {
	Claim(context.Context, DeliveryClaimRequest) (DeliveryClaimBatch, error)
	Heartbeat(context.Context, DeliveryHeartbeatRequest) (DeliveryClaim, error)
	Deliver(context.Context, DeliveryClaim) (DeliveryResult, error)
	Inspect(context.Context, DeliveryInspectionRequest) (DeliveryView, error)
	Reconcile(context.Context, DeliveryReconcileRequest) (DeliveryResult, error)
}

type authoritativeOutboxRecord struct {
	EnactmentRef
	DecisionID    DecisionID
	TaskID        TaskID
	PhaseRunID    PhaseRunID
	RuntimeRunID  RuntimeRunID
	SafetyEpoch   SafetyEpoch
	Prerequisites DeliveryPrerequisites
	CommittedAt   time.Time
}

type memoryDeliveryState struct {
	Authority      authorityValue
	LeaseFence     DeliveryLeaseFence
	LeaseExpiresAt time.Time
	Disposition    DeliveryDisposition
	ResultDigest   DeliveryResultDigest
	DeliveryCount  uint32
	Terminal       bool
	ReconcileFence uint64
	RetryAt        time.Time
	DeferralReason OwnedTransportDeferralReason
}

type dispatcher struct {
	persistence *memoryPersistence
	config      DispatcherConfig
	transport   OwnedTransport
}

func (harness *DeterministicHarness) NewOutboxDispatcher(
	config DispatcherConfig,
	transport OwnedTransport,
) (OutboxDispatcher, error) {
	if harness == nil || harness.persistence == nil || transport == nil ||
		config.Now == nil || config.MaxBatchSize == 0 || config.LeaseDuration <= 0 ||
		config.TransportVersion != OwnedTransportV1 || len(config.Authorities) == 0 {
		return nil, newDeliveryError(DeliveryInvalidRequest)
	}
	for _, authority := range config.Authorities {
		if !validDeliveryAuthority(authority) {
			return nil, newDeliveryError(DeliveryInvalidRequest)
		}
	}
	config.Authorities = append([]WorkerAuthority(nil), config.Authorities...)
	return &dispatcher{persistence: harness.persistence, config: config, transport: transport}, nil
}

func (value *dispatcher) Claim(
	ctx context.Context,
	request DeliveryClaimRequest,
) (DeliveryClaimBatch, error) {
	if ctx == nil || ctx.Err() != nil || request.Limit == 0 ||
		!value.authorized(request.Authority) {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	limit := request.Limit
	if limit > value.config.MaxBatchSize {
		limit = value.config.MaxBatchSize
	}
	now := value.config.Now().UTC()
	if now.IsZero() {
		return DeliveryClaimBatch{}, newDeliveryError(DeliveryUnavailable)
	}

	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	operationIDs := make([]OperationID, 0, len(value.persistence.outbox))
	for operationID := range value.persistence.outbox {
		operationIDs = append(operationIDs, operationID)
	}
	sort.Slice(operationIDs, func(left, right int) bool {
		leftRecord := value.persistence.outbox[operationIDs[left]]
		rightRecord := value.persistence.outbox[operationIDs[right]]
		if leftRecord.CommittedAt.Equal(rightRecord.CommittedAt) {
			return operationIDs[left].value < operationIDs[right].value
		}
		return leftRecord.CommittedAt.Before(rightRecord.CommittedAt)
	})
	batch := DeliveryClaimBatch{Claims: make([]DeliveryClaim, 0, limit)}
	for _, operationID := range operationIDs {
		state := value.persistence.deliveries[operationID]
		record := value.persistence.outbox[operationID]
		task, taskExists := value.persistence.tasks[record.TaskID]
		if taskExists && record.SafetyEpoch < effectiveSafetyEpoch(task, value.persistence.recovery) {
			state.Disposition = DeliverySuperseded
			state.Terminal = true
			state.Authority = authorityValue{}
			state.LeaseExpiresAt = time.Time{}
			value.persistence.deliveries[operationID] = state
			continue
		}
		if state.Terminal || state.Disposition == DeliveryReconciliationRequired ||
			state.RetryAt.After(now) ||
			state.LeaseFence != 0 && state.LeaseExpiresAt.After(now) {
			continue
		}
		state.Authority = request.Authority.value
		state.LeaseFence++
		state.LeaseExpiresAt = now.Add(value.config.LeaseDuration)
		state.Disposition = DeliveryClaimed
		state.RetryAt = time.Time{}
		state.DeferralReason = 0
		value.persistence.deliveries[operationID] = state
		transportRequest := value.transportRequest(record, request.Authority)
		batch.Claims = append(batch.Claims, DeliveryClaim{
			OperationID: operationID, Request: transportRequest,
			LeaseFence: state.LeaseFence, LeaseExpiresAt: state.LeaseExpiresAt,
		})
		if uint32(len(batch.Claims)) == limit {
			break
		}
	}
	return batch, nil
}

func (value *dispatcher) Deliver(
	ctx context.Context,
	claim DeliveryClaim,
) (DeliveryResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	authority := claim.Request.Authority
	if !value.authorized(authority) || !validOpaqueID(claim.OperationID.value) ||
		claim.LeaseFence == 0 {
		return DeliveryResult{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	now := value.config.Now().UTC()
	if now.IsZero() {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	value.persistence.mu.Lock()
	state, exists := value.persistence.deliveries[claim.OperationID]
	record, committed := value.persistence.outbox[claim.OperationID]
	if !exists || !committed || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence || !state.LeaseExpiresAt.After(now) {
		value.persistence.mu.Unlock()
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	state.DeliveryCount++
	value.persistence.deliveries[claim.OperationID] = state
	request := value.transportRequest(record, authority)
	value.persistence.mu.Unlock()

	response, err := value.transport.Deliver(ctx, request)
	if err != nil {
		return value.markReconciliationRequired(claim, authority)
	}
	if response.Version != request.Version || response.OperationID != request.OperationID {
		return value.markReconciliationRequired(claim, authority)
	}
	if response.Outcome == OwnedTransportBackpressured {
		return value.markBackpressured(claim, authority, response.RetryAt)
	}
	if response.Outcome == OwnedTransportDeferred {
		return value.markDeferred(claim, authority, response.DeferralReason, response.RetryAt)
	}
	if response.Outcome == OwnedTransportSuperseded {
		return value.markTerminalDisposition(claim, authority, DeliverySuperseded)
	}
	if response.Outcome == OwnedTransportPoisoned ||
		response.Outcome == OwnedTransportUnsupportedVersion ||
		response.Outcome == OwnedTransportUnauthorized {
		return value.markTerminalDisposition(claim, authority, DeliveryPoisoned)
	}
	if response.Outcome == OwnedTransportIntegrityConflict {
		return value.markTerminalDisposition(claim, authority, DeliveryIntegrityConflict)
	}
	if response.Outcome != OwnedTransportAccepted ||
		response.ResultDigest == (DeliveryResultDigest{}) {
		return value.markReconciliationRequired(claim, authority)
	}

	finishedAt := value.config.Now().UTC()
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists = value.persistence.deliveries[claim.OperationID]
	if !exists || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence || !state.LeaseExpiresAt.After(finishedAt) {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	state.Disposition = DeliveryAccepted
	state.ResultDigest = response.ResultDigest
	state.Terminal = true
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
	value.persistence.deliveries[claim.OperationID] = state
	return DeliveryResult{
		OperationID: claim.OperationID, Disposition: state.Disposition,
		ResultDigest: state.ResultDigest, DeliveryCount: state.DeliveryCount,
	}, nil
}

func (value *dispatcher) markDeferred(
	claim DeliveryClaim,
	authority WorkerAuthority,
	reason OwnedTransportDeferralReason,
	retryAt time.Time,
) (DeliveryResult, error) {
	now := value.config.Now().UTC()
	if reason != OwnedTransportPrerequisiteDeferred || retryAt.Location() != time.UTC ||
		!retryAt.After(now) {
		return value.markReconciliationRequired(claim, authority)
	}
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists := value.persistence.deliveries[claim.OperationID]
	if !exists || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	state.Disposition = DeliveryDeferred
	state.DeferralReason = reason
	state.RetryAt = retryAt
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
	value.persistence.deliveries[claim.OperationID] = state
	return deliveryResultFromState(claim.OperationID, state), nil
}

func (value *dispatcher) markTerminalDisposition(
	claim DeliveryClaim,
	authority WorkerAuthority,
	disposition DeliveryDisposition,
) (DeliveryResult, error) {
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists := value.persistence.deliveries[claim.OperationID]
	if !exists || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	state.Disposition = disposition
	state.Terminal = true
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
	value.persistence.deliveries[claim.OperationID] = state
	return deliveryResultFromState(claim.OperationID, state), nil
}

func (value *dispatcher) markBackpressured(
	claim DeliveryClaim,
	authority WorkerAuthority,
	retryAt time.Time,
) (DeliveryResult, error) {
	now := value.config.Now().UTC()
	if retryAt.Location() != time.UTC || !retryAt.After(now) {
		return value.markReconciliationRequired(claim, authority)
	}
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists := value.persistence.deliveries[claim.OperationID]
	if !exists || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	state.Disposition = DeliveryBackpressured
	state.DeferralReason = 0
	state.RetryAt = retryAt
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
	value.persistence.deliveries[claim.OperationID] = state
	return deliveryResultFromState(claim.OperationID, state), nil
}

func (value *dispatcher) markReconciliationRequired(
	claim DeliveryClaim,
	authority WorkerAuthority,
) (DeliveryResult, error) {
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists := value.persistence.deliveries[claim.OperationID]
	if !exists || state.Terminal || state.Authority != authority.value ||
		state.LeaseFence != claim.LeaseFence {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	state.Disposition = DeliveryReconciliationRequired
	state.Authority = authorityValue{}
	state.LeaseExpiresAt = time.Time{}
	value.persistence.deliveries[claim.OperationID] = state
	return deliveryResultFromState(claim.OperationID, state), nil
}

func (value *dispatcher) Inspect(
	ctx context.Context,
	request DeliveryInspectionRequest,
) (DeliveryView, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryView{}, newDeliveryError(DeliveryUnavailable)
	}
	if !value.authorized(request.Authority) || !validOpaqueID(request.OperationID.value) {
		return DeliveryView{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	if _, committed := value.persistence.outbox[request.OperationID]; !committed {
		return DeliveryView{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	state, exists := value.persistence.deliveries[request.OperationID]
	if !exists {
		return DeliveryView{OperationID: request.OperationID, Disposition: DeliveryPending}, nil
	}
	return DeliveryView{
		OperationID: request.OperationID, Disposition: state.Disposition,
		ResultDigest: state.ResultDigest, DeliveryCount: state.DeliveryCount,
		Terminal: state.Terminal, RetryAt: state.RetryAt,
		DeferralReason: state.DeferralReason, LeaseFence: state.LeaseFence,
		LeaseExpiresAt: state.LeaseExpiresAt,
	}, nil
}

func (value *dispatcher) Reconcile(
	ctx context.Context,
	request DeliveryReconcileRequest,
) (DeliveryResult, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryResult{}, newDeliveryError(DeliveryUnavailable)
	}
	if !value.authorized(request.Authority) || !validOpaqueID(request.OperationID.value) {
		return DeliveryResult{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	value.persistence.mu.Lock()
	state, exists := value.persistence.deliveries[request.OperationID]
	_, committed := value.persistence.outbox[request.OperationID]
	if !exists || !committed || state.Disposition != DeliveryReconciliationRequired || state.Terminal {
		value.persistence.mu.Unlock()
		return DeliveryResult{}, newDeliveryError(DeliveryInvalidRequest)
	}
	state.ReconcileFence++
	reconcileFence := state.ReconcileFence
	value.persistence.deliveries[request.OperationID] = state
	value.persistence.mu.Unlock()

	response, err := value.transport.Inspect(ctx, OwnedTransportInspection{
		Version: value.config.TransportVersion, Authority: request.Authority,
		OperationID: request.OperationID,
	})
	if err != nil {
		return deliveryResultFromState(request.OperationID, state), nil
	}
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists = value.persistence.deliveries[request.OperationID]
	if !exists || state.Terminal || state.Disposition != DeliveryReconciliationRequired ||
		state.ReconcileFence != reconcileFence {
		return DeliveryResult{}, newDeliveryError(DeliveryClaimLost)
	}
	if response.Version != value.config.TransportVersion || response.OperationID != request.OperationID {
		return deliveryResultFromState(request.OperationID, state), nil
	}
	switch response.Outcome {
	case OwnedTransportAccepted:
		if response.ResultDigest == (DeliveryResultDigest{}) {
			return deliveryResultFromState(request.OperationID, state), nil
		}
		state.Disposition = DeliveryAccepted
		state.ResultDigest = response.ResultDigest
		state.Terminal = true
	case OwnedTransportUnknown:
		state.Disposition = DeliveryPending
	case OwnedTransportSuperseded:
		state.Disposition = DeliverySuperseded
		state.Terminal = true
	case OwnedTransportIntegrityConflict:
		state.Disposition = DeliveryIntegrityConflict
		state.Terminal = true
	case OwnedTransportPoisoned, OwnedTransportUnsupportedVersion, OwnedTransportUnauthorized:
		state.Disposition = DeliveryPoisoned
		state.Terminal = true
	case OwnedTransportBackpressured:
		if response.RetryAt.Location() != time.UTC || !response.RetryAt.After(value.config.Now().UTC()) {
			return deliveryResultFromState(request.OperationID, state), nil
		}
		state.Disposition = DeliveryBackpressured
		state.RetryAt = response.RetryAt
	case OwnedTransportDeferred:
		if response.DeferralReason != OwnedTransportPrerequisiteDeferred ||
			response.RetryAt.Location() != time.UTC || !response.RetryAt.After(value.config.Now().UTC()) {
			return deliveryResultFromState(request.OperationID, state), nil
		}
		state.Disposition = DeliveryDeferred
		state.DeferralReason = response.DeferralReason
		state.RetryAt = response.RetryAt
	default:
		return deliveryResultFromState(request.OperationID, state), nil
	}
	value.persistence.deliveries[request.OperationID] = state
	return deliveryResultFromState(request.OperationID, state), nil
}

func deliveryResultFromState(operationID OperationID, state memoryDeliveryState) DeliveryResult {
	return DeliveryResult{
		OperationID: operationID, Disposition: state.Disposition,
		ResultDigest: state.ResultDigest, DeliveryCount: state.DeliveryCount,
		RetryAt: state.RetryAt, DeferralReason: state.DeferralReason,
	}
}

func (value *dispatcher) Heartbeat(
	ctx context.Context,
	request DeliveryHeartbeatRequest,
) (DeliveryClaim, error) {
	if ctx == nil || ctx.Err() != nil {
		return DeliveryClaim{}, newDeliveryError(DeliveryUnavailable)
	}
	if !value.authorized(request.Authority) ||
		!validOpaqueID(request.OperationID.value) || request.LeaseFence == 0 {
		return DeliveryClaim{}, newDeliveryError(DeliveryAuthorizationDenied)
	}
	now := value.config.Now().UTC()
	if now.IsZero() {
		return DeliveryClaim{}, newDeliveryError(DeliveryUnavailable)
	}
	value.persistence.mu.Lock()
	defer value.persistence.mu.Unlock()
	state, exists := value.persistence.deliveries[request.OperationID]
	record, committed := value.persistence.outbox[request.OperationID]
	if !exists || !committed || state.Authority != request.Authority.value ||
		state.LeaseFence != request.LeaseFence || !state.LeaseExpiresAt.After(now) {
		return DeliveryClaim{}, newDeliveryError(DeliveryClaimLost)
	}
	state.LeaseExpiresAt = now.Add(value.config.LeaseDuration)
	value.persistence.deliveries[request.OperationID] = state
	return DeliveryClaim{
		OperationID:    request.OperationID,
		Request:        value.transportRequest(record, request.Authority),
		LeaseFence:     state.LeaseFence,
		LeaseExpiresAt: state.LeaseExpiresAt,
	}, nil
}

func (value *dispatcher) authorized(authority WorkerAuthority) bool {
	if !validDeliveryAuthority(authority) {
		return false
	}
	for _, allowed := range value.config.Authorities {
		if allowed.value == authority.value {
			return true
		}
	}
	return false
}

func (value *dispatcher) transportRequest(
	record authoritativeOutboxRecord,
	authority WorkerAuthority,
) OwnedTransportRequest {
	fenceKind, fence := postgresFenceValue(record.Fence)
	return OwnedTransportRequest{
		Version: value.config.TransportVersion, Authority: authority,
		OperationID: record.OperationID, DecisionID: record.DecisionID, TaskID: record.TaskID,
		PhaseRunID: record.PhaseRunID, RuntimeRunID: record.RuntimeRunID, Kind: record.Kind,
		PayloadDigest: record.PayloadDigest, ActivityGeneration: record.ActivityGeneration,
		SafetyEpoch: record.SafetyEpoch,
		FenceKind:   fenceKind, Fence: fence, CausationID: record.CausationID,
		Prerequisites: DeliveryPrerequisites{
			TaskRevision:        record.Prerequisites.TaskRevision,
			AcceptedEvidenceIDs: cloneEvidenceIDs(record.Prerequisites.AcceptedEvidenceIDs),
		},
	}
}

func cloneEvidenceIDs(values []EvidenceID) []EvidenceID {
	cloned := make([]EvidenceID, len(values))
	copy(cloned, values)
	return cloned
}

func validDeliveryAuthority(authority WorkerAuthority) bool {
	return authority.value.valid() && authority.value.kind == AuthorityWorker
}

func evidenceIDValues(refs []EvidenceRef) []EvidenceID {
	values := make([]EvidenceID, 0, len(refs))
	for _, ref := range refs {
		values = append(values, ref.ID)
	}
	return values
}
