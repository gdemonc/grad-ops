<script setup lang="ts">
import { Message } from '@arco-design/web-vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  approveProposal,
  errorMessage,
  executeProposal,
  recoverTask,
  rejectProposal,
  verifyTask,
} from '../api'
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
import type { ProposalItem } from '../types'

const route = useRoute()
const router = useRouter()
const store = useTasksStore()

const approver = ref('operator')
const comment = ref('')
const actingId = ref(0)

const taskId = computed(() => Number(route.params.id))

const current = computed(() => store.current)
const pendingProposals = computed(
  () => current.value?.proposals.filter((item) => item.status === 'PENDING_APPROVAL') ?? [],
)
const liveApprovalProposalIds = computed(
  () =>
    new Set(
      (current.value?.approvals ?? [])
        .filter((item) => item.status === 'PENDING')
        .map((item) => item.proposal_id),
    ),
)

let timer: number | undefined

async function load() {
  const id = taskId.value
  if (!Number.isFinite(id)) return
  await store.fetchTask(id)
  syncPolling()
}

function syncPolling() {
  const status = current.value?.task.status
  const active = status !== undefined && !isTerminalStatus(status)
  if (active && timer === undefined) {
    timer = window.setInterval(() => {
      store.fetchTask(taskId.value).then(() => {
        const now = current.value?.task.status
        if (now && !isTerminalStatus(now)) return
        if (timer !== undefined) {
          window.clearInterval(timer)
          timer = undefined
        }
      })
    }, 2000)
  } else if (!active && timer !== undefined) {
    window.clearInterval(timer)
    timer = undefined
  }
}

/**
 * Approval and execution are two calls on purpose: the approval returns a
 * one-time token that is never stored, so the console carries it from the
 * decision to the execution without ever putting it on disk.
 */
