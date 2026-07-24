package taskworkspace

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type InMemoryConfig struct {
	ValidationAuthorityID          ValidationAuthorityID
	DurabilityAuthorityID          DurabilityAuthorityID
	DurableObject                  DurableObjectPort
	Persistence                    *InMemoryPersistence
	SandboxLeaseAuthorityID        SandboxLeaseAuthorityID
	CurrentSandboxLeaseAuthorities []SandboxLeaseAuthority
	CurrentSandboxLeaseAuthority   func(SandboxLeaseID) (SandboxLeaseAuthority, bool)
	Now                            func() Instant
	BeforeRuntimeViewTerminal      func(RuntimeViewTerminalAttempt)
	FaultHook                      func(FaultEvent) error
	ResponseDelivery               func(ResponseDeliveryEvent)
	ExpiryPolicy                   ExpiryPolicy
	RecoveryAuthorityID            RecoveryAuthorityID
	CurrentRecoveryIntents         []AuthorizedRecoveryIntent
	CurrentRecoveryIntent          func(RecoveryIntentID) (AuthorizedRecoveryIntent, bool)
	ReconstructionInput            ReconstructionInputPort
	ExpiryProtection               ExpiryProtectionPort
}

// InMemoryPersistence is an opaque persistence handle for deterministic
// restart scenarios. Its journal and lifecycle records remain module-private.
type InMemoryPersistence struct {
	mu                 sync.Mutex
	nextID             uint64
	workspaces         map[TaskID]workspaceBinding
	materializations   map[MaterializationID]materializationBinding
	views              map[RuntimeViewID]runtimeViewBinding
	revisions          map[RevisionID]revisionRecord
	checkpoints        map[CheckpointID]checkpointRecord
	operations         map[operationScope]operationRecord
	contentReferences  map[ContentReferenceID]ContentReference
	contentFacts       map[ContentID]durableContentFact
	durabilityReceipts map[DurabilityReceiptID]DurabilityReceipt
	currentReceipts    map[receiptAuthorityScope]DurabilityReceipt
	receiptGenerations map[receiptAuthorityScope]map[DurabilityGenerationID]DurabilityReceiptID
}

type inMemory struct {
	*InMemoryPersistence
	validationAuthorityID   ValidationAuthorityID
	durabilityAuthorityID   DurabilityAuthorityID
	durableObject           DurableObjectPort
	sandboxLeaseAuthorityID SandboxLeaseAuthorityID
	now                     func() Instant
	beforeTerminal          func(RuntimeViewTerminalAttempt)
	faultHook               func(FaultEvent) error
	responseDelivery        func(ResponseDeliveryEvent)
	expiryPolicy            ExpiryPolicy
	recoveryAuthorityID     RecoveryAuthorityID
	recoveryIntents         map[RecoveryIntentID]AuthorizedRecoveryIntent
	currentRecoveryIntent   func(RecoveryIntentID) (AuthorizedRecoveryIntent, bool)
	reconstructionInput     ReconstructionInputPort
	expiryProtection        ExpiryProtectionPort
	sandboxLeaseAuthorities map[SandboxLeaseID]SandboxLeaseAuthority
	currentLeaseAuthority   func(SandboxLeaseID) (SandboxLeaseAuthority, bool)
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
	policyDomainID         PolicyDomainID
	taskID                 TaskID
	taskWorkspaceID        TaskWorkspaceID
	revisionID             RevisionID
	checkpointID           CheckpointID
	generation             Generation
	fence                  Fence
	expiryPolicyID         ExpiryPolicyID
	expiresAt              Instant
	readOnlyInputs         []ReadOnlyInputMaterialization
	artifactVersionID      ArtifactVersionID
	artifactManifestDigest Digest
	reconstructionEvidence ArtifactVersionReconstructionEvidence
	publicationAuthorityID PublicationAuthorityID
}

type runtimeViewBinding struct {
	policyDomainID         PolicyDomainID
	taskID                 TaskID
	taskWorkspaceID        TaskWorkspaceID
	materializationID      MaterializationID
	baseRevisionID         RevisionID
	phaseRunID             PhaseRunID
	runtimeRunID           RuntimeRunID
	runtimeOperationID     OperationID
	sandboxLeaseAuthority  SandboxLeaseAuthority
	effectClass            RuntimeViewEffectClass
	expiresAt              Instant
	expiryPolicyID         ExpiryPolicyID
	generation             Generation
	fence                  Fence
	terminalDecision       runtimeViewTerminalDecision
	expired                bool
	readOnlyInputs         []ReadOnlyInputMaterialization
	artifactVersionID      ArtifactVersionID
	artifactManifestDigest Digest
	reconstructionEvidence ArtifactVersionReconstructionEvidence
	publicationAuthorityID PublicationAuthorityID
}

type runtimeViewTerminalDecision string

const (
	runtimeViewNonTerminal runtimeViewTerminalDecision = ""
	runtimeViewCommitted   runtimeViewTerminalDecision = "committed"
	runtimeViewDiscarded   runtimeViewTerminalDecision = "discarded"
	runtimeViewFenced      runtimeViewTerminalDecision = "fenced"
)

