import { ElMessage } from 'element-plus'

/** 复制到剪贴板并给出轻量反馈。 */
export function copyText(text: string, label = '已复制'): void {
  const value = text ?? ''
  const done = (): void => {
    ElMessage.success(label)
  }
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(value).then(done).catch(() => fallbackCopy(value, done))
    return
  }
  fallbackCopy(value, done)
}

function fallbackCopy(text: string, done: () => void): void {
  const area = document.createElement('textarea')
  area.value = text
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  try {
    document.execCommand('copy')
    done()
  } catch {
    // 忽略复制失败（仅展示侧，不抛错）。
  } finally {
    document.body.removeChild(area)
  }
}

export default copyText
