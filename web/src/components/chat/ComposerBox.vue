<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { Lock, Promotion, VideoPause } from '@element-plus/icons-vue'

import { listSkills } from '@/api/skills'
import type { Skill } from '@/api/types'

/**
 * P2.10/P3.8.4 ComposerBox：底部消息输入区。
 * - 多行输入：Enter 发送 / Shift+Enter 换行；
 * - 工具条展示当前模型与 Secret 策略（与原型一致，只读提示）；
 * - 空闲显示「发送」，运行中变为「停止」；
 * - 输入 `@` 弹出已启用的 procedural Skill 列表（由父级传入 skill.read 权限），
 *   选中后插入 `@技能名`。前端只负责提示与回填，正文可信度与服务端强校验仍在后端。
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
    /** 当前角色是否可引用 Skill（skill.read）。前端只隐藏提示，后端独立强校验。 */
    canUseSkills?: boolean
  }>(),
  {
    model: '',
    secretPolicy: 'create_only',
    disabled: false,
    sending: false,
    canUseSkills: false,
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

/** 已启用的 procedural Skill 列表（供 @ 提示与父级提交前解析）。 */
const availableSkills = ref<Skill[]>([])

/** 当前光标处的 @ 引用是否存在、过滤词与选中项。 */
const mentionOpen = ref(false)
const mentionQuery = ref('')
const mentionAnchor = ref(-1)
const mentionIndex = ref(0)

/** 可选项：仅 procedural 且已启用的 Skill，按名称排序保证列表稳定。 */
const mentionSkills = computed(() => {
  const q = mentionQuery.value.trim().toLowerCase()
  return availableSkills.value
    .filter((skill) => skill.spec.type === 'procedural' && skill.enabled)
    .filter((skill) => {
      if (!q) {
        return true
      }
      return (
        skill.name.toLowerCase().includes(q) ||
        (skill.spec.description ?? '').toLowerCase().includes(q)
      )
    })
    .sort((a, b) => a.name.localeCompare(b.name))
})

function closeMention(): void {
  mentionOpen.value = false
  mentionQuery.value = ''
  mentionAnchor.value = -1
  mentionIndex.value = 0
}

/** 依据光标位置判断是否正在输入 `@技能名`，是则打开引用提示。 */
function recomputeMention(textarea: HTMLTextAreaElement): void {
  if (!props.canUseSkills) {
    closeMention()
    return
  }
  const value = textarea.value
  const pos = textarea.selectionStart ?? value.length
  const before = value.slice(0, pos)
  const at = before.lastIndexOf('@')
  if (at >= 0 && !/\s/.test(before.slice(at))) {
    const token = before.slice(at + 1)
    if (token === '' || /^[a-zA-Z0-9][\w.-]*$/.test(token)) {
      mentionAnchor.value = at
      mentionQuery.value = token
      mentionIndex.value = 0
      mentionOpen.value = true
      return
    }
  }
  closeMention()
}

/** 插入选中的技能名（替换 `@过滤词`），光标落在名字后。 */
function selectMention(skill: Skill): void {
  const textarea = textareaRef.value
  if (!textarea || mentionAnchor.value < 0) {
    closeMention()
    return
  }
  const anchor = mentionAnchor.value
  const pos = textarea.selectionStart ?? props.modelValue.length
  const next =
    props.modelValue.slice(0, anchor) + `@${skill.name} ` + props.modelValue.slice(pos)
  emit('update:modelValue', next)
  closeMention()
  void nextTick(() => {
    textarea.focus()
    const caret = anchor + skill.name.length + 2
    textarea.setSelectionRange(caret, caret)
  })
}

/** Enter 发送（Shift+Enter 换行）；引用提示打开时优先选中技能。 */
function handleEnter(): void {
  if (mentionOpen.value) {
    const target = mentionSkills.value[mentionIndex.value] ?? mentionSkills.value[0]
    if (target) {
      selectMention(target)
    }
    return
  }
  if (props.disabled || props.sending || !props.modelValue.trim()) {
    return
  }
  emit('send')
}

/** 方向键在引用提示中上下移动。 */
function handleKeydown(event: KeyboardEvent): void {
  if (!mentionOpen.value || mentionSkills.value.length === 0) {
    return
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    mentionIndex.value = (mentionIndex.value + 1) % mentionSkills.value.length
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    mentionIndex.value =
      (mentionIndex.value - 1 + mentionSkills.value.length) % mentionSkills.value.length
  } else if (event.key === 'Escape') {
    event.preventDefault()
    closeMention()
  }
}

onMounted(() => {
  if (!props.canUseSkills) {
    return
  }
  void listSkills()
    .then((skills) => {
      availableSkills.value = skills ?? []
    })
    .catch(() => {
      availableSkills.value = []
    })
})

defineExpose({ focus, availableSkills })
</script>

<template>
  <div class="composer">
    <div
      class="composer-input"
      @keydown.enter.exact.prevent="handleEnter"
      @keydown="handleKeydown"
    >
      <div v-if="mentionOpen && mentionSkills.length" class="mention-popup" role="listbox">
        <button
          v-for="(skill, index) in mentionSkills"
          :key="skill.name"
          type="button"
          class="mention-item"
          :class="{ active: index === mentionIndex }"
          role="option"
          :aria-selected="index === mentionIndex"
          @mousedown.prevent="selectMention(skill)"
          @mouseover="mentionIndex = index"
        >
          <span class="mention-name">@{{ skill.name }}</span>
          <span class="mention-desc">{{ skill.spec.description }}</span>
        </button>
      </div>
      <textarea
        ref="textareaRef"
        :value="modelValue"
        class="composer-textarea"
        :disabled="disabled"
        rows="3"
        placeholder="描述你的运维问题，例如：查看 default 命名空间异常工作负载…（Enter 发送，Shift+Enter 换行）"
        @input="
          emit('update:modelValue', ($event.target as HTMLTextAreaElement).value);
          recomputeMention($event.target as HTMLTextAreaElement);
        "
      ></textarea>
    </div>
    <div class="composer-toolbar">
      <span class="composer-chip">🧠 {{ model || '默认模型' }}</span>
      <span class="composer-chip"><el-icon><Lock /></el-icon> Secret: {{ secretPolicy }}</span>
      <el-tooltip
        content="Shell 语法命令需人工审批；Secret 值与 kubeconfig 不会进入会话与审计"
        placement="top"
      >
        <!-- P7.2：长安全文案降噪为小字入口，完整说明放 Tooltip 与 aria-label（信息不丢、可访问） -->
        <span
          class="composer-hint"
          aria-label="Shell 语法命令需人工审批；Secret 值与 kubeconfig 不会进入会话与审计"
          >安全策略</span
        >
      </el-tooltip>
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
  /* P7.6：输入区是玻璃层，不做纯黑方块 */
  background: var(--app-surface);
  backdrop-filter: blur(16px) saturate(120%);
  transition:
    border-color 0.15s ease,
    box-shadow 0.15s ease;
}

