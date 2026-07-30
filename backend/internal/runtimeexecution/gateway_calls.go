package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

type GatewayCallID struct{ value string }
type GatewayAttemptID struct{ value string }
type UsageReceiptID struct{ value string }

func NewGatewayCallID(value string) (GatewayCallID, error) {
	value, err := newOpaqueID(value)
	return GatewayCallID{value: value}, err
}

func NewGatewayAttemptID(value string) (GatewayAttemptID, error) {
	value, err := newOpaqueID(value)
	return GatewayAttemptID{value: value}, err
}

func NewUsageReceiptID(value string) (UsageReceiptID, error) {
	value, err := newOpaqueID(value)
	return UsageReceiptID{value: value}, err
}

func (id GatewayCallID) String() string    { return id.value }
func (id GatewayAttemptID) String() string { return id.value }
func (id UsageReceiptID) String() string   { return id.value }

type GatewayCallRequestInput struct {
	GatewayCallID                GatewayCallID
	RuntimeRunID                 RuntimeRunID
	StartOperationID             OperationID
	GatewayGrantID               GatewayGrantID
	GatewayGrantGeneration       GatewayGrantGeneration
	LeaseID                      SandboxLeaseID
	LeaseGeneration              LeaseGeneration
	LeaseFence                   LeaseFence
	RuntimeFence                 RuntimeFence
	QuotaReservationID           QuotaReservationID
	QuotaReservationGeneration   QuotaReservationGeneration
	QuotaReservationMode         QuotaReservationMode
	GatewayRoutePolicyID         GatewayRoutePolicyID
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration
	Capability                   ProviderCapabilityScope
	AcceptedAt                   time.Time
}

type GatewayCallRequest struct {
	GatewayCallRequestInput
	CanonicalRequestDigest Digest
}

func NewGatewayCallRequest(input GatewayCallRequestInput) (GatewayCallRequest, error) {
	input.AcceptedAt = input.AcceptedAt.UTC()
	request := GatewayCallRequest{GatewayCallRequestInput: input}
	if !validGatewayCallRequest(request) {
		return GatewayCallRequest{}, newError(ErrorInvalidRequest)
	}
	request.CanonicalRequestDigest = canonicalGatewayCallDigest(request)
	return request, nil
}

type GatewayCallDisposition uint8

const (
	GatewayCallAccepted GatewayCallDisposition = iota + 1
	GatewayCallRejected
)

type GatewayCallDecision struct {
	GatewayCallID          GatewayCallID
	CanonicalRequestDigest Digest
	Disposition            GatewayCallDisposition
	GatewayAttemptID       GatewayAttemptID
	GrantGeneration        GatewayGrantGeneration
}

type GatewayCallAuthorityFact struct {
	PersonalWorkspaceID          PersonalWorkspaceID
	TaskID                       TaskID
	PhaseRunID                   PhaseRunID
	RuntimeRunID                 RuntimeRunID
	StartOperationID             OperationID
	RuntimeBindingID             RuntimeBindingID
	RuntimeBindingDigest         Digest
	ReleaseSafetyEpoch           ReleaseSafetyEpoch
	GatewayGrantID               GatewayGrantID
	GatewayGrantGeneration       GatewayGrantGeneration
	LeaseID                      SandboxLeaseID
	LeaseGeneration              LeaseGeneration
	LeaseFence                   LeaseFence
	RuntimeFence                 RuntimeFence
	QuotaReservationID           QuotaReservationID
	QuotaReservationGeneration   QuotaReservationGeneration
	QuotaReservationMode         QuotaReservationMode
	OwnerAuthorityGeneration     AuthorizationGeneration
	AuthorizationGeneration      AuthorizationGeneration
	GatewayRoutePolicyID         GatewayRoutePolicyID
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration
	Capability                   ProviderCapabilityScope
	RecoveryGeneration           GatewayRecoveryGeneration
	RecoveryMode                 GatewayRecoveryMode
	GrantExpiresAt               time.Time
	ValidAt                      time.Time
}

