<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, CircleCheck, Lock, Warning } from '@element-plus/icons-vue'

/**
 * P2.9 ApprovalCard：审批卡。
 * - 风险 + 工具 + 参数摘要 + 目标资源 + 命中策略/原因；
 * - 自确认/审批/拒绝；白名单自动通过显示 AUTO 徽标；
 * - 高危 / shell 永不自动（后端强校验，前端只做展示与提交）；
 * - secret=true 时密码框取值仅本次提交，不入历史流（后端脱敏，前端不回显）。
 */

export interface ApprovalUiData {
  key: string
  prompt: string
  options: Array<{ label: string; value: string }>
  secret: boolean
  decision: 'pending' | 'approved' | 'rejected' | 'auto'
  tool: string
  risk: string
  summary: string
  resource: unknown
  reason?: string
}

const props = withDefaults(
  defineProps<{
    approval: ApprovalUiData
    /**
     * P2.9 当前用户是否有权审批（本人自确认）。
     * false 时隐藏审批/拒绝按钮并给出提示——后端仍独立强校验，前端隐藏不是安全机制。
     * 默认 true：未接入权限上下文的场景（如三期回放）保持原有可操作行为。
     */
    canDecide?: boolean
  }>(),
  {
    canDecide: true,
  },
)

const emit = defineEmits<{
  (e: 'approve', value: string): void
  (e: 'reject'): void
}>()

const secretValue = ref('')
const secretVisible = ref(false)

const pending = computed(() => props.approval.decision === 'pending')
const auto = computed(() => props.approval.decision === 'auto')
const showActions = computed(() => pending.value && props.canDecide)

const riskTag = computed<'danger' | 'warning' | 'info' | 'primary'>(() => {
  const risk = props.approval.risk?.toUpperCase() ?? ''
  if (/HIGH|高|CRITICAL|SHELL/.test(risk)) return 'danger'
  if (/MEDIUM|中|RISK/.test(risk)) return 'warning'
  if (/LOW|低/.test(risk)) return 'info'
  return 'primary'
})

const riskLabel = computed(() => {
  const risk = props.approval.risk?.toUpperCase() ?? ''
  if (/HIGH|高|CRITICAL/.test(risk)) return '高风险'
  if (/MEDIUM|中|RISK/.test(risk)) return '中风险'
  if (/LOW|低/.test(risk)) return '低风险'
  return props.approval.risk || ''
})

const resourceText = computed(() => {
  const record = props.approval.resource
  if (!record) return ''
  if (typeof record === 'string') return record
  const parsed = record as Record<string, unknown>
  const parts: string[] = []
  for (const key of ['kind', 'name', 'namespace', 'group', 'version']) {
    if (typeof parsed[key] === 'string' && parsed[key]) {
      parts.push(String(parsed[key]))
    }
  }
  return parts.join(' · ')
})

/** 把密码框值填入允许占位符（如 $INPUT / $SECRET / $$SECRET），仅本次提交。 */
function resolveSecret(optionValue: string): string {
  const value = secretValue.value
  if (!value) {
    return optionValue
  }
  return optionValue
    .replace(/\$\$SECRET/g, value)
    .replace(/\$SECRET/g, value)
    .replace(/\$INPUT/g, value)
}

function approve(option: { label: string; value: string }): void {
  const value = props.approval.secret ? resolveSecret(option.value) : option.value
  emit('approve', value)
  // 值不入流，提交后即清空。
  secretValue.value = ''
}

function reject(): void {
  emit('reject')
  secretValue.value = ''
}
</script>

