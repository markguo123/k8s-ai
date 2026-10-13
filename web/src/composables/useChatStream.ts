/**
 * P2.5 会话 SSE 流生命周期管理。
 *
 * - 绑定会话：切换会话时先中止旧连接再开启新连接（AbortController 精确回收）；
 * - 断线重连：指数退避 + 抖动，重连成功以服务端历史快照为准重建时间线；
 * - 去重：历史快照是“断点”，每收到快照即清空实时事件列表，避免重连重复；
 * - 归一：实时帧统一为 AgentEvent 供 ChatView 消费（P2.6/P2.7 在此基础上渲染）。
 */

import { onUnmounted, ref } from 'vue'
import type { AgentEvent, ChatStreamFrame, HistorySnapshot } from '@/api/sse'
import { fetchChatStream } from '@/api/sse'

export type StreamStatus = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed'

/** 重连退避：1s → 2s → 4s → 8s 封顶 + 最多 500ms 抖动。 */
function backoff(attempt: number): number {
  const exp = Math.min(attempt - 1, 3)
  return 1000 * 2 ** exp + Math.round(Math.random() * 500)
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms))
}

export function useChatStream() {
  const status = ref<StreamStatus>('idle')
  const sessionId = ref<string | null>(null)
  const error = ref('')
  const reconnectAttempt = ref(0)
  const lastFrameAt = ref('')
  const history = ref<HistorySnapshot | null>(null)
  const events = ref<AgentEvent[]>([])

  let controller: AbortController | null = null
  let running = false
  let disposed = false

  function reset(): void {
    status.value = 'idle'
    error.value = ''
    reconnectAttempt.value = 0
    lastFrameAt.value = ''
    history.value = null
    events.value = []
  }

  function clear(): void {
    running = false
    controller?.abort()
    controller = null
    sessionId.value = null
    reset()
  }

  function handleFrame(frame: ChatStreamFrame): void {
    lastFrameAt.value = new Date().toISOString()
    if (frame.kind === 'history') {
      // 历史快照为会话重放的断点：重建历史并清空实时事件，规避重连重复。
      history.value = frame.snapshot
      events.value = []
      status.value = 'open'
      return
    }
    if (frame.kind === 'event') {
      events.value.push(frame.event)
      return
    }
    // legacy/unknown 帧暂不进入归一视图（progress/state 无统一事件对应），
    // 后续 P2.6/P2.7 如需展示可在此扩展。
  }

  async function runLoop(id: string): Promise<void> {
    let attempt = 0
    while (running && sessionId.value === id) {
      controller = new AbortController()
      events.value = []
      try {
        await fetchChatStream(
          id,
          {
            onOpen: () => {
              status.value = 'open'
              error.value = ''
            },
            onFrame: handleFrame,
          },
          controller.signal,
        )
        if (sessionId.value !== id) {
          return
        }
        if (controller.signal.aborted) {
          return
        }
        // 流被服务端关闭（如进程重启/网络抖动）：进入重连退避。
        attempt += 1
        reconnectAttempt.value = attempt
        status.value = 'reconnecting'
        error.value = '连接中断，正在自动重连'
        await delay(backoff(attempt))
      } catch (err) {
        if (disposed || sessionId.value !== id || controller.signal.aborted) {
          return
        }
        attempt += 1
        reconnectAttempt.value = attempt
        status.value = 'reconnecting'
        error.value = err instanceof Error ? err.message : '连接失败，正在自动重连'
        await delay(backoff(attempt))
      }
    }
    if (status.value === 'reconnecting') {
      status.value = 'closed'
    }
  }

  async function connect(id: string): Promise<void> {
    clear()
    sessionId.value = id
    status.value = 'connecting'
    running = true
    await runLoop(id)
  }

  /** 手动重连：中止当前连接并用同一会话回到历史快照 + 重新订阅。 */
  async function reconnect(): Promise<void> {
    const id = sessionId.value
    if (!id) {
      return
    }
    status.value = 'connecting'
    reconnectAttempt.value = 0
    controller?.abort()
    controller = null
    running = true
    await runLoop(id)
  }

  function stop(): void {
    clear()
  }

  onUnmounted(() => {
    disposed = true
    clear()
  })

  return {
    status,
    sessionId,
    error,
    reconnectAttempt,
    lastFrameAt,
    history,
    events,
    connect,
    reconnect,
    stop,
  }
}

export default useChatStream
