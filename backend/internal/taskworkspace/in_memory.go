package taskworkspace

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

type InMemoryConfig struct {
	ValidationAuthorityID ValidationAuthorityID
	DurabilityAuthorityID DurabilityAuthorityID
	DurableObject         DurableObjectPort
}

type inMemory struct {
	mu                    sync.Mutex
	nextID                uint64
	validationAuthorityID ValidationAuthorityID
	durabilityAuthorityID DurabilityAuthorityID
	durableObject         DurableObjectPort
	workspaces            map[TaskID]workspaceBinding
	materializations      map[MaterializationID]materializationBinding
	views                 map[RuntimeViewID]runtimeViewBinding
	revisions             map[RevisionID]revisionRecord
	checkpoints           map[CheckpointID]checkpointRecord
	operations            map[operationScope]operationRecord
	contentReferences     map[ContentReferenceID]ContentReference
	contentFacts          map[ContentID]durableContentFact
	durabilityReceipts    map[DurabilityReceiptID]DurabilityReceipt
	currentReceipts       map[receiptAuthorityScope]DurabilityReceipt
	receiptGenerations    map[receiptAuthorityScope]map[DurabilityGenerationID]DurabilityReceiptID
}

type durableContentFact struct {
	policyDomainID PolicyDomainID
	contentDigest  Digest
	size           uint64
}

type receiptAuthorityScope struct {
	policyDomainID PolicyDomainID
	contentID      ContentID
}

type workspaceBinding struct {
	policyDomainID      PolicyDomainID
	taskWorkspaceID     TaskWorkspaceID
	currentRevisionID   RevisionID
	currentCheckpointID CheckpointID
	currentManifest     Digest
	generation          Generation
	fence               Fence
}

type revisionRecord struct {
	taskWorkspaceID TaskWorkspaceID
	manifestDigest  Digest
	predecessor     RevisionID
}

type checkpointRecord struct {
	taskWorkspaceID TaskWorkspaceID
	revisionID      RevisionID
	manifestDigest  Digest
	operationID     OperationID
	evidence        CheckpointEvidence
}

type materializationBinding struct {
	policyDomainID  PolicyDomainID
	taskID          TaskID
	taskWorkspaceID TaskWorkspaceID
	revisionID      RevisionID
	checkpointID    CheckpointID
	generation      Generation
	fence           Fence
}

type runtimeViewBinding struct {
	policyDomainID    PolicyDomainID
	taskID            TaskID
	taskWorkspaceID   TaskWorkspaceID
	materializationID MaterializationID
	baseRevisionID    RevisionID
	phaseRunID        PhaseRunID
	runtimeRunID      RuntimeRunID
	sandboxLeaseID    SandboxLeaseID
	generation        Generation
	fence             Fence
	terminal          bool
}

type operationScope struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
	operationID    OperationID
}

type operationRecord struct {
	requestDigest Digest
	result        any
	err           *Error
}

func NewInMemory(config InMemoryConfig) Lifecycle {
	memory := &inMemory{
		validationAuthorityID: config.ValidationAuthorityID,
		durabilityAuthorityID: config.DurabilityAuthorityID,
		durableObject:         config.DurableObject,
		workspaces:            make(map[TaskID]workspaceBinding),
		materializations:      make(map[MaterializationID]materializationBinding),
		views:                 make(map[RuntimeViewID]runtimeViewBinding),
		revisions:             make(map[RevisionID]revisionRecord),
		checkpoints:           make(map[CheckpointID]checkpointRecord),
		operations:            make(map[operationScope]operationRecord),
		contentReferences:     make(map[ContentReferenceID]ContentReference),
		contentFacts:          make(map[ContentID]durableContentFact),
		durabilityReceipts:    make(map[DurabilityReceiptID]DurabilityReceipt),
		currentReceipts:       make(map[receiptAuthorityScope]DurabilityReceipt),
		receiptGenerations:    make(map[receiptAuthorityScope]map[DurabilityGenerationID]DurabilityReceiptID),
	}
	return memory
}

func (m *inMemory) OpenRuntimeView(_ context.Context, request OpenRuntimeViewRequest) (OpenRuntimeViewResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.MaterializationID == "" || request.BaseRevisionID == "" || request.PhaseRunID == "" ||
		request.RuntimeRunID == "" || request.SandboxLeaseID == "" || request.Generation == 0 || request.Fence == 0 ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return OpenRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}
	if result, replayed, err := replayOperation[OpenRuntimeViewResult](m.operations, request.PolicyDomainID, request.TaskID, request.Operation); replayed {
		return result, err
	}
	workspace, workspaceOK := m.workspaces[request.TaskID]
	materialization, materializationOK := m.materializations[request.MaterializationID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID ||
		!materializationOK || materialization.policyDomainID != request.PolicyDomainID || materialization.taskID != request.TaskID ||
		materialization.taskWorkspaceID != request.TaskWorkspaceID {
		err := &Error{Code: ErrorOwnershipDenied}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, OpenRuntimeViewResult{}, err)
		return OpenRuntimeViewResult{}, err
	}
	if workspace.currentRevisionID != request.BaseRevisionID || workspace.generation != request.Generation || workspace.fence != request.Fence ||
		materialization.revisionID != request.BaseRevisionID || materialization.checkpointID != workspace.currentCheckpointID ||
		materialization.generation != request.Generation || materialization.fence != request.Fence {
		err := &Error{Code: ErrorStaleAuthority}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, OpenRuntimeViewResult{}, err)
		return OpenRuntimeViewResult{}, err
	}

	identity := RuntimeViewID(m.nextOpaqueID("runtime-view"))
	m.views[identity] = runtimeViewBinding{
		policyDomainID:    request.PolicyDomainID,
		taskID:            request.TaskID,
		taskWorkspaceID:   request.TaskWorkspaceID,
		materializationID: request.MaterializationID,
		baseRevisionID:    request.BaseRevisionID,
		phaseRunID:        request.PhaseRunID,
		runtimeRunID:      request.RuntimeRunID,
		sandboxLeaseID:    request.SandboxLeaseID,
		generation:        request.Generation,
		fence:             request.Fence,
	}
	result := OpenRuntimeViewResult{
		RuntimeViewID:   identity,
		TaskWorkspaceID: request.TaskWorkspaceID,
		BaseRevisionID:  request.BaseRevisionID,
		PhaseRunID:      request.PhaseRunID,
		RuntimeRunID:    request.RuntimeRunID,
		SandboxLeaseID:  request.SandboxLeaseID,
		Generation:      request.Generation,
		Fence:           request.Fence,
	}
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

