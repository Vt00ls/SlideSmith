package taskworkspace

import (
	"context"
	"errors"
	"sort"
	"sync"
)

type (
	ProjectionSchemaRevision  uint64
	ProjectionFactID          string
	ProjectionFactRevision    uint64
	ProjectionFactKind        string
	ProjectionOperation       string
	ProjectionResult          string
	ProjectionLifecycleState  string
	ProjectionResourceClass   string
	ProjectionAdapterClass    string
	MetricName                string
	MetricModule              string
	LogEventName              string
	TraceOperationName        string
	AuditDeliveryFactID       string
	AuditDeliveryResult       string
	LogSchemaRevision         uint64
	LogSeverity               string
	ProjectionSourcePartition string
)

const (
	ProjectionSchemaV1 ProjectionSchemaRevision = 1
	ProjectionSchemaV2 ProjectionSchemaRevision = 2

	ProjectionFactLifecycle      ProjectionFactKind = "lifecycle"
	ProjectionFactIntegrity      ProjectionFactKind = "integrity"
	ProjectionFactOperation      ProjectionFactKind = "operation"
	ProjectionFactRetention      ProjectionFactKind = "retention"
	ProjectionFactCleanupDebt    ProjectionFactKind = "cleanup_debt"
	ProjectionFactMandatoryAudit ProjectionFactKind = "mandatory_audit"

	ProjectionOperationConfirm     ProjectionOperation = "confirm"
	ProjectionOperationMaterialize ProjectionOperation = "materialize"
	ProjectionOperationOpenView    ProjectionOperation = "open_runtime_view"
	ProjectionOperationCommit      ProjectionOperation = "commit"
	ProjectionOperationDiscard     ProjectionOperation = "discard"
	ProjectionOperationFence       ProjectionOperation = "fence"
	ProjectionOperationExpire      ProjectionOperation = "expire"
	ProjectionOperationRestore     ProjectionOperation = "restore"
	ProjectionOperationReconstruct ProjectionOperation = "reconstruct"
	ProjectionOperationRetention   ProjectionOperation = "retention"
	ProjectionOperationReclaim     ProjectionOperation = "reclaim"
	ProjectionOperationCleanup     ProjectionOperation = "cleanup"
	ProjectionOperationAudit       ProjectionOperation = "audit"
	ProjectionOperationOther       ProjectionOperation = "other"

	ProjectionResultCommitted ProjectionResult = "committed"
	ProjectionResultRejected  ProjectionResult = "rejected"
	ProjectionResultPending   ProjectionResult = "pending"

	ProjectionStateActive          ProjectionLifecycleState = "active"
	ProjectionStateMaterialized    ProjectionLifecycleState = "materialized"
	ProjectionStateOpen            ProjectionLifecycleState = "open"
	ProjectionStateCommitted       ProjectionLifecycleState = "committed"
	ProjectionStateDiscarded       ProjectionLifecycleState = "discarded"
	ProjectionStateFenced          ProjectionLifecycleState = "fenced"
	ProjectionStateExpired         ProjectionLifecycleState = "expired"
	ProjectionStateRestored        ProjectionLifecycleState = "restored"
	ProjectionStateVerified        ProjectionLifecycleState = "verified"
	ProjectionStateRetained        ProjectionLifecycleState = "retained"
	ProjectionStatePendingReclaim  ProjectionLifecycleState = "pending_reclaim"
	ProjectionStateReclaimed       ProjectionLifecycleState = "reclaimed"
	ProjectionStateCleanupOpen     ProjectionLifecycleState = "cleanup_open"
	ProjectionStateCleanupClaimed  ProjectionLifecycleState = "cleanup_claimed"
	ProjectionStateCleanupRetry    ProjectionLifecycleState = "cleanup_retry_scheduled"
	ProjectionStateCleanupBlocked  ProjectionLifecycleState = "cleanup_blocked"
	ProjectionStateCleanupResolved ProjectionLifecycleState = "cleanup_resolved"
	ProjectionStateAuditCommitted  ProjectionLifecycleState = "audit_committed"
	ProjectionStatePending         ProjectionLifecycleState = "pending"
	ProjectionStateUnknown         ProjectionLifecycleState = "unknown"

	ProjectionResourceTaskWorkspace ProjectionResourceClass = "task_workspace"
	ProjectionResourceRuntimeView   ProjectionResourceClass = "runtime_view"
	ProjectionResourceCheckpoint    ProjectionResourceClass = "checkpoint"
	ProjectionResourceCleanupDebt   ProjectionResourceClass = "cleanup_debt"
	ProjectionResourceAudit         ProjectionResourceClass = "audit"
	ProjectionResourceOther         ProjectionResourceClass = "other"

	ProjectionAdapterDeterministic ProjectionAdapterClass = "deterministic"

	MetricLifecycleTransition MetricName   = "taskworkspace_lifecycle_transition_total"
	MetricModuleC04           MetricModule = "c04_task_workspace_lifecycle"

	LogLifecycleTransition   LogEventName              = "taskworkspace.lifecycle.transition"
	TraceLifecycleProjection TraceOperationName        = "taskworkspace.lifecycle.projection"
	LogSchemaV1              LogSchemaRevision         = 1
	LogSeverityInfo          LogSeverity               = "info"
	LogSeverityWarning       LogSeverity               = "warning"
	ProjectionSourceC04      ProjectionSourcePartition = "c04_authoritative_facts"

	AuditDeliveryReclaimed           AuditDeliveryResult = "reclaimed"
	AuditDeliveryAlreadyAbsent       AuditDeliveryResult = "already_absent"
	AuditDeliveryRetainedByAuthority AuditDeliveryResult = "retained_by_authority"
	AuditDeliveryAcceptedException   AuditDeliveryResult = "accepted_exception"
	AuditDeliveryDiagnosticsAccessed AuditDeliveryResult = "diagnostics_accessed"
)

// ProjectionEnvelope is a content-free delivery derived from one retained
// authoritative fact. Projection identity never becomes a business key.
type ProjectionEnvelope struct {
	FactID         ProjectionFactID
	FactRevision   ProjectionFactRevision
	SchemaRevision ProjectionSchemaRevision
	Kind           ProjectionFactKind
	Operation      ProjectionOperation
	Result         ProjectionResult
	LifecycleState ProjectionLifecycleState
	SafeError      SafeErrorCategory
	ResourceClass  ProjectionResourceClass
	AdapterClass   ProjectionAdapterClass
	RecordedAt     Instant
}

func (e ProjectionEnvelope) canonicalDigest() Digest {
	return canonicalDigest(e)
}

type PrimaryMetricLabels struct {
	Module    MetricModule
	Operation ProjectionOperation
	Result    ProjectionResult
	FactKind  ProjectionFactKind
	SafeError SafeErrorCategory
	Resource  ProjectionResourceClass
	Adapter   ProjectionAdapterClass
	State     ProjectionLifecycleState
}

type BoundedMetric struct {
	Name   MetricName
	Labels PrimaryMetricLabels
	Value  uint64
}

type StructuredLog struct {
	Event          LogEventName
	SchemaRevision LogSchemaRevision
	Severity       LogSeverity
	Timestamp      SignalTimestamp
	Module         MetricModule
	FactKind       ProjectionFactKind
	Operation      ProjectionOperation
	Result         ProjectionResult
	SafeError      SafeErrorCategory
	Resource       ProjectionResourceClass
	State          ProjectionLifecycleState
}

type TraceObservation struct {
	Operation ProjectionOperation
	Name      TraceOperationName
	Result    ProjectionResult
	FactKind  ProjectionFactKind
	SafeError SafeErrorCategory
	Resource  ProjectionResourceClass
	State     ProjectionLifecycleState
}

type SignalTimestamp struct {
	Known bool
	Value Instant
}

type ProjectionSignalIdentity struct {
	FactID         ProjectionFactID
	FactRevision   ProjectionFactRevision
	SchemaRevision ProjectionSchemaRevision
}

// OperationalSignals deliberately has no arbitrary attribute or label map.
type OperationalSignals struct {
	Identity ProjectionSignalIdentity
	Metrics  []BoundedMetric
	Logs     []StructuredLog
	Traces   []TraceObservation
}

type TelemetryPort interface {
	Emit(context.Context, OperationalSignals) error
}

