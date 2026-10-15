<script setup lang="ts">
import {
  IconCheckCircleFill,
  IconCloseCircleFill,
  IconLeft,
} from '@arco-design/web-vue/es/icon'
import { Message } from '@arco-design/web-vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { approveApproval, errorMessage, rejectApproval } from '../api'
import EvidenceCard from '../components/EvidenceCard.vue'
import ProposalCard from '../components/ProposalCard.vue'
import TimelineItem from '../components/TimelineItem.vue'
import { useTasksStore } from '../stores/tasks'
import {
  approvalStatusMeta,
  formatTime,
  isTerminalStatus,
  taskStatusMeta,
} from '../types'

const route = useRoute()
const router = useRouter()
const store = useTasksStore()

const commentMap = ref<Record<number, string>>({})
const actingId = ref(0)

const taskId = computed(() => Number(route.params.id))

let timer: number | undefined

async function load() {
  const id = taskId.value
  if (!Number.isFinite(id)) return
  await Promise.all([store.fetchTask(id), store.fetchTimeline(id)])
  syncPolling()
}

function syncPolling() {
  const status = store.current?.status
  const active = status !== undefined && !isTerminalStatus(status)
  if (active && timer === undefined) {
    timer = window.setInterval(() => {
      store.fetchTask(taskId.value).then(() => {
        const current = store.current
        if (current && !isTerminalStatus(current.status)) return
        if (timer !== undefined) {
          window.clearInterval(timer)
          timer = undefined
        }
        store.fetchTimeline(taskId.value)
      })
    }, 2000)
  } else if (!active && timer !== undefined) {
    window.clearInterval(timer)
    timer = undefined
  }
}

