import { createRouter, createWebHistory } from 'vue-router'
import ApprovalQueue from './pages/ApprovalQueue.vue'
import AuditVerify from './pages/AuditVerify.vue'
import SafetyReport from './pages/SafetyReport.vue'
import TaskDetail from './pages/TaskDetail.vue'
import TaskList from './pages/TaskList.vue'

const routes = [
  { path: '/', name: 'task-list', component: TaskList, meta: { title: '任务列表' } },
  { path: '/tasks/:id', name: 'task-detail', component: TaskDetail, meta: { title: '任务详情' } },
  { path: '/approvals', name: 'approvals', component: ApprovalQueue, meta: { title: '审批队列' } },
  { path: '/audit', name: 'audit', component: AuditVerify, meta: { title: '审计校验' } },
  { path: '/safety', name: 'safety', component: SafetyReport, meta: { title: '安全报告' } },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

export default router
