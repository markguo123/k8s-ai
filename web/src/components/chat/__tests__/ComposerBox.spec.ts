import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ComposerBox from '@/components/chat/ComposerBox.vue'

describe('ComposerBox（P2.10 输入与发送）', () => {
  it('展示当前模型与 Secret 策略提示（对齐原型工具条）', () => {
    const wrapper = mount(ComposerBox, {
      props: { modelValue: '', model: 'qwen-plus', secretPolicy: 'create_only' },
    })
    expect(wrapper.text()).toContain('qwen-plus')
    expect(wrapper.text()).toContain('Secret: create_only')
    expect(wrapper.text()).toContain('Shell 语法命令需人工审批 · Secret 值不会进入会话与审计')
  })

  it('输入回写 modelValue，Enter 触发发送', async () => {
    const wrapper = mount(ComposerBox, { props: { modelValue: '' } })
    await wrapper.find('textarea').setValue('看一下 crashloop 的 pod')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['看一下 crashloop 的 pod'])

    await wrapper.setProps({ modelValue: '看一下 crashloop 的 pod' })
    await wrapper.find('textarea').trigger('keydown.enter')
    expect(wrapper.emitted('send')).toHaveLength(1)
  })

  it('空白输入或禁用态不发送', async () => {
    const empty = mount(ComposerBox, { props: { modelValue: '   ' } })
    await empty.find('textarea').trigger('keydown.enter')
    expect(empty.emitted('send')).toBeUndefined()

    const disabled = mount(ComposerBox, { props: { modelValue: '内容', disabled: true } })
    await disabled.find('textarea').trigger('keydown.enter')
    expect(disabled.emitted('send')).toBeUndefined()
  })

  it('运行中显示「停止」并回传 stop（每轮结束由父级复位）', async () => {
    const wrapper = mount(ComposerBox, { props: { modelValue: '继续', sending: true } })
    expect(wrapper.text()).not.toContain('发送')
    const stop = wrapper.findAll('button').find((btn) => btn.text().includes('停止'))
    await stop!.trigger('click')
    expect(wrapper.emitted('stop')).toHaveLength(1)
  })
})
