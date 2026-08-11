package artifactpublication

// This file delivers child SPEC #107 (C05-04): the production-shaped real
// PostgreSQL owned persistence adapter for the Artifact Publication deep
// module, together with the restricted same-PostgreSQL Durable Object
// participant that attaches exact typed references during atomic
// activation.
//
// The adapter owns the request/operation journal, candidate, manifest,
// members, lineage, publication stream/head, verification evidence,
// mandatory audit and Task Orchestration outbox behind the same closed
// public seam (Mutate/Query). It never exposes SQL, a general repository,
// a raw callback ingest, or a caller-controlled transaction handle. The
// same invariant engine runs here and in the deterministic in-memory
// authority: canonical digests, the closed evidence matrix
// (evaluateEvidence), activation evidence construction, decision rendering
// (decisionForRecord) and query view rendering (versionView,
// artifactVersionView, memberRecordView) are all shared pure functions.
//
// Atomic activation is the single business linearization point: one real
// PostgreSQL transaction row-locks the publication stream and the original
// operation, revalidates the OperationID, request digest, expected stream
// revision/head, activity generation, publication fence and safety epoch,
// and all-or-none commits the immutable Artifact Version, members, lineage,
// typed Durable Object references (through the restricted participant),
// stream revision/current head, terminal operation, mandatory audit,
// activation evidence and outbox. No Durable Object call, network,
// filesystem or other remote I/O ever happens inside the transaction.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresConfig configures the real PostgreSQL owned persistence adapter.
// All authorities are registered explicitly so evidence doubles cannot
// self-verify. The capability and scope resolvers default to the adapter's
// own restricted tables (publication_do_capability for the Durable Object
// authority registry and publication_content_scope for the Identity &
// Ownership / Sharing availability facts); they can be overridden by a
// controlled double for deterministic fault tests.
type PostgresConfig struct {
	// Schema is the PostgreSQL schema that owns the publication tables.
	// It must be a plain lower-case identifier; "public" is the default.
	Schema string
	// Now returns the controlled diagnostic clock.
	Now func() Instant
	// Faults injects persistence faults at bounded points inside and
	// around the real transactions.
	Faults PostgresFaultHook
	// RuntimeAuthorityID registers the Runtime Execution authority.
	RuntimeAuthorityID AuthorityID
	// ValidationAuthorityID registers the Platform validation authority.
	ValidationAuthorityID AuthorityID
	// C04AuthorityID registers the Task Workspace Lifecycle authority.
	C04AuthorityID AuthorityID
	// DurableObjectAuthorityID registers the Durable Object authority.
	DurableObjectAuthorityID AuthorityID
	// TaskOrchestrationAuthorityID registers the Task Orchestration
	// authority that submits prepare/verify/reject/cancel/reconcile.
	TaskOrchestrationAuthorityID AuthorityID
	// RecoveryAuthorityID registers the protected recovery authority.
	RecoveryAuthorityID AuthorityID
	// CleanupAuthorityID registers the protected publication cleanup
	// authority that records C05-owned assembly obligations, requests
	// Durable Object residue release, resolves C05-owned Cleanup Debts and
	// reconciles release. It can never prepare/verify/activate/reject/
	// cancel a publication or mutate a candidate.
	CleanupAuthorityID AuthorityID
	// PublicationAuthorityID is C05's own authority identity bound by
	// every C04 reconstruction capability.
	PublicationAuthorityID AuthorityID
	// ResidueRetention computes the residue expiry instant from the residue
	// creation instant. When nil or zero, the residue has no expiry.
	ResidueRetention func(createdAt Instant) Instant
	// DurableObjectRelease is the restricted same-PostgreSQL Durable Object
	// participant that performs the physical release of the EXACT typed
	// staging references of one residue and returns an evidence-backed
	// receipt. When nil, the default participant validates every typed fact
	// against publication_do_capability, verifies no activated attach row
	// exists for the references, writes the release evidence row and marks
	// the capabilities released in the same transaction. A participant can
	// only release the exact typed references of one residue; it cannot
	// list objects, infer a publication from a path/prefix/bucket, or
	// perform remote I/O.
	DurableObjectRelease DurableObjectReleaseParticipant
	// CurrentContentCapability optionally overrides the current
	// verified-content capability resolution. When nil, the adapter reads
	// the restricted publication_do_capability registry owned by the
	// Durable Object authority.
	CurrentContentCapability func(ContentCapabilityID) (ContentCapabilityEvidence, bool)
	// CurrentContentScope optionally overrides the current availability
	// fact resolution. When nil, the adapter reads the restricted
	// publication_content_scope registry owned by Identity & Ownership /
	// Sharing.
	CurrentContentScope func(ContentScopeKey) (ContentScope, bool)
	// DurableObjectAttach is the restricted same-PostgreSQL Durable Object
	// participant that attaches exact typed references inside the
	// activation transaction. When nil, the default participant validates
	// every typed fact against publication_do_capability and inserts the
	// attach rows in the same transaction. A participant can only attach
	// the exact typed references of one candidate; it cannot list objects,
	// infer a publication from a path/prefix/bucket, or perform remote I/O.
	DurableObjectAttach DurableObjectAttachParticipant
	// ExternalAudit receives content-free copies of committed mandatory
	// audit facts strictly after the protected decision commits. Sink
	// failure never rolls back the committed decision; it produces a
	// durable, rebuildable delivery backlog (child SPEC #111).
	ExternalAudit ExternalAuditProjectionSink
	// Telemetry receives bounded content-free telemetry projections
	// strictly after the protected decision commits. Sink failure never
	// rolls back the committed decision; it produces a durable,
	// rebuildable delivery backlog.
	Telemetry TelemetrySink
	// TelemetryAdapter is the adapter-class dimension recorded in the
	// telemetry projections of this authority.
	TelemetryAdapter MetricAdapter
	// DiagnosticAuditFaults is the deterministic fail-closed seam proving
	// protected diagnostics are never returned without access audit.
	DiagnosticAuditFaults *DiagnosticAuditFaultController
}

