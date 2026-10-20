package server

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/authz"
)

func authzEndpoints(userID, id string) []struct{ method, path, body string } {
	return []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/grants", ""},
		{http.MethodPost, "/api/v1/grants", `{"principalType":"user","principalId":"` + userID + `","namespaces":["ns-a"],"permissions":["*"]}`},
		{http.MethodGet, "/api/v1/grants/" + id, ""},
		{http.MethodPatch, "/api/v1/grants/" + id, `{"namespaces":["ns-b"]}`},
		{http.MethodDelete, "/api/v1/grants/" + id, ""},
		{http.MethodGet, "/api/v1/users/" + userID + "/tokens", ""},
		{http.MethodPost, "/api/v1/users/" + userID + "/tokens", `{"name":"cli"}`},
		{http.MethodPatch, "/api/v1/users/" + userID + "/tokens/" + id, `{"status":"revoked"}`},
		{http.MethodDelete, "/api/v1/users/" + userID + "/tokens/" + id, ""},
	}
}

// TestAuthzRequireAuth 全部 grants/tokens 端点未登录应 401。
func TestAuthzRequireAuth(t *testing.T) {
	p := newPlatformServer(t)
	for _, tc := range authzEndpoints("u-1", "a-1") {
		resp, raw := p.do(t, tc.method, tc.path, "", tc.body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d: %s", tc.method, tc.path, resp.StatusCode, raw)
		}
	}
}

