<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound } from '@element-plus/icons-vue'

import type { SessionSummary } from '@/api/sessions'
import { createSession, deleteSession, listSessions, renameSession } from '@/api/sessions'
import ConversationList from '@/components/chat/ConversationList.vue'
import { sessionStateTagType } from '@/utils/format'

const ACTIVE_SESSION_KEY = 'k8s_ai_active_session'

const sessions = ref<SessionSummary[]>([])
const loading = ref(false)
const activeId = ref<string | null>(sessionStorage.getItem(ACTIVE_SESSION_KEY))

const activeSession = computed(
  () => sessions.value.find((session) => session.id === activeId.value) ?? null,
)
const emptyText = computed(() =>
  activeSession.value
    ? `已选择会话，消息时间线将在 P2.5（SSE 事件流）落地`
    : '创建一个会话，开始与 Agent 对话',
)

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
        <el-empty :description="emptyText" :image-size="96">
          <template v-if="!activeSession">
            <el-button type="primary" :icon="ChatDotRound" @click="handleCreate">新建会话</el-button>
          </template>
        </el-empty>
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
  align-items: center;
  justify-content: center;
  flex: 1;
  min-height: 0;
}
</style>
