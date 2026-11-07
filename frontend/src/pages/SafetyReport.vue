<script setup lang="ts">
import {
  IconCheckCircleFill,
  IconCloseCircleFill,
  IconExclamationCircleFill,
  IconRefresh,
} from '@arco-design/web-vue/es/icon'
import { onMounted } from 'vue'
import { useTasksStore } from '../stores/tasks'
import { guardStatusMeta } from '../types'
import type { SafetyGuard, SafetyReport } from '../types'

const store = useTasksStore()

function bannerType(status: SafetyReport['overall_status']): 'success' | 'warning' | 'error' {
  if (status === 'ok') return 'success'
  if (status === 'warn') return 'warning'
  return 'error'
}

function bannerTitle(status: SafetyReport['overall_status']): string {
  if (status === 'ok') return '安全状态正常'
  if (status === 'warn') return '部分边界尚未实现'
  return '副作用执行已被阻断'
}

function guardIcon(status: SafetyGuard['status']) {
  if (status === 'ok') return IconCheckCircleFill
  if (status === 'warn') return IconExclamationCircleFill
  return IconCloseCircleFill
}

function ttlText(ms: number): string {
  if (ms % 60000 === 0) return `${ms / 60000} 分钟`
  return `${Math.round(ms / 1000)} 秒`
}

onMounted(() => {
  store.fetchSafetyReport()
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h2>安全报告</h2>
        <div class="page-sub">策略、契约、审批与运行边界的真实状态，未实现的能力会直接标注</div>
      </div>
      <a-button :loading="store.safetyLoading" @click="store.fetchSafetyReport()">
        <template #icon><icon-refresh /></template>
        刷新
      </a-button>
    </div>

    <a-spin :loading="store.safetyLoading && !store.safetyReport" class="block-spin">
      <template v-if="store.safetyReport">
        <a-alert
          class="panel"
          banner
          :type="bannerType(store.safetyReport.overall_status)"
          :title="bannerTitle(store.safetyReport.overall_status)"
        >
          {{ store.safetyReport.summary }}
        </a-alert>

        <div class="grid-2">
          <a-card class="panel">
            <template #title><div class="panel-title">运行时配置</div></template>
            <div class="kv">
              <span class="k">Shell</span>
              <span class="v mono">{{ store.safetyReport.runtime.bash }}</span>
            </div>
            <div class="kv">
              <span class="k">工作目录</span>
              <span class="v mono">{{ store.safetyReport.runtime.work_dir }}</span>
            </div>
            <div class="kv">
              <span class="k">契约工作目录</span>
              <span class="v mono">{{ store.safetyReport.runtime.contract_work_dir }}</span>
            </div>
            <div class="kv">
              <span class="k">策略版本</span>
              <span class="v mono">{{ store.safetyReport.runtime.policy_version }}</span>
            </div>
            <div class="kv">
              <span class="k">审批有效期</span>
              <span class="v mono">{{ ttlText(store.safetyReport.runtime.approval_ttl_ms) }}</span>
            </div>
            <div class="kv">
              <span class="k">后台 Worker</span>
              <span class="v">
                <a-tag :color="store.safetyReport.runtime.worker_enabled ? 'green' : 'orange'" size="small">
                  {{ store.safetyReport.runtime.worker_enabled ? '已启用' : '已停用' }}
                </a-tag>
              </span>
            </div>
            <div class="kv">
              <span class="k">身份强制校验</span>
              <span class="v">
                <a-tag
                  :color="store.safetyReport.runtime.require_run_as_match ? 'green' : 'orange'"
                  size="small"
                >
                  {{ store.safetyReport.runtime.require_run_as_match ? '已开启' : '未开启' }}
                </a-tag>
              </span>
            </div>
            <div class="kv">
              <span class="k">探针会话</span>
              <span class="v">
                <a-tag
                  :color="store.safetyReport.runtime.persistent_probes ? 'arcoblue' : 'gray'"
                  size="small"
                >
                  {{ store.safetyReport.runtime.persistent_probes ? '常驻（共享状态）' : '每次独立进程' }}
                </a-tag>
              </span>
            </div>
          </a-card>

          <a-card class="panel">
            <template #title><div class="panel-title">允许的路径根</div></template>
            <div class="tag-row">
              <a-tag
                v-for="root in store.safetyReport.runtime.allowed_roots"
                :key="root"
                color="green"
                class="mono"
                size="small"
              >
                {{ root }}
              </a-tag>
            </div>
            <div class="kv" style="margin-top: 12px">
              <span class="k">说明</span>
            </div>
            <p class="dim">
              动作声明的每个路径都必须落在这些根之内；契约里的运行身份必须是显式声明的非 root
              身份。策略层目前是确定性的模式匹配，不是 AST 分析。
            </p>
          </a-card>
        </div>

        <a-card class="panel" style="margin-top: 16px">
          <template #title><div class="panel-title">防护闸门</div></template>
          <div v-for="guard in store.safetyReport.guards" :key="guard.key" class="guard-item">
            <component :is="guardIcon(guard.status)" :class="['guard-icon', guard.status]" />
            <div>
              <div class="guard-name">
                {{ guard.name }}
                <a-tag :color="guardStatusMeta(guard.status).color" size="small">
                  {{ guardStatusMeta(guard.status).label }}
                </a-tag>
              </div>
              <div class="guard-detail dim">{{ guard.detail }}</div>
            </div>
          </div>
        </a-card>
      </template>

      <a-card v-else-if="!store.safetyLoading" class="panel">
        <div class="empty-block">
          <a-empty description="暂无安全报告" />
        </div>
      </a-card>
    </a-spin>
  </div>
</template>
