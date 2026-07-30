package runtimeexecution

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

func TestPostgresRuntimeBindingRejectionTerminalizesBeforeLeaseAcquisition(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 59, 0, 0, time.UTC)
	db, schema, config, store, start, leaseCalls, validationCalls :=
		newPostgresWaitingRuntimeBindingRejection(
			t, "binding_prelease_reject", now, time.Time{}, nil, PrerequisiteFailureRevoked, false,
		)

	rejected, err := store.Execute(context.Background(), start)
	if err != nil || rejected.Snapshot.State != RuntimeTerminal ||
		rejected.Snapshot.Outcome != RuntimeRejected ||
		rejected.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalImmutableBinding ||
		rejected.Snapshot.Lease.AcquireStatus != LeaseNotRequested ||
		rejected.Snapshot.Lease.Disposition != LeaseDispositionNone ||
		rejected.Snapshot.Cleanup != (RuntimeLeaseCleanupSnapshot{}) ||
		rejected.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteRejected ||
		rejected.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureRevoked ||
		*leaseCalls != 0 || *validationCalls != 1 {
		t.Fatalf("pre-lease binding rejection: %+v err=%v lease calls=%d validation calls=%d",
			rejected, err, *leaseCalls, *validationCalls)
	}
	assertProvenNoLeaseTerminal(t, rejected.Snapshot)

	var leases, prerequisiteFacts int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_prelease_leases),
		(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_operations
		 WHERE prerequisite_kind=$1)`, postgresPrerequisiteRuntimeBinding).
		Scan(&leases, &prerequisiteFacts); err != nil {
		t.Fatal(err)
	}
	if leases != 0 || prerequisiteFacts != 1 {
		t.Fatalf("pre-lease durable facts: leases=%d Runtime Binding=%d", leases, prerequisiteFacts)
	}
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed != rejected || *leaseCalls != 0 || *validationCalls != 1 {
		t.Fatalf("pre-lease binding rejection replay: %+v err=%v lease calls=%d validation calls=%d",
			replayed, err, *leaseCalls, *validationCalls)
	}
}

func TestPostgresRuntimeBindingIsRevalidatedBeforeDelayedLeaseAcquisition(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 59, 30, 0, time.UTC)
	_, _, _, store, start, leaseCalls, validationCalls :=
		newPostgresWaitingRuntimeBindingRejection(
			t, "bind_revoke_retry", now, time.Time{}, nil, PrerequisiteFailureRevoked, true,
		)

	waiting, err := store.Execute(context.Background(), start)
	if err != nil || waiting.Snapshot.State != RuntimeWaitingForLease ||
		waiting.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteAccepted ||
		*validationCalls != 1 || *leaseCalls != 1 {
		t.Fatalf("first PostgreSQL lease attempt: %+v err=%v validation=%d lease=%d",
			waiting, err, *validationCalls, *leaseCalls)
	}
	rejected, err := store.Execute(context.Background(), start)
	if err != nil || rejected.Snapshot.State != RuntimeTerminal ||
		rejected.Snapshot.Outcome != RuntimeRejected ||
		rejected.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalImmutableBinding ||
		rejected.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteRejected ||
		rejected.Snapshot.Readiness.RuntimeBinding.Failure != PrerequisiteFailureRevoked ||
		*validationCalls != 2 || *leaseCalls != 1 {
		t.Fatalf("revoked PostgreSQL binding crossed delayed lease attempt: %+v err=%v validation=%d lease=%d",
			rejected, err, *validationCalls, *leaseCalls)
	}
	assertProvenNoLeaseTerminal(t, rejected.Snapshot)
}

func TestPostgresAmbiguousRuntimeBindingCannotOutlivePreLeaseTimeBounds(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 59, 45, 0, time.UTC)
	for _, testCase := range []struct {
		name           string
		schema         string
		leaseAcquireBy time.Time
		terminalAt     time.Time
		wantOutcome    RuntimeOutcome
		wantReason     PreLeaseTerminalReason
	}{
		{
			name: "LeaseAcquireBy", schema: "bind_amb_acquire",
			leaseAcquireBy: now.Add(15 * time.Minute), terminalAt: now.Add(15 * time.Minute),
			wantOutcome: RuntimeRejected, wantReason: PreLeaseTerminalAdmissionAuthorityExpired,
		},
		{
			name: "Runtime deadline", schema: "bind_amb_deadline",
			leaseAcquireBy: now.Add(20 * time.Minute), terminalAt: now.Add(20 * time.Minute),
			wantOutcome: RuntimeTimedOut, wantReason: PreLeaseTerminalRuntimeDeadline,
		},
		{
			name: "Runtime deadline wins when both expire", schema: "bind_amb_both",
			leaseAcquireBy: now.Add(5 * time.Minute), terminalAt: now.Add(20 * time.Minute),
			wantOutcome: RuntimeTimedOut, wantReason: PreLeaseTerminalRuntimeDeadline,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, _, store, start, leaseCalls, validationCalls :=
				newPostgresWaitingRuntimeBindingRejection(
					t, testCase.schema, now, testCase.leaseAcquireBy, nil, PrerequisiteFailureRevoked, false,
				)
			currentTime := now
			store.now = func() time.Time { return currentTime }
			store.runtimeBindingValidator = RuntimeBindingValidatorFunc(func(
				context.Context,
				RuntimeBindingValidationRequest,
			) (PrerequisiteObservation, error) {
				*validationCalls++
				return PrerequisiteObservation{
					Disposition: PrerequisiteObservationAmbiguous,
					Failure:     PrerequisiteFailureDependencyUnavailable,
				}, nil
			})

			waiting, err := store.Execute(context.Background(), start)
			if err != nil || waiting.Snapshot.State != RuntimeWaitingForLease ||
				waiting.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired ||
				*validationCalls != 1 || *leaseCalls != 0 {
				t.Fatalf("initial PostgreSQL ambiguous binding: %+v err=%v validation=%d lease=%d",
					waiting, err, *validationCalls, *leaseCalls)
			}
			currentTime = testCase.terminalAt
			terminal, err := store.Execute(context.Background(), start)
			if err != nil || terminal.Snapshot.State != RuntimeTerminal ||
				terminal.Snapshot.Outcome != testCase.wantOutcome ||
				terminal.Snapshot.PreLeaseTerminalReason != testCase.wantReason ||
				*validationCalls != 2 || *leaseCalls != 0 {
				t.Fatalf("PostgreSQL ambiguous binding after time bound: %+v err=%v validation=%d lease=%d",
					terminal, err, *validationCalls, *leaseCalls)
			}
			assertProvenNoLeaseTerminal(t, terminal.Snapshot)
		})
	}
}

func TestPostgresRuntimeDeadlineWinsOverLateRuntimeBindingRejection(t *testing.T) {
	now := time.Date(2026, time.July, 29, 21, 59, 50, 0, time.UTC)
	_, _, _, store, start, leaseCalls, validationCalls :=
		newPostgresWaitingRuntimeBindingRejection(
			t, "bind_late_reject", now, now.Add(5*time.Minute), nil, PrerequisiteFailureRevoked, false,
		)
	currentTime := now
	store.now = func() time.Time { return currentTime }
	store.runtimeBindingValidator = RuntimeBindingValidatorFunc(func(
		context.Context,
		RuntimeBindingValidationRequest,
	) (PrerequisiteObservation, error) {
		*validationCalls++
		if *validationCalls == 1 {
			return PrerequisiteObservation{
				Disposition: PrerequisiteObservationAmbiguous,
				Failure:     PrerequisiteFailureDependencyUnavailable,
			}, nil
		}
		return PrerequisiteObservation{
			Disposition: PrerequisiteObservationRejected,
			Failure:     PrerequisiteFailureRevoked,
		}, nil
	})

	waiting, err := store.Execute(context.Background(), start)
	if err != nil || waiting.Snapshot.State != RuntimeWaitingForLease ||
		waiting.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired ||
		*validationCalls != 1 || *leaseCalls != 0 {
		t.Fatalf("initial PostgreSQL late-rejection setup: %+v err=%v validation=%d lease=%d",
			waiting, err, *validationCalls, *leaseCalls)
	}
	currentTime = start.Deadline
	timedOut, err := store.Execute(context.Background(), start)
	if err != nil || timedOut.Snapshot.State != RuntimeTerminal ||
		timedOut.Snapshot.Outcome != RuntimeTimedOut ||
		timedOut.Snapshot.PreLeaseTerminalReason != PreLeaseTerminalRuntimeDeadline ||
		*validationCalls != 2 || *leaseCalls != 0 {
		t.Fatalf("late PostgreSQL Runtime Binding rejection defeated deadline: %+v err=%v validation=%d lease=%d",
			timedOut, err, *validationCalls, *leaseCalls)
	}
	assertProvenNoLeaseTerminal(t, timedOut.Snapshot)
}

func TestPostgresPermanentC04OpenRejectionIsAuditedAndDoesNotRepeatEffects(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 0, 0, 0, time.UTC)
	openCalls := 0
	lifecycle := RuntimeViewPrerequisiteAdapter{
		OpenRuntimeViewFunc: func(
			context.Context,
			taskworkspace.OpenRuntimeViewRequest,
		) (taskworkspace.OpenRuntimeViewResult, error) {
			openCalls++
			return taskworkspace.OpenRuntimeViewResult{}, &taskworkspace.Error{
				Code: taskworkspace.ErrorStaleAuthority,
			}
		},
	}
	db, schema, _, config, start := newPostgresReadyMutatingPrerequisiteRuntime(
		t, "postlease_rejection", now, func() time.Time { return now }, lifecycle, nil,
	)
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := store.Execute(context.Background(), start)
	if err != nil || rejected.Snapshot.State != RuntimeTerminal ||
		rejected.Snapshot.Outcome != RuntimeRejected ||
		rejected.Snapshot.Lease.Disposition != LeaseRevoked ||
		rejected.Snapshot.Cleanup.Status != LeaseCleanupPending ||
		rejected.Snapshot.Cleanup.FenceRuntimeView ||
		rejected.Snapshot.Capacity.LogicalRelease != LogicalCapacityReleaseReady ||
		rejected.Snapshot.Capacity.Physical != PhysicalCapacityUnknownOrQuarantined {
		t.Fatalf("post-lease rejection: %+v err=%v", rejected, err)
	}
	if openCalls != 1 {
		t.Fatalf("C04 Open calls=%d want=1", openCalls)
	}

	var commands, audits, runtimeOutbox, cleanup, capacity, openOutbox, viewTerminal int
	var auditBytes []byte
	if err := db.QueryRowContext(context.Background(), `SELECT
					(SELECT count(*) FROM `+schema+`.runtime_execution_requests WHERE command_kind=$1),
				(SELECT count(*) FROM `+schema+`.runtime_execution_mandatory_audit WHERE action=$2),
				(SELECT count(*) FROM `+schema+`.runtime_execution_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_lease_cleanup_obligations),
				(SELECT count(*) FROM `+schema+`.runtime_execution_capacity_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_prerequisite_outbox),
				(SELECT count(*) FROM `+schema+`.runtime_execution_runtime_view_terminal_operations),
				(SELECT audit_state FROM `+schema+`.runtime_execution_mandatory_audit WHERE action=$2)`,
		postgresPostLeaseRejectionCommandKind, postgresAuditPostLeasePrerequisiteRejected).
		Scan(&commands, &audits, &runtimeOutbox, &cleanup, &capacity, &openOutbox, &viewTerminal, &auditBytes); err != nil {
		t.Fatal(err)
	}
	auditState, err := decodePostgresMandatoryAuditState(auditBytes)
	if err != nil || auditState.ReasonCode != uint8(postLeaseRuntimeViewRejected) {
		t.Fatalf("post-lease rejection audit reason=%d want=%d err=%v",
			auditState.ReasonCode, postLeaseRuntimeViewRejected, err)
	}
	if commands != 1 || audits != 1 || runtimeOutbox != 2 || cleanup != 1 || capacity != 1 ||
		openOutbox != 1 || viewTerminal != 0 {
		t.Fatalf("durable rejection facts: command=%d audit=%d outbox=%d cleanup=%d capacity=%d open=%d terminal=%d",
			commands, audits, runtimeOutbox, cleanup, capacity, openOutbox, viewTerminal)
	}

	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed != rejected || openCalls != 1 {
		t.Fatalf("post-lease rejection restart replay: %+v err=%v opens=%d", replayed, err, openCalls)
	}
}

func TestPostgresPreLeaseRuntimeBindingRejectionCommitLossReplaysTheTerminalRuntime(t *testing.T) {
	now := time.Date(2026, time.July, 29, 22, 10, 0, 0, time.UTC)
	faults := &PersistenceFaultController{}
	db, schema, config, store, start, leaseCalls, validationCalls :=
		newPostgresWaitingRuntimeBindingRejection(
			t, "binding_reject_loss", now, time.Time{}, faults, PrerequisiteFailureIncompatible, false,
		)
	if err := faults.FailNextAt(PersistenceFaultAfterNoLeaseCommit); err != nil {
		t.Fatal(err)
	}
	_, err := store.Execute(context.Background(), start)
	assertErrorCode(t, err, ErrorReconciliationRequired)

	snapshot, err := store.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID,
		Authority: start.Authority,
	})
	if err != nil || snapshot.State != RuntimeTerminal || snapshot.Outcome != RuntimeRejected {
		t.Fatalf("committed rejection after response loss: %+v err=%v", snapshot, err)
	}
	var commands, audits, outbox int
	if err := db.QueryRowContext(context.Background(), `SELECT
		(SELECT count(*) FROM `+schema+`.runtime_execution_requests WHERE command_kind=$1),
		(SELECT count(*) FROM `+schema+`.runtime_execution_mandatory_audit WHERE action=$2),
		(SELECT count(*) FROM `+schema+`.runtime_execution_outbox)`,
		postgresPreLeaseTerminalCommandKind, postgresAuditPreLeaseTerminal).
		Scan(&commands, &audits, &outbox); err != nil {
		t.Fatal(err)
	}
	if commands != 1 || audits != 1 || outbox != 2 || *leaseCalls != 0 {
		t.Fatalf("commit-loss facts: command=%d audit=%d outbox=%d", commands, audits, outbox)
	}

	config.Faults = nil
	restarted, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Execute(context.Background(), start)
	if err != nil || replayed.Snapshot != snapshot || *validationCalls != 1 || *leaseCalls != 0 {
		t.Fatalf("commit-loss replay: %+v err=%v validationCalls=%d leaseCalls=%d",
			replayed, err, *validationCalls, *leaseCalls)
	}
}

func newPostgresWaitingRuntimeBindingRejection(
	t *testing.T,
	schemaPrefix string,
	now time.Time,
	leaseAcquireBy time.Time,
	faults *PersistenceFaultController,
	failure PrerequisiteFailure,
	acceptBeforeRevocation bool,
) (*sql.DB, string, PostgresConfig, *PostgresAuthority, StartRuntimeRun, *int, *int) {
	t.Helper()
	db, schema := testpostgres.Open(t, schemaPrefix)
	authority := mustTaskOrchestrationAuthority(t, schemaPrefix+"-authority", 9)
	start := standardStart(t, now, authority, schemaPrefix)
	grant := grantFixtureForStart(start, now.Add(10*time.Minute), true)
	grant.ExecutionNodeID = startNodeID(t, schemaPrefix+"-node")
	grant.NodeCapacityGeneration = 1
	grant.SchedulerEpoch = 1
	grant.PolicyVersion = 1
	fixture := acceptedPostgresRuntimeFixture(start, authority, now)
	fixture.Operation.WorkItemID = grant.WorkItemID
	fixture.Operation.ExecutionNodeID = grant.ExecutionNodeID
	fixture.Operation.NodeCapacityGeneration = grant.NodeCapacityGeneration
	fixture.Operation.ResourceClassID = start.ResourceClassID
	fixture.Operation.ExecutionPolicyID = start.ExecutionPolicyID
	fixture.Operation.SchedulerEpoch = grant.SchedulerEpoch
	fixture.Operation.PolicyVersion = grant.PolicyVersion
	fixture.Lease.AcquireOperationID, fixture.Lease.AcquireDigest = stableLeaseAcquireBinding(start)
	if !leaseAcquireBy.IsZero() {
		fixture.LeaseAcquireBy = leaseAcquireBy
	}
	fixture.Readiness = initialRuntimeReadiness(start)
	leaseCalls := new(int)
	validationCalls := new(int)
	config := PostgresConfig{
		Schema: schema, Now: func() time.Time { return now }, Faults: faults,
		LeaseAcquisition: LeaseAcquisitionAdapterFunc(func(
			context.Context,
			LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			*leaseCalls++
			if acceptBeforeRevocation {
				return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionTemporaryUnavailable}, nil
			}
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		}),
		RuntimeBindingValidator: RuntimeBindingValidatorFunc(func(
			context.Context,
			RuntimeBindingValidationRequest,
		) (PrerequisiteObservation, error) {
			*validationCalls++
			if acceptBeforeRevocation && *validationCalls == 1 {
				return acceptedPrerequisiteObservation(t, schemaPrefix+"-evidence", digest(248)), nil
			}
			return PrerequisiteObservation{
				Disposition: PrerequisiteObservationRejected,
				Failure:     failure,
			}, nil
		}),
	}
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	installPostgresRuntimeFixture(t, db, schema, fixture, now)
	installPostgresAcceptedStartFacts(t, db, schema, start, retainedAcceptedStartFact(
		start, "runtime-decision-"+schemaPrefix,
	), now)
	return db, schema, config, store, start, leaseCalls, validationCalls
}
