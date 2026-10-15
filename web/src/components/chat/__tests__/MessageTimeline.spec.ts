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

/**
 * ADR-032 / D-22：回合聚合契约（与后端生命周期同源，前端必须自洽）。
 * 这里锁的是三个真实发生过的回归：
 *   1. 分片 index 参与展示 key → 30 个分片渲染出 30 块报告（事件平铺）；
 *   2. `turn.completed.output` 单独成块 + `message.completed` 再建一块 → 同一份报告出现 3 次；
 *   3. 终态之后到达的迟到分片被当成新块 → 永久停在「正在生成…」。
 */
const FULL_REPORT = [
  '## default 命名空间巡检报告',
  '',
  '**结论**：发现 3 个异常项',
  '',
  '1. 优先处理 nginx-7d9f8',
].join('\n')

function mountTimeline(props: {
  history?: HistorySnapshot | null
  events?: AgentEvent[]
  canDecide?: boolean
}) {
  return mount(MessageTimeline, {
    props: { history: props.history ?? null, events: props.events ?? [], ...(props.canDecide === undefined ? {} : { canDecide: props.canDecide }) },
  })
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

  it('终态早于末段正文时，仍然只有一块报告且不残留「正在生成…」', () => {
    const wrapper = mountTimeline({
      events: [
        event('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
        event('reason.started', { kind: 'reason', status: 'running' }),
        event('message.delta', { role: 'reasoning', delta: '先看 Pod 列表', index: 0 }),
        event('reason.completed', { kind: 'reason', status: 'done' }),
        event('turn.completed', { status: 'done', output: FULL_REPORT, token: { total_tokens: 128 } }),
        event('session.completed', { state: 'done' }),
        event('message.completed', { role: 'assistant', delta: FULL_REPORT }),
        // 迟到分片（旧后端会把增量排在终态之后）：必须被忽略而不是新建一块
        event('message.delta', { role: 'assistant', delta: FULL_REPORT, index: 0 }),
      ],
    })

    expect(wrapper.findAll('.report-block')).toHaveLength(1)
    expect(wrapper.findAll('.report-hint')).toHaveLength(0)
    expect(wrapper.findAll('.md-body')).toHaveLength(1)
    // 权威全文只出现一次（不拼接、不重复成两份）
    expect(wrapper.get('.md-body').text().split('发现 3 个异常项')).toHaveLength(2)
  })

  it('30 个分片聚合成一块，并由 message.completed 全文覆盖', () => {
    const fragments = Array.from({ length: 30 }, (_, i) => `第${i}段内容`)
    const wrapper = mountTimeline({
      events: [
        event('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
        ...fragments.map((delta, index) => event('message.delta', { role: 'assistant', delta, index })),
        event('turn.completed', { status: 'done', output: FULL_REPORT }),
        event('message.completed', { role: 'assistant', delta: FULL_REPORT }),
        event('session.completed', { state: 'done' }),
      ],
    })

    expect(wrapper.findAll('.report-block')).toHaveLength(1)
    const text = wrapper.get('.md-body').text()
    expect(text).toContain('发现 3 个异常项')
    // 覆盖而不是把 30 个分片拼在权威全文前面
    expect(text).not.toContain('第0段内容')
  })

  it('思考增量早于 reason.started 也会被保留（不出现空面板 + 有正文面板两个）', () => {
    const wrapper = mountTimeline({
      events: [
        event('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
        event('message.delta', { role: 'reasoning', delta: '先诊断 nginx', index: 0 }),
        event('message.delta', { role: 'reasoning', delta: '，再汇总报告', index: 1 }),
        event('reason.started', { kind: 'reason', status: 'running' }),
        event('reason.completed', { kind: 'reason', status: 'done' }),
        event('message.completed', { role: 'assistant', delta: FULL_REPORT }),
      ],
    })

    expect(wrapper.findAll('.reasoning')).toHaveLength(1)
    expect(wrapper.get('.reasoning').text()).toContain('先诊断 nginx，再汇总报告')
  })

  it('没有思考增量时不渲染空的思考面板', () => {
    const wrapper = mountTimeline({
      events: [
        event('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
        event('reason.started', { kind: 'reason', status: 'running' }),
        event('reason.completed', { kind: 'reason', status: 'done' }),
        event('message.completed', { role: 'assistant', delta: FULL_REPORT }),
      ],
    })

    expect(wrapper.findAll('.report-block')).toHaveLength(1)
    expect(wrapper.findAll('.reasoning')).toHaveLength(0)
  })

  it('刷新后的历史快照保持「思考 → 工具 → 思考 → 报告」的顺序与分组', () => {
    const wrapper = mountTimeline({
      history: history([
        { role: 'user', type: 'text', content: '巡检 default 命名空间', timestamp: '2026-10-14T23:30:00.000Z' },
        { role: 'assistant', type: 'reasoning', content: '先看 Pod 列表', timestamp: '2026-10-14T23:30:01.000Z' },
        {
          role: 'tool',
          type: 'tool-call-response',
          name: 'get_pods',
          content: '3 pods',
          ok: true,
          arguments: { namespace: 'default' },
          timestamp: '2026-10-14T23:30:02.000Z',
        },
        { role: 'assistant', type: 'reasoning', content: '汇总异常项', timestamp: '2026-10-14T23:30:03.000Z' },
        { role: 'assistant', type: 'text', content: FULL_REPORT, timestamp: '2026-10-14T23:30:04.000Z' },
      ]),
    })

    // 思考与工具必须保持交错顺序，而不是被合并成一块塞在报告之前
    const kinds = wrapper.findAll('.turn-wrap:last-child .turn-items > *').map((el) => {
      const cls = el.classes()
      if (cls.includes('reasoning')) return 'reasoning'
      if (cls.includes('toolcard')) return 'tool'
      if (cls.includes('report-block')) return 'report'
      return cls.join('|')
    })
    expect(kinds).toEqual(['reasoning', 'tool', 'reasoning', 'report'])

    // 每轮一条思考消息 → 两个独立面板，内容不串
    const panels = wrapper.findAll('.turn-wrap:last-child .reasoning').map((panel) => panel.text())
    expect(panels).toHaveLength(2)
    expect(panels[0]).toContain('先看 Pod 列表')
    expect(panels[1]).toContain('汇总异常项')
  })
})
