package taskorchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type AuditSchemaVersion uint32
type AuditIntegrityVersion uint16
type AuditFactDigest [32]byte
type AuditOwningModule uint8
type AuditResult uint8
type AuditReasonCode uint8
type AuditSourceClock uint8

const (
	AuditSchemaV1    AuditSchemaVersion    = 1 << 16
	AuditIntegrityV1 AuditIntegrityVersion = 1
)

const AuditModuleTaskOrchestration AuditOwningModule = 1
const AuditAccepted AuditResult = 1

const (
	AuditReasonNone AuditReasonCode = iota
	AuditReasonUserRequested
	AuditReasonAdministratorSafety
	AuditReasonAdministratorRecovery
	AuditReasonRetry
	AuditReasonManualEdit
	AuditReasonReconciliation
	AuditReasonRecoveryFence
)

const AuditSourceTaskOrchestrationClock AuditSourceClock = 1

func (digest AuditFactDigest) String() string { return hex.EncodeToString(digest[:]) }

type AuditEnactmentBinding struct {
	OperationID        OperationID
	Kind               EnactmentKind
	PayloadDigest      EnactmentPayloadDigest
	ActivityGeneration ActivityGeneration
	FenceKind          EnactmentFenceKind
	Fence              uint64
	CausationID        CausationID
}

// AuditFactRef is the content-free authoritative audit envelope committed in
// the same transaction as its accepted Decision, revision, and outbox.
type AuditFactRef struct {
	SchemaVersion                AuditSchemaVersion
	AuditFactID                  AuditFactID
	CanonicalDigest              AuditFactDigest
	IntegrityVersion             AuditIntegrityVersion
	OwningModule                 AuditOwningModule
	DecisionID                   DecisionID
	TaskID                       TaskID
	IdempotencyDecisionRequestID DecisionRequestID
	Action                       IntentKind
	Result                       AuditResult
	AuthorityKind                AuthorityKind
	AuthorityID                  AuthorityID
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
	EvidenceRefs                 []EvidenceRef
	EnactmentRefs                []AuditEnactmentBinding
	RetryPhaseRunID              PhaseRunID
	RetryRuntimeRunID            RuntimeRunID
	ReconciliationOperationID    OperationID
	ReconciliationFence          ReconciliationFence
	OccurredAt                   time.Time
	RecordedAt                   time.Time
	SourceClock                  AuditSourceClock
}

// MandatoryAuditFactDigest returns the canonical integrity identity. The
// digest field itself is excluded so recomputation is stable.
func MandatoryAuditFactDigest(fact AuditFactRef) AuditFactDigest {
	evidence := make([]map[string]any, len(fact.EvidenceRefs))
	for index, ref := range fact.EvidenceRefs {
		evidence[index] = map[string]any{
			"digest": ref.Digest.String(), "id": ref.ID.value, "kind": evidenceKindName(ref.Kind),
		}
	}
	enactments := make([]map[string]any, len(fact.EnactmentRefs))
	for index, ref := range fact.EnactmentRefs {
		enactments[index] = map[string]any{
			"activity_generation": uint64(ref.ActivityGeneration),
			"causation_id":        ref.CausationID.value, "fence": ref.Fence,
			"fence_kind": uint64(ref.FenceKind), "kind": uint64(ref.Kind),
			"operation_id": ref.OperationID.value, "payload_digest": ref.PayloadDigest.String(),
		}
	}
	encoded, _ := json.Marshal(map[string]any{
		"action": uint64(fact.Action), "after_activity_generation": uint64(fact.AfterActivityGeneration),
		"after_safety_epoch": uint64(fact.AfterSafetyEpoch), "after_status": uint64(fact.AfterStatus),
		"after_task_revision": uint64(fact.AfterTaskRevision), "audit_fact_id": fact.AuditFactID.value,
		"authority_generation": uint64(fact.AuthorizationGeneration),
		"authority_id":         fact.AuthorityID.value, "authority_kind": uint64(fact.AuthorityKind),
		"authority_reason":           uint64(fact.AuthorityReason),
		"before_activity_generation": uint64(fact.BeforeActivityGeneration),
		"before_safety_epoch":        uint64(fact.BeforeSafetyEpoch), "before_status": uint64(fact.BeforeStatus),
		"before_task_revision": uint64(fact.BeforeTaskRevision), "decision_id": fact.DecisionID.value,
		"enactments": enactments, "evidence": evidence,
		"idempotency_decision_request_id": fact.IdempotencyDecisionRequestID.value,
		"integrity_version":               uint64(fact.IntegrityVersion), "module": uint64(fact.OwningModule),
		"occurred_at": fact.OccurredAt.UTC().Format(time.RFC3339Nano),
		"reason_code": uint64(fact.ReasonCode), "reconciliation_fence": uint64(fact.ReconciliationFence),
		"reconciliation_operation_id": fact.ReconciliationOperationID.value,
		"recorded_at":                 fact.RecordedAt.UTC().Format(time.RFC3339Nano),
		"recovery_generation":         uint64(fact.RecoveryGeneration), "result": uint64(fact.Result),
		"retry_phase_run_id":   fact.RetryPhaseRunID.value,
		"retry_runtime_run_id": fact.RetryRuntimeRunID.value,
		"schema_version":       uint64(fact.SchemaVersion), "source_clock": uint64(fact.SourceClock),
		"task_id": fact.TaskID.value,
	})
	return sha256.Sum256(encoded)
}

