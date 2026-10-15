import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import MessageTimeline from '@/components/chat/MessageTimeline.vue'
import type { AgentEvent, HistorySnapshot } from '@/api/sse'

function history(messages: HistorySnapshot['messages']): HistorySnapshot {
  return { type: 'history', sessionId: 's1', state: 'done', messages, token: null }
}

function event(type: AgentEvent['event_type'], data: unknown, turnId = 'turn-1'): AgentEvent {
  return { event_type: type, turn_id: turnId, timestamp: '2026-10-15T10:00:00Z', data: data as AgentEvent['data'] }
}

describe('MessageTimeline（P2.6/P2.7 回合聚合）', () => {
  it('历史快照按用户消息切分为回合，工具/思考消息各归其位', () => {
    const wrapper = mount(MessageTimeline, {
      props: {
        history: history([
          { role: 'user', type: 'text', content: 'web 为什么不稳', timestamp: '2026-10-15T10:00:00Z' },
          { role: 'assistant', type: 'reasoning', content: '先看 Pod', timestamp: '2026-10-15T10:00:01Z' },
          {
            role: 'tool',
            type: 'tool-call-response',
            name: 'get_pod',
            ok: true,
            content: 'kubectl get pod web -n default',
            timestamp: '2026-10-15T10:00:02Z',
          },
          { role: 'assistant', type: 'text', content: '根因是 OOMKilled', timestamp: '2026-10-15T10:00:03Z' },
        ]),
        events: [],
      },
    })
    expect(wrapper.findAll('.turn')).toHaveLength(1)
    expect(wrapper.text()).toContain('web 为什么不稳')
    expect(wrapper.text()).toContain('先看 Pod')
    expect(wrapper.text()).toContain('根因是 OOMKilled')
    expect(wrapper.findComponent({ name: 'ToolCallCard' }).exists()).toBe(true)
  })

  it('实时事件按 turn_id 聚合：同一工具只渲染一张卡（started 与 observation 不重复）', async () => {
    const wrapper = mount(MessageTimeline, {
      props: {
        history: history([]),
        events: [
          event('turn.started', { input: '诊断 web' }),
          event('reason.started', { kind: 'reason' }),
          event('message.delta', { role: 'reasoning', delta: '看 Pod 状态' }),
          event('reason.completed', { kind: 'reason' }),
          event('tool.started', { kind: 'tool', tool: 'get_pod', params: '{"namespace":"default"}' }),
          event('observation.created', { tool: 'get_pod', result: '1 个 CrashLoopBackOff', ok: true }),
          event('tool.completed', { kind: 'tool', tool: 'get_pod', status: 'ok', result: '1 个 CrashLoopBackOff' }),
          event('message.delta', { role: 'assistant', delta: '结论：' }),
          event('message.delta', { role: 'assistant', delta: 'OOMKilled' }),
          event('turn.completed', {
            output: '结论：OOMKilled',
            token: { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 },
          }),
        ],
      },
    })
    expect(wrapper.findAll('.turn')).toHaveLength(1)
    expect(wrapper.findAllComponents({ name: 'ToolCallCard' })).toHaveLength(1)
    expect(wrapper.text()).toContain('1 个 CrashLoopBackOff')
    expect(wrapper.text()).toContain('OOMKilled')
    expect(wrapper.text()).toContain('已完成')
  })

  it('审批事件渲染审批卡；已决策后按钮消失', async () => {
    const wrapper = mount(MessageTimeline, {
      props: {
        history: history([]),
        events: [
          event('turn.started', { input: '修复它' }),
          event('approval.required', {
            prompt: '确认执行 dry-run 后的 apply？',
            options: [{ label: '确认并执行', value: 'approve' }],
            tool: 'create_resource',
            risk: 'HIGH',
          }),
        ],
      },
    })
    expect(wrapper.text()).toContain('审批请求')
    expect(wrapper.findAll('button').some((btn) => btn.text() === '确认并执行')).toBe(true)

    await wrapper.setProps({
      events: [
        event('turn.started', { input: '修复它' }),
        event('approval.required', {
          prompt: '确认执行 dry-run 后的 apply？',
          options: [{ label: '确认并执行', value: 'approve' }],
        }),
        event('approval.approved', { decision: 'approve' }),
      ],
    })
    expect(wrapper.text()).toContain('已通过')
  })

  it('只读角色（canDecide=false）下审批操作区收起', () => {
    const wrapper = mount(MessageTimeline, {
      props: {
        canDecide: false,
        history: history([]),
        events: [
          event('turn.started', { input: '修复它' }),
          event('approval.required', {
            prompt: '确认执行？',
            options: [{ label: '确认并执行', value: 'approve' }],
          }),
        ],
      },
    })
    expect(wrapper.text()).toContain('无审批权限')
  })

  it('回合失败时展示错误态，不伪造完成', () => {
    const wrapper = mount(MessageTimeline, {
      props: {
        history: history([]),
        events: [
          event('turn.started', { input: '创建资源' }),
          event('turn.completed', { error: '权限不足：namespaces "web" is forbidden' }),
        ],
      },
    })
    expect(wrapper.text()).toContain('失败')
    expect(wrapper.text()).toContain('权限不足')
  })
})
