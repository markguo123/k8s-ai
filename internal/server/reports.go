package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// codeGone 用于「报告业务行还在、内容已不可恢复」（410）。它和 404 的区别对运维是
// 有意义的：404 表示这条引用不存在（可能压根没这个 id），410 表示它存在过、内容没了。
const codeGone = "gone"

// blobRetryAfter 是对象存储降级时给客户端的重试建议（秒），与 service 侧的懒恢复节流
// 间隔同一量级：更早重试只会再撞一次同一个坏掉的桶。
const blobRetryAfter = "30"

// reportService 是报告端点依赖的最小接口（service.ReportPlatform 满足）。签名里带
// objectstore.Ref 是有意的：下载头需要 Content-Length 与 Content-Type，而这些只有内容
// 后端给出的 Ref 才是权威的，让 service 再造一个结构体只会多一次字段搬运。
type reportService interface {
	List(ctx context.Context, scope service.ReportScope, limit, offset int) ([]service.ReportView, int64, error)
	OpenContent(ctx context.Context, id string, scope service.ReportScope) (service.ReportView, io.ReadCloser, objectstore.Ref, error)
	Presign(ctx context.Context, id string, scope service.ReportScope, ttl time.Duration) (service.ReportView, string, error)
}

// handleListReports GET /api/v1/reports：只列本人 + 有权集群的系统报告。
//
// 权限位是 AccessAuthenticated 而不是某个 report.read：四期没有独立的「报告」权限域，
// 报告的可见性由 owner 与集群授权决定（方案 15 §12.2）。在这里加一个恒真的权限位只会
// 让人误以为存在一个可以授予的「读所有报告」开关。
func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	if s.reportSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "reports is not configured")
		return
	}
	scope, _, err := s.reportScopeFor(r.Context())
	if err != nil {
		writeReportScopeError(w, r, err)
		return
	}
	if clusterID := r.URL.Query().Get("clusterId"); clusterID != "" {
		scope.ClusterID = clusterID
	}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		scope.Kind = kind
	}
	page, pageSize := PageOf(r)
	items, total, err := s.reportSvc.List(r.Context(), scope, pageSize, (page-1)*pageSize)
	if err != nil {
		slog.Warn("server: 报告列表读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "list reports failed")
		return
	}
	writePage(w, r, http.StatusOK, items, total)
}

// handleGetReportContent GET /api/v1/reports/{id}/content?mode=stream|presign：
// 下载报告正文。mode=presign 只有 s3 后端支持，其余后端回 400 指回 mode=stream。
func (s *Server) handleGetReportContent(w http.ResponseWriter, r *http.Request) {
	if s.reportSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "reports is not configured")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "stream"
	}
	if mode != "stream" && mode != "presign" {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, `mode must be "stream" or "presign"`)
		return
	}
	scope, user, err := s.reportScopeFor(r.Context())
	if err != nil {
		writeReportScopeError(w, r, err)
		return
	}
	id := r.PathValue("id")

	if mode == "presign" {
		// ttl 传 0：签名有效期是后端的部署参数（platform.blobs.presign_ttl），
		// 让请求方自选时长会把一个运维旋钮变成"能不能签一条长期有效链接"的攻击面。
		view, url, err := s.reportSvc.Presign(r.Context(), id, scope, 0)
		if err != nil {
			writeReportError(w, r, err)
			return
		}
		s.recordReportDownload(r, user, view)
		// URL 含签名：只进本次响应体，不落库、不进 SSE、不进审计、不进日志（§13.2）。
		writeOK(w, r, http.StatusOK, map[string]any{"report": view, "downloadUrl": url})
		return
	}

	view, rc, ref, err := s.reportSvc.OpenContent(r.Context(), id, scope)
	if err != nil {
		writeReportError(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()

	// 强制下载 + 禁 MIME 嗅探：报告正文来自集群数据，绝不能让浏览器把它当同源脚本渲染。
	h := w.Header()
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+reportFilename(view, ref))
	h.Set("X-Content-Type-Options", "nosniff")
	mediaType := ref.MediaType
	if mediaType == "" {
		mediaType = view.MediaType
	}
	if mediaType != "" {
		h.Set("Content-Type", mediaType)
	}
	if ref.Size > 0 {
		h.Set("Content-Length", strconv.FormatInt(ref.Size, 10))
	}
	s.recordReportDownload(r, user, view)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		// 状态行已经写出，改不了响应码；留下日志是这里唯一诚实的收尾。
		slog.Warn("server: 报告正文流式传输中断", "report_id", id, "error", err)
	}
}

