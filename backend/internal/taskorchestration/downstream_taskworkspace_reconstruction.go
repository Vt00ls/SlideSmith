package taskorchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

// TaskWorkspaceReconstructionAdapterBinding binds one pre-Phase manual-edit
// reconstruction enactment to the exact opaque C04 recovery request selected
// by the Platform Control Plane.
type TaskWorkspaceReconstructionAdapterBinding struct {
	Enactment   EnactmentRef
	Producer    EvidenceProducer
	TaskID      TaskID
	SafetyEpoch SafetyEpoch
	Request     *taskworkspace.ReconstructTaskWorkspaceRequest
}

// TaskWorkspaceReconstructionRequestBinding is the opaque full C04 request
// admitted by the manual-edit decision. Its digest includes the authorized
// recovery intent, exact Artifact Version input, all immutable read-only
// capabilities, lifecycle lineage, expiry, mode, and stable OperationID.
type TaskWorkspaceReconstructionRequestBinding struct {
	operationID        OperationID
	payloadDigest      EnactmentPayloadDigest
	taskID             TaskID
	taskWorkspaceID    TaskWorkspaceID
	artifactVersionID  ArtifactVersionID
	expectedRevision   TaskWorkspaceRevisionID
	expectedCheckpoint CheckpointID
	generation         TaskWorkspaceLifecycleGeneration
	fence              TaskWorkspaceLifecycleFence
}

func NewTaskWorkspaceReconstructionRequestBinding(
	request taskworkspace.ReconstructTaskWorkspaceRequest,
) (TaskWorkspaceReconstructionRequestBinding, error) {
	intent := request.Intent
	if request.Operation.RequestDigest == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() ||
		intent.Digest == "" || intent.Digest != intent.CanonicalDigest() ||
		intent.Generation == 0 || intent.Fence == 0 {
		return TaskWorkspaceReconstructionRequestBinding{}, invalidIntentError()
	}
	operationID, operationErr := NewOperationID(string(request.Operation.ID))
	taskID, taskErr := NewTaskID(string(intent.TaskID))
	taskWorkspaceID, workspaceErr := NewTaskWorkspaceID(string(intent.TaskWorkspaceID))
	artifactVersionID, artifactErr := NewArtifactVersionID(string(intent.ArtifactVersionInput.ArtifactVersionID))
	expectedRevision, revisionErr := NewTaskWorkspaceRevisionID(string(intent.ExpectedCurrentRevisionID))
	expectedCheckpoint, checkpointErr := NewCheckpointID(string(intent.ExpectedCurrentCheckpointID))
	payloadDigest, digestErr := taskWorkspaceRequestPayloadDigest(request.Operation.RequestDigest)
	if operationErr != nil || taskErr != nil || workspaceErr != nil || artifactErr != nil ||
		revisionErr != nil || checkpointErr != nil || digestErr != nil {
		return TaskWorkspaceReconstructionRequestBinding{}, invalidIntentError()
	}
	binding := TaskWorkspaceReconstructionRequestBinding{
		operationID: operationID, payloadDigest: payloadDigest, taskID: taskID,
		taskWorkspaceID: taskWorkspaceID, artifactVersionID: artifactVersionID,
		expectedRevision: expectedRevision, expectedCheckpoint: expectedCheckpoint,
		generation: TaskWorkspaceLifecycleGeneration(intent.Generation),
		fence:      TaskWorkspaceLifecycleFence(intent.Fence),
	}
	requestCopy := request
	if failure := validateTaskWorkspaceReconstructionBinding(TaskWorkspaceReconstructionAdapterBinding{
		Enactment: exactTaskWorkspaceBindingEnactment(
			binding.operationID, binding.payloadDigest, binding.fence,
		),
		Producer: exactTaskWorkspaceBindingProducer(), TaskID: binding.taskID,
		SafetyEpoch: 1, Request: &requestCopy,
	}); failure != nil {
		return TaskWorkspaceReconstructionRequestBinding{}, invalidIntentError()
	}
	return binding, nil
}

func (binding TaskWorkspaceReconstructionRequestBinding) valid() bool {
	return validOpaqueID(binding.operationID.value) && binding.payloadDigest != (EnactmentPayloadDigest{}) &&
		validOpaqueID(binding.taskID.value) && validOpaqueID(binding.taskWorkspaceID.value) &&
		validOpaqueID(binding.artifactVersionID.value) && validOpaqueID(binding.expectedRevision.value) &&
		validOpaqueID(binding.expectedCheckpoint.value) && binding.generation > 0 && binding.fence > 0
}

