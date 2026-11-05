import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import ComposerBox from '@/components/chat/ComposerBox.vue'

const SKILL = {
  name: 'k8s-mysql-troubleshoot',
  spec: {
    description: 'Kubernetes 上 MySQL 故障排查与诊断方法论',
    name: 'k8s-mysql-troubleshoot',
    type: 'procedural',
    domain: 'database',
    version: '1.0.0',
    kinds: [],
    risk: { default: 'medium' },
    source: 'custom',
  },
  source: 'custom',
  enabled: true,
  hitCount: 0,
  createdAt: '',
  updatedAt: '',
}

describe('ComposerBox（P2.10 输入与发送）', () => {
  it('展示当前模型与 Secret 策略提示（对齐原型工具条）', () => {
    const wrapper = mount(ComposerBox, {
      props: { modelValue: '', model: 'qwen-plus', secretPolicy: 'create_only' },
    })
    expect(wrapper.text()).toContain('qwen-plus')
    expect(wrapper.text()).toContain('Secret: create_only')
    // P7.2：长安全文案降噪为小字入口，完整说明保留在 aria-label（Tooltip 同源）
    expect(wrapper.text()).toContain('安全策略')
    expect(wrapper.get('.composer-hint').attributes('aria-label')).toContain(
      'Secret 值与 kubeconfig 不会进入会话与审计',
    )
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

  it('P3.8.4：输入 @ 弹出已启用 procedural Skill，选中后回填 @技能名', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(JSON.stringify({ data: [SKILL] }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
      ),
    )
    const wrapper = mount(ComposerBox, {
      props: { modelValue: '', canUseSkills: true },
      attachTo: document.body,
    })
    await vi.waitFor(() => expect(wrapper.vm.availableSkills.length).toBe(1))

    const textarea = wrapper.find('textarea')
    await textarea.setValue('@k8s-mysql')
    expect(wrapper.find('.mention-popup').exists()).toBe(true)
    expect(wrapper.find('.mention-name').text()).toContain('k8s-mysql-troubleshoot')

    await wrapper.find('.mention-item').trigger('mousedown')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toContain('@k8s-mysql-troubleshoot ')
    expect(wrapper.find('.mention-popup').exists()).toBe(false)
    vi.unstubAllGlobals()
  })

  it('P3.8.4：无 skill.read 时不弹 @ 引用提示', async () => {
    const spy = vi.fn(async () => new Response('{}', { status: 200 }))
    vi.stubGlobal('fetch', spy)
    const wrapper = mount(ComposerBox, {
      props: { modelValue: '', canUseSkills: false },
      attachTo: document.body,
    })
    await wrapper.find('textarea').setValue('@x')
    expect(wrapper.find('.mention-popup').exists()).toBe(false)
    expect(spy).not.toHaveBeenCalled()
    vi.unstubAllGlobals()
  })
})
