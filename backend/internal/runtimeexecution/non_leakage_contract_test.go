package runtimeexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var decision20Canaries = []string{
	"source-content-canary",
	"prompt-instruction-canary",
	"tool-param-canary",
	"raw-worker-provider-error-canary",
	"credential=bearer-token-canary",
	"host=/private/runtime/path-canary",
	"session=agent-session-canary",
	"mount=/mnt/workspace-canary",
	"object-key=s3://bucket/object-locator-canary",
	"registry-locator=registry.example/private/image-canary",
	"url=https://private.endpoint.example/query?token=url-canary",
	"foreign-workspace-exists-canary",
}

func decision20RawFailure() error {
	return errors.New(strings.Join(decision20Canaries, " | "))
}

// TestCanaryNonLeakageAcrossEveryDecision20Surface injects every Decision 20
// canary through a hostile adapter failure and proves no surface — public
// types, strict wire, PostgreSQL safe records, Runtime Evidence, audit,
// logs, traces, metrics, or errors — retains them.
func TestCanaryNonLeakageAcrossEveryDecision20Surface(t *testing.T) {
	now := time.Date(2026, time.July, 31, 13, 0, 0, 0, time.UTC)
	rawFailure := decision20RawFailure()

	var telemetryProjections []RuntimeTelemetryProjection
	sink := &canaryTelemetrySink{projections: &telemetryProjections}
	projector := ProjectionDeliveryFunc(func(ctx context.Context, fact ProjectionFact) error {
		return rawFailure
	})

	db, schema, config, store, start, _, _ := newPostgresWaitingRuntimeBindingRejection(
		t, "nonleak_surface", now, time.Time{}, nil, PrerequisiteFailureIncompatible, false,
	)
	config.RuntimeBindingValidator = RuntimeBindingValidatorFunc(func(
		context.Context,
		RuntimeBindingValidationRequest,
	) (PrerequisiteObservation, error) {
		return PrerequisiteObservation{}, rawFailure
	})
	config.ProjectionDelivery = projector
	config.Telemetry = sink
	store, err := NewPostgresAuthority(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	decision, err := store.Execute(context.Background(), start)
	if err != nil || decision.Snapshot.Readiness.RuntimeBinding.State != PrerequisiteReconciliationRequired {
		t.Fatalf("raw binding failure: %+v err=%v", decision, err)
	}

	// Public surface and errors.
	public := fmt.Sprintf("%+v %v", decision, err)
	assertAbsentCanaries(t, "public Execute result", public, decision20Canaries)

	// Telemetry projections (metrics/logs/traces sources).
	for _, projection := range telemetryProjections {
		assertAbsentCanaries(t, "telemetry projection", fmt.Sprintf("%+v", projection), decision20Canaries)
		if !validRuntimeTelemetryProjection(projection) {
			t.Fatalf("telemetry projection escaped bounded validation: %+v", projection)
		}
	}

	// Mandatory audit facts and projection backlog.
	ref := postgresMandatoryAuditRef{
		DecisionID: decision.Fact.DecisionID, PersonalWorkspaceID: start.PersonalWorkspaceID,
		RuntimeRunID: start.RuntimeRunID, OperationID: start.OperationID,
		RequestDigest: start.CanonicalRequestDigest, Authority: start.Authority,
	}
	view, err := store.loadMandatoryAudit(context.Background(), ref)
	if err != nil {
		t.Fatalf("load mandatory audit for canary surface: %v", err)
	}
	assertAbsentCanaries(t, "mandatory audit", fmt.Sprintf("%+v", view), decision20Canaries)

	// Runtime Evidence (accepted evidence roots and reconciliation obligations).
	backlog, err := store.InspectProjectionBacklog(context.Background(),
		NewProjectionDeliveryInspectionRequest(
			mustAdministratorAuthority(t, "nonleak-admin", 22), start.RuntimeRunID, 10,
		))
	if err != nil {
		t.Fatalf("inspect projection backlog: %v", err)
	}
	assertAbsentCanaries(t, "projection backlog", fmt.Sprintf("%+v", backlog), decision20Canaries)

	// PostgreSQL safe records across every authoritative C03 table.
	assertNoPostgresCanaries(t, db, schema, decision20Canaries)
}

func assertAbsentCanaries(t *testing.T, surface string, text string, canaries []string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(text, canary) {
			t.Fatalf("forbidden canary %q leaked from %s", canary, surface)
		}
	}
}

