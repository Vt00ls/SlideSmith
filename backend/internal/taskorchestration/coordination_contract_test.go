package taskorchestration_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
)

func TestExactReplayPrecedesCurrentRevisionValidation(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-replay"), taskorchestration.AuthorizationGeneration(1),
	)
	originalIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "request-exact-replay", "task-exact-replay", now),
		authority,
	)
	original, err := harness.Mutations.Decide(context.Background(), originalIntent)
	if err != nil {
		t.Fatalf("commit original decision: %v", err)
	}

	advanceHeader := intentHeader(t, "request-after-original", "task-exact-replay", now.Add(time.Second))
	advanceHeader.ExpectedTaskRevision = 1
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(advanceHeader, authority),
	); err != nil {
		t.Fatalf("advance Task revision: %v", err)
	}

	replayed, err := harness.Mutations.Decide(context.Background(), originalIntent)
	if err != nil {
		t.Fatalf("replay original decision after revision advanced: %v", err)
	}
	if !reflect.DeepEqual(replayed, original) {
		t.Fatal("exact replay did not return the original committed decision")
	}
	view, err := harness.Queries.Query(
		context.Background(), taskQuery(t, "task-exact-replay", "user-authority-replay"),
	)
	if err != nil {
		t.Fatalf("query after exact replay: %v", err)
	}
	if view.TaskRevision != 2 || view.DecisionCount != 2 {
		t.Fatalf(
			"replay state = revision %d, decisions %d; want 2, 2",
			view.TaskRevision, view.DecisionCount,
		)
	}
}

func TestDecisionRequestIdentityCannotBeReboundToAnotherPayload(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 10, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-integrity"), taskorchestration.AuthorizationGeneration(1),
	)
	originalIntent := taskorchestration.NewStartTaskIntent(
		intentHeader(t, "request-integrity", "task-integrity", now), authority,
	)
	if _, err := harness.Mutations.Decide(context.Background(), originalIntent); err != nil {
		t.Fatalf("commit original decision: %v", err)
	}

	changedHeader := originalIntent.Header()
	changedHeader.ExpectedTaskRevision = 1
	changedHeader.OccurredAt = now.Add(time.Second)
	changedIntent := taskorchestration.NewStartTaskIntent(changedHeader, authority)
	_, err = harness.Mutations.Decide(context.Background(), changedIntent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorIntegrityConflict {
		t.Fatalf("same-key changed-payload error = %T, want integrity conflict", err)
	}

	view, err := harness.Queries.Query(
		context.Background(), taskQuery(t, "task-integrity", "user-authority-integrity"),
	)
	if err != nil {
		t.Fatalf("query after integrity conflict: %v", err)
	}
	if view.TaskRevision != 1 || view.DecisionCount != 1 || view.EnactmentCount != 0 {
		t.Fatalf(
			"integrity conflict state = revision %d, decisions %d, enactments %d; want 1, 1, 0",
			view.TaskRevision, view.DecisionCount, view.EnactmentCount,
		)
	}
}

func TestCancellationAdvancesActivityGenerationAndRejectsStaleWork(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 20, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-generation"), taskorchestration.AuthorizationGeneration(1),
	)
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "request-generation-start", "task-generation", now), authority,
		),
	); err != nil {
		t.Fatalf("start Task: %v", err)
	}

	cancelHeader := intentHeader(t, "request-generation-cancel", "task-generation", now.Add(time.Second))
	cancelHeader.ExpectedTaskRevision = 1
	cancelled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, authority, taskorchestration.CancelReasonUserRequested,
		),
	)
	if err != nil {
		t.Fatalf("accept cancellation: %v", err)
	}
	if cancelled.TaskProjection.ActivityGeneration != 2 ||
		cancelled.TaskProjection.CancellationState != taskorchestration.CancellationCancelled {
		t.Fatalf(
			"cancellation projection = generation %d, state %d; want 2, cancelled",
			cancelled.TaskProjection.ActivityGeneration,
			cancelled.TaskProjection.CancellationState,
		)
	}

	staleHeader := intentHeader(t, "request-generation-stale", "task-generation", now.Add(2*time.Second))
	staleHeader.ExpectedTaskRevision = 2
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewSubmitConfirmationGateIntent(
			staleHeader,
			authority,
			gateID(t, "gate-generation-stale"),
			payloadDigest(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
		t.Fatalf("stale generation error = %T, want stale authority", err)
	}

	fencedHeader := intentHeader(t, "request-generation-fenced", "task-generation", now.Add(3*time.Second))
	fencedHeader.ExpectedTaskRevision = 2
	fencedHeader.ActivityGeneration = 2
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewSubmitConfirmationGateIntent(
			fencedHeader,
			authority,
			gateID(t, "gate-generation-fenced"),
			payloadDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		),
	)
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorTerminalConflict {
		t.Fatalf("post-cancellation work error = %T, want terminal conflict", err)
	}
	view, err := harness.Queries.Query(
		context.Background(), taskQuery(t, "task-generation", "user-authority-generation"),
	)
	if err != nil {
		t.Fatalf("query after stale generation: %v", err)
	}
	if view.TaskRevision != 2 || view.ActivityGeneration != 2 || view.DecisionCount != 2 ||
		view.CancellationState != taskorchestration.CancellationCancelled {
		t.Fatalf(
			"stale generation state = revision %d, generation %d, decisions %d; want 2, 2, 2",
			view.TaskRevision, view.ActivityGeneration, view.DecisionCount,
		)
	}
}

func TestRuntimeEvidenceRejectsEveryStaleTypedBinding(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 30, 0, 0, time.UTC)
	task := taskID(t, "task-runtime-fencing")
	phaseRun := phaseRunID(t, "phase-run-runtime-fencing")
	runtimeRun := runtimeRunID(t, "runtime-run-runtime-fencing")
	operation := operationID(t, "operation-runtime-fencing")
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-runtime-fencing"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-fencing"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID:             task,
			Owner:              authority,
			TaskRevision:       7,
			ActivityGeneration: 3,
			SafetyEpoch:        5,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun,
				Generation: 2,
				Fence:      9,
				Active:     true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID:  operation,
				PhaseRunID:   phaseRun,
				RuntimeRunID: runtimeRun,
				Authority:    runtimeAuthority,
				Generation:   4,
				Fence:        11,
				SafetyEpoch:  5,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "runtime-evidence-fencing"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	)

	tests := []struct {
		name         string
		activity     taskorchestration.ActivityGeneration
		phaseGen     taskorchestration.PhaseRunGeneration
		phaseFence   taskorchestration.PhaseRunFence
		runtimeGen   taskorchestration.RuntimeGeneration
		runtimeFence taskorchestration.RuntimeFence
		safetyEpoch  taskorchestration.SafetyEpoch
	}{
		{name: "activity generation", activity: 2, phaseGen: 2, phaseFence: 9, runtimeGen: 4, runtimeFence: 11, safetyEpoch: 5},
		{name: "Phase Run generation", activity: 3, phaseGen: 1, phaseFence: 9, runtimeGen: 4, runtimeFence: 11, safetyEpoch: 5},
		{name: "Phase Run fence", activity: 3, phaseGen: 2, phaseFence: 8, runtimeGen: 4, runtimeFence: 11, safetyEpoch: 5},
		{name: "Runtime generation", activity: 3, phaseGen: 2, phaseFence: 9, runtimeGen: 3, runtimeFence: 11, safetyEpoch: 5},
		{name: "Runtime lease fence", activity: 3, phaseGen: 2, phaseFence: 9, runtimeGen: 4, runtimeFence: 10, safetyEpoch: 5},
		{name: "out-of-order future Runtime fence", activity: 3, phaseGen: 2, phaseFence: 9, runtimeGen: 4, runtimeFence: 12, safetyEpoch: 5},
		{name: "safety epoch", activity: 3, phaseGen: 2, phaseFence: 9, runtimeGen: 4, runtimeFence: 11, safetyEpoch: 4},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := intentHeader(t, "request-runtime-stale-"+string(rune('a'+index)), task.String(), now)
			header.ExpectedTaskRevision = 7
			header.ActivityGeneration = test.activity
			_, err := harness.Mutations.Decide(
				context.Background(),
				taskorchestration.NewAcceptRuntimeEvidenceIntent(
					header,
					runtimeAuthority,
					taskorchestration.RuntimeEvidenceBinding{
						Evidence:           evidence,
						PhaseRunID:         phaseRun,
						PhaseRunGeneration: test.phaseGen,
						PhaseRunFence:      test.phaseFence,
						RuntimeRunID:       runtimeRun,
						OperationID:        operation,
						Generation:         test.runtimeGen,
						Fence:              test.runtimeFence,
						SafetyEpoch:        test.safetyEpoch,
					},
				),
			)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
				t.Fatalf("stale binding error = %T, want stale authority", err)
			}
		})
	}

	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(authority),
		},
	)
	if err != nil {
		t.Fatalf("query after stale evidence: %v", err)
	}
	if view.TaskRevision != 7 || view.DecisionCount != 0 || view.EnactmentCount != 0 {
		t.Fatalf(
			"stale evidence state = revision %d, decisions %d, enactments %d; want 7, 0, 0",
			view.TaskRevision, view.DecisionCount, view.EnactmentCount,
		)
	}
}

