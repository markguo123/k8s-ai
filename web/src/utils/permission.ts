/**
 * P2.9 前端权限口径：与后端 `internal/permission` 的 Permission 常量、
 * `internal/auth` 的默认角色权限保持一致。
 *
 * 在四期平台里，真实权限由后端判定（P1.6 权限门禁 + P4 审批中心）：
 * 前端只负责「不展示无权操作的入口」，隐藏按钮不是安全机制。
 */

import type { Role } from '@/stores/auth'

/** 与后端 permission 包同名的权限常量（仅列出前端需要的部分）。 */
export const PERM = {
  chatRead: 'chat.read',
  chatWrite: 'chat.write',
  clusterRead: 'cluster.read',
  clusterManage: 'cluster.manage',
  approvalRead: 'approval.read',
  approvalExecute: 'approval.execute',
  auditRead: 'audit.read',
  settingsRead: 'settings.read',
  settingsWrite: 'settings.write',
  skillRead: 'skill.read',
  skillManage: 'skill.manage',
  userManage: 'user.manage',
} as const

/**
 * 角色默认平台权限，对齐后端 `auth.defaultRoles()`。user/operator 的平台
 * 权限为零，集群能力由 admin 授予的 namespace AuthzGrant 决定；admin 为全量。
 */
export const ROLE_PERMISSIONS: Record<Role, string[]> = {
  admin: ['*'],
  operator: [],
  user: [],
}

/**
 * 权限判定：支持精确匹配、全量通配 `*` 与段通配 `cluster.*`，
 * 语义与后端 `permission.Enforcer.Has` 一致。
 */
export function hasPermission(perms: string[] | undefined, required: string): boolean {
  if (!required) {
    return true
  }
  if (!perms || perms.length === 0) {
    return false
  }
  return perms.some((granted) => {
    if (granted === '*' || granted === required) {
      return true
    }
    if (granted.endsWith('.*')) {
      const prefix = granted.slice(0, -1) // "cluster.*" → "cluster."
      return required.startsWith(prefix) && required.length > prefix.length
    }
    return false
  })
}

/** 某角色的权限集合（未知角色按最小权限处理）。 */
export function permissionsOf(role: Role | null | undefined): string[] {
  if (!role) {
    return []
  }
  return ROLE_PERMISSIONS[role] ?? []
}

/**
 * 是否可审批当前会话的写操作（本人自确认）。
 * 审批与平台角色权限解耦：三类登录角色都可确认自己的会话内操作；
 * 资源范围、销毁能力与审批策略仍由后端独立校验。
 * 后端仍会独立校验审批策略与权限，前端隐藏不是安全机制。
 */
export function canDecideApproval(role: Role | null | undefined): boolean {
  return role === 'admin' || role === 'operator' || role === 'user'
}

/** 是否可查看审批与审计（用于治理菜单的入口显隐）。 */
export function canReadGovernance(role: Role | null | undefined): boolean {
  return hasPermission(permissionsOf(role), PERM.approvalRead)
}
