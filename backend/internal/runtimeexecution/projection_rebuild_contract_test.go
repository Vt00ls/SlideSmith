package runtimeexecution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

// TestPostgresTelemetryProjectionIsBoundedAndCommittedAfterDecision proves
// telemetry is projected strictly after the authoritative transaction commit,
// uses bounded typed labels, and never rolls back the decision.
func TestPostgresTelemetryProjectionIsBoundedAndCommittedAfterDecision(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 31, 14, 0, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-telemetry-owner", 26)
	start := standardStart(t, now, owner, "postgres-telemetry")

	observedCommitted := false
	var telemetryProjection RuntimeTelemetryProjection
	var observedFact ProjectionFact
	sink := &recordingTelemetrySink{}
	projection := ProjectionDeliveryFunc(func(ctx context.Context, fact ProjectionFact) error {
		observedFact = fact
		var decisions int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+schema+`.runtime_execution_decisions
			WHERE decision_id=$1`, fact.DecisionID.String()).Scan(&decisions); err == nil && decisions == 1 {
			observedCommitted = true
		}
		return nil
	})
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now },
		ProjectionDelivery: projection, Telemetry: sink,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-telemetry"), owner, ReconciliationTransportAmbiguous,
	)
	committed, err := store.persistReconciliationFoundation(context.Background(), intent)
	if err != nil {
		t.Fatalf("commit with bounded telemetry: %v", err)
	}
	if !observedCommitted {
		t.Fatal("projection delivery ran before the authoritative transaction committed")
	}
	if sink.count() != 1 || len(sink.projections()) != 1 {
		t.Fatalf("telemetry delivery count = %d projections = %d", sink.count(), len(sink.projections()))
	}
	telemetryProjection = sink.projections()[0]
	if !validRuntimeTelemetryProjection(telemetryProjection) {
		t.Fatalf("telemetry projection escaped bounded validation: %+v", telemetryProjection)
	}
	if telemetryProjection.Operation != MetricOperationReconciliation ||
		telemetryProjection.State != MetricStateReconciling ||
		telemetryProjection.Outcome != MetricOutcomeNone ||
		telemetryProjection.RuntimeRunID != start.RuntimeRunID ||
		telemetryProjection.OperationID != intent.OperationID ||
		telemetryProjection.RuntimeRevision != committed.Snapshot.RuntimeRevision {
		t.Fatalf("telemetry projection lost committed facts: %+v", telemetryProjection)
	}
	sample := MetricSample{
		Name: MetricRunStateCount,
		Labels: MetricLabels{
			Operation: telemetryProjection.Operation, State: telemetryProjection.State,
			Category: telemetryProjection.Category,
		},
		Count: 1,
	}
	if !RegisteredMetricSample(sample) {
		t.Fatalf("committed telemetry escaped the closed registry: %+v", sample)
	}
	var auditStatus, telemetryStatus ProjectionDeliveryStatus
	var degraded bool
	if err := db.QueryRowContext(context.Background(), `SELECT audit_delivery_status,
		telemetry_delivery_status, degraded
		FROM `+schema+`.runtime_execution_projection_backlog WHERE fact_id=$1`,
		committed.Fact.DecisionID.String()).Scan(&auditStatus, &telemetryStatus, &degraded); err != nil {
		t.Fatal("inspect durable projection backlog")
	}
	if auditStatus != ProjectionDelivered || telemetryStatus != ProjectionDelivered || degraded {
		t.Fatalf("projection backlog statuses = audit:%v telemetry:%v degraded:%v",
			auditStatus, telemetryStatus, degraded)
	}
	if observedFact.AuditFactID == "" || observedFact.AuditCanonicalDigest == (Digest{}) ||
		observedFact.ProjectionSchemaVersion != SchemaV1 {
		t.Fatalf("projection omitted authoritative AuditFact identity: %+v", observedFact)
	}
}

