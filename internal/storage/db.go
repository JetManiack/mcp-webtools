// Package storage holds this service's persistence layer: the GORM models and
// the repository functions over them, behind a single Open(dsn) that selects
// SQLite or Postgres from the DSN.
package storage

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Open opens a database at dsn — a postgres:// or postgresql:// scheme
// selects the Postgres backend; anything else (a file path or ":memory:")
// selects SQLite — applies backend-specific setup, and migrates the
// schema.
func Open(dsn string) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	if IsPostgresDSN(dsn) {
		db, err = openPostgres(dsn)
	} else {
		db, err = openSQLite(dsn)
	}
	if err != nil {
		return nil, err
	}

	migrate := func() error {
		if err := db.AutoMigrate(
			&Actor{},
			&AgentCredential{},
			&UserIdentity{},
			&Session{},
			&ToolCall{},
		); err != nil {
			return fmt.Errorf("automigrate: %w", err)
		}
		if err := backfillToolCallRename(db); err != nil {
			return fmt.Errorf("backfill tool_calls: %w", err)
		}
		return nil
	}

	// On Postgres, a second replica migrating the same schema concurrently
	// (e.g. mid-RollingUpdate) can deadlock against this one — serialize
	// with an advisory lock. SQLite has no concurrent-replica scenario to
	// guard against.
	if db.Dialector.Name() == "postgres" {
		if err := withMigrationLock(db, migrate); err != nil {
			return nil, err
		}
	} else if err := migrate(); err != nil {
		return nil, err
	}

	return db, nil
}

// IsPostgresDSN reports whether dsn selects the Postgres backend.
func IsPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// backfillToolCallRename migrates tool_calls rows written by the
// pre-skeleton schema (args/status/error_message/response_preview/
// response_bytes/created_at) into the renamed columns AutoMigrate just
// added. AutoMigrate alone broke on a populated table in production: it
// emitted ADD COLUMN input_json NOT NULL onto 211 existing rows (hence
// the default:'' on the model now) and never carried the old values over.
// GORM doesn't drop the old columns, so they are still there to read.
// Idempotent: each UPDATE touches only rows whose new column is still
// empty, and each is guarded by the old column existing.
func backfillToolCallRename(db *gorm.DB) error {
	type rename struct{ from, to, expr string }
	renames := []rename{
		{"args", "input_json", "args"},
		{"response_preview", "output_json", "response_preview"},
	}
	for _, r := range renames {
		if !db.Migrator().HasColumn(&ToolCall{}, r.from) {
			continue
		}
		if err := db.Exec(fmt.Sprintf(
			`UPDATE tool_calls SET %s = %s WHERE (%s IS NULL OR %s = '') AND %s IS NOT NULL`,
			r.to, r.expr, r.to, r.to, r.from)).Error; err != nil {
			return err
		}
	}
	// called_at is a timestamp, not text — the generic guard above compares
	// against '' and is wrong for it on Postgres; handle it separately.
	if db.Migrator().HasColumn(&ToolCall{}, "created_at") {
		if err := db.Exec(
			`UPDATE tool_calls SET called_at = created_at WHERE called_at IS NULL AND created_at IS NOT NULL`,
		).Error; err != nil {
			return err
		}
	}
	// status was an ok/error enum; is_error is a bool.
	if db.Migrator().HasColumn(&ToolCall{}, "status") {
		if err := db.Exec(
			`UPDATE tool_calls SET is_error = (status = 'error') WHERE status IS NOT NULL`,
		).Error; err != nil {
			return err
		}
	}
	return nil
}
