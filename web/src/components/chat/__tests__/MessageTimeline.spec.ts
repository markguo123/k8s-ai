import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { describe, expect, it } from 'vitest'

import type { AgentEvent, AgentEventType } from '@/api/sse'
import MessageTimeline from '../MessageTimeline.vue'

/**
 * P2.12 / D-22 / ADR-032：回合聚合契约。
 *
 * 三个真实发生过的回归：
 *   1. 分片 index 参与展示 key → 30 个分片渲染出 30 块报告（事件平铺）；
 *   2. `turn.completed.output` 单独成块 + `message.completed` 再建一块 → 同一份报告出现 3 次；
 *   3. 终态之后到达的迟到分片被当成新块 → 永久停在"正在生成…"。
 */
const FULL = ['## default 命名空间巡检报告', '', '**结论**：发现 3 个异常项', '', '1. 优先处理 nginx-7d9f8'].join('\n')

function ev(eventType: AgentEventType, data: AgentEvent['data'], turnId = 'turn-1'): AgentEvent {
  return {
    session_id: 'sess-1',
    turn_id: turnId,
    step_id: '',
    timestamp: '2026-10-14T23:30:00.000Z',
    event_type: eventType,
    data,
  }
}

function mountTimeline(events: AgentEvent[]) {
  return mount(MessageTimeline, {
    props: { history: null, events },
    global: { plugins: [ElementPlus] },
  })
}

describe('MessageTimeline 回合聚合', () => {
  it('终态早于末段正文时，仍然只有一块报告且不残留"正在生成…"', () => {
    const wrapper = mountTimeline([
      ev('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
      ev('reason.started', { kind: 'reason', status: 'running' }),
      ev('message.delta', { role: 'reasoning', delta: '先看 Pod 列表', index: 0 }),
      ev('reason.completed', { kind: 'reason', status: 'done' }),
      ev('turn.completed', { status: 'done', output: FULL, token: { total_tokens: 128 } }),
      ev('session.completed', { state: 'done' }),
      ev('message.completed', { role: 'assistant', delta: FULL }),
      // 旧后端会把这个迟到分片放在终态之后（P0-3），前端必须忽略而不是新建一块
      ev('message.delta', { role: 'assistant', delta: FULL, index: 0 }),
    ])

    expect(wrapper.findAll('.report-block')).toHaveLength(1)
    expect(wrapper.findAll('.report-hint')).toHaveLength(0)
    expect(wrapper.findAll('.md-body')).toHaveLength(1)
    // 权威全文只出现一次（不拼接、不重复成两份）
    expect(wrapper.get('.md-body').text().split('发现 3 个异常项')).toHaveLength(2)
  })

  it('30 个分片聚合成一块，并由 message.completed 全文覆盖', () => {
    const fragments = Array.from({ length: 30 }, (_, i) => `第${i}段内容`)
    const events: AgentEvent[] = [
      ev('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
      ...fragments.map((delta, index) => ev('message.delta', { role: 'assistant', delta, index })),
      ev('turn.completed', { status: 'done', output: FULL }),
      ev('message.completed', { role: 'assistant', delta: FULL }),
      ev('session.completed', { state: 'done' }),
    ]
    const wrapper = mountTimeline(events)

    expect(wrapper.findAll('.report-block')).toHaveLength(1)
    const text = wrapper.get('.md-body').text()
    expect(text).toContain('发现 3 个异常项')
    // 覆盖而不是把 30 个分片拼在权威全文前面
    expect(text).not.toContain('第0段内容')
  })

  it('思考增量早于 reason.started 也会被保留，空思考面板不占位', () => {
    const wrapper = mountTimeline([
      ev('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
      // 真实后端顺序：reasoning 增量先到，reason.started 后到（后端已修，前端仍需容错）
      ev('message.delta', { role: 'reasoning', delta: '先诊断 nginx', index: 0 }),
      ev('message.delta', { role: 'reasoning', delta: '，再汇总报告', index: 1 }),
      ev('reason.started', { kind: 'reason', status: 'running' }),
      ev('reason.completed', { kind: 'reason', status: 'done' }),
      ev('message.completed', { role: 'assistant', delta: FULL }),
    ])

    expect(wrapper.findAll('.reasoning')).toHaveLength(1)
    expect(wrapper.get('.reasoning').text()).toContain('先诊断 nginx，再汇总报告')
  })

  it('没有思考增量时不渲染空的思考面板', () => {
    const wrapper = mountTimeline([
      ev('turn.started', { status: 'running', input: '巡检 default 命名空间' }),
      ev('reason.started', { kind: 'reason', status: 'running' }),
      ev('reason.completed', { kind: 'reason', status: 'done' }),
      ev('message.completed', { role: 'assistant', delta: FULL }),
    ])

    expect(wrapper.findAll('.report-block')).toHaveLength(1)
    expect(wrapper.findAll('.reasoning')).toHaveLength(0)
  })
})