func newMandatoryAuditFact(
	id AuditFactID,
	intent intentValue,
	decisionID DecisionID,
	before taskRecord,
	after taskRecord,
	beforeRecovery recoveryBinding,
	afterRecovery recoveryBinding,
	previousRevision TaskRevision,
	acceptedRevision TaskRevision,
	acceptedEvidence []EvidenceRef,
	enactments []EnactmentRef,
	recordedAt time.Time,
) AuditFactRef {
	fact := AuditFactRef{
		SchemaVersion: AuditSchemaV1, AuditFactID: id, IntegrityVersion: AuditIntegrityV1,
		OwningModule: AuditModuleTaskOrchestration, DecisionID: decisionID,
		TaskID: intent.header.TaskID, IdempotencyDecisionRequestID: intent.header.DecisionRequestID,
		Action: intent.kind, Result: AuditAccepted,
		AuthorityKind: intent.authority.kind, AuthorityID: intent.authority.id,
		AuthorizationGeneration: intent.authority.generation, AuthorityReason: intent.authority.reason,
		ReasonCode:         auditReason(intent),
		BeforeTaskRevision: previousRevision, AfterTaskRevision: acceptedRevision,
		BeforeStatus: aggregateStatus(before.aggregate), AfterStatus: aggregateStatus(after.aggregate),
		BeforeActivityGeneration: effectiveActivityGeneration(before, beforeRecovery),
		AfterActivityGeneration:  effectiveActivityGeneration(after, afterRecovery),
		RecoveryGeneration:       afterRecovery.generation,
		BeforeSafetyEpoch:        effectiveSafetyEpoch(before, beforeRecovery),
		AfterSafetyEpoch:         effectiveSafetyEpoch(after, afterRecovery),
		EvidenceRefs:             append([]EvidenceRef(nil), acceptedEvidence...),
		OccurredAt:               intent.header.OccurredAt.UTC(), RecordedAt: recordedAt.UTC(),
		SourceClock: AuditSourceTaskOrchestrationClock,
	}
	for _, ref := range enactments {
		fenceKind, fence := postgresFenceValue(ref.Fence)
		fact.EnactmentRefs = append(fact.EnactmentRefs, AuditEnactmentBinding{
			OperationID: ref.OperationID, Kind: ref.Kind, PayloadDigest: ref.PayloadDigest,
			ActivityGeneration: ref.ActivityGeneration, FenceKind: fenceKind, Fence: fence,
			CausationID: ref.CausationID,
		})
	}
	switch payload := intent.payload.(type) {
	case phaseRunPayload:
		if intent.kind == IntentRetryPhase {
			fact.RetryPhaseRunID = payload.phaseRunID
		}
	case runtimeRunRetryPayload:
		fact.RetryPhaseRunID = payload.phaseRunID
		fact.RetryRuntimeRunID = payload.runtimeRunID
	case reconcilePayload:
		fact.ReconciliationOperationID = payload.operationID
		fact.ReconciliationFence = payload.fence
	}
	fact.CanonicalDigest = MandatoryAuditFactDigest(fact)
	return fact
}