func (binding TaskWorkspaceReconstructionRequestBinding) canonical() map[string]any {
	if !binding.valid() {
		return map[string]any{}
	}
	return map[string]any{
		"operation_id": binding.operationID.value, "payload_digest": binding.payloadDigest.String(),
		"generation": uint64(binding.generation), "fence": uint64(binding.fence),
	}
}

type TaskWorkspaceReconstructionEvidenceAdapter interface {
	Enact(context.Context, EnactmentRef) (TaskWorkspaceReconstructionAdapterEvidence, error)
}

type TaskWorkspaceReconstructionAdapterEvidence struct {
	SchemaVersion             EvidenceSchemaVersion
	Evidence                  EvidenceRef
	Producer                  EvidenceProducer
	TaskID                    TaskID
	OperationID               OperationID
	ActivityGeneration        ActivityGeneration
	ArtifactVersionID         ArtifactVersionID
	RevisionID                TaskWorkspaceRevisionID
	CheckpointID              CheckpointID
	Generation                TaskWorkspaceLifecycleGeneration
	Fence                     TaskWorkspaceLifecycleFence
	ObservedGeneration        TaskWorkspaceLifecycleGeneration
	ObservedFence             TaskWorkspaceLifecycleFence
	SafetyEpoch               SafetyEpoch
	ReconstructionProofDigest EvidenceDigest
}

func (evidence TaskWorkspaceReconstructionAdapterEvidence) Intent(
	header IntentHeader,
) (TransitionIntent, error) {
	if header.TaskID != evidence.TaskID || header.ActivityGeneration != evidence.ActivityGeneration ||
		evidence.SchemaVersion.Major() != EvidenceSchemaV1.Major() ||
		!validEvidenceRef(evidence.Evidence) || evidence.Evidence.Kind != EvidenceTaskWorkspaceLifecycle ||
		!validOpaqueID(evidence.Producer.AuthorityID.String()) || evidence.Producer.Generation == 0 ||
		!validOpaqueID(evidence.OperationID.String()) || !validOpaqueID(evidence.ArtifactVersionID.String()) ||
		!validOpaqueID(evidence.RevisionID.String()) || !validOpaqueID(evidence.CheckpointID.String()) ||
		evidence.Generation == 0 || evidence.Fence == 0 ||
		evidence.ObservedGeneration <= evidence.Generation || evidence.ObservedFence <= evidence.Fence ||
		evidence.SafetyEpoch == 0 || evidence.ReconstructionProofDigest == (EvidenceDigest{}) {
		return nil, newDownstreamError(DownstreamCorruptEvidence)
	}
	authority := NewTaskWorkspaceLifecycleAuthority(
		evidence.Producer.AuthorityID, evidence.Producer.Generation,
	)
	return NewAcceptTaskWorkspaceReconstructionEvidenceIntent(
		header, authority, TaskWorkspaceReconstructionEvidenceBinding{
			Evidence: evidence.Evidence, OperationID: evidence.OperationID,
			ArtifactVersionID: evidence.ArtifactVersionID,
			RevisionID:        evidence.RevisionID, CheckpointID: evidence.CheckpointID,
			Generation: evidence.Generation, Fence: evidence.Fence,
			ObservedGeneration: evidence.ObservedGeneration, ObservedFence: evidence.ObservedFence,
			SafetyEpoch: evidence.SafetyEpoch,
		},
	), nil
}

type taskWorkspaceReconstructionEvidenceAdapter struct {
	mu          sync.Mutex
	port        TaskWorkspaceLifecyclePort
	binding     TaskWorkspaceReconstructionAdapterBinding
	fingerprint [32]byte
	completed   bool
	evidence    TaskWorkspaceReconstructionAdapterEvidence
}

func NewTaskWorkspaceReconstructionEvidenceAdapter(
	port TaskWorkspaceLifecyclePort,
	binding TaskWorkspaceReconstructionAdapterBinding,
) TaskWorkspaceReconstructionEvidenceAdapter {
	var fingerprint [32]byte
	if validateEnactmentRef(
		binding.Enactment, EnactmentTaskWorkspaceLifecycle, EnactmentFenceTaskWorkspaceLifecycle,
	) == nil {
		fingerprint = enactmentFingerprint(binding.Enactment)
	}
	return &taskWorkspaceReconstructionEvidenceAdapter{
		port: port, binding: cloneTaskWorkspaceReconstructionBinding(binding), fingerprint: fingerprint,
	}
}

