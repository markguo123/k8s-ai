/**
 * P2.4 通用展示格式化：相对时间与会话状态标签映射。
 */

export function formatRelativeTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const diff = Date.now() - date.getTime()
  if (diff < 60_000) {
    return '刚刚'
  }
  if (diff < 3_600_000) {
    return `${Math.floor(diff / 60_000)} 分钟前`
  }
  if (diff < 86_400_000) {
    return `${Math.floor(diff / 3_600_000)} 小时前`
  }
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${date.getMonth() + 1}月${date.getDate()}日 ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** Agent 状态 → Element Plus tag 类型；状态集对齐 internal/agent/message.go。 */
export function sessionStateTagType(state: string): 'primary' | 'success' | 'warning' | 'danger' | 'info' {
  switch (state) {
    case 'running':
    case 'initializing':
      return 'primary'
    case 'done':
      return 'success'
    case 'waiting-for-input':
      return 'warning'
    case 'exited':
    case 'error':
      return 'danger'
    default:
      return 'info'
  }
}

/** P2.13 Token 计数展示：千分位（对齐原型 36,420 的口径），空值显示占位符。 */
export function formatTokenCount(value: number | null | undefined): string {
  if (value === null || value === undefined) {
    return '–'
  }
  return value.toLocaleString('en-US')
}

/**
 * P2.13 会话级 Token 累加：历史快照（落库汇总）+ 本次连接内各轮增量。
 * 返回 null 表示"没有任何用量"（未持久化或全部为 0），前端据此隐藏统计。
 */
export function addTokenUsage(
  base: { total_tokens?: number; prompt_tokens?: number; completion_tokens?: number } | null | undefined,
  delta: { total_tokens?: number; prompt_tokens?: number; completion_tokens?: number } | null | undefined,
): { total_tokens: number; prompt_tokens: number; completion_tokens: number } | null {
  if (!base && !delta) {
    return null
  }
  const sum = {
    total_tokens: (base?.total_tokens ?? 0) + (delta?.total_tokens ?? 0),
    prompt_tokens: (base?.prompt_tokens ?? 0) + (delta?.prompt_tokens ?? 0),
    completion_tokens: (base?.completion_tokens ?? 0) + (delta?.completion_tokens ?? 0),
  }
  if (sum.total_tokens === 0 && sum.prompt_tokens === 0 && sum.completion_tokens === 0) {
    return null
  }
  return sum
}
