package taskorchestration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/runtimeexecution"
	"github.com/slidesmith/slidesmith/backend/internal/scheduler"
)

func TestPostgresIssue76MigrationsUpgradeEmptyIssue75AuthorityTables(t *testing.T) {
	db, schema := isolatedPostgresSchema(t)
	statements := []string{
		fmt.Sprintf(`CREATE TABLE %s.scheduler_work_items (
			work_item_id text PRIMARY KEY, work_item_generation bigint NOT NULL,
			operation_id text NOT NULL UNIQUE, personal_workspace_id text NOT NULL,
			task_id text NOT NULL, phase_run_id text NOT NULL, runtime_run_id text NOT NULL,
			decision_id text NOT NULL, task_revision bigint NOT NULL, kind smallint NOT NULL,
			runtime_request_kind smallint NOT NULL, payload_digest bytea NOT NULL,
			canonical_payload bytea NOT NULL, activity_generation bigint NOT NULL,
			fence_kind smallint NOT NULL, fence bigint NOT NULL, causation_id text NOT NULL,
			priority_class smallint NOT NULL, state smallint NOT NULL,
			current_grant_generation bigint NOT NULL DEFAULT 0,
			last_grant_generation bigint NOT NULL DEFAULT 0,
			accepted_decision_id text NOT NULL DEFAULT '',
			accepted_runtime_revision bigint NOT NULL DEFAULT 0,
			accepted_runtime_fence bigint NOT NULL DEFAULT 0,
			enqueued_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
		)`, schema),
		fmt.Sprintf(`CREATE TABLE %s.scheduler_admission_grants (
			admission_grant_id text PRIMARY KEY,
			work_item_id text NOT NULL REFERENCES %s.scheduler_work_items(work_item_id),
			work_item_generation bigint NOT NULL, generation bigint NOT NULL,
			delivery_claim_id text NOT NULL UNIQUE, operation_id text NOT NULL,
			payload_digest bytea NOT NULL, runtime_run_id text NOT NULL,
			personal_workspace_id text NOT NULL, execution_node_id text NOT NULL,
			node_capacity_generation bigint NOT NULL, resource_class_id text NOT NULL,
			execution_policy_id text NOT NULL, scheduler_epoch bigint NOT NULL,
			policy_version bigint NOT NULL, expires_at timestamptz NOT NULL, state smallint NOT NULL,
			bound_decision_id text NOT NULL DEFAULT '', bound_runtime_revision bigint NOT NULL DEFAULT 0,
			bound_runtime_fence bigint NOT NULL DEFAULT 0, terminal_decision_id text NOT NULL DEFAULT '',
			terminal_runtime_revision bigint NOT NULL DEFAULT 0,
			terminal_runtime_fence bigint NOT NULL DEFAULT 0,
			terminal_scheduler_epoch bigint NOT NULL DEFAULT 0,
			terminal_policy_version bigint NOT NULL DEFAULT 0,
			lease_acquire_operation_id text NOT NULL DEFAULT '', lease_acquire_digest bytea,
			lease_acquire_by timestamptz, accepted_at timestamptz,
			created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			UNIQUE (work_item_id, generation)
		)`, schema, schema),
		fmt.Sprintf(`CREATE TABLE %s.runtime_execution_runtimes (
			runtime_run_id text PRIMARY KEY, personal_workspace_id text NOT NULL,
			task_id text NOT NULL, phase_run_id text NOT NULL, owner_authority_id text NOT NULL,
			owner_authority_generation bigint NOT NULL, owner_authority_kind smallint NOT NULL,
			task_revision bigint NOT NULL, runtime_revision bigint NOT NULL,
			operation_generation bigint NOT NULL, runtime_fence bigint NOT NULL,
			safety_epoch bigint NOT NULL, runtime_state smallint NOT NULL,
			runtime_outcome smallint NOT NULL, terminal_evidence_id text NOT NULL DEFAULT '',
			aggregate_state jsonb NOT NULL, updated_at timestamptz NOT NULL
		)`, schema),
		fmt.Sprintf(`CREATE TABLE %s.runtime_execution_prelease_leases (
			lease_acquire_operation_id text PRIMARY KEY, lease_acquire_digest bytea NOT NULL,
			runtime_run_id text NOT NULL UNIQUE REFERENCES %s.runtime_execution_runtimes(runtime_run_id),
			start_operation_id text NOT NULL, start_digest bytea NOT NULL, work_item_id text NOT NULL,
			admission_grant_id text NOT NULL, grant_generation bigint NOT NULL,
			execution_node_id text NOT NULL, node_capacity_generation bigint NOT NULL,
			resource_class_id text NOT NULL, execution_policy_id text NOT NULL,
			scheduler_epoch bigint NOT NULL, policy_version bigint NOT NULL, safety_epoch bigint NOT NULL,
			sandbox_lease_id text NOT NULL UNIQUE, lease_generation bigint NOT NULL,
			lease_fence bigint NOT NULL, committed_at timestamptz NOT NULL
		)`, schema, schema),
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("install issue-75 authority table: %v", err)
		}
	}

	nodeID, _ := scheduler.NewExecutionNodeID("migration-node")
	resourceClassID, _ := scheduler.NewResourceClassID("migration-class")
	executionPolicyID, _ := scheduler.NewExecutionPolicyID("migration-policy")
	scheduling, err := scheduler.NewPostgresAuthority(db, scheduler.PostgresConfig{
		Schema: schema, Admission: scheduler.LocalAdmissionConfig{
			SchedulerEpoch: 1, PolicyVersion: 1, GrantTTL: time.Minute,
			Limits: scheduler.AdmissionLimits{Global: 1, PersonalWorkspace: 1, WorkerClass: 1, ResourceClass: 1},
			Node: scheduler.ExecutionNodeConfig{ExecutionNodeID: nodeID, CapacityGeneration: 1,
				ResourceClassID: resourceClassID, ExecutionPolicyID: executionPolicyID, AvailableRuntimeSlots: 1},
		},
	})
	if err != nil {
		t.Fatalf("create Scheduler authority for issue-75 upgrade: %v", err)
	}
	if err := scheduling.Migrate(context.Background()); err != nil {
		t.Fatalf("upgrade issue-75 Scheduler schema: %v", err)
	}
	runtimeAuthority, err := runtimeexecution.NewPostgresAuthority(db, runtimeexecution.PostgresConfig{Schema: schema})
	if err != nil {
		t.Fatalf("create Runtime authority for issue-75 upgrade: %v", err)
	}
	if err := runtimeAuthority.Migrate(context.Background()); err != nil {
		t.Fatalf("upgrade issue-75 Runtime schema: %v", err)
	}
	if err := scheduling.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat Scheduler migration: %v", err)
	}
	if err := runtimeAuthority.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat Runtime migration: %v", err)
	}

	assertPostgresColumns(t, db, schema, "scheduler_admission_grants", []string{
		"sandbox_lease_id", "lease_generation", "lease_fence", "lease_attached_at",
	}, false)
	assertPostgresColumns(t, db, schema, "runtime_execution_prelease_leases", []string{
		"lease_disposition", "lease_expires_at", "sandbox_id", "sandbox_generation", "sandbox_fence",
		"worker_authority_id", "worker_generation", "node_authority_id", "authorization_generation",
		"authorization_expires_at", "catalog_safety_epoch",
	}, true)
}

func assertPostgresColumns(
	t *testing.T,
	db interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	schema string,
	table string,
	columns []string,
	requireNotNull bool,
) {
	t.Helper()
	for _, column := range columns {
		var nullable string
		if err := db.QueryRowContext(context.Background(), `SELECT is_nullable FROM information_schema.columns
			WHERE table_schema=$1 AND table_name=$2 AND column_name=$3`, schema, table, column).Scan(&nullable); err != nil {
			t.Fatalf("inspect migrated %s.%s: %v", table, column, err)
		}
		if requireNotNull && nullable != "NO" {
			t.Fatalf("migrated %s.%s is nullable", table, column)
		}
	}
}

