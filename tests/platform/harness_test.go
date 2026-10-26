// Package platform_test 是四期 P3.13「平台能力测试与红线回归」测试集。
//
// 与包内单测不同，这里严格按 internal/cli/server.go 的生产装配路径拉起真实组件：
// SQLite Store（文件库，便于直接扫描落库字节）+ 认证/角色 + 配置中心 +
// 集群管理 + Skill 平台 + Chat 会话，端到端验证 Cluster/Skill/Config/User/Session
// 五类平台能力，并验证 Secret / kubeconfig / API key 三条红线在「API 响应、
// 审计记录、落库字节」三处都不泄漏。
package platform_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

import (
	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/server"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/webui"
)

const (
	adminUsername = "admin"
	adminPassword = "platform-test-admin-pw"
	// encKey 是测试用的 32 字节凭据加密密钥（等价生产 K8S_AI_ENC_KEY）。
	encKey = "0123456789abcdef0123456789abcdef"
)

// harness 是一套按生产装配拉起的平台实例。
type harness struct {
	t       *testing.T
	dbPath  string
	st      store.Store
	authSvc *auth.Service
	ts      *httptest.Server
}

// newHarness 装配平台并启动 HTTP 测试服务。会话路由与 internal/cli/server.go
// 一致：/api/v1/sessions* 交给 webui，其余交给平台 API。
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "platform.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	authSvc := auth.New(st.Users(), st.Roles())
	if err := authSvc.BootstrapWithName(ctx, adminUsername, adminPassword); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	auditSvc := service.NewAuditService(st)
	cipher, err := cluster.NewCipher([]byte(encKey))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	clusterSvc := cluster.New(st.Clusters(), st.Credentials(), cipher,
		cluster.WithConnector(fakeConnector{}),
		cluster.WithAudit(auditSvc.Record))
	settingsSvc := settings.New(st, settings.WithCipher(cipher))
	skillPlat := service.NewSkillPlatform(st.Skills())
	if err := skillPlat.SeedBuiltins(ctx); err != nil {
		t.Fatalf("seed builtin skills: %v", err)
	}
	if err := skillPlat.Reload(ctx); err != nil {
		t.Fatalf("reload skills: %v", err)
	}
	authzSvc := authz.New(st)
	platformHandler := server.NewWithPlatform(fakeScan{}, authSvc, settingsSvc, auditSvc).
		WithClusters(clusterSvc).
		WithSkills(skillPlat).
		WithAuthz(authzSvc).
		Handler()
	chatAPI := webui.New(newStoreChat(st), "test-model").
		WithAudit(auditSvc.Record).
		WithScopeResolver(webui.AuthzScopeResolver(authzSvc))
	sessionHandler := chatAPI.AuthenticatedHandler(authSvc.UserByToken)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions" || strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") {
			sessionHandler.ServeHTTP(w, r)
			return
		}
		platformHandler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &harness{t: t, dbPath: dbPath, st: st, authSvc: authSvc, ts: ts}
}

// reply 是一次 HTTP 调用的结果：状态码 + 原始响应体。
type reply struct {
	status int
	body   string
}

// data 解析统一响应信封的 data 字段到 dst，并顺带校验信封元数据。
func (r reply) data(t *testing.T, dst any) {
	t.Helper()
	var env struct {
		RequestID string          `json:"request_id"`
		Timestamp time.Time       `json:"timestamp"`
		Data      json.RawMessage `json:"data"`
		Error     *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &env); err != nil {
		t.Fatalf("解析响应信封失败: %v\n%s", err, r.body)
	}
	if env.RequestID == "" || env.Timestamp.IsZero() {
		t.Fatalf("响应缺少 request_id/timestamp: %s", r.body)
	}
	if dst == nil {
		return
	}
	if len(env.Data) == 0 {
		t.Fatalf("响应缺少 data（error=%+v）: %s", env.Error, r.body)
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		t.Fatalf("解析 data 失败: %v\n%s", err, r.body)
	}
}