async function approveAndExecute(proposal: ProposalItem) {
  actingId.value = proposal.id
  try {
    const grant = await approveProposal(proposal.id, approver.value, comment.value)
    if (!grant.token) {
      throw new Error('审批没有返回 token')
    }
    const execution = await executeProposal(proposal.id, grant.token, approver.value)
    if (execution.status === 'SUCCEEDED') {
      const verification = await verifyTask(taskId.value)
      if (verification.passed) {
        Message.success('执行完成，独立验证通过，任务已封存')
      } else {
        Message.warning(`执行完成，但验证未通过：${verification.failures.join('；')}`)
      }
    } else {
      Message.warning(`执行结果：${execution.status}`)
    }
    comment.value = ''
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

async function reject(proposal: ProposalItem) {
  if (!comment.value.trim()) {
    Message.warning('拒绝必须填写理由')
    return
  }
  actingId.value = proposal.id
  try {
    await rejectProposal(proposal.id, approver.value, comment.value)
    Message.success('已拒绝，任务终止')
    comment.value = ''
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

async function runVerifyOnly() {
  actingId.value = -1
  try {
    const result = await verifyTask(taskId.value)
    Message[result.passed ? 'success' : 'warning'](
      result.passed ? '验证通过，任务已封存' : `验证未通过：${result.failures.join('；')}`,
    )
    await load()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    actingId.value = 0
  }
}

async function runRecover() {
  actingId.value = -2
  try {
    const result = await recoverTask(taskId.value)
    Message[result.status === 'RECOVERED' ? 'success' : 'error'](
      result.status === 'RECOVERED' ? '补偿动作已执行并生效' : '补偿动作未能生效',
    )
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
    comment.value = ''
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
          <a-button size="small" type="text" @click="router.push('/')">返回列表</a-button>
          <h2 v-if="current">任务 #{{ current.task.id }}</h2>
        </a-space>
        <div v-if="current" class="page-sub mono">来源 {{ current.task.source }}</div>
      </div>
      <a-space v-if="current">
        <a-tag :color="taskStatusMeta(current.task.status).color">
          {{ taskStatusMeta(current.task.status).label }}
        </a-tag>
        <span class="mono dim">{{ formatTime(current.task.updated_at) }}</span>
      </a-space>
    </div>

    <a-spin :loading="store.currentLoading && !current" class="block-spin">
      <template v-if="current">
        <div class="detail-header">
          <div class="row-between">
            <span class="panel-title">原始请求</span>
            <a-tag
              v-if="current.task.failure_stage"
              color="red"
              size="small"
            >
              失败阶段 {{ current.task.failure_stage }}
            </a-tag>
          </div>
          <p class="detail-request">{{ current.task.request_text }}</p>
          <p v-if="current.task.summary" class="detail-request dim">
            处置摘要：{{ current.task.summary }}
          </p>
          <a-space class="actions" :size="12">
            <a-button
              v-if="current.task.status === 'VERIFYING'"
              size="small"
              :loading="actingId === -1"
              @click="runVerifyOnly"
            >
              执行独立验证
            </a-button>
            <a-button
              v-if="current.task.status === 'ROLLING_BACK'"
              size="small"
              status="danger"
              :loading="actingId === -2"
              @click="runRecover"
            >
              执行契约内补偿动作
            </a-button>
          </a-space>
        </div>

        <div v-if="pendingProposals.length" class="panel">
          <a-card title="待审批动作">
            <div class="stack">
              <div v-for="proposal in pendingProposals" :key="proposal.id" class="stack">
                <ProposalCard :proposal="proposal" />
                <a-textarea
                  v-model="comment"
                  :rows="2"
                  :max-length="500"
                  allow-clear
                  placeholder="审批意见（拒绝必填）"
                />
                <a-space class="actions" :size="12">
                  <a-button
                    type="primary"
                    size="small"
                    :loading="actingId === proposal.id"
                    @click="approveAndExecute(proposal)"
                  >
                    批准并执行
                  </a-button>
                  <a-button
                    status="danger"
                    size="small"
                    :loading="actingId === proposal.id"
                    @click="reject(proposal)"
                  >
                    拒绝
                  </a-button>
                  <a-input
                    v-model="approver"
                    size="small"
                    placeholder="审批人"
                    style="width: 140px"
                    class="mono"
                  />
                </a-space>
                <div v-if="liveApprovalProposalIds.has(proposal.id)" class="dim">
                  该提案已有有效审批，但 token 只在审批响应中出现一次；请等待其过期后重新批准。
                </div>
              </div>
            </div>
          </a-card>
        </div>

        <div class="grid-2">
          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                证据
                <a-tag color="cyan" size="small">{{ current.evidence.length }}</a-tag>
              </div>
            </template>
            <div v-if="current.evidence.length" class="stack">
              <EvidenceCard v-for="item in current.evidence" :key="item.id" :evidence="item" />
            </div>
            <div v-else class="empty-block">
              <a-empty description="尚未采集证据" />
            </div>
          </a-card>

          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                提案与契约
                <a-tag color="blue" size="small">{{ current.proposals.length }}</a-tag>
              </div>
            </template>
            <div v-if="current.proposals.length" class="stack">
              <ProposalCard v-for="item in current.proposals" :key="item.id" :proposal="item" />
            </div>
            <div v-else class="empty-block">
              <a-empty description="尚未生成处置方案" />
            </div>
          </a-card>

          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                审批记录
                <a-tag color="orange" size="small">{{ current.approvals.length }}</a-tag>
              </div>
            </template>
            <div v-if="current.approvals.length" class="stack">
              <div
                v-for="approval in current.approvals"
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
                  <span class="dim">{{ approval.approver || '—' }}</span>
                </div>
                <div class="kv">
                  <span class="k">决策</span>
                  <span class="v mono">{{ approval.decision }}</span>
                </div>
                <div class="kv">
                  <span class="k">意见</span>
                  <span class="v">{{ approval.comment || '—' }}</span>
                </div>
                <div class="kv">
                  <span class="k">有效期至</span>
                  <span class="v mono dim">{{ formatTime(approval.expires_at) }}</span>
                </div>
              </div>
            </div>
            <div v-else class="empty-block">
              <a-empty description="本任务没有审批记录" />
            </div>
          </a-card>

          <a-card class="panel">
            <template #title>
              <div class="panel-title">
                执行与验证
                <a-tag color="green" size="small">
                  {{ current.executions.length }} / {{ current.verifications.length }}
                </a-tag>
              </div>
            </template>
            <div v-if="current.executions.length || current.verifications.length" class="stack">
              <div v-for="record in current.executions" :key="`e-${record.id}`" class="evidence-card">
                <div class="row-between">
                  <a-space>
                    <a-tag :color="record.outcome === 'SUCCEEDED' ? 'green' : 'red'" size="small">
                      {{ record.outcome }}
                    </a-tag>
                    <span class="mono dim">exit {{ record.exit_code }} · {{ record.duration_ms }}ms</span>
                  </a-space>
                  <span class="mono dim">{{ formatTime(record.started_at) }}</span>
                </div>
                <div class="kv">
                  <span class="k">运行身份</span>
                  <span class="v mono">{{ record.run_as || '—' }}</span>
                </div>
              </div>
              <div
                v-for="record in current.verifications"
                :key="`v-${record.id}`"
                class="evidence-card"
              >
                <div class="row-between">
                  <a-tag :color="record.passed ? 'green' : 'red'" size="small">
                    {{ record.passed ? '验证通过' : '验证未通过' }}
                  </a-tag>
                  <span class="mono dim">{{ formatTime(record.checked_at) }}</span>
                </div>
                <pre class="json">{{ record.postcondition }}</pre>
                <div class="kv">
                  <span class="k">证据</span>
                  <span class="v mono">{{ record.evidence_id || '—' }}</span>
                </div>
              </div>
            </div>
            <div v-else class="empty-block">
              <a-empty description="尚未执行动作" />
            </div>
          </a-card>
        </div>

        <a-card class="panel" style="margin-top: 16px">
          <template #title>
            <div class="panel-title">
              审计时间线
              <a-tag color="gray" size="small">{{ current.timeline.length }}</a-tag>
            </div>
          </template>
          <a-timeline v-if="current.timeline.length" class="timeline">
            <TimelineItem v-for="event in current.timeline" :key="event.seq" :event="event" />
          </a-timeline>
          <div v-else class="empty-block">
            <a-empty description="暂无审计事件" />
          </div>
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
