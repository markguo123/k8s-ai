/**
 * P2.3 通用 request 封装：基于 fetch 的 `/api/v1` 客户端，统一处理
 * 认证头、分页查询参数、响应信封解析、错误码归一与 401 会话失效跳转。
 */
import {
  ErrorCode,
  type ApiResponse,
  type PageQuery,
} from './types'

const API_BASE = '/api/v1'

export const TOKEN_KEY = 'k8s_ai_token'
export const USER_KEY = 'k8s_ai_user'

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  body?: unknown
  query?: PageQuery
  headers?: HeadersInit
  /**
   * 401 时是否清除本地会话并跳转登录页。登录/登出等公开端点传 false，
   * 避免在已处于登录页时触发重复跳转。
   */
  redirectOnUnauthorized?: boolean
}

/** 统一 API 错误：携带 HTTP 状态码与后端错误码，可供 UI 直接展示。 */
export class ApiRequestError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiRequestError'
    this.status = status
    this.code = code
  }
}

export function readToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function readStoredUser<T>(): T | null {
  const raw = localStorage.getItem(USER_KEY)
  if (!raw) {
    return null
  }
  try {
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}

export function saveSession(token: string, user: unknown): void {
  localStorage.setItem(TOKEN_KEY, token)
  localStorage.setItem(USER_KEY, JSON.stringify(user))
}

export function clearSession(): void {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
}

function buildUrl(path: string, query?: PageQuery): string {
  const qs = new URLSearchParams()
  if (query?.page !== undefined) {
    qs.set('page', String(query.page))
  }
  const pageSize = query?.page_size ?? query?.pageSize
  if (pageSize !== undefined) {
    qs.set('page_size', String(pageSize))
  }
  const suffix = qs.size > 0 ? `?${qs.toString()}` : ''
  return `${API_BASE}${path}${suffix}`
}

function errorCodeForStatus(status: number): string {
  switch (status) {
    case 400:
      return ErrorCode.invalidArgument
    case 401:
      return ErrorCode.unauthorized
    case 403:
      return ErrorCode.forbidden
    case 404:
      return ErrorCode.notFound
    case 409:
      return ErrorCode.conflict
    default:
      return ErrorCode.internal
  }
}

function redirectToLogin(): void {
  const currentPath = `${window.location.pathname}${window.location.search}`
  clearSession()
  if (window.location.pathname === '/login') {
    return
  }
  const redirect = encodeURIComponent(currentPath)
  window.location.assign(`/login?redirect=${redirect}`)
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<ApiResponse<T>> {
  const {
    method = 'GET',
    body,
    query,
    headers: extraHeaders,
    redirectOnUnauthorized = true,
  } = options

  const headers = new Headers(extraHeaders)
  headers.set('Accept', 'application/json')
  if (body !== undefined) {
    headers.set('Content-Type', 'application/json')
  }
  const token = readToken()
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }

  const init: RequestInit = { method, headers }
  if (body !== undefined) {
    init.body = JSON.stringify(body)
  }

  const response = await fetch(buildUrl(path, query), init)
  const text = await response.text()
  let payload: ApiResponse<T> | null = null
  if (text) {
    try {
      payload = JSON.parse(text) as ApiResponse<T>
    } catch {
      payload = null
    }
  }

  if (!response.ok || payload?.error) {
    const status = response.status
    if (status === 401 && redirectOnUnauthorized) {
      redirectToLogin()
    }
    const code = payload?.error?.code ?? errorCodeForStatus(status)
    const message = payload?.error?.message ?? `request failed with status ${status}`
    throw new ApiRequestError(status, code, message)
  }

  if (!payload) {
    throw new ApiRequestError(response.status, errorCodeForStatus(response.status), 'empty response body')
  }
  return payload
}

export function apiGet<T>(path: string, query?: PageQuery): Promise<ApiResponse<T>> {
  return request<T>(path, { method: 'GET', query })
}

export function apiPost<T>(path: string, body?: unknown): Promise<ApiResponse<T>> {
  return request<T>(path, { method: 'POST', body })
}

export function apiPatch<T>(path: string, body?: unknown): Promise<ApiResponse<T>> {
  return request<T>(path, { method: 'PATCH', body })
}

export function apiDelete<T>(path: string): Promise<ApiResponse<T>> {
  return request<T>(path, { method: 'DELETE' })
}

export default request
