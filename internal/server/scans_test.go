package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// grantNamespaces 给用户授出命名空间集合。EffectiveScope 只在 permissions 含 "*" 时
// 累计 namespaces（destroy 是"在已授权命名空间上额外能销毁"，不单独开辟可见范围），
// 所以这里必须用 "*"，照抄 grantCluster 的 destroy 会得到一个空范围。
func (f *reportFixture) grantNamespaces(t *testing.T, principalID string, namespaces ...string) {
	t.Helper()
	if _, err := f.authzSvc.CreateGrant(context.Background(), authz.CreateGrantRequest{
		PrincipalType: "user", PrincipalID: principalID,
		Namespaces: namespaces, Permissions: []string{"*"},
	}, "admin"); err != nil {
		t.Fatalf("create grant: %v", err)
	}
}

// postScan 发起巡检并返回 (状态码, 任务 ID)。
func (f *reportFixture) postScan(t *testing.T, token, body string) (int, string) {
	t.Helper()
	status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", token, body)
	if status != http.StatusAccepted {
		return status, ""
	}
	var env struct {
		Data ScanJob `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("解析 202 响应失败：%v / %s", err, raw)
	}
	return status, env.Data.ID
}

// waitScan 等任务离开 running：单次并发闸门下，上一个任务没结束就发下一个只会拿到 409，
// 那会把"第二个请求的断言"变成一次完全无关的并发测试。
func (f *reportFixture) waitScan(t *testing.T, token, id string) {
	t.Helper()
	if id == "" {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, _, raw := f.req(t, http.MethodGet, "/api/v1/scans/"+id, token, "")
		if status != http.StatusOK {
			t.Fatalf("等待任务结束却拿到 %d：%s", status, raw)
		}
		var env struct {
			Data ScanJob `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		if env.Data.Status == StatusSucceeded || env.Data.Status == StatusFailed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("任务未在超时内结束")
}

// TestScanClusterScopeRequiresAdmin 锁方案 17 §4.2：全集群巡检读的是全部命名空间的清单、
// 事件与日志，超出 namespace 授权的表达力，operator 有 scan.execute 也不能扫全集群。
func TestScanClusterScopeRequiresAdmin(t *testing.T) {
	f := newReportFixture(t, nil)
	operator := f.userToken(t, "op-cluster", "operator")
	f.grantNamespaces(t, "u-op-cluster", "app")

	status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", operator, `{"scope":"cluster"}`)
	if status != http.StatusForbidden {
		t.Fatalf("operator 发起全集群巡检应 403，got %d：%s", status, raw)
	}
	// 被拒时不该有任何一次扫描真的跑起来。
	select {
	case opts := <-f.scan.seen:
		t.Fatalf("403 之后仍然发起了扫描：%+v", opts)
	case <-time.After(200 * time.Millisecond):
	}

	if status, id := f.postScan(t, f.adminToken(t), `{"scope":"cluster"}`); status != http.StatusAccepted {
		t.Fatalf("admin 发起全集群巡检应 202，got %d", status)
	} else {
		f.waitScan(t, f.adminToken(t), id)
	}
}

// TestScanNamespaceScopeRequiresGrant 锁方案 17 §4.3：非 admin 只能扫已授权命名空间，
// 越权点名拒绝且**不静默裁剪**成已授权子集。
func TestScanNamespaceScopeRequiresGrant(t *testing.T) {
	f := newReportFixture(t, nil)
	operator := f.userToken(t, "op-ns", "operator")
	f.grantNamespaces(t, "u-op-ns", "app")

	status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", operator,
		`{"scope":"namespace","namespace":"kube-system"}`)
	if status != http.StatusForbidden {
		t.Fatalf("扫未授权命名空间应 403，got %d：%s", status, raw)
	}
	// 文案要点名被拒的命名空间：静默改扫 app 会让人拿着一份没要的结论去判断集群健康。
	if !strings.Contains(raw, "kube-system") {
		t.Fatalf("403 文案应点名被拒的命名空间：%s", raw)
	}
	select {
	case opts := <-f.scan.seen:
		t.Fatalf("越权请求仍然发起了扫描：%+v", opts)
	case <-time.After(200 * time.Millisecond):
	}

	// 已授权命名空间放行，且实际扫描参数落在请求要的那个 ns 上。
	status, id := f.postScan(t, operator, `{"scope":"namespace","namespace":"app"}`)
	if status != http.StatusAccepted {
		t.Fatalf("扫已授权命名空间应 202，got %d", status)
	}
	var opts model.ScanOptions
	select {
	case opts = <-f.scan.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("扫描未被调用")
	}
	if opts.Namespace != "app" {
		t.Fatalf("实际扫描命名空间被改写：%+v", opts)
	}
	if opts.Attribution.ScopeKind != model.ScopeNamespace || opts.Attribution.ScopeValue != "app" {
		t.Fatalf("巡检范围两列不对：%+v", opts.Attribution)
	}
	f.waitScan(t, operator, id)

	// pod 范围同样受命名空间闸门约束，且范围取值带 <ns>/<pod>。
	if status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", operator,
		`{"scope":"pod","namespace":"kube-system","pod":"web-0"}`); status != http.StatusForbidden {
		t.Fatalf("扫未授权命名空间里的 pod 应 403，got %d：%s", status, raw)
	}
}

