package taskworkspace

import "context"

type localCompletionOperationKind string
type localCompletionPhase string
type localCompletionState string

const (
	localCompletionCommitRuntimeView localCompletionOperationKind = "commit_runtime_view"
	localCompletionMaterialize       localCompletionOperationKind = "materialize"
	localCompletionRestore           localCompletionOperationKind = "restore_task_workspace"
	localCompletionReconstruct       localCompletionOperationKind = "reconstruct_task_workspace"
	localCompletionExpire            localCompletionOperationKind = "expire_materialization"

	localCompletionCheckpointActivation   localCompletionPhase = "checkpoint_activation"
	localCompletionMaterializationBinding localCompletionPhase = "materialization_binding"
	localCompletionMaterializationExpiry  localCompletionPhase = "materialization_expiry"

	localCompletionPending   localCompletionState = "pending"
	localCompletionCompleted localCompletionState = "completed"
)

// localCompletionRecord is adapter-private durable mechanics. The core
// operation journal remains the sole lifecycle business authority.
type localCompletionRecord struct {
	OperationID             OperationID                  `json:"operation_id"`
	RequestDigest           Digest                       `json:"request_digest"`
	Kind                    localCompletionOperationKind `json:"kind"`
	Phase                   localCompletionPhase         `json:"phase"`
	PolicyDomainID          PolicyDomainID               `json:"policy_domain_id"`
	TaskID                  TaskID                       `json:"task_id"`
	TaskWorkspaceID         TaskWorkspaceID              `json:"task_workspace_id"`
	ExpectedRevisionID      RevisionID                   `json:"expected_revision_id"`
	Generation              Generation                   `json:"generation"`
	Fence                   Fence                        `json:"fence"`
	AuthorityBindingsDigest Digest                       `json:"authority_bindings_digest"`
	RevisionID              RevisionID                   `json:"revision_id,omitempty"`
	CheckpointID            CheckpointID                 `json:"checkpoint_id,omitempty"`
	MaterializationID       MaterializationID            `json:"materialization_id,omitempty"`
	ExpiryPolicyID          ExpiryPolicyID               `json:"expiry_policy_id,omitempty"`
	State                   localCompletionState         `json:"state"`
	ResultDigest            Digest                       `json:"result_digest,omitempty"`
	CompletionEvidence      Digest                       `json:"completion_evidence,omitempty"`
}

func localExpireCompletionIntent(request ExpireMaterializationRequest) localCompletionRecord {
	metadata := expireMaterializationIntentMetadata(request)
	return localCompletionRecord{
		OperationID: request.Operation.ID, RequestDigest: request.Operation.RequestDigest,
		Kind: localCompletionExpire, Phase: localCompletionMaterializationExpiry,
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, ExpectedRevisionID: metadata.expectedRevisionID,
		Generation: metadata.generation, Fence: metadata.fence,
		AuthorityBindingsDigest: metadata.authorityBindingsDigest,
		RevisionID:              request.RevisionID, CheckpointID: request.CheckpointID,
		MaterializationID: request.MaterializationID, ExpiryPolicyID: request.ExpiryPolicyID,
		State: localCompletionPending,
	}
}

func localMaterializeCompletionIntent(request MaterializeRequest) localCompletionRecord {
	metadata := materializeIntentMetadata(request)
	return localCompletionRecord{
		OperationID: request.Operation.ID, RequestDigest: request.Operation.RequestDigest,
		Kind: localCompletionMaterialize, Phase: localCompletionMaterializationBinding,
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, ExpectedRevisionID: metadata.expectedRevisionID,
		Generation: metadata.generation, Fence: metadata.fence,
		AuthorityBindingsDigest: metadata.authorityBindingsDigest, State: localCompletionPending,
	}
}

