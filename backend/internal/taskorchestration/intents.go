package taskorchestration

type AdministratorReason uint8

const (
	AdministratorReasonSafety AdministratorReason = iota + 1
	AdministratorReasonRecovery
)

type CancelReason uint8

const (
	CancelReasonUserRequested CancelReason = iota + 1
	CancelReasonSafety
)

type OperationalMode uint8

const (
	OperationalReadOnly OperationalMode = iota + 1
	OperationalFullReady
)

type authorityValue struct {
	kind       AuthorityKind
	id         AuthorityID
	generation AuthorizationGeneration
	reason     AdministratorReason
}

func (authority authorityValue) canonical() map[string]any {
	encoded := map[string]any{
		"generation": uint64(authority.generation),
		"id":         authority.id.value,
		"kind":       authorityKindName(authority.kind),
	}
	if authority.kind == AuthorityAdministrator {
		encoded["reason"] = administratorReasonName(authority.reason)
	}
	return encoded
}

func (authority authorityValue) valid() bool {
	if !validOpaqueID(authority.id.value) || authority.generation == 0 ||
		authorityKindName(authority.kind) == "" {
		return false
	}
	if authority.kind == AuthorityAdministrator {
		return administratorReasonName(authority.reason) != ""
	}
	return authority.reason == 0
}

// Each authority wrapper is a distinct type. Intent constructors accept one
// concrete wrapper and therefore cannot combine human and machine authority.
type UserAuthority struct{ value authorityValue }
type UserQueryAuthority struct{ value authorityValue }
type AdministratorAuthority struct{ value authorityValue }
type WorkerAuthority struct{ value authorityValue }
type RuntimeAuthority struct{ value authorityValue }
type ValidatorAuthority struct{ value authorityValue }
type TaskWorkspaceLifecycleAuthority struct{ value authorityValue }
type PublicationAuthority struct{ value authorityValue }
type SchedulerAuthority struct{ value authorityValue }
type RecoveryAuthority struct{ value authorityValue }

func NewUserAuthority(id AuthorityID, generation AuthorizationGeneration) UserAuthority {
	return UserAuthority{value: newAuthorityValue(AuthorityUser, id, generation)}
}

// NewUserQueryAuthority derives the read-only query scope from a User
// authority without granting mutation authority to the query seam.
func NewUserQueryAuthority(authority UserAuthority) UserQueryAuthority {
	return UserQueryAuthority{value: authority.value}
}

func NewAdministratorAuthority(
	id AuthorityID,
	generation AuthorizationGeneration,
	reason AdministratorReason,
) AdministratorAuthority {
	value := newAuthorityValue(AuthorityAdministrator, id, generation)
	value.reason = reason
	return AdministratorAuthority{value: value}
}

func NewWorkerAuthority(id AuthorityID, generation AuthorizationGeneration) WorkerAuthority {
	return WorkerAuthority{value: newAuthorityValue(AuthorityWorker, id, generation)}
}

func NewRuntimeAuthority(id AuthorityID, generation AuthorizationGeneration) RuntimeAuthority {
	return RuntimeAuthority{value: newAuthorityValue(AuthorityRuntime, id, generation)}
}

func NewValidatorAuthority(id AuthorityID, generation AuthorizationGeneration) ValidatorAuthority {
	return ValidatorAuthority{value: newAuthorityValue(AuthorityValidator, id, generation)}
}

func NewTaskWorkspaceLifecycleAuthority(
	id AuthorityID,
	generation AuthorizationGeneration,
) TaskWorkspaceLifecycleAuthority {
	return TaskWorkspaceLifecycleAuthority{
		value: newAuthorityValue(AuthorityTaskWorkspaceLifecycle, id, generation),
	}
}

func NewPublicationAuthority(id AuthorityID, generation AuthorizationGeneration) PublicationAuthority {
	return PublicationAuthority{value: newAuthorityValue(AuthorityPublication, id, generation)}
}

func NewSchedulerAuthority(id AuthorityID, generation AuthorizationGeneration) SchedulerAuthority {
	return SchedulerAuthority{value: newAuthorityValue(AuthorityScheduler, id, generation)}
}

func NewRecoveryAuthority(id AuthorityID, generation AuthorizationGeneration) RecoveryAuthority {
	return RecoveryAuthority{value: newAuthorityValue(AuthorityRecovery, id, generation)}
}

