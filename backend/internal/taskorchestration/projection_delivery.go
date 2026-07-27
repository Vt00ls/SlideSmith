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
	Limit uint32
}

// DecisionProjectionBacklog is a bounded, content-free inspection of delivery
// evidence rebuilt from retained mandatory audit and Decision facts.
type DecisionProjectionBacklog struct {
	Pending         uint64
	Delivered       uint64
	SourceFactCount uint64
	Evidence        []ProjectionDeliveryEvidence
}

type ProjectionDeliveryEvidence struct {
	AuditFactID            AuditFactID
	CanonicalDigest        ProjectionDigest
	AttemptCount           uint64
	ExternalAuditDelivered bool
	TelemetryDelivered     bool
	LastOutcome            ProjectionDeliveryOutcome
	LastAttemptAt          time.Time
}

type projectionDeliverySource struct {
	decision TransitionDecision
	evidence ProjectionDeliveryEvidence
	stored   bool
}

func (adapter *PostgresAdapter) InspectDecisionProjectionBacklog(
	ctx context.Context,
) (DecisionProjectionBacklog, error) {
	if adapter == nil || adapter.db == nil || ctx == nil || ctx.Err() != nil {
		return DecisionProjectionBacklog{}, &ProjectionError{code: ProjectionUnavailable}
	}
	sources, err := adapter.loadDecisionProjectionSources(ctx)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	return projectionBacklogFromSources(sources), nil
}

// RebuildDecisionProjectionDelivery retries only already committed projection
// facts. It cannot create, advance, or repair Task state.
func (adapter *PostgresAdapter) RebuildDecisionProjectionDelivery(
	ctx context.Context,
	request ProjectionDeliveryRebuildRequest,
) (DecisionProjectionBacklog, error) {
	if adapter == nil || adapter.db == nil || adapter.projectionDelivery == nil ||
		ctx == nil || ctx.Err() != nil || request.Limit == 0 || request.Limit > 100 {
		return DecisionProjectionBacklog{}, &ProjectionError{code: ProjectionInvalidConfiguration}
	}
	sources, err := adapter.loadDecisionProjectionSources(ctx)
	if err != nil {
		return DecisionProjectionBacklog{}, err
	}
	var attempted uint32
	for _, source := range sources {
		if attempted >= request.Limit {
			break
		}
		if source.evidence.ExternalAuditDelivered && source.evidence.TelemetryDelivered {
			continue
		}
		if err := adapter.beginDecisionProjectionAttempt(ctx, source); err != nil {
			return DecisionProjectionBacklog{}, err
		}
		auditDelivered, telemetryDelivered := adapter.projectionDelivery.projectCommittedDecision(
			ctx,
			source.decision,
			!source.evidence.ExternalAuditDelivered,
			!source.evidence.TelemetryDelivered,
		)
		if source.evidence.ExternalAuditDelivered {
			auditDelivered = true
		}
		if source.evidence.TelemetryDelivered {
			telemetryDelivered = true
		}
		if err := adapter.finishDecisionProjectionAttempt(
			ctx, source.evidence.AuditFactID, source.evidence.CanonicalDigest,
			auditDelivered, telemetryDelivered,
		); err != nil {
			return DecisionProjectionBacklog{}, err
		}
		attempted++
	}
	return adapter.InspectDecisionProjectionBacklog(ctx)
}

