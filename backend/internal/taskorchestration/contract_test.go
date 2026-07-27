package taskorchestration_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestTaskOrchestrationPublicMutationSeamIsOnlyDecide(t *testing.T) {
	mutation := reflect.TypeOf((*taskorchestration.TaskOrchestration)(nil)).Elem()
	if mutation.NumMethod() != 1 || mutation.Method(0).Name != "Decide" {
		t.Fatalf("Task Orchestration gained another public mutation operation: %v", mutation)
	}
	query := reflect.TypeOf((*taskorchestration.TaskOrchestrationQuery)(nil)).Elem()
	if query.NumMethod() != 1 || query.Method(0).Name != "Query" {
		t.Fatalf("Task Orchestration query seam is not independently read-only: %v", query)
	}
}

func TestDecideCommitsThroughTheTaskOrchestrationSeam(t *testing.T) {
	startedAt := time.Date(2026, time.July, 26, 9, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: startedAt,
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}

	intent := minimalPinnedStartIntent(t,
		intentHeader(t, "decision-request-1", "task-1", startedAt),
		taskorchestration.NewUserAuthority(
			authorityID(t, "user-authority-1"),
			taskorchestration.AuthorizationGeneration(1),
		),
	)
	const canonicalGolden = "726c9c7dbbccfa56a86f1e767c01bf95fe4f6986d3c7606f8fce0d3d77d1680e"
	if intent.Header().CanonicalRequestDigest.String() != canonicalGolden {
		t.Fatalf("intent canonical digest = %s, want schema-v1 golden %s", intent.Header().CanonicalRequestDigest.String(), canonicalGolden)
	}
	decision, err := harness.Mutations.Decide(context.Background(), intent)
	if err != nil {
		t.Fatalf("decide start intent: %v", err)
	}

	if decision.DecisionID.String() != "decision-000001" {
		t.Fatalf("DecisionID = %q, want deterministic first identity", decision.DecisionID.String())
	}
	if decision.DecisionRequestID != intent.Header().DecisionRequestID {
		t.Fatal("decision omitted its request identity")
	}
	if decision.CanonicalRequestDigest.String() != canonicalGolden {
		t.Fatal("decision did not commit the schema-v1 canonical golden digest")
	}
	if decision.PreviousTaskRevision != 0 || decision.AcceptedTaskRevision != 1 {
		t.Fatalf(
			"accepted revision = (%d, %d), want (0, 1)",
			decision.PreviousTaskRevision,
			decision.AcceptedTaskRevision,
		)
	}
	if decision.TaskProjection.TaskID != intent.Header().TaskID ||
		decision.TaskProjection.TaskRevision != decision.AcceptedTaskRevision ||
		decision.TaskProjection.ActivityGeneration != 1 ||
		decision.TaskProjection.ExecutionLockID.String() == "" ||
		decision.TaskProjection.TemplateLockID.String() == "" {
		t.Fatal("decision omitted its committed pinned Task projection")
	}
	if decision.CommittedAt != startedAt {
		t.Fatalf("commit time = %s, want controlled time", decision.CommittedAt)
	}
	if len(decision.EnactmentRefs) != 0 {
		t.Fatal("pinned Start created work before availability")
	}
	if len(decision.AffectedPhaseRuns) != 0 || len(decision.AcceptedEvidenceRefs) != 0 {
		t.Fatal("pinned Start invented Phase Run or evidence changes")
	}
	if decision.MandatoryAuditFactRef.AuditFactID.String() != "audit-fact-000001" {
		t.Fatal("decision omitted its deterministic mandatory audit fact")
	}
}

func TestDecideFailsClosedForUnknownSchemaMajor(t *testing.T) {
	now := time.Date(2026, time.July, 26, 10, 30, 0, 0, time.UTC)
	header := intentHeader(t, "decision-request-unknown", "task-unknown", now)
	header.SchemaVersion = taskorchestration.NewSchemaVersion(2, 0)
	intent := minimalPinnedStartIntent(t,
		header,
		taskorchestration.NewUserAuthority(
			authorityID(t, "user-authority-unknown"),
			taskorchestration.AuthorizationGeneration(1),
		),
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}

	_, err = harness.Mutations.Decide(context.Background(), intent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) {
		t.Fatalf("unknown schema error = %T, want closed Task Orchestration error", err)
	}
	if decisionError.Code() != taskorchestration.ErrorUnsupportedSchema {
		t.Fatalf("error code = %v, want unsupported schema", decisionError.Code())
	}
	if decisionError.RetryDisposition() != taskorchestration.RetryNever {
		t.Fatalf("retry disposition = %v, want never", decisionError.RetryDisposition())
	}
	if decisionError.ReconciliationDisposition() != taskorchestration.ReconciliationNotRequired {
		t.Fatalf("reconciliation disposition = %v, want not required", decisionError.ReconciliationDisposition())
	}
	for _, submitted := range []string{"decision-request-unknown", "task-unknown", "user-authority-unknown"} {
		if strings.Contains(err.Error(), submitted) {
			t.Fatal("safe error disclosed a submitted identity")
		}
	}
}

