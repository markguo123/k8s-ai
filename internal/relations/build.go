package relations

import (
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Build 从 Phase1 快照与关联索引构建资源关系图。
//
// 所有边都满足两条判据：
//  1. 关系写在 Kubernetes 对象字段里（ownerReferences / selector / spec 引用 / status 引用）；
//  2. 目标资源确实在本次采集快照里存在（引用了不存在的对象时不建边，
//     这类缺失由 STORAGE_STORAGECLASSNOPROVISIONER / NETWORK_INGRESSBACKEND 等规则说明）。
func Build(snap *model.ClusterSnapshot, idx *model.CorrelationIndex) *Graph {
	g := newGraph()
	if snap == nil {
		return g
	}
	if idx == nil {
		idx = &model.CorrelationIndex{}
	}

	// 1. ownerReferences：Pod/Workload 的归属链（Pod→ReplicaSet→Deployment 等）。
	for i := range snap.Pods {
		p := &snap.Pods[i]
		for _, o := range p.OwnerRefs {
			g.add(p.Ref, o, OwnedBy, Provenance{SrcOwnerReference, "metadata.ownerReferences"})
		}
	}
	for i := range snap.Workloads {
		w := &snap.Workloads[i]
		for _, o := range w.OwnerRefs {
			g.add(w.Ref, o, OwnedBy, Provenance{SrcOwnerReference, "metadata.ownerReferences"})
		}
	}

	// 2. Pod → Node（spec.nodeName）。
	for i := range snap.Pods {
		p := &snap.Pods[i]
		if p.NodeName == "" {
			continue
		}
		node := model.ResourceRef{Kind: "Node", Name: p.NodeName}
		if _, ok := idx.Nodes[node.Key()]; !ok {
			continue
		}
		g.add(p.Ref, node, RunsOn, Provenance{SrcSpecField, "spec.nodeName"})
	}

	// 3. Pod → PVC → PV → StorageClass，以及 Pod 引用的 ConfigMap/Secret（仅名称）。
	for i := range snap.Pods {
		p := &snap.Pods[i]
		for _, pvcRef := range p.PVCRefs {
			g.add(p.Ref, pvcRef, Mounts, Provenance{SrcSpecField, "spec.volumes[].persistentVolumeClaim.claimName"})
		}
		for _, cm := range p.ConfigMaps {
			g.add(p.Ref, cm, ConfigRef, Provenance{SrcSpecField, "spec.volumes[].configMap / envFrom"})
		}
		for _, sc := range p.SecretRefs {
			g.add(p.Ref, sc, SecretRef, Provenance{SrcSpecField, "spec.volumes[].secret / envFrom（仅名称）"})
		}
	}
	for i := range snap.Storage {
		st := &snap.Storage[i]
		switch st.Kind {
		case "PVC":
			if st.VolumeName != "" {
				g.add(st.Ref, model.ResourceRef{Kind: "PV", Name: st.VolumeName}, BoundTo,
					Provenance{SrcSpecField, "spec.volumeName"})
			}
			if st.StorageClassName != "" {
				g.add(st.Ref, model.ResourceRef{Kind: "StorageClass", Name: st.StorageClassName}, ProvisionedBy,
					Provenance{SrcSpecField, "spec.storageClassName"})
			}
		case "PV":
			if st.StorageClassName != "" {
				g.add(st.Ref, model.ResourceRef{Kind: "StorageClass", Name: st.StorageClassName}, ProvisionedBy,
					Provenance{SrcSpecField, "spec.storageClassName"})
			}
		}
	}

	// 4. Service → Pod（selector 命中，复用 correlation 算好的双向索引，避免两套匹配逻辑）。
	for svcKey, pods := range idx.PodsByService {
		svc := idx.Services[svcKey]
		if svc == nil {
			continue
		}
		for _, p := range pods {
			g.add(svc.Ref, p.Ref, SelectedBy, Provenance{SrcLabelSelector, "spec.selector"})
		}
	}

	// 5. EndpointSlice → Service / Pod（地址级关系，比 selector 更接近"实际能不能收到流量"）。
	for i := range snap.EndpointSlices {
		es := &snap.EndpointSlices[i]
		if es.ServiceName != "" {
			g.add(es.Ref, model.ResourceRef{Kind: "Service", Namespace: es.Ref.Namespace, Name: es.ServiceName},
				EndpointOf, Provenance{SrcObjectLabel, "metadata.labels[kubernetes.io/service-name]"})
		}
		for _, tp := range es.TargetPods {
			g.add(es.Ref, tp, HasEndpoint, Provenance{SrcSpecField, "addresses[].targetRef"})
		}
	}

	// 6. Ingress → Service（后端引用）。
	for i := range snap.Ingresses {
		ing := &snap.Ingresses[i]
		for _, be := range ing.Backends {
			ns, name, ok := parseServiceBackend(be, ing.Ref.Namespace)
			if !ok {
				continue
			}
			target := model.ResourceRef{Kind: "Service", Namespace: ns, Name: name}
			if _, exists := idx.Services[target.Key()]; !exists {
				continue // 后端 Service 不存在由 NETWORK_INGRESSBACKEND 规则说明，这里不建悬空边
			}
			g.add(ing.Ref, target, RoutesTo,
				Provenance{SrcSpecField, "spec.rules[].http.paths[].backend.service.name"})
		}
	}

	// 7. HPA → 伸缩目标（spec.scaleTargetRef，同命名空间内的精确名称引用）。
	for i := range snap.Hpas {
		h := &snap.Hpas[i]
		kind, name, ok := parseKindName(h.ScaleTarget)
		if !ok {
			continue
		}
		target := model.ResourceRef{Kind: kind, Namespace: h.Ref.Namespace, Name: name}
		if _, exists := idx.Workloads[target.Key()]; !exists {
			continue // 目标不存在由 WORKLOAD_HPASCALETARGETMISSING 规则说明，这里不建悬空边
		}
		g.add(h.Ref, target, Targets, Provenance{SrcSpecField, "spec.scaleTargetRef"})
	}

	// 8. PDB → Pod、NetworkPolicy → Pod（selector 命中）。
	for i := range snap.Pdbs {
		pdb := &snap.Pdbs[i]
		if len(pdb.Selector) == 0 {
			continue
		}
		for _, p := range podsMatching(snap, pdb.Ref.Namespace, pdb.Selector) {
			g.add(pdb.Ref, p.Ref, Protects, Provenance{SrcLabelSelector, "spec.selector"})
		}
	}
	for i := range snap.NetworkPolicies {
		np := &snap.NetworkPolicies[i]
		if len(np.PodSelector) == 0 {
			continue
		}
		for _, p := range podsMatching(snap, np.Ref.Namespace, np.PodSelector) {
			g.add(np.Ref, p.Ref, AppliesTo, Provenance{SrcLabelSelector, "spec.podSelector"})
		}
	}
	return g
}

// podsMatching 返回同命名空间内标签命中 selector 的 Pod。
//
// selector 为空在 Kubernetes 语义里是"全选"，但调用方已提前跳过空 selector，
// 这里不把空 selector 扩大解释成全命名空间命中，避免误建边。
func podsMatching(snap *model.ClusterSnapshot, ns string, selector map[string]string) []*model.PodInfo {
	var out []*model.PodInfo
	for i := range snap.Pods {
		p := &snap.Pods[i]
		if p.Ref.Namespace != ns {
			continue
		}
		if labelsMatch(selector, p.Labels) {
			out = append(out, p)
		}
	}
	return out
}

func labelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// parseKindName 解析 "Deployment/web" 形式的引用（HPA scaleTargetRef）。
func parseKindName(s string) (kind, name string, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "/")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// parseServiceBackend 解析采集器归一化后的 Ingress 后端串 "ns/svc:port" 或 "svc:port"，
// 后者按 Ingress 自身命名空间解释。
func parseServiceBackend(s, defaultNS string) (ns, name string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "", "", false
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		ns, name = s[:i], s[i+1:]
	} else {
		ns, name = defaultNS, s
	}
	return ns, name, name != ""
}
