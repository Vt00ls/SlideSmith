package runtimeexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type PersistenceErrorCode uint8

const (
	PersistenceInvalidConfiguration PersistenceErrorCode = iota + 1
	PersistenceUnavailable
	PersistenceAuthorizationDenied
	PersistenceIntegrityConflict
	PersistenceStateCorrupt
)

// PersistenceError is a closed, content-free error. It never retains a
// PostgreSQL error, SQL text, table, DSN, credential, locator, or host path.
type PersistenceError struct {
	code PersistenceErrorCode
}

func (failure *PersistenceError) Error() string {
	if failure == nil {
		return "runtime execution persistence is unavailable"
	}
	switch failure.code {
	case PersistenceInvalidConfiguration:
		return "runtime execution persistence configuration is invalid"
	case PersistenceAuthorizationDenied:
		return "runtime execution persistence authority is denied"
	case PersistenceIntegrityConflict:
		return "runtime execution persistence binding conflicts with retained authority"
	case PersistenceStateCorrupt:
		return "runtime execution persistence state is invalid"
	default:
		return "runtime execution persistence is unavailable"
	}
}

func (failure *PersistenceError) Code() PersistenceErrorCode {
	if failure == nil {
		return PersistenceUnavailable
	}
	return failure.code
}

func (failure *PersistenceError) RetryDisposition() RetryDisposition {
	if failure != nil && failure.code == PersistenceUnavailable {
		return RetryAfterDependency
	}
	return RetryNever
}

func newPersistenceError(code PersistenceErrorCode) *PersistenceError {
	return &PersistenceError{code: code}
}

type PostgresConfig struct {
	Schema             string
	Now                func() time.Time
	Faults             PersistenceFaultInjector
	ProjectionDelivery ProjectionDelivery
}

// PostgresAuthority owns C03 persistence behind the RuntimeExecution seam.
// Its implementation does not expose SQL, a general repository, or a
// caller-controlled transaction handle.
type PostgresAuthority struct {
	db         *sql.DB
	schema     string
	now        func() time.Time
	faults     PersistenceFaultInjector
	projection ProjectionDelivery
}

var _ RuntimeExecution = (*PostgresAuthority)(nil)

