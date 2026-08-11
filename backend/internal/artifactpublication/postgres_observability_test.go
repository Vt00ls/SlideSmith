package artifactpublication

// Child SPEC #111 (C05-07) real-PostgreSQL observability and projection
// contract: the canonical mandatory audit facts are persisted in the same
// transaction as each protected decision; external audit and telemetry
// projections are emitted strictly after commit into a durable
// publication_projection_backlog; sink failure never rolls back the
// committed decision and the protected rebuild surface redelivers from the
// retained facts. A corrupt or unknown retained audit fact fails closed and
// is never projected as delivered or as zero.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

// newPostgresObservableFixture builds a postgres fixture wired with the
// deterministic telemetry/audit sink so post-commit projections are
// observable through the protected surfaces.
func newPostgresObservableFixture(t *testing.T) (*postgresFixture, *DeterministicTelemetry) {
	t.Helper()
	db, schema := testpostgresOpen(t, "c05_07_publication")
	base := newFixture(t)
	telemetry := NewDeterministicTelemetry(DeterministicTelemetryConfig{
		Now: func() time.Time { return time.Unix(int64(base.now), 0).UTC() },
	})
	authority, err := NewPostgresAuthority(db, PostgresConfig{
		Schema: schema, Now: func() Instant { return base.now },
		RuntimeAuthorityID:           base.runtimeAuthority,
		ValidationAuthorityID:        base.validationAuthority,
		C04AuthorityID:               base.c04Authority,
		DurableObjectAuthorityID:     base.durableObjectAuthority,
		TaskOrchestrationAuthorityID: base.taskOrchestrationAuthority,
		RecoveryAuthorityID:          base.recoveryAuthority,
		CleanupAuthorityID:           base.cleanupAuthority,
		PublicationAuthorityID:       base.publicationAuthority,
		ExternalAudit:                telemetry,
		Telemetry:                    telemetry,
		TelemetryAdapter:             MetricAdapterPostgres,
		DiagnosticAuditFaults:        &DiagnosticAuditFaultController{},
	})
	if err != nil {
		t.Fatalf("new postgres authority: %v", err)
	}
	if err := authority.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate postgres authority: %v", err)
	}
	base.core = authority
	f := &postgresFixture{fixture: base, db: db, schema: schema, authority: authority}
	return f, telemetry
}

func testpostgresOpen(t *testing.T, prefix string) (*sql.DB, string) {
	t.Helper()
	return testpostgres.Open(t, prefix)
}

