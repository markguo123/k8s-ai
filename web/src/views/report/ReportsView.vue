<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  CircleClose,
  Connection,
  Delete,
  Document,
  Link,
  Refresh,
  Search,
  VideoPlay,
  View,
} from '@element-plus/icons-vue'

import { myScope } from '@/api/auth'
import { listClusters } from '@/api/clusters'
import {
  cleanupReports,
  deleteReport,
  describeReportError,
  downloadReport,
  exportReportMarkdown,
  getReportDownloadUrl,
  getReportSummary,
  getStorageHealth,
  listReports,
} from '@/api/reports'
import {
  SCAN_FORMATS,
  SCAN_PRESETS,
  SCAN_SCOPE_LABELS,
  cancelScan,
  createScan,
  describeCancelError,
  describeScanError,
  getScan,
  isScanFinished,
  type ScanFormat,
  type ScanPreset,
  type ScanScope,
} from '@/api/scans'
import type {
  Cluster,
  EffectiveScope,
  ReportOverview,
  ReportView,
  ScanJob,
  StageRecord,
  StorageHealth,
} from '@/api/types'
import TechEmptyState from '@/components/TechEmptyState.vue'
import { useAuthStore } from '@/stores/auth'
import { hasPermission, PERM, permissionsOf } from '@/utils/permission'

// P8.11：报告产物列表。可见范围（本人 + 有权集群的系统报告）由后端判定，
// 这里只传筛选条件、不做任何归属推断；跨 owner 下载的审计也由后端落，前端只提示。
const auth = useAuthStore()
const router = useRouter()

const reports = ref<ReportView[]>([])
const clusters = ref<Cluster[]>([])
const health = ref<StorageHealth | null>(null)
const loading = ref(false)
const downloading = ref('')
const cleaning = ref(false)

/** 顶部类别总览（可见报告的全量分布，与分页无关）。 */
const overview = ref<ReportOverview | null>(null)

// 勾选范围只在当前可见行里保留：翻页后不再看得见的行自动退出选择，
// 免得「清理选中 12 条」里有 10 条是用户在第三页勾的。
const selected = ref<string[]>([])

// 只看某次体检的产物：体检完成后默认打开，也可手动取消。
const filterScanId = ref('')

const filterKind = ref('')
const filterCategory = ref('')
const filterCluster = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

/** 存储健康是设置域信息，没有 settings.read 的人不查也不显示（后端同样按权限拦）。 */
const canSeeStorage = computed(() => hasPermission(permissionsOf(auth.role), PERM.settingsRead))

/** 发起入口只对持 scan.execute 的人出现；范围与归属判定仍逐次在后端做。 */
const canScan = computed(() => hasPermission(permissionsOf(auth.role), PERM.scanExecute))

/** 「全集群」的可见性与后端同一判据：角色持全域权限 `*`（不是"看起来像管理员"）。 */
const canClusterScope = computed(() => hasPermission(permissionsOf(auth.role), '*'))

const KIND_LABELS: Record<string, string> = {
  'latest-md': '体检报告',
  'latest-json': '体检报告',
}

/**
 * 产物格式的小字标识。
 *
 * 大字标题统一叫「体检报告」——用户发起一次体检就得到一份报告，格式只是这份报告的
 * 存档形态，不该在标题位上长成两个看起来像不同东西的名字。
 */
function artifactLabel(kind: string): string {
  return kind === 'latest-json' ? 'JSON' : 'Markdown'
}

const presignOpen = ref(false)
const presignUrl = ref('')
const presignTarget = ref<ReportView | null>(null)

const storageTone = computed(() => {
  const status = health.value?.status
  if (status === 'ready') return 'ok'
  if (status === 'degraded') return 'err'
  return 'mute'
})

const storageLabel = computed(() => {
  const driver = (health.value?.driver ?? '').toUpperCase()
  switch (health.value?.status) {
    case 'ready':
      return `对象存储 ${driver} 就绪`
    case 'degraded':
      return `对象存储 ${driver} 暂不可用`
    case 'disabled':
      return '未开启对象存储归档'
    default:
      return '对象存储状态未知'
  }
})

const mismatchText = computed(() => {
  const mismatch = health.value?.driverMismatch
  if (!mismatch) return ''
  const parts = Object.entries(mismatch).map(([driver, count]) => `${count} 个仍在 ${driver}`)
  return parts.length ? `历史对象待迁移：${parts.join('、')}` : ''
})

const clusterName = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {}
  for (const item of clusters.value) {
    map[item.id] = item.name || item.id
  }
  return map
})

/**
 * 归属展示成「发起人：张三」（P9.13）。
 *
 * 直接报用户名比「我的报告 / 他人报告」有用：多人共用一套平台时，「别人的」这个信息
 * 得靠用户自己在心里补一步。后端把用户 ID 换成名字，账号删了就是空串，退回显示 ID。
 * 「系统自动」是定时体检这类没有真人发起的产物。
 */
function ownerLabel(row: ReportView): string {
  if (!row.createdBy || row.createdBy === 'system') return '发起人：系统自动'
  return `发起人：${row.createdByName || row.createdBy}`
}

function ownerTone(row: ReportView): string {
  if (!row.createdBy || row.createdBy === 'system') return 'info'
  return row.createdBy === auth.user?.userId ? 'mute' : 'warn'
}

