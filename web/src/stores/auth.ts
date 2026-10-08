import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

export type Role = 'admin' | 'viewer' | 'operator'

export interface AuthUser {
  username: string
  role: Role
}

const TOKEN_KEY = 'k8s_ai_token'
const USER_KEY = 'k8s_ai_user'

function readStoredUser(): AuthUser | null {
  const raw = localStorage.getItem(USER_KEY)
  if (!raw) {
    return null
  }
  try {
    return JSON.parse(raw) as AuthUser
  } catch {
    return null
  }
}

/**
 * P1.1 骨架：本地会话占位，真实认证在 P1.5/P2.2 接入 /api/v1/auth/login。
 */
export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(localStorage.getItem(TOKEN_KEY))
  const user = ref<AuthUser | null>(readStoredUser())

  const isAuthenticated = computed(() => token.value !== null)
  const role = computed<Role>(() => user.value?.role ?? 'viewer')

  async function login(username: string, password: string): Promise<void> {
    void password
    const session = {
      username: username.trim() || 'admin',
      role: 'admin' as const,
    }
    token.value = `demo-${Date.now()}`
    user.value = session
    localStorage.setItem(TOKEN_KEY, token.value)
    localStorage.setItem(USER_KEY, JSON.stringify(session))
  }

  function logout(): void {
    token.value = null
    user.value = null
    localStorage.removeItem(TOKEN_KEY)
    localStorage.removeItem(USER_KEY)
  }

  return { token, user, role, isAuthenticated, login, logout }
})
