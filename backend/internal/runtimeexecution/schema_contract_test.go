package runtimeexecution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPostgresMaintenanceReplayCodecRejectsMissingLifecycleAuthority(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(postgresMaintenanceDecisionState{
		OperationID: "maintenance-missing-authority", CanonicalRequestDigest: digest(44),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodePostgresMaintenanceDecision(encoded)
	var persistenceError *PersistenceError
	if !errors.As(err, &persistenceError) || persistenceError.Code() != PersistenceStateCorrupt {
		t.Fatalf("decode error = %T %v, want corrupt persistence state", err, err)
	}
}

func TestMalformedZeroSchemaCancelIsInvalidRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 2, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "malformed-cancel-caller", 4)
	start := standardStart(t, now, authority, "malformed-cancel")
	harness := harnessForStart(t, now, authority, start)

	malformed := CancelRuntimeRun{CancelRuntimeRunInput: CancelRuntimeRunInput{SchemaVersion: 0}}
	assertErrorCode(t, executeError(harness, malformed), ErrorInvalidRequest)
}

func TestMalformedZeroSchemaInspectIsInvalidRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 2, 50, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "malformed-inspect-caller", 4)
	start := standardStart(t, now, authority, "malformed-inspect")
	harness := harnessForStart(t, now, authority, start)

	_, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: 0, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	})
	assertErrorCode(t, err, ErrorInvalidRequest)
}

func TestInspectFailuresPersistSanitizedObservations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 2, 55, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "inspect-observation-caller", 4)
	start := standardStart(t, now, authority, "inspect-observation")
	harness := harnessForStart(t, now, authority, start)

	base := RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: start.PersonalWorkspaceID, RuntimeRunID: start.RuntimeRunID, Authority: authority,
	}
	malformed := base
	malformed.SchemaVersion = 0
	_, err := harness.Runtime.Inspect(context.Background(), malformed)
	assertErrorCode(t, err, ErrorInvalidRequest)

	unknown := base
	unknown.SchemaVersion = NewSchemaVersion(2, 0)
	_, err = harness.Runtime.Inspect(context.Background(), unknown)
	assertErrorCode(t, err, ErrorUnsupportedSchema)

	missing := base
	missing.RuntimeRunID = mustRuntimeRunID(t, "inspect-observation-missing")
	_, err = harness.Runtime.Inspect(context.Background(), missing)
	assertErrorCode(t, err, ErrorAuthorizationDenied)

	observations := harness.IngressObservations()
	want := []IngressObservationKind{IngressMalformed, IngressUnsupportedSchema, IngressAuthorizationDenied}
	if len(observations) != len(want) {
		t.Fatalf("inspect observations = %#v", observations)
	}
	for index, observation := range observations {
		if observation.Kind != want[index] {
			t.Fatalf("inspect observation %d = %#v", index, observation)
		}
	}
}