/* P7.2/P7.6：聚焦时边框点亮为主色 + 外发光，明确"当前在输入" */
.composer:focus-within {
  border-color: var(--app-accent);
  box-shadow:
    var(--app-glow-accent),
    inset 0 0 0 1px var(--app-accent-soft);
}

.composer-toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  min-height: 22px;
  padding-top: 6px;
  border-top: 1px solid var(--app-hairline);
}

.composer-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0 10px;
  height: 26px;
  border: 1px solid var(--app-hairline);
  border-radius: 999px;
  background: var(--app-card-bg);
  color: var(--app-muted);
  font-size: 12px;
  white-space: nowrap;
}

.composer-hint {
  flex: 1;
  min-width: 0;
  font-size: 11px;
  color: var(--app-faint);
  cursor: help;
  text-align: left;
  text-decoration: underline dotted;
  text-underline-offset: 3px;
}

.composer-input {
  position: relative;
  display: flex;
}

/* P3.8.4 @ 技能引用提示：悬浮在输入区上方，点击/方向键选中 */
.mention-popup {
  position: absolute;
  bottom: calc(100% + 8px);
  left: 0;
  z-index: 40;
  display: flex;
  flex-direction: column;
  max-width: 420px;
  max-height: 220px;
  overflow-y: auto;
  padding: 4px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  box-shadow: 0 12px 32px -18px rgba(0, 0, 0, 0.55);
}

.mention-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 8px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--app-text);
  text-align: left;
  cursor: pointer;
}

.mention-item:hover,
.mention-item.active {
  background: var(--app-accent-soft);
}

.mention-name {
  flex: none;
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 12px;
  font-weight: 600;
  color: var(--app-accent);
}

.mention-desc {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  color: var(--app-muted);
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

.composer-textarea::placeholder {
  color: var(--app-faint);
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

/* P7.6 发送按钮：登录页同款渐变（--app-brand-gradient），悬浮掠光 */
.composer-actions :deep(.el-button--primary) {
  position: relative;
  overflow: hidden;
  border: none;
  background: var(--app-brand-gradient);
  box-shadow: 0 8px 20px -12px var(--app-accent);
}

.composer-actions :deep(.el-button--primary:hover:not(.is-disabled))::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.35), transparent);
  transform: translateX(-100%);
  animation: composerSheen 0.9s ease;
}

/* 禁用态回到平面中性色：渐变会让"不可发送"看起来仍可点，属于误导 */
.composer-actions :deep(.el-button--primary.is-disabled) {
  background: var(--app-neutral-soft);
  color: var(--app-faint);
  box-shadow: none;
}

@keyframes composerSheen {
  to {
    transform: translateX(100%);
  }
}

@media (prefers-reduced-motion: reduce) {
  .composer-actions :deep(.el-button--primary:hover:not(.is-disabled))::after {
    animation: none;
  }
}
</style>
