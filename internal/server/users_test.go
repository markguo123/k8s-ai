package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

func roleIDByName(t *testing.T, st store.Store, name string) string {
	t.Helper()
	roles, err := st.Roles().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if r.Name == name {
			return r.ID
		}
	}
	t.Fatalf("角色 %q 不存在", name)
	return ""
}

func userByUsername(t *testing.T, st store.Store, username string) *store.User {
	t.Helper()
	u, err := st.Users().GetByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("读取用户 %s: %v", username, err)
	}
	return u
}

func TestUsersRolesRequireAuth(t *testing.T) {
	p := newPlatformServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodPatch, "/api/v1/users/u-1"},
		{http.MethodDelete, "/api/v1/users/u-1"},
		{http.MethodGet, "/api/v1/roles"},
	} {
		body := ""
		if tc.method == http.MethodPost {
			body = `{"username":"x","password":"password123","roleId":"r"}`
		}
		resp, raw := p.do(t, tc.method, tc.path, "", body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d: %s", tc.method, tc.path, resp.StatusCode, raw)
		}
	}
}

// TestUsersAdminCRUD 覆盖 admin 的创建/查重/列表/更新（改名+改密）/停用/删除主路径。
func TestUsersAdminCRUD(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	userRole := roleIDByName(t, p.store, auth.RoleUser)

	createBody := fmt.Sprintf(`{"username":"alice","displayName":"Alice","password":"secret123","roleId":"%s"}`, userRole)
	resp, raw := p.do(t, http.MethodPost, "/api/v1/users", token, createBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建用户应 201, got %d: %s", resp.StatusCode, raw)
	}
	var created store.User
	decodeData(t, mustReplay(t, raw), &created)
	if created.ID == "" || created.Username != "alice" || created.RoleID != userRole || created.Status != "active" {
		t.Fatalf("创建结果不符: %+v", created)
	}

	// 重名 409。
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/users", token, createBody); resp.StatusCode != http.StatusConflict {
		t.Fatalf("重名应 409, got %d: %s", resp.StatusCode, raw)
	}

	// 列表可读。
	resp, raw = p.do(t, http.MethodGet, "/api/v1/users", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /users 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "alice") {
		t.Fatal("用户列表未包含 alice")
	}

	// 角色列表含三角色。
	resp, raw = p.do(t, http.MethodGet, "/api/v1/roles", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /roles 应 200, got %d: %s", resp.StatusCode, raw)
	}
	for _, want := range []string{auth.RoleAdmin, auth.RoleUser, auth.RoleOperator} {
		if !strings.Contains(raw, `"name":"`+want+`"`) {
			t.Fatalf("角色列表缺少 %q", want)
		}
	}

	// 改名 + 改密。
	patchBody := `{"displayName":"Alice Renamed","password":"newpass456"}`
	resp, raw = p.do(t, http.MethodPatch, "/api/v1/users/"+created.ID, token, patchBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("更新用户应 200, got %d: %s", resp.StatusCode, raw)
	}
	var updated store.User
	decodeData(t, mustReplay(t, raw), &updated)
	if updated.DisplayName != "Alice Renamed" {
		t.Fatalf("displayName 未更新: %+v", updated)
	}
	// 新密码可登录。
	if resp, _, _ := login(t, p.ts, `{"username":"alice","password":"newpass456"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("改密后新密码应可登录, got %d", resp.StatusCode)
	}

	// 停用后登录被拒。
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+created.ID, token, `{"status":"disabled"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("停用应 200, got %d: %s", resp.StatusCode, raw)
	}
	if resp, _, _ := login(t, p.ts, `{"username":"alice","password":"newpass456"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("停用用户登录应 401, got %d", resp.StatusCode)
	}

	// 删除后不再出现。
	if resp, raw := p.do(t, http.MethodDelete, "/api/v1/users/"+created.ID, token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200, got %d: %s", resp.StatusCode, raw)
	}
	resp, raw = p.do(t, http.MethodGet, "/api/v1/users", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /users 应 200, got %d", resp.StatusCode)
	}
	if strings.Contains(raw, "alice") {
		t.Fatal("删除后用户列表仍含 alice")
	}
}

// TestUsersPermissionDenied 验证权限判定落在 Permission 层：
// user / operator 不持 user.manage，全部返回 403 且不产生副作用。
func TestUsersPermissionDenied(t *testing.T) {
	p := newPlatformServer(t)
	tokens := map[string]string{}
	for _, role := range []string{auth.RoleUser, auth.RoleOperator} {
		tokens[role] = createUserWithRole(t, p, "denied-"+role, role)
	}
	// 基线 = bootstrap admin + 两个无权限测试账号；越权请求后数量不得变化。
	before, err := p.store.Users().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for role, token := range tokens {
		if resp, raw := p.do(t, http.MethodGet, "/api/v1/users", token, ""); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s 读取用户应 403, got %d: %s", role, resp.StatusCode, raw)
		}
		if resp, raw := p.do(t, http.MethodPost, "/api/v1/users", token, `{"username":"x","password":"password123","roleId":"r"}`); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s 创建用户应 403, got %d: %s", role, resp.StatusCode, raw)
		}
		if resp, raw := p.do(t, http.MethodGet, "/api/v1/roles", token, ""); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s 读取角色应 403, got %d: %s", role, resp.StatusCode, raw)
		}
	}
	// 越权尝试不得产生任何用户：数量与前置基线一致。
	after, err := p.store.Users().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("越权创建泄入 Store, users %d -> %d", len(before), len(after))
	}
}

// TestUsersProtectedAdmin 验证保守保护：内置 admin 不可删除；
// 登录用户禁止停用或降级自己的账号。
func TestUsersProtectedAdmin(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	admin := userByUsername(t, p.store, auth.DefaultAdminUsername)
	userRole := roleIDByName(t, p.store, auth.RoleUser)
	adminRole := roleIDByName(t, p.store, auth.RoleAdmin)

	// 内置 admin 自己不能停用/降级自己（先命中当前用户保护 400）。
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID, token, `{"status":"disabled"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("停用自己的账号应 400, got %d: %s", resp.StatusCode, raw)
	}
	patchSelf := fmt.Sprintf(`{"roleId":"%s"}`, userRole)
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID, token, patchSelf); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("降级自己的账号应 400, got %d: %s", resp.StatusCode, raw)
	}

	// 以第二个 admin 验证服务层保守保护：内置 admin 不可删除/停用/降级（403）。
	admin2Body := fmt.Sprintf(`{"username":"admin2","displayName":"Admin 2","password":"admin2-pw-123","roleId":"%s"}`, adminRole)
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/users", token, admin2Body); resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建 admin2 应 201, got %d: %s", resp.StatusCode, raw)
	}
	token2 := p.tokenAs(t, "admin2", "admin2-pw-123")
	if resp, raw := p.do(t, http.MethodDelete, "/api/v1/users/"+admin.ID, token2, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("删除内置 admin 应 403, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID, token2, `{"status":"disabled"}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("停用内置 admin 应 403, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID, token2, patchSelf); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("降级内置 admin 应 403, got %d: %s", resp.StatusCode, raw)
	}
}

// TestUsersNeverEchoPassword 是红线用例：创建/列表响应不得出现明文口令，
// 且解码出的 PasswordHash 字段必须为空（json:"-" 已遮挡）。
func TestUsersNeverEchoPassword(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	userRole := roleIDByName(t, p.store, auth.RoleUser)
	const plain = "s3cret-pass-987"

	body := fmt.Sprintf(`{"username":"bob","displayName":"Bob","password":"%s","roleId":"%s"}`, plain, userRole)
	resp, raw := p.do(t, http.MethodPost, "/api/v1/users", token, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建用户应 201, got %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, plain) {
		t.Fatal("创建响应泄漏口令明文")
	}
	var u store.User
	decodeData(t, mustReplay(t, raw), &u)
	if u.PasswordHash != "" {
		t.Fatal("响应回显了 PasswordHash")
	}

	resp, raw = p.do(t, http.MethodGet, "/api/v1/users", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /users 应 200, got %d", resp.StatusCode)
	}
	if strings.Contains(raw, plain) {
		t.Fatal("用户列表泄漏口令明文")
	}
}
