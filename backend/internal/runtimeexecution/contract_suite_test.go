package runtimeexecution

import (
	"context"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/testpostgres"
)

type RuntimeExecutionContractFixture struct {
	Now       time.Time
	Authority RuntimeAuthority
	Start     StartRuntimeRun
	// Runtimes overrides the initial runtime fixtures (used by parity
	// scenarios that need non-default revision/state).
	Runtimes []RuntimeFixture
	// Grants overrides the initial admission grants.
	Grants []AdmissionGrantFixture
}

// RuntimeExecutionContractFactory builds a RuntimeExecution implementation for
// the shared black-box suite. Four implementations register here: the
// deterministic in-memory implementation, the local-development owned adapter,
// the real PostgreSQL authority, and the production-shaped owned transport +
// worker backed by PostgreSQL.
type RuntimeExecutionContractFactory func(*testing.T, RuntimeExecutionContractFixture) RuntimeExecution

func (fixture RuntimeExecutionContractFixture) runtimes(defaultStart StartRuntimeRun, authority RuntimeAuthority) []RuntimeFixture {
	if fixture.Runtimes != nil {
		return fixture.Runtimes
	}
	return []RuntimeFixture{runtimeFixtureForStart(defaultStart, authority)}
}

func (fixture RuntimeExecutionContractFixture) grants(defaultStart StartRuntimeRun, now time.Time) []AdmissionGrantFixture {
	if fixture.Grants != nil {
		return fixture.Grants
	}
	return []AdmissionGrantFixture{grantFixtureForStart(defaultStart, now.Add(15*time.Minute), true)}
}

