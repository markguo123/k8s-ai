package rule

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// ctxFor 用快照构建规则上下文（索引由 correlation 构建）。
func ctxFor(snap *model.ClusterSnapshot) *RuleContext {
	return &RuleContext{Snapshot: snap, Index: correlation.Build(snap), Severity: DefaultSeverityPolicy(), Now: time.Now()}
}

func testPod(ns, name string, containers ...model.ContainerInfo) model.PodInfo {
	return model.PodInfo{Ref: model.ResourceRef{Kind: "Pod", Namespace: ns, Name: name}, Containers: containers}
}

func crashContainer() model.ContainerInfo {
	return model.ContainerInfo{
		Name: "web", State: "Waiting", Reason: "CrashLoopBackOff",
		RestartCount: 17, LastReason: "OOMKilled", LastExitCode: 137,
		Limits: map[string]string{"memory": "256Mi"},
	}
}

func TestCrashLoopBackOff(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Pods:       []model.PodInfo{testPod("default", "web-abc", crashContainer())},
	}
	rctx := ctxFor(snap)
	fs := (CrashLoopBackOffRule{}).Evaluate(rctx)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	f := fs[0]
	if f.Severity != model.SeverityMedium {
		t.Fatalf("severity = %s, want MEDIUM", f.Severity)
	}
	if f.ID == "" || f.ID != Fingerprint(rctx.Index, *f) {
		t.Fatal("指纹异常")
	}
	// NeedsLogs → 进入 Phase2 目标。
	reg := NewRegistry()
	reg.Register(CrashLoopBackOffRule{})
	targets := LogTargets(reg, fs, rctx.Index)
	if len(targets) != 1 || targets[0].Name != "web-abc" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestPodRules(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Pods: []model.PodInfo{
			testPod("prod", "oom-1", model.ContainerInfo{Name: "c", LastReason: "OOMKilled", LastExitCode: 137, Limits: map[string]string{"memory": "256Mi"}}),
			testPod("prod", "img-1", model.ContainerInfo{Name: "c", State: "Waiting", Reason: "ImagePullBackOff", Message: "pull denied", Image: "repo/img:latest"}),
			testPod("prod", "pend-1", model.ContainerInfo{Name: "c"}),
		},
	}
	snap.Pods[2].Phase = "Pending"
	snap.Pods[2].Conditions = []model.ConditionInfo{{Type: "PodScheduled", Status: "False", Message: "0/3 nodes available"}}
	rctx := ctxFor(snap)
	if fs := (OOMKilledRule{}).Evaluate(rctx); len(fs) != 1 {
		t.Fatalf("OOMKilled findings = %d, want 1", len(fs))
	}
	if fs := (ImagePullBackOffRule{}).Evaluate(rctx); len(fs) != 1 {
		t.Fatalf("ImagePullBackOff findings = %d, want 1", len(fs))
	}
	if fs := (PendingPodRule{}).Evaluate(rctx); len(fs) != 1 {
		t.Fatalf("PendingPod findings = %d, want 1", len(fs))
	}
}

func nodeWithConditions(name string, conds ...model.ConditionInfo) model.NodeInfo {
	return model.NodeInfo{Ref: model.ResourceRef{Kind: "Node", Name: name}, Conditions: conds}
}

func TestNodeRulesAndCorrelated(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Nodes: []model.NodeInfo{
			nodeWithConditions("node-1", model.ConditionInfo{Type: "Ready", Status: "False", Reason: "KubeletNotReady"}),
			nodeWithConditions("node-2", model.ConditionInfo{Type: "DiskPressure", Status: "True"}),
		},
		Pods: []model.PodInfo{
			{Ref: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-1"}, NodeName: "node-1", Containers: []model.ContainerInfo{crashContainer()}},
			{Ref: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "ok-1"}, NodeName: "node-3", Containers: []model.ContainerInfo{{Name: "c"}}},
		},
	}
	rctx := ctxFor(snap)
	notReady := (NodeNotReadyRule{}).Evaluate(rctx)
	if len(notReady) != 1 || notReady[0].Severity != model.SeverityCritical {
		t.Fatalf("NotReady = %+v", notReady)
	}
	if fs := (NodeDiskPressureRule{}).Evaluate(rctx); len(fs) != 1 {
		t.Fatalf("DiskPressure findings = %d", len(fs))
	}
	if fs := (NodeMemoryPressureRule{}).Evaluate(rctx); len(fs) != 0 {
		t.Fatalf("MemoryPressure 不应命中")
	}
	// 引擎级后处理：node-1 上的 web-1 应被标记 Correlated。
	reg := NewRegistry()
	for _, r := range []Rule{CrashLoopBackOffRule{}, NodeNotReadyRule{}} {
		reg.Register(r)
	}
	findings := NewEngine(reg).Evaluate(context.Background(), snap, rctx.Index, model.ScanOptions{})
	var podF *model.Finding
	for _, f := range findings {
		if f.Rule == "CrashLoopBackOff" {
			podF = f
		}
	}
	if podF == nil || !podF.Correlated {
		t.Fatalf("web-1 应被标记 Correlated: %+v", podF)
	}
}

