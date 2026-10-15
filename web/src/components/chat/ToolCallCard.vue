<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowDown, CopyDocument, WarningFilled } from '@element-plus/icons-vue'

import MarkdownView from './MarkdownView.vue'
import YamlBlock from './render/YamlBlock.vue'
import LogBlock from './render/LogBlock.vue'
import StructuredTable from './render/StructuredTable.vue'
import StructuredKv from './render/StructuredKv.vue'
import StructuredEvents from './render/StructuredEvents.vue'
import { maskSensitiveValues, parseJsonLoose, stripUntrusted } from './render/constants'
import copyText from '@/composables/useClipboard'

/**
 * P2.8 ToolCallCard：结构化工具结果卡。
 * - 头部：状态点 + 动作短名 + 命令摘要 + 风险 + 耗时 + 复制 + 展开/收起；
 * - 完成（ok）后结果自动展开并保持，失败则以 error 样式呈现；
 * - create_resource 专属：提交 manifest + dry-run→apply 两段式状态；
 * - Secret：只展示键指纹，值永不回显。
 */

export interface CreateResourceState {
  manifest?: string
  dryRun?: boolean
  applied?: boolean
  validated?: boolean
}

export interface ToolUiData {
  key: string
  name: string
  shortName: string
  command: string
  risk: string
  status: 'running' | 'done' | 'error'
  ok: boolean
  params: unknown
  result: unknown
  raw: string
  duration: number | null
  /** P7.4（ADR-034）：后端声明的头部摘要与渲染形态，chat 与刷新同源。 */
  summary?: string
  displayType?: string
  error?: string
  createResource?: CreateResourceState | null
}

const props = withDefaults(
  defineProps<{
    tool: ToolUiData
    /** 完成并成功后默认展开（对齐三期行为）。 */
    autoOpen?: boolean
    /** P7.4：回合报告完成后，未手动操作过的卡片自动折叠为摘要行。 */
    reportDone?: boolean
    /** P7.4：折叠/展开全部信号（数值变化触发）。 */
    collapseAll?: number
    expandAll?: number
  }>(),
  { autoOpen: true, reportDone: false, collapseAll: 0, expandAll: 0 },
)

// P7.4：手动折叠状态持久化（key=toolCallId），刷新后保持，手动优先于自动折叠。
const FOLD_KEY_PREFIX = 'k8s_ai_tool_fold:'
const storedFold = (() => {
  try {
    return localStorage.getItem(FOLD_KEY_PREFIX + props.tool.key)
  } catch {
    return null
  }
})()
const manual = ref(storedFold !== null)
const expanded = ref(storedFold !== null ? storedFold !== '1' : props.autoOpen)
const showRaw = ref(false)

// 工具结果到达（done/error）后自动展开，保持展开直到手动收起。
watch(
  () => props.tool.status,
  (status) => {
    if (status !== 'running' && !manual.value) {
      expanded.value = true
    }
  },
  { immediate: true },
)

// 报告完成后自动折叠"未手动操作过"的工具卡（P7.4 默认态）。
watch(
  () => props.reportDone,
  (done) => {
    if (done && !manual.value) {
      expanded.value = false
    }
  },
)

watch(
  () => props.collapseAll,
  (signal) => {
    if (signal && signal > 0) {
      manual.value = true
      expanded.value = false
    }
  },
)

watch(
  () => props.expandAll,
  (signal) => {
    if (signal && signal > 0) {
      manual.value = true
      expanded.value = true
    }
  },
)

const riskTag = computed<'danger' | 'warning' | 'info' | 'primary'>(() => {
  const risk = props.tool.risk?.toUpperCase() ?? ''
  if (/HIGH|高|CRITICAL/.test(risk)) return 'danger'
  if (/MEDIUM|中|RISK/.test(risk)) return 'warning'
  if (/LOW|低/.test(risk)) return 'info'
  return 'primary'
})

const riskLabel = computed(() => {
  const risk = props.tool.risk?.toUpperCase() ?? ''
  if (/HIGH|高|CRITICAL/.test(risk)) return '高风险'
  if (/MEDIUM|中|RISK/.test(risk)) return '中风险'
  if (/LOW|低/.test(risk)) return '低风险'
  return props.tool.risk || ''
})

const durationText = computed(() => {
  const value = props.tool.duration
  if (value === null || value === undefined) return ''
  if (value < 1000) return `${Math.round(value)}ms`
  return `${(value / 1000).toFixed(1)}s`
})

