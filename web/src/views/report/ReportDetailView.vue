<script setup lang="ts">
/**
 * P9 报告详情页（需求 §8.2 / 方案 18 §11）。
 *
 * 三条口径写在文件头，是因为它们最容易被后续改版悄悄破坏：
 *  1. 前端不做诊断。根因、因果链、置信度、处置建议一律来自后端；页面自己「推断」一次
 *     根因，就等于凭空多出第二套诊断引擎，而它没有证据、也没有 RBAC。
 *  2. 根因与症状必须看得出区别。故障链里根因是一条带强调的区块，牵连异常是缩进列表，
 *     「哪个是要先去修的那个」不该靠读者自己数。
 *  3. 数据缺口不藏。采集失败、证据缺口、大模型降级原因、健康评分封顶原因都原文展示：
 *     一份写着「关键数据缺失」的报告，比一份假装健康的报告有用得多。
 *
 * 老报告（legacy / schema v1）缺整块区块，这里给诚实的空态而不是用零值凑图——
 * 凑出来的 0 会被读成「采集到 0 个异常」。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  CircleCheckFilled,
  CircleCloseFilled,
  Connection,
  CopyDocument,
  Download,
  Document,
  InfoFilled,
  Loading,
  Refresh,
  Warning,
  WarningFilled,
} from '@element-plus/icons-vue'

import { ApiRequestError } from '@/api/request'
import {
  describeReportError,
  downloadReport,
  exportReportMarkdown,
  getStructuredReport,
} from '@/api/reports'
import type {
  Conclusion,
  Diagnosis,
  DiagnosisCommand,
  Evidence,
  Finding,
  Incident,
  LLMDegradation,
  RemediationItem,
  ReportView,
  StructuredReportEnvelope,
} from '@/api/types'
import MarkdownView from '@/components/chat/MarkdownView.vue'
import TechEmptyState from '@/components/TechEmptyState.vue'

const route = useRoute()
const router = useRouter()

const reportId = computed(() => String(route.params.id ?? ''))

const envelope = ref<StructuredReportEnvelope | null>(null)
const loading = ref(false)
const loadError = ref('')
/** 后端回 415：这份报告只归档了 Markdown 正文，没有结构化形态。 */
const notStructured = ref(false)
/** 长证据（日志、describe 输出）默认折叠，按「报告项 id + 证据 id」记住展开态。 */
const openEvidence = ref<string[]>([])
const openIncidents = ref<string[]>([])
const openFindings = ref<string[]>([])
const rawOpen = ref(false)

const view = computed(() => envelope.value)
const report = computed<ReportView | null>(() => envelope.value?.report ?? null)
const insp = computed(() => envelope.value?.inspection ?? null)
/** 旧结构：缺结论 / 采集质量 / 诊断账 / 阶段账，页面按区块诚实降级。 */
const legacy = computed(() => envelope.value?.legacy === true)

// ------------------------------------------------------------ 索引与查找

const findingsById = computed(() => {
  const map = new Map<string, Finding>()
  for (const item of insp.value?.findings ?? []) {
    map.set(item.id, item)
  }
  return map
})

const diagnosisByIncident = computed(() => {
  const map = new Map<string, Diagnosis>()
  for (const item of insp.value?.diagnoses ?? []) {
    if (item.incidentId && !map.has(item.incidentId)) {
      map.set(item.incidentId, item)
    }
  }
  return map
})

/** 一次体检里「谁被判成了根因」：故障链之外的独立异常也要能一眼认出。 */
const operationalIncidents = computed(() =>
  (insp.value?.incidents ?? []).filter((incident) => !findingsById.value.get(incident.root.id)?.suggestion),
)
const advisoryIncidents = computed(() =>
  (insp.value?.incidents ?? []).filter((incident) => findingsById.value.get(incident.root.id)?.suggestion),
)
const incidentCount = computed(() => operationalIncidents.value.length)
const advisoryCount = computed(() => advisoryIncidents.value.length)
const findingCount = computed(() => insp.value?.findings?.length ?? 0)
const severityCounts = computed(() => {
  const counts: Record<string, number> = {}
  for (const item of insp.value?.findings ?? []) {
    counts[item.severity] = (counts[item.severity] ?? 0) + 1
  }
  return counts
})
const severityOrder = ['CRITICAL', 'HIGH', 'MEDIUM', 'WARNING', 'LOW', 'INFO']
const severityChips = computed(() =>
  severityOrder
    .filter((key) => (severityCounts.value[key] ?? 0) > 0)
    .map((key) => ({ key, count: severityCounts.value[key] ?? 0 })),
)

// ---------------------------------------------------------------- 展示口径

const CONCLUSION_META: Record<string, { label: string; tone: string }> = {
  healthy: { label: '健康', tone: 'ok' },
  attention: { label: '需要关注', tone: 'warn' },
  critical: { label: '存在严重风险', tone: 'err' },
  unknown: { label: '结论不确定', tone: 'mute' },
}

const SEVERITY_META: Record<string, { label: string; tone: string }> = {
  CRITICAL: { label: 'CRITICAL 严重', tone: 'err' },
  HIGH: { label: 'HIGH 高风险', tone: 'err' },
  MEDIUM: { label: 'MEDIUM 中等', tone: 'warn' },
  WARNING: { label: 'WARNING 告警', tone: 'warn' },
  LOW: { label: 'LOW 低危', tone: 'info' },
  INFO: { label: 'INFO 提示', tone: 'mute' },
}

const ROOT_STATUS_META: Record<string, { label: string; tone: string }> = {
  confirmed: { label: '根因已确认', tone: 'ok' },
  probable: { label: '疑似根因', tone: 'warn' },
  unknown: { label: '未能定位根因', tone: 'err' },
}

const INTEGRITY_META: Record<string, { label: string; tone: string }> = {
  complete: { label: '采集完整', tone: 'ok' },
  partial: { label: '部分资源采集失败', tone: 'warn' },
  insufficient: { label: '关键数据缺失', tone: 'err' },
}

const STAGE_META: Record<string, { label: string; tone: string }> = {
  running: { label: '执行中', tone: 'info' },
  done: { label: '完成', tone: 'ok' },
  failed: { label: '失败', tone: 'err' },
  skipped: { label: '未执行', tone: 'mute' },
}

const METHOD_LABELS: Record<string, string> = {
  rule: '规则判定',
  deterministic: '确定性引擎',
  llm: '大模型增强',
  hybrid: '确定性引擎 + 大模型',
  none: '未定因',
}

const DIAG_MODE_LABELS: Record<string, string> = {
  rule_only: '仅规则与确定性引擎，本轮未调用大模型',
  llm_enhanced: '确定性引擎 + 大模型增强',
  llm_degraded: '大模型降级，结论以确定性引擎为准',
}

const LLM_STATUS_LABELS: Record<string, string> = {
  not_configured: '未配置模型',
  success: '成功',
  request_failed: '请求失败',
  timeout: '请求超时',
  invalid_response: '响应无法解析',
  schema_validation_failed: '结构校验不通过',
  evidence_validation_failed: '证据校验不通过，已退回确定性结论',
  partial_success: '部分成功',
  degraded: '降级',
  budget_skipped: '诊断预算不足，未送诊',
}

const COMMAND_KIND_LABELS: Record<string, string> = {
  investigation: '排查',
  remediation: '修复',
  verification: '验证',
}

const RISK_META: Record<string, { label: string; tone: string }> = {
  SAFE: { label: '只读', tone: 'mute' },
  LOW: { label: '低风险', tone: 'info' },
  MEDIUM: { label: '中风险', tone: 'warn' },
  HIGH: { label: '高风险', tone: 'err' },
  CRITICAL: { label: '高危', tone: 'err' },
}

const FINDING_ROLE_LABELS: Record<string, string> = {
  anchor: '主信号',
  symptom: '关联症状',
  impact: '业务影响',
  advisory: '治理建议',
  independent: '独立异常',
}

const SUMMARY_LABELS: Record<string, string> = {
  namespaces: '命名空间',
  nodes: '节点',
  pods: 'Pod',
  services: 'Service',
  endpointSlices: 'EndpointSlice',
  workloads: '工作负载',
  storage: '存储对象',
  ingresses: 'Ingress',
  networkPolicies: 'NetworkPolicy',
  roles: 'Role',
  roleBindings: 'RoleBinding',
  clusterRoles: 'ClusterRole',
  clusterRoleBindings: 'ClusterRoleBinding',
  components: '关键组件',
  events: '事件',
}

const SCOPE_LABELS: Record<string, string> = {
  cluster: '集群体检',
  namespace: '业务体检 · 命名空间',
  pod: '业务体检 · Pod',
}

function metaOf(
  table: Record<string, { label: string; tone: string }>,
  key: string | undefined,
  fallbackLabel: string,
  fallbackTone = 'mute',
): { label: string; tone: string } {
  if (key && table[key]) {
    return table[key] as { label: string; tone: string }
  }
  return { label: fallbackLabel, tone: fallbackTone }
}

