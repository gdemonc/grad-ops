package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/domain"
)

// RuntimeInfo is the configuration the operator can see reflected in the API.
// It is a copy, not the live config: the API reports what the process was told
// to do, it does not decide it.
type RuntimeInfo struct {
	Bash            string   `json:"bash"`
	WorkDir         string   `json:"work_dir"`
	ContractWorkDir string   `json:"contract_work_dir"`
	AllowedRoots    []string `json:"allowed_roots"`
	PolicyVersion   string   `json:"policy_version"`
	ApprovalTTLMS   int64    `json:"approval_ttl_ms"`
	WorkerEnabled   bool     `json:"worker_enabled"`
}

type approvalItemDTO struct {
	ID             int64  `json:"id"`
	ProposalID     int64  `json:"proposal_id"`
	TaskID         int64  `json:"task_id"`
	Approver       string `json:"approver"`
	Comment        string `json:"comment"`
	Decision       string `json:"decision"`
	Status         string `json:"status"`
	ExpiresAt      string `json:"expires_at"`
	ConsumedAt     string `json:"consumed_at"`
	CreatedAt      string `json:"created_at"`
	Tool           string `json:"tool"`
	RiskLevel      string `json:"risk_level"`
	ProposalStatus string `json:"proposal_status"`
	RequestText    string `json:"request_text"`
}

