package runtimeexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBoundedMetricRegistryRejectsBusinessIDsAndFreeFormValues(t *testing.T) {
	t.Parallel()

	if bound := MetricSeriesUpperBound(); bound == 0 || bound > 20000 {
		t.Fatalf("metric series bound is absent or unexpectedly unbounded: %d", bound)
	}

	valid := []MetricSample{
		{
			Name: MetricRunStateCount,
			Labels: MetricLabels{
				Operation: MetricOperationStart, State: MetricStateWaitingForLease,
				Outcome: MetricOutcomeNone, Category: TelemetryCategoryNone,
			},
			Count: 1,
		},
		{
			Name: MetricTerminalOutcomeCount,
			Labels: MetricLabels{
				Operation: MetricOperationTerminal, State: MetricStateTerminal,
				Outcome: MetricOutcomeSucceeded, Category: TelemetryCategoryNone,
			},
			Count: 1,
		},
		{
			Name: MetricLeaseOperationCount,
			Labels: MetricLabels{
				Operation: MetricOperationLease, State: MetricStateRunning,
				Category: TelemetryCategoryNone,
			},
			Count: 1,
		},
		{
			Name: MetricSafeErrorCount,
			Labels: MetricLabels{
				Operation: MetricOperationStart, State: MetricStateAccepted,
				Category: TelemetryCategoryAuthorization,
			},
			Count: 1,
		},
		{
			Name: MetricEvidenceStateCount,
			Labels: MetricLabels{
				Operation: MetricOperationEvidence, Evidence: MetricEvidenceAccepted,
			},
			Count: 1,
		},
		{
			Name: MetricCleanupDebtCount,
			Labels: MetricLabels{
				Operation: MetricOperationCleanup, Category: TelemetryCategoryCleanup,
			},
			Count: 1,
		},
	}
	for _, sample := range valid {
		if !RegisteredMetricSample(sample) {
			t.Fatalf("registered sample was rejected: %+v", sample)
		}
	}

	invalid := []MetricSample{
		{Name: MetricName(0), Labels: MetricLabels{}, Count: 1},
		{Name: MetricName(255), Labels: MetricLabels{}, Count: 1},
		{Name: MetricRunStateCount, Labels: MetricLabels{}, Count: 0},
		{Name: MetricRunStateCount, Labels: MetricLabels{Operation: MetricOperation(255)}, Count: 1},
		{Name: MetricRunStateCount, Labels: MetricLabels{State: MetricState(255)}, Count: 1},
		{Name: MetricRunStateCount, Labels: MetricLabels{Category: TelemetryCategory(255)}, Count: 1},
		{Name: MetricRunStateCount, Labels: MetricLabels{Worker: MetricWorker(255)}, Count: 1},
		{Name: MetricRunStateCount, Labels: MetricLabels{Adapter: MetricAdapter(255)}, Count: 1},
		{Name: MetricRunStateCount, Labels: MetricLabels{Evidence: MetricEvidence(255)}, Count: 1},
		// A terminal outcome is not registered for the run-state metric.
		{Name: MetricRunStateCount, Labels: MetricLabels{
			Operation: MetricOperationStart, State: MetricStateTerminal,
			Outcome: MetricOutcomeSucceeded, Category: TelemetryCategoryNone,
		}, Count: 1},
		// A non-terminal operation is not registered for the terminal metric.
		{Name: MetricTerminalOutcomeCount, Labels: MetricLabels{
			Operation: MetricOperationStart, State: MetricStateTerminal,
			Outcome: MetricOutcomeSucceeded, Category: TelemetryCategoryNone,
		}, Count: 1},
	}
	for _, sample := range invalid {
		if RegisteredMetricSample(sample) {
			t.Fatalf("unregistered sample escaped the allowlist: %+v", sample)
		}
	}
}

