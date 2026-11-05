package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// migrations holds the versioned schema. Files are applied in lexical order and
// recorded in schema_migrations, so a release never silently recreates tables.
//
//go:embed migrations/*.sql
var migrations embed.FS

const migrationDir = "migrations"

// Migrate applies every migration that has not been applied yet.
//
// Each file runs in its own transaction together with its bookkeeping row: a
// failed migration leaves no partial schema behind and no version recorded.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		applied, err := db.migrationApplied(ctx, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		body, err := migrations.ReadFile(migrationDir + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		err = db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
				name)
			if err != nil {
				return fmt.Errorf("record migration %s: %w", name, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrations, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("no embedded migrations found")
	}
	return names, nil
}

func (db *DB) migrationApplied(ctx context.Context, name string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM schema_migrations WHERE version = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check migration %s: %w", name, err)
	}
	return true, nil
}