func (m *inMemory) CommitRuntimeView(ctx context.Context, request CommitRuntimeViewRequest) (CommitRuntimeViewResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RuntimeViewID == "" || request.BaseRevisionID == "" || request.ExpectedCurrentRevision == "" ||
		request.Generation == 0 || request.Fence == 0 || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CommitRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}
	if result, replayed, err := replayOperation[CommitRuntimeViewResult](m.operations, request.PolicyDomainID, request.TaskID, request.Operation); replayed {
		return result, err
	}
	trustedRequest := request
	trustedRequest.DeclaredStateManifest = cloneDeclaredStateManifest(request.DeclaredStateManifest)
	fail := func(code ErrorCode) (CommitRuntimeViewResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, CommitRuntimeViewResult{}, err)
		return CommitRuntimeViewResult{}, err
	}

	workspace, workspaceOK := m.workspaces[request.TaskID]
	view, viewOK := m.views[request.RuntimeViewID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID ||
		!viewOK || view.policyDomainID != request.PolicyDomainID || view.taskID != request.TaskID ||
		view.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if view.baseRevisionID != request.BaseRevisionID || view.generation != request.Generation || view.fence != request.Fence {
		return fail(ErrorStaleAuthority)
	}
	if view.terminal {
		return fail(ErrorViewTerminalConflict)
	}
	if workspace.currentRevisionID != request.ExpectedCurrentRevision || request.ExpectedCurrentRevision != request.BaseRevisionID ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		return fail(ErrorStaleAuthority)
	}
	if !m.validationEvidenceMatches(trustedRequest.ValidationEvidence, trustedRequest, view) {
		return fail(ErrorIntegrityFailure)
	}
	contentRoot, ok := validateDeclaredStateManifest(trustedRequest.DeclaredStateManifest)
	if !ok {
		return fail(ErrorIntegrityFailure)
	}

	predecessor := workspace.currentRevisionID
	resultingRevision := predecessor
	changed := trustedRequest.DeclaredStateManifest.Digest != workspace.currentManifest
	if changed {
		resultingRevision = RevisionID(m.nextOpaqueID("revision"))
	} else {
		predecessor = m.revisions[resultingRevision].predecessor
	}
	checkpointID := CheckpointID(m.nextOpaqueID("checkpoint"))
	checkpointEvidence := CheckpointEvidence{}
	var durabilityRoot EvidenceRoot
	if m.durableObject != nil {
		prepared, err := m.durableObject.PrepareCheckpoint(ctx, PrepareCheckpointContentRequest{
			PolicyDomainID:    request.PolicyDomainID,
			TaskID:            request.TaskID,
			TaskWorkspaceID:   request.TaskWorkspaceID,
			RuntimeViewID:     request.RuntimeViewID,
			RevisionID:        resultingRevision,
			CheckpointID:      checkpointID,
			Manifest:          cloneDeclaredStateManifest(trustedRequest.DeclaredStateManifest),
			CanonicalManifest: trustedRequest.DeclaredStateManifest.CanonicalBytes(),
			Generation:        request.Generation,
			Fence:             request.Fence,
			Operation:         request.Operation,
		})
		if err != nil {
			return fail(ErrorIntegrityFailure)
		}
		var trusted bool
		checkpointEvidence, contentRoot, durabilityRoot, trusted = m.bindCheckpointEvidence(
			trustedRequest,
			resultingRevision,
			checkpointID,
			prepared,
		)
		if !trusted {
			return fail(ErrorIntegrityFailure)
		}
	} else {
		return fail(ErrorIntegrityFailure)
	}

	if changed {
		m.revisions[resultingRevision] = revisionRecord{
			taskWorkspaceID: request.TaskWorkspaceID,
			manifestDigest:  trustedRequest.DeclaredStateManifest.Digest,
			predecessor:     predecessor,
		}
		workspace.currentRevisionID = resultingRevision
		workspace.currentManifest = trustedRequest.DeclaredStateManifest.Digest
	}
	workspace.currentCheckpointID = checkpointID
	m.workspaces[request.TaskID] = workspace
	m.checkpoints[checkpointID] = checkpointRecord{
		taskWorkspaceID: request.TaskWorkspaceID,
		revisionID:      resultingRevision,
		manifestDigest:  trustedRequest.DeclaredStateManifest.Digest,
		operationID:     request.Operation.ID,
		evidence:        cloneCheckpointEvidence(checkpointEvidence),
	}
	m.recordDurableEvidenceIdentities(verifiedCheckpointContent(checkpointEvidence))
	view.terminal = true
	m.views[request.RuntimeViewID] = view

	result := CommitRuntimeViewResult{
		TaskWorkspaceID:          request.TaskWorkspaceID,
		RevisionID:               resultingRevision,
		CheckpointID:             checkpointID,
		BaseRevisionID:           request.BaseRevisionID,
		PredecessorRevisionID:    predecessor,
		ManifestDigest:           trustedRequest.DeclaredStateManifest.Digest,
		ValidationEvidenceID:     request.ValidationEvidence.ID,
		ValidationEvidenceDigest: request.ValidationEvidence.Digest,
		ContentEvidenceRoot:      contentRoot,
		DurabilityEvidenceRoot:   durabilityRoot,
		CheckpointEvidence:       checkpointEvidence,
		Generation:               request.Generation,
		Fence:                    request.Fence,
		Operation:                request.Operation,
	}
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

