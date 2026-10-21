import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import SettingsView from '@/views/settings/SettingsView.vue'

/**
 * P6.13 配置中心布局回归：左 rail + 全宽内容。
 *
 * 长分组必须有独立的内容滚动容器（`.settings-content`），表头单独成块
 * （`.form-head`）以便滚动时吸顶；只靠外层 `.app-main` 滚动会让左侧分组
 * 随页面一起滚走，且长配置项看不全。
 */

const ITEMS = Array.from({ length: 26 }, (_, i) => ({
  key: `demo.key_${i}`,
  kind: i % 3 === 0 ? 'bool' : i % 3 === 1 ? 'string' : 'int',
  value: i % 3 === 0 ? true : i % 3 === 1 ? `value-${i}` : i,
  hasValue: true,
  source: 'demo',
  desc: `演示配置项 ${i}`,
}))

function installFetch(): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(
      JSON.stringify({
        request_id: 'req-test',
        timestamp: new Date().toISOString(),
        data: { group: 'model', items: ITEMS },
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )),
  )
}

enableAutoUnmount(afterEach)

describe('SettingsView 布局（P6.13 左 rail + 全宽内容）', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem(TOKEN_KEY, 'tok-test')
    localStorage.setItem(
      USER_KEY,
      JSON.stringify({ userId: 'u1', username: 'admin', displayName: '管理员', role: 'admin' }),
    )
    setActivePinia(createPinia())
    installFetch()
  })

  it('渲染左右分区，长分组全部字段位于独立内容容器内', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    const layout = wrapper.find('.settings-layout')
    expect(layout.exists()).toBe(true)
    expect(layout.find('.settings-rail').exists()).toBe(true)
    expect(layout.find('.settings-content').exists()).toBe(true)

    const content = layout.find('.settings-content')
    expect(content.findAll('.field')).toHaveLength(ITEMS.length)
    expect(content.find('.form-head').exists()).toBe(true)
    expect(content.find('.form-head .el-button').text()).toContain('保存')
  })

  it('每个配置项占满内容区一整行，信息与控件左右分区', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    const fields = wrapper.findAll('.field')
    for (const field of fields) {
      expect(field.find('.field-meta').exists()).toBe(true)
      expect(field.find('.field-control').exists()).toBe(true)
      expect(field.find('.field-control .el-input, .field-control .el-switch, .field-control .el-input-number').exists()).toBe(true)
    }
  })

  it('切换分组时仍复用同一内容滚动容器', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    const railItems = wrapper.findAll('.rail-item')
    expect(railItems.length).toBeGreaterThan(1)
    await railItems[1]!.trigger('click')
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    expect(wrapper.findAll('.settings-content')).toHaveLength(1)
    expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length)
  })
})