func TestClosedIntentFamiliesCarryExactlyOneTypedAuthority(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 0, 0, 0, time.UTC)
	header := func(request string) taskorchestration.IntentHeader {
		return intentHeader(t, request, "task-1", now)
	}
	user := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	administrator := taskorchestration.NewAdministratorAuthority(
		authorityID(t, "administrator-authority-1"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.AdministratorReasonSafety,
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "worker-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	validator := taskorchestration.NewValidatorAuthority(
		authorityID(t, "validator-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	taskWorkspaceLifecycle := taskorchestration.NewTaskWorkspaceLifecycleAuthority(
		authorityID(t, "task-workspace-lifecycle-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	publication := taskorchestration.NewPublicationAuthority(
		authorityID(t, "publication-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	scheduler := taskorchestration.NewSchedulerAuthority(
		authorityID(t, "scheduler-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	recovery := taskorchestration.NewRecoveryAuthority(
		authorityID(t, "recovery-authority-1"), taskorchestration.AuthorizationGeneration(1),
	)
	phaseRunID := phaseRunID(t, "phase-run-1")
	runtimeRunID := runtimeRunID(t, "runtime-run-1")
	operationID := operationID(t, "operation-1")
	runtimeEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "evidence-1"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, strings.Repeat("c", 64)),
	)
	validationEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "evidence-2"),
		taskorchestration.EvidencePhaseValidation,
		evidenceDigest(t, strings.Repeat("d", 64)),
	)
	taskWorkspaceLifecycleEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "evidence-3"),
		taskorchestration.EvidenceTaskWorkspaceLifecycle,
		evidenceDigest(t, strings.Repeat("e", 64)),
	)
	publicationEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "evidence-4"),
		taskorchestration.EvidencePublication,
		evidenceDigest(t, strings.Repeat("f", 64)),
	)
	schedulingEvidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "evidence-5"),
		taskorchestration.EvidenceScheduling,
		evidenceDigest(t, strings.Repeat("1", 64)),
	)

	tests := []struct {
		name      string
		kind      taskorchestration.IntentKind
		authority taskorchestration.AuthorityKind
		intent    taskorchestration.TransitionIntent
	}{
		{"start", taskorchestration.IntentStartTask, taskorchestration.AuthorityUser,
			minimalPinnedStartIntent(t, header("request-start"), user)},
		{"work available", taskorchestration.IntentMakeWorkAvailable, taskorchestration.AuthorityWorker,
			taskorchestration.NewMakeWorkAvailableIntent(header("request-work"), worker, operationID)},
		{"confirmation", taskorchestration.IntentSubmitConfirmationGate, taskorchestration.AuthorityUser,
			taskorchestration.NewSubmitConfirmationGateIntent(
				header("request-confirm"), user, gateID(t, "gate-1"), payloadDigest(t, strings.Repeat("a", 64)),
			)},
		{"retry", taskorchestration.IntentRetryPhase, taskorchestration.AuthorityUser,
			taskorchestration.NewRetryPhaseIntent(header("request-retry"), user, phaseRunID)},
		{"cancel", taskorchestration.IntentCancelTask, taskorchestration.AuthorityAdministrator,
			taskorchestration.NewCancelTaskByAdministratorIntent(
				header("request-cancel"), administrator, taskorchestration.CancelReasonSafety,
			)},
		{"manual edit", taskorchestration.IntentBeginManualEdit, taskorchestration.AuthorityUser,
			taskorchestration.NewBeginManualEditIntent(
				header("request-edit"), user, artifactVersionID(t, "artifact-version-1"),
			)},
		{"runtime evidence", taskorchestration.IntentAcceptRuntimeEvidence, taskorchestration.AuthorityRuntime,
			taskorchestration.NewAcceptRuntimeEvidenceIntent(
				header("request-runtime"), runtimeAuthority,
				taskorchestration.RuntimeEvidenceBinding{
					Evidence: runtimeEvidence, PhaseRunID: phaseRunID, RuntimeRunID: runtimeRunID,
					PhaseRunGeneration: 1, PhaseRunFence: 1,
					OperationID: operationID, Generation: 1, Fence: 1, SafetyEpoch: 1,
				},
			)},
		{"validation evidence", taskorchestration.IntentAcceptPhaseValidationEvidence, taskorchestration.AuthorityValidator,
			taskorchestration.NewAcceptPhaseValidationEvidenceIntent(
				header("request-validation"), validator,
				taskorchestration.ValidationEvidenceBinding{
					Evidence: validationEvidence, PhaseRunID: phaseRunID,
					PhaseRunGeneration: 1, PhaseRunFence: 1,
					Generation: 1, Fence: 1, SafetyEpoch: 1,
				},
			)},
		{"Task Workspace lifecycle evidence", taskorchestration.IntentAcceptTaskWorkspaceLifecycleEvidence, taskorchestration.AuthorityTaskWorkspaceLifecycle,
			taskorchestration.NewAcceptTaskWorkspaceLifecycleEvidenceIntent(
				header("request-task-workspace-lifecycle"), taskWorkspaceLifecycle,
				taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{
					Evidence: taskWorkspaceLifecycleEvidence, PhaseRunID: phaseRunID, OperationID: operationID,
					PhaseRunGeneration: 1, PhaseRunFence: 1,
					Generation: 1, Fence: 1, ObservedGeneration: 1, ObservedFence: 2,
					SafetyEpoch:  1,
					Outcome:      taskorchestration.LifecycleEvidenceCommitted,
					RevisionID:   taskWorkspaceRevisionID(t, "workspace-revision-contract"),
					CheckpointID: checkpointID(t, "checkpoint-contract"),
				},
			)},
		{"publication evidence", taskorchestration.IntentAcceptPublicationEvidence, taskorchestration.AuthorityPublication,
			taskorchestration.NewAcceptPublicationEvidenceIntent(
				header("request-publication"), publication,
				taskorchestration.PublicationEvidenceBinding{
					Evidence: publicationEvidence, PhaseRunID: phaseRunID, OperationID: operationID,
					PhaseRunGeneration: 1, PhaseRunFence: 1,
					Generation: 1, Fence: 1, SafetyEpoch: 1,
				},
			)},
		{"scheduling evidence", taskorchestration.IntentAcceptSchedulingEvidence, taskorchestration.AuthorityScheduler,
			taskorchestration.NewAcceptSchedulingEvidenceIntent(
				header("request-scheduling"), scheduler,
				taskorchestration.SchedulingEvidenceBinding{
					Evidence: schedulingEvidence, PhaseRunID: phaseRunID, OperationID: operationID,
					PhaseRunGeneration: 1, PhaseRunFence: 1,
					Generation: 1, Fence: 1, SafetyEpoch: 1,
				},
			)},
		{"reconcile", taskorchestration.IntentReconcileEnactment, taskorchestration.AuthorityWorker,
			taskorchestration.NewReconcileEnactmentIntent(
				header("request-reconcile"), worker, operationID, taskorchestration.ReconciliationFence(1),
			)},
		{"operational fence", taskorchestration.IntentApplyOperationalFence, taskorchestration.AuthorityRecovery,
			taskorchestration.NewApplyOperationalFenceIntent(
				header("request-fence"), recovery,
				taskorchestration.OperationalFenceBinding{
					Generation: 1, Fence: 1, SafetyEpoch: 1,
					Mode: taskorchestration.OperationalReadOnly,
				},
			)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.intent.Kind() != test.kind {
				t.Fatalf("intent kind = %v, want %v", test.intent.Kind(), test.kind)
			}
			if test.intent.AuthorityKind() != test.authority {
				t.Fatalf("authority kind = %v, want %v", test.intent.AuthorityKind(), test.authority)
			}
			if test.intent.Header().CanonicalRequestDigest == (taskorchestration.CanonicalRequestDigest{}) {
				t.Fatal("accepted intent omitted its canonical digest")
			}
		})
	}
}

