import axios from 'axios'
import type {
  ApprovalDecisionResult,
  ApprovalItem,
  AuditVerifyResult,
  EvidenceItem,
  ExecutionItem,
  ExecutionResult,
  ProposalItem,
  SafetyReport,
  TaskSummary,
  TimelineEvent,
  VerificationItem,
  VerificationResult,
} from './types'

const client = axios.create({
  baseURL: '/api',
  timeout: 20000,
})

// A deployment that sets GRADOPS_API_TOKEN can store the same token in this
// browser key. It is never baked into the bundle by default.
if (typeof window !== 'undefined') {
  const token = window.localStorage.getItem('gradops_api_token')
  if (token) {
    client.defaults.headers.common.Authorization = `Bearer ${token}`
  }
}

export function errorMessage(err: unknown): string {
  if (axios.isAxiosError(err)) {
    const data = err.response?.data as { error?: { code?: string; message?: string } } | undefined
    if (typeof data?.error?.message === 'string' && data.error.message) {
      return data.error.message
    }
    if (err.message) return err.message
  }
  if (err instanceof Error && err.message) return err.message
  return '未知错误'
}

// --- tasks -----------------------------------------------------------------

export async function createTask(requestText: string, source = 'web'): Promise<TaskSummary> {
  const { data } = await client.post<TaskSummary>('/tasks', {
    request_text: requestText,
    source,
  })
  return data
}

export async function listTasks(status?: string, limit?: number): Promise<TaskSummary[]> {
  const { data } = await client.get<TaskSummary[]>('/tasks', {
    params: { status, limit },
  })
  return data
}

export async function getTask(id: number): Promise<TaskSummary> {
  const { data } = await client.get<TaskSummary>(`/tasks/${id}`)
  return data
}

export async function getTimeline(id: number): Promise<TimelineEvent[]> {
  const { data } = await client.get<TimelineEvent[]>(`/tasks/${id}/timeline`)
  return data
}

export async function getEvidence(id: number): Promise<EvidenceItem[]> {
  const { data } = await client.get<EvidenceItem[]>(`/tasks/${id}/evidence`)
  return data
}

export async function getExecutions(id: number): Promise<ExecutionItem[]> {
  const { data } = await client.get<ExecutionItem[]>(`/tasks/${id}/executions`)
  return data
}

export async function getVerifications(id: number): Promise<VerificationItem[]> {
  const { data } = await client.get<VerificationItem[]>(`/tasks/${id}/verifications`)
  return data
}

export async function verifyTask(id: number): Promise<VerificationResult> {
  const { data } = await client.post<VerificationResult>(`/tasks/${id}/verify`)
  return data
}

export async function recoverTask(id: number): Promise<ExecutionResult> {
  const { data } = await client.post<ExecutionResult>(`/tasks/${id}/recover`)
  return data
}

// --- proposals and approvals ------------------------------------------------

export async function listProposals(params: {
  task_id?: number
  status?: string
}): Promise<ProposalItem[]> {
  const { data } = await client.get<ProposalItem[]>('/proposals', { params })
  return data
}

export async function getProposal(id: number): Promise<ProposalItem> {
  const { data } = await client.get<ProposalItem>(`/proposals/${id}`)
  return data
}

export async function listApprovals(
  status?: string,
  limit?: number,
): Promise<ApprovalItem[]> {
  const { data } = await client.get<ApprovalItem[]>('/approvals', {
    params: { status, limit },
  })
  return data
}

/** Approving returns the one-time token; it is never retrievable afterwards. */
export async function approveProposal(
  proposalId: number,
  approver: string,
  comment: string,
): Promise<ApprovalDecisionResult> {
  const { data } = await client.post<ApprovalDecisionResult>(
    `/proposals/${proposalId}/approvals`,
    { approver, comment, decision: 'APPROVED' },
  )
  return data
}

export async function rejectProposal(
  proposalId: number,
  approver: string,
  comment: string,
): Promise<ApprovalDecisionResult> {
  const { data } = await client.post<ApprovalDecisionResult>(
    `/proposals/${proposalId}/approvals`,
    { approver, comment, decision: 'REJECTED' },
  )
  return data
}

export async function executeProposal(
  proposalId: number,
  token: string,
  actor = 'console',
): Promise<ExecutionResult> {
  const { data } = await client.post<ExecutionResult>(
    `/proposals/${proposalId}/execute`,
    { token, actor },
  )
  return data
}

// --- integrity ---------------------------------------------------------------

export async function verifyAudit(): Promise<AuditVerifyResult> {
  const { data } = await client.get<AuditVerifyResult>('/audit/verify')
  return data
}

export async function getSafetyReport(): Promise<SafetyReport> {
  const { data } = await client.get<SafetyReport>('/safety')
  return data
}
