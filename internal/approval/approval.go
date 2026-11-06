// Package approval issues and consumes one-time approvals.
//
// An approval is a capability, not a comment. It is bound to the digest of one
// action contract, it carries a random token that is stored only as a hash, and
// it can be consumed exactly once. The consuming statement is a single
// conditional UPDATE, so two workers racing on the same token cannot both win.
package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/store"
)

var (
	// ErrNotFound means the token is unknown.
	ErrNotFound = errors.New("approval not found")
	// ErrExpired means the approval's validity window has passed.
	ErrExpired = errors.New("approval expired")
	// ErrAlreadyConsumed means the token was already spent.
	ErrAlreadyConsumed = errors.New("approval already consumed")
	// ErrRejected means the approval recorded a rejection.
	ErrRejected = errors.New("approval was rejected")
	// ErrContractMismatch means the token authorises a different action.
	ErrContractMismatch = errors.New("approval is bound to a different contract")
	// ErrInvalidRequest means the caller supplied an unusable request.
	ErrInvalidRequest = errors.New("invalid approval request")
)

// Decision is the approver's verdict.
type Decision string

const (
	// Approved authorises the bound contract once.
	Approved Decision = "APPROVED"
	// Rejected refuses the bound contract.
	Rejected Decision = "REJECTED"
)

// TokenBytes is the entropy of an approval token.
const TokenBytes = 32

// errNoMatch is internal: the conditional UPDATE found nothing to consume.
var errNoMatch = errors.New("approval not consumable")

// Request asks for an approval record.
type Request struct {
	TaskID         int64
	ProposalID     int64
	ContractDigest string
	Approver       string
	Comment        string
	Decision       Decision
}

// Grant is the result of issuing an approval.
type Grant struct {
	ApprovalID int64
	Decision   Decision
	// Token is returned exactly once, to the approver. Only its hash is stored.
	Token     string
	ExpiresAt time.Time
}

// Consumed describes a successful consumption.
type Consumed struct {
	ApprovalID int64
	ProposalID int64
	TaskID     int64
	Approver   string
	Comment    string
	ConsumedAt time.Time
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces the clock. Tests use it to cross an expiry without sleeping.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithRandom replaces the token source.
func WithRandom(reader io.Reader) Option {
	return func(s *Service) { s.random = reader }
}

// Service issues and consumes approvals.
type Service struct {
	db     *store.DB
	ttl    time.Duration
	now    func() time.Time
	random io.Reader
}

// New returns a service whose tokens stay valid for ttl.
func New(db *store.DB, ttl time.Duration, opts ...Option) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalidRequest)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: approval ttl must be positive", ErrInvalidRequest)
	}

	service := &Service{db: db, ttl: ttl, now: time.Now, random: rand.Reader}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

// Grant records an approval and, when approved, returns its one-time token.
func (s *Service) Grant(ctx context.Context, req Request) (Grant, error) {
	if req.TaskID <= 0 || req.ProposalID <= 0 {
		return Grant{}, fmt.Errorf("%w: task and proposal are required", ErrInvalidRequest)
	}
	if len(req.ContractDigest) != 64 || !isHex(req.ContractDigest) {
		return Grant{}, fmt.Errorf("%w: contract digest must be a hex sha256", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Approver) == "" {
		return Grant{}, fmt.Errorf("%w: approver is required", ErrInvalidRequest)
	}
	if req.Decision != Approved && req.Decision != Rejected {
		return Grant{}, fmt.Errorf("%w: unknown decision %q", ErrInvalidRequest, req.Decision)
	}

	now := s.now().UTC()
	grant := Grant{Decision: req.Decision, ExpiresAt: now.Add(s.ttl)}

	var tokenHash any
	if req.Decision == Approved {
		raw := make([]byte, TokenBytes)
		if _, err := io.ReadFull(s.random, raw); err != nil {
			return Grant{}, fmt.Errorf("generate approval token: %w", err)
		}
		grant.Token = hex.EncodeToString(raw)
		tokenHash = hashToken(grant.Token)
	}

	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO approvals
			    (proposal_id, approver, comment, decision, token_hash, expires_at, contract_digest, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			req.ProposalID, req.Approver, req.Comment, string(req.Decision),
			tokenHash, domain.FormatTime(grant.ExpiresAt), req.ContractDigest, domain.FormatTime(now),
		)
		if err != nil {
			return fmt.Errorf("insert approval: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("approval id: %w", err)
		}
		grant.ApprovalID = id

		payload, err := json.Marshal(map[string]any{
			"approval_id":     id,
			"proposal_id":     req.ProposalID,
			"contract_digest": req.ContractDigest,
			"decision":        string(req.Decision),
			"expires_at":      domain.FormatTime(grant.ExpiresAt),
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}

		taskID := req.TaskID
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     req.Approver,
			Type:      auditType(req.Decision),
			Message:   fmt.Sprintf("approval %d for proposal %d recorded", id, req.ProposalID),
			Payload:   payload,
			CreatedAt: now,
		})
	})
	if err != nil {
		return Grant{}, err
	}

	return grant, nil
}

