<script setup lang="ts">
import { computed } from 'vue'
import { Coin } from '@element-plus/icons-vue'

import type { AgentToken } from '@/api/sse'

/**
 * P2.13 TokenUsageBadge：会话级 Token 用量浮标（右下角）。
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

const visible = computed(() => total.value !== null || prompt.value !== null || completion.value !== null)

function format(value: number | null): string {
  if (value === null) {
    return '–'
  }
  return String(value)
}
</script>

<template>
  <div v-if="visible" class="token-badge" :title="`提示词 ${format(prompt)} · 生成 ${format(completion)}`">
    <el-icon class="token-icon"><Coin /></el-icon>
    <span class="token-total">{{ format(total) }}</span>
    <span class="token-label">tokens</span>
  </div>
</template>

<style scoped>
.token-badge {
  position: fixed;
  right: 24px;
  bottom: 96px;
  z-index: 30;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-surface);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.18);
  font-size: 11px;
  color: var(--app-muted);
  font-variant-numeric: tabular-nums;
  pointer-events: none;
}

.token-icon {
  color: var(--app-accent);
}

.token-total {
  color: var(--app-text);
  font-weight: 600;
}
</style>