/** 尝试识别结果是否为结构化数据：K8s 列表 / 事件 / 单对象键值。 */
interface ResultView {
  type: 'table' | 'events' | 'kv' | 'yaml' | 'logs' | 'markdown' | 'text' | 'empty'
  parse: unknown
  kind?: string
  label?: string
}

function guessKind(record: Record<string, unknown>): string {
  if (typeof record.kind === 'string' && record.kind) {
    return record.kind
  }
  if (typeof record.type === 'string' && record.type) {
    return record.type
  }
  return ''
}

function looksLikeYaml(text: string): boolean {
  return (
    /^\s*(apiVersion:|kind:|metadata:|#[^\n]*\n\s*\w)/.test(text) || /^\s*-\s+name:/m.test(text)
  )
}

function looksLikeLogs(text: string): boolean {
  return (
    /^\s*\S+\s+\d{1,2}:\d{2}:\d{2}.*\n/m.test(text) || /\bpanic\b|\bfatal\b|\bERROR\b/i.test(text)
  )
}

function detectView(source: unknown): ResultView {
  if (source !== null && source !== undefined && typeof source === 'object') {
    if (Array.isArray(source)) {
      if (source.length === 0) {
        return { type: 'empty', parse: null };
      }
      const kind = guessKind((source[0] as Record<string, unknown>) ?? {}) || 'List';
      return { type: 'table', parse: source, kind };
    }
    const record = source as Record<string, unknown>;
    if (
      Array.isArray(record.events) &&
      record.events.length > 0 &&
      record.events.every((item) => item && typeof item === 'object')
    ) {
      return { type: 'events', parse: record.events, label: 'events' };
    }
    if (
      Array.isArray(record.conditions) &&
      record.conditions.length > 0 &&
      record.conditions.every((item) => item && typeof item === 'object')
    ) {
      return { type: 'events', parse: record.conditions, label: 'conditions' };
    }
    if (Array.isArray(record.items)) {
      if (record.items.length === 0) {
        return { type: 'empty', parse: null };
      }
      const kind = guessKind(record);
      const first = record.items[0];
      const itemKind =
        first && typeof first === 'object' ? guessKind(first as Record<string, unknown>) : kind;
      return { type: 'table', parse: record.items, kind: itemKind || kind || 'List' };
    }
    const scalarKeys = Object.keys(record).filter(
      (key) => record[key] === null || typeof record[key] !== 'object',
    );
    if (scalarKeys.length >= 2 && scalarKeys.length <= 12) {
      return { type: 'kv', parse: record, kind: guessKind(record) };
    }
    return { type: 'yaml', parse: JSON.stringify(record, null, 2) };
  }

  const raw = stripUntrusted(source);
  if (raw.trim() === '') {
    return { type: 'empty', parse: null };
  }
  const parsed = parseJsonLoose(raw);
  if (raw !== parsed && parsed && typeof parsed === 'object') {
    return detectView(parsed);
  }
  if (looksLikeYaml(raw)) {
    return { type: 'yaml', parse: raw };
  }
  if (looksLikeLogs(raw)) {
    return { type: 'logs', parse: raw };
  }
  return { type: 'text', parse: raw };
}

const view = computed<ResultView>(() => {
  const result = maskedResult.value
  if (result === undefined || result === null) {
    // 结果未到时语法层不猜完成态（对齐 §8）：由调用方仅在 completion/done 事件后传 result。
    return { type: 'empty', parse: null }
  }
  const guessed = detectView(result)
  const declared = props.tool.displayType
  // P7.4：后端声明渲染形态（ADR-034），前端优先采用，仍保留启发式兜底。
  if (declared && ['table', 'kv', 'events', 'yaml', 'logs', 'text'].includes(declared)) {
    return { ...guessed, type: declared as ResultView['type'] }
  }
  return guessed
})

/** 结果先做凭据兜底遮蔽，再做结构化识别（值永不回显）。 */
const maskedResult = computed<unknown>(() => maskSensitiveValues(props.tool.result))

const rawText = computed(() => {
  const text =
    props.tool.raw ||
    (props.tool.result !== undefined && props.tool.result !== null
      ? stripUntrusted(
          typeof props.tool.result === 'string'
            ? props.tool.result
            : JSON.stringify(props.tool.result, null, 2),
        )
      : '')
  if (!text) {
    return '（无返回数据）'
  }
  // JSON 文本走解析→遮蔽→回显，避免原始文本里带出凭据值。
  const parsed = parseJsonLoose(text)
  if (parsed !== text && parsed && typeof parsed === 'object') {
    return JSON.stringify(maskSensitiveValues(parsed), null, 2)
  }
  return text
})

function toggle(): void {
  manual.value = true
  expanded.value = !expanded.value
  try {
    localStorage.setItem(FOLD_KEY_PREFIX + props.tool.key, expanded.value ? '0' : '1')
  } catch {
    // 隐私模式/localStorage 不可用时仅影响折叠记忆，不影响交互。
  }
}
</script>

<template>
  <article class="toolcard" :class="tool.status">
    <button type="button" class="toolcard-head" @click="toggle">
      <span class="toolcard-dot" :class="tool.status"></span>
      <span v-if="tool.shortName && tool.shortName !== tool.name" class="toolcard-short">{{
        tool.shortName
      }}</span>
      <span class="toolcard-title">{{ tool.name }}</span>
      <!-- P7.4：头部概览优先展示后端摘要（如 "成功 · 12 pods · 320ms"），命令降级为 tooltip -->
      <span v-if="tool.summary" class="toolcard-cmd toolcard-summary" :title="tool.command || tool.summary">
        {{ tool.summary }}
      </span>
      <span v-else-if="tool.command" class="toolcard-cmd" :title="tool.command">{{ tool.command }}</span>
      <el-tag v-if="riskLabel" size="small" effect="plain" :type="riskTag">{{ riskLabel }}</el-tag>
      <span v-if="durationText" class="toolcard-duration">{{ durationText }}</span>
      <span class="toolcard-tools">
        <el-tooltip content="复制命令">
          <el-button
            size="small"
            text
            :icon="CopyDocument"
            @click.stop="copyText(tool.command || rawText, '命令已复制')"
          />
        </el-tooltip>
      </span>
      <el-icon class="toolcard-caret" :class="{ open: expanded }"><ArrowDown /></el-icon>
    </button>

    <!-- create_resource：提交的 manifest + dry-run→apply 两段式状态 -->
    <div
      v-if="expanded && tool.createResource && tool.createResource.manifest"
      class="toolcard-block"
    >
      <div class="toolcard-subhead">
        <span>提交的 Manifest</span>
        <span class="create-status">
          <el-tag v-if="tool.createResource.dryRun" size="small" type="warning" effect="plain"
            >dry-run</el-tag
          >
          <el-tag v-if="tool.createResource.validated" size="small" type="success" effect="plain"
            >validated</el-tag
          >
          <el-tag v-if="tool.createResource.applied" size="small" type="success" effect="plain"
            >applied</el-tag
          >
        </span>
      </div>
      <YamlBlock :text="tool.createResource.manifest" label="manifest" :max-height="280" />
    </div>

    <!-- 工具错误信息 -->
    <div v-if="expanded && tool.status === 'error'" class="toolcard-error">
      <el-icon><WarningFilled /></el-icon>
      <span class="toolcard-error-text">{{ tool.error || '工具执行失败' }}</span>
    </div>

    <!-- 结构化结果 -->
    <div v-if="expanded && tool.status !== 'error'" class="toolcard-result">
      <template v-if="tool.status === 'running'">
        <div class="toolcard-running">
          <span class="running-bar"></span>
          <span>执行中…</span>
        </div>
      </template>
      <template v-else>
        <StructuredTable
          v-if="view.type === 'table'"
          :items="view.parse as unknown[]"
          :kind="view.kind"
        />
        <StructuredEvents
          v-else-if="view.type === 'events'"
          :events="view.parse as unknown[]"
          :label="view.label"
        />
        <StructuredKv
          v-else-if="view.type === 'kv'"
          :data="view.parse as Record<string, unknown>"
        />
        <YamlBlock v-else-if="view.type === 'yaml'" :text="view.parse as string" />
        <LogBlock v-else-if="view.type === 'logs'" :text="view.parse as string" />
        <MarkdownView v-else-if="view.type === 'markdown'" :text="view.parse as string" />
        <pre v-else-if="view.type === 'text'" class="toolcard-raw">{{ view.parse as string }}</pre>
        <div v-else class="toolcard-empty">（等待结果返回…）</div>
      </template>
    </div>

    <!-- 查看原始 JSON（折叠层） -->
    <div v-if="expanded && view.type !== 'empty'" class="toolcard-rawtoggle">
      <button type="button" class="raw-link" @click="showRaw = !showRaw">
        {{ showRaw ? '收起原始数据' : '查看原始 JSON' }}
      </button>
      <pre v-if="showRaw" class="toolcard-rawpre">{{ rawText }}</pre>
    </div>
  </article>
</template>

<style scoped>
.toolcard {
  max-width: 820px;
  border: 1px solid var(--app-border);
  border-left: 3px solid var(--app-muted);
  border-radius: var(--app-radius);
}

.toolcard.done {
  border-left-color: var(--app-success);
}

.toolcard.error {
  border-left-color: var(--app-danger);
}

.toolcard.running {
  border-left-color: var(--app-accent);
}

.toolcard-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-family: inherit;
  font-size: 12.5px;
  cursor: pointer;
  text-align: left;
}

