package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

const canonicalStartDomain = "slidesmith.runtime-execution.request/v1\n"

type ExecutionNodeConfig struct {
	ExecutionNodeID       ExecutionNodeID
	CapacityGeneration    NodeCapacityGeneration
	ResourceClassID       ResourceClassID
	ExecutionPolicyID     ExecutionPolicyID
	AvailableRuntimeSlots uint64
}

type AdmissionLimits struct {
	Global            uint64
	PersonalWorkspace uint64
	WorkerClass       uint64
	ResourceClass     uint64
}

type LocalAdmissionConfig struct {
	SchedulerEpoch SchedulerEpoch
	PolicyVersion  PolicyVersion
	GrantTTL       time.Duration
	Limits         AdmissionLimits
	Node           ExecutionNodeConfig
}

type PostgresConfig struct {
	Schema                                 string
	Now                                    func() time.Time
	Admission                              LocalAdmissionConfig
	RuntimeNodeFactFunction                string
	RuntimePhysicalReleaseEvidenceFunction string
}

type PostgresAuthority struct {
	db                                     *sql.DB
	schema                                 string
	now                                    func() time.Time
	admission                              LocalAdmissionConfig
	runtimeNodeFactFunction                string
	runtimePhysicalReleaseEvidenceFunction string
}

var _ Scheduling = (*PostgresAuthority)(nil)

func NewPostgresAuthority(db *sql.DB, config PostgresConfig) (*PostgresAuthority, error) {
	if db == nil || !validPostgresIdentifier(config.Schema) || !validAdmissionConfig(config.Admission) ||
		config.RuntimeNodeFactFunction != "" && !validPostgresQualifiedIdentifier(config.RuntimeNodeFactFunction) ||
		config.RuntimePhysicalReleaseEvidenceFunction != "" &&
			!validPostgresQualifiedIdentifier(config.RuntimePhysicalReleaseEvidenceFunction) {
		return nil, newError(ErrorInvalidRequest)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &PostgresAuthority{db: db, schema: config.Schema, now: now, admission: config.Admission,
		runtimeNodeFactFunction:                config.RuntimeNodeFactFunction,
		runtimePhysicalReleaseEvidenceFunction: config.RuntimePhysicalReleaseEvidenceFunction}, nil
}

func validAdmissionConfig(config LocalAdmissionConfig) bool {
	return config.SchedulerEpoch > 0 && config.PolicyVersion > 0 && config.GrantTTL > 0 &&
		config.Limits.Global > 0 && config.Limits.PersonalWorkspace > 0 &&
		config.Limits.WorkerClass > 0 && config.Limits.ResourceClass > 0 &&
		validOpaqueID(config.Node.ExecutionNodeID.value) && config.Node.CapacityGeneration > 0 &&
		validOpaqueID(config.Node.ResourceClassID.value) && validOpaqueID(config.Node.ExecutionPolicyID.value) &&
		config.Node.AvailableRuntimeSlots > 0
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
	parts := bytes.Split([]byte(value), []byte("."))
	return len(parts) == 2 && validPostgresIdentifier(string(parts[0])) &&
		validPostgresIdentifier(string(parts[1]))
}

func (authority *PostgresAuthority) table(name string) string { return authority.schema + "." + name }

// TaskEnqueueParticipant is the restricted adapter for Task Orchestration's
// existing transaction. It can invoke only the configured Scheduler-owned
// enqueue operation and receives no SQL or mutable Scheduler state.
func (authority *PostgresAuthority) TaskEnqueueParticipant() taskorchestration.SchedulerTransactionalParticipant {
	return taskorchestration.SchedulerTransactionalParticipantFunc(func(
		ctx context.Context,
		transaction taskorchestration.SchedulerTransaction,
		_ taskorchestration.SchedulerEnqueueFact,
	) error {
		return transaction.Enqueue(ctx)
	})
}

func (authority *PostgresAuthority) TaskEnqueueFunction() string {
	return authority.table("scheduler_enqueue_work_item")
}

func (authority *PostgresAuthority) RuntimeAcceptanceParticipant() runtimeexecution.SchedulerAcceptanceParticipant {
	return runtimeexecution.SchedulerAcceptanceParticipantFunc(func(
		ctx context.Context,
		transaction runtimeexecution.SchedulerAcceptanceTransaction,
		_ runtimeexecution.SchedulerAcceptanceFact,
	) (runtimeexecution.SchedulerGrantBinding, error) {
		return transaction.AcceptAndBind(ctx)
	})
}

func (authority *PostgresAuthority) RuntimeAcceptanceFunction() string {
	return authority.table("scheduler_accept_runtime_start")
}

func (authority *PostgresAuthority) RuntimeLeaseAttachmentParticipant() runtimeexecution.SchedulerLeaseAttachmentParticipant {
	return runtimeexecution.SchedulerLeaseAttachmentParticipantFunc(func(
		ctx context.Context,
		transaction runtimeexecution.SchedulerLeaseAttachmentTransaction,
		_ runtimeexecution.SchedulerLeaseAttachmentFact,
	) error {
		return transaction.AttachLease(ctx)
	})
}

func (authority *PostgresAuthority) RuntimeLeaseAttachmentFunction() string {
	return authority.table("scheduler_attach_runtime_lease")
}

func (authority *PostgresAuthority) RuntimeCancellationParticipant() runtimeexecution.SchedulerCancellationParticipant {
	return runtimeexecution.SchedulerCancellationParticipantFunc(func(
		ctx context.Context,
		transaction runtimeexecution.SchedulerCancellationTransaction,
		_ runtimeexecution.SchedulerCancellationFact,
	) error {
		return transaction.AcceptCancellation(ctx)
	})
}

func (authority *PostgresAuthority) RuntimeCancellationFunction() string {
	return authority.table("scheduler_accept_runtime_cancel")
}

func (authority *PostgresAuthority) Migrate(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return newError(ErrorDependencyUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, nil)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(750075)"); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	for _, statement := range authority.migrationStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return newError(ErrorDependencyUnavailable)
		}
	}
	if err := tx.Commit(); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	return nil
}

