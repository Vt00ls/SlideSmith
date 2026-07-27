package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestSharedDownstreamEvidenceAdapterBlackBoxContract(t *testing.T) {
	const canary = "host=/private/downstream session=secret mount=/mnt/secret bucket=secret locator=secret credential=secret content=secret"
	assertDownstreamAdapterPublicSurfaces(t)
	invalid := taskorchestration.NewDownstreamError(taskorchestration.DownstreamErrorCode(255))
	if invalid.Code() != taskorchestration.DownstreamInvalidEnactment ||
		invalid.RetryDisposition() != taskorchestration.RetryNever ||
		invalid.ReconciliationDisposition() != taskorchestration.ReconciliationNotRequired {
		t.Fatalf("invalid public downstream error code did not fail closed: %#v", invalid)
	}
	factories := []sharedDownstreamAdapterFactory{
		{name: "C04", new: newC04SharedContractAdapter},
		{name: "Runtime", new: newRuntimeSharedContractAdapter, schemaErrors: true, genericTypedErrors: true},
		{name: "Scheduler", new: newSchedulerSharedContractAdapter, schemaErrors: true, genericTypedErrors: true},
		{name: "Publication", new: newPublicationSharedContractAdapter, schemaErrors: true, genericTypedErrors: true},
		{name: "UsageAccounting", new: newUsageSharedContractAdapter, schemaErrors: true, genericTypedErrors: true},
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

			scenarios := []struct {
				mode sharedAdapterMode
				want taskorchestration.DownstreamErrorCode
			}{
				{sharedAdapterPrerequisiteMissing, taskorchestration.DownstreamPrerequisitePending},
				{sharedAdapterStale, taskorchestration.DownstreamStale},
				{sharedAdapterCorrupt, taskorchestration.DownstreamCorruptEvidence},
			}
			if factory.schemaErrors {
				scenarios = append(scenarios, struct {
					mode sharedAdapterMode
					want taskorchestration.DownstreamErrorCode
				}{sharedAdapterUnknownMajor, taskorchestration.DownstreamUnsupportedSchema})
			}
			for _, scenario := range scenarios {
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

			if factory.genericTypedErrors {
				reconciliationRef, reconciliationEnact, _ := factory.new(
					t,
					sharedAdapterValid,
					taskorchestration.NewDownstreamError(
						taskorchestration.DownstreamReconciliationRequired,
					),
				)
				_, err = reconciliationEnact(reconciliationRef)
				assertDownstreamErrorCode(t, err, taskorchestration.DownstreamReconciliationRequired)
				var reconciliationFailure *taskorchestration.DownstreamError
				if !errors.As(err, &reconciliationFailure) ||
					reconciliationFailure.RetryDisposition() != taskorchestration.RetrySameRequest ||
					reconciliationFailure.ReconciliationDisposition() != taskorchestration.ReconciliationRequired {
					t.Fatalf("reconciliation error dispositions = %#v", reconciliationFailure)
				}
				if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v", err), canary) {
					t.Fatalf("typed reconciliation error leaked raw failure: %v", err)
				}
			}
		})
	}

	runSharedPinnedLockAdapterContract(t, canary, []sharedPinnedLockAdapterFactory{
		{name: "ReleaseManagement", new: newReleaseSharedContractAdapter},
		{name: "CatalogPublication", new: newCatalogSharedContractAdapter},
	})
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
	name               string
	schemaErrors       bool
	genericTypedErrors bool
	new                func(*testing.T, sharedAdapterMode, error) (
		taskorchestration.EnactmentRef,
		func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error),
		*int,
	)
}

type sharedPinnedLockObservation struct {
	LockID string
	Digest taskorchestration.EvidenceDigest
}

type sharedPinnedLockAdapterFactory struct {
	name string
	new  func(*testing.T, sharedAdapterMode, error) (
		func(taskorchestration.PinnedLockPurpose, bool) (sharedPinnedLockObservation, error),
		*int,
	)
}

