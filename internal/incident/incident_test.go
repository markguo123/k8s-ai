package incident

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

func crashPod(ns, name, node string) model.PodInfo {
	return model.PodInfo{
		Ref:        model.ResourceRef{Kind: "Pod", Namespace: ns, Name: name},
		NodeName:   node,
		OwnerRefs:  []model.ResourceRef{{Kind: "ReplicaSet", Namespace: ns, Name: name + "-rs"}},
		Containers: []model.ContainerInfo{{Name: "c", State: "Waiting", Reason: "CrashLoopBackOff"}},
	}
}

// TestCrashLoopChain 验证 Pod CrashLoop + Deployment 副本不足 + Service 无 Endpoint 聚合为 1 个 Incident，根因为 Pod。
func TestCrashLoopChain(t *testing.T) {
	ns := "prod"
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: ns}}},
		Pods: []model.PodInfo{{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "web-abc", UID: "p1"},
			NodeName:   "node-1",
			OwnerRefs:  []model.ResourceRef{{Kind: "ReplicaSet", Namespace: ns, Name: "web-rs"}},
			Labels:     map[string]string{"app": "web"},
			Containers: []model.ContainerInfo{{Name: "web", State: "Waiting", Reason: "CrashLoopBackOff"}},
		}},
		Workloads: []model.WorkloadInfo{
			{Ref: model.ResourceRef{Kind: "ReplicaSet", Namespace: ns, Name: "web-rs"}, OwnerRefs: []model.ResourceRef{{Kind: "Deployment", Namespace: ns, Name: "web"}}},
			{Ref: model.ResourceRef{Kind: "Deployment", Namespace: ns, Name: "web"}},
		},
		Services: []model.ServiceInfo{{Ref: model.ResourceRef{Kind: "Service", Namespace: ns, Name: "web-svc"}, Selector: map[string]string{"app": "web"}}},
	}
	idx := correlation.Build(snap)
	findings := []model.Finding{
		{ID: "f-pod", Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "容器崩溃重启", Resource: model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "web-abc"}},
		{ID: "f-dep", Rule: "DeploymentReplica", Severity: model.SeverityHigh, Title: "副本不足", Resource: model.ResourceRef{Kind: "Deployment", Namespace: ns, Name: "web"}},
		{ID: "f-svc", Rule: "ServiceNoEndpoint", Severity: model.SeverityMedium, Title: "无 Endpoint", Resource: model.ResourceRef{Kind: "Service", Namespace: ns, Name: "web-svc"}},
	}
	incs := Build(findings, idx)
	if len(incs) != 1 {
		t.Fatalf("incidents = %d, want 1", len(incs))
	}
	if incs[0].Root.ID != "f-pod" {
		t.Fatalf("根因应为 Pod finding: %+v", incs[0].Root)
	}
	if len(incs[0].Members) != 2 {
		t.Fatalf("members = %d, want 2", len(incs[0].Members))
	}
}

// TestPVCChain 验证 PVC Pending + Pod FailedMount 聚合为 1 个 Incident，根因为 PVC。
func TestPVCChain(t *testing.T) {
	ns := "prod"
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: ns}}},
		Pods: []model.PodInfo{{
			Ref:     model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "db-0"},
			PVCRefs: []model.ResourceRef{{Kind: "PVC", Namespace: ns, Name: "data"}},
		}},
		Storage: []model.StorageInfo{{Ref: model.ResourceRef{Kind: "PVC", Namespace: ns, Name: "data"}, Kind: "PVC", Status: "Pending"}},
	}
	idx := correlation.Build(snap)
	findings := []model.Finding{
		{ID: "f-pvc", Rule: "PVCPending", Severity: model.SeverityHigh, Title: "PVC Pending", Resource: model.ResourceRef{Kind: "PVC", Namespace: ns, Name: "data"}},
		{ID: "f-pod", Rule: "FailedMount", Severity: model.SeverityMedium, Title: "挂载失败", Resource: model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "db-0"}},
	}
	incs := Build(findings, idx)
	if len(incs) != 1 || incs[0].Root.ID != "f-pvc" {
		t.Fatalf("incidents = %+v, 根因应为 PVC", incs)
	}
}

// TestNodeChain 验证 Node NotReady 与其上 Pod 聚合，根因为 Node。
func TestNodeChain(t *testing.T) {
	ns := "prod"
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: ns}}},
		Nodes:      []model.NodeInfo{{Ref: model.ResourceRef{Kind: "Node", Name: "node-1"}}},
		Pods:       []model.PodInfo{crashPod(ns, "web-1", "node-1")},
	}
	idx := correlation.Build(snap)
	findings := []model.Finding{
		{ID: "f-node", Rule: "NodeNotReady", Severity: model.SeverityCritical, Title: "节点 NotReady", Resource: model.ResourceRef{Kind: "Node", Name: "node-1"}},
		{ID: "f-pod", Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "崩溃", Resource: model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "web-1"}},
	}
	incs := Build(findings, idx)
	if len(incs) != 1 || incs[0].Root.ID != "f-node" {
		t.Fatalf("incidents = %+v, 根因应为 Node", incs)
	}
}

// TestIndependentIncidents 验证无关联的故障保持独立。
func TestIndependentIncidents(t *testing.T) {
	ns := "prod"
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: ns}}},
		Pods:       []model.PodInfo{crashPod(ns, "a-1", "node-1"), crashPod(ns, "b-1", "node-2")},
	}
	idx := correlation.Build(snap)
	findings := []model.Finding{
		{ID: "f-a", Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "a", Resource: model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "a-1"}},
		{ID: "f-b", Rule: "CrashLoopBackOff", Severity: model.SeverityHigh, Title: "b", Resource: model.ResourceRef{Kind: "Pod", Namespace: ns, Name: "b-1"}},
	}
	if incs := Build(findings, idx); len(incs) != 2 {
		t.Fatalf("incidents = %d, want 2", len(incs))
	}
}