func TestStorageAndNetworkRules(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Storage: []model.StorageInfo{
			{Ref: model.ResourceRef{Kind: "PVC", Namespace: "default", Name: "data-0"}, Kind: "PVC", Status: "Pending"},
		},
		Services: []model.ServiceInfo{
			{Ref: model.ResourceRef{Kind: "Service", Namespace: "default", Name: "web-svc"}, Selector: map[string]string{"app": "web"}},
			{Ref: model.ResourceRef{Kind: "Service", Namespace: "default", Name: "no-selector"}},
		},
	}
	rctx := ctxFor(snap)
	if fs := (PVCPendingRule{}).Evaluate(rctx); len(fs) != 1 || fs[0].Severity != model.SeverityHigh {
		t.Fatalf("PVCPending = %+v", fs)
	}
	svc := (ServiceNoEndpointRule{}).Evaluate(rctx)
	if len(svc) != 1 || svc[0].Resource.Name != "web-svc" {
		t.Fatalf("ServiceNoEndpoint = %+v", svc)
	}
}

func TestFailedMountRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Pods:       []model.PodInfo{testPod("prod", "db-0")},
		EventsIndex: map[string][]model.EventInfo{
			"uid-1": {{Reason: "FailedMount", Type: "Warning", Message: "MountVolume.SetUp failed", InvolvedObject: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "db-0", UID: "uid-1"}}},
		},
	}
	snap.Pods[0].Ref.UID = "uid-1"
	rctx := ctxFor(snap)
	fs := (FailedMountRule{}).Evaluate(rctx)
	if len(fs) != 1 {
		t.Fatalf("FailedMount findings = %d, want 1", len(fs))
	}
}

func TestWorkloadRules(t *testing.T) {
	desired := int32(3)
	ready1 := int32(1)
	ready3 := int32(3)
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Workloads: []model.WorkloadInfo{
			{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "prod", Name: "web"}, DesiredReplicas: &desired, ReadyReplicas: &ready1},
			{Ref: model.ResourceRef{Kind: "StatefulSet", Namespace: "prod", Name: "db"}, DesiredReplicas: &desired, ReadyReplicas: &ready1},
			{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "prod", Name: "ok"}, DesiredReplicas: &desired, ReadyReplicas: &ready3},
			{Ref: model.ResourceRef{Kind: "Job", Namespace: "prod", Name: "migrate"}, Conditions: []model.ConditionInfo{{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded"}}},
		},
	}
	rctx := ctxFor(snap)
	if fs := (DeploymentReplicaRule{}).Evaluate(rctx); len(fs) != 1 || fs[0].Resource.Name != "web" {
		t.Fatalf("DeploymentReplica = %+v", fs)
	}
	if fs := (StatefulSetReplicaRule{}).Evaluate(rctx); len(fs) != 1 {
		t.Fatalf("StatefulSetReplica = %+v", fs)
	}
	if fs := (JobFailedRule{}).Evaluate(rctx); len(fs) != 1 {
		t.Fatalf("JobFailed = %+v", fs)
	}
}

