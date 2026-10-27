package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder 返回只登记"被命中"的 handler，用于断言顶层路由分派。
func recorder(name string, got *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = name
		w.WriteHeader(http.StatusTeapot)
	})
}

// TestNewRootHandlerRouting 锁 P6.11 / D-26 的路由边界：会话 API、平台 API、探针端点、
// 内嵌前端四条通道互不串台。重点是任何 /api/v1/* 都必须进平台 API 处理链，
// 即使静态托管开着，也不能被 SPA fallback 接走（否则前端 fetch 会拿到 index.html）。
func TestNewRootHandlerRouting(t *testing.T) {
	cases := []struct {
		method, path  string
		staticEnabled bool
		want          string
	}{
		{http.MethodGet, "/api/v1/sessions", true, "session"},
		{http.MethodPost, "/api/v1/sessions/s-1/messages", true, "session"},
		{http.MethodGet, "/api/v1/users", true, "platform"},
		{http.MethodGet, "/api/v1/nope", true, "platform"},
		{http.MethodGet, "/api/v1", true, "platform"},
		{http.MethodGet, "/healthz", true, "platform"},
		{http.MethodGet, "/readyz", false, "platform"},
		{http.MethodGet, "/version", true, "platform"},
		{http.MethodGet, "/", true, "static"},
		{http.MethodGet, "/settings/model", true, "static"},
		{http.MethodGet, "/assets/index-abc123.js", true, "static"},
		{http.MethodGet, "/settings/model", false, "notfound"},
		{http.MethodGet, "/api/v1/nope", false, "platform"},
	}
	for _, tc := range cases {
		var got string
		h := newRootHandler(
			recorder("platform", &got),
			recorder("session", &got),
			recorder("static", &got),
			tc.staticEnabled,
		)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if tc.want == "notfound" {
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s（静态关闭）应 JSON 404, got %d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
				t.Fatalf("静态关闭时非 API 路径必须回 JSON, got %q", ct)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("%s %s（staticEnabled=%v）应派发到 %s, got %q", tc.method, tc.path, tc.staticEnabled, tc.want, got)
		}
	}
}
