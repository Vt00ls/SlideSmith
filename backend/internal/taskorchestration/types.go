// Package taskorchestration defines the closed Task Orchestration seam.
package taskorchestration

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"
)

// SchemaVersion identifies the business encoding understood by an intent.
type SchemaVersion uint32

// SchemaV1 is the initial Task Orchestration intent schema.
const SchemaV1 SchemaVersion = 1 << 16

func NewSchemaVersion(major, minor uint16) SchemaVersion {
	return SchemaVersion(uint32(major)<<16 | uint32(minor))
}

func (version SchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version SchemaVersion) Minor() uint16 { return uint16(version) }

type (
	TaskRevision                     uint64
	ActivityGeneration               uint64
	PhaseRunGeneration               uint64
	PhaseRunFence                    uint64
	RuntimeGeneration                uint64
	TaskWorkspaceLifecycleGeneration uint64
	AuthorizationGeneration          uint64
	ProducerGeneration               uint64
	RuntimeFence                     uint64
	ValidationFence                  uint64
	TaskWorkspaceLifecycleFence      uint64
	PublicationFence                 uint64
	SchedulerFence                   uint64
	UsageFence                       uint64
	ReconciliationFence              uint64
	RecoveryGeneration               uint64
	RecoveryFence                    uint64
	ConfirmationFence                uint64
	SafetyEpoch                      uint64
)

type DecisionRequestID struct{ value string }
type DecisionID struct{ value string }
type AuditFactID struct{ value string }
type OperationID struct{ value string }
type CausationID struct{ value string }
type TaskID struct{ value string }
type AuthorityID struct{ value string }
type GateID struct{ value string }
type PhaseRunID struct{ value string }
type RuntimeRunID struct{ value string }
type ArtifactVersionID struct{ value string }
type EvidenceID struct{ value string }
type PhaseKey struct{ value string }
type PipelineVersionID struct{ value string }
type ExecutionLockID struct{ value string }
type RuntimeReleaseID struct{ value string }
type CompatibilityApprovalID struct{ value string }
type TemplateLockID struct{ value string }
type TaskWorkspaceRevisionID struct{ value string }
type TaskWorkspaceID struct{ value string }
type CheckpointID struct{ value string }

type TraceID [16]byte
type PayloadDigest [32]byte
type EvidenceDigest [32]byte
type EnactmentPayloadDigest [32]byte
type CanonicalRequestDigest [32]byte

func NewDecisionRequestID(value string) (DecisionRequestID, error) {
	if !validOpaqueID(value) {
		return DecisionRequestID{}, invalidIntentError()
	}
	return DecisionRequestID{value: value}, nil
}

func NewTaskID(value string) (TaskID, error) {
	if !validOpaqueID(value) {
		return TaskID{}, invalidIntentError()
	}
	return TaskID{value: value}, nil
}

func NewAuthorityID(value string) (AuthorityID, error) {
	if !validOpaqueID(value) {
		return AuthorityID{}, invalidIntentError()
	}
	return AuthorityID{value: value}, nil
}

func NewGateID(value string) (GateID, error) {
	if !validOpaqueID(value) {
		return GateID{}, invalidIntentError()
	}
	return GateID{value: value}, nil
}

func NewOperationID(value string) (OperationID, error) {
	if !validOpaqueID(value) {
		return OperationID{}, invalidIntentError()
	}
	return OperationID{value: value}, nil
}

func NewCausationID(value string) (CausationID, error) {
	if !validOpaqueID(value) {
		return CausationID{}, invalidIntentError()
	}
	return CausationID{value: value}, nil
}

func NewPhaseRunID(value string) (PhaseRunID, error) {
	if !validOpaqueID(value) {
		return PhaseRunID{}, invalidIntentError()
	}
	return PhaseRunID{value: value}, nil
}

func NewRuntimeRunID(value string) (RuntimeRunID, error) {
	if !validOpaqueID(value) {
		return RuntimeRunID{}, invalidIntentError()
	}
	return RuntimeRunID{value: value}, nil
}

func NewArtifactVersionID(value string) (ArtifactVersionID, error) {
	if !validOpaqueID(value) {
		return ArtifactVersionID{}, invalidIntentError()
	}
	return ArtifactVersionID{value: value}, nil
}

func NewEvidenceID(value string) (EvidenceID, error) {
	if !validOpaqueID(value) {
		return EvidenceID{}, invalidIntentError()
	}
	return EvidenceID{value: value}, nil
}

func NewPhaseKey(value string) (PhaseKey, error) {
	if !validOpaqueID(value) {
		return PhaseKey{}, invalidIntentError()
	}
	return PhaseKey{value: value}, nil
}