func TestSeverityAdjust(t *testing.T) {
	svcKey := "Service/prod/web-svc"
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{
			{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}, Labels: map[string]string{"environment": "production"}},
			{Ref: model.ResourceRef{Kind: "Namespace", Name: "dev"}},
			{Ref: model.ResourceRef{Kind: "Namespace", Name: "kube-system"}},
		},
		Pods: []model.PodInfo{
			{Ref: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "p1"}, Labels: map[string]string{"app": "other"}},
			{Ref: model.ResourceRef{Kind: "Pod", Namespace: "dev", Name: "p2"}},
			{Ref: model.ResourceRef{Kind: "Pod", Namespace: "kube-system", Name: "p3"}},
		},
		Services: []model.ServiceInfo{
			{Ref: model.ResourceRef{Kind: "Service", Namespace: "prod", Name: "web-svc"}, Selector: map[string]string{"app": "web"}},
		},
	}
	idx := correlation.Build(snap)
	pol := DefaultSeverityPolicy()
	if got := pol.Adjust(model.SeverityMedium, model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "p1"}, idx); got != model.SeverityHigh {
		t.Fatalf("生产环境提升后 = %s, want HIGH", got)
	}
	if got := pol.Adjust(model.SeverityMedium, model.ResourceRef{Kind: "Pod", Namespace: "dev", Name: "p2"}, idx); got != model.SeverityLow {
		t.Fatalf("开发环境降级后 = %s, want LOW", got)
	}
	if got := pol.Adjust(model.SeverityMedium, model.ResourceRef{Kind: "Pod", Namespace: "kube-system", Name: "p3"}, idx); got != model.SeverityHigh {
		t.Fatalf("系统命名空间提升后 = %s, want HIGH", got)
	}
	_ = svcKey
}

func TestFingerprintOwnerNormalization(t *testing.T) {
	// 同一 Deployment 下两个随机 Pod 名的 CrashLoop，指纹应一致（ADR-003）。
	mk := func(podName string) *model.ClusterSnapshot {
		return &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
			Pods: []model.PodInfo{{
				Ref:        model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: podName},
				OwnerRefs:  []model.ResourceRef{{Kind: "ReplicaSet", Namespace: "prod", Name: "web-rs"}},
				Containers: []model.ContainerInfo{crashContainer()},
			}},
			Workloads: []model.WorkloadInfo{
				{Ref: model.ResourceRef{Kind: "ReplicaSet", Namespace: "prod", Name: "web-rs"}, OwnerRefs: []model.ResourceRef{{Kind: "Deployment", Namespace: "prod", Name: "web"}}},
				{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "prod", Name: "web"}},
			},
		}
	}
	id1 := (CrashLoopBackOffRule{}).Evaluate(ctxFor(mk("web-abc123")))[0].ID
	id2 := (CrashLoopBackOffRule{}).Evaluate(ctxFor(mk("web-def456")))[0].ID
	if id1 != id2 {
		t.Fatalf("owner 归一化后指纹应一致: %s vs %s", id1, id2)
	}
}

func TestRegistryFiltered(t *testing.T) {
	reg := NewRegistry()
	reg.Register(CrashLoopBackOffRule{})
	reg.Register(OOMKilledRule{})
	if got := len(reg.Filtered(nil, []string{"OOMKilled"})); got != 1 {
		t.Fatalf("disabled 过滤后 = %d, want 1", got)
	}
	if got := len(reg.Filtered([]string{"OOMKilled"}, nil)); got != 1 {
		t.Fatalf("enabled 过滤后 = %d, want 1", got)
	}
}

func TestEngineEmptySnapshot(t *testing.T) {
	reg := NewRegistry()
	for _, r := range AllDefault() {
		reg.Register(r)
	}
	fs := NewEngine(reg).Evaluate(context.Background(), &model.ClusterSnapshot{}, correlation.Build(&model.ClusterSnapshot{}), model.ScanOptions{})
	if len(fs) != 0 {
		t.Fatalf("空快照不应有 finding: %d", len(fs))
	}
}

// TestContainerCreateErrorRule 覆盖 Secret 缺失/配置错误导致的容器创建失败。
func TestContainerCreateErrorRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Pods: []model.PodInfo{{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "app-0", UID: "uid-1"},
			SecretRefs: []model.ResourceRef{{Kind: "Secret", Namespace: "prod", Name: "db-secret"}},
			Containers: []model.ContainerInfo{{Name: "app", State: "Waiting", Reason: "CreateContainerConfigError", Message: "secret \"db-secret\" not found"}},
		}},
		EventsIndex: map[string][]model.EventInfo{
			"uid-1": {{Reason: "FailedMount", Type: "Warning", Message: "MountVolume.SetUp failed for volume \"cred\" : secret \"db-secret\" not found", InvolvedObject: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "app-0", UID: "uid-1"}}},
		},
	}
	rctx := ctxFor(snap)
	fs := (ContainerCreateErrorRule{}).Evaluate(rctx)
	if len(fs) != 1 {
		t.Fatalf("ContainerCreateError findings = %d, want 1", len(fs))
	}
	hasSecret := false
	for _, e := range fs[0].Evidence {
		if e.Key == "secretRefs" && strings.Contains(e.Value, "prod/db-secret") {
			hasSecret = true
		}
	}
	if !hasSecret {
		t.Fatalf("证据缺少 secretRefs: %+v", fs[0].Evidence)
	}
	// FailedMount 事件规则也应对同一场景命中（事件驱动兜底）。
	if fm := (FailedMountRule{}).Evaluate(rctx); len(fm) != 1 {
		t.Fatalf("FailedMount findings = %d, want 1", len(fm))
	}
}