func TestDuplicateExactRuntimeEvidenceReturnsItsOriginalDecision(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 40, 0, 0, time.UTC)
	task := taskID(t, "task-runtime-duplicate")
	phaseRun := phaseRunID(t, "phase-run-runtime-duplicate")
	runtimeRun := runtimeRunID(t, "runtime-run-runtime-duplicate")
	operation := operationID(t, "operation-runtime-duplicate")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-runtime-duplicate"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-duplicate"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID:             task,
			Owner:              owner,
			TaskRevision:       4,
			ActivityGeneration: 2,
			SafetyEpoch:        3,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 6, Fence: 7, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: runtimeAuthority, Generation: 8, Fence: 9, SafetyEpoch: 3,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "runtime-evidence-duplicate"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
	)
	binding := taskorchestration.RuntimeEvidenceBinding{
		Evidence: evidence, PhaseRunID: phaseRun, PhaseRunGeneration: 6, PhaseRunFence: 7,
		RuntimeRunID: runtimeRun, OperationID: operation, Generation: 8, Fence: 9, SafetyEpoch: 3,
	}
	firstHeader := intentHeader(t, "request-runtime-evidence-first", task.String(), now)
	firstHeader.ExpectedTaskRevision = 4
	firstHeader.ActivityGeneration = 2
	first, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(firstHeader, runtimeAuthority, binding),
	)
	if err != nil {
		t.Fatalf("accept Runtime Evidence: %v", err)
	}
	expectedEvidence := first.AcceptedEvidenceRefs[0]
	first.AcceptedEvidenceRefs[0] = taskorchestration.NewEvidenceRef(
		evidenceID(t, "caller-mutated-evidence"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "9999999999999999999999999999999999999999999999999999999999999999"),
	)

	duplicateHeader := firstHeader
	duplicateHeader.DecisionRequestID = decisionRequestID(t, "request-runtime-evidence-duplicate")
	duplicate := taskorchestration.NewAcceptRuntimeEvidenceIntent(
		duplicateHeader, runtimeAuthority, binding,
	)
	replayed, err := harness.Mutations.Decide(context.Background(), duplicate)
	if err != nil {
		t.Fatalf("accept duplicate exact Runtime Evidence: %v", err)
	}
	if len(replayed.AcceptedEvidenceRefs) != 1 || replayed.AcceptedEvidenceRefs[0] != expectedEvidence ||
		replayed.DecisionID != first.DecisionID || replayed.AcceptedTaskRevision != 5 {
		t.Fatal("duplicate exact Runtime Evidence did not return its immutable original decision")
	}
	reboundBinding := binding
	reboundBinding.OperationID = operationID(t, "operation-runtime-evidence-rebound")
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(
			duplicateHeader, runtimeAuthority, reboundBinding,
		),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorIntegrityConflict {
		t.Fatalf("rebound duplicate request error = %T, want integrity conflict", err)
	}
	view, err := harness.Queries.Query(
		context.Background(),
		taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query after duplicate exact evidence: %v", err)
	}
	if view.TaskRevision != 5 || view.DecisionCount != 1 {
		t.Fatalf(
			"duplicate evidence state = revision %d, decisions %d; want 5, 1",
			view.TaskRevision, view.DecisionCount,
		)
	}
}

func TestMismatchedEvidenceIsRetainedOnlyAsNonAuthoritativeDiagnostic(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 50, 0, 0, time.UTC)
	task := taskID(t, "task-runtime-mismatch")
	phaseRun := phaseRunID(t, "phase-run-runtime-mismatch")
	runtimeRun := runtimeRunID(t, "runtime-run-runtime-mismatch")
	operation := operationID(t, "operation-runtime-mismatch")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-runtime-mismatch"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-mismatch"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 2, ActivityGeneration: 1, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 1, Fence: 2, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: runtimeAuthority, Generation: 3, Fence: 4, SafetyEpoch: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "runtime-evidence-mismatch"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
	)
	header := intentHeader(t, "request-runtime-mismatch-original", task.String(), now)
	header.ExpectedTaskRevision = 2
	binding := taskorchestration.RuntimeEvidenceBinding{
		Evidence: evidence, PhaseRunID: phaseRun, PhaseRunGeneration: 1, PhaseRunFence: 2,
		RuntimeRunID: runtimeRun, OperationID: operation, Generation: 3, Fence: 4, SafetyEpoch: 1,
	}
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(header, runtimeAuthority, binding),
	); err != nil {
		t.Fatalf("accept original Runtime Evidence: %v", err)
	}

	mismatchHeader := header
	mismatchHeader.DecisionRequestID = decisionRequestID(t, "request-runtime-mismatch-conflict")
	mismatchBinding := binding
	mismatchBinding.OperationID = operationID(t, "operation-runtime-mismatch-other")
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(
			mismatchHeader, runtimeAuthority, mismatchBinding,
		),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorEvidenceScopeConflict {
		t.Fatalf("mismatched evidence error = %T, want evidence scope conflict", err)
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query after mismatched evidence: %v", err)
	}
	if view.TaskRevision != 3 || view.DecisionCount != 1 || view.EnactmentCount != 0 {
		t.Fatalf(
			"mismatch state = revision %d, decisions %d, enactments %d; want 3, 1, 0",
			view.TaskRevision, view.DecisionCount, view.EnactmentCount,
		)
	}
	if view.EvidenceDiagnosticCount != 1 ||
		view.LatestEvidenceDiagnostic.EvidenceID != evidence.ID ||
		view.LatestEvidenceDiagnostic.Disposition != taskorchestration.EvidenceDispositionNonAuthoritative ||
		view.LatestEvidenceDiagnostic.Reason != taskorchestration.EvidenceDiagnosticScopeConflict {
		t.Fatal("mismatched evidence was not retained as a non-authoritative diagnostic disposition")
	}
}

func TestTerminalOperationRejectsFreshEvidenceWithoutOverwritingItsResult(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 55, 0, 0, time.UTC)
	task := taskID(t, "task-terminal-operation")
	phaseRun := phaseRunID(t, "phase-run-terminal-operation")
	operation := operationID(t, "operation-terminal-operation")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-terminal-operation"),
		taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-terminal-operation"),
		taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 10, ActivityGeneration: 2, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 1, Fence: 2, Active: true,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, Authority: lifecycleAuthority,
				Generation: 3, Fence: 4, SafetyEpoch: 1,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	firstRevision := taskWorkspaceRevisionID(t, "workspace-revision-terminal-operation-first")
	firstCheckpoint := checkpointID(t, "checkpoint-terminal-operation-first")
	header := intentHeader(t, "request-terminal-operation-first", task.String(), now)
	header.ExpectedTaskRevision = 10
	header.ActivityGeneration = 2
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			header,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "lifecycle-evidence-terminal-operation-first"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "1212121212121212121212121212121212121212121212121212121212121212"),
				),
				PhaseRunID: phaseRun, PhaseRunGeneration: 1, PhaseRunFence: 2,
				OperationID: operation, Generation: 3, Fence: 4, SafetyEpoch: 1,
				Outcome:    taskorchestration.LifecycleEvidenceCommitted,
				RevisionID: firstRevision, CheckpointID: firstCheckpoint,
			},
		),
	); err != nil {
		t.Fatalf("accept first terminal evidence: %v", err)
	}

	secondHeader := intentHeader(t, "request-terminal-operation-second", task.String(), now.Add(time.Second))
	secondHeader.ExpectedTaskRevision = 11
	secondHeader.ActivityGeneration = 2
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			secondHeader,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "lifecycle-evidence-terminal-operation-second"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "3434343434343434343434343434343434343434343434343434343434343434"),
				),
				PhaseRunID: phaseRun, PhaseRunGeneration: 1, PhaseRunFence: 2,
				OperationID: operation, Generation: 3, Fence: 4, SafetyEpoch: 1,
				Outcome:      taskorchestration.LifecycleEvidenceCommitted,
				RevisionID:   taskWorkspaceRevisionID(t, "workspace-revision-terminal-operation-second"),
				CheckpointID: checkpointID(t, "checkpoint-terminal-operation-second"),
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorEvidenceScopeConflict)

	view, err := harness.Queries.Query(
		context.Background(),
		taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query terminal operation: %v", err)
	}
	if view.TaskRevision != 11 || view.DecisionCount != 1 ||
		view.LatestRevisionID != firstRevision || view.LatestCheckpointID != firstCheckpoint ||
		view.EvidenceDiagnosticCount != 1 ||
		view.LatestEvidenceDiagnostic.Reason != taskorchestration.EvidenceDiagnosticScopeConflict {
		t.Fatal("fresh evidence overwrote a consumed terminal operation")
	}
}

func TestInactivePhaseRunRejectsLateEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 15, 57, 0, 0, time.UTC)
	task := taskID(t, "task-inactive-phase-evidence")
	phaseRun := phaseRunID(t, "phase-run-inactive-evidence")
	runtimeRun := runtimeRunID(t, "runtime-run-inactive-evidence")
	operation := operationID(t, "operation-inactive-evidence")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-inactive-evidence"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-inactive-evidence"),
		taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 5, ActivityGeneration: 2, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 3, Fence: 4, Active: false,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: runtimeAuthority, Generation: 5, Fence: 6, SafetyEpoch: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	header := intentHeader(t, "request-inactive-phase-evidence", task.String(), now)
	header.ExpectedTaskRevision = 5
	header.ActivityGeneration = 2
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(
			header,
			runtimeAuthority,
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "runtime-evidence-inactive-phase"), taskorchestration.EvidenceRuntime,
					evidenceDigest(t, "7878787878787878787878787878787878787878787878787878787878787878"),
				),
				PhaseRunID: phaseRun, PhaseRunGeneration: 3, PhaseRunFence: 4,
				RuntimeRunID: runtimeRun, OperationID: operation,
				Generation: 5, Fence: 6, SafetyEpoch: 1,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorStaleAuthority)
	view, err := harness.Queries.Query(
		context.Background(),
		taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query inactive Phase Run: %v", err)
	}
	if view.TaskRevision != 5 || view.DecisionCount != 0 || view.EvidenceDiagnosticCount != 1 ||
		view.LatestEvidenceDiagnostic.Reason != taskorchestration.EvidenceDiagnosticStale {
		t.Fatal("inactive Phase Run evidence changed authoritative Task state")
	}
}

func TestExactLifecycleCommitEvidenceRecordsRevisionAndCheckpoint(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 0, 0, 0, time.UTC)
	task := taskID(t, "task-lifecycle-commit")
	phaseRun := phaseRunID(t, "phase-run-lifecycle-commit")
	operation := operationID(t, "operation-lifecycle-commit")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-lifecycle-commit"), taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-commit"), taskorchestration.AuthorizationGeneration(1),
	)
	revisionID := taskWorkspaceRevisionID(t, "workspace-revision-committed")
	checkpoint := checkpointID(t, "checkpoint-committed")
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 10, ActivityGeneration: 4, SafetyEpoch: 2,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 3, Fence: 5, Active: true,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, Authority: lifecycleAuthority,
				Generation: 7, Fence: 11, SafetyEpoch: 2,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "lifecycle-evidence-commit"),
		taskorchestration.EvidenceTaskWorkspaceLifecycle,
		evidenceDigest(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
	)
	header := intentHeader(t, "request-lifecycle-commit", task.String(), now)
	header.ExpectedTaskRevision = 10
	header.ActivityGeneration = 4
	decision, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			header,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: evidence, PhaseRunID: phaseRun, PhaseRunGeneration: 3, PhaseRunFence: 5,
				OperationID: operation, Generation: 7, Fence: 11, SafetyEpoch: 2,
				Outcome:    taskorchestration.LifecycleEvidenceCommitted,
				RevisionID: revisionID, CheckpointID: checkpoint,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept lifecycle commit evidence: %v", err)
	}
	if decision.TaskProjection.LatestRevisionID != revisionID ||
		decision.TaskProjection.LatestCheckpointID != checkpoint {
		t.Fatal("commit decision omitted the exact Task Workspace Revision or Checkpoint")
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query after lifecycle commit: %v", err)
	}
	if view.TaskRevision != 11 || view.LatestRevisionID != revisionID ||
		view.LatestCheckpointID != checkpoint {
		t.Fatal("Task projection did not retain the accepted lifecycle commit facts")
	}
}

func TestCommitFirstCancellationRetainsCommitAndFencesFurtherWork(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 10, 0, 0, time.UTC)
	task := taskID(t, "task-cancel-commit-first")
	phaseRun := phaseRunID(t, "phase-run-cancel-commit-first")
	runtimeRun := runtimeRunID(t, "runtime-run-cancel-commit-first")
	runtimeOperation := operationID(t, "operation-runtime-cancel-commit-first")
	lifecycleOperation := operationID(t, "operation-lifecycle-cancel-commit-first")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-cancel-commit-first"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-cancel-commit-first"), taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-cancel-commit-first"), taskorchestration.AuthorizationGeneration(1),
	)
	revisionID := taskWorkspaceRevisionID(t, "workspace-revision-cancel-commit-first")
	checkpoint := checkpointID(t, "checkpoint-cancel-commit-first")
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 20, ActivityGeneration: 4, SafetyEpoch: 2,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 3, Fence: 5, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: runtimeOperation, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: runtimeAuthority, Generation: 8, Fence: 13, SafetyEpoch: 2,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: lifecycleOperation, PhaseRunID: phaseRun, Authority: lifecycleAuthority,
				Generation: 7, Fence: 11, SafetyEpoch: 2,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	commitHeader := intentHeader(t, "request-cancel-commit-first-evidence", task.String(), now)
	commitHeader.ExpectedTaskRevision = 20
	commitHeader.ActivityGeneration = 4
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			commitHeader,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "lifecycle-evidence-cancel-commit-first"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
				),
				PhaseRunID: phaseRun, PhaseRunGeneration: 3, PhaseRunFence: 5,
				OperationID: lifecycleOperation, Generation: 7, Fence: 11, SafetyEpoch: 2,
				Outcome:    taskorchestration.LifecycleEvidenceCommitted,
				RevisionID: revisionID, CheckpointID: checkpoint,
			},
		),
	); err != nil {
		t.Fatalf("accept commit-first lifecycle evidence: %v", err)
	}

	cancelHeader := intentHeader(t, "request-cancel-commit-first", task.String(), now.Add(time.Second))
	cancelHeader.ExpectedTaskRevision = 21
	cancelHeader.ActivityGeneration = 4
	cancelled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
		),
	)
	if err != nil {
		t.Fatalf("accept commit-first cancellation: %v", err)
	}
	if cancelled.TaskProjection.CancellationState != taskorchestration.CancellationCancelling ||
		cancelled.TaskProjection.ActivityGeneration != 5 ||
		cancelled.TaskProjection.LatestRevisionID != revisionID ||
		cancelled.TaskProjection.LatestCheckpointID != checkpoint {
		t.Fatal("commit-first cancellation lost the committed state or cancellation fence")
	}
	if len(cancelled.EnactmentRefs) != 2 {
		t.Fatalf("cancellation enactments = %d, want Runtime cancel and C04 fence/discard", len(cancelled.EnactmentRefs))
	}
	kinds := map[taskorchestration.EnactmentKind]bool{}
	operationIDs := map[string]bool{}
	for _, enactment := range cancelled.EnactmentRefs {
		kinds[enactment.Kind] = true
		operationIDs[enactment.OperationID.String()] = true
		if enactment.ActivityGeneration != 5 {
			t.Fatal("cancellation enactment did not bind the fenced activity generation")
		}
	}
	if !kinds[taskorchestration.EnactmentRuntimeExecution] ||
		!kinds[taskorchestration.EnactmentTaskWorkspaceLifecycle] || len(operationIDs) != 2 {
		t.Fatal("cancellation did not create distinct Runtime and C04 operation identities")
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query commit-first cancellation: %v", err)
	}
	if view.TaskRevision != 22 || view.CancellationState != taskorchestration.CancellationCancelling ||
		view.PhaseRunCount != 1 || view.RuntimeRunCount != 1 || view.EnactmentCount != 2 {
		t.Fatal("commit-first cancellation created a business attempt or omitted its fences")
	}
}

