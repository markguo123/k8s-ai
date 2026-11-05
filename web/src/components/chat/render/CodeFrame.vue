<script setup lang="ts">
import { computed } from 'vue'
import { CopyDocument } from '@element-plus/icons-vue'

import copyText from '@/composables/useClipboard'

const props = withDefaults(
  defineProps<{
    /** 展示用的行（每行可为纯文本或 HTML 片段字符串）。 */
    lines?: string[]
    /** 原始完整文本（用于复制）。缺省时拼接 lines。 */
    raw?: string
    /** 顶部标签，如 yaml / logs / 文件名。 */
    label?: string
    /** 每行高亮（行号 → 是否高亮）。 */
    highlight?: Record<number, boolean>
    /** 朴素文本模式（不做行号包装额外转义，已由调用方脱敏）。 */
    plain?: boolean
    maxHeight?: number
  }>(),
  {
    lines: () => [],
    raw: '',
    label: '',
    highlight: undefined,
    plain: false,
    maxHeight: 360,
  },
)

const copySource = computed(() => props.raw || props.lines.join('\n'))

const style = computed(() => ({ maxHeight: `${props.maxHeight}px` }))
</script>

<template>
  <div class="code-frame">
    <div v-if="label" class="code-frame-head">
      <span class="code-frame-label">{{ label }}</span>
      <button type="button" class="code-frame-copy" @click="copyText(copySource)">
        <el-icon><CopyDocument /></el-icon>
        复制
      </button>
    </div>
    <div class="code-frame-body" :style="style">
      <template v-if="plain">
        <pre class="code-frame-pre"><code>{{ raw || lines.join('\n') }}</code></pre>
      </template>
      <template v-else>
        <div
          v-for="(line, index) in lines"
          :key="index"
          class="code-frame-line"
          :class="{ highlight: highlight?.[index + 1] }"
        >
          <span class="code-frame-lno">{{ index + 1 }}</span>
          <span class="code-frame-line-content" v-html="line || ' '"></span>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.code-frame {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-code-bg, #0f172a);
}

.code-frame-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
  background: rgba(255, 255, 255, 0.04);
}

.code-frame-label {
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 11px;
  color: #94a3b8;
}

.code-frame-copy {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: #cbd5e1;
  font-family: inherit;
  font-size: 11px;
  cursor: pointer;
}

.code-frame-copy:hover {
  background: rgba(255, 255, 255, 0.1);
}

.code-frame-body {
  overflow: auto;
  padding: 4px 0;
}

.code-frame-pre {
  margin: 0;
  padding: 10px 12px;
  overflow: auto;
  color: #e2e8f0;
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}

.code-frame-line {
  display: flex;
  min-height: 21px;
  padding: 0 12px 0 0;
  color: #cbd5e1;
}

.code-frame-line:hover {
  background: rgba(255, 255, 255, 0.03);
}

.code-frame-line.highlight {
  background: rgba(248, 113, 113, 0.12);
}

.code-frame-lno {
  flex: none;
  width: 40px;
  padding-right: 10px;
  color: #475569;
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 11px;
  line-height: 21px;
  text-align: right;
  user-select: none;
}

.code-frame-line-content {
  flex: 1;
  min-width: 0;
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 12px;
  line-height: 21px;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
