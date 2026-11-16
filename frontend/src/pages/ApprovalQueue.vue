<script setup lang="ts">
import { IconRefresh } from '@arco-design/web-vue/es/icon'
import { Message } from '@arco-design/web-vue'
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  approveProposal,
  errorMessage,
  executeProposal,
  rejectProposal,
  verifyTask,
} from '../api'
import FileTargetSummary from '../components/FileTargetSummary.vue'
import { useTasksStore } from '../stores/tasks'
import { formatTime, riskMeta } from '../types'
import type { ApprovalItem } from '../types'

const store = useTasksStore()
const router = useRouter()

const comments = reactive<Record<number, string>>({})
const pendingTokens = reactive<Record<number, string>>({})
const actingId = ref(0)
const approver = ref('operator')

const columns = [
  { title: 'ID', dataIndex: 'id', width: 72 },
  { title: '处置提案', slotName: 'proposal', width: 420 },
  { title: '任务请求', slotName: 'task' },
  { title: '有效期', slotName: 'expires', width: 170 },
  { title: '决策', slotName: 'action', width: 160 },
]

async function refresh() {
  await store.fetchApprovals()
}

async function grantApproval(record: ApprovalItem): Promise<string> {
  const comment = (comments[record.id] ?? '').trim()
  const grant = await approveProposal(record.proposal_id, approver.value, comment)
  if (!grant.token) throw new Error('审批没有返回 token')
  return grant.token
}

async function executeAndVerify(record: ApprovalItem, token: string) {
  const execution = await executeProposal(record.proposal_id, token, approver.value)
  if (execution.status === 'SUCCEEDED') {
    const verification = await verifyTask(record.task_id)
    Message[verification.passed ? 'success' : 'warning'](
      verification.passed ? '已执行，独立验证通过' : '已执行，但验证未通过',
    )
  } else {
    Message.warning(`执行结果：${execution.status}`)
  }
}

async function approveOnly(record: ApprovalItem) {
  actingId.value = record.id
  try {
    pendingTokens[record.id] = await grantApproval(record)
    comments[record.id] = ''
    Message.success('已批准；执行令牌只保存在当前页面内存，刷新后需要重新审批')
    await store.fetchApprovals()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

async function approveAndExecute(record: ApprovalItem) {
  actingId.value = record.id
  try {
    const token = pendingTokens[record.id] || (await grantApproval(record))
    delete pendingTokens[record.id]
    comments[record.id] = ''
    await executeAndVerify(record, token)
    await Promise.all([store.fetchApprovals(), store.fetchTasks()])
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

async function executeApproved(record: ApprovalItem) {
  const token = pendingTokens[record.id]
  if (!token) {
    Message.warning('执行令牌不在当前页面内存中，请重新批准')
    return
  }
  actingId.value = record.id
  try {
    delete pendingTokens[record.id]
    await executeAndVerify(record, token)
    await Promise.all([store.fetchApprovals(), store.fetchTasks()])
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

async function reject(record: ApprovalItem) {
  const comment = (comments[record.id] ?? '').trim()
  if (!comment) {
    Message.warning('拒绝审批必须填写意见')
    return
  }
  actingId.value = record.id
  try {
    await rejectProposal(record.proposal_id, approver.value, comment)
    Message.success('已拒绝，任务终止')
    comments[record.id] = ''
    delete pendingTokens[record.id]
    await Promise.all([store.fetchApprovals(), store.fetchTasks()])
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

function openTask(taskId: number) {
  router.push(`/tasks/${taskId}`)
}

onMounted(refresh)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>审批队列</h2>
        <div class="page-sub">R2/R3 处置动作必须经人工批准后才会进入受限执行</div>
      </div>
      <a-space>
        <a-input v-model="approver" size="small" placeholder="审批人" class="mono" style="width: 150px" />
        <a-button :loading="store.approvalsLoading" @click="refresh">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </a-space>
    </div>

    <a-card class="panel" title="待审批记录">
      <a-table
        :data="store.approvals"
        :columns="columns"
        :loading="store.approvalsLoading"
        :pagination="false"
        row-key="id"
        size="medium"
      >
        <template #proposal="{ record }">
          <a-space>
            <span class="mono">{{ record.tool }}</span>
            <a-tag :color="riskMeta(record.risk_level).color" size="small">
              {{ riskMeta(record.risk_level).label }}
            </a-tag>
          </a-space>
          <div class="dim mono" style="font-size: 12px">
            提案 #{{ record.proposal_id }} · 审批 #{{ record.id }}
          </div>
          <FileTargetSummary
            v-if="record.contract?.file_target"
            :target="record.contract.file_target"
          />
          <div v-else-if="record.contract" class="dim mono" style="font-size: 12px">
            动作类型：{{ record.contract.kind }} · 运行身份：{{ record.contract.run_as || '—' }}
          </div>
          <a-link @click="openTask(record.task_id)">查看任务与完整契约</a-link>
        </template>
        <template #task="{ record }">
          <a-link @click="openTask(record.task_id)">查看任务 #{{ record.task_id }}</a-link>
          <div class="clamp-2 dim" style="font-size: 12px; margin-top: 2px">
            {{ record.request_text }}
          </div>
        </template>
        <template #expires="{ record }">
          <span class="mono dim">{{ formatTime(record.expires_at) }}</span>
        </template>
        <template #action="{ record }">
          <a-popover trigger="click" position="left">
            <template #content>
              <div class="decision-pop">
                <a-textarea
                  v-model="comments[record.id]"
                  :rows="2"
                  :max-length="500"
                  allow-clear
                  placeholder="审批意见（拒绝必填）"
                />
                <a-space class="pop-actions" :size="8">
                  <a-button
                    size="small"
                    :loading="actingId === record.id"
                    @click="approveOnly(record)"
                  >
                    仅批准
                  </a-button>
                  <a-button
                    size="small"
                    type="primary"
                    :loading="actingId === record.id"
                    @click="approveAndExecute(record)"
                  >
                    批准并执行
                  </a-button>
                  <a-button
                    v-if="pendingTokens[record.id]"
                    size="small"
                    type="primary"
                    :loading="actingId === record.id"
                    @click="executeApproved(record)"
                  >
                    执行已批准
                  </a-button>
                  <a-button
                    size="small"
                    status="danger"
                    :loading="actingId === record.id"
                    @click="reject(record)"
                  >
                    拒绝
                  </a-button>
                </a-space>
              </div>
            </template>
            <a-button size="small">处理审批</a-button>
          </a-popover>
        </template>
        <template #empty>
          <div class="empty-block">
            <a-empty description="当前没有待审批的处置方案" />
          </div>
        </template>
      </a-table>
    </a-card>
  </div>
</template>
