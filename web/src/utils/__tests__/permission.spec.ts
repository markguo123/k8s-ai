import { describe, expect, it } from 'vitest'

import {
  PERM,
  ROLE_PERMISSIONS,
  canDecideApproval,
  canReadGovernance,
  hasPermission,
  permissionsOf,
} from '@/utils/permission'

describe('hasPermission', () => {
  it('支持精确匹配、全量通配与段通配（与后端 Enforcer.Has 同语义）', () => {
    expect(hasPermission(['chat.read'], 'chat.read')).toBe(true)
    expect(hasPermission(['chat.read'], 'chat.write')).toBe(false)
    expect(hasPermission(['*'], 'user.manage')).toBe(true)
    expect(hasPermission(['cluster.*'], 'cluster.read')).toBe(true)
    expect(hasPermission(['cluster.*'], 'clusterx.read')).toBe(false)
    expect(hasPermission(['cluster.*'], 'cluster')).toBe(false)
  })

  it('空权限集合拒绝，空要求放行', () => {
    expect(hasPermission([], 'chat.read')).toBe(false)
    expect(hasPermission(undefined, 'chat.read')).toBe(false)
    expect(hasPermission([], '')).toBe(true)
  })
})

describe('角色权限口径', () => {
  it('admin 通配、viewer 只读、operator 可写集群', () => {
    expect(hasPermission(permissionsOf('admin'), PERM.userManage)).toBe(true)
    expect(hasPermission(permissionsOf('operator'), PERM.chatWrite)).toBe(true)
    expect(hasPermission(permissionsOf('viewer'), PERM.chatWrite)).toBe(false)
    expect(hasPermission(permissionsOf('viewer'), PERM.auditRead)).toBe(true)
  })

  it('未登录（空角色）按最小权限处理', () => {
    expect(permissionsOf(null)).toEqual([])
    expect(canDecideApproval(null)).toBe(false)
    expect(canReadGovernance(null)).toBe(false)
  })

  it('P2.9 审批按钮显隐：只读角色不展示，可写角色可见', () => {
    expect(canDecideApproval('admin')).toBe(true)
    expect(canDecideApproval('operator')).toBe(true)
    expect(canDecideApproval('user')).toBe(true)
    expect(canDecideApproval('viewer')).toBe(false)
  })

  it('角色权限表覆盖全部产品角色（避免新增角色漏配导致越权展示）', () => {
    expect(Object.keys(ROLE_PERMISSIONS).sort()).toEqual(['admin', 'operator', 'user', 'viewer'])
  })
})
