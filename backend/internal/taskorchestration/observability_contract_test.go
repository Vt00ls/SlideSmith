package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestExternalAuditAndTelemetryProjectionDeliveryIsAsynchronousAndRebuildable(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 0, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := &failingDecisionProjectionSink{}
	auditFaults := &taskorchestration.DiagnosticAuditFaultController{}
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
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
		DiagnosticAuditFaults: auditFaults,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "projection-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "projection-start", "projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit with failed external projections: %v", err)
	}
	if decision.AcceptedTaskRevision != 1 || sink.auditCount() != 0 || sink.telemetryCount() != 0 {
		t.Fatalf("projection delivery ran in protected Decide path: decision=%+v", decision)
	}
	if _, err := adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "projection-other-start", "projection-other-task", now), owner,
	)); err != nil {
		t.Fatalf("commit other Task used to prove scoped backlog: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	inspection := taskorchestration.NewProjectionDeliveryInspectionRequest(
		administrator, taskID(t, "projection-task"), 1,
	)
	auditFaults.FailNext()
	_, err = adapter.InspectDecisionProjectionBacklog(context.Background(), inspection)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	beforeDelivery, err := adapter.InspectDecisionProjectionBacklog(context.Background(), inspection)
	if err != nil || beforeDelivery.Pending != 1 || beforeDelivery.Delivered != 0 ||
		beforeDelivery.SourceFactCount != 1 || len(beforeDelivery.Evidence) != 1 ||
		beforeDelivery.Evidence[0].AttemptCount != 0 ||
		beforeDelivery.AccessAuditFactRef.AuditFactID.String() == "" ||
		beforeDelivery.AccessAuditFactRef.Outcome != taskorchestration.DiagnosticAuditAccepted {
		t.Fatalf("inspect initial projection backlog: backlog=%+v err=%v", beforeDelivery, err)
	}
	auditFaults.FailNext()
	_, err = adapter.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	if sink.auditCount() != 0 || sink.telemetryCount() != 0 {
		t.Fatal("privileged redrive reached projection sinks before mandatory access audit")
	}
	failed, err := adapter.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	if err != nil || failed.Pending != 3 || failed.Delivered != 0 ||
		failed.SourceFactCount != 3 || len(failed.Evidence) != 3 ||
		failed.Evidence[0].AttemptCount != 1 || sink.auditCount() != 3 || sink.telemetryCount() != 1 {
		t.Fatalf("failed projection delivery was not retained: backlog=%+v err=%v", failed, err)
	}
	if sink.lastAudit().CanonicalDigest == (taskorchestration.ProjectionDigest{}) ||
		sink.lastAudit().CanonicalDigest != taskorchestration.ExternalAuditProjectionDigest(sink.lastAudit()) {
		t.Fatalf("external audit projection lacks canonical digest: %+v", sink.lastAudit())
	}
	view, err := adapter.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || view.TaskRevision != 1 || view.DecisionCount != 1 {
		t.Fatalf("projection failure rolled back protected Task: view=%+v err=%v", view, err)
	}
	sink.setHealthy()
	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
		DiagnosticAuditFaults: auditFaults,
	})
	rebuilt, err := restarted.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	if err != nil || rebuilt.Pending != 0 || rebuilt.Delivered != 4 ||
		rebuilt.SourceFactCount != 4 || len(rebuilt.Evidence) != 4 ||
		rebuilt.Evidence[0].AttemptCount != 2 || sink.auditCount() != 7 || sink.telemetryCount() != 2 {
		t.Fatalf("rebuild projection delivery after restart: backlog=%+v err=%v", rebuilt, err)
	}
	again, err := restarted.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			administrator, taskID(t, "projection-task"), 100,
		),
	)
	if err != nil || again.Pending != 0 || again.Delivered != 5 ||
		again.SourceFactCount != 5 || sink.auditCount() != 8 || sink.telemetryCount() != 2 ||
		sink.auditFactCount(decision.MandatoryAuditFactRef.AuditFactID) != 2 {
		t.Fatalf("delivered projection was not idempotent: backlog=%+v err=%v", again, err)
	}
}

