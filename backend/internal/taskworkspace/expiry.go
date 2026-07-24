package taskworkspace

import "context"

type ExpiryPolicy struct {
	ID                      ExpiryPolicyID
	MaterializationLifetime Duration
	RuntimeViewLifetime     Duration
}

type (
	ExpiryTargetKind     string
	IntegrityIncidentID  string
	RetentionReferenceID string
)

const (
	ExpiryTargetMaterialization ExpiryTargetKind = "materialization"
	ExpiryTargetRuntimeView     ExpiryTargetKind = "runtime_view"
)

type InspectExpiryProtectionRequest struct {
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	TaskWorkspaceID   TaskWorkspaceID
	TargetKind        ExpiryTargetKind
	MaterializationID MaterializationID
	RuntimeViewID     RuntimeViewID
	Generation        Generation
	Fence             Fence
	OperationID       OperationID
}

type ExpiryProtection struct {
	IntegrityIncidentID  IntegrityIncidentID
	RetentionReferenceID RetentionReferenceID
}

type ExpiryProtectionPort interface {
	InspectExpiryProtection(context.Context, InspectExpiryProtectionRequest) (ExpiryProtection, error)
}

type ExpireMaterializationRequest struct {
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	TaskWorkspaceID   TaskWorkspaceID
	MaterializationID MaterializationID
	RevisionID        RevisionID
	CheckpointID      CheckpointID
	Generation        Generation
	Fence             Fence
	ExpiryPolicyID    ExpiryPolicyID
	Operation         Operation
}

func (r ExpireMaterializationRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind              string
		PolicyDomainID    PolicyDomainID
		TaskID            TaskID
		TaskWorkspaceID   TaskWorkspaceID
		MaterializationID MaterializationID
		RevisionID        RevisionID
		CheckpointID      CheckpointID
		Generation        Generation
		Fence             Fence
		ExpiryPolicyID    ExpiryPolicyID
		OperationID       OperationID
	}{
		Kind:              "expire_materialization",
		PolicyDomainID:    r.PolicyDomainID,
		TaskID:            r.TaskID,
		TaskWorkspaceID:   r.TaskWorkspaceID,
		MaterializationID: r.MaterializationID,
		RevisionID:        r.RevisionID,
		CheckpointID:      r.CheckpointID,
		Generation:        r.Generation,
		Fence:             r.Fence,
		ExpiryPolicyID:    r.ExpiryPolicyID,
		OperationID:       r.Operation.ID,
	})
}

type ExpireMaterializationResult struct {
	TaskWorkspaceID   TaskWorkspaceID
	MaterializationID MaterializationID
	RevisionID        RevisionID
	CheckpointID      CheckpointID
	Generation        Generation
	Fence             Fence
	ExpiryPolicyID    ExpiryPolicyID
	ExpiredAt         Instant
	Operation         Operation
}

type ExpireRuntimeViewRequest struct {
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	TaskWorkspaceID   TaskWorkspaceID
	RuntimeViewID     RuntimeViewID
	MaterializationID MaterializationID
	BaseRevisionID    RevisionID
	Generation        Generation
	Fence             Fence
	ExpiryPolicyID    ExpiryPolicyID
	Operation         Operation
}

func (r ExpireRuntimeViewRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind              string
		PolicyDomainID    PolicyDomainID
		TaskID            TaskID
		TaskWorkspaceID   TaskWorkspaceID
		RuntimeViewID     RuntimeViewID
		MaterializationID MaterializationID
		BaseRevisionID    RevisionID
		Generation        Generation
		Fence             Fence
		ExpiryPolicyID    ExpiryPolicyID
		OperationID       OperationID
	}{
		Kind:              "expire_runtime_view",
		PolicyDomainID:    r.PolicyDomainID,
		TaskID:            r.TaskID,
		TaskWorkspaceID:   r.TaskWorkspaceID,
		RuntimeViewID:     r.RuntimeViewID,
		MaterializationID: r.MaterializationID,
		BaseRevisionID:    r.BaseRevisionID,
		Generation:        r.Generation,
		Fence:             r.Fence,
		ExpiryPolicyID:    r.ExpiryPolicyID,
		OperationID:       r.Operation.ID,
	})
}

type ExpireRuntimeViewResult struct {
	TaskWorkspaceID TaskWorkspaceID
	RuntimeViewID   RuntimeViewID
	BaseRevisionID  RevisionID
	Generation      Generation
	Fence           Fence
	ExpiryPolicyID  ExpiryPolicyID
	ExpiredAt       Instant
	Operation       Operation
}

