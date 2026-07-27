package taskorchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

// TaskWorkspaceLifecyclePort is the exact subset of the completed C04 public
// Lifecycle contract used by Task Orchestration. taskworkspace.Lifecycle
// satisfies it without a wrapper or a new inspection surface.
type TaskWorkspaceLifecyclePort interface {
	CommitRuntimeView(context.Context, taskworkspace.CommitRuntimeViewRequest) (taskworkspace.CommitRuntimeViewResult, error)
	FenceRuntimeView(context.Context, taskworkspace.FenceRuntimeViewRequest) (taskworkspace.FenceRuntimeViewResult, error)
	InspectOperation(context.Context, taskworkspace.InspectOperationRequest) (taskworkspace.OperationInspection, error)
	ReconcileOperation(context.Context, taskworkspace.ReconcileOperationRequest) (taskworkspace.OperationInspection, error)
}

var _ TaskWorkspaceLifecyclePort = (taskworkspace.Lifecycle)(nil)

type TaskWorkspaceLifecycleAdapterBinding struct {
	Enactment          EnactmentRef
	Producer           EvidenceProducer
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	SafetyEpoch        SafetyEpoch
	Prerequisites      []EvidencePrerequisite
	Commit             *taskworkspace.CommitRuntimeViewRequest
	Fence              *taskworkspace.FenceRuntimeViewRequest
}

type TaskWorkspaceLifecycleEvidenceAdapter interface {
	Enact(context.Context, EnactmentRef) (TaskWorkspaceLifecycleAdapterEvidence, error)
}

type TaskWorkspaceLifecycleAdapterEvidence struct {
	SchemaVersion      EvidenceSchemaVersion
	Evidence           EvidenceRef
	Producer           EvidenceProducer
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	OperationID        OperationID
	ActivityGeneration ActivityGeneration
	Generation         TaskWorkspaceLifecycleGeneration
	Fence              TaskWorkspaceLifecycleFence
	ObservedGeneration TaskWorkspaceLifecycleGeneration
	ObservedFence      TaskWorkspaceLifecycleFence
	SafetyEpoch        SafetyEpoch
	Outcome            LifecycleEvidenceOutcome
	RevisionID         TaskWorkspaceRevisionID
	CheckpointID       CheckpointID
	CommitProofDigest  EvidenceDigest
	FenceProofDigest   EvidenceDigest
	Prerequisites      []EvidencePrerequisite
}

func (evidence TaskWorkspaceLifecycleAdapterEvidence) Intent(header IntentHeader) (TransitionIntent, error) {
	if header.TaskID != evidence.TaskID || header.ActivityGeneration != evidence.ActivityGeneration ||
		evidence.SchemaVersion.Major() != EvidenceSchemaV1.Major() ||
		!validEvidenceRef(evidence.Evidence) || evidence.Evidence.Kind != EvidenceTaskWorkspaceLifecycle ||
		!validOpaqueID(evidence.Producer.AuthorityID.String()) || evidence.Producer.Generation == 0 ||
		evidence.ObservedGeneration == 0 || evidence.ObservedFence <= evidence.Fence ||
		(evidence.Outcome == LifecycleEvidenceCommitted &&
			(evidence.CommitProofDigest == (EvidenceDigest{}) || evidence.FenceProofDigest != (EvidenceDigest{}))) ||
		(evidence.Outcome == LifecycleEvidenceFenced &&
			(evidence.CommitProofDigest != (EvidenceDigest{}) || evidence.FenceProofDigest == (EvidenceDigest{}))) {
		return nil, newDownstreamError(DownstreamCorruptEvidence)
	}
	authority := NewTaskWorkspaceLifecycleAuthority(
		evidence.Producer.AuthorityID, evidence.Producer.Generation,
	)
	return NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
		header,
		authority,
		TaskWorkspaceLifecycleEvidenceBinding{
			Evidence: evidence.Evidence, PhaseRunID: evidence.PhaseRunID,
			PhaseRunGeneration: evidence.PhaseRunGeneration, PhaseRunFence: evidence.PhaseRunFence,
			OperationID: evidence.OperationID, Generation: evidence.Generation, Fence: evidence.Fence,
			SafetyEpoch: evidence.SafetyEpoch, Outcome: evidence.Outcome,
			RevisionID: evidence.RevisionID, CheckpointID: evidence.CheckpointID,
		},
	), nil
}

