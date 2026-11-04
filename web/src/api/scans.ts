/**
 * P8.13 页面发起巡检 API 客户端（与后端 `internal/server/scans.go` 一致）。
 *
 * 两条口径写在这里是为了让调用点不用重复推断：
 *  1. 请求体是白名单：只有 scope / namespace / pod / preset 四项，后端
 *     DisallowUnknownFields。连接参数、report.directory、超时都由服务端配置决定，
 *     前端传了就是 400，这是刻意的——否则就等于把「往哪扫、往哪写」交给浏览器。
 *  2. 范围授权与可见性判定全在后端：越权范围直接 403 并点名命名空间，绝不静默裁剪；
 *     任务查询只回本人任务与 admin 可见任务，前端隐藏按钮不是安全机制。
 */
import { ApiRequestError, apiErrorFrom, apiGet, apiPost } from './request'
import type { ScanJob } from './types'

/** 巡检维度（与后端 model.Scope* 常量同字面量）。 */
export type ScanScope = 'cluster' | 'namespace' | 'pod'

/** 巡检档位（后端锁死三档，不可自定义参数）。 */
export type ScanPreset = 'quick' | 'standard' | 'deep'

export interface ScanRequest {
  scope: ScanScope
  namespace?: string
  pod?: string
  preset?: ScanPreset
}

/** 三档预设的展示口径；行为参数由后端常量决定，这里只负责说人话。 */
export const SCAN_PRESETS: { value: ScanPreset; label: string; hint: string }[] = [
  { value: 'quick', label: '快速体检', hint: '约 1 分钟，不跑大模型，只看清单与事件' },
  { value: 'standard', label: '标准巡检', hint: '约 5 分钟，含大模型根因分析' },
  { value: 'deep', label: '深度巡检', hint: '约 10 分钟，额外采集近 1 小时日志' },
]

export const SCAN_SCOPE_LABELS: Record<ScanScope, string> = {
  cluster: '全集群',
  namespace: '命名空间',
  pod: '单 Pod',
}

/** 发起一次巡检。返回 202 与 pending 任务，结果靠 getScan 轮询。 */
export async function createScan(req: ScanRequest): Promise<ScanJob> {
  const env = await apiPost<ScanJob>('/scans', {
    scope: req.scope,
    namespace: req.namespace ?? '',
    pod: req.pod ?? '',
    preset: req.preset ?? 'standard',
  })
  if (!env.data?.id) {
    throw new Error('未返回巡检任务')
  }
  return env.data
}

/** 查询任务状态。他人任务与不存在的任务同形 404。 */
export async function getScan(id: string): Promise<ScanJob> {
  const env = await apiGet<ScanJob>(`/scans/${encodeURIComponent(id)}`)
  if (!env.data) {
    throw new Error('巡检任务不存在')
  }
  return env.data
}

/** 终态判定：只有这两种状态停止轮询。 */
export function isScanFinished(job: ScanJob): boolean {
  return job.status === 'succeeded' || job.status === 'failed'
}

/** 统一错误文案：403 要说清「没扫、也没偷偷改扫别的」，这是巡检结论可信的前提。 */
export function describeScanError(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 409) {
      return '已有一次巡检正在执行，请等它结束后再发起'
    }
    if (error.status === 403 && error.message) {
      return error.message
    }
    if (error.status === 501) {
      return '当前服务未开启巡检能力'
    }
    if (error.message) {
      return error.message
    }
  }
  if (error instanceof Error && error.message) {
    return error.message
  }
  return '发起巡检失败，请稍后重试'
}

// 保持 DefaultExport 便于使用方统一 import 默认值。
export default {
  createScan,
  getScan,
  isScanFinished,
  describeScanError,
  SCAN_PRESETS,
  SCAN_SCOPE_LABELS,
}