func runSharedPinnedLockAdapterContract(
	t *testing.T,
	canary string,
	factories []sharedPinnedLockAdapterFactory,
) {
	t.Helper()
	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			resolve, calls := factory.new(t, sharedAdapterValid, nil)
			first, err := resolve(taskorchestration.PinnedLockRetry, false)
			if err != nil || first.LockID == "" || first.Digest == (taskorchestration.EvidenceDigest{}) {
				t.Fatalf("valid pinned lock = %#v, err=%v", first, err)
			}
			for _, purpose := range []taskorchestration.PinnedLockPurpose{
				taskorchestration.PinnedLockRecovery,
				taskorchestration.PinnedLockManualEdit,
			} {
				replayed, replayErr := resolve(purpose, false)
				if replayErr != nil || replayed != first || *calls != 1 {
					t.Fatalf("pinned reuse purpose=%d evidence=%#v calls=%d err=%v", purpose, replayed, *calls, replayErr)
				}
			}
			_, err = resolve(taskorchestration.PinnedLockRetry, true)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamIntegrityConflict)

			for _, scenario := range []struct {
				mode sharedAdapterMode
				want taskorchestration.DownstreamErrorCode
			}{
				{sharedAdapterStale, taskorchestration.DownstreamStale},
				{sharedAdapterUnknownMajor, taskorchestration.DownstreamUnsupportedSchema},
				{sharedAdapterCorrupt, taskorchestration.DownstreamCorruptEvidence},
			} {
				badResolve, _ := factory.new(t, scenario.mode, nil)
				_, err := badResolve(taskorchestration.PinnedLockRetry, false)
				assertDownstreamErrorCode(t, err, scenario.want)
			}

			rawResolve, _ := factory.new(t, sharedAdapterValid, fmt.Errorf("wrapped: %s", canary))
			_, err = rawResolve(taskorchestration.PinnedLockRetry, false)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamDependencyUnavailable)
			if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v", err), canary) {
				t.Fatalf("normalized pinned-lock error leaked raw failure: %v", err)
			}

			reconciliationResolve, _ := factory.new(
				t,
				sharedAdapterValid,
				taskorchestration.NewDownstreamError(taskorchestration.DownstreamReconciliationRequired),
			)
			_, err = reconciliationResolve(taskorchestration.PinnedLockRetry, false)
			assertDownstreamErrorCode(t, err, taskorchestration.DownstreamReconciliationRequired)
			var failure *taskorchestration.DownstreamError
			if !errors.As(err, &failure) || failure.RetryDisposition() != taskorchestration.RetrySameRequest ||
				failure.ReconciliationDisposition() != taskorchestration.ReconciliationRequired {
				t.Fatalf("pinned-lock reconciliation dispositions = %#v", failure)
			}
		})
	}
}

func newReleaseSharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (func(taskorchestration.PinnedLockPurpose, bool) (sharedPinnedLockObservation, error), *int) {
	t.Helper()
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-release-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	record := taskorchestration.PublishedExecutionContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "shared-release-authority"), Generation: 4,
		},
		ExecutionLock: pinned.ExecutionLock,
		SafetyEpoch:   3,
	}
	record.ContractDigest = taskorchestration.ExecutionLockContractDigest(record.ExecutionLock)
	request := taskorchestration.PinnedExecutionLockRequest{
		TaskID: downstreamTaskID(t, "shared-release-task"), ExecutionLock: pinned.ExecutionLock,
		ContractDigest: record.ContractDigest, SafetyEpoch: 3,
	}
	switch mode {
	case sharedAdapterStale:
		record.SafetyEpoch = 2
	case sharedAdapterUnknownMajor:
		record.SchemaVersion = taskorchestration.NewEvidenceSchemaVersion(2, 0)
	case sharedAdapterCorrupt:
		record.ContractDigest = taskorchestration.EvidenceDigest{}
	}
	port := &releaseManagementPortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewReleaseManagementAdapter(port)
	return func(
		purpose taskorchestration.PinnedLockPurpose,
		mismatch bool,
	) (sharedPinnedLockObservation, error) {
		candidate := request
		candidate.Purpose = purpose
		if mismatch {
			candidate.SafetyEpoch++
		}
		resolved, err := adapter.Resolve(context.Background(), candidate)
		return sharedPinnedLockObservation{
			LockID: resolved.ExecutionLock.ID.String(), Digest: resolved.ContractDigest,
		}, err
	}, &port.calls
}

func newCatalogSharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (func(taskorchestration.PinnedLockPurpose, bool) (sharedPinnedLockObservation, error), *int) {
	t.Helper()
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "shared-catalog-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	record := taskorchestration.PublishedTemplateLockContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: downstreamAuthorityID(t, "shared-catalog-authority"), Generation: 8,
		},
		TemplateLockID:     pinned.TemplateLockID,
		TemplateVersionID:  downstreamTemplateVersionID(t, "shared-catalog-version"),
		ObservedGeneration: 12,
		SafetyEpoch:        3,
		BundleClosure: []taskorchestration.ResourceBundleContract{{
			ResourceBundleID: downstreamResourceBundleID(t, "shared-catalog-bundle"),
			ManifestDigest: downstreamEvidenceRef(
				t, "shared-catalog-bundle-manifest", taskorchestration.EvidencePublication,
			).Digest,
			PackageDigest: downstreamEvidenceRef(
				t, "shared-catalog-bundle-package", taskorchestration.EvidencePublication,
			).Digest,
		}},
	}
	bindTemplatePublishedContract(t, &record, pinned.ExecutionLock.ID, "shared-catalog")
	record.LockDigest = taskorchestration.TemplateLockContractDigest(record)
	request := taskorchestration.PinnedTemplateLockRequest{
		TaskID: downstreamTaskID(t, "shared-catalog-task"), TemplateLockID: pinned.TemplateLockID,
		LockDigest: record.LockDigest, ObservedGeneration: 12, SafetyEpoch: 3,
	}
	switch mode {
	case sharedAdapterStale:
		request.ObservedGeneration = 13
	case sharedAdapterUnknownMajor:
		record.SchemaVersion = taskorchestration.NewEvidenceSchemaVersion(2, 0)
	case sharedAdapterCorrupt:
		record.TemplatePackageDigest = taskorchestration.EvidenceDigest{}
	}
	port := &catalogPublicationPortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewCatalogPublicationAdapter(port)
	return func(
		purpose taskorchestration.PinnedLockPurpose,
		mismatch bool,
	) (sharedPinnedLockObservation, error) {
		candidate := request
		candidate.Purpose = purpose
		if mismatch {
			candidate.ObservedGeneration++
		}
		resolved, err := adapter.Resolve(context.Background(), candidate)
		return sharedPinnedLockObservation{
			LockID: resolved.TemplateLockID.String(), Digest: resolved.LockDigest,
		}, err
	}, &port.calls
}

