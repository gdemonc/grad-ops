<script setup lang="ts">
import { computed } from 'vue'
import { formatTime, prettyJson, stageColor } from '../types'
import type { TimelineEvent } from '../types'

const props = defineProps<{
  event: TimelineEvent
}>()

const payloadText = computed(() => prettyJson(props.event.payload_json))
const hasPayload = computed(
  () => payloadText.value.length > 0 && payloadText.value !== '{}',
)
</script>

<template>
  <a-timeline-item :dot-color="stageColor(event.stage)">
    <div class="tl-head">
      <span class="mono dim">#{{ event.seq }}</span>
      <a-tag :color="stageColor(event.stage)" size="small">{{ event.stage }}</a-tag>
      <span class="mono tl-time">{{ event.event_type }}</span>
      <span class="tl-time">{{ formatTime(event.created_at) }}</span>
    </div>
    <div class="tl-msg">{{ event.message }}</div>
    <a-collapse v-if="hasPayload" class="tl-payload">
      <a-collapse-item name="payload" header="载荷 payload">
        <pre class="json">{{ payloadText }}</pre>
      </a-collapse-item>
    </a-collapse>
  </a-timeline-item>
</template>
