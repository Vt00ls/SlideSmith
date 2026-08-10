package runtimeexecution

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// installContractSchedulerFunctions creates the minimal restricted Scheduler
// SQL functions used by the four-way contract suite. The suite drives the
// public Execute/Inspect seam without a full Scheduler schema; these functions
// validate the exact proposal and bind the already-issued Admission Grant
// deterministically, preserving the participant's restricted surface.
func installContractSchedulerFunctions(t *testing.T, db *sql.DB, schema string) {
	t.Helper()
	statements := []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s.contract_scheduler_accept(
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
		) LANGUAGE plpgsql AS $contract_accept$
		BEGIN
			IF p_work_item_id IS NULL OR p_admission_grant_id IS NULL OR
				p_grant_generation <= 0 OR p_operation_id IS NULL OR
				octet_length(p_payload_digest) <> 32 OR p_runtime_run_id IS NULL OR
				p_decision_id IS NULL OR p_runtime_revision <= 0 OR p_runtime_fence <= 0 THEN
				RAISE EXCEPTION 'contract scheduler accept binding conflict' USING ERRCODE = '23000';
			END IF;
			RETURN QUERY SELECT
				'contract-node-' || p_admission_grant_id,
				1::bigint,
				p_resource_class_id,
				p_execution_policy_id,
				1::bigint,
				1::bigint,
				p_accepted_at + interval '15 minutes',
				LEAST(p_accepted_at + interval '15 minutes', p_runtime_deadline);
		END
		$contract_accept$`, schema),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s.contract_scheduler_attach_lease(
			p_work_item_id text, p_admission_grant_id text, p_grant_generation bigint,
			p_runtime_run_id text, p_start_operation_id text, p_start_digest bytea,
			p_runtime_revision bigint, p_runtime_fence bigint,
			p_lease_acquire_operation_id text, p_lease_acquire_digest bytea,
			p_sandbox_lease_id text, p_lease_generation bigint, p_lease_fence bigint,
			p_sandbox_id text, p_sandbox_generation bigint, p_sandbox_fence bigint,
			p_execution_node_id text, p_node_capacity_generation bigint,
			p_resource_class_id text, p_execution_policy_id text,
			p_scheduler_epoch bigint, p_policy_version bigint, p_attached_at timestamptz
		) RETURNS void LANGUAGE plpgsql AS $contract_attach$
		BEGIN
			IF p_work_item_id IS NULL OR p_admission_grant_id IS NULL OR
				p_grant_generation <= 0 OR p_runtime_run_id IS NULL OR
				octet_length(p_start_digest) <> 32 OR p_runtime_revision <= 0 OR
				p_runtime_fence <= 0 OR p_sandbox_lease_id IS NULL OR
				p_lease_generation <= 0 OR p_lease_fence <= 0 OR
				p_execution_node_id IS NULL OR p_node_capacity_generation <= 0 OR
				p_scheduler_epoch <= 0 OR p_policy_version <= 0 THEN
				RAISE EXCEPTION 'contract scheduler attach lease conflict' USING ERRCODE = '23000';
			END IF;
		END
		$contract_attach$`, schema),
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s.contract_scheduler_cancel(
			p_operation_id text, p_canonical_digest bytea, p_runtime_run_id text,
			p_decision_id text, p_runtime_revision bigint, p_runtime_fence bigint,
			p_cancelled_at timestamptz
		) RETURNS void LANGUAGE plpgsql AS $contract_cancel$
		BEGIN
			IF p_operation_id IS NULL OR octet_length(p_canonical_digest) <> 32 OR
				p_runtime_run_id IS NULL OR p_decision_id IS NULL OR
				p_runtime_revision <= 0 OR p_runtime_fence <= 0 THEN
				RAISE EXCEPTION 'contract scheduler cancel conflict' USING ERRCODE = '23000';
			END IF;
		END
		$contract_cancel$`, schema),
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("install contract scheduler function: %v", err)
		}
	}
}