func TestCommitFirstEvidenceMayArriveAfterCancellationDecision(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 15, 0, 0, time.UTC)
	task := taskID(t, "task-cancel-commit-response-late")
	phaseRun := phaseRunID(t, "phase-run-cancel-commit-response-late")
	commitOperation := operationID(t, "operation-cancel-commit-response-late")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-cancel-commit-response-late"),
		taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-cancel-commit-response-late"),
		taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 60, ActivityGeneration: 1, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 1, Fence: 3, Active: true,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: commitOperation, PhaseRunID: phaseRun, Authority: lifecycleAuthority,
				Generation: 2, Fence: 4, SafetyEpoch: 1,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	cancelHeader := intentHeader(t, "request-cancel-before-commit-response", task.String(), now)
	cancelHeader.ExpectedTaskRevision = 60
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
		),
	); err != nil {
		t.Fatalf("accept cancellation while commit response is pending: %v", err)
	}

	revisionID := taskWorkspaceRevisionID(t, "workspace-revision-commit-response-late")
	checkpoint := checkpointID(t, "checkpoint-commit-response-late")
	commitHeader := intentHeader(t, "request-commit-response-late", task.String(), now.Add(time.Second))
	commitHeader.ExpectedTaskRevision = 61
	commitHeader.ActivityGeneration = 1
	committed, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			commitHeader,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "lifecycle-evidence-commit-response-late"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "7777777777777777777777777777777777777777777777777777777777777777"),
				),
				PhaseRunID: phaseRun, PhaseRunGeneration: 1, PhaseRunFence: 3,
				OperationID: commitOperation, Generation: 2, Fence: 4, SafetyEpoch: 1,
				Outcome:    taskorchestration.LifecycleEvidenceCommitted,
				RevisionID: revisionID, CheckpointID: checkpoint,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept commit-first evidence after cancellation Decision: %v", err)
	}
	if committed.TaskProjection.ActivityGeneration != 2 ||
		committed.TaskProjection.CancellationState != taskorchestration.CancellationCancelling ||
		committed.TaskProjection.LatestRevisionID != revisionID ||
		committed.TaskProjection.LatestCheckpointID != checkpoint {
		t.Fatal("late commit response lost the cancellation fence or committed state")
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query commit-first late response: %v", err)
	}
	if view.TaskRevision != 62 || view.ActivityGeneration != 2 ||
		view.CancellationState != taskorchestration.CancellationCancelling ||
		view.PhaseRunCount != 1 {
		t.Fatal("commit-first response advanced later work or removed the cancellation fence")
	}
}

func TestFenceFirstCancellationRejectsLateCommitAndTerminatesCancelled(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 20, 0, 0, time.UTC)
	task := taskID(t, "task-cancel-fence-first")
	phaseRun := phaseRunID(t, "phase-run-cancel-fence-first")
	commitOperation := operationID(t, "operation-lifecycle-cancel-fence-first")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-cancel-fence-first"), taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-cancel-fence-first"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 30, ActivityGeneration: 4, SafetyEpoch: 2,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 3, Fence: 5, Active: true,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: commitOperation, PhaseRunID: phaseRun, Authority: lifecycleAuthority,
				Generation: 7, Fence: 11, SafetyEpoch: 2,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	cancelHeader := intentHeader(t, "request-cancel-fence-first", task.String(), now)
	cancelHeader.ExpectedTaskRevision = 30
	cancelHeader.ActivityGeneration = 4
	cancelled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
		),
	)
	if err != nil {
		t.Fatalf("accept fence-first cancellation: %v", err)
	}
	var fenceOperation taskorchestration.EnactmentRef
	for _, enactment := range cancelled.EnactmentRefs {
		if enactment.Kind == taskorchestration.EnactmentTaskWorkspaceLifecycle {
			fenceOperation = enactment
			break
		}
	}
	if fenceOperation.OperationID.String() == "" {
		t.Fatal("cancellation omitted its C04 fence/discard operation")
	}
	lifecycleFence, ok := fenceOperation.Fence.(taskorchestration.TaskWorkspaceLifecycleFence)
	if !ok {
		t.Fatal("cancellation C04 enactment did not retain its typed fence")
	}
	fenceHeader := intentHeader(t, "request-cancel-fence-evidence", task.String(), now.Add(time.Second))
	fenceHeader.ExpectedTaskRevision = 31
	fenceHeader.ActivityGeneration = 5
	terminal, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			fenceHeader,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence: taskorchestration.NewEvidenceRef(
					evidenceID(t, "lifecycle-evidence-cancel-fenced"),
					taskorchestration.EvidenceTaskWorkspaceLifecycle,
					evidenceDigest(t, "1111111111111111111111111111111111111111111111111111111111111111"),
				),
				PhaseRunID: phaseRun, PhaseRunGeneration: 3, PhaseRunFence: 5,
				OperationID: fenceOperation.OperationID,
				Generation:  7, Fence: lifecycleFence, SafetyEpoch: 2,
				Outcome: taskorchestration.LifecycleEvidenceFenced,
			},
		),
	)
	if err != nil {
		t.Fatalf("accept C04 fencing evidence: %v", err)
	}
	if terminal.TaskProjection.CancellationState != taskorchestration.CancellationCancelled {
		t.Fatal("C04 fencing evidence did not terminally cancel the Task")
	}

	lateHeader := intentHeader(t, "request-cancel-late-commit", task.String(), now.Add(2*time.Second))
	lateHeader.ExpectedTaskRevision = 32
	lateHeader.ActivityGeneration = 5
	lateEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "lifecycle-evidence-cancel-late-commit"),
		taskorchestration.EvidenceTaskWorkspaceLifecycle,
		evidenceDigest(t, "2222222222222222222222222222222222222222222222222222222222222222"),
	)
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
			lateHeader,
			lifecycleAuthority,
			taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
				Evidence:   lateEvidence,
				PhaseRunID: phaseRun, PhaseRunGeneration: 3, PhaseRunFence: 5,
				OperationID: commitOperation, Generation: 7, Fence: 11, SafetyEpoch: 2,
				Outcome:      taskorchestration.LifecycleEvidenceCommitted,
				RevisionID:   taskWorkspaceRevisionID(t, "workspace-revision-cancel-late"),
				CheckpointID: checkpointID(t, "checkpoint-cancel-late"),
			},
		),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
		t.Fatalf("late commit error = %T, want stale authority", err)
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query fence-first cancellation: %v", err)
	}
	if view.TaskRevision != 32 || view.CancellationState != taskorchestration.CancellationCancelled ||
		view.LatestRevisionID.String() != "" || view.LatestCheckpointID.String() != "" ||
		view.EvidenceDiagnosticCount != 1 ||
		view.LatestEvidenceDiagnostic.EvidenceID != lateEvidence.ID ||
		view.LatestEvidenceDiagnostic.Reason != taskorchestration.EvidenceDiagnosticStale {
		t.Fatal("late commit changed the terminal Task or was not retained as non-authoritative diagnostic")
	}
}

