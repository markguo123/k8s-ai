package webui

import (
	"context"

	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// AuthzScopeResolver 把平台 AuthzGrant 解析为 Agent 可执行范围。
func AuthzScopeResolver(svc *authz.Service) ScopeResolver {
	return func(ctx context.Context, user *store.User, role *store.Role) (*tools.AccessScope, error) {
		scope, err := svc.EffectiveScope(ctx, user, role, "")
		if err != nil {
			return nil, err
		}
		return &tools.AccessScope{
			ClusterID: scope.ClusterID, Namespaces: scope.Namespaces,
			ClusterScoped: scope.ClusterScoped, IsAdmin: scope.IsAdmin,
			CanWrite: scope.CanWrite, CanDestroy: scope.CanDestroy,
		}, nil
	}
}
