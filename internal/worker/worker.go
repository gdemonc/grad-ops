// Package worker claims durable jobs under a lease and drives them.
//
// The package is deliberately independent of any LLM or host tool: it decides
// *when* claimed work runs and what a crashed run means, never *whether* an
// action is allowed. A handler that changes system state must therefore enter
// through the executor, not directly from here.
package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/store"
)

// errNoJob means the queue is empty; it is not a failure.
var errNoJob = errors.New("no job available")

// expiredLease is one lease that outlived its deadline.
type expiredLease struct {
	id          int64
	taskID      int64
	kind        string
	attempts    int
	maxAttempts int
}

// Config controls claiming behaviour.
type Config struct {
	// Owner identifies this worker in lease records.
	Owner string
	// Lease is how long a claim stays valid without being renewed.
	Lease time.Duration
	// PollInterval is the pause between queue scans.
	PollInterval time.Duration
	// RetryDelay is how long a failed job waits before another attempt.
	RetryDelay time.Duration
}

// DefaultConfig returns conservative defaults for a single-host deployment.
func DefaultConfig(owner string) Config {
	return Config{
		Owner:        owner,
		Lease:        2 * time.Minute,
		PollInterval: 2 * time.Second,
		RetryDelay:   5 * time.Second,
	}
}

func (c Config) validate() error {
	if c.Owner == "" {
		return errors.New("worker owner must not be empty")
	}
	if c.Lease <= 0 {
		return errors.New("worker lease must be positive")
	}
	if c.PollInterval <= 0 {
		return errors.New("worker poll interval must be positive")
	}
	if c.RetryDelay < 0 {
		return errors.New("worker retry delay must not be negative")
	}
	return nil
}

// Job is a claimed unit of work.
type Job struct {
	ID          int64
	TaskID      int64
	Kind        domain.JobKind
	Attempts    int
	MaxAttempts int
}

// Handler performs the work of one claimed job.
//
// An error means the attempt failed. The worker decides whether that becomes a
// retry or a terminal failure; a handler must not retry by itself, because it
// cannot see the attempt budget.
type Handler interface {
	Handle(ctx context.Context, job Job) error
}

// Recovered summarises a lease recovery pass.
type Recovered struct {
	// Requeued counts read-only jobs returned to the queue.
	Requeued int
	// Abandoned counts jobs left in a final or unknown state.
	Abandoned int
}

// Worker claims and runs jobs.
type Worker struct {
	db     *store.DB
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
}