// TestScenarioCoverage 覆盖三类常见故障：Secret 缺失 / PVC 未绑定 / 镜像拉取失败。
func TestScenarioCoverage(t *testing.T) {
	// 镜像拉取失败
	pullSnap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Pods:       []model.PodInfo{testPod("default", "img-1", model.ContainerInfo{Name: "c", State: "Waiting", Reason: "ImagePullBackOff", Message: "pull access denied", Image: "repo/img:latest"})},
	}
	if fs := (ImagePullBackOffRule{}).Evaluate(ctxFor(pullSnap)); len(fs) != 1 {
		t.Fatalf("ImagePullBackOff 应命中: %d", len(fs))
	}
	// PVC 未绑定（Pending）
	pvcSnap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Storage: []model.StorageInfo{
			{Ref: model.ResourceRef{Kind: "PVC", Namespace: "default", Name: "data"}, Kind: "PVC", Status: "Pending"},
		},
	}
	if fs := (PVCPendingRule{}).Evaluate(ctxFor(pvcSnap)); len(fs) != 1 {
		t.Fatalf("PVCPending 应命中: %d", len(fs))
	}
	// Secret 缺失（CreateContainerConfigError）
	secSnap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Pods: []model.PodInfo{{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "app-0"},
			SecretRefs: []model.ResourceRef{{Kind: "Secret", Namespace: "default", Name: "db-secret"}},
			Containers: []model.ContainerInfo{{Name: "app", State: "Waiting", Reason: "CreateContainerConfigError", Message: "secret not found"}},
		}},
	}
	if fs := (ContainerCreateErrorRule{}).Evaluate(ctxFor(secSnap)); len(fs) != 1 {
		t.Fatalf("ContainerCreateError 应命中: %d", len(fs))
	}
}

// TestUnhealthyRule 探针失败信号：Running 未 Ready + Unhealthy 事件。
func TestUnhealthyRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Pods: []model.PodInfo{{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "u-1"},
			Containers: []model.ContainerInfo{{Name: "web", State: "Running", Ready: false}},
		}},
		EventsIndex: map[string][]model.EventInfo{
			"u-1": {{Reason: "Unhealthy", Type: "Warning", Message: "Readiness probe failed", InvolvedObject: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "u-1"}}},
		},
	}
	fs := (UnhealthyRule{}).Evaluate(ctxFor(snap))
	if len(fs) != 1 {
		t.Fatalf("Unhealthy findings = %d, want 1", len(fs))
	}
}

// TestIngressBackendRule Ingress backend 指向不存在 Service 的信号。
func TestIngressBackendRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Ingresses:  []model.IngressInfo{{Ref: model.ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "web"}, Backends: []string{"prod/web-svc:80"}}},
	}
	fs := (IngressBackendRule{}).Evaluate(ctxFor(snap))
	if len(fs) != 1 {
		t.Fatalf("IngressBackend findings = %d, want 1", len(fs))
	}
}

