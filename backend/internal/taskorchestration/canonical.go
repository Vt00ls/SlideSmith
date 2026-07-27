package taskorchestration

import (
	"crypto/sha256"
	"encoding/json"
)

const canonicalIntentDomain = "slidesmith.task-orchestration.intent/v1\n"
const canonicalEvidenceReplayDomain = "slidesmith.task-orchestration.evidence-replay/v1\n"
const canonicalEnactmentPayloadDomain = "slidesmith.task-orchestration.enactment/v1\n"

func canonicalizeIntent(intent TransitionIntent) (CanonicalRequestDigest, error) {
	digest, err := computeCanonicalIntent(intent)
	if err != nil {
		return CanonicalRequestDigest{}, err
	}
	if intent.Header().CanonicalRequestDigest != digest {
		return CanonicalRequestDigest{}, newError(ErrorIntegrityConflict)
	}
	return digest, nil
}

func computeCanonicalIntent(intent TransitionIntent) (CanonicalRequestDigest, error) {
	if intent == nil {
		return CanonicalRequestDigest{}, invalidIntentError()
	}
	if intent.Header().SchemaVersion.Major() != SchemaV1.Major() {
		return CanonicalRequestDigest{}, newError(ErrorUnsupportedSchema)
	}
	if err := validateEvidenceIntent(intent); err != nil {
		return CanonicalRequestDigest{}, err
	}
	if !validIntent(intent) {
		return CanonicalRequestDigest{}, invalidIntentError()
	}
	header := intent.Header()
	encoded, err := json.Marshal(map[string]any{
		"activity_generation":    uint64(header.ActivityGeneration),
		"authority":              intent.canonicalAuthority(),
		"decision_request_id":    header.DecisionRequestID.value,
		"expected_task_revision": uint64(header.ExpectedTaskRevision),
		"kind":                   intentKindName(intent.Kind()),
		"occurred_at":            header.OccurredAt.UTC().Format(canonicalTimeFormat),
		"payload":                intent.canonicalPayload(),
		"schema": map[string]any{
			"major": uint64(header.SchemaVersion.Major()),
			"minor": uint64(header.SchemaVersion.Minor()),
		},
		"task_id": header.TaskID.value,
	})
	if err != nil {
		return CanonicalRequestDigest{}, invalidIntentError()
	}
	hashed := sha256.Sum256(append([]byte(canonicalIntentDomain), encoded...))
	return CanonicalRequestDigest(hashed), nil
}

func computeEvidenceReplayDigest(intent TransitionIntent) (EvidenceID, [32]byte, bool, error) {
	facts, isEvidenceIntent, err := evidenceFacts(intent)
	if err != nil || !isEvidenceIntent {
		return EvidenceID{}, [32]byte{}, isEvidenceIntent, err
	}
	header := intent.Header()
	encoded, err := json.Marshal(map[string]any{
		"activity_generation":    uint64(header.ActivityGeneration),
		"authority":              intent.canonicalAuthority(),
		"expected_task_revision": uint64(header.ExpectedTaskRevision),
		"kind":                   intentKindName(intent.Kind()),
		"occurred_at":            header.OccurredAt.UTC().Format(canonicalTimeFormat),
		"payload":                intent.canonicalPayload(),
		"schema": map[string]any{
			"major": uint64(header.SchemaVersion.Major()),
			"minor": uint64(header.SchemaVersion.Minor()),
		},
		"task_id": header.TaskID.value,
	})
	if err != nil {
		return EvidenceID{}, [32]byte{}, true, invalidIntentError()
	}
	return facts.evidence.ID,
		sha256.Sum256(append([]byte(canonicalEvidenceReplayDomain), encoded...)), true, nil
}

func computeEnactmentPayloadDigest(payload map[string]any) EnactmentPayloadDigest {
	encoded, _ := json.Marshal(payload)
	return EnactmentPayloadDigest(
		sha256.Sum256(append([]byte(canonicalEnactmentPayloadDomain), encoded...)),
	)
}

func validateEvidenceIntent(intent TransitionIntent) error {
	facts, isEvidenceIntent, err := evidenceFacts(intent)
	if err != nil {
		return err
	}
	if isEvidenceIntent &&
		(!validEvidenceRef(facts.evidence) || facts.evidence.Kind != facts.expectedKind) {
		return newError(ErrorEvidenceInvalid)
	}
	return nil
}

type acceptedEvidenceFacts struct {
	phaseRunID   PhaseRunID
	evidence     EvidenceRef
	expectedKind EvidenceKind
}

