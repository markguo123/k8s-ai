import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  createGrant,
  createToken,
  createUser,
  deleteGrant,
  deleteToken,
  deleteUser,
  describeUsersError,
  getGrant,
  listRoles,
  listTokens,
  listUsers,
  updateGrant,
  updateTokenStatus,
  updateUser,
} from '@/api/users'
import { TOKEN_KEY } from '@/api/request'

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

function makeUser(id: string, username: string, roleId = 'user'): Record<string, unknown> {
  return {
    id,
    username,
    displayName: `显示名 ${username}`,
    roleId,
    status: 'active',
    createdAt: '2026-10-20T00:00:00Z',
    updatedAt: '2026-10-20T00:00:00Z',
  }
}

function makeGrant(id: string, principalId: string): Record<string, unknown> {
  return {
    id,
    principalType: 'user',
    principalId,
    clusterId: 'cluster-demo',
    namespaces: ['ns-a', 'ns-b'],
    permissions: ['*'],
    expiresAt: '2030-01-01T00:00:00Z',
    createdBy: 'admin',
    createdAt: '2026-10-20T00:00:00Z',
    updatedAt: '2026-10-20T00:00:00Z',
  }
}

function makeToken(id: string, name: string): Record<string, unknown> {
  return {
    id,
    userId: 'u-1',
    name,
    expiresAt: '2030-01-01T00:00:00Z',
    status: 'active',
    createdAt: '2026-10-20T00:00:00Z',
    updatedAt: '2026-10-20T00:00:00Z',
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(TOKEN_KEY)
})

describe('users API（P3.12 用户与权限前端契约）', () => {
  it('listUsers / listRoles 解析列表，空 data 兜底为空数组', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, [makeUser('u-1', 'alice')])))
    const list = await listUsers()
    expect(list).toHaveLength(1)
    expect(list[0].username).toBe('alice')
    expect((list[0] as unknown as Record<string, unknown>).password).toBeUndefined()

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, undefined)))
    await expect(listUsers()).resolves.toEqual([])

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, [])))
    await expect(listRoles()).resolves.toEqual([])
  })

  it('createUser 携带密码与角色提交，返回不含口令', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(201, makeUser('u-new', 'alice', 'operator')))
    vi.stubGlobal('fetch', fetchMock)
    const user = await createUser({
      username: 'alice',
      displayName: 'A',
      password: 'password123',
      roleId: 'operator',
      status: 'active',
    })
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((init.method ?? '').toUpperCase()).toBe('POST')
    expect(JSON.parse(init.body as string)).toMatchObject({
      username: 'alice',
      password: 'password123',
      roleId: 'operator',
    })
    expect(user.roleId).toBe('operator')
  })

  it('updateUser 走 PATCH 且 id 需 URL 编码', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, makeUser('u/1', 'alice')))
    vi.stubGlobal('fetch', fetchMock)
    await updateUser('u/1', { displayName: '新名', status: 'disabled' })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain(`/users/${encodeURIComponent('u/1')}`)
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ displayName: '新名', status: 'disabled' })
  })

  it('deleteUser 成功返回无副作用；信封错误时抛错并归一文案', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: 'ok' })))
    await expect(deleteUser('u-1')).resolves.toBeUndefined()

    const errorEnvelope = {
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code: 'forbidden', message: 'permission denied' },
    }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response(JSON.stringify(errorEnvelope), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      })),
    )
    await expect(deleteUser('u-1')).rejects.toMatchObject({ status: 403, code: 'forbidden' })
    expect(describeUsersError('删除用户', new Error('permission denied'))).toContain('permission denied')
    expect(describeUsersError('删除用户', null)).toBe('删除用户失败，请稍后重试')
  })

  it('grants CRUD：创建/读取/更新/撤销均按契约携带正确方法与主体', async () => {
    const created = makeGrant('g-1', 'alice')
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(201, created))
    vi.stubGlobal('fetch', fetchMock)
    const grant = await createGrant({
      principalType: 'user',
      principalId: 'alice',
      namespaces: ['ns-a', 'ns-b'],
      permissions: ['*'],
    })
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toMatchObject({ principalId: 'alice', namespaces: ['ns-a', 'ns-b'] })
    expect(grant.principalId).toBe('alice')

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, created)))
    await expect(getGrant('g-1')).resolves.toMatchObject({ id: 'g-1' })

    const updateMock = vi.fn().mockResolvedValue(envelopeResponse(200, { ...created, permissions: ['destroy'] }))
    vi.stubGlobal('fetch', updateMock)
    await updateGrant('g-1', { permissions: ['destroy'], expiresAt: '2031-01-01T00:00:00Z' })
    const [url, updateInit] = updateMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/grants/g-1')
    expect(updateInit.method).toBe('PATCH')
    expect(JSON.parse(updateInit.body as string)).toEqual({
      permissions: ['destroy'],
      expiresAt: '2031-01-01T00:00:00Z',
    })

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: 'ok' })))
    await expect(deleteGrant('g-1')).resolves.toBeUndefined()
  })

  it('tokens：列表 / 签发明文一次 / 状态流转 / 删除', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, [makeToken('t-1', 'cli')])))
    await expect(listTokens('u-1')).resolves.toHaveLength(1)

    const createMock = vi.fn().mockResolvedValue(
      envelopeResponse(201, { apiToken: makeToken('t-2', 'ci'), token: 'plain-token-value' }),
    )
    vi.stubGlobal('fetch', createMock)
    const result = await createToken('u-1', { name: 'ci', expiresAt: '2030-01-01T00:00:00Z' })
    const [, init] = createMock.mock.calls[0] as [string, RequestInit]
    expect(init.method).toBe('POST')
    expect(result.token).toBe('plain-token-value')
    expect(result.apiToken.name).toBe('ci')

    const patchMock = vi.fn().mockResolvedValue(envelopeResponse(200, { ...makeToken('t-1', 'cli'), status: 'revoked' }))
    vi.stubGlobal('fetch', patchMock)
    await updateTokenStatus('u-1', 't-1', 'revoked')
    const [url, patchInit] = patchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain(`/users/u-1/tokens/${encodeURIComponent('t-1')}`)
    expect(patchInit.method).toBe('PATCH')
    expect(JSON.parse(patchInit.body as string)).toEqual({ status: 'revoked' })

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, { deleted: 'ok' })))
    await expect(deleteToken('u-1', 't-1')).resolves.toBeUndefined()
  })
})