// TestInitContainerRules init 容器不再是盲区：
// CrashLoopBackOff/ImagePullBackOff/ContainerCreateError 均应识别 init 容器并正确称谓。
func TestInitContainerRules(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Pods: []model.PodInfo{{
			Ref:            model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "app-0"},
			InitContainers: []model.ContainerInfo{{Name: "init-db", State: "Waiting", Reason: "CrashLoopBackOff", RestartCount: 5, LastReason: "Error", LastExitCode: 1}},
			Containers:     []model.ContainerInfo{{Name: "app", State: "Running", Ready: true}},
		}},
	}
	rctx := ctxFor(snap)
	fs := (CrashLoopBackOffRule{}).Evaluate(rctx)
	if len(fs) != 1 {
		t.Fatalf("init 容器 CrashLoopBackOff findings = %d, want 1", len(fs))
	}
	if !strings.Contains(fs[0].Title, "init 容器 init-db") {
		t.Fatalf("标题应标识 init 容器: %s", fs[0].Title)
	}
	// init 容器镜像拉取失败。
	snap.Pods[0].InitContainers = []model.ContainerInfo{{Name: "init-db", State: "Waiting", Reason: "ImagePullBackOff", Message: "pull denied", Image: "repo/init:1"}}
	if fs := (ImagePullBackOffRule{}).Evaluate(rctx); len(fs) != 1 || !strings.Contains(fs[0].Title, "init 容器") {
		t.Fatalf("init 容器 ImagePullBackOff = %+v", fs)
	}
	// init 容器创建失败（配置缺失应升 HIGH）。
	snap.Pods[0].InitContainers = []model.ContainerInfo{{Name: "init-db", State: "Waiting", Reason: "CreateContainerConfigError", Message: "configmap \"cfg\" not found"}}
	fs = (ContainerCreateErrorRule{}).Evaluate(rctx)
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "init 容器") {
		t.Fatalf("init 容器 ContainerCreateError = %+v", fs)
	}
	if fs[0].Severity != model.SeverityHigh {
		t.Fatalf("init 容器配置缺失应为 HIGH: %s", fs[0].Severity)
	}
}

// TestPendingPodAgeThreshold PendingPod 时间门槛：新建（<60s）的 Pending 属滚动发布暂态，跳过。
func TestPendingPodAgeThreshold(t *testing.T) {
	mk := func(age time.Duration) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
			Pods: []model.PodInfo{{
				Ref:               model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "web-new"},
				Phase:             "Pending",
				CreationTimestamp: time.Now().Add(-age).UTC().Format(time.RFC3339),
			}},
		}
		return ctxFor(snap)
	}
	if fs := (PendingPodRule{}).Evaluate(mk(30 * time.Second)); len(fs) != 0 {
		t.Fatalf("Pending <60s 应被时间门槛跳过: %d", len(fs))
	}
	if fs := (PendingPodRule{}).Evaluate(mk(5 * time.Minute)); len(fs) != 1 {
		t.Fatalf("Pending >60s 应上报: %d", len(fs))
	}
}

// TestPodTerminatingRule Pod 卡 Terminating：>5min 才报，且证据含 finalizers。
func TestPodTerminatingRule(t *testing.T) {
	mk := func(age time.Duration, finalizers []string) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
			Pods: []model.PodInfo{{
				Ref:               model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "web-old"},
				DeletionTimestamp: time.Now().Add(-age).UTC().Format(time.RFC3339),
				Finalizers:        finalizers,
			}},
		}
		return ctxFor(snap)
	}
	if fs := (PodTerminatingRule{}).Evaluate(mk(2*time.Minute, nil)); len(fs) != 0 {
		t.Fatalf("Terminating <5min 应跳过: %d", len(fs))
	}
	fs := (PodTerminatingRule{}).Evaluate(mk(10*time.Minute, []string{"kubernetes.io/pv-protection"}))
	if len(fs) != 1 {
		t.Fatalf("Terminating >5min 应上报: %d", len(fs))
	}
	hasFin := false
	for _, e := range fs[0].Evidence {
		if e.Key == "finalizers" {
			hasFin = true
		}
	}
	if !hasFin {
		t.Fatalf("证据应包含 finalizers: %+v", fs[0].Evidence)
	}
}

// TestNodePIDPressureRule 节点 PID 压力：规则命中且其下 Pod 异常被标记 Correlated。
func TestNodePIDPressureRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Nodes:      []model.NodeInfo{nodeWithConditions("node-p", model.ConditionInfo{Type: "PIDPressure", Status: "True"})},
		Pods: []model.PodInfo{
			{Ref: model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "web-1"}, NodeName: "node-p", Containers: []model.ContainerInfo{crashContainer()}},
		},
	}
	rctx := ctxFor(snap)
	fs := (NodePIDPressureRule{}).Evaluate(rctx)
	if len(fs) != 1 || fs[0].Severity != model.SeverityHigh {
		t.Fatalf("PIDPressure = %+v", fs)
	}
	// 引擎级后处理：PIDPressure 节点上的 Pod 异常应被标记 Correlated。
	reg := NewRegistry()
	reg.Register(CrashLoopBackOffRule{})
	reg.Register(NodePIDPressureRule{})
	findings := NewEngine(reg).Evaluate(context.Background(), snap, rctx.Index, model.ScanOptions{})
	var podF *model.Finding
	for _, f := range findings {
		if f.Rule == "CrashLoopBackOff" {
			podF = f
		}
	}
	if podF == nil || !podF.Correlated {
		t.Fatalf("web-1 应被标记 Correlated: %+v", podF)
	}
}