func (m *inMemory) validationEvidenceMatches(
	evidence ValidationEvidence,
	request CommitRuntimeViewRequest,
	view runtimeViewBinding,
) bool {
	return m.validationAuthorityID != "" &&
		evidence.ID != "" && validDigest(evidence.Digest) && evidence.Digest == evidence.CanonicalDigest() &&
		evidence.ValidationAuthorityID == m.validationAuthorityID && evidence.Decision == ValidationAccepted &&
		evidence.PolicyDomainID == request.PolicyDomainID && evidence.TaskID == request.TaskID &&
		evidence.TaskWorkspaceID == request.TaskWorkspaceID && evidence.RuntimeViewID == request.RuntimeViewID &&
		evidence.BaseRevisionID == request.BaseRevisionID && evidence.PhaseRunID == view.phaseRunID &&
		evidence.RuntimeRunID == view.runtimeRunID && evidence.ManifestDigest == request.DeclaredStateManifest.Digest &&
		evidence.Generation == request.Generation && evidence.Fence == request.Fence
}

func (m *inMemory) bindCheckpointEvidence(
	request CommitRuntimeViewRequest,
	revisionID RevisionID,
	checkpointID CheckpointID,
	prepared VerifiedCheckpointContent,
) (CheckpointEvidence, EvidenceRoot, EvidenceRoot, bool) {
	manifestReference := prepared.ManifestReference
	if !checkpointManifestMatchesDeclaration(prepared.Manifest, request.DeclaredStateManifest) ||
		!contentReferenceIsCanonical(manifestReference) ||
		!contentReferenceMatchesCheckpoint(
			manifestReference,
			CheckpointManifestReference,
			request,
			revisionID,
			checkpointID,
		) ||
		manifestReference.StateMemberID != "" || manifestReference.LogicalMember != "" ||
		manifestReference.ContentDigest != prepared.Manifest.Digest ||
		manifestReference.Size != uint64(len(prepared.Manifest.CanonicalBytes())) {
		return CheckpointEvidence{}, "", "", false
	}

	if len(prepared.ContentReferences) != len(request.DeclaredStateManifest.Members) {
		return CheckpointEvidence{}, "", "", false
	}
	members := make(map[StateMemberID]DeclaredStateMember, len(request.DeclaredStateManifest.Members))
	for _, member := range request.DeclaredStateManifest.Members {
		members[member.ID] = member
	}
	manifestMembers := make(map[StateMemberID]CheckpointManifestMember, len(prepared.Manifest.Members))
	for _, member := range prepared.Manifest.Members {
		manifestMembers[member.ID] = member
	}
	seenReferences := map[ContentReferenceID]struct{}{manifestReference.ID: {}}
	seenMembers := make(map[StateMemberID]struct{}, len(prepared.ContentReferences))
	for _, reference := range prepared.ContentReferences {
		member, exists := members[reference.StateMemberID]
		manifestMember, manifestMemberExists := manifestMembers[reference.StateMemberID]
		if !exists || !contentReferenceIsCanonical(reference) ||
			!manifestMemberExists ||
			!contentReferenceMatchesCheckpoint(
				reference,
				CheckpointMemberReference,
				request,
				revisionID,
				checkpointID,
			) ||
			reference.LogicalMember != member.LogicalMember || reference.ContentID != manifestMember.ContentID ||
			reference.ContentDigest != member.ContentDigest || reference.Size != member.Size {
			return CheckpointEvidence{}, "", "", false
		}
		if _, duplicate := seenReferences[reference.ID]; duplicate {
			return CheckpointEvidence{}, "", "", false
		}
		if _, duplicate := seenMembers[reference.StateMemberID]; duplicate {
			return CheckpointEvidence{}, "", "", false
		}
		seenReferences[reference.ID] = struct{}{}
		seenMembers[reference.StateMemberID] = struct{}{}
	}

	contentRoot, durabilityRoot, durable := m.durableContentRoots(prepared)
	if !durable {
		return CheckpointEvidence{}, "", "", false
	}
	integrity := m.newCheckpointIntegrityEvidence(CheckpointIntegrityEvidence{
		DurabilityAuthorityID:    m.durabilityAuthorityID,
		PolicyDomainID:           request.PolicyDomainID,
		TaskID:                   request.TaskID,
		TaskWorkspaceID:          request.TaskWorkspaceID,
		RevisionID:               revisionID,
		CheckpointID:             checkpointID,
		ManifestDigest:           prepared.Manifest.Digest,
		ManifestContentID:        manifestReference.ContentID,
		ValidationEvidenceID:     request.ValidationEvidence.ID,
		ValidationEvidenceDigest: request.ValidationEvidence.Digest,
	}, request.Operation, request.Generation, request.Fence, contentRoot, durabilityRoot)
	evidence := CheckpointEvidence{
		Manifest:           cloneCheckpointManifest(prepared.Manifest),
		ManifestReference:  manifestReference,
		ContentReferences:  append([]ContentReference(nil), prepared.ContentReferences...),
		DurabilityReceipts: append([]DurabilityReceipt(nil), prepared.DurabilityReceipts...),
		IntegrityEvidence:  integrity,
	}
	return evidence, contentRoot, durabilityRoot, true
}

