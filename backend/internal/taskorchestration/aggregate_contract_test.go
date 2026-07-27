package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestBareTaskStartFailsClosedWithoutPinnedRouteAndLocks(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "bare-start-owner"), taskorchestration.AuthorizationGeneration(1),
	)

	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "bare-start-request", "bare-start-task", now), owner,
	))
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorInvalidIntent {
		t.Fatalf("bare Start error = %v, want invalid intent", err)
	}
}

func TestPinnedPipelineVersionCreatesTheEntryPhaseFromItsContract(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "aggregate-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{
			Key:                 phaseKey(t, "contract-entry"),
			Kind:                taskorchestration.PhaseNonMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: 1,
			RetryEligible:       true,
			NextPhase:           phaseKey(t, "contract-publication"),
		},
		{
			Key:  phaseKey(t, "contract-publication"),
			Kind: taskorchestration.PhasePublication,
		},
	})

	start, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "aggregate-start", "aggregate-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	if start.TaskProjection.Status != taskorchestration.TaskReady ||
		start.TaskProjection.ExecutionLockID != pinned.ExecutionLock.ID ||
		start.TaskProjection.TemplateLockID != pinned.TemplateLockID ||
		start.TaskProjection.CurrentPhase != phaseKey(t, "contract-entry") ||
		start.TaskProjection.TaskWorkspaceID != pinned.TaskWorkspaceID {
		t.Fatal("start decision did not project the pinned route, locks, and entry Phase")
	}

	workHeader := intentHeader(t, "aggregate-work", "aggregate-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "aggregate-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "aggregate-work-availability"),
	))
	if err != nil {
		t.Fatalf("make pinned work available: %v", err)
	}
	if len(work.AffectedPhaseRuns) != 1 || len(work.EnactmentRefs) != 1 ||
		work.EnactmentRefs[0].Kind != taskorchestration.EnactmentRuntimeExecution {
		t.Fatal("entry Phase did not commit its Phase Run and Runtime enactment together")
	}

	view, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "aggregate-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query aggregate: %v", err)
	}
	if view.Route != taskorchestration.RouteGeneration ||
		view.ExecutionLockID != pinned.ExecutionLock.ID ||
		view.TemplateLockID != pinned.TemplateLockID ||
		view.TaskWorkspaceID != pinned.TaskWorkspaceID ||
		len(view.PhaseRuns) != 1 ||
		view.PhaseRuns[0].PhaseKey != phaseKey(t, "contract-entry") ||
		view.PhaseRuns[0].Attempt != 1 ||
		len(view.PhaseRuns[0].RuntimeRuns) != 1 {
		t.Fatal("aggregate view did not retain pinned locks and entry Phase history")
	}
}

func TestDecisionCreatedRuntimeBindingRejectsAnUnpinnedProducer(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 10, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "producer-binding-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "producer-binding-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "producer-binding-start", "producer-binding-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "producer-binding-work", "producer-binding-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "producer-binding-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "producer-binding-work-available"),
	))
	if err != nil {
		t.Fatalf("make work available: %v", err)
	}
	view := queryAggregate(t, harness, "producer-binding-task", owner)
	run := view.PhaseRuns[0]
	runtimeRun := run.RuntimeRuns[0]
	header := intentHeader(t, "producer-binding-forged", "producer-binding-task", now.Add(2*time.Second))
	header.ExpectedTaskRevision = work.AcceptedTaskRevision
	header.ActivityGeneration = work.TaskProjection.ActivityGeneration

	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		header,
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "producer-binding-forged-runtime"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "producer-binding-forged-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "6767676767676767676767676767676767676767676767676767676767676767"),
			),
			PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation, PhaseRunFence: run.Fence,
			RuntimeRunID: runtimeRun.RuntimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.RuntimeGeneration(run.Generation),
			Fence:      taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	))
	assertDecisionErrorCode(t, err, taskorchestration.ErrorAuthorizationDenied)
}

func TestAggregateEnactmentReconciliationRedeliversTheOriginalOperation(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 10, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "aggregate-reconcile-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "aggregate-reconcile-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "aggregate-reconcile-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "aggregate-reconcile-start", "aggregate-reconcile-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(
		t, "aggregate-reconcile-work", "aggregate-reconcile-task", now.Add(time.Second),
	)
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "aggregate-reconcile-availability"),
	))
	if err != nil {
		t.Fatalf("make aggregate work available: %v", err)
	}
	originalOperationID := work.EnactmentRefs[0].OperationID
	reconcileHeader := intentHeader(
		t, "aggregate-reconcile-redelivery", "aggregate-reconcile-task", now.Add(2*time.Second),
	)
	reconcileHeader.ExpectedTaskRevision = 2
	reconciled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewReconcileEnactmentIntent(
			reconcileHeader, worker, originalOperationID, taskorchestration.ReconciliationFence(1),
		),
	)
	if err != nil {
		t.Fatalf("reconcile aggregate enactment: %v", err)
	}
	if len(reconciled.EnactmentRefs) != 1 ||
		reconciled.EnactmentRefs[0].OperationID != originalOperationID {
		t.Fatal("aggregate reconciliation did not redeliver the original OperationID")
	}
	view := queryAggregate(t, harness, "aggregate-reconcile-task", owner)
	if view.TaskRevision != 3 || view.DecisionCount != 3 || view.EnactmentCount != 1 ||
		len(view.PhaseRuns) != 1 {
		t.Fatal("aggregate reconciliation created a business attempt or a new enactment")
	}
}

func TestPinnedPipelineVersionFailsClosedWithoutItsValidationContract(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 15, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "missing-validation-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{
			Key: phaseKey(t, "missing-validation"), Kind: taskorchestration.PhaseNonMutating,
			RequiredRuntimeRuns: 1, NextPhase: phaseKey(t, "missing-validation-publication"),
		},
		{Key: phaseKey(t, "missing-validation-publication"), Kind: taskorchestration.PhasePublication},
	})
	_, err = harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "missing-validation-start", "missing-validation-task", now), owner, pinned,
	))
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorInvalidIntent {
		t.Fatalf("missing validation contract error = %T, want invalid intent", err)
	}
}

func TestRuntimeSuccessWaitsForValidationBeforeAdvancingANonMutatingPhase(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "validation-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{
			Key:                 phaseKey(t, "inspect-source"),
			Kind:                taskorchestration.PhaseNonMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: 1,
			RetryEligible:       true,
			NextPhase:           phaseKey(t, "publish-deck"),
		},
		{Key: phaseKey(t, "publish-deck"), Kind: taskorchestration.PhasePublication},
	})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "validation-start", "validation-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "validation-work", "validation-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "validation-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "validation-work-availability"),
	))
	if err != nil {
		t.Fatalf("make work available: %v", err)
	}
	view := queryAggregate(t, harness, "validation-task", owner)
	phaseRunID := view.PhaseRuns[0].PhaseRunID
	runtimeRunID := view.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID

	runtimeHeader := intentHeader(t, "validation-runtime", "validation-task", now.Add(2*time.Second))
	runtimeHeader.ExpectedTaskRevision = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		runtimeHeader,
		pinned.Authorities.Runtime,
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "validation-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			PhaseRunID: phaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
			RuntimeRunID: runtimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: 1, Fence: 1, SafetyEpoch: 1,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)); err != nil {
		t.Fatalf("accept Runtime evidence: %v", err)
	}
	view = queryAggregate(t, harness, "validation-task", owner)
	if view.CurrentPhase != phaseKey(t, "inspect-source") ||
		view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunRunning ||
		view.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunSucceeded {
		t.Fatal("Runtime success advanced or completed the authoritative Phase")
	}

	validationHeader := intentHeader(t, "validation-accepted", "validation-task", now.Add(3*time.Second))
	validationHeader.ExpectedTaskRevision = 3
	decision, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader,
			pinned.Authorities.Validator,
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "validation-accepted-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				),
				PhaseRunID: phaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
				Generation: 1, Fence: 1, SafetyEpoch: 1,
				Outcome: taskorchestration.PhaseValidationAccepted,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept validation evidence: %v", err)
	}
	if decision.TaskProjection.CurrentPhase != phaseKey(t, "publish-deck") ||
		decision.TaskProjection.ActivePhaseRunID != (taskorchestration.PhaseRunID{}) {
		t.Fatal("validation did not advance through the pinned Phase edge")
	}
	view = queryAggregate(t, harness, "validation-task", owner)
	if view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunSucceeded {
		t.Fatal("accepted validation did not complete the non-mutating Phase Run")
	}
}

func TestConfirmationGateHasNoRuntimeRunsAndReplaysOnlyExactOwningUserSubmission(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "gate-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	gate := gateID(t, "design-confirmation")
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{
			Key:       phaseKey(t, "confirm-design"),
			Kind:      taskorchestration.PhaseConfirmationGate,
			GateID:    gate,
			NextPhase: phaseKey(t, "publish-confirmed-deck"),
		},
		{Key: phaseKey(t, "publish-confirmed-deck"), Kind: taskorchestration.PhasePublication},
	})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "gate-start", "gate-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "gate-work", "gate-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "gate-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "gate-work-availability"),
	))
	if err != nil {
		t.Fatalf("make Gate work available: %v", err)
	}
	view := queryAggregate(t, harness, "gate-task", owner)
	if view.Status != taskorchestration.TaskAwaitingConfirmation ||
		len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 0 ||
		len(work.EnactmentRefs) != 1 ||
		work.EnactmentRefs[0].Kind != taskorchestration.EnactmentPresentConfirmationGate {
		t.Fatal("Confirmation Gate was not represented as a zero-Runtime-Run Phase")
	}

	gateHeader := intentHeader(t, "gate-submission", "gate-task", now.Add(2*time.Second))
	gateHeader.ExpectedTaskRevision = 2
	submission := taskorchestration.NewSubmitConfirmationGateIntent(
		gateHeader, owner, gate,
		payloadDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	)
	first, err := harness.Mutations.Decide(context.Background(), submission)
	if err != nil {
		t.Fatalf("submit Confirmation Gate: %v", err)
	}
	if first.TaskProjection.CurrentPhase != phaseKey(t, "publish-confirmed-deck") ||
		first.TaskProjection.Status != taskorchestration.TaskRunning {
		t.Fatal("owning User submission did not complete the Gate through its pinned edge")
	}
	replayed, err := harness.Mutations.Decide(context.Background(), submission)
	if err != nil {
		t.Fatalf("replay exact Gate submission: %v", err)
	}
	if replayed.DecisionID != first.DecisionID ||
		replayed.AcceptedTaskRevision != first.AcceptedTaskRevision {
		t.Fatal("exact Gate replay did not return the original committed decision")
	}

	changed := taskorchestration.NewSubmitConfirmationGateIntent(
		gateHeader, owner, gate,
		payloadDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	)
	_, err = harness.Mutations.Decide(context.Background(), changed)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorIntegrityConflict {
		t.Fatalf("changed Gate replay error = %T, want integrity conflict", err)
	}
	view = queryAggregate(t, harness, "gate-task", owner)
	if view.TaskRevision != 3 || view.DecisionCount != 3 ||
		view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunSucceeded {
		t.Fatal("Gate replay changed aggregate history")
	}
}

