package taskorchestration_test

import (
	"context"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestAuthorizedDeterministicDiagnosticsContract(t *testing.T) {
	testCases := []struct {
		name string
		new  func(
			*testing.T,
			time.Time,
		) (taskorchestration.TaskOrchestration, taskorchestration.TaskOrchestrationQuery, taskorchestration.OperationalDiagnostics, func())
	}{
		{name: "in_memory", new: func(t *testing.T, now time.Time) (
			taskorchestration.TaskOrchestration,
			taskorchestration.TaskOrchestrationQuery,
			taskorchestration.OperationalDiagnostics,
			func(),
		) {
			t.Helper()
			faults := &taskorchestration.DiagnosticAuditFaultController{}
			harness, err := taskorchestration.NewDeterministicHarness(
				taskorchestration.HarnessConfig{Now: now, DiagnosticAuditFaults: faults},
			)
			if err != nil {
				t.Fatalf("create diagnostic harness: %v", err)
			}
			return harness.Mutations, harness.Queries, harness.Diagnostics, faults.FailNext
		}},
		{name: "postgres_owned_persistence", new: func(t *testing.T, now time.Time) (
			taskorchestration.TaskOrchestration,
			taskorchestration.TaskOrchestrationQuery,
			taskorchestration.OperationalDiagnostics,
			func(),
		) {
			t.Helper()
			db, schema := isolatedPostgresSchema(t)
			faults := &taskorchestration.DiagnosticAuditFaultController{}
			adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
				Now: func() time.Time { return now }, DiagnosticAuditFaults: faults,
			})
			return adapter, adapter, adapter, faults.FailNext
		}},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runAuthorizedDiagnosticContract(t, testCase.new)
		})
	}
}

func runAuthorizedDiagnosticContract(
	t *testing.T,
	newAdapter func(
		*testing.T,
		time.Time,
	) (taskorchestration.TaskOrchestration, taskorchestration.TaskOrchestrationQuery, taskorchestration.OperationalDiagnostics, func()),
) {
	t.Helper()
	now := time.Date(2026, time.July, 27, 23, 0, 0, 0, time.UTC)
	mutations, queries, diagnostics, failNextAudit := newAdapter(t, now)
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "diagnostic-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "diagnostic-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "diagnostic-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := mutations.Decide(context.Background(), taskorchestration.NewStartPinnedTaskIntent(
		intentHeader(t, "diagnostic-start", "diagnostic-task", now), owner, pinned,
	))
	if err != nil {
		t.Fatalf("start diagnostic Task: %v", err)
	}
	workHeader := intentHeader(t, "diagnostic-work", "diagnostic-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := mutations.Decide(context.Background(), taskorchestration.NewMakeWorkAvailableIntent(
		workHeader, worker, operationID(t, "diagnostic-work-available"),
	))
	if err != nil {
		t.Fatalf("commit diagnostic operation: %v", err)
	}
	query := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "diagnostic-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	before, err := queries.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query before diagnostics: %v", err)
	}
	authority := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "diagnostic-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	failNextAudit()
	_, err = diagnostics.Diagnose(
		context.Background(),
		taskorchestration.NewDecisionDiagnosticQuery(
			authority, taskID(t, "diagnostic-task"), work.DecisionID,
		),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	decisionView, err := diagnostics.Diagnose(
		context.Background(),
		taskorchestration.NewDecisionDiagnosticQuery(
			authority, taskID(t, "diagnostic-task"), work.DecisionID,
		),
	)
	if err != nil || decisionView.DecisionID != work.DecisionID ||
		decisionView.AcceptedTaskRevision != work.AcceptedTaskRevision ||
		decisionView.AuditFactID != work.MandatoryAuditFactRef.AuditFactID ||
		decisionView.AccessAuditFactRef.AuditFactID.String() == "" ||
		decisionView.AccessAuditFactRef.CanonicalDigest == (taskorchestration.ProjectionDigest{}) ||
		decisionView.AccessAuditFactRef.Outcome != taskorchestration.DiagnosticAuditAccepted {
		t.Fatalf("query Decision diagnostic: view=%+v err=%v", decisionView, err)
	}
	operationView, err := diagnostics.Diagnose(
		context.Background(),
		taskorchestration.NewOperationDiagnosticQuery(
			authority, taskID(t, "diagnostic-task"), work.EnactmentRefs[0].OperationID,
		),
	)
	if err != nil || operationView.DecisionID != work.DecisionID ||
		operationView.OperationID != work.EnactmentRefs[0].OperationID ||
		operationView.EnactmentKind != work.EnactmentRefs[0].Kind ||
		operationView.AccessAuditFactRef.AuditFactID == decisionView.AccessAuditFactRef.AuditFactID {
		t.Fatalf("query Operation diagnostic: view=%+v err=%v", operationView, err)
	}
	_, err = diagnostics.Diagnose(
		context.Background(),
		taskorchestration.NewDecisionDiagnosticQuery(
			authority, taskID(t, "diagnostic-other-task"), work.DecisionID,
		),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorAuthorizationDenied)
	_, err = diagnostics.Diagnose(
		context.Background(),
		taskorchestration.NewDecisionDiagnosticQuery(
			taskorchestration.AdministratorMetadataAuthority{},
			taskID(t, "diagnostic-task"), work.DecisionID,
		),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorAuthorizationDenied)
	after, err := queries.Query(context.Background(), query)
	if err != nil || after.TaskRevision != before.TaskRevision ||
		after.DecisionCount != before.DecisionCount || after.LatestDecisionID != before.LatestDecisionID {
		t.Fatalf("diagnostics changed Task authority: before=%+v after=%+v err=%v", before, after, err)
	}
}
