package taskorchestration_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestTaskOrchestrationCanaryRedactionAndNonLeakageContract(t *testing.T) {
	runTaskOrchestrationCanaryRedactionAndNonLeakageContract(t)
}

func runTaskOrchestrationCanaryRedactionAndNonLeakageContract(t *testing.T) {
	t.Helper()
	canaries := []string{
		"source-content-canary",
		"/private/task/path-canary",
		"session=agent-session-canary",
		"mount=/mnt/workspace-canary",
		"s3://bucket/object-locator-canary",
		"credential=bearer-token-canary",
		"foreign-workspace-exists-canary",
	}
	rawFailure := errors.New(strings.Join(canaries, " | "))
	now := time.Date(2026, time.July, 27, 23, 30, 0, 0, time.UTC)
	telemetry := taskorchestration.NewDeterministicTelemetry(
		taskorchestration.DeterministicTelemetryConfig{Now: func() time.Time { return now }},
	)
	sink := &canaryProjectionSink{rawFailure: rawFailure, telemetry: telemetry}
	projector, err := taskorchestration.NewDecisionProjectionAdapter(
		taskorchestration.DecisionProjectionConfig{ExternalAudit: sink, Telemetry: sink},
	)
	if err != nil {
		t.Fatalf("create canary projector: %v", err)
	}
	harness, err := taskorchestration.NewDeterministicHarness(
		taskorchestration.HarnessConfig{Now: now},
	)
	if err != nil {
		t.Fatalf("create canary harness: %v", err)
	}
	owner := taskorchestration.NewUserAuthority(
		authorityID(t, "canary-owner"), taskorchestration.AuthorizationGeneration(1),
	)
	worker := taskorchestration.NewWorkerAuthority(
		authorityID(t, "canary-worker"), taskorchestration.AuthorizationGeneration(1),
	)
	pinned := generationPinnedPipeline(t, []taskorchestration.PhaseDefinition{{
		Key: phaseKey(t, "canary-phase"), Kind: taskorchestration.PhaseNonMutating,
		ValidationContract:  taskorchestration.PhaseValidationAllRuntimeRunsSucceeded,
		RequiredRuntimeRuns: 1,
	}})
	start, err := harness.Mutations.Decide(context.Background(),
		taskorchestration.NewStartPinnedTaskIntent(
			intentHeader(t, "canary-start", "canary-task", now), owner, pinned,
		))
	if err != nil {
		t.Fatalf("start canary Task: %v", err)
	}
	workHeader := intentHeader(t, "canary-work", "canary-task", now.Add(time.Second))
	workHeader.ExpectedTaskRevision = start.AcceptedTaskRevision
	work, err := harness.Mutations.Decide(context.Background(),
		taskorchestration.NewMakeWorkAvailableIntent(
			workHeader, worker, operationID(t, "canary-work-available"),
		))
	if err != nil {
		t.Fatalf("commit canary work: %v", err)
	}
	projectionErr := projector.ObserveCommittedDecision(context.Background(), work)
	var safeProjectionErr *taskorchestration.ProjectionError
	if !errors.As(projectionErr, &safeProjectionErr) ||
		safeProjectionErr.Code() != taskorchestration.ProjectionUnavailable {
		t.Fatalf("raw projection failure was not normalized: %T %v", projectionErr, projectionErr)
	}
	db, schema := isolatedPostgresSchema(t)
	postgres := newPostgresAdapter(t, db, schema, taskorchestration.PostgresConfig{
		Now: func() time.Time { return now }, ProjectionDelivery: projector,
	})
	if _, err := postgres.Decide(
		context.Background(),
		taskorchestration.NewStartTaskIntent(
			intentHeader(t, "canary-postgres-start", "canary-postgres-task", now), owner,
		),
	); err != nil {
		t.Fatalf("commit canary PostgreSQL Decision: %v", err)
	}
	projectionBacklog, err := postgres.RebuildDecisionProjectionDelivery(
		context.Background(), taskorchestration.NewProjectionDeliveryRebuildRequest(
			taskorchestration.NewAdministratorMetadataAuthority(
				authorityID(t, "canary-projection-administrator"),
				taskorchestration.AuthorizationGeneration(1),
				taskorchestration.DiagnosticReasonOperations,
			),
			taskID(t, "canary-postgres-task"), 1,
		),
	)
	if err != nil || projectionBacklog.Pending != 2 || projectionBacklog.SourceFactCount != 2 {
		t.Fatalf("retain canary projection backlog: backlog=%+v err=%v", projectionBacklog, err)
	}

	authority := taskorchestration.NewAdministratorMetadataAuthority(
		authorityID(t, "canary-diagnostic-administrator"),
		taskorchestration.AuthorizationGeneration(1),
		taskorchestration.DiagnosticReasonOperations,
	)
	missingOperation := operationID(t, "foreign-workspace-exists-canary")
	_, diagnosticErr := harness.Diagnostics.Diagnose(
		context.Background(),
		taskorchestration.NewOperationDiagnosticQuery(
			authority, taskID(t, "canary-task"), missingOperation,
		),
	)
	requireSharedDecisionError(t, diagnosticErr, taskorchestration.ErrorAuthorizationDenied)
	telemetrySnapshot, snapshotErr := telemetry.Snapshot(
		context.Background(),
		taskorchestration.NewTelemetryDiagnosticQuery(
			authority, taskID(t, "canary-task"), 10,
		),
	)
	if snapshotErr != nil {
		t.Fatalf("read canary telemetry through protected diagnostics: %v", snapshotErr)
	}

	surfaces := []any{
		sink.audit,
		telemetrySnapshot,
		projectionErr,
		projectionBacklog,
		diagnosticErr,
		taskorchestration.NewDownstreamError(taskorchestration.DownstreamDependencyUnavailable),
	}
	for _, surface := range surfaces {
		text := fmt.Sprintf("%+v", surface)
		for _, canary := range canaries {
			if strings.Contains(text, canary) {
				t.Fatalf("forbidden canary %q leaked from %T", canary, surface)
			}
		}
	}

	for _, value := range []any{
		taskorchestration.ExternalAuditProjection{},
		taskorchestration.DecisionTelemetryProjection{},
		taskorchestration.MetricLabels{},
		taskorchestration.MetricSample{},
		taskorchestration.StructuredLogRecord{},
		taskorchestration.TraceSpanRecord{},
		taskorchestration.ReconciliationTelemetryProjection{},
		taskorchestration.OperationalDiagnosticView{},
		taskorchestration.DiagnosticAuditFactRef{},
		taskorchestration.DecisionProjectionBacklog{},
		taskorchestration.ProjectionDeliveryEvidence{},
	} {
		assertAllowlistedNonLeakageSurface(t, reflect.TypeOf(value))
	}
}

