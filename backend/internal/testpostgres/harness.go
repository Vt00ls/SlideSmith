// Package testpostgres provides reusable real-PostgreSQL test isolation.
package testpostgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var schemaSequence atomic.Uint64

// Open creates an isolated schema in a real PostgreSQL database and removes it
// automatically when the test finishes.
func Open(t testing.TB, prefix string) (*sql.DB, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("SLIDESMITH_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("SLIDESMITH_TEST_POSTGRES_DSN is required for real PostgreSQL integration tests")
	}
	if !validPrefix(prefix) {
		t.Fatal("PostgreSQL test schema prefix is invalid")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open real PostgreSQL test database")
	}
	// `go test ./...` runs several PostgreSQL-heavy packages concurrently.
	// Bound each isolated test pool so one package cannot exhaust the shared
	// server and turn an otherwise local concurrency test into a blocked open.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("real PostgreSQL test database is unavailable")
	}
	schema := fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), schemaSequence.Add(1))
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create isolated PostgreSQL test schema")
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupContext, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("drop isolated PostgreSQL test schema")
		}
	})
	return db, schema
}

func validPrefix(value string) bool {
	if value == "" || len(value) > 24 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character == '_' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
