package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/service"
)

// codeUnsupportedMediaType 用于「这份报告没有结构化形态」（415）：老报告只有 Markdown
// 正文时，前端要的是「去看原文」这个动作，而不是一个渲染不出来的空详情页。
const codeUnsupportedMediaType = "unsupported_media_type"

// codeTooLarge 用于「报告正文超过服务端读取上限」（413）。
const codeTooLarge = "payload_too_large"

// structuredReportResponse 是 GET /api/v1/reports/{id}/report 的响应体。
//
// 刻意分开 report 与 inspection 两块：前者是「你在报告中心点开的这一行」（文件名、
// 归属、下载入口都靠它），后者是统一体检报告本体。把两者混成一个对象，前端就得自己
// 猜哪些字段来自数据库、哪些来自 JSON。
//
// 这里绝不外泄 blob_key / object 元数据：报告详情是页面入口，能看到的应该是报告内容，
// 不是平台的存储布局。
type structuredReportResponse struct {
	Report        service.ReportView `json:"report"`
	SchemaVersion int                `json:"schemaVersion"`
	Legacy        bool               `json:"legacy"`
	Inspection    *model.ScanResult  `json:"inspection"`
}

// handleGetReportStructured GET /api/v1/reports/{id}/report：读结构化体检报告（P9）。
//
// 它与既有的 /content 是两种用途，不能互相顶替：/content 是「下载原文」，强制附件下载、
// 禁 MIME 嗅探，浏览器里永远拿不到内容；/report 是「在页面里渲染报告详情页」，走普通
// JSON 响应。授权判定复用同一套 reportScopeFor，两个端点的可见集合因此天然一致——
// 详情页不会成为第二条越权读取通道。
func (s *Server) handleGetReportStructured(w http.ResponseWriter, r *http.Request) {
	if s.reportSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "reports is not configured")
		return
	}
	scope, user, err := s.reportScopeFor(r.Context())
	if err != nil {
		writeReportScopeError(w, r, err)
		return
	}
	id := r.PathValue("id")

	sr, err := s.reportSvc.OpenStructuredReport(r.Context(), id, scope)
	if err != nil {
		writeStructuredReportError(w, r, err)
		return
	}
	// 详情页与下载同样计一次跨 owner 读取：管理员翻别人的体检结论必须留痕，
	// 「只是看了看」不构成例外。
	s.recordReportDownload(r, user, sr.Report)
	writeOK(w, r, http.StatusOK, structuredReportResponse{
		Report:        sr.Report,
		SchemaVersion: sr.SchemaVersion,
		Legacy:        sr.Legacy,
		Inspection:    sr.Result,
	})
}

// writeStructuredReportError 在报告面通用错误映射之上补两类详情专属状态码：
// 415（没有结构化形态）与 413（超大）。其余一律走 writeReportError，保证 503/410/409/404
// 与下载端点完全同形——同一个坏掉的对象在两个端点上必须给出同一个运维动作。
func writeStructuredReportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrReportNotStructured):
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMediaType,
			"该报告没有结构化版本（早期只归档了 Markdown 正文），请使用原文下载查看")
	case errors.Is(err, service.ErrReportTooLarge):
		writeError(w, r, http.StatusRequestEntityTooLarge, codeTooLarge, err.Error())
	default:
		slog.Debug("server: 结构化报告读取失败", "error", err)
		writeReportError(w, r, err)
	}
}

// handleExportReportMarkdown GET /api/v1/reports/{id}/export：按需导出一份 Markdown。
//
// 这个端点存在的理由是「一次体检只留一份产物」：发起时选了 JSON，归档里就只有一条 JSON
// 行，用户想要 Markdown 时由同一套渲染器当场渲染出来（service.ExportMarkdown），而不是
// 事先存两份让报告列表长出两条几乎相同的行。事实源只有一个，两种格式不会互相过期。
//
// 响应头与 /content 的流式下载保持同一套安全形状：强制附件、禁 MIME 嗅探、显式长度。
// 正文来自集群数据，绝不能让浏览器把它当同源页面渲染。
func (s *Server) handleExportReportMarkdown(w http.ResponseWriter, r *http.Request) {
	if s.reportSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "reports is not configured")
		return
	}
	scope, user, err := s.reportScopeFor(r.Context())
	if err != nil {
		writeReportScopeError(w, r, err)
		return
	}
	view, body, err := s.reportSvc.ExportMarkdown(r.Context(), r.PathValue("id"), scope)
	if err != nil {
		writeReportError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+rfc5987Encode(reportFilenameExt(view, ".md")))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	s.recordReportDownload(r, user, view)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		slog.Warn("server: 报告 Markdown 导出中断", "report_id", view.ID, "error", err)
	}
}