func TestExternalAuditProjectionCarriesTheExactMandatoryFact(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 12, 0, 0, time.UTC)
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create projection harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "exact-audit-projection-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	start, err := harness.Mutations.Decide(context.Background(), minimalPinnedStartIntent(
		t, intentHeader(t, "exact-audit-projection-start", "exact-audit-projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("start projection Task: %v", err)
	}
	workHeader := intentHeader(
		t, "exact-audit-projection-work", "exact-audit-projection-task", now.Add(time.Second),
	)
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(), minimalWorkIntent(t, workHeader))
	if err != nil || len(work.MandatoryAuditFactRef.EnactmentRefs) != 1 {
		t.Fatalf("commit projection work: decision=%+v err=%v", work, err)
	}
	sink := &failingDecisionProjectionSink{healthy: true}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{ExternalAudit: sink, Telemetry: sink},
	)
	if err != nil {
		t.Fatalf("create projection adapter: %v", err)
	}
	if err := projector.ObserveCommittedDecision(context.Background(), work); err != nil {
		t.Fatalf("project work decision: %v", err)
	}
	projection, ok := sink.auditFor(work.MandatoryAuditFactRef.AuditFactID)
	if !ok || !reflect.DeepEqual(projection.AuthoritativeFact, work.MandatoryAuditFactRef) {
		t.Fatalf("external audit projection changed mandatory fact: projection=%+v fact=%+v",
			projection.AuthoritativeFact, work.MandatoryAuditFactRef)
	}
	projection.AuthoritativeFact.EnactmentRefs[0].Fence++
	if reflect.DeepEqual(projection.AuthoritativeFact, work.MandatoryAuditFactRef) {
		t.Fatal("external audit projection aliases the authoritative Decision fact")
	}
}

func TestTelemetryOnlyProjectionRetryPreservesExternalAuditDeliveryTimes(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 18, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := &splitProjectionSink{}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{ExternalAudit: sink, Telemetry: sink},
	)
	if err != nil {
		t.Fatalf("create split projection sink: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "split-projection-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "split-projection-start", "split-projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit split projection Task: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "split-projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	request := taskorchestration.NewProjectionDeliveryRebuildRequest(
		administrator, taskID(t, "split-projection-task"), 1,
	)
	first, err := adapter.RebuildDecisionProjectionDelivery(context.Background(), request)
	if err != nil {
		t.Fatalf("first split projection attempt: %v", err)
	}
	firstEvidence := projectionEvidenceForAudit(t, first, decision.MandatoryAuditFactRef.AuditFactID)
	if !firstEvidence.ExternalAuditDelivered || firstEvidence.TelemetryDelivered ||
		!firstEvidence.FirstExternalAuditDeliveredAt.Equal(now) ||
		!firstEvidence.LastExternalAuditDeliveredAt.Equal(now) {
		t.Fatalf("first split projection evidence is invalid: %+v", firstEvidence)
	}
	sink.telemetryHealthy = true
	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
	})
	second, err := restarted.RebuildDecisionProjectionDelivery(context.Background(), request)
	if err != nil {
		t.Fatalf("telemetry-only projection retry: %v", err)
	}
	secondEvidence := projectionEvidenceForAudit(t, second, decision.MandatoryAuditFactRef.AuditFactID)
	if !secondEvidence.ExternalAuditDelivered || !secondEvidence.TelemetryDelivered ||
		!secondEvidence.FirstExternalAuditDeliveredAt.Equal(now) ||
		!secondEvidence.LastExternalAuditDeliveredAt.Equal(now) ||
		sink.auditCalls != 1 || sink.telemetryCalls != 2 {
		t.Fatalf("telemetry-only retry changed audit delivery evidence: evidence=%+v sink=%+v", secondEvidence, sink)
	}
}

func projectionEvidenceForAudit(
	t *testing.T,
	backlog taskorchestration.DecisionProjectionBacklog,
	auditFactID taskorchestration.AuditFactID,
) taskorchestration.ProjectionDeliveryEvidence {
	t.Helper()
	for _, evidence := range backlog.Evidence {
		if evidence.AuditFactID == auditFactID {
			return evidence
		}
	}
	t.Fatalf("projection evidence for audit %s is missing: %+v", auditFactID, backlog)
	return taskorchestration.ProjectionDeliveryEvidence{}
}

