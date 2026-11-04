import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  cleanupReports,
  deleteReport,
  describeReportError,
  downloadReport,
  filenameFromDisposition,
  getReportDownloadUrl,
  getStorageHealth,
  listReports,
} from '@/api/reports'
import { ApiRequestError, TOKEN_KEY } from '@/api/request'
import type { ReportView } from '@/api/types'

function envelopeResponse<T>(status: number, data?: T, pagination?: unknown): Response {
  return new Response(
    JSON.stringify({ request_id: 'req-test', timestamp: new Date().toISOString(), data, pagination }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code, message },
    }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

function sampleReport(overrides: Partial<ReportView> = {}): ReportView {
  return {
    id: 'r1',
    clusterId: 'c1',
    kind: 'latest-md',
    mediaType: 'text/markdown; charset=utf-8',
    size: 2048,
    sha256: 'a'.repeat(64),
    findings: 3,
    createdBy: 'u1',
    available: true,
    createdAt: '2026-11-01T02:00:00Z',
    ...overrides,
  }
}

/** 从 fetch mock 里取第 n 次调用的 URL。 */
function urlOf(fetchMock: ReturnType<typeof vi.fn>, index = 0): string {
  const [url] = fetchMock.mock.calls[index]
  return String(url)
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('reports API（P8.11）', () => {
  it('listReports 透传分页与筛选，并读取信封 pagination.total', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      envelopeResponse(200, [sampleReport()], { page: 2, page_size: 20, total: 41, total_pages: 3 }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const result = await listReports({ page: 2, pageSize: 20, clusterId: 'c1', kind: 'latest-md' })

    expect(urlOf(fetchMock)).toBe('/api/v1/reports?page=2&page_size=20&clusterId=c1&kind=latest-md')
    expect(result.total).toBe(41)
    expect(result.totalPages).toBe(3)
    expect(result.items[0].kind).toBe('latest-md')
  })

  it('未选择的筛选项不进 query string，空列表不报错', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, [], { total: 0 }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await listReports({ page: 1, pageSize: 20, clusterId: '', kind: '' })

    expect(urlOf(fetchMock)).toBe('/api/v1/reports?page=1&page_size=20')
    expect(result.items).toEqual([])
    expect(result.total).toBe(0)
  })

  it('downloadReport 带 Bearer 下载二进制并解析 filename*', async () => {
    vi.useFakeTimers()
    const createObjectURL = vi.fn(() => 'blob:report')
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, writable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, writable: true })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const fetchMock = vi.fn().mockResolvedValue(
      new Response('# 体检报告', {
        status: 200,
        headers: { 'Content-Disposition': "attachment; filename*=UTF-8''report-r1-latest-md.md" },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')

    const filename = await downloadReport('r1')

    expect(urlOf(fetchMock)).toBe('/api/v1/reports/r1/content?mode=stream')
    const [, init] = fetchMock.mock.calls[0]
    expect((init.headers as Headers).get('Authorization')).toBe('Bearer tok-demo')
    expect(filename).toBe('report-r1-latest-md.md')
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    expect(click).toHaveBeenCalledTimes(1)
    // blob: 链接必须回收，否则每下一次都漏一块内存。
    vi.advanceTimersByTime(60_000)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:report')
  })

  it('下载遇到 503 时给出去错方向的提示，且不创建 blob 链接', async () => {
    const createObjectURL = vi.fn(() => 'blob:should-not-happen')
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, writable: true })
    const fetchMock = vi
      .fn()
      .mockResolvedValue(errorResponse(503, 'service_unavailable', 'object storage is down'))
    vi.stubGlobal('fetch', fetchMock)

    let caught: unknown
    try {
      await downloadReport('r1')
    } catch (error) {
      caught = error
    }

    expect(caught).toBeInstanceOf(ApiRequestError)
    expect((caught as ApiRequestError).status).toBe(503)
    expect(createObjectURL).not.toHaveBeenCalled()
    expect(describeReportError('下载报告', caught)).toContain('聊天、审批、审计不受影响')
  })

  it('409/410/501 各自给出可执行的下一步', async () => {
    const mismatch = new ApiRequestError(409, 'conflict', 'lives on another driver')
    const gone = new ApiRequestError(410, 'gone', 'content gone')
    const disabled = new ApiRequestError(501, 'internal_error', 'driver=off')
    expect(describeReportError('下载', mismatch)).toContain('blobs migrate')
    expect(describeReportError('下载', gone)).toContain('已不可恢复')
    expect(describeReportError('下载', disabled)).toContain('未开启对象存储归档')
  })

  it('presign 走 mode=presign 并回传签名链接', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(envelopeResponse(200, { report: sampleReport(), downloadUrl: 'https://s3/signed' }))
    vi.stubGlobal('fetch', fetchMock)

    const ticket = await getReportDownloadUrl('r1')

    expect(urlOf(fetchMock)).toBe('/api/v1/reports/r1/content?mode=presign')
    expect(ticket.downloadUrl).toBe('https://s3/signed')
    expect(ticket.report.id).toBe('r1')
  })

  it('storage/health 直接返回健康体（含 driverMismatch）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      envelopeResponse(200, {
        driver: 's3',
        status: 'ready',
        ready: true,
        objects: 12,
        bytes: 4096,
        driverMismatch: { db: 2 },
        observedAt: new Date().toISOString(),
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const health = await getStorageHealth()

    expect(urlOf(fetchMock)).toBe('/api/v1/storage/health')
    expect(health.status).toBe('ready')
    expect(health.driverMismatch?.db).toBe(2)
  })

  it('filenameFromDisposition 兼容百分号编码与普通 filename，并回落到 id', () => {
    expect(filenameFromDisposition("attachment; filename*=UTF-8''%E6%8A%A5%E5%91%8A.md", 'r1')).toBe(
      '报告.md',
    )
    expect(filenameFromDisposition('attachment; filename="plain.json"', 'r1')).toBe('plain.json')
    expect(filenameFromDisposition(null, 'r1')).toBe('report-r1')
    expect(filenameFromDisposition('attachment', 'r1')).toBe('report-r1')
  })
})

