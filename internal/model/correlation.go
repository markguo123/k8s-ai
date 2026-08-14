package model

// CorrelationIndex 是 Phase1 快照之上的关联索引（FR-012）。
// 由 correlation.Build 构建，规则引擎/评分/诊断通过查询方法复用；
// 本类型只依赖标准库，保持领域纯净。
type CorrelationIndex struct {
	Namespaces map[string]*NamespaceInfo
	Pods       map[string]*PodInfo
	Workloads  map[string]*WorkloadInfo
	Nodes      map[string]*NodeInfo
	Services   map[string]*ServiceInfo
	Endpoints  map[string]*EndpointSliceInfo
	Storage    map[string]*StorageInfo

	PodsByNode      map[string][]*PodInfo
	PodsByWorkload  map[string][]*PodInfo
	PodsByService   map[string][]*PodInfo
	PodsByPVC       map[string][]*PodInfo
	ServicesByPod   map[string][]*ServiceInfo
	PVByPVC         map[string]*StorageInfo
	SCByPV          map[string]*StorageInfo
	SlicesByService map[string][]*EndpointSliceInfo
	EventsByUID     map[string][]EventInfo
	EventsByKey     map[string][]EventInfo
}

// Namespace 按名称返回命名空间。
func (ix *CorrelationIndex) Namespace(name string) *NamespaceInfo {
	return ix.Namespaces["Namespace//"+name]
}

// Pod 按 key（kind/ns/name）返回 Pod。
func (ix *CorrelationIndex) Pod(key string) *PodInfo {
	return ix.Pods[key]
}

// Node 按节点名返回节点。
func (ix *CorrelationIndex) Node(name string) *NodeInfo {
	return ix.Nodes[ResourceRef{Kind: "Node", Name: name}.Key()]
}

// PodsOfNode 返回运行在该节点上的 Pod。
func (ix *CorrelationIndex) PodsOfNode(name string) []*PodInfo {
	return ix.PodsByNode[name]
}

// OwnerChain 返回 Pod 的归属链（直接 owner 在前，如 ReplicaSet → Deployment）。
func (ix *CorrelationIndex) OwnerChain(p *PodInfo) []*WorkloadInfo {
	var chain []*WorkloadInfo
	seen := map[string]bool{}
	queue := make([]ResourceRef, len(p.OwnerRefs))
	copy(queue, p.OwnerRefs)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		w := ix.Workloads[ref.Key()]
		if w == nil || seen[w.Ref.Key()] {
			continue
		}
		seen[w.Ref.Key()] = true
		chain = append(chain, w)
		queue = append(queue, w.OwnerRefs...)
	}
	return chain
}

// PodsOfWorkload 返回直接或间接属于该 workload（key）的 Pod。
func (ix *CorrelationIndex) PodsOfWorkload(key string) []*PodInfo {
	return ix.PodsByWorkload[key]
}

// PodsOfService 返回 selector 匹配该 Service 的 Pod。
func (ix *CorrelationIndex) PodsOfService(key string) []*PodInfo {
	return ix.PodsByService[key]
}

// SlicesOfService 返回属于该 Service 的 EndpointSlice。
func (ix *CorrelationIndex) SlicesOfService(key string) []*EndpointSliceInfo {
	return ix.SlicesByService[key]
}

// ServicesOfPod 返回选中该 Pod 的 Service。
func (ix *CorrelationIndex) ServicesOfPod(key string) []*ServiceInfo {
	return ix.ServicesByPod[key]
}

// PodsOfPVC 返回使用该 PVC 的 Pod。
func (ix *CorrelationIndex) PodsOfPVC(key string) []*PodInfo {
	return ix.PodsByPVC[key]
}

// StorageChain 返回 Pod 的存储链（PVC → PV → StorageClass），可能为空。
func (ix *CorrelationIndex) StorageChain(p *PodInfo) []*StorageInfo {
	var chain []*StorageInfo
	for _, pvcRef := range p.PVCRefs {
		pvc := ix.Storage[pvcRef.Key()]
		if pvc == nil || pvc.Kind != "PVC" {
			continue
		}
		chain = append(chain, pvc)
		if pv := ix.PVByPVC[pvcRef.Key()]; pv != nil {
			chain = append(chain, pv)
			if sc := ix.SCByPV[pv.Ref.Key()]; sc != nil {
				chain = append(chain, sc)
			}
		}
	}
	return chain
}

// EventsFor 按 UID（优先）或 kind/ns/name 返回关联事件。
func (ix *CorrelationIndex) EventsFor(ref ResourceRef) []EventInfo {
	if evs := ix.EventsByUID[ref.UID]; len(evs) > 0 {
		return evs
	}
	return ix.EventsByKey[ref.Key()]
}
