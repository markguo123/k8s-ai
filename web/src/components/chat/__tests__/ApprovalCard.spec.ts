import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import ApprovalCard, { type ApprovalUiData } from '@/components/chat/ApprovalCard.vue'

function approval(overrides: Partial<ApprovalUiData> = {}): ApprovalUiData {
  return {
    key: 'a1',
    prompt: '将把 Deployment/web 的内存 limit 调整为 1Gi，是否执行？',
    options: [
      { label: '确认并执行', value: 'approve' },
      { label: '本次会话都允许', value: 'always' },
    ],
    secret: false,
    decision: 'pending',
    tool: 'create_resource',
    risk: 'HIGH',
    summary: 'mode=dry_run 提升内存 limit 1Gi',
    resource: { kind: 'Deployment', name: 'web', namespace: 'default' },
    ...overrides,
  }
}

describe('ApprovalCard（P2.9 审批卡）', () => {
  it('展示风险/工具/参数摘要/目标资源', () => {
    const wrapper = mount(ApprovalCard, { props: { approval: approval() } })
    expect(wrapper.text()).toContain('高风险')
    expect(wrapper.text()).toContain('create_resource')
    expect(wrapper.text()).toContain('mode=dry_run')
    expect(wrapper.text()).toContain('Deployment · web · default')
  })

  it('审批通过/拒绝各自回传选择值，不影响会话内其他状态', async () => {
    const wrapper = mount(ApprovalCard, { props: { approval: approval() } })
    const buttons = wrapper.findAll('button')
    await buttons[0].trigger('click')
    expect(wrapper.emitted('approve')?.[0]).toEqual(['approve'])

    const rejectButton = wrapper.findAll('button').find((btn) => btn.text() === '拒绝')
    await rejectButton!.trigger('click')
    expect(wrapper.emitted('reject')).toHaveLength(1)
  })

  it('无权用户：审批按钮整体隐藏并给出原因（后端仍强校验）', () => {
    const wrapper = mount(ApprovalCard, { props: { approval: approval(), canDecide: false } })
    expect(wrapper.findAll('button')).toHaveLength(0)
    expect(wrapper.text()).toContain('无审批权限')
  })

  it('已决策（通过/拒绝）后不再展示操作按钮', () => {
    const approved = mount(ApprovalCard, { props: { approval: approval({ decision: 'approved' }) } })
    expect(approved.text()).toContain('已通过')
    expect(approved.findAll('button')).toHaveLength(0)

    const rejected = mount(ApprovalCard, { props: { approval: approval({ decision: 'rejected' }) } })
    expect(rejected.text()).toContain('已拒绝')
  })

  it('白名单自动通过显示 AUTO 徽标且无可点按钮', () => {
    const wrapper = mount(ApprovalCard, { props: { approval: approval({ decision: 'auto' }) } })
    expect(wrapper.text()).toContain('AUTO')
    expect(wrapper.findAll('button')).toHaveLength(0)
  })

  it('Secret 占位输入：值只随选择值提交一次，不写进 DOM 文本', async () => {
    const wrapper = mount(ApprovalCard, {
      props: {
        approval: approval({
          secret: true,
          options: [{ label: '确认', value: 'secret:$SECRET' }],
        }),
      },
    })
    const input = wrapper.find('input')
    await input.setValue('s3cr3t-value')
    const confirm = wrapper.findAll('button').find((btn) => btn.text() === '确认')
    await confirm!.trigger('click')

    expect(wrapper.emitted('approve')?.[0]).toEqual(['secret:s3cr3t-value'])
    expect(wrapper.html()).not.toContain('s3cr3t-value')
  })
})