func TestRecoveryReadOnlyAndPostRecoverySafetyEpochFenceOldEvidence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 30, 0, 0, time.UTC)
	task := taskID(t, "task-recovery-fencing")
	phaseRun := phaseRunID(t, "phase-run-recovery-fencing")
	runtimeRun := runtimeRunID(t, "runtime-run-recovery-fencing")
	runtimeOperation := operationID(t, "operation-runtime-recovery-fencing")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-recovery-fencing"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-recovery-fencing"), taskorchestration.AuthorizationGeneration(1),
	)
	recoveryAuthority := taskorchestration.NewRecoveryAuthority(
		authorityID(t, "recovery-authority-fencing"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Recovery: taskorchestration.HarnessRecoveryFixture{
			Authority: recoveryAuthority, Generation: 1, Fence: 1, SafetyEpoch: 1,
			Mode: taskorchestration.OperationalFullReady,
		},
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 40, ActivityGeneration: 2, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 1, Fence: 2, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: runtimeOperation, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: runtimeAuthority, Generation: 3, Fence: 4, SafetyEpoch: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	readOnlyHeader := intentHeader(t, "request-recovery-read-only", task.String(), now)
	readOnlyHeader.ExpectedTaskRevision = 40
	readOnlyHeader.ActivityGeneration = 2
	readOnlyDecision, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewApplyOperationalFenceIntent(
			readOnlyHeader,
			recoveryAuthority,
			taskorchestration.OperationalFenceBinding{
				Generation: 2, Fence: 2, SafetyEpoch: 2, Mode: taskorchestration.OperationalReadOnly,
			},
		),
	)
	if err != nil {
		t.Fatalf("apply recovery read-only fence: %v", err)
	}
	if readOnlyDecision.TaskProjection.OperationalMode != taskorchestration.OperationalReadOnly ||
		readOnlyDecision.TaskProjection.SafetyEpoch != 2 {
		t.Fatal("read-only decision omitted its recovery generation fence")
	}

	blockedHeader := intentHeader(t, "request-recovery-blocked", task.String(), now.Add(time.Second))
	blockedHeader.ExpectedTaskRevision = 41
	blockedHeader.ActivityGeneration = 2
	_, err = harness.Mutations.Decide(
		context.Background(), taskorchestration.NewStartTaskIntent(blockedHeader, owner),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorOperationalReadOnly {
		t.Fatalf("read-only mutation error = %T, want operational read-only", err)
	}
	replayed, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewApplyOperationalFenceIntent(
			readOnlyHeader,
			recoveryAuthority,
			taskorchestration.OperationalFenceBinding{
				Generation: 2, Fence: 2, SafetyEpoch: 2, Mode: taskorchestration.OperationalReadOnly,
			},
		),
	)
	if err != nil || !reflect.DeepEqual(replayed, readOnlyDecision) {
		t.Fatal("read-only mode prevented exact replay of its committed decision")
	}

	readyHeader := intentHeader(t, "request-recovery-full-ready", task.String(), now.Add(2*time.Second))
	readyHeader.ExpectedTaskRevision = 41
	readyHeader.ActivityGeneration = 3
	ready, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewApplyOperationalFenceIntent(
			readyHeader,
			recoveryAuthority,
			taskorchestration.OperationalFenceBinding{
				Generation: 3, Fence: 3, SafetyEpoch: 3, Mode: taskorchestration.OperationalFullReady,
			},
		),
	)
	if err != nil {
		t.Fatalf("apply full-ready recovery fence: %v", err)
	}
	if ready.TaskProjection.OperationalMode != taskorchestration.OperationalFullReady ||
		ready.TaskProjection.SafetyEpoch != 3 {
		t.Fatal("full-ready decision omitted its post-recovery safety epoch")
	}

	staleHeader := intentHeader(t, "request-recovery-stale-runtime", task.String(), now.Add(3*time.Second))
	staleHeader.ExpectedTaskRevision = 42
	staleHeader.ActivityGeneration = 2
	staleEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "runtime-evidence-pre-recovery"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, "3333333333333333333333333333333333333333333333333333333333333333"),
	)
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptRuntimeEvidenceIntent(
			staleHeader,
			runtimeAuthority,
			taskorchestration.RuntimeEvidenceBinding{
				Evidence: staleEvidence, PhaseRunID: phaseRun, PhaseRunGeneration: 1, PhaseRunFence: 2,
				RuntimeRunID: runtimeRun, OperationID: runtimeOperation,
				Generation: 3, Fence: 4, SafetyEpoch: 1,
			},
		),
	)
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
		t.Fatalf("pre-recovery evidence error = %T, want stale authority", err)
	}

	resumedHeader := intentHeader(t, "request-recovery-resumed", task.String(), now.Add(4*time.Second))
	resumedHeader.ExpectedTaskRevision = 42
	resumedHeader.ActivityGeneration = 4
	if _, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewStartTaskIntent(resumedHeader, owner),
	); err != nil {
		t.Fatalf("mutate safely after full-ready recovery: %v", err)
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query post-recovery Task: %v", err)
	}
	if view.TaskRevision != 43 || view.ActivityGeneration != 4 || view.SafetyEpoch != 3 ||
		view.OperationalMode != taskorchestration.OperationalFullReady ||
		view.EvidenceDiagnosticCount != 1 {
		t.Fatal("post-recovery state did not fence old evidence or resume safely")
	}
}

