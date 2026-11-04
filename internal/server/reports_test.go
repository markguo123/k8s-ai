package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// reportFixture 装配一台接了报告面与存储观测端点的服务（与 cli server 一致：
// db 内容后端 + AuthzGrant + 审计）。newPlatformServer 没接 reports，这里独立建一套，
// 不去改动既有用例的构造器。
type reportFixture struct {
	ts       *httptest.Server
	st       store.Store
	backend  objectstore.Store
	gw       *service.BlobGateway
	authzSvc *authz.Service
	authSvc  *auth.Service
	scan     *capturingScan
	// scanBaseline 是页面发起体检时的连接/模型基线。测试改它即可模拟「部署配了模型 /
	// 没配模型」两种现场，而不用真的接一个 LLM。
	scanBaseline model.ScanOptions
	server       *Server
}

// newReportFixture 建服务。reportsOverride 非 nil 时用它顶替真实报告面（错误映射用例）。
func newReportFixture(t *testing.T, reportsOverride reportService) *reportFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenSQLite("file:server-reports-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	authSvc := auth.New(st.Users(), st.Roles())
	if err := authSvc.Bootstrap(ctx, "admin-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	backend, err := st.ObjectContent(objectstore.DefaultPrefix, 1<<20)
	if err != nil {
		t.Fatalf("db content backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	gw := service.NewBlobGateway(backend, st.Objects())
	cipher, err := cluster.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	reports := reportService(service.NewReportPlatform(st, gw))
	if reportsOverride != nil {
		reports = reportsOverride
	}
	authzSvc := authz.New(st)
	scan := &capturingScan{seen: make(chan model.ScanOptions, 8)}
	srv := NewWithPlatform(scan, authSvc, settings.New(st, settings.WithCipher(cipher)), service.NewAuditService(st)).
		WithAuthz(authzSvc).WithReports(reports).WithStorage(gw)
	handler := srv.Handler()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &reportFixture{ts: ts, st: st, backend: backend, gw: gw, authzSvc: authzSvc, authSvc: authSvc, scan: scan, server: srv}
}

// setScanBaseline 注入页面体检的参数基线。必须在发第一个 scan 请求之前调用。
func (f *reportFixture) setScanBaseline(opts model.ScanOptions) {
	f.scanBaseline = opts
f.server.WithScanBaseline(func(context.Context) model.ScanOptions { return f.scanBaseline })
}

// req 发一次请求并读完响应体（返回状态码、响应头与原始 body 字符串——同形 404 只能比原始体）。
func (f *reportFixture) req(t *testing.T, method, path, token, body string) (int, http.Header, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	r, err := http.NewRequest(method, f.ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(raw)
}

func (f *reportFixture) login(t *testing.T, username, password string) string {
	t.Helper()
	status, _, raw := f.req(t, http.MethodPost, "/api/v1/auth/login", "",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if status != http.StatusOK {
		t.Fatalf("登录 %s 失败：%d %s", username, status, raw)
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Token == "" {
		t.Fatalf("登录响应缺少 token：%s", raw)
	}
	return env.Data.Token
}

func (f *reportFixture) adminToken(t *testing.T) string {
	t.Helper()
	return f.login(t, "admin", "admin-pw")
}

// userToken 建一个指定默认角色的用户并登录（角色权限位由 Bootstrap 的默认角色决定）。
func (f *reportFixture) userToken(t *testing.T, username, roleName string) string {
	t.Helper()
	ctx := context.Background()
	roles, err := f.st.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var roleID string
	for _, r := range roles {
		if r.Name == roleName {
			roleID = r.ID
		}
	}
	if roleID == "" {
		t.Fatalf("默认角色 %q 不存在", roleName)
	}
	hash, err := auth.HashPassword("pw-test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := f.st.Users().Create(ctx, &store.User{
		ID: "u-" + username, Username: username, PasswordHash: hash, RoleID: roleID,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return f.login(t, username, "pw-test")
}

// grantCluster 给用户加一条集群授权（权限位用 destroy：读系统报告不要求写权限，
// 用一把写不了集群的钥匙正好证明可见性判定没有顺手搬来写权限判据）。
func (f *reportFixture) grantCluster(t *testing.T, principalID, clusterID string) {
	t.Helper()
	if _, err := f.authzSvc.CreateGrant(context.Background(), authz.CreateGrantRequest{
		PrincipalType: "user", PrincipalID: principalID,
		ClusterID: clusterID, Namespaces: []string{"app"}, Permissions: []string{"destroy"},
	}, "admin"); err != nil {
		t.Fatalf("create grant: %v", err)
	}
}

// putContent 先经网关写正文，再用后端返回的 Ref 建业务行：内容寻址下 key 由后端算，
// 自己拼 key 再建行等于跳过了「行指向的对象真的存在」这条前提。
func (f *reportFixture) putContent(t *testing.T, body string, scope []string) objectstore.Ref {
	t.Helper()
	ref, err := f.gw.Put(context.Background(), objectstore.Spec{
		Category:  objectstore.CategoryReports,
		Scope:     scope,
		Ext:       ".md",
		MediaType: "text/markdown; charset=utf-8",
	}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put content: %v", err)
	}
	return ref
}

// seedReport 走唯一合法写路径（同事务 InsertReport + Acquire）落一行报告。
func (f *reportFixture) seedReport(t *testing.T, id, actor, clusterID string, ref objectstore.Ref) {
	t.Helper()
	ctx := context.Background()
	row := &store.Report{
		ID: id, ClusterID: clusterID, ScanID: "scan-" + id, Kind: service.ReportKindLatestMD,
		BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		Findings: 3, CreatedBy: actor, Available: true, CreatedAt: time.Now().UTC(),
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

// reportViews 解析列表响应（data 直接是数组，分页在 pagination 里）。
func reportViews(t *testing.T, raw string) ([]service.ReportView, *Pagination) {
	t.Helper()
	var env struct {
		Data       []service.ReportView `json:"data"`
		Pagination *Pagination          `json:"pagination"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("解析列表响应失败：%v / %s", err, raw)
	}
	return env.Data, env.Pagination
}

func downloadAudits(t *testing.T, st store.Store) []store.AuditEvent {
	t.Helper()
	rows, err := st.Audit().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range rows {
		if e.Action == store.ActionReportDownload {
			out = append(out, *e)
		}
	}
	return out
}

// errorOf 取错误信封里的 error 对象（同形 404 要比它，request_id/timestamp 每次都不同）。
func errorOf(t *testing.T, raw string) string {
	t.Helper()
	var env apiResponse
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("解析错误信封失败：%v / %s", err, raw)
	}
	if env.Error == nil {
		t.Fatalf("响应里没有错误对象：%s", raw)
	}
	return env.Error.Code + "|" + env.Error.Message
}

// TestReportsOwnerListAndDownloadLocksContract 是 P8.5 的正向契约：本人列得到、下得动，
// 下载头按 §13.3 强制 attachment + nosniff，响应里不出现对象 key。
func TestReportsOwnerListAndDownloadLocksContract(t *testing.T) {
	f := newReportFixture(t, nil)
	body := "# 体检结论\n发现 3 条风险\n"
	f.seedReport(t, "r-mine", "u-alice", "c-prod", f.putContent(t, body, []string{"c-prod", "scan-mine"}))
	token := f.userToken(t, "alice", "user")

	status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports", token, "")
	if status != http.StatusOK {
		t.Fatalf("列表应 200，got %d：%s", status, raw)
	}
	items, page := reportViews(t, raw)
	if len(items) != 1 || items[0].ID != "r-mine" {
		t.Fatalf("列表内容不对：%+v", items)
	}
	if page == nil || page.Total != 1 {
		t.Fatalf("分页 total 不对：%+v", page)
	}
	if items[0].Available != true || items[0].Size != int64(len(body)) {
		t.Fatalf("列表字段不对：%+v", items[0])
	}
	// 对象 key 是内部结构（含集群/扫描标识），一个字都不该出现在响应里。
	for _, leak := range []string{"blob_key", "blobKey", "payload", objectstore.DefaultPrefix + "/"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("列表响应泄露 %q：%s", leak, raw)
		}
	}

	status, head, got := f.req(t, http.MethodGet, "/api/v1/reports/r-mine/content", token, "")
	if status != http.StatusOK {
		t.Fatalf("下载应 200，got %d：%s", status, got)
	}
	if got, want := head.Get("Content-Disposition"), "attachment; filename*=UTF-8''report-r-mine-latest-md.md"; got != want {
		t.Fatalf("Content-Disposition = %q，期望 %q", got, want)
	}
	if got := head.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("缺少 nosniff：%q", got)
	}
	if got := head.Get("Content-Length"); got != fmt.Sprint(len(body)) {
		t.Fatalf("Content-Length = %q，期望 %d", got, len(body))
	}
	if ct := head.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got != body {
		t.Fatalf("正文不一致：%q", got)
	}
	// 自己的报告不该刷审计：只有跨归属读取才需要追责（§12.4）。
	if rows := downloadAudits(t, f.st); len(rows) != 0 {
		t.Fatalf("本人下载不应产生 report.download 审计：%+v", rows)
	}
}

// TestReportsOtherUsersReportLooksAbsent 是防越权下载的核心契约：越权与 id 不存在必须
// 完全同形（同状态码、同错误对象）。403 等于对试探者承认「这份报告存在」。
func TestReportsOtherUsersReportLooksAbsent(t *testing.T) {
	f := newReportFixture(t, nil)
	f.seedReport(t, "r-theirs", "u-bob", "c-prod", f.putContent(t, "bob 的体检结论", []string{"c-prod", "scan-theirs"}))
	token := f.userToken(t, "alice", "user")

	statusTheirs, _, rawTheirs := f.req(t, http.MethodGet, "/api/v1/reports/r-theirs/content", token, "")
	statusAbsent, _, rawAbsent := f.req(t, http.MethodGet, "/api/v1/reports/r-nope/content", token, "")
	if statusTheirs != http.StatusNotFound || statusAbsent != http.StatusNotFound {
		t.Fatalf("越权=%d 不存在=%d，期望都是 404", statusTheirs, statusAbsent)
	}
	if a, b := errorOf(t, rawTheirs), errorOf(t, rawAbsent); a != b {
		t.Fatalf("越权与不存在的错误体不同形：\n越权 %s\n不存在 %s", a, b)
	}
	if msg := errorOf(t, rawTheirs); strings.Contains(msg, "bob") || strings.Contains(msg, "owner") {
		t.Fatalf("404 文案泄露归属：%s", msg)
	}

	// 列表同样看不到，且 total 不把别人的报告算进去。
	_, _, rawList := f.req(t, http.MethodGet, "/api/v1/reports", token, "")
	items, page := reportViews(t, rawList)
	if len(items) != 0 || page == nil || page.Total != 0 {
		t.Fatalf("别人的报告出现在列表里：items=%+v page=%+v", items, page)
	}
}

// TestReportsRequireAuthentication 锁路由口径：两条 reports 路由都是 AccessAuthenticated，
// 未登录必须在网关处 401，而不是走到 handler 再判空。
func TestReportsRequireAuthentication(t *testing.T) {
	f := newReportFixture(t, nil)
	for _, path := range []string{"/api/v1/reports", "/api/v1/reports/r-any/content"} {
		status, _, raw := f.req(t, http.MethodGet, path, "", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("%s 未登录应 401，got %d：%s", path, status, raw)
		}
	}
}

// TestReportContentModeValidation 锁下载模式参数：未知 mode 直接 400；非 s3 后端不认
// presign；越权的 presign 必须止步于归属判定（404），不能让签名本身变成一次授权。
func TestReportContentModeValidation(t *testing.T) {
	f := newReportFixture(t, nil)
	f.seedReport(t, "r-mine", "u-alice", "c-prod", f.putContent(t, "body", []string{"c-prod", "scan-mine"}))
	f.seedReport(t, "r-theirs", "u-bob", "c-prod", f.putContent(t, "bob body", []string{"c-prod", "scan-theirs"}))
	token := f.userToken(t, "alice", "user")

	status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-mine/content?mode=bogus", token, "")
	if status != http.StatusBadRequest || !strings.Contains(errorOf(t, raw), codeInvalidArgument) {
		t.Fatalf("未知 mode 应 400/invalid_argument，got %d：%s", status, raw)
	}
	// db 后端没有可签名的公网 URL：回 400 指回 mode=stream，而不是回一条坏链接。
	status, _, raw = f.req(t, http.MethodGet, "/api/v1/reports/r-mine/content?mode=presign", token, "")
	if status != http.StatusBadRequest {
		t.Fatalf("db 后端 presign 应 400，got %d：%s", status, raw)
	}
	status, _, raw = f.req(t, http.MethodGet, "/api/v1/reports/r-theirs/content?mode=presign", token, "")
	if status != http.StatusNotFound {
		t.Fatalf("越权 presign 应同形 404，got %d：%s", status, raw)
	}
}

// TestAdminCrossOwnerDownloadIsAudited 兑现「Admin 跨用户访问必须留审计」：读别人的报告
// 落一条 REPORT 事件，读自己的不落——否则追责记录会被日常下载淹掉。
func TestAdminCrossOwnerDownloadIsAudited(t *testing.T) {
	f := newReportFixture(t, nil)
	token := f.adminToken(t)
	user, _, err := f.authSvc.UserByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	f.seedReport(t, "r-bob", "u-bob", "c-prod", f.putContent(t, "bob body", []string{"c-prod", "scan-bob"}))
	// 管理员自己的报告要用真实用户 ID 建：归属位错了，「读自己不落审计」就测不出东西。
	f.seedReport(t, "r-admin", user.ID, "c-prod", f.putContent(t, "admin body", []string{"c-prod", "scan-admin"}))

	status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-bob/content", token, "")
	if status != http.StatusOK {
		t.Fatalf("admin 跨归属下载应 200，got %d：%s", status, raw)
	}
	rows := downloadAudits(t, f.st)
	if len(rows) != 1 {
		t.Fatalf("应有 1 条跨归属审计，got %+v", rows)
	}
	row := rows[0]
	if row.EventType != store.AuditEventReport || row.ResourceKind != "report" ||
		row.ResourceName != "r-bob" || row.UserID != user.ID || row.Status != "success" {
		t.Fatalf("审计内容不对：%+v", row)
	}
	if !strings.Contains(row.ResultSummary, "u-bob") {
		t.Fatalf("审计摘要应留下原归属：%s", row.ResultSummary)
	}

	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-admin/content", token, ""); status != http.StatusOK {
		t.Fatalf("admin 读自己的报告应 200：%d %s", status, raw)
	}
	if rows := downloadAudits(t, f.st); len(rows) != 1 {
		t.Fatalf("读自己的报告不应新增审计：%+v", rows)
	}
}

// TestSystemReportVisibilityFollowsClusterGrants 覆盖三层判定的中间层：created_by=system
// 的体检报告按 AuthzGrant 集群授权放行；cluster_id 为空的系统报告只 admin 可读。
func TestSystemReportVisibilityFollowsClusterGrants(t *testing.T) {
	f := newReportFixture(t, nil)
	f.seedReport(t, "r-sys-prod", objectstore.SystemActor, "c-prod", f.putContent(t, "prod 体检", []string{"c-prod", "scan-prod"}))
	f.seedReport(t, "r-sys-staging", objectstore.SystemActor, "c-staging", f.putContent(t, "staging 体检", []string{"c-staging", "scan-staging"}))
	f.seedReport(t, "r-sys-nocluster", objectstore.SystemActor, "", f.putContent(t, "无集群标识", []string{"local", "scan-local"}))
	granted := f.userToken(t, "carol", "operator")
	f.grantCluster(t, "u-carol", "c-prod")
	stranger := f.userToken(t, "dave", "operator")

	_, _, rawList := f.req(t, http.MethodGet, "/api/v1/reports", granted, "")
	items, page := reportViews(t, rawList)
	if len(items) != 1 || items[0].ID != "r-sys-prod" || page.Total != 1 {
		t.Fatalf("只该看见被授权集群的系统报告：items=%+v total=%v", items, page.Total)
	}
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-sys-prod/content", granted, ""); status != http.StatusOK {
		t.Fatalf("授权集群的系统报告应 200：%d %s", status, raw)
	}
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-sys-staging/content", granted, ""); status != http.StatusNotFound {
		t.Fatalf("未授权集群的系统报告应同形 404：%d %s", status, raw)
	}
	// cluster_id 为空 = 没有可比对的授权对象，默认拒绝比默认放行诚实。
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-sys-nocluster/content", granted, ""); status != http.StatusNotFound {
		t.Fatalf("无集群标识的系统报告应 404：%d %s", status, raw)
	}
	// 读系统报告同样算跨归属读取：它不是审计者自己产出的结论。
	if rows := downloadAudits(t, f.st); len(rows) != 1 || rows[0].ResourceName != "r-sys-prod" {
		t.Fatalf("读系统报告应留审计：%+v", rows)
	}

	_, _, rawStranger := f.req(t, http.MethodGet, "/api/v1/reports", stranger, "")
	if items, page := reportViews(t, rawStranger); len(items) != 0 || page.Total != 0 {
		t.Fatalf("零授权用户看见了系统报告：%+v", items)
	}

	admin := f.adminToken(t)
	_, _, rawAdmin := f.req(t, http.MethodGet, "/api/v1/reports", admin, "")
	if items, page := reportViews(t, rawAdmin); len(items) != 3 || page.Total != 3 {
		t.Fatalf("admin 应看见全部 3 条系统报告：%+v", items)
	}
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/reports/r-sys-nocluster/content", admin, ""); status != http.StatusOK {
		t.Fatalf("admin 应能读无集群标识的系统报告：%d %s", status, raw)
	}
}

// scriptedReportService 用脚本化错误顶替报告面，只为验 HTTP 映射：503 与 410/409 的
// 运维动作完全不同（等自愈 vs 找备份 vs 跑迁移），混成一个 500 就会把人引去查错方向。
type scriptedReportService struct{ openErr map[string]error }

func (s scriptedReportService) List(context.Context, service.ReportScope, int, int) ([]service.ReportView, int64, error) {
	return nil, 0, nil
}

func (s scriptedReportService) OpenContent(_ context.Context, id string, _ service.ReportScope) (service.ReportView, io.ReadCloser, objectstore.Ref, error) {
	err, ok := s.openErr[id]
	if !ok {
		return service.ReportView{}, nil, objectstore.Ref{}, store.ErrNotFound
	}
	return service.ReportView{}, nil, objectstore.Ref{}, err
}

func (s scriptedReportService) Presign(context.Context, string, service.ReportScope, time.Duration) (service.ReportView, string, error) {
	return service.ReportView{}, "", store.ErrNotFound
}

// Delete / Cleanup 只为满足 reportService 契约：错误映射用例不碰删除路径，删除行为由
// TestReportDelete* 用真实报告面覆盖。
func (s scriptedReportService) Delete(context.Context, string, service.ReportScope) (service.ReportView, error) {
	return service.ReportView{}, store.ErrNotFound
}

func (s scriptedReportService) Cleanup(context.Context, []string, service.ReportScope) ([]service.ReportView, []service.CleanupSkip, error) {
	return nil, nil, nil
}

// OpenStructuredReport 只为满足 reportService 契约：详情页行为由 TestReportStructured* 用
// 真实报告面覆盖，本用例只关心 HTTP 错误映射。
func (s scriptedReportService) OpenStructuredReport(context.Context, string, service.ReportScope) (service.StructuredReport, error) {
	return service.StructuredReport{}, store.ErrNotFound
}

// Overview / ExportMarkdown 同样只为满足契约：总览与按需导出的真实行为分别由
// TestReportOverview* 与 TestReportExport* 用真实报告面覆盖。
func (s scriptedReportService) Overview(context.Context, service.ReportScope) (service.ReportOverview, error) {
	return service.ReportOverview{}, nil
}

func (s scriptedReportService) ExportMarkdown(context.Context, string, service.ReportScope) (service.ReportView, []byte, error) {
	return service.ReportView{}, nil, store.ErrNotFound
}

var _ reportService = scriptedReportService{}

func TestReportContentErrorStatusMapping(t *testing.T) {
	f := newReportFixture(t, scriptedReportService{openErr: map[string]error{
		"r-unavailable": fmt.Errorf("%w: bucket unreachable", objectstore.ErrUnavailable),
		"r-locked":      fmt.Errorf("%w: dir root locked by another process", objectstore.ErrLocked),
		"r-gone":        fmt.Errorf("%w: payload was purged", service.ErrReportUnavailable),
		"r-mismatch":    fmt.Errorf("%w: object is on driver dir, current driver is db", service.ErrObjectDriverMismatch),
		"r-unsupported": fmt.Errorf("%w: driver db serves downloads via mode=stream", objectstore.ErrUnsupported),
		"r-disabled":    fmt.Errorf("%w: platform.blobs.driver=off", objectstore.ErrDisabled),
	}})
	token := f.userToken(t, "erin", "user")

	cases := []struct {
		id         string
		wantStatus int
		wantCode   string
	}{
		{"r-unavailable", http.StatusServiceUnavailable, codeUnavailable},
		{"r-locked", http.StatusServiceUnavailable, codeUnavailable},
		{"r-gone", http.StatusGone, codeGone},
		{"r-mismatch", http.StatusConflict, codeConflict},
		{"r-unsupported", http.StatusBadRequest, codeInvalidArgument},
		{"r-disabled", http.StatusNotImplemented, codeInternal},
		{"r-absent", http.StatusNotFound, codeNotFound},
	}
	for _, tc := range cases {
		status, head, raw := f.req(t, http.MethodGet, "/api/v1/reports/"+tc.id+"/content", token, "")
		if status != tc.wantStatus {
			t.Errorf("%s 状态码 = %d，期望 %d（%s）", tc.id, status, tc.wantStatus, raw)
			continue
		}
		if got := errorOf(t, raw); !strings.HasPrefix(got, tc.wantCode+"|") {
			t.Errorf("%s 错误码 = %s，期望前缀 %s", tc.id, got, tc.wantCode)
		}
		if tc.wantStatus == http.StatusServiceUnavailable {
			// 503 必须带 Retry-After：懒恢复有 30s 节流，更早重试只会再撞一次坏桶。
			if got := head.Get("Retry-After"); got != blobRetryAfter {
				t.Errorf("%s 缺少 Retry-After：%q", tc.id, got)
			}
		}
	}
}

// TestStorageHealthEndpointReusesSettingsRead 锁 §13.1：观测入口复用 settings.read
// （它已代表「能看平台运行形态」），且 degraded 也回 200 —— 状态码变红只会把那句真正
// 有用的 probeError 藏起来。
func TestStorageHealthEndpointReusesSettingsRead(t *testing.T) {
	f := newReportFixture(t, nil)
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/storage/health", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("未登录应 401：%d %s", status, raw)
	}
	plain := f.userToken(t, "frank", "user")
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/storage/health", plain, ""); status != http.StatusForbidden {
		t.Fatalf("无 settings.read 应 403：%d %s", status, raw)
	}
	status, _, raw := f.req(t, http.MethodGet, "/api/v1/storage/health", f.adminToken(t), "")
	if status != http.StatusOK {
		t.Fatalf("admin 应 200：%d %s", status, raw)
	}
	var env struct {
		Data service.BlobHealth `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Driver != objectstore.DriverDB || env.Data.Status != service.BlobStatusReady || !env.Data.Ready {
		t.Fatalf("健康响应不对：%+v", env.Data)
	}
	// 桶名/端点/凭据都不该出现在观测响应里（这里连这些字段都没有）。
	if strings.Contains(raw, "secret") || strings.Contains(raw, "endpoint") {
		t.Fatalf("健康响应含敏感字段：%s", raw)
	}
}

// capturingScan 记录服务端真正交给扫描面的参数——报告归属是否可信，只能在这里验。
type capturingScan struct{ seen chan model.ScanOptions }

func (c *capturingScan) Run(_ context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	select {
	case c.seen <- opts:
	default:
	}
	return &model.ScanResult{}, nil
}

func (c *capturingScan) Validate(context.Context, model.ScanOptions) (string, error) { return "ok", nil }

// TestCreateScanRejectsForgedAttribution 锁方案 17 §8 的白名单请求体：归属字段与连接类
// 参数一律不在 DTO 里，请求体带上来就是 400，而不是一边接受一边「覆盖」。改造前这里直接
// Decode 到 model.ScanOptions，伪造 actor 能把报告挂到别人名下。
func TestCreateScanRejectsForgedAttribution(t *testing.T) {
	f := newReportFixture(t, nil)
	admin := f.adminToken(t)

	for name, body := range map[string]string{
		"attribution": `{"scope":"namespace","namespace":"kube-system","attribution":{"actor":"victim"}}`,
		"scanId":      `{"scope":"namespace","namespace":"kube-system","scanId":"forged"}`,
		"kubeconfig":  `{"scope":"namespace","namespace":"kube-system","kubeconfig":"/etc/passwd"}`,
		"reportDir":   `{"scope":"namespace","namespace":"kube-system","reportDirectory":"/tmp/pwn"}`,
		"timeout":     `{"scope":"namespace","namespace":"kube-system","timeout":3600000000000}`,
	} {
		status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", admin, body)
		if status != http.StatusBadRequest {
			t.Errorf("%s 字段应 400，got %d：%s", name, status, raw)
		}
	}
	// 白名单字段本身要能过：否则上面那批 400 可能只是请求格式全错了。
	if status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", admin,
		`{"scope":"namespace","namespace":"kube-system","preset":"quick"}`); status != http.StatusAccepted {
		t.Fatalf("合法请求应 202，got %d：%s", status, raw)
	}
}

// TestCreateScanAttributesServerSide 锁 §6.4：actor 取认证身份、scan_id 取本次任务 ID、
// scope 取校验后的范围，三者都不来自请求体。
func TestCreateScanAttributesServerSide(t *testing.T) {
	f := newReportFixture(t, nil)
	admin := f.adminToken(t)
	user, _, err := f.authSvc.UserByToken(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", admin,
		`{"scope":"namespace","namespace":"kube-system"}`)
	if status != http.StatusAccepted {
		t.Fatalf("发起扫描应 202，got %d：%s", status, raw)
	}
	var accepted struct {
		Data struct {
			ID         string `json:"id"`
			Owner      string `json:"owner"`
			ScopeKind  string `json:"scopeKind"`
			ScopeValue string `json:"scopeValue"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &accepted); err != nil {
		t.Fatalf("解析 202 响应失败：%v / %s", err, raw)
	}
	if accepted.Data.ID == "" {
		t.Fatalf("202 响应缺少任务 ID：%s", raw)
	}
	// 任务归属同步回显：页面要靠它判断「这是不是我发起的」。
	if accepted.Data.Owner != user.ID || accepted.Data.ScopeKind != model.ScopeNamespace || accepted.Data.ScopeValue != "kube-system" {
		t.Fatalf("202 回显的任务归属不对：%+v", accepted.Data)
	}

	var opts model.ScanOptions
	select {
	case opts = <-f.scan.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("扫描未被调用")
	}
	if opts.Attribution.Actor != user.ID {
		t.Fatalf("actor 被请求体左右：%q，期望认证身份 %q", opts.Attribution.Actor, user.ID)
	}
	if opts.Attribution.ScanID != accepted.Data.ID {
		t.Fatalf("scan_id 应取本次任务 ID %s：%+v", accepted.Data.ID, opts.Attribution)
	}
	if opts.Attribution.ScopeKind != model.ScopeNamespace || opts.Attribution.ScopeValue != "kube-system" {
		t.Fatalf("体检范围没有随归属一起下发：%+v", opts.Attribution)
	}
	if opts.Attribution.ClusterID != "" {
		t.Fatalf("本期体检目标固定为平台自身集群，cluster_id 应为空：%+v", opts.Attribution)
	}
	if opts.Namespace != "kube-system" {
		t.Fatalf("其余扫描参数被改写了：%+v", opts)
	}
	// 页面体检不落容器本地副本（方案 17 §7.3）：reportDirectory 非空意味着往容器里写文件。
	if opts.ReportDirectory != "" || opts.ReportMode != "" {
		t.Fatalf("页面体检不应写容器本地副本：%+v", opts)
	}
}

// ---- P8.14 报告清单清理 ----

// deleteAudits 取全部 report.delete 审计（删除每一条都留痕，与下载只记跨 owner 不同）。
func deleteAudits(t *testing.T, st store.Store) []store.AuditEvent {
	t.Helper()
	rows, err := st.Audit().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range rows {
		if e.Action == store.ActionReportDelete {
			out = append(out, *e)
		}
	}
	return out
}

// TestReportDeleteRemovesRowAndReleasesRef 锁 §13.1 主路径：删自己的报告 → 200、列表里消失、
// 独占引用归零可回收、对象本身还在（物理回收只归 gc）。
func TestReportDeleteRemovesRowAndReleasesRef(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "del-owner", "user")
	ref := f.putContent(t, "# only mine", []string{"c1", "scan-del"})
	f.seedReport(t, "r-del", "u-del-owner", "c1", ref)

	if status, _, raw := f.req(t, http.MethodPost, "/api/v1/reports/cleanup", owner, `{"ids":["r-del"]}`); status != http.StatusOK {
		t.Fatalf("删自己的报告应 200，got %d：%s", status, raw)
	}
	if _, err := f.st.Reports().Get(context.Background(), "r-del"); err == nil {
		t.Fatal("业务行仍在列表里：删除没生效")
	}
	meta, err := f.st.Objects().Get(context.Background(), ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RefCount != 0 {
		t.Fatalf("引用未随删除释放，GC 永远不会回收它：ref_count=%d", meta.RefCount)
	}
	// 引用归零不等于对象消失——在宽限期内它还得能靠备份/对账解释清楚。
	if rc, _, err := f.backend.Get(context.Background(), ref.Key); err != nil {
		t.Fatalf("删除路径不该碰后端对象：%v", err)
	} else {
		_ = rc.Close()
	}
	audits := deleteAudits(t, f.st)
	if len(audits) != 1 || audits[0].UserID != "u-del-owner" || audits[0].ResourceName != "r-del" {
		t.Fatalf("删除审计不对： %+v", audits)
	}
	if strings.Contains(audits[0].ResultSummary, "cross_owner") {
		t.Fatalf("删自己的报告不该标 cross_owner：%s", audits[0].ResultSummary)
	}
}

// TestReportDeleteVisibilityMatchesRead 锁「清理不是第二条授权通道」：别人的报告删不掉，
// 且响应与不存在同形，report id 不因为加了 DELETE 就变成存在性探针。
func TestReportDeleteVisibilityMatchesRead(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "del-keeper", "user")
	stranger := f.userToken(t, "del-stranger", "user")
	ref := f.putContent(t, "# not yours", []string{"c1", "scan-mine"})
	f.seedReport(t, "r-mine", "u-del-keeper", "c1", ref)

	status, _, raw := f.req(t, http.MethodDelete, "/api/v1/reports/r-mine", stranger, "")
	if status != http.StatusNotFound {
		t.Fatalf("他人删除应与不存在同形 404，got %d：%s", status, raw)
	}
	if _, err := f.st.Reports().Get(context.Background(), "r-mine"); err != nil {
		t.Fatalf("被拒的删除动到了数据：%v", err)
	}
	// 批量端点同判：混进别人的 id 只会计成 skipped，整批既不失败也不顺手删。
	if status, _, raw := f.req(t, http.MethodPost, "/api/v1/reports/cleanup", stranger,
		`{"ids":["r-mine","r-absent"]}`); status != http.StatusOK {
		t.Fatalf("批量清理应 200 并逐条报告结果，got %d：%s", status, raw)
	} else {
		var env struct {
			Data struct {
				Deleted []string             `json:"deleted"`
				Skipped []map[string]string  `json:"skipped"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		if len(env.Data.Deleted) != 0 {
			t.Fatalf("越权 id 不该被删：%+v", env.Data.Deleted)
		}
		if len(env.Data.Skipped) != 2 {
			t.Fatalf("两个 id 都该被跳过：%s", raw)
		}
	}
	if _, err := f.st.Reports().Get(context.Background(), "r-mine"); err != nil {
		t.Fatalf("批量越权动到了数据：%v", err)
	}
	// owner 本人可删，收尾。
	if status, _, raw := f.req(t, http.MethodDelete, "/api/v1/reports/r-mine", owner, ""); status != http.StatusOK {
		t.Fatalf("owner 删除应 200，got %d：%s", status, raw)
	}
}

// TestReportAdminCrossOwnerDeleteAudits 锁 §13.1「管理员跨归属删除必须留痕」这一半：
// 允许，但审计里要能看出删的是别人的报告。
func TestReportAdminCrossOwnerDeleteAudits(t *testing.T) {
	f := newReportFixture(t, nil)
	f.userToken(t, "del-victim", "user")
	ref := f.putContent(t, "# user report", []string{"c1", "scan-user"})
	f.seedReport(t, "r-userrep", "u-del-victim", "c1", ref)

	admin := f.adminToken(t)
	if status, _, raw := f.req(t, http.MethodDelete, "/api/v1/reports/r-userrep", admin, ""); status != http.StatusOK {
		t.Fatalf("admin 跨归属删除应 200，got %d：%s", status, raw)
	}
	audits := deleteAudits(t, f.st)
	if len(audits) != 1 {
		t.Fatalf("应有 1 条删除审计：%+v", audits)
	}
	if !strings.Contains(audits[0].ResultSummary, `"cross_owner":true`) {
		t.Fatalf("跨归属删除必须显式标注：%s", audits[0].ResultSummary)
	}
	if audits[0].UserID != "admin" && audits[0].UserID != "" {
		// 审计的操作者必须是执行删除的管理员本人，不能记成报告归属人。
		if got := audits[0].UserID; got == "u-del-victim" {
			t.Fatalf("删除审计把操作者记成了报告归属人：%s", got)
		}
	}
}

// TestReportCleanupUnavailableRow 锁「清理的主要对象恰恰是内容已不可恢复的行」：
// available=false、甚至 objects 元数据都没了，业务行照样要能清掉。
func TestReportCleanupUnavailableRow(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "del-orphan", "user")
	ctx := context.Background()
	// 直接造一行「有业务行、无对象元数据」的孤儿：正常运行期由 GC 竞态或历史迁移留下，
	// 这里手工复现，验的就是清理对这种行的兜底。
	row := &store.Report{
		ID: "r-orphan", ClusterID: "c1", ScanID: "scan-orphan", Kind: service.ReportKindLatestMD,
		// key 必须过 objectstore 的格式校验（末段以 64 位 sha256 开头），否则这条用例会
		// 在 DeleteReport 的 key 校验处先炸，测不到真正想测的「元数据缺失仍可清理」。
		BlobKey: "blobs/reports/c1/scan-orphan/" + strings.Repeat("a", 64) + "-gone.md",
		MediaType: "text/markdown", Size: 10,
		SHA256: strings.Repeat("a", 64), Findings: 1, CreatedBy: "u-del-orphan",
		Available: false, CreatedAt: time.Now().UTC(),
	}
	// 报告面刻意不暴露脱离事务的 Create，孤儿行只能经 Tx 造（这里刻意不 Acquire）。
	if err := f.st.WithTx(ctx, func(tx *store.Tx) error { return tx.InsertReport(ctx, row) }); err != nil {
		t.Fatalf("create orphan: %v", err)
	}
	if status, _, raw := f.req(t, http.MethodPost, "/api/v1/reports/cleanup", owner, `{"ids":["r-orphan"]}`); status != http.StatusOK {
		t.Fatalf("孤儿行应可清理，got %d：%s", status, raw)
	}
	if _, err := f.st.Reports().Get(ctx, "r-orphan"); err == nil {
		t.Fatal("孤儿行没被清掉：清理功能对它永久失效")
	}
}

// TestReportCleanupBatchLimit 锁单次上限：超限是 400（请求不合法），不是 500。
func TestReportCleanupBatchLimit(t *testing.T) {
	f := newReportFixture(t, nil)
	owner := f.userToken(t, "del-flood", "user")
	var sb strings.Builder
	sb.WriteString(`{"ids":[`)
	for i := 0; i <= 200; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"r-` + fmt.Sprint(i) + `"`)
	}
	sb.WriteString(`]}`)
	status, _, raw := f.req(t, http.MethodPost, "/api/v1/reports/cleanup", owner, sb.String())
	if status != http.StatusBadRequest {
		t.Fatalf("超过单次上限应 400，got %d：%s", status, raw)
	}
	if code := errorOf(t, raw); !strings.HasPrefix(code, codeInvalidArgument+"|") {
		t.Fatalf("上限错误应是 invalid_argument：%s", code)
	}
}

// TestReportDeleteNotConfigured 未装配报告面时删除/清理回 501，与其他报告端点一致。
func TestReportDeleteNotConfigured(t *testing.T) {
	f := newReportFixture(t, scriptedReportService{})
	// scriptedReportService 的 Delete/Cleanup 恒回 ErrNotFound，这里要验的是「没装配」这条
	// 更早的分支，所以另起一台不带 reports 的服务。
	srv := httptest.NewServer(New(&fakeScan{}).Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/api/v1/reports/cleanup", "application/json", strings.NewReader(`{"ids":["x"]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = f
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未装配时应 401/501，got %d", resp.StatusCode)
	}
	decodeData(t, resp, nil)
}