// do 发起一次带 Bearer Token 的 HTTP 请求；token 为空表示匿名。
func (h *harness) do(method, path, token, body string) reply {
	h.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rdr)
	if err != nil {
		h.t.Fatalf("构造请求 %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("请求 %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("读取响应 %s %s: %v", method, path, err)
	}
	return reply{status: resp.StatusCode, body: string(raw)}
}

// plain 直接解析响应体：/api/v1/sessions* 由 webui 承载，用的是自身精简 JSON
// 契约（无 request_id 信封），与平台 API 的统一信封不同。
func (r reply) plain(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), dst); err != nil {
		t.Fatalf("解析响应体失败: %v\n%s", err, r.body)
	}
}

// must 断言状态码符合预期，返回同一个 reply 便于链式取 data。
func (h *harness) must(r reply, want int) reply {
	h.t.Helper()
	if r.status != want {
		h.t.Fatalf("状态码 = %d, want %d；响应体: %s", r.status, want, r.body)
	}
	return r
}

// login 走真实 POST /api/v1/auth/login 换取会话 Token。
func (h *harness) login(username, password string) string {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	var data struct {
		Token string `json:"token"`
	}
	h.must(h.do(http.MethodPost, "/api/v1/auth/login", "", string(body)), http.StatusOK).data(h.t, &data)
	if data.Token == "" {
		h.t.Fatalf("登录未返回 token: %s", username)
	}
	return data.Token
}

// adminToken 返回超级管理员 Token。
func (h *harness) adminToken() string {
	h.t.Helper()
	return h.login(adminUsername, adminPassword)
}

// roleIDByName 按角色名查内置角色 ID（admin/user/operator）。
func (h *harness) roleIDByName(token, name string) string {
	h.t.Helper()
	var roles []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	h.must(h.do(http.MethodGet, "/api/v1/roles", token, ""), http.StatusOK).data(h.t, &roles)
	for _, r := range roles {
		if r.Name == name {
			return r.ID
		}
	}
	h.t.Fatalf("内置角色 %q 不存在", name)
	return ""
}

// createUser 以管理员身份创建用户，返回其用户 ID。
func (h *harness) createUser(adminToken, username, password, role string) string {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username":    username,
		"displayName": username,
		"password":    password,
		"roleId":      h.roleIDByName(adminToken, role),
	})
	var created struct {
		ID string `json:"id"`
	}
	h.must(h.do(http.MethodPost, "/api/v1/users", adminToken, string(body)), http.StatusCreated).data(h.t, &created)
	if created.ID == "" {
		h.t.Fatalf("创建用户未返回 id: %s", username)
	}
	return created.ID
}

// userHarness 是「一个平台实例 + admin/owner/outsider 三个真实登录身份」的常用组合。
type userHarness struct {
	*harness
	adminToken    string
	ownerID       string
	ownerToken    string
	outsiderID    string
	outsiderToken string
}

// newUserHarness 预置 admin、user 角色的 owner 与 outsider 两个账号。
func newUserHarness(t *testing.T) *userHarness {
	t.Helper()
	h := newHarness(t)
	admin := h.adminToken()
	ownerID := h.createUser(admin, "owner1", "owner-password-123", "user")
	outsiderID := h.createUser(admin, "outsider1", "outsider-password-1", "user")
	return &userHarness{
		harness:       h,
		adminToken:    admin,
		ownerID:       ownerID,
		ownerToken:    h.login("owner1", "owner-password-123"),
		outsiderID:    outsiderID,
		outsiderToken: h.login("outsider1", "outsider-password-1"),
	}
}

// fakeScan 是 service.ScanService 桩：本测试集不驱动扫描。
type fakeScan struct{}

func (fakeScan) Run(context.Context, model.ScanOptions) (*model.ScanResult, error) {
	return &model.ScanResult{}, nil
}

func (fakeScan) Validate(context.Context, model.ScanOptions) (string, error) {
	return "v1.28.13", nil
}

// fakeConnector 是 cluster.Connector 桩：避免连真实集群。
type fakeConnector struct{}

