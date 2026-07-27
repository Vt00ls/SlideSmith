package taskorchestration_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestRuntimeAdapterConsumesExactEnactmentAndReturnsTypedEvidence(t *testing.T) {
	ref := downstreamEnactmentRef(t, "runtime-operation", taskorchestration.EnactmentRuntimeExecution,
		taskorchestration.RuntimeFence(7))
	record := taskorchestration.RuntimeEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "runtime-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "runtime-authority"),
			Generation:  3,
		},
		TaskID:             downstreamTaskID(t, "task-runtime-adapter"),
		PhaseRunID:         downstreamPhaseRunID(t, "phase-run-runtime-adapter"),
		PhaseRunGeneration: 5,
		PhaseRunFence:      6,
		RuntimeRunID:       downstreamRuntimeRunID(t, "runtime-run-adapter"),
		RuntimeBindingID:   downstreamRuntimeBindingID(t, "runtime-binding-adapter"),
		RuntimeBindingDigest: downstreamEvidenceRef(
			t, "runtime-binding-digest-adapter", taskorchestration.EvidenceRuntime,
		).Digest,
		ImmutableInputManifestDigest: downstreamEvidenceRef(
			t, "runtime-input-manifest-adapter", taskorchestration.EvidenceRuntime,
		).Digest,
		ExecutionNodeID: downstreamExecutionNodeID(t, "runtime-node-adapter"),
		SandboxLeaseID:  downstreamSandboxLeaseID(t, "runtime-lease-adapter"),
		OutputManifestDigest: downstreamEvidenceRef(
			t, "runtime-output-manifest-adapter", taskorchestration.EvidenceRuntime,
		).Digest,
		OperationID:        ref.OperationID,
		ActivityGeneration: ref.ActivityGeneration,
		Generation:         3,
		Fence:              7,
		SafetyEpoch:        2,
		Outcome:            taskorchestration.RuntimeRunSucceeded,
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{
				Evidence:   downstreamEvidenceRef(t, "admission-evidence", taskorchestration.EvidenceScheduling),
				Generation: 2,
				Fence:      4,
			},
		},
	}
	record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
	port := &runtimeEvidencePortDouble{record: record}
	adapter := taskorchestration.NewRuntimeEvidenceAdapter(port, prerequisiteAuthority(record.Prerequisites))

	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatalf("enact Runtime operation: %v", err)
	}
	if evidence.TaskID != record.TaskID || evidence.PhaseRunID != record.PhaseRunID ||
		evidence.RuntimeRunID != record.RuntimeRunID || evidence.OperationID != ref.OperationID ||
		evidence.RuntimeBindingID != record.RuntimeBindingID ||
		evidence.RuntimeBindingDigest != record.RuntimeBindingDigest ||
		evidence.ImmutableInputManifestDigest != record.ImmutableInputManifestDigest ||
		evidence.ExecutionNodeID != record.ExecutionNodeID || evidence.SandboxLeaseID != record.SandboxLeaseID ||
		evidence.OutputManifestDigest != record.OutputManifestDigest ||
		evidence.Outcome != taskorchestration.RuntimeRunSucceeded ||
		evidence.Evidence.Kind != taskorchestration.EvidenceRuntime ||
		evidence.Evidence.Digest != record.EvidenceDigest || len(evidence.Prerequisites) != 1 {
		t.Fatalf("Runtime evidence lost exact bindings: %#v", evidence)
	}

	replayed, err := adapter.Enact(context.Background(), ref)
	if err != nil || !reflect.DeepEqual(replayed, evidence) || port.calls != 1 {
		t.Fatalf("exact duplicate did not replay evidence: evidence=%#v calls=%d err=%v", replayed, port.calls, err)
	}
}

func TestRuntimeAdapterEvidenceCannotCompletePhaseWithoutValidation(t *testing.T) {
	now := time.Date(2026, time.July, 27, 8, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "runtime-owner"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "runtime-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "runtime-start", "runtime-task", now), owner, pinned,
	)); err != nil {
		t.Fatal(err)
	}
	workHeader := intentHeader(t, "runtime-work", "runtime-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, taskorchestration.NewWorkerAuthority(downstreamAuthorityID(t, "runtime-worker"), 1),
		downstreamOperationID(t, "runtime-work-available"),
	))
	if err != nil {
		t.Fatal(err)
	}
	view := queryAggregate(t, harness, "runtime-task", owner)
	run := view.PhaseRuns[0]
	record := taskorchestration.RuntimeEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1, EvidenceID: downstreamEvidenceID(t, "runtime-success-evidence"),
		Producer: taskorchestration.EvidenceProducer{AuthorityID: downstreamAuthorityID(t, "generation-runtime-authority"), Generation: 1},
		TaskID:   view.TaskID, PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
		PhaseRunFence: run.Fence, RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID,
		OperationID: work.EnactmentRefs[0].OperationID, ActivityGeneration: view.ActivityGeneration,
		Generation: 1, Fence: taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
		Outcome: taskorchestration.RuntimeRunSucceeded,
	}
	bindRuntimeEvidenceContract(t, &record, "runtime-success")
	record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
	adapter := taskorchestration.NewRuntimeEvidenceAdapter(
		&runtimeEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)
	evidence, err := adapter.Enact(context.Background(), work.EnactmentRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	evidenceHeader := intentHeader(t, "runtime-evidence-decision", "runtime-task", now.Add(2*time.Second))
	evidenceHeader.ExpectedTaskRevision = 2
	intent, err := evidence.Intent(evidenceHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Mutations.Decide(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	view = queryAggregate(t, harness, "runtime-task", owner)
	if view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunRunning ||
		view.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunSucceeded {
		t.Fatalf("Runtime evidence acquired Phase authority: %#v", view.PhaseRuns[0])
	}
}

func TestRuntimeAdapterEvidenceFailsClosedAcrossScopeAndProducerAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 27, 8, 15, 0, 0, time.UTC)
	task := downstreamTaskID(t, "runtime-scope-task")
	phaseRun := downstreamPhaseRunID(t, "runtime-scope-phase")
	runtimeRun := downstreamRuntimeRunID(t, "runtime-scope-run")
	ref := downstreamEnactmentRef(t, "runtime-scope-operation",
		taskorchestration.EnactmentRuntimeExecution, taskorchestration.RuntimeFence(7))
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "runtime-scope-owner"), 1)
	expectedProducer := taskorchestration.NewRuntimeAuthority(
		downstreamAuthorityID(t, "runtime-expected-producer"), 3,
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 4, ActivityGeneration: ref.ActivityGeneration, SafetyEpoch: 2,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 5, Fence: 6, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: ref.OperationID, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: expectedProducer, Generation: 3, Fence: 7, SafetyEpoch: 2,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := taskorchestration.RuntimeEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "runtime-scope-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "runtime-unauthorized-producer"), Generation: 3,
		},
		TaskID: task, PhaseRunID: phaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
		RuntimeRunID: runtimeRun, OperationID: ref.OperationID,
		ActivityGeneration: ref.ActivityGeneration, Generation: 3, Fence: 7,
		SafetyEpoch: 2, Outcome: taskorchestration.RuntimeRunSucceeded,
	}
	bindRuntimeEvidenceContract(t, &record, "runtime-scope")
	record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
	adapter := taskorchestration.NewRuntimeEvidenceAdapter(
		&runtimeEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)
	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	crossTaskHeader := intentHeader(t, "runtime-cross-task", "runtime-other-task", now)
	crossTaskHeader.ActivityGeneration = ref.ActivityGeneration
	_, err = evidence.Intent(crossTaskHeader)
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamCorruptEvidence)

	header := intentHeader(t, "runtime-unauthorized", task.String(), now)
	header.ExpectedTaskRevision = 4
	header.ActivityGeneration = ref.ActivityGeneration
	intent, err := evidence.Intent(header)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Mutations.Decide(context.Background(), intent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
		t.Fatalf("unauthorized producer error = %T %v", err, err)
	}

	crossPhaseRecord := record
	crossPhaseRecord.EvidenceID = downstreamEvidenceID(t, "runtime-cross-phase-evidence")
	crossPhaseRecord.Producer.AuthorityID = downstreamAuthorityID(t, "runtime-expected-producer")
	crossPhaseRecord.PhaseRunID = downstreamPhaseRunID(t, "runtime-other-phase")
	crossPhaseRecord.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(crossPhaseRecord)
	crossPhaseAdapter := taskorchestration.NewRuntimeEvidenceAdapter(
		&runtimeEvidencePortDouble{record: crossPhaseRecord},
		prerequisiteAuthority(crossPhaseRecord.Prerequisites),
	)
	crossPhaseEvidence, err := crossPhaseAdapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	header.DecisionRequestID, err = taskorchestration.NewDecisionRequestID("runtime-cross-phase")
	if err != nil {
		t.Fatal(err)
	}
	crossPhaseIntent, err := crossPhaseEvidence.Intent(header)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Mutations.Decide(context.Background(), crossPhaseIntent)
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorEvidenceScopeConflict {
		t.Fatalf("cross-Phase evidence error = %T %v", err, err)
	}
	view, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || view.TaskRevision != 4 || view.DecisionCount != 0 || view.EvidenceDiagnosticCount != 2 {
		t.Fatalf("rejected producer/scope evidence changed Task: %#v err=%v", view, err)
	}
}

func TestRuntimeAdapterDoesNotTrustProducerDeclaredPrerequisiteOmission(t *testing.T) {
	ref := downstreamEnactmentRef(t, "runtime-prerequisite-authority-operation",
		taskorchestration.EnactmentRuntimeExecution, taskorchestration.RuntimeFence(7))
	record := taskorchestration.RuntimeEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "runtime-prerequisite-authority-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "runtime-prerequisite-authority"), Generation: 3,
		},
		TaskID:             downstreamTaskID(t, "runtime-prerequisite-task"),
		PhaseRunID:         downstreamPhaseRunID(t, "runtime-prerequisite-phase"),
		PhaseRunGeneration: 5, PhaseRunFence: 6,
		RuntimeRunID: downstreamRuntimeRunID(t, "runtime-prerequisite-run"),
		OperationID:  ref.OperationID, ActivityGeneration: ref.ActivityGeneration,
		Generation: 3, Fence: 7, SafetyEpoch: 2, Outcome: taskorchestration.RuntimeRunSucceeded,
	}
	bindRuntimeEvidenceContract(t, &record, "runtime-prerequisite-authority")
	record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
	expected := taskorchestration.EvidencePrerequisite{
		Evidence:   downstreamEvidenceRef(t, "runtime-required-admission", taskorchestration.EvidenceScheduling),
		Generation: 2, Fence: 4,
	}
	adapter := taskorchestration.NewRuntimeEvidenceAdapter(
		&runtimeEvidencePortDouble{record: record},
		&prerequisiteAuthorityDouble{expected: []taskorchestration.EvidencePrerequisite{expected}},
	)

	_, err := adapter.Enact(context.Background(), ref)
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamPrerequisitePending)
}

type prerequisiteAuthorityDouble struct {
	expected []taskorchestration.EvidencePrerequisite
	err      error
}

func prerequisiteAuthority(
	expected []taskorchestration.EvidencePrerequisite,
) *prerequisiteAuthorityDouble {
	return &prerequisiteAuthorityDouble{
		expected: append([]taskorchestration.EvidencePrerequisite(nil), expected...),
	}
}

func (authority *prerequisiteAuthorityDouble) ExpectedPrerequisites(
	_ context.Context,
	_ taskorchestration.EnactmentRef,
) ([]taskorchestration.EvidencePrerequisite, error) {
	return append([]taskorchestration.EvidencePrerequisite(nil), authority.expected...), authority.err
}

func TestSchedulerAdapterReturnsEvidenceWithoutChangingTaskOrPhase(t *testing.T) {
	now := time.Date(2026, time.July, 27, 8, 30, 0, 0, time.UTC)
	task := downstreamTaskID(t, "scheduler-task")
	phaseRun := downstreamPhaseRunID(t, "scheduler-phase-run")
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "scheduler-owner"), 1)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 4, ActivityGeneration: 3, SafetyEpoch: 2,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 5, Fence: 6, Active: true,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatal(err)
	}

	for index, kind := range []taskorchestration.SchedulerEvidenceKind{
		taskorchestration.SchedulerClaimed,
		taskorchestration.SchedulerAdmitted,
		taskorchestration.SchedulerDeadlineElapsed,
		taskorchestration.SchedulerDeadLettered,
	} {
		ref := downstreamEnactmentRef(t, "scheduler-operation-"+string(rune('a'+index)),
			taskorchestration.EnactmentScheduling, taskorchestration.SchedulerFence(8))
		record := taskorchestration.SchedulerEvidenceRecord{
			SchemaVersion: taskorchestration.EvidenceSchemaV1,
			EvidenceID:    downstreamEvidenceID(t, "scheduler-evidence-"+string(rune('a'+index))),
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "scheduler-authority"), Generation: 7,
			},
			TaskID: task, PhaseRunID: phaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
			OperationID: ref.OperationID, ActivityGeneration: ref.ActivityGeneration,
			Generation: 7, Fence: 8, SafetyEpoch: 2, Kind: kind,
			WorkItemID: downstreamSchedulerWorkItemID(t, "scheduler-work-item-"+string(rune('a'+index))),
		}
		if kind == taskorchestration.SchedulerClaimed || kind == taskorchestration.SchedulerAdmitted {
			record.DeliveryClaimID = downstreamDeliveryClaimID(t, "scheduler-claim-"+string(rune('a'+index)))
		}
		if kind == taskorchestration.SchedulerAdmitted {
			record.AdmissionGrantID = downstreamAdmissionGrantID(t, "scheduler-grant-"+string(rune('a'+index)))
		}
		record.EvidenceDigest = taskorchestration.SchedulerEvidenceDigest(record)
		adapter := taskorchestration.NewSchedulerEvidenceAdapter(
			&schedulerEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
		)
		evidence, err := adapter.Enact(context.Background(), ref)
		if err != nil || evidence.Kind != kind || evidence.OperationID != ref.OperationID ||
			evidence.WorkItemID != record.WorkItemID || evidence.DeliveryClaimID != record.DeliveryClaimID ||
			evidence.AdmissionGrantID != record.AdmissionGrantID {
			t.Fatalf("Scheduler evidence kind %d = %#v, err=%v", kind, evidence, err)
		}
		header := intentHeader(t, "scheduler-evidence-intent-"+string(rune('a'+index)), task.String(), now)
		header.ActivityGeneration = ref.ActivityGeneration
		intent, err := evidence.Intent(header)
		if err != nil || intent.Kind() != taskorchestration.IntentAcceptSchedulingEvidence {
			t.Fatalf("Scheduler typed evidence intent = %#v, err=%v", intent, err)
		}
	}

	after, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Scheduler adapter changed Task or Phase: before=%#v after=%#v", before, after)
	}
}

