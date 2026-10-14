<script setup lang="ts">
import { computed } from 'vue'

import CodeFrame from './CodeFrame.vue'
import { stripUntrusted } from './constants'

const props = withDefaults(
  defineProps<{
    text: string
    label?: string
    maxHeight?: number
  }>(),
  {
    label: 'logs',
    maxHeight: 360,
  },
)

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/**
 * 日志行着色：时间戳 / 级别（INFO/WARN/ERROR/DEBUG/PANIC/FATAL）/ 消息
 * （对齐老 UI renderLogsBlock；panic/fatal 高亮整行）。
 */
function colorLine(line: string): string {
  const escaped = escapeHtml(line)
  const match = line.match(
    /^(\d{4}-\d{2}-\d{2}[T\s]\d{2}:\d{2}:\d{2}[^\s]*\s+)?(INFO|WARN|WARNING|ERROR|PANIC|FATAL|DEBUG)?\s*(.*)$/i,
  )
  const ts = match?.[1] ? `<span class="log-ts">${escapeHtml(match[1].trim())} </span>` : ''
  const lvl = match?.[2]
    ? `<span class="log-lvl ${match[2].toUpperCase()}">${match[2].toUpperCase()}</span> `
    : ''
  const msg = match ? escapeHtml(match[3] || '') : escaped
  return `${ts}${lvl}<span class="log-msg">${msg}</span>`
}

const lines = computed<string[]>(() =>
  stripUntrusted(props.text)
    .split('\n')
    .map((line) => colorLine(line)),
)
</script>

<template>
  <CodeFrame
    :lines="lines"
    :raw="stripUntrusted(text)"
    :label="label"
    :max-height="maxHeight"
    :highlight="{}"
  />
</template>

<style scoped lang="css">
:deep(.log-ts) {
  color: #94a3b8;
}

:deep(.log-lvl.INFO) {
  color: #38bdf8;
}

:deep(.log-lvl.WARN),
:deep(.log-lvl.WARNING) {
  color: #fbbf24;
}

:deep(.log-lvl.ERROR),
:deep(.log-lvl.PANIC),
:deep(.log-lvl.FATAL) {
  color: #f87171;
}

:deep(.log-lvl.DEBUG) {
  color: #94a3b8;
}

:deep(.log-msg) {
  color: #cbd5e1;
}
</style>