func TestPostgresRuntimeInspectReconstructsExactActiveLeaseAndNodeOccupancy(t *testing.T) {
	now := time.Date(2026, time.July, 29, 16, 0, 0, 0, time.UTC)
	ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{
			Disposition: runtimeexecution.LeaseAcquisitionReady,
		}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
	work := system.enqueueAndAdmitRuntime(t, "issue-76-active-lease")

	accepted, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("acquire exact lease: %v", err)
	}
	inspected, err := system.runtime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion:       runtimeexecution.SchemaV1,
		ProjectionVersion:   runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID,
		RuntimeRunID:        work.start.RuntimeRunID,
		Authority:           work.start.Authority,
	})
	if err != nil {
		t.Fatalf("inspect exact lease: %v", err)
	}
	if inspected != accepted.Snapshot {
		t.Fatalf("Execute and Inspect disagree: execute=%+v inspect=%+v", accepted.Snapshot, inspected)
	}
	if inspected.Lease.AcquireStatus != runtimeexecution.LeaseGranted ||
		inspected.Lease.Disposition != runtimeexecution.LeaseActive ||
		inspected.Lease.LeaseID.String() == "" || inspected.Lease.SandboxID.String() == "" ||
		inspected.Lease.Generation != 1 || inspected.Lease.Fence != 1 ||
		inspected.Lease.SandboxGeneration == 0 || inspected.Lease.SandboxFence == 0 ||
		inspected.Lease.WorkerAuthorityID.String() == "" || inspected.Lease.NodeAuthorityID.String() == "" ||
		!inspected.Lease.ExpiresAt.After(now) {
		t.Fatalf("Inspect lost exact active lease authority: %+v", inspected.Lease)
	}
	if inspected.Node.ExecutionNodeID != inspected.Operation.ExecutionNodeID ||
		inspected.Node.Generation != runtimeexecution.NodeGeneration(inspected.Operation.NodeCapacityGeneration) ||
		inspected.Node.Readiness != runtimeexecution.NodeReady ||
		inspected.Node.Occupancy != runtimeexecution.NodeOccupied || inspected.Node.Quarantined ||
		inspected.Node.Containment != runtimeexecution.ContainmentPending ||
		inspected.Node.Reset != runtimeexecution.ResetRequired ||
		inspected.Capacity.Physical != runtimeexecution.PhysicalCapacityOccupied {
		t.Fatalf("Inspect lost authoritative node occupancy: node=%+v capacity=%+v", inspected.Node, inspected.Capacity)
	}
	restarted, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
		Schema: system.schema, Now: system.clock.Now,
		SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
		SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
		SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
		SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
		SchedulerCancellationParticipant:    system.scheduling.RuntimeCancellationParticipant(),
		SchedulerCancellationFunction:       system.scheduling.RuntimeCancellationFunction(),
		LeaseAcquisition:                    ready,
		RuntimeBindingValidator:             system.runtimeBinding,
	})
	if err != nil {
		t.Fatalf("restart Runtime Execution authority: %v", err)
	}
	reconstructed, err := restarted.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
		SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		Authority: work.start.Authority,
	})
	if err != nil || reconstructed != inspected {
		t.Fatalf("restart lost exact lease/node state: reconstructed=%+v err=%v want=%+v", reconstructed, err, inspected)
	}
}

