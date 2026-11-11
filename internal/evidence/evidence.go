// Package evidence records what was observed.
//
// Evidence is the only thing a conclusion may cite. Each record is content
// addressed: the stored hash is recomputed when the record is loaded, so a
// conclusion that cites an id is tied to the exact bytes it was drawn from, and
// a record edited after the fact no longer matches the id it was cited by.
package evidence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdemonc/grad-ops/internal/domain"
)

// ErrNotFound means no evidence has that id.
var ErrNotFound = errors.New("evidence not found")

// ErrHashMismatch means stored evidence no longer matches its recorded hash.
var ErrHashMismatch = errors.New("evidence content does not match its hash")

// Record is one observation.
type Record struct {
	ID     string
	TaskID int64
	// Attempt is the worker attempt that produced the observation. Direct
	// callers that are not part of a worker run use zero.
	Attempt     int
	Tool        string
	Command     string
	ObservedAt  time.Time
	Content     string
	ContentHash string
	Truncated   bool
}

// Queryer is satisfied by both *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// NewID returns a fresh evidence identifier.
func NewID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate evidence id: %w", err)
	}
	return "ev_" + hex.EncodeToString(raw), nil
}

// Hash returns the content hash used to bind evidence to its bytes.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Store persists evidence inside a caller-provided transaction.
type Store struct{}

// New returns a store handle.
func New() *Store { return &Store{} }

// Save writes a record, filling in the id and hash when they are absent.
//
// It must run in the same transaction as the conclusion that cites the record:
// an evidence row whose citing decision was rolled back is an orphan, and a
// decision whose evidence was rolled back cites nothing.
func (s *Store) Save(ctx context.Context, q Queryer, record *Record) error {
	if record.TaskID <= 0 {
		return fmt.Errorf("evidence requires a task")
	}
	if strings.TrimSpace(record.Tool) == "" {
		return fmt.Errorf("evidence requires a tool")
	}
	if !utf8.ValidString(record.Content) {
		return fmt.Errorf("evidence content must be valid UTF-8")
	}
	if record.ID == "" {
		id, err := NewID()
		if err != nil {
			return err
		}
		record.ID = id
	}
	if record.ContentHash == "" {
		record.ContentHash = Hash(record.Content)
	}
	if record.ObservedAt.IsZero() {
		record.ObservedAt = time.Now()
	}

	truncated := 0
	if record.Truncated {
		truncated = 1
	}

	_, err := q.ExecContext(ctx,
		`INSERT INTO evidence (id, task_id, attempt, tool, command, observed_at, content, content_hash, truncated)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.TaskID, record.Attempt, record.Tool, record.Command,
		domain.FormatTime(record.ObservedAt), record.Content, record.ContentHash, truncated,
	)
	if err != nil {
		return fmt.Errorf("insert evidence: %w", err)
	}
	return nil
}

// Get loads one record and verifies that its content still matches its hash.
func (s *Store) Get(ctx context.Context, q Queryer, id string) (Record, error) {
	var (
		record    Record
		observed  string
		truncated int
	)

	err := q.QueryRowContext(ctx,
		`SELECT id, task_id, attempt, tool, command, observed_at, content, content_hash, truncated
		   FROM evidence WHERE id = ?`, id,
	).Scan(&record.ID, &record.TaskID, &record.Attempt, &record.Tool, &record.Command,
		&observed, &record.Content, &record.ContentHash, &truncated)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("load evidence: %w", err)
	}

	if record.ObservedAt, err = domain.ParseTime(observed); err != nil {
		return Record{}, fmt.Errorf("evidence time: %w", err)
	}
	record.Truncated = truncated != 0

	if Hash(record.Content) != record.ContentHash {
		return Record{}, ErrHashMismatch
	}
	return record, nil
}

// ListByTask returns a task's evidence in observation order.
func (s *Store) ListByTask(ctx context.Context, q Queryer, taskID int64) ([]Record, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, task_id, attempt, tool, command, observed_at, content, content_hash, truncated
		   FROM evidence WHERE task_id = ? ORDER BY observed_at, id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list evidence: %w", err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var (
			record    Record
			observed  string
			truncated int
		)
		if err := rows.Scan(&record.ID, &record.TaskID, &record.Attempt, &record.Tool, &record.Command,
			&observed, &record.Content, &record.ContentHash, &truncated); err != nil {
			return nil, fmt.Errorf("scan evidence: %w", err)
		}
		if record.ObservedAt, err = domain.ParseTime(observed); err != nil {
			return nil, fmt.Errorf("evidence time: %w", err)
		}
		record.Truncated = truncated != 0
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list evidence: %w", err)
	}
	return records, nil
}

// ListByTaskAttempt returns the evidence produced by one worker attempt.
func (s *Store) ListByTaskAttempt(ctx context.Context, q Queryer, taskID int64, attempt int) ([]Record, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, task_id, attempt, tool, command, observed_at, content, content_hash, truncated
		   FROM evidence WHERE task_id = ? AND attempt = ? ORDER BY observed_at, id`, taskID, attempt)
	if err != nil {
		return nil, fmt.Errorf("list evidence attempt: %w", err)
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		var (
			record    Record
			observed  string
			truncated int
		)
		if err := rows.Scan(&record.ID, &record.TaskID, &record.Attempt, &record.Tool, &record.Command,
			&observed, &record.Content, &record.ContentHash, &truncated); err != nil {
			return nil, fmt.Errorf("scan evidence attempt: %w", err)
		}
		if record.ObservedAt, err = domain.ParseTime(observed); err != nil {
			return nil, fmt.Errorf("evidence time: %w", err)
		}
		record.Truncated = truncated != 0
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list evidence attempt: %w", err)
	}
	return records, nil
}

// VerifyCitations checks that every cited id exists, belongs to the task and
// still matches its hash.
//
// A conclusion that cites an id the database does not have, or that cites
// evidence from another task, is ungrounded and must not be accepted.
func (s *Store) VerifyCitations(ctx context.Context, q Queryer, taskID int64, ids []string) error {
	return s.VerifyCitationsForAttempt(ctx, q, taskID, -1, ids)
}

// VerifyCitationsForAttempt additionally rejects evidence from another attempt
// when attempt is non-negative.
func (s *Store) VerifyCitationsForAttempt(ctx context.Context, q Queryer, taskID int64, attempt int, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("a conclusion must cite at least one piece of evidence")
	}

	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("a cited evidence id is empty")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}

		record, err := s.Get(ctx, q, id)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("cited evidence %q does not exist", id)
		}
		if err != nil {
			return err
		}
		if record.TaskID != taskID {
			return fmt.Errorf("cited evidence %q belongs to another task", id)
		}
		if attempt >= 0 && record.Attempt != attempt {
			return fmt.Errorf("cited evidence %q belongs to another attempt", id)
		}
	}
	return nil
}
