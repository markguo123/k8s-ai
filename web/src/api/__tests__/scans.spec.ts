import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  createScan,
  describeScanError,
  getScan,
  isScanFinished,
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

describe('scans API（P8.13 页面发起巡检）', () => {
  it('createScan 只发白名单四项，缺省补 standard', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(202, job()))
    vi.stubGlobal('fetch', fetchMock)

    const result = await createScan({ scope: 'namespace', namespace: 'arc' })

    expect(urlOf(fetchMock)).toBe('/api/v1/scans')
    // 连接参数、report.directory、超时都由服务端配置决定：多一个键就等于把
    // 「往哪扫、往哪写」交给浏览器，后端 DisallowUnknownFields 会直接 400。
    expect(bodyOf(fetchMock)).toEqual({ scope: 'namespace', namespace: 'arc', pod: '', preset: 'standard' })
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
    })
    expect(bodyOf(fetchMock, 1)).toEqual({ scope: 'cluster', namespace: '', pod: '', preset: 'quick' })
  })

  it('后端没回任务 id 时直接报错，界面不会拿着 undefined 去轮询', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(202, undefined)))

    await expect(createScan({ scope: 'namespace', namespace: 'arc' })).rejects.toThrow('未返回巡检任务')
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

  it('错误文案把「并发冲突」和「越权」说清楚', () => {
    expect(describeScanError(new ApiRequestError(409, 'conflict', 'a scan is already running'))).toContain(
      '已有一次巡检正在执行',
    )
    // 403 原样透传后端文案：点名是哪个命名空间，才看得出没被静默裁剪成别的范围。
    expect(describeScanError(new ApiRequestError(403, 'forbidden', '你没有命名空间 arc 的访问权限'))).toBe(
      '你没有命名空间 arc 的访问权限',
    )
    expect(describeScanError(new ApiRequestError(501, 'internal_error', 'scan disabled'))).toContain('未开启巡检能力')
    expect(describeScanError(new Error('boom'))).toBe('boom')
    expect(describeScanError('x')).toContain('发起巡检失败')
  })

  it('三档预设齐备且默认档是标准巡检', () => {
    expect(SCAN_PRESETS.map((item) => item.value)).toEqual(['quick', 'standard', 'deep'])
    expect(SCAN_PRESETS.every((item) => item.label && item.hint)).toBe(true)
  })
})