// New validates cfg and returns a Worker.
func New(db *store.DB, cfg Config, logger *slog.Logger) (*Worker, error) {
	if db == nil {
		return nil, errors.New("worker requires a database")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		db:     db,
		cfg:    cfg,
		logger: logger,
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// Claim atomically leases the oldest available job and moves its task into
// INVESTIGATING in the same transaction.
//
// The second return value is false when the queue is empty.
func (w *Worker) Claim(ctx context.Context) (Job, bool, error) {
	now := w.now()
	timestamp := domain.FormatTime(now)
	deadline := domain.FormatTime(now.Add(w.cfg.Lease))

	var (
		job      Job
		kind     string
		rejected bool
	)
	err := w.db.Tx(ctx, func(tx *sql.Tx) error {
		// The claim is a single statement: select-and-update in two steps
		// would let two workers claim the same job between them.
		row := tx.QueryRowContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = ?, lease_expires_at = ?, attempts = attempts + 1, updated_at = ?
WHERE id = (
    SELECT id FROM task_jobs
    WHERE status = ? AND available_at <= ?
    ORDER BY id
    LIMIT 1
)
RETURNING id, task_id, kind, attempts, max_attempts`,
			string(domain.JobStatusLeased), w.cfg.Owner, deadline, timestamp,
			string(domain.JobStatusPending), timestamp)

		if err := row.Scan(&job.ID, &job.TaskID, &kind, &job.Attempts, &job.MaxAttempts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNoJob
			}
			return fmt.Errorf("claim job: %w", err)
		}
		job.Kind = domain.JobKind(kind)

		// reject is reported through the return value, not an error: the
		// rejection has to be committed, and returning an error would roll the
		// transaction back.
		var enterErr error
		rejected, enterErr = w.enterInvestigation(ctx, tx, job, now)
		return enterErr
	})
	if errors.Is(err, errNoJob) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	if rejected {
		return Job{}, false, nil
	}
	return job, true, nil
}

// enterInvestigation moves the task into INVESTIGATING, or resumes it when a
// previous worker died after making the same transition.
func (w *Worker) enterInvestigation(ctx context.Context, tx *sql.Tx, job Job, now time.Time) (bool, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, job.TaskID).Scan(&raw); err != nil {
		return false, fmt.Errorf("load task %d: %w", job.TaskID, err)
	}
	status, err := domain.ParseStatus(raw)
	if err != nil {
		return false, err
	}

	eventType := "task.investigation.started"
	message := "investigation started"
	if status == domain.StatusInvestigating {
		// A resumed job keeps the task in the same state; re-entering is not a
		// transition and must not be recorded as one.
		eventType = "task.investigation.resumed"
		message = "investigation resumed after an interrupted attempt"
	} else {
		if err := domain.ValidateTransition(status, domain.StatusInvestigating); err != nil {
			// The task can no longer be investigated (for example it already
			// finished). Fail the job so it is not claimed again forever.
			if _, uerr := tx.ExecContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, updated_at = ?
WHERE id = ?`, string(domain.JobStatusFailed), domain.FormatTime(now), job.ID); uerr != nil {
				return false, fmt.Errorf("reject job %d: %w", job.ID, uerr)
			}
			if aerr := w.record(ctx, tx, job.TaskID, "job.rejected",
				"task cannot be investigated in its current state",
				map[string]any{"job_id": job.ID, "task_status": string(status)}, now); aerr != nil {
				return false, aerr
			}
			return true, nil
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`,
			string(domain.StatusInvestigating), domain.FormatTime(now), job.TaskID); err != nil {
			return false, fmt.Errorf("move task %d to investigating: %w", job.TaskID, err)
		}
	}

	return false, w.record(ctx, tx, job.TaskID, eventType, message, map[string]any{
		"job_id":       job.ID,
		"attempt":      job.Attempts,
		"max_attempts": job.MaxAttempts,
		"owner":        w.cfg.Owner,
	}, now)
}

// Recover returns expired leases to the queue, or abandons them.
//
// This is the crash path: the worker that held the lease is gone, so the
// question is only whether the work may be repeated. Read-only work may; work
// with side effects may not, because its outcome is unknown.
func (w *Worker) Recover(ctx context.Context) (Recovered, error) {
	now := w.now()
	var recovered Recovered

	// The rows are read into memory and closed before any write: the pool holds
	// a single connection, so an open cursor would block the recovery writes.
	var stale []expiredLease
	rows, err := w.db.QueryContext(ctx, `
SELECT id, task_id, kind, attempts, max_attempts
FROM task_jobs
WHERE status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
ORDER BY id`, string(domain.JobStatusLeased), domain.FormatTime(now))
	if err != nil {
		return Recovered{}, fmt.Errorf("scan expired leases: %w", err)
	}
	for rows.Next() {
		var lease expiredLease
		if err := rows.Scan(&lease.id, &lease.taskID, &lease.kind, &lease.attempts, &lease.maxAttempts); err != nil {
			rows.Close()
			return Recovered{}, fmt.Errorf("scan expired lease: %w", err)
		}
		stale = append(stale, lease)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Recovered{}, fmt.Errorf("scan expired leases: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Recovered{}, fmt.Errorf("close expired lease rows: %w", err)
	}

	for _, lease := range stale {
		kind := domain.JobKind(lease.kind)
		if kind.Retryable() && lease.attempts < lease.maxAttempts {
			if err := w.requeue(ctx, lease.id, lease.taskID, now); err != nil {
				return recovered, err
			}
			recovered.Requeued++
			continue
		}
		if err := w.abandon(ctx, lease, now); err != nil {
			return recovered, err
		}
		recovered.Abandoned++
	}

	return recovered, nil
}

func (w *Worker) requeue(ctx context.Context, jobID, taskID int64, now time.Time) error {
	timestamp := domain.FormatTime(now)
	err := w.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, available_at = ?, updated_at = ?
WHERE id = ?`, string(domain.JobStatusPending), timestamp, timestamp, jobID); err != nil {
			return fmt.Errorf("requeue job %d: %w", jobID, err)
		}
		return w.record(ctx, tx, taskID, "job.lease.expired",
			"job lease expired; requeued because the work is read-only",
			map[string]any{"job_id": jobID, "requeued": true}, now)
	})
	if err != nil {
		return err
	}
	w.logger.Warn("requeued job after expired lease", "job_id", jobID, "task_id", taskID)
	return nil
}

func (w *Worker) abandon(ctx context.Context, lease expiredLease, now time.Time) error {
	exhausted := lease.attempts >= lease.maxAttempts

	status := domain.JobStatusUnknown
	stage := "outcome_unknown"
	message := "job lease expired with an uncertain outcome; not replayed"
	eventType := "job.abandoned.unknown"
	if exhausted {
		status = domain.JobStatusFailed
		stage = "investigation"
		message = "job lease expired after the attempt budget was used"
		eventType = "job.failed"
	}

	err := w.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, updated_at = ?
WHERE id = ?`, string(status), domain.FormatTime(now), lease.id); err != nil {
			return fmt.Errorf("abandon job %d: %w", lease.id, err)
		}
		if err := w.failTask(ctx, tx, lease.taskID, stage, now); err != nil {
			return err
		}
		return w.record(ctx, tx, lease.taskID, eventType, message, map[string]any{
			"job_id":   lease.id,
			"attempts": lease.attempts,
		}, now)
	})
	if err != nil {
		return err
	}
	w.logger.Error("abandoned job after expired lease",
		"job_id", lease.id, "task_id", lease.taskID, "attempts", lease.attempts)
	return nil
}

// Complete marks a job done and records it.
func (w *Worker) Complete(ctx context.Context, job Job) error {
	now := w.now()
	return w.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, updated_at = ?
WHERE id = ?`, string(domain.JobStatusDone), domain.FormatTime(now), job.ID); err != nil {
			return fmt.Errorf("complete job %d: %w", job.ID, err)
		}
		return w.record(ctx, tx, job.TaskID, "job.completed", "job completed",
			map[string]any{"job_id": job.ID}, now)
	})
}