func newAuthorityValue(
	kind AuthorityKind,
	id AuthorityID,
	generation AuthorizationGeneration,
) authorityValue {
	return authorityValue{kind: kind, id: id, generation: generation}
}

type intentPayload interface {
	canonical() map[string]any
	valid() bool
}

type intentValue struct {
	header    IntentHeader
	kind      IntentKind
	authority authorityValue
	payload   intentPayload
}

func (intent intentValue) Header() IntentHeader               { return intent.header }
func (intent intentValue) Kind() IntentKind                   { return intent.kind }
func (intent intentValue) AuthorityKind() AuthorityKind       { return intent.authority.kind }
func (intent intentValue) canonicalAuthority() map[string]any { return intent.authority.canonical() }
func (intent intentValue) canonicalPayload() map[string]any   { return intent.payload.canonical() }
func (intentValue) transitionIntent()                         {}

type emptyIntentPayload struct{}

func (emptyIntentPayload) canonical() map[string]any { return map[string]any{} }
func (emptyIntentPayload) valid() bool               { return true }

func NewStartTaskIntent(header IntentHeader, authority UserAuthority) TransitionIntent {
	return newIntent(header, IntentStartTask, authority.value, emptyIntentPayload{})
}

type pinnedTaskStartPayload struct {
	pinned                      PinnedTaskStart
	verifiedTask                TaskID
	safetyEpoch                 SafetyEpoch
	executionVerificationDigest EvidenceDigest
	templateVerificationDigest  EvidenceDigest
}

func (payload pinnedTaskStartPayload) canonical() map[string]any {
	return map[string]any{
		"admission": map[string]any{
			"execution_verification_digest": payload.executionVerificationDigest.String(),
			"safety_epoch":                  uint64(payload.safetyEpoch),
			"task_id":                       payload.verifiedTask.value,
			"template_verification_digest":  payload.templateVerificationDigest.String(),
		},
		"authorities": payload.pinned.Authorities.canonical(),
		"execution_lock": map[string]any{
			"compatibility_approval_id": payload.pinned.ExecutionLock.CompatibilityApprovalID.value,
			"id":                        payload.pinned.ExecutionLock.ID.value,
			"pipeline_contract":         payload.pinned.ExecutionLock.PipelineContract.canonical(),
			"pipeline_version_id":       payload.pinned.ExecutionLock.PipelineVersionID.value,
			"runtime_release_id":        payload.pinned.ExecutionLock.RuntimeReleaseID.value,
		},
		"route":             payload.pinned.Route.String(),
		"task_workspace_id": payload.pinned.TaskWorkspaceID.value,
		"template_lock_id":  payload.pinned.TemplateLockID.value,
	}
}

func (payload pinnedTaskStartPayload) valid() bool {
	if !payload.pinned.valid() || !validOpaqueID(payload.verifiedTask.value) ||
		payload.safetyEpoch == 0 || payload.executionVerificationDigest == (EvidenceDigest{}) {
		return false
	}
	if payload.pinned.Route == RouteGeneration {
		return payload.templateVerificationDigest != (EvidenceDigest{})
	}
	return payload.templateVerificationDigest == (EvidenceDigest{})
}

func NewStartPinnedTaskIntent(
	header IntentHeader,
	authority UserAuthority,
	admission TaskStartAdmission,
) TransitionIntent {
	if !admission.validFor(header.TaskID) {
		return newIntent(header, IntentStartTask, authority.value, pinnedTaskStartPayload{})
	}
	return newIntent(
		header, IntentStartTask, authority.value,
		pinnedTaskStartPayload{
			pinned: clonePinnedTaskStart(admission.pinned), verifiedTask: admission.taskID,
			safetyEpoch:                 admission.safetyEpoch,
			executionVerificationDigest: admission.executionVerificationDigest,
			templateVerificationDigest:  admission.templateVerificationDigest,
		},
	)
}

type operationPayload struct{ operationID OperationID }

func (payload operationPayload) canonical() map[string]any {
	return map[string]any{"operation_id": payload.operationID.value}
}
func (payload operationPayload) valid() bool { return validOpaqueID(payload.operationID.value) }

func NewMakeWorkAvailableIntent(
	header IntentHeader,
	authority WorkerAuthority,
	operationID OperationID,
) TransitionIntent {
	return newIntent(header, IntentMakeWorkAvailable, authority.value, operationPayload{operationID})
}

type confirmationPayload struct {
	gateID        GateID
	payloadDigest PayloadDigest
}