// TestPostgresCanonicalAuditFactsPersistedInTransaction proves every
// protected decision persists one canonical content-free audit fact with
// its digest in the same transaction, and a restart resumes the same facts.
func TestPostgresCanonicalAuditFactsPersistedInTransaction(t *testing.T) {
	f, _ := newPostgresObservableFixture(t)
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "pg-audit-1", set)
	f.mustVerify(t, "pg-audit-1", set)
	f.mustActivate(t, "pg-audit-1")

	if got := f.countRows(t, "publication_audit"); got != 3 {
		t.Fatalf("publication_audit rows = %d, want 3", got)
	}
	// The canonical digest is retained for every fact and the schema is
	// versioned.
	var badDigest int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM "`+f.schema+`"."publication_audit" WHERE canonical_digest = '\x0000000000000000000000000000000000000000000000000000000000000000'::bytea OR schema_version = 0`).Scan(&badDigest); err != nil || badDigest != 0 {
		t.Fatalf("audit rows with missing canonical digest or schema = %d err=%v, want 0", badDigest, err)
	}
	// A restart resumes the retained audit facts with their digests.
	f.rebuildAuthority(t)
	if got := f.countRows(t, "publication_audit"); got != 3 {
		t.Fatalf("audit rows after restart = %d, want 3", got)
	}
}

// TestPostgresProjectionBacklogRebuildProves the durable projection backlog
// over real PostgreSQL: post-commit projections are delivered through the
// protected inspection/rebuild surface, sink failure stays pending without
// rollback, and rebuild redelivers.
func TestPostgresProjectionBacklogRebuildProves(t *testing.T) {
	f, telemetry := newPostgresObservableFixture(t)
	authority := f.authority
	meta := NewAdministratorMetadataAuthority(f.publicationAuthority, 1, DiagnosticReasonOperations)

	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "pg-backlog", set)

	// The committed fact is delivered post-commit into the durable backlog.
	backlog, err := authority.InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(meta, f.taskID, 10))
	if err != nil {
		t.Fatalf("inspect backlog: %v", err)
	}
	if backlog.SourceFactCount != 1 || backlog.Delivered != 1 || backlog.Pending != 0 {
		t.Fatalf("expected the committed fact delivered: %#v", backlog)
	}
	if len(telemetry.audit) != 1 || len(telemetry.projections) != 1 {
		t.Fatalf("post-commit projections missing: audit=%d telemetry=%d", len(telemetry.audit), len(telemetry.projections))
	}

	// Simulate an outage: a fresh authority over the same schema with a
	// failing sink; a NEW decision commits and its projection stays pending.
	down := &failingSink{err: newProjectionError(ProjectionUnavailable)}
	// Rebuild the same fixture's authority over the same schema but with
	// the failing sink.
	rebuilt, err := NewPostgresAuthority(f.db, PostgresConfig{
		Schema: f.schema, Now: func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CleanupAuthorityID:           f.cleanupAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		ExternalAudit:                down,
		Telemetry:                    down,
		TelemetryAdapter:             MetricAdapterPostgres,
		DiagnosticAuditFaults:        &DiagnosticAuditFaultController{},
	})
	if err != nil {
		t.Fatalf("rebuild authority with failing sink: %v", err)
	}
	f.authority = rebuilt
	f.fixture.core = rebuilt

	// New decision under outage: the decision commits, delivery fails.
	set2 := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "pg-backlog-2", set2)
	backlog2, err := rebuilt.InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(meta, f.taskID, 10))
	if err != nil {
		t.Fatalf("inspect backlog under outage: %v", err)
	}
	if backlog2.SourceFactCount != 2 || backlog2.Delivered != 1 || backlog2.Pending != 1 {
		t.Fatalf("outage must keep the new fact pending without rollback: %#v", backlog2)
	}

	// Sink recovers: rebuild redelivers only the pending fact.
	recovered, err := NewPostgresAuthority(f.db, PostgresConfig{
		Schema: f.schema, Now: func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CleanupAuthorityID:           f.cleanupAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		ExternalAudit:                telemetry,
		Telemetry:                    telemetry,
		TelemetryAdapter:             MetricAdapterPostgres,
		DiagnosticAuditFaults:        &DiagnosticAuditFaultController{},
	})
	if err != nil {
		t.Fatalf("rebuild authority with recovered sink: %v", err)
	}
	f.authority = recovered
	f.fixture.core = recovered
	rebuiltBacklog, err := recovered.RebuildProjectionDelivery(context.Background(),
		NewProjectionDeliveryRebuildRequest(meta, f.taskID, 10))
	if err != nil {
		t.Fatalf("rebuild delivery: %v", err)
	}
	if rebuiltBacklog.Pending != 0 || rebuiltBacklog.Delivered != 2 {
		t.Fatalf("rebuild must deliver the pending fact: %#v", rebuiltBacklog)
	}
}

// TestPostgresCorruptRetainedAuditFailsClosedNotZero proves a corrupt
// retained audit fact fails closed on inspection: it is never projected as
// delivered, never projected as zero, and never silently repaired.
func TestPostgresCorruptRetainedAuditFailsClosedNotZero(t *testing.T) {
	f, _ := newPostgresObservableFixture(t)
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "pg-corrupt", set)

	// Corrupt the retained canonical digest of the committed audit fact.
	if _, err := f.db.ExecContext(context.Background(),
		`UPDATE "`+f.schema+`"."publication_audit" SET canonical_digest = '\x0101010101010101010101010101010101010101010101010101010101010101'::bytea`); err != nil {
		t.Fatalf("corrupt retained digest: %v", err)
	}
	meta := NewAdministratorMetadataAuthority(f.publicationAuthority, 1, DiagnosticReasonOperations)
	if _, err := f.authority.InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(meta, f.taskID, 10)); !isProjectionError(err, ProjectionInvalidFact) {
		t.Fatalf("corrupt retained fact must fail closed as invalid fact, got %v", err)
	}
	// The authoritative decision is untouched.
	operation, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: "pg-corrupt",
	})
	if err != nil || operation.State != OperationPrepared {
		t.Fatalf("corrupt projection must not change authority: %#v err=%v", operation, err)
	}
}

func isProjectionError(err error, code ProjectionErrorCode) bool {
	if err == nil {
		return false
	}
	var projectionError *ProjectionError
	if !errors.As(err, &projectionError) {
		return false
	}
	return projectionError.Code() == code
}
