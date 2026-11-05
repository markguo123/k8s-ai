package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// TestAuthorizedClusterIDs 覆盖 P8.5 报告可见性的授权来源（方案 15 §12.2）：
// 系统体检报告能看见哪些集群，取决于 AuthzGrant 而不是角色名。这里一旦放宽，
// 普通用户就能读到全集群的体检结论；收紧过度则只读角色一份系统报告都看不到。
func TestAuthorizedClusterIDs(t *testing.T) {
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

	// 零授权：不给全部、也不给空串占位（空串会让 store 的 IN 子句退化成匹配空值）。
	all, ids, err := svc.AuthorizedClusterIDs(ctx, user, role)
	if err != nil {
		t.Fatalf("authorized clusters: %v", err)
	}
	if all || len(ids) != 0 {
		t.Fatalf("零授权 = all=%v ids=%v，期望 all=false 且无集群", all, ids)
	}

	// principalId 允许 username 写法（EffectiveScope 同一套判据），乱序与重复要收敛。
	// grant 的权限位刻意用 destroy：写权限面要求 grant 含 *，而读系统报告不要求——
	// 用一把「写不了」的钥匙正好证明这里没有顺手搬来写权限判据。
	for _, cluster := range []string{"c-prod", "c-test", "c-prod"} {
		if _, err := svc.CreateGrant(ctx, CreateGrantRequest{
			PrincipalType: "user", PrincipalID: user.Username,
			ClusterID: cluster, Namespaces: []string{"app"}, Permissions: []string{"destroy"},
		}, "admin"); err != nil {
			t.Fatalf("create grant %s: %v", cluster, err)
		}
	}
	all, ids, err = svc.AuthorizedClusterIDs(ctx, user, role)
	if err != nil {
		t.Fatalf("authorized clusters: %v", err)
	}
	if all || len(ids) != 2 || ids[0] != "c-prod" || ids[1] != "c-test" {
		t.Fatalf("授权集群 = all=%v ids=%v，期望 [c-prod c-test] 去重升序", all, ids)
	}

	// 过期授权不计入。创建侧会直接拒绝过期时间，所以直接写库模拟一条历史遗留行——
	// 这条路径真实存在：授权放着不动就会变成库里的过期行。
	past := time.Now().UTC().Add(-time.Hour)
	now := time.Now().UTC()
	if err := st.AuthzGrants().Create(ctx, &store.AuthzGrant{
		ID: "grant-expired", PrincipalType: "user", PrincipalID: user.Username,
		ClusterID: "c-expired", Namespaces: "[]", Permissions: `["destroy"]`,
		ExpiresAt: &past, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("insert expired grant: %v", err)
	}
	if _, ids, err = svc.AuthorizedClusterIDs(ctx, user, role); err != nil {
		t.Fatalf("authorized clusters: %v", err)
	}
	for _, id := range ids {
		if id == "c-expired" {
			t.Fatalf("过期授权仍计入可见集群：%v", ids)
		}
	}

	// 一条不限定集群的授权 = 全部系统体检报告可见（只放宽系统报告，不放宽他人私有报告）。
	if _, err := svc.CreateGrant(ctx, CreateGrantRequest{
		PrincipalType: "user", PrincipalID: user.ID,
		Namespaces: []string{"app"}, Permissions: []string{"destroy"},
	}, "admin"); err != nil {
		t.Fatalf("create global grant: %v", err)
	}
	all, ids, err = svc.AuthorizedClusterIDs(ctx, user, role)
	if err != nil {
		t.Fatalf("authorized clusters: %v", err)
	}
	if !all || len(ids) != 0 {
		t.Fatalf("全域授权 = all=%v ids=%v，期望 all=true 且 ids 为空", all, ids)
	}

	// 角色含 * 的管理员无需任何 grant。
	admin, err := st.Users().GetByUsername(ctx, auth.DefaultAdminUsername)
	if err != nil {
		t.Fatal(err)
	}
	adminRole, err := st.Roles().Get(ctx, admin.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	if all, ids, err = svc.AuthorizedClusterIDs(ctx, admin, adminRole); err != nil || !all || len(ids) != 0 {
		t.Fatalf("admin = all=%v ids=%v err=%v，期望 all=true", all, ids, err)
	}

	// 缺身份必须报错而不是「返回空集合」：空集合在调用方看起来和「无权限」一样，
	// 但把 nil 吞掉会让装配 bug 伪装成一次正常的越权拦截。
	if _, _, err := svc.AuthorizedClusterIDs(ctx, nil, role); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil user = %v, want ErrInvalid", err)
	}
	if _, _, err := svc.AuthorizedClusterIDs(ctx, user, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil role = %v, want ErrInvalid", err)
	}
}
