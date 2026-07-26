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

type cancelPayload struct{ reason CancelReason }

func (payload cancelPayload) canonical() map[string]any {
	return map[string]any{"reason": cancelReasonName(payload.reason)}
}
func (payload cancelPayload) valid() bool { return cancelReasonName(payload.reason) != "" }

func NewCancelTaskByUserIntent(
	header IntentHeader,
	authority UserAuthority,
	reason CancelReason,
) TransitionIntent {
	return newIntent(header, IntentCancelTask, authority.value, cancelPayload{reason})
}

func NewCancelTaskByAdministratorIntent(
	header IntentHeader,
	authority AdministratorAuthority,
	reason CancelReason,
) TransitionIntent {
	return newIntent(header, IntentCancelTask, authority.value, cancelPayload{reason})
}

type manualEditPayload struct{ artifactVersionID ArtifactVersionID }

func (payload manualEditPayload) canonical() map[string]any {
	return map[string]any{"artifact_version_id": payload.artifactVersionID.value}
}
func (payload manualEditPayload) valid() bool {
	return validOpaqueID(payload.artifactVersionID.value)
}

func NewBeginManualEditIntent(
	header IntentHeader,
	authority UserAuthority,
	artifactVersionID ArtifactVersionID,
) TransitionIntent {
	return newIntent(header, IntentBeginManualEdit, authority.value, manualEditPayload{artifactVersionID})
}

type RuntimeEvidenceBinding struct {
	Evidence     EvidenceRef
	PhaseRunID   PhaseRunID
	RuntimeRunID RuntimeRunID
	OperationID  OperationID
	Generation   ProducerGeneration
	Fence        RuntimeFence
}

type runtimeEvidencePayload struct{ binding RuntimeEvidenceBinding }

func (payload runtimeEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence,
		payload.binding.PhaseRunID,
		payload.binding.OperationID,
		payload.binding.Generation,
		uint64(payload.binding.Fence),
		map[string]any{"runtime_run_id": payload.binding.RuntimeRunID.value},
	)
}
func (payload runtimeEvidencePayload) valid() bool {
	return validEvidenceBinding(
		payload.binding.Evidence,
		payload.binding.PhaseRunID,
		payload.binding.OperationID,
		payload.binding.Generation,
		uint64(payload.binding.Fence),
	) && validOpaqueID(payload.binding.RuntimeRunID.value)
}

func NewAcceptRuntimeEvidenceIntent(
	header IntentHeader,
	authority RuntimeAuthority,
	binding RuntimeEvidenceBinding,
) TransitionIntent {
	return newIntent(header, IntentAcceptRuntimeEvidence, authority.value, runtimeEvidencePayload{binding})
}

type ValidationEvidenceBinding struct {
	Evidence   EvidenceRef
	PhaseRunID PhaseRunID
	Generation ProducerGeneration
	Fence      ValidationFence
}

type validationEvidencePayload struct{ binding ValidationEvidenceBinding }

func (payload validationEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence,
		payload.binding.PhaseRunID,
		OperationID{},
		payload.binding.Generation,
		uint64(payload.binding.Fence),
		nil,
	)
}
func (payload validationEvidencePayload) valid() bool {
	return validEvidenceRef(payload.binding.Evidence) &&
		validOpaqueID(payload.binding.PhaseRunID.value) &&
		payload.binding.Generation > 0 && payload.binding.Fence > 0
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
	Evidence    EvidenceRef
	PhaseRunID  PhaseRunID
	OperationID OperationID
	Generation  ProducerGeneration
	Fence       TaskWorkspaceLifecycleFence
}

type PublicationEvidenceBinding struct {
	Evidence    EvidenceRef
	PhaseRunID  PhaseRunID
	OperationID OperationID
	Generation  ProducerGeneration
	Fence       PublicationFence
}

type SchedulingEvidenceBinding struct {
	Evidence    EvidenceRef
	PhaseRunID  PhaseRunID
	OperationID OperationID
	Generation  ProducerGeneration
	Fence       SchedulerFence
}

type taskWorkspaceLifecycleEvidencePayload struct {
	binding TaskWorkspaceLifecycleEvidenceBinding
}
type publicationEvidencePayload struct{ binding PublicationEvidenceBinding }
type schedulingEvidencePayload struct{ binding SchedulingEvidenceBinding }

func (payload taskWorkspaceLifecycleEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence), nil,
	)
}
func (payload taskWorkspaceLifecycleEvidencePayload) valid() bool {
	return validEvidenceBinding(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence),
	)
}
func (payload publicationEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence), nil,
	)
}
func (payload publicationEvidencePayload) valid() bool {
	return validEvidenceBinding(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence),
	)
}
func (payload schedulingEvidencePayload) canonical() map[string]any {
	return evidenceCanonical(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence), nil,
	)
}
func (payload schedulingEvidencePayload) valid() bool {
	return validEvidenceBinding(
		payload.binding.Evidence, payload.binding.PhaseRunID, payload.binding.OperationID,
		payload.binding.Generation, uint64(payload.binding.Fence),
	)
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
	Generation RecoveryGeneration
	Fence      RecoveryFence
	Mode       OperationalMode
}

type operationalFencePayload struct{ binding OperationalFenceBinding }

func (payload operationalFencePayload) canonical() map[string]any {
	return map[string]any{
		"fence":      uint64(payload.binding.Fence),
		"generation": uint64(payload.binding.Generation),
		"mode":       operationalModeName(payload.binding.Mode),
	}
}
func (payload operationalFencePayload) valid() bool {
	return payload.binding.Generation > 0 && payload.binding.Fence > 0 &&
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
	default:
		return ""
	}
}
