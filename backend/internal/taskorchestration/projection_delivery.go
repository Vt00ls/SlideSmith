package taskorchestration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type ProjectionDeliveryOutcome uint8

const (
	ProjectionDeliveryPending ProjectionDeliveryOutcome = iota + 1
	ProjectionDeliveryDelivered
	ProjectionDeliveryDeferred
)

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

// DecisionProjectionBacklog is a bounded, content-free inspection of delivery
// evidence rebuilt from retained authoritative audit facts.
type DecisionProjectionBacklog struct {
	Pending            uint64
	Delivered          uint64
	SourceFactCount    uint64
	Evidence           []ProjectionDeliveryEvidence
	AccessAuditFactRef DiagnosticAuditFactRef
}

type ProjectionDeliveryEvidence struct {
	AuditFactID                   AuditFactID
	CanonicalDigest               ProjectionDigest
	AttemptCount                  uint64
	ExternalAuditDelivered        bool
	TelemetryDelivered            bool
	LastOutcome                   ProjectionDeliveryOutcome
	LastAttemptAt                 time.Time
	FirstExternalAuditDeliveredAt time.Time
	LastExternalAuditDeliveredAt  time.Time
}

type projectionDeliverySource struct {
	externalAudit ExternalAuditProjection
	telemetry     DecisionTelemetryProjection
	hasTelemetry  bool
	evidence      ProjectionDeliveryEvidence
}

func (adapter *PostgresAdapter) InspectDecisionProjectionBacklog(
	ctx context.Context,
	request ProjectionDeliveryInspectionRequest,
) (DecisionProjectionBacklog, error) {
	if adapter == nil || adapter.db == nil || ctx == nil || ctx.Err() != nil {
		return DecisionProjectionBacklog{}, &ProjectionError{code: ProjectionUnavailable}
	}
	if !request.authority.valid() || !validOpaqueID(request.taskID.value) {
		return DecisionProjectionBacklog{}, newError(ErrorAuthorizationDenied)
	}
	if request.limit == 0 || request.limit > 100 {
		return DecisionProjectionBacklog{}, &ProjectionError{code: ProjectionInvalidConfiguration}
	}
	backlog, err := adapter.inspectDecisionProjectionBacklog(
		ctx, request.taskID, request.limit,
	)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	auditRef, err := adapter.recordProjectionDeliveryAccessAudit(
		ctx, request.authority, request.taskID, request.limit,
		DiagnosticLookupProjectionBacklogInspection,
	)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	backlog.AccessAuditFactRef = auditRef
	return backlog, nil
}

// RebuildDecisionProjectionDelivery retries only already committed projection
// facts. It cannot create, advance, or repair Task state.
func (adapter *PostgresAdapter) RebuildDecisionProjectionDelivery(
	ctx context.Context,
	request ProjectionDeliveryRebuildRequest,
) (DecisionProjectionBacklog, error) {
	if adapter == nil || adapter.db == nil || adapter.projectionDelivery == nil ||
		ctx == nil || ctx.Err() != nil || request.limit == 0 || request.limit > 100 {
		return DecisionProjectionBacklog{}, &ProjectionError{code: ProjectionInvalidConfiguration}
	}
	if !request.authority.valid() || !validOpaqueID(request.taskID.value) {
		return DecisionProjectionBacklog{}, newError(ErrorAuthorizationDenied)
	}
	auditRef, err := adapter.recordProjectionDeliveryAccessAudit(
		ctx, request.authority, request.taskID, request.limit,
		DiagnosticLookupProjectionBacklogRebuild,
	)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	sources, err := adapter.loadDecisionProjectionSources(
		ctx, request.taskID, request.limit, true,
	)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	var attempted uint32
	for _, source := range sources {
		if attempted >= request.limit {
			break
		}
		if source.evidence.ExternalAuditDelivered && source.evidence.TelemetryDelivered {
			continue
		}
		if err := adapter.beginProjectionDeliveryAttempt(ctx, source); err != nil {
			return DecisionProjectionBacklog{}, err
		}
		auditDeliveredThisAttempt := false
		telemetryDeliveredThisAttempt := false
		if !source.evidence.ExternalAuditDelivered {
			auditDeliveredThisAttempt = adapter.projectionDelivery.externalAudit.ProjectExternalAudit(
				ctx, source.externalAudit,
			) == nil
		}
		if source.hasTelemetry && !source.evidence.TelemetryDelivered {
			telemetryDeliveredThisAttempt = adapter.projectionDelivery.telemetry.ProjectTelemetry(
				ctx, source.telemetry,
			) == nil
		}
		if err := adapter.finishProjectionDeliveryAttempt(
			ctx, source.evidence.AuditFactID, source.evidence.CanonicalDigest,
			auditDeliveredThisAttempt, telemetryDeliveredThisAttempt,
		); err != nil {
			return DecisionProjectionBacklog{}, err
		}
		attempted++
	}
	backlog, err := adapter.inspectDecisionProjectionBacklog(
		ctx, request.taskID, request.limit,
	)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	backlog.AccessAuditFactRef = auditRef
	return backlog, nil
}

