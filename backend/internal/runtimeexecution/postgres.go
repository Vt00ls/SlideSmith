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
	Schema                           string
	Now                              func() time.Time
	Faults                           PersistenceFaultInjector
	ProjectionDelivery               ProjectionDelivery
	SchedulerParticipant             SchedulerAcceptanceParticipant
	SchedulerAcceptanceFunction      string
	SchedulerCancellationParticipant SchedulerCancellationParticipant
	SchedulerCancellationFunction    string
	LeaseAcquisition                 LeaseAcquisitionAdapter
}

// PostgresAuthority owns C03 persistence behind the RuntimeExecution seam.
// Its implementation does not expose SQL, a general repository, or a
// caller-controlled transaction handle.
type PostgresAuthority struct {
	db                               *sql.DB
	schema                           string
	now                              func() time.Time
	faults                           PersistenceFaultInjector
	projection                       ProjectionDelivery
	schedulerParticipant             SchedulerAcceptanceParticipant
	schedulerAcceptanceFunction      string
	schedulerCancellationParticipant SchedulerCancellationParticipant
	schedulerCancellationFunction    string
	leaseAcquisition                 LeaseAcquisitionAdapter
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
	if config.SchedulerParticipant != nil && !validPostgresQualifiedIdentifier(config.SchedulerAcceptanceFunction) ||
		config.SchedulerParticipant == nil && config.SchedulerAcceptanceFunction != "" ||
		config.SchedulerCancellationParticipant != nil && !validPostgresQualifiedIdentifier(config.SchedulerCancellationFunction) ||
		config.SchedulerCancellationParticipant == nil && config.SchedulerCancellationFunction != "" {
		return nil, newPersistenceError(PersistenceInvalidConfiguration)
	}
	return &PostgresAuthority{
		db: db, schema: schema, now: now, faults: config.Faults, projection: config.ProjectionDelivery,
		schedulerParticipant:             config.SchedulerParticipant,
		schedulerAcceptanceFunction:      config.SchedulerAcceptanceFunction,
		schedulerCancellationParticipant: config.SchedulerCancellationParticipant,
		schedulerCancellationFunction:    config.SchedulerCancellationFunction,
		leaseAcquisition:                 config.LeaseAcquisition,
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

func validPostgresQualifiedIdentifier(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	return validPostgresIdentifier(parts[0]) && validPostgresIdentifier(parts[1])
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
	if err := authority.db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		return normalizePostgresPersistenceFailure(err)
	}
	if version < 120000 {
		return newPersistenceError(PersistenceUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, nil)
	if err != nil {
		return normalizePostgresPersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(740074)"); err != nil {
		return normalizePostgresPersistenceFailure(err)
	}
	for _, statement := range authority.migrationStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return normalizePostgresPersistenceFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return normalizePostgresPersistenceFailure(err)
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
	capacityOutbox := authority.table("runtime_execution_capacity_outbox")
	preLeaseLeases := authority.table("runtime_execution_prelease_leases")
	delivery := authority.table("runtime_execution_outbox_delivery")
	reconciliation := authority.table("runtime_execution_reconciliation_obligations")
	projection := authority.table("runtime_execution_projection_backlog")
	integrityIncidents := authority.table("runtime_execution_integrity_incidents")
	leaseRoots := authority.table("runtime_execution_lease_roots")
	evidenceRoots := authority.table("runtime_execution_evidence_roots")
	cleanup := authority.table("runtime_execution_cleanup_obligations")
	cleanupMutations := authority.table("runtime_execution_cleanup_mutations")
	cleanupResolutionProofs := authority.table("runtime_execution_cleanup_resolution_proofs")
	cleanupResolutionAudit := authority.table("runtime_execution_cleanup_resolution_audit")
	heartbeats := authority.table("runtime_execution_heartbeat_history")
	compaction := authority.table("runtime_execution_heartbeat_compaction")
	immutableFunction := authority.table("runtime_execution_reject_immutable_mutation")
	cleanupRebindingFunction := authority.table("runtime_execution_reject_cleanup_rebinding")
	return []string{
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", authority.table("runtime_execution_decision_sequence")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", authority.table("runtime_execution_sandbox_lease_sequence")),
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
			admission_work_item_id text NOT NULL DEFAULT '',
			admission_grant_generation bigint NOT NULL DEFAULT 0 CHECK (admission_grant_generation >= 0),
			PRIMARY KEY (runtime_run_id, operation_id)
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
			schema_version bigint NOT NULL,
			integrity_version smallint NOT NULL,
			owning_module text NOT NULL,
			canonical_digest bytea NOT NULL CHECK (octet_length(canonical_digest) = 32),
			authority_kind smallint NOT NULL,
			authority_id text NOT NULL,
			authority_generation bigint NOT NULL CHECK (authority_generation > 0),
			action smallint NOT NULL,
			result smallint NOT NULL,
			before_revision bigint NOT NULL CHECK (before_revision > 0),
			after_revision bigint NOT NULL CHECK (after_revision > 0),
			occurred_at timestamptz NOT NULL,
			recorded_at timestamptz NOT NULL,
			source_clock_id text NOT NULL,
			audit_state jsonb NOT NULL
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
			terminal_decision_id text PRIMARY KEY REFERENCES %s(decision_id),
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			work_item_id text NOT NULL,
			admission_grant_id text NOT NULL,
			grant_generation bigint NOT NULL CHECK (grant_generation > 0),
			runtime_fenced_or_terminal jsonb NOT NULL,
			no_lease_physical_disposition jsonb NOT NULL,
			physical_capacity_release_ready jsonb,
			committed_at timestamptz NOT NULL
			)`, capacityOutbox, decisions, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			lease_acquire_operation_id text PRIMARY KEY,
			lease_acquire_digest bytea NOT NULL CHECK (octet_length(lease_acquire_digest) = 32),
			runtime_run_id text NOT NULL UNIQUE REFERENCES %s(runtime_run_id),
			start_operation_id text NOT NULL,
			start_digest bytea NOT NULL CHECK (octet_length(start_digest) = 32),
			work_item_id text NOT NULL,
			admission_grant_id text NOT NULL,
			grant_generation bigint NOT NULL CHECK (grant_generation > 0),
			execution_node_id text NOT NULL,
			node_capacity_generation bigint NOT NULL CHECK (node_capacity_generation > 0),
			resource_class_id text NOT NULL,
			execution_policy_id text NOT NULL,
			scheduler_epoch bigint NOT NULL CHECK (scheduler_epoch > 0),
			policy_version bigint NOT NULL CHECK (policy_version > 0),
			safety_epoch bigint NOT NULL CHECK (safety_epoch > 0),
			sandbox_lease_id text NOT NULL UNIQUE,
			lease_generation bigint NOT NULL CHECK (lease_generation > 0),
			lease_fence bigint NOT NULL CHECK (lease_fence > 0),
			committed_at timestamptz NOT NULL
		)`, preLeaseLeases, runtimes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			operation_id text PRIMARY KEY,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			decision_id text NOT NULL UNIQUE REFERENCES %s(decision_id),
			owner_authority_kind smallint NOT NULL,
			owner_authority_id text NOT NULL,
			owner_authority_generation bigint NOT NULL CHECK (owner_authority_generation > 0),
			runtime_revision bigint NOT NULL CHECK (runtime_revision > 0),
			operation_generation bigint NOT NULL CHECK (operation_generation > 0),
			runtime_fence bigint NOT NULL CHECK (runtime_fence > 0),
			reason smallint NOT NULL,
			status smallint NOT NULL,
			result smallint NOT NULL,
			first_recorded_at timestamptz NOT NULL,
			last_recorded_at timestamptz NOT NULL,
			observation_count bigint NOT NULL CHECK (observation_count > 0),
			unresolved boolean NOT NULL,
			next_retry_at timestamptz,
			safe_failure_count bigint NOT NULL DEFAULT 0 CHECK (safe_failure_count >= 0),
			stale_evidence_count bigint NOT NULL DEFAULT 0 CHECK (stale_evidence_count >= 0),
			evidence_root_id text NOT NULL DEFAULT '',
			evidence_root_digest bytea CHECK (evidence_root_digest IS NULL OR octet_length(evidence_root_digest) = 32)
		)`, reconciliation, runtimes, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			fact_id text PRIMARY KEY REFERENCES %s(decision_id),
			audit_fact_id text NOT NULL REFERENCES %s(audit_fact_id),
			audit_canonical_digest bytea NOT NULL CHECK (octet_length(audit_canonical_digest) = 32),
			fact_revision bigint NOT NULL CHECK (fact_revision > 0),
			projection_schema_version bigint NOT NULL,
			audit_delivery_status smallint NOT NULL,
			telemetry_delivery_status smallint NOT NULL,
			attempt_count bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			last_safe_failure smallint NOT NULL DEFAULT 0,
			degraded boolean NOT NULL DEFAULT FALSE,
			first_attempt_at timestamptz,
			last_attempt_at timestamptz,
			delivered_at timestamptz
		)`, projection, decisions, audit),
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
			personal_workspace_id text NOT NULL,
			task_id text NOT NULL,
			phase_run_id text NOT NULL,
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			owner_module text NOT NULL,
			resource_class smallint NOT NULL,
			resource_identity_digest bytea NOT NULL CHECK (octet_length(resource_identity_digest) = 32),
			resource_generation bigint NOT NULL CHECK (resource_generation > 0),
			resource_fence bigint NOT NULL CHECK (resource_fence > 0),
			cleanup_intent smallint NOT NULL,
			cause_decision_id text NOT NULL REFERENCES %s(decision_id),
			cause_operation_id text NOT NULL,
			retention_fact_digest bytea NOT NULL CHECK (octet_length(retention_fact_digest) = 32),
			eligibility_fact_digest bytea NOT NULL CHECK (octet_length(eligibility_fact_digest) = 32),
			debt_revision bigint NOT NULL CHECK (debt_revision > 0),
			status smallint NOT NULL,
			unresolved boolean NOT NULL,
			uncontained boolean NOT NULL,
			canonical_digest bytea NOT NULL CHECK (octet_length(canonical_digest) = 32),
			debt_state jsonb NOT NULL,
			updated_at timestamptz NOT NULL,
			UNIQUE (owner_module, resource_class, resource_identity_digest,
				resource_generation, resource_fence, cleanup_intent)
		)`, cleanup, runtimes, decisions),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			debt_id text NOT NULL REFERENCES %s(debt_id),
			mutation_id text NOT NULL,
			mutation_kind smallint NOT NULL,
			mutation_digest bytea NOT NULL CHECK (octet_length(mutation_digest) = 32),
			result_revision bigint NOT NULL CHECK (result_revision > 0),
			result_digest bytea NOT NULL CHECK (octet_length(result_digest) = 32),
			result_state jsonb NOT NULL,
			committed_at timestamptz NOT NULL,
			PRIMARY KEY (debt_id, mutation_id)
		)`, cleanupMutations, cleanup),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			proof_id text PRIMARY KEY,
			debt_id text NOT NULL REFERENCES %s(debt_id),
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			resource_identity_digest bytea NOT NULL CHECK (octet_length(resource_identity_digest) = 32),
			resource_generation bigint NOT NULL CHECK (resource_generation > 0),
			resource_fence bigint NOT NULL CHECK (resource_fence > 0),
			resolution_class smallint NOT NULL,
			resolution_reason smallint NOT NULL,
			proof_disposition smallint NOT NULL,
			evidence_root_id text NOT NULL REFERENCES %s(evidence_root_id),
			evidence_root_digest bytea NOT NULL CHECK (octet_length(evidence_root_digest) = 32),
			observed_at timestamptz NOT NULL,
			recorded_at timestamptz NOT NULL,
			source_clock_id text NOT NULL,
			canonical_digest bytea NOT NULL CHECK (octet_length(canonical_digest) = 32),
			proof_state jsonb NOT NULL,
			UNIQUE (debt_id, resolution_class, evidence_root_id)
		)`, cleanupResolutionProofs, cleanup, runtimes, evidenceRoots),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			audit_fact_id text PRIMARY KEY,
			debt_id text NOT NULL UNIQUE REFERENCES %s(debt_id),
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			operation_id text NOT NULL,
			operation_digest bytea NOT NULL CHECK (octet_length(operation_digest) = 32),
			resource_identity_digest bytea NOT NULL CHECK (octet_length(resource_identity_digest) = 32),
			resource_generation bigint NOT NULL CHECK (resource_generation > 0),
			resource_fence bigint NOT NULL CHECK (resource_fence > 0),
			resolution_class smallint NOT NULL,
			resolution_reason smallint NOT NULL,
			authority_kind smallint NOT NULL,
			authority_id text NOT NULL,
			authority_generation bigint NOT NULL CHECK (authority_generation > 0),
			before_debt_revision bigint NOT NULL CHECK (before_debt_revision > 0),
			after_debt_revision bigint NOT NULL CHECK (after_debt_revision = before_debt_revision + 1),
			before_debt_status smallint NOT NULL,
			after_debt_status smallint NOT NULL,
			evidence_root_id text NOT NULL REFERENCES %s(evidence_root_id),
			evidence_root_digest bytea NOT NULL CHECK (octet_length(evidence_root_digest) = 32),
			resolution_proof_id text NOT NULL REFERENCES %s(proof_id),
			resolution_proof_digest bytea NOT NULL CHECK (octet_length(resolution_proof_digest) = 32),
			occurred_at timestamptz NOT NULL,
			recorded_at timestamptz NOT NULL,
			source_clock_id text NOT NULL,
			canonical_digest bytea NOT NULL CHECK (octet_length(canonical_digest) = 32),
			audit_state jsonb NOT NULL
		)`, cleanupResolutionAudit, cleanup, runtimes, evidenceRoots, cleanupResolutionProofs),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			runtime_run_id text NOT NULL REFERENCES %s(runtime_run_id),
			operation_id text NOT NULL,
			retained_command_kind smallint NOT NULL,
			attempted_command_kind smallint NOT NULL,
			retained_request_digest bytea NOT NULL CHECK (octet_length(retained_request_digest) = 32),
			attempted_request_digest bytea NOT NULL CHECK (octet_length(attempted_request_digest) = 32),
			retained_grant_id text NOT NULL,
			attempted_grant_id text NOT NULL,
			retained_work_item_id text NOT NULL,
			attempted_work_item_id text NOT NULL,
			retained_grant_generation bigint NOT NULL CHECK (retained_grant_generation >= 0),
			attempted_grant_generation bigint NOT NULL CHECK (attempted_grant_generation >= 0),
			authority_scope_digest bytea NOT NULL CHECK (octet_length(authority_scope_digest) = 32),
			observed_at timestamptz NOT NULL,
			PRIMARY KEY (runtime_run_id, operation_id, attempted_command_kind,
				attempted_request_digest, attempted_grant_id, attempted_work_item_id, attempted_grant_generation)
		)`, integrityIncidents, runtimes),
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
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF TG_OP = 'DELETE' THEN
				RAISE EXCEPTION USING ERRCODE = '23000', MESSAGE = 'immutable cleanup authority';
			END IF;
			IF OLD.debt_id IS DISTINCT FROM NEW.debt_id
				OR OLD.personal_workspace_id IS DISTINCT FROM NEW.personal_workspace_id
				OR OLD.task_id IS DISTINCT FROM NEW.task_id
				OR OLD.phase_run_id IS DISTINCT FROM NEW.phase_run_id
				OR OLD.runtime_run_id IS DISTINCT FROM NEW.runtime_run_id
				OR OLD.owner_module IS DISTINCT FROM NEW.owner_module
				OR OLD.resource_class IS DISTINCT FROM NEW.resource_class
				OR OLD.resource_identity_digest IS DISTINCT FROM NEW.resource_identity_digest
				OR OLD.resource_generation IS DISTINCT FROM NEW.resource_generation
				OR OLD.resource_fence IS DISTINCT FROM NEW.resource_fence
				OR OLD.cleanup_intent IS DISTINCT FROM NEW.cleanup_intent
				OR OLD.cause_decision_id IS DISTINCT FROM NEW.cause_decision_id
				OR OLD.cause_operation_id IS DISTINCT FROM NEW.cause_operation_id
				OR OLD.retention_fact_digest IS DISTINCT FROM NEW.retention_fact_digest
				OR OLD.eligibility_fact_digest IS DISTINCT FROM NEW.eligibility_fact_digest THEN
				RAISE EXCEPTION USING ERRCODE = '23000', MESSAGE = 'immutable cleanup authority';
			END IF;
			RETURN NEW;
		END $$`, cleanupRebindingFunction),
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
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", integrityIncidents),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", integrityIncidents, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", cleanupMutations),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", cleanupMutations, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", cleanupResolutionProofs),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", cleanupResolutionProofs, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_immutable_mutation ON %s", cleanupResolutionAudit),
		fmt.Sprintf("CREATE TRIGGER reject_immutable_mutation BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", cleanupResolutionAudit, immutableFunction),
		fmt.Sprintf("DROP TRIGGER IF EXISTS reject_cleanup_rebinding ON %s", cleanup),
		fmt.Sprintf("CREATE TRIGGER reject_cleanup_rebinding BEFORE UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION %s()", cleanup, cleanupRebindingFunction),
	}
}