type taskWorkspaceLifecycleEvidenceAdapter struct {
	mu          sync.Mutex
	port        TaskWorkspaceLifecyclePort
	binding     TaskWorkspaceLifecycleAdapterBinding
	fingerprint [32]byte
	completed   bool
	evidence    TaskWorkspaceLifecycleAdapterEvidence
}

func NewTaskWorkspaceLifecycleEvidenceAdapter(
	port TaskWorkspaceLifecyclePort,
	binding TaskWorkspaceLifecycleAdapterBinding,
) TaskWorkspaceLifecycleEvidenceAdapter {
	var fingerprint [32]byte
	if validateEnactmentRef(
		binding.Enactment, EnactmentTaskWorkspaceLifecycle, EnactmentFenceTaskWorkspaceLifecycle,
	) == nil {
		fingerprint = enactmentFingerprint(binding.Enactment)
	}
	return &taskWorkspaceLifecycleEvidenceAdapter{
		port: port, binding: cloneTaskWorkspaceLifecycleBinding(binding),
		fingerprint: fingerprint,
	}
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) Enact(
	ctx context.Context,
	ref EnactmentRef,
) (TaskWorkspaceLifecycleAdapterEvidence, error) {
	if failure := validateEnactmentRef(
		ref, EnactmentTaskWorkspaceLifecycle, EnactmentFenceTaskWorkspaceLifecycle,
	); failure != nil {
		return TaskWorkspaceLifecycleAdapterEvidence{}, failure
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if failure := validateTaskWorkspaceLifecycleBinding(adapter.binding); failure != nil {
		return TaskWorkspaceLifecycleAdapterEvidence{}, failure
	}
	if enactmentFingerprint(ref) != adapter.fingerprint ||
		adapter.binding.Enactment.OperationID != ref.OperationID {
		if adapter.binding.Enactment.OperationID == ref.OperationID {
			return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamIntegrityConflict)
		}
		return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamInvalidEnactment)
	}
	if adapter.completed {
		return cloneTaskWorkspaceLifecycleEvidence(adapter.evidence), nil
	}
	if adapter.port == nil {
		return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamDependencyUnavailable)
	}
	evidence, err := adapter.execute(ctx)
	if err != nil {
		return TaskWorkspaceLifecycleAdapterEvidence{}, err
	}
	adapter.completed = true
	adapter.evidence = evidence
	return cloneTaskWorkspaceLifecycleEvidence(evidence), nil
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) execute(
	ctx context.Context,
) (evidence TaskWorkspaceLifecycleAdapterEvidence, err error) {
	defer func() {
		if recover() != nil {
			evidence = TaskWorkspaceLifecycleAdapterEvidence{}
			err = newDownstreamError(DownstreamDependencyUnavailable)
		}
	}()
	if adapter.binding.Commit != nil {
		result, callErr := adapter.port.CommitRuntimeView(ctx, *adapter.binding.Commit)
		if callErr == nil {
			return adapter.commitEvidence(result)
		}
		return adapter.reconcile(ctx, callErr)
	}
	result, callErr := adapter.port.FenceRuntimeView(ctx, *adapter.binding.Fence)
	if callErr == nil {
		return adapter.fenceEvidence(result)
	}
	return adapter.reconcile(ctx, callErr)
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) reconcile(
	ctx context.Context,
	callErr error,
) (TaskWorkspaceLifecycleAdapterEvidence, error) {
	failure := normalizeTaskWorkspaceLifecycleError(callErr)
	if failure.Code() != DownstreamReconciliationRequired {
		return TaskWorkspaceLifecycleAdapterEvidence{}, failure
	}
	policyDomainID, taskID, operationID := adapter.c04Scope()
	inspection, inspectErr := adapter.port.InspectOperation(ctx, taskworkspace.InspectOperationRequest{
		PolicyDomainID: policyDomainID, TaskID: taskID, OperationID: operationID,
	})
	if inspectErr == nil && inspection.Disposition == taskworkspace.OperationTerminal {
		return adapter.inspectionEvidence(inspection)
	}
	inspection, reconcileErr := adapter.port.ReconcileOperation(ctx, taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: policyDomainID, TaskID: taskID, OperationID: operationID,
	})
	if reconcileErr != nil {
		return TaskWorkspaceLifecycleAdapterEvidence{}, normalizeTaskWorkspaceLifecycleError(reconcileErr)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal {
		return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamReconciliationRequired)
	}
	return adapter.inspectionEvidence(inspection)
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) inspectionEvidence(
	inspection taskworkspace.OperationInspection,
) (TaskWorkspaceLifecycleAdapterEvidence, error) {
	if adapter.binding.Commit != nil && inspection.CommitRuntimeView != nil {
		return adapter.commitEvidence(*inspection.CommitRuntimeView)
	}
	if adapter.binding.Fence != nil && inspection.FenceRuntimeView != nil {
		return adapter.fenceEvidence(*inspection.FenceRuntimeView)
	}
	return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) commitEvidence(
	result taskworkspace.CommitRuntimeViewResult,
) (TaskWorkspaceLifecycleAdapterEvidence, error) {
	request := adapter.binding.Commit
	if result.Operation.ID != request.Operation.ID || result.Operation.RequestDigest != request.Operation.RequestDigest ||
		result.Generation != request.Generation || result.PreviousFence != request.Fence || result.Fence <= request.Fence ||
		result.TaskWorkspaceID == "" || result.TaskWorkspaceID != request.TaskWorkspaceID ||
		result.RevisionID == "" || result.CheckpointID == "" ||
		result.BaseRevisionID != request.BaseRevisionID ||
		result.PredecessorRevisionID != request.ExpectedCurrentRevision ||
		result.ManifestDigest == "" || result.ManifestDigest != request.DeclaredStateManifest.Digest ||
		result.ValidationEvidenceID == "" || result.ValidationEvidenceID != request.ValidationEvidence.ID ||
		result.ValidationEvidenceDigest == "" || result.ValidationEvidenceDigest != request.ValidationEvidence.Digest ||
		result.ContentEvidenceRoot == "" || result.DurabilityEvidenceRoot == "" {
		return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
	}
	revisionID, revisionErr := NewTaskWorkspaceRevisionID(string(result.RevisionID))
	checkpointID, checkpointErr := NewCheckpointID(string(result.CheckpointID))
	if revisionErr != nil || checkpointErr != nil {
		return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
	}
	return adapter.evidenceFor(
		LifecycleEvidenceCommitted, revisionID, checkpointID,
		TaskWorkspaceLifecycleGeneration(request.Generation), TaskWorkspaceLifecycleFence(request.Fence),
		TaskWorkspaceLifecycleGeneration(result.Generation), TaskWorkspaceLifecycleFence(result.Fence),
		TaskWorkspaceCommitProofDigest(result), EvidenceDigest{},
	), nil
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) fenceEvidence(
	result taskworkspace.FenceRuntimeViewResult,
) (TaskWorkspaceLifecycleAdapterEvidence, error) {
	request := adapter.binding.Fence
	if result.Operation.ID != request.Operation.ID || result.Operation.RequestDigest != request.Operation.RequestDigest ||
		result.PreviousFence != request.Fence || result.Fence <= request.Fence ||
		result.TaskWorkspaceID == "" || result.TaskWorkspaceID != request.TaskWorkspaceID ||
		result.RuntimeViewID == "" || result.RuntimeViewID != request.RuntimeViewID ||
		result.BaseRevisionID != request.BaseRevisionID ||
		result.CurrentRevisionID != request.ExpectedCurrentRevision || result.Reason != request.Reason ||
		(request.Reason != taskworkspace.RuntimeViewRecoveryGenerationAdvanced && result.Generation != request.Generation) ||
		(request.Reason == taskworkspace.RuntimeViewRecoveryGenerationAdvanced && result.Generation != request.Generation+1) {
		return TaskWorkspaceLifecycleAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
	}
	return adapter.evidenceFor(
		LifecycleEvidenceFenced, TaskWorkspaceRevisionID{}, CheckpointID{},
		TaskWorkspaceLifecycleGeneration(request.Generation), TaskWorkspaceLifecycleFence(request.Fence),
		TaskWorkspaceLifecycleGeneration(result.Generation), TaskWorkspaceLifecycleFence(result.Fence),
		EvidenceDigest{}, TaskWorkspaceFenceProofDigest(result),
	), nil
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) evidenceFor(
	outcome LifecycleEvidenceOutcome,
	revisionID TaskWorkspaceRevisionID,
	checkpointID CheckpointID,
	generation TaskWorkspaceLifecycleGeneration,
	fence TaskWorkspaceLifecycleFence,
	observedGeneration TaskWorkspaceLifecycleGeneration,
	observedFence TaskWorkspaceLifecycleFence,
	commitProofDigest EvidenceDigest,
	fenceProofDigest EvidenceDigest,
) TaskWorkspaceLifecycleAdapterEvidence {
	binding := adapter.binding
	evidence := TaskWorkspaceLifecycleAdapterEvidence{
		SchemaVersion: EvidenceSchemaV1,
		Producer:      binding.Producer, TaskID: binding.TaskID, PhaseRunID: binding.PhaseRunID,
		PhaseRunGeneration: binding.PhaseRunGeneration, PhaseRunFence: binding.PhaseRunFence,
		OperationID: binding.Enactment.OperationID, ActivityGeneration: binding.Enactment.ActivityGeneration,
		Generation: generation, Fence: fence, ObservedGeneration: observedGeneration, ObservedFence: observedFence,
		SafetyEpoch: binding.SafetyEpoch,
		Outcome:     outcome, RevisionID: revisionID, CheckpointID: checkpointID,
		CommitProofDigest: commitProofDigest,
		FenceProofDigest:  fenceProofDigest,
		Prerequisites:     append([]EvidencePrerequisite(nil), binding.Prerequisites...),
	}
	evidenceID := lifecycleEvidenceID(binding.Enactment.OperationID)
	evidence.Evidence = NewEvidenceRef(
		evidenceID, EvidenceTaskWorkspaceLifecycle, taskWorkspaceLifecycleEvidenceDigest(evidence, evidenceID),
	)
	return evidence
}