func NewPipelineVersionID(value string) (PipelineVersionID, error) {
	if !validOpaqueID(value) {
		return PipelineVersionID{}, invalidIntentError()
	}
	return PipelineVersionID{value: value}, nil
}

func NewExecutionLockID(value string) (ExecutionLockID, error) {
	if !validOpaqueID(value) {
		return ExecutionLockID{}, invalidIntentError()
	}
	return ExecutionLockID{value: value}, nil
}

func NewRuntimeReleaseID(value string) (RuntimeReleaseID, error) {
	if !validOpaqueID(value) {
		return RuntimeReleaseID{}, invalidIntentError()
	}
	return RuntimeReleaseID{value: value}, nil
}

func NewCompatibilityApprovalID(value string) (CompatibilityApprovalID, error) {
	if !validOpaqueID(value) {
		return CompatibilityApprovalID{}, invalidIntentError()
	}
	return CompatibilityApprovalID{value: value}, nil
}

func NewTemplateLockID(value string) (TemplateLockID, error) {
	if !validOpaqueID(value) {
		return TemplateLockID{}, invalidIntentError()
	}
	return TemplateLockID{value: value}, nil
}

func NewTaskWorkspaceRevisionID(value string) (TaskWorkspaceRevisionID, error) {
	if !validOpaqueID(value) {
		return TaskWorkspaceRevisionID{}, invalidIntentError()
	}
	return TaskWorkspaceRevisionID{value: value}, nil
}

func NewTaskWorkspaceID(value string) (TaskWorkspaceID, error) {
	if !validOpaqueID(value) {
		return TaskWorkspaceID{}, invalidIntentError()
	}
	return TaskWorkspaceID{value: value}, nil
}

func NewCheckpointID(value string) (CheckpointID, error) {
	if !validOpaqueID(value) {
		return CheckpointID{}, invalidIntentError()
	}
	return CheckpointID{value: value}, nil
}

func ParseTraceID(value string) (TraceID, error) {
	var id TraceID
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(id) {
		return TraceID{}, invalidIntentError()
	}
	copy(id[:], decoded)
	if id == (TraceID{}) {
		return TraceID{}, invalidIntentError()
	}
	return id, nil
}

func ParsePayloadDigest(value string) (PayloadDigest, error) {
	var digest PayloadDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return PayloadDigest{}, invalidIntentError()
	}
	copy(digest[:], decoded)
	return digest, nil
}

func ParseEvidenceDigest(value string) (EvidenceDigest, error) {
	var digest EvidenceDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return EvidenceDigest{}, invalidIntentError()
	}
	copy(digest[:], decoded)
	return digest, nil
}

func ParseEnactmentPayloadDigest(value string) (EnactmentPayloadDigest, error) {
	var digest EnactmentPayloadDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return EnactmentPayloadDigest{}, invalidIntentError()
	}
	copy(digest[:], decoded)
	return digest, nil
}

func (id DecisionRequestID) String() string       { return id.value }
func (id DecisionID) String() string              { return id.value }
func (id AuditFactID) String() string             { return id.value }
func (id OperationID) String() string             { return id.value }
func (id CausationID) String() string             { return id.value }
func (id TaskID) String() string                  { return id.value }
func (id AuthorityID) String() string             { return id.value }
func (id GateID) String() string                  { return id.value }
func (id PhaseRunID) String() string              { return id.value }
func (id RuntimeRunID) String() string            { return id.value }
func (id ArtifactVersionID) String() string       { return id.value }
func (id EvidenceID) String() string              { return id.value }
func (id PhaseKey) String() string                { return id.value }
func (id PipelineVersionID) String() string       { return id.value }
func (id ExecutionLockID) String() string         { return id.value }
func (id RuntimeReleaseID) String() string        { return id.value }
func (id CompatibilityApprovalID) String() string { return id.value }
func (id TemplateLockID) String() string          { return id.value }
func (id TaskWorkspaceRevisionID) String() string { return id.value }
func (id TaskWorkspaceID) String() string         { return id.value }
func (id CheckpointID) String() string            { return id.value }
func (id TraceID) String() string                 { return hex.EncodeToString(id[:]) }
func (digest PayloadDigest) String() string       { return hex.EncodeToString(digest[:]) }
func (digest EvidenceDigest) String() string      { return hex.EncodeToString(digest[:]) }
func (digest EnactmentPayloadDigest) String() string {
	return hex.EncodeToString(digest[:])
}
func (digest CanonicalRequestDigest) String() string {
	return hex.EncodeToString(digest[:])
}