type AuditDeliveryFact struct {
	AuditFactID AuditDeliveryFactID
	Digest      Digest
	Action      CleanupAuditAction
	Result      AuditDeliveryResult
	RecordedAt  Instant
}

type AuditDeliveryPort interface {
	Deliver(context.Context, AuditDeliveryFact) error
}

type AuditDeliveryIntegrityAlert struct {
	AuditFactID AuditDeliveryFactID
	SafeError   SafeErrorCategory
}

type AuditDeliveryAlertPort interface {
	AlertAuditDeliveryIntegrity(context.Context, AuditDeliveryIntegrityAlert) error
}

type KnownQuantity struct {
	Known bool
	Value uint64
}

type SourceWatermark struct {
	Known bool
	Value uint64
}

type AuditDeliveryRebuildRequest struct{}

type AuditDeliveryBacklog struct {
	Pending         KnownQuantity
	Delivered       KnownQuantity
	Quarantined     KnownQuantity
	SourceWatermark SourceWatermark
	Evidence        []AuditDeliveryEvidence
}

type AuditDeliveryEvidence struct {
	AuditFactID       AuditDeliveryFactID
	Digest            Digest
	AttemptCount      uint64
	AttemptGeneration uint64
	FirstAttemptAt    Instant
	LastAttemptAt     Instant
	LastResult        ProjectionResult
	SafeError         SafeErrorCategory
	Quarantined       bool
}

type (
	DiagnosticSubject        string
	DiagnosticReason         string
	DiagnosticsSourceState   string
	DiagnosticLifecycleState string
	DiagnosticNextAction     string
	DiagnosticEvidenceKind   string
)

const (
	DiagnosticSubjectCleanupDebt DiagnosticSubject = "cleanup_debt"

	DiagnosticReasonCleanupReconciliation DiagnosticReason = "cleanup_reconciliation"

	DiagnosticsSourceCurrent     DiagnosticsSourceState = "current"
	DiagnosticsSourceStale       DiagnosticsSourceState = "stale"
	DiagnosticsSourceUnavailable DiagnosticsSourceState = "unavailable"

	DiagnosticLifecycleUnknown        DiagnosticLifecycleState = "unknown"
	DiagnosticLifecycleOpen           DiagnosticLifecycleState = "open"
	DiagnosticLifecycleClaimed        DiagnosticLifecycleState = "claimed"
	DiagnosticLifecycleRetryScheduled DiagnosticLifecycleState = "retry_scheduled"
	DiagnosticLifecycleBlocked        DiagnosticLifecycleState = "blocked"
	DiagnosticLifecycleResolved       DiagnosticLifecycleState = "resolved"

	DiagnosticNextRetryCleanup     DiagnosticNextAction = "retry_cleanup"
	DiagnosticNextWaitForAuthority DiagnosticNextAction = "wait_for_authority"
	DiagnosticNextNone             DiagnosticNextAction = "none"
	DiagnosticNextRefreshSource    DiagnosticNextAction = "refresh_source"

	DiagnosticEvidenceEligibility DiagnosticEvidenceKind = "eligibility"
	DiagnosticEvidenceFailure     DiagnosticEvidenceKind = "safe_failure"
	DiagnosticEvidenceResolution  DiagnosticEvidenceKind = "resolution"
	DiagnosticEvidenceAudit       DiagnosticEvidenceKind = "mandatory_audit"
)

type DiagnosticsSourceStatus struct {
	State     DiagnosticsSourceState
	Watermark SourceWatermark
}

type AdministratorDiagnosticIntent struct {
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	DebtID                 CleanupDebtID
	Subject                DiagnosticSubject
	Reason                 DiagnosticReason
	AdministratorAuthority PlatformAdministratorAuthority
	Operation              Operation
}

type DiagnosticsPort interface {
	Authorize(context.Context, AdministratorDiagnosticIntent) error
	SourceStatus(context.Context) DiagnosticsSourceStatus
}

type QueryAdministratorDiagnosticsRequest struct {
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	DebtID                 CleanupDebtID
	Subject                DiagnosticSubject
	Reason                 DiagnosticReason
	AdministratorAuthority PlatformAdministratorAuthority
	Operation              Operation
}

func (r QueryAdministratorDiagnosticsRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                   string
		PolicyDomainID         PolicyDomainID
		TaskID                 TaskID
		DebtID                 CleanupDebtID
		Subject                DiagnosticSubject
		Reason                 DiagnosticReason
		AdministratorAuthority PlatformAdministratorAuthority
		OperationID            OperationID
	}{
		Kind: "query_administrator_diagnostics", PolicyDomainID: r.PolicyDomainID,
		TaskID: r.TaskID, DebtID: r.DebtID, Subject: r.Subject, Reason: r.Reason,
		AdministratorAuthority: r.AdministratorAuthority, OperationID: r.Operation.ID,
	})
}

type DiagnosticDuration struct {
	Known bool
	Value Duration
}

type DiagnosticRelationship struct {
	RevisionID  RevisionID
	OperationID OperationID
	Fence       Fence
}

type DiagnosticEvidenceReference struct {
	Kind     DiagnosticEvidenceKind
	Identity Digest
}

type AdministratorDiagnostics struct {
	LifecycleState     DiagnosticLifecycleState
	Relationship       DiagnosticRelationship
	CleanupOwner       CleanupOwner
	RetryAge           DiagnosticDuration
	EstimatedBytes     CleanupQuantity
	EstimatedInodes    CleanupQuantity
	SafeError          SafeErrorCategory
	Blockers           []CleanupBlocker
	NextAction         DiagnosticNextAction
	EvidenceReferences []DiagnosticEvidenceReference
	SourceState        DiagnosticsSourceState
	SourceWatermark    SourceWatermark
}

type diagnosticOperationRecord struct {
	requestDigest          Digest
	administratorAuthority PlatformAdministratorAuthority
	result                 AdministratorDiagnostics
}

type auditDeliveryRecord struct {
	fact              AuditDeliveryFact
	delivered         bool
	delivering        bool
	quarantined       bool
	attempts          uint64
	attemptGeneration uint64
	firstAttemptAt    Instant
	lastAttemptAt     Instant
	lastResult        ProjectionResult
	safeError         SafeErrorCategory
}

type LifecycleProjectionPort interface {
	Project(context.Context, ProjectionEnvelope) error
	Rebuild(context.Context, ProjectionSchemaRevision, []ProjectionEnvelope) error
}

type ProjectionRebuildRequest struct {
	SchemaRevision ProjectionSchemaRevision
}

type ProjectionRebuildResult struct {
	Projected       KnownQuantity
	SourceWatermark SourceWatermark
}

type ProjectionCursor struct {
	SourcePartition ProjectionSourcePartition
	SchemaRevision  ProjectionSchemaRevision
	SourceWatermark SourceWatermark
	FirstFactID     ProjectionFactID
	LastFactID      ProjectionFactID
	DuplicateCount  uint64
	RetryPending    KnownQuantity
	SafeError       SafeErrorCategory
}

type deterministicProjectionState struct {
	revision          ProjectionFactRevision
	digest            Digest
	delivered         bool
	delivering        bool
	attemptGeneration uint64
}

// DeterministicProjection provides vendor-neutral idempotency before signals
// cross into a metrics, log, trace, or collector adapter.
type DeterministicProjection struct {
	mu        sync.Mutex
	telemetry TelemetryPort
	states    map[projectionIdentity]deterministicProjectionState
	cursors   map[ProjectionSchemaRevision]ProjectionCursor
}

type projectionIdentity struct {
	factID ProjectionFactID
	schema ProjectionSchemaRevision
}

func NewDeterministicProjection(telemetry TelemetryPort) *DeterministicProjection {
	return &DeterministicProjection{
		telemetry: telemetry,
		states:    make(map[projectionIdentity]deterministicProjectionState),
		cursors:   make(map[ProjectionSchemaRevision]ProjectionCursor),
	}
}

func (p *DeterministicProjection) Project(ctx context.Context, envelope ProjectionEnvelope) error {
	return p.project(ctx, envelope, false)
}

