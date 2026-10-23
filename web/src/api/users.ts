/**
 * P3.12 用户与权限 API 客户端（与后端 `internal/server/users.go`、
 * `internal/server/authz.go` 一致）。
 *
 * 能力：用户 CRUD（user.manage）、角色只读视图（user.manage）、
 * namespace 授权/销毁委派（AuthzGrant，user.manage）、用户 API Token（user.manage）。
 *
 * 安全约定：口令/TokenHash 永不回传；明文 API Token 仅在创建时返回一次，
 * 页面展示后本地不得持久化。
 */
import { apiDelete, apiGet, apiPatch, apiPost } from './request'
import type {
  ApiToken,
  AuthRole,
  AuthUser,
  AuthzGrant,
  CreateGrantRequest,
  CreateTokenRequest,
  CreateTokenResult,
  CreateUserRequest,
  UpdateGrantRequest,
  UpdateUserRequest,
} from './types'

/** 用户列表（PasswordHash 由 json:"-" 挡掉，永不回读）。 */
export async function listUsers(): Promise<AuthUser[]> {
  const env = await apiGet<AuthUser[]>('/users')
  return env.data ?? []
}

/** 角色列表（user/operator/admin 及权限视图，管理端只读）。 */
export async function listRoles(): Promise<AuthRole[]> {
  const env = await apiGet<AuthRole[]>('/roles')
  return env.data ?? []
}

/** 创建用户（密码最短 8 位，角色必须命中三角色白名单）。 */
export async function createUser(req: CreateUserRequest): Promise<AuthUser> {
  const env = await apiPost<AuthUser>('/users', req)
  if (!env.data) {
    throw new Error('创建用户结果为空')
  }
  return env.data
}

/** 部分更新用户（displayName/password/roleId/status 均可选）。 */
export async function updateUser(id: string, req: UpdateUserRequest): Promise<AuthUser> {
  const env = await apiPatch<AuthUser>(`/users/${encodeURIComponent(id)}`, req)
  if (!env.data) {
    throw new Error('更新用户结果为空')
  }
  return env.data
}

/** 删除用户（内置 admin 与当前登录用户受后端保护）。 */
export async function deleteUser(id: string): Promise<void> {
  const env = await apiDelete<Record<string, string>>(`/users/${encodeURIComponent(id)}`)
  if (env.error) {
    throw new Error(env.error.message)
  }
}

/** 全部 namespace 授权/销毁委派（管理员视图）。 */
export async function listGrants(): Promise<AuthzGrant[]> {
  const env = await apiGet<AuthzGrant[]>('/grants')
  return env.data ?? []
}

/** 单条授权委派。 */
export async function getGrant(id: string): Promise<AuthzGrant> {
  const env = await apiGet<AuthzGrant>(`/grants/${encodeURIComponent(id)}`)
  if (!env.data) {
    throw new Error('授权委派不存在')
  }
  return env.data
}

/** 创建 namespace 授权或销毁委派（expiresAt 省略=永久）。 */
export async function createGrant(req: CreateGrantRequest): Promise<AuthzGrant> {
  const env = await apiPost<AuthzGrant>('/grants', req)
  if (!env.data) {
    throw new Error('创建授权结果为空')
  }
  return env.data
}

/** 部分更新授权委派（不可变更主体；expiresAt 省略=保持原值）。 */
export async function updateGrant(id: string, req: UpdateGrantRequest): Promise<AuthzGrant> {
  const env = await apiPatch<AuthzGrant>(`/grants/${encodeURIComponent(id)}`, req)
  if (!env.data) {
    throw new Error('更新授权结果为空')
  }
  return env.data
}

/** 撤销授权委派（立即生效，集群侧 RBAC 回收属 P4.6）。 */
export async function deleteGrant(id: string): Promise<void> {
  const env = await apiDelete<Record<string, string>>(`/grants/${encodeURIComponent(id)}`)
  if (env.error) {
    throw new Error(env.error.message)
  }
}

/** 某用户的 API Token 元数据列表（TokenHash 永不回读）。 */
export async function listTokens(userId: string): Promise<ApiToken[]> {
  const env = await apiGet<ApiToken[]>(`/users/${encodeURIComponent(userId)}/tokens`)
  return env.data ?? []
}

/** 签发 API Token：明文仅此一次返回，服务端只存 SHA-256。 */
export async function createToken(userId: string, req: CreateTokenRequest): Promise<CreateTokenResult> {
  const env = await apiPost<CreateTokenResult>(`/users/${encodeURIComponent(userId)}/tokens`, req)
  if (!env.data || !env.data.token) {
    throw new Error('签发 Token 结果为空')
  }
  return env.data
}

/** 更新 Token 生命周期状态（active | revoked | disabled）。 */
export async function updateTokenStatus(userId: string, tokenId: string, status: string): Promise<ApiToken> {
  const env = await apiPatch<ApiToken>(
    `/users/${encodeURIComponent(userId)}/tokens/${encodeURIComponent(tokenId)}`,
    { status },
  )
  if (!env.data) {
    throw new Error('更新 Token 结果为空')
  }
  return env.data
}

/** 撤销并删除 API Token（仅限该用户名下）。 */
export async function deleteToken(userId: string, tokenId: string): Promise<void> {
  const env = await apiDelete<Record<string, string>>(
    `/users/${encodeURIComponent(userId)}/tokens/${encodeURIComponent(tokenId)}`,
  )
  if (env.error) {
    throw new Error(env.error.message)
  }
}

/** 统一错误文案，供页面直接展示（不透出请求体内容）。 */
export function describeUsersError(method: string, error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message
  }
  return `${method}失败，请稍后重试`
}

export default {
  listUsers,
  listRoles,
  createUser,
  updateUser,
  deleteUser,
  listGrants,
  getGrant,
  createGrant,
  updateGrant,
  deleteGrant,
  listTokens,
  createToken,
  updateTokenStatus,
  deleteToken,
}
