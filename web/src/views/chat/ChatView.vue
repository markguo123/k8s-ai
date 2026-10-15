<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, RefreshRight } from '@element-plus/icons-vue'

import type { SessionSummary } from '@/api/sessions'
import {
  approveRun,
  cancelRun,
  createSession,
  deleteSession,
  listSessions,
  renameSession,
  sendMessage,
} from '@/api/sessions'
import type { AgentTurnData } from '@/api/sse'
import ConversationList from '@/components/chat/ConversationList.vue'
import MessageTimeline from '@/components/chat/MessageTimeline.vue'
import ComposerBox from '@/components/chat/ComposerBox.vue'
import TokenUsageBadge from '@/components/chat/TokenUsageBadge.vue'
import { useChatStream, type StreamStatus } from '@/composables/useChatStream'
import { useAuthStore } from '@/stores/auth'
import { addTokenUsage, sessionStateTagType } from '@/utils/format'
import { canDecideApproval } from '@/utils/permission'

const ACTIVE_SESSION_KEY = 'k8s_ai_active_session'

const sessions = ref<SessionSummary[]>([])
const loading = ref(false)
const activeId = ref<string | null>(sessionStorage.getItem(ACTIVE_SESSION_KEY))
const stream = useChatStream()

const input = ref('')
const sending = ref(false)
const composerRef = ref<InstanceType<typeof ComposerBox> | null>(null)
const auth = useAuthStore()

const CONVERSATIONS_KEY = 'k8s_ai_conversations_collapsed'
const conversationsCollapsed = ref(localStorage.getItem(CONVERSATIONS_KEY) === '1')
watch(conversationsCollapsed, (collapsed) => {
  localStorage.setItem(CONVERSATIONS_KEY, collapsed ? '1' : '0')
})

const activeSession = computed(
  () => sessions.value.find((session) => session.id === activeId.value) ?? null,
)

/** P2.9 审批操作显隐：只读角色不展示审批按钮（后端仍独立强校验）。 */
const canDecide = computed(() => canDecideApproval(auth.role))

/** P2.13 会话级 Token：历史快照（落库汇总）+ 本次连接内各轮增量，按回合去重。 */
const sessionToken = computed(() => {
  let total = addTokenUsage(stream.history.value?.token ?? null, null)
  const seen = new Set<string>()
  for (const event of stream.events.value) {
    if (event.event_type !== 'turn.completed') {
      continue
    }
    const turnId = event.turn_id ?? `idx:${seen.size}`
    if (seen.has(turnId)) {
      continue
    }
    seen.add(turnId)
    const data = (event.data ?? {}) as AgentTurnData
    if (data.token) {
      total = addTokenUsage(total, data.token)
    }
  }
  return total
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
    // 同一会话重复点击按「重新订阅」处理：冷启动或断流后点一下就恢复，避免点击无反应。
    if (stream.sessionId.value === id) {
      void stream.reconnect()
    } else {
      void stream.connect(id)
    }
    return
  }
  activeId.value = id
  sessionStorage.setItem(ACTIVE_SESSION_KEY, id)
}

function handleReconnect(): void {
  if (!activeId.value) {
    return
  }
  if (stream.sessionId.value === activeId.value) {
    void stream.reconnect()
  } else {
    void stream.connect(activeId.value)
  }
}