func TestPostgresLeaseRenewalIsExactReplayAndStaleAuthorityFailsClosed(t *testing.T) {
	now := time.Date(2026, time.July, 29, 17, 0, 0, 0, time.UTC)
	ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
	work := system.enqueueAndAdmitRuntime(t, "issue-76-renew")
	acquired, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	lease, node := acquired.Snapshot.Lease, acquired.Snapshot.Node
	operationID, err := runtimeexecution.NewOperationID("issue-76-renew-current")
	if err != nil {
		t.Fatal(err)
	}
	catalogEpoch := runtimeexecution.CatalogSafetyEpoch(0)
	if work.start.CatalogBinding != nil {
		catalogEpoch = work.start.CatalogBinding.SafetyEpoch
	}
	renew, err := runtimeexecution.NewRenewSandboxLease(runtimeexecution.RenewSandboxLeaseInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: operationID,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
		AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
		Authority: runtimeexecution.NewLeaseRenewalAuthority(lease.WorkerAuthorityID,
			lease.WorkerGeneration, lease.NodeAuthorityID, lease.AuthorizationGeneration),
		ReleaseSafetyEpoch: work.start.ReleaseSafetyEpoch, CatalogSafetyEpoch: catalogEpoch,
		RequestedExpiresAt: now.Add(150 * time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("construct renewal: %v", err)
	}
	first, err := system.runtime.Maintain(context.Background(), renew)
	if err != nil {
		t.Fatalf("renew current lease: %v", err)
	}
	if first.Lease.Generation != lease.Generation+1 || first.Lease.Fence != lease.Fence+1 ||
		!first.Lease.ExpiresAt.Equal(now.Add(150*time.Second)) || first.Replayed {
		t.Fatalf("renewal did not advance exact authority: %+v", first)
	}
	replayed, err := system.runtime.Maintain(context.Background(), renew)
	if err != nil || !replayed.Replayed || replayed.Lease != first.Lease ||
		replayed.RuntimeRevision != first.RuntimeRevision || replayed.RuntimeFence != first.RuntimeFence {
		t.Fatalf("exact renewal replay changed authority: first=%+v replay=%+v err=%v", first, replayed, err)
	}
	staleOperationID, err := runtimeexecution.NewOperationID("issue-76-renew-stale")
	if err != nil {
		t.Fatal(err)
	}
	staleInput := renew.RenewSandboxLeaseInput
	staleInput.OperationID = staleOperationID
	staleInput.RequestedExpiresAt = now.Add(180 * time.Second)
	stale, err := runtimeexecution.NewRenewSandboxLease(staleInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = system.runtime.Maintain(context.Background(), stale)
	assertRuntimeExecutionErrorCode(t, err, runtimeexecution.ErrorIntegrityConflict)
}

func TestPostgresPostLeaseCancelFencesBeforeCleanupWithoutPhysicalRelease(t *testing.T) {
	now := time.Date(2026, time.July, 29, 17, 30, 0, 0, time.UTC)
	ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
	work := system.enqueueAndAdmitRuntime(t, "issue-76-post-lease-cancel")
	acquired, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	cancel := newPostgresPreLeaseCancel(t, work.start, acquired.Snapshot.RuntimeRevision,
		acquired.Snapshot.Operation.Generation, acquired.Snapshot.RuntimeFence, now)
	cancelled, err := system.runtime.Execute(context.Background(), cancel)
	if err != nil {
		t.Fatalf("cancel leased Runtime: %v", err)
	}
	if cancelled.Snapshot.State != runtimeexecution.RuntimeTerminal ||
		cancelled.Snapshot.Outcome != runtimeexecution.RuntimeCancelled ||
		cancelled.Snapshot.Lease.Disposition != runtimeexecution.LeaseRevoked ||
		cancelled.Snapshot.Lease.Generation != acquired.Snapshot.Lease.Generation+1 ||
		cancelled.Snapshot.Lease.Fence != acquired.Snapshot.Lease.Fence+1 ||
		cancelled.Snapshot.Node.Occupancy != runtimeexecution.NodeOccupancyUnknown ||
		!cancelled.Snapshot.Node.Quarantined ||
		cancelled.Snapshot.Cleanup.Status != runtimeexecution.LeaseCleanupPending ||
		cancelled.Snapshot.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityReleaseReady ||
		cancelled.Snapshot.Capacity.NoLease != runtimeexecution.NoLeaseDispositionNone ||
		cancelled.Snapshot.Capacity.Physical != runtimeexecution.PhysicalCapacityUnknownOrQuarantined ||
		cancelled.Snapshot.CapacityEvidence.NoLeasePhysicalDisposition != (runtimeexecution.NoLeasePhysicalDispositionEvidence{}) ||
		cancelled.Snapshot.CapacityEvidence.PhysicalCapacityReleaseReady != (runtimeexecution.PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("leased cancel crossed fencing/evidence authority: %+v", cancelled.Snapshot)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(),
		cancelled.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal); err != nil {
		t.Fatalf("logical release rejected exact leased terminal evidence: %v", err)
	}
	fencedEvidence := cancelled.Snapshot.CapacityEvidence.RuntimeFencedOrTerminal
	forgedNoLease := runtimeexecution.NoLeasePhysicalDispositionEvidence{
		WorkItemID: fencedEvidence.WorkItemID, AdmissionGrantID: fencedEvidence.AdmissionGrantID,
		GrantGeneration: fencedEvidence.GrantGeneration, RuntimeRunID: fencedEvidence.RuntimeRunID,
		StartOperationID: fencedEvidence.StartOperationID, StartDigest: fencedEvidence.StartDigest,
		TerminalDecisionID: fencedEvidence.TerminalDecisionID, RuntimeRevision: fencedEvidence.RuntimeRevision,
		RuntimeFence: fencedEvidence.RuntimeFence, SchedulerEpoch: fencedEvidence.SchedulerEpoch,
		PolicyVersion:           fencedEvidence.PolicyVersion,
		LeaseAcquireOperationID: fencedEvidence.LeaseAcquireOperationID,
		LeaseAcquireDigest:      fencedEvidence.LeaseAcquireDigest,
		ExecutionNodeID:         cancelled.Snapshot.Operation.ExecutionNodeID,
		NodeCapacityGeneration:  cancelled.Snapshot.Operation.NodeCapacityGeneration,
	}
	err = system.scheduling.ApplyNoLeasePhysicalDisposition(context.Background(), forgedNoLease)
	assertIssue76SchedulerCode(t, err, scheduler.ErrorIntegrityConflict)
	workspaceID, err := scheduler.NewPersonalWorkspaceID(work.start.PersonalWorkspaceID.String())
	if err != nil {
		t.Fatal(err)
	}
	view, err := system.scheduling.Inspect(context.Background(), scheduler.WorkItemRef{
		WorkItemID: work.canonical.WorkItemID, Scope: scheduler.NewOwnerWorkItemQueryScope(workspaceID),
	})
	if err != nil || view.Grant.State != scheduler.GrantLeaseAttached ||
		view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationLeaseAttached ||
		view.PhysicalOccupancy != scheduler.PhysicalOccupancyHeld {
		t.Fatalf("logical evidence changed physical occupancy: view=%+v err=%v", view, err)
	}
}

func TestPostgresNodeLossReleasesLogicalCountersButKeepsPhysicalOccupancyQuarantined(t *testing.T) {
	now := time.Date(2026, time.July, 29, 17, 45, 0, 0, time.UTC)
	ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
	})
	system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
	work := system.enqueueAndAdmitRuntime(t, "issue-76-node-loss-logical-release")
	acquired, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil {
		t.Fatal(err)
	}
	lease, node := acquired.Snapshot.Lease, acquired.Snapshot.Node
	operationID, _ := runtimeexecution.NewOperationID("issue-76-node-loss-fence")
	fence, err := runtimeexecution.NewFenceSandboxLease(runtimeexecution.FenceSandboxLeaseInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: operationID,
		PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
		ExpectedRuntimeFence: acquired.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
		Reason: runtimeexecution.LeaseFenceNodeLost, Authority: system.fencingAuthority,
		ReleaseSafetyEpoch: work.start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.runtime.Maintain(context.Background(), fence); err != nil {
		t.Fatal(err)
	}
	inspected := inspectPostgresRuntime(t, system.runtime, work.start)
	if inspected.Capacity.LogicalRelease != runtimeexecution.LogicalCapacityReleaseReady ||
		inspected.Capacity.Physical != runtimeexecution.PhysicalCapacityUnknownOrQuarantined ||
		inspected.Node.Readiness != runtimeexecution.NodeUnavailable || !inspected.Node.Quarantined ||
		inspected.CapacityEvidence.RuntimeFencedOrTerminal.RuntimeRunID != work.start.RuntimeRunID ||
		inspected.CapacityEvidence.NoLeasePhysicalDisposition != (runtimeexecution.NoLeasePhysicalDispositionEvidence{}) ||
		inspected.CapacityEvidence.PhysicalCapacityReleaseReady != (runtimeexecution.PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("node loss crossed Runtime capacity authorities: %+v", inspected)
	}
	if err := system.scheduling.ApplyRuntimeFencedOrTerminal(context.Background(),
		inspected.CapacityEvidence.RuntimeFencedOrTerminal); err != nil {
		t.Fatalf("Scheduler rejected exact node-loss logical evidence: %v", err)
	}
	workspaceID, _ := scheduler.NewPersonalWorkspaceID(work.start.PersonalWorkspaceID.String())
	view, err := system.scheduling.Inspect(context.Background(), scheduler.WorkItemRef{
		WorkItemID: work.canonical.WorkItemID, Scope: scheduler.NewOwnerWorkItemQueryScope(workspaceID),
	})
	if err != nil || view.Grant.State != scheduler.GrantLeaseAttached ||
		view.LogicalReservation != scheduler.ReservationReleased ||
		view.SelectedNodeReservation != scheduler.ReservationLeaseAttached ||
		view.PhysicalOccupancy != scheduler.PhysicalOccupancyHeld {
		t.Fatalf("node-loss evidence released physical capacity: view=%+v err=%v", view, err)
	}
}

func TestPostgresRevokeResetPhysicalReleaseAndPoolReuseStayExactlyFenced(t *testing.T) {
	now := time.Date(2026, time.July, 29, 18, 0, 0, 0, time.UTC)
	ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
		context.Context,
		runtimeexecution.LeaseAcquisitionRequest,
	) (runtimeexecution.LeaseAcquisitionObservation, error) {
		return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
	})
	system := newPostgresRuntimeAdmissionSystemWithLimits(t, now, 10*time.Minute,
		structuralIssue76Limits(), 1, ready, nil)
	firstWork := system.enqueueAndAdmitRuntime(t, "issue-76-reuse-first")
	first, err := system.runtime.Execute(context.Background(), firstWork.start)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	lease, node := first.Snapshot.Lease, first.Snapshot.Node
	poolCanary := newIssue76SandboxPoolCanaryAdapter(lease, node.ExecutionNodeID, firstWork.start.OperationID)
	poolCanary.Inject(issue76SandboxResidueCanaries{
		TaskWorkspaceBytes:     "prior-task-workspace-bytes-canary",
		Secrets:                "prior-secret-capability-canary",
		WritableCacheMutations: "prior-writable-cache-mutation-canary",
		LogsAndTranscripts:     "prior-log-transcript-canary",
		Evidence:               "prior-runtime-evidence-canary",
		MainProcessState:       "prior-main-process-state-canary",
		ChildProcessState:      "prior-child-process-state-canary",
		NetworkState:           "prior-network-state-canary",
		PriorOperationIdentity: firstWork.start.OperationID.String(),
	})
	securityAuthorityID, _ := runtimeexecution.NewAuthorityID("issue-76-security-authority")
	wrongSecurityAuthorityID, _ := runtimeexecution.NewAuthorityID("issue-76-wrong-security-authority")
	for index, rejectedAuthority := range []runtimeexecution.LeaseFencingAuthority{
		runtimeexecution.NewSecurityLeaseFencingAuthority(wrongSecurityAuthorityID, 3),
		runtimeexecution.NewSecurityLeaseFencingAuthority(securityAuthorityID, 2),
	} {
		operationID, _ := runtimeexecution.NewOperationID(fmt.Sprintf("issue-76-rejected-fence-%d", index))
		rejected, constructErr := runtimeexecution.NewFenceSandboxLease(runtimeexecution.FenceSandboxLeaseInput{
			SchemaVersion: runtimeexecution.SchemaV1, OperationID: operationID,
			PersonalWorkspaceID: firstWork.start.PersonalWorkspaceID, RuntimeRunID: firstWork.start.RuntimeRunID,
			ExpectedRuntimeFence: first.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
			LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
			ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
			Reason: runtimeexecution.LeaseFenceRevoked, Authority: rejectedAuthority,
			ReleaseSafetyEpoch: firstWork.start.ReleaseSafetyEpoch, OccurredAt: now,
		})
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := system.runtime.Maintain(context.Background(), rejected)
		assertRuntimeExecutionErrorCode(t, maintainErr, runtimeexecution.ErrorAuthorizationDenied)
	}
	if afterRejected := inspectPostgresRuntime(t, system.runtime, firstWork.start); afterRejected != first.Snapshot {
		t.Fatalf("rejected fencing authority changed Runtime: got=%+v want=%+v", afterRejected, first.Snapshot)
	}
	fenceOperationID, err := runtimeexecution.NewOperationID("issue-76-revoke-first")
	if err != nil {
		t.Fatal(err)
	}
	revoke, err := runtimeexecution.NewFenceSandboxLease(runtimeexecution.FenceSandboxLeaseInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: fenceOperationID,
		PersonalWorkspaceID: firstWork.start.PersonalWorkspaceID, RuntimeRunID: firstWork.start.RuntimeRunID,
		ExpectedRuntimeFence: first.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
		LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
		Reason:             runtimeexecution.LeaseFenceRevoked,
		Authority:          system.fencingAuthority,
		ReleaseSafetyEpoch: firstWork.start.ReleaseSafetyEpoch, OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("construct revoke: %v", err)
	}
	fenced, err := system.runtime.Maintain(context.Background(), revoke)
	if err != nil {
		t.Fatalf("revoke lease: %v", err)
	}
	if fenced.Lease.Disposition != runtimeexecution.LeaseRevoked ||
		fenced.Lease.Generation != lease.Generation+1 || fenced.Lease.Fence != lease.Fence+1 ||
		fenced.RuntimeFence != first.Snapshot.RuntimeFence+1 || !fenced.Node.Quarantined ||
		fenced.Node.Occupancy != runtimeexecution.NodeOccupancyUnknown ||
		fenced.Cleanup.Status != runtimeexecution.LeaseCleanupPending ||
		fenced.PhysicalCapacityReleaseReady != (runtimeexecution.PhysicalCapacityReleaseReadyEvidence{}) {
		t.Fatalf("revoke did not fence before immutable cleanup: %+v", fenced)
	}

	system.enqueueRuntime(t, "issue-76-reuse-second")
	_, err = system.scheduling.ClaimAndAdmit(context.Background())
	assertIssue76SchedulerCode(t, err, scheduler.ErrorNoEligibleWork)

	resetOperationID, err := runtimeexecution.NewOperationID("issue-76-reset-first")
	if err != nil {
		t.Fatal(err)
	}
	resetEvidenceID, err := runtimeexecution.NewEvidenceID("issue-76-reset-evidence")
	if err != nil {
		t.Fatal(err)
	}
	resetInput := runtimeexecution.ConfirmSandboxResetInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: resetOperationID,
		PersonalWorkspaceID: firstWork.start.PersonalWorkspaceID, RuntimeRunID: firstWork.start.RuntimeRunID,
		ExpectedRuntimeFence: fenced.RuntimeFence, SandboxLeaseID: fenced.Lease.LeaseID,
		LeaseGeneration: fenced.Lease.Generation, LeaseFence: fenced.Lease.Fence,
		SandboxID: fenced.Lease.SandboxID, SandboxGeneration: fenced.Lease.SandboxGeneration,
		SandboxFence: fenced.Lease.SandboxFence, ExecutionNodeID: node.ExecutionNodeID,
		NodeGeneration: node.Generation, Authority: system.resetAuthority,
		EvidenceID: resetEvidenceID, EvidenceDigest: runtimeexecution.Digest{31: 76},
		OccurredAt: now,
	}
	forgedBeforeReset := runtimeexecution.PhysicalCapacityReleaseReadyEvidence{
		WorkItemID:       first.Snapshot.Operation.WorkItemID,
		AdmissionGrantID: first.Snapshot.Operation.AdmissionGrantID,
		GrantGeneration:  first.Snapshot.Operation.GrantGeneration,
		RuntimeRunID:     firstWork.start.RuntimeRunID, StartOperationID: firstWork.start.OperationID,
		StartDigest: firstWork.start.CanonicalRequestDigest, ReleaseOperationID: resetOperationID,
		ReleaseOperationDigest: runtimeexecution.Digest{31: 75}, RuntimeRevision: fenced.RuntimeRevision + 1,
		RuntimeFence: fenced.RuntimeFence, SandboxLeaseID: fenced.Lease.LeaseID,
		LeaseGeneration: fenced.Lease.Generation + 1, LeaseFence: fenced.Lease.Fence + 1,
		SandboxID: fenced.Lease.SandboxID, SandboxGeneration: fenced.Lease.SandboxGeneration,
		SandboxFence: fenced.Lease.SandboxFence + 1, ExecutionNodeID: node.ExecutionNodeID,
		NodeCapacityGeneration: first.Snapshot.Operation.NodeCapacityGeneration,
		ResetEvidenceID:        resetEvidenceID, ResetEvidenceDigest: resetInput.EvidenceDigest,
	}
	if err := poolCanary.ApplyPhysicalRelease(forgedBeforeReset); err == nil {
		t.Fatal("physical-release evidence erased sandbox residue before independently observed cleanup")
	}
	if poolCanary.Observe() == (issue76SandboxResidueCanaries{}) {
		t.Fatal("rejected pre-reset physical release erased sandbox residue")
	}
	err = system.scheduling.ApplyPhysicalCapacityReleaseReady(context.Background(), forgedBeforeReset)
	assertIssue76SchedulerCode(t, err, scheduler.ErrorIntegrityConflict)
	for index, omitted := range allIssue76SandboxResidueSurfaces {
		probe := newIssue76SandboxPoolCanaryAdapter(lease, node.ExecutionNodeID, firstWork.start.OperationID)
		probe.Inject(poolCanary.Observe())
		for _, surface := range allIssue76SandboxResidueSurfaces {
			if surface != omitted {
				probe.ApplyObservedCleanup(surface)
			}
		}
		probe.ObserveContainmentAndReset()
		incompleteInput := probe.BindObservedResetEvidence(resetInput)
		incompleteInput.OperationID, _ = runtimeexecution.NewOperationID(fmt.Sprintf("issue-76-incomplete-reset-%d", index))
		if issue76ResetEvidenceClaimsSurfaceClean(incompleteInput, omitted) {
			t.Fatalf("omitted cleanup surface %s was not observable in reset evidence", omitted)
		}
		incomplete, constructErr := runtimeexecution.NewConfirmSandboxReset(incompleteInput)
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := system.runtime.Maintain(context.Background(), incomplete)
		assertRuntimeExecutionErrorCode(t, maintainErr, runtimeexecution.ErrorIntegrityConflict)
		if probe.Observe() == (issue76SandboxResidueCanaries{}) {
			t.Fatalf("omitted cleanup surface %s was erased by another reset step", omitted)
		}
	}
	for _, surface := range allIssue76SandboxResidueSurfaces {
		poolCanary.ApplyObservedCleanup(surface)
	}
	poolCanary.ObserveContainmentAndReset()
	resetInput = poolCanary.BindObservedResetEvidence(resetInput)
	if poolCanary.Observe() != (issue76SandboxResidueCanaries{}) {
		t.Fatalf("complete observed cleanup retained sandbox residue: %+v", poolCanary.Observe())
	}
	cleanupAuthorityID, _ := runtimeexecution.NewAuthorityID("issue-76-cleanup-authority")
	wrongCleanupAuthorityID, _ := runtimeexecution.NewAuthorityID("issue-76-wrong-cleanup-authority")
	for index, rejectedAuthority := range []runtimeexecution.SandboxResetAuthority{
		runtimeexecution.NewSandboxResetAuthority(wrongCleanupAuthorityID, 4),
		runtimeexecution.NewSandboxResetAuthority(cleanupAuthorityID, 3),
	} {
		rejectedInput := resetInput
		rejectedInput.OperationID, _ = runtimeexecution.NewOperationID(fmt.Sprintf("issue-76-rejected-reset-%d", index))
		rejectedInput.Authority = rejectedAuthority
		rejected, constructErr := runtimeexecution.NewConfirmSandboxReset(rejectedInput)
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := system.runtime.Maintain(context.Background(), rejected)
		assertRuntimeExecutionErrorCode(t, maintainErr, runtimeexecution.ErrorAuthorizationDenied)
	}

	reset, err := runtimeexecution.NewConfirmSandboxReset(resetInput)
	if err != nil {
		t.Fatal(err)
	}
	released, err := system.runtime.Maintain(context.Background(), reset)
	if err != nil {
		t.Fatalf("confirm complete reset: %v", err)
	}
	evidence := released.PhysicalCapacityReleaseReady
	if released.Lease.Disposition != runtimeexecution.LeaseReleased ||
		released.Cleanup.Status != runtimeexecution.LeaseCleanupCompleted ||
		released.Node.Occupancy != runtimeexecution.NodeUnoccupied || !released.Node.Quarantined ||
		evidence.SandboxLeaseID != lease.LeaseID || evidence.SandboxID != lease.SandboxID ||
		evidence.ResetEvidenceID != resetEvidenceID {
		t.Fatalf("complete reset did not publish exact release evidence: %+v", released)
	}
	if err := poolCanary.ApplyPhysicalRelease(evidence); err != nil {
		t.Fatalf("test sandbox pool rejected exact physical-release evidence: %v", err)
	}
	replayedReset, err := system.runtime.Maintain(context.Background(), reset)
	if err != nil || !replayedReset.Replayed ||
		replayedReset.PhysicalCapacityReleaseReady != evidence || replayedReset.Lease != released.Lease {
		t.Fatalf("duplicate reset evidence changed release authority: replay=%+v err=%v", replayedReset, err)
	}
	beforeRestart := inspectPostgresRuntime(t, system.runtime, firstWork.start)
	afterRestart := inspectPostgresRuntime(t, system.restartRuntime(t), firstWork.start)
	if afterRestart != beforeRestart ||
		afterRestart.CapacityEvidence.PhysicalCapacityReleaseReady != evidence {
		t.Fatalf("restart lost reset-bound physical evidence: before=%+v after=%+v", beforeRestart, afterRestart)
	}
	wrongRelease := evidence
	wrongRelease.ResetEvidenceDigest = runtimeexecution.Digest{31: 77}
	err = system.scheduling.ApplyPhysicalCapacityReleaseReady(context.Background(), wrongRelease)
	assertIssue76SchedulerCode(t, err, scheduler.ErrorIntegrityConflict)
	if err := system.scheduling.ApplyPhysicalCapacityReleaseReady(context.Background(), evidence); err != nil {
		t.Fatalf("apply exact physical release: %v", err)
	}
	if err := system.scheduling.ApplyPhysicalCapacityReleaseReady(context.Background(), evidence); err != nil {
		t.Fatalf("replay exact physical release: %v", err)
	}

	returnAttestationID, err := runtimeexecution.NewNodeAttestationID("issue-76-return-attestation")
	if err != nil {
		t.Fatal(err)
	}
	returnOperationID, err := runtimeexecution.NewOperationID("issue-76-return-node")
	if err != nil {
		t.Fatal(err)
	}
	returnNodeAuthorityID, err := runtimeexecution.NewNodeAuthorityID("issue-76-return-node-authority")
	if err != nil {
		t.Fatal(err)
	}
	returnWorkerAuthorityID, err := runtimeexecution.NewWorkerAuthorityID("issue-76-return-worker-authority")
	if err != nil {
		t.Fatal(err)
	}
	catalogEpoch := runtimeexecution.CatalogSafetyEpoch(0)
	if firstWork.start.CatalogBinding != nil {
		catalogEpoch = firstWork.start.CatalogBinding.SafetyEpoch
	}
	attest, err := runtimeexecution.NewAttestExecutionNode(runtimeexecution.AttestExecutionNodeInput{
		SchemaVersion: runtimeexecution.SchemaV1, OperationID: returnOperationID,
		Authority:       system.attestationAuthority,
		ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
		AttestationID: returnAttestationID, AttestationGeneration: node.AttestationGeneration + 1,
		AttestedAt: now, ExpiresAt: now.Add(24 * time.Hour), ResourceClassID: firstWork.start.ResourceClassID,
		ExecutionPolicyID: firstWork.start.ExecutionPolicyID, NodeAuthorityID: returnNodeAuthorityID,
		WorkerAuthorityID: returnWorkerAuthorityID, WorkerGeneration: lease.WorkerGeneration + 1,
		AuthorizationGeneration: lease.AuthorizationGeneration + 1,
		AuthorizationExpiresAt:  now.Add(24 * time.Hour), ReleaseSafetyEpoch: firstWork.start.ReleaseSafetyEpoch,
		CatalogSafetyEpoch: catalogEpoch, ResetEvidenceID: resetEvidenceID,
		ResetEvidenceDigest: resetInput.EvidenceDigest, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveryAuthorityID, _ := runtimeexecution.NewAuthorityID("issue-76-recovery-authority")
	wrongRecoveryAuthorityID, _ := runtimeexecution.NewAuthorityID("issue-76-wrong-recovery-authority")
	for index, rejectedAuthority := range []runtimeexecution.NodeAttestationAuthority{
		runtimeexecution.NewRecoveryNodeAttestationAuthority(wrongRecoveryAuthorityID, 5),
		runtimeexecution.NewRecoveryNodeAttestationAuthority(recoveryAuthorityID, 4),
	} {
		rejectedInput := attest.AttestExecutionNodeInput
		rejectedInput.OperationID, _ = runtimeexecution.NewOperationID(fmt.Sprintf("issue-76-rejected-attestation-%d", index))
		rejectedInput.Authority = rejectedAuthority
		rejected, constructErr := runtimeexecution.NewAttestExecutionNode(rejectedInput)
		if constructErr != nil {
			t.Fatal(constructErr)
		}
		_, maintainErr := system.runtime.Maintain(context.Background(), rejected)
		assertRuntimeExecutionErrorCode(t, maintainErr, runtimeexecution.ErrorAuthorizationDenied)
	}
	returned, err := system.runtime.Maintain(context.Background(), attest)
	if err != nil || returned.Node.Readiness != runtimeexecution.NodeReady || returned.Node.Quarantined {
		t.Fatalf("fresh attestation did not restore node: decision=%+v err=%v", returned, err)
	}
	for _, expected := range []struct {
		operationID string
		kind        runtimeexecution.RuntimeMaintenanceAuthorityKind
		authorityID string
		generation  uint64
	}{
		{fenceOperationID.String(), runtimeexecution.MaintenanceAuthoritySecurity, securityAuthorityID.String(), 3},
		{resetOperationID.String(), runtimeexecution.MaintenanceAuthorityCleanup, cleanupAuthorityID.String(), 4},
		{returnOperationID.String(), runtimeexecution.MaintenanceAuthorityRecovery, recoveryAuthorityID.String(), 5},
	} {
		var kind runtimeexecution.RuntimeMaintenanceAuthorityKind
		var authorityID string
		var generation uint64
		err := system.db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT authority_kind,
			authority_id, authority_generation FROM %s.runtime_execution_maintenance_audit
			WHERE operation_id=$1`, system.schema), expected.operationID).Scan(&kind, &authorityID, &generation)
		if err != nil || kind != expected.kind || authorityID != expected.authorityID || generation != expected.generation {
			t.Fatalf("maintenance audit caller for %s = (%d,%s,%d) err=%v, want (%d,%s,%d)",
				expected.operationID, kind, authorityID, generation, err,
				expected.kind, expected.authorityID, expected.generation)
		}
	}

	secondWork := system.claimRuntime(t)
	second, err := system.runtime.Execute(context.Background(), secondWork.start)
	if err != nil {
		t.Fatalf("acquire pooled lease: %v", err)
	}
	if second.Snapshot.Lease.LeaseID == lease.LeaseID || second.Snapshot.Lease.SandboxID == lease.SandboxID ||
		second.Snapshot.Lease.SandboxGeneration <= lease.SandboxGeneration ||
		second.Snapshot.Lease.SandboxFence <= released.Lease.SandboxFence {
		t.Fatalf("pool reuse crossed fenced identity: first=%+v released=%+v second=%+v",
			lease, released.Lease, second.Snapshot.Lease)
	}
	observedCanaries, err := poolCanary.Reuse(second.Snapshot.Lease, second.Snapshot.Node.ExecutionNodeID,
		secondWork.start.OperationID)
	if err != nil || observedCanaries != (issue76SandboxResidueCanaries{}) {
		t.Fatalf("sandbox pool carried residue across leases: residue=%+v err=%v", observedCanaries, err)
	}
}

func TestPostgresProviderLeaseValidatesExactActiveQuotaReservationInCommit(t *testing.T) {
	now := time.Date(2026, time.July, 29, 19, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name       string
		row        bool
		state      int
		generation uint64
		mode       runtimeexecution.QuotaReservationMode
		workspace  string
		expiresAt  time.Time
		wantLease  bool
	}{
		{name: "missing"},
		{name: "inactive", row: true, state: 2, generation: 4, mode: runtimeexecution.QuotaReservationObservation},
		{name: "stale generation", row: true, state: 1, generation: 5, mode: runtimeexecution.QuotaReservationObservation},
		{name: "wrong mode", row: true, state: 1, generation: 4, mode: runtimeexecution.QuotaReservationEnforced},
		{name: "cross scope", row: true, state: 1, generation: 4, mode: runtimeexecution.QuotaReservationObservation, workspace: "foreign-workspace"},
		{name: "expired", row: true, state: 1, generation: 4, mode: runtimeexecution.QuotaReservationObservation, expiresAt: now},
		{name: "active exact", row: true, state: 1, generation: 4, mode: runtimeexecution.QuotaReservationObservation, wantLease: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
				context.Context,
				runtimeexecution.LeaseAcquisitionRequest,
			) (runtimeexecution.LeaseAcquisitionObservation, error) {
				return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
			})
			system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
			work := system.enqueueAndAdmitRuntime(t, "issue-76-provider-"+issue76Slug(testCase.name))
			reservationID, err := runtimeexecution.NewQuotaReservationID("issue-76-reservation-" + issue76Slug(testCase.name))
			if err != nil {
				t.Fatal(err)
			}
			gatewayID, err := runtimeexecution.NewGatewayRoutePolicyID("issue-76-gateway-" + issue76Slug(testCase.name))
			if err != nil {
				t.Fatal(err)
			}
			input := work.start.StartRuntimeRunInput
			input.ProviderCapability = runtimeexecution.ProviderCapabilityRequired
			input.ProviderBinding = &runtimeexecution.ProviderExecutionBinding{
				QuotaReservationID: reservationID, Generation: 4,
				Mode: runtimeexecution.QuotaReservationObservation, GatewayRoutePolicyID: gatewayID,
			}
			providerStart, err := runtimeexecution.NewStartRuntimeRun(input)
			if err != nil {
				t.Fatalf("construct provider Start: %v", err)
			}
			canonical, err := runtimeexecution.CanonicalStartPayload(providerStart)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.scheduler_work_items
				SET payload_digest=$1, canonical_payload=$2 WHERE work_item_id=$3`, system.schema),
				providerStart.CanonicalRequestDigest[:], canonical, work.canonical.WorkItemID.String()); err != nil {
				t.Fatalf("seed provider Scheduler Work Item: %v", err)
			}
			if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.scheduler_admission_grants
				SET payload_digest=$1 WHERE admission_grant_id=$2`, system.schema),
				providerStart.CanonicalRequestDigest[:], work.canonical.Grant.AdmissionGrantID.String()); err != nil {
				t.Fatalf("seed provider Admission Grant: %v", err)
			}
			quotaTable := system.schema + ".issue76_quota_reservations"
			quotaFunction := system.schema + ".issue76_validate_quota_reservation"
			if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`CREATE TABLE %s (
				quota_reservation_id text PRIMARY KEY, generation bigint NOT NULL, mode smallint NOT NULL,
				state smallint NOT NULL, personal_workspace_id text NOT NULL, phase_run_id text NOT NULL,
				capability smallint NOT NULL, valid_from timestamptz NOT NULL, expires_at timestamptz NOT NULL
			)`, quotaTable)); err != nil {
				t.Fatalf("create Usage Reservation fixture: %v", err)
			}
			if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`CREATE FUNCTION %s(
				p_id text, p_generation bigint, p_mode smallint, p_workspace text,
				p_phase text, p_capability smallint, p_valid_at timestamptz
			) RETURNS void LANGUAGE plpgsql AS $quota$
			DECLARE retained %s%%ROWTYPE;
			BEGIN
				SELECT * INTO retained FROM %s WHERE quota_reservation_id=p_id FOR SHARE;
				IF retained.quota_reservation_id IS NULL OR retained.generation <> p_generation OR
					retained.mode <> p_mode OR retained.state <> 1 OR
					retained.personal_workspace_id <> p_workspace OR retained.phase_run_id <> p_phase OR
					retained.capability <> p_capability OR retained.valid_from > p_valid_at OR
					retained.expires_at <= p_valid_at THEN
					RAISE EXCEPTION 'quota reservation binding conflict' USING ERRCODE = '23000';
				END IF;
			END $quota$`, quotaFunction, quotaTable, quotaTable)); err != nil {
				t.Fatalf("create Usage Reservation participant: %v", err)
			}
			if testCase.row {
				workspace := providerStart.PersonalWorkspaceID.String()
				if testCase.workspace != "" {
					workspace = testCase.workspace
				}
				expiresAt := now.Add(10 * time.Minute)
				if !testCase.expiresAt.IsZero() {
					expiresAt = testCase.expiresAt
				}
				if _, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`INSERT INTO %s
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, quotaTable), reservationID.String(),
					testCase.generation, testCase.mode, testCase.state, workspace,
					providerStart.PhaseRunID.String(), runtimeexecution.ProviderCapabilityRequired,
					now.Add(-time.Minute), expiresAt); err != nil {
					t.Fatalf("seed Usage Reservation: %v", err)
				}
			}
			quotaParticipant := runtimeexecution.QuotaReservationParticipantFunc(func(
				ctx context.Context,
				transaction runtimeexecution.QuotaReservationValidationTransaction,
				_ runtimeexecution.QuotaReservationValidationFact,
			) error {
				return transaction.ValidateQuotaReservation(ctx)
			})
			providerRuntime, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
				Schema: system.schema, Now: system.clock.Now,
				SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
				SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
				SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
				SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
				LeaseAcquisition:                    ready, QuotaReservationParticipant: quotaParticipant,
				QuotaReservationFunction: quotaFunction,
				RuntimeBindingValidator:  system.runtimeBinding,
			})
			if err != nil {
				t.Fatalf("create provider Runtime authority: %v", err)
			}
			decision, executeErr := providerRuntime.Execute(context.Background(), providerStart)
			if testCase.wantLease {
				if executeErr != nil || decision.Snapshot.Lease.Disposition != runtimeexecution.LeaseActive {
					t.Fatalf("exact active Reservation did not authorize lease: decision=%+v err=%v", decision, executeErr)
				}
				return
			}
			assertRuntimeExecutionErrorCode(t, executeErr, runtimeexecution.ErrorIntegrityConflict)
			inspected, inspectErr := providerRuntime.Inspect(context.Background(), runtimeexecution.RuntimeRunRef{
				SchemaVersion: runtimeexecution.SchemaV1, ProjectionVersion: runtimeexecution.SnapshotSchemaCurrent,
				PersonalWorkspaceID: providerStart.PersonalWorkspaceID, RuntimeRunID: providerStart.RuntimeRunID,
				Authority: providerStart.Authority,
			})
			if inspectErr != nil || inspected.Lease.AcquireStatus == runtimeexecution.LeaseGranted ||
				inspected.Capacity.Physical != runtimeexecution.PhysicalCapacityNotApplicable {
				t.Fatalf("invalid Reservation committed lease authority: snapshot=%+v err=%v", inspected, inspectErr)
			}
		})
	}
}

