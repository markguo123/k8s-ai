<!--
  P2.6 MessageTimeline：统一渲染历史快照与实时 SSE 归一事件。

  关键约定（对齐 PRD §8「最终报告与流式增量一致、不直接猜状态」）：
  - 消息文本只由事件驱动拼接（message.delta 追加、message.completed 收敛全文），
    不根据时间/倒计时猜测完成态；
  - message.delta 按 turn+role 聚合成单个消息块，流式中保持开启；message.completed
    以事件携带的全文覆盖并标记完成，状态只吃事件本身；
  - 统一事件携带 turn/step id；旧事件映射后 id 为空——同一 role 已有 turn 级流式
    块时跳过旧事件增量，避免 legacy 全文与 stream 增量重复累加。
-->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { Loading, Lock, Tools, View } from '@element-plus/icons-vue'

import type { AgentEvent } from '@/api/sse'
import type { HistorySnapshot } from '@/api/sse'

const props = defineProps<{
  history: HistorySnapshot | null
  events: AgentEvent[]
}>()

const rollRef = ref<HTMLElement | null>(null)

/** 消息块：流式增量聚合 + message.completed 收敛后的展示形态。 */
interface MessageBlock {
  key: string
  role: string
  text: string
  streaming: boolean
  completed: boolean
  time: string
}

/** 时间线条目：消息块或需要单独展示的事件（tool/approval/turn/session 等）。 */
type TimelineRow = { kind: 'message'; block: MessageBlock } | { kind: 'event'; key: string; event: AgentEvent }

/** 事件展示侧栏小标签。 */
const STAGE_LABEL: Record<string, string> = {
  'session.created': '会话',
  'turn.started': '回合',
  'turn.completed': '回合完成',
  'reason.started': '分析',
  'reason.completed': '分析完成',
  'tool.started': '工具',
  'tool.completed': '工具完成',
  'observation.created': '结果',
  'approval.required': '审批',
  'approval.approved': '已通过',
  'approval.rejected': '已拒绝',
  'session.completed': '结束',
  'session.failed': '失败',
}

function eventLabel(event: AgentEvent): string {
  return STAGE_LABEL[event.event_type] ?? event.event_type
}

/** 单事件概览：只展示非敏感字段，Secret 值永不进入时间线。 */
function eventSummary(event: AgentEvent): string {
  const data = (event.data ?? {}) as Record<string, unknown>
  const parts: string[] = []
  if (typeof data.tool === 'string' && data.tool) parts.push(data.tool)
  if (typeof data.risk === 'string' && data.risk) parts.push(data.risk)
  if (typeof data.status === 'string' && data.status) parts.push(data.status)
  if (typeof data.decision === 'string' && data.decision) parts.push(data.decision)
  if (typeof data.name === 'string' && data.name) parts.push(data.name)
  if (typeof data.state === 'string' && data.state) parts.push(data.state)
  if (typeof data.result === 'string' && data.result) parts.push(data.result.slice(0, 140))
  if (typeof data.reason === 'string' && data.reason) parts.push(data.reason.slice(0, 140))
  return parts.join(' · ')
}

