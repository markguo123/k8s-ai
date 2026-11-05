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

// putStructured 写一份报告正文（md 或 json 都走网关，key 由后端算）。
func (f *reportFixture) putStructured(t *testing.T, body, ext string, scope []string) objectstore.Ref {
	t.Helper()
	ref, err := f.gw.Put(context.Background(), objectstore.Spec{
		Category:  objectstore.CategoryReports,
		Scope:     scope,
		Ext:       ext,
		MediaType: objectstore.ResolveMediaType("", ext),
	}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put content: %v", err)
	}
	return ref
}

// seedStructuredRow 落一行可控 kind / scan_id 的报告行。seedReport 把这两列写死成
// latest-md / scan-<id>，而「同一轮扫描的两行产物」正是详情页取结构化数据的依赖。
func (f *reportFixture) seedStructuredRow(t *testing.T, id, kind, scanID, actor, clusterID string, ref objectstore.Ref) {
	t.Helper()
	ctx := context.Background()
	row := &store.Report{
		ID: id, ClusterID: clusterID, ScanID: scanID, Kind: kind,
		BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		Findings: 1, CreatedBy: actor, Available: true, CreatedAt: time.Now().UTC(),
	}
	err := f.st.WithTx(ctx, func(tx *store.Tx) error {
		if err := tx.InsertReport(ctx, row); err != nil {
			return err
		}
		return tx.Acquire(ctx, ref, objectstore.DriverDB, actor)
	})
	if err != nil {
		t.Fatalf("seed report %s: %v", id, err)
	}
}