func assertNoPostgresCanaries(t *testing.T, db *sql.DB, schema string, canaries []string) {
	t.Helper()
	// Serialize every owned table row (bytea columns via escape encoding, which
	// preserves printable text verbatim) and prove no canary appears anywhere
	// in authoritative PostgreSQL state.
	var tables []string
	rows, err := db.QueryContext(context.Background(),
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema=$1 AND table_type='BASE TABLE' ORDER BY table_name`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	scans := make([]string, 0, len(tables))
	for _, table := range tables {
		var columns []string
		var dataTypes []string
		columnRows, err := db.QueryContext(context.Background(),
			`SELECT column_name, data_type FROM information_schema.columns
			 WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, table)
		if err != nil {
			t.Fatal(err)
		}
		for columnRows.Next() {
			var columnName, dataType string
			if err := columnRows.Scan(&columnName, &dataType); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, columnName)
			dataTypes = append(dataTypes, dataType)
		}
		columnRows.Close()
		if err := columnRows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(columns) == 0 {
			continue
		}
		expressions := make([]string, 0, len(columns))
		for index, column := range columns {
			if dataTypes[index] == "bytea" {
				expressions = append(expressions, fmt.Sprintf("encode(%s,'escape')", column))
			} else {
				expressions = append(expressions, fmt.Sprintf("(%s)::text", column))
			}
		}
		scans = append(scans, fmt.Sprintf(
			"SELECT coalesce(string_agg(concat_ws('|', %s), ''), '') FROM %s.%s",
			strings.Join(expressions, ", "), schema, table))
	}
	allRows, err := db.QueryContext(context.Background(), strings.Join(scans, " UNION ALL "))
	if err != nil {
		t.Fatal(err)
	}
	defer allRows.Close()
	for allRows.Next() {
		var retained string
		if err := allRows.Scan(&retained); err != nil {
			t.Fatal(err)
		}
		assertAbsentCanaries(t, "PostgreSQL safe records", retained, canaries)
	}
	if err := allRows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestCanaryNonLeakagePublicAndObservabilityTypes proves the public and
// observability types are structurally incapable of carrying content, paths,
// sessions, mounts, locators, credentials, messages, or arbitrary attributes.
func TestCanaryNonLeakagePublicAndObservabilityTypes(t *testing.T) {
	t.Parallel()
	for _, surface := range []any{
		RuntimeDecisionFact{},
		RuntimeDecision{},
		RuntimeSnapshot{},
		RuntimeOperationBinding{},
		RuntimeLeaseSnapshot{},
		RuntimeNodeSnapshot{},
		RuntimeCancellationSnapshot{},
		EvidenceRootSnapshot{},
		RuntimeCapacitySnapshot{},
		RuntimeCapacityEvidenceSnapshot{},
		RuntimeViewBindingSnapshot{},
		GatewayPrerequisiteSnapshot{},
		RuntimeUsageEvidenceSnapshot{},
		RuntimeCapsuleSnapshot{},
		RuntimeWorkerSnapshot{},
		Error{},
		RuntimeTelemetryProjection{},
		MetricLabels{},
		MetricSample{},
		StructuredLogRecord{},
		TraceSpanRecord{},
		OperationalDiagnosticView{},
		NodeDiagnosticView{},
		RuntimeDiagnosticView{},
		AdapterNormalizedError{},
	} {
		assertAllowlistedNonLeakageSurface(t, reflect.TypeOf(surface))
	}
}

func assertAllowlistedNonLeakageSurface(t *testing.T, surface reflect.Type) {
	t.Helper()
	for index := 0; index < surface.NumField(); index++ {
		field := surface.Field(index)
		name := strings.ToLower(field.Name)
		for _, forbidden := range []string{
			"content", "path", "session", "mount", "locator", "credential", "secret",
			"message", "attribute", "baggage", "error", "prompt", "url",
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

// PostgresProjectionFact is the public projection identity (alias for tests).
type PostgresProjectionFact = ProjectionFact

type canaryTelemetrySink struct {
	projections *[]RuntimeTelemetryProjection
}

func (sink *canaryTelemetrySink) ProjectTelemetry(
	_ context.Context,
	projection RuntimeTelemetryProjection,
) error {
	*sink.projections = append(*sink.projections, projection)
	return decision20RawFailure()
}