type operationScope struct {
	policyDomainID PolicyDomainID
	taskID         TaskID
	operationID    OperationID
}

type operationRecord struct {
	requestDigest            Digest
	payload                  operationJournalPayload
	state                    operationJournalState
	intentState              OperationIntentState
	expectedRevisionID       RevisionID
	generation               Generation
	fence                    Fence
	authorityBindingsDigest  Digest
	plannedIDs               map[string]string
	plannedRuntimeViewExpiry runtimeViewExpiryDecision
	err                      *Error
}

type runtimeViewExpiryDecision struct {
	policyID  ExpiryPolicyID
	expiresAt Instant
}

func NewInMemoryPersistence() *InMemoryPersistence {
	persistence := &InMemoryPersistence{}
	persistence.initialize()
	return persistence
}

func (p *InMemoryPersistence) initialize() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.workspaces == nil {
		p.workspaces = make(map[TaskID]workspaceBinding)
		p.materializations = make(map[MaterializationID]materializationBinding)
		p.views = make(map[RuntimeViewID]runtimeViewBinding)
		p.revisions = make(map[RevisionID]revisionRecord)
		p.checkpoints = make(map[CheckpointID]checkpointRecord)
		p.operations = make(map[operationScope]operationRecord)
		p.contentReferences = make(map[ContentReferenceID]ContentReference)
		p.contentFacts = make(map[ContentID]durableContentFact)
		p.durabilityReceipts = make(map[DurabilityReceiptID]DurabilityReceipt)
		p.currentReceipts = make(map[receiptAuthorityScope]DurabilityReceipt)
		p.receiptGenerations = make(map[receiptAuthorityScope]map[DurabilityGenerationID]DurabilityReceiptID)
	}
}

func NewInMemory(config InMemoryConfig) Lifecycle {
	now := config.Now
	if now == nil {
		now = func() Instant { return Instant(time.Now().UnixNano()) }
	}
	expiryPolicy := config.ExpiryPolicy
	defaultExpiryLifetime := Duration(24 * time.Hour)
	if expiryPolicy.ID == "" {
		expiryPolicy.ID = "default-expiry-policy"
	}
	if expiryPolicy.MaterializationLifetime <= 0 {
		expiryPolicy.MaterializationLifetime = defaultExpiryLifetime
	}
	if expiryPolicy.RuntimeViewLifetime <= 0 {
		expiryPolicy.RuntimeViewLifetime = expiryPolicy.MaterializationLifetime
	}
	persistence := config.Persistence
	if persistence == nil {
		persistence = NewInMemoryPersistence()
	} else {
		persistence.initialize()
	}
	memory := &inMemory{
		InMemoryPersistence:     persistence,
		validationAuthorityID:   config.ValidationAuthorityID,
		durabilityAuthorityID:   config.DurabilityAuthorityID,
		durableObject:           config.DurableObject,
		sandboxLeaseAuthorityID: config.SandboxLeaseAuthorityID,
		now:                     now,
		beforeTerminal:          config.BeforeRuntimeViewTerminal,
		faultHook:               config.FaultHook,
		responseDelivery:        config.ResponseDelivery,
		expiryPolicy:            expiryPolicy,
		recoveryAuthorityID:     config.RecoveryAuthorityID,
		recoveryIntents:         make(map[RecoveryIntentID]AuthorizedRecoveryIntent),
		reconstructionInput:     config.ReconstructionInput,
		expiryProtection:        config.ExpiryProtection,
		sandboxLeaseAuthorities: make(map[SandboxLeaseID]SandboxLeaseAuthority),
	}
	leaseDuplicates := make(map[SandboxLeaseID]bool)
	for _, authority := range config.CurrentSandboxLeaseAuthorities {
		if !sandboxLeaseAuthorityIsCanonical(authority) || authority.AuthorityID != config.SandboxLeaseAuthorityID {
			continue
		}
		if _, exists := memory.sandboxLeaseAuthorities[authority.ID]; exists {
			delete(memory.sandboxLeaseAuthorities, authority.ID)
			leaseDuplicates[authority.ID] = true
			continue
		}
		if !leaseDuplicates[authority.ID] {
			memory.sandboxLeaseAuthorities[authority.ID] = authority
		}
	}
	memory.currentLeaseAuthority = config.CurrentSandboxLeaseAuthority
	if memory.currentLeaseAuthority == nil {
		memory.currentLeaseAuthority = func(id SandboxLeaseID) (SandboxLeaseAuthority, bool) {
			authority, ok := memory.sandboxLeaseAuthorities[id]
			return authority, ok
		}
	}
	recoveryDuplicates := make(map[RecoveryIntentID]bool)
	for _, intent := range config.CurrentRecoveryIntents {
		if !authorizedRecoveryIntentIsCanonical(intent) || intent.RecoveryAuthorityID != config.RecoveryAuthorityID {
			continue
		}
		if _, exists := memory.recoveryIntents[intent.ID]; exists {
			delete(memory.recoveryIntents, intent.ID)
			recoveryDuplicates[intent.ID] = true
			continue
		}
		if !recoveryDuplicates[intent.ID] {
			memory.recoveryIntents[intent.ID] = cloneAuthorizedRecoveryIntent(intent)
		}
	}
	memory.currentRecoveryIntent = config.CurrentRecoveryIntent
	if memory.currentRecoveryIntent == nil {
		memory.currentRecoveryIntent = func(id RecoveryIntentID) (AuthorizedRecoveryIntent, bool) {
			intent, ok := memory.recoveryIntents[id]
			return cloneAuthorizedRecoveryIntent(intent), ok
		}
	}
	return memory
}