// TestAuthzGrantAdminCRUD admin 的授权委派全生命周期主路径。
func TestAuthzGrantAdminCRUD(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	body := `{"principalType":"user","principalId":"p-user-1","clusterId":"c-prod","namespaces":["app-a","app-b"],"permissions":["*","destroy"]}`
	resp, raw := p.do(t, http.MethodPost, "/api/v1/grants", token, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建授权应 201, got %d: %s", resp.StatusCode, raw)
	}
	var created authz.Grant
	decodeData(t, mustReplay(t, raw), &created)
	if created.ID == "" || created.PrincipalType != "user" || !reflect.DeepEqual(created.Namespaces, []string{"app-a", "app-b"}) {
		t.Fatalf("创建结果不符: %+v", created)
	}
	if created.CreatedBy == "" {
		t.Fatal("CreatedBy 应为当前操作者")
	}

	resp, raw = p.do(t, http.MethodGet, "/api/v1/grants/"+created.ID, token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /grants/{id} 应 200, got %d: %s", resp.StatusCode, raw)
	}
	resp, raw = p.do(t, http.MethodGet, "/api/v1/grants", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /grants 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "app-b") {
		t.Fatal("授权列表未包含 namespace")
	}

	resp, raw = p.do(t, http.MethodPatch, "/api/v1/grants/"+created.ID, token, `{"namespaces":["app-c"],"permissions":["destroy"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /grants/{id} 应 200, got %d: %s", resp.StatusCode, raw)
	}
	var updated authz.Grant
	decodeData(t, mustReplay(t, raw), &updated)
	if !reflect.DeepEqual(updated.Namespaces, []string{"app-c"}) || !reflect.DeepEqual(updated.Permissions, []string{"destroy"}) {
		t.Fatalf("更新结果不符: %+v", updated)
	}

	if resp, raw := p.do(t, http.MethodDelete, "/api/v1/grants/"+created.ID, token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /grants/{id} 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodGet, "/api/v1/grants/"+created.ID, token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后 GET 应 404, got %d: %s", resp.StatusCode, raw)
	}

	// 非法授权应 400 且不落库。
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/grants", token, `{"principalType":"user","principalId":"p","namespaces":["x"],"permissions":["watch"]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 permissions 应 400, got %d: %s", resp.StatusCode, raw)
	}
}

// TestAuthzTokenCRUDHashNeverEchoed token 生命周期与“明文仅创建返回一次”。
func TestAuthzTokenCRUDHashNeverEchoed(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	admin := userByUsername(t, p.store, auth.DefaultAdminUsername)

	resp, raw := p.do(t, http.MethodPost, "/api/v1/users/"+admin.ID+"/tokens", token, `{"name":"local-cli","expiresAt":"2030-01-01T00:00:00Z"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建 token 应 201, got %d: %s", resp.StatusCode, raw)
	}
	var result authz.CreateTokenResult
	decodeData(t, mustReplay(t, raw), &result)
	if result.Token == "" || result.APIToken == nil || result.APIToken.ID == "" {
		t.Fatalf("创建响应缺少明文或元数据: %+v", result)
	}
	plain := result.Token

	// 列表中绝不能出现明文 token。
	resp, raw = p.do(t, http.MethodGet, "/api/v1/users/"+admin.ID+"/tokens", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tokens 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, plain) {
		t.Fatal("token 列表回显了明文")
	}
	if strings.Contains(raw, "tokenHash") {
		t.Fatal("token 列表回显了 tokenHash 字段")
	}
	if !strings.Contains(raw, `"status":"active"`) {
		t.Fatal("新 token 状态应为 active")
	}

	// 停用后状态更新；明文无论何时都不再返回。
	resp, raw = p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID+"/tokens/"+result.APIToken.ID, token, `{"status":"disabled"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH token 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, plain) {
		t.Fatal("更新响应泄漏明文")
	}

	// 删除成功后再读为空/404。
	if resp, raw := p.do(t, http.MethodDelete, "/api/v1/users/"+admin.ID+"/tokens/"+result.APIToken.ID, token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE token 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if _, err := p.store.ApiTokens().Get(context.Background(), result.APIToken.ID); err == nil {
		t.Fatal("删除后 token 仍存在")
	}

	// 非法状态 400；已删除 token 的更新 404。
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID+"/tokens/"+result.APIToken.ID, token, `{"status":"bogus"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法状态应 400, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/users/"+admin.ID+"/tokens/"+result.APIToken.ID, token, `{"status":"revoked"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后更新应 404, got %d: %s", resp.StatusCode, raw)
	}
}

// TestAuthzPermissionDenied user/operator 无 user.manage，全部端点 403 且无副作用。
func TestAuthzPermissionDenied(t *testing.T) {
	p := newPlatformServer(t)
	tokens := map[string]string{}
	for _, role := range []string{auth.RoleUser, auth.RoleOperator} {
		tokens[role] = createUserWithRole(t, p, "denied-"+role, role)
	}
	admin := userByUsername(t, p.store, auth.DefaultAdminUsername)

	gBefore, _ := p.store.AuthzGrants().List(context.Background())
	tBefore, _ := p.store.ApiTokens().ListByUser(context.Background(), admin.ID)
	for role, token := range tokens {
		for _, tc := range authzEndpoints(admin.ID, "a-123") {
			resp, raw := p.do(t, tc.method, tc.path, token, tc.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s %s 应 403, got %d: %s", role, tc.method, tc.path, resp.StatusCode, raw)
			}
		}
	}
	gAfter, _ := p.store.AuthzGrants().List(context.Background())
	tAfter, _ := p.store.ApiTokens().ListByUser(context.Background(), admin.ID)
	if len(gAfter) != len(gBefore) || len(tAfter) != len(tBefore) {
		t.Fatalf("越权请求产生副作用: grants %d->%d, tokens %d->%d", len(gBefore), len(gAfter), len(tBefore), len(tAfter))
	}
}

// TestAuthzWriteInvalidBody 验证非法 JSON/缺失字段的收敛路径。
func TestAuthzWriteInvalidBody(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	admin := userByUsername(t, p.store, auth.DefaultAdminUsername)

	if resp, raw := p.do(t, http.MethodPost, "/api/v1/grants", token, `{`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/grants", token, `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空请求体应 400, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/users/"+admin.ID+"/tokens", token, `{"expiresAt":"2000-01-01T00:00:00Z"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("过期时间应 400, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodGet, "/api/v1/users/user-missing/tokens", token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的用户应 404, got %d: %s", resp.StatusCode, raw)
	}
}

