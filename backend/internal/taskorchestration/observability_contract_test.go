package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestExternalAuditAndTelemetryProjectionFailureDoesNotRollbackDecision(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := &failingDecisionProjectionSink{}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: sink,
			Telemetry:     sink,
		},
	)
	if err != nil {
		t.Fatalf("create decision projection adapter: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, CommitObserver: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "projection-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), taskorchestration.NewStartTaskIntent(
		intentHeader(t, "projection-start", "projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit with failed external projections: %v", err)
	}
	if decision.AcceptedTaskRevision != 1 || sink.auditCount() != 1 || sink.telemetryCount() != 1 {
		t.Fatalf("projection failures affected committed decision or skipped a sink: decision=%+v", decision)
	}
	view, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || view.TaskRevision != 1 || view.DecisionCount != 1 {
		t.Fatalf("projection failure rolled back protected Task: view=%+v err=%v", view, err)
	}
}

func TestBoundedTelemetryUsesTypedLabelsAndAllowlistedLogsAndTraces(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 30, 0, 0, time.UTC)
	telemetry := taskorchestration.NewDeterministicTelemetry()
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: taskorchestration.ExternalAuditProjectionSinkFunc(func(
				context.Context,
				taskorchestration.ExternalAuditProjection,
			) error {
				return nil
			}),
			Telemetry: telemetry,
		},
	)
	if err != nil {
		t.Fatalf("create bounded telemetry projector: %v", err)
	}
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create telemetry decision harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "telemetry-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "telemetry-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "telemetry-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(),
		taskorchestration.NewStartPinnedTaskIntent(
			intentHeader(t, "telemetry-start", "telemetry-task", now), owner, pinned,
		))
	if err != nil {
		t.Fatalf("start telemetry Task: %v", err)
	}
	workHeader := intentHeader(t, "telemetry-work", "telemetry-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			workHeader, worker, operationID(t, "telemetry-work-available"),
		))
	if err != nil {
		t.Fatalf("commit telemetry work: %v", err)
	}
	if err := projector.ObserveCommittedDecision(context.Background(), work); err != nil {
		t.Fatalf("project committed Decision: %v", err)
	}
	if err := telemetry.ProjectReconciliation(
		context.Background(),
		taskorchestration.ReconciliationTelemetryProjection{
			SchemaVersion: taskorchestration.ProjectionSchemaV1,
			DecisionID:    work.DecisionID, OperationID: work.EnactmentRefs[0].OperationID,
			TaskRevision: work.AcceptedTaskRevision,
			Outcome:      taskorchestration.TelemetryReconciliationRequired,
			Category:     taskorchestration.TelemetryCategoryDependency,
			ObservedAt:   now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("project reconciliation health: %v", err)
	}
	snapshot := telemetry.Snapshot()
	if len(snapshot.Metrics) != 3 || len(snapshot.Logs) != 3 || len(snapshot.Traces) != 3 {
		t.Fatalf("bounded telemetry snapshot omitted signals: %+v", snapshot)
	}
	if taskorchestration.MetricSeriesUpperBound() == 0 ||
		taskorchestration.MetricSeriesUpperBound() > 512 {
		t.Fatalf("metric series bound is absent or unexpectedly unbounded: %d",
			taskorchestration.MetricSeriesUpperBound())
	}
	for _, metric := range snapshot.Metrics {
		text := fmt.Sprintf("%+v", metric.Labels)
		for _, forbidden := range []string{
			work.DecisionID.String(), work.EnactmentRefs[0].OperationID.String(),
			"telemetry-task", "telemetry-work-available",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("metric labels contain high-cardinality identity %q: %+v", forbidden, metric)
			}
		}
	}
	for _, log := range snapshot.Logs {
		text := fmt.Sprintf("%+v", log)
		if strings.Contains(text, work.DecisionID.String()) ||
			strings.Contains(text, work.EnactmentRefs[0].OperationID.String()) {
			t.Fatalf("ordinary structured log contains protected business identity: %+v", log)
		}
	}
	if snapshot.Traces[0].DecisionID != work.DecisionID ||
		snapshot.Traces[1].OperationID != work.EnactmentRefs[0].OperationID {
		t.Fatalf("allowlisted protected trace correlation is missing: %+v", snapshot.Traces)
	}
}

type failingDecisionProjectionSink struct {
	mu        sync.Mutex
	audits    uint64
	telemetry uint64
}

func (sink *failingDecisionProjectionSink) ProjectExternalAudit(
	context.Context,
	taskorchestration.ExternalAuditProjection,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.audits++
	return errors.New("credential-canary at /private/session")
}

func (sink *failingDecisionProjectionSink) ProjectTelemetry(
	context.Context,
	taskorchestration.DecisionTelemetryProjection,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.telemetry++
	return errors.New("content-canary at object-locator")
}

func (sink *failingDecisionProjectionSink) auditCount() uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.audits
}

func (sink *failingDecisionProjectionSink) telemetryCount() uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.telemetry
}
