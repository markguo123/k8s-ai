package server

import (
	"context"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

const (
	ctxKeyUser ctxKey = iota + 100
	ctxKeyRole
)

// principalResolver 从 Token 解析当前登录用户与角色（由 Server.auth 满足）。
type principalResolver func(ctx context.Context, token string) (*store.User, *store.Role, error)

// UserFrom 从上下文取当前登录用户；无则返回 nil。
func UserFrom(ctx context.Context) *store.User {
	if v, ok := ctx.Value(ctxKeyUser).(*store.User); ok {
		return v
	}
	return nil
}

// RoleFrom 从上下文取当前登录用户角色；无则返回 nil。
func RoleFrom(ctx context.Context) *store.Role {
	if v, ok := ctx.Value(ctxKeyRole).(*store.Role); ok {
		return v
	}
	return nil
}

// DefaultAPIPolicy 返回四期 `/api/v1` 的默认路由权限策略。
// 访问要求：Public（无需登录）、Authenticated（仅登录）、Permission（登录 + 指定权限）。
func DefaultAPIPolicy() *permission.Policy {
	return permission.NewPolicy(
		permission.Rule{Method: "GET", Path: "/healthz", Access: permission.AccessPublic},
		permission.Rule{Method: "GET", Path: "/readyz", Access: permission.AccessPublic},
		permission.Rule{Method: "GET", Path: "/version", Access: permission.AccessPublic},
		permission.Rule{Method: "POST", Path: "/api/v1/auth/login", Access: permission.AccessPublic},
		permission.Rule{Method: "POST", Path: "/api/v1/auth/logout", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "GET", Path: "/api/v1/auth/me", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "GET", Path: "/api/v1/auth/scope", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "POST", Path: "/api/v1/auth/password", Access: permission.AccessAuthenticated},
		// 发起体检单独立权限位 scan.execute（方案 17 §4.1）：借 chat.write 会让「能聊天」
		// 等于「能在集群上跑一次全量采集」，而这两件事的爆炸半径完全不同。
		permission.Rule{Method: "POST", Path: "/api/v1/scans", Access: permission.AccessPermission, Permission: permission.ScanExecute},
		// 任务详情只登记到「登录」这一档：真正的闸门在 handler 里按发起人判定（任务结果
		// 含被扫命名空间的问题清单，任何持 cluster.read 的人凭 job id 读他人任务即越权）。
		permission.Rule{Method: "GET", Path: "/api/v1/scans/{id}", Access: permission.AccessAuthenticated},
		// 停止体检同 GET 任务详情：登记到「登录」，由 handler 判「是不是你发起的那次」。
		// 不复用 scan.execute 是有意的——停自己的体检不该需要「能在集群上发起全量采集」的权限。
		permission.Rule{Method: "POST", Path: "/api/v1/scans/{id}/cancel", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "POST", Path: "/api/v1/clusters", Access: permission.AccessPermission, Permission: permission.ClusterManage},
		permission.Rule{Method: "POST", Path: "/api/v1/clusters/test", Access: permission.AccessPermission, Permission: permission.ClusterManage},
		permission.Rule{Method: "GET", Path: "/api/v1/clusters", Access: permission.AccessPermission, Permission: permission.ClusterRead},
		permission.Rule{Method: "GET", Path: "/api/v1/clusters/current", Access: permission.AccessPermission, Permission: permission.ClusterRead},
		permission.Rule{Method: "GET", Path: "/api/v1/clusters/{id}", Access: permission.AccessPermission, Permission: permission.ClusterRead},
		permission.Rule{Method: "DELETE", Path: "/api/v1/clusters/{id}", Access: permission.AccessPermission, Permission: permission.ClusterManage},
		permission.Rule{Method: "GET", Path: "/api/v1/settings/{group}", Access: permission.AccessPermission, Permission: permission.SettingsRead},
		permission.Rule{Method: "PATCH", Path: "/api/v1/settings/{group}", Access: permission.AccessPermission, Permission: permission.SettingsWrite},
		permission.Rule{Method: "GET", Path: "/api/v1/users", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "POST", Path: "/api/v1/users", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "PATCH", Path: "/api/v1/users/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "DELETE", Path: "/api/v1/users/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "GET", Path: "/api/v1/roles", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "GET", Path: "/api/v1/grants", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "GET", Path: "/api/v1/grants/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "POST", Path: "/api/v1/grants", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "PATCH", Path: "/api/v1/grants/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "DELETE", Path: "/api/v1/grants/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "GET", Path: "/api/v1/users/{userID}/tokens", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "POST", Path: "/api/v1/users/{userID}/tokens", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "PATCH", Path: "/api/v1/users/{userID}/tokens/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "DELETE", Path: "/api/v1/users/{userID}/tokens/{id}", Access: permission.AccessPermission, Permission: permission.UserManage},
		permission.Rule{Method: "GET", Path: "/api/v1/skills", Access: permission.AccessPermission, Permission: permission.SkillRead},
		permission.Rule{Method: "GET", Path: "/api/v1/skills/{name}", Access: permission.AccessPermission, Permission: permission.SkillRead},
		permission.Rule{Method: "POST", Path: "/api/v1/skills/import-procedural", Access: permission.AccessPermission, Permission: permission.SkillManage},
		permission.Rule{Method: "POST", Path: "/api/v1/skills/import-zip", Access: permission.AccessPermission, Permission: permission.SkillManage},
		permission.Rule{Method: "POST", Path: "/api/v1/skills/import", Access: permission.AccessPermission, Permission: permission.SkillManage},
		permission.Rule{Method: "PATCH", Path: "/api/v1/skills/{name}/enabled", Access: permission.AccessPermission, Permission: permission.SkillManage},
		// 报告（P8.5）：只登记「必须登录」，可见性由 owner 与集群授权在 handler 里判定。
		// 这里刻意不加 report.read 权限位——四期没有独立的报告权限域，凭空造一个恒真的
		// 权限位会让人误以为存在「授予后即可读所有报告」的开关（方案 15 §12.2）。
		permission.Rule{Method: "GET", Path: "/api/v1/reports", Access: permission.AccessAuthenticated},
		// 报告总览（P9.13）：与列表同一条可见性判定，登记到「登录」，聚合口径在 handler 里算。
		permission.Rule{Method: "GET", Path: "/api/v1/reports/summary", Access: permission.AccessAuthenticated},
		// 按需导出 Markdown（P9.10）：产物只存 JSON 一份，给人看的正文按需渲染，
		// 可见性与 /content 完全一致。
		permission.Rule{Method: "GET", Path: "/api/v1/reports/{id}/export", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "GET", Path: "/api/v1/reports/{id}/content", Access: permission.AccessAuthenticated},
		// 报告详情页（P9）：与原文下载同一档「必须登录」，可见集合复用 reportScopeFor，
		// 不新增权限位——详情页不是第二条授权通道。
		permission.Rule{Method: "GET", Path: "/api/v1/reports/{id}/report", Access: permission.AccessAuthenticated},
		// 清理两面同样只登记到「登录」：归属判定复用报告读侧那一套（owner / system+集群授权 /
		// admin），不新增权限位（方案 15 v1.12 §18）。
		permission.Rule{Method: "DELETE", Path: "/api/v1/reports/{id}", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "POST", Path: "/api/v1/reports/cleanup", Access: permission.AccessAuthenticated},
		// 对象存储健康：权限复用 settings.read（已代表「能看平台运行形态」），不新增权限位。
		permission.Rule{Method: "GET", Path: "/api/v1/storage/health", Access: permission.AccessPermission, Permission: permission.SettingsRead},
	)
}

// permissionGate 对请求做认证与权限判定（Role → Permission → Policy）。
// /api/v1 下未登记的路由默认拒绝（403）；其余路径透传由 mux 处理（含 404）。
func permissionGate(enforcer *permission.Enforcer, resolve principalResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, required, registered := enforcer.Policy().RequireAccess(r.Method, r.URL.Path)
		if !registered {
			if isAPIRequest(r.URL.Path) {
				// 未被访问策略登记的 API 路径一律在网关处拒绝（deny-by-default 不放松，
				// 状态码沿用三期能力基线 tests/platform 锁定的 403）。P6.11 要锁的是
				// "必须是统一 JSON 错误体"：/api/v1/* 由 newRootHandler 全量送进平台 API，
				// 永远不可能落到 SPA fallback 变成 index.html。
				writeError(w, r, http.StatusForbidden, codeForbidden, "endpoint not covered by access policy")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if access == permission.AccessPublic {
			next.ServeHTTP(w, r)
			return
		}
		u, role, err := resolve(r.Context(), BearerTokenFrom(r.Context()))
		if err != nil || u == nil || role == nil {
			writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
			return
		}
		if access == permission.AccessPermission && !enforcer.Has(role.Permissions, required) {
			writeError(w, r, http.StatusForbidden, codeForbidden, "permission denied")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUser, u)
		ctx = context.WithValue(ctx, ctxKeyRole, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isAPIRequest(path string) bool {
	return len(path) >= len(apiPrefix) && path[:len(apiPrefix)] == apiPrefix
}

const apiPrefix = "/api/v1/"
