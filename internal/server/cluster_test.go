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
	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// sentinelToken 是集群凭据红线哨兵值：任何响应体/审计都不得出现。
const sentinelToken = "SECRET-TOKEN-9f2b"

// fakeClusterConnector 模拟 kubeconfig 解析/连测（成功或失败），不触碰真实集群。
type fakeClusterConnector struct {
	info *cluster.Info
	err  error
}

func (f *fakeClusterConnector) Inspect(ctx context.Context, _ []byte) (*cluster.Info, error) {
	if f.err != nil {
		return nil, f.err
	}
	cp := *f.info
	return &cp, nil
}

// clusterEnv 是与 cli server 一致装配的集群管理服务端环境。
type clusterEnv struct {
	ts      *httptest.Server
	store   store.Store
	authSvc *auth.Service
	cipher  *cluster.Cipher
}

func newClusterServer(t *testing.T, conn cluster.Connector) *clusterEnv {
	t.Helper()
	st, err := store.OpenSQLite("file:server-cluster-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
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
	cipher, err := cluster.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	auditSvc := service.NewAuditService(st)
	svc := cluster.New(st.Clusters(), st.Credentials(), cipher,
		cluster.WithConnector(conn), cluster.WithAudit(auditSvc.Record))
	handler := NewWithPlatform(&fakeScan{}, authSvc, settings.New(st), auditSvc).
		WithClusters(svc).Handler()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &clusterEnv{ts: ts, store: st, authSvc: authSvc, cipher: cipher}
}

func (c *clusterEnv) do(t *testing.T, method, path, token, body string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, c.ts.URL+path, reader)
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

func (c *clusterEnv) adminToken(t *testing.T) string {
	resp, _, payload := login(t, c.ts, `{"username":"admin","password":"admin-pw"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin 登录失败: %d", resp.StatusCode)
	}
	token, _ := payload["token"].(string)
	if token == "" {
		t.Fatal("登录响应缺少 token")
	}
	return token
}

// kubeconfigBody 构造最小可解析 kubeconfig 请求体（fake Connector 不校验结构，
// 但 Content 必须有值以模拟"用户提交了配置"）。
func kubeconfigBody(name string) string {
	b, _ := json.Marshal(map[string]string{
		"name":       name,
		"kubeconfig": sentinelToken + "-" + name,
	})
	return string(b)
}

func TestClusterRequiresAuth(t *testing.T) {
	c := newClusterServer(t, &fakeClusterConnector{
		info: &cluster.Info{Name: "prod", Context: "prod", Server: "https://10.0.0.1:6443"},
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/clusters"},
		{http.MethodPost, "/api/v1/clusters/test"},
		{http.MethodGet, "/api/v1/clusters"},
		{http.MethodGet, "/api/v1/clusters/current"},
	} {
		resp, raw := c.do(t, tc.method, tc.path, "", kubeconfigBody("any"))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录应 401, got %d: %s", tc.method, tc.path, resp.StatusCode, raw)
		}
	}
}

// TestClusterAdminCreateNoEcho 验证 admin 可创建集群且 kubeconfig/哨兵 token 不进入响应体。
func TestClusterAdminCreateNoEcho(t *testing.T) {
	c := newClusterServer(t, &fakeClusterConnector{
		info: &cluster.Info{Name: "prod", Context: "prod", Server: "https://10.0.0.1:6443", Version: "v1.28.13", Health: "ok"},
	})
	token := c.adminToken(t)

	resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", token, kubeconfigBody("prod"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建集群应 201, got %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, sentinelToken) {
		t.Fatalf("创建响应泄漏哨兵 token: %s", raw)
	}
	var created store.Cluster
	env := decodeData(t, mustReplay(t, raw), &created)
	if env.Error != nil {
		t.Fatalf("成功响应不应携带 error: %+v", env.Error)
	}
	if created.ID == "" || created.Name != "prod" || created.Status != string(cluster.StatusConnected) {
		t.Fatalf("创建结果不符: %+v", created)
	}
	if created.Server != "https://10.0.0.1:6443" || created.Version != "v1.28.13" {
		t.Fatalf("元数据不符: %+v", created)
	}

	// 列表与详情同样不得出现哨兵值。
	for _, path := range []string{"/api/v1/clusters", "/api/v1/clusters/" + created.ID} {
		resp, raw := c.do(t, http.MethodGet, path, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
		}
		if strings.Contains(raw, sentinelToken) {
			t.Fatalf("GET %s 泄漏哨兵 token", path)
		}
	}
}

// TestClusterReadPermission 验证权限判定：具备 cluster.read 的 user 可读但不可写。
func TestClusterReadPermission(t *testing.T) {
	c := newClusterServer(t, &fakeClusterConnector{
		info: &cluster.Info{Name: "prod", Context: "prod", Server: "https://10.0.0.1:6443"},
	})
	admin := c.adminToken(t)
	if resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", admin, kubeconfigBody("prod")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin 创建失败: %d %s", resp.StatusCode, raw)
	}

	userRole := roleIDByName(t, c.store, auth.RoleUser)
	role, err := c.store.Roles().Get(context.Background(), userRole)
	if err != nil {
		t.Fatal(err)
	}
	role.Permissions = []string{permission.ClusterRead}
	if err := c.store.Roles().Update(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	userToken := createRoleUser(t, c.store, c.authSvc, auth.RoleUser, "cluster-user", "cluster-pw")
	if resp, raw := c.do(t, http.MethodGet, "/api/v1/clusters", userToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("user 读集群应 200, got %d: %s", resp.StatusCode, raw)
	}
	resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", userToken, kubeconfigBody("evil"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("user 创建集群应 403, got %d: %s", resp.StatusCode, raw)
	}
	// 被拒请求不得产生任何副作用：仍只有 admin 创建的那一个集群。
	clusters, err := c.store.Clusters().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 {
		t.Fatalf("被拒创建不得落库, got %d clusters", len(clusters))
	}
}

// TestClusterLifecycle 覆盖 test/当前/详情/删除 与错误路径。
func TestClusterLifecycle(t *testing.T) {
	c := newClusterServer(t, &fakeClusterConnector{
		info: &cluster.Info{Name: "prod", Context: "prod", Server: "https://10.0.0.1:6443", Version: "v1.28.13", Health: "ok"},
	})
	token := c.adminToken(t)

	// POST /test：只连测不回显、不落库。
	resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters/test", token, kubeconfigBody("probe"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test 应 200, got %d: %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, sentinelToken) {
		t.Fatalf("test 响应泄漏哨兵 token")
	}
	var info cluster.Info
	decodeData(t, mustReplay(t, raw), &info)
	if info.Server != "https://10.0.0.1:6443" || info.Health != "ok" {
		t.Fatalf("test 结果不符: %+v", info)
	}

	// 无集群时 /current 应 404。
	if resp, raw := c.do(t, http.MethodGet, "/api/v1/clusters/current", token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("无集群 /current 应 404, got %d: %s", resp.StatusCode, raw)
	}

	// 创建后 /current 返回状态视图。
	if resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", token, kubeconfigBody("prod")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", resp.StatusCode, raw)
	}
	resp, raw = c.do(t, http.MethodGet, "/api/v1/clusters/current", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("current 应 200, got %d: %s", resp.StatusCode, raw)
	}
	var view cluster.StatusView
	decodeData(t, mustReplay(t, raw), &view)
	if view.Cluster == nil || view.Info.Version != "v1.28.13" || view.Info.Health != "ok" {
		t.Fatalf("current 视图不符: %+v", view)
	}

	// 删除成功后详情 404。
	resp, _ = c.do(t, http.MethodDelete, "/api/v1/clusters/"+view.Cluster.ID, token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete 应 200, got %d", resp.StatusCode)
	}
	if resp, raw := c.do(t, http.MethodGet, "/api/v1/clusters/"+view.Cluster.ID, token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后详情应 404, got %d: %s", resp.StatusCode, raw)
	}
}

// TestClusterErrorPaths 覆盖 409（重名）、400（非法/空 kubeconfig）、404（不存在 id）与 501（未装配）。
func TestClusterErrorPaths(t *testing.T) {
	c := newClusterServer(t, &fakeClusterConnector{
		info: &cluster.Info{Name: "prod", Context: "prod", Server: "https://10.0.0.1:6443"},
	})
	token := c.adminToken(t)
	if resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", token, kubeconfigBody("prod")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", resp.StatusCode, raw)
	}

	if resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", token, kubeconfigBody("prod")); resp.StatusCode != http.StatusConflict {
		t.Fatalf("重名应 409, got %d: %s", resp.StatusCode, raw)
	}
	// 空 kubeconfig 解析失败 → 400。
	if resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", token, `{"name":"nosvc","kubeconfig":""}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 kubeconfig 应 400, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := c.do(t, http.MethodGet, "/api/v1/clusters/does-not-exist", token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在集群应 404, got %d: %s", resp.StatusCode, raw)
	}

	// 未装配：不带 clusters 的普通 server 返回 501。
	st, err := store.OpenSQLite("file:server-nocluster-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	authSvc := auth.New(st.Users(), st.Roles())
	if err := authSvc.Bootstrap(context.Background(), "admin-pw"); err != nil {
		t.Fatal(err)
	}
	// 模拟 cli/server 在 K8S_AI_ENC_KEY 缺失时的装配结果：具体类型为
	// *cluster.Service(nil)。不能因 typed nil 包装进接口而 panic。
	var unavailable *cluster.Service
	plain := httptest.NewServer(NewWithAuth(&fakeScan{}, authSvc).WithClusters(unavailable).Handler())
	t.Cleanup(plain.Close)
	loginResp, _, payload := login(t, plain, `{"username":"admin","password":"admin-pw"}`)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("plain server 登录失败: %d", loginResp.StatusCode)
	}
	plainToken, _ := payload["token"].(string)
	resp, raw := postRaw(t, plain, plainToken, `/api/v1/clusters`, kubeconfigBody("prod"))
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未装配集群应 501, got %d: %s", resp.StatusCode, raw)
	}
	for _, path := range []string{"/api/v1/clusters", "/api/v1/clusters/current"} {
		req, err := http.NewRequest(http.MethodGet, plain.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+plainToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s 不应发生连接重置: %v", path, err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("未装配集群 GET %s 应 501, got %d: %s", path, resp.StatusCode, raw)
		}
	}
}

// TestClusterChangeAudit 校验集群变更写 CLUSTER_CHANGE 审计，且审计不含哨兵 token。
func TestClusterChangeAudit(t *testing.T) {
	c := newClusterServer(t, &fakeClusterConnector{
		info: &cluster.Info{Name: "prod", Context: "prod", Server: "https://10.0.0.1:6443"},
	})
	token := c.adminToken(t)
	resp, raw := c.do(t, http.MethodPost, "/api/v1/clusters", token, kubeconfigBody("prod"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", resp.StatusCode, raw)
	}
	var created store.Cluster
	decodeData(t, mustReplay(t, raw), &created)

	events, err := c.store.Audit().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var clusterChanges int
	for _, ev := range events {
		if ev.Action == store.ActionClusterChange {
			clusterChanges++
			if strings.Contains(ev.ResultSummary, sentinelToken) ||
				strings.Contains(ev.Payload, sentinelToken) {
				t.Fatalf("审计泄漏哨兵 token: %+v", ev)
			}
			if ev.ClusterID != created.ID || ev.Operation != "create" {
				t.Fatalf("审计关联字段缺失: %+v", ev)
			}
		}
	}
	if clusterChanges == 0 {
		t.Fatal("创建集群后应有 CLUSTER_CHANGE 审计")
	}
}

// createRoleUser 创建指定角色的用户并返回其登录 token（测试辅助）。
func createRoleUser(t *testing.T, st store.Store, authSvc *auth.Service, roleName, username, password string) string {
	t.Helper()
	roleID := roleIDByName(t, st, roleName)
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users().Create(context.Background(), &store.User{
		ID: "u-" + username, Username: username, PasswordHash: hash, RoleID: roleID,
		Status: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	sess, err := authSvc.Login(context.Background(), username, password)
	if err != nil {
		t.Fatalf("login %s: %v", username, err)
	}
	return sess.Token
}

// postRaw 向给定 server 发 POST，返回未关闭 body 的已读响应。
func postRaw(t *testing.T, ts *httptest.Server, token, path, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
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
