import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import { authApi, type AuthUser as ApiAuthUser } from '@/api'
import { clearSession, readStoredUser, readToken, saveSession } from '@/api/request'

/** 产品角色：新设计为 user/operator/admin，viewer 仅保留三期兼容读取。 */
export type Role = 'admin' | 'operator' | 'viewer' | 'user'

/** 前端会话视图：仅暴露展示所需字段，不含任何敏感信息。 */
export interface AuthUser {
  userId: string
  username: string
  displayName: string
  role: Role
}

function normalizeRole(name?: string | null): Role {
  const key = name?.trim().toLowerCase()
  if (key === 'admin' || key === 'operator' || key === 'user') {
    return key
  }
  return 'viewer'
}

function toAuthUser(apiUser: ApiAuthUser, roleName?: string): AuthUser {
  return {
    userId: apiUser.id,
    username: apiUser.username,
    displayName: apiUser.displayName || apiUser.username,
    role: normalizeRole(roleName ?? apiUser.roleId),
  }
}

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(readToken())
  const user = ref<AuthUser | null>(readStoredUser<AuthUser>())

  const isAuthenticated = computed(() => token.value !== null)
  const role = computed<Role>(() => user.value?.role ?? 'viewer')

  async function login(username: string, password: string): Promise<void> {
    const session = await authApi.login({ username, password })
    token.value = session.token
    user.value = toAuthUser(session.user)
    saveSession(session.token, user.value)
    await refreshMe()
  }

  async function refreshMe(): Promise<void> {
    const me = await authApi.me()
    if (!user.value) {
      return
    }
    user.value = { ...user.value, role: normalizeRole(me.role.name) }
    saveSession(token.value ?? '', user.value)
  }

  /** 应用启动时恢复本地会话，并用 /auth/me 校正角色（401 时由 request 清理并跳登录）。 */
  async function restore(): Promise<void> {
    const storedUser = readStoredUser<AuthUser>()
    const storedToken = readToken()
    if (!storedUser || !storedToken) {
      clearSession()
      return
    }
    token.value = storedToken
    user.value = storedUser
    try {
      await refreshMe()
    } catch {
      // 网络异常时保留本地会话；401 已在 request 内部处理。
    }
  }

  async function logout(): Promise<void> {
    await authApi.logout()
    clearSession()
    token.value = null
    user.value = null
  }

  return { token, user, role, isAuthenticated, login, logout, restore }
})