func (adapter *PostgresAdapter) loadDecisionProjectionSources(
	ctx context.Context,
	taskID TaskID,
	limit uint32,
	pendingOnly bool,
) ([]projectionDeliverySource, error) {
	pendingPredicate := ""
	if pendingOnly {
		pendingPredicate = fmt.Sprintf(` AND (delivery.audit_fact_id IS NULL OR
			NOT delivery.external_audit_delivered OR
			NOT COALESCE(delivery.telemetry_delivered,
				source.fact_kind=%d))`, ExternalAuditDiagnosticAccessFact)
	}
	rows, err := adapter.db.QueryContext(ctx, fmt.Sprintf(`%s
		SELECT source.fact_kind, source.audit_fact_id, source.task_id,
		source.decision_state, source.source_digest, source.lookup_kind,
		source.decision_id, source.operation_id, source.result_limit,
		source.authority_id, source.authority_generation, source.reason,
		source.diagnostic_outcome, source.recorded_at, delivery.canonical_digest,
		COALESCE(delivery.external_audit_delivered, FALSE),
		COALESCE(delivery.telemetry_delivered, source.fact_kind=%d),
		COALESCE(delivery.attempt_count, 0),
		COALESCE(delivery.last_outcome, $1), delivery.last_attempt_at,
		delivery.first_external_audit_delivered_at,
		delivery.last_external_audit_delivered_at
		FROM projection_sources AS source
		LEFT JOIN %s AS delivery ON delivery.audit_fact_id=source.audit_fact_id
		WHERE source.task_id=$2%s
		ORDER BY source.audit_fact_id
		LIMIT $3`,
		adapter.projectionDeliverySourcesCTE(),
		ExternalAuditDiagnosticAccessFact,
		adapter.table("task_orchestration_projection_delivery"),
		pendingPredicate,
	), ProjectionDeliveryPending, taskID.value, limit)
	if err != nil {
		return nil, &ProjectionError{code: ProjectionUnavailable}
	}
	defer rows.Close()
	sources := make([]projectionDeliverySource, 0)
	for rows.Next() {
		var factKind ExternalAuditFactKind
		var auditFactID string
		var sourceTaskID string
		var encodedDecision []byte
		var sourceDigest []byte
		var lookupKind DiagnosticLookupKind
		var decisionID string
		var operationID string
		var resultLimit uint32
		var authorityID string
		var authorityGeneration AuthorizationGeneration
		var reason DiagnosticReason
		var diagnosticOutcome DiagnosticAuditOutcome
		var recordedAt time.Time
		var storedDigest []byte
		var evidence ProjectionDeliveryEvidence
		var lastAttempt sql.NullTime
		var firstExternalAuditDeliveredAt sql.NullTime
		var lastExternalAuditDeliveredAt sql.NullTime
		if err := rows.Scan(
			&factKind, &auditFactID, &sourceTaskID, &encodedDecision, &sourceDigest,
			&lookupKind, &decisionID, &operationID, &resultLimit, &authorityID,
			&authorityGeneration, &reason, &diagnosticOutcome, &recordedAt, &storedDigest,
			&evidence.ExternalAuditDelivered, &evidence.TelemetryDelivered,
			&evidence.AttemptCount, &evidence.LastOutcome, &lastAttempt,
			&firstExternalAuditDeliveredAt, &lastExternalAuditDeliveredAt,
		); err != nil {
			return nil, &ProjectionError{code: ProjectionUnavailable}
		}
		if sourceTaskID != taskID.value || !validOpaqueID(auditFactID) {
			return nil, &ProjectionError{code: ProjectionInvalidFact}
		}
		source := projectionDeliverySource{evidence: evidence}
		switch factKind {
		case ExternalAuditDecisionFact:
			var state postgresDecisionState
			if json.Unmarshal(encodedDecision, &state) != nil {
				return nil, &ProjectionError{code: ProjectionInvalidFact}
			}
			decision := state.decision()
			if !validPersistedDecision(decision) ||
				decision.TaskProjection.TaskID.value != sourceTaskID ||
				decision.MandatoryAuditFactRef.AuditFactID.value != auditFactID {
				return nil, &ProjectionError{code: ProjectionInvalidFact}
			}
			source.externalAudit, source.telemetry = decisionProjections(decision)
			source.hasTelemetry = true
		case ExternalAuditDiagnosticAccessFact:
			query := OperationalDiagnosticQuery{
				authority: AdministratorMetadataAuthority{
					id: AuthorityID{value: authorityID}, generation: authorityGeneration,
					reason: reason,
				},
				taskID: TaskID{value: sourceTaskID}, lookupKind: lookupKind,
				decisionID: DecisionID{value: decisionID}, operationID: OperationID{value: operationID},
				resultLimit: resultLimit,
			}
			if !validOperationalDiagnosticQuery(query) ||
				(diagnosticOutcome != DiagnosticAuditAccepted &&
					diagnosticOutcome != DiagnosticAuditDenied) || recordedAt.IsZero() {
				return nil, &ProjectionError{code: ProjectionInvalidFact}
			}
			source.externalAudit = diagnosticAuditProjection(
				AuditFactID{value: auditFactID}, query, diagnosticOutcome, recordedAt.UTC(),
			)
			if len(sourceDigest) != len(source.externalAudit.CanonicalDigest) ||
				!bytes.Equal(sourceDigest, source.externalAudit.CanonicalDigest[:]) {
				return nil, &ProjectionError{code: ProjectionInvalidFact}
			}
		default:
			return nil, &ProjectionError{code: ProjectionInvalidFact}
		}
		evidence.AuditFactID = AuditFactID{value: auditFactID}
		evidence.CanonicalDigest = source.externalAudit.CanonicalDigest
		if lastAttempt.Valid {
			evidence.LastAttemptAt = lastAttempt.Time.UTC()
		}
		if firstExternalAuditDeliveredAt.Valid {
			evidence.FirstExternalAuditDeliveredAt = firstExternalAuditDeliveredAt.Time.UTC()
		}
		if lastExternalAuditDeliveredAt.Valid {
			evidence.LastExternalAuditDeliveredAt = lastExternalAuditDeliveredAt.Time.UTC()
		}
		if len(storedDigest) != 0 && (len(storedDigest) != len(evidence.CanonicalDigest) ||
			!bytes.Equal(storedDigest, evidence.CanonicalDigest[:])) {
			return nil, &ProjectionError{code: ProjectionInvalidFact}
		}
		source.evidence = evidence
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, &ProjectionError{code: ProjectionUnavailable}
	}
	return sources, nil
}