func TestProtectedDiagnosticAccessAuditDeliveryIsRebuildable(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 15, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := &failingDecisionProjectionSink{healthy: true}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: sink,
			Telemetry:     sink,
		},
	)
	if err != nil {
		t.Fatalf("create diagnostic audit projection adapter: %v", err)
	}
	adapter := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "diagnostic-projection-owner"),
		taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := adapter.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "diagnostic-projection-start", "diagnostic-projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit diagnostic projection Task: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "diagnostic-projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonIntegrity,
	)
	view, err := adapter.Diagnose(
		context.Background(),
		taskorchestration.NewDecisionDiagnosticQuery(
			administrator, taskID(t, "diagnostic-projection-task"), decision.DecisionID,
		),
	)
	if err != nil {
		t.Fatalf("record protected diagnostic access audit: %v", err)
	}
	request := taskorchestration.NewProjectionDeliveryRebuildRequest(
		administrator, taskID(t, "diagnostic-projection-task"), 10,
	)
	if _, err := adapter.RebuildDecisionProjectionDelivery(context.Background(), request); err != nil {
		t.Fatalf("rebuild diagnostic access audit delivery: %v", err)
	}
	projected, found := sink.auditFor(view.AccessAuditFactRef.AuditFactID)
	if !found || projected.FactKind != taskorchestration.ExternalAuditDiagnosticAccessFact ||
		projected.CanonicalDigest != view.AccessAuditFactRef.CanonicalDigest ||
		taskorchestration.ExternalAuditProjectionDigest(projected) != projected.CanonicalDigest ||
		sink.auditFactCount(view.AccessAuditFactRef.AuditFactID) != 1 {
		t.Fatalf("diagnostic access audit was not projected canonically: projected=%+v found=%t", projected, found)
	}
	restarted := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
	})
	if _, err := restarted.RebuildDecisionProjectionDelivery(context.Background(), request); err != nil {
		t.Fatalf("rebuild diagnostic access audit delivery after restart: %v", err)
	}
	if sink.auditFactCount(view.AccessAuditFactRef.AuditFactID) != 1 {
		t.Fatalf("delivered diagnostic access audit was projected more than once")
	}
}

