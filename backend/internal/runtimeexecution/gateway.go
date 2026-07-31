package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

type GatewayGrantGeneration uint64

type GatewayGrantID struct{ value string }

func NewGatewayGrantID(value string) (GatewayGrantID, error) {
	value, err := newOpaqueID(value)
	return GatewayGrantID{value: value}, err
}

func (id GatewayGrantID) String() string { return id.value }

type GatewayGrantRequestKind uint8

const (
	GatewayGrantInitial GatewayGrantRequestKind = iota + 1
	GatewayGrantRefresh
)

type GatewayRecoveryMode uint8
type GatewayRecoveryGeneration uint64

const (
	GatewayRecoveryWritable GatewayRecoveryMode = iota + 1
	GatewayRecoveryDegradedReadOnly
)

type GatewayRecoverySnapshot struct {
	Generation GatewayRecoveryGeneration
	Mode       GatewayRecoveryMode
	ExpiresAt  time.Time
}

// GatewayRecoveryAuthority is the read-only current recovery fact used when
// preparing a grant and accepting each Call. It carries no recovery mutation
// or provider authority.
type GatewayRecoveryAuthority interface {
	InspectGatewayRecovery(context.Context) (GatewayRecoverySnapshot, error)
}

type GatewayRecoveryAuthorityFunc func(context.Context) (GatewayRecoverySnapshot, error)

func (function GatewayRecoveryAuthorityFunc) InspectGatewayRecovery(
	ctx context.Context,
) (GatewayRecoverySnapshot, error) {
	return function(ctx)
}

type GatewayGrantRequest struct {
	Kind                         GatewayGrantRequestKind
	OperationID                  OperationID
	CanonicalRequestDigest       Digest
	PersonalWorkspaceID          PersonalWorkspaceID
	TaskID                       TaskID
	PhaseRunID                   PhaseRunID
	RuntimeRunID                 RuntimeRunID
	StartOperationID             OperationID
	RuntimeBindingID             RuntimeBindingID
	RuntimeBindingDigest         Digest
	ReleaseSafetyEpoch           ReleaseSafetyEpoch
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
	CapabilityScope              ProviderCapabilityScope
	RecoveryGeneration           GatewayRecoveryGeneration
	RecoveryMode                 GatewayRecoveryMode
	RequestedGeneration          GatewayGrantGeneration
	PreviousGeneration           GatewayGrantGeneration
	PreviousGrantID              GatewayGrantID
	RuntimeDeadline              time.Time
	LeaseExpiresAt               time.Time
	AuthorizationExpiresAt       time.Time
	ReservationExpiresAt         time.Time
	RoutePolicyExpiresAt         time.Time
	RecoveryExpiresAt            time.Time
	NotAfter                     time.Time
}

type GatewayGrantOperationRef struct {
	OperationID            OperationID
	CanonicalRequestDigest Digest
}

type GatewayGrantInput struct {
	GatewayGrantID               GatewayGrantID
	Generation                   GatewayGrantGeneration
	PersonalWorkspaceID          PersonalWorkspaceID
	TaskID                       TaskID
	PhaseRunID                   PhaseRunID
	RuntimeRunID                 RuntimeRunID
	StartOperationID             OperationID
	RuntimeBindingID             RuntimeBindingID
	RuntimeBindingDigest         Digest
	ReleaseSafetyEpoch           ReleaseSafetyEpoch
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
	CapabilityScope              ProviderCapabilityScope
	RecoveryGeneration           GatewayRecoveryGeneration
	RecoveryMode                 GatewayRecoveryMode
	RecoveryExpiresAt            time.Time
	ExpiresAt                    time.Time
}

type GatewayGrant struct {
	GatewayGrantInput
	CanonicalDigest Digest
}

func NewGatewayGrant(input GatewayGrantInput) (GatewayGrant, error) {
	input.ExpiresAt = input.ExpiresAt.UTC()
	grant := GatewayGrant{GatewayGrantInput: input}
	if !validGatewayGrant(grant) {
		return GatewayGrant{}, newError(ErrorInvalidRequest)
	}
	grant.CanonicalDigest = canonicalGatewayGrantDigest(grant)
	return grant, nil
}

func gatewayGrantInputForRequest(id GatewayGrantID, request GatewayGrantRequest) GatewayGrantInput {
	return GatewayGrantInput{
		GatewayGrantID: id, Generation: request.RequestedGeneration,
		PersonalWorkspaceID: request.PersonalWorkspaceID, TaskID: request.TaskID, PhaseRunID: request.PhaseRunID,
		RuntimeRunID: request.RuntimeRunID, StartOperationID: request.StartOperationID,
		RuntimeBindingID: request.RuntimeBindingID, RuntimeBindingDigest: request.RuntimeBindingDigest,
		ReleaseSafetyEpoch: request.ReleaseSafetyEpoch,
		LeaseID:            request.LeaseID, LeaseGeneration: request.LeaseGeneration, LeaseFence: request.LeaseFence,
		RuntimeFence: request.RuntimeFence, QuotaReservationID: request.QuotaReservationID,
		QuotaReservationGeneration:   request.QuotaReservationGeneration,
		QuotaReservationMode:         request.QuotaReservationMode,
		OwnerAuthorityGeneration:     request.OwnerAuthorityGeneration,
		AuthorizationGeneration:      request.AuthorizationGeneration,
		GatewayRoutePolicyID:         request.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: request.GatewayRoutePolicyGeneration,
		CapabilityScope:              request.CapabilityScope, RecoveryGeneration: request.RecoveryGeneration,
		RecoveryMode: request.RecoveryMode, RecoveryExpiresAt: request.RecoveryExpiresAt, ExpiresAt: request.NotAfter,
	}
}

type GatewayGrantDecisionDisposition uint8

