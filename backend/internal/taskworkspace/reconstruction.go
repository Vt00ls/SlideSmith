package taskworkspace

import "context"

type (
	ArtifactVersionID                string
	ArtifactVersionInputCapabilityID string
	PublicationAuthorityID           string
	ImmutableInputKind               string
	ImmutableInputID                 string
	ReadOnlyInputCapabilityID        string
	ImmutableInputAuthorityID        string
	ReadOnlyInputMaterializationID   string
	InputAccess                      string
	ReconstructionInputDecision      string
	ValidatedExportEvidenceID        string
)

const (
	ImmutableInputRuntimeRelease  ImmutableInputKind = "runtime_release"
	ImmutableInputTemplateVersion ImmutableInputKind = "template_version"
	ImmutableInputResourceBundle  ImmutableInputKind = "resource_bundle"
	ImmutableInputSourceMaterial  ImmutableInputKind = "source_material"

	InputAccessReadOnly InputAccess = "read_only"

	ReconstructionInputVerified ReconstructionInputDecision = "verified"
)

type ArtifactVersionReconstructionInput struct {
	ID                     ArtifactVersionInputCapabilityID
	Digest                 Digest
	PublicationAuthorityID PublicationAuthorityID
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	ArtifactVersionID      ArtifactVersionID
	ManifestDigest         Digest
	ExpiresAt              Instant
}

func (i ArtifactVersionReconstructionInput) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                     ArtifactVersionInputCapabilityID
		PublicationAuthorityID PublicationAuthorityID
		PolicyDomainID         PolicyDomainID
		TaskID                 TaskID
		ArtifactVersionID      ArtifactVersionID
		ManifestDigest         Digest
		ExpiresAt              Instant
	}{
		ID:                     i.ID,
		PublicationAuthorityID: i.PublicationAuthorityID,
		PolicyDomainID:         i.PolicyDomainID,
		TaskID:                 i.TaskID,
		ArtifactVersionID:      i.ArtifactVersionID,
		ManifestDigest:         i.ManifestDigest,
		ExpiresAt:              i.ExpiresAt,
	})
}

type ReadOnlyInputCapability struct {
	ID             ReadOnlyInputCapabilityID
	Digest         Digest
	AuthorityID    ImmutableInputAuthorityID
	PolicyDomainID PolicyDomainID
	TaskID         TaskID
	Kind           ImmutableInputKind
	InputID        ImmutableInputID
	ManifestDigest Digest
	ExpiresAt      Instant
}

func (c ReadOnlyInputCapability) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID             ReadOnlyInputCapabilityID
		AuthorityID    ImmutableInputAuthorityID
		PolicyDomainID PolicyDomainID
		TaskID         TaskID
		Kind           ImmutableInputKind
		InputID        ImmutableInputID
		ManifestDigest Digest
		ExpiresAt      Instant
	}{
		ID:             c.ID,
		AuthorityID:    c.AuthorityID,
		PolicyDomainID: c.PolicyDomainID,
		TaskID:         c.TaskID,
		Kind:           c.Kind,
		InputID:        c.InputID,
		ManifestDigest: c.ManifestDigest,
		ExpiresAt:      c.ExpiresAt,
	})
}

type VerifyArtifactVersionReconstructionRequest struct {
	RecoveryIntentID     RecoveryIntentID
	ArtifactVersionInput ArtifactVersionReconstructionInput
	Generation           Generation
	Fence                Fence
	Operation            Operation
}

type ArtifactVersionReconstructionEvidence struct {
	ID                     EvidenceID
	Digest                 Digest
	PublicationAuthorityID PublicationAuthorityID
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	ArtifactVersionID      ArtifactVersionID
	ManifestDigest         Digest
	InputCapabilityID      ArtifactVersionInputCapabilityID
	ContentEvidenceRoot    EvidenceRoot
	Decision               ReconstructionInputDecision
	RecoveryIntentID       RecoveryIntentID
	Generation             Generation
	Fence                  Fence
	OperationID            OperationID
}

func (e ArtifactVersionReconstructionEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                     EvidenceID
		PublicationAuthorityID PublicationAuthorityID
		PolicyDomainID         PolicyDomainID
		TaskID                 TaskID
		ArtifactVersionID      ArtifactVersionID
		ManifestDigest         Digest
		InputCapabilityID      ArtifactVersionInputCapabilityID
		ContentEvidenceRoot    EvidenceRoot
		Decision               ReconstructionInputDecision
		RecoveryIntentID       RecoveryIntentID
		Generation             Generation
		Fence                  Fence
		OperationID            OperationID
	}{
		ID:                     e.ID,
		PublicationAuthorityID: e.PublicationAuthorityID,
		PolicyDomainID:         e.PolicyDomainID,
		TaskID:                 e.TaskID,
		ArtifactVersionID:      e.ArtifactVersionID,
		ManifestDigest:         e.ManifestDigest,
		InputCapabilityID:      e.InputCapabilityID,
		ContentEvidenceRoot:    e.ContentEvidenceRoot,
		Decision:               e.Decision,
		RecoveryIntentID:       e.RecoveryIntentID,
		Generation:             e.Generation,
		Fence:                  e.Fence,
		OperationID:            e.OperationID,
	})
}

type MaterializeReadOnlyInputRequest struct {
	RecoveryIntentID RecoveryIntentID
	Capability       ReadOnlyInputCapability
	Generation       Generation
	Fence            Fence
	Operation        Operation
}

type ReadOnlyInputMaterialization struct {
	ID             ReadOnlyInputMaterializationID
	Digest         Digest
	CapabilityID   ReadOnlyInputCapabilityID
	Kind           ImmutableInputKind
	InputID        ImmutableInputID
	ManifestDigest Digest
	EvidenceID     EvidenceID
	Access         InputAccess
	Generation     Generation
	Fence          Fence
}

func (m ReadOnlyInputMaterialization) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID             ReadOnlyInputMaterializationID
		CapabilityID   ReadOnlyInputCapabilityID
		Kind           ImmutableInputKind
		InputID        ImmutableInputID
		ManifestDigest Digest
		EvidenceID     EvidenceID
		Access         InputAccess
		Generation     Generation
		Fence          Fence
	}{
		ID:             m.ID,
		CapabilityID:   m.CapabilityID,
		Kind:           m.Kind,
		InputID:        m.InputID,
		ManifestDigest: m.ManifestDigest,
		EvidenceID:     m.EvidenceID,
		Access:         m.Access,
		Generation:     m.Generation,
		Fence:          m.Fence,
	})
}

// ReconstructionInputPort verifies the exact Artifact Version selected by the
// Platform Control Plane and materializes immutable dependencies through their
// independent read-only capabilities. It cannot select or publish a version.
type ReconstructionInputPort interface {
	VerifyArtifactVersion(context.Context, VerifyArtifactVersionReconstructionRequest) (ArtifactVersionReconstructionEvidence, error)
	MaterializeReadOnlyInput(context.Context, MaterializeReadOnlyInputRequest) (ReadOnlyInputMaterialization, error)
}

type ReconstructTaskWorkspaceRequest struct {
	Intent    AuthorizedRecoveryIntent
	Operation Operation
}

func (r ReconstructTaskWorkspaceRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind        string
		Intent      AuthorizedRecoveryIntent
		OperationID OperationID
	}{
		Kind:        "reconstruct_task_workspace",
		Intent:      cloneAuthorizedRecoveryIntent(r.Intent),
		OperationID: r.Operation.ID,
	})
}