func (m *inMemory) reverifyCheckpointEvidence(
	request MaterializeRequest,
	expected CheckpointEvidence,
	verified VerifiedCheckpointContent,
) (CheckpointEvidence, bool) {
	if expected.Manifest.Digest == "" || expected.Manifest.Digest != expected.Manifest.CanonicalDigest() ||
		expected.IntegrityEvidence.Digest != expected.IntegrityEvidence.CanonicalDigest() ||
		expected.IntegrityEvidence.Decision != CheckpointIntegrityVerified ||
		!sameCheckpointManifest(verified.Manifest, expected.Manifest) ||
		verified.ManifestReference != expected.ManifestReference ||
		!sameContentReferences(verified.ContentReferences, expected.ContentReferences) ||
		!m.receiptsRespectKnownIdentity(expected.DurabilityReceipts, verified.DurabilityReceipts) {
		return CheckpointEvidence{}, false
	}
	contentRoot, durabilityRoot, durable := m.durableContentRoots(verified)
	if !durable || contentRoot != expected.IntegrityEvidence.ContentEvidenceRoot {
		return CheckpointEvidence{}, false
	}
	prior := expected.IntegrityEvidence
	if prior.PolicyDomainID != request.PolicyDomainID || prior.TaskID != request.TaskID ||
		prior.TaskWorkspaceID != request.TaskWorkspaceID || prior.RevisionID != request.RevisionID ||
		prior.CheckpointID == "" || prior.ManifestDigest != expected.Manifest.Digest ||
		prior.ManifestContentID != verified.ManifestReference.ContentID ||
		prior.DurabilityAuthorityID != m.durabilityAuthorityID {
		return CheckpointEvidence{}, false
	}
	integrity := m.newCheckpointIntegrityEvidence(
		prior,
		request.Operation,
		request.Generation,
		request.Fence,
		contentRoot,
		durabilityRoot,
	)
	return CheckpointEvidence{
		Manifest:           cloneCheckpointManifest(expected.Manifest),
		ManifestReference:  verified.ManifestReference,
		ContentReferences:  append([]ContentReference(nil), verified.ContentReferences...),
		DurabilityReceipts: append([]DurabilityReceipt(nil), verified.DurabilityReceipts...),
		IntegrityEvidence:  integrity,
	}, true
}

func (m *inMemory) newCheckpointIntegrityEvidence(
	base CheckpointIntegrityEvidence,
	operation Operation,
	generation Generation,
	fence Fence,
	contentRoot EvidenceRoot,
	durabilityRoot EvidenceRoot,
) CheckpointIntegrityEvidence {
	base.ID = CheckpointIntegrityID(m.nextOpaqueID("checkpoint-integrity"))
	base.Digest = ""
	base.ContentEvidenceRoot = contentRoot
	base.DurabilityEvidenceRoot = durabilityRoot
	base.OperationID = operation.ID
	base.RequestDigest = operation.RequestDigest
	base.Generation = generation
	base.Fence = fence
	base.Decision = CheckpointIntegrityVerified
	base.Digest = base.CanonicalDigest()
	return base
}

func sameCheckpointManifest(left, right CheckpointManifest) bool {
	if left.Digest != right.Digest || left.DeclaredStateDigest != right.DeclaredStateDigest ||
		len(left.Members) != len(right.Members) {
		return false
	}
	leftMembers := left.canonicalValue().Members
	rightMembers := right.canonicalValue().Members
	for index := range leftMembers {
		if leftMembers[index] != rightMembers[index] {
			return false
		}
	}
	return true
}

