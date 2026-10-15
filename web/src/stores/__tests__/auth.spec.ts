import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'

const authApi = {
  login: vi.fn(),
  logout: vi.fn(),
  me: vi.fn(),
}

vi.mock('@/api', () => ({ authApi }))

const { useAuthStore } = await import('@/stores/auth')

describe('auth store（P2.2/P2.3 真实认证接入）', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    authApi.login.mockReset()
    authApi.logout.mockReset()
    authApi.me.mockReset()
  })

  it('登录成功后写入令牌与用户，并以 /me 校正角色', async () => {
    authApi.login.mockResolvedValue({
      token: 'tok-1',
      user: { id: 'u1', username: 'admin', displayName: '管理员', roleId: 'role-admin' },
    })
    authApi.me.mockResolvedValue({
      user: { id: 'u1', username: 'admin' },
      role: { name: 'admin', permissions: ['*'] },
    })

    const auth = useAuthStore()
    await auth.login('admin', 'pw')

    expect(auth.isAuthenticated).toBe(true)
    expect(auth.role).toBe('admin')
    expect(auth.user?.displayName).toBe('管理员')
    expect(localStorage.getItem(TOKEN_KEY)).toBe('tok-1')
  })

  it('未知角色名落到最小权限角色 viewer，避免越权展示', async () => {
    authApi.login.mockResolvedValue({
      token: 'tok-2',
      user: { id: 'u2', username: 'ghost', displayName: '', roleId: 'mystery' },
    })
    authApi.me.mockResolvedValue({ user: { id: 'u2' }, role: { name: 'mystery' } })

    const auth = useAuthStore()
    await auth.login('ghost', 'pw')
    expect(auth.role).toBe('viewer')
  })

  it('restore 用本地会话 + /me 校正角色；无本地会话时清空', async () => {
    localStorage.setItem(TOKEN_KEY, 'tok-3')
    localStorage.setItem(USER_KEY, JSON.stringify({ userId: 'u3', username: 'op', role: 'viewer' }))
    authApi.me.mockResolvedValue({ user: { id: 'u3' }, role: { name: 'operator' } })

    const auth = useAuthStore()
    await auth.restore()
    expect(auth.isAuthenticated).toBe(true)
    expect(auth.role).toBe('operator')

    localStorage.clear()
    setActivePinia(createPinia())
    const fresh = useAuthStore()
    await fresh.restore()
    expect(fresh.isAuthenticated).toBe(false)
    expect(fresh.user).toBeNull()
  })

  it('登出清理本地会话', async () => {
    localStorage.setItem(TOKEN_KEY, 'tok-4')
    authApi.logout.mockResolvedValue(undefined)

    const auth = useAuthStore()
    await auth.logout()
    expect(auth.isAuthenticated).toBe(false)
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull()
  })
})
