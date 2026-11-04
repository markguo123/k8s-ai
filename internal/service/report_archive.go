package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/report"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 报告 kind 取值（方案 15 §6.2，与 store.Report 的注释同一份口径）。
const (
	ReportKindLatestMD   = "latest-md"
	ReportKindLatestJSON = "latest-json"
)

// ReportArchiveWriter 把一次扫描的渲染产物归档进对象存储，并在同一事务里写下业务行与
// 对象引用（P8.5，方案 15 §6.6）。
//
// 两条不变量决定了这个类型为什么存在：
//
//  1. **先有对象，再有引用**。Put 成功之前不建 reports 行——反过来做会留下一个指向
//     不存在对象的行，下载时既取不到内容又已经计入列表，是比缺一份报告更难解释的状态。
//     对象写成功、事务回滚，留下的只是一个未被引用的对象，由 GC 的差集自然回收。
//  2. **report.directory 的本地文件不是事实源**。它只是兼容副本（三期 CLI 的行为保持
//     不变），事实源是 objects + reports。所以这里的失败绝不影响扫描结果本身。
type ReportArchiveWriter struct {
	st store.Store
	gw *BlobGateway
}

// NewReportArchiveWriter 创建归档写入器。
func NewReportArchiveWriter(st store.Store, gw *BlobGateway) *ReportArchiveWriter {
	return &ReportArchiveWriter{st: st, gw: gw}
}

// ReportArchiveBinder 是 ScanService 的可选能力（P8.5）：注入归档写入器后，每次扫描的
// 产物额外归档到对象存储。未注入时完全 no-op，三期 CLI 的行为一字不变——这也是它做成
// 可选接口而不是构造参数的原因：不该让没接平台的用户为归档失败承担启动错误。
type ReportArchiveBinder interface {
	SetReportArchive(w *ReportArchiveWriter)
}

// SetReportArchive 实现 ReportArchiveBinder。
func (s *scanService) SetReportArchive(w *ReportArchiveWriter) { s.archive = w }

// Archive 渲染并归档 markdown + json 两份产物，返回成功归档的份数。
//
// 两份产物各自独立成行：kind 不唯一，同一 scan 重跑产生新行、引用同一个对象
// （内容寻址让 ref_count 变 2），这是设计允许的历史记录而不是重复数据。
//
// 任一对象写入失败即返回错误且不写它的行；调用方（Run）只记 WARN，不改扫描返回值。
func (w *ReportArchiveWriter) Archive(ctx context.Context, result *model.ScanResult, attr model.ReportAttribution) (int, error) {
	if w == nil || w.gw == nil || w.st == nil {
		return 0, fmt.Errorf("archive report: writer not wired")
	}
	if result == nil {
		return 0, fmt.Errorf("archive report: nil result")
	}
	actor := strings.TrimSpace(attr.Actor)
	if actor == "" {
		actor = objectstore.SystemActor
	}
	scope := archiveScope(attr.ClusterID, attr.ScanID)
	scopeKind, scopeValue := archiveScopeLabel(attr.ScopeKind, attr.ScopeValue)

	targets := []struct {
		kind      string
		ext       string
		mediaType string
		render    func() ([]byte, error)
	}{
		// .md 必须显式给 media type：objectstore 的扩展名推断表里没有 .md，
		// 交给它只会得到 application/octet-stream，浏览器就会当成二进制下载。
		{ReportKindLatestMD, ".md", "text/markdown; charset=utf-8", func() ([]byte, error) {
			return report.MarkdownRenderer{}.Render(result)
		}},
		{ReportKindLatestJSON, ".json", "", func() ([]byte, error) {
			return report.JSONRenderer{}.Render(result)
		}},
	}

	var done int
	var errs []string
	for _, t := range targets {
		payload, err := t.render()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: render: %v", t.kind, err))
			continue
		}
		ref, err := w.gw.Put(ctx, objectstore.Spec{
			Category:  objectstore.CategoryReports,
			Scope:     scope,
			Ext:       t.ext,
			MediaType: t.mediaType,
			CreatedBy: actor,
		}, strings.NewReader(string(payload)))
		if err != nil {
			// 刻意不写行：引用一个不存在的对象比少一份报告更难收拾。
			errs = append(errs, fmt.Sprintf("%s: put: %v", t.kind, err))
			continue
		}
		row := &store.Report{
			ID:        newReportID(),
			ClusterID: attr.ClusterID,
			ScanID:    attr.ScanID,
			Kind:      t.kind,
			BlobKey:   ref.Key,
			MediaType: ref.MediaType,
			Size:      ref.Size,
			SHA256:    ref.SHA256,
			Findings:  int64(len(result.Findings)),
			CreatedBy: actor,
			Available: true,
			CreatedAt: time.Now().UTC(),
			// 巡检范围与业务行同事务落库（P8.13）：它只回答「扫的是什么」，不参与 key 与鉴权。
			// 非法值一律降级成空串而不是拒掉整次归档——范围是展示信息，为一个展示字段丢一份
			// 已经写进桶的报告本末倒置；降级会留 WARN，不会变成一个没人知道的分支。
			ScopeKind:  scopeKind,
			ScopeValue: scopeValue,
		}
		// 业务行与引用必须同事务：行提交了而引用没加，对象会被 GC 当成孤儿删掉；
		// 引用加了而行回滚，ref_count 永远减不掉。回调内只用 Tx（SQLite 单连接下
		// 事务里再要连接会挂死），所以这里没有任何前置读。
		if err := w.st.WithTx(ctx, func(tx *store.Tx) error {
			if err := tx.InsertReport(ctx, row); err != nil {
				return err
			}
			return tx.Acquire(ctx, ref, w.gw.Driver(), actor)
		}); err != nil {
			errs = append(errs, fmt.Sprintf("%s: commit: %v", t.kind, err))
			continue
		}
		done++
	}
	if len(errs) > 0 {
		return done, fmt.Errorf("archive report: %s", strings.Join(errs, "; "))
	}
	return done, nil
}

