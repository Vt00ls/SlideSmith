package runtimeexecution

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// TelemetrySchemaVersion identifies bounded, content-free telemetry and
// external-audit projection envelopes. Projections are always rebuildable from
// retained authoritative facts; they never replace the authoritative fact and
// never drive Runtime state, capacity, or cleanup authority.
type TelemetrySchemaVersion uint32

const TelemetrySchemaV1 TelemetrySchemaVersion = 1 << 16

func (version TelemetrySchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version TelemetrySchemaVersion) Minor() uint16 { return uint16(version) }

// TelemetryCategory is the closed safe-error/outcome category admitted into
// telemetry. It mirrors the Decision 88 safe-error taxonomy and never carries
// raw cause text, locators, or cross-Workspace existence.
type TelemetryCategory uint8

const (
	TelemetryCategoryNone TelemetryCategory = iota + 1
	TelemetryCategoryAuthorization
	TelemetryCategoryInvalid
	TelemetryCategoryConflict
	TelemetryCategoryStale
	TelemetryCategoryBinding
	TelemetryCategoryIntegrity
	TelemetryCategoryAdmission
	TelemetryCategoryAdapter
	TelemetryCategoryAgentTool
	TelemetryCategoryCancelDeadline
	TelemetryCategoryWorkerNode
	TelemetryCategoryCleanup
	TelemetryCategoryDependency
	TelemetryCategoryUnknown
)

func validTelemetryCategory(value TelemetryCategory) bool {
	return value >= TelemetryCategoryNone && value <= TelemetryCategoryUnknown
}

// MetricOperation is a closed bounded dimension. Exact operation identities
// never become metric labels; only this registered enum may.
type MetricOperation uint8

const (
	MetricOperationNone MetricOperation = iota
	MetricOperationStart
	MetricOperationCancel
	MetricOperationInspect
	MetricOperationLease
	MetricOperationTerminal
	MetricOperationReconciliation
	MetricOperationMaintenance
	MetricOperationCleanup
	MetricOperationPrerequisite
	MetricOperationGateway
	MetricOperationDispatch
	MetricOperationEvidence
)

func validMetricOperation(value MetricOperation) bool {
	return value >= MetricOperationNone && value <= MetricOperationEvidence
}

func (operation MetricOperation) String() string {
	switch operation {
	case MetricOperationStart:
		return "start"
	case MetricOperationCancel:
		return "cancel"
	case MetricOperationInspect:
		return "inspect"
	case MetricOperationLease:
		return "lease"
	case MetricOperationTerminal:
		return "terminal"
	case MetricOperationReconciliation:
		return "reconciliation"
	case MetricOperationMaintenance:
		return "maintenance"
	case MetricOperationCleanup:
		return "cleanup"
	case MetricOperationPrerequisite:
		return "prerequisite"
	case MetricOperationGateway:
		return "gateway"
	case MetricOperationDispatch:
		return "dispatch"
	case MetricOperationEvidence:
		return "evidence"
	default:
		return "none"
	}
}

// MetricState mirrors the closed RuntimeState set. Exact Runtime Run
// revisions never become metric labels.
type MetricState uint8

const (
	MetricStateNone MetricState = iota
	MetricStateCreated
	MetricStateAccepted
	MetricStateWaitingForLease
	MetricStateReconciling
	MetricStatePreparingPrerequisites
	MetricStateStarting
	MetricStateRunning
	MetricStateStopping
	MetricStateTerminal
)

func validMetricState(value MetricState) bool {
	return value >= MetricStateNone && value <= MetricStateTerminal
}

func runtimeStateMetricState(state RuntimeState) MetricState {
	switch state {
	case RuntimeCreated:
		return MetricStateCreated
	case RuntimeAccepted:
		return MetricStateAccepted
	case RuntimeWaitingForLease:
		return MetricStateWaitingForLease
	case RuntimeReconciling:
		return MetricStateReconciling
	case RuntimePreparingPrerequisites:
		return MetricStatePreparingPrerequisites
	case RuntimeStarting:
		return MetricStateStarting
	case RuntimeRunning:
		return MetricStateRunning
	case RuntimeStopping:
		return MetricStateStopping
	case RuntimeTerminal:
		return MetricStateTerminal
	default:
		return MetricStateNone
	}
}