async function decide(approvalId: number, action: 'approve' | 'reject') {
  const comment = (commentMap.value[approvalId] ?? '').trim()
  if (action === 'reject' && !comment) {
    Message.warning('拒绝审批必须填写意见')
    return
  }
  actingId.value = approvalId
  try {
    if (action === 'approve') {
      await approveApproval(approvalId, comment)
      Message.success('已批准，任务继续执行')
    } else {
      await rejectApproval(approvalId, comment)
      Message.success('已拒绝，任务终止')
    }
    commentMap.value[approvalId] = ''
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

onMounted(load)

watch(
  () => route.params.id,
  () => {
    commentMap.value = {}
    load()
  },
)

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <a-space>
          <a-button size="small" type="text" @click="router.push('/')">
            <template #icon><icon-left /></template>
            返回列表
          </a-button>
          <h2 v-if="store.current">任务 #{{ store.current.id }}</h2>
        </a-space>
        <div v-if="store.current" class="page-sub mono">{{ store.current.trace_id }}</div>
      </div>
      <a-space v-if="store.current">
        <a-tag :color="taskStatusMeta(store.current.status).color">
          {{ taskStatusMeta(store.current.status).label }}
        </a-tag>
        <span class="mono dim">{{ formatTime(store.current.updated_at) }}</span>
      </a-space>
    </div>

    <a-spin :loading="store.currentLoading && !store.current" class="block-spin">
      <template v-if="store.current">
        <div class="detail-header">
          <div class="row-between">
            <span class="panel-title">原始请求</span>
            <a-tag color="gray" size="small">来源 {{ store.current.source }}</a-tag>
          </div>
          <p class="detail-request">{{ store.current.request_text }}</p>
          <p v-if="store.current.summary" class="detail-request dim">
            处置摘要：{{ store.current.summary }}
          </p>
        </div>

        <div class="grid-2">
          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                证据列表
                <a-tag color="cyan" size="small">{{ store.current.evidence.length }}</a-tag>
              </div>
            </template>
            <div v-if="store.current.evidence.length" class="stack">
              <EvidenceCard
                v-for="item in store.current.evidence"
                :key="item.id"
                :evidence="item"
              />
            </div>
            <div v-else class="empty-block">
              <a-empty description="尚未采集证据" />
            </div>
          </a-card>

          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                处置方案与契约摘要
                <a-tag color="blue" size="small">{{ store.current.proposals.length }}</a-tag>
              </div>
            </template>
            <div v-if="store.current.proposals.length" class="stack">
              <ProposalCard
                v-for="item in store.current.proposals"
                :key="item.id"
                :proposal="item"
              />
            </div>
            <div v-else class="empty-block">
              <a-empty description="尚未生成处置方案" />
            </div>
          </a-card>

          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                审批
                <a-tag color="orange" size="small">{{ store.current.approvals.length }}</a-tag>
              </div>
            </template>
            <div v-if="store.current.approvals.length" class="stack">
              <div
                v-for="approval in store.current.approvals"
                :key="approval.id"
                class="evidence-card approval-block"
              >
                <div class="row-between">
                  <a-space>
                    <span class="mono">审批 #{{ approval.id }}</span>
                    <a-tag :color="approvalStatusMeta(approval.status).color" size="small">
                      {{ approvalStatusMeta(approval.status).label }}
                    </a-tag>
                  </a-space>
                  <span class="dim">{{ approval.decided_by || '—' }}</span>
                </div>
                <div class="kv">
                  <span class="k">审批意见</span>
                  <span class="v">{{ approval.comment || '—' }}</span>
                </div>
                <template v-if="approval.status === 'PENDING'">
                  <a-textarea
                    v-model="commentMap[approval.id]"
                    :rows="2"
                    :max-length="500"
                    allow-clear
                    placeholder="审批意见（拒绝必填，批准可留空）"
                  />
                  <a-space class="actions" :size="12">
                    <a-button
                      type="primary"
                      size="small"
                      :loading="actingId === approval.id"
                      @click="decide(approval.id, 'approve')"
                    >
                      批准执行
                    </a-button>
                    <a-button
                      status="danger"
                      size="small"
                      :loading="actingId === approval.id"
                      @click="decide(approval.id, 'reject')"
                    >
                      拒绝
                    </a-button>
                  </a-space>
                </template>
              </div>
            </div>
            <div v-else class="empty-block">
              <a-empty description="本任务无需审批" />
            </div>
          </a-card>

          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                恢复验证
                <a-tag color="green" size="small">{{ store.current.verifications.length }}</a-tag>
              </div>
            </template>
            <div v-if="store.current.verifications.length" class="stack">
              <div
                v-for="record in store.current.verifications"
                :key="record.id"
                class="evidence-card"
              >
                <div class="row-between">
                  <a-tag :color="record.passed ? 'green' : 'red'" size="small">
                    {{ record.passed ? '验证通过' : '验证未通过' }}
                  </a-tag>
                  <span class="mono dim">{{ formatTime(record.verified_at) }}</span>
                </div>
                <p class="evidence-summary">{{ record.note || '—' }}</p>
                <ul class="check-list">
                  <li
                    v-for="check in record.checks_json"
                    :key="check.name"
                    class="check-item"
                  >
                    <icon-check-circle-fill v-if="check.ok" class="ok-icon" />
                    <icon-close-circle-fill v-else class="bad-icon" />
                    <span class="check-name">{{ check.name }}</span>
                    <span class="dim">{{ check.detail }}</span>
                  </li>
                </ul>
              </div>
            </div>
            <div v-else class="empty-block">
              <a-empty description="尚未执行独立恢复验证" />
            </div>
          </a-card>
        </div>

        <a-card class="panel" style="margin-top: 16px">
          <template #title>
            <div class="panel-title">
              审计时间线
              <a-tag color="gray" size="small">{{ store.timeline.length }}</a-tag>
            </div>
          </template>
          <a-spin :loading="store.timelineLoading && store.timeline.length === 0">
            <a-timeline v-if="store.timeline.length" class="timeline">
              <TimelineItem
                v-for="event in store.timeline"
                :key="event.seq"
                :event="event"
              />
            </a-timeline>
            <div v-else class="empty-block">
              <a-empty description="暂无审计事件" />
            </div>
          </a-spin>
        </a-card>
      </template>

      <div v-else-if="!store.currentLoading" class="empty-block">
        <a-empty description="任务不存在或已删除">
          <a-button type="primary" @click="router.push('/')">返回任务列表</a-button>
        </a-empty>
      </div>
    </a-spin>
  </div>
</template>
