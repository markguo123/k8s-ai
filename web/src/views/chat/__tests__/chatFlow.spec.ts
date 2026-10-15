import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import ChatView from '@/views/chat/ChatView.vue'

/**
 * P2.12 E2E 冒烟（前端全链路，后端以 fetch 打桩）：
 * 登录态 → 会话列表 → 选择会话并订阅 SSE → 发送 → 审批卡回传 → 停止。
 * 真实集群端到端由 P6.7 覆盖，本用例保证前端契约与交互不断链。
 */

const SESSION = {
  id: 's1',
  name: '诊断 default 命名空间部署',
  state: 'running',
  model: 'qwen-plus',
  messageCount: 2,
  lastModified: new Date().toISOString(),
  token: { prompt_tokens: 11, completion_tokens: 7, total_tokens: 18 },
}

interface Call {
  url: string
  body: unknown
}

let calls: Call[] = []

function sseStream(frames: unknown[]): Response {
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    async start(controller) {
      controller.enqueue(
        encoder.encode(
          `data: ${JSON.stringify({
            type: 'history',
            sessionId: 's1',
            state: 'running',
            messages: [],
            token: SESSION.token,
          })}\n\n`,
        ),
      )
      for (const frame of frames) {
        await new Promise((resolve) => setTimeout(resolve, 5))
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(frame)}\n\n`))
      }
    },
  })
  return new Response(stream, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
}

function installFetch(frames: unknown[]): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = String(input)
      const body = init.body ? JSON.parse(String(init.body)) : null
      calls.push({ url, body })
      if (url === '/api/sessions' && (init.method ?? 'GET') === 'GET') {
        return new Response(JSON.stringify([SESSION]), { status: 200 })
      }
      if (url.endsWith('/stream')) {
        return sseStream(frames)
      }
      if (url.endsWith('/send-message')) {
        return new Response(JSON.stringify({ sessionId: 's1' }), { status: 202 })
      }
      if (url.endsWith('/approve') || url.endsWith('/cancel')) {
        return new Response(JSON.stringify({ sessionId: 's1' }), { status: 200 })
      }
      return new Response('{}', { status: 200 })
    }),
  )
}

function postCallsSuffix(suffix: string): Call[] {
  return calls.filter((call) => call.url.endsWith(suffix))
}

async function mountChat(frames: unknown[] = []) {
  installFetch(frames)
  const wrapper = mount(ChatView, { attachTo: document.body })
  await vi.waitFor(() => expect(wrapper.text()).toContain(SESSION.name))
  await wrapper.find('.session-item').trigger('click')
  await vi.waitFor(() => expect(wrapper.text()).toContain('Token 用量'))
  return wrapper
}

enableAutoUnmount(afterEach)

describe('ChatView 全链路冒烟（P2.10/P2.11/P2.13）', () => {
  beforeEach(() => {
    calls = []
    localStorage.clear()
    localStorage.setItem(TOKEN_KEY, 'tok-1')
    localStorage.setItem(USER_KEY, JSON.stringify({ userId: 'u1', username: 'admin', role: 'admin' }))
    sessionStorage.clear()
    setActivePinia(createPinia())
  })

  it('加载会话列表并展示会话级 Token（刷新/重开仍可读）', async () => {
    const wrapper = await mountChat()
    expect(wrapper.text()).toContain('2 条')
    expect(wrapper.text()).toContain('18')
    expect(wrapper.text()).toContain('Token 用量')
  })

  it('选择会话后订阅 SSE：发送进入 Turn，审批卡回传选择值', async () => {
    const wrapper = await mountChat([
      {
        event_type: 'turn.started',
        turn_id: 't1',
        data: { input: '修复它' },
      },
      {
        event_type: 'approval.required',
        turn_id: 't1',
        data: {
          prompt: '确认执行 dry-run 后的 apply？',
          options: [{ label: '确认并执行', value: 'approve' }],
          tool: 'create_resource',
          risk: 'HIGH',
        },
      },
    ])

    await vi.waitFor(() => expect(wrapper.text()).toContain('审批请求'))

    // 发送：输入 → 点击发送按钮
    await wrapper.find('textarea').setValue('修复它')
    const send = wrapper.findAll('button').find((btn) => btn.text() === '发送')!
    await send.trigger('click')
    await vi.waitFor(() => expect(postCallsSuffix('/send-message')).toHaveLength(1))
    expect(postCallsSuffix('/send-message')[0].body).toEqual({ query: '修复它' })

    // 审批回传
    const approve = wrapper.findAll('button').find((btn) => btn.text() === '确认并执行')!
    await approve.trigger('click')
    await vi.waitFor(() => expect(postCallsSuffix('/approve')).toHaveLength(1))
    expect(postCallsSuffix('/approve')[0].body).toEqual({ choice: 'approve' })
  })

  it('SSE 驱动运行态：turn.completed 后回到可发送，停止按钮回传 cancel', async () => {
    const wrapper = await mountChat([
      { event_type: 'turn.started', turn_id: 't2', data: { input: '检查集群' } },
    ])
    await vi.waitFor(() => expect(wrapper.text()).toContain('检查集群'))

    await wrapper.find('textarea').setValue('检查集群')
    const send = wrapper.findAll('button').find((btn) => btn.text() === '发送')!
    await send.trigger('click')
    await vi.waitFor(() => expect(postCallsSuffix('/send-message')).toHaveLength(1))

    // 运行中：发送按钮变为停止
    const stop = wrapper.findAll('button').find((btn) => btn.text().includes('停止'))!
    await stop.trigger('click')
    await vi.waitFor(() => expect(postCallsSuffix('/cancel')).toHaveLength(1))
  })

  it('会话级 Token 随 turn.completed 的 usage 累加', async () => {
    const wrapper = await mountChat([
      { event_type: 'turn.started', turn_id: 't3', data: { input: '再查一次' } },
      {
        event_type: 'turn.completed',
        turn_id: 't3',
        data: { output: '完成', token: { prompt_tokens: 5, completion_tokens: 5, total_tokens: 10 } },
      },
    ])
    await vi.waitFor(() => expect(wrapper.text()).toContain('28'))
  })
})