func (authority *PostgresAuthority) failAt(point PersistenceFaultPoint) bool {
	return authority.faults != nil && authority.faults.FailAt(point)
}

type retainedCommandBindingValue struct {
	kind                        int16
	operationID                 OperationID
	workspaceID                 PersonalWorkspaceID
	runtimeRunID                RuntimeRunID
	caller                      RuntimeAuthority
	digest                      Digest
	canonical                   []byte
	admissionGrantID            AdmissionGrantID
	admissionWorkItemID         WorkItemID
	admissionGrantGeneration    AdmissionGrantGeneration
	expectedOperationGeneration OperationGeneration
	expectedRuntimeFence        RuntimeFence
	safetyEpoch                 ReleaseSafetyEpoch
	auditAction                 postgresMandatoryAuditAction
	auditReasonCode             uint8
}

func (authority *PostgresAuthority) Execute(ctx context.Context, command RuntimeCommand) (RuntimeDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	binding, err := retainedCommandBinding(command)
	if err != nil {
		return RuntimeDecision{}, err
	}
	if start, ok := command.(StartRuntimeRun); ok {
		decision, executeErr := authority.executePostgresStart(ctx, start, binding)
		if executeErr != nil {
			return RuntimeDecision{}, executeErr
		}
		return authority.advancePostgresPreLease(ctx, start, decision)
	}
	if cancel, ok := command.(CancelRuntimeRun); ok {
		return authority.executePostgresCancel(ctx, cancel, binding)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	defer func() { _ = tx.Rollback() }()

	runtimeRow := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, task_id, phase_run_id,
		owner_authority_id, owner_authority_generation, owner_authority_kind,
		task_revision, runtime_revision, operation_generation, runtime_fence,
		safety_epoch, runtime_state, runtime_outcome, terminal_evidence_id, aggregate_state
		FROM %s WHERE runtime_run_id=$1`, authority.table("runtime_execution_runtimes")), binding.runtimeRunID.String())
	record, err := scanPostgresRuntimeRecord(runtimeRow, binding.runtimeRunID)
	if err == sql.ErrNoRows || err == nil && !authorized(record, binding.workspaceID, binding.caller) {
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}

	var retainedDigest []byte
	var retainedCanonical []byte
	var retainedWorkspaceID, decisionID, retainedGrantID, retainedWorkItemID string
	var retainedKind int16
	var retainedGrantGeneration AdmissionGrantGeneration
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT personal_workspace_id, command_kind, canonical_request_digest,
		canonical_request, decision_id, admission_grant_id, admission_work_item_id,
		admission_grant_generation FROM %s WHERE runtime_run_id=$1 AND operation_id=$2`,
		authority.table("runtime_execution_requests")), binding.runtimeRunID.String(), binding.operationID.String()).Scan(
		&retainedWorkspaceID, &retainedKind, &retainedDigest, &retainedCanonical, &decisionID,
		&retainedGrantID, &retainedWorkItemID, &retainedGrantGeneration,
	)
	if err == sql.ErrNoRows {
		// #75 owns fresh Start/Accepted/Bound. This foundation can only replay a
		// retained command and therefore cannot create a half-admission state.
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	if retainedWorkspaceID != binding.workspaceID.String() || retainedKind != binding.kind ||
		!bytes.Equal(retainedDigest, binding.digest[:]) ||
		!bytes.Equal(retainedCanonical, binding.canonical) ||
		retainedGrantID != binding.admissionGrantID.String() ||
		retainedWorkItemID != binding.admissionWorkItemID.String() ||
		retainedGrantGeneration != binding.admissionGrantGeneration {
		if err := authority.recordIntegrityIncident(ctx, tx, binding, retainedKind, retainedDigest,
			retainedGrantID, retainedWorkItemID, retainedGrantGeneration); err != nil {
			return RuntimeDecision{}, err
		}
		if err := tx.Commit(); err != nil {
			return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
		}
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	fact, err := authority.loadRetainedDecision(ctx, tx, decisionID, binding)
	if err != nil {
		return RuntimeDecision{}, err
	}
	projected, representable := renderSnapshot(record, SnapshotSchemaCurrent)
	if !representable {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeDecision{}, normalizeRuntimePersistenceFailure(err)
	}
	return RuntimeDecision{Fact: fact, Snapshot: projected}, nil
}

func retainedCommandBinding(command RuntimeCommand) (retainedCommandBindingValue, error) {
	if command == nil {
		return retainedCommandBindingValue{}, newError(ErrorInvalidRequest)
	}
	switch typed := command.(type) {
	case StartRuntimeRun:
		if typed.SchemaVersion == 0 {
			return retainedCommandBindingValue{}, newError(ErrorInvalidRequest)
		}
		if typed.SchemaVersion.Major() != SchemaV1.Major() {
			return retainedCommandBindingValue{}, newError(ErrorUnsupportedSchema)
		}
		if !validStart(typed) {
			return retainedCommandBindingValue{}, newError(ErrorInvalidRequest)
		}
		canonical, err := canonicalStartEncoding(typed)
		if err != nil {
			return retainedCommandBindingValue{}, err
		}
		digest := canonicalRequestDigest(canonical)
		if digest != typed.CanonicalRequestDigest {
			return retainedCommandBindingValue{}, newError(ErrorIntegrityConflict)
		}
		if typed.Authority.kind != AuthorityTaskOrchestration {
			return retainedCommandBindingValue{}, newError(ErrorAuthorizationDenied)
		}
		return retainedCommandBindingValue{
			kind: int16(CommandStartRuntimeRun), operationID: typed.OperationID,
			workspaceID: typed.PersonalWorkspaceID, runtimeRunID: typed.RuntimeRunID,
			caller: typed.Authority, digest: digest, canonical: canonical,
			admissionGrantID:            typed.AdmissionGrant.AdmissionGrantID,
			admissionWorkItemID:         typed.AdmissionGrant.WorkItemID,
			admissionGrantGeneration:    typed.AdmissionGrant.Generation,
			expectedOperationGeneration: typed.ExpectedOperationGeneration,
			expectedRuntimeFence:        typed.ExpectedRuntimeFence, safetyEpoch: typed.ReleaseSafetyEpoch,
			auditAction: postgresAuditStartAccepted,
		}, nil
	case CancelRuntimeRun:
		if typed.SchemaVersion == 0 {
			return retainedCommandBindingValue{}, newError(ErrorInvalidRequest)
		}
		if typed.SchemaVersion.Major() != SchemaV1.Major() {
			return retainedCommandBindingValue{}, newError(ErrorUnsupportedSchema)
		}
		canonical, err := canonicalCancelEncoding(typed)
		if err != nil {
			return retainedCommandBindingValue{}, err
		}
		digest := canonicalRequestDigest(canonical)
		if digest != typed.CanonicalRequestDigest {
			return retainedCommandBindingValue{}, newError(ErrorIntegrityConflict)
		}
		return retainedCommandBindingValue{
			kind: int16(CommandCancelRuntimeRun), operationID: typed.OperationID,
			workspaceID: typed.PersonalWorkspaceID, runtimeRunID: typed.RuntimeRunID,
			caller: typed.Authority, digest: digest, canonical: canonical,
			expectedOperationGeneration: typed.ExpectedOperationGeneration,
			expectedRuntimeFence:        typed.ExpectedRuntimeFence, safetyEpoch: typed.SafetyEpoch,
			auditAction: postgresAuditCancelAccepted, auditReasonCode: uint8(typed.Reason),
		}, nil
	default:
		return retainedCommandBindingValue{}, newError(ErrorInvalidRequest)
	}
}

func canonicalRequestDigest(canonical []byte) Digest {
	return Digest(sha256.Sum256(append([]byte(canonicalRequestDomain), canonical...)))
}

func (authority *PostgresAuthority) loadRetainedDecision(
	ctx context.Context,
	tx *sql.Tx,
	decisionID string,
	binding retainedCommandBindingValue,
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
	if err != nil || fact.DecisionID.String() != decisionID || storedRuntimeRunID != binding.runtimeRunID.String() ||
		storedOperationID != binding.operationID.String() || !bytes.Equal(storedDigest, binding.digest[:]) ||
		fact.OperationID != binding.operationID || fact.CanonicalRequestDigest != binding.digest ||
		fact.PreviousRuntimeRevision != previousRevision || fact.ResultingRuntimeRevision != resultingRevision {
		return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
	}
	auditView, err := authority.loadMandatoryAuditInTransaction(ctx, tx, postgresMandatoryAuditRef{
		DecisionID: fact.DecisionID, PersonalWorkspaceID: binding.workspaceID,
		RuntimeRunID: binding.runtimeRunID, OperationID: binding.operationID,
		RequestDigest: binding.digest, Authority: binding.caller,
	})
	if err != nil {
		return RuntimeDecisionFact{}, err
	}
	if auditView.State.Action != binding.auditAction ||
		auditView.State.Result != postgresAuditAccepted ||
		!auditStateMatchesBinding(auditView.State, fact, binding) {
		return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
	}
	var outboxDecisionID, outboxRuntimeRunID string
	var outboxCanonicalDigest, outboxScopeDigest, outboxPayload, outboxPayloadDigest []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT decision_id, runtime_run_id,
		canonical_request_digest, authority_scope_digest, payload, payload_digest
		FROM %s WHERE operation_id=$1`, authority.table("runtime_execution_outbox")), binding.operationID.String()).Scan(
		&outboxDecisionID, &outboxRuntimeRunID, &outboxCanonicalDigest, &outboxScopeDigest,
		&outboxPayload, &outboxPayloadDigest,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
		}
		return RuntimeDecisionFact{}, normalizeRuntimePersistenceFailure(err)
	}
	wantScopeDigest := authorityScopeDigest(binding.workspaceID, binding.runtimeRunID)
	wantPayloadDigest := digestBytes(binding.canonical)
	if outboxDecisionID != decisionID || outboxRuntimeRunID != binding.runtimeRunID.String() ||
		!bytes.Equal(outboxCanonicalDigest, binding.digest[:]) || !bytes.Equal(outboxScopeDigest, wantScopeDigest[:]) ||
		!bytes.Equal(outboxPayload, binding.canonical) || !bytes.Equal(outboxPayloadDigest, wantPayloadDigest[:]) {
		return RuntimeDecisionFact{}, newError(ErrorIntegrityConflict)
	}
	return fact, nil
}

func mustParsePostgresAuditTime(value string) time.Time {
	parsed, _ := time.Parse(canonicalTimeFormat, value)
	return parsed
}

func auditStateMatchesBinding(
	state postgresMandatoryAuditState,
	fact RuntimeDecisionFact,
	binding retainedCommandBindingValue,
) bool {
	return state.DecisionID == fact.DecisionID.String() && state.RuntimeRunID == binding.runtimeRunID.String() &&
		state.OperationID == binding.operationID.String() && state.RequestDigest == binding.digest.String() &&
		state.AuthorityKind == binding.caller.kind && state.AuthorityID == binding.caller.id.String() &&
		state.AuthorityGeneration == binding.caller.generation && state.Action == binding.auditAction &&
		state.ReasonCode == binding.auditReasonCode && state.BeforeRevision == fact.PreviousRuntimeRevision &&
		state.AfterRevision == fact.ResultingRuntimeRevision && state.AfterState == fact.StateAtDecision &&
		state.BeforeOperationGeneration == binding.expectedOperationGeneration &&
		state.BeforeRuntimeFence == binding.expectedRuntimeFence && state.BeforeSafetyEpoch == binding.safetyEpoch &&
		state.RetryDisposition == fact.Retry && state.ReconciliationDisposition == fact.Reconciliation
}

func (authority *PostgresAuthority) recordIntegrityIncident(
	ctx context.Context,
	tx *sql.Tx,
	binding retainedCommandBindingValue,
	retainedKind int16,
	retainedDigest []byte,
	retainedGrantID string,
	retainedWorkItemID string,
	retainedGrantGeneration AdmissionGrantGeneration,
) error {
	if len(retainedDigest) != len(binding.digest) {
		return newError(ErrorIntegrityConflict)
	}
	scopeDigest := authorityScopeDigest(binding.workspaceID, binding.runtimeRunID)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		runtime_run_id, operation_id, retained_command_kind, attempted_command_kind,
		retained_request_digest, attempted_request_digest, retained_grant_id, attempted_grant_id,
		retained_work_item_id, attempted_work_item_id, retained_grant_generation,
		attempted_grant_generation, authority_scope_digest, observed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	ON CONFLICT DO NOTHING`, authority.table("runtime_execution_integrity_incidents")),
		binding.runtimeRunID.String(), binding.operationID.String(), retainedKind, binding.kind,
		retainedDigest, binding.digest[:], retainedGrantID, binding.admissionGrantID.String(),
		retainedWorkItemID, binding.admissionWorkItemID.String(), retainedGrantGeneration,
		binding.admissionGrantGeneration, scopeDigest[:], postgresTimestamp(authority.now()))
	if err != nil {
		return normalizeRuntimePersistenceFailure(err)
	}
	return nil
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
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
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
		return RuntimeSnapshot{}, normalizeRuntimePersistenceFailure(err)
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

func normalizePostgresPersistenceFailure(err error) error {
	return newPersistenceError(classifyPersistenceFailure(err))
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
