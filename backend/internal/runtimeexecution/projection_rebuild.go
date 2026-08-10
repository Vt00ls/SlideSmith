package runtimeexecution

import (
	"context"
	"database/sql"
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

// ProjectionDeliveryRebuildRequest is a reason-bound, exact-runtime-run,
// bounded protected operational request that redrives only already committed
// projection facts. It can never create, advance, or repair Runtime state,
// capacity, or cleanup authority.
type ProjectionDeliveryRebuildRequest struct {
	authority    RuntimeAuthority
	runtimeRunID RuntimeRunID
	limit        uint32
}

func NewProjectionDeliveryRebuildRequest(
	authority RuntimeAuthority,
	runtimeRunID RuntimeRunID,
	limit uint32,
) ProjectionDeliveryRebuildRequest {
	return ProjectionDeliveryRebuildRequest{
		authority: authority, runtimeRunID: runtimeRunID, limit: limit,
	}
}

// ProjectionDeliveryInspectionRequest is the read-only protected inspection
// counterpart of the rebuild request.
type ProjectionDeliveryInspectionRequest struct {
	authority    RuntimeAuthority
	runtimeRunID RuntimeRunID
	limit        uint32
}

func NewProjectionDeliveryInspectionRequest(
	authority RuntimeAuthority,
	runtimeRunID RuntimeRunID,
	limit uint32,
) ProjectionDeliveryInspectionRequest {
	return ProjectionDeliveryInspectionRequest{
		authority: authority, runtimeRunID: runtimeRunID, limit: limit,
	}
}

// RuntimeProjectionBacklog is a bounded, content-free inspection of delivery
// evidence rebuilt from retained authoritative audit facts. SourceFactCount is
// always derived from retained facts; an unknown or missing source is never
// projected as zero.
type RuntimeProjectionBacklog struct {
	Pending         uint64
	Delivered       uint64
	SourceFactCount uint64
	Evidence        []ProjectionDeliveryEvidence
}

type ProjectionDeliveryEvidence struct {
	DecisionID         RuntimeDecisionID
	AuditFactID        string
	CanonicalDigest    Digest
	AttemptCount       uint64
	AuditDelivered     bool
	TelemetryDelivered bool
	LastOutcome        ProjectionDeliveryOutcome
	LastAttemptAt      time.Time
	FirstDeliveredAt   time.Time
	LastDeliveredAt    time.Time
}

type projectionDeliverySource struct {
	fact      ProjectionFact
	telemetry RuntimeTelemetryProjection
	evidence  ProjectionDeliveryEvidence
}

// InspectProjectionBacklog returns the bounded delivery evidence for one exact
// Runtime Run, rebuilt from retained authoritative audit facts. It is
// read-only and cannot mutate Runtime or projection state.
func (authority *PostgresAuthority) InspectProjectionBacklog(
	ctx context.Context,
	request ProjectionDeliveryInspectionRequest,
) (RuntimeProjectionBacklog, error) {
	if authority == nil || authority.db == nil || ctx == nil || ctx.Err() != nil {
		return RuntimeProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	if !validProjectionDeliveryAuthority(request.authority) ||
		!validOpaqueID(request.runtimeRunID.String()) {
		return RuntimeProjectionBacklog{}, newError(ErrorAuthorizationDenied)
	}
	if request.limit == 0 || request.limit > 100 {
		return RuntimeProjectionBacklog{}, newProjectionError(ProjectionInvalidConfiguration)
	}
	return authority.inspectProjectionBacklog(ctx, request.runtimeRunID, request.limit)
}

// RebuildProjectionDelivery retries only already committed projection facts
// reconstructed from retained authoritative audit facts. Delivery failure
// keeps a durable backlog and degrades health; it never rolls back the
// committed decision and never invents a delivered status for an unknown fact.
func (authority *PostgresAuthority) RebuildProjectionDelivery(
	ctx context.Context,
	request ProjectionDeliveryRebuildRequest,
) (RuntimeProjectionBacklog, error) {
	if authority == nil || authority.db == nil || ctx == nil || ctx.Err() != nil {
		return RuntimeProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	if !validProjectionDeliveryAuthority(request.authority) ||
		!validOpaqueID(request.runtimeRunID.String()) {
		return RuntimeProjectionBacklog{}, newError(ErrorAuthorizationDenied)
	}
	if request.limit == 0 || request.limit > 100 {
		return RuntimeProjectionBacklog{}, newProjectionError(ProjectionInvalidConfiguration)
	}
	sources, err := authority.loadProjectionDeliverySources(ctx, request.runtimeRunID, request.limit, true)
	if err != nil {
		return RuntimeProjectionBacklog{}, err
	}
	var attempted uint32
	for _, source := range sources {
		if attempted >= request.limit {
			break
		}
		if source.evidence.AuditDelivered && source.evidence.TelemetryDelivered {
			continue
		}
		if err := authority.beginProjectionDeliveryAttempt(ctx, source); err != nil {
			return RuntimeProjectionBacklog{}, err
		}
		auditDelivered := source.evidence.AuditDelivered
		telemetryDelivered := source.evidence.TelemetryDelivered
		if authority.projection != nil && !auditDelivered {
			auditDelivered = authority.projection.Deliver(ctx, source.fact) == nil
		}
		if authority.telemetry != nil && !telemetryDelivered {
			telemetryDelivered = authority.telemetry.ProjectTelemetry(ctx, source.telemetry) == nil
		}
		if err := authority.finishProjectionDeliveryAttempt(
			ctx, source.evidence.DecisionID, source.evidence.AuditFactID,
			source.evidence.CanonicalDigest, source.fact.RuntimeRevision,
			auditDelivered, telemetryDelivered,
		); err != nil {
			return RuntimeProjectionBacklog{}, err
		}
		attempted++
	}
	return authority.inspectProjectionBacklog(ctx, request.runtimeRunID, request.limit)
}

func validProjectionDeliveryAuthority(authority RuntimeAuthority) bool {
	return validAuthority(authority) && authority.kind == AuthorityAdministrator
}

func (authority *PostgresAuthority) loadProjectionDeliverySources(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	limit uint32,
	pendingOnly bool,
) ([]projectionDeliverySource, error) {
	pendingPredicate := ""
	if pendingOnly {
		pendingPredicate = fmt.Sprintf(" AND (delivery.audit_delivery_status IS NULL OR"+
			" delivery.audit_delivery_status=%d OR delivery.telemetry_delivery_status=%d)",
			ProjectionPending, ProjectionFailed)
	}
	rows, err := authority.db.QueryContext(ctx, fmt.Sprintf(`%s
		SELECT source.audit_fact_id, source.decision_id, source.runtime_run_id,
		source.operation_id, source.request_digest, source.audit_state,
		source.canonical_digest, source.after_revision,
		COALESCE(delivery.audit_delivery_status, $1),
		COALESCE(delivery.telemetry_delivery_status, $1),
		COALESCE(delivery.attempt_count, 0),
		delivery.first_attempt_at, delivery.last_attempt_at, delivery.delivered_at
		FROM projection_sources AS source
		LEFT JOIN %s AS delivery ON delivery.fact_id=source.decision_id
		WHERE source.runtime_run_id=$2%s
		ORDER BY source.audit_fact_id
		LIMIT $3`,
		authority.projectionDeliverySourcesCTE(),
		authority.table("runtime_execution_projection_backlog"),
		pendingPredicate,
	), ProjectionPending, runtimeRunID.String(), limit)
	if err != nil {
		return nil, newProjectionError(ProjectionUnavailable)
	}
	defer rows.Close()
	sources := make([]projectionDeliverySource, 0)
	for rows.Next() {
		var auditFactID, decisionID, sourceRuntimeRunID, operationID string
		var requestDigest, auditStateBytes, canonicalDigest []byte
		var afterRevision RuntimeRevision
		var auditStatus, telemetryStatus ProjectionDeliveryStatus
		var attemptCount uint64
		var firstAttemptAt, lastAttemptAt, deliveredAt sql.NullTime
		if err := rows.Scan(
			&auditFactID, &decisionID, &sourceRuntimeRunID, &operationID,
			&requestDigest, &auditStateBytes, &canonicalDigest, &afterRevision,
			&auditStatus, &telemetryStatus, &attemptCount,
			&firstAttemptAt, &lastAttemptAt, &deliveredAt,
		); err != nil {
			return nil, newProjectionError(ProjectionUnavailable)
		}
		if sourceRuntimeRunID != runtimeRunID.String() || !validOpaqueID(auditFactID) ||
			!validOpaqueID(decisionID) || !validOpaqueID(operationID) {
			return nil, newProjectionError(ProjectionInvalidFact)
		}
		state, err := decodePostgresMandatoryAuditState(auditStateBytes)
		if err != nil || state.AuditFactID != auditFactID || state.DecisionID != decisionID ||
			state.RuntimeRunID != sourceRuntimeRunID || state.AfterRevision != afterRevision {
			// A corrupt or unknown retained fact fails closed; it is never
			// projected as delivered or as zero.
			return nil, newProjectionError(ProjectionInvalidFact)
		}
		fact := ProjectionFact{
			DecisionID:              RuntimeDecisionID{value: decisionID},
			RuntimeRunID:            RuntimeRunID{value: sourceRuntimeRunID},
			OperationID:             OperationID{value: operationID},
			CanonicalDigest:         digestFromBytes(requestDigest),
			RuntimeRevision:         afterRevision,
			AuditFactID:             auditFactID,
			AuditCanonicalDigest:    digestFromBytes(canonicalDigest),
			ProjectionSchemaVersion: SchemaV1,
		}
		if len(requestDigest) != len(Digest{}) || len(canonicalDigest) != len(Digest{}) ||
			fact.CanonicalDigest == (Digest{}) || fact.AuditCanonicalDigest == (Digest{}) {
			return nil, newProjectionError(ProjectionInvalidFact)
		}
		telemetry, err := telemetryProjectionFromAudit(fact, state)
		if err != nil {
			return nil, newProjectionError(ProjectionInvalidFact)
		}
		source := projectionDeliverySource{fact: fact, telemetry: telemetry}
		source.evidence = ProjectionDeliveryEvidence{
			DecisionID: fact.DecisionID, AuditFactID: auditFactID,
			CanonicalDigest: fact.AuditCanonicalDigest, AttemptCount: attemptCount,
			AuditDelivered:     auditStatus == ProjectionDelivered,
			TelemetryDelivered: telemetryStatus == ProjectionDelivered,
		}
		if auditStatus == ProjectionDelivered && telemetryStatus == ProjectionDelivered {
			source.evidence.LastOutcome = ProjectionDeliveryDelivered
		} else if attemptCount > 0 {
			source.evidence.LastOutcome = ProjectionDeliveryDeferred
		} else {
			source.evidence.LastOutcome = ProjectionDeliveryPending
		}
		if lastAttemptAt.Valid {
			source.evidence.LastAttemptAt = lastAttemptAt.Time.UTC()
		}
		if firstAttemptAt.Valid {
			source.evidence.FirstDeliveredAt = firstAttemptAt.Time.UTC()
		}
		if deliveredAt.Valid {
			source.evidence.LastDeliveredAt = deliveredAt.Time.UTC()
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, newProjectionError(ProjectionUnavailable)
	}
	return sources, nil
}

func (authority *PostgresAuthority) projectionDeliverySourcesCTE() string {
	return fmt.Sprintf(`WITH projection_sources AS (
		SELECT audit_fact_id, decision_id, runtime_run_id, operation_id,
			request_digest, audit_state, canonical_digest, after_revision
		FROM %s
	)`, authority.table("runtime_execution_mandatory_audit"))
}

func (authority *PostgresAuthority) inspectProjectionBacklog(
	ctx context.Context,
	runtimeRunID RuntimeRunID,
	limit uint32,
) (RuntimeProjectionBacklog, error) {
	sources, err := authority.loadProjectionDeliverySources(ctx, runtimeRunID, limit, false)
	if err != nil {
		return RuntimeProjectionBacklog{}, err
	}
	var sourceCount, deliveredCount uint64
	if err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`%s
		SELECT COUNT(*), COUNT(*) FILTER (WHERE
			delivery.audit_delivery_status=$1 AND delivery.telemetry_delivery_status=$1)
		FROM projection_sources AS source
		LEFT JOIN %s AS delivery ON delivery.fact_id=source.decision_id
		WHERE source.runtime_run_id=$2`,
		authority.projectionDeliverySourcesCTE(),
		authority.table("runtime_execution_projection_backlog"),
	), ProjectionDelivered, runtimeRunID.String()).Scan(&sourceCount, &deliveredCount); err != nil {
		return RuntimeProjectionBacklog{}, newProjectionError(ProjectionUnavailable)
	}
	return RuntimeProjectionBacklog{
		SourceFactCount: sourceCount,
		Delivered:       deliveredCount,
		Pending:         sourceCount - deliveredCount,
		Evidence:        make([]ProjectionDeliveryEvidence, 0, len(sources)),
	}.withEvidence(sources), nil
}

func (backlog RuntimeProjectionBacklog) withEvidence(sources []projectionDeliverySource) RuntimeProjectionBacklog {
	for _, source := range sources {
		backlog.Evidence = append(backlog.Evidence, source.evidence)
	}
	return backlog
}

func (authority *PostgresAuthority) beginProjectionDeliveryAttempt(
	ctx context.Context,
	source projectionDeliverySource,
) error {
	now := postgresTimestamp(authority.now())
	result, err := authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		attempt_count=attempt_count+1,
		first_attempt_at=COALESCE(first_attempt_at,$1),
		last_attempt_at=$1
		WHERE fact_id=$2 AND audit_fact_id=$3 AND audit_canonical_digest=$4
		AND fact_revision=$5 AND projection_schema_version=$6`,
		authority.table("runtime_execution_projection_backlog")),
		now, source.evidence.DecisionID.String(), source.evidence.AuditFactID,
		source.evidence.CanonicalDigest[:], source.fact.RuntimeRevision, SchemaV1)
	if err != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return newProjectionError(ProjectionInvalidFact)
	}
	return nil
}

func (authority *PostgresAuthority) finishProjectionDeliveryAttempt(
	ctx context.Context,
	decisionID RuntimeDecisionID,
	auditFactID string,
	digest Digest,
	runtimeRevision RuntimeRevision,
	auditDelivered bool,
	telemetryDelivered bool,
) error {
	now := postgresTimestamp(authority.now())
	auditStatus := ProjectionFailed
	telemetryStatus := ProjectionFailed
	if auditDelivered {
		auditStatus = ProjectionDelivered
	}
	if telemetryDelivered {
		telemetryStatus = ProjectionDelivered
	}
	degraded := !auditDelivered || !telemetryDelivered
	deliveredAtSetter := "delivered_at=delivered_at"
	if auditDelivered && telemetryDelivered {
		deliveredAtSetter = "delivered_at=COALESCE(delivered_at,$1)"
	}
	result, err := authority.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		audit_delivery_status=$2, telemetry_delivery_status=$3,
		last_safe_failure=$4, degraded=$5,
		last_attempt_at=$1, %s
		WHERE fact_id=$6 AND audit_fact_id=$7 AND audit_canonical_digest=$8
		AND fact_revision=$9 AND projection_schema_version=$10`,
		authority.table("runtime_execution_projection_backlog"), deliveredAtSetter),
		now, auditStatus, telemetryStatus, safeFailureForDelivery(auditDelivered, telemetryDelivered),
		degraded, decisionID.String(), auditFactID, digest[:], runtimeRevision, SchemaV1)
	if err != nil {
		return newProjectionError(ProjectionUnavailable)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return newProjectionError(ProjectionInvalidFact)
	}
	return nil
}

func safeFailureForDelivery(auditDelivered, telemetryDelivered bool) ProjectionSafeFailure {
	if auditDelivered && telemetryDelivered {
		return ProjectionFailureNone
	}
	return ProjectionFailureUnavailable
}

// digestFromBytes constructs a Digest from a raw byte slice. The caller must
// verify the length before trusting the value.
func digestFromBytes(value []byte) Digest {
	var digest Digest
	copy(digest[:], value)
	return digest
}
