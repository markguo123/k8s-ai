<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  ChatDotRound,
  Delete,
  Download,
  EditPen,
  Expand,
  Fold,
  MoreFilled,
  Plus,
  Refresh,
  Search,
  Star,
  StarFilled,
} from '@element-plus/icons-vue'

import type { SessionSummary } from '@/api/sessions'
import TechEmptyState from '@/components/TechEmptyState.vue'
import { formatRelativeTime, sessionStateLabel, sessionStateTone } from '@/utils/format'

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
  (e: 'pin', payload: { id: string; pinned: boolean }): void
  (e: 'export', session: SessionSummary): void
  (e: 'refresh'): void
  (e: 'toggle'): void
}>()

const editingId = ref<string | null>(null)
const draftName = ref('')
const keyword = ref('')

const collapsed = computed(() => props.collapsed === true)

/** P7.5：搜索按标题与会话 id 做大小写不敏感过滤。 */
const filteredSessions = computed(() => {
  const query = keyword.value.trim().toLowerCase()
  if (!query) {
    return props.sessions
  }
  return props.sessions.filter(
    (session) =>
      session.name.toLowerCase().includes(query) || session.id.toLowerCase().includes(query),
  )
})

/**
 * P7.5：置顶组与普通组之间画一条分隔线（对齐 DeepSeek 的会话栏分组）。
 * 后端已保证置顶排最前，这里只需识别"第一条非置顶且前一条是置顶"的那张卡。
 */
function isGroupStart(index: number): boolean {
  if (index <= 0) {
    return false
  }
  const prev = filteredSessions.value[index - 1]
  const current = filteredSessions.value[index]
  return Boolean(prev?.pinned) && !current?.pinned
}

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

function onMenuCommand(session: SessionSummary, command: string): void {
  switch (command) {
    case 'rename':
      startRename(session)
      break
    case 'pin':
      emit('pin', { id: session.id, pinned: !session.pinned })
      break
    case 'export':
      emit('export', session)
      break
    case 'delete':
      emit('delete', session)
      break
  }
}
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

    <el-input
      v-if="!collapsed"
      v-model="keyword"
      class="list-search"
      size="small"
      clearable
      placeholder="搜索会话"
      :prefix-icon="Search"
    />

    <div v-if="!collapsed" v-loading="loading" class="list-body">
      <TechEmptyState
        v-if="!loading && filteredSessions.length === 0"
        compact
        :title="sessions.length === 0 ? '暂无会话' : '没有匹配的会话'"
        hint="点右上角 + 新建一个会话"
      />
      <ul v-else class="session-items">
        <li
          v-for="(session, index) in filteredSessions"
          :key="session.id"
          class="session-item rise-in"
          :class="{
            active: session.id === activeId,
            pinned: session.pinned,
            'group-start': isGroupStart(index),
          }"
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
                <div class="session-top">
                  <el-icon v-if="session.pinned" class="session-pin" title="已置顶"
                    ><StarFilled
                  /></el-icon>
                  <strong class="session-name" :title="session.name || session.id">
                    {{ session.name || session.id }}
                  </strong>
                  <span
                    class="state-pill"
                    :class="`tone-${sessionStateTone(session.state)}`"
                    :title="session.state"
                  >
                    {{ sessionStateLabel(session.state) }}
                  </span>
                </div>
                <p class="session-meta">
                  {{ session.messageCount }} 条 · {{ formatRelativeTime(session.lastModified) }}
                </p>
              </template>
            </div>
          </div>
          <div class="session-tools">
            <el-dropdown
              trigger="click"
              placement="bottom-end"
              @click.stop
              @command="(command: string) => onMenuCommand(session, command)"
            >
              <el-button :icon="MoreFilled" circle size="small" text aria-label="会话操作" />
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item :icon="session.pinned ? Star : StarFilled" command="pin">
                    {{ session.pinned ? '取消置顶' : '置顶' }}
                  </el-dropdown-item>
                  <el-dropdown-item :icon="EditPen" command="rename">重命名</el-dropdown-item>
                  <el-dropdown-item :icon="Download" command="export">导出 Markdown</el-dropdown-item>
                  <el-dropdown-item :icon="Delete" command="delete" divided>删除</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
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
  gap: 12px;
  height: 100%;
  min-width: 0;
  min-height: 0;
  /* P7.6 海拔：会话列表是玻璃层；右侧发丝线 + 微阴影与聊天画布拉开层级 */
  padding: 16px;
  border: 1px solid var(--app-hairline);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  backdrop-filter: blur(16px) saturate(120%);
  box-shadow: var(--app-panel-shadow);
  overflow: visible;
}