func TestMutatingPhaseWaitsForValidatedC04CommitEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "mutation-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{
			Key:                 phaseKey(t, "realize-deck"),
			Kind:                taskorchestration.PhaseMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: 1,
			RetryEligible:       true,
			NextPhase:           phaseKey(t, "publish-realized-deck"),
		},
		{Key: phaseKey(t, "publish-realized-deck"), Kind: taskorchestration.PhasePublication},
	})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "mutation-start", "mutation-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "mutation-work", "mutation-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "mutation-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "mutation-work-availability"),
	))
	if err != nil {
		t.Fatalf("make mutation work available: %v", err)
	}
	view := queryAggregate(t, harness, "mutation-task", owner)
	phaseRunID := view.PhaseRuns[0].PhaseRunID
	runtimeRunID := view.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID
	runtimeHeader := intentHeader(t, "mutation-runtime", "mutation-task", now.Add(2*time.Second))
	runtimeHeader.ExpectedTaskRevision = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		runtimeHeader,
		pinned.Authorities.Runtime,
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "mutation-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			PhaseRunID: phaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
			RuntimeRunID: runtimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: 1, Fence: 1, SafetyEpoch: 1,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)); err != nil {
		t.Fatalf("accept Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "mutation-validation", "mutation-task", now.Add(3*time.Second))
	validationHeader.ExpectedTaskRevision = 3
	commitBinding := exactC04CommitRequestBinding(
		t, "mutation-task", phaseRunID, pinned.TaskWorkspaceID, "mutation-c04-operation",
		1, 1, taskorchestration.TaskWorkspaceRevisionID{},
	)
	validation, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader,
			pinned.Authorities.Validator,
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "mutation-validation-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				),
				PhaseRunID: phaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
				Generation: 1, Fence: 1, SafetyEpoch: 1,
				Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: commitBinding,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept validation evidence: %v", err)
	}
	if validation.TaskProjection.CurrentPhase != phaseKey(t, "realize-deck") ||
		len(validation.EnactmentRefs) != 1 ||
		validation.EnactmentRefs[0].Kind != taskorchestration.EnactmentTaskWorkspaceLifecycle {
		t.Fatal("validation bypassed or failed to request the C04 evidence gate")
	}

	lifecycleHeader := intentHeader(t, "mutation-c04", "mutation-task", now.Add(4*time.Second))
	lifecycleHeader.ExpectedTaskRevision = 4
	committed, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			lifecycleHeader,
			pinned.Authorities.TaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "mutation-c04-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
				),
				PhaseRunID: phaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
				OperationID: validation.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: 1, ObservedGeneration: 1, ObservedFence: 2,
				SafetyEpoch:  1,
				Outcome:      taskorchestration.TaskWorkspaceLifecycleCommitted,
				RevisionID:   taskWorkspaceRevisionID(t, "revision-realized-deck"),
				CheckpointID: checkpointID(t, "checkpoint-realized-deck"),
			},
		),
	)
	if err != nil {
		t.Fatalf("accept C04 evidence: %v", err)
	}
	if committed.TaskProjection.CurrentPhase != phaseKey(t, "publish-realized-deck") {
		t.Fatal("exact C04 commit evidence did not advance the mutating Phase")
	}
	view = queryAggregate(t, harness, "mutation-task", owner)
	if view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunSucceeded ||
		view.PhaseRuns[0].RevisionID != taskWorkspaceRevisionID(t, "revision-realized-deck") ||
		view.PhaseRuns[0].CheckpointID != checkpointID(t, "checkpoint-realized-deck") ||
		view.TaskWorkspaceLifecycleGeneration != 1 || view.TaskWorkspaceLifecycleFence != 2 {
		t.Fatal("mutating Phase history omitted its authoritative C04 result")
	}
}

func TestMutatingPhaseRecordsTerminalC04RejectionEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 45, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "rejected-c04-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "rejected-c04-phase"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1, RetryEligible: true,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "rejected-c04-start", "rejected-c04-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "rejected-c04-work", "rejected-c04-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "rejected-c04-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "rejected-c04-availability"),
	))
	if err != nil {
		t.Fatalf("make mutating work available: %v", err)
	}
	view := queryAggregate(t, harness, "rejected-c04-task", owner)
	run := view.PhaseRuns[0]
	runtimeHeader := intentHeader(
		t, "rejected-c04-runtime", "rejected-c04-task", now.Add(2*time.Second),
	)
	runtimeHeader.ExpectedTaskRevision = 2
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(
			runtimeHeader,
			pinned.Authorities.Runtime,
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "rejected-c04-runtime-evidence"),
					taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
				),
				PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
				PhaseRunFence: run.Fence, RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID,
				OperationID: work.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: 1,
				Outcome: taskorchestration.RuntimeRunSucceeded,
			},
		),
	); err != nil {
		t.Fatalf("accept Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(
		t, "rejected-c04-validation", "rejected-c04-task", now.Add(3*time.Second),
	)
	validationHeader.ExpectedTaskRevision = 3
	commitBinding := exactC04CommitRequestBinding(
		t, "rejected-c04-task", run.PhaseRunID, pinned.TaskWorkspaceID,
		"rejected-c04-commit-operation", 1, 1, taskorchestration.TaskWorkspaceRevisionID{},
	)
	validation, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader,
			pinned.Authorities.Validator,
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "rejected-c04-validation-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
				),
				PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
				PhaseRunFence: run.Fence, Generation: 1,
				Fence: taskorchestration.ValidationFence(run.Fence), SafetyEpoch: 1,
				Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: commitBinding,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept validation evidence: %v", err)
	}
	rejectionHeader := intentHeader(
		t, "rejected-c04-terminal", "rejected-c04-task", now.Add(4*time.Second),
	)
	rejectionHeader.ExpectedTaskRevision = 4
	rejected, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			rejectionHeader,
			pinned.Authorities.TaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "rejected-c04-terminal-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "abababababababababababababababababababababababababababababababab"),
				),
				PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
				PhaseRunFence: run.Fence, OperationID: validation.EnactmentRefs[0].OperationID,
				Generation: 1, Fence: taskorchestration.TaskWorkspaceLifecycleFence(run.Fence),
				ObservedGeneration: 1, ObservedFence: taskorchestration.TaskWorkspaceLifecycleFence(run.Fence + 1),
				SafetyEpoch: 1, Outcome: taskorchestration.TaskWorkspaceLifecycleRejected,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept terminal C04 rejection: %v", err)
	}
	if rejected.TaskProjection.Status != taskorchestration.TaskFailed ||
		len(rejected.AcceptedEvidenceRefs) != 1 {
		t.Fatal("terminal C04 rejection did not fail the mutating Phase")
	}
	view = queryAggregate(t, harness, "rejected-c04-task", owner)
	if view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunFailed ||
		view.PhaseRuns[0].LifecycleOutcome != taskorchestration.TaskWorkspaceLifecycleRejected {
		t.Fatal("aggregate history omitted terminal C04 rejection evidence")
	}
}

func TestPublicationPhaseCompletesOnlyFromArtifactActivationEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "publication-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{Key: phaseKey(t, "activate-artifact"), Kind: taskorchestration.PhasePublication},
	})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "publication-start", "publication-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "publication-work", "publication-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "publication-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "publication-work-availability"),
	))
	if err != nil {
		t.Fatalf("make publication work available: %v", err)
	}
	view := queryAggregate(t, harness, "publication-task", owner)
	if len(view.PhaseRuns[0].RuntimeRuns) != 0 || len(work.EnactmentRefs) != 1 ||
		work.EnactmentRefs[0].Kind != taskorchestration.EnactmentArtifactPublication ||
		view.Status != taskorchestration.TaskRunning {
		t.Fatal("publication Phase completed without Artifact activation evidence")
	}

	publicationHeader := intentHeader(t, "publication-activation", "publication-task", now.Add(2*time.Second))
	publicationHeader.ExpectedTaskRevision = 2
	artifact := artifactVersionID(t, "artifact-version-publication")
	decision, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPublicationEvidenceIntent(
			publicationHeader,
			pinned.Authorities.Publication,
			taskorchestration.PublicationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "publication-evidence"), taskorchestration.EvidencePublication,
					evidenceDigest(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
				),
				PhaseRunID: view.PhaseRuns[0].PhaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
				OperationID: work.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: 1, SafetyEpoch: 1,
				Outcome:           taskorchestration.PublicationActivated,
				ArtifactVersionID: artifact,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept publication evidence: %v", err)
	}
	if decision.TaskProjection.Status != taskorchestration.TaskCompleted ||
		decision.TaskProjection.LatestArtifactVersionID != artifact {
		t.Fatal("Artifact activation did not complete publication")
	}
	view = queryAggregate(t, harness, "publication-task", owner)
	if view.LatestArtifactVersionID != artifact ||
		view.PhaseRuns[0].PublicationOutcome != taskorchestration.PublicationActivated ||
		view.PhaseRuns[0].ArtifactVersionID != artifact {
		t.Fatal("publication history omitted the activated Artifact Version")
	}
}

func TestRetryCreatesANewAttemptAndPreservesLocksAndHistory(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "retry-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{
			Key: phaseKey(t, "retryable-analysis"), Kind: taskorchestration.PhaseNonMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: 1, RetryEligible: true,
			NextPhase: phaseKey(t, "retry-publication"),
		},
		{Key: phaseKey(t, "retry-publication"), Kind: taskorchestration.PhasePublication},
	})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "retry-start", "retry-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "retry-work", "retry-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "retry-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "retry-work-availability"),
	))
	if err != nil {
		t.Fatalf("make retryable work available: %v", err)
	}
	view := queryAggregate(t, harness, "retry-task", owner)
	failedRunID := view.PhaseRuns[0].PhaseRunID
	runtimeHeader := intentHeader(t, "retry-runtime-failed", "retry-task", now.Add(2*time.Second))
	runtimeHeader.ExpectedTaskRevision = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		runtimeHeader,
		pinned.Authorities.Runtime,
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "retry-runtime-failed-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
			),
			PhaseRunID: failedRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
			RuntimeRunID: view.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID,
			OperationID:  work.EnactmentRefs[0].OperationID,
			Generation:   1, Fence: 1, SafetyEpoch: 1,
			Outcome: taskorchestration.RuntimeRunFailed,
		},
	)); err != nil {
		t.Fatalf("accept failed Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "retry-validation-rejected", "retry-task", now.Add(3*time.Second))
	validationHeader.ExpectedTaskRevision = 3
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader,
			pinned.Authorities.Validator,
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "retry-validation-rejected-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
				),
				PhaseRunID: failedRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
				Generation: 1, Fence: 1, SafetyEpoch: 1,
				Outcome: taskorchestration.PhaseValidationRejected,
			},
		),
	); err != nil {
		t.Fatalf("accept rejected validation: %v", err)
	}

	retryHeader := intentHeader(t, "retry-new-attempt", "retry-task", now.Add(4*time.Second))
	retryHeader.ExpectedTaskRevision = 4
	retry, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewRetryPhaseIntent(
		retryHeader, owner, failedRunID,
	))
	if err != nil {
		t.Fatalf("retry failed Phase: %v", err)
	}
	view = queryAggregate(t, harness, "retry-task", owner)
	if len(retry.AffectedPhaseRuns) != 1 || len(view.PhaseRuns) != 2 ||
		view.PhaseRuns[0].PhaseRunID != failedRunID ||
		view.PhaseRuns[0].Outcome != taskorchestration.PhaseRunFailed ||
		view.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunFailed ||
		view.PhaseRuns[1].PhaseRunID == failedRunID || view.PhaseRuns[1].Attempt != 2 ||
		view.PhaseRuns[1].Generation != 2 || view.PhaseRuns[1].Fence != 2 ||
		view.ActivePhaseRunID != view.PhaseRuns[1].PhaseRunID {
		t.Fatal("retry overwrote history or failed to create a newly fenced attempt")
	}
	if view.ExecutionLockID != pinned.ExecutionLock.ID || view.TemplateLockID != pinned.TemplateLockID {
		t.Fatal("retry repinned immutable Task locks")
	}
}

