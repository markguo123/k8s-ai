<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Connection, Delete, Link, Refresh, Search, VideoPlay } from '@element-plus/icons-vue'

import { myScope } from '@/api/auth'
import { listClusters } from '@/api/clusters'
import {
  cleanupReports,
  deleteReport,
  describeReportError,
  downloadReport,
  getReportDownloadUrl,
  getStorageHealth,
  listReports,
} from '@/api/reports'
import {
  SCAN_PRESETS,
  SCAN_SCOPE_LABELS,
  createScan,
  describeScanError,
  getScan,
  isScanFinished,
  type ScanPreset,
  type ScanScope,
} from '@/api/scans'
import type { Cluster, EffectiveScope, ReportView, ScanJob, StorageHealth } from '@/api/types'
import TechEmptyState from '@/components/TechEmptyState.vue'
import { useAuthStore } from '@/stores/auth'
import { hasPermission, PERM, permissionsOf } from '@/utils/permission'

// P8.11：报告产物列表。可见范围（本人 + 有权集群的系统报告）由后端判定，
// 这里只传筛选条件、不做任何归属推断；跨 owner 下载的审计也由后端落，前端只提示。
const auth = useAuthStore()

const reports = ref<ReportView[]>([])
const clusters = ref<Cluster[]>([])
const health = ref<StorageHealth | null>(null)
const loading = ref(false)
const downloading = ref('')
const cleaning = ref(false)

// 勾选范围只在当前可见行里保留：翻页后不再看得见的行自动退出选择，
// 免得「清理选中 12 条」里有 10 条是用户在第三页勾的。
const selected = ref<string[]>([])

// 只看某次巡检的产物：巡检完成后默认打开，也可手动取消。
const filterScanId = ref('')

const filterKind = ref('')
const filterCluster = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const totalPages = ref(0)

/** 存储健康是设置域信息，没有 settings.read 的人不查也不显示（后端同样按权限拦）。 */
const canSeeStorage = computed(() => hasPermission(permissionsOf(auth.role), PERM.settingsRead))

/** 发起入口只对持 scan.execute 的人出现；范围与归属判定仍逐次在后端做。 */
const canScan = computed(() => hasPermission(permissionsOf(auth.role), PERM.scanExecute))

/** 「全集群」的可见性与后端同一判据：角色持全域权限 `*`（不是"看起来像管理员"）。 */
const canClusterScope = computed(() => hasPermission(permissionsOf(auth.role), '*'))

const KIND_LABELS: Record<string, string> = {
  'latest-md': 'Markdown 报告',
  'latest-json': 'JSON 数据',
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

/** 系统巡检报告没有 clusterId 之外的归属人语义，空 createdBy 按系统生成展示。 */
function ownerLabel(row: ReportView): string {
  if (!row.createdBy) return '系统巡检'
  if (row.createdBy === auth.user?.userId) return '我的报告'
  return '他人报告 · 下载将记入审计'
}

function ownerTone(row: ReportView): string {
  if (!row.createdBy) return 'info'
  return row.createdBy === auth.user?.userId ? 'mute' : 'warn'
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
 * 巡检维度徽标。产物文件名里已经写了维度，列表这一行是让人不点开文件就能认出
 * 「这是扫哪个维度出来的」（方案 17 §5）；scopeKind 为空是范围留痕之前的历史行。
 */
function scopeLabel(row: ReportView): string {
  switch (row.scopeKind) {
    case 'cluster':
      return '全集群巡检'
    case 'namespace':
      return `命名空间巡检 · ${row.scopeValue || '—'}`
    case 'pod':
      return `Pod 巡检 · ${row.scopeValue || '—'}`
    default:
      return '范围未记录'
  }
}

function scopeTone(row: ReportView): string {
  return SCOPE_TONES[row.scopeKind ?? ''] ?? 'mute'
}

function scopeTextOf(kind?: string, value?: string): string {
  if (!kind) return '巡检'
  const label = SCAN_SCOPE_LABELS[kind as ScanScope] ?? kind
  return value ? `${label} ${value}` : label
}

/** 空态文案跟着权限走：没有发起入口的人，不该被指着点一个不存在的按钮。 */
const emptyHint = computed(() =>
  canScan.value
    ? '点击右上角「发起巡检」生成报告；他人的报告需要你拥有对应集群的授权。'
    : '报告由具备巡检权限的成员生成；他人的报告需要你拥有对应集群的授权。',
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
      `确定清理「${scopeLabel(row)} · ${kindLabel(row.kind)}」？${CLEANUP_WARNING}`,
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
      clusterId: filterCluster.value,
      scanId: filterScanId.value,
    })
    reports.value = result.items
    total.value = result.total
    totalPages.value = result.totalPages
    // 只保留还在这一页里的勾选：跨页留着看不见的勾选，批量确认时的数字就骗人了。
    const visible = new Set(result.items.map((row) => row.id))
    selected.value = selected.value.filter((id) => visible.has(id))
  } catch (error) {
    ElMessage.error(describeReportError('加载报告列表', error))
    reports.value = []
    total.value = 0
    totalPages.value = 0
    selected.value = []
  } finally {
    loading.value = false
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
  await Promise.all([loadReports(), loadSidePanels()])
}