func (m *inMemory) ExpireMaterialization(
	ctx context.Context,
	request ExpireMaterializationRequest,
) (ExpireMaterializationResult, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.MaterializationID == "" || request.RevisionID == "" || request.Generation == 0 ||
		request.Fence == 0 || request.ExpiryPolicyID == "" || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return ExpireMaterializationResult{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[ExpireMaterializationResult](
		m.operations, scope, request.Operation,
	); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, expireMaterializationJournalSpec(), nil,
	); err != nil {
		return ExpireMaterializationResult{}, err
	}
	fail := func(code ErrorCode) (ExpireMaterializationResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, ExpireMaterializationResult{}, err)
		return ExpireMaterializationResult{}, err
	}
	workspace, workspaceOK := m.workspaces[request.TaskID]
	materialization, materializationOK := m.materializations[request.MaterializationID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID || !materializationOK ||
		materialization.policyDomainID != request.PolicyDomainID || materialization.taskID != request.TaskID ||
		materialization.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if materialization.revisionID != request.RevisionID || materialization.checkpointID != request.CheckpointID ||
		materialization.generation != request.Generation || materialization.fence != request.Fence ||
		materialization.expiryPolicyID != request.ExpiryPolicyID {
		return fail(ErrorStaleAuthority)
	}
	if materialization.expiresAt > m.now() {
		return fail(ErrorExpiryBlocked)
	}
	for _, view := range m.views {
		if view.materializationID == request.MaterializationID &&
			view.sandboxLeaseAuthority.ExpiresAt > m.now() &&
			m.sandboxLeaseAuthorityIsCurrent(view.sandboxLeaseAuthority) {
			return fail(ErrorExpiryBlocked)
		}
	}
	if m.hasPendingMaterializationOperation(request.MaterializationID, materialization) {
		return fail(ErrorExpiryBlocked)
	}
	if m.expiryProtection != nil {
		protection, err := m.expiryProtection.InspectExpiryProtection(ctx, InspectExpiryProtectionRequest{
			PolicyDomainID:    request.PolicyDomainID,
			TaskID:            request.TaskID,
			TaskWorkspaceID:   request.TaskWorkspaceID,
			TargetKind:        ExpiryTargetMaterialization,
			MaterializationID: request.MaterializationID,
			Generation:        request.Generation,
			Fence:             request.Fence,
			OperationID:       request.Operation.ID,
		})
		if err != nil || protection.IntegrityIncidentID != "" || protection.RetentionReferenceID != "" {
			return fail(ErrorExpiryBlocked)
		}
	}

	expiredAt := m.now()
	markOperationReconciliationRequired(m.operations, scope)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforePhysicalExpiry,
		OperationID: request.Operation.ID,
		SubjectID:   string(request.MaterializationID),
	}); err != nil {
		return ExpireMaterializationResult{}, err
	}
	delete(m.materializations, request.MaterializationID)
	result := ExpireMaterializationResult{
		TaskWorkspaceID:   request.TaskWorkspaceID,
		MaterializationID: request.MaterializationID,
		RevisionID:        request.RevisionID,
		CheckpointID:      request.CheckpointID,
		Generation:        request.Generation,
		Fence:             request.Fence,
		ExpiryPolicyID:    request.ExpiryPolicyID,
		ExpiredAt:         expiredAt,
		Operation:         request.Operation,
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterPhysicalExpiry,
		OperationID: request.Operation.ID,
		SubjectID:   string(request.MaterializationID),
	}); err != nil {
		return ExpireMaterializationResult{}, err
	}
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) ExpireRuntimeView(
	ctx context.Context,
	request ExpireRuntimeViewRequest,
) (ExpireRuntimeViewResult, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RuntimeViewID == "" || request.MaterializationID == "" || request.BaseRevisionID == "" ||
		request.Generation == 0 || request.Fence == 0 || request.ExpiryPolicyID == "" || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return ExpireRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[ExpireRuntimeViewResult](
		m.operations, scope, request.Operation,
	); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, expireRuntimeViewJournalSpec(), nil,
	); err != nil {
		return ExpireRuntimeViewResult{}, err
	}
	fail := func(code ErrorCode) (ExpireRuntimeViewResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, ExpireRuntimeViewResult{}, err)
		return ExpireRuntimeViewResult{}, err
	}
	workspace, workspaceOK := m.workspaces[request.TaskID]
	view, viewOK := m.views[request.RuntimeViewID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID || !viewOK ||
		view.policyDomainID != request.PolicyDomainID || view.taskID != request.TaskID ||
		view.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if view.materializationID != request.MaterializationID || view.baseRevisionID != request.BaseRevisionID ||
		view.generation != request.Generation || view.fence != request.Fence ||
		view.expiryPolicyID != request.ExpiryPolicyID {
		return fail(ErrorStaleAuthority)
	}
	if view.expired || view.expiresAt > m.now() ||
		(view.sandboxLeaseAuthority.ExpiresAt > m.now() && m.sandboxLeaseAuthorityIsCurrent(view.sandboxLeaseAuthority)) {
		return fail(ErrorExpiryBlocked)
	}
	if m.hasPendingRuntimeViewOperation(request.RuntimeViewID, view) {
		return fail(ErrorExpiryBlocked)
	}
	if m.expiryProtection != nil {
		protection, err := m.expiryProtection.InspectExpiryProtection(ctx, InspectExpiryProtectionRequest{
			PolicyDomainID:    request.PolicyDomainID,
			TaskID:            request.TaskID,
			TaskWorkspaceID:   request.TaskWorkspaceID,
			TargetKind:        ExpiryTargetRuntimeView,
			MaterializationID: request.MaterializationID,
			RuntimeViewID:     request.RuntimeViewID,
			Generation:        request.Generation,
			Fence:             request.Fence,
			OperationID:       request.Operation.ID,
		})
		if err != nil || protection.IntegrityIncidentID != "" || protection.RetentionReferenceID != "" {
			return fail(ErrorExpiryBlocked)
		}
	}

	markOperationReconciliationRequired(m.operations, scope)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforePhysicalExpiry,
		OperationID: request.Operation.ID,
		SubjectID:   string(request.RuntimeViewID),
	}); err != nil {
		return ExpireRuntimeViewResult{}, err
	}
	view.expired = true
	m.views[request.RuntimeViewID] = view
	result := ExpireRuntimeViewResult{
		TaskWorkspaceID: request.TaskWorkspaceID,
		RuntimeViewID:   request.RuntimeViewID,
		BaseRevisionID:  request.BaseRevisionID,
		Generation:      request.Generation,
		Fence:           request.Fence,
		ExpiryPolicyID:  request.ExpiryPolicyID,
		ExpiredAt:       m.now(),
		Operation:       request.Operation,
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterPhysicalExpiry,
		OperationID: request.Operation.ID,
		SubjectID:   string(request.RuntimeViewID),
	}); err != nil {
		return ExpireRuntimeViewResult{}, err
	}
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) hasPendingMaterializationOperation(
	materializationID MaterializationID,
	materialization materializationBinding,
) bool {
	for _, record := range m.operations {
		if record.state == operationJournalTerminal || record.payload == nil {
			continue
		}
		switch journal := record.payload.(type) {
		case *typedOperationJournal[CommitRuntimeViewRequest, CommitRuntimeViewResult]:
			view, ok := m.views[journal.request.RuntimeViewID]
			if ok && view.materializationID == materializationID {
				return true
			}
		case *typedOperationJournal[ReconstructTaskWorkspaceRequest, ReconstructTaskWorkspaceResult]:
			intent := journal.request.Intent
			if intent.PolicyDomainID == materialization.policyDomainID && intent.TaskID == materialization.taskID &&
				intent.TaskWorkspaceID == materialization.taskWorkspaceID {
				return true
			}
		case *typedOperationJournal[RestoreTaskWorkspaceRequest, RestoreTaskWorkspaceResult]:
			intent := journal.request.Intent
			if intent.PolicyDomainID == materialization.policyDomainID && intent.TaskID == materialization.taskID &&
				intent.TaskWorkspaceID == materialization.taskWorkspaceID {
				return true
			}
		}
	}
	return false
}

func (m *inMemory) hasPendingRuntimeViewOperation(
	runtimeViewID RuntimeViewID,
	view runtimeViewBinding,
) bool {
	for _, record := range m.operations {
		if record.state == operationJournalTerminal || record.payload == nil {
			continue
		}
		switch journal := record.payload.(type) {
		case *typedOperationJournal[CommitRuntimeViewRequest, CommitRuntimeViewResult]:
			if journal.request.RuntimeViewID == runtimeViewID {
				return true
			}
		case *typedOperationJournal[ReconstructTaskWorkspaceRequest, ReconstructTaskWorkspaceResult]:
			intent := journal.request.Intent
			if intent.PolicyDomainID == view.policyDomainID && intent.TaskID == view.taskID &&
				intent.TaskWorkspaceID == view.taskWorkspaceID {
				return true
			}
		case *typedOperationJournal[RestoreTaskWorkspaceRequest, RestoreTaskWorkspaceResult]:
			intent := journal.request.Intent
			if intent.PolicyDomainID == view.policyDomainID && intent.TaskID == view.taskID &&
				intent.TaskWorkspaceID == view.taskWorkspaceID {
				return true
			}
		}
	}
	return false
}
