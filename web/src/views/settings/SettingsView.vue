<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Check, InfoFilled, Key, Lock, Refresh, Setting } from '@element-plus/icons-vue'

import TechEmptyState from '@/components/TechEmptyState.vue'
import {
  describeSettingsError,
  getSettings,
  patchSettings,
} from '@/api/settings'
import type { SettingsGroupView, SettingsItem } from '@/api/types'
import { useAuthStore } from '@/stores/auth'
import { hasPermission, PERM, permissionsOf } from '@/utils/permission'

const auth = useAuthStore()

/** 配置分组目录（后端 settings.Groups() 稳定顺序）。 */
const GROUPS: Array<{ key: string; title: string; desc: string }> = [
  { key: 'model', title: '模型', desc: 'LLM 提供方与调用参数' },
  { key: 'skill', title: 'Skill', desc: '内置与自定义 Skill、创建通道' },
  { key: 'approval', title: '审批', desc: '写操作审批模式与安全闸门' },
  { key: 'memory', title: '记忆', desc: '长期记忆作用域与容量' },
  { key: 'retention', title: '保留', desc: '自动清理策略' },
  { key: 'mcp', title: 'MCP', desc: '外部工具接入' },
  { key: 'ops', title: '运维', desc: 'Kubernetes 集群与扫描参数' },
]

const canRead = computed(() => hasPermission(permissionsOf(auth.role), PERM.settingsRead))
const canWrite = computed(() => hasPermission(permissionsOf(auth.role), PERM.settingsWrite))

const activeGroup = ref('model')
const view = ref<SettingsGroupView | null>(null)
const loading = ref(false)
const saving = ref(false)
const savingError = ref('')
const drafts = ref<Record<string, unknown>>({})
const dirty = ref<Record<string, boolean>>({})
const clearRequested = ref<Record<string, boolean>>({})

const groupMeta = computed(
  () => GROUPS.find((g) => g.key === activeGroup.value) ?? GROUPS[0],
)

const appliedCount = computed(
  () => view.value?.items.reduce((sum, item) => sum + (item.hasValue ? 1 : 0), 0) ?? 0,
)

const dirtyCount = computed(
  () => Object.values(dirty.value).filter(Boolean).length,
)

const hasSensitiveItems = computed(
  () => view.value?.items.some((item) => item.sensitive) ?? false,
)

const encryptionUnavailable = computed(
  () => hasSensitiveItems.value && view.value?.encryptionAvailable === false,
)

/** thinking/scan_thinking 是固定形状的思考开关，不从任意 JSON 文本推导。 */
function isThinkingSwitch(item: SettingsItem): boolean {
  return item.key === 'thinking' || item.key === 'scan_thinking'
}

function thinkingEnabled(value: unknown): boolean {
  return Boolean(
    value
    && typeof value === 'object'
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'enabled',
  )
}

/** 把服务端视图映射成可编辑草稿；敏感键一律以空串起步（不回显）。 */
function initDrafts(next: SettingsGroupView): void {
  const nextDrafts: Record<string, unknown> = {}
  const nextDirty: Record<string, boolean> = {}
  const nextClear: Record<string, boolean> = {}
  for (const item of next.items) {
    nextDirty[item.key] = false
    nextClear[item.key] = false
    if (item.sensitive) {
      nextDrafts[item.key] = ''
      continue
    }
    if (item.kind === 'stringList') {
      nextDrafts[item.key] = (item.value as string[] | undefined ?? []).join(', ')
      continue
    }
    if (isThinkingSwitch(item)) {
      nextDrafts[item.key] = thinkingEnabled(item.value)
      continue
    }
    if (item.kind === 'json') {
      nextDrafts[item.key] =
        item.value === null || item.value === undefined
          ? ''
          : JSON.stringify(item.value, null, 2)
      continue
    }
    nextDrafts[item.key] = item.value
  }
  drafts.value = nextDrafts
  dirty.value = nextDirty
  clearRequested.value = nextClear
  savingError.value = ''
}

function exampleOf(item: SettingsItem): string {
  return item.example ?? ''
}

function placeholderOf(item: SettingsItem): string {
  if (item.sensitive) {
    return '输入新值后保存以覆盖（不回显当前内容）'
  }
  if (item.kind === 'stringList') {
    return '逗号、分号或空格分隔'
  }
  if (item.kind === 'duration') {
    return exampleOf(item) || '例如 30s、5m'
  }
  return exampleOf(item)
}