func (payload confirmationPayload) canonical() map[string]any {
	return map[string]any{
		"gate_id":        payload.gateID.value,
		"payload_digest": payload.payloadDigest.String(),
	}
}
func (payload confirmationPayload) valid() bool {
	return validOpaqueID(payload.gateID.value) && payload.payloadDigest != (PayloadDigest{})
}

func NewSubmitConfirmationGateIntent(
	header IntentHeader,
	authority UserAuthority,
	gateID GateID,
	payloadDigest PayloadDigest,
) TransitionIntent {
	return newIntent(header, IntentSubmitConfirmationGate, authority.value, confirmationPayload{
		gateID: gateID, payloadDigest: payloadDigest,
	})
}

type phaseRunPayload struct{ phaseRunID PhaseRunID }

func (payload phaseRunPayload) canonical() map[string]any {
	return map[string]any{"phase_run_id": payload.phaseRunID.value}
}
func (payload phaseRunPayload) valid() bool { return validOpaqueID(payload.phaseRunID.value) }

func NewRetryPhaseIntent(
	header IntentHeader,
	authority UserAuthority,
	phaseRunID PhaseRunID,
) TransitionIntent {
	return newIntent(header, IntentRetryPhase, authority.value, phaseRunPayload{phaseRunID})
}

type runtimeRunRetryPayload struct {
	phaseRunID   PhaseRunID
	runtimeRunID RuntimeRunID
}

func (payload runtimeRunRetryPayload) canonical() map[string]any {
	return map[string]any{
		"phase_run_id": payload.phaseRunID.value, "runtime_run_id": payload.runtimeRunID.value,
	}
}

func (payload runtimeRunRetryPayload) valid() bool {
	return validOpaqueID(payload.phaseRunID.value) && validOpaqueID(payload.runtimeRunID.value)
}

// NewRetryRuntimeRunIntent requests a new execution attempt inside the same
// Phase Run. It is distinct from business Phase retry and delivery replay.
func NewRetryRuntimeRunIntent(
	header IntentHeader,
	authority WorkerAuthority,
	phaseRunID PhaseRunID,
	runtimeRunID RuntimeRunID,
) TransitionIntent {
	return newIntent(
		header, IntentRetryRuntimeRun, authority.value,
		runtimeRunRetryPayload{phaseRunID: phaseRunID, runtimeRunID: runtimeRunID},
	)
}

type cancelPayload struct {
	reason         CancelReason
	lifecycleFence TaskWorkspaceLifecycleFenceRequestBinding
}

func (payload cancelPayload) canonical() map[string]any {
	canonical := map[string]any{"reason": cancelReasonName(payload.reason)}
	if payload.lifecycleFence.valid() {
		canonical["task_workspace_lifecycle_fence"] = payload.lifecycleFence.canonical()
	}
	return canonical
}
func (payload cancelPayload) valid() bool {
	return cancelReasonName(payload.reason) != "" &&
		(payload.lifecycleFence == (TaskWorkspaceLifecycleFenceRequestBinding{}) ||
			payload.lifecycleFence.valid())
}

func NewCancelTaskByUserIntent(
	header IntentHeader,
	authority UserAuthority,
	reason CancelReason,
) TransitionIntent {
	return newIntent(
		header, IntentCancelTask, authority.value,
		cancelPayload{reason: reason},
	)
}

func NewCancelTaskByUserWithLifecycleFenceIntent(
	header IntentHeader,
	authority UserAuthority,
	reason CancelReason,
	binding TaskWorkspaceLifecycleFenceRequestBinding,
) TransitionIntent {
	return newIntent(
		header, IntentCancelTask, authority.value,
		cancelPayload{reason: reason, lifecycleFence: binding},
	)
}

func NewCancelTaskByAdministratorIntent(
	header IntentHeader,
	authority AdministratorAuthority,
	reason CancelReason,
) TransitionIntent {
	return newIntent(
		header, IntentCancelTask, authority.value,
		cancelPayload{reason: reason},
	)
}

func NewCancelTaskByAdministratorWithLifecycleFenceIntent(
	header IntentHeader,
	authority AdministratorAuthority,
	reason CancelReason,
	binding TaskWorkspaceLifecycleFenceRequestBinding,
) TransitionIntent {
	return newIntent(
		header, IntentCancelTask, authority.value,
		cancelPayload{reason: reason, lifecycleFence: binding},
	)
}

type manualEditPayload struct {
	artifactVersionID      ArtifactVersionID
	reconstructionRequired bool
	reconstructionRequest  TaskWorkspaceReconstructionRequestBinding
}