func NewPostgresAuthority(db *sql.DB, config PostgresConfig) (*PostgresAuthority, error) {
	if db == nil {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	schema := config.Schema
	if schema == "" {
		schema = "public"
	}
	if !validPostgresIdentifier(schema) {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &PostgresAuthority{
		db: db, schema: schema, now: now, faults: config.Faults, projection: config.ProjectionDelivery,
	}, nil
}

func validPostgresIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 63 {
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

func (authority *PostgresAuthority) table(name string) string {
	return authority.schema + "." + name
}

// Migrate installs the owned persistence foundation in an already selected
// schema. Production rollout and cutover remain outside this module.
func (authority *PostgresAuthority) Migrate(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	var version int
	if err := authority.db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version < 120000 {
		return newPersistenceError(PersistenceUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, nil)
	if err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(740074)"); err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	for _, statement := range authority.migrationStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return newPersistenceError(PersistenceUnavailable)
		}
	}
	if err := tx.Commit(); err != nil {
		return newPersistenceError(PersistenceUnavailable)
	}
	return nil
}

func (authority *PostgresAuthority) migrationStatements() []string {
	runtimes := authority.table("runtime_execution_runtimes")
	decisions := authority.table("runtime_execution_decisions")
	requests := authority.table("runtime_execution_requests")
	revisions := authority.table("runtime_execution_revisions")
	audit := authority.table("runtime_execution_mandatory_audit")
	outbox := authority.table("runtime_execution_outbox")
	delivery := authority.table("runtime_execution_outbox_delivery")
	reconciliation := authority.table("runtime_execution_reconciliation_obligations")
	projection := authority.table("runtime_execution_projection_backlog")
	leaseRoots := authority.table("runtime_execution_lease_roots")
	evidenceRoots := authority.table("runtime_execution_evidence_roots")
	cleanup := authority.table("runtime_execution_cleanup_obligations")
	heartbeats := authority.table("runtime_execution_heartbeat_history")
	compaction := authority.table("runtime_execution_heartbeat_compaction")
	immutableFunction := authority.table("runtime_execution_reject_immutable_mutation")
	return []string{
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", authority.table("runtime_execution_decision_sequence")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			runtime_run_id text PRIMARY KEY,
			personal_workspace_id text NOT NULL,
			task_id text NOT NULL,
			phase_run_id text NOT NULL,
			owner_authority_id text NOT NULL,
			owner_authority_generation bigint NOT NULL CHECK (owner_authority_generation > 0),
			owner_authority_kind smallint NOT NULL,
			task_revision bigint NOT NULL CHECK (task_revision > 0),
			runtime_revision bigint NOT NULL CHECK (runtime_revision > 0),
			operation_generation bigint NOT NULL CHECK (operation_generation > 0),
			runtime_fence bigint NOT NULL CHECK (runtime_fence > 0),
			safety_epoch bigint NOT NULL CHECK (safety_epoch > 0),
			runtime_state smallint NOT NULL,
			runtime_outcome smallint NOT NULL,
			terminal_evidence_id text NOT NULL DEFAULT '',
			aggregate_state jsonb NOT NULL,
			updated_at timestamptz NOT NULL
		)`, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			decision_id text PRIMARY KEY,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			operation_id text NOT NULL,
			canonical_request_digest bytea NOT NULL CHECK (octet_length(canonical_request_digest) = 32),
			previous_runtime_revision bigint NOT NULL CHECK (previous_runtime_revision > 0),
			resulting_runtime_revision bigint NOT NULL CHECK (resulting_runtime_revision > 0),
			decision_state jsonb NOT NULL,
			committed_at timestamptz NOT NULL,
			UNIQUE (runtime_run_id, operation_id)
		)`, decisions, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			personal_workspace_id text NOT NULL,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			command_kind smallint NOT NULL,
			operation_id text NOT NULL,
			canonical_request_digest bytea NOT NULL CHECK (octet_length(canonical_request_digest) = 32),
			canonical_request bytea NOT NULL,
			decision_id text NOT NULL REFERENCES %s(decision_id),
			admission_grant_id text NOT NULL DEFAULT '',
			admission_grant_generation bigint NOT NULL DEFAULT 0 CHECK (admission_grant_generation >= 0),
			PRIMARY KEY (runtime_run_id, command_kind, operation_id)
		)`, requests, runtimes, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			runtime_revision bigint NOT NULL CHECK (runtime_revision > 0),
			decision_id text NOT NULL REFERENCES %s(decision_id),
			aggregate_state jsonb NOT NULL,
			PRIMARY KEY (runtime_run_id, runtime_revision),
			UNIQUE (decision_id)
		)`, revisions, runtimes, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			audit_fact_id text PRIMARY KEY,
			decision_id text NOT NULL UNIQUE REFERENCES %s(decision_id),
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			operation_id text NOT NULL,
			request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
			canonical_digest bytea NOT NULL CHECK (octet_length(canonical_digest) = 32),
			authority_kind smallint NOT NULL,
			authority_id text NOT NULL,
			authority_generation bigint NOT NULL CHECK (authority_generation > 0),
			before_revision bigint NOT NULL CHECK (before_revision > 0),
			after_revision bigint NOT NULL CHECK (after_revision > 0),
			recorded_at timestamptz NOT NULL
		)`, audit, decisions, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			operation_id text PRIMARY KEY,
			decision_id text NOT NULL REFERENCES %s(decision_id),
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			canonical_request_digest bytea NOT NULL CHECK (octet_length(canonical_request_digest) = 32),
			authority_scope_digest bytea NOT NULL CHECK (octet_length(authority_scope_digest) = 32),
			payload bytea NOT NULL,
			payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
			committed_at timestamptz NOT NULL
		)`, outbox, decisions, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			operation_id text PRIMARY KEY REFERENCES %s(operation_id),
			disposition smallint NOT NULL,
			ack_digest bytea CHECK (ack_digest IS NULL OR octet_length(ack_digest) = 32),
			delivery_count bigint NOT NULL DEFAULT 0 CHECK (delivery_count >= 0),
			first_attempt_at timestamptz,
			last_attempt_at timestamptz,
			acknowledged_at timestamptz
		)`, delivery, outbox),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			operation_id text PRIMARY KEY,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			decision_id text NOT NULL UNIQUE REFERENCES %s(decision_id),
			reason smallint NOT NULL,
			status smallint NOT NULL,
			first_recorded_at timestamptz NOT NULL,
			last_recorded_at timestamptz NOT NULL,
			observation_count bigint NOT NULL CHECK (observation_count > 0),
			unresolved boolean NOT NULL,
			evidence_root_id text NOT NULL DEFAULT '',
			evidence_root_digest bytea CHECK (evidence_root_digest IS NULL OR octet_length(evidence_root_digest) = 32)
		)`, reconciliation, runtimes, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			fact_id text PRIMARY KEY REFERENCES %s(decision_id),
			audit_delivery_status smallint NOT NULL,
			telemetry_delivery_status smallint NOT NULL,
			attempt_count bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			last_safe_failure smallint NOT NULL DEFAULT 0,
			degraded boolean NOT NULL DEFAULT FALSE,
			first_attempt_at timestamptz,
			last_attempt_at timestamptz,
			delivered_at timestamptz
		)`, projection, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			evidence_root_id text PRIMARY KEY,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			schema_version bigint NOT NULL,
			digest bytea NOT NULL CHECK (octet_length(digest) = 32),
			accepted_at timestamptz NOT NULL
		)`, evidenceRoots, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			runtime_run_id text PRIMARY KEY REFERENCES %s(runtime_run_id),
			lease_id text NOT NULL,
			lease_generation bigint NOT NULL CHECK (lease_generation > 0),
			lease_fence bigint NOT NULL CHECK (lease_fence > 0),
			current_fact boolean NOT NULL,
			conflict boolean NOT NULL,
			uncontained boolean NOT NULL,
			evidence_root_id text NOT NULL REFERENCES %s(evidence_root_id),
			evidence_root_digest bytea NOT NULL CHECK (octet_length(evidence_root_digest) = 32),
			updated_at timestamptz NOT NULL
		)`, leaseRoots, runtimes, evidenceRoots),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			debt_id text PRIMARY KEY,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			resource_identity_digest bytea NOT NULL CHECK (octet_length(resource_identity_digest) = 32),
			resource_generation bigint NOT NULL CHECK (resource_generation > 0),
			resource_fence bigint NOT NULL CHECK (resource_fence > 0),
			status smallint NOT NULL,
			unresolved boolean NOT NULL,
			uncontained boolean NOT NULL,
			first_recorded_at timestamptz NOT NULL,
			last_recorded_at timestamptz NOT NULL,
			attempt_count bigint NOT NULL CHECK (attempt_count > 0),
			safe_reason smallint NOT NULL,
			evidence_root_id text NOT NULL REFERENCES %s(evidence_root_id),
			evidence_root_digest bytea NOT NULL CHECK (octet_length(evidence_root_digest) = 32)
		)`, cleanup, runtimes, evidenceRoots),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			heartbeat_sequence bigint NOT NULL CHECK (heartbeat_sequence > 0),
			lease_id text NOT NULL,
			lease_generation bigint NOT NULL CHECK (lease_generation > 0),
			lease_fence bigint NOT NULL CHECK (lease_fence > 0),
			observed_at timestamptz NOT NULL,
			reason smallint NOT NULL,
			evidence_root_id text NOT NULL REFERENCES %s(evidence_root_id),
			evidence_root_digest bytea NOT NULL CHECK (octet_length(evidence_root_digest) = 32),
			terminal_history boolean NOT NULL,
			conflict boolean NOT NULL,
			uncontained boolean NOT NULL,
			unresolved_reconciliation boolean NOT NULL,
			PRIMARY KEY (runtime_run_id, heartbeat_sequence)
		)`, heartbeats, runtimes, evidenceRoots),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			lease_id text NOT NULL,
			lease_generation bigint NOT NULL,
			lease_fence bigint NOT NULL,
			reason smallint NOT NULL,
			evidence_root_id text NOT NULL REFERENCES %s(evidence_root_id),
			evidence_root_digest bytea NOT NULL CHECK (octet_length(evidence_root_digest) = 32),
			first_observed_at timestamptz NOT NULL,
			last_observed_at timestamptz NOT NULL,
			observation_count bigint NOT NULL CHECK (observation_count > 0),
			authenticated_digest bytea NOT NULL CHECK (octet_length(authenticated_digest) = 32),
			PRIMARY KEY (runtime_run_id, lease_id, lease_generation, lease_fence, reason, evidence_root_id)
		)`, compaction, runtimes, evidenceRoots),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'immutable runtime execution fact'; END $$`, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", decisions),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", decisions, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", requests),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", requests, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", revisions),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", revisions, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", audit),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", audit, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", outbox),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", outbox, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", evidenceRoots),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", evidenceRoots, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", compaction),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", compaction, immutableFunction),
	}
}

func (authority *PostgresAuthority) failAt(point PersistenceFaultPoint) bool {
	return authority.faults != nil && authority.faults.FailAt(point)
}

func (authority *PostgresAuthority) Execute(ctx context.Context, command RuntimeCommand) (RuntimeDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	kind, operationID, workspaceID, runtimeRunID, caller, digest, canonical, err := retainedCommandBinding(command)
	if err != nil {
		return RuntimeDecision{}, err
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()

	runtimeRow := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id, aggregate_state
		FROM %s WHERE runtime_run_id=$1`, authority.table("runtime_execution_runtimes")), runtimeRunID.String())
	record, err := scanPostgresRuntimeRecord(runtimeRow, runtimeRunID)
	if err == sql.ErrNoRows || err == nil && !authorized(record, workspaceID, caller) {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}

	var retainedDigest []byte
	var retainedCanonical []byte
	var decisionID string
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT canonical_request_digest, canonical_request, decision_id
		FROM %s WHERE runtime_run_id=$1 AND command_kind=$2 AND operation_id=$3`,
		authority.table("runtime_execution_requests")), runtimeRunID.String(), kind, operationID.String()).
		Scan(&retainedDigest, &retainedCanonical, &decisionID)
	if err == sql.ErrNoRows {
		// #75 owns fresh Start/Accepted/Bound. This foundation can only replay a
		// retained command and therefore cannot create a half-admission state.
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if !bytes.Equal(retainedDigest, digest[:]) || !bytes.Equal(retainedCanonical, canonical) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	fact, err := authority.loadRetainedDecision(
		ctx, tx, decisionID, workspaceID, runtimeRunID, operationID, digest, canonical, caller,
	)
	if err != nil {
		return RuntimeDecision{}, err
	}
	projected, representable := renderSnapshot(record, SnapshotSchemaCurrent)
	if !representable {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	return RuntimeDecision{Fact: fact, Snapshot: projected}, nil
}

func retainedCommandBinding(command RuntimeCommand) (
	CommandKind,
	OperationID,
	PersonalWorkspaceID,
	RuntimeRunID,
	RuntimeAuthority,
	Digest,
	[]byte,
	error,
) {
	if command == nil {
		return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorInvalidRequest)
	}
	switch typed := command.(type) {
	case StartRuntimeRun:
		if typed.SchemaVersion == 0 {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorInvalidRequest)
		}
		if typed.SchemaVersion.Major() != SchemaV1.Major() {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorUnsupportedSchema)
		}
		canonical, err := canonicalStartEncoding(typed)
		if err != nil {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, err
		}
		digest := Digest(sha256.Sum256(append([]byte(canonicalRequestDomain), canonical...)))
		if digest != typed.CanonicalRequestDigest {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorIntegrityConflict)
		}
		if typed.Authority.kind != AuthorityTaskOrchestration {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorAuthorizationDenied)
		}
		return CommandStartRuntimeRun, typed.OperationID, typed.PersonalWorkspaceID, typed.RuntimeRunID,
			typed.Authority, digest, canonical, nil
	case CancelRuntimeRun:
		if typed.SchemaVersion == 0 {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorInvalidRequest)
		}
		if typed.SchemaVersion.Major() != SchemaV1.Major() {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorUnsupportedSchema)
		}
		canonical, err := canonicalCancelEncoding(typed)
		if err != nil {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, err
		}
		digest := Digest(sha256.Sum256(append([]byte(canonicalRequestDomain), canonical...)))
		if digest != typed.CanonicalRequestDigest {
			return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorIntegrityConflict)
		}
		return CommandCancelRuntimeRun, typed.OperationID, typed.PersonalWorkspaceID, typed.RuntimeRunID,
			typed.Authority, digest, canonical, nil
	default:
		return 0, OperationID{}, PersonalWorkspaceID{}, RuntimeRunID{}, RuntimeAuthority{}, Digest{}, nil, newError(ErrorInvalidRequest)
	}
}

func (authority *PostgresAuthority) loadRetainedDecision(
	ctx context.Context,
	tx *sql.Tx,
	decisionID string,
	workspaceID PersonalWorkspaceID,
	runtimeRunID RuntimeRunID,
	operationID OperationID,
	digest Digest,
	canonical []byte,
	caller RuntimeAuthority,
) (RuntimeDecisionFact, error) {
	var storedRuntimeRunID, storedOperationID string
	var storedDigest, state []byte
	var previousRevision, resultingRevision RuntimeRevision
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, operation_id, canonical_request_digest,
		previous_runtime_revision, resulting_runtime_revision, decision_state
		FROM %s WHERE decision_id=$1`, authority.table("runtime_execution_decisions")), decisionID).
		Scan(&storedRuntimeRunID, &storedOperationID, &storedDigest, &previousRevision, &resultingRevision, &state)
	if err != nil {
		if err == sql.ErrNoRows {
			return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
		}
		return RuntimeDecisionFact{}, normalizeRuntimePersistenceFailure(err)
	}
	fact, err := decodePostgresDecisionFact(state)
	if err != nil || fact.DecisionID.String() != decisionID || storedRuntimeRunID != runtimeRunID.String() ||
		storedOperationID != operationID.String() || !bytes.Equal(storedDigest, digest[:]) ||
		fact.OperationID != operationID || fact.CanonicalRequestDigest != digest ||
		fact.PreviousRuntimeRevision != previousRevision || fact.ResultingRuntimeRevision != resultingRevision {
		return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
	}
	var auditRuntimeRunID, auditOperationID, auditAuthorityID string
	var auditRequestDigest, auditCanonicalDigest []byte
	var auditAuthorityKind AuthorityKind
	var auditAuthorityGeneration AuthorizationGeneration
	var auditBeforeRevision, auditAfterRevision RuntimeRevision
	var auditRecordedAt time.Time
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT runtime_run_id, operation_id, request_digest,
		canonical_digest, authority_kind, authority_id, authority_generation,
		before_revision, after_revision, recorded_at FROM %s WHERE decision_id=$1`,
		authority.table("runtime_execution_mandatory_audit")), decisionID).Scan(
		&auditRuntimeRunID, &auditOperationID, &auditRequestDigest, &auditCanonicalDigest,
		&auditAuthorityKind, &auditAuthorityID, &auditAuthorityGeneration,
		&auditBeforeRevision, &auditAfterRevision, &auditRecordedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
		}
		return RuntimeDecisionFact{}, newError(ErrorDependencyUnavailable)
	}
	wantAuditDigest := mandatoryAuditCanonicalDigest(
		RuntimeDecisionID{value: decisionID}, runtimeRunID, operationID, digest, caller,
		auditBeforeRevision, auditAfterRevision, auditRecordedAt.UTC(),
	)
	if auditRuntimeRunID != runtimeRunID.String() || auditOperationID != operationID.String() ||
		!bytes.Equal(auditRequestDigest, digest[:]) || !bytes.Equal(auditCanonicalDigest, wantAuditDigest[:]) ||
		auditAuthorityKind != caller.kind || auditAuthorityID != caller.id.String() ||
		auditAuthorityGeneration != caller.generation || auditBeforeRevision != fact.PreviousRuntimeRevision ||
		auditAfterRevision != fact.ResultingRuntimeRevision {
		return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
	}
	var outboxDecisionID, outboxRuntimeRunID string
	var outboxCanonicalDigest, outboxScopeDigest, outboxPayload, outboxPayloadDigest []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT decision_id, runtime_run_id,
		canonical_request_digest, authority_scope_digest, payload, payload_digest
		FROM %s WHERE operation_id=$1`, authority.table("runtime_execution_outbox")), operationID.String()).Scan(
		&outboxDecisionID, &outboxRuntimeRunID, &outboxCanonicalDigest, &outboxScopeDigest,
		&outboxPayload, &outboxPayloadDigest,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
		}
		return RuntimeDecisionFact{}, newError(ErrorDependencyUnavailable)
	}
	wantScopeDigest := authorityScopeDigest(workspaceID, runtimeRunID)
	wantPayloadDigest := digestBytes(canonical)
	if outboxDecisionID != decisionID || outboxRuntimeRunID != runtimeRunID.String() ||
		!bytes.Equal(outboxCanonicalDigest, digest[:]) || !bytes.Equal(outboxScopeDigest, wantScopeDigest[:]) ||
		!bytes.Equal(outboxPayload, canonical) || !bytes.Equal(outboxPayloadDigest, wantPayloadDigest[:]) {
		return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
	}
	return fact, nil
}

