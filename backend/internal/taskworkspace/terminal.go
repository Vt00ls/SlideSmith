package taskworkspace

import "context"

type RuntimeViewTerminalIntent string

const (
	RuntimeViewCommitIntent  RuntimeViewTerminalIntent = "commit"
	RuntimeViewDiscardIntent RuntimeViewTerminalIntent = "discard"
	RuntimeViewFenceIntent   RuntimeViewTerminalIntent = "fence"
)

type RuntimeViewTerminalAttempt struct {
	RuntimeViewID RuntimeViewID
	OperationID   OperationID
	Intent        RuntimeViewTerminalIntent
}

type RuntimeViewDiscardReason string

const (
	RuntimeViewValidationRejected RuntimeViewDiscardReason = "validation_rejected"
	RuntimeViewRuntimeFailed      RuntimeViewDiscardReason = "runtime_failed"
)

type RuntimeViewFenceReason string

const (
	RuntimeViewCancelled                  RuntimeViewFenceReason = "cancelled"
	RuntimeViewTimedOut                   RuntimeViewFenceReason = "timed_out"
	RuntimeViewRevoked                    RuntimeViewFenceReason = "revoked"
	RuntimeViewRecoveryGenerationAdvanced RuntimeViewFenceReason = "recovery_generation_advanced"
)

type DiscardRuntimeViewRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	RuntimeViewID           RuntimeViewID
	RuntimeOperationID      OperationID
	SandboxLeaseAuthority   SandboxLeaseAuthority
	BaseRevisionID          RevisionID
	ExpectedCurrentRevision RevisionID
	Generation              Generation
	Fence                   Fence
	Reason                  RuntimeViewDiscardReason
	Operation               Operation
}

func (r DiscardRuntimeViewRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		RuntimeViewID           RuntimeViewID
		RuntimeOperationID      OperationID
		SandboxLeaseAuthority   SandboxLeaseAuthority
		BaseRevisionID          RevisionID
		ExpectedCurrentRevision RevisionID
		Generation              Generation
		Fence                   Fence
		Reason                  RuntimeViewDiscardReason
		OperationID             OperationID
	}{
		Kind:                    "discard_runtime_view",
		PolicyDomainID:          r.PolicyDomainID,
		TaskID:                  r.TaskID,
		TaskWorkspaceID:         r.TaskWorkspaceID,
		RuntimeViewID:           r.RuntimeViewID,
		RuntimeOperationID:      r.RuntimeOperationID,
		SandboxLeaseAuthority:   r.SandboxLeaseAuthority,
		BaseRevisionID:          r.BaseRevisionID,
		ExpectedCurrentRevision: r.ExpectedCurrentRevision,
		Generation:              r.Generation,
		Fence:                   r.Fence,
		Reason:                  r.Reason,
		OperationID:             r.Operation.ID,
	})
}

type DiscardRuntimeViewResult struct {
	TaskWorkspaceID   TaskWorkspaceID
	RuntimeViewID     RuntimeViewID
	BaseRevisionID    RevisionID
	CurrentRevisionID RevisionID
	Reason            RuntimeViewDiscardReason
	Generation        Generation
	Fence             Fence
	Operation         Operation
}

func (m *inMemory) DiscardRuntimeView(
	_ context.Context,
	request DiscardRuntimeViewRequest,
) (DiscardRuntimeViewResult, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RuntimeViewID == "" || request.RuntimeOperationID == "" || request.SandboxLeaseAuthority.ID == "" ||
		request.BaseRevisionID == "" || request.ExpectedCurrentRevision == "" || request.Generation == 0 ||
		request.Fence == 0 || (request.Reason != RuntimeViewValidationRejected && request.Reason != RuntimeViewRuntimeFailed) ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return DiscardRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}
	m.beforeRuntimeViewTerminal(RuntimeViewTerminalAttempt{
		RuntimeViewID: request.RuntimeViewID,
		OperationID:   request.Operation.ID,
		Intent:        RuntimeViewDiscardIntent,
	})
	m.mu.Lock()
	defer m.mu.Unlock()

	if result, replayed, err := replayOperation[DiscardRuntimeViewResult](
		m.operations, request.PolicyDomainID, request.TaskID, request.Operation,
	); replayed {
		return result, err
	}
	fail := func(code ErrorCode) (DiscardRuntimeViewResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, DiscardRuntimeViewResult{}, err)
		return DiscardRuntimeViewResult{}, err
	}

	workspace, workspaceOK := m.workspaces[request.TaskID]
	view, viewOK := m.views[request.RuntimeViewID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID ||
		!viewOK || view.policyDomainID != request.PolicyDomainID || view.taskID != request.TaskID ||
		view.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if view.baseRevisionID != request.BaseRevisionID || view.generation != request.Generation || view.fence != request.Fence ||
		view.runtimeOperationID != request.RuntimeOperationID || view.sandboxLeaseAuthority != request.SandboxLeaseAuthority {
		return fail(ErrorStaleAuthority)
	}
	if view.terminalDecision != runtimeViewNonTerminal {
		return fail(ErrorViewTerminalConflict)
	}
	if workspace.currentRevisionID != request.ExpectedCurrentRevision || request.ExpectedCurrentRevision != request.BaseRevisionID ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		return fail(ErrorStaleAuthority)
	}

	view.terminalDecision = runtimeViewDiscarded
	m.views[request.RuntimeViewID] = view
	result := DiscardRuntimeViewResult{
		TaskWorkspaceID:   request.TaskWorkspaceID,
		RuntimeViewID:     request.RuntimeViewID,
		BaseRevisionID:    request.BaseRevisionID,
		CurrentRevisionID: workspace.currentRevisionID,
		Reason:            request.Reason,
		Generation:        request.Generation,
		Fence:             request.Fence,
		Operation:         request.Operation,
	}
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

type FenceRuntimeViewRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	RuntimeViewID           RuntimeViewID
	RuntimeOperationID      OperationID
	SandboxLeaseAuthority   SandboxLeaseAuthority
	BaseRevisionID          RevisionID
	ExpectedCurrentRevision RevisionID
	Generation              Generation
	Fence                   Fence
	Reason                  RuntimeViewFenceReason
	Operation               Operation
}

func (r FenceRuntimeViewRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		RuntimeViewID           RuntimeViewID
		RuntimeOperationID      OperationID
		SandboxLeaseAuthority   SandboxLeaseAuthority
		BaseRevisionID          RevisionID
		ExpectedCurrentRevision RevisionID
		Generation              Generation
		Fence                   Fence
		Reason                  RuntimeViewFenceReason
		OperationID             OperationID
	}{
		Kind:                    "fence_runtime_view",
		PolicyDomainID:          r.PolicyDomainID,
		TaskID:                  r.TaskID,
		TaskWorkspaceID:         r.TaskWorkspaceID,
		RuntimeViewID:           r.RuntimeViewID,
		RuntimeOperationID:      r.RuntimeOperationID,
		SandboxLeaseAuthority:   r.SandboxLeaseAuthority,
		BaseRevisionID:          r.BaseRevisionID,
		ExpectedCurrentRevision: r.ExpectedCurrentRevision,
		Generation:              r.Generation,
		Fence:                   r.Fence,
		Reason:                  r.Reason,
		OperationID:             r.Operation.ID,
	})
}

type FenceRuntimeViewResult struct {
	TaskWorkspaceID   TaskWorkspaceID
	RuntimeViewID     RuntimeViewID
	BaseRevisionID    RevisionID
	CurrentRevisionID RevisionID
	Reason            RuntimeViewFenceReason
	Generation        Generation
	PreviousFence     Fence
	Fence             Fence
	Operation         Operation
}

func (m *inMemory) FenceRuntimeView(
	_ context.Context,
	request FenceRuntimeViewRequest,
) (FenceRuntimeViewResult, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RuntimeViewID == "" || request.RuntimeOperationID == "" || request.SandboxLeaseAuthority.ID == "" ||
		request.BaseRevisionID == "" || request.ExpectedCurrentRevision == "" || request.Generation == 0 ||
		request.Fence == 0 || !validRuntimeViewFenceReason(request.Reason) || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return FenceRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}
	m.beforeRuntimeViewTerminal(RuntimeViewTerminalAttempt{
		RuntimeViewID: request.RuntimeViewID,
		OperationID:   request.Operation.ID,
		Intent:        RuntimeViewFenceIntent,
	})
	m.mu.Lock()
	defer m.mu.Unlock()

	if result, replayed, err := replayOperation[FenceRuntimeViewResult](
		m.operations, request.PolicyDomainID, request.TaskID, request.Operation,
	); replayed {
		return result, err
	}
	fail := func(code ErrorCode) (FenceRuntimeViewResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, FenceRuntimeViewResult{}, err)
		return FenceRuntimeViewResult{}, err
	}

	workspace, workspaceOK := m.workspaces[request.TaskID]
	view, viewOK := m.views[request.RuntimeViewID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID ||
		!viewOK || view.policyDomainID != request.PolicyDomainID || view.taskID != request.TaskID ||
		view.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if view.baseRevisionID != request.BaseRevisionID || view.generation != request.Generation || view.fence != request.Fence ||
		view.runtimeOperationID != request.RuntimeOperationID || view.sandboxLeaseAuthority != request.SandboxLeaseAuthority {
		return fail(ErrorStaleAuthority)
	}
	if view.terminalDecision != runtimeViewNonTerminal {
		return fail(ErrorViewTerminalConflict)
	}
	if workspace.currentRevisionID != request.ExpectedCurrentRevision || request.ExpectedCurrentRevision != request.BaseRevisionID ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		return fail(ErrorStaleAuthority)
	}

	workspace.fence++
	if request.Reason == RuntimeViewRecoveryGenerationAdvanced {
		workspace.generation++
	}
	m.workspaces[request.TaskID] = workspace
	view.generation = workspace.generation
	view.fence = workspace.fence
	view.terminalDecision = runtimeViewFenced
	m.views[request.RuntimeViewID] = view
	result := FenceRuntimeViewResult{
		TaskWorkspaceID:   request.TaskWorkspaceID,
		RuntimeViewID:     request.RuntimeViewID,
		BaseRevisionID:    request.BaseRevisionID,
		CurrentRevisionID: workspace.currentRevisionID,
		Reason:            request.Reason,
		Generation:        workspace.generation,
		PreviousFence:     request.Fence,
		Fence:             workspace.fence,
		Operation:         request.Operation,
	}
	recordOperation(m.operations, request.PolicyDomainID, request.TaskID, request.Operation, result, nil)
	return result, nil
}

func validRuntimeViewFenceReason(reason RuntimeViewFenceReason) bool {
	switch reason {
	case RuntimeViewCancelled, RuntimeViewTimedOut, RuntimeViewRevoked, RuntimeViewRecoveryGenerationAdvanced:
		return true
	default:
		return false
	}
}