func (p *DeterministicProjection) project(
	ctx context.Context,
	envelope ProjectionEnvelope,
	forceRebuild bool,
) error {
	if p == nil || p.telemetry == nil || !validProjectionSchema(envelope.SchemaRevision) ||
		!validProjectionEnvelope(envelope) {
		return &Error{Code: ErrorInvalidIntent}
	}
	p.mu.Lock()
	if p.states == nil {
		p.states = make(map[projectionIdentity]deterministicProjectionState)
	}
	if p.cursors == nil {
		p.cursors = make(map[ProjectionSchemaRevision]ProjectionCursor)
	}
	identity := projectionIdentity{factID: envelope.FactID, schema: envelope.SchemaRevision}
	digest := envelope.canonicalDigest()
	p.touchProjectionCursorLocked(envelope)
	state, exists := p.states[identity]
	if exists && envelope.FactRevision < state.revision {
		p.recordProjectionDuplicateLocked(envelope.SchemaRevision)
		if !forceRebuild {
			p.updateProjectionWatermarkLocked(envelope.SchemaRevision)
		}
		p.mu.Unlock()
		return nil
	}
	if exists && envelope.FactRevision == state.revision {
		if state.digest != digest {
			if !forceRebuild {
				p.updateProjectionWatermarkLocked(envelope.SchemaRevision)
			}
			cursor := p.cursors[envelope.SchemaRevision]
			cursor.SafeError = SafeErrorIdempotencyConflict
			cursor.RetryPending = p.projectionRetryPendingLocked(envelope.SchemaRevision)
			p.cursors[envelope.SchemaRevision] = cursor
			p.mu.Unlock()
			return &Error{Code: ErrorIntegrityConflict}
		}
		if !forceRebuild && (state.delivered || state.delivering) {
			p.recordProjectionDuplicateLocked(envelope.SchemaRevision)
			p.updateProjectionWatermarkLocked(envelope.SchemaRevision)
			p.mu.Unlock()
			return nil
		}
		if forceRebuild {
			p.recordProjectionDuplicateLocked(envelope.SchemaRevision)
		}
	}
	state.attemptGeneration++
	state = deterministicProjectionState{
		revision: envelope.FactRevision, digest: digest, delivering: true,
		attemptGeneration: state.attemptGeneration,
	}
	p.states[identity] = state
	if !forceRebuild {
		p.updateProjectionWatermarkLocked(envelope.SchemaRevision)
	}
	attemptGeneration := state.attemptGeneration
	p.mu.Unlock()

	emitErr := callProjectionAdapter(func() error {
		return p.telemetry.Emit(ctx, operationalSignals(envelope))
	})
	p.mu.Lock()
	current, currentExists := p.states[identity]
	if currentExists && current.revision == envelope.FactRevision && current.digest == digest &&
		current.attemptGeneration == attemptGeneration {
		current.delivering = false
		current.delivered = emitErr == nil
		p.states[identity] = current
	}
	cursor := p.cursors[envelope.SchemaRevision]
	if emitErr != nil {
		cursor.SafeError = SafeErrorReconciliationRequired
	}
	cursor.RetryPending = p.projectionRetryPendingLocked(envelope.SchemaRevision)
	if cursor.RetryPending.Value == 0 {
		cursor.SafeError = ""
	}
	p.cursors[envelope.SchemaRevision] = cursor
	p.mu.Unlock()
	if emitErr != nil {
		return &Error{Code: ErrorReconciliationRequired}
	}
	return nil
}

func (p *DeterministicProjection) Rebuild(
	ctx context.Context,
	schema ProjectionSchemaRevision,
	facts []ProjectionEnvelope,
) error {
	if p == nil || p.telemetry == nil || !validProjectionSchema(schema) {
		return &Error{Code: ErrorInvalidIntent}
	}
	for _, fact := range facts {
		if fact.SchemaRevision != schema || !validProjectionEnvelope(fact) {
			return &Error{Code: ErrorInvalidIntent}
		}
	}
	ordered := append([]ProjectionEnvelope(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].FactID == ordered[j].FactID {
			return ordered[i].FactRevision < ordered[j].FactRevision
		}
		return ordered[i].FactID < ordered[j].FactID
	})
	p.mu.Lock()
	if p.cursors == nil {
		p.cursors = make(map[ProjectionSchemaRevision]ProjectionCursor)
	}
	cursor := p.cursors[schema]
	cursor.SourcePartition = ProjectionSourceC04
	cursor.SchemaRevision = schema
	cursor.SourceWatermark = SourceWatermark{
		Known: true, Value: authoritativeProjectionWatermark(ordered),
	}
	cursor.FirstFactID = ""
	cursor.LastFactID = ""
	p.cursors[schema] = cursor
	p.mu.Unlock()
	for _, fact := range ordered {
		if err := p.project(ctx, fact, true); err != nil {
			return err
		}
	}
	return nil
}