func TestTelemetrySurfacesNeverCarryBusinessIdentitiesOrFreeFormText(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 10, 0, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "telemetry-caller", 7)
	start := standardStart(t, now, authority, "telemetry")
	runtimeRunID := start.RuntimeRunID.String()
	operationID := start.OperationID.String()
	workspaceID := start.PersonalWorkspaceID.String()

	telemetry := NewDeterministicTelemetry(DeterministicTelemetryConfig{Now: func() time.Time { return now }})
	projections := []RuntimeTelemetryProjection{
		{
			SchemaVersion: TelemetrySchemaV1, DecisionID: RuntimeDecisionID{value: "runtime-decision-telemetry-1"},
			RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
			RuntimeRevision: start.ExpectedRuntimeRevision + 1,
			Operation:       MetricOperationStart, State: MetricStateWaitingForLease,
			Outcome: MetricOutcomeNone, Category: TelemetryCategoryNone, RecordedAt: now,
		},
		{
			SchemaVersion: TelemetrySchemaV1, DecisionID: RuntimeDecisionID{value: "runtime-decision-telemetry-2"},
			RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
			RuntimeRevision: start.ExpectedRuntimeRevision + 2,
			Operation:       MetricOperationLease, State: MetricStateRunning,
			Outcome: MetricOutcomeNone, Category: TelemetryCategoryNone, RecordedAt: now.Add(time.Second),
		},
		{
			SchemaVersion: TelemetrySchemaV1, DecisionID: RuntimeDecisionID{value: "runtime-decision-telemetry-3"},
			RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
			RuntimeRevision: start.ExpectedRuntimeRevision + 3,
			Operation:       MetricOperationTerminal, State: MetricStateTerminal,
			Outcome: MetricOutcomeSucceeded, Category: TelemetryCategoryNone,
			TraceID:    TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
			RecordedAt: now.Add(2 * time.Second),
		},
	}
	for _, projection := range projections {
		if err := telemetry.ProjectTelemetry(context.Background(), projection); err != nil {
			t.Fatalf("project telemetry: %v", err)
		}
	}
	if err := telemetry.ProjectTelemetry(context.Background(), RuntimeTelemetryProjection{
		SchemaVersion: TelemetrySchemaV1, RuntimeRunID: start.RuntimeRunID,
		OperationID: start.OperationID, RuntimeRevision: 1,
		Operation: MetricOperation(255), RecordedAt: now,
	}); err == nil {
		t.Fatal("invalid metric operation was accepted")
	}
	var projectionFailure *ProjectionError
	if !errors.As(telemetry.ProjectTelemetry(context.Background(), RuntimeTelemetryProjection{
		SchemaVersion: TelemetrySchemaV1, RuntimeRunID: start.RuntimeRunID,
		OperationID: start.OperationID, RuntimeRevision: 1,
		Operation: MetricOperationStart, State: MetricState(255), RecordedAt: now,
	}), &projectionFailure) || projectionFailure.Code() != ProjectionInvalidFact {
		t.Fatalf("unregistered metric state was not rejected: %v", projectionFailure)
	}

	administrator := mustAdministratorAuthority(t, "telemetry-admin", 22)
	snapshot, err := telemetry.Snapshot(context.Background(),
		NewTelemetryDiagnosticQuery(administrator, start.RuntimeRunID, DiagnosticReasonCleanupHealth, 10))
	if err != nil {
		t.Fatalf("telemetry snapshot: %v", err)
	}
	if len(snapshot.Metrics) != 5 || len(snapshot.Logs) != 3 || len(snapshot.Traces) != 3 {
		t.Fatalf("bounded telemetry snapshot omitted signals: %+v", snapshot)
	}
	if snapshot.Metrics[3].Name != MetricCardinalityRejectionCount ||
		snapshot.Metrics[3].Labels.Category != TelemetryCategoryInvalid ||
		snapshot.Metrics[4].Name != MetricCardinalityRejectionCount ||
		snapshot.Metrics[4].Labels.Category != TelemetryCategoryInvalid {
		t.Fatalf("cardinality rejection did not use bounded labels: %+v", snapshot.Metrics)
	}
	for _, metric := range snapshot.Metrics {
		if !RegisteredMetricSample(metric) {
			t.Fatalf("metric escaped the closed registry: %+v", metric)
		}
	}
	for _, logRecord := range snapshot.Logs {
		if logRecord.SchemaVersion != StructuredLogSchemaV1 ||
			(logRecord.Severity != StructuredLogInfo && logRecord.Severity != StructuredLogWarning) {
			t.Fatalf("structured log lacks registered schema or safe severity: %+v", logRecord)
		}
	}
	for _, trace := range snapshot.Traces {
		if trace.RuntimeRunID != start.RuntimeRunID {
			t.Fatalf("trace escaped exact Runtime Run scope: %+v", trace)
		}
	}
	// Metrics and ordinary logs must never carry business identities, paths,
	// locators, credentials, or free-form text.
	for _, metric := range snapshot.Metrics {
		text := fmt.Sprintf("%+v", metric.Labels)
		for _, forbidden := range []string{runtimeRunID, operationID, workspaceID, "telemetry", "path", "credential", "secret", "token", "/"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("metric labels contain high-cardinality value %q: %+v", forbidden, metric)
			}
		}
	}
	for _, logRecord := range snapshot.Logs {
		text := fmt.Sprintf("%+v", logRecord)
		for _, forbidden := range []string{runtimeRunID, operationID, workspaceID, "credential", "secret", "token"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("ordinary structured log contains protected value %q: %+v", forbidden, logRecord)
			}
		}
	}
	// TraceID is allowlisted protected correlation and diagnostic-only.
	if snapshot.Traces[2].TraceID == (TraceID{}) {
		t.Fatal("trace correlation dropped the diagnostic TraceID")
	}
	if snapshot.Traces[2].RuntimeRunID != start.RuntimeRunID ||
		snapshot.Traces[2].OperationID != start.OperationID {
		t.Fatalf("allowlisted protected trace correlation is missing: %+v", snapshot.Traces[2])
	}
	bounded, err := telemetry.Snapshot(context.Background(),
		NewTelemetryDiagnosticQuery(administrator, start.RuntimeRunID, DiagnosticReasonCleanupHealth, 1))
	if err != nil || len(bounded.Metrics) > 1 || len(bounded.Logs) > 1 || len(bounded.Traces) > 1 {
		t.Fatalf("telemetry diagnostic result is not bounded: snapshot=%+v err=%v", bounded, err)
	}
}

