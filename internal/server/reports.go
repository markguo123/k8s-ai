package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
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
	Delete(ctx context.Context, id string, scope service.ReportScope) (service.ReportView, error)
	Cleanup(ctx context.Context, ids []string, scope service.ReportScope) ([]service.ReportView, []service.CleanupSkip, error)
	// OpenStructuredReport 读出统一体检报告本体（P9 报告详情页）。它可能顺 scan_id 去开
	// 同一轮的 JSON 行，所以不能由 HTTP 层自己拼——只有 service 知道一次扫描的两行产物怎么配。
	OpenStructuredReport(ctx context.Context, id string, scope service.ReportScope) (service.StructuredReport, error)
	// Overview 给出可见报告的分类计数（P9.13 列表总览）。
	Overview(ctx context.Context, scope service.ReportScope) (service.ReportOverview, error)
	// ExportMarkdown 按需渲染一条报告的 Markdown 正文（P9.10）。
	ExportMarkdown(ctx context.Context, id string, scope service.ReportScope) (service.ReportView, []byte, error)
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
	// scanId 是「刚发起的体检出报告了没有」的找回路径（P8.13）：任务注册表在内存里，
	// 刷新页面就没了，靠 reports.scan_id 反查是唯一不引入新端点的做法。它是过滤条件，
	// 不是授权条件——猜中别人的 scanId 也只能看到自己名下的报告。
	if scanID := r.URL.Query().Get("scanId"); scanID != "" {
		scope.ScanID = scanID
	}
	// scopeCategory 是报告类别（cluster | business | other），与总览共用 store 里的
	// 一份分组口径；非法取值不报错，等同于不筛——它是一个下拉框，不是一个契约。
	if category := r.URL.Query().Get("scopeCategory"); category != "" {
		scope.ScopeCategory = category
	}
	page, pageSize := PageOf(r)
	items, total, err := s.reportSvc.List(r.Context(), scope, pageSize, (page-1)*pageSize)
	if err != nil {
		slog.Warn("server: 报告列表读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "list reports failed")
		return
	}
	writePage(w, r, http.StatusOK, s.withAuthorNames(r.Context(), items), total)
}

