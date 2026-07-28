package runtimeexecution

import (
	"context"
	"testing"
	"time"
)

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

	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, Runtimes: []RuntimeFixture{normal, lossy, unknownEvidence, unknownVariant},
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