func v2InspectionBody(t *testing.T) string {
	t.Helper()
	res := model.ScanResult{
		SchemaVersion: model.ReportSchemaVersion,
		Meta:          model.ScanMeta{Cluster: "prod", ScanStartedAt: "2026-11-04T09:00:00Z", Duration: "12s"},
		Findings:      []model.Finding{{ID: "F1", Severity: model.SeverityCritical, Title: "副本不足", Summary: "nginx 只有 1 个可用副本"}},
		Conclusion:    &model.Conclusion{Level: "critical", Headline: "发现 1 个严重风险"},
		Collection:    &model.CollectionQuality{Planned: 10, Collected: 9, Integrity: model.IntegrityPartial},
		Stages:        []model.StageRecord{{Name: "collect", Title: "资源采集", Status: "done", Elapsed: "1.2s"}},
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

type structuredResponse struct {
	RequestID string `json:"request_id"`
	Data      struct {
		Report struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"report"`
		SchemaVersion int             `json:"schemaVersion"`
		Legacy        bool            `json:"legacy"`
		Inspection    json.RawMessage `json:"inspection"`
	} `json:"data"`
	Error *APIError `json:"error"`
}

// TestReportStructuredEndpointRendersJSONReport 是详情页的正向契约：v2 报告一次请求拿到
// 结论、采集质量、阶段账，且响应里没有对象 key。
func TestReportStructuredEndpointRendersJSONReport(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "carol", "user")
	f.seedStructuredRow(t, "r-v2", service.ReportKindLatestJSON, "scan-v2", "u-carol", "",
		f.putStructured(t, v2InspectionBody(t), ".json", []string{"local", "scan-v2"}))

	status, head, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-v2/report", owner, "")
	if status != http.StatusOK {
		t.Fatalf("读自己的结构化报告应 200：%d %s", status, raw)
	}
	var got structuredResponse
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("解析响应失败：%v / %s", err, raw)
	}
	if got.Data.Report.ID != "r-v2" || got.Data.SchemaVersion != model.ReportSchemaVersion || got.Data.Legacy {
		t.Fatalf("报告头不对：%+v", got.Data)
	}
	var inspection model.ScanResult
	if err := json.Unmarshal(got.Data.Inspection, &inspection); err != nil {
		t.Fatalf("inspection 不是体检报告结构：%v", err)
	}
	if inspection.Conclusion == nil || inspection.Conclusion.Level != "critical" {
		t.Fatalf("结论区块丢失：%s", got.Data.Inspection)
	}
	if inspection.Collection == nil || inspection.Collection.Integrity != model.IntegrityPartial {
		t.Fatalf("采集质量区块丢失：%s", got.Data.Inspection)
	}
	if len(inspection.Stages) != 1 || inspection.Stages[0].Name != "collect" {
		t.Fatalf("阶段账丢失：%+v", inspection.Stages)
	}
	// 对象存储布局绝不进响应：blob_key 一旦外泄，报告接口就成了对象枚举面。
	if strings.Contains(raw, "objects/") || strings.Contains(raw, got.Data.Report.Kind+"$$") {
		t.Fatalf("响应里出现对象 key：%s", raw)
	}
	// 详情页必须能在浏览器里被 fetch 渲染：附件头会让浏览器把它当下载文件而不是数据。
	if cd := head.Get("Content-Disposition"); strings.Contains(cd, "attachment") {
		t.Fatalf("/report 不该强制下载：%q", cd)
	}
}

// TestReportStructuredFollowsScanSibling 锁详情页的可用性：报告中心列出的、用户点开的都是
// Markdown 那一行，同一轮的 JSON 产物必须靠 scan_id 顺出来，而不是让前端去猜行 id。
func TestReportStructuredFollowsScanSibling(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "dan", "user")
	f.seedStructuredRow(t, "r-md", service.ReportKindLatestMD, "scan-round", "u-dan", "",
		f.putStructured(t, "# 体检报告\n\n1 个严重风险", ".md", []string{"local", "scan-round"}))
	f.seedStructuredRow(t, "r-json", service.ReportKindLatestJSON, "scan-round", "u-dan", "",
		f.putStructured(t, v2InspectionBody(t), ".json", []string{"local", "scan-round"}))

	status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-md/report", owner, "")
	if status != http.StatusOK {
		t.Fatalf("从 Markdown 行应能取到结构化数据：%d %s", status, raw)
	}
	var got structuredResponse
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data.Report.ID != "r-md" {
		t.Fatalf("报告头应仍是用户点开的那一行：%+v", got.Data.Report)
	}
	var inspection model.ScanResult
	if err := json.Unmarshal(got.Data.Inspection, &inspection); err != nil {
		t.Fatal(err)
	}
	if len(inspection.Findings) != 1 || inspection.Findings[0].ID != "F1" {
		t.Fatalf("没顺到同一轮的 JSON：%s", got.Data.Inspection)
	}
}

// TestReportStructuredMarkdownOnlyReports415：只归档了 Markdown 的老报告要给「去看原文」
// 的明确信号，不能给一个渲染不出来的 200。
func TestReportStructuredMarkdownOnlyReports415(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "eve", "user")
	f.seedStructuredRow(t, "r-old", service.ReportKindLatestMD, "scan-old", "u-eve", "",
		f.putStructured(t, "# 老报告", ".md", []string{"local", "scan-old"}))

	status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-old/report", owner, "")
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("只有 Markdown 应 415：%d %s", status, raw)
	}
	if got := errorOf(t, raw); !strings.HasPrefix(got, codeUnsupportedMediaType+"|") {
		t.Fatalf("错误码 = %s，期望 %s", got, codeUnsupportedMediaType)
	}
	// 同一条报告在原文下载端点上仍然是可下载的：415 只说「没有结构化形态」。
	if s, _, body := f.req(t, http.MethodGet, "/api/v1/reports/r-old/content", owner, ""); s != http.StatusOK {
		t.Fatalf("原文下载应照常 200：%d %s", s, body)
	}
}

// TestReportStructuredVisibilityMatchesDownload 锁「详情页不是第二条授权通道」：别人的报告
// 在这里必须与 /content 给出同一个 not_found。
func TestReportStructuredVisibilityMatchesDownload(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "frank", "user")
	other := f.userToken(t, "grace", "user")
	f.seedStructuredRow(t, "r-priv", service.ReportKindLatestJSON, "scan-priv", "u-frank", "",
		f.putStructured(t, v2InspectionBody(t), ".json", []string{"local", "scan-priv"}))

	if s, _, _ := f.req(t, http.MethodGet, "/api/v1/reports/r-priv/report", owner, ""); s != http.StatusOK {
		t.Fatalf("本人应 200")
	}
	sOther, _, rawOther := f.req(t, http.MethodGet, "/api/v1/reports/r-priv/report", other, "")
	sContent, _, rawContent := f.req(t, http.MethodGet, "/api/v1/reports/r-priv/content", other, "")
	if sOther != http.StatusNotFound || sContent != http.StatusNotFound {
		t.Fatalf("他人应同形 404：report=%d content=%d", sOther, sContent)
	}
	if errorOf(t, rawOther) != errorOf(t, rawContent) {
		t.Fatalf("两个端点的越权响应不同形：%s vs %s", errorOf(t, rawOther), errorOf(t, rawContent))
	}
	if s, _, body := f.req(t, http.MethodGet, "/api/v1/reports/r-priv/report", "", ""); s != http.StatusUnauthorized {
		t.Fatalf("未登录应 401：%d %s", s, body)
	}
}

// TestReportStructuredCrossOwnerAudits 与下载一致：管理员看别人的体检结论要留痕。
func TestReportStructuredCrossOwnerAudits(t *testing.T) {
	f := newReportFixture(t, nil)
	f.seedStructuredRow(t, "r-audit", service.ReportKindLatestJSON, "scan-audit", "u-henry", "",
		f.putStructured(t, v2InspectionBody(t), ".json", []string{"local", "scan-audit"}))

	if s, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-audit/report", f.adminToken(t), ""); s != http.StatusOK {
		t.Fatalf("admin 读他人报告应 200：%d %s", s, raw)
	}
	audits := downloadAudits(t, f.st)
	if len(audits) != 1 || audits[0].ResourceName != "r-audit" {
		t.Fatalf("跨 owner 读取应落一条下载审计：%+v", audits)
	}
}