func assertAllowlistedNonLeakageSurface(t *testing.T, surface reflect.Type) {
	t.Helper()
	for index := 0; index < surface.NumField(); index++ {
		field := surface.Field(index)
		name := strings.ToLower(field.Name)
		for _, forbidden := range []string{
			"content", "path", "session", "mount", "locator", "credential", "secret",
			"message", "attribute", "baggage", "error",
		} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("%s exposes forbidden field %s", surface, field.Name)
			}
		}
		if field.Type.Kind() == reflect.String || field.Type.Kind() == reflect.Map ||
			field.Type == reflect.TypeOf((*error)(nil)).Elem() ||
			field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Uint8 {
			t.Fatalf("%s exposes untyped data field %s (%s)", surface, field.Name, field.Type)
		}
	}
}

type canaryProjectionSink struct {
	rawFailure error
	telemetry  *taskorchestration.DeterministicTelemetry
	audit      taskorchestration.ExternalAuditProjection
}

func (sink *canaryProjectionSink) ProjectExternalAudit(
	_ context.Context,
	projection taskorchestration.ExternalAuditProjection,
) error {
	sink.audit = projection
	return sink.rawFailure
}

func (sink *canaryProjectionSink) ProjectTelemetry(
	ctx context.Context,
	projection taskorchestration.DecisionTelemetryProjection,
) error {
	if err := sink.telemetry.ProjectTelemetry(ctx, projection); err != nil {
		return err
	}
	return sink.rawFailure
}
