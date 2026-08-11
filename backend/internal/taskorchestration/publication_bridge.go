package taskorchestration

// Publication bridge (child SPEC #109 / C05-06). This file delivers the
// Task Orchestration side of the owned publication bridge: the COMPLETE
// canonical C05 publication request is fixed as a typed
// PublicationRequestBinding at outbox-commit time (Task, Phase Run,
// operation, activity, expected head/revision, parent, contract, evidence
// and generation/fence bindings), and an owned adapter delivers that exact
// committed binding through the C05 owned transport. The adapter can never
// assemble, overwrite or re-bind request fields at delivery, callback, test
// or reconciliation time; every ambiguous result returns to the ORIGINAL
// OperationID and never creates a new Artifact Version, parent, head or
// Task retry. Task Orchestration keeps deciding Phase Run and Task
// progression from the typed activation/rejection evidence C05 returns; the
// bridge never advances a Task.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"time"
)

// PublicationSchemaVersion identifies the canonical C05 request encoding
// understood by the bridge. The high 16 bits are the major version; the low
// 16 bits are the minor version. Unknown major versions fail closed.
type PublicationSchemaVersion uint32

// PublicationSchemaV1 is the initial C05 publication request schema.
const PublicationSchemaV1 PublicationSchemaVersion = 1 << 16

func (version PublicationSchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }

type (
	// PublicationRequestID is the opaque identity of one committed C05
	// publication request.
	PublicationRequestID struct{ value string }
	// PublicationContractID is the opaque identity of the pinned
	// publication contract that fixes required channels and aggregation
	// rules. It is pinned by the Task and never floats.
	PublicationContractID struct{ value string }
	// PublicationMemberSlotID is the stable per-candidate identity of one
	// member slot, shared between the Task Orchestration request, the
	// Durable Object staging references and the C05 canonical manifest.
	PublicationMemberSlotID struct{ value string }
	// PublicationContentID is the opaque Durable Object content identity
	// of one staged member.
	PublicationContentID struct{ value string }
	// PublicationCapabilityID is the opaque identity of one Durable Object
	// verified-content capability required for one member slot.
	PublicationCapabilityID struct{ value string }
	// PublicationAdapterID is the opaque identity of one registered
	// Durable Object adapter.
	PublicationAdapterID struct{ value string }
)

// PublicationKind is the closed publication kind fixed by the Task.
type PublicationKind uint8

const (
	// PublicationKindFirstGeneration publishes the first Artifact Version
	// of the Task; it has no parent.
	PublicationKindFirstGeneration PublicationKind = iota + 1
	// PublicationKindManualEdit publishes a child of an exact activated
	// parent Artifact Version selected by the Task Orchestration.
	PublicationKindManualEdit
)

func validPublicationOpaqueID(value string) bool {
	return validOpaqueID(value)
}

func (id PublicationRequestID) String() string    { return id.value }
func (id PublicationContractID) String() string   { return id.value }
func (id PublicationMemberSlotID) String() string { return id.value }
func (id PublicationContentID) String() string    { return id.value }
func (id PublicationCapabilityID) String() string { return id.value }
func (id PublicationAdapterID) String() string    { return id.value }

// NewPublicationRequestID builds one opaque publication request identity.
func NewPublicationRequestID(value string) (PublicationRequestID, error) {
	if !validPublicationOpaqueID(value) {
		return PublicationRequestID{}, invalidIntentError()
	}
	return PublicationRequestID{value: value}, nil
}

// NewPublicationContractID builds one opaque pinned publication contract
// identity.
func NewPublicationContractID(value string) (PublicationContractID, error) {
	if !validPublicationOpaqueID(value) {
		return PublicationContractID{}, invalidIntentError()
	}
	return PublicationContractID{value: value}, nil
}

// NewPublicationMemberSlotID builds one opaque member slot identity.
func NewPublicationMemberSlotID(value string) (PublicationMemberSlotID, error) {
	if !validPublicationOpaqueID(value) {
		return PublicationMemberSlotID{}, invalidIntentError()
	}
	return PublicationMemberSlotID{value: value}, nil
}

// NewPublicationContentID builds one opaque Durable Object content
// identity.
func NewPublicationContentID(value string) (PublicationContentID, error) {
	if !validPublicationOpaqueID(value) {
		return PublicationContentID{}, invalidIntentError()
	}
	return PublicationContentID{value: value}, nil
}

// NewPublicationCapabilityID builds one opaque verified-content capability
// identity.
func NewPublicationCapabilityID(value string) (PublicationCapabilityID, error) {
	if !validPublicationOpaqueID(value) {
		return PublicationCapabilityID{}, invalidIntentError()
	}
	return PublicationCapabilityID{value: value}, nil
}

