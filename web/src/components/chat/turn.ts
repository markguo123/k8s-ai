import type { AgentToken } from '@/api/sse'
import type { ToolUiData } from './ToolCallCard.vue'
import type { ApprovalUiData } from './ApprovalCard.vue'

/**
 * P2.7 回合（Turn）数据模型：历史快照与实时 SSE 统一事件经
 * MessageTimeline 折叠为以 turn 为单位的展示对象，供 TurnBlock 渲染。
 */

export interface ReasoningUiData {
  key: string
  text: string
  streaming: boolean
  done: boolean
}

export interface ReportUiData {
  key: string
  text: string
  streaming: boolean
  done: boolean
}

export type TurnItem =
  | { type: 'reasoning'; data: ReasoningUiData }
  | { type: 'tool'; data: ToolUiData }
  | { type: 'approval'; data: ApprovalUiData }
  | { type: 'report'; data: ReportUiData }

export type TurnStatus = 'running' | 'completed' | 'failed' | 'waiting'

export interface TurnUiData {
  key: string
  seq: number
  input: string
  status: TurnStatus
  error?: string
  token?: AgentToken | null
  startedAt?: string
  completedAt?: string
  items: TurnItem[]
}

/** 单回合耗时（ms）。 */
export function turnDuration(turn: TurnUiData): number | null {
  const start = turn.startedAt ? new Date(turn.startedAt).getTime() : NaN
  const end = turn.completedAt ? new Date(turn.completedAt).getTime() : NaN
  if (Number.isNaN(start)) {
    return null
  }
  if (Number.isNaN(end)) {
    return Date.now() - start
  }
  return Math.max(0, end - start)
}

export function formatDuration(ms: number | null): string {
  if (ms === null || ms === undefined) {
    return ''
  }
  if (ms < 1000) {
    return `${Math.round(ms)}ms`
  }
  return `${(ms / 1000).toFixed(1)}s`
}