.conversation-list.collapsed {
  padding: 8px 4px;
}

.list-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 2px 2px 4px;
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
  background: var(--app-accent-soft);
  color: var(--app-accent);
  font-size: 11px;
  line-height: 18px;
  text-align: center;
}

.list-actions {
  display: flex;
  gap: 4px;
}

.list-search {
  flex: none;
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

/* P7.6 会话卡：轻微圆角 + 极弱底；hover 变亮 + 青色发光描边 + 左缘流光 */
.session-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: var(--app-card-bg);
  cursor: pointer;
  transition:
    background 0.16s ease,
    border-color 0.16s ease,
    box-shadow 0.16s ease;
}

.session-item:hover {
  background: var(--app-card-bg-hover);
  border-color: var(--app-accent-2-border);
  box-shadow: var(--app-glow-cyan);
}

.session-item:hover::before {
  content: '';
  position: absolute;
  left: 0;
  top: 8%;
  bottom: 8%;
  width: 2px;
  border-radius: 2px;
  background: linear-gradient(180deg, transparent, var(--app-accent-2), transparent);
}

.session-item.active {
  background: var(--app-accent-soft);
  border-color: var(--app-accent-border);
  box-shadow: -6px 0 22px -14px var(--app-accent);
}

.session-item.pinned {
  border-color: var(--app-accent-2-border);
}

/*
 * P7.5：置顶组与普通组之间的分隔带（对齐 DeepSeek 的会话栏分组）。
 * 除了基础 8px 行距，再给首条普通会话加 14px 上外边距，把两段真正拉开；
 * 线条走渐变（两端淡出）而不是通铺实线，在窄侧栏里比均匀细线更清晰也更轻。
 * 用 ::after 承载；::before 已用于 hover 左缘流光，不能复用。
 */
.session-item.group-start {
  margin-top: 14px;
}

.session-item.group-start::after {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  /* 落在「8px 行距 + 14px 外边距」共 22px 分隔带的正中 */
  top: -11px;
  height: 1px;
  background: linear-gradient(
    90deg,
    transparent,
    rgba(125, 145, 185, 0.45),
    transparent
  );
  pointer-events: none;
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

.session-pin {
  flex: none;
  color: var(--app-accent-2);
  font-size: 12px;
}

.session-item.active .session-icon {
  color: var(--app-accent);
}

.session-text {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  flex: 1;
}

/* 第一行：标题 + 状态胶囊（胶囊靠右、不参与标题的省略号计算） */
.session-top {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.session-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12.5px;
  font-weight: 600;
}

.session-top .state-pill {
  flex: none;
  /* 会话栏只有 240px：胶囊收紧一点，把宽度让给标题 */
  padding: 0 6.5px;
  height: 17px;
  font-size: 10.5px;
  line-height: 17px;
  letter-spacing: 0;
}

/* 第二行：消息数 · 时间（单行省略，不再换行成竖条） */
.session-meta {
  margin: 0;
  /* 右侧预留悬浮操作的宽度：hover 时文字不跳动 */
  padding-right: 52px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11.5px;
  color: var(--app-faint);
}

.session-tools {
  position: absolute;
  right: 6px;
  bottom: 6px;
  display: none;
  align-items: center;
  gap: 2px;
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