// NewPublicationAdapterID builds one opaque Durable Object adapter
// identity.
func NewPublicationAdapterID(value string) (PublicationAdapterID, error) {
	if !validPublicationOpaqueID(value) {
		return PublicationAdapterID{}, invalidIntentError()
	}
	return PublicationAdapterID{value: value}, nil
}

func publicationKindName(kind PublicationKind) string {
	switch kind {
	case PublicationKindFirstGeneration:
		return "first_generation"
	case PublicationKindManualEdit:
		return "manual_edit"
	default:
		return ""
	}
}

// PublicationMemberKind is the closed registered member kind.
type PublicationMemberKind uint8

const (
	PublicationMemberDeck PublicationMemberKind = iota + 1
	PublicationMemberPreview
	PublicationMemberPlan
	PublicationMemberValidationReport
)

func publicationMemberKindName(kind PublicationMemberKind) string {
	switch kind {
	case PublicationMemberDeck:
		return "deck"
	case PublicationMemberPreview:
		return "preview"
	case PublicationMemberPlan:
		return "plan"
	case PublicationMemberValidationReport:
		return "validation_report"
	default:
		return ""
	}
}

// PublicationChannel is the closed output channel kind.
type PublicationChannel uint8

const (
	PublicationChannelDeck PublicationChannel = iota + 1
	PublicationChannelPreview
	PublicationChannelPlan
	PublicationChannelValidationReport
)

func publicationChannelName(channel PublicationChannel) string {
	switch channel {
	case PublicationChannelDeck:
		return "deck"
	case PublicationChannelPreview:
		return "preview"
	case PublicationChannelPlan:
		return "plan"
	case PublicationChannelValidationReport:
		return "validation_report"
	default:
		return ""
	}
}

// PublicationStagingPurpose is the closed purpose of one typed staging
// reference.
type PublicationStagingPurpose uint8

const (
	PublicationStagingPurposeMember PublicationStagingPurpose = iota + 1
)

func publicationStagingPurposeName(purpose PublicationStagingPurpose) string {
	switch purpose {
	case PublicationStagingPurposeMember:
		return "publication_member"
	default:
		return ""
	}
}

// PublicationMemberSpec is one semantic member declaration of the
// candidate, pinned by the Task Orchestration at commit time.
type PublicationMemberSpec struct {
	Slot          PublicationMemberSlotID
	Kind          PublicationMemberKind
	LogicalName   string
	MediaType     string
	Size          uint64
	ContentDigest EvidenceDigest
}

// PublicationStagingReference is one typed Durable Object prepare receipt
// for one member slot, pinned by the Task Orchestration at commit time. It
// is never a materialization locator.
type PublicationStagingReference struct {
	MemberSlot         PublicationMemberSlotID
	ContentID          PublicationContentID
	ContentDigest      EvidenceDigest
	Size               uint64
	Purpose            PublicationStagingPurpose
	PhysicalGeneration uint64
	AdapterID          PublicationAdapterID
}

// PublicationRuntimeEvidenceRef pins one required Runtime Evidence for one
// declared output channel.
type PublicationRuntimeEvidenceRef struct {
	Channel    PublicationChannel
	EvidenceID EvidenceID
	Digest     EvidenceDigest
}

// PublicationEvidenceRef pins one opaque upstream evidence record.
type PublicationEvidenceRef struct {
	EvidenceID EvidenceID
	Digest     EvidenceDigest
}

// PublicationCapabilityRef pins the Durable Object verified-content
// capability required for one member slot.
type PublicationCapabilityRef struct {
	MemberSlot   PublicationMemberSlotID
	CapabilityID PublicationCapabilityID
	Digest       EvidenceDigest
}

// PublicationRequestSpec is the canonical payload of a C05 publication
// request: contract, kind, parent, members, staging references, required
// channels, and the pinned evidence/capability references. The Task pins it
// with the Task (through PinnedTaskStart) and the phase start commits it
// into the outbox with the phase's authoritative facts. It never contains a
// path, object key, bucket, mount, vendor, credential or locator.
type PublicationRequestSpec struct {
	ContractID            PublicationContractID
	Kind                  PublicationKind
	Parent                ArtifactVersionID
	Members               []PublicationMemberSpec
	Staging               []PublicationStagingReference
	RequiredChannels      []PublicationChannel
	RuntimeRefs           []PublicationRuntimeEvidenceRef
	ValidationRef         PublicationEvidenceRef
	C04CommitRef          PublicationEvidenceRef
	ContentCapabilityRefs []PublicationCapabilityRef
}