// TestScanScopeValidation 锁范围参数本身的形状：缺字段、野值、clusterId 一律 400，
// 而不是"缺了就当成全集群"。
func TestScanScopeValidation(t *testing.T) {
	f := newReportFixture(t, nil)
	admin := f.adminToken(t)
	cases := map[string]string{
		"scope 缺失":         `{"namespace":"app"}`,
		"scope 野值":         `{"scope":"project","namespace":"app"}`,
		"namespace 缺失":     `{"scope":"namespace"}`,
		"pod 缺 pod":       `{"scope":"pod","namespace":"app"}`,
		"pod 缺 namespace": `{"scope":"pod","pod":"web-0"}`,
		"preset 野值":        `{"scope":"namespace","namespace":"app","preset":"ultra"}`,
		"clusterId 未接入":    `{"scope":"namespace","namespace":"app","clusterId":"c-prod"}`,
	}
	for name, body := range cases {
		status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", admin, body)
		if status != http.StatusBadRequest {
			t.Errorf("%s 应 400，got %d：%s", name, status, raw)
		}
	}
}

// TestScanJobOwnerIsolation 锁 §4.5：任务结果含被扫命名空间的问题清单，别人的任务读不到，
// 且"别人的任务"与"不存在"同形，job id 不成为存在性探测面。
func TestScanJobOwnerIsolation(t *testing.T) {
	f := newReportFixture(t, nil)
	operator := f.userToken(t, "op-owner", "operator")
	other := f.userToken(t, "op-other", "operator")
	f.grantNamespaces(t, "u-op-owner", "app")
	f.grantNamespaces(t, "u-op-other", "app")

	_, id := f.postScan(t, operator, `{"scope":"namespace","namespace":"app"}`)
	if id == "" {
		t.Fatal("发起巡检未拿到任务 ID")
	}
	f.waitScan(t, operator, id)

	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/scans/"+id, other, ""); status != http.StatusNotFound {
		t.Fatalf("他人读任务应同形 404，got %d：%s", status, raw)
	}
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/scans/"+id, operator, ""); status != http.StatusOK {
		t.Fatalf("发起人读自己的任务应 200，got %d：%s", status, raw)
	}
	if status, _, raw := f.req(t, http.MethodGet, "/api/v1/scans/"+id, f.adminToken(t), ""); status != http.StatusOK {
		t.Fatalf("admin 读任务应 200，got %d：%s", status, raw)
	}
}

// TestScanPresetMapping 锁 §5 档位表：档位只能关掉 LLM、只能收窄超时，不能无中生有。
func TestScanPresetMapping(t *testing.T) {
	base := func() model.ScanOptions {
		o := model.ScanOptions{}
		o.LLM.Enabled = true
		o.Timeout = 30 * time.Minute
		o.CollectPreviousLogs = true
		return o
	}

	// 快速体检：不跑 LLM、不收日志、超时收到 60s。
	quick := base()
	applyScanPreset(&quick, scanPresets[scanPresetQuick])
	if quick.LLM.Enabled || quick.CollectLogs || !quick.CollectEvents {
		t.Fatalf("quick 档位映射不对：%+v", quick)
	}
	if quick.Timeout != scanPresets[scanPresetQuick].timeout {
		t.Fatalf("quick 超时应收到 %v，got %v", scanPresets[scanPresetQuick].timeout, quick.Timeout)
	}
	if quick.CollectPreviousLogs || quick.Since != 0 {
		t.Fatalf("不采日志时不该留着日志窗口参数：%+v", quick)
	}

	// 深度巡检：开日志与时间窗。
	deep := base()
	applyScanPreset(&deep, scanPresets[scanPresetDeep])
	if !deep.LLM.Enabled || !deep.CollectLogs || deep.Since != time.Hour {
		t.Fatalf("deep 档位映射不对：%+v", deep)
	}

	// 配置里的超时更短时以配置为准：现场把 scan.timeout 调小必须真的生效。
	tight := base()
	tight.Timeout = 2 * time.Minute
	applyScanPreset(&tight, scanPresets[scanPresetStandard])
	if tight.Timeout != 2*time.Minute {
		t.Fatalf("超时应收敛到配置值 2m，got %v", tight.Timeout)
	}

	// 部署没配模型时，档位不许凭空打开 LLM（否则标准巡检必然以失败收场）。
	noLLM := model.ScanOptions{Timeout: 10 * time.Minute}
	applyScanPreset(&noLLM, scanPresets[scanPresetStandard])
	if noLLM.LLM.Enabled {
		t.Fatal("无模型配置下档位不得自行启用 LLM")
	}
}
