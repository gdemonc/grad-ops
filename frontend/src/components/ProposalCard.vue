<script setup lang="ts">
import { prettyJson, proposalStatusMeta, riskMeta } from '../types'
import type { ProposalItem } from '../types'

defineProps<{
  proposal: ProposalItem
}>()
</script>

<template>
  <a-card class="evidence-card" size="small">
    <template #title>
      <div class="panel-title">
        <span class="mono">提案 #{{ proposal.id }} · {{ proposal.tool_name }}</span>
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
      <span class="k">契约摘要</span>
      <span class="v hash">{{ proposal.contract_digest || '—' }}</span>
    </div>
    <div class="kv">
      <span class="k">前置条件</span>
    </div>
    <pre class="json">{{ prettyJson(proposal.preconditions_json) || '[]' }}</pre>
    <div class="kv">
      <span class="k">后置条件</span>
    </div>
    <pre class="json">{{ prettyJson(proposal.postconditions_json) || '[]' }}</pre>
  </a-card>
</template>