function severityText(severity: string | undefined): string {
  return metaOf(SEVERITY_META, severity, severity || '未知等级').label
}

function severityTone(severity: string | undefined): string {
  return metaOf(SEVERITY_META, severity, '未知等级').tone
}

/** 等级用「图标 + 文字」双通道表达：只靠颜色的话，色觉差异的人分不出 HIGH 与 MEDIUM。 */
function severityIcon(severity: string | undefined) {
  switch (severityTone(severity)) {
    case 'err':
      return CircleCloseFilled
    case 'warn':
      return WarningFilled
    case 'info':
      return Warning
    default:
      return InfoFilled
  }
}

function riskMeta(risk: string | undefined): { label: string; tone: string } {
  return metaOf(RISK_META, risk, risk || '未标注风险')
}

function findingRoleLabel(role: string | undefined): string {
  return role ? (FINDING_ROLE_LABELS[role] ?? role) : ''
}

const conclusion = computed<Conclusion | null>(() => insp.value?.conclusion ?? null)
const conclusionMeta = computed(() =>
  metaOf(CONCLUSION_META, conclusion.value?.level, '结论未记录'),
)
const primaryIncident = computed(() => operationalIncidents.value[0])
const primaryDiagnosis = computed(() =>
  primaryIncident.value ? diagnosisByIncident.value.get(primaryIncident.value.id) : undefined,
)
const primaryRootCause = computed(() =>
  primaryIncident.value ? incidentRootCause(primaryIncident.value) : '',
)
const primaryBusinessImpact = computed(() => {
  const incident = primaryIncident.value
  if (!incident) {
    return ''
  }
  const impact = incidentImpactLabels(incident)
  return impact.length ? impact.join('；') : incident.title
})
const conclusionHeadline = computed(() => {
  if (primaryIncident.value) {
    return `${resourceText(primaryIncident.value.root.resource)}：${primaryBusinessImpact.value}`
  }
  return conclusion.value?.headline || '这份报告没有总体结论区块（四期之前的报告结构）'
})
const conclusionSummary = computed(() => {
  if (!insp.value || !primaryIncident.value) {
    return conclusion.value?.summary ?? ''
  }
  const reportScore = insp.value.healthScore
  const parts = [
    `健康评分 ${reportScore?.score ?? '—'}/${reportScore?.max ?? 100}`,
    `业务故障链 ${incidentCount.value} 条`,
    `治理建议 ${advisoryCount.value} 项`,
  ]
  if (insp.value.collection?.integrity) {
    parts.push(`采集完整性 ${insp.value.collection.integrity}`)
  }
  return `${parts.join('；')}。`
})
const topRisks = computed(() => conclusion.value?.topRisks ?? [])
const priorities = computed(() => conclusion.value?.priorities ?? [])
const dataCaveats = computed(() => conclusion.value?.dataCaveats ?? [])

const health = computed(() => insp.value?.healthScore ?? null)
const scoreText = computed(() => {
  if (!health.value) {
    return '—'
  }
  return String(health.value.score)
})
const scoreTone = computed(() => {
  const score = health.value?.score
  const max = health.value?.max || 100
  if (score === undefined) {
    return 'mute'
  }
  if (score >= max * 0.9) {
    return 'ok'
  }
  if (score >= max * 0.7) {
    return 'warn'
  }
  return 'err'
})

const collection = computed(() => insp.value?.collection ?? null)
const collectionMeta = computed(() =>
  metaOf(INTEGRITY_META, collection.value?.integrity, '采集质量未记录'),
)
const coverageText = computed(() =>
  collection.value ? `${Math.round(collection.value.coverage * 100)}%` : '—',
)

const diagSummary = computed(() => insp.value?.diagnosisSummary ?? null)
const llmSummary = computed(() => insp.value?.llmSummary ?? null)
const degradations = computed<LLMDegradation[]>(
  () => diagSummary.value?.degradations ?? llmSummary.value?.degradations ?? [],
)
const llmModeText = computed(() => {
  const mode = diagSummary.value?.mode ?? llmSummary.value?.mode ?? ''
  return DIAG_MODE_LABELS[mode] ?? '大模型诊断状态未记录'
})

const stages = computed(() => insp.value?.stages ?? [])
const statistics = computed(() => insp.value?.statistics ?? [])
const components = computed(() => insp.value?.components ?? [])
const remediation = computed<RemediationItem[]>(() => insp.value?.remediation ?? [])
const history = computed(() => insp.value?.history ?? null)
const collectionErrors = computed(() => insp.value?.collectionErrors ?? [])

const pageTitle = computed(() => {
  const scan = insp.value?.meta?.scanEndedAt || report.value?.createdAt || ''
  const kind = report.value?.kind === 'latest-json' ? 'JSON 体检报告' : 'Markdown 体检报告'
  return scan ? `${kind} · ${formatTime(scan)}` : kind
})

const scopeLabel = computed(() => {
  const kind = report.value?.scopeKind ?? ''
  if (!kind) {
    return '体检范围未记录'
  }
  const label = SCOPE_LABELS[kind] ?? kind
  const value = kind === 'cluster' ? '' : report.value?.scopeValue ?? ''
  return value ? `${label} · ${value}` : label
})
const scopeTone = computed(() => (report.value?.scopeKind === 'cluster' ? 'info' : 'mute'))

/** 归档的是 JSON 正文时，"下载原文"下下来的是数据而不是可读报告，导出 Markdown 才有意义。 */
const isJsonArtifact = computed(() => report.value?.kind === 'latest-json')

const clusterLabel = computed(() => {
  const fromScan = insp.value?.meta?.cluster ?? ''
  if (fromScan) {
    return fromScan
  }
  // 页面体检不绑定已登记集群：没有集群身份就留空，编一个「平台集群」只会让人以为真有这么个集群。
  return ''
})

const summaryChips = computed(() => {
  const summary = insp.value?.summary ?? {}
  return Object.entries(SUMMARY_LABELS)
    .filter(([key]) => summary[key] !== undefined)
    .map(([key, label]) => ({ label, value: summary[key] ?? 0 }))
})

const jsonText = computed(() => {
  if (!insp.value) {
    return ''
  }
  return JSON.stringify(insp.value, null, 2)
})
/** 原始 JSON 只用于目视核对，页面里截断展示，完整内容走下载。 */
const jsonPreview = computed(() => {
  const limit = 200_000
  if (jsonText.value.length <= limit) {
    return jsonText.value
  }
  return `${jsonText.value.slice(0, limit)}\n… 其余内容请使用「下载 JSON」查看`
})

// ------------------------------------------------------------------ 取数

async function load(): Promise<void> {
  const id = reportId.value
  if (!id) {
    loadError.value = '缺少报告 id'
    return
  }
  loading.value = true
  notStructured.value = false
  try {
    const data = await getStructuredReport(id)
    envelope.value = data
    loadError.value = ''
    // 第一条故障链默认展开：进来第一眼就该看到「最要紧的那个原因」。
    const first = operationalIncidents.value[0]
    if (first && openIncidents.value.length === 0) {
      openIncidents.value = [first.id]
    }
  } catch (error) {
    if (error instanceof ApiRequestError && error.status === 415) {
      notStructured.value = true
      loadError.value = ''
      return
    }
    loadError.value = describeReportError('读取报告详情', error)
    if (envelope.value) {
      ElMessage.error(loadError.value)
    }
  } finally {
    loading.value = false
  }
}

function backToList(): void {
  void router.push({ name: 'report' })
}

async function handleDownloadOriginal(): Promise<void> {
  const id = reportId.value
  if (!id) {
    return
  }
  try {
    const filename = await downloadReport(id)
    ElMessage.success(`已下载 ${filename}`)
  } catch (error) {
    ElMessage.error(describeReportError('下载报告', error))
  }
}

/**
 * 按需导出一份 Markdown（P9.13）。
 *
 * 一次体检只归档发起时选中的那一种格式，另一种不落对象存储；这里要的是「我正在看这份
 * 结论，顺手给我一份能贴进工单的正文」，由后端用同一份数据当场渲染，不产生第二份存档。
 */
async function handleExportMarkdown(): Promise<void> {
  const id = reportId.value
  if (!id) {
    return
  }
  try {
    const filename = await exportReportMarkdown(id)
    ElMessage.success(`已导出 ${filename}`)
  } catch (error) {
    ElMessage.error(describeReportError('导出 Markdown', error))
  }
}

/**
 * 下载页面上正在看的这份结构化 JSON。
 *
 * 数据已经在内存里，直接用 Blob 落盘，不再回服务端取一次：这样「下载的」和
 * 「屏幕上看到的」必定同一份，不会因为两次读取之间对象存储换了后端而对不上。
 */
