import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ReasoningPanel from '@/components/chat/ReasoningPanel.vue'

describe('ReasoningPanel（P2.7 思考框：推理中展开、报告后折叠）', () => {
  it('推理进行中自动展开并展示推理文本', async () => {
    const wrapper = mount(ReasoningPanel, {
      props: { text: '先看 Pod 状态', streaming: true },
    })
    expect(wrapper.classes()).toContain('open')
    expect(wrapper.text()).toContain('推理中…')
    expect(wrapper.text()).toContain('先看 Pod 状态')
  })

  it('推理结束后自动折叠为一行摘要（内容不丢）', async () => {
    const wrapper = mount(ReasoningPanel, {
      props: { text: '先看 Pod 状态，再看事件', streaming: true },
    })
    await wrapper.setProps({ streaming: false })

    expect(wrapper.classes()).not.toContain('open')
    expect(wrapper.text()).toContain('先看 Pod 状态，再看事件')
    expect(wrapper.find('.reasoning-body').exists()).toBe(false)
  })

  it('用户手动展开后保持展开，不被流式状态覆盖', async () => {
    const wrapper = mount(ReasoningPanel, {
      props: { text: '思考', streaming: false },
    })
    await wrapper.find('.reasoning-head').trigger('click')
    expect(wrapper.find('.reasoning-body').exists()).toBe(true)

    await wrapper.setProps({ streaming: true })
    await wrapper.setProps({ streaming: false })
    expect(wrapper.find('.reasoning-body').exists()).toBe(true)
    expect(wrapper.text()).toContain('不进入最终报告')
  })

  it('推理内容为空时展示占位而不是空白面板', () => {
    const wrapper = mount(ReasoningPanel, { props: { text: '', streaming: true } })
    expect(wrapper.text()).toContain('正在思考…')
  })
})