func (p *DeterministicProjection) Cursor(schema ProjectionSchemaRevision) (ProjectionCursor, error) {
	if p == nil || !validProjectionSchema(schema) {
		return ProjectionCursor{}, &Error{Code: ErrorInvalidIntent}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cursor := p.cursors[schema]
	if cursor.SchemaRevision == 0 {
		return ProjectionCursor{}, &Error{Code: ErrorReconciliationRequired}
	}
	cursor.RetryPending = p.projectionRetryPendingLocked(schema)
	return cursor, nil
}

func (p *DeterministicProjection) touchProjectionCursorLocked(envelope ProjectionEnvelope) {
	cursor := p.cursors[envelope.SchemaRevision]
	cursor.SourcePartition = ProjectionSourceC04
	cursor.SchemaRevision = envelope.SchemaRevision
	if cursor.FirstFactID == "" {
		cursor.FirstFactID = envelope.FactID
	}
	cursor.LastFactID = envelope.FactID
	p.cursors[envelope.SchemaRevision] = cursor
}

func (p *DeterministicProjection) updateProjectionWatermarkLocked(schema ProjectionSchemaRevision) {
	var watermark uint64
	for identity, state := range p.states {
		if identity.schema != schema {
			continue
		}
		if ^uint64(0)-watermark < uint64(state.revision) {
			watermark = ^uint64(0)
			break
		}
		watermark += uint64(state.revision)
	}
	cursor := p.cursors[schema]
	cursor.SourceWatermark = SourceWatermark{Known: true, Value: watermark}
	p.cursors[schema] = cursor
}

func (p *DeterministicProjection) recordProjectionDuplicateLocked(schema ProjectionSchemaRevision) {
	cursor := p.cursors[schema]
	cursor.SourcePartition = ProjectionSourceC04
	cursor.SchemaRevision = schema
	cursor.DuplicateCount++
	p.cursors[schema] = cursor
}

func (p *DeterministicProjection) projectionRetryPendingLocked(
	schema ProjectionSchemaRevision,
) KnownQuantity {
	var pending uint64
	for identity, state := range p.states {
		if identity.schema == schema && !state.delivered {
			pending++
		}
	}
	return KnownQuantity{Known: true, Value: pending}
}

func validProjectionSchema(schema ProjectionSchemaRevision) bool {
	return schema == ProjectionSchemaV1 || schema == ProjectionSchemaV2
}

func validProjectionEnvelope(envelope ProjectionEnvelope) bool {
	return envelope.FactID != "" && envelope.FactRevision != 0 && envelope.SchemaRevision != 0 &&
		validProjectionFactKind(envelope.Kind) && validProjectionOperation(envelope.Operation) &&
		validProjectionResult(envelope.Result) && validProjectionLifecycleState(envelope.LifecycleState) &&
		validProjectionSafeError(envelope.SafeError) && validProjectionResource(envelope.ResourceClass) &&
		envelope.AdapterClass == ProjectionAdapterDeterministic
}

func operationalSignals(envelope ProjectionEnvelope) OperationalSignals {
	labels := PrimaryMetricLabels{
		Module: MetricModuleC04, FactKind: envelope.Kind, Operation: envelope.Operation,
		Result: envelope.Result, SafeError: envelope.SafeError, Resource: envelope.ResourceClass,
		Adapter: envelope.AdapterClass, State: envelope.LifecycleState,
	}
	return OperationalSignals{
		Identity: ProjectionSignalIdentity{
			FactID: envelope.FactID, FactRevision: envelope.FactRevision,
			SchemaRevision: envelope.SchemaRevision,
		},
		Metrics: []BoundedMetric{{Name: MetricLifecycleTransition, Labels: labels, Value: 1}},
		Logs: []StructuredLog{{
			Event: LogLifecycleTransition, SchemaRevision: LogSchemaV1,
			Severity:  projectionLogSeverity(envelope),
			Timestamp: SignalTimestamp{Known: envelope.RecordedAt != 0, Value: envelope.RecordedAt},
			Module:    MetricModuleC04, FactKind: envelope.Kind,
			Operation: envelope.Operation, Result: envelope.Result, SafeError: envelope.SafeError,
			Resource: envelope.ResourceClass, State: envelope.LifecycleState,
		}},
		Traces: []TraceObservation{{
			Operation: envelope.Operation, Name: TraceLifecycleProjection, Result: envelope.Result,
			FactKind: envelope.Kind, SafeError: envelope.SafeError,
			Resource: envelope.ResourceClass, State: envelope.LifecycleState,
		}},
	}
}

func projectionLogSeverity(envelope ProjectionEnvelope) LogSeverity {
	if envelope.Result == ProjectionResultRejected || envelope.SafeError != "" {
		return LogSeverityWarning
	}
	return LogSeverityInfo
}

func validProjectionFactKind(kind ProjectionFactKind) bool {
	switch kind {
	case ProjectionFactLifecycle, ProjectionFactIntegrity, ProjectionFactOperation,
		ProjectionFactRetention, ProjectionFactCleanupDebt, ProjectionFactMandatoryAudit:
		return true
	default:
		return false
	}
}

func validProjectionOperation(operation ProjectionOperation) bool {
	switch operation {
	case ProjectionOperationConfirm, ProjectionOperationMaterialize, ProjectionOperationOpenView,
		ProjectionOperationCommit, ProjectionOperationDiscard, ProjectionOperationFence,
		ProjectionOperationExpire, ProjectionOperationRestore, ProjectionOperationReconstruct,
		ProjectionOperationRetention, ProjectionOperationReclaim, ProjectionOperationCleanup,
		ProjectionOperationAudit, ProjectionOperationOther:
		return true
	default:
		return false
	}
}

func validProjectionResult(result ProjectionResult) bool {
	return result == ProjectionResultCommitted || result == ProjectionResultRejected ||
		result == ProjectionResultPending
}

func validProjectionLifecycleState(state ProjectionLifecycleState) bool {
	switch state {
	case ProjectionStateActive, ProjectionStateMaterialized, ProjectionStateOpen,
		ProjectionStateCommitted, ProjectionStateDiscarded, ProjectionStateFenced,
		ProjectionStateExpired, ProjectionStateRestored, ProjectionStateVerified,
		ProjectionStateRetained, ProjectionStatePendingReclaim, ProjectionStateReclaimed,
		ProjectionStateCleanupOpen, ProjectionStateCleanupClaimed, ProjectionStateCleanupRetry,
		ProjectionStateCleanupBlocked, ProjectionStateCleanupResolved,
		ProjectionStateAuditCommitted, ProjectionStatePending, ProjectionStateUnknown:
		return true
	default:
		return false
	}
}

func validProjectionSafeError(category SafeErrorCategory) bool {
	switch category {
	case "", SafeErrorAuthorizationDenied, SafeErrorInvalidIntent, SafeErrorIdempotencyConflict,
		SafeErrorStaleRevisionGenerationFence, SafeErrorTerminalConflict,
		SafeErrorIntegrityUnavailableContent, SafeErrorDurabilityUnverified,
		SafeErrorResourceExhausted, SafeErrorRetryableUnavailable,
		SafeErrorReconciliationRequired, SafeErrorCleanupDebt:
		return true
	default:
		return false
	}
}

func validProjectionResource(resource ProjectionResourceClass) bool {
	switch resource {
	case ProjectionResourceTaskWorkspace, ProjectionResourceRuntimeView,
		ProjectionResourceCheckpoint, ProjectionResourceCleanupDebt,
		ProjectionResourceAudit, ProjectionResourceOther:
		return true
	default:
		return false
	}
}

type observedLifecycle struct {
	*inMemory
	projection LifecycleProjectionPort
	schema     ProjectionSchemaRevision
}

func (l *observedLifecycle) ConfirmTaskWorkspace(
	ctx context.Context,
	request ConfirmTaskWorkspaceRequest,
) (ConfirmTaskWorkspaceResult, error) {
	result, err := l.inMemory.ConfirmTaskWorkspace(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectWorkspace(ctx, request.TaskID)
	}
	return result, err
}

func (l *observedLifecycle) Materialize(
	ctx context.Context,
	request MaterializeRequest,
) (MaterializeResult, error) {
	result, err := l.inMemory.Materialize(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) OpenRuntimeView(
	ctx context.Context,
	request OpenRuntimeViewRequest,
) (OpenRuntimeViewResult, error) {
	result, err := l.inMemory.OpenRuntimeView(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) CommitRuntimeView(
	ctx context.Context,
	request CommitRuntimeViewRequest,
) (CommitRuntimeViewResult, error) {
	result, err := l.inMemory.CommitRuntimeView(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectWorkspace(ctx, request.TaskID)
		l.projectCheckpoint(ctx, result.TaskWorkspaceID, result.CheckpointID)
	}
	return result, err
}

func (l *observedLifecycle) DiscardRuntimeView(
	ctx context.Context,
	request DiscardRuntimeViewRequest,
) (DiscardRuntimeViewResult, error) {
	result, err := l.inMemory.DiscardRuntimeView(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) FenceRuntimeView(
	ctx context.Context,
	request FenceRuntimeViewRequest,
) (FenceRuntimeViewResult, error) {
	result, err := l.inMemory.FenceRuntimeView(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) ExpireMaterialization(
	ctx context.Context,
	request ExpireMaterializationRequest,
) (ExpireMaterializationResult, error) {
	result, err := l.inMemory.ExpireMaterialization(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) ExpireRuntimeView(
	ctx context.Context,
	request ExpireRuntimeViewRequest,
) (ExpireRuntimeViewResult, error) {
	result, err := l.inMemory.ExpireRuntimeView(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) RestoreTaskWorkspace(
	ctx context.Context,
	request RestoreTaskWorkspaceRequest,
) (RestoreTaskWorkspaceResult, error) {
	result, err := l.inMemory.RestoreTaskWorkspace(ctx, request)
	l.projectOperation(ctx, operationScope{
		request.Intent.PolicyDomainID, request.Intent.TaskID, request.Operation.ID,
	})
	if err == nil {
		l.projectWorkspace(ctx, request.Intent.TaskID)
	}
	return result, err
}

func (l *observedLifecycle) ReconstructTaskWorkspace(
	ctx context.Context,
	request ReconstructTaskWorkspaceRequest,
) (ReconstructTaskWorkspaceResult, error) {
	result, err := l.inMemory.ReconstructTaskWorkspace(ctx, request)
	l.projectOperation(ctx, operationScope{
		request.Intent.PolicyDomainID, request.Intent.TaskID, request.Operation.ID,
	})
	if err == nil {
		l.projectWorkspace(ctx, request.Intent.TaskID)
	}
	return result, err
}

func (l *observedLifecycle) AttachCheckpointRetention(
	ctx context.Context,
	request AttachCheckpointRetentionRequest,
) (CheckpointRetention, error) {
	result, err := l.inMemory.AttachCheckpointRetention(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectCheckpoint(ctx, request.TaskWorkspaceID, request.CheckpointID)
	}
	return result, err
}

func (l *observedLifecycle) ReleaseCheckpointRetention(
	ctx context.Context,
	request ReleaseCheckpointRetentionRequest,
) (CheckpointRetention, error) {
	result, err := l.inMemory.ReleaseCheckpointRetention(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectCheckpoint(ctx, request.TaskWorkspaceID, request.CheckpointID)
	}
	return result, err
}

func (l *observedLifecycle) ReclaimCheckpoint(
	ctx context.Context,
	request ReclaimCheckpointRequest,
) (CheckpointReclamation, error) {
	result, err := l.inMemory.ReclaimCheckpoint(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectCheckpoint(ctx, request.TaskWorkspaceID, request.CheckpointID)
	}
	return result, err
}

func (l *observedLifecycle) ObserveCheckpointInventory(
	ctx context.Context,
	request ObserveCheckpointInventoryRequest,
) (CheckpointInventoryObservation, error) {
	result, err := l.inMemory.ObserveCheckpointInventory(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) ReconcileOperation(
	ctx context.Context,
	request ReconcileOperationRequest,
) (OperationInspection, error) {
	result, err := l.inMemory.ReconcileOperation(ctx, request)
	l.projectOperation(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.OperationID})
	return result, err
}

func (l *observedLifecycle) QueryAdministratorDiagnostics(
	ctx context.Context,
	request QueryAdministratorDiagnosticsRequest,
) (AdministratorDiagnostics, error) {
	result, err := l.inMemory.QueryAdministratorDiagnostics(ctx, request)
	if err == nil {
		l.projectMandatoryAuditForOperation(
			ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID},
		)
	}
	return result, err
}

func (l *observedLifecycle) projectOperation(ctx context.Context, scope operationScope) {
	if l.projection == nil || scope.policyDomainID == "" || scope.taskID == "" || scope.operationID == "" {
		return
	}
	l.mu.Lock()
	record, exists := l.operations[scope]
	if !exists {
		l.mu.Unlock()
		return
	}
	envelope, valid := projectionOperationEnvelope(scope, record, l.schema)
	l.mu.Unlock()
	if valid {
		l.projectEnvelope(ctx, envelope)
	}
}

func (l *observedLifecycle) projectWorkspace(ctx context.Context, taskID TaskID) {
	if l.projection == nil {
		return
	}
	l.mu.Lock()
	workspace, exists := l.workspaces[taskID]
	if !exists {
		l.mu.Unlock()
		return
	}
	envelope := workspaceProjectionEnvelope(taskID, workspace, l.schema)
	l.mu.Unlock()
	l.projectEnvelope(ctx, envelope)
}

func (l *observedLifecycle) projectCheckpoint(
	ctx context.Context,
	taskWorkspaceID TaskWorkspaceID,
	checkpointID CheckpointID,
) {
	if l.projection == nil {
		return
	}
	l.mu.Lock()
	checkpoint, exists := l.checkpoints[checkpointID]
	if !exists || checkpoint.taskWorkspaceID != taskWorkspaceID {
		l.mu.Unlock()
		return
	}
	integrity, retention := checkpointProjectionEnvelopes(
		taskWorkspaceID, checkpointID, checkpoint, l.schema,
	)
	l.mu.Unlock()
	l.projectEnvelope(ctx, integrity)
	l.projectEnvelope(ctx, retention)
}

func (l *observedLifecycle) projectEnvelope(ctx context.Context, envelope ProjectionEnvelope) {
	if l.projection != nil {
		_ = callProjectionAdapter(func() error {
			return l.projection.Project(ctx, envelope)
		})
	}
}

func callProjectionAdapter(invoke func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = &Error{Code: ErrorRetryableUnavailable}
		}
	}()
	if err = invoke(); err != nil {
		return normalizeProjectionAdapterError(err)
	}
	return nil
}

func (l *observedLifecycle) CreateCleanupObligation(
	ctx context.Context,
	request CreateCleanupObligationRequest,
) (CleanupDebt, error) {
	result, err := l.inMemory.CreateCleanupObligation(ctx, request)
	l.projectCleanupDebt(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) ClaimCleanupDebt(
	ctx context.Context,
	request ClaimCleanupDebtRequest,
) (CleanupDebt, error) {
	result, err := l.inMemory.ClaimCleanupDebt(ctx, request)
	l.projectCleanupDebt(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) ReconcileCleanupDebt(
	ctx context.Context,
	request ReconcileCleanupDebtRequest,
) (CleanupDebt, error) {
	result, err := l.inMemory.ReconcileCleanupDebt(ctx, request)
	l.projectCleanupDebt(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) ResolveCleanupDebt(
	ctx context.Context,
	request ResolveCleanupDebtRequest,
) (CleanupDebt, error) {
	result, err := l.inMemory.ResolveCleanupDebt(ctx, request)
	l.projectCleanupDebt(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectMandatoryAuditForOperation(
			ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID},
		)
	}
	return result, err
}

func (l *observedLifecycle) ReopenCleanupDebt(
	ctx context.Context,
	request ReopenCleanupDebtRequest,
) (CleanupDebt, error) {
	result, err := l.inMemory.ReopenCleanupDebt(ctx, request)
	l.projectCleanupDebt(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	if err == nil {
		l.projectMandatoryAuditForOperation(
			ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID},
		)
	}
	return result, err
}

func (l *observedLifecycle) AcceptLegacyCleanupObligation(
	ctx context.Context,
	request AcceptLegacyCleanupObligationRequest,
) (CleanupDebt, error) {
	result, err := l.inMemory.AcceptLegacyCleanupObligation(ctx, request)
	l.projectCleanupDebt(ctx, operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID})
	return result, err
}

func (l *observedLifecycle) projectCleanupDebt(
	ctx context.Context,
	scope operationScope,
) {
	if l.projection == nil || scope.policyDomainID == "" || scope.taskID == "" || scope.operationID == "" {
		return
	}
	l.mu.Lock()
	record, exists := l.cleanupOperations[scope]
	if !exists {
		l.mu.Unlock()
		return
	}
	operation := cleanupOperationProjectionEnvelope(scope, record, l.schema)
	var debt ProjectionEnvelope
	hasDebt := record.err == nil && record.result.DebtID != ""
	if hasDebt {
		debt = cleanupDebtProjectionEnvelope(record.result, l.schema)
	}
	l.mu.Unlock()
	l.projectEnvelope(ctx, operation)
	if hasDebt {
		l.projectEnvelope(ctx, debt)
	}
}

func (l *observedLifecycle) projectMandatoryAuditForOperation(
	ctx context.Context,
	scope operationScope,
) {
	if l.projection == nil {
		return
	}
	l.mu.Lock()
	facts := make([]CleanupAuditEvidence, 0, 1)
	auditOperationID := contentFreeAuditOperation(scope, Operation{ID: scope.operationID}).ID
	for _, evidence := range l.cleanupAuditFacts {
		debt, debtExists := l.cleanupDebts[evidence.DebtID]
		if evidence.OperationID == auditOperationID && debtExists &&
			debt.debt.PolicyDomainID == scope.policyDomainID && debt.debt.TaskID == scope.taskID {
			facts = append(facts, evidence)
		}
	}
	l.mu.Unlock()
	for _, evidence := range facts {
		l.projectEnvelope(ctx, ProjectionEnvelope{
			FactID:       projectionMandatoryAuditFactID(evidence.ID),
			FactRevision: 1, SchemaRevision: l.schema,
			Kind: ProjectionFactMandatoryAudit, Operation: ProjectionOperationAudit,
			Result: ProjectionResultCommitted, LifecycleState: ProjectionStateAuditCommitted,
			ResourceClass: ProjectionResourceAudit, AdapterClass: ProjectionAdapterDeterministic,
			RecordedAt: evidence.RecordedAt,
		})
	}
}

func safeErrorFrom(err error) SafeErrorCategory {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.SafeCategory()
	}
	if err != nil {
		return SafeErrorRetryableUnavailable
	}
	return ""
}

func projectionOperationEnvelope(
	scope operationScope,
	record operationRecord,
	schema ProjectionSchemaRevision,
) (ProjectionEnvelope, bool) {
	inspection := OperationInspection{Error: cloneLifecycleError(record.err)}
	if record.state == operationJournalTerminal {
		if record.payload == nil || record.payload.projectResult(&inspection) != nil {
			return ProjectionEnvelope{}, false
		}
	}
	operation, state, resource := projectionOperationState(inspection)
	result := ProjectionResultPending
	if record.err != nil {
		result = ProjectionResultRejected
		state = ProjectionStateUnknown
	} else if record.state == operationJournalTerminal {
		result = ProjectionResultCommitted
	}
	return ProjectionEnvelope{
		FactID:         projectionOperationFactID(scope),
		FactRevision:   projectionOperationRevision(record.state),
		SchemaRevision: schema,
		Kind:           ProjectionFactOperation,
		Operation:      operation,
		Result:         result,
		LifecycleState: state,
		SafeError:      safeErrorFrom(record.err),
		ResourceClass:  resource,
		AdapterClass:   ProjectionAdapterDeterministic,
		RecordedAt:     record.recordedAt,
	}, true
}

func cleanupOperationProjectionEnvelope(
	scope operationScope,
	record cleanupOperationRecord,
	schema ProjectionSchemaRevision,
) ProjectionEnvelope {
	result := ProjectionResultCommitted
	state := projectionCleanupDebtState(record.result.State)
	if record.err != nil {
		result = ProjectionResultRejected
		state = ProjectionStateUnknown
	}
	return ProjectionEnvelope{
		FactID:         projectionOperationFactID(scope),
		FactRevision:   projectionOperationRevision(operationJournalTerminal),
		SchemaRevision: schema,
		Kind:           ProjectionFactOperation,
		Operation:      ProjectionOperationCleanup,
		Result:         result,
		LifecycleState: state,
		SafeError:      safeErrorFrom(record.err),
		ResourceClass:  ProjectionResourceCleanupDebt,
		AdapterClass:   ProjectionAdapterDeterministic,
		RecordedAt:     record.recordedAt,
	}
}

func cleanupDebtProjectionEnvelope(
	debt CleanupDebt,
	schema ProjectionSchemaRevision,
) ProjectionEnvelope {
	return ProjectionEnvelope{
		FactID:         projectionCleanupDebtFactID(debt),
		FactRevision:   projectionCleanupDebtRevision(debt),
		SchemaRevision: schema,
		Kind:           ProjectionFactCleanupDebt,
		Operation:      ProjectionOperationCleanup,
		Result:         ProjectionResultCommitted,
		LifecycleState: projectionCleanupDebtState(debt.State),
		SafeError:      cleanupFailureSafeCategory(debt.LastFailureCategory),
		ResourceClass:  ProjectionResourceCleanupDebt,
		AdapterClass:   ProjectionAdapterDeterministic,
		RecordedAt:     cleanupDebtRecordedAt(debt),
	}
}

func cleanupDebtRecordedAt(debt CleanupDebt) Instant {
	if debt.ResolvedAt != 0 {
		return debt.ResolvedAt
	}
	if debt.LastAttemptAt != 0 {
		return debt.LastAttemptAt
	}
	return debt.CreatedAt
}

func workspaceProjectionEnvelope(
	taskID TaskID,
	workspace workspaceBinding,
	schema ProjectionSchemaRevision,
) ProjectionEnvelope {
	revision := ProjectionFactRevision(workspace.fence)
	if revision == 0 {
		revision = 1
	}
	return ProjectionEnvelope{
		FactID: projectionFactID(ProjectionFactLifecycle, struct {
			PolicyDomainID  PolicyDomainID
			TaskID          TaskID
			TaskWorkspaceID TaskWorkspaceID
		}{workspace.policyDomainID, taskID, workspace.taskWorkspaceID}),
		FactRevision:   revision,
		SchemaRevision: schema,
		Kind:           ProjectionFactLifecycle,
		Operation:      ProjectionOperationOther,
		Result:         ProjectionResultCommitted,
		LifecycleState: ProjectionStateActive,
		ResourceClass:  ProjectionResourceTaskWorkspace,
		AdapterClass:   ProjectionAdapterDeterministic,
		RecordedAt:     workspace.recordedAt,
	}
}

func checkpointProjectionEnvelopes(
	taskWorkspaceID TaskWorkspaceID,
	checkpointID CheckpointID,
	checkpoint checkpointRecord,
	schema ProjectionSchemaRevision,
) (ProjectionEnvelope, ProjectionEnvelope) {
	subject := struct {
		TaskWorkspaceID TaskWorkspaceID
		CheckpointID    CheckpointID
	}{taskWorkspaceID, checkpointID}
	retentionRecordedAt := checkpoint.retention.recordedAt
	if retentionRecordedAt == 0 {
		retentionRecordedAt = checkpoint.recordedAt
	}
	return ProjectionEnvelope{
			FactID:         projectionFactID(ProjectionFactIntegrity, subject),
			FactRevision:   1,
			SchemaRevision: schema,
			Kind:           ProjectionFactIntegrity,
			Operation:      ProjectionOperationCommit,
			Result:         ProjectionResultCommitted,
			LifecycleState: ProjectionStateVerified,
			ResourceClass:  ProjectionResourceCheckpoint,
			AdapterClass:   ProjectionAdapterDeterministic,
			RecordedAt:     checkpoint.recordedAt,
		}, ProjectionEnvelope{
			FactID:         projectionFactID(ProjectionFactRetention, subject),
			FactRevision:   checkpointRetentionProjectionRevision(checkpoint.retention),
			SchemaRevision: schema,
			Kind:           ProjectionFactRetention,
			Operation:      ProjectionOperationRetention,
			Result:         ProjectionResultCommitted,
			LifecycleState: projectionRetentionState(checkpoint.retention),
			ResourceClass:  ProjectionResourceCheckpoint,
			AdapterClass:   ProjectionAdapterDeterministic,
			RecordedAt:     retentionRecordedAt,
		}
}

func checkpointRetentionProjectionRevision(retention checkpointRetentionRecord) ProjectionFactRevision {
	revision := ProjectionFactRevision(uint64(retention.generation) * 2)
	if retention.reclaimed {
		revision++
	}
	if revision == 0 {
		return 1
	}
	return revision
}

func projectionOperationFactID(scope operationScope) ProjectionFactID {
	return projectionFactID(ProjectionFactOperation, struct {
		PolicyDomainID PolicyDomainID
		TaskID         TaskID
		OperationID    OperationID
	}{scope.policyDomainID, scope.taskID, scope.operationID})
}

func projectionCleanupDebtFactID(debt CleanupDebt) ProjectionFactID {
	return projectionFactID(ProjectionFactCleanupDebt, struct {
		PolicyDomainID  PolicyDomainID
		TaskID          TaskID
		TaskWorkspaceID TaskWorkspaceID
		DebtID          CleanupDebtID
	}{debt.PolicyDomainID, debt.TaskID, debt.TaskWorkspaceID, debt.DebtID})
}

func (m *inMemory) RebuildProjections(
	ctx context.Context,
	request ProjectionRebuildRequest,
) (ProjectionRebuildResult, error) {
	if !validProjectionSchema(request.SchemaRevision) || m == nil {
		return ProjectionRebuildResult{}, &Error{Code: ErrorInvalidIntent}
	}
	observed := m.projection
	if observed == nil {
		return ProjectionRebuildResult{}, &Error{Code: ErrorReconciliationRequired}
	}
	m.mu.Lock()
	facts := authoritativeProjectionFactsLocked(m, request.SchemaRevision)
	watermark := authoritativeProjectionWatermark(facts)
	m.mu.Unlock()
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].FactID == facts[j].FactID {
			return facts[i].FactRevision < facts[j].FactRevision
		}
		return facts[i].FactID < facts[j].FactID
	})
	if err := callProjectionAdapter(func() error {
		return observed.Rebuild(ctx, request.SchemaRevision, facts)
	}); err != nil {
		return ProjectionRebuildResult{}, err
	}
	return ProjectionRebuildResult{
		Projected:       KnownQuantity{Known: true, Value: uint64(len(facts))},
		SourceWatermark: SourceWatermark{Known: true, Value: watermark},
	}, nil
}

func authoritativeProjectionFactsLocked(
	m *inMemory,
	schema ProjectionSchemaRevision,
) []ProjectionEnvelope {
	facts := make([]ProjectionEnvelope, 0,
		len(m.operations)+len(m.cleanupOperations)+len(m.workspaces)+
			(2*len(m.checkpoints))+len(m.cleanupDebts)+len(m.cleanupAuditFacts),
	)
	for scope, record := range m.operations {
		if _, cleanupOperationExists := m.cleanupOperations[scope]; cleanupOperationExists {
			continue
		}
		envelope, valid := projectionOperationEnvelope(scope, record, schema)
		if valid {
			facts = append(facts, envelope)
		}
	}
	for scope, record := range m.cleanupOperations {
		facts = append(facts, cleanupOperationProjectionEnvelope(scope, record, schema))
	}
	for taskID, workspace := range m.workspaces {
		facts = append(facts, workspaceProjectionEnvelope(taskID, workspace, schema))
	}
	for checkpointID, checkpoint := range m.checkpoints {
		integrity, retention := checkpointProjectionEnvelopes(
			checkpoint.taskWorkspaceID, checkpointID, checkpoint, schema,
		)
		facts = append(facts, integrity, retention)
	}
	for _, record := range m.cleanupDebts {
		facts = append(facts, cleanupDebtProjectionEnvelope(record.debt, schema))
	}
	for auditID, evidence := range m.cleanupAuditFacts {
		facts = append(facts, ProjectionEnvelope{
			FactID:       projectionMandatoryAuditFactID(auditID),
			FactRevision: 1, SchemaRevision: schema,
			Kind: ProjectionFactMandatoryAudit, Operation: ProjectionOperationAudit,
			Result: ProjectionResultCommitted, LifecycleState: ProjectionStateAuditCommitted,
			ResourceClass: ProjectionResourceAudit, AdapterClass: ProjectionAdapterDeterministic,
			RecordedAt: evidence.RecordedAt,
		})
	}
	return facts
}

func authoritativeProjectionWatermark(facts []ProjectionEnvelope) uint64 {
	var watermark uint64
	for _, fact := range facts {
		if ^uint64(0)-watermark < uint64(fact.FactRevision) {
			return ^uint64(0)
		}
		watermark += uint64(fact.FactRevision)
	}
	return watermark
}

func projectionFactID(kind ProjectionFactKind, subject any) ProjectionFactID {
	return ProjectionFactID(canonicalDigest(struct {
		Kind    ProjectionFactKind
		Subject any
	}{Kind: kind, Subject: subject}))
}

func projectionMandatoryAuditFactID(auditID CleanupAuditEvidenceID) ProjectionFactID {
	return projectionFactID(ProjectionFactMandatoryAudit, struct {
		AuditID CleanupAuditEvidenceID
	}{auditID})
}

func auditDeliveryFact(evidence CleanupAuditEvidence) AuditDeliveryFact {
	return AuditDeliveryFact{
		AuditFactID: AuditDeliveryFactID(canonicalDigest(struct {
			Kind    string
			AuditID CleanupAuditEvidenceID
		}{"external_audit_delivery", evidence.ID})),
		Digest:     evidence.Digest,
		Action:     evidence.Action,
		Result:     auditDeliveryResult(evidence),
		RecordedAt: evidence.RecordedAt,
	}
}

func auditDeliveryResult(evidence CleanupAuditEvidence) AuditDeliveryResult {
	if evidence.Action == CleanupAuditQueryDiagnostics {
		return AuditDeliveryDiagnosticsAccessed
	}
	switch evidence.Resolution {
	case CleanupReclaimed:
		return AuditDeliveryReclaimed
	case CleanupAlreadyAbsent:
		return AuditDeliveryAlreadyAbsent
	case CleanupRetainedByAuthority:
		return AuditDeliveryRetainedByAuthority
	case CleanupAcceptedException:
		return AuditDeliveryAcceptedException
	default:
		return ""
	}
}

func projectionOperationRevision(state operationJournalState) ProjectionFactRevision {
	switch state {
	case operationJournalIntentPersisted:
		return 1
	case operationJournalReconciliationRequired:
		return 2
	case operationJournalTerminal:
		return 3
	default:
		return 1
	}
}

func projectionOperationState(
	inspection OperationInspection,
) (ProjectionOperation, ProjectionLifecycleState, ProjectionResourceClass) {
	switch {
	case inspection.ConfirmTaskWorkspace != nil:
		return ProjectionOperationConfirm, ProjectionStateActive, ProjectionResourceTaskWorkspace
	case inspection.Materialize != nil:
		return ProjectionOperationMaterialize, ProjectionStateMaterialized, ProjectionResourceTaskWorkspace
	case inspection.OpenRuntimeView != nil:
		return ProjectionOperationOpenView, ProjectionStateOpen, ProjectionResourceRuntimeView
	case inspection.CommitRuntimeView != nil:
		return ProjectionOperationCommit, ProjectionStateCommitted, ProjectionResourceTaskWorkspace
	case inspection.DiscardRuntimeView != nil:
		return ProjectionOperationDiscard, ProjectionStateDiscarded, ProjectionResourceRuntimeView
	case inspection.FenceRuntimeView != nil:
		return ProjectionOperationFence, ProjectionStateFenced, ProjectionResourceRuntimeView
	case inspection.ReconstructTaskWorkspace != nil:
		return ProjectionOperationReconstruct, ProjectionStateMaterialized, ProjectionResourceTaskWorkspace
	case inspection.ExpireMaterialization != nil, inspection.ExpireRuntimeView != nil:
		return ProjectionOperationExpire, ProjectionStateExpired, ProjectionResourceRuntimeView
	case inspection.RestoreTaskWorkspace != nil:
		return ProjectionOperationRestore, ProjectionStateRestored, ProjectionResourceTaskWorkspace
	case inspection.AttachCheckpointRetention != nil, inspection.ReleaseCheckpointRetention != nil:
		return ProjectionOperationRetention, ProjectionStateRetained, ProjectionResourceCheckpoint
	case inspection.ReclaimCheckpoint != nil:
		return ProjectionOperationReclaim, ProjectionStateReclaimed, ProjectionResourceCheckpoint
	case inspection.ReconcileCleanupDebt != nil:
		return ProjectionOperationCleanup,
			projectionCleanupDebtState(inspection.ReconcileCleanupDebt.State), ProjectionResourceCleanupDebt
	default:
		return ProjectionOperationOther, ProjectionStatePending, ProjectionResourceOther
	}
}

func projectionRetentionState(retention checkpointRetentionRecord) ProjectionLifecycleState {
	if retention.reclaimed {
		return ProjectionStateReclaimed
	}
	if len(retention.authorities) > 0 {
		return ProjectionStateRetained
	}
	return ProjectionStatePendingReclaim
}

func projectionCleanupDebtRevision(debt CleanupDebt) ProjectionFactRevision {
	counter := uint64(1) + uint64(debt.RetryGeneration) + debt.ResolutionGeneration +
		debt.AttemptCount + uint64(debt.ClaimGeneration) + debt.ConsecutiveFailureCount
	return ProjectionFactRevision((counter * 4) + cleanupDebtProjectionPhase(debt.State))
}

func cleanupDebtProjectionPhase(state CleanupDebtState) uint64 {
	switch state {
	case CleanupDebtClaimed:
		return 1
	case CleanupDebtRetryScheduled, CleanupDebtBlocked:
		return 2
	case CleanupDebtResolved:
		return 3
	default:
		return 0
	}
}

func projectionCleanupDebtState(state CleanupDebtState) ProjectionLifecycleState {
	switch state {
	case CleanupDebtOpen:
		return ProjectionStateCleanupOpen
	case CleanupDebtClaimed:
		return ProjectionStateCleanupClaimed
	case CleanupDebtRetryScheduled:
		return ProjectionStateCleanupRetry
	case CleanupDebtBlocked:
		return ProjectionStateCleanupBlocked
	case CleanupDebtResolved:
		return ProjectionStateCleanupResolved
	default:
		return ProjectionStateUnknown
	}
}

func (m *inMemory) QueryAdministratorDiagnostics(
	ctx context.Context,
	request QueryAdministratorDiagnosticsRequest,
) (AdministratorDiagnostics, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.Operation.ID == "" {
		return AdministratorDiagnostics{}, &Error{Code: ErrorOwnershipDenied}
	}
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	requestDigest := request.CanonicalRequestDigest()
	m.mu.Lock()
	if recorded, exists := m.diagnosticOperations[scope]; exists {
		if request.Operation.RequestDigest == requestDigest &&
			recorded.requestDigest == request.Operation.RequestDigest {
			result := cloneAdministratorDiagnostics(recorded.result)
			m.mu.Unlock()
			return result, nil
		}
		recordedAuthority := recorded.administratorAuthority
		m.mu.Unlock()
		if recordedAuthority != request.AdministratorAuthority ||
			!m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) {
			return AdministratorDiagnostics{}, &Error{Code: ErrorOwnershipDenied}
		}
		return AdministratorDiagnostics{}, &Error{Code: ErrorIntegrityConflict}
	}
	m.mu.Unlock()

	if request.DebtID == "" ||
		request.Subject != DiagnosticSubjectCleanupDebt ||
		request.Reason != DiagnosticReasonCleanupReconciliation ||
		request.Operation.RequestDigest != requestDigest ||
		!m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) || m.diagnostics == nil {
		return AdministratorDiagnostics{}, &Error{Code: ErrorOwnershipDenied}
	}
	intent := AdministratorDiagnosticIntent{
		PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID, DebtID: request.DebtID,
		Subject: request.Subject, Reason: request.Reason,
		AdministratorAuthority: request.AdministratorAuthority, Operation: request.Operation,
	}
	if err := m.diagnostics.Authorize(ctx, intent); err != nil {
		return AdministratorDiagnostics{}, &Error{Code: ErrorOwnershipDenied}
	}
	status := m.diagnostics.SourceStatus(ctx)
	if status.State != DiagnosticsSourceCurrent && status.State != DiagnosticsSourceStale &&
		status.State != DiagnosticsSourceUnavailable {
		status = DiagnosticsSourceStatus{State: DiagnosticsSourceUnavailable}
	}
	m.mu.Lock()
	var deliveryEvidence CleanupAuditEvidence
	defer func() {
		m.mu.Unlock()
		if deliveryEvidence.ID != "" {
			m.deliverRequiredAudit(ctx, deliveryEvidence)
		}
	}()
	if recorded, exists := m.diagnosticOperations[scope]; exists {
		if recorded.requestDigest != request.Operation.RequestDigest ||
			request.Operation.RequestDigest != requestDigest {
			if recorded.administratorAuthority != request.AdministratorAuthority {
				return AdministratorDiagnostics{}, &Error{Code: ErrorOwnershipDenied}
			}
			return AdministratorDiagnostics{}, &Error{Code: ErrorIntegrityConflict}
		}
		return cloneAdministratorDiagnostics(recorded.result), nil
	}
	record, exists := m.cleanupDebts[request.DebtID]
	workspace, workspaceExists := m.workspaces[request.TaskID]
	if !exists || !workspaceExists || record.debt.PolicyDomainID != request.PolicyDomainID ||
		record.debt.TaskID != request.TaskID || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != record.debt.TaskWorkspaceID {
		return AdministratorDiagnostics{}, &Error{Code: ErrorOwnershipDenied}
	}
	debt := record.debt
	var result AdministratorDiagnostics
	if status.State == DiagnosticsSourceStale || status.State == DiagnosticsSourceUnavailable {
		result = unknownAdministratorDiagnostics(status)
	} else {
		watermark := status.Watermark
		if !watermark.Known {
			watermark = SourceWatermark{
				Known: true,
				Value: authoritativeProjectionWatermark(
					authoritativeProjectionFactsLocked(m, ProjectionSchemaV1),
				),
			}
		}
		result = AdministratorDiagnostics{
			LifecycleState: diagnosticLifecycleState(debt.State),
			Relationship: DiagnosticRelationship{
				RevisionID: workspace.currentRevisionID, OperationID: debt.Operation.ID, Fence: debt.Fence,
			},
			CleanupOwner:   debt.Owner,
			EstimatedBytes: debt.Capacity.Bytes, EstimatedInodes: debt.Capacity.Inodes,
			SafeError:          cleanupFailureSafeCategory(debt.LastFailureCategory),
			Blockers:           append([]CleanupBlocker(nil), debt.Blockers...),
			NextAction:         diagnosticNextAction(debt),
			EvidenceReferences: diagnosticEvidenceReferences(debt),
			SourceState:        DiagnosticsSourceCurrent, SourceWatermark: watermark,
		}
		if debt.CreatedAt != 0 && m.now() >= debt.CreatedAt {
			result.RetryAge = DiagnosticDuration{Known: true, Value: Duration(m.now() - debt.CreatedAt)}
		}
	}
	auditIntent := CleanupAuditIntent{
		Action:                 CleanupAuditQueryDiagnostics,
		DebtID:                 debt.DebtID,
		AdministratorAuthority: request.AdministratorAuthority,
		DecisionEvidenceRoot: canonicalDigest(struct {
			Kind         string
			DebtRevision ProjectionFactRevision
			Subject      DiagnosticSubject
			Reason       DiagnosticReason
			SourceState  DiagnosticsSourceState
			Watermark    SourceWatermark
		}{
			"administrator_diagnostics", projectionCleanupDebtRevision(debt),
			request.Subject, request.Reason, result.SourceState, result.SourceWatermark,
		}),
		ResolutionGeneration: uint64(projectionCleanupDebtRevision(debt)),
		Operation:            contentFreeAuditOperation(scope, request.Operation),
	}
	auditEvidence, auditErr := m.recordRequiredCleanupAudit(ctx, auditIntent, func(evidence CleanupAuditEvidence) error {
		if !m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) {
			return &Error{Code: ErrorStaleAuthority}
		}
		if existing, factExists := m.cleanupAuditFacts[evidence.ID]; factExists && existing.Digest != evidence.Digest {
			return &Error{Code: ErrorIntegrityConflict}
		}
		m.cleanupAuditFacts[evidence.ID] = evidence
		m.diagnosticOperations[scope] = diagnosticOperationRecord{
			requestDigest:          request.Operation.RequestDigest,
			administratorAuthority: request.AdministratorAuthority,
			result:                 cloneAdministratorDiagnostics(result),
		}
		return nil
	})
	if auditErr != nil {
		return AdministratorDiagnostics{}, &Error{Code: ErrorIntegrityFailure}
	}
	deliveryEvidence = auditEvidence
	return cloneAdministratorDiagnostics(result), nil
}