// PostgresAuthority owns real PostgreSQL persistence behind the closed
// PublicationCore seam. Its implementation does not expose SQL, a general
// repository, or a caller-controlled transaction handle; the public surface
// remains Mutate(PublicationIntent) and Query(PublicationQuery) only.
type PostgresAuthority struct {
	db                    *sql.DB
	schema                string
	now                   func() Instant
	faults                PostgresFaultHook
	runtimeAuth           AuthorityID
	validationAuth        AuthorityID
	c04Auth               AuthorityID
	doAuth                AuthorityID
	toAuth                AuthorityID
	recoveryAuth          AuthorityID
	cleanupAuth           AuthorityID
	publicationAuth       AuthorityID
	residueRetention      func(createdAt Instant) Instant
	capabilityResolver    func(ContentCapabilityID) (ContentCapabilityEvidence, bool)
	scopeResolver         func(ContentScopeKey) (ContentScope, bool)
	attach                DurableObjectAttachParticipant
	release               DurableObjectReleaseParticipant
	externalAudit         ExternalAuditProjectionSink
	telemetry             TelemetrySink
	telemetryAdapter      MetricAdapter
	diagnosticAuditFaults *DiagnosticAuditFaultController
}

var _ PublicationCore = (*PostgresAuthority)(nil)

