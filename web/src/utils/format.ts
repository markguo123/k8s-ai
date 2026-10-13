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
