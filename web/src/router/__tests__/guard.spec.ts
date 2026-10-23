import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import router from '@/router'

async function goto(path: string): Promise<string> {
  await router.push(path)
  await router.isReady()
  return router.currentRoute.value.fullPath
}

describe('路由守卫（P2.2 未登录不可访问业务页）', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('未登录访问业务页跳登录并带上回跳地址', async () => {
    const path = await goto('/app/approval')
    expect(path).toContain('/login')
    expect(path).toContain('redirect=')
    expect(decodeURIComponent(path)).toContain('/app/approval')
  })

  it('已登录访问 /login 回到聊天页，业务页保持可达', async () => {
    localStorage.setItem(TOKEN_KEY, 'tok-1')
    localStorage.setItem(USER_KEY, JSON.stringify({ userId: 'u1', username: 'admin', role: 'admin' }))
    setActivePinia(createPinia())

    expect(await goto('/login')).toBe('/app/chat')
    expect(await goto('/app/settings')).toBe('/app/settings')
  })

  it.each(['user', 'operator'])('%s 不能进入配置中心与用户权限页', async (role) => {
    localStorage.setItem(TOKEN_KEY, 'tok-1')
    localStorage.setItem(USER_KEY, JSON.stringify({ userId: 'u1', username: role, role }))
    setActivePinia(createPinia())

    await goto('/app/chat')
    expect(await goto('/app/settings')).toBe('/app/chat')
    expect(await goto('/app/users')).toBe('/app/chat')
  })

  it('未登录访问登录页与根路径不循环跳转', async () => {
    expect(await goto('/login')).toContain('/login')
    expect(await goto('/')).toContain('/login')
  })
})