func newC04SharedContractAdapter(
	t *testing.T,
	mode sharedAdapterMode,
	rawErr error,
) (taskorchestration.EnactmentRef, func(taskorchestration.EnactmentRef) (sharedEvidenceObservation, error), *int) {
	t.Helper()
	ref := downstreamEnactmentRef(
		t,
		"shared-c04-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(7),
	)
	request, result := c04CommitContractFixture(
		t, ref, "shared-c04-task", "shared-c04-phase", "shared-c04-workspace", 5, 7,
	)
	bindC04EnactmentPayload(t, &ref, request.Operation.RequestDigest)
	prerequisites := []taskorchestration.EvidencePrerequisite{
		{
			Evidence:   downstreamEvidenceRef(t, "shared-c04-validation", taskorchestration.EvidenceRuntime),
			Generation: 2,
			Fence:      4,
		},
		{
			Evidence:   downstreamEvidenceRef(t, "shared-c04-scheduling", taskorchestration.EvidenceScheduling),
			Generation: 3,
			Fence:      5,
		},
	}
	port := &taskWorkspaceLifecyclePortDouble{commit: result, commitErr: rawErr}
	switch mode {
	case sharedAdapterPrerequisiteMissing:
		prerequisites[0].Generation = 0
	case sharedAdapterPrerequisitesReordered:
		prerequisites[0], prerequisites[1] = prerequisites[1], prerequisites[0]
	case sharedAdapterStale:
		port.commitErr = &taskworkspace.Error{Code: taskworkspace.ErrorStaleAuthority}
	case sharedAdapterCorrupt:
		port.commit.ContentEvidenceRoot = ""
	}
	adapter := taskorchestration.NewTaskWorkspaceLifecycleEvidenceAdapter(
		port,
		taskorchestration.TaskWorkspaceLifecycleAdapterBinding{
			Enactment: ref,
			Producer: taskorchestration.EvidenceProducer{
				AuthorityID: downstreamAuthorityID(t, "shared-c04-authority"), Generation: 5,
			},
			TaskID:             downstreamTaskID(t, "shared-c04-task"),
			PhaseRunID:         downstreamPhaseRunID(t, "shared-c04-phase"),
			PhaseRunGeneration: 6,
			PhaseRunFence:      7,
			SafetyEpoch:        2,
			Prerequisites:      prerequisites,
			Commit:             &request,
		},
	)
	return ref, func(ref taskorchestration.EnactmentRef) (sharedEvidenceObservation, error) {
		evidence, err := adapter.Enact(context.Background(), ref)
		return sharedEvidenceObservation{
			EvidenceID: evidence.Evidence.ID, EvidenceDigest: evidence.Evidence.Digest,
			OperationID: evidence.OperationID,
		}, err
	}, &port.commitCalls
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
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	bindRuntimeEvidenceContract(t, &record, "shared-runtime")
	expectedPrerequisites := append([]taskorchestration.EvidencePrerequisite(nil), record.Prerequisites...)
	applyRuntimeSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.RuntimeEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-runtime-corrupt", taskorchestration.EvidenceRuntime).Digest
	}
	port := &runtimeEvidencePortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewRuntimeEvidenceAdapter(port, prerequisiteAuthority(expectedPrerequisites))
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
		WorkItemID:       downstreamSchedulerWorkItemID(t, "shared-scheduler-work-item"),
		DeliveryClaimID:  downstreamDeliveryClaimID(t, "shared-scheduler-claim"),
		AdmissionGrantID: downstreamAdmissionGrantID(t, "shared-scheduler-grant"),
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	expectedPrerequisites := append([]taskorchestration.EvidencePrerequisite(nil), record.Prerequisites...)
	applySchedulerSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.SchedulerEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-scheduler-corrupt", taskorchestration.EvidenceScheduling).Digest
	}
	port := &schedulerEvidencePortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewSchedulerEvidenceAdapter(port, prerequisiteAuthority(expectedPrerequisites))
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
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	expectedPrerequisites := append([]taskorchestration.EvidencePrerequisite(nil), record.Prerequisites...)
	applyPublicationSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.PublicationEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-publication-corrupt", taskorchestration.EvidencePublication).Digest
	}
	port := &publicationEvidencePortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewPublicationEvidenceAdapter(port, prerequisiteAuthority(expectedPrerequisites))
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
		Kind:               taskorchestration.UsageReservationSettled,
		QuotaReservationID: downstreamQuotaReservationID(t, "shared-quota-reservation"),
		Prerequisites: []taskorchestration.EvidencePrerequisite{
			{Evidence: required, Generation: 2, Fence: 4},
			{Evidence: secondRequired, Generation: 3, Fence: 5},
		},
	}
	expectedPrerequisites := append([]taskorchestration.EvidencePrerequisite(nil), record.Prerequisites...)
	applyUsageSharedMode(&record, mode)
	record.EvidenceDigest = taskorchestration.UsageAccountingEvidenceDigest(record)
	if mode == sharedAdapterCorrupt {
		record.EvidenceDigest = downstreamEvidenceRef(t, "shared-usage-corrupt", taskorchestration.EvidenceUsageAccounting).Digest
	}
	port := &usageAccountingPortDouble{record: record, err: rawErr}
	adapter := taskorchestration.NewUsageAccountingEvidenceAdapter(port, prerequisiteAuthority(expectedPrerequisites))
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
