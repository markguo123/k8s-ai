<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  data: unknown
}>()

const record = computed<Record<string, unknown>>(() =>
  props.data && typeof props.data === 'object' && !Array.isArray(props.data)
    ? (props.data as Record<string, unknown>)
    : {},
)

const command = computed(() => (typeof record.value.command === 'string' ? record.value.command : ''))
const stdout = computed(() => {
  if (typeof props.data === 'string') {
    return props.data
  }
  return typeof record.value.stdout === 'string' ? record.value.stdout : ''
})
const stderr = computed(() => (typeof record.value.stderr === 'string' ? record.value.stderr : ''))
const exitCode = computed(() =>
  typeof record.value.exitCode === 'number' ? record.value.exitCode : null,
)
</script>

<template>
  <div class="toolcard-terminal">
    <div v-if="command" class="terminal-command">
      <span class="terminal-prompt">$</span>
      <code>{{ command }}</code>
    </div>
    <div v-if="stdout" class="terminal-section">
      <div class="terminal-label">stdout</div>
      <pre class="terminal-output">{{ stdout }}</pre>
    </div>
    <div v-if="stderr" class="terminal-section terminal-stderr-section">
      <div class="terminal-label">stderr</div>
      <pre class="terminal-output terminal-stderr">{{ stderr }}</pre>
    </div>
    <div v-if="exitCode !== null" class="terminal-exit" :class="{ failed: exitCode !== 0 }">
      退出码 {{ exitCode }}
    </div>
  </div>
</template>

<style scoped>
.toolcard-terminal {
  display: flex;
  flex-direction: column;
  gap: 8px;
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface-2);
}

.terminal-command {
  display: flex;
  gap: 8px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--app-border);
  font-family: var(--app-mono);
  font-size: 11.5px;
  line-height: 1.5;
  word-break: break-all;
}

.terminal-prompt {
  flex: none;
  color: var(--app-accent);
}

.terminal-command code {
  min-width: 0;
  color: var(--app-text);
  white-space: pre-wrap;
  word-break: break-word;
}

.terminal-section {
  padding: 0 10px;
}

.terminal-label {
  margin-bottom: 4px;
  color: var(--app-muted);
  font-family: var(--app-mono);
  font-size: 10.5px;
  text-transform: lowercase;
}

.terminal-output {
  margin: 0;
  max-height: 360px;
  overflow: auto;
  color: var(--app-text);
  font-family: var(--app-mono);
  font-size: 11.5px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}

.terminal-stderr {
  color: var(--app-danger);
}

.terminal-exit {
  align-self: flex-start;
  margin: 0 10px 10px;
  padding: 1px 7px;
  border-radius: 999px;
  color: var(--app-success);
  background: var(--app-success-soft);
  font-family: var(--app-mono);
  font-size: 11px;
}

.terminal-exit.failed {
  color: var(--app-danger);
  background: var(--app-danger-soft);
}
</style>
