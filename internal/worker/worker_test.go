package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
	"github.com/gdemonc/grad-ops/internal/worker"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "worker.db"))
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

func newTestWorker(t *testing.T, db *store.DB) *worker.Worker {
	t.Helper()

	wk, err := worker.New(db, worker.Config{
		Owner:        "test-worker",
		Lease:        time.Minute,
		PollInterval: 10 * time.Millisecond,
		RetryDelay:   time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	return wk
}

func createTask(t *testing.T, db *store.DB) domain.Task {
	t.Helper()

	created, err := task.NewService(db).Create(context.Background(),
		task.CreateInput{RequestText: "检查磁盘使用率异常"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return created
}

func jobStatus(t *testing.T, db *store.DB, taskID int64) string {
	t.Helper()

	var status string
	err := db.QueryRow(
		`SELECT status FROM task_jobs WHERE task_id = ? ORDER BY id LIMIT 1`, taskID).Scan(&status)
	if err != nil {
		t.Fatalf("read job status: %v", err)
	}
	return status
}

func expireLease(t *testing.T, db *store.DB, taskID int64) {
	t.Helper()

	past := domain.FormatTime(time.Now().Add(-time.Hour).UTC())
	if _, err := db.Exec(
		`UPDATE task_jobs SET lease_expires_at = ? WHERE task_id = ?`, past, taskID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

func TestClaimMovesTaskToInvestigating(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	job, ok, err := wk.Claim(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatal("expected a job to be claimable")
	}
	if job.TaskID != created.ID {
		t.Fatalf("expected job for task %d, got %d", created.ID, job.TaskID)
	}
	if job.Kind != domain.JobKindInvestigate {
		t.Fatalf("unexpected job kind %q", job.Kind)
	}
	if job.Attempts != 1 {
		t.Fatalf("expected the first attempt, got %d", job.Attempts)
	}

	stored, err := task.NewService(db).Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	if stored.Status != domain.StatusInvestigating {
		t.Fatalf("expected INVESTIGATING, got %s", stored.Status)
	}

	events, err := task.NewService(db).Timeline(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected created + started events, got %d", len(events))
	}
	if events[1].Type != "task.investigation.started" {
		t.Fatalf("unexpected second event %q", events[1].Type)
	}
}

func TestClaimReturnsNothingWhenQueueIsEmpty(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)

	if _, ok, err := wk.Claim(context.Background()); err != nil || ok {
		t.Fatalf("expected an empty queue, got ok=%v err=%v", ok, err)
	}
}

func TestClaimIsExclusive(t *testing.T) {
	db := newTestDB(t)
	createTask(t, db)

	first := newTestWorker(t, db)
	second := newTestWorker(t, db)

	if _, ok, err := first.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if _, ok, err := second.Claim(context.Background()); err != nil || ok {
		t.Fatalf("expected the second worker to find nothing, got ok=%v err=%v", ok, err)
	}
}

func TestRecoverRequeuesReadOnlyLeaseAndResumes(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	if _, ok, err := wk.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	expireLease(t, db, created.ID)

	recovered, err := wk.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Requeued != 1 || recovered.Abandoned != 0 {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusPending) {
		t.Fatalf("expected the job back in PENDING, got %s", status)
	}

	job, ok, err := wk.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("second claim: ok=%v err=%v", ok, err)
	}
	if job.Attempts != 2 {
		t.Fatalf("expected attempt 2, got %d", job.Attempts)
	}

	events, err := task.NewService(db).Timeline(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	want := []string{"task.created", "task.investigation.started", "job.lease.expired", "task.investigation.resumed"}
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d: %+v", len(want), len(events), types(events))
	}
	for i, eventType := range want {
		if events[i].Type != eventType {
			t.Fatalf("event %d: expected %q, got %q", i, eventType, events[i].Type)
		}
	}
}

func TestRecoverIgnoresActiveLease(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	if _, ok, err := wk.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	recovered, err := wk.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Requeued != 0 || recovered.Abandoned != 0 {
		t.Fatalf("expected an untouched active lease, got %+v", recovered)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusLeased) {
		t.Fatalf("expected the job to stay LEASED, got %s", status)
	}
}

func TestClaimRejectsTaskThatCannotBeInvestigated(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	// Simulate a task that reached a final state while its job was queued.
	if _, err := db.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, string(domain.StatusSealed), created.ID); err != nil {
		t.Fatalf("seal task: %v", err)
	}

	job, ok, err := wk.Claim(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok {
		t.Fatalf("expected no claim, got %+v", job)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusFailed) {
		t.Fatalf("expected the job to be failed, got %s", status)
	}

	events, err := task.NewService(db).Timeline(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if len(events) != 2 || events[1].Type != "job.rejected" {
		t.Fatalf("expected a job.rejected event, got %+v", types(events))
	}
}

func TestRecoverAbandonsUncertainSideEffectJob(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	past := domain.FormatTime(time.Now().Add(-time.Hour).UTC())
	if _, err := db.Exec(`
INSERT INTO task_jobs
    (task_id, kind, status, lease_owner, lease_expires_at, attempts, max_attempts, available_at, created_at, updated_at)
VALUES (?, 'apply_change', 'LEASED', 'dead-worker', ?, 1, 3, ?, ?, ?)`,
		created.ID, past, past, past, past); err != nil {
		t.Fatalf("insert side-effect job: %v", err)
	}

	recovered, err := wk.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Abandoned != 1 || recovered.Requeued != 0 {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}

	var status string
	if err := db.QueryRow(
		`SELECT status FROM task_jobs WHERE kind = 'apply_change'`).Scan(&status); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if status != string(domain.JobStatusUnknown) {
		t.Fatalf("expected UNKNOWN for an uncertain side effect, got %s", status)
	}

	stored, err := task.NewService(db).Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	if stored.Status != domain.StatusFailed {
		t.Fatalf("expected FAILED, got %s", stored.Status)
	}
	if stored.FailureStage == nil || *stored.FailureStage != "outcome_unknown" {
		t.Fatalf("expected failure_stage outcome_unknown, got %v", stored.FailureStage)
	}
}

func TestRecoverFailsJobWhenAttemptsAreExhausted(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	if _, ok, err := wk.Claim(context.Background()); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.Exec(
		`UPDATE task_jobs SET attempts = 1, max_attempts = 1 WHERE task_id = ?`, created.ID); err != nil {
		t.Fatalf("force attempt budget: %v", err)
	}
	expireLease(t, db, created.ID)

	recovered, err := wk.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Abandoned != 1 || recovered.Requeued != 0 {
		t.Fatalf("unexpected recovery result: %+v", recovered)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusFailed) {
		t.Fatalf("expected FAILED, got %s", status)
	}

	stored, err := task.NewService(db).Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	if stored.Status != domain.StatusFailed {
		t.Fatalf("expected the task to be FAILED, got %s", stored.Status)
	}
	if stored.FailureStage == nil || *stored.FailureStage != "investigation" {
		t.Fatalf("expected failure_stage investigation, got %v", stored.FailureStage)
	}
}

func TestCompleteMarksJobDone(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	job, ok, err := wk.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := wk.Complete(context.Background(), job); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusDone) {
		t.Fatalf("expected DONE, got %s", status)
	}
}

func TestRetryRequeuesThenFailsTheTask(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	job, ok, err := wk.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	if err := wk.Retry(context.Background(), job, errors.New("transient")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusPending) {
		t.Fatalf("expected PENDING after a retry, got %s", status)
	}

	exhausted := job
	exhausted.Attempts = exhausted.MaxAttempts
	if err := wk.Retry(context.Background(), exhausted, errors.New("permanent")); err != nil {
		t.Fatalf("retry with an exhausted budget: %v", err)
	}
	if status := jobStatus(t, db, created.ID); status != string(domain.JobStatusFailed) {
		t.Fatalf("expected FAILED, got %s", status)
	}

	stored, err := task.NewService(db).Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	if stored.Status != domain.StatusFailed {
		t.Fatalf("expected the task to be FAILED, got %s", stored.Status)
	}
}

func TestRunDrainsTheQueue(t *testing.T) {
	db := newTestDB(t)
	wk := newTestWorker(t, db)
	created := createTask(t, db)

	handler := &recordingHandler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- wk.Run(ctx, handler) }()

	deadline := time.Now().Add(3 * time.Second)
	for jobStatus(t, db, created.ID) != string(domain.JobStatusDone) {
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete; status=%s", jobStatus(t, db, created.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.jobs) != 1 {
		t.Fatalf("expected the handler to run once, got %d", len(handler.jobs))
	}
}

type recordingHandler struct {
	mu   sync.Mutex
	jobs []worker.Job
}

func (h *recordingHandler) Handle(_ context.Context, job worker.Job) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobs = append(h.jobs, job)
	return nil
}

func types(events []audit.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}
