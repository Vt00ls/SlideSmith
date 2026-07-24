package taskworkspace

import "context"

type FaultPoint string

const (
	FaultBeforeBaseMaterialization           FaultPoint = "before_base_materialization"
	FaultAfterBaseMaterialization            FaultPoint = "after_base_materialization"
	FaultBeforeRuntimeViewCreation           FaultPoint = "before_runtime_view_creation"
	FaultAfterRuntimeViewCreation            FaultPoint = "after_runtime_view_creation"
	FaultBeforeDeclaredManifestVerification  FaultPoint = "before_declared_manifest_verification"
	FaultAfterDeclaredManifestVerification   FaultPoint = "after_declared_manifest_verification"
	FaultBeforeContentPrepare                FaultPoint = "before_content_prepare"
	FaultAfterContentPrepare                 FaultPoint = "after_content_prepare"
	FaultBeforeDurabilityReceiptVerification FaultPoint = "before_durability_receipt_verification"
	FaultAfterDurabilityReceiptVerification  FaultPoint = "after_durability_receipt_verification"
	FaultBeforeDiscardEvidencePersistence    FaultPoint = "before_discard_evidence_persistence"
	FaultAfterDiscardEvidencePersistence     FaultPoint = "after_discard_evidence_persistence"
	FaultBeforeAuthoritativeTransaction      FaultPoint = "before_authoritative_transaction"
	FaultAfterAuthoritativeTransaction       FaultPoint = "after_authoritative_transaction"
	FaultBeforeIntentPersistence             FaultPoint = "before_intent_persistence"
	FaultAfterIntentPersistence              FaultPoint = "after_intent_persistence"
	FaultBeforeResponse                      FaultPoint = "before_response"
	FaultAfterResponse                       FaultPoint = "after_response"
)

type FaultEvent struct {
	Point       FaultPoint
	OperationID OperationID
	SubjectID   string
	Ordinal     int
}

type ResponseDeliveryEvent struct {
	OperationID OperationID
}

type OperationDisposition string

type OperationIntentState string

const (
	OperationPending                OperationDisposition = "pending"
	OperationTerminal               OperationDisposition = "terminal"
	OperationReconciliationRequired OperationDisposition = "reconciliation_required"
)

const (
	OperationIntentPersisted OperationIntentState = "persisted"
	OperationIntentActing    OperationIntentState = "acting"
	OperationIntentVerified  OperationIntentState = "verified"
	OperationIntentActivated OperationIntentState = "activated"
	OperationIntentRejected  OperationIntentState = "rejected"
)

type InspectOperationRequest struct {
	PolicyDomainID PolicyDomainID
	TaskID         TaskID
	OperationID    OperationID
}

type OperationInspection struct {
	Operation               Operation
	Disposition             OperationDisposition
	IntentState             OperationIntentState
	ExpectedRevisionID      RevisionID
	Generation              Generation
	Fence                   Fence
	AuthorityBindingsDigest Digest
	ConfirmTaskWorkspace    *ConfirmTaskWorkspaceResult
	Materialize             *MaterializeResult
	OpenRuntimeView         *OpenRuntimeViewResult
	CommitRuntimeView       *CommitRuntimeViewResult
	DiscardRuntimeView      *DiscardRuntimeViewResult
	FenceRuntimeView        *FenceRuntimeViewResult
	Error                   *Error
}

type ReconcileOperationRequest struct {
	PolicyDomainID PolicyDomainID
	TaskID         TaskID
	OperationID    OperationID
}

type operationJournalState string

const (
	operationJournalIntentPersisted        operationJournalState = "intent_persisted"
	operationJournalReconciliationRequired operationJournalState = "reconciliation_required"
	operationJournalTerminal               operationJournalState = "terminal"
)

type operationIntentMetadata struct {
	expectedRevisionID      RevisionID
	generation              Generation
	fence                   Fence
	authorityBindingsDigest Digest
}

type operationJournalPayload interface {
	reconcile(context.Context, *inMemory) error
	projectResult(*OperationInspection) error
}

type operationResultAccess[Result any] interface {
	operationResult() (Result, bool)
	storeOperationResult(Result)
}

type operationJournalSpec[Request, Result any] struct {
	cloneRequest func(Request) Request
	cloneResult  func(Result) Result
	intent       func(Request) operationIntentMetadata
	execute      func(context.Context, *inMemory, Request) (Result, error)
	project      func(*OperationInspection, Result)
}

type typedOperationJournal[Request, Result any] struct {
	spec      operationJournalSpec[Request, Result]
	request   Request
	result    Result
	resultSet bool
}