function rangeHint(item: SettingsItem): string {
  if (item.kind !== 'int' && item.kind !== 'float') {
    return ''
  }
  if (item.min !== undefined && item.max !== undefined) {
    return `取值范围：${item.min} - ${item.max}`
  }
  if (item.min !== undefined) {
    return `取值范围：${item.min} 以上`
  }
  if (item.max !== undefined) {
    return `取值范围：${item.max} 以下`
  }
  return ''
}

function formatHint(item: SettingsItem): string {
  if (isThinkingSwitch(item)) {
    return ''
  }
  if (item.kind === 'duration') {
    return `时长格式：${exampleOf(item) || '30s、5m'}`
  }
  if (item.kind === 'stringList') {
    return `多值格式：${exampleOf(item) || '用逗号、分号或空格分隔'}`
  }
  if (item.kind === 'json' && item.example) {
    return `JSON 示例：${item.example}`
  }
  return ''
}

function shouldShowFormatHint(item: SettingsItem): boolean {
  return !isThinkingSwitch(item) && Boolean(formatHint(item))
}

function jsonPlaceholder(item: SettingsItem): string {
  if (item.sensitive) {
    return item.example
      ? `输入新 JSON 后保存以覆盖；示例：${item.example}`
      : '输入新 JSON 后保存以覆盖（不回显当前内容）'
  }
  return item.example || '请输入合法 JSON'
}

function setDraft(key: string, value: unknown): void {
  drafts.value[key] = value
  dirty.value[key] = true
  savingError.value = ''
}

function requestClear(item: SettingsItem): void {
  drafts.value[item.key] = ''
  dirty.value[item.key] = true
  clearRequested.value[item.key] = true
  savingError.value = ''
}

function draftAsBool(key: string): boolean {
  return Boolean(drafts.value[key])
}

function draftAsNumber(key: string): number | undefined {
  const value = drafts.value[key]
  if (typeof value === 'number') {
    return value
  }
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value)
    return Number.isNaN(parsed) ? undefined : parsed
  }
  return undefined
}

function draftAsString(key: string): string {
  const value = drafts.value[key]
  return typeof value === 'string' ? value : value == null ? '' : String(value)
}

/** 逗号/分号/空白分隔的自定义词表（stringList 控件）。 */
function splitList(text: string): string[] {
  return text
    .split(/[,，;；\s]+/)
    .map((part) => part.trim())
    .filter(Boolean)
}

/** 校验并解析文本草稿为 JSON（无输入视为 null）。 */
function parseJsonText(text: string, key: string): unknown {
  const trimmed = text.trim()
  if (trimmed === '') {
    return null
  }
  try {
    return JSON.parse(trimmed)
  } catch {
    throw new Error(`配置项 ${key} 不是合法 JSON`)
  }
}

/** 只提交“出现过的键”（PATCH 语义）：敏感键留空视为未改动，避免误删。 */
function buildPatch(): Record<string, unknown> {
  const patch: Record<string, unknown> = {}
  if (!view.value) {
    return patch
  }
  for (const item of view.value.items) {
    if (!dirty.value[item.key]) {
      continue
    }
    if (item.sensitive) {
      if (clearRequested.value[item.key]) {
        patch[item.key] = '' // 空值 = 清空敏感配置
        continue
      }
      const text = draftAsString(item.key).trim()
      if (text === '') {
        continue // 未改动的敏感键不回写
      }
      if (item.kind === 'json') {
        try {
          patch[item.key] = JSON.parse(text)
        } catch {
          throw new Error(`配置项 ${item.key} 不是合法 JSON`)
        }
      } else {
        patch[item.key] = text
      }
      continue
    }
    if (isThinkingSwitch(item)) {
      patch[item.key] = { type: draftAsBool(item.key) ? 'enabled' : 'disabled' }
      continue
    }
    if (item.kind === 'json') {
      patch[item.key] = parseJsonText(draftAsString(item.key), item.key)
      continue
    }
    if (item.kind === 'stringList') {
      patch[item.key] = splitList(draftAsString(item.key))
      continue
    }
    patch[item.key] = drafts.value[item.key]
  }
  return patch
}