// PublicationRequestBinding is the COMPLETE canonical C05 publication
// request fixed at outbox-commit time. It binds the Task, Phase Run,
// operation, activity generation, expected stream revision/head, parent,
// contract, evidence references and generation/fence/safety-epoch facts
// together with the immutable spec. The canonical digest commits all of
// them; the owned adapter delivers this exact binding and can never
// assemble a request.
type PublicationRequestBinding struct {
	SchemaVersion          PublicationSchemaVersion
	RequestID              PublicationRequestID
	OperationID            OperationID
	TaskID                 TaskID
	PhaseRunID             PhaseRunID
	ActivityGeneration     ActivityGeneration
	Generation             ProducerGeneration
	Fence                  PublicationFence
	SafetyEpoch            SafetyEpoch
	ExpectedStreamRevision uint64
	ExpectedHead           ArtifactVersionID
	Spec                   PublicationRequestSpec
}

// NewPublicationRequestSpec builds the canonical payload of a C05
// publication request.
func NewPublicationRequestSpec(
	contractID PublicationContractID,
	kind PublicationKind,
	parent ArtifactVersionID,
	members []PublicationMemberSpec,
	staging []PublicationStagingReference,
	requiredChannels []PublicationChannel,
	runtimeRefs []PublicationRuntimeEvidenceRef,
	validationRef PublicationEvidenceRef,
	c04CommitRef PublicationEvidenceRef,
	contentCapabilityRefs []PublicationCapabilityRef,
) PublicationRequestSpec {
	return PublicationRequestSpec{
		ContractID: contractID, Kind: kind, Parent: parent,
		Members:          append([]PublicationMemberSpec(nil), members...),
		Staging:          append([]PublicationStagingReference(nil), staging...),
		RequiredChannels: append([]PublicationChannel(nil), requiredChannels...),
		RuntimeRefs:      append([]PublicationRuntimeEvidenceRef(nil), runtimeRefs...),
		ValidationRef:    validationRef, C04CommitRef: c04CommitRef,
		ContentCapabilityRefs: append([]PublicationCapabilityRef(nil), contentCapabilityRefs...),
	}
}

// CanonicalBytes deterministically encodes the complete canonical request.
// The adapter stores and delivers these exact bytes; a delivery that
// changes any binding produces a different digest and fails closed.
func (binding PublicationRequestBinding) CanonicalBytes() []byte {
	encoded, err := json.Marshal(bindingCanonical(binding))
	if err != nil {
		return nil
	}
	return encoded
}

// CanonicalDigest is the deterministic digest of the complete canonical
// request. It equals the C05 request's exact digest binding: the adapter
// never changes it between outbox-commit and delivery.
func (binding PublicationRequestBinding) CanonicalDigest() CanonicalRequestDigest {
	sum := sha256.Sum256(binding.CanonicalBytes())
	return CanonicalRequestDigest(sum)
}

