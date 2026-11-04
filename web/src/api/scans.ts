/**
 * P8.13 页面发起体检 API 客户端（与后端 `internal/server/scans.go` 一致）。
 *
 * 两条口径写在这里是为了让调用点不用重复推断：
 *  1. 请求体是白名单：只有 scope / namespace / pod / preset 四项，后端
 *     DisallowUnknownFields。连接参数、report.directory、超时都由服务端配置决定，
 *     前端传了就是 400，这是刻意的——否则就等于把「往哪扫、往哪写」交给浏览器。
 *  2. 范围授权与可见性判定全在后端：越权范围直接 403 并点名命名空间，绝不静默裁剪；
 *     任务查询只回本人任务与 admin 可见任务，前端隐藏按钮不是安全机制。
 */
import { ApiRequestError, apiGet, apiPost } from './request'
import type { ScanJob } from './types'

/** 体检维度（与后端 model.Scope* 常量同字面量）。 */
export type ScanScope = 'cluster' | 'namespace' | 'pod'

/** 体检档位（后端锁死三档，不可自定义参数）。 */
export type ScanPreset = 'quick' | 'standard' | 'deep'

/**
 * 报告产物格式（P9.13）：一次体检只留用户选中的那一种。
 *
 * 后端刻意不同时归档两份：报告列表会长出两条几乎相同的行，用户反而分不清哪份是最新的。
 * 另一种格式由 GET /reports/{id}/export 在同一次渲染里当场生成，两边不会互相过期。
 */
export type ScanFormat = 'json' | 'markdown'

export interface ScanRequest {
  scope: ScanScope
  namespace?: string
  pod?: string
  preset?: ScanPreset
  /** 缺省 json：结构化正文能撑起报告详情页的结论、故障链与证据区块。 */
  format?: ScanFormat
}

/** 三档预设的展示口径；行为参数由后端常量决定，这里只负责说人话。 */
export const SCAN_PRESETS: { value: ScanPreset; label: string; hint: string }[] = [
  { value: 'quick', label: '快速体检', hint: '约 1 分钟，不跑大模型，只看清单与事件' },
  { value: 'standard', label: '标准体检', hint: '约 5 分钟，含大模型根因分析' },
  { value: 'deep', label: '深度体检', hint: '约 10 分钟，额外采集近 1 小时日志' },
]

export const SCAN_SCOPE_LABELS: Record<ScanScope, string> = {
  cluster: '全集群',
  namespace: '命名空间',
  pod: '单 Pod',
}

/** 两种产物的展示口径：报告详情页吃 JSON，Markdown 给人直接读或存档。 */
export const SCAN_FORMATS: { value: ScanFormat; label: string; hint: string }[] = [
  { value: 'json', label: 'JSON（推荐）', hint: '报告详情页按区块渲染结论、故障链与证据' },
  { value: 'markdown', label: 'Markdown', hint: '只归档一份可读正文，适合直接贴进工单' },
]

/** 发起一次体检。返回 202 与 pending 任务，结果靠 getScan 轮询。 */
export async function createScan(req: ScanRequest): Promise<ScanJob> {
  const env = await apiPost<ScanJob>('/scans', {
    scope: req.scope,
    namespace: req.namespace ?? '',
    pod: req.pod ?? '',
    preset: req.preset ?? 'standard',
    format: req.format ?? 'json',
  })
  if (!env.data?.id) {
    throw new Error('未返回体检任务')
  }
  return env.data
}

/** 查询任务状态。他人任务与不存在的任务同形 404。 */
export async function getScan(id: string): Promise<ScanJob> {
  const env = await apiGet<ScanJob>(`/scans/${encodeURIComponent(id)}`)
  if (!env.data) {
    throw new Error('体检任务不存在')
  }
  return env.data
}

/**
 * 停止一次体检（P9.11）。只有发起人能停自己的任务，他人任务与不存在的任务同形 404；
 * 后端在返回前就把状态写成 cancelled，页面立刻停轮询，不必等采集 goroutine 退出。
 */
export async function cancelScan(id: string): Promise<ScanJob> {
  const env = await apiPost<ScanJob>(`/scans/${encodeURIComponent(id)}/cancel`, {})
  if (!env.data) {
    throw new Error('停止体检失败')
  }
  return env.data
}

/** 终态判定：这三种状态停止轮询（cancelled 是发起人主动停止）。 */
export function isScanFinished(job: ScanJob): boolean {
  return job.status === 'succeeded' || job.status === 'failed' || job.status === 'cancelled'
}

/** 统一错误文案：403 要说清「没扫、也没偷偷改扫别的」，这是体检结论可信的前提。 */
export function describeScanError(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.status === 409) {
      return '已有一次体检正在执行，请等它结束后再发起'
    }
    if (error.status === 403 && error.message) {
      return error.message
    }
    if (error.status === 501) {
      return '当前服务未开启体检能力'
    }
    if (error.message) {
      return error.message
    }
  }
  if (error instanceof Error && error.message) {
    return error.message
  }
  return '开始体检失败，请稍后重试'
}

/** 停止体检的错误文案：409 在两个端点上的意思完全不同，不能共用一句。 */
export function describeCancelError(error: unknown): string {
  if (error instanceof ApiRequestError && error.status === 409) {
    return '该次体检已经结束，无需停止'
  }
  return describeScanError(error)
}

// 保持 DefaultExport 便于使用方统一 import 默认值。
export default {
  createScan,
  getScan,
  cancelScan,
  isScanFinished,
  describeScanError,
  describeCancelError,
  SCAN_PRESETS,
  SCAN_FORMATS,
  SCAN_SCOPE_LABELS,
}