<template>
  <article class="approval-card" :class="approval.decision">
    <div class="approval-head">
      <el-icon class="approval-icon" :class="{ danger: pending }"
        ><Lock v-if="pending" /><CircleCheck
          v-else-if="auto || approval.decision === 'approved'" /><Warning v-else
      /></el-icon>
      <span class="approval-title">审批请求</span>
      <el-tag v-if="auto" size="small" type="info" effect="dark" class="approval-auto">
        AUTO 白名单自动通过
      </el-tag>
      <el-tag
        v-else-if="approval.decision === 'approved'"
        size="small"
        type="success"
        effect="plain"
      >
        已通过
      </el-tag>
      <el-tag
        v-else-if="approval.decision === 'rejected'"
        size="small"
        type="danger"
        effect="plain"
      >
        已拒绝
      </el-tag>
    </div>

    <p v-if="approval.prompt" class="approval-prompt">{{ approval.prompt }}</p>

    <div class="approval-meta">
      <el-tag v-if="riskLabel" size="small" effect="plain" :type="riskTag">{{ riskLabel }}</el-tag>
      <span v-if="approval.tool" class="approval-meta-item">{{ approval.tool }}</span>
      <span
        v-if="approval.summary"
        class="approval-meta-item approval-summary"
        :title="approval.summary"
      >
        {{ approval.summary }}
      </span>
      <span v-if="resourceText" class="approval-meta-item approval-resource">{{
        resourceText
      }}</span>
      <span v-if="approval.reason" class="approval-meta-item approval-reason">{{
        approval.reason
      }}</span>
    </div>

    <!-- Secret 占位输入：仅本次提交，value 永不写入历史 -->
    <div v-if="approval.secret && showActions" class="approval-secret">
      <div class="approval-secret-row">
        <el-input
          v-model="secretValue"
          class="approval-secret-input"
          :type="secretVisible ? 'text' : 'password'"
          :show-password="false"
          placeholder="输入 Secret 值（仅本次验证使用，不会回显或入库）"
          @keyup.enter="showActions && approval.options[0] && approve(approval.options[0])"
        />
        <el-button
          :icon="approval.options[0] ? Check : undefined"
          type="primary"
          :disabled="!secretValue"
          @click="approval.options[0] && approve(approval.options[0])"
        >
          {{ approval.options[0]?.label || '确认' }}
        </el-button>
        <el-button type="danger" plain @click="reject">拒绝</el-button>
      </div>
      <span class="approval-secret-hint">Secret 值不进入会话记录与审计，仅用于本次鉴权回传。</span>
    </div>

    <!-- 普通审批（非 secret）：展示批准/拒绝选项 -->
    <div v-else-if="showActions" class="approval-actions">
      <el-button
        v-for="(option, index) in approval.options"
        :key="option.value || index"
        type="primary"
        plain
        @click="approve(option)"
      >
        {{ option.label || option.value }}
      </el-button>
      <el-button type="danger" plain @click="reject">拒绝</el-button>
    </div>

    <!-- 无权审批：按钮整体隐藏，只说明原因（后端仍会独立拒绝越权请求） -->
    <div v-else-if="pending" class="approval-locked">
      <el-icon><Lock /></el-icon>
      <span>当前账号无审批权限，请联系管理员开通后重试</span>
    </div>
  </article>
</template>

<style scoped>
.approval-card {
  /* 对齐原型：审批卡与工具卡同一视觉族，左侧 accent 竖条标识"需要人工介入" */
  max-width: 820px;
  border: 1px solid var(--app-border);
  border-left: 3px solid var(--app-accent);
  border-radius: var(--app-radius);
  background: var(--app-surface);
}

.approval-card.approved {
  border-left-color: var(--app-success);
}

.approval-card.rejected {
  border-left-color: var(--app-danger);
}

.approval-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
}

.approval-icon {
  color: var(--app-warning);
  flex: none;
}

.approval-icon.danger {
  color: var(--app-accent);
}

.approval-title {
  font-weight: 600;
  font-size: 12.5px;
}

.approval-auto {
  margin-left: auto;
}

.approval-prompt {
  margin: 0;
  padding: 0 12px 8px;
  color: var(--app-text);
  font-size: 13px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}

.approval-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  padding: 0 12px 10px;
}

.approval-meta-item {
  font-size: 11.5px;
  color: var(--app-muted);
}

.approval-summary {
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--app-mono);
}

.approval-resource {
  padding: 1px 8px;
  border-radius: 4px;
  background: var(--app-bg);
}

.approval-reason {
  color: var(--app-accent);
}

.approval-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 12px 10px;
}

.approval-locked {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 12px 10px;
  color: var(--app-muted);
  font-size: 12px;
}

.approval-secret {
  padding: 0 12px 10px;
}

.approval-secret-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.approval-secret-input {
  flex: 1;
  min-width: 0;
}

.approval-secret-hint {
  display: block;
  margin-top: 6px;
  font-size: 11px;
  color: var(--app-muted);
}
</style>