func validOpaqueID(value string) bool {
	if len(value) == 0 || len(value) > 96 {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		if index == 0 {
			return false
		}
		return false
	}
	return true
}

type ErrorCode uint8

const (
	ErrorAuthorizationDenied ErrorCode = iota + 1
	ErrorInvalidIntent
	ErrorIntegrityConflict
	ErrorStaleTaskRevision
	ErrorStaleAuthority
	ErrorUnsupportedSchema
	ErrorUnsupportedPipelineContract
	ErrorEvidenceInvalid
	ErrorEvidenceScopeConflict
	ErrorOperationalReadOnly
	ErrorDependencyUnavailable
	ErrorReconciliationRequired
	ErrorQueueRejected
	ErrorTerminalConflict
)

type RetryDisposition uint8

const (
	RetryNever RetryDisposition = iota
	RetrySameRequest
	RetryAfterDependency
)

type ReconciliationDisposition uint8

const (
	ReconciliationNotRequired ReconciliationDisposition = iota
	ReconciliationRequired
)

// Error crosses the Task Orchestration seam without retaining a cause or
// caller-supplied detail.
type Error struct {
	code           ErrorCode
	retry          RetryDisposition
	reconciliation ReconciliationDisposition
}

func (e *Error) Error() string {
	if e == nil {
		return "task orchestration intent is invalid"
	}
	return e.code.String()
}

func (e *Error) Code() ErrorCode {
	if e == nil {
		return ErrorInvalidIntent
	}
	return e.code
}

func (e *Error) RetryDisposition() RetryDisposition {
	if e == nil {
		return RetryNever
	}
	return e.retry
}

func (e *Error) ReconciliationDisposition() ReconciliationDisposition {
	if e == nil {
		return ReconciliationNotRequired
	}
	return e.reconciliation
}

func (code ErrorCode) String() string {
	switch code {
	case ErrorAuthorizationDenied:
		return "task orchestration authority is denied"
	case ErrorIntegrityConflict:
		return "task orchestration request conflicts with a committed identity"
	case ErrorStaleTaskRevision:
		return "task orchestration revision is stale"
	case ErrorStaleAuthority:
		return "task orchestration authority is stale"
	case ErrorUnsupportedSchema:
		return "task orchestration schema is unsupported"
	case ErrorUnsupportedPipelineContract:
		return "task orchestration Pipeline contract is unsupported"
	case ErrorEvidenceInvalid:
		return "task orchestration evidence is invalid"
	case ErrorEvidenceScopeConflict:
		return "task orchestration evidence scope conflicts"
	case ErrorOperationalReadOnly:
		return "task orchestration mutation is unavailable"
	case ErrorDependencyUnavailable:
		return "task orchestration dependency is unavailable"
	case ErrorReconciliationRequired:
		return "task orchestration result requires reconciliation"
	case ErrorQueueRejected:
		return "task orchestration request is deferred"
	case ErrorTerminalConflict:
		return "task orchestration terminal state conflicts"
	default:
		return "task orchestration intent is invalid"
	}
}

func invalidIntentError() *Error { return newError(ErrorInvalidIntent) }

func newError(code ErrorCode) *Error {
	errorValue := &Error{
		code:           code,
		retry:          RetryNever,
		reconciliation: ReconciliationNotRequired,
	}
	switch code {
	case ErrorDependencyUnavailable, ErrorQueueRejected, ErrorOperationalReadOnly:
		errorValue.retry = RetryAfterDependency
	case ErrorReconciliationRequired:
		errorValue.retry = RetrySameRequest
		errorValue.reconciliation = ReconciliationRequired
	}
	return errorValue
}

// IntentHeader contains the facts common to every closed intent variant.
type IntentHeader struct {
	SchemaVersion          SchemaVersion
	DecisionRequestID      DecisionRequestID
	CanonicalRequestDigest CanonicalRequestDigest
	TaskID                 TaskID
	ExpectedTaskRevision   TaskRevision
	ActivityGeneration     ActivityGeneration
	OccurredAt             time.Time
	Metadata               IntentMetadata
}

type IntentMetadata struct {
	Trace      TraceMetadata
	Transport  TransportMetadata
	Diagnostic DiagnosticMetadata
}

type TraceMetadata struct {
	TraceID TraceID
}

type TransportMetadata struct {
	Deadline        time.Time
	DeliveryAttempt uint32
}

type DiagnosticCode uint8

const (
	DiagnosticIngress DiagnosticCode = iota + 1
	DiagnosticReplay
)

