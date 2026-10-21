package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

type platformServer struct {
	ts        *httptest.Server
	store     store.Store
	authSvc   *auth.Service
	skillPlat *service.SkillPlatform
	authzSvc  *authz.Service
}

// newPlatformServer 装配带认证 + 配置中心 + 审计的服务（与 cli server 一致）。
func newPlatformServer(t *testing.T) *platformServer {
	t.Helper()
	st, err := store.OpenSQLite("file:server-settings-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	authSvc := auth.New(st.Users(), st.Roles())
	if err := authSvc.Bootstrap(context.Background(), "admin-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// P3.5：Skill 平台与 cli server 装配一致（内置种子 + 内存 registry 重建）。
	skillPlat := service.NewSkillPlatform(st.Skills())
	if err := skillPlat.SeedBuiltins(context.Background()); err != nil {
		t.Fatalf("seed skills: %v", err)
	}
	if err := skillPlat.Reload(context.Background()); err != nil {
		t.Fatalf("reload skills: %v", err)
	}
	authzSvc := authz.New(st)
	// P3.9：与 cli server 装配一致，配置中心与集群共用同一凭据加密器；
	// 未注入时对敏感键的写入会整批返回 501（ErrCipherRequired）。
	cipher, err := cluster.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	handler := NewWithPlatform(&fakeScan{}, authSvc, settings.New(st, settings.WithCipher(cipher)), service.NewAuditService(st)).WithSkills(skillPlat).WithAuthz(authzSvc).Handler()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &platformServer{ts: ts, store: st, authSvc: authSvc, skillPlat: skillPlat, authzSvc: authzSvc}
}

func (p *platformServer) tokenAs(t *testing.T, username, password string) string {
	t.Helper()
	resp, _, payload := login(t, p.ts, `{"username":"`+username+`","password":"`+password+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录 %s 失败: %d", username, resp.StatusCode)
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatal("登录响应缺少 token")
	}
	return token
}

func (p *platformServer) do(t *testing.T, method, path, token, body string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, p.ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(raw)
}

// settingsView 与 settings.GroupView 对齐（服务端只暴露脱敏后的视图）。
type settingsView struct {
	Group string `json:"group"`
	Items []struct {
		Key       string          `json:"key"`
		Value     json.RawMessage `json:"value"`
		HasValue  bool            `json:"hasValue"`
		Sensitive bool            `json:"sensitive"`
	} `json:"items"`
}

func (v settingsView) item(key string) (json.RawMessage, bool, bool) {
	for _, item := range v.Items {
		if item.Key == key {
			return item.Value, item.HasValue, item.Sensitive
		}
	}
	return nil, false, false
}

func TestSettingsRequiresAuth(t *testing.T) {
	p := newPlatformServer(t)
	resp, raw := p.do(t, http.MethodGet, "/api/v1/settings/model", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d: %s", resp.StatusCode, raw)
	}
}

func TestSettingsGetAndPatch(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	resp, raw := p.do(t, http.MethodGet, "/api/v1/settings/model", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 应 200, got %d: %s", resp.StatusCode, raw)
	}
	var before settingsView
	decodeData(t, mustReplay(t, raw), &before)
	if before.Group != "model" || len(before.Items) == 0 {
		t.Fatalf("分组视图不符: %+v", before)
	}

	resp, raw = p.do(t, http.MethodPatch, "/api/v1/settings/model", token, `{"max_tokens":2048}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH 应 200, got %d: %s", resp.StatusCode, raw)
	}
	var after settingsView
	decodeData(t, mustReplay(t, raw), &after)
	if value, _, _ := after.item("max_tokens"); string(value) != "2048" {
		t.Fatalf("PATCH 后应读到 2048, got %s", value)
	}
}

func TestSettingsErrors(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"未知分组", http.MethodGet, "/api/v1/settings/nope", "", http.StatusNotFound},
		{"内部记账分组不可访问", http.MethodGet, "/api/v1/settings/migration", "", http.StatusNotFound},
		{"未知键", http.MethodPatch, "/api/v1/settings/model", `{"nope":1}`, http.StatusBadRequest},
		{"取值越界", http.MethodPatch, "/api/v1/settings/model", `{"temperature":9}`, http.StatusBadRequest},
		{"空补丁", http.MethodPatch, "/api/v1/settings/model", `{}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp, raw := p.do(t, tc.method, tc.path, token, tc.body)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: 期望 %d, got %d: %s", tc.name, tc.want, resp.StatusCode, raw)
		}
	}
}

// TestSettingsViewerCanReadButNotWrite 验证权限判定落在 Permission 层：
// viewer 持 settings.read（读非敏感配置），不持 settings.write。
func TestSettingsViewerCanReadButNotWrite(t *testing.T) {
	p := newPlatformServer(t)
	hash, err := auth.HashPassword("viewer-pw")
	if err != nil {
		t.Fatal(err)
	}
	// P3.11a：默认播种移除 viewer，测试按需补建只读观察者角色。
	viewerRole := ensureRole(t, p.store, auth.RoleViewer, "只读观察者（测试补建）", permission.SettingsRead)
	err = p.store.Users().Create(context.Background(), &store.User{
		ID: "u-viewer", Username: "viewer1", PasswordHash: hash, RoleID: viewerRole,
		Status: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	token := p.tokenAs(t, "viewer1", "viewer-pw")

	if resp, raw := p.do(t, http.MethodGet, "/api/v1/settings/model", token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer 读取配置应 200, got %d: %s", resp.StatusCode, raw)
	}
	resp, raw := p.do(t, http.MethodPatch, "/api/v1/settings/model", token, `{"max_tokens":1}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer 改配置应 403, got %d: %s", resp.StatusCode, raw)
	}
	// 越权尝试不得产生任何副作用。
	if _, err := p.store.Settings().Get(context.Background(), "model", "max_tokens"); err == nil {
		t.Fatal("被拒绝的 PATCH 不得写入配置")
	}
}

// TestSettingsNeverEchoesSecrets 是红线用例：敏感键写入后，
// GET/PATCH 响应体都不得出现明文。
func TestSettingsNeverEchoesSecrets(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	const secret = "sk-live-9f2b7c"
	const kubeconfig = "/var/run/secrets/admin.kubeconfig"

	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/settings/model", token, `{"api_key":"`+secret+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH api_key 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/settings/ops", token, `{"kubeconfig":"`+kubeconfig+`"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH kubeconfig 应 200, got %d: %s", resp.StatusCode, raw)
	}
	for _, group := range []string{"model", "ops"} {
		resp, raw := p.do(t, http.MethodGet, "/api/v1/settings/"+group, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", group, resp.StatusCode)
		}
		for _, banned := range []string{secret, kubeconfig} {
			if strings.Contains(raw, banned) {
				t.Fatalf("GET %s 响应泄漏明文 %s", group, banned)
			}
		}
	}
	// CONFIG_CHANGE 审计同样不得带值，且要能定位到分组与操作者。
	events, err := p.store.Audit().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var changes int
	for _, ev := range events {
		if ev.EventType != store.AuditEventConfigChange {
			continue
		}
		changes++
		if strings.Contains(ev.ParameterSummary, secret) || strings.Contains(ev.ParameterSummary, kubeconfig) {
			t.Fatalf("审计摘要泄漏明文: %s", ev.ParameterSummary)
		}
		if ev.Operation != http.MethodPatch || ev.UserID == "" {
			t.Fatalf("CONFIG_CHANGE 归属不完整: %+v", ev)
		}
	}
	if changes != 2 {
		t.Fatalf("应写入 2 条 CONFIG_CHANGE, got %d", changes)
	}
}

// TestAuthEventsAreAudited 验证登录/登出落库（P1.11），且不记录凭据。
func TestAuthEventsAreAudited(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/auth/logout", token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("logout 应 200, got %d: %s", resp.StatusCode, raw)
	}

	events, err := p.store.Audit().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, ev := range events {
		seen[ev.EventType]++
		if strings.Contains(ev.ParameterSummary+ev.ResultSummary, "admin-pw") {
			t.Fatalf("认证审计不得包含口令: %s", ev.ResultSummary)
		}
	}
	if seen[store.AuditEventLogin] == 0 || seen[store.AuditEventLogout] == 0 {
		t.Fatalf("应同时存在 LOGIN 与 LOGOUT 事件, got %+v", seen)
	}
}

// mustReplay 把已读出的响应体重新包成 Response，复用信封校验。
func mustReplay(t *testing.T, raw string) *http.Response {
	t.Helper()
	return &http.Response{Body: io.NopCloser(strings.NewReader(raw))}
}
