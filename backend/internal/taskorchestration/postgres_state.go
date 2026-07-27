package taskorchestration

import (
	"encoding/json"
	"sort"
	"time"
)

const postgresTaskStateVersion uint16 = 1

type postgresTaskState struct {
	Version                  uint16
	Revision                 TaskRevision
	ActivityGeneration       ActivityGeneration
	RecoveryActivityFence    ActivityGeneration
	Owner                    postgresOwnershipState
	LatestDecision           postgresDecisionState
	DecisionCount            uint64
	EnactmentCount           uint64
	SafetyEpoch              SafetyEpoch
	PhaseRuns                []postgresPhaseRunBindingState
	RuntimeOperations        []postgresRuntimeOperationState
	ValidationBindings       []postgresValidationBindingState
	LifecycleOperations      []postgresLifecycleOperationState
	PublicationOperations    []postgresPublicationOperationState
	SchedulingOperations     []postgresSchedulingOperationState
	EvidenceDiagnosticCount  uint64
	LatestEvidenceDiagnostic postgresEvidenceDiagnosticState
	LatestRevisionID         string
	LatestCheckpointID       string
	CancellationState        CancellationState
	Enactments               []postgresEnactmentState
	Reconciler               postgresAuthorityState
	ReconciliationFences     []postgresReconciliationFenceState
	Aggregate                *postgresAggregateState
	GateDecisions            []postgresGateDecisionState
}

type postgresOwnershipState struct {
	AuthorityID string
	Generation  AuthorizationGeneration
}

type postgresAuthorityState struct {
	Kind       AuthorityKind
	ID         string
	Generation AuthorizationGeneration
	Reason     AdministratorReason
}

type postgresDecisionState struct {
	DecisionID             string
	DecisionRequestID      string
	CanonicalRequestDigest CanonicalRequestDigest
	PreviousTaskRevision   TaskRevision
	AcceptedTaskRevision   TaskRevision
	TaskProjection         postgresTaskProjectionState
	AffectedPhaseRuns      []string
	AcceptedEvidenceRefs   []postgresEvidenceRefState
	CommittedAt            string
	EnactmentRefs          []postgresEnactmentState
	MandatoryAuditFact     postgresAuditFactState
}

type postgresAuditFactState struct {
	SchemaVersion                AuditSchemaVersion
	AuditFactID                  string
	CanonicalDigest              AuditFactDigest
	IntegrityVersion             AuditIntegrityVersion
	OwningModule                 AuditOwningModule
	DecisionID                   string
	TaskID                       string
	IdempotencyDecisionRequestID string
	Action                       IntentKind
	Result                       AuditResult
	AuthorityKind                AuthorityKind
	AuthorityID                  string
	AuthorizationGeneration      AuthorizationGeneration
	AuthorityReason              AdministratorReason
	ReasonCode                   AuditReasonCode
	BeforeTaskRevision           TaskRevision
	AfterTaskRevision            TaskRevision
	BeforeStatus                 TaskStatus
	AfterStatus                  TaskStatus
	BeforeActivityGeneration     ActivityGeneration
	AfterActivityGeneration      ActivityGeneration
	RecoveryGeneration           RecoveryGeneration
	BeforeSafetyEpoch            SafetyEpoch
	AfterSafetyEpoch             SafetyEpoch
	EvidenceRefs                 []postgresEvidenceRefState
	EnactmentRefs                []postgresAuditEnactmentState
	RetryPhaseRunID              string
	RetryRuntimeRunID            string
	ReconciliationOperationID    string
	ReconciliationFence          ReconciliationFence
	OccurredAt                   string
	RecordedAt                   string
	SourceClock                  AuditSourceClock
}

type postgresAuditEnactmentState struct {
	OperationID        string
	Kind               EnactmentKind
	PayloadDigest      EnactmentPayloadDigest
	ActivityGeneration ActivityGeneration
	FenceKind          EnactmentFenceKind
	Fence              uint64
	CausationID        string
}

type postgresTaskProjectionState struct {
	TaskID                           string
	TaskRevision                     TaskRevision
	ActivityGeneration               ActivityGeneration
	Status                           TaskStatus
	Route                            Route
	Activity                         ActivityKind
	ExecutionLockID                  string
	TemplateLockID                   string
	CurrentPhase                     string
	ActivePhaseRunID                 string
	LatestArtifactVersionID          string
	TaskWorkspaceID                  string
	LatestRevisionID                 string
	LatestCheckpointID               string
	TaskWorkspaceLifecycleGeneration TaskWorkspaceLifecycleGeneration
	TaskWorkspaceLifecycleFence      TaskWorkspaceLifecycleFence
	CancellationState                CancellationState
	SafetyEpoch                      SafetyEpoch
	OperationalMode                  OperationalMode
}

type postgresEvidenceRefState struct {
	ID     string
	Kind   EvidenceKind
	Digest EvidenceDigest
}

type postgresEvidenceDiagnosticState struct {
	EvidenceID  string
	Disposition EvidenceDisposition
	Reason      EvidenceDiagnosticReason
}

type postgresEnactmentState struct {
	OperationID        string
	Kind               EnactmentKind
	PayloadDigest      EnactmentPayloadDigest
	ActivityGeneration ActivityGeneration
	FenceKind          EnactmentFenceKind
	Fence              uint64
	CausationID        string
}

type postgresPhaseRunBindingState struct {
	PhaseRunID string
	Generation PhaseRunGeneration
	Fence      PhaseRunFence
	Active     bool
}

type postgresRuntimeOperationState struct {
	OperationID        string
	PhaseRunID         string
	RuntimeRunID       string
	Authority          postgresAuthorityState
	Generation         RuntimeGeneration
	Fence              RuntimeFence
	SafetyEpoch        SafetyEpoch
	ActivityGeneration ActivityGeneration
	Terminal           bool
}

type postgresValidationBindingState struct {
	PhaseRunID         string
	Authority          postgresAuthorityState
	Generation         ProducerGeneration
	Fence              ValidationFence
	SafetyEpoch        SafetyEpoch
	ActivityGeneration ActivityGeneration
	Terminal           bool
}

type postgresLifecycleOperationState struct {
	OperationID        string
	PhaseRunID         string
	Authority          postgresAuthorityState
	Generation         TaskWorkspaceLifecycleGeneration
	Fence              TaskWorkspaceLifecycleFence
	SafetyEpoch        SafetyEpoch
	Purpose            LifecycleOperationPurpose
	ActivityGeneration ActivityGeneration
	Terminal           bool
}

type postgresPublicationOperationState struct {
	OperationID        string
	PhaseRunID         string
	Authority          postgresAuthorityState
	Generation         ProducerGeneration
	Fence              PublicationFence
	SafetyEpoch        SafetyEpoch
	ActivityGeneration ActivityGeneration
	Terminal           bool
}