func (binding PublicationRequestBinding) Valid() bool {
	if binding.SchemaVersion.Major() != PublicationSchemaV1.Major() ||
		!validOpaqueID(binding.RequestID.value) || !validOpaqueID(binding.OperationID.value) ||
		!validOpaqueID(binding.TaskID.value) || !validOpaqueID(binding.PhaseRunID.value) ||
		binding.ActivityGeneration == 0 || binding.Generation == 0 || binding.Fence == 0 ||
		binding.SafetyEpoch == 0 || publicationKindName(binding.Spec.Kind) == "" ||
		!validOpaqueID(binding.Spec.ContractID.value) {
		return false
	}
	if binding.Spec.Kind == PublicationKindFirstGeneration &&
		binding.Spec.Parent != (ArtifactVersionID{}) {
		return false
	}
	if binding.Spec.Kind == PublicationKindManualEdit &&
		!validOpaqueID(binding.Spec.Parent.value) {
		return false
	}
	if len(binding.Spec.Members) == 0 || len(binding.Spec.Staging) == 0 ||
		len(binding.Spec.RequiredChannels) == 0 || len(binding.Spec.RuntimeRefs) == 0 ||
		len(binding.Spec.ContentCapabilityRefs) == 0 ||
		!validOpaqueID(binding.Spec.ValidationRef.EvidenceID.value) ||
		!validOpaqueID(binding.Spec.C04CommitRef.EvidenceID.value) {
		return false
	}
	if len(binding.Spec.RequiredChannels) != len(binding.Spec.RuntimeRefs) ||
		len(binding.Spec.Members) != len(binding.Spec.Staging) ||
		len(binding.Spec.Members) != len(binding.Spec.ContentCapabilityRefs) {
		return false
	}
	slots := make(map[string]bool, len(binding.Spec.Members))
	for _, member := range binding.Spec.Members {
		if !validOpaqueID(member.Slot.value) || publicationMemberKindName(member.Kind) == "" ||
			member.LogicalName == "" || member.MediaType == "" || member.Size == 0 ||
			member.ContentDigest == (EvidenceDigest{}) {
			return false
		}
		if slots[member.Slot.value] {
			return false
		}
		slots[member.Slot.value] = true
	}
	staged := make(map[string]bool, len(binding.Spec.Staging))
	for _, ref := range binding.Spec.Staging {
		if !validOpaqueID(ref.MemberSlot.value) || !validOpaqueID(ref.ContentID.value) ||
			ref.ContentDigest == (EvidenceDigest{}) ||
			publicationStagingPurposeName(ref.Purpose) == "" ||
			!validOpaqueID(ref.AdapterID.value) || !slots[ref.MemberSlot.value] || staged[ref.MemberSlot.value] {
			return false
		}
		staged[ref.MemberSlot.value] = true
	}
	for slot := range slots {
		if !staged[slot] {
			return false
		}
	}
	for _, channel := range binding.Spec.RequiredChannels {
		if publicationChannelName(channel) == "" {
			return false
		}
	}
	for _, ref := range binding.Spec.RuntimeRefs {
		if publicationChannelName(ref.Channel) == "" || !validOpaqueID(ref.EvidenceID.value) ||
			ref.Digest == (EvidenceDigest{}) {
			return false
		}
	}
	capabilitySlots := make(map[string]bool, len(binding.Spec.ContentCapabilityRefs))
	for _, ref := range binding.Spec.ContentCapabilityRefs {
		if !validOpaqueID(ref.MemberSlot.value) || !validOpaqueID(ref.CapabilityID.value) ||
			ref.Digest == (EvidenceDigest{}) || !slots[ref.MemberSlot.value] ||
			capabilitySlots[ref.MemberSlot.value] {
			return false
		}
		capabilitySlots[ref.MemberSlot.value] = true
	}
	for slot := range slots {
		if !capabilitySlots[slot] {
			return false
		}
	}
	return true
}

// bindingCanonical deterministically encodes the binding as a sorted-key
// map so field order can never change the digest.
func bindingCanonical(binding PublicationRequestBinding) map[string]any {
	members := make([]map[string]any, 0, len(binding.Spec.Members))
	for _, member := range binding.Spec.Members {
		members = append(members, map[string]any{
			"slot":           member.Slot.value,
			"kind":           publicationMemberKindName(member.Kind),
			"logical_name":   member.LogicalName,
			"media_type":     member.MediaType,
			"size":           member.Size,
			"content_digest": digestString(member.ContentDigest),
		})
	}
	staging := make([]map[string]any, 0, len(binding.Spec.Staging))
	for _, ref := range binding.Spec.Staging {
		staging = append(staging, map[string]any{
			"member_slot":         ref.MemberSlot.value,
			"content_id":          ref.ContentID.value,
			"content_digest":      digestString(ref.ContentDigest),
			"size":                ref.Size,
			"purpose":             publicationStagingPurposeName(ref.Purpose),
			"physical_generation": ref.PhysicalGeneration,
			"adapter_id":          ref.AdapterID.value,
		})
	}
	channels := make([]string, 0, len(binding.Spec.RequiredChannels))
	for _, channel := range binding.Spec.RequiredChannels {
		channels = append(channels, publicationChannelName(channel))
	}
	runtimeRefs := make([]map[string]any, 0, len(binding.Spec.RuntimeRefs))
	for _, ref := range binding.Spec.RuntimeRefs {
		runtimeRefs = append(runtimeRefs, map[string]any{
			"channel":     publicationChannelName(ref.Channel),
			"evidence_id": ref.EvidenceID.value,
			"digest":      digestString(ref.Digest),
		})
	}
	capabilityRefs := make([]map[string]any, 0, len(binding.Spec.ContentCapabilityRefs))
	for _, ref := range binding.Spec.ContentCapabilityRefs {
		capabilityRefs = append(capabilityRefs, map[string]any{
			"member_slot":   ref.MemberSlot.value,
			"capability_id": ref.CapabilityID.value,
			"digest":        digestString(ref.Digest),
		})
	}
	return map[string]any{
		"schema_version":           uint32(binding.SchemaVersion),
		"request_id":               binding.RequestID.value,
		"operation_id":             binding.OperationID.value,
		"task_id":                  binding.TaskID.value,
		"phase_run_id":             binding.PhaseRunID.value,
		"activity_generation":      uint64(binding.ActivityGeneration),
		"generation":               uint64(binding.Generation),
		"fence":                    uint64(binding.Fence),
		"safety_epoch":             uint64(binding.SafetyEpoch),
		"expected_stream_revision": uint64(binding.ExpectedStreamRevision),
		"expected_head":            binding.ExpectedHead.value,
		"contract_id":              binding.Spec.ContractID.value,
		"kind":                     publicationKindName(binding.Spec.Kind),
		"parent":                   binding.Spec.Parent.value,
		"members":                  members,
		"staging":                  staging,
		"required_channels":        channels,
		"runtime_refs":             runtimeRefs,
		"validation_ref": map[string]any{
			"evidence_id": binding.Spec.ValidationRef.EvidenceID.value,
			"digest":      digestString(binding.Spec.ValidationRef.Digest),
		},
		"c04_commit_ref": map[string]any{
			"evidence_id": binding.Spec.C04CommitRef.EvidenceID.value,
			"digest":      digestString(binding.Spec.C04CommitRef.Digest),
		},
		"content_capability_refs": capabilityRefs,
	}
}