func (fakeConnector) Inspect(context.Context, []byte) (*cluster.Info, error) {
	return &cluster.Info{
		Name:    "platform-test",
		Context: "platform-test-context",
		Server:  "https://kube.example.internal:6443",
		Version: "v1.28.13",
		Health:  "healthy",
	}, nil
}

// storeChat 是 webui chatService 桩：会话读写全部走真实 SessionStore，
// 因此落库脱敏、owner 归属这些边界与生产同路径。Chat 本体不做 LLM 调用，
// 只把用户输入落库并回一条收口事件。
type storeChat struct {
	sessions store.SessionStore
}

func newStoreChat(st store.Store) *storeChat { return &storeChat{sessions: st.Sessions()} }

func (c *storeChat) Chat(ctx context.Context, req service.ChatRequest, respond <-chan string) (<-chan service.ChatEvent, error) {
	sess, err := c.sessions.Get(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	sess.AddMessage(session.Message{Role: "user", Type: "text", Content: req.Query})
	if err := c.sessions.Update(ctx, sess); err != nil {
		return nil, err
	}
	out := make(chan service.ChatEvent, 1)
	out <- service.ChatEvent{Type: service.ChatEventAnswer, Text: "ok"}
	close(out)
	return out, nil
}

// lookupSession 读会话并把 Store 的未找到错误翻译为会话域错误：webui 的归属
// 判定依赖 session.ErrNotFound 才走「按创建者新建」分支，否则会把 404 变成 500。
func (c *storeChat) lookupSession(id string) (*session.Session, error) {
	if id == "" {
		return nil, session.ErrNotFound
	}
	sess, err := c.sessions.Get(context.Background(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, session.ErrNotFound
	}
	return sess, err
}

func (c *storeChat) GetSession(id string) (*session.Session, error) {
	return c.lookupSession(id)
}

func (c *storeChat) GetOrCreateSessionForUser(id, userID, namespace, model string) (*session.Session, error) {
	ctx := context.Background()
	if id != "" {
		sess, err := c.lookupSession(id)
		if err == nil {
			return sess, nil
		}
		if !errors.Is(err, session.ErrNotFound) {
			return nil, err
		}
	}
	newID := id
	if newID == "" {
		newID = "s-" + userID + "-" + time.Now().UTC().Format("150405.000000")
	}
	sess := &session.Session{ID: newID, UserID: userID, Namespace: namespace, Model: model, Name: "新会话"}
	if err := c.sessions.Create(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (c *storeChat) ListSessions() ([]session.Session, error) {
	list, err := c.sessions.List(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]session.Session, 0, len(list))
	for _, s := range list {
		out = append(out, *s)
	}
	return out, nil
}

func (c *storeChat) ListSessionsForUser(userID string) ([]session.Session, error) {
	list, err := c.ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]session.Session, 0, len(list))
	for _, s := range list {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *storeChat) DeleteSession(id string) error {
	return c.sessions.Delete(context.Background(), id)
}

func (c *storeChat) RenameSession(id, name string) error {
	sess, err := c.lookupSession(id)
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess.Name = name
	return c.sessions.Update(ctx, sess)
}

func (c *storeChat) PinSession(id string, pinned bool) error {
	sess, err := c.lookupSession(id)
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess.Pinned = pinned
	return c.sessions.Update(ctx, sess)
}

// dbBytes 读取 SQLite 主库与 WAL/SHM 边车文件的全部字节，用于红线落库扫描。
// Store 未开启 WAL（默认 rollback journal），提交后数据即在主库文件中。
func (h *harness) dbBytes() []byte {
	h.t.Helper()
	var all bytes.Buffer
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(h.dbPath + suffix)
		if err != nil {
			continue
		}
		all.Write(raw)
	}
	return all.Bytes()
}

// auditEvents 读取全部审计记录。
func (h *harness) auditEvents() []*store.AuditEvent {
	h.t.Helper()
	list, err := h.st.Audit().List(context.Background())
	if err != nil {
		h.t.Fatalf("读取审计失败: %v", err)
	}
	return list
}
