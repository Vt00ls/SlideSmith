package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// LeaseAcquisitionDisposition is a closed observation from the owned
// execution-node adapter. It is evidence input only; Runtime Execution owns
// every authoritative state, lease, fence, and capacity mutation.
type LeaseAcquisitionDisposition uint8

const (
	LeaseAcquisitionTemporaryUnavailable LeaseAcquisitionDisposition = iota + 1
	LeaseAcquisitionRetryablePrerequisite
	LeaseAcquisitionAmbiguousPrerequisite
	LeaseAcquisitionPermanentFailure
	LeaseAcquisitionReady
)

type PreLeasePermanentFailure uint8

const (
	PreLeasePermanentStaleNodeGeneration PreLeasePermanentFailure = iota + 1
	PreLeasePermanentNodeIneligible
	PreLeasePermanentReservation
	PreLeasePermanentAuthorization
	PreLeasePermanentResourceClass
	PreLeasePermanentExecutionPolicy
	PreLeasePermanentSchedulerPolicy
	PreLeasePermanentSchedulerEpoch
	PreLeasePermanentReleaseSafety
	PreLeasePermanentCatalogSafety
	PreLeasePermanentImmutableBinding
)

type PreLeaseTerminalReason uint8

const (
	PreLeaseTerminalNone PreLeaseTerminalReason = iota
	PreLeaseTerminalStaleNodeGeneration
	PreLeaseTerminalNodeIneligible
	PreLeaseTerminalReservation
	PreLeaseTerminalAuthorization
	PreLeaseTerminalResourceClass
	PreLeaseTerminalExecutionPolicy
	PreLeaseTerminalSchedulerPolicy
	PreLeaseTerminalSchedulerEpoch
	PreLeaseTerminalReleaseSafety
	PreLeaseTerminalCatalogSafety
	PreLeaseTerminalImmutableBinding
	PreLeaseTerminalAdmissionAuthorityExpired
	PreLeaseTerminalRuntimeDeadline
)

type LeaseAcquisitionRequest struct {
	RuntimeRunID           RuntimeRunID
	WorkItemID             WorkItemID
	AdmissionGrantID       AdmissionGrantID
	GrantGeneration        AdmissionGrantGeneration
	StartOperationID       OperationID
	StartDigest            Digest
	OperationID            OperationID
	Digest                 Digest
	ExecutionNodeID        ExecutionNodeID
	NodeCapacityGeneration uint64
	ResourceClassID        ResourceClassID
	ExecutionPolicyID      ExecutionPolicyID
	SchedulerEpoch         uint64
	PolicyVersion          uint64
	SafetyEpoch            ReleaseSafetyEpoch
	RuntimeFence           RuntimeFence
	Deadline               time.Time
	LeaseAcquireBy         time.Time
}

type LeaseAcquisitionObservation struct {
	Disposition      LeaseAcquisitionDisposition
	PermanentFailure PreLeasePermanentFailure
}

// LeaseAcquisitionAdapter is a private owned-adapter port. Callers cannot use
// it to mutate Runtime state or choose a lease identity, node, generation, or
// fence; it can only report a closed observation for the exact stable request.
type LeaseAcquisitionAdapter interface {
	ObserveLeaseAcquisition(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error)
}

type LeaseAcquisitionAdapterFunc func(context.Context, LeaseAcquisitionRequest) (LeaseAcquisitionObservation, error)

func (function LeaseAcquisitionAdapterFunc) ObserveLeaseAcquisition(
	ctx context.Context,
	request LeaseAcquisitionRequest,
) (LeaseAcquisitionObservation, error) {
	return function(ctx, request)
}

func validLeaseAcquisitionObservation(observation LeaseAcquisitionObservation) bool {
	if observation.Disposition < LeaseAcquisitionTemporaryUnavailable ||
		observation.Disposition > LeaseAcquisitionReady {
		return false
	}
	if observation.Disposition == LeaseAcquisitionPermanentFailure {
		return observation.PermanentFailure >= PreLeasePermanentStaleNodeGeneration &&
			observation.PermanentFailure <= PreLeasePermanentImmutableBinding
	}
	return observation.PermanentFailure == 0
}

