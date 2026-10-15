<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'

import type { AgentEvent } from '@/api/sse'
import type { HistorySnapshot } from '@/api/sse'
import type { TurnItem, TurnUiData } from './turn'
import type { ToolUiData } from './ToolCallCard.vue'
import type { ApprovalUiData } from './ApprovalCard.vue'
import TechEmptyState from '@/components/TechEmptyState.vue'
import TurnBlock from './TurnBlock.vue'
import { parseJsonLoose } from './render/constants'

/**
 * P2.6 MessageTimeline：以「回合 turn」为单位分组渲染。
 * - 历史快照按 user 消息切分出历史回合；实时统一事件按 turn_id 归入回合；
 * - 状态只吃事件本身（§8）：streaming/done 均由 message.delta/completed 等驱动；
 * - 旧事件（无 turn_id）兜底归入当前进行中的回合，避免硬失效；
 * - Secret 值永不进入时间线（仅展示脱敏指纹，值由后端统一脱敏）。
 */

const props = withDefaults(
  defineProps<{
    history: HistorySnapshot | null
    events: AgentEvent[]
    /** P2.9 当前用户是否有权审批：false 时审批卡收起操作区（后端仍强校验）。 */
    canDecide?: boolean
    /** P7.6 空态文案：由外层按「是否已选会话」给出，避免已选会话却提示"选择会话"。 */
    emptyTitle?: string
    emptyHint?: string
  }>(),
  {
    canDecide: true,
    emptyTitle: '选择或新建一个会话',
    emptyHint: '提问后助手会先思考、再调用工具核实现场，最后给出可复核的报告',
  },
)

const emit = defineEmits<{
  (e: 'approve', value: string): void
  (e: 'reject'): void
}>()

const rollRef = ref<HTMLElement | null>(null)

/** 工具调用：从历史 tool 消息构建展示对象。 */
function toolFromHistoryMessage(
  message: {
    name?: string
    ok?: boolean
    data?: unknown
    content?: string
    summary?: string
    displayType?: string
    durationMs?: number
    risk?: string
  },
  key: string,
): ToolUiData {
  const name = message.name ?? '工具调用'
  const raw = message.content ?? ''
  const params = message.data ?? null
  const command = summarizeCommand(name, params)
  return {
    key,
    name,
    shortName: shortNameOf(name, command),
    command,
    risk: message.risk ?? '',
    status: message.ok ? 'done' : 'error',
    ok: message.ok ?? false,
    params,
    result: message.data ?? message.content ?? null,
    raw,
    duration: typeof message.durationMs === 'number' ? message.durationMs : null,
    summary: message.summary,
    displayType: message.displayType,
    createResource: detectCreateResource(name, message.data ?? null, message.ok ?? false),
  }
}

/** 解析统一事件里的 StepData（reason/tool.*），转成工具展示对象。 */
function toolFromEvent(event: AgentEvent, key: string, data: Record<string, unknown>): ToolUiData {
  const name = typeof data.tool === 'string' && data.tool ? data.tool : '工具调用'
  const status: ToolUiData['status'] =
    event.event_type === 'tool.completed' ? (data.error ? 'error' : 'done') : 'running'
  const ok = data.status === 'ok' || (data.error === undefined && status === 'done')
  const paramsRaw = typeof data.params === 'string' ? data.params : ''
  const params = (() => {
    const parsed = parseJsonLoose(paramsRaw)
    return parsed !== paramsRaw ? parsed : data.params
  })()
  const command = summarizeCommand(name, params)
  return {
    key,
    name,
    shortName: shortNameOf(name, command),
    command,
    risk: typeof data.risk === 'string' ? data.risk : '',
    status,
    ok,
    params,
    result:
      data.result !== undefined && data.result !== null
        ? data.result
        : typeof data.error === 'string'
          ? data.error
          : null,
    raw: (typeof data.result === 'string' ? data.result : '') || paramsRaw,
    duration: typeof data.durationMs === 'number' ? data.durationMs : null,
    summary: typeof data.summary === 'string' ? data.summary : undefined,
    displayType: typeof data.displayType === 'string' ? data.displayType : undefined,
    error: typeof data.error === 'string' ? data.error : undefined,
    createResource: detectCreateResource(name, params, ok),
  }
}

