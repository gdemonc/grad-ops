<script setup lang="ts">
import { formatTime } from '../types'
import type { EvidenceItem } from '../types'

defineProps<{
  evidence: EvidenceItem
}>()
</script>

<template>
  <a-card class="evidence-card" size="small">
    <template #title>
      <div class="panel-title">
        <a-tag color="cyan" size="small">{{ evidence.kind }}</a-tag>
        <span class="mono dim">证据 #{{ evidence.id }}</span>
      </div>
    </template>
    <p class="evidence-summary">{{ evidence.summary }}</p>
    <div class="kv">
      <span class="k">内容哈希</span>
      <span class="v hash">{{ evidence.content_hash }}</span>
    </div>
    <div class="kv">
      <span class="k">采集时间</span>
      <span class="v mono dim">{{ formatTime(evidence.created_at) }}</span>
    </div>
  </a-card>
</template>