// MetricOutcome mirrors the closed immutable RuntimeOutcome set. Terminal
// outcomes are execution facts; a metric never invents a zero for an unknown
// source.
type MetricOutcome uint8

const (
	MetricOutcomeNone MetricOutcome = iota
	MetricOutcomeSucceeded
	MetricOutcomeFailed
	MetricOutcomeCancelled
	MetricOutcomeTimedOut
	MetricOutcomeLost
	MetricOutcomeRejected
)

func validMetricOutcome(value MetricOutcome) bool {
	return value >= MetricOutcomeNone && value <= MetricOutcomeRejected
}

func runtimeOutcomeMetricOutcome(outcome RuntimeOutcome) MetricOutcome {
	switch outcome {
	case RuntimeSucceeded:
		return MetricOutcomeSucceeded
	case RuntimeFailed:
		return MetricOutcomeFailed
	case RuntimeCancelled:
		return MetricOutcomeCancelled
	case RuntimeTimedOut:
		return MetricOutcomeTimedOut
	case RuntimeLost:
		return MetricOutcomeLost
	case RuntimeRejected:
		return MetricOutcomeRejected
	default:
		return MetricOutcomeNone
	}
}

// MetricWorker is the closed worker-class dimension (Agent or Tool). Worker
// identities never become labels.
type MetricWorker uint8

const (
	MetricWorkerNone MetricWorker = iota
	MetricWorkerAgent
	MetricWorkerTool
)

func validMetricWorker(value MetricWorker) bool {
	return value >= MetricWorkerNone && value <= MetricWorkerTool
}

// MetricAdapter is the closed adapter-class dimension.
type MetricAdapter uint8

const (
	MetricAdapterNone MetricAdapter = iota
	MetricAdapterSync
	MetricAdapterPoll
	MetricAdapterCallback
	MetricAdapterQueue
	MetricAdapterAgentCompose
	MetricAdapterToolExecutor
	MetricAdapterGateway
	MetricAdapterScheduler
)

func validMetricAdapter(value MetricAdapter) bool {
	return value >= MetricAdapterNone && value <= MetricAdapterScheduler
}

// MetricEvidence is the closed evidence-state dimension.
type MetricEvidence uint8

const (
	MetricEvidenceNone MetricEvidence = iota
	MetricEvidenceAccepted
	MetricEvidenceRejected
	MetricEvidenceStale
	MetricEvidenceDeferred
	MetricEvidencePending
)

func validMetricEvidence(value MetricEvidence) bool {
	return value >= MetricEvidenceNone && value <= MetricEvidencePending
}

type MetricName uint8

const (
	MetricRunStateCount MetricName = iota + 1
	MetricTerminalOutcomeCount
	MetricLeaseOperationCount
	MetricSafeErrorCount
	MetricEvidenceStateCount
	MetricCleanupDebtCount
	MetricCardinalityRejectionCount
)

// MetricLabels is strictly typed. Every label is a closed enum; a business,
// high-cardinality, or free-form value cannot even be expressed, and any
// unregistered combination is rejected by the registry. The registered
// dimensions mirror Decision 86: module, operation, state/outcome, worker,
// adapter class, evidence state, and safe-error category. Exact capability and
// Resource Class identities are deployment-bounded and stay in protected
// diagnostics — they are deliberately not primary labels, exactly like
// User/Workspace/Task/run/operation/node/lease/evidence/debt/path/locator/
// digest/trace/free-form values.
type MetricLabels struct {
	Operation MetricOperation
	State     MetricState
	Outcome   MetricOutcome
	Worker    MetricWorker
	Adapter   MetricAdapter
	Evidence  MetricEvidence
	Category  TelemetryCategory
}

type MetricSample struct {
	Name   MetricName
	Labels MetricLabels
	Count  uint64
}

