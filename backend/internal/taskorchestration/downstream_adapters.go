package taskorchestration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"sync"
)

// EvidenceSchemaVersion identifies a downstream evidence envelope encoding.
type EvidenceSchemaVersion uint32

const EvidenceSchemaV1 EvidenceSchemaVersion = 1 << 16

func NewEvidenceSchemaVersion(major, minor uint16) EvidenceSchemaVersion {
	return EvidenceSchemaVersion(uint32(major)<<16 | uint32(minor))
}

func (version EvidenceSchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version EvidenceSchemaVersion) Minor() uint16 { return uint16(version) }

// EvidenceProducer is the exact downstream authority that signed an envelope.
type EvidenceProducer struct {
	AuthorityID AuthorityID
	Generation  AuthorizationGeneration
}

// EvidencePrerequisite binds an accepted prerequisite without granting it
// Task or Phase progression authority.
type EvidencePrerequisite struct {
	Evidence   EvidenceRef
	Generation ProducerGeneration
	Fence      uint64
}

type DownstreamErrorCode uint8

const (
	DownstreamInvalidEnactment DownstreamErrorCode = iota + 1
	DownstreamIntegrityConflict
	DownstreamUnsupportedSchema
	DownstreamUnauthorized
	DownstreamStale
	DownstreamPrerequisitePending
	DownstreamCorruptEvidence
	DownstreamDependencyUnavailable
	DownstreamReconciliationRequired
)

// DownstreamError is closed and content-free. Raw adapter failures are never
// retained as causes or copied into its message.
type DownstreamError struct {
	code           DownstreamErrorCode
	retry          RetryDisposition
	reconciliation ReconciliationDisposition
}

func (failure *DownstreamError) Error() string {
	if failure == nil {
		return "downstream adapter rejected input"
	}
	switch failure.code {
	case DownstreamIntegrityConflict:
		return "downstream operation integrity conflicts"
	case DownstreamUnsupportedSchema:
		return "downstream evidence schema is unsupported"
	case DownstreamUnauthorized:
		return "downstream evidence authority is denied"
	case DownstreamStale:
		return "downstream evidence authority is stale"
	case DownstreamPrerequisitePending:
		return "downstream evidence prerequisite is pending"
	case DownstreamCorruptEvidence:
		return "downstream evidence integrity is invalid"
	case DownstreamDependencyUnavailable:
		return "downstream dependency is unavailable"
	case DownstreamReconciliationRequired:
		return "downstream result requires reconciliation"
	default:
		return "downstream adapter rejected input"
	}
}

func (failure *DownstreamError) Code() DownstreamErrorCode {
	if failure == nil {
		return DownstreamInvalidEnactment
	}
	return failure.code
}

func (failure *DownstreamError) RetryDisposition() RetryDisposition {
	if failure == nil {
		return RetryNever
	}
	return failure.retry
}

func (failure *DownstreamError) ReconciliationDisposition() ReconciliationDisposition {
	if failure == nil {
		return ReconciliationNotRequired
	}
	return failure.reconciliation
}

func newDownstreamError(code DownstreamErrorCode) *DownstreamError {
	failure := &DownstreamError{code: code}
	switch code {
	case DownstreamDependencyUnavailable, DownstreamPrerequisitePending:
		failure.retry = RetryAfterDependency
	case DownstreamReconciliationRequired:
		failure.retry = RetrySameRequest
		failure.reconciliation = ReconciliationRequired
	}
	return failure
}

func normalizeDownstreamError(err error) *DownstreamError {
	var failure *DownstreamError
	if errors.As(err, &failure) && failure != nil &&
		failure.code >= DownstreamInvalidEnactment && failure.code <= DownstreamReconciliationRequired {
		return newDownstreamError(failure.code)
	}
	return newDownstreamError(DownstreamDependencyUnavailable)
}

// RuntimeEvidenceRecord is the content-free record returned by the Runtime
// Execution authority before adapter verification.
type RuntimeEvidenceRecord struct {
	SchemaVersion         EvidenceSchemaVersion
	EvidenceID            EvidenceID
	EvidenceDigest        EvidenceDigest
	Producer              EvidenceProducer
	TaskID                TaskID
	PhaseRunID            PhaseRunID
	PhaseRunGeneration    PhaseRunGeneration
	PhaseRunFence         PhaseRunFence
	RuntimeRunID          RuntimeRunID
	OperationID           OperationID
	ActivityGeneration    ActivityGeneration
	Generation            RuntimeGeneration
	Fence                 RuntimeFence
	SafetyEpoch           SafetyEpoch
	Outcome               RuntimeRunOutcome
	RequiredPrerequisites []EvidenceRef
	Prerequisites         []EvidencePrerequisite
}