func TestRuntimeRetryCreatesANewRuntimeRunInsideTheSamePhaseRun(t *testing.T) {
	now := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(authorityID(t, "runtime-retry-owner"), 1)
	worker := taskorchestration.NewWorkerAuthority(authorityID(t, "runtime-retry-worker"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "runtime-retry-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1, RetryEligible: true,
	}})
	start, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "runtime-retry-start", "runtime-retry-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start runtime-retry Task: %v", err)
	}
	workHeader := intentHeader(t, "runtime-retry-work", "runtime-retry-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "runtime-retry-work-available"),
	))
	if err != nil {
		t.Fatalf("make initial Runtime work available: %v", err)
	}
	view := queryAggregate(t, harness, "runtime-retry-task", owner)
	phase := view.PhaseRuns[0]
	failedRuntime := phase.RuntimeRuns[0]
	failedHeader := intentHeader(t, "runtime-retry-failed", "runtime-retry-task", now.Add(2*time.Second))
	failedHeader.ExpectedTaskRevision = work.AcceptedTaskRevision
	failed, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		failedHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "runtime-retry-failed-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "6868686868686868686868686868686868686868686868686868686868686868"),
			),
			PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation, PhaseRunFence: phase.Fence,
			RuntimeRunID: failedRuntime.RuntimeRunID, OperationID: work.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:      taskorchestration.RuntimeFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunFailed,
		},
	))
	if err != nil {
		t.Fatalf("accept failed Runtime evidence: %v", err)
	}
	retryHeader := intentHeader(t, "runtime-retry-command", "runtime-retry-task", now.Add(3*time.Second))
	retryHeader.ExpectedTaskRevision = failed.AcceptedTaskRevision
	retryIntent := taskorchestration.NewRetryRuntimeRunIntent(
		retryHeader, worker, phase.PhaseRunID, failedRuntime.RuntimeRunID,
	)
	retried, err := harness.Mutations.Decide(context.Background(), retryIntent)
	if err != nil {
		t.Fatalf("retry failed Runtime Run: %v", err)
	}
	view = queryAggregate(t, harness, "runtime-retry-task", owner)
	if len(view.PhaseRuns) != 1 || view.PhaseRuns[0].PhaseRunID != phase.PhaseRunID ||
		view.PhaseRuns[0].Attempt != phase.Attempt || view.PhaseRuns[0].Generation != phase.Generation ||
		view.PhaseRuns[0].Fence != phase.Fence || len(view.PhaseRuns[0].RuntimeRuns) != 2 ||
		view.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID != failedRuntime.RuntimeRunID ||
		view.PhaseRuns[0].RuntimeRuns[0].Outcome != taskorchestration.RuntimeRunFailed ||
		view.PhaseRuns[0].RuntimeRuns[1].RuntimeRunID == failedRuntime.RuntimeRunID ||
		view.PhaseRuns[0].RuntimeRuns[1].Outcome != taskorchestration.RuntimeRunPending ||
		len(retried.EnactmentRefs) != 1 ||
		retried.EnactmentRefs[0].Kind != taskorchestration.EnactmentRuntimeExecution ||
		retried.EnactmentRefs[0].OperationID == work.EnactmentRefs[0].OperationID ||
		retried.TaskProjection.ExecutionLockID != pinned.ExecutionLock.ID ||
		retried.TaskProjection.TemplateLockID != pinned.TemplateLockID ||
		retried.MandatoryAuditFactRef.RetryRuntimeRunID != failedRuntime.RuntimeRunID {
		t.Fatalf("Runtime retry changed Phase attempt/history/locks: decision=%+v view=%+v", retried, view)
	}

	retriedRuntime := view.PhaseRuns[0].RuntimeRuns[1]
	invalidRetryHeader := intentHeader(t, "runtime-retry-pending", "runtime-retry-task", now.Add(3500*time.Millisecond))
	invalidRetryHeader.ExpectedTaskRevision = retried.AcceptedTaskRevision
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewRetryRuntimeRunIntent(
		invalidRetryHeader, worker, phase.PhaseRunID, retriedRuntime.RuntimeRunID,
	))
	assertDecisionErrorCode(t, err, taskorchestration.ErrorTerminalConflict)
	unauthorizedHeader := intentHeader(t, "runtime-retry-unauthorized", "runtime-retry-task", now.Add(3600*time.Millisecond))
	unauthorizedHeader.ExpectedTaskRevision = retried.AcceptedTaskRevision
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewRetryRuntimeRunIntent(
		unauthorizedHeader,
		taskorchestration.NewWorkerAuthority(authorityID(t, "runtime-retry-foreign-worker"), 1),
		phase.PhaseRunID, failedRuntime.RuntimeRunID,
	))
	assertDecisionErrorCode(t, err, taskorchestration.ErrorAuthorizationDenied)
	crossScopeHeader := intentHeader(t, "runtime-retry-cross-scope", "runtime-retry-task", now.Add(3700*time.Millisecond))
	crossScopeHeader.ExpectedTaskRevision = retried.AcceptedTaskRevision
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewRetryRuntimeRunIntent(
		crossScopeHeader, worker, phaseRunID(t, "runtime-retry-other-phase"), failedRuntime.RuntimeRunID,
	))
	assertDecisionErrorCode(t, err, taskorchestration.ErrorTerminalConflict)

	successHeader := intentHeader(t, "runtime-retry-success", "runtime-retry-task", now.Add(4*time.Second))
	successHeader.ExpectedTaskRevision = retried.AcceptedTaskRevision
	succeeded, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		successHeader, pinned.Authorities.Runtime, taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "runtime-retry-success-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "6969696969696969696969696969696969696969696969696969696969696969"),
			),
			PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation, PhaseRunFence: phase.Fence,
			RuntimeRunID: retriedRuntime.RuntimeRunID, OperationID: retried.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.RuntimeGeneration(phase.Generation),
			Fence:      taskorchestration.RuntimeFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	))
	if err != nil {
		t.Fatalf("accept retried Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "runtime-retry-validation", "runtime-retry-task", now.Add(5*time.Second))
	validationHeader.ExpectedTaskRevision = succeeded.AcceptedTaskRevision
	completed, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
		validationHeader, pinned.Authorities.Validator, taskorchestration.ValidationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "runtime-retry-validation-evidence"), taskorchestration.EvidencePhaseValidation,
				evidenceDigest(t, "7070707070707070707070707070707070707070707070707070707070707070"),
			),
			PhaseRunID: phase.PhaseRunID, PhaseRunGeneration: phase.Generation, PhaseRunFence: phase.Fence,
			Generation: taskorchestration.ProducerGeneration(phase.Generation),
			Fence:      taskorchestration.ValidationFence(phase.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.PhaseValidationAccepted,
		},
	))
	if err != nil || completed.TaskProjection.Status != taskorchestration.TaskCompleted {
		t.Fatalf("validation did not aggregate the successful retry: decision=%+v err=%v", completed, err)
	}
	replayed, err := harness.Mutations.Decide(context.Background(), retryIntent)
	if err != nil || replayed.DecisionID != retried.DecisionID ||
		replayed.EnactmentRefs[0].OperationID != retried.EnactmentRefs[0].OperationID {
		t.Fatalf("Runtime retry exact replay changed its Runtime Run or OperationID: %+v err=%v", replayed, err)
	}
}

func TestManualEditUsesLatestArtifactAndThePinnedPostPublicationGraph(t *testing.T) {
	now := time.Date(2026, time.July, 26, 18, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "manual-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
		{Key: phaseKey(t, "initial-publication"), Kind: taskorchestration.PhasePublication},
	})
	pinned.ExecutionLock.PipelineContract.ManualEditEntryPhase = phaseKey(t, "manual-apply")
	pinned.ExecutionLock.PipelineContract.ManualEditPhases = []taskorchestration.PhaseDefinition{
		{
			Key: phaseKey(t, "manual-apply"), Kind: taskorchestration.PhaseMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: 1, RetryEligible: true,
			NextPhase: phaseKey(t, "manual-publication"),
		},
		{Key: phaseKey(t, "manual-publication"), Kind: taskorchestration.PhasePublication},
	}
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "manual-start", "manual-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "manual-initial-work", "manual-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "manual-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "manual-initial-work-availability"),
	))
	if err != nil {
		t.Fatalf("make initial publication available: %v", err)
	}
	view := queryAggregate(t, harness, "manual-task", owner)
	initialArtifact := artifactVersionID(t, "manual-artifact-v1")
	publicationHeader := intentHeader(t, "manual-initial-publication", "manual-task", now.Add(2*time.Second))
	publicationHeader.ExpectedTaskRevision = 2
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPublicationEvidenceIntent(
			publicationHeader,
			pinned.Authorities.Publication,
			taskorchestration.PublicationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "manual-initial-publication-evidence"),
					taskorchestration.EvidencePublication,
					evidenceDigest(t, "1111111111111111111111111111111111111111111111111111111111111111"),
				),
				PhaseRunID: view.PhaseRuns[0].PhaseRunID, PhaseRunGeneration: 1, PhaseRunFence: 1,
				OperationID: work.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: 1, SafetyEpoch: 1,
				Outcome:           taskorchestration.PublicationActivated,
				ArtifactVersionID: initialArtifact,
			},
		),
	); err != nil {
		t.Fatalf("complete initial publication: %v", err)
	}

	staleHeader := intentHeader(t, "manual-stale-artifact", "manual-task", now.Add(3*time.Second))
	staleHeader.ExpectedTaskRevision = 3
	staleHeader.ActivityGeneration = 2
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewBeginManualEditIntent(
		staleHeader, owner, artifactVersionID(t, "manual-artifact-v0"),
	))
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorTerminalConflict {
		t.Fatalf("stale manual Artifact error = %T, want terminal conflict", err)
	}

	manualHeader := intentHeader(t, "manual-begin", "manual-task", now.Add(4*time.Second))
	manualHeader.ExpectedTaskRevision = 3
	manualHeader.ActivityGeneration = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewBeginManualEditIntent(
		manualHeader, owner, initialArtifact,
	)); err != nil {
		t.Fatalf("begin manual edit: %v", err)
	}
	manualWorkHeader := intentHeader(t, "manual-work", "manual-task", now.Add(5*time.Second))
	manualWorkHeader.ExpectedTaskRevision = 4
	manualWorkHeader.ActivityGeneration = 2
	manualWork, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		manualWorkHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "manual-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "manual-work-availability"),
	))
	if err != nil {
		t.Fatalf("make manual-edit work available: %v", err)
	}
	view = queryAggregate(t, harness, "manual-task", owner)
	if view.Activity != taskorchestration.ActivityManualEdit || view.ActivityGeneration != 2 ||
		view.CurrentPhase != phaseKey(t, "manual-apply") || len(view.PhaseRuns) != 2 ||
		view.PhaseRuns[0].PhaseKey != phaseKey(t, "initial-publication") ||
		view.PhaseRuns[1].PhaseKey != phaseKey(t, "manual-apply") ||
		view.ExecutionLockID != pinned.ExecutionLock.ID || view.TemplateLockID != pinned.TemplateLockID ||
		view.TaskWorkspaceID != pinned.TaskWorkspaceID ||
		view.PhaseRuns[1].TaskWorkspaceID != pinned.TaskWorkspaceID ||
		view.PhaseRuns[1].InputArtifactVersionID != initialArtifact {
		t.Fatal("manual edit forked Task authority, locks, history, or pinned graph")
	}

	parallelHeader := intentHeader(t, "manual-parallel", "manual-task", now.Add(6*time.Second))
	parallelHeader.ExpectedTaskRevision = 5
	parallelHeader.ActivityGeneration = 3
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewBeginManualEditIntent(
		parallelHeader, owner, initialArtifact,
	))
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorTerminalConflict {
		t.Fatalf("parallel manual edit error = %T, want terminal conflict", err)
	}
	view = queryAggregate(t, harness, "manual-task", owner)
	if len(view.PhaseRuns) != 2 || view.ActivePhaseRunID != view.PhaseRuns[1].PhaseRunID {
		t.Fatal("Task admitted more than one mutation-bearing activity")
	}

	manualRun := view.PhaseRuns[1]
	runtimeHeader := intentHeader(t, "manual-runtime", "manual-task", now.Add(7*time.Second))
	runtimeHeader.ExpectedTaskRevision = 5
	runtimeHeader.ActivityGeneration = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
		runtimeHeader,
		pinned.Authorities.Runtime,
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "7777777777777777777777777777777777777777777777777777777777777777"),
			),
			PhaseRunID: manualRun.PhaseRunID, PhaseRunGeneration: manualRun.Generation,
			PhaseRunFence: manualRun.Fence, RuntimeRunID: manualRun.RuntimeRuns[0].RuntimeRunID,
			OperationID: manualWork.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: taskorchestration.RuntimeFence(manualRun.Fence), SafetyEpoch: 1,
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)); err != nil {
		t.Fatalf("accept manual Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "manual-validation", "manual-task", now.Add(8*time.Second))
	validationHeader.ExpectedTaskRevision = 6
	validationHeader.ActivityGeneration = 2
	manualCommitBinding := exactC04CommitRequestBinding(
		t, "manual-task", manualRun.PhaseRunID, pinned.TaskWorkspaceID,
		"manual-lifecycle-operation", 1, taskorchestration.TaskWorkspaceLifecycleFence(manualRun.Fence),
		view.LatestRevisionID,
	)
	validation, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
		validationHeader,
		pinned.Authorities.Validator,
		taskorchestration.ValidationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-validation-evidence"), taskorchestration.EvidencePhaseValidation,
				evidenceDigest(t, "8888888888888888888888888888888888888888888888888888888888888888"),
			),
			PhaseRunID: manualRun.PhaseRunID, PhaseRunGeneration: manualRun.Generation,
			PhaseRunFence: manualRun.Fence, Generation: 1,
			Fence: taskorchestration.ValidationFence(manualRun.Fence), SafetyEpoch: 1,
			Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: manualCommitBinding,
		},
	))
	if err != nil {
		t.Fatalf("validate manual mutation: %v", err)
	}
	lifecycleHeader := intentHeader(t, "manual-lifecycle", "manual-task", now.Add(9*time.Second))
	lifecycleHeader.ExpectedTaskRevision = 7
	lifecycleHeader.ActivityGeneration = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
		lifecycleHeader,
		pinned.Authorities.TaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-lifecycle-evidence"),
				taskorchestration.EvidenceTaskWorkspaceLifecycle,
				evidenceDigest(t, "9999999999999999999999999999999999999999999999999999999999999999"),
			),
			PhaseRunID: manualRun.PhaseRunID, PhaseRunGeneration: manualRun.Generation,
			PhaseRunFence: manualRun.Fence, OperationID: validation.EnactmentRefs[0].OperationID,
			Generation: 1, Fence: taskorchestration.TaskWorkspaceLifecycleFence(manualRun.Fence),
			ObservedGeneration: 1, ObservedFence: taskorchestration.TaskWorkspaceLifecycleFence(manualRun.Fence + 1),
			SafetyEpoch:  1,
			Outcome:      taskorchestration.TaskWorkspaceLifecycleCommitted,
			RevisionID:   taskWorkspaceRevisionID(t, "manual-revision-v2"),
			CheckpointID: checkpointID(t, "manual-checkpoint-v2"),
		},
	)); err != nil {
		t.Fatalf("commit manual mutation: %v", err)
	}
	manualPublicationHeader := intentHeader(t, "manual-publication-work", "manual-task", now.Add(10*time.Second))
	manualPublicationHeader.ExpectedTaskRevision = 8
	manualPublicationHeader.ActivityGeneration = 2
	manualPublication, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		manualPublicationHeader,
		taskorchestration.NewWorkerAuthority(
			authorityID(t, "manual-worker"), taskorchestration.AuthorizationGeneration(1),
		),
		operationID(t, "manual-publication-work-availability"),
	))
	if err != nil {
		t.Fatalf("make manual publication available: %v", err)
	}
	view = queryAggregate(t, harness, "manual-task", owner)
	manualPublicationRun := view.PhaseRuns[2]
	childArtifact := artifactVersionID(t, "manual-artifact-v2")
	childPublicationHeader := intentHeader(t, "manual-child-publication", "manual-task", now.Add(11*time.Second))
	childPublicationHeader.ExpectedTaskRevision = 9
	childPublicationHeader.ActivityGeneration = 2
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPublicationEvidenceIntent(
		childPublicationHeader,
		pinned.Authorities.Publication,
		taskorchestration.PublicationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-child-publication-evidence"), taskorchestration.EvidencePublication,
				evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			PhaseRunID:         manualPublicationRun.PhaseRunID,
			PhaseRunGeneration: manualPublicationRun.Generation, PhaseRunFence: manualPublicationRun.Fence,
			OperationID: manualPublication.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: taskorchestration.PublicationFence(manualPublicationRun.Fence), SafetyEpoch: 1,
			Outcome: taskorchestration.PublicationActivated, ArtifactVersionID: childArtifact,
		},
	)); err != nil {
		t.Fatalf("complete manual child publication: %v", err)
	}
	view = queryAggregate(t, harness, "manual-task", owner)
	if view.Status != taskorchestration.TaskCompleted || view.Activity != 0 ||
		view.LatestArtifactVersionID != childArtifact || len(view.PhaseRuns) != 3 {
		t.Fatal("manual edit did not complete through child publication on the same Task")
	}
}

