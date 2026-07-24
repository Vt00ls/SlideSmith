package taskworkspace

import (
	"context"
	"errors"
	"reflect"
)

type (
	RecoveryIntentID    string
	RecoveryAuthorityID string
	RecoveryTargetKind  string
	RecoveryMode        string
)

const (
	RecoveryTargetCheckpoint      RecoveryTargetKind = "checkpoint"
	RecoveryTargetArtifactVersion RecoveryTargetKind = "artifact_version"

	RecoveryModeWritable         RecoveryMode = "writable"
	RecoveryModeDegradedReadOnly RecoveryMode = "recovery_degraded_read_only"
)

type AuthorizedRecoveryIntent struct {
	ID                          RecoveryIntentID
	Digest                      Digest
	RecoveryAuthorityID         RecoveryAuthorityID
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	TargetKind                  RecoveryTargetKind
	ExpectedCurrentRevisionID   RevisionID
	ExpectedCurrentCheckpointID CheckpointID
	TargetRevisionID            RevisionID
	TargetCheckpointID          CheckpointID
	ArtifactVersionInput        ArtifactVersionReconstructionInput
	ReadOnlyInputs              []ReadOnlyInputCapability
	PublicationAuthorityID      PublicationAuthorityID
	Generation                  Generation
	Fence                       Fence
	Mode                        RecoveryMode
	ExpiresAt                   Instant
}

func (i AuthorizedRecoveryIntent) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                          RecoveryIntentID
		RecoveryAuthorityID         RecoveryAuthorityID
		PolicyDomainID              PolicyDomainID
		TaskID                      TaskID
		TaskWorkspaceID             TaskWorkspaceID
		TargetKind                  RecoveryTargetKind
		ExpectedCurrentRevisionID   RevisionID
		ExpectedCurrentCheckpointID CheckpointID
		TargetRevisionID            RevisionID
		TargetCheckpointID          CheckpointID
		ArtifactVersionInput        ArtifactVersionReconstructionInput
		ReadOnlyInputs              []ReadOnlyInputCapability
		PublicationAuthorityID      PublicationAuthorityID
		Generation                  Generation
		Fence                       Fence
		Mode                        RecoveryMode
		ExpiresAt                   Instant
	}{
		ID:                          i.ID,
		RecoveryAuthorityID:         i.RecoveryAuthorityID,
		PolicyDomainID:              i.PolicyDomainID,
		TaskID:                      i.TaskID,
		TaskWorkspaceID:             i.TaskWorkspaceID,
		TargetKind:                  i.TargetKind,
		ExpectedCurrentRevisionID:   i.ExpectedCurrentRevisionID,
		ExpectedCurrentCheckpointID: i.ExpectedCurrentCheckpointID,
		TargetRevisionID:            i.TargetRevisionID,
		TargetCheckpointID:          i.TargetCheckpointID,
		ArtifactVersionInput:        i.ArtifactVersionInput,
		ReadOnlyInputs:              append([]ReadOnlyInputCapability(nil), i.ReadOnlyInputs...),
		PublicationAuthorityID:      i.PublicationAuthorityID,
		Generation:                  i.Generation,
		Fence:                       i.Fence,
		Mode:                        i.Mode,
		ExpiresAt:                   i.ExpiresAt,
	})
}

type RestoreTaskWorkspaceRequest struct {
	Intent    AuthorizedRecoveryIntent
	Operation Operation
}

func (r RestoreTaskWorkspaceRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind        string
		Intent      AuthorizedRecoveryIntent
		OperationID OperationID
	}{
		Kind:        "restore_task_workspace",
		Intent:      r.Intent,
		OperationID: r.Operation.ID,
	})
}

type RestoreTaskWorkspaceResult struct {
	TaskWorkspaceID        TaskWorkspaceID
	MaterializationID      MaterializationID
	RevisionID             RevisionID
	CheckpointID           CheckpointID
	CurrentRevisionID      RevisionID
	CurrentCheckpointID    CheckpointID
	ManifestDigest         Digest
	ContentEvidenceRoot    EvidenceRoot
	DurabilityEvidenceRoot EvidenceRoot
	CheckpointEvidence     CheckpointEvidence
	ReadOnlyInputs         []ReadOnlyInputMaterialization
	Generation             Generation
	PreviousFence          Fence
	Fence                  Fence
	RecoveryIntentID       RecoveryIntentID
	Operation              Operation
}