func (j *typedOperationJournal[Request, Result]) reconcile(ctx context.Context, m *inMemory) error {
	_, err := j.spec.execute(ctx, m, j.spec.cloneRequest(j.request))
	return err
}

func (j *typedOperationJournal[Request, Result]) projectResult(inspection *OperationInspection) error {
	if !j.resultSet {
		return &Error{Code: ErrorIntegrityConflict}
	}
	j.spec.project(inspection, j.spec.cloneResult(j.result))
	return nil
}

func (j *typedOperationJournal[Request, Result]) operationResult() (Result, bool) {
	return j.spec.cloneResult(j.result), j.resultSet
}

func (j *typedOperationJournal[Request, Result]) storeOperationResult(result Result) {
	j.result = j.spec.cloneResult(result)
	j.resultSet = true
}

func identityClone[Value any](value Value) Value {
	return value
}

func cloneCommitRequest(request CommitRuntimeViewRequest) CommitRuntimeViewRequest {
	request.DeclaredStateManifest = cloneDeclaredStateManifest(request.DeclaredStateManifest)
	return request
}

func cloneCommitResult(result CommitRuntimeViewResult) CommitRuntimeViewResult {
	result.CheckpointEvidence = cloneCheckpointEvidence(result.CheckpointEvidence)
	return result
}

func cloneMaterializeResult(result MaterializeResult) MaterializeResult {
	result.CheckpointEvidence = cloneCheckpointEvidence(result.CheckpointEvidence)
	return result
}

func confirmJournalSpec() operationJournalSpec[ConfirmTaskWorkspaceRequest, ConfirmTaskWorkspaceResult] {
	return operationJournalSpec[ConfirmTaskWorkspaceRequest, ConfirmTaskWorkspaceResult]{
		cloneRequest: identityClone[ConfirmTaskWorkspaceRequest],
		cloneResult:  identityClone[ConfirmTaskWorkspaceResult],
		intent:       confirmIntentMetadata,
		execute: func(ctx context.Context, m *inMemory, request ConfirmTaskWorkspaceRequest) (ConfirmTaskWorkspaceResult, error) {
			return m.ConfirmTaskWorkspace(ctx, request)
		},
		project: func(inspection *OperationInspection, result ConfirmTaskWorkspaceResult) {
			inspection.ConfirmTaskWorkspace = &result
		},
	}
}

func materializeJournalSpec() operationJournalSpec[MaterializeRequest, MaterializeResult] {
	return operationJournalSpec[MaterializeRequest, MaterializeResult]{
		cloneRequest: identityClone[MaterializeRequest],
		cloneResult:  cloneMaterializeResult,
		intent:       materializeIntentMetadata,
		execute: func(ctx context.Context, m *inMemory, request MaterializeRequest) (MaterializeResult, error) {
			return m.Materialize(ctx, request)
		},
		project: func(inspection *OperationInspection, result MaterializeResult) {
			inspection.Materialize = &result
		},
	}
}

func openRuntimeViewJournalSpec() operationJournalSpec[OpenRuntimeViewRequest, OpenRuntimeViewResult] {
	return operationJournalSpec[OpenRuntimeViewRequest, OpenRuntimeViewResult]{
		cloneRequest: identityClone[OpenRuntimeViewRequest],
		cloneResult:  identityClone[OpenRuntimeViewResult],
		intent:       openRuntimeViewIntentMetadata,
		execute: func(ctx context.Context, m *inMemory, request OpenRuntimeViewRequest) (OpenRuntimeViewResult, error) {
			return m.OpenRuntimeView(ctx, request)
		},
		project: func(inspection *OperationInspection, result OpenRuntimeViewResult) {
			inspection.OpenRuntimeView = &result
		},
	}
}

func commitRuntimeViewJournalSpec() operationJournalSpec[CommitRuntimeViewRequest, CommitRuntimeViewResult] {
	return operationJournalSpec[CommitRuntimeViewRequest, CommitRuntimeViewResult]{
		cloneRequest: cloneCommitRequest,
		cloneResult:  cloneCommitResult,
		intent:       commitRuntimeViewIntentMetadata,
		execute: func(ctx context.Context, m *inMemory, request CommitRuntimeViewRequest) (CommitRuntimeViewResult, error) {
			return m.CommitRuntimeView(ctx, request)
		},
		project: func(inspection *OperationInspection, result CommitRuntimeViewResult) {
			inspection.CommitRuntimeView = &result
		},
	}
}

