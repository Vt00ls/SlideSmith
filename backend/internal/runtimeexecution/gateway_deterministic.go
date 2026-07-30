package runtimeexecution

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DeterministicGateway is the fault-controllable ProviderAccess adapter used
// by the Gateway contract suite. It owns grant activation; Runtime Execution
// only persists the returned typed decision.
type DeterministicGateway struct {
	mu               sync.Mutex
	now              func() time.Time
	operations       map[OperationID]GatewayGrantDecision
	current          map[RuntimeRunID]GatewayGrant
	calls            map[GatewayCallID]GatewayCallDecision
	attempts         map[GatewayAttemptID]RuntimeRunID
	receipts         map[UsageReceiptID]UsageReceiptReference
	receiptDigests   map[UsageReceiptID]Digest
	runtimeAuthority GatewayCallAuthorityValidator
	nextGrant        uint64
	nextAttempt      uint64
	loseNextResponse bool
}

func NewDeterministicGateway(now func() time.Time) (*DeterministicGateway, error) {
	if now == nil || now().IsZero() {
		return nil, newError(ErrorInvalidRequest)
	}
	return &DeterministicGateway{
		now: now, operations: make(map[OperationID]GatewayGrantDecision),
		current: make(map[RuntimeRunID]GatewayGrant), calls: make(map[GatewayCallID]GatewayCallDecision),
		attempts: make(map[GatewayAttemptID]RuntimeRunID), receipts: make(map[UsageReceiptID]UsageReceiptReference),
		receiptDigests: make(map[UsageReceiptID]Digest), nextGrant: 1, nextAttempt: 1,
	}, nil
}