func digestString(digest EvidenceDigest) string { return digest.String() }

// PublicationDeliveryOutcome is the closed outcome of one bridge delivery.
type PublicationDeliveryOutcome uint8

const (
	// PublicationDelivered reports the owned transport accepted the exact
	// committed request (durable on the C05 side).
	PublicationDelivered PublicationDeliveryOutcome = iota + 1
	// PublicationReconciliationRequired reports the delivery may have
	// committed but the acknowledgement was lost; the caller must inspect/
	// reconcile the ORIGINAL OperationID.
	PublicationReconciliationRequired
	// PublicationIntegrityConflict reports a re-binding attempt under the
	// same OperationID with a different canonical request.
	PublicationIntegrityConflict
	// PublicationUnsupported reports an unknown request schema or an
	// unsupported envelope version.
	PublicationUnsupported
	// PublicationPoisoned reports a malformed or unauthorized request.
	PublicationPoisoned
	// PublicationUnknown reports the operation is not known to the
	// transport (used by inspection of never-delivered operations).
	PublicationUnknown
)

func publicationDeliveryOutcomeName(outcome PublicationDeliveryOutcome) string {
	switch outcome {
	case PublicationDelivered:
		return "delivered"
	case PublicationReconciliationRequired:
		return "reconciliation_required"
	case PublicationIntegrityConflict:
		return "integrity_conflict"
	case PublicationUnsupported:
		return "unsupported"
	case PublicationPoisoned:
		return "poisoned"
	case PublicationUnknown:
		return "unknown"
	default:
		return ""
	}
}

// PublicationDelivery is the content-free result of one bridge delivery or
// inspection. It reports only the ORIGINAL OperationID, the closed outcome,
// the committed request digest and a diagnostic time.
type PublicationDelivery struct {
	OperationID OperationID
	Outcome     PublicationDeliveryOutcome
	Digest      CanonicalRequestDigest
	Duplicate   bool
	OccurredAt  time.Time
}

// PublicationTransportPort is the narrow black-box port to the owned
// transport. The adapter passes the committed binding verbatim; the port
// never assembles a request.
type PublicationTransportPort interface {
	DeliverPublication(context.Context, PublicationRequestBinding) (PublicationDelivery, error)
	InspectPublication(context.Context, OperationID) (PublicationDelivery, error)
}

// PublicationBridgeAdapter delivers ONLY the exact committed
// PublicationRequestBinding through the owned transport. It validates the
// committed binding shape and then passes it verbatim; it can never
// assemble, overwrite or re-bind parent, manifest, prerequisite or
// authority fields at delivery, callback, test or reconciliation time.
type PublicationBridgeAdapter struct {
	port PublicationTransportPort
}

// NewPublicationBridgeAdapter builds the bridge adapter over an owned
// transport port.
func NewPublicationBridgeAdapter(port PublicationTransportPort) *PublicationBridgeAdapter {
	return &PublicationBridgeAdapter{port: port}
}

// DeliverPublication passes the committed binding verbatim to the owned
// transport.
func (adapter *PublicationBridgeAdapter) DeliverPublication(
	ctx context.Context,
	binding PublicationRequestBinding,
) (PublicationDelivery, error) {
	if adapter == nil || adapter.port == nil || !binding.Valid() ||
		binding.OperationID == (OperationID{}) {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorInvalidRequest)
	}
	return adapter.port.DeliverPublication(ctx, binding)
}

