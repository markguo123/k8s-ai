<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowDown, WarningFilled } from '@element-plus/icons-vue'

import type { TurnUiData } from './turn'
import { formatDuration } from './turn'
import AgentStepper, { type AgentStep, type StepState } from './AgentStepper.vue'
import ReasoningPanel from './ReasoningPanel.vue'
import ToolCallCard from './ToolCallCard.vue'
import ApprovalCard from './ApprovalCard.vue'
import MarkdownView from './MarkdownView.vue'

/**
 * P2.7 TurnBlock：单回合容器。
 * 头部：序号 + 状态摘要 + 用户输入 + 耗时 + token；body 可折叠，
 * 内含 思考面板 / 步进器 / 工具卡 / 审批卡 / 最终报告（markdown）。
 */

const props = defineProps<{
  turn: TurnUiData
}>()

const emit = defineEmits<{
  (e: 'approve', value: string): void
  (e: 'reject'): void
}>()

const open = ref(true)

/**
 * 思考面板的展开态：默认跟随流式状态（推理中展开、报告后折叠），
 * 但用户手动点过头之后以手动选择为准，否则折叠按钮点了没反应、历史思考再也打不开。
 */
const reasoningOpen = ref<Record<string, boolean>>({})

function isReasoningOpen(item: Extract<TurnUiData['items'][number], { type: 'reasoning' }>): boolean {
  const manual = reasoningOpen.value[item.data.key]
  return manual === undefined ? item.data.streaming : manual
}

function setReasoningOpen(key: string, value: boolean): void {
  reasoningOpen.value = { ...reasoningOpen.value, [key]: value }
}

const statusType = computed<'primary' | 'success' | 'danger' | 'warning' | 'info'>(() => {
  switch (props.turn.status) {
    case 'running':
      return 'primary'
    case 'completed':
      return 'success'
    case 'failed':
      return 'danger'
    case 'waiting':
      return 'warning'
    default:
      return 'info'
  }
})

const statusLabel = computed(() => {
  switch (props.turn.status) {
    case 'running':
      return '运行中'
    case 'completed':
      return '已完成'
    case 'failed':
      return '失败'
    case 'waiting':
      return '待处理'
    default:
      return props.turn.status
  }
})

const duration = computed(() => formatDuration(turnDurationMs(props.turn)))

function turnDurationMs(turn: TurnUiData): number | null {
  const start = turn.startedAt ? new Date(turn.startedAt).getTime() : NaN
  const end = turn.completedAt ? new Date(turn.completedAt).getTime() : NaN
  if (Number.isNaN(start)) return null
  return (Number.isNaN(end) ? Date.now() : end) - start
}

const tokenTotal = computed(() => {
  const total = props.turn.token?.total_tokens
  const prompt = props.turn.token?.prompt_tokens
  const completion = props.turn.token?.completion_tokens
  if (total !== undefined && total !== null) return total
  if (prompt === undefined && completion === undefined) return null
  return (prompt ?? 0) + (completion ?? 0)
})

const steps = computed<AgentStep[]>(() => {
  const turn = props.turn
  const reasoningItems = turn.items.filter((item) => item.type === 'reasoning')
  const toolItems = turn.items.filter((item) => item.type === 'tool')
  const reportItems = turn.items.filter((item) => item.type === 'report')

  const lastReasoning = reasoningItems[reasoningItems.length - 1]
  const lastReport = reportItems[reportItems.length - 1]
  const hasTool = toolItems.length > 0
  const toolRunning = toolItems.some((item) => item.data.status === 'running')
  const toolDone = toolItems.some((item) => item.data.status === 'done')
  const hasObservation = toolItems.some(
    (item) => item.data.status === 'done' && item.data.ok !== undefined && item.data.ok !== null,
  )

  const reportRunning = !!lastReport?.data.streaming
  const reportDone = !!lastReport?.data.done

  const result: AgentStep[] = [
    {
      key: 'reason',
      label: '思考',
      state: (lastReasoning
        ? lastReasoning.data.streaming
          ? 'active'
          : 'done'
        : turn.status === 'failed' || turn.status === 'completed'
          ? 'error'
          : 'pending') as StepState,
    },
    {
      key: 'tool',
      label: '工具',
      state: (toolRunning
        ? 'active'
        : toolDone
          ? 'done'
          : hasTool
            ? 'pending'
            : 'idle') as StepState,
    },
    {
      key: 'observe',
      label: '观察',
      state: (hasObservation
        ? 'done'
        : toolRunning
          ? 'active'
          : hasTool
            ? 'pending'
            : 'idle') as StepState,
    },
    {
      key: 'report',
      label: '报告',
      state: (reportRunning
        ? 'active'
        : reportDone
          ? 'done'
          : turn.status === 'failed'
            ? 'error'
            : 'pending') as StepState,
    },
  ]
  return result
})

