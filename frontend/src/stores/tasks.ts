import { defineStore } from 'pinia'
import * as api from '../api'
import { isTerminalStatus } from '../types'
import type {
  ApprovalQueueItem,
  CreateTaskResult,
  RunQueueResult,
  SafetyReport,
  TaskDetail,
  TaskSummary,
  TimelineEvent,
} from '../types'

export const useTasksStore = defineStore('tasks', {
  state: () => ({
    tasks: [] as TaskSummary[],
    tasksLoading: false,
    current: null as TaskDetail | null,
    currentLoading: false,
    timeline: [] as TimelineEvent[],
    timelineLoading: false,
    approvals: [] as ApprovalQueueItem[],
    approvalsLoading: false,
    safetyReport: null as SafetyReport | null,
    safetyLoading: false,
    lastError: '',
  }),
  getters: {
    activeTaskCount(state): number {
      return state.tasks.filter((task) => !isTerminalStatus(task.status)).length
    },
    pendingApprovals(state): ApprovalQueueItem[] {
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
        this.current = await api.getTask(id)
        this.lastError = ''
      } catch (err) {
        this.current = null
        this.lastError = api.errorMessage(err)
      } finally {
        this.currentLoading = false
      }
    },
    async fetchTimeline(id: number) {
      this.timelineLoading = true
      try {
        this.timeline = await api.getTimeline(id)
        this.lastError = ''
      } catch (err) {
        this.timeline = []
        this.lastError = api.errorMessage(err)
      } finally {
        this.timelineLoading = false
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
      await Promise.all([
        this.fetchTasks(),
        this.fetchApprovals(),
        this.fetchSafetyReport(),
      ])
    },
    async submitTask(requestText: string): Promise<CreateTaskResult> {
      const created = await api.createTask(requestText)
      await api.runQueue().catch(() => undefined)
      await this.fetchTasks()
      return created
    },
    async runQueueOnce(): Promise<RunQueueResult> {
      const result = await api.runQueue()
      await this.fetchTasks()
      return result
    },
  },
})