function downloadJson(): void {
  if (!insp.value) {
    ElMessage.warning('没有可下载的结构化内容')
    return
  }
  const blob = new Blob([jsonText.value], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `k8s-ai-report-${reportId.value}.json`
  anchor.rel = 'noopener'
  anchor.style.display = 'none'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 60_000)
}

async function copyText(text: string, label = '内容'): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(`${label}已复制`)
  } catch {
    ElMessage.error('浏览器拒绝了剪贴板写入，请手动选择文本复制')
  }
}

// -------------------------------------------------------------- 展开控制

function toggleId(list: string[], id: string): string[] {
  return list.includes(id) ? list.filter((item) => item !== id) : [...list, id]
}

function toggleIncident(id: string): void {
  openIncidents.value = toggleId(openIncidents.value, id)
}

function toggleFinding(id: string): void {
  openFindings.value = toggleId(openFindings.value, id)
}

function expandAllIncidents(): void {
  const all = operationalIncidents.value.map((item) => item.id)
  openIncidents.value = openIncidents.value.length === all.length ? [] : all
}

function evidenceKey(scope: string, id: string): string {
  return `${scope}#${id}`
}

function isEvidenceOpen(scope: string, id: string): boolean {
  return openEvidence.value.includes(evidenceKey(scope, id))
}

function toggleEvidence(scope: string, id: string): void {
  const key = evidenceKey(scope, id)
  openEvidence.value = openEvidence.value.includes(key)
    ? openEvidence.value.filter((item) => item !== key)
    : [...openEvidence.value, key]
}

/** 日志与 describe 输出默认折叠；短字段（exit code、restartCount）不值得一个开关。 */
function isLongEvidence(value: string): boolean {
  return value.length > 200 || value.split('\n').length > 3
}

/**
 * 故障链默认只展示诊断实际引用的关键证据，不再把所有成员的全部事件和日志铺满首屏。
 * 未给证据链时回退到根 Finding 的前几条证据；完整证据仍在“完整异常清单”里。
 */
function incidentEvidence(incident: Incident): Array<{ ref: string; evidence: Evidence; owner: string }> {
  const out: Array<{ ref: string; evidence: Evidence; owner: string }> = []
  const seen = new Set<string>()
  const rootFinding = findingsById.value.get(incident.root.id)
  const memberFindings = (incident.members ?? [])
    .map((member) => findingsById.value.get(member.id))
    .filter((finding): finding is Finding => finding !== undefined)
  const refs = diagnosisByIncident.value.get(incident.id)?.evidenceChain ?? []

  const add = (finding: Finding | undefined, evidenceID: string) => {
    const evidence = finding?.evidence?.find((item) => item.id === evidenceID)
    if (!finding || !evidence) {
      return
    }
    const ref = `${finding.id}#${evidence.id}`
    if (!seen.has(ref)) {
      seen.add(ref)
      out.push({ ref, evidence, owner: finding.id })
    }
  }

  for (const rawRef of refs) {
    const [owner, evidenceID] = rawRef.includes('#')
      ? rawRef.split('#', 2)
      : ['', rawRef]
    if (owner) {
      add(findingsById.value.get(owner), evidenceID)
      continue
    }
    const finding =
      rootFinding?.evidence?.some((evidence) => evidence.id === evidenceID)
        ? rootFinding
        : memberFindings.find((candidate) =>
            candidate.evidence?.some((evidence) => evidence.id === evidenceID),
          )
    add(finding, evidenceID)
  }

  if (!out.length) {
    for (const evidence of (rootFinding?.evidence ?? []).slice(0, 6)) {
      add(rootFinding, evidence.id)
    }
  }
  return out
}

function incidentDiagnosis(incident: Incident): Diagnosis | undefined {
  return diagnosisByIncident.value.get(incident.id)
}

function incidentMethod(incident: Incident): string {
  return incidentDiagnosis(incident)?.source || incident.rootCauseMethod || ''
}

function incidentRootCause(incident: Incident): string {
  return incidentDiagnosis(incident)?.rootCause || incident.rootCause || '未定位到根因'
}

function incidentImpactLabels(incident: Incident): string[] {
  return (incident.members ?? [])
    .filter((member) => ['DeploymentReplica', 'StatefulSetReplica', 'ServiceNoEndpoint'].includes(member.rule))
    .map((member) => member.title)
}

const INCIDENT_EVIDENCE_SCOPE = 'incident'

// ------------------------------------------------------------------ 格式化

