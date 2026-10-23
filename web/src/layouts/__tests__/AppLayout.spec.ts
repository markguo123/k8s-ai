import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import AppLayout from '@/layouts/AppLayout.vue'

const Blank = { template: '<div />' }

async function mountLayout(role: 'admin' | 'operator' | 'user') {
  localStorage.clear()
  localStorage.setItem(TOKEN_KEY, 'tok-test')
  localStorage.setItem(USER_KEY, JSON.stringify({ userId: `u-${role}`, username: role, role }))
  setActivePinia(createPinia())

  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['chat', 'cluster', 'skill', 'approval', 'audit', 'settings', 'users'].map((name) => ({
      path: `/app/${name}`,
      component: Blank,
    })),
  })
  await router.push('/app/chat')
  await router.isReady()
  return mount(AppLayout, { global: { plugins: [router] } })
}

describe('AppLayout 权限菜单', () => {
  it.each(['user', 'operator'] as const)('%s 不展示配置中心和用户权限', async (role) => {
    const wrapper = await mountLayout(role)
    const menu = wrapper.find('.app-menu').text()
    expect(menu).toContain('聊天')
    expect(menu).not.toContain('配置中心')
    expect(menu).not.toContain('用户与权限')
  })

  it('admin 展示完整平台管理菜单', async () => {
    const wrapper = await mountLayout('admin')
    const menu = wrapper.find('.app-menu').text()
    expect(menu).toContain('配置中心')
    expect(menu).toContain('用户与权限')
  })
})
