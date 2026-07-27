package taskorchestration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestSharedDownstreamEvidenceAdapterBlackBoxContract(t *testing.T) {
	const canary = "host=/private/downstream session=secret mount=/mnt/secret bucket=secret locator=secret credential=secret content=secret"
	factories := []sharedDownstreamAdapterFactory{
		{name: "Runtime", new: newRuntimeSharedContractAdapter},
		{name: "Scheduler", new: newSchedulerSharedContractAdapter},
		{name: "Publication", new: newPublicationSharedContractAdapter},
		{name: "UsageAccounting", new: newUsageSharedContractAdapter},
	}
	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			ref, enact, calls := factory.new(t, sharedAdapterValid, nil)
			first, err := enact(ref)
			if err != nil || first.OperationID != ref.OperationID ||
				first.EvidenceID == (taskorchestration.EvidenceID{}) {
				t.Fatalf("valid evidence = %#v, err=%v", first, err)
			}
			second, err := enact(ref)
			if err != nil || second != first || *calls != 1 {
				t.Fatalf("exact replay = %#v calls=%d err=%v", second, *calls, err)
			}
			mismatched := ref
			mismatched.PayloadDigest, err = taskorchestration.ParseEnactmentPayloadDigest(
				"abababababababababababababababababababababababababababababababab",
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = enact(mismatched)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamIntegrityConflict)

			reorderedRef, reorderedEnact, _ := factory.new(
				t, sharedAdapterPrerequisitesReordered, nil,
			)
			reordered, err := reorderedEnact(reorderedRef)
			if err != nil {
				t.Fatalf("exact prerequisites depended on arrival order: %v", err)
			}
			if reordered.EvidenceDigest != first.EvidenceDigest {
				t.Fatalf("canonical evidence digest depended on prerequisite order")
			}

			for _, scenario := range []struct {
				mode sharedAdapterMode
				want taskorchestration.DownstreamErrorCode
			}{
				{sharedAdapterPrerequisiteMissing, taskorchestration.DownstreamPrerequisitePending},
				{sharedAdapterStale, taskorchestration.DownstreamStale},
				{sharedAdapterUnknownMajor, taskorchestration.DownstreamUnsupportedSchema},
				{sharedAdapterCorrupt, taskorchestration.DownstreamCorruptEvidence},
			} {
				badRef, badEnact, _ := factory.new(t, scenario.mode, nil)
				_, err := badEnact(badRef)
				assertDownstreamErrorCode(t, err, scenario.want)
			}

			errorRef, errorEnact, _ := factory.new(
				t, sharedAdapterValid, fmt.Errorf("wrapped: %s", canary),
			)
			_, err = errorEnact(errorRef)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamDependencyUnavailable)
			if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v", err), canary) {
				t.Fatalf("normalized adapter error leaked raw failure: %v", err)
			}
		})
	}
}

type sharedAdapterMode uint8

const (
	sharedAdapterValid sharedAdapterMode = iota
	sharedAdapterPrerequisiteMissing
	sharedAdapterPrerequisitesReordered
	sharedAdapterStale
	sharedAdapterUnknownMajor
	sharedAdapterCorrupt
)

type sharedEvidenceObservation struct {
	EvidenceID     taskorchestration.EvidenceID
	EvidenceDigest taskorchestration.EvidenceDigest
	OperationID    taskorchestration.OperationID
}

type sharedDownstreamAdapterFactory struct {
	name string
	new  func(*testing.T, sharedAdapterMode, error) (
		taskorchestration.EnactmentRef,
		func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error),
		*int,
	)
}

func newRuntimeSharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (taskorchestration.EnactmentRef, func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error), *int) {
	t.Helper()
	ref := downstreamEnactmentRef(t, "shared-runtime-operation",
		taskorchestration.EnactmentRuntimeExecution, taskorchestration.RuntimeFence(7))
	required := downstreamEvidenceRef(t, "shared-runtime-prerequisite", taskorchestration.EvidenceScheduling)
	secondRequired := downstreamEvidenceRef(t, "shared-runtime-prerequisite-second", taskorchestration.EvidenceUsageAccounting)
	record := taskorchestration.RuntimeEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "shared-runtime-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "shared-runtime-authority"), Generation: 3,
		},
		TaskID:             downstreamTaskID(t, "shared-runtime-task"),
		PhaseRunID:         downstreamPhaseRunID(t, "shared-runtime-phase"),
		PhaseRunGeneration: 5, PhaseRunFence: 6,
		RuntimeRunID: downstreamRuntimeRunID(t, "shared-runtime-run"),
		OperationID:  ref.OperationID, ActivityGeneration: ref.ActivityGeneration,
		Generation: 3, Fence: 7, SafetyEpoch: 2, Outcome: taskorchestration.RuntimeRunSucceeded,
		RequiredPrerequisites: []taskorchestration.EvidenceRef{required, secondRequired},
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	applyRuntimeSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-runtime-corrupt", taskorchestration.EvidenceRuntime).Digest
	}
	port := &runtimeEvidencePortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewRuntimeEvidenceAdapter(port)
	return ref, func(value taskorchestration.EnactmentRef) (sharedEvidenceObservation, error) {
		evidence, err := adapter.Enact(context.Background(), value)
		return sharedEvidenceObservation{evidence.Evidence.ID, evidence.Evidence.Digest, evidence.OperationID}, err
	}, &port.calls
}

