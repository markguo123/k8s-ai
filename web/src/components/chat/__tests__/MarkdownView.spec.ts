import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import MarkdownView from '../MarkdownView.vue'

/**
 * P2.12 / ADR-032 / 设计方案 §7：报告渲染契约。
 *
 * 这里锁的是两个真实发生过的回归：
 *   1. `DOMPurify.sanitize(..., { USE_PROFILES: { html: false } })` 会把白名单收成只剩纯文本，
 *      报告退化成"源码裸奔"（`**` 与表格管道符原样上屏），且 `class="language-*"` 丢失；
 *   2. markdown 一旦真的产出标签，代码块包壳的 DOM 顺序错误会抛 HierarchyRequestError。
 */
const SAMPLE = [
  '## 巡检报告',
  '',
  '**时间**：2026-10-14 · `default`',
  '',
  '| 资源 | 状态 |',
  '| --- | --- |',
  '| nginx-7d9f8 | CrashLoopBackOff |',
  '',
  '1. 优先处理 `nginx-7d9f8`',
  '',
  '> 以下命令是写操作，需要人工确认',
  '',
  '```bash',
  'kubectl -n default logs nginx-7d9f8 --previous',
  '```',
].join('\n')

describe('MarkdownView', () => {
  it('把 markdown 渲染成真实标签，而不是塌成纯文本', () => {
    const wrapper = mount(MarkdownView, { props: { text: SAMPLE } })
    const body = wrapper.get('.md-body')

    for (const tag of ['h2', 'strong', 'code', 'table', 'th', 'td', 'ol', 'li', 'blockquote']) {
      expect(body.find(tag).exists(), `缺少结构标签 <${tag}>，markdown 可能被解包成纯文本`).toBe(true)
    }
    // 解析成功后，原始 markdown 记号不应再出现在可见文本里
    expect(body.text()).not.toContain('**')
    expect(body.text()).not.toContain('| --- |')
  })

  it('保留代码块语言标签并挂上复制按钮', () => {
    const wrapper = mount(MarkdownView, { props: { text: SAMPLE } })

    expect(wrapper.find('.md-code-wrap').exists()).toBe(true)
    expect(wrapper.get('.md-code-head span').text()).toBe('bash')
    expect(wrapper.get('.md-code-copy').text()).toBe('复制')
  })

  it('清洗不可信内容：script 与事件属性不落 DOM', () => {
    const hostile = ['<script>alert(1)</script>', '', '<img src=x onerror="alert(2)">', '', '[链接](javascript:alert(3))'].join('\n')
    const wrapper = mount(MarkdownView, { props: { text: hostile } })
    const html = wrapper.get('.md-body').html()

    expect(html).not.toContain('<script')
    expect(html.toLowerCase()).not.toContain('onerror')
    expect(html.toLowerCase()).not.toContain('javascript:')
  })
})
