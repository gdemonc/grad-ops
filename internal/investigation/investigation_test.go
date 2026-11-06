package investigation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
)

func requireBash(t *testing.T) string {
	t.Helper()

	candidate := os.Getenv("GRADOPS_TEST_BASH")
	if candidate == "" {
		found, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("no bash available; set GRADOPS_TEST_BASH")
		}
		candidate = found
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("resolve bash: %v", err)
	}
	probe := exec.Command(absolute, "--noprofile", "--norc", "-c", "printf ok")
	if output, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Skipf("bash at %s is not usable: %v (%s)", absolute, err, output)
	}
	return absolute
}

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

func seedTask(t *testing.T, db *store.DB, status domain.Status) int64 {
	t.Helper()

	var taskID int64
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		now := domain.FormatTime(time.Now())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"check the app", string(status), now, now)
		if err != nil {
			return err
		}
		taskID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return taskID
}

func newService(t *testing.T, db *store.DB, planner investigation.Planner) *investigation.Service {
	t.Helper()

	workdir := t.TempDir()
	engine, err := policy.New(policy.Config{Version: "test-policy", AllowedRoots: []string{"/var/log"}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	service, err := investigation.New(db, engine, planner, investigation.Config{
		Bash:           requireBash(t),
		BaseEnv:        map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		WorkDir:        workdir,
		Timeout:        5 * time.Second,
		MaxOutputBytes: 4096,
		MaxCalls:       5,
	})
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	return service
}

func TestConcludeSealsTheTaskWithEvidence(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	service := newService(t, db, investigation.ScriptedPlanner{
		Probes:    []string{"printf 'disk 40% used'"},
		Summary:   "disk usage is moderate",
		Citations: []int{1},
	})

	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusSealed {
		t.Fatalf("expected SEALED, got %s", result.Status)
	}
	if len(result.EvidenceIDs) != 1 {
		t.Fatalf("expected one evidence record, got %v", result.EvidenceIDs)
	}

	var status, summary string
	if err := db.QueryRow(`SELECT status, summary FROM tasks WHERE id = ?`, taskID).Scan(&status, &summary); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusSealed) || summary != "disk usage is moderate" {
		t.Fatalf("unexpected task: status=%s summary=%q", status, summary)
	}

	verified, err := audit.Verify(context.Background(), db)
	if err != nil || !verified.OK {
		t.Fatalf("audit chain: ok=%v err=%v", verified.OK, err)
	}
}

func TestUngroundedConclusionIsRejected(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	service := newService(t, db, investigation.ScriptedPlanner{
		Probes:    []string{"printf 'one'"},
		Summary:   "the second probe says it is fine",
		Citations: []int{2},
	})

	if _, err := service.Investigate(context.Background(), taskID); !errors.Is(err, investigation.ErrUngrounded) {
		t.Fatalf("expected ErrUngrounded, got %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusInvestigating) {
		t.Fatalf("a rejected conclusion must not move the task, got %s", status)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM evidence WHERE task_id = ?`, taskID).Scan(&count); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if count != 0 {
		t.Fatalf("the transaction should have rolled back, found %d evidence rows", count)
	}
}

func TestProposalIsCreatedAndAwaitingApproval(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	action := &investigation.Action{
		Kind:       policy.KindServiceControl,
		Script:     "printf 'restarted'",
		Paths:      []string{"/var/log/app.log"},
		RunAs:      "gradops-agent",
		Reversible: true,
		Postcondition: contract.Postcondition{
			Command:        "printf 'active'",
			StdoutContains: "active",
		},
		Rationale: "the service is not running",
	}

	service := newService(t, db, investigation.ScriptedPlanner{
		Probes: []string{"printf 'inactive'"},
		Action: action,
	})

	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusAwaitingApproval {
		t.Fatalf("expected AWAITING_APPROVAL, got %s", result.Status)
	}
	if result.ProposalID == 0 {
		t.Fatal("expected a proposal id")
	}

	var (
		raw, digest, status, policyVersion string
	)
	if err := db.QueryRow(
		`SELECT contract, contract_digest, status, policy_version FROM proposals WHERE id = ?`,
		result.ProposalID).Scan(&raw, &digest, &status, &policyVersion); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if status != "PENDING_APPROVAL" {
		t.Fatalf("expected PENDING_APPROVAL, got %s", status)
	}
	if policyVersion != "test-policy" {
		t.Fatalf("unexpected policy version %q", policyVersion)
	}

	var c contract.Contract
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if c.Risk != string(domain.RiskR2) {
		t.Fatalf("expected the contract to record R2, got %s", c.Risk)
	}
	if err := contract.Verify(c, digest); err != nil {
		t.Fatalf("stored contract does not match its digest: %v", err)
	}
}

func TestDeniedActionRejectsTheTask(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	action := &investigation.Action{
		Kind:      policy.KindServiceControl,
		Script:    "sudo systemctl restart nginx",
		Paths:     []string{"/var/log/app.log"},
		RunAs:     "gradops-agent",
		Rationale: "restart",
	}

	service := newService(t, db, investigation.ScriptedPlanner{
		Probes: []string{"printf 'x'"},
		Action: action,
	})

	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusRejected {
		t.Fatalf("expected REJECTED, got %s", result.Status)
	}

	var stage sql.NullString
	if err := db.QueryRow(`SELECT failure_stage FROM tasks WHERE id = ?`, taskID).Scan(&stage); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if !stage.Valid || stage.String != "policy" {
		t.Fatalf("expected failure stage policy, got %v", stage)
	}

	var proposals int
	if err := db.QueryRow(`SELECT COUNT(*) FROM proposals WHERE task_id = ?`, taskID).Scan(&proposals); err != nil {
		t.Fatalf("count proposals: %v", err)
	}
	if proposals != 0 {
		t.Fatal("a denied action must not create a proposal")
	}
}

func TestInvestigateRequiresTheInvestigatingState(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusQueued)

	service := newService(t, db, investigation.ScriptedPlanner{
		Probes:    []string{"printf 'x'"},
		Summary:   "x",
		Citations: []int{1},
	})

	if _, err := service.Investigate(context.Background(), taskID); !errors.Is(err, investigation.ErrWrongState) {
		t.Fatalf("expected ErrWrongState, got %v", err)
	}
}