/** 别人的报告要多提醒一句：下载动作会留审计，这不是越权，但也不是自己的东西。 */
function ownerSuffix(row: ReportView): string {
  if (!row.createdBy || row.createdBy === 'system') return ''
  return row.createdBy === auth.user?.userId ? '' : ' · 下载将记入审计'
}

function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind
}

const SCOPE_TONES: Record<string, string> = {
  cluster: 'info',
  namespace: 'mute',
  pod: 'mute',
}

/**
 * 报告类别徽标（P9.13 口径）。
 *
 * 类别只有两种是现在就存在的：全集群 = 集群体检，命名空间/单 Pod = 业务体检。
 * 「系统体检」（K8s 系统组件与节点）是预留词，本期没有对应实现，因此这里刻意不占用它，
 * 免得用户以为点了就有东西。
 */
function scopeCategoryLabel(row: ReportView): string {
  switch (row.scopeKind) {
    case 'cluster':
      return '集群体检'
    case 'namespace':
    case 'pod':
      return '业务体检'
    default:
      return '范围未记录'
  }
}

/**
 * 类别之下的具体范围：命名空间名或 Pod 名。
 *
 * 范围留痕之前的老报告返回空串，这时候只剩「范围未记录」那一枚徽标，不去编一个范围出来。
 */
function scopeDetailLabel(row: ReportView): string {
  const label = SCAN_SCOPE_LABELS[row.scopeKind as ScanScope]
  if (!label) return ''
  return row.scopeKind === 'cluster' ? label : `${label} ${row.scopeValue || '—'}`
}

function scopeTone(row: ReportView): string {
  return SCOPE_TONES[row.scopeKind ?? ''] ?? 'mute'
}

function scopeTextOf(kind?: string, value?: string): string {
  if (!kind) return '体检'
  const label = SCAN_SCOPE_LABELS[kind as ScanScope] ?? kind
  return value ? `${label} ${value}` : label
}

/** 空态文案跟着权限走：没有发起入口的人，不该被指着点一个不存在的按钮。 */
const emptyHint = computed(() =>
  canScan.value
    ? '点击右上角「开始体检」生成报告；他人的报告需要你拥有对应集群的授权。'
    : '报告由具备体检权限的成员生成；他人的报告需要你拥有对应集群的授权。',
)

// ------------------------------------------------------------- 勾选与清理

const allSelected = computed(
  () => reports.value.length > 0 && reports.value.every((row) => selected.value.includes(row.id)),
)

function toggleRow(id: string, checked: boolean): void {
  const next = new Set(selected.value)
  if (checked) {
    next.add(id)
  } else {
    next.delete(id)
  }
  selected.value = [...next]
}

function toggleAll(checked: boolean): void {
  selected.value = checked ? reports.value.map((row) => row.id) : []
}

/**
 * 清理 = 删掉「平台认为这份报告存在」这条业务引用（P8.14）。正文不在这里删：
 * blob_key 内容寻址、可能被多份报告共享，回收统一交给 blobs gc 的宽限期。
 *
 * 归属判定在后端，别人的 id 只会变成一条 skipped；单条与批量共用同一套收尾逻辑。
 */
async function removeReports(ids: string[]): Promise<void> {
  if (!ids.length) {
    return
  }
  cleaning.value = true
  try {
    const result = ids.length === 1 ? await deleteReport(ids[0]) : await cleanupReports(ids)
    if (result.deleted.length) {
      ElMessage.success(`已清理 ${result.deleted.length} 条报告，下载入口同时失效`)
    }
    if (result.skipped.length) {
      ElMessage.warning(`${result.skipped.length} 条未清理：报告已不存在或暂时不可用`)
    }
    selected.value = selected.value.filter((id) => !result.deleted.includes(id))
    await loadReports()
  } catch (error) {
    ElMessage.error(describeReportError('清理报告', error))
  } finally {
    cleaning.value = false
  }
}

const CLEANUP_WARNING =
  '清理后该报告立即无法下载，正文由对象存储回收任务在宽限期后删除，此操作不可恢复。'