async function loadGroup(): Promise<void> {
  loading.value = true
  savingError.value = ''
  try {
    const next = await getSettings(activeGroup.value)
    view.value = next
    initDrafts(next)
  } catch (error) {
    ElMessage.error(describeSettingsError('加载配置', error))
  } finally {
    loading.value = false
  }
}

async function handleSave(): Promise<void> {
  if (!canWrite.value || !view.value) {
    return
  }
  let patch: Record<string, unknown>
  try {
    patch = buildPatch()
  } catch (error) {
    savingError.value = error instanceof Error ? error.message : '配置格式有误'
    ElMessage.error(savingError.value)
    return
  }
  if (Object.keys(patch).length === 0) {
    ElMessage.info('没有需要保存的改动')
    return
  }
  saving.value = true
  savingError.value = ''
  try {
    const next = await patchSettings(activeGroup.value, patch)
    view.value = next
    initDrafts(next)
    ElMessage.success('配置已保存')
  } catch (error) {
    savingError.value = describeSettingsError('保存配置', error)
    ElMessage.error(savingError.value)
  } finally {
    saving.value = false
  }
}

watch(activeGroup, loadGroup)
onMounted(loadGroup)
</script>

<template>
  <section class="settings-page">
    <TechEmptyState
      v-if="!canRead"
      title="无配置中心访问权限"
      hint="需要 settings.read 权限才能查看系统配置。"
    />
    <template v-else>
      <div class="page-head">
        <h2 class="title">
          <el-icon class="title-icon"><Setting /></el-icon>
          配置中心
        </h2>
        <span class="desc">模型、Skill、审批、记忆与集群参数，写操作即时生效。</span>
        <el-button text class="refresh" :icon="Refresh" :loading="loading" @click="loadGroup">
          刷新
        </el-button>
      </div>

      <div class="settings-layout">
        <aside class="settings-rail" aria-label="配置分组">
          <button
            v-for="group in GROUPS"
            :key="group.key"
            type="button"
            class="rail-item"
            :class="{ active: activeGroup === group.key }"
            @click="activeGroup = group.key"
          >
            <span class="rail-title">{{ group.title }}</span>
            <span class="rail-desc">{{ group.desc }}</span>
          </button>
        </aside>

        <div class="settings-content">
          <div class="card form-card">
            <div class="form-head">
              <div class="row form-head-row">
                <div class="grow">
                  <div class="sub strong">{{ groupMeta.title }}</div>
                  <div class="muted small">{{ groupMeta.desc }}</div>
                  <div class="faint small">
                    <span class="mono">已配置 {{ appliedCount }}</span>
                    <span v-if="dirtyCount > 0" class="warn-tag"> · 待保存 {{ dirtyCount }}</span>
                  </div>
                </div>
                <el-button
                  v-if="canWrite"
                  type="primary"
                  :icon="Check"
                  :loading="saving"
                  @click="handleSave"
                >
                  保存
                </el-button>
              </div>
            </div>

            <div v-if="loading" class="muted center-hint">加载中…</div>
            <div v-else class="field-grid">
              <el-alert
                v-if="encryptionUnavailable"
                class="encryption-alert"
                type="warning"
                :closable="false"
                show-icon
                title="敏感配置暂不可保存"
                description="服务端尚未配置 K8S_AI_ENC_KEY。请联系平台管理员完成加密密钥配置后再保存 API Key、kubeconfig 或 MCP 配置。"
              />
              <div v-for="item in view?.items ?? []" :key="item.key" class="field">
                <div class="field-meta">
                  <div class="row field-label">
                    <span class="mono strong">{{ item.key }}</span>
                    <span v-if="item.sensitive" class="pill" :class="item.hasValue ? 'ok' : 'mute'">
                      <el-icon class="tiny"><Key /></el-icon>
                      {{ item.hasValue ? '已配置' : '未配置' }}
                    </span>
                    <span v-if="item.source" class="faint mono small">← {{ item.source }}</span>
                  </div>
                  <div v-if="item.desc" class="muted small desc-line">{{ item.desc }}</div>
                  <div v-if="shouldShowFormatHint(item)" class="faint small example-line">
                    <el-icon class="tiny"><InfoFilled /></el-icon>
                    {{ formatHint(item) }}
                  </div>
                  <div v-else-if="rangeHint(item)" class="faint small example-line">
                    {{ rangeHint(item) }}
                  </div>
                </div>

                <div class="field-control">
                  <template v-if="item.sensitive">
                    <div v-if="item.kind === 'json'" class="secret-textarea">
                      <el-input
                        :model-value="draftAsString(item.key)"
                        type="textarea"
                        :rows="4"
                        spellcheck="false"
                        :disabled="!view?.encryptionAvailable"
                        :placeholder="jsonPlaceholder(item)"
                        @update:model-value="(value: string) => setDraft(item.key, value)"
                      />
                    </div>
                    <div v-else class="secret-row">
                      <el-input
                        :model-value="draftAsString(item.key)"
                        type="password"
                        show-password
                        spellcheck="false"
                        :disabled="!view?.encryptionAvailable"
                        :placeholder="placeholderOf(item)"
                        @update:model-value="(value: string) => setDraft(item.key, value)"
                      />
                      <el-button
                        v-if="item.hasValue"
                        text
                        type="danger"
                        :icon="Lock"
                        :disabled="!view?.encryptionAvailable"
                        @click="requestClear(item)"
                      >
                        清除
                      </el-button>
                    </div>
                  </template>

                  <el-switch
                    v-else-if="isThinkingSwitch(item)"
                    :model-value="draftAsBool(item.key)"
                    active-text="启用"
                    inactive-text="停用"
                    :aria-label="`切换 ${item.key}`"
                    @change="(value: string | number | boolean) => setDraft(item.key, Boolean(value))"
                  />

                  <el-switch
                    v-else-if="item.kind === 'bool'"
                    :model-value="draftAsBool(item.key)"
                    :aria-label="`切换 ${item.key}`"
                    @change="(value: string | number | boolean) => setDraft(item.key, Boolean(value))"
                  />

                  <el-select
                    v-else-if="item.enum?.length"
                    :model-value="draftAsString(item.key)"
                    class="select"
                    placeholder="请选择"
                    @update:model-value="(value: string) => setDraft(item.key, value)"
                  >
                    <el-option v-for="option in item.enum" :key="option" :label="option" :value="option" />
                  </el-select>

                  <el-input-number
                    v-else-if="item.kind === 'int' || item.kind === 'float'"
                    :model-value="draftAsNumber(item.key)"
                    :min="item.min"
                    :max="item.max"
                    :step="item.kind === 'int' ? 1 : 0.1"
                    :precision="item.kind === 'int' ? 0 : 2"
                    :aria-label="`设置 ${item.key}`"
                    @update:model-value="(value: number | undefined) => setDraft(item.key, value)"
                  />

                  <el-input
                    v-else-if="item.kind === 'stringList'"
                    :model-value="draftAsString(item.key)"
                    spellcheck="false"
                    :placeholder="placeholderOf(item)"
                    @update:model-value="(value: string) => setDraft(item.key, value)"
                  />

                  <el-input
                    v-else-if="item.kind === 'json'"
                    :model-value="draftAsString(item.key)"
                    type="textarea"
                    :rows="4"
                    spellcheck="false"
                    :placeholder="jsonPlaceholder(item)"
                    @update:model-value="(value: string) => setDraft(item.key, value)"
                  />

                  <el-input
                    v-else
                    :model-value="draftAsString(item.key)"
                    spellcheck="false"
                    :placeholder="placeholderOf(item)"
                    @update:model-value="(value: string) => setDraft(item.key, value)"
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.settings-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
  flex: 1;
  width: 100%;
  min-width: 0;
  min-height: 0;
}