func terminalReasonForPermanentFailure(failure PreLeasePermanentFailure) PreLeaseTerminalReason {
	switch failure {
	case PreLeasePermanentStaleNodeGeneration:
		return PreLeaseTerminalStaleNodeGeneration
	case PreLeasePermanentNodeIneligible:
		return PreLeaseTerminalNodeIneligible
	case PreLeasePermanentReservation:
		return PreLeaseTerminalReservation
	case PreLeasePermanentAuthorization:
		return PreLeaseTerminalAuthorization
	case PreLeasePermanentResourceClass:
		return PreLeaseTerminalResourceClass
	case PreLeasePermanentExecutionPolicy:
		return PreLeaseTerminalExecutionPolicy
	case PreLeasePermanentSchedulerPolicy:
		return PreLeaseTerminalSchedulerPolicy
	case PreLeasePermanentSchedulerEpoch:
		return PreLeaseTerminalSchedulerEpoch
	case PreLeasePermanentReleaseSafety:
		return PreLeaseTerminalReleaseSafety
	case PreLeasePermanentCatalogSafety:
		return PreLeaseTerminalCatalogSafety
	case PreLeasePermanentImmutableBinding:
		return PreLeaseTerminalImmutableBinding
	default:
		return PreLeaseTerminalNone
	}
}

func knownPreLeaseTerminalReason(reason PreLeaseTerminalReason) bool {
	return reason >= PreLeaseTerminalNone && reason <= PreLeaseTerminalRuntimeDeadline
}

func leaseAcquisitionRequest(record *runtimeRecord) LeaseAcquisitionRequest {
	return LeaseAcquisitionRequest{
		RuntimeRunID: record.fixture.RuntimeRunID,
		WorkItemID:   record.operation.WorkItemID, AdmissionGrantID: record.operation.AdmissionGrantID,
		GrantGeneration: record.operation.GrantGeneration, StartOperationID: record.acceptedStart.OperationID,
		StartDigest: record.acceptedStartDigest, OperationID: record.lease.AcquireOperationID,
		Digest: record.lease.AcquireDigest, ExecutionNodeID: record.operation.ExecutionNodeID,
		NodeCapacityGeneration: record.operation.NodeCapacityGeneration,
		ResourceClassID:        record.operation.ResourceClassID, ExecutionPolicyID: record.operation.ExecutionPolicyID,
		SchedulerEpoch: record.operation.SchedulerEpoch, PolicyVersion: record.operation.PolicyVersion,
		SafetyEpoch: record.fixture.SafetyEpoch, RuntimeFence: record.fixture.RuntimeFence,
		Deadline: record.deadline, LeaseAcquireBy: record.leaseAcquireBy,
	}
}

func validLeaseAcquisitionRequest(request LeaseAcquisitionRequest) bool {
	return validOpaqueID(request.RuntimeRunID.String()) && validOpaqueID(request.WorkItemID.String()) &&
		validOpaqueID(request.AdmissionGrantID.String()) && request.GrantGeneration > 0 &&
		validOpaqueID(request.StartOperationID.String()) && request.StartDigest != (Digest{}) &&
		validOpaqueID(request.OperationID.String()) && request.Digest != (Digest{}) &&
		validOpaqueID(request.ExecutionNodeID.String()) && request.NodeCapacityGeneration > 0 &&
		validOpaqueID(request.ResourceClassID.String()) && validOpaqueID(request.ExecutionPolicyID.String()) &&
		request.SchedulerEpoch > 0 && request.PolicyVersion > 0 && request.SafetyEpoch > 0 &&
		request.RuntimeFence > 0 && !request.Deadline.IsZero() && !request.LeaseAcquireBy.IsZero() &&
		!request.LeaseAcquireBy.After(request.Deadline)
}

func stablePreLeaseTerminalBinding(
	lease RuntimeLeaseSnapshot,
	outcome RuntimeOutcome,
	reason PreLeaseTerminalReason,
) (OperationID, Digest) {
	payload := []byte(fmt.Sprintf(
		"slidesmith.runtime-execution.pre-lease-terminal/v1\n%s\n%s\n%d\n%d",
		lease.AcquireOperationID.String(), lease.AcquireDigest.String(), outcome, reason,
	))
	digest := Digest(sha256.Sum256(payload))
	return OperationID{value: "prelease-terminal-" + fmt.Sprintf("%x", digest[:16])}, digest
}

func stablePostgresPreLeaseTerminalBinding(
	lease RuntimeLeaseSnapshot,
	outcome RuntimeOutcome,
	reason PreLeaseTerminalReason,
) (OperationID, Digest, []byte) {
	operationID, _ := stablePreLeaseTerminalBinding(lease, outcome, reason)
	canonical := []byte(fmt.Sprintf(
		"slidesmith.runtime-execution.pre-lease-terminal-command/v1\n%s\n%s\n%d\n%d",
		operationID.String(), lease.AcquireOperationID.String(), outcome, reason,
	))
	digest := Digest(sha256.Sum256(canonical))
	return operationID, digest, canonical
}