// handleReportSummary GET /api/v1/reports/summary：列表页顶部的分类总览。
//
// 单独一个端点而不是塞进列表响应：总览要的是「全部可见行」的分布，列表要的是当页内容，
// 两者的 SQL 形状不同；合在一个响应里只会让翻页时反复重算统计。
func (s *Server) handleReportSummary(w http.ResponseWriter, r *http.Request) {
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
	if scanID := r.URL.Query().Get("scanId"); scanID != "" {
		scope.ScanID = scanID
	}
	if category := r.URL.Query().Get("scopeCategory"); category != "" {
		scope.ScopeCategory = category
	}
	overview, err := s.reportSvc.Overview(r.Context(), scope)
	if err != nil {
		slog.Warn("server: 报告总览统计失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "overview reports failed")
		return
	}
	writeOK(w, r, http.StatusOK, overview)
}

// reportRow 是列表响应里的一行：service 的可见视图 + 发起人用户名。
//
// 用内嵌而不是给 service.ReportView 加字段：归属人怎么变成「一个名字」是 HTTP 展示层的
// 事（要查用户表），报告读侧不该为了画页面去知道用户表长什么样。
type reportRow struct {
	service.ReportView
	// CreatedByName 是发起人的用户名（查不到时留空，页面退回显示占位符）。
	CreatedByName string `json:"createdByName,omitempty"`
}

// withAuthorNames 把行里的 created_by（用户 ID）换成用户名（P9.13：列表要写
// 「发起人：zhangsan」，一串 8 字节 ID 对用户没有任何信息量）。
//
// 用户表整体读一次而不是逐行查：用户是个位数到几十位的表，而列表每页二十行，
// N+1 只会更慢。这里不构成「枚举用户」的入口——它只回答「这行报告是谁跑的」，
// 而这一行本来就已经通过归属判定了。取名字失败只降级成空串：报告本身比一个名字重要。
func (s *Server) withAuthorNames(ctx context.Context, items []service.ReportView) []reportRow {
	names := map[string]string{}
	if s.auth != nil && len(items) > 0 {
		if users, err := s.auth.ListUsers(ctx); err == nil {
			for _, u := range users {
				if u == nil {
					continue
				}
				name := strings.TrimSpace(u.DisplayName)
				if name == "" {
					name = u.Username
				}
				names[u.ID] = name
			}
		} else {
			slog.Warn("server: 发起人姓名解析失败，列表退回显示用户 ID", "error", err)
		}
	}
	out := make([]reportRow, 0, len(items))
	for _, item := range items {
		out = append(out, reportRow{ReportView: item, CreatedByName: names[item.CreatedBy]})
	}
	return out
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
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+rfc5987Encode(reportFilename(view, ref)))
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

// reportFilename 生成人可读的下载文件名（方案 17 §7.2）：体检-业务-命名空间arc-20261103-1712.md。
//
// 文件名要回答「这份报告是扫什么、什么时候扫的」，否则下载下来的一堆 .md 只能靠修改时间认。
func reportFilename(view service.ReportView, ref objectstore.Ref) string {
	ext := mediaExt(view, ref)
	return reportFilenameExt(view, ext)
}

// reportFilenameExt 是 reportFilename 的扩展名无关内核：按需导出 Markdown 时正文是当场
// 渲染出来的，没有 Ref 可问媒体类型，扩展名由端点自己定。
func reportFilenameExt(view service.ReportView, ext string) string {
	label := reportScopeLabel(view)
	if label == "" {
		// v8 之前的存量行与 CLI 产物没有 scope：回退旧式命名。把老报告改成一个对不上
		// 任何记录的名字，比它的名字不好看更糟。
		return sanitizeFilename("report-" + view.ID + "-" + view.Kind + ext)
	}
	// 本地时区而不是 UTC：文件名是给人核对「上午那次体检」用的。
	return sanitizeFilename("体检-" + label + "-" + view.CreatedAt.Local().Format("20060102-1504") + ext)
}

// reportScopeLabel 把 scope 两列翻成文件名片段；kind 为空（存量行/CLI 产物）时返回空串。
func reportScopeLabel(view service.ReportView) string {
	switch view.ScopeKind {
	case model.ScopeCluster:
		return "集群"
	case model.ScopeNamespace:
		return "业务-命名空间" + view.ScopeValue
	case model.ScopePod:
		return "业务-Pod-" + strings.ReplaceAll(view.ScopeValue, "/", "-")
	default:
		return ""
	}
}

// sanitizeFilename 放行 Unicode 字母与数字（中文名不再被打成一串下划线），仍剔除路径
// 分隔符、shell/Windows 非法字符、控制字符与首尾空白点号。这里防的是响应头注入与
// 「下载名里带 ../」两类问题，不是给文件名做美观处理。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	out := make([]rune, 0, len(name)+8)
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '?' || r == '*' || r == '"' || r == '<' || r == '>' || r == '|':
			out = append(out, '_')
		case r < 0x20 || r == 0x7f:
			out = append(out, '_')
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' || r == ' ':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	trimmed := strings.Trim(strings.TrimSpace(string(out)), ".")
	if trimmed == "" {
		return "report"
	}
	return trimmed
}

// rfc5987Encode 按 RFC 5987 的 attr-char 百分号编码。中文文件名必须真编码：Go 的服务端
// 会放行 obs-text（0x80-0xFF）字节，但反向代理与浏览器对裸 UTF-8 头部并没有一致口径，
// 靠"多半能过"来发下载名是拿用户的产品换运气。
func rfc5987Encode(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	b.Grow(len(s))
	for _, by := range []byte(s) {
		switch {
		case by >= 'a' && by <= 'z', by >= 'A' && by <= 'Z', by >= '0' && by <= '9',
			strings.IndexByte(attrChars, by) >= 0:
			b.WriteByte(by)
		default:
			fmt.Fprintf(&b, "%%%02X", by)
		}
	}
	return b.String()
}