func (payload manualEditPayload) canonical() map[string]any {
	canonical := map[string]any{
		"artifact_version_id":     payload.artifactVersionID.value,
		"reconstruction_required": payload.reconstructionRequired,
	}
	if payload.reconstructionRequest.valid() {
		canonical["reconstruction_request"] = payload.reconstructionRequest.canonical()
	}
	return canonical
}
func (payload manualEditPayload) valid() bool {
	if !validOpaqueID(payload.artifactVersionID.value) {
		return false
	}
	if payload.reconstructionRequired {
		return payload.reconstructionRequest.valid()
	}
	return payload.reconstructionRequest == (TaskWorkspaceReconstructionRequestBinding{})
}

func NewBeginManualEditIntent(
	header IntentHeader,
	authority UserAuthority,
	artifactVersionID ArtifactVersionID,
) TransitionIntent {
	return newIntent(
		header, IntentBeginManualEdit, authority.value,
		manualEditPayload{artifactVersionID: artifactVersionID},
	)
}

// NewBeginManualEditAfterExpiryIntent begins a manual-edit activity whose
// workspace materialization must first be reconstructed from the exact latest
// Artifact Version. Reconstruction remains a C04 enactment; only accepted C04
// evidence may make the manual-edit Phase available.
func NewBeginManualEditAfterExpiryIntent(
	header IntentHeader,
	authority UserAuthority,
	artifactVersionID ArtifactVersionID,
	reconstructionRequest TaskWorkspaceReconstructionRequestBinding,
) TransitionIntent {
	return newIntent(header, IntentBeginManualEdit, authority.value, manualEditPayload{
		artifactVersionID: artifactVersionID, reconstructionRequired: true,
		reconstructionRequest: reconstructionRequest,
	})
}

type RuntimeEvidenceBinding struct {
	Evidence           EvidenceRef
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	RuntimeRunID       RuntimeRunID
	OperationID        OperationID
	Generation         RuntimeGeneration
	Fence              RuntimeFence
	SafetyEpoch        SafetyEpoch
	Outcome            RuntimeRunOutcome
}

type runtimeEvidencePayload struct{ binding RuntimeEvidenceBinding }

func (payload runtimeEvidencePayload) canonical() map[string]any {
	extra := map[string]any{"runtime_run_id": payload.binding.RuntimeRunID.value}
	if payload.binding.Outcome != 0 {
		extra["outcome"] = runtimeRunOutcomeName(payload.binding.Outcome)
	}
	extra["phase_run_fence"] = uint64(payload.binding.PhaseRunFence)
	extra["phase_run_generation"] = uint64(payload.binding.PhaseRunGeneration)
	extra["safety_epoch"] = uint64(payload.binding.SafetyEpoch)
	return evidenceCanonical(
		payload.binding.Evidence,
		payload.binding.PhaseRunID,
		payload.binding.OperationID,
		ProducerGeneration(payload.binding.Generation),
		uint64(payload.binding.Fence), extra,
	)
}
func (payload runtimeEvidencePayload) valid() bool {
	return validEvidenceBinding(
		payload.binding.Evidence,
		payload.binding.PhaseRunID,
		payload.binding.OperationID,
		ProducerGeneration(payload.binding.Generation),
		uint64(payload.binding.Fence),
	) && validOpaqueID(payload.binding.RuntimeRunID.value) &&
		payload.binding.PhaseRunGeneration > 0 && payload.binding.PhaseRunFence > 0 &&
		payload.binding.SafetyEpoch > 0 &&
		(payload.binding.Outcome == 0 || runtimeRunOutcomeName(payload.binding.Outcome) != "")
}

func NewAcceptRuntimeEvidenceIntent(
	header IntentHeader,
	authority RuntimeAuthority,
	binding RuntimeEvidenceBinding,
) TransitionIntent {
	return newIntent(header, IntentAcceptRuntimeEvidence, authority.value, runtimeEvidencePayload{binding})
}

type ValidationEvidenceBinding struct {
	Evidence           EvidenceRef
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	Generation         ProducerGeneration
	Fence              ValidationFence
	SafetyEpoch        SafetyEpoch
	Outcome            PhaseValidationOutcome
	LifecycleCommit    TaskWorkspaceLifecycleCommitRequestBinding
}

type validationEvidencePayload struct{ binding ValidationEvidenceBinding }

