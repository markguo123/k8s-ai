<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Link } from '@element-plus/icons-vue'

import {
  createCluster,
  currentCluster,
  deleteCluster,
  describeClusterError,
  listClusters,
  testCluster,
} from '@/api/clusters'
import type { Cluster, ClusterInfo, ClusterStatusView } from '@/api/types'

// P3.4：单集群接入，UI 不做多集群（D-02）。"当前集群"= 最近创建的受管集群。
const clusters = ref<Cluster[]>([])
const current = ref<ClusterStatusView | null>(null)
const loading = ref(false)
const testing = ref(false)
const creating = ref(false)
const showConnect = ref(false)

const name = ref('')
const kubeconfigText = ref('')
const testResult = ref<ClusterInfo | null>(null)
const testError = ref('')

const canManage = computed(() => true)

function statusType(status?: string): 'success' | 'danger' | 'info' | 'warning' {
  switch (status) {
    case 'connected':
      return 'success'
    case 'disconnected':
      return 'warning'
    case 'error':
      return 'danger'
    default:
      return 'info'
  }
}

function statusText(status?: string): string {
  switch (status) {
    case 'connected':
      return '已连接'
    case 'disconnected':
      return '已断连'
    case 'error':
      return '异常'
    default:
      return '未知'
  }
}

async function loadAll(): Promise<void> {
  loading.value = true
  try {
    clusters.value = await listClusters()
  } catch (error) {
    ElMessage.error(describeClusterError('加载集群', error))
  }
  try {
    current.value = await currentCluster()
  } catch {
    // 尚未接入任何集群：current 保持 null，页面展示"未接入"。
    current.value = null
  } finally {
    loading.value = false
  }
}

/** 仅连测，不落库：结果只展示版本 / Context / 健康，不回显 kubeconfig。 */
async function handleTest(): Promise<void> {
  const kubeconfig = kubeconfigText.value.trim()
  if (!kubeconfig) {
    ElMessage.warning('请先粘贴或上传 kubeconfig')
    return
  }
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    testResult.value = await testCluster({ kubeconfig })
    ElMessage.success('连接测试通过')
  } catch (error) {
    testError.value = describeClusterError('连接测试', error)
    ElMessage.error(testError.value)
  } finally {
    testing.value = false
  }
}

/** 保存连接：kubeconfig 仅随请求体一次性上传，本地与页面均不保留明文。 */
async function handleCreate(): Promise<void> {
  const kubeconfig = kubeconfigText.value.trim()
  if (!kubeconfig) {
    ElMessage.warning('请先粘贴或上传 kubeconfig')
    return
  }
  creating.value = true
  try {
    await createCluster({ name: name.value.trim() || undefined, kubeconfig })
    kubeconfigText.value = ''
    name.value = ''
    testResult.value = null
    testError.value = ''
    showConnect.value = false
    ElMessage.success('集群已连接，凭据已加密存储')
    await loadAll()
  } catch (error) {
    ElMessage.error(describeClusterError('保存集群', error))
  } finally {
    creating.value = false
  }
}

async function handleDelete(target: Cluster): Promise<void> {
  try {
    await ElMessageBox.confirm(`确定删除集群「${target.name || target.id}」及其加密凭据？`, '删除集群', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    return
  }
  try {
    await deleteCluster(target.id)
    ElMessage.success('集群已删除')
    await loadAll()
  } catch (error) {
    ElMessage.error(describeClusterError('删除集群', error))
  }
}

/** 读取本地文件内容到粘贴框（不落盘、不进日志）。 */
function onFileSelected(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) {
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    kubeconfigText.value = String(reader.result ?? '')
    testError.value = ''
  }
  reader.onerror = () => {
    ElMessage.error('读取 kubeconfig 文件失败')
  }
  reader.readAsText(file)
  input.value = ''
}

onMounted(loadAll)
</script>

