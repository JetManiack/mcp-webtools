package storage

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

// openTestDB opens a migrated database for one test.
//
// With TEST_POSTGRES_DSN set it runs against that Postgres instance, so every
// test in this package exercises the Postgres code paths too — the
// advisory-lock migration and any driver-specific SQL — rather than only the
// handful of Postgres-specific tests. Otherwise it uses a fresh SQLite file
// per test, which needs nothing installed.
//
// Postgres runs share one database, so this truncates every table on cleanup.
// Tests here must therefore not call t.Parallel().
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		db, err := Open(dsn)
		if err != nil {
			t.Fatalf("Open(postgres): %v", err)
		}
		truncateAll(t, db)
		t.Cleanup(func() { truncateAll(t, db) })
		return db
	}

	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open(sqlite): %v", err)
	}
	return db
}

func truncateAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("TRUNCATE tool_calls, agent_credentials, user_identities, sessions, actors").Error; err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// skipUnlessPostgres marks a test as Postgres-only.
func skipUnlessPostgres(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db.Dialector.Name() != "postgres" {
		t.Skip("Postgres-only test; set TEST_POSTGRES_DSN to run it")
	}
}

func TestOpenMigratesEveryModel(t *testing.T) {
	db := openTestDB(t)

	for _, model := range []any{&Actor{}, &AgentCredential{}, &UserIdentity{}, &Session{}, &ToolCall{}} {
		if !db.Migrator().HasTable(model) {
			t.Errorf("table for %T was not created", model)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if os.Getenv("TEST_POSTGRES_DSN") != "" {
		// Re-opening the shared Postgres database re-runs AutoMigrate, which is
		// exactly what a second replica does; it must not error.
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		for range 2 {
			if _, err := Open(dsn); err != nil {
				t.Fatalf("Open: %v", err)
			}
		}
		return
	}

	dsn := filepath.Join(dir, "idempotent.db")
	for range 2 {
		if _, err := Open(dsn); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
}

// The SQLite path creates the parent directory itself: the default DSN is
// data/webtools.db, and a container's fresh volume has no data/ yet.
func TestOpenCreatesSQLiteParentDirectory(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "nested", "deeper", "webtools.db")
	if _, err := openSQLite(dsn); err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dsn)); err != nil {
		t.Errorf("parent directory was not created: %v", err)
	}
}

func TestIsPostgresDSN(t *testing.T) {
	tests := map[string]bool{
		"postgres://user@host/db":   true,
		"postgresql://user@host/db": true,
		"data/webtools.db":          false,
		":memory:":                  false,
		"/var/lib/webtools.db":      false,
		"":                          false,
	}
	for dsn, want := range tests {
		if got := IsPostgresDSN(dsn); got != want {
			t.Errorf("IsPostgresDSN(%q) = %v, want %v", dsn, got, want)
		}
	}
}

func TestSQLitePragmasAreApplied(t *testing.T) {
	db, err := openSQLite(filepath.Join(t.TempDir(), "pragmas.db"))
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}

	// WAL and a busy timeout are both required: every tool call writes a
	// history row, so concurrent writers are the normal case, and SQLite's
	// default is to fail fast on contention rather than wait.
	var journalMode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}