func gatewayGrantAuthorizesCall(grant GatewayGrant, fact GatewayCallAuthorityFact, now time.Time) bool {
	return validGatewayGrant(grant) && grant.CanonicalDigest == canonicalGatewayGrantDigest(grant) &&
		grant.PersonalWorkspaceID == fact.PersonalWorkspaceID && grant.TaskID == fact.TaskID &&
		grant.PhaseRunID == fact.PhaseRunID && grant.RuntimeRunID == fact.RuntimeRunID &&
		grant.StartOperationID == fact.StartOperationID && grant.RuntimeBindingID == fact.RuntimeBindingID &&
		grant.RuntimeBindingDigest == fact.RuntimeBindingDigest &&
		grant.ReleaseSafetyEpoch == fact.ReleaseSafetyEpoch &&
		grant.GatewayGrantID == fact.GatewayGrantID && grant.Generation == fact.GatewayGrantGeneration &&
		grant.LeaseID == fact.LeaseID && grant.LeaseGeneration == fact.LeaseGeneration &&
		grant.LeaseFence == fact.LeaseFence && grant.RuntimeFence == fact.RuntimeFence &&
		grant.QuotaReservationID == fact.QuotaReservationID &&
		grant.QuotaReservationGeneration == fact.QuotaReservationGeneration &&
		grant.QuotaReservationMode == fact.QuotaReservationMode &&
		grant.OwnerAuthorityGeneration == fact.OwnerAuthorityGeneration &&
		grant.AuthorizationGeneration == fact.AuthorizationGeneration &&
		grant.GatewayRoutePolicyID == fact.GatewayRoutePolicyID &&
		grant.GatewayRoutePolicyGeneration == fact.GatewayRoutePolicyGeneration &&
		grant.RecoveryGeneration == fact.RecoveryGeneration && grant.RecoveryMode == fact.RecoveryMode &&
		grant.ExpiresAt.Equal(fact.GrantExpiresAt) &&
		fact.Capability != 0 && fact.Capability&^grant.CapabilityScope == 0 &&
		grant.ExpiresAt.After(now.UTC())
}

// GatewayCallExternalAuthorityFact binds the current recovery and independent
// route-policy authority that must remain fenced through Call acceptance.
type GatewayCallExternalAuthorityFact struct {
	RecoveryGeneration           GatewayRecoveryGeneration
	RecoveryMode                 GatewayRecoveryMode
	GatewayRoutePolicyID         GatewayRoutePolicyID
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration
	CapabilityScope              ProviderCapabilityScope
	GrantExpiresAt               time.Time
	ValidAt                      time.Time
}

func validGatewayCallExternalAuthorityFact(fact GatewayCallExternalAuthorityFact) bool {
	return fact.RecoveryGeneration > 0 && fact.RecoveryMode == GatewayRecoveryWritable &&
		validOpaqueID(fact.GatewayRoutePolicyID.String()) && fact.GatewayRoutePolicyGeneration > 0 &&
		fact.CapabilityScope != 0 && fact.CapabilityScope&^knownProviderCapabilityScope == 0 &&
		fact.GrantExpiresAt.After(fact.ValidAt.UTC())
}

// GatewayCallExternalAuthority must validate current recovery and route policy
// while holding their mutation fences until accept returns. It owns neither
// the Runtime/Reservation transaction nor the Gateway acceptance itself.
type GatewayCallExternalAuthority interface {
	ValidateGatewayCallExternalAuthority(
		context.Context,
		GatewayCallExternalAuthorityFact,
		GatewayCallAcceptance,
	) error
}

type GatewayCallExternalAuthorityFunc func(
	context.Context,
	GatewayCallExternalAuthorityFact,
	GatewayCallAcceptance,
) error

func (function GatewayCallExternalAuthorityFunc) ValidateGatewayCallExternalAuthority(
	ctx context.Context,
	fact GatewayCallExternalAuthorityFact,
	accept GatewayCallAcceptance,
) error {
	return function(ctx, fact, accept)
}