// InspectPublication inspects the ORIGINAL OperationID through the owned
// transport. It never delivers a new request.
func (adapter *PublicationBridgeAdapter) InspectPublication(
	ctx context.Context,
	operationID OperationID,
) (PublicationDelivery, error) {
	if adapter == nil || adapter.port == nil || operationID == (OperationID{}) {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorInvalidRequest)
	}
	return adapter.port.InspectPublication(ctx, operationID)
}

// PublicationBridgeErrorCode is the closed bridge error surface.
type PublicationBridgeErrorCode uint8

const (
	PublicationBridgeErrorInvalidRequest PublicationBridgeErrorCode = iota + 1
	PublicationBridgeErrorUnavailable
	PublicationBridgeErrorIntegrityConflict
	PublicationBridgeErrorReconciliationRequired
)

// PublicationBridgeError is the typed safe bridge error. It never carries a
// raw transport, persistence, or caller-supplied detail.
type PublicationBridgeError struct {
	code PublicationBridgeErrorCode
}

func (err *PublicationBridgeError) Error() string {
	if err == nil {
		return "publication bridge request is invalid"
	}
	switch err.code {
	case PublicationBridgeErrorUnavailable:
		return "publication bridge transport is unavailable"
	case PublicationBridgeErrorIntegrityConflict:
		return "publication bridge request integrity conflict"
	case PublicationBridgeErrorReconciliationRequired:
		return "publication bridge delivery requires reconciliation"
	default:
		return "publication bridge request is invalid"
	}
}

func (err *PublicationBridgeError) Code() PublicationBridgeErrorCode {
	if err == nil {
		return PublicationBridgeErrorInvalidRequest
	}
	return err.code
}

func newBridgeError(code PublicationBridgeErrorCode) *PublicationBridgeError {
	return &PublicationBridgeError{code: code}
}

// DeterministicPublicationTransport is the owned transport double for the
// bridge contract: it journals the exact committed binding keyed by the
// ORIGINAL OperationID, returns the same outcome on exact duplicate
// delivery, rejects a same-OperationID/different-request re-binding as an
// integrity conflict, and supports inspect by the original OperationID.
type DeterministicPublicationTransport struct {
	journal map[OperationID]deterministicPublicationRecord
	now     func() time.Time
}

type deterministicPublicationRecord struct {
	digest    CanonicalRequestDigest
	canonical []byte
	outcome   PublicationDeliveryOutcome
	occurred  time.Time
}

// NewDeterministicPublicationTransport builds the journaling transport
// double.
func NewDeterministicPublicationTransport(now func() time.Time) *DeterministicPublicationTransport {
	if now == nil {
		now = func() time.Time { return time.Unix(0, 0).UTC() }
	}
	return &DeterministicPublicationTransport{
		journal: make(map[OperationID]deterministicPublicationRecord),
		now:     now,
	}
}

// DeliverPublication journals the exact committed request. An exact
// duplicate delivery returns the same outcome; a different request under
// the same OperationID is a durable integrity conflict.
func (transport *DeterministicPublicationTransport) DeliverPublication(
	_ context.Context,
	binding PublicationRequestBinding,
) (PublicationDelivery, error) {
	if transport == nil || !binding.Valid() {
		return PublicationDelivery{OperationID: binding.OperationID, Outcome: PublicationPoisoned}, nil
	}
	digest := binding.CanonicalDigest()
	canonical := binding.CanonicalBytes()
	now := transport.now().UTC()
	if existing, ok := transport.journal[binding.OperationID]; ok {
		if existing.digest != digest || !bytesEqual(existing.canonical, canonical) {
			return PublicationDelivery{
				OperationID: binding.OperationID, Outcome: PublicationIntegrityConflict,
				Digest: existing.digest, OccurredAt: existing.occurred,
			}, nil
		}
		return PublicationDelivery{
			OperationID: binding.OperationID, Outcome: existing.outcome,
			Digest: digest, Duplicate: true, OccurredAt: existing.occurred,
		}, nil
	}
	transport.journal[binding.OperationID] = deterministicPublicationRecord{
		digest: digest, canonical: canonical, outcome: PublicationDelivered, occurred: now,
	}
	return PublicationDelivery{
		OperationID: binding.OperationID, Outcome: PublicationDelivered,
		Digest: digest, OccurredAt: now,
	}, nil
}