func (gateway *DeterministicGateway) BindRuntimeAuthority(validator GatewayCallAuthorityValidator) error {
	if validator == nil {
		return newError(ErrorInvalidRequest)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.runtimeAuthority != nil {
		return newError(ErrorIntegrityConflict)
	}
	gateway.runtimeAuthority = validator
	return nil
}

func (gateway *DeterministicGateway) LoseNextGrantResponse() {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	gateway.loseNextResponse = true
}

func (gateway *DeterministicGateway) DecideGatewayGrant(
	ctx context.Context,
	request GatewayGrantRequest,
) (GatewayGrantDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
	}
	now := gateway.now().UTC()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if retained, exists := gateway.operations[request.OperationID]; exists {
		if retained.CanonicalRequestDigest != request.CanonicalRequestDigest {
			return GatewayGrantDecision{}, newError(ErrorIntegrityConflict)
		}
		return retained, nil
	}
	if !validGatewayGrantRequest(request, now) {
		return GatewayGrantDecision{}, newError(ErrorIntegrityConflict)
	}
	current := gateway.current[request.RuntimeRunID]
	if request.Kind == GatewayGrantInitial {
		if current != (GatewayGrant{}) || request.RequestedGeneration != 1 {
			return GatewayGrantDecision{}, newError(ErrorIntegrityConflict)
		}
	} else {
		if current == (GatewayGrant{}) || current.GatewayGrantID != request.PreviousGrantID ||
			current.Generation != request.PreviousGeneration || request.RequestedGeneration != current.Generation+1 ||
			current.PersonalWorkspaceID != request.PersonalWorkspaceID || current.TaskID != request.TaskID ||
			current.PhaseRunID != request.PhaseRunID ||
			current.RuntimeRunID != request.RuntimeRunID || current.StartOperationID != request.StartOperationID ||
			current.RuntimeBindingID != request.RuntimeBindingID ||
			current.RuntimeBindingDigest != request.RuntimeBindingDigest ||
			current.ReleaseSafetyEpoch != request.ReleaseSafetyEpoch ||
			current.LeaseID != request.LeaseID || current.LeaseGeneration != request.LeaseGeneration ||
			current.LeaseFence != request.LeaseFence || current.RuntimeFence != request.RuntimeFence ||
			current.QuotaReservationID != request.QuotaReservationID ||
			current.QuotaReservationGeneration != request.QuotaReservationGeneration ||
			current.QuotaReservationMode != request.QuotaReservationMode ||
			current.OwnerAuthorityGeneration != request.OwnerAuthorityGeneration ||
			current.AuthorizationGeneration != request.AuthorizationGeneration ||
			current.GatewayRoutePolicyID != request.GatewayRoutePolicyID ||
			current.GatewayRoutePolicyGeneration != request.GatewayRoutePolicyGeneration ||
			request.RecoveryGeneration < current.RecoveryGeneration ||
			request.RecoveryGeneration == current.RecoveryGeneration &&
				request.RecoveryExpiresAt.After(current.RecoveryExpiresAt) ||
			request.CapabilityScope == 0 || request.CapabilityScope&^current.CapabilityScope != 0 {
			return GatewayGrantDecision{}, newError(ErrorIntegrityConflict)
		}
	}
	grant, err := NewGatewayGrant(gatewayGrantInputForRequest(
		GatewayGrantID{value: fmt.Sprintf("gateway-grant-issued-%06d", gateway.nextGrant)}, request,
	))
	if err != nil {
		return GatewayGrantDecision{}, err
	}
	gateway.nextGrant++
	decision := GatewayGrantDecision{
		OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
		Disposition: GatewayGrantDecisionAccepted, Grant: grant,
	}
	// Operation retention and current-generation activation are one CAS
	// linearization point in this deterministic adapter.
	gateway.operations[request.OperationID] = decision
	gateway.current[request.RuntimeRunID] = grant
	if gateway.loseNextResponse {
		gateway.loseNextResponse = false
		return GatewayGrantDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func (gateway *DeterministicGateway) InspectGatewayGrant(
	ctx context.Context,
	ref GatewayGrantOperationRef,
) (GatewayGrantDecision, error) {
	if ctx == nil || ctx.Err() != nil || !validOpaqueID(ref.OperationID.String()) ||
		ref.CanonicalRequestDigest == (Digest{}) {
		return GatewayGrantDecision{}, newError(ErrorInvalidRequest)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	decision, exists := gateway.operations[ref.OperationID]
	if !exists {
		return GatewayGrantDecision{}, newError(ErrorDependencyUnavailable)
	}
	if decision.CanonicalRequestDigest != ref.CanonicalRequestDigest {
		return GatewayGrantDecision{}, newError(ErrorIntegrityConflict)
	}
	return decision, nil
}

var _ GatewayGrantAdapter = (*DeterministicGateway)(nil)

func (gateway *DeterministicGateway) AcceptGatewayCall(
	ctx context.Context,
	request GatewayCallRequest,
) (GatewayCallDecision, error) {
	if ctx == nil || ctx.Err() != nil || !validGatewayCallRequest(request) ||
		request.CanonicalRequestDigest != canonicalGatewayCallDigest(request) {
		return GatewayCallDecision{}, newError(ErrorInvalidRequest)
	}
	gateway.mu.Lock()
	if retained, exists := gateway.calls[request.GatewayCallID]; exists {
		gateway.mu.Unlock()
		if retained.CanonicalRequestDigest != request.CanonicalRequestDigest {
			return GatewayCallDecision{}, newError(ErrorIntegrityConflict)
		}
		return retained, nil
	}
	current := gateway.current[request.RuntimeRunID]
	validator := gateway.runtimeAuthority
	now := gateway.now().UTC()
	if current == (GatewayGrant{}) || validator == nil || current.GatewayGrantID != request.GatewayGrantID ||
		current.Generation != request.GatewayGrantGeneration || current.StartOperationID != request.StartOperationID ||
		current.LeaseID != request.LeaseID || current.LeaseGeneration != request.LeaseGeneration ||
		current.LeaseFence != request.LeaseFence || current.RuntimeFence != request.RuntimeFence ||
		current.QuotaReservationID != request.QuotaReservationID ||
		current.QuotaReservationGeneration != request.QuotaReservationGeneration ||
		current.QuotaReservationMode != request.QuotaReservationMode ||
		current.GatewayRoutePolicyID != request.GatewayRoutePolicyID ||
		current.GatewayRoutePolicyGeneration != request.GatewayRoutePolicyGeneration ||
		request.Capability&^current.CapabilityScope != 0 || !current.ExpiresAt.After(now) {
		gateway.mu.Unlock()
		return GatewayCallDecision{}, newError(ErrorIntegrityConflict)
	}
	authorityFact := GatewayCallAuthorityFact{
		PersonalWorkspaceID: current.PersonalWorkspaceID, TaskID: current.TaskID, PhaseRunID: current.PhaseRunID,
		RuntimeRunID: request.RuntimeRunID, StartOperationID: request.StartOperationID,
		RuntimeBindingID: current.RuntimeBindingID, RuntimeBindingDigest: current.RuntimeBindingDigest,
		ReleaseSafetyEpoch: current.ReleaseSafetyEpoch,
		GatewayGrantID:     request.GatewayGrantID, GatewayGrantGeneration: request.GatewayGrantGeneration,
		LeaseID: request.LeaseID, LeaseGeneration: request.LeaseGeneration, LeaseFence: request.LeaseFence,
		RuntimeFence: request.RuntimeFence, QuotaReservationID: request.QuotaReservationID,
		QuotaReservationGeneration:   request.QuotaReservationGeneration,
		QuotaReservationMode:         request.QuotaReservationMode,
		OwnerAuthorityGeneration:     current.OwnerAuthorityGeneration,
		AuthorizationGeneration:      current.AuthorizationGeneration,
		GatewayRoutePolicyID:         request.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: request.GatewayRoutePolicyGeneration,
		Capability:                   request.Capability, RecoveryGeneration: current.RecoveryGeneration,
		RecoveryMode: current.RecoveryMode, GrantExpiresAt: current.ExpiresAt, ValidAt: now,
	}
	if !gatewayGrantAuthorizesCall(current, authorityFact, now) {
		gateway.mu.Unlock()
		return GatewayCallDecision{}, newError(ErrorIntegrityConflict)
	}
	gateway.mu.Unlock()
	var decision GatewayCallDecision
	committed := false
	accept := func() error {
		gateway.mu.Lock()
		defer gateway.mu.Unlock()
		current = gateway.current[request.RuntimeRunID]
		now = gateway.now().UTC()
		if !gatewayGrantAuthorizesCall(current, authorityFact, now) {
			return newError(ErrorIntegrityConflict)
		}
		if retained, exists := gateway.calls[request.GatewayCallID]; exists {
			if retained.CanonicalRequestDigest != request.CanonicalRequestDigest {
				return newError(ErrorIntegrityConflict)
			}
			decision = retained
			committed = true
			return nil
		}
		attemptID := GatewayAttemptID{value: fmt.Sprintf("gateway-attempt-%06d", gateway.nextAttempt)}
		gateway.nextAttempt++
		decision = GatewayCallDecision{
			GatewayCallID: request.GatewayCallID, CanonicalRequestDigest: request.CanonicalRequestDigest,
			Disposition: GatewayCallAccepted, GatewayAttemptID: attemptID,
			GrantGeneration: request.GatewayGrantGeneration,
		}
		gateway.calls[request.GatewayCallID] = decision
		gateway.attempts[attemptID] = request.RuntimeRunID
		committed = true
		return nil
	}
	if err := validator.ValidateGatewayCall(ctx, authorityFact, accept); err != nil {
		return GatewayCallDecision{}, err
	}
	if !committed {
		return GatewayCallDecision{}, newError(ErrorIntegrityConflict)
	}
	return decision, nil
}

func (gateway *DeterministicGateway) SettleGatewayAttempt(
	ctx context.Context,
	settlement GatewayAttemptSettlement,
) (UsageReceiptReference, error) {
	if ctx == nil || ctx.Err() != nil || !validGatewayAttemptSettlement(settlement) ||
		settlement.CanonicalDigest != canonicalGatewayAttemptSettlementDigest(settlement) {
		return UsageReceiptReference{}, newError(ErrorInvalidRequest)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if retained, exists := gateway.receipts[settlement.UsageReceiptID]; exists {
		if gateway.receiptDigests[settlement.UsageReceiptID] != settlement.CanonicalDigest {
			return UsageReceiptReference{}, newError(ErrorIntegrityConflict)
		}
		return retained, nil
	}
	if _, accepted := gateway.attempts[settlement.GatewayAttemptID]; !accepted {
		return UsageReceiptReference{}, newError(ErrorIntegrityConflict)
	}
	reference := UsageReceiptReference{
		UsageReceiptID: settlement.UsageReceiptID, GatewayAttemptID: settlement.GatewayAttemptID,
		Disposition: settlement.Disposition, CanonicalDigest: settlement.CanonicalDigest,
	}
	gateway.receipts[settlement.UsageReceiptID] = reference
	gateway.receiptDigests[settlement.UsageReceiptID] = settlement.CanonicalDigest
	return reference, nil
}

var _ GatewayCallAccess = (*DeterministicGateway)(nil)

func (gateway *DeterministicGateway) QueryUsageReceiptEvidence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
) (UsageReceiptEvidence, error) {
	if ctx == nil || ctx.Err() != nil || !validOpaqueID(runtimeRunID.String()) {
		return UsageReceiptEvidence{}, newError(ErrorInvalidRequest)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	references := make([]UsageReceiptReference, 0)
	for _, reference := range gateway.receipts {
		if gateway.attempts[reference.GatewayAttemptID] == runtimeRunID {
			references = append(references, reference)
		}
	}
	sort.Slice(references, func(left, right int) bool {
		return references[left].UsageReceiptID.String() < references[right].UsageReceiptID.String()
	})
	if len(references) == 0 {
		return UsageReceiptEvidence{Disposition: UsageEvidenceMissing}, nil
	}
	disposition := UsageEvidenceKnown
	allProvenNoSend := true
	for _, reference := range references {
		switch reference.Disposition {
		case UsageEvidenceUnknown:
			disposition = UsageEvidenceUnknown
		case UsageEvidenceEstimated:
			if disposition != UsageEvidenceUnknown {
				disposition = UsageEvidenceEstimated
			}
		}
		if reference.Disposition != UsageEvidenceProvenNoSend {
			allProvenNoSend = false
		}
	}
	if allProvenNoSend {
		disposition = UsageEvidenceProvenNoSend
	}
	return UsageReceiptEvidence{Disposition: disposition, References: references}, nil
}

var _ UsageReceiptEvidenceSource = (*DeterministicGateway)(nil)
