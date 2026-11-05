// Package task is the application service for tasks: it owns validation, the
// transaction that creates a task together with its first audit event, and the
// read models used by the API.
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/store"
)

const (
	// maxRequestText bounds the operator request so a single task cannot
	// exhaust memory or make the persisted record unreadable.
	maxRequestText = 4000
	// defaultSource is recorded when the caller does not identify one.
	defaultSource = "web"
	// defaultLimit and maxLimit bound list responses.
	defaultLimit = 50
	maxLimit     = 200

	jobKindInvestigate = "investigate"
	jobStatusPending   = "PENDING"
)

// Service reads and writes tasks.
type Service struct {
	db  *store.DB
	now func() time.Time
}

// NewService returns a Service backed by db.
func NewService(db *store.DB) *Service {
	return &Service{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// CreateInput is a validated-by-the-service task request.
type CreateInput struct {
	RequestText string
	Source      string
}

// Create stores a new task in QUEUED, queues its investigation job, and records
// the creation event in the same transaction.
func (s *Service) Create(ctx context.Context, in CreateInput) (domain.Task, error) {
	text := strings.TrimSpace(in.RequestText)
	if text == "" {
		return domain.Task{}, fmt.Errorf("%w: request_text must not be empty", domain.ErrInvalidRequest)
	}
	if utf8.RuneCountInString(text) > maxRequestText {
		return domain.Task{}, fmt.Errorf("%w: request_text must be at most %d characters",
			domain.ErrInvalidRequest, maxRequestText)
	}

	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = defaultSource
	}

	now := s.now()
	created := domain.FormatTime(now)
	task := domain.Task{
		RequestText: text,
		Source:      source,
		Status:      domain.StatusQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		inserted, err := tx.ExecContext(ctx, `
INSERT INTO tasks (request_text, source, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)`,
			task.RequestText, task.Source, string(task.Status), created, created)
		if err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		id, err := inserted.LastInsertId()
		if err != nil {
			return fmt.Errorf("read task id: %w", err)
		}
		task.ID = id

		// The job is the durable hand-off to the worker: a crash between the
		// insert and the claim must leave the task queued, not lost.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_jobs (task_id, kind, status, available_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			id, jobKindInvestigate, jobStatusPending, created, created, created); err != nil {
			return fmt.Errorf("enqueue investigation job: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"task_id": id,
			"status":  string(task.Status),
			"source":  source,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		event := audit.Event{
			TaskID:    &task.ID,
			Actor:     "api",
			Type:      "task.created",
			Message:   "task created",
			Payload:   payload,
			CreatedAt: now,
		}
		return audit.Append(ctx, tx, &event)
	})
	if err != nil {
		return domain.Task{}, err
	}

	return task, nil
}

// Get returns one task.
func (s *Service) Get(ctx context.Context, id int64) (domain.Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, request_text, source, status, summary, failure_stage, created_at, updated_at
FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

// List returns tasks newest first, optionally filtered by status.
func (s *Service) List(ctx context.Context, status *domain.Status, limit int) ([]domain.Task, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	query := `
SELECT id, request_text, source, status, summary, failure_stage, created_at, updated_at
FROM tasks`
	args := []any{}
	if status != nil {
		query += ` WHERE status = ?`
		args = append(args, string(*status))
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, limit)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}

// Timeline returns a task's audit events in chain order.
//
// The task is read first so a missing task is reported as not found instead of
// returning an empty timeline.
func (s *Service) Timeline(ctx context.Context, id int64) ([]audit.Event, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	return audit.ListByTask(ctx, s.db, id)
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (domain.Task, error) {
	var (
		task      domain.Task
		status    string
		summary   sql.NullString
		failure   sql.NullString
		createdAt string
		updatedAt string
	)
	err := row.Scan(&task.ID, &task.RequestText, &task.Source, &status,
		&summary, &failure, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, fmt.Errorf("%w: task does not exist", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("scan task: %w", err)
	}

	parsedStatus, err := domain.ParseStatus(status)
	if err != nil {
		return domain.Task{}, fmt.Errorf("scan task %d: %w", task.ID, err)
	}
	task.Status = parsedStatus
	if summary.Valid {
		task.Summary = &summary.String
	}
	if failure.Valid {
		task.FailureStage = &failure.String
	}
	if task.CreatedAt, err = domain.ParseTime(createdAt); err != nil {
		return domain.Task{}, fmt.Errorf("scan task %d: %w", task.ID, err)
	}
	if task.UpdatedAt, err = domain.ParseTime(updatedAt); err != nil {
		return domain.Task{}, fmt.Errorf("scan task %d: %w", task.ID, err)
	}
	return task, nil
}