// RuntimeEvidenceDigest independently canonicalizes the typed record. The
// claimed EvidenceDigest is deliberately excluded.
func RuntimeEvidenceDigest(record RuntimeEvidenceRecord) EvidenceDigest {
	prerequisites := canonicalEvidencePrerequisites(record.Prerequisites)
	requiredPrerequisites := canonicalRequiredPrerequisites(record.RequiredPrerequisites)
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation":    uint64(record.ActivityGeneration),
		"evidence_id":            record.EvidenceID.String(),
		"fence":                  uint64(record.Fence),
		"generation":             uint64(record.Generation),
		"operation_id":           record.OperationID.String(),
		"outcome":                runtimeRunOutcomeName(record.Outcome),
		"phase_run_fence":        uint64(record.PhaseRunFence),
		"phase_run_generation":   uint64(record.PhaseRunGeneration),
		"phase_run_id":           record.PhaseRunID.String(),
		"prerequisites":          prerequisites,
		"required_prerequisites": requiredPrerequisites,
		"producer_authority_id":  record.Producer.AuthorityID.String(),
		"producer_generation":    uint64(record.Producer.Generation),
		"runtime_run_id":         record.RuntimeRunID.String(),
		"safety_epoch":           uint64(record.SafetyEpoch),
		"schema_version":         uint64(record.SchemaVersion),
		"task_id":                record.TaskID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

func canonicalPrerequisite(prerequisite EvidencePrerequisite) map[string]any {
	return map[string]any{
		"digest":      prerequisite.Evidence.Digest.String(),
		"evidence_id": prerequisite.Evidence.ID.String(),
		"fence":       prerequisite.Fence,
		"generation":  uint64(prerequisite.Generation),
		"kind":        evidenceKindName(prerequisite.Evidence.Kind),
	}
}

type RuntimeEvidencePort interface {
	EnactRuntime(context.Context, EnactmentRef) (RuntimeEvidenceRecord, error)
}

// RuntimeEvidenceAdapter consumes one exact Runtime enactment and can only
// return typed evidence.
type RuntimeEvidenceAdapter interface {
	Enact(context.Context, EnactmentRef) (RuntimeAdapterEvidence, error)
}

type RuntimeAdapterEvidence struct {
	SchemaVersion      EvidenceSchemaVersion
	Evidence           EvidenceRef
	Producer           EvidenceProducer
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	RuntimeRunID       RuntimeRunID
	OperationID        OperationID
	ActivityGeneration ActivityGeneration
	Generation         RuntimeGeneration
	Fence              RuntimeFence
	SafetyEpoch        SafetyEpoch
	Outcome            RuntimeRunOutcome
	Prerequisites      []EvidencePrerequisite
}

// Intent translates verified Runtime evidence into the one authoritative
// Task mutation seam. It cannot select an outcome other than the one signed by
// the Runtime producer.
func (evidence RuntimeAdapterEvidence) Intent(header IntentHeader) (TransitionIntent, error) {
	if header.TaskID != evidence.TaskID ||
		header.ActivityGeneration != evidence.ActivityGeneration ||
		evidence.SchemaVersion.Major() != EvidenceSchemaV1.Major() ||
		!validEvidenceRef(evidence.Evidence) || evidence.Evidence.Kind != EvidenceRuntime ||
		!validOpaqueID(evidence.Producer.AuthorityID.String()) || evidence.Producer.Generation == 0 {
		return nil, newDownstreamError(DownstreamCorruptEvidence)
	}
	authority := NewRuntimeAuthority(evidence.Producer.AuthorityID, evidence.Producer.Generation)
	return NewAcceptRuntimeEvidenceIntent(header, authority, RuntimeEvidenceBinding{
		Evidence: evidence.Evidence, PhaseRunID: evidence.PhaseRunID,
		PhaseRunGeneration: evidence.PhaseRunGeneration, PhaseRunFence: evidence.PhaseRunFence,
		RuntimeRunID: evidence.RuntimeRunID, OperationID: evidence.OperationID,
		Generation: evidence.Generation, Fence: evidence.Fence, SafetyEpoch: evidence.SafetyEpoch,
		Outcome: evidence.Outcome,
	}), nil
}

type runtimeEvidenceAdapter struct {
	mu      sync.Mutex
	port    RuntimeEvidencePort
	replays map[OperationID]runtimeEvidenceReplay
}

type runtimeEvidenceReplay struct {
	fingerprint [32]byte
	evidence    RuntimeAdapterEvidence
}

func NewRuntimeEvidenceAdapter(port RuntimeEvidencePort) RuntimeEvidenceAdapter {
	return &runtimeEvidenceAdapter{port: port, replays: make(map[OperationID]runtimeEvidenceReplay)}
}

func (adapter *runtimeEvidenceAdapter) Enact(
	ctx context.Context,
	ref EnactmentRef,
) (evidence RuntimeAdapterEvidence, err error) {
	if failure := validateEnactmentRef(ref, EnactmentRuntimeExecution, EnactmentFenceRuntimeExecution); failure != nil {
		return RuntimeAdapterEvidence{}, failure
	}
	fingerprint := enactmentFingerprint(ref)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if replay, exists := adapter.replays[ref.OperationID]; exists {
		if replay.fingerprint != fingerprint {
			return RuntimeAdapterEvidence{}, newDownstreamError(DownstreamIntegrityConflict)
		}
		return cloneRuntimeAdapterEvidence(replay.evidence), nil
	}
	if adapter.port == nil {
		return RuntimeAdapterEvidence{}, newDownstreamError(DownstreamDependencyUnavailable)
	}
	var record RuntimeEvidenceRecord
	func() {
		defer func() {
			if recover() != nil {
				err = newDownstreamError(DownstreamDependencyUnavailable)
			}
		}()
		record, err = adapter.port.EnactRuntime(ctx, ref)
	}()
	if err != nil {
		return RuntimeAdapterEvidence{}, normalizeDownstreamError(err)
	}
	if failure := validateRuntimeEvidenceRecord(record, ref); failure != nil {
		return RuntimeAdapterEvidence{}, failure
	}
	evidence = RuntimeAdapterEvidence{
		SchemaVersion: record.SchemaVersion,
		Evidence:      NewEvidenceRef(record.EvidenceID, EvidenceRuntime, record.EvidenceDigest),
		Producer:      record.Producer, TaskID: record.TaskID, PhaseRunID: record.PhaseRunID,
		PhaseRunGeneration: record.PhaseRunGeneration, PhaseRunFence: record.PhaseRunFence,
		RuntimeRunID: record.RuntimeRunID, OperationID: record.OperationID,
		ActivityGeneration: record.ActivityGeneration, Generation: record.Generation,
		Fence: record.Fence, SafetyEpoch: record.SafetyEpoch, Outcome: record.Outcome,
		Prerequisites: append([]EvidencePrerequisite(nil), record.Prerequisites...),
	}
	adapter.replays[ref.OperationID] = runtimeEvidenceReplay{fingerprint: fingerprint, evidence: evidence}
	return cloneRuntimeAdapterEvidence(evidence), nil
}

func validateRuntimeEvidenceRecord(record RuntimeEvidenceRecord, ref EnactmentRef) *DownstreamError {
	if record.SchemaVersion.Major() != EvidenceSchemaV1.Major() {
		return newDownstreamError(DownstreamUnsupportedSchema)
	}
	if !validOpaqueID(record.EvidenceID.String()) || !validOpaqueID(record.Producer.AuthorityID.String()) ||
		record.Producer.Generation == 0 || !validOpaqueID(record.TaskID.String()) ||
		!validOpaqueID(record.PhaseRunID.String()) || record.PhaseRunGeneration == 0 ||
		record.PhaseRunFence == 0 || !validOpaqueID(record.RuntimeRunID.String()) ||
		record.OperationID != ref.OperationID || record.ActivityGeneration != ref.ActivityGeneration ||
		record.Generation == 0 || record.Fence == 0 || record.SafetyEpoch == 0 ||
		runtimeRunOutcomeName(record.Outcome) == "" {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	runtimeFence, ok := ref.Fence.(RuntimeFence)
	if !ok || record.Fence != runtimeFence {
		return newDownstreamError(DownstreamStale)
	}
	if failure := validateEvidencePrerequisites(record.RequiredPrerequisites, record.Prerequisites); failure != nil {
		return failure
	}
	if record.EvidenceDigest == (EvidenceDigest{}) || record.EvidenceDigest != RuntimeEvidenceDigest(record) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	return nil
}

func validateEnactmentRef(
	ref EnactmentRef,
	kind EnactmentKind,
	fenceKind EnactmentFenceKind,
) *DownstreamError {
	if !validOpaqueID(ref.OperationID.String()) || ref.Kind != kind ||
		ref.PayloadDigest == (EnactmentPayloadDigest{}) || ref.ActivityGeneration == 0 ||
		ref.Fence == nil || ref.Fence.EnactmentFenceKind() != fenceKind ||
		!validOpaqueID(ref.CausationID.String()) {
		return newDownstreamError(DownstreamInvalidEnactment)
	}
	return nil
}

func enactmentFingerprint(ref EnactmentRef) [32]byte {
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation": uint64(ref.ActivityGeneration),
		"causation_id":        ref.CausationID.String(),
		"fence":               enactmentFenceValue(ref.Fence),
		"fence_kind":          uint64(ref.Fence.EnactmentFenceKind()),
		"kind":                uint64(ref.Kind),
		"operation_id":        ref.OperationID.String(),
		"payload_digest":      ref.PayloadDigest.String(),
	})
	return sha256.Sum256(encoded)
}