func localRestoreCompletionIntent(request RestoreTaskWorkspaceRequest) localCompletionRecord {
	metadata := restoreTaskWorkspaceIntentMetadata(request)
	return localCompletionRecord{
		OperationID: request.Operation.ID, RequestDigest: request.Operation.RequestDigest,
		Kind: localCompletionRestore, Phase: localCompletionMaterializationBinding,
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		TaskWorkspaceID: request.Intent.TaskWorkspaceID, ExpectedRevisionID: metadata.expectedRevisionID,
		Generation: metadata.generation, Fence: metadata.fence,
		AuthorityBindingsDigest: metadata.authorityBindingsDigest, State: localCompletionPending,
	}
}

func localReconstructCompletionIntent(request ReconstructTaskWorkspaceRequest) localCompletionRecord {
	metadata := reconstructTaskWorkspaceIntentMetadata(request)
	return localCompletionRecord{
		OperationID: request.Operation.ID, RequestDigest: request.Operation.RequestDigest,
		Kind: localCompletionReconstruct, Phase: localCompletionMaterializationBinding,
		PolicyDomainID: request.Intent.PolicyDomainID, TaskID: request.Intent.TaskID,
		TaskWorkspaceID: request.Intent.TaskWorkspaceID, ExpectedRevisionID: metadata.expectedRevisionID,
		Generation: metadata.generation, Fence: metadata.fence,
		AuthorityBindingsDigest: metadata.authorityBindingsDigest, State: localCompletionPending,
	}
}

func localCommitCompletionIntent(request CommitRuntimeViewRequest) localCompletionRecord {
	metadata := commitRuntimeViewIntentMetadata(request)
	return localCompletionRecord{
		OperationID: request.Operation.ID, RequestDigest: request.Operation.RequestDigest,
		Kind: localCompletionCommitRuntimeView, Phase: localCompletionCheckpointActivation,
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, ExpectedRevisionID: metadata.expectedRevisionID,
		Generation: metadata.generation, Fence: metadata.fence,
		AuthorityBindingsDigest: metadata.authorityBindingsDigest, State: localCompletionPending,
	}
}

func localCompletionScopeKey(policyDomainID PolicyDomainID, taskID TaskID, operationID OperationID) string {
	return string(policyDomainID) + "\x00" + string(taskID) + "\x00" + string(operationID)
}

func (r localCompletionRecord) scopeKey() string {
	return localCompletionScopeKey(r.PolicyDomainID, r.TaskID, r.OperationID)
}

func (r localCompletionRecord) intentDigest() Digest {
	return canonicalDigest(struct {
		OperationID             OperationID
		RequestDigest           Digest
		Kind                    localCompletionOperationKind
		Phase                   localCompletionPhase
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		ExpectedRevisionID      RevisionID
		Generation              Generation
		Fence                   Fence
		AuthorityBindingsDigest Digest
		RevisionID              RevisionID
		CheckpointID            CheckpointID
		MaterializationID       MaterializationID
		ExpiryPolicyID          ExpiryPolicyID
	}{
		r.OperationID, r.RequestDigest, r.Kind, r.Phase, r.PolicyDomainID, r.TaskID,
		r.TaskWorkspaceID, r.ExpectedRevisionID, r.Generation, r.Fence,
		r.AuthorityBindingsDigest, r.RevisionID, r.CheckpointID, r.MaterializationID,
		r.ExpiryPolicyID,
	})
}

func (r localCompletionRecord) completionEvidence(resultDigest Digest) Digest {
	return canonicalDigest(struct {
		IntentDigest Digest
		ResultDigest Digest
		State        localCompletionState
	}{r.intentDigest(), resultDigest, localCompletionCompleted})
}

