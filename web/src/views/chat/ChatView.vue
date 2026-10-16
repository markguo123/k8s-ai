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
  pinSession,
  renameSession,
  sendMessage,
} from '@/api/sessions'
import type { AgentTurnData } from '@/api/sse'
import ConversationList from '@/components/chat/ConversationList.vue'
import MessageTimeline from '@/components/chat/MessageTimeline.vue'
import ComposerBox from '@/components/chat/ComposerBox.vue'
import TokenUsageBadge from '@/components/chat/TokenUsageBadge.vue'
import TechEmptyState from '@/components/TechEmptyState.vue'
import { buildSessionMarkdown, markdownFileName } from '@/utils/exportMarkdown'
import { useChatStream, type StreamStatus } from '@/composables/useChatStream'
import { useAuthStore } from '@/stores/auth'
import { addTokenUsage, sessionStateTone } from '@/utils/format'
import { canDecideApproval, hasPermission, PERM, permissionsOf } from '@/utils/permission'
import type { Skill } from '@/api/types'

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
const mobileDrawerOpen = ref(false)
watch(conversationsCollapsed, (collapsed) => {
  localStorage.setItem(CONVERSATIONS_KEY, collapsed ? '1' : '0')
})

const MENTION_RE = /@([a-zA-Z0-9][\w.-]*)/g

/**
 * 从输入中剥离已识别的 `@技能名` 引用：
 * - 命中的名称进入 skills 列表（后端强校验启停）；
 * - 未知 @ 串原样保留在查询中，避免误伤普通输入。
 */
function extractSkillMentions(
  query: string,
  skills: Skill[],
): { cleaned: string; names: string[] } {
  const names: string[] = []
  const spans: Array<[number, number]> = []
  for (const match of query.matchAll(MENTION_RE)) {
    const alias = match[1]
    const skill = skills.find((s) => s.name.toLowerCase() === alias.toLowerCase())
    if (!skill) {
      continue
    }
    if (!names.some((n) => n.toLowerCase() === skill.name.toLowerCase())) {
      names.push(skill.name)
    }
    spans.push([match.index, match.index + match[0].length])
  }
  if (spans.length === 0) {
    return { cleaned: query, names }
  }
  const parts: string[] = []
  let cursor = 0
  for (const [start, end] of spans) {
    parts.push(query.slice(cursor, start))
    cursor = end
  }
  parts.push(query.slice(cursor))
  return { cleaned: parts.join('').replace(/\s+/g, ' ').trim(), names }
}

const activeSession = computed(
  () => sessions.value.find((session) => session.id === activeId.value) ?? null,
)

/** P2.9 审批操作显隐：只读角色不展示审批按钮（后端仍独立强校验）。 */
const canDecide = computed(() => canDecideApproval(auth.role))

/** P3.8.4 @ 技能引用权限：仅具备 skill.read 时开启提示与回填。 */
const canUseSkills = computed(() => hasPermission(permissionsOf(auth.role), PERM.skillRead))

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

