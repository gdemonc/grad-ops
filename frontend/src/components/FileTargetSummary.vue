<script setup lang="ts">
import { computed } from 'vue'
import { fileIdentity, fileTargetPreview } from '../observability'
import type { FileTarget } from '../types'

const props = defineProps<{
  target: FileTarget
}>()

const preview = computed(() => fileTargetPreview(props.target))
const hasData = computed(() => (props.target.data ?? '').length > 0)
</script>

<template>
  <div class="file-target">
    <div class="kv">
      <span class="k">可信根目录</span>
      <span class="v mono">{{ target.root }}</span>
    </div>
    <div class="kv">
      <span class="k">目标文件</span>
      <span class="v mono">{{ target.relative }}</span>
    </div>
    <div class="kv">
      <span class="k">操作</span>
      <span class="v mono">{{ target.operation }}</span>
    </div>
    <div class="kv">
      <span class="k">根目录身份</span>
      <span class="v mono">{{ fileIdentity(target.root_device, target.root_inode) }}</span>
    </div>
    <div class="kv">
      <span class="k">目标身份</span>
      <span class="v mono">{{ fileIdentity(target.device, target.inode) }}</span>
    </div>
    <div class="kv">
      <span class="k">追加内容</span>
      <span class="v">
        <a-tag v-if="!hasData" color="gray" size="small">空内容</a-tag>
        <a-tag v-else-if="preview.truncated" color="orange" size="small">预览（已截断）</a-tag>
        <a-tag v-else color="green" size="small">完整内容</a-tag>
      </span>
    </div>
    <pre v-if="hasData" class="json">{{ preview.text }}</pre>
    <a-collapse v-if="preview.truncated" class="tl-payload">
      <a-collapse-item name="full-append" header="查看完整追加内容">
        <pre class="json">{{ target.data }}</pre>
      </a-collapse-item>
    </a-collapse>
  </div>
</template>
