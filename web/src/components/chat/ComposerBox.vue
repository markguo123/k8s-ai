<script setup lang="ts">
import { ref } from 'vue'
import { Lock, Promotion, VideoPause } from '@element-plus/icons-vue'

/**
 * P2.10 ComposerBox：底部消息输入区。
 * - 多行输入：Enter 发送 / Shift+Enter 换行；
 * - 工具条展示当前模型与 Secret 策略（与原型一致，只读提示）；
 * - 空闲显示「发送」，运行中变为「停止」。
 */

const props = withDefaults(
  defineProps<{
    modelValue: string
    /** 当前会话模型名（只读展示，切换模型属配置中心能力）。 */
    model?: string
    /** Secret 访问策略展示串（如 create_only）。 */
    secretPolicy?: string
    disabled?: boolean
    sending?: boolean
  }>(),
  {
    model: '',
    secretPolicy: 'create_only',
    disabled: false,
    sending: false,
  },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'send'): void
  (e: 'stop'): void
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)

function focus(): void {
  textareaRef.value?.focus()
}

/** Enter 发送（Shift+Enter 换行）；禁用或空白输入时不发送。 */
function handleEnter(): void {
  if (props.disabled || props.sending || !props.modelValue.trim()) {
    return
  }
  emit('send')
}

defineExpose({ focus })
</script>

<template>
  <div class="composer">
    <div class="composer-input" @keydown.enter.exact.prevent="handleEnter">
      <textarea
        ref="textareaRef"
        :value="modelValue"
        class="composer-textarea"
        :disabled="disabled"
        rows="3"
        placeholder="描述你的运维问题，例如：查看 default 命名空间异常工作负载…（Enter 发送，Shift+Enter 换行）"
        @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
      ></textarea>
    </div>
    <div class="composer-toolbar">
      <span class="composer-chip">🧠 {{ model || '默认模型' }}</span>
      <span class="composer-chip"><el-icon><Lock /></el-icon> Secret: {{ secretPolicy }}</span>
      <span class="composer-hint">Shell 语法命令需人工审批 · Secret 值不会进入会话与审计</span>
      <span class="composer-actions">
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
      </span>
    </div>
  </div>
</template>

<style scoped>
.composer {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: 12px;
  background: var(--app-surface);
}

.composer-toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  min-height: 22px;
  padding-top: 6px;
  border-top: 1px solid var(--app-border);
}

.composer-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0 10px;
  height: 26px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  color: var(--app-muted);
  font-size: 12px;
  white-space: nowrap;
}

.composer-hint {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11.5px;
  color: var(--app-faint);
}

.composer-input {
  display: flex;
}

.composer-textarea {
  flex: 1;
  /* 对齐原型：输入区无边框透明，容器提供焦点环 */
  min-height: 52px;
  max-height: 180px;
  resize: none;
  padding: 2px 2px 4px;
  border: none;
  border-radius: 0;
  background: transparent;
  color: var(--app-text);
  font-family: inherit;
  font-size: 13.5px;
  line-height: 1.55;
  outline: none;
  transition: none;
}

.composer:focus-within {
  border-color: var(--app-accent);
}

.composer-textarea:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.composer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-left: auto;
}

/* 发送/停止按钮对齐原型 .send-btn（30px 高、8px 圆角） */
.composer-actions :deep(.el-button) {
  height: 30px;
  border-radius: 8px;
  font-size: 12.5px;
}
</style>