func (s *localFilesystemStore) ensureCompletionIntent(intent localCompletionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if intent.OperationID == "" || intent.RequestDigest == "" || intent.PolicyDomainID == "" ||
		intent.TaskID == "" || intent.Kind == "" || intent.Phase == "" || intent.Generation == 0 ||
		intent.Fence == 0 || intent.AuthorityBindingsDigest == "" {
		return &Error{Code: ErrorInvalidIntent}
	}
	key := intent.scopeKey()
	if existing, ok := s.state.Completions[key]; ok {
		if existing.intentDigest() != intent.intentDigest() {
			return &Error{Code: ErrorIntegrityConflict}
		}
		return nil
	}
	s.state.Completions[key] = intent
	if err := s.inject(LocalFaultAfterCompletionMutation, LocalFilesystemFaultEvent{
		OperationID: intent.OperationID, SubjectID: string(intent.Kind), Ordinal: 1,
	}); err != nil {
		delete(s.state.Completions, key)
		return ErrDurableObjectResultAmbiguous
	}
	if err := s.persistState(); err != nil {
		delete(s.state.Completions, key)
		return ErrDurableObjectResultAmbiguous
	}
	return nil
}

func (s *localFilesystemStore) completionRecord(
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operationID OperationID,
) (localCompletionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Completions[localCompletionScopeKey(policyDomainID, taskID, operationID)]
	return record, ok
}

func (s *localFilesystemStore) completeOperation(record localCompletionRecord, resultDigest Digest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := record.scopeKey()
	current, ok := s.state.Completions[key]
	if !ok || current.intentDigest() != record.intentDigest() {
		return &Error{Code: ErrorIntegrityConflict}
	}
	evidence := current.completionEvidence(resultDigest)
	if current.State == localCompletionCompleted {
		if current.ResultDigest != resultDigest || current.CompletionEvidence != evidence {
			return &Error{Code: ErrorIntegrityConflict}
		}
		return nil
	}
	current.State = localCompletionCompleted
	current.ResultDigest = resultDigest
	current.CompletionEvidence = evidence
	s.state.Completions[key] = current
	if err := s.inject(LocalFaultAfterCompletionMutation, LocalFilesystemFaultEvent{
		OperationID: record.OperationID, SubjectID: string(record.Kind), Ordinal: 2,
	}); err != nil {
		s.state.Completions[key] = record
		return ErrDurableObjectResultAmbiguous
	}
	if err := s.persistState(); err != nil {
		s.state.Completions[key] = record
		return ErrDurableObjectResultAmbiguous
	}
	return nil
}

func localCompletionError(err error) error {
	if lifecycleErr, ok := err.(*Error); ok {
		return lifecycleErr
	}
	return &Error{Code: ErrorReconciliationRequired}
}

func runLocalCompoundOperation[Result any](
	ctx context.Context,
	lifecycle *localFilesystemLifecycle,
	valid bool,
	intent localCompletionRecord,
	registerFailureResidues bool,
	invokeCore func() (Result, error),
) (Result, error) {
	var zero Result
	if !valid {
		return zero, &Error{Code: ErrorInvalidIntent}
	}
	if err := lifecycle.store.ensureCompletionIntent(intent); err != nil {
		return zero, localCompletionError(err)
	}
	result, lifecycleErr := invokeCore()
	if err := lifecycle.finishLocalCompoundOperation(
		ctx, intent, lifecycleErr, registerFailureResidues,
	); err != nil {
		return zero, err
	}
	return result, nil
}

func (l *localFilesystemLifecycle) finishLocalCompoundOperation(
	ctx context.Context,
	intent localCompletionRecord,
	lifecycleErr error,
	registerFailureResidues bool,
) error {
	if lifecycleErr != nil {
		inspection, inspectErr := l.Lifecycle.InspectOperation(ctx, InspectOperationRequest{
			PolicyDomainID: intent.PolicyDomainID, TaskID: intent.TaskID, OperationID: intent.OperationID,
		})
		if inspectErr == nil && inspection.Disposition != OperationTerminal {
			if registerFailureResidues {
				if err := l.createFailureResidueDebts(
					ctx, intent.PolicyDomainID, intent.TaskID, intent.TaskWorkspaceID,
					intent.Generation, intent.Fence, intent.OperationID,
				); err != nil {
					return &Error{Code: ErrorReconciliationRequired}
				}
			}
			return lifecycleErr
		}
		if typed, ok := lifecycleErr.(*Error); ok && typed.ReconciliationRequired() {
			return lifecycleErr
		}
	}
	if err := l.completeLocalOperation(ctx, intent.PolicyDomainID, intent.TaskID, intent.OperationID); err != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return lifecycleErr
}