const (
	GatewayGrantDecisionAccepted GatewayGrantDecisionDisposition = iota + 1
	GatewayGrantDecisionRejected
	GatewayGrantDecisionUnknown
)

type GatewayGrantDecision struct {
	OperationID            OperationID
	CanonicalRequestDigest Digest
	Disposition            GatewayGrantDecisionDisposition
	Grant                  GatewayGrant
}

// GatewayGrantAdapter is the owned machine-authority port to ProviderAccess.
// It carries no provider credential, endpoint, route selection, or Usage
// Ledger mutation authority.
type GatewayGrantAdapter interface {
	DecideGatewayGrant(context.Context, GatewayGrantRequest) (GatewayGrantDecision, error)
	InspectGatewayGrant(context.Context, GatewayGrantOperationRef) (GatewayGrantDecision, error)
}

type GatewayPrerequisiteApplicability uint8

const (
	GatewayPrerequisiteNotApplicable GatewayPrerequisiteApplicability = iota + 1
	GatewayPrerequisiteRequired
)

type GatewayGrantStatus uint8

const (
	GatewayGrantNotApplicable GatewayGrantStatus = iota + 1
	GatewayGrantWaitingForLease
	GatewayGrantPending
	GatewayGrantCurrent
	GatewayGrantReconciliationRequired
	GatewayGrantExpired
	GatewayGrantStale
)

type GatewayPrerequisiteSnapshot struct {
	Applicability          GatewayPrerequisiteApplicability
	Status                 GatewayGrantStatus
	Ready                  bool
	OperationID            OperationID
	CanonicalRequestDigest Digest
	RequestedGeneration    GatewayGrantGeneration
	CurrentGrant           GatewayGrant
}

func stableGatewayGrantRequest(
	start StartRuntimeRun,
	lease RuntimeLeaseSnapshot,
	runtimeFence RuntimeFence,
	reservationExpiresAt time.Time,
	recovery GatewayRecoverySnapshot,
	now time.Time,
	lifetime time.Duration,
	current GatewayGrant,
) (GatewayGrantRequest, error) {
	if start.ProviderCapability != ProviderCapabilityRequired || start.ProviderBinding == nil ||
		lease.AcquireStatus != LeaseGranted || lease.Disposition != LeaseActive || lifetime <= 0 ||
		!gatewayRecoveryAllowsGrant(recovery, now) {
		return GatewayGrantRequest{}, newError(ErrorIntegrityConflict)
	}
	kind := GatewayGrantInitial
	requestedGeneration := GatewayGrantGeneration(1)
	previousGeneration := GatewayGrantGeneration(0)
	previousGrantID := GatewayGrantID{}
	if current.GatewayGrantID != (GatewayGrantID{}) {
		kind = GatewayGrantRefresh
		requestedGeneration = current.Generation + 1
		previousGeneration = current.Generation
		previousGrantID = current.GatewayGrantID
	}
	notAfter := earliestTime(
		now.UTC().Add(lifetime), start.Deadline.UTC(), lease.ExpiresAt.UTC(),
		lease.AuthorizationExpiresAt.UTC(), reservationExpiresAt.UTC(),
		start.ProviderBinding.RoutePolicyExpiresAt.UTC(), recovery.ExpiresAt.UTC(),
	)
	request := GatewayGrantRequest{
		Kind: kind, PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, StartOperationID: start.OperationID,
		RuntimeBindingID: start.RuntimeBindingID, RuntimeBindingDigest: start.RuntimeBindingDigest,
		ReleaseSafetyEpoch: start.ReleaseSafetyEpoch,
		LeaseID:            lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		RuntimeFence: runtimeFence, QuotaReservationID: start.ProviderBinding.QuotaReservationID,
		QuotaReservationGeneration:   start.ProviderBinding.Generation,
		QuotaReservationMode:         start.ProviderBinding.Mode,
		OwnerAuthorityGeneration:     start.Authority.generation,
		AuthorizationGeneration:      lease.AuthorizationGeneration,
		GatewayRoutePolicyID:         start.ProviderBinding.GatewayRoutePolicyID,
		GatewayRoutePolicyGeneration: start.ProviderBinding.GatewayRoutePolicyGeneration,
		CapabilityScope:              start.ProviderBinding.CapabilityScope,
		RecoveryGeneration:           recovery.Generation, RecoveryMode: recovery.Mode,
		RequestedGeneration: requestedGeneration, PreviousGeneration: previousGeneration,
		PreviousGrantID: previousGrantID, RuntimeDeadline: start.Deadline.UTC(),
		LeaseExpiresAt: lease.ExpiresAt.UTC(), AuthorizationExpiresAt: lease.AuthorizationExpiresAt.UTC(),
		ReservationExpiresAt: reservationExpiresAt.UTC(),
		RoutePolicyExpiresAt: start.ProviderBinding.RoutePolicyExpiresAt.UTC(),
		RecoveryExpiresAt:    recovery.ExpiresAt.UTC(), NotAfter: notAfter,
	}
	return NewCanonicalGatewayGrantRequest(request, now)
}

func NewCanonicalGatewayGrantRequest(request GatewayGrantRequest, now time.Time) (GatewayGrantRequest, error) {
	request.OperationID = OperationID{}
	request.CanonicalRequestDigest = Digest{}
	operationMaterial := canonicalGatewayGrantOperation(request)
	operationDigest := sha256.Sum256(append([]byte("slidesmith.runtime-execution.gateway-grant-operation/v1\n"), operationMaterial...))
	request.OperationID = OperationID{value: fmt.Sprintf("gateway-grant-%x", operationDigest[:16])}
	request.CanonicalRequestDigest = Digest(sha256.Sum256(append(
		[]byte("slidesmith.runtime-execution.gateway-grant-request/v1\n"),
		canonicalGatewayGrantRequest(request, true)...,
	)))
	if !validGatewayGrantRequest(request, now) {
		return GatewayGrantRequest{}, newError(ErrorIntegrityConflict)
	}
	return request, nil
}

