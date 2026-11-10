// Package audit maintains the append-only, hash-chained event log.
//
// The chain proves internal consistency: no event was edited, removed from the
// middle, or reordered without detection. It does not defend against an
// attacker who controls the database, because such an attacker can rewrite the
// whole chain or drop the triggers; the threat model names that boundary
// explicitly. Trailing truncation is likewise invisible locally and needs an
// external signed checkpoint.
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/domain"
)

// genesisHash is the predecessor of the first event.
const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// chainDomain separates this chain's digests from any other SHA-256 use.
const chainDomain = "gradops.audit.v1"

// Event is one auditable fact.
type Event struct {
	Seq         int64
	TaskID      *int64
	Actor       string
	Type        string
	Message     string
	Payload     json.RawMessage
	PayloadHash string
	PrevHash    string
	Hash        string
	CreatedAt   time.Time
}

// Queryer is the subset of the database handle used by this package, satisfied
// by both *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Append writes ev to the chain inside tx and fills in Seq, PayloadHash,
// PrevHash and Hash.
//
// Requiring a transaction is deliberate: a state change and its audit event are
// committed together or not at all, so the log can never describe a change that
// did not happen.
func Append(ctx context.Context, tx *sql.Tx, ev *Event) error {
	if strings.TrimSpace(ev.Actor) == "" {
		return errors.New("audit event requires an actor")
	}
	if strings.TrimSpace(ev.Type) == "" {
		return errors.New("audit event requires a type")
	}
	if strings.TrimSpace(ev.Message) == "" {
		return errors.New("audit event requires a message")
	}

	payload := ev.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return errors.New("audit payload must be valid JSON")
	}

	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}
	createdAt := domain.FormatTime(ev.CreatedAt)
	payloadHash := hashBytes(payload)

	prev := genesisHash
	err := tx.QueryRowContext(ctx,
		`SELECT event_hash FROM audit_events ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read audit chain head: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		prev = genesisHash
	}

	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM audit_events`).Scan(&seq); err != nil {
		return fmt.Errorf("read audit sequence: %w", err)
	}

	eventHash := computeHash(prev, seq, ev.TaskID, ev.Actor, ev.Type, ev.Message, payloadHash, createdAt)

	_, err = tx.ExecContext(ctx, `
INSERT INTO audit_events
    (seq, task_id, actor, event_type, message, payload, payload_hash, prev_hash, event_hash, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, ev.TaskID, ev.Actor, ev.Type, ev.Message, string(payload), payloadHash, prev, eventHash, createdAt)
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}

	ev.Seq = seq
	ev.PayloadHash = payloadHash
	ev.PrevHash = prev
	ev.Hash = eventHash
	return nil
}

// ListByTask returns one task's events in chain order.
func ListByTask(ctx context.Context, q Queryer, taskID int64) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `
SELECT seq, task_id, actor, event_type, message, payload, payload_hash, prev_hash, event_hash, created_at
FROM audit_events WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// Result is the outcome of a chain verification.
type Result struct {
	Events    int
	OK        bool
	BrokenSeq int64
	Reason    string
}

// Verify walks the chain and reports the first inconsistency.
func Verify(ctx context.Context, q Queryer) (Result, error) {
	rows, err := q.QueryContext(ctx, `
SELECT seq, task_id, actor, event_type, message, payload, payload_hash, prev_hash, event_hash, created_at
FROM audit_events ORDER BY seq`)
	if err != nil {
		return Result{}, fmt.Errorf("read audit chain: %w", err)
	}
	defer rows.Close()

	result := Result{OK: true}
	var expectedSeq int64 = 1
	prev := genesisHash

	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return Result{}, err
		}
		result.Events++

		if ev.Seq != expectedSeq {
			result.OK = false
			result.BrokenSeq = expectedSeq
			result.Reason = fmt.Sprintf("sequence gap: expected %d, found %d", expectedSeq, ev.Seq)
			return result, nil
		}
		if ev.PrevHash != prev {
			result.OK = false
			result.BrokenSeq = ev.Seq
			result.Reason = fmt.Sprintf("event %d does not link to its predecessor", ev.Seq)
			return result, nil
		}
		if actual := hashBytes(ev.Payload); actual != ev.PayloadHash {
			result.OK = false
			result.BrokenSeq = ev.Seq
			result.Reason = fmt.Sprintf("event %d payload was modified", ev.Seq)
			return result, nil
		}
		createdAt := domain.FormatTime(ev.CreatedAt)
		expected := computeHash(ev.PrevHash, ev.Seq, ev.TaskID, ev.Actor, ev.Type, ev.Message, ev.PayloadHash, createdAt)
		if expected != ev.Hash {
			result.OK = false
			result.BrokenSeq = ev.Seq
			result.Reason = fmt.Sprintf("event %d hash does not match its contents", ev.Seq)
			return result, nil
		}

		prev = ev.Hash
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("read audit chain: %w", err)
	}

	return result, nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	return events, nil
}

func scanEvent(rows *sql.Rows) (Event, error) {
	var (
		ev        Event
		taskID    sql.NullInt64
		payload   string
		createdAt string
	)
	if err := rows.Scan(&ev.Seq, &taskID, &ev.Actor, &ev.Type, &ev.Message,
		&payload, &ev.PayloadHash, &ev.PrevHash, &ev.Hash, &createdAt); err != nil {
		return Event{}, fmt.Errorf("scan audit event: %w", err)
	}
	if taskID.Valid {
		id := taskID.Int64
		ev.TaskID = &id
	}
	ev.Payload = json.RawMessage(payload)
	parsed, err := domain.ParseTime(createdAt)
	if err != nil {
		return Event{}, fmt.Errorf("scan audit event: %w", err)
	}
	ev.CreatedAt = parsed
	return ev, nil
}

func computeHash(prev string, seq int64, taskID *int64, actor, eventType, message, payloadHash, createdAt string) string {
	h := sha256.New()
	writeField(h, chainDomain)
	writeField(h, strconv.FormatInt(seq, 10))
	if taskID == nil {
		writeField(h, "")
	} else {
		writeField(h, strconv.FormatInt(*taskID, 10))
	}
	writeField(h, actor)
	writeField(h, eventType)
	writeField(h, message)
	writeField(h, payloadHash)
	writeField(h, createdAt)
	writeField(h, prev)
	return hex.EncodeToString(h.Sum(nil))
}

// writeField prefixes each field with its length. Length prefixing keeps the
// encoding unambiguous even when a message contains the separator, which a
// plain join would not.
func writeField(h hash.Hash, field string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(field)))
	h.Write(size[:])
	io.WriteString(h, field)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