type metricRegistryEntry struct {
	name      MetricName
	operation []MetricOperation
	state     []MetricState
	outcome   []MetricOutcome
	worker    []MetricWorker
	adapter   []MetricAdapter
	evidence  []MetricEvidence
	category  []TelemetryCategory
}

func metricOperations() []MetricOperation {
	return []MetricOperation{
		MetricOperationStart, MetricOperationCancel, MetricOperationInspect,
		MetricOperationLease, MetricOperationTerminal, MetricOperationReconciliation,
		MetricOperationMaintenance, MetricOperationCleanup, MetricOperationPrerequisite,
		MetricOperationGateway, MetricOperationDispatch, MetricOperationEvidence,
	}
}

func metricStates() []MetricState {
	return []MetricState{
		MetricStateCreated, MetricStateAccepted, MetricStateWaitingForLease,
		MetricStateReconciling, MetricStatePreparingPrerequisites, MetricStateStarting,
		MetricStateRunning, MetricStateStopping, MetricStateTerminal,
	}
}

func terminalMetricOutcomes() []MetricOutcome {
	return []MetricOutcome{
		MetricOutcomeSucceeded, MetricOutcomeFailed, MetricOutcomeCancelled,
		MetricOutcomeTimedOut, MetricOutcomeLost, MetricOutcomeRejected,
	}
}

func telemetryCategories() []TelemetryCategory {
	return []TelemetryCategory{
		TelemetryCategoryNone, TelemetryCategoryAuthorization, TelemetryCategoryInvalid,
		TelemetryCategoryConflict, TelemetryCategoryStale, TelemetryCategoryBinding,
		TelemetryCategoryIntegrity, TelemetryCategoryAdmission, TelemetryCategoryAdapter,
		TelemetryCategoryAgentTool, TelemetryCategoryCancelDeadline, TelemetryCategoryWorkerNode,
		TelemetryCategoryCleanup, TelemetryCategoryDependency, TelemetryCategoryUnknown,
	}
}

