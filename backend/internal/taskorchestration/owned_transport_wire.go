package taskorchestration

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"time"
)

type OwnedTransportWireErrorCode uint8

const (
	OwnedTransportWireInvalidEnvelope OwnedTransportWireErrorCode = iota + 1
	OwnedTransportWireUnsupportedVersion
)

// OwnedTransportWireError deliberately omits parser input and causes.
type OwnedTransportWireError struct {
	code OwnedTransportWireErrorCode
}

func (err *OwnedTransportWireError) Error() string {
	if err != nil && err.code == OwnedTransportWireUnsupportedVersion {
		return "owned transport wire version is unsupported"
	}
	return "owned transport wire envelope is invalid"
}

func (err *OwnedTransportWireError) Code() OwnedTransportWireErrorCode {
	if err == nil {
		return OwnedTransportWireInvalidEnvelope
	}
	return err.code
}

type ownedTransportAuthorityWire struct {
	Kind       string                  `json:"kind"`
	ID         string                  `json:"id"`
	Generation AuthorizationGeneration `json:"generation"`
}

type ownedTransportFenceWire struct {
	Kind  string `json:"kind"`
	Value uint64 `json:"value"`
}

type ownedTransportPrerequisitesWire struct {
	TaskRevision        TaskRevision `json:"task_revision"`
	AcceptedEvidenceIDs []string     `json:"accepted_evidence_ids"`
}

type ownedTransportRequestWire struct {
	SchemaVersion      string                          `json:"schema_version"`
	Authorization      ownedTransportAuthorityWire     `json:"authorization"`
	Deadline           string                          `json:"deadline"`
	OperationID        string                          `json:"operation_id"`
	DecisionID         string                          `json:"decision_id"`
	TaskID             string                          `json:"task_id"`
	PhaseRunID         string                          `json:"phase_run_id,omitempty"`
	RuntimeRunID       string                          `json:"runtime_run_id,omitempty"`
	Kind               string                          `json:"kind"`
	PayloadDigest      string                          `json:"payload_digest"`
	ActivityGeneration ActivityGeneration              `json:"activity_generation"`
	SafetyEpoch        SafetyEpoch                     `json:"safety_epoch"`
	Fence              ownedTransportFenceWire         `json:"fence"`
	CausationID        string                          `json:"causation_id"`
	Prerequisites      ownedTransportPrerequisitesWire `json:"prerequisites"`
}

type ownedTransportResponseWire struct {
	SchemaVersion  string `json:"schema_version"`
	OperationID    string `json:"operation_id"`
	Outcome        string `json:"outcome"`
	ResultDigest   string `json:"result_digest,omitempty"`
	Duplicate      bool   `json:"duplicate,omitempty"`
	RetryAt        string `json:"retry_at,omitempty"`
	DeferralReason string `json:"deferral_reason,omitempty"`
}

