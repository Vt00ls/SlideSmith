package taskorchestration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"sync"
	"time"
)

// ProjectionSchemaVersion identifies content-free external audit and
// telemetry projection envelopes.
type ProjectionSchemaVersion uint32

const ProjectionSchemaV1 ProjectionSchemaVersion = 1 << 16

func (version ProjectionSchemaVersion) Major() uint16 {
	return uint16(uint32(version) >> 16)
}

func (version ProjectionSchemaVersion) Minor() uint16 { return uint16(version) }

type ProjectionOutcome uint8

// ProjectionDigest binds a content-free projection or protected diagnostic
// audit fact to one canonical representation.
type ProjectionDigest [32]byte

const (
	ProjectionAccepted ProjectionOutcome = iota + 1
)

// ExternalAuditProjection is a rebuildable copy of one already committed
// mandatory audit fact. It contains no content, path, locator, session, or
// credential field and never replaces the authoritative audit fact.
type ExternalAuditProjection struct {
	SchemaVersion     ProjectionSchemaVersion
	CanonicalDigest   ProjectionDigest
	AuditFactID       AuditFactID
	DecisionID        DecisionID
	DecisionRequestID DecisionRequestID
	AcceptedRevision  TaskRevision
	Outcome           ProjectionOutcome
	RecordedAt        time.Time
}

// ExternalAuditProjectionDigest returns the canonical delivery identity for a
// content-free copy of one retained authoritative audit fact.
func ExternalAuditProjectionDigest(projection ExternalAuditProjection) ProjectionDigest {
	encoded, _ := json.Marshal(struct {
		SchemaVersion     ProjectionSchemaVersion
		AuditFactID       string
		DecisionID        string
		DecisionRequestID string
		AcceptedRevision  TaskRevision
		Outcome           ProjectionOutcome
		RecordedAt        int64
	}{
		SchemaVersion:     projection.SchemaVersion,
		AuditFactID:       projection.AuditFactID.value,
		DecisionID:        projection.DecisionID.value,
		DecisionRequestID: projection.DecisionRequestID.value,
		AcceptedRevision:  projection.AcceptedRevision,
		Outcome:           projection.Outcome,
		RecordedAt:        projection.RecordedAt.UnixNano(),
	})
	return sha256.Sum256(encoded)
}

// TelemetryEnactmentProjection is protected correlation metadata for one
// immutable enactment. Consumers must not turn its identities into metric
// labels.
type TelemetryEnactmentProjection struct {
	OperationID OperationID
	Kind        EnactmentKind
}

// DecisionTelemetryProjection is a typed, allowlisted projection of one
// committed decision. It deliberately exposes no arbitrary attribute map.
type DecisionTelemetryProjection struct {
	SchemaVersion    ProjectionSchemaVersion
	DecisionID       DecisionID
	AcceptedRevision TaskRevision
	Outcome          ProjectionOutcome
	RecordedAt       time.Time
	Enactments       []TelemetryEnactmentProjection
}

type TelemetryOutcome uint8

const (
	TelemetryAccepted TelemetryOutcome = iota + 1
	TelemetryRejected
	TelemetryDelivered
	TelemetryDeferred
	TelemetryReconciliationRequired
	TelemetryFailed
)

type TelemetryKind uint8

const (
	TelemetryDecision TelemetryKind = iota + 1
	TelemetryRuntimeEnactment
	TelemetryTaskWorkspaceLifecycleEnactment
	TelemetryPublicationEnactment
	TelemetrySchedulingEnactment
	TelemetryUsageEnactment
	TelemetryConfirmationEnactment
	TelemetryReconciliation
)

type TelemetryCategory uint8

const (
	TelemetryCategoryNone TelemetryCategory = iota + 1
	TelemetryCategoryAuthorization
	TelemetryCategoryIntegrity
	TelemetryCategoryStale
	TelemetryCategoryDependency
	TelemetryCategoryInvalid
	TelemetryCategoryUnknown
)

type MetricName uint8

