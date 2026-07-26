package taskorchestration_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/taskorchestration"
)

func TestPostgresPersistenceErrorsAreTypedAndDoNotRetainPrivateDetail(t *testing.T) {
	canaries := []string{"postgres://credential-canary", "/private/task/path", "SELECT secret"}
	_, nilDatabaseError := taskorchestration.NewPostgresAdapter(nil, taskorchestration.PostgresConfig{})
	var nilDatabasePersistenceError *taskorchestration.PersistenceError
	if !errors.As(nilDatabaseError, &nilDatabasePersistenceError) ||
		nilDatabasePersistenceError.Code() != taskorchestration.PersistenceInvalidConfiguration {
		t.Fatalf("nil PostgreSQL database = %T, want typed configuration error", nilDatabaseError)
	}
	db := &sql.DB{}
	for _, schema := range append([]string{canaries[0], canaries[1], canaries[2]}, "invalid-schema") {
		_, err := taskorchestration.NewPostgresAdapter(db, taskorchestration.PostgresConfig{Schema: schema})
		var persistenceError *taskorchestration.PersistenceError
		if !errors.As(err, &persistenceError) ||
			persistenceError.Code() != taskorchestration.PersistenceInvalidConfiguration {
			t.Fatalf("invalid PostgreSQL configuration = %T, want typed configuration error", err)
		}
		for _, canary := range canaries {
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("persistence error leaked private detail %q", canary)
			}
		}
	}
}

func TestPostgresFaultControllerRejectsUnknownBoundariesWithSafeTypedError(t *testing.T) {
	controller := &taskorchestration.PersistenceFaultController{}
	err := controller.FailNextAt(taskorchestration.PersistenceFaultPoint(255))
	var persistenceError *taskorchestration.PersistenceError
	if !errors.As(err, &persistenceError) ||
		persistenceError.Code() != taskorchestration.PersistenceInvalidConfiguration {
		t.Fatalf("unknown fault boundary = %T, want typed configuration error", err)
	}
}