func validateGatewayCallExternalAuthority(
	ctx context.Context,
	authority GatewayCallExternalAuthority,
	fact GatewayCallExternalAuthorityFact,
	accept GatewayCallAcceptance,
) error {
	if ctx == nil || ctx.Err() != nil || authority == nil || accept == nil ||
		!validGatewayCallExternalAuthorityFact(fact) {
		return newError(ErrorIntegrityConflict)
	}
	const (
		externalAuthorityCallbackOpen uint32 = iota
		externalAuthorityCallbackCalled
		externalAuthorityCallbackClosed
	)
	var callbackState atomic.Uint32
	guarded := func() error {
		if !callbackState.CompareAndSwap(
			externalAuthorityCallbackOpen,
			externalAuthorityCallbackCalled,
		) {
			return newError(ErrorIntegrityConflict)
		}
		return accept()
	}
	err := authority.ValidateGatewayCallExternalAuthority(ctx, fact, guarded)
	if callbackState.CompareAndSwap(
		externalAuthorityCallbackOpen,
		externalAuthorityCallbackClosed,
	) {
		return newError(ErrorIntegrityConflict)
	}
	if err != nil || callbackState.Load() != externalAuthorityCallbackCalled {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func gatewayCallExternalAuthorityFact(
	grant GatewayGrant,
	now time.Time,
) GatewayCallExternalAuthorityFact {
	return GatewayCallExternalAuthorityFact{
		RecoveryGeneration: grant.RecoveryGeneration, RecoveryMode: grant.RecoveryMode,
		GatewayRoutePolicyID:         grant.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: grant.GatewayRoutePolicyGeneration,
		CapabilityScope:              grant.CapabilityScope, GrantExpiresAt: grant.ExpiresAt, ValidAt: now.UTC(),
	}
}

// GatewayCallAcceptance is the Gateway-owned Call acceptance linearization
// point. C03 may invoke it only while the validated Runtime authority remains
// fenced against concurrent mutation.
type GatewayCallAcceptance func() error

// GatewayCallAuthorityValidator is a C03 authority-ordering seam. It cannot
// create Calls, grant a lease, change a Reservation, or settle usage; the
// supplied acceptance remains owned and executed by the Gateway.
type GatewayCallAuthorityValidator interface {
	ValidateGatewayCall(context.Context, GatewayCallAuthorityFact, GatewayCallAcceptance) error
}

type GatewayCallAccess interface {
	AcceptGatewayCall(context.Context, GatewayCallRequest) (GatewayCallDecision, error)
	SettleGatewayAttempt(context.Context, GatewayAttemptSettlement) (UsageReceiptReference, error)
}

type UsageEvidenceDisposition uint8

const (
	UsageEvidenceKnown UsageEvidenceDisposition = iota + 1
	UsageEvidenceUnknown
	UsageEvidenceMissing
	UsageEvidenceEstimated
	UsageEvidenceNotApplicable
	UsageEvidenceProvenNoSend
)

type GatewayAttemptSettlementInput struct {
	GatewayAttemptID GatewayAttemptID
	UsageReceiptID   UsageReceiptID
	Disposition      UsageEvidenceDisposition
	ObservedAt       time.Time
}

type GatewayAttemptSettlement struct {
	GatewayAttemptSettlementInput
	CanonicalDigest Digest
}

func NewGatewayAttemptSettlement(input GatewayAttemptSettlementInput) (GatewayAttemptSettlement, error) {
	input.ObservedAt = input.ObservedAt.UTC()
	settlement := GatewayAttemptSettlement{GatewayAttemptSettlementInput: input}
	if !validGatewayAttemptSettlement(settlement) {
		return GatewayAttemptSettlement{}, newError(ErrorInvalidRequest)
	}
	settlement.CanonicalDigest = canonicalGatewayAttemptSettlementDigest(settlement)
	return settlement, nil
}

type UsageReceiptReference struct {
	UsageReceiptID   UsageReceiptID
	GatewayAttemptID GatewayAttemptID
	Disposition      UsageEvidenceDisposition
	CanonicalDigest  Digest
}

type UsageReceiptEvidence struct {
	Disposition UsageEvidenceDisposition
	References  []UsageReceiptReference
}

type UsageReceiptEvidenceSource interface {
	QueryUsageReceiptEvidence(context.Context, RuntimeRunID) (UsageReceiptEvidence, error)
}

type UsageReceiptReferenceSet struct {
	Count      uint64
	RootDigest Digest
}

func NewUsageReceiptReferenceSet(references []UsageReceiptReference) (UsageReceiptReferenceSet, error) {
	ordered := append([]UsageReceiptReference(nil), references...)
	for _, reference := range ordered {
		if !validUsageReceiptReference(reference) {
			return UsageReceiptReferenceSet{}, newError(ErrorIntegrityConflict)
		}
	}
	if len(ordered) == 0 {
		return UsageReceiptReferenceSet{}, nil
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].UsageReceiptID.String() < ordered[right].UsageReceiptID.String()
	})
	hasher := sha256.New()
	_, _ = fmt.Fprintln(hasher, "slidesmith.usage-receipt-reference-set/v1")
	for index, reference := range ordered {
		if index > 0 && ordered[index-1].UsageReceiptID == reference.UsageReceiptID {
			return UsageReceiptReferenceSet{}, newError(ErrorIntegrityConflict)
		}
		_, _ = fmt.Fprintf(hasher, "%s\n%s\n%d\n%x\n", reference.UsageReceiptID.String(),
			reference.GatewayAttemptID.String(), reference.Disposition, reference.CanonicalDigest)
	}
	var root Digest
	copy(root[:], hasher.Sum(nil))
	return UsageReceiptReferenceSet{Count: uint64(len(ordered)), RootDigest: root}, nil
}

