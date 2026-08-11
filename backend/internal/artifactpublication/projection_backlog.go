package artifactpublication

// This file delivers child SPEC #111 (C05-07): the protected projection
// delivery backlog for the Artifact Publication module. External audit and
// telemetry projections are incomplete, expiring copies of retained
// authoritative audit facts: they are emitted strictly after the protected
// decision commits, their failure never rolls back the committed decision,
// and they are rebuildable from the retained canonical facts. Duplicate or
// out-of-order delivery is idempotent by AuditFactID and canonical digest;
// a corrupt or unknown retained fact fails closed and is NEVER projected as
// delivered or as zero. The inspection and rebuild surfaces are protected:
// reason-bound, least-privilege, exact-scope, bounded, and audited.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ProjectionDeliveryOutcome is the bounded delivery disposition of one
// retained projection fact.
type ProjectionDeliveryOutcome uint8

const (
	ProjectionDeliveryPending ProjectionDeliveryOutcome = iota + 1
	ProjectionDeliveryDelivered
	ProjectionDeliveryDeferred
)

// ProjectionDeliveryStatus is the durable per-channel delivery status.
type ProjectionDeliveryStatus uint8

const (
	ProjectionPending ProjectionDeliveryStatus = iota + 1
	ProjectionDelivered
	ProjectionFailed
)

// ProjectionDeliveryInspectionRequest is a reason-bound, exact-task,
// bounded protected operational request that inspects the delivery evidence
// of already committed projection facts. It can never create, advance, or
// repair publication, residue, or cleanup authority.
type ProjectionDeliveryInspectionRequest struct {
	authority AdministratorMetadataAuthority
	taskID    TaskID
	limit     uint32
}

func NewProjectionDeliveryInspectionRequest(
	authority AdministratorMetadataAuthority,
	taskID TaskID,
	limit uint32,
) ProjectionDeliveryInspectionRequest {
	return ProjectionDeliveryInspectionRequest{
		authority: authority, taskID: taskID, limit: limit,
	}
}

// ProjectionDeliveryRebuildRequest is the reason-bound, exact-task, bounded
// protected operational request that redrives only already committed
// projection facts. It can never create, advance, or repair publication,
// residue, or cleanup authority.
type ProjectionDeliveryRebuildRequest struct {
	authority AdministratorMetadataAuthority
	taskID    TaskID
	limit     uint32
}

func NewProjectionDeliveryRebuildRequest(
	authority AdministratorMetadataAuthority,
	taskID TaskID,
	limit uint32,
) ProjectionDeliveryRebuildRequest {
	return ProjectionDeliveryRebuildRequest{
		authority: authority, taskID: taskID, limit: limit,
	}
}

// PublicationProjectionBacklog is a bounded, content-free inspection of
// delivery evidence rebuilt from retained authoritative audit facts.
// SourceFactCount is always derived from retained facts; an unknown or
// missing source is never projected as zero.
type PublicationProjectionBacklog struct {
	Pending         uint64
	Delivered       uint64
	SourceFactCount uint64
	Evidence        []ProjectionDeliveryEvidence
}

// ProjectionDeliveryEvidence is the content-free delivery evidence of one
// retained authoritative audit fact.
type ProjectionDeliveryEvidence struct {
	AuditFactID        string
	CanonicalDigest    AuditFactDigest
	TaskID             TaskID
	OperationID        PublicationOperationID
	Action             PublicationIntentKind
	State              PublicationOperationState
	AttemptCount       uint64
	AuditDelivered     bool
	TelemetryDelivered bool
	LastOutcome        ProjectionDeliveryOutcome
	LastAttemptAt      time.Time
	FirstDeliveredAt   time.Time
	LastDeliveredAt    time.Time
}

// ProjectionBacklogAuthority is the protected read/redrive seam of the
// projection delivery backlog. Both the deterministic in-memory authority
// and the real PostgreSQL adapter implement it; neither surface can mutate
// publication, residue, or cleanup authority.
type ProjectionBacklogAuthority interface {
	InspectProjectionBacklog(context.Context, ProjectionDeliveryInspectionRequest) (PublicationProjectionBacklog, error)
	RebuildProjectionDelivery(context.Context, ProjectionDeliveryRebuildRequest) (PublicationProjectionBacklog, error)
}

