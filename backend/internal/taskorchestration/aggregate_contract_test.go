package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestPinnedPipelineCreatesTheEntryPhaseFromItsContract(t *testing.T) {
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

	start, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "aggregate-start", "aggregate-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start pinned Task: %v", err)
	}
	if start.TaskProjection.Status != taskorchestration.TaskReady ||
		start.TaskProjection.CurrentPhase != phaseKey(t, "contract-entry") ||
		start.TaskProjection.TaskWorkspaceID != pinned.TaskWorkspaceID {
		t.Fatal("start decision did not project the pinned contract entry Phase")
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

func TestPinnedPipelineFailsClosedWithoutItsValidationContract(t *testing.T) {
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
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "validation-runtime-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "validation-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			PhaseRunID: phaseRunID, RuntimeRunID: runtimeRunID,
			OperationID: work.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: 1, Outcome: taskorchestration.RuntimeRunSucceeded,
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
			taskorchestration.NewValidatorAuthority(
				authorityID(t, "validation-authority"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "validation-accepted-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				),
				PhaseRunID: phaseRunID, Generation: 1, Fence: 1,
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "mutation-runtime-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "mutation-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			PhaseRunID: phaseRunID, RuntimeRunID: runtimeRunID,
			OperationID: work.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: 1, Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)); err != nil {
		t.Fatalf("accept Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "mutation-validation", "mutation-task", now.Add(3*time.Second))
	validationHeader.ExpectedTaskRevision = 3
	validation, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			validationHeader,
			taskorchestration.NewValidatorAuthority(
				authorityID(t, "mutation-validator"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "mutation-validation-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				),
				PhaseRunID: phaseRunID, Generation: 1, Fence: 1,
				Outcome: taskorchestration.PhaseValidationAccepted,
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
			taskorchestration.NewTaskWorkspaceLifecycleAuthority(
				authorityID(t, "mutation-c04-authority"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "mutation-c04-evidence"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
				),
				PhaseRunID:  phaseRunID,
				OperationID: validation.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: 1,
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
		view.PhaseRuns[0].CheckpointID != checkpointID(t, "checkpoint-realized-deck") {
		t.Fatal("mutating Phase history omitted its authoritative C04 result")
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
			taskorchestration.NewPublicationAuthority(
				authorityID(t, "publication-authority"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.PublicationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "publication-evidence"), taskorchestration.EvidencePublication,
					evidenceDigest(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
				),
				PhaseRunID:  view.PhaseRuns[0].PhaseRunID,
				OperationID: work.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: 1,
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "retry-runtime-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "retry-runtime-failed-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
			),
			PhaseRunID: failedRunID, RuntimeRunID: view.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID,
			OperationID: work.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: 1, Outcome: taskorchestration.RuntimeRunFailed,
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
			taskorchestration.NewValidatorAuthority(
				authorityID(t, "retry-validator"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.ValidationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "retry-validation-rejected-evidence"),
					taskorchestration.EvidencePhaseValidation,
					evidenceDigest(t, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
				),
				PhaseRunID: failedRunID, Generation: 1, Fence: 1,
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
			taskorchestration.NewPublicationAuthority(
				authorityID(t, "manual-publication-authority"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.PublicationEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "manual-initial-publication-evidence"),
					taskorchestration.EvidencePublication,
					evidenceDigest(t, "1111111111111111111111111111111111111111111111111111111111111111"),
				),
				PhaseRunID:  view.PhaseRuns[0].PhaseRunID,
				OperationID: work.EnactmentRefs[0].OperationID,
				Generation:  1, Fence: 1, Outcome: taskorchestration.PublicationActivated,
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
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "manual-runtime-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-runtime-evidence"), taskorchestration.EvidenceRuntime,
				evidenceDigest(t, "7777777777777777777777777777777777777777777777777777777777777777"),
			),
			PhaseRunID: manualRun.PhaseRunID, RuntimeRunID: manualRun.RuntimeRuns[0].RuntimeRunID,
			OperationID: manualWork.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: taskorchestration.RuntimeFence(manualRun.Fence),
			Outcome: taskorchestration.RuntimeRunSucceeded,
		},
	)); err != nil {
		t.Fatalf("accept manual Runtime evidence: %v", err)
	}
	validationHeader := intentHeader(t, "manual-validation", "manual-task", now.Add(8*time.Second))
	validationHeader.ExpectedTaskRevision = 6
	validationHeader.ActivityGeneration = 2
	validation, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
		validationHeader,
		taskorchestration.NewValidatorAuthority(
			authorityID(t, "manual-validator"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.ValidationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-validation-evidence"), taskorchestration.EvidencePhaseValidation,
				evidenceDigest(t, "8888888888888888888888888888888888888888888888888888888888888888"),
			),
			PhaseRunID: manualRun.PhaseRunID, Generation: 1,
			Fence:   taskorchestration.ValidationFence(manualRun.Fence),
			Outcome: taskorchestration.PhaseValidationAccepted,
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
		taskorchestration.NewTaskWorkspaceLifecycleAuthority(
			authorityID(t, "manual-c04-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-lifecycle-evidence"),
				taskorchestration.EvidenceTaskWorkspaceLifecycle,
				evidenceDigest(t, "9999999999999999999999999999999999999999999999999999999999999999"),
			),
			PhaseRunID: manualRun.PhaseRunID, OperationID: validation.EnactmentRefs[0].OperationID,
			Generation: 1, Fence: taskorchestration.TaskWorkspaceLifecycleFence(manualRun.Fence),
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
		taskorchestration.NewPublicationAuthority(
			authorityID(t, "manual-publication-authority"), taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.PublicationEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "manual-child-publication-evidence"), taskorchestration.EvidencePublication,
				evidenceDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			),
			PhaseRunID:  manualPublicationRun.PhaseRunID,
			OperationID: manualPublication.EnactmentRefs[0].OperationID,
			Generation:  1, Fence: taskorchestration.PublicationFence(manualPublicationRun.Fence),
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
		if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
		if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
		decision, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
		))
		if err != nil {
			t.Fatalf("cancel active mutation: %v", err)
		}
		if decision.TaskProjection.Status != taskorchestration.TaskCancelling ||
			decision.TaskProjection.CurrentPhase != before.CurrentPhase ||
			decision.TaskProjection.ActivePhaseRunID != before.ActivePhaseRunID {
			t.Fatal("active mutation cancellation advanced or erased authoritative work")
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
			taskorchestration.NewRuntimeAuthority(
				authorityID(t, "cancel-active-runtime"), taskorchestration.AuthorizationGeneration(1),
			),
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "cancel-active-late-runtime-evidence"), taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				),
				PhaseRunID:   before.PhaseRuns[0].PhaseRunID,
				RuntimeRunID: before.PhaseRuns[0].RuntimeRuns[0].RuntimeRunID,
				OperationID:  activeWork.EnactmentRefs[0].OperationID,
				Generation:   1, Fence: taskorchestration.RuntimeFence(before.PhaseRuns[0].Fence),
				Outcome: taskorchestration.RuntimeRunSucceeded,
			},
		))
		if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorTerminalConflict {
			t.Fatalf("evidence during cancelling error = %T, want terminal conflict", err)
		}
	})
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
	if _, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
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
				taskorchestration.NewPublicationAuthority(
					authorityID(t, prefix+"-scenario-publication"), taskorchestration.AuthorizationGeneration(1),
				),
				taskorchestration.PublicationEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, fmt.Sprintf("%s-publication-evidence-%d", prefix, phaseIndex)),
						taskorchestration.EvidencePublication,
						evidenceDigest(t, "3333333333333333333333333333333333333333333333333333333333333333"),
					),
					PhaseRunID: run.PhaseRunID, OperationID: work.EnactmentRefs[0].OperationID,
					Generation: 1, Fence: taskorchestration.PublicationFence(run.Fence),
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
					taskorchestration.NewRuntimeAuthority(
						authorityID(t, prefix+"-scenario-runtime"), taskorchestration.AuthorizationGeneration(1),
					),
					taskorchestration.RuntimeEvidenceBinding{
						Evidence: taskorchestration.NewEvidenceRef(
							evidenceID(t, fmt.Sprintf("%s-runtime-evidence-%d-%d", prefix, phaseIndex, runtimeIndex)),
							taskorchestration.EvidenceRuntime,
							evidenceDigest(t, "4444444444444444444444444444444444444444444444444444444444444444"),
						),
						PhaseRunID: run.PhaseRunID, RuntimeRunID: runtimeRun.RuntimeRunID,
						OperationID: work.EnactmentRefs[runtimeIndex].OperationID,
						Generation:  1, Fence: taskorchestration.RuntimeFence(run.Fence),
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
			validation, err := harness.Mutations.Decide(context.Background(), taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
				header,
				taskorchestration.NewValidatorAuthority(
					authorityID(t, prefix+"-scenario-validator"), taskorchestration.AuthorizationGeneration(1),
				),
				taskorchestration.ValidationEvidenceBinding{
					Evidence: taskorchestration.NewEvidenceRef(
						evidenceID(t, fmt.Sprintf("%s-validation-evidence-%d", prefix, phaseIndex)),
						taskorchestration.EvidencePhaseValidation,
						evidenceDigest(t, "5555555555555555555555555555555555555555555555555555555555555555"),
					),
					PhaseRunID: run.PhaseRunID, Generation: 1,
					Fence:   taskorchestration.ValidationFence(run.Fence),
					Outcome: taskorchestration.PhaseValidationAccepted,
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
					taskorchestration.NewTaskWorkspaceLifecycleAuthority(
						authorityID(t, prefix+"-scenario-c04"), taskorchestration.AuthorizationGeneration(1),
					),
					taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
						Evidence: taskorchestration.NewEvidenceRef(
							evidenceID(t, fmt.Sprintf("%s-lifecycle-evidence-%d", prefix, phaseIndex)),
							taskorchestration.EvidenceTaskWorkspaceLifecycle,
							evidenceDigest(t, "6666666666666666666666666666666666666666666666666666666666666666"),
						),
						PhaseRunID: run.PhaseRunID, OperationID: validation.EnactmentRefs[0].OperationID,
						Generation: 1, Fence: taskorchestration.TaskWorkspaceLifecycleFence(run.Fence),
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
