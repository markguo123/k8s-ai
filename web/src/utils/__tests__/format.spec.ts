import { describe, expect, it } from 'vitest'

import { addTokenUsage, formatRelativeTime, formatTokenCount } from '@/utils/format'

describe('formatRelativeTime', () => {
  it('按时间差给出中文相对时间，非法输入返回空串', () => {
    expect(formatRelativeTime(new Date(Date.now() - 30_000).toISOString())).toBe('刚刚')
    expect(formatRelativeTime(new Date(Date.now() - 5 * 60_000).toISOString())).toBe('5 分钟前')
    expect(formatRelativeTime(new Date(Date.now() - 3 * 3_600_000).toISOString())).toBe('3 小时前')
    expect(formatRelativeTime('not-a-date')).toBe('')
  })
})

describe('formatTokenCount', () => {
  it('千分位展示，空值用占位符而不是 0', () => {
    expect(formatTokenCount(36420)).toBe('36,420')
    expect(formatTokenCount(0)).toBe('0')
    expect(formatTokenCount(null)).toBe('–')
    expect(formatTokenCount(undefined)).toBe('–')
  })
})

describe('addTokenUsage（P2.13 会话级累计）', () => {
  it('快照 + 运行中增量逐轮累加', () => {
    const base = { prompt_tokens: 10, completion_tokens: 4, total_tokens: 14 }
    const delta = { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 }
    expect(addTokenUsage(base, delta)).toEqual({
      prompt_tokens: 13,
      completion_tokens: 6,
      total_tokens: 19,
    })
  })

  it('两边都为空或全零时返回 null（前端隐藏统计，不伪造 0）', () => {
    expect(addTokenUsage(null, null)).toBeNull()
    expect(addTokenUsage(undefined, undefined)).toBeNull()
    expect(addTokenUsage({}, { total_tokens: 0 })).toBeNull()
  })

  it('只有快照时按快照返回（刷新/重开会话后仍可读）', () => {
    expect(addTokenUsage({ total_tokens: 7 }, null)).toEqual({
      prompt_tokens: 0,
      completion_tokens: 0,
      total_tokens: 7,
    })
  })
})
