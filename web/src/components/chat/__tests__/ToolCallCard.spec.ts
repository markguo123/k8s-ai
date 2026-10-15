import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ToolCallCard, { type ToolUiData } from '@/components/chat/ToolCallCard.vue'
import { MASKED_VALUE } from '@/components/chat/render/constants'

function tool(overrides: Partial<ToolUiData> = {}): ToolUiData {
  return {
    key: 't1',
    name: 'get_pod',
    shortName: 'get_pod',
    command: 'kubectl get pod web -n default -o yaml',
    risk: 'SAFE',
    status: 'done',
    ok: true,
    params: { namespace: 'default' },
    result: { name: 'web', phase: 'Running' },
    raw: '{"name":"web","phase":"Running"}',
    duration: 610,
    ...overrides,
  }
}

describe('ToolCallCard（P2.8 工具卡）', () => {
  it('已完成的历史工具卡默认展开并展示结果（刷新后无需再点一次）', () => {
    const wrapper = mount(ToolCallCard, { props: { tool: tool() } })
    expect(wrapper.find('.toolcard-result').exists()).toBe(true)
    expect(wrapper.text()).toContain('610ms')
    expect(wrapper.text()).toContain('SAFE')
  })

  it('结果到达（running → done）后自动展开并保持', async () => {
    const wrapper = mount(ToolCallCard, {
      props: { tool: tool({ status: 'running', result: null, raw: '' }) },
    })
    expect(wrapper.find('.toolcard-running').exists()).toBe(true)

    await wrapper.setProps({ tool: tool({ status: 'done' }) })
    expect(wrapper.find('.toolcard-result').exists()).toBe(true)
  })

  it('失败工具卡展示错误摘要而不是渲染结果表', () => {
    const wrapper = mount(ToolCallCard, {
      props: { tool: tool({ status: 'error', ok: false, error: 'namespace not found' }) },
    })
    expect(wrapper.text()).toContain('namespace not found')
    expect(wrapper.find('.toolcard-error').exists()).toBe(true)
  })

  it('凭据兜底：Secret 值不进入卡片（含原始 JSON），只在键名处保留结构', async () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        tool: tool({
          name: 'get_secret',
          shortName: 'get_secret',
          result: { name: 'db', password: 'p@ssw0rd', data: { apiKey: 'sk-live-1' } },
          raw: JSON.stringify({ name: 'db', password: 'p@ssw0rd', apiKey: 'sk-live-1' }),
        }),
      },
    })
    expect(wrapper.html()).not.toContain('p@ssw0rd')
    expect(wrapper.html()).not.toContain('sk-live-1')

    await wrapper.find('.raw-link').trigger('click')
    const raw = wrapper.find('.toolcard-rawpre').text()
    expect(raw).not.toContain('p@ssw0rd')
    expect(raw).not.toContain('sk-live-1')
    expect(raw).toContain('password')
    expect(raw).toContain(MASKED_VALUE)
  })

  it('对象字段不会渲染成 [object Object]', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        tool: tool({
          result: { items: [{ name: 'web-1', phase: 'Running' }], total: 1 },
          raw: '',
        }),
      },
    })
    expect(wrapper.html()).not.toContain('[object Object]')
  })

  it('create_resource 两段式：展示 manifest 与 dry-run/validated/applied 状态', () => {
    const wrapper = mount(ToolCallCard, {
      props: {
        tool: tool({
          name: 'create_resource',
          shortName: 'create_resource',
          createResource: {
            manifest: 'apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web',
            dryRun: true,
            validated: true,
            applied: false,
          },
        }),
      },
    })
    expect(wrapper.text()).toContain('提交的 Manifest')
    expect(wrapper.text()).toContain('dry-run')
    expect(wrapper.text()).toContain('validated')
    expect(wrapper.text()).not.toContain('applied')
  })
})
