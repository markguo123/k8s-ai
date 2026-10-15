/**
 * P2.5 SSE 客户端与事件归一。
 *
 * 传输层：基于 fetch 的 SSE 读取器（兼容 `event:`/`data:` 帧与 CRLF），
 * 由消费方通过 AbortSignal 断开，便于会话切换时精确回收连接。
 *
 * 归一层：服务端 SSE 会并行下发两路帧（D-08：迁移期旧事件继续可消费）：
 *   1. 四期统一事件（snake_case，`event_type` 字段，对齐 internal/sse/event.go）；
 *   2. 三期旧 ChatEvent（camelCase，`type` 字段，对齐 internal/service/chat.go）。
 * 本模块统一归一到 AgentEvent 信封，旧事件经 MapFromLegacy 同源规则翻译。
 */

import type { ChatMessage } from './sessions'
import { readToken } from './request'

/** 统一事件类型，对齐 `internal/sse/event.go` 的 EventType 全量枚举。 */
export type AgentEventType =
  | 'session.created'
  | 'turn.started'
  | 'turn.completed'
  | 'reason.started'
  | 'reason.completed'
  | 'tool.started'
  | 'tool.completed'
  | 'observation.created'
  | 'approval.required'
  | 'approval.approved'
  | 'approval.rejected'
  | 'message.delta'
  | 'message.completed'
  | 'session.completed'
  | 'session.failed'

/** Token 用量（snake_case）。 */
export interface AgentToken {
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
}

/** turn.* / session.* 事件载荷（对齐 TurnData）。 */
export interface AgentTurnData {
  status?: string
  input?: string
  output?: string
  error?: string
  token?: AgentToken | null
}

/** reason.* / tool.* 事件载荷（对齐 StepData，params/result 已脱敏）。 */
export interface AgentStepData {
  kind?: string
  status?: string
  seq?: number
  tool?: string
  risk?: string
  params?: string
  result?: string
  error?: string
}

/** observation.created 载荷（对齐 ObservationData）。 */
export interface AgentObservationData {
  tool?: string
  result?: string
  ok?: boolean
  /** P7.4（ADR-034）：后端声明的完整 tool_result，chat 与刷新同源渲染。 */
  summary?: string
  displayType?: string
  durationMs?: number
  risk?: string
  data?: unknown
}

export interface AgentChoiceOption {
  label: string
  value: string
}

/** approval.* 载荷（对齐 ApprovalData；secret=true 时前端密码框取值不入流）。 */
export interface AgentApprovalData {
  prompt?: string
  options?: AgentChoiceOption[]
  secret?: boolean
  decision?: string
  resource?: Record<string, unknown>
}

/** message.delta / message.completed 载荷（对齐 MessageData）。 */
export interface AgentMessageData {
  role?: 'assistant' | 'reasoning' | 'user' | (string & {})
  delta?: string
  index?: number
}

/** session 级附加信息（对齐 SessionData）。 */
export interface AgentSessionData {
  state?: string
  reason?: string
}

/** 统一事件信封，对齐 `internal/sse.Event`。 */
export interface AgentEvent {
  session_id?: string
  turn_id?: string
  step_id?: string
  timestamp?: string
  event_type: AgentEventType
  data:
    | AgentTurnData
    | AgentStepData
    | AgentObservationData
    | AgentApprovalData
    | AgentMessageData
    | AgentSessionData
    | null
}

/** 三期旧事件载荷（camelCase，对齐 service.ChatEvent / sse.LegacyEvent）。 */
export interface LegacyToolView {
  name?: string
  arguments?: Record<string, unknown>
  risk?: string
  result?: string
  summary?: string
  ok?: boolean
  truncated?: boolean
  data?: unknown
}

export interface LegacyChoiceOption {
  label: string
  value: string
}

export interface LegacyChoice {
  prompt?: string
  options?: LegacyChoiceOption[]
  secret?: boolean
}

export interface LegacyEvent {
  type?: string
  text?: string
  tool?: LegacyToolView
  choice?: LegacyChoice
  state?: string
  usage?: Record<string, unknown>
}

/** 服务端首次推送的历史快照（对齐 webui Server.handleStream）。 */
export interface HistorySnapshot {
  type: 'history'
  sessionId: string
  state: string
  messages: ChatMessage[]
  /**
   * P2.13 会话级 Token 用量累计（来自落库 Turn 汇总）。
   * 未装配四期 Store 或尚无记录时为 null：前端退化为隐藏统计，不伪造 0。
   */
  token?: AgentToken | null
}

/** 归一帧：历史快照、统一事件，或无法映射的旧帧（保留原样供下游消费）。 */
export type ChatStreamFrame =
  | { kind: 'history'; snapshot: HistorySnapshot }
  | { kind: 'event'; event: AgentEvent }
  | { kind: 'legacy'; event: LegacyEvent }
  | { kind: 'unknown'; payload: Record<string, unknown> }