func registeredMetricPolicies() []metricRegistryEntry {
	return []metricRegistryEntry{
		{
			name:      MetricRunStateCount,
			operation: metricOperations(),
			state:     metricStates(),
			category:  telemetryCategories(),
		},
		{
			name:      MetricTerminalOutcomeCount,
			operation: []MetricOperation{MetricOperationTerminal},
			state:     []MetricState{MetricStateTerminal},
			outcome:   terminalMetricOutcomes(),
			category:  telemetryCategories(),
		},
		{
			name:      MetricLeaseOperationCount,
			operation: []MetricOperation{MetricOperationLease},
			state:     metricStates(),
			category:  telemetryCategories(),
		},
		{
			name:      MetricSafeErrorCount,
			operation: metricOperations(),
			state:     metricStates(),
			category:  telemetryCategories(),
		},
		{
			name:      MetricEvidenceStateCount,
			operation: []MetricOperation{MetricOperationEvidence},
			evidence: []MetricEvidence{
				MetricEvidenceAccepted, MetricEvidenceRejected, MetricEvidenceStale,
				MetricEvidenceDeferred, MetricEvidencePending,
			},
		},
		{
			name:      MetricCleanupDebtCount,
			operation: []MetricOperation{MetricOperationCleanup},
			category:  telemetryCategories(),
		},
		{
			name:      MetricCardinalityRejectionCount,
			operation: metricOperations(),
			category:  []TelemetryCategory{TelemetryCategoryInvalid},
		},
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

func containsMetricWorker(values []MetricWorker, value MetricWorker) bool {
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

func containsMetricEvidence(values []MetricEvidence, value MetricEvidence) bool {
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

// RegisteredMetricSample reports whether a sample is inside the closed metric
// registry. Unregistered names, invalid label values, or label combinations
// outside the per-metric policy are rejected; the caller must emit a bounded
// cardinality-rejection counter instead.
func RegisteredMetricSample(sample MetricSample) bool {
	if sample.Count == 0 {
		return false
	}
	for _, policy := range registeredMetricPolicies() {
		if sample.Name != policy.name {
			continue
		}
		if !allowlistedMetricDimension(policy.operation, MetricOperation(sample.Labels.Operation)) ||
			!allowlistedMetricDimension(policy.state, MetricState(sample.Labels.State)) ||
			!allowlistedMetricDimension(policy.outcome, MetricOutcome(sample.Labels.Outcome)) ||
			!allowlistedMetricDimension(policy.worker, MetricWorker(sample.Labels.Worker)) ||
			!allowlistedMetricDimension(policy.adapter, MetricAdapter(sample.Labels.Adapter)) ||
			!allowlistedMetricDimension(policy.evidence, MetricEvidence(sample.Labels.Evidence)) ||
			!allowlistedMetricDimension(policy.category, TelemetryCategory(sample.Labels.Category)) {
			return false
		}
		return true
	}
	return false
}

// allowlistedMetricDimension allows the zero/none value always and otherwise
// requires membership in the per-metric closed policy.
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
		bound += uint64(len(policy.operation)+1) * uint64(len(policy.state)+1) *
			uint64(len(policy.outcome)+1) * uint64(len(policy.worker)+1) *
			uint64(len(policy.adapter)+1) * uint64(len(policy.evidence)+1) *
			uint64(len(policy.category)+1)
	}
	return bound
}

type StructuredLogEvent uint8

const (
	StructuredLogStartCommitted StructuredLogEvent = iota + 1
	StructuredLogCancelCommitted
	StructuredLogLeaseCommitted
	StructuredLogTerminalCommitted
	StructuredLogReconciliationObserved
	StructuredLogOperationCommitted
)

type StructuredLogSchemaVersion uint16

const StructuredLogSchemaV1 StructuredLogSchemaVersion = 1

type StructuredLogSeverity uint8

const (
	StructuredLogInfo StructuredLogSeverity = iota + 1
	StructuredLogWarning
)

type TelemetryModule uint8

const TelemetryModuleRuntimeExecution TelemetryModule = iota + 1

// StructuredLogRecord has a closed allowlist and deliberately carries no
// business identity, free-form message, path, locator, credential, or content.
// Redaction happens before buffering or export by construction: there is no
// field that could hold a redactable value.
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
	TraceStartCommit TraceSpanName = iota + 1
	TraceCancelCommit
	TraceLeaseCommit
	TraceTerminalCommit
	TraceReconciliation
	TraceOperationCommit
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
	RuntimeRunID    RuntimeRunID
	OperationID     OperationID
	RuntimeRevision RuntimeRevision
	TraceID         TraceID
	RecordedAt      time.Time
}

// RuntimeTelemetryProjection is the bounded, content-free post-commit
// telemetry envelope. It carries only closed enums plus allowlisted protected
// correlation for trace diagnosis; it never carries content, paths, locators,
// credentials, or arbitrary attributes.
type RuntimeTelemetryProjection struct {
	SchemaVersion   TelemetrySchemaVersion
	DecisionID      RuntimeDecisionID
	RuntimeRunID    RuntimeRunID
	OperationID     OperationID
	RuntimeRevision RuntimeRevision
	Operation       MetricOperation
	State           MetricState
	Outcome         MetricOutcome
	Category        TelemetryCategory
	TraceID         TraceID
	RecordedAt      time.Time
}

func validRuntimeTelemetryProjection(projection RuntimeTelemetryProjection) bool {
	return projection.SchemaVersion == TelemetrySchemaV1 &&
		validOpaqueID(projection.DecisionID.String()) &&
		validOpaqueID(projection.RuntimeRunID.String()) &&
		validOpaqueID(projection.OperationID.String()) &&
		projection.RuntimeRevision > 0 &&
		validMetricOperation(projection.Operation) &&
		validMetricState(projection.State) &&
		validMetricOutcome(projection.Outcome) &&
		validTelemetryCategory(projection.Category) &&
		!projection.RecordedAt.IsZero()
}

// TelemetrySink receives bounded content-free telemetry projections after the
// authoritative transaction commits. Sink failure never rolls back the
// committed decision; it only produces a durable backlog/degraded signal.
type TelemetrySink interface {
	ProjectTelemetry(context.Context, RuntimeTelemetryProjection) error
}

type TelemetrySinkFunc func(context.Context, RuntimeTelemetryProjection) error

func (function TelemetrySinkFunc) ProjectTelemetry(
	ctx context.Context,
	projection RuntimeTelemetryProjection,
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
		return "runtime execution projection configuration is invalid"
	}
	if failure.code == ProjectionInvalidFact {
		return "runtime execution projection fact is invalid"
	}
	return "runtime execution projection is unavailable"
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

type runtimeScopedMetricSample struct {
	runtimeRunID RuntimeRunID
	sample       MetricSample
}

type runtimeScopedLogRecord struct {
	runtimeRunID RuntimeRunID
	record       StructuredLogRecord
}

// DeterministicTelemetry is a vendor-neutral, restart-free contract adapter
// for tests and local diagnostics. It stores typed projections only and never
// accepts arbitrary attributes or business-ID labels.
type DeterministicTelemetry struct {
	mu          sync.Mutex
	projections map[RuntimeRunID][]RuntimeTelemetryProjection
	metrics     []runtimeScopedMetricSample
	logs        []runtimeScopedLogRecord
	traces      []TraceSpanRecord
	now         func() time.Time
}

func NewDeterministicTelemetry(config DeterministicTelemetryConfig) *DeterministicTelemetry {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &DeterministicTelemetry{
		projections: make(map[RuntimeRunID][]RuntimeTelemetryProjection),
		now:         now,
	}
}

// ProjectTelemetry records one bounded content-free telemetry projection.
// Invalid projections are rejected with a safe ProjectionError and a bounded
// cardinality-rejection metric.
func (telemetry *DeterministicTelemetry) ProjectTelemetry(
	ctx context.Context,
	projection RuntimeTelemetryProjection,
) error {
	if telemetry == nil || ctx == nil || ctx.Err() != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if !validRuntimeTelemetryProjection(projection) {
		telemetry.recordMetricRejection(projection.RuntimeRunID, projection.Operation)
		return newProjectionError(ProjectionInvalidFact)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	existing := telemetry.projections[projection.RuntimeRunID]
	for _, retained := range existing {
		if reflect.DeepEqual(retained, projection) {
			return nil
		}
	}
	telemetry.projections[projection.RuntimeRunID] = append(existing, projection)
	metric, logRecord, trace := telemetrySignalsForProjection(projection)
	telemetry.metrics = append(telemetry.metrics, runtimeScopedMetricSample{
		runtimeRunID: projection.RuntimeRunID, sample: metric,
	})
	telemetry.logs = append(telemetry.logs, runtimeScopedLogRecord{
		runtimeRunID: projection.RuntimeRunID, record: logRecord,
	})
	telemetry.traces = append(telemetry.traces, trace)
	return nil
}

func telemetrySignalsForProjection(
	projection RuntimeTelemetryProjection,
) (MetricSample, StructuredLogRecord, TraceSpanRecord) {
	name := MetricRunStateCount
	if projection.State == MetricStateTerminal && projection.Outcome != MetricOutcomeNone {
		name = MetricTerminalOutcomeCount
	}
	labels := MetricLabels{
		Operation: projection.Operation, State: projection.State,
		Outcome: projection.Outcome, Category: projection.Category,
	}
	severity := StructuredLogInfo
	event := logEventForMetricOperation(projection.Operation)
	if projection.Category != TelemetryCategoryNone {
		severity = StructuredLogWarning
	}
	return MetricSample{Name: name, Labels: labels, Count: 1},
		StructuredLogRecord{
			SchemaVersion: StructuredLogSchemaV1, Severity: severity,
			Module: TelemetryModuleRuntimeExecution, Event: event,
			Operation: projection.Operation, State: projection.State,
			Outcome: projection.Outcome, Category: projection.Category,
			RecordedAt: projection.RecordedAt,
		},
		TraceSpanRecord{
			Module: TelemetryModuleRuntimeExecution, Name: traceNameForMetricOperation(projection.Operation),
			Operation: projection.Operation, State: projection.State,
			Outcome: projection.Outcome, Category: projection.Category,
			RuntimeRunID: projection.RuntimeRunID, OperationID: projection.OperationID,
			RuntimeRevision: projection.RuntimeRevision, TraceID: projection.TraceID,
			RecordedAt: projection.RecordedAt,
		}
}

func logEventForMetricOperation(operation MetricOperation) StructuredLogEvent {
	switch operation {
	case MetricOperationStart:
		return StructuredLogStartCommitted
	case MetricOperationCancel:
		return StructuredLogCancelCommitted
	case MetricOperationLease:
		return StructuredLogLeaseCommitted
	case MetricOperationTerminal:
		return StructuredLogTerminalCommitted
	case MetricOperationReconciliation:
		return StructuredLogReconciliationObserved
	default:
		return StructuredLogOperationCommitted
	}
}

func traceNameForMetricOperation(operation MetricOperation) TraceSpanName {
	switch operation {
	case MetricOperationStart:
		return TraceStartCommit
	case MetricOperationCancel:
		return TraceCancelCommit
	case MetricOperationLease:
		return TraceLeaseCommit
	case MetricOperationTerminal:
		return TraceTerminalCommit
	case MetricOperationReconciliation:
		return TraceReconciliation
	default:
		return TraceOperationCommit
	}
}

func (telemetry *DeterministicTelemetry) recordMetricRejection(
	runtimeRunID RuntimeRunID,
	operation MetricOperation,
) {
	if !validMetricOperation(operation) {
		operation = MetricOperationNone
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	telemetry.metrics = append(telemetry.metrics, runtimeScopedMetricSample{
		runtimeRunID: runtimeRunID,
		sample: MetricSample{
			Name: MetricCardinalityRejectionCount,
			Labels: MetricLabels{
				Operation: operation, Category: TelemetryCategoryInvalid,
			},
			Count: 1,
		},
	})
}

// TelemetryDiagnosticQuery is a reason-bound, exact-reference, bounded query
// for the protected read-only telemetry diagnostic surface. It can never
// enumerate the whole population.
type TelemetryDiagnosticQuery struct {
	authority    RuntimeAuthority
	runtimeRunID RuntimeRunID
	reason       DiagnosticReason
	limit        uint32
}

func NewTelemetryDiagnosticQuery(
	authority RuntimeAuthority,
	runtimeRunID RuntimeRunID,
	reason DiagnosticReason,
	limit uint32,
) TelemetryDiagnosticQuery {
	return TelemetryDiagnosticQuery{authority: authority, runtimeRunID: runtimeRunID, reason: reason, limit: limit}
}

type TelemetrySnapshot struct {
	Metrics []MetricSample
	Logs    []StructuredLogRecord
	Traces  []TraceSpanRecord
}

// Snapshot returns a bounded, content-free, exact-run-scoped view of the
// telemetry projections. It is read-only and cannot mutate Runtime state.
func (telemetry *DeterministicTelemetry) Snapshot(
	ctx context.Context,
	query TelemetryDiagnosticQuery,
) (TelemetrySnapshot, error) {
	if telemetry == nil || ctx == nil || ctx.Err() != nil {
		return TelemetrySnapshot{}, newProjectionError(ProjectionUnavailable)
	}
	if !validAuthority(query.authority) || query.authority.kind != AuthorityAdministrator ||
		!validOpaqueID(query.runtimeRunID.String()) ||
		query.reason < DiagnosticReasonCleanupHealth || query.reason > DiagnosticReasonCapacityInvestigation ||
		query.limit == 0 || query.limit > 100 {
		return TelemetrySnapshot{}, newError(ErrorAuthorizationDenied)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	limit := int(query.limit)
	metrics := make([]MetricSample, 0, limit)
	for _, observation := range telemetry.metrics {
		if observation.runtimeRunID != query.runtimeRunID {
			continue
		}
		metrics = append(metrics, observation.sample)
		if len(metrics) == limit {
			break
		}
	}
	logs := make([]StructuredLogRecord, 0, limit)
	for _, observation := range telemetry.logs {
		if observation.runtimeRunID != query.runtimeRunID {
			continue
		}
		logs = append(logs, observation.record)
		if len(logs) == limit {
			break
		}
	}
	traces := make([]TraceSpanRecord, 0, limit)
	for _, trace := range telemetry.traces {
		if trace.RuntimeRunID != query.runtimeRunID {
			continue
		}
		traces = append(traces, trace)
		if len(traces) == limit {
			break
		}
	}
	return TelemetrySnapshot{Metrics: metrics, Logs: logs, Traces: traces}, nil
}

// telemetryProjectionFromAudit derives one bounded content-free telemetry
// projection from an authoritative audit state that was committed with the
// protected decision. It never reads raw worker, vendor, or provider detail.
func telemetryProjectionFromAudit(
	fact ProjectionFact,
	state postgresMandatoryAuditState,
) (RuntimeTelemetryProjection, error) {
	if fact.DecisionID == (RuntimeDecisionID{}) || !validOpaqueID(fact.RuntimeRunID.String()) ||
		!validOpaqueID(fact.OperationID.String()) || fact.RuntimeRevision == 0 ||
		!validPostgresMandatoryAuditState(state) {
		return RuntimeTelemetryProjection{}, newError(ErrorIntegrityConflict)
	}
	recordedAt, err := time.Parse(canonicalTimeFormat, state.RecordedAt)
	if err != nil {
		return RuntimeTelemetryProjection{}, newError(ErrorIntegrityConflict)
	}
	return RuntimeTelemetryProjection{
		SchemaVersion:   TelemetrySchemaV1,
		DecisionID:      fact.DecisionID,
		RuntimeRunID:    fact.RuntimeRunID,
		OperationID:     fact.OperationID,
		RuntimeRevision: fact.RuntimeRevision,
		Operation:       metricOperationForAuditAction(state.Action),
		State:           runtimeStateMetricState(state.AfterState),
		Outcome:         metricOutcomeForAuditAction(state),
		Category:        TelemetryCategoryNone,
		RecordedAt:      recordedAt.UTC(),
	}, nil
}

func metricOperationForAuditAction(action postgresMandatoryAuditAction) MetricOperation {
	switch action {
	case postgresAuditStartAccepted:
		return MetricOperationStart
	case postgresAuditCancelAccepted:
		return MetricOperationCancel
	case postgresAuditReconciliationRequired:
		return MetricOperationReconciliation
	case postgresAuditPreLeaseTerminal, postgresAuditPostLeaseDeadline:
		return MetricOperationTerminal
	case postgresAuditLeaseCommitted:
		return MetricOperationLease
	case postgresAuditPostLeasePrerequisiteRejected:
		return MetricOperationPrerequisite
	case postgresAuditEvidenceTerminal:
		return MetricOperationEvidence
	default:
		return MetricOperationNone
	}
}

func metricOutcomeForAuditAction(state postgresMandatoryAuditState) MetricOutcome {
	if state.AfterState != RuntimeTerminal {
		return MetricOutcomeNone
	}
	switch state.Action {
	case postgresAuditPreLeaseTerminal:
		if PreLeaseTerminalReason(state.ReasonCode) == PreLeaseTerminalRuntimeDeadline {
			return MetricOutcomeTimedOut
		}
		return MetricOutcomeRejected
	case postgresAuditPostLeaseDeadline:
		return MetricOutcomeTimedOut
	case postgresAuditPostLeasePrerequisiteRejected:
		return MetricOutcomeRejected
	case postgresAuditEvidenceTerminal:
		return runtimeOutcomeMetricOutcome(RuntimeOutcome(state.ReasonCode))
	default:
		return MetricOutcomeNone
	}
}
