import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import UsersView from '@/views/users/UsersView.vue'

/**
 * P3.12 用户与权限页面回归：左 rail 四分区 + 全宽内容。
 *
 * 有 user.manage 的管理员进入完整管理视图（用户/角色/授权/Token）；
 * 无权限角色只看到 TechEmptyState，且不渲染任何管理入口。
 */

function envelopeResponse<T>(status: number, data: T): Response {
  return new Response(
    JSON.stringify({
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      data,
    }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

function makeUser(id: string, username: string, roleId: string): Record<string, unknown> {
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

function makeRole(id: string, name: string, permissions: string[]): Record<string, unknown> {
  return {
    id,
    name,
    description: `${name} 角色`,
    permissions,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
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
    userId: 'u-admin',
    name,
    expiresAt: '2030-01-01T00:00:00Z',
    status: 'active',
    createdAt: '2026-10-20T00:00:00Z',
    updatedAt: '2026-10-20T00:00:00Z',
  }
}

function installFetch(): void {
  const tokens = [makeToken('t-1', 'cli')]
  const users = [makeUser('u-admin', 'admin', 'admin'), makeUser('u-alice', 'alice', 'user')]
  const roles = [
    makeRole('admin', '管理员', ['*']),
    makeRole('operator', '运维', ['chat.write', 'cluster.manage']),
    makeRole('user', '普通用户', ['chat.read', 'chat.write', 'cluster.read', 'skill.read', 'approval.read']),
  ]
  const grants = [makeGrant('g-1', 'alice')]
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/roles')) {
        return envelopeResponse(200, roles)
      }
      if (url.endsWith('/grants')) {
        return envelopeResponse(200, grants)
      }
      if (url.includes('/users/') && url.endsWith('/tokens')) {
        return envelopeResponse(200, tokens)
      }
      if (url.endsWith('/users')) {
        return envelopeResponse(200, users)
      }
      throw new Error(`unexpected fetch url: ${url}`)
    }),
  )
}

enableAutoUnmount(afterEach)

describe('UsersView 用户与权限（P3.12）', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  describe('管理员视图', () => {
    beforeEach(() => {
      localStorage.setItem(TOKEN_KEY, 'tok-test')
      localStorage.setItem(
        USER_KEY,
        JSON.stringify({ userId: 'u-admin', username: 'admin', displayName: '管理员', role: 'admin' }),
      )
      setActivePinia(createPinia())
      installFetch()
    })

    it('渲染左 rail 四分区、统计卡与账号列表', async () => {
      const wrapper = mount(UsersView)
      await flushPromises()
      await vi.waitFor(() => expect(wrapper.findAll('.rail-item')).toHaveLength(4))

      expect(wrapper.find('.users-rail').exists()).toBe(true)
      expect(wrapper.find('.users-content').exists()).toBe(true)
      expect(wrapper.findAll('.rail-item').map((item) => item.text())).toEqual([
        expect.stringContaining('用户管理'),
        expect.stringContaining('角色与权限'),
        expect.stringContaining('授权委派'),
        expect.stringContaining('API Token'),
      ])
      expect(wrapper.findAll('.stat')).toHaveLength(4)
      expect(wrapper.text()).toContain('创建用户')
      expect(wrapper.text()).toContain('alice')
      expect(wrapper.text()).not.toContain('password')
    })

    it('切换到角色分区展示权限矩阵', async () => {
      const wrapper = mount(UsersView)
      await flushPromises()
      await vi.waitFor(() => expect(wrapper.findAll('.rail-item')).toHaveLength(4))

      const roleButton = wrapper.findAll('.rail-item')[1]!
      await roleButton.trigger('click')
      await flushPromises()

      expect(wrapper.text()).toContain('角色与权限矩阵')
      expect(wrapper.findAll('.role-card')).toHaveLength(3)
      expect(wrapper.text()).toContain('全量权限 *')
      expect(wrapper.text()).toContain('cluster.manage')
    })

    it('切换到授权委派分区展示委派列表与有效期', async () => {
      const wrapper = mount(UsersView)
      await flushPromises()
      await vi.waitFor(() => expect(wrapper.findAll('.rail-item')).toHaveLength(4))

      await wrapper.findAll('.rail-item')[2]!.trigger('click')
      await flushPromises()

      expect(wrapper.text()).toContain('授权委派')
      expect(wrapper.text()).toContain('alice')
      expect(wrapper.text()).toContain('ns-a / ns-b')
      expect(wrapper.text()).toContain('撤销')
    })

    it('切换到 Token 分区展示用户选择与已签发 Token', async () => {
      const wrapper = mount(UsersView)
      await flushPromises()
      await vi.waitFor(() => expect(wrapper.findAll('.rail-item')).toHaveLength(4))
      await vi.waitFor(() => expect(wrapper.findAll('.stat')).toHaveLength(4))

      await wrapper.findAll('.rail-item')[3]!.trigger('click')
      await flushPromises()
      await vi.waitFor(() => expect(wrapper.text()).toContain('签发 Token'))

      expect(wrapper.find('.user-select').exists()).toBe(true)
      expect(wrapper.text()).toContain('cli')

      const issueButton = wrapper.findAll('button').find((button) => button.text().includes('签发 Token'))
      expect(issueButton).toBeDefined()
      await issueButton!.trigger('click')
      await flushPromises()

      expect(wrapper.text()).toContain('Token 名称')
      expect(wrapper.text()).toContain('签发')
    })
  })

  describe('无权限角色视图', () => {
    beforeEach(() => {
      localStorage.setItem(TOKEN_KEY, 'tok-test')
      localStorage.setItem(
        USER_KEY,
        JSON.stringify({ userId: 'u-viewer', username: 'viewer', displayName: '只读', role: 'viewer' }),
      )
      setActivePinia(createPinia())
      vi.stubGlobal(
        'fetch',
        vi.fn(async () => {
          throw new Error('no managed endpoint should be called')
        }),
      )
    })

    it('只展示空态，不渲染任何管理入口', async () => {
      const wrapper = mount(UsersView)
      await flushPromises()

      expect(wrapper.find('.users-rail').exists()).toBe(false)
      expect(wrapper.find('.stat-grid').exists()).toBe(false)
      expect(wrapper.text()).toContain('无平台管理权限')
      expect(wrapper.text()).toContain('user.manage')
      expect(wrapper.text()).not.toContain('创建用户')
    })
  })
})