const (
	MetricDecisionCount MetricName = iota + 1
	MetricOutboxCount
	MetricReconciliationCount
)

type MetricLabels struct {
	Outcome  TelemetryOutcome
	Kind     TelemetryKind
	Category TelemetryCategory
}

type MetricSample struct {
	Name   MetricName
	Labels MetricLabels
	Count  uint64
}

type metricRegistryEntry struct {
	name       MetricName
	outcomes   []TelemetryOutcome
	kinds      []TelemetryKind
	categories []TelemetryCategory
}

var registeredMetricPolicies = []metricRegistryEntry{
	{
		name:       MetricDecisionCount,
		outcomes:   []TelemetryOutcome{TelemetryAccepted, TelemetryRejected},
		kinds:      []TelemetryKind{TelemetryDecision},
		categories: registeredTelemetryCategories(),
	},
	{
		name: MetricOutboxCount,
		outcomes: []TelemetryOutcome{
			TelemetryAccepted, TelemetryDelivered, TelemetryDeferred,
			TelemetryReconciliationRequired, TelemetryFailed,
		},
		kinds: []TelemetryKind{
			TelemetryRuntimeEnactment, TelemetryTaskWorkspaceLifecycleEnactment,
			TelemetryPublicationEnactment, TelemetrySchedulingEnactment,
			TelemetryUsageEnactment, TelemetryConfirmationEnactment,
		},
		categories: registeredTelemetryCategories(),
	},
	{
		name: MetricReconciliationCount,
		outcomes: []TelemetryOutcome{
			TelemetryDelivered, TelemetryDeferred,
			TelemetryReconciliationRequired, TelemetryFailed,
		},
		kinds:      []TelemetryKind{TelemetryReconciliation},
		categories: registeredTelemetryCategories(),
	},
}

func registeredTelemetryCategories() []TelemetryCategory {
	return []TelemetryCategory{
		TelemetryCategoryNone, TelemetryCategoryAuthorization,
		TelemetryCategoryIntegrity, TelemetryCategoryStale,
		TelemetryCategoryDependency, TelemetryCategoryInvalid,
		TelemetryCategoryUnknown,
	}
}

// MetricSeriesUpperBound is derived from the same closed registry used to
// reject unregistered samples. No runtime identity can enlarge it.
func MetricSeriesUpperBound() uint64 {
	var bound uint64
	for _, policy := range registeredMetricPolicies {
		bound += uint64(len(policy.outcomes) * len(policy.kinds) * len(policy.categories))
	}
	return bound
}

func RegisteredMetricSample(sample MetricSample) bool {
	if sample.Count == 0 {
		return false
	}
	for _, policy := range registeredMetricPolicies {
		if sample.Name == policy.name && containsTelemetryOutcome(policy.outcomes, sample.Labels.Outcome) &&
			containsTelemetryKind(policy.kinds, sample.Labels.Kind) &&
			containsTelemetryCategory(policy.categories, sample.Labels.Category) {
			return true
		}
	}
	return false
}