function formatTime(value: string | undefined): string {
  if (!value) {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString()
}

function resourceText(resource: { kind: string; namespace?: string; name: string } | undefined): string {
  if (!resource) {
    return '—'
  }
  const scope = resource.namespace ? `${resource.namespace}/` : ''
  return `${resource.kind} ${scope}${resource.name}`
}

function durationText(seconds: number): string {
  if (seconds < 60) {
    return `${seconds.toFixed(seconds < 10 ? 1 : 0)}s`
  }
  return `${Math.floor(seconds / 60)}m${Math.round(seconds % 60)}s`
}

function commandsOf(diagnosis: Diagnosis | undefined, kind: string): DiagnosisCommand[] {
  if (!diagnosis) {
    return []
  }
  if (kind === 'investigation') {
    return diagnosis.investigation ?? []
  }
  if (kind === 'remediation') {
    return diagnosis.remediation ?? []
  }
  return diagnosis.verification ?? []
}

function commandRiskMeta(command: DiagnosisCommand, kind: string): { label: string; tone: string } {
  const risk = command.risk || (kind === 'investigation' || kind === 'verification' ? 'SAFE' : '')
  return riskMeta(risk)
}

function remediationOfKind(kind: string): RemediationItem[] {
  return remediation.value.filter((item) => item.kind === kind)
}

function isRemediationCommand(item: RemediationItem): boolean {
  if (item.format === 'command') {
    return true
  }
  if (item.format === 'text') {
    return false
  }
  return /^(kubectl|helm|crictl|journalctl|systemctl|curl|openssl|df|ls)\b/.test(item.text.trim())
}

onMounted(load)
watch(reportId, () => {
  envelope.value = null
  openIncidents.value = []
  openFindings.value = []
  openEvidence.value = []
  rawOpen.value = false
  void load()
})

// 让模板里的常量表可访问（同时便于单测断言文案口径）。
defineExpose({
  CONCLUSION_META,
  COMMAND_KIND_LABELS,
  LLM_STATUS_LABELS,
  durationText,
  incidentEvidence,
})
</script>

<template>
  <section class="detail-page">
    <div class="page-head">
      <el-button text :icon="ArrowLeft" @click="backToList">报告列表</el-button>
      <h2 class="title ellipsis">{{ pageTitle }}</h2>
      <span class="pill" :class="scopeTone">{{ scopeLabel }}</span>
      <span v-if="clusterLabel" class="pill mute ellipsis" :title="clusterLabel">{{ clusterLabel }}</span>
      <span
        v-if="legacy"
        class="pill warn"
        title="四期之前归档的报告结构，缺少总体结论、采集质量与诊断账区块"
      >
        旧版报告结构
      </span>
      <div class="head-actions">
        <el-button :icon="Refresh" :loading="loading" @click="load">重新读取</el-button>
        <el-button v-if="isJsonArtifact" :icon="Document" @click="handleExportMarkdown">
          导出 Markdown
        </el-button>
        <el-button type="primary" :icon="Download" @click="handleDownloadOriginal">下载原文</el-button>
      </div>
    </div>

    <div v-if="loading && !view" class="card hint-card">
      <el-icon class="is-loading"><Loading /></el-icon>
      <span class="faint">正在从对象存储读取报告正文…</span>
    </div>

    <TechEmptyState
      v-else-if="notStructured"
      title="这份报告只归档了 Markdown 正文"
      hint="发起体检时选择 Markdown 格式，就只归档这份可读正文，页面因此没有故障链与证据区块。需要页面上展开结论与证据，请在开始体检时选择 JSON 格式。"
    >
      <el-button type="primary" :icon="Download" @click="handleDownloadOriginal">下载原文</el-button>
    </TechEmptyState>

    <TechEmptyState v-else-if="!view" title="报告内容读不出来" :hint="loadError || '报告正文可能已被回收，或所在存储后端暂不可用。'">
      <el-button :icon="Refresh" @click="load">重试</el-button>
      <el-button @click="backToList">返回报告列表</el-button>
    </TechEmptyState>

    <template v-else-if="view">
      <p v-if="loadError" class="alert err">{{ loadError }}</p>

      <!-- 1 总体结论与健康评分 -->
      <div class="card conclusion-card" :class="conclusionMeta.tone">
        <div class="conclusion-main">
          <div class="conclusion-top">
            <span class="pill" :class="conclusionMeta.tone">{{ conclusionMeta.label }}</span>
            <span class="faint">总体结论由程序从规则与诊断结果推导，未经大模型改写</span>
          </div>
          <h3 class="headline">{{ conclusionHeadline }}</h3>
          <div v-if="primaryIncident && primaryRootCause" class="primary-cause">
            <span class="sub-title">直接根因</span>
            <MarkdownView :text="primaryRootCause" />
            <p v-if="primaryDiagnosis?.uncertainty" class="primary-uncertainty faint">
              未确认边界：{{ primaryDiagnosis.uncertainty }}
            </p>
          </div>
          <p v-if="conclusionSummary" class="summary">{{ conclusionSummary }}</p>
          <div v-if="topRisks.length" class="list-block">
            <span class="sub-title">主要风险</span>
            <ul class="dot-list">
              <li v-for="(item, index) in topRisks" :key="`risk-${index}`">{{ item }}</li>
            </ul>
          </div>
          <div v-if="priorities.length" class="list-block">
            <span class="sub-title">建议优先处理</span>
            <ol class="num-list">
              <li v-for="(item, index) in priorities" :key="`prio-${index}`">{{ item }}</li>
            </ol>
          </div>
          <div v-if="dataCaveats.length" class="list-block">
            <span class="sub-title caveat-title">影响结论可信度的数据缺口</span>
            <ul class="dot-list warn">
              <li v-for="(item, index) in dataCaveats" :key="`caveat-${index}`">{{ item }}</li>
            </ul>
          </div>
        </div>
        <div class="score-box">
          <div class="score-v mono" :class="scoreTone">{{ scoreText }}</div>
          <div class="score-l faint">健康评分 / {{ health?.max ?? 100 }}</div>
          <span v-if="health?.capped" class="pill warn" :title="health.capReason">
            受数据完整性封顶
          </span>
          <p v-if="health?.capReason" class="cap-reason faint">{{ health.capReason }}</p>
          <span class="pill" :class="collectionMeta.tone">{{ collectionMeta.label }}</span>
        </div>
      </div>

      <!-- 2 概览：规模、等级分布、诊断账 -->
      <div class="stat-grid">
        <div class="card stat">
          <div class="l">业务故障链</div>
          <div class="v mono">{{ incidentCount }}</div>
          <span class="u">{{ findingCount }} 条完整异常 · {{ advisoryCount }} 项治理建议</span>
        </div>
        <div class="card stat">
          <div class="l">确定性诊断</div>
          <div class="v mono">{{ diagSummary?.deterministic ?? '—' }}</div>
          <span class="u">
            确认 {{ diagSummary?.confirmed ?? 0 }} · 疑似 {{ diagSummary?.probable ?? 0 }} · 未定位
            {{ diagSummary?.unknown ?? 0 }}
          </span>
        </div>
        <div class="card stat">
          <div class="l">大模型诊断</div>
          <div class="v mono">{{ diagSummary?.llmSucceeded ?? llmSummary?.succeeded ?? 0 }}</div>
          <span class="u">
            调用 {{ diagSummary?.llmCalls ?? llmSummary?.calls ?? 0 }} · 失败
            {{ diagSummary?.llmFailed ?? llmSummary?.failed ?? 0 }} · 退回
            {{ diagSummary?.llmRejected ?? 0 }}
          </span>
        </div>
        <div class="card stat">
          <div class="l">采集覆盖率</div>
          <div class="v mono">{{ coverageText }}</div>
          <span class="u">
            {{ collection ? `${collection.collected}/${collection.planned} 项计划采集` : '采集质量未记录' }}
          </span>
        </div>
      </div>

      <div v-if="severityChips.length || summaryChips.length" class="chip-row">
        <span
          v-for="chip in severityChips"
          :key="chip.key"
          class="pill"
          :class="severityTone(chip.key)"
        >
          <el-icon><component :is="severityIcon(chip.key)" /></el-icon>
          {{ severityText(chip.key) }} {{ chip.count }}
        </span>
        <span v-for="chip in summaryChips" :key="chip.label" class="pill mute mono">
          {{ chip.label }} {{ chip.value }}
        </span>
        <span class="pill mute">{{ llmModeText }}</span>
      </div>

      <!-- 3 业务故障链：只放真实业务异常；治理建议单独成区 -->
      <section class="block">
        <div class="block-head">
          <h3 class="block-title">业务故障链 <span class="mono faint">{{ incidentCount }}</span></h3>
          <span class="block-hint faint">优先回答“业务为什么不可用”，根因结论来自后端诊断</span>
          <el-button
            v-if="incidentCount > 1"
            class="block-action"
            size="small"
            link
            @click="expandAllIncidents"
          >
            {{ openIncidents.length === incidentCount ? '全部收起' : '全部展开' }}
          </el-button>
        </div>

        <div v-if="incidentCount" class="card list-card fault-chain-list">
          <article
            v-for="incident in operationalIncidents"
            :key="incident.id"
            class="item"
            :class="{ open: openIncidents.includes(incident.id) }"
          >
            <div class="item-row">
              <button class="expander" type="button" @click="toggleIncident(incident.id)">
                <el-icon><component :is="openIncidents.includes(incident.id) ? ArrowDown : ArrowRight" /></el-icon>
              </button>
              <span class="pill" :class="severityTone(incident.severity)">
                <el-icon><component :is="severityIcon(incident.severity)" /></el-icon>
                {{ severityText(incident.severity) }}
              </span>
              <span class="t ellipsis strong">{{ incident.title }}</span>
              <span class="mono faint ellipsis" :title="resourceText(incident.root.resource)">
                {{ resourceText(incident.root.resource) }}
              </span>
              <span
                v-for="impact in incidentImpactLabels(incident)"
                :key="impact"
                class="pill err impact-pill"
                :title="impact"
              >
                {{ impact }}
              </span>
              <span
                v-if="incident.independent"
                class="pill mute"
                title="该异常未与任何其他资源形成故障链，单独存在"
              >
                独立异常
              </span>
              <span v-else class="pill mute">{{ (incident.members ?? []).length }} 个关联异常</span>
            </div>

            <template v-if="openIncidents.includes(incident.id)">
              <!-- 根因与被它牵连的异常在视觉上必须分开：一个用强调块，一个是缩进清单 -->
              <div class="root-band" :class="metaOf(ROOT_STATUS_META, incident.rootCauseStatus, '根因未记录').tone">
                <div class="root-head">
                  <span class="root-tag">根因</span>
                  <span class="pill" :class="metaOf(ROOT_STATUS_META, incident.rootCauseStatus, '根因未记录').tone">
                    {{ metaOf(ROOT_STATUS_META, incident.rootCauseStatus, '根因未记录').label }}
                  </span>
                  <span v-if="incidentMethod(incident)" class="pill info">
                    {{ METHOD_LABELS[incidentMethod(incident)] ?? incidentMethod(incident) }}
                  </span>
                  <span v-if="incident.rootCauseRule" class="pill mute mono" title="命中的诊断器 ID">
                    {{ incident.rootCauseRule }}
                  </span>
                  <span
                    v-if="incident.confidence !== undefined && incident.rootCauseStatus !== 'unknown'"
                    class="pill mute mono"
                    :title="incident.confidenceBasis || '置信度依据未记录'"
                  >
                    置信 {{ Math.round(incident.confidence * 100) }}%
                  </span>
                </div>
                <div class="root-text">
                  <MarkdownView :text="incidentRootCause(incident)" />
                </div>
                <p v-if="incident.confidenceBasis" class="root-basis faint">
                  可信度依据：{{ incident.confidenceBasis }}
                </p>
                <p v-if="incidentDiagnosis(incident)?.summary" class="root-summary">
                  {{ incidentDiagnosis(incident)?.summary }}
                </p>
                <p v-if="incidentDiagnosis(incident)?.uncertainty" class="root-basis faint">
                  未确认边界：{{ incidentDiagnosis(incident)?.uncertainty }}
                </p>
              </div>

              <details v-if="incident.causalEdges?.length" class="sub-block causal-panel">
                <summary>展开传播路径（{{ incident.causalEdges.length }} 条因果边）</summary>
                <ul class="edge-list">
                  <li v-for="(edge, index) in incident.causalEdges" :key="`edge-${index}`">
                    <span class="mono">{{ resourceText(edge.sourceResource) }}</span>
                    <span class="edge-arrow">
                      <el-icon><ArrowRight /></el-icon>
                      {{ edge.relationType || 'related' }}
                    </span>
                    <span class="mono">{{ resourceText(edge.targetResource) }}</span>
                    <span class="pill" :class="edge.confidence === 'confirmed' ? 'ok' : 'warn'">
                      {{ edge.confidence === 'confirmed' ? '已确认' : '疑似' }}
                    </span>
                    <p class="edge-mechanism faint">{{ edge.mechanism }}</p>
                  </li>
                </ul>
              </details>

              <div v-if="incident.impactChain?.length" class="sub-block">
                <span class="sub-title">影响链</span>
                <ol class="chain-list">
                  <li v-for="(step, index) in incident.impactChain" :key="`chain-${index}`">{{ step }}</li>
                </ol>
              </div>

              <details v-if="(incident.members ?? []).length > 0" class="sub-block member-panel">
                <summary>展开被牵连的异常（{{ incident.members.length }} 个）</summary>
                <ul class="member-list">
                  <li v-for="member in incident.members" :key="member.id">
                    <span class="pill" :class="severityTone(member.severity)">
                      <el-icon><component :is="severityIcon(member.severity)" /></el-icon>
                      {{ severityText(member.severity) }}
                    </span>
                    <span class="mono faint">{{ member.id }}</span>
                    <span class="ellipsis">{{ member.title }}</span>
                    <span class="mono faint ellipsis">{{ resourceText(member.resource) }}</span>
                  </li>
                </ul>
              </details>

              <details v-if="incidentEvidence(incident).length" class="sub-block evidence-panel">
                <summary>关键证据（{{ incidentEvidence(incident).length }} 条，点击展开）</summary>
                <div class="evidence-list">
                  <div
                    v-for="entry in incidentEvidence(incident)"
                    :key="entry.ref"
                    class="evidence"
                  >
                    <div class="evidence-head">
                      <span class="ev-id mono" :title="`诊断结论引用号 ${entry.ref}`">{{ entry.ref }}</span>
                      <span class="pill mute">{{ entry.evidence.kind }}</span>
                      <span class="mono faint ellipsis">{{ entry.evidence.key }}</span>
                      <span v-if="entry.evidence.redacted" class="pill warn">含脱敏字段</span>
                      <button class="copy-btn" type="button" title="复制该条证据" @click="copyText(entry.evidence.value, '证据')">
                        <el-icon><CopyDocument /></el-icon>
                      </button>
                    </div>
                    <pre
                      class="ev-value mono"
                      :class="{ collapsed: isLongEvidence(entry.evidence.value) && !isEvidenceOpen(INCIDENT_EVIDENCE_SCOPE + incident.id, entry.evidence.id) }"
                      @click="isLongEvidence(entry.evidence.value) ? toggleEvidence(INCIDENT_EVIDENCE_SCOPE + incident.id, entry.evidence.id) : undefined"
                    >{{ entry.evidence.value }}</pre>
                    <button
                      v-if="isLongEvidence(entry.evidence.value)"
                      class="fold-btn faint"
                      type="button"
                      @click="toggleEvidence(INCIDENT_EVIDENCE_SCOPE + incident.id, entry.evidence.id)"
                    >
                      {{
                        isEvidenceOpen(INCIDENT_EVIDENCE_SCOPE + incident.id, entry.evidence.id)
                          ? '收起'
                          : `展开${entry.evidence.truncated ? '（后端已截断，完整内容见原文）' : ''}`
                      }}
                    </button>
                  </div>
                </div>
              </details>

              <details
                v-if="incident.evidenceGaps?.length || incident.alternativeCauses?.length"
                class="sub-block diagnosis-notes"
              >
                <summary>根因判断边界与待排除项</summary>
                <div v-if="incident.evidenceGaps?.length" class="sub-block">
                  <span class="sub-title warn-title">证据缺口</span>
                  <ul class="dot-list warn">
                    <li v-for="(gap, index) in incident.evidenceGaps" :key="`gap-${index}`">{{ gap }}</li>
                  </ul>
                </div>
                <div v-if="incident.alternativeCauses?.length" class="sub-block">
                  <span class="sub-title">其他可能原因</span>
                  <ul class="dot-list">
                    <li v-for="(item, index) in incident.alternativeCauses" :key="`alt-${index}`">{{ item }}</li>
                  </ul>
                </div>
              </details>

              <div v-if="incidentDiagnosis(incident)" class="action-panel">
                <div class="action-head">
                  <span class="action-title">处置方案</span>
                  <span
                    v-if="incidentDiagnosis(incident)?.validation?.commandIssues?.length"
                    class="pill warn"
                  >
                    部分命令未收录
                  </span>
                  <span
                    v-else-if="incidentDiagnosis(incident)?.validation?.passed"
                    class="pill ok"
                    title="诊断引用的证据与资源均存在"
                  >
                    诊断校验通过
                  </span>
                  <span
                    v-else-if="incidentDiagnosis(incident)?.validation"
                    class="pill warn"
                  >
                    诊断已降级
                  </span>
                </div>

                <div v-if="incidentDiagnosis(incident)?.remediationText" class="action-section">
                  <span class="sub-title">修复说明</span>
                  <MarkdownView :text="incidentDiagnosis(incident)?.remediationText || ''" />
                </div>

                <div v-if="incidentDiagnosis(incident)?.remediationDirection" class="action-section">
                  <span class="sub-title">执行前置条件与边界</span>
                  <MarkdownView :text="incidentDiagnosis(incident)?.remediationDirection || ''" />
                </div>

                <div
                  v-for="kind in ['investigation', 'remediation', 'verification']"
                  :key="kind"
                  class="action-section"
                >
                  <template v-if="commandsOf(incidentDiagnosis(incident), kind).length">
                    <span class="sub-title">{{ COMMAND_KIND_LABELS[kind] ?? kind }}建议</span>
                    <ul class="cmd-list">
                      <li v-for="(cmd, index) in commandsOf(incidentDiagnosis(incident), kind)" :key="`cmd-${kind}-${index}`">
                        <pre class="cmd mono"><code>{{ cmd.text }}</code></pre>
                        <span class="pill" :class="commandRiskMeta(cmd, kind).tone">{{ commandRiskMeta(cmd, kind).label }}</span>
                        <button class="copy-btn" type="button" title="复制命令" @click="copyText(cmd.text, '命令')">
                          <el-icon><CopyDocument /></el-icon>
                        </button>
                      </li>
                    </ul>
                  </template>
                </div>

                <ul
                  v-if="incidentDiagnosis(incident)?.validation?.issues?.length"
                  class="dot-list warn validation-issues"
                >
                  <li
                    v-for="(issue, index) in incidentDiagnosis(incident)?.validation?.issues ?? []"
                    :key="`issue-${index}`"
                  >
                    {{ issue }}
                  </li>
                </ul>
                <ul
                  v-if="incidentDiagnosis(incident)?.validation?.commandIssues?.length"
                  class="dot-list warn validation-issues"
                >
                  <li
                    v-for="(issue, index) in incidentDiagnosis(incident)?.validation?.commandIssues ?? []"
                    :key="`cmd-issue-${index}`"
                  >
                    {{ issue }}
                  </li>
                </ul>
              </div>
            </template>
          </article>
        </div>
        <div v-else class="card hint-card">
          <span class="faint">
            {{ insp ? '本轮没有发现可聚合的异常，没有故障链要展示。' : '这份报告没有故障链区块（旧版报告结构）。' }}
          </span>
        </div>
      </section>

      <!-- 4 治理建议：安全/基线类建议不混入业务故障链 -->
      <section v-if="advisoryCount" class="block">
        <div class="block-head">
          <h3 class="block-title">治理建议 <span class="mono faint">{{ advisoryCount }}</span></h3>
          <span class="block-hint faint">安全基线建议，不代表当前业务故障</span>
        </div>
        <div class="card list-card advisory-list">
          <details v-for="incident in advisoryIncidents" :key="incident.id" class="advisory-item">
            <summary>
              <span class="pill" :class="severityTone(incident.severity)">{{ severityText(incident.severity) }}</span>
              <span class="t">{{ incident.title }}</span>
              <span class="mono faint">{{ resourceText(incident.root.resource) }}</span>
            </summary>
            <div class="advisory-body">
              <MarkdownView :text="incidentRootCause(incident)" />
              <p v-if="incidentDiagnosis(incident)?.uncertainty" class="plain-text faint">
                未确认边界：{{ incidentDiagnosis(incident)?.uncertainty }}
              </p>
              <p
                v-if="incidentDiagnosis(incident)?.remediationText || incidentDiagnosis(incident)?.remediationDirection"
                class="plain-text"
              >
                {{ incidentDiagnosis(incident)?.remediationText || incidentDiagnosis(incident)?.remediationDirection }}
              </p>
            </div>
          </details>
        </div>
      </section>

      <!-- 5 完整异常清单：核心结论已在故障链，此处保留全部事实并按需展开 -->
      <section class="block">
        <div class="block-head">
          <h3 class="block-title">完整异常清单 <span class="mono faint">{{ findingCount }}</span></h3>
          <span class="block-hint faint">规则引擎逐条产出，默认折叠；重点结论以上方故障链为准</span>
        </div>
        <div v-if="findingCount" class="card list-card">
          <div
            v-for="finding in insp?.findings ?? []"
            :key="finding.id"
            class="item"
            :class="{ open: openFindings.includes(finding.id) }"
          >
            <div class="item-row">
              <button class="expander" type="button" @click="toggleFinding(finding.id)">
                <el-icon><component :is="openFindings.includes(finding.id) ? ArrowDown : ArrowRight" /></el-icon>
              </button>
              <span class="pill" :class="severityTone(finding.severity)">
                <el-icon><component :is="severityIcon(finding.severity)" /></el-icon>
                {{ severityText(finding.severity) }}
              </span>
              <span class="t ellipsis">{{ finding.title }}</span>
              <span class="mono faint ellipsis" :title="resourceText(finding.resource)">
                {{ resourceText(finding.resource) }}
              </span>
              <span v-if="finding.correlated" class="pill info" title="已归入某条故障链">已关联</span>
              <span v-if="finding.role" class="pill mute">{{ findingRoleLabel(finding.role) }}</span>
              <span v-if="finding.suggestion && !finding.role" class="pill mute">建议项</span>
              <span class="pill mute mono">{{ finding.rule }}</span>
            </div>
            <template v-if="openFindings.includes(finding.id)">
              <p class="plain-text">{{ finding.summary }}</p>
              <div v-if="finding.evidence?.length" class="sub-block">
                <div v-for="evidence in finding.evidence" :key="evidence.id" class="evidence">
                  <div class="evidence-head">
                    <span class="ev-id mono">{{ evidence.id }}</span>
                    <span class="pill mute">{{ evidence.kind }}</span>
                    <span class="mono faint ellipsis">{{ evidence.key }}</span>
                    <span v-if="evidence.redacted" class="pill warn">含脱敏字段</span>
                    <button class="copy-btn" type="button" title="复制该条证据" @click="copyText(evidence.value, '证据')">
                      <el-icon><CopyDocument /></el-icon>
                    </button>
                  </div>
                  <pre
                    class="ev-value mono"
                    :class="{ collapsed: isLongEvidence(evidence.value) && !isEvidenceOpen('finding' + finding.id, evidence.id) }"
                  >{{ evidence.value }}</pre>
                  <button
                    v-if="isLongEvidence(evidence.value)"
                    class="fold-btn faint"
                    type="button"
                    @click="toggleEvidence('finding' + finding.id, evidence.id)"
                  >
                    {{ isEvidenceOpen('finding' + finding.id, evidence.id) ? '收起' : '展开' }}
                  </button>
                </div>
              </div>
            </template>
          </div>
        </div>
        <div v-else class="card hint-card">
          <span class="faint">{{ insp ? '本轮没有命中任何规则。' : '这份报告没有异常明细（旧版报告结构）。' }}</span>
        </div>
      </section>

      <!-- 5 资源健康 -->
      <section v-if="statistics.length || components.length" class="block">
        <div class="block-head">
          <h3 class="block-title">资源健康</h3>
          <span class="block-hint faint">按资源域汇总，关键组件单列</span>
        </div>
        <div class="card list-card">
          <div v-if="statistics.length" class="domain-grid">
            <div v-for="row in statistics" :key="row.domain" class="domain" :class="row.critical ? 'err' : row.unhealthy ? 'warn' : 'ok'">
              <div class="domain-name">{{ row.domain }}</div>
              <div class="domain-v mono">{{ row.unhealthy }} / {{ row.total }}</div>
              <div class="domain-l faint">异常 / 总数{{ row.critical ? ` · 严重 ${row.critical}` : '' }}</div>
              <div v-if="row.detail" class="domain-detail faint ellipsis" :title="row.detail">{{ row.detail }}</div>
            </div>
          </div>
          <div v-if="components.length" class="comp-block">
            <span class="sub-title">关键组件</span>
            <div class="comp-grid">
              <div v-for="row in components" :key="`${row.namespace}/${row.name}`" class="comp">
                <span class="pill" :class="!row.present ? 'mute' : row.ready ? 'ok' : 'warn'">
                  {{ !row.present ? '未部署' : row.ready ? '就绪' : '未就绪' }}
                </span>
                <span class="mono ellipsis" :title="row.detail">{{ row.namespace ? `${row.namespace}/` : '' }}{{ row.name }}</span>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- 6 处置建议汇总 -->
      <section v-if="remediation.length" class="block">
        <div class="block-head">
          <h3 class="block-title">处置建议 <span class="mono faint">{{ remediation.length }}</span></h3>
          <span class="block-hint faint">
            体检只读：以下命令都需要你在具备相应 RBAC 的终端里自行执行，平台不会代跑
          </span>
        </div>
        <div class="card list-card remedy-card">
          <div v-for="kind in ['investigation', 'remediation', 'verification']" :key="kind" class="remedy-group">
            <template v-if="remediationOfKind(kind).length">
              <span class="sub-title">{{ COMMAND_KIND_LABELS[kind] ?? kind }}</span>
              <ul class="cmd-list">
                <li v-for="(item, index) in remediationOfKind(kind)" :key="`remedy-${kind}-${index}`">
                  <template v-if="isRemediationCommand(item)">
                    <pre class="cmd mono"><code>{{ item.text }}</code></pre>
                    <span v-if="item.target" class="pill mute mono ellipsis" :title="item.target">{{ item.target }}</span>
                    <span
                      v-if="item.risk || kind === 'investigation' || kind === 'verification'"
                      class="pill"
                      :class="riskMeta(item.risk || 'SAFE').tone"
                    >
                      {{ riskMeta(item.risk || 'SAFE').label }}
                    </span>
                    <span v-if="item.required" class="pill err">必须处理</span>
                    <button class="copy-btn" type="button" title="复制" @click="copyText(item.text, '建议')">
                      <el-icon><CopyDocument /></el-icon>
                    </button>
                  </template>
                  <MarkdownView v-else :text="item.text" />
                </li>
              </ul>
            </template>
          </div>
        </div>
      </section>

      <!-- 7 采集与诊断质量 -->
      <section class="block">
        <div class="block-head">
          <h3 class="block-title">采集与诊断质量</h3>
          <span class="block-hint faint">结论能信到什么程度，取决于这一栏</span>
        </div>
        <div class="card list-card quality-card">
          <div class="quality-row">
            <span class="pill" :class="collectionMeta.tone">{{ collectionMeta.label }}</span>
            <span v-if="collection" class="mono faint">
              计划 {{ collection.planned }} · 成功 {{ collection.collected }} · 失败
              {{ collection.failed }} · 跳过 {{ collection.skipped }} · 覆盖率 {{ coverageText }}
            </span>
            <span v-else class="faint">采集质量账未记录（旧版报告结构）</span>
          </div>
          <div v-if="collection?.criticalGaps?.length" class="sub-block">
            <span class="sub-title warn-title">关键数据缺口</span>
            <ul class="dot-list err">
              <li v-for="(item, index) in collection?.criticalGaps ?? []" :key="`cg-${index}`">{{ item }}</li>
            </ul>
          </div>
          <div v-if="collection?.missingData?.length" class="sub-block">
            <span class="sub-title">未采集到的数据</span>
            <ul class="dot-list warn">
              <li v-for="(item, index) in collection?.missingData ?? []" :key="`md-${index}`">{{ item }}</li>
            </ul>
          </div>
          <div v-if="collectionErrors.length" class="sub-block">
            <span class="sub-title">采集失败明细 <span class="mono faint">{{ collectionErrors.length }}</span></span>
            <ul class="err-list">
              <li v-for="(item, index) in collectionErrors" :key="`ce-${index}`">
                <span class="mono">{{ item.operation }}</span>
                <span class="mono faint">{{ resourceText(item.resource) }}</span>
                <span class="ellipsis">{{ item.message }}</span>
              </li>
            </ul>
          </div>
          <div v-if="diagSummary" class="sub-block">
            <span class="sub-title">诊断账</span>
            <p class="plain-text mono faint">
              规则异常 {{ diagSummary.ruleFindings }} · 确定性结论 {{ diagSummary.deterministic }}
              （确认 {{ diagSummary.confirmed }} / 疑似 {{ diagSummary.probable }} / 未定位
              {{ diagSummary.unknown }}） · 大模型调用 {{ diagSummary.llmCalls }}（成功
              {{ diagSummary.llmSucceeded }} / 失败 {{ diagSummary.llmFailed }} / 降级
              {{ diagSummary.llmDegraded }} / 校验退回 {{ diagSummary.llmRejected }}） · 合并
              {{ diagSummary.hybrid }}
            </p>
            <p class="plain-text">{{ llmModeText }}</p>
          </div>
          <div v-if="degradations.length" class="sub-block">
            <span class="sub-title">大模型降级明细 <span class="mono faint">{{ degradations.length }}</span></span>
            <ul class="err-list">
              <li v-for="(item, index) in degradations" :key="`deg-${index}`">
                <span class="pill" :class="item.status === 'success' ? 'ok' : 'warn'">
                  {{ LLM_STATUS_LABELS[item.status] ?? item.status }}
                </span>
                <span class="mono faint">{{ item.incidentId || item.findingId || '—' }}</span>
                <span class="ellipsis">{{ item.reason }}</span>
                <span v-if="item.keptDeterministic" class="pill ok">已保留确定性结论</span>
              </li>
            </ul>
          </div>
          <div v-if="!collectionErrors.length && !degradations.length && !collection" class="hint-inline faint">
            这份报告没有质量账区块，无法判断采集与诊断是否完整；如需评估，请重新发起一次体检。
          </div>
        </div>
      </section>

      <!-- 8 与上一轮对比 -->
      <section v-if="history" class="block">
        <div class="block-head">
          <h3 class="block-title">与上一轮对比</h3>
          <span class="block-hint faint">上一轮 {{ formatTime(history.previousScanAt) }}</span>
        </div>
        <div class="stat-grid">
          <div class="card stat">
            <div class="l">新增</div>
            <div class="v mono err">{{ history.added?.length ?? 0 }}</div>
          </div>
          <div class="card stat">
            <div class="l">持续</div>
            <div class="v mono warn">{{ history.continued?.length ?? 0 }}</div>
          </div>
          <div class="card stat">
            <div class="l">已恢复</div>
            <div class="v mono ok">{{ history.recovered?.length ?? 0 }}</div>
          </div>
        </div>
        <div v-if="history.recovered?.length" class="card list-card recovered-card">
          <div v-for="item in history.recovered" :key="item.id" class="item-row quiet">
            <el-icon class="ok"><CircleCheckFilled /></el-icon>
            <span class="ellipsis">{{ item.title }}</span>
            <span class="mono faint ellipsis">{{ resourceText(item.resource) }}</span>
          </div>
        </div>
      </section>

      <!-- 9 体检过程：真实阶段状态与耗时，不编造百分比 -->
      <section v-if="stages.length" class="block">
        <div class="block-head">
          <h3 class="block-title">体检过程 <span class="mono faint">{{ insp?.meta?.duration || '—' }}</span></h3>
          <span class="block-hint faint">只有真实阶段状态与耗时；未执行的阶段会写明原因</span>
        </div>
        <div class="card list-card stage-card">
          <!-- 状态类名与语气类名都要绑：状态驱动圆点配色，语气驱动文字色，缺一个就会静默掉色 -->
          <div
            v-for="stage in stages"
            :key="stage.name"
            class="stage"
            :class="[stage.status, metaOf(STAGE_META, stage.status, stage.status).tone]"
          >
            <span class="stage-dot">
              <el-icon v-if="stage.status === 'running'" class="is-loading"><Loading /></el-icon>
              <el-icon v-else-if="stage.status === 'done'"><CircleCheckFilled /></el-icon>
              <el-icon v-else-if="stage.status === 'failed'"><CircleCloseFilled /></el-icon>
              <el-icon v-else><InfoFilled /></el-icon>
            </span>
            <span class="stage-title">{{ stage.title || stage.name }}</span>
            <span class="pill" :class="metaOf(STAGE_META, stage.status, stage.status).tone">
              {{ metaOf(STAGE_META, stage.status, stage.status).label }}
            </span>
            <span class="stage-elapsed mono faint">{{ stage.elapsed || (stage.status === 'skipped' ? '—' : '') }}</span>
            <span v-if="stage.detail" class="stage-detail faint ellipsis" :title="stage.detail">{{ stage.detail }}</span>
            <span v-if="stage.progress" class="stage-progress faint ellipsis" :title="stage.progress">{{ stage.progress }}</span>
          </div>
        </div>
      </section>

      <!-- 10 原始数据 -->
      <section class="block">
        <div class="block-head">
          <h3 class="block-title">原始数据</h3>
          <span class="block-hint faint">排查平台自身问题时用：这就是归档 JSON 的内容</span>
          <div class="block-actions">
            <el-button size="small" link @click="rawOpen = !rawOpen">
              {{ rawOpen ? '收起 JSON' : '查看 JSON' }}
            </el-button>
            <el-button size="small" :icon="Download" :disabled="!insp" @click="downloadJson">下载 JSON</el-button>
            <el-button size="small" :icon="Connection" @click="copyText(jsonText, 'JSON')">复制 JSON</el-button>
          </div>
        </div>
        <div v-if="rawOpen" class="card raw-card">
          <pre class="raw mono">{{ jsonPreview }}</pre>
        </div>
      </section>
    </template>
  </section>
</template>

<style scoped>
.detail-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.page-head {
  display: flex;
  align-items: center;
  gap: 10px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  padding: 12px 16px;
  min-width: 0;
}

.page-head .title {
  margin: 0;
  font-size: 16px;
  min-width: 0;
}

.head-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.card {
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}

.hint-card {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px 16px;
  font-size: 12.5px;
}

.alert {
  margin: 0;
  padding: 10px 14px;
  border-radius: var(--app-radius-sm);
  font-size: 12.5px;
}

.alert.err {
  color: var(--app-danger);
  background: var(--app-danger-soft);
}

/* ------------------------------------------------------------ 结论区 */

.conclusion-card {
  display: flex;
  gap: 20px;
  padding: 16px 18px;
  align-items: flex-start;
  min-width: 0;
}

.conclusion-card.err {
  border-color: var(--app-danger-border);
}

.conclusion-card.warn {
  border-color: var(--app-warning);
}

.conclusion-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.conclusion-top {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 11.5px;
}

.headline {
  margin: 0;
  font-size: 15px;
  font-weight: 650;
  color: var(--app-strong);
  line-height: 1.5;
}

.summary {
  margin: 0;
  font-size: 12.5px;
  color: var(--app-text);
  line-height: 1.7;
}

.primary-cause {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  border-left: 3px solid var(--app-danger);
  border-radius: 0 var(--app-radius-sm) var(--app-radius-sm) 0;
  background: var(--app-danger-soft);
  font-size: 12.5px;
  color: var(--app-text);
}

.primary-cause :deep(.md-body p) {
  margin: 0;
  font-weight: 600;
  line-height: 1.65;
}

.primary-uncertainty {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.55;
}

.list-block {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.sub-title {
  font-size: 11.5px;
  font-weight: 650;
  color: var(--app-muted);
  letter-spacing: 0.02em;
}

.warn-title {
  color: var(--app-warning);
}

.dot-list,
.num-list {
  margin: 0;
  padding-left: 18px;
  font-size: 12.5px;
  line-height: 1.75;
  color: var(--app-text);
}

.dot-list.warn li::marker {
  color: var(--app-warning);
}

.dot-list.err li::marker {
  color: var(--app-danger);
}

.score-box {
  width: 168px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
  text-align: right;
  border-left: 1px solid var(--app-border);
  padding-left: 18px;
}

.score-v {
  font-size: 30px;
  font-weight: 700;
  line-height: 1.1;
}

.score-v.ok {
  color: var(--app-success);
}

.score-v.warn {
  color: var(--app-warning);
}

.score-v.err {
  color: var(--app-danger);
}

.score-v.mute {
  color: var(--app-muted);
}

.score-l {
  font-size: 11.5px;
}

.cap-reason {
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  white-space: normal;
}

@media (max-width: 900px) {
  .conclusion-card {
    flex-direction: column;
  }

  .score-box {
    width: auto;
    align-items: flex-start;
    text-align: left;
    border-left: none;
    border-top: 1px solid var(--app-border);
    padding-left: 0;
    padding-top: 12px;
  }
}

/* ------------------------------------------------------------ 概览 */

.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 12px;
}

.stat {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding: 13px 16px;
  min-width: 0;
}

.stat .l {
  font-size: 12px;
  color: var(--app-muted);
}

.stat .v {
  font-size: 19px;
  font-weight: 650;
  color: var(--app-strong);
}

.stat .v.ok {
  color: var(--app-success);
}

.stat .v.warn {
  color: var(--app-warning);
}

.stat .v.err {
  color: var(--app-danger);
}

.stat .u {
  font-size: 11.5px;
  color: var(--app-faint);
  line-height: 1.5;
}

.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
}