// NewPostgresAuthority constructs a real PostgreSQL owned persistence
// adapter over an existing *sql.DB. The schema must already exist (tests
// create one isolated schema per test through testpostgres); the owned
// tables are created idempotently by Migrate.
func NewPostgresAuthority(db *sql.DB, config PostgresConfig) (*PostgresAuthority, error) {
	if db == nil {
		return nil, &Error{Code: ErrorRetryableUnavailable}
	}
	schema := config.Schema
	if schema == "" {
		schema = "public"
	}
	if !validPostgresIdentifier(schema) {
		return nil, &Error{Code: ErrorInvalidIntent}
	}
	now := config.Now
	if now == nil {
		now = func() Instant { return Instant(time.Now().Unix()) }
	}
	authority := &PostgresAuthority{
		db: db, schema: schema, now: now, faults: config.Faults,
		runtimeAuth:           config.RuntimeAuthorityID,
		validationAuth:        config.ValidationAuthorityID,
		c04Auth:               config.C04AuthorityID,
		doAuth:                config.DurableObjectAuthorityID,
		toAuth:                config.TaskOrchestrationAuthorityID,
		recoveryAuth:          config.RecoveryAuthorityID,
		cleanupAuth:           config.CleanupAuthorityID,
		publicationAuth:       config.PublicationAuthorityID,
		residueRetention:      config.ResidueRetention,
		attach:                config.DurableObjectAttach,
		release:               config.DurableObjectRelease,
		externalAudit:         config.ExternalAudit,
		telemetry:             config.Telemetry,
		telemetryAdapter:      config.TelemetryAdapter,
		diagnosticAuditFaults: config.DiagnosticAuditFaults,
	}
	authority.capabilityResolver = config.CurrentContentCapability
	if authority.capabilityResolver == nil {
		authority.capabilityResolver = func(id ContentCapabilityID) (ContentCapabilityEvidence, bool) {
			return authority.currentContentCapabilityFromDB(id)
		}
	}
	authority.scopeResolver = config.CurrentContentScope
	if authority.scopeResolver == nil {
		authority.scopeResolver = func(key ContentScopeKey) (ContentScope, bool) {
			return authority.currentContentScopeFromDB(key)
		}
	}
	if authority.attach == nil {
		authority.attach = &postgresDurableObjectAttach{authority: authority}
	}
	if authority.release == nil {
		authority.release = &postgresDurableObjectRelease{authority: authority}
	}
	return authority, nil
}

// Migrate creates the owned publication tables, indexes and sequence in the
// configured schema. It is idempotent. The tables are the adapter's private
// persistence: no other package may read or write them, and no general
// repository is exposed.
func (p *PostgresAuthority) Migrate(ctx context.Context) error {
	ddl := p.schemaDDL()
	if _, err := p.db.ExecContext(ctx, ddl); err != nil {
		return normalizePersistenceError(err)
	}
	return nil
}