// Consume spends a token, binding it to contractDigest.
//
// The UPDATE both checks and consumes in one statement: token match, unconsumed
// state, approval decision, unexpired window and contract binding are all part
// of the WHERE clause, so there is no window between checking and spending.
func (s *Service) Consume(ctx context.Context, token, contractDigest string) (Consumed, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(contractDigest) == "" {
		return Consumed{}, fmt.Errorf("%w: token and contract digest are required", ErrInvalidRequest)
	}

	hash := hashToken(token)
	now := s.now().UTC()
	consumed := Consumed{ConsumedAt: now}

	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			`UPDATE approvals
			    SET consumed_at = ?
			  WHERE token_hash = ?
			    AND consumed_at IS NULL
			    AND decision = ?
			    AND expires_at > ?
			    AND contract_digest = ?
			  RETURNING id, proposal_id, approver`,
			domain.FormatTime(now), hash, string(Approved),
			domain.FormatTime(now), contractDigest,
		)

		var approver sql.NullString
		if err := row.Scan(&consumed.ApprovalID, &consumed.ProposalID, &approver); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errNoMatch
			}
			return fmt.Errorf("consume approval: %w", err)
		}
		consumed.Approver = approver.String

		if err := tx.QueryRowContext(ctx,
			`SELECT task_id FROM proposals WHERE id = ?`, consumed.ProposalID,
		).Scan(&consumed.TaskID); err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"approval_id":     consumed.ApprovalID,
			"proposal_id":     consumed.ProposalID,
			"contract_digest": contractDigest,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}

		taskID := consumed.TaskID
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     consumed.Approver,
			Type:      "approval.consumed",
			Message:   fmt.Sprintf("approval %d consumed for proposal %d", consumed.ApprovalID, consumed.ProposalID),
			Payload:   payload,
			CreatedAt: now,
		})
	})
	if errors.Is(err, errNoMatch) {
		return Consumed{}, s.explain(ctx, hash, contractDigest, now)
	}
	if err != nil {
		return Consumed{}, err
	}

	return consumed, nil
}

// explain turns "the conditional update matched nothing" into a specific error.
func (s *Service) explain(ctx context.Context, hash, contractDigest string, now time.Time) error {
	var (
		decision   string
		expiresAt  string
		consumedAt sql.NullString
		storedBind string
		proposalID int64
		approvalID int64
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, proposal_id, decision, expires_at, consumed_at, contract_digest
		   FROM approvals WHERE token_hash = ?`, hash,
	).Scan(&approvalID, &proposalID, &decision, &expiresAt, &consumedAt, &storedBind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect approval: %w", err)
	}

	if decision != string(Approved) {
		return ErrRejected
	}
	if consumedAt.Valid {
		return ErrAlreadyConsumed
	}
	if expires, err := domain.ParseTime(expiresAt); err != nil || !now.Before(expires) {
		return ErrExpired
	}
	if storedBind != contractDigest {
		return ErrContractMismatch
	}
	return ErrNotFound
}

func auditType(decision Decision) string {
	if decision == Rejected {
		return "approval.rejected"
	}
	return "approval.granted"
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isHex(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
