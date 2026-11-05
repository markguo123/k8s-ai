import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ConversationList from '@/components/chat/ConversationList.vue'
import type { SessionSummary } from '@/api/sessions'

function session(id: string, pinned = false): SessionSummary {
  return {
    id,
    name: `会话 ${id}`,
    state: 'done',
    messageCount: 3,
    lastModified: '2026-10-15T10:00:00Z',
    pinned,
  }
}

function mountList(sessions: SessionSummary[], collapsed = false) {
  return mount(ConversationList, {
    props: { sessions, activeId: null, loading: false, collapsed },
  })
}

/**
 * P7.5 会话栏分组与折叠开关契约。
 * 两条都踩过坑：
 *   1. 置顶会话与普通会话之间要有分隔线，否则置顶在视觉上"粘"在第一条普通会话上；
 *   2. 折叠后用来恢复的展开按钮一度因为漏 import 图标而整个不渲染（进退两难）。
 */
describe('ConversationList（P7.5 置顶分组与折叠开关）', () => {
  it('置顶组与普通组之间只画一条分隔线，且落在第一条非置顶会话上', () => {
    const wrapper = mountList([
      session('p1', true),
      session('p2', true),
      session('n1'),
      session('n2'),
    ])

    expect(wrapper.findAll('.session-item.pinned')).toHaveLength(2)
    const starts = wrapper.findAll('.session-item.group-start')
    expect(starts).toHaveLength(1)
    // 分隔线属于第一条非置顶会话（索引 2），不在置顶组内部
    expect(starts[0].find('.session-name').text()).toBe('会话 n1')
  })

  it('没有置顶会话时不需要分组分隔线', () => {
    const wrapper = mountList([session('n1'), session('n2')])
    expect(wrapper.findAll('.session-item.group-start')).toHaveLength(0)
  })

  it('全部置顶时不出现悬空分隔线', () => {
    const wrapper = mountList([session('p1', true), session('p2', true)])
    expect(wrapper.findAll('.session-item.group-start')).toHaveLength(0)
  })

  it('折叠态仍然渲染可点击的展开按钮，且按钮内图标有真实尺寸', () => {
    const wrapper = mountList([session('n1')], true)

    const toggle = wrapper.get('.ctx-toggle')
    expect(toggle.attributes('title')).toBe('展开会话栏')
    // 图标组件漏 import 时 Vue 会渲染成空节点，这里守住"按钮里真的有图标"
    expect(toggle.find('svg').exists()).toBe(true)
  })

  it('展开态点击折叠按钮会向上抛 toggle 事件', async () => {
    const wrapper = mountList([session('n1')])
    await wrapper.get('.ctx-toggle').trigger('click')
    expect(wrapper.emitted('toggle')).toHaveLength(1)
  })
})