/** P7.6 流状态胶囊 tone：对齐全局 .state-pill.tone-*，替代 Element Plus 的扁平 tag。 */
function streamTone(status: StreamStatus): 'running' | 'done' | 'waiting' | 'idle' {
  switch (status) {
    case 'connecting':
      return 'running'
    case 'open':
      return 'done'
    case 'reconnecting':
      return 'waiting'
    case 'closed':
    case 'idle':
    default:
      return 'idle'
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
  mobileDrawerOpen.value = false
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

async function handlePin(payload: { id: string; pinned: boolean }): Promise<void> {
  try {
    await pinSession(payload.id, payload.pinned)
    await loadSessions()
    ElMessage.success(payload.pinned ? '已置顶' : '已取消置顶')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '置顶操作失败')
  }
}

/** P7.5：导出当前会话为 Markdown 文件（前端 Blob 下载，不新增后端接口）。 */
async function handleExport(target: SessionSummary): Promise<void> {
  try {
    const detail = await createSession({ id: target.id })
    const markdown = buildSessionMarkdown(detail.messages ?? [], {
      id: detail.id,
      name: detail.name ?? target.name,
      model: detail.model,
    })
    const blob = new Blob([markdown], { type: 'text/markdown;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = markdownFileName(detail.name ?? target.name)
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '导出会话失败')
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
  const raw = input.value.trim()
  if (!activeId.value || !raw || sending.value) {
    return
  }
  const available = composerRef.value?.availableSkills ?? []
  const { cleaned, names } = extractSkillMentions(raw, available)
  sending.value = true
  input.value = ''
  try {
    await sendMessage(activeId.value, { query: cleaned || raw, skills: names })
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
      :class="{ 'drawer-open': mobileDrawerOpen }"
      :sessions="sessions"
      :active-id="activeId"
      :loading="loading"
      :collapsed="conversationsCollapsed"
      @create="handleCreate"
      @select="selectSession"
      @delete="handleDelete"
      @rename="handleRename"
      @pin="handlePin"
      @export="handleExport"
      @refresh="loadSessions"
      @toggle="conversationsCollapsed = !conversationsCollapsed"
    />
    <div
      v-if="mobileDrawerOpen"
      class="mobile-backdrop"
      aria-hidden="true"
      @click="mobileDrawerOpen = false"
    />
    <main class="chat-main">
      <header class="chat-head">
        <el-button
          class="mobile-menu-btn"
          circle
          plain
          size="small"
          :icon="ChatDotRound"
          aria-label="打开会话列表"
          @click="mobileDrawerOpen = true"
        />
        <div class="chat-heading">
          <h2>{{ activeSession?.name || '未选择会话' }}</h2>
          <span v-if="activeSession" class="chat-id" :title="activeSession.id">
            {{ activeSession.id }}
          </span>
        </div>
        <div v-if="activeSession" class="chat-meta">
          <TokenUsageBadge :token="sessionToken" />
          <span class="state-pill" :class="`tone-${sessionStateTone(activeSession.state)}`">
            {{ activeSession.state }}
          </span>
          <span class="chat-count"> {{ activeSession.messageCount }} 条消息 </span>
        </div>
      </header>
      <div class="chat-body">
        <template v-if="activeSession">
          <main class="chat-stream">
            <header class="stream-bar">
              <div class="stream-state">
                <span class="state-pill" :class="`tone-${streamTone(stream.status.value)}`">
                  {{ statusText }}
                </span>
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
              empty-title="该会话暂无消息"
              empty-hint="在下方输入框描述你的运维问题；Agent 会先思考、再调用工具核实现场"
              @approve="handleApprove"
              @reject="handleReject"
            />
          </main>
        </template>
        <template v-else>
          <TechEmptyState
            title="未选择会话"
            hint="新建一个会话，描述你的运维问题；Agent 会先思考、再调用工具核实现场"
          >
            <el-button type="primary" :icon="ChatDotRound" @click="handleCreate"
              >新建会话</el-button
            >
          </TechEmptyState>
        </template>
      </div>

      <footer class="chat-composer">
        <ComposerBox
          ref="composerRef"
          v-model="input"
          :model="activeSession?.model || ''"
          :disabled="!activeSession"
          :sending="sending"
          :can-use-skills="canUseSkills"
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
  /* 对齐原型 .chat-pane：会话栏 240px + 16px 间距；收起态 56px */
  grid-template-columns: 240px minmax(0, 1fr);
  /* 行必须显式填满容器，否则隐式 auto 行按内容高度撑开，把输入框顶出视口 */
  grid-template-rows: minmax(0, 1fr);
  gap: 16px;
  /* P7.2 经典双栏：由 .app-main（flex 列）约束为"占满可用高度"，消息再多也不把输入框顶下去 */
  flex: 1 1 0;
  min-height: 0;
  overflow: hidden;
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
  padding: 0 4px 10px;
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
  border: 1px solid var(--app-hairline);
  border-radius: 6px;
  background: var(--app-neutral-soft);
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
  /* P7.6 海拔：画布比侧栏/会话列表亮一档 + 微弱内发光，解决"中间一大块黑" */
  background: var(--app-canvas);
  box-shadow: var(--app-canvas-inset);
  border: 1px solid var(--app-hairline);
  border-radius: var(--app-radius);
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

.stream-state .state-pill {
  font-variant: tabular-nums;
}

.stream-last {
  font-size: 12px;
  color: var(--app-faint);
}

.stream-actions {
  flex: none;
}

/* P7.6 断流提示：软色玻璃条 + 左侧高亮条 + 微弱光晕，替代原来的纯色块 */
.stream-error {
  display: flex;
  align-items: center;
  margin: 0 0 8px;
  padding: 7px 12px;
  border: 1px solid var(--app-danger-border);
  border-left: 2px solid var(--app-danger);
  border-radius: var(--app-radius-sm);
  background: var(--app-danger-soft);
  box-shadow: 0 0 18px -8px var(--app-danger);
  color: var(--app-danger);
  font-size: 12px;
}

.chat-composer {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 12px;
}

/* P7.5：移动端（≤768px）会话列表改为抽屉，默认收起、顶栏按钮唤出。 */
.mobile-menu-btn {
  display: none;
  flex: none;
}

.mobile-backdrop {
  display: none;
}

@media (max-width: 768px) {
  .chat-page,
  .chat-page.ctx-collapsed {
    grid-template-columns: minmax(0, 1fr);
  }

  .mobile-menu-btn {
    display: inline-flex;
  }

  .chat-side {
    position: fixed;
    top: 8px;
    bottom: 8px;
    left: 8px;
    width: 300px;
    max-width: 86vw;
    z-index: 50;
    transform: translateX(-110%);
    transition: transform 0.22s ease;
  }

  .chat-side.drawer-open {
    transform: translateX(0);
  }

  .mobile-backdrop {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 40;
    background: rgba(0, 0, 0, 0.45);
    backdrop-filter: blur(2px);
  }
}
</style>