func aggregateStatus(aggregate *taskAggregate) TaskStatus {
	if aggregate == nil {
		return 0
	}
	return aggregate.status
}

func auditReason(intent intentValue) AuditReasonCode {
	if intent.authority.kind == AuthorityAdministrator {
		if intent.authority.reason == AdministratorReasonSafety {
			return AuditReasonAdministratorSafety
		}
		return AuditReasonAdministratorRecovery
	}
	switch intent.kind {
	case IntentRetryPhase, IntentRetryRuntimeRun:
		return AuditReasonRetry
	case IntentBeginManualEdit:
		return AuditReasonManualEdit
	case IntentReconcileEnactment:
		return AuditReasonReconciliation
	case IntentApplyOperationalFence:
		return AuditReasonRecoveryFence
	case IntentCancelTask:
		if payload, ok := intent.payload.(cancelPayload); ok && payload.reason == CancelReasonUserRequested {
			return AuditReasonUserRequested
		}
		return AuditReasonAdministratorSafety
	default:
		return AuditReasonNone
	}
}

func validMandatoryAuditFact(fact AuditFactRef, decision TransitionDecision) bool {
	if fact.SchemaVersion != AuditSchemaV1 || fact.IntegrityVersion != AuditIntegrityV1 ||
		fact.OwningModule != AuditModuleTaskOrchestration || fact.Result != AuditAccepted ||
		!validOpaqueID(fact.AuditFactID.value) || fact.DecisionID != decision.DecisionID ||
		fact.TaskID != decision.TaskProjection.TaskID ||
		fact.IdempotencyDecisionRequestID != decision.DecisionRequestID ||
		intentKindName(fact.Action) == "" || authorityKindName(fact.AuthorityKind) == "" ||
		!validOpaqueID(fact.AuthorityID.value) || fact.AuthorizationGeneration == 0 ||
		fact.BeforeTaskRevision != decision.PreviousTaskRevision ||
		fact.AfterTaskRevision != decision.AcceptedTaskRevision ||
		fact.AfterStatus != decision.TaskProjection.Status ||
		fact.AfterActivityGeneration != decision.TaskProjection.ActivityGeneration ||
		fact.AfterSafetyEpoch != decision.TaskProjection.SafetyEpoch ||
		fact.BeforeSafetyEpoch == 0 || fact.AfterSafetyEpoch == 0 ||
		fact.OccurredAt.IsZero() || !fact.RecordedAt.Equal(decision.CommittedAt) ||
		fact.SourceClock != AuditSourceTaskOrchestrationClock ||
		fact.CanonicalDigest == (AuditFactDigest{}) ||
		fact.CanonicalDigest != MandatoryAuditFactDigest(fact) ||
		len(fact.EvidenceRefs) != len(decision.AcceptedEvidenceRefs) ||
		len(fact.EnactmentRefs) != len(decision.EnactmentRefs) {
		return false
	}
	if fact.AuthorityKind == AuthorityAdministrator {
		if administratorReasonName(fact.AuthorityReason) == "" || fact.ReasonCode == AuditReasonNone {
			return false
		}
	} else if fact.AuthorityReason != 0 {
		return false
	}
	for index, evidence := range fact.EvidenceRefs {
		if evidence != decision.AcceptedEvidenceRefs[index] {
			return false
		}
	}
	for index, enactment := range fact.EnactmentRefs {
		decisionRef := decision.EnactmentRefs[index]
		fenceKind, fence := postgresFenceValue(decisionRef.Fence)
		if enactment.OperationID != decisionRef.OperationID || enactment.Kind != decisionRef.Kind ||
			enactment.PayloadDigest != decisionRef.PayloadDigest ||
			enactment.ActivityGeneration != decisionRef.ActivityGeneration ||
			enactment.FenceKind != fenceKind || enactment.Fence != fence ||
			enactment.CausationID != decisionRef.CausationID {
			return false
		}
	}
	return true
}
