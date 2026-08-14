package rule

import (
	"context"
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
	targets := LogTargets(reg, fs)
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
