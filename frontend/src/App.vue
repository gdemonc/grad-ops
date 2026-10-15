<script setup lang="ts">
import { IconDashboard, IconHistory, IconSafe, IconUserAdd } from '@arco-design/web-vue/es/icon'
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

const route = useRoute()
const router = useRouter()
const collapsed = ref(false)

const menuItems = [
  { key: 'tasks', label: '任务列表', path: '/', icon: IconDashboard },
  { key: 'approvals', label: '审批队列', path: '/approvals', icon: IconUserAdd },
  { key: 'audit', label: '审计校验', path: '/audit', icon: IconHistory },
  { key: 'safety', label: '安全报告', path: '/safety', icon: IconSafe },
] as const

const menuKey = computed(() => {
  const path = route.path
  if (path.startsWith('/approvals')) return 'approvals'
  if (path.startsWith('/audit')) return 'audit'
  if (path.startsWith('/safety')) return 'safety'
  return 'tasks'
})

const pageTitle = computed(() => String(route.meta.title ?? '任务列表'))

function handleMenuClick(key: string | number) {
  const item = menuItems.find((entry) => entry.key === String(key))
  if (item) router.push(item.path)
}
</script>

<template>
  <a-layout class="shell">
    <a-layout-sider
      v-model:collapsed="collapsed"
      :width="216"
      :collapsed-width="64"
      collapsible
      class="shell-sider"
    >
      <div class="brand">
        <div class="brand-mark">GO</div>
        <div v-if="!collapsed" class="brand-text">
          <div class="brand-title">grad-ops</div>
          <div class="brand-sub">证据约束运维控制台</div>
        </div>
      </div>
      <a-menu
        mode="vertical"
        :selected-keys="[menuKey]"
        :collapsed="collapsed"
        @menu-item-click="handleMenuClick"
      >
        <a-menu-item v-for="item in menuItems" :key="item.key">
          <template #icon>
            <component :is="item.icon" />
          </template>
          {{ item.label }}
        </a-menu-item>
      </a-menu>
    </a-layout-sider>
    <a-layout class="shell-main">
      <a-layout-header class="shell-header">
        <div class="header-title">{{ pageTitle }}</div>
        <div class="header-meta">证据驱动 · 人工审批 · 哈希审计</div>
      </a-layout-header>
      <a-layout-content class="shell-content">
        <router-view />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>