func TestEvidenceIntentRejectsAMismatchedTypedKind(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 15, 0, 0, time.UTC)
	intent := taskorchestration.NewAcceptRuntimeEvidenceIntent(
		intentHeader(t, "request-wrong-evidence", "task-wrong-evidence", now),
		taskorchestration.NewRuntimeAuthority(
			authorityID(t, "runtime-authority-wrong-evidence"),
			taskorchestration.AuthorizationGeneration(1),
		),
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: taskorchestration.NewEvidenceRef(
				evidenceID(t, "wrong-evidence"),
				taskorchestration.EvidencePublication,
				evidenceDigest(t, strings.Repeat("2", 64)),
			),
			PhaseRunID:   phaseRunID(t, "phase-run-wrong-evidence"),
			RuntimeRunID: runtimeRunID(t, "runtime-run-wrong-evidence"),
			OperationID:  operationID(t, "operation-wrong-evidence"),
			Generation:   1,
			Fence:        1,
		},
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}

	_, err = harness.Mutations.Decide(context.Background(), intent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) || decisionError.Code() != taskorchestration.ErrorEvidenceInvalid {
		t.Fatalf("mismatched evidence error = %T, want typed evidence rejection", err)
	}
}

func TestEvidenceDecisionReportsOnlyItsAcceptedTypedFacts(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 17, 0, 0, time.UTC)
	task := taskID(t, "task-accepted-evidence")
	phaseRun := phaseRunID(t, "phase-run-accepted-evidence")
	runtimeRun := runtimeRunID(t, "runtime-run-accepted-evidence")
	operation := operationID(t, "operation-accepted-evidence")
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-accepted-evidence"),
		taskorchestration.AuthorizationGeneration(1),
	)
	runtimeAuthority := taskorchestration.NewRuntimeAuthority(
		authorityID(t, "runtime-authority-accepted-evidence"),
		taskorchestration.AuthorizationGeneration(1),
	)
	evidence := taskorchestration.NewEvidenceRef(
		evidenceID(t, "accepted-runtime-evidence"),
		taskorchestration.EvidenceRuntime,
		evidenceDigest(t, strings.Repeat("3", 64)),
	)
	intent := taskorchestration.NewAcceptRuntimeEvidenceIntent(
		intentHeader(t, "request-accepted-evidence", task.String(), now),
		runtimeAuthority,
		taskorchestration.RuntimeEvidenceBinding{
			Evidence: evidence, PhaseRunID: phaseRun, PhaseRunGeneration: 1, PhaseRunFence: 1,
			RuntimeRunID: runtimeRun, OperationID: operation, Generation: 1, Fence: 1, SafetyEpoch: 1,
		},
	)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		Tasks: []taskorchestration.HarnessTaskFixture{{
			TaskID: task, Owner: owner, ActivityGeneration: 1, SafetyEpoch: 1,
			PhaseRuns: []taskorchestration.HarnessPhaseRunFixture{{
				PhaseRunID: phaseRun, Generation: 1, Fence: 1, Active: true,
			}},
			RuntimeOperations: []taskorchestration.HarnessRuntimeOperationFixture{{
				OperationID: operation, PhaseRunID: phaseRun, RuntimeRunID: runtimeRun,
				Authority: runtimeAuthority, Generation: 1, Fence: 1, SafetyEpoch: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	decision, err := harness.Mutations.Decide(context.Background(), intent)
	if err != nil {
		t.Fatalf("decide intent: %v", err)
	}
	if len(decision.AffectedPhaseRuns) != 1 || decision.AffectedPhaseRuns[0] != phaseRun {
		t.Fatal("evidence decision omitted its affected Phase Run")
	}
	if len(decision.AcceptedEvidenceRefs) != 1 || decision.AcceptedEvidenceRefs[0] != evidence {
		t.Fatal("evidence decision omitted its validated typed evidence reference")
	}
	if len(decision.EnactmentRefs) != 0 {
		t.Fatal("TO-01 evidence acceptance invented a downstream enactment")
	}
}

func TestQueryDeniesMissingAndForeignTasksWithoutDisclosingExistence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 20, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-query-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	other := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-query-other"), taskorchestration.AuthorizationGeneration(1),
	)
	task := taskID(t, "task-query-scope")

	_, missingErr := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    task,
		Authority: taskorchestration.NewUserQueryAuthority(other),
	})
	var missingDecisionError *taskorchestration.Error
	if !errors.As(missingErr, &missingDecisionError) ||
		missingDecisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
		t.Fatalf("missing Task query error = %T, want safe authorization denial", missingErr)
	}

	_, err = harness.Mutations.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "request-query-owner", "task-query-scope", now),
		owner,
	))
	if err != nil {
		t.Fatalf("decide owned Task: %v", err)
	}
	_, foreignErr := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    task,
		Authority: taskorchestration.NewUserQueryAuthority(other),
	})
	var foreignDecisionError *taskorchestration.Error
	if !errors.As(foreignErr, &foreignDecisionError) ||
		foreignDecisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
		t.Fatalf("foreign Task query error = %T, want safe authorization denial", foreignErr)
	}
	if missingErr.Error() != foreignErr.Error() {
		t.Fatal("query disclosed whether a Task exists outside the User authority")
	}

	if _, err := harness.Queries.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    task,
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}); err != nil {
		t.Fatalf("query owned Task: %v", err)
	}
}