func (m *inMemory) durableContentRoots(
	content VerifiedCheckpointContent,
) (EvidenceRoot, EvidenceRoot, bool) {
	references := canonicalContentReferences(content.ManifestReference, content.ContentReferences)
	requiredReceipts := make(map[ContentID]durableContentFact, len(references))
	seenReferenceIDs := make(map[ContentReferenceID]struct{}, len(references))
	for _, reference := range references {
		if !contentReferenceIsCanonical(reference) {
			return "", "", false
		}
		if _, duplicate := seenReferenceIDs[reference.ID]; duplicate {
			return "", "", false
		}
		seenReferenceIDs[reference.ID] = struct{}{}
		fact := durableContentFact{
			policyDomainID: reference.PolicyDomainID,
			contentDigest:  reference.ContentDigest,
			size:           reference.Size,
		}
		if existing, duplicate := requiredReceipts[reference.ContentID]; duplicate && existing != fact {
			return "", "", false
		}
		requiredReceipts[reference.ContentID] = fact
	}
	if len(content.DurabilityReceipts) != len(requiredReceipts) {
		return "", "", false
	}
	seenReceiptIDs := make(map[DurabilityReceiptID]struct{}, len(content.DurabilityReceipts))
	seenReceiptContent := make(map[ContentID]struct{}, len(content.DurabilityReceipts))
	for _, receipt := range content.DurabilityReceipts {
		fact, required := requiredReceipts[receipt.ContentID]
		if !required || !durabilityReceiptIsCanonical(receipt) ||
			receipt.DurabilityAuthorityID != m.durabilityAuthorityID ||
			receipt.PolicyDomainID != fact.policyDomainID ||
			receipt.ContentDigest != fact.contentDigest || receipt.Size != fact.size {
			return "", "", false
		}
		if _, duplicate := seenReceiptIDs[receipt.ID]; duplicate {
			return "", "", false
		}
		if _, duplicate := seenReceiptContent[receipt.ContentID]; duplicate {
			return "", "", false
		}
		seenReceiptIDs[receipt.ID] = struct{}{}
		seenReceiptContent[receipt.ContentID] = struct{}{}
	}
	if !m.durableEvidenceIdentitiesAreConsistent(content) {
		return "", "", false
	}
	receipts := canonicalDurabilityReceipts(content.DurabilityReceipts)
	contentRoot := EvidenceRoot(canonicalDigest(struct {
		References []ContentReference
	}{References: references}))
	durabilityRoot := EvidenceRoot(canonicalDigest(struct {
		Receipts []DurabilityReceipt
	}{Receipts: receipts}))
	return contentRoot, durabilityRoot, true
}

func (m *inMemory) durableEvidenceIdentitiesAreConsistent(content VerifiedCheckpointContent) bool {
	for _, reference := range canonicalContentReferences(content.ManifestReference, content.ContentReferences) {
		if prior, exists := m.contentReferences[reference.ID]; exists && prior != reference {
			return false
		}
		fact := durableContentFact{
			policyDomainID: reference.PolicyDomainID,
			contentDigest:  reference.ContentDigest,
			size:           reference.Size,
		}
		if prior, exists := m.contentFacts[reference.ContentID]; exists && prior != fact {
			return false
		}
	}
	for _, receipt := range content.DurabilityReceipts {
		if prior, exists := m.durabilityReceipts[receipt.ID]; exists && prior != receipt {
			return false
		}
		if !m.receiptCanBecomeCurrent(receipt) {
			return false
		}
	}
	return true
}

func (m *inMemory) recordDurableEvidenceIdentities(content VerifiedCheckpointContent) {
	for _, reference := range canonicalContentReferences(content.ManifestReference, content.ContentReferences) {
		m.contentReferences[reference.ID] = reference
		m.contentFacts[reference.ContentID] = durableContentFact{
			policyDomainID: reference.PolicyDomainID,
			contentDigest:  reference.ContentDigest,
			size:           reference.Size,
		}
	}
	for _, receipt := range content.DurabilityReceipts {
		m.durabilityReceipts[receipt.ID] = receipt
		scope := receiptAuthorityScope{
			policyDomainID: receipt.PolicyDomainID,
			contentID:      receipt.ContentID,
		}
		m.currentReceipts[scope] = receipt
		generations := m.receiptGenerations[scope]
		if generations == nil {
			generations = make(map[DurabilityGenerationID]DurabilityReceiptID)
			m.receiptGenerations[scope] = generations
		}
		generations[receipt.DurabilityGenerationID] = receipt.ID
	}
}

func (m *inMemory) receiptCanBecomeCurrent(receipt DurabilityReceipt) bool {
	scope := receiptAuthorityScope{
		policyDomainID: receipt.PolicyDomainID,
		contentID:      receipt.ContentID,
	}
	current, exists := m.currentReceipts[scope]
	if !exists {
		return receipt.Replaces == (DurabilityReplacementProof{})
	}
	if receipt == current {
		return true
	}
	if receipt.ID == current.ID || receipt.DurabilityGenerationID == current.DurabilityGenerationID ||
		receipt.Replaces.ReceiptID != current.ID ||
		receipt.Replaces.GenerationID != current.DurabilityGenerationID {
		return false
	}
	if _, reused := m.receiptGenerations[scope][receipt.DurabilityGenerationID]; reused {
		return false
	}
	return true
}

func sameContentReferences(left, right []ContentReference) bool {
	if len(left) != len(right) {
		return false
	}
	leftCanonical := append([]ContentReference(nil), left...)
	rightCanonical := append([]ContentReference(nil), right...)
	sort.Slice(leftCanonical, func(i, j int) bool { return leftCanonical[i].ID < leftCanonical[j].ID })
	sort.Slice(rightCanonical, func(i, j int) bool { return rightCanonical[i].ID < rightCanonical[j].ID })
	for index := range leftCanonical {
		if leftCanonical[index] != rightCanonical[index] {
			return false
		}
	}
	return true
}