func (m *inMemory) OpenRuntimeView(_ context.Context, request OpenRuntimeViewRequest) (OpenRuntimeViewResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.MaterializationID == "" || request.BaseRevisionID == "" || request.PhaseRunID == "" ||
		request.RuntimeRunID == "" || request.RuntimeOperationID == "" || request.Generation == 0 || request.Fence == 0 ||
		(request.EffectClass != RuntimeViewReadOnly && request.EffectClass != RuntimeViewMutating) || request.ExpiresAt == 0 ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return OpenRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[OpenRuntimeViewResult](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	var expiryDecision runtimeViewExpiryDecision
	reserveExpiryDecision := func() {
		expiryDecision = m.reserveRuntimeViewExpiryDecision(scope)
	}
	created, err := ensureOperationIntent(
		m, scope, request.Operation, request, openRuntimeViewJournalSpec(), reserveExpiryDecision,
	)
	if err != nil {
		return OpenRuntimeViewResult{}, err
	}
	if !created {
		reserveExpiryDecision()
	}
	workspace, workspaceOK := m.workspaces[request.TaskID]
	materialization, materializationOK := m.materializations[request.MaterializationID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID ||
		!materializationOK || materialization.policyDomainID != request.PolicyDomainID || materialization.taskID != request.TaskID ||
		materialization.taskWorkspaceID != request.TaskWorkspaceID {
		err := &Error{Code: ErrorOwnershipDenied}
		recordOperation(m.operations, scope, request.Operation, OpenRuntimeViewResult{}, err)
		return OpenRuntimeViewResult{}, err
	}
	if workspace.currentRevisionID != request.BaseRevisionID || workspace.generation != request.Generation || workspace.fence != request.Fence ||
		materialization.revisionID != request.BaseRevisionID || materialization.checkpointID != workspace.currentCheckpointID ||
		materialization.generation != request.Generation || materialization.fence != request.Fence {
		err := &Error{Code: ErrorStaleAuthority}
		recordOperation(m.operations, scope, request.Operation, OpenRuntimeViewResult{}, err)
		return OpenRuntimeViewResult{}, err
	}
	if !m.sandboxLeaseAuthorityMatches(request) || request.ExpiresAt < expiryDecision.expiresAt || expiryDecision.expiresAt <= m.now() {
		err := &Error{Code: ErrorStaleAuthority}
		recordOperation(m.operations, scope, request.Operation, OpenRuntimeViewResult{}, err)
		return OpenRuntimeViewResult{}, err
	}

	identity := RuntimeViewID(m.operationOpaqueID(scope, "runtime-view", "runtime-view"))
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforeRuntimeViewCreation,
		OperationID: request.Operation.ID,
		SubjectID:   string(identity),
	}); err != nil {
		return OpenRuntimeViewResult{}, err
	}
	markOperationReconciliationRequired(m.operations, scope)
	m.views[identity] = runtimeViewBinding{
		policyDomainID:         request.PolicyDomainID,
		taskID:                 request.TaskID,
		taskWorkspaceID:        request.TaskWorkspaceID,
		materializationID:      request.MaterializationID,
		baseRevisionID:         request.BaseRevisionID,
		phaseRunID:             request.PhaseRunID,
		runtimeRunID:           request.RuntimeRunID,
		runtimeOperationID:     request.RuntimeOperationID,
		sandboxLeaseAuthority:  request.SandboxLeaseAuthority,
		effectClass:            request.EffectClass,
		expiresAt:              expiryDecision.expiresAt,
		expiryPolicyID:         expiryDecision.policyID,
		generation:             request.Generation,
		fence:                  request.Fence,
		readOnlyInputs:         cloneReadOnlyInputMaterializations(materialization.readOnlyInputs),
		artifactVersionID:      materialization.artifactVersionID,
		artifactManifestDigest: materialization.artifactManifestDigest,
		reconstructionEvidence: materialization.reconstructionEvidence,
		publicationAuthorityID: materialization.publicationAuthorityID,
	}
	result := OpenRuntimeViewResult{
		PolicyDomainID:          request.PolicyDomainID,
		TaskID:                  request.TaskID,
		RuntimeViewID:           identity,
		TaskWorkspaceID:         request.TaskWorkspaceID,
		MaterializationID:       request.MaterializationID,
		BaseRevisionID:          request.BaseRevisionID,
		PhaseRunID:              request.PhaseRunID,
		RuntimeRunID:            request.RuntimeRunID,
		RuntimeOperationID:      request.RuntimeOperationID,
		SandboxLeaseAuthority:   request.SandboxLeaseAuthority,
		EffectClass:             request.EffectClass,
		ExpiresAt:               expiryDecision.expiresAt,
		Generation:              request.Generation,
		Fence:                   request.Fence,
		ReadOnlyInputs:          cloneReadOnlyInputMaterializations(materialization.readOnlyInputs),
		SourceArtifactVersionID: materialization.artifactVersionID,
		Operation:               request.Operation,
	}
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterRuntimeViewCreation,
		OperationID: request.Operation.ID,
		SubjectID:   string(identity),
	}); err != nil {
		return OpenRuntimeViewResult{}, err
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) sandboxLeaseAuthorityMatches(request OpenRuntimeViewRequest) bool {
	authority := request.SandboxLeaseAuthority
	return m.sandboxLeaseAuthorityIsCurrent(authority) &&
		authority.PolicyDomainID == request.PolicyDomainID &&
		authority.TaskID == request.TaskID && authority.PhaseRunID == request.PhaseRunID &&
		authority.RuntimeRunID == request.RuntimeRunID && authority.RuntimeOperationID == request.RuntimeOperationID &&
		authority.EffectClass == request.EffectClass &&
		authority.ExpiresAt >= request.ExpiresAt && authority.ExpiresAt > m.now()
}