func enactmentFenceValue(ref EnactmentFenceRef) uint64 {
	switch fence := ref.(type) {
	case RuntimeFence:
		return uint64(fence)
	case TaskWorkspaceLifecycleFence:
		return uint64(fence)
	case PublicationFence:
		return uint64(fence)
	case SchedulerFence:
		return uint64(fence)
	case UsageFence:
		return uint64(fence)
	case ConfirmationFence:
		return uint64(fence)
	default:
		return 0
	}
}

func cloneRuntimeAdapterEvidence(evidence RuntimeAdapterEvidence) RuntimeAdapterEvidence {
	evidence.Prerequisites = append([]EvidencePrerequisite(nil), evidence.Prerequisites...)
	return evidence
}

type SchedulerWorkItemID struct{ value string }
type DeliveryClaimID struct{ value string }
type AdmissionGrantID struct{ value string }

func NewSchedulerWorkItemID(value string) (SchedulerWorkItemID, error) {
	if !validOpaqueID(value) {
		return SchedulerWorkItemID{}, invalidIntentError()
	}
	return SchedulerWorkItemID{value: value}, nil
}

func NewDeliveryClaimID(value string) (DeliveryClaimID, error) {
	if !validOpaqueID(value) {
		return DeliveryClaimID{}, invalidIntentError()
	}
	return DeliveryClaimID{value: value}, nil
}

func NewAdmissionGrantID(value string) (AdmissionGrantID, error) {
	if !validOpaqueID(value) {
		return AdmissionGrantID{}, invalidIntentError()
	}
	return AdmissionGrantID{value: value}, nil
}

func (id SchedulerWorkItemID) String() string { return id.value }
func (id DeliveryClaimID) String() string     { return id.value }
func (id AdmissionGrantID) String() string    { return id.value }

type SchedulerEvidenceKind uint8

const (
	SchedulerClaimed SchedulerEvidenceKind = iota + 1
	SchedulerAdmitted
	SchedulerDeadlineElapsed
	SchedulerDeadLettered
)

func schedulerEvidenceKindName(kind SchedulerEvidenceKind) string {
	switch kind {
	case SchedulerClaimed:
		return "claimed"
	case SchedulerAdmitted:
		return "admitted"
	case SchedulerDeadlineElapsed:
		return "deadline_elapsed"
	case SchedulerDeadLettered:
		return "dead_lettered"
	default:
		return ""
	}
}

type SchedulerEvidenceRecord struct {
	SchemaVersion         EvidenceSchemaVersion
	EvidenceID            EvidenceID
	EvidenceDigest        EvidenceDigest
	Producer              EvidenceProducer
	TaskID                TaskID
	PhaseRunID            PhaseRunID
	PhaseRunGeneration    PhaseRunGeneration
	PhaseRunFence         PhaseRunFence
	OperationID           OperationID
	ActivityGeneration    ActivityGeneration
	Generation            ProducerGeneration
	Fence                 SchedulerFence
	SafetyEpoch           SafetyEpoch
	Kind                  SchedulerEvidenceKind
	WorkItemID            SchedulerWorkItemID
	DeliveryClaimID       DeliveryClaimID
	AdmissionGrantID      AdmissionGrantID
	RequiredPrerequisites []EvidenceRef
	Prerequisites         []EvidencePrerequisite
}

