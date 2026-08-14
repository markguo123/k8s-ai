package correlation

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// buildTestSnapshot 构造包含三条关联链的最小快照。
func buildTestSnapshot() *model.ClusterSnapshot {
	return &model.ClusterSnapshot{
		Pods: []model.PodInfo{
			{
				Ref:      model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "pod-uid-1"},
				NodeName: "node-1",
				OwnerRefs: []model.ResourceRef{
					{Kind: "ReplicaSet", Namespace: "prod", Name: "web-rs"},
				},
				Labels: map[string]string{"app": "web"},
				PVCRefs: []model.ResourceRef{
					{Kind: "PVC", Namespace: "prod", Name: "web-data"},
				},
			},
			{
				Ref:      model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "worker-0"},
				NodeName: "node-2",
				Labels:   map[string]string{"app": "worker"},
			},
		},
		Workloads: []model.WorkloadInfo{
			{
				Ref: model.ResourceRef{Kind: "ReplicaSet", Namespace: "prod", Name: "web-rs"},
				OwnerRefs: []model.ResourceRef{
					{Kind: "Deployment", Namespace: "prod", Name: "web-dep"},
				},
			},
			{
				Ref: model.ResourceRef{Kind: "Deployment", Namespace: "prod", Name: "web-dep"},
			},
		},
		Nodes: []model.NodeInfo{
			{Ref: model.ResourceRef{Kind: "Node", Name: "node-1"}},
			{Ref: model.ResourceRef{Kind: "Node", Name: "node-2"}},
		},
		Services: []model.ServiceInfo{
			{
				Ref:      model.ResourceRef{Kind: "Service", Namespace: "prod", Name: "web-svc"},
				Selector: map[string]string{"app": "web"},
			},
		},
		EndpointSlices: []model.EndpointSliceInfo{
			{
				Ref:         model.ResourceRef{Kind: "EndpointSlice", Namespace: "prod", Name: "web-svc-abc"},
				ServiceName: "web-svc",
				TargetPods:  []model.ResourceRef{{Kind: "Pod", Namespace: "prod", Name: "web-0"}},
			},
		},
		Storage: []model.StorageInfo{
			{
				Ref:        model.ResourceRef{Kind: "PVC", Namespace: "prod", Name: "web-data"},
				Kind:       "PVC",
				VolumeName: "pv-1",
			},
			{
				Ref:              model.ResourceRef{Kind: "PV", Name: "pv-1"},
				Kind:             "PV",
				StorageClassName: "fast",
			},
			{
				Ref:  model.ResourceRef{Kind: "StorageClass", Name: "fast"},
				Kind: "StorageClass",
			},
		},
		EventsIndex: map[string][]model.EventInfo{
			"pod-uid-1": {{
				Reason:         "BackOff",
				Type:           "Warning",
				InvolvedObject: model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "pod-uid-1"},
			}},
		},
	}
}

// TestOwnerChain 验证 Pod → ReplicaSet → Deployment 归属链与反查。
func TestOwnerChain(t *testing.T) {
	idx := Build(buildTestSnapshot())
	pod := idx.Pods["Pod/prod/web-0"]
	if pod == nil {
		t.Fatal("pod 未入索引")
	}
	chain := idx.OwnerChain(pod)
	if len(chain) != 2 {
		t.Fatalf("归属链长度 = %d, want 2", len(chain))
	}
	if chain[0].Ref.Name != "web-rs" || chain[1].Ref.Name != "web-dep" {
		t.Fatalf("归属链顺序异常: %+v", chain)
	}
	// Pod 应同时挂在 RS 与 Deployment 下（间接归属）。
	if got := len(idx.PodsOfWorkload("ReplicaSet/prod/web-rs")); got != 1 {
		t.Fatalf("RS pods = %d, want 1", got)
	}
	if got := len(idx.PodsOfWorkload("Deployment/prod/web-dep")); got != 1 {
		t.Fatalf("Deployment pods = %d, want 1", got)
	}
	// Node 关联。
	if idx.Node("node-1") == nil {
		t.Fatal("node-1 未入索引")
	}
	if got := len(idx.PodsOfNode("node-1")); got != 1 {
		t.Fatalf("node-1 pods = %d, want 1", got)
	}
}

// TestStorageChain 验证 Pod → PVC → PV → StorageClass 关联链。
func TestStorageChain(t *testing.T) {
	idx := Build(buildTestSnapshot())
	pod := idx.Pods["Pod/prod/web-0"]
	chain := idx.StorageChain(pod)
	if len(chain) != 3 {
		t.Fatalf("存储链长度 = %d, want 3 (PVC/PV/SC)", len(chain))
	}
	if chain[0].Kind != "PVC" || chain[1].Kind != "PV" || chain[2].Kind != "StorageClass" {
		t.Fatalf("存储链类型异常: %+v", chain)
	}
	if got := len(idx.PodsOfPVC("PVC/prod/web-data")); got != 1 {
		t.Fatalf("PVC pods = %d, want 1", got)
	}
}

// TestServiceChain 验证 Service → EndpointSlice → Pod 与 selector 匹配。
func TestServiceChain(t *testing.T) {
	idx := Build(buildTestSnapshot())
	svcKey := "Service/prod/web-svc"
	if got := len(idx.SlicesOfService(svcKey)); got != 1 {
		t.Fatalf("endpoint slices = %d, want 1", got)
	}
	pods := idx.PodsOfService(svcKey)
	if len(pods) != 1 || pods[0].Ref.Name != "web-0" {
		t.Fatalf("selector 匹配 pod = %+v, want web-0", pods)
	}
	// worker-0 标签不匹配，不应出现在该 Service 下。
	if got := len(idx.PodsOfService("Service/prod/web-svc")); got != 1 {
		t.Fatalf("多余匹配: %d", got)
	}
	svcs := idx.ServicesOfPod("Pod/prod/web-0")
	if len(svcs) != 1 || svcs[0].Ref.Name != "web-svc" {
		t.Fatalf("pod 的 service = %+v, want web-svc", svcs)
	}
}

// TestEventsFor 验证事件按 UID 与 key 查询。
func TestEventsFor(t *testing.T) {
	idx := Build(buildTestSnapshot())
	evs := idx.EventsFor(model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0", UID: "pod-uid-1"})
	if len(evs) != 1 || evs[0].Reason != "BackOff" {
		t.Fatalf("events = %+v, want BackOff", evs)
	}
	// 无 UID 时按 key 兜底。
	evs2 := idx.EventsFor(model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"})
	if len(evs2) != 1 {
		t.Fatalf("key 查询 events = %d, want 1", len(evs2))
	}
}

// TestResourceRefKey 验证内部键格式。
func TestResourceRefKey(t *testing.T) {
	r := model.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-0"}
	if r.Key() != "Pod/prod/web-0" {
		t.Fatalf("key = %q", r.Key())
	}
	pv := model.ResourceRef{Kind: "PV", Name: "pv-1"}
	if pv.Key() != "PV//pv-1" {
		t.Fatalf("cluster-scoped key = %q", pv.Key())
	}
}