/* ------------------------------------------------------- 区块与条目 */

.block {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

.block-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
  padding: 0 2px;
}

.block-title {
  margin: 0;
  font-size: 13.5px;
  font-weight: 650;
  color: var(--app-strong);
}

.block-hint {
  font-size: 11.5px;
}

.block-action,
.block-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 6px;
}

.list-card {
  padding: 4px 0;
  overflow: hidden;
}

.item {
  border-bottom: 1px solid var(--app-border);
  padding: 8px 16px 10px;
  min-width: 0;
}

.item:last-child {
  border-bottom: none;
}

.item.open {
  background: var(--app-surface-2);
}

.item-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex-wrap: wrap;
}

.item-row.quiet {
  padding: 6px 16px;
  font-size: 12.5px;
}

.item-row .t {
  font-size: 12.5px;
  color: var(--app-text);
  flex-shrink: 1;
  min-width: 60px;
}

.expander {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  background: transparent;
  color: var(--app-muted);
  cursor: pointer;
  flex-shrink: 0;
  padding: 0;
}

.expander:hover {
  color: var(--app-accent);
}

.impact-pill {
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 根因用强调块，症状用缩进清单：一眼能认出先去修哪个 */
.root-band {
  margin: 10px 0 4px;
  padding: 10px 12px;
  border-left: 3px solid var(--app-accent);
  background: var(--app-accent-soft);
  border-radius: 0 var(--app-radius-sm) var(--app-radius-sm) 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.root-band.ok {
  border-left-color: var(--app-success);
  background: var(--app-success-soft);
}

.root-band.warn {
  border-left-color: var(--app-warning);
  background: var(--app-warning-soft);
}

.root-band.err {
  border-left-color: var(--app-danger);
  background: var(--app-danger-soft);
}

.root-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.root-tag {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  color: var(--app-strong);
}

.root-text {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-strong);
  line-height: 1.6;
}

.root-text :deep(.md-body p) {
  margin: 0;
}

.root-summary {
  margin: 0;
  padding: 7px 9px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 12px;
  line-height: 1.65;
}

.root-basis {
  margin: 0;
  font-size: 11.5px;
  line-height: 1.6;
}

.sub-block {
  margin-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 5px;
  min-width: 0;
}

.plain-text {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--app-text);
  white-space: pre-wrap;
  word-break: break-word;
}

