// Package permission 提供四期平台化的权限判定（P1.6）：
// Role → Permission → Policy。业务代码只声明所需 Permission，
// 不散落 role == "admin" 这类直接判断。
//
// 安全约定：产品内权限只作用于 Web/API 入口与审批策略，不进入 CLI
// 执行路径；namespace / resource 的真实权限仍由 kubeconfig / ServiceAccount
// RBAC 决定。
package permission

import (
	"strings"
)

// 基础 Permission 常量（对齐需求 v1.2 §17 的示例枚举）。
const (
	ChatRead  = "chat.read"
	ChatWrite = "chat.write"
	// ChatReadAll 仅授予平台管理员：普通用户与操作员只能读取自己的会话。
	ChatReadAll = "chat.read.all"
	// ChatManageAll 仅授予平台管理员：可控制、审批和维护全部会话。
	ChatManageAll   = "chat.manage.all"
	ClusterRead     = "cluster.read"
	ClusterManage   = "cluster.manage"
	SkillRead       = "skill.read"
	SkillManage     = "skill.manage"
	ApprovalRead    = "approval.read"
	ApprovalExecute = "approval.execute"
	AuditRead       = "audit.read"
	SettingsRead    = "settings.read"
	SettingsWrite   = "settings.write"
	UserManage      = "user.manage"
)

// Access 描述某条 API 路由的访问要求。
type Access int

const (
	// AccessPublic 无需认证即可访问（如 /healthz、/api/v1/auth/login）。
	AccessPublic Access = iota
	// AccessAuthenticated 仅要求登录，不要求具体 Permission。
	AccessAuthenticated
	// AccessPermission 要求登录并持有指定 Permission。
	AccessPermission
)

// Rule 是 Policy 中的一条 API 路由规则。
type Rule struct {
	// Method 是 HTTP 方法（GET/POST/PUT/PATCH/DELETE）或 "ANY"。
	Method string
	// Path 是路由模式；{id} 匹配单段，末尾 * 通配剩余路径。
	Path string
	// Access 是该路由的访问级别。
	Access Access
	// Permission 仅在 Access==AccessPermission 时生效。
	Permission string
}

// Policy 将 Method+Path 映射到访问要求。后登记的规则优先，
// 便于在默认权限之上做细粒度覆盖。
type Policy struct {
	rules []Rule
}

// NewPolicy 用给定规则构建策略。
func NewPolicy(rules ...Rule) *Policy {
	return &Policy{rules: rules}
}

// Require 返回 method+path 匹配的最优先规则；ok=false 表示未登记。
func (p *Policy) Require(method, path string) (Rule, bool) {
	for i := len(p.rules) - 1; i >= 0; i-- {
		r := p.rules[i]
		if !methodMatches(r.Method, method) || !routeMatches(r.Path, path) {
			continue
		}
		return r, true
	}
	return Rule{}, false
}

// RequireAccess 返回访问级别与所需 Permission；ok=false 表示未登记。
// 未登记路径应被调用方按默认拒绝处理（见 internal/server 的权限门禁）。
func (p *Policy) RequireAccess(method, path string) (Access, string, bool) {
	r, ok := p.Require(method, path)
	if !ok {
		return AccessPermission, "", false
	}
	return r.Access, r.Permission, true
}

// Enforcer 判定角色 Permission 集合是否满足某条 Policy 的要求
// （Role → Permission → Policy）。
type Enforcer struct {
	policy *Policy
}

// NewEnforcer 以给定 Policy 构建判定器。
func NewEnforcer(p *Policy) *Enforcer {
	return &Enforcer{policy: p}
}

// Policy 返回判定器使用的策略。
func (e *Enforcer) Policy() *Policy {
	return e.policy
}

// Has 判定 Permission 集合是否满足 required。
// 支持精确匹配、全量通配 "*"，以及 "cluster.*" 形式的段通配。
func (e *Enforcer) Has(perms []string, required string) bool {
	return HasPermission(perms, required)
}

// HasPermission 判定 Permission 集合是否满足要求。独立函数供不持有路由
// Policy 的调用方复用（例如会话归属判定），语义与 Enforcer.Has 完全一致。
func HasPermission(perms []string, required string) bool {
	if required == "" {
		return true
	}
	for _, p := range perms {
		if p == "*" || p == required {
			return true
		}
		if strings.HasSuffix(p, ".*") &&
			strings.HasPrefix(required, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// Authorize 判定角色权限在指定路由上是否被允许。
// required 为路由要求的 Permission；allowed=false 表示越权。
func (e *Enforcer) Authorize(perms []string, method, path string) (required string, allowed bool, registered bool) {
	access, perm, ok := e.policy.RequireAccess(method, path)
	if !ok {
		return "", false, false
	}
	if access != AccessPermission {
		return "", true, true
	}
	return perm, e.Has(perms, perm), true
}

func methodMatches(pattern, method string) bool {
	return pattern == "ANY" || strings.EqualFold(pattern, method)
}

// routeMatches 将规则路径与请求路径按段匹配：{id} 匹配任意单段，
// 末尾 * 匹配剩余任意路径。
func routeMatches(pattern, target string) bool {
	pp := splitPath(pattern)
	tp := splitPath(target)
	for i, seg := range pp {
		if seg == "*" {
			return true
		}
		if i >= len(tp) {
			return false
		}
		if seg == tp[i] {
			continue
		}
		if len(seg) > 2 && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		return false
	}
	return len(pp) == len(tp)
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