function applyFilters(): void {
  page.value = 1
  void loadReports()
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

// ------------------------------------------------------- 发起巡检（P8.13）

const scanOpen = ref(false)
const scanSubmitting = ref(false)
const scanScope = ref<ScanScope>('namespace')
const scanNamespace = ref('')
const scanPod = ref('')
const scanPreset = ref<ScanPreset>('standard')

/** 自己的授权范围只用来填下拉候选；真正的判定在后端，拉不到也不该拦住发起。 */
const scopeInfo = ref<EffectiveScope | null>(null)
const namespaceOptions = computed(() => scopeInfo.value?.namespaces ?? [])

const activeJob = ref<ScanJob | null>(null)
const activePreset = ref('')
/** 完成后留下的指针：让「只看本次产物」在横幅关掉之后仍然点得到。 */
const lastScanId = ref('')
let pollTimer: number | undefined

const scanBusy = computed(() => activeJob.value !== null && !isScanFinished(activeJob.value))

const SCAN_STATUS_LABELS: Record<string, string> = {
  pending: '排队中',
  running: '执行中',
  succeeded: '已完成',
  failed: '失败',
}

const activeTone = computed(() => {
  const status = activeJob.value?.status
  if (status === 'failed') return 'err'
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
  if (job.status === 'succeeded') {
    const findings = job.result?.findings?.length ?? 0
    const score = job.result?.healthScore
    const scoreText =
      typeof score?.score === 'number' && typeof score?.max === 'number'
        ? ` · 健康度 ${score.score}/${score.max}`
        : ''
    return `发现 ${findings} 条问题${scoreText}，产物已归档到报告列表`
  }
  return '巡检正在执行，完成后产物会自动出现在下面的列表里'
})

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
    ElMessage.warning('请选择要巡检的命名空间')
    return
  }
  if (scanScope.value === 'pod' && !pod) {
    ElMessage.warning('单 Pod 巡检还需要填 Pod 名称')
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
      })
      activePreset.value = scanPreset.value
      activeJob.value = job
      scanOpen.value = false
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
      if (job.status === 'failed') {
        ElMessage.error(`巡检失败：${job.error || '未知原因'}`)
        return
      }
      lastScanId.value = job.id
      filterScanId.value = job.id
      ElMessage.success(`巡检完成，发现 ${job.result?.findings?.length ?? 0} 条问题`)
      await loadReports()
    } catch (error) {
      stopPolling()
      ElMessage.error(describeScanError(error))
    }
  }, 3000)
}

function dismissScanBanner(): void {
  stopPolling()
  activeJob.value = null
}

function filterByScan(id: string): void {
  filterScanId.value = id
  page.value = 1
  void loadReports()
}

function clearScanFilter(): void {
  filterScanId.value = ''
  page.value = 1
  void loadReports()
}