.evidence-panel,
.diagnosis-notes,
.causal-panel,
.member-panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  padding: 0;
  overflow: hidden;
}

.evidence-panel > summary,
.diagnosis-notes > summary,
.causal-panel > summary,
.member-panel > summary,
.advisory-item > summary {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  cursor: pointer;
  color: var(--app-text);
  font-size: 12px;
  font-weight: 600;
  list-style: none;
}

.evidence-panel > summary::-webkit-details-marker,
.diagnosis-notes > summary::-webkit-details-marker,
.causal-panel > summary::-webkit-details-marker,
.member-panel > summary::-webkit-details-marker,
.advisory-item > summary::-webkit-details-marker {
  display: none;
}

.evidence-panel > summary::before,
.diagnosis-notes > summary::before,
.causal-panel > summary::before,
.member-panel > summary::before,
.advisory-item > summary::before {
  content: '›';
  color: var(--app-accent);
  font-size: 16px;
  line-height: 1;
  transition: transform 0.15s ease;
}

.evidence-panel[open] > summary::before,
.diagnosis-notes[open] > summary::before,
.causal-panel[open] > summary::before,
.member-panel[open] > summary::before,
.advisory-item[open] > summary::before {
  transform: rotate(90deg);
}

.evidence-list,
.diagnosis-notes > .sub-block,
.causal-panel > .edge-list,
.member-panel > .member-list {
  padding: 0 12px 10px;
}