func containsTelemetryOutcome(values []TelemetryOutcome, value TelemetryOutcome) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsTelemetryKind(values []TelemetryKind, value TelemetryKind) bool {
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

type StructuredLogEvent uint8

const (
	StructuredLogDecisionCommitted StructuredLogEvent = iota + 1
	StructuredLogOutboxCommitted
	StructuredLogReconciliationObserved
)

type StructuredLogSchemaVersion uint16

const StructuredLogSchemaV1 StructuredLogSchemaVersion = 1

type StructuredLogSeverity uint8

const (
	StructuredLogInfo StructuredLogSeverity = iota + 1
	StructuredLogWarning
)

type TelemetryModule uint8

const (
	TelemetryModuleTaskOrchestration TelemetryModule = iota + 1
)

// StructuredLogRecord has a closed allowlist and deliberately carries no
// business identity or arbitrary message/attribute field.
type StructuredLogRecord struct {
	SchemaVersion StructuredLogSchemaVersion
	Severity      StructuredLogSeverity
	Module        TelemetryModule
	Event         StructuredLogEvent
	Outcome       TelemetryOutcome
	Kind          TelemetryKind
	Category      TelemetryCategory
	RecordedAt    time.Time
}

type TraceSpanName uint8

const (
	TraceDecisionCommit TraceSpanName = iota + 1
	TraceOutboxCommit
	TraceReconciliation
)

// TraceSpanRecord admits only the protected correlations needed for exact
// operational diagnosis. It has no generic attributes or baggage.
type TraceSpanRecord struct {
	Module       TelemetryModule
	Name         TraceSpanName
	Outcome      TelemetryOutcome
	Kind         TelemetryKind
	Category     TelemetryCategory
	DecisionID   DecisionID
	OperationID  OperationID
	TaskRevision TaskRevision
	RecordedAt   time.Time
}

type ReconciliationTelemetryProjection struct {
	SchemaVersion ProjectionSchemaVersion
	DecisionID    DecisionID
	OperationID   OperationID
	TaskRevision  TaskRevision
	Outcome       TelemetryOutcome
	Category      TelemetryCategory
	ObservedAt    time.Time
}

type ReconciliationTelemetrySink interface {
	ProjectReconciliation(context.Context, ReconciliationTelemetryProjection) error
}

type TelemetrySnapshot struct {
	Metrics []MetricSample
	Logs    []StructuredLogRecord
	Traces  []TraceSpanRecord
}

// DeterministicTelemetry is a vendor-neutral, restart-free contract adapter.
// It stores typed projections for tests and local diagnostics only.
type DeterministicTelemetry struct {
	mu              sync.Mutex
	decisions       map[DecisionID]DecisionTelemetryProjection
	reconciliations map[OperationID]ReconciliationTelemetryProjection
	metrics         []MetricSample
	logs            []StructuredLogRecord
	traces          []TraceSpanRecord
}

func NewDeterministicTelemetry() *DeterministicTelemetry {
	return &DeterministicTelemetry{
		decisions:       make(map[DecisionID]DecisionTelemetryProjection),
		reconciliations: make(map[OperationID]ReconciliationTelemetryProjection),
	}
}

func (telemetry *DeterministicTelemetry) ProjectTelemetry(
	ctx context.Context,
	projection DecisionTelemetryProjection,
) error {
	if telemetry == nil || ctx == nil || ctx.Err() != nil ||
		!validDecisionTelemetryProjection(projection) {
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	projection.Enactments = append([]TelemetryEnactmentProjection(nil), projection.Enactments...)
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if existing, ok := telemetry.decisions[projection.DecisionID]; ok {
		if reflect.DeepEqual(existing, projection) {
			return nil
		}
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	telemetry.decisions[projection.DecisionID] = projection
	telemetry.metrics = append(telemetry.metrics, MetricSample{
		Name: MetricDecisionCount,
		Labels: MetricLabels{
			Outcome: TelemetryAccepted, Kind: TelemetryDecision, Category: TelemetryCategoryNone,
		},
		Count: 1,
	})
	telemetry.logs = append(telemetry.logs, StructuredLogRecord{
		SchemaVersion: StructuredLogSchemaV1, Severity: StructuredLogInfo,
		Module: TelemetryModuleTaskOrchestration,
		Event:  StructuredLogDecisionCommitted, Outcome: TelemetryAccepted,
		Kind: TelemetryDecision, Category: TelemetryCategoryNone, RecordedAt: projection.RecordedAt,
	})
	telemetry.traces = append(telemetry.traces, TraceSpanRecord{
		Module: TelemetryModuleTaskOrchestration,
		Name:   TraceDecisionCommit, Outcome: TelemetryAccepted,
		Kind: TelemetryDecision, Category: TelemetryCategoryNone,
		DecisionID: projection.DecisionID, TaskRevision: projection.AcceptedRevision,
		RecordedAt: projection.RecordedAt,
	})
	for _, enactment := range projection.Enactments {
		kind := telemetryKindForEnactment(enactment.Kind)
		telemetry.metrics = append(telemetry.metrics, MetricSample{
			Name: MetricOutboxCount,
			Labels: MetricLabels{
				Outcome: TelemetryAccepted, Kind: kind, Category: TelemetryCategoryNone,
			},
			Count: 1,
		})
		telemetry.logs = append(telemetry.logs, StructuredLogRecord{
			SchemaVersion: StructuredLogSchemaV1, Severity: StructuredLogInfo,
			Module: TelemetryModuleTaskOrchestration,
			Event:  StructuredLogOutboxCommitted, Outcome: TelemetryAccepted,
			Kind: kind, Category: TelemetryCategoryNone, RecordedAt: projection.RecordedAt,
		})
		telemetry.traces = append(telemetry.traces, TraceSpanRecord{
			Module: TelemetryModuleTaskOrchestration,
			Name:   TraceOutboxCommit, Outcome: TelemetryAccepted,
			Kind: kind, Category: TelemetryCategoryNone,
			DecisionID: projection.DecisionID, OperationID: enactment.OperationID,
			TaskRevision: projection.AcceptedRevision, RecordedAt: projection.RecordedAt,
		})
	}
	return nil
}

func (telemetry *DeterministicTelemetry) ProjectReconciliation(
	ctx context.Context,
	projection ReconciliationTelemetryProjection,
) error {
	if telemetry == nil || ctx == nil || ctx.Err() != nil ||
		!validReconciliationTelemetryProjection(projection) {
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if existing, ok := telemetry.reconciliations[projection.OperationID]; ok {
		if existing == projection {
			return nil
		}
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	telemetry.reconciliations[projection.OperationID] = projection
	labels := MetricLabels{
		Outcome:  projection.Outcome,
		Kind:     TelemetryReconciliation,
		Category: projection.Category,
	}
	telemetry.metrics = append(telemetry.metrics, MetricSample{
		Name: MetricReconciliationCount, Labels: labels, Count: 1,
	})
	telemetry.logs = append(telemetry.logs, StructuredLogRecord{
		SchemaVersion: StructuredLogSchemaV1, Severity: StructuredLogWarning,
		Module:  TelemetryModuleTaskOrchestration,
		Event:   StructuredLogReconciliationObserved,
		Outcome: projection.Outcome, Kind: labels.Kind, Category: projection.Category,
		RecordedAt: projection.ObservedAt,
	})
	telemetry.traces = append(telemetry.traces, TraceSpanRecord{
		Module:  TelemetryModuleTaskOrchestration,
		Name:    TraceReconciliation,
		Outcome: projection.Outcome, Kind: labels.Kind, Category: projection.Category,
		DecisionID: projection.DecisionID, OperationID: projection.OperationID,
		TaskRevision: projection.TaskRevision, RecordedAt: projection.ObservedAt,
	})
	return nil
}

func (telemetry *DeterministicTelemetry) Snapshot() TelemetrySnapshot {
	if telemetry == nil {
		return TelemetrySnapshot{}
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	return TelemetrySnapshot{
		Metrics: append([]MetricSample(nil), telemetry.metrics...),
		Logs:    append([]StructuredLogRecord(nil), telemetry.logs...),
		Traces:  append([]TraceSpanRecord(nil), telemetry.traces...),
	}
}

func validDecisionTelemetryProjection(projection DecisionTelemetryProjection) bool {
	if projection.SchemaVersion != ProjectionSchemaV1 ||
		!validOpaqueID(projection.DecisionID.value) || projection.AcceptedRevision == 0 ||
		projection.Outcome != ProjectionAccepted || projection.RecordedAt.IsZero() {
		return false
	}
	for _, enactment := range projection.Enactments {
		if !validOpaqueID(enactment.OperationID.value) ||
			telemetryKindForEnactment(enactment.Kind) == 0 {
			return false
		}
	}
	return true
}

func validReconciliationTelemetryProjection(projection ReconciliationTelemetryProjection) bool {
	return projection.SchemaVersion == ProjectionSchemaV1 &&
		validOpaqueID(projection.DecisionID.value) && validOpaqueID(projection.OperationID.value) &&
		projection.TaskRevision > 0 && projection.Outcome == TelemetryReconciliationRequired &&
		projection.Category >= TelemetryCategoryNone &&
		projection.Category <= TelemetryCategoryUnknown && !projection.ObservedAt.IsZero()
}

func telemetryKindForEnactment(kind EnactmentKind) TelemetryKind {
	switch kind {
	case EnactmentRuntimeExecution:
		return TelemetryRuntimeEnactment
	case EnactmentTaskWorkspaceLifecycle:
		return TelemetryTaskWorkspaceLifecycleEnactment
	case EnactmentArtifactPublication:
		return TelemetryPublicationEnactment
	case EnactmentScheduling:
		return TelemetrySchedulingEnactment
	case EnactmentUsageAccounting:
		return TelemetryUsageEnactment
	case EnactmentPresentConfirmationGate:
		return TelemetryConfirmationEnactment
	default:
		return 0
	}
}

type ExternalAuditProjectionSink interface {
	ProjectExternalAudit(context.Context, ExternalAuditProjection) error
}

type DecisionTelemetryProjectionSink interface {
	ProjectTelemetry(context.Context, DecisionTelemetryProjection) error
}

type ExternalAuditProjectionSinkFunc func(context.Context, ExternalAuditProjection) error

func (function ExternalAuditProjectionSinkFunc) ProjectExternalAudit(
	ctx context.Context,
	projection ExternalAuditProjection,
) error {
	return function(ctx, projection)
}

type DecisionTelemetryProjectionSinkFunc func(context.Context, DecisionTelemetryProjection) error

func (function DecisionTelemetryProjectionSinkFunc) ProjectTelemetry(
	ctx context.Context,
	projection DecisionTelemetryProjection,
) error {
	return function(ctx, projection)
}

type DecisionProjectionConfig struct {
	ExternalAudit ExternalAuditProjectionSink
	Telemetry     DecisionTelemetryProjectionSink
}

type ProjectionErrorCode uint8

const (
	ProjectionInvalidConfiguration ProjectionErrorCode = iota + 1
	ProjectionInvalidFact
	ProjectionUnavailable
)

// ProjectionError never retains a sink error or caller-provided detail.
type ProjectionError struct {
	code ProjectionErrorCode
}

func (failure *ProjectionError) Error() string {
	if failure == nil || failure.code == ProjectionInvalidConfiguration {
		return "task orchestration projection configuration is invalid"
	}
	if failure.code == ProjectionInvalidFact {
		return "task orchestration projection fact is invalid"
	}
	return "task orchestration projection is unavailable"
}

func (failure *ProjectionError) Code() ProjectionErrorCode {
	if failure == nil {
		return ProjectionInvalidConfiguration
	}
	return failure.code
}

// DecisionProjectionAdapter projects committed facts after their transaction.
// Sink delivery errors are reported only as a safe projection failure; callers
// must never use them to roll back or repair the protected Decision.
type DecisionProjectionAdapter struct {
	externalAudit ExternalAuditProjectionSink
	telemetry     DecisionTelemetryProjectionSink
}

func NewDecisionProjectionAdapter(
	config DecisionProjectionConfig,
) (*DecisionProjectionAdapter, error) {
	if config.ExternalAudit == nil || config.Telemetry == nil {
		return nil, &ProjectionError{code: ProjectionInvalidConfiguration}
	}
	return &DecisionProjectionAdapter{
		externalAudit: config.ExternalAudit,
		telemetry:     config.Telemetry,
	}, nil
}

func (adapter *DecisionProjectionAdapter) ObserveCommittedDecision(
	ctx context.Context,
	decision TransitionDecision,
) error {
	if adapter == nil || adapter.externalAudit == nil || adapter.telemetry == nil ||
		ctx == nil || !validProjectionDecision(decision) {
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	audit, telemetry := decisionProjections(decision)
	auditErr := adapter.externalAudit.ProjectExternalAudit(ctx, audit)
	telemetryErr := adapter.telemetry.ProjectTelemetry(ctx, telemetry)
	if auditErr != nil || telemetryErr != nil {
		return &ProjectionError{code: ProjectionUnavailable}
	}
	return nil
}

func decisionProjections(
	decision TransitionDecision,
) (ExternalAuditProjection, DecisionTelemetryProjection) {
	audit := ExternalAuditProjection{
		SchemaVersion:     ProjectionSchemaV1,
		AuditFactID:       decision.MandatoryAuditFactRef.AuditFactID,
		DecisionID:        decision.DecisionID,
		DecisionRequestID: decision.DecisionRequestID,
		AcceptedRevision:  decision.AcceptedTaskRevision,
		Outcome:           ProjectionAccepted,
		RecordedAt:        decision.CommittedAt,
	}
	audit.CanonicalDigest = ExternalAuditProjectionDigest(audit)
	enactments := make([]TelemetryEnactmentProjection, len(decision.EnactmentRefs))
	for index, enactment := range decision.EnactmentRefs {
		enactments[index] = TelemetryEnactmentProjection{
			OperationID: enactment.OperationID,
			Kind:        enactment.Kind,
		}
	}
	telemetry := DecisionTelemetryProjection{
		SchemaVersion:    ProjectionSchemaV1,
		DecisionID:       decision.DecisionID,
		AcceptedRevision: decision.AcceptedTaskRevision,
		Outcome:          ProjectionAccepted,
		RecordedAt:       decision.CommittedAt,
		Enactments:       enactments,
	}
	return audit, telemetry
}

func (adapter *DecisionProjectionAdapter) projectCommittedDecision(
	ctx context.Context,
	decision TransitionDecision,
	externalAuditPending bool,
	telemetryPending bool,
) (externalAuditDelivered bool, telemetryDelivered bool) {
	if adapter == nil || ctx == nil || ctx.Err() != nil || !validProjectionDecision(decision) {
		return false, false
	}
	audit, telemetry := decisionProjections(decision)
	externalAuditDelivered = !externalAuditPending
	telemetryDelivered = !telemetryPending
	if externalAuditPending {
		externalAuditDelivered = adapter.externalAudit.ProjectExternalAudit(ctx, audit) == nil
	}
	if telemetryPending {
		telemetryDelivered = adapter.telemetry.ProjectTelemetry(ctx, telemetry) == nil
	}
	return externalAuditDelivered, telemetryDelivered
}

func validProjectionDecision(decision TransitionDecision) bool {
	if !validOpaqueID(decision.DecisionID.value) ||
		!validOpaqueID(decision.DecisionRequestID.value) ||
		!validOpaqueID(decision.MandatoryAuditFactRef.AuditFactID.value) ||
		decision.AcceptedTaskRevision == 0 ||
		decision.AcceptedTaskRevision != decision.PreviousTaskRevision+1 ||
		decision.CanonicalRequestDigest == (CanonicalRequestDigest{}) ||
		decision.CommittedAt.IsZero() || projectionDecisionOutcomeInvalid(decision) {
		return false
	}
	for _, enactment := range decision.EnactmentRefs {
		if !validOpaqueID(enactment.OperationID.value) || !validEnactmentKind(enactment.Kind) ||
			enactment.PayloadDigest == (EnactmentPayloadDigest{}) ||
			enactment.ActivityGeneration == 0 || enactment.Fence == nil ||
			!validOpaqueID(enactment.CausationID.value) {
			return false
		}
	}
	return true
}

func projectionDecisionOutcomeInvalid(decision TransitionDecision) bool {
	return decision.TaskProjection.TaskRevision != decision.AcceptedTaskRevision ||
		decision.TaskProjection.TaskID == (TaskID{})
}