var _ ProjectionBacklogAuthority = (*inMemory)(nil)

func validProjectionDeliveryAuthority(authority AdministratorMetadataAuthority) bool {
	return authority.valid()
}

func validProjectionDeliveryTask(taskID TaskID) bool {
	return taskID != ""
}

func validProjectionDeliveryLimit(limit uint32) bool {
	return limit > 0 && limit <= 100
}

// InspectProjectionBacklog returns the bounded delivery evidence for one
// exact Task, rebuilt from retained authoritative audit facts. It is
// read-only and cannot mutate publication or projection state.
func (m *inMemory) InspectProjectionBacklog(
	ctx context.Context,
	request ProjectionDeliveryInspectionRequest,
) (PublicationProjectionBacklog, error) {
	if m == nil || m.persistence == nil || ctx == nil || ctx.Err() != nil {
		return PublicationProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	if !validProjectionDeliveryAuthority(request.authority) ||
		!validProjectionDeliveryTask(request.taskID) ||
		!validProjectionDeliveryLimit(request.limit) {
		return PublicationProjectionBacklog{}, newError(ErrorOwnershipDenied)
	}
	m.persistence.mu.Lock()
	defer m.persistence.mu.Unlock()
	if m.config.DiagnosticAuditFaults != nil && m.config.DiagnosticAuditFaults.consume() {
		return PublicationProjectionBacklog{}, newError(ErrorRetryableUnavailable)
	}
	return m.inspectProjectionBacklogLocked(request.taskID, request.limit), nil
}

// RebuildProjectionDelivery retries only already committed projection facts
// reconstructed from retained authoritative audit facts. Delivery failure
// keeps a durable backlog and degrades health; it never rolls back the
// committed decision and never invents a delivered status for an unknown
// fact.
func (m *inMemory) RebuildProjectionDelivery(
	ctx context.Context,
	request ProjectionDeliveryRebuildRequest,
) (PublicationProjectionBacklog, error) {
	if m == nil || m.persistence == nil || ctx == nil || ctx.Err() != nil {
		return PublicationProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	if !validProjectionDeliveryAuthority(request.authority) ||
		!validProjectionDeliveryTask(request.taskID) ||
		!validProjectionDeliveryLimit(request.limit) {
		return PublicationProjectionBacklog{}, newError(ErrorOwnershipDenied)
	}
	m.persistence.mu.Lock()
	defer m.persistence.mu.Unlock()
	if m.config.DiagnosticAuditFaults != nil && m.config.DiagnosticAuditFaults.consume() {
		return PublicationProjectionBacklog{}, newError(ErrorRetryableUnavailable)
	}
	var attempted uint32
	for _, fact := range m.persistence.auditFacts {
		if fact.TaskID != request.taskID {
			continue
		}
		if attempted >= request.limit {
			break
		}
		delivery := m.persistence.projectionDeliveries[fact.AuditFactID]
		if delivery != nil && delivery.auditDelivered && delivery.telemetryDelivered {
			continue
		}
		m.deliverProjection(fact)
		attempted++
	}
	return m.inspectProjectionBacklogLocked(request.taskID, request.limit), nil
}

func (m *inMemory) inspectProjectionBacklogLocked(taskID TaskID, limit uint32) PublicationProjectionBacklog {
	backlog := PublicationProjectionBacklog{
		Evidence: make([]ProjectionDeliveryEvidence, 0),
	}
	for _, fact := range m.persistence.auditFacts {
		if fact.TaskID != taskID {
			continue
		}
		backlog.SourceFactCount++
		delivery := m.persistence.projectionDeliveries[fact.AuditFactID]
		evidence := ProjectionDeliveryEvidence{
			AuditFactID: fact.AuditFactID, CanonicalDigest: fact.CanonicalDigest,
			TaskID: fact.TaskID, OperationID: fact.OperationID,
			Action: fact.Action, State: fact.State,
		}
		if delivery != nil {
			evidence.AttemptCount = delivery.attemptCount
			evidence.AuditDelivered = delivery.auditDelivered
			evidence.TelemetryDelivered = delivery.telemetryDelivered
			if delivery.auditDelivered && delivery.telemetryDelivered {
				evidence.LastOutcome = ProjectionDeliveryDelivered
				backlog.Delivered++
			} else if delivery.attemptCount > 0 {
				evidence.LastOutcome = ProjectionDeliveryDeferred
				backlog.Pending++
			} else {
				evidence.LastOutcome = ProjectionDeliveryPending
				backlog.Pending++
			}
		} else {
			evidence.LastOutcome = ProjectionDeliveryPending
			backlog.Pending++
		}
		backlog.Evidence = append(backlog.Evidence, evidence)
		if uint32(len(backlog.Evidence)) >= limit {
			break
		}
	}
	return backlog
}

// ---------------------------------------------------------------------------
// PostgreSQL durable projection backlog
// ---------------------------------------------------------------------------

// ProjectionDeliveryInspectionRequestPG is the PostgreSQL implementation of
// the protected projection backlog inspection surface. The PostgresAuthority
// implements ProjectionBacklogAuthority below.
var _ ProjectionBacklogAuthority = (*PostgresAuthority)(nil)

// InspectProjectionBacklog returns the bounded delivery evidence for one
// exact Task, rebuilt from the retained publication_audit facts.
func (p *PostgresAuthority) InspectProjectionBacklog(
	ctx context.Context,
	request ProjectionDeliveryInspectionRequest,
) (PublicationProjectionBacklog, error) {
	if p == nil || p.db == nil || ctx == nil || ctx.Err() != nil {
		return PublicationProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	if !validProjectionDeliveryAuthority(request.authority) ||
		!validProjectionDeliveryTask(request.taskID) ||
		!validProjectionDeliveryLimit(request.limit) {
		return PublicationProjectionBacklog{}, newError(ErrorOwnershipDenied)
	}
	if p.diagnosticAuditFaults != nil && p.diagnosticAuditFaults.consume() {
		return PublicationProjectionBacklog{}, newError(ErrorRetryableUnavailable)
	}
	return p.inspectProjectionBacklogPG(ctx, request.taskID, request.limit)
}

// RebuildProjectionDelivery retries only already committed projection facts
// reconstructed from retained authoritative audit facts. Delivery failure
// keeps a durable backlog; it never rolls back the committed decision.
func (p *PostgresAuthority) RebuildProjectionDelivery(
	ctx context.Context,
	request ProjectionDeliveryRebuildRequest,
) (PublicationProjectionBacklog, error) {
	if p == nil || p.db == nil || ctx == nil || ctx.Err() != nil {
		return PublicationProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	if !validProjectionDeliveryAuthority(request.authority) ||
		!validProjectionDeliveryTask(request.taskID) ||
		!validProjectionDeliveryLimit(request.limit) {
		return PublicationProjectionBacklog{}, newError(ErrorOwnershipDenied)
	}
	if p.diagnosticAuditFaults != nil && p.diagnosticAuditFaults.consume() {
		return PublicationProjectionBacklog{}, newError(ErrorRetryableUnavailable)
	}
	sources, err := p.loadProjectionDeliverySourcesPG(ctx, request.taskID, request.limit, true)
	if err != nil {
		return PublicationProjectionBacklog{}, err
	}
	var attempted uint32
	for _, source := range sources {
		if attempted >= request.limit {
			break
		}
		if source.delivery.AuditDelivered && source.delivery.TelemetryDelivered {
			continue
		}
		if err := p.beginProjectionDeliveryAttemptPG(ctx, source.fact); err != nil {
			return PublicationProjectionBacklog{}, err
		}
		auditDelivered := source.delivery.AuditDelivered
		telemetryDelivered := source.delivery.TelemetryDelivered
		if p.externalAudit != nil && !auditDelivered {
			auditDelivered = p.externalAudit.ProjectExternalAudit(ctx, source.audit) == nil
		}
		if p.telemetry != nil && !telemetryDelivered {
			telemetryDelivered = p.telemetry.ProjectTelemetry(ctx, source.telemetry) == nil
		}
		if err := p.finishProjectionDeliveryAttemptPG(ctx, source.fact, auditDelivered, telemetryDelivered); err != nil {
			return PublicationProjectionBacklog{}, err
		}
		attempted++
	}
	return p.inspectProjectionBacklogPG(ctx, request.taskID, request.limit)
}

func (p *PostgresAuthority) inspectProjectionBacklogPG(
	ctx context.Context,
	taskID TaskID,
	limit uint32,
) (PublicationProjectionBacklog, error) {
	sources, err := p.loadProjectionDeliverySourcesPG(ctx, taskID, limit, false)
	if err != nil {
		return PublicationProjectionBacklog{}, err
	}
	backlog := PublicationProjectionBacklog{
		Evidence: make([]ProjectionDeliveryEvidence, 0, len(sources)),
	}
	for _, source := range sources {
		evidence := source.delivery
		backlog.SourceFactCount++
		if evidence.AuditDelivered && evidence.TelemetryDelivered {
			backlog.Delivered++
		} else {
			backlog.Pending++
		}
		backlog.Evidence = append(backlog.Evidence, evidence)
	}
	return backlog, nil
}

type projectionDeliverySourcePG struct {
	fact      PublicationAuditFact
	audit     ExternalAuditProjection
	telemetry PublicationTelemetryProjection
	delivery  ProjectionDeliveryEvidence
}

func (p *PostgresAuthority) loadProjectionDeliverySourcesPG(
	ctx context.Context,
	taskID TaskID,
	limit uint32,
	pendingOnly bool,
) ([]projectionDeliverySourcePG, error) {
	// Parameter numbering depends on whether the pending predicate adds $2:
	// non-pending queries use ($1 pending, $2 task, $3 limit); pending
	// queries use ($1 pending, $2 failed, $3 task, $4 limit).
	taskParam := "$2"
	limitParam := "$3"
	args := []any{ProjectionPending, string(taskID), limit}
	if pendingOnly {
		taskParam = "$3"
		limitParam = "$4"
		args = []any{ProjectionPending, ProjectionFailed, string(taskID), limit}
	}
	query := `SELECT audit.audit_id, audit.schema_version, audit.integrity_version, audit.owning_module,
		audit.canonical_digest, audit.policy_domain_id, audit.task_id, audit.operation_id,
		audit.request_id, audit.request_digest, audit.intent_kind, audit.result, audit.actor_kind,
		audit.actor_id, audit.actor_generation, audit.state, audit.version_id, audit.manifest_digest,
		audit.lineage_digest, audit.stream_revision, audit.occurred_at, audit.recorded_at, audit.source_clock,
		COALESCE(delivery.audit_delivery_status, $1),
		COALESCE(delivery.telemetry_delivery_status, $1),
		COALESCE(delivery.attempt_count, 0),
		delivery.first_attempt_at, delivery.last_attempt_at, delivery.delivered_at
		FROM ` + p.q("publication_audit") + ` AS audit
		LEFT JOIN ` + p.q("publication_projection_backlog") + ` AS delivery ON delivery.audit_fact_id = 'audit-' || audit.audit_id::text
		WHERE audit.task_id=` + taskParam
	if pendingOnly {
		// A fact is pending when it was never attempted or at least one
		// channel is still pending or failed; a fact whose channels are BOTH
		// delivered is never redriven.
		query += ` AND (delivery.audit_fact_id IS NULL OR delivery.audit_delivery_status=$1 OR
			delivery.telemetry_delivery_status=$1 OR delivery.audit_delivery_status=$2 OR
			delivery.telemetry_delivery_status=$2)`
	}
	query += ` ORDER BY audit.audit_id LIMIT ` + limitParam
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, newProjectionError(ProjectionUnavailable)
	}
	defer rows.Close()
	sources := make([]projectionDeliverySourcePG, 0)
	for rows.Next() {
		var auditFactID int64
		var schemaVersion uint32
		var integrityVersion uint16
		var owningModule uint8
		var canonicalDigest []byte
		var policyDomainID, taskIDValue, operationID, requestID, requestDigest string
		var action, resultRaw, actorKind, actorID, state, versionID, manifestDigest, lineageDigest string
		var actorGeneration, streamRevision uint64
		var occurredAt, recordedAt int64
		var sourceClock uint8
		var auditStatus, telemetryStatus ProjectionDeliveryStatus
		var attemptCount uint64
		var firstAttemptAt, lastAttemptAt, deliveredAt sqlNullTime
		if err := rows.Scan(&auditFactID, &schemaVersion, &integrityVersion, &owningModule,
			&canonicalDigest, &policyDomainID, &taskIDValue, &operationID,
			&requestID, &requestDigest, &action, &resultRaw, &actorKind,
			&actorID, &actorGeneration, &state, &versionID, &manifestDigest,
			&lineageDigest, &streamRevision, &occurredAt, &recordedAt, &sourceClock,
			&auditStatus, &telemetryStatus, &attemptCount,
			&firstAttemptAt, &lastAttemptAt, &deliveredAt); err != nil {
			return nil, newProjectionError(ProjectionUnavailable)
		}
		if taskIDValue != string(taskID) || len(canonicalDigest) != 32 ||
			schemaVersion != uint32(AuditSchemaV1) || integrityVersion != uint16(AuditIntegrityV1) ||
			owningModule != uint8(AuditModuleArtifactPublication) || sourceClock != uint8(AuditSourceArtifactPublicationClock) {
			return nil, newProjectionError(ProjectionInvalidFact)
		}
		fact := PublicationAuditFact{
			SchemaVersion:       AuditSchemaVersion(schemaVersion),
			IntegrityVersion:    AuditIntegrityVersion(integrityVersion),
			AuditFactID:         fmt.Sprintf("audit-%d", auditFactID),
			OwningModule:        AuditOwningModule(owningModule),
			PolicyDomainID:      PolicyDomainID(policyDomainID),
			TaskID:              TaskID(taskIDValue),
			OperationID:         PublicationOperationID(operationID),
			RequestID:           PublicationRequestID(requestID),
			RequestDigest:       Digest(requestDigest),
			Action:              PublicationIntentKind(action),
			Result:              AuditResult(parseUint64(resultRaw)),
			AuthorityKind:       EvidenceAuthorityKind(actorKind),
			AuthorityID:         AuthorityID(actorID),
			AuthorityGeneration: Generation(actorGeneration),
			State:               PublicationOperationState(state),
			VersionID:           ArtifactVersionID(versionID),
			ManifestDigest:      Digest(manifestDigest),
			LineageDigest:       Digest(lineageDigest),
			StreamRevision:      StreamRevision(streamRevision),
			OccurredAt:          Instant(occurredAt),
			RecordedAt:          time.Unix(recordedAt, 0).UTC(),
			SourceClock:         AuditSourceClock(sourceClock),
		}
		// The retained audit row stores the canonical digest; a corrupt or
		// unknown retained fact fails closed and is never projected as
		// delivered or as zero.
		wantDigest := auditDigestFromBytes(canonicalDigest)
		fact.CanonicalDigest = wantDigest
		if !validPublicationAuditFact(fact) {
			return nil, newProjectionError(ProjectionInvalidFact)
		}
		evidence := ProjectionDeliveryEvidence{
			AuditFactID: fact.AuditFactID, CanonicalDigest: wantDigest,
			TaskID: fact.TaskID, OperationID: fact.OperationID,
			Action: fact.Action, State: fact.State, AttemptCount: attemptCount,
			AuditDelivered:     auditStatus == ProjectionDelivered,
			TelemetryDelivered: telemetryStatus == ProjectionDelivered,
		}
		if evidence.AuditDelivered && evidence.TelemetryDelivered {
			evidence.LastOutcome = ProjectionDeliveryDelivered
		} else if attemptCount > 0 {
			evidence.LastOutcome = ProjectionDeliveryDeferred
		} else {
			evidence.LastOutcome = ProjectionDeliveryPending
		}
		if lastAttemptAt.Valid {
			evidence.LastAttemptAt = lastAttemptAt.Time.UTC()
		}
		if firstAttemptAt.Valid {
			evidence.FirstDeliveredAt = firstAttemptAt.Time.UTC()
		}
		if deliveredAt.Valid {
			evidence.LastDeliveredAt = deliveredAt.Time.UTC()
		}
		sources = append(sources, projectionDeliverySourcePG{
			fact:      fact,
			audit:     auditProjectionFromFact(fact),
			telemetry: telemetryProjectionFromAudit(fact, p.telemetryAdapter),
			delivery:  evidence,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, newProjectionError(ProjectionUnavailable)
	}
	return sources, nil
}

// validPublicationAuditFactFields validates every field of a reconstructed
// audit fact except the canonical digest (which the caller compares against
// the retained digest separately).
func validPublicationAuditFactFields(fact PublicationAuditFact) bool {
	return fact.SchemaVersion == AuditSchemaV1 && fact.IntegrityVersion == AuditIntegrityV1 &&
		fact.OwningModule == AuditModuleArtifactPublication && fact.AuditFactID != "" &&
		fact.PolicyDomainID != "" && fact.TaskID != "" && fact.OperationID != "" &&
		fact.RequestDigest != "" && validIntentKind(fact.Action) && fact.Result != 0 &&
		validEvidenceAuthorityKind(fact.AuthorityKind) && fact.AuthorityID != "" &&
		fact.AuthorityGeneration != 0 && validOperationState(fact.State) &&
		!fact.RecordedAt.IsZero() && fact.SourceClock == AuditSourceArtifactPublicationClock
}

func (p *PostgresAuthority) beginProjectionDeliveryAttemptPG(ctx context.Context, fact PublicationAuditFact) error {
	now := p.nowTimeValue()
	result, err := p.db.ExecContext(ctx, `INSERT INTO `+p.q("publication_projection_backlog")+`
		(audit_fact_id, audit_canonical_digest, task_id, operation_id, action, state,
		 audit_delivery_status, telemetry_delivery_status, attempt_count, degraded,
		 first_attempt_at, last_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, FALSE, $9, $9)
		ON CONFLICT (audit_fact_id) DO UPDATE SET
			attempt_count = `+p.q("publication_projection_backlog")+`.attempt_count + 1,
			last_attempt_at = $9,
			first_attempt_at = COALESCE(`+p.q("publication_projection_backlog")+`.first_attempt_at, $9)`,
		fact.AuditFactID, fact.CanonicalDigest[:], string(fact.TaskID), string(fact.OperationID),
		string(fact.Action), string(fact.State), ProjectionPending, ProjectionPending, now)
	if err != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return newProjectionError(ProjectionInvalidFact)
	}
	return nil
}

func (p *PostgresAuthority) finishProjectionDeliveryAttemptPG(
	ctx context.Context,
	fact PublicationAuditFact,
	auditDelivered bool,
	telemetryDelivered bool,
) error {
	auditStatus := ProjectionFailed
	if auditDelivered {
		auditStatus = ProjectionDelivered
	}
	telemetryStatus := ProjectionFailed
	if telemetryDelivered {
		telemetryStatus = ProjectionDelivered
	}
	degraded := !auditDelivered || !telemetryDelivered
	deliveredAtSetter := "delivered_at=delivered_at"
	deliveredAt := any(nil)
	if auditDelivered && telemetryDelivered {
		deliveredAtSetter = "delivered_at=COALESCE(delivered_at,$1)"
		deliveredAt = p.nowTimeValue()
	}
	result, err := p.db.ExecContext(ctx, `UPDATE `+p.q("publication_projection_backlog")+` SET
		audit_delivery_status=$2, telemetry_delivery_status=$3, degraded=$4,
		last_attempt_at=$1, `+deliveredAtSetter+`
		WHERE audit_fact_id=$5 AND audit_canonical_digest=$6`,
		p.nowTimeValue(), auditStatus, telemetryStatus, degraded, fact.AuditFactID, fact.CanonicalDigest[:])
	if err != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return newProjectionError(ProjectionInvalidFact)
	}
	_ = deliveredAt
	return nil
}

func auditDigestFromBytes(value []byte) AuditFactDigest {
	var digest AuditFactDigest
	copy(digest[:], value)
	return digest
}

// sqlNullTime is a minimal nullable time scanner used by the projection
// backlog queries.
type sqlNullTime struct {
	Time  time.Time
	Valid bool
}

func (null *sqlNullTime) Scan(value any) error {
	if value == nil {
		null.Valid = false
		return nil
	}
	switch typed := value.(type) {
	case time.Time:
		null.Time = typed
		null.Valid = true
		return nil
	case []byte:
		parsed, err := time.Parse(time.RFC3339Nano, string(typed))
		if err != nil {
			return err
		}
		null.Time = parsed
		null.Valid = true
		return nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return err
		}
		null.Time = parsed
		null.Valid = true
		return nil
	default:
		return errors.New("unsupported null time scan value")
	}
}
