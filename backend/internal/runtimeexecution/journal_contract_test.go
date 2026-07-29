package runtimeexecution

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestNilRuntimeCommandIsInvalidRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 1, 45, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "nil-command-caller", 5)
	start := standardStart(t, now, authority, "nil-command")
	harness := harnessForStart(t, now, authority, start)

	assertErrorCode(t, executeError(harness, nil), ErrorInvalidRequest)
}

func TestExecuteRejectsCanonicalStartUntilSchedulerGrantIsAttached(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "grant-free-owner", 4)
	input := standardStart(t, now, authority, "grant-free").StartRuntimeRunInput
	start, err := NewCanonicalStartRuntimeRun(input)
	if err != nil {
		t.Fatalf("construct grant-free canonical request: %v", err)
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now:      now,
		Runtimes: []RuntimeFixture{runtimeFixtureForStart(start, authority)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	assertErrorCode(t, executeError(harness, start), ErrorInvalidRequest)
	assertNoBusinessEffect(t, harness, start, start.ExpectedRuntimeRevision)
}

func TestDecision97HostileIngressPersistsSanitizedObservations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 1, 50, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "observation-caller", 5)
	start := standardStart(t, now, authority, "observation")
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 20, ObservationStart: 10},
		Runtimes:        []RuntimeFixture{runtimeFixtureForStart(start, authority)},
		AdmissionGrants: []AdmissionGrantFixture{grantFixtureForStart(start, now.Add(10*time.Minute), true)},
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	malformed := StartRuntimeRun{StartRuntimeRunInput: StartRuntimeRunInput{SchemaVersion: SchemaV1}}
	assertErrorCode(t, executeError(harness, malformed), ErrorInvalidRequest)

	unknownInput := start.StartRuntimeRunInput
	unknownInput.SchemaVersion = NewSchemaVersion(2, 0)
	unknown := mustStart(t, unknownInput)
	assertErrorCode(t, executeError(harness, unknown), ErrorUnsupportedSchema)

	intruder := mustTaskOrchestrationAuthority(t, "observation-intruder", 5)
	unauthorizedInput := start.StartRuntimeRunInput
	unauthorizedInput.Authority = intruder
	unauthorized := mustStart(t, unauthorizedInput)
	assertErrorCode(t, executeError(harness, unauthorized), ErrorAuthorizationDenied)

	executeDecision(t, harness, start)
	conflictingInput := start.StartRuntimeRunInput
	conflictingInput.OutputContractDigest = digest(90)
	conflicting := mustStart(t, conflictingInput)
	assertErrorCode(t, executeError(harness, conflicting), ErrorIntegrityConflict)

	observations := harness.IngressObservations()
	wantKinds := []IngressObservationKind{
		IngressMalformed, IngressUnsupportedSchema, IngressAuthorizationDenied, IngressIntegrityConflict,
	}
	if len(observations) != len(wantKinds) {
		t.Fatalf("ingress observations = %#v", observations)
	}
	for index, observation := range observations {
		if observation.ID.String() != fmt.Sprintf("runtime-observation-%06d", index+10) || observation.Kind != wantKinds[index] {
			t.Fatalf("observation %d = %#v", index, observation)
		}
	}
	for range 100 {
		assertErrorCode(t, executeError(harness, malformed), ErrorInvalidRequest)
	}
	bounded := harness.IngressObservations()
	if len(bounded) != 64 || bounded[len(bounded)-1].ID.String() != "runtime-observation-000073" {
		t.Fatalf("bounded ingress observations = %#v", bounded)
	}
}