async function handleCreate(): Promise<void> {
  try {
    const session = await createSession()
    activeId.value = session.id
    sessionStorage.setItem(ACTIVE_SESSION_KEY, session.id)
    await loadSessions()
    await nextTick()
    composerRef.value?.focus()
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

async function handleSend(): Promise<void> {
  const query = input.value.trim()
  if (!activeId.value || !query || sending.value) {
    return
  }
  sending.value = true
  input.value = ''
  try {
    await sendMessage(activeId.value, { query })
  } catch (error) {
    sending.value = false
    ElMessage.error(error instanceof Error ? error.message : '发送失败')
  }
}

/** 停止当前正在执行的会话运行。 */
function handleStop(): void {
  if (!activeId.value) {
    return
  }
  sending.value = false
  void cancelRun(activeId.value).catch((error) => {
    ElMessage.error(error instanceof Error ? error.message : '取消失败')
  })
}

/** 审批回传：把用户选择回传给服务端。 */
function handleApprove(value: string): void {
  if (!activeId.value) {
    return
  }
  void approveRun(activeId.value, value).catch((error) => {
    ElMessage.error(error instanceof Error ? error.message : '审批回传失败')
  })
}

/** 审批拒绝：与旧 UI 一致，回传 `no` 作为否定选择。 */
function handleReject(): void {
  if (!activeId.value) {
    return
  }
  void approveRun(activeId.value, 'no').catch((error) => {
    ElMessage.error(error instanceof Error ? error.message : '拒绝回传失败')
  })
}

onMounted(() => {
  void loadSessions()
})

// 会话切换：先由 connect 内部 clear() 中止旧连接，再订阅新会话。
// immediate 不可省：activeId 从 sessionStorage 恢复时首帧不会触发 watch，
// 缺了它刷新后不会订阅 SSE（页面停在「未连接 / 暂无消息」）。
watch(
  activeId,
  (id) => {
    if (id) {
      sending.value = false
      void stream.connect(id)
    } else {
      stream.stop()
      sending.value = false
    }
  },
  { immediate: true },
)

// 回合/会话结束或失败时复位“运行中”态（token 统计由 sessionToken 计算属性驱动）。
watch(
  () => stream.events.value.length,
  () => {
    const events = stream.events.value
    let terminalSeen = false
    for (let i = events.length - 1; i >= 0; i -= 1) {
      const event = events[i]
      if (terminalSeen) {
        break
      }
      if (event.event_type === 'session.completed' || event.event_type === 'session.failed') {
        terminalSeen = true
        break
      }
      if (event.event_type === 'turn.completed') {
        terminalSeen = true
        break
      }
    }
    if (terminalSeen) {
      sending.value = false
    }
  },
  { deep: false },
)
</script>

<template>
  <section class="chat-page" :class="{ 'ctx-collapsed': conversationsCollapsed }">
    <ConversationList
      class="chat-side"
      :sessions="sessions"
      :active-id="activeId"
      :loading="loading"
      :collapsed="conversationsCollapsed"
      @create="handleCreate"
      @select="selectSession"
      @delete="handleDelete"
      @rename="handleRename"
      @refresh="loadSessions"
      @toggle="conversationsCollapsed = !conversationsCollapsed"
    />
    <main class="chat-main">
      <TokenUsageBadge :token="sessionToken" />
      <header class="chat-head">
        <div class="chat-heading">
          <h2>{{ activeSession?.name || '未选择会话' }}</h2>
          <span v-if="activeSession" class="chat-id" :title="activeSession.id">
            {{ activeSession.id }}
          </span>
        </div>
        <div v-if="activeSession" class="chat-meta">
          <el-tag
            size="small"
            effect="plain"
            :type="sessionStateTagType(activeSession.state)"
            class="chat-state"
          >
            {{ activeSession.state }}
          </el-tag>
          <span class="chat-count"> {{ activeSession.messageCount }} 条消息 </span>
        </div>
      </header>
      <div class="chat-body">
        <template v-if="activeSession">
          <main class="chat-stream">
            <header class="stream-bar">
              <div class="stream-state">
                <el-tag
                  size="small"
                  effect="plain"
                  :type="statusTag(stream.status.value)"
                  class="stream-tag"
                >
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
            <MessageTimeline
              :history="stream.history.value"
              :events="stream.events.value"
              :can-decide="canDecide"
              @approve="handleApprove"
              @reject="handleReject"
            />
          </main>
        </template>
        <template v-else>
          <el-empty description="创建一个会话，开始与 Agent 对话" :image-size="96">
            <el-button type="primary" :icon="ChatDotRound" @click="handleCreate"
              >新建会话</el-button
            >
          </el-empty>
        </template>
      </div>

      <footer class="chat-composer">
        <ComposerBox
          ref="composerRef"
          v-model="input"
          :model="activeSession?.model || ''"
          :disabled="!activeSession"
          :sending="sending"
          @send="handleSend"
          @stop="handleStop"
        />
      </footer>
    </main>
  </section>
</template>

<style scoped>
.chat-page {
  display: grid;
  /* 对齐原型 .chat-pane：会话栏 240px + 14px 间距；收起态 56px */
  grid-template-columns: 240px minmax(0, 1fr);
  gap: 14px;
  height: 100%;
  min-height: 0;
  transition: grid-template-columns 0.2s ease;
}

.chat-page.ctx-collapsed {
  grid-template-columns: 56px minmax(0, 1fr);
}

.chat-main {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}

.chat-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 2px 8px;
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
  padding: 0 2px 8px;
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
  color: var(--app-faint);
}

.stream-actions {
  flex: none;
}

.stream-error {
  margin: 0;
  padding: 6px 10px;
  border-radius: 6px;
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 12px;
}

.chat-composer {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 12px;
}
</style>