func TestManualEditAfterExpiryRequiresExactC04ReconstructionEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 27, 11, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(authorityID(t, "manual-expiry-owner"), 1)
	worker := taskorchestration.NewWorkerAuthority(authorityID(t, "manual-expiry-worker"), 1)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "manual-expiry-initial-publication"), Kind: taskorchestration.PhasePublication,
	}})
	pinned.ExecutionLock.PipelineContract.ManualEditEntryPhase = phaseKey(t, "manual-expiry-apply")
	pinned.ExecutionLock.PipelineContract.ManualEditPhases = []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "manual-expiry-apply"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}}
	start, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "manual-expiry-start", "manual-expiry-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start manual-expiry Task: %v", err)
	}
	publicationWorkHeader := intentHeader(
		t, "manual-expiry-publication-work", "manual-expiry-task", now.Add(time.Second),
	)
	publicationWorkHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	publicationWork, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		publicationWorkHeader, worker, operationID(t, "manual-expiry-publication-work-available"),
	))
	if err != nil {
		t.Fatalf("make initial publication available: %v", err)
	}
	view := queryAggregate(t, harness, "manual-expiry-task", owner)
	publicationRun := view.PhaseRuns[0]
	reconstructionRef := downstreamEnactmentRef(
		t, "manual-expiry-reconstruction-operation",
		taskorchestration.EnactmentTaskWorkspaceLifecycle,
		taskorchestration.TaskWorkspaceLifecycleFence(2),
	)
	reconstructionRequest, _ := c04ReconstructionContractFixture(
		t, reconstructionRef, "manual-expiry-task", pinned.TaskWorkspaceID.String(), 2, 2,
	)
	reconstructionBinding, err := taskorchestration.NewTaskWorkspaceReconstructionRequestBinding(
		reconstructionRequest,
	)
	if err != nil {
		t.Fatalf("bind exact manual-expiry C04 reconstruction request: %v", err)
	}
	artifact := artifactVersionID(
		t, string(reconstructionRequest.Intent.ArtifactVersionInput.ArtifactVersionID),
	)
	publicationHeader := intentHeader(
		t, "manual-expiry-publication", "manual-expiry-task", now.Add(2*time.Second),
	)
	publicationHeader.ExpectedTaskRevision = publicationWork.AcceptedTaskRevision
	published, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPublicationEvidenceIntent(
		publicationHeader, pinned.Authorities.Publication, taskorchestration.PublicationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-expiry-publication-evidence"), taskorchestration.EvidencePublication,
				evidenceDigest(t, "7272727272727272727272727272727272727272727272727272727272727272"),
			),
			PhaseRunID: publicationRun.PhaseRunID, PhaseRunGeneration: publicationRun.Generation,
			PhaseRunFence: publicationRun.Fence, OperationID: publicationWork.EnactmentRefs[0].OperationID,
			Generation: taskorchestration.ProducerGeneration(publicationRun.Generation),
			Fence:      taskorchestration.PublicationFence(publicationRun.Fence), SafetyEpoch: view.SafetyEpoch,
			Outcome: taskorchestration.PublicationActivated, ArtifactVersionID: artifact,
		},
	))
	if err != nil {
		t.Fatalf("complete initial publication: %v", err)
	}
	beginHeader := intentHeader(t, "manual-expiry-begin", "manual-expiry-task", now.Add(3*time.Second))
	beginHeader.ExpectedTaskRevision = published.AcceptedTaskRevision
	beginHeader.ActivityGeneration = 2
	reconstruction, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewBeginManualEditAfterExpiryIntent(
			beginHeader, owner, artifact, reconstructionBinding,
		),
	)
	if err != nil || len(reconstruction.EnactmentRefs) != 1 ||
		reconstruction.EnactmentRefs[0].Kind != taskorchestration.EnactmentTaskWorkspaceLifecycle {
		t.Fatalf("expired manual edit did not request C04 reconstruction: %+v err=%v", reconstruction, err)
	}
	blockedHeader := intentHeader(t, "manual-expiry-blocked-work", "manual-expiry-task", now.Add(4*time.Second))
	blockedHeader.ExpectedTaskRevision = reconstruction.AcceptedTaskRevision
	blockedHeader.ActivityGeneration = 2
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		blockedHeader, worker, operationID(t, "manual-expiry-blocked-work-available"),
	))
	assertDecisionErrorCode(t, err, taskorchestration.ErrorTerminalConflict)

	revision := taskWorkspaceRevisionID(t, "manual-expiry-reconstructed-revision")
	checkpoint := checkpointID(t, "manual-expiry-reconstructed-checkpoint")
	evidenceHeader := intentHeader(t, "manual-expiry-reconstructed", "manual-expiry-task", now.Add(5*time.Second))
	evidenceHeader.ExpectedTaskRevision = reconstruction.AcceptedTaskRevision
	evidenceHeader.ActivityGeneration = 2
	reconstructed, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceReconstructionEvidenceIntent(
			evidenceHeader, pinned.Authorities.TaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceReconstructionEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "manual-expiry-reconstruction-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "7373737373737373737373737373737373737373737373737373737373737373"),
				),
				OperationID:       reconstruction.EnactmentRefs[0].OperationID,
				ArtifactVersionID: artifact, RevisionID: revision, CheckpointID: checkpoint,
				Generation: 2, Fence: 2, ObservedGeneration: 3, ObservedFence: 3,
				SafetyEpoch: view.SafetyEpoch,
			},
		),
	)
	if err != nil || len(reconstructed.AcceptedEvidenceRefs) != 1 ||
		reconstructed.TaskProjection.LatestRevisionID != revision ||
		reconstructed.TaskProjection.LatestCheckpointID != checkpoint {
		t.Fatalf("exact C04 reconstruction evidence was not accepted: %+v err=%v", reconstructed, err)
	}
	manualWorkHeader := intentHeader(t, "manual-expiry-work", "manual-expiry-task", now.Add(6*time.Second))
	manualWorkHeader.ExpectedTaskRevision = reconstructed.AcceptedTaskRevision
	manualWorkHeader.ActivityGeneration = 2
	manualWork, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		manualWorkHeader, worker, operationID(t, "manual-expiry-work-available"),
	))
	if err != nil || len(manualWork.EnactmentRefs) != 1 ||
		manualWork.EnactmentRefs[0].Kind != taskorchestration.EnactmentRuntimeExecution {
		t.Fatalf("manual mutation did not wait for reconstruction evidence: %+v err=%v", manualWork, err)
	}
}