func (l *localFilesystemLifecycle) InspectOperation(
	ctx context.Context,
	request InspectOperationRequest,
) (OperationInspection, error) {
	inspection, err := l.Lifecycle.InspectOperation(ctx, request)
	if err != nil {
		return OperationInspection{}, err
	}
	record, ok := l.store.completionRecord(request.PolicyDomainID, request.TaskID, request.OperationID)
	if !ok {
		_, _, requiresCompletion, completionErr := localInspectionCompletionResult(inspection)
		if completionErr != nil {
			return OperationInspection{}, completionErr
		}
		if inspection.Disposition == OperationTerminal && requiresCompletion {
			return localPendingOperationInspection(inspection), nil
		}
		return inspection, nil
	}
	if err := validateLocalCompletionInspection(record, inspection); err != nil {
		return OperationInspection{}, err
	}
	if inspection.Disposition == OperationTerminal && record.State != localCompletionCompleted {
		return localPendingOperationInspection(inspection), nil
	}
	if record.State == localCompletionCompleted {
		kind, resultDigest, present, err := localInspectionCompletionResult(inspection)
		if err != nil || !present || kind != record.Kind || record.ResultDigest != resultDigest ||
			record.CompletionEvidence != record.completionEvidence(resultDigest) {
			return OperationInspection{}, &Error{Code: ErrorIntegrityConflict}
		}
	}
	return inspection, nil
}

func (l *localFilesystemLifecycle) ReconcileOperation(
	ctx context.Context,
	request ReconcileOperationRequest,
) (OperationInspection, error) {
	inspection, err := l.Lifecycle.ReconcileOperation(ctx, request)
	if err != nil && inspection.Disposition != OperationTerminal {
		return inspection, err
	}
	if inspection.Disposition != OperationTerminal {
		return inspection, err
	}
	if completionErr := l.completeLocalInspection(
		ctx, request.PolicyDomainID, request.TaskID, inspection,
	); completionErr != nil {
		return localPendingOperationInspection(inspection), completionErr
	}
	return l.InspectOperation(ctx, InspectOperationRequest(request))
}

