<script setup lang="ts">
import { ref } from 'vue'
import { Promotion, VideoPause } from '@element-plus/icons-vue'

/**
 * P2.10 ComposerBox：底部消息输入区。
 * - 多行输入：Enter 发送 / Shift+Enter 换行；
 * - 模型选择（可选，空则隐藏）；
 * - 空闲显示「发送」，运行中变为「停止」。
 */

defineProps<{
  modelValue: string
  disabled?: boolean
  sending?: boolean
  models?: string[]
  model?: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'update:model', value: string): void
  (e: 'send'): void
  (e: 'stop'): void
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)

function focus(): void {
  textareaRef.value?.focus()
}

defineExpose({ focus })
</script>

<template>
  <div class="composer">
    <div class="composer-options">
      <el-select
        v-if="models && models.length > 0"
        :model-value="model"
        size="small"
        class="composer-model"
        placeholder="选择模型"
        @update:model-value="emit('update:model', $event)"
      >
        <el-option v-for="item in models" :key="item" :label="item" :value="item" />
      </el-select>
      <span class="composer-tip">Enter 发送 · Shift+Enter 换行</span>
    </div>
    <div class="composer-input" @keydown.enter.exact.prevent="!disabled && emit('send')">
      <textarea
        ref="textareaRef"
        :value="modelValue"
        class="composer-textarea"
        :disabled="disabled"
        rows="3"
        placeholder="输入消息，描述你的诉求或问题…"
        @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
      ></textarea>
    </div>
    <div class="composer-actions">
      <el-button
        v-if="!sending"
        type="primary"
        :icon="Promotion"
        :disabled="disabled || !modelValue.trim()"
        @click="emit('send')"
      >
        发送
      </el-button>
      <el-button v-else type="danger" plain :icon="VideoPause" @click="emit('stop')">
        停止
      </el-button>
    </div>
  </div>
</template>

<style scoped>
.composer {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.composer-options {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 22px;
}

.composer-model {
  width: 180px;
}

.composer-tip {
  font-size: 11px;
  color: var(--app-muted);
}

.composer-input {
  display: flex;
}

.composer-textarea {
  flex: 1;
  min-height: 72px;
  max-height: 200px;
  resize: vertical;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-bg);
  color: var(--app-text);
  font-family: inherit;
  font-size: 13.5px;
  line-height: 1.55;
  outline: none;
  transition:
    border-color 0.15s ease,
    box-shadow 0.15s ease;
}

.composer-textarea:focus {
  border-color: var(--app-accent);
  box-shadow: 0 0 0 2px var(--app-accent-soft);
}

.composer-textarea:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.composer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