type RuntimeUsageEvidenceSnapshot struct {
	Disposition UsageEvidenceDisposition
	Receipts    UsageReceiptReferenceSet
}

func validGatewayCallRequest(request GatewayCallRequest) bool {
	return validOpaqueID(request.GatewayCallID.String()) && validOpaqueID(request.RuntimeRunID.String()) &&
		validOpaqueID(request.StartOperationID.String()) && validOpaqueID(request.GatewayGrantID.String()) &&
		request.GatewayGrantGeneration > 0 && validOpaqueID(request.LeaseID.String()) &&
		request.LeaseGeneration > 0 && request.LeaseFence > 0 && request.RuntimeFence > 0 &&
		validOpaqueID(request.QuotaReservationID.String()) && request.QuotaReservationGeneration > 0 &&
		quotaReservationModeName(request.QuotaReservationMode) != "" &&
		validOpaqueID(request.GatewayRoutePolicyID.String()) && request.GatewayRoutePolicyGeneration > 0 &&
		request.Capability != 0 && request.Capability&^knownProviderCapabilityScope == 0 &&
		!request.AcceptedAt.IsZero()
}

func canonicalGatewayCallDigest(request GatewayCallRequest) Digest {
	payload := []byte(fmt.Sprintf(
		"slidesmith.gateway-call/v1\n%s\n%s\n%s\n%s\n%d\n%s\n%d\n%d\n%d\n%s\n%d\n%d\n%s\n%d\n%d\n%s",
		request.GatewayCallID.String(), request.RuntimeRunID.String(), request.StartOperationID.String(),
		request.GatewayGrantID.String(), request.GatewayGrantGeneration, request.LeaseID.String(),
		request.LeaseGeneration, request.LeaseFence, request.RuntimeFence, request.QuotaReservationID.String(),
		request.QuotaReservationGeneration, request.QuotaReservationMode, request.GatewayRoutePolicyID.String(),
		request.GatewayRoutePolicyGeneration, request.Capability, request.AcceptedAt.UTC().Format(canonicalTimeFormat),
	))
	return Digest(sha256.Sum256(payload))
}

func validGatewayAttemptSettlement(settlement GatewayAttemptSettlement) bool {
	return validOpaqueID(settlement.GatewayAttemptID.String()) && validOpaqueID(settlement.UsageReceiptID.String()) &&
		settlement.Disposition >= UsageEvidenceKnown && settlement.Disposition <= UsageEvidenceProvenNoSend &&
		settlement.Disposition != UsageEvidenceMissing && settlement.Disposition != UsageEvidenceNotApplicable &&
		!settlement.ObservedAt.IsZero()
}

func canonicalGatewayAttemptSettlementDigest(settlement GatewayAttemptSettlement) Digest {
	payload := []byte(fmt.Sprintf(
		"slidesmith.gateway-attempt-settlement/v1\n%s\n%s\n%d\n%s",
		settlement.GatewayAttemptID.String(), settlement.UsageReceiptID.String(), settlement.Disposition,
		settlement.ObservedAt.UTC().Format(canonicalTimeFormat),
	))
	return Digest(sha256.Sum256(payload))
}

func validUsageReceiptReference(reference UsageReceiptReference) bool {
	return validOpaqueID(reference.UsageReceiptID.String()) && validOpaqueID(reference.GatewayAttemptID.String()) &&
		reference.Disposition >= UsageEvidenceKnown && reference.Disposition <= UsageEvidenceProvenNoSend &&
		reference.Disposition != UsageEvidenceMissing && reference.Disposition != UsageEvidenceNotApplicable &&
		reference.CanonicalDigest != (Digest{})
}

func knownRuntimeUsageEvidence(snapshot RuntimeUsageEvidenceSnapshot) bool {
	if snapshot == (RuntimeUsageEvidenceSnapshot{}) {
		return true
	}
	if snapshot.Disposition < UsageEvidenceKnown || snapshot.Disposition > UsageEvidenceProvenNoSend {
		return false
	}
	count := snapshot.Receipts.Count
	if snapshot.Disposition == UsageEvidenceMissing || snapshot.Disposition == UsageEvidenceNotApplicable {
		return count == 0 && snapshot.Receipts.RootDigest == (Digest{})
	}
	if snapshot.Disposition == UsageEvidenceUnknown && count == 0 {
		return snapshot.Receipts.RootDigest == (Digest{})
	}
	if count == 0 {
		return false
	}
	if snapshot.Receipts.RootDigest == (Digest{}) {
		return false
	}
	if snapshot.Disposition == UsageEvidenceUnknown {
		return true
	}
	return true
}