func TestCancelProjectsTerminalOrCancellingWithoutRepinningOrAdvancing(t *testing.T) {
	now := time.Date(2026, time.July, 26, 18, 30, 0, 0, time.UTC)
	t.Run("before a mutation-bearing Phase Run", func(t *testing.T) {
		harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
		if err != nil {
			t.Fatalf("create deterministic harness: %v", err)
		}
		owner := taskorchestration.NewUserAuthority(
			authorityID(t, "cancel-ready-owner"), taskorchestration.AuthorizationGeneration(1),
		)
		pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
			{
				Key: phaseKey(t, "cancel-ready-phase"), Kind: taskorchestration.PhaseMutating,
				ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
				RequiredRuntimeRuns: 1,
			},
		})
		if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
			intentHeader(t, "cancel-ready-start", "cancel-ready-task", now), owner, pinned,
		)); err != nil {
			t.Fatalf("start pinned Task: %v", err)
		}
		cancelHeader := intentHeader(t, "cancel-ready", "cancel-ready-task", now.Add(time.Second))
		cancelHeader.ExpectedTaskRevision = 1
		decision, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
		))
		if err != nil {
			t.Fatalf("cancel Task before active mutation: %v", err)
		}
		if decision.TaskProjection.Status != taskorchestration.TaskCancelled ||
			decision.TaskProjection.CurrentPhase != phaseKey(t, "cancel-ready-phase") {
			t.Fatal("cancel before active mutation advanced the pinned cursor")
		}
	})

	t.Run("while a non-mutating Phase Run is active", func(t *testing.T) {
		harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
		if err != nil {
			t.Fatalf("create deterministic harness: %v", err)
		}
		owner := taskorchestration.NewUserAuthority(
			authorityID(t, "cancel-analysis-owner"), taskorchestration.AuthorizationGeneration(1),
		)
		pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
			{
				Key: phaseKey(t, "cancel-analysis-phase"), Kind: taskorchestration.PhaseNonMutating,
				ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
				RequiredRuntimeRuns: 1,
			},
		})
		if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
			intentHeader(t, "cancel-analysis-start", "cancel-analysis-task", now), owner, pinned,
		)); err != nil {
			t.Fatalf("start pinned Task: %v", err)
		}
		workHeader := intentHeader(t, "cancel-analysis-work", "cancel-analysis-task", now.Add(time.Second))
		workHeader.ExpectedTaskRevision = 1
		work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
			workHeader,
			taskorchestration.NewWorkerAuthority(
				authorityID(t, "cancel-analysis-worker"), taskorchestration.AuthorizationGeneration(1),
			),
			operationID(t, "cancel-analysis-work-availability"),
		))
		if err != nil {
			t.Fatalf("make non-mutating work available: %v", err)
		}
		before := queryAggregate(t, harness, "cancel-analysis-task", owner)
		cancelHeader := intentHeader(t, "cancel-analysis", "cancel-analysis-task", now.Add(2*time.Second))
		cancelHeader.ExpectedTaskRevision = 2
		decision, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
		))
		if err != nil {
			t.Fatalf("cancel active non-mutating Phase: %v", err)
		}
		if decision.TaskProjection.Status != taskorchestration.TaskCancelled ||
			decision.TaskProjection.CancellationState != taskorchestration.CancellationCancelled ||
			decision.TaskProjection.ActivePhaseRunID != (taskorchestration.PhaseRunID{}) ||
			len(decision.EnactmentRefs) != 1 ||
			decision.EnactmentRefs[0].Kind != taskorchestration.EnactmentRuntimeExecution ||
			decision.EnactmentRefs[0].OperationID == work.EnactmentRefs[0].OperationID {
			t.Fatal("active non-mutating cancellation omitted Runtime cancellation or emitted C04 work")
		}
		after := queryAggregate(t, harness, "cancel-analysis-task", owner)
		if after.PhaseRuns[0].PhaseRunID != before.ActivePhaseRunID ||
			after.PhaseRuns[0].Outcome != taskorchestration.PhaseRunCancelled {
			t.Fatal("active non-mutating cancellation lost or advanced the Phase Run")
		}
		cancelFence, ok := decision.EnactmentRefs[0].Fence.(taskorchestration.RuntimeFence)
		if !ok {
			t.Fatal("Runtime cancellation enactment omitted its typed fence")
		}
		evidenceHeader := intentHeader(
			t, "cancel-analysis-runtime-terminal", "cancel-analysis-task", now.Add(3*time.Second),
		)
		evidenceHeader.ExpectedTaskRevision = 3
		evidenceHeader.ActivityGeneration = 2
		accepted, err := harness.Mutations.Decide(
			context.Background(),
			taskorchestration.NewAcceptRuntimeEvidenceIntent(
				evidenceHeader,
				pinned.Authorities.Runtime,
				taskorchestration.RuntimeEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, "cancel-analysis-runtime-terminal-evidence"),
						taskorchestration.EvidenceRuntime,
						evidenceDigest(t, "edededededededededededededededededededededededededededededededed"),
					),
					PhaseRunID:         before.PhaseRuns[0].PhaseRunID,
					PhaseRunGeneration: before.PhaseRuns[0].Generation,
					PhaseRunFence:      before.PhaseRuns[0].Fence,
					RuntimeRunID:       before.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID,
					OperationID:        decision.EnactmentRefs[0].OperationID,
					Generation:         taskorchestration.RuntimeGeneration(before.PhaseRuns[0].Generation),
					Fence:              cancelFence,
					SafetyEpoch:        1,
					Outcome:            taskorchestration.RuntimeRunFailed,
				},
			),
		)
		if err != nil {
			t.Fatalf("accept terminal Runtime cancellation evidence: %v", err)
		}
		if accepted.TaskProjection.Status != taskorchestration.TaskCancelled ||
			len(accepted.AcceptedEvidenceRefs) != 1 {
			t.Fatal("Runtime termination evidence revived or changed the cancelled Task")
		}
	})

	t.Run("while a mutation-bearing Phase Run is active", func(t *testing.T) {
		harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
		if err != nil {
			t.Fatalf("create deterministic harness: %v", err)
		}
		owner := taskorchestration.NewUserAuthority(
			authorityID(t, "cancel-active-owner"), taskorchestration.AuthorizationGeneration(1),
		)
		pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{
			{
				Key: phaseKey(t, "cancel-active-phase"), Kind: taskorchestration.PhaseMutating,
				ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
				RequiredRuntimeRuns: 1,
			},
		})
		if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
			intentHeader(t, "cancel-active-start", "cancel-active-task", now), owner, pinned,
		)); err != nil {
			t.Fatalf("start pinned Task: %v", err)
		}
		workHeader := intentHeader(t, "cancel-active-work", "cancel-active-task", now.Add(time.Second))
		workHeader.ExpectedTaskRevision = 1
		activeWork, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
			workHeader,
			taskorchestration.NewWorkerAuthority(
				authorityID(t, "cancel-active-worker"), taskorchestration.AuthorizationGeneration(1),
			),
			operationID(t, "cancel-active-work-availability"),
		))
		if err != nil {
			t.Fatalf("make mutation work available: %v", err)
		}
		before := queryAggregate(t, harness, "cancel-active-task", owner)
		cancelHeader := intentHeader(t, "cancel-active", "cancel-active-task", now.Add(2*time.Second))
		cancelHeader.ExpectedTaskRevision = 2
		cancelBinding := exactC04FenceRequestBinding(
			t, "cancel-active-task", before.PhaseRuns[0].PhaseRunID, pinned.TaskWorkspaceID,
			"cancel-active-c04-fence-operation",
			taskorchestration.TaskWorkspaceLifecycleGeneration(before.PhaseRuns[0].Generation),
			taskorchestration.TaskWorkspaceLifecycleFence(before.PhaseRuns[0].Fence),
			before.LatestRevisionID,
		)
		decision, err := harness.Mutations.Decide(
			context.Background(), taskorchestration.NewCancelTaskByUserWithLifecycleFenceIntent(
				cancelHeader, owner, taskorchestration.CancelReasonUserRequested, cancelBinding,
			),
		)
		if err != nil {
			t.Fatalf("cancel active mutation: %v", err)
		}
		var runtimeCancellation, lifecycleFence taskorchestration.EnactmentRef
		for _, ref := range decision.EnactmentRefs {
			switch ref.Kind {
			case taskorchestration.EnactmentRuntimeExecution:
				runtimeCancellation = ref
			case taskorchestration.EnactmentTaskWorkspaceLifecycle:
				lifecycleFence = ref
			}
		}
		if decision.TaskProjection.Status != taskorchestration.TaskCancelling ||
			decision.TaskProjection.CurrentPhase != before.CurrentPhase ||
			decision.TaskProjection.ActivePhaseRunID != before.ActivePhaseRunID ||
			decision.TaskProjection.CancellationState != taskorchestration.CancellationCancelling ||
			len(decision.EnactmentRefs) != 2 ||
			runtimeCancellation.OperationID == (taskorchestration.OperationID{}) ||
			lifecycleFence.OperationID == (taskorchestration.OperationID{}) ||
			runtimeCancellation.OperationID == activeWork.EnactmentRefs[0].OperationID {
			t.Fatal("pre-C04 cancellation did not fence Runtime and C04 authority")
		}
		after := queryAggregate(t, harness, "cancel-active-task", owner)
		if after.ExecutionLockID != pinned.ExecutionLock.ID || after.TemplateLockID != pinned.TemplateLockID {
			t.Fatal("cancellation repinned immutable locks")
		}

		blockedHeader := intentHeader(t, "cancel-active-more-work", "cancel-active-task", now.Add(3*time.Second))
		blockedHeader.ExpectedTaskRevision = 3
		_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
			blockedHeader,
			taskorchestration.NewWorkerAuthority(
				authorityID(t, "cancel-active-worker"), taskorchestration.AuthorizationGeneration(1),
			),
			operationID(t, "cancel-active-more-work-availability"),
		))
		var decisionError *taskorchestration.Error
		if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorTerminalConflict {
			t.Fatalf("work during cancelling error = %T, want terminal conflict", err)
		}

		lateRuntimeHeader := intentHeader(t, "cancel-active-late-runtime", "cancel-active-task", now.Add(4*time.Second))
		lateRuntimeHeader.ExpectedTaskRevision = 3
		_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
			lateRuntimeHeader,
			pinned.Authorities.Runtime,
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "cancel-active-late-runtime-evidence"), taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				),
				PhaseRunID:         before.PhaseRuns[0].PhaseRunID,
				PhaseRunGeneration: before.PhaseRuns[0].Generation, PhaseRunFence: before.PhaseRuns[0].Fence,
				RuntimeRunID: before.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID,
				OperationID:  activeWork.EnactmentRefs[0].OperationID,
				Generation:   1, Fence: taskorchestration.RuntimeFence(before.PhaseRuns[0].Fence), SafetyEpoch: 1,
				Outcome: taskorchestration.RuntimeRunSucceeded,
			},
		))
		if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
			t.Fatalf("evidence during cancelling error = %T, want stale authority", err)
		}
		afterLateEvidence := queryAggregate(t, harness, "cancel-active-task", owner)
		if afterLateEvidence.TaskRevision != 3 || afterLateEvidence.EvidenceDiagnosticCount != 1 ||
			afterLateEvidence.LatestEvidenceDiagnostic.EvidenceID !=
				evidenceID(t, "cancel-active-late-runtime-evidence") ||
			afterLateEvidence.LatestEvidenceDiagnostic.Reason !=
				taskorchestration.EvidenceDiagnosticStale {
			t.Fatal("late Runtime evidence changed the cancelling Task or was not retained diagnostically")
		}

		cancelFence, ok := runtimeCancellation.Fence.(taskorchestration.RuntimeFence)
		if !ok {
			t.Fatal("Runtime cancellation enactment omitted its typed fence")
		}
		cancelEvidenceHeader := intentHeader(
			t, "cancel-active-runtime-fenced", "cancel-active-task", now.Add(5*time.Second),
		)
		cancelEvidenceHeader.ExpectedTaskRevision = 3
		cancelEvidenceHeader.ActivityGeneration = 2
		cancelEvidence, err := harness.Mutations.Decide(
			context.Background(),
			taskorchestration.NewAcceptRuntimeEvidenceIntent(
				cancelEvidenceHeader,
				pinned.Authorities.Runtime,
				taskorchestration.RuntimeEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, "cancel-active-runtime-fenced-evidence"),
						taskorchestration.EvidenceRuntime,
						evidenceDigest(t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
					),
					PhaseRunID:         before.PhaseRuns[0].PhaseRunID,
					PhaseRunGeneration: before.PhaseRuns[0].Generation,
					PhaseRunFence:      before.PhaseRuns[0].Fence,
					RuntimeRunID:       before.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID,
					OperationID:        runtimeCancellation.OperationID,
					Generation:         1,
					Fence:              cancelFence,
					SafetyEpoch:        1,
					Outcome:            taskorchestration.RuntimeRunFailed,
				},
			),
		)
		if err != nil {
			t.Fatalf("accept Runtime cancellation evidence: %v", err)
		}
		if cancelEvidence.TaskProjection.Status != taskorchestration.TaskCancelling ||
			len(cancelEvidence.AcceptedEvidenceRefs) != 1 {
			t.Fatal("Runtime cancellation evidence bypassed the C04 fence")
		}

		lifecycleFenceValue, ok := lifecycleFence.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
		if !ok {
			t.Fatal("C04 cancellation enactment omitted its typed fence")
		}
		lifecycleEvidenceHeader := intentHeader(
			t, "cancel-active-c04-fenced", "cancel-active-task", now.Add(6*time.Second),
		)
		lifecycleEvidenceHeader.ExpectedTaskRevision = 4
		lifecycleEvidenceHeader.ActivityGeneration = 2
		terminal, err := harness.Mutations.Decide(
			context.Background(),
			taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
				lifecycleEvidenceHeader,
				pinned.Authorities.TaskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, "cancel-active-c04-fenced-evidence"),
						taskorchestration.EvidenceTaskWorkspaceLifecycle,
						evidenceDigest(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
					),
					PhaseRunID:         before.PhaseRuns[0].PhaseRunID,
					PhaseRunGeneration: before.PhaseRuns[0].Generation,
					PhaseRunFence:      before.PhaseRuns[0].Fence,
					OperationID:        lifecycleFence.OperationID,
					Generation: taskorchestration.TaskWorkspaceLifecycleGeneration(
						before.PhaseRuns[0].Generation,
					),
					Fence: lifecycleFenceValue,
					ObservedGeneration: taskorchestration.TaskWorkspaceLifecycleGeneration(
						before.PhaseRuns[0].Generation,
					),
					ObservedFence: lifecycleFenceValue + 1,
					SafetyEpoch:   1,
					Outcome:       taskorchestration.LifecycleEvidenceFenced,
				},
			),
		)
		if err != nil {
			t.Fatalf("accept C04 cancellation fence: %v", err)
		}
		if terminal.TaskProjection.Status != taskorchestration.TaskCancelled ||
			terminal.TaskProjection.CancellationState != taskorchestration.CancellationCancelled ||
			terminal.TaskProjection.ActivePhaseRunID != (taskorchestration.PhaseRunID{}) {
			t.Fatal("C04 fence evidence did not terminally cancel the aggregate")
		}
	})
}

