package artifactpublication

// This file delivers child SPEC #111 (C05-07): bounded, content-free
// observability for the Artifact Publication deep module. Metrics use only
// registered bounded dimensions; User, Workspace, Task, version, member,
// operation, evidence, digest, path, locator, TraceID and free-form values
// can never become primary labels. Structured logs and trace spans carry
// closed allowlists and no arbitrary attributes. Telemetry and external
// audit projections are incomplete, expiring copies of retained
// authoritative facts: they are emitted strictly after commit, their
// failure never rolls back a committed protected decision, and they can be
// rebuilt from the retained canonical audit facts (projection_backlog.go).
// No telemetry signal can authorize access, advance a stream, release
// residue, or close a Cleanup Debt.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"sync"
	"time"
)

// TelemetrySchemaVersion identifies bounded, content-free telemetry and
// external-audit projection envelopes.
type TelemetrySchemaVersion uint32

const TelemetrySchemaV1 TelemetrySchemaVersion = 1 << 16

func (version TelemetrySchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version TelemetrySchemaVersion) Minor() uint16 { return uint16(version) }

// TelemetryCategory is the closed safe-error/outcome category admitted into
// telemetry. It mirrors the closed C05 safe-error taxonomy and never
// carries raw cause text, locators, or cross-Workspace existence.
type TelemetryCategory uint8

const (
	TelemetryCategoryNone TelemetryCategory = iota + 1
	TelemetryCategoryAuthorization
	TelemetryCategoryInvalid
	TelemetryCategoryConflict
	TelemetryCategoryStale
	TelemetryCategoryBinding
	TelemetryCategoryIntegrity
	TelemetryCategoryDurability
	TelemetryCategoryResource
	TelemetryCategoryDependency
	TelemetryCategoryReconciliation
	TelemetryCategoryUnknown
)

func validTelemetryCategory(value TelemetryCategory) bool {
	return value >= TelemetryCategoryNone && value <= TelemetryCategoryUnknown
}

// MetricOperation is a closed bounded dimension. Exact publication
// OperationIDs never become metric labels; only this registered enum may.
type MetricOperation uint8

const (
	MetricOperationNone MetricOperation = iota
	MetricOperationPrepare
	MetricOperationVerify
	MetricOperationActivate
	MetricOperationReject
	MetricOperationCancel
	MetricOperationReconcile
	MetricOperationResidue
	MetricOperationCleanup
)

func validMetricOperation(value MetricOperation) bool {
	return value >= MetricOperationNone && value <= MetricOperationCleanup
}

func (operation MetricOperation) String() string {
	switch operation {
	case MetricOperationPrepare:
		return "prepare"
	case MetricOperationVerify:
		return "verify"
	case MetricOperationActivate:
		return "activate"
	case MetricOperationReject:
		return "reject"
	case MetricOperationCancel:
		return "cancel"
	case MetricOperationReconcile:
		return "reconcile"
	case MetricOperationResidue:
		return "residue"
	case MetricOperationCleanup:
		return "cleanup"
	default:
		return "none"
	}
}

// MetricState mirrors the closed PublicationOperationState set plus the
// non-terminal pending disposition. Exact stream revisions never become
// metric labels.
type MetricState uint8

const (
	MetricStateNone MetricState = iota
	MetricStatePrepared
	MetricStateVerified
	MetricStateActivated
	MetricStateRejected
	MetricStateCancelled
	MetricStateReconciliationRequired
)

func validMetricState(value MetricState) bool {
	return value >= MetricStateNone && value <= MetricStateReconciliationRequired
}

func publicationStateMetricState(state PublicationOperationState) MetricState {
	switch state {
	case OperationPrepared:
		return MetricStatePrepared
	case OperationVerified:
		return MetricStateVerified
	case OperationActivated:
		return MetricStateActivated
	case OperationRejected:
		return MetricStateRejected
	case OperationCancelled:
		return MetricStateCancelled
	case OperationReconciliationRequired:
		return MetricStateReconciliationRequired
	default:
		return MetricStateNone
	}
}

// MetricOutcome mirrors the closed protected-decision outcome set. An
// unknown or missing source is never invented as a zero or success.
type MetricOutcome uint8

const (
	MetricOutcomeNone MetricOutcome = iota
	MetricOutcomeAccepted
	MetricOutcomeRejected
	MetricOutcomeCancelled
	MetricOutcomeReplayed
	MetricOutcomeConflict
	MetricOutcomeDeferred
	MetricOutcomeFailed
)

func validMetricOutcome(value MetricOutcome) bool {
	return value >= MetricOutcomeNone && value <= MetricOutcomeFailed
}