.action-panel {
  margin-top: 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.action-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.action-title {
  color: var(--app-strong);
  font-size: 13px;
  font-weight: 650;
}

.action-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-top: 10px;
  border-top: 1px dashed var(--app-border);
}

.action-section:first-of-type {
  padding-top: 0;
  border-top: none;
}

.validation-issues {
  margin-top: 2px;
}

.edge-list,
.chain-list,
.member-list,
.cmd-list,
.err-list {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.chain-list {
  padding-left: 18px;
  list-style: decimal;
  font-size: 12.5px;
  line-height: 1.7;
}

.edge-list li,
.member-list li,
.err-list li {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  font-size: 12px;
  min-width: 0;
}

.edge-arrow {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  color: var(--app-accent);
  font-size: 11.5px;
}

.edge-mechanism {
  width: 100%;
  margin: 0;
  font-size: 11.5px;
  line-height: 1.6;
}

.member-list {
  padding-left: 12px;
  border-left: 1px solid var(--app-border);
}

.cmd-list li {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.cmd {
  flex: 1;
  min-width: 0;
  margin: 0;
  padding: 5px 8px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  font-size: 11.5px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 260px;
  overflow: auto;
}

.cmd code {
  font: inherit;
  color: inherit;
}

.copy-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  flex-shrink: 0;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-muted);
  cursor: pointer;
  padding: 0;
}