func TestRecoveryFenceReissuesCancellationOperationsWithoutANewBusinessAttempt(t *testing.T) {
	now := time.Date(2026, time.July, 26, 18, 45, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "cancel-recovery-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "cancel-recovery-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	recoveryAuthority := taskorchestration.NewRecoveryAuthority(
		authorityID(t, "cancel-recovery-authority"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Recovery: taskorchestration.HarnessRecoveryFixture{
			Authority: recoveryAuthority, Generation: 1, Fence: 1, SafetyEpoch: 1,
			Mode: taskorchestration.OperationalFullReady,
		},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "cancel-recovery-phase"), Kind: taskorchestration.PhaseMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "cancel-recovery-start", "cancel-recovery-task", now), owner, pinned,
	)); err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	workHeader := intentHeader(t, "cancel-recovery-work", "cancel-recovery-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = 1
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "cancel-recovery-availability"),
	)); err != nil {
		t.Fatalf("make mutation work available: %v", err)
	}
	firstView := queryAggregate(t, harness, "cancel-recovery-task", owner)
	run := firstView.PhaseRuns[0]
	cancelHeader := intentHeader(t, "cancel-recovery-cancel", "cancel-recovery-task", now.Add(2*time.Second))
	cancelHeader.ExpectedTaskRevision = 2
	initialFenceBinding := exactC04FenceRequestBinding(
		t, "cancel-recovery-task", run.PhaseRunID, pinned.TaskWorkspaceID,
		"cancel-recovery-initial-c04-fence", taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
		taskorchestration.TaskWorkspaceLifecycleFence(run.Fence), firstView.LatestRevisionID,
	)
	cancelled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserWithLifecycleFenceIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested, initialFenceBinding,
		),
	)
	if err != nil {
		t.Fatalf("cancel active mutation: %v", err)
	}
	secondOwner := taskorchestration.NewUserAuthority(
		authorityID(t, "cancel-recovery-second-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	secondWorker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "cancel-recovery-second-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	secondPinned := pinned
	secondPinned.TaskWorkspaceID = taskWorkspaceID(t, "cancel-recovery-second-workspace")
	secondPinned.ExecutionLock.ID = executionLockID(t, "cancel-recovery-second-execution-lock")
	secondPinned.TemplateLockID = templateLockID(t, "cancel-recovery-second-template-lock")
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, "cancel-recovery-second-start", "cancel-recovery-second-task", now),
		secondOwner, secondPinned,
	)); err != nil {
		t.Fatalf("start second pinned Task: %v", err)
	}
	secondWorkHeader := intentHeader(
		t, "cancel-recovery-second-work", "cancel-recovery-second-task", now.Add(time.Second),
	)
	secondWorkHeader.ExpectedTaskRevision = 1
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		secondWorkHeader, secondWorker, operationID(t, "cancel-recovery-second-availability"),
	)); err != nil {
		t.Fatalf("make second mutation work available: %v", err)
	}
	secondView := queryAggregate(t, harness, "cancel-recovery-second-task", secondOwner)
	secondRun := secondView.PhaseRuns[0]
	secondCancelHeader := intentHeader(
		t, "cancel-recovery-second-cancel", "cancel-recovery-second-task", now.Add(2*time.Second),
	)
	secondCancelHeader.ExpectedTaskRevision = 2
	secondInitialFenceBinding := exactC04FenceRequestBinding(
		t, "cancel-recovery-second-task", secondRun.PhaseRunID, secondPinned.TaskWorkspaceID,
		"cancel-recovery-second-initial-c04-fence",
		taskorchestration.TaskWorkspaceLifecycleGeneration(secondRun.Generation),
		taskorchestration.TaskWorkspaceLifecycleFence(secondRun.Fence), secondView.LatestRevisionID,
	)
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserWithLifecycleFenceIntent(
			secondCancelHeader, secondOwner, taskorchestration.CancelReasonUserRequested,
			secondInitialFenceBinding,
		),
	); err != nil {
		t.Fatalf("cancel second active mutation: %v", err)
	}

	recoveryHeader := intentHeader(
		t, "cancel-recovery-full-ready", "cancel-recovery-task", now.Add(3*time.Second),
	)
	recoveryHeader.ExpectedTaskRevision = 3
	recoveryHeader.ActivityGeneration = 2
	recoveryFenceBinding := exactC04FenceRequestBinding(
		t, "cancel-recovery-task", run.PhaseRunID, pinned.TaskWorkspaceID,
		"cancel-recovery-reissued-c04-fence", taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
		taskorchestration.TaskWorkspaceLifecycleFence(run.Fence), firstView.LatestRevisionID,
	)
	recovered, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewApplyOperationalFenceIntent(
			recoveryHeader,
			recoveryAuthority,
			taskorchestration.OperationalFenceBinding{
				Generation: 2, Fence: 2, SafetyEpoch: 2,
				Mode: taskorchestration.OperationalFullReady, LifecycleFence: recoveryFenceBinding,
			},
		),
	)
	if err != nil {
		t.Fatalf("apply post-recovery fence while cancelling: %v", err)
	}
	secondRecoveryHeader := intentHeader(
		t, "cancel-recovery-second-full-ready", "cancel-recovery-second-task", now.Add(3*time.Second),
	)
	secondRecoveryHeader.ExpectedTaskRevision = 3
	secondRecoveryHeader.ActivityGeneration = 3
	secondRecoveryFenceBinding := exactC04FenceRequestBinding(
		t, "cancel-recovery-second-task", secondRun.PhaseRunID, secondPinned.TaskWorkspaceID,
		"cancel-recovery-second-reissued-c04-fence",
		taskorchestration.TaskWorkspaceLifecycleGeneration(secondRun.Generation),
		taskorchestration.TaskWorkspaceLifecycleFence(secondRun.Fence), secondView.LatestRevisionID,
	)
	secondRecovered, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewApplyOperationalFenceIntent(
			secondRecoveryHeader,
			recoveryAuthority,
			taskorchestration.OperationalFenceBinding{
				Generation: 2, Fence: 2, SafetyEpoch: 2,
				Mode: taskorchestration.OperationalFullReady, LifecycleFence: secondRecoveryFenceBinding,
			},
		),
	)
	if err != nil {
		t.Fatalf("catch second cancelling Task up to the global recovery fence: %v", err)
	}
	var secondLifecycleFence taskorchestration.EnactmentRef
	for _, ref := range secondRecovered.EnactmentRefs {
		if ref.Kind == taskorchestration.EnactmentTaskWorkspaceLifecycle {
			secondLifecycleFence = ref
		}
	}
	if secondRecovered.TaskProjection.ActivityGeneration != 3 ||
		secondRecovered.TaskProjection.SafetyEpoch != 2 ||
		len(secondRecovered.EnactmentRefs) != 2 ||
		secondLifecycleFence.OperationID == (taskorchestration.OperationID{}) {
		t.Fatal("second cancelling Task did not catch up at the existing global recovery epoch")
	}
	var oldLifecycleOperationID taskorchestration.OperationID
	for _, ref := range cancelled.EnactmentRefs {
		if ref.Kind == taskorchestration.EnactmentTaskWorkspaceLifecycle {
			oldLifecycleOperationID = ref.OperationID
		}
	}
	var runtimeCancellation, lifecycleFence taskorchestration.EnactmentRef
	for _, ref := range recovered.EnactmentRefs {
		switch ref.Kind {
		case taskorchestration.EnactmentRuntimeExecution:
			runtimeCancellation = ref
		case taskorchestration.EnactmentTaskWorkspaceLifecycle:
			lifecycleFence = ref
		}
	}
	if recovered.TaskProjection.Status != taskorchestration.TaskCancelling ||
		recovered.TaskProjection.ActivityGeneration != 3 ||
		recovered.TaskProjection.SafetyEpoch != 2 ||
		len(recovered.EnactmentRefs) != 2 ||
		runtimeCancellation.OperationID == (taskorchestration.OperationID{}) ||
		lifecycleFence.OperationID == (taskorchestration.OperationID{}) ||
		lifecycleFence.OperationID == oldLifecycleOperationID {
		t.Fatal("post-recovery fence did not issue fresh cancellation authority")
	}
	view := queryAggregate(t, harness, "cancel-recovery-task", owner)
	if view.TaskRevision != 4 || view.DecisionCount != 4 || view.EnactmentCount != 5 ||
		len(view.PhaseRuns) != 1 || len(view.PhaseRuns[0].RuntimeRuns) != 1 {
		t.Fatal("post-recovery cancellation fencing created a business attempt")
	}

	lifecycleFenceValue, ok := lifecycleFence.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
	if !ok {
		t.Fatal("post-recovery C04 cancellation omitted its typed fence")
	}
	lifecycleHeader := intentHeader(
		t, "cancel-recovery-c04", "cancel-recovery-task", now.Add(4*time.Second),
	)
	lifecycleHeader.ExpectedTaskRevision = 4
	lifecycleHeader.ActivityGeneration = 3
	terminal, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			lifecycleHeader,
			pinned.Authorities.TaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "cancel-recovery-c04-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "5656565656565656565656565656565656565656565656565656565656565656"),
				),
				PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
				PhaseRunFence: run.Fence, OperationID: lifecycleFence.OperationID,
				Generation:         taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
				Fence:              lifecycleFenceValue,
				ObservedGeneration: taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation),
				ObservedFence:      lifecycleFenceValue + 1, SafetyEpoch: 2,
				Outcome: taskorchestration.LifecycleEvidenceFenced,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept post-recovery C04 cancellation evidence: %v", err)
	}
	if terminal.TaskProjection.Status != taskorchestration.TaskCancelled ||
		terminal.TaskProjection.CancellationState != taskorchestration.CancellationCancelled {
		t.Fatal("post-recovery C04 evidence did not terminally cancel the Task")
	}

	runtimeFence, ok := runtimeCancellation.Fence.(taskorchestration.RuntimeFence)
	if !ok {
		t.Fatal("post-recovery Runtime cancellation omitted its typed fence")
	}
	runtimeHeader := intentHeader(
		t, "cancel-recovery-runtime", "cancel-recovery-task", now.Add(5*time.Second),
	)
	runtimeHeader.ExpectedTaskRevision = 5
	runtimeHeader.ActivityGeneration = 3
	accepted, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(
			runtimeHeader,
			pinned.Authorities.Runtime,
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "cancel-recovery-runtime-evidence"), taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "3434343434343434343434343434343434343434343434343434343434343434"),
				),
				PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
				PhaseRunFence: run.Fence, RuntimeRunID: run.RuntimeRuns[0].RuntimeRunID,
				OperationID: runtimeCancellation.OperationID,
				Generation:  taskorchestration.RuntimeGeneration(run.Generation),
				Fence:       runtimeFence,
				SafetyEpoch: 2,
				Outcome:     taskorchestration.RuntimeRunFailed,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept post-recovery Runtime cancellation evidence: %v", err)
	}
	if accepted.TaskProjection.Status != taskorchestration.TaskCancelled ||
		len(accepted.AcceptedEvidenceRefs) != 1 {
		t.Fatal("post-recovery Runtime evidence revived the cancelled Task")
	}

	secondLifecycleFenceValue, ok := secondLifecycleFence.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
	if !ok {
		t.Fatal("second post-recovery C04 cancellation omitted its typed fence")
	}
	secondLifecycleHeader := intentHeader(
		t, "cancel-recovery-second-c04", "cancel-recovery-second-task", now.Add(6*time.Second),
	)
	secondLifecycleHeader.ExpectedTaskRevision = 4
	secondLifecycleHeader.ActivityGeneration = 3
	secondTerminal, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			secondLifecycleHeader,
			secondPinned.Authorities.TaskWorkspaceLifecycle,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "cancel-recovery-second-c04-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "7878787878787878787878787878787878787878787878787878787878787878"),
				),
				PhaseRunID: secondRun.PhaseRunID, PhaseRunGeneration: secondRun.Generation,
				PhaseRunFence: secondRun.Fence, OperationID: secondLifecycleFence.OperationID,
				Generation:         taskorchestration.TaskWorkspaceLifecycleGeneration(secondRun.Generation),
				Fence:              secondLifecycleFenceValue,
				ObservedGeneration: taskorchestration.TaskWorkspaceLifecycleGeneration(secondRun.Generation),
				ObservedFence:      secondLifecycleFenceValue + 1, SafetyEpoch: 2,
				Outcome: taskorchestration.LifecycleEvidenceFenced,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept second post-recovery C04 cancellation evidence: %v", err)
	}
	if secondTerminal.TaskProjection.Status != taskorchestration.TaskCancelled {
		t.Fatal("second cancelling Task did not terminalize at the shared recovery epoch")
	}
}