// MetricMemberKind is the closed registered Artifact kind dimension. Exact
// member identities, names and digests never become labels.
type MetricMemberKind uint8

const (
	MetricMemberKindNone MetricMemberKind = iota
	MetricMemberKindDeck
	MetricMemberKindPreview
	MetricMemberKindPlan
	MetricMemberKindReport
)

func validMetricMemberKind(value MetricMemberKind) bool {
	return value >= MetricMemberKindNone && value <= MetricMemberKindReport
}

func artifactKindMetricMemberKind(kind ArtifactKind) MetricMemberKind {
	switch kind {
	case ArtifactKindDeck:
		return MetricMemberKindDeck
	case ArtifactKindPreview:
		return MetricMemberKindPreview
	case ArtifactKindPlan:
		return MetricMemberKindPlan
	case ArtifactKindValidationReport:
		return MetricMemberKindReport
	default:
		return MetricMemberKindNone
	}
}

// MetricAdapter is the closed adapter-class dimension. Adapter identities
// never become labels.
type MetricAdapter uint8

const (
	MetricAdapterNone MetricAdapter = iota
	MetricAdapterInMemory
	MetricAdapterPostgres
	MetricAdapterOwnedTransport
)

func validMetricAdapter(value MetricAdapter) bool {
	return value >= MetricAdapterNone && value <= MetricAdapterOwnedTransport
}

// MetricResidueDisposition is the closed residue/debt disposition
// dimension. Exact residue references never become labels.
type MetricResidueDisposition uint8

const (
	MetricResidueDispositionNone MetricResidueDisposition = iota
	MetricResidueDispositionPending
	MetricResidueDispositionReleased
	MetricResidueDispositionAmbiguous
	MetricResidueDispositionExpired
	MetricResidueDispositionRetained
)

func validMetricResidueDisposition(value MetricResidueDisposition) bool {
	return value >= MetricResidueDispositionNone && value <= MetricResidueDispositionRetained
}

type MetricName uint8

const (
	MetricPublicationOperationCount MetricName = iota + 1
	MetricProjectionDeliveryCount
	MetricSafeErrorCount
	MetricResidueDispositionCount
	MetricCleanupDebtCount
	MetricCardinalityRejectionCount
)

// MetricLabels is strictly typed. Every label is a closed enum; a business,
// high-cardinality, or free-form value cannot even be expressed, and any
// unregistered combination is rejected by the registry. The registered
// dimensions mirror the observability contract: module, operation family,
// state, outcome, member kind, adapter class, residue disposition, and
// safe-error category. Exact Task/version/member/operation/evidence/digest/
// path/locator/TraceID values stay in protected diagnostics or audit and
// are deliberately not primary labels.
type MetricLabels struct {
	Operation          MetricOperation
	State              MetricState
	Outcome            MetricOutcome
	MemberKind         MetricMemberKind
	Adapter            MetricAdapter
	ResidueDisposition MetricResidueDisposition
	Category           TelemetryCategory
}

type MetricSample struct {
	Name   MetricName
	Labels MetricLabels
	Count  uint64
}

type metricRegistryEntry struct {
	name               MetricName
	operations         []MetricOperation
	states             []MetricState
	outcomes           []MetricOutcome
	memberKinds        []MetricMemberKind
	adapters           []MetricAdapter
	residueDisposition []MetricResidueDisposition
	categories         []TelemetryCategory
}

func metricOperations() []MetricOperation {
	return []MetricOperation{
		MetricOperationPrepare, MetricOperationVerify, MetricOperationActivate,
		MetricOperationReject, MetricOperationCancel, MetricOperationReconcile,
		MetricOperationResidue, MetricOperationCleanup,
	}
}

func metricStates() []MetricState {
	return []MetricState{
		MetricStatePrepared, MetricStateVerified, MetricStateActivated,
		MetricStateRejected, MetricStateCancelled, MetricStateReconciliationRequired,
	}
}

func metricOutcomes() []MetricOutcome {
	return []MetricOutcome{
		MetricOutcomeAccepted, MetricOutcomeRejected, MetricOutcomeCancelled,
		MetricOutcomeReplayed, MetricOutcomeConflict, MetricOutcomeDeferred,
		MetricOutcomeFailed,
	}
}

func telemetryCategories() []TelemetryCategory {
	return []TelemetryCategory{
		TelemetryCategoryNone, TelemetryCategoryAuthorization, TelemetryCategoryInvalid,
		TelemetryCategoryConflict, TelemetryCategoryStale, TelemetryCategoryBinding,
		TelemetryCategoryIntegrity, TelemetryCategoryDurability, TelemetryCategoryResource,
		TelemetryCategoryDependency, TelemetryCategoryReconciliation, TelemetryCategoryUnknown,
	}
}

