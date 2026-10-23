/**
 * 平台会话端点客户端。
 * 这些端点由平台 server 直接提供（`GET|POST /api/v1/sessions`、
 * `POST /api/v1/sessions/{id}/rename`、`DELETE /api/v1/sessions/{id}`），
 * 所有请求携带当前登录 Token，并由服务端执行 owner 校验。
 */

import type { AgentToken } from './sse'
import { readToken } from './request'

/** 列表项，与服务端 handleListSessions 返回字段一一对应。 */
export interface SessionSummary {
  id: string
  name: string
  state: string
  /** 会话使用的模型名（composer 只读展示）。 */
  model?: string
  messageCount: number
  lastModified: string
  /** P7.5 置顶状态（置顶会话排最前）。 */
  pinned?: boolean
  /** P2.13 会话级 Token 用量合计；未持久化时缺省。 */
  token?: AgentToken | null
}

/** 会话内一条消息，对齐 `internal/session/session.go` 的 Message。 */
export interface ChatMessage {
  role: string
  type: string
  name?: string
  toolCallId?: string
  arguments?: unknown
  data?: unknown
  ok?: boolean
  /** P7.4（ADR-034）：工具结果同源契约，刷新后与 chat 共用同一份头部信息。 */
  summary?: string
  displayType?: string
  durationMs?: number
  risk?: string
  content: string
  timestamp: string
}

/** 完整 Session，对齐 `internal/session/session.go` 的 Session。 */
export interface SessionDetail {
  id: string
  name?: string
  cluster?: string
  namespace?: string
  model?: string
  state?: string
  messages?: ChatMessage[]
  approvedWrites?: string[]
  declinedWrites?: Record<string, number>
  createdAt?: string
  lastModified?: string
}

async function sessionFetch<T>(url: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body !== undefined) {
    headers.set('Content-Type', 'application/json')
  }
  // 会话端点在四期平台进程下同样受鉴权保护：带 Bearer 令牌，
  // 三期 webui（无鉴权）会忽略该头，行为不变。
  const token = readToken()
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }
  const response = await fetch(url, { ...init, headers })
  const text = await response.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text) as unknown
    } catch {
      data = null
    }
  }
  if (!response.ok) {
    const message =
      (data &&
        typeof data === 'object' &&
        'error' in data &&
        typeof (data as { error: unknown }).error === 'string'
        ? (data as { error: string }).error
        : undefined) ?? `request failed with status ${response.status}`
    throw new Error(message)
  }
  return data as T
}

export async function listSessions(): Promise<SessionSummary[]> {
  return sessionFetch<SessionSummary[]>('/api/v1/sessions')
}

/** 创建新会话；若传 id 则等同“恢复/获取”该会话。 */
export async function createSession(payload: { id?: string; name?: string } = {}): Promise<SessionDetail> {
  return sessionFetch<SessionDetail>('/api/v1/sessions', {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export async function renameSession(id: string, name: string): Promise<{ id: string; name: string }> {
  return sessionFetch<{ id: string; name: string }>(
    `/api/v1/sessions/${encodeURIComponent(id)}/rename`,
    { method: 'POST', body: JSON.stringify({ name }) },
  )
}

/** 置顶/取消置顶会话（P7.5）。 */
export async function pinSession(
  id: string,
  pinned: boolean,
): Promise<{ id: string; pinned: boolean }> {
  return sessionFetch<{ id: string; pinned: boolean }>(
    `/api/v1/sessions/${encodeURIComponent(id)}/pin`,
    { method: 'POST', body: JSON.stringify({ pinned }) },
  )
}

/** 发起一轮 Chat（webui `POST /api/v1/sessions/{id}/send-message`），SSE 流随后推送事件。 */
export async function sendMessage(
  id: string,
  payload: { query: string; skipPermissions?: boolean; skills?: string[] },
): Promise<{ sessionId: string }> {
  const body: Record<string, unknown> = { query: payload.query }
  if (payload.skipPermissions !== undefined) {
    body.skipPermissions = payload.skipPermissions
  }
  // P3.8.4：显式 @skill 指定的名称只在非空时随请求提交，后端强校验。
  if (payload.skills && payload.skills.length > 0) {
    body.skills = payload.skills
  }
  return sessionFetch<{ sessionId: string }>(
    `/api/v1/sessions/${encodeURIComponent(id)}/send-message`,
    { method: 'POST', body: JSON.stringify(body) },
  )
}

/** 取消当前会话正在执行的 Chat 运行。 */
export async function cancelRun(id: string): Promise<{ sessionId: string }> {
  return sessionFetch<{ sessionId: string }>(`/api/v1/sessions/${encodeURIComponent(id)}/cancel`, {
    method: 'POST',
  })
}

/** 回传审批选择（webui `POST /api/v1/sessions/{id}/approve`）。choice 不写回响应体。 */
export async function approveRun(id: string, choice: string): Promise<{ sessionId: string }> {
  return sessionFetch<{ sessionId: string }>(
    `/api/v1/sessions/${encodeURIComponent(id)}/approve`,
    { method: 'POST', body: JSON.stringify({ choice }) },
  )
}

export async function deleteSession(id: string): Promise<{ deleted: string }> {
  return sessionFetch<{ deleted: string }>(`/api/v1/sessions/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}