func evidenceFacts(intent TransitionIntent) (acceptedEvidenceFacts, bool, error) {
	typed, ok := intent.(intentValue)
	if !ok {
		return acceptedEvidenceFacts{}, false, nil
	}

	switch typed.kind {
	case IntentAcceptRuntimeEvidence:
		payload, ok := typed.payload.(runtimeEvidencePayload)
		if !ok {
			return acceptedEvidenceFacts{}, false, invalidIntentError()
		}
		return acceptedEvidenceFacts{
			phaseRunID:   payload.binding.PhaseRunID,
			evidence:     payload.binding.Evidence,
			expectedKind: EvidenceRuntime,
		}, true, nil
	case IntentAcceptPhaseValidationEvidence:
		payload, ok := typed.payload.(validationEvidencePayload)
		if !ok {
			return acceptedEvidenceFacts{}, false, invalidIntentError()
		}
		return acceptedEvidenceFacts{
			phaseRunID:   payload.binding.PhaseRunID,
			evidence:     payload.binding.Evidence,
			expectedKind: EvidencePhaseValidation,
		}, true, nil
	case IntentAcceptTaskWorkspaceLifecycleEvidence:
		payload, ok := typed.payload.(taskWorkspaceLifecycleEvidencePayload)
		if !ok {
			return acceptedEvidenceFacts{}, false, invalidIntentError()
		}
		return acceptedEvidenceFacts{
			phaseRunID:   payload.binding.PhaseRunID,
			evidence:     payload.binding.Evidence,
			expectedKind: EvidenceTaskWorkspaceLifecycle,
		}, true, nil
	case IntentAcceptTaskWorkspaceReconstructionEvidence:
		payload, ok := typed.payload.(taskWorkspaceReconstructionEvidencePayload)
		if !ok {
			return acceptedEvidenceFacts{}, false, invalidIntentError()
		}
		return acceptedEvidenceFacts{
			evidence: payload.binding.Evidence, expectedKind: EvidenceTaskWorkspaceLifecycle,
		}, true, nil
	case IntentAcceptPublicationEvidence:
		payload, ok := typed.payload.(publicationEvidencePayload)
		if !ok {
			return acceptedEvidenceFacts{}, false, invalidIntentError()
		}
		return acceptedEvidenceFacts{
			phaseRunID:   payload.binding.PhaseRunID,
			evidence:     payload.binding.Evidence,
			expectedKind: EvidencePublication,
		}, true, nil
	case IntentAcceptSchedulingEvidence:
		payload, ok := typed.payload.(schedulingEvidencePayload)
		if !ok {
			return acceptedEvidenceFacts{}, false, invalidIntentError()
		}
		return acceptedEvidenceFacts{
			phaseRunID:   payload.binding.PhaseRunID,
			evidence:     payload.binding.Evidence,
			expectedKind: EvidenceScheduling,
		}, true, nil
	default:
		return acceptedEvidenceFacts{}, false, nil
	}
}

const canonicalTimeFormat = "2006-01-02T15:04:05.999999999Z"

func intentKindName(kind IntentKind) string {
	switch kind {
	case IntentStartTask:
		return "start_task"
	case IntentMakeWorkAvailable:
		return "make_work_available"
	case IntentSubmitConfirmationGate:
		return "submit_confirmation_gate"
	case IntentRetryPhase:
		return "retry_phase"
	case IntentCancelTask:
		return "cancel_task"
	case IntentBeginManualEdit:
		return "begin_manual_edit"
	case IntentAcceptRuntimeEvidence:
		return "accept_runtime_evidence"
	case IntentAcceptPhaseValidationEvidence:
		return "accept_phase_validation_evidence"
	case IntentAcceptTaskWorkspaceLifecycleEvidence:
		return "accept_task_workspace_lifecycle_evidence"
	case IntentAcceptPublicationEvidence:
		return "accept_publication_evidence"
	case IntentAcceptSchedulingEvidence:
		return "accept_scheduling_evidence"
	case IntentReconcileEnactment:
		return "reconcile_enactment"
	case IntentApplyOperationalFence:
		return "apply_operational_fence"
	case IntentRetryRuntimeRun:
		return "retry_runtime_run"
	case IntentAcceptTaskWorkspaceReconstructionEvidence:
		return "accept_task_workspace_reconstruction_evidence"
	default:
		return ""
	}
}

func validIntent(intent TransitionIntent) bool {
	if intent == nil || !validIntentHeader(intent.Header()) || intentKindName(intent.Kind()) == "" {
		return false
	}
	typed, ok := intent.(intentValue)
	if !ok || !typed.authority.valid() || typed.payload == nil || !typed.payload.valid() {
		return false
	}
	return authorityAllowed(typed.kind, typed.authority.kind)
}

func validIntentHeader(header IntentHeader) bool {
	return validOpaqueID(header.DecisionRequestID.value) &&
		validOpaqueID(header.TaskID.value) &&
		header.ActivityGeneration > 0 &&
		!header.OccurredAt.IsZero()
}

func authorityAllowed(kind IntentKind, authority AuthorityKind) bool {
	switch kind {
	case IntentStartTask, IntentSubmitConfirmationGate, IntentRetryPhase, IntentBeginManualEdit:
		return authority == AuthorityUser
	case IntentCancelTask:
		return authority == AuthorityUser || authority == AuthorityAdministrator
	case IntentMakeWorkAvailable, IntentReconcileEnactment, IntentRetryRuntimeRun:
		return authority == AuthorityWorker
	case IntentAcceptRuntimeEvidence:
		return authority == AuthorityRuntime
	case IntentAcceptPhaseValidationEvidence:
		return authority == AuthorityValidator
	case IntentAcceptTaskWorkspaceLifecycleEvidence, IntentAcceptTaskWorkspaceReconstructionEvidence:
		return authority == AuthorityTaskWorkspaceLifecycle
	case IntentAcceptPublicationEvidence:
		return authority == AuthorityPublication
	case IntentAcceptSchedulingEvidence:
		return authority == AuthorityScheduler
	case IntentApplyOperationalFence:
		return authority == AuthorityRecovery
	default:
		return false
	}
}
