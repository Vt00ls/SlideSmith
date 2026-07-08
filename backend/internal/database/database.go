package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	_ "time/tzdata"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const postgresMigrationLockKey int64 = 7560419505985

func Open(cfg config.DatabaseConfig) (*gorm.DB, error) {
	switch strings.ToLower(cfg.Driver) {
	case "", "sqlite":
		if err := ensureSQLiteDir(cfg.DSN); err != nil {
			return nil, err
		}
		return gorm.Open(sqlite.Open(cfg.DSN), &gorm.Config{})
	case "postgres", "postgresql":
		return gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	default:
		return nil, fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}
}

func Migrate(db *gorm.DB) error {
	release, err := acquireMigrationLock(db)
	if err != nil {
		return err
	}
	if release != nil {
		defer release()
	}
	return db.AutoMigrate(
		&model.Task{},
		&model.TaskEvent{},
		&model.Artifact{},
		&model.TaskRuntimeRun{},
		&model.TaskPhaseRun{},
		&model.TaskConfirmation{},
		&model.TemplateRegistryEntry{},
	)
}

func acquireMigrationLock(db *gorm.DB) (func(), error) {
	if db.Dialector.Name() != "postgres" {
		return nil, nil
	}
	if err := db.Exec("SELECT pg_advisory_lock(?)", postgresMigrationLockKey).Error; err != nil {
		return nil, err
	}
	return func() {
		_ = db.Exec("SELECT pg_advisory_unlock(?)", postgresMigrationLockKey).Error
	}, nil
}

func ensureSQLiteDir(dsn string) error {
	if dsn == "" || strings.Contains(dsn, ":memory:") {
		return nil
	}
	path := dsn
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
		if idx := strings.Index(path, "?"); idx >= 0 {
			path = path[:idx]
		}
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
