/**
 * P2.3 认证端点客户端：POST /api/v1/auth/login、POST /api/v1/auth/logout、
 * GET /api/v1/auth/me。登录/登出为公开端点，401 不触发跳转。
 */
import {
  ErrorCode,
  type ChangePasswordRequest,
  type LoginRequest,
  type LoginResponse,
  type MeResponse,
  type EffectiveScope,
} from './types'
import request, { ApiRequestError } from './request'

export async function login(payload: LoginRequest): Promise<LoginResponse> {
  const res = await request<LoginResponse>('/auth/login', {
    method: 'POST',
    body: payload,
    redirectOnUnauthorized: false,
  })
  if (!res.data) {
    throw new ApiRequestError(200, ErrorCode.internal, 'auth: empty login response')
  }
  return res.data
}

export async function logout(): Promise<void> {
  try {
    await request<never>('/auth/logout', {
      method: 'POST',
      redirectOnUnauthorized: false,
    })
  } catch {
    // 服务端会话可能已失效或不可达；前端侧仍然执行本地清理。
  }
}

export async function me(): Promise<MeResponse> {
  const res = await request<MeResponse>('/auth/me')
  if (!res.data) {
    throw new ApiRequestError(200, ErrorCode.internal, 'auth: empty me response')
  }
  return res.data
}

/** 当前登录用户的 Kubernetes 有效授权范围。 */
export async function myScope(): Promise<EffectiveScope> {
  const res = await request<EffectiveScope>('/auth/scope')
  if (!res.data) {
    throw new ApiRequestError(200, ErrorCode.internal, 'auth: empty scope response')
  }
  return res.data
}

/** 登录用户自助改密（P6.12）；401 代表旧密码错误，400 代表新密码不合规。 */
export async function changePassword(payload: ChangePasswordRequest): Promise<void> {
  await request<{ status: string }>('/auth/password', {
    method: 'POST',
    body: payload,
    redirectOnUnauthorized: false,
  })
}
