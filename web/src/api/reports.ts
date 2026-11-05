/**
 * P8.11 报告与对象存储 API 客户端（与后端 `internal/server/reports.go`、
 * `internal/server/storage_health.go` 一致）。
 *
 * 三条安全约定写在这里，是为了让后端的设计意图在调用点就看得见：
 *  1. 可见性判定全在后端（owner + AuthzGrant 集群范围 + admin），前端不做任何"这条报告
 *     你能不能看"的推断，只负责把后端给的东西画出来；越权同形 404，前端也就无从绕过。
 *  2. 报告正文是二进制流，不能走通用 JSON 信封封装，因此 downloadReport 自己 fetch，
 *     但错误翻译复用 apiErrorFrom，避免同一套错误码在两边各写一遍。
 *  3. presign 返回的签名 URL 等同一条临时凭据：只在调用方内存里停留，绝不落
 *     localStorage / store / 日志 / SSE。
 */
import {
  API_BASE,
  ApiRequestError,
  apiErrorFrom,
  apiDelete,
  apiGet,
  apiPost,
  readToken,
} from './request'
import type {
  ReportCleanupResult,
  ReportDownloadTicket,
  ReportOverview,
  ReportView,
  StorageHealth,
  StructuredReportEnvelope,
} from './types'

/** 报告列表查询条件（后端 handleListReports 的 query 参数）。 */
export interface ReportListOptions {
  page?: number
  pageSize?: number
  clusterId?: string
  kind?: string
  /** 只看某次体检产出的产物；这是筛选条件，不是授权条件，越权判定仍由后端做。 */
  scanId?: string
  /**
   * 报告类别（cluster | business | other）：集群体检 / 业务体检 / 范围未记录。
   * 与顶部总览共用后端的同一条分组口径，非法取值后端按「不筛」处理。
   */
  scopeCategory?: string
}

/** 报告列表结果：total 与当页共用后端同一条归属谓词，因此不会把别人的报告算进来。 */
export interface ReportListResult {
  items: ReportView[]
  total: number
  page: number
  pageSize: number
  totalPages: number
}

/** 列表页只回元数据列，后端刻意不加载对象正文。 */
export async function listReports(opts: ReportListOptions = {}): Promise<ReportListResult> {
  const env = await apiGet<ReportView[]>(
    '/reports',
    { page: opts.page, pageSize: opts.pageSize },
    { clusterId: opts.clusterId, kind: opts.kind, scanId: opts.scanId, scopeCategory: opts.scopeCategory },
  )
  return {
    items: env.data ?? [],
    total: env.pagination?.total ?? 0,
    page: env.pagination?.page ?? opts.page ?? 1,
    pageSize: env.pagination?.page_size ?? opts.pageSize ?? 0,
    totalPages: env.pagination?.total_pages ?? 0,
  }
}

/** blob: 链接的存活时间。点击之后立刻 revoke 在部分浏览器里会把下载一起取消掉。 */
const BLOB_URL_TTL_MS = 60_000

/** 从 Content-Disposition 里取文件名。后端只发 RFC 5987 的 filename*，
 * 但这里同时兼容 filename="..."，回落 report-<id>，宁可名字丑也不要下载失败。 */
export function filenameFromDisposition(header: string | null, fallbackId: string): string {
  const fallback = `report-${fallbackId}`
  if (!header) {
    return fallback
  }
  const extended = /filename\*\s*=\s*([^']*)'[^']*'([^;]+)/i.exec(header)
  if (extended?.[2]) {
    return decodeFilename(extended[2].trim(), fallback)
  }
  const quoted = /filename\s*=\s*"([^"]+)"/i.exec(header) || /filename\s*=\s*([^;]+)/i.exec(header)
  const plain = quoted?.[1]?.trim()
  return plain ? decodeFilename(plain, fallback) : fallback
}

function decodeFilename(raw: string, fallback: string): string {
  try {
    return decodeURIComponent(raw.replace(/^"|"$/g, '')) || fallback
  } catch {
    return fallback
  }
}

/**
 * 流式下载报告正文，返回实际保存的文件名（供页面提示）。
 *
 * 必须自己拼 fetch：响应体是二进制附件，套进 request() 的 JSON 解析会把内容吃掉。
 */
async function fetchAndSave(id: string, suffix: string): Promise<string> {
  const headers = new Headers({ Accept: 'application/octet-stream' })
  const token = readToken()
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }
  const response = await fetch(`${API_BASE}/reports/${encodeURIComponent(id)}${suffix}`, { headers })
  if (!response.ok) {
    throw apiErrorFrom(response.status, await response.text())
  }
  const filename = filenameFromDisposition(response.headers.get('Content-Disposition'), id)
  const blob = await response.blob()
  const url = URL.createObjectURL(blob)
  triggerBrowserDownload(url, filename)
  window.setTimeout(() => URL.revokeObjectURL(url), BLOB_URL_TTL_MS)
  return filename
}

/** 流式下载归档里的原文（Markdown 或 JSON，取决于发起体检时选的那一种）。 */
export async function downloadReport(id: string): Promise<string> {
  return fetchAndSave(id, '/content?mode=stream')
}

/**
 * 按需导出一份 Markdown（P9.10）。
 *
 * 一次体检只归档一种产物，想要另一种格式时由后端用同一份正文当场渲染：归档里只有
 * 一个事实源，页面上也就不会出现「哪份才是最新的」这种问题。
 */
export async function exportReportMarkdown(id: string): Promise<string> {
  return fetchAndSave(id, '/export')
}

function triggerBrowserDownload(url: string, filename: string): void {
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.rel = 'noopener'
  anchor.style.display = 'none'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
}