func registeredMetricPolicies() []metricRegistryEntry {
	return []metricRegistryEntry{
		{
			name:       MetricPublicationOperationCount,
			operations: metricOperations(),
			states:     metricStates(),
			outcomes:   metricOutcomes(),
			adapters:   metricAdapters(),
			categories: telemetryCategories(),
		},
		{
			name:       MetricProjectionDeliveryCount,
			operations: metricOperations(),
			states:     metricStates(),
			outcomes: []MetricOutcome{
				MetricOutcomeAccepted, MetricOutcomeDeferred, MetricOutcomeFailed,
				MetricOutcomeReplayed,
			},
			adapters:   metricAdapters(),
			categories: telemetryCategories(),
		},
		{
			name:       MetricSafeErrorCount,
			operations: metricOperations(),
			states:     metricStates(),
			categories: telemetryCategories(),
		},
		{
			name:       MetricResidueDispositionCount,
			operations: []MetricOperation{MetricOperationResidue},
			states: []MetricState{
				MetricStateRejected, MetricStateCancelled, MetricStateReconciliationRequired,
			},
			residueDisposition: []MetricResidueDisposition{
				MetricResidueDispositionPending, MetricResidueDispositionReleased,
				MetricResidueDispositionAmbiguous, MetricResidueDispositionExpired,
				MetricResidueDispositionRetained,
			},
			categories: telemetryCategories(),
		},
		{
			name:       MetricCleanupDebtCount,
			operations: []MetricOperation{MetricOperationCleanup},
			states:     []MetricState{MetricStateReconciliationRequired},
			categories: telemetryCategories(),
		},
		{
			name:       MetricCardinalityRejectionCount,
			operations: metricOperations(),
			categories: []TelemetryCategory{
				TelemetryCategoryInvalid, TelemetryCategoryUnknown,
			},
		},
	}
}

func metricAdapters() []MetricAdapter {
	return []MetricAdapter{
		MetricAdapterInMemory, MetricAdapterPostgres, MetricAdapterOwnedTransport,
	}
}

func containsMetricOperation(values []MetricOperation, value MetricOperation) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsMetricState(values []MetricState, value MetricState) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsMetricOutcome(values []MetricOutcome, value MetricOutcome) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsMetricMemberKind(values []MetricMemberKind, value MetricMemberKind) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsMetricAdapter(values []MetricAdapter, value MetricAdapter) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsMetricResidueDisposition(values []MetricResidueDisposition, value MetricResidueDisposition) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsTelemetryCategory(values []TelemetryCategory, value TelemetryCategory) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// RegisteredMetricSample reports whether a sample is inside the closed
// metric registry. Unregistered names, invalid label values, or label
// combinations outside the per-metric policy are rejected; the caller must
// emit a bounded cardinality-rejection counter instead.
func RegisteredMetricSample(sample MetricSample) bool {
	if sample.Count == 0 {
		return false
	}
	for _, policy := range registeredMetricPolicies() {
		if sample.Name != policy.name {
			continue
		}
		if !allowlistedMetricDimension(policy.operations, sample.Labels.Operation) ||
			!allowlistedMetricDimension(policy.states, sample.Labels.State) ||
			!allowlistedMetricDimension(policy.outcomes, sample.Labels.Outcome) ||
			!allowlistedMetricDimension(policy.memberKinds, sample.Labels.MemberKind) ||
			!allowlistedMetricDimension(policy.adapters, sample.Labels.Adapter) ||
			!allowlistedMetricDimension(policy.residueDisposition, sample.Labels.ResidueDisposition) ||
			!allowlistedMetricDimension(policy.categories, sample.Labels.Category) {
			return false
		}
		return true
	}
	return false
}

// allowlistedMetricDimension allows the zero/none value always and
// otherwise requires membership in the per-metric closed policy.
func allowlistedMetricDimension[T ~uint8](policy []T, value T) bool {
	if value == 0 {
		return true
	}
	for _, candidate := range policy {
		if candidate == value {
			return true
		}
	}
	return false
}

// MetricSeriesUpperBound is derived from the same closed registry used to
// reject unregistered samples. No runtime identity can enlarge it.
func MetricSeriesUpperBound() uint64 {
	var bound uint64
	for _, policy := range registeredMetricPolicies() {
		bound += uint64(len(policy.operations)+1) * uint64(len(policy.states)+1) *
			uint64(len(policy.outcomes)+1) * uint64(len(policy.memberKinds)+1) *
			uint64(len(policy.adapters)+1) * uint64(len(policy.residueDisposition)+1) *
			uint64(len(policy.categories)+1)
	}
	return bound
}