/** 三期旧事件 → 统一事件的翻译，与 internal/sse/legacy.go 的 MapFromLegacy 同源。 */
export function mapLegacyToEvent(legacy: LegacyEvent): AgentEvent | null {
  switch (legacy.type) {
    case 'reasoning-delta':
      return { event_type: 'message.delta', data: { role: 'reasoning', delta: legacy.text ?? '' } }
    case 'answer-delta':
    case 'answer':
      return { event_type: 'message.delta', data: { role: 'assistant', delta: legacy.text ?? '' } }
    case 'text':
      return { event_type: 'message.delta', data: { role: 'user', delta: legacy.text ?? '' } }
    case 'tool-call':
      if (!legacy.tool) {
        return null
      }
      return {
        event_type: 'tool.started',
        data: {
          kind: 'tool',
          status: 'running',
          tool: legacy.tool.name,
          params: JSON.stringify(legacy.tool.arguments ?? {}),
        },
      }
    case 'tool-response':
      if (!legacy.tool) {
        return null
      }
      return {
        event_type: 'observation.created',
        data: { tool: legacy.tool.name, result: legacy.tool.result, ok: legacy.tool.ok },
      }
    case 'choice-request':
      if (!legacy.choice) {
        return null
      }
      return {
        event_type: 'approval.required',
        data: {
          prompt: legacy.choice.prompt,
          options: (legacy.choice.options ?? []).map((option) => ({
            label: option.label,
            value: option.value,
          })),
          secret: legacy.choice.secret ?? false,
        },
      }
    default:
      // state/progress/delta 等无一一对应的统一事件，保持旧流原样。
      return null
  }
}

/** 把单条 SSE data JSON 归一为 ChatStreamFrame。 */
export function normalizeFrame(payload: unknown): ChatStreamFrame | null {
  if (!payload || typeof payload !== 'object') {
    return null
  }
  const record = payload as Record<string, unknown>
  if (record.type === 'history') {
    const sessionId = typeof record.sessionId === 'string' ? record.sessionId : ''
    const state = typeof record.state === 'string' ? record.state : ''
    const messages = Array.isArray(record.messages)
      ? (record.messages as ChatMessage[])
      : []
    const token = (record.token ?? null) as AgentToken | null
    return { kind: 'history', snapshot: { type: 'history', sessionId, state, messages, token } }
  }
  if (typeof record.event_type === 'string') {
    return { kind: 'event', event: record as unknown as AgentEvent }
  }
  if (typeof record.type === 'string') {
    const legacy = record as unknown as LegacyEvent
    // 服务端在四期模式会并行下发统一事件与三期旧事件（D-08 迁移期兼容）。
    // 为避免同一语义（工具调用/思考/报告/审批）被渲染两份，旧事件在此保持
    // 独立帧，由消费方忽略；统一事件流是唯一展示数据源。
    return { kind: 'legacy', event: legacy }
  }
  return { kind: 'unknown', payload: record }
}

/** 解析一段 SSE 原始帧，提取拼接后的 `data:` 值；无 data 行返回 null。 */
function parseSseData(chunk: string): string | null {
  const parts: string[] = []
  for (const line of chunk.split(/\r?\n/)) {
    if (!line.startsWith('data:')) {
      continue
    }
    parts.push(line.replace(/^data:\s?/, ''))
  }
  return parts.length > 0 ? parts.join('\n') : null
}

async function* readSseFrames(response: Response): AsyncGenerator<{ data: string }> {
  const reader = response.body?.getReader()
  if (!reader) {
    return
  }
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) {
      break
    }
    buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n')
    let boundary = buffer.indexOf('\n\n')
    while (boundary >= 0) {
      const raw = buffer.slice(0, boundary)
      buffer = buffer.slice(boundary + 2)
      const data = parseSseData(raw)
      if (data !== null) {
        yield { data }
      }
      boundary = buffer.indexOf('\n\n')
    }
  }
  const tail = decoder.decode().replace(/\r\n/g, '\n')
  if (tail) {
    const data = parseSseData(tail)
    if (data !== null) {
      yield { data }
    }
  }
}

export interface ChatStreamHandlers {
  /** HTTP 头已就绪、开始读取事件帧。 */
  onOpen?: () => void
  onFrame: (frame: ChatStreamFrame) => void
}

/** 打开会话 SSE 流；流结束或取消（AbortSignal）时返回。异常由调用方处理。 */
export async function fetchChatStream(
  sessionId: string,
  handlers: ChatStreamHandlers,
  signal: AbortSignal,
): Promise<void> {
  const headers = new Headers({ Accept: 'text/event-stream' })
  // 四期平台进程下 SSE 同样受鉴权保护；三期 webui 忽略该头。
  const token = readToken()
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }
  const response = await fetch(`/api/sessions/${encodeURIComponent(sessionId)}/stream`, {
    signal,
    headers,
  })
  if (!response.ok) {
    throw new Error(`stream request failed with status ${response.status}`)
  }
  handlers.onOpen?.()
  for await (const frame of readSseFrames(response)) {
    if (signal.aborted) {
      return
    }
    let payload: unknown
    try {
      payload = JSON.parse(frame.data) as unknown
    } catch {
      continue
    }
    const normalized = normalizeFrame(payload)
    if (normalized) {
      handlers.onFrame(normalized)
    }
  }
}

export default fetchChatStream
