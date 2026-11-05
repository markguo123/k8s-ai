package relations

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

func ref(kind, ns, name string) model.ResourceRef {
	return model.ResourceRef{Kind: kind, Namespace: ns, Name: name}
}

// fullSnapshot 覆盖全部 14 种关系类型：一张图把每种边都建出来，
// 后续用例按类型逐一核对「边存在 + 出处非空」。
func fullSnapshot() *model.ClusterSnapshot {
	pod := model.PodInfo{
		Ref:      model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-1", UID: "uid-web-1"},
		NodeName: "node-1",
		Labels:   map[string]string{"app": "web"},
		OwnerRefs: []model.ResourceRef{
			ref("ReplicaSet", "prod", "web-abc"),
		},
		PVCRefs:    []model.ResourceRef{ref("PVC", "prod", "data-web-1")},
		ConfigMaps: []model.ResourceRef{ref("ConfigMap", "prod", "web-conf")},
		SecretRefs: []model.ResourceRef{ref("Secret", "prod", "web-cred")},
	}
	rs := model.WorkloadInfo{
		Ref:       ref("ReplicaSet", "prod", "web-abc"),
		OwnerRefs: []model.ResourceRef{ref("Deployment", "prod", "web")},
	}
	return &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: ref("Namespace", "", "prod")}},
		Nodes:      []model.NodeInfo{{Ref: ref("Node", "", "node-1")}},
		Pods:       []model.PodInfo{pod},
		Workloads:  []model.WorkloadInfo{rs, {Ref: ref("Deployment", "prod", "web")}},
		Services: []model.ServiceInfo{
			{Ref: ref("Service", "prod", "web"), Selector: map[string]string{"app": "web"}},
		},
		EndpointSlices: []model.EndpointSliceInfo{{
			Ref:         ref("EndpointSlice", "prod", "web-abc12"),
			ServiceName: "web",
			TargetPods:  []model.ResourceRef{pod.Ref},
		}},
		Ingresses: []model.IngressInfo{{
			Ref:      ref("Ingress", "prod", "web-ing"),
			Backends: []string{"prod/web:80"},
		}},
		NetworkPolicies: []model.NetworkPolicyInfo{{
			Ref:         ref("NetworkPolicy", "prod", "deny-all"),
			PodSelector: map[string]string{"app": "web"},
		}},
		Hpas: []model.HpaInfo{{
			Ref:         ref("HPA", "prod", "web-hpa"),
			ScaleTarget: "Deployment/web",
		}},
		Pdbs: []model.PdbInfo{{
			Ref:      ref("PDB", "prod", "web-pdb"),
			Selector: map[string]string{"app": "web"},
		}},
		Storage: []model.StorageInfo{
			{Ref: ref("PVC", "prod", "data-web-1"), Kind: "PVC",
				VolumeName: "pv-web-1", StorageClassName: "fast-ssd"},
			{Ref: ref("PV", "", "pv-web-1"), Kind: "PV", StorageClassName: "fast-ssd"},
			{Ref: ref("StorageClass", "", "fast-ssd"), Kind: "StorageClass", Driver: "csi.example.com"},
		},
	}
}

func built(t *testing.T) *Graph {
	t.Helper()
	snap := fullSnapshot()
	g := Build(snap, correlation.Build(snap))
	if g.EdgeCount() == 0 {
		t.Fatal("关系图为空，夹具未生效")
	}
	return g
}