.page-head {
  display: flex;
  align-items: center;
  gap: 12px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  padding: 14px 16px;
}

.page-head .title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  margin: 0;
  font-size: 16px;
}

.title-icon {
  color: var(--app-accent-2);
}

.page-head .desc {
  font-size: 12.5px;
  color: var(--app-muted);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.refresh {
  margin-left: auto;
}

.settings-layout {
  display: flex;
  gap: 14px;
  flex: 1;
  width: 100%;
  min-width: 0;
  min-height: 0;
  align-items: stretch;
}

.settings-rail {
  display: flex;
  flex-direction: column;
  gap: 6px;
  flex: none;
  width: 220px;
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
  padding: 8px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}

.rail-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  align-items: flex-start;
  width: 100%;
  padding: 9px 10px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--app-sidebar-text);
  text-align: left;
  cursor: pointer;
  transition: background 0.15s ease, border-color 0.15s ease;
}

.rail-item:hover {
  background: var(--app-surface-hover);
}

.rail-item.active {
  background: var(--app-surface-2);
  border-color: var(--app-hairline);
}

.rail-title {
  font-size: 13px;
  font-weight: 650;
  line-height: 1.4;
}

.rail-item.active .rail-title {
  color: var(--app-accent);
}

.rail-desc {
  font-size: 11.5px;
  line-height: 1.4;
  color: var(--app-muted);
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}

