package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// insertTestReport 经唯一合法写路径（Tx.InsertReport）插一行报告。
// 绕过 Tx 直接写表会让测试测不到真实约束，也顺手放弃了"引用必须同事务"这条不变量。
func insertTestReport(t *testing.T, s Store, id, clusterID, kind, createdBy string, createdAt time.Time) *Report {
	t.Helper()
	ref := objTestRef(t, objectstore.CategoryReports, []string{firstNonEmpty(clusterID, "local"), id}, ".md", "seed-"+id)
	row := &Report{
		ID: id, ClusterID: clusterID, ScanID: "scan-" + id, Kind: kind,
		BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		Findings: 2, CreatedBy: createdBy, Available: true, CreatedAt: createdAt,
	}
	err := s.WithTx(context.Background(), func(tx *Tx) error { return tx.InsertReport(context.Background(), row) })
	if err != nil {
		t.Fatalf("insert report %s: %v", id, err)
	}
	return row
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "local"
}

// TestReportListHidesOtherUsersAndTheirTotals 是 P8.5 最核心的一条：可见性必须下推到 SQL，
// 所以 total 也不能把别人的报告算进去。内存里筛一页的话，total 会泄出他人报告的数量，
// 分页控件还会长出点不到的页码。
func TestReportListHidesOtherUsersAndTheirTotals(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	insertTestReport(t, s, "own-1", "c1", "latest-md", "alice", base)
	insertTestReport(t, s, "own-2", "c1", "latest-md", "alice", base.Add(time.Minute))
	insertTestReport(t, s, "other-1", "c1", "latest-md", "bob", base.Add(2*time.Minute))
	insertTestReport(t, s, "other-2", "c2", "latest-md", "bob", base.Add(3*time.Minute))

	items, total, err := s.Reports().List(ctx, ReportQuery{ViewerID: "alice"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("alice 应只看到自己的 2 条, got total=%d items=%d", total, len(items))
	}
	for _, r := range items {
		if r.CreatedBy != "alice" {
			t.Fatalf("列表漏进他人报告: %s owner=%s", r.ID, r.CreatedBy)
		}
	}
	// 倒序：newest first（own-2 比 own-1 晚）。
	if items[0].ID != "own-2" {
		t.Fatalf("应按 created_at 倒序, got first=%s", items[0].ID)
	}

	// admin 看全量。
	if _, total, err := s.Reports().List(ctx, ReportQuery{Admin: true}); err != nil || total != 4 {
		t.Fatalf("admin 应看到 4 条, got total=%d err=%v", total, err)
	}
}

// TestReportListRequiresViewerForNonAdmin 守住那个最危险的默认值：非 admin 且没有查看者
// 身份时，"无过滤条件"等于把全表交出去。
func TestReportListRequiresViewerForNonAdmin(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Reports().List(context.Background(), ReportQuery{}); err == nil {
		t.Fatal("非 admin 且 ViewerID 为空应报错")
	}
}

// TestReportListSystemReportsFollowClusterGrants 系统体检报告按集群授权放行，
// cluster_id 为空的系统报告只 admin 能读——没有可比对的授权对象时默认拒绝。
func TestReportListSystemReportsFollowClusterGrants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	insertTestReport(t, s, "sys-c1", "c1", "latest-md", objectstore.SystemActor, base)
	insertTestReport(t, s, "sys-c2", "c2", "latest-md", objectstore.SystemActor, base)
	insertTestReport(t, s, "sys-nocluster", "", "latest-md", objectstore.SystemActor, base)

	items, total, err := s.Reports().List(ctx, ReportQuery{ViewerID: "alice", SystemClusterIDs: []string{"c1", "c1", " "}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || items[0].ID != "sys-c1" {
		t.Fatalf("只应看到被授权集群的系统报告, got total=%d items=%d", total, len(items))
	}

	items, total, err = s.Reports().List(ctx, ReportQuery{ViewerID: "alice", AllSystemClusters: true})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 2 {
		ids := make([]string, 0, len(items))
		for _, r := range items {
			ids = append(ids, r.ID)
		}
		t.Fatalf("全域授权应看到 2 条系统报告（空 cluster_id 不算）, got total=%d ids=%v", total, ids)
	}

	if _, total, err := s.Reports().List(ctx, ReportQuery{Admin: true}); err != nil || total != 3 {
		t.Fatalf("admin 应看到含空 cluster_id 的 3 条, got total=%d err=%v", total, err)
	}
}

// TestReportListFiltersAndPaging cluster/kind 过滤必须与归属谓词是 AND 关系：
// 归属三段 OR 少了括号时，kind 过滤会被 OR 吞掉，别人的报告也算命中。
func TestReportListFiltersAndPaging(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	insertTestReport(t, s, "a-md", "c1", "latest-md", "alice", base)
	insertTestReport(t, s, "a-json", "c1", "latest-json", "alice", base.Add(time.Minute))
	insertTestReport(t, s, "b-md", "c1", "latest-md", "bob", base.Add(2*time.Minute))
	insertTestReport(t, s, "a-c2", "c2", "latest-md", "alice", base.Add(3*time.Minute))

	_, total, err := s.Reports().List(ctx, ReportQuery{ViewerID: "alice", Kind: "latest-md", ClusterID: "c1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Fatalf("kind+cluster 过滤应命中 1 条, got %d", total)
	}

	// alice 的三条按时间倒序是 a-c2 > a-json > a-md；total 始终是 3（不是当页条数）。
	items, total, err := s.Reports().List(ctx, ReportQuery{ViewerID: "alice", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != 3 || len(items) != 1 || items[0].ID != "a-json" {
		t.Fatalf("offset 分页应给出第二条 a-json, got total=%d items=%d", total, len(items))
	}
}

// TestReportGetRoundTrip 单读要能取回正文定位字段与 available 位。
func TestReportGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	want := insertTestReport(t, s, "r-1", "c1", "latest-md", "alice", time.Now().UTC())

	got, err := s.Reports().Get(ctx, "r-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BlobKey != want.BlobKey || got.SHA256 != want.SHA256 || !got.Available || got.Findings != 2 {
		t.Fatalf("读回字段不符: %+v", got)
	}
	if _, err := s.Reports().Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的报告应 ErrNotFound, got %v", err)
	}
}