<template>
  <section class="cluster-page">
    <div class="page-head">
      <h2 class="title">集群</h2>
      <span class="desc">
        {{ current ? `${current.cluster.name} · ${current.cluster.context || '无 Context'}` : '未接入集群' }}
        · 凭据加密存储，页面不展示明文
      </span>
      <el-button
        v-if="canManage"
        type="primary"
        class="head-action"
        :icon="Link"
        @click="showConnect = !showConnect"
      >
        {{ showConnect ? '收起' : '＋ 连接集群' }}
      </el-button>
    </div>

    <div class="stat-grid">
      <div class="card stat">
        <div class="l">当前集群</div>
        <div class="v ellipsis">{{ current?.cluster.name || '—' }}</div>
      </div>
      <div class="card stat">
        <div class="l">版本</div>
        <div class="v mono">{{ current?.info.version || current?.cluster.version || '—' }}</div>
      </div>
      <div class="card stat">
        <div class="l">上下文</div>
        <div class="v ellipsis">{{ current?.cluster.context || '—' }}</div>
        <span class="u ellipsis">{{ current?.cluster.server || '' }}</span>
      </div>
      <div class="card stat">
        <div class="l">健康度</div>
        <div class="v">
          <span class="pill" :class="statusType(current?.info.health ? 'connected' : current?.cluster.status)">
            {{ current?.info.health === 'ok' ? 'Healthy' : statusText(current?.cluster.status) }}
          </span>
        </div>
      </div>
    </div>

    <div class="two-col">
      <div class="card list-card">
        <div class="row list-head">
          <span class="sub strong">受管集群</span>
          <span class="pill mute">{{ clusters.length }} 个</span>
          <el-button v-if="clusters.length" size="small" text class="refresh" @click="loadAll">
            刷新
          </el-button>
        </div>
        <div v-if="loading" class="muted center-hint">加载中…</div>
        <div v-else-if="clusters.length === 0" class="muted center-hint">
          暂无受管集群，点击右上角「连接集群」接入。
        </div>
        <div v-else class="cluster-list">
          <div v-for="cluster in clusters" :key="cluster.id" class="list-item">
            <div class="grow">
              <div class="row">
                <span class="t">{{ cluster.name }}</span>
                <span class="pill" :class="statusType(cluster.status)">{{ statusText(cluster.status) }}</span>
              </div>
              <div class="muted meta">
                <span class="mono">{{ cluster.context || '—' }}</span>
                <span class="faint">·</span>
                <span class="mono ellipsis">{{ cluster.server || '—' }}</span>
              </div>
            </div>
            <span v-if="cluster.version" class="pill info mono">{{ cluster.version }}</span>
            <el-button
              v-if="canManage"
              size="small"
              text
              type="danger"
              :icon="Delete"
              class="del"
              @click="handleDelete(cluster)"
            >
              删除
            </el-button>
          </div>
        </div>
      </div>

      <div v-if="showConnect" class="card connect-card">
        <div class="sub strong" style="margin-bottom: 10px">连接 kubeconfig</div>
        <label class="dropzone">
          <div class="big">⬆</div>
          拖拽或点击选择 kubeconfig
          <input type="file" accept=".yaml,.yml,.kubeconfig,text/*" class="file-input" @change="onFileSelected" />
          <span class="faint">文件内容仅加密后存储在服务器，页面不展示明文</span>
        </label>
        <el-input
          v-model="name"
          class="field"
          placeholder="集群名称（可选，默认取 Context）"
          clearable
        />
        <el-input
          v-model="kubeconfigText"
          class="field"
          type="textarea"
          :rows="8"
          placeholder="在此粘贴 kubeconfig 内容"
          spellcheck="false"
        />
        <div v-if="testResult" class="row result">
          <span class="pill ok">校验通过</span>
          <span class="mono muted ellipsis">{{ testResult.version || testResult.server || '已连接' }}</span>
        </div>
        <p v-if="testError" class="result-error">
          {{ testError }}
        </p>
        <div class="row actions">
          <el-button :loading="testing" @click="handleTest">连接测试</el-button>
          <el-button type="primary" :loading="creating" :disabled="!kubeconfigText.trim()" @click="handleCreate">
            保存连接
          </el-button>
        </div>
      </div>
      <div v-else class="card connect-card muted center-hint">
        连接 kubeconfig 以接入集群；凭据将加密存储并用于只读连测。
      </div>
    </div>
  </section>
</template>

<style scoped>
.cluster-page {
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

.two-col {
  display: grid;
  grid-template-columns: 1.4fr 1fr;
  gap: 14px;
  align-items: stretch;
}

.list-card,
.connect-card {
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
}

.refresh {
  margin-left: auto;
}

.sub {
  font-size: 12px;
  color: var(--app-muted);
  text-transform: uppercase;
  letter-spacing: 0.03em;
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

.cluster-list {
  display: flex;
  flex-direction: column;
}

.list-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 2px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.05);
  min-width: 0;
}

.list-item:last-child {
  border-bottom: none;
}

.list-item .t {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}

.meta {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11.5px;
  margin-top: 3px;
  min-width: 0;
}

.mono {
  font-family: var(--app-mono);
  font-size: 11.5px;
}

.muted {
  color: var(--app-muted);
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

.center-hint {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 120px;
  font-size: 12.5px;
  text-align: center;
  padding: 12px;
}

.dropzone {
  display: block;
  border: 1.5px dashed var(--app-border);
  border-radius: var(--app-radius);
  padding: 18px;
  text-align: center;
  color: var(--app-muted);
  font-size: 12.5px;
  cursor: pointer;
  margin-bottom: 12px;
}

.dropzone:hover {
  border-color: var(--app-accent-2);
  color: var(--app-text);
}

.dropzone .big {
  font-size: 22px;
  margin-bottom: 6px;
}

.dropzone .faint {
  display: block;
  margin-top: 6px;
  font-size: 11px;
}

.file-input {
  display: none;
}

.field {
  margin-bottom: 12px;
}

.result {
  margin-bottom: 8px;
}

.result-error {
  margin: 0 0 8px;
  font-size: 12px;
  color: var(--app-danger);
}

.actions {
  justify-content: flex-end;
  margin-top: 4px;
}

@media (max-width: 900px) {
  .two-col {
    grid-template-columns: 1fr;
  }
}
</style>
