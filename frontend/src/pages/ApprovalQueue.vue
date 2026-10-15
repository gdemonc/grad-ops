<script setup lang="ts">
import { IconRefresh } from '@arco-design/web-vue/es/icon'
import { Message } from '@arco-design/web-vue'
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { approveApproval, errorMessage, rejectApproval } from '../api'
import { useTasksStore } from '../stores/tasks'
import { riskMeta } from '../types'
import type { ApprovalQueueItem } from '../types'

const store = useTasksStore()
const router = useRouter()

const comments = reactive<Record<number, string>>({})
const actingId = ref(0)

const columns = [
  { title: 'ID', dataIndex: 'id', width: 72 },
  { title: '处置提案', slotName: 'proposal', width: 260 },
  { title: '任务请求', slotName: 'task' },
  { title: '决策', slotName: 'action', width: 150 },
]

async function refresh() {
  await store.fetchApprovals()
}

async function decide(record: ApprovalQueueItem, action: 'approve' | 'reject') {
  const comment = (comments[record.id] ?? '').trim()
  if (action === 'reject' && !comment) {
    Message.warning('拒绝审批必须填写意见')
    return
  }
  actingId.value = record.id
  try {
    if (action === 'approve') {
      await approveApproval(record.id, comment)
      Message.success('已批准，任务继续执行')
    } else {
      await rejectApproval(record.id, comment)
      Message.success('已拒绝，任务终止')
    }
    comments[record.id] = ''
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
            <span class="mono">{{ record.tool_name }}</span>
            <a-tag :color="riskMeta(record.risk_level).color" size="small">
              {{ riskMeta(record.risk_level).label }}
            </a-tag>
          </a-space>
          <div class="dim mono" style="font-size: 12px">
            提案 #{{ record.proposal_id }} · 审批 #{{ record.id }}
          </div>
        </template>
        <template #task="{ record }">
          <a-link @click="openTask(record.task_id)">
            查看任务 #{{ record.task_id }}
          </a-link>
          <div class="clamp-2 dim" style="font-size: 12px; margin-top: 2px">
            {{ record.request_text }}
          </div>
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
                    type="primary"
                    :loading="actingId === record.id"
                    @click="decide(record, 'approve')"
                  >
                    批准
                  </a-button>
                  <a-button
                    size="small"
                    status="danger"
                    :loading="actingId === record.id"
                    @click="decide(record, 'reject')"
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
