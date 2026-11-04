package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// seedReportRow 落一行可控 kind / scan_id 的报告。seedPlatformReport 把这两列写死了
// （latest-md / scan-<id>），而「同一轮扫描的两行产物」正是兄弟行查找的前提，必须能造。
func seedReportRow(t *testing.T, st store.Store, id, kind, scanID, actor, clusterID string, ref objectstore.Ref) {
	t.Helper()
	row := &store.Report{
		ID: id, ClusterID: clusterID, ScanID: scanID, Kind: kind,
		BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		Findings: 1, CreatedBy: actor, Available: true, CreatedAt: time.Now().UTC(),
	}
	err := st.WithTx(context.Background(), func(tx *store.Tx) error {
		if err := tx.InsertReport(context.Background(), row); err != nil {
			return err
		}
		return tx.Acquire(context.Background(), ref, objectstore.DriverDB, actor)
	})
	if err != nil {
		t.Fatalf("seed report %s: %v", id, err)
	}
}

func v2ReportBody(t *testing.T) string {
	t.Helper()
	res := &model.ScanResult{
		SchemaVersion: model.ReportSchemaVersion,
		Meta:          model.ScanMeta{Cluster: "prod", ScanStartedAt: "2026-11-04T09:00:00Z"},
		Findings:      []model.Finding{{ID: "F1", Severity: model.SeverityCritical, Summary: "nginx 副本不足"}},
		Conclusion:    &model.Conclusion{Level: "critical", Headline: "发现 1 个严重风险"},
		Collection:    &model.CollectionQuality{Planned: 10, Collected: 9, Integrity: model.IntegrityPartial},
		Stages:        []model.StageRecord{{Name: "collect", Title: "资源采集", Status: "done"}},
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestOpenStructuredReportReadsJSONRow(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	body := v2ReportBody(t)
	ref := putPlatformContent(t, backend, body, []string{"c1", "scan-1"}, ".json", "alice")
	seedReportRow(t, st, "r-json", ReportKindLatestJSON, "scan-1", "alice", "c1", ref)

	sr, err := platform.OpenStructuredReport(ctx, "r-json", ReportScope{ViewerID: "alice"})
	if err != nil {
		t.Fatalf("读结构化报告失败：%v", err)
	}
	if sr.Legacy || sr.SchemaVersion != model.ReportSchemaVersion {
		t.Fatalf("schema 判定错：%d legacy=%v", sr.SchemaVersion, sr.Legacy)
	}
	if sr.Result.Conclusion == nil || sr.Result.Conclusion.Level != "critical" {
		t.Fatalf("结论区块没读出来：%+v", sr.Result.Conclusion)
	}
	if len(sr.Result.Stages) != 1 || sr.Result.Stages[0].Name != "collect" {
		t.Fatalf("阶段账没读出来：%+v", sr.Result.Stages)
	}
	if sr.Report.ID != "r-json" {
		t.Fatalf("报告行 = %s，期望 r-json", sr.Report.ID)
	}
}

// TestOpenStructuredReportFindsScanSibling 锁 P9 的关键行为：页面点开的是 Markdown 那一行
// （报告列表里只有它），详情页必须能顺 scan_id 找到同一轮的 JSON 产物。
func TestOpenStructuredReportFindsScanSibling(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	mdRef := putPlatformContent(t, backend, "# 体检报告\n\n1 个严重风险", []string{"c1", "scan-2"}, ".md", "alice")
	seedReportRow(t, st, "r-md", ReportKindLatestMD, "scan-2", "alice", "c1", mdRef)
	jsonRef := putPlatformContent(t, backend, v2ReportBody(t), []string{"c1", "scan-2"}, ".json", "alice")
	seedReportRow(t, st, "r-json", ReportKindLatestJSON, "scan-2", "alice", "c1", jsonRef)

	sr, err := platform.OpenStructuredReport(ctx, "r-md", ReportScope{ViewerID: "alice"})
	if err != nil {
		t.Fatalf("从 Markdown 行找不到同一轮的 JSON：%v", err)
	}
	// 回的是用户点开的那一行：报告头不该冒出另一行的 id。
	if sr.Report.ID != "r-md" {
		t.Fatalf("报告行 = %s，期望 r-md", sr.Report.ID)
	}
	if sr.Result == nil || len(sr.Result.Findings) != 1 || sr.Result.Findings[0].ID != "F1" {
		t.Fatalf("内容没取到兄弟行：%+v", sr.Result)
	}
}

// TestOpenStructuredReportMarkdownOnly 是老报告的真实形状：只归档了 .md。诚实报错，
// 不拿 Markdown 硬凑一个空的结构化对象让前端渲染出一片假空白。
func TestOpenStructuredReportMarkdownOnly(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	mdRef := putPlatformContent(t, backend, "# 老报告", []string{"c1", "scan-3"}, ".md", "alice")
	seedReportRow(t, st, "r-old", ReportKindLatestMD, "scan-3", "alice", "c1", mdRef)

	_, err := platform.OpenStructuredReport(ctx, "r-old", ReportScope{ViewerID: "alice"})
	if !errors.Is(err, ErrReportNotStructured) {
		t.Fatalf("只有 Markdown 的报告应报 ErrReportNotStructured，实得 %v", err)
	}
}

// TestOpenStructuredReportDoesNotCrossOwnerViaSibling 是兄弟行查找的授权底线：同一次扫描
// 里别人那份 JSON 不能被「顺 scan_id」顺出来——兄弟查询带的仍是调用者自己的 scope。
func TestOpenStructuredReportDoesNotCrossOwnerViaSibling(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	mdRef := putPlatformContent(t, backend, "# 我的 Markdown", []string{"c1", "scan-4"}, ".md", "alice")
	seedReportRow(t, st, "r-mine", ReportKindLatestMD, "scan-4", "alice", "c1", mdRef)
	jsonRef := putPlatformContent(t, backend, v2ReportBody(t), []string{"c1", "scan-4"}, ".json", "bob")
	seedReportRow(t, st, "r-bobs-json", ReportKindLatestJSON, "scan-4", "bob", "c1", jsonRef)

	_, err := platform.OpenStructuredReport(ctx, "r-mine", ReportScope{ViewerID: "alice"})
	if !errors.Is(err, ErrReportNotStructured) {
		t.Fatalf("不该借兄弟行读到别人的产物，实得 %v", err)
	}
	if strings.Contains(err.Error(), "bob") {
		t.Fatalf("错误文案泄出归属：%v", err)
	}

	// admin 的 scope 能看到那一行，才算「同一轮」真的找得到。
	sr, err := platform.OpenStructuredReport(ctx, "r-mine", ReportScope{ViewerID: "root", Admin: true})
	if err != nil {
		t.Fatalf("admin 应能顺到兄弟行：%v", err)
	}
	if sr.Result == nil || len(sr.Result.Findings) == 0 {
		t.Fatalf("admin 读到的不是 JSON 那一行：%+v", sr.Result)
	}
}

func TestOpenStructuredReportLegacyReportsV1(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	// v1 报告：没有 schemaVersion 字段，也没有结论/采集质量区块。
	ref := putPlatformContent(t, backend, `{"meta":{"cluster":"old"},"findings":[],"healthScore":{"score":88,"max":100}}`,
		[]string{"c1", "scan-5"}, ".json", "alice")
	seedReportRow(t, st, "r-v1", ReportKindLatestJSON, "scan-5", "alice", "c1", ref)

	sr, err := platform.OpenStructuredReport(ctx, "r-v1", ReportScope{ViewerID: "alice"})
	if err != nil {
		t.Fatalf("老报告应可读：%v", err)
	}
	if !sr.Legacy || sr.SchemaVersion != 1 {
		t.Fatalf("老报告应归一化成 v1：%d legacy=%v", sr.SchemaVersion, sr.Legacy)
	}
	if sr.Result.HealthScore.Score != 88 {
		t.Fatalf("老报告字段丢失：%+v", sr.Result.HealthScore)
	}
}

func TestOpenStructuredReportOwnershipStillRequired(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	ref := putPlatformContent(t, backend, v2ReportBody(t), []string{"c1", "scan-6"}, ".json", "bob")
	seedReportRow(t, st, "r-bob", ReportKindLatestJSON, "scan-6", "bob", "c1", ref)

	_, err := platform.OpenStructuredReport(ctx, "r-bob", ReportScope{ViewerID: "alice"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("越权应与不存在同形：%v", err)
	}
}

func TestLooksLikeJSON(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:               true,
		"  \n\t{\"a\":1}":       true,
		"\xef\xbb\xbf{\"a\":1}": true,
		"# 体检报告":                false,
		"":                      false,
		"   ":                   false,
		"[1,2]":                 false, // 报告根只能是对象
	}
	for body, want := range cases {
		if got := looksLikeJSON([]byte(body)); got != want {
			t.Errorf("looksLikeJSON(%q) = %v，期望 %v", body, got, want)
		}
	}
}