func TestThreePinnedRoutesCompleteFromStartToPublication(t *testing.T) {
	tests := []struct {
		route         taskorchestration.Route
		phases        []taskorchestration.PhaseDefinition
		runtimeCounts []int
	}{
		{
			route:         taskorchestration.RouteGeneration,
			phases:        routeScenarioPhases(t, "generation", 2),
			runtimeCounts: []int{2, 0, 1, 0},
		},
		{
			route:         taskorchestration.RouteBeautify,
			phases:        routeScenarioPhases(t, "beautify", 1),
			runtimeCounts: []int{1, 0, 1, 0},
		},
		{
			route:         taskorchestration.RouteTemplateFill,
			phases:        routeScenarioPhases(t, "template-fill", 0),
			runtimeCounts: []int{0, 0, 1, 0},
		},
	}
	for _, test := range tests {
		t.Run(test.route.String(), func(t *testing.T) {
			view, pinned := drivePinnedRouteToPublication(t, test.route, test.phases)
			if view.Status != taskorchestration.TaskCompleted || len(view.PhaseRuns) != len(test.phases) {
				t.Fatal("Route did not complete its pinned start-to-publication graph")
			}
			for index, phaseRun := range view.PhaseRuns {
				if phaseRun.PhaseKey != test.phases[index].Key ||
					phaseRun.Outcome != taskorchestration.PhaseRunSucceeded ||
					len(phaseRun.RuntimeRuns) != test.runtimeCounts[index] {
					t.Fatalf("Phase Run %d did not follow its pinned definition", index)
				}
			}
			if view.ExecutionLockID != pinned.ExecutionLock.ID {
				t.Fatal("Route progression changed its Execution Lock")
			}
			if test.route == taskorchestration.RouteGeneration {
				if view.TemplateLockID != pinned.TemplateLockID {
					t.Fatal("Generation progression changed its Template Lock")
				}
			} else if view.TemplateLockID != (taskorchestration.TemplateLockID{}) {
				t.Fatal("non-Generation Route fabricated a Catalog Template Lock")
			}
		})
	}
}

func routeScenarioPhases(
	t *testing.T,
	prefix string,
	firstRuntimeRuns uint32,
) []taskorchestration.PhaseDefinition {
	t.Helper()
	analyze := phaseKey(t, prefix+"-analyze")
	confirm := phaseKey(t, prefix+"-confirm")
	realize := phaseKey(t, prefix+"-realize")
	publish := phaseKey(t, prefix+"-publish")
	return []taskorchestration.PhaseDefinition{
		{
			Key: analyze, Kind: taskorchestration.PhaseNonMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: firstRuntimeRuns, RetryEligible: true, NextPhase: confirm,
		},
		{
			Key: confirm, Kind: taskorchestration.PhaseConfirmationGate,
			GateID: gateID(t, prefix+"-gate"), NextPhase: realize,
		},
		{
			Key: realize, Kind: taskorchestration.PhaseMutating,
			ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
			RequiredRuntimeRuns: 1, RetryEligible: true, NextPhase: publish,
		},
		{Key: publish, Kind: taskorchestration.PhasePublication},
	}
}

func drivePinnedRouteToPublication(
	t *testing.T,
	route taskorchestration.Route,
	phases []taskorchestration.PhaseDefinition,
) (taskorchestration.TaskOrchestrationView, taskorchestration.PinnedTaskStart) {
	t.Helper()
	now := time.Date(2026, time.July, 26, 19, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	prefix := route.String()
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, prefix+"-scenario-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := routePinnedPipeline(t, route, phases)
	taskName := prefix + "-scenario-task"
	if _, err := harness.Mutations.Decide(context.Background(), verifiedPinnedStartIntent(t,
		intentHeader(t, prefix+"-scenario-start", taskName, now), owner, pinned,
	)); err != nil {
		t.Fatalf("start %s Route: %v", prefix, err)
	}
	step := 0
	for phaseIndex, phase := range phases {
		step++
		view := queryAggregate(t, harness, taskName, owner)
		workHeader := intentHeader(t, fmt.Sprintf("%s-work-%d", prefix, phaseIndex), taskName, now.Add(time.Duration(step)*time.Second))
		workHeader.ExpectedTaskRevision = view.TaskRevision
		work, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
			workHeader,
			taskorchestration.NewWorkerAuthority(
				authorityID(t, prefix+"-scenario-worker"), taskorchestration.AuthorizationGeneration(1),
			),
			operationID(t, fmt.Sprintf("%s-availability-%d", prefix, phaseIndex)),
		))
		if err != nil {
			t.Fatalf("begin %s Phase %d: %v", prefix, phaseIndex, err)
		}
		view = queryAggregate(t, harness, taskName, owner)
		run := view.PhaseRuns[len(view.PhaseRuns)-1]
		switch phase.Kind {
		case taskorchestration.PhaseConfirmationGate:
			step++
			header := intentHeader(t, fmt.Sprintf("%s-gate-%d", prefix, phaseIndex), taskName, now.Add(time.Duration(step)*time.Second))
			header.ExpectedTaskRevision = view.TaskRevision
			if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewSubmitConfirmationGateIntent(
				header, owner, phase.GateID,
				payloadDigest(t, "2222222222222222222222222222222222222222222222222222222222222222"),
			)); err != nil {
				t.Fatalf("complete %s Gate: %v", prefix, err)
			}
		case taskorchestration.PhasePublication:
			step++
			header := intentHeader(t, fmt.Sprintf("%s-publication-%d", prefix, phaseIndex), taskName, now.Add(time.Duration(step)*time.Second))
			header.ExpectedTaskRevision = view.TaskRevision
			if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPublicationEvidenceIntent(
				header,
				pinned.Authorities.Publication,
				taskorchestration.PublicationEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, fmt.Sprintf("%s-publication-evidence-%d", prefix, phaseIndex)),
						taskorchestration.EvidencePublication,
						evidenceDigest(t, "3333333333333333333333333333333333333333333333333333333333333333"),
					),
					PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
					PhaseRunFence: run.Fence, OperationID: work.EnactmentRefs[0].OperationID,
					Generation: 1, Fence: taskorchestration.PublicationFence(run.Fence), SafetyEpoch: 1,
					Outcome:           taskorchestration.PublicationActivated,
					ArtifactVersionID: artifactVersionID(t, prefix+"-artifact-v1"),
				},
			)); err != nil {
				t.Fatalf("complete %s publication: %v", prefix, err)
			}
		default:
			for runtimeIndex, runtimeRun := range run.RuntimeRuns {
				step++
				view = queryAggregate(t, harness, taskName, owner)
				header := intentHeader(t, fmt.Sprintf("%s-runtime-%d-%d", prefix, phaseIndex, runtimeIndex), taskName, now.Add(time.Duration(step)*time.Second))
				header.ExpectedTaskRevision = view.TaskRevision
				if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptRuntimeEvidenceIntent(
					header,
					pinned.Authorities.Runtime,
					taskorchestration.RuntimeEvidenceBinding{
						Evidence: taskorchestration.NewEvidenceRef(
							evidenceID(t, fmt.Sprintf("%s-runtime-evidence-%d-%d", prefix, phaseIndex, runtimeIndex)),
							taskorchestration.EvidenceRuntime,
							evidenceDigest(t, "4444444444444444444444444444444444444444444444444444444444444444"),
						),
						PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
						PhaseRunFence: run.Fence, RuntimeRunID: runtimeRun.RuntimeRunID,
						OperationID: work.EnactmentRefs[runtimeIndex].OperationID,
						Generation:  1, Fence: taskorchestration.RuntimeFence(run.Fence), SafetyEpoch: 1,
						Outcome: taskorchestration.RuntimeRunSucceeded,
					},
				)); err != nil {
					t.Fatalf("accept %s Runtime evidence: %v", prefix, err)
				}
			}
			step++
			view = queryAggregate(t, harness, taskName, owner)
			header := intentHeader(t, fmt.Sprintf("%s-validation-%d", prefix, phaseIndex), taskName, now.Add(time.Duration(step)*time.Second))
			header.ExpectedTaskRevision = view.TaskRevision
			lifecycleCommit := taskorchestration.TaskWorkspaceLifecycleCommitRequestBinding{}
			lifecycleGeneration := view.TaskWorkspaceLifecycleGeneration
			lifecycleFence := view.TaskWorkspaceLifecycleFence
			if phase.Kind == taskorchestration.PhaseMutating {
				if lifecycleGeneration == 0 {
					lifecycleGeneration = taskorchestration.TaskWorkspaceLifecycleGeneration(run.Generation)
				}
				if lifecycleFence == 0 {
					lifecycleFence = taskorchestration.TaskWorkspaceLifecycleFence(run.Fence)
				}
				lifecycleCommit = exactC04CommitRequestBinding(
					t, taskName, run.PhaseRunID, pinned.TaskWorkspaceID,
					fmt.Sprintf("%s-lifecycle-operation-%d", prefix, phaseIndex),
					lifecycleGeneration, lifecycleFence, view.LatestRevisionID,
				)
			}
			validation, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
				header,
				pinned.Authorities.Validator,
				taskorchestration.ValidationEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, fmt.Sprintf("%s-validation-evidence-%d", prefix, phaseIndex)),
						taskorchestration.EvidencePhaseValidation,
						evidenceDigest(t, "5555555555555555555555555555555555555555555555555555555555555555"),
					),
					PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
					PhaseRunFence: run.Fence, Generation: 1,
					Fence: taskorchestration.ValidationFence(run.Fence), SafetyEpoch: 1,
					Outcome: taskorchestration.PhaseValidationAccepted, LifecycleCommit: lifecycleCommit,
				},
			))
			if err != nil {
				t.Fatalf("validate %s Phase: %v", prefix, err)
			}
			if phase.Kind == taskorchestration.PhaseMutating {
				step++
				view = queryAggregate(t, harness, taskName, owner)
				header := intentHeader(t, fmt.Sprintf("%s-lifecycle-%d", prefix, phaseIndex), taskName, now.Add(time.Duration(step)*time.Second))
				header.ExpectedTaskRevision = view.TaskRevision
				if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
					header,
					pinned.Authorities.TaskWorkspaceLifecycle,
					taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
						Evidence: taskorchestration.NewEvidenceRef(
							evidenceID(t, fmt.Sprintf("%s-lifecycle-evidence-%d", prefix, phaseIndex)),
							taskorchestration.EvidenceTaskWorkspaceLifecycle,
							evidenceDigest(t, "6666666666666666666666666666666666666666666666666666666666666666"),
						),
						PhaseRunID: run.PhaseRunID, PhaseRunGeneration: run.Generation,
						PhaseRunFence: run.Fence, OperationID: validation.EnactmentRefs[0].OperationID,
						Generation: lifecycleGeneration, Fence: lifecycleFence,
						ObservedGeneration: lifecycleGeneration, ObservedFence: lifecycleFence + 1,
						SafetyEpoch:  1,
						Outcome:      taskorchestration.TaskWorkspaceLifecycleCommitted,
						RevisionID:   taskWorkspaceRevisionID(t, fmt.Sprintf("%s-revision-%d", prefix, phaseIndex)),
						CheckpointID: checkpointID(t, fmt.Sprintf("%s-checkpoint-%d", prefix, phaseIndex)),
					},
				)); err != nil {
					t.Fatalf("commit %s mutating Phase: %v", prefix, err)
				}
			}
		}
	}
	return queryAggregate(t, harness, taskName, owner), pinned
}

