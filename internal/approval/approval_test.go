package approval_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/store"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

func seedProposal(t *testing.T, db *store.DB) (int64, int64) {
	t.Helper()

	var taskID, proposalID int64
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		now := domain.FormatTime(time.Now())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"check disk", string(domain.StatusQueued), now, now)
		if err != nil {
			return err
		}
		if taskID, err = result.LastInsertId(); err != nil {
			return err
		}

		result, err = tx.Exec(
			`INSERT INTO proposals
			    (task_id, tool, arguments, risk_level, policy_version, contract, contract_digest, status, created_at)
			 VALUES (?, 'bash', '{}', 'R2', 'test-policy', '{}', ?, 'PENDING_APPROVAL', ?)`,
			taskID, testDigest, now)
		if err != nil {
			return err
		}
		proposalID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	return taskID, proposalID
}

func newService(t *testing.T, db *store.DB, opts ...approval.Option) *approval.Service {
	t.Helper()

	service, err := approval.New(db, 5*time.Minute, opts...)
	if err != nil {
		t.Fatalf("new approval service: %v", err)
	}
	return service
}

func TestGrantAndConsumeOnce(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)
	service := newService(t, db)

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Comment:        "restart approved",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if grant.Token == "" {
		t.Fatal("expected a token for an approved request")
	}

	consumed, err := service.Consume(context.Background(), grant.Token, testDigest)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumed.ApprovalID != grant.ApprovalID {
		t.Fatalf("consumed approval %d, granted %d", consumed.ApprovalID, grant.ApprovalID)
	}
	if consumed.Approver != "alice" {
		t.Fatalf("unexpected approver %q", consumed.Approver)
	}

	if _, err := service.Consume(context.Background(), grant.Token, testDigest); !errors.Is(err, approval.ErrAlreadyConsumed) {
		t.Fatalf("expected ErrAlreadyConsumed on replay, got %v", err)
	}
}

func TestTokenIsStoredOnlyAsHash(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)
	service := newService(t, db)

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM approvals WHERE id = ?`, grant.ApprovalID).Scan(&stored); err != nil {
		t.Fatalf("read token hash: %v", err)
	}
	if stored == grant.Token {
		t.Fatal("the token was stored in clear text")
	}
	if len(stored) != 64 {
		t.Fatalf("expected a hex sha256, got %q", stored)
	}
}

func TestExpiredTokenCannotBeConsumed(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)

	now := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	service := newService(t, db, approval.WithClock(func() time.Time { return now }))

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	later := newService(t, db, approval.WithClock(func() time.Time { return now.Add(6 * time.Minute) }))
	if _, err := later.Consume(context.Background(), grant.Token, testDigest); !errors.Is(err, approval.ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestTokenCannotBeUsedForAnotherContract(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)
	service := newService(t, db)

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	other := strings.Repeat("f", 64)
	if _, err := service.Consume(context.Background(), grant.Token, other); !errors.Is(err, approval.ErrContractMismatch) {
		t.Fatalf("expected ErrContractMismatch, got %v", err)
	}

	// The mismatch must not have spent the token.
	if _, err := service.Consume(context.Background(), grant.Token, testDigest); err != nil {
		t.Fatalf("the bound contract should still be consumable: %v", err)
	}
}

func TestUnknownToken(t *testing.T) {
	db := testDB(t)
	service := newService(t, db)

	if _, err := service.Consume(context.Background(), strings.Repeat("ab", 32), testDigest); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRejectedApprovalIssuesNoToken(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)
	service := newService(t, db)

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "bob",
		Comment:        "not today",
		Decision:       approval.Rejected,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if grant.Token != "" {
		t.Fatal("a rejection must not produce a token")
	}

	var decision string
	var token sql.NullString
	if err := db.QueryRow(`SELECT decision, token_hash FROM approvals WHERE id = ?`, grant.ApprovalID).
		Scan(&decision, &token); err != nil {
		t.Fatalf("read approval: %v", err)
	}
	if decision != string(approval.Rejected) {
		t.Fatalf("unexpected decision %q", decision)
	}
	if token.Valid {
		t.Fatal("a rejection must not store a token hash")
	}
}

func TestConcurrentConsumeOnlyOneWins(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)
	service := newService(t, db)

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	const attempts = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		success  int
		failures []error
	)
	start := make(chan struct{})

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Consume(context.Background(), grant.Token, testDigest)

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
				return
			}
			failures = append(failures, err)
		}()
	}
	close(start)
	wg.Wait()

	if success != 1 {
		t.Fatalf("expected exactly one successful consumption, got %d (failures: %v)", success, failures)
	}
	for _, err := range failures {
		if !errors.Is(err, approval.ErrAlreadyConsumed) {
			t.Fatalf("unexpected failure: %v", err)
		}
	}
}

func TestGrantAndConsumeAreAudited(t *testing.T) {
	db := testDB(t)
	taskID, proposalID := seedProposal(t, db)
	service := newService(t, db)

	grant, err := service.Grant(context.Background(), approval.Request{
		TaskID:         taskID,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := service.Consume(context.Background(), grant.Token, testDigest); err != nil {
		t.Fatalf("consume: %v", err)
	}

	events, err := audit.ListByTask(context.Background(), db, taskID)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}

	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "approval.granted") || !strings.Contains(joined, "approval.consumed") {
		t.Fatalf("expected approval events, got %v", types)
	}

	verified, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !verified.OK {
		t.Fatal("audit chain did not verify")
	}
}

func TestGrantValidation(t *testing.T) {
	db := testDB(t)
	_, proposalID := seedProposal(t, db)
	service := newService(t, db)

	base := approval.Request{
		TaskID:         1,
		ProposalID:     proposalID,
		ContractDigest: testDigest,
		Approver:       "alice",
		Decision:       approval.Approved,
	}

	cases := map[string]func(*approval.Request){
		"short digest":     func(r *approval.Request) { r.ContractDigest = "abc" },
		"non hex digest":   func(r *approval.Request) { r.ContractDigest = strings.Repeat("z", 64) },
		"missing approver": func(r *approval.Request) { r.Approver = " " },
		"unknown decision": func(r *approval.Request) { r.Decision = "MAYBE" },
		"missing proposal": func(r *approval.Request) { r.ProposalID = 0 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := service.Grant(context.Background(), request); !errors.Is(err, approval.ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
		})
	}
}
