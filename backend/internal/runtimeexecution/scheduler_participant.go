package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// SchedulerAcceptanceFact is the complete immutable proposal C03 presents to
// the restricted Scheduler participant inside the C03 transaction.
type SchedulerAcceptanceFact struct {
	WorkItemID              WorkItemID
	AdmissionGrantID        AdmissionGrantID
	GrantGeneration         AdmissionGrantGeneration
	OperationID             OperationID
	CanonicalRequestDigest  Digest
	RuntimeRunID            RuntimeRunID
	DecisionID              RuntimeDecisionID
	AcceptedRuntimeRevision RuntimeRevision
	RuntimeFence            RuntimeFence
	LeaseAcquireOperationID OperationID
	LeaseAcquireDigest      Digest
	RuntimeDeadline         time.Time
	ResourceClassID         ResourceClassID
	ExecutionPolicyID       ExecutionPolicyID
	AcceptedAt              time.Time
}

// SchedulerGrantBinding is Scheduler-owned authority returned by the
// restricted participant. C03 can validate and retain it but cannot mutate it.
type SchedulerGrantBinding struct {
	ExecutionNodeID        ExecutionNodeID
	NodeCapacityGeneration uint64
	ResourceClassID        ResourceClassID
	ExecutionPolicyID      ExecutionPolicyID
	SchedulerEpoch         uint64
	PolicyVersion          uint64
	GrantExpiresAt         time.Time
	LeaseAcquireBy         time.Time
}

// SchedulerAcceptanceTransaction exposes only exact grant binding in the
// caller's PostgreSQL transaction. It exposes neither SQL nor Scheduler state.
type SchedulerAcceptanceTransaction interface {
	AcceptAndBind(context.Context) (SchedulerGrantBinding, error)
}

type SchedulerAcceptanceParticipant interface {
	Participate(context.Context, SchedulerAcceptanceTransaction, SchedulerAcceptanceFact) (SchedulerGrantBinding, error)
}

type SchedulerAcceptanceParticipantFunc func(
	context.Context,
	SchedulerAcceptanceTransaction,
	SchedulerAcceptanceFact,
) (SchedulerGrantBinding, error)

func (function SchedulerAcceptanceParticipantFunc) Participate(
	ctx context.Context,
	transaction SchedulerAcceptanceTransaction,
	fact SchedulerAcceptanceFact,
) (SchedulerGrantBinding, error) {
	return function(ctx, transaction, fact)
}

// SchedulerLeaseAttachmentFact is the exact C03 lease commit that Scheduler
// must bind to its already-Bound Admission Grant and selected-node reservation
// in the caller's PostgreSQL transaction.
type SchedulerLeaseAttachmentFact struct {
	WorkItemID              WorkItemID
	AdmissionGrantID        AdmissionGrantID
	GrantGeneration         AdmissionGrantGeneration
	RuntimeRunID            RuntimeRunID
	StartOperationID        OperationID
	StartDigest             Digest
	RuntimeRevision         RuntimeRevision
	RuntimeFence            RuntimeFence
	LeaseAcquireOperationID OperationID
	LeaseAcquireDigest      Digest
	SandboxLeaseID          SandboxLeaseID
	LeaseGeneration         LeaseGeneration
	LeaseFence              LeaseFence
	SandboxID               SandboxID
	SandboxGeneration       SandboxGeneration
	SandboxFence            SandboxFence
	ExecutionNodeID         ExecutionNodeID
	NodeCapacityGeneration  uint64
	ResourceClassID         ResourceClassID
	ExecutionPolicyID       ExecutionPolicyID
	SchedulerEpoch          uint64
	PolicyVersion           uint64
	AttachedAt              time.Time
}

// SchedulerLeaseAttachmentTransaction exposes only the restricted exact
// attachment function, never SQL or caller-controlled capacity mutation.
type SchedulerLeaseAttachmentTransaction interface {
	AttachLease(context.Context) error
}

type SchedulerLeaseAttachmentParticipant interface {
	ParticipateLeaseAttachment(context.Context, SchedulerLeaseAttachmentTransaction, SchedulerLeaseAttachmentFact) error
}

