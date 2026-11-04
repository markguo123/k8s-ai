package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 报告读取的两类不可用原因（P8.5，方案 15 §12.4）。它们必须由 HTTP 层翻成不同状态码，
// 因为运维要做的动作完全不同：410 是「这份报告的内容确实没了，去看备份」，409 是
// 「换过 driver 没迁移，跑一次 blobs migrate 就恢复」。把两者混成一个 500 会把人引去
// 查错方向。
var (
	// ErrReportUnavailable 报告业务行还在、内容已不可恢复（410 Gone）。
	ErrReportUnavailable = errors.New("service: report content is no longer available")
	// ErrObjectDriverMismatch 内容还在，但躺在另一个后端里（409 Conflict）。
	ErrObjectDriverMismatch = errors.New("service: report content lives on another object storage driver")
)

// ReportView 是报告的对外视图。
//
// 它刻意不复用 store.Report：后者的 BlobKey 虽然带 json:"-"，但那是"靠字段标签兜住"的
// 安全，一旦有人把它塞进别的响应包装（日志、SSE、审计摘要）就漏了 key——key 本身携带
// 集群 ID 与扫描 ID，属于内部结构。视图类型让"对外可见字段"这件事在类型上就是封闭的。
type ReportView struct {
	ID        string    `json:"id"`
	ClusterID string    `json:"clusterId,omitempty"`
	ScanID    string    `json:"scanId,omitempty"`
	Kind      string    `json:"kind"`
	MediaType string    `json:"mediaType,omitempty"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Findings  int64     `json:"findings"`
	CreatedBy string    `json:"createdBy"`
	Available bool      `json:"available"`
	CreatedAt time.Time `json:"createdAt"`
	// ScopeKind / ScopeValue 是巡检范围（P8.13）：空 kind 表示 v8 之前的存量报告或 CLI 产物，
	// 页面显示「未记录」。它们不参与鉴权，读得到就读得到，与 scope 无关。
	ScopeKind  string `json:"scopeKind,omitempty"`
	ScopeValue string `json:"scopeValue,omitempty"`
}

// ReportScope 是本次请求的可见性范围（方案 15 §12.2）。由 server 层从认证上下文与
// AuthzGrant 算出，service 层不自己猜调用者是谁。
type ReportScope struct {
	// ViewerID 是当前用户 ID；Admin 为 false 时必填。
	ViewerID string
	// Admin 表示角色含全域权限（*），可跨用户读取（跨 owner 由 server 层落审计）。
	Admin bool
	// AllSystemClusters / SystemClusterIDs 决定能看到哪些系统巡检报告。
	AllSystemClusters  bool
	SystemClusterIDs   []string
	ClusterID          string
	Kind               string
	// ScanID 按扫描任务反查产物（P8.13）：页面发起巡检后靠它找回自己的报告。
	// 它是过滤条件而不是授权条件——归属判定仍然只看 owner 与集群授权，扫描 ID 猜中了
	// 也读不到别人的报告。
	ScanID string
}

// ReportPlatform 是报告的读取面（P8.5）：归属判定 + 对象内容取回。
//
// 写面在 ReportArchiveWriter（扫描流水线里），这里不开写入口——报告行只能由"对象已经
// 写成功"这条路径产生，否则就等于允许指向不存在对象的业务行。
type ReportPlatform struct {
	st store.Store
	gw *BlobGateway
}

// NewReportPlatform 装配报告读取面。gw 为 nil 时所有取内容的方法返回错误（列表仍可读，
// 因为列表不加载正文）。
func NewReportPlatform(st store.Store, gw *BlobGateway) *ReportPlatform {
	return &ReportPlatform{st: st, gw: gw}
}

// List 分页列出可见报告。可见性下推到 SQL：total 与当页共用同一条归属谓词，
// 别人的报告既不会出现在 items 里，也不会被算进 total。
func (p *ReportPlatform) List(ctx context.Context, scope ReportScope, limit, offset int) ([]ReportView, int64, error) {
	if p == nil || p.st == nil {
		return nil, 0, fmt.Errorf("list reports: report platform not wired")
	}
	rows, total, err := p.st.Reports().List(ctx, store.ReportQuery{
		ViewerID:          scope.ViewerID,
		Admin:             scope.Admin,
		AllSystemClusters: scope.AllSystemClusters,
		SystemClusterIDs:  scope.SystemClusterIDs,
		ClusterID:         scope.ClusterID,
		Kind:              scope.Kind,
		ScanID:            scope.ScanID,
		Limit:             limit,
		Offset:            offset,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]ReportView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toReportView(r))
	}
	return out, total, nil
}

// OpenContent 打开报告正文，调用方必须 Close 返回的流。
//
// 顺序是有讲究的：先归属、再 available、再元数据行、最后内容。越权与 id 不存在返回同一
// 个 store.ErrNotFound（server 层同形成 404），否则报告 ID 就成了一个可以探测存在性的
// 枚举面——8 字节随机 ID 不是权限机制，别把"猜不中"当成防线。
func (p *ReportPlatform) OpenContent(ctx context.Context, id string, scope ReportScope) (ReportView, io.ReadCloser, objectstore.Ref, error) {
	row, _, err := p.resolve(ctx, id, scope)
	if err != nil {
		return ReportView{}, nil, objectstore.Ref{}, err
	}
	if p.gw == nil {
		return ReportView{}, nil, objectstore.Ref{}, fmt.Errorf("%w: object storage is not wired", ErrReportUnavailable)
	}
	rc, ref, err := p.gw.Get(ctx, row.BlobKey)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			// 后端明确说"没有"，才允许把行标成 missing；不可达绝不走到这里
			// （ErrUnavailable 会原样上抛成 503），否则一次网络抖动就能把好数据判成永久丢失。
			p.markMissing(ctx, row.BlobKey)
			return ReportView{}, nil, objectstore.Ref{}, fmt.Errorf("%w: %v", ErrReportUnavailable, err)
		}
		return ReportView{}, nil, objectstore.Ref{}, err
	}
	view := toReportView(row)
	if ref.MediaType != "" {
		view.MediaType = ref.MediaType
	}
	if ref.Size > 0 {
		view.Size = ref.Size
	}
	return view, rc, ref, nil
}

// Presign 生成限时下载 URL（仅 s3）。
//
// presign 在 S3 上不校验对象存在，所以这里必须把归属与元数据在库里查干净再签：
// 否则一个越权的 id 也能拿到一个签名 URL，签名本身变成了一次授权。
func (p *ReportPlatform) Presign(ctx context.Context, id string, scope ReportScope, ttl time.Duration) (ReportView, string, error) {
	row, meta, err := p.resolve(ctx, id, scope)
	if err != nil {
		return ReportView{}, "", err
	}
	if p.gw == nil {
		return ReportView{}, "", fmt.Errorf("%w: object storage is not wired", ErrReportUnavailable)
	}
	url, err := p.gw.Presign(ctx, row.BlobKey, ttl)
	if err != nil {
		return ReportView{}, "", err
	}
	view := toReportView(row)
	if meta.MediaType != "" {
		view.MediaType = meta.MediaType
	}
	return view, url, nil
}

// resolve 走完「存在 → 归属 → available → 元数据行 → driver 一致」这条判定链，
// 流式下载与预签名共用，避免两条路径各写一遍鉴权然后只有一条改对。
func (p *ReportPlatform) resolve(ctx context.Context, id string, scope ReportScope) (*store.Report, *store.ObjectMeta, error) {
	if p == nil || p.st == nil {
		return nil, nil, fmt.Errorf("get report: report platform not wired")
	}
	row, err := p.st.Reports().Get(ctx, id)
	if err != nil {
		return nil, nil, err // store.ErrNotFound 由 server 层翻成 404
	}
	if !reportVisible(row, scope) {
		// 同形 404：这里是整个 P8.5 最关键的一行——不能回 403，403 会把「存在但不是你的」
		// 白送给任何拿着 id 试探的人。
		return nil, nil, store.ErrNotFound
	}
	if !row.Available {
		return nil, nil, fmt.Errorf("%w: report %s was marked unavailable", ErrReportUnavailable, row.ID)
	}
	meta, err := p.st.Objects().Get(ctx, row.BlobKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 没有元数据行说明这条引用从未成功提交（或已被 GC 清账）。此刻把行标 missing
			// 没有意义——没有行可标——只报 410。
			return nil, nil, fmt.Errorf("%w: object metadata row for %s is gone", ErrReportUnavailable, row.BlobKey)
		}
		return nil, nil, err
	}
	if p.gw != nil && meta.Driver != p.gw.Driver() {
		// 内容只是躺在别的后端里，不是丢了：这里绝不 MarkMissing，也不动 reports.available，
		// 否则一次没跑 migrate 的配置切换就把历史报告判成永久丢失（方案 15 §10.3）。
		return nil, nil, fmt.Errorf("%w: object is on driver %q, current driver is %q; run k8s-ai blobs migrate --to %q",
			ErrObjectDriverMismatch, meta.Driver, p.gw.Driver(), p.gw.Driver())
	}
	return row, meta, nil
}

// CleanupSkip 是批量清理里一个没删掉的 id 及其原因。
type CleanupSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// maxCleanupBatch 是批量清理的单次上限（方案 15 v1.12 §0）。
//
// 上限不是为了性能——逐 id 独立事务本身就不会打满连接——而是把「一次点击」保持在一个可审计、
// 可解释的规模里：一次清两万条不像人的动作，更像脚本失控。
const maxCleanupBatch = 200

// 清理结果里 CleanupSkip.Reason 的取值。
const (
	CleanupSkipNotFound = "not_found"
	CleanupSkipInternal = "internal"
)

// Delete 删除一条报告的业务引用（P8.14，方案 15 §13.1）。
//
// 与读面刻意不同的两点：
//
//  1. **不复用 resolve()**。读面的 available→410 与 driver→409 闸门是给「下载正文」把门的，
//     而清理的主要对象恰恰是这些内容已不可恢复的行——照抄判定链等于把要清理的东西全挡在门外，
//     功能永远做不完。这里只复用 reportVisible 判归属。
//  2. **删行与 Release 同事务**，且绝不碰后端对象：blob_key 内容寻址，两份报告可能共享同一个
//     key，删对象会连别人的报告一起删掉。物理回收只归 blobs gc 在宽限期之后执行。
//
// objects 元数据行已被 GC 清账（或从未提交）时只删业务行并记 WARN：这一类行恰恰是清单里最该
// 清掉的，为了「引用没减成功」把它永久留在列表里，清理功能就对它永久无效。
func (p *ReportPlatform) Delete(ctx context.Context, id string, scope ReportScope) (ReportView, error) {
	if p == nil || p.st == nil {
		return ReportView{}, fmt.Errorf("delete report: report platform not wired")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ReportView{}, store.ErrNotFound
	}
	row, err := p.st.Reports().Get(ctx, id)
	if err != nil {
		return ReportView{}, err
	}
	if !reportVisible(row, scope) {
		// 与读面同形：越权与不存在都是 store.ErrNotFound → 404，不回 403。
		return ReportView{}, store.ErrNotFound
	}

	// 引用能不能减，必须在事务开始前就知道（WithTx 回调里不能再调非事务入口）。
	skipRelease := false
	meta, err := p.st.Objects().Get(ctx, row.BlobKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		skipRelease = true
	case err != nil:
		return ReportView{}, err
	case meta.RefCount <= 0:
		// 计数已归零：GC 已认领它，再 Release 就是下溢报错。业务行照删。
		skipRelease = true
	}
	if skipRelease {
		slog.Warn("report: 清理时对象引用已不存在，仅删除业务行",
			"report_id", row.ID, "blob_key", row.BlobKey)
	}

	err = p.st.WithTx(ctx, func(tx *store.Tx) error {
		changed, err := tx.DeleteReport(ctx, row.ID, row.BlobKey)
		if err != nil {
			return err
		}
		if !changed {
			return store.ErrNotFound
		}
		if skipRelease {
			return nil
		}
		return tx.Release(ctx, row.BlobKey)
	})
	if err != nil {
		return ReportView{}, err
	}
	return toReportView(row), nil
}

// ErrCleanupTooMany 批量清理的 id 数超过单次上限（server 层翻成 400）。
//
// 它是 sentinel 而不是靠错误文案匹配：HTTP 层要区分「请求不合法」与「库里出了岔子」，
// 前者该 400 让调用方改请求，后者该 500 并留下日志。
var ErrCleanupTooMany = errors.New("service: cleanup too many report ids")

// Cleanup 逐 id 删除（P8.14）。批量只是省掉前端的 N 次往返，不放宽任何一条判定：每个 id
// 单独判归属、单独成事务，所以「一批里混进别人的 id」既不会整批失败，也不会顺手删掉。
//
// deleted 按请求顺序返回被删报告的视图（server 层要据此逐条落审计），skipped 带原因；
// err 只在请求本身不合法（超过上限）时非空。
func (p *ReportPlatform) Cleanup(ctx context.Context, ids []string, scope ReportScope) ([]ReportView, []CleanupSkip, error) {
	targets, err := cleanupTargets(ids)
	if err != nil {
		return nil, nil, err
	}
	deleted := make([]ReportView, 0, len(targets))
	var skipped []CleanupSkip
	for _, id := range targets {
		view, err := p.Delete(ctx, id, scope)
		switch {
		case err == nil:
			deleted = append(deleted, view)
		case errors.Is(err, store.ErrNotFound):
			skipped = append(skipped, CleanupSkip{ID: id, Reason: CleanupSkipNotFound})
		default:
			slog.Warn("report: 清理单条报告失败", "report_id", id, "error", err)
			skipped = append(skipped, CleanupSkip{ID: id, Reason: CleanupSkipInternal})
		}
	}
	return deleted, skipped, nil
}

// cleanupTargets 去空白、去重并保序，同时把请求规模收在上限内。
func cleanupTargets(ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) > maxCleanupBatch {
		return nil, fmt.Errorf("%w: %d ids, max %d", ErrCleanupTooMany, len(out), maxCleanupBatch)
	}
	return out, nil
}

// markMissing 只在后端明确回答 ErrNotFound 时调用；失败本身不该盖掉 410 的响应。
func (p *ReportPlatform) markMissing(ctx context.Context, key string) {
	if err := p.st.Objects().MarkMissing(ctx, []string{key}); err != nil {
		// 状态位没落成不重要：这一条请求已经确定要回 410，下一位读者会再试一次标记。
		// 真正常态化的失败会在这里留下线索，而不是变成一条看不见的分支。
		slog.Warn("report: 标记对象 missing 失败", "blob_key", key, "error", err)
	}
}

// reportVisible 是单条报告的归属判定，与 store.ReportQuery.where() 是同一套规则的
// 内存版——两处必须同步改，否则「列表里看得见、点进去 404」会比看不见更难解释。
func reportVisible(r *store.Report, scope ReportScope) bool {
	if scope.Admin {
		return true
	}
	if scope.ViewerID != "" && r.CreatedBy == scope.ViewerID {
		return true
	}
	// 系统巡检报告按集群授权放行；cluster_id 为空的系统报告只 admin 可读（无授权对象可比对）。
	if r.CreatedBy != objectstore.SystemActor || r.ClusterID == "" {
		return false
	}
	if scope.AllSystemClusters {
		return true
	}
	for _, id := range scope.SystemClusterIDs {
		if id == r.ClusterID {
			return true
		}
	}
	return false
}

func toReportView(r *store.Report) ReportView {
	return ReportView{
		ID:        r.ID,
		ClusterID: r.ClusterID,
		ScanID:    r.ScanID,
		Kind:      r.Kind,
		MediaType: r.MediaType,
		Size:      r.Size,
		SHA256:    r.SHA256,
		Findings:  r.Findings,
		CreatedBy: r.CreatedBy,
		Available: r.Available,
		CreatedAt: r.CreatedAt,
		ScopeKind:  r.ScopeKind,
		ScopeValue: r.ScopeValue,
	}
}
