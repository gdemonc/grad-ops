<script setup lang="ts">
import { proposalStatusMeta, riskMeta, shortHash } from '../types'
import type { ProposalItem } from '../types'

defineProps<{
  proposal: ProposalItem
}>()
</script>

<template>
  <a-card class="evidence-card" size="small">
    <template #title>
      <div class="panel-title">
        <span class="mono">提案 #{{ proposal.id }} · {{ proposal.tool }}</span>
        <a-tag :color="riskMeta(proposal.risk_level).color" size="small">
          {{ riskMeta(proposal.risk_level).label }}
        </a-tag>
        <a-tag :color="proposalStatusMeta(proposal.status).color" size="small">
          {{ proposalStatusMeta(proposal.status).label }}
        </a-tag>
      </div>
    </template>
    <p class="evidence-summary">{{ proposal.rationale || '（无理由说明）' }}</p>

    <div class="kv">
      <span class="k">动作类型</span>
      <span class="v mono">{{ proposal.contract.kind }}</span>
    </div>
    <div class="kv">
      <span class="k">执行脚本</span>
    </div>
    <pre class="json">{{ proposal.contract.script }}</pre>

    <div class="kv">
      <span class="k">后置条件</span>
    </div>
    <pre class="json">{{ proposal.contract.postcondition.command }}</pre>

    <template v-if="proposal.contract.compensation">
      <div class="kv">
        <span class="k">补偿动作</span>
      </div>
      <pre class="json">{{ proposal.contract.compensation.command }}</pre>
    </template>

    <div class="kv">
      <span class="k">运行身份</span>
      <span class="v mono">{{ proposal.contract.run_as || '—' }}</span>
    </div>
    <div class="kv">
      <span class="k">路径</span>
      <span class="v mono">{{ (proposal.contract.paths || []).join(', ') || '—' }}</span>
    </div>
    <div class="kv">
      <span class="k">契约摘要</span>
      <span class="v hash">{{ shortHash(proposal.contract_digest) }}</span>
    </div>
    <div class="kv">
      <span class="k">策略版本</span>
      <span class="v mono dim">{{ proposal.policy_version }}</span>
    </div>
  </a-card>
</template>