// validPostgresIdentifier reports whether value is a safe PostgreSQL schema
// identifier (lower-case letters, digits after the first character, and
// underscores). It is used to quote schema names in DDL/table references
// without injection risk; the identifier itself is never user content.
func validPostgresIdentifier(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character == '_' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

// q returns the quoted, validated schema-qualified identifier for one owned
// table. The schema was validated at construction; the table names are
// compile-time constants in this package.
func (p *PostgresAuthority) q(table string) string {
	return `"` + p.schema + `"."` + table + `"`
}

func (p *PostgresAuthority) schemaDDL() string {
	q := p.q
	ddl := fmt.Sprintf(`
CREATE SEQUENCE IF NOT EXISTS %[1]s;
CREATE TABLE IF NOT EXISTS %[2]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    revision BIGINT NOT NULL,
    current_head TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id)
);
CREATE TABLE IF NOT EXISTS %[3]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    state TEXT NOT NULL,
    stream_revision BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    fence BIGINT NOT NULL,
    safety_epoch BIGINT NOT NULL,
    activity_generation BIGINT NOT NULL,
    occurred_at BIGINT NOT NULL,
    reject_reason TEXT,
    cancel_reason TEXT,
    reconcile_mode TEXT,
    integrity_conflict BOOLEAN NOT NULL DEFAULT FALSE,
    activation_evidence JSONB,
    PRIMARY KEY (policy_domain_id, task_id, operation_id)
);
CREATE TABLE IF NOT EXISTS %[4]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    intent_kind TEXT NOT NULL,
    digest TEXT NOT NULL,
    state TEXT NOT NULL,
    decision JSONB NOT NULL,
    err_code TEXT,
    recorded_at BIGINT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, intent_kind)
);
CREATE TABLE IF NOT EXISTS %[5]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    version_id TEXT NOT NULL UNIQUE,
    schema_version BIGINT NOT NULL,
    kind TEXT NOT NULL,
    parent TEXT NOT NULL,
    contract_id TEXT NOT NULL,
    phase_run_id TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    lineage_digest TEXT NOT NULL,
    required_channels JSONB NOT NULL,
    validation_ref_evidence_id TEXT NOT NULL,
    validation_ref_digest TEXT NOT NULL,
    c04_ref_evidence_id TEXT NOT NULL,
    c04_ref_digest TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id)
);
CREATE TABLE IF NOT EXISTS %[6]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    slot TEXT NOT NULL,
    artifact_id TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL,
    logical_name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    content_digest TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, slot)
);
CREATE TABLE IF NOT EXISTS %[7]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    slot TEXT NOT NULL,
    content_id TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    size BIGINT NOT NULL,
    purpose TEXT NOT NULL,
    physical_generation BIGINT NOT NULL,
    adapter_id TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, slot)
);
CREATE TABLE IF NOT EXISTS %[8]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    evidence_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, channel)
);
CREATE TABLE IF NOT EXISTS %[9]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    member_slot TEXT NOT NULL,
    capability_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, member_slot)
);
CREATE TABLE IF NOT EXISTS %[10]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    state TEXT NOT NULL,
    failure_kind TEXT,
    failure_evidence_id TEXT,
    failure_capability_id TEXT,
    pending_slots JSONB,
    PRIMARY KEY (policy_domain_id, task_id, operation_id)
);
CREATE TABLE IF NOT EXISTS %[11]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    evidence_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, kind, evidence_id)
);
CREATE TABLE IF NOT EXISTS %[12]s (
    version_id TEXT PRIMARY KEY,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS %[13]s (
    artifact_id TEXT PRIMARY KEY,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS %[14]s (
    version_id TEXT PRIMARY KEY,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    schema_version BIGINT NOT NULL,
    kind TEXT NOT NULL,
    parent TEXT NOT NULL,
    contract_id TEXT NOT NULL,
    phase_run_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    stream_revision BIGINT NOT NULL,
    manifest_digest TEXT NOT NULL,
    lineage_digest TEXT NOT NULL,
    occurred_at BIGINT NOT NULL,
    evidence JSONB NOT NULL
);
CREATE TABLE IF NOT EXISTS %[15]s (
    version_id TEXT NOT NULL,
    slot TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    logical_name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    content_digest TEXT NOT NULL,
    PRIMARY KEY (version_id, slot)
);
CREATE TABLE IF NOT EXISTS %[16]s (
    version_id TEXT NOT NULL,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    slot TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    capability_id TEXT NOT NULL,
    content_id TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    size BIGINT NOT NULL,
    purpose TEXT NOT NULL,
    physical_generation BIGINT NOT NULL,
    verification_method TEXT NOT NULL,
    adapter_id TEXT NOT NULL,
    attached_at BIGINT NOT NULL,
    PRIMARY KEY (version_id, slot)
);
CREATE TABLE IF NOT EXISTS %[17]s (
    capability_id TEXT PRIMARY KEY,
    producer_authority_id TEXT NOT NULL,
    producer_generation BIGINT NOT NULL,
    policy_domain_id TEXT NOT NULL,
    purpose TEXT NOT NULL,
    content_id TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    size BIGINT NOT NULL,
    write_intent TEXT NOT NULL,
    physical_generation BIGINT NOT NULL,
    verification_method TEXT NOT NULL,
    adapter_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    fence BIGINT NOT NULL,
    safety_epoch BIGINT NOT NULL,
    digest TEXT NOT NULL,
    current BOOLEAN NOT NULL,
    released BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE IF NOT EXISTS %[18]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    artifact_version_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    availability_generation BIGINT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, artifact_version_id, scope_kind, scope_id)
);
CREATE TABLE IF NOT EXISTS %[19]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    generation BIGINT NOT NULL,
    fence BIGINT NOT NULL,
    release_intent TEXT NOT NULL,
    occurred_at BIGINT NOT NULL,
    expiry BIGINT NOT NULL DEFAULT 0,
    disposition TEXT NOT NULL DEFAULT 'pending',
    requires_reconciliation BOOLEAN NOT NULL DEFAULT FALSE,
    attempt_count BIGINT NOT NULL DEFAULT 0,
    consecutive_failures BIGINT NOT NULL DEFAULT 0,
    next_retry_at BIGINT NOT NULL DEFAULT 0,
    claim_generation BIGINT NOT NULL DEFAULT 0,
    claim_fence BIGINT NOT NULL DEFAULT 0,
    last_error_category TEXT NOT NULL DEFAULT '',
    release_receipt JSONB,
    assembly_ref TEXT NOT NULL DEFAULT '',
    assembly_digest TEXT NOT NULL DEFAULT '',
    assembly_generation BIGINT NOT NULL DEFAULT 0,
    assembly_fence BIGINT NOT NULL DEFAULT 0,
    debt_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (policy_domain_id, task_id, operation_id)
);
CREATE TABLE IF NOT EXISTS %[20]s (
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    slot TEXT NOT NULL,
    content_id TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    size BIGINT NOT NULL,
    purpose TEXT NOT NULL,
    physical_generation BIGINT NOT NULL,
    adapter_id TEXT NOT NULL,
    PRIMARY KEY (policy_domain_id, task_id, operation_id, slot)
);
CREATE TABLE IF NOT EXISTS %[21]s (
    audit_id BIGSERIAL PRIMARY KEY,
    occurred_at BIGINT NOT NULL,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    intent_kind TEXT NOT NULL,
    action TEXT NOT NULL,
    actor_kind TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_generation BIGINT NOT NULL,
    state TEXT NOT NULL,
    version_id TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    stream_revision BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS %[26]s (
    audit_fact_id TEXT PRIMARY KEY,
    audit_canonical_digest BYTEA NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    action TEXT NOT NULL,
    state TEXT NOT NULL,
    audit_delivery_status SMALLINT NOT NULL,
    telemetry_delivery_status SMALLINT NOT NULL,
    attempt_count BIGINT NOT NULL DEFAULT 0,
    degraded BOOLEAN NOT NULL DEFAULT FALSE,
    first_attempt_at TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS %[22]s (
    outbox_id BIGSERIAL PRIMARY KEY,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    kind TEXT NOT NULL,
    envelope JSONB NOT NULL,
    state TEXT NOT NULL,
    created_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS %[23]s (
    incident_id BIGSERIAL PRIMARY KEY,
    occurred_at BIGINT NOT NULL,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    subject_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS %[24]s (
    debt_id TEXT PRIMARY KEY,
    revision BIGINT NOT NULL,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    resource_ref TEXT NOT NULL,
    resource_digest TEXT NOT NULL,
    resource_generation BIGINT NOT NULL,
    resource_fence BIGINT NOT NULL,
    status TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    eligible_at BIGINT NOT NULL,
    first_attempt_at BIGINT NOT NULL DEFAULT 0,
    last_attempt_at BIGINT NOT NULL DEFAULT 0,
    next_retry_at BIGINT NOT NULL DEFAULT 0,
    attempt_count BIGINT NOT NULL DEFAULT 0,
    consecutive_failures BIGINT NOT NULL DEFAULT 0,
    claim_generation BIGINT NOT NULL DEFAULT 0,
    claim_fence BIGINT NOT NULL DEFAULT 0,
    retry_disposition TEXT NOT NULL DEFAULT '',
    last_error_category TEXT NOT NULL DEFAULT '',
    blocker_classes BIGINT NOT NULL DEFAULT 0,
    resolved_at BIGINT NOT NULL DEFAULT 0,
    resolution_class TEXT NOT NULL DEFAULT '',
    resolution_reason TEXT NOT NULL DEFAULT '',
    resolution_evidence JSONB,
    resolution_audit_fact_id TEXT NOT NULL DEFAULT '',
    resolution_approval_ref TEXT NOT NULL DEFAULT '',
    resolution_expires_at BIGINT NOT NULL DEFAULT 0,
    UNIQUE (policy_domain_id, task_id, operation_id)
);
CREATE TABLE IF NOT EXISTS %[25]s (
    receipt_id TEXT PRIMARY KEY,
    policy_domain_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    producer_authority_id TEXT NOT NULL,
    producer_generation BIGINT NOT NULL,
    outcome TEXT NOT NULL,
    blocker_classes BIGINT NOT NULL DEFAULT 0,
    expiry BIGINT NOT NULL DEFAULT 0,
    occurred_at BIGINT NOT NULL,
    digest TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activated_op ON %[14]s (policy_domain_id, task_id, stream_revision);
CREATE INDEX IF NOT EXISTS idx_outcome_kind ON %[4]s (policy_domain_id, task_id, operation_id, intent_kind);
CREATE INDEX IF NOT EXISTS idx_candidate_version ON %[5]s (version_id);
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS expiry BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS disposition TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS requires_reconciliation BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS attempt_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS consecutive_failures BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS next_retry_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS claim_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS claim_fence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS last_error_category TEXT NOT NULL DEFAULT '';
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS release_receipt JSONB;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS assembly_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS assembly_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS assembly_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS assembly_fence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[19]s ADD COLUMN IF NOT EXISTS debt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE %[17]s ADD COLUMN IF NOT EXISTS released BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS schema_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS integrity_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS owning_module BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS canonical_digest BYTEA NOT NULL DEFAULT '\x00'::bytea;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS request_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS result BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS lineage_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS recorded_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE %[21]s ADD COLUMN IF NOT EXISTS source_clock BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_audit_task ON %[21]s (task_id, audit_id);
`,
		q("publication_identity_seq"),
		q("publication_stream"),
		q("publication_operation"),
		q("publication_outcome"),
		q("publication_candidate"),
		q("publication_member"),
		q("publication_staging"),
		q("publication_runtime_ref"),
		q("publication_capability_ref"),
		q("publication_verification"),
		q("publication_evidence_accepted"),
		q("publication_version_fact"),
		q("publication_artifact_fact"),
		q("publication_activated"),
		q("publication_activated_member"),
		q("publication_attach"),
		q("publication_do_capability"),
		q("publication_content_scope"),
		q("publication_residue"),
		q("publication_residue_staging"),
		q("publication_audit"),
		q("publication_outbox"),
		q("publication_integrity_incident"),
		q("publication_cleanup_debt"),
		q("publication_do_release"),
		q("publication_projection_backlog"))
	return ddl
}

// normalizePersistenceError maps a raw persistence error into the closed,
// content-free safe-error surface. It never retains a PostgreSQL error
// chain, SQL text, table name, DSN, credential, locator, or host path.
func normalizePersistenceError(err error) *Error {
	if err == nil {
		return nil
	}
	var publicationError *Error
	if errors.As(err, &publicationError) && publicationError != nil && knownErrorCode(publicationError.Code) {
		return publicationError
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation: a durable identity or binding conflict
			return &Error{Code: ErrorIntegrityConflict}
		case "40001", "40P01": // serialization_failure, deadlock_detected
			return &Error{Code: ErrorRetryableUnavailable}
		case "57P01", "57P02", "57P03": // admin_shutdown, crash_shutdown, cannot_connect_now
			return &Error{Code: ErrorRetryableUnavailable}
		default:
			return &Error{Code: ErrorRetryableUnavailable}
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: ErrorRetryableUnavailable}
	}
	return &Error{Code: ErrorRetryableUnavailable}
}

