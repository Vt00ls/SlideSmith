package runtimeexecution

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	postgresMandatoryAuditIntegrityVersion uint16 = 1
	postgresMandatoryAuditOwningModule            = "runtime_execution"
	postgresMandatoryAuditSourceClock             = "platform_control_plane"
	postgresMandatoryAuditDomain                  = "slidesmith.runtime-execution.mandatory-audit/v1\n"
)

type postgresMandatoryAuditAction uint8

const (
	postgresAuditStartAccepted postgresMandatoryAuditAction = iota + 1
	postgresAuditCancelAccepted
	postgresAuditReconciliationRequired
)

type postgresMandatoryAuditResult uint8

const (
	postgresAuditAccepted postgresMandatoryAuditResult = iota + 1
)

// postgresMandatoryAuditState is the complete, content-free authoritative
// AuditFact persisted with a protected Runtime Execution decision.
type postgresMandatoryAuditState struct {
	AuditFactID               string                       `json:"audit_fact_id"`
	SchemaVersion             SchemaVersion                `json:"schema_version"`
	IntegrityVersion          uint16                       `json:"integrity_version"`
	OwningModule              string                       `json:"owning_module"`
	DecisionID                string                       `json:"decision_id"`
	RuntimeRunID              string                       `json:"runtime_run_id"`
	OperationID               string                       `json:"operation_id"`
	RequestDigest             string                       `json:"request_digest"`
	AuthorityKind             AuthorityKind                `json:"authority_kind"`
	AuthorityID               string                       `json:"authority_id"`
	AuthorityGeneration       AuthorizationGeneration      `json:"authority_generation"`
	Action                    postgresMandatoryAuditAction `json:"action"`
	Result                    postgresMandatoryAuditResult `json:"result"`
	ReasonCode                uint8                        `json:"reason_code"`
	BeforeRevision            RuntimeRevision              `json:"before_revision"`
	AfterRevision             RuntimeRevision              `json:"after_revision"`
	BeforeState               RuntimeState                 `json:"before_state"`
	AfterState                RuntimeState                 `json:"after_state"`
	BeforeOperationGeneration OperationGeneration          `json:"before_operation_generation"`
	AfterOperationGeneration  OperationGeneration          `json:"after_operation_generation"`
	BeforeRuntimeFence        RuntimeFence                 `json:"before_runtime_fence"`
	AfterRuntimeFence         RuntimeFence                 `json:"after_runtime_fence"`
	PolicyEpoch               uint64                       `json:"policy_epoch"`
	AuthorizationEpoch        AuthorizationGeneration      `json:"authorization_epoch"`
	RecoveryGeneration        uint64                       `json:"recovery_generation"`
	BeforeSafetyEpoch         ReleaseSafetyEpoch           `json:"before_safety_epoch"`
	AfterSafetyEpoch          ReleaseSafetyEpoch           `json:"after_safety_epoch"`
	OccurredAt                string                       `json:"occurred_at"`
	RecordedAt                string                       `json:"recorded_at"`
	SourceClockID             string                       `json:"source_clock_id"`
	EvidenceRootID            string                       `json:"evidence_root_id"`
	EvidenceRootDigest        string                       `json:"evidence_root_digest"`
	ApprovalReference         string                       `json:"approval_reference"`
	IdempotencyReference      string                       `json:"idempotency_reference"`
	RetryDisposition          RetryDisposition             `json:"retry_disposition"`
	ReconciliationDisposition ReconciliationDisposition    `json:"reconciliation_disposition"`
	SupersedingAuditFactID    string                       `json:"superseding_audit_fact_id"`
	SafeErrorCode             ErrorCode                    `json:"safe_error_code"`
}

type postgresMandatoryAuditInput struct {
	AuditFactID               string
	Action                    postgresMandatoryAuditAction
	ReasonCode                uint8
	Decision                  RuntimeDecisionFact
	RuntimeRunID              RuntimeRunID
	RequestDigest             Digest
	Authority                 RuntimeAuthority
	BeforeState               RuntimeState
	AfterState                RuntimeState
	BeforeOperationGeneration OperationGeneration
	AfterOperationGeneration  OperationGeneration
	BeforeRuntimeFence        RuntimeFence
	AfterRuntimeFence         RuntimeFence
	PolicyEpoch               uint64
	BeforeSafetyEpoch         ReleaseSafetyEpoch
	AfterSafetyEpoch          ReleaseSafetyEpoch
	OccurredAt                time.Time
	RecordedAt                time.Time
	EvidenceRoot              EvidenceRootSnapshot
}