func TestUserMutationDeniesMissingAndForeignTasksBeforeRevisionChecks(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 25, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-mutation-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	foreign := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-mutation-foreign"), taskorchestration.AuthorizationGeneration(1),
	)
	_, err = harness.Mutations.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "request-mutation-owner", "task-mutation-owned", now),
		owner,
	))
	if err != nil {
		t.Fatalf("decide owned Task: %v", err)
	}

	missingHeader := intentHeader(t, "request-mutation-missing", "task-mutation-missing", now)
	missingHeader.ExpectedTaskRevision = 99
	_, missingErr := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			missingHeader, foreign, taskorchestration.CancelReasonUserRequested,
		),
	)
	foreignHeader := intentHeader(t, "request-mutation-foreign", "task-mutation-owned", now)
	foreignHeader.ExpectedTaskRevision = 99
	_, foreignErr := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			foreignHeader, foreign, taskorchestration.CancelReasonUserRequested,
		),
	)

	for _, denied := range []error{missingErr, foreignErr} {
		var decisionError *taskorchestration.Error
		if !errors.As(denied, &decisionError) ||
			decisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
			t.Fatalf("unowned mutation error = %T, want safe authorization denial", denied)
		}
	}
	if missingErr.Error() != foreignErr.Error() {
		t.Fatal("mutation disclosed whether an unowned Task exists or which revision it has")
	}

	ownerHeader := intentHeader(t, "request-mutation-owner-cancel", "task-mutation-owned", now)
	ownerHeader.ExpectedTaskRevision = 1
	if _, err := harness.Mutations.Decide(
		context.Background(),
		taskorchestration.NewCancelTaskByUserIntent(
			ownerHeader, owner, taskorchestration.CancelReasonUserRequested,
		),
	); err != nil {
		t.Fatalf("decide owning User mutation: %v", err)
	}
}

func TestQueryIsReadOnlyAndDoesNotAllocateMutationFacts(t *testing.T) {
	now := time.Date(2026, time.July, 26, 11, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	query := taskQuery(t, "task-query-1", "user-authority-query")

	for range 3 {
		_, err := harness.Queries.Query(context.Background(), query)
		var decisionError *taskorchestration.Error
		if !errors.As(err, &decisionError) ||
			decisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
			t.Fatalf("query missing Task error = %T, want safe authorization denial", err)
		}
	}

	first := minimalPinnedStartIntent(t,
		intentHeader(t, "request-query-1", "task-query-1", now),
		taskorchestration.NewUserAuthority(
			authorityID(t, "user-authority-query"), taskorchestration.AuthorizationGeneration(1),
		),
	)
	firstDecision, err := harness.Mutations.Decide(context.Background(), first)
	if err != nil {
		t.Fatalf("decide after empty queries: %v", err)
	}
	if firstDecision.DecisionID.String() != "decision-000001" {
		t.Fatal("query consumed a deterministic decision identity")
	}
	view, err := harness.Queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query committed projection: %v", err)
	}
	if view.TaskRevision != 1 || view.DecisionCount != 1 || view.EnactmentCount != 0 ||
		view.LatestDecisionID != firstDecision.DecisionID {
		t.Fatal("query did not return exactly the committed shell projection")
	}

	secondHeader := intentHeader(t, "request-query-2", "task-query-1", now.Add(time.Second))
	secondHeader.ExpectedTaskRevision = 1
	second := minimalWorkIntent(t, secondHeader)
	secondDecision, err := harness.Mutations.Decide(context.Background(), second)
	if err != nil {
		t.Fatalf("decide after committed query: %v", err)
	}
	if secondDecision.DecisionID.String() != "decision-000002" || secondDecision.AcceptedTaskRevision != 2 {
		t.Fatal("query changed the next deterministic mutation facts")
	}
}

func TestHarnessRestartsWithDurableStateClockAndIDSequence(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	first := minimalPinnedStartIntent(t,
		intentHeader(t, "request-restart-1", "task-restart-1", now),
		taskorchestration.NewUserAuthority(
			authorityID(t, "user-authority-restart"), taskorchestration.AuthorizationGeneration(1),
		),
	)
	firstDecision, err := harness.Mutations.Decide(context.Background(), first)
	if err != nil {
		t.Fatalf("decide before restart: %v", err)
	}
	if err := harness.AdvanceClock(5 * time.Minute); err != nil {
		t.Fatalf("advance controlled clock: %v", err)
	}

	restarted := harness.Restart()
	view, err := restarted.Queries.Query(
		context.Background(),
		taskQuery(t, "task-restart-1", "user-authority-restart"),
	)
	if err != nil {
		t.Fatalf("query after restart: %v", err)
	}
	if view.TaskRevision != 1 || view.LatestDecisionID != firstDecision.DecisionID {
		t.Fatal("restart did not preserve committed shell state")
	}
	secondHeader := intentHeader(t, "request-restart-2", "task-restart-1", now.Add(time.Minute))
	secondHeader.ExpectedTaskRevision = 1
	second := minimalWorkIntent(t, secondHeader)
	secondDecision, err := restarted.Mutations.Decide(context.Background(), second)
	if err != nil {
		t.Fatalf("decide after restart: %v", err)
	}
	if secondDecision.DecisionID.String() != "decision-000002" {
		t.Fatal("restart did not preserve the deterministic ID sequence")
	}
	if secondDecision.CommittedAt != now.Add(5*time.Minute) {
		t.Fatal("restart did not preserve the controlled clock")
	}
}