func TestRecoveryFenceProjectsGloballyWithoutAdvancingUnrelatedTasks(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 35, 0, 0, time.UTC)
	fencedTask := taskID(t, "task-recovery-global-fence")
	unrelatedTask := taskID(t, "task-recovery-unrelated")
	newTask := taskID(t, "task-recovery-new")
	fencedOwner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-recovery-global-fence"),
		taskorchestration.AuthorizationGeneration(1),
	)
	unrelatedOwner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-recovery-unrelated"),
		taskorchestration.AuthorizationGeneration(1),
	)
	newOwner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-recovery-new"),
		taskorchestration.AuthorizationGeneration(1),
	)
	recoveryAuthority := taskorchestration.NewRecoveryAuthority(
		authorityID(t, "recovery-authority-global-fence"),
		taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Recovery: taskorchestration.HarnessRecoveryFixture{
			Authority: recoveryAuthority, Generation: 1, Fence: 1, SafetyEpoch: 1,
			Mode: taskorchestration.OperationalFullReady,
		},
		Tasks: []taskorchestration.HarnessTaskFixture{
			{
				TaskID: fencedTask, Owner: fencedOwner, TaskRevision: 4,
				ActivityGeneration: 2, SafetyEpoch: 1,
			},
			{
				TaskID: unrelatedTask, Owner: unrelatedOwner, TaskRevision: 9,
				ActivityGeneration: 3, SafetyEpoch: 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	staleHeader := intentHeader(t, "request-recovery-pre-fence-work", unrelatedTask.String(), now)
	staleHeader.ExpectedTaskRevision = 9
	staleHeader.ActivityGeneration = 3
	preFenceIntent := taskorchestration.NewSubmitConfirmationGateIntent(
		staleHeader,
		unrelatedOwner,
		gateID(t, "gate-recovery-pre-fence-work"),
		payloadDigest(t, "abababababababababababababababababababababababababababababababab"),
	)

	fenceHeader := intentHeader(t, "request-recovery-global-fence", fencedTask.String(), now)
	fenceHeader.ExpectedTaskRevision = 4
	fenceHeader.ActivityGeneration = 2
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewApplyOperationalFenceIntent(
			fenceHeader,
			recoveryAuthority,
			taskorchestration.OperationalFenceBinding{
				Generation: 2, Fence: 2, SafetyEpoch: 2,
				Mode: taskorchestration.OperationalFullReady,
			},
		),
	); err != nil {
		t.Fatalf("apply global recovery fence: %v", err)
	}

	unrelatedView, err := harness.Queries.Query(
		context.Background(),
		taskorchestration.TaskQuery{
			TaskID:    unrelatedTask,
			Authority: taskorchestration.NewUserQueryAuthority(unrelatedOwner),
		},
	)
	if err != nil {
		t.Fatalf("query unrelated Task: %v", err)
	}
	if unrelatedView.TaskRevision != 9 || unrelatedView.ActivityGeneration != 4 ||
		unrelatedView.DecisionCount != 0 ||
		unrelatedView.SafetyEpoch != 2 {
		t.Fatal("global recovery fence rewrote unrelated Task authority instead of projecting it")
	}
	_, err = harness.Mutations.Decide(context.Background(), preFenceIntent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
		t.Fatalf("pre-recovery work error = %T, want stale authority", err)
	}

	newHeader := intentHeader(t, "request-recovery-new-task", newTask.String(), now.Add(time.Second))
	created, err := harness.Mutations.Decide(
		context.Background(), taskorchestration.NewStartTaskIntent(newHeader, newOwner),
	)
	if err != nil {
		t.Fatalf("create Task after global recovery fence: %v", err)
	}
	if created.TaskProjection.SafetyEpoch != 2 {
		t.Fatalf(
			"new Task safety epoch = %d, want current global epoch 2",
			created.TaskProjection.SafetyEpoch,
		)
	}
}

func TestResponseLossReconciliationRedeliversTheOriginalOperationOnly(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 40, 0, 0, time.UTC)
	task := taskID(t, "task-response-loss-reconciliation")
	phaseRun := phaseRunID(t, "phase-run-response-loss-reconciliation")
	runtimeRun := runtimeRunID(t, "runtime-run-response-loss-reconciliation")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-response-loss-reconciliation"),
		taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-response-loss-reconciliation"),
		taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-response-loss-reconciliation"),
		taskorchestration.AuthorizationGeneration(1),
	)
	reconciler := taskorchestration.NewWorkerAuthority(
		authorityID(t, "worker-authority-response-loss-reconciliation"),
		taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, Reconciler: reconciler,
			TaskRevision: 50, ActivityGeneration: 1, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 1, Fence: 1, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: operationID(t, "operation-runtime-response-loss-reconciliation"),
				PhaseRunID:  phaseRun, RuntimeRunID: runtimeRun, Authority: runtimeAuthority,
				Generation: 1, Fence: 1, SafetyEpoch: 1,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: operationID(t, "operation-lifecycle-response-loss-reconciliation"),
				PhaseRunID:  phaseRun, Authority: lifecycleAuthority,
				Generation: 1, Fence: 1, SafetyEpoch: 1,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	cancelHeader := intentHeader(t, "request-response-loss-cancel", task.String(), now)
	cancelHeader.ExpectedTaskRevision = 50
	cancelIntent := taskorchestration.NewCancelTaskByUserIntent(
		cancelHeader, owner, taskorchestration.CancelReasonUserRequested,
	)
	harness.LoseNextResponse()
	if _, err := harness.Mutations.Decide(context.Background(), cancelIntent); err == nil {
		t.Fatal("lost cancellation response did not remain ambiguous")
	} else {
		var decisionError *taskorchestration.Error
		if !errors.As(err, &decisionError) ||
			decisionError.Code() != taskorchestration.ErrorReconciliationRequired {
			t.Fatalf("lost cancellation response error = %T, want reconciliation required", err)
		}
	}
	deliveryRetryHeader := cancelHeader
	deliveryRetryHeader.Metadata.Transport.DeliveryAttempt = 2
	deliveryRetryHeader.Metadata.Transport.Deadline = now.Add(time.Minute)
	cancelled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			deliveryRetryHeader, owner, taskorchestration.CancelReasonUserRequested,
		),
	)
	if err != nil {
		t.Fatalf("exact replay lost cancellation response: %v", err)
	}
	if len(cancelled.EnactmentRefs) != 2 {
		t.Fatalf("replayed cancellation enactments = %d, want 2", len(cancelled.EnactmentRefs))
	}
	originalOperation := cancelled.EnactmentRefs[0].OperationID

	reconcileHeader := intentHeader(t, "request-response-loss-reconcile", task.String(), now.Add(time.Second))
	reconcileHeader.ExpectedTaskRevision = 51
	reconcileHeader.ActivityGeneration = 2
	reconcileIntent := taskorchestration.NewReconcileEnactmentIntent(
		reconcileHeader, reconciler, originalOperation, taskorchestration.ReconciliationFence(1),
	)
	harness.LoseNextResponse()
	if _, err := harness.Mutations.Decide(context.Background(), reconcileIntent); err == nil {
		t.Fatal("lost reconciliation response did not remain ambiguous")
	} else {
		var decisionError *taskorchestration.Error
		if !errors.As(err, &decisionError) ||
			decisionError.Code() != taskorchestration.ErrorReconciliationRequired {
			t.Fatalf("lost reconciliation response error = %T, want reconciliation required", err)
		}
	}
	reconcileDeliveryRetryHeader := reconcileHeader
	reconcileDeliveryRetryHeader.Metadata.Transport.DeliveryAttempt = 2
	reconcileDeliveryRetryHeader.Metadata.Transport.Deadline = now.Add(2 * time.Minute)
	reconciled, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewReconcileEnactmentIntent(
			reconcileDeliveryRetryHeader,
			reconciler,
			originalOperation,
			taskorchestration.ReconciliationFence(1),
		),
	)
	if err != nil {
		t.Fatalf("exact replay reconciliation: %v", err)
	}
	if len(reconciled.EnactmentRefs) != 1 ||
		reconciled.EnactmentRefs[0].OperationID != originalOperation {
		t.Fatal("reconciliation did not redeliver the original OperationID")
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query response-loss reconciliation: %v", err)
	}
	if view.TaskRevision != 52 || view.DecisionCount != 2 || view.EnactmentCount != 2 ||
		view.PhaseRunCount != 1 || view.RuntimeRunCount != 1 ||
		view.CancellationState != taskorchestration.CancellationCancelling {
		t.Fatal("response-loss reconciliation created a new business attempt or operation")
	}
}

func TestConcurrentConfirmCancelAndRetryHaveOneRevisionWinner(t *testing.T) {
	now := time.Date(2026, time.July, 26, 16, 50, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-concurrent-decisions"),
		taskorchestration.AuthorizationGeneration(1),
	)
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "request-concurrent-start", "task-concurrent-decisions", now), authority,
		),
	); err != nil {
		t.Fatalf("start Task: %v", err)
	}
	header := func(request string) taskorchestration.IntentHeader {
		value := intentHeader(t, request, "task-concurrent-decisions", now.Add(time.Second))
		value.ExpectedTaskRevision = 1
		return value
	}
	intents := []taskorchestration.TransitionIntent{
		taskorchestration.NewSubmitConfirmationGateIntent(
			header("request-concurrent-confirm"),
			authority,
			gateID(t, "gate-concurrent"),
			payloadDigest(t, "4444444444444444444444444444444444444444444444444444444444444444"),
		),
		taskorchestration.NewCancelTaskByUserIntent(
			header("request-concurrent-cancel"), authority, taskorchestration.CancelReasonUserRequested,
		),
		taskorchestration.NewRetryPhaseIntent(
			header("request-concurrent-retry"), authority, phaseRunID(t, "phase-run-concurrent"),
		),
	}
	type result struct {
		decision taskorchestration.TransitionDecision
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, len(intents))
	var waitGroup sync.WaitGroup
	for _, intent := range intents {
		waitGroup.Add(1)
		go func(intent taskorchestration.TransitionIntent) {
			defer waitGroup.Done()
			<-start
			decision, err := harness.Mutations.Decide(context.Background(), intent)
			results <- result{decision: decision, err: err}
		}(intent)
	}
	close(start)
	waitGroup.Wait()
	close(results)

	winners := 0
	stale := 0
	for result := range results {
		if result.err == nil {
			winners++
			if result.decision.PreviousTaskRevision != 1 || result.decision.AcceptedTaskRevision != 2 {
				t.Fatal("concurrent winner committed outside the expected revision")
			}
			continue
		}
		var decisionError *taskorchestration.Error
		if errors.As(result.err, &decisionError) &&
			decisionError.Code() == taskorchestration.ErrorStaleTaskRevision {
			stale++
			continue
		}
		t.Fatalf("concurrent loser error = %T, want stale revision", result.err)
	}
	if winners != 1 || stale != 2 {
		t.Fatalf("concurrent outcomes = %d winners, %d stale; want 1, 2", winners, stale)
	}
	view, err := harness.Queries.Query(
		context.Background(), taskQuery(t, "task-concurrent-decisions", "user-authority-concurrent-decisions"),
	)
	if err != nil {
		t.Fatalf("query concurrent decisions: %v", err)
	}
	if view.TaskRevision != 2 || view.DecisionCount != 2 {
		t.Fatal("concurrent decisions committed more than one expected-revision winner")
	}
}