func TestTelemetryDiagnosticRequiresAdministratorAndExactRuntimeRun(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 0, 0, 0, time.UTC)
	telemetry := NewDeterministicTelemetry(DeterministicTelemetryConfig{Now: func() time.Time { return now }})
	runtimeRunID := mustRuntimeRunID(t, "diagnostic-scope")
	operationID := mustOperationID(t, "diagnostic-scope-operation")
	if err := telemetry.ProjectTelemetry(context.Background(), RuntimeTelemetryProjection{
		SchemaVersion: TelemetrySchemaV1, DecisionID: RuntimeDecisionID{value: "runtime-decision-diag-1"},
		RuntimeRunID: runtimeRunID, OperationID: operationID, RuntimeRevision: 1,
		Operation: MetricOperationStart, State: MetricStateAccepted,
		Outcome: MetricOutcomeNone, Category: TelemetryCategoryNone, RecordedAt: now,
	}); err != nil {
		t.Fatalf("project scoped telemetry: %v", err)
	}
	user := mustTaskOrchestrationAuthority(t, "diag-user", 7)
	if _, err := telemetry.Snapshot(context.Background(),
		NewTelemetryDiagnosticQuery(user, runtimeRunID, DiagnosticReasonCleanupHealth, 10)); err == nil {
		t.Fatal("non-administrator read protected telemetry diagnostics")
	}
	administrator := mustAdministratorAuthority(t, "diag-admin", 22)
	if _, err := telemetry.Snapshot(context.Background(),
		NewTelemetryDiagnosticQuery(administrator, mustRuntimeRunID(t, "other-run"), DiagnosticReasonNodeHealth, 10)); err != nil {
		t.Fatalf("exact non-enumerating query for another run failed: %v", err)
	}
	other, err := telemetry.Snapshot(context.Background(),
		NewTelemetryDiagnosticQuery(administrator, mustRuntimeRunID(t, "other-run"), DiagnosticReasonNodeHealth, 10))
	if err != nil || len(other.Metrics) != 0 || len(other.Logs) != 0 || len(other.Traces) != 0 {
		t.Fatalf("exact query for another run enumerated signals: %+v err=%v", other, err)
	}
	if _, err := telemetry.Snapshot(context.Background(),
		NewTelemetryDiagnosticQuery(administrator, runtimeRunID, DiagnosticReasonCleanupHealth, 0)); err == nil {
		t.Fatal("unbounded telemetry diagnostic was accepted")
	}
}

func TestTraceIDIsDiagnosticContextAndNeverBecomesAKey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 31, 11, 30, 0, 0, time.UTC)
	authority := mustTaskOrchestrationAuthority(t, "trace-key-caller", 7)
	start := standardStart(t, now, authority, "trace-key")
	start.Trace.TraceID = TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	plain := start
	plain.Trace.TraceID = TraceID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	harness := harnessForStart(t, now, authority, start)
	accepted, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil {
		t.Fatalf("execute traced start: %v", err)
	}
	replayed, err := harness.Runtime.Execute(context.Background(), start)
	if err != nil || replayed != accepted {
		t.Fatalf("traced exact replay diverged: %+v err=%v", replayed, err)
	}
	// Changing the TraceID must NOT create a new decision, revision, or key:
	// TraceID is diagnostic context, never idempotency/ownership/fence key.
	changed, err := harness.Runtime.Execute(context.Background(), plain)
	if err != nil || changed != accepted {
		t.Fatalf("TraceID changed authoritative replay identity: %+v err=%v", changed, err)
	}
	if accepted.Fact.DecisionID == (RuntimeDecisionID{}) ||
		accepted.Snapshot.RuntimeRevision != start.ExpectedRuntimeRevision+1 {
		t.Fatalf("TraceID altered accepted decision facts: %+v", accepted)
	}
	// TraceID must not appear in the canonical start digest that binds the
	// operation (already asserted by canonical contract; re-prove here).
	if strings.Contains(accepted.Fact.CanonicalRequestDigest.String(), fmt.Sprintf("%x", start.Trace.TraceID)) {
		t.Fatal("TraceID entered the canonical start digest")
	}
}