func TestHarnessAcceptsDeterministicIdentityStarts(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 15, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{
		Now: now,
		IDs: taskorchestration.DeterministicIDConfig{
			DecisionStart:  41,
			AuditFactStart: 91,
		},
	})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-custom-identities"), taskorchestration.AuthorizationGeneration(1),
	)
	first, err := harness.Mutations.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "request-custom-identities-1", "task-custom-identities", now),
		authority,
	))
	if err != nil {
		t.Fatalf("decide with custom deterministic identities: %v", err)
	}
	if first.DecisionID.String() != "decision-000041" ||
		first.MandatoryAuditFactRef.AuditFactID.String() != "audit-fact-000091" {
		t.Fatal("harness ignored the injected deterministic identity starts")
	}

	header := intentHeader(t, "request-custom-identities-2", "task-custom-identities", now)
	header.ExpectedTaskRevision = 1
	second, err := harness.Restart().Mutations.Decide(
		context.Background(),
		minimalWorkIntent(t, header),
	)
	if err != nil {
		t.Fatalf("decide after restart with custom deterministic identities: %v", err)
	}
	if second.DecisionID.String() != "decision-000042" ||
		second.MandatoryAuditFactRef.AuditFactID.String() != "audit-fact-000092" {
		t.Fatal("restart did not preserve the injected deterministic identity sequences")
	}
}

func TestHarnessCanLoseAResponseAfterCommit(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 30, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	harness.LoseNextResponse()
	intent := minimalPinnedStartIntent(t,
		intentHeader(t, "request-response-loss", "task-response-loss", now),
		taskorchestration.NewUserAuthority(
			authorityID(t, "user-authority-response"), taskorchestration.AuthorizationGeneration(1),
		),
	)

	_, err = harness.Mutations.Decide(context.Background(), intent)
	var decisionError *taskorchestration.Error
	if !errors.As(err, &decisionError) ||
		decisionError.Code() != taskorchestration.ErrorReconciliationRequired ||
		decisionError.ReconciliationDisposition() != taskorchestration.ReconciliationRequired {
		t.Fatalf("lost response error = %T, want reconciliation-required", err)
	}
	view, err := harness.Queries.Query(
		context.Background(),
		taskQuery(t, "task-response-loss", "user-authority-response"),
	)
	if err != nil {
		t.Fatalf("query after lost response: %v", err)
	}
	if view.TaskRevision != 1 || view.DecisionCount != 1 ||
		view.LatestDecisionID.String() != "decision-000001" {
		t.Fatal("lost response did not retain the committed decision")
	}

	restarted := harness.Restart()
	secondHeader := intentHeader(t, "request-response-next", "task-response-loss", now.Add(time.Second))
	secondHeader.ExpectedTaskRevision = 1
	second, err := restarted.Mutations.Decide(context.Background(), minimalWorkIntent(t, secondHeader))
	if err != nil {
		t.Fatalf("decide after response-loss restart: %v", err)
	}
	if second.DecisionID.String() != "decision-000002" || second.PreviousTaskRevision != 1 {
		t.Fatal("response loss changed durable ID or revision sequencing")
	}
}

func TestHarnessCrashBoundariesRespectCommitLinearization(t *testing.T) {
	now := time.Date(2026, time.July, 26, 13, 0, 0, 0, time.UTC)
	tests := []struct {
		name              string
		boundary          taskorchestration.CrashBoundary
		wantCode          taskorchestration.ErrorCode
		wantRevision      taskorchestration.TaskRevision
		wantDecisionCount uint64
	}{
		{"before commit", taskorchestration.CrashBeforeCommit, taskorchestration.ErrorDependencyUnavailable, 0, 0},
		{"after commit", taskorchestration.CrashAfterCommit, taskorchestration.ErrorReconciliationRequired, 1, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
			if err != nil {
				t.Fatalf("create deterministic harness: %v", err)
			}
			if err := harness.CrashNextAt(test.boundary); err != nil {
				t.Fatalf("configure crash boundary: %v", err)
			}
			intent := minimalPinnedStartIntent(t,
				intentHeader(t, "request-crash", "task-crash", now),
				taskorchestration.NewUserAuthority(
					authorityID(t, "user-authority-crash"), taskorchestration.AuthorizationGeneration(1),
				),
			)
			_, err = harness.Mutations.Decide(context.Background(), intent)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) || decisionError.Code() != test.wantCode {
				t.Fatalf("crash error = %T, want code %v", err, test.wantCode)
			}
			query := taskQuery(t, "task-crash", "user-authority-crash")
			if _, err := harness.Queries.Query(context.Background(), query); err == nil {
				t.Fatal("crashed harness continued serving queries")
			}

			restarted := harness.Restart()
			view, queryErr := restarted.Queries.Query(context.Background(), query)
			if test.boundary == taskorchestration.CrashBeforeCommit {
				var queryDecisionError *taskorchestration.Error
				if !errors.As(queryErr, &queryDecisionError) ||
					queryDecisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
					t.Fatalf("pre-commit restart query error = %T, want safe authorization denial", queryErr)
				}
				decision, err := restarted.Mutations.Decide(context.Background(), intent)
				if err != nil {
					t.Fatalf("decide after pre-commit crash: %v", err)
				}
				if decision.DecisionID.String() != "decision-000001" {
					t.Fatal("pre-commit crash consumed a deterministic identity")
				}
				return
			}
			if queryErr != nil {
				t.Fatalf("query restarted harness: %v", queryErr)
			}
			if view.TaskRevision != test.wantRevision || view.DecisionCount != test.wantDecisionCount {
				t.Fatalf(
					"restart state = revision %d, decisions %d; want %d, %d",
					view.TaskRevision, view.DecisionCount, test.wantRevision, test.wantDecisionCount,
				)
			}
		})
	}
}

