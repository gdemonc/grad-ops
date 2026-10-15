export type TaskStatus =
  | 'QUEUED'
  | 'INVESTIGATING'
  | 'AWAITING_APPROVAL'
  | 'EXECUTING'
  | 'VERIFYING'
  | 'SEALED'
  | 'REJECTED'
  | 'BLOCKED'
  | 'ROLLED_BACK'
  | 'FAILED'

export type ApprovalStatus = 'PENDING' | 'APPROVED' | 'REJECTED' | 'CONSUMED'

export type ProposalStatus =
  | 'DRAFT'
  | 'PENDING_APPROVAL'
  | 'APPROVED'
  | 'REJECTED'
  | 'DENIED_BY_POLICY'
  | 'EXECUTED'
  | 'FAILED'

export type TaskSummary = {
  id: number
  trace_id: string
  request_text: string
  status: TaskStatus
  summary: string | null
  created_at: string
}

export type EvidenceItem = {
  id: number
  kind: string
  summary: string
  content_hash: string
  created_at: string
}

export type ProposalItem = {
  id: number
  tool_name: string
  risk_level: string
  rationale: string
  status: ProposalStatus
  contract_digest: string | null
  preconditions_json: unknown
  postconditions_json: unknown
}

export type ApprovalItem = {
  id: number
  proposal_id: number
  status: ApprovalStatus
  decided_by: string | null
  comment: string | null
}

export type ExecutionItem = {
  id: number
  proposal_id: number
  ok: boolean
  exit_code: number | null
  stdout: string | null
  stderr: string | null
  duration_ms: number | null
  executed_at: string | null
}

export type VerificationCheck = {
  name: string
  ok: boolean
  detail: string
  tool_call_names: string[]
}

export type VerificationItem = {
  id: number
  proposal_id: number
  checks_json: VerificationCheck[]
  passed: boolean
  note: string | null
  verified_at: string | null
}

export type TaskDetail = {
  id: number
  trace_id: string
  request_text: string
  source: string
  status: TaskStatus
  summary: string | null
  created_at: string
  updated_at: string
  evidence: EvidenceItem[]
  proposals: ProposalItem[]
  approvals: ApprovalItem[]
  executions: ExecutionItem[]
  verifications: VerificationItem[]
  grounding_summary: GroundingSummary | null
}

export type GroundingSummary = {
  supported?: GroundingClaim[]
  unsupported?: GroundingClaim[]
  [key: string]: unknown
}

export type GroundingClaim = {
  text?: string
  evidence_ids?: string[]
  [key: string]: unknown
}

export type TimelineEvent = {
  seq: number
  stage: string
  event_type: string
  message: string
  payload_json: Record<string, unknown> | null
  created_at: string
}

export type ApprovalQueueItem = {
  id: number
  proposal_id: number
  status: ApprovalStatus
  decided_by: string | null
  decided_at: string | null
  comment: string | null
  tool_name: string
  risk_level: string
  task_id: number
  request_text: string
}

export type AuditVerifyEntry = {
  chain_id: number
  seq: number
  prev_ok: boolean
  payload_ok: boolean
  event_ok: boolean
  valid: boolean
  stored_event_hash: string
  expected_event_hash: string
}

export type AuditVerifyResult = {
  trace_id: string
  valid: boolean
  entry_count: number
  head_hash: string
  entries?: AuditVerifyEntry[]
}

export type SafetyExecutor = {
  runtime_user: string
  runtime_uid: number
  target_user: string
  allow_root_executor: boolean
  action_execution_enabled: boolean
}

export type SafetyBoundary = {
  allowed_tools: string[]
  allowed_path_prefixes: string[]
  protected_path_prefixes: string[]
}

export type SafetyGuard = {
  key: string
  name: string
  status: 'ok' | 'warn' | 'blocked'
  detail: string
}

export type SafetyReport = {
  overall_status: 'ok' | 'warn' | 'blocked'
  summary: string
  executor: SafetyExecutor
  boundary: SafetyBoundary
  guards: SafetyGuard[]
}

export type CreateTaskResult = {
  id: number
  trace_id: string
  status: TaskStatus
}

export type RunQueueResult = {
  worked: boolean
}

export type ApprovalDecisionResult = {
  approval_id: number
  status: ApprovalStatus
  resumed: boolean
}