func discardRuntimeViewJournalSpec() operationJournalSpec[DiscardRuntimeViewRequest, DiscardRuntimeViewResult] {
	return operationJournalSpec[DiscardRuntimeViewRequest, DiscardRuntimeViewResult]{
		cloneRequest: identityClone[DiscardRuntimeViewRequest],
		cloneResult:  identityClone[DiscardRuntimeViewResult],
		intent:       discardRuntimeViewIntentMetadata,
		execute: func(ctx context.Context, m *inMemory, request DiscardRuntimeViewRequest) (DiscardRuntimeViewResult, error) {
			return m.DiscardRuntimeView(ctx, request)
		},
		project: func(inspection *OperationInspection, result DiscardRuntimeViewResult) {
			inspection.DiscardRuntimeView = &result
		},
	}
}

func fenceRuntimeViewJournalSpec() operationJournalSpec[FenceRuntimeViewRequest, FenceRuntimeViewResult] {
	return operationJournalSpec[FenceRuntimeViewRequest, FenceRuntimeViewResult]{
		cloneRequest: identityClone[FenceRuntimeViewRequest],
		cloneResult:  identityClone[FenceRuntimeViewResult],
		intent:       fenceRuntimeViewIntentMetadata,
		execute: func(ctx context.Context, m *inMemory, request FenceRuntimeViewRequest) (FenceRuntimeViewResult, error) {
			return m.FenceRuntimeView(ctx, request)
		},
		project: func(inspection *OperationInspection, result FenceRuntimeViewResult) {
			inspection.FenceRuntimeView = &result
		},
	}
}