func TestHarnessFaultHooksPreserveTheCommitBoundary(t *testing.T) {
	now := time.Date(2026, time.July, 26, 13, 30, 0, 0, time.UTC)
	tests := []struct {
		point         taskorchestration.FaultPoint
		wantCode      taskorchestration.ErrorCode
		wantCommitted bool
	}{
		{taskorchestration.FaultBeforeValidation, taskorchestration.ErrorDependencyUnavailable, false},
		{taskorchestration.FaultAfterValidation, taskorchestration.ErrorDependencyUnavailable, false},
		{taskorchestration.FaultBeforeCommit, taskorchestration.ErrorDependencyUnavailable, false},
		{taskorchestration.FaultAfterCommit, taskorchestration.ErrorReconciliationRequired, true},
		{taskorchestration.FaultBeforeResponse, taskorchestration.ErrorReconciliationRequired, true},
	}
	for _, test := range tests {
		t.Run(test.point.String(), func(t *testing.T) {
			harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
			if err != nil {
				t.Fatalf("create deterministic harness: %v", err)
			}
			if err := harness.FailNextAt(test.point); err != nil {
				t.Fatalf("configure fault hook: %v", err)
			}
			intent := minimalPinnedStartIntent(t,
				intentHeader(t, "request-fault", "task-fault", now),
				taskorchestration.NewUserAuthority(
					authorityID(t, "user-authority-fault"), taskorchestration.AuthorizationGeneration(1),
				),
			)
			_, err = harness.Mutations.Decide(context.Background(), intent)
			var decisionError *taskorchestration.Error
			if !errors.As(err, &decisionError) || decisionError.Code() != test.wantCode {
				t.Fatalf("fault error = %T, want code %v", err, test.wantCode)
			}
			view, queryErr := harness.Queries.Query(
				context.Background(),
				taskQuery(t, "task-fault", "user-authority-fault"),
			)
			if test.wantCommitted {
				if queryErr != nil || view.DecisionCount != 1 {
					t.Fatalf("committed query = (%d, %v), want one decision", view.DecisionCount, queryErr)
				}
				return
			}
			var queryDecisionError *taskorchestration.Error
			if !errors.As(queryErr, &queryDecisionError) ||
				queryDecisionError.Code() != taskorchestration.ErrorAuthorizationDenied {
				t.Fatalf("uncommitted query error = %T, want safe authorization denial", queryErr)
			}
		})
	}
}

func TestPublicContractUsesOpaqueSeparatedTypes(t *testing.T) {
	strictTypes := []reflect.Type{
		reflect.TypeOf(taskorchestration.DecisionRequestID{}),
		reflect.TypeOf(taskorchestration.DecisionID{}),
		reflect.TypeOf(taskorchestration.AuditFactID{}),
		reflect.TypeOf(taskorchestration.OperationID{}),
		reflect.TypeOf(taskorchestration.CausationID{}),
		reflect.TypeOf(taskorchestration.TraceID{}),
		reflect.TypeOf(taskorchestration.TaskRevision(0)),
		reflect.TypeOf(taskorchestration.ActivityGeneration(0)),
		reflect.TypeOf(taskorchestration.PhaseRunGeneration(0)),
		reflect.TypeOf(taskorchestration.PhaseRunFence(0)),
		reflect.TypeOf(taskorchestration.RuntimeGeneration(0)),
		reflect.TypeOf(taskorchestration.TaskWorkspaceLifecycleGeneration(0)),
		reflect.TypeOf(taskorchestration.RuntimeFence(0)),
		reflect.TypeOf(taskorchestration.ValidationFence(0)),
		reflect.TypeOf(taskorchestration.TaskWorkspaceLifecycleFence(0)),
		reflect.TypeOf(taskorchestration.PublicationFence(0)),
		reflect.TypeOf(taskorchestration.SchedulerFence(0)),
		reflect.TypeOf(taskorchestration.UsageFence(0)),
		reflect.TypeOf(taskorchestration.ReconciliationFence(0)),
		reflect.TypeOf(taskorchestration.RecoveryFence(0)),
		reflect.TypeOf(taskorchestration.SafetyEpoch(0)),
		reflect.TypeOf(taskorchestration.PayloadDigest{}),
		reflect.TypeOf(taskorchestration.EvidenceDigest{}),
		reflect.TypeOf(taskorchestration.EnactmentPayloadDigest{}),
		reflect.TypeOf(taskorchestration.CanonicalRequestDigest{}),
		reflect.TypeOf(taskorchestration.PhaseKey{}),
		reflect.TypeOf(taskorchestration.PipelineVersionID{}),
		reflect.TypeOf(taskorchestration.ExecutionLockID{}),
		reflect.TypeOf(taskorchestration.RuntimeReleaseID{}),
		reflect.TypeOf(taskorchestration.CompatibilityApprovalID{}),
		reflect.TypeOf(taskorchestration.TemplateLockID{}),
		reflect.TypeOf(taskorchestration.TaskWorkspaceID{}),
		reflect.TypeOf(taskorchestration.TaskWorkspaceRevisionID{}),
		reflect.TypeOf(taskorchestration.CheckpointID{}),
	}
	for left := range strictTypes {
		for right := left + 1; right < len(strictTypes); right++ {
			if strictTypes[left] == strictTypes[right] {
				t.Fatal("two authority concepts share one public type")
			}
		}
	}

	publicStructs := []reflect.Type{
		reflect.TypeOf(taskorchestration.IntentHeader{}),
		reflect.TypeOf(taskorchestration.IntentMetadata{}),
		reflect.TypeOf(taskorchestration.TraceMetadata{}),
		reflect.TypeOf(taskorchestration.TransportMetadata{}),
		reflect.TypeOf(taskorchestration.DiagnosticMetadata{}),
		reflect.TypeOf(taskorchestration.EvidenceRef{}),
		reflect.TypeOf(taskorchestration.EvidenceDiagnostic{}),
		reflect.TypeOf(taskorchestration.RuntimeEvidenceBinding{}),
		reflect.TypeOf(taskorchestration.ValidationEvidenceBinding{}),
		reflect.TypeOf(taskorchestration.TaskWorkspaceLifecycleEvidenceBinding{}),
		reflect.TypeOf(taskorchestration.PublicationEvidenceBinding{}),
		reflect.TypeOf(taskorchestration.SchedulingEvidenceBinding{}),
		reflect.TypeOf(taskorchestration.OperationalFenceBinding{}),
		reflect.TypeOf(taskorchestration.TransitionDecision{}),
		reflect.TypeOf(taskorchestration.TaskProjection{}),
		reflect.TypeOf(taskorchestration.AuditFactRef{}),
		reflect.TypeOf(taskorchestration.EnactmentRef{}),
		reflect.TypeOf(taskorchestration.TaskQuery{}),
		reflect.TypeOf(taskorchestration.TaskOrchestrationView{}),
		reflect.TypeOf(taskorchestration.Error{}),
		reflect.TypeOf(taskorchestration.PinnedTaskStart{}),
		reflect.TypeOf(taskorchestration.ExecutionLock{}),
		reflect.TypeOf(taskorchestration.PipelineContract{}),
		reflect.TypeOf(taskorchestration.RouteDefinition{}),
		reflect.TypeOf(taskorchestration.PhaseDefinition{}),
		reflect.TypeOf(taskorchestration.PhaseRunView{}),
		reflect.TypeOf(taskorchestration.RuntimeRunView{}),
	}
	deniedFieldTokens := []string{
		"content", "path", "session", "mount", "locator", "credential", "vendor",
	}
	for _, structType := range publicStructs {
		for fieldIndex := range structType.NumField() {
			fieldName := strings.ToLower(structType.Field(fieldIndex).Name)
			for _, denied := range deniedFieldTokens {
				if strings.Contains(fieldName, denied) {
					t.Fatal("exported seam struct contains a denied field category")
				}
			}
		}
	}

	for code := taskorchestration.ErrorAuthorizationDenied; code <= taskorchestration.ErrorTerminalConflict; code++ {
		safeText := code.String()
		for _, denied := range deniedFieldTokens {
			if strings.Contains(strings.ToLower(safeText), denied) {
				t.Fatal("closed error emitted a denied diagnostic category")
			}
		}
	}
}