.toolcard-head:hover {
  background: var(--app-surface-2);
}

.toolcard-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
}

.toolcard-dot.running {
  background: var(--app-accent);
  box-shadow: 0 0 0 4px var(--app-accent-soft);
  animation: tc-blink 1.1s ease-in-out infinite;
}

.toolcard-dot.done {
  background: var(--app-success);
}

.toolcard-dot.error {
  background: var(--app-danger);
}

.toolcard-short {
  flex: none;
  padding: 0 8px;
  border-radius: 999px;
  background: var(--app-surface-2);
  color: var(--app-muted);
  font-family: var(--app-mono);
  font-size: 11px;
  font-weight: 600;
  line-height: 18px;
}

.toolcard-title {
  font-weight: 650;
  flex: none;
}

.toolcard-cmd {
  overflow: hidden;
  flex: 1;
  min-width: 0;
  max-width: 340px;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--app-muted);
  font-family: var(--app-mono);
  font-size: 11.5px;
}

.toolcard-summary {
  color: var(--app-text);
  font-family: inherit;
}

.toolcard-duration {
  flex: none;
  margin-left: auto;
  color: var(--app-faint);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.toolcard-tools {
  flex: none;
  display: inline-flex;
  align-items: center;
}

.toolcard-caret {
  flex: none;
  color: var(--app-muted);
  transition: transform 0.2s ease;
}

.toolcard-caret.open {
  transform: rotate(180deg);
}

.toolcard-block,
.toolcard-result {
  padding: 0 12px 10px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.toolcard-subhead {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin: 2px 0 6px;
  font-size: 12px;
  color: var(--app-muted);
}

.create-status {
  display: inline-flex;
  gap: 4px;
}

.toolcard-error {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin: 0 12px 10px;
  padding: 7px 10px;
  border-radius: 6px;
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 12px;
}

.toolcard-error-text {
  min-width: 0;
  word-break: break-word;
}

.toolcard-running {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 2px;
  color: var(--app-muted);
  font-size: 12px;
}

.running-bar {
  width: 14px;
  height: 2px;
  border-radius: 1px;
  background: var(--app-accent);
  animation: tc-run 1s ease-in-out infinite;
}

.toolcard-raw {
  margin: 0;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--app-surface-2);
  color: var(--app-text);
  font-family: var(--app-mono);
  font-size: 11.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}

.toolcard-empty {
  padding: 8px 2px;
  color: var(--app-muted);
  font-size: 12px;
}

.toolcard-rawtoggle {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 0 12px 10px;
}

.raw-link {
  align-self: flex-start;
  border: none;
  background: transparent;
  color: var(--app-accent);
  font-family: inherit;
  font-size: 11.5px;
  cursor: pointer;
  padding: 0;
}

.raw-link:hover {
  text-decoration: underline;
}

.toolcard-rawpre {
  margin: 0;
  padding: 8px 10px;
  max-height: 280px;
  overflow: auto;
  border-radius: 6px;
  background: var(--app-surface-2);
  color: var(--app-text);
  font-family: var(--app-mono);
  font-size: 11px;
  line-height: 1.5;
}

@keyframes tc-blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.3;
  }
}

@keyframes tc-run {
  0% {
    transform: translateX(0);
    opacity: 0.4;
  }
  50% {
    transform: translateX(4px);
    opacity: 1;
  }
  100% {
    transform: translateX(0);
    opacity: 0.4;
  }
}
</style>