// now returns the controlled diagnostic clock.
func (p *PostgresAuthority) nowValue() Instant {
	return p.now()
}

// nowTimeValue returns the controlled diagnostic clock as a time.Time.
func (p *PostgresAuthority) nowTimeValue() time.Time {
	return time.Unix(int64(p.nowValue()), 0).UTC()
}

// residueExpiry computes the residue expiry from its creation instant.
func (p *PostgresAuthority) residueExpiry(createdAt Instant) Instant {
	if p.residueRetention == nil {
		return 0
	}
	return p.residueRetention(createdAt)
}

// mintCleanupDebtID allocates the next opaque, non-reused C05-owned Cleanup
// Debt identity from the owned sequence.
func (p *PostgresAuthority) mintCleanupDebtID(ctx context.Context, tx *sql.Tx) (CleanupDebtID, error) {
	var next string
	if err := tx.QueryRowContext(ctx, `SELECT nextval('`+p.q("publication_identity_seq")+`')::text`).Scan(&next); err != nil {
		return "", err
	}
	return CleanupDebtID("publication-cleanup-debt-" + next), nil
}

// injectFault aborts the mutation exactly at the given bounded persistence
// point. A fault before commit rolls back the whole transaction; a fault
// after commit simulates response loss with the decision already durable.
func (p *PostgresAuthority) injectFault(point PostgresFaultPoint, operationID PublicationOperationID, kind PublicationIntentKind, subject string) error {
	if p.faults == nil {
		return nil
	}
	return p.faults(PostgresFaultEvent{
		Point: point, OperationID: operationID, IntentKind: kind, SubjectID: subject,
	})
}