type ReconstructTaskWorkspaceResult struct {
	TaskWorkspaceID                TaskWorkspaceID
	MaterializationID              MaterializationID
	CurrentRevisionID              RevisionID
	CurrentCheckpointID            CheckpointID
	ArtifactVersionID              ArtifactVersionID
	ArtifactManifestDigest         Digest
	ArtifactReconstructionEvidence ArtifactVersionReconstructionEvidence
	ReadOnlyInputs                 []ReadOnlyInputMaterialization
	PublicationAuthorityID         PublicationAuthorityID
	Generation                     Generation
	PreviousFence                  Fence
	Fence                          Fence
	RecoveryIntentID               RecoveryIntentID
	Operation                      Operation
}

type ValidatedExportEvidence struct {
	ID                           ValidatedExportEvidenceID
	Digest                       Digest
	PublicationAuthorityID       PublicationAuthorityID
	PolicyDomainID               PolicyDomainID
	TaskID                       TaskID
	TaskWorkspaceID              TaskWorkspaceID
	SourceArtifactVersionID      ArtifactVersionID
	ReconstructionEvidenceID     EvidenceID
	ReconstructionEvidenceDigest Digest
	RevisionID                   RevisionID
	CheckpointID                 CheckpointID
	ManifestDigest               Digest
	ValidationEvidenceID         EvidenceID
	ValidationEvidenceDigest     Digest
	ContentEvidenceRoot          EvidenceRoot
	DurabilityEvidenceRoot       EvidenceRoot
	Generation                   Generation
	Fence                        Fence
	OperationID                  OperationID
}

func (e ValidatedExportEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                           ValidatedExportEvidenceID
		PublicationAuthorityID       PublicationAuthorityID
		PolicyDomainID               PolicyDomainID
		TaskID                       TaskID
		TaskWorkspaceID              TaskWorkspaceID
		SourceArtifactVersionID      ArtifactVersionID
		ReconstructionEvidenceID     EvidenceID
		ReconstructionEvidenceDigest Digest
		RevisionID                   RevisionID
		CheckpointID                 CheckpointID
		ManifestDigest               Digest
		ValidationEvidenceID         EvidenceID
		ValidationEvidenceDigest     Digest
		ContentEvidenceRoot          EvidenceRoot
		DurabilityEvidenceRoot       EvidenceRoot
		Generation                   Generation
		Fence                        Fence
		OperationID                  OperationID
	}{
		ID:                           e.ID,
		PublicationAuthorityID:       e.PublicationAuthorityID,
		PolicyDomainID:               e.PolicyDomainID,
		TaskID:                       e.TaskID,
		TaskWorkspaceID:              e.TaskWorkspaceID,
		SourceArtifactVersionID:      e.SourceArtifactVersionID,
		ReconstructionEvidenceID:     e.ReconstructionEvidenceID,
		ReconstructionEvidenceDigest: e.ReconstructionEvidenceDigest,
		RevisionID:                   e.RevisionID,
		CheckpointID:                 e.CheckpointID,
		ManifestDigest:               e.ManifestDigest,
		ValidationEvidenceID:         e.ValidationEvidenceID,
		ValidationEvidenceDigest:     e.ValidationEvidenceDigest,
		ContentEvidenceRoot:          e.ContentEvidenceRoot,
		DurabilityEvidenceRoot:       e.DurabilityEvidenceRoot,
		Generation:                   e.Generation,
		Fence:                        e.Fence,
		OperationID:                  e.OperationID,
	})
}

