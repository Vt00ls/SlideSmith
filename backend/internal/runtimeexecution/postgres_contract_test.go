package runtimeexecution

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresRuntimeAggregateCodecRetainsGatewayAndUsageEvidence(t *testing.T) {
	gateway := GatewayPrerequisiteSnapshot{
		Applicability:          GatewayPrerequisiteRequired,
		Status:                 GatewayGrantPending,
		OperationID:            mustOperationID(t, "gateway-codec-operation"),
		CanonicalRequestDigest: digest(41),
		RequestedGeneration:    3,
	}
	usage := RuntimeUsageEvidenceSnapshot{
		Disposition: UsageEvidenceEstimated,
		Receipts: UsageReceiptReferenceSet{
			Count: 37, RootDigest: digest(42),
		},
	}
	fixture := RuntimeFixture{Gateway: gateway, Usage: usage}

	encoded, err := encodePostgresRuntimeFixture(fixture)
	if err != nil {
		t.Fatalf("encode Runtime aggregate: %v", err)
	}
	var persisted postgresRuntimeState
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatalf("decode Runtime aggregate JSON: %v", err)
	}
	gotGateway := gatewaySnapshotFromPostgres(persisted.Gateway)
	gotUsage := usageSnapshotFromPostgres(persisted.Usage)
	if gotGateway != gateway || gotUsage != usage {
		t.Fatalf("aggregate dropped Gateway/Usage: got=%+v/%+v want=%+v/%+v",
			gotGateway, gotUsage, gateway, usage)
	}

	record := &runtimeRecord{fixture: fixture, gateway: gateway, usage: usage}
	retained := fixtureFromRuntimeRecord(record)
	if retained.Gateway != gateway || retained.Usage != usage {
		t.Fatalf("fixture projection dropped Gateway/Usage: got=%+v/%+v want=%+v/%+v",
			retained.Gateway, retained.Usage, gateway, usage)
	}
}

func TestPostgresAuthorityRejectsUnsafeConfigurationWithClosedError(t *testing.T) {
	t.Parallel()

	canaries := []string{
		"postgres://authority:credential@database/runtime",
		"runtime_execution; SELECT secret",
		"/private/runtime/path",
	}
	tests := []struct {
		name   string
		db     *sql.DB
		schema string
	}{
		{name: "missing database", schema: "runtime_execution"},
		{name: "dsn shaped schema", db: &sql.DB{}, schema: canaries[0]},
		{name: "sql shaped schema", db: &sql.DB{}, schema: canaries[1]},
		{name: "path shaped schema", db: &sql.DB{}, schema: canaries[2]},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPostgresAuthority(test.db, PostgresConfig{Schema: test.schema})
			var persistenceError *PersistenceError
			if !errors.As(err, &persistenceError) || persistenceError.Code() != PersistenceInvalidConfiguration {
				t.Fatalf("configuration error = %T, want closed invalid-configuration error", err)
			}
			if persistenceError.RetryDisposition() != RetryNever {
				t.Fatalf("invalid configuration retry = %v, want never", persistenceError.RetryDisposition())
			}
			for _, canary := range canaries {
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("safe persistence error leaked %q", canary)
				}
			}
		})
	}
}

func TestPostgresDriverFailuresNormalizeWithoutPrivateDetail(t *testing.T) {
	t.Parallel()

	canary := "postgres://credential-canary/private/runtime/path SELECT secret FROM runtime_execution_runtimes"
	tests := []struct {
		name      string
		sqlState  string
		wantCode  ErrorCode
		wantRetry RetryDisposition
	}{
		{name: "integrity", sqlState: "23505", wantCode: ErrorIntegrityConflict, wantRetry: RetryNever},
		{name: "authorization", sqlState: "42501", wantCode: ErrorAuthorizationDenied, wantRetry: RetryNever},
		{name: "transport", sqlState: "08006", wantCode: ErrorDependencyUnavailable, wantRetry: RetryAfterDependency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeRuntimePersistenceFailure(&pgconn.PgError{
				Code: test.sqlState, Message: canary, Detail: canary, Where: canary,
			})
			var safeError *Error
			if !errors.As(normalized, &safeError) || safeError.Code() != test.wantCode ||
				safeError.RetryDisposition() != test.wantRetry {
				t.Fatalf("SQLSTATE %s normalized to %T %+v, want code=%v retry=%v",
					test.sqlState, normalized, safeError, test.wantCode, test.wantRetry)
			}
			if strings.Contains(normalized.Error(), canary) || strings.Contains(normalized.Error(), "runtime_execution_runtimes") {
				t.Fatal("normalized persistence error retained driver or table detail")
			}
		})
	}
}