func newPostgresMandatoryAuditState(input postgresMandatoryAuditInput) postgresMandatoryAuditState {
	occurredAt := postgresTimestamp(input.OccurredAt)
	recordedAt := postgresTimestamp(input.RecordedAt)
	return postgresMandatoryAuditState{
		AuditFactID: input.AuditFactID, SchemaVersion: SchemaV1,
		IntegrityVersion: postgresMandatoryAuditIntegrityVersion,
		OwningModule:     postgresMandatoryAuditOwningModule,
		DecisionID:       input.Decision.DecisionID.String(), RuntimeRunID: input.RuntimeRunID.String(),
		OperationID: input.Decision.OperationID.String(), RequestDigest: input.RequestDigest.String(),
		AuthorityKind: input.Authority.kind, AuthorityID: input.Authority.id.String(),
		AuthorityGeneration: input.Authority.generation,
		Action:              input.Action, Result: postgresAuditAccepted, ReasonCode: input.ReasonCode,
		BeforeRevision: input.Decision.PreviousRuntimeRevision,
		AfterRevision:  input.Decision.ResultingRuntimeRevision,
		BeforeState:    input.BeforeState, AfterState: input.AfterState,
		BeforeOperationGeneration: input.BeforeOperationGeneration,
		AfterOperationGeneration:  input.AfterOperationGeneration,
		BeforeRuntimeFence:        input.BeforeRuntimeFence,
		AfterRuntimeFence:         input.AfterRuntimeFence,
		PolicyEpoch:               input.PolicyEpoch, AuthorizationEpoch: input.Authority.generation,
		BeforeSafetyEpoch: input.BeforeSafetyEpoch, AfterSafetyEpoch: input.AfterSafetyEpoch,
		OccurredAt: occurredAt.Format(canonicalTimeFormat), RecordedAt: recordedAt.Format(canonicalTimeFormat),
		SourceClockID:        postgresMandatoryAuditSourceClock,
		EvidenceRootID:       input.EvidenceRoot.EvidenceRootID.String(),
		EvidenceRootDigest:   input.EvidenceRoot.Digest.String(),
		IdempotencyReference: input.Decision.OperationID.String(),
		RetryDisposition:     input.Decision.Retry, ReconciliationDisposition: input.Decision.Reconciliation,
	}
}

func (state postgresMandatoryAuditState) encode() ([]byte, error) {
	return json.Marshal(state)
}

func (state postgresMandatoryAuditState) canonicalDigest() (Digest, error) {
	encoded, err := state.encode()
	if err != nil {
		return Digest{}, err
	}
	return digestBytes(append([]byte(postgresMandatoryAuditDomain), encoded...)), nil
}

func decodePostgresMandatoryAuditState(encoded []byte) (postgresMandatoryAuditState, error) {
	var state postgresMandatoryAuditState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return postgresMandatoryAuditState{}, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil || !validPostgresMandatoryAuditState(state) {
		return postgresMandatoryAuditState{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return state, nil
}

func validPostgresMandatoryAuditState(state postgresMandatoryAuditState) bool {
	if !validOpaqueID(state.AuditFactID) || state.SchemaVersion != SchemaV1 ||
		state.IntegrityVersion != postgresMandatoryAuditIntegrityVersion ||
		state.OwningModule != postgresMandatoryAuditOwningModule ||
		!validOpaqueID(state.DecisionID) || !validOpaqueID(state.RuntimeRunID) ||
		!validOpaqueID(state.OperationID) || !validDigestText(state.RequestDigest) ||
		!validAuthority(RuntimeAuthority{id: AuthorityID{value: state.AuthorityID}, generation: state.AuthorityGeneration, kind: state.AuthorityKind}) ||
		state.Action < postgresAuditStartAccepted || state.Action > postgresAuditReconciliationRequired ||
		state.Result != postgresAuditAccepted || state.BeforeRevision == 0 || state.AfterRevision == 0 ||
		!knownRuntimeState(state.BeforeState) || !knownRuntimeState(state.AfterState) ||
		state.BeforeOperationGeneration == 0 || state.AfterOperationGeneration == 0 ||
		state.BeforeRuntimeFence == 0 || state.AfterRuntimeFence == 0 ||
		state.AuthorizationEpoch != state.AuthorityGeneration ||
		state.BeforeSafetyEpoch == 0 || state.AfterSafetyEpoch == 0 ||
		state.SourceClockID != postgresMandatoryAuditSourceClock ||
		state.IdempotencyReference != state.OperationID ||
		state.SafeErrorCode != 0 ||
		state.RetryDisposition < RetryNever || state.RetryDisposition > RetryAfterDependency ||
		state.ReconciliationDisposition < ReconciliationNotRequired || state.ReconciliationDisposition > ReconciliationRequired {
		return false
	}
	switch state.Action {
	case postgresAuditStartAccepted:
		if state.ReasonCode != 0 {
			return false
		}
	case postgresAuditCancelAccepted:
		if state.ReasonCode < uint8(CancellationUserRequested) || state.ReasonCode > uint8(CancellationAdministratorRequested) {
			return false
		}
	case postgresAuditReconciliationRequired:
		if state.ReasonCode < uint8(ReconciliationTransportAmbiguous) || state.ReasonCode > uint8(ReconciliationProjectionDelivery) {
			return false
		}
	}
	occurredAt, err := time.Parse(canonicalTimeFormat, state.OccurredAt)
	if err != nil {
		return false
	}
	recordedAt, err := time.Parse(canonicalTimeFormat, state.RecordedAt)
	if err != nil || recordedAt.Before(occurredAt) {
		return false
	}
	if state.EvidenceRootID == "" {
		return state.EvidenceRootDigest == (Digest{}).String()
	}
	return validOpaqueID(state.EvidenceRootID) && validDigestText(state.EvidenceRootDigest)
}

func validDigestText(value string) bool {
	if len(value) != hex.EncodedLen(len(Digest{})) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == len(Digest{})
}