func digestBytes(payload []byte) Digest {
	return Digest(sha256.Sum256(payload))
}

func authorityScopeDigest(workspaceID PersonalWorkspaceID, runtimeRunID RuntimeRunID) Digest {
	payload := []byte("slidesmith.runtime-execution.authority-scope/v1\n" + workspaceID.String() + "\n" + runtimeRunID.String())
	return digestBytes(payload)
}

func (authority *PostgresAuthority) Inspect(ctx context.Context, ref RuntimeRunRef) (RuntimeSnapshot, error) {
	if ctx == nil || ctx.Err() != nil {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	if ref.SchemaVersion == 0 || !validOpaqueID(ref.PersonalWorkspaceID.String()) ||
		!validOpaqueID(ref.RuntimeRunID.String()) || !validAuthority(ref.Authority) {
		return RuntimeSnapshot{}, newError(ErrorInvalidRequest)
	}
	if ref.SchemaVersion.Major() != SchemaV1.Major() ||
		(ref.ProjectionVersion != SnapshotSchemaCurrent && ref.ProjectionVersion != SnapshotSchemaV1) {
		return RuntimeSnapshot{}, newError(ErrorUnsupportedSchema)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id, aggregate_state
		FROM %s WHERE runtime_run_id=$1`, authority.table("runtime_execution_runtimes")), ref.RuntimeRunID.String())
	record, err := scanPostgresRuntimeRecord(row, ref.RuntimeRunID)
	if err == sql.ErrNoRows {
		return RuntimeSnapshot{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
	}
	if !authorized(record, ref.PersonalWorkspaceID, ref.Authority) {
		return RuntimeSnapshot{}, newError(ErrorAuthorizationDenied)
	}
	projected, representable := renderSnapshot(record, ref.ProjectionVersion)
	if !representable {
		return RuntimeSnapshot{}, newError(ErrorUnsupportedSchema)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	return projected, nil
}

func normalizeRuntimePersistenceFailure(err error) error {
	if err == nil {
		return nil
	}
	switch classifyPersistenceFailure(err) {
	case PersistenceAuthorizationDenied:
		return newError(ErrorAuthorizationDenied)
	case PersistenceIntegrityConflict, PersistenceStateCorrupt:
		return newError(ErrorIntegrityConflict)
	case PersistenceInvalidConfiguration:
		return newError(ErrorInvalidRequest)
	default:
		return newError(ErrorDependencyUnavailable)
	}
}

func classifyPersistenceFailure(err error) PersistenceErrorCode {
	var persistenceError *PersistenceError
	if errors.As(err, &persistenceError) {
		return persistenceError.Code()
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return PersistenceUnavailable
	}
	code := postgresError.Code
	switch {
	case code == "42501" || strings.HasPrefix(code, "28"):
		return PersistenceAuthorizationDenied
	case strings.HasPrefix(code, "22"), strings.HasPrefix(code, "23"):
		return PersistenceIntegrityConflict
	case strings.HasPrefix(code, "42"):
		return PersistenceStateCorrupt
	case strings.HasPrefix(code, "08"), strings.HasPrefix(code, "40"),
		strings.HasPrefix(code, "53"), code == "55P03", code == "57P01":
		return PersistenceUnavailable
	default:
		return PersistenceUnavailable
	}
}