func TestSchedulerAdapterBindsExactWorkItemClaimAndAdmissionGrant(t *testing.T) {
	ref := downstreamEnactmentRef(t, "scheduler-admission-operation",
		taskorchestration.EnactmentScheduling, taskorchestration.SchedulerFence(8))
	record := taskorchestration.SchedulerEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "scheduler-admission-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "scheduler-admission-authority"), Generation: 7,
		},
		TaskID:             downstreamTaskID(t, "scheduler-admission-task"),
		PhaseRunID:         downstreamPhaseRunID(t, "scheduler-admission-phase"),
		PhaseRunGeneration: 5, PhaseRunFence: 6,
		OperationID: ref.OperationID, ActivityGeneration: ref.ActivityGeneration,
		Generation: 7, Fence: 8, SafetyEpoch: 2, Kind: taskorchestration.SchedulerAdmitted,
		WorkItemID:       downstreamSchedulerWorkItemID(t, "scheduler-work-item"),
		DeliveryClaimID:  downstreamDeliveryClaimID(t, "scheduler-delivery-claim"),
		AdmissionGrantID: downstreamAdmissionGrantID(t, "scheduler-admission-grant"),
	}
	record.EvidenceDigest = taskorchestration.SchedulerEvidenceDigest(record)
	adapter := taskorchestration.NewSchedulerEvidenceAdapter(
		&schedulerEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)

	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.WorkItemID != record.WorkItemID ||
		evidence.DeliveryClaimID != record.DeliveryClaimID ||
		evidence.AdmissionGrantID != record.AdmissionGrantID {
		t.Fatalf("Scheduler evidence lost exact admission bindings: %#v", evidence)
	}
}

type schedulerEvidencePortDouble struct {
	record taskorchestration.SchedulerEvidenceRecord
	err    error
	calls  int
}

func TestPublicationAdapterRequiresExactActivationEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "publication-owner"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "publication-phase"), Kind: taskorchestration.PhasePublication,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "publication-start", "publication-task", now), owner, pinned,
	)); err != nil {
		t.Fatal(err)
	}
	workHeader := intentHeader(t, "publication-work", "publication-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, taskorchestration.NewWorkerAuthority(downstreamAuthorityID(t, "publication-worker"), 1),
		downstreamOperationID(t, "publication-work-available"),
	))
	if err != nil {
		t.Fatal(err)
	}
	view := queryAggregate(t, harness, "publication-task", owner)
	run := view.PhaseRuns[0]
	record := taskorchestration.PublicationEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "publication-activation-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "generation-publication-authority"), Generation: 1,
		},
		TaskID: view.TaskID, PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
		PhaseRunFence: run.Fence, OperationID: work.EnactmentRefs[0].OperationID,
		ActivityGeneration: view.ActivityGeneration, Generation: 1,
		Fence: taskorchestration.PublicationFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
		Outcome:           taskorchestration.PublicationActivated,
		ArtifactVersionID: downstreamArtifactVersionID(t, "artifact-version-activated"),
	}
	record.EvidenceDigest = taskorchestration.PublicationEvidenceDigest(record)
	invalidAdapter := taskorchestration.NewPublicationEvidenceAdapter(
		&publicationEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)
	_, err = invalidAdapter.Enact(context.Background(), work.EnactmentRefs[0])
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamCorruptEvidence)

	record.ArtifactManifestDigest = downstreamEvidenceRef(
		t, "artifact-manifest-activated", taskorchestration.EvidencePublication,
	).Digest
	record.EvidenceDigest = taskorchestration.PublicationEvidenceDigest(record)
	adapter := taskorchestration.NewPublicationEvidenceAdapter(
		&publicationEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)
	evidence, err := adapter.Enact(context.Background(), work.EnactmentRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	header := intentHeader(t, "publication-evidence-decision", "publication-task", now.Add(2*time.Second))
	header.ExpectedTaskRevision = 2
	intent, err := evidence.Intent(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Mutations.Decide(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	view = queryAggregate(t, harness, "publication-task", owner)
	if view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunSucceeded ||
		view.PhaseRuns[0].PublicationOutcome != taskorchestration.PublicationActivated ||
		view.LatestArtifactVersionID != record.ArtifactVersionID {
		t.Fatalf("exact activation evidence did not complete publication: %#v", view)
	}
}

func TestLatePublicationAdapterEvidenceCannotResurrectCancelledTask(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 15, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "late-publication-owner"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "late-publication-phase"), Kind: taskorchestration.PhasePublication,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "late-publication-start", "late-publication-task", now), owner, pinned,
	)); err != nil {
		t.Fatal(err)
	}
	workHeader := intentHeader(t, "late-publication-work", "late-publication-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, taskorchestration.NewWorkerAuthority(downstreamAuthorityID(t, "late-publication-worker"), 1),
		downstreamOperationID(t, "late-publication-work-available"),
	))
	if err != nil {
		t.Fatal(err)
	}
	beforeCancel := queryAggregate(t, harness, "late-publication-task", owner)
	run := beforeCancel.PhaseRuns[0]
	record := taskorchestration.PublicationEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "late-publication-activation"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "late-publication-authority"), Generation: 1,
		},
		TaskID: beforeCancel.TaskID, PhaseRunID: run.PhaseRunID,
		PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
		OperationID:        work.EnactmentRefs[0].OperationID,
		ActivityGeneration: beforeCancel.ActivityGeneration, Generation: 1,
		Fence: taskorchestration.PublicationFence(run.Fence), SafetyEpoch: beforeCancel.SafetyEpoch,
		Outcome:           taskorchestration.PublicationActivated,
		ArtifactVersionID: downstreamArtifactVersionID(t, "late-publication-artifact"),
		ArtifactManifestDigest: downstreamEvidenceRef(
			t, "late-publication-manifest", taskorchestration.EvidencePublication,
		).Digest,
	}
	record.EvidenceDigest = taskorchestration.PublicationEvidenceDigest(record)
	adapter := taskorchestration.NewPublicationEvidenceAdapter(
		&publicationEvidencePortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)
	lateEvidence, err := adapter.Enact(context.Background(), work.EnactmentRefs[0])
	if err != nil {
		t.Fatal(err)
	}

	cancelHeader := intentHeader(t, "late-publication-cancel", "late-publication-task", now.Add(2*time.Second))
	cancelHeader.ExpectedTaskRevision = 2
	cancelled, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.TaskProjection.Status != taskorchestration.TaskCancelled ||
		cancelled.TaskProjection.CancellationState != taskorchestration.CancellationCancelled ||
		len(cancelled.EnactmentRefs) != 0 {
		t.Fatalf("non-mutating publication cancellation was not terminal: %#v", cancelled)
	}
	terminal := queryAggregate(t, harness, "late-publication-task", owner)
	lateHeader := intentHeader(t, "late-publication-arrived", "late-publication-task", now.Add(4*time.Second))
	lateHeader.ExpectedTaskRevision = terminal.TaskRevision
	lateHeader.ActivityGeneration = lateEvidence.ActivityGeneration
	lateIntent, err := lateEvidence.Intent(lateHeader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Mutations.Decide(context.Background(), lateIntent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
		t.Fatalf("late publication error = %T %v", err, err)
	}
	after := queryAggregate(t, harness, "late-publication-task", owner)
	if after.TaskRevision != terminal.TaskRevision || after.Status != taskorchestration.TaskCancelled ||
		after.LatestArtifactVersionID != (taskorchestration.ArtifactVersionID{}) ||
		after.EvidenceDiagnosticCount != terminal.EvidenceDiagnosticCount+1 {
		t.Fatalf("late publication evidence resurrected Task: before=%#v after=%#v", terminal, after)
	}
}

type publicationEvidencePortDouble struct {
	record taskorchestration.PublicationEvidenceRecord
	err    error
	calls  int
}

func (port *publicationEvidencePortDouble) EnactPublication(
	_ context.Context,
	_ taskorchestration.EnactmentRef,
) (taskorchestration.PublicationEvidenceRecord, error) {
	port.calls++
	return port.record, port.err
}

func assertDownstreamErrorCode(t *testing.T, err error, want taskorchestration.DownstreamErrorCode) {
	t.Helper()
	var failure *taskorchestration.DownstreamError
	if !errors.As(err, &failure) || failure.Code() != want {
		t.Fatalf("downstream error = %T %v, want code %d", err, err, want)
	}
}

func TestTaskWorkspaceLifecycleAdapterUsesOpaqueC04CommitEvidence(t *testing.T) {
	ref := downstreamEnactmentRef(t, "c04-commit-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(7))
	request, result := c04CommitContractFixture(t, ref, "c04-task", "c04-phase-run", "c04-workspace", 5, 7)
	bindC04EnactmentPayload(t, &ref, request.Operation.RequestDigest)
	port := &taskWorkspaceLifecyclePortDouble{commit: result}
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		port,
		taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "c04-authority"), Generation: 5,
			},
			TaskID:             downstreamTaskID(t, "c04-task"),
			PhaseRunID:         downstreamPhaseRunID(t, "c04-phase-run"),
			PhaseRunGeneration: 6,
			PhaseRunFence:      7,
			SafetyEpoch:        2,
			Commit:             &request,
		},
	)

	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Outcome != taskorchestration.LifecycleEvidenceCommitted ||
		evidence.OperationID != ref.OperationID ||
		evidence.RevisionID.String() != string(result.RevisionID) ||
		evidence.CheckpointID.String() != string(result.CheckpointID) ||
		evidence.Generation != taskorchestration.TaskWorkspaceLifecycleGeneration(request.Generation) ||
		evidence.Fence != taskorchestration.TaskWorkspaceLifecycleFence(request.Fence) ||
		evidence.ObservedGeneration != taskorchestration.TaskWorkspaceLifecycleGeneration(result.Generation) ||
		evidence.ObservedFence != taskorchestration.TaskWorkspaceLifecycleFence(result.Fence) ||
		evidence.CommitProofDigest == (taskorchestration.EvidenceDigest{}) ||
		evidence.FenceProofDigest != (taskorchestration.EvidenceDigest{}) ||
		evidence.Evidence.Kind != taskorchestration.EvidenceTaskWorkspaceLifecycle ||
		port.commitCalls != 1 {
		t.Fatalf("opaque C04 commit evidence = %#v, calls=%d", evidence, port.commitCalls)
	}
	replayed, err := adapter.Enact(context.Background(), ref)
	if err != nil || !reflect.DeepEqual(replayed, evidence) || port.commitCalls != 1 {
		t.Fatalf("C04 exact replay = %#v, calls=%d, err=%v", replayed, port.commitCalls, err)
	}

	now := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "c04-owner"), 1)
	authority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		downstreamAuthorityID(t, "c04-authority"), 5,
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: evidence.TaskID, Owner: owner, TaskRevision: 10,
			ActivityGeneration: ref.ActivityGeneration, SafetyEpoch: evidence.SafetyEpoch,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: evidence.PhaseRunID, Generation: evidence.PhaseRunGeneration,
				Fence: evidence.PhaseRunFence, Active: true,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: evidence.OperationID, PhaseRunID: evidence.PhaseRunID, Authority: authority,
				Generation: evidence.Generation, Fence: evidence.Fence, SafetyEpoch: evidence.SafetyEpoch,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	header := intentHeader(t, "c04-real-fence-decision", evidence.TaskID.String(), now)
	header.ExpectedTaskRevision = 10
	header.ActivityGeneration = ref.ActivityGeneration
	intent, err := evidence.Intent(header)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := harness.Mutations.Decide(context.Background(), intent)
	if err != nil || len(decision.AcceptedEvidenceRefs) != 1 ||
		decision.AcceptedEvidenceRefs[0] != evidence.Evidence {
		t.Fatalf("C04 evidence with advanced result fence was not accepted: %#v err=%v", decision, err)
	}
}

func TestDecideGeneratedC04CommitEnactmentBindsTheExactCanonicalRequest(t *testing.T) {
	now := time.Date(2026, time.July, 27, 19, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "decide-c04-commit-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "decide-c04-commit-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "decide-c04-commit-phase"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(
		t, intentHeader(t, "decide-c04-commit-start", "decide-c04-commit-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatal(err)
	}
	workHeader := intentHeader(t, "decide-c04-commit-work", "decide-c04-commit-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "decide-c04-commit-work-operation"),
	))
	if err != nil {
		t.Fatal(err)
	}
	view := queryAggregate(t, harness, "decide-c04-commit-task", owner)
	run := view.PhaseRuns[0]
	runtimeHeader := intentHeader(
		t, "decide-c04-commit-runtime", "decide-c04-commit-task", now.Add(2*time.Second),
	)
	runtimeHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	acceptedRuntime, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		runtimeHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "decide-c04-commit-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "9191919191919191919191919191919191919191919191919191919191919191"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.RuntimeGeneration(run.Generation),
			Fence:      taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	requestRef := downstreamEnactmentRef(
		t, "decide-c04-commit-operation", taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(7),
	)
	request, result := c04CommitContractFixture(
		t, requestRef, "decide-c04-commit-task", run.PhaseRunID.String(), pinned.TaskWorkspaceID.String(), 7, 7,
	)
	commitBinding, err := taskorchestration.NewTaskWorkspaceLifecycleCommitRequestBinding(request)
	if err != nil {
		t.Fatalf("bind exact C04 commit request: %v", err)
	}
	validationHeader := intentHeader(
		t, "decide-c04-commit-validation", "decide-c04-commit-task", now.Add(3*time.Second),
	)
	validationHeader.ExpectedTaskRevision = acceptedRuntime.AcceptedTaskRevision
	decision, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader, pinned.Authorities.Validator, taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "decide-c04-commit-validation-evidence"), taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "9292929292929292929292929292929292929292929292929292929292929292"),
				),
				PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
				Generation: taskorchestration.ProducerGeneration(run.Generation),
				Fence:      taskorchestration.ValidationFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
				Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: commitBinding,
			},
		),
	)
	if err != nil || len(decision.EnactmentRefs) != 1 {
		t.Fatalf("commit exact C04 enactment: decision=%+v err=%v", decision, err)
	}
	ref := decision.EnactmentRefs[0]
	if ref.OperationID.String() != string(request.Operation.ID) ||
		"sha256:"+ref.PayloadDigest.String() != string(request.Operation.RequestDigest) {
		t.Fatalf("Decide changed the exact C04 request binding: ref=%+v request=%+v", ref, request.Operation)
	}
	port := &taskWorkspaceLifecyclePortDouble{commit: result}
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		port, taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, "generation-lifecycle-authority"), Generation: 1,
			},
			TaskID: taskID(t, "decide-c04-commit-task"), PhaseRunID: run.PhaseRunID,
			PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			SafetyEpoch: view.SafetyEpoch, Commit: &request,
		},
	)
	if _, err := adapter.Enact(context.Background(), ref); err != nil || port.commitCalls != 1 {
		t.Fatalf("Decide-generated C04 enactment was not consumable: calls=%d err=%v", port.commitCalls, err)
	}
}