async function confirmCleanupSelected(): Promise<void> {
  const ids = [...selected.value]
  try {
    await ElMessageBox.confirm(
      `确定清理选中的 ${ids.length} 条报告？${CLEANUP_WARNING}`,
      '清理报告',
      { type: 'warning', confirmButtonText: '确定清理', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await removeReports(ids)
}

async function confirmCleanupRow(row: ReportView): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `确定清理「${scopeCategoryLabel(row)} · ${artifactLabel(row.kind)} 体检报告」？${CLEANUP_WARNING}`,
      '清理报告',
      { type: 'warning', confirmButtonText: '确定清理', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  await removeReports([row.id])
}

function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB']
  let value = bytes / 1024
  let unit = units[0]
  for (let i = 1; i < units.length && value >= 1024; i++) {
    value /= 1024
    unit = units[i]
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${unit}`
}

function formatTime(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

async function loadReports(): Promise<void> {
  loading.value = true
  try {
    const result = await listReports({
      page: page.value,
      pageSize: pageSize.value,
      kind: filterKind.value,
      scopeCategory: filterCategory.value,
      clusterId: filterCluster.value,
      scanId: filterScanId.value,
    })
    reports.value = result.items
    total.value = result.total
    // 只保留还在这一页里的勾选：跨页留着看不见的勾选，批量确认时的数字就骗人了。
    const visible = new Set(result.items.map((row) => row.id))
    selected.value = selected.value.filter((id) => visible.has(id))
  } catch (error) {
    ElMessage.error(describeReportError('加载报告列表', error))
    reports.value = []
    total.value = 0
    selected.value = []
  } finally {
    loading.value = false
  }
}

/**
 * 顶部总览走独立端点：它要的是「全部可见行」的分布，翻页时数字本来就不该动，
 * 因此只在筛选条件变化和手动刷新时请求。失败降级成全 0 而不是弹错——列表能打开
 * 才是这一屏的主要任务，统计条读不到时显示 0 比整屏报错更诚实。
 */
async function loadOverview(): Promise<void> {
  try {
    overview.value = await getReportSummary({
      kind: filterKind.value,
      scopeCategory: filterCategory.value,
      clusterId: filterCluster.value,
      scanId: filterScanId.value,
    })
  } catch {
    overview.value = null
  }
}

async function loadSidePanels(): Promise<void> {
  try {
    clusters.value = await listClusters()
  } catch {
    // 集群列表只服务于筛选项：拿不到就只留"全部集群"，不该连带打断报告列表。
    clusters.value = []
  }
  if (!canSeeStorage.value) {
    return
  }
  try {
    health.value = await getStorageHealth()
  } catch {
    health.value = null
  }
}

async function refreshAll(): Promise<void> {
  await Promise.all([loadReports(), loadOverview(), loadSidePanels()])
}

function applyFilters(): void {
  page.value = 1
  // 统计条和列表共用后端的同一条筛选谓词，两者必须一起变，否则同一屏里数字会互相打脸。
  void Promise.all([loadReports(), loadOverview()])
}

function changePage(next: number): void {
  page.value = Math.max(1, next)
  void loadReports()
}

async function handleDownload(row: ReportView): Promise<void> {
  downloading.value = row.id
  try {
    const filename = await downloadReport(row.id)
    ElMessage.success(`已下载 ${filename}`)
  } catch (error) {
    ElMessage.error(describeReportError('下载报告', error))
  } finally {
    downloading.value = ''
  }
}

/**
 * 按需导出一份 Markdown（P9.13）。
 *
 * 一次体检只归档发起时选中的那一种格式，另一种不落对象存储：列表里也就永远不会长出
 * 两条几乎相同的行。想要另一种格式时由后端用同一份正文当场渲染，两边不会互相过期。
 */
async function handleExportMarkdown(row: ReportView): Promise<void> {
  downloading.value = row.id
  try {
    const filename = await exportReportMarkdown(row.id)
    ElMessage.success(`已导出 ${filename}`)
  } catch (error) {
    ElMessage.error(describeReportError('导出 Markdown', error))
  } finally {
    downloading.value = ''
  }
}

/**
 * 进入报告详情页（P9）。详情页自己按 id 取结构化正文——列表响应里不带正文，也不该带。
 * 内容已不可恢复的行不给入口：那里同样读不到正文，只是把 410 换个地方报一次。
 */
function openDetail(row: ReportView): void {
  void router.push({ name: 'report-detail', params: { id: row.id } })
}

/** 直连链接只在弹窗内存里存在，关闭即清空：签名 URL 等同临时凭据。 */
async function handlePresign(row: ReportView): Promise<void> {
  downloading.value = row.id
  try {
    const ticket = await getReportDownloadUrl(row.id)
    presignTarget.value = row
    presignUrl.value = ticket.downloadUrl
    presignOpen.value = true
  } catch (error) {
    ElMessage.error(describeReportError('生成直连链接', error))
  } finally {
    downloading.value = ''
  }
}

function closePresign(): void {
  presignOpen.value = false
  presignUrl.value = ''
  presignTarget.value = null
}

async function copyPresign(): Promise<void> {
  try {
    await navigator.clipboard.writeText(presignUrl.value)
    ElMessage.success('链接已复制，请注意它带签名，不要转发到群聊或工单')
  } catch {
    ElMessage.warning('当前浏览器不允许自动复制，请手动选中链接')
  }
}

/** 只有 s3 后端有直连能力；其余后端不显示这个按钮，省得点了才知道不支持。 */
const canPresign = computed(() => health.value?.driver === 's3' && health.value?.ready === true)

// ------------------------------------------------------- 发起体检（P8.13）

const scanOpen = ref(false)
const scanSubmitting = ref(false)
const scanScope = ref<ScanScope>('namespace')
const scanNamespace = ref('')
const scanPod = ref('')
const scanPreset = ref<ScanPreset>('standard')
/** 报告格式：一次体检只归档这一种，另一种走「导出 Markdown」按需生成。 */
const scanFormat = ref<ScanFormat>('json')

/** 自己的授权范围只用来填下拉候选；真正的判定在后端，拉不到也不该拦住发起。 */
const scopeInfo = ref<EffectiveScope | null>(null)
const namespaceOptions = computed(() => scopeInfo.value?.namespaces ?? [])

const activeJob = ref<ScanJob | null>(null)
const activePreset = ref('')
/** 完成后留下的指针：让「只看本次产物」在横幅关掉之后仍然点得到。 */
const lastScanId = ref('')
let pollTimer: number | undefined

/**
 * 进行中体检的 id 记在 sessionStorage 里（P9.14）。
 *
 * 任务是后端在跑的，前端没有任何理由因为一次刷新就把它忘掉：挂载时按这里记的 id 取一次
 * 状态，就能把进度横幅重新挂上。用 sessionStorage 而不是 localStorage——换一张标签页
 * 就是新会话，不该把别的窗口那次的进度搬过来。
 */
const ACTIVE_SCAN_KEY = 'k8s-ai.activeScan'

function readStoredScan(): { id: string; preset: string } | null {
  try {
    const raw = window.sessionStorage.getItem(ACTIVE_SCAN_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as { id?: unknown; preset?: unknown }
    if (typeof parsed.id !== 'string' || !parsed.id) return null
    return {
      id: parsed.id,
      preset: typeof parsed.preset === 'string' ? parsed.preset : '',
    }
  } catch {
    return null
  }
}

function storeActiveScan(id: string, preset: string): void {
  try {
    window.sessionStorage.setItem(ACTIVE_SCAN_KEY, JSON.stringify({ id, preset }))
  } catch {
    // 隐私模式写不进去：代价只是刷新后看不到进度，发起体检本身不受影响。
  }
}

function clearStoredScan(): void {
  try {
    window.sessionStorage.removeItem(ACTIVE_SCAN_KEY)
  } catch {
    // 写不进去也就删不掉，同样只影响进度横幅。
  }
}

const scanBusy = computed(() => activeJob.value !== null && !isScanFinished(activeJob.value))

const SCAN_STATUS_LABELS: Record<string, string> = {
  pending: '排队中',
  running: '执行中',
  succeeded: '已完成',
  failed: '失败',
  cancelled: '已停止',
}

/** 阶段状态徽标（后端阶段账只有这四种取值）。 */
const STAGE_STATUS_LABELS: Record<string, string> = {
  running: '进行中',
  done: '完成',
  failed: '失败',
  skipped: '跳过',
}

const activeTone = computed(() => {
  const status = activeJob.value?.status
  if (status === 'failed') return 'err'
  if (status === 'cancelled') return 'warn'
  if (status === 'succeeded') return 'ok'
  return 'info'
})

const activeScanTitle = computed(() => {
  const job = activeJob.value
  if (!job) return ''
  const label = SCAN_STATUS_LABELS[job.status] ?? job.status
  const preset = activePreset.value ? ` · ${presetLabel(activePreset.value)}` : ''
  return `${label} · ${scopeTextOf(job.scopeKind, job.scopeValue)}${preset}`
})

const activeScanDetail = computed(() => {
  const job = activeJob.value
  if (!job) return ''
  if (job.status === 'failed') return job.error || '未知原因'
  if (job.status === 'cancelled') return job.error || '已由发起人停止，本次不产出报告'
  if (job.status === 'succeeded') {
    const findings = job.result?.findings?.length ?? 0
    const score = job.result?.healthScore
    const scoreText =
      typeof score?.score === 'number' && typeof score?.max === 'number'
        ? ` · 健康度 ${score.score}/${score.max}`
        : ''
    return `发现 ${findings} 条问题${scoreText}，产物已归档到报告列表`
  }
  return '体检正在执行，完成后产物会自动出现在下面的列表里'
})

/** 阶段账快照。排队中的任务还没有阶段，页面据此显示"等待开始"。 */
const activeStages = computed<StageRecord[]>(() => activeJob.value?.stages ?? [])

const stageDoneCount = computed(
  () => activeStages.value.filter((s) => s.status === 'done' || s.status === 'skipped').length,
)

function stageTone(status: string): string {
  if (status === 'done') return 'ok'
  if (status === 'failed') return 'err'
  if (status === 'running') return 'info'
  return 'mute'
}

function presetLabel(value: string): string {
  return SCAN_PRESETS.find((item) => item.value === value)?.label ?? value
}

function stopPolling(): void {
  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer)
    pollTimer = undefined
  }
}

async function loadScope(): Promise<void> {
  try {
    scopeInfo.value = await myScope()
  } catch {
    // 只影响候选列表：管理员可以直接输入，普通用户仍可手输名字，后端照样逐次判定。
    scopeInfo.value = null
  }
  if (!scanNamespace.value && namespaceOptions.value.length === 1) {
    scanNamespace.value = namespaceOptions.value[0]
  }
}

function openScanDialog(): void {
  scanOpen.value = true
  if (!namespaceOptions.value.length && !canClusterScope.value) {
    void loadScope()
  }
}

function submitScan(): void {
  const ns = scanNamespace.value.trim()
  const pod = scanPod.value.trim()
  if (scanScope.value !== 'cluster' && !ns) {
    ElMessage.warning('请选择要体检的命名空间')
    return
  }
  if (scanScope.value === 'pod' && !pod) {
    ElMessage.warning('单 Pod 体检还需要填 Pod 名称')
    return
  }
  scanSubmitting.value = true
  void (async () => {
    try {
      const job = await createScan({
        scope: scanScope.value,
        namespace: ns,
        pod,
        preset: scanPreset.value,
        format: scanFormat.value,
      })
      activePreset.value = scanPreset.value
      activeJob.value = job
      scanOpen.value = false
      storeActiveScan(job.id, scanPreset.value)
      startPolling(job.id)
    } catch (error) {
      ElMessage.error(describeScanError(error))
    } finally {
      scanSubmitting.value = false
    }
  })()
}

/** 轮询到终态即停：后端一次只跑一个任务，挂着定时器没有任何意义。 */
function startPolling(id: string): void {
  stopPolling()
  pollTimer = window.setInterval(async () => {
    try {
      const job = await getScan(id)
      activeJob.value = job
      if (!isScanFinished(job)) {
        return
      }
      stopPolling()
      clearStoredScan()
      if (job.status === 'failed') {
        ElMessage.error(`体检失败：${job.error || '未知原因'}`)
        return
      }
      if (job.status === 'cancelled') {
        // 停止是自己按的，用 warning 而不是 error，也别去筛一个不会存在的产物。
        ElMessage.warning('该次体检已停止，本次不产出报告')
        return
      }
      lastScanId.value = job.id
      filterScanId.value = job.id
      ElMessage.success(`体检完成，发现 ${job.result?.findings?.length ?? 0} 条问题`)
      await Promise.all([loadReports(), loadOverview()])
    } catch (error) {
      stopPolling()
      ElMessage.error(describeScanError(error))
    }
  }, 3000)
}

function dismissScanBanner(): void {
  stopPolling()
  clearStoredScan()
  activeJob.value = null
}

/**
 * 停止体检（P9.11）。
 *
 * 后端在返回前就把状态写成 cancelled 并取消扫描的 context，已完成的那几个阶段不会再发
 * 网络请求；这里拿到返回值直接覆盖任务，页面立刻停轮询，不等采集 goroutine 退出。
 * 只有发起人能停自己的任务，别人的按钮本来就不在这一屏上，后端照样再判一次。
 */
const stopping = ref(false)
const canStopScan = computed(() => scanBusy.value)

async function confirmStopScan(): Promise<void> {
  const job = activeJob.value
  if (!job) return
  try {
    await ElMessageBox.confirm(
      '确定停止这次体检？停止后本次不产出报告，已经采到的数据不会归档，可以重新发起。',
      '停止体检',
      { type: 'warning', confirmButtonText: '确定停止', cancelButtonText: '再等等' },
    )
  } catch {
    return
  }
  stopping.value = true
  try {
    const cancelled = await cancelScan(job.id)
    activeJob.value = cancelled
    stopPolling()
    clearStoredScan()
    ElMessage.success('已停止该次体检')
  } catch (error) {
    ElMessage.error(describeCancelError(error))
  } finally {
    stopping.value = false
  }
}

/**
 * 刷新或切页回来时重新挂上进行中的体检（P9.14）。
 *
 * 已经跑到终态就只把终态横幅画出来，不再续轮询；读不到（任务已被回收、或这份记录属于
 * 别人）就清掉标记，下一次挂载不再白跑一趟。
 */
async function resumeActiveScan(): Promise<void> {
  const stored = readStoredScan()
  if (!stored) return
  try {
    const job = await getScan(stored.id)
    activeJob.value = job
    activePreset.value = stored.preset
    if (isScanFinished(job)) {
      if (job.status === 'succeeded') lastScanId.value = job.id
      clearStoredScan()
      return
    }
    startPolling(job.id)
  } catch {
    clearStoredScan()
  }
}

function filterByScan(id: string): void {
  filterScanId.value = id
  page.value = 1
  void Promise.all([loadReports(), loadOverview()])
}

function clearScanFilter(): void {
  filterScanId.value = ''
  page.value = 1
  void Promise.all([loadReports(), loadOverview()])
}

onMounted(() => {
  void refreshAll()
  void resumeActiveScan()
})
onBeforeUnmount(stopPolling)
</script>

<template>
  <section class="report-page">
    <div class="page-head">
      <h2 class="title">智能体检中心</h2>
      <span class="desc"> 开始体检并查看根因分析、故障关联分析与证据；报告正文按需从对象存储取回 </span>
      <span
        v-if="canSeeStorage && health"
        class="pill"
        :class="storageTone"
        :title="health.probeError || storageLabel"
      >
        {{ storageLabel }}
      </span>
      <div class="head-actions">
        <el-button
          v-if="canScan"
          type="primary"
          :icon="VideoPlay"
          :disabled="scanBusy"
          :loading="scanBusy"
          @click="openScanDialog"
        >
          开始体检
        </el-button>
        <el-button :icon="Refresh" :loading="loading" @click="refreshAll">刷新</el-button>
      </div>
    </div>

    <p v-if="mismatchText" class="mismatch">{{ mismatchText }}，请执行 k8s-ai blobs migrate</p>

    <div v-if="activeJob" class="scan-banner" :class="activeTone">
      <div class="scan-head">
        <span class="strong ellipsis">{{ activeScanTitle }}</span>
        <span v-if="activeStages.length" class="stage-count mono faint">
          {{ stageDoneCount }} / {{ activeStages.length }} 阶段
        </span>
        <span class="scan-detail faint ellipsis">{{ activeScanDetail }}</span>
        <el-button
          v-if="canStopScan"
          size="small"
          type="danger"
          plain
          :icon="CircleClose"
          :loading="stopping"
          @click="confirmStopScan"
        >
          停止体检
        </el-button>
        <el-button
          v-if="lastScanId"
          link
          size="small"
          :type="filterScanId === lastScanId ? 'success' : 'primary'"
          @click="filterScanId === lastScanId ? clearScanFilter() : filterByScan(lastScanId)"
        >
          {{ filterScanId === lastScanId ? '显示全部报告' : '只看本次产物' }}
        </el-button>
        <el-button class="banner-close" link size="small" @click="dismissScanBanner"
          >关闭</el-button
        >
      </div>

      <!-- 阶段账来自后端同一份事实：报告「诊断信息」区块写的就是这些阶段，屏幕和报告对得上。 -->
      <ol v-if="activeStages.length" class="stage-list">
        <li v-for="stage in activeStages" :key="stage.name" class="stage" :class="stage.status">
          <span class="stage-dot" :class="stageTone(stage.status)" />
          <span class="stage-title ellipsis">{{ stage.title }}</span>
          <span class="pill" :class="stageTone(stage.status)">
            {{ STAGE_STATUS_LABELS[stage.status] ?? stage.status }}
          </span>
          <span v-if="stage.progress" class="mono faint">{{ stage.progress }}</span>
          <span v-if="stage.elapsed" class="mono faint">{{ stage.elapsed }}</span>
          <span v-if="stage.detail" class="stage-detail faint ellipsis" :title="stage.detail">
            {{ stage.detail }}
          </span>
        </li>
      </ol>
      <p v-else-if="scanBusy" class="stage-waiting faint">任务已排队，等待调度…</p>
    </div>

    <!-- 总览统计的是「你看得见的全部报告」，翻页不会改变它；类别口径与下方筛选完全同源。 -->
    <div class="stat-grid">
      <div class="card stat">
        <div class="l">可见报告</div>
        <div class="v mono">{{ overview ? overview.total : total }}</div>
        <span v-if="overview && overview.total > overview.available" class="u">
          {{ overview.total - overview.available }} 份内容已不可恢复
        </span>
      </div>
      <div class="card stat">
        <div class="l">集群体检</div>
        <div class="v mono">{{ overview ? overview.cluster : '—' }}</div>
        <span class="u">全集群范围</span>
      </div>
      <div class="card stat">
        <div class="l">业务体检</div>
        <div class="v mono">{{ overview ? overview.business : '—' }}</div>
        <span class="u">命名空间 / 单 Pod</span>
      </div>
      <div class="card stat">
        <div class="l">范围未记录</div>
        <div class="v mono">{{ overview ? overview.other : '—' }}</div>
        <span class="u">早期存量与命令行产物</span>
      </div>
      <div class="card stat">
        <div class="l">归档对象</div>
        <div class="v mono">{{ health ? health.objects : '—' }}</div>
        <span class="u">{{
          health ? `${formatSize(health.bytes)} 合计` : '需 settings.read 权限'
        }}</span>
      </div>
    </div>

    <div class="card list-card">
      <div class="row list-head">
        <span class="strong">报告列表</span>
        <el-checkbox
          v-if="reports.length"
          class="select-all"
          :model-value="allSelected"
          @change="toggleAll(Boolean($event))"
        >
          全选
        </el-checkbox>
        <span v-if="filterScanId" class="pill info">
          仅本次体检产物
          <button class="pill-x" type="button" title="取消筛选" @click="clearScanFilter">×</button>
        </span>
        <div class="filters">
          <el-select
            v-model="filterCategory"
            size="small"
            class="filter"
            placeholder="全部类别"
            @change="applyFilters"
          >
            <el-option label="全部类别" value="" />
            <el-option label="集群体检" value="cluster" />
            <el-option label="业务体检" value="business" />
            <el-option label="范围未记录" value="other" />
          </el-select>
          <el-select
            v-model="filterKind"
            size="small"
            class="filter"
            placeholder="全部格式"
            @change="applyFilters"
          >
            <el-option label="全部格式" value="" />
            <el-option label="JSON" value="latest-json" />
            <el-option label="Markdown" value="latest-md" />
          </el-select>
          <el-select
            v-model="filterCluster"
            size="small"
            class="filter"
            filterable
            placeholder="全部集群"
            @change="applyFilters"
          >
            <el-option label="全部集群" value="" />
            <el-option
              v-for="item in clusters"
              :key="item.id"
              :label="item.name || item.id"
              :value="item.id"
            />
          </el-select>
          <el-button size="small" :icon="Search" @click="applyFilters">查询</el-button>
          <el-button
            size="small"
            type="danger"
            plain
            :icon="Delete"
            :disabled="!selected.length"
            :loading="cleaning"
            @click="confirmCleanupSelected"
          >
            {{ selected.length ? `清理选中 (${selected.length})` : '清理选中' }}
          </el-button>
        </div>
      </div>

      <div v-if="reports.length" class="report-list">
        <div v-for="row in reports" :key="row.id" class="report-item">
          <el-checkbox
            class="row-check"
            :model-value="selected.includes(row.id)"
            :disabled="cleaning"
            @change="toggleRow(row.id, Boolean($event))"
          />
          <div class="grow">
            <div class="row">
              <span class="t ellipsis">{{ kindLabel(row.kind) }}</span>
              <span class="mono faint">{{ artifactLabel(row.kind) }}</span>
              <span class="pill ellipsis" :class="scopeTone(row)">{{
                scopeCategoryLabel(row)
              }}</span>
              <span v-if="scopeDetailLabel(row)" class="pill mute ellipsis">{{
                scopeDetailLabel(row)
              }}</span>
              <span v-if="row.clusterId" class="pill mute ellipsis">
                {{ clusterName[row.clusterId] || row.clusterId }}
              </span>
              <span class="pill" :class="ownerTone(row)" :title="ownerSuffix(row)">
                {{ ownerLabel(row) }}
              </span>
              <span v-if="!row.available" class="pill err">内容已不可恢复</span>
            </div>
            <div class="meta faint">
              <span>{{ formatSize(row.size) }}</span>
              <span>·</span>
              <span>{{ row.findings }} 条 Findings</span>
              <span>·</span>
              <span>{{ formatTime(row.createdAt) }}</span>
              <span>·</span>
              <span class="mono" :title="row.sha256">sha256 {{ row.sha256.slice(0, 12) }}</span>
            </div>
          </div>
          <div class="row item-actions">
            <el-button
              size="small"
              :icon="View"
              :disabled="!row.available"
              @click="openDetail(row)"
            >
              详情
            </el-button>
            <el-button
              v-if="canPresign && row.available"
              size="small"
              :icon="Link"
              :loading="downloading === row.id"
              @click="handlePresign(row)"
            >
              直连链接
            </el-button>
            <el-button
              v-if="row.kind === 'latest-json' && row.available"
              size="small"
              :icon="Document"
              :loading="downloading === row.id"
              @click="handleExportMarkdown(row)"
            >
              导出 Markdown
            </el-button>
            <el-button
              size="small"
              type="primary"
              :icon="Connection"
              :disabled="!row.available"
              :loading="downloading === row.id"
              @click="handleDownload(row)"
            >
              下载
            </el-button>
            <el-button
              size="small"
              :icon="Delete"
              :disabled="cleaning"
              @click="confirmCleanupRow(row)"
            >
              清理
            </el-button>
          </div>
        </div>
      </div>

      <TechEmptyState v-else-if="!loading" title="暂无可见报告" :hint="emptyHint" />

      <div class="pager">
        <el-pagination
          layout="prev, pager, next"
          size="small"
          :current-page="page"
          :page-size="pageSize"
          :total="total"
          background
          @current-change="changePage"
        />
      </div>
    </div>

    <el-dialog
      :model-value="presignOpen"
      title="直连下载链接"
      width="560px"
      @update:model-value="closePresign"
    >
      <p class="dialog-hint">
        链接内含签名，等同于临时凭据：不要粘贴到群聊、工单或截图，过期后重新生成。
      </p>
      <p v-if="presignTarget" class="dialog-target mono">
        {{ kindLabel(presignTarget.kind) }} · {{ formatSize(presignTarget.size) }}
      </p>
      <el-input :model-value="presignUrl" readonly type="textarea" :rows="3" />
      <template #footer>
        <el-button @click="closePresign">关闭并丢弃</el-button>
        <el-button type="primary" @click="copyPresign">复制</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="scanOpen" title="开始体检" width="540px" top="6vh">
      <div class="scan-form">
        <div class="field">
          <span class="field-label">体检维度</span>
          <el-radio-group v-model="scanScope">
            <el-radio-button value="namespace">命名空间</el-radio-button>
            <el-radio-button value="pod">单 Pod</el-radio-button>
            <el-radio-button v-if="canClusterScope" value="cluster">全集群</el-radio-button>
          </el-radio-group>
        </div>

        <div v-if="scanScope !== 'cluster'" class="field">
          <span class="field-label">命名空间</span>
          <el-select
            v-model="scanNamespace"
            class="field-control"
            filterable
            allow-create
            default-first-option
            placeholder="选择已授权的命名空间，也可直接输入"
          >
            <el-option v-for="ns in namespaceOptions" :key="ns" :label="ns" :value="ns" />
          </el-select>
        </div>

        <div v-if="scanScope === 'pod'" class="field">
          <span class="field-label">Pod 名称</span>
          <el-input v-model="scanPod" placeholder="例如 k8s-ai-server-846ffd68bf-gd5dr" />
        </div>

        <div class="field">
          <span class="field-label">报告格式</span>
          <el-radio-group v-model="scanFormat" class="preset-group">
            <el-radio v-for="item in SCAN_FORMATS" :key="item.value" :value="item.value">
              {{ item.label }}
              <span class="preset-hint">{{ item.hint }}</span>
            </el-radio>
          </el-radio-group>
        </div>

        <div class="field">
          <span class="field-label">体检档位</span>
          <el-radio-group v-model="scanPreset" class="preset-group">
            <el-radio v-for="item in SCAN_PRESETS" :key="item.value" :value="item.value">
              {{ item.label }}
              <span class="preset-hint">{{ item.hint }}</span>
            </el-radio>
          </el-radio-group>
        </div>

        <p class="scan-note">
          {{
            canClusterScope
              ? '体检按你的授权范围执行，同一时间平台只跑一个任务；产物连同维度一起归档到报告列表。'
              : '集群体检会读取所有命名空间，只有管理员能发起。超出你授权范围的命名空间会被直接拒绝，不会改扫别的。'
          }}
          一次体检只归档上面选中的那一种格式，另一种可在报告列表点「导出 Markdown」当场生成。
        </p>
      </div>
      <template #footer>
        <el-button @click="scanOpen = false">取消</el-button>
        <el-button type="primary" :loading="scanSubmitting" @click="submitScan">开始体检</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.report-page {
  display: flex;
  flex-direction: column;
  gap: 14px;
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
  margin: 0;
  font-size: 16px;
}

.page-head .desc {
  font-size: 12.5px;
  color: var(--app-muted);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.head-actions {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.scan-banner {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 8px;
  padding: 10px 14px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  font-size: 12.5px;
  min-width: 0;
}

.scan-head {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.scan-banner.ok {
  border-color: var(--app-success);
}

.scan-banner.err {
  border-color: var(--app-danger);
}

.scan-banner.warn {
  border-color: var(--app-warning);
}

.scan-detail {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.banner-close {
  margin-left: auto;
  flex-shrink: 0;
}

.stage-count {
  flex-shrink: 0;
  color: var(--app-muted);
}

/* 阶段账按后端固定顺序逐行铺开：一行一阶段，跑没跑到、跑了多久一眼能扫完。 */
.stage-list {
  list-style: none;
  margin: 0;
  padding: 0 0 2px;
  display: flex;
  flex-direction: column;
  gap: 3px;
  border-top: 1px solid rgba(255, 255, 255, 0.05);
  padding-top: 8px;
}

.stage {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  min-width: 0;
  color: var(--app-muted);
}

.stage.pending {
  opacity: 0.55;
}

.stage-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--app-faint);
}

.stage-dot.ok {
  background: var(--app-success);
}

.stage-dot.err {
  background: var(--app-danger);
}

.stage-dot.info {
  background: var(--app-info);
}

.stage-title {
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  max-width: 46%;
}

/* 状态徽标和耗时是定宽的：该被截的只有标题与详情两句人话。 */
.stage .pill,
.stage .mono {
  flex-shrink: 0;
}

.stage.running .stage-title {
  color: var(--app-info);
}

.stage-detail {
  min-width: 0;
  flex: 1;
}

.stage-waiting {
  margin: 0;
  font-size: 12px;
}

.mismatch {
  margin: 0;
  font-size: 12.5px;
  color: var(--app-warning);
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}

.card {
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
}

.stat {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 14px 16px;
  min-width: 0;
}

.stat .l {
  font-size: 12px;
  color: var(--app-muted);
}

.stat .v {
  font-size: 18px;
  font-weight: 650;
  color: var(--app-strong);
}

.stat .u {
  font-size: 11.5px;
  color: var(--app-faint);
}

.list-card {
  padding: 14px 16px;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.list-head {
  padding-bottom: 10px;
  border-bottom: 1px solid var(--app-border);
  flex-wrap: wrap;
}

.filters {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
}

.filter {
  width: 140px;
}

.strong {
  font-weight: 650;
  color: var(--app-text);
}

.pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 20px;
  padding: 0 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
  max-width: 100%;
}

.pill.ok {
  color: var(--app-success);
  background: var(--app-success-soft);
}

.pill.warn {
  color: var(--app-warning);
  background: var(--app-warning-soft);
}

.pill.err {
  color: var(--app-danger);
  background: var(--app-danger-soft);
}

.pill.info {
  color: var(--app-info);
  background: var(--app-info-soft);
}

.pill.mute {
  color: var(--app-muted);
  background: var(--app-surface-2);
}

.report-list {
  display: flex;
  flex-direction: column;
}

.report-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 2px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
  min-width: 0;
}

.report-item:last-child {
  border-bottom: none;
}

.report-item .t {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}

.meta {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  margin-top: 4px;
  min-width: 0;
  flex-wrap: wrap;
}

.item-actions {
  flex-shrink: 0;
}

.pager {
  display: flex;
  justify-content: flex-end;
  padding-top: 12px;
}

.mono {
  font-family: var(--app-mono);
  font-size: 11.5px;
}

.faint {
  color: var(--app-faint);
}

.ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.grow {
  flex: 1;
  min-width: 0;
}

.dialog-hint {
  margin: 0 0 8px;
  font-size: 12.5px;
  color: var(--app-warning);
}

.dialog-target {
  margin: 0 0 8px;
  color: var(--app-muted);
}

.select-all {
  margin-left: 2px;
}

.row-check {
  flex-shrink: 0;
}

.pill-x {
  border: none;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  padding: 0 0 0 3px;
}

/* 弹窗自己滚：footer 常驻，否则笔记本/半屏高度下「开始体检」按钮根本点不到。 */
.scan-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
  max-height: min(52vh, 540px);
  overflow-y: auto;
  padding-right: 6px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.field-label {
  font-size: 12px;
  color: var(--app-muted);
}

.field-control {
  width: 100%;
}

.preset-group {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
}

.preset-group :deep(.el-radio) {
  height: auto;
  margin-right: 0;
  align-items: flex-start;
  white-space: normal;
}

.preset-hint {
  display: block;
  font-size: 11.5px;
  font-weight: 400;
  color: var(--app-faint);
}

.scan-note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--app-muted);
}

@media (max-width: 900px) {
  .filters {
    margin-left: 0;
    width: 100%;
  }

  .head-actions {
    margin-left: 0;
    width: 100%;
  }

  .scan-banner {
    flex-wrap: wrap;
  }

  .report-item {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
