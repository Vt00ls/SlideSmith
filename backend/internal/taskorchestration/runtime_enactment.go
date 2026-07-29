package taskorchestration

import (
	"crypto/sha256"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
)

const runtimeEnactmentDeadline = 30 * time.Minute

func canonicalRuntimeStartEnactment(
	record taskRecord,
	phase PhaseDefinition,
	run phaseRunRecord,
	runtimeRunID RuntimeRunID,
	operationID OperationID,
	header IntentHeader,
	decisionID DecisionID,
) (runtimeRunRecord, EnactmentRef, error) {
	taskID := record.latestDecision.TaskProjection.TaskID
	if !validOpaqueID(taskID.value) {
		taskID = header.TaskID
	}
	workspaceID, err := runtimeexecution.NewPersonalWorkspaceID("personal-workspace-" + taskID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	runtimeTaskID, err := runtimeexecution.NewTaskID(taskID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	runtimePhaseRunID, err := runtimeexecution.NewPhaseRunID(run.id.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	runtimeID, err := runtimeexecution.NewRuntimeRunID(runtimeRunID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	runtimeOperationID, err := runtimeexecution.NewOperationID(operationID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	authorityID, err := runtimeexecution.NewAuthorityID("task-orchestration")
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	runtimeBindingID, err := runtimeexecution.NewRuntimeBindingID("runtime-binding-" + runtimeRunID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	resourceClassID, err := runtimeexecution.NewResourceClassID("resource-class-" + record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	executionPolicyID, err := runtimeexecution.NewExecutionPolicyID("execution-policy-" + record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	networkPolicyID, err := runtimeexecution.NewNetworkPolicyID("network-policy-" + record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	secretPolicyID, err := runtimeexecution.NewSecretPolicyID("secret-policy-" + record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}

	effect := runtimeexecution.EffectReadOnly
	var viewRequirement *runtimeexecution.RuntimeViewRequirement
	if phase.Kind == PhaseMutating {
		effect = runtimeexecution.EffectMutating
		workspace, workspaceErr := runtimeexecution.NewTaskWorkspaceID(record.aggregate.pinned.TaskWorkspaceID.value)
		materialization, materializationErr := runtimeexecution.NewTaskWorkspaceMaterializationID("materialization-" + runtimeRunID.value)
		baseRevisionValue := record.latestRevisionID.value
		if baseRevisionValue == "" {
			baseRevisionValue = "base-revision-" + taskID.value
		}
		baseRevision, baseRevisionErr := runtimeexecution.NewTaskWorkspaceRevisionID(baseRevisionValue)
		if workspaceErr != nil || materializationErr != nil || baseRevisionErr != nil {
			return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
		}
		generation := record.aggregate.lifecycleGeneration
		if generation == 0 {
			generation = 1
		}
		fence := record.aggregate.lifecycleFence
		if fence == 0 {
			fence = 1
		}
		viewRequirement = &runtimeexecution.RuntimeViewRequirement{
			TaskWorkspaceID: workspace, MaterializationID: materialization,
			BaseRevisionID: baseRevision, LifecycleGeneration: runtimeexecution.TaskWorkspaceLifecycleGeneration(generation),
			LifecycleFence:          runtimeexecution.TaskWorkspaceLifecycleFence(fence),
			ExpiryPolicy:            runtimeexecution.RuntimeViewExpiryAtDeadline,
			OpenOperationDerivation: runtimeEnactmentDigest("runtime-view-open", operationID.value),
		}
	}

	start, err := runtimeexecution.NewCanonicalStartRuntimeRun(runtimeexecution.StartRuntimeRunInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: runtimeOperationID,
		PersonalWorkspaceID: workspaceID, TaskID: runtimeTaskID, PhaseRunID: runtimePhaseRunID,
		RuntimeRunID: runtimeID, Attempt: run.attempt,
		ExpectedTaskRevision:    runtimeexecution.TaskRevision(record.revision + 1),
		ExpectedRuntimeRevision: 1, ExpectedOperationGeneration: 1, ExpectedRuntimeFence: 1,
		Authority:                   runtimeexecution.NewTaskOrchestrationAuthority(authorityID, 1),
		RuntimeBindingID:            runtimeBindingID,
		RuntimeBindingDigest:        runtimeEnactmentDigest("runtime-binding", runtimeRunID.value, record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value),
		ExecutionLockDigest:         runtimeEnactmentDigest("execution-lock", record.aggregate.pinned.ExecutionLock.ID.value),
		CapabilityContractDigest:    runtimeEnactmentDigest("capability-contract", phase.Key.value),
		AllowedPlatformImagesDigest: runtimeEnactmentDigest("platform-images", record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value),
		ExecutorContractDigest:      runtimeEnactmentDigest("executor-contract", record.aggregate.pinned.ExecutionLock.RuntimeReleaseID.value),
		ReleaseSafetyEpoch:          runtimeexecution.ReleaseSafetyEpoch(record.safetyEpoch),
		WorkerClass:                 runtimeexecution.WorkerAgent, Effect: effect,
		OutputContractDigest:   runtimeEnactmentDigest("output-contract", phase.Key.value),
		EvidenceContractDigest: runtimeEnactmentDigest("evidence-contract", phase.Key.value),
		RuntimeViewRequirement: viewRequirement, ResourceClassID: resourceClassID,
		ExecutionPolicyID: executionPolicyID, ProviderCapability: runtimeexecution.ProviderCapabilityNone,
		NetworkPolicyID: networkPolicyID, SecretPolicyID: secretPolicyID,
		Deadline:           header.OccurredAt.UTC().Add(runtimeEnactmentDeadline),
		CancellationPolicy: runtimeexecution.CancellationFenceFirst,
	})
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	payload, err := runtimeexecution.CanonicalStartPayload(start)
	if err != nil {
		return runtimeRunRecord{}, EnactmentRef{}, invalidIntentError()
	}
	payloadDigest := EnactmentPayloadDigest(start.CanonicalRequestDigest)
	runtimeRun := runtimeRunRecord{
		id: runtimeRunID, operationID: operationID, outcome: RuntimeRunPending,
		canonicalPayload: append([]byte(nil), payload...), payloadDigest: payloadDigest,
	}
	return runtimeRun, EnactmentRef{
		OperationID: operationID, Kind: EnactmentRuntimeExecution, PayloadDigest: payloadDigest,
		ActivityGeneration: header.ActivityGeneration, Fence: RuntimeFence(run.fence),
		CausationID: CausationID{value: "causation-" + decisionID.value},
	}, nil
}

func runtimeEnactmentDigest(domain string, values ...string) runtimeexecution.Digest {
	payload := "slidesmith.task-orchestration." + domain + "/v1\n" + strings.Join(values, "\n")
	return runtimeexecution.Digest(sha256.Sum256([]byte(payload)))
}

func canonicalRuntimeCancelEnactment(
	startBinding runtimeOperationBinding,
	taskID TaskID,
	operationID OperationID,
	reason CancelReason,
	occurredAt time.Time,
) ([]byte, EnactmentPayloadDigest, error) {
	if len(startBinding.canonicalPayload) == 0 || startBinding.payloadDigest == (EnactmentPayloadDigest{}) {
		return nil, EnactmentPayloadDigest{}, invalidIntentError()
	}
	start, err := runtimeexecution.ParseCanonicalStartPayload(
		startBinding.canonicalPayload, runtimeexecution.Digest(startBinding.payloadDigest),
	)
	if err != nil || start.TaskID.String() != taskID.value ||
		start.PhaseRunID.String() != startBinding.phaseRunID.value ||
		start.RuntimeRunID.String() != startBinding.runtimeRunID.value {
		return nil, EnactmentPayloadDigest{}, newError(ErrorIntegrityConflict)
	}
	runtimeOperationID, err := runtimeexecution.NewOperationID(operationID.value)
	if err != nil {
		return nil, EnactmentPayloadDigest{}, invalidIntentError()
	}
	cancellationReason := runtimeexecution.CancellationUserRequested
	if reason == CancelReasonSafety {
		cancellationReason = runtimeexecution.CancellationAdministratorRequested
	}
	cancel, err := runtimeexecution.NewCancelRuntimeRun(runtimeexecution.CancelRuntimeRunInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: runtimeOperationID,
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
		ExpectedRuntimeRevision:     start.ExpectedRuntimeRevision + 1,
		ExpectedStartOperationID:    start.OperationID,
		ExpectedOperationGeneration: start.ExpectedOperationGeneration + 1,
		ExpectedRuntimeFence:        start.ExpectedRuntimeFence + 1,
		Authority:                   start.Authority, Reason: cancellationReason,
		SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: occurredAt.UTC(),
	})
	if err != nil {
		return nil, EnactmentPayloadDigest{}, invalidIntentError()
	}
	payload, err := runtimeexecution.CanonicalCancelPayload(cancel)
	if err != nil {
		return nil, EnactmentPayloadDigest{}, invalidIntentError()
	}
	return payload, EnactmentPayloadDigest(cancel.CanonicalRequestDigest), nil
}
