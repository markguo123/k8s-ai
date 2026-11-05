package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func builtDist() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>k8s-ai</title>")},
		"assets/app.abc1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
	}
}

func TestStaticHandlerServesBuiltAssets(t *testing.T) {
	h := NewStaticHandler(builtDist())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "k8s-ai") {
		t.Fatalf("根路径应回 index.html: %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != cacheNoCache {
		t.Fatalf("index.html 应 no-cache, got %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("静态响应应带 nosniff, got %q", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app.abc1.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("hash 资源应命中, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("assets 应 long-lived immutable, got %q", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if got := rec.Header().Get("Cache-Control"); got != cacheStaticShort {
		t.Fatalf("其余静态资源应短缓存, got %q", got)
	}
}

// TestStaticHandlerSPAFallback 锁前端 history 路由：未命中的无扩展名路径回 index.html，
// 未命中的带扩展名路径回 JSON 404（缺文件不能被伪装成首页）。
func TestStaticHandlerSPAFallback(t *testing.T) {
	h := NewStaticHandler(builtDist())

	for _, p := range []string{"/sessions/abc123", "/clusters/current", "/new-route"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "k8s-ai") {
			t.Fatalf("%s 应回退 index.html: %d %q", p, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != cacheNoCache {
			t.Fatalf("fallback 必须 no-cache, got %q", got)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing.hash1.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失资源应 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("缺失资源应回 JSON, got %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), codeNotFound) {
		t.Fatalf("应带统一 404 错误码, got %s", rec.Body.String())
	}
}

// TestStaticHandlerRejectsUnbuiltTree 锁干净检出体验：没有 index.html 时给明确提示，
// 不返回空白 200 或裸 404。
func TestStaticHandlerRejectsUnbuiltTree(t *testing.T) {
	for name, dist := range map[string]fstest.MapFS{
		"only placeholder": {".gitkeep": {Data: []byte("")}},
		"assets only":      {"assets/app.js": {Data: []byte("x")}},
	} {
		h := NewStaticHandler(dist)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s 应 503, got %d", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "npm run build") {
			t.Fatalf("%s 应给出可操作提示, got %q", name, rec.Body.String())
		}
	}

	// dist 为 nil（未装配内嵌产物）同样给明确提示。
	rec := httptest.NewRecorder()
	NewStaticHandler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil dist 应 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "static_enabled") {
		t.Fatalf("提示应指向形态 B 部署开关, got %q", rec.Body.String())
	}
}

// TestStaticHandlerMethodGate 锁静态路由只读。
func TestStaticHandlerMethodGate(t *testing.T) {
	h := NewStaticHandler(builtDist())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 静态路径应 405, got %d", rec.Code)
	}
	if rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("405 应带 Allow 头, got %q", rec.Header().Get("Allow"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD 应可用, got %d", rec.Code)
	}
}
