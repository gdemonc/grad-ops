<script setup lang="ts">
import { computed } from 'vue'
import { eventTypeColor, formatTime, prettyJson } from '../types'
import type { TimelineEvent } from '../types'

const props = defineProps<{
  event: TimelineEvent
}>()

const payloadText = computed(() => prettyJson(props.event.payload))
const hasPayload = computed(
  () => payloadText.value.length > 0 && payloadText.value !== '{}' && payloadText.value !== 'null',
)
</script>

<template>
  <a-timeline-item :dot-color="eventTypeColor(event.type)">
    <div class="tl-head">
      <span class="mono dim">#{{ event.seq }}</span>
      <a-tag :color="eventTypeColor(event.type)" size="small">{{ event.type }}</a-tag>
      <span class="mono tl-time">{{ event.actor }}</span>
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