func routePinnedPipeline(
	t *testing.T,
	route taskorchestration.Route,
	phases []taskorchestration.PhaseDefinition,
) taskorchestration.PinnedTaskStart {
	t.Helper()
	prefix := route.String()
	pipelineID := pipelineVersionID(t, prefix+"-scenario-pipeline-v1")
	pinned := taskorchestration.PinnedTaskStart{
		Route:           route,
		TaskWorkspaceID: taskWorkspaceID(t, prefix+"-scenario-task-workspace"),
		Authorities:     downstreamAuthorityBindings(t, prefix+"-scenario"),
		ExecutionLock: taskorchestration.ExecutionLock{
			ID:                      executionLockID(t, prefix+"-scenario-execution-lock"),
			PipelineVersionID:       pipelineID,
			RuntimeReleaseID:        runtimeReleaseID(t, prefix+"-scenario-runtime-release"),
			CompatibilityApprovalID: compatibilityApprovalID(t, prefix+"-scenario-compatibility"),
			PipelineContract: taskorchestration.PipelineContract{
				SchemaVersion:     taskorchestration.PipelineContractV1,
				PipelineVersionID: pipelineID,
				Routes: []taskorchestration.RouteDefinition{
					{Route: route, EntryPhase: phases[0].Key, Phases: phases},
				},
			},
		},
	}
	if route == taskorchestration.RouteGeneration {
		pinned.TemplateLockID = templateLockID(t, prefix+"-scenario-template-lock")
	}
	return pinned
}

func queryAggregate(
	t *testing.T,
	harness *taskorchestration.DeterministicHarness,
	task string,
	owner taskorchestration.UserAuthority,
) taskorchestration.TaskOrchestrationView {
	t.Helper()
	view, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID: taskID(t, task), Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query aggregate: %v", err)
	}
	return view
}

func generationPinnedPipeline(
	t *testing.T,
	phases []taskorchestration.PhaseDefinition,
) taskorchestration.PinnedTaskStart {
	t.Helper()
	return taskorchestration.PinnedTaskStart{
		Route:           taskorchestration.RouteGeneration,
		TaskWorkspaceID: taskWorkspaceID(t, "task-workspace-generation"),
		Authorities:     downstreamAuthorityBindings(t, "generation"),
		ExecutionLock: taskorchestration.ExecutionLock{
			ID:                      executionLockID(t, "execution-lock-generation"),
			PipelineVersionID:       pipelineVersionID(t, "pipeline-generation-v1"),
			RuntimeReleaseID:        runtimeReleaseID(t, "runtime-release-v1"),
			CompatibilityApprovalID: compatibilityApprovalID(t, "compatibility-v1"),
			PipelineContract: taskorchestration.PipelineContract{
				SchemaVersion:     taskorchestration.PipelineContractV1,
				PipelineVersionID: pipelineVersionID(t, "pipeline-generation-v1"),
				Routes: []taskorchestration.RouteDefinition{
					{
						Route:      taskorchestration.RouteGeneration,
						EntryPhase: phases[0].Key,
						Phases:     phases,
					},
				},
			},
		},
		TemplateLockID: templateLockID(t, "template-lock-generation"),
	}
}

func downstreamAuthorityBindings(
	t *testing.T,
	prefix string,
) taskorchestration.DownstreamAuthorityBindings {
	t.Helper()
	return taskorchestration.DownstreamAuthorityBindings{
		Runtime: taskorchestration.NewRuntimeAuthority(
			authorityID(t, prefix+"-runtime-authority"), 1,
		),
		Validator: taskorchestration.NewValidatorAuthority(
			authorityID(t, prefix+"-validator-authority"), 1,
		),
		TaskWorkspaceLifecycle: taskorchestration.NewTaskWorkspaceLifecycleAuthority(
			authorityID(t, prefix+"-lifecycle-authority"), 1,
		),
		Publication: taskorchestration.NewPublicationAuthority(
			authorityID(t, prefix+"-publication-authority"), 1,
		),
		Scheduler: taskorchestration.NewSchedulerAuthority(
			authorityID(t, prefix+"-scheduler-authority"), 1,
		),
	}
}

// verifiedPinnedStartIntent keeps tests on the production admission path:
// Release and Catalog adapters verify exact Task-bound contracts before the
// opaque admission can enter the closed Start intent.
func verifiedPinnedStartIntent(
	t *testing.T,
	header taskorchestration.IntentHeader,
	authority taskorchestration.UserAuthority,
	candidate any,
) taskorchestration.TransitionIntent {
	return verifiedPinnedStartIntentAtSafetyEpoch(t, header, authority, candidate, 1)
}

func verifiedPinnedStartIntentAtSafetyEpoch(
	t *testing.T,
	header taskorchestration.IntentHeader,
	authority taskorchestration.UserAuthority,
	candidate any,
	safetyEpoch taskorchestration.SafetyEpoch,
) taskorchestration.TransitionIntent {
	t.Helper()
	if admission, ok := candidate.(taskorchestration.TaskStartAdmission); ok {
		return taskorchestration.NewStartPinnedTaskIntent(header, authority, admission)
	}
	pinned, ok := candidate.(taskorchestration.PinnedTaskStart)
	if !ok {
		return taskorchestration.NewStartPinnedTaskIntent(
			header, authority, taskorchestration.TaskStartAdmission{},
		)
	}
	executionRecord := taskorchestration.PublishedExecutionContract{
		SchemaVersion: taskorchestration.EvidenceSchemaV1,
		Producer: taskorchestration.EvidenceProducer{
			AuthorityID: authorityID(t, "test-start-release-authority"), Generation: 1,
		},
		ExecutionLock: pinned.ExecutionLock, SafetyEpoch: safetyEpoch,
	}
	executionRecord.ContractDigest = taskorchestration.ExecutionLockContractDigest(executionRecord.ExecutionLock)
	execution, err := taskorchestration.NewReleaseManagementAdapter(
		&releaseManagementPortDouble{record: executionRecord},
	).Resolve(context.Background(), taskorchestration.PinnedExecutionLockRequest{
		TaskID: header.TaskID, ExecutionLock: pinned.ExecutionLock,
		ContractDigest: executionRecord.ContractDigest, SafetyEpoch: safetyEpoch,
		Purpose: taskorchestration.PinnedLockStart,
	})
	if err != nil {
		return taskorchestration.NewStartPinnedTaskIntent(
			header, authority, taskorchestration.TaskStartAdmission{},
		)
	}
	var template *taskorchestration.ResolvedTemplateLockContract
	if pinned.Route == taskorchestration.RouteGeneration {
		record := validPublishedTemplateLockContract(
			t, pinned.TemplateLockID, pinned.ExecutionLock.ID,
			"test-start", taskorchestration.ProducerGeneration(1), safetyEpoch,
		)
		resolved, resolveErr := taskorchestration.NewCatalogPublicationAdapter(
			&catalogPublicationPortDouble{record: record},
		).Resolve(context.Background(), taskorchestration.PinnedTemplateLockRequest{
			TaskID: header.TaskID, TemplateLockID: pinned.TemplateLockID,
			LockDigest: record.LockDigest, ObservedGeneration: record.ObservedGeneration,
			SafetyEpoch: safetyEpoch, Purpose: taskorchestration.PinnedLockStart,
		})
		if resolveErr != nil {
			return taskorchestration.NewStartPinnedTaskIntent(
				header, authority, taskorchestration.TaskStartAdmission{},
			)
		}
		template = &resolved
	}
	admission, err := taskorchestration.NewTaskStartAdmission(
		header.TaskID, pinned.Route, pinned.TaskWorkspaceID, pinned.Authorities, execution, template,
	)
	if err != nil {
		return taskorchestration.NewStartPinnedTaskIntent(
			header, authority, taskorchestration.TaskStartAdmission{},
		)
	}
	return taskorchestration.NewStartPinnedTaskIntent(header, authority, admission)
}

func minimalPinnedStartIntent(
	t *testing.T,
	header taskorchestration.IntentHeader,
	authority taskorchestration.UserAuthority,
) taskorchestration.TransitionIntent {
	t.Helper()
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "minimal-start-publication"), Kind: taskorchestration.PhasePublication,
	}})
	return verifiedPinnedStartIntent(t, header, authority, pinned)
}

func minimalWorkIntent(
	t *testing.T,
	header taskorchestration.IntentHeader,
) taskorchestration.TransitionIntent {
	t.Helper()
	return taskorchestration.NewMakeWorkAvailableIntent(
		header,
		taskorchestration.NewWorkerAuthority(authorityID(t, "minimal-start-worker"), 1),
		operationID(t, header.DecisionRequestID.String()+"-work"),
	)
}

func phaseKey(t *testing.T, value string) taskorchestration.PhaseKey {
	t.Helper()
	id, err := taskorchestration.NewPhaseKey(value)
	if err != nil {
		t.Fatalf("create Phase key: %v", err)
	}
	return id
}

func executionLockID(t *testing.T, value string) taskorchestration.ExecutionLockID {
	t.Helper()
	id, err := taskorchestration.NewExecutionLockID(value)
	if err != nil {
		t.Fatalf("create Execution Lock identity: %v", err)
	}
	return id
}

func pipelineVersionID(t *testing.T, value string) taskorchestration.PipelineVersionID {
	t.Helper()
	id, err := taskorchestration.NewPipelineVersionID(value)
	if err != nil {
		t.Fatalf("create Pipeline Version identity: %v", err)
	}
	return id
}

func runtimeReleaseID(t *testing.T, value string) taskorchestration.RuntimeReleaseID {
	t.Helper()
	id, err := taskorchestration.NewRuntimeReleaseID(value)
	if err != nil {
		t.Fatalf("create Runtime Release identity: %v", err)
	}
	return id
}

func compatibilityApprovalID(t *testing.T, value string) taskorchestration.CompatibilityApprovalID {
	t.Helper()
	id, err := taskorchestration.NewCompatibilityApprovalID(value)
	if err != nil {
		t.Fatalf("create Compatibility Approval identity: %v", err)
	}
	return id
}

func templateLockID(t *testing.T, value string) taskorchestration.TemplateLockID {
	t.Helper()
	id, err := taskorchestration.NewTemplateLockID(value)
	if err != nil {
		t.Fatalf("create Template Lock identity: %v", err)
	}
	return id
}

func taskWorkspaceRevisionID(t *testing.T, value string) taskorchestration.TaskWorkspaceRevisionID {
	t.Helper()
	id, err := taskorchestration.NewTaskWorkspaceRevisionID(value)
	if err != nil {
		t.Fatalf("create Task Workspace Revision identity: %v", err)
	}
	return id
}

func checkpointID(t *testing.T, value string) taskorchestration.CheckpointID {
	t.Helper()
	id, err := taskorchestration.NewCheckpointID(value)
	if err != nil {
		t.Fatalf("create Checkpoint identity: %v", err)
	}
	return id
}

func taskWorkspaceID(t *testing.T, value string) taskorchestration.TaskWorkspaceID {
	t.Helper()
	id, err := taskorchestration.NewTaskWorkspaceID(value)
	if err != nil {
		t.Fatalf("create Task Workspace identity: %v", err)
	}
	return id
}