type StructuredLogEvent uint8

const (
	StructuredLogPrepareCommitted StructuredLogEvent = iota + 1
	StructuredLogVerifyCommitted
	StructuredLogActivationCommitted
	StructuredLogRejectionCommitted
	StructuredLogCancellationCommitted
	StructuredLogReconciliationObserved
	StructuredLogResidueCommitted
	StructuredLogCleanupCommitted
)

type StructuredLogSchemaVersion uint16

const StructuredLogSchemaV1 StructuredLogSchemaVersion = 1

type StructuredLogSeverity uint8

const (
	StructuredLogInfo StructuredLogSeverity = iota + 1
	StructuredLogWarning
)

type TelemetryModule uint8

const TelemetryModuleArtifactPublication TelemetryModule = iota + 1

// StructuredLogRecord has a closed allowlist and deliberately carries no
// business identity, free-form message, path, locator, credential, or
// content. Redaction happens before buffering or export by construction:
// there is no field that could hold a redactable value.
type StructuredLogRecord struct {
	SchemaVersion StructuredLogSchemaVersion
	Severity      StructuredLogSeverity
	Module        TelemetryModule
	Event         StructuredLogEvent
	Operation     MetricOperation
	State         MetricState
	Outcome       MetricOutcome
	Category      TelemetryCategory
	RecordedAt    time.Time
}

type TraceSpanName uint8

const (
	TracePrepareCommit TraceSpanName = iota + 1
	TraceVerifyCommit
	TraceActivationCommit
	TraceRejectionCommit
	TraceCancellationCommit
	TraceReconciliation
	TraceResidueCommit
	TraceCleanupCommit
)

// TraceSpanRecord admits only allowlisted protected correlation. TraceID is
// diagnostic context only: it is never an idempotency, ownership, fence,
// evidence, cleanup, or recovery key.
type TraceSpanRecord struct {
	Module          TelemetryModule
	Name            TraceSpanName
	Operation       MetricOperation
	State           MetricState
	Outcome         MetricOutcome
	Category        TelemetryCategory
	TaskID          TaskID
	OperationID     PublicationOperationID
	ArtifactVersion ArtifactVersionID
	TraceID         string
	RecordedAt      time.Time
}

// PublicationTelemetryProjection is the bounded, content-free post-commit
// telemetry envelope. It carries only closed enums plus allowlisted
// protected correlation (the opaque AuditFactID and OperationID) for trace
// diagnosis; it never carries content, member names, paths, locators,
// credentials, or arbitrary attributes.
type PublicationTelemetryProjection struct {
	SchemaVersion  TelemetrySchemaVersion
	AuditFactID    string
	TaskID         TaskID
	OperationID    PublicationOperationID
	StreamRevision StreamRevision
	Operation      MetricOperation
	State          MetricState
	Outcome        MetricOutcome
	Category       TelemetryCategory
	Adapter        MetricAdapter
	TraceID        string
	RecordedAt     time.Time
}

func validPublicationTelemetryProjection(projection PublicationTelemetryProjection) bool {
	return projection.SchemaVersion == TelemetrySchemaV1 &&
		projection.AuditFactID != "" &&
		projection.TaskID != "" && projection.OperationID != "" &&
		validMetricOperation(projection.Operation) &&
		validMetricState(projection.State) &&
		validMetricOutcome(projection.Outcome) &&
		validTelemetryCategory(projection.Category) &&
		validMetricAdapter(projection.Adapter) &&
		!projection.RecordedAt.IsZero()
}

// ExternalAuditProjectionKind is the closed fact-kind of an external audit
// delivery projection.
type ExternalAuditProjectionKind uint8

const (
	ExternalAuditProtectedDecision ExternalAuditProjectionKind = iota + 1
	ExternalAuditProtectedCleanupResolution
)

// ExternalAuditProjection is a rebuildable, content-free copy of one
// already committed mandatory audit fact. It never replaces the
// authoritative fact and its failure never rolls back the committed
// decision; it is redelivered from the retained authoritative fact.
type ExternalAuditProjection struct {
	SchemaVersion     TelemetrySchemaVersion
	FactKind          ExternalAuditProjectionKind
	AuditFactID       string
	CanonicalDigest   AuditFactDigest
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	OperationID       PublicationOperationID
	RequestID         PublicationRequestID
	RequestDigest     Digest
	Action            PublicationIntentKind
	AuthorityKind     EvidenceAuthorityKind
	AuthorityID       AuthorityID
	State             PublicationOperationState
	VersionID         ArtifactVersionID
	ManifestDigest    Digest
	StreamRevision    StreamRevision
	RecordedAt        time.Time
	AuthoritativeFact PublicationAuditFact
}