// currentContentCapabilityFromDB reads the current verified-content
// capability fact from the restricted Durable Object registry.
func (p *PostgresAuthority) currentContentCapabilityFromDB(id ContentCapabilityID) (ContentCapabilityEvidence, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.currentContentCapabilityFrom(ctx, p.db, id)
}

func (p *PostgresAuthority) currentContentCapabilityFrom(ctx context.Context, querier sqlQuerier, id ContentCapabilityID) (ContentCapabilityEvidence, bool) {
	var row ContentCapabilityEvidence
	var producerID string
	var producerGeneration uint64
	err := querier.QueryRowContext(ctx, `SELECT capability_id, producer_authority_id, producer_generation, policy_domain_id, purpose,
		content_id, content_digest, size, write_intent, physical_generation, verification_method,
		adapter_id, generation, fence, safety_epoch, digest
		FROM `+p.q("publication_do_capability")+` WHERE capability_id = $1 AND current = TRUE`, string(id)).
		Scan(&row.ID, &producerID, &producerGeneration, &row.PolicyDomainID, &row.Purpose,
			&row.ContentID, &row.ContentDigest, &row.Size, &row.WriteIntent, &row.PhysicalGeneration,
			&row.VerificationMethod, &row.AdapterID, &row.Generation, &row.Fence, &row.SafetyEpoch, &row.Digest)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ContentCapabilityEvidence{}, false
		}
		return ContentCapabilityEvidence{}, false
	}
	row.Producer = EvidenceProducer{AuthorityID: AuthorityID(producerID), Generation: Generation(producerGeneration)}
	return row, true
}

func (p *PostgresAuthority) currentContentScopeFromDB(key ContentScopeKey) (ContentScope, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.currentContentScopeFrom(ctx, p.db, key)
}

func (p *PostgresAuthority) currentContentScopeFrom(ctx context.Context, querier sqlQuerier, key ContentScopeKey) (ContentScope, bool) {
	var scope ContentScope
	err := querier.QueryRowContext(ctx, `SELECT scope_kind, scope_id, availability_generation
		FROM `+p.q("publication_content_scope")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND artifact_version_id = $3 AND scope_kind = $4 AND scope_id = $5`,
		string(key.PolicyDomainID), string(key.TaskID), string(key.ArtifactVersionID), string(key.Kind), string(key.ID)).
		Scan(&scope.Kind, &scope.ID, &scope.AvailabilityGeneration)
	if err != nil {
		return ContentScope{}, false
	}
	return scope, true
}

// parseUint64 converts a numeric column string back into the typed
// Generation. The column is written as a decimal string of a uint64; the
// conversion cannot fail for values produced by this adapter.
func parseUint64(value string) uint64 {
	var parsed uint64
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0
		}
		parsed = parsed*10 + uint64(character-'0')
	}
	return parsed
}