func TestDecideGeneratedC04CancellationEnactmentBindsTheExactCanonicalRequest(t *testing.T) {
	now := time.Date(2026, time.July, 27, 19, 15, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "decide-c04-fence-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "decide-c04-fence-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "decide-c04-fence-phase"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(
		t, intentHeader(t, "decide-c04-fence-start", "decide-c04-fence-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatal(err)
	}
	workHeader := intentHeader(t, "decide-c04-fence-work", "decide-c04-fence-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "decide-c04-fence-work-operation"),
	))
	if err != nil {
		t.Fatal(err)
	}
	view := queryAggregate(t, harness, "decide-c04-fence-task", owner)
	run := view.PhaseRuns[0]
	requestRef := downstreamEnactmentRef(
		t, "decide-c04-fence-operation", taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(1),
	)
	request, result := c04FenceContractFixture(
		t, requestRef, "decide-c04-fence-task", run.PhaseRunID.String(), pinned.TaskWorkspaceID.String(), 1, 1,
	)
	fenceBinding, err := taskorchestration.NewTaskWorkspaceLifecycleFenceRequestBinding(request)
	if err != nil {
		t.Fatalf("bind exact C04 fence request: %v", err)
	}
	cancelHeader := intentHeader(t, "decide-c04-fence-cancel", "decide-c04-fence-task", now.Add(2*time.Second))
	cancelHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	cancelled, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewCancelTaskByUserWithLifecycleFenceIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested, fenceBinding,
		),
	)
	if err != nil {
		t.Fatalf("commit exact C04 fence enactment: %v", err)
	}
	var ref taskorchestration.EnactmentRef
	for _, candidate := range cancelled.EnactmentRefs {
		if candidate.Kind == taskorchestration.EnactmentTaskWorkspaceLifecycle {
			ref = candidate
			break
		}
	}
	if ref.OperationID.String() != string(request.Operation.ID) ||
		"sha256:"+ref.PayloadDigest.String() != string(request.Operation.RequestDigest) {
		t.Fatalf("Decide changed the exact C04 fence binding: ref=%+v request=%+v", ref, request.Operation)
	}
	port := &taskWorkspaceLifecyclePortDouble{fence: result}
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		port, taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, "generation-lifecycle-authority"), Generation: 1,
			},
			TaskID: taskID(t, "decide-c04-fence-task"), PhaseRunID: run.PhaseRunID,
			PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			SafetyEpoch: view.SafetyEpoch, Fence: &request,
		},
	)
	if _, err := adapter.Enact(context.Background(), ref); err != nil || port.fenceCalls != 1 {
		t.Fatalf("Decide-generated C04 fence was not consumable: calls=%d err=%v", port.fenceCalls, err)
	}
}

func TestC04ReconstructionAdvancesLifecycleLineageForTheNextExactCommit(t *testing.T) {
	now := time.Date(2026, time.July, 27, 19, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "reconstruction-lineage-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "reconstruction-lineage-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "reconstruction-lineage-publication"), Kind: taskorchestration.PhasePublication,
	}})
	pinned.ExecutionLock.PipelineContract.ManualEditEntryPhase = phaseKey(
		t, "reconstruction-lineage-manual-edit",
	)
	pinned.ExecutionLock.PipelineContract.ManualEditPhases = []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "reconstruction-lineage-manual-edit"), Kind: taskorchestration.PhaseMutating,
		ValidationContract: taskorchestration.PhaseValidationAllRuntimeRunsSucceeded, RequiredRuntimeRuns: 1,
	}}
	started, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(
		t, intentHeader(t, "reconstruction-lineage-start", "reconstruction-lineage-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatal(err)
	}
	publicationHeader := intentHeader(
		t, "reconstruction-lineage-publication-work", "reconstruction-lineage-task", now.Add(time.Second),
	)
	publicationHeader.ExpectedTaskRevision = started.AcceptedTaskRevision
	publicationWork, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
			publicationHeader, worker, operationID(t, "reconstruction-lineage-publication-operation"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	view := queryAggregate(t, harness, "reconstruction-lineage-task", owner)
	publicationRun := view.PhaseRuns[0]
	reconstructionSeed := downstreamEnactmentRef(
		t, "reconstruction-lineage-operation", taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(4),
	)
	reconstructionRequest, reconstructionResult := c04ReconstructionContractFixture(
		t, reconstructionSeed, "reconstruction-lineage-task", pinned.TaskWorkspaceID.String(), 4, 4,
	)
	artifact := artifactVersionID(
		t, string(reconstructionRequest.Intent.ArtifactVersionInput.ArtifactVersionID),
	)
	publishedHeader := intentHeader(
		t, "reconstruction-lineage-published", "reconstruction-lineage-task", now.Add(2*time.Second),
	)
	publishedHeader.ExpectedTaskRevision = publicationWork.AcceptedTaskRevision
	published, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewAcceptPublicationEvidenceIntent(
			publishedHeader, pinned.Authorities.Publication, taskorchestration.PublicationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "reconstruction-lineage-publication-evidence"),
					taskorchestration.EvidencePublication,
					evidenceDigest(t, "9393939393939393939393939393939393939393939393939393939393939393"),
				),
				PhaseRunID: publicationRun.PhaseRunID, PhaseRunGeneration: publicationRun.Generation,
				PhaseRunFence: publicationRun.Fence, OperationID: publicationWork.EnactmentRefs[0].OperationID,
				Generation: taskorchestration.ProducerGeneration(publicationRun.Generation),
				Fence:      taskorchestration.PublicationFence(publicationRun.Fence), SafetyEpoch: view.SafetyEpoch,
				Outcome: taskorchestration.PublicationActivated, ArtifactVersionID: artifact,
			},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	reconstructionBinding, err := taskorchestration.NewTaskWorkspaceReconstructionRequestBinding(
		reconstructionRequest,
	)
	if err != nil {
		t.Fatal(err)
	}
	beginHeader := intentHeader(
		t, "reconstruction-lineage-begin", "reconstruction-lineage-task", now.Add(3*time.Second),
	)
	beginHeader.ExpectedTaskRevision = published.AcceptedTaskRevision
	beginHeader.ActivityGeneration = 2
	reconstructionDecision, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewBeginManualEditAfterExpiryIntent(
			beginHeader, owner, artifact, reconstructionBinding,
		),
	)
	if err != nil || len(reconstructionDecision.EnactmentRefs) != 1 {
		t.Fatalf("begin exact reconstruction: decision=%+v err=%v", reconstructionDecision, err)
	}
	reconstructionRef := reconstructionDecision.EnactmentRefs[0]
	if reconstructionRef.OperationID.String() != string(reconstructionRequest.Operation.ID) ||
		"sha256:"+reconstructionRef.PayloadDigest.String() != string(reconstructionRequest.Operation.RequestDigest) {
		t.Fatalf("reconstruction outbox changed exact request: ref=%+v", reconstructionRef)
	}
	reconstructionPort := &taskWorkspaceLifecyclePortDouble{reconstruction: reconstructionResult}
	reconstructionAdapter := taskorchestration.NewTaskWorkspaceReconstructionEvidenceAdapter(
		reconstructionPort, taskorchestration.TaskWorkspaceReconstructionAdapterBinding{
			Enactment: reconstructionRef,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, "generation-lifecycle-authority"), Generation: 1,
			},
			TaskID: taskID(t, "reconstruction-lineage-task"), SafetyEpoch: view.SafetyEpoch,
			Request: &reconstructionRequest,
		},
	)
	reconstructionEvidence, err := reconstructionAdapter.Enact(context.Background(), reconstructionRef)
	if err != nil {
		t.Fatalf("enact exact reconstruction: %v", err)
	}
	evidenceHeader := intentHeader(
		t, "reconstruction-lineage-evidence", "reconstruction-lineage-task", now.Add(4*time.Second),
	)
	evidenceHeader.ExpectedTaskRevision = reconstructionDecision.AcceptedTaskRevision
	evidenceHeader.ActivityGeneration = 2
	reconstructionIntent, err := reconstructionEvidence.Intent(evidenceHeader)
	if err != nil {
		t.Fatalf("convert C04 reconstruction evidence: %v", err)
	}
	acceptedReconstruction, err := harness.Mutations.Decide(context.Background(), reconstructionIntent)
	if err != nil {
		t.Fatalf("accept C04 reconstruction evidence: %v", err)
	}
	view = queryAggregate(t, harness, "reconstruction-lineage-task", owner)
	if view.TaskWorkspaceLifecycleGeneration != reconstructionEvidence.ObservedGeneration ||
		view.TaskWorkspaceLifecycleFence != reconstructionEvidence.ObservedFence {
		t.Fatalf("advanced C04 lineage was not projected: view=%+v evidence=%+v", view, reconstructionEvidence)
	}
	manualHeader := intentHeader(
		t, "reconstruction-lineage-manual-work", "reconstruction-lineage-task", now.Add(5*time.Second),
	)
	manualHeader.ExpectedTaskRevision = acceptedReconstruction.AcceptedTaskRevision
	manualHeader.ActivityGeneration = 2
	manualWork, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
			manualHeader, worker, operationID(t, "reconstruction-lineage-runtime-operation"),
		),
	)
	if err != nil {
		t.Fatalf("start manual-edit mutation after reconstruction: %v", err)
	}
	view = queryAggregate(t, harness, "reconstruction-lineage-task", owner)
	manualRun := view.PhaseRuns[len(view.PhaseRuns)-1]
	runtimeHeader := intentHeader(
		t, "reconstruction-lineage-runtime-evidence", "reconstruction-lineage-task", now.Add(6*time.Second),
	)
	runtimeHeader.ExpectedTaskRevision = manualWork.AcceptedTaskRevision
	runtimeHeader.ActivityGeneration = 2
	acceptedRuntime, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
			runtimeHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "reconstruction-lineage-runtime-evidence-ref"), taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "9494949494949494949494949494949494949494949494949494949494949494"),
				),
				PhaseRunID: manualRun.PhaseRunID, PhaseRunGeneration: manualRun.Generation,
				PhaseRunFence: manualRun.Fence, RuntimeRunID: manualRun.RuntimeRuns[0].RuntimeRunID,
				OperationID: manualWork.EnactmentRefs[0].OperationID,
				Generation:  taskorchestration.RuntimeGeneration(manualRun.Generation),
				Fence:       taskorchestration.RuntimeFence(manualRun.Fence), SafetyEpoch: view.SafetyEpoch,
				Outcome: taskorchestration.RuntimeRunSucceeded,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept manual-edit Runtime evidence: %v", err)
	}
	commitSeed := downstreamEnactmentRef(
		t, "reconstruction-lineage-commit-operation", taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(view.TaskWorkspaceLifecycleFence),
	)
	commitRequest, commitResult := c04CommitContractFixture(
		t, commitSeed, "reconstruction-lineage-task", manualRun.PhaseRunID.String(),
		pinned.TaskWorkspaceID.String(), taskworkspace.Generation(view.TaskWorkspaceLifecycleGeneration),
		taskworkspace.Fence(view.TaskWorkspaceLifecycleFence),
	)
	currentRevision := taskworkspace.RevisionID(view.LatestRevisionID.String())
	commitRequest.BaseRevisionID = currentRevision
	commitRequest.ExpectedCurrentRevision = currentRevision
	commitRequest.ValidationEvidence.BaseRevisionID = currentRevision
	commitRequest.ValidationEvidence.Digest = commitRequest.ValidationEvidence.CanonicalDigest()
	commitRequest.Operation.RequestDigest = commitRequest.CanonicalRequestDigest()
	commitResult.BaseRevisionID = currentRevision
	commitResult.PredecessorRevisionID = currentRevision
	commitResult.ValidationEvidenceDigest = commitRequest.ValidationEvidence.Digest
	commitResult.Operation = commitRequest.Operation
	commitBinding, err := taskorchestration.NewTaskWorkspaceLifecycleCommitRequestBinding(commitRequest)
	if err != nil {
		t.Fatalf("bind post-reconstruction commit request: %v", err)
	}
	validationHeader := intentHeader(
		t, "reconstruction-lineage-validation", "reconstruction-lineage-task", now.Add(7*time.Second),
	)
	validationHeader.ExpectedTaskRevision = acceptedRuntime.AcceptedTaskRevision
	validationHeader.ActivityGeneration = 2
	commitDecision, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader, pinned.Authorities.Validator, taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "reconstruction-lineage-validation-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "9595959595959595959595959595959595959595959595959595959595959595"),
				),
				PhaseRunID: manualRun.PhaseRunID, PhaseRunGeneration: manualRun.Generation,
				PhaseRunFence: manualRun.Fence, Generation: taskorchestration.ProducerGeneration(manualRun.Generation),
				Fence: taskorchestration.ValidationFence(manualRun.Fence), SafetyEpoch: view.SafetyEpoch,
				Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: commitBinding,
			},
		),
	)
	if err != nil || len(commitDecision.EnactmentRefs) != 1 {
		t.Fatalf("commit post-reconstruction C04 outbox: decision=%+v err=%v", commitDecision, err)
	}
	commitRef := commitDecision.EnactmentRefs[0]
	commitPort := &taskWorkspaceLifecyclePortDouble{commit: commitResult}
	commitAdapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		commitPort, taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: commitRef,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: authorityID(t, "generation-lifecycle-authority"), Generation: 1,
			},
			TaskID: taskID(t, "reconstruction-lineage-task"), PhaseRunID: manualRun.PhaseRunID,
			PhaseRunGeneration: manualRun.Generation, PhaseRunFence: manualRun.Fence,
			SafetyEpoch: view.SafetyEpoch, Commit: &commitRequest,
		},
	)
	if _, err := commitAdapter.Enact(context.Background(), commitRef); err != nil || commitPort.commitCalls != 1 {
		t.Fatalf("advanced-lineage C04 commit was not consumable: calls=%d err=%v", commitPort.commitCalls, err)
	}
}