// ExternalAuditProjectionDigest returns the canonical delivery identity of
// one content-free copy of one retained authoritative audit fact.
func ExternalAuditProjectionDigest(projection ExternalAuditProjection) ProjectionDigest {
	encoded, _ := json.Marshal(struct {
		SchemaVersion   TelemetrySchemaVersion
		FactKind        ExternalAuditProjectionKind
		AuditFactID     string
		CanonicalDigest string
		PolicyDomainID  string
		TaskID          string
		OperationID     string
		RequestID       string
		RequestDigest   string
		Action          string
		AuthorityKind   string
		AuthorityID     string
		State           string
		VersionID       string
		ManifestDigest  string
		StreamRevision  uint64
		RecordedAt      int64
	}{
		SchemaVersion:   projection.SchemaVersion,
		FactKind:        projection.FactKind,
		AuditFactID:     projection.AuditFactID,
		CanonicalDigest: projection.CanonicalDigest.String(),
		PolicyDomainID:  string(projection.PolicyDomainID),
		TaskID:          string(projection.TaskID),
		OperationID:     string(projection.OperationID),
		RequestID:       string(projection.RequestID),
		RequestDigest:   string(projection.RequestDigest),
		Action:          string(projection.Action),
		AuthorityKind:   string(projection.AuthorityKind),
		AuthorityID:     string(projection.AuthorityID),
		State:           string(projection.State),
		VersionID:       string(projection.VersionID),
		ManifestDigest:  string(projection.ManifestDigest),
		StreamRevision:  uint64(projection.StreamRevision),
		RecordedAt:      projection.RecordedAt.UnixNano(),
	})
	return sha256.Sum256(encoded)
}

// ProjectionDigest is the canonical delivery identity of one content-free
// external audit projection.
type ProjectionDigest [32]byte

func (digest ProjectionDigest) String() string {
	return hexDigest(digest[:])
}

func hexDigest(value []byte) string {
	const hexDigits = "0123456789abcdef"
	output := make([]byte, len(value)*2)
	for index, byteValue := range value {
		output[index*2] = hexDigits[byteValue>>4]
		output[index*2+1] = hexDigits[byteValue&0x0f]
	}
	return string(output)
}

// auditProjectionFromFact derives the bounded external audit projection
// from one retained authoritative audit fact.
func auditProjectionFromFact(fact PublicationAuditFact) ExternalAuditProjection {
	factKind := ExternalAuditProtectedDecision
	switch fact.Action {
	case IntentRecordResidueAssembly, IntentReleaseResidue, IntentResolveCleanupDebt:
		factKind = ExternalAuditProtectedCleanupResolution
	}
	projection := ExternalAuditProjection{
		SchemaVersion:     TelemetrySchemaV1,
		FactKind:          factKind,
		AuditFactID:       fact.AuditFactID,
		CanonicalDigest:   fact.CanonicalDigest,
		PolicyDomainID:    fact.PolicyDomainID,
		TaskID:            fact.TaskID,
		OperationID:       fact.OperationID,
		RequestID:         fact.RequestID,
		RequestDigest:     fact.RequestDigest,
		Action:            fact.Action,
		AuthorityKind:     fact.AuthorityKind,
		AuthorityID:       fact.AuthorityID,
		State:             fact.State,
		VersionID:         fact.VersionID,
		ManifestDigest:    fact.ManifestDigest,
		StreamRevision:    fact.StreamRevision,
		RecordedAt:        fact.RecordedAt,
		AuthoritativeFact: fact,
	}
	return projection
}

// telemetryProjectionFromAudit derives one bounded content-free telemetry
// projection from one retained authoritative audit fact. It never reads raw
// adapter, vendor, or Durable Object detail.
func telemetryProjectionFromAudit(
	fact PublicationAuditFact,
	adapter MetricAdapter,
) PublicationTelemetryProjection {
	return PublicationTelemetryProjection{
		SchemaVersion:  TelemetrySchemaV1,
		AuditFactID:    fact.AuditFactID,
		TaskID:         fact.TaskID,
		OperationID:    fact.OperationID,
		StreamRevision: fact.StreamRevision,
		Operation:      metricOperationForAuditAction(fact.Action),
		State:          publicationStateMetricState(fact.State),
		Outcome:        MetricOutcomeAccepted,
		Category:       TelemetryCategoryNone,
		Adapter:        adapter,
		RecordedAt:     fact.RecordedAt,
	}
}