type SchedulerLeaseAttachmentParticipantFunc func(
	context.Context,
	SchedulerLeaseAttachmentTransaction,
	SchedulerLeaseAttachmentFact,
) error

func (function SchedulerLeaseAttachmentParticipantFunc) ParticipateLeaseAttachment(
	ctx context.Context,
	transaction SchedulerLeaseAttachmentTransaction,
	fact SchedulerLeaseAttachmentFact,
) error {
	return function(ctx, transaction, fact)
}

type QuotaReservationValidationFact struct {
	QuotaReservationID           QuotaReservationID
	Generation                   QuotaReservationGeneration
	Mode                         QuotaReservationMode
	PersonalWorkspaceID          PersonalWorkspaceID
	TaskID                       TaskID
	PhaseRunID                   PhaseRunID
	AuthorizationGeneration      AuthorizationGeneration
	Capability                   ProviderCapability
	GatewayRoutePolicyID         GatewayRoutePolicyID
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration
	CapabilityScope              ProviderCapabilityScope
	ValidAt                      time.Time
}

type QuotaReservationValidationResult struct {
	ExpiresAt time.Time
}

type QuotaReservationValidationTransaction interface {
	ValidateQuotaReservation(context.Context) (QuotaReservationValidationResult, error)
}

type QuotaReservationParticipant interface {
	ParticipateQuotaReservation(
		context.Context,
		QuotaReservationValidationTransaction,
		QuotaReservationValidationFact,
	) (QuotaReservationValidationResult, error)
}

type QuotaReservationParticipantFunc func(
	context.Context,
	QuotaReservationValidationTransaction,
	QuotaReservationValidationFact,
) (QuotaReservationValidationResult, error)

func (function QuotaReservationParticipantFunc) ParticipateQuotaReservation(
	ctx context.Context,
	transaction QuotaReservationValidationTransaction,
	fact QuotaReservationValidationFact,
) (QuotaReservationValidationResult, error) {
	return function(ctx, transaction, fact)
}

type SchedulerCancellationFact struct {
	OperationID            OperationID
	CanonicalRequestDigest Digest
	RuntimeRunID           RuntimeRunID
	DecisionID             RuntimeDecisionID
	RuntimeRevision        RuntimeRevision
	RuntimeFence           RuntimeFence
	AcceptedAt             time.Time
}

type SchedulerCancellationTransaction interface {
	AcceptCancellation(context.Context) error
}

type SchedulerCancellationParticipant interface {
	ParticipateCancellation(context.Context, SchedulerCancellationTransaction, SchedulerCancellationFact) error
}

type SchedulerCancellationParticipantFunc func(
	context.Context,
	SchedulerCancellationTransaction,
	SchedulerCancellationFact,
) error

func (function SchedulerCancellationParticipantFunc) ParticipateCancellation(
	ctx context.Context,
	transaction SchedulerCancellationTransaction,
	fact SchedulerCancellationFact,
) error {
	return function(ctx, transaction, fact)
}

func stableLeaseAcquireBinding(command StartRuntimeRun) (OperationID, Digest) {
	identity := sha256.Sum256([]byte(strings.Join([]string{
		"slidesmith.runtime-execution.lease-acquire-operation/v1",
		command.RuntimeRunID.String(), command.OperationID.String(),
		command.AdmissionGrant.AdmissionGrantID.String(),
		fmt.Sprintf("%d", command.AdmissionGrant.Generation),
	}, "\n")))
	operationID := OperationID{value: "lease-acquire-" + fmt.Sprintf("%x", identity[:16])}
	payload := strings.Join([]string{
		"slidesmith.runtime-execution.lease-acquire/v1",
		operationID.String(), command.RuntimeRunID.String(), command.OperationID.String(),
		command.CanonicalRequestDigest.String(), command.AdmissionGrant.AdmissionGrantID.String(),
		command.AdmissionGrant.WorkItemID.String(), fmt.Sprintf("%d", command.AdmissionGrant.Generation),
	}, "\n")
	return operationID, Digest(sha256.Sum256([]byte(payload)))
}
