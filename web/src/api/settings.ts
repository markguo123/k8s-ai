/**
 * P3.10 配置中心 API 客户端（与后端 `internal/server/settings.go` 一致）。
 *
 * 能力：GET/PATCH /settings/{group}。读视图对敏感键（api_key/kubeconfig/mcp）
 * 只回 hasValue，永不回明文（P3.9 落库静态加密）；PATCH 只更新出现的键，
 * 成功返回整组最新视图。
 */
import { apiGet, apiPatch } from './request'
import type { SettingsGroupView } from './types'

/** 读取配置分组视图（敏感键 value 为 null，仅回 hasValue）。 */
export async function getSettings(group: string): Promise<SettingsGroupView> {
  const env = await apiGet<SettingsGroupView>(`/settings/${encodeURIComponent(group)}`)
  if (!env.data) {
    throw new Error('配置分组不存在')
  }
  return env.data
}

/** 局部更新分组配置：只提交出现的键；成功返回整组最新视图。 */
export async function patchSettings(
  group: string,
  values: Record<string, unknown>,
): Promise<SettingsGroupView> {
  const env = await apiPatch<SettingsGroupView>(`/settings/${encodeURIComponent(group)}`, values)
  if (!env.data) {
    throw new Error('配置更新结果为空')
  }
  return env.data
}

/** 统一错误文案，供页面直接展示（不透出请求体内容）。 */
export function describeSettingsError(method: string, error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message
  }
  return `${method}失败，请稍后重试`
}

export default {
  getSettings,
  patchSettings,
}