func (m *inMemory) receiptsRespectKnownIdentity(expected, verified []DurabilityReceipt) bool {
	knownByID := make(map[DurabilityReceiptID]DurabilityReceipt, len(expected))
	knownByContent := make(map[ContentID]DurabilityReceipt, len(expected))
	for _, receipt := range expected {
		knownByID[receipt.ID] = receipt
		knownByContent[receipt.ContentID] = receipt
	}
	for _, receipt := range verified {
		if prior, exists := knownByID[receipt.ID]; exists && prior != receipt {
			return false
		}
		prior, exists := knownByContent[receipt.ContentID]
		if !exists {
			return false
		}
		if receipt.ID != prior.ID && receipt.DurabilityGenerationID == prior.DurabilityGenerationID {
			return false
		}
	}
	return true
}

func verifiedCheckpointContent(evidence CheckpointEvidence) VerifiedCheckpointContent {
	return VerifiedCheckpointContent{
		Manifest:           cloneCheckpointManifest(evidence.Manifest),
		ManifestReference:  evidence.ManifestReference,
		ContentReferences:  append([]ContentReference(nil), evidence.ContentReferences...),
		DurabilityReceipts: append([]DurabilityReceipt(nil), evidence.DurabilityReceipts...),
	}
}

func contentReferenceMatchesCheckpoint(
	reference ContentReference,
	referenceType ContentReferenceType,
	request CommitRuntimeViewRequest,
	revisionID RevisionID,
	checkpointID CheckpointID,
) bool {
	return reference.Type == referenceType &&
		reference.PolicyDomainID == request.PolicyDomainID && reference.TaskID == request.TaskID &&
		reference.TaskWorkspaceID == request.TaskWorkspaceID && reference.RevisionID == revisionID &&
		reference.CheckpointID == checkpointID && reference.OperationID == request.Operation.ID
}

func contentReferenceIsCanonical(reference ContentReference) bool {
	return reference.ID != "" && reference.ContentID != "" && validDigest(reference.ContentDigest) &&
		validDigest(reference.EvidenceDigest) && reference.EvidenceDigest == reference.CanonicalDigest()
}

func durabilityReceiptIsCanonical(receipt DurabilityReceipt) bool {
	replacementComplete := (receipt.Replaces.ReceiptID == "") == (receipt.Replaces.GenerationID == "")
	return receipt.ID != "" && receipt.DurabilityAuthorityID != "" && receipt.DurableWriteID != "" &&
		receipt.DurabilityAdapterID != "" && receipt.PolicyDomainID != "" && receipt.ContentID != "" && validDigest(receipt.ContentDigest) &&
		receipt.DurabilityGenerationID != "" &&
		(receipt.VerificationMethod == VerificationEndToEndChecksum ||
			receipt.VerificationMethod == VerificationIndependentReadback) &&
		replacementComplete && !receipt.VerifiedAt.IsZero() && receipt.Decision == DurabilityVerified &&
		validDigest(receipt.EvidenceDigest) && receipt.EvidenceDigest == receipt.CanonicalDigest()
}

func cloneDeclaredStateManifest(manifest DeclaredStateManifest) DeclaredStateManifest {
	clone := manifest
	clone.Members = append([]DeclaredStateMember(nil), manifest.Members...)
	return clone
}

func cloneCheckpointManifest(manifest CheckpointManifest) CheckpointManifest {
	clone := manifest
	clone.Members = append([]CheckpointManifestMember(nil), manifest.Members...)
	return clone
}

func cloneCheckpointEvidence(evidence CheckpointEvidence) CheckpointEvidence {
	clone := evidence
	clone.Manifest = cloneCheckpointManifest(evidence.Manifest)
	clone.ContentReferences = append([]ContentReference(nil), evidence.ContentReferences...)
	clone.DurabilityReceipts = append([]DurabilityReceipt(nil), evidence.DurabilityReceipts...)
	return clone
}

func checkpointManifestMatchesDeclaration(manifest CheckpointManifest, declared DeclaredStateManifest) bool {
	if !validDigest(manifest.Digest) || manifest.Digest != manifest.CanonicalDigest() ||
		manifest.DeclaredStateDigest != declared.Digest || len(manifest.Members) != len(declared.Members) {
		return false
	}
	declaredByID := make(map[StateMemberID]DeclaredStateMember, len(declared.Members))
	for _, member := range declared.Members {
		declaredByID[member.ID] = member
	}
	seenMembers := make(map[StateMemberID]struct{}, len(manifest.Members))
	seenLogicalMembers := make(map[LogicalMember]struct{}, len(manifest.Members))
	for _, member := range manifest.Members {
		declaredMember, exists := declaredByID[member.ID]
		if !exists || member.ContentID == "" || member.LogicalMember != declaredMember.LogicalMember ||
			member.Type != declaredMember.Type || member.Mode != declaredMember.Mode || member.Class != declaredMember.Class ||
			member.ContentDigest != declaredMember.ContentDigest || member.Size != declaredMember.Size {
			return false
		}
		if _, duplicate := seenMembers[member.ID]; duplicate {
			return false
		}
		seenMembers[member.ID] = struct{}{}
		if _, duplicate := seenLogicalMembers[member.LogicalMember]; duplicate {
			return false
		}
		seenLogicalMembers[member.LogicalMember] = struct{}{}
	}
	return true
}

