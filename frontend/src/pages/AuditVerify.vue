<script setup lang="ts">
import { Message } from '@arco-design/web-vue'
import { ref } from 'vue'
import { errorMessage, verifyAudit } from '../api'
import type { AuditVerifyResult } from '../types'

const traceId = ref('')
const verifying = ref(false)
const showEntries = ref(false)
const result = ref<AuditVerifyResult | null>(null)

const entryColumns = [
  { title: 'seq', dataIndex: 'seq', width: 80 },
  { title: 'prev_hash', slotName: 'prev', width: 120 },
  { title: 'payload_hash', slotName: 'payload', width: 130 },
  { title: 'event_hash', slotName: 'event', width: 120 },
  { title: '存储事件哈希', slotName: 'hash' },
]

async function runVerify(full: boolean) {
  const id = traceId.value.trim()
  if (!id) {
    Message.warning('请输入 trace_id')
    return
  }
  verifying.value = true
  try {
    result.value = await verifyAudit(id, full)
    if (result.value.valid) {
      Message.success('哈希链校验通过')
    } else {
      Message.error('哈希链校验失败，存在被篡改的记录')
    }
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    verifying.value = false
  }
}

async function toggleEntries() {
  showEntries.value = !showEntries.value
  if (result.value) {
    await runVerify(showEntries.value)
  }
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>审计校验</h2>
        <div class="page-sub">对指定 trace 的审计事件逐条重算 SHA-256，检测篡改与断链</div>
      </div>
    </div>

    <a-card class="panel" title="校验入口">
      <a-space>
        <a-input
          v-model="traceId"
          placeholder="输入 trace_id，例如 5f0c9e2a-..."
          allow-clear
          style="width: 420px"
          class="mono"
          @press-enter="runVerify(false)"
        />
        <a-button type="primary" :loading="verifying" @click="runVerify(false)">
          校验哈希链
        </a-button>
      </a-space>
    </a-card>

    <a-card v-if="result" class="panel" title="校验结果">
      <div class="verify-head">
        <a-tag :color="result.valid ? 'green' : 'red'" size="large">
          {{ result.valid ? '哈希链完整' : '哈希链校验失败' }}
        </a-tag>
        <a-button size="small" @click="toggleEntries">
          {{ showEntries ? '收起明细' : '查看明细' }}
        </a-button>
      </div>
      <div class="kv">
        <span class="k">trace_id</span>
        <span class="v mono">{{ result.trace_id }}</span>
      </div>
      <div class="kv">
        <span class="k">事件数</span>
        <span class="v mono">{{ result.entry_count }}</span>
      </div>
      <div class="kv">
        <span class="k">链头哈希</span>
        <span class="v hash">{{ result.head_hash }}</span>
      </div>

      <template v-if="showEntries && result.entries">
        <a-table
          :data="result.entries"
          :columns="entryColumns"
          :pagination="false"
          row-key="seq"
          size="small"
          style="margin-top: 16px"
        >
          <template #prev="{ record }">
            <a-tag :color="record.prev_ok ? 'green' : 'red'" size="small">
              {{ record.prev_ok ? '通过' : '异常' }}
            </a-tag>
          </template>
          <template #payload="{ record }">
            <a-tag :color="record.payload_ok ? 'green' : 'red'" size="small">
              {{ record.payload_ok ? '通过' : '异常' }}
            </a-tag>
          </template>
          <template #event="{ record }">
            <a-tag :color="record.event_ok ? 'green' : 'red'" size="small">
              {{ record.event_ok ? '通过' : '异常' }}
            </a-tag>
          </template>
          <template #hash="{ record }">
            <span class="hash">{{ record.stored_event_hash }}</span>
          </template>
        </a-table>
      </template>
    </a-card>

    <a-card v-else class="panel">
      <div class="empty-block">
        <a-empty description="输入任务 trace_id，开始校验审计哈希链" />
      </div>
    </a-card>
  </div>
</template>
