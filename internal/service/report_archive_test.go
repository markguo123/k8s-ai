package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 编译期确认归档器真的挂在 ScanService 上：这条断言守的是装配面——
// cli/server.go 靠类型断言注入，接口一旦漂了那里会静默不注入，归档整体失效却无人报错。
var _ ReportArchiveBinder = (*scanService)(nil)

func newArchiveStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite("file:svc-archive-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// archiveTestResult 用固定时间构造结果：渲染必须逐字节稳定，
// 否则「同内容重跑应命中同一对象」这条内容寻址断言会变成随机失败。
func archiveTestResult() *model.ScanResult {
	return &model.ScanResult{
		Meta: model.ScanMeta{
			ToolVersion: "v4.0.0", Cluster: "prod", ServerVersion: "v1.28.13",
			ScanStartedAt: "2026-11-02T10:00:00Z", ScanEndedAt: "2026-11-02T10:00:12Z", Duration: "12s",
		},
		Findings: []model.Finding{{Title: "web pod is crashlooping"}},
	}
}

func archiveDBGateway(t *testing.T, st store.Store) *BlobGateway {
	t.Helper()
	backend, err := st.ObjectContent("k8sai", 1<<20)
	if err != nil {
		t.Fatalf("db content backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return NewBlobGateway(backend, st.Objects())
}

// TestReportArchiveWritesBothKindsWithReferences 覆盖 P8.5 的正常写入面：
// 一次扫描产出 md + json 两行、各自挂一个对象引用，md 带正确 media type。
func TestReportArchiveWritesBothKindsWithReferences(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	w := NewReportArchiveWriter(st, archiveDBGateway(t, st))

	n, err := w.Archive(ctx, archiveTestResult(), model.ReportAttribution{Actor: "alice", ClusterID: "c1", ScanID: "s1"})
	if err != nil || n != 2 {
		t.Fatalf("归档结果 = %d (%v)，期望 2 份且无错误", n, err)
	}

	rows, total, err := st.Reports().List(ctx, store.ReportQuery{Admin: true})
	if err != nil {
		t.Fatalf("list reports: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("reports 行数 = %d/%d，期望 2", len(rows), total)
	}
	kinds := map[string]bool{}
	for _, row := range rows {
		kinds[row.Kind] = true
		if row.CreatedBy != "alice" || row.ClusterID != "c1" || row.ScanID != "s1" {
			t.Fatalf("归属写错：%+v", row)
		}
		if !row.Available || row.Findings != 1 || row.BlobKey == "" || row.SHA256 == "" {
			t.Fatalf("报告行字段不完整：%+v", row)
		}

		meta, err := st.Objects().Get(ctx, row.BlobKey)
		if err != nil {
			t.Fatalf("objects 行缺失 %s: %v", row.BlobKey, err)
		}
		// 引用计数为 1 且状态 ready：这是「业务行与引用同事务提交」的直接证据。
		if meta.RefCount != 1 || meta.State != store.ObjectStateReady {
			t.Fatalf("对象引用状态 = ref_count=%d state=%s，期望 1/ready", meta.RefCount, meta.State)
		}
		if meta.Driver != objectstore.DriverDB || meta.Category != objectstore.CategoryReports {
			t.Fatalf("对象登记不符：driver=%s category=%s", meta.Driver, meta.Category)
		}
		if meta.CreatedBy != "alice" {
			t.Fatalf("objects.created_by = %q，期望归属写用户 ID", meta.CreatedBy)
		}
		if meta.Size != row.Size || meta.SHA256 != row.SHA256 {
			t.Fatalf("报告行与对象元数据不一致：row(size=%d sha=%s) meta(size=%d sha=%s)",
				row.Size, row.SHA256, meta.Size, meta.SHA256)
		}
	}
	if !kinds[ReportKindLatestMD] || !kinds[ReportKindLatestJSON] {
		t.Fatalf("产物 kind = %v，期望 latest-md 与 latest-json 各一份", kinds)
	}
	// markdown 必须显式带 media type：objectstore 的扩展名表里没有 .md，
	// 漏了它就会以 application/octet-stream 落库，浏览器当成二进制下载。
	for _, row := range rows {
		if row.Kind == ReportKindLatestMD && row.MediaType != "text/markdown; charset=utf-8" {
			t.Fatalf("markdown media type = %q，期望 text/markdown; charset=utf-8", row.MediaType)
		}
	}
}

// TestReportArchiveReusesSameObjectForSameContent 证明内容寻址去重：同内容重跑
// 新增一行历史记录，但只加引用，不再复制一份正文。
func TestReportArchiveReusesSameObjectForSameContent(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	w := NewReportArchiveWriter(st, archiveDBGateway(t, st))
	attr := model.ReportAttribution{Actor: "alice", ClusterID: "c1", ScanID: "s1"}

	if _, err := w.Archive(ctx, archiveTestResult(), attr); err != nil {
		t.Fatalf("first archive: %v", err)
	}
	first, _, err := st.Reports().List(ctx, store.ReportQuery{Admin: true})
	if err != nil || len(first) != 2 {
		t.Fatalf("首轮报告 = %d 条 (%v)", len(first), err)
	}
	if _, err := w.Archive(ctx, archiveTestResult(), attr); err != nil {
		t.Fatalf("second archive: %v", err)
	}

	rows, total, err := st.Reports().List(ctx, store.ReportQuery{Admin: true})
	if err != nil || total != 4 {
		t.Fatalf("重跑后报告 = %d 条 (%v)，期望 4 条历史记录", total, err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		meta, err := st.Objects().Get(ctx, row.BlobKey)
		if err != nil {
			t.Fatalf("objects 行缺失：%v", err)
		}
		if meta.RefCount != 2 {
			t.Fatalf("同内容重跑后 ref_count = %d，期望 2", meta.RefCount)
		}
		seen[row.BlobKey] = true
	}
	if len(seen) != 2 {
		t.Fatalf("重跑产生了重复正文：%d 行报告对应 %d 个对象，期望 2", len(rows), len(seen))
	}
}

// TestReportArchiveWritesNoRowsWhenPutFails 是 P8.5 最要紧的一条不变量：
// 对象没写成功就绝不能留下业务行。悬空引用会让报告列表里出现一条永远下不下来的报告，
// 比彻底少一份报告更难解释。
func TestReportArchiveWritesNoRowsWhenPutFails(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	broken := NewBlobGateway(&fakeBlobBackend{
		driver:    objectstore.DriverS3,
		ensureErr: fmt.Errorf("%w: bucket unreachable", objectstore.ErrUnavailable),
	}, st.Objects())
	w := NewReportArchiveWriter(st, broken)

	n, err := w.Archive(ctx, archiveTestResult(), model.ReportAttribution{Actor: "alice", ClusterID: "c1", ScanID: "s1"})
	if err == nil {
		t.Fatal("对象写入失败却返回无错误")
	}
	if n != 0 {
		t.Fatalf("归档成功份数 = %d，期望 0", n)
	}
	rows, total, err := st.Reports().List(ctx, store.ReportQuery{Admin: true})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("对象失败后仍留下 %d 条报告行：%v", total, err)
	}
	stats, err := st.Objects().ObjectStats(ctx)
	if err != nil {
		t.Fatalf("object stats: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("对象失败后仍登记了元数据行：%+v", stats)
	}
}

// TestReportArchiveAttributesSystemTasks：CLI 与定时巡检不填 Actor，归档必须落到系统归属，
// 否则这些报告会被当成某个不存在用户的私有报告，谁都读不到。
func TestReportArchiveAttributesSystemTasks(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	w := NewReportArchiveWriter(st, archiveDBGateway(t, st))

	// cluster/scan 都为空（CLI 本地扫描）：key 的 scope 段退化到稳定占位段而不是拼出空段。
	if _, err := w.Archive(ctx, archiveTestResult(), model.ReportAttribution{}); err != nil {
		t.Fatalf("archive system task: %v", err)
	}
	rows, total, err := st.Reports().List(ctx, store.ReportQuery{Admin: true})
	if err != nil || total != 2 {
		t.Fatalf("系统任务报告 = %d 条 (%v)", total, err)
	}
	for _, row := range rows {
		if row.CreatedBy != objectstore.SystemActor {
			t.Fatalf("created_by = %q，期望 %q", row.CreatedBy, objectstore.SystemActor)
		}
	}

	// 系统归属 + 未绑定集群：非 admin 一律读不到（没有可比对的授权对象时默认拒绝）。
	scope := ReportScope{ViewerID: "alice", SystemClusterIDs: []string{"c1"}}
	for _, row := range rows {
		if reportVisible(row, scope) {
			t.Fatalf("cluster_id 为空的系统报告对普通用户可见：%+v", row)
		}
	}
}

// TestReportArchiveRejectsUnwiredWriter：归档器缺件时报错而不是 panic——扫描流水线
// 只在 WARN 分支消费这个错误，返回错误就足够，不能把一次归档配置问题升级成进程崩溃。
func TestReportArchiveRejectsUnwiredWriter(t *testing.T) {
	ctx := context.Background()
	if _, err := (&ReportArchiveWriter{}).Archive(ctx, archiveTestResult(), model.ReportAttribution{}); err == nil {
		t.Fatal("未装配的归档器应返回错误")
	}
	st := newArchiveStore(t)
	if _, err := NewReportArchiveWriter(st, archiveDBGateway(t, st)).Archive(ctx, nil, model.ReportAttribution{}); err == nil {
		t.Fatal("result 为空应返回错误")
	}
}