func cloneOperationResult[T any](value T) T {
	switch result := any(value).(type) {
	case CommitRuntimeViewResult:
		result.CheckpointEvidence = cloneCheckpointEvidence(result.CheckpointEvidence)
		return any(result).(T)
	case MaterializeResult:
		result.CheckpointEvidence = cloneCheckpointEvidence(result.CheckpointEvidence)
		return any(result).(T)
	default:
		return value
	}
}

func validateDeclaredStateManifest(manifest DeclaredStateManifest) (EvidenceRoot, bool) {
	if !validDigest(manifest.Digest) || manifest.Digest != manifest.CanonicalDigest() {
		return "", false
	}
	members := manifest.canonicalValue().Members
	seenMembers := make(map[StateMemberID]struct{}, len(members))
	seenLogicalMembers := make(map[LogicalMember]struct{}, len(members))
	for _, member := range members {
		if member.ID == "" || !safeLogicalMember(member.LogicalMember) ||
			member.Type != StateMemberRegularFile || member.Mode == 0 || member.Mode&^uint32(0o777) != 0 ||
			member.Class != StateMemberTaskOwnedMutable || !validDigest(member.ContentDigest) {
			return "", false
		}
		if _, duplicate := seenMembers[member.ID]; duplicate {
			return "", false
		}
		seenMembers[member.ID] = struct{}{}
		if _, duplicate := seenLogicalMembers[member.LogicalMember]; duplicate {
			return "", false
		}
		seenLogicalMembers[member.LogicalMember] = struct{}{}
	}
	contentRoot := canonicalDigest(struct {
		Members []struct {
			ID            StateMemberID
			LogicalMember LogicalMember
			Type          StateMemberType
			Mode          uint32
			Class         StateMemberClass
			ContentDigest Digest
			Size          uint64
		}
	}{Members: contentFacts(members)})
	return EvidenceRoot(contentRoot), true
}