func (authority *PostgresAuthority) migrationStatements() []string {
	workItems := authority.table("scheduler_work_items")
	grants := authority.table("scheduler_admission_grants")
	claims := authority.table("scheduler_delivery_claims")
	logical := authority.table("scheduler_logical_reservations")
	node := authority.table("scheduler_node_reservations")
	physical := authority.table("scheduler_physical_occupancy")
	fairness := authority.table("scheduler_fairness_state")
	return []string{
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", authority.table("scheduler_grant_sequence")),
		fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s", authority.table("scheduler_claim_sequence")),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			work_item_id text PRIMARY KEY,
			work_item_generation bigint NOT NULL CHECK (work_item_generation > 0),
			operation_id text NOT NULL UNIQUE,
			personal_workspace_id text NOT NULL,
			task_id text NOT NULL,
			phase_run_id text NOT NULL,
			runtime_run_id text NOT NULL,
			decision_id text NOT NULL,
			task_revision bigint NOT NULL CHECK (task_revision > 0),
			kind smallint NOT NULL,
			runtime_request_kind smallint NOT NULL CHECK (runtime_request_kind BETWEEN 0 AND 2),
			payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
			canonical_payload bytea NOT NULL,
			activity_generation bigint NOT NULL CHECK (activity_generation > 0),
			fence_kind smallint NOT NULL,
			fence bigint NOT NULL CHECK (fence > 0),
			causation_id text NOT NULL,
			priority_class smallint NOT NULL,
			state smallint NOT NULL,
			current_grant_generation bigint NOT NULL DEFAULT 0 CHECK (current_grant_generation >= 0),
			last_grant_generation bigint NOT NULL DEFAULT 0 CHECK (last_grant_generation >= 0),
			accepted_decision_id text NOT NULL DEFAULT '',
			accepted_runtime_revision bigint NOT NULL DEFAULT 0 CHECK (accepted_runtime_revision >= 0),
			accepted_runtime_fence bigint NOT NULL DEFAULT 0 CHECK (accepted_runtime_fence >= 0),
			enqueued_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL
		)`, workItems),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			admission_grant_id text PRIMARY KEY,
			work_item_id text NOT NULL REFERENCES %s(work_item_id),
			work_item_generation bigint NOT NULL,
			generation bigint NOT NULL CHECK (generation > 0),
			delivery_claim_id text NOT NULL UNIQUE,
			operation_id text NOT NULL,
			payload_digest bytea NOT NULL CHECK (octet_length(payload_digest) = 32),
			runtime_run_id text NOT NULL,
			personal_workspace_id text NOT NULL,
			execution_node_id text NOT NULL,
			node_capacity_generation bigint NOT NULL CHECK (node_capacity_generation > 0),
			resource_class_id text NOT NULL,
			execution_policy_id text NOT NULL,
			scheduler_epoch bigint NOT NULL CHECK (scheduler_epoch > 0),
			policy_version bigint NOT NULL CHECK (policy_version > 0),
			expires_at timestamptz NOT NULL,
			state smallint NOT NULL,
			bound_decision_id text NOT NULL DEFAULT '',
			bound_runtime_revision bigint NOT NULL DEFAULT 0 CHECK (bound_runtime_revision >= 0),
			bound_runtime_fence bigint NOT NULL DEFAULT 0 CHECK (bound_runtime_fence >= 0),
			terminal_decision_id text NOT NULL DEFAULT '',
			terminal_runtime_revision bigint NOT NULL DEFAULT 0 CHECK (terminal_runtime_revision >= 0),
			terminal_runtime_fence bigint NOT NULL DEFAULT 0 CHECK (terminal_runtime_fence >= 0),
			terminal_scheduler_epoch bigint NOT NULL DEFAULT 0 CHECK (terminal_scheduler_epoch >= 0),
			terminal_policy_version bigint NOT NULL DEFAULT 0 CHECK (terminal_policy_version >= 0),
			lease_acquire_operation_id text NOT NULL DEFAULT '',
			lease_acquire_digest bytea CHECK (lease_acquire_digest IS NULL OR octet_length(lease_acquire_digest) = 32),
			lease_acquire_by timestamptz,
			sandbox_lease_id text NOT NULL DEFAULT '',
			lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
			lease_fence bigint NOT NULL DEFAULT 0 CHECK (lease_fence >= 0),
			lease_attached_at timestamptz,
			accepted_at timestamptz,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			UNIQUE (work_item_id, generation)
		)`, grants, workItems),
		fmt.Sprintf(`ALTER TABLE %s
			ADD COLUMN IF NOT EXISTS sandbox_lease_id text NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
			ADD COLUMN IF NOT EXISTS lease_fence bigint NOT NULL DEFAULT 0 CHECK (lease_fence >= 0),
			ADD COLUMN IF NOT EXISTS lease_attached_at timestamptz`, grants),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			delivery_claim_id text PRIMARY KEY,
			work_item_id text NOT NULL REFERENCES %s(work_item_id),
			grant_generation bigint NOT NULL CHECK (grant_generation > 0),
			claim_generation bigint NOT NULL CHECK (claim_generation > 0),
			state smallint NOT NULL,
			expires_at timestamptz NOT NULL,
			created_at timestamptz NOT NULL
		)`, claims, workItems),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			admission_grant_id text NOT NULL REFERENCES %s(admission_grant_id),
			counter_kind smallint NOT NULL,
			counter_key text NOT NULL,
			grant_generation bigint NOT NULL CHECK (grant_generation > 0),
			state smallint NOT NULL,
			updated_at timestamptz NOT NULL,
			PRIMARY KEY (admission_grant_id, counter_kind)
		)`, logical, grants),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			admission_grant_id text PRIMARY KEY REFERENCES %s(admission_grant_id),
			execution_node_id text NOT NULL,
			node_capacity_generation bigint NOT NULL CHECK (node_capacity_generation > 0),
			grant_generation bigint NOT NULL CHECK (grant_generation > 0),
			state smallint NOT NULL,
			updated_at timestamptz NOT NULL
		)`, node, grants),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			sandbox_lease_id text PRIMARY KEY,
			admission_grant_id text NOT NULL UNIQUE REFERENCES %s(admission_grant_id),
			work_item_id text NOT NULL,
			grant_generation bigint NOT NULL CHECK (grant_generation > 0),
			runtime_run_id text NOT NULL,
			start_operation_id text NOT NULL,
			start_digest bytea NOT NULL CHECK (octet_length(start_digest) = 32),
			lease_acquire_operation_id text NOT NULL,
			lease_acquire_digest bytea NOT NULL CHECK (octet_length(lease_acquire_digest) = 32),
			lease_generation bigint NOT NULL CHECK (lease_generation > 0),
			lease_fence bigint NOT NULL CHECK (lease_fence > 0),
			sandbox_id text NOT NULL,
			sandbox_generation bigint NOT NULL CHECK (sandbox_generation > 0),
			sandbox_fence bigint NOT NULL CHECK (sandbox_fence > 0),
			execution_node_id text NOT NULL,
			node_capacity_generation bigint NOT NULL CHECK (node_capacity_generation > 0),
			resource_class_id text NOT NULL,
			execution_policy_id text NOT NULL,
			scheduler_epoch bigint NOT NULL CHECK (scheduler_epoch > 0),
			policy_version bigint NOT NULL CHECK (policy_version > 0),
			state smallint NOT NULL,
			release_operation_id text NOT NULL DEFAULT '',
			release_operation_digest bytea CHECK (release_operation_digest IS NULL OR octet_length(release_operation_digest) = 32),
			reset_evidence_id text NOT NULL DEFAULT '',
			reset_evidence_digest bytea CHECK (reset_evidence_digest IS NULL OR octet_length(reset_evidence_digest) = 32),
			attached_at timestamptz NOT NULL,
			released_at timestamptz
		)`, physical, grants),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			singleton boolean PRIMARY KEY CHECK (singleton),
			last_personal_workspace_id text NOT NULL,
			updated_at timestamptz NOT NULL
		)`, fairness),
		fmt.Sprintf(`INSERT INTO %s (singleton, last_personal_workspace_id, updated_at)
			VALUES (TRUE, '', CURRENT_TIMESTAMP) ON CONFLICT (singleton) DO NOTHING`, fairness),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s(
			p_operation_id text, p_personal_workspace_id text, p_task_id text,
			p_phase_run_id text, p_runtime_run_id text,
			p_decision_id text, p_task_revision bigint, p_kind smallint, p_payload_digest bytea,
			p_canonical_payload bytea, p_runtime_request_kind smallint,
			p_activity_generation bigint, p_fence_kind smallint,
			p_fence bigint, p_causation_id text
		) RETURNS void LANGUAGE plpgsql AS $scheduler_enqueue$
		DECLARE
			retained %s%%ROWTYPE;
			candidate_id text := 'scheduler-work-item-' || p_operation_id;
		BEGIN
			INSERT INTO %s (
				work_item_id, work_item_generation, operation_id, personal_workspace_id,
				task_id, phase_run_id,
				runtime_run_id, decision_id, task_revision, kind, runtime_request_kind, payload_digest,
				canonical_payload, activity_generation, fence_kind, fence, causation_id,
				priority_class, state, enqueued_at, updated_at
			) VALUES (
				candidate_id, 1, p_operation_id, p_personal_workspace_id,
				p_task_id, p_phase_run_id, p_runtime_run_id,
				p_decision_id, p_task_revision, p_kind, p_runtime_request_kind,
				p_payload_digest, p_canonical_payload,
				p_activity_generation, p_fence_kind, p_fence, p_causation_id, 2, 1,
				CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
			) ON CONFLICT (operation_id) DO NOTHING;
			SELECT * INTO retained FROM %s WHERE operation_id = p_operation_id;
			IF retained.work_item_id IS NULL OR retained.work_item_id <> candidate_id OR
				retained.personal_workspace_id <> p_personal_workspace_id OR
				retained.task_id <> p_task_id OR retained.phase_run_id <> p_phase_run_id OR
				retained.runtime_run_id <> p_runtime_run_id OR retained.decision_id <> p_decision_id OR
				retained.task_revision <> p_task_revision OR retained.kind <> p_kind OR
				retained.runtime_request_kind <> p_runtime_request_kind OR
				retained.payload_digest <> p_payload_digest OR retained.canonical_payload <> p_canonical_payload OR
				retained.activity_generation <> p_activity_generation OR retained.fence_kind <> p_fence_kind OR
				retained.fence <> p_fence OR retained.causation_id <> p_causation_id THEN
				RAISE EXCEPTION 'scheduler enqueue binding conflict' USING ERRCODE = '23000';
			END IF;
		END
		$scheduler_enqueue$`, authority.TaskEnqueueFunction(), workItems, workItems, workItems),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s(
			p_work_item_id text, p_admission_grant_id text, p_grant_generation bigint,
			p_operation_id text, p_payload_digest bytea, p_runtime_run_id text,
			p_decision_id text, p_runtime_revision bigint, p_runtime_fence bigint,
			p_lease_acquire_operation_id text, p_lease_acquire_digest bytea,
			p_runtime_deadline timestamptz, p_resource_class_id text,
			p_execution_policy_id text, p_accepted_at timestamptz
		) RETURNS TABLE (
			execution_node_id text, node_capacity_generation bigint,
			resource_class_id text, execution_policy_id text,
			scheduler_epoch bigint, policy_version bigint,
			grant_expires_at timestamptz, lease_acquire_by timestamptz
		) LANGUAGE plpgsql AS $scheduler_accept$
		DECLARE
			retained_grant %s%%ROWTYPE;
			retained_work %s%%ROWTYPE;
			fixed_lease_acquire_by timestamptz;
		BEGIN
			SELECT * INTO retained_work FROM %s WHERE work_item_id=p_work_item_id FOR UPDATE;
			SELECT * INTO retained_grant FROM %s
				WHERE admission_grant_id=p_admission_grant_id AND work_item_id=p_work_item_id
				AND generation=p_grant_generation FOR UPDATE;
			IF retained_work.work_item_id IS NULL OR retained_grant.admission_grant_id IS NULL OR
				retained_work.operation_id <> p_operation_id OR retained_work.payload_digest <> p_payload_digest OR
				retained_work.runtime_run_id <> p_runtime_run_id OR retained_work.state <> 2 OR
				retained_work.current_grant_generation <> p_grant_generation OR
				retained_grant.operation_id <> p_operation_id OR retained_grant.payload_digest <> p_payload_digest OR
				retained_grant.runtime_run_id <> p_runtime_run_id OR retained_grant.state <> 1 OR
				retained_grant.resource_class_id <> p_resource_class_id OR
				retained_grant.execution_policy_id <> p_execution_policy_id OR
				retained_grant.expires_at <= p_accepted_at OR p_runtime_deadline <= p_accepted_at THEN
				RAISE EXCEPTION 'scheduler runtime acceptance binding conflict' USING ERRCODE = '23000';
			END IF;
			fixed_lease_acquire_by := LEAST(retained_grant.expires_at, p_runtime_deadline);
			UPDATE %s SET state=2, bound_decision_id=p_decision_id,
				bound_runtime_revision=p_runtime_revision, bound_runtime_fence=p_runtime_fence,
				lease_acquire_operation_id=p_lease_acquire_operation_id,
				lease_acquire_digest=p_lease_acquire_digest,
				lease_acquire_by=fixed_lease_acquire_by, accepted_at=p_accepted_at,
				updated_at=p_accepted_at
				WHERE admission_grant_id=p_admission_grant_id AND generation=p_grant_generation AND state=1;
			UPDATE %s SET state=3, accepted_decision_id=p_decision_id,
				accepted_runtime_revision=p_runtime_revision, accepted_runtime_fence=p_runtime_fence,
				updated_at=p_accepted_at
				WHERE work_item_id=p_work_item_id AND state=2 AND current_grant_generation=p_grant_generation;
			UPDATE %s SET state=2, updated_at=p_accepted_at
				WHERE admission_grant_id=p_admission_grant_id AND grant_generation=p_grant_generation AND state=1;
			UPDATE %s SET state=2, updated_at=p_accepted_at
				WHERE admission_grant_id=p_admission_grant_id AND grant_generation=p_grant_generation AND state=1;
			RETURN QUERY SELECT retained_grant.execution_node_id,
				retained_grant.node_capacity_generation, retained_grant.resource_class_id,
				retained_grant.execution_policy_id, retained_grant.scheduler_epoch,
				retained_grant.policy_version, retained_grant.expires_at, fixed_lease_acquire_by;
		END
		$scheduler_accept$`, authority.RuntimeAcceptanceFunction(), grants, workItems,
			workItems, grants, grants, workItems, logical, node),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s(
			p_work_item_id text, p_admission_grant_id text, p_grant_generation bigint,
			p_runtime_run_id text, p_start_operation_id text, p_start_digest bytea,
			p_runtime_revision bigint, p_runtime_fence bigint,
			p_lease_acquire_operation_id text, p_lease_acquire_digest bytea,
			p_sandbox_lease_id text, p_lease_generation bigint, p_lease_fence bigint,
			p_sandbox_id text, p_sandbox_generation bigint, p_sandbox_fence bigint,
			p_execution_node_id text, p_node_capacity_generation bigint,
			p_resource_class_id text, p_execution_policy_id text,
			p_scheduler_epoch bigint, p_policy_version bigint, p_attached_at timestamptz
		) RETURNS void LANGUAGE plpgsql AS $scheduler_attach$
		DECLARE
			retained_grant %s%%ROWTYPE;
			retained_occupancy %s%%ROWTYPE;
			affected integer;
		BEGIN
			SELECT * INTO retained_grant FROM %s
				WHERE admission_grant_id=p_admission_grant_id AND work_item_id=p_work_item_id
				AND generation=p_grant_generation FOR UPDATE;
			IF retained_grant.admission_grant_id IS NULL OR retained_grant.state NOT IN (%d,%d) OR
				retained_grant.runtime_run_id <> p_runtime_run_id OR
				retained_grant.operation_id <> p_start_operation_id OR retained_grant.payload_digest <> p_start_digest OR
				retained_grant.bound_runtime_revision >= p_runtime_revision OR
				retained_grant.bound_runtime_fence <> p_runtime_fence OR
				retained_grant.lease_acquire_operation_id <> p_lease_acquire_operation_id OR
				retained_grant.lease_acquire_digest <> p_lease_acquire_digest OR
				retained_grant.execution_node_id <> p_execution_node_id OR
				retained_grant.node_capacity_generation <> p_node_capacity_generation OR
				retained_grant.resource_class_id <> p_resource_class_id OR
				retained_grant.execution_policy_id <> p_execution_policy_id OR
				retained_grant.scheduler_epoch <> p_scheduler_epoch OR retained_grant.policy_version <> p_policy_version OR
				retained_grant.lease_acquire_by <= p_attached_at THEN
				RAISE EXCEPTION 'scheduler lease attachment binding conflict' USING ERRCODE = '23000';
			END IF;
			IF retained_grant.state = %d THEN
				SELECT * INTO retained_occupancy FROM %s WHERE admission_grant_id=p_admission_grant_id;
				IF retained_grant.sandbox_lease_id <> p_sandbox_lease_id OR
					retained_grant.lease_generation <> p_lease_generation OR retained_grant.lease_fence <> p_lease_fence OR
					retained_occupancy.sandbox_lease_id <> p_sandbox_lease_id OR
					retained_occupancy.sandbox_id <> p_sandbox_id OR
					retained_occupancy.sandbox_generation <> p_sandbox_generation OR
					retained_occupancy.sandbox_fence <> p_sandbox_fence OR retained_occupancy.state <> 1 THEN
					RAISE EXCEPTION 'scheduler lease attachment replay conflict' USING ERRCODE = '23000';
				END IF;
				RETURN;
			END IF;
			UPDATE %s SET state=%d, sandbox_lease_id=p_sandbox_lease_id,
				lease_generation=p_lease_generation, lease_fence=p_lease_fence,
				lease_attached_at=p_attached_at, updated_at=p_attached_at
				WHERE admission_grant_id=p_admission_grant_id AND generation=p_grant_generation AND state=2;
			GET DIAGNOSTICS affected = ROW_COUNT;
			IF affected <> 1 THEN RAISE EXCEPTION 'scheduler lease attachment conflict' USING ERRCODE = '23000'; END IF;
			UPDATE %s SET state=%d, updated_at=p_attached_at
				WHERE admission_grant_id=p_admission_grant_id AND grant_generation=p_grant_generation
				AND execution_node_id=p_execution_node_id AND node_capacity_generation=p_node_capacity_generation AND state=2;
			GET DIAGNOSTICS affected = ROW_COUNT;
			IF affected <> 1 THEN RAISE EXCEPTION 'scheduler node attachment conflict' USING ERRCODE = '23000'; END IF;
			INSERT INTO %s (
				sandbox_lease_id, admission_grant_id, work_item_id, grant_generation,
				runtime_run_id, start_operation_id, start_digest, lease_acquire_operation_id,
				lease_acquire_digest, lease_generation, lease_fence, sandbox_id,
				sandbox_generation, sandbox_fence, execution_node_id,
				node_capacity_generation, resource_class_id, execution_policy_id,
				scheduler_epoch, policy_version, state, attached_at
			) VALUES (p_sandbox_lease_id,p_admission_grant_id,p_work_item_id,p_grant_generation,
				p_runtime_run_id,p_start_operation_id,p_start_digest,p_lease_acquire_operation_id,
				p_lease_acquire_digest,p_lease_generation,p_lease_fence,p_sandbox_id,
				p_sandbox_generation,p_sandbox_fence,p_execution_node_id,
				p_node_capacity_generation,p_resource_class_id,p_execution_policy_id,
				p_scheduler_epoch,p_policy_version,%d,p_attached_at);
		END
		$scheduler_attach$`, authority.RuntimeLeaseAttachmentFunction(), grants, physical,
			grants, GrantBound, GrantLeaseAttached, GrantLeaseAttached, physical,
			grants, GrantLeaseAttached, node, ReservationLeaseAttached, physical, PhysicalOccupancyHeld),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s(
			p_operation_id text, p_payload_digest bytea, p_runtime_run_id text,
			p_decision_id text, p_runtime_revision bigint, p_runtime_fence bigint,
			p_accepted_at timestamptz
		) RETURNS void LANGUAGE plpgsql AS $scheduler_cancel$
		DECLARE
			affected integer;
		BEGIN
			IF p_runtime_revision <= 0 OR p_runtime_fence <= 0 OR p_decision_id = '' THEN
				RAISE EXCEPTION 'scheduler cancellation binding conflict' USING ERRCODE = '23000';
			END IF;
			UPDATE %s SET state=3, accepted_decision_id=p_decision_id,
				accepted_runtime_revision=p_runtime_revision, accepted_runtime_fence=p_runtime_fence,
				updated_at=p_accepted_at
				WHERE operation_id=p_operation_id AND payload_digest=p_payload_digest
				AND runtime_run_id=p_runtime_run_id AND runtime_request_kind=2 AND state=1
				AND current_grant_generation=0;
			GET DIAGNOSTICS affected = ROW_COUNT;
			IF affected <> 1 THEN
				RAISE EXCEPTION 'scheduler cancellation binding conflict' USING ERRCODE = '23000';
			END IF;
		END
		$scheduler_cancel$`, authority.RuntimeCancellationFunction(), workItems),
	}
}

type canonicalStartEnvelope struct {
	Kind                string    `json:"kind"`
	OperationID         string    `json:"operation_id"`
	PersonalWorkspaceID string    `json:"personal_workspace_id"`
	TaskID              string    `json:"task_id"`
	PhaseRunID          string    `json:"phase_run_id"`
	RuntimeRunID        string    `json:"runtime_run_id"`
	WorkerClass         string    `json:"worker_class"`
	ResourceClassID     string    `json:"resource_class_id"`
	ExecutionPolicyID   string    `json:"execution_policy_id"`
	Deadline            time.Time `json:"deadline"`
}

type lockedWorkItem struct {
	id                  WorkItemID
	generation          uint64
	operationID         string
	personalWorkspaceID string
	taskID              string
	phaseRunID          string
	runtimeRunID        string
	payloadDigest       Digest
	canonicalPayload    []byte
	state               WorkItemState
	currentGeneration   GrantGeneration
	lastGeneration      GrantGeneration
}

func (authority *PostgresAuthority) ClaimAndAdmit(ctx context.Context) (AdmissionDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return AdmissionDecision{}, newError(ErrorDependencyUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return AdmissionDecision{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()

	work, err := authority.lockAdmissionCandidate(ctx, tx)
	if err != nil {
		return AdmissionDecision{}, err
	}
	envelope, err := authority.validateCanonicalWork(work)
	if err != nil {
		return AdmissionDecision{}, err
	}
	now := authority.now().UTC().Truncate(time.Microsecond)
	if authority.runtimeNodeFactFunction != "" {
		var generation NodeCapacityGeneration
		var readiness runtimeexecution.NodeReadiness
		var quarantined bool
		var occupancy runtimeexecution.NodeOccupancy
		var containment runtimeexecution.ContainmentStatus
		var reset runtimeexecution.ResetStatus
		var attestationExpiresAt, authorizationExpiresAt time.Time
		err := tx.QueryRowContext(ctx, "SELECT * FROM "+authority.runtimeNodeFactFunction+"($1)",
			authority.admission.Node.ExecutionNodeID.String()).Scan(&generation, &readiness, &quarantined,
			&occupancy, &containment, &reset, &attestationExpiresAt, &authorizationExpiresAt)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (generation != authority.admission.Node.CapacityGeneration ||
			readiness != runtimeexecution.NodeReady || quarantined ||
			occupancy != runtimeexecution.NodeUnoccupied || containment != runtimeexecution.ContainmentEstablished ||
			reset != runtimeexecution.ResetCompleted || !now.Before(attestationExpiresAt) ||
			!now.Before(authorizationExpiresAt)) {
			return AdmissionDecision{}, newError(ErrorNoEligibleWork)
		}
		if err != nil {
			return AdmissionDecision{}, newError(ErrorDependencyUnavailable)
		}
	}
	if work.currentGeneration > 0 {
		grant, state, loadErr := authority.loadGrant(ctx, tx, work.id, work.currentGeneration)
		if loadErr != nil {
			return AdmissionDecision{}, loadErr
		}
		if state != GrantReservedUnbound {
			return AdmissionDecision{}, newError(ErrorIntegrityConflict)
		}
		if grant.ExpiresAt.After(now) {
			decision := admissionDecision(work, grant)
			if err := tx.Commit(); err != nil {
				return AdmissionDecision{}, newError(ErrorDependencyUnavailable)
			}
			return decision, nil
		}
		if err := authority.expireUnboundGrant(ctx, tx, work, grant, now); err != nil {
			return AdmissionDecision{}, err
		}
		work.state = WorkItemQueued
		work.currentGeneration = 0
	}
	if !envelope.Deadline.After(now) {
		return AdmissionDecision{}, newError(ErrorNoEligibleWork)
	}
	for _, limit := range []struct {
		kind  uint8
		key   string
		limit uint64
	}{
		{kind: 1, key: "site", limit: authority.admission.Limits.Global},
		{kind: 2, key: envelope.PersonalWorkspaceID, limit: authority.admission.Limits.PersonalWorkspace},
		{kind: 3, key: envelope.WorkerClass, limit: authority.admission.Limits.WorkerClass},
		{kind: 4, key: envelope.ResourceClassID, limit: authority.admission.Limits.ResourceClass},
	} {
		occupied, countErr := authority.logicalReservationOccupancy(ctx, tx, limit.kind, limit.key)
		if countErr != nil {
			return AdmissionDecision{}, countErr
		}
		if occupied >= limit.limit {
			return AdmissionDecision{}, newError(ErrorNoEligibleWork)
		}
	}
	var occupied uint64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		(SELECT count(*) FROM %s WHERE execution_node_id=$1 AND node_capacity_generation=$2 AND state IN ($3,$4)) +
		(SELECT count(*) FROM %s WHERE execution_node_id=$1 AND node_capacity_generation=$2 AND state=$5)`,
		authority.table("scheduler_node_reservations"), authority.table("scheduler_physical_occupancy")),
		authority.admission.Node.ExecutionNodeID.value, authority.admission.Node.CapacityGeneration,
		ReservationReservedUnbound, ReservationBound, PhysicalOccupancyHeld).Scan(&occupied); err != nil {
		return AdmissionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if occupied >= authority.admission.Node.AvailableRuntimeSlots {
		return AdmissionDecision{}, newError(ErrorNoEligibleWork)
	}
	grant, err := authority.reserveAdmission(ctx, tx, work, envelope, now)
	if err != nil {
		return AdmissionDecision{}, err
	}
	decision := admissionDecision(work, grant)
	if err := tx.Commit(); err != nil {
		return AdmissionDecision{}, newError(ErrorDependencyUnavailable)
	}
	return decision, nil
}

