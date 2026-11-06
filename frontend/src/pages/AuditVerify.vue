<script setup lang="ts">
import { Message } from '@arco-design/web-vue'
import { onMounted, ref } from 'vue'
import { errorMessage, verifyAudit } from '../api'
import type { AuditVerifyResult } from '../types'

const verifying = ref(false)
const result = ref<AuditVerifyResult | null>(null)

async function runVerify() {
  verifying.value = true
  try {
    result.value = await verifyAudit()
    if (result.value.ok) {
      Message.success('哈希链校验通过')
    } else {
      Message.error('哈希链校验失败，存在被篡改或断链的记录')
    }
  } catch (err) {
    Message.error(errorMessage(err))
  } finally {
    verifying.value = false
  }
}

onMounted(runVerify)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>审计校验</h2>
        <div class="page-sub">
          逐条重算整条审计链的 SHA-256：任何修改、删除或重排都会让后续链接失效
        </div>
      </div>
      <a-button type="primary" :loading="verifying" @click="runVerify">重新校验</a-button>
    </div>

    <a-card v-if="result" class="panel" title="校验结果">
      <div class="verify-head">
        <a-tag :color="result.ok ? 'green' : 'red'" size="large">
          {{ result.ok ? '哈希链完整' : '哈希链校验失败' }}
        </a-tag>
      </div>
      <div class="kv">
        <span class="k">事件数</span>
        <span class="v mono">{{ result.events }}</span>
      </div>
      <div v-if="!result.ok" class="kv">
        <span class="k">断链位置</span>
        <span class="v mono">{{ result.broken_seq ?? '—' }}</span>
      </div>
      <div v-if="!result.ok" class="kv">
        <span class="k">原因</span>
        <span class="v">{{ result.reason || '—' }}</span>
      </div>
      <p class="dim">
        注意：链的信任边界是本机数据库。拥有数据库写权限的人可以重写整条链，链校验只能发现
        与存储值不一致的篡改，不能证明历史从未被替换。
      </p>
    </a-card>

    <a-card v-else class="panel">
      <div class="empty-block">
        <a-empty description="点击“重新校验”开始校验审计哈希链" />
      </div>
    </a-card>
  </div>
</template>
