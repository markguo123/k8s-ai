package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// parseConnector 复用真实 ParseKubeconfig 做连测（测试隔离点内仍按真实解析口径校验）。
type parseConnector struct {
	version string
	health  string
	err     error
	calls   int
}

func (p *parseConnector) Inspect(ctx context.Context, data []byte) (*Info, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	info, err := ParseKubeconfig(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	info.Version = p.version
	info.Health = p.health
	return info, nil
}

// kubeconfigFor 构造最小可解析 kubeconfig。token 是凭据哨兵值：不加密落库、
// 不回读、不进审计时即视为泄漏。
func kubeconfigFor(server, token string) string {
	ctxName := "test-ctx"
	if server == "" {
		server = "https://10.0.0.1:6443"
	}
	if token == "" {
		token = "SECRET-TOKEN-default"
	}
	return "apiVersion: v1\n" +
		"kind: Config\n" +
		"clusters:\n" +
		"- name: c1\n" +
		"  cluster:\n" +
		"    server: " + server + "\n" +
		"contexts:\n" +
		"- name: " + ctxName + "\n" +
		"  context:\n" +
		"    cluster: c1\n" +
		"    user: u1\n" +
		"current-context: " + ctxName + "\n" +
		"users:\n" +
		"- name: u1\n" +
		"  user:\n" +
		"    token: " + token + "\n"
}

func newTestService(t *testing.T, p *parseConnector) (*Service, store.Store, *[]store.AuditEvent) {
	t.Helper()
	st, err := store.OpenSQLite("file:cluster-svc-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	base := time.Now().UTC()
	var events []store.AuditEvent
	svc := New(st.Clusters(), st.Credentials(), mustCipher(t),
		WithConnector(p),
		WithClock(func() time.Time { base = base.Add(time.Second); return base }),
		WithAudit(func(ctx context.Context, e store.AuditEvent) error {
			events = append(events, e)
			return nil
		}))
	return svc, st, &events
}

func mustCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ci, err := NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return ci
}

func TestServiceCreateListGetDelete(t *testing.T) {
	p := &parseConnector{version: "v1.30.0", health: "ok"}
	svc, st, events := newTestService(t, p)

	c, err := svc.Create(context.Background(), CreateRequest{Name: "prod", Kubeconfig: kubeconfigFor("", "SECRET-TOKEN-9f2b")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID == "" || c.Status != string(StatusConnected) || c.Server == "" {
		t.Fatalf("Create 元数据不完整: %+v", c)
	}

	// 落库的凭据必须是密文：库里不得出现明文 kubeconfig。
	cred, err := st.Credentials().GetByCluster(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("GetByCluster: %v", err)
	}
	if strings.Contains(cred.KubeconfigSecret, "SECRET-TOKEN-9f2b") {
		t.Fatal("凭据落库不应包含明文 kubeconfig")
	}

	// List/Get 只回元数据，不暴露凭据对象字段。
	list, err := svc.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v, %d", err, len(list))
	}
	got, err := svc.Get(context.Background(), c.ID)
	if err != nil || got.ID != c.ID {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), "Kubeconfig") {
		t.Fatal("Get 不应暴露凭据对象字段")
	}

	// 审计事件只含集群标识与 server，不含 kubeconfig 明文凭据。
	if len(*events) == 0 {
		t.Fatal("应写入 CLUSTER_CHANGE 审计")
	}
	for _, ev := range *events {
		if ev.EventType != store.AuditEventClusterChange {
			continue
		}
		if strings.Contains(ev.ResultSummary+ev.ParameterSummary, "SECRET-TOKEN-9f2b") {
			t.Fatal("审计不得携带 kubeconfig 明文凭据")
		}
	}

	// Delete 同时移除集群元数据与加密凭据。
	if err := svc.Delete(context.Background(), c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(context.Background(), c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后 Get 应 ErrNotFound, got %v", err)
	}
	if _, err := st.Credentials().GetByCluster(context.Background(), c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除后凭据应一并移除, got %v", err)
	}
}

func TestServiceCreateDuplicate(t *testing.T) {
	svc, _, _ := newTestService(t, &parseConnector{version: "v1.30.0"})
	req := CreateRequest{Name: "cluster-a", Kubeconfig: kubeconfigFor("", "")}
	if _, err := svc.Create(context.Background(), req); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(context.Background(), req); !errors.Is(err, ErrExists) {
		t.Fatalf("重复创建应 ErrExists, got %v", err)
	}
}

func TestServiceCreateInvalid(t *testing.T) {
	svc, _, _ := newTestService(t, &parseConnector{err: fmt.Errorf("%w: connect failed", ErrInvalid)})
	if _, err := svc.Create(context.Background(), CreateRequest{Kubeconfig: kubeconfigFor("", "")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("连接失败应 ErrInvalid, got %v", err)
	}
	if _, err := svc.Create(context.Background(), CreateRequest{Kubeconfig: "   "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 kubeconfig 应 ErrInvalid, got %v", err)
	}
}

func TestServiceTestDoesNotPersist(t *testing.T) {
	svc, st, _ := newTestService(t, &parseConnector{version: "v1.29.2", health: "ok"})
	info, err := svc.Test(context.Background(), TestRequest{Kubeconfig: kubeconfigFor("", "")})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if info.Version != "v1.29.2" || info.Health != "ok" || info.Context == "" {
		t.Fatalf("Test 返回值不完整: %+v", info)
	}
	list, err := st.Clusters().List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("Test 不应落库, got %d", len(list))
	}
}

// TestServiceCurrentPicksLatest 验证 GET /clusters/current 取最近创建的受管集群。
func TestServiceCurrentPicksLatest(t *testing.T) {
	p := &parseConnector{version: "v1.30.0", health: "ok"}
	svc, _, _ := newTestService(t, p)
	first, err := svc.Create(context.Background(), CreateRequest{Name: "first", Kubeconfig: kubeconfigFor("https://10.0.0.2:6443", "")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(context.Background(), CreateRequest{Name: "second", Kubeconfig: kubeconfigFor("https://10.0.0.3:6443", "")})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if view.Cluster.ID == first.ID {
		t.Fatal("Current 应取最近创建的集群，而不是旧集群")
	}
	if view.Cluster.ID != second.ID || view.Info.Server == "" {
		t.Fatalf("Current 状态不符: %+v", view)
	}
}

func TestServiceRefreshDisconnected(t *testing.T) {
	p := &parseConnector{version: "v1.30.0"}
	svc, _, _ := newTestService(t, p)
	c, err := svc.Create(context.Background(), CreateRequest{Name: "prod", Kubeconfig: kubeconfigFor("", "")})
	if err != nil {
		t.Fatal(err)
	}
	p.err = fmt.Errorf("%w: connect failed: dial tcp", ErrInvalid)
	view, err := svc.Refresh(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Refresh 连测失败不应视为内部错误, got %v", err)
	}
	if view.Cluster.Status != string(StatusDisconnected) || view.Info.Health != "down" {
		t.Fatalf("刷新失败应置 disconnected/down: %+v", view)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	svc, _, _ := newTestService(t, &parseConnector{})
	if _, err := svc.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知 id 应 ErrNotFound, got %v", err)
	}
	if _, err := svc.Get(context.Background(), " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("空 id 应 ErrInvalid, got %v", err)
	}
}
