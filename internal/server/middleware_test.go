package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

func TestRequestIDGenerated(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	RequestID(okHandler()).ServeHTTP(rr, req)

	rid := rr.Header().Get(HeaderRequestID)
	if rid == "" {
		t.Fatal("应生成 X-Request-ID")
	}
	if RequestIDFrom(req.Context()) != "" {
		t.Fatal("不能用修改前的请求上下文读取 request_id")
	}
}

func TestRequestIDInContext(t *testing.T) {
	var got string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set(HeaderRequestID, "rid-42")
	RequestID(h).ServeHTTP(rr, req)
	if got != "rid-42" {
		t.Fatalf("request_id 注入错误, got %q", got)
	}
	if rr.Header().Get(HeaderRequestID) != "rid-42" {
		t.Fatalf("响应头缺少 request_id, got %q", rr.Header().Get(HeaderRequestID))
	}
}

func TestCORSPreflightAllowed(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/scans", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	CORS(DefaultCORS())(okHandler()).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight 应返回 204, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Allow-Origin 不正确, got %q", got)
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("应声明允许的 HTTP 方法")
	}
}

func TestCORSDeniedOrigin(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scans", nil)
	req.Header.Set("Origin", "https://evil.example")
	CORS(DefaultCORS())(okHandler()).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("不允许的来源仍应透传实际请求, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("不应放行未允许的来源, got %q", got)
	}
}

func TestCORSAllowAll(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scans", nil)
	req.Header.Set("Origin", "https://anything.example")
	CORS(CORSConfig{AllowAllOrigins: true})(okHandler()).ServeHTTP(rr, req)
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.example" {
		t.Fatalf("AllowAllOrigins 应放行任意来源, got %q", got)
	}
}

func TestAuthPlaceholderBearer(t *testing.T) {
	var token string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = BearerTokenFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.Header.Set(HeaderAuthorization, "Bearer tok-321")
	AuthPlaceholder(h).ServeHTTP(rr, req)

	if token != "tok-321" {
		t.Fatalf("Bearer Token 未注入上下文, got %q", token)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("占位中间件不应拒绝请求, got %d", rr.Code)
	}
}

func TestAuthPlaceholderCookie(t *testing.T) {
	var token string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = BearerTokenFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.AddCookie(&http.Cookie{Name: cookieToken, Value: "cookie-tok"})
	AuthPlaceholder(h).ServeHTTP(rr, req)
	if token != "cookie-tok" {
		t.Fatalf("Cookie Token 未注入上下文, got %q", token)
	}
}

func TestAuthPlaceholderNoToken(t *testing.T) {
	var token string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = BearerTokenFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	AuthPlaceholder(h).ServeHTTP(rr, req)
	if token != "" {
		t.Fatalf("无凭据时不应注入 Token, got %q", token)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("无凭据也不应拒绝请求, got %d", rr.Code)
	}
}
