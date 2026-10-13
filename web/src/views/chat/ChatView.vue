<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, RefreshRight } from '@element-plus/icons-vue'

import type { SessionSummary } from '@/api/sessions'
import { createSession, deleteSession, listSessions, renameSession } from '@/api/sessions'
import ConversationList from '@/components/chat/ConversationList.vue'
import { useChatStream, type StreamStatus } from '@/composables/useChatStream'
import type { AgentEvent } from '@/api/sse'
import { sessionStateTagType } from '@/utils/format'

const ACTIVE_SESSION_KEY = 'k8s_ai_active_session'

const sessions = ref<SessionSummary[]>([])
const loading = ref(false)
const activeId = ref<string | null>(sessionStorage.getItem(ACTIVE_SESSION_KEY))
const stream = useChatStream()

const activeSession = computed(
  () => sessions.value.find((session) => session.id === activeId.value) ?? null,
)

/** 归一事件展示项：message.delta 按 turn+role 聚合为流式文本，其余事件逐条展示。 */
interface LiveItem {
  key: string
  role?: string
  text?: string
  event?: AgentEvent
}

const liveItems = computed<LiveItem[]>(() => {
  const rows: LiveItem[] = []
  const streams = new Map<string, LiveItem>()
  const order: string[] = []
  for (const event of stream.events.value) {
    const data = (event.data ?? {}) as Record<string, unknown>
    if (event.event_type === 'message.delta' && typeof data.role === 'string') {
      const key = `${event.turn_id ?? ''}:${data.role as string}`
      const delta = typeof data.delta === 'string' ? data.delta : ''
      let item = streams.get(key)
      if (!item) {
        item = { key, role: data.role as string, text: delta }
        streams.set(key, item)
        order.push(key)
        rows.push(item)
      } else {
        item.text = `${item.text ?? ''}${delta}`
      }
      continue
    }
    rows.push({ key: `${event.step_id ?? ''}-${event.event_type}-${rows.length}`, event })
  }
  return rows
})

const statusText = computed(() => {
  switch (stream.status.value) {
    case 'connecting':
      return '连接中'
    case 'open':
      return '已连接'
    case 'reconnecting':
      return `重连中（第 ${stream.reconnectAttempt.value} 次）`
    case 'closed':
    case 'idle':
    default:
      return '未连接'
  }
})

function statusTag(status: StreamStatus): 'primary' | 'success' | 'warning' | 'danger' | 'info' {
  switch (status) {
    case 'connecting':
      return 'primary'
    case 'open':
      return 'success'
    case 'reconnecting':
      return 'warning'
    case 'closed':
    case 'idle':
    default:
      return 'info'
  }
}

function eventTitle(event: AgentEvent): string {
  const map: Record<string, string> = {
    'session.created': '会话已创建',
    'turn.started': '回合开始',
    'turn.completed': '回合完成',
    'reason.started': '开始分析',
    'reason.completed': '分析完成',
    'tool.started': '调用工具',
    'tool.completed': '工具完成',
    'observation.created': '工具结果',
    'approval.required': '需要审批',
    'approval.approved': '审批通过',
    'approval.rejected': '审批拒绝',
    'message.completed': '消息完成',
    'session.completed': '会话结束',
    'session.failed': '会话失败',
  }
  return map[event.event_type] ?? event.event_type
}

function eventMeta(event: AgentEvent): string {
  const data = (event.data ?? {}) as Record<string, unknown>
  const parts: string[] = []
  if (typeof data.tool === 'string' && data.tool) {
    parts.push(data.tool)
  }
  if (typeof data.risk === 'string' && data.risk) {
    parts.push(data.risk)
  }
  if (typeof data.status === 'string' && data.status) {
    parts.push(data.status)
  }
  if (typeof data.decision === 'string' && data.decision) {
    parts.push(data.decision)
  }
  if (typeof data.result === 'string' && data.result) {
    parts.push(data.result.slice(0, 120))
  }
  return parts.join(' · ')
}

async function loadSessions(): Promise<void> {
  loading.value = true
  try {
    sessions.value = await listSessions()
    if (activeId.value && !sessions.value.some((session) => session.id === activeId.value)) {
      activeId.value = null
      sessionStorage.removeItem(ACTIVE_SESSION_KEY)
    }
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '加载会话失败')
  } finally {
    loading.value = false
  }
}

function selectSession(id: string): void {
  if (id === activeId.value) {
    return
  }
  activeId.value = id
  sessionStorage.setItem(ACTIVE_SESSION_KEY, id)
}

function handleReconnect(): void {
  if (!activeId.value) {
    return
  }
  void stream.reconnect()
}

async function handleCreate(): Promise<void> {
  try {
    const session = await createSession()
    activeId.value = session.id
    sessionStorage.setItem(ACTIVE_SESSION_KEY, session.id)
    await loadSessions()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '创建会话失败')
  }
}

async function handleRename(payload: { id: string; name: string }): Promise<void> {
  try {
    await renameSession(payload.id, payload.name)
    await loadSessions()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '重命名会话失败')
  }
}

async function handleDelete(target: SessionSummary): Promise<void> {
  try {
    await ElMessageBox.confirm(`确定删除会话「${target.name || target.id}」？`, '删除会话', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    await deleteSession(target.id)
    if (activeId.value === target.id) {
      activeId.value = null
      sessionStorage.removeItem(ACTIVE_SESSION_KEY)
    }
    await loadSessions()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '删除会话失败')
  }
}