// Retry schedules another attempt, or fails the task once the budget is used.
func (w *Worker) Retry(ctx context.Context, job Job, cause error) error {
	now := w.now()
	reason := "unknown error"
	if cause != nil {
		reason = cause.Error()
	}

	if job.Attempts >= job.MaxAttempts {
		return w.db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, updated_at = ?
WHERE id = ?`, string(domain.JobStatusFailed), domain.FormatTime(now), job.ID); err != nil {
				return fmt.Errorf("fail job %d: %w", job.ID, err)
			}
			if err := w.failTask(ctx, tx, job.TaskID, "investigation", now); err != nil {
				return err
			}
			return w.record(ctx, tx, job.TaskID, "job.failed",
				"job failed after its attempt budget was used",
				map[string]any{"job_id": job.ID, "attempts": job.Attempts, "error": reason}, now)
		})
	}

	available := domain.FormatTime(now.Add(w.cfg.RetryDelay))
	return w.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE task_jobs
SET status = ?, lease_owner = NULL, lease_expires_at = NULL, available_at = ?, updated_at = ?
WHERE id = ?`, string(domain.JobStatusPending), available, domain.FormatTime(now), job.ID); err != nil {
			return fmt.Errorf("requeue job %d: %w", job.ID, err)
		}
		return w.record(ctx, tx, job.TaskID, "job.retry.scheduled", "job failed; another attempt is scheduled",
			map[string]any{"job_id": job.ID, "attempts": job.Attempts, "error": reason}, now)
	})
}

// Run polls for jobs until ctx is cancelled.
func (w *Worker) Run(ctx context.Context, handler Handler) error {
	if handler == nil {
		return errors.New("worker requires a handler")
	}

	if recovered, err := w.Recover(ctx); err != nil {
		// Recovery is best effort at startup: the poll loop below retries it on
		// the next pass, and stopping the whole service here would hide the
		// error behind an unavailable API.
		w.logger.Error("lease recovery failed at startup", "err", err)
	} else if recovered.Requeued > 0 || recovered.Abandoned > 0 {
		w.logger.Warn("recovered expired leases",
			"requeued", recovered.Requeued, "abandoned", recovered.Abandoned)
	}

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.drain(ctx, handler); err != nil {
				w.logger.Error("worker pass failed", "err", err)
			}
		}
	}
}

// drain processes every claimable job in one pass.
func (w *Worker) drain(ctx context.Context, handler Handler) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		job, ok, err := w.Claim(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		if err := handler.Handle(ctx, job); err != nil {
			w.logger.Warn("job handler failed", "job_id", job.ID, "attempt", job.Attempts, "err", err)
			if err := w.Retry(ctx, job, err); err != nil {
				return err
			}
			continue
		}

		if err := w.Complete(ctx, job); err != nil {
			return err
		}
	}
}

// failTask moves a task to FAILED unless it already reached a final state.
func (w *Worker) failTask(ctx context.Context, tx *sql.Tx, taskID int64, stage string, now time.Time) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&raw); err != nil {
		return fmt.Errorf("load task %d: %w", taskID, err)
	}
	status, err := domain.ParseStatus(raw)
	if err != nil {
		return err
	}
	if status.Terminal() {
		// Already final: do not rewrite a decided outcome.
		return nil
	}
	if err := domain.ValidateTransition(status, domain.StatusFailed); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET status = ?, failure_stage = ?, updated_at = ? WHERE id = ?`,
		string(domain.StatusFailed), stage, domain.FormatTime(now), taskID); err != nil {
		return fmt.Errorf("fail task %d: %w", taskID, err)
	}
	return nil
}

// record appends a worker audit event for a task.
func (w *Worker) record(ctx context.Context, tx *sql.Tx, taskID int64, eventType, message string, payload map[string]any, now time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode audit payload: %w", err)
	}
	id := taskID
	return audit.Append(ctx, tx, &audit.Event{
		TaskID:    &id,
		Actor:     "worker",
		Type:      eventType,
		Message:   message,
		Payload:   raw,
		CreatedAt: now,
	})
}
