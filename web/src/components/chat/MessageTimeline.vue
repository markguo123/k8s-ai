<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'

import type { AgentEvent } from '@/api/sse'
import type { HistorySnapshot } from '@/api/sse'
import type { TurnUiData } from './turn'
import type { ToolUiData } from './ToolCallCard.vue'
import type { ApprovalUiData } from './ApprovalCard.vue'
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
  }>(),
  {
    canDecide: true,
  },
)

const emit = defineEmits<{
  (e: 'approve', value: string): void
  (e: 'reject'): void
}>()

const rollRef = ref<HTMLElement | null>(null)

/** 工具调用：从历史 tool 消息构建展示对象。 */
function toolFromHistoryMessage(
  message: { name?: string; ok?: boolean; data?: unknown; content?: string },
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
    risk: '',
    status: message.ok ? 'done' : 'error',
    ok: message.ok ?? false,
    params,
    result: message.data ?? message.content ?? null,
    raw,
    duration: null,
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
    duration: null,
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

    if (
      eventType === 'session.created' ||
      eventType === 'session.completed' ||
      eventType === 'session.failed'
    ) {
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
      if (typeof data.output === 'string' && data.output) {
        turn.items.push({
          type: 'report',
          data: { key: `${turn.key}:out`, text: data.output, streaming: false, done: true },
        })
      }
      turn.token = (data.token as TurnUiData['token']) ?? turn.token
      turn.completedAt = event.timestamp || turn.completedAt
      continue
    }

    if (eventType === 'reason.started' || eventType === 'reason.completed') {
      const turn = attachTurn(event)
      if (eventType === 'reason.started') {
        turn.items.push({
          type: 'reasoning',
          data: { key: `${turn.key}:reason:${turn.items.length}`, text: '', streaming: true, done: false },
        })
      } else {
        const item = [...turn.items]
          .reverse()
          .find((entry) => entry.type === 'reasoning' && entry.data.streaming)
        if (item && item.type === 'reasoning') {
          item.data.streaming = false
          item.data.done = true
        }
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
      const index = typeof data.index === 'number' ? data.index : 0

      if (role === 'reasoning') {
        const item = [...turn.items]
          .reverse()
          .find((entry) => entry.type === 'reasoning' && entry.data.streaming)
        if (item && item.type === 'reasoning') {
          if (delta) item.data.text = `${item.data.text}${delta}`
          if (eventType === 'message.completed') {
            item.data.streaming = false
            item.data.done = true
          }
        }
        continue
      }

      if (role === 'assistant') {
        const key = `${turn.key}:report:${index}`
        const existing = turn.items.find(
          (entry) => entry.type === 'report' && entry.data.key === key,
        )
        if (existing && existing.type === 'report') {
          if (eventType === 'message.completed') {
            if (delta) existing.data.text = delta
            existing.data.streaming = false
            existing.data.done = true
          } else if (delta) {
            existing.data.text = `${existing.data.text}${delta}`
          }
        } else {
          turn.items.push({
            type: 'report',
            data: {
              key,
              text: delta,
              streaming: eventType === 'message.delta',
              done: eventType === 'message.completed',
            },
          })
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

defineExpose({ turns })
</script>

<template>
  <div ref="rollRef" class="timeline">
    <template v-if="turns.length">
      <div v-for="turn in turns" :key="turn.key" class="turn-wrap">
        <TurnBlock
          :turn="turn"
          :can-decide="canDecide"
          @approve="emit('approve', $event)"
          @reject="emit('reject')"
        />
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
  gap: 12px;
  min-height: 0;
  padding: 16px;
  overflow-y: auto;
}

.turn-wrap {
  max-width: 820px;
  width: 100%;
  align-self: center;
}
</style>
