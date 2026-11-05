package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// seedScopedReport 落一行带范围留痕的报告：P9.13 的「报告类别」完全由 scope 两列算出来，
// 没有带范围的行就没法验证总览与筛选是不是同一套口径。
func (f *reportFixture) seedScopedReport(t *testing.T, id, actor, scopeKind, scopeValue, kind, ext, body string) {
	t.Helper()
	ctx := context.Background()
	scanID := "scan-" + id
	ref := f.putStructured(t, body, ext, []string{"local", scanID})
	row := &store.Report{
		ID: id, ScanID: scanID, Kind: kind, BlobKey: ref.Key, MediaType: ref.MediaType,
		Size: ref.Size, SHA256: ref.SHA256, Findings: 1, CreatedBy: actor,
		Available: true, CreatedAt: time.Now().UTC(),
		ScopeKind: scopeKind, ScopeValue: scopeValue,
	}
	err := f.st.WithTx(ctx, func(tx *store.Tx) error {
		if err := tx.InsertReport(ctx, row); err != nil {
			return err
		}
		return tx.Acquire(ctx, ref, objectstore.DriverDB, actor)
	})
	if err != nil {
		t.Fatalf("seed scoped report %s: %v", id, err)
	}
}

// seedReportSet 铺四类产物：1 集群体检 + 1 命名空间 + 1 Pod + 1 无范围（存量/CLI）。
func seedReportSet(t *testing.T, f *reportFixture, actor string) {
	t.Helper()
	f.seedScopedReport(t, "s-cluster", actor, model.ScopeCluster, "", service.ReportKindLatestJSON, ".json", v2InspectionBody(t))
	f.seedScopedReport(t, "s-ns", actor, model.ScopeNamespace, "arc", service.ReportKindLatestJSON, ".json", v2InspectionBody(t))
	f.seedScopedReport(t, "s-pod", actor, model.ScopePod, "arc/web-0", service.ReportKindLatestJSON, ".json", v2InspectionBody(t))
	f.seedScopedReport(t, "s-legacy", actor, "", "", service.ReportKindLatestMD, ".md", "# 老报告")
}