func canonicalGatewayGrantOperation(request GatewayGrantRequest) []byte {
	return []byte(strings.Join([]string{
		fmt.Sprint(request.Kind), request.PersonalWorkspaceID.String(), request.TaskID.String(),
		request.PhaseRunID.String(), request.RuntimeRunID.String(), request.StartOperationID.String(),
		request.RuntimeBindingID.String(), request.RuntimeBindingDigest.String(),
		fmt.Sprint(request.RequestedGeneration), fmt.Sprint(request.PreviousGeneration), request.PreviousGrantID.String(),
	}, "\n"))
}

func canonicalGatewayGrantRequest(request GatewayGrantRequest, includeOperation bool) []byte {
	operation := ""
	if includeOperation {
		operation = request.OperationID.String()
	}
	return []byte(strings.Join([]string{
		fmt.Sprint(request.Kind), operation, request.PersonalWorkspaceID.String(), request.TaskID.String(),
		request.PhaseRunID.String(), request.RuntimeRunID.String(), request.StartOperationID.String(),
		request.RuntimeBindingID.String(), request.RuntimeBindingDigest.String(), fmt.Sprint(request.ReleaseSafetyEpoch),
		request.LeaseID.String(), fmt.Sprint(request.LeaseGeneration), fmt.Sprint(request.LeaseFence),
		fmt.Sprint(request.RuntimeFence), request.QuotaReservationID.String(),
		fmt.Sprint(request.QuotaReservationGeneration), fmt.Sprint(request.QuotaReservationMode),
		fmt.Sprint(request.OwnerAuthorityGeneration), fmt.Sprint(request.AuthorizationGeneration),
		request.GatewayRoutePolicyID.String(),
		fmt.Sprint(request.GatewayRoutePolicyGeneration), fmt.Sprint(request.CapabilityScope),
		fmt.Sprint(request.RecoveryGeneration), fmt.Sprint(request.RecoveryMode),
		fmt.Sprint(request.RequestedGeneration), fmt.Sprint(request.PreviousGeneration), request.PreviousGrantID.String(),
		request.RuntimeDeadline.UTC().Format(canonicalTimeFormat), request.LeaseExpiresAt.UTC().Format(canonicalTimeFormat),
		request.AuthorizationExpiresAt.UTC().Format(canonicalTimeFormat),
		request.ReservationExpiresAt.UTC().Format(canonicalTimeFormat),
		request.RoutePolicyExpiresAt.UTC().Format(canonicalTimeFormat),
		request.RecoveryExpiresAt.UTC().Format(canonicalTimeFormat), request.NotAfter.UTC().Format(canonicalTimeFormat),
	}, "\n"))
}

func gatewayGrantRequestDigestValid(request GatewayGrantRequest) bool {
	operationMaterial := canonicalGatewayGrantOperation(request)
	operationDigest := sha256.Sum256(append(
		[]byte("slidesmith.runtime-execution.gateway-grant-operation/v1\n"), operationMaterial...,
	))
	wantOperationID := OperationID{value: fmt.Sprintf("gateway-grant-%x", operationDigest[:16])}
	wantDigest := Digest(sha256.Sum256(append(
		[]byte("slidesmith.runtime-execution.gateway-grant-request/v1\n"),
		canonicalGatewayGrantRequest(request, true)...,
	)))
	return request.OperationID == wantOperationID && request.CanonicalRequestDigest == wantDigest
}

func validGatewayGrantRequest(request GatewayGrantRequest, now time.Time) bool {
	return (request.Kind == GatewayGrantInitial || request.Kind == GatewayGrantRefresh) &&
		gatewayGrantRequestDigestValid(request) &&
		validOpaqueID(request.OperationID.String()) && request.CanonicalRequestDigest != (Digest{}) &&
		validOpaqueID(request.PersonalWorkspaceID.String()) && validOpaqueID(request.TaskID.String()) &&
		validOpaqueID(request.PhaseRunID.String()) &&
		validOpaqueID(request.RuntimeRunID.String()) && validOpaqueID(request.StartOperationID.String()) &&
		validOpaqueID(request.RuntimeBindingID.String()) && request.RuntimeBindingDigest != (Digest{}) &&
		request.ReleaseSafetyEpoch > 0 &&
		validOpaqueID(request.LeaseID.String()) && request.LeaseGeneration > 0 && request.LeaseFence > 0 &&
		request.RuntimeFence > 0 && validOpaqueID(request.QuotaReservationID.String()) &&
		request.QuotaReservationGeneration > 0 && quotaReservationModeName(request.QuotaReservationMode) != "" &&
		request.OwnerAuthorityGeneration > 0 && request.AuthorizationGeneration > 0 &&
		validOpaqueID(request.GatewayRoutePolicyID.String()) &&
		request.GatewayRoutePolicyGeneration > 0 && request.CapabilityScope != 0 &&
		request.CapabilityScope&^knownProviderCapabilityScope == 0 && request.RecoveryGeneration > 0 &&
		request.RecoveryMode == GatewayRecoveryWritable && request.RequestedGeneration > 0 &&
		!request.NotAfter.After(request.RuntimeDeadline) && !request.NotAfter.After(request.LeaseExpiresAt) &&
		!request.NotAfter.After(request.AuthorizationExpiresAt) && !request.NotAfter.After(request.ReservationExpiresAt) &&
		!request.NotAfter.After(request.RoutePolicyExpiresAt) && !request.NotAfter.After(request.RecoveryExpiresAt) &&
		request.NotAfter.After(now.UTC()) &&
		(request.Kind != GatewayGrantInitial || request.RequestedGeneration == 1 && request.PreviousGeneration == 0 &&
			request.PreviousGrantID == (GatewayGrantID{})) &&
		(request.Kind != GatewayGrantRefresh || request.PreviousGeneration+1 == request.RequestedGeneration &&
			validOpaqueID(request.PreviousGrantID.String()))
}