func TestRuntimeSnapshotClosedSchemaAndLosslessProjectionRules(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "schema-caller", 4)
	normal := schemaRuntimeFixture(t, authority, "normal")
	lossy := schemaRuntimeFixture(t, authority, "lossy")
	lossy.Capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionNone,
		Physical:       PhysicalCapacityUnknownOrQuarantined,
	}
	lossy.Reconciliation = ReconciliationRequiredStatus
	unknownEvidence := schemaRuntimeFixture(t, authority, "unknown-evidence")
	unknownEvidence.EvidenceRoot = EvidenceRootSnapshot{
		SchemaVersion:  NewSchemaVersion(2, 0),
		EvidenceRootID: EvidenceRootID{value: "evidence-root-unknown"},
		Digest:         digest(21),
	}
	unknownVariant := schemaRuntimeFixture(t, authority, "unknown-variant")
	unknownVariant.State = RuntimeState(255)
	unknownLeaseVariant := schemaRuntimeFixture(t, authority, "unknown-lease-variant")
	unknownLeaseVariant.Lease.Disposition = LeaseDisposition(255)
	unknownNodeVariant := schemaRuntimeFixture(t, authority, "unknown-node-variant")
	unknownNodeVariant.Node.Readiness = NodeReadiness(255)
	grantedWithoutLifecycle := schemaRuntimeFixture(t, authority, "granted-without-lifecycle")
	grantedWithoutLifecycle.Lease = RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, AcquireOperationID: mustOperationID(t, "granted-without-lifecycle-acquire"),
		AcquireDigest: digest(31), LeaseID: SandboxLeaseID{value: "granted-without-lifecycle-lease"},
		Generation: 1, Fence: 1,
	}
	activeWithoutNodeTruth := schemaRuntimeFixture(t, authority, "active-without-node-truth")
	activeWithoutNodeTruth.Operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: mustOperationID(t, "active-without-node-start"), Digest: digest(32),
		Generation: 1, AdmissionGrantID: AdmissionGrantID{value: "active-without-node-grant"},
		WorkItemID: WorkItemID{value: "active-without-node-work"}, GrantGeneration: 1,
		ExecutionNodeID: ExecutionNodeID{value: "active-without-node-node"}, NodeCapacityGeneration: 1,
		ResourceClassID:   ResourceClassID{value: "active-without-node-class"},
		ExecutionPolicyID: ExecutionPolicyID{value: "active-without-node-policy"},
		SchedulerEpoch:    1, PolicyVersion: 1,
	}
	activeWithoutNodeTruth.Lease = RuntimeLeaseSnapshot{
		AcquireStatus: LeaseGranted, AcquireOperationID: mustOperationID(t, "active-without-node-acquire"),
		AcquireDigest: digest(33), LeaseID: SandboxLeaseID{value: "active-without-node-lease"},
		Generation: 1, Fence: 1, Disposition: LeaseActive, ExpiresAt: now.Add(time.Minute),
		SandboxID: SandboxID{value: "active-without-node-sandbox"}, SandboxGeneration: 1, SandboxFence: 1,
		WorkerAuthorityID: WorkerAuthorityID{value: "active-without-node-worker"}, WorkerGeneration: 1,
		NodeAuthorityID: NodeAuthorityID{value: "active-without-node-authority"}, AuthorizationGeneration: 1,
		AuthorizationExpiresAt: now.Add(time.Minute),
	}

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{
			normal, lossy, unknownEvidence, unknownVariant, unknownLeaseVariant, unknownNodeVariant,
			grantedWithoutLifecycle, activeWithoutNodeTruth,
		},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	current := inspectRuntime(t, harness, normal, authority, NewSchemaVersion(1, 99), SnapshotSchemaCurrent)
	if current.SchemaVersion != SnapshotSchemaCurrent {
		t.Fatalf("optional request minor changed projection: %v", current.SchemaVersion)
	}
	old := inspectRuntime(t, harness, normal, authority, SchemaV1, SnapshotSchemaV1)
	if old.SchemaVersion != SnapshotSchemaV1 || old.RuntimeRunID != normal.RuntimeRunID {
		t.Fatalf("explicit lossless old projection = %#v", old)
	}

	assertInspectError(t, harness, lossy, authority, SchemaV1, SnapshotSchemaV1, ErrorUnsupportedSchema)
	assertInspectError(t, harness, normal, authority, SchemaV1, 0, ErrorUnsupportedSchema)
	assertInspectError(t, harness, normal, authority, SchemaV1, NewSchemaVersion(9, 0), ErrorUnsupportedSchema)
	assertInspectError(t, harness, unknownEvidence, authority, SchemaV1, SnapshotSchemaCurrent, ErrorUnsupportedSchema)
	assertInspectError(t, harness, unknownVariant, authority, SchemaV1, SnapshotSchemaCurrent, ErrorUnsupportedSchema)
	assertInspectError(t, harness, unknownLeaseVariant, authority, SchemaV1, SnapshotSchemaCurrent, ErrorUnsupportedSchema)
	assertInspectError(t, harness, unknownNodeVariant, authority, SchemaV1, SnapshotSchemaCurrent, ErrorUnsupportedSchema)
	assertInspectError(t, harness, grantedWithoutLifecycle, authority, SchemaV1, SnapshotSchemaCurrent, ErrorUnsupportedSchema)
	assertInspectError(t, harness, activeWithoutNodeTruth, authority, SchemaV1, SnapshotSchemaCurrent, ErrorUnsupportedSchema)

	assertInspectError(t, harness, normal, authority, NewSchemaVersion(2, 0), SnapshotSchemaCurrent, ErrorUnsupportedSchema)
	missing := normal
	missing.RuntimeRunID = mustRuntimeRunID(t, "schema-missing")
	assertInspectError(t, harness, missing, authority, NewSchemaVersion(2, 0), SnapshotSchemaCurrent, ErrorUnsupportedSchema)
}

func schemaRuntimeFixture(t *testing.T, authority RuntimeAuthority, suffix string) RuntimeFixture {
	t.Helper()
	return RuntimeFixture{
		PersonalWorkspaceID: mustPersonalWorkspaceID(t, "schema-workspace-"+suffix),
		TaskID:              mustTaskID(t, "schema-task-"+suffix), PhaseRunID: mustPhaseRunID(t, "schema-phase-"+suffix),
		RuntimeRunID: mustRuntimeRunID(t, "schema-runtime-"+suffix), Owner: authority,
		TaskRevision: 1, RuntimeRevision: 1, OperationGeneration: 1, RuntimeFence: 1,
		SafetyEpoch: 1, State: RuntimeCreated,
	}
}

func inspectRuntime(
	t *testing.T,
	harness *DeterministicHarness,
	fixture RuntimeFixture,
	authority RuntimeAuthority,
	requestVersion SchemaVersion,
	projectionVersion SchemaVersion,
) RuntimeSnapshot {
	t.Helper()
	snapshot, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: requestVersion, ProjectionVersion: projectionVersion,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: authority,
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return snapshot
}

func assertInspectError(
	t *testing.T,
	harness *DeterministicHarness,
	fixture RuntimeFixture,
	authority RuntimeAuthority,
	requestVersion SchemaVersion,
	projectionVersion SchemaVersion,
	code ErrorCode,
) {
	t.Helper()
	_, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: requestVersion, ProjectionVersion: projectionVersion,
		PersonalWorkspaceID: fixture.PersonalWorkspaceID, RuntimeRunID: fixture.RuntimeRunID, Authority: authority,
	})
	assertErrorCode(t, err, code)
}
