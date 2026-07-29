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