func reportOverviewOf(t *testing.T, raw string) service.ReportOverview {
	t.Helper()
	var env struct {
		Data service.ReportOverview `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("解析总览失败：%v / %s", err, raw)
	}
	return env.Data
}

// TestReportSummaryMatchesListFilter 锁 P9.13 最要紧的一条不变量：总览的类别数字与
// scopeCategory 筛出来的行数必须来自同一条归属谓词。两者不一致时页面会写着「业务体检 5」
// 而点开只有 2 行，这种自相矛盾比没有总览更伤信任。
func TestReportSummaryMatchesListFilter(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "kate", "user")
	seedReportSet(t, f, "u-kate")

	status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/summary", owner, "")
	if status != http.StatusOK {
		t.Fatalf("总览应 200：%d %s", status, raw)
	}
	got := reportOverviewOf(t, raw)
	if got.Total != 4 || got.Cluster != 1 || got.Business != 2 || got.Other != 1 {
		t.Fatalf("类别计数不对：%+v", got)
	}
	if got.Markdown != 1 || got.JSON != 3 || got.Available != 4 {
		t.Fatalf("产物形态计数不对：%+v", got)
	}

	// 筛选与总览同步收窄：筛业务体检时，总览的 total 就是筛出来的行数。
	_, _, rawList := f.req(t, http.MethodGet, "/api/v1/reports?scopeCategory=business", owner, "")
	views, page := reportViews(t, rawList)
	if page == nil || page.Total != 2 || len(views) != 2 {
		t.Fatalf("业务体检应筛出 2 行：%+v / %+v", views, page)
	}
	_, _, rawScoped := f.req(t, http.MethodGet, "/api/v1/reports/summary?scopeCategory=business", owner, "")
	if scoped := reportOverviewOf(t, rawScoped); scoped.Total != 2 || scoped.Business != 2 || scoped.Cluster != 0 {
		t.Fatalf("带类别的总览没收窄：%+v", scoped)
	}

	// 别人的报告既不进列表也不进计数：总览不能成为第二条枚举面。
	other := f.userToken(t, "leo", "user")
	_, _, rawOther := f.req(t, http.MethodGet, "/api/v1/reports/summary", other, "")
	if empty := reportOverviewOf(t, rawOther); empty.Total != 0 {
		t.Fatalf("他人总览应为空：%+v", empty)
	}
}

// TestReportListCarriesAuthorName 锁「发起人」这一列：列表要给出用户名而不是用户 ID，
// 一串 8 字节随机 ID 对翻报告的人没有任何信息量。
func TestReportListCarriesAuthorName(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "mona", "user")
	f.seedScopedReport(t, "s-name", "u-mona", model.ScopeNamespace, "arc", service.ReportKindLatestJSON, ".json", v2InspectionBody(t))

	_, _, raw := f.req(t, http.MethodGet, "/api/v1/reports", owner, "")
	if !strings.Contains(raw, `"createdByName":"mona"`) {
		t.Fatalf("列表应带发起人用户名：%s", raw)
	}
}

// TestReportExportMarkdownRendersFromJSON 是「一次体检只留一份产物」的下半场：选了 JSON
// 归档的报告，Markdown 由同一份正文当场渲染，归档里不会长出第二行。
func TestReportExportMarkdownRendersFromJSON(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "nina", "user")
	f.seedScopedReport(t, "s-json", "u-nina", model.ScopeNamespace, "arc", service.ReportKindLatestJSON, ".json", v2InspectionBody(t))

	status, head, body := f.req(t, http.MethodGet, "/api/v1/reports/s-json/export", owner, "")
	if status != http.StatusOK {
		t.Fatalf("按需导出应 200：%d %s", status, body)
	}
	if ct := head.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("Content-Type = %q，期望 text/markdown", ct)
	}
	cd := head.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".md") {
		t.Fatalf("导出必须是附件且扩展名为 .md：%q", cd)
	}
	// 渲染自 JSON，而不是把 JSON 原文当 Markdown 发出去。
	if !strings.HasPrefix(body, "# Kubernetes ") || strings.Contains(body, `"schemaVersion"`) {
		t.Fatalf("导出正文不是渲染出来的 Markdown：%s", body[:min(160, len(body))])
	}
}

// TestReportExportMarkdownPassesThroughLegacy：只有 Markdown 正文的存量报告原样交回，
// 不做二次解析——用户要的东西本来就在这一行里。
func TestReportExportMarkdownPassesThroughLegacy(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "otto", "user")
	f.seedScopedReport(t, "s-old", "u-otto", "", "", service.ReportKindLatestMD, ".md", "# 老报告\n\n正文")

	status, _, body := f.req(t, http.MethodGet, "/api/v1/reports/s-old/export", owner, "")
	if status != http.StatusOK || body != "# 老报告\n\n正文" {
		t.Fatalf("Markdown 报告应原样导出：%d %q", status, body)
	}
}

// TestReportExportVisibilityMatchesDownload 锁越权同形：导出端点不能成为第二条读取他人
// 报告的通道，别人的 id 与不存在的 id 必须回一模一样的 404。
func TestReportExportVisibilityMatchesDownload(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "pam", "user")
	f.seedScopedReport(t, "s-priv", "u-pam", model.ScopeNamespace, "arc", service.ReportKindLatestJSON, ".json", v2InspectionBody(t))

	other := f.userToken(t, "quinn", "user")
	if s, _, body := f.req(t, http.MethodGet, "/api/v1/reports/s-priv/export", owner, ""); s != http.StatusOK {
		t.Fatalf("本人导出应 200：%d %s", s, body)
	}
	sExport, _, rawExport := f.req(t, http.MethodGet, "/api/v1/reports/s-priv/export", other, "")
	sContent, _, rawContent := f.req(t, http.MethodGet, "/api/v1/reports/s-priv/content", other, "")
	if sExport != http.StatusNotFound || sContent != http.StatusNotFound {
		t.Fatalf("他人导出应同形 404：export=%d content=%d", sExport, sContent)
	}
	if errorOf(t, rawExport) != errorOf(t, rawContent) {
		t.Fatalf("两个端点的越权响应不同形：%s vs %s", errorOf(t, rawExport), errorOf(t, rawContent))
	}
	if s, _, body := f.req(t, http.MethodGet, "/api/v1/reports/s-priv/export", "", ""); s != http.StatusUnauthorized {
		t.Fatalf("未登录应 401：%d %s", s, body)
	}
}
