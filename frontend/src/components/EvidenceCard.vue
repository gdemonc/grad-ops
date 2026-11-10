<script setup lang="ts">
import { formatTime, shortHash } from '../types'
import type { EvidenceItem } from '../types'

defineProps<{
  evidence: EvidenceItem
}>()
</script>

<template>
  <a-card class="evidence-card" size="small">
    <template #title>
      <div class="panel-title">
        <a-tag color="cyan" size="small">{{ evidence.tool }}</a-tag>
        <span class="mono dim">{{ evidence.id }}</span>
        <a-tag v-if="evidence.truncated" color="orange" size="small">输出已截断</a-tag>
      </div>
    </template>
    <div class="kv">
      <span class="k">命令</span>
      <span class="v mono">{{ evidence.command || '—' }}</span>
    </div>
    <pre class="json">{{ evidence.content || '（空输出）' }}</pre>
    <div class="kv">
      <span class="k">内容哈希</span>
      <span class="v hash">{{ shortHash(evidence.content_hash) }}</span>
    </div>
    <div class="kv">
      <span class="k">采集时间</span>
      <span class="v mono dim">{{ formatTime(evidence.observed_at) }}</span>
    </div>
  </a-card>
</template>