func (authority *PostgresAuthority) ClaimCancellation(
	ctx context.Context,
) (CancellationDecision, error) {
	if ctx == nil || ctx.Err() != nil {
		return CancellationDecision{}, newError(ErrorDependencyUnavailable)
	}
	tx, err := authority.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CancellationDecision{}, newError(ErrorDependencyUnavailable)
	}
	defer func() { _ = tx.Rollback() }()
	var decision CancellationDecision
	var personalWorkspaceID string
	var digest []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT work_item_id, personal_workspace_id,
		operation_id, task_id, phase_run_id, runtime_run_id, payload_digest, canonical_payload
		FROM %s WHERE state=$1 AND runtime_request_kind=2
		ORDER BY enqueued_at, work_item_id LIMIT 1 FOR UPDATE`,
		authority.table("scheduler_work_items")), WorkItemQueued).Scan(
		&decision.WorkItemID.value, &personalWorkspaceID, &decision.OperationID,
		&decision.TaskID, &decision.PhaseRunID, &decision.RuntimeRunID, &digest, &decision.CanonicalPayload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CancellationDecision{}, newError(ErrorNoEligibleWork)
	}
	if err != nil || len(digest) != sha256.Size {
		return CancellationDecision{}, newError(ErrorDependencyUnavailable)
	}
	copy(decision.PayloadDigest[:], digest)
	decision.PersonalWorkspaceID, err = NewPersonalWorkspaceID(personalWorkspaceID)
	if err != nil {
		return CancellationDecision{}, newError(ErrorIntegrityConflict)
	}
	cancel, err := runtimeexecution.ParseCanonicalCancelPayload(
		decision.CanonicalPayload, runtimeexecution.Digest(decision.PayloadDigest),
	)
	if err != nil || cancel.OperationID.String() != decision.OperationID ||
		cancel.PersonalWorkspaceID.String() != personalWorkspaceID || cancel.TaskID.String() != decision.TaskID ||
		cancel.PhaseRunID.String() != decision.PhaseRunID || cancel.RuntimeRunID.String() != decision.RuntimeRunID {
		return CancellationDecision{}, newError(ErrorIntegrityConflict)
	}
	decision.CanonicalPayload = append([]byte(nil), decision.CanonicalPayload...)
	if err := tx.Commit(); err != nil {
		return CancellationDecision{}, newError(ErrorDependencyUnavailable)
	}
	return decision, nil
}

func (authority *PostgresAuthority) lockAdmissionCandidate(ctx context.Context, tx *sql.Tx) (lockedWorkItem, error) {
	var lastPersonalWorkspaceID string
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT last_personal_workspace_id FROM %s
		WHERE singleton=TRUE FOR UPDATE`, authority.table("scheduler_fairness_state"))).Scan(
		&lastPersonalWorkspaceID,
	); err != nil {
		return lockedWorkItem{}, newError(ErrorDependencyUnavailable)
	}
	var work lockedWorkItem
	var digest []byte
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT work_item_id, work_item_generation,
		operation_id, personal_workspace_id, task_id, phase_run_id, runtime_run_id, payload_digest, canonical_payload,
		state, current_grant_generation, last_grant_generation FROM %s
		WHERE state=$1 AND runtime_request_kind=1 AND octet_length(canonical_payload) > 0
		ORDER BY enqueued_at, work_item_id LIMIT 1 FOR UPDATE`,
		authority.table("scheduler_work_items")), WorkItemDelivering).Scan(
		&work.id.value, &work.generation, &work.operationID, &work.personalWorkspaceID, &work.taskID, &work.phaseRunID,
		&work.runtimeRunID, &digest, &work.canonicalPayload, &work.state,
		&work.currentGeneration, &work.lastGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT work_item_id, work_item_generation,
			operation_id, personal_workspace_id, task_id, phase_run_id, runtime_run_id, payload_digest, canonical_payload,
			state, current_grant_generation, last_grant_generation FROM %s AS work
			WHERE work.state=$1 AND work.runtime_request_kind=1 AND octet_length(canonical_payload) > 0 AND (
				SELECT count(*) FROM %s AS reservation
				WHERE reservation.counter_kind=2
				AND reservation.counter_key=work.personal_workspace_id
				AND reservation.state IN ($2,$3)
			) < $4
			ORDER BY CASE WHEN personal_workspace_id > $5 THEN 0 ELSE 1 END,
				personal_workspace_id, enqueued_at, work_item_id
			LIMIT 1 FOR UPDATE`, authority.table("scheduler_work_items"),
			authority.table("scheduler_logical_reservations")), WorkItemQueued,
			ReservationReservedUnbound, ReservationBound, authority.admission.Limits.PersonalWorkspace,
			lastPersonalWorkspaceID).Scan(
			&work.id.value, &work.generation, &work.operationID, &work.personalWorkspaceID, &work.taskID, &work.phaseRunID,
			&work.runtimeRunID, &digest, &work.canonicalPayload, &work.state,
			&work.currentGeneration, &work.lastGeneration,
		)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return lockedWorkItem{}, newError(ErrorNoEligibleWork)
	}
	if err != nil || len(digest) != sha256.Size {
		return lockedWorkItem{}, newError(ErrorDependencyUnavailable)
	}
	copy(work.payloadDigest[:], digest)
	return work, nil
}