type executionItemDTO struct {
	ID              int64  `json:"id"`
	ProposalID      int64  `json:"proposal_id"`
	Outcome         string `json:"outcome"`
	ExitCode        int    `json:"exit_code"`
	OutputTruncated bool   `json:"output_truncated"`
	RunAs           string `json:"run_as"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
	DurationMS      int64  `json:"duration_ms"`
}

type verificationItemDTO struct {
	ID            int64  `json:"id"`
	TaskID        int64  `json:"task_id"`
	ProposalID    int64  `json:"proposal_id"`
	Postcondition string `json:"postcondition"`
	Passed        bool   `json:"passed"`
	EvidenceID    string `json:"evidence_id"`
	CheckedAt     string `json:"checked_at"`
}

type guardDTO struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type safetyDTO struct {
	OverallStatus string      `json:"overall_status"`
	Summary       string      `json:"summary"`
	Runtime       RuntimeInfo `json:"runtime"`
	Guards        []guardDTO  `json:"guards"`
}

// listApprovals serves the review queue. The stored decision and the effective
// state of the credential are different things: an approval can be approved,
// expired or already spent, and the queue must show which.
func (h *handlers) listApprovals(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "approvals are not configured")
		return
	}

	want := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))
	limit, err := parseLimit(r, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	rows, err := h.deps.DB.QueryContext(r.Context(),
		`SELECT a.id, a.proposal_id, p.task_id, COALESCE(a.approver, ''), COALESCE(a.comment, ''),
		        a.decision, COALESCE(a.expires_at, ''), COALESCE(a.consumed_at, ''), a.created_at,
		        p.tool, p.risk_level, p.status, t.request_text
		   FROM approvals a
		   JOIN proposals p ON p.id = a.proposal_id
		   JOIN tasks t     ON t.id = p.task_id
		  ORDER BY a.id DESC
		  LIMIT ?`, limit)
	if err != nil {
		h.writeActionError(w, fmt.Errorf("list approvals: %w", err))
		return
	}
	defer rows.Close()

	now := time.Now().UTC()
	out := make([]approvalItemDTO, 0)
	for rows.Next() {
		var (
			item      approvalItemDTO
			expiresAt string
			consumed  string
		)
		if err := rows.Scan(&item.ID, &item.ProposalID, &item.TaskID, &item.Approver, &item.Comment,
			&item.Decision, &expiresAt, &consumed, &item.CreatedAt,
			&item.Tool, &item.RiskLevel, &item.ProposalStatus, &item.RequestText); err != nil {
			h.writeActionError(w, fmt.Errorf("scan approval: %w", err))
			return
		}
		item.ExpiresAt = expiresAt
		item.ConsumedAt = consumed
		item.Status = effectiveApprovalStatus(item.Decision, expiresAt, consumed, now)
		if want != "" && item.Status != want {
			continue
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		h.writeActionError(w, fmt.Errorf("list approvals: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func effectiveApprovalStatus(decision, expiresAt, consumedAt string, now time.Time) string {
	if decision == string(approval.Rejected) {
		return "REJECTED"
	}
	if consumedAt != "" {
		return "CONSUMED"
	}
	if expiresAt != "" {
		expiry, err := domain.ParseTime(expiresAt)
		if err == nil && !expiry.After(now) {
			return "EXPIRED"
		}
	}
	return "PENDING"
}

// listProposals returns the frozen actions of a task, or of everything.
func (h *handlers) listProposals(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "proposals are not configured")
		return
	}

	query := r.URL.Query()
	limit, err := parseLimit(r, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	where := []string{"1 = 1"}
	args := []any{}

	if raw := strings.TrimSpace(query.Get("task_id")); raw != "" {
		taskID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || taskID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "task_id must be a positive integer")
			return
		}
		where = append(where, "task_id = ?")
		args = append(args, taskID)
	}
	if raw := strings.ToUpper(strings.TrimSpace(query.Get("status"))); raw != "" {
		where = append(where, "status = ?")
		args = append(args, raw)
	}
	args = append(args, limit)

	rows, err := h.deps.DB.QueryContext(r.Context(),
		`SELECT id, task_id, tool, risk_level, policy_version, contract, contract_digest,
		        status, COALESCE(rationale, ''), created_at
		   FROM proposals
		  WHERE `+strings.Join(where, " AND ")+`
		  ORDER BY id DESC
		  LIMIT ?`, args...)
	if err != nil {
		h.writeActionError(w, fmt.Errorf("list proposals: %w", err))
		return
	}
	defer rows.Close()

	out := make([]proposalDTO, 0)
	for rows.Next() {
		var (
			item         proposalDTO
			contractJSON string
		)
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Tool, &item.RiskLevel, &item.PolicyVersion,
			&contractJSON, &item.ContractDigest, &item.Status, &item.Rationale, &item.CreatedAt); err != nil {
			h.writeActionError(w, fmt.Errorf("scan proposal: %w", err))
			return
		}
		item.Contract = rawJSON(contractJSON)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		h.writeActionError(w, fmt.Errorf("list proposals: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) listExecutions(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "executions are not configured")
		return
	}

	taskID, err := parsePathID(r, "task id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	rows, err := h.deps.DB.QueryContext(r.Context(),
		`SELECT e.id, e.proposal_id, e.outcome, COALESCE(e.exit_code, -1), e.output_truncated,
		        COALESCE(e.run_as, ''), e.started_at, COALESCE(e.finished_at, ''),
		        COALESCE(e.duration_ms, 0)
		   FROM execution_records e
		   JOIN proposals p ON p.id = e.proposal_id
		  WHERE p.task_id = ?
		  ORDER BY e.id`, taskID)
	if err != nil {
		h.writeActionError(w, fmt.Errorf("list executions: %w", err))
		return
	}
	defer rows.Close()

	out := make([]executionItemDTO, 0)
	for rows.Next() {
		var (
			item      executionItemDTO
			truncated int
		)
		if err := rows.Scan(&item.ID, &item.ProposalID, &item.Outcome, &item.ExitCode, &truncated,
			&item.RunAs, &item.StartedAt, &item.FinishedAt, &item.DurationMS); err != nil {
			h.writeActionError(w, fmt.Errorf("scan execution: %w", err))
			return
		}
		item.OutputTruncated = truncated == 1
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		h.writeActionError(w, fmt.Errorf("list executions: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) listVerifications(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "verifications are not configured")
		return
	}

	taskID, err := parsePathID(r, "task id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	rows, err := h.deps.DB.QueryContext(r.Context(),
		`SELECT id, task_id, COALESCE(proposal_id, 0), postcondition, passed,
		        COALESCE(evidence_id, ''), checked_at
		   FROM verification_records
		  WHERE task_id = ?
		  ORDER BY id`, taskID)
	if err != nil {
		h.writeActionError(w, fmt.Errorf("list verifications: %w", err))
		return
	}
	defer rows.Close()

	out := make([]verificationItemDTO, 0)
	for rows.Next() {
		var (
			item   verificationItemDTO
			passed int
		)
		if err := rows.Scan(&item.ID, &item.TaskID, &item.ProposalID, &item.Postcondition, &passed,
			&item.EvidenceID, &item.CheckedAt); err != nil {
			h.writeActionError(w, fmt.Errorf("scan verification: %w", err))
			return
		}
		item.Passed = passed == 1
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		h.writeActionError(w, fmt.Errorf("list verifications: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, out)
}

// safety reports the boundary the process is actually running with. Guards that
// are not implemented are reported as such instead of being implied by a green
// dashboard.
func (h *handlers) safety(w http.ResponseWriter, _ *http.Request) {
	info := h.deps.Runtime

	guards := []guardDTO{
		{Key: "policy", Name: "确定性策略", Status: "ok",
			Detail: fmt.Sprintf("策略版本 %s，判定不读取模型自报风险", info.PolicyVersion)},
		{Key: "contract", Name: "动作契约", Status: "ok",
			Detail: "执行前重算规范化摘要，不一致即拒绝"},
		{Key: "approval", Name: "一次性审批", Status: "ok",
			Detail: fmt.Sprintf("只存 token 哈希，有效期 %s，绑定契约摘要，单条 SQL 原子消费",
				(time.Duration(info.ApprovalTTLMS) * time.Millisecond).String())},
		{Key: "paths", Name: "路径边界", Status: "ok",
			Detail: "动作声明的路径必须落在允许根目录内：" + strings.Join(info.AllowedRoots, ", ")},
		{Key: "identity", Name: "运行身份", Status: "warn",
			Detail: "契约要求声明非 root 的 run_as，但服务不会切换身份：脚本以服务进程身份运行"},
		{Key: "isolation", Name: "运行时隔离", Status: "warn",
			Detail: "未实现：无 uid 切换、namespace、cgroup 或 systemd 单元；目前只有进程组、超时与输出上限"},
		{Key: "audit", Name: "审计链", Status: "ok",
			Detail: "SHA-256 链，事件与状态变更在同一事务写入"},
		{Key: "worker", Name: "持久 Worker", Status: "ok",
			Detail: fmt.Sprintf("启用=%t；带副作用的作业不会自动重放", info.WorkerEnabled)},
	}

	overall := "ok"
	for _, guard := range guards {
		if guard.Status == "blocked" {
			overall = "blocked"
			break
		}
		if guard.Status == "warn" {
			overall = "warn"
		}
	}

	summary := "策略、契约、审批与审计已生效；运行时隔离尚未实现，主机权限边界低于目标设计"
	if overall == "ok" {
		summary = "全部检查项正常"
	}

	writeJSON(w, http.StatusOK, safetyDTO{
		OverallStatus: overall,
		Summary:       summary,
		Runtime:       info,
		Guards:        guards,
	})
}

func parseLimit(r *http.Request, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > 1000 {
		return 0, fmt.Errorf("%w: limit must be an integer between 1 and 1000", domain.ErrInvalidRequest)
	}
	return value, nil
}

func rawJSON(value string) []byte {
	return []byte(value)
}