func TestDecision97RequestPersistenceMatrix(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "matrix-caller", 5)
	accepted := standardStart(t, now, authority, "matrix-accepted")
	policyRejected := standardStart(t, now, authority, "matrix-policy")
	staleRevision := standardStart(t, now, authority, "matrix-stale-revision")
	staleGrant := standardStart(t, now, authority, "matrix-stale-grant")
	afterHostile := standardStart(t, now, authority, "matrix-after-hostile")

	staleGrant.AdmissionGrant.Generation = 1
	updatedGrant := staleGrant
	updatedGrant.AdmissionGrant.AdmissionGrantID = mustAdmissionGrantID(t, "grant-matrix-stale-grant-current")
	updatedGrant.AdmissionGrant.Generation = 2
	if updatedGrant.CanonicalRequestDigest != staleGrant.CanonicalRequestDigest {
		t.Fatal("replaceable Admission Grant entered the canonical start digest")
	}

	runtimes := []RuntimeFixture{
		runtimeFixtureForStart(accepted, authority),
		runtimeFixtureForStart(policyRejected, authority),
		runtimeFixtureForStart(staleRevision, authority),
		runtimeFixtureForStart(staleGrant, authority),
		runtimeFixtureForStart(afterHostile, authority),
	}
	runtimes[2].RuntimeRevision++
	grants := []AdmissionGrantFixture{
		grantFixtureForStart(accepted, now.Add(10*time.Minute), true),
		grantFixtureForStart(policyRejected, now.Add(-time.Second), true),
		grantFixtureForStart(staleRevision, now.Add(10*time.Minute), true),
		grantFixtureForStart(staleGrant, now.Add(10*time.Minute), false),
		grantFixtureForStart(updatedGrant, now.Add(10*time.Minute), true),
		grantFixtureForStart(afterHostile, now.Add(10*time.Minute), true),
	}
	harness, err := NewDeterministicHarness(HarnessConfig{
		Now: now, IDs: DeterministicIDConfig{DecisionStart: 100}, Runtimes: runtimes, AdmissionGrants: grants,
	})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}

	acceptedDecision := executeDecision(t, harness, accepted)
	assertDecision(t, acceptedDecision, "runtime-decision-000100", DecisionAccepted, CommandRejectionNone)

	policyDecision := executeDecision(t, harness, policyRejected)
	assertDecision(t, policyDecision, "runtime-decision-000101", DecisionRejected, CommandRejectionPolicy)
	assertNoBusinessEffect(t, harness, policyRejected, policyRejected.ExpectedRuntimeRevision)
	policyReplay := executeDecision(t, harness, policyRejected)
	if policyReplay.Fact != policyDecision.Fact {
		t.Fatalf("authenticated canonical rejection did not replay: %#v", policyReplay.Fact)
	}

	staleDecision := executeDecision(t, harness, staleRevision)
	assertDecision(t, staleDecision, "runtime-decision-000102", DecisionRejected, CommandRejectionStaleRevision)
	assertNoBusinessEffect(t, harness, staleRevision, staleRevision.ExpectedRuntimeRevision+1)
	if replay := executeDecision(t, harness, staleRevision); replay.Fact != staleDecision.Fact {
		t.Fatalf("stale revision rejection did not replay: %#v", replay.Fact)
	}

	staleGrantDecision := executeDecision(t, harness, staleGrant)
	assertDecision(t, staleGrantDecision, "runtime-decision-000103", DecisionRejected, CommandRejectionStaleGrant)
	if staleGrantDecision.Fact.Retry != RetryWithUpdatedGrant {
		t.Fatalf("stale grant retry = %v", staleGrantDecision.Fact.Retry)
	}
	assertNoBusinessEffect(t, harness, staleGrant, staleGrant.ExpectedRuntimeRevision)
	if replay := executeDecision(t, harness, staleGrant); replay.Fact != staleGrantDecision.Fact {
		t.Fatalf("same stale grant did not replay: %#v", replay.Fact)
	}
	updatedGrantDecision := executeDecision(t, harness, updatedGrant)
	assertDecision(t, updatedGrantDecision, "runtime-decision-000104", DecisionAccepted, CommandRejectionNone)

	intruder := mustTaskOrchestrationAuthority(t, "matrix-intruder", authority.generation)
	unauthorized := afterHostile
	unauthorized.Authority = intruder
	unauthorized.CanonicalRequestDigest, err = computeStartDigest(unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, unauthorized), ErrorAuthorizationDenied)
	missing := standardStart(t, now, authority, "matrix-missing")
	assertErrorCode(t, executeError(harness, missing), ErrorAuthorizationDenied)

	malformed := StartRuntimeRun{StartRuntimeRunInput: StartRuntimeRunInput{SchemaVersion: SchemaV1}}
	assertErrorCode(t, executeError(harness, malformed), ErrorInvalidRequest)

	unknownMajor := afterHostile
	unknownMajor.SchemaVersion = NewSchemaVersion(2, 0)
	unknownMajor.CanonicalRequestDigest, err = computeStartDigest(unknownMajor)
	if err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, unknownMajor), ErrorUnsupportedSchema)
	unknownMissing := missing
	unknownMissing.SchemaVersion = NewSchemaVersion(2, 0)
	unknownMissing.CanonicalRequestDigest, err = computeStartDigest(unknownMissing)
	if err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, unknownMissing), ErrorUnsupportedSchema)

	conflicting := accepted
	conflicting.OutputContractDigest = digest(99)
	conflicting.CanonicalRequestDigest, err = computeStartDigest(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	assertErrorCode(t, executeError(harness, conflicting), ErrorIntegrityConflict)
	if replay := executeDecision(t, harness, accepted); replay.Fact != acceptedDecision.Fact {
		t.Fatalf("conflict overwrote the original binding: %#v", replay.Fact)
	}

	afterHostileDecision := executeDecision(t, harness, afterHostile)
	assertDecision(t, afterHostileDecision, "runtime-decision-000105", DecisionAccepted, CommandRejectionNone)
}

