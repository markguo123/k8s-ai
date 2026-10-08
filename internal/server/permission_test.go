package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

func newAuthedScanServer(t *testing.T, scan *fakeScan) (*httptest.Server, string, store.Store, *auth.Service) {
	t.Helper()
	st, err := store.OpenSQLite("file:server-perm-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
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
	srv := NewWithAuth(scan, authSvc)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	_, env, payload := login(t, ts, `{"username":"admin","password":"admin-pw"}`)
	if env.Error != nil {
		t.Fatalf("admin login failed: %+v", env.Error)
	}
	token, _ := payload["token"].(string)
	return ts, token, st, authSvc
}

func authedGet(t *testing.T, ts *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if token != "" {
		req.Header.Set(HeaderAuthorization, "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func authedPost(ts *httptest.Server, path, body, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(HeaderAuthorization, "Bearer "+token)
	}
	return http.DefaultClient.Do(req)
}

func TestPermissionGateRequiresAuth(t *testing.T) {
	ts := newAuthServer(t) // no token
	resp := authedGet(t, ts, "/api/v1/scans/nope", "")
	env := decodeData(t, resp, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录受保护路由应 401, got %d: %+v", resp.StatusCode, env.Error)
	}
	if env.Error == nil || env.Error.Code != codeUnauthorized {
		t.Fatalf("401 应带统一错误码: %+v", env.Error)
	}
}

func TestPermissionGateAllowsAdmin(t *testing.T) {
	ts, token, _, _ := newAuthedScanServer(t, &fakeScan{result: &model.ScanResult{}})
	meResp := authedGet(t, ts, "/api/v1/auth/me", token)
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("admin /me 应为 200, got %d", meResp.StatusCode)
	}
	decodeData(t, meResp, nil)
	resp, err := authedPost(ts, "/api/v1/scans", `{"namespace":"default"}`, token)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("admin 创建 scan 应 202, got %d", resp.StatusCode)
	}
	var job ScanJob
	decodeData(t, resp, &job)

	getResp := authedGet(t, ts, "/api/v1/scans/"+job.ID, token)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("admin 读取 scan 应 200, got %d", getResp.StatusCode)
	}
	decodeData(t, getResp, nil)
}

func TestPermissionGateDeniesViewer(t *testing.T) {
	ts, _, st, authSvc := newAuthedScanServer(t, &fakeScan{result: &model.ScanResult{}})
	token := createAndLogin(t, st, authSvc, ts, "viewer", "viewer-pw", auth.RoleViewer)

	// viewer 有 cluster.read，允许 GET scan。
	getResp := authedGet(t, ts, "/api/v1/scans/some-id", token)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("viewer GET scan 应放行并落到 404, got %d", getResp.StatusCode)
	}
	decodeData(t, getResp, nil)

	// viewer 无 chat.write，创建 scan 应 403。
	resp, err := authedPost(ts, "/api/v1/scans", `{"namespace":"default"}`, token)
	if err != nil {
		t.Fatal(err)
	}
	env := decodeData(t, resp, nil)
	if resp.StatusCode != http.StatusForbidden || env.Error == nil || env.Error.Code != codeForbidden {
		t.Fatalf("viewer 创建 scan 应 403 forbidden, got %d: %+v", resp.StatusCode, env.Error)
	}
}

// createAndLogin 在真实 Store 上创建指定角色用户并登录取得 Token。
func createAndLogin(t *testing.T, st store.Store, authSvc *auth.Service, ts *httptest.Server, username, password, roleName string) string {
	t.Helper()
	roles, err := st.Roles().List(context.Background())
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	var role *store.Role
	for _, r := range roles {
		if r.Name == roleName {
			role = r
			break
		}
	}
	if role == nil {
		t.Fatalf("role %s not found", roleName)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &store.User{
		ID:           "u-" + username,
		Username:     username,
		DisplayName:  username,
		PasswordHash: hash,
		RoleID:       role.ID,
		Status:       "active",
	}
	if err := st.Users().Create(context.Background(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	resp, env, payload := login(t, ts, `{"username":"`+username+`","password":"`+password+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s failed: %d %+v", username, resp.StatusCode, env.Error)
	}
	tok, _ := payload["token"].(string)
	if tok == "" {
		t.Fatalf("login %s missing token", username)
	}
	return tok
}