func (payload validationEvidencePayload) canonical() map[string]any {
	extra := map[string]any(nil)
	if payload.binding.Outcome != 0 {
		extra = map[string]any{"outcome": phaseValidationOutcomeName(payload.binding.Outcome)}
	}
	if extra == nil {
		extra = make(map[string]any)
	}
	extra["phase_run_fence"] = uint64(payload.binding.PhaseRunFence)
	extra["phase_run_generation"] = uint64(payload.binding.PhaseRunGeneration)
	extra["safety_epoch"] = uint64(payload.binding.SafetyEpoch)
	if payload.binding.LifecycleCommit.valid() {
		extra["task_workspace_lifecycle_commit"] = payload.binding.LifecycleCommit.canonical()
	}
	return evidenceCanonical(
		payload.binding.Evidence,
		payload.binding.PhaseRunID,
		OperationID{},
		payload.binding.Generation,
		uint64(payload.binding.Fence), extra,
	)
}
func (payload validationEvidencePayload) valid() bool {
	return validEvidenceRef(payload.binding.Evidence) &&
		validOpaqueID(payload.binding.PhaseRunID.value) &&
		payload.binding.PhaseRunGeneration > 0 && payload.binding.PhaseRunFence > 0 &&
		payload.binding.Generation > 0 && payload.binding.Fence > 0 &&
		payload.binding.SafetyEpoch > 0 &&
		(payload.binding.LifecycleCommit == (TaskWorkspaceLifecycleCommitRequestBinding{}) ||
			payload.binding.LifecycleCommit.valid()) &&
		(payload.binding.Outcome == 0 || phaseValidationOutcomeName(payload.binding.Outcome) != "")
}

func NewAcceptPhaseValidationEvidenceIntent(
	header IntentHeader,
	authority ValidatorAuthority,
	binding ValidationEvidenceBinding,
) TransitionIntent {
	return newIntent(
		header,
		IntentAcceptPhaseValidationEvidence,
		authority.value,
		validationEvidencePayload{binding},
	)
}

type TaskWorkspaceLifecycleEvidenceBinding struct {
	Evidence           EvidenceRef
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	OperationID        OperationID
	Generation         TaskWorkspaceLifecycleGeneration
	Fence              TaskWorkspaceLifecycleFence
	ObservedGeneration TaskWorkspaceLifecycleGeneration
	ObservedFence      TaskWorkspaceLifecycleFence
	SafetyEpoch        SafetyEpoch
	Outcome            LifecycleEvidenceOutcome
	RevisionID         TaskWorkspaceRevisionID
	CheckpointID       CheckpointID
}

// TaskWorkspaceReconstructionEvidenceBinding is the content-free result of a
// C04 reconstruction enactment. It deliberately has no Phase Run identity:
// reconstruction establishes the workspace before the manual-edit Phase Run
// exists.
type TaskWorkspaceReconstructionEvidenceBinding struct {
	Evidence           EvidenceRef
	OperationID        OperationID
	ArtifactVersionID  ArtifactVersionID
	RevisionID         TaskWorkspaceRevisionID
	CheckpointID       CheckpointID
	Generation         TaskWorkspaceLifecycleGeneration
	Fence              TaskWorkspaceLifecycleFence
	ObservedGeneration TaskWorkspaceLifecycleGeneration
	ObservedFence      TaskWorkspaceLifecycleFence
	SafetyEpoch        SafetyEpoch
}

type LifecycleEvidenceOutcome = TaskWorkspaceLifecycleOutcome

const (
	LifecycleEvidenceCommitted = TaskWorkspaceLifecycleCommitted
	LifecycleEvidenceFenced    = TaskWorkspaceLifecycleFenced
)

type PublicationEvidenceBinding struct {
	Evidence           EvidenceRef
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	OperationID        OperationID
	Generation         ProducerGeneration
	Fence              PublicationFence
	SafetyEpoch        SafetyEpoch
	Outcome            PublicationOutcome
	ArtifactVersionID  ArtifactVersionID
}

type SchedulingEvidenceBinding struct {
	Evidence           EvidenceRef
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	OperationID        OperationID
	Generation         ProducerGeneration
	Fence              SchedulerFence
	SafetyEpoch        SafetyEpoch
}

type taskWorkspaceLifecycleEvidencePayload struct {
	binding TaskWorkspaceLifecycleEvidenceBinding
}
type taskWorkspaceReconstructionEvidencePayload struct {
	binding TaskWorkspaceReconstructionEvidenceBinding
}
type publicationEvidencePayload struct{ binding PublicationEvidenceBinding }
type schedulingEvidencePayload struct{ binding SchedulingEvidenceBinding }

