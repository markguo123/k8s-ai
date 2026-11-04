/**
 * 登录页样式必须待在 `.login-ep` 里（P9.15）。
 *
 * login-tech.css 是 LoginView 用 import 引入的：登录页一访问就以全局 <style> 注入，而且排在
 * tech-theme.css 之后。它曾经带着裸 `.card` 和 `:root{--bg…}` 泄漏到整站——报告页、集群页、
 * 报告详情页的每张 .card 被压成 420px 宽、文字竖排，应用色板也被登录页那套取值盖掉。
 * 这里是源码级断言：任何一条不以 .login-ep 开头的选择器都不许再进来。
 */
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

/**
 * vitest 配了 css:false，CSS 导入会被 stub 成空串；jsdom 下 import.meta.url 也不是 file URL，
 * 所以这里按 vitest root（web/）解析真实文件路径读源码。
 */
const cssPath = path.resolve(process.cwd(), 'src/styles/login-tech.css')
const css = readFileSync(cssPath, 'utf8')

/** 取出规则选择器；@keyframes 内部的 from/to/百分比不参与断言。 */
function ruleSelectors(source: string): string[] {
  const stripped = source.replace(/\/\*[\s\S]*?\*\//g, '')
  const out: string[] = []
  let i = 0
  for (;;) {
    const brace = stripped.indexOf('{', i)
    if (brace < 0) return out
    let depth = 1
    let j = brace + 1
    while (j < stripped.length && depth > 0) {
      if (stripped[j] === '{') depth++
      else if (stripped[j] === '}') depth--
      j++
    }
    const head = stripped.slice(i, brace).trim()
    const body = stripped.slice(brace + 1, j - 1)
    if (head.startsWith('@keyframes')) {
      // 关键帧只贡献动画名，不贡献选择器
    } else if (/^@(media|supports)/.test(head)) {
      out.push(...ruleSelectors(body))
    } else if (!head.startsWith('@')) {
      out.push(
        ...head
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
      )
    }
    i = j
  }
}

describe('登录页样式作用域（login-tech.css）', () => {
  it('每条规则都限定在 .login-ep 之内', () => {
    const offenders = ruleSelectors(css).filter((sel) => !sel.startsWith('.login-ep'))
    expect(offenders).toEqual([])
    expect(ruleSelectors(css).length).toBeGreaterThan(30)
  })

  it('不覆盖应用级设计令牌，也不碰 html/body', () => {
    expect(css).not.toMatch(/:root\s*\{/)
    expect(css).not.toMatch(/(^|[\s,])html\s*[,{]/)
    expect(css).not.toMatch(/(^|[\s,])body\s*[,{]/)
  })

  it('关键帧与自定义属性带 lx- 前缀，不与站内其它动效同名', () => {
    const names = [...css.matchAll(/@keyframes\s+([\w-]+)/g)].map((m) => m[1])
    expect(names.length).toBeGreaterThan(0)
    expect(names.every((n) => n.startsWith('lx-'))).toBe(true)
    expect(css).not.toMatch(/--angle\b/)
  })
})
