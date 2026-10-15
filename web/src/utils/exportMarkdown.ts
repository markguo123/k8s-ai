import type { ChatMessage } from '@/api/sessions'

/**
 * P7.5「分享 = 导出 Markdown」：把会话按时间顺序整理成可读的 `.md` 文本。
 *
 * 约束（见 `14-方案-前端体验优化-四期.md` §4）：
 * - 顺序固定为 提问 → 思考过程 → 工具调用 → 最终报告；
 * - 不新增后端接口，前端生成 Blob 下载；
 * - 内容沿用既有脱敏结果（后端已脱敏的 content/data 原样导出，本函数不引入任何新字段，
 *   也不去读 arguments/data 里的原始值，避免把未脱敏数据带进导出文件）。
 */
export interface ExportSessionMeta {
  id: string
  name?: string
  exportedAt?: Date
  model?: string
}

function toolLine(message: ChatMessage): string[] {
  const status = message.ok === false ? '失败' : '成功'
  const summary = (message.content ?? '').trim().split('\n')[0].slice(0, 200)
  return [`## 工具调用 · ${message.name ?? 'tool'}`, '', `- 状态：${status}`, summary ? `- 摘要：${summary}` : '', '']
}

export function buildSessionMarkdown(messages: ChatMessage[], meta: ExportSessionMeta): string {
  const at = meta.exportedAt ?? new Date()
  const lines: string[] = [
    `# ${meta.name?.trim() || 'k8s-ai 会话'}`,
    '',
    `- 会话 ID：\`${meta.id}\``,
    `- 导出时间：${at.toISOString()}`,
  ]
  if (meta.model) {
    lines.push(`- 模型：\`${meta.model}\``)
  }
  lines.push('')

  for (const message of messages) {
    if (message.role === 'user' && message.type === 'text') {
      lines.push('## 提问', '', message.content ?? '', '')
      continue
    }
    if (message.type === 'reasoning') {
      lines.push('## 思考过程', '', message.content ?? '', '')
      continue
    }
    if (message.role === 'tool' || message.type === 'tool-call-response') {
      lines.push(...toolLine(message))
      continue
    }
    if (message.type === 'text') {
      lines.push('## 最终报告', '', message.content ?? '', '')
    }
  }

  return `${lines.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd()}\n`
}

/** 文件名：`k8s-ai-<会话名>-<yyyyMMdd-HHmm>.md`（去掉文件系统非法字符）。 */
export function markdownFileName(name: string | undefined, at: Date = new Date()): string {
  const pad = (n: number): string => String(n).padStart(2, '0')
  const safe =
    (name ?? '')
      .trim()
      .replace(/[\\/:*?"<>|\s]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 40) || 'session'
  return `k8s-ai-${safe}-${at.getFullYear()}${pad(at.getMonth() + 1)}${pad(at.getDate())}-${pad(at.getHours())}${pad(at.getMinutes())}.md`
}
