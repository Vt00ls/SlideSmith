package artifactpublication

// This file delivers child SPEC #111 (C05-07): post-commit projection
// delivery for the real PostgreSQL authority. After a protected decision
// commits (audit row included in the same transaction), the authority
// best-effort delivers the content-free external audit copy and the bounded
// telemetry projection of the retained authoritative fact. Sink failure
// never rolls back the committed decision; the durable
// publication_projection_backlog row keeps the pending/failed disposition
// and the protected rebuild surface redrives it from the retained audit
// facts. A crash between commit and delivery leaves the backlog pending;
// corrupt or unknown retained facts fail closed and are never projected as
// delivered or as zero.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// deliverCommittedProjection is invoked strictly after the protected
// decision transaction commits. It reconstructs the canonical audit fact
// from the retained audit row, records the first-wins backlog row, and
// best-effort delivers the external audit and telemetry projections.
// Failures are recorded in the durable backlog and never roll back.
func (p *PostgresAuthority) deliverCommittedProjection(
	ctx context.Context,
	header PublicationIntentHeader,
	action string,
	state PublicationOperationState,
	versionID ArtifactVersionID,
	manifestDigest Digest,
	lineageDigest Digest,
	streamRevision StreamRevision,
) {
	if p.externalAudit == nil && p.telemetry == nil {
		return
	}
	fact, err := p.loadCommittedAuditFact(ctx, header, action, state, versionID, manifestDigest, lineageDigest, streamRevision)
	if err != nil {
		// The authoritative fact is already committed; a projection
		// reconstruction failure only means delivery is deferred to the
		// protected rebuild surface. It never rolls back the decision.
		return
	}
	if err := p.ensureProjectionBacklogRow(ctx, fact); err != nil {
		return
	}
	p.attemptProjectionDelivery(ctx, fact)
}

// loadCommittedAuditFact reconstructs the canonical content-free audit fact
// from the most recently committed audit row of the exact operation/action.
// Every field is read from the retained row and the canonical digest is
// verified against the retained digest; a mismatch means the retained fact
// is corrupt and fails closed (never projected as delivered or as zero).
func (p *PostgresAuthority) loadCommittedAuditFact(
	ctx context.Context,
	header PublicationIntentHeader,
	action string,
	state PublicationOperationState,
	versionID ArtifactVersionID,
	manifestDigest Digest,
	lineageDigest Digest,
	streamRevision StreamRevision,
) (PublicationAuditFact, error) {
	var auditID int64
	var retainedDigest []byte
	var requestID, requestDigest, actorKind, actorID string
	var actorGeneration, recordedAt, occurredAt int64
	var sourceClock, result uint8
	var rowState, rowVersion, rowManifest, rowLineage, rowPolicy, rowTask, rowOperation string
	var rowStreamRevision uint64
	err := p.db.QueryRowContext(ctx, `SELECT audit_id, canonical_digest, request_id, request_digest,
		actor_kind, actor_id, actor_generation, recorded_at, source_clock, result,
		occurred_at, policy_domain_id, task_id, operation_id, state, version_id,
		manifest_digest, lineage_digest, stream_revision
		FROM `+p.q("publication_audit")+`
		WHERE policy_domain_id=$1 AND task_id=$2 AND operation_id=$3 AND action=$4
		ORDER BY audit_id DESC LIMIT 1`,
		string(header.PolicyDomainID), string(header.TaskID), string(header.Operation.ID), action).Scan(
		&auditID, &retainedDigest, &requestID, &requestDigest,
		&actorKind, &actorID, &actorGeneration, &recordedAt, &sourceClock, &result,
		&occurredAt, &rowPolicy, &rowTask, &rowOperation, &rowState, &rowVersion,
		&rowManifest, &rowLineage, &rowStreamRevision)
	if err != nil {
		return PublicationAuditFact{}, err
	}
	fact := PublicationAuditFact{
		SchemaVersion:       AuditSchemaV1,
		IntegrityVersion:    AuditIntegrityV1,
		AuditFactID:         auditFactIDString(auditID),
		OwningModule:        AuditModuleArtifactPublication,
		PolicyDomainID:      PolicyDomainID(rowPolicy),
		TaskID:              TaskID(rowTask),
		OperationID:         PublicationOperationID(rowOperation),
		RequestID:           PublicationRequestID(requestID),
		RequestDigest:       Digest(requestDigest),
		Action:              PublicationIntentKind(action),
		Result:              AuditResult(result),
		AuthorityKind:       EvidenceAuthorityKind(actorKind),
		AuthorityID:         AuthorityID(actorID),
		AuthorityGeneration: Generation(actorGeneration),
		State:               PublicationOperationState(rowState),
		VersionID:           ArtifactVersionID(rowVersion),
		ManifestDigest:      Digest(rowManifest),
		LineageDigest:       Digest(rowLineage),
		StreamRevision:      StreamRevision(rowStreamRevision),
		OccurredAt:          Instant(occurredAt),
		RecordedAt:          time.Unix(recordedAt, 0).UTC(),
		SourceClock:         AuditSourceClock(sourceClock),
	}
	fact.CanonicalDigest = PublicationAuditFactDigest(fact)
	if len(retainedDigest) != 32 || fact.CanonicalDigest != auditDigestFromBytes(retainedDigest) ||
		!validPublicationAuditFactFields(fact) {
		return PublicationAuditFact{}, errProjectionCorruptFact
	}
	return fact, nil
}