func (adapter *PostgresAdapter) projectionDeliverySourcesCTE() string {
	return fmt.Sprintf(`WITH projection_sources AS (
		SELECT %d::smallint AS fact_kind, audit.audit_fact_id, audit.task_id,
			decision.decision_state, NULL::bytea AS source_digest,
			0::smallint AS lookup_kind, ''::text AS decision_id,
			''::text AS operation_id, 0::bigint AS result_limit,
			''::text AS authority_id, 0::bigint AS authority_generation,
			0::smallint AS reason, 0::smallint AS diagnostic_outcome,
			audit.committed_at AS recorded_at
		FROM %s AS audit
		JOIN %s AS decision ON decision.decision_id=audit.decision_id
		UNION ALL
		SELECT %d::smallint AS fact_kind, audit.audit_fact_id, audit.task_id,
			NULL::jsonb AS decision_state, audit.canonical_digest AS source_digest,
			audit.lookup_kind, audit.decision_id, audit.operation_id,
			audit.result_limit, audit.authority_id, audit.authority_generation,
			audit.reason, audit.outcome AS diagnostic_outcome, audit.recorded_at
		FROM %s AS audit
	)`, ExternalAuditDecisionFact,
		adapter.table("task_orchestration_mandatory_audit_facts"),
		adapter.table("task_orchestration_decisions"),
		ExternalAuditDiagnosticAccessFact,
		adapter.table("task_orchestration_diagnostic_audit_facts"))
}

func (adapter *PostgresAdapter) inspectDecisionProjectionBacklog(
	ctx context.Context,
	taskID TaskID,
	limit uint32,
) (DecisionProjectionBacklog, error) {
	sources, err := adapter.loadDecisionProjectionSources(ctx, taskID, limit, false)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	var sourceCount int64
	var deliveredCount int64
	if err := adapter.db.QueryRowContext(ctx, fmt.Sprintf(`%s
		SELECT
		COUNT(*), COUNT(*) FILTER (WHERE
			delivery.external_audit_delivered AND
			COALESCE(delivery.telemetry_delivered, source.fact_kind=%d))
		FROM projection_sources AS source
		LEFT JOIN %s AS delivery ON delivery.audit_fact_id=source.audit_fact_id
		WHERE source.task_id=$1`,
		adapter.projectionDeliverySourcesCTE(),
		ExternalAuditDiagnosticAccessFact,
		adapter.table("task_orchestration_projection_delivery"),
	), taskID.value).Scan(&sourceCount, &deliveredCount); err != nil {
		return DecisionProjectionBacklog{}, &ProjectionError{code: ProjectionUnavailable}
	}
	return projectionBacklogFromSources(
		sources, uint64(sourceCount), uint64(deliveredCount),
	), nil
}

