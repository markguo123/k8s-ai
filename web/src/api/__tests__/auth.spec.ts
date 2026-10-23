import { afterEach, describe, expect, it, vi } from 'vitest'

import { changePassword, myScope } from '@/api/auth'
import { TOKEN_KEY } from '@/api/request'

function envelopeResponse(status: number, data?: unknown): Response {
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
  localStorage.removeItem(TOKEN_KEY)
})

describe('auth API（P6.12 自助改密契约）', () => {
  it('changePassword 走 POST /auth/password，携带旧/新密码且不触发 401 跳转', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, { status: 'ok' }))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')

    await changePassword({ oldPassword: 'old-pass', newPassword: 'new-pass-123' })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/auth/password')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({
      oldPassword: 'old-pass',
      newPassword: 'new-pass-123',
    })
    const headers = init.headers as Headers
    expect(headers.get('Authorization')).toBe('Bearer tok-demo')
  })

  it('旧密码错误（401）时原样抛出，不清会话、不跳转登录页', async () => {
    const errorEnvelope = {
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code: 'unauthorized', message: 'old password is incorrect' },
    }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(() => new Response(JSON.stringify(errorEnvelope), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      })),
    )
    localStorage.setItem(TOKEN_KEY, 'tok-demo')

    await expect(changePassword({ oldPassword: 'bad', newPassword: 'new-pass-123' }))
      .rejects.toMatchObject({ status: 401, code: 'unauthorized' })
    expect(localStorage.getItem(TOKEN_KEY)).toBe('tok-demo')
  })

  it('myScope 读取 /auth/scope 有效授权范围', async () => {
    const scope = {
      namespaces: ['mysql-dev'],
      clusterScoped: false,
      isAdmin: false,
      canWrite: true,
      canDestroy: false,
    }
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, scope))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')

    await expect(myScope()).resolves.toEqual(scope)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/auth/scope')
    expect((init.headers as Headers).get('Authorization')).toBe('Bearer tok-demo')
  })
})
