package incident

import (
	"strings"
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

func TestEvidenceRefsUseFindingQualifiedIdentity(t *testing.T) {
	root := model.Finding{
		ID: "root-finding", Rule: "Unhealthy", Severity: model.SeverityMedium,
		Title: "探针失败", Resource: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"},
		Evidence: []model.Evidence{{ID: "E1", Key: "ready", Value: "false"}},
	}
	member := model.Finding{
		ID: "restart-finding", Rule: "HighRestartCount", Severity: model.SeverityLow,
		Title: "高频重启", Resource: root.Resource,
		Evidence: []model.Evidence{{ID: "E1", Key: "restartCount", Value: "36"}},
	}
	incs := []model.Incident{{
		ID: root.ID, Title: root.Title, Severity: model.SeverityMedium,
		Root:    model.FindingRef{ID: root.ID, Rule: root.Rule, Resource: root.Resource},
		Members: []model.FindingRef{{ID: member.ID, Rule: member.Rule, Resource: member.Resource}},
	}}
	Relate([]model.Finding{root, member}, incs, nil, nil)

	for _, ref := range incs[0].EvidenceRefs {
		if !strings.Contains(ref, "#E") {
			t.Fatalf("Incident 证据引用必须包含 Finding ID：%v", incs[0].EvidenceRefs)
		}
	}
	if len(incs[0].CausalEdges) == 0 {
		t.Fatal("同对象异常应建立因果边")
	}
	for _, ref := range incs[0].CausalEdges[0].EvidenceRefs {
		if !strings.Contains(ref, "#E") {
			t.Fatalf("CausalEdge 证据引用必须包含 Finding ID：%+v", incs[0].CausalEdges[0].EvidenceRefs)
		}
	}
}

func TestAssignFindingRolesFromIncidents(t *testing.T) {
	findings := []*model.Finding{
		{ID: "anchor", Rule: "CrashLoopBackOff"},
		{ID: "impact", Rule: "DeploymentReplica"},
		{ID: "symptom", Rule: "HighRestartCount"},
		{ID: "advisory", Rule: "NetworkPolicyNoDefaultDeny", Suggestion: true},
		{ID: "independent", Rule: "HpaReplicasMismatch"},
	}
	incs := []model.Incident{
		{
			ID:      "anchor",
			Root:    model.FindingRef{ID: "anchor"},
			Members: []model.FindingRef{{ID: "impact"}, {ID: "symptom"}},
		},
		{
			ID:          "advisory",
			Root:        model.FindingRef{ID: "advisory"},
			Independent: true,
		},
	}
	AssignRoles(findings, incs)

	want := map[string]model.FindingRole{
		"anchor":      model.FindingRoleAnchor,
		"impact":      model.FindingRoleImpact,
		"symptom":     model.FindingRoleSymptom,
		"advisory":    model.FindingRoleAdvisory,
		"independent": model.FindingRoleIndependent,
	}
	for _, finding := range findings {
		if finding.Role != want[finding.ID] {
			t.Errorf("%s role = %s, want %s", finding.ID, finding.Role, want[finding.ID])
		}
	}
}

func TestRelateKeepsOneExplainingEdgePerSymptom(t *testing.T) {
	res := model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"}
	findings := []model.Finding{
		{ID: "unhealthy", Rule: "Unhealthy", Resource: res},
		{ID: "crash", Rule: "CrashLoopBackOff", Resource: res},
		{ID: "restart", Rule: "HighRestartCount", Resource: res},
	}
	incs := []model.Incident{{
		ID: "unhealthy", Root: model.FindingRef{ID: "unhealthy", Rule: "Unhealthy", Resource: res},
		Members: []model.FindingRef{
			{ID: "crash", Rule: "CrashLoopBackOff", Resource: res},
			{ID: "restart", Rule: "HighRestartCount", Resource: res},
		},
	}}
	Relate(findings, incs, nil, nil)

	targets := map[string]int{}
	for _, edge := range incs[0].CausalEdges {
		targets[edge.TargetFinding]++
	}
	for target, count := range targets {
		if count != 1 {
			t.Fatalf("symptom %s 应只保留一条最能解释它的边，实际 %d：%+v", target, count, incs[0].CausalEdges)
		}
	}
	if len(incs[0].CausalEdges) != 2 {
		t.Fatalf("两个症状应生成两棵传播边，实际 %+v", incs[0].CausalEdges)
	}
}