// reportScopeFor 把认证上下文翻译成报告可见性范围（方案 15 §12.2 三层判定）：
// 角色含 * 走 admin；否则取本人 owner + AuthzGrant 授权的集群集合。
func (s *Server) reportScopeFor(ctx context.Context) (service.ReportScope, *store.User, error) {
	u := UserFrom(ctx)
	if u == nil {
		return service.ReportScope{}, nil, errNoPrincipal
	}
	role := RoleFrom(ctx)
	scope := service.ReportScope{ViewerID: u.ID}
	if role == nil {
		// 没有角色就只看得见自己的报告：这里不能当成"无限制"，未授权角色读到系统报告
		// 是最典型的一种权限倒挂。
		return scope, u, nil
	}
	if permission.HasPermission(role.Permissions, "*") {
		scope.Admin = true
		return scope, u, nil
	}
	if s.authzSvc == nil {
		return scope, u, nil
	}
	all, ids, err := s.authzSvc.AuthorizedClusterIDs(ctx, u, role)
	if err != nil {
		return service.ReportScope{}, u, err
	}
	scope.AllSystemClusters = all
	scope.SystemClusterIDs = ids
	return scope, u, nil
}

var errNoPrincipal = errors.New("server: no authenticated principal")

func writeReportScopeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNoPrincipal) {
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	slog.Warn("server: 报告授权范围解析失败", "error", err)
	writeError(w, r, http.StatusInternalServerError, codeInternal, "resolve report scope failed")
}

// writeReportError 把报告读取错误映射为统一错误信封。顺序上先判不可用类（503）再判
// 业务态（410/409），最后才落到 404/500：越权同形 404 由 service 层用 store.ErrNotFound
// 表达，这里不需要再区分。
func writeReportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, objectstore.ErrUnavailable), errors.Is(err, objectstore.ErrLocked):
		w.Header().Set("Retry-After", blobRetryAfter)
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable,
			"object storage is not available right now; chat, approval and audit are unaffected")
	case errors.Is(err, service.ErrReportUnavailable):
		writeError(w, r, http.StatusGone, codeGone, err.Error())
	case errors.Is(err, service.ErrObjectDriverMismatch):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "report not found")
	case errors.Is(err, objectstore.ErrUnsupported):
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
	case errors.Is(err, objectstore.ErrDisabled):
		writeError(w, r, http.StatusNotImplemented, codeInternal,
			"object storage is disabled (platform.blobs.driver=off); reports are not archived")
	case errors.Is(err, objectstore.ErrKeyInvalid):
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
	default:
		slog.Warn("server: 报告内容读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "read report content failed")
	}
}

// recordReportDownload 只在跨 owner 读取时留审计（含读系统报告）：用户下自己的报告
// 是日常动作，把它全量写进审计表只会把真正需要追责的那条——管理员翻别人的体检结论——
// 淹没在噪声里（方案 15 §12.4）。
func (s *Server) recordReportDownload(r *http.Request, user *store.User, view service.ReportView) {
	if s.auditlog == nil || user == nil || view.CreatedBy == user.ID {
		return
	}
	err := s.auditlog.Record(r.Context(), store.AuditEvent{
		Action:    store.ActionReportDownload,
		EventType: store.AuditEventReport,
		RequestID: RequestIDFrom(r.Context()),
		UserID:    user.ID,
		ClusterID: view.ClusterID,
		Operation: "download",
		// 报告不是集群里的 Kubernetes 资源，但沿用 resource_kind/name 这一对字段描述
		// 被操作对象，审计查询侧就不必为报告单开一套列。
		ResourceKind:  "report",
		ResourceName:  view.ID,
		Status:        "success",
		ResultSummary: fmt.Sprintf(`{"kind":%q,"owner":%q,"size":%d}`, view.Kind, view.CreatedBy, view.Size),
	})
	if err != nil {
		slog.Warn("server: 报告下载审计写入失败", "report_id", view.ID, "user_id", user.ID, "error", err)
	}
}

// reportFilename 生成下载文件名。收敛到 [A-Za-z0-9._-] 之后不需要真正的百分号编码
// （RFC 5987 的 attr-char 已覆盖这个集合），这样既满足 filename* 语法，也不会因为
// kind/ID 里混进引号或换行而把响应头写坏——头部注入在这里是一行就能打开的洞。
func reportFilename(view service.ReportView, ref objectstore.Ref) string {
	name := "report-" + view.ID + "-" + view.Kind + mediaExt(view, ref)
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "report.bin"
	}
	return string(out)
}

// mediaExt 只给常见类型配扩展名；认不出来就不加，好过编一个误导性的。
func mediaExt(view service.ReportView, ref objectstore.Ref) string {
	mt := ref.MediaType
	if mt == "" {
		mt = view.MediaType
	}
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = mt[:i]
	}
	switch mt {
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	default:
		return ""
	}
}
