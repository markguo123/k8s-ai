<script setup lang="ts">
import { computed } from 'vue'
import { ArrowDown, View } from '@element-plus/icons-vue'

/**
 * P2.7 ReasoningPanel：思考面板。
 * 推理期间展开并流式渲染；报告出现后由父级（TurnBlock）折叠为摘要。
 */

const props = withDefaults(
  defineProps<{
    text: string
    streaming: boolean
    open: boolean
  }>(),
  {
    text: '',
    streaming: false,
    open: true,
  },
)

const emit = defineEmits<{ (e: 'update:open', value: boolean): void }>()

const hasContent = computed(() => props.text.trim().length > 0)

function toggle(): void {
  emit('update:open', !props.open)
}
</script>

<template>
  <section class="reasoning" :class="{ open, streaming }">
      <button type="button" class="reasoning-head" @click="toggle" :aria-expanded="open">
      <span class="reasoning-dot" :class="{ on: streaming }"></span>
      <span class="reasoning-title">思考过程</span>
      <span v-if="streaming" class="reasoning-hint">推理中…</span>
      <span v-else-if="hasContent" class="reasoning-summary">{{ text.slice(0, 60) }}</span>
      <el-icon class="reasoning-caret" :class="{ open }"><ArrowDown /></el-icon>
    </button>
    <div v-if="open" class="reasoning-body">
      <pre class="reasoning-text"><template v-if="hasContent">{{ text }}</template><template v-else><el-icon class="reasoning-loading"><View /></el-icon> 正在思考…</template></pre>
    </div>
  </section>
</template>

<style scoped>
.reasoning {
  overflow: hidden;
  border: 1px dashed var(--app-border);
  border-left: 3px solid var(--app-accent);
  border-radius: var(--app-radius);
  background: var(--app-bg);
}

.reasoning-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  text-align: left;
}

.reasoning-head:hover {
  background: var(--app-surface);
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
  font-weight: 600;
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
  border-top: 1px dashed var(--app-border);
  padding: 10px 12px;
}

.reasoning-text {
  margin: 0;
  color: var(--app-muted);
  font-family: inherit;
  font-size: 12.5px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
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
