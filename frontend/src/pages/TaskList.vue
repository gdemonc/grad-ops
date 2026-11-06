<script setup lang="ts">
import { IconRefresh } from '@arco-design/web-vue/es/icon'
import type { TableData } from '@arco-design/web-vue/es/table/interface'
import { Message } from '@arco-design/web-vue'
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { errorMessage } from '../api'
import { useTasksStore } from '../stores/tasks'
import { formatTime, isTerminalStatus, taskStatusMeta } from '../types'

const store = useTasksStore()
const router = useRouter()

const requestText = ref('')
const submitting = ref(false)

const columns = [
  { title: 'ID', dataIndex: 'id', width: 72 },
  { title: '状态', slotName: 'status', width: 130 },
  { title: '请求 / 摘要', slotName: 'summary' },
  { title: '更新时间', slotName: 'updated', width: 180 },
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
    Message.success('任务已入队，后台 Worker 会认领并开始只读调查')
    syncPolling()
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    submitting.value = false
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
        <div class="page-sub">
          提交自然语言运维请求，观察只读取证、策略评估、人工审批、受限执行与独立验证的完整闭环
        </div>
      </div>
      <a-space>
        <a-button @click="refresh">
          <template #icon><icon-refresh /></template>
          刷新
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
        placeholder="例如：检查本机磁盘使用率，如果超过阈值就清理 /var/log/app 下的过期日志"
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
        <template #status="{ record }">
          <a-tag :color="taskStatusMeta(record.status).color" size="small">
            {{ taskStatusMeta(record.status).label }}
          </a-tag>
        </template>
        <template #summary="{ record }">
          <span class="clamp-2">{{ record.summary || record.request_text }}</span>
        </template>
        <template #updated="{ record }">
          <span class="mono dim">{{ formatTime(record.updated_at) }}</span>
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
