package evidence_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gradops.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedTask(t *testing.T, db *store.DB) int64 {
	t.Helper()

	var id int64
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		now := domain.FormatTime(time.Now())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"look at the disk", string(domain.StatusQueued), now, now)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return id
}

func save(t *testing.T, db *store.DB, record evidence.Record) evidence.Record {
	t.Helper()

	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		return evidence.New().Save(context.Background(), tx, &record)
	})
	if err != nil {
		t.Fatalf("save evidence: %v", err)
	}
	return record
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db)

	record := save(t, db, evidence.Record{
		TaskID:     taskID,
		Tool:       "bash",
		Command:    "df -h",
		Content:    "Filesystem  Size  Used Avail Use%",
		ObservedAt: time.Now(),
	})
	if record.ID == "" || record.ContentHash == "" {
		t.Fatal("expected the id and hash to be filled in")
	}

	loaded, err := evidence.New().Get(context.Background(), db, record.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if loaded.Content != record.Content || loaded.ContentHash != record.ContentHash {
		t.Fatalf("round trip changed the record: %+v", loaded)
	}
}

// TestEditedEvidenceIsDetected is the property grounding depends on: a record
// that was altered after the fact no longer matches the id it was cited by.
func TestEditedEvidenceIsDetected(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db)

	record := save(t, db, evidence.Record{TaskID: taskID, Tool: "bash", Content: "disk is fine"})

	if _, err := db.Exec(`UPDATE evidence SET content = ? WHERE id = ?`, "disk is full", record.ID); err != nil {
		t.Fatalf("edit evidence: %v", err)
	}

	if _, err := evidence.New().Get(context.Background(), db, record.ID); !errors.Is(err, evidence.ErrHashMismatch) {
		t.Fatalf("expected ErrHashMismatch, got %v", err)
	}
}

func TestVerifyCitations(t *testing.T) {
	db := testDB(t)
	store := evidence.New()
	taskID := seedTask(t, db)
	otherTaskID := seedTask(t, db)

	first := save(t, db, evidence.Record{TaskID: taskID, Tool: "bash", Content: "uptime"})
	second := save(t, db, evidence.Record{TaskID: taskID, Tool: "bash", Content: "free -m"})
	foreign := save(t, db, evidence.Record{TaskID: otherTaskID, Tool: "bash", Content: "elsewhere"})

	if err := store.VerifyCitations(context.Background(), db, taskID, []string{first.ID, second.ID}); err != nil {
		t.Fatalf("expected the citations to verify: %v", err)
	}

	if err := store.VerifyCitations(context.Background(), db, taskID, nil); err == nil {
		t.Fatal("a conclusion with no citations must be rejected")
	}
	if err := store.VerifyCitations(context.Background(), db, taskID, []string{"ev_does_not_exist"}); err == nil {
		t.Fatal("an unknown citation must be rejected")
	}
	if err := store.VerifyCitations(context.Background(), db, taskID, []string{foreign.ID}); err == nil {
		t.Fatal("evidence from another task must be rejected")
	}
}

func TestListByTaskReturnsOnlyItsOwnEvidence(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db)
	otherTaskID := seedTask(t, db)

	save(t, db, evidence.Record{TaskID: taskID, Tool: "bash", Content: "one", ObservedAt: time.Now()})
	save(t, db, evidence.Record{TaskID: taskID, Tool: "bash", Content: "two", ObservedAt: time.Now().Add(time.Second)})
	save(t, db, evidence.Record{TaskID: otherTaskID, Tool: "bash", Content: "three"})

	records, err := evidence.New().ListByTask(context.Background(), db, taskID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected two records, got %d", len(records))
	}
	if records[0].Content != "one" || records[1].Content != "two" {
		t.Fatalf("unexpected order: %v", []string{records[0].Content, records[1].Content})
	}
}

func TestSaveRejectsUnusableRecords(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db)

	cases := map[string]evidence.Record{
		"no task":  {Tool: "bash", Content: "x"},
		"no tool":  {TaskID: taskID, Content: "x"},
		"bad utf8": {TaskID: taskID, Tool: "bash", Content: "\xff\xfe"},
	}

	for name, record := range cases {
		t.Run(name, func(t *testing.T) {
			err := db.Tx(context.Background(), func(tx *sql.Tx) error {
				return evidence.New().Save(context.Background(), tx, &record)
			})
			if err == nil {
				t.Fatal("expected the record to be rejected")
			}
		})
	}
}