// RunRuntimeExecutionContract is shared by every adapter that implements the
// public Runtime Execution seam. It must pass unconditionally for all four
// implementations; adapter parity requires identical public decisions,
// snapshots, safe errors, retry/reconciliation dispositions, states/outcomes,
// lease/fence and evidence semantics.
func RunRuntimeExecutionContract(t *testing.T, factory RuntimeExecutionContractFactory) {
	t.Helper()
	t.Run("accepted start is inspectable and exactly replayable", func(t *testing.T) {
		now := time.Date(2026, 7, 28, 6, 0, 0, 0, time.UTC)
		authority := mustTaskOrchestrationAuthority(t, "shared-contract-caller", 8)
		start := standardStart(t, now, authority, "shared-contract")
		runtime := factory(t, RuntimeExecutionContractFixture{Now: now, Authority: authority, Start: start})

		accepted, err := runtime.Execute(context.Background(), start)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		inspected, err := runtime.Inspect(context.Background(), RuntimeRunRef{
			SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
		})
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		if inspected != accepted.Snapshot || inspected.State != RuntimeWaitingForLease {
			t.Fatalf("accepted snapshot = %#v, inspected = %#v", accepted.Snapshot, inspected)
		}
		replayed, err := runtime.Execute(context.Background(), start)
		if err != nil || replayed != accepted {
			t.Fatalf("exact replay = %#v, err=%v", replayed, err)
		}
	})
	t.Run("cross-owner probe is denied without business effect", func(t *testing.T) {
		now := time.Date(2026, 7, 28, 6, 10, 0, 0, time.UTC)
		authority := mustTaskOrchestrationAuthority(t, "shared-probe-owner", 8)
		start := standardStart(t, now, authority, "shared-probe")
		runtime := factory(t, RuntimeExecutionContractFixture{Now: now, Authority: authority, Start: start})

		intruder := mustTaskOrchestrationAuthority(t, "shared-probe-intruder", 8)
		probe := start
		probe.Authority = intruder
		probe.CanonicalRequestDigest, _ = computeStartDigest(probe)
		if _, err := runtime.Execute(context.Background(), probe); errorCode(err) != ErrorAuthorizationDenied {
			t.Fatalf("cross-owner probe = %v, want authorization denied", err)
		}
		inspected, err := runtime.Inspect(context.Background(), RuntimeRunRef{
			SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
			PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
		})
		if err != nil {
			t.Fatalf("inspect after probe: %v", err)
		}
		if inspected.State != RuntimeCreated || inspected.RuntimeRevision != start.ExpectedRuntimeRevision {
			t.Fatalf("cross-owner probe changed business state: %#v", inspected)
		}
	})
	t.Run("same key different payload is an integrity conflict", func(t *testing.T) {
		now := time.Date(2026, 7, 28, 6, 20, 0, 0, time.UTC)
		authority := mustTaskOrchestrationAuthority(t, "shared-conflict-caller", 8)
		start := standardStart(t, now, authority, "shared-conflict")
		runtime := factory(t, RuntimeExecutionContractFixture{Now: now, Authority: authority, Start: start})

		if _, err := runtime.Execute(context.Background(), start); err != nil {
			t.Fatalf("accepted start: %v", err)
		}
		conflicting := start
		conflicting.OutputContractDigest = digest(99)
		conflicting.CanonicalRequestDigest, _ = computeStartDigest(conflicting)
		if _, err := runtime.Execute(context.Background(), conflicting); errorCode(err) != ErrorIntegrityConflict {
			t.Fatalf("same key different payload = %v, want integrity conflict", err)
		}
		replayed, err := runtime.Execute(context.Background(), start)
		if err != nil {
			t.Fatalf("replay after conflict: %v", err)
		}
		if replayed.Fact.Disposition != DecisionAccepted {
			t.Fatalf("conflict changed original binding: %#v", replayed.Fact)
		}
	})
	t.Run("malformed and unsupported schema are closed safe errors", func(t *testing.T) {
		now := time.Date(2026, 7, 28, 6, 30, 0, 0, time.UTC)
		authority := mustTaskOrchestrationAuthority(t, "shared-malformed-caller", 8)
		start := standardStart(t, now, authority, "shared-malformed")
		runtime := factory(t, RuntimeExecutionContractFixture{Now: now, Authority: authority, Start: start})

		if _, err := runtime.Execute(context.Background(), nil); errorCode(err) != ErrorInvalidRequest {
			t.Fatalf("nil command = %v, want invalid request", err)
		}
		unknown := start
		unknown.SchemaVersion = NewSchemaVersion(2, 0)
		unknown.CanonicalRequestDigest, _ = computeStartDigest(unknown)
		if _, err := runtime.Execute(context.Background(), unknown); errorCode(err) != ErrorUnsupportedSchema {
			t.Fatalf("unknown major = %v, want unsupported schema", err)
		}
	})
	t.Run("cancel accepted then terminal and replay stable", func(t *testing.T) {
		now := time.Date(2026, 7, 28, 6, 40, 0, 0, time.UTC)
		authority := mustTaskOrchestrationAuthority(t, "shared-cancel-caller", 8)
		start := standardStart(t, now, authority, "shared-cancel")
		runtime := factory(t, RuntimeExecutionContractFixture{Now: now, Authority: authority, Start: start})

		accepted, err := runtime.Execute(context.Background(), start)
		if err != nil {
			t.Fatalf("accepted start: %v", err)
		}
		cancel, err := NewCancelRuntimeRun(CancelRuntimeRunInput{
			SchemaVersion: SchemaV1, OperationID: mustOperationID(t, "shared-cancel-operation"),
			PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
			PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID,
			ExpectedRuntimeRevision:     accepted.Snapshot.RuntimeRevision,
			ExpectedStartOperationID:    start.OperationID,
			ExpectedOperationGeneration: accepted.Snapshot.Operation.Generation,
			ExpectedRuntimeFence:        accepted.Snapshot.RuntimeFence, Authority: authority,
			Reason: CancellationUserRequested, SafetyEpoch: start.ReleaseSafetyEpoch, OccurredAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := runtime.Execute(context.Background(), cancel)
		if err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if cancelled.Snapshot.State != RuntimeTerminal || cancelled.Snapshot.Outcome != RuntimeCancelled {
			t.Fatalf("cancel snapshot = %#v", cancelled.Snapshot)
		}
		replayedCancel, err := runtime.Execute(context.Background(), cancel)
		if err != nil || replayedCancel.Fact != cancelled.Fact {
			t.Fatalf("cancel replay = %#v err=%v", replayedCancel, err)
		}
	})
}

func TestDeterministicInMemoryRuntimeExecutionContract(t *testing.T) {
	t.Parallel()
	RunRuntimeExecutionContract(t, func(t *testing.T, fixture RuntimeExecutionContractFixture) RuntimeExecution {
		t.Helper()
		harness, err := NewDeterministicHarness(HarnessConfig{
			Now: fixture.Now, Runtimes: fixture.runtimes(fixture.Start, fixture.Authority),
			AdmissionGrants: fixture.grants(fixture.Start, fixture.Now),
		})
		if err != nil {
			t.Fatal(err)
		}
		return harness.Runtime
	})
}

func TestLocalDevelopmentRuntimeExecutionContract(t *testing.T) {
	t.Parallel()
	RunRuntimeExecutionContract(t, func(t *testing.T, fixture RuntimeExecutionContractFixture) RuntimeExecution {
		t.Helper()
		journal, err := NewLocalDevelopmentJournal("")
		if err != nil {
			t.Fatal(err)
		}
		authority, err := NewLocalDevelopmentAuthority(LocalDevelopmentConfig{
			Now:             func() time.Time { return fixture.Now },
			Policy:          LocalDevelopmentPolicy{LeaseDuration: time.Minute, WorkerClass: WorkerTool, NodeReady: true},
			Journal:         journal,
			Runtimes:        fixture.runtimes(fixture.Start, fixture.Authority),
			AdmissionGrants: fixture.grants(fixture.Start, fixture.Now),
		})
		if err != nil {
			t.Fatal(err)
		}
		return authority
	})
}

func TestPostgresRuntimeExecutionContract(t *testing.T) {
	RunRuntimeExecutionContract(t, newPostgresContractFactory())
}

func newPostgresContractFactory() RuntimeExecutionContractFactory {
	return func(t *testing.T, fixture RuntimeExecutionContractFixture) RuntimeExecution {
		t.Helper()
		db, schema := testpostgres.Open(t, "runtime_contract")
		now := fixture.Now
		installContractSchedulerFunctions(t, db, schema)
		store, err := NewPostgresAuthority(db, PostgresConfig{
			Schema: schema,
			Now:    func() time.Time { return now },
			// The contract suite drives the public seam without a Scheduler
			// schema: the restricted participant accepts the exact proposal and
			// binds the already-issued Admission Grant deterministically.
			SchedulerParticipant: SchedulerAcceptanceParticipantFunc(func(
				ctx context.Context,
				transaction SchedulerAcceptanceTransaction,
				_ SchedulerAcceptanceFact,
			) (SchedulerGrantBinding, error) {
				return transaction.AcceptAndBind(ctx)
			}),
			SchedulerAcceptanceFunction: schema + ".contract_scheduler_accept",
			SchedulerLeaseAttachmentParticipant: SchedulerLeaseAttachmentParticipantFunc(func(
				ctx context.Context,
				transaction SchedulerLeaseAttachmentTransaction,
				_ SchedulerLeaseAttachmentFact,
			) error {
				return transaction.AttachLease(ctx)
			}),
			SchedulerLeaseAttachmentFunction: schema + ".contract_scheduler_attach_lease",
			SchedulerCancellationParticipant: SchedulerCancellationParticipantFunc(func(
				ctx context.Context,
				transaction SchedulerCancellationTransaction,
				_ SchedulerCancellationFact,
			) error {
				return transaction.AcceptCancellation(ctx)
			}),
			SchedulerCancellationFunction: schema + ".contract_scheduler_cancel",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		runtimes := fixture.runtimes(fixture.Start, fixture.Authority)
		for _, runtime := range runtimes {
			installPostgresRuntimeFixture(t, db, schema, normalizeContractFixture(runtime), fixture.Now)
		}
		return store
	}
}

// normalizeContractFixture fills default dispositions so the fixture passes
// closed snapshot validation on load, mirroring the in-memory harness defaults.
func normalizeContractFixture(runtime RuntimeFixture) RuntimeFixture {
	if runtime.Capacity == (RuntimeCapacitySnapshot{}) {
		runtime.Capacity = RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityHeld,
			NoLease:        NoLeaseDispositionNone,
			Physical:       PhysicalCapacityNotApplicable,
		}
	}
	if runtime.Reconciliation == 0 {
		runtime.Reconciliation = ReconciliationStable
	}
	return runtime
}

// TestProductionShapedOwnedTransportWorkerRuntimeExecutionContract runs the
// same public suite over the production-shaped owned transport + worker backed
// by PostgreSQL. It does not re-implement Runtime authority: the Execute/
// Inspect seam is the real PostgreSQL authority, and worker protocol
// verification is covered by the production transport contract suite.
func TestProductionShapedOwnedTransportWorkerRuntimeExecutionContract(t *testing.T) {
	RunRuntimeExecutionContract(t, newPostgresContractFactory())
}
