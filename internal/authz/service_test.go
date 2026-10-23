package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

func newTestService(t *testing.T) (*Service, store.Store, string) {
	t.Helper()
	st, err := store.OpenSQLite("file:authz-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	authSvc := auth.New(st.Users(), st.Roles())
	if err := authSvc.Bootstrap(context.Background(), "admin-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// 建一个普通用户，token 面向该用户签发。
	roles, err := st.Roles().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var userRole string
	for _, r := range roles {
		if r.Name == auth.RoleUser {
			userRole = r.ID
		}
	}
	if userRole == "" {
		t.Fatal("默认 user 角色不存在")
	}
	hash, err := auth.HashPassword("pw-12345")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	u := &store.User{
		ID: "user-1", Username: "alice", DisplayName: "Alice",
		PasswordHash: hash, RoleID: userRole, Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Users().Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return New(st), st, u.ID
}

func TestGrantCRUD(t *testing.T) {
	svc, st, userID := newTestService(t)
	ctx := context.Background()

	future := time.Now().UTC().Add(24 * time.Hour)
	g, err := svc.CreateGrant(ctx, CreateGrantRequest{
		PrincipalType: "user",
		PrincipalID:   userID,
		ClusterID:     "c-prod",
		Namespaces:    []string{"app-a", "app-b"},
		Permissions:   []string{"*", "destroy"},
		ExpiresAt:     &future,
	}, "admin-user-id")
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if g.ID == "" || g.PrincipalType != "user" || len(g.Namespaces) != 2 || len(g.Permissions) != 2 {
		t.Fatalf("grant view 不符: %+v", g)
	}

	got, err := svc.GetGrant(ctx, g.ID)
	if err != nil {
		t.Fatalf("get grant: %v", err)
	}
	if got.ClusterID != "c-prod" || got.CreatedBy != "admin-user-id" {
		t.Fatalf("get grant 内容不符: %+v", got)
	}

	list, err := svc.ListGrants(ctx)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list grants 数量 = %d, want 1", len(list))
	}

	ns := []string{"app-c"}
	perms := []string{"destroy"}
	updated, err := svc.UpdateGrant(ctx, g.ID, UpdateGrantRequest{
		ClusterID:   strp("c-staging"),
		Namespaces:  &ns,
		Permissions: &perms,
	})
	if err != nil {
		t.Fatalf("update grant: %v", err)
	}
	if updated.ClusterID != "c-staging" || len(updated.Namespaces) != 1 || len(updated.Permissions) != 1 {
		t.Fatalf("update grant 内容不符: %+v", updated)
	}
	if updated.PrincipalID != userID {
		t.Fatal("update 不应变更主体")
	}

	raw, _ := st.AuthzGrants().Get(ctx, g.ID)
	if !strings.Contains(raw.Namespaces, "app-c") || !strings.Contains(raw.Permissions, "destroy") {
		t.Fatalf("落库 JSON 不符: %+v", raw)
	}

	if err := svc.DeleteGrant(ctx, g.ID); err != nil {
		t.Fatalf("delete grant: %v", err)
	}
	if _, err := svc.GetGrant(ctx, g.ID); err == nil {
		t.Fatal("删除后仍可读取")
	}
	if err := svc.DeleteGrant(ctx, g.ID); err == nil {
		t.Fatal("重复删除应报错")
	}
}

func TestEffectiveScope(t *testing.T) {
	svc, st, userID := newTestService(t)
	ctx := context.Background()
	user, err := st.Users().Get(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	role, err := st.Roles().Get(ctx, user.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGrant(ctx, CreateGrantRequest{
		PrincipalType: "user", PrincipalID: user.Username,
		Namespaces: []string{"mysql-dev"}, Permissions: []string{"*"},
	}, "admin"); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := svc.CreateGrant(ctx, CreateGrantRequest{
		PrincipalType: "user", PrincipalID: user.Username,
		Namespaces: []string{"expired"}, Permissions: []string{"*"}, ExpiresAt: &past,
	}, "admin"); err == nil {
		t.Fatal("expired grant should be rejected at creation")
	}

	scope, err := svc.EffectiveScope(ctx, user, role, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	if scope.IsAdmin || scope.ClusterScoped || !scope.CanWrite || len(scope.Namespaces) != 1 || scope.Namespaces[0] != "mysql-dev" {
		t.Fatalf("scope = %+v", scope)
	}

	admin, err := st.Users().GetByUsername(ctx, auth.DefaultAdminUsername)
	if err != nil {
		t.Fatal(err)
	}
	adminRole, err := st.Roles().Get(ctx, admin.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	adminScope, err := svc.EffectiveScope(ctx, admin, adminRole, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	if !adminScope.IsAdmin || !adminScope.ClusterScoped || !adminScope.CanDestroy {
		t.Fatalf("admin scope = %+v", adminScope)
	}
}

func TestGrantValidation(t *testing.T) {
	svc, _, userID := newTestService(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Hour)
	cases := []CreateGrantRequest{
		{PrincipalType: "x", PrincipalID: userID, Namespaces: []string{"ns"}, Permissions: []string{"*"}},
		{PrincipalType: "user", PrincipalID: "", Namespaces: []string{"ns"}, Permissions: []string{"*"}},
		{PrincipalType: "user", PrincipalID: userID, Namespaces: nil, Permissions: []string{"*"}},
		{PrincipalType: "user", PrincipalID: userID, Namespaces: []string{"", "b"}, Permissions: []string{"*"}},
		{PrincipalType: "user", PrincipalID: userID, Namespaces: []string{"a", "a"}, Permissions: []string{"*"}},
		{PrincipalType: "user", PrincipalID: userID, Namespaces: []string{"a"}, Permissions: []string{"watch"}},
		{PrincipalType: "user", PrincipalID: userID, Namespaces: []string{"a"}, Permissions: []string{"*"}, ExpiresAt: &past},
	}
	for i, c := range cases {
		if _, err := svc.CreateGrant(ctx, c, "actor"); err == nil {
			t.Errorf("case %d: 非法授权应被拒绝: %+v", i, c)
		}
	}
}

func TestTokenCreateHashOnlyAndNeverEchoed(t *testing.T) {
	svc, st, userID := newTestService(t)
	ctx := context.Background()

	exp := time.Now().UTC().Add(7 * 24 * time.Hour)
	tok, plain, err := svc.CreateToken(ctx, userID, CreateTokenRequest{Name: "cli-token", ExpiresAt: &exp})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if plain == "" || len(plain) != 64 {
		t.Fatalf("明文 token 长度不符: %d", len(plain))
	}
	sum := sha256.Sum256([]byte(plain))
	wantHash := hex.EncodeToString(sum[:])
	raw, err := st.ApiTokens().Get(ctx, tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.TokenHash != wantHash {
		t.Fatal("落库的 token hash 与明文不匹配")
	}
	if raw.TokenHash == plain {
		t.Fatal("明文竟然与哈希一致")
	}

	list, err := svc.ListTokens(ctx, userID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list tokens 数量 = %d, want 1", len(list))
	}
	// authz.Token 视图结构上不含 TokenHash 字段；此处校验状态与数量。
	for _, v := range list {
		if v.Status != StatusActive {
			t.Fatalf("新 token 状态应为 active, got %q", v.Status)
		}
	}
}

func TestTokenStatusTransitions(t *testing.T) {
	svc, _, userID := newTestService(t)
	ctx := context.Background()

	tok, _, err := svc.CreateToken(ctx, userID, CreateTokenRequest{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.UpdateTokenStatus(ctx, userID, tok.ID, StatusDisabled)
	if err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if got.Status != StatusDisabled {
		t.Fatalf("状态 = %q, want disabled", got.Status)
	}
	got, err = svc.UpdateTokenStatus(ctx, userID, tok.ID, StatusRevoked)
	if err != nil {
		t.Fatalf("revoked: %v", err)
	}
	if got.Status != StatusRevoked {
		t.Fatalf("状态 = %q, want revoked", got.Status)
	}
	if _, err := svc.UpdateTokenStatus(ctx, userID, tok.ID, "bogus"); err == nil {
		t.Fatal("非法状态应拒绝")
	}

	// 其他用户无法改/删/读该 token。
	if _, err := svc.UpdateTokenStatus(ctx, "user-other", tok.ID, StatusRevoked); err == nil {
		t.Fatal("跨用户改 token 应拒绝")
	}
	if err := svc.DeleteToken(ctx, "user-other", tok.ID); err == nil {
		t.Fatal("跨用户删 token 应拒绝")
	}

	if err := svc.DeleteToken(ctx, userID, tok.ID); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	if err := svc.DeleteToken(ctx, userID, tok.ID); err == nil {
		t.Fatal("重复删除应报错")
	}
}

func TestTargetUserMustExist(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.ListTokens(ctx, "user-missing"); err == nil {
		t.Fatal("管理不存在的用户 token 应拒绝")
	}
	if _, _, err := svc.CreateToken(ctx, "user-missing", CreateTokenRequest{}); err == nil {
		t.Fatal("给不存在的用户签发 token 应拒绝")
	}
}

func strp(s string) *string { return &s }