// archiveScope 组装 key 的命名空间段：只用内部生成的 ID（clusterID/scanID），都空时
// 退化成一个稳定占位段。刻意不做「净化用户输入」——能进这里的都是系统生成的 ID，
// 真出现非法字符就该由 BuildKey 报 ErrKeyInvalid 并经 WARN 冒出来，静默改写只会把
// 一个装配 bug 伪装成一次偶发的归档失败。
func archiveScope(clusterID, scanID string) []string {
	scope := make([]string, 0, 2)
	if id := strings.TrimSpace(clusterID); id != "" {
		scope = append(scope, id)
	}
	if id := strings.TrimSpace(scanID); id != "" {
		scope = append(scope, id)
	}
	if len(scope) == 0 {
		return []string{"local"}
	}
	return scope
}

// archiveScanPhase 是归档阶段的进度名（Web 端阶段列表按它显示）。
const archiveScanPhase = "archive"

// archiveScopeLabel 校验巡检范围两列的取值（P8.13，方案 17 §7.1）。
//
// 值域外一律清空并记 WARN：范围只用于展示与过滤，为一个展示字段丢掉一份已经写进桶的报告
// 本末倒置；但也不能把调用方传来的野值原样落库——那会让页面的「巡检范围」列长出无法枚举的
// 取值。清空 + WARN 是唯一一种既不断链、又留得下线索的收尾。
func archiveScopeLabel(kind, value string) (string, string) {
	kind = strings.TrimSpace(kind)
	value = strings.TrimSpace(value)
	if model.ValidReportScope(kind, value) {
		return kind, value
	}
	slog.Warn("archive report: 忽略非法的巡检范围取值", "scope_kind", kind, "scope_value", value)
	return "", ""
}

// archiveReportsIfNeeded 在 Run 的末尾归档报告：未注入写入器时直接返回，注入了则任何
// 失败都只记 WARN 并标该阶段降级——扫描结果本身已经算完，绝不能因为桶不可达让整次巡检
// 变成失败（方案 15 §11.3「对象存储故障不影响基础业务」）。
func (s *scanService) archiveReportsIfNeeded(
	ctx context.Context,
	result *model.ScanResult,
	opts model.ScanOptions,
	phaseStart func(name, title string) time.Time,
	phaseDone func(name, detail string, ok bool, started time.Time),
) {
	if s.archive == nil {
		return
	}
	started := phaseStart(archiveScanPhase, "归档报告到对象存储")
	count, err := s.archive.Archive(ctx, result, opts.Attribution)
	if err != nil {
		slog.Warn("scan: 报告归档到对象存储失败",
			"error", err,
			"cluster_id", opts.Attribution.ClusterID,
			"scan_id", opts.Attribution.ScanID,
			"archived", count)
		phaseDone(archiveScanPhase, err.Error(), false, started)
		return
	}
	phaseDone(archiveScanPhase, fmt.Sprintf("objects=%d", count), true, started)
}

// newReportID 生成报告行主键（8 字节十六进制，与 server 的任务 ID 同一形态）。
func newReportID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("report-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