onMounted(refreshAll)
onBeforeUnmount(stopPolling)
</script>

<template>
  <section class="report-page">
    <div class="page-head">
      <h2 class="title">报告</h2>
      <span class="desc">
        扫描产物只读归档；列表仅含元数据，正文按需从对象存储取回
      </span>
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
          发起巡检
        </el-button>
        <el-button :icon="Refresh" :loading="loading" @click="refreshAll">刷新</el-button>
      </div>
    </div>

    <p v-if="mismatchText" class="mismatch">{{ mismatchText }}，请执行 k8s-ai blobs migrate</p>

    <div v-if="activeJob" class="scan-banner" :class="activeTone">
      <span class="strong ellipsis">{{ activeScanTitle }}</span>
      <span class="scan-detail faint ellipsis">{{ activeScanDetail }}</span>
      <el-button
        v-if="lastScanId"
        link
        size="small"
        :type="filterScanId === lastScanId ? 'success' : 'primary'"
        @click="filterScanId === lastScanId ? clearScanFilter() : filterByScan(lastScanId)"
      >
        {{ filterScanId === lastScanId ? '显示全部报告' : '只看本次产物' }}
      </el-button>
      <el-button class="banner-close" link size="small" @click="dismissScanBanner">关闭</el-button>
    </div>

    <div class="stat-grid">
      <div class="card stat">
        <div class="l">可见报告</div>
        <div class="v mono">{{ total }}</div>
      </div>
      <div class="card stat">
        <div class="l">归档对象</div>
        <div class="v mono">{{ health ? health.objects : '—' }}</div>
        <span class="u">{{ health ? `${formatSize(health.bytes)} 合计` : '需 settings.read 权限' }}</span>
      </div>
      <div class="card stat">
        <div class="l">当前页</div>
        <div class="v mono">{{ page }} / {{ Math.max(totalPages, 1) }}</div>
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
          仅本次巡检产物
          <button class="pill-x" type="button" title="取消筛选" @click="clearScanFilter">×</button>
        </span>
        <div class="filters">
          <el-select
            v-model="filterKind"
            size="small"
            class="filter"
            placeholder="全部类型"
            @change="applyFilters"
          >
            <el-option label="全部类型" value="" />
            <el-option label="Markdown 报告" value="latest-md" />
            <el-option label="JSON 数据" value="latest-json" />
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
              <span class="mono faint">{{ row.kind }}</span>
              <span class="pill ellipsis" :class="scopeTone(row)">{{ scopeLabel(row) }}</span>
              <span class="pill mute ellipsis">
                {{ row.clusterId ? clusterName[row.clusterId] || row.clusterId : '系统巡检' }}
              </span>
              <span class="pill" :class="ownerTone(row)">{{ ownerLabel(row) }}</span>
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
              v-if="canPresign && row.available"
              size="small"
              :icon="Link"
              :loading="downloading === row.id"
              @click="handlePresign(row)"
            >
              直连链接
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

      <TechEmptyState
        v-else-if="!loading"
        title="暂无可见报告"
        :hint="emptyHint"
      />

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

    <el-dialog v-model="scanOpen" title="发起巡检" width="540px">
      <div class="scan-form">
        <div class="field">
          <span class="field-label">巡检维度</span>
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
          <span class="field-label">巡检档位</span>
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
              ? '巡检按你的授权范围执行，同一时间平台只跑一个任务；产物连同维度一起归档到报告列表。'
              : '全集群巡检会读取所有命名空间，只有管理员能发起。超出你授权范围的命名空间会被直接拒绝，不会改扫别的。'
          }}
        </p>
      </div>
      <template #footer>
        <el-button @click="scanOpen = false">取消</el-button>
        <el-button type="primary" :loading="scanSubmitting" @click="submitScan">开始巡检</el-button>
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
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  font-size: 12.5px;
  min-width: 0;
}

.scan-banner.ok {
  border-color: var(--app-success);
}

.scan-banner.err {
  border-color: var(--app-danger);
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

.scan-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
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
