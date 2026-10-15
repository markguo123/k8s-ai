import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'

import { useChatStream } from '@/composables/useChatStream'

/** 在真实组件实例内使用组合式函数（对齐运行时生命周期，避免裸调用告警）。 */
function mountStream() {
  let stream!: ReturnType<typeof useChatStream>
  const wrapper = mount(
    defineComponent({
      setup() {
        stream = useChatStream()
        return () => null
      },
    }),
  )
  return { stream, unmount: () => wrapper.unmount() }
}

function sseResponse(frames: unknown[], closeAfter = true): Response {
  const body = frames.map((frame) => `data: ${JSON.stringify(frame)}\n\n`).join('')
  if (closeAfter) {
    return new Response(body, { status: 200 })
  }
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(body))
      // 保持连接打开：模拟运行中的会话流。
    },
  })
  return new Response(stream, { status: 200 })
}

describe('useChatStream（P2.5 会话流生命周期）', () => {
  it('以历史快照为断点重建，并把实时事件按序追加', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        sseResponse(
          [
            { type: 'history', sessionId: 's1', state: 'idle', messages: [], token: { total_tokens: 5 } },
            { event_type: 'turn.started', data: { input: '诊断' } },
          ],
          false,
        ),
      ),
    )
    const { stream, unmount } = mountStream()
    void stream.connect('s1')
    await vi.waitFor(() => expect(stream.events.value.length).toBe(1))

    expect(stream.status.value).toBe('open')
    expect(stream.history.value?.token?.total_tokens).toBe(5)
    expect(stream.events.value[0].event_type).toBe('turn.started')
    unmount()
  })

  it('切换会话时中止旧连接并清空状态（不串会话）', async () => {
    const signals: AbortSignal[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit = {}) => {
        if (init.signal) {
          signals.push(init.signal)
        }
        return sseResponse([{ event_type: 'turn.started', data: { input: 'x' } }], false)
      }),
    )
    const { stream, unmount } = mountStream()
    void stream.connect('s1')
    await vi.waitFor(() => expect(signals.length).toBe(1))
    void stream.connect('s2')
    await nextTick()

    expect(signals[0].aborted).toBe(true)
    expect(stream.sessionId.value).toBe('s2')
    expect(stream.events.value.length).toBeLessThanOrEqual(1)
    unmount()
  })

  it('流被服务端关闭后进入重连退避（指数退避 + 重连计数）', async () => {
    vi.useFakeTimers()
    let calls = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        calls += 1
        return sseResponse([], true)
      }),
    )
    const { stream, unmount } = mountStream()
    void stream.connect('s1')
    await vi.waitFor(() => expect(calls).toBeGreaterThan(0))
    await vi.advanceTimersByTimeAsync(1500)

    expect(stream.status.value).toBe('reconnecting')
    expect(stream.reconnectAttempt.value).toBeGreaterThanOrEqual(1)
    expect(stream.error.value).toContain('重连')
    stream.stop()
    vi.useRealTimers()
    unmount()
  })

  it('stop() 复位为 idle 并中止连接', async () => {
    const signals: AbortSignal[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit = {}) => {
        if (init.signal) {
          signals.push(init.signal)
        }
        return sseResponse([], false)
      }),
    )
    const { stream, unmount } = mountStream()
    void stream.connect('s1')
    await vi.waitFor(() => expect(signals.length).toBe(1))
    stream.stop()

    expect(signals[0].aborted).toBe(true)
    expect(stream.status.value).toBe('idle')
    expect(stream.sessionId.value).toBeNull()
    unmount()
  })
})
