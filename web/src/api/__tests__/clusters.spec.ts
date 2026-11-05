import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  createCluster,
  deleteCluster,
  describeClusterError,
  listClusters,
  testCluster,
} from '@/api/clusters'
import { ApiRequestError, TOKEN_KEY } from '@/api/request'

function envelopeResponse<T>(status: number, data?: T): Response {
  return new Response(
    JSON.stringify({
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      data,
      error: undefined,
    }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('clusters API（P3.4）', () => {
  it('listClusters 返回受管集群元数据', async () => {
    const cluster = {
      id: 'c1',
      name: 'prod',
      context: 'prod-admin',
      server: 'https://10.0.0.1:6443',
      version: 'v1.30.2',
      status: 'connected',
      createdAt: '2026-10-15T00:00:00Z',
      updatedAt: '2026-10-15T00:00:00Z',
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, [cluster])))
    const list = await listClusters()
    expect(list).toHaveLength(1)
    expect(list[0].name).toBe('prod')
    expect(list[0].server).toBe('https://10.0.0.1:6443')
  })

  it('创建请求携带 Bearer 且不回传 kubeconfig 明文', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      envelopeResponse(201, { id: 'c2', name: 'stg', status: 'connected', createdAt: '', updatedAt: '' }),
    )
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')
    const created = await createCluster({ name: 'stg', kubeconfig: 'clouds:\n  - server: x\n' })
    const [, init] = fetchMock.mock.calls[0]
    const headers = init.headers as Headers
    expect(headers.get('Authorization')).toBe('Bearer tok-demo')
    const parsed = JSON.parse(init.body as string) as { kubeconfig: string }
    expect(parsed.kubeconfig).toContain('clouds')
    expect(created.name).toBe('stg')
    localStorage.removeItem(TOKEN_KEY)
  })

  it('testCluster 连测走 POST /clusters/test 且不透出明文', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      envelopeResponse(200, {
        name: 'probe',
        context: 'prod-admin',
        server: 'https://10.0.0.1:6443',
        version: 'v1.30.2',
        health: 'healthy',
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const info = await testCluster({ kubeconfig: 'clouds:\n  - server: x\n' })
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toContain('/clusters/test')
    expect(init.method).toBe('POST')
    const parsed = JSON.parse(init.body as string) as { kubeconfig: string }
    expect(parsed.kubeconfig).toContain('server: x')
    expect(info.health).toBe('healthy')
  })

  it('deleteCluster 成功后返回；失败抛出错误码', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: 'ok' })))
    await expect(deleteCluster('c1')).resolves.toBeUndefined()

    const errorEnvelope = {
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code: 'forbidden', message: 'permission denied' },
    }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(errorEnvelope), {
      status: 403,
      headers: { 'Content-Type': 'application/json' },
    })))
    await expect(deleteCluster('c1')).rejects.toMatchObject({ status: 403, code: 'forbidden' })
  })

  it('未配置加密密钥的 501 转为可执行的平台提示', () => {
    const error = new ApiRequestError(
      501,
      'internal_error',
      'cluster service is not configured: set K8S_AI_ENC_KEY',
    )
    expect(describeClusterError('加载集群', error)).toBe(
      '集群管理能力未启用，请联系平台管理员配置凭据加密密钥后重试',
    )
  })
})