func (adapter *PostgresAdapter) recordProjectionDeliveryAccessAudit(
	ctx context.Context,
	authority AdministratorMetadataAuthority,
	taskID TaskID,
	limit uint32,
	lookupKind DiagnosticLookupKind,
) (DiagnosticAuditFactRef, error) {
	if adapter.diagnosticAuditFaults.consume() {
		return DiagnosticAuditFactRef{}, newError(ErrorDependencyUnavailable)
	}
	tx, err := adapter.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return DiagnosticAuditFactRef{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	query := OperationalDiagnosticQuery{
		authority: authority, taskID: taskID, lookupKind: lookupKind, resultLimit: limit,
	}
	auditRef, err := adapter.recordDiagnosticAudit(
		ctx, tx, query, DiagnosticAuditAccepted,
	)
	if err != nil {
		return DiagnosticAuditFactRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return DiagnosticAuditFactRef{}, newError(ErrorDependencyUnavailable)
	}
	return auditRef, nil
}

func projectionBacklogFromSources(
	sources []projectionDeliverySource,
	sourceFactCount uint64,
	delivered uint64,
) DecisionProjectionBacklog {
	backlog := DecisionProjectionBacklog{
		SourceFactCount: sourceFactCount,
		Delivered:       delivered,
		Pending:         sourceFactCount - delivered,
		Evidence:        make([]ProjectionDeliveryEvidence, 0, len(sources)),
	}
	for _, source := range sources {
		backlog.Evidence = append(backlog.Evidence, source.evidence)
	}
	return backlog
}

func (adapter *PostgresAdapter) beginProjectionDeliveryAttempt(
	ctx context.Context,
	source projectionDeliverySource,
) error {
	result, err := adapter.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		audit_fact_id, canonical_digest, external_audit_delivered, telemetry_delivered,
		attempt_count, last_outcome, last_attempt_at
	) VALUES ($1,$2,$3,$4,1,$5,$6)
	ON CONFLICT (audit_fact_id) DO UPDATE SET
		attempt_count=%s.attempt_count+1,
		last_outcome=EXCLUDED.last_outcome,
		last_attempt_at=EXCLUDED.last_attempt_at
	WHERE %s.canonical_digest=EXCLUDED.canonical_digest`,
		adapter.table("task_orchestration_projection_delivery"),
		adapter.table("task_orchestration_projection_delivery"),
		adapter.table("task_orchestration_projection_delivery"),
	), source.evidence.AuditFactID.value, source.evidence.CanonicalDigest[:],
		source.evidence.ExternalAuditDelivered, source.evidence.TelemetryDelivered,
		ProjectionDeliveryPending, adapter.now().UTC())
	if err != nil {
		return &ProjectionError{code: ProjectionUnavailable}
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	return nil
}

func (adapter *PostgresAdapter) finishProjectionDeliveryAttempt(
	ctx context.Context,
	auditFactID AuditFactID,
	digest ProjectionDigest,
	externalAuditDelivered bool,
	telemetryDelivered bool,
) error {
	deliveredAt := adapter.now().UTC()
	result, err := adapter.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		external_audit_delivered=%s.external_audit_delivered OR $1,
		telemetry_delivered=%s.telemetry_delivered OR $2,
		first_external_audit_delivered_at=CASE WHEN $1 THEN
			CASE WHEN first_external_audit_delivered_at IS NULL THEN $3
			ELSE LEAST(first_external_audit_delivered_at, $3) END
			ELSE first_external_audit_delivered_at END,
		last_external_audit_delivered_at=CASE WHEN $1 THEN
			CASE WHEN last_external_audit_delivered_at IS NULL THEN $3
			ELSE GREATEST(last_external_audit_delivered_at, $3) END
			ELSE last_external_audit_delivered_at END,
		last_outcome=CASE WHEN
			(%s.external_audit_delivered OR $1) AND
			(%s.telemetry_delivered OR $2)
			THEN $4::smallint ELSE $5::smallint END
		WHERE audit_fact_id=$6 AND canonical_digest=$7`,
		adapter.table("task_orchestration_projection_delivery"),
		adapter.table("task_orchestration_projection_delivery"),
		adapter.table("task_orchestration_projection_delivery"),
		adapter.table("task_orchestration_projection_delivery"),
		adapter.table("task_orchestration_projection_delivery")),
		externalAuditDelivered, telemetryDelivered, deliveredAt,
		ProjectionDeliveryDelivered, ProjectionDeliveryDeferred,
		auditFactID.value, digest[:])
	if err != nil {
		return &ProjectionError{code: ProjectionUnavailable}
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	return nil
}