func (m *inMemory) RestoreTaskWorkspace(
	ctx context.Context,
	request RestoreTaskWorkspaceRequest,
) (RestoreTaskWorkspaceResult, error) {
	intent := cloneAuthorizedRecoveryIntent(request.Intent)
	if !authorizedCheckpointRestoreIntentIsCanonical(intent) ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return RestoreTaskWorkspaceResult{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{intent.PolicyDomainID, intent.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[RestoreTaskWorkspaceResult](
		m.operations, scope, request.Operation,
	); replayed {
		return result, err
	}
	trustedRequest := RestoreTaskWorkspaceRequest{Intent: intent, Operation: request.Operation}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, trustedRequest, restoreTaskWorkspaceJournalSpec(), nil,
	); err != nil {
		return RestoreTaskWorkspaceResult{}, err
	}
	fail := func(code ErrorCode) (RestoreTaskWorkspaceResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, RestoreTaskWorkspaceResult{}, err)
		return RestoreTaskWorkspaceResult{}, err
	}
	currentIntent, intentOK := m.currentRecoveryIntent(intent.ID)
	if !intentOK || !sameAuthorizedRecoveryIntent(currentIntent, intent) || intent.RecoveryAuthorityID != m.recoveryAuthorityID ||
		intent.ExpiresAt <= m.now() {
		return fail(ErrorStaleAuthority)
	}
	workspace, workspaceOK := m.workspaces[intent.TaskID]
	if !workspaceOK || workspace.policyDomainID != intent.PolicyDomainID ||
		workspace.taskWorkspaceID != intent.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if workspace.currentRevisionID != intent.ExpectedCurrentRevisionID ||
		workspace.currentCheckpointID != intent.ExpectedCurrentCheckpointID ||
		workspace.generation != intent.Generation || workspace.fence != intent.Fence {
		return fail(ErrorStaleAuthority)
	}
	if intent.Mode != RecoveryModeWritable {
		return fail(ErrorRecoveryReadOnly)
	}
	checkpoint, checkpointOK := m.checkpoints[intent.TargetCheckpointID]
	if !checkpointOK || checkpoint.taskWorkspaceID != intent.TaskWorkspaceID ||
		checkpoint.revisionID != intent.TargetRevisionID || m.durableObject == nil {
		return fail(ErrorIntegrityFailure)
	}
	if !checkpoint.retention.semanticallyRetained(m.now()) {
		return fail(ErrorCheckpointNotRetained)
	}
	if len(intent.ReadOnlyInputs) > 0 && m.reconstructionInput == nil {
		return fail(ErrorIntegrityFailure)
	}

	nextGeneration := workspace.generation + 1
	nextFence := workspace.fence + 1
	markOperationReconciliationRequired(m.operations, scope)
	verified, verifyErr := m.durableObject.VerifyCheckpoint(ctx, VerifyCheckpointContentRequest{
		PolicyDomainID:    intent.PolicyDomainID,
		TaskID:            intent.TaskID,
		TaskWorkspaceID:   intent.TaskWorkspaceID,
		RevisionID:        intent.TargetRevisionID,
		CheckpointID:      intent.TargetCheckpointID,
		Manifest:          cloneCheckpointManifest(checkpoint.evidence.Manifest),
		CanonicalManifest: checkpoint.evidence.Manifest.CanonicalBytes(),
		Expected:          verifiedCheckpointContent(checkpoint.evidence),
		Generation:        nextGeneration,
		Fence:             nextFence,
		Operation:         request.Operation,
	})
	if verifyErr != nil {
		if errors.Is(verifyErr, ErrDurableObjectResultAmbiguous) {
			return RestoreTaskWorkspaceResult{}, &Error{Code: ErrorReconciliationRequired}
		}
		return fail(ErrorIntegrityFailure)
	}
	verificationRequest := MaterializeRequest{
		PolicyDomainID:  intent.PolicyDomainID,
		TaskID:          intent.TaskID,
		TaskWorkspaceID: intent.TaskWorkspaceID,
		RevisionID:      intent.TargetRevisionID,
		CheckpointID:    intent.TargetCheckpointID,
		Generation:      nextGeneration,
		Fence:           nextFence,
		Operation:       request.Operation,
	}
	evidence, trusted := m.reverifyCheckpointEvidence(verificationRequest, checkpoint.evidence, verified)
	if !trusted {
		return fail(ErrorIntegrityFailure)
	}

	readOnlyInputs := make([]ReadOnlyInputMaterialization, 0, len(intent.ReadOnlyInputs))
	seenCapabilities := make(map[ReadOnlyInputCapabilityID]struct{}, len(intent.ReadOnlyInputs))
	seenMaterializations := make(map[ReadOnlyInputMaterializationID]struct{}, len(intent.ReadOnlyInputs))
	for _, capability := range intent.ReadOnlyInputs {
		if !readOnlyInputCapabilityIsCanonical(capability) || capability.PolicyDomainID != intent.PolicyDomainID ||
			capability.TaskID != intent.TaskID || capability.ExpiresAt <= m.now() {
			return fail(ErrorIntegrityFailure)
		}
		if _, duplicate := seenCapabilities[capability.ID]; duplicate {
			return fail(ErrorIntegrityFailure)
		}
		seenCapabilities[capability.ID] = struct{}{}
		materialized, materializeErr := m.reconstructionInput.MaterializeReadOnlyInput(ctx, MaterializeReadOnlyInputRequest{
			RecoveryIntentID: intent.ID,
			Capability:       capability,
			Generation:       nextGeneration,
			Fence:            nextFence,
			Operation:        request.Operation,
		})
		if materializeErr != nil || !readOnlyInputMaterializationMatches(materialized, capability, nextGeneration, nextFence) {
			return fail(ErrorIntegrityFailure)
		}
		if _, duplicate := seenMaterializations[materialized.ID]; duplicate {
			return fail(ErrorIntegrityFailure)
		}
		seenMaterializations[materialized.ID] = struct{}{}
		readOnlyInputs = append(readOnlyInputs, materialized)
	}
	markOperationVerified(m.operations, scope)
	materializationID := MaterializationID(m.operationOpaqueID(scope, "materialization", "materialization"))
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforeAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(materializationID),
	}); err != nil {
		return RestoreTaskWorkspaceResult{}, err
	}

	checkpoint.evidence = cloneCheckpointEvidence(evidence)
	m.checkpoints[intent.TargetCheckpointID] = checkpoint
	m.recordDurableEvidenceIdentities(verifiedCheckpointContent(evidence))
	workspace.generation = nextGeneration
	workspace.fence = nextFence
	m.workspaces[intent.TaskID] = workspace
	m.materializations[materializationID] = materializationBinding{
		policyDomainID:  intent.PolicyDomainID,
		taskID:          intent.TaskID,
		taskWorkspaceID: intent.TaskWorkspaceID,
		revisionID:      intent.TargetRevisionID,
		checkpointID:    intent.TargetCheckpointID,
		generation:      nextGeneration,
		fence:           nextFence,
		expiryPolicyID:  m.expiryPolicy.ID,
		expiresAt:       m.now() + Instant(m.expiryPolicy.MaterializationLifetime),
		readOnlyInputs:  cloneReadOnlyInputMaterializations(readOnlyInputs),
	}
	result := RestoreTaskWorkspaceResult{
		TaskWorkspaceID:        intent.TaskWorkspaceID,
		MaterializationID:      materializationID,
		RevisionID:             intent.TargetRevisionID,
		CheckpointID:           intent.TargetCheckpointID,
		CurrentRevisionID:      workspace.currentRevisionID,
		CurrentCheckpointID:    workspace.currentCheckpointID,
		ManifestDigest:         checkpoint.manifestDigest,
		ContentEvidenceRoot:    evidence.IntegrityEvidence.ContentEvidenceRoot,
		DurabilityEvidenceRoot: evidence.IntegrityEvidence.DurabilityEvidenceRoot,
		CheckpointEvidence:     evidence,
		ReadOnlyInputs:         cloneReadOnlyInputMaterializations(readOnlyInputs),
		Generation:             nextGeneration,
		PreviousFence:          intent.Fence,
		Fence:                  nextFence,
		RecoveryIntentID:       intent.ID,
		Operation:              request.Operation,
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(materializationID),
	}); err != nil {
		return RestoreTaskWorkspaceResult{}, err
	}
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func authorizedCheckpointRestoreIntentIsCanonical(intent AuthorizedRecoveryIntent) bool {
	if !authorizedRecoveryIntentIsCanonical(intent) || intent.TargetKind != RecoveryTargetCheckpoint ||
		intent.TargetRevisionID == "" || intent.TargetCheckpointID == "" ||
		intent.ArtifactVersionInput != (ArtifactVersionReconstructionInput{}) ||
		intent.PublicationAuthorityID != "" || !validRecoveryMode(intent.Mode) {
		return false
	}
	for _, capability := range intent.ReadOnlyInputs {
		if !readOnlyInputCapabilityIsCanonical(capability) {
			return false
		}
	}
	return true
}

func authorizedRecoveryIntentIsCanonical(intent AuthorizedRecoveryIntent) bool {
	return intent.ID != "" && intent.RecoveryAuthorityID != "" && intent.PolicyDomainID != "" &&
		intent.TaskID != "" && intent.TaskWorkspaceID != "" && intent.ExpectedCurrentRevisionID != "" &&
		intent.Generation != 0 && intent.Fence != 0 && intent.ExpiresAt != 0 && validDigest(intent.Digest) &&
		intent.Digest == intent.CanonicalDigest()
}

func sameAuthorizedRecoveryIntent(left, right AuthorizedRecoveryIntent) bool {
	return reflect.DeepEqual(left, right)
}

func cloneAuthorizedRecoveryIntent(intent AuthorizedRecoveryIntent) AuthorizedRecoveryIntent {
	intent.ReadOnlyInputs = append([]ReadOnlyInputCapability(nil), intent.ReadOnlyInputs...)
	return intent
}

func validRecoveryMode(mode RecoveryMode) bool {
	return mode == RecoveryModeWritable || mode == RecoveryModeDegradedReadOnly
}