.settings-content {
  flex: 1;
  width: 100%;
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
  display: flex;
  flex-direction: column;
}

.card {
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}

.form-card {
  flex: none;
  width: 100%;
  padding: 16px;
  min-width: 0;
}

.form-head {
  position: sticky;
  top: 0;
  z-index: 2;
  margin: -16px -16px 16px;
  padding: 16px;
  background: var(--app-canvas);
  backdrop-filter: blur(14px) saturate(120%);
  border-bottom: 1px solid var(--app-hairline);
  border-radius: var(--app-radius) var(--app-radius) 0 0;
  box-shadow: 0 10px 18px -18px rgba(0, 0, 0, 0.9);
}

.form-head-row {
  width: 100%;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.grow {
  min-width: 0;
  flex: 1;
}

.sub {
  font-size: 13px;
}

.strong {
  font-weight: 650;
}

.pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: none;
  padding: 2px 8px;
  font-size: 11px;
  line-height: 1.4;
  border-radius: 999px;
  background: rgba(99, 102, 241, 0.12);
  color: var(--app-accent);
}

.pill.ok {
  background: rgba(52, 211, 153, 0.12);
  color: #34d399;
}

.pill.mute {
  background: var(--app-surface-hover);
  color: var(--app-muted);
}

.tiny {
  font-size: 12px;
}

.field-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  width: 100%;
  min-width: 0;
  gap: 10px;
}

.field {
  display: grid;
  grid-template-columns: minmax(220px, 0.7fr) minmax(0, 1.3fr);
  align-items: center;
  gap: 10px 24px;
  width: 100%;
  min-width: 0;
  padding: 14px 16px;
  border: 1px solid var(--app-hairline);
  border-radius: 8px;
  background: var(--app-surface-2);
}

.field-meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.field-control {
  display: flex;
  align-items: center;
  width: 100%;
  min-width: 0;
}

.field-control > .el-input,
.field-control > .el-input-number,
.field-control > .el-select,
.field-control > .secret-textarea,
.field-control > .secret-row {
  width: 100%;
}

.field-control > .el-switch {
  flex: none;
  align-self: flex-start;
}

.field-control > .el-input-number {
  width: 100%;
  max-width: 100%;
}

.field-control > .el-select {
  width: 100%;
  max-width: 100%;
}

.field-label {
  flex-wrap: wrap;
  gap: 6px;
}

.desc-line {
  line-height: 1.5;
}

.example-line {
  display: flex;
  align-items: flex-start;
  gap: 5px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.encryption-alert {
  width: 100%;
}

.select {
  width: 100%;
}

.secret-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.secret-textarea {
  width: 100%;
}

.mono {
  font-family: var(--app-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
}

.faint {
  color: var(--app-faint, var(--app-muted));
  opacity: 0.8;
}

.muted {
  color: var(--app-muted);
}

.center-hint {
  padding: 24px 8px;
  text-align: center;
  font-size: 12.5px;
}

.small {
  font-size: 12px;
}

.warn-tag {
  color: #fbbf24;
}

@media (max-width: 1100px) {
  .field {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
  }
}

@media (max-width: 900px) {
  .settings-page {
    flex: none;
  }
  .page-head {
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .page-head .title {
    white-space: nowrap;
  }
  .page-head .desc {
    order: 3;
    flex: 1 0 100%;
    white-space: normal;
  }
  .settings-layout {
    flex-direction: column;
  }
  .settings-rail {
    flex-direction: row;
    flex-wrap: wrap;
    width: 100%;
    overflow: visible;
  }
  .rail-item {
    width: auto;
    min-width: 120px;
  }
  .settings-content {
    overflow: visible;
    scrollbar-gutter: auto;
  }
}
</style>