func validGatewayGrant(grant GatewayGrant) bool {
	return validOpaqueID(grant.GatewayGrantID.String()) && grant.Generation > 0 &&
		validOpaqueID(grant.PersonalWorkspaceID.String()) && validOpaqueID(grant.TaskID.String()) &&
		validOpaqueID(grant.PhaseRunID.String()) &&
		validOpaqueID(grant.RuntimeRunID.String()) && validOpaqueID(grant.StartOperationID.String()) &&
		validOpaqueID(grant.RuntimeBindingID.String()) && grant.RuntimeBindingDigest != (Digest{}) &&
		grant.ReleaseSafetyEpoch > 0 &&
		validOpaqueID(grant.LeaseID.String()) && grant.LeaseGeneration > 0 && grant.LeaseFence > 0 &&
		grant.RuntimeFence > 0 && validOpaqueID(grant.QuotaReservationID.String()) &&
		grant.QuotaReservationGeneration > 0 && quotaReservationModeName(grant.QuotaReservationMode) != "" &&
		grant.OwnerAuthorityGeneration > 0 && grant.AuthorizationGeneration > 0 &&
		validOpaqueID(grant.GatewayRoutePolicyID.String()) &&
		grant.GatewayRoutePolicyGeneration > 0 && grant.CapabilityScope != 0 &&
		grant.CapabilityScope&^knownProviderCapabilityScope == 0 && grant.RecoveryGeneration > 0 &&
		grant.RecoveryMode == GatewayRecoveryWritable && !grant.RecoveryExpiresAt.IsZero() &&
		!grant.ExpiresAt.After(grant.RecoveryExpiresAt) && !grant.ExpiresAt.IsZero()
}

func canonicalGatewayGrantDigest(grant GatewayGrant) Digest {
	payload := []byte(fmt.Sprintf(
		"slidesmith.gateway-grant/v1\n%s\n%d\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n%d\n%s\n%d\n%d\n%d\n%s\n%d\n%d\n%d\n%d\n%s\n%d\n%d\n%d\n%d\n%s\n%s",
		grant.GatewayGrantID.String(), grant.Generation, grant.PersonalWorkspaceID.String(), grant.TaskID.String(),
		grant.PhaseRunID.String(), grant.RuntimeRunID.String(), grant.StartOperationID.String(),
		grant.RuntimeBindingID.String(), grant.RuntimeBindingDigest.String(), grant.ReleaseSafetyEpoch,
		grant.LeaseID.String(), grant.LeaseGeneration, grant.LeaseFence, grant.RuntimeFence,
		grant.QuotaReservationID.String(), grant.QuotaReservationGeneration, grant.QuotaReservationMode,
		grant.OwnerAuthorityGeneration, grant.AuthorizationGeneration,
		grant.GatewayRoutePolicyID.String(), grant.GatewayRoutePolicyGeneration,
		grant.CapabilityScope, grant.RecoveryGeneration, grant.RecoveryMode,
		grant.RecoveryExpiresAt.UTC().Format(canonicalTimeFormat), grant.ExpiresAt.UTC().Format(canonicalTimeFormat),
	))
	return Digest(sha256.Sum256(payload))
}

func gatewayGrantMatchesRequest(grant GatewayGrant, request GatewayGrantRequest, now time.Time) bool {
	return validGatewayGrant(grant) && grant.CanonicalDigest == canonicalGatewayGrantDigest(grant) &&
		grant.Generation == request.RequestedGeneration && grant.RuntimeRunID == request.RuntimeRunID &&
		grant.PersonalWorkspaceID == request.PersonalWorkspaceID && grant.TaskID == request.TaskID &&
		grant.PhaseRunID == request.PhaseRunID && grant.RuntimeBindingID == request.RuntimeBindingID &&
		grant.RuntimeBindingDigest == request.RuntimeBindingDigest &&
		grant.ReleaseSafetyEpoch == request.ReleaseSafetyEpoch &&
		grant.StartOperationID == request.StartOperationID && grant.LeaseID == request.LeaseID &&
		grant.LeaseGeneration == request.LeaseGeneration && grant.LeaseFence == request.LeaseFence &&
		grant.RuntimeFence == request.RuntimeFence && grant.QuotaReservationID == request.QuotaReservationID &&
		grant.QuotaReservationGeneration == request.QuotaReservationGeneration &&
		grant.QuotaReservationMode == request.QuotaReservationMode &&
		grant.OwnerAuthorityGeneration == request.OwnerAuthorityGeneration &&
		grant.AuthorizationGeneration == request.AuthorizationGeneration &&
		grant.GatewayRoutePolicyID == request.GatewayRoutePolicyID &&
		grant.GatewayRoutePolicyGeneration == request.GatewayRoutePolicyGeneration &&
		grant.RecoveryGeneration == request.RecoveryGeneration && grant.RecoveryMode == request.RecoveryMode &&
		grant.RecoveryExpiresAt.Equal(request.RecoveryExpiresAt) &&
		grant.CapabilityScope != 0 && grant.CapabilityScope&^request.CapabilityScope == 0 &&
		grant.ExpiresAt.After(now.UTC()) && !grant.ExpiresAt.After(request.NotAfter)
}

