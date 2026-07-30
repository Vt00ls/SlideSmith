package runtimeexecution

import (
	"context"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestPostgresC04OpenAcceptedAfterLeaseRenewalCannotSatisfyReadiness(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 25, 0, 0, time.UTC)
	var requests []taskworkspace.OpenRuntimeViewRequest
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			_ context.Context,
			request taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			requests = append(requests, request)
			if len(requests) == 1 {
				return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{
					Code: taskworkspace.ErrorReconciliationRequired,
				}
			}
			return acceptedRuntimeViewResult(request, "postgres-stale-open-view"), nil
		},
	}
	db, schema, store, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "stale_open", now, func() time.Time { return now }, lifecycle, nil,
	)

	first, err := store.Execute(context.Background(), start)
	if err != nil || first.Snapshot.Readiness.RuntimeView.State != PrerequisiteReconciliationRequired ||
		first.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("initial ambiguous PostgreSQL open: %+v err=%v", first, err)
	}
	lease := first.Snapshot.Lease
	var catalogSafetyEpoch CatalogSafetyEpoch
	if start.CatalogBinding != nil {
		catalogSafetyEpoch = start.CatalogBinding.SafetyEpoch
	}
	renew, err := NewRenewSandboxLease(RenewSandboxLeaseInput{
		SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "postgres-stale-open-renew"),
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		SandboxLeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseFence: lease.Fence,
		ExecutionNodeID: first.Snapshot.Operation.ExecutionNodeID,
		NodeGeneration:  NodeGeneration(first.Snapshot.Operation.NodeCapacityGeneration),
		AttestationID:   first.Snapshot.Node.AttestationID, AttestationGeneration: first.Snapshot.Node.AttestationGeneration,
		Authority: NewLeaseRenewalAuthority(
			lease.WorkerAuthorityID, lease.WorkerGeneration, lease.NodeAuthorityID, lease.AuthorizationGeneration,
		),
		ReleaseSafetyEpoch: start.ReleaseSafetyEpoch, CatalogSafetyEpoch: catalogSafetyEpoch,
		RequestedExpiresAt: now.Add(150 * time.Second), OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := store.Maintain(context.Background(), renew)
	if err != nil || renewed.Lease.Generation != lease.Generation+1 || renewed.Lease.Fence != lease.Fence+1 {
		t.Fatalf("renew PostgreSQL lease: %+v err=%v", renewed, err)
	}

	settled, err := store.Execute(context.Background(), start)
	if err != nil || settled.Snapshot.Readiness.RuntimeView.State != PrerequisiteAccepted ||
		settled.Snapshot.Readiness.ImmutableInputs.State != PrerequisitePending ||
		settled.Snapshot.Readiness.CapsuleReady {
		t.Fatalf("stale PostgreSQL C04 acceptance satisfied readiness: %+v err=%v", settled, err)
	}
	if len(requests) != 2 || requests[0] != requests[1] ||
		settled.Snapshot.RuntimeViewBinding.LeaseGeneration != lease.Generation ||
		settled.Snapshot.Lease.Generation != renewed.Lease.Generation {
		t.Fatalf("PostgreSQL stale open was repinned: requests=%+v snapshot=%+v", requests, settled.Snapshot)
	}

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := restarted.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: start.Authority,
	})
	if err != nil || inspected.Readiness.CapsuleReady ||
		inspected.Readiness.ImmutableInputs.State != PrerequisitePending {
		t.Fatalf("restart lost stale-authority readiness fence: %+v err=%v", inspected, err)
	}
	var deliveries int
	if err := db.QueryRowContext(context.Background(), `SELECT delivery_count FROM `+schema+
		`.runtime_execution_prerequisite_outbox_delivery WHERE operation_id=$1`,
		requests[0].Operation.ID).Scan(&deliveries); err != nil || deliveries != 2 {
		t.Fatalf("stale open deliveries=%d err=%v, want exact replay twice", deliveries, err)
	}
}