func (m *inMemory) InspectOperation(
	_ context.Context,
	request InspectOperationRequest,
) (OperationInspection, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.OperationID == "" {
		return OperationInspection{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.operations[operationScope{
		policyDomainID: request.PolicyDomainID,
		taskID:         request.TaskID,
		operationID:    request.OperationID,
	}]
	if !ok {
		return OperationInspection{}, &Error{Code: ErrorInvalidIntent}
	}
	inspection := OperationInspection{
		Operation:               Operation{ID: request.OperationID, RequestDigest: record.requestDigest},
		Disposition:             operationDisposition(record.state),
		IntentState:             record.intentState,
		ExpectedRevisionID:      record.expectedRevisionID,
		Generation:              record.generation,
		Fence:                   record.fence,
		AuthorityBindingsDigest: record.authorityBindingsDigest,
		Error:                   cloneLifecycleError(record.err),
	}
	if record.state != operationJournalTerminal {
		return inspection, nil
	}
	if record.payload == nil {
		return OperationInspection{}, &Error{Code: ErrorIntegrityConflict}
	}
	if err := record.payload.projectResult(&inspection); err != nil {
		return OperationInspection{}, err
	}
	return inspection, nil
}

func (m *inMemory) ReconcileOperation(
	ctx context.Context,
	request ReconcileOperationRequest,
) (OperationInspection, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.OperationID == "" {
		return OperationInspection{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	record, ok := m.operations[operationScope{
		policyDomainID: request.PolicyDomainID,
		taskID:         request.TaskID,
		operationID:    request.OperationID,
	}]
	if !ok {
		m.mu.Unlock()
		return OperationInspection{}, &Error{Code: ErrorInvalidIntent}
	}
	payload := record.payload
	terminal := record.state == operationJournalTerminal
	m.mu.Unlock()
	if !terminal {
		if payload == nil {
			return OperationInspection{}, &Error{Code: ErrorReconciliationRequired}
		}
		reconcileErr := payload.reconcile(ctx, m)
		if reconcileErr != nil {
			inspection, inspectErr := m.InspectOperation(ctx, InspectOperationRequest(request))
			if inspectErr == nil {
				if inspection.Disposition == OperationTerminal {
					return inspection, nil
				}
				return inspection, reconcileErr
			}
			return OperationInspection{}, reconcileErr
		}
	}
	return m.InspectOperation(ctx, InspectOperationRequest(request))
}

func operationDisposition(state operationJournalState) OperationDisposition {
	switch state {
	case operationJournalIntentPersisted:
		return OperationPending
	case operationJournalReconciliationRequired:
		return OperationReconciliationRequired
	case operationJournalTerminal:
		return OperationTerminal
	default:
		return OperationReconciliationRequired
	}
}

func persistOperationIntent[Request, Result any](
	records map[operationScope]operationRecord,
	scope operationScope,
	operation Operation,
	request Request,
	spec operationJournalSpec[Request, Result],
) bool {
	if _, exists := records[scope]; exists {
		return false
	}
	metadata := spec.intent(request)
	records[scope] = operationRecord{
		requestDigest: operation.RequestDigest,
		payload: &typedOperationJournal[Request, Result]{
			spec:    spec,
			request: spec.cloneRequest(request),
		},
		state:                   operationJournalIntentPersisted,
		intentState:             OperationIntentPersisted,
		expectedRevisionID:      metadata.expectedRevisionID,
		generation:              metadata.generation,
		fence:                   metadata.fence,
		authorityBindingsDigest: metadata.authorityBindingsDigest,
		plannedIDs:              make(map[string]string),
	}
	return true
}

func ensureOperationIntent[Request, Result any](
	m *inMemory,
	scope operationScope,
	operation Operation,
	request Request,
	spec operationJournalSpec[Request, Result],
	afterPersistence func(),
) (bool, error) {
	if _, exists := m.operations[scope]; exists {
		return false, nil
	}
	if err := m.injectFault(FaultBeforeIntentPersistence, operation.ID); err != nil {
		return false, err
	}
	created := persistOperationIntent(m.operations, scope, operation, request, spec)
	if !created {
		return false, nil
	}
	if afterPersistence != nil {
		afterPersistence()
	}
	if err := m.injectFault(FaultAfterIntentPersistence, operation.ID); err != nil {
		return true, err
	}
	return true, nil
}

func markOperationReconciliationRequired(
	records map[operationScope]operationRecord,
	scope operationScope,
) {
	record := records[scope]
	record.state = operationJournalReconciliationRequired
	record.intentState = OperationIntentActing
	records[scope] = record
}

func markOperationVerified(
	records map[operationScope]operationRecord,
	scope operationScope,
) {
	record := records[scope]
	record.state = operationJournalReconciliationRequired
	record.intentState = OperationIntentVerified
	records[scope] = record
}

func (m *inMemory) operationOpaqueID(
	scope operationScope,
	key string,
	kind string,
) string {
	record := m.operations[scope]
	if identity := record.plannedIDs[key]; identity != "" {
		return identity
	}
	identity := m.nextOpaqueID(kind)
	if record.plannedIDs == nil {
		record.plannedIDs = make(map[string]string)
	}
	record.plannedIDs[key] = identity
	m.operations[scope] = record
	return identity
}

func confirmIntentMetadata(request ConfirmTaskWorkspaceRequest) operationIntentMetadata {
	return operationIntentMetadata{
		generation: 1,
		fence:      1,
		authorityBindingsDigest: canonicalDigest(struct {
			PolicyDomainID PolicyDomainID
			TaskID         TaskID
		}{
			PolicyDomainID: request.PolicyDomainID,
			TaskID:         request.TaskID,
		}),
	}
}

func commitRuntimeViewIntentMetadata(request CommitRuntimeViewRequest) operationIntentMetadata {
	return operationIntentMetadata{
		expectedRevisionID: request.ExpectedCurrentRevision,
		generation:         request.Generation,
		fence:              request.Fence,
		authorityBindingsDigest: canonicalDigest(struct {
			PolicyDomainID              PolicyDomainID
			TaskID                      TaskID
			TaskWorkspaceID             TaskWorkspaceID
			RuntimeViewID               RuntimeViewID
			RuntimeOperationID          OperationID
			SandboxLeaseAuthorityDigest Digest
			ValidationEvidenceID        EvidenceID
			ValidationEvidenceDigest    Digest
		}{
			PolicyDomainID:              request.PolicyDomainID,
			TaskID:                      request.TaskID,
			TaskWorkspaceID:             request.TaskWorkspaceID,
			RuntimeViewID:               request.RuntimeViewID,
			RuntimeOperationID:          request.RuntimeOperationID,
			SandboxLeaseAuthorityDigest: request.SandboxLeaseAuthority.Digest,
			ValidationEvidenceID:        request.ValidationEvidence.ID,
			ValidationEvidenceDigest:    request.ValidationEvidence.Digest,
		}),
	}
}

func materializeIntentMetadata(request MaterializeRequest) operationIntentMetadata {
	return operationIntentMetadata{
		expectedRevisionID: request.RevisionID,
		generation:         request.Generation,
		fence:              request.Fence,
		authorityBindingsDigest: canonicalDigest(struct {
			PolicyDomainID  PolicyDomainID
			TaskID          TaskID
			TaskWorkspaceID TaskWorkspaceID
			RevisionID      RevisionID
			CheckpointID    CheckpointID
		}{
			PolicyDomainID:  request.PolicyDomainID,
			TaskID:          request.TaskID,
			TaskWorkspaceID: request.TaskWorkspaceID,
			RevisionID:      request.RevisionID,
			CheckpointID:    request.CheckpointID,
		}),
	}
}

func openRuntimeViewIntentMetadata(request OpenRuntimeViewRequest) operationIntentMetadata {
	return operationIntentMetadata{
		expectedRevisionID: request.BaseRevisionID,
		generation:         request.Generation,
		fence:              request.Fence,
		authorityBindingsDigest: canonicalDigest(struct {
			PolicyDomainID              PolicyDomainID
			TaskID                      TaskID
			TaskWorkspaceID             TaskWorkspaceID
			MaterializationID           MaterializationID
			PhaseRunID                  PhaseRunID
			RuntimeRunID                RuntimeRunID
			RuntimeOperationID          OperationID
			SandboxLeaseAuthorityDigest Digest
		}{
			PolicyDomainID:              request.PolicyDomainID,
			TaskID:                      request.TaskID,
			TaskWorkspaceID:             request.TaskWorkspaceID,
			MaterializationID:           request.MaterializationID,
			PhaseRunID:                  request.PhaseRunID,
			RuntimeRunID:                request.RuntimeRunID,
			RuntimeOperationID:          request.RuntimeOperationID,
			SandboxLeaseAuthorityDigest: request.SandboxLeaseAuthority.Digest,
		}),
	}
}

func discardRuntimeViewIntentMetadata(request DiscardRuntimeViewRequest) operationIntentMetadata {
	return operationIntentMetadata{
		expectedRevisionID: request.ExpectedCurrentRevision,
		generation:         request.Generation,
		fence:              request.Fence,
		authorityBindingsDigest: canonicalDigest(struct {
			PolicyDomainID              PolicyDomainID
			TaskID                      TaskID
			TaskWorkspaceID             TaskWorkspaceID
			RuntimeViewID               RuntimeViewID
			RuntimeOperationID          OperationID
			SandboxLeaseAuthorityDigest Digest
			Reason                      RuntimeViewDiscardReason
		}{
			PolicyDomainID:              request.PolicyDomainID,
			TaskID:                      request.TaskID,
			TaskWorkspaceID:             request.TaskWorkspaceID,
			RuntimeViewID:               request.RuntimeViewID,
			RuntimeOperationID:          request.RuntimeOperationID,
			SandboxLeaseAuthorityDigest: request.SandboxLeaseAuthority.Digest,
			Reason:                      request.Reason,
		}),
	}
}

func fenceRuntimeViewIntentMetadata(request FenceRuntimeViewRequest) operationIntentMetadata {
	return operationIntentMetadata{
		expectedRevisionID: request.ExpectedCurrentRevision,
		generation:         request.Generation,
		fence:              request.Fence,
		authorityBindingsDigest: canonicalDigest(struct {
			PolicyDomainID              PolicyDomainID
			TaskID                      TaskID
			TaskWorkspaceID             TaskWorkspaceID
			RuntimeViewID               RuntimeViewID
			RuntimeOperationID          OperationID
			SandboxLeaseAuthorityDigest Digest
			Reason                      RuntimeViewFenceReason
		}{
			PolicyDomainID:              request.PolicyDomainID,
			TaskID:                      request.TaskID,
			TaskWorkspaceID:             request.TaskWorkspaceID,
			RuntimeViewID:               request.RuntimeViewID,
			RuntimeOperationID:          request.RuntimeOperationID,
			SandboxLeaseAuthorityDigest: request.SandboxLeaseAuthority.Digest,
			Reason:                      request.Reason,
		}),
	}
}

func (m *inMemory) injectFault(point FaultPoint, operationID OperationID) error {
	return m.injectFaultEvent(FaultEvent{Point: point, OperationID: operationID})
}

func (m *inMemory) injectFaultEvent(event FaultEvent) error {
	if m.faultHook == nil {
		return nil
	}
	if err := m.faultHook(event); err != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return nil
}

func deliverOperationResponse[Result any](
	m *inMemory,
	operationID OperationID,
	result Result,
) (Result, error) {
	var zero Result
	if err := m.injectFault(FaultBeforeResponse, operationID); err != nil {
		return zero, err
	}
	if m.responseDelivery != nil {
		m.responseDelivery(ResponseDeliveryEvent{OperationID: operationID})
	}
	if err := m.injectFault(FaultAfterResponse, operationID); err != nil {
		return zero, err
	}
	return result, nil
}