func SchedulerEvidenceDigest(record SchedulerEvidenceRecord) EvidenceDigest {
	prerequisites := canonicalEvidencePrerequisites(record.Prerequisites)
	requiredPrerequisites := canonicalRequiredPrerequisites(record.RequiredPrerequisites)
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation":    uint64(record.ActivityGeneration),
		"evidence_id":            record.EvidenceID.String(),
		"fence":                  uint64(record.Fence),
		"generation":             uint64(record.Generation),
		"kind":                   schedulerEvidenceKindName(record.Kind),
		"operation_id":           record.OperationID.String(),
		"admission_grant_id":     record.AdmissionGrantID.String(),
		"delivery_claim_id":      record.DeliveryClaimID.String(),
		"phase_run_fence":        uint64(record.PhaseRunFence),
		"phase_run_generation":   uint64(record.PhaseRunGeneration),
		"phase_run_id":           record.PhaseRunID.String(),
		"prerequisites":          prerequisites,
		"required_prerequisites": requiredPrerequisites,
		"producer_authority_id":  record.Producer.AuthorityID.String(),
		"producer_generation":    uint64(record.Producer.Generation),
		"safety_epoch":           uint64(record.SafetyEpoch),
		"schema_version":         uint64(record.SchemaVersion),
		"task_id":                record.TaskID.String(),
		"work_item_id":           record.WorkItemID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

type SchedulerEvidencePort interface {
	EnactScheduling(context.Context, EnactmentRef) (SchedulerEvidenceRecord, error)
}

type SchedulerEvidenceAdapter interface {
	Enact(context.Context, EnactmentRef) (SchedulerAdapterEvidence, error)
}

type SchedulerAdapterEvidence struct {
	SchemaVersion      EvidenceSchemaVersion
	Evidence           EvidenceRef
	Producer           EvidenceProducer
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	OperationID        OperationID
	ActivityGeneration ActivityGeneration
	Generation         ProducerGeneration
	Fence              SchedulerFence
	SafetyEpoch        SafetyEpoch
	Kind               SchedulerEvidenceKind
	WorkItemID         SchedulerWorkItemID
	DeliveryClaimID    DeliveryClaimID
	AdmissionGrantID   AdmissionGrantID
	Prerequisites      []EvidencePrerequisite
}

func (evidence SchedulerAdapterEvidence) Intent(header IntentHeader) (TransitionIntent, error) {
	if header.TaskID != evidence.TaskID || header.ActivityGeneration != evidence.ActivityGeneration ||
		evidence.SchemaVersion.Major() != EvidenceSchemaV1.Major() ||
		!validEvidenceRef(evidence.Evidence) || evidence.Evidence.Kind != EvidenceScheduling ||
		!validOpaqueID(evidence.Producer.AuthorityID.String()) || evidence.Producer.Generation == 0 {
		return nil, newDownstreamError(DownstreamCorruptEvidence)
	}
	authority := NewSchedulerAuthority(evidence.Producer.AuthorityID, evidence.Producer.Generation)
	return NewAcceptSchedulingEvidenceIntent(header, authority, SchedulingEvidenceBinding{
		Evidence: evidence.Evidence, PhaseRunID: evidence.PhaseRunID,
		PhaseRunGeneration: evidence.PhaseRunGeneration, PhaseRunFence: evidence.PhaseRunFence,
		OperationID: evidence.OperationID, Generation: evidence.Generation, Fence: evidence.Fence,
		SafetyEpoch: evidence.SafetyEpoch,
	}), nil
}

type schedulerEvidenceAdapter struct {
	mu      sync.Mutex
	port    SchedulerEvidencePort
	replays map[OperationID]schedulerEvidenceReplay
}

type schedulerEvidenceReplay struct {
	fingerprint [32]byte
	evidence    SchedulerAdapterEvidence
}

func NewSchedulerEvidenceAdapter(port SchedulerEvidencePort) SchedulerEvidenceAdapter {
	return &schedulerEvidenceAdapter{port: port, replays: make(map[OperationID]schedulerEvidenceReplay)}
}

func (adapter *schedulerEvidenceAdapter) Enact(
	ctx context.Context,
	ref EnactmentRef,
) (evidence SchedulerAdapterEvidence, err error) {
	if failure := validateEnactmentRef(ref, EnactmentScheduling, EnactmentFenceScheduling); failure != nil {
		return SchedulerAdapterEvidence{}, failure
	}
	fingerprint := enactmentFingerprint(ref)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if replay, exists := adapter.replays[ref.OperationID]; exists {
		if replay.fingerprint != fingerprint {
			return SchedulerAdapterEvidence{}, newDownstreamError(DownstreamIntegrityConflict)
		}
		return cloneSchedulerAdapterEvidence(replay.evidence), nil
	}
	if adapter.port == nil {
		return SchedulerAdapterEvidence{}, newDownstreamError(DownstreamDependencyUnavailable)
	}
	var record SchedulerEvidenceRecord
	func() {
		defer func() {
			if recover() != nil {
				err = newDownstreamError(DownstreamDependencyUnavailable)
			}
		}()
		record, err = adapter.port.EnactScheduling(ctx, ref)
	}()
	if err != nil {
		return SchedulerAdapterEvidence{}, normalizeDownstreamError(err)
	}
	if failure := validateSchedulerEvidenceRecord(record, ref); failure != nil {
		return SchedulerAdapterEvidence{}, failure
	}
	evidence = SchedulerAdapterEvidence{
		SchemaVersion: record.SchemaVersion,
		Evidence:      NewEvidenceRef(record.EvidenceID, EvidenceScheduling, record.EvidenceDigest),
		Producer:      record.Producer, TaskID: record.TaskID, PhaseRunID: record.PhaseRunID,
		PhaseRunGeneration: record.PhaseRunGeneration, PhaseRunFence: record.PhaseRunFence,
		OperationID: record.OperationID, ActivityGeneration: record.ActivityGeneration,
		Generation: record.Generation, Fence: record.Fence, SafetyEpoch: record.SafetyEpoch,
		Kind: record.Kind, WorkItemID: record.WorkItemID, DeliveryClaimID: record.DeliveryClaimID,
		AdmissionGrantID: record.AdmissionGrantID,
		Prerequisites:    append([]EvidencePrerequisite(nil), record.Prerequisites...),
	}
	adapter.replays[ref.OperationID] = schedulerEvidenceReplay{fingerprint, evidence}
	return cloneSchedulerAdapterEvidence(evidence), nil
}

func validateSchedulerEvidenceRecord(record SchedulerEvidenceRecord, ref EnactmentRef) *DownstreamError {
	if record.SchemaVersion.Major() != EvidenceSchemaV1.Major() {
		return newDownstreamError(DownstreamUnsupportedSchema)
	}
	if !validOpaqueID(record.EvidenceID.String()) || !validOpaqueID(record.Producer.AuthorityID.String()) ||
		record.Producer.Generation == 0 || !validOpaqueID(record.TaskID.String()) ||
		!validOpaqueID(record.PhaseRunID.String()) || record.PhaseRunGeneration == 0 ||
		record.PhaseRunFence == 0 || record.OperationID != ref.OperationID ||
		record.ActivityGeneration != ref.ActivityGeneration || record.Generation == 0 ||
		record.Fence == 0 || record.SafetyEpoch == 0 || schedulerEvidenceKindName(record.Kind) == "" {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	if !validSchedulerEvidenceIdentities(record) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	fence, ok := ref.Fence.(SchedulerFence)
	if !ok || record.Fence != fence {
		return newDownstreamError(DownstreamStale)
	}
	if failure := validateEvidencePrerequisites(record.RequiredPrerequisites, record.Prerequisites); failure != nil {
		return failure
	}
	if record.EvidenceDigest == (EvidenceDigest{}) || record.EvidenceDigest != SchedulerEvidenceDigest(record) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	return nil
}

func validSchedulerEvidenceIdentities(record SchedulerEvidenceRecord) bool {
	if !validOpaqueID(record.WorkItemID.String()) {
		return false
	}
	hasClaim := validOpaqueID(record.DeliveryClaimID.String())
	hasGrant := validOpaqueID(record.AdmissionGrantID.String())
	switch record.Kind {
	case SchedulerClaimed:
		return hasClaim && !hasGrant
	case SchedulerAdmitted:
		return hasClaim && hasGrant
	case SchedulerDeadlineElapsed, SchedulerDeadLettered:
		return !hasClaim && !hasGrant
	default:
		return false
	}
}

func cloneSchedulerAdapterEvidence(evidence SchedulerAdapterEvidence) SchedulerAdapterEvidence {
	evidence.Prerequisites = append([]EvidencePrerequisite(nil), evidence.Prerequisites...)
	return evidence
}

type PublicationEvidenceRecord struct {
	SchemaVersion          EvidenceSchemaVersion
	EvidenceID             EvidenceID
	EvidenceDigest         EvidenceDigest
	Producer               EvidenceProducer
	TaskID                 TaskID
	PhaseRunID             PhaseRunID
	PhaseRunGeneration     PhaseRunGeneration
	PhaseRunFence          PhaseRunFence
	OperationID            OperationID
	ActivityGeneration     ActivityGeneration
	Generation             ProducerGeneration
	Fence                  PublicationFence
	SafetyEpoch            SafetyEpoch
	Outcome                PublicationOutcome
	ArtifactVersionID      ArtifactVersionID
	ArtifactManifestDigest EvidenceDigest
	RequiredPrerequisites  []EvidenceRef
	Prerequisites          []EvidencePrerequisite
}

func PublicationEvidenceDigest(record PublicationEvidenceRecord) EvidenceDigest {
	prerequisites := canonicalEvidencePrerequisites(record.Prerequisites)
	requiredPrerequisites := canonicalRequiredPrerequisites(record.RequiredPrerequisites)
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation":      uint64(record.ActivityGeneration),
		"artifact_manifest_digest": record.ArtifactManifestDigest.String(),
		"artifact_version_id":      record.ArtifactVersionID.String(),
		"evidence_id":              record.EvidenceID.String(),
		"fence":                    uint64(record.Fence),
		"generation":               uint64(record.Generation),
		"operation_id":             record.OperationID.String(),
		"outcome":                  publicationOutcomeName(record.Outcome),
		"phase_run_fence":          uint64(record.PhaseRunFence),
		"phase_run_generation":     uint64(record.PhaseRunGeneration),
		"phase_run_id":             record.PhaseRunID.String(),
		"prerequisites":            prerequisites,
		"required_prerequisites":   requiredPrerequisites,
		"producer_authority_id":    record.Producer.AuthorityID.String(),
		"producer_generation":      uint64(record.Producer.Generation),
		"safety_epoch":             uint64(record.SafetyEpoch),
		"schema_version":           uint64(record.SchemaVersion),
		"task_id":                  record.TaskID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

type PublicationEvidencePort interface {
	EnactPublication(context.Context, EnactmentRef) (PublicationEvidenceRecord, error)
}

type PublicationEvidenceAdapter interface {
	Enact(context.Context, EnactmentRef) (PublicationAdapterEvidence, error)
}

type PublicationAdapterEvidence struct {
	SchemaVersion          EvidenceSchemaVersion
	Evidence               EvidenceRef
	Producer               EvidenceProducer
	TaskID                 TaskID
	PhaseRunID             PhaseRunID
	PhaseRunGeneration     PhaseRunGeneration
	PhaseRunFence          PhaseRunFence
	OperationID            OperationID
	ActivityGeneration     ActivityGeneration
	Generation             ProducerGeneration
	Fence                  PublicationFence
	SafetyEpoch            SafetyEpoch
	Outcome                PublicationOutcome
	ArtifactVersionID      ArtifactVersionID
	ArtifactManifestDigest EvidenceDigest
	Prerequisites          []EvidencePrerequisite
}

func (evidence PublicationAdapterEvidence) Intent(header IntentHeader) (TransitionIntent, error) {
	if header.TaskID != evidence.TaskID || header.ActivityGeneration != evidence.ActivityGeneration ||
		evidence.SchemaVersion.Major() != EvidenceSchemaV1.Major() ||
		!validEvidenceRef(evidence.Evidence) || evidence.Evidence.Kind != EvidencePublication ||
		!validOpaqueID(evidence.Producer.AuthorityID.String()) || evidence.Producer.Generation == 0 {
		return nil, newDownstreamError(DownstreamCorruptEvidence)
	}
	authority := NewPublicationAuthority(evidence.Producer.AuthorityID, evidence.Producer.Generation)
	return NewAcceptPublicationEvidenceIntent(header, authority, PublicationEvidenceBinding{
		Evidence: evidence.Evidence, PhaseRunID: evidence.PhaseRunID,
		PhaseRunGeneration: evidence.PhaseRunGeneration, PhaseRunFence: evidence.PhaseRunFence,
		OperationID: evidence.OperationID, Generation: evidence.Generation, Fence: evidence.Fence,
		SafetyEpoch: evidence.SafetyEpoch, Outcome: evidence.Outcome,
		ArtifactVersionID: evidence.ArtifactVersionID,
	}), nil
}

type publicationEvidenceAdapter struct {
	mu      sync.Mutex
	port    PublicationEvidencePort
	replays map[OperationID]publicationEvidenceReplay
}

type publicationEvidenceReplay struct {
	fingerprint [32]byte
	evidence    PublicationAdapterEvidence
}

func NewPublicationEvidenceAdapter(port PublicationEvidencePort) PublicationEvidenceAdapter {
	return &publicationEvidenceAdapter{port: port, replays: make(map[OperationID]publicationEvidenceReplay)}
}

func (adapter *publicationEvidenceAdapter) Enact(
	ctx context.Context,
	ref EnactmentRef,
) (evidence PublicationAdapterEvidence, err error) {
	if failure := validateEnactmentRef(ref, EnactmentArtifactPublication, EnactmentFenceArtifactPublication); failure != nil {
		return PublicationAdapterEvidence{}, failure
	}
	fingerprint := enactmentFingerprint(ref)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if replay, exists := adapter.replays[ref.OperationID]; exists {
		if replay.fingerprint != fingerprint {
			return PublicationAdapterEvidence{}, newDownstreamError(DownstreamIntegrityConflict)
		}
		return clonePublicationAdapterEvidence(replay.evidence), nil
	}
	if adapter.port == nil {
		return PublicationAdapterEvidence{}, newDownstreamError(DownstreamDependencyUnavailable)
	}
	var record PublicationEvidenceRecord
	func() {
		defer func() {
			if recover() != nil {
				err = newDownstreamError(DownstreamDependencyUnavailable)
			}
		}()
		record, err = adapter.port.EnactPublication(ctx, ref)
	}()
	if err != nil {
		return PublicationAdapterEvidence{}, normalizeDownstreamError(err)
	}
	if failure := validatePublicationEvidenceRecord(record, ref); failure != nil {
		return PublicationAdapterEvidence{}, failure
	}
	evidence = PublicationAdapterEvidence{
		SchemaVersion: record.SchemaVersion,
		Evidence:      NewEvidenceRef(record.EvidenceID, EvidencePublication, record.EvidenceDigest),
		Producer:      record.Producer, TaskID: record.TaskID, PhaseRunID: record.PhaseRunID,
		PhaseRunGeneration: record.PhaseRunGeneration, PhaseRunFence: record.PhaseRunFence,
		OperationID: record.OperationID, ActivityGeneration: record.ActivityGeneration,
		Generation: record.Generation, Fence: record.Fence, SafetyEpoch: record.SafetyEpoch,
		Outcome: record.Outcome, ArtifactVersionID: record.ArtifactVersionID,
		ArtifactManifestDigest: record.ArtifactManifestDigest,
		Prerequisites:          append([]EvidencePrerequisite(nil), record.Prerequisites...),
	}
	adapter.replays[ref.OperationID] = publicationEvidenceReplay{fingerprint, evidence}
	return clonePublicationAdapterEvidence(evidence), nil
}

func validatePublicationEvidenceRecord(record PublicationEvidenceRecord, ref EnactmentRef) *DownstreamError {
	if record.SchemaVersion.Major() != EvidenceSchemaV1.Major() {
		return newDownstreamError(DownstreamUnsupportedSchema)
	}
	if !validOpaqueID(record.EvidenceID.String()) || !validOpaqueID(record.Producer.AuthorityID.String()) ||
		record.Producer.Generation == 0 || !validOpaqueID(record.TaskID.String()) ||
		!validOpaqueID(record.PhaseRunID.String()) || record.PhaseRunGeneration == 0 ||
		record.PhaseRunFence == 0 || record.OperationID != ref.OperationID ||
		record.ActivityGeneration != ref.ActivityGeneration || record.Generation == 0 ||
		record.Fence == 0 || record.SafetyEpoch == 0 || publicationOutcomeName(record.Outcome) == "" ||
		(record.Outcome == PublicationActivated &&
			(!validOpaqueID(record.ArtifactVersionID.String()) || record.ArtifactManifestDigest == (EvidenceDigest{}))) ||
		(record.Outcome == PublicationRejected &&
			(record.ArtifactVersionID != (ArtifactVersionID{}) || record.ArtifactManifestDigest != (EvidenceDigest{}))) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	fence, ok := ref.Fence.(PublicationFence)
	if !ok || record.Fence != fence {
		return newDownstreamError(DownstreamStale)
	}
	if failure := validateEvidencePrerequisites(record.RequiredPrerequisites, record.Prerequisites); failure != nil {
		return failure
	}
	if record.EvidenceDigest == (EvidenceDigest{}) || record.EvidenceDigest != PublicationEvidenceDigest(record) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	return nil
}

func clonePublicationAdapterEvidence(evidence PublicationAdapterEvidence) PublicationAdapterEvidence {
	evidence.Prerequisites = append([]EvidencePrerequisite(nil), evidence.Prerequisites...)
	return evidence
}

type QuotaReservationID struct{ value string }

func NewQuotaReservationID(value string) (QuotaReservationID, error) {
	if !validOpaqueID(value) {
		return QuotaReservationID{}, invalidIntentError()
	}
	return QuotaReservationID{value: value}, nil
}

func (id QuotaReservationID) String() string { return id.value }

type UsageAccountingEvidenceKind uint8

const (
	UsageReservationActive UsageAccountingEvidenceKind = iota + 1
	UsageReservationClosing
	UsageReservationSettled
)

func usageAccountingEvidenceKindName(kind UsageAccountingEvidenceKind) string {
	switch kind {
	case UsageReservationActive:
		return "active"
	case UsageReservationClosing:
		return "closing"
	case UsageReservationSettled:
		return "settled"
	default:
		return ""
	}
}

type UsageAccountingEvidenceRecord struct {
	SchemaVersion         EvidenceSchemaVersion
	EvidenceID            EvidenceID
	EvidenceDigest        EvidenceDigest
	Producer              EvidenceProducer
	TaskID                TaskID
	PhaseRunID            PhaseRunID
	PhaseRunGeneration    PhaseRunGeneration
	PhaseRunFence         PhaseRunFence
	OperationID           OperationID
	ActivityGeneration    ActivityGeneration
	Generation            ProducerGeneration
	Fence                 UsageFence
	SafetyEpoch           SafetyEpoch
	Kind                  UsageAccountingEvidenceKind
	QuotaReservationID    QuotaReservationID
	RequiredPrerequisites []EvidenceRef
	Prerequisites         []EvidencePrerequisite
}

func UsageAccountingEvidenceDigest(record UsageAccountingEvidenceRecord) EvidenceDigest {
	prerequisites := canonicalEvidencePrerequisites(record.Prerequisites)
	requiredPrerequisites := canonicalRequiredPrerequisites(record.RequiredPrerequisites)
	encoded, _ := json.Marshal(map[string]any{
		"activity_generation":    uint64(record.ActivityGeneration),
		"evidence_id":            record.EvidenceID.String(),
		"fence":                  uint64(record.Fence),
		"generation":             uint64(record.Generation),
		"kind":                   usageAccountingEvidenceKindName(record.Kind),
		"operation_id":           record.OperationID.String(),
		"phase_run_fence":        uint64(record.PhaseRunFence),
		"phase_run_generation":   uint64(record.PhaseRunGeneration),
		"phase_run_id":           record.PhaseRunID.String(),
		"prerequisites":          prerequisites,
		"required_prerequisites": requiredPrerequisites,
		"producer_authority_id":  record.Producer.AuthorityID.String(),
		"producer_generation":    uint64(record.Producer.Generation),
		"quota_reservation_id":   record.QuotaReservationID.String(),
		"safety_epoch":           uint64(record.SafetyEpoch),
		"schema_version":         uint64(record.SchemaVersion),
		"task_id":                record.TaskID.String(),
	})
	sum := sha256.Sum256(encoded)
	return EvidenceDigest(sum)
}

type UsageAccountingEvidencePort interface {
	EnactUsageAccounting(context.Context, EnactmentRef) (UsageAccountingEvidenceRecord, error)
}

type UsageAccountingEvidenceAdapter interface {
	Enact(context.Context, EnactmentRef) (UsageAccountingAdapterEvidence, error)
}

// UsageAccountingAdapterEvidence intentionally has no Intent method: late
// reserve/close/settlement facts remain Usage Accounting authority and cannot
// reopen a terminal Phase Run.
type UsageAccountingAdapterEvidence struct {
	SchemaVersion      EvidenceSchemaVersion
	Evidence           EvidenceRef
	Producer           EvidenceProducer
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	PhaseRunGeneration PhaseRunGeneration
	PhaseRunFence      PhaseRunFence
	OperationID        OperationID
	ActivityGeneration ActivityGeneration
	Generation         ProducerGeneration
	Fence              UsageFence
	SafetyEpoch        SafetyEpoch
	Kind               UsageAccountingEvidenceKind
	QuotaReservationID QuotaReservationID
	Prerequisites      []EvidencePrerequisite
}

type usageAccountingEvidenceAdapter struct {
	mu      sync.Mutex
	port    UsageAccountingEvidencePort
	replays map[OperationID]usageAccountingEvidenceReplay
}

type usageAccountingEvidenceReplay struct {
	fingerprint [32]byte
	evidence    UsageAccountingAdapterEvidence
}

func NewUsageAccountingEvidenceAdapter(port UsageAccountingEvidencePort) UsageAccountingEvidenceAdapter {
	return &usageAccountingEvidenceAdapter{
		port: port, replays: make(map[OperationID]usageAccountingEvidenceReplay),
	}
}

func (adapter *usageAccountingEvidenceAdapter) Enact(
	ctx context.Context,
	ref EnactmentRef,
) (evidence UsageAccountingAdapterEvidence, err error) {
	if failure := validateEnactmentRef(ref, EnactmentUsageAccounting, EnactmentFenceUsageAccounting); failure != nil {
		return UsageAccountingAdapterEvidence{}, failure
	}
	fingerprint := enactmentFingerprint(ref)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if replay, exists := adapter.replays[ref.OperationID]; exists {
		if replay.fingerprint != fingerprint {
			return UsageAccountingAdapterEvidence{}, newDownstreamError(DownstreamIntegrityConflict)
		}
		return cloneUsageAccountingAdapterEvidence(replay.evidence), nil
	}
	if adapter.port == nil {
		return UsageAccountingAdapterEvidence{}, newDownstreamError(DownstreamDependencyUnavailable)
	}
	var record UsageAccountingEvidenceRecord
	func() {
		defer func() {
			if recover() != nil {
				err = newDownstreamError(DownstreamDependencyUnavailable)
			}
		}()
		record, err = adapter.port.EnactUsageAccounting(ctx, ref)
	}()
	if err != nil {
		return UsageAccountingAdapterEvidence{}, normalizeDownstreamError(err)
	}
	if failure := validateUsageAccountingEvidenceRecord(record, ref); failure != nil {
		return UsageAccountingAdapterEvidence{}, failure
	}
	evidence = UsageAccountingAdapterEvidence{
		SchemaVersion: record.SchemaVersion,
		Evidence:      NewEvidenceRef(record.EvidenceID, EvidenceUsageAccounting, record.EvidenceDigest),
		Producer:      record.Producer, TaskID: record.TaskID, PhaseRunID: record.PhaseRunID,
		PhaseRunGeneration: record.PhaseRunGeneration, PhaseRunFence: record.PhaseRunFence,
		OperationID: record.OperationID, ActivityGeneration: record.ActivityGeneration,
		Generation: record.Generation, Fence: record.Fence, SafetyEpoch: record.SafetyEpoch,
		Kind: record.Kind, QuotaReservationID: record.QuotaReservationID,
		Prerequisites: append([]EvidencePrerequisite(nil), record.Prerequisites...),
	}
	adapter.replays[ref.OperationID] = usageAccountingEvidenceReplay{fingerprint, evidence}
	return cloneUsageAccountingAdapterEvidence(evidence), nil
}

func validateUsageAccountingEvidenceRecord(
	record UsageAccountingEvidenceRecord,
	ref EnactmentRef,
) *DownstreamError {
	if record.SchemaVersion.Major() != EvidenceSchemaV1.Major() {
		return newDownstreamError(DownstreamUnsupportedSchema)
	}
	if !validOpaqueID(record.EvidenceID.String()) || !validOpaqueID(record.Producer.AuthorityID.String()) ||
		record.Producer.Generation == 0 || !validOpaqueID(record.TaskID.String()) ||
		!validOpaqueID(record.PhaseRunID.String()) || record.PhaseRunGeneration == 0 ||
		record.PhaseRunFence == 0 || record.OperationID != ref.OperationID ||
		record.ActivityGeneration != ref.ActivityGeneration || record.Generation == 0 ||
		record.Fence == 0 || record.SafetyEpoch == 0 || usageAccountingEvidenceKindName(record.Kind) == "" ||
		!validOpaqueID(record.QuotaReservationID.String()) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	fence, ok := ref.Fence.(UsageFence)
	if !ok || record.Fence != fence {
		return newDownstreamError(DownstreamStale)
	}
	if failure := validateEvidencePrerequisites(record.RequiredPrerequisites, record.Prerequisites); failure != nil {
		return failure
	}
	if record.EvidenceDigest == (EvidenceDigest{}) ||
		record.EvidenceDigest != UsageAccountingEvidenceDigest(record) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	return nil
}

func cloneUsageAccountingAdapterEvidence(
	evidence UsageAccountingAdapterEvidence,
) UsageAccountingAdapterEvidence {
	evidence.Prerequisites = append([]EvidencePrerequisite(nil), evidence.Prerequisites...)
	return evidence
}

func canonicalRequiredPrerequisites(required []EvidenceRef) []map[string]any {
	encoded := make([]map[string]any, len(required))
	for index, evidence := range required {
		encoded[index] = map[string]any{
			"digest":      evidence.Digest.String(),
			"evidence_id": evidence.ID.String(),
			"kind":        evidenceKindName(evidence.Kind),
		}
	}
	sort.Slice(encoded, func(i, j int) bool {
		return canonicalPrerequisiteSortKey(encoded[i]) < canonicalPrerequisiteSortKey(encoded[j])
	})
	return encoded
}

func canonicalEvidencePrerequisites(prerequisites []EvidencePrerequisite) []map[string]any {
	encoded := make([]map[string]any, len(prerequisites))
	for index, prerequisite := range prerequisites {
		encoded[index] = canonicalPrerequisite(prerequisite)
	}
	sort.Slice(encoded, func(i, j int) bool {
		return canonicalPrerequisiteSortKey(encoded[i]) < canonicalPrerequisiteSortKey(encoded[j])
	})
	return encoded
}

func canonicalPrerequisiteSortKey(encoded map[string]any) string {
	return encoded["evidence_id"].(string) + "\x00" + encoded["kind"].(string) + "\x00" + encoded["digest"].(string)
}

func validateEvidencePrerequisites(
	required []EvidenceRef,
	observed []EvidencePrerequisite,
) *DownstreamError {
	if len(observed) < len(required) {
		return newDownstreamError(DownstreamPrerequisitePending)
	}
	if len(observed) != len(required) {
		return newDownstreamError(DownstreamCorruptEvidence)
	}
	requiredByID := make(map[EvidenceID]EvidenceRef, len(required))
	for _, evidence := range required {
		if !validEvidenceRef(evidence) {
			return newDownstreamError(DownstreamCorruptEvidence)
		}
		if _, duplicate := requiredByID[evidence.ID]; duplicate {
			return newDownstreamError(DownstreamCorruptEvidence)
		}
		requiredByID[evidence.ID] = evidence
	}
	seen := make(map[EvidenceID]struct{}, len(observed))
	for _, prerequisite := range observed {
		if !validEvidenceRef(prerequisite.Evidence) || prerequisite.Generation == 0 || prerequisite.Fence == 0 {
			return newDownstreamError(DownstreamPrerequisitePending)
		}
		expected, exists := requiredByID[prerequisite.Evidence.ID]
		if !exists || prerequisite.Evidence != expected {
			return newDownstreamError(DownstreamCorruptEvidence)
		}
		if _, duplicate := seen[prerequisite.Evidence.ID]; duplicate {
			return newDownstreamError(DownstreamCorruptEvidence)
		}
		seen[prerequisite.Evidence.ID] = struct{}{}
	}
	return nil
}