func EncodeOwnedTransportRequest(request OwnedTransportRequest) ([]byte, error) {
	if !validOwnedTransportRequest(request) {
		return nil, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	evidenceIDs := make([]string, 0, len(request.Prerequisites.AcceptedEvidenceIDs))
	for _, evidenceID := range request.Prerequisites.AcceptedEvidenceIDs {
		evidenceIDs = append(evidenceIDs, evidenceID.value)
	}
	sort.Strings(evidenceIDs)
	wire := ownedTransportRequestWire{
		SchemaVersion: ownedTransportVersionName(request.Version),
		Authorization: ownedTransportAuthorityWire{
			Kind: "worker", ID: request.Authority.value.id.value,
			Generation: request.Authority.value.generation,
		},
		Deadline:    request.Deadline.Format(time.RFC3339Nano),
		OperationID: request.OperationID.value, DecisionID: request.DecisionID.value,
		TaskID: request.TaskID.value, PhaseRunID: request.PhaseRunID.value,
		RuntimeRunID: request.RuntimeRunID.value, Kind: enactmentKindName(request.Kind),
		PayloadDigest:      request.PayloadDigest.String(),
		ActivityGeneration: request.ActivityGeneration, SafetyEpoch: request.SafetyEpoch,
		Fence: ownedTransportFenceWire{
			Kind: enactmentFenceKindName(request.FenceKind), Value: request.Fence,
		},
		CausationID: request.CausationID.value,
		Prerequisites: ownedTransportPrerequisitesWire{
			TaskRevision: request.Prerequisites.TaskRevision, AcceptedEvidenceIDs: evidenceIDs,
		},
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	return encoded, nil
}

func DecodeOwnedTransportRequest(encoded []byte) (OwnedTransportRequest, error) {
	var wire ownedTransportRequestWire
	if !decodeOwnedTransportWire(encoded, &wire) {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	version, ok := parseOwnedTransportVersion(wire.SchemaVersion)
	if !ok {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireUnsupportedVersion}
	}
	if wire.Authorization.Kind != "worker" {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	authorityID, err := NewAuthorityID(wire.Authorization.ID)
	if err != nil {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	payloadDigest, err := ParseEnactmentPayloadDigest(wire.PayloadDigest)
	if err != nil {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	deadline, err := time.Parse(time.RFC3339Nano, wire.Deadline)
	if err != nil {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	request := OwnedTransportRequest{
		Version:     version,
		Authority:   NewWorkerAuthority(authorityID, wire.Authorization.Generation),
		Deadline:    deadline.UTC(),
		OperationID: OperationID{value: wire.OperationID}, DecisionID: DecisionID{value: wire.DecisionID},
		TaskID: TaskID{value: wire.TaskID}, PhaseRunID: PhaseRunID{value: wire.PhaseRunID},
		RuntimeRunID: RuntimeRunID{value: wire.RuntimeRunID}, Kind: parseEnactmentKind(wire.Kind),
		PayloadDigest: payloadDigest, ActivityGeneration: wire.ActivityGeneration,
		SafetyEpoch: wire.SafetyEpoch, FenceKind: parseEnactmentFenceKind(wire.Fence.Kind),
		Fence: wire.Fence.Value, CausationID: CausationID{value: wire.CausationID},
		Prerequisites: DeliveryPrerequisites{TaskRevision: wire.Prerequisites.TaskRevision},
	}
	request.Prerequisites.AcceptedEvidenceIDs = make([]EvidenceID, len(wire.Prerequisites.AcceptedEvidenceIDs))
	for index, evidenceID := range wire.Prerequisites.AcceptedEvidenceIDs {
		request.Prerequisites.AcceptedEvidenceIDs[index] = EvidenceID{value: evidenceID}
	}
	if !validOwnedTransportRequest(request) {
		return OwnedTransportRequest{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	return request, nil
}

func EncodeOwnedTransportResponse(response OwnedTransportResponse) ([]byte, error) {
	if !validOwnedTransportResponse(response) {
		return nil, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	wire := ownedTransportResponseWire{
		SchemaVersion: ownedTransportVersionName(response.Version),
		OperationID:   response.OperationID.value, Outcome: ownedTransportOutcomeName(response.Outcome),
		Duplicate:      response.Duplicate,
		DeferralReason: ownedTransportDeferralReasonName(response.DeferralReason),
	}
	if response.ResultDigest != (DeliveryResultDigest{}) {
		wire.ResultDigest = response.ResultDigest.String()
	}
	if !response.RetryAt.IsZero() {
		wire.RetryAt = response.RetryAt.UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	return encoded, nil
}

func DecodeOwnedTransportResponse(encoded []byte) (OwnedTransportResponse, error) {
	var wire ownedTransportResponseWire
	if !decodeOwnedTransportWire(encoded, &wire) {
		return OwnedTransportResponse{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	version, ok := parseOwnedTransportVersion(wire.SchemaVersion)
	if !ok {
		return OwnedTransportResponse{}, &OwnedTransportWireError{code: OwnedTransportWireUnsupportedVersion}
	}
	response := OwnedTransportResponse{
		Version: version, OperationID: OperationID{value: wire.OperationID},
		Outcome: parseOwnedTransportOutcome(wire.Outcome), Duplicate: wire.Duplicate,
		DeferralReason: parseOwnedTransportDeferralReason(wire.DeferralReason),
	}
	if wire.ResultDigest != "" {
		resultDigest, err := ParseDeliveryResultDigest(wire.ResultDigest)
		if err != nil {
			return OwnedTransportResponse{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
		}
		response.ResultDigest = resultDigest
	}
	if wire.RetryAt != "" {
		retryAt, err := time.Parse(time.RFC3339Nano, wire.RetryAt)
		if err != nil {
			return OwnedTransportResponse{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
		}
		response.RetryAt = retryAt.UTC()
	}
	if !validOwnedTransportResponse(response) {
		return OwnedTransportResponse{}, &OwnedTransportWireError{code: OwnedTransportWireInvalidEnvelope}
	}
	return response, nil
}

func decodeOwnedTransportWire(encoded []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func validOwnedTransportResponse(response OwnedTransportResponse) bool {
	if response.Version != OwnedTransportV1 || !validOpaqueID(response.OperationID.value) ||
		ownedTransportOutcomeName(response.Outcome) == "" {
		return false
	}
	switch response.Outcome {
	case OwnedTransportAccepted:
		return response.ResultDigest != (DeliveryResultDigest{}) && response.RetryAt.IsZero() &&
			response.DeferralReason == 0
	case OwnedTransportBackpressured:
		return response.ResultDigest == (DeliveryResultDigest{}) && !response.RetryAt.IsZero() &&
			response.DeferralReason == 0
	case OwnedTransportDeferred:
		return response.ResultDigest == (DeliveryResultDigest{}) && !response.RetryAt.IsZero() &&
			response.DeferralReason == OwnedTransportPrerequisiteDeferred
	default:
		return response.ResultDigest == (DeliveryResultDigest{}) && response.RetryAt.IsZero() &&
			response.DeferralReason == 0
	}
}

func ownedTransportVersionName(version OwnedTransportVersion) string {
	if version == OwnedTransportV1 {
		return "1.0"
	}
	return ""
}

func parseOwnedTransportVersion(value string) (OwnedTransportVersion, bool) {
	return OwnedTransportV1, value == "1.0"
}

func enactmentKindName(kind EnactmentKind) string {
	switch kind {
	case EnactmentRuntimeExecution:
		return "runtime_execution"
	case EnactmentTaskWorkspaceLifecycle:
		return "task_workspace_lifecycle"
	case EnactmentArtifactPublication:
		return "artifact_publication"
	case EnactmentScheduling:
		return "scheduling"
	case EnactmentUsageAccounting:
		return "usage_accounting"
	case EnactmentPresentConfirmationGate:
		return "present_confirmation_gate"
	default:
		return ""
	}
}

func parseEnactmentKind(value string) EnactmentKind {
	for kind := EnactmentRuntimeExecution; kind <= EnactmentPresentConfirmationGate; kind++ {
		if enactmentKindName(kind) == value {
			return kind
		}
	}
	return 0
}

func enactmentFenceKindName(kind EnactmentFenceKind) string {
	switch kind {
	case EnactmentFenceRuntimeExecution:
		return "runtime_execution"
	case EnactmentFenceTaskWorkspaceLifecycle:
		return "task_workspace_lifecycle"
	case EnactmentFenceArtifactPublication:
		return "artifact_publication"
	case EnactmentFenceScheduling:
		return "scheduling"
	case EnactmentFenceUsageAccounting:
		return "usage_accounting"
	case EnactmentFenceConfirmation:
		return "confirmation"
	default:
		return ""
	}
}

func parseEnactmentFenceKind(value string) EnactmentFenceKind {
	for kind := EnactmentFenceRuntimeExecution; kind <= EnactmentFenceConfirmation; kind++ {
		if enactmentFenceKindName(kind) == value {
			return kind
		}
	}
	return 0
}

func ownedTransportOutcomeName(outcome OwnedTransportOutcome) string {
	switch outcome {
	case OwnedTransportAccepted:
		return "accepted"
	case OwnedTransportIntegrityConflict:
		return "integrity_conflict"
	case OwnedTransportUnknown:
		return "unknown"
	case OwnedTransportUnsupportedVersion:
		return "unsupported_version"
	case OwnedTransportUnauthorized:
		return "unauthorized"
	case OwnedTransportPoisoned:
		return "poisoned"
	case OwnedTransportBackpressured:
		return "backpressured"
	case OwnedTransportSuperseded:
		return "superseded"
	case OwnedTransportDeferred:
		return "deferred"
	default:
		return ""
	}
}

func parseOwnedTransportOutcome(value string) OwnedTransportOutcome {
	for outcome := OwnedTransportAccepted; outcome <= OwnedTransportDeferred; outcome++ {
		if ownedTransportOutcomeName(outcome) == value {
			return outcome
		}
	}
	return 0
}

func ownedTransportDeferralReasonName(reason OwnedTransportDeferralReason) string {
	if reason == OwnedTransportPrerequisiteDeferred {
		return "prerequisite"
	}
	return ""
}

func parseOwnedTransportDeferralReason(value string) OwnedTransportDeferralReason {
	if value == "prerequisite" {
		return OwnedTransportPrerequisiteDeferred
	}
	return 0
}
