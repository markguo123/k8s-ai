/**
 * P3.2/P3.3 集群管理 API 客户端（与后端 `internal/server/cluster.go` 一致）。
 *
 * 安全约定：kubeconfig 只在 create/test 请求体的内存中出现一次，
 * 客户端本地不缓存明文；列表 / 状态响应用受管元数据（store.Cluster）建模。
 */
import { ApiRequestError, apiDelete, apiGet, apiPost } from './request'
import type {
  Cluster,
  ClusterCreateRequest,
  ClusterInfo,
  ClusterStatusView,
  ClusterTestRequest,
} from './types'

/** 受管集群列表（不含凭据）。 */
export async function listClusters(): Promise<Cluster[]> {
  const env = await apiGet<Cluster[]>('/clusters')
  return env.data ?? []
}

/** 当前集群实时状态（版本 / Context / 健康/断连）。 */
export async function currentCluster(): Promise<ClusterStatusView> {
  const env = await apiGet<ClusterStatusView>('/clusters/current')
  if (!env.data) {
    throw new Error('当前集群状态为空')
  }
  return env.data
}

/** 仅连测，不落库、不保留明文。 */
export async function testCluster(body: ClusterTestRequest): Promise<ClusterInfo> {
  const env = await apiPost<ClusterInfo>('/clusters/test', body)
  if (!env.data) {
    throw new Error('连接测试结果为空')
  }
  return env.data
}

/** 上传 / 粘贴 kubeconfig 并加密落库。 */
export async function createCluster(body: ClusterCreateRequest): Promise<Cluster> {
  const env = await apiPost<Cluster>('/clusters', body)
  if (!env.data) {
    throw new Error('创建集群结果为空')
  }
  return env.data
}

/** 删除受管集群与其加密凭据。 */
export async function deleteCluster(id: string): Promise<void> {
  const env = await apiDelete<Record<string, string>>(`/clusters/${encodeURIComponent(id)}`)
  if (env.error) {
    throw new Error(env.error.message)
  }
}

/** 统一错误文案，供页面直接展示（不透出请求体内容）。 */
export function describeClusterError(method: string, error: unknown): string {
  if (error instanceof ApiRequestError && error.status === 501) {
    return '集群管理能力未启用，请联系平台管理员配置凭据加密密钥后重试'
  }
  if (error instanceof Error && error.message) {
    return error.message
  }
  return `${method}失败，请稍后重试`
}

// 保持 DefaultExport 便于 tree-shaking 之外的使用方统一 import 默认值。
export default { listClusters, currentCluster, testCluster, createCluster, deleteCluster }
