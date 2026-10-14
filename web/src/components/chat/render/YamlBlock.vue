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
    label: 'yaml',
    maxHeight: 360,
  },
)

/** 先整体 HTML 转义，再对「键 / 字符串 / 数字 / 注释」上色（对齐老 UI renderYamlBlock）。 */
function escapeHtml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

function colorLine(line: string): string {
  const esc = escapeHtml(line)
  if (/^\s*#/.test(line)) {
    return esc.replace(/^([\s]*)(#.*)$/, '$1<span class="ycom">$2</span>')
  }
  return esc
    .replace(/^(\s*)([a-zA-Z][\w.-]*)(:)/, '$1<span class="ykey">$2</span><span class="ypunct">$3</span>')
    .replace(/(:[\s]*)(["'].*?["'])/g, '$1<span class="ystr">$2</span>')
    .replace(/(:[\s]*)(\d+)([\s]*)$/g, '$1<span class="ynum">$2</span>$3')
}

const lines = computed<string[]>(() => stripUntrusted(props.text).split('\n').map(colorLine))
</script>

<template>
  <CodeFrame :lines="lines" :raw="stripUntrusted(text)" :label="label" :max-height="maxHeight" />
</template>

<style scoped lang="css">
:deep(.ykey) {
  color: #7dd3fc;
}

:deep(.ystr) {
  color: #bef264;
}

:deep(.ynum) {
  color: #fbbf24;
}

:deep(.ycom) {
  color: #64748b;
  font-style: italic;
}

:deep(.ypunct) {
  color: #94a3b8;
}
</style>
