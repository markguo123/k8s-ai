import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import TechEmptyState from '@/components/TechEmptyState.vue'

/**
 * P7.6 空状态插画契约：标题/引导/紧凑尺寸/操作插槽。
 * 三处（会话列表、聊天画布、消息时间线）共用同一块"空"的视觉与语义，
 * 因此这里锁死对外契约，避免以后各页又各写一套灰底占位。
 */
describe('TechEmptyState', () => {
  it('渲染标题与引导文案，插画对辅助技术隐藏、容器标注为 status', () => {
    const wrapper = mount(TechEmptyState, {
      props: { title: '未选择会话', hint: '新建一个会话，开始与 Agent 对话' },
    })

    expect(wrapper.attributes('role')).toBe('status')
    expect(wrapper.get('.tech-empty-title').text()).toBe('未选择会话')
    expect(wrapper.get('.tech-empty-hint').text()).toBe('新建一个会话，开始与 Agent 对话')
    expect(wrapper.get('.tech-empty-art').attributes('aria-hidden')).toBe('true')
    expect(wrapper.classes()).not.toContain('is-compact')
  })

  it('未给引导文案时不渲染提示行', () => {
    const wrapper = mount(TechEmptyState, { props: { title: '暂无会话' } })
    expect(wrapper.find('.tech-empty-hint').exists()).toBe(false)
  })

  it('compact 用紧凑插画（会话列表是窄容器）', () => {
    const wrapper = mount(TechEmptyState, { props: { title: '暂无会话', compact: true } })
    expect(wrapper.classes()).toContain('is-compact')
  })

  it('操作按钮走默认插槽，没有插槽时不渲染动作区', () => {
    const bare = mount(TechEmptyState, { props: { title: '暂无会话' } })
    expect(bare.find('.tech-empty-actions').exists()).toBe(false)

    const withAction = mount(TechEmptyState, {
      props: { title: '未选择会话' },
      slots: { default: '<button type="button">新建会话</button>' },
    })
    expect(withAction.get('.tech-empty-actions button').text()).toBe('新建会话')
  })
})