func (adapter *taskWorkspaceReconstructionEvidenceAdapter) Enact(
	ctx context.Context,
	ref EnactmentRef,
) (TaskWorkspaceReconstructionAdapterEvidence, error) {
	if failure := validateEnactmentRef(
		ref, EnactmentTaskWorkspaceLifecycle, EnactmentFenceTaskWorkspaceLifecycle,
	); failure != nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, failure
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if failure := validateTaskWorkspaceReconstructionBinding(adapter.binding); failure != nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, failure
	}
	if enactmentFingerprint(ref) != adapter.fingerprint ||
		adapter.binding.Enactment.OperationID != ref.OperationID {
		if adapter.binding.Enactment.OperationID == ref.OperationID {
			return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamIntegrityConflict)
		}
		return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamInvalidEnactment)
	}
	if adapter.completed {
		return adapter.evidence, nil
	}
	if adapter.port == nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamDependencyUnavailable)
	}
	evidence, err := adapter.execute(ctx)
	if err != nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, err
	}
	adapter.completed = true
	adapter.evidence = evidence
	return evidence, nil
}

func (adapter *taskWorkspaceReconstructionEvidenceAdapter) execute(
	ctx context.Context,
) (evidence TaskWorkspaceReconstructionAdapterEvidence, err error) {
	defer func() {
		if recover() != nil {
			evidence = TaskWorkspaceReconstructionAdapterEvidence{}
			err = newDownstreamError(DownstreamDependencyUnavailable)
		}
	}()
	result, callErr := adapter.port.ReconstructTaskWorkspace(ctx, *adapter.binding.Request)
	if callErr == nil {
		return adapter.resultEvidence(result)
	}
	failure := normalizeTaskWorkspaceLifecycleError(callErr)
	if failure.Code() != DownstreamReconciliationRequired {
		return TaskWorkspaceReconstructionAdapterEvidence{}, failure
	}
	request := adapter.binding.Request
	inspection, inspectErr := adapter.port.InspectOperation(ctx, taskworkspace.InspectOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID,
		TaskID:         request.Intent.TaskID,
		OperationID:    request.Operation.ID,
	})
	if inspectErr == nil && inspection.Disposition == taskworkspace.OperationTerminal {
		return adapter.inspectionEvidence(inspection)
	}
	inspection, reconcileErr := adapter.port.ReconcileOperation(ctx, taskworkspace.ReconcileOperationRequest{
		PolicyDomainID: request.Intent.PolicyDomainID,
		TaskID:         request.Intent.TaskID,
		OperationID:    request.Operation.ID,
	})
	if reconcileErr != nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, normalizeTaskWorkspaceLifecycleError(reconcileErr)
	}
	if inspection.Disposition != taskworkspace.OperationTerminal {
		return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamReconciliationRequired)
	}
	return adapter.inspectionEvidence(inspection)
}

func (adapter *taskWorkspaceReconstructionEvidenceAdapter) inspectionEvidence(
	inspection taskworkspace.OperationInspection,
) (TaskWorkspaceReconstructionAdapterEvidence, error) {
	if inspection.ReconstructTaskWorkspace == nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
	}
	return adapter.resultEvidence(*inspection.ReconstructTaskWorkspace)
}