func TestRuntimeEvidenceRejectsCrossScopeAndUnauthorizedBindings(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 0, 0, 0, time.UTC)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-evidence-scope"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-evidence-scope"), taskorchestration.AuthorizationGeneration(1),
	)
	unauthorizedRuntime := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-evidence-unauthorized"),
		taskorchestration.AuthorizationGeneration(1),
	)
	staleRuntimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-evidence-scope"), taskorchestration.AuthorizationGeneration(2),
	)
	taskA := taskID(t, "task-evidence-scope-a")
	phaseA := phaseRunID(t, "phase-run-evidence-scope-a")
	runtimeA := runtimeRunID(t, "runtime-run-evidence-scope-a")
	operationA := operationID(t, "operation-evidence-scope-a")
	taskB := taskID(t, "task-evidence-scope-b")
	phaseB := phaseRunID(t, "phase-run-evidence-scope-b")
	runtimeB := runtimeRunID(t, "runtime-run-evidence-scope-b")
	operationB := operationID(t, "operation-evidence-scope-b")
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{
			{
				TaskID: taskA, Owner: owner, TaskRevision: 6, ActivityGeneration: 2, SafetyEpoch: 3,
				PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
					PhaseRunID: phaseA, Generation: 4, Fence: 5, Active: true,
				}},
				RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
					OperationID: operationA, PhaseRunID: phaseA, RuntimeRunID: runtimeA,
					Authority: runtimeAuthority, Generation: 7, Fence: 8, SafetyEpoch: 3,
				}},
			},
			{
				TaskID: taskB, Owner: owner, TaskRevision: 1, ActivityGeneration: 2, SafetyEpoch: 3,
				PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
					PhaseRunID: phaseB, Generation: 4, Fence: 5, Active: true,
				}},
				RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
					OperationID: operationB, PhaseRunID: phaseB, RuntimeRunID: runtimeB,
					Authority: runtimeAuthority, Generation: 7, Fence: 8, SafetyEpoch: 3,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	tests := []struct {
		name       string
		authority  taskorchestration.RuntimeAuthority
		phaseRun   taskorchestration.PhaseRunID
		runtimeRun taskorchestration.RuntimeRunID
		operation  taskorchestration.OperationID
		wantCode   taskorchestration.ErrorCode
	}{
		{"cross Task operation", runtimeAuthority, phaseB, runtimeB, operationB, taskorchestration.ErrorEvidenceScopeConflict},
		{"cross Phase Run", runtimeAuthority, phaseB, runtimeA, operationA, taskorchestration.ErrorEvidenceScopeConflict},
		{"cross Operation", runtimeAuthority, phaseA, runtimeA, operationID(t, "operation-evidence-scope-other"), taskorchestration.ErrorEvidenceScopeConflict},
		{"cross Runtime Run", runtimeAuthority, phaseA, runtimeB, operationA, taskorchestration.ErrorEvidenceScopeConflict},
		{"stale producer generation", staleRuntimeAuthority, phaseA, runtimeA, operationA, taskorchestration.ErrorStaleAuthority},
		{"unauthorized producer", unauthorizedRuntime, phaseA, runtimeA, operationA, taskorchestration.ErrorAuthorizationDenied},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := intentHeader(t, "request-evidence-scope-"+string(rune('a'+index)), taskA.String(), now)
			header.ExpectedTaskRevision = 6
			header.ActivityGeneration = 2
			_, err := harness.Mutations.Decide(
				context.Background(),
				taskorchestration.NewAcceptRuntimeEvidenceIntent(
					header,
					test.authority,
					taskorchestration.RuntimeEvidenceBinding{
						Evidence: taskorchestration.NewEvidenceRef(
							evidenceID(t, "runtime-evidence-scope-"+string(rune('a'+index))),
							taskorchestration.EvidenceRuntime,
							evidenceDigest(t, "5555555555555555555555555555555555555555555555555555555555555555"),
						),
						PhaseRunID: test.phaseRun, PhaseRunGeneration: 4, PhaseRunFence: 5,
						RuntimeRunID: test.runtimeRun, OperationID: test.operation,
						Generation: 7, Fence: 8, SafetyEpoch: 3,
					},
				),
			)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) || decisionError.Code() != test.wantCode {
				t.Fatalf("evidence rejection = %T, want %v", err, test.wantCode)
			}
		})
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: taskA, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query after rejected evidence: %v", err)
	}
	if view.TaskRevision != 6 || view.DecisionCount != 0 || view.EnactmentCount != 0 ||
		view.EvidenceDiagnosticCount != uint64(len(tests)) {
		t.Fatal("rejected cross-scope evidence changed authoritative Task state")
	}
}

func TestOtherEvidenceFamiliesRejectUnboundScopeGenerationFenceAndAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 5, 0, 0, time.UTC)
	task := taskID(t, "task-other-evidence-fencing")
	phaseRun := phaseRunID(t, "phase-run-other-evidence-fencing")
	otherPhaseRun := phaseRunID(t, "phase-run-other-evidence-unbound")
	publicationOperation := operationID(t, "operation-publication-evidence-fencing")
	schedulingOperation := operationID(t, "operation-scheduling-evidence-fencing")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-other-evidence-fencing"),
		taskorchestration.AuthorizationGeneration(1),
	)
	validatorAuthority := taskorchestration.NewValidatorAuthority(
		authorityID(t, "validator-authority-evidence-fencing"),
		taskorchestration.AuthorizationGeneration(1),
	)
	publicationAuthority := taskorchestration.NewPublicationAuthority(
		authorityID(t, "publication-authority-evidence-fencing"),
		taskorchestration.AuthorizationGeneration(1),
	)
	schedulerAuthority := taskorchestration.NewSchedulerAuthority(
		authorityID(t, "scheduler-authority-evidence-fencing"),
		taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 12, ActivityGeneration: 4, SafetyEpoch: 3,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 5, Fence: 6, Active: true,
			}},
			ValidationBindings: []taskorchestration.HarnessValidationBindingFixture{{
				PhaseRunID: phaseRun, Authority: validatorAuthority,
				Generation: 7, Fence: 8, SafetyEpoch: 3,
			}},
			PublicationOperations: []taskorchestration.HarnessPublicationOperationFixture{{
				OperationID: publicationOperation, PhaseRunID: phaseRun,
				Authority: publicationAuthority, Generation: 9, Fence: 10, SafetyEpoch: 3,
			}},
			SchedulingOperations: []taskorchestration.HarnessSchedulingOperationFixture{{
				OperationID: schedulingOperation, PhaseRunID: phaseRun,
				Authority: schedulerAuthority, Generation: 11, Fence: 12, SafetyEpoch: 3,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	header := func(request string) taskorchestration.IntentHeader {
		value := intentHeader(t, request, task.String(), now)
		value.ExpectedTaskRevision = 12
		value.ActivityGeneration = 4
		return value
	}
	evidence := func(id string, kind taskorchestration.EvidenceKind) taskorchestration.EvidenceRef {
		return taskorchestration.NewEvidenceRef(
			evidenceID(t, id), kind,
			evidenceDigest(t, "5656565656565656565656565656565656565656565656565656565656565656"),
		)
	}

	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			header("request-validation-cross-phase"),
			validatorAuthority,
			taskorchestration.ValidationEvidenceBinding{
				Evidence:   evidence("validation-evidence-cross-phase", taskorchestration.EvidencePhaseValidation),
				PhaseRunID: otherPhaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
				Generation: 7, Fence: 8, SafetyEpoch: 3,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorEvidenceScopeConflict)

	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
			header("request-validation-stale-generation"),
			validatorAuthority,
			taskorchestration.ValidationEvidenceBinding{
				Evidence:   evidence("validation-evidence-stale-generation", taskorchestration.EvidencePhaseValidation),
				PhaseRunID: phaseRun, PhaseRunGeneration: 4, PhaseRunFence: 6,
				Generation: 7, Fence: 8, SafetyEpoch: 3,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorStaleAuthority)

	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPublicationEvidenceIntent(
			header("request-publication-cross-operation"),
			publicationAuthority,
			taskorchestration.PublicationEvidenceBinding{
				Evidence:   evidence("publication-evidence-cross-operation", taskorchestration.EvidencePublication),
				PhaseRunID: phaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
				OperationID: operationID(t, "operation-publication-evidence-unbound"),
				Generation:  9, Fence: 10, SafetyEpoch: 3,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorEvidenceScopeConflict)

	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptPublicationEvidenceIntent(
			header("request-publication-stale-fence"),
			publicationAuthority,
			taskorchestration.PublicationEvidenceBinding{
				Evidence:   evidence("publication-evidence-stale-fence", taskorchestration.EvidencePublication),
				PhaseRunID: phaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
				OperationID: publicationOperation, Generation: 9, Fence: 9, SafetyEpoch: 3,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorStaleAuthority)

	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptSchedulingEvidenceIntent(
			header("request-scheduling-stale-safety"),
			schedulerAuthority,
			taskorchestration.SchedulingEvidenceBinding{
				Evidence:   evidence("scheduling-evidence-stale-safety", taskorchestration.EvidenceScheduling),
				PhaseRunID: phaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
				OperationID: schedulingOperation, Generation: 11, Fence: 12, SafetyEpoch: 2,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorStaleAuthority)

	unauthorizedScheduler := taskorchestration.NewSchedulerAuthority(
		authorityID(t, "scheduler-authority-evidence-unauthorized"),
		taskorchestration.AuthorizationGeneration(1),
	)
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewAcceptSchedulingEvidenceIntent(
			header("request-scheduling-unauthorized"),
			unauthorizedScheduler,
			taskorchestration.SchedulingEvidenceBinding{
				Evidence:   evidence("scheduling-evidence-unauthorized", taskorchestration.EvidenceScheduling),
				PhaseRunID: phaseRun, PhaseRunGeneration: 5, PhaseRunFence: 6,
				OperationID: schedulingOperation, Generation: 11, Fence: 12, SafetyEpoch: 3,
			},
		),
	)
	assertDecisionErrorCode(t, err, taskorchestration.ErrorAuthorizationDenied)

	view, err := harness.Queries.Query(
		context.Background(),
		taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query after rejected evidence: %v", err)
	}
	if view.TaskRevision != 12 || view.DecisionCount != 0 || view.EvidenceDiagnosticCount != 6 {
		t.Fatal("rejected evidence changed authoritative Task state or omitted diagnostics")
	}
}

func TestLifecycleEvidenceRejectsStaleGenerationFenceAndSafetyEpoch(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 10, 0, 0, time.UTC)
	task := taskID(t, "task-lifecycle-stale")
	phaseRun := phaseRunID(t, "phase-run-lifecycle-stale")
	operation := operationID(t, "operation-lifecycle-stale")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-lifecycle-stale"), taskorchestration.AuthorizationGeneration(1),
	)
	lifecycleAuthority := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "lifecycle-authority-stale"), taskorchestration.AuthorizationGeneration(1),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, TaskRevision: 14, ActivityGeneration: 2, SafetyEpoch: 5,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 4, Fence: 6, Active: true,
			}},
			LifecycleOperations: []taskorchestration.HarnessLifecycleOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, Authority: lifecycleAuthority,
				Generation: 8, Fence: 10, SafetyEpoch: 5,
				Purpose: taskorchestration.LifecycleOperationCommit,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	tests := []struct {
		name       string
		generation taskorchestration.TaskWorkspaceLifecycleGeneration
		fence      taskorchestration.TaskWorkspaceLifecycleFence
		safety     taskorchestration.SafetyEpoch
	}{
		{"C04 generation", 7, 10, 5},
		{"C04 fence", 8, 9, 5},
		{"out-of-order future C04 fence", 8, 11, 5},
		{"safety epoch", 8, 10, 4},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := intentHeader(t, "request-lifecycle-stale-"+string(rune('a'+index)), task.String(), now)
			header.ExpectedTaskRevision = 14
			header.ActivityGeneration = 2
			_, err := harness.Mutations.Decide(
				context.Background(),
				taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
					header,
					lifecycleAuthority,
					taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
						Evidence: taskorchestration.NewEvidenceRef(
							evidenceID(t, "lifecycle-evidence-stale-"+string(rune('a'+index))),
							taskorchestration.EvidenceTaskWorkspaceLifecycle,
							evidenceDigest(t, "6666666666666666666666666666666666666666666666666666666666666666"),
						),
						PhaseRunID: phaseRun, PhaseRunGeneration: 4, PhaseRunFence: 6,
						OperationID: operation, Generation: test.generation, Fence: test.fence,
						SafetyEpoch: test.safety, Outcome: taskorchestration.LifecycleEvidenceCommitted,
						RevisionID:   taskWorkspaceRevisionID(t, "workspace-revision-lifecycle-stale"),
						CheckpointID: checkpointID(t, "checkpoint-lifecycle-stale"),
					},
				),
			)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorStaleAuthority {
				t.Fatalf("stale lifecycle binding error = %T, want stale authority", err)
			}
		})
	}
	view, err := harness.Queries.Query(
		context.Background(), taskorchestration.TaskQuery{
			TaskID: task, Authority: taskorchestration.NewUserQueryAuthority(owner),
		},
	)
	if err != nil {
		t.Fatalf("query stale lifecycle evidence: %v", err)
	}
	if view.TaskRevision != 14 || view.DecisionCount != 0 ||
		view.EvidenceDiagnosticCount != uint64(len(tests)) {
		t.Fatal("stale lifecycle evidence changed authoritative Task state")
	}
}