func taskWorkspaceLifecycleEvidenceDigest(
	evidence TaskWorkspaceLifecycleAdapterEvidence,
	evidenceID EvidenceID,
) EvidenceDigest {
	prerequisites := canonicalEvidencePrerequisites(evidence.Prerequisites)
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation":   uint64(evidence.ActivityGeneration),
		"checkpoint_id":         evidence.CheckpointID.String(),
		"commit_proof_digest":   evidence.CommitProofDigest.String(),
		"evidence_id":           evidenceID.String(),
		"fence":                 uint64(evidence.Fence),
		"fence_proof_digest":    evidence.FenceProofDigest.String(),
		"generation":            uint64(evidence.Generation),
		"observed_fence":        uint64(evidence.ObservedFence),
		"observed_generation":   uint64(evidence.ObservedGeneration),
		"operation_id":          evidence.OperationID.String(),
		"outcome":               lifecycleEvidenceOutcomeName(evidence.Outcome),
		"phase_run_fence":       uint64(evidence.PhaseRunFence),
		"phase_run_generation":  uint64(evidence.PhaseRunGeneration),
		"phase_run_id":          evidence.PhaseRunID.String(),
		"prerequisites":         prerequisites,
		"producer_authority_id": evidence.Producer.AuthorityID.String(),
		"producer_generation":   uint64(evidence.Producer.Generation),
		"revision_id":           evidence.RevisionID.String(),
		"safety_epoch":          uint64(evidence.SafetyEpoch),
		"schema_version":        uint64(evidence.SchemaVersion),
		"task_id":               evidence.TaskID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

// TaskWorkspaceCommitProofDigest binds the complete public C04 commit result
// without exposing or reinterpreting C04's internal journal or storage model.
func TaskWorkspaceCommitProofDigest(result taskworkspace.CommitRuntimeViewResult) EvidenceDigest {
	encoded, _ := json.Marshal(result)
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

// TaskWorkspaceFenceProofDigest binds the complete public C04 fence result
// while Task Orchestration continues to compare the evidence against the
// original enactment generation and fence.
func TaskWorkspaceFenceProofDigest(result taskworkspace.FenceRuntimeViewResult) EvidenceDigest {
	encoded, _ := json.Marshal(result)
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

func lifecycleEvidenceID(operationID OperationID) EvidenceID {
	sum := sha256.Sum256([]byte(operationID.String()))
	return EvidenceID{value: "c04-evidence-" + hex.EncodeToString(sum[:12])}
}

func validateTaskWorkspaceLifecycleBinding(binding TaskWorkspaceLifecycleAdapterBinding) *DownstreamError {
	if failure := validateEnactmentRef(
		binding.Enactment, EnactmentTaskWorkspaceLifecycle, EnactmentFenceTaskWorkspaceLifecycle,
	); failure != nil {
		return failure
	}
	if !validOpaqueID(binding.Producer.AuthorityID.String()) || binding.Producer.Generation == 0 ||
		!validOpaqueID(binding.TaskID.String()) || !validOpaqueID(binding.PhaseRunID.String()) ||
		binding.PhaseRunGeneration == 0 || binding.PhaseRunFence == 0 || binding.SafetyEpoch == 0 ||
		(binding.Commit == nil) == (binding.Fence == nil) {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	for _, prerequisite := range binding.Prerequisites {
		if !validEvidenceRef(prerequisite.Evidence) || prerequisite.Generation == 0 || prerequisite.Fence == 0 {
			return newDownstreamError(DownstreamPrerequisitePending)
		}
	}
	expectedFence := taskworkspace.Fence(enactmentFenceValue(binding.Enactment.Fence))
	if binding.Commit != nil {
		request := binding.Commit
		if string(request.TaskID) != binding.TaskID.String() ||
			string(request.Operation.ID) != binding.Enactment.OperationID.String() ||
			request.Operation.RequestDigest == "" || request.Generation == 0 || request.Fence != expectedFence ||
			!validTaskWorkspaceCommitScope(binding, *request) {
			return newDownstreamError(DownstreamInvalidEnactment)
		}
		return nil
	}
	request := binding.Fence
	if string(request.TaskID) != binding.TaskID.String() ||
		string(request.Operation.ID) != binding.Enactment.OperationID.String() ||
		request.Operation.RequestDigest == "" || request.Generation == 0 || request.Fence != expectedFence ||
		!validTaskWorkspaceFenceScope(binding, *request) {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	return nil
}

func validTaskWorkspaceCommitScope(
	binding TaskWorkspaceLifecycleAdapterBinding,
	request taskworkspace.CommitRuntimeViewRequest,
) bool {
	lease := request.SandboxLeaseAuthority
	validation := request.ValidationEvidence
	return request.PolicyDomainID != "" && request.TaskWorkspaceID != "" && request.RuntimeViewID != "" &&
		request.RuntimeOperationID != "" && request.BaseRevisionID != "" && request.ExpectedCurrentRevision != "" &&
		request.Operation.RequestDigest == request.CanonicalRequestDigest() &&
		"sha256:"+binding.Enactment.PayloadDigest.String() == string(request.Operation.RequestDigest) &&
		validTaskWorkspaceLeaseScope(binding, request.PolicyDomainID, request.TaskID, request.TaskWorkspaceID,
			request.RuntimeViewID, request.RuntimeOperationID, lease) &&
		validation.ID != "" && validation.Digest == validation.CanonicalDigest() &&
		validation.ValidationAuthorityID != "" && validation.PolicyDomainID == request.PolicyDomainID &&
		validation.TaskID == request.TaskID && validation.TaskWorkspaceID == request.TaskWorkspaceID &&
		validation.RuntimeViewID == request.RuntimeViewID && validation.BaseRevisionID == request.BaseRevisionID &&
		string(validation.PhaseRunID) == binding.PhaseRunID.String() &&
		validation.RuntimeRunID == lease.RuntimeRunID && validation.RuntimeOperationID == request.RuntimeOperationID &&
		validation.SandboxLeaseAuthorityDigest == lease.Digest &&
		validation.ManifestDigest == request.DeclaredStateManifest.Digest &&
		validation.Generation == request.Generation && validation.Fence == request.Fence &&
		validation.Decision == taskworkspace.ValidationAccepted && request.DeclaredStateManifest.Digest != "" &&
		request.DeclaredStateManifest.Digest == request.DeclaredStateManifest.CanonicalDigest()
}

func validTaskWorkspaceFenceScope(
	binding TaskWorkspaceLifecycleAdapterBinding,
	request taskworkspace.FenceRuntimeViewRequest,
) bool {
	return request.PolicyDomainID != "" && request.TaskWorkspaceID != "" && request.RuntimeViewID != "" &&
		request.RuntimeOperationID != "" && request.BaseRevisionID != "" && request.ExpectedCurrentRevision != "" &&
		request.Reason != "" && request.Operation.RequestDigest == request.CanonicalRequestDigest() &&
		"sha256:"+binding.Enactment.PayloadDigest.String() == string(request.Operation.RequestDigest) &&
		validTaskWorkspaceLeaseScope(binding, request.PolicyDomainID, request.TaskID, request.TaskWorkspaceID,
			request.RuntimeViewID, request.RuntimeOperationID, request.SandboxLeaseAuthority)
}

func validTaskWorkspaceLeaseScope(
	binding TaskWorkspaceLifecycleAdapterBinding,
	policyDomainID taskworkspace.PolicyDomainID,
	taskID taskworkspace.TaskID,
	taskWorkspaceID taskworkspace.TaskWorkspaceID,
	runtimeViewID taskworkspace.RuntimeViewID,
	runtimeOperationID taskworkspace.OperationID,
	lease taskworkspace.SandboxLeaseAuthority,
) bool {
	return lease.ID != "" && lease.EvidenceID != "" && lease.AuthorityID != "" &&
		lease.Digest == lease.CanonicalDigest() && lease.PolicyDomainID == policyDomainID && lease.TaskID == taskID &&
		string(lease.PhaseRunID) == binding.PhaseRunID.String() && lease.RuntimeRunID != "" &&
		lease.RuntimeOperationID == runtimeOperationID && lease.EffectClass == taskworkspace.RuntimeViewMutating &&
		lease.LeaseGeneration != 0 && lease.LeaseFence != 0 && lease.ExpiresAt != 0 &&
		taskWorkspaceID != "" && runtimeViewID != ""
}

func normalizeTaskWorkspaceLifecycleError(err error) *DownstreamError {
	var failure *taskworkspace.Error
	if !errors.As(err, &failure) || failure == nil {
		return newDownstreamError(DownstreamDependencyUnavailable)
	}
	switch failure.SafeCategory() {
	case taskworkspace.SafeErrorAuthorizationDenied:
		return newDownstreamError(DownstreamUnauthorized)
	case taskworkspace.SafeErrorInvalidIntent:
		return newDownstreamError(DownstreamInvalidEnactment)
	case taskworkspace.SafeErrorIdempotencyConflict:
		return newDownstreamError(DownstreamIntegrityConflict)
	case taskworkspace.SafeErrorStaleRevisionGenerationFence:
		return newDownstreamError(DownstreamStale)
	case taskworkspace.SafeErrorTerminalConflict:
		return newDownstreamError(DownstreamStale)
	case taskworkspace.SafeErrorReconciliationRequired:
		return newDownstreamError(DownstreamReconciliationRequired)
	case taskworkspace.SafeErrorIntegrityUnavailableContent, taskworkspace.SafeErrorDurabilityUnverified:
		return newDownstreamError(DownstreamCorruptEvidence)
	default:
		return newDownstreamError(DownstreamDependencyUnavailable)
	}
}

func (adapter *taskWorkspaceLifecycleEvidenceAdapter) c04Scope() (
	taskworkspace.PolicyDomainID,
	taskworkspace.TaskID,
	taskworkspace.OperationID,
) {
	if adapter.binding.Commit != nil {
		return adapter.binding.Commit.PolicyDomainID, adapter.binding.Commit.TaskID,
			adapter.binding.Commit.Operation.ID
	}
	return adapter.binding.Fence.PolicyDomainID, adapter.binding.Fence.TaskID,
		adapter.binding.Fence.Operation.ID
}

func cloneTaskWorkspaceLifecycleBinding(
	binding TaskWorkspaceLifecycleAdapterBinding,
) TaskWorkspaceLifecycleAdapterBinding {
	binding.Prerequisites = append([]EvidencePrerequisite(nil), binding.Prerequisites...)
	if binding.Commit != nil {
		request := *binding.Commit
		binding.Commit = &request
	}
	if binding.Fence != nil {
		request := *binding.Fence
		binding.Fence = &request
	}
	return binding
}

func cloneTaskWorkspaceLifecycleEvidence(
	evidence TaskWorkspaceLifecycleAdapterEvidence,
) TaskWorkspaceLifecycleAdapterEvidence {
	evidence.Prerequisites = append([]EvidencePrerequisite(nil), evidence.Prerequisites...)
	return evidence
}
