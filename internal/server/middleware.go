package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	// HeaderRequestID 请求号请求/响应头。
	HeaderRequestID = "X-Request-ID"
	// HeaderAuthorization 认证请求头。
	HeaderAuthorization = "Authorization"

	cookieToken = "k8sai_token"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyBearerToken
)

// CORSConfig 配置跨域策略。默认放行本地 Vite dev server。
type CORSConfig struct {
	// AllowedOrigins 精确允许的来源（完整 Origin，如 http://localhost:5173）。
	AllowedOrigins []string
	// AllowAllOrigins 为 true 时允许任意来源（仅调试用）。
	AllowAllOrigins bool
}

// DefaultCORS 返回默认策略：放行本地开发前端。
func DefaultCORS() CORSConfig {
	return CORSConfig{
		AllowedOrigins: []string{
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		},
	}
}

// WithMiddleware 挂载 request_id / 认证占位 / CORS 中间件。
func WithMiddleware(next http.Handler, cors CORSConfig) http.Handler {
	return CORS(cors)(AuthPlaceholder(RequestID(next)))
}

// RequestID 从 X-Request-ID 读取或生成请求号，写入上下文并回写响应头。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := strings.TrimSpace(r.Header.Get(HeaderRequestID))
		if rid == "" {
			rid = newRequestID()
		}
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		w.Header().Set(HeaderRequestID, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom 从上下文取请求号；无则返回空字符串。
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// AuthPlaceholder 解析 Bearer Token / 登录 Cookie 并写入上下文。
// 仅做占位，不校验、不拒绝：真实认证与 401/403 判定在 P1.5/P1.6。
func AuthPlaceholder(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if token := bearerToken(r); token != "" {
			ctx = context.WithValue(ctx, ctxKeyBearerToken, token)
		} else if c, err := r.Cookie(cookieToken); err == nil && c.Value != "" {
			ctx = context.WithValue(ctx, ctxKeyBearerToken, c.Value)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// BearerTokenFrom 从上下文取占位阶段解析出的 Token；无则返回空字符串。
func BearerTokenFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyBearerToken).(string); ok {
		return v
	}
	return ""
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get(HeaderAuthorization)
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// CORS 是跨域中间件：允许的来源放行预检与实际请求，其余请求透传。
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowsOrigin(r, cfg, origin) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
				h.Set("Access-Control-Max-Age", "86400")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func allowsOrigin(r *http.Request, cfg CORSConfig, origin string) bool {
	if cfg.AllowAllOrigins {
		return true
	}
	if slices.Contains(cfg.AllowedOrigins, origin) {
		return true
	}
	u, err := url.Parse(origin)
	if err == nil && u.Host != "" && u.Host == r.Host {
		return true
	}
	return slices.Contains(cfg.AllowedOrigins, originAsHost(origin))
}

// originAsHost 兼容配置里只写 host:port 的情况。
func originAsHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Host
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
