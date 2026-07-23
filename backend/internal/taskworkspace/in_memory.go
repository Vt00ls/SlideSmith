package taskworkspace

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

type InMemoryConfig struct {
	ValidationAuthorityID ValidationAuthorityID
}

type inMemory struct {
	mu                    sync.Mutex
	nextID                uint64
	validationAuthorityID ValidationAuthorityID
	workspaces            map[TaskID]workspaceBinding
	materializations      map[MaterializationID]materializationBinding
	views                 map[RuntimeViewID]runtimeViewBinding
	revisions             map[RevisionID]revisionRecord
	checkpoints           map[CheckpointID]checkpointRecord
	operations            map[operationScope]operationRecord
}

type workspaceBinding struct {
	policyDomainID    PolicyDomainID
	taskWorkspaceID   TaskWorkspaceID
	currentRevisionID RevisionID
	currentManifest   Digest
	generation        Generation
	fence             Fence
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
}

type materializationBinding struct {
	policyDomainID  PolicyDomainID
	taskID          TaskID
	taskWorkspaceID TaskWorkspaceID
	revisionID      RevisionID
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
	return &inMemory{
		validationAuthorityID: config.ValidationAuthorityID,
		workspaces:            make(map[TaskID]workspaceBinding),
		materializations:      make(map[MaterializationID]materializationBinding),
		views:                 make(map[RuntimeViewID]runtimeViewBinding),
		revisions:             make(map[RevisionID]revisionRecord),
		checkpoints:           make(map[CheckpointID]checkpointRecord),
		operations:            make(map[operationScope]operationRecord),
	}
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
		materialization.revisionID != request.BaseRevisionID || materialization.generation != request.Generation || materialization.fence != request.Fence {
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

func (m *inMemory) CommitRuntimeView(_ context.Context, request CommitRuntimeViewRequest) (CommitRuntimeViewResult, error) {
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
	if !m.validationEvidenceMatches(request.ValidationEvidence, request, view) {
		return fail(ErrorIntegrityFailure)
	}
	contentRoot, durabilityRoot, ok := validateDeclaredStateManifest(request.DeclaredStateManifest)
	if !ok {
		return fail(ErrorIntegrityFailure)
	}

	predecessor := workspace.currentRevisionID
	resultingRevision := predecessor
	if request.DeclaredStateManifest.Digest != workspace.currentManifest {
		resultingRevision = RevisionID(m.nextOpaqueID("revision"))
		m.revisions[resultingRevision] = revisionRecord{
			taskWorkspaceID: request.TaskWorkspaceID,
			manifestDigest:  request.DeclaredStateManifest.Digest,
			predecessor:     predecessor,
		}
		workspace.currentRevisionID = resultingRevision
		workspace.currentManifest = request.DeclaredStateManifest.Digest
		m.workspaces[request.TaskID] = workspace
	}
	checkpointID := CheckpointID(m.nextOpaqueID("checkpoint"))
	m.checkpoints[checkpointID] = checkpointRecord{
		taskWorkspaceID: request.TaskWorkspaceID,
		revisionID:      resultingRevision,
		manifestDigest:  request.DeclaredStateManifest.Digest,
		operationID:     request.Operation.ID,
	}
	view.terminal = true
	m.views[request.RuntimeViewID] = view

	result := CommitRuntimeViewResult{
		TaskWorkspaceID:          request.TaskWorkspaceID,
		RevisionID:               resultingRevision,
		CheckpointID:             checkpointID,
		BaseRevisionID:           request.BaseRevisionID,
		PredecessorRevisionID:    predecessor,
		ManifestDigest:           request.DeclaredStateManifest.Digest,
		ValidationEvidenceID:     request.ValidationEvidence.ID,
		ValidationEvidenceDigest: request.ValidationEvidence.Digest,
		ContentEvidenceRoot:      contentRoot,
		DurabilityEvidenceRoot:   durabilityRoot,
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

func validateDeclaredStateManifest(manifest DeclaredStateManifest) (EvidenceRoot, EvidenceRoot, bool) {
	if !validDigest(manifest.Digest) || manifest.Digest != manifest.CanonicalDigest() {
		return "", "", false
	}
	members := manifest.canonicalValue().Members
	seen := make(map[StateMemberID]struct{}, len(members))
	for _, member := range members {
		if member.ID == "" || member.ContentID == "" || !validDigest(member.ContentDigest) ||
			member.DurabilityEvidence.ID == "" || !validDigest(member.DurabilityEvidence.Digest) ||
			member.DurabilityEvidence.Digest != member.DurabilityEvidence.CanonicalDigest() ||
			member.DurabilityEvidence.ContentID != member.ContentID ||
			member.DurabilityEvidence.ContentDigest != member.ContentDigest ||
			member.DurabilityEvidence.Size != member.Size ||
			member.DurabilityEvidence.Decision != DurabilityVerified {
			return "", "", false
		}
		if _, duplicate := seen[member.ID]; duplicate {
			return "", "", false
		}
		seen[member.ID] = struct{}{}
	}
	contentRoot := canonicalDigest(struct {
		Members []struct {
			ID            StateMemberID
			ContentID     ContentID
			ContentDigest Digest
			Size          uint64
		}
	}{Members: contentFacts(members)})
	durabilityRoot := canonicalDigest(struct {
		Evidence []DurabilityEvidence
	}{Evidence: durabilityFacts(members)})
	return EvidenceRoot(contentRoot), EvidenceRoot(durabilityRoot), true
}

func contentFacts(members []DeclaredStateMember) []struct {
	ID            StateMemberID
	ContentID     ContentID
	ContentDigest Digest
	Size          uint64
} {
	facts := make([]struct {
		ID            StateMemberID
		ContentID     ContentID
		ContentDigest Digest
		Size          uint64
	}, len(members))
	for i, member := range members {
		facts[i] = struct {
			ID            StateMemberID
			ContentID     ContentID
			ContentDigest Digest
			Size          uint64
		}{member.ID, member.ContentID, member.ContentDigest, member.Size}
	}
	return facts
}

func durabilityFacts(members []DeclaredStateMember) []DurabilityEvidence {
	facts := make([]DurabilityEvidence, len(members))
	for i, member := range members {
		facts[i] = member.DurabilityEvidence
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

func (m *inMemory) Materialize(_ context.Context, request MaterializeRequest) (MaterializeResult, error) {
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
	if workspace.currentRevisionID != request.RevisionID || workspace.generation != request.Generation || workspace.fence != request.Fence {
		err := &Error{Code: ErrorStaleAuthority}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, MaterializeResult{}, err)
		return MaterializeResult{}, err
	}

	identity := MaterializationID(m.nextOpaqueID("materialization"))
	m.materializations[identity] = materializationBinding{
		policyDomainID:  request.PolicyDomainID,
		taskID:          request.TaskID,
		taskWorkspaceID: request.TaskWorkspaceID,
		revisionID:      request.RevisionID,
		generation:      request.Generation,
		fence:           request.Fence,
	}
	result := MaterializeResult{
		MaterializationID: identity,
		TaskWorkspaceID:   request.TaskWorkspaceID,
		RevisionID:        request.RevisionID,
		Generation:        request.Generation,
		Fence:             request.Fence,
	}
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

func confirmResult(binding workspaceBinding) ConfirmTaskWorkspaceResult {
	return ConfirmTaskWorkspaceResult{
		TaskWorkspaceID:   binding.taskWorkspaceID,
		CurrentRevisionID: binding.currentRevisionID,
		Generation:        binding.generation,
		Fence:             binding.fence,
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
		return result, true, record.err
	}
	return result, true, nil
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
		result:        result,
		err:           err,
	}
}