// TestPostgresProjectionRebuildRedrivesFromRetainedFacts proves delivery
// failure keeps a durable backlog and a protected rebuild redrives from
// retained authoritative facts; an unknown/corrupt retained fact fails closed
// and is never projected as zero.
func TestPostgresProjectionRebuildRedrivesFromRetainedFacts(t *testing.T) {
	db, schema := testpostgres.Open(t, "runtime_execution_test")
	now := time.Date(2026, 7, 31, 14, 30, 0, 0, time.UTC)
	owner := mustTaskOrchestrationAuthority(t, "postgres-rebuild-owner", 27)
	start := standardStart(t, now, owner, "postgres-rebuild")
	administrator := mustAdministratorAuthority(t, "postgres-rebuild-admin", 28)

	failingProjection := ProjectionDeliveryFunc(func(context.Context, ProjectionFact) error {
		return errors.New("credential-canary at /private/session")
	})
	sink := &recordingTelemetrySink{fail: true}
	store, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now },
		ProjectionDelivery: failingProjection, Telemetry: sink,
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("prepare PostgreSQL authority schema: %v", err)
	}
	fixture := acceptedPostgresRuntimeFixture(start, owner, now)
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	intent := newReconciliationFoundationIntent(
		fixture, mustOperationID(t, "reconcile-rebuild"), owner, ReconciliationProjectionDelivery,
	)
	if _, err := store.persistReconciliationFoundation(context.Background(), intent); err != nil {
		t.Fatalf("projection failure rolled back committed Decision: %v", err)
	}

	before, err := store.InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(administrator, start.RuntimeRunID, 10))
	if err != nil || before.Pending != 1 || before.Delivered != 0 ||
		before.SourceFactCount != 1 || len(before.Evidence) != 1 ||
		before.Evidence[0].AttemptCount != 1 {
		t.Fatalf("inspect initial projection backlog: backlog=%+v err=%v", before, err)
	}
	if sink.count() != 1 || len(sink.projections()) != 1 {
		t.Fatalf("failed telemetry delivery was not attempted exactly once")
	}

	// Rebuild with a healthy sink redrives from retained facts.
	store2, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() time.Time { return now.Add(time.Second) },
		ProjectionDelivery: ProjectionDeliveryFunc(func(context.Context, ProjectionFact) error { return nil }),
		Telemetry:          &recordingTelemetrySink{},
	})
	if err != nil {
		t.Fatalf("new PostgreSQL authority for rebuild: %v", err)
	}
	rebuilt, err := store2.RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(administrator, start.RuntimeRunID, 10))
	if err != nil || rebuilt.Pending != 0 || rebuilt.Delivered != 1 ||
		rebuilt.SourceFactCount != 1 || len(rebuilt.Evidence) != 1 ||
		!rebuilt.Evidence[0].AuditDelivered || !rebuilt.Evidence[0].TelemetryDelivered ||
		rebuilt.Evidence[0].AttemptCount != 2 {
		t.Fatalf("rebuild projection delivery: backlog=%+v err=%v", rebuilt, err)
	}

	// Rebuild again is idempotent: no re-delivery of already delivered facts.
	again, err := store2.RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(administrator, start.RuntimeRunID, 10))
	if err != nil || again.Pending != 0 || again.Delivered != 1 {
		t.Fatalf("rebuild was not idempotent: backlog=%+v err=%v", again, err)
	}

	// A corrupt retained fact must fail closed, never project zero. The audit
	// table is append-only, so a corrupt fact is proven by inserting a fake
	// authoritative decision + audit pair whose audit_state is valid JSON but
	// fails the strict schema decoder.
	corruptStart := standardStart(t, now, owner, "postgres-rebuild-corrupt")
	corruptFixture := acceptedPostgresRuntimeFixture(corruptStart, owner, now)
	installPostgresRuntimeFixture(t, db, schema, corruptFixture, now)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_decisions (
		decision_id, runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state, committed_at
	) VALUES ($1,$2,$3,$4,$5,$6,'{}'::jsonb,$7)`,
		"runtime-decision-corrupt", corruptStart.RuntimeRunID.String(), corruptStart.OperationID.String(),
		corruptStart.CanonicalRequestDigest[:], corruptStart.ExpectedRuntimeRevision,
		corruptStart.ExpectedRuntimeRevision+1, now.UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO `+schema+`.runtime_execution_mandatory_audit (
		audit_fact_id, decision_id, runtime_run_id, operation_id, request_digest,
		schema_version, integrity_version, owning_module, canonical_digest,
		authority_kind, authority_id, authority_generation, action, result,
		before_revision, after_revision, occurred_at, recorded_at, source_clock_id, audit_state
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		"runtime-audit-corrupt", "runtime-decision-corrupt", corruptStart.RuntimeRunID.String(),
		corruptStart.OperationID.String(), corruptStart.CanonicalRequestDigest[:],
		SchemaV1, uint16(1), "runtime_execution", corruptStart.CanonicalRequestDigest[:],
		uint8(AuthorityTaskOrchestration), corruptStart.Authority.id.String(), corruptStart.Authority.generation,
		uint8(postgresAuditStartAccepted), uint8(postgresAuditAccepted),
		corruptStart.ExpectedRuntimeRevision, corruptStart.ExpectedRuntimeRevision+1,
		now.UTC(), now.UTC(), "platform_control_plane", `{"unknown_schema_field":"canary"}`); err != nil {
		t.Fatal(err)
	}
	_, err = store2.RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(administrator, corruptStart.RuntimeRunID, 10))
	var projectionFailure *ProjectionError
	if !errors.As(err, &projectionFailure) || projectionFailure.Code() != ProjectionInvalidFact {
		t.Fatalf("corrupt retained audit fact = %T %v, want closed projection failure", err, err)
	}
}

type recordingTelemetrySink struct {
	mu   chan struct{}
	fail bool
	seen []RuntimeTelemetryProjection
}

func (sink *recordingTelemetrySink) ProjectTelemetry(
	_ context.Context,
	projection RuntimeTelemetryProjection,
) error {
	sink.seen = append(sink.seen, projection)
	if sink.fail {
		return errors.New("content-canary at object-locator")
	}
	return nil
}

func (sink *recordingTelemetrySink) count() int { return len(sink.seen) }
func (sink *recordingTelemetrySink) projections() []RuntimeTelemetryProjection {
	return append([]RuntimeTelemetryProjection(nil), sink.seen...)
}
