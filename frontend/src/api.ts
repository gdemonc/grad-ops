import axios from 'axios'
import type {
  ApprovalDecisionResult,
  ApprovalQueueItem,
  AuditVerifyResult,
  CreateTaskResult,
  RunQueueResult,
  SafetyReport,
  TaskDetail,
  TaskSummary,
  TimelineEvent,
} from './types'

const client = axios.create({
  baseURL: '/api',
  timeout: 20000,
})

export function errorMessage(err: unknown): string {
  if (axios.isAxiosError(err)) {
    const data = err.response?.data as { detail?: unknown } | undefined
    if (typeof data?.detail === 'string') return data.detail
    if (data?.detail !== undefined) return JSON.stringify(data.detail)
    if (err.message) return err.message
  }
  if (err instanceof Error && err.message) return err.message
  return '未知错误'
}

export async function createTask(requestText: string, source = 'web'): Promise<CreateTaskResult> {
  const { data } = await client.post<CreateTaskResult>('/tasks', {
    request_text: requestText,
    source,
  })
  return data
}

export async function listTasks(status?: string): Promise<TaskSummary[]> {
  const { data } = await client.get<TaskSummary[]>('/tasks', {
    params: status ? { status } : {},
  })
  return data
}

export async function getTask(id: number): Promise<TaskDetail> {
  const { data } = await client.get<TaskDetail>(`/tasks/${id}`)
  return data
}

export async function getTimeline(id: number): Promise<TimelineEvent[]> {
  const { data } = await client.get<TimelineEvent[]>(`/tasks/${id}/timeline`)
  return data
}

export async function runQueue(): Promise<RunQueueResult> {
  const { data } = await client.post<RunQueueResult>('/tasks/run')
  return data
}

export async function listApprovals(status = 'PENDING'): Promise<ApprovalQueueItem[]> {
  const { data } = await client.get<ApprovalQueueItem[]>('/approvals', {
    params: { status },
  })
  return data
}

export async function approveApproval(
  approvalId: number,
  comment: string,
): Promise<ApprovalDecisionResult> {
  const { data } = await client.post<ApprovalDecisionResult>(
    `/approvals/${approvalId}/approve`,
    { comment },
  )
  return data
}

export async function rejectApproval(
  approvalId: number,
  comment: string,
): Promise<ApprovalDecisionResult> {
  const { data } = await client.post<ApprovalDecisionResult>(
    `/approvals/${approvalId}/reject`,
    { comment },
  )
  return data
}

export async function verifyAudit(traceId: string, full = false): Promise<AuditVerifyResult> {
  const { data } = await client.get<AuditVerifyResult>(
    `/audit/${encodeURIComponent(traceId)}/verify`,
    { params: { full } },
  )
  return data
}

export async function getSafetyReport(): Promise<SafetyReport> {
  const { data } = await client.get<SafetyReport>('/safety/report')
  return data
}