func TestExactC04RequestBindingsRejectSelfConsistentButUnconsumableScope(t *testing.T) {
	t.Run("commit validation Task", func(t *testing.T) {
		seed := downstreamEnactmentRef(
			t, "binding-scope-commit", taskorchestration.EnactmentTaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleFence(2),
		)
		request, _ := c04CommitContractFixture(
			t, seed, "binding-scope-task", "binding-scope-phase", "binding-scope-workspace", 2, 2,
		)
		request.ValidationEvidence.TaskID = taskworkspace.TaskID("other-binding-scope-task")
		request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		if _, err := taskorchestration.NewTaskWorkspaceLifecycleCommitRequestBinding(request); err == nil {
			t.Fatal("self-consistent commit with cross-Task validation scope was admitted")
		}
	})

	t.Run("fence lease Task", func(t *testing.T) {
		seed := downstreamEnactmentRef(
			t, "binding-scope-fence", taskorchestration.EnactmentTaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleFence(2),
		)
		request, _ := c04FenceContractFixture(
			t, seed, "binding-scope-task", "binding-scope-phase", "binding-scope-workspace", 2, 2,
		)
		request.SandboxLeaseAuthority.TaskID = taskworkspace.TaskID("other-binding-scope-task")
		request.SandboxLeaseAuthority.Digest = request.SandboxLeaseAuthority.CanonicalDigest()
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		if _, err := taskorchestration.NewTaskWorkspaceLifecycleFenceRequestBinding(request); err == nil {
			t.Fatal("self-consistent fence with cross-Task lease scope was admitted")
		}
	})

	t.Run("reconstruction Artifact input Task", func(t *testing.T) {
		seed := downstreamEnactmentRef(
			t, "binding-scope-reconstruction", taskorchestration.EnactmentTaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleFence(2),
		)
		request, _ := c04ReconstructionContractFixture(
			t, seed, "binding-scope-task", "binding-scope-workspace", 2, 2,
		)
		request.Intent.ArtifactVersionInput.TaskID = taskworkspace.TaskID("other-binding-scope-task")
		request.Intent.ArtifactVersionInput.Digest = request.Intent.ArtifactVersionInput.CanonicalDigest()
		request.Intent.Digest = request.Intent.CanonicalDigest()
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
		if _, err := taskorchestration.NewTaskWorkspaceReconstructionRequestBinding(request); err == nil {
			t.Fatal("self-consistent reconstruction with cross-Task Artifact input was admitted")
		}
	})
}

func TestTaskWorkspaceReconstructionAdapterUsesExactOpaqueC04EvidenceAndReplays(t *testing.T) {
	ref := downstreamEnactmentRef(
		t, "c04-reconstruction-operation", taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(2),
	)
	request, result := c04ReconstructionContractFixture(
		t, ref, "c04-reconstruction-task", "c04-reconstruction-workspace", 2, 2,
	)
	ref.PayloadDigest = taskorchestration.TaskWorkspaceReconstructionEnactmentPayloadDigest(request)
	port := &taskWorkspaceLifecyclePortDouble{reconstruction: result}
	adapter := taskorchestration.NewTaskWorkspaceReconstructionEvidenceAdapter(
		port,
		taskorchestration.TaskWorkspaceReconstructionAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "c04-reconstruction-authority"), Generation: 5,
			},
			TaskID: downstreamTaskID(t, "c04-reconstruction-task"), SafetyEpoch: 3,
			Request: &request,
		},
	)

	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatalf("enact C04 reconstruction: %v", err)
	}
	if evidence.TaskID.String() != string(request.Intent.TaskID) ||
		evidence.OperationID != ref.OperationID ||
		evidence.ArtifactVersionID.String() != string(request.Intent.ArtifactVersionInput.ArtifactVersionID) ||
		evidence.RevisionID.String() != string(result.CurrentRevisionID) ||
		evidence.CheckpointID.String() != string(result.CurrentCheckpointID) ||
		evidence.Generation != taskorchestration.TaskWorkspaceLifecycleGeneration(request.Intent.Generation) ||
		evidence.Fence != taskorchestration.TaskWorkspaceLifecycleFence(request.Intent.Fence) ||
		evidence.ObservedGeneration != taskorchestration.TaskWorkspaceLifecycleGeneration(result.Generation) ||
		evidence.ObservedFence != taskorchestration.TaskWorkspaceLifecycleFence(result.Fence) ||
		evidence.ReconstructionProofDigest == (taskorchestration.EvidenceDigest{}) ||
		evidence.Evidence.Kind != taskorchestration.EvidenceTaskWorkspaceLifecycle ||
		port.reconstructionCalls != 1 {
		t.Fatalf("opaque C04 reconstruction evidence = %#v calls=%d", evidence, port.reconstructionCalls)
	}
	replayed, err := adapter.Enact(context.Background(), ref)
	if err != nil || !reflect.DeepEqual(replayed, evidence) || port.reconstructionCalls != 1 {
		t.Fatalf("C04 reconstruction replay = %#v calls=%d err=%v", replayed, port.reconstructionCalls, err)
	}
}

func TestTaskWorkspaceReconstructionAdapterRecoversEvidenceAfterResponseLoss(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		inspectPending bool
		wantReconcile  int
	}{
		{name: "inspect terminal"},
		{name: "reconcile pending inspection", inspectPending: true, wantReconcile: 1},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			ref := downstreamEnactmentRef(
				t, "c04-reconstruction-response-loss-"+strings.ReplaceAll(scenario.name, " ", "-"),
				taskorchestration.EnactmentTaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleFence(2),
			)
			request, result := c04ReconstructionContractFixture(
				t, ref, "c04-reconstruction-response-loss-task", "c04-reconstruction-response-loss-workspace", 2, 2,
			)
			ref.PayloadDigest = taskorchestration.TaskWorkspaceReconstructionEnactmentPayloadDigest(request)
			terminal := taskworkspace.OperationInspection{
				Operation: request.Operation, Disposition: taskworkspace.OperationTerminal,
				ReconstructTaskWorkspace: &result,
			}
			port := &taskWorkspaceLifecyclePortDouble{
				reconstructionErr: &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired},
				inspection:        terminal,
			}
			if scenario.inspectPending {
				port.inspection = taskworkspace.OperationInspection{Disposition: taskworkspace.OperationPending}
				port.reconciled = terminal
			}
			adapter := taskorchestration.NewTaskWorkspaceReconstructionEvidenceAdapter(
				port, taskorchestration.TaskWorkspaceReconstructionAdapterBinding{
					Enactment: ref,
					Producer: taskorchestration.EvidenceProducer{
						AuthorityID: downstreamAuthorityID(t, "c04-reconstruction-response-loss-authority"),
						Generation:  5,
					},
					TaskID:      downstreamTaskID(t, "c04-reconstruction-response-loss-task"),
					SafetyEpoch: 3, Request: &request,
				},
			)
			evidence, err := adapter.Enact(context.Background(), ref)
			if err != nil || evidence.RevisionID.String() != string(result.CurrentRevisionID) ||
				port.reconstructionCalls != 1 || port.inspectCalls != 1 ||
				port.reconcileCalls != scenario.wantReconcile {
				t.Fatalf("reconstruction response-loss evidence=%#v calls=(%d,%d,%d) err=%v",
					evidence, port.reconstructionCalls, port.inspectCalls, port.reconcileCalls, err)
			}
		})
	}
}

func TestTaskWorkspaceReconstructionAdapterRejectsMismatchedOpaqueResult(t *testing.T) {
	for _, scenario := range []struct {
		name string
		edit func(*taskworkspace.ReconstructTaskWorkspaceResult)
	}{
		{name: "operation", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.Operation.ID = "other-reconstruction-operation"
		}},
		{name: "Artifact Version", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.ArtifactVersionID = "other-artifact-version"
		}},
		{name: "generation", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.Generation++
		}},
		{name: "fence", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.Fence++
		}},
		{name: "Task Workspace", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.TaskWorkspaceID = "other-task-workspace"
		}},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			ref := downstreamEnactmentRef(
				t, "c04-reconstruction-mismatch-"+strings.ReplaceAll(scenario.name, " ", "-"),
				taskorchestration.EnactmentTaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleFence(2),
			)
			request, result := c04ReconstructionContractFixture(
				t, ref, "c04-reconstruction-mismatch-task", "c04-reconstruction-mismatch-workspace", 2, 2,
			)
			ref.PayloadDigest = taskorchestration.TaskWorkspaceReconstructionEnactmentPayloadDigest(request)
			scenario.edit(&result)
			adapter := taskorchestration.NewTaskWorkspaceReconstructionEvidenceAdapter(
				&taskWorkspaceLifecyclePortDouble{reconstruction: result},
				taskorchestration.TaskWorkspaceReconstructionAdapterBinding{
					Enactment: ref,
					Producer: taskorchestration.EvidenceProducer{
						AuthorityID: downstreamAuthorityID(t, "c04-reconstruction-mismatch-authority"),
						Generation:  5,
					},
					TaskID:      downstreamTaskID(t, "c04-reconstruction-mismatch-task"),
					SafetyEpoch: 3, Request: &request,
				},
			)
			_, err := adapter.Enact(context.Background(), ref)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamCorruptEvidence)
		})
	}
}

func TestTaskWorkspaceReconstructionAdapterRejectsNonExactReadOnlyInputs(t *testing.T) {
	for _, scenario := range []struct {
		name string
		edit func(*taskworkspace.ReconstructTaskWorkspaceResult)
	}{
		{name: "missing", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.ReadOnlyInputs = nil
		}},
		{name: "different immutable input", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.ReadOnlyInputs[0].InputID = "other-runtime-release"
			result.ReadOnlyInputs[0].Digest = result.ReadOnlyInputs[0].CanonicalDigest()
		}},
		{name: "duplicate", edit: func(result *taskworkspace.ReconstructTaskWorkspaceResult) {
			result.ReadOnlyInputs = append(result.ReadOnlyInputs, result.ReadOnlyInputs[0])
		}},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			ref := downstreamEnactmentRef(
				t, "c04-reconstruction-input-"+strings.ReplaceAll(scenario.name, " ", "-"),
				taskorchestration.EnactmentTaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleFence(2),
			)
			request, result := c04ReconstructionContractFixture(
				t, ref, "c04-reconstruction-input-task", "c04-reconstruction-input-workspace", 2, 2,
			)
			capability := taskworkspace.ReadOnlyInputCapability{
				ID:             "runtime-release-capability",
				AuthorityID:    "release-authority",
				PolicyDomainID: request.Intent.PolicyDomainID,
				TaskID:         request.Intent.TaskID,
				Kind:           taskworkspace.ImmutableInputRuntimeRelease,
				InputID:        "runtime-release-v1",
				ManifestDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ExpiresAt:      100,
			}
			capability.Digest = capability.CanonicalDigest()
			request.Intent.ReadOnlyInputs = []taskworkspace.ReadOnlyInputCapability{capability}
			request.Intent.Digest = request.Intent.CanonicalDigest()
			request.Operation.RequestDigest = request.CanonicalRequestDigest()
			result.Operation = request.Operation
			materialization := taskworkspace.ReadOnlyInputMaterialization{
				ID:             "runtime-release-materialization",
				CapabilityID:   capability.ID,
				Kind:           capability.Kind,
				InputID:        capability.InputID,
				ManifestDigest: capability.ManifestDigest,
				EvidenceID:     "runtime-release-evidence",
				Access:         taskworkspace.InputAccessReadOnly,
				Generation:     result.Generation,
				Fence:          result.Fence,
			}
			materialization.Digest = materialization.CanonicalDigest()
			result.ReadOnlyInputs = []taskworkspace.ReadOnlyInputMaterialization{materialization}
			ref.PayloadDigest = taskorchestration.TaskWorkspaceReconstructionEnactmentPayloadDigest(request)
			scenario.edit(&result)

			adapter := taskorchestration.NewTaskWorkspaceReconstructionEvidenceAdapter(
				&taskWorkspaceLifecyclePortDouble{reconstruction: result},
				taskorchestration.TaskWorkspaceReconstructionAdapterBinding{
					Enactment: ref,
					Producer: taskorchestration.EvidenceProducer{
						AuthorityID: downstreamAuthorityID(t, "c04-reconstruction-input-authority"),
						Generation:  5,
					},
					TaskID:      downstreamTaskID(t, "c04-reconstruction-input-task"),
					SafetyEpoch: 3,
					Request:     &request,
				},
			)
			_, err := adapter.Enact(context.Background(), ref)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamCorruptEvidence)
		})
	}
}