func (payload taskWorkspaceLifecycleEvidencePayload) canonical() map[string]any {
	extra := map[string]any(nil)
	if payload.binding.Outcome != 0 {
		extra = map[string]any{
			"checkpoint_id": payload.binding.CheckpointID.value,
			"outcome":       taskWorkspaceLifecycleOutcomeName(payload.binding.Outcome),
			"revision_id":   payload.binding.RevisionID.value,
		}
	}
	if extra == nil {
		extra = make(map[string]any)
	}
	extra["phase_run_fence"] = uint64(payload.binding.PhaseRunFence)
	extra["phase_run_generation"] = uint64(payload.binding.PhaseRunGeneration)
	extra["observed_fence"] = uint64(payload.binding.ObservedFence)
	extra["observed_generation"] = uint64(payload.binding.ObservedGeneration)
	extra["safety_epoch"] = uint64(payload.binding.SafetyEpoch)
	return evidenceCanonical(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		ProducerGeneration(payload.binding.Generation), uint64(payload.binding.Fence), extra,
	)
}
func (payload taskWorkspaceLifecycleEvidencePayload) valid() bool {
	if !validEvidenceBinding(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		ProducerGeneration(payload.binding.Generation), uint64(payload.binding.Fence),
	) || payload.binding.PhaseRunGeneration == 0 || payload.binding.PhaseRunFence == 0 ||
		payload.binding.ObservedGeneration < payload.binding.Generation ||
		payload.binding.ObservedFence <= payload.binding.Fence ||
		payload.binding.SafetyEpoch == 0 {
		return false
	}
	if payload.binding.Outcome == 0 {
		return payload.binding.RevisionID == (TaskWorkspaceRevisionID{}) &&
			payload.binding.CheckpointID == (CheckpointID{})
	}
	switch payload.binding.Outcome {
	case LifecycleEvidenceCommitted:
		return validOpaqueID(payload.binding.RevisionID.value) &&
			validOpaqueID(payload.binding.CheckpointID.value)
	case LifecycleEvidenceFenced:
		return payload.binding.RevisionID == (TaskWorkspaceRevisionID{}) &&
			payload.binding.CheckpointID == (CheckpointID{})
	default:
		return payload.binding.Outcome == TaskWorkspaceLifecycleRejected &&
			payload.binding.RevisionID == (TaskWorkspaceRevisionID{}) &&
			payload.binding.CheckpointID == (CheckpointID{})
	}
}

func (payload taskWorkspaceReconstructionEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence, PhaseRunID{}, payload.binding.OperationID,
		ProducerGeneration(payload.binding.Generation), uint64(payload.binding.Fence), map[string]any{
			"artifact_version_id": payload.binding.ArtifactVersionID.value,
			"checkpoint_id":       payload.binding.CheckpointID.value,
			"observed_fence":      uint64(payload.binding.ObservedFence),
			"observed_generation": uint64(payload.binding.ObservedGeneration),
			"revision_id":         payload.binding.RevisionID.value,
			"safety_epoch":        uint64(payload.binding.SafetyEpoch),
		},
	)
}