func (authority *PostgresAuthority) logicalReservationOccupancy(
	ctx context.Context,
	tx *sql.Tx,
	kind uint8,
	key string,
) (uint64, error) {
	var occupied uint64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s
		WHERE counter_kind=$1 AND counter_key=$2 AND state IN ($3,$4)`,
		authority.table("scheduler_logical_reservations")), kind, key,
		ReservationReservedUnbound, ReservationBound).Scan(&occupied); err != nil {
		return 0, newError(ErrorDependencyUnavailable)
	}
	return occupied, nil
}

func (authority *PostgresAuthority) validateCanonicalWork(work lockedWorkItem) (canonicalStartEnvelope, error) {
	want := sha256.Sum256(append([]byte(canonicalStartDomain), work.canonicalPayload...))
	if !bytes.Equal(want[:], work.payloadDigest[:]) {
		return canonicalStartEnvelope{}, newError(ErrorIntegrityConflict)
	}
	var envelope canonicalStartEnvelope
	if err := json.Unmarshal(work.canonicalPayload, &envelope); err != nil ||
		envelope.Kind != "start_runtime_run" || envelope.OperationID != work.operationID ||
		envelope.TaskID != work.taskID || envelope.PhaseRunID != work.phaseRunID ||
		envelope.RuntimeRunID != work.runtimeRunID || !validOpaqueID(envelope.PersonalWorkspaceID) ||
		envelope.PersonalWorkspaceID != work.personalWorkspaceID ||
		envelope.ResourceClassID != authority.admission.Node.ResourceClassID.value ||
		envelope.ExecutionPolicyID != authority.admission.Node.ExecutionPolicyID.value ||
		!validOpaqueID(envelope.WorkerClass) || envelope.Deadline.IsZero() {
		return canonicalStartEnvelope{}, newError(ErrorIntegrityConflict)
	}
	return envelope, nil
}

func (authority *PostgresAuthority) reserveAdmission(
	ctx context.Context,
	tx *sql.Tx,
	work lockedWorkItem,
	envelope canonicalStartEnvelope,
	now time.Time,
) (AdmissionGrant, error) {
	generation := work.lastGeneration + 1
	var grantSequence, claimSequence uint64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("scheduler_grant_sequence")+"')").Scan(&grantSequence); err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+authority.table("scheduler_claim_sequence")+"')").Scan(&claimSequence); err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	grant := AdmissionGrant{
		AdmissionGrantID: AdmissionGrantID{value: fmt.Sprintf("admission-grant-%012d", grantSequence)},
		WorkItemID:       work.id, Generation: generation,
		DeliveryClaimID:        DeliveryClaimID{value: fmt.Sprintf("delivery-claim-%012d", claimSequence)},
		ExecutionNodeID:        authority.admission.Node.ExecutionNodeID,
		NodeCapacityGeneration: authority.admission.Node.CapacityGeneration,
		ResourceClassID:        authority.admission.Node.ResourceClassID,
		ExecutionPolicyID:      authority.admission.Node.ExecutionPolicyID,
		SchedulerEpoch:         authority.admission.SchedulerEpoch, PolicyVersion: authority.admission.PolicyVersion,
		ExpiresAt: earlier(now.Add(authority.admission.GrantTTL), envelope.Deadline.UTC()),
	}
	if !grant.ExpiresAt.After(now) {
		return AdmissionGrant{}, newError(ErrorNoEligibleWork)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		admission_grant_id, work_item_id, work_item_generation, generation, delivery_claim_id,
		operation_id, payload_digest, runtime_run_id, personal_workspace_id, execution_node_id,
		node_capacity_generation, resource_class_id, execution_policy_id, scheduler_epoch,
		policy_version, expires_at, state, created_at, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)`,
		authority.table("scheduler_admission_grants")), grant.AdmissionGrantID.value, work.id.value,
		work.generation, generation, grant.DeliveryClaimID.value, work.operationID, work.payloadDigest[:],
		work.runtimeRunID, envelope.PersonalWorkspaceID, grant.ExecutionNodeID.value,
		grant.NodeCapacityGeneration, grant.ResourceClassID.value, grant.ExecutionPolicyID.value,
		grant.SchedulerEpoch, grant.PolicyVersion, grant.ExpiresAt, GrantReservedUnbound, now); err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		delivery_claim_id, work_item_id, grant_generation, claim_generation, state, expires_at, created_at
	) VALUES ($1,$2,$3,$3,$4,$5,$6)`, authority.table("scheduler_delivery_claims")),
		grant.DeliveryClaimID.value, work.id.value, generation, GrantReservedUnbound, grant.ExpiresAt, now); err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	counters := []struct {
		kind uint8
		key  string
	}{{1, "site"}, {2, envelope.PersonalWorkspaceID}, {3, envelope.WorkerClass}, {4, envelope.ResourceClassID}}
	for _, counter := range counters {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
			admission_grant_id, counter_kind, counter_key, grant_generation, state, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6)`, authority.table("scheduler_logical_reservations")),
			grant.AdmissionGrantID.value, counter.kind, counter.key, generation, GrantReservedUnbound, now); err != nil {
			return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (
		admission_grant_id, execution_node_id, node_capacity_generation, grant_generation, state, updated_at
	) VALUES ($1,$2,$3,$4,$5,$6)`, authority.table("scheduler_node_reservations")),
		grant.AdmissionGrantID.value, grant.ExecutionNodeID.value, grant.NodeCapacityGeneration,
		generation, GrantReservedUnbound, now); err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1,
		current_grant_generation=$2, last_grant_generation=$2, updated_at=$3
		WHERE work_item_id=$4 AND state=$5 AND current_grant_generation=0 AND last_grant_generation=$6`,
		authority.table("scheduler_work_items")), WorkItemDelivering, generation, now,
		work.id.value, WorkItemQueued, work.lastGeneration)
	if err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return AdmissionGrant{}, newError(ErrorIntegrityConflict)
	}
	result, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET
		last_personal_workspace_id=$1, updated_at=$2 WHERE singleton=TRUE`,
		authority.table("scheduler_fairness_state")), work.personalWorkspaceID, now)
	if err != nil {
		return AdmissionGrant{}, newError(ErrorDependencyUnavailable)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return AdmissionGrant{}, newError(ErrorIntegrityConflict)
	}
	return grant, nil
}

func (authority *PostgresAuthority) expireUnboundGrant(
	ctx context.Context,
	tx *sql.Tx,
	work lockedWorkItem,
	grant AdmissionGrant,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=$2
		WHERE admission_grant_id=$3 AND generation=$4 AND state=$5`,
		authority.table("scheduler_admission_grants")), GrantExpiredUnbound, now,
		grant.AdmissionGrantID.value, grant.Generation, GrantReservedUnbound)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	for _, table := range []string{"scheduler_logical_reservations", "scheduler_node_reservations"} {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1, updated_at=$2
			WHERE admission_grant_id=$3 AND grant_generation=$4 AND state=$5`, authority.table(table)),
			GrantReleased, now, grant.AdmissionGrantID.value, grant.Generation, GrantReservedUnbound); err != nil {
			return newError(ErrorDependencyUnavailable)
		}
	}
	result, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET state=$1,
		current_grant_generation=0, updated_at=$2 WHERE work_item_id=$3 AND state=$4
		AND current_grant_generation=$5 AND last_grant_generation=$5`, authority.table("scheduler_work_items")),
		WorkItemQueued, now, work.id.value, WorkItemDelivering, grant.Generation)
	if err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	rows, err = result.RowsAffected()
	if err != nil || rows != 1 {
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (authority *PostgresAuthority) loadGrant(
	ctx context.Context,
	tx *sql.Tx,
	workItemID WorkItemID,
	generation GrantGeneration,
) (AdmissionGrant, GrantState, error) {
	var grant AdmissionGrant
	var state GrantState
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT admission_grant_id, delivery_claim_id,
		execution_node_id, node_capacity_generation, resource_class_id, execution_policy_id,
		scheduler_epoch, policy_version, expires_at, state FROM %s
		WHERE work_item_id=$1 AND generation=$2`, authority.table("scheduler_admission_grants")),
		workItemID.value, generation).Scan(&grant.AdmissionGrantID.value, &grant.DeliveryClaimID.value,
		&grant.ExecutionNodeID.value, &grant.NodeCapacityGeneration, &grant.ResourceClassID.value,
		&grant.ExecutionPolicyID.value, &grant.SchedulerEpoch, &grant.PolicyVersion, &grant.ExpiresAt, &state)
	if err != nil {
		return AdmissionGrant{}, 0, newError(ErrorIntegrityConflict)
	}
	grant.WorkItemID = workItemID
	grant.Generation = generation
	grant.ExpiresAt = grant.ExpiresAt.UTC()
	return grant, state, nil
}

func admissionDecision(work lockedWorkItem, grant AdmissionGrant) AdmissionDecision {
	personalWorkspaceID, _ := NewPersonalWorkspaceID(work.personalWorkspaceID)
	return AdmissionDecision{
		WorkItemID: work.id, PersonalWorkspaceID: personalWorkspaceID,
		OperationID: work.operationID, TaskID: work.taskID,
		PhaseRunID: work.phaseRunID, RuntimeRunID: work.runtimeRunID,
		PayloadDigest: work.payloadDigest, CanonicalPayload: append([]byte(nil), work.canonicalPayload...), Grant: grant,
	}
}

func (authority *PostgresAuthority) Inspect(ctx context.Context, ref WorkItemRef) (WorkItemView, error) {
	if ctx == nil || ctx.Err() != nil || !validOpaqueID(ref.WorkItemID.value) || !ref.Scope.valid() {
		return WorkItemView{}, newError(ErrorInvalidRequest)
	}
	var view WorkItemView
	var digest []byte
	err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT work_item_id, operation_id,
		task_id, phase_run_id, runtime_run_id, payload_digest, state FROM %s
		WHERE work_item_id=$1 AND ($2 OR personal_workspace_id=$3)`,
		authority.table("scheduler_work_items")), ref.WorkItemID.value, ref.Scope.administrator(),
		ref.Scope.personalWorkspaceID.String()).Scan(&view.WorkItemID.value,
		&view.OperationID, &view.TaskID, &view.PhaseRunID, &view.RuntimeRunID, &digest, &view.State)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItemView{}, newError(ErrorAuthorizationDenied)
	}
	if err != nil || len(digest) != sha256.Size {
		return WorkItemView{}, newError(ErrorDependencyUnavailable)
	}
	copy(view.PayloadDigest[:], digest)
	var generation GrantGeneration
	err = authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT generation FROM %s
		WHERE work_item_id=$1 ORDER BY generation DESC LIMIT 1`, authority.table("scheduler_admission_grants")),
		ref.WorkItemID.value).Scan(&generation)
	if err == nil {
		grant, state, loadErr := authority.loadGrantOutsideTransaction(ctx, ref.WorkItemID, generation)
		if loadErr != nil {
			return WorkItemView{}, loadErr
		}
		view.Grant = GrantView{AdmissionGrant: grant, State: state}
	} else if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	} else {
		return WorkItemView{}, newError(ErrorDependencyUnavailable)
	}
	var logicalMin, logicalMax, nodeState ReservationState
	if err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT min(state), max(state)
		FROM %s WHERE admission_grant_id=$1`, authority.table("scheduler_logical_reservations")),
		view.Grant.AdmissionGrantID.value).Scan(&logicalMin, &logicalMax); err != nil {
		return WorkItemView{}, newError(ErrorDependencyUnavailable)
	}
	if logicalMin != logicalMax {
		return WorkItemView{}, newError(ErrorIntegrityConflict)
	}
	if err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT state FROM %s
		WHERE admission_grant_id=$1`, authority.table("scheduler_node_reservations")),
		view.Grant.AdmissionGrantID.value).Scan(&nodeState); err != nil {
		return WorkItemView{}, newError(ErrorDependencyUnavailable)
	}
	view.LogicalReservation = logicalMin
	view.SelectedNodeReservation = nodeState
	if view.Grant.State == GrantLeaseAttached || view.Grant.State == GrantReleased {
		var physicalState PhysicalOccupancyState
		err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT state FROM %s
			WHERE admission_grant_id=$1`, authority.table("scheduler_physical_occupancy")),
			view.Grant.AdmissionGrantID.value).Scan(&physicalState)
		if err == nil {
			view.PhysicalOccupancy = physicalState
		} else if !errors.Is(err, sql.ErrNoRows) {
			return WorkItemView{}, newError(ErrorDependencyUnavailable)
		}
	}
	return view, nil
}