func TestProjectionDeliverySuccessIsMonotonicAcrossConcurrentRebuilds(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 20, 0, 0, time.UTC)
	db, schema := isolatedPostgresSchema(t)
	sink := newInterleavingProjectionSink()
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{
			ExternalAudit: sink,
			Telemetry:     sink,
		},
	)
	if err != nil {
		t.Fatalf("create concurrent projection adapter: %v", err)
	}
	older := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
	})
	newer := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now.Add(time.Second) }, ProjectionDelivery: projector,
	})
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "concurrent-projection-owner"),
		taskorchestration.AuthorizationGeneration(1),
	)
	decision, err := older.Decide(context.Background(), minimalPinnedStartIntent(t,
		intentHeader(t, "concurrent-projection-start", "concurrent-projection-task", now), owner,
	))
	if err != nil {
		t.Fatalf("commit concurrent projection Task: %v", err)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "concurrent-projection-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	rebuild := taskorchestration.NewProjectionDeliveryRebuildRequest(
		administrator, taskID(t, "concurrent-projection-task"), 10,
	)
	olderDone := make(chan error, 1)
	go func() {
		_, rebuildErr := older.RebuildDecisionProjectionDelivery(context.Background(), rebuild)
		olderDone <- rebuildErr
	}()
	<-sink.firstAuditStarted
	if _, err := newer.RebuildDecisionProjectionDelivery(context.Background(), rebuild); err != nil {
		t.Fatalf("newer concurrent projection attempt: %v", err)
	}
	close(sink.releaseFirstAudit)
	if err := <-olderDone; err != nil {
		t.Fatalf("older concurrent projection attempt: %v", err)
	}
	before, err := older.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "concurrent-projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil {
		t.Fatalf("query Task after concurrent projection attempts: %v", err)
	}
	backlog, err := newer.InspectDecisionProjectionBacklog(
		context.Background(),
		taskorchestration.NewProjectionDeliveryInspectionRequest(
			administrator, taskID(t, "concurrent-projection-task"), 10,
		),
	)
	if err != nil {
		t.Fatalf("inspect concurrent projection evidence: %v", err)
	}
	var evidence taskorchestration.ProjectionDeliveryEvidence
	for _, candidate := range backlog.Evidence {
		if candidate.AuditFactID == decision.MandatoryAuditFactRef.AuditFactID {
			evidence = candidate
			break
		}
	}
	if !evidence.ExternalAuditDelivered || !evidence.TelemetryDelivered ||
		evidence.FirstExternalAuditDeliveredAt.IsZero() ||
		evidence.LastExternalAuditDeliveredAt.IsZero() ||
		!evidence.FirstExternalAuditDeliveredAt.Equal(now.Add(time.Second)) ||
		!evidence.LastExternalAuditDeliveredAt.Equal(now.Add(time.Second)) {
		t.Fatalf("older failed attempt regressed successful delivery evidence: %+v", evidence)
	}
	after, err := newer.Query(context.Background(), taskorchestration.TaskQuery{
		TaskID:    taskID(t, "concurrent-projection-task"),
		Authority: taskorchestration.NewUserQueryAuthority(owner),
	})
	if err != nil || after.TaskRevision != before.TaskRevision ||
		after.DecisionCount != before.DecisionCount || after.LatestDecisionID != before.LatestDecisionID {
		t.Fatalf("concurrent projection delivery changed Task state: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestBoundedTelemetryUsesTypedLabelsAndAllowlistedLogsAndTraces(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 30, 0, 0, time.UTC)
	auditFaults := &taskorchestration.DiagnosticAuditFaultController{}
	telemetry := taskorchestration.NewDeterministicTelemetry(
		taskorchestration.DeterministicTelemetryConfig{
			Now: func() time.Time { return now }, DiagnosticAuditFaults: auditFaults,
		},
	)
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
		verifiedPinnedStartIntent(t,
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
			TaskID:        taskID(t, "telemetry-task"),
			DecisionID:    work.DecisionID, OperationID: work.EnactmentRefs[0].OperationID,
			TaskRevision: work.AcceptedTaskRevision,
			Outcome:      taskorchestration.TelemetryReconciliationRequired,
			Category:     taskorchestration.TelemetryCategoryDependency,
			ObservedAt:   now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("project reconciliation health: %v", err)
	}
	invalidProjectionErr := telemetry.ProjectReconciliation(
		context.Background(),
		taskorchestration.ReconciliationTelemetryProjection{
			SchemaVersion: taskorchestration.ProjectionSchemaV1,
			TaskID:        taskID(t, "telemetry-task"),
			DecisionID:    work.DecisionID, OperationID: work.EnactmentRefs[0].OperationID,
			TaskRevision: work.AcceptedTaskRevision,
			Outcome:      taskorchestration.TelemetryReconciliationRequired,
			Category:     taskorchestration.TelemetryCategory(255),
			ObservedAt:   now.Add(3 * time.Second),
		},
	)
	var invalidProjection *taskorchestration.ProjectionError
	if !errors.As(invalidProjectionErr, &invalidProjection) ||
		invalidProjection.Code() != taskorchestration.ProjectionInvalidFact {
		t.Fatalf("unknown metric category was not rejected: %T %v", invalidProjectionErr, invalidProjectionErr)
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "telemetry-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	diagnosticQuery := taskorchestration.NewTelemetryDiagnosticQuery(
		administrator, taskID(t, "telemetry-task"), 10,
	)
	auditFaults.FailNext()
	_, err = telemetry.Snapshot(context.Background(), diagnosticQuery)
	requireSharedDecisionError(t, err, taskorchestration.ErrorDependencyUnavailable)
	snapshot, err := telemetry.Snapshot(context.Background(), diagnosticQuery)
	if err != nil || snapshot.AccessAuditFactRef.AuditFactID.String() == "" ||
		snapshot.AccessAuditFactRef.Outcome != taskorchestration.DiagnosticAuditAccepted {
		t.Fatalf("authorized telemetry snapshot audit: snapshot=%+v err=%v", snapshot, err)
	}
	if len(snapshot.Metrics) != 4 || len(snapshot.Logs) != 3 || len(snapshot.Traces) != 3 {
		t.Fatalf("bounded telemetry snapshot omitted signals: %+v", snapshot)
	}
	if taskorchestration.MetricSeriesUpperBound() == 0 ||
		taskorchestration.MetricSeriesUpperBound() > 512 {
		t.Fatalf("metric series bound is absent or unexpectedly unbounded: %d",
			taskorchestration.MetricSeriesUpperBound())
	}
	for _, metric := range snapshot.Metrics {
		if !taskorchestration.RegisteredMetricSample(metric) {
			t.Fatalf("metric escaped the closed registry: %+v", metric)
		}
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
	if snapshot.Metrics[3].Name != taskorchestration.MetricCardinalityRejectionCount ||
		snapshot.Metrics[3].Labels.Outcome != taskorchestration.TelemetryRejected ||
		snapshot.Metrics[3].Labels.Kind != taskorchestration.TelemetryReconciliation ||
		snapshot.Metrics[3].Labels.Category != taskorchestration.TelemetryCategoryInvalid {
		t.Fatalf("metric policy rejection did not use bounded labels: %+v", snapshot.Metrics[3])
	}
	for _, log := range snapshot.Logs {
		if log.SchemaVersion != taskorchestration.StructuredLogSchemaV1 ||
			(log.Severity != taskorchestration.StructuredLogInfo &&
				log.Severity != taskorchestration.StructuredLogWarning) {
			t.Fatalf("ordinary structured log lacks registered schema or safe severity: %+v", log)
		}
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
	for _, trace := range snapshot.Traces {
		if trace.TaskID != taskID(t, "telemetry-task") {
			t.Fatalf("telemetry diagnostic escaped exact Task scope: %+v", trace)
		}
	}
	bounded, err := telemetry.Snapshot(
		context.Background(),
		taskorchestration.NewTelemetryDiagnosticQuery(
			administrator, taskID(t, "telemetry-task"), 1,
		),
	)
	if err != nil || len(bounded.Metrics) > 1 || len(bounded.Logs) > 1 || len(bounded.Traces) > 1 {
		t.Fatalf("telemetry diagnostic result is not bounded: snapshot=%+v err=%v", bounded, err)
	}
}

func TestTelemetryDiagnosticSnapshotIsolatesEverySignalToTheAuthorizedTask(t *testing.T) {
	now := time.Date(2026, time.July, 27, 22, 45, 0, 0, time.UTC)
	telemetry := taskorchestration.NewDeterministicTelemetry(
		taskorchestration.DeterministicTelemetryConfig{Now: func() time.Time { return now }},
	)
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
		t.Fatalf("create Task-scoped telemetry projector: %v", err)
	}
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create Task-scoped telemetry harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "scoped-telemetry-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	for _, value := range []string{"scoped-telemetry-task-a", "scoped-telemetry-task-b"} {
		decision, decideErr := harness.Mutations.Decide(
			context.Background(),
			minimalPinnedStartIntent(t,
				intentHeader(t, value+"-start", value, now), owner,
			),
		)
		if decideErr != nil {
			t.Fatalf("commit %s: %v", value, decideErr)
		}
		if projectErr := projector.ObserveCommittedDecision(context.Background(), decision); projectErr != nil {
			t.Fatalf("project %s: %v", value, projectErr)
		}
	}
	administrator := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "scoped-telemetry-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	snapshot, err := telemetry.Snapshot(
		context.Background(),
		taskorchestration.NewTelemetryDiagnosticQuery(
			administrator, taskID(t, "scoped-telemetry-task-a"), 10,
		),
	)
	if err != nil {
		t.Fatalf("inspect Task-scoped telemetry: %v", err)
	}
	if len(snapshot.Metrics) != 1 || len(snapshot.Logs) != 1 || len(snapshot.Traces) != 1 {
		t.Fatalf("Task diagnostic included another Task's signals: %+v", snapshot)
	}
	if snapshot.Metrics[0].Name != taskorchestration.MetricDecisionCount ||
		snapshot.Metrics[0].Labels.Kind != taskorchestration.TelemetryDecision ||
		snapshot.Logs[0].Event != taskorchestration.StructuredLogDecisionCommitted ||
		snapshot.Traces[0].TaskID != taskID(t, "scoped-telemetry-task-a") {
		t.Fatalf("Task diagnostic returned the wrong scoped signals: %+v", snapshot)
	}
}

