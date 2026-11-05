package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// 静态资源托管（P6.11 / D-26 / D-30，形态 A：同端口提供 SPA 与 API）。
//
// 缓存策略：
//   - index.html 与 SPA fallback：no-cache，发版后立刻拿到新的资源 hash；
//   - /assets/*：vite 产物文件名带内容 hash，可 long-lived immutable；
//   - 其余静态文件（favicon、字体等）：短缓存。
const (
	indexFile        = "index.html"
	cacheNoCache     = "no-cache"
	cacheImmutable   = "public, max-age=31536000, immutable"
	cacheStaticShort = "public, max-age=300"
)

// notBuiltMessage 是干净检出（未跑前端构建）时的明确提示，避免用户对着空白页猜原因。
const notBuiltMessage = "front-end assets are not embedded in this binary: run npm ci && npm run build in web/ before go build, " +
	"or deploy the separate web image and set platform.server.static_enabled to false\n"

// NewStaticHandler 返回内嵌前端产物的处理器。
//
// 行为约定：
//   - dist 为 nil 或没有 index.html（未构建）→ 503 + 明确提示，不返回空白 404；
//   - 未命中的无扩展名路径 → 回 index.html（SPA history 路由 fallback）；
//   - 未命中的带扩展名路径 → 统一 JSON 404，不 HTML 兜底（便于前端定位缺文件）；
//   - 只接受 GET/HEAD。
func NewStaticHandler(dist fs.FS) http.Handler {
	if dist == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeNotBuilt(w)
		})
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, codeInvalidArgument, "static assets allow GET and HEAD only")
			return
		}
		if !hasEntry(dist, indexFile) {
			writeNotBuilt(w)
			return
		}
		name := staticName(r.URL.Path)
		if name != "" && !hasEntry(dist, name) {
			if path.Ext(name) != "" {
				writeError(w, r, http.StatusNotFound, codeNotFound, "asset not found: /"+name)
				return
			}
			// SPA fallback：前端路由自己决定渲染什么。
			setStaticCache(w, indexFile)
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			files.ServeHTTP(w, r)
			return
		}
		setStaticCache(w, name)
		files.ServeHTTP(w, r)
	})
}

// staticName 把 URL 路径转成 fs.FS 的相对文件名（去掉前导 "/"，根路径返回空串）。
func staticName(urlPath string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))
	name := strings.TrimPrefix(cleaned, "/")
	if name == "." {
		return ""
	}
	return name
}

func hasEntry(fsys fs.FS, name string) bool {
	if name == "" {
		return false
	}
	_, err := fs.Stat(fsys, name)
	return err == nil
}

// NewNotFoundHandler 返回统一 JSON 404 处理器（P6.11）：形态 B 下静态资源交给
// 独立前端镜像，后端收到的非 API 路径同样必须回 JSON，不能吐 HTML。
func NewNotFoundHandler() http.Handler {
	s := &Server{}
	return http.HandlerFunc(s.handleAPINotFound)
}

func setStaticCache(w http.ResponseWriter, name string) {
	switch {
	case name == indexFile || name == "":
		w.Header().Set("Cache-Control", cacheNoCache)
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", cacheImmutable)
	default:
		w.Header().Set("Cache-Control", cacheStaticShort)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeNotBuilt(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", cacheNoCache)
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(notBuiltMessage))
}
