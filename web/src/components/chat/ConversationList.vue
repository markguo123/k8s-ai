<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChatDotRound, Delete, EditPen, Fold, Plus, Refresh, Expand } from '@element-plus/icons-vue'

import type { SessionSummary } from '@/api/sessions'
import { formatRelativeTime, sessionStateTagType } from '@/utils/format'

const props = defineProps<{
  sessions: SessionSummary[]
  activeId: string | null
  loading: boolean
  collapsed?: boolean
}>()

const emit = defineEmits<{
  (e: 'create'): void
  (e: 'select', id: string): void
  (e: 'delete', session: SessionSummary): void
  (e: 'rename', payload: { id: string; name: string }): void
  (e: 'refresh'): void
  (e: 'toggle'): void
}>()

const editingId = ref<string | null>(null)
const draftName = ref('')

function startRename(session: SessionSummary): void {
  editingId.value = session.id
  draftName.value = session.name || session.id
}

function commitRename(id: string): void {
  const name = draftName.value.trim()
  editingId.value = null
  if (!name) {
    return
  }
  emit('rename', { id, name })
}

function cancelRename(): void {
  editingId.value = null
}

const collapsed = computed(() => props.collapsed === true)
</script>

<template>
  <aside class="conversation-list" :class="{ collapsed }">
    <div class="list-head">
      <template v-if="!collapsed">
        <div class="list-title">
          <h3>会话</h3>
          <span class="list-count">{{ sessions.length }}</span>
        </div>
        <div class="list-actions">
          <el-tooltip content="刷新">
            <el-button :icon="Refresh" circle plain size="small" @click="emit('refresh')" />
          </el-tooltip>
          <el-tooltip content="新建会话">
            <el-button :icon="Plus" circle type="primary" size="small" @click="emit('create')" />
          </el-tooltip>
        </div>
      </template>
      <template v-else>
        <el-tooltip content="新建会话" placement="right">
          <el-button :icon="Plus" circle type="primary" size="small" @click="emit('create')" />
        </el-tooltip>
      </template>
      <button
        type="button"
        class="ctx-toggle"
        :title="collapsed ? '展开会话栏' : '收起会话栏'"
        @click="emit('toggle')"
      >
        <el-icon><Fold v-if="!collapsed" /><Expand v-else /></el-icon>
      </button>
    </div>

    <div v-if="!collapsed" v-loading="loading" class="list-body">
      <el-empty v-if="!loading && sessions.length === 0" description="暂无会话" :image-size="72" />
      <ul v-else class="session-items">
        <li
          v-for="session in sessions"
          :key="session.id"
          class="session-item"
          :class="{ active: session.id === activeId }"
          @click="emit('select', session.id)"
        >
          <div class="session-main">
            <el-icon class="session-icon"><ChatDotRound /></el-icon>
            <div class="session-text">
              <template v-if="editingId === session.id">
                <el-input
                  v-model="draftName"
                  size="small"
                  maxlength="80"
                  autofocus
                  @keyup.enter="commitRename(session.id)"
                  @keyup.esc="cancelRename"
                  @blur="commitRename(session.id)"
                />
              </template>
              <template v-else>
                <strong class="session-name" :title="session.name || session.id">
                  {{ session.name || session.id }}
                </strong>
                <div class="session-meta">
                  <el-tag
                    size="small"
                    effect="plain"
                    :type="sessionStateTagType(session.state)"
                    class="session-state"
                  >
                    {{ session.state }}
                  </el-tag>
                  <span>{{ session.messageCount }} 条</span>
                  <span>{{ formatRelativeTime(session.lastModified) }}</span>
                </div>
              </template>
            </div>
          </div>
          <div class="session-tools">
            <el-tooltip content="重命名">
              <el-button
                :icon="EditPen"
                circle
                size="small"
                text
                @click.stop="startRename(session)"
              />
            </el-tooltip>
            <el-tooltip content="删除">
              <el-button
                :icon="Delete"
                circle
                size="small"
                text
                type="danger"
                @click.stop="emit('delete', session)"
              />
            </el-tooltip>
          </div>
        </li>
      </ul>
    </div>
  </aside>
</template>

<style scoped>
.conversation-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  min-width: 0;
  overflow: visible;
}

.list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 4px 2px;
  flex: none;
}

.list-title {
  display: flex;
  align-items: center;
  gap: 8px;
}

.list-title h3 {
  margin: 0;
  font-size: 14.5px;
  font-weight: 650;
}

.list-count {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: var(--app-surface-2);
  color: var(--app-muted);
  font-size: 11px;
  line-height: 18px;
  text-align: center;
}

.list-actions {
  display: flex;
  gap: 4px;
}

.list-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

.session-items {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.session-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  cursor: pointer;
  transition:
    background 0.15s ease,
    border-color 0.15s ease;
}

.session-item:hover {
  border-color: var(--app-accent);
}

.session-item.active {
  background: var(--app-accent-soft);
  border-color: var(--app-accent);
}

.session-main {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
  flex: 1;
}

.session-icon {
  flex: none;
  margin-top: 2px;
  color: var(--app-muted);
}

.session-item.active .session-icon {
  color: var(--app-accent);
}

.session-text {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  flex: 1;
}

.session-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12.5px;
  font-weight: 600;
}

.session-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 12px;
  color: var(--app-muted);
}

.session-state {
  text-transform: capitalize;
}

.session-tools {
  display: none;
  align-items: center;
  gap: 2px;
  flex: none;
}

.session-item:hover .session-tools,
.session-item.active .session-tools {
  display: flex;
}

.conversation-list.collapsed .list-head {
  flex-direction: column;
  justify-content: center;
  gap: 6px;
  padding: 8px 0;
}

.ctx-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  flex: none;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--app-muted);
  cursor: pointer;
  font-size: 13px;
  transition:
    color 0.15s ease,
    background 0.15s ease;
}

.ctx-toggle:hover {
  color: var(--app-text);
  background: var(--app-surface-2);
}
</style>