func runtimeFixtureForStart(start StartRuntimeRun, authority RuntimeAuthority) RuntimeFixture {
	return RuntimeFixture{
		PersonalWorkspaceID: start.PersonalWorkspaceID, TaskID: start.TaskID,
		PhaseRunID: start.PhaseRunID, RuntimeRunID: start.RuntimeRunID, Owner: authority,
		TaskRevision: start.ExpectedTaskRevision, RuntimeRevision: start.ExpectedRuntimeRevision,
		OperationGeneration: start.ExpectedOperationGeneration, RuntimeFence: start.ExpectedRuntimeFence,
		SafetyEpoch: start.ReleaseSafetyEpoch, State: RuntimeCreated,
	}
}

func grantFixtureForStart(start StartRuntimeRun, expiresAt time.Time, current bool) AdmissionGrantFixture {
	return AdmissionGrantFixture{
		AdmissionGrantID: start.AdmissionGrant.AdmissionGrantID, WorkItemID: start.AdmissionGrant.WorkItemID,
		Generation: start.AdmissionGrant.Generation, PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
		CanonicalStartDigest: start.CanonicalRequestDigest, ExpiresAt: expiresAt, Current: current,
	}
}

func executeDecision(t *testing.T, harness *DeterministicHarness, command RuntimeCommand) RuntimeDecision {
	t.Helper()
	decision, err := harness.Runtime.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return decision
}

func executeError(harness *DeterministicHarness, command RuntimeCommand) error {
	_, err := harness.Runtime.Execute(context.Background(), command)
	return err
}

func assertDecision(t *testing.T, decision RuntimeDecision, id string, disposition DecisionDisposition, rejection CommandRejection) {
	t.Helper()
	if decision.Fact.DecisionID.String() != id || decision.Fact.Disposition != disposition || decision.Fact.Rejection != rejection {
		t.Fatalf("decision = %#v, want id=%s disposition=%v rejection=%v", decision.Fact, id, disposition, rejection)
	}
}

func assertNoBusinessEffect(t *testing.T, harness *DeterministicHarness, command StartRuntimeRun, revision RuntimeRevision) {
	t.Helper()
	snapshot, err := harness.Runtime.Inspect(context.Background(), RuntimeRunRef{
		SchemaVersion: SchemaV1, ProjectionVersion: SnapshotSchemaCurrent,
		PersonalWorkspaceID: command.PersonalWorkspaceID, RuntimeRunID: command.RuntimeRunID, Authority: command.Authority,
	})
	if err != nil {
		t.Fatalf("inspect rejected command: %v", err)
	}
	if snapshot.RuntimeRevision != revision || snapshot.State != RuntimeCreated || snapshot.Outcome != RuntimeOutcomeNone ||
		snapshot.Operation.Status != OperationUnbound || snapshot.Lease.AcquireStatus != LeaseNotRequested ||
		snapshot.Deadline != (time.Time{}) || snapshot.LeaseAcquireBy != (time.Time{}) {
		t.Fatalf("rejected command had a business side effect: %#v", snapshot)
	}
}

func assertErrorCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var runtimeError *Error
	if !errors.As(err, &runtimeError) || runtimeError.Code() != code {
		t.Fatalf("error = %v, want code %v", err, code)
	}
}