// InspectPublication returns the journaled outcome of the ORIGINAL
// OperationID without delivering anything new.
func (transport *DeterministicPublicationTransport) InspectPublication(
	_ context.Context,
	operationID OperationID,
) (PublicationDelivery, error) {
	if transport == nil || operationID == (OperationID{}) {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorInvalidRequest)
	}
	existing, ok := transport.journal[operationID]
	if !ok {
		return PublicationDelivery{OperationID: operationID, Outcome: PublicationUnknown}, nil
	}
	return PublicationDelivery{
		OperationID: operationID, Outcome: existing.outcome,
		Digest: existing.digest, Duplicate: true, OccurredAt: existing.occurred,
	}, nil
}

func bytesEqual(first, second []byte) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

// PublicationBridgeDisposition is the committed outbox disposition of one
// publication request.
type PublicationBridgeDisposition uint8

const (
	PublicationBridgePending PublicationBridgeDisposition = iota + 1
	PublicationBridgeClaimed
	PublicationBridgeDelivered
	PublicationBridgeReconciliationRequired
	PublicationBridgeIntegrityConflict
	PublicationBridgePoisoned
)

// publicationOutboxRecord is one committed publication request. The binding
// and its canonical digest are fixed at Commit; the adapter can only use
// them.
type publicationOutboxRecord struct {
	Binding       PublicationRequestBinding
	Digest        CanonicalRequestDigest
	CommittedAt   time.Time
	Disposition   PublicationBridgeDisposition
	DeliveryCount uint32
}

// PublicationBridge owns the committed publication outbox: Commit fixes the
// complete canonical request, Claim/Deliver move it through the owned
// transport, and Inspect/Reconcile always return to the ORIGINAL
// OperationID. It never creates a new Artifact Version, parent, head or
// Task retry.
type PublicationBridge struct {
	adapter *PublicationBridgeAdapter
	now     func() time.Time
	records map[OperationID]publicationOutboxRecord
	claims  map[OperationID]bool
}

// NewPublicationBridge builds the committed publication outbox over the
// bridge adapter.
func NewPublicationBridge(adapter *PublicationBridgeAdapter, now func() time.Time) *PublicationBridge {
	if now == nil {
		now = func() time.Time { return time.Unix(0, 0).UTC() }
	}
	return &PublicationBridge{
		adapter: adapter, now: now,
		records: make(map[OperationID]publicationOutboxRecord),
		claims:  make(map[OperationID]bool),
	}
}

// Commit fixes the complete canonical C05 request for one operation at
// outbox-commit time. The same binding exact-replays; a different binding
// under the same OperationID is a durable integrity conflict. The committed
// record is an immutable deep copy so later caller mutations can never
// change the committed request.
func (bridge *PublicationBridge) Commit(binding PublicationRequestBinding) error {
	if bridge == nil || !binding.Valid() || binding.OperationID == (OperationID{}) {
		return newBridgeError(PublicationBridgeErrorInvalidRequest)
	}
	binding = clonePublicationRequestBinding(binding)
	digest := binding.CanonicalDigest()
	if existing, ok := bridge.records[binding.OperationID]; ok {
		if existing.Digest != digest {
			return newBridgeError(PublicationBridgeErrorIntegrityConflict)
		}
		return nil
	}
	bridge.records[binding.OperationID] = publicationOutboxRecord{
		Binding: binding, Digest: digest, CommittedAt: bridge.now().UTC(),
		Disposition: PublicationBridgePending,
	}
	return nil
}

// Claim returns the committed pending operations that are not currently
// claimed.
func (bridge *PublicationBridge) Claim(limit int) []PublicationRequestBinding {
	if bridge == nil || limit <= 0 {
		return nil
	}
	operationIDs := make([]OperationID, 0, len(bridge.records))
	for operationID, record := range bridge.records {
		if record.Disposition == PublicationBridgePending && !bridge.claims[operationID] {
			operationIDs = append(operationIDs, operationID)
		}
	}
	sort.Slice(operationIDs, func(left, right int) bool {
		return operationIDs[left].value < operationIDs[right].value
	})
	claimed := make([]PublicationRequestBinding, 0, limit)
	for _, operationID := range operationIDs {
		record := bridge.records[operationID]
		record.Disposition = PublicationBridgeClaimed
		bridge.records[operationID] = record
		bridge.claims[operationID] = true
		claimed = append(claimed, record.Binding)
		if len(claimed) == limit {
			break
		}
	}
	return claimed
}

