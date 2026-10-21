package storage_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/JetManiack/mcp-webtools/internal/storage"
)

// TestOpenMigratesPopulatedPreSkeletonTable reproduces the production
// outage of 2026-10-21: AutoMigrate adding a NOT NULL column (input_json)
// to a tool_calls table that already holds rows from the pre-skeleton
// schema. Tests against an empty database can't catch that class of
// failure, so this one seeds the old schema with data first and then runs
// the real storage.Open.
func TestOpenMigratesPopulatedPreSkeletonTable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "old.db")

	// Seed the pre-skeleton schema (commit 2dee207) with a populated
	// tool_calls table, bypassing storage.Open so none of today's migration
	// code runs yet.
	seed, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := seed.Exec(`CREATE TABLE tool_calls (
		id char(36) PRIMARY KEY,
		actor_id char(36) NOT NULL,
		tool varchar(64) NOT NULL,
		args text NOT NULL,
		status varchar(10) NOT NULL,
		error_message text,
		duration_ms integer,
		response_bytes integer,
		response_preview text,
		truncated numeric,
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("create old table: %v", err)
	}
	calledAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := seed.Exec(
		`INSERT INTO tool_calls (id, actor_id, tool, args, status, response_preview, created_at)
		 VALUES ('row-1', 'actor-1', 'fetch', '{"url":"https://example.org"}', 'error', '{"content":"x"}', ?)`,
		calledAt,
	).Error; err != nil {
		t.Fatalf("insert old row: %v", err)
	}
	if sqlDB, err := seed.DB(); err == nil {
		sqlDB.Close()
	}

	// The real migration must now succeed on the populated table…
	db, err := storage.Open(dsn)
	if err != nil {
		t.Fatalf("storage.Open on populated pre-skeleton db: %v", err)
	}

	// …and carry the old values into the renamed columns.
	var call storage.ToolCall
	if err := db.First(&call, "id = ?", "row-1").Error; err != nil {
		t.Fatalf("load migrated row: %v", err)
	}
	if call.InputJSON != `{"url":"https://example.org"}` {
		t.Errorf("InputJSON = %q, want the old args value", call.InputJSON)
	}
	if call.OutputJSON != `{"content":"x"}` {
		t.Errorf("OutputJSON = %q, want the old response_preview value", call.OutputJSON)
	}
	if !call.IsError {
		t.Error("IsError = false, want true (old status was 'error')")
	}
	if !call.CalledAt.Equal(calledAt) {
		t.Errorf("CalledAt = %v, want the old created_at %v", call.CalledAt, calledAt)
	}

	// And a second Open (every later pod start) must stay idempotent.
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.Close()
	}
	if _, err := storage.Open(dsn); err != nil {
		t.Fatalf("second storage.Open: %v", err)
	}
}