func (m *inMemory) sandboxLeaseAuthorityIsCurrent(authority SandboxLeaseAuthority) bool {
	current, ok := m.currentLeaseAuthority(authority.ID)
	return ok && current == authority && m.sandboxLeaseAuthorityID != "" &&
		sandboxLeaseAuthorityIsCanonical(authority) && authority.AuthorityID == m.sandboxLeaseAuthorityID
}

func sandboxLeaseAuthorityIsCanonical(authority SandboxLeaseAuthority) bool {
	return authority.ID != "" && authority.EvidenceID != "" && authority.AuthorityID != "" && authority.PolicyDomainID != "" &&
		authority.TaskID != "" && authority.PhaseRunID != "" && authority.RuntimeRunID != "" &&
		authority.RuntimeOperationID != "" &&
		(authority.EffectClass == RuntimeViewReadOnly || authority.EffectClass == RuntimeViewMutating) &&
		authority.LeaseGeneration != 0 && authority.LeaseFence != 0 &&
		authority.ExpiresAt != 0 && validDigest(authority.Digest) && authority.Digest == authority.CanonicalDigest()
}

func (m *inMemory) CommitRuntimeView(ctx context.Context, request CommitRuntimeViewRequest) (CommitRuntimeViewResult, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RuntimeViewID == "" || request.RuntimeOperationID == "" || request.SandboxLeaseAuthority.ID == "" ||
		request.BaseRevisionID == "" || request.ExpectedCurrentRevision == "" ||
		request.Generation == 0 || request.Fence == 0 || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CommitRuntimeViewResult{}, &Error{Code: ErrorInvalidIntent}
	}
	m.beforeRuntimeViewTerminal(RuntimeViewTerminalAttempt{
		RuntimeViewID: request.RuntimeViewID,
		OperationID:   request.Operation.ID,
		Intent:        RuntimeViewCommitIntent,
	})
	m.mu.Lock()
	defer m.mu.Unlock()

	scope := operationScope{
		policyDomainID: request.PolicyDomainID,
		taskID:         request.TaskID,
		operationID:    request.Operation.ID,
	}
	if result, replayed, err := replayOperation[CommitRuntimeViewResult](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	trustedRequest := request
	trustedRequest.DeclaredStateManifest = cloneDeclaredStateManifest(request.DeclaredStateManifest)
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, trustedRequest, commitRuntimeViewJournalSpec(), nil,
	); err != nil {
		return CommitRuntimeViewResult{}, err
	}
	fail := func(code ErrorCode) (CommitRuntimeViewResult, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, CommitRuntimeViewResult{}, err)
		return CommitRuntimeViewResult{}, err
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
	if view.expired {
		return fail(ErrorStaleAuthority)
	}
	if view.terminalDecision != runtimeViewNonTerminal {
		return fail(ErrorViewTerminalConflict)
	}
	if workspace.currentRevisionID != request.ExpectedCurrentRevision || request.ExpectedCurrentRevision != request.BaseRevisionID ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		return fail(ErrorStaleAuthority)
	}
	if !m.sandboxLeaseAuthorityIsCurrent(view.sandboxLeaseAuthority) {
		return fail(ErrorStaleAuthority)
	}
	if view.expiresAt <= m.now() || view.sandboxLeaseAuthority.ExpiresAt <= m.now() {
		return fail(ErrorStaleAuthority)
	}
	if view.effectClass != RuntimeViewMutating {
		return fail(ErrorEffectDenied)
	}
	if !m.validationEvidenceMatches(trustedRequest.ValidationEvidence, trustedRequest, view) {
		return fail(ErrorIntegrityFailure)
	}
	if err := m.injectFault(FaultBeforeDeclaredManifestVerification, request.Operation.ID); err != nil {
		return CommitRuntimeViewResult{}, err
	}
	contentRoot, ok := validateDeclaredStateManifest(trustedRequest.DeclaredStateManifest)
	if !ok {
		return fail(ErrorIntegrityFailure)
	}
	if err := m.injectFault(FaultAfterDeclaredManifestVerification, request.Operation.ID); err != nil {
		return CommitRuntimeViewResult{}, err
	}

	predecessor := workspace.currentRevisionID
	resultingRevision := predecessor
	changed := trustedRequest.DeclaredStateManifest.Digest != workspace.currentManifest
	if changed {
		resultingRevision = RevisionID(m.operationOpaqueID(scope, "revision", "revision"))
	} else {
		predecessor = m.revisions[resultingRevision].predecessor
	}
	checkpointID := CheckpointID(m.operationOpaqueID(scope, "checkpoint", "checkpoint"))
	checkpointEvidence := CheckpointEvidence{}
	var durabilityRoot EvidenceRoot
	if m.durableObject != nil {
		expectedPrepares := len(trustedRequest.DeclaredStateManifest.Members) + 1
		prepareOrdinal := 0
		prepareStarted := false
		progress := func(event DurableContentPrepareProgress) error {
			if event.Ordinal != prepareOrdinal {
				return &Error{Code: ErrorIntegrityFailure}
			}
			switch event.Boundary {
			case DurableContentPrepareBefore:
				if prepareStarted || prepareOrdinal >= expectedPrepares {
					return &Error{Code: ErrorIntegrityFailure}
				}
				if err := m.injectFaultEvent(FaultEvent{
					Point:       FaultBeforeContentPrepare,
					OperationID: request.Operation.ID,
					SubjectID:   event.SubjectID,
					Ordinal:     event.Ordinal,
				}); err != nil {
					return err
				}
				markOperationReconciliationRequired(m.operations, scope)
				prepareStarted = true
				return nil
			case DurableContentPrepareAfter:
				if !prepareStarted {
					return &Error{Code: ErrorIntegrityFailure}
				}
				if err := m.injectFaultEvent(FaultEvent{
					Point:       FaultAfterContentPrepare,
					OperationID: request.Operation.ID,
					SubjectID:   event.SubjectID,
					Ordinal:     event.Ordinal,
				}); err != nil {
					return err
				}
				prepareStarted = false
				prepareOrdinal++
				return nil
			default:
				return &Error{Code: ErrorIntegrityFailure}
			}
		}
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
			Progress:          progress,
		})
		if err != nil {
			if errors.Is(err, ErrDurableObjectResultAmbiguous) {
				markOperationReconciliationRequired(m.operations, scope)
				return CommitRuntimeViewResult{}, &Error{Code: ErrorReconciliationRequired}
			}
			var lifecycleError *Error
			if errors.As(err, &lifecycleError) && lifecycleError.Code == ErrorReconciliationRequired {
				return CommitRuntimeViewResult{}, lifecycleError
			}
			return fail(ErrorIntegrityFailure)
		}
		if prepareStarted || prepareOrdinal != expectedPrepares {
			return fail(ErrorIntegrityFailure)
		}
		var trusted bool
		var bindErr error
		checkpointEvidence, contentRoot, durabilityRoot, trusted, bindErr = m.bindCheckpointEvidence(
			trustedRequest,
			resultingRevision,
			checkpointID,
			prepared,
		)
		if bindErr != nil {
			return CommitRuntimeViewResult{}, bindErr
		}
		if !trusted {
			return fail(ErrorIntegrityFailure)
		}
		markOperationVerified(m.operations, scope)
	} else {
		return fail(ErrorIntegrityFailure)
	}
	if !m.sandboxLeaseAuthorityIsCurrent(view.sandboxLeaseAuthority) {
		return fail(ErrorStaleAuthority)
	}
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforeAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(checkpointID),
	}); err != nil {
		return CommitRuntimeViewResult{}, err
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
	workspace.fence++
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
	view.terminalDecision = runtimeViewCommitted
	m.views[request.RuntimeViewID] = view
	validatedExportEvidence := ValidatedExportEvidence{}
	if view.artifactVersionID != "" {
		validatedExportEvidence = ValidatedExportEvidence{
			ID:                           ValidatedExportEvidenceID(m.operationOpaqueID(scope, "validated-export", "validated-export-evidence")),
			PublicationAuthorityID:       view.publicationAuthorityID,
			PolicyDomainID:               request.PolicyDomainID,
			TaskID:                       request.TaskID,
			TaskWorkspaceID:              request.TaskWorkspaceID,
			SourceArtifactVersionID:      view.artifactVersionID,
			ReconstructionEvidenceID:     view.reconstructionEvidence.ID,
			ReconstructionEvidenceDigest: view.reconstructionEvidence.Digest,
			RevisionID:                   resultingRevision,
			CheckpointID:                 checkpointID,
			ManifestDigest:               trustedRequest.DeclaredStateManifest.Digest,
			ValidationEvidenceID:         request.ValidationEvidence.ID,
			ValidationEvidenceDigest:     request.ValidationEvidence.Digest,
			ContentEvidenceRoot:          contentRoot,
			DurabilityEvidenceRoot:       durabilityRoot,
			Generation:                   request.Generation,
			Fence:                        workspace.fence,
			OperationID:                  request.Operation.ID,
		}
		validatedExportEvidence.Digest = validatedExportEvidence.CanonicalDigest()
	}

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
		ValidatedExportEvidence:  validatedExportEvidence,
		Generation:               request.Generation,
		PreviousFence:            request.Fence,
		Fence:                    workspace.fence,
		Operation:                request.Operation,
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(checkpointID),
	}); err != nil {
		return CommitRuntimeViewResult{}, err
	}
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) beforeRuntimeViewTerminal(attempt RuntimeViewTerminalAttempt) {
	if m.beforeTerminal != nil {
		m.beforeTerminal(attempt)
	}
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
		evidence.RuntimeRunID == view.runtimeRunID && evidence.RuntimeOperationID == view.runtimeOperationID &&
		evidence.SandboxLeaseAuthorityDigest == view.sandboxLeaseAuthority.Digest &&
		evidence.ManifestDigest == request.DeclaredStateManifest.Digest &&
		evidence.Generation == request.Generation && evidence.Fence == request.Fence
}