type postgresSchedulingOperationState struct {
	OperationID        string
	PhaseRunID         string
	Authority          postgresAuthorityState
	Generation         ProducerGeneration
	Fence              SchedulerFence
	SafetyEpoch        SafetyEpoch
	ActivityGeneration ActivityGeneration
	Terminal           bool
}

type postgresReconciliationFenceState struct {
	OperationID string
	Fence       ReconciliationFence
}

type postgresGateDecisionState struct {
	DecisionRequestID string
	Digest            CanonicalRequestDigest
	Decision          postgresDecisionState
}

type postgresAggregateState struct {
	Status                          TaskStatus
	Route                           Route
	Activity                        ActivityKind
	Pinned                          postgresPinnedTaskState
	CurrentPhase                    string
	ActivePhaseRunID                string
	PhaseRuns                       []postgresPhaseRunState
	LatestArtifactVersionID         string
	ActivitySourceArtifactVersionID string
	ReconstructionRequired          bool
	ReconstructionAccepted          bool
	ReconstructionOperationID       string
	LifecycleGeneration             TaskWorkspaceLifecycleGeneration
	LifecycleFence                  TaskWorkspaceLifecycleFence
}

type postgresPinnedTaskState struct {
	Route           Route
	TaskWorkspaceID string
	ExecutionLock   postgresExecutionLockState
	TemplateLockID  string
	Authorities     postgresDownstreamAuthorityBindingsState
}

type postgresDownstreamAuthorityBindingsState struct {
	Runtime                postgresAuthorityState
	Validator              postgresAuthorityState
	TaskWorkspaceLifecycle postgresAuthorityState
	Publication            postgresAuthorityState
	Scheduler              postgresAuthorityState
}

type postgresExecutionLockState struct {
	ID                      string
	PipelineVersionID       string
	RuntimeReleaseID        string
	CompatibilityApprovalID string
	PipelineContract        postgresPipelineContractState
}

type postgresPipelineContractState struct {
	SchemaVersion        PipelineContractVersion
	PipelineVersionID    string
	Routes               []postgresRouteDefinitionState
	ManualEditEntryPhase string
	ManualEditPhases     []postgresPhaseDefinitionState
}

type postgresRouteDefinitionState struct {
	Route      Route
	EntryPhase string
	Phases     []postgresPhaseDefinitionState
}

type postgresPhaseDefinitionState struct {
	Key                 string
	Kind                PhaseKind
	ValidationContract  PhaseValidationContract
	RequiredRuntimeRuns uint32
	RetryEligible       bool
	GateID              string
	NextPhase           string
}

type postgresPhaseRunState struct {
	ID                     string
	PhaseKey               string
	Attempt                uint32
	Generation             PhaseRunGeneration
	Fence                  PhaseRunFence
	Outcome                PhaseRunOutcome
	ValidationOutcome      PhaseValidationOutcome
	LifecycleOutcome       TaskWorkspaceLifecycleOutcome
	RevisionID             string
	CheckpointID           string
	LifecycleOperationID   string
	PublicationOutcome     PublicationOutcome
	ArtifactVersionID      string
	PublicationOperationID string
	RuntimeRuns            []postgresRuntimeRunState
	TaskWorkspaceID        string
	InputArtifactVersionID string
}

type postgresRuntimeRunState struct {
	ID          string
	OperationID string
	Outcome     RuntimeRunOutcome
}

func encodePostgresTaskState(record taskRecord) ([]byte, error) {
	return json.Marshal(postgresTaskStateFromRecord(record))
}