// TestBuild_EveryRelationTypeHasProvenance 逐类型核对边与出处。
func TestBuild_EveryRelationTypeHasProvenance(t *testing.T) {
	g := built(t)
	want := []RelationType{
		OwnedBy, RunsOn, SelectedBy, HasEndpoint, EndpointOf, RoutesTo,
		Mounts, BoundTo, ProvisionedBy, Targets, Protects, AppliesTo,
		ConfigRef, SecretRef,
	}
	counts := g.KindCounts()
	for _, rt := range want {
		if counts[rt] == 0 {
			t.Errorf("关系类型 %s 未建边", rt)
		}
	}
	for _, e := range g.All() {
		if e.Prov.Source == "" || e.Prov.Field == "" {
			t.Errorf("边缺少出处: %s", e.String())
		}
		if e.From.Name == "" || e.To.Name == "" {
			t.Errorf("边缺少端点: %s", e.String())
		}
		if e.From.Key() == e.To.Key() {
			t.Errorf("存在自环边: %s", e.String())
		}
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total != g.EdgeCount() || len(g.All()) != g.EdgeCount() {
		t.Fatalf("边数不自洽: kindSum=%d EdgeCount=%d All=%d", total, g.EdgeCount(), len(g.All()))
	}
}

// TestBuild_OwnerReferenceChain Pod→ReplicaSet→Deployment 两级 owner 都要成边。
func TestBuild_OwnerReferenceChain(t *testing.T) {
	g := built(t)
	pod := ref("Pod", "prod", "web-1")
	rs := ref("ReplicaSet", "prod", "web-abc")
	dep := ref("Deployment", "prod", "web")

	e, ok := g.DirectEdge(pod, rs)
	if !ok || e.Type != OwnedBy || e.Prov.Source != SrcOwnerReference {
		t.Fatalf("Pod→ReplicaSet owner 边缺失: %+v", e)
	}
	if _, ok := g.DirectEdge(rs, dep); !ok {
		t.Fatal("ReplicaSet→Deployment owner 边缺失")
	}
	// 跨两跳可达：这是「Deployment 副本不足由 Pod 故障引起」能成立的前提。
	if !g.Connected(pod, dep) {
		t.Fatal("Pod 与 Deployment 应通过 owner 链连通")
	}
	path, ok := g.Path(pod, dep)
	if !ok || len(path) != 2 {
		t.Fatalf("owner 链路径 = %v ok=%v, want 2 跳", path, ok)
	}
}

// TestBuild_NoNameSimilarityEdge 名称前缀相似不得建边（需求 §3.1）。
func TestBuild_NoNameSimilarityEdge(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: ref("Namespace", "", "prod")}},
		Pods: []model.PodInfo{{
			Ref:   ref("Pod", "prod", "web-def"),
			Phase: "Running",
			// 无 ownerReferences：名字像 web Deployment 的 Pod，在图上必须是孤立节点。
		}},
		Workloads: []model.WorkloadInfo{{Ref: ref("Deployment", "prod", "web")}},
	}
	g := Build(snap, correlation.Build(snap))
	if g.EdgeCount() != 0 {
		t.Fatalf("按名称相似度建了边: %+v", g.All())
	}
	if g.Connected(ref("Pod", "prod", "web-def"), ref("Deployment", "prod", "web")) {
		t.Fatal("名称相似被判定为有关系")
	}
}

// TestBuild_DanglingReferencesProduceNoEdge 引用不存在的对象时不建悬空边，
// 缺失由规则说明（NETWORK_INGRESSBACKEND / WORKLOAD_HPASCALETARGETMISSING）。
func TestBuild_DanglingReferencesProduceNoEdge(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: ref("Namespace", "", "prod")}},
		Pods: []model.PodInfo{{
			Ref:      ref("Pod", "prod", "web-1"),
			NodeName: "node-ghost", // 节点未被采集
		}},
		Hpas: []model.HpaInfo{{
			Ref:         ref("HPA", "prod", "web-hpa"),
			ScaleTarget: "Deployment/ghost",
		}},
		Ingresses: []model.IngressInfo{{
			Ref:      ref("Ingress", "prod", "web-ing"),
			Backends: []string{"prod/ghost-svc:80"},
		}},
	}
	g := Build(snap, correlation.Build(snap))
	if g.EdgeCount() != 0 {
		t.Fatalf("悬空引用被建成边: %+v", g.All())
	}
}

// TestBuild_MountAndStorageChain Pod→PVC→PV→StorageClass 挂载链完整。
func TestBuild_MountAndStorageChain(t *testing.T) {
	g := built(t)
	pod := ref("Pod", "prod", "web-1")
	pvc := ref("PVC", "prod", "data-web-1")
	sc := ref("StorageClass", "", "fast-ssd")

	if e, ok := g.DirectEdge(pod, pvc); !ok || e.Type != Mounts {
		t.Fatalf("Pod→PVC 挂载边缺失: %+v", e)
	}
	if e, ok := g.DirectEdge(pvc, ref("PV", "", "pv-web-1")); !ok || e.Type != BoundTo {
		t.Fatalf("PVC→PV 绑定边缺失: %+v", e)
	}
	if e, ok := g.DirectEdge(pvc, sc); !ok || e.Type != ProvisionedBy {
		t.Fatalf("PVC→StorageClass 边缺失: %+v", e)
	}
	// StorageClass 故障能否解释 Pod 挂不上盘，取决于这条路径存在。
	if !g.Connected(pod, sc) {
		t.Fatal("Pod 与 StorageClass 应通过挂载链连通")
	}
}