onMounted(() => {
  void loadSessions()
})

// 会话切换：先由 connect 内部 clear() 中止旧连接，再订阅新会话。
watch(activeId, (id) => {
  if (id) {
    void stream.connect(id)
  } else {
    stream.stop()
  }
})
</script>

<template>
  <section class="chat-page">
    <ConversationList
      class="chat-side"
      :sessions="sessions"
      :active-id="activeId"
      :loading="loading"
      @create="handleCreate"
      @select="selectSession"
      @delete="handleDelete"
      @rename="handleRename"
      @refresh="loadSessions"
    />
    <main class="chat-main">
      <header class="chat-head">
        <div class="chat-heading">
          <h2>{{ activeSession?.name || '未选择会话' }}</h2>
          <span v-if="activeSession" class="chat-id" :title="activeSession.id">
            {{ activeSession.id }}
          </span>
        </div>
        <div v-if="activeSession" class="chat-meta">
          <el-tag size="small" effect="plain" :type="sessionStateTagType(activeSession.state)" class="chat-state">
            {{ activeSession.state }}
          </el-tag>
          <span class="chat-count">
            {{ activeSession.messageCount }} 条消息
          </span>
        </div>
      </header>
      <div class="chat-body">
        <template v-if="activeSession">
          <main class="chat-stream">
            <header class="stream-bar">
              <div class="stream-state">
                <el-tag size="small" effect="plain" :type="statusTag(stream.status.value)" class="stream-tag">
                  {{ statusText }}
                </el-tag>
                <span v-if="stream.lastFrameAt.value" class="stream-last">
                  最后事件更新于 {{ new Date(stream.lastFrameAt.value).toLocaleTimeString() }}
                </span>
              </div>
              <div class="stream-actions">
                <el-button size="small" text :icon="RefreshRight" @click="handleReconnect">
                  重连
                </el-button>
              </div>
            </header>
            <p v-if="stream.error.value" class="stream-error">{{ stream.error.value }}</p>
            <div class="timeline">
              <template v-if="(stream.history.value?.messages ?? []).length || liveItems.length">
                <article
                  v-for="(message, index) in stream.history.value?.messages ?? []"
                  :key="`history-${message.timestamp ?? ''}-${index}`"
                  class="timeline-message"
                  :class="message.role"
                >
                  <span class="message-role">{{ message.role }}</span>
                  <p class="message-body">{{ message.content }}</p>
                </article>
                <article
                  v-for="item in liveItems"
                  :key="item.key"
                  class="timeline-message"
                  :class="item.role ? `delta-${item.role}` : 'event'"
                >
                  <span v-if="item.role" class="message-role">{{ item.role }}</span>
                  <template v-else-if="item.event">
                    <span class="message-role">{{ eventTitle(item.event) }}</span>
                    <p class="message-body">{{ eventMeta(item.event) }}</p>
                  </template>
                  <p v-if="item.text" class="message-body message-flow">{{ item.text }}</p>
                </article>
              </template>
              <el-empty
                v-else
                description="连接后将在此展示 Agent 事件流"
                :image-size="72"
              />
            </div>
          </main>
        </template>
        <template v-else>
          <el-empty description="创建一个会话，开始与 Agent 对话" :image-size="96">
            <el-button type="primary" :icon="ChatDotRound" @click="handleCreate">新建会话</el-button>
          </el-empty>
        </template>
      </div>
    </main>
  </section>
</template>

<style scoped>
.chat-page {
  display: grid;
  grid-template-columns: 280px minmax(0, 1fr);
  gap: 16px;
  height: 100%;
  min-height: 0;
}

.chat-main {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  overflow: hidden;
}

.chat-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--app-border);
}

.chat-heading {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.chat-heading h2 {
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 15px;
  font-weight: 600;
}

.chat-id {
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 0 8px;
  border-radius: 4px;
  background: var(--app-bg);
  color: var(--app-muted);
  font-family: 'JetBrains Mono', Consolas, monospace;
  font-size: 11px;
  line-height: 18px;
}

.chat-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
}

.chat-state {
  text-transform: capitalize;
}

.chat-count {
  font-size: 12px;
  color: var(--app-muted);
}

.chat-body {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
}

.chat-stream {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 0;
  overflow: hidden;
}

.stream-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--app-border);
  background: var(--app-bg);
}

.stream-state {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.stream-tag {
  font-variant: tabular-nums;
}

.stream-last {
  font-size: 12px;
  color: var(--app-muted);
}

.stream-actions {
  flex: none;
}

.stream-error {
  margin: 0;
  padding: 6px 16px;
  border-bottom: 1px solid var(--app-border);
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
  font-size: 12px;
}

.timeline {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 10px;
  min-height: 0;
  padding: 16px;
  overflow-y: auto;
}

.timeline-message {
  max-width: 760px;
  align-self: flex-start;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
}

.timeline-message.user {
  align-self: flex-end;
  background: var(--app-accent-soft);
  border-color: var(--app-accent);
}

.timeline-message .message-role {
  display: inline-block;
  margin-bottom: 4px;
  font-size: 11px;
  font-weight: 600;
  text-transform: capitalize;
  color: var(--app-muted);
}

.timeline-message .message-body {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.55;
}

.timeline-message.user .message-role {
  color: var(--app-accent);
}

.timeline-message .message-flow {
  color: var(--app-muted);
  font-size: 13px;
}
</style>