func metricOperationForAuditAction(action PublicationIntentKind) MetricOperation {
	switch action {
	case IntentPreparePublication:
		return MetricOperationPrepare
	case IntentVerifyPublication:
		return MetricOperationVerify
	case IntentActivatePublication:
		return MetricOperationActivate
	case IntentRejectPublication:
		return MetricOperationReject
	case IntentCancelPublication:
		return MetricOperationCancel
	case IntentReconcilePublication:
		return MetricOperationReconcile
	case IntentReleaseResidue:
		return MetricOperationResidue
	case IntentRecordResidueAssembly, IntentResolveCleanupDebt:
		return MetricOperationCleanup
	default:
		return MetricOperationNone
	}
}

// TelemetrySink receives bounded content-free telemetry projections after
// the authoritative transaction commits. Sink failure never rolls back the
// committed decision; it only produces a durable backlog/degraded signal.
type TelemetrySink interface {
	ProjectTelemetry(context.Context, PublicationTelemetryProjection) error
}

type TelemetrySinkFunc func(context.Context, PublicationTelemetryProjection) error

func (function TelemetrySinkFunc) ProjectTelemetry(
	ctx context.Context,
	projection PublicationTelemetryProjection,
) error {
	return function(ctx, projection)
}

// ExternalAuditProjectionSink receives content-free copies of committed
// mandatory audit facts after commit. Sink failure never rolls back the
// committed decision; delivery is rebuildable from the retained facts.
type ExternalAuditProjectionSink interface {
	ProjectExternalAudit(context.Context, ExternalAuditProjection) error
}

type ExternalAuditProjectionSinkFunc func(context.Context, ExternalAuditProjection) error

func (function ExternalAuditProjectionSinkFunc) ProjectExternalAudit(
	ctx context.Context,
	projection ExternalAuditProjection,
) error {
	return function(ctx, projection)
}

type ProjectionErrorCode uint8

const (
	ProjectionInvalidConfiguration ProjectionErrorCode = iota + 1
	ProjectionInvalidFact
	ProjectionUnavailable
)

// ProjectionError never retains a sink error, caller detail, or raw cause.
type ProjectionError struct {
	code ProjectionErrorCode
}

func (failure *ProjectionError) Error() string {
	if failure == nil || failure.code == ProjectionInvalidConfiguration {
		return "artifact publication projection configuration is invalid"
	}
	if failure.code == ProjectionInvalidFact {
		return "artifact publication projection fact is invalid"
	}
	return "artifact publication projection is unavailable"
}

func (failure *ProjectionError) Code() ProjectionErrorCode {
	if failure == nil {
		return ProjectionInvalidConfiguration
	}
	return failure.code
}

func newProjectionError(code ProjectionErrorCode) *ProjectionError {
	return &ProjectionError{code: code}
}

// ---------------------------------------------------------------------------
// Deterministic telemetry adapter
// ---------------------------------------------------------------------------

type DeterministicTelemetryConfig struct {
	Now func() time.Time
}

type taskScopedMetricSample struct {
	taskID TaskID
	sample MetricSample
}

type taskScopedLogRecord struct {
	taskID TaskID
	record StructuredLogRecord
}

// DeterministicTelemetry is a vendor-neutral, restart-free contract adapter
// for tests and local diagnostics. It stores typed projections only, never
// accepts arbitrary attributes or business-ID labels, and exposes one
// protected reason-bound, bounded, exact-scope snapshot surface.
type DeterministicTelemetry struct {
	mu            sync.Mutex
	projections   map[string]PublicationTelemetryProjection
	audit         map[string]ExternalAuditProjection
	metrics       []taskScopedMetricSample
	logs          []taskScopedLogRecord
	traces        []TraceSpanRecord
	now           func() time.Time
	diagnosticSeq uint64
}

func NewDeterministicTelemetry(config DeterministicTelemetryConfig) *DeterministicTelemetry {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &DeterministicTelemetry{
		projections: make(map[string]PublicationTelemetryProjection),
		audit:       make(map[string]ExternalAuditProjection),
		now:         now,
	}
}