function timeLabel(value?: string): string {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}
</script>

<template>
  <section class="turn" :class="turn.status">
    <header class="turn-head">
      <button type="button" class="turn-toggle" @click="open = !open" :aria-expanded="open">
        <el-icon class="turn-caret" :class="{ open }"><ArrowDown /></el-icon>
        <span class="turn-seq">{{ turn.seq }}</span>
        <span class="turn-input">{{ turn.input || '（历史会话）' }}</span>
      </button>
      <div class="turn-meta">
        <span v-if="turn.startedAt" class="turn-time">{{ timeLabel(turn.startedAt) }}</span>
        <span v-if="duration" class="turn-duration">{{ duration }}</span>
        <span v-if="tokenTotal !== null" class="turn-token" :title="`提示词 + 生成 tokens`"
          >{{ tokenTotal }} tokens</span
        >
        <el-tag size="small" effect="plain" :type="statusType" class="turn-status">{{
          statusLabel
        }}</el-tag>
      </div>
    </header>

    <div v-if="open" class="turn-body">
      <AgentStepper :steps="steps" />

      <div v-if="turn.error" class="turn-error">
        <el-icon><WarningFilled /></el-icon>
        <span>{{ turn.error }}</span>
      </div>

      <div class="turn-items">
        <template
          v-for="item in turn.items"
          :key="item.type === 'reasoning' || item.type === 'report' ? item.data.key : item.data.key"
        >
          <ReasoningPanel
            v-if="item.type === 'reasoning'"
            class="turn-item"
            :text="item.data.text"
            :streaming="item.data.streaming"
            :open="isReasoningOpen(item)"
            @update:open="setReasoningOpen(item.data.key, $event)"
          />
          <ToolCallCard v-else-if="item.type === 'tool'" class="turn-item" :tool="item.data" />
          <ApprovalCard
            v-else-if="item.type === 'approval'"
            class="turn-item"
            :approval="item.data"
            @approve="emit('approve', $event)"
            @reject="emit('reject')"
          />
          <article
            v-else-if="item.type === 'report'"
            class="turn-item report-block"
            :class="{ streaming: item.data.streaming }"
          >
            <div class="report-head">
              <span class="report-label">最终报告</span>
              <span v-if="item.data.streaming" class="report-hint">正在生成…</span>
            </div>
            <MarkdownView :text="item.data.text" />
          </article>
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.turn {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
}

.turn-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
}

.turn-toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  border: none;
  background: transparent;
  padding: 0;
  color: var(--app-text);
  font-family: inherit;
  cursor: pointer;
  text-align: left;
  flex: 1;
}

.turn-caret {
  flex: none;
  color: var(--app-muted);
  transition: transform 0.2s ease;
}

.turn-caret.open {
  transform: rotate(180deg);
}

.turn-seq {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 50%;
  background: var(--app-accent);
  color: #fff;
  font-size: 12px;
  font-weight: 600;
}

.turn.failed .turn-seq {
  background: var(--app-danger, #ef4444);
}

.turn-input {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 600;
  font-size: 13.5px;
}

.turn-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
}

.turn-time {
  font-size: 11px;
  color: var(--app-muted);
  font-variant-numeric: tabular-nums;
}

.turn-duration {
  font-size: 11px;
  color: var(--app-muted);
  font-variant-numeric: tabular-nums;
}

.turn-token {
  font-size: 11px;
  color: var(--app-accent);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.turn-status {
  text-transform: capitalize;
}

.turn-body {
  border-top: 1px solid var(--app-border);
  padding: 4px 14px 12px;
}

.turn-error {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-top: 8px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
  font-size: 12px;
}

.turn-items {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 8px;
}

.report-block {
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-left: 3px solid var(--app-accent);
  border-radius: var(--app-radius);
  padding: 8px 12px 10px;
  background: var(--app-bg);
}

.report-block.streaming {
  box-shadow: 0 0 0 1px var(--app-accent);
}

.report-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.report-label {
  font-weight: 600;
  font-size: 12px;
  color: var(--app-muted);
}

.report-hint {
  font-size: 11px;
  color: var(--app-accent);
  animation: report-pulse 1.1s ease-in-out infinite;
}

@keyframes report-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.4;
  }
}
</style>