func TestTaskWorkspaceLifecycleAdapterRecoversCommitEvidenceAfterResponseLoss(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		inspection taskworkspace.OperationInspection
		reconciled taskworkspace.OperationInspection
		wantCalls  int
	}{
		{name: "inspect terminal", wantCalls: 0},
		{
			name:       "reconcile pending inspection",
			inspection: taskworkspace.OperationInspection{Disposition: taskworkspace.OperationPending},
			wantCalls:  1,
		},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			ref := downstreamEnactmentRef(t, "c04-response-loss-"+strings.ReplaceAll(scenario.name, " ", "-"),
				taskorchestration.EnactmentTaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleFence(7))
			request, result := c04CommitContractFixture(
				t, ref, "c04-response-loss-task", "c04-response-loss-phase", "c04-response-loss-workspace", 5, 7,
			)
			bindC04EnactmentPayload(t, &ref, request.Operation.RequestDigest)
			terminal := taskworkspace.OperationInspection{
				Operation: request.Operation, Disposition: taskworkspace.OperationTerminal, CommitRuntimeView: &result,
			}
			if scenario.name == "inspect terminal" {
				scenario.inspection = terminal
			} else {
				scenario.reconciled = terminal
			}
			port := &taskWorkspaceLifecyclePortDouble{
				commitErr:  &taskworkspace.Error{Code: taskworkspace.ErrorReconciliationRequired},
				inspection: scenario.inspection, reconciled: scenario.reconciled,
			}
			adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
				port,
				taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
					Enactment: ref,
					Producer: taskorchestration.EvidenceProducer{
						AuthorityID: downstreamAuthorityID(t, "c04-response-loss-authority"), Generation: 5,
					},
					TaskID:             downstreamTaskID(t, "c04-response-loss-task"),
					PhaseRunID:         downstreamPhaseRunID(t, "c04-response-loss-phase"),
					PhaseRunGeneration: 6, PhaseRunFence: 7, SafetyEpoch: 2, Commit: &request,
				},
			)

			evidence, err := adapter.Enact(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.RevisionID.String() != string(result.RevisionID) ||
				evidence.CheckpointID.String() != string(result.CheckpointID) ||
				port.commitCalls != 1 || port.inspectCalls != 1 || port.reconcileCalls != scenario.wantCalls {
				t.Fatalf("response-loss reconciliation = %#v calls=(%d,%d,%d)",
					evidence, port.commitCalls, port.inspectCalls, port.reconcileCalls)
			}
		})
	}
}

func TestTaskWorkspaceLifecycleAdapterAcceptsUnchangedContentCommitLineage(t *testing.T) {
	ref := downstreamEnactmentRef(t, "c04-unchanged-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(7))
	request, result := c04CommitContractFixture(
		t, ref, "c04-unchanged-task", "c04-unchanged-phase", "c04-unchanged-workspace", 5, 7,
	)
	bindC04EnactmentPayload(t, &ref, request.Operation.RequestDigest)
	result.RevisionID = request.ExpectedCurrentRevision
	result.PredecessorRevisionID = taskworkspace.RevisionID("c04-unchanged-predecessor")
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		&taskWorkspaceLifecyclePortDouble{commit: result},
		taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "c04-unchanged-authority"), Generation: 5,
			},
			TaskID:             downstreamTaskID(t, "c04-unchanged-task"),
			PhaseRunID:         downstreamPhaseRunID(t, "c04-unchanged-phase"),
			PhaseRunGeneration: 6, PhaseRunFence: 7, SafetyEpoch: 2, Commit: &request,
		},
	)

	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil {
		t.Fatalf("valid unchanged-content C04 commit was rejected: %v", err)
	}
	if evidence.RevisionID.String() != string(request.ExpectedCurrentRevision) ||
		evidence.CheckpointID.String() != string(result.CheckpointID) ||
		evidence.CommitProofDigest == (taskorchestration.EvidenceDigest{}) {
		t.Fatalf("unchanged-content C04 evidence = %#v", evidence)
	}
}

func TestTaskWorkspaceLifecycleAdapterSafelyRejectsMalformedEnactment(t *testing.T) {
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		&taskWorkspaceLifecyclePortDouble{},
		taskorchestration.TaskWorkspaceLifecycleAdapterBinding{},
	)
	_, err := adapter.Enact(context.Background(), taskorchestration.EnactmentRef{})
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamInvalidEnactment)
}

func TestTaskWorkspaceLifecycleAdapterRejectsCrossWorkspaceCommitResult(t *testing.T) {
	ref := downstreamEnactmentRef(t, "c04-cross-workspace-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(7))
	request, result := c04CommitContractFixture(
		t, ref, "c04-cross-workspace-task", "c04-cross-workspace-phase", "c04-workspace-a", 5, 7,
	)
	bindC04EnactmentPayload(t, &ref, request.Operation.RequestDigest)
	result.TaskWorkspaceID = "c04-workspace-b"
	port := &taskWorkspaceLifecyclePortDouble{commit: result}
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		port,
		taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "c04-cross-workspace-authority"), Generation: 5,
			},
			TaskID:             downstreamTaskID(t, "c04-cross-workspace-task"),
			PhaseRunID:         downstreamPhaseRunID(t, "c04-cross-workspace-phase"),
			PhaseRunGeneration: 6, PhaseRunFence: 7, SafetyEpoch: 2, Commit: &request,
		},
	)

	_, err := adapter.Enact(context.Background(), ref)
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamCorruptEvidence)
}

func TestTaskWorkspaceLifecycleAdapterRejectsCrossPhaseCommitBinding(t *testing.T) {
	ref := downstreamEnactmentRef(t, "c04-cross-phase-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(7))
	request, result := c04CommitContractFixture(
		t, ref, "c04-cross-phase-task", "c04-other-phase", "c04-cross-phase-workspace", 5, 7,
	)
	bindC04EnactmentPayload(t, &ref, request.Operation.RequestDigest)
	port := &taskWorkspaceLifecyclePortDouble{commit: result}
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		port,
		taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "c04-cross-phase-authority"), Generation: 5,
			},
			TaskID:             downstreamTaskID(t, "c04-cross-phase-task"),
			PhaseRunID:         downstreamPhaseRunID(t, "c04-expected-phase"),
			PhaseRunGeneration: 6, PhaseRunFence: 7, SafetyEpoch: 2, Commit: &request,
		},
	)

	_, err := adapter.Enact(context.Background(), ref)
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamInvalidEnactment)
	if port.commitCalls != 0 {
		t.Fatal("cross-Phase C04 binding reached Lifecycle port")
	}
}

func TestTaskWorkspaceLifecycleAdapterCancellationRaceIsSafelyNormalized(t *testing.T) {
	for _, winner := range []string{"commit", "fence"} {
		winner := winner
		t.Run(winner+"-first", func(t *testing.T) {
			commitRef := downstreamEnactmentRef(t, "c04-race-commit-"+winner,
				taskorchestration.EnactmentTaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleFence(7))
			fenceRef := downstreamEnactmentRef(t, "c04-race-fence-"+winner,
				taskorchestration.EnactmentTaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleFence(8))
			commitRequest, commitResult := c04CommitContractFixture(
				t, commitRef, "c04-race-task", "c04-race-phase", "c04-race-workspace", 7, 7,
			)
			bindC04EnactmentPayload(t, &commitRef, commitRequest.Operation.RequestDigest)
			fenceRequest, fenceResult := c04FenceContractFixture(
				t, fenceRef, "c04-race-task", "c04-race-phase", "c04-race-workspace", 8, 8,
			)
			bindC04EnactmentPayload(t, &fenceRef, fenceRequest.Operation.RequestDigest)
			port := newC04RacePort(winner, commitResult, fenceResult)
			producer := taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "c04-race-authority"), Generation: 7,
			}
			commitAdapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
				port, taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
					Enactment: commitRef, Producer: producer,
					TaskID:             downstreamTaskID(t, "c04-race-task"),
					PhaseRunID:         downstreamPhaseRunID(t, "c04-race-phase"),
					PhaseRunGeneration: 6, PhaseRunFence: 7, SafetyEpoch: 2, Commit: &commitRequest,
				},
			)
			fenceAdapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
				port, taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
					Enactment: fenceRef, Producer: producer,
					TaskID:             downstreamTaskID(t, "c04-race-task"),
					PhaseRunID:         downstreamPhaseRunID(t, "c04-race-phase"),
					PhaseRunGeneration: 6, PhaseRunFence: 7, SafetyEpoch: 2, Fence: &fenceRequest,
				},
			)

			type raceResult struct {
				kind     string
				evidence taskorchestration.TaskWorkspaceLifecycleAdapterEvidence
				err      error
			}
			results := make(chan raceResult, 2)
			go func() {
				evidence, err := commitAdapter.Enact(context.Background(), commitRef)
				results <- raceResult{"commit", evidence, err}
			}()
			go func() {
				evidence, err := fenceAdapter.Enact(context.Background(), fenceRef)
				results <- raceResult{"fence", evidence, err}
			}()

			for range 2 {
				result := <-results
				if result.kind == winner {
					if result.err != nil {
						t.Fatalf("%s winner failed: %v", winner, result.err)
					}
					want := taskorchestration.LifecycleEvidenceCommitted
					if winner == "fence" {
						want = taskorchestration.LifecycleEvidenceFenced
					}
					if result.evidence.Outcome != want {
						t.Fatalf("%s winner evidence = %#v", winner, result.evidence)
					}
					winnerRef := commitRef
					if winner == "fence" {
						winnerRef = fenceRef
					}
					if result.evidence.Fence != winnerRef.Fence ||
						result.evidence.ObservedFence <= result.evidence.Fence ||
						(winner == "commit" && result.evidence.CommitProofDigest == (taskorchestration.EvidenceDigest{})) ||
						(winner == "fence" && result.evidence.FenceProofDigest == (taskorchestration.EvidenceDigest{})) {
						t.Fatalf("%s evidence lost enactment/result fence separation: %#v", winner, result.evidence)
					}
					continue
				}
				assertDownstreamErrorCode(t, result.err, taskorchestration.DownstreamStale)
			}
		})
	}
}

type c04RacePort struct {
	winner       string
	commitResult taskworkspace.CommitRuntimeViewResult
	fenceResult  taskworkspace.FenceRuntimeViewResult
	commitWon    chan struct{}
	fenceWon     chan struct{}
}

func newC04RacePort(
	winner string,
	commitResult taskworkspace.CommitRuntimeViewResult,
	fenceResult taskworkspace.FenceRuntimeViewResult,
) *c04RacePort {
	return &c04RacePort{
		winner: winner, commitResult: commitResult, fenceResult: fenceResult,
		commitWon: make(chan struct{}), fenceWon: make(chan struct{}),
	}
}

func (port *c04RacePort) CommitRuntimeView(
	_ context.Context,
	_ taskworkspace.CommitRuntimeViewRequest,
) (taskworkspace.CommitRuntimeViewResult, error) {
	if port.winner == "fence" {
		<-port.fenceWon
		return taskworkspace.CommitRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorViewTerminalConflict}
	}
	close(port.commitWon)
	return port.commitResult, nil
}

func (port *c04RacePort) FenceRuntimeView(
	_ context.Context,
	_ taskworkspace.FenceRuntimeViewRequest,
) (taskworkspace.FenceRuntimeViewResult, error) {
	if port.winner == "commit" {
		<-port.commitWon
		return taskworkspace.FenceRuntimeViewResult{}, &taskworkspace.Error{Code: taskworkspace.ErrorViewTerminalConflict}
	}
	close(port.fenceWon)
	return port.fenceResult, nil
}

func (port *c04RacePort) InspectOperation(
	_ context.Context,
	_ taskworkspace.InspectOperationRequest,
) (taskworkspace.OperationInspection, error) {
	return taskworkspace.OperationInspection{}, &taskworkspace.Error{Code: taskworkspace.ErrorViewTerminalConflict}
}

func (port *c04RacePort) ReconcileOperation(
	_ context.Context,
	_ taskworkspace.ReconcileOperationRequest,
) (taskworkspace.OperationInspection, error) {
	return taskworkspace.OperationInspection{}, &taskworkspace.Error{Code: taskworkspace.ErrorViewTerminalConflict}
}

func (port *c04RacePort) ReconstructTaskWorkspace(
	_ context.Context,
	_ taskworkspace.ReconstructTaskWorkspaceRequest,
) (taskworkspace.ReconstructTaskWorkspaceResult, error) {
	return taskworkspace.ReconstructTaskWorkspaceResult{}, &taskworkspace.Error{
		Code: taskworkspace.ErrorViewTerminalConflict,
	}
}

type taskWorkspaceLifecyclePortDouble struct {
	commit              taskworkspace.CommitRuntimeViewResult
	fence               taskworkspace.FenceRuntimeViewResult
	reconstruction      taskworkspace.ReconstructTaskWorkspaceResult
	inspection          taskworkspace.OperationInspection
	reconciled          taskworkspace.OperationInspection
	commitErr           error
	fenceErr            error
	reconstructionErr   error
	inspectErr          error
	reconcileErr        error
	commitCalls         int
	fenceCalls          int
	reconstructionCalls int
	inspectCalls        int
	reconcileCalls      int
}