var errProjectionCorruptFact = errors.New("retained audit fact is corrupt")

// ensureProjectionBacklogRow records the durable first-wins backlog row for
// one retained audit fact. Duplicate post-commit delivery never rewrites the
// source fact; the backlog row carries the delivery status only.
func (p *PostgresAuthority) ensureProjectionBacklogRow(ctx context.Context, fact PublicationAuditFact) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO `+p.q("publication_projection_backlog")+`
		(audit_fact_id, audit_canonical_digest, task_id, operation_id, action, state,
		 audit_delivery_status, telemetry_delivery_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (audit_fact_id) DO NOTHING`,
		fact.AuditFactID, fact.CanonicalDigest[:], string(fact.TaskID), string(fact.OperationID),
		string(fact.Action), string(fact.State), ProjectionPending, ProjectionPending)
	return err
}

// attemptProjectionDelivery best-effort delivers the external audit copy and
// the bounded telemetry projection and records the durable delivery status.
// Duplicate or out-of-order delivery is idempotent by AuditFactID and
// canonical digest; an unavailable sink keeps the backlog pending/failed.
func (p *PostgresAuthority) attemptProjectionDelivery(ctx context.Context, fact PublicationAuditFact) {
	auditDelivered := false
	telemetryDelivered := false
	if p.externalAudit != nil {
		auditDelivered = p.externalAudit.ProjectExternalAudit(ctx, auditProjectionFromFact(fact)) == nil
	}
	if p.telemetry != nil {
		adapter := p.telemetryAdapter
		if !validMetricAdapter(adapter) {
			adapter = MetricAdapterPostgres
		}
		telemetryDelivered = p.telemetry.ProjectTelemetry(ctx, telemetryProjectionFromAudit(fact, adapter)) == nil
	}
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
	if auditDelivered && telemetryDelivered {
		deliveredAtSetter = "delivered_at=COALESCE(delivered_at,$1)"
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE `+p.q("publication_projection_backlog")+` SET
		audit_delivery_status=$2, telemetry_delivery_status=$3, degraded=$4,
		attempt_count=attempt_count+1, last_attempt_at=$1,
		first_attempt_at=COALESCE(first_attempt_at,$1), `+deliveredAtSetter+`
		WHERE audit_fact_id=$5 AND audit_canonical_digest=$6`,
		p.nowTimeValue(), auditStatus, telemetryStatus, degraded, fact.AuditFactID, fact.CanonicalDigest[:])
}

// auditFactIDString formats the BIGSERIAL audit id as the opaque audit fact
// identity.
func auditFactIDString(auditID int64) string {
	return "audit-" + fmtInt64(auditID)
}

func fmtInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		buffer[index] = '-'
	}
	return string(buffer[index:])
}

var _ = sql.ErrNoRows