func (adapter *PostgresAdapter) loadDecisionProjectionSources(
	ctx context.Context,
) ([]projectionDeliverySource, error) {
	rows, err := adapter.db.QueryContext(ctx, fmt.Sprintf(`SELECT
		audit.audit_fact_id, decision.decision_state, delivery.canonical_digest,
		COALESCE(delivery.external_audit_delivered, FALSE),
		COALESCE(delivery.telemetry_delivered, FALSE),
		COALESCE(delivery.attempt_count, 0),
		COALESCE(delivery.last_outcome, $1), delivery.last_attempt_at
		FROM %s AS audit
		JOIN %s AS decision ON decision.decision_id=audit.decision_id
		LEFT JOIN %s AS delivery ON delivery.audit_fact_id=audit.audit_fact_id
		ORDER BY audit.audit_fact_id`,
		adapter.table("task_orchestration_mandatory_audit_facts"),
		adapter.table("task_orchestration_decisions"),
		adapter.table("task_orchestration_projection_delivery"),
	), ProjectionDeliveryPending)
	if err != nil {
		return nil, &ProjectionError{code: ProjectionUnavailable}
	}
	defer rows.Close()
	sources := make([]projectionDeliverySource, 0)
	for rows.Next() {
		var auditFactID string
		var encodedDecision []byte
		var storedDigest []byte
		var evidence ProjectionDeliveryEvidence
		var lastAttempt sql.NullTime
		if err := rows.Scan(
			&auditFactID, &encodedDecision, &storedDigest,
			&evidence.ExternalAuditDelivered, &evidence.TelemetryDelivered,
			&evidence.AttemptCount, &evidence.LastOutcome, &lastAttempt,
		); err != nil {
			return nil, &ProjectionError{code: ProjectionUnavailable}
		}
		var state postgresDecisionState
		if json.Unmarshal(encodedDecision, &state) != nil {
			return nil, &ProjectionError{code: ProjectionInvalidFact}
		}
		decision := state.decision()
		if !validPersistedDecision(decision) ||
			decision.MandatoryAuditFactRef.AuditFactID.value != auditFactID {
			return nil, &ProjectionError{code: ProjectionInvalidFact}
		}
		auditProjection, _ := decisionProjections(decision)
		evidence.AuditFactID = AuditFactID{value: auditFactID}
		evidence.CanonicalDigest = auditProjection.CanonicalDigest
		if lastAttempt.Valid {
			evidence.LastAttemptAt = lastAttempt.Time.UTC()
		}
		stored := len(storedDigest) != 0
		if stored && (len(storedDigest) != len(evidence.CanonicalDigest) ||
			!bytes.Equal(storedDigest, evidence.CanonicalDigest[:])) {
			return nil, &ProjectionError{code: ProjectionInvalidFact}
		}
		sources = append(sources, projectionDeliverySource{
			decision: decision, evidence: evidence, stored: stored,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, &ProjectionError{code: ProjectionUnavailable}
	}
	return sources, nil
}

func projectionBacklogFromSources(sources []projectionDeliverySource) DecisionProjectionBacklog {
	backlog := DecisionProjectionBacklog{
		SourceFactCount: uint64(len(sources)),
		Evidence:        make([]ProjectionDeliveryEvidence, 0, len(sources)),
	}
	for _, source := range sources {
		if source.evidence.ExternalAuditDelivered && source.evidence.TelemetryDelivered {
			backlog.Delivered++
		} else {
			backlog.Pending++
		}
		backlog.Evidence = append(backlog.Evidence, source.evidence)
	}
	return backlog
}

func (adapter *PostgresAdapter) beginDecisionProjectionAttempt(
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

func (adapter *PostgresAdapter) finishDecisionProjectionAttempt(
	ctx context.Context,
	auditFactID AuditFactID,
	digest ProjectionDigest,
	externalAuditDelivered bool,
	telemetryDelivered bool,
) error {
	outcome := ProjectionDeliveryDeferred
	if externalAuditDelivered && telemetryDelivered {
		outcome = ProjectionDeliveryDelivered
	}
	result, err := adapter.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		external_audit_delivered=$1, telemetry_delivered=$2, last_outcome=$3
		WHERE audit_fact_id=$4 AND canonical_digest=$5`,
		adapter.table("task_orchestration_projection_delivery")),
		externalAuditDelivered, telemetryDelivered, outcome, auditFactID.value, digest[:])
	if err != nil {
		return &ProjectionError{code: ProjectionUnavailable}
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return &ProjectionError{code: ProjectionInvalidFact}
	}
	return nil
}
