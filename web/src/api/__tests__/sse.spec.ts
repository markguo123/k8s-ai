import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  fetchChatStream,
  mapLegacyToEvent,
  normalizeFrame,
  type ChatStreamFrame,
} from '@/api/sse'
import { TOKEN_KEY } from '@/api/request'

function sseResponse(frames: unknown[]): Response {
  const body = frames.map((frame) => `data: ${JSON.stringify(frame)}\n\n`).join('')
  return new Response(body, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

describe('normalizeFrame（P2.5 事件归一）', () => {
  it('识别历史快照并保留会话级 token（P2.13）', () => {
    const frame = normalizeFrame({
      type: 'history',
      sessionId: 's1',
      state: 'idle',
      messages: [],
      token: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 },
    }) as Extract<ChatStreamFrame, { kind: 'history' }>
    expect(frame.kind).toBe('history')
    expect(frame.snapshot.sessionId).toBe('s1')
    expect(frame.snapshot.token?.total_tokens).toBe(5)
  })

  it('未持久化时 token 为 null（前端隐藏统计）', () => {
    const frame = normalizeFrame({ type: 'history', sessionId: 's1', state: 'idle', messages: [] })
    expect((frame as Extract<ChatStreamFrame, { kind: 'history' }>).snapshot.token).toBeNull()
  })

  it('统一事件按 event_type 判定，旧事件单独成帧不被混入事件流', () => {
    const unified = normalizeFrame({ event_type: 'tool.started', data: { tool: 'get_pod' } })
    expect(unified?.kind).toBe('event')
    const legacy = normalizeFrame({ type: 'progress', text: '正在扫描' })
    expect(legacy?.kind).toBe('legacy')
    expect(normalizeFrame('nope')).toBeNull()
  })
})

describe('mapLegacyToEvent（与后端 internal/sse/legacy.go 同源）', () => {
  it('思考/回答/用户回显都归一到 message.delta 的对应 role', () => {
    expect(mapLegacyToEvent({ type: 'reasoning-delta', text: '想' })).toMatchObject({
      event_type: 'message.delta',
      data: { role: 'reasoning', delta: '想' },
    })
    expect(mapLegacyToEvent({ type: 'answer-delta', text: '答' })).toMatchObject({
      data: { role: 'assistant', delta: '答' },
    })
    expect(mapLegacyToEvent({ type: 'answer', text: '全文' })).toMatchObject({
      data: { role: 'assistant', delta: '全文' },
    })
    expect(mapLegacyToEvent({ type: 'text', text: '问' })).toMatchObject({
      data: { role: 'user', delta: '问' },
    })
  })

  it('工具调用/结果与审批请求映射为统一事件', () => {
    expect(
      mapLegacyToEvent({ type: 'tool-call', tool: { name: 'get_pod', arguments: { ns: 'default' } } }),
    ).toMatchObject({ event_type: 'tool.started', data: { tool: 'get_pod' } })
    expect(
      mapLegacyToEvent({ type: 'tool-response', tool: { name: 'get_pod', result: 'ok', ok: true } }),
    ).toMatchObject({ event_type: 'observation.created', data: { tool: 'get_pod', ok: true } })
    expect(
      mapLegacyToEvent({
        type: 'choice-request',
        choice: { prompt: '确认', options: [{ label: '批准', value: 'yes' }], secret: true },
      }),
    ).toMatchObject({
      event_type: 'approval.required',
      data: { prompt: '确认', secret: true },
    })
  })

  it('缺失载荷或无对应语义的事件返回 null（不产生伪造事件）', () => {
    expect(mapLegacyToEvent({ type: 'tool-call' })).toBeNull()
    expect(mapLegacyToEvent({ type: 'progress', text: '扫描中' })).toBeNull()
    expect(mapLegacyToEvent({ type: 'state', state: 'running' })).toBeNull()
  })
})

describe('fetchChatStream（SSE 传输）', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('按序回调历史快照与实时事件，并带上 Bearer 令牌', async () => {
    localStorage.setItem(TOKEN_KEY, 'tok-1')
    const frames: ChatStreamFrame[] = []
    const fetchMock = vi.fn(async () =>
      sseResponse([
        { type: 'history', sessionId: 's1', state: 'idle', messages: [], token: null },
        { event_type: 'turn.started', data: { input: '你好' } },
      ]),
    )
    vi.stubGlobal('fetch', fetchMock)

    await fetchChatStream('s1', { onFrame: (frame) => frames.push(frame) }, new AbortController().signal)

    expect(frames.map((frame) => frame.kind)).toEqual(['history', 'event'])
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/sessions/s1/stream')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer tok-1')
  })

  it('非 2xx 直接抛错（由上层进入重连退避）', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 500 })))
    await expect(
      fetchChatStream('s1', { onFrame: () => {} }, new AbortController().signal),
    ).rejects.toThrow('stream request failed with status 500')
  })
})