func decodePostgresTaskState(encoded []byte) (taskRecord, error) {
	var state postgresTaskState
	if err := json.Unmarshal(encoded, &state); err != nil || state.Version != postgresTaskStateVersion {
		return taskRecord{}, newPersistenceError(PersistenceStateCorrupt)
	}
	record := state.taskRecord()
	if !validPostgresTaskRecord(record) {
		return taskRecord{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return record, nil
}

func validPostgresTaskRecord(record taskRecord) bool {
	if record.revision == 0 || record.activityGeneration == 0 || record.safetyEpoch == 0 ||
		!validOpaqueID(record.owner.authorityID.value) || record.owner.generation == 0 ||
		!validPersistedDecision(record.latestDecision) ||
		!validOptionalOpaqueID(record.latestRevisionID.value) ||
		!validOptionalOpaqueID(record.latestCheckpointID.value) ||
		record.cancellationState > CancellationCancelled {
		return false
	}
	if record.evidenceDiagnosticCount > 0 &&
		(!validOpaqueID(record.latestEvidenceDiagnostic.EvidenceID.value) ||
			record.latestEvidenceDiagnostic.Disposition != EvidenceDispositionNonAuthoritative ||
			record.latestEvidenceDiagnostic.Reason < EvidenceDiagnosticScopeConflict ||
			record.latestEvidenceDiagnostic.Reason > EvidenceDiagnosticUnauthorized) {
		return false
	}
	for id, binding := range record.phaseRuns {
		if !validOpaqueID(id.value) || binding.generation == 0 || binding.fence == 0 {
			return false
		}
	}
	for id, operation := range record.runtimeOperations {
		if !validOpaqueID(id.value) || !validOpaqueID(operation.phaseRunID.value) ||
			!validOpaqueID(operation.runtimeRunID.value) || operation.generation == 0 ||
			operation.fence == 0 || operation.safetyEpoch == 0 ||
			operation.activityGeneration == 0 || !validOptionalAuthority(operation.authority) {
			return false
		}
	}
	for id, binding := range record.validationBindings {
		if !validOpaqueID(id.value) || binding.generation == 0 || binding.fence == 0 ||
			binding.safetyEpoch == 0 || binding.activityGeneration == 0 ||
			!validOptionalAuthority(binding.authority) {
			return false
		}
	}
	for id, operation := range record.lifecycleOperations {
		validPhaseScope := validOpaqueID(operation.phaseRunID.value) ||
			(operation.purpose == LifecycleOperationReconstruction && operation.phaseRunID == (PhaseRunID{}))
		if !validOpaqueID(id.value) || !validPhaseScope ||
			operation.generation == 0 || operation.fence == 0 || operation.safetyEpoch == 0 ||
			operation.activityGeneration == 0 || !validOptionalAuthority(operation.authority) ||
			(operation.purpose != LifecycleOperationCommit &&
				operation.purpose != LifecycleOperationCancellationFence &&
				operation.purpose != LifecycleOperationReconstruction) {
			return false
		}
	}
	for id, operation := range record.publicationOperations {
		if !validOpaqueID(id.value) || !validOpaqueID(operation.phaseRunID.value) ||
			operation.generation == 0 || operation.fence == 0 || operation.safetyEpoch == 0 ||
			operation.activityGeneration == 0 || !validOptionalAuthority(operation.authority) {
			return false
		}
	}
	for id, operation := range record.schedulingOperations {
		if !validOpaqueID(id.value) || !validOpaqueID(operation.phaseRunID.value) ||
			operation.generation == 0 || operation.fence == 0 || operation.safetyEpoch == 0 ||
			operation.activityGeneration == 0 || !validOptionalAuthority(operation.authority) {
			return false
		}
	}
	for id, enactment := range record.enactments {
		if id != enactment.OperationID || !validPersistedEnactment(enactment) {
			return false
		}
	}
	if record.reconciler != (authorityValue{}) && !record.reconciler.valid() {
		return false
	}
	for id, fence := range record.reconciliationFences {
		if !validOpaqueID(id.value) || fence == 0 {
			return false
		}
	}
	for id, gate := range record.gateDecisions {
		if !validOpaqueID(id.value) || gate.digest == (CanonicalRequestDigest{}) ||
			!validPersistedDecision(gate.decision) {
			return false
		}
	}
	if record.aggregate != nil && !validPostgresAggregate(*record.aggregate) {
		return false
	}
	return true
}

func validOptionalAuthority(authority authorityValue) bool {
	return authority == (authorityValue{}) || authority.valid()
}

func validPostgresAggregate(aggregate taskAggregate) bool {
	if !aggregate.pinned.valid() || !validOptionalOpaqueID(aggregate.currentPhase.value) {
		return false
	}
	if aggregate.reconstructionRequired {
		if aggregate.activity != ActivityManualEdit ||
			!validOpaqueID(aggregate.reconstructionOperationID.value) ||
			!validOpaqueID(aggregate.activitySourceArtifactVersionID.value) {
			return false
		}
	} else if aggregate.reconstructionOperationID != (OperationID{}) {
		return false
	}
	if (aggregate.lifecycleGeneration == 0) != (aggregate.lifecycleFence == 0) {
		return false
	}
	for _, run := range aggregate.phaseRuns {
		if !validOpaqueID(run.id.value) || !validOpaqueID(run.phaseKey.value) ||
			run.attempt == 0 || run.generation == 0 || run.fence == 0 ||
			!validOptionalOpaqueID(run.revisionID.value) ||
			!validOptionalOpaqueID(run.checkpointID.value) ||
			!validOptionalOpaqueID(run.lifecycleOperationID.value) ||
			!validOptionalOpaqueID(run.artifactVersionID.value) ||
			!validOptionalOpaqueID(run.publicationOperationID.value) ||
			!validOptionalOpaqueID(run.taskWorkspaceID.value) ||
			!validOptionalOpaqueID(run.inputArtifactVersionID.value) {
			return false
		}
		for _, runtimeRun := range run.runtimeRuns {
			if !validOpaqueID(runtimeRun.id.value) || !validOpaqueID(runtimeRun.operationID.value) {
				return false
			}
		}
	}
	return validOptionalOpaqueID(aggregate.activePhaseRunID.value) &&
		validOptionalOpaqueID(aggregate.latestArtifactVersionID.value) &&
		validOptionalOpaqueID(aggregate.activitySourceArtifactVersionID.value)
}

func validOptionalOpaqueID(value string) bool {
	return value == "" || validOpaqueID(value)
}

func validPersistedDecision(decision TransitionDecision) bool {
	if !validOpaqueID(decision.DecisionID.value) ||
		!validOpaqueID(decision.DecisionRequestID.value) ||
		decision.CanonicalRequestDigest == (CanonicalRequestDigest{}) ||
		decision.AcceptedTaskRevision != decision.PreviousTaskRevision+1 ||
		!validOpaqueID(decision.TaskProjection.TaskID.value) ||
		decision.TaskProjection.TaskRevision != decision.AcceptedTaskRevision ||
		decision.TaskProjection.ActivityGeneration == 0 || decision.CommittedAt.IsZero() ||
		!validMandatoryAuditFact(decision.MandatoryAuditFactRef, decision) {
		return false
	}
	for _, id := range decision.AffectedPhaseRuns {
		if !validOpaqueID(id.value) {
			return false
		}
	}
	for _, evidence := range decision.AcceptedEvidenceRefs {
		if !validOpaqueID(evidence.ID.value) || evidence.Digest == (EvidenceDigest{}) ||
			evidence.Kind < EvidenceRuntime || evidence.Kind > EvidenceScheduling {
			return false
		}
	}
	for _, enactment := range decision.EnactmentRefs {
		if !validPersistedEnactment(enactment) {
			return false
		}
	}
	return true
}

func validPersistedEnactment(enactment EnactmentRef) bool {
	_, fence := postgresFenceValue(enactment.Fence)
	return validOpaqueID(enactment.OperationID.value) &&
		enactment.Kind >= EnactmentRuntimeExecution && enactment.Kind <= EnactmentPresentConfirmationGate &&
		enactment.PayloadDigest != (EnactmentPayloadDigest{}) && enactment.ActivityGeneration > 0 &&
		enactment.Fence != nil && fence > 0 && validOpaqueID(enactment.CausationID.value)
}

func postgresTaskStateFromRecord(record taskRecord) postgresTaskState {
	state := postgresTaskState{
		Version:                  postgresTaskStateVersion,
		Revision:                 record.revision,
		ActivityGeneration:       record.activityGeneration,
		RecoveryActivityFence:    record.recoveryActivityFence,
		Owner:                    postgresOwnershipState{record.owner.authorityID.value, record.owner.generation},
		LatestDecision:           postgresDecisionStateFromDecision(record.latestDecision),
		DecisionCount:            record.decisionCount,
		EnactmentCount:           record.enactmentCount,
		SafetyEpoch:              record.safetyEpoch,
		EvidenceDiagnosticCount:  record.evidenceDiagnosticCount,
		LatestEvidenceDiagnostic: postgresEvidenceDiagnosticStateFromDiagnostic(record.latestEvidenceDiagnostic),
		LatestRevisionID:         record.latestRevisionID.value,
		LatestCheckpointID:       record.latestCheckpointID.value,
		CancellationState:        record.cancellationState,
		Reconciler:               postgresAuthorityStateFromAuthority(record.reconciler),
	}
	for id, binding := range record.phaseRuns {
		state.PhaseRuns = append(state.PhaseRuns, postgresPhaseRunBindingState{
			PhaseRunID: id.value, Generation: binding.generation, Fence: binding.fence, Active: binding.active,
		})
	}
	for id, operation := range record.runtimeOperations {
		state.RuntimeOperations = append(state.RuntimeOperations, postgresRuntimeOperationState{
			OperationID: id.value, PhaseRunID: operation.phaseRunID.value,
			RuntimeRunID: operation.runtimeRunID.value,
			Authority:    postgresAuthorityStateFromAuthority(operation.authority),
			Generation:   operation.generation, Fence: operation.fence,
			SafetyEpoch: operation.safetyEpoch, ActivityGeneration: operation.activityGeneration,
			Terminal: operation.terminal,
		})
	}
	for id, binding := range record.validationBindings {
		state.ValidationBindings = append(state.ValidationBindings, postgresValidationBindingState{
			PhaseRunID: id.value, Authority: postgresAuthorityStateFromAuthority(binding.authority),
			Generation: binding.generation, Fence: binding.fence, SafetyEpoch: binding.safetyEpoch,
			ActivityGeneration: binding.activityGeneration, Terminal: binding.terminal,
		})
	}
	for id, operation := range record.lifecycleOperations {
		state.LifecycleOperations = append(state.LifecycleOperations, postgresLifecycleOperationState{
			OperationID: id.value, PhaseRunID: operation.phaseRunID.value,
			Authority:  postgresAuthorityStateFromAuthority(operation.authority),
			Generation: operation.generation, Fence: operation.fence, SafetyEpoch: operation.safetyEpoch,
			Purpose: operation.purpose, ActivityGeneration: operation.activityGeneration,
			Terminal: operation.terminal,
		})
	}
	for id, operation := range record.publicationOperations {
		state.PublicationOperations = append(state.PublicationOperations, postgresPublicationOperationState{
			OperationID: id.value, PhaseRunID: operation.phaseRunID.value,
			Authority:  postgresAuthorityStateFromAuthority(operation.authority),
			Generation: operation.generation, Fence: operation.fence, SafetyEpoch: operation.safetyEpoch,
			ActivityGeneration: operation.activityGeneration, Terminal: operation.terminal,
		})
	}
	for id, operation := range record.schedulingOperations {
		state.SchedulingOperations = append(state.SchedulingOperations, postgresSchedulingOperationState{
			OperationID: id.value, PhaseRunID: operation.phaseRunID.value,
			Authority:  postgresAuthorityStateFromAuthority(operation.authority),
			Generation: operation.generation, Fence: operation.fence, SafetyEpoch: operation.safetyEpoch,
			ActivityGeneration: operation.activityGeneration, Terminal: operation.terminal,
		})
	}
	for _, enactment := range record.enactments {
		state.Enactments = append(state.Enactments, postgresEnactmentStateFromRef(enactment))
	}
	for id, fence := range record.reconciliationFences {
		state.ReconciliationFences = append(state.ReconciliationFences, postgresReconciliationFenceState{
			OperationID: id.value, Fence: fence,
		})
	}
	for id, gate := range record.gateDecisions {
		state.GateDecisions = append(state.GateDecisions, postgresGateDecisionState{
			DecisionRequestID: id.value, Digest: gate.digest,
			Decision: postgresDecisionStateFromDecision(gate.decision),
		})
	}
	if record.aggregate != nil {
		state.Aggregate = postgresAggregateStateFromAggregate(*record.aggregate)
	}
	state.sort()
	return state
}

func (state postgresTaskState) taskRecord() taskRecord {
	record := taskRecord{
		revision:                 state.Revision,
		activityGeneration:       state.ActivityGeneration,
		recoveryActivityFence:    state.RecoveryActivityFence,
		owner:                    userOwnershipBinding{AuthorityID{state.Owner.AuthorityID}, state.Owner.Generation},
		latestDecision:           state.LatestDecision.decision(),
		decisionCount:            state.DecisionCount,
		enactmentCount:           state.EnactmentCount,
		safetyEpoch:              state.SafetyEpoch,
		phaseRuns:                make(map[PhaseRunID]phaseRunBinding, len(state.PhaseRuns)),
		runtimeOperations:        make(map[OperationID]runtimeOperationBinding, len(state.RuntimeOperations)),
		validationBindings:       make(map[PhaseRunID]validationBinding, len(state.ValidationBindings)),
		lifecycleOperations:      make(map[OperationID]lifecycleOperationBinding, len(state.LifecycleOperations)),
		publicationOperations:    make(map[OperationID]publicationOperationBinding, len(state.PublicationOperations)),
		schedulingOperations:     make(map[OperationID]schedulingOperationBinding, len(state.SchedulingOperations)),
		evidenceDiagnosticCount:  state.EvidenceDiagnosticCount,
		latestEvidenceDiagnostic: state.LatestEvidenceDiagnostic.diagnostic(),
		latestRevisionID:         TaskWorkspaceRevisionID{state.LatestRevisionID},
		latestCheckpointID:       CheckpointID{state.LatestCheckpointID},
		cancellationState:        state.CancellationState,
		enactments:               make(map[OperationID]EnactmentRef, len(state.Enactments)),
		reconciler:               state.Reconciler.authority(),
		reconciliationFences:     make(map[OperationID]ReconciliationFence, len(state.ReconciliationFences)),
		gateDecisions:            make(map[DecisionRequestID]gateDecisionRecord, len(state.GateDecisions)),
	}
	for _, item := range state.PhaseRuns {
		record.phaseRuns[PhaseRunID{item.PhaseRunID}] = phaseRunBinding{
			generation: item.Generation, fence: item.Fence, active: item.Active,
		}
	}
	for _, item := range state.RuntimeOperations {
		record.runtimeOperations[OperationID{item.OperationID}] = runtimeOperationBinding{
			phaseRunID: PhaseRunID{item.PhaseRunID}, runtimeRunID: RuntimeRunID{item.RuntimeRunID},
			authority: item.Authority.authority(), generation: item.Generation, fence: item.Fence,
			safetyEpoch: item.SafetyEpoch, activityGeneration: item.ActivityGeneration,
			terminal: item.Terminal,
		}
	}
	for _, item := range state.ValidationBindings {
		record.validationBindings[PhaseRunID{item.PhaseRunID}] = validationBinding{
			authority: item.Authority.authority(), generation: item.Generation, fence: item.Fence,
			safetyEpoch: item.SafetyEpoch, activityGeneration: item.ActivityGeneration,
			terminal: item.Terminal,
		}
	}
	for _, item := range state.LifecycleOperations {
		record.lifecycleOperations[OperationID{item.OperationID}] = lifecycleOperationBinding{
			phaseRunID: PhaseRunID{item.PhaseRunID}, authority: item.Authority.authority(),
			generation: item.Generation, fence: item.Fence, safetyEpoch: item.SafetyEpoch,
			purpose: item.Purpose, activityGeneration: item.ActivityGeneration, terminal: item.Terminal,
		}
	}
	for _, item := range state.PublicationOperations {
		record.publicationOperations[OperationID{item.OperationID}] = publicationOperationBinding{
			phaseRunID: PhaseRunID{item.PhaseRunID}, authority: item.Authority.authority(),
			generation: item.Generation, fence: item.Fence, safetyEpoch: item.SafetyEpoch,
			activityGeneration: item.ActivityGeneration, terminal: item.Terminal,
		}
	}
	for _, item := range state.SchedulingOperations {
		record.schedulingOperations[OperationID{item.OperationID}] = schedulingOperationBinding{
			phaseRunID: PhaseRunID{item.PhaseRunID}, authority: item.Authority.authority(),
			generation: item.Generation, fence: item.Fence, safetyEpoch: item.SafetyEpoch,
			activityGeneration: item.ActivityGeneration, terminal: item.Terminal,
		}
	}
	for _, item := range state.Enactments {
		ref := item.enactment()
		record.enactments[ref.OperationID] = ref
	}
	for _, item := range state.ReconciliationFences {
		record.reconciliationFences[OperationID{item.OperationID}] = item.Fence
	}
	for _, item := range state.GateDecisions {
		record.gateDecisions[DecisionRequestID{item.DecisionRequestID}] = gateDecisionRecord{
			digest: item.Digest, decision: item.Decision.decision(),
		}
	}
	if state.Aggregate != nil {
		record.aggregate = state.Aggregate.aggregate()
	}
	return record
}

func postgresDecisionStateFromDecision(decision TransitionDecision) postgresDecisionState {
	state := postgresDecisionState{
		DecisionID: decision.DecisionID.value, DecisionRequestID: decision.DecisionRequestID.value,
		CanonicalRequestDigest: decision.CanonicalRequestDigest,
		PreviousTaskRevision:   decision.PreviousTaskRevision,
		AcceptedTaskRevision:   decision.AcceptedTaskRevision,
		TaskProjection:         postgresTaskProjectionStateFromProjection(decision.TaskProjection),
		CommittedAt:            decision.CommittedAt.UTC().Format(canonicalTimeFormat),
		MandatoryAuditFact:     postgresAuditFactStateFromFact(decision.MandatoryAuditFactRef),
	}
	for _, id := range decision.AffectedPhaseRuns {
		state.AffectedPhaseRuns = append(state.AffectedPhaseRuns, id.value)
	}
	for _, evidence := range decision.AcceptedEvidenceRefs {
		state.AcceptedEvidenceRefs = append(state.AcceptedEvidenceRefs, postgresEvidenceRefState{
			ID: evidence.ID.value, Kind: evidence.Kind, Digest: evidence.Digest,
		})
	}
	for _, enactment := range decision.EnactmentRefs {
		state.EnactmentRefs = append(state.EnactmentRefs, postgresEnactmentStateFromRef(enactment))
	}
	return state
}

func (state postgresDecisionState) decision() TransitionDecision {
	decision := TransitionDecision{
		DecisionID: DecisionID{state.DecisionID}, DecisionRequestID: DecisionRequestID{state.DecisionRequestID},
		CanonicalRequestDigest: state.CanonicalRequestDigest,
		PreviousTaskRevision:   state.PreviousTaskRevision,
		AcceptedTaskRevision:   state.AcceptedTaskRevision,
		TaskProjection:         state.TaskProjection.projection(),
		CommittedAt:            parsePostgresStateTime(state.CommittedAt),
		MandatoryAuditFactRef:  state.MandatoryAuditFact.fact(),
	}
	for _, id := range state.AffectedPhaseRuns {
		decision.AffectedPhaseRuns = append(decision.AffectedPhaseRuns, PhaseRunID{id})
	}
	for _, evidence := range state.AcceptedEvidenceRefs {
		decision.AcceptedEvidenceRefs = append(decision.AcceptedEvidenceRefs, EvidenceRef{
			ID: EvidenceID{evidence.ID}, Kind: evidence.Kind, Digest: evidence.Digest,
		})
	}
	for _, enactment := range state.EnactmentRefs {
		decision.EnactmentRefs = append(decision.EnactmentRefs, enactment.enactment())
	}
	return decision
}

func postgresAuditFactStateFromFact(fact AuditFactRef) postgresAuditFactState {
	state := postgresAuditFactState{
		SchemaVersion: fact.SchemaVersion, AuditFactID: fact.AuditFactID.value,
		CanonicalDigest: fact.CanonicalDigest, IntegrityVersion: fact.IntegrityVersion,
		OwningModule: fact.OwningModule, DecisionID: fact.DecisionID.value, TaskID: fact.TaskID.value,
		IdempotencyDecisionRequestID: fact.IdempotencyDecisionRequestID.value,
		Action:                       fact.Action, Result: fact.Result, AuthorityKind: fact.AuthorityKind,
		AuthorityID: fact.AuthorityID.value, AuthorizationGeneration: fact.AuthorizationGeneration,
		AuthorityReason: fact.AuthorityReason, ReasonCode: fact.ReasonCode,
		BeforeTaskRevision: fact.BeforeTaskRevision, AfterTaskRevision: fact.AfterTaskRevision,
		BeforeStatus: fact.BeforeStatus, AfterStatus: fact.AfterStatus,
		BeforeActivityGeneration: fact.BeforeActivityGeneration,
		AfterActivityGeneration:  fact.AfterActivityGeneration,
		RecoveryGeneration:       fact.RecoveryGeneration, BeforeSafetyEpoch: fact.BeforeSafetyEpoch,
		AfterSafetyEpoch: fact.AfterSafetyEpoch, RetryPhaseRunID: fact.RetryPhaseRunID.value,
		RetryRuntimeRunID:         fact.RetryRuntimeRunID.value,
		ReconciliationOperationID: fact.ReconciliationOperationID.value,
		ReconciliationFence:       fact.ReconciliationFence,
		OccurredAt:                fact.OccurredAt.UTC().Format(canonicalTimeFormat),
		RecordedAt:                fact.RecordedAt.UTC().Format(canonicalTimeFormat), SourceClock: fact.SourceClock,
	}
	for _, evidence := range fact.EvidenceRefs {
		state.EvidenceRefs = append(state.EvidenceRefs, postgresEvidenceRefState{
			ID: evidence.ID.value, Kind: evidence.Kind, Digest: evidence.Digest,
		})
	}
	for _, enactment := range fact.EnactmentRefs {
		state.EnactmentRefs = append(state.EnactmentRefs, postgresAuditEnactmentState{
			OperationID: enactment.OperationID.value, Kind: enactment.Kind,
			PayloadDigest: enactment.PayloadDigest, ActivityGeneration: enactment.ActivityGeneration,
			FenceKind: enactment.FenceKind, Fence: enactment.Fence,
			CausationID: enactment.CausationID.value,
		})
	}
	return state
}

func (state postgresAuditFactState) fact() AuditFactRef {
	fact := AuditFactRef{
		SchemaVersion: state.SchemaVersion, AuditFactID: AuditFactID{state.AuditFactID},
		CanonicalDigest: state.CanonicalDigest, IntegrityVersion: state.IntegrityVersion,
		OwningModule: state.OwningModule, DecisionID: DecisionID{state.DecisionID},
		TaskID:                       TaskID{state.TaskID},
		IdempotencyDecisionRequestID: DecisionRequestID{state.IdempotencyDecisionRequestID},
		Action:                       state.Action, Result: state.Result, AuthorityKind: state.AuthorityKind,
		AuthorityID: AuthorityID{state.AuthorityID}, AuthorizationGeneration: state.AuthorizationGeneration,
		AuthorityReason: state.AuthorityReason, ReasonCode: state.ReasonCode,
		BeforeTaskRevision: state.BeforeTaskRevision, AfterTaskRevision: state.AfterTaskRevision,
		BeforeStatus: state.BeforeStatus, AfterStatus: state.AfterStatus,
		BeforeActivityGeneration: state.BeforeActivityGeneration,
		AfterActivityGeneration:  state.AfterActivityGeneration,
		RecoveryGeneration:       state.RecoveryGeneration, BeforeSafetyEpoch: state.BeforeSafetyEpoch,
		AfterSafetyEpoch: state.AfterSafetyEpoch, RetryPhaseRunID: PhaseRunID{state.RetryPhaseRunID},
		RetryRuntimeRunID:         RuntimeRunID{state.RetryRuntimeRunID},
		ReconciliationOperationID: OperationID{state.ReconciliationOperationID},
		ReconciliationFence:       state.ReconciliationFence,
		OccurredAt:                parsePostgresStateTime(state.OccurredAt),
		RecordedAt:                parsePostgresStateTime(state.RecordedAt), SourceClock: state.SourceClock,
	}
	for _, evidence := range state.EvidenceRefs {
		fact.EvidenceRefs = append(fact.EvidenceRefs, EvidenceRef{
			ID: EvidenceID{evidence.ID}, Kind: evidence.Kind, Digest: evidence.Digest,
		})
	}
	for _, enactment := range state.EnactmentRefs {
		fact.EnactmentRefs = append(fact.EnactmentRefs, AuditEnactmentBinding{
			OperationID: OperationID{enactment.OperationID}, Kind: enactment.Kind,
			PayloadDigest: enactment.PayloadDigest, ActivityGeneration: enactment.ActivityGeneration,
			FenceKind: enactment.FenceKind, Fence: enactment.Fence,
			CausationID: CausationID{enactment.CausationID},
		})
	}
	return fact
}

func postgresTaskProjectionStateFromProjection(projection TaskProjection) postgresTaskProjectionState {
	return postgresTaskProjectionState{
		TaskID: projection.TaskID.value, TaskRevision: projection.TaskRevision,
		ActivityGeneration: projection.ActivityGeneration, Status: projection.Status,
		Route: projection.Route, Activity: projection.Activity, CurrentPhase: projection.CurrentPhase.value,
		ExecutionLockID:                  projection.ExecutionLockID.value,
		TemplateLockID:                   projection.TemplateLockID.value,
		ActivePhaseRunID:                 projection.ActivePhaseRunID.value,
		LatestArtifactVersionID:          projection.LatestArtifactVersionID.value,
		TaskWorkspaceID:                  projection.TaskWorkspaceID.value,
		LatestRevisionID:                 projection.LatestRevisionID.value,
		LatestCheckpointID:               projection.LatestCheckpointID.value,
		TaskWorkspaceLifecycleGeneration: projection.TaskWorkspaceLifecycleGeneration,
		TaskWorkspaceLifecycleFence:      projection.TaskWorkspaceLifecycleFence,
		CancellationState:                projection.CancellationState, SafetyEpoch: projection.SafetyEpoch,
		OperationalMode: projection.OperationalMode,
	}
}

func (state postgresTaskProjectionState) projection() TaskProjection {
	return TaskProjection{
		TaskID: TaskID{state.TaskID}, TaskRevision: state.TaskRevision,
		ActivityGeneration: state.ActivityGeneration, Status: state.Status, Route: state.Route,
		Activity: state.Activity, CurrentPhase: PhaseKey{state.CurrentPhase},
		ExecutionLockID:                  ExecutionLockID{state.ExecutionLockID},
		TemplateLockID:                   TemplateLockID{state.TemplateLockID},
		ActivePhaseRunID:                 PhaseRunID{state.ActivePhaseRunID},
		LatestArtifactVersionID:          ArtifactVersionID{state.LatestArtifactVersionID},
		TaskWorkspaceID:                  TaskWorkspaceID{state.TaskWorkspaceID},
		LatestRevisionID:                 TaskWorkspaceRevisionID{state.LatestRevisionID},
		LatestCheckpointID:               CheckpointID{state.LatestCheckpointID},
		TaskWorkspaceLifecycleGeneration: state.TaskWorkspaceLifecycleGeneration,
		TaskWorkspaceLifecycleFence:      state.TaskWorkspaceLifecycleFence,
		CancellationState:                state.CancellationState, SafetyEpoch: state.SafetyEpoch,
		OperationalMode: state.OperationalMode,
	}
}

func postgresEnactmentStateFromRef(ref EnactmentRef) postgresEnactmentState {
	fenceKind, fence := postgresFenceValue(ref.Fence)
	return postgresEnactmentState{
		OperationID: ref.OperationID.value, Kind: ref.Kind, PayloadDigest: ref.PayloadDigest,
		ActivityGeneration: ref.ActivityGeneration, FenceKind: fenceKind, Fence: fence,
		CausationID: ref.CausationID.value,
	}
}

func (state postgresEnactmentState) enactment() EnactmentRef {
	return EnactmentRef{
		OperationID: OperationID{state.OperationID}, Kind: state.Kind, PayloadDigest: state.PayloadDigest,
		ActivityGeneration: state.ActivityGeneration, Fence: postgresFenceRef(state.FenceKind, state.Fence),
		CausationID: CausationID{state.CausationID},
	}
}

func postgresFenceValue(ref EnactmentFenceRef) (EnactmentFenceKind, uint64) {
	switch fence := ref.(type) {
	case RuntimeFence:
		return EnactmentFenceRuntimeExecution, uint64(fence)
	case TaskWorkspaceLifecycleFence:
		return EnactmentFenceTaskWorkspaceLifecycle, uint64(fence)
	case PublicationFence:
		return EnactmentFenceArtifactPublication, uint64(fence)
	case SchedulerFence:
		return EnactmentFenceScheduling, uint64(fence)
	case UsageFence:
		return EnactmentFenceUsageAccounting, uint64(fence)
	case ConfirmationFence:
		return EnactmentFenceConfirmation, uint64(fence)
	default:
		return 0, 0
	}
}

func postgresFenceRef(kind EnactmentFenceKind, value uint64) EnactmentFenceRef {
	switch kind {
	case EnactmentFenceRuntimeExecution:
		return RuntimeFence(value)
	case EnactmentFenceTaskWorkspaceLifecycle:
		return TaskWorkspaceLifecycleFence(value)
	case EnactmentFenceArtifactPublication:
		return PublicationFence(value)
	case EnactmentFenceScheduling:
		return SchedulerFence(value)
	case EnactmentFenceUsageAccounting:
		return UsageFence(value)
	case EnactmentFenceConfirmation:
		return ConfirmationFence(value)
	default:
		return nil
	}
}

func postgresAggregateStateFromAggregate(aggregate taskAggregate) *postgresAggregateState {
	state := &postgresAggregateState{
		Status: aggregate.status, Route: aggregate.route, Activity: aggregate.activity,
		Pinned: postgresPinnedTaskStateFromPinned(aggregate.pinned), CurrentPhase: aggregate.currentPhase.value,
		ActivePhaseRunID:                aggregate.activePhaseRunID.value,
		LatestArtifactVersionID:         aggregate.latestArtifactVersionID.value,
		ActivitySourceArtifactVersionID: aggregate.activitySourceArtifactVersionID.value,
		ReconstructionRequired:          aggregate.reconstructionRequired,
		ReconstructionAccepted:          aggregate.reconstructionAccepted,
		ReconstructionOperationID:       aggregate.reconstructionOperationID.value,
		LifecycleGeneration:             aggregate.lifecycleGeneration,
		LifecycleFence:                  aggregate.lifecycleFence,
	}
	for _, run := range aggregate.phaseRuns {
		item := postgresPhaseRunState{
			ID: run.id.value, PhaseKey: run.phaseKey.value, Attempt: run.attempt,
			Generation: run.generation, Fence: run.fence, Outcome: run.outcome,
			ValidationOutcome: run.validationOutcome, LifecycleOutcome: run.lifecycleOutcome,
			RevisionID: run.revisionID.value, CheckpointID: run.checkpointID.value,
			LifecycleOperationID: run.lifecycleOperationID.value,
			PublicationOutcome:   run.publicationOutcome, ArtifactVersionID: run.artifactVersionID.value,
			PublicationOperationID: run.publicationOperationID.value,
			TaskWorkspaceID:        run.taskWorkspaceID.value,
			InputArtifactVersionID: run.inputArtifactVersionID.value,
		}
		for _, runtimeRun := range run.runtimeRuns {
			item.RuntimeRuns = append(item.RuntimeRuns, postgresRuntimeRunState{
				ID: runtimeRun.id.value, OperationID: runtimeRun.operationID.value, Outcome: runtimeRun.outcome,
			})
		}
		state.PhaseRuns = append(state.PhaseRuns, item)
	}
	return state
}

func (state postgresAggregateState) aggregate() *taskAggregate {
	aggregate := &taskAggregate{
		status: state.Status, route: state.Route, activity: state.Activity,
		pinned: state.Pinned.pinned(), currentPhase: PhaseKey{state.CurrentPhase},
		activePhaseRunID:                PhaseRunID{state.ActivePhaseRunID},
		latestArtifactVersionID:         ArtifactVersionID{state.LatestArtifactVersionID},
		activitySourceArtifactVersionID: ArtifactVersionID{state.ActivitySourceArtifactVersionID},
		reconstructionRequired:          state.ReconstructionRequired,
		reconstructionAccepted:          state.ReconstructionAccepted,
		reconstructionOperationID:       OperationID{state.ReconstructionOperationID},
		lifecycleGeneration:             state.LifecycleGeneration,
		lifecycleFence:                  state.LifecycleFence,
	}
	for _, item := range state.PhaseRuns {
		run := phaseRunRecord{
			id: PhaseRunID{item.ID}, phaseKey: PhaseKey{item.PhaseKey}, attempt: item.Attempt,
			generation: item.Generation, fence: item.Fence, outcome: item.Outcome,
			validationOutcome: item.ValidationOutcome, lifecycleOutcome: item.LifecycleOutcome,
			revisionID: TaskWorkspaceRevisionID{item.RevisionID}, checkpointID: CheckpointID{item.CheckpointID},
			lifecycleOperationID:   OperationID{item.LifecycleOperationID},
			publicationOutcome:     item.PublicationOutcome,
			artifactVersionID:      ArtifactVersionID{item.ArtifactVersionID},
			publicationOperationID: OperationID{item.PublicationOperationID},
			taskWorkspaceID:        TaskWorkspaceID{item.TaskWorkspaceID},
			inputArtifactVersionID: ArtifactVersionID{item.InputArtifactVersionID},
		}
		for _, runtimeRun := range item.RuntimeRuns {
			run.runtimeRuns = append(run.runtimeRuns, runtimeRunRecord{
				id: RuntimeRunID{runtimeRun.ID}, operationID: OperationID{runtimeRun.OperationID},
				outcome: runtimeRun.Outcome,
			})
		}
		aggregate.phaseRuns = append(aggregate.phaseRuns, run)
	}
	return aggregate
}

func postgresPinnedTaskStateFromPinned(pinned PinnedTaskStart) postgresPinnedTaskState {
	return postgresPinnedTaskState{
		Route: pinned.Route, TaskWorkspaceID: pinned.TaskWorkspaceID.value,
		ExecutionLock: postgresExecutionLockState{
			ID:                      pinned.ExecutionLock.ID.value,
			PipelineVersionID:       pinned.ExecutionLock.PipelineVersionID.value,
			RuntimeReleaseID:        pinned.ExecutionLock.RuntimeReleaseID.value,
			CompatibilityApprovalID: pinned.ExecutionLock.CompatibilityApprovalID.value,
			PipelineContract:        postgresPipelineContractStateFromContract(pinned.ExecutionLock.PipelineContract),
		},
		TemplateLockID: pinned.TemplateLockID.value,
		Authorities: postgresDownstreamAuthorityBindingsState{
			Runtime:   postgresAuthorityStateFromAuthority(pinned.Authorities.Runtime.value),
			Validator: postgresAuthorityStateFromAuthority(pinned.Authorities.Validator.value),
			TaskWorkspaceLifecycle: postgresAuthorityStateFromAuthority(
				pinned.Authorities.TaskWorkspaceLifecycle.value,
			),
			Publication: postgresAuthorityStateFromAuthority(pinned.Authorities.Publication.value),
			Scheduler:   postgresAuthorityStateFromAuthority(pinned.Authorities.Scheduler.value),
		},
	}
}

func (state postgresPinnedTaskState) pinned() PinnedTaskStart {
	return PinnedTaskStart{
		Route: state.Route, TaskWorkspaceID: TaskWorkspaceID{state.TaskWorkspaceID},
		ExecutionLock: ExecutionLock{
			ID:                      ExecutionLockID{state.ExecutionLock.ID},
			PipelineVersionID:       PipelineVersionID{state.ExecutionLock.PipelineVersionID},
			RuntimeReleaseID:        RuntimeReleaseID{state.ExecutionLock.RuntimeReleaseID},
			CompatibilityApprovalID: CompatibilityApprovalID{state.ExecutionLock.CompatibilityApprovalID},
			PipelineContract:        state.ExecutionLock.PipelineContract.contract(),
		},
		TemplateLockID: TemplateLockID{state.TemplateLockID},
		Authorities: DownstreamAuthorityBindings{
			Runtime:   RuntimeAuthority{value: state.Authorities.Runtime.authority()},
			Validator: ValidatorAuthority{value: state.Authorities.Validator.authority()},
			TaskWorkspaceLifecycle: TaskWorkspaceLifecycleAuthority{
				value: state.Authorities.TaskWorkspaceLifecycle.authority(),
			},
			Publication: PublicationAuthority{value: state.Authorities.Publication.authority()},
			Scheduler:   SchedulerAuthority{value: state.Authorities.Scheduler.authority()},
		},
	}
}

func postgresPipelineContractStateFromContract(contract PipelineContract) postgresPipelineContractState {
	state := postgresPipelineContractState{
		SchemaVersion: contract.SchemaVersion, PipelineVersionID: contract.PipelineVersionID.value,
		ManualEditEntryPhase: contract.ManualEditEntryPhase.value,
	}
	for _, route := range contract.Routes {
		item := postgresRouteDefinitionState{Route: route.Route, EntryPhase: route.EntryPhase.value}
		for _, phase := range route.Phases {
			item.Phases = append(item.Phases, postgresPhaseDefinitionStateFromDefinition(phase))
		}
		state.Routes = append(state.Routes, item)
	}
	for _, phase := range contract.ManualEditPhases {
		state.ManualEditPhases = append(state.ManualEditPhases, postgresPhaseDefinitionStateFromDefinition(phase))
	}
	return state
}

func (state postgresPipelineContractState) contract() PipelineContract {
	contract := PipelineContract{
		SchemaVersion: state.SchemaVersion, PipelineVersionID: PipelineVersionID{state.PipelineVersionID},
		ManualEditEntryPhase: PhaseKey{state.ManualEditEntryPhase},
	}
	for _, route := range state.Routes {
		item := RouteDefinition{Route: route.Route, EntryPhase: PhaseKey{route.EntryPhase}}
		for _, phase := range route.Phases {
			item.Phases = append(item.Phases, phase.definition())
		}
		contract.Routes = append(contract.Routes, item)
	}
	for _, phase := range state.ManualEditPhases {
		contract.ManualEditPhases = append(contract.ManualEditPhases, phase.definition())
	}
	return contract
}

func postgresPhaseDefinitionStateFromDefinition(phase PhaseDefinition) postgresPhaseDefinitionState {
	return postgresPhaseDefinitionState{
		Key: phase.Key.value, Kind: phase.Kind, ValidationContract: phase.ValidationContract,
		RequiredRuntimeRuns: phase.RequiredRuntimeRuns, RetryEligible: phase.RetryEligible,
		GateID: phase.GateID.value, NextPhase: phase.NextPhase.value,
	}
}

func (state postgresPhaseDefinitionState) definition() PhaseDefinition {
	return PhaseDefinition{
		Key: PhaseKey{state.Key}, Kind: state.Kind, ValidationContract: state.ValidationContract,
		RequiredRuntimeRuns: state.RequiredRuntimeRuns, RetryEligible: state.RetryEligible,
		GateID: GateID{state.GateID}, NextPhase: PhaseKey{state.NextPhase},
	}
}

func postgresAuthorityStateFromAuthority(authority authorityValue) postgresAuthorityState {
	return postgresAuthorityState{
		Kind: authority.kind, ID: authority.id.value, Generation: authority.generation, Reason: authority.reason,
	}
}

func (state postgresAuthorityState) authority() authorityValue {
	return authorityValue{
		kind: state.Kind, id: AuthorityID{state.ID}, generation: state.Generation, reason: state.Reason,
	}
}

func postgresEvidenceDiagnosticStateFromDiagnostic(value EvidenceDiagnostic) postgresEvidenceDiagnosticState {
	return postgresEvidenceDiagnosticState{
		EvidenceID: value.EvidenceID.value, Disposition: value.Disposition, Reason: value.Reason,
	}
}

func (state postgresEvidenceDiagnosticState) diagnostic() EvidenceDiagnostic {
	return EvidenceDiagnostic{
		EvidenceID: EvidenceID{state.EvidenceID}, Disposition: state.Disposition, Reason: state.Reason,
	}
}

func parsePostgresStateTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, _ := parseCanonicalTime(value)
	return parsed
}

func parseCanonicalTime(value string) (time.Time, error) {
	return time.Parse(canonicalTimeFormat, value)
}

func (state *postgresTaskState) sort() {
	sort.Slice(state.PhaseRuns, func(i, j int) bool { return state.PhaseRuns[i].PhaseRunID < state.PhaseRuns[j].PhaseRunID })
	sort.Slice(state.RuntimeOperations, func(i, j int) bool {
		return state.RuntimeOperations[i].OperationID < state.RuntimeOperations[j].OperationID
	})
	sort.Slice(state.ValidationBindings, func(i, j int) bool {
		return state.ValidationBindings[i].PhaseRunID < state.ValidationBindings[j].PhaseRunID
	})
	sort.Slice(state.LifecycleOperations, func(i, j int) bool {
		return state.LifecycleOperations[i].OperationID < state.LifecycleOperations[j].OperationID
	})
	sort.Slice(state.PublicationOperations, func(i, j int) bool {
		return state.PublicationOperations[i].OperationID < state.PublicationOperations[j].OperationID
	})
	sort.Slice(state.SchedulingOperations, func(i, j int) bool {
		return state.SchedulingOperations[i].OperationID < state.SchedulingOperations[j].OperationID
	})
	sort.Slice(state.Enactments, func(i, j int) bool { return state.Enactments[i].OperationID < state.Enactments[j].OperationID })
	sort.Slice(state.ReconciliationFences, func(i, j int) bool {
		return state.ReconciliationFences[i].OperationID < state.ReconciliationFences[j].OperationID
	})
	sort.Slice(state.GateDecisions, func(i, j int) bool {
		return state.GateDecisions[i].DecisionRequestID < state.GateDecisions[j].DecisionRequestID
	})
}
