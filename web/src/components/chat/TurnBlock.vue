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

const props = withDefaults(
  defineProps<{
    turn: TurnUiData
    /** P2.9 当前用户是否有权审批（本人自确认）。 */
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

const open = ref(true)
// P7.4：展开/折叠全部工具卡（数值信号驱动子卡片），报告完成后未手动操作过的卡片自动折叠。
const collapseAllSignal = ref(0)
const expandAllSignal = ref(0)

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

const reportDone = computed(() =>
  props.turn.items.some((item) => item.type === 'report' && item.data.done),
)

const hasToolCards = computed(() => props.turn.items.some((item) => item.type === 'tool'))

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

/**
 * P7.2 Run Bar：给回合内条目生成稳定锚点 id，并把步进器步骤映射到对应条目，
 * 点击步骤即可滚动定位（配合 CSS 的 scroll-margin-top 避让 sticky 顶栏）。
 */
function itemAnchor(item: { data: { key: string } }): string {
  return `step-${props.turn.key}-${item.data.key}`
}

const navSteps = computed<AgentStep[]>(() => {
  const lastOf = (type: 'reasoning' | 'tool' | 'report') =>
    [...props.turn.items].reverse().find((item) => item.type === type)
  const anchors: Record<string, ReturnType<typeof lastOf>> = {
    reason: lastOf('reasoning'),
    tool: lastOf('tool'),
    observe: lastOf('tool'),
    report: lastOf('report'),
  }
  return steps.value.map((step) => {
    const target = anchors[step.key]
    return { ...step, anchor: target ? itemAnchor(target) : undefined }
  })
})

function locateStep(step: AgentStep): void {
  if (!step.anchor) {
    return
  }
  document.getElementById(step.anchor)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
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
      <!-- 用户输入：与原型一致，回合内先回显用户诉求，再展示执行过程 -->
      <article v-if="turn.input" class="turn-question">
        <span class="question-role">你</span>
        <p class="question-text">{{ turn.input }}</p>
      </article>

      <AgentStepper :steps="navSteps" @select="locateStep" />

      <div v-if="hasToolCards" class="turn-tools">
        <button type="button" class="turn-tools-btn" @click="expandAllSignal += 1">
          展开全部
        </button>
        <button type="button" class="turn-tools-btn" @click="collapseAllSignal += 1">
          折叠全部
        </button>
      </div>

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
            :id="itemAnchor(item)"
            class="turn-item"
            :text="item.data.text"
            :streaming="item.data.streaming"
          />
          <ToolCallCard
            v-else-if="item.type === 'tool'"
            :id="itemAnchor(item)"
            class="turn-item"
            :tool="item.data"
            :report-done="reportDone"
            :collapse-all="collapseAllSignal"
            :expand-all="expandAllSignal"
          />
          <ApprovalCard
            v-else-if="item.type === 'approval'"
            class="turn-item"
            :approval="item.data"
            :can-decide="canDecide"
            @approve="emit('approve', $event)"
            @reject="emit('reject')"
          />
          <article
            v-else-if="item.type === 'report'"
            :id="itemAnchor(item)"
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
  /* 对齐原型：回合本身是透明的时间线分组（卡片感由内部工具卡/报告块提供） */
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.turn-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 6px 2px;
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
  color: var(--app-faint);
  font-family: var(--app-mono);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.5px;
}

.turn-seq::before {
  content: 'T';
}

.turn-input {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 650;
  font-size: 13px;
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
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}

.turn-question {
  /* 对齐原型 .msg.user：accent 描边 + accent-soft 底色，右对齐 */
  align-self: flex-end;
  max-width: 820px;
  padding: 11px 14px;
  border: 1px solid var(--app-accent);
  border-radius: var(--app-radius);
  background: var(--app-accent-soft);
}

.question-role {
  color: var(--app-faint);
  font-size: 11px;
}

.question-text {
  margin: 0;
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.turn-error {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 7px 10px;
  border-radius: 6px;
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 12px;
}

.turn-tools {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 2px 0;
}

.turn-tools-btn {
  padding: 2px 10px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: transparent;
  color: var(--app-muted);
  font-size: 11.5px;
  cursor: pointer;
}

.turn-tools-btn:hover {
  border-color: var(--app-accent);
  color: var(--app-accent);
}

.turn-items {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.report-block {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  padding: 16px 20px;
  background: var(--app-surface);
  font-size: 13.5px;
  line-height: 1.65;
}

.report-block.streaming {
  border-color: var(--app-accent);
}

.report-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.report-label {
  font-weight: 650;
  font-size: 11.5px;
  color: var(--app-muted);
  letter-spacing: 0.3px;
  text-transform: uppercase;
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

/* P7.2：Run Bar 定位时避开 sticky 顶栏 */
.turn-item {
  scroll-margin-top: 56px;
}
</style>