func TestPostgresConcurrentLeaseWorkersCommitExactlyOneLease(t *testing.T) {
	now := time.Date(2026, time.July, 29, 20, 0, 0, 0, time.UTC)
	adapter := &controlledLeaseAcquisitionAdapter{observation: runtimeexecution.LeaseAcquisitionObservation{
		Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
	}}
	barrier := &issue76BlockingFault{
		point:   runtimeexecution.PersistenceFaultBeforeLeaseCommit,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	system := newPostgresRuntimeAdmissionSystem(t, now, adapter, barrier)
	work := system.enqueueAndAdmitRuntime(t, "issue-76-concurrent-acquire")
	waiting, err := system.runtime.Execute(context.Background(), work.start)
	if err != nil || waiting.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseAcquirePending {
		t.Fatalf("accept waiting Runtime: decision=%+v err=%v", waiting, err)
	}
	adapter.Set(runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady})
	type result struct {
		decision runtimeexecution.RuntimeDecision
		err      error
	}
	results := make(chan result, 2)
	go func() {
		decision, executeErr := system.runtime.Execute(context.Background(), work.start)
		results <- result{decision: decision, err: executeErr}
	}()
	<-barrier.entered
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		decision, executeErr := system.runtime.Execute(context.Background(), work.start)
		results <- result{decision: decision, err: executeErr}
	}()
	<-secondStarted
	close(barrier.release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil ||
		first.decision.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseGranted ||
		second.decision.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseGranted ||
		first.decision.Snapshot.Lease != second.decision.Snapshot.Lease ||
		first.decision.Snapshot.Lease.LeaseID.String() == "" ||
		first.decision.Snapshot.Node.Occupancy != runtimeexecution.NodeOccupied ||
		second.decision.Snapshot.Node.Occupancy != runtimeexecution.NodeOccupied {
		t.Fatalf("concurrent workers did not converge on one lease: first=%+v second=%+v", first, second)
	}
}