func (adapter *taskWorkspaceReconstructionEvidenceAdapter) resultEvidence(
	result taskworkspace.ReconstructTaskWorkspaceResult,
) (TaskWorkspaceReconstructionAdapterEvidence, error) {
	request := adapter.binding.Request
	intent := request.Intent
	artifact := intent.ArtifactVersionInput
	artifactEvidence := result.ArtifactReconstructionEvidence
	if result.Operation != request.Operation || result.TaskWorkspaceID != intent.TaskWorkspaceID ||
		result.MaterializationID == "" || result.CurrentRevisionID != intent.ExpectedCurrentRevisionID ||
		result.CurrentCheckpointID != intent.ExpectedCurrentCheckpointID ||
		result.ArtifactVersionID != artifact.ArtifactVersionID ||
		result.ArtifactManifestDigest != artifact.ManifestDigest ||
		result.PublicationAuthorityID != intent.PublicationAuthorityID ||
		result.Generation != intent.Generation+1 || result.PreviousFence != intent.Fence ||
		result.Fence != intent.Fence+1 || result.RecoveryIntentID != intent.ID ||
		artifactEvidence.ID == "" || artifactEvidence.Digest == "" ||
		artifactEvidence.Digest != artifactEvidence.CanonicalDigest() ||
		artifactEvidence.PublicationAuthorityID != artifact.PublicationAuthorityID ||
		artifactEvidence.PolicyDomainID != intent.PolicyDomainID || artifactEvidence.TaskID != intent.TaskID ||
		artifactEvidence.ArtifactVersionID != artifact.ArtifactVersionID ||
		artifactEvidence.ManifestDigest != artifact.ManifestDigest ||
		artifactEvidence.InputCapabilityID != artifact.ID || artifactEvidence.ContentEvidenceRoot == "" ||
		artifactEvidence.Decision != taskworkspace.ReconstructionInputVerified ||
		artifactEvidence.RecoveryIntentID != intent.ID || artifactEvidence.Generation != result.Generation ||
		artifactEvidence.Fence != result.Fence || artifactEvidence.OperationID != request.Operation.ID ||
		!taskWorkspaceReconstructionReadOnlyInputsMatch(
			result.ReadOnlyInputs, intent.ReadOnlyInputs, result.Generation, result.Fence,
		) {
		return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
	}
	revisionID, revisionErr := NewTaskWorkspaceRevisionID(string(result.CurrentRevisionID))
	checkpointID, checkpointErr := NewCheckpointID(string(result.CurrentCheckpointID))
	artifactVersionID, artifactErr := NewArtifactVersionID(string(result.ArtifactVersionID))
	if revisionErr != nil || checkpointErr != nil || artifactErr != nil {
		return TaskWorkspaceReconstructionAdapterEvidence{}, newDownstreamError(DownstreamCorruptEvidence)
	}
	evidence := TaskWorkspaceReconstructionAdapterEvidence{
		SchemaVersion: EvidenceSchemaV1, Producer: adapter.binding.Producer,
		TaskID: adapter.binding.TaskID, OperationID: adapter.binding.Enactment.OperationID,
		ActivityGeneration: adapter.binding.Enactment.ActivityGeneration,
		ArtifactVersionID:  artifactVersionID, RevisionID: revisionID, CheckpointID: checkpointID,
		Generation:                TaskWorkspaceLifecycleGeneration(intent.Generation),
		Fence:                     TaskWorkspaceLifecycleFence(intent.Fence),
		ObservedGeneration:        TaskWorkspaceLifecycleGeneration(result.Generation),
		ObservedFence:             TaskWorkspaceLifecycleFence(result.Fence),
		SafetyEpoch:               adapter.binding.SafetyEpoch,
		ReconstructionProofDigest: TaskWorkspaceReconstructionProofDigest(result),
	}
	evidenceID := taskWorkspaceReconstructionEvidenceID(evidence.OperationID)
	evidence.Evidence = NewEvidenceRef(
		evidenceID, EvidenceTaskWorkspaceLifecycle,
		taskWorkspaceReconstructionEvidenceDigest(evidence, evidenceID),
	)
	return evidence, nil
}