func (payload taskWorkspaceReconstructionEvidencePayload) valid() bool {
	return validEvidenceRef(payload.binding.Evidence) &&
		validOpaqueID(payload.binding.OperationID.value) &&
		validOpaqueID(payload.binding.ArtifactVersionID.value) &&
		validOpaqueID(payload.binding.RevisionID.value) &&
		validOpaqueID(payload.binding.CheckpointID.value) &&
		payload.binding.Generation > 0 && payload.binding.Fence > 0 &&
		payload.binding.ObservedGeneration > payload.binding.Generation &&
		payload.binding.ObservedFence > payload.binding.Fence &&
		payload.binding.SafetyEpoch > 0
}
func (payload publicationEvidencePayload) canonical() map[string]any {
	extra := map[string]any(nil)
	if payload.binding.Outcome != 0 {
		extra = map[string]any{
			"artifact_version_id": payload.binding.ArtifactVersionID.value,
			"outcome":             publicationOutcomeName(payload.binding.Outcome),
		}
	}
	if extra == nil {
		extra = make(map[string]any)
	}
	extra["phase_run_fence"] = uint64(payload.binding.PhaseRunFence)
	extra["phase_run_generation"] = uint64(payload.binding.PhaseRunGeneration)
	extra["safety_epoch"] = uint64(payload.binding.SafetyEpoch)
	return evidenceCanonical(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence), extra,
	)
}
func (payload publicationEvidencePayload) valid() bool {
	if !validEvidenceBinding(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence),
	) || payload.binding.PhaseRunGeneration == 0 || payload.binding.PhaseRunFence == 0 ||
		payload.binding.SafetyEpoch == 0 {
		return false
	}
	if payload.binding.Outcome == 0 {
		return payload.binding.ArtifactVersionID == (ArtifactVersionID{})
	}
	if publicationOutcomeName(payload.binding.Outcome) == "" {
		return false
	}
	if payload.binding.Outcome == PublicationActivated {
		return validOpaqueID(payload.binding.ArtifactVersionID.value)
	}
	return payload.binding.ArtifactVersionID == (ArtifactVersionID{})
}
func (payload schedulingEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence), map[string]any{
			"phase_run_fence":      uint64(payload.binding.PhaseRunFence),
			"phase_run_generation": uint64(payload.binding.PhaseRunGeneration),
			"safety_epoch":         uint64(payload.binding.SafetyEpoch),
		},
	)
}
func (payload schedulingEvidencePayload) valid() bool {
	return validEvidenceBinding(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence),
	) && payload.binding.PhaseRunGeneration > 0 && payload.binding.PhaseRunFence > 0 &&
		payload.binding.SafetyEpoch > 0
}

func NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
	header IntentHeader,
	authority TaskWorkspaceLifecycleAuthority,
	binding TaskWorkspaceLifecycleEvidenceBinding,
) TransitionIntent {
	return newIntent(
		header,
		IntentAcceptTaskWorkspaceLifecycleEvidence,
		authority.value,
		taskWorkspaceLifecycleEvidencePayload{binding},
	)
}

func NewAcceptTaskWorkspaceReconstructionEvidenceIntent(
	header IntentHeader,
	authority TaskWorkspaceLifecycleAuthority,
	binding TaskWorkspaceReconstructionEvidenceBinding,
) TransitionIntent {
	return newIntent(
		header,
		IntentAcceptTaskWorkspaceReconstructionEvidence,
		authority.value,
		taskWorkspaceReconstructionEvidencePayload{binding},
	)
}

func NewAcceptPublicationEvidenceIntent(
	header IntentHeader,
	authority PublicationAuthority,
	binding PublicationEvidenceBinding,
) TransitionIntent {
	return newIntent(header, IntentAcceptPublicationEvidence, authority.value, publicationEvidencePayload{binding})
}

func NewAcceptSchedulingEvidenceIntent(
	header IntentHeader,
	authority SchedulerAuthority,
	binding SchedulingEvidenceBinding,
) TransitionIntent {
	return newIntent(header, IntentAcceptSchedulingEvidence, authority.value, schedulingEvidencePayload{binding})
}

type reconcilePayload struct {
	operationID OperationID
	fence       ReconciliationFence
}

func (payload reconcilePayload) canonical() map[string]any {
	return map[string]any{
		"fence":        uint64(payload.fence),
		"operation_id": payload.operationID.value,
	}
}
func (payload reconcilePayload) valid() bool {
	return validOpaqueID(payload.operationID.value) && payload.fence > 0
}

func NewReconcileEnactmentIntent(
	header IntentHeader,
	authority WorkerAuthority,
	operationID OperationID,
	fence ReconciliationFence,
) TransitionIntent {
	return newIntent(header, IntentReconcileEnactment, authority.value, reconcilePayload{operationID, fence})
}

type OperationalFenceBinding struct {
	Generation     RecoveryGeneration
	Fence          RecoveryFence
	SafetyEpoch    SafetyEpoch
	Mode           OperationalMode
	LifecycleFence TaskWorkspaceLifecycleFenceRequestBinding
}

type operationalFencePayload struct{ binding OperationalFenceBinding }

func (payload operationalFencePayload) canonical() map[string]any {
	canonical := map[string]any{
		"fence":        uint64(payload.binding.Fence),
		"generation":   uint64(payload.binding.Generation),
		"mode":         operationalModeName(payload.binding.Mode),
		"safety_epoch": uint64(payload.binding.SafetyEpoch),
	}
	if payload.binding.LifecycleFence.valid() {
		canonical["task_workspace_lifecycle_fence"] = payload.binding.LifecycleFence.canonical()
	}
	return canonical
}
func (payload operationalFencePayload) valid() bool {
	return payload.binding.Generation > 0 && payload.binding.Fence > 0 &&
		payload.binding.SafetyEpoch > 0 &&
		(payload.binding.LifecycleFence == (TaskWorkspaceLifecycleFenceRequestBinding{}) ||
			payload.binding.LifecycleFence.valid()) &&
		operationalModeName(payload.binding.Mode) != ""
}