// handleDeleteReport DELETE /api/v1/reports/{id}：删除一条报告的业务引用（P8.14）。
//
// 这里删的是「平台认为这份报告存在」这件事，不是正文：正文归 blobs gc 在宽限期后回收，
// 请求路径里任何时候都不允许出现后端对象的 Delete（共享 blob_key 的报告会被连带删掉）。
func (s *Server) handleDeleteReport(w http.ResponseWriter, r *http.Request) {
	if s.reportSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "reports is not configured")
		return
	}
	scope, user, err := s.reportScopeFor(r.Context())
	if err != nil {
		writeReportScopeError(w, r, err)
		return
	}
	view, err := s.reportSvc.Delete(r.Context(), r.PathValue("id"), scope)
	if err != nil {
		writeReportError(w, r, err)
		return
	}
	s.recordReportDelete(r, user, view)
	// 响应形状与批量端点一致（deleted 是 id 数组）：前端两条路径共用一套收尾逻辑。
	writeOK(w, r, http.StatusOK, map[string]any{"deleted": []string{view.ID}, "skipped": []string{}})
}

// cleanupReportsRequest 是批量清理的请求体。
type cleanupReportsRequest struct {
	IDs []string `json:"ids"`
}

// handleCleanupReports POST /api/v1/reports/cleanup：批量清理（P8.14）。
//
// 批量端点不构成第二条授权通道：service 侧逐 id 判归属、逐 id 独立事务，语义与逐条
// DELETE 完全一致，混进来的他人 id 只会变成一条 skipped，不会整批失败也不会顺手删掉。
func (s *Server) handleCleanupReports(w http.ResponseWriter, r *http.Request) {
	if s.reportSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "reports is not configured")
		return
	}
	var req cleanupReportsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	scope, user, err := s.reportScopeFor(r.Context())
	if err != nil {
		writeReportScopeError(w, r, err)
		return
	}
	deleted, skipped, err := s.reportSvc.Cleanup(r.Context(), req.IDs, scope)
	if errors.Is(err, service.ErrCleanupTooMany) {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
		return
	}
	if err != nil {
		slog.Warn("server: 报告批量清理失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "cleanup reports failed")
		return
	}
	ids := make([]string, 0, len(deleted))
	for _, view := range deleted {
		s.recordReportDelete(r, user, view)
		ids = append(ids, view.ID)
	}
	// deleted 返回 id 而不是整行：前端手里已经有行了，把视图再塞回去只会让响应体随批量
	// 规模线性变长，而它唯一要做的就是把删掉的行从表里移出去。
	writeOK(w, r, http.StatusOK, map[string]any{"deleted": ids, "skipped": skipped})
}

// recordReportDelete 为**每一条**删除落一条 report.delete 审计（方案 15 §13.1）。
//
// 与 report.download 只记跨 owner 相反：删除低频且不可逆，全量留痕才配得上「谁能说这份
// 体检结论不存在了」。跨 owner（管理员删别人的报告）在 ResultSummary 里显式标出来。
func (s *Server) recordReportDelete(r *http.Request, user *store.User, view service.ReportView) {
	if s.auditlog == nil || user == nil {
		return
	}
	summary := fmt.Sprintf(`{"kind":%q,"owner":%q,"size":%d,"scope_kind":%q,"scope_value":%q`,
		view.Kind, view.CreatedBy, view.Size, view.ScopeKind, view.ScopeValue)
	if view.CreatedBy != user.ID {
		summary += `,"cross_owner":true`
	}
	summary += "}"
	err := s.auditlog.Record(r.Context(), store.AuditEvent{
		Action:       store.ActionReportDelete,
		EventType:    store.AuditEventReport,
		RequestID:    RequestIDFrom(r.Context()),
		UserID:       user.ID,
		ClusterID:    view.ClusterID,
		Operation:    "delete",
		ResourceKind: "report",
		ResourceName: view.ID,
		Status:       "success",
		ResultSummary: summary,
	})
	if err != nil {
		slog.Warn("server: 报告删除审计写入失败", "report_id", view.ID, "user_id", user.ID, "error", err)
	}
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
