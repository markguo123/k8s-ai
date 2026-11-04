import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  cancelScan,
  createScan,
  describeCancelError,
  describeScanError,
  getScan,
  isScanFinished,
  SCAN_FORMATS,
  SCAN_PRESETS,
} from '@/api/scans'
import { ApiRequestError, TOKEN_KEY } from '@/api/request'
import type { ScanJob } from '@/api/types'

function envelopeResponse<T>(status: number, data?: T): Response {
  return new Response(
    JSON.stringify({ request_id: 'req-test', timestamp: new Date().toISOString(), data }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

function job(overrides: Partial<ScanJob> = {}): ScanJob {
  return {
    id: 'job-1',
    status: 'pending',
    scopeKind: 'namespace',
    scopeValue: 'arc',
    createdAt: '2026-11-04T02:00:00Z',
    ...overrides,
  }
}

function urlOf(fetchMock: ReturnType<typeof vi.fn>, index = 0): string {
  return String(fetchMock.mock.calls[index][0])
}

function bodyOf(fetchMock: ReturnType<typeof vi.fn>, index = 0): Record<string, unknown> {
  return JSON.parse(String((fetchMock.mock.calls[index][1] as RequestInit).body))
}

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(TOKEN_KEY, 'tok-demo')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('scans API（P8.13 页面开始体检）', () => {
  it('createScan 只发白名单五项，缺省补 standard 与 json', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(202, job()))
    vi.stubGlobal('fetch', fetchMock)

    const result = await createScan({ scope: 'namespace', namespace: 'arc' })

    expect(urlOf(fetchMock)).toBe('/api/v1/scans')
    // 连接参数、report.directory、超时都由服务端配置决定：多一个键就等于把
    // 「往哪扫、往哪写」交给浏览器，后端 DisallowUnknownFields 会直接 400。
    // format 缺省 json：结构化正文才撑得起报告详情页的结论、故障链与证据区块。
    expect(bodyOf(fetchMock)).toEqual({
      scope: 'namespace',
      namespace: 'arc',
      pod: '',
      preset: 'standard',
      format: 'json',
    })
    expect(result.id).toBe('job-1')
  })

  it('createScan 保留 pod 与档位，cluster 不需要命名空间', async () => {
    // Response 的 body 只能读一次，两次调用必须各拿一个新实例。
    const fetchMock = vi.fn(async () => envelopeResponse(202, job()))
    vi.stubGlobal('fetch', fetchMock)

    await createScan({ scope: 'pod', namespace: 'arc', pod: 'api-7d9', preset: 'deep' })
    await createScan({ scope: 'cluster', preset: 'quick' })

    expect(bodyOf(fetchMock, 0)).toEqual({
      scope: 'pod',
      namespace: 'arc',
      pod: 'api-7d9',
      preset: 'deep',
      format: 'json',
    })
    expect(bodyOf(fetchMock, 1)).toEqual({
      scope: 'cluster',
      namespace: '',
      pod: '',
      preset: 'quick',
      format: 'json',
    })
  })

  it('选 markdown 时按所选格式发送（一次体检只归档那一种产物）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(202, job()))
    vi.stubGlobal('fetch', fetchMock)

    await createScan({ scope: 'namespace', namespace: 'arc', format: 'markdown' })

    expect(bodyOf(fetchMock).format).toBe('markdown')
  })

  it('后端没回任务 id 时直接报错，界面不会拿着 undefined 去轮询', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(202, undefined)))

    await expect(createScan({ scope: 'namespace', namespace: 'arc' })).rejects.toThrow('未返回体检任务')
  })

  it('getScan 查询单个任务，路径按 id 转义', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(envelopeResponse(200, job({ status: 'succeeded', result: { findings: [] } })))
    vi.stubGlobal('fetch', fetchMock)

    const result = await getScan('job/1')

    expect(urlOf(fetchMock)).toBe('/api/v1/scans/job%2F1')
    expect(isScanFinished(result)).toBe(true)
  })

  it('isScanFinished 只在两种终态停轮询', () => {
    expect(isScanFinished(job({ status: 'pending' }))).toBe(false)
    expect(isScanFinished(job({ status: 'running' }))).toBe(false)
    expect(isScanFinished(job({ status: 'succeeded' }))).toBe(true)
    expect(isScanFinished(job({ status: 'failed' }))).toBe(true)
  })

  it('isScanFinished 把 cancelled 也算终态，停止后不会一直轮询', () => {
    expect(isScanFinished(job({ status: 'cancelled' }))).toBe(true)
  })

  it('cancelScan 打到 /scans/{id}/cancel，路径按 id 转义', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(envelopeResponse(200, job({ status: 'cancelled', error: '已由发起人停止' })))
    vi.stubGlobal('fetch', fetchMock)

    const result = await cancelScan('job/1')

    expect(urlOf(fetchMock)).toBe('/api/v1/scans/job%2F1/cancel')
    expect(result.status).toBe('cancelled')
  })

  it('停止端点没回任务时报错，页面不会挂着一个假装已停的任务', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, undefined)))

    await expect(cancelScan('job-1')).rejects.toThrow('停止体检失败')
  })

  it('错误文案把「并发冲突」和「越权」说清楚', () => {
    expect(describeScanError(new ApiRequestError(409, 'conflict', 'a scan is already running'))).toContain(
      '已有一次体检正在执行',
    )
    // 403 原样透传后端文案：点名是哪个命名空间，才看得出没被静默裁剪成别的范围。
    expect(describeScanError(new ApiRequestError(403, 'forbidden', '你没有命名空间 arc 的访问权限'))).toBe(
      '你没有命名空间 arc 的访问权限',
    )
    expect(describeScanError(new ApiRequestError(501, 'internal_error', 'scan disabled'))).toContain('未开启体检能力')
    expect(describeScanError(new Error('boom'))).toBe('boom')
    expect(describeScanError('x')).toContain('开始体检失败')
  })

  it('停止遇到 409 说「已经结束」，不复用「已有体检在跑」那句', () => {
    // 同一个状态码在两个端点上意思相反：发起时 409 是抢占失败，停止时 409 是本来就没在跑。
    expect(describeCancelError(new ApiRequestError(409, 'conflict', 'scan not running'))).toContain(
      '该次体检已经结束，无需停止',
    )
    expect(describeCancelError(new Error('boom'))).toBe('boom')
  })

  it('三档预设齐备且默认档是标准体检', () => {
    expect(SCAN_PRESETS.map((item) => item.value)).toEqual(['quick', 'standard', 'deep'])
    expect(SCAN_PRESETS.every((item) => item.label && item.hint)).toBe(true)
  })

  it('两种报告格式齐备，json 排在前面（它是缺省档）', () => {
    expect(SCAN_FORMATS.map((item) => item.value)).toEqual(['json', 'markdown'])
    expect(SCAN_FORMATS.every((item) => item.label && item.hint)).toBe(true)
  })
})