func c04ReconstructionContractFixture(
	t *testing.T,
	ref taskorchestration.EnactmentRef,
	taskID string,
	workspaceID string,
	generation taskworkspace.Generation,
	fence taskworkspace.Fence,
) (taskworkspace.ReconstructTaskWorkspaceRequest, taskworkspace.ReconstructTaskWorkspaceResult) {
	t.Helper()
	policyDomainID := taskworkspace.PolicyDomainID("policy-" + taskID)
	publicationAuthorityID := taskworkspace.PublicationAuthorityID("publication-" + taskID)
	artifactInput := taskworkspace.ArtifactVersionReconstructionInput{
		ID:                     taskworkspace.ArtifactVersionInputCapabilityID("artifact-input-" + ref.OperationID.String()),
		PublicationAuthorityID: publicationAuthorityID, PolicyDomainID: policyDomainID,
		TaskID:            taskworkspace.TaskID(taskID),
		ArtifactVersionID: taskworkspace.ArtifactVersionID("artifact-" + ref.OperationID.String()),
		ManifestDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpiresAt:         100,
	}
	artifactInput.Digest = artifactInput.CanonicalDigest()
	intent := taskworkspace.AuthorizedRecoveryIntent{
		ID:                  taskworkspace.RecoveryIntentID("recovery-" + ref.OperationID.String()),
		RecoveryAuthorityID: taskworkspace.RecoveryAuthorityID("recovery-authority-" + taskID),
		PolicyDomainID:      policyDomainID, TaskID: taskworkspace.TaskID(taskID),
		TaskWorkspaceID:             taskworkspace.TaskWorkspaceID(workspaceID),
		TargetKind:                  taskworkspace.RecoveryTargetArtifactVersion,
		ExpectedCurrentRevisionID:   taskworkspace.RevisionID("revision-" + taskID),
		ExpectedCurrentCheckpointID: taskworkspace.CheckpointID("checkpoint-" + taskID),
		ArtifactVersionInput:        artifactInput, PublicationAuthorityID: publicationAuthorityID,
		Generation: generation, Fence: fence, Mode: taskworkspace.RecoveryModeWritable, ExpiresAt: 100,
	}
	intent.Digest = intent.CanonicalDigest()
	request := taskworkspace.ReconstructTaskWorkspaceRequest{
		Intent: intent, Operation: taskworkspace.Operation{ID: taskworkspace.OperationID(ref.OperationID.String())},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	artifactEvidence := taskworkspace.ArtifactVersionReconstructionEvidence{
		ID:                     taskworkspace.EvidenceID("artifact-evidence-" + ref.OperationID.String()),
		PublicationAuthorityID: publicationAuthorityID, PolicyDomainID: policyDomainID,
		TaskID: intent.TaskID, ArtifactVersionID: artifactInput.ArtifactVersionID,
		ManifestDigest: artifactInput.ManifestDigest, InputCapabilityID: artifactInput.ID,
		ContentEvidenceRoot: taskworkspace.EvidenceRoot("content-root-" + ref.OperationID.String()),
		Decision:            taskworkspace.ReconstructionInputVerified, RecoveryIntentID: intent.ID,
		Generation: generation + 1, Fence: fence + 1, OperationID: request.Operation.ID,
	}
	artifactEvidence.Digest = artifactEvidence.CanonicalDigest()
	result := taskworkspace.ReconstructTaskWorkspaceResult{
		TaskWorkspaceID:                intent.TaskWorkspaceID,
		MaterializationID:              taskworkspace.MaterializationID("materialization-" + ref.OperationID.String()),
		CurrentRevisionID:              intent.ExpectedCurrentRevisionID,
		CurrentCheckpointID:            intent.ExpectedCurrentCheckpointID,
		ArtifactVersionID:              artifactInput.ArtifactVersionID,
		ArtifactManifestDigest:         artifactInput.ManifestDigest,
		ArtifactReconstructionEvidence: artifactEvidence,
		PublicationAuthorityID:         publicationAuthorityID,
		Generation:                     generation + 1, PreviousFence: fence, Fence: fence + 1,
		RecoveryIntentID: intent.ID, Operation: request.Operation,
	}
	return request, result
}

func c04CommitContractFixture(
	t *testing.T,
	ref taskorchestration.EnactmentRef,
	taskID string,
	phaseRunID string,
	workspaceID string,
	generation taskworkspace.Generation,
	fence taskworkspace.Fence,
) (taskworkspace.CommitRuntimeViewRequest, taskworkspace.CommitRuntimeViewResult) {
	t.Helper()
	policyDomainID := taskworkspace.PolicyDomainID("policy-" + taskID)
	runtimeOperationID := taskworkspace.OperationID("runtime-" + ref.OperationID.String())
	runtimeViewID := taskworkspace.RuntimeViewID("view-" + ref.OperationID.String())
	baseRevisionID := taskworkspace.RevisionID("base-" + ref.OperationID.String())
	lease := taskworkspace.SandboxLeaseAuthority{
		ID:             taskworkspace.SandboxLeaseID("lease-" + ref.OperationID.String()),
		EvidenceID:     taskworkspace.EvidenceID("lease-evidence-" + ref.OperationID.String()),
		AuthorityID:    taskworkspace.SandboxLeaseAuthorityID("lease-authority-" + ref.OperationID.String()),
		PolicyDomainID: policyDomainID, TaskID: taskworkspace.TaskID(taskID),
		PhaseRunID:         taskworkspace.PhaseRunID(phaseRunID),
		RuntimeRunID:       taskworkspace.RuntimeRunID("runtime-run-" + ref.OperationID.String()),
		RuntimeOperationID: runtimeOperationID, EffectClass: taskworkspace.RuntimeViewMutating,
		LeaseGeneration: 1, LeaseFence: 1, ExpiresAt: 100,
	}
	lease.Digest = lease.CanonicalDigest()
	manifest := taskworkspace.DeclaredStateManifest{}
	manifest.Digest = manifest.CanonicalDigest()
	validation := taskworkspace.ValidationEvidence{
		ID:                    taskworkspace.EvidenceID("validation-" + ref.OperationID.String()),
		ValidationAuthorityID: taskworkspace.ValidationAuthorityID("validator-" + ref.OperationID.String()),
		PolicyDomainID:        policyDomainID, TaskID: taskworkspace.TaskID(taskID),
		TaskWorkspaceID: taskworkspace.TaskWorkspaceID(workspaceID), RuntimeViewID: runtimeViewID,
		BaseRevisionID: baseRevisionID, PhaseRunID: taskworkspace.PhaseRunID(phaseRunID),
		RuntimeRunID: lease.RuntimeRunID, RuntimeOperationID: runtimeOperationID,
		SandboxLeaseAuthorityDigest: lease.Digest, ManifestDigest: manifest.Digest,
		Generation: generation, Fence: fence, Decision: taskworkspace.ValidationAccepted,
	}
	validation.Digest = validation.CanonicalDigest()
	request := taskworkspace.CommitRuntimeViewRequest{
		PolicyDomainID: policyDomainID, TaskID: taskworkspace.TaskID(taskID),
		TaskWorkspaceID: taskworkspace.TaskWorkspaceID(workspaceID), RuntimeViewID: runtimeViewID,
		RuntimeOperationID: runtimeOperationID, SandboxLeaseAuthority: lease,
		BaseRevisionID: baseRevisionID, ExpectedCurrentRevision: baseRevisionID,
		Generation: generation, Fence: fence, ValidationEvidence: validation,
		DeclaredStateManifest: manifest,
		Operation:             taskworkspace.Operation{ID: taskworkspace.OperationID(ref.OperationID.String())},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	result := taskworkspace.CommitRuntimeViewResult{
		TaskWorkspaceID: request.TaskWorkspaceID,
		RevisionID:      taskworkspace.RevisionID("revision-" + ref.OperationID.String()),
		CheckpointID:    taskworkspace.CheckpointID("checkpoint-" + ref.OperationID.String()),
		BaseRevisionID:  baseRevisionID, PredecessorRevisionID: baseRevisionID,
		ManifestDigest: manifest.Digest, ValidationEvidenceID: validation.ID,
		ValidationEvidenceDigest: validation.Digest,
		ContentEvidenceRoot:      taskworkspace.EvidenceRoot("content-root-" + ref.OperationID.String()),
		DurabilityEvidenceRoot:   taskworkspace.EvidenceRoot("durability-root-" + ref.OperationID.String()),
		Generation:               generation, PreviousFence: fence, Fence: fence + 1, Operation: request.Operation,
	}
	return request, result
}

func c04FenceContractFixture(
	t *testing.T,
	ref taskorchestration.EnactmentRef,
	taskID string,
	phaseRunID string,
	workspaceID string,
	generation taskworkspace.Generation,
	fence taskworkspace.Fence,
) (taskworkspace.FenceRuntimeViewRequest, taskworkspace.FenceRuntimeViewResult) {
	t.Helper()
	commit, _ := c04CommitContractFixture(t, ref, taskID, phaseRunID, workspaceID, generation, fence)
	request := taskworkspace.FenceRuntimeViewRequest{
		PolicyDomainID: commit.PolicyDomainID, TaskID: commit.TaskID,
		TaskWorkspaceID: commit.TaskWorkspaceID, RuntimeViewID: commit.RuntimeViewID,
		RuntimeOperationID: commit.RuntimeOperationID, SandboxLeaseAuthority: commit.SandboxLeaseAuthority,
		BaseRevisionID: commit.BaseRevisionID, ExpectedCurrentRevision: commit.ExpectedCurrentRevision,
		Generation: generation, Fence: fence, Reason: taskworkspace.RuntimeViewCancelled,
		Operation: taskworkspace.Operation{ID: taskworkspace.OperationID(ref.OperationID.String())},
	}
	request.Operation.RequestDigest = request.CanonicalRequestDigest()
	return request, taskworkspace.FenceRuntimeViewResult{
		TaskWorkspaceID: request.TaskWorkspaceID, RuntimeViewID: request.RuntimeViewID,
		BaseRevisionID: request.BaseRevisionID, CurrentRevisionID: request.ExpectedCurrentRevision,
		Reason: request.Reason, Generation: generation, PreviousFence: fence, Fence: fence + 1,
		Operation: request.Operation,
	}
}

func exactC04CommitRequestBinding(
	t *testing.T,
	taskID string,
	phaseRunID taskorchestration.PhaseRunID,
	workspaceID taskorchestration.TaskWorkspaceID,
	operation string,
	generation taskorchestration.TaskWorkspaceLifecycleGeneration,
	fence taskorchestration.TaskWorkspaceLifecycleFence,
	expectedRevision taskorchestration.TaskWorkspaceRevisionID,
) taskorchestration.TaskWorkspaceLifecycleCommitRequestBinding {
	t.Helper()
	seed := downstreamEnactmentRef(
		t, operation, taskorchestration.EnactmentTaskWorkspaceLifecycle, fence,
	)
	request, _ := c04CommitContractFixture(
		t, seed, taskID, phaseRunID.String(), workspaceID.String(),
		taskworkspace.Generation(generation), taskworkspace.Fence(fence),
	)
	if expectedRevision != (taskorchestration.TaskWorkspaceRevisionID{}) {
		currentRevision := taskworkspace.RevisionID(expectedRevision.String())
		request.BaseRevisionID = currentRevision
		request.ExpectedCurrentRevision = currentRevision
		request.ValidationEvidence.BaseRevisionID = currentRevision
		request.ValidationEvidence.Digest = request.ValidationEvidence.CanonicalDigest()
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
	}
	binding, err := taskorchestration.NewTaskWorkspaceLifecycleCommitRequestBinding(request)
	if err != nil {
		t.Fatalf("bind exact C04 commit request %q: %v", operation, err)
	}
	return binding
}

func exactC04FenceRequestBinding(
	t *testing.T,
	taskID string,
	phaseRunID taskorchestration.PhaseRunID,
	workspaceID taskorchestration.TaskWorkspaceID,
	operation string,
	generation taskorchestration.TaskWorkspaceLifecycleGeneration,
	fence taskorchestration.TaskWorkspaceLifecycleFence,
	expectedRevision taskorchestration.TaskWorkspaceRevisionID,
) taskorchestration.TaskWorkspaceLifecycleFenceRequestBinding {
	t.Helper()
	seed := downstreamEnactmentRef(
		t, operation, taskorchestration.EnactmentTaskWorkspaceLifecycle, fence,
	)
	request, _ := c04FenceContractFixture(
		t, seed, taskID, phaseRunID.String(), workspaceID.String(),
		taskworkspace.Generation(generation), taskworkspace.Fence(fence),
	)
	if expectedRevision != (taskorchestration.TaskWorkspaceRevisionID{}) {
		currentRevision := taskworkspace.RevisionID(expectedRevision.String())
		request.BaseRevisionID = currentRevision
		request.ExpectedCurrentRevision = currentRevision
		request.Operation.RequestDigest = request.CanonicalRequestDigest()
	}
	binding, err := taskorchestration.NewTaskWorkspaceLifecycleFenceRequestBinding(request)
	if err != nil {
		t.Fatalf("bind exact C04 fence request %q: %v", operation, err)
	}
	return binding
}

func bindC04EnactmentPayload(
	t *testing.T,
	ref *taskorchestration.EnactmentRef,
	digest taskworkspace.Digest,
) {
	t.Helper()
	value := strings.TrimPrefix(string(digest), "sha256:")
	parsed, err := taskorchestration.ParseEnactmentPayloadDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	ref.PayloadDigest = parsed
}

func (port *taskWorkspaceLifecyclePortDouble) CommitRuntimeView(
	_ context.Context,
	_ taskworkspace.CommitRuntimeViewRequest,
) (taskworkspace.CommitRuntimeViewResult, error) {
	port.commitCalls++
	return port.commit, port.commitErr
}

func (port *taskWorkspaceLifecyclePortDouble) FenceRuntimeView(
	_ context.Context,
	_ taskworkspace.FenceRuntimeViewRequest,
) (taskworkspace.FenceRuntimeViewResult, error) {
	port.fenceCalls++
	return port.fence, port.fenceErr
}

func (port *taskWorkspaceLifecyclePortDouble) ReconstructTaskWorkspace(
	_ context.Context,
	_ taskworkspace.ReconstructTaskWorkspaceRequest,
) (taskworkspace.ReconstructTaskWorkspaceResult, error) {
	port.reconstructionCalls++
	return port.reconstruction, port.reconstructionErr
}

func (port *taskWorkspaceLifecyclePortDouble) InspectOperation(
	_ context.Context,
	_ taskworkspace.InspectOperationRequest,
) (taskworkspace.OperationInspection, error) {
	port.inspectCalls++
	return port.inspection, port.inspectErr
}

func (port *taskWorkspaceLifecyclePortDouble) ReconcileOperation(
	_ context.Context,
	_ taskworkspace.ReconcileOperationRequest,
) (taskworkspace.OperationInspection, error) {
	port.reconcileCalls++
	return port.reconciled, port.reconcileErr
}

func TestReleaseAndCatalogAdaptersNeverRepinExistingTaskLocks(t *testing.T) {
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "pinned-lock-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	releaseRecord := taskorchestration.PublishedExecutionContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "release-authority"), Generation: 4,
		},
		ExecutionLock: pinned.ExecutionLock,
		SafetyEpoch:   3,
	}
	releaseRecord.ContractDigest = taskorchestration.ExecutionLockContractDigest(releaseRecord.ExecutionLock)
	releasePort := &releaseManagementPortDouble{record: releaseRecord}
	releaseAdapter := taskorchestration.NewReleaseManagementAdapter(releasePort)

	request := taskorchestration.PinnedExecutionLockRequest{
		TaskID: downstreamTaskID(t, "pinned-lock-task"), ExecutionLock: pinned.ExecutionLock,
		ContractDigest: releaseRecord.ContractDigest, SafetyEpoch: 3,
	}
	var firstRelease taskorchestration.ResolvedExecutionContract
	for index, purpose := range []taskorchestration.PinnedLockPurpose{
		taskorchestration.PinnedLockRetry,
		taskorchestration.PinnedLockRecovery,
		taskorchestration.PinnedLockManualEdit,
	} {
		request.Purpose = purpose
		resolved, err := releaseAdapter.Resolve(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstRelease = resolved
		} else if !reflect.DeepEqual(resolved, firstRelease) {
			t.Fatalf("Release adapter repinned purpose %d: %#v != %#v", purpose, resolved, firstRelease)
		}
	}
	if releasePort.calls != 1 {
		t.Fatalf("Release exact lock resolved %d times, want one pinned result", releasePort.calls)
	}

	templateVersion := downstreamTemplateVersionID(t, "template-version-pinned")
	bundle := taskorchestration.ResourceBundleContract{
		ResourceBundleID: downstreamResourceBundleID(t, "bundle-pinned"),
		ManifestDigest:   downstreamEvidenceRef(t, "bundle-manifest", taskorchestration.EvidencePublication).Digest,
		PackageDigest:    downstreamEvidenceRef(t, "bundle-package", taskorchestration.EvidencePublication).Digest,
	}
	catalogRecord := taskorchestration.PublishedTemplateLockContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "catalog-authority"), Generation: 8,
		},
		TemplateLockID:    pinned.TemplateLockID,
		TemplateVersionID: templateVersion,
		TemplateManifestDigest: downstreamEvidenceRef(
			t, "template-manifest-pinned", taskorchestration.EvidencePublication,
		).Digest,
		TemplatePackageDigest: downstreamEvidenceRef(
			t, "template-package-pinned", taskorchestration.EvidencePublication,
		).Digest,
		CompatibilityEvidenceID: downstreamEvidenceID(t, "template-compatibility-pinned"),
		CompatibilityEvidenceDigest: downstreamEvidenceRef(
			t, "template-compatibility-digest-pinned", taskorchestration.EvidencePublication,
		).Digest,
		CompatibilityExecutionLockID: pinned.ExecutionLock.ID,
		ObservedGeneration:           12,
		SafetyEpoch:                  3,
		BundleClosure:                []taskorchestration.ResourceBundleContract{bundle},
	}
	catalogRecord.LockDigest = taskorchestration.TemplateLockContractDigest(catalogRecord)
	catalogPort := &catalogPublicationPortDouble{record: catalogRecord}
	catalogAdapter := taskorchestration.NewCatalogPublicationAdapter(catalogPort)
	catalogRequest := taskorchestration.PinnedTemplateLockRequest{
		TaskID: downstreamTaskID(t, "pinned-lock-task"), TemplateLockID: pinned.TemplateLockID,
		LockDigest: catalogRecord.LockDigest, ObservedGeneration: 12, SafetyEpoch: 3,
	}
	var firstCatalog taskorchestration.ResolvedTemplateLockContract
	for index, purpose := range []taskorchestration.PinnedLockPurpose{
		taskorchestration.PinnedLockRetry,
		taskorchestration.PinnedLockRecovery,
		taskorchestration.PinnedLockManualEdit,
	} {
		catalogRequest.Purpose = purpose
		resolved, err := catalogAdapter.Resolve(context.Background(), catalogRequest)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			firstCatalog = resolved
		} else if !reflect.DeepEqual(resolved, firstCatalog) {
			t.Fatalf("Catalog adapter repinned purpose %d: %#v != %#v", purpose, resolved, firstCatalog)
		}
		if resolved.TemplateManifestDigest != catalogRecord.TemplateManifestDigest ||
			resolved.TemplatePackageDigest != catalogRecord.TemplatePackageDigest ||
			resolved.CompatibilityEvidenceID != catalogRecord.CompatibilityEvidenceID ||
			resolved.CompatibilityEvidenceDigest != catalogRecord.CompatibilityEvidenceDigest ||
			resolved.CompatibilityExecutionLockID != catalogRecord.CompatibilityExecutionLockID {
			t.Fatalf("Catalog adapter dropped pinned Template contract material: %#v", resolved)
		}
	}
	if catalogPort.calls != 1 {
		t.Fatalf("Catalog exact lock resolved %d times, want one pinned result", catalogPort.calls)
	}
}