function timeLabel(value?: string): string {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/**
 * 把历史快照消息 + 实时事件折叠为统一时间线。
 * 归一旦遇到 message.delta/message.completed 时按 turn:role 聚合/收敛；
 * 旧事件（无 turn_id）仅在该 role 尚无 turn 级块时按 raw 兜底，避免重复累加。
 */
const rows = computed<TimelineRow[]>(() => {
  const out: TimelineRow[] = []
  const blocks = new Map<string, MessageBlock>()
  const turnRoles = new Set<string>()
  const rawRoles = new Set<string>()

  const roleOf = (role: unknown): string => (typeof role === 'string' && role ? role : 'assistant')
  const deltaOf = (delta: unknown): string => (typeof delta === 'string' ? delta : '')
  const keyFor = (turnId: string | undefined, role: string): string =>
    turnId ? `${turnId}:${role}` : `raw:${role}`

  const hasTurnBlock = (role: string): boolean =>
    Array.from(turnRoles).some((key) => key.endsWith(`:${role}`))

  const pushMessage = (key: string, role: string, text: string, streaming: boolean, completed: boolean, time: string): void => {
    let block = blocks.get(key)
    if (!block) {
      block = { key, role, text, streaming, completed, time }
      blocks.set(key, block)
      out.push({ kind: 'message', block })
      return
    }
    if (text) block.text = `${block.text}${text}`
    if (completed) {
      block.completed = true
      block.streaming = false
    }
    if (!block.time && time) block.time = time
  }

  // 历史快照（已落库消息）统一视为完成态，作为时间线起点。
  const historyMessages = props.history?.messages ?? []
  for (let i = 0; i < historyMessages.length; i += 1) {
    const m = historyMessages[i]
    out.push({
      kind: 'message',
      block: {
        key: `history:${m.timestamp ?? ''}:${i}`,
        role: m.role || 'assistant',
        text: m.content ?? '',
        streaming: false,
        completed: true,
        time: m.timestamp ?? '',
      },
    })
  }

  for (const event of props.events ?? []) {
    const data = (event.data ?? {}) as Record<string, unknown>
    const turnId = typeof event.turn_id === 'string' ? event.turn_id : undefined

    if (event.event_type === 'message.delta') {
      const role = roleOf(data.role)
      const delta = deltaOf(data.delta)
      if (!delta) continue
      if (turnId) {
        const key = keyFor(turnId, role)
        turnRoles.add(key)
        pushMessage(key, role, delta, true, false, event.timestamp ?? '')
      } else if (!hasTurnBlock(role)) {
        // 旧事件兜底：仅当该 role 尚无 turn 级流式块时采用。
        const key = keyFor(undefined, role)
        if (!rawRoles.has(key)) {
          rawRoles.add(key)
          pushMessage(key, role, delta, true, false, event.timestamp ?? '')
        }
      }
      continue
    }

    if (event.event_type === 'message.completed') {
      const role = roleOf(data.role)
      const finalText = deltaOf(data.delta)
      if (turnId) {
        const key = keyFor(turnId, role)
        turnRoles.add(key)
        if (blocks.has(key)) {
          const block = blocks.get(key) as MessageBlock
          if (finalText) block.text = finalText
          block.streaming = false
          block.completed = true
          if (!block.time) block.time = event.timestamp ?? ''
        } else {
          pushMessage(key, role, finalText, false, true, event.timestamp ?? '')
        }
      } else if (!hasTurnBlock(role)) {
        const key = keyFor(undefined, role)
        if (blocks.has(key)) {
          const block = blocks.get(key) as MessageBlock
          if (finalText) block.text = finalText
          block.streaming = false
          block.completed = true
          if (!block.time) block.time = event.timestamp ?? ''
        } else {
          pushMessage(key, role, finalText, false, true, event.timestamp ?? '')
        }
      }
      continue
    }

    out.push({ kind: 'event', key: `${event.step_id ?? ''}:${event.event_type}:${out.length}`, event })
  }

  return out
})

/** 追加行后滚动到底部（保持最新消息可见）。 */
function scrollToBottom(): void {
  void nextTick(() => {
    if (rollRef.value) {
      rollRef.value.scrollTop = rollRef.value.scrollHeight
    }
  })
}

watch(
  () => [props.history, props.events?.length] as const,
  () => scrollToBottom(),
  { deep: false },
)

defineExpose({ rows })
</script>

<template>
  <div ref="rollRef" class="timeline">
    <template v-if="rows.length">
      <div v-for="row in rows" :key="row.kind === 'message' ? row.block.key : row.key" class="timeline-row">
        <!-- 消息块：按 role 区分左右对齐与底色 -->
        <article v-if="row.kind === 'message'" class="message" :class="[row.block.role, { streaming: row.block.streaming }]">
          <div class="message-head">
            <span class="message-role">{{ row.block.role }}</span>
            <span v-if="row.block.streaming" class="message-state"><el-icon class="spin"><Loading /></el-icon>输出中</span>
            <span v-else-if="row.block.completed" class="message-state done">已完成</span>
            <time v-if="row.block.time" class="message-time">{{ timeLabel(row.block.time) }}</time>
          </div>
          <p class="message-body">{{ row.block.text || (row.block.streaming ? '等待输出…' : '') }}</p>
        </article>

        <!-- 事件条目：工具/审批/回合等过程事件 -->
        <article v-else class="event">
          <el-icon class="event-icon"><Lock v-if="row.event.event_type.startsWith('approval')" /><Tools v-else-if="row.event.event_type.endsWith('.started') || row.event.event_type === 'observation.created'" /><View v-else /></el-icon>
          <div class="event-main">
            <span class="event-label">{{ eventLabel(row.event) }}</span>
            <span v-if="eventSummary(row.event)" class="event-summary">{{ eventSummary(row.event) }}</span>
          </div>
          <time v-if="row.event.timestamp" class="event-time">{{ timeLabel(row.event.timestamp) }}</time>
        </article>
      </div>
    </template>
    <el-empty v-else description="暂无消息，开始对话后将在此展示" :image-size="72" />
  </div>
</template>

<style scoped>
.timeline {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
  padding: 16px;
  overflow-y: auto;
}

.timeline-row {
  display: flex;
  flex-direction: column;
}

.message {
  max-width: 760px;
  align-self: flex-start;
  min-width: 120px;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
}

.message.user {
  align-self: flex-end;
  background: var(--app-accent-soft);
  border-color: var(--app-accent);
}

.message.streaming {
  border-color: var(--app-accent);
  box-shadow: 0 0 0 1px var(--app-accent);
}

.message-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.message-role {
  font-size: 11px;
  font-weight: 600;
  text-transform: capitalize;
  color: var(--app-muted);
}

.message.user .message-role {
  color: var(--app-accent);
}

.message-state {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  color: var(--app-accent);
}

.message-state.done {
  color: var(--app-success, #67c23a);
}

.message-state .spin {
  animation: rotate 1s linear infinite;
}

.message-time,
.event-time {
  margin-left: auto;
  font-size: 11px;
  color: var(--app-muted);
  font-variant-numeric: tabular-nums;
}

.message-body {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.55;
}

.event {
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 760px;
  align-self: flex-start;
  padding: 6px 10px;
  border: 1px dashed var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-bg);
}

.event-icon {
  color: var(--app-muted);
  flex: none;
}

.event-main {
  display: flex;
  align-items: baseline;
  gap: 8px;
  min-width: 0;
}

.event-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--app-strong, #1f2937);
  flex: none;
}

.event-summary {
  font-size: 12px;
  color: var(--app-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@keyframes rotate {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}
</style>