func safeLogicalMember(member LogicalMember) bool {
	value := string(member)
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.Contains(value, "\\") || strings.Contains(value, ":") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func contentFacts(members []DeclaredStateMember) []struct {
	ID            StateMemberID
	LogicalMember LogicalMember
	Type          StateMemberType
	Mode          uint32
	Class         StateMemberClass
	ContentDigest Digest
	Size          uint64
} {
	facts := make([]struct {
		ID            StateMemberID
		LogicalMember LogicalMember
		Type          StateMemberType
		Mode          uint32
		Class         StateMemberClass
		ContentDigest Digest
		Size          uint64
	}, len(members))
	for i, member := range members {
		facts[i] = struct {
			ID            StateMemberID
			LogicalMember LogicalMember
			Type          StateMemberType
			Mode          uint32
			Class         StateMemberClass
			ContentDigest Digest
			Size          uint64
		}{member.ID, member.LogicalMember, member.Type, member.Mode, member.Class, member.ContentDigest, member.Size}
	}
	return facts
}

func validDigest(digest Digest) bool {
	const prefix = "sha256:"
	value := string(digest)
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func (m *inMemory) ConfirmTaskWorkspace(_ context.Context, request ConfirmTaskWorkspaceRequest) (ConfirmTaskWorkspaceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if request.PolicyDomainID == "" || request.TaskID == "" || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return ConfirmTaskWorkspaceResult{}, &Error{Code: ErrorInvalidIntent}
	}
	if result, replayed, err := replayOperation[ConfirmTaskWorkspaceResult](m.operations, request.PolicyDomainID, request.TaskID, request.Operation); replayed {
		return result, err
	}
	if existing, ok := m.workspaces[request.TaskID]; ok {
		if existing.policyDomainID != request.PolicyDomainID {
			err := &Error{Code: ErrorOwnershipDenied}
			recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, ConfirmTaskWorkspaceResult{}, err)
			return ConfirmTaskWorkspaceResult{}, err
		}
		result := confirmResult(existing)
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
		return result, nil
	}

	emptyManifest := DeclaredStateManifest{}
	binding := workspaceBinding{
		policyDomainID:    request.PolicyDomainID,
		taskWorkspaceID:   TaskWorkspaceID(m.nextOpaqueID("workspace")),
		currentRevisionID: RevisionID(m.nextOpaqueID("revision")),
		currentManifest:   emptyManifest.CanonicalDigest(),
		generation:        1,
		fence:             1,
	}
	m.workspaces[request.TaskID] = binding
	m.revisions[binding.currentRevisionID] = revisionRecord{
		taskWorkspaceID: binding.taskWorkspaceID,
		manifestDigest:  binding.currentManifest,
	}
	result := confirmResult(binding)
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

func (m *inMemory) Materialize(ctx context.Context, request MaterializeRequest) (MaterializeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RevisionID == "" || request.Generation == 0 || request.Fence == 0 ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return MaterializeResult{}, &Error{Code: ErrorInvalidIntent}
	}
	if result, replayed, err := replayOperation[MaterializeResult](m.operations, request.PolicyDomainID, request.TaskID, request.Operation); replayed {
		return result, err
	}
	workspace, ok := m.workspaces[request.TaskID]
	if !ok || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID {
		err := &Error{Code: ErrorOwnershipDenied}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, MaterializeResult{}, err)
		return MaterializeResult{}, err
	}
	if workspace.currentRevisionID != request.RevisionID || workspace.currentCheckpointID != request.CheckpointID ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		err := &Error{Code: ErrorStaleAuthority}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, MaterializeResult{}, err)
		return MaterializeResult{}, err
	}

	checkpointEvidence := CheckpointEvidence{}
	checkpointID := request.CheckpointID
	if checkpointID != "" {
		checkpoint, exists := m.checkpoints[checkpointID]
		if !exists || checkpoint.taskWorkspaceID != request.TaskWorkspaceID || checkpoint.revisionID != request.RevisionID ||
			checkpoint.manifestDigest != workspace.currentManifest || m.durableObject == nil {
			err := &Error{Code: ErrorIntegrityFailure}
			recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, MaterializeResult{}, err)
			return MaterializeResult{}, err
		}
		verified, verifyErr := m.durableObject.VerifyCheckpoint(ctx, VerifyCheckpointContentRequest{
			PolicyDomainID:    request.PolicyDomainID,
			TaskID:            request.TaskID,
			TaskWorkspaceID:   request.TaskWorkspaceID,
			RevisionID:        request.RevisionID,
			CheckpointID:      checkpointID,
			Manifest:          cloneCheckpointManifest(checkpoint.evidence.Manifest),
			CanonicalManifest: checkpoint.evidence.Manifest.CanonicalBytes(),
			Expected:          verifiedCheckpointContent(checkpoint.evidence),
			Generation:        request.Generation,
			Fence:             request.Fence,
			Operation:         request.Operation,
		})
		if verifyErr != nil {
			err := &Error{Code: ErrorIntegrityFailure}
			recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, MaterializeResult{}, err)
			return MaterializeResult{}, err
		}
		var trusted bool
		checkpointEvidence, trusted = m.reverifyCheckpointEvidence(request, checkpoint.evidence, verified)
		if !trusted {
			err := &Error{Code: ErrorIntegrityFailure}
			recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, MaterializeResult{}, err)
			return MaterializeResult{}, err
		}
		checkpoint.evidence = cloneCheckpointEvidence(checkpointEvidence)
		m.checkpoints[checkpointID] = checkpoint
		m.recordDurableEvidenceIdentities(verifiedCheckpointContent(checkpointEvidence))
	}

	identity := MaterializationID(m.nextOpaqueID("materialization"))
	m.materializations[identity] = materializationBinding{
		policyDomainID:  request.PolicyDomainID,
		taskID:          request.TaskID,
		taskWorkspaceID: request.TaskWorkspaceID,
		revisionID:      request.RevisionID,
		checkpointID:    request.CheckpointID,
		generation:      request.Generation,
		fence:           request.Fence,
	}
	result := MaterializeResult{
		MaterializationID:      identity,
		TaskWorkspaceID:        request.TaskWorkspaceID,
		RevisionID:             request.RevisionID,
		CheckpointID:           checkpointID,
		ManifestDigest:         workspace.currentManifest,
		ContentEvidenceRoot:    checkpointEvidence.IntegrityEvidence.ContentEvidenceRoot,
		DurabilityEvidenceRoot: checkpointEvidence.IntegrityEvidence.DurabilityEvidenceRoot,
		CheckpointEvidence:     checkpointEvidence,
		Generation:             request.Generation,
		Fence:                  request.Fence,
	}
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

func confirmResult(binding workspaceBinding) ConfirmTaskWorkspaceResult {
	return ConfirmTaskWorkspaceResult{
		TaskWorkspaceID:     binding.taskWorkspaceID,
		CurrentRevisionID:   binding.currentRevisionID,
		CurrentCheckpointID: binding.currentCheckpointID,
		Generation:          binding.generation,
		Fence:               binding.fence,
	}
}

func (m *inMemory) nextOpaqueID(kind string) string {
	m.nextID++
	return fmt.Sprintf("%s-%016x", kind, m.nextID)
}

func replayOperation[T any](
	records map[operationScope]operationRecord,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operation Operation,
) (T, bool, error) {
	var zero T
	record, ok := records[operationScope{
		policyDomainID: policyDomainID,
		taskID:         taskID,
		operationID:    operation.ID,
	}]
	if !ok {
		return zero, false, nil
	}
	if record.requestDigest != operation.RequestDigest {
		return zero, true, &Error{Code: ErrorIntegrityConflict}
	}
	result, ok := record.result.(T)
	if !ok {
		return zero, true, &Error{Code: ErrorIntegrityConflict}
	}
	if record.err != nil {
		return cloneOperationResult(result), true, cloneLifecycleError(record.err)
	}
	return cloneOperationResult(result), true, nil
}

func recordOperation[T any](
	records map[operationScope]operationRecord,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operation Operation,
	result T,
	err *Error,
) {
	records[operationScope{
		policyDomainID: policyDomainID,
		taskID:         taskID,
		operationID:    operation.ID,
	}] = operationRecord{
		requestDigest: operation.RequestDigest,
		result:        cloneOperationResult(result),
		err:           cloneLifecycleError(err),
	}
}

func cloneLifecycleError(err *Error) *Error {
	if err == nil {
		return nil
	}
	clone := *err
	return &clone
}