describe('报告清理 API（P8.14）', () => {
  it('listReports 把 scanId 带进 query，用于只看本次巡检产物', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, [sampleReport()], { total: 1 }))
    vi.stubGlobal('fetch', fetchMock)

    await listReports({ page: 1, pageSize: 20, scanId: 'scan-1' })

    expect(urlOf(fetchMock)).toBe('/api/v1/reports?page=1&page_size=20&scanId=scan-1')
  })

  it('deleteReport 走 DELETE，返回值补齐成 deleted/skipped 两个数组', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: ['r1'] }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await deleteReport('r1')

    expect(urlOf(fetchMock)).toBe('/api/v1/reports/r1')
    const [, init] = fetchMock.mock.calls[0]
    expect((init as RequestInit).method).toBe('DELETE')
    // 后端只回 deleted 时也要能安全遍历：否则页面收尾得到处写 data.skipped || []。
    expect(result).toEqual({ deleted: ['r1'], skipped: [] })
  })

  it('cleanupReports 提交 ids 数组，skipped 连原因一起带回界面', async () => {
    const skipped = [{ id: 'r9', reason: 'not_found' }]
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: ['r1'], skipped }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await cleanupReports(['r1', 'r9'])

    expect(urlOf(fetchMock)).toBe('/api/v1/reports/cleanup')
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({
      ids: ['r1', 'r9'],
    })
    // r9 是别人的或不存在的 id：它必须留在 skipped 里，界面不能报「清理了 2 条」。
    expect(result.deleted).toEqual(['r1'])
    expect(result.skipped).toEqual(skipped)
  })

  it('后端把空 skipped 编成 null 时不会让界面在 length 上炸掉', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: ['r1'] })))

    const result = await cleanupReports(['r1'])

    expect(result).toEqual({ deleted: ['r1'], skipped: [] })
  })
})
