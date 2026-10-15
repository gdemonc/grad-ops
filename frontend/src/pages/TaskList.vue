<script setup lang="ts">
import { IconRefresh } from '@arco-design/web-vue/es/icon'
import type { TableData } from '@arco-design/web-vue/es/table/interface'
import { Message } from '@arco-design/web-vue'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { errorMessage } from '../api'
import { useTasksStore } from '../stores/tasks'
import { formatTime, isTerminalStatus, shortTrace, taskStatusMeta } from '../types'

const store = useTasksStore()
const router = useRouter()

const requestText = ref('')
const submitting = ref(false)
const running = ref(false)

const columns = [
  { title: 'ID', dataIndex: 'id', width: 72 },
  { title: 'Trace', slotName: 'trace', width: 130 },
  { title: '状态', slotName: 'status', width: 130 },
  { title: '摘要', slotName: 'summary' },
  { title: '创建时间', slotName: 'created', width: 180 },
]

let timer: number | undefined

function syncPolling() {
  const active = store.tasks.some((task) => !isTerminalStatus(task.status))
  if (active && timer === undefined) {
    timer = window.setInterval(() => {
      store.fetchTasks().then(() => {
        const stillActive = store.tasks.some((task) => !isTerminalStatus(task.status))
        if (!stillActive && timer !== undefined) {
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

async function refresh() {
  await store.fetchTasks()
  syncPolling()
}

async function submitTask() {
  const text = requestText.value.trim()
  if (!text) {
    Message.warning('请输入运维请求内容')
    return
  }
  submitting.value = true
  try {
    await store.submitTask(text)
    requestText.value = ''
    Message.success('任务已提交，队列已推进一次')
    syncPolling()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    submitting.value = false
  }
}

async function runQueue() {
  running.value = true
  try {
    const result = await store.runQueueOnce()
    Message.success(result.worked ? '队列推进完成，已处理一个任务' : '队列暂无可处理任务')
    syncPolling()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    running.value = false
  }
}

function openTask(record: TableData) {
  router.push(`/tasks/${String(record.id)}`)
}

onMounted(async () => {
  await store.fetchTasks()
  syncPolling()
})

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>任务列表</h2>
        <div class="page-sub">提交自然语言运维请求，观察只读取证、策略评估、人工审批与独立验证的完整闭环</div>
      </div>
      <a-space>
        <a-button @click="refresh">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
        <a-button type="primary" :loading="running" @click="runQueue">
          运行队列
        </a-button>
      </a-space>
    </div>

    <a-card class="panel" title="提交新任务">
      <a-textarea
        v-model="requestText"
        :rows="3"
        :max-length="2000"
        allow-clear
        show-word-limit
        placeholder="例如：检查本机 8000 端口的监听进程，并备份 /var/log/app 下的过期日志"
      />
      <div class="actions">
        <a-button
          type="primary"
          :loading="submitting"
          :disabled="requestText.trim().length === 0"
          @click="submitTask"
        >
          提交任务
        </a-button>
      </div>
    </a-card>

    <a-card class="panel" title="任务列表">
      <a-table
        :data="store.tasks"
        :columns="columns"
        :loading="store.tasksLoading"
        :pagination="false"
        row-key="id"
        size="medium"
        @row-click="openTask"
      >
        <template #trace="{ record }">
          <span class="mono dim">{{ shortTrace(record.trace_id) }}</span>
        </template>
        <template #status="{ record }">
          <a-tag :color="taskStatusMeta(record.status).color" size="small">
            {{ taskStatusMeta(record.status).label }}
          </a-tag>
        </template>
        <template #summary="{ record }">
          <span class="clamp-2">{{ record.summary || record.request_text }}</span>
        </template>
        <template #created="{ record }">
          <span class="mono dim">{{ formatTime(record.created_at) }}</span>
        </template>
        <template #empty>
          <div class="empty-block">
            <a-empty description="暂无任务，先提交一条运维请求" />
          </div>
        </template>
      </a-table>
    </a-card>
  </div>
</template>
