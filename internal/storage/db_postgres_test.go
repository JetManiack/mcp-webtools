package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Two replicas rolling out at once both run AutoMigrate against the same
// schema, which can deadlock. The advisory lock is what makes them take turns,
// so this holds the lock from outside and asserts withMigrationLock actually
// waits for it rather than charging ahead.
func TestWithMigrationLockSerializes(t *testing.T) {
	db := openTestDB(t)
	skipUnlessPostgres(t, db)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get pooled db handle: %v", err)
	}

	ctx := context.Background()
	holder, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("checkout holder connection: %v", err)
	}
	defer holder.Close()

	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", MigrationAdvisoryLockKey); err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", MigrationAdvisoryLockKey); err != nil {
			t.Errorf("release lock: %v", err)
		}
	}
	defer release()

	ran := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withMigrationLock(db, func() error {
			close(ran)
			return nil
		})
	}()

	select {
	case <-ran:
		t.Fatal("the migration ran while another holder had the lock")
	case err := <-done:
		t.Fatalf("withMigrationLock returned early: %v", err)
	case <-time.After(500 * time.Millisecond):
		// Still waiting, which is the point.
	}

	release()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("withMigrationLock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withMigrationLock never acquired the lock after it was released")
	}
}

// An error from the migration itself has to propagate — swallowing it would let
// a replica come up serving a schema it failed to create.
func TestWithMigrationLockPropagatesError(t *testing.T) {
	db := openTestDB(t)
	skipUnlessPostgres(t, db)

	wantErr := errors.New("migration blew up")
	if err := withMigrationLock(db, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}

	// And the lock must be free afterwards, or the next replica hangs forever.
	if err := withMigrationLock(db, func() error { return nil }); err != nil {
		t.Errorf("second withMigrationLock: %v — the lock was not released after a failure", err)
	}
}
