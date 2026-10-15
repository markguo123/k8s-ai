<script setup lang="ts">
import { computed } from 'vue'

import type { AgentToken } from '@/api/sse'
import { formatTokenCount } from '@/utils/format'

/**
 * P2.13 TokenUsageBadge：会话级 Token 用量浮标（右下角，对齐原型）。
 * - 会话级累计：打开历史会话时来自落库 Turn 汇总（刷新/重开仍可读），
 *   运行中随 `turn.completed` 的 token 事件累加；
 * 仅展示计数，不含任何敏感数据（对齐 §5.5）。
 */

const props = withDefaults(
  defineProps<{
    token: AgentToken | null
  }>(),
  {
    token: null,
  },
)

const total = computed(() => props.token?.total_tokens ?? null)
const prompt = computed(() => props.token?.prompt_tokens ?? null)
const completion = computed(() => props.token?.completion_tokens ?? null)

const visible = computed(() => {
  if (total.value === null && prompt.value === null && completion.value === null) {
    return false
  }
  return (total.value ?? 0) + (prompt.value ?? 0) + (completion.value ?? 0) > 0
})

const title = computed(
  () => `提示词 ${formatTokenCount(prompt.value)} · 生成 ${formatTokenCount(completion.value)}`,
)
</script>

<template>
  <div v-if="visible" class="token-badge" :title="title">
    <span class="token-label">Token 用量</span>
    <b class="token-total">{{ formatTokenCount(total) }}</b>
    <span class="token-note">· 会话</span>
  </div>
</template>

<style scoped>
.token-badge {
  position: fixed;
  right: 18px;
  bottom: 18px;
  z-index: 30;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-surface);
  box-shadow: var(--app-shadow);
  font-size: 11.5px;
  color: var(--app-muted);
  font-variant-numeric: tabular-nums;
  pointer-events: none;
}

.token-total {
  color: var(--app-text);
  font-weight: 600;
  font-family: var(--app-mono);
}

.token-note {
  color: var(--app-faint);
}
</style>