func (m *inMemory) ReconstructTaskWorkspace(
	ctx context.Context,
	request ReconstructTaskWorkspaceRequest,
) (ReconstructTaskWorkspaceResult, error) {
	intent := cloneAuthorizedRecoveryIntent(request.Intent)
	if !authorizedArtifactReconstructionIntentIsCanonical(intent) || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return ReconstructTaskWorkspaceResult{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{intent.PolicyDomainID, intent.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[ReconstructTaskWorkspaceResult](
		m.operations, scope, request.Operation,
	); replayed {
		return result, err
	}
	trustedRequest := ReconstructTaskWorkspaceRequest{Intent: intent, Operation: request.Operation}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, trustedRequest, reconstructTaskWorkspaceJournalSpec(), nil,
	); err != nil {
		return ReconstructTaskWorkspaceResult{}, err
	}
	fail := func(code ErrorCode) (ReconstructTaskWorkspaceResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, ReconstructTaskWorkspaceResult{}, err)
		return ReconstructTaskWorkspaceResult{}, err
	}
	currentIntent, intentOK := m.currentRecoveryIntent(intent.ID)
	if !intentOK || !sameAuthorizedRecoveryIntent(currentIntent, intent) ||
		intent.RecoveryAuthorityID != m.recoveryAuthorityID || intent.ExpiresAt <= m.now() {
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
	if m.reconstructionInput == nil || intent.ArtifactVersionInput.ExpiresAt <= m.now() {
		return fail(ErrorIntegrityFailure)
	}

	nextGeneration := workspace.generation + 1
	nextFence := workspace.fence + 1
	markOperationReconciliationRequired(m.operations, scope)
	artifactEvidence, err := m.reconstructionInput.VerifyArtifactVersion(ctx, VerifyArtifactVersionReconstructionRequest{
		RecoveryIntentID:     intent.ID,
		ArtifactVersionInput: intent.ArtifactVersionInput,
		Generation:           nextGeneration,
		Fence:                nextFence,
		Operation:            request.Operation,
	})
	if err != nil || !artifactReconstructionEvidenceMatches(
		artifactEvidence,
		intent.ArtifactVersionInput,
		intent.ID,
		nextGeneration,
		nextFence,
		request.Operation.ID,
	) {
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
		return ReconstructTaskWorkspaceResult{}, err
	}

	workspace.generation = nextGeneration
	workspace.fence = nextFence
	m.workspaces[intent.TaskID] = workspace
	m.materializations[materializationID] = materializationBinding{
		policyDomainID:         intent.PolicyDomainID,
		taskID:                 intent.TaskID,
		taskWorkspaceID:        intent.TaskWorkspaceID,
		revisionID:             workspace.currentRevisionID,
		checkpointID:           workspace.currentCheckpointID,
		generation:             nextGeneration,
		fence:                  nextFence,
		expiryPolicyID:         m.expiryPolicy.ID,
		expiresAt:              m.now() + Instant(m.expiryPolicy.MaterializationLifetime),
		readOnlyInputs:         cloneReadOnlyInputMaterializations(readOnlyInputs),
		artifactVersionID:      intent.ArtifactVersionInput.ArtifactVersionID,
		artifactManifestDigest: intent.ArtifactVersionInput.ManifestDigest,
		reconstructionEvidence: artifactEvidence,
		publicationAuthorityID: intent.PublicationAuthorityID,
	}
	result := ReconstructTaskWorkspaceResult{
		TaskWorkspaceID:                intent.TaskWorkspaceID,
		MaterializationID:              materializationID,
		CurrentRevisionID:              workspace.currentRevisionID,
		CurrentCheckpointID:            workspace.currentCheckpointID,
		ArtifactVersionID:              intent.ArtifactVersionInput.ArtifactVersionID,
		ArtifactManifestDigest:         intent.ArtifactVersionInput.ManifestDigest,
		ArtifactReconstructionEvidence: artifactEvidence,
		ReadOnlyInputs:                 cloneReadOnlyInputMaterializations(readOnlyInputs),
		PublicationAuthorityID:         intent.PublicationAuthorityID,
		Generation:                     nextGeneration,
		PreviousFence:                  intent.Fence,
		Fence:                          nextFence,
		RecoveryIntentID:               intent.ID,
		Operation:                      request.Operation,
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(materializationID),
	}); err != nil {
		return ReconstructTaskWorkspaceResult{}, err
	}
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func authorizedArtifactReconstructionIntentIsCanonical(intent AuthorizedRecoveryIntent) bool {
	if !authorizedRecoveryIntentIsCanonical(intent) || intent.TargetKind != RecoveryTargetArtifactVersion ||
		intent.TargetRevisionID != "" || intent.TargetCheckpointID != "" || !validRecoveryMode(intent.Mode) ||
		intent.PublicationAuthorityID == "" || !artifactVersionInputIsCanonical(intent.ArtifactVersionInput) ||
		intent.ArtifactVersionInput.PublicationAuthorityID != intent.PublicationAuthorityID ||
		intent.ArtifactVersionInput.PolicyDomainID != intent.PolicyDomainID ||
		intent.ArtifactVersionInput.TaskID != intent.TaskID {
		return false
	}
	for _, capability := range intent.ReadOnlyInputs {
		if !readOnlyInputCapabilityIsCanonical(capability) {
			return false
		}
	}
	return true
}

func artifactVersionInputIsCanonical(input ArtifactVersionReconstructionInput) bool {
	return input.ID != "" && input.PublicationAuthorityID != "" && input.PolicyDomainID != "" &&
		input.TaskID != "" && input.ArtifactVersionID != "" && validDigest(input.ManifestDigest) &&
		input.ExpiresAt != 0 && validDigest(input.Digest) && input.Digest == input.CanonicalDigest()
}

func readOnlyInputCapabilityIsCanonical(capability ReadOnlyInputCapability) bool {
	return capability.ID != "" && capability.AuthorityID != "" && capability.PolicyDomainID != "" &&
		capability.TaskID != "" && validImmutableInputKind(capability.Kind) && capability.InputID != "" &&
		validDigest(capability.ManifestDigest) && capability.ExpiresAt != 0 && validDigest(capability.Digest) &&
		capability.Digest == capability.CanonicalDigest()
}

func validImmutableInputKind(kind ImmutableInputKind) bool {
	switch kind {
	case ImmutableInputRuntimeRelease, ImmutableInputTemplateVersion, ImmutableInputResourceBundle, ImmutableInputSourceMaterial:
		return true
	default:
		return false
	}
}

func artifactReconstructionEvidenceMatches(
	evidence ArtifactVersionReconstructionEvidence,
	input ArtifactVersionReconstructionInput,
	recoveryIntentID RecoveryIntentID,
	generation Generation,
	fence Fence,
	operationID OperationID,
) bool {
	return evidence.ID != "" && validDigest(evidence.Digest) && evidence.Digest == evidence.CanonicalDigest() &&
		evidence.Decision == ReconstructionInputVerified && evidence.PublicationAuthorityID == input.PublicationAuthorityID &&
		evidence.PolicyDomainID == input.PolicyDomainID && evidence.TaskID == input.TaskID &&
		evidence.ArtifactVersionID == input.ArtifactVersionID && evidence.ManifestDigest == input.ManifestDigest &&
		evidence.InputCapabilityID == input.ID && evidence.ContentEvidenceRoot != "" &&
		evidence.RecoveryIntentID == recoveryIntentID && evidence.Generation == generation && evidence.Fence == fence &&
		evidence.OperationID == operationID
}

func readOnlyInputMaterializationMatches(
	materialization ReadOnlyInputMaterialization,
	capability ReadOnlyInputCapability,
	generation Generation,
	fence Fence,
) bool {
	return materialization.ID != "" && validDigest(materialization.Digest) &&
		materialization.Digest == materialization.CanonicalDigest() && materialization.CapabilityID == capability.ID &&
		materialization.Kind == capability.Kind && materialization.InputID == capability.InputID &&
		materialization.ManifestDigest == capability.ManifestDigest && materialization.EvidenceID != "" &&
		materialization.Access == InputAccessReadOnly && materialization.Generation == generation && materialization.Fence == fence
}

func cloneReadOnlyInputMaterializations(inputs []ReadOnlyInputMaterialization) []ReadOnlyInputMaterialization {
	return append([]ReadOnlyInputMaterialization(nil), inputs...)
}
