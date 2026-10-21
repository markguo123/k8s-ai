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
		permission.Rule{Method: "POST", Path: "/api/v1/auth/password", Access: permission.AccessAuthenticated},
		permission.Rule{Method: "POST", Path: "/api/v1/scans", Access: permission.AccessPermission, Permission: permission.ChatWrite},
		permission.Rule{Method: "GET", Path: "/api/v1/scans/{id}", Access: permission.AccessPermission, Permission: permission.ClusterRead},
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
	)
}

// permissionGate 对请求做认证与权限判定（Role → Permission → Policy）。
// /api/v1 下未登记的路由默认拒绝（403）；其余路径透传由 mux 处理（含 404）。
func permissionGate(enforcer *permission.Enforcer, resolve principalResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, required, registered := enforcer.Policy().RequireAccess(r.Method, r.URL.Path)
		if !registered {
			if isAPIRequest(r.URL.Path) {
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