export type StatusMeta = {
  label: string
  color: string
}

export const TASK_STATUS_META: Record<string, StatusMeta> = {
  QUEUED: { label: '排队中', color: 'blue' },
  INVESTIGATING: { label: '调查中', color: 'blue' },
  AWAITING_APPROVAL: { label: '待审批', color: 'orange' },
  EXECUTING: { label: '执行中', color: 'blue' },
  VERIFYING: { label: '验证中', color: 'blue' },
  SEALED: { label: '已封存', color: 'green' },
  REJECTED: { label: '已拒绝', color: 'red' },
  BLOCKED: { label: '已阻断', color: 'red' },
  ROLLED_BACK: { label: '已回滚', color: 'red' },
  FAILED: { label: '失败', color: 'red' },
}

export const RISK_META: Record<string, StatusMeta> = {
  R0: { label: 'R0 只读', color: 'green' },
  R1: { label: 'R1 可逆', color: 'cyan' },
  R2: { label: 'R2 需审批', color: 'orange' },
  R3: { label: 'R3 高风险', color: 'orangered' },
  R4: { label: 'R4 拒绝', color: 'red' },
}

export const APPROVAL_STATUS_META: Record<string, StatusMeta> = {
  PENDING: { label: '待审批', color: 'orange' },
  APPROVED: { label: '已批准', color: 'green' },
  REJECTED: { label: '已拒绝', color: 'red' },
  CONSUMED: { label: '令牌已消费', color: 'gray' },
}

export const PROPOSAL_STATUS_META: Record<string, StatusMeta> = {
  DRAFT: { label: '草稿', color: 'gray' },
  PENDING_APPROVAL: { label: '待审批', color: 'orange' },
  APPROVED: { label: '已批准', color: 'green' },
  REJECTED: { label: '已拒绝', color: 'red' },
  DENIED_BY_POLICY: { label: '策略拒绝', color: 'red' },
  EXECUTED: { label: '已执行', color: 'blue' },
  FAILED: { label: '执行失败', color: 'red' },
}

export const GUARD_STATUS_META: Record<string, StatusMeta> = {
  ok: { label: '正常', color: 'green' },
  warn: { label: '告警', color: 'orange' },
  blocked: { label: '已阻断', color: 'red' },
}

const STAGE_COLORS: Record<string, string> = {
  queue: '#86909c',
  worker: '#86909c',
  investigation: '#3491db',
  grounding: '#722ed1',
  policy: '#0fc6c2',
  approval: '#ff7d00',
  executor: '#f53f3f',
  verification: '#00b42a',
}

const TERMINAL_STATUSES: readonly string[] = [
  'SEALED',
  'REJECTED',
  'BLOCKED',
  'ROLLED_BACK',
  'FAILED',
]

export function taskStatusMeta(status: string): StatusMeta {
  return TASK_STATUS_META[status] ?? { label: status, color: 'gray' }
}

export function riskMeta(risk: string): StatusMeta {
  return RISK_META[risk] ?? { label: risk, color: 'gray' }
}

export function approvalStatusMeta(status: string): StatusMeta {
  return APPROVAL_STATUS_META[status] ?? { label: status, color: 'gray' }
}

export function proposalStatusMeta(status: string): StatusMeta {
  return PROPOSAL_STATUS_META[status] ?? { label: status, color: 'gray' }
}

export function guardStatusMeta(status: string): StatusMeta {
  return GUARD_STATUS_META[status] ?? { label: status, color: 'gray' }
}

export function stageColor(stage: string): string {
  return STAGE_COLORS[stage] ?? '#86909c'
}

export function isTerminalStatus(status: string): boolean {
  return TERMINAL_STATUSES.includes(status)
}

export function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const date = value.slice(0, 10)
  const time = value.slice(11, 19)
  return time ? `${date} ${time}` : value
}

export function shortTrace(traceId: string): string {
  if (!traceId) return '—'
  return traceId.length > 12 ? `${traceId.slice(0, 8)}…` : traceId
}

export function prettyJson(value: unknown): string {
  if (value === null || value === undefined) return ''
  try {
    return JSON.stringify(value, null, 2) ?? String(value)
  } catch {
    return String(value)
  }
}
