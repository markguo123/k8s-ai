package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

func newAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.OpenSQLite("file:server-auth-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
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
	ts := httptest.NewServer(NewWithAuth(&fakeScan{}, authSvc).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func login(t *testing.T, ts *httptest.Server, body string) (*http.Response, *apiResponse, map[string]any) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	env := decodeData(t, resp, nil)
	var payload map[string]any
	if env.Data != nil {
		raw, _ := json.Marshal(env.Data)
		_ = json.Unmarshal(raw, &payload)
	}
	return resp, env, payload
}

func TestAuthLoginMeLogout(t *testing.T) {
	ts := newAuthServer(t)

	resp, env, payload := login(t, ts, `{"username":"admin","password":"admin-pw"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login 应成功, got %d: %+v", resp.StatusCode, env.Error)
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatal("login 响应缺少 token")
	}
	if env.Error != nil {
		t.Fatalf("成功响应不应携带 error: %+v", env.Error)
	}

	// GET /me 携带 Bearer Token。
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/me", nil)
	req.Header.Set(HeaderAuthorization, "Bearer "+token)
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var me struct {
		User map[string]any `json:"user"`
		Role map[string]any `json:"role"`
	}
	decodeData(t, meResp, &me)
	if me.User["username"] != "admin" || me.Role["name"] != auth.RoleAdmin {
		t.Fatalf("/me 返回错误用户或角色: %+v", me)
	}

	// POST /logout 使 Token 失效。
	outReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/logout", nil)
	outReq.Header.Set(HeaderAuthorization, "Bearer "+token)
	outResp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		t.Fatal(err)
	}
	if outResp.StatusCode != http.StatusOK {
		t.Fatalf("logout 应成功, got %d", outResp.StatusCode)
	}

	// 登出后 /me 应 401。
	afterResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if afterResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("登出后 /me 应 401, got %d", afterResp.StatusCode)
	}
	afterEnv := decodeData(t, afterResp, nil)
	if afterEnv.Error == nil || afterEnv.Error.Code != codeUnauthorized {
		t.Fatalf("401 应带统一错误码: %+v", afterEnv.Error)
	}
}

func TestAuthLoginInvalidPassword(t *testing.T) {
	ts := newAuthServer(t)
	resp, env, _ := login(t, ts, `{"username":"admin","password":"wrong"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401, got %d", resp.StatusCode)
	}
	if env.Error == nil || env.Error.Code != codeUnauthorized {
		t.Fatalf("401 应带统一错误码: %+v", env.Error)
	}
}

func TestAuthLoginBadBody(t *testing.T) {
	ts := newAuthServer(t)
	resp, env, _ := login(t, ts, `{bad`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 body 应 400, got %d", resp.StatusCode)
	}
	if env.Error == nil || env.Error.Code != codeInvalidArgument {
		t.Fatalf("400 应带统一错误码: %+v", env.Error)
	}
}

func TestAuthMeWithoutToken(t *testing.T) {
	ts := newAuthServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录 /me 应 401, got %d", resp.StatusCode)
	}
	decodeData(t, resp, nil)
}

func TestAuthEndpointsNotConfigured(t *testing.T) {
	srv := New(&fakeScan{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/api/v1/auth/login", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未配置认证时应 501, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		t.Fatal("响应应含 JSON Content-Type")
	}
}