func gatewayGrantRequestMatchesAuthority(
	request GatewayGrantRequest,
	start StartRuntimeRun,
	lease RuntimeLeaseSnapshot,
	runtimeFence RuntimeFence,
	current GatewayGrant,
	reservationExpiresAt time.Time,
	recovery GatewayRecoverySnapshot,
) bool {
	return start.ProviderBinding != nil &&
		request.PersonalWorkspaceID == start.PersonalWorkspaceID && request.TaskID == start.TaskID &&
		request.PhaseRunID == start.PhaseRunID &&
		request.RuntimeRunID == start.RuntimeRunID && request.StartOperationID == start.OperationID &&
		request.RuntimeBindingID == start.RuntimeBindingID && request.RuntimeBindingDigest == start.RuntimeBindingDigest &&
		request.ReleaseSafetyEpoch == start.ReleaseSafetyEpoch &&
		request.LeaseID == lease.LeaseID && request.LeaseGeneration == lease.Generation &&
		request.LeaseFence == lease.Fence && request.RuntimeFence == runtimeFence &&
		request.QuotaReservationID == start.ProviderBinding.QuotaReservationID &&
		request.QuotaReservationGeneration == start.ProviderBinding.Generation &&
		request.QuotaReservationMode == start.ProviderBinding.Mode &&
		request.OwnerAuthorityGeneration == start.Authority.generation &&
		request.AuthorizationGeneration == lease.AuthorizationGeneration &&
		request.GatewayRoutePolicyID == start.ProviderBinding.GatewayRoutePolicyID &&
		request.GatewayRoutePolicyGeneration == start.ProviderBinding.GatewayRoutePolicyGeneration &&
		request.CapabilityScope == start.ProviderBinding.CapabilityScope &&
		request.RecoveryGeneration == recovery.Generation && request.RecoveryMode == recovery.Mode &&
		request.RecoveryExpiresAt.Equal(recovery.ExpiresAt.UTC()) &&
		request.RuntimeDeadline.Equal(start.Deadline.UTC()) && request.LeaseExpiresAt.Equal(lease.ExpiresAt) &&
		request.AuthorizationExpiresAt.Equal(lease.AuthorizationExpiresAt) &&
		request.ReservationExpiresAt.Equal(reservationExpiresAt.UTC()) &&
		request.RoutePolicyExpiresAt.Equal(start.ProviderBinding.RoutePolicyExpiresAt.UTC()) &&
		request.PreviousGeneration == current.Generation &&
		request.PreviousGrantID == current.GatewayGrantID
}

func validGatewayRecoverySnapshot(snapshot GatewayRecoverySnapshot) bool {
	return snapshot.Generation > 0 &&
		(snapshot.Mode == GatewayRecoveryWritable || snapshot.Mode == GatewayRecoveryDegradedReadOnly) &&
		!snapshot.ExpiresAt.IsZero()
}

func gatewayRecoveryAllowsGrant(snapshot GatewayRecoverySnapshot, now time.Time) bool {
	return validGatewayRecoverySnapshot(snapshot) && snapshot.Mode == GatewayRecoveryWritable &&
		snapshot.ExpiresAt.After(now.UTC())
}

func inspectGatewayRecovery(
	ctx context.Context,
	authority GatewayRecoveryAuthority,
) (GatewayRecoverySnapshot, error) {
	if ctx == nil || ctx.Err() != nil || authority == nil {
		return GatewayRecoverySnapshot{}, newError(ErrorDependencyUnavailable)
	}
	snapshot, err := authority.InspectGatewayRecovery(ctx)
	snapshot.ExpiresAt = snapshot.ExpiresAt.UTC()
	if err != nil {
		return GatewayRecoverySnapshot{}, newError(ErrorDependencyUnavailable)
	}
	if !validGatewayRecoverySnapshot(snapshot) {
		return GatewayRecoverySnapshot{}, newError(ErrorIntegrityConflict)
	}
	return snapshot, nil
}

func gatewayGrantRecoveryCurrent(
	grant GatewayGrant,
	recovery GatewayRecoverySnapshot,
	now time.Time,
) bool {
	return gatewayRecoveryAllowsGrant(recovery, now) && grant.RecoveryGeneration == recovery.Generation &&
		grant.RecoveryMode == recovery.Mode && !grant.ExpiresAt.After(recovery.ExpiresAt)
}

func gatewayGrantCurrentForRecord(
	record *runtimeRecord,
	recovery GatewayRecoverySnapshot,
	now time.Time,
) bool {
	if record == nil || record.fixture.State == RuntimeTerminal || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.gateway.Applicability != GatewayPrerequisiteRequired ||
		record.gateway.Status != GatewayGrantCurrent || !record.gateway.Ready ||
		record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive ||
		!record.deadline.After(now.UTC()) || !record.lease.ExpiresAt.After(now.UTC()) ||
		!record.lease.AuthorizationExpiresAt.After(now.UTC()) {
		return false
	}
	grant := record.gateway.CurrentGrant
	return validGatewayGrant(grant) && grant.CanonicalDigest == canonicalGatewayGrantDigest(grant) &&
		grant.PersonalWorkspaceID == record.fixture.PersonalWorkspaceID && grant.TaskID == record.fixture.TaskID &&
		grant.PhaseRunID == record.fixture.PhaseRunID && grant.RuntimeRunID == record.fixture.RuntimeRunID &&
		grant.StartOperationID == record.operation.OperationID &&
		grant.ReleaseSafetyEpoch == record.fixture.SafetyEpoch &&
		grant.RuntimeFence == record.fixture.RuntimeFence &&
		grant.OwnerAuthorityGeneration == record.fixture.Owner.generation &&
		grant.LeaseID == record.lease.LeaseID && grant.LeaseGeneration == record.lease.Generation &&
		grant.LeaseFence == record.lease.Fence &&
		grant.AuthorizationGeneration == record.lease.AuthorizationGeneration &&
		gatewayGrantRecoveryCurrent(grant, recovery, now)
}

