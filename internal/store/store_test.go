package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gdemonc/grad-ops/internal/store"
)

func openTestDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gradops.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func TestMigrateCreatesEveryTable(t *testing.T) {
	db := openTestDB(t)

	tables := []string{
		"tasks",
		"task_jobs",
		"evidence",
		"proposals",
		"approvals",
		"execution_records",
		"verification_records",
		"audit_events",
		"schema_migrations",
	}
	for _, table := range tables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			t.Errorf("table %q was not created", table)
			continue
		}
		if err != nil {
			t.Errorf("look up table %q: %v", table, err)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openTestDB(t)

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected 1 recorded migration, got %d", applied)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	db := openTestDB(t)

	_, err := db.Exec(`
INSERT INTO task_jobs (task_id, kind, available_at, created_at, updated_at)
VALUES (999, 'investigate', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Fatal("expected a foreign key violation for a job without a task")
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	sentinel := errors.New("boom")

	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
INSERT INTO tasks (request_text, status, created_at, updated_at)
VALUES ('rollback probe', 'QUEUED', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the sentinel error, got %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the transaction to roll back, found %d tasks", count)
	}
}

func TestTxCommitsOnSuccess(t *testing.T) {
	db := openTestDB(t)

	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
INSERT INTO tasks (request_text, status, created_at, updated_at)
VALUES ('committed', 'QUEUED', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		return err
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 committed task, found %d", count)
	}
}