function detectCreateResource(
  name: string,
  params: unknown,
  ok: boolean,
): ToolUiData['createResource'] {
  if (!/create_resource|create-resource/.test(name)) {
    return null
  }
  const record = params && typeof params === 'object' ? (params as Record<string, unknown>) : {}
  const manifest =
    (typeof record.manifest === 'string' && record.manifest) ||
    (typeof record.command === 'string' && record.command) ||
    ''
  const status = typeof record.status === 'string' ? record.status : ''
  return {
    manifest,
    dryRun: /dry.?run/i.test(status) || manifest.length > 0,
    validated: ok || /validated|ready/i.test(status),
    applied: ok || /applied/.test(status),
  }
}

function summarizeCommand(tool: string, params: unknown): string {
  if (typeof params === 'string') {
    const trimmed = params.trim()
    if (/^\s*kubectl\b/.test(trimmed) || /\s/.test(trimmed)) {
      return trimmed.slice(0, 160)
    }
    const record = parseJsonLoose(trimmed)
    if (record !== trimmed && record && typeof record === 'object') {
      return summarizeRecord(tool, record as Record<string, unknown>)
    }
    return ''
  }
  if (params && typeof params === 'object' && !Array.isArray(params)) {
    return summarizeRecord(tool, params as Record<string, unknown>)
  }
  return ''
}

function summarizeRecord(tool: string, record: Record<string, unknown>): string {
  for (const key of ['command', 'cmd', 'exec']) {
    const value = record[key]
    if (typeof value === 'string' && value) {
      return value.slice(0, 160)
    }
  }
  const argv =
    (Array.isArray(record['argv']) && record['argv']) ||
    (Array.isArray(record['args']) && record['args'])
  if (argv) {
    return (argv as unknown[]).map(String).join(' ').slice(0, 160)
  }
  if (typeof record['namespace'] === 'string' && record['namespace']) {
    return `${tool} --namespace ${record['namespace']}`
  }
  return ''
}

function shortNameOf(tool: string, command: string): string {
  const match = command.trim().match(/\b(?:kubectl|helm|k8sai|kubescape)\s+([\w-]+)/i)
  if (match) {
    return match[1]
  }
  if (command.trim()) {
    return command.trim().split(/\s+/)[0].slice(0, 12)
  }
  const parts = tool.split(/[.\-_:]/).filter(Boolean)
  return (parts[parts.length - 1] || tool).slice(0, 12)
}

/** 历史快照（已落库消息）→ 按 user 消息切分的回合列表。 */
function buildHistoryTurns(): TurnUiData[] {
  const messages = props.history?.messages ?? []
  const list: TurnUiData[] = []
  let current: TurnUiData | null = null
  let seq = 0

  for (let i = 0; i < messages.length; i += 1) {
    const message = messages[i]
    const base = `hist:${i}`
    if (message.role === 'user') {
      seq += 1
      current = {
        key: base,
        seq,
        input: message.content ?? '',
        status: 'completed',
        startedAt: message.timestamp || undefined,
        completedAt: message.timestamp || undefined,
        items: [],
      }
      list.push(current)
      continue
    }
    if (!current) {
      // 无 user 开头的孤立消息：为其生成独立回合（保留上下文）。
      seq += 1
      current = {
        key: base,
        seq,
        input: '',
        status: message.role === 'tool' && message.ok === false ? 'failed' : 'completed',
        startedAt: message.timestamp || undefined,
        completedAt: message.timestamp || undefined,
        items: [],
      }
      list.push(current)
    }
    if (message.role === 'tool') {
      current.items.push({ type: 'tool', data: toolFromHistoryMessage(message, `${base}:tool`) })
      continue
    }
    if (message.type === 'reasoning') {
      current.items.push({
        type: 'reasoning',
        data: { key: `${base}:reasoning`, text: message.content ?? '', streaming: false, done: true },
      })
      continue
    }
    if (message.type === 'error') {
      current.status = 'failed'
      current.error = message.content || '会话执行失败'
      current.items.push({
        type: 'report',
        data: { key: `${base}:error`, text: message.content ?? '', streaming: false, done: true },
      })
      continue
    }
    // assistant text（最终报告）
    const index = current.items.filter((item) => item.type === 'report').length
    current.items.push({
      type: 'report',
      data: { key: `${base}:report:${index}`, text: message.content ?? '', streaming: false, done: true },
    })
  }

  return list
}

/** 本回合最近一个仍在流式的思考面板（没有则返回 null）。 */
function streamingReasoningOf(turn: TurnUiData): TurnItem | null {
  for (let i = turn.items.length - 1; i >= 0; i -= 1) {
    const entry = turn.items[i]
    if (entry.type === 'reasoning' && entry.data.streaming) {
      return entry
    }
  }
  return null
}

