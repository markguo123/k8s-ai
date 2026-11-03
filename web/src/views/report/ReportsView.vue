<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Connection, Link, Refresh, Search } from '@element-plus/icons-vue'

import { listClusters } from '@/api/clusters'
import {
  describeReportError,
  downloadReport,
  getReportDownloadUrl,
  getStorageHealth,
  listReports,
} from '@/api/reports'
import type { Cluster, ReportView, StorageHealth } from '@/api/types'
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

const filterKind = ref('')
const filterCluster = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const totalPages = ref(0)

/** 存储健康是设置域信息，没有 settings.read 的人不查也不显示（后端同样按权限拦）。 */
const canSeeStorage = computed(() => hasPermission(permissionsOf(auth.role), PERM.settingsRead))

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
    })
    reports.value = result.items
    total.value = result.total
    totalPages.value = result.totalPages
  } catch (error) {
    ElMessage.error(describeReportError('加载报告列表', error))
    reports.value = []
    total.value = 0
    totalPages.value = 0
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

onMounted(refreshAll)
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
      <el-button class="head-action" :icon="Refresh" :loading="loading" @click="refreshAll">
        刷新
      </el-button>
    </div>

    <p v-if="mismatchText" class="mismatch">{{ mismatchText }}，请执行 k8s-ai blobs migrate</p>

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
        </div>
      </div>

      <div v-if="reports.length" class="report-list">
        <div v-for="row in reports" :key="row.id" class="report-item">
          <div class="grow">
            <div class="row">
              <span class="t ellipsis">{{ kindLabel(row.kind) }}</span>
              <span class="mono faint">{{ row.kind }}</span>
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
          </div>
        </div>
      </div>

      <TechEmptyState
        v-else-if="!loading"
        title="暂无可见报告"
        hint="发起一次集群扫描即可生成报告；他人的报告需要你拥有对应集群的授权。"
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

.head-action {
  margin-left: auto;
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

@media (max-width: 900px) {
  .filters {
    margin-left: 0;
    width: 100%;
  }

  .report-item {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
