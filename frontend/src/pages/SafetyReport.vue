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
import type { SafetyReport, SafetyGuard } from '../types'

const store = useTasksStore()

function bannerType(status: SafetyReport['overall_status']): 'success' | 'warning' | 'error' {
  if (status === 'ok') return 'success'
  if (status === 'warn') return 'warning'
  return 'error'
}

function bannerTitle(status: SafetyReport['overall_status']): string {
  if (status === 'ok') return '安全状态正常'
  if (status === 'warn') return '安全状态告警'
  return '副作用执行已被阻断'
}

function guardIcon(status: SafetyGuard['status']) {
  if (status === 'ok') return IconCheckCircleFill
  if (status === 'warn') return IconExclamationCircleFill
  return IconCloseCircleFill
}

function boolText(value: boolean): string {
  return value ? '是' : '否'
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
        <div class="page-sub">运行身份、工具白名单、路径边界与审批闸门的实时状态</div>
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
            <template #title><div class="panel-title">执行身份</div></template>
            <div class="kv">
              <span class="k">运行用户</span>
              <span class="v mono">{{ store.safetyReport.executor.runtime_user }}</span>
            </div>
            <div class="kv">
              <span class="k">运行 uid</span>
              <span class="v mono">{{ store.safetyReport.executor.runtime_uid }}</span>
            </div>
            <div class="kv">
              <span class="k">目标用户</span>
              <span class="v mono">{{ store.safetyReport.executor.target_user }}</span>
            </div>
            <div class="kv">
              <span class="k">允许 root 执行</span>
              <span class="v">
                <a-tag
                  :color="store.safetyReport.executor.allow_root_executor ? 'orange' : 'green'"
                  size="small"
                >
                  {{ boolText(store.safetyReport.executor.allow_root_executor) }}
                </a-tag>
              </span>
            </div>
            <div class="kv">
              <span class="k">副作用执行</span>
              <span class="v">
                <a-tag
                  :color="store.safetyReport.executor.action_execution_enabled ? 'green' : 'red'"
                  size="small"
                >
                  {{ store.safetyReport.executor.action_execution_enabled ? '已开放' : '已锁定' }}
                </a-tag>
              </span>
            </div>
          </a-card>

          <a-card class="panel">
            <template #title><div class="panel-title">路径与工具边界</div></template>
            <div class="kv">
              <span class="k">副作用工具</span>
            </div>
            <div class="tag-row">
              <a-tag
                v-for="tool in store.safetyReport.boundary.allowed_tools"
                :key="tool"
                color="cyan"
                class="mono"
                size="small"
              >
                {{ tool }}
              </a-tag>
            </div>
            <div class="kv" style="margin-top: 10px">
              <span class="k">允许路径前缀</span>
            </div>
            <div class="tag-row">
              <a-tag
                v-for="prefix in store.safetyReport.boundary.allowed_path_prefixes"
                :key="prefix"
                color="green"
                class="mono"
                size="small"
              >
                {{ prefix }}
              </a-tag>
            </div>
            <div class="kv" style="margin-top: 10px">
              <span class="k">受保护路径</span>
            </div>
            <div class="tag-row">
              <a-tag
                v-for="prefix in store.safetyReport.boundary.protected_path_prefixes"
                :key="prefix"
                color="red"
                class="mono"
                size="small"
              >
                {{ prefix }}
              </a-tag>
            </div>
          </a-card>
        </div>

        <a-card class="panel" style="margin-top: 16px">
          <template #title><div class="panel-title">防护闸门</div></template>
          <div
            v-for="guard in store.safetyReport.guards"
            :key="guard.key"
            class="guard-item"
          >
            <component
              :is="guardIcon(guard.status)"
              :class="['guard-icon', guard.status]"
            />
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