/**
 * 回合唯一的报告块。
 *
 * message.delta 的 index 只是同一 role 内的分片序号，不能参与 key：一旦参与，
 * 每个分片都会新建一块，报告被渲染成事件平铺（PRD §8：展示以事件为准，
 * 不以分片为单位铺开），也与设计方案 §4.2「回合即叙事」冲突。
 */
function reportOf(turn: TurnUiData, create: boolean): TurnItem | null {
  const key = `${turn.key}:report`
  for (let i = turn.items.length - 1; i >= 0; i -= 1) {
    const entry = turn.items[i]
    if (entry.type === 'report' && entry.data.key === key) {
      return entry
    }
  }
  if (!create) {
    return null
  }
  const item: TurnItem = {
    type: 'report',
    data: { key, text: '', streaming: true, done: false },
  }
  turn.items.push(item)
  return item
}

/** 思考面板：无内容不占位（设计方案 §5.2）。 */
function dropEmptyReasoning(turn: TurnUiData): void {
  const kept = turn.items.filter(
    (entry) => !(entry.type === 'reasoning' && entry.data.done && entry.data.text.trim() === ''),
  )
  if (kept.length !== turn.items.length) {
    turn.items = kept
  }
}

const turns = computed<TurnUiData[]>(() => {
  const list = buildHistoryTurns()
  const byKey = new Map<string, TurnUiData>()
  for (const turn of list) {
    byKey.set(turn.key, turn)
  }

  let seq = list.length
  let live: TurnUiData | null = null

  const attachTurn = (event: AgentEvent): TurnUiData => {
    const turnId = typeof event.turn_id === 'string' && event.turn_id ? event.turn_id : undefined
    if (turnId) {
      const existing = byKey.get(turnId)
      if (existing) {
        live = existing
        return existing
      }
      seq += 1
      const turn: TurnUiData = {
        key: turnId,
        seq,
        input: '',
        status: 'running',
        startedAt: event.timestamp || undefined,
        items: [],
      }
      list.push(turn)
      byKey.set(turnId, turn)
      live = turn
      return turn
    }
    if (!live) {
      seq += 1
      const key = `live:${seq}`
      const turn: TurnUiData = {
        key,
        seq,
        input: '',
        status: 'waiting',
        startedAt: event.timestamp || undefined,
        items: [],
      }
      list.push(turn)
      byKey.set(key, turn)
      live = turn
    }
    return live
  }

  for (const event of props.events ?? []) {
    const data = (event.data ?? {}) as Record<string, unknown>
    const eventType = event.event_type

    if (eventType === 'session.created') {
      continue
    }

    if (eventType === 'session.completed' || eventType === 'session.failed') {
      // 会话终态兜底：收口仍在流式的报告/思考，避免终态后残留“正在生成…”。
      for (const turn of list) {
        for (const entry of turn.items) {
          if ((entry.type === 'report' || entry.type === 'reasoning') && entry.data.streaming) {
            entry.data.streaming = false
            entry.data.done = true
          }
        }
        dropEmptyReasoning(turn)
      }
      continue
    }

    if (eventType === 'turn.started') {
      const turn = attachTurn(event)
      const input = typeof data.input === 'string' ? data.input : ''
      if (input) {
        turn.input = input
      }
      turn.status = 'running'
      turn.startedAt = turn.startedAt || event.timestamp
      turn.token = (data.token as TurnUiData['token']) ?? turn.token
      continue
    }

    if (eventType === 'turn.completed') {
      const turn = attachTurn(event)
      turn.status = data.error ? 'failed' : 'completed'
      turn.error = typeof data.error === 'string' ? data.error : turn.error
      // turn.completed.output 只作兜底：本回合还没有报告正文时用它填充。
      // 不再单独成块，否则会与 message.completed 形成两份同样的报告。
      const output = typeof data.output === 'string' ? data.output : ''
      const report = reportOf(turn, output !== '')
      if (report && report.type === 'report') {
        if (!report.data.text && output) {
          report.data.text = output
        }
        // 回合终态即报告终态：迟到分片会被 done 标记忽略，message.completed 仍可覆盖全文。
        report.data.streaming = false
        report.data.done = true
      }
      turn.token = (data.token as TurnUiData['token']) ?? turn.token
      turn.completedAt = event.timestamp || turn.completedAt
      continue
    }

    if (eventType === 'reason.started' || eventType === 'reason.completed') {
      const turn = attachTurn(event)
      if (eventType === 'reason.started') {
        // 增量可能先于 reason.started 到达：已有流式面板就沿用，
        // 否则同一个 Step 会出现"有正文的面板 + 空面板"两个。
        if (!streamingReasoningOf(turn)) {
          turn.items.push({
            type: 'reasoning',
            data: { key: `${turn.key}:reason:${turn.items.length}`, text: '', streaming: true, done: false },
          })
        }
      } else {
        const item = streamingReasoningOf(turn)
        if (item && item.type === 'reasoning') {
          item.data.streaming = false
          item.data.done = true
        }
        // 本步没有产出任何思考内容时不占位（设计方案 §5.2）。
        dropEmptyReasoning(turn)
      }
      continue
    }

    if (eventType === 'tool.started') {
      const turn = attachTurn(event)
      turn.items.push({
        type: 'tool',
        data: toolFromEvent(event, `${turn.key}:tool:${turn.items.length}`, data),
      })
      continue
    }

    if (eventType === 'tool.completed' || eventType === 'observation.created') {
      const turn = attachTurn(event)
      const toolName = typeof data.tool === 'string' && data.tool ? data.tool : ''
      const item = [...turn.items]
        .reverse()
        .find(
          (entry) =>
            entry.type === 'tool' &&
            (!toolName || entry.data.name === toolName || entry.data.shortName === toolName),
        )
      if (item && item.type === 'tool') {
        if (eventType === 'tool.completed') {
          item.data.status = data.error ? 'error' : 'done'
          item.data.ok = item.data.status === 'done'
          item.data.error = typeof data.error === 'string' ? data.error : undefined
          if (data.result !== undefined && data.result !== null) {
            item.data.result = data.result
          }
        } else if (data.result !== undefined && data.result !== null) {
          item.data.result = data.result
          item.data.ok = item.data.ok || data.ok === true
          if (data.ok === true) {
            item.data.status = 'done'
          }
        }
        // P7.4：observation.created 携带后端声明的完整 tool_result（summary/形态/耗时/风险/结构化 data），
        // chat 与刷新共用同一份头部信息与 body 结构。
        if (typeof data.summary === 'string' && data.summary) {
          item.data.summary = data.summary
        }
        if (typeof data.displayType === 'string' && data.displayType) {
          item.data.displayType = data.displayType
        }
        if (typeof data.durationMs === 'number') {
          item.data.duration = data.durationMs
        }
        if (typeof data.risk === 'string' && data.risk) {
          item.data.risk = data.risk
        }
        if (data.data !== undefined && data.data !== null) {
          item.data.result = data.data
        }
      }
      continue
    }

    if (
      eventType === 'approval.required' ||
      eventType === 'approval.approved' ||
      eventType === 'approval.rejected'
    ) {
      const turn = attachTurn(event)
      if (eventType === 'approval.required') {
        const options = Array.isArray(data.options)
          ? (data.options as Array<{ label: string; value: string }>)
          : []
        const approval: ApprovalUiData = {
          key: `${turn.key}:approval:${turn.items.length}`,
          prompt: typeof data.prompt === 'string' ? data.prompt : '需要你的审批确认',
          options,
          secret: data.secret === true,
          decision: /auto/i.test((data.decision as string) || '') ? 'auto' : 'pending',
          tool: typeof data.tool === 'string' ? data.tool : '',
          risk: typeof data.risk === 'string' ? data.risk : '',
          summary: typeof data.params === 'string' ? data.params.slice(0, 300) : '',
          resource: data.resource ?? null,
          reason: typeof data.reason === 'string' ? data.reason : '',
        }
        turn.items.push({ type: 'approval', data: approval })
      } else {
        const target = [...turn.items]
          .reverse()
          .find((entry) => entry.type === 'approval' && entry.data.decision === 'pending')
        if (target && target.type === 'approval') {
          target.data.decision = eventType === 'approval.approved' ? 'approved' : 'rejected'
        }
      }
      continue
    }

    if (eventType === 'message.delta' || eventType === 'message.completed') {
      const turn = attachTurn(event)
      const role = typeof data.role === 'string' && data.role ? data.role : 'assistant'
      const delta = typeof data.delta === 'string' ? data.delta : ''
      const authoritative = eventType === 'message.completed'

      if (role === 'reasoning') {
        // 思考增量与 reason.started 的到达顺序由后端决定（step 在 LLM 回合之后才创建）：
        // 这里按增量懒建面板，已有流式面板就续写，避免 delta 早于 started 时被整体丢弃。
        let item = streamingReasoningOf(turn)
        if (!item && (!authoritative || delta)) {
          const created: TurnItem = {
            type: 'reasoning',
            data: {
              key: `${turn.key}:reason:${turn.items.length}`,
              text: '',
              streaming: true,
              done: false,
            },
          }
          turn.items.push(created)
          item = created
        }
        if (item && item.type === 'reasoning') {
          if (delta) {
            item.data.text = authoritative ? delta : `${item.data.text}${delta}`
          }
          if (authoritative) {
            item.data.streaming = false
            item.data.done = true
            dropEmptyReasoning(turn)
          }
        }
        continue
      }

      if (role === 'assistant') {
        // 一个回合只有一块最终报告，分片的先后与到达时机都不改变块的数量。
        const item = reportOf(turn, !authoritative || delta !== '')
        if (item && item.type === 'report') {
          if (authoritative) {
            // message.completed 携带权威全文：整体覆盖，而不是把全文再拼一次。
            if (delta) {
              item.data.text = delta
            }
            item.data.streaming = false
            item.data.done = true
          } else if (item.data.done) {
            // 终态之后的迟到分片：忽略，避免同一份报告被追加成两份。
          } else if (delta) {
            item.data.text = `${item.data.text}${delta}`
            item.data.streaming = true
          }
        }
      }
      continue
    }
  }

  // 收尾兜底：运行中回合有最终报告且无活动态时，稳定为完成（仅当 turn.completed 缺失时）。
  for (const turn of list) {
    if (turn.status === 'running') {
      const hasFinalReport = turn.items.some((entry) => entry.type === 'report' && entry.data.done)
      if (hasFinalReport) {
        turn.status = 'completed'
        turn.completedAt = turn.completedAt || turn.startedAt
      }
    }
  }

  return list
})