type failingDecisionProjectionSink struct {
	mu         sync.Mutex
	audits     uint64
	telemetry  uint64
	healthy    bool
	audit      taskorchestration.ExternalAuditProjection
	auditsSeen []taskorchestration.ExternalAuditProjection
}

type interleavingProjectionSink struct {
	mu                sync.Mutex
	auditCalls        uint64
	firstAuditStarted chan struct{}
	releaseFirstAudit chan struct{}
}

type splitProjectionSink struct {
	auditCalls       uint64
	telemetryCalls   uint64
	telemetryHealthy bool
}

func (sink *splitProjectionSink) ProjectExternalAudit(
	context.Context,
	taskorchestration.ExternalAuditProjection,
) error {
	sink.auditCalls++
	return nil
}

func (sink *splitProjectionSink) ProjectTelemetry(
	context.Context,
	taskorchestration.DecisionTelemetryProjection,
) error {
	sink.telemetryCalls++
	if sink.telemetryHealthy {
		return nil
	}
	return errors.New("controlled telemetry failure")
}

func newInterleavingProjectionSink() *interleavingProjectionSink {
	return &interleavingProjectionSink{
		firstAuditStarted: make(chan struct{}),
		releaseFirstAudit: make(chan struct{}),
	}
}

func (sink *interleavingProjectionSink) ProjectExternalAudit(
	_ context.Context,
	_ taskorchestration.ExternalAuditProjection,
) error {
	sink.mu.Lock()
	sink.auditCalls++
	call := sink.auditCalls
	sink.mu.Unlock()
	if call == 1 {
		close(sink.firstAuditStarted)
		<-sink.releaseFirstAudit
		return errors.New("older controlled projection failure")
	}
	return nil
}

