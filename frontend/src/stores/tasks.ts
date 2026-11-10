import { defineStore } from 'pinia'
import * as api from '../api'
import { isTerminalStatus } from '../types'
import type {
  ApprovalItem,
  SafetyReport,
  TaskDetail,
  TaskSummary,
} from '../types'

export const useTasksStore = defineStore('tasks', {
  state: () => ({
    tasks: [] as TaskSummary[],
    tasksLoading: false,
    current: null as TaskDetail | null,
    currentLoading: false,
    approvals: [] as ApprovalItem[],
    approvalsLoading: false,
    safetyReport: null as SafetyReport | null,
    safetyLoading: false,
    lastError: '',
  }),
  getters: {
    activeTaskCount(state): number {
      return state.tasks.filter((task) => !isTerminalStatus(task.status)).length
    },
    pendingApprovals(state): ApprovalItem[] {
      return state.approvals.filter((item) => item.status === 'PENDING')
    },
  },
  actions: {
    async fetchTasks() {
      this.tasksLoading = true
      try {
        this.tasks = await api.listTasks()
        this.lastError = ''
      } catch (err) {
        this.lastError = api.errorMessage(err)
      } finally {
        this.tasksLoading = false
      }
    },
    async fetchTask(id: number) {
      this.currentLoading = true
      try {
        // The task, its evidence, its proposals and its outcomes are separate
        // resources on the server; the console shows them together.
        const [task, timeline, evidence, proposals, executions, verifications] = await Promise.all([
          api.getTask(id),
          api.getTimeline(id),
          api.getEvidence(id),
          api.listProposals({ task_id: id }),
          api.getExecutions(id),
          api.getVerifications(id),
        ])
        const approvals: ApprovalItem[] = []
        if (proposals.length > 0) {
          const all = await api.listApprovals(undefined, 200)
          approvals.push(...all.filter((item) => item.task_id === id))
        }
        this.current = { task, timeline, evidence, proposals, approvals, executions, verifications }
        this.lastError = ''
      } catch (err) {
        this.current = null
        this.lastError = api.errorMessage(err)
      } finally {
        this.currentLoading = false
      }
    },
    async fetchApprovals() {
      this.approvalsLoading = true
      try {
        this.approvals = await api.listApprovals('PENDING')
        this.lastError = ''
      } catch (err) {
        this.lastError = api.errorMessage(err)
      } finally {
        this.approvalsLoading = false
      }
    },
    async fetchSafetyReport() {
      this.safetyLoading = true
      try {
        this.safetyReport = await api.getSafetyReport()
        this.lastError = ''
      } catch (err) {
        this.safetyReport = null
        this.lastError = api.errorMessage(err)
      } finally {
        this.safetyLoading = false
      }
    },
    async refreshAll() {
      await Promise.all([this.fetchTasks(), this.fetchApprovals(), this.fetchSafetyReport()])
    },
    async submitTask(requestText: string): Promise<TaskSummary> {
      const created = await api.createTask(requestText)
      // The durable worker picks the task up on its own; there is no
      // "run the queue" button to press.
      await this.fetchTasks()
      return created
    },
  },
})