func TestEnactmentRefKeepsClosedTypedDurableFacts(t *testing.T) {
	payloadDigest, err := taskorchestration.ParseEnactmentPayloadDigest(strings.Repeat("4", 64))
	if err != nil {
		t.Fatalf("parse enactment payload digest: %v", err)
	}
	causationID, err := taskorchestration.NewCausationID("causation-1")
	if err != nil {
		t.Fatalf("create causation identity: %v", err)
	}
	ref := taskorchestration.EnactmentRef{
		OperationID:        operationID(t, "operation-enactment-1"),
		Kind:               taskorchestration.EnactmentRuntimeExecution,
		PayloadDigest:      payloadDigest,
		ActivityGeneration: 7,
		Fence:              taskorchestration.RuntimeFence(11),
		CausationID:        causationID,
	}
	if ref.PayloadDigest.String() != strings.Repeat("4", 64) ||
		ref.CausationID.String() != "causation-1" ||
		ref.Fence.EnactmentFenceKind() != taskorchestration.EnactmentFenceRuntimeExecution {
		t.Fatal("enactment reference collapsed or omitted a durable typed fact")
	}

	fences := []struct {
		ref  taskorchestration.EnactmentFenceRef
		kind taskorchestration.EnactmentFenceKind
	}{
		{taskorchestration.RuntimeFence(1), taskorchestration.EnactmentFenceRuntimeExecution},
		{taskorchestration.TaskWorkspaceLifecycleFence(1), taskorchestration.EnactmentFenceTaskWorkspaceLifecycle},
		{taskorchestration.PublicationFence(1), taskorchestration.EnactmentFenceArtifactPublication},
		{taskorchestration.SchedulerFence(1), taskorchestration.EnactmentFenceScheduling},
		{taskorchestration.UsageFence(1), taskorchestration.EnactmentFenceUsageAccounting},
	}
	for _, fence := range fences {
		if fence.ref.EnactmentFenceKind() != fence.kind {
			t.Fatal("typed enactment fence reported the wrong closed kind")
		}
	}
}

func TestDecideUsesDeterministicBusinessDigest(t *testing.T) {
	occurredAt := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
	firstHeader := intentHeader(t, "decision-request-1", "task-1", occurredAt)
	firstHeader.Metadata = taskorchestration.IntentMetadata{
		Trace: taskorchestration.TraceMetadata{TraceID: traceID(t, strings.Repeat("1", 32))},
		Transport: taskorchestration.TransportMetadata{
			Deadline:        occurredAt.Add(time.Minute),
			DeliveryAttempt: 1,
		},
		Diagnostic: taskorchestration.DiagnosticMetadata{Code: taskorchestration.DiagnosticIngress},
	}
	secondHeader := taskorchestration.IntentHeader{
		OccurredAt:           occurredAt,
		ActivityGeneration:   1,
		ExpectedTaskRevision: 0,
		TaskID:               taskID(t, "task-1"),
		DecisionRequestID:    decisionRequestID(t, "decision-request-1"),
		SchemaVersion:        taskorchestration.SchemaV1,
		Metadata: taskorchestration.IntentMetadata{
			Diagnostic: taskorchestration.DiagnosticMetadata{Code: taskorchestration.DiagnosticReplay},
			Transport: taskorchestration.TransportMetadata{
				Deadline:        occurredAt.Add(10 * time.Minute),
				DeliveryAttempt: 9,
			},
			Trace: taskorchestration.TraceMetadata{TraceID: traceID(t, strings.Repeat("2", 32))},
		},
	}
	authority := taskorchestration.NewUserAuthority(
		authorityID(t, "user-authority-1"),
		taskorchestration.AuthorizationGeneration(1),
	)
	gateID := gateID(t, "gate-1")
	firstIntent := taskorchestration.NewSubmitConfirmationGateIntent(
		firstHeader,
		authority,
		gateID,
		payloadDigest(t, strings.Repeat("a", 64)),
	)
	secondIntent := taskorchestration.NewSubmitConfirmationGateIntent(
		secondHeader,
		authority,
		gateID,
		payloadDigest(t, strings.Repeat("a", 64)),
	)
	changedIntent := taskorchestration.NewSubmitConfirmationGateIntent(
		secondHeader,
		authority,
		gateID,
		payloadDigest(t, strings.Repeat("b", 64)),
	)

	firstDigest := firstIntent.Header().CanonicalRequestDigest
	secondDigest := secondIntent.Header().CanonicalRequestDigest
	changedDigest := changedIntent.Header().CanonicalRequestDigest
	if firstDigest != secondDigest {
		t.Fatal("non-business metadata or field construction order changed the business digest")
	}
	if firstDigest == changedDigest {
		t.Fatal("semantic payload change did not change the business digest")
	}
}