type DiagnosticMetadata struct {
	Code DiagnosticCode
}

type IntentKind uint8

const (
	IntentStartTask IntentKind = iota + 1
	IntentMakeWorkAvailable
	IntentSubmitConfirmationGate
	IntentRetryPhase
	IntentCancelTask
	IntentBeginManualEdit
	IntentAcceptRuntimeEvidence
	IntentAcceptPhaseValidationEvidence
	IntentAcceptTaskWorkspaceLifecycleEvidence
	IntentAcceptPublicationEvidence
	IntentAcceptSchedulingEvidence
	IntentReconcileEnactment
	IntentApplyOperationalFence
)

type AuthorityKind uint8

const (
	AuthorityUser AuthorityKind = iota + 1
	AuthorityAdministrator
	AuthorityWorker
	AuthorityRuntime
	AuthorityValidator
	AuthorityTaskWorkspaceLifecycle
	AuthorityPublication
	AuthorityScheduler
	AuthorityRecovery
)

type EvidenceKind uint8

const (
	EvidenceRuntime EvidenceKind = iota + 1
	EvidencePhaseValidation
	EvidenceTaskWorkspaceLifecycle
	EvidencePublication
	EvidenceScheduling
)

type EvidenceRef struct {
	ID     EvidenceID
	Kind   EvidenceKind
	Digest EvidenceDigest
}

type EvidenceDisposition uint8

const (
	EvidenceDispositionNonAuthoritative EvidenceDisposition = iota + 1
)

type EvidenceDiagnosticReason uint8

const (
	EvidenceDiagnosticScopeConflict EvidenceDiagnosticReason = iota + 1
	EvidenceDiagnosticStale
	EvidenceDiagnosticUnauthorized
)

// EvidenceDiagnostic reports a rejected observation without granting it Task
// or Phase authority.
type EvidenceDiagnostic struct {
	EvidenceID  EvidenceID
	Disposition EvidenceDisposition
	Reason      EvidenceDiagnosticReason
}

type CancellationState uint8

const (
	CancellationNotRequested CancellationState = iota
	CancellationCancelling
	CancellationCancelled
)

func NewEvidenceRef(id EvidenceID, kind EvidenceKind, digest EvidenceDigest) EvidenceRef {
	return EvidenceRef{ID: id, Kind: kind, Digest: digest}
}

// TransitionIntent is closed outside this package by its private marker.
type TransitionIntent interface {
	Header() IntentHeader
	Kind() IntentKind
	AuthorityKind() AuthorityKind
	canonicalPayload() map[string]any
	canonicalAuthority() map[string]any
	transitionIntent()
}

// TaskOrchestration is the only public Task mutation interface.
type TaskOrchestration interface {
	Decide(context.Context, TransitionIntent) (TransitionDecision, error)
}

type TaskQuery struct {
	TaskID    TaskID
	Authority UserQueryAuthority
}

// TaskOrchestrationQuery is independently read-only.
type TaskOrchestrationQuery interface {
	Query(context.Context, TaskQuery) (TaskOrchestrationView, error)
}

type TaskOrchestrationView struct {
	TaskID                   TaskID
	TaskRevision             TaskRevision
	ActivityGeneration       ActivityGeneration
	LatestDecisionID         DecisionID
	DecisionCount            uint64
	EnactmentCount           uint64
	Status                   TaskStatus
	Route                    Route
	Activity                 ActivityKind
	ExecutionLockID          ExecutionLockID
	TemplateLockID           TemplateLockID
	CurrentPhase             PhaseKey
	ActivePhaseRunID         PhaseRunID
	PhaseRuns                []PhaseRunView
	LatestArtifactVersionID  ArtifactVersionID
	TaskWorkspaceID          TaskWorkspaceID
	EvidenceDiagnosticCount  uint64
	LatestEvidenceDiagnostic EvidenceDiagnostic
	LatestRevisionID         TaskWorkspaceRevisionID
	LatestCheckpointID       CheckpointID
	CancellationState        CancellationState
	PhaseRunCount            uint64
	RuntimeRunCount          uint64
	SafetyEpoch              SafetyEpoch
	OperationalMode          OperationalMode
}

type PhaseRunView struct {
	PhaseRunID             PhaseRunID
	PhaseKey               PhaseKey
	Attempt                uint32
	Generation             PhaseRunGeneration
	Fence                  PhaseRunFence
	Outcome                PhaseRunOutcome
	ValidationOutcome      PhaseValidationOutcome
	LifecycleOutcome       TaskWorkspaceLifecycleOutcome
	RevisionID             TaskWorkspaceRevisionID
	CheckpointID           CheckpointID
	PublicationOutcome     PublicationOutcome
	ArtifactVersionID      ArtifactVersionID
	TaskWorkspaceID        TaskWorkspaceID
	InputArtifactVersionID ArtifactVersionID
	RuntimeRuns            []RuntimeRunView
}

