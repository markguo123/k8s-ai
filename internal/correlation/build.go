// Package correlation 构建资源关联索引（FR-012）。
// 索引在 Phase1 之后、规则引擎之前构建，供分级、去重与证据补全复用。
package correlation

import (
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Build 从 Phase1 快照构建关联索引，覆盖三条关联链：
//  1. Pod → Owner(Workload) → Node
//  2. Pod → PVC → PV → StorageClass
//  3. Service → EndpointSlice → Pod
func Build(snap *model.ClusterSnapshot) *model.CorrelationIndex {
	idx := &model.CorrelationIndex{
		Namespaces:      map[string]*model.NamespaceInfo{},
		Pods:            map[string]*model.PodInfo{},
		Workloads:       map[string]*model.WorkloadInfo{},
		Nodes:           map[string]*model.NodeInfo{},
		Services:        map[string]*model.ServiceInfo{},
		Endpoints:       map[string]*model.EndpointSliceInfo{},
		Storage:         map[string]*model.StorageInfo{},
		PodsByNode:      map[string][]*model.PodInfo{},
		PodsByWorkload:  map[string][]*model.PodInfo{},
		PodsByService:   map[string][]*model.PodInfo{},
		PodsByPVC:       map[string][]*model.PodInfo{},
		ServicesByPod:   map[string][]*model.ServiceInfo{},
		PVByPVC:         map[string]*model.StorageInfo{},
		SCByPV:          map[string]*model.StorageInfo{},
		SlicesByService: map[string][]*model.EndpointSliceInfo{},
		EventsByUID:     map[string][]model.EventInfo{},
		EventsByKey:     map[string][]model.EventInfo{},
	}

	// 1. 基础索引：各类资源按 key（kind/ns/name）入表。
	for i := range snap.Namespaces {
		idx.Namespaces[snap.Namespaces[i].Ref.Key()] = &snap.Namespaces[i]
	}
	for i := range snap.Pods {
		idx.Pods[snap.Pods[i].Ref.Key()] = &snap.Pods[i]
	}
	for i := range snap.Workloads {
		idx.Workloads[snap.Workloads[i].Ref.Key()] = &snap.Workloads[i]
	}
	for i := range snap.Nodes {
		idx.Nodes[snap.Nodes[i].Ref.Key()] = &snap.Nodes[i]
	}
	for i := range snap.Services {
		idx.Services[snap.Services[i].Ref.Key()] = &snap.Services[i]
	}
	for i := range snap.EndpointSlices {
		idx.Endpoints[snap.EndpointSlices[i].Ref.Key()] = &snap.EndpointSlices[i]
	}
	for i := range snap.Storage {
		idx.Storage[snap.Storage[i].Ref.Key()] = &snap.Storage[i]
	}

	// 2. Pod → Node。
	for _, p := range idx.Pods {
		if p.NodeName != "" {
			idx.PodsByNode[p.NodeName] = append(idx.PodsByNode[p.NodeName], p)
		}
	}

	// 3. Pod → Workload：沿 ownerReferences 上溯（Pod → RS → Deployment），
	//    让 Pod 同时挂到直接 owner 与所有祖先 workload 下。
	for _, p := range idx.Pods {
		seen := map[string]bool{}
		queue := make([]model.ResourceRef, len(p.OwnerRefs))
		copy(queue, p.OwnerRefs)
		for len(queue) > 0 {
			ref := queue[0]
			queue = queue[1:]
			if seen[ref.Key()] {
				continue
			}
			seen[ref.Key()] = true
			if w := idx.Workloads[ref.Key()]; w != nil {
				idx.PodsByWorkload[ref.Key()] = append(idx.PodsByWorkload[ref.Key()], p)
				queue = append(queue, w.OwnerRefs...)
			}
		}
	}

	// 4. Pod → PVC → PV → StorageClass。
	for _, p := range idx.Pods {
		for _, pvcRef := range p.PVCRefs {
			pvc := idx.Storage[pvcRef.Key()]
			if pvc == nil || pvc.Kind != "PVC" {
				continue
			}
			idx.PodsByPVC[pvcRef.Key()] = append(idx.PodsByPVC[pvcRef.Key()], p)
			if pvc.VolumeName == "" {
				continue
			}
			pv := findStorageByName(idx.Storage, "PV", pvc.VolumeName)
			if pv == nil {
				continue
			}
			idx.PVByPVC[pvcRef.Key()] = pv
			if pv.StorageClassName != "" {
				if sc := findStorageByName(idx.Storage, "StorageClass", pv.StorageClassName); sc != nil {
					idx.SCByPV[pv.Ref.Key()] = sc
				}
			}
		}
	}

	// 5. Service → EndpointSlice：按 service-name 标签归属。
	for _, es := range idx.Endpoints {
		if es.ServiceName == "" {
			continue
		}
		svcKey := model.ResourceRef{Kind: "Service", Namespace: es.Ref.Namespace, Name: es.ServiceName}.Key()
		idx.SlicesByService[svcKey] = append(idx.SlicesByService[svcKey], es)
	}

	// 6. Service selector ↔ Pod labels（双向索引）。
	for _, svc := range idx.Services {
		if len(svc.Selector) == 0 {
			continue
		}
		for _, p := range idx.Pods {
			if labelsMatch(svc.Selector, p.Labels) {
				idx.PodsByService[svc.Ref.Key()] = append(idx.PodsByService[svc.Ref.Key()], p)
				idx.ServicesByPod[p.Ref.Key()] = append(idx.ServicesByPod[p.Ref.Key()], svc)
			}
		}
	}

	// 7. Events：按 UID 与 kind/ns/name 双键索引，供规则与证据 O(1) 查询。
	for uid, evs := range snap.EventsIndex {
		idx.EventsByUID[uid] = evs
	}
	for _, evs := range idx.EventsByUID {
		for _, e := range evs {
			k := e.InvolvedObject.Key()
			idx.EventsByKey[k] = append(idx.EventsByKey[k], e)
		}
	}
	return idx
}

// findStorageByName 按 kind 与名称查找存储资源（PV/SC 为集群级资源）。
func findStorageByName(storage map[string]*model.StorageInfo, kind, name string) *model.StorageInfo {
	return storage[model.ResourceRef{Kind: kind, Name: name}.Key()]
}

// labelsMatch 判断 Pod 标签是否完全匹配 Service selector。
func labelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
