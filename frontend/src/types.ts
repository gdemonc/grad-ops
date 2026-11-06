export type TaskStatus =
  | 'QUEUED'
  | 'INVESTIGATING'
  | 'AWAITING_APPROVAL'
  | 'EXECUTING'
  | 'VERIFYING'
  | 'ROLLING_BACK'
  | 'SEALED'
  | 'ROLLED_BACK'
  | 'RECOVERY_FAILED'
  | 'REJECTED'
  | 'FAILED'

export type ApprovalStatus = 'PENDING' | 'APPROVED' | 'REJECTED' | 'CONSUMED' | 'EXPIRED'

export type ProposalStatus =
  | 'PENDING_APPROVAL'
  | 'APPROVED'
  | 'REJECTED'
  | 'EXECUTING'
  | 'EXECUTED'
  | 'ROLLING_BACK'
  | 'FAILED'

export type TaskSummary = {
  id: number
  request_text: string
  source: string
  status: TaskStatus
  summary: string | null
  failure_stage: string | null
  created_at: string
  updated_at: string
}

export type EvidenceItem = {
  id: string
  tool: string
  command: string
  observed_at: string
  content: string
  content_hash: string
  truncated: boolean
}

export type Postcondition = {
  command: string
  expect_exit_code: number
  stdout_contains: string
}

export type ActionContract = {
  task_id: number
  tool: string
  kind: string
  script: string
  script_digest: string
  work_dir: string
  env: Record<string, string>
  paths: string[] | null
  run_as: string
  reversible: boolean
  policy_ref: string
  runtime: { timeout_ms: number; max_output_bytes: number; max_calls: number }
  postcondition: Postcondition
  compensation?: Postcondition | null
  risk: string
  policy_version: string
  approval_level: string
  nonce: string
  created_at: string
  expires_at: string
}

export type ProposalItem = {
  id: number
  task_id: number
  tool: string
  risk_level: string
  policy_version: string
  contract: ActionContract
  contract_digest: string
  status: ProposalStatus
  rationale: string
  created_at: string
}

export type ApprovalItem = {
  id: number
  proposal_id: number
  task_id: number
  approver: string
  comment: string
  decision: string
  status: ApprovalStatus
  expires_at: string
  consumed_at: string
  created_at: string
  tool: string
  risk_level: string
  proposal_status: string
  request_text: string
}

export type ExecutionItem = {
  id: number
  proposal_id: number
  outcome: string
  exit_code: number
  output_truncated: boolean
  run_as: string
  started_at: string
  finished_at: string
  duration_ms: number
}

export type VerificationItem = {
  id: number
  task_id: number
  proposal_id: number
  postcondition: string
  passed: boolean
  evidence_id: string
  checked_at: string
}

export type TimelineEvent = {
  seq: number
  task_id: number | null
  type: string
  actor: string
  message: string
  payload: unknown
  created_at: string
}

export type TaskDetail = {
  task: TaskSummary
  timeline: TimelineEvent[]
  evidence: EvidenceItem[]
  proposals: ProposalItem[]
  approvals: ApprovalItem[]
  executions: ExecutionItem[]
  verifications: VerificationItem[]
}

export type AuditVerifyResult = {
  ok: boolean
  events: number
  broken_seq?: number
  reason?: string
}

export type SafetyRuntime = {
  bash: string
  work_dir: string
  contract_work_dir: string
  allowed_roots: string[]
  policy_version: string
  approval_ttl_ms: number
  worker_enabled: boolean
  require_run_as_match: boolean
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
  runtime: SafetyRuntime
  guards: SafetyGuard[]
}

export type ApprovalDecisionResult = {
  approval_id: number
  decision: string
  expires_at: string
  token?: string
}

export type ExecutionResult = {
  execution_id: number
  proposal_id: number
  task_id: number
  status: string
  exit_code: number
  stdout: string
  stderr: string
  timed_out: boolean
  truncated: boolean
  duration_ms: number
}

export type VerificationResult = {
  task_id: number
  proposal_id: number
  passed: boolean
  evidence_id: string
  exit_code: number
  stdout: string
  failures: string[]
  checked_at: string
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
  ROLLING_BACK: { label: '回滚中', color: 'orangered' },
  SEALED: { label: '已封存', color: 'green' },
  ROLLED_BACK: { label: '已回滚', color: 'purple' },
  RECOVERY_FAILED: { label: '恢复失败', color: 'red' },
  REJECTED: { label: '已拒绝', color: 'red' },
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
  PENDING: { label: '待执行', color: 'orange' },
  APPROVED: { label: '已批准', color: 'green' },
  REJECTED: { label: '已拒绝', color: 'red' },
  CONSUMED: { label: '令牌已消费', color: 'gray' },
  EXPIRED: { label: '令牌已过期', color: 'gray' },
}

export const PROPOSAL_STATUS_META: Record<string, StatusMeta> = {
  PENDING_APPROVAL: { label: '待审批', color: 'orange' },
  APPROVED: { label: '已批准', color: 'green' },
  REJECTED: { label: '已拒绝', color: 'red' },
  EXECUTING: { label: '执行中', color: 'blue' },
  EXECUTED: { label: '已执行', color: 'cyan' },
  ROLLING_BACK: { label: '回滚中', color: 'orangered' },
  FAILED: { label: '执行失败', color: 'red' },
}

export const GUARD_STATUS_META: Record<string, StatusMeta> = {
  ok: { label: '正常', color: 'green' },
  warn: { label: '未实现 / 降级', color: 'orange' },
  blocked: { label: '已阻断', color: 'red' },
}

const EVENT_COLORS: Record<string, string> = {
  task: '#86909c',
  job: '#86909c',
  investigation: '#3491db',
  evidence: '#3491db',
  policy: '#0fc6c2',
  approval: '#ff7d00',
  execution: '#f53f3f',
  executor: '#f53f3f',
  verification: '#00b42a',
  recovery: '#722ed1',
}

const TERMINAL_STATUSES: readonly string[] = [
  'SEALED',
  'ROLLED_BACK',
  'RECOVERY_FAILED',
  'REJECTED',
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

/** Audit event types are dotted (`approval.consumed`); colour by the prefix. */
export function eventTypeColor(eventType: string): string {
  return EVENT_COLORS[eventType.split('.')[0]] ?? '#86909c'
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

export function prettyJson(value: unknown): string {
  if (value === null || value === undefined) return ''
  try {
    return JSON.stringify(value, null, 2) ?? String(value)
  } catch {
    return String(value)
  }
}

export function shortHash(value: string | null | undefined): string {
  if (!value) return '—'
  return value.length > 16 ? `${value.slice(0, 12)}…` : value
}