// Deliver sends the EXACT claimed binding through the owned transport and
// records the closed disposition. A different binding under the same
// OperationID is a typed integrity conflict, never a new request.
func (bridge *PublicationBridge) Deliver(ctx context.Context, binding PublicationRequestBinding) (PublicationDelivery, error) {
	if bridge == nil || bridge.adapter == nil {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorUnavailable)
	}
	record, ok := bridge.records[binding.OperationID]
	if !ok {
		return PublicationDelivery{OperationID: binding.OperationID, Outcome: PublicationUnknown}, nil
	}
	if record.Digest != binding.CanonicalDigest() {
		return PublicationDelivery{
			OperationID: binding.OperationID, Outcome: PublicationIntegrityConflict,
			Digest: record.Digest, OccurredAt: record.CommittedAt,
		}, nil
	}
	if record.Disposition != PublicationBridgeClaimed || !bridge.claims[binding.OperationID] {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorInvalidRequest)
	}
	delivery, err := bridge.adapter.DeliverPublication(ctx, binding)
	if err != nil {
		return delivery, err
	}
	record.DeliveryCount++
	switch delivery.Outcome {
	case PublicationDelivered:
		record.Disposition = PublicationBridgeDelivered
	case PublicationReconciliationRequired:
		record.Disposition = PublicationBridgeReconciliationRequired
	case PublicationIntegrityConflict:
		record.Disposition = PublicationBridgeIntegrityConflict
	default:
		record.Disposition = PublicationBridgePoisoned
	}
	bridge.records[binding.OperationID] = record
	bridge.claims[binding.OperationID] = false
	return delivery, nil
}

// Inspect inspects the ORIGINAL OperationID through the owned transport.
func (bridge *PublicationBridge) Inspect(ctx context.Context, operationID OperationID) (PublicationDelivery, error) {
	if bridge == nil || bridge.adapter == nil {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorUnavailable)
	}
	if _, ok := bridge.records[operationID]; !ok {
		return PublicationDelivery{OperationID: operationID, Outcome: PublicationUnknown}, nil
	}
	return bridge.adapter.InspectPublication(ctx, operationID)
}

// Reconcile re-inspects the ORIGINAL OperationID and, when the owned
// transport confirms delivery, records the delivered disposition without
// delivering a new request and without creating a new Artifact Version,
// parent, head or Task retry.
func (bridge *PublicationBridge) Reconcile(ctx context.Context, operationID OperationID) (PublicationDelivery, error) {
	if bridge == nil || bridge.adapter == nil {
		return PublicationDelivery{}, newBridgeError(PublicationBridgeErrorUnavailable)
	}
	record, ok := bridge.records[operationID]
	if !ok {
		return PublicationDelivery{OperationID: operationID, Outcome: PublicationUnknown}, nil
	}
	delivery, err := bridge.adapter.InspectPublication(ctx, operationID)
	if err != nil {
		return delivery, err
	}
	if delivery.Outcome == PublicationDelivered {
		record.Disposition = PublicationBridgeDelivered
		bridge.records[operationID] = record
	}
	return delivery, nil
}

// Records returns the committed outbox records for contract assertions.
func (bridge *PublicationBridge) Records() map[OperationID]publicationOutboxRecord {
	if bridge == nil {
		return nil
	}
	records := make(map[OperationID]publicationOutboxRecord, len(bridge.records))
	for operationID, record := range bridge.records {
		records[operationID] = record
	}
	return records
}

// clonePublicationRequestBinding deep-copies every slice of a binding so a
// committed record can never be mutated by a later caller change.
func clonePublicationRequestBinding(binding PublicationRequestBinding) PublicationRequestBinding {
	binding.Spec = PublicationRequestSpec{
		ContractID:            binding.Spec.ContractID,
		Kind:                  binding.Spec.Kind,
		Parent:                binding.Spec.Parent,
		Members:               append([]PublicationMemberSpec(nil), binding.Spec.Members...),
		Staging:               append([]PublicationStagingReference(nil), binding.Spec.Staging...),
		RequiredChannels:      append([]PublicationChannel(nil), binding.Spec.RequiredChannels...),
		RuntimeRefs:           append([]PublicationRuntimeEvidenceRef(nil), binding.Spec.RuntimeRefs...),
		ValidationRef:         binding.Spec.ValidationRef,
		C04CommitRef:          binding.Spec.C04CommitRef,
		ContentCapabilityRefs: append([]PublicationCapabilityRef(nil), binding.Spec.ContentCapabilityRefs...),
	}
	return binding
}

// Digest returns the committed canonical digest of one operation.
func (bridge *PublicationBridge) Digest(operationID OperationID) (CanonicalRequestDigest, bool) {
	record, ok := bridge.records[operationID]
	if !ok {
		return CanonicalRequestDigest{}, false
	}
	return record.Digest, true
}