func cloneAdministratorDiagnostics(result AdministratorDiagnostics) AdministratorDiagnostics {
	result.Blockers = append([]CleanupBlocker(nil), result.Blockers...)
	result.EvidenceReferences = append([]DiagnosticEvidenceReference(nil), result.EvidenceReferences...)
	return result
}

func unknownAdministratorDiagnostics(status DiagnosticsSourceStatus) AdministratorDiagnostics {
	return AdministratorDiagnostics{
		LifecycleState: DiagnosticLifecycleUnknown,
		EstimatedBytes: UnknownCleanupQuantity(), EstimatedInodes: UnknownCleanupQuantity(),
		SafeError: SafeErrorRetryableUnavailable, NextAction: DiagnosticNextRefreshSource,
		SourceState: status.State, SourceWatermark: status.Watermark,
	}
}

func diagnosticLifecycleState(state CleanupDebtState) DiagnosticLifecycleState {
	switch state {
	case CleanupDebtOpen:
		return DiagnosticLifecycleOpen
	case CleanupDebtClaimed:
		return DiagnosticLifecycleClaimed
	case CleanupDebtRetryScheduled:
		return DiagnosticLifecycleRetryScheduled
	case CleanupDebtBlocked:
		return DiagnosticLifecycleBlocked
	case CleanupDebtResolved:
		return DiagnosticLifecycleResolved
	default:
		return DiagnosticLifecycleUnknown
	}
}