func (authority *PostgresAuthority) loadGrantOutsideTransaction(
	ctx context.Context,
	workItemID WorkItemID,
	generation GrantGeneration,
) (AdmissionGrant, GrantState, error) {
	var grant AdmissionGrant
	var state GrantState
	err := authority.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT admission_grant_id, delivery_claim_id,
		execution_node_id, node_capacity_generation, resource_class_id, execution_policy_id,
		scheduler_epoch, policy_version, expires_at, state FROM %s
		WHERE work_item_id=$1 AND generation=$2`, authority.table("scheduler_admission_grants")),
		workItemID.value, generation).Scan(&grant.AdmissionGrantID.value, &grant.DeliveryClaimID.value,
		&grant.ExecutionNodeID.value, &grant.NodeCapacityGeneration, &grant.ResourceClassID.value,
		&grant.ExecutionPolicyID.value, &grant.SchedulerEpoch, &grant.PolicyVersion, &grant.ExpiresAt, &state)
	if err != nil {
		return AdmissionGrant{}, 0, newError(ErrorIntegrityConflict)
	}
	grant.WorkItemID = workItemID
	grant.Generation = generation
	grant.ExpiresAt = grant.ExpiresAt.UTC()
	return grant, state, nil
}

func earlier(left, right time.Time) time.Time {
	if left.Before(right) {
		return left.UTC()
	}
	return right.UTC()
}