func quotaReservationAuthorizesGateway(
	reservation *QuotaReservationFixture,
	record *runtimeRecord,
	now time.Time,
) bool {
	if reservation == nil || record == nil {
		return false
	}
	grant := record.gateway.CurrentGrant
	return reservation.State == QuotaReservationActive &&
		reservation.QuotaReservationID == grant.QuotaReservationID &&
		reservation.Generation == grant.QuotaReservationGeneration && reservation.Mode == grant.QuotaReservationMode &&
		reservation.PersonalWorkspaceID == record.fixture.PersonalWorkspaceID &&
		reservation.TaskID == record.fixture.TaskID && reservation.PhaseRunID == record.fixture.PhaseRunID &&
		reservation.AuthorizationGeneration == grant.OwnerAuthorityGeneration &&
		reservation.Capability == ProviderCapabilityRequired &&
		reservation.GatewayRoutePolicyID == grant.GatewayRoutePolicyID &&
		reservation.GatewayRoutePolicyGeneration == grant.GatewayRoutePolicyGeneration &&
		reservation.CapabilityScope == grant.CapabilityScope && !now.UTC().Before(reservation.ValidFrom) &&
		reservation.ExpiresAt.After(now.UTC()) && !grant.ExpiresAt.After(reservation.ExpiresAt)
}

func projectGatewayAuthorityAt(snapshot *RuntimeSnapshot, current bool, now time.Time) {
	if snapshot == nil || snapshot.Gateway.Applicability != GatewayPrerequisiteRequired ||
		snapshot.Gateway.Status != GatewayGrantCurrent || !snapshot.Gateway.Ready || current {
		return
	}
	if !snapshot.Gateway.CurrentGrant.ExpiresAt.After(now.UTC()) {
		snapshot.Gateway.Status = GatewayGrantExpired
	} else {
		snapshot.Gateway.Status = GatewayGrantStale
	}
	snapshot.Gateway.Ready = false
}

func knownGatewayPrerequisite(snapshot GatewayPrerequisiteSnapshot) bool {
	if snapshot == (GatewayPrerequisiteSnapshot{}) {
		return true
	}
	if snapshot.Applicability == GatewayPrerequisiteNotApplicable {
		return snapshot.Status == GatewayGrantNotApplicable && snapshot.Ready &&
			snapshot.CurrentGrant == (GatewayGrant{})
	}
	if snapshot.Applicability != GatewayPrerequisiteRequired ||
		snapshot.Status < GatewayGrantWaitingForLease || snapshot.Status > GatewayGrantStale {
		return false
	}
	if snapshot.Status == GatewayGrantCurrent {
		return snapshot.Ready && snapshot.CurrentGrant != (GatewayGrant{}) &&
			validGatewayGrant(snapshot.CurrentGrant)
	}
	return !snapshot.Ready
}

func gatewayPrerequisiteFactAt(snapshot GatewayPrerequisiteSnapshot, now time.Time) PrerequisiteFact {
	switch snapshot.Applicability {
	case GatewayPrerequisiteNotApplicable:
		if snapshot.Status == GatewayGrantNotApplicable && snapshot.Ready {
			return PrerequisiteFact{State: PrerequisiteNotApplicable}
		}
	case GatewayPrerequisiteRequired:
		if snapshot.Status == GatewayGrantReconciliationRequired &&
			validOpaqueID(snapshot.OperationID.String()) && snapshot.CanonicalRequestDigest != (Digest{}) {
			return PrerequisiteFact{
				State:         PrerequisiteReconciliationRequired,
				OperationID:   snapshot.OperationID,
				RequestDigest: snapshot.CanonicalRequestDigest,
				Failure:       PrerequisiteFailureDependencyUnavailable,
			}
		}
		grant := snapshot.CurrentGrant
		if snapshot.Status == GatewayGrantCurrent && snapshot.Ready &&
			validOpaqueID(snapshot.OperationID.String()) && snapshot.CanonicalRequestDigest != (Digest{}) &&
			snapshot.RequestedGeneration == grant.Generation && validGatewayGrant(grant) &&
			grant.CanonicalDigest == canonicalGatewayGrantDigest(grant) && grant.ExpiresAt.After(now.UTC()) {
			return PrerequisiteFact{
				State:          PrerequisiteAccepted,
				OperationID:    snapshot.OperationID,
				RequestDigest:  snapshot.CanonicalRequestDigest,
				EvidenceID:     EvidenceID{value: "gateway-grant-evidence-" + grant.GatewayGrantID.String()},
				EvidenceDigest: grant.CanonicalDigest,
			}
		}
	}
	return PrerequisiteFact{State: PrerequisitePending}
}

func synchronizeGatewayReadiness(record *runtimeRecord, now time.Time) {
	if record == nil || record.readiness == (RuntimeReadinessSnapshot{}) ||
		record.gateway == (GatewayPrerequisiteSnapshot{}) {
		return
	}
	record.readiness.LLMGateway = gatewayPrerequisiteFactAt(record.gateway, now)
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease, record.capsule.snapshot)
}