func validateTaskWorkspaceReconstructionBinding(
	binding TaskWorkspaceReconstructionAdapterBinding,
) *DownstreamError {
	if failure := validateEnactmentRef(
		binding.Enactment, EnactmentTaskWorkspaceLifecycle, EnactmentFenceTaskWorkspaceLifecycle,
	); failure != nil {
		return failure
	}
	if !validOpaqueID(binding.Producer.AuthorityID.String()) || binding.Producer.Generation == 0 ||
		!validOpaqueID(binding.TaskID.String()) || binding.SafetyEpoch == 0 || binding.Request == nil {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	request := binding.Request
	intent := request.Intent
	artifact := intent.ArtifactVersionInput
	expectedFence, ok := binding.Enactment.Fence.(TaskWorkspaceLifecycleFence)
	if !ok || string(intent.TaskID) != binding.TaskID.String() ||
		string(request.Operation.ID) != binding.Enactment.OperationID.String() ||
		request.Operation.RequestDigest == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() ||
		intent.ID == "" || intent.Digest == "" || intent.Digest != intent.CanonicalDigest() ||
		intent.RecoveryAuthorityID == "" || intent.PolicyDomainID == "" || intent.TaskWorkspaceID == "" ||
		intent.TargetKind != taskworkspace.RecoveryTargetArtifactVersion ||
		intent.ExpectedCurrentRevisionID == "" || intent.ExpectedCurrentCheckpointID == "" ||
		intent.TargetRevisionID != "" || intent.TargetCheckpointID != "" ||
		intent.PublicationAuthorityID == "" || intent.Generation == 0 || intent.Fence == 0 ||
		uint64(expectedFence) != uint64(intent.Fence) || intent.Mode != taskworkspace.RecoveryModeWritable ||
		intent.ExpiresAt == 0 || artifact.ID == "" || artifact.Digest == "" ||
		artifact.Digest != artifact.CanonicalDigest() || artifact.PublicationAuthorityID != intent.PublicationAuthorityID ||
		artifact.PolicyDomainID != intent.PolicyDomainID || artifact.TaskID != intent.TaskID ||
		artifact.ArtifactVersionID == "" || artifact.ManifestDigest == "" || artifact.ExpiresAt == 0 ||
		binding.Enactment.PayloadDigest != TaskWorkspaceReconstructionEnactmentPayloadDigest(*request) {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	return nil
}

func taskWorkspaceReconstructionReadOnlyInputsMatch(
	materializations []taskworkspace.ReadOnlyInputMaterialization,
	capabilities []taskworkspace.ReadOnlyInputCapability,
	generation taskworkspace.Generation,
	fence taskworkspace.Fence,
) bool {
	if len(materializations) != len(capabilities) {
		return false
	}
	seenMaterializations := make(map[taskworkspace.ReadOnlyInputMaterializationID]struct{}, len(materializations))
	seenEvidence := make(map[taskworkspace.EvidenceID]struct{}, len(materializations))
	for index, materialization := range materializations {
		capability := capabilities[index]
		if materialization.ID == "" || materialization.Digest == "" ||
			materialization.Digest != materialization.CanonicalDigest() ||
			materialization.CapabilityID != capability.ID || materialization.Kind != capability.Kind ||
			materialization.InputID != capability.InputID ||
			materialization.ManifestDigest != capability.ManifestDigest || materialization.EvidenceID == "" ||
			materialization.Access != taskworkspace.InputAccessReadOnly ||
			materialization.Generation != generation || materialization.Fence != fence {
			return false
		}
		if _, duplicate := seenMaterializations[materialization.ID]; duplicate {
			return false
		}
		if _, duplicate := seenEvidence[materialization.EvidenceID]; duplicate {
			return false
		}
		seenMaterializations[materialization.ID] = struct{}{}
		seenEvidence[materialization.EvidenceID] = struct{}{}
	}
	return true
}

func cloneTaskWorkspaceReconstructionBinding(
	binding TaskWorkspaceReconstructionAdapterBinding,
) TaskWorkspaceReconstructionAdapterBinding {
	if binding.Request != nil {
		request := *binding.Request
		request.Intent.ReadOnlyInputs = append(
			[]taskworkspace.ReadOnlyInputCapability(nil), request.Intent.ReadOnlyInputs...,
		)
		binding.Request = &request
	}
	return binding
}

// TaskWorkspaceReconstructionEnactmentPayloadDigest binds only opaque public
// identities shared by Task Orchestration and C04. It never exposes physical
// materialization, path, session, mount, or storage detail.
func TaskWorkspaceReconstructionEnactmentPayloadDigest(
	request taskworkspace.ReconstructTaskWorkspaceRequest,
) EnactmentPayloadDigest {
	if request.Operation.RequestDigest == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return EnactmentPayloadDigest{}
	}
	digest, err := taskWorkspaceRequestPayloadDigest(request.Operation.RequestDigest)
	if err != nil {
		return EnactmentPayloadDigest{}
	}
	return digest
}

func TaskWorkspaceReconstructionProofDigest(
	result taskworkspace.ReconstructTaskWorkspaceResult,
) EvidenceDigest {
	encoded, _ := json.Marshal(result)
	return EvidenceDigest(sha256.Sum256(encoded))
}

func taskWorkspaceReconstructionEvidenceDigest(
	evidence TaskWorkspaceReconstructionAdapterEvidence,
	evidenceID EvidenceID,
) EvidenceDigest {
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation": uint64(evidence.ActivityGeneration),
		"artifact_version_id": evidence.ArtifactVersionID.String(),
		"checkpoint_id":       evidence.CheckpointID.String(),
		"evidence_id":         evidenceID.String(), "fence": uint64(evidence.Fence),
		"generation": uint64(evidence.Generation), "observed_fence": uint64(evidence.ObservedFence),
		"observed_generation":         uint64(evidence.ObservedGeneration),
		"operation_id":                evidence.OperationID.String(),
		"producer_authority_id":       evidence.Producer.AuthorityID.String(),
		"producer_generation":         uint64(evidence.Producer.Generation),
		"reconstruction_proof_digest": evidence.ReconstructionProofDigest.String(),
		"revision_id":                 evidence.RevisionID.String(), "safety_epoch": uint64(evidence.SafetyEpoch),
		"schema_version": uint64(evidence.SchemaVersion), "task_id": evidence.TaskID.String(),
	})
	return EvidenceDigest(sha256.Sum256(encoded))
}

func taskWorkspaceReconstructionEvidenceID(operationID OperationID) EvidenceID {
	digest := sha256.Sum256([]byte("task-workspace-reconstruction|" + operationID.String()))
	return EvidenceID{value: "c04-reconstruction-evidence-" + hex.EncodeToString(digest[:12])}
}