func TestPostgresRenewCannotCrossConcurrentRevokeOrExpiryFence(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		reason runtimeexecution.LeaseFenceReason
		expiry bool
	}{
		{name: "revoke wins", reason: runtimeexecution.LeaseFenceRevoked},
		{name: "expiry wins", reason: runtimeexecution.LeaseFenceExpired, expiry: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 20, 30, 0, 0, time.UTC)
			ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
				context.Context,
				runtimeexecution.LeaseAcquisitionRequest,
			) (runtimeexecution.LeaseAcquisitionObservation, error) {
				return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
			})
			system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
			work := system.enqueueAndAdmitRuntime(t, "issue-76-race-"+issue76Slug(testCase.name))
			acquired, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil {
				t.Fatal(err)
			}
			lease, node := acquired.Snapshot.Lease, acquired.Snapshot.Node
			if testCase.expiry {
				system.clock.Set(lease.ExpiresAt)
			}
			occurredAt := system.clock.Now()
			fenceOperationID, _ := runtimeexecution.NewOperationID("issue-76-race-fence-" + issue76Slug(testCase.name))
			fence, err := runtimeexecution.NewFenceSandboxLease(runtimeexecution.FenceSandboxLeaseInput{
				SchemaVersion: runtimeexecution.SchemaV1, OperationID: fenceOperationID,
				PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
				ExpectedRuntimeFence: acquired.Snapshot.RuntimeFence, SandboxLeaseID: lease.LeaseID,
				LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
				ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation, Reason: testCase.reason,
				Authority:          system.fencingAuthority,
				ReleaseSafetyEpoch: work.start.ReleaseSafetyEpoch, OccurredAt: occurredAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			renewOperationID, _ := runtimeexecution.NewOperationID("issue-76-race-renew-" + issue76Slug(testCase.name))
			catalogEpoch := runtimeexecution.CatalogSafetyEpoch(0)
			if work.start.CatalogBinding != nil {
				catalogEpoch = work.start.CatalogBinding.SafetyEpoch
			}
			renew, err := runtimeexecution.NewRenewSandboxLease(runtimeexecution.RenewSandboxLeaseInput{
				SchemaVersion: runtimeexecution.SchemaV1, OperationID: renewOperationID,
				PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
				SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
				ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
				AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
				Authority: runtimeexecution.NewLeaseRenewalAuthority(lease.WorkerAuthorityID,
					lease.WorkerGeneration, lease.NodeAuthorityID, lease.AuthorizationGeneration),
				ReleaseSafetyEpoch: work.start.ReleaseSafetyEpoch, CatalogSafetyEpoch: catalogEpoch,
				RequestedExpiresAt: lease.ExpiresAt.Add(time.Minute), OccurredAt: occurredAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			barrier := &issue76BlockingFault{
				point:   runtimeexecution.PersistenceFaultBeforeMandatoryAudit,
				entered: make(chan struct{}), release: make(chan struct{}),
			}
			raced, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
				Schema: system.schema, Now: system.clock.Now, Faults: barrier,
				SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
				SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
				SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
				SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
				LeaseAcquisition:                    ready,
				RuntimeBindingValidator:             system.runtimeBinding,
			})
			if err != nil {
				t.Fatal(err)
			}
			type maintenanceResult struct {
				decision runtimeexecution.RuntimeMaintenanceDecision
				err      error
			}
			fenceResult := make(chan maintenanceResult, 1)
			go func() {
				decision, maintainErr := raced.Maintain(context.Background(), fence)
				fenceResult <- maintenanceResult{decision: decision, err: maintainErr}
			}()
			<-barrier.entered
			renewResult := make(chan maintenanceResult, 1)
			go func() {
				decision, maintainErr := raced.Maintain(context.Background(), renew)
				renewResult <- maintenanceResult{decision: decision, err: maintainErr}
			}()
			close(barrier.release)
			fenced, renewal := <-fenceResult, <-renewResult
			if fenced.err != nil || fenced.decision.Lease.Disposition == runtimeexecution.LeaseActive {
				t.Fatalf("fence did not win deterministic race: %+v", fenced)
			}
			assertRuntimeExecutionErrorCode(t, renewal.err, runtimeexecution.ErrorIntegrityConflict)
			inspected := inspectPostgresRuntime(t, raced, work.start)
			if inspected.Lease != fenced.decision.Lease || inspected.Capacity.Physical != runtimeexecution.PhysicalCapacityUnknownOrQuarantined {
				t.Fatalf("late renewal crossed committed fence: snapshot=%+v fence=%+v", inspected, fenced.decision)
			}
		})
	}
}