func (sink *interleavingProjectionSink) ProjectTelemetry(
	_ context.Context,
	_ taskorchestration.DecisionTelemetryProjection,
) error {
	return nil
}

func (sink *failingDecisionProjectionSink) ProjectExternalAudit(
	_ context.Context,
	projection taskorchestration.ExternalAuditProjection,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.audits++
	sink.audit = projection
	sink.auditsSeen = append(sink.auditsSeen, projection)
	if sink.healthy {
		return nil
	}
	return errors.New("credential-canary at /private/session")
}

func (sink *failingDecisionProjectionSink) ProjectTelemetry(
	_ context.Context,
	_ taskorchestration.DecisionTelemetryProjection,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.telemetry++
	if sink.healthy {
		return nil
	}
	return errors.New("content-canary at object-locator")
}

func (sink *failingDecisionProjectionSink) setHealthy() {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.healthy = true
}

func (sink *failingDecisionProjectionSink) lastAudit() taskorchestration.ExternalAuditProjection {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.audit
}

func (sink *failingDecisionProjectionSink) auditCount() uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.audits
}

func (sink *failingDecisionProjectionSink) auditFor(
	auditFactID taskorchestration.AuditFactID,
) (taskorchestration.ExternalAuditProjection, bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, projection := range sink.auditsSeen {
		if projection.AuditFactID == auditFactID {
			return projection, true
		}
	}
	return taskorchestration.ExternalAuditProjection{}, false
}

func (sink *failingDecisionProjectionSink) auditFactCount(
	auditFactID taskorchestration.AuditFactID,
) uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var count uint64
	for _, projection := range sink.auditsSeen {
		if projection.AuditFactID == auditFactID {
			count++
		}
	}
	return count
}

func (sink *failingDecisionProjectionSink) telemetryCount() uint64 {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.telemetry
}