/**
 * P7.2 智能跟随：默认跟随最新输出；用户上滑离开底部（阈值 24px）后暂停跟随，
 * 出现"有新内容，回到底部"按钮；点按钮或滚回底部后恢复跟随。
 */
const FOLLOW_THRESHOLD_PX = 24
const following = ref(true)

function distanceToBottom(el: HTMLElement): number {
  return el.scrollHeight - el.scrollTop - el.clientHeight
}

function onScroll(): void {
  const el = rollRef.value
  if (!el) {
    return
  }
  following.value = distanceToBottom(el) <= FOLLOW_THRESHOLD_PX
}

/** 追加行后滚动到底部（仅在跟随状态下，避免打断用户向上翻阅）。 */
function scrollToBottom(): void {
  if (!following.value) {
    return
  }
  void nextTick(() => {
    if (rollRef.value) {
      rollRef.value.scrollTop = rollRef.value.scrollHeight
    }
  })
}

/** 点"回到底部"：恢复跟随并滚到最新。 */
function resumeFollow(): void {
  following.value = true
  void nextTick(() => {
    rollRef.value?.scrollTo({ top: rollRef.value.scrollHeight, behavior: 'smooth' })
  })
}

watch(
  () => [props.history, props.events?.length] as const,
  () => scrollToBottom(),
  { deep: false },
)