// ProjectTelemetry records one bounded content-free telemetry projection.
// Invalid projections are rejected with a safe ProjectionError and a bounded
// cardinality-rejection metric; duplicate exact projections are idempotent
// and never rewrite a retained projection.
func (telemetry *DeterministicTelemetry) ProjectTelemetry(
	ctx context.Context,
	projection PublicationTelemetryProjection,
) error {
	if telemetry == nil || ctx == nil || ctx.Err() != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if !validPublicationTelemetryProjection(projection) {
		telemetry.recordMetricRejection(projection.TaskID, projection.Operation)
		return newProjectionError(ProjectionInvalidFact)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if existing, ok := telemetry.projections[projection.AuditFactID]; ok {
		if reflect.DeepEqual(existing, projection) {
			return nil
		}
		// Same audit fact with a different telemetry payload is a
		// projection integrity conflict; the authoritative fact is never
		// rewritten by a projection.
		telemetry.metrics = append(telemetry.metrics, taskScopedMetricSample{
			taskID: projection.TaskID,
			sample: metricRejectionSample(projection.Operation),
		})
		return newProjectionError(ProjectionInvalidFact)
	}
	telemetry.projections[projection.AuditFactID] = projection
	metric, logRecord, trace := telemetrySignalsForProjection(projection)
	telemetry.metrics = append(telemetry.metrics, taskScopedMetricSample{
		taskID: projection.TaskID, sample: metric,
	})
	telemetry.logs = append(telemetry.logs, taskScopedLogRecord{
		taskID: projection.TaskID, record: logRecord,
	})
	telemetry.traces = append(telemetry.traces, trace)
	return nil
}

// ProjectExternalAudit records one content-free copy of a committed
// mandatory audit fact. Duplicate exact delivery is idempotent; a same
// AuditFactID with a different canonical digest is an integrity conflict
// that is never silently resolved.
func (telemetry *DeterministicTelemetry) ProjectExternalAudit(
	ctx context.Context,
	projection ExternalAuditProjection,
) error {
	if telemetry == nil || ctx == nil || ctx.Err() != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if !validExternalAuditProjection(projection) {
		return newProjectionError(ProjectionInvalidFact)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if existing, ok := telemetry.audit[projection.AuditFactID]; ok {
		if existing.CanonicalDigest == projection.CanonicalDigest &&
			existing.OperationID == projection.OperationID {
			return nil
		}
		return newProjectionError(ProjectionInvalidFact)
	}
	telemetry.audit[projection.AuditFactID] = projection
	return nil
}

func validExternalAuditProjection(projection ExternalAuditProjection) bool {
	if projection.SchemaVersion != TelemetrySchemaV1 ||
		projection.AuditFactID == "" || projection.CanonicalDigest == (AuditFactDigest{}) ||
		projection.PolicyDomainID == "" || projection.TaskID == "" ||
		projection.OperationID == "" || projection.RequestDigest == "" ||
		!validIntentKind(projection.Action) ||
		!validEvidenceAuthorityKind(projection.AuthorityKind) ||
		!validOperationState(projection.State) || projection.RecordedAt.IsZero() {
		return false
	}
	return projection.CanonicalDigest == projection.AuthoritativeFact.CanonicalDigest &&
		projection.AuthoritativeFact.AuditFactID == projection.AuditFactID &&
		validPublicationAuditFact(projection.AuthoritativeFact)
}

func telemetrySignalsForProjection(
	projection PublicationTelemetryProjection,
) (MetricSample, StructuredLogRecord, TraceSpanRecord) {
	name := MetricPublicationOperationCount
	if projection.Operation == MetricOperationResidue ||
		projection.Operation == MetricOperationCleanup {
		name = MetricCleanupDebtCount
		if projection.Operation == MetricOperationResidue {
			name = MetricResidueDispositionCount
		}
	}
	labels := MetricLabels{
		Operation: projection.Operation, State: projection.State,
		Outcome: projection.Outcome, Adapter: projection.Adapter,
		Category: projection.Category,
	}
	severity := StructuredLogInfo
	event := logEventForMetricOperation(projection.Operation)
	if projection.Category != TelemetryCategoryNone {
		severity = StructuredLogWarning
	}
	return MetricSample{Name: name, Labels: labels, Count: 1},
		StructuredLogRecord{
			SchemaVersion: StructuredLogSchemaV1, Severity: severity,
			Module: TelemetryModuleArtifactPublication, Event: event,
			Operation: projection.Operation, State: projection.State,
			Outcome: projection.Outcome, Category: projection.Category,
			RecordedAt: projection.RecordedAt,
		},
		TraceSpanRecord{
			Module: TelemetryModuleArtifactPublication, Name: traceNameForMetricOperation(projection.Operation),
			Operation: projection.Operation, State: projection.State,
			Outcome: projection.Outcome, Category: projection.Category,
			TaskID: projection.TaskID, OperationID: projection.OperationID,
			TraceID: projection.TraceID, RecordedAt: projection.RecordedAt,
		}
}

func logEventForMetricOperation(operation MetricOperation) StructuredLogEvent {
	switch operation {
	case MetricOperationPrepare:
		return StructuredLogPrepareCommitted
	case MetricOperationVerify:
		return StructuredLogVerifyCommitted
	case MetricOperationActivate:
		return StructuredLogActivationCommitted
	case MetricOperationReject:
		return StructuredLogRejectionCommitted
	case MetricOperationCancel:
		return StructuredLogCancellationCommitted
	case MetricOperationReconcile:
		return StructuredLogReconciliationObserved
	case MetricOperationResidue:
		return StructuredLogResidueCommitted
	default:
		return StructuredLogCleanupCommitted
	}
}

func traceNameForMetricOperation(operation MetricOperation) TraceSpanName {
	switch operation {
	case MetricOperationPrepare:
		return TracePrepareCommit
	case MetricOperationVerify:
		return TraceVerifyCommit
	case MetricOperationActivate:
		return TraceActivationCommit
	case MetricOperationReject:
		return TraceRejectionCommit
	case MetricOperationCancel:
		return TraceCancellationCommit
	case MetricOperationReconcile:
		return TraceReconciliation
	case MetricOperationResidue:
		return TraceResidueCommit
	default:
		return TraceCleanupCommit
	}
}

func metricRejectionSample(operation MetricOperation) MetricSample {
	if !validMetricOperation(operation) {
		operation = MetricOperationNone
	}
	return MetricSample{
		Name: MetricCardinalityRejectionCount,
		Labels: MetricLabels{
			Operation: operation, Category: TelemetryCategoryInvalid,
		},
		Count: 1,
	}
}

func (telemetry *DeterministicTelemetry) recordMetricRejection(taskID TaskID, operation MetricOperation) {
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	telemetry.metrics = append(telemetry.metrics, taskScopedMetricSample{
		taskID: taskID,
		sample: metricRejectionSample(operation),
	})
}

// TelemetryDiagnosticQuery is a reason-bound, exact-operation, bounded
// query for the protected read-only telemetry diagnostic surface. It can
// never enumerate the whole population.
type TelemetryDiagnosticQuery struct {
	authority   AdministratorMetadataAuthority
	taskID      TaskID
	operationID PublicationOperationID
	limit       uint32
}

func NewTelemetryDiagnosticQuery(
	authority AdministratorMetadataAuthority,
	taskID TaskID,
	operationID PublicationOperationID,
	limit uint32,
) TelemetryDiagnosticQuery {
	return TelemetryDiagnosticQuery{
		authority: authority, taskID: taskID, operationID: operationID, limit: limit,
	}
}

// TelemetrySnapshot is the bounded, content-free, exact-scope protected
// telemetry view. Metrics and logs are per-Task; traces are per-Task and
// exact-operation when an operation identity is supplied.
type TelemetrySnapshot struct {
	Metrics []MetricSample
	Logs    []StructuredLogRecord
	Traces  []TraceSpanRecord
}

// Snapshot returns a bounded, content-free, exact-scope view of the
// telemetry projections. It is read-only, requires a reason-bound
// administrator metadata authority, and cannot mutate publication state.
func (telemetry *DeterministicTelemetry) Snapshot(
	ctx context.Context,
	query TelemetryDiagnosticQuery,
) (TelemetrySnapshot, error) {
	if telemetry == nil || ctx == nil || ctx.Err() != nil {
		return TelemetrySnapshot{}, newProjectionError(ProjectionUnavailable)
	}
	if !query.authority.valid() || !query.authority.reasonBound() ||
		query.taskID == "" || query.limit == 0 || query.limit > 100 {
		return TelemetrySnapshot{}, newError(ErrorOwnershipDenied)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	limit := int(query.limit)
	metrics := make([]MetricSample, 0, limit)
	for _, observation := range telemetry.metrics {
		if observation.taskID != query.taskID {
			continue
		}
		metrics = append(metrics, observation.sample)
		if len(metrics) == limit {
			break
		}
	}
	logs := make([]StructuredLogRecord, 0, limit)
	for _, observation := range telemetry.logs {
		if observation.taskID != query.taskID {
			continue
		}
		logs = append(logs, observation.record)
		if len(logs) == limit {
			break
		}
	}
	traces := make([]TraceSpanRecord, 0, limit)
	for _, trace := range telemetry.traces {
		if trace.TaskID != query.taskID {
			continue
		}
		if query.operationID != "" && trace.OperationID != query.operationID {
			continue
		}
		traces = append(traces, trace)
		if len(traces) == limit {
			break
		}
	}
	return TelemetrySnapshot{Metrics: metrics, Logs: logs, Traces: traces}, nil
}