func (l *localFilesystemLifecycle) completeLocalOperation(
	ctx context.Context,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	operationID OperationID,
) error {
	inspection, err := l.Lifecycle.InspectOperation(ctx, InspectOperationRequest{
		PolicyDomainID: policyDomainID, TaskID: taskID, OperationID: operationID,
	})
	if err != nil {
		return err
	}
	if inspection.Disposition != OperationTerminal {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return l.completeLocalInspection(ctx, policyDomainID, taskID, inspection)
}

func (l *localFilesystemLifecycle) completeLocalInspection(
	ctx context.Context,
	policyDomainID PolicyDomainID,
	taskID TaskID,
	inspection OperationInspection,
) error {
	record, ok := l.store.completionRecord(policyDomainID, taskID, inspection.Operation.ID)
	if !ok {
		_, _, requiresCompletion, completionErr := localInspectionCompletionResult(inspection)
		if completionErr != nil {
			return completionErr
		}
		if requiresCompletion {
			return &Error{Code: ErrorReconciliationRequired}
		}
		return nil
	}
	if err := validateLocalCompletionInspection(record, inspection); err != nil {
		return err
	}
	kind, resultDigest, present, err := localInspectionCompletionResult(inspection)
	if err != nil || !present || kind != record.Kind {
		if err != nil {
			return err
		}
		return &Error{Code: ErrorIntegrityConflict}
	}
	if record.State == localCompletionCompleted {
		return l.store.completeOperation(record, resultDigest)
	}
	if inspection.Error != nil {
		if err := l.createFailureResidueDebts(ctx, record.PolicyDomainID, record.TaskID,
			record.TaskWorkspaceID, record.Generation, record.Fence, record.OperationID); err != nil {
			return err
		}
	} else {
		switch record.Phase {
		case localCompletionCheckpointActivation:
			if inspection.CommitRuntimeView == nil {
				return &Error{Code: ErrorIntegrityConflict}
			}
			if err := l.store.activateCheckpoint(record.OperationID, inspection.CommitRuntimeView.CheckpointEvidence); err != nil {
				_ = l.createFailureResidueDebts(ctx, record.PolicyDomainID, record.TaskID,
					record.TaskWorkspaceID, record.Generation, record.Fence, record.OperationID)
				return err
			}
		case localCompletionMaterializationBinding:
			var authority localMaterializationAuthority
			switch record.Kind {
			case localCompletionMaterialize:
				if inspection.Materialize == nil {
					return &Error{Code: ErrorIntegrityConflict}
				}
				result := inspection.Materialize
				authority = localMaterializationAuthority{
					OperationID: record.OperationID, MaterializationID: result.MaterializationID,
					PolicyDomainID: record.PolicyDomainID, TaskID: record.TaskID,
					TaskWorkspaceID: result.TaskWorkspaceID, Generation: result.Generation,
					Fence: result.Fence, PhysicalRequired: result.CheckpointID != "",
				}
			case localCompletionRestore:
				if inspection.RestoreTaskWorkspace == nil {
					return &Error{Code: ErrorIntegrityConflict}
				}
				result := inspection.RestoreTaskWorkspace
				authority = localMaterializationAuthority{
					OperationID: record.OperationID, MaterializationID: result.MaterializationID,
					PolicyDomainID: record.PolicyDomainID, TaskID: record.TaskID,
					TaskWorkspaceID: result.TaskWorkspaceID, Generation: result.Generation,
					Fence: result.Fence, PhysicalRequired: result.CheckpointID != "",
				}
			case localCompletionReconstruct:
				if inspection.ReconstructTaskWorkspace == nil {
					return &Error{Code: ErrorIntegrityConflict}
				}
				result := inspection.ReconstructTaskWorkspace
				authority = localMaterializationAuthority{
					OperationID: record.OperationID, MaterializationID: result.MaterializationID,
					PolicyDomainID: record.PolicyDomainID, TaskID: record.TaskID,
					TaskWorkspaceID: result.TaskWorkspaceID, Generation: result.Generation,
					Fence: result.Fence,
				}
			default:
				return &Error{Code: ErrorIntegrityConflict}
			}
			if err := l.store.bindMaterialization(authority); err != nil {
				return err
			}
		case localCompletionMaterializationExpiry:
			if record.Kind != localCompletionExpire || inspection.ExpireMaterialization == nil {
				return &Error{Code: ErrorIntegrityConflict}
			}
			request := ExpireMaterializationRequest{
				PolicyDomainID: record.PolicyDomainID, TaskID: record.TaskID,
				TaskWorkspaceID: record.TaskWorkspaceID, MaterializationID: record.MaterializationID,
				RevisionID: record.RevisionID, CheckpointID: record.CheckpointID,
				Generation: record.Generation, Fence: record.Fence, ExpiryPolicyID: record.ExpiryPolicyID,
				Operation: Operation{ID: record.OperationID, RequestDigest: record.RequestDigest},
			}
			residue, cleanupErr := l.store.expireMaterialization(request)
			if cleanupErr != nil {
				if residue == nil {
					return &Error{Code: ErrorReconciliationRequired}
				}
				if err := l.createResidueDebt(
					ctx, record.PolicyDomainID, record.TaskID, record.TaskWorkspaceID,
					record.Generation, record.Fence, record.OperationID, *residue,
					CleanupTaskWorkspaceMaterialization,
				); err != nil {
					return err
				}
			}
		default:
			return &Error{Code: ErrorIntegrityConflict}
		}
	}
	if err := l.store.completeOperation(record, resultDigest); err != nil {
		return err
	}
	if err := l.store.inject(LocalFaultAfterCompletionPersistence, LocalFilesystemFaultEvent{
		OperationID: record.OperationID, SubjectID: string(record.Kind),
	}); err != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return nil
}

func validateLocalCompletionInspection(record localCompletionRecord, inspection OperationInspection) error {
	if inspection.Operation.ID != record.OperationID ||
		inspection.Operation.RequestDigest != record.RequestDigest ||
		inspection.ExpectedRevisionID != record.ExpectedRevisionID ||
		inspection.Generation != record.Generation || inspection.Fence != record.Fence ||
		inspection.AuthorityBindingsDigest != record.AuthorityBindingsDigest {
		return &Error{Code: ErrorIntegrityConflict}
	}
	return nil
}

func localInspectionCompletionResult(
	inspection OperationInspection,
) (localCompletionOperationKind, Digest, bool, error) {
	var kind localCompletionOperationKind
	var resultDigest Digest
	resultCount := 0
	if inspection.CommitRuntimeView != nil {
		kind = localCompletionCommitRuntimeView
		resultCount++
		resultDigest = canonicalDigest(struct {
			Result CommitRuntimeViewResult
			Error  *Error
		}{cloneCommitResult(*inspection.CommitRuntimeView), cloneLifecycleError(inspection.Error)})
	}
	if inspection.Materialize != nil {
		kind = localCompletionMaterialize
		resultCount++
		resultDigest = canonicalDigest(struct {
			Result MaterializeResult
			Error  *Error
		}{cloneMaterializeResult(*inspection.Materialize), cloneLifecycleError(inspection.Error)})
	}
	if inspection.RestoreTaskWorkspace != nil {
		kind = localCompletionRestore
		resultCount++
		resultDigest = canonicalDigest(struct {
			Result RestoreTaskWorkspaceResult
			Error  *Error
		}{cloneRestoreTaskWorkspaceResult(*inspection.RestoreTaskWorkspace), cloneLifecycleError(inspection.Error)})
	}
	if inspection.ReconstructTaskWorkspace != nil {
		kind = localCompletionReconstruct
		resultCount++
		resultDigest = canonicalDigest(struct {
			Result ReconstructTaskWorkspaceResult
			Error  *Error
		}{cloneReconstructResult(*inspection.ReconstructTaskWorkspace), cloneLifecycleError(inspection.Error)})
	}
	if inspection.ExpireMaterialization != nil {
		kind = localCompletionExpire
		resultCount++
		resultDigest = canonicalDigest(struct {
			Result ExpireMaterializationResult
			Error  *Error
		}{*inspection.ExpireMaterialization, cloneLifecycleError(inspection.Error)})
	}
	if resultCount == 0 {
		return "", "", false, nil
	}
	if resultCount != 1 || resultDigest == "" {
		return "", "", false, &Error{Code: ErrorIntegrityConflict}
	}
	return kind, resultDigest, true, nil
}

func localPendingOperationInspection(inspection OperationInspection) OperationInspection {
	inspection.Disposition = OperationReconciliationRequired
	inspection.IntentState = OperationIntentActing
	inspection.CommitRuntimeView = nil
	inspection.Materialize = nil
	inspection.RestoreTaskWorkspace = nil
	inspection.ReconstructTaskWorkspace = nil
	inspection.ExpireMaterialization = nil
	inspection.Error = &Error{Code: ErrorReconciliationRequired}
	return inspection
}