func applyRuntimeSharedMode(record *taskorchestration.RuntimeEvidenceRecord, mode sharedAdapterMode) {
	switch mode {
	case sharedAdapterPrerequisiteMissing:
		record.Prerequisites = nil
	case sharedAdapterPrerequisitesReordered:
		record.Prerequisites[0], record.Prerequisites[1] = record.Prerequisites[1], record.Prerequisites[0]
	case sharedAdapterStale:
		record.Fence--
	case sharedAdapterUnknownMajor:
		record.SchemaVersion = taskorchestration.NewEvidenceSchemaVersion(2, 0)
	}
}

func newSchedulerSharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (taskorchestration.EnactmentRef, func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error), *int) {
	t.Helper()
	ref := downstreamEnactmentRef(t, "shared-scheduler-operation",
		taskorchestration.EnactmentScheduling, taskorchestration.SchedulerFence(7))
	required := downstreamEvidenceRef(t, "shared-scheduler-prerequisite", taskorchestration.EvidenceUsageAccounting)
	secondRequired := downstreamEvidenceRef(t, "shared-scheduler-prerequisite-second", taskorchestration.EvidenceRuntime)
	record := taskorchestration.SchedulerEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "shared-scheduler-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "shared-scheduler-authority"), Generation: 3,
		},
		TaskID:             downstreamTaskID(t, "shared-scheduler-task"),
		PhaseRunID:         downstreamPhaseRunID(t, "shared-scheduler-phase"),
		PhaseRunGeneration: 5, PhaseRunFence: 6, OperationID: ref.OperationID,
		ActivityGeneration: ref.ActivityGeneration, Generation: 3, Fence: 7,
		SafetyEpoch: 2, Kind: taskorchestration.SchedulerAdmitted,
		WorkItemID:            downstreamSchedulerWorkItemID(t, "shared-scheduler-work-item"),
		DeliveryClaimID:       downstreamDeliveryClaimID(t, "shared-scheduler-claim"),
		AdmissionGrantID:      downstreamAdmissionGrantID(t, "shared-scheduler-grant"),
		RequiredPrerequisites: []taskorchestration.EvidenceRef{required, secondRequired},
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	applySchedulerSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.SchedulerEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-scheduler-corrupt", taskorchestration.EvidenceScheduling).Digest
	}
	port := &schedulerEvidencePortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewSchedulerEvidenceAdapter(port)
	return ref, func(value taskorchestration.EnactmentRef) (sharedEvidenceObservation, error) {
		evidence, err := adapter.Enact(context.Background(), value)
		return sharedEvidenceObservation{evidence.Evidence.ID, evidence.Evidence.Digest, evidence.OperationID}, err
	}, &port.calls
}

func applySchedulerSharedMode(record *taskorchestration.SchedulerEvidenceRecord, mode sharedAdapterMode) {
	switch mode {
	case sharedAdapterPrerequisiteMissing:
		record.Prerequisites = nil
	case sharedAdapterPrerequisitesReordered:
		record.Prerequisites[0], record.Prerequisites[1] = record.Prerequisites[1], record.Prerequisites[0]
	case sharedAdapterStale:
		record.Fence--
	case sharedAdapterUnknownMajor:
		record.SchemaVersion = taskorchestration.NewEvidenceSchemaVersion(2, 0)
	}
}

func newPublicationSharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (taskorchestration.EnactmentRef, func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error), *int) {
	t.Helper()
	ref := downstreamEnactmentRef(t, "shared-publication-operation",
		taskorchestration.EnactmentArtifactPublication, taskorchestration.PublicationFence(7))
	required := downstreamEvidenceRef(t, "shared-publication-prerequisite", taskorchestration.EvidenceTaskWorkspaceLifecycle)
	secondRequired := downstreamEvidenceRef(t, "shared-publication-prerequisite-second", taskorchestration.EvidenceRuntime)
	record := taskorchestration.PublicationEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "shared-publication-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "shared-publication-authority"), Generation: 3,
		},
		TaskID:             downstreamTaskID(t, "shared-publication-task"),
		PhaseRunID:         downstreamPhaseRunID(t, "shared-publication-phase"),
		PhaseRunGeneration: 5, PhaseRunFence: 6, OperationID: ref.OperationID,
		ActivityGeneration: ref.ActivityGeneration, Generation: 3, Fence: 7, SafetyEpoch: 2,
		Outcome:           taskorchestration.PublicationActivated,
		ArtifactVersionID: downstreamArtifactVersionID(t, "shared-artifact"),
		ArtifactManifestDigest: downstreamEvidenceRef(
			t, "shared-artifact-manifest", taskorchestration.EvidencePublication,
		).Digest,
		RequiredPrerequisites: []taskorchestration.EvidenceRef{required, secondRequired},
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	applyPublicationSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.PublicationEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-publication-corrupt", taskorchestration.EvidencePublication).Digest
	}
	port := &publicationEvidencePortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewPublicationEvidenceAdapter(port)
	return ref, func(value taskorchestration.EnactmentRef) (sharedEvidenceObservation, error) {
		evidence, err := adapter.Enact(context.Background(), value)
		return sharedEvidenceObservation{evidence.Evidence.ID, evidence.Evidence.Digest, evidence.OperationID}, err
	}, &port.calls
}

func applyPublicationSharedMode(record *taskorchestration.PublicationEvidenceRecord, mode sharedAdapterMode) {
	switch mode {
	case sharedAdapterPrerequisiteMissing:
		record.Prerequisites = nil
	case sharedAdapterPrerequisitesReordered:
		record.Prerequisites[0], record.Prerequisites[1] = record.Prerequisites[1], record.Prerequisites[0]
	case sharedAdapterStale:
		record.Fence--
	case sharedAdapterUnknownMajor:
		record.SchemaVersion = taskorchestration.NewEvidenceSchemaVersion(2, 0)
	}
}

func newUsageSharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (taskorchestration.EnactmentRef, func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error), *int) {
	t.Helper()
	ref := downstreamEnactmentRef(t, "shared-usage-operation",
		taskorchestration.EnactmentUsageAccounting, taskorchestration.UsageFence(7))
	required := downstreamEvidenceRef(t, "shared-usage-prerequisite", taskorchestration.EvidenceRuntime)
	secondRequired := downstreamEvidenceRef(t, "shared-usage-prerequisite-second", taskorchestration.EvidenceScheduling)
	record := taskorchestration.UsageAccountingEvidenceRecord{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		EvidenceID:    downstreamEvidenceID(t, "shared-usage-evidence"),
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "shared-usage-authority"), Generation: 3,
		},
		TaskID:             downstreamTaskID(t, "shared-usage-task"),
		PhaseRunID:         downstreamPhaseRunID(t, "shared-usage-phase"),
		PhaseRunGeneration: 5, PhaseRunFence: 6, OperationID: ref.OperationID,
		ActivityGeneration: ref.ActivityGeneration, Generation: 3, Fence: 7, SafetyEpoch: 2,
		Kind:                  taskorchestration.UsageReservationSettled,
		QuotaReservationID:    downstreamQuotaReservationID(t, "shared-quota-reservation"),
		RequiredPrerequisites: []taskorchestration.EvidenceRef{required, secondRequired},
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	applyUsageSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.UsageAccountingEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-usage-corrupt", taskorchestration.EvidenceUsageAccounting).Digest
	}
	port := &usageAccountingPortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewUsageAccountingEvidenceAdapter(port)
	return ref, func(value taskorchestration.EnactmentRef) (sharedEvidenceObservation, error) {
		evidence, err := adapter.Enact(context.Background(), value)
		return sharedEvidenceObservation{evidence.Evidence.ID, evidence.Evidence.Digest, evidence.OperationID}, err
	}, &port.calls
}

func applyUsageSharedMode(record *taskorchestration.UsageAccountingEvidenceRecord, mode sharedAdapterMode) {
	switch mode {
	case sharedAdapterPrerequisiteMissing:
		record.Prerequisites = nil
	case sharedAdapterPrerequisitesReordered:
		record.Prerequisites[0], record.Prerequisites[1] = record.Prerequisites[1], record.Prerequisites[0]
	case sharedAdapterStale:
		record.Fence--
	case sharedAdapterUnknownMajor:
		record.SchemaVersion = taskorchestration.NewEvidenceSchemaVersion(2, 0)
	}
}