// TestPVFailedRule PV 异常：Failed=High，Released=Low，Bound 不报。
func TestPVFailedRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Storage: []model.StorageInfo{
			{Ref: model.ResourceRef{Kind: "PV", Name: "pv-failed"}, Kind: "PV", Status: "Failed", Reason: "VolumeDeleted"},
			{Ref: model.ResourceRef{Kind: "PV", Name: "pv-released"}, Kind: "PV", Status: "Released"},
			{Ref: model.ResourceRef{Kind: "PV", Name: "pv-bound"}, Kind: "PV", Status: "Bound"},
		},
	}
	fs := (PVFailedRule{}).Evaluate(ctxFor(snap))
	if len(fs) != 2 {
		t.Fatalf("PVFailed findings = %d, want 2", len(fs))
	}
	sevByName := map[string]model.Severity{}
	for _, f := range fs {
		sevByName[f.Resource.Name] = f.Severity
	}
	if sevByName["pv-failed"] != model.SeverityHigh {
		t.Fatalf("Failed 应为 HIGH: %s", sevByName["pv-failed"])
	}
	if sevByName["pv-released"] != model.SeverityLow {
		t.Fatalf("Released 应为 LOW: %s", sevByName["pv-released"])
	}
}

// TestServiceNoEndpointGracePeriod 滚动发布宽限：匹配 Pod 全新（<2min）时跳过，混合或无匹配仍上报。
func TestServiceNoEndpointGracePeriod(t *testing.T) {
	mk := func(podAges ...time.Duration) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
			Services: []model.ServiceInfo{
				{Ref: model.ResourceRef{Kind: "Service", Namespace: "default", Name: "web-svc"}, Selector: map[string]string{"app": "web"}},
			},
		}
		for i, age := range podAges {
			snap.Pods = append(snap.Pods, model.PodInfo{
				Ref:               model.ResourceRef{Kind: "Pod", Namespace: "default", Name: fmt.Sprintf("web-%d", i)},
				Labels:            map[string]string{"app": "web"},
				CreationTimestamp: time.Now().Add(-age).UTC().Format(time.RFC3339),
			})
		}
		return ctxFor(snap)
	}
	// 全部 Pod 为 2min 内新建：滚动发布暂态，跳过。
	if fs := (ServiceNoEndpointRule{}).Evaluate(mk(30*time.Second, 1*time.Minute)); len(fs) != 0 {
		t.Fatalf("全新 Pod 应处于宽限期: %d", len(fs))
	}
	// 存在旧 Pod：非暂态，上报。
	if fs := (ServiceNoEndpointRule{}).Evaluate(mk(30*time.Second, 10*time.Minute)); len(fs) != 1 {
		t.Fatalf("新旧混合应上报: %d", len(fs))
	}
	// 无匹配 Pod：selector 失配，仍上报。
	if fs := (ServiceNoEndpointRule{}).Evaluate(mk()); len(fs) != 1 {
		t.Fatalf("无匹配 Pod 应上报: %d", len(fs))
	}
}

// TestJobFailedLogTargets JobFailed 通过关联索引反查下属 Pod 作为日志采集目标。
func TestJobFailedLogTargets(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "prod"}}},
		Workloads: []model.WorkloadInfo{
			{Ref: model.ResourceRef{Kind: "Job", Namespace: "prod", Name: "migrate"}, Conditions: []model.ConditionInfo{{Type: "Failed", Status: "True", Reason: "BackoffLimitExceeded"}}},
		},
		Pods: []model.PodInfo{{
			Ref:       model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "migrate-x1"},
			OwnerRefs: []model.ResourceRef{{Kind: "Job", Namespace: "prod", Name: "migrate"}},
		}},
	}
	rctx := ctxFor(snap)
	fs := (JobFailedRule{}).Evaluate(rctx)
	if len(fs) != 1 {
		t.Fatalf("JobFailed findings = %d, want 1", len(fs))
	}
	reg := NewRegistry()
	reg.Register(JobFailedRule{})
	targets := LogTargets(reg, fs, rctx.Index)
	if len(targets) != 1 || targets[0].Name != "migrate-x1" {
		t.Fatalf("Job→Pod 反查目标 = %+v", targets)
	}
}