func cleanupFailureSafeCategory(category CleanupFailureCategory) SafeErrorCategory {
	switch category {
	case CleanupFailureAdapterUnavailable:
		return SafeErrorRetryableUnavailable
	case CleanupFailureAmbiguous:
		return SafeErrorReconciliationRequired
	case CleanupFailureInvalidEvidence:
		return SafeErrorIntegrityUnavailableContent
	default:
		return ""
	}
}

func diagnosticNextAction(debt CleanupDebt) DiagnosticNextAction {
	switch debt.State {
	case CleanupDebtResolved:
		return DiagnosticNextNone
	case CleanupDebtBlocked:
		return DiagnosticNextWaitForAuthority
	default:
		return DiagnosticNextRetryCleanup
	}
}

func diagnosticEvidenceReferences(debt CleanupDebt) []DiagnosticEvidenceReference {
	result := make([]DiagnosticEvidenceReference, 0, 4)
	for _, reference := range []DiagnosticEvidenceReference{
		{Kind: DiagnosticEvidenceEligibility, Identity: debt.EligibilityEvidenceRoot},
		{Kind: DiagnosticEvidenceFailure, Identity: debt.SafeFailureEvidenceRoot},
		{Kind: DiagnosticEvidenceResolution, Identity: debt.ResolutionEvidenceRoot},
		{Kind: DiagnosticEvidenceAudit, Identity: debt.ResolutionAuditEvidenceRoot},
	} {
		if reference.Identity != "" {
			result = append(result, reference)
		}
	}
	return result
}