/**
 * 申请一条直连下载链接（仅 s3 后端支持；其余后端回 400，调用方回落 stream）。
 * 返回值含签名，按文件头第 3 条约定处置。
 */
export async function getReportDownloadUrl(id: string): Promise<ReportDownloadTicket> {
  const env = await apiGet<ReportDownloadTicket>(
    `/reports/${encodeURIComponent(id)}/content`,
    undefined,
    { mode: 'presign' },
  )
  if (!env.data?.downloadUrl) {
    throw new Error('该后端未返回直连链接，请改用流式下载')
  }
  return env.data
}

/**
 * 读结构化体检报告（P9 报告详情页数据源）。
 *
 * 与 downloadReport 是两条用途不同的通道，不能互相顶替：/content 强制附件下载且禁
 * MIME 嗅探，浏览器里拿不到正文；/report 走普通 JSON 信封，专门给页面渲染。两者的
 * 可见性判定复用后端同一条 scope 谓词，详情页因此不是第二条越权读取通道。
 *
 * 后端会顺着同一轮体检去找 JSON 产物（列表里点开的往往是 Markdown 那一行），前端
 * 不需要知道哪一行是 latest-json；真的没有结构化形态时回 415，由 describeReportError
 * 翻译成「请使用原文下载」。
 */
export async function getStructuredReport(id: string): Promise<StructuredReportEnvelope> {
  const env = await apiGet<StructuredReportEnvelope>(`/reports/${encodeURIComponent(id)}/report`)
  if (!env.data) {
    throw new Error('报告详情为空')
  }
  return env.data
}

/**
 * 对象存储健康。后端恒回 200（degraded 也是 200 + status），这样徽标不会因为
 * 一次"健康的坏消息"变成请求失败，probeError 那句真正有用的话也就丢掉了。
 */
export async function getStorageHealth(): Promise<StorageHealth> {
  const env = await apiGet<StorageHealth>('/storage/health')
  if (!env.data) {
    throw new Error('存储健康数据为空')
  }
  return env.data
}

/**
 * 报告列表顶部的类别总览（P9.13）。
 *
 * 统计口径是「你看得见的全部报告」，与分页无关，因此翻页不需要重算；但它和列表共用
 * 同一条归属与筛选谓词，所以筛「业务体检」时这里的数字会跟着一起变。
 */
export async function getReportSummary(opts: ReportListOptions = {}): Promise<ReportOverview> {
  const env = await apiGet<ReportOverview>(
    '/reports/summary',
    undefined,
    { clusterId: opts.clusterId, kind: opts.kind, scanId: opts.scanId, scopeCategory: opts.scopeCategory },
  )
  if (!env.data) {
    throw new Error('报告总览数据为空')
  }
  return env.data
}

/**
 * 删除一条报告的业务引用（P8.14）。删的是「平台认为这份报告存在」，正文由
 * `blobs gc` 在宽限期后回收：请求路径上永不出现对象存储的 Delete，因为 blob_key
 * 是内容寻址、可能被多份报告共享。
 *
 * 越权同形 404：别人的报告 id 与不存在的 id 回一样的东西，猜 id 探不到任何东西。
 */
export async function deleteReport(id: string): Promise<ReportCleanupResult> {
  const env = await apiDelete<ReportCleanupResult>(`/reports/${encodeURIComponent(id)}`)
  return normalizeCleanup(env.data)
}

/**
 * 批量清理（P8.14）。后端逐条判归属、逐条独立事务，混进来的他人 id 只会变成一条
 * skipped，不会让整批失败，也不会顺手删掉别人的东西。
 */
export async function cleanupReports(ids: string[]): Promise<ReportCleanupResult> {
  const env = await apiPost<ReportCleanupResult>('/reports/cleanup', { ids })
  return normalizeCleanup(env.data)
}

function normalizeCleanup(data?: ReportCleanupResult): ReportCleanupResult {
  return { deleted: data?.deleted ?? [], skipped: data?.skipped ?? [] }
}

/** 统一错误文案，供页面直接展示。状态码优先于错误码：它们是后端分档的依据。 */
export function describeReportError(method: string, error: unknown): string {
  if (error instanceof ApiRequestError) {
    switch (error.status) {
      case 415:
        // 不是错误清单里的一条，而是「这条报告太老，压根没有结构化形态」：
        // 页面要给出的动作是去看原文，而不是渲染一个空壳详情页。
        return '该报告没有结构化版本（早期只归档了 Markdown 正文），请使用原文下载查看'
      case 413:
        return '报告正文超过服务端读取上限，无法在页面展开，请改用原文下载查看'
      case 503:
        return '对象存储暂不可用，报告暂时读不到；聊天、审批、审计不受影响，请稍后重试'
      case 410:
        return '该报告的内容已不可恢复（可能已过期清理），请检查对象存储备份'
      case 409:
        return '报告内容仍在另一个对象存储后端，需先在服务端执行 blobs migrate 后重试'
      case 501:
        return '未开启对象存储归档（platform.blobs.driver=off），平台不会生成报告产物'
      case 403:
        return '没有访问该报告的权限'
      case 404:
        return '报告不存在，或不在你的可见范围内'
      default:
        break
    }
    if (error.message) {
      return error.message
    }
  }
  if (error instanceof Error && error.message) {
    return error.message
  }
  return `${method}失败，请稍后重试`
}

// 保持 DefaultExport 便于使用方统一 import 默认值。
export default {
  listReports,
  getReportSummary,
  downloadReport,
  exportReportMarkdown,
  getReportDownloadUrl,
  getStructuredReport,
  getStorageHealth,
  deleteReport,
  cleanupReports,
  describeReportError,
}
