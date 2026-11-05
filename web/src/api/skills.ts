/**
 * P3.6/P3.7 Skill 平台 API 客户端（与后端 `internal/server/skills.go` 一致）。
 *
 * 能力：列表/单条读取（skill.read）、导入与启停（skill.manage）。
 * 导入分三种载体（P3.6/P3.8）：
 *   - /skills/import            原始 YAML 文本（声明式 structured，`---` 多文档）
 *   - /skills/import-procedural 原始 SKILL.md 文本（标准定义式 procedural，多篇）
 *   - /skills/import-zip        技能包 zip（multipart file，仅根目录 SKILL.md + 文本资产）
 * 均整批先校验后落库；启停即时影响 Chat 命中 registry，Store 保留历史记录。
 */
import { apiGet, apiPatch, apiPostFormData, apiPostRaw } from './request'
import type { Skill } from './types'

/** 内置与自定义 Skill 记录列表（含运行统计；Skill 为声明式规格，无执行字段）。 */
export async function listSkills(): Promise<Skill[]> {
  const env = await apiGet<Skill[]>('/skills')
  return env.data ?? []
}

/** 单条 Skill 记录读取（P3.6）。 */
export async function getSkill(name: string): Promise<Skill> {
  const env = await apiGet<Skill>(`/skills/${encodeURIComponent(name)}`)
  if (!env.data) {
    throw new Error('Skill 不存在')
  }
  return env.data
}

/** 导入自定义 Skill：body 为原始 YAML 文本，支持单个或 `---` 分隔的多文档。 */
export async function importSkills(yamlText: string): Promise<Skill[]> {
  const env = await apiPostRaw<Skill[]>('/skills/import', yamlText)
  return env.data ?? []
}

/** 导入标准 SKILL.md 文本（P3.8.2）：原始 Markdown 文本，支持多篇，正文只作文本参考。 */
export async function importProceduralDocs(markdownText: string): Promise<Skill[]> {
  const env = await apiPostRaw<Skill[]>('/skills/import-procedural', markdownText)
  return env.data ?? []
}

/** 上传技能包 zip（P3.8.3）：multipart 字段 file，仅根目录 SKILL.md 主文 + 白名单文本资产。 */
export async function importProceduralZip(file: File): Promise<Skill[]> {
  const formData = new FormData()
  formData.append('file', file)
  const env = await apiPostFormData<Skill[]>('/skills/import-zip', formData)
  return env.data ?? []
}

/** 启停 Skill（P3.7）：停用后即时从 Chat 命中 registry 移除，Store 保留记录。 */
export async function setSkillEnabled(name: string, enabled: boolean): Promise<Skill> {
  const env = await apiPatch<Skill>(`/skills/${encodeURIComponent(name)}/enabled`, { enabled })
  if (!env.data) {
    throw new Error('Skill 更新结果为空')
  }
  return env.data
}

/** 统一错误文案，供页面直接展示（不透出请求体内容）。 */
export function describeSkillError(method: string, error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message
  }
  return `${method}失败，请稍后重试`
}

export default {
  listSkills,
  getSkill,
  importSkills,
  importProceduralDocs,
  importProceduralZip,
  setSkillEnabled,
}