func TestTaskStartAdmissionRequiresExactTaskBoundReleaseAndCatalogVerification(t *testing.T) {
	now := time.Date(2026, time.July, 27, 17, 0, 0, 0, time.UTC)
	task := downstreamTaskID(t, "verified-admission-task")
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "verified-admission-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract: taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
	}})
	safetyEpoch := taskorchestration.SafetyEpoch(1)
	executionRecord := taskorchestration.PublishedExecutionContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "verified-admission-release-authority"), Generation: 2,
		},
		ExecutionLock: pinned.ExecutionLock, SafetyEpoch: safetyEpoch,
	}
	executionRecord.ContractDigest = taskorchestration.ExecutionLockContractDigest(executionRecord.ExecutionLock)
	execution, err := taskorchestration.NewReleaseManagementAdapter(
		&releaseManagementPortDouble{record: executionRecord},
	).Resolve(context.Background(), taskorchestration.PinnedExecutionLockRequest{
		TaskID: task, ExecutionLock: pinned.ExecutionLock, ContractDigest: executionRecord.ContractDigest,
		SafetyEpoch: safetyEpoch, Purpose: taskorchestration.PinnedLockStart,
	})
	if err != nil {
		t.Fatalf("verify exact Execution Lock for Start: %v", err)
	}
	templateRecord := validPublishedTemplateLockContract(
		t, pinned.TemplateLockID, pinned.ExecutionLock.ID, "verified-admission", 3, safetyEpoch,
	)
	template, err := taskorchestration.NewCatalogPublicationAdapter(
		&catalogPublicationPortDouble{record: templateRecord},
	).Resolve(context.Background(), taskorchestration.PinnedTemplateLockRequest{
		TaskID: task, TemplateLockID: pinned.TemplateLockID, LockDigest: templateRecord.LockDigest,
		ObservedGeneration: templateRecord.ObservedGeneration, SafetyEpoch: safetyEpoch,
		Purpose: taskorchestration.PinnedLockStart,
	})
	if err != nil {
		t.Fatalf("verify exact Template Lock for Start: %v", err)
	}
	admission, err := taskorchestration.NewTaskStartAdmission(
		task, pinned.Route, pinned.TaskWorkspaceID, pinned.Authorities, execution, &template,
	)
	if err != nil {
		t.Fatalf("bind verified Task Start admission: %v", err)
	}
	constructor := reflect.TypeOf(taskorchestration.NewStartPinnedTaskIntent)
	if constructor.In(2) != reflect.TypeOf(taskorchestration.TaskStartAdmission{}) {
		t.Fatalf("Start constructor still accepts caller-built pins: %v", constructor.In(2))
	}
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "verified-admission-owner"), 1)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	header := intentHeader(t, "verified-admission-start", task.String(), now)
	started, err := harness.Mutations.Decide(
		context.Background(), verifiedPinnedStartIntent(t, header, owner, admission),
	)
	if err != nil || started.TaskProjection.ExecutionLockID != pinned.ExecutionLock.ID ||
		started.TaskProjection.TemplateLockID != pinned.TemplateLockID {
		t.Fatalf("verified admission did not atomically pin exact locks: decision=%+v err=%v", started, err)
	}
	zeroHeader := intentHeader(t, "zero-admission-start", "zero-admission-task", now.Add(time.Second))
	_, err = harness.Mutations.Decide(
		context.Background(),
		verifiedPinnedStartIntent(t, zeroHeader, owner, taskorchestration.TaskStartAdmission{}),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorInvalidIntent {
		t.Fatalf("zero/unverified admission = %T %v, want invalid intent", err, err)
	}
	otherTask := downstreamTaskID(t, "verified-admission-other-task")
	if _, err := taskorchestration.NewTaskStartAdmission(
		otherTask, pinned.Route, pinned.TaskWorkspaceID, pinned.Authorities, execution, &template,
	); err == nil {
		t.Fatal("Task-bound Release/Catalog verification was reusable for another Task")
	}
}

func TestReleaseManagementAdapterRejectsStaleSafetyEpoch(t *testing.T) {
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "release-stale-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	record := taskorchestration.PublishedExecutionContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "release-stale-authority"), Generation: 4,
		},
		ExecutionLock: pinned.ExecutionLock,
		SafetyEpoch:   2,
	}
	record.ContractDigest = taskorchestration.ExecutionLockContractDigest(record.ExecutionLock)
	adapter := taskorchestration.NewReleaseManagementAdapter(&releaseManagementPortDouble{record: record})

	_, err := adapter.Resolve(context.Background(), taskorchestration.PinnedExecutionLockRequest{
		TaskID: downstreamTaskID(t, "release-stale-task"), ExecutionLock: pinned.ExecutionLock,
		ContractDigest: record.ContractDigest, SafetyEpoch: 3, Purpose: taskorchestration.PinnedLockRecovery,
	})
	assertDownstreamErrorCode(t, err, taskorchestration.DownstreamStale)
}

func TestCatalogPublicationAdapterRejectsStaleGenerationAndSafetyEpoch(t *testing.T) {
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "catalog-stale-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	record := taskorchestration.PublishedTemplateLockContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "catalog-stale-authority"), Generation: 8,
		},
		TemplateLockID: pinned.TemplateLockID, TemplateVersionID: downstreamTemplateVersionID(t, "catalog-stale-version"),
		ObservedGeneration: 12, SafetyEpoch: 3,
		BundleClosure: []taskorchestration.ResourceBundleContract{{
			ResourceBundleID: downstreamResourceBundleID(t, "catalog-stale-bundle"),
			ManifestDigest:   downstreamEvidenceRef(t, "catalog-stale-manifest", taskorchestration.EvidencePublication).Digest,
			PackageDigest:    downstreamEvidenceRef(t, "catalog-stale-package", taskorchestration.EvidencePublication).Digest,
		}},
	}
	bindTemplatePublishedContract(t, &record, pinned.ExecutionLock.ID, "catalog-stale")
	record.LockDigest = taskorchestration.TemplateLockContractDigest(record)

	for _, scenario := range []struct {
		name       string
		generation taskorchestration.ProducerGeneration
		epoch      taskorchestration.SafetyEpoch
	}{
		{name: "observed generation", generation: 13, epoch: 3},
		{name: "safety epoch", generation: 12, epoch: 4},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			adapter := taskorchestration.NewCatalogPublicationAdapter(&catalogPublicationPortDouble{record: record})
			_, err := adapter.Resolve(context.Background(), taskorchestration.PinnedTemplateLockRequest{
				TaskID: downstreamTaskID(t, "catalog-stale-task"), TemplateLockID: pinned.TemplateLockID,
				LockDigest: record.LockDigest, ObservedGeneration: scenario.generation,
				SafetyEpoch: scenario.epoch, Purpose: taskorchestration.PinnedLockRecovery,
			})
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamStale)
		})
	}
}

func TestCatalogPublicationAdapterCanonicalizesBundleClosureOrder(t *testing.T) {
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "catalog-closure-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	bundles := []taskorchestration.ResourceBundleContract{
		{
			ResourceBundleID: downstreamResourceBundleID(t, "catalog-closure-bundle-a"),
			ManifestDigest:   downstreamEvidenceRef(t, "catalog-closure-manifest-a", taskorchestration.EvidencePublication).Digest,
			PackageDigest:    downstreamEvidenceRef(t, "catalog-closure-package-a", taskorchestration.EvidencePublication).Digest,
		},
		{
			ResourceBundleID: downstreamResourceBundleID(t, "catalog-closure-bundle-b"),
			ManifestDigest:   downstreamEvidenceRef(t, "catalog-closure-manifest-b", taskorchestration.EvidencePublication).Digest,
			PackageDigest:    downstreamEvidenceRef(t, "catalog-closure-package-b", taskorchestration.EvidencePublication).Digest,
		},
	}
	record := taskorchestration.PublishedTemplateLockContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "catalog-closure-authority"), Generation: 8,
		},
		TemplateLockID: pinned.TemplateLockID, TemplateVersionID: downstreamTemplateVersionID(t, "catalog-closure-version"),
		ObservedGeneration: 12, SafetyEpoch: 3, BundleClosure: bundles,
	}
	bindTemplatePublishedContract(t, &record, pinned.ExecutionLock.ID, "catalog-closure")
	record.LockDigest = taskorchestration.TemplateLockContractDigest(record)
	record.BundleClosure = []taskorchestration.ResourceBundleContract{bundles[1], bundles[0]}
	adapter := taskorchestration.NewCatalogPublicationAdapter(&catalogPublicationPortDouble{record: record})

	resolved, err := adapter.Resolve(context.Background(), taskorchestration.PinnedTemplateLockRequest{
		TaskID: downstreamTaskID(t, "catalog-closure-task"), TemplateLockID: pinned.TemplateLockID,
		LockDigest: record.LockDigest, ObservedGeneration: 12, SafetyEpoch: 3,
		Purpose: taskorchestration.PinnedLockRetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.BundleClosure) != 2 || resolved.LockDigest != record.LockDigest {
		t.Fatalf("resolved exact bundle closure = %#v", resolved)
	}
}

func TestDownstreamAdapterPublicSurfacesDoNotExposeProgressionOrPhysicalMechanics(t *testing.T) {
	assertDownstreamAdapterPublicSurfaces(t)
}

