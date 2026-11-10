// Package store owns the SQLite connection, the versioned schema, and the
// transaction boundary used by every other package.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	// The pure-Go driver keeps the build free of cgo, which keeps the Linux
	// build reproducible and lets the same code run in CI without a toolchain
	// for a C extension.
	_ "modernc.org/sqlite"
)

// DB wraps the SQLite handle with the settings the rest of the service relies
// on. Access goes through a single pooled connection: SQLite has one writer, so
// serializing here makes lock contention a non-issue instead of a latent
// SQLITE_BUSY.
type DB struct {
	*sql.DB

	path string
}

// Open opens (and creates on first use) the database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path %q: %w", path, err)
	}

	dsn := "file:" + filepath.ToSlash(abs) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", abs, err)
	}
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxLifetime(0)

	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("open database %q: %w", abs, err)
	}

	return &DB{DB: handle, path: abs}, nil
}

// Path returns the absolute database path.
func (db *DB) Path() string { return db.path }

// Tx runs fn inside a single transaction and commits only when fn succeeds.
//
// Callers that must keep a state change and its audit event atomic pass the
// *sql.Tx down instead of using the pool directly.
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		// Rollback errors are secondary: the caller's error explains the
		// failure, and the transaction is discarded either way.
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
