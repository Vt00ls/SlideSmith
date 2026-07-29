// Package scheduler owns durable Work Item delivery and the unique capacity
// admission decision used by Runtime Execution.
package scheduler

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
)

type (
	SchedulerEpoch         uint64
	PolicyVersion          uint64
	GrantGeneration        uint64
	NodeCapacityGeneration uint64
	Digest                 [sha256.Size]byte
)

type WorkItemID struct{ value string }
type PersonalWorkspaceID = runtimeexecution.PersonalWorkspaceID
type AdmissionGrantID struct{ value string }
type DeliveryClaimID struct{ value string }
type ExecutionNodeID struct{ value string }
type ResourceClassID struct{ value string }
type ExecutionPolicyID struct{ value string }

func NewExecutionNodeID(value string) (ExecutionNodeID, error) {
	if !validOpaqueID(value) {
		return ExecutionNodeID{}, newError(ErrorInvalidRequest)
	}
	return ExecutionNodeID{value: value}, nil
}

func NewPersonalWorkspaceID(value string) (PersonalWorkspaceID, error) {
	return runtimeexecution.NewPersonalWorkspaceID(value)
}

func NewResourceClassID(value string) (ResourceClassID, error) {
	if !validOpaqueID(value) {
		return ResourceClassID{}, newError(ErrorInvalidRequest)
	}
	return ResourceClassID{value: value}, nil
}

func NewExecutionPolicyID(value string) (ExecutionPolicyID, error) {
	if !validOpaqueID(value) {
		return ExecutionPolicyID{}, newError(ErrorInvalidRequest)
	}
	return ExecutionPolicyID{value: value}, nil
}

func (id WorkItemID) String() string        { return id.value }
func (id AdmissionGrantID) String() string  { return id.value }
func (id DeliveryClaimID) String() string   { return id.value }
func (id ExecutionNodeID) String() string   { return id.value }
func (id ResourceClassID) String() string   { return id.value }
func (id ExecutionPolicyID) String() string { return id.value }
func (digest Digest) String() string        { return hexDigest(digest[:]) }

type WorkItemState uint8

const (
	WorkItemQueued WorkItemState = iota + 1
	WorkItemDelivering
	WorkItemAccepted
	WorkItemCancelled
)

type GrantState uint8

const (
	GrantReservedUnbound GrantState = iota + 1
	GrantBound
	GrantExpiredUnbound
	GrantTerminalNoLease
	GrantReleased
)

type AdmissionGrant struct {
	AdmissionGrantID       AdmissionGrantID
	WorkItemID             WorkItemID
	Generation             GrantGeneration
	DeliveryClaimID        DeliveryClaimID
	ExecutionNodeID        ExecutionNodeID
	NodeCapacityGeneration NodeCapacityGeneration
	ResourceClassID        ResourceClassID
	ExecutionPolicyID      ExecutionPolicyID
	SchedulerEpoch         SchedulerEpoch
	PolicyVersion          PolicyVersion
	ExpiresAt              time.Time
}

type AdmissionDecision struct {
	WorkItemID          WorkItemID
	PersonalWorkspaceID PersonalWorkspaceID
	OperationID         string
	TaskID              string
	PhaseRunID          string
	RuntimeRunID        string
	PayloadDigest       Digest
	CanonicalPayload    []byte
	Grant               AdmissionGrant
}

type CancellationDecision struct {
	WorkItemID          WorkItemID
	PersonalWorkspaceID PersonalWorkspaceID
	OperationID         string
	TaskID              string
	PhaseRunID          string
	RuntimeRunID        string
	PayloadDigest       Digest
	CanonicalPayload    []byte
}

type WorkItemQueryScopeKind uint8

const (
	WorkItemQueryOwner WorkItemQueryScopeKind = iota + 1
	WorkItemQueryAdministrator
)

type WorkItemQueryScope struct {
	kind                WorkItemQueryScopeKind
	personalWorkspaceID PersonalWorkspaceID
}

func NewOwnerWorkItemQueryScope(personalWorkspaceID PersonalWorkspaceID) WorkItemQueryScope {
	return WorkItemQueryScope{kind: WorkItemQueryOwner, personalWorkspaceID: personalWorkspaceID}
}

func NewAdministratorWorkItemQueryScope() WorkItemQueryScope {
	return WorkItemQueryScope{kind: WorkItemQueryAdministrator}
}

type WorkItemRef struct {
	WorkItemID WorkItemID
	Scope      WorkItemQueryScope
}

type GrantView struct {
	AdmissionGrant
	State GrantState
}

type WorkItemView struct {
	WorkItemID              WorkItemID
	OperationID             string
	TaskID                  string
	PhaseRunID              string
	RuntimeRunID            string
	PayloadDigest           Digest
	State                   WorkItemState
	Grant                   GrantView
	LogicalReservation      ReservationState
	SelectedNodeReservation ReservationState
}

type ReservationState uint8

const (
	ReservationReservedUnbound ReservationState = iota + 1
	ReservationBound
	ReservationReleased ReservationState = 5
)

type Scheduling interface {
	ClaimAndAdmit(context.Context) (AdmissionDecision, error)
	ClaimCancellation(context.Context) (CancellationDecision, error)
	Inspect(context.Context, WorkItemRef) (WorkItemView, error)
	ApplyRuntimeFencedOrTerminal(context.Context, runtimeexecution.RuntimeFencedOrTerminalEvidence) error
	ApplyNoLeasePhysicalDisposition(context.Context, runtimeexecution.NoLeasePhysicalDispositionEvidence) error
}

type ErrorCode uint8

const (
	ErrorInvalidRequest ErrorCode = iota + 1
	ErrorNoEligibleWork
	ErrorIntegrityConflict
	ErrorDependencyUnavailable
	ErrorAuthorizationDenied
)

type Error struct{ code ErrorCode }

func (failure *Error) Error() string {
	if failure == nil {
		return "scheduler request is invalid"
	}
	switch failure.code {
	case ErrorNoEligibleWork:
		return "scheduler has no eligible work"
	case ErrorIntegrityConflict:
		return "scheduler authority binding conflicts with retained state"
	case ErrorDependencyUnavailable:
		return "scheduler dependency is unavailable"
	case ErrorAuthorizationDenied:
		return "scheduler Work Item is not available to this query scope"
	default:
		return "scheduler request is invalid"
	}
}

func (failure *Error) Code() ErrorCode {
	if failure == nil {
		return ErrorInvalidRequest
	}
	return failure.code
}

func newError(code ErrorCode) *Error { return &Error{code: code} }

func (scope WorkItemQueryScope) valid() bool {
	return scope.kind == WorkItemQueryAdministrator ||
		scope.kind == WorkItemQueryOwner && validOpaqueID(scope.personalWorkspaceID.String())
}

func (scope WorkItemQueryScope) administrator() bool {
	return scope.kind == WorkItemQueryAdministrator
}

func validOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 255 {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func hexDigest(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = alphabet[item>>4]
		encoded[index*2+1] = alphabet[item&0x0f]
	}
	return string(encoded)
}
