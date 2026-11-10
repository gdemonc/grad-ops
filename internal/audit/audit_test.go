package audit_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/store"
)

func newTestDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "audit.db"))
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

func appendEvent(t *testing.T, db *store.DB, eventType string) audit.Event {
	t.Helper()

	payload, err := json.Marshal(map[string]string{"event": eventType})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	var appended audit.Event
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		ev := audit.Event{
			Actor:     "tester",
			Type:      eventType,
			Message:   "test event " + eventType,
			Payload:   payload,
			CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		}
		if err := audit.Append(context.Background(), tx, &ev); err != nil {
			return err
		}
		appended = ev
		return nil
	})
	if err != nil {
		t.Fatalf("append event %q: %v", eventType, err)
	}
	return appended
}

func TestAppendLinksEventsAndVerifyAcceptsTheChain(t *testing.T) {
	db := newTestDB(t)

	first := appendEvent(t, db, "first")
	if first.Seq != 1 {
		t.Fatalf("expected the first event to have seq 1, got %d", first.Seq)
	}
	if first.PrevHash != strings.Repeat("0", 64) {
		t.Fatalf("expected the first event to link to the genesis hash, got %q", first.PrevHash)
	}

	second := appendEvent(t, db, "second")
	if second.Seq != 2 {
		t.Fatalf("expected the second event to have seq 2, got %d", second.Seq)
	}
	if second.PrevHash != first.Hash {
		t.Fatalf("expected the second event to link to the first hash")
	}
	appendEvent(t, db, "third")

	result, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected a valid chain, got %+v", result)
	}
	if result.Events != 3 {
		t.Fatalf("expected 3 events, got %d", result.Events)
	}
}

func TestVerifyDetectsModifiedPayload(t *testing.T) {
	db := newTestDB(t)
	appendEvent(t, db, "first")
	appendEvent(t, db, "second")
	appendEvent(t, db, "third")

	// Remove the guard first: the test simulates an attacker who can write to
	// the database, which is exactly the boundary the threat model describes.
	if _, err := db.Exec(`DROP TRIGGER audit_events_no_update`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := db.Exec(`UPDATE audit_events SET payload = '{"tampered":true}' WHERE seq = 2`); err != nil {
		t.Fatalf("tamper payload: %v", err)
	}

	result, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if result.OK {
		t.Fatal("expected tampering to be detected")
	}
	if result.BrokenSeq != 2 {
		t.Fatalf("expected the break at seq 2, got %d (%s)", result.BrokenSeq, result.Reason)
	}
}

func TestVerifyDetectsRemovedEventInTheMiddle(t *testing.T) {
	db := newTestDB(t)
	appendEvent(t, db, "first")
	appendEvent(t, db, "second")
	appendEvent(t, db, "third")

	if _, err := db.Exec(`DROP TRIGGER audit_events_no_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE seq = 2`); err != nil {
		t.Fatalf("delete event: %v", err)
	}

	result, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if result.OK {
		t.Fatal("expected the missing event to be detected")
	}
	if !strings.Contains(result.Reason, "sequence gap") {
		t.Fatalf("expected a sequence gap report, got %q", result.Reason)
	}
}

func TestVerifyDetectsReorderedEvents(t *testing.T) {
	db := newTestDB(t)
	appendEvent(t, db, "first")
	appendEvent(t, db, "second")
	appendEvent(t, db, "third")

	if _, err := db.Exec(`DROP TRIGGER audit_events_no_update`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	// Swap the messages of the last two events: the sequence still runs 1..3,
	// so only the recomputed hash can notice the change.
	swap := `UPDATE audit_events
SET message = CASE seq WHEN 2 THEN (SELECT message FROM audit_events WHERE seq = 3)
                       WHEN 3 THEN (SELECT message FROM audit_events WHERE seq = 2) END
WHERE seq IN (2, 3)`
	if _, err := db.Exec(swap); err != nil {
		t.Fatalf("reorder events: %v", err)
	}

	result, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if result.OK {
		t.Fatal("expected reordering to be detected")
	}
}

func TestTriggersRefuseUpdateAndDelete(t *testing.T) {
	db := newTestDB(t)
	appendEvent(t, db, "first")

	if _, err := db.Exec(`UPDATE audit_events SET message = 'changed' WHERE seq = 1`); err == nil {
		t.Fatal("expected the update trigger to refuse the change")
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE seq = 1`); err == nil {
		t.Fatal("expected the delete trigger to refuse the change")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the event to survive, found %d events", count)
	}
}

// TestTailTruncationIsNotDetectedLocally pins down a known boundary rather than
// a desired property: deleting the newest events leaves a chain that is still
// internally consistent. Detecting that needs an externally stored checkpoint,
// so the test documents the limitation instead of pretending it is covered.
func TestTailTruncationIsNotDetectedLocally(t *testing.T) {
	db := newTestDB(t)
	appendEvent(t, db, "first")
	appendEvent(t, db, "second")
	appendEvent(t, db, "third")

	if _, err := db.Exec(`DROP TRIGGER audit_events_no_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE seq = 3`); err != nil {
		t.Fatalf("delete newest event: %v", err)
	}

	result, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if !result.OK || result.Events != 2 {
		t.Fatalf("expected a locally consistent 2-event chain, got %+v", result)
	}
}

func TestAppendRejectsIncompleteEvents(t *testing.T) {
	db := newTestDB(t)

	cases := map[string]audit.Event{
		"missing actor":   {Type: "task.created", Message: "m"},
		"missing type":    {Actor: "a", Message: "m"},
		"missing message": {Actor: "a", Type: "task.created"},
		"invalid payload": {Actor: "a", Type: "task.created", Message: "m", Payload: json.RawMessage(`{`)},
	}

	for name, ev := range cases {
		t.Run(name, func(t *testing.T) {
			err := db.Tx(context.Background(), func(tx *sql.Tx) error {
				return audit.Append(context.Background(), tx, &ev)
			})
			if err == nil {
				t.Fatal("expected the event to be rejected")
			}
		})
	}
}

func TestListByTaskFiltersEvents(t *testing.T) {
	db := newTestDB(t)

	// Two tasks, each with one event.
	for taskID := int64(1); taskID <= 2; taskID++ {
		id := taskID
		err := db.Tx(context.Background(), func(tx *sql.Tx) error {
			ev := audit.Event{
				TaskID:  &id,
				Actor:   "tester",
				Type:    "task.created",
				Message: fmt.Sprintf("task %d created", id),
				Payload: json.RawMessage(`{}`),
			}
			return audit.Append(context.Background(), tx, &ev)
		})
		if err != nil {
			t.Fatalf("append event for task %d: %v", id, err)
		}
	}

	events, err := audit.ListByTask(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event for task 1, got %d", len(events))
	}
	if events[0].TaskID == nil || *events[0].TaskID != 1 {
		t.Fatalf("unexpected task id on event: %+v", events[0])
	}
}
