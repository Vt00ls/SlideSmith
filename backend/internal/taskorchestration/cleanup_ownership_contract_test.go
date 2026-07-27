package taskorchestration_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestCleanupDebtOwnershipAndEvidenceReferenceContract(t *testing.T) {
	runCleanupDebtOwnershipAndEvidenceReferenceContract(t)
}

func runCleanupDebtOwnershipAndEvidenceReferenceContract(t *testing.T) {
	t.Helper()
	matrix := taskorchestration.CleanupOwnershipMatrix()
	if len(matrix) == 0 {
		t.Fatal("Cleanup Debt ownership matrix is empty")
	}
	owners := make(map[taskorchestration.CleanupResourceClass]taskorchestration.CleanupOwnerModule)
	for _, entry := range matrix {
		if _, duplicate := owners[entry.ResourceClass]; duplicate {
			t.Fatalf("physical resource class %v has more than one Cleanup Debt owner", entry.ResourceClass)
		}
		if entry.Owner.String() == "" || entry.Owner.String() == "task_orchestration" {
			t.Fatalf("invalid physical Cleanup Debt owner: %+v", entry)
		}
		owners[entry.ResourceClass] = entry.Owner
	}
	for resource, owner := range map[taskorchestration.CleanupResourceClass]taskorchestration.CleanupOwnerModule{
		taskorchestration.CleanupTaskWorkspaceRuntimeView:        taskorchestration.CleanupOwnerTaskWorkspaceLifecycle,
		taskorchestration.CleanupTaskWorkspaceMaterialization:    taskorchestration.CleanupOwnerTaskWorkspaceLifecycle,
		taskorchestration.CleanupTaskWorkspaceCheckpoint:         taskorchestration.CleanupOwnerTaskWorkspaceLifecycle,
		taskorchestration.CleanupRuntimeProcess:                  taskorchestration.CleanupOwnerRuntimeExecution,
		taskorchestration.CleanupRuntimeSandbox:                  taskorchestration.CleanupOwnerRuntimeExecution,
		taskorchestration.CleanupRuntimeLease:                    taskorchestration.CleanupOwnerRuntimeExecution,
		taskorchestration.CleanupDurableObjectStaging:            taskorchestration.CleanupOwnerDurableObject,
		taskorchestration.CleanupDurableObjectPhysicalGeneration: taskorchestration.CleanupOwnerDurableObject,
		taskorchestration.CleanupDurableObjectCache:              taskorchestration.CleanupOwnerDurableObject,
	} {
		if owners[resource] != owner {
			t.Fatalf("resource class %v owner = %v, want %v", resource, owners[resource], owner)
		}
	}

	now := time.Date(2026, time.July, 28, 0, 0, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create cleanup authority harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "cleanup-task-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	_, err = harness.Mutations.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "cleanup-task-start", "cleanup-task", now), owner,
	))
	if err != nil {
		t.Fatalf("start cleanup reference Task: %v", err)
	}
	taskQuery := taskorchestration.TaskQuery{
		TaskID:    taskID(t, "cleanup-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	}
	before, err := harness.Queries.Query(context.Background(), taskQuery)
	if err != nil {
		t.Fatalf("query before Cleanup Debt evidence reference: %v", err)
	}

	auditFaults := &taskorchestration.DiagnosticAuditFaultController{}
	index := taskorchestration.NewDeterministicCleanupEvidenceIndex(
		taskorchestration.CleanupEvidenceIndexConfig{
			Now: func() time.Time { return now }, DiagnosticAuditFaults: auditFaults,
		},
	)
	producer := taskorchestration.NewCleanupEvidenceAuthority(
		authorityID(t, "cleanup-c04-producer"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.CleanupOwnerTaskWorkspaceLifecycle,
	)
	reference, err := taskorchestration.NewCleanupEvidenceReference(
		cleanupDebtID(t, "cleanup-debt-c04"),
		taskorchestration.CleanupOwnerTaskWorkspaceLifecycle,
		taskorchestration.CleanupTaskWorkspaceRuntimeView,
		evidenceID(t, "cleanup-evidence-c04"),
		operationID(t, "cleanup-operation-c04"),
		taskorchestration.TelemetryCategoryDependency,
	)
	if err != nil {
		t.Fatalf("create content-free cleanup evidence reference: %v", err)
	}
	if err := index.Record(context.Background(), producer, reference); err != nil {
		t.Fatalf("record content-free cleanup evidence reference: %v", err)
	}
	if err := index.Record(context.Background(), producer, reference); err != nil {
		t.Fatalf("exact cleanup evidence replay was not idempotent: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "cleanup-diagnostic-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	auditFaults.FailNext()
	_, err = index.Query(
		context.Background(),
		taskorchestration.NewCleanupEvidenceQuery(administrator, reference.DebtID),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	observed, err := index.Query(
		context.Background(),
		taskorchestration.NewCleanupEvidenceQuery(administrator, reference.DebtID),
	)
	if err != nil || observed.Reference != reference ||
		observed.AccessAuditFactRef.AuditFactID.String() == "" ||
		observed.AccessAuditFactRef.CanonicalDigest == (taskorchestration.ProjectionDigest{}) ||
		observed.AccessAuditFactRef.Outcome != taskorchestration.DiagnosticAuditAccepted {
		t.Fatalf("query cleanup evidence reference: observed=%+v err=%v", observed, err)
	}
	missingDebtID := cleanupDebtID(t, "cleanup-debt-missing")
	auditFaults.FailNext()
	_, err = index.Query(
		context.Background(),
		taskorchestration.NewCleanupEvidenceQuery(administrator, missingDebtID),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	_, err = index.Query(
		context.Background(),
		taskorchestration.NewCleanupEvidenceQuery(administrator, missingDebtID),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorAuthorizationDenied)

	_, err = taskorchestration.NewCleanupEvidenceReference(
		cleanupDebtID(t, "cleanup-debt-wrong-owner"),
		taskorchestration.CleanupOwnerRuntimeExecution,
		taskorchestration.CleanupTaskWorkspaceRuntimeView,
		evidenceID(t, "cleanup-evidence-wrong-owner"),
		operationID(t, "cleanup-operation-wrong-owner"),
		taskorchestration.TelemetryCategoryIntegrity,
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorIntegrityConflict)

	after, err := harness.Queries.Query(context.Background(), taskQuery)
	if err != nil || after.TaskRevision != before.TaskRevision ||
		after.DecisionCount != before.DecisionCount || after.LatestDecisionID != before.LatestDecisionID {
		t.Fatalf("cleanup evidence index advanced or repaired Task state: before=%+v after=%+v err=%v", before, after, err)
	}
	assertAllowlistedNonLeakageSurface(
		t, reflect.TypeOf(taskorchestration.CleanupEvidenceReference{}),
	)
	assertAllowlistedNonLeakageSurface(
		t, reflect.TypeOf(taskorchestration.CleanupEvidenceQueryResult{}),
	)
}

func cleanupDebtID(t *testing.T, value string) taskorchestration.CleanupDebtID {
	t.Helper()
	id, err := taskorchestration.NewCleanupDebtID(value)
	if err != nil {
		t.Fatalf("create Cleanup Debt identity: %v", err)
	}
	return id
}