defineExpose({ turns })
</script>

<template>
  <div ref="rollRef" class="timeline" @scroll.passive="onScroll">
    <template v-if="turns.length">
      <div v-for="turn in turns" :key="turn.key" class="turn-wrap rise-in">
        <TurnBlock
          :turn="turn"
          :can-decide="canDecide"
          @approve="emit('approve', $event)"
          @reject="emit('reject')"
        />
      </div>
    </template>
    <TechEmptyState
      v-else
      :title="props.emptyTitle"
      :hint="props.emptyHint"
    />
    <button v-if="!following && turns.length" type="button" class="follow-latest" @click="resumeFollow">
      有新内容，回到底部
    </button>
  </div>
</template>

<style scoped>
.timeline {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 12px;
  min-height: 0;
  padding: 16px;
  overflow-y: auto;
  position: relative;
}

/* P7.2：暂停跟随时浮出的"回到底部"，不遮挡内容（贴底居中、半透明） */
.follow-latest {
  position: sticky;
  bottom: 4px;
  align-self: center;
  margin-top: -8px;
  padding: 6px 14px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-surface-2);
  color: var(--app-text);
  box-shadow: var(--app-shadow);
  font-size: 12.5px;
  cursor: pointer;
}

.follow-latest:hover {
  border-color: var(--app-accent);
  color: var(--app-accent);
}

.turn-wrap {
  max-width: 820px;
  width: 100%;
  align-self: center;
}

</style>