// TestBuild_IngressToPodPath Ingress 经 Service/EndpointSlice 两跳到 Pod。
func TestBuild_IngressToPodPath(t *testing.T) {
	g := built(t)
	ing := ref("Ingress", "prod", "web-ing")
	pod := ref("Pod", "prod", "web-1")
	if !g.Connected(ing, pod) {
		t.Fatal("Ingress 与后端 Pod 应连通（Service → EndpointSlice → Pod）")
	}
	if e, ok := g.DirectEdge(ing, ref("Service", "prod", "web")); !ok || e.Type != RoutesTo {
		t.Fatalf("Ingress→Service 边缺失: %+v", e)
	}
	// Ingress→Pod 不是直接引用，必须靠路径而不是名称。
	if _, ok := g.DirectEdge(ing, pod); ok {
		t.Fatal("Ingress 与 Pod 之间不应存在直接边")
	}
	// 最短路径 Ingress→Service→Pod（RoutesTo + SelectedBy），
	// 正好落在 internal/incident 允许的 2 跳因果窗口内。
	path, ok := g.Path(ing, pod)
	if !ok || len(path) != 2 {
		t.Fatalf("Ingress→Pod 最短路径应为 2 跳，实际 %v ok=%v", path, ok)
	}
}

// TestBuild_SelectorRelations Service selector / PDB / NetworkPolicy 三个 selector 来源。
func TestBuild_SelectorRelations(t *testing.T) {
	g := built(t)
	pod := ref("Pod", "prod", "web-1")
	for _, tc := range []struct {
		from model.ResourceRef
		typ  RelationType
	}{
		{ref("Service", "prod", "web"), SelectedBy},
		{ref("PDB", "prod", "web-pdb"), Protects},
		{ref("NetworkPolicy", "prod", "deny-all"), AppliesTo},
		{ref("EndpointSlice", "prod", "web-abc12"), HasEndpoint},
	} {
		e, ok := g.DirectEdge(tc.from, pod)
		if !ok || e.Type != tc.typ {
			t.Errorf("%s → Pod 期望 %s，实际 %+v ok=%v", tc.from.Name, tc.typ, e, ok)
			continue
		}
		if e.Prov.Source != SrcLabelSelector && e.Prov.Source != SrcSpecField && e.Prov.Source != SrcObjectLabel {
			t.Errorf("%s 的出处来源异常: %+v", tc.from.Name, e.Prov)
		}
	}
}

// TestBuild_EmptySelectorNotExpandedToWholeNamespace selector 为空不扩大解释成全命名空间命中。
func TestBuild_EmptySelectorNotExpandedToWholeNamespace(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: ref("Namespace", "", "prod")}},
		Pods:       []model.PodInfo{{Ref: ref("Pod", "prod", "web-1"), Labels: map[string]string{"app": "web"}}},
		Pdbs:       []model.PdbInfo{{Ref: ref("PDB", "prod", "pdb-all")}},
		NetworkPolicies: []model.NetworkPolicyInfo{
			{Ref: ref("NetworkPolicy", "prod", "np-all")},
		},
	}
	g := Build(snap, correlation.Build(snap))
	if g.EdgeCount() != 0 {
		t.Fatalf("空 selector 不应建边: %+v", g.All())
	}
}

// TestBuild_SecretEdgeCarriesNamesOnly Secret 关系只带名称，不带任何数据字段。
func TestBuild_SecretEdgeCarriesNamesOnly(t *testing.T) {
	g := built(t)
	edges := g.EdgesOfType(SecretRef)
	if len(edges) != 1 {
		t.Fatalf("SecretRef 边数 = %d, want 1", len(edges))
	}
	if !strings.Contains(edges[0].Prov.Field, "仅名称") {
		t.Fatalf("Secret 关系出处应声明只读名称: %+v", edges[0].Prov)
	}
	if strings.Contains(edges[0].String(), "token") {
		t.Fatal("关系描述出现了 Secret 数据")
	}
}