func (engine *invariantEngine) advanceGatewayPrerequisite(
	ctx context.Context,
	start StartRuntimeRun,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	now := engine.clock.current()
	if start.ProviderCapability == ProviderCapabilityNone {
		record.gateway = GatewayPrerequisiteSnapshot{
			Applicability: GatewayPrerequisiteNotApplicable, Status: GatewayGrantNotApplicable, Ready: true,
		}
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if start.ProviderCapability != ProviderCapabilityRequired || start.ProviderBinding == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		record.gateway.Status = GatewayGrantStale
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		record.gateway.Status = GatewayGrantWaitingForLease
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if !gatewayRequestPrerequisitesSatisfiedAt(snapshot(record, SnapshotSchemaCurrent), now) {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		record.gateway.Status = GatewayGrantPending
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	reservation := engine.store.reservations[start.ProviderBinding.QuotaReservationID]
	if !reservationEligibleForLease(engine.store.reservations, start, now) {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		record.gateway.Status = GatewayGrantStale
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if engine.gatewayGrants == nil {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		record.gateway.Status = GatewayGrantPending
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	engine.store.mu.Unlock()
	recovery, recoveryErr := inspectGatewayRecovery(ctx, engine.gatewayRecovery)
	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	now = engine.clock.current()
	reservation = engine.store.reservations[start.ProviderBinding.QuotaReservationID]
	if record == nil || record.fixture.State == RuntimeTerminal ||
		record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive ||
		!gatewayRequestPrerequisitesSatisfiedAt(snapshot(record, SnapshotSchemaCurrent), now) ||
		!reservationEligibleForLease(engine.store.reservations, start, now) || reservation == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if recoveryErr != nil {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		record.gateway.Status = GatewayGrantReconciliationRequired
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	if !gatewayRecoveryAllowsGrant(recovery, now) {
		record.gateway.Applicability = GatewayPrerequisiteRequired
		if record.gateway.CurrentGrant == (GatewayGrant{}) {
			record.gateway.Status = GatewayGrantPending
		} else {
			record.gateway.Status = GatewayGrantStale
		}
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.gateway.Status == GatewayGrantCurrent && record.gateway.CurrentGrant.ExpiresAt.After(now) &&
		record.gateway.CurrentGrant.ExpiresAt.Sub(now) > 20*time.Second &&
		gatewayGrantRecoveryCurrent(record.gateway.CurrentGrant, recovery, now) {
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.gateway.Status == GatewayGrantCurrent && !record.gateway.CurrentGrant.ExpiresAt.After(now) {
		record.gateway.Status = GatewayGrantExpired
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	var request GatewayGrantRequest
	if (record.gateway.Status == GatewayGrantPending ||
		record.gateway.Status == GatewayGrantReconciliationRequired ||
		record.gateway.Status == GatewayGrantExpired) && record.gatewayRequest != nil {
		retained := *record.gatewayRequest
		if record.gateway.OperationID != retained.OperationID ||
			record.gateway.CanonicalRequestDigest != retained.CanonicalRequestDigest ||
			!gatewayGrantRequestMatchesAuthority(retained, start, record.lease, record.fixture.RuntimeFence,
				record.gateway.CurrentGrant, reservation.ExpiresAt, recovery) {
			engine.store.mu.Unlock()
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
		request = retained
	}
	if request == (GatewayGrantRequest{}) {
		var err error
		request, err = stableGatewayGrantRequest(
			start, record.lease, record.fixture.RuntimeFence, reservation.ExpiresAt,
			recovery, now, engine.grantLifetime, record.gateway.CurrentGrant,
		)
		if err != nil {
			engine.store.mu.Unlock()
			return RuntimeDecision{}, err
		}
		retained := request
		record.gatewayRequest = &retained
	}
	if !request.NotAfter.After(now) {
		record.gateway.Status = GatewayGrantExpired
		record.gateway.Ready = false
		synchronizeGatewayReadiness(record, now)
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	retainedGeneration := record.gateway.CurrentGrant.Generation
	record.gateway = GatewayPrerequisiteSnapshot{
		Applicability: GatewayPrerequisiteRequired, Status: GatewayGrantPending,
		OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
		RequestedGeneration: request.RequestedGeneration, CurrentGrant: record.gateway.CurrentGrant,
	}
	synchronizeGatewayReadiness(record, now)
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()

	gatewayDecision, decideErr := engine.gatewayGrants.DecideGatewayGrant(ctx, request)
	if decideErr != nil {
		gatewayDecision, decideErr = engine.gatewayGrants.InspectGatewayGrant(ctx, GatewayGrantOperationRef{
			OperationID: request.OperationID, CanonicalRequestDigest: request.CanonicalRequestDigest,
		})
	}
	if decideErr != nil {
		engine.store.mu.Lock()
		record = engine.store.runtimes[start.RuntimeRunID]
		if record != nil && record.gateway.OperationID == request.OperationID &&
			record.gateway.CanonicalRequestDigest == request.CanonicalRequestDigest {
			record.gateway.Status = GatewayGrantReconciliationRequired
			record.gateway.Ready = false
			synchronizeGatewayReadiness(record, engine.clock.current())
		}
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	if gatewayDecision.OperationID != request.OperationID ||
		gatewayDecision.CanonicalRequestDigest != request.CanonicalRequestDigest ||
		gatewayDecision.Disposition != GatewayGrantDecisionAccepted ||
		!gatewayGrantMatchesRequest(gatewayDecision.Grant, request, now) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	acceptanceRecovery, recoveryErr := inspectGatewayRecovery(ctx, engine.gatewayRecovery)
	if recoveryErr != nil {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}

	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	reservation = engine.store.reservations[start.ProviderBinding.QuotaReservationID]
	if record == nil || record.fixture.State == RuntimeTerminal || record.fixture.RuntimeFence != request.RuntimeFence ||
		record.lease.LeaseID != request.LeaseID || record.lease.Generation != request.LeaseGeneration ||
		record.lease.Fence != request.LeaseFence || record.lease.Disposition != LeaseActive ||
		record.gateway.OperationID != request.OperationID ||
		record.gateway.CanonicalRequestDigest != request.CanonicalRequestDigest ||
		record.gateway.CurrentGrant.Generation != retainedGeneration ||
		!reservationEligibleForLease(engine.store.reservations, start, engine.clock.current()) || reservation == nil ||
		!gatewayGrantRequestMatchesAuthority(request, start, record.lease, record.fixture.RuntimeFence,
			record.gateway.CurrentGrant, reservation.ExpiresAt, acceptanceRecovery) {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	record.gateway.Status = GatewayGrantCurrent
	record.gateway.Ready = true
	record.gateway.CurrentGrant = gatewayDecision.Grant
	synchronizeGatewayReadiness(record, engine.clock.current())
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	return decision, nil
}

func (harness *DeterministicHarness) ValidateGatewayCall(
	ctx context.Context,
	fact GatewayCallAuthorityFact,
	accept GatewayCallAcceptance,
) error {
	if ctx == nil || ctx.Err() != nil || harness.controls.isCrashed() || accept == nil {
		return newError(ErrorDependencyUnavailable)
	}
	now := harness.clock.current()
	if !fact.ValidAt.UTC().Equal(now) || fact.Capability == 0 ||
		fact.Capability&^knownProviderCapabilityScope != 0 {
		return newError(ErrorIntegrityConflict)
	}
	harness.store.mu.Lock()
	defer harness.store.mu.Unlock()
	record := harness.store.runtimes[fact.RuntimeRunID]
	if record == nil || record.fixture.State == RuntimeTerminal || record.fixture.Outcome != RuntimeOutcomeNone ||
		record.fixture.PersonalWorkspaceID != fact.PersonalWorkspaceID || record.fixture.TaskID != fact.TaskID ||
		record.fixture.PhaseRunID != fact.PhaseRunID || record.fixture.SafetyEpoch != fact.ReleaseSafetyEpoch ||
		record.fixture.Owner.generation != fact.OwnerAuthorityGeneration ||
		record.fixture.RuntimeFence != fact.RuntimeFence || !record.deadline.After(now) ||
		record.gateway.Status != GatewayGrantCurrent || !record.gateway.Ready ||
		!gatewayGrantAuthorizesCall(record.gateway.CurrentGrant, fact, now) ||
		record.lease.AcquireStatus != LeaseGranted || record.lease.Disposition != LeaseActive ||
		record.lease.LeaseID != fact.LeaseID || record.lease.Generation != fact.LeaseGeneration ||
		record.lease.Fence != fact.LeaseFence ||
		record.lease.AuthorizationGeneration != fact.AuthorizationGeneration ||
		!record.lease.ExpiresAt.After(now) {
		return newError(ErrorIntegrityConflict)
	}
	reservation := harness.store.reservations[fact.QuotaReservationID]
	if reservation == nil || reservation.State != QuotaReservationActive ||
		reservation.Generation != fact.QuotaReservationGeneration || reservation.Mode != fact.QuotaReservationMode ||
		reservation.PersonalWorkspaceID != record.fixture.PersonalWorkspaceID ||
		reservation.TaskID != record.fixture.TaskID || reservation.PhaseRunID != record.fixture.PhaseRunID ||
		reservation.AuthorizationGeneration != fact.OwnerAuthorityGeneration ||
		reservation.GatewayRoutePolicyID != fact.GatewayRoutePolicyID ||
		reservation.GatewayRoutePolicyGeneration != fact.GatewayRoutePolicyGeneration ||
		fact.GatewayRoutePolicyID != record.gateway.CurrentGrant.GatewayRoutePolicyID ||
		fact.GatewayRoutePolicyGeneration != record.gateway.CurrentGrant.GatewayRoutePolicyGeneration ||
		reservation.CapabilityScope != record.gateway.CurrentGrant.CapabilityScope ||
		now.Before(reservation.ValidFrom) || !reservation.ExpiresAt.After(now) {
		return newError(ErrorIntegrityConflict)
	}
	return validateGatewayCallExternalAuthority(ctx, harness.gatewayCallAuthority,
		gatewayCallExternalAuthorityFact(record.gateway.CurrentGrant, now), accept)
}

var _ GatewayCallAuthorityValidator = (*DeterministicHarness)(nil)

func (engine *invariantEngine) advanceUsageEvidence(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	decision RuntimeDecision,
) (RuntimeDecision, error) {
	if decision.Fact.Disposition != DecisionAccepted {
		return decision, nil
	}
	engine.store.mu.Lock()
	record := engine.store.runtimes[runtimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal && record.gateway.Applicability == GatewayPrerequisiteRequired {
		record.gateway.Status = GatewayGrantStale
		record.gateway.Ready = false
	}
	synchronizeGatewayReadiness(record, engine.clock.current())
	if record.gateway.Applicability == GatewayPrerequisiteNotApplicable {
		record.usage = RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceNotApplicable}
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	if record.gateway.Applicability != GatewayPrerequisiteRequired {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if engine.usageReceipts == nil {
		if record.usage == (RuntimeUsageEvidenceSnapshot{}) {
			record.usage = RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceMissing}
		}
		decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return decision, nil
	}
	engine.store.mu.Unlock()

	evidence, err := engine.usageReceipts.QueryUsageReceiptEvidence(ctx, runtimeRunID)
	if err != nil {
		engine.store.mu.Lock()
		record = engine.store.runtimes[runtimeRunID]
		if record != nil && record.usage.Disposition == UsageEvidenceMissing {
			record.usage = RuntimeUsageEvidenceSnapshot{Disposition: UsageEvidenceUnknown}
			decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		}
		engine.store.mu.Unlock()
		return decision, nil
	}
	set, err := NewUsageReceiptReferenceSet(evidence.References)
	if err != nil {
		return RuntimeDecision{}, err
	}
	updated := RuntimeUsageEvidenceSnapshot{Disposition: evidence.Disposition, Receipts: set}
	if !knownRuntimeUsageEvidence(updated) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Lock()
	record = engine.store.runtimes[runtimeRunID]
	if record == nil || record.gateway.Applicability != GatewayPrerequisiteRequired {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	record.usage = updated
	decision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	return decision, nil
}