func NewApplyOperationalFenceIntent(
	header IntentHeader,
	authority RecoveryAuthority,
	binding OperationalFenceBinding,
) TransitionIntent {
	return newIntent(header, IntentApplyOperationalFence, authority.value, operationalFencePayload{binding})
}

func newIntent(
	header IntentHeader,
	kind IntentKind,
	authority authorityValue,
	payload intentPayload,
) TransitionIntent {
	intent := intentValue{header: header, kind: kind, authority: authority, payload: payload}
	digest, err := computeCanonicalIntent(intent)
	if err == nil {
		intent.header.CanonicalRequestDigest = digest
	}
	return intent
}

func evidenceCanonical(
	evidence EvidenceRef,
	phaseRunID PhaseRunID,
	operationID OperationID,
	generation ProducerGeneration,
	fence uint64,
	extra map[string]any,
) map[string]any {
	encoded := map[string]any{
		"evidence": map[string]any{
			"digest": evidence.Digest.String(),
			"id":     evidence.ID.value,
			"kind":   evidenceKindName(evidence.Kind),
		},
		"fence":        fence,
		"generation":   uint64(generation),
		"phase_run_id": phaseRunID.value,
	}
	if operationID != (OperationID{}) {
		encoded["operation_id"] = operationID.value
	}
	for key, value := range extra {
		encoded[key] = value
	}
	return encoded
}

func validEvidenceBinding(
	evidence EvidenceRef,
	phaseRunID PhaseRunID,
	operationID OperationID,
	generation ProducerGeneration,
	fence uint64,
) bool {
	return validEvidenceRef(evidence) && validOpaqueID(phaseRunID.value) &&
		validOpaqueID(operationID.value) && generation > 0 && fence > 0
}

func validEvidenceRef(evidence EvidenceRef) bool {
	return validOpaqueID(evidence.ID.value) && evidenceKindName(evidence.Kind) != "" &&
		evidence.Digest != (EvidenceDigest{})
}

func authorityKindName(kind AuthorityKind) string {
	switch kind {
	case AuthorityUser:
		return "user"
	case AuthorityAdministrator:
		return "administrator"
	case AuthorityWorker:
		return "worker"
	case AuthorityRuntime:
		return "runtime"
	case AuthorityValidator:
		return "validator"
	case AuthorityTaskWorkspaceLifecycle:
		return "task_workspace_lifecycle"
	case AuthorityPublication:
		return "publication"
	case AuthorityScheduler:
		return "scheduler"
	case AuthorityRecovery:
		return "recovery"
	default:
		return ""
	}
}

func administratorReasonName(reason AdministratorReason) string {
	switch reason {
	case AdministratorReasonSafety:
		return "safety"
	case AdministratorReasonRecovery:
		return "recovery"
	default:
		return ""
	}
}

func cancelReasonName(reason CancelReason) string {
	switch reason {
	case CancelReasonUserRequested:
		return "user_requested"
	case CancelReasonSafety:
		return "safety"
	default:
		return ""
	}
}

func operationalModeName(mode OperationalMode) string {
	switch mode {
	case OperationalReadOnly:
		return "read_only"
	case OperationalFullReady:
		return "full_ready"
	default:
		return ""
	}
}

func evidenceKindName(kind EvidenceKind) string {
	switch kind {
	case EvidenceRuntime:
		return "runtime"
	case EvidencePhaseValidation:
		return "phase_validation"
	case EvidenceTaskWorkspaceLifecycle:
		return "task_workspace_lifecycle"
	case EvidencePublication:
		return "publication"
	case EvidenceScheduling:
		return "scheduling"
	case EvidenceUsageAccounting:
		return "usage_accounting"
	default:
		return ""
	}
}

func lifecycleEvidenceOutcomeName(outcome LifecycleEvidenceOutcome) string {
	switch outcome {
	case LifecycleEvidenceCommitted:
		return "committed"
	case LifecycleEvidenceFenced:
		return "fenced"
	default:
		return ""
	}
}