.copy-btn:hover {
  color: var(--app-accent);
  border-color: var(--app-accent);
}

/* ------------------------------------------------------------- 证据 */

.evidence {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 6px 0;
  border-top: 1px dashed var(--app-border);
}

.evidence-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  min-width: 0;
}

.ev-id {
  font-size: 11px;
  font-weight: 650;
  color: var(--app-accent);
}

.ev-value {
  margin: 0;
  padding: 7px 9px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  font-size: 11.5px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: none;
  overflow: auto;
}

/* 日志这类长证据默认折成 6 行：报告详情页不是日志查看器 */
.ev-value.collapsed {
  max-height: 118px;
  overflow: hidden;
  cursor: pointer;
}

.fold-btn {
  align-self: flex-start;
  border: none;
  background: transparent;
  padding: 0;
  font-size: 11.5px;
  cursor: pointer;
  text-decoration: underline;
}

.fold-btn:hover {
  color: var(--app-accent);
}

/* --------------------------------------------------------- 资源健康 */

.domain-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 10px;
  padding: 12px 16px;
}

.domain {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 9px 11px;
  border-left: 3px solid var(--app-success);
  background: var(--app-surface);
  border-radius: 0 var(--app-radius-sm) var(--app-radius-sm) 0;
  min-width: 0;
}

.domain.warn {
  border-left-color: var(--app-warning);
}

.domain.err {
  border-left-color: var(--app-danger);
}

.domain-name {
  font-size: 12px;
  color: var(--app-text);
}

.domain-v {
  font-size: 16px;
  font-weight: 650;
  color: var(--app-strong);
}

.domain-l,
.domain-detail {
  font-size: 11px;
}

.comp-block {
  padding: 4px 16px 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  border-top: 1px solid var(--app-border);
}

.comp-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
  gap: 6px 14px;
}

.comp {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  min-width: 0;
}

/* ----------------------------------------------------- 处置与质量 */

.remedy-card,
.quality-card {
  padding: 12px 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.remedy-group {
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.quality-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 12px;
}

.hint-inline {
  font-size: 12px;
}

.err-list li {
  padding: 4px 0;
  border-top: 1px dashed var(--app-border);
}

.recovered-card {
  padding: 6px 0;
}

.advisory-item {
  border-bottom: 1px solid var(--app-border);
  padding: 0 14px;
}

.advisory-item:last-child {
  border-bottom: none;
}

.advisory-item > summary .t {
  flex: 1;
  min-width: 0;
}

.advisory-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 0 6px 12px 20px;
  font-size: 12px;
}

/* --------------------------------------------------------- 阶段账 */

.stage-card {
  padding: 6px 0;
}

.stage {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 16px;
  font-size: 12.5px;
  border-bottom: 1px solid var(--app-border);
  min-width: 0;
}

.stage:last-child {
  border-bottom: none;
}

.stage.skipped,
.stage.mute {
  color: var(--app-muted);
}

.stage-dot {
  display: inline-flex;
  width: 16px;
  justify-content: center;
  flex-shrink: 0;
  color: var(--app-muted);
}

.stage.done .stage-dot {
  color: var(--app-success);
}

.stage.failed .stage-dot {
  color: var(--app-danger);
}

.stage.running .stage-dot {
  color: var(--app-info);
}

.stage-title {
  min-width: 108px;
  flex-shrink: 0;
  color: var(--app-text);
}

.stage-elapsed {
  min-width: 62px;
  text-align: right;
  flex-shrink: 0;
}

.stage-detail,
.stage-progress {
  font-size: 11.5px;
  min-width: 0;
}

.stage-detail {
  flex: 1;
}

/* --------------------------------------------------------- 原始 JSON */

.raw-card {
  padding: 10px 12px;
  max-height: 460px;
  overflow: auto;
}

.raw {
  margin: 0;
  font-size: 11px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
}

/* ------------------------------------------------------------ 通用 */

.pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 20px;
  padding: 0 8px;
  border-radius: 10px;
  font-size: 11.5px;
  border: 1px solid var(--app-border);
  color: var(--app-muted);
  background: var(--app-surface);
  flex-shrink: 0;
  max-width: 100%;
}

.pill.ok {
  color: var(--app-success);
  border-color: var(--app-success);
  background: var(--app-success-soft);
}

.pill.err {
  color: var(--app-danger);
  border-color: var(--app-danger-border);
  background: var(--app-danger-soft);
}

.pill.warn {
  color: var(--app-warning);
  border-color: var(--app-warning);
  background: var(--app-warning-soft);
}

.pill.info {
  color: var(--app-info);
  border-color: var(--app-info);
  background: var(--app-info-soft);
}

.pill.mute {
  color: var(--app-muted);
}

.mono {
  font-family: var(--app-mono);
}

.faint {
  color: var(--app-faint);
}

.strong {
  font-weight: 650;
  color: var(--app-text);
}

.ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