func TestDecisionRequestIdentityIsScopedToItsTask(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 20, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-request-scope"), taskorchestration.AuthorizationGeneration(1),
	)
	first, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "shared-request-scope", "task-request-scope-a", now), authority,
		),
	)
	if err != nil {
		t.Fatalf("commit first Task-scoped request: %v", err)
	}
	second, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "shared-request-scope", "task-request-scope-b", now), authority,
		),
	)
	if err != nil {
		t.Fatalf("commit second Task-scoped request: %v", err)
	}
	if first.DecisionID == second.DecisionID || first.TaskProjection.TaskID == second.TaskProjection.TaskID {
		t.Fatal("Task-scoped DecisionRequestID collapsed two Task decisions")
	}
}

func TestDecisionRequestScopeDoesNotDiscloseKeysAcrossAuthorities(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 25, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-request-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	foreign := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-request-foreign"), taskorchestration.AuthorizationGeneration(1),
	)
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "shared-authority-request", "task-authority-request", now), owner,
		),
	); err != nil {
		t.Fatalf("commit owner request: %v", err)
	}
	foreignHeader := intentHeader(t, "shared-authority-request", "task-authority-request", now.Add(time.Second))
	foreignHeader.ExpectedTaskRevision = 1
	_, err = harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			foreignHeader, foreign, taskorchestration.CancelReasonUserRequested,
		),
	)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
		t.Fatalf("foreign request-scope error = %T, want authorization denial", err)
	}
}

func TestRetryIdentitiesRemainTypedByOwningRetryLayer(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(phaseRunID(t, "retry-identity")),
		reflect.TypeOf(runtimeRunID(t, "retry-identity")),
		reflect.TypeOf(operationID(t, "retry-identity")),
		reflect.TypeOf(taskworkspace.CleanupRetryGeneration(1)),
	}
	for left := range types {
		for right := left + 1; right < len(types); right++ {
			if types[left] == types[right] {
				t.Fatal("business, Runtime, delivery, or Cleanup Debt retry identities share a type")
			}
		}
	}
}

func TestPostCommitFaultsRecoverByExactReplay(t *testing.T) {
	now := time.Date(2026, time.July, 26, 17, 30, 0, 0, time.UTC)
	tests := []struct {
		name         string
		fault        taskorchestration.FaultPoint
		crash        taskorchestration.CrashBoundary
		needsRestart bool
	}{
		{name: "after commit fault", fault: taskorchestration.FaultAfterCommit},
		{name: "before response fault", fault: taskorchestration.FaultBeforeResponse},
		{name: "after commit crash", crash: taskorchestration.CrashAfterCommit, needsRestart: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness, err := taskorchestration.NewDeterministicHarness(
				taskorchestration.HarnessConfig{Now: now},
			)
			if err != nil {
				t.Fatalf("create deterministic harness: %v", err)
			}
			if test.fault != 0 {
				if err := harness.FailNextAt(test.fault); err != nil {
					t.Fatalf("configure fault: %v", err)
				}
			} else if err := harness.CrashNextAt(test.crash); err != nil {
				t.Fatalf("configure crash: %v", err)
			}
			intent := taskorchestration.NewStartTaskIntent(
				intentHeader(t, "request-post-commit-replay", "task-post-commit-replay", now),
				taskorchestration.NewUserAuthority(
					authorityID(t, "user-authority-post-commit-replay"),
					taskorchestration.AuthorizationGeneration(1),
				),
			)
			_, err = harness.Mutations.Decide(context.Background(), intent)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) ||
				decisionError.Code() != taskorchestration.ErrorReconciliationRequired {
				t.Fatalf("post-commit fault error = %T, want reconciliation required", err)
			}
			if test.needsRestart {
				harness = harness.Restart()
			}
			replayed, err := harness.Mutations.Decide(context.Background(), intent)
			if err != nil {
				t.Fatalf("exact replay after post-commit fault: %v", err)
			}
			if replayed.DecisionID.String() != "decision-000001" ||
				replayed.AcceptedTaskRevision != 1 {
				t.Fatal("post-commit replay allocated a new Decision or revision")
			}
		})
	}
}

func assertDecisionErrorCode(
	t *testing.T,
	err error,
	want taskorchestration.ErrorCode,
) {
	t.Helper()
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != want {
		t.Fatalf("decision error = %T, want %v", err, want)
	}
}
