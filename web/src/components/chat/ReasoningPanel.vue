<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDown, View } from '@element-plus/icons-vue'

/**
 * P2.7 ReasoningPanel：思考面板。
 * 行为对齐三期 Web Chat 与原型：推理期间自动展开并流式渲染，
 * 推理结束后自动折叠为一行摘要（不是消失）；用户手动开合后以用户操作为准。
 */

const props = withDefaults(
  defineProps<{
    text: string
    streaming: boolean
    /** 未手动操作时的初始展开态（默认折叠，仅流式时自动展开）。 */
    defaultOpen?: boolean
  }>(),
  {
    text: '',
    streaming: false,
    defaultOpen: false,
  },
)

const open = ref(props.defaultOpen || props.streaming)
/** 用户是否手动操作过：手动操作优先，不被流式状态覆盖。 */
const touched = ref(false)

watch(
  () => props.streaming,
  (streaming) => {
    if (touched.value) {
      return
    }
    // 推理开始自动展开；推理结束（报告出现）自动折叠为摘要。
    open.value = streaming
  },
)

const hasContent = computed(() => props.text.trim().length > 0)
const summary = computed(() => props.text.replace(/\s+/g, ' ').trim().slice(0, 80))

function toggle(): void {
  touched.value = true
  open.value = !open.value
}
</script>

<template>
  <section class="reasoning" :class="{ open, streaming }">
    <button type="button" class="reasoning-head" @click="toggle" :aria-expanded="open">
      <span class="reasoning-dot" :class="{ on: streaming }"></span>
      <span class="reasoning-title">思考过程</span>
      <span v-if="streaming" class="reasoning-hint">推理中…</span>
      <span v-else-if="hasContent" class="reasoning-summary">{{ summary }}</span>
      <el-icon class="reasoning-caret" :class="{ open }"><ArrowDown /></el-icon>
    </button>
    <div v-if="open" class="reasoning-body">
      <pre class="reasoning-text"><template v-if="hasContent">{{ text }}</template><template v-else><el-icon class="reasoning-loading"><View /></el-icon> 正在思考…</template></pre>
      <p class="reasoning-foot">reasoning · 仅为模型推理过程，不进入最终报告</p>
    </div>
  </section>
</template>

<style scoped>
.reasoning {
  overflow: hidden;
  max-width: 820px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}

.reasoning-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 9px 14px;
  border: none;
  background: var(--app-surface);
  color: var(--app-text);
  font-family: inherit;
  font-size: 12px;
  font-weight: 650;
  cursor: pointer;
  text-align: left;
}

.reasoning-head:hover {
  color: #fff;
}

.reasoning-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
  background: var(--app-muted);
}

.reasoning-dot.on {
  background: var(--app-accent);
  box-shadow: 0 0 0 4px var(--app-accent-soft);
  animation: reasoning-blink 1.1s ease-in-out infinite;
}

.reasoning-title {
  font-weight: 650;
  color: var(--app-muted);
  flex: none;
}

.reasoning-hint {
  color: var(--app-accent);
  font-size: 11px;
}

.reasoning-summary {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--app-muted);
  font-size: 11px;
}

.reasoning-caret {
  margin-left: auto;
  color: var(--app-muted);
  transition: transform 0.2s ease;
}

.reasoning-caret.open {
  transform: rotate(180deg);
}

.reasoning-body {
  border-top: 1px solid var(--app-border);
  padding: 11px 14px;
  background: var(--app-surface-2);
}

.reasoning-text {
  margin: 0;
  color: var(--app-muted);
  font-family: var(--app-mono);
  font-size: 12.5px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
}

.reasoning-foot {
  margin: 0;
  padding: 6px 0 0;
  border-top: 1px dashed var(--app-border);
  color: var(--app-faint);
  font-size: 11px;
}

.reasoning-loading {
  vertical-align: -2px;
  color: var(--app-accent);
  animation: reasoning-pulse 1.1s ease-in-out infinite;
}

@keyframes reasoning-blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.3;
  }
}

@keyframes reasoning-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.4;
  }
}
</style>