type RuntimeRunView struct {
	RuntimeRunID RuntimeRunID
	Outcome      RuntimeRunOutcome
}

type EnactmentKind uint8

const (
	EnactmentRuntimeExecution EnactmentKind = iota + 1
	EnactmentTaskWorkspaceLifecycle
	EnactmentArtifactPublication
	EnactmentScheduling
	EnactmentUsageAccounting
	EnactmentPresentConfirmationGate
)

type EnactmentFenceKind uint8

const (
	EnactmentFenceRuntimeExecution EnactmentFenceKind = iota + 1
	EnactmentFenceTaskWorkspaceLifecycle
	EnactmentFenceArtifactPublication
	EnactmentFenceScheduling
	EnactmentFenceUsageAccounting
	EnactmentFenceConfirmation
)

// EnactmentFenceRef is closed to the downstream-specific fence types below.
type EnactmentFenceRef interface {
	EnactmentFenceKind() EnactmentFenceKind
	enactmentFenceRef()
}

func (RuntimeFence) enactmentFenceRef()                {}
func (TaskWorkspaceLifecycleFence) enactmentFenceRef() {}
func (PublicationFence) enactmentFenceRef()            {}
func (SchedulerFence) enactmentFenceRef()              {}
func (UsageFence) enactmentFenceRef()                  {}
func (ConfirmationFence) enactmentFenceRef()           {}

func (RuntimeFence) EnactmentFenceKind() EnactmentFenceKind {
	return EnactmentFenceRuntimeExecution
}

func (TaskWorkspaceLifecycleFence) EnactmentFenceKind() EnactmentFenceKind {
	return EnactmentFenceTaskWorkspaceLifecycle
}

func (PublicationFence) EnactmentFenceKind() EnactmentFenceKind {
	return EnactmentFenceArtifactPublication
}

func (SchedulerFence) EnactmentFenceKind() EnactmentFenceKind {
	return EnactmentFenceScheduling
}

func (UsageFence) EnactmentFenceKind() EnactmentFenceKind {
	return EnactmentFenceUsageAccounting
}

func (ConfirmationFence) EnactmentFenceKind() EnactmentFenceKind {
	return EnactmentFenceConfirmation
}

type EnactmentRef struct {
	OperationID        OperationID
	Kind               EnactmentKind
	PayloadDigest      EnactmentPayloadDigest
	ActivityGeneration ActivityGeneration
	Fence              EnactmentFenceRef
	CausationID        CausationID
}

type TaskProjection struct {
	TaskID                  TaskID
	TaskRevision            TaskRevision
	ActivityGeneration      ActivityGeneration
	Status                  TaskStatus
	Route                   Route
	Activity                ActivityKind
	CurrentPhase            PhaseKey
	ActivePhaseRunID        PhaseRunID
	LatestArtifactVersionID ArtifactVersionID
	TaskWorkspaceID         TaskWorkspaceID
	LatestRevisionID        TaskWorkspaceRevisionID
	LatestCheckpointID      CheckpointID
	CancellationState       CancellationState
	SafetyEpoch             SafetyEpoch
	OperationalMode         OperationalMode
}

type AuditFactRef struct {
	AuditFactID AuditFactID
}

// TransitionDecision reports only facts committed by Decide.
type TransitionDecision struct {
	DecisionID             DecisionID
	DecisionRequestID      DecisionRequestID
	CanonicalRequestDigest CanonicalRequestDigest
	PreviousTaskRevision   TaskRevision
	AcceptedTaskRevision   TaskRevision
	TaskProjection         TaskProjection
	AffectedPhaseRuns      []PhaseRunID
	AcceptedEvidenceRefs   []EvidenceRef
	CommittedAt            time.Time
	EnactmentRefs          []EnactmentRef
	MandatoryAuditFactRef  AuditFactRef
}

func nextDecisionID(sequence uint64) DecisionID {
	return DecisionID{value: fmt.Sprintf("decision-%06d", sequence)}
}

func nextAuditFactID(sequence uint64) AuditFactID {
	return AuditFactID{value: fmt.Sprintf("audit-fact-%06d", sequence)}
}

func nextCausationID(sequence uint64) CausationID {
	return CausationID{value: fmt.Sprintf("causation-%06d", sequence)}
}