func assertDownstreamAdapterPublicSurfaces(t *testing.T) {
	t.Helper()
	interfaces := []reflect.Type{
		reflect.TypeOf((*taskorchestration.RuntimeEvidenceAdapter)(nil)).Elem(),
		reflect.TypeOf((*taskorchestration.SchedulerEvidenceAdapter)(nil)).Elem(),
		reflect.TypeOf((*taskorchestration.PublicationEvidenceAdapter)(nil)).Elem(),
		reflect.TypeOf((*taskorchestration.UsageAccountingEvidenceAdapter)(nil)).Elem(),
		reflect.TypeOf((*taskorchestration.TaskWorkspaceLifecycleEvidenceAdapter)(nil)).Elem(),
		reflect.TypeOf((*taskorchestration.TaskWorkspaceReconstructionEvidenceAdapter)(nil)).Elem(),
	}
	for _, adapter := range interfaces {
		if adapter.NumMethod() != 1 || adapter.Method(0).Name != "Enact" {
			t.Fatalf("downstream adapter gained progression surface: %v", adapter)
		}
	}
	for _, lockAdapter := range []reflect.Type{
		reflect.TypeOf((*taskorchestration.ReleaseManagementAdapter)(nil)).Elem(),
		reflect.TypeOf((*taskorchestration.CatalogPublicationAdapter)(nil)).Elem(),
	} {
		if lockAdapter.NumMethod() != 1 || lockAdapter.Method(0).Name != "Resolve" {
			t.Fatalf("pinned-lock adapter gained mutation surface: %v", lockAdapter)
		}
	}

	forbidden := []string{
		"path", "session", "mount", "storage", "locator", "credential", "sql", "handler",
		"repository", "setstatus", "completephase", "advancetonext", "phasecursor", "phaseoutcome",
	}
	publicTypes := []reflect.Type{
		reflect.TypeOf(taskorchestration.RuntimeAdapterEvidence{}),
		reflect.TypeOf(taskorchestration.SchedulerAdapterEvidence{}),
		reflect.TypeOf(taskorchestration.PublicationAdapterEvidence{}),
		reflect.TypeOf(taskorchestration.UsageAccountingAdapterEvidence{}),
		reflect.TypeOf(taskorchestration.TaskWorkspaceLifecycleAdapterEvidence{}),
		reflect.TypeOf(taskorchestration.TaskWorkspaceReconstructionAdapterEvidence{}),
		reflect.TypeOf(taskorchestration.ResolvedExecutionContract{}),
		reflect.TypeOf(taskorchestration.ResolvedTemplateLockContract{}),
		reflect.TypeOf(taskorchestration.DownstreamError{}),
	}
	for _, publicType := range publicTypes {
		for index := 0; index < publicType.NumField(); index++ {
			field := strings.ToLower(publicType.Field(index).Name)
			for _, word := range forbidden {
				if strings.Contains(field, word) {
					t.Fatalf("public type %s leaks forbidden field %s", publicType, field)
				}
			}
		}
	}
}

type releaseManagementPortDouble struct {
	record taskorchestration.PublishedExecutionContract
	err    error
	calls  int
}

func (port *releaseManagementPortDouble) ResolveExecutionLock(
	_ context.Context,
	_ taskorchestration.ExecutionLockID,
) (taskorchestration.PublishedExecutionContract, error) {
	port.calls++
	return port.record, port.err
}

type catalogPublicationPortDouble struct {
	record taskorchestration.PublishedTemplateLockContract
	err    error
	calls  int
}

func (port *catalogPublicationPortDouble) ResolveTemplateLock(
	_ context.Context,
	_ taskorchestration.TemplateLockID,
) (taskorchestration.PublishedTemplateLockContract, error) {
	port.calls++
	return port.record, port.err
}

func TestUsageAccountingEvidenceCannotReopenTerminalPhase(t *testing.T) {
	now := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	owner := taskorchestration.NewUserAuthority(downstreamAuthorityID(t, "usage-owner"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "usage-terminal-publication"), Kind: taskorchestration.PhasePublication,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "usage-start", "usage-task", now), owner, pinned,
	)); err != nil {
		t.Fatal(err)
	}
	workHeader := intentHeader(t, "usage-work", "usage-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, taskorchestration.NewWorkerAuthority(downstreamAuthorityID(t, "usage-worker"), 1),
		downstreamOperationID(t, "usage-work-available"),
	))
	if err != nil {
		t.Fatal(err)
	}
	view := queryAggregate(t, harness, "usage-task", owner)
	run := view.PhaseRuns[0]
	publicationHeader := intentHeader(t, "usage-publication", "usage-task", now.Add(2*time.Second))
	publicationHeader.ExpectedTaskRevision = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPublicationEvidenceIntent(
		publicationHeader,
		pinned.Authorities.Publication,
		taskorchestration.PublicationEvidenceBinding{
			Evidence:   downstreamEvidenceRef(t, "usage-publication-evidence", taskorchestration.EvidencePublication),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			OperationID: work.EnactmentRefs[0].OperationID, Generation: 1,
			Fence: taskorchestration.PublicationFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome:           taskorchestration.PublicationActivated,
			ArtifactVersionID: downstreamArtifactVersionID(t, "usage-terminal-artifact"),
		},
	)); err != nil {
		t.Fatal(err)
	}
	before := queryAggregate(t, harness, "usage-task", owner)

	ref := downstreamEnactmentRef(t, "usage-close-operation",
		taskorchestration.EnactmentUsageAccounting, taskorchestration.UsageFence(11))
	record := taskorchestration.UsageAccountingEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "late-usage-settlement"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "usage-accounting-authority"), Generation: 9,
		},
		TaskID: before.TaskID, PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
		PhaseRunFence: run.Fence, OperationID: ref.OperationID,
		ActivityGeneration: ref.ActivityGeneration, Generation: 9, Fence: 11,
		SafetyEpoch: before.SafetyEpoch, Kind: taskorchestration.UsageReservationSettled,
		QuotaReservationID: downstreamQuotaReservationID(t, "quota-reservation-terminal"),
	}
	record.EvidenceDigest = taskorchestration.UsageAccountingEvidenceDigest(record)
	adapter := taskorchestration.NewUsageAccountingEvidenceAdapter(
		&usageAccountingPortDouble{record: record}, prerequisiteAuthority(record.Prerequisites),
	)
	evidence, err := adapter.Enact(context.Background(), ref)
	if err != nil || evidence.Kind != taskorchestration.UsageReservationSettled {
		t.Fatalf("late Usage evidence = %#v, err=%v", evidence, err)
	}
	after := queryAggregate(t, harness, "usage-task", owner)
	if !reflect.DeepEqual(after, before) || after.PhaseRuns[0].Outcome != taskorchestration.PhaseRunSucceeded {
		t.Fatalf("late Usage evidence reopened terminal Phase: before=%#v after=%#v", before, after)
	}
}

type usageAccountingPortDouble struct {
	record taskorchestration.UsageAccountingEvidenceRecord
	err    error
	calls  int
}

func (port *usageAccountingPortDouble) EnactUsageAccounting(
	_ context.Context,
	_ taskorchestration.EnactmentRef,
) (taskorchestration.UsageAccountingEvidenceRecord, error) {
	port.calls++
	return port.record, port.err
}

func (port *schedulerEvidencePortDouble) EnactScheduling(
	_ context.Context,
	_ taskorchestration.EnactmentRef,
) (taskorchestration.SchedulerEvidenceRecord, error) {
	port.calls++
	return port.record, port.err
}

type runtimeEvidencePortDouble struct {
	record taskorchestration.RuntimeEvidenceRecord
	err    error
	calls  int
}

func (port *runtimeEvidencePortDouble) EnactRuntime(
	_ context.Context,
	_ taskorchestration.EnactmentRef,
) (taskorchestration.RuntimeEvidenceRecord, error) {
	port.calls++
	return port.record, port.err
}

func downstreamEnactmentRef(
	t *testing.T,
	operation string,
	kind taskorchestration.EnactmentKind,
	fence taskorchestration.EnactmentFenceRef,
) taskorchestration.EnactmentRef {
	t.Helper()
	payload, err := taskorchestration.ParseEnactmentPayloadDigest(
		"1111111111111111111111111111111111111111111111111111111111111111",
	)
	if err != nil {
		t.Fatal(err)
	}
	causation, err := taskorchestration.NewCausationID("causation-" + operation)
	if err != nil {
		t.Fatal(err)
	}
	return taskorchestration.EnactmentRef{
		OperationID:        downstreamOperationID(t, operation),
		Kind:               kind,
		PayloadDigest:      payload,
		ActivityGeneration: 9,
		Fence:              fence,
		CausationID:        causation,
	}
}

func downstreamEvidenceRef(
	t *testing.T,
	id string,
	kind taskorchestration.EvidenceKind,
) taskorchestration.EvidenceRef {
	t.Helper()
	digest, err := taskorchestration.ParseEvidenceDigest(
		"2222222222222222222222222222222222222222222222222222222222222222",
	)
	if err != nil {
		t.Fatal(err)
	}
	return taskorchestration.NewEvidenceRef(downstreamEvidenceID(t, id), kind, digest)
}

func downstreamOperationID(t *testing.T, value string) taskorchestration.OperationID {
	t.Helper()
	id, err := taskorchestration.NewOperationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamEvidenceID(t *testing.T, value string) taskorchestration.EvidenceID {
	t.Helper()
	id, err := taskorchestration.NewEvidenceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamAuthorityID(t *testing.T, value string) taskorchestration.AuthorityID {
	t.Helper()
	id, err := taskorchestration.NewAuthorityID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamTaskID(t *testing.T, value string) taskorchestration.TaskID {
	t.Helper()
	id, err := taskorchestration.NewTaskID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamPhaseRunID(t *testing.T, value string) taskorchestration.PhaseRunID {
	t.Helper()
	id, err := taskorchestration.NewPhaseRunID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamRuntimeRunID(t *testing.T, value string) taskorchestration.RuntimeRunID {
	t.Helper()
	id, err := taskorchestration.NewRuntimeRunID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamArtifactVersionID(t *testing.T, value string) taskorchestration.ArtifactVersionID {
	t.Helper()
	id, err := taskorchestration.NewArtifactVersionID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamTemplateVersionID(t *testing.T, value string) taskorchestration.TemplateVersionID {
	t.Helper()
	id, err := taskorchestration.NewTemplateVersionID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamResourceBundleID(t *testing.T, value string) taskorchestration.ResourceBundleID {
	t.Helper()
	id, err := taskorchestration.NewResourceBundleID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamQuotaReservationID(t *testing.T, value string) taskorchestration.QuotaReservationID {
	t.Helper()
	id, err := taskorchestration.NewQuotaReservationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamSchedulerWorkItemID(t *testing.T, value string) taskorchestration.SchedulerWorkItemID {
	t.Helper()
	id, err := taskorchestration.NewSchedulerWorkItemID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamDeliveryClaimID(t *testing.T, value string) taskorchestration.DeliveryClaimID {
	t.Helper()
	id, err := taskorchestration.NewDeliveryClaimID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamAdmissionGrantID(t *testing.T, value string) taskorchestration.AdmissionGrantID {
	t.Helper()
	id, err := taskorchestration.NewAdmissionGrantID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamRuntimeBindingID(t *testing.T, value string) taskorchestration.RuntimeBindingID {
	t.Helper()
	id, err := taskorchestration.NewRuntimeBindingID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamExecutionNodeID(t *testing.T, value string) taskorchestration.ExecutionNodeID {
	t.Helper()
	id, err := taskorchestration.NewExecutionNodeID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func downstreamSandboxLeaseID(t *testing.T, value string) taskorchestration.SandboxLeaseID {
	t.Helper()
	id, err := taskorchestration.NewSandboxLeaseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func bindRuntimeEvidenceContract(
	t *testing.T,
	record *taskorchestration.RuntimeEvidenceRecord,
	prefix string,
) {
	t.Helper()
	record.RuntimeBindingID = downstreamRuntimeBindingID(t, prefix+"-binding")
	record.RuntimeBindingDigest = downstreamEvidenceRef(t, prefix+"-binding-digest", taskorchestration.EvidenceRuntime).Digest
	record.ImmutableInputManifestDigest = downstreamEvidenceRef(t, prefix+"-input-manifest", taskorchestration.EvidenceRuntime).Digest
	record.ExecutionNodeID = downstreamExecutionNodeID(t, prefix+"-node")
	record.SandboxLeaseID = downstreamSandboxLeaseID(t, prefix+"-lease")
	record.OutputManifestDigest = downstreamEvidenceRef(t, prefix+"-output-manifest", taskorchestration.EvidenceRuntime).Digest
}

func bindTemplatePublishedContract(
	t *testing.T,
	record *taskorchestration.PublishedTemplateLockContract,
	executionLockID taskorchestration.ExecutionLockID,
	prefix string,
) {
	t.Helper()
	record.TemplateManifestDigest = downstreamEvidenceRef(t, prefix+"-template-manifest", taskorchestration.EvidencePublication).Digest
	record.TemplatePackageDigest = downstreamEvidenceRef(t, prefix+"-template-package", taskorchestration.EvidencePublication).Digest
	record.CompatibilityEvidenceID = downstreamEvidenceID(t, prefix+"-compatibility")
	record.CompatibilityEvidenceDigest = downstreamEvidenceRef(t, prefix+"-compatibility-digest", taskorchestration.EvidencePublication).Digest
	record.CompatibilityExecutionLockID = executionLockID
}

func validPublishedTemplateLockContract(
	t *testing.T,
	templateLockID taskorchestration.TemplateLockID,
	executionLockID taskorchestration.ExecutionLockID,
	prefix string,
	observedGeneration taskorchestration.ProducerGeneration,
	safetyEpoch taskorchestration.SafetyEpoch,
) taskorchestration.PublishedTemplateLockContract {
	t.Helper()
	record := taskorchestration.PublishedTemplateLockContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, prefix+"-catalog-authority"), Generation: 3,
		},
		TemplateLockID:     templateLockID,
		TemplateVersionID:  downstreamTemplateVersionID(t, prefix+"-template-version"),
		ObservedGeneration: observedGeneration, SafetyEpoch: safetyEpoch,
		BundleClosure: []taskorchestration.ResourceBundleContract{{
			ResourceBundleID: downstreamResourceBundleID(t, prefix+"-bundle"),
			ManifestDigest:   downstreamEvidenceRef(t, prefix+"-bundle-manifest", taskorchestration.EvidencePublication).Digest,
			PackageDigest:    downstreamEvidenceRef(t, prefix+"-bundle-package", taskorchestration.EvidencePublication).Digest,
		}},
	}
	bindTemplatePublishedContract(t, &record, executionLockID, prefix)
	record.LockDigest = taskorchestration.TemplateLockContractDigest(record)
	return record
}
