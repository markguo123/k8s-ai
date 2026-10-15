import { describe, expect, it } from 'vitest'

import type { ChatMessage } from '@/api/sessions'
import { buildSessionMarkdown, markdownFileName } from '../exportMarkdown'

const messages: ChatMessage[] = [
  { role: 'user', type: 'text', content: 'web 为什么不稳', timestamp: '2026-10-15T10:00:00Z' },
  { role: 'assistant', type: 'reasoning', content: '先看 Pod 列表', timestamp: '2026-10-15T10:00:01Z' },
  {
    role: 'tool',
    type: 'tool-call-response',
    name: 'get_pods',
    ok: true,
    content: '12 个 Pod，其中 1 个 CrashLoopBackOff',
    timestamp: '2026-10-15T10:00:02Z',
  },
  { role: 'assistant', type: 'text', content: '## 结论\n\nnginx 崩溃重启', timestamp: '2026-10-15T10:00:03Z' },
]

describe('buildSessionMarkdown（P7.5 导出）', () => {
  it('按 提问 → 思考 → 工具 → 报告 的顺序导出，并带上会话元信息', () => {
    const md = buildSessionMarkdown(messages, {
      id: '20261015-c851',
      name: 'default 巡检',
      model: 'qwen-plus',
      exportedAt: new Date('2026-10-15T12:00:00Z'),
    })

    const order = ['# default 巡检', '## 提问', '## 思考过程', '## 工具调用 · get_pods', '## 最终报告']
    let cursor = -1
    for (const marker of order) {
      const at = md.indexOf(marker)
      expect(at, `缺少 ${marker}`).toBeGreaterThan(-1)
      expect(at, `${marker} 顺序错误`).toBeGreaterThan(cursor)
      cursor = at
    }
    expect(md).toContain('- 会话 ID：`20261015-c851`')
    expect(md).toContain('- 模型：`qwen-plus`')
    expect(md).toContain('12 个 Pod，其中 1 个 CrashLoopBackOff')
  })

  it('工具失败时标注状态，且不导出原始参数/数据字段', () => {
    const md = buildSessionMarkdown(
      [
        {
          role: 'tool',
          type: 'tool-call-response',
          name: 'kubectl',
          ok: false,
          content: 'forbidden',
          arguments: { token: 'should-not-appear' },
          data: { secret: 'should-not-appear' },
          timestamp: '2026-10-15T10:00:02Z',
        },
      ],
      { id: 's1' },
    )
    expect(md).toContain('- 状态：失败')
    expect(md).not.toContain('should-not-appear')
  })
})

describe('markdownFileName', () => {
  it('生成 k8s-ai-<会话名>-<时间>.md 并过滤非法字符', () => {
    expect(markdownFileName('default 巡检 / prod:01', new Date('2026-10-15T09:05:00'))).toBe(
      'k8s-ai-default-巡检-prod-01-20261015-0905.md',
    )
    expect(markdownFileName('', new Date('2026-10-15T09:05:00'))).toBe('k8s-ai-session-20261015-0905.md')
  })
})