func TestPostgresLeaseMaintenanceAuditOutboxFaultsAreAllOrNone(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		point     runtimeexecution.PersistenceFaultPoint
		wantCode  runtimeexecution.ErrorCode
		committed bool
	}{
		{name: "before audit", point: runtimeexecution.PersistenceFaultBeforeMandatoryAudit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after audit", point: runtimeexecution.PersistenceFaultAfterMandatoryAudit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before outbox", point: runtimeexecution.PersistenceFaultBeforeOutbox, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "before commit", point: runtimeexecution.PersistenceFaultBeforeCommit, wantCode: runtimeexecution.ErrorDependencyUnavailable},
		{name: "after commit", point: runtimeexecution.PersistenceFaultAfterCommit, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
		{name: "before response", point: runtimeexecution.PersistenceFaultBeforeResponse, wantCode: runtimeexecution.ErrorReconciliationRequired, committed: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 21, 0, 0, 0, time.UTC)
			ready := runtimeexecution.LeaseAcquisitionAdapterFunc(func(
				context.Context,
				runtimeexecution.LeaseAcquisitionRequest,
			) (runtimeexecution.LeaseAcquisitionObservation, error) {
				return runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady}, nil
			})
			system := newPostgresRuntimeAdmissionSystem(t, now, ready, nil)
			work := system.enqueueAndAdmitRuntime(t, "issue-76-maintenance-fault-"+issue76Slug(testCase.name))
			acquired, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil {
				t.Fatal(err)
			}
			lease, node := acquired.Snapshot.Lease, acquired.Snapshot.Node
			operationID, _ := runtimeexecution.NewOperationID("issue-76-maintenance-fault-" + issue76Slug(testCase.name))
			catalogEpoch := runtimeexecution.CatalogSafetyEpoch(0)
			if work.start.CatalogBinding != nil {
				catalogEpoch = work.start.CatalogBinding.SafetyEpoch
			}
			renew, err := runtimeexecution.NewRenewSandboxLease(runtimeexecution.RenewSandboxLeaseInput{
				SchemaVersion: runtimeexecution.SchemaV1, OperationID: operationID,
				PersonalWorkspaceID: work.start.PersonalWorkspaceID, RuntimeRunID: work.start.RuntimeRunID,
				SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
				ExecutionNodeID: node.ExecutionNodeID, NodeGeneration: node.Generation,
				AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
				Authority: runtimeexecution.NewLeaseRenewalAuthority(lease.WorkerAuthorityID,
					lease.WorkerGeneration, lease.NodeAuthorityID, lease.AuthorizationGeneration),
				ReleaseSafetyEpoch: work.start.ReleaseSafetyEpoch, CatalogSafetyEpoch: catalogEpoch,
				RequestedExpiresAt: lease.ExpiresAt.Add(time.Minute), OccurredAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			faults := &runtimeexecution.PersistenceFaultController{}
			if err := faults.FailNextAt(testCase.point); err != nil {
				t.Fatal(err)
			}
			faulted, err := runtimeexecution.NewPostgresAuthority(system.db, runtimeexecution.PostgresConfig{
				Schema: system.schema, Now: system.clock.Now, Faults: faults,
				SchedulerParticipant:                system.scheduling.RuntimeAcceptanceParticipant(),
				SchedulerAcceptanceFunction:         system.scheduling.RuntimeAcceptanceFunction(),
				SchedulerLeaseAttachmentParticipant: system.scheduling.RuntimeLeaseAttachmentParticipant(),
				SchedulerLeaseAttachmentFunction:    system.scheduling.RuntimeLeaseAttachmentFunction(),
				LeaseAcquisition:                    ready,
				RuntimeBindingValidator:             system.runtimeBinding,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = faulted.Maintain(context.Background(), renew)
			assertRuntimeExecutionErrorCode(t, err, testCase.wantCode)
			after := inspectPostgresRuntime(t, faulted, work.start)
			if testCase.committed {
				if after.Lease.Generation != lease.Generation+1 || after.Lease.Fence != lease.Fence+1 {
					t.Fatalf("post-commit fault lost renewal: %+v", after)
				}
			} else if after != acquired.Snapshot {
				t.Fatalf("pre-commit maintenance fault left partial authority: got=%+v want=%+v", after, acquired.Snapshot)
			}
			replayed, err := faulted.Maintain(context.Background(), renew)
			if err != nil || replayed.Replayed != testCase.committed ||
				replayed.Lease.Generation != lease.Generation+1 {
				t.Fatalf("maintenance ambiguity did not replay original operation: replay=%+v err=%v", replayed, err)
			}
		})
	}
}

func TestPostgresLeaseCommitRelocksExactGrantAndAuthoritativeNodeTruth(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, *postgresRuntimeAdmissionSystem, admittedRuntimeWork)
	}{
		{name: "grant generation", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, work admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.scheduler_admission_grants
				SET generation=generation+1 WHERE admission_grant_id=$1`, system.schema), work.canonical.Grant.AdmissionGrantID.String())
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "selected node", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, work admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.scheduler_admission_grants
				SET execution_node_id='wrong-node' WHERE admission_grant_id=$1`, system.schema), work.canonical.Grant.AdmissionGrantID.String())
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "node generation", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET node_generation=node_generation+1`, system.schema))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "node readiness", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET readiness=$1`, system.schema), runtimeexecution.NodeUnavailable)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Resource Class", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET resource_class_id='wrong-class'`, system.schema))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Execution Policy", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET execution_policy_id='wrong-policy'`, system.schema))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "release safety epoch", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET release_safety_epoch=release_safety_epoch+1`, system.schema))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "catalog safety epoch", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET catalog_safety_epoch=catalog_safety_epoch+1`, system.schema))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "authorization expiry", mutate: func(t *testing.T, system *postgresRuntimeAdmissionSystem, _ admittedRuntimeWork) {
			_, err := system.db.ExecContext(context.Background(), fmt.Sprintf(`UPDATE %s.runtime_execution_nodes
				SET authorization_expires_at=$1`, system.schema), system.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, time.July, 29, 21, 30, 0, 0, time.UTC)
			adapter := &controlledLeaseAcquisitionAdapter{observation: runtimeexecution.LeaseAcquisitionObservation{
				Disposition: runtimeexecution.LeaseAcquisitionTemporaryUnavailable,
			}}
			system := newPostgresRuntimeAdmissionSystem(t, now, adapter, nil)
			work := system.enqueueAndAdmitRuntime(t, "issue-76-relock-"+issue76Slug(testCase.name))
			waiting, err := system.runtime.Execute(context.Background(), work.start)
			if err != nil || waiting.Snapshot.Lease.AcquireStatus != runtimeexecution.LeaseAcquirePending {
				t.Fatalf("accept waiting Runtime: decision=%+v err=%v", waiting, err)
			}
			testCase.mutate(t, system, work)
			adapter.Set(runtimeexecution.LeaseAcquisitionObservation{Disposition: runtimeexecution.LeaseAcquisitionReady})
			_, err = system.runtime.Execute(context.Background(), work.start)
			assertRuntimeExecutionErrorCode(t, err, runtimeexecution.ErrorIntegrityConflict)
			after := inspectPostgresRuntime(t, system.runtime, work.start)
			if after.Lease.AcquireStatus == runtimeexecution.LeaseGranted ||
				after.Capacity.Physical != runtimeexecution.PhysicalCapacityNotApplicable {
				t.Fatalf("stale grant/node truth committed lease: %+v", after)
			}
		})
	}
}

type issue76BlockingFault struct {
	point   runtimeexecution.PersistenceFaultPoint
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (fault *issue76BlockingFault) FailAt(point runtimeexecution.PersistenceFaultPoint) bool {
	if point != fault.point {
		return false
	}
	fault.once.Do(func() {
		close(fault.entered)
		<-fault.release
	})
	return false
}

func issue76Slug(value string) string {
	result := ""
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			result += string(character)
		} else {
			result += "-"
		}
	}
	return result
}

func structuralIssue76Limits() scheduler.AdmissionLimits {
	return scheduler.AdmissionLimits{Global: 2, PersonalWorkspace: 2, WorkerClass: 2, ResourceClass: 2}
}

func assertIssue76SchedulerCode(t *testing.T, err error, want scheduler.ErrorCode) {
	t.Helper()
	var failure *scheduler.Error
	if !errors.As(err, &failure) || failure.Code() != want {
		t.Fatalf("Scheduler error = %T %v, want code %v", err, err, want)
	}
}

type issue76SandboxResidueCanaries struct {
	TaskWorkspaceBytes     string
	Secrets                string
	WritableCacheMutations string
	LogsAndTranscripts     string
	Evidence               string
	MainProcessState       string
	ChildProcessState      string
	NetworkState           string
	PriorOperationIdentity string
}

type issue76SandboxResidueSurface string

const (
	issue76TaskWorkspaceBytesSurface issue76SandboxResidueSurface = "Task/Workspace bytes"
	issue76SecretsSurface            issue76SandboxResidueSurface = "secrets"
	issue76WritableCacheSurface      issue76SandboxResidueSurface = "writable cache"
	issue76LogsTranscriptsSurface    issue76SandboxResidueSurface = "logs/transcripts"
	issue76EvidenceSurface           issue76SandboxResidueSurface = "evidence"
	issue76MainProcessSurface        issue76SandboxResidueSurface = "main process state"
	issue76ChildProcessSurface       issue76SandboxResidueSurface = "child process state"
	issue76NetworkSurface            issue76SandboxResidueSurface = "network state"
	issue76OperationIdentitySurface  issue76SandboxResidueSurface = "prior operation identity"
)

var allIssue76SandboxResidueSurfaces = []issue76SandboxResidueSurface{
	issue76TaskWorkspaceBytesSurface,
	issue76SecretsSurface,
	issue76WritableCacheSurface,
	issue76LogsTranscriptsSurface,
	issue76EvidenceSurface,
	issue76MainProcessSurface,
	issue76ChildProcessSurface,
	issue76NetworkSurface,
	issue76OperationIdentitySurface,
}

type issue76SandboxPoolCanaryAdapter struct {
	leaseID           runtimeexecution.SandboxLeaseID
	leaseGeneration   runtimeexecution.LeaseGeneration
	leaseFence        runtimeexecution.LeaseFence
	sandboxID         runtimeexecution.SandboxID
	sandboxGeneration runtimeexecution.SandboxGeneration
	sandboxFence      runtimeexecution.SandboxFence
	nodeID            runtimeexecution.ExecutionNodeID
	operationID       runtimeexecution.OperationID
	residue           issue76SandboxResidueCanaries
	containment       bool
	reset             bool
	occupancyResolved bool
	workerAuthority   bool
	released          bool
}

func newIssue76SandboxPoolCanaryAdapter(
	lease runtimeexecution.RuntimeLeaseSnapshot,
	nodeID runtimeexecution.ExecutionNodeID,
	operationID runtimeexecution.OperationID,
) *issue76SandboxPoolCanaryAdapter {
	return &issue76SandboxPoolCanaryAdapter{
		leaseID: lease.LeaseID, leaseGeneration: lease.Generation, leaseFence: lease.Fence,
		sandboxID: lease.SandboxID, sandboxGeneration: lease.SandboxGeneration,
		sandboxFence: lease.SandboxFence, nodeID: nodeID, operationID: operationID,
	}
}

func (adapter *issue76SandboxPoolCanaryAdapter) Inject(canaries issue76SandboxResidueCanaries) {
	adapter.residue = canaries
}

func (adapter *issue76SandboxPoolCanaryAdapter) Observe() issue76SandboxResidueCanaries {
	return adapter.residue
}

func (adapter *issue76SandboxPoolCanaryAdapter) ApplyObservedCleanup(surface issue76SandboxResidueSurface) {
	switch surface {
	case issue76TaskWorkspaceBytesSurface:
		adapter.residue.TaskWorkspaceBytes = ""
	case issue76SecretsSurface:
		adapter.residue.Secrets = ""
	case issue76WritableCacheSurface:
		adapter.residue.WritableCacheMutations = ""
	case issue76LogsTranscriptsSurface:
		adapter.residue.LogsAndTranscripts = ""
	case issue76EvidenceSurface:
		adapter.residue.Evidence = ""
	case issue76MainProcessSurface:
		adapter.residue.MainProcessState = ""
	case issue76ChildProcessSurface:
		adapter.residue.ChildProcessState = ""
	case issue76NetworkSurface:
		adapter.residue.NetworkState = ""
	case issue76OperationIdentitySurface:
		adapter.residue.PriorOperationIdentity = ""
	}
}

func (adapter *issue76SandboxPoolCanaryAdapter) ObserveContainmentAndReset() {
	adapter.containment = true
	adapter.reset = true
	adapter.occupancyResolved = true
	adapter.workerAuthority = true
}

func (adapter *issue76SandboxPoolCanaryAdapter) BindObservedResetEvidence(
	input runtimeexecution.ConfirmSandboxResetInput,
) runtimeexecution.ConfirmSandboxResetInput {
	residue := adapter.Observe()
	input.ProcessStopped = residue.MainProcessState == ""
	input.ChildProcessesStopped = residue.ChildProcessState == ""
	input.SecretsRevoked = residue.Secrets == ""
	input.NetworkRemoved = residue.NetworkState == ""
	input.ContainmentEstablished = adapter.containment
	input.ResetCompleted = adapter.reset
	input.NoUnresolvedOccupancy = adapter.occupancyResolved
	input.NoStaleWorkerAuthority = adapter.workerAuthority
	input.NoPriorTaskBytes = residue.TaskWorkspaceBytes == ""
	input.NoPriorSecrets = residue.Secrets == ""
	input.NoWritableCacheMutations = residue.WritableCacheMutations == ""
	input.NoLogsOrTranscripts = residue.LogsAndTranscripts == ""
	input.NoPriorEvidence = residue.Evidence == ""
	input.NoProcessState = residue.MainProcessState == "" && residue.ChildProcessState == ""
	input.NoNetworkState = residue.NetworkState == ""
	input.NoPriorOperationIdentities = residue.PriorOperationIdentity == ""
	canonicalObservation := fmt.Sprintf("%+v\n%t\n%t\n%t\n%t", residue, adapter.containment,
		adapter.reset, adapter.occupancyResolved, adapter.workerAuthority)
	input.EvidenceDigest = runtimeexecution.Digest(sha256.Sum256([]byte(canonicalObservation)))
	return input
}

func issue76ResetEvidenceClaimsSurfaceClean(
	input runtimeexecution.ConfirmSandboxResetInput,
	surface issue76SandboxResidueSurface,
) bool {
	switch surface {
	case issue76TaskWorkspaceBytesSurface:
		return input.NoPriorTaskBytes
	case issue76SecretsSurface:
		return input.SecretsRevoked && input.NoPriorSecrets
	case issue76WritableCacheSurface:
		return input.NoWritableCacheMutations
	case issue76LogsTranscriptsSurface:
		return input.NoLogsOrTranscripts
	case issue76EvidenceSurface:
		return input.NoPriorEvidence
	case issue76MainProcessSurface:
		return input.ProcessStopped && input.NoProcessState
	case issue76ChildProcessSurface:
		return input.ChildProcessesStopped && input.NoProcessState
	case issue76NetworkSurface:
		return input.NetworkRemoved && input.NoNetworkState
	case issue76OperationIdentitySurface:
		return input.NoPriorOperationIdentities
	default:
		return false
	}
}

func (adapter *issue76SandboxPoolCanaryAdapter) ApplyPhysicalRelease(
	evidence runtimeexecution.PhysicalCapacityReleaseReadyEvidence,
) error {
	if adapter.released || evidence.SandboxLeaseID != adapter.leaseID ||
		evidence.LeaseGeneration <= adapter.leaseGeneration || evidence.LeaseFence <= adapter.leaseFence ||
		evidence.SandboxID != adapter.sandboxID || evidence.SandboxGeneration != adapter.sandboxGeneration ||
		evidence.SandboxFence <= adapter.sandboxFence || evidence.ExecutionNodeID != adapter.nodeID ||
		evidence.StartOperationID != adapter.operationID || evidence.ResetEvidenceID.String() == "" ||
		evidence.ResetEvidenceDigest == (runtimeexecution.Digest{}) ||
		adapter.residue != (issue76SandboxResidueCanaries{}) || !adapter.containment || !adapter.reset ||
		!adapter.occupancyResolved || !adapter.workerAuthority {
		return errors.New("physical release evidence did not exactly fence the occupied sandbox")
	}
	adapter.released = true
	return nil
}

func (adapter *issue76SandboxPoolCanaryAdapter) Reuse(
	lease runtimeexecution.RuntimeLeaseSnapshot,
	nodeID runtimeexecution.ExecutionNodeID,
	operationID runtimeexecution.OperationID,
) (issue76SandboxResidueCanaries, error) {
	if !adapter.released || nodeID != adapter.nodeID || lease.LeaseID == adapter.leaseID ||
		lease.SandboxID == adapter.sandboxID || lease.SandboxGeneration <= adapter.sandboxGeneration ||
		lease.SandboxFence <= adapter.sandboxFence || operationID == adapter.operationID ||
		lease.Disposition != runtimeexecution.LeaseActive {
		return issue76SandboxResidueCanaries{}, errors.New("sandbox reuse did not establish a fresh fenced incarnation")
	}
	return adapter.residue, nil
}