// TestGraph_TextFor 输出必须带出处，且超长时明确说明截断。
func TestGraph_TextFor(t *testing.T) {
	g := built(t)
	pod := ref("Pod", "prod", "web-1")
	text := g.TextFor([]model.ResourceRef{pod}, 24)
	if !strings.Contains(text, "[ownerReference:metadata.ownerReferences]") {
		t.Fatalf("关系摘要缺少出处: %q", text)
	}
	if !strings.Contains(text, string(Mounts)) {
		t.Fatalf("关系摘要缺少挂载关系: %q", text)
	}
	if truncated := g.TextFor([]model.ResourceRef{pod}, 1); !strings.Contains(truncated, "已截断") {
		t.Fatalf("截断未标注: %q", truncated)
	}
	if got := g.TextFor([]model.ResourceRef{ref("Pod", "prod", "ghost")}, 24); got != "" {
		t.Fatalf("未知资源应返回空关系: %q", got)
	}
}

// TestGraph_QueryStability All 稳定排序、In/Out 方向、Path 自反与未知节点。
func TestGraph_QueryStability(t *testing.T) {
	g := built(t)
	all := g.All()
	for i := 1; i < len(all); i++ {
		if all[i-1].From.Key() > all[i].From.Key() {
			t.Fatalf("All 未按 from 排序: %s 在 %s 之后", all[i-1].From.Key(), all[i].From.Key())
		}
	}
	pod := ref("Pod", "prod", "web-1")
	if len(g.Out(pod.Key())) == 0 {
		t.Fatal("Pod 出边为空")
	}
	svc := ref("Service", "prod", "web")
	if len(g.In(svc.Key())) == 0 {
		t.Fatal("Service 入边为空（Service 是 Pod 关系的目标，方向不能反）")
	}
	if _, ok := g.Path(pod, pod); !ok {
		t.Fatal("自身到自身应视为连通")
	}
	if _, ok := g.Path(ref("Pod", "prod", "ghost"), pod); ok {
		t.Fatal("图外节点不应连通")
	}
	// 快照未变的两次构图必须给出同样的边集（报告可复现的前提）。
	snap := fullSnapshot()
	again := Build(snap, correlation.Build(snap))
	if a, b := all, again.All(); len(a) != len(b) {
		t.Fatalf("两次构图边数不同: %d vs %d", len(a), len(b))
	} else {
		for i := range a {
			if a[i].String() != b[i].String() {
				t.Fatalf("两次构图第 %d 条边不同: %s vs %s", i, a[i], b[i])
			}
		}
	}
}

// TestBuild_NilSnapshot 构图对空输入必须安全（采集失败时不允许 panic）。
func TestBuild_NilSnapshot(t *testing.T) {
	g := Build(nil, nil)
	if g == nil || g.EdgeCount() != 0 {
		t.Fatalf("nil 快照构图异常: %+v", g)
	}
	if g.Connected(ref("Pod", "prod", "a"), ref("Pod", "prod", "b")) {
		t.Fatal("空图不应连通任何资源")
	}
	if g.TextFor(nil, 0) != "" {
		t.Fatal("空图不应输出关系文本")
	}
}

// TestParseHelpers 校验采集器归一化串的解析口径（含跨命名空间后端与缺端口）。
func TestParseHelpers(t *testing.T) {
	for _, tc := range []struct {
		in, defNS, ns, name string
		ok                  bool
	}{
		{"prod/web:80", "prod", "prod", "web", true},
		{"shared/web:443", "prod", "shared", "web", true},
		{"web:80", "prod", "prod", "web", true},
		{"web", "prod", "prod", "web", true},
		{"", "prod", "", "", false},
		{":80", "prod", "", "", false},
	} {
		ns, name, ok := parseServiceBackend(tc.in, tc.defNS)
		if ok != tc.ok || ns != tc.ns || name != tc.name {
			t.Errorf("parseServiceBackend(%q) = %q,%q,%v want %q,%q,%v", tc.in, ns, name, ok, tc.ns, tc.name, tc.ok)
		}
	}
	for _, tc := range []struct {
		in        string
		kind, nam string
		ok        bool
	}{
		{"Deployment/web", "Deployment", "web", true},
		{"StatefulSet/db", "StatefulSet", "db", true},
		{"web", "", "", false},
		{"/web", "", "", false},
		{"Deployment/", "", "", false},
	} {
		k, n, ok := parseKindName(tc.in)
		if ok != tc.ok || k != tc.kind || n != tc.nam {
			t.Errorf("parseKindName(%q) = %q,%q,%v want %q,%q,%v", tc.in, k, n, ok, tc.kind, tc.nam, tc.ok)
		}
	}
}