func decideOnce(
	t *testing.T,
	now time.Time,
	intent taskorchestration.TransitionIntent,
) taskorchestration.TransitionDecision {
	t.Helper()
	harness, err := taskorchestration.NewDeterministicHarness(taskorchestration.HarnessConfig{Now: now})
	if err != nil {
		t.Fatalf("create deterministic harness: %v", err)
	}
	decision, err := harness.Mutations.Decide(context.Background(), intent)
	if err != nil {
		t.Fatalf("decide intent: %v", err)
	}
	return decision
}

func intentHeader(t *testing.T, request, task string, occurredAt time.Time) taskorchestration.IntentHeader {
	t.Helper()
	requestID, err := taskorchestration.NewDecisionRequestID(request)
	if err != nil {
		t.Fatalf("create request identity: %v", err)
	}
	taskID, err := taskorchestration.NewTaskID(task)
	if err != nil {
		t.Fatalf("create Task identity: %v", err)
	}
	return taskorchestration.IntentHeader{
		SchemaVersion:        taskorchestration.SchemaV1,
		DecisionRequestID:    requestID,
		TaskID:               taskID,
		ExpectedTaskRevision: 0,
		ActivityGeneration:   1,
		OccurredAt:           occurredAt,
	}
}

func authorityID(t *testing.T, value string) taskorchestration.AuthorityID {
	t.Helper()
	id, err := taskorchestration.NewAuthorityID(value)
	if err != nil {
		t.Fatalf("create authority identity: %v", err)
	}
	return id
}

func decisionRequestID(t *testing.T, value string) taskorchestration.DecisionRequestID {
	t.Helper()
	id, err := taskorchestration.NewDecisionRequestID(value)
	if err != nil {
		t.Fatalf("create request identity: %v", err)
	}
	return id
}

func taskID(t *testing.T, value string) taskorchestration.TaskID {
	t.Helper()
	id, err := taskorchestration.NewTaskID(value)
	if err != nil {
		t.Fatalf("create Task identity: %v", err)
	}
	return id
}

func taskQuery(t *testing.T, task, authority string) taskorchestration.TaskQuery {
	t.Helper()
	return taskorchestration.TaskQuery{
		TaskID: taskID(t, task),
		Authority: taskorchestration.NewUserQueryAuthority(taskorchestration.NewUserAuthority(
			authorityID(t, authority), taskorchestration.AuthorizationGeneration(1),
		)),
	}
}

func gateID(t *testing.T, value string) taskorchestration.GateID {
	t.Helper()
	id, err := taskorchestration.NewGateID(value)
	if err != nil {
		t.Fatalf("create Gate identity: %v", err)
	}
	return id
}

func traceID(t *testing.T, value string) taskorchestration.TraceID {
	t.Helper()
	id, err := taskorchestration.ParseTraceID(value)
	if err != nil {
		t.Fatalf("create trace identity: %v", err)
	}
	return id
}

func payloadDigest(t *testing.T, value string) taskorchestration.PayloadDigest {
	t.Helper()
	digest, err := taskorchestration.ParsePayloadDigest(value)
	if err != nil {
		t.Fatalf("create payload digest: %v", err)
	}
	return digest
}

func operationID(t *testing.T, value string) taskorchestration.OperationID {
	t.Helper()
	id, err := taskorchestration.NewOperationID(value)
	if err != nil {
		t.Fatalf("create operation identity: %v", err)
	}
	return id
}

func phaseRunID(t *testing.T, value string) taskorchestration.PhaseRunID {
	t.Helper()
	id, err := taskorchestration.NewPhaseRunID(value)
	if err != nil {
		t.Fatalf("create Phase Run identity: %v", err)
	}
	return id
}

func runtimeRunID(t *testing.T, value string) taskorchestration.RuntimeRunID {
	t.Helper()
	id, err := taskorchestration.NewRuntimeRunID(value)
	if err != nil {
		t.Fatalf("create Runtime Run identity: %v", err)
	}
	return id
}

func artifactVersionID(t *testing.T, value string) taskorchestration.ArtifactVersionID {
	t.Helper()
	id, err := taskorchestration.NewArtifactVersionID(value)
	if err != nil {
		t.Fatalf("create Artifact Version identity: %v", err)
	}
	return id
}

func evidenceID(t *testing.T, value string) taskorchestration.EvidenceID {
	t.Helper()
	id, err := taskorchestration.NewEvidenceID(value)
	if err != nil {
		t.Fatalf("create evidence identity: %v", err)
	}
	return id
}

func evidenceDigest(t *testing.T, value string) taskorchestration.EvidenceDigest {
	t.Helper()
	digest, err := taskorchestration.ParseEvidenceDigest(value)
	if err != nil {
		t.Fatalf("create evidence digest: %v", err)
	}
	return digest
}