func (m *inMemory) bindCheckpointEvidence(
	request CommitRuntimeViewRequest,
	revisionID RevisionID,
	checkpointID CheckpointID,
	prepared VerifiedCheckpointContent,
) (CheckpointEvidence, EvidenceRoot, EvidenceRoot, bool, error) {
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
		return CheckpointEvidence{}, "", "", false, nil
	}

	if len(prepared.ContentReferences) != len(request.DeclaredStateManifest.Members) {
		return CheckpointEvidence{}, "", "", false, nil
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
			return CheckpointEvidence{}, "", "", false, nil
		}
		if _, duplicate := seenReferences[reference.ID]; duplicate {
			return CheckpointEvidence{}, "", "", false, nil
		}
		if _, duplicate := seenMembers[reference.StateMemberID]; duplicate {
			return CheckpointEvidence{}, "", "", false, nil
		}
		seenReferences[reference.ID] = struct{}{}
		seenMembers[reference.StateMemberID] = struct{}{}
	}

	contentRoot, durabilityRoot, durable, err := m.durableContentRoots(
		prepared,
		func(point FaultPoint, ordinal int, subjectID string) error {
			return m.injectFaultEvent(FaultEvent{
				Point:       point,
				OperationID: request.Operation.ID,
				SubjectID:   subjectID,
				Ordinal:     ordinal,
			})
		},
	)
	if err != nil {
		return CheckpointEvidence{}, "", "", false, err
	}
	if !durable {
		return CheckpointEvidence{}, "", "", false, nil
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
	return evidence, contentRoot, durabilityRoot, true, nil
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
	contentRoot, durabilityRoot, durable, err := m.durableContentRoots(verified, nil)
	if err != nil {
		return CheckpointEvidence{}, false
	}
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
	base.ID = CheckpointIntegrityID(m.operationOpaqueID(
		operationScope{base.PolicyDomainID, base.TaskID, operation.ID},
		"checkpoint-integrity:"+string(base.CheckpointID),
		"checkpoint-integrity",
	))
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
	receiptBoundary func(FaultPoint, int, string) error,
) (EvidenceRoot, EvidenceRoot, bool, error) {
	references := canonicalContentReferences(content.ManifestReference, content.ContentReferences)
	requiredReceipts := make(map[ContentID]durableContentFact, len(references))
	seenReferenceIDs := make(map[ContentReferenceID]struct{}, len(references))
	for _, reference := range references {
		if !contentReferenceIsCanonical(reference) {
			return "", "", false, nil
		}
		if _, duplicate := seenReferenceIDs[reference.ID]; duplicate {
			return "", "", false, nil
		}
		seenReferenceIDs[reference.ID] = struct{}{}
		fact := durableContentFact{
			policyDomainID: reference.PolicyDomainID,
			contentDigest:  reference.ContentDigest,
			size:           reference.Size,
		}
		if existing, duplicate := requiredReceipts[reference.ContentID]; duplicate && existing != fact {
			return "", "", false, nil
		}
		requiredReceipts[reference.ContentID] = fact
	}
	if len(content.DurabilityReceipts) != len(requiredReceipts) {
		return "", "", false, nil
	}
	seenReceiptIDs := make(map[DurabilityReceiptID]struct{}, len(content.DurabilityReceipts))
	seenReceiptContent := make(map[ContentID]struct{}, len(content.DurabilityReceipts))
	for ordinal, receipt := range canonicalDurabilityReceipts(content.DurabilityReceipts) {
		if receiptBoundary != nil {
			if err := receiptBoundary(FaultBeforeDurabilityReceiptVerification, ordinal, string(receipt.ID)); err != nil {
				return "", "", false, err
			}
		}
		fact, required := requiredReceipts[receipt.ContentID]
		if !required || !durabilityReceiptIsCanonical(receipt) ||
			receipt.DurabilityAuthorityID != m.durabilityAuthorityID ||
			receipt.PolicyDomainID != fact.policyDomainID ||
			receipt.ContentDigest != fact.contentDigest || receipt.Size != fact.size {
			return "", "", false, nil
		}
		if _, duplicate := seenReceiptIDs[receipt.ID]; duplicate {
			return "", "", false, nil
		}
		if _, duplicate := seenReceiptContent[receipt.ContentID]; duplicate {
			return "", "", false, nil
		}
		seenReceiptIDs[receipt.ID] = struct{}{}
		seenReceiptContent[receipt.ContentID] = struct{}{}
		if receiptBoundary != nil {
			if err := receiptBoundary(FaultAfterDurabilityReceiptVerification, ordinal, string(receipt.ID)); err != nil {
				return "", "", false, err
			}
		}
	}
	if !m.durableEvidenceIdentitiesAreConsistent(content) {
		return "", "", false, nil
	}
	receipts := canonicalDurabilityReceipts(content.DurabilityReceipts)
	contentRoot := EvidenceRoot(canonicalDigest(struct {
		References []ContentReference
	}{References: references}))
	durabilityRoot := EvidenceRoot(canonicalDigest(struct {
		Receipts []DurabilityReceipt
	}{Receipts: receipts}))
	return contentRoot, durabilityRoot, true, nil
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
	scope := operationScope{
		policyDomainID: request.PolicyDomainID,
		taskID:         request.TaskID,
		operationID:    request.Operation.ID,
	}
	if result, replayed, err := replayOperation[ConfirmTaskWorkspaceResult](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	var plannedWorkspaceID TaskWorkspaceID
	var plannedRevisionID RevisionID
	reserveIdentities := func() {
		plannedWorkspaceID = TaskWorkspaceID(m.operationOpaqueID(scope, "workspace", "workspace"))
		plannedRevisionID = RevisionID(m.operationOpaqueID(scope, "initial-revision", "revision"))
		record := m.operations[scope]
		record.expectedRevisionID = plannedRevisionID
		m.operations[scope] = record
	}
	created, err := ensureOperationIntent(
		m, scope, request.Operation, request, confirmJournalSpec(), reserveIdentities,
	)
	if err != nil {
		return ConfirmTaskWorkspaceResult{}, err
	}
	if !created {
		reserveIdentities()
	}
	if existing, ok := m.workspaces[request.TaskID]; ok {
		if existing.policyDomainID != request.PolicyDomainID {
			err := &Error{Code: ErrorOwnershipDenied}
			recordOperation(m.operations, scope, request.Operation, ConfirmTaskWorkspaceResult{}, err)
			return ConfirmTaskWorkspaceResult{}, err
		}
		result := confirmResult(existing)
		recordOperation(m.operations, scope, request.Operation, result, nil)
		return deliverOperationResponse(m, request.Operation.ID, result)
	}

	emptyManifest := DeclaredStateManifest{}
	binding := workspaceBinding{
		policyDomainID:    request.PolicyDomainID,
		taskWorkspaceID:   plannedWorkspaceID,
		currentRevisionID: plannedRevisionID,
		currentManifest:   emptyManifest.CanonicalDigest(),
		generation:        1,
		fence:             1,
	}
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforeAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(plannedWorkspaceID),
	}); err != nil {
		return ConfirmTaskWorkspaceResult{}, err
	}
	m.workspaces[request.TaskID] = binding
	m.revisions[binding.currentRevisionID] = revisionRecord{
		taskWorkspaceID: binding.taskWorkspaceID,
		manifestDigest:  binding.currentManifest,
	}
	result := confirmResult(binding)
	recordOperation(m.operations, scope, request.Operation, result, nil)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterAuthoritativeTransaction,
		OperationID: request.Operation.ID,
		SubjectID:   string(plannedWorkspaceID),
	}); err != nil {
		return ConfirmTaskWorkspaceResult{}, err
	}
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) Materialize(ctx context.Context, request MaterializeRequest) (MaterializeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.RevisionID == "" || request.Generation == 0 || request.Fence == 0 ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return MaterializeResult{}, &Error{Code: ErrorInvalidIntent}
	}
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[MaterializeResult](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, materializeJournalSpec(), nil,
	); err != nil {
		return MaterializeResult{}, err
	}
	workspace, ok := m.workspaces[request.TaskID]
	if !ok || workspace.policyDomainID != request.PolicyDomainID || workspace.taskWorkspaceID != request.TaskWorkspaceID {
		err := &Error{Code: ErrorOwnershipDenied}
		recordOperation(m.operations, scope, request.Operation, MaterializeResult{}, err)
		return MaterializeResult{}, err
	}
	if workspace.currentRevisionID != request.RevisionID || workspace.currentCheckpointID != request.CheckpointID ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		err := &Error{Code: ErrorStaleAuthority}
		recordOperation(m.operations, scope, request.Operation, MaterializeResult{}, err)
		return MaterializeResult{}, err
	}

	identity := MaterializationID(m.operationOpaqueID(scope, "materialization", "materialization"))
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforeBaseMaterialization,
		OperationID: request.Operation.ID,
		SubjectID:   string(identity),
	}); err != nil {
		return MaterializeResult{}, err
	}
	markOperationReconciliationRequired(m.operations, scope)

	checkpointEvidence := CheckpointEvidence{}
	checkpointID := request.CheckpointID
	if checkpointID != "" {
		checkpoint, exists := m.checkpoints[checkpointID]
		if !exists || checkpoint.taskWorkspaceID != request.TaskWorkspaceID || checkpoint.revisionID != request.RevisionID ||
			checkpoint.manifestDigest != workspace.currentManifest || m.durableObject == nil {
			err := &Error{Code: ErrorIntegrityFailure}
			recordOperation(m.operations, scope, request.Operation, MaterializeResult{}, err)
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
			if errors.Is(verifyErr, ErrDurableObjectResultAmbiguous) {
				return MaterializeResult{}, &Error{Code: ErrorReconciliationRequired}
			}
			var lifecycleError *Error
			if errors.As(verifyErr, &lifecycleError) && lifecycleError.Code == ErrorReconciliationRequired {
				return MaterializeResult{}, lifecycleError
			}
			err := &Error{Code: ErrorIntegrityFailure}
			recordOperation(m.operations, scope, request.Operation, MaterializeResult{}, err)
			return MaterializeResult{}, err
		}
		var trusted bool
		checkpointEvidence, trusted = m.reverifyCheckpointEvidence(request, checkpoint.evidence, verified)
		if !trusted {
			err := &Error{Code: ErrorIntegrityFailure}
			recordOperation(m.operations, scope, request.Operation, MaterializeResult{}, err)
			return MaterializeResult{}, err
		}
		checkpoint.evidence = cloneCheckpointEvidence(checkpointEvidence)
		m.checkpoints[checkpointID] = checkpoint
		m.recordDurableEvidenceIdentities(verifiedCheckpointContent(checkpointEvidence))
	}

	m.materializations[identity] = materializationBinding{
		policyDomainID:  request.PolicyDomainID,
		taskID:          request.TaskID,
		taskWorkspaceID: request.TaskWorkspaceID,
		revisionID:      request.RevisionID,
		checkpointID:    request.CheckpointID,
		generation:      request.Generation,
		fence:           request.Fence,
		expiryPolicyID:  m.expiryPolicy.ID,
		expiresAt:       m.now() + Instant(m.expiryPolicy.MaterializationLifetime),
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
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterBaseMaterialization,
		OperationID: request.Operation.ID,
		SubjectID:   string(identity),
	}); err != nil {
		return MaterializeResult{}, err
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	return deliverOperationResponse(m, request.Operation.ID, result)
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

func replayOperation[Result any](
	records map[operationScope]operationRecord,
	scope operationScope,
	operation Operation,
) (Result, bool, error) {
	var zero Result
	record, ok := records[scope]
	if !ok {
		return zero, false, nil
	}
	if record.requestDigest != operation.RequestDigest {
		return zero, true, &Error{Code: ErrorIntegrityConflict}
	}
	if record.state != operationJournalTerminal {
		return zero, false, nil
	}
	access, ok := record.payload.(operationResultAccess[Result])
	if !ok {
		return zero, true, &Error{Code: ErrorIntegrityConflict}
	}
	result, resultSet := access.operationResult()
	if !resultSet {
		return zero, true, &Error{Code: ErrorIntegrityConflict}
	}
	if record.err != nil {
		return result, true, cloneLifecycleError(record.err)
	}
	return result, true, nil
}

func recordOperation[Result any](
	records map[operationScope]operationRecord,
	scope operationScope,
	operation Operation,
	result Result,
	err *Error,
) {
	record := records[scope]
	record.requestDigest = operation.RequestDigest
	record.state = operationJournalTerminal
	if err == nil {
		record.intentState = OperationIntentActivated
	} else {
		record.intentState = OperationIntentRejected
	}
	access, ok := record.payload.(operationResultAccess[Result])
	if !ok {
		record.err = &Error{Code: ErrorIntegrityConflict}
		records[scope] = record
		return
	}
	access.storeOperationResult(result)
	record.err = cloneLifecycleError(err)
	records[scope] = record
}

func cloneLifecycleError(err *Error) *Error {
	if err == nil {
		return nil
	}
	clone := *err
	return &clone
}
