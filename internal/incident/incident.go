// Package incident 将因果相关的 Findings 聚合成 Incident（故障链），
// 使"一个故障链只诊断一次"：根因 + 派生症状作为一个诊断单元。
package incident

import (
	"sort"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// dsu 并查集。
type dsu struct{ parent []int }

func newDSU(n int) *dsu {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &dsu{parent: p}
}

func (d *dsu) find(x int) int {
	for d.parent[x] != x {
		d.parent[x] = d.parent[d.parent[x]]
		x = d.parent[x]
	}
	return x
}

func (d *dsu) union(a, b int) {
	ra, rb := d.find(a), d.find(b)
	if ra != rb {
		d.parent[rb] = ra
	}
}

// touchKeys 返回 Finding 涉及的所有资源 key（用于按故障链合并）。
// 规则：Pod↔owner/Node/Service/PVC；Workload↔其 Pod；Service↔匹配 Pod；Node↔其上 Pod；PVC↔使用它的 Pod。
func touchKeys(idx *model.CorrelationIndex, f model.Finding) map[string]bool {
	keys := map[string]bool{f.Resource.Key(): true}
	switch f.Resource.Kind {
	case "Pod":
		if p := idx.Pod(f.Resource.Key()); p != nil {
			for _, w := range idx.OwnerChain(p) {
				keys[w.Ref.Key()] = true
			}
			if p.NodeName != "" {
				keys[model.ResourceRef{Kind: "Node", Name: p.NodeName}.Key()] = true
			}
			for _, s := range idx.ServicesOfPod(f.Resource.Key()) {
				keys[s.Ref.Key()] = true
			}
			for _, pvc := range p.PVCRefs {
				keys[pvc.Key()] = true
			}
		}
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob":
		for _, p := range idx.PodsOfWorkload(f.Resource.Key()) {
			keys[p.Ref.Key()] = true
		}
	case "Service":
		for _, p := range idx.PodsOfService(f.Resource.Key()) {
			keys[p.Ref.Key()] = true
		}
	case "Node":
		for _, p := range idx.PodsOfNode(f.Resource.Name) {
			keys[p.Ref.Key()] = true
		}
	case "PVC":
		for _, p := range idx.PodsOfPVC(f.Resource.Key()) {
			keys[p.Ref.Key()] = true
		}
	}
	return keys
}

// Build 将 Findings 按故障链聚合为 Incidents，按严重级降序返回。
func Build(findings []model.Finding, idx *model.CorrelationIndex) []model.Incident {
	n := len(findings)
	if n == 0 {
		return nil
	}
	uf := newDSU(n)
	keyToIdx := map[string][]int{}
	for i := range findings {
		for k := range touchKeys(idx, findings[i]) {
			keyToIdx[k] = append(keyToIdx[k], i)
		}
	}
	for _, group := range keyToIdx {
		for j := 1; j < len(group); j++ {
			uf.union(group[0], group[j])
		}
	}
	groups := map[int][]int{}
	for i := 0; i < n; i++ {
		r := uf.find(i)
		groups[r] = append(groups[r], i)
	}
	var out []model.Incident
	for _, idxs := range groups {
		fs := make([]model.Finding, 0, len(idxs))
		for _, i := range idxs {
			fs = append(fs, findings[i])
		}
		root := pickRoot(fs)
		var members []model.FindingRef
		for _, f := range fs {
			if f.ID == root.ID {
				continue
			}
			members = append(members, refOf(f))
		}
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].Severity != members[j].Severity {
				return model.SeverityRank(members[i].Severity) > model.SeverityRank(members[j].Severity)
			}
			return members[i].ID < members[j].ID
		})
		out = append(out, model.Incident{ID: root.ID, Title: root.Title, Severity: root.Severity, Root: refOf(root), Members: members})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return model.SeverityRank(out[i].Severity) > model.SeverityRank(out[j].Severity)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// pickRoot 选择故障链的最底层可证实原因：
// 存储异常 > 节点异常 > Pod 启动/运行类 > Workload > Service。
func pickRoot(fs []model.Finding) model.Finding {
	for _, r := range []string{"PVCPending"} {
		if f, ok := findByRule(fs, r); ok {
			return f
		}
	}
	for _, r := range []string{"NodeNotReady", "NodeDiskPressure", "NodeMemoryPressure"} {
		if f, ok := findByRule(fs, r); ok {
			return f
		}
	}
	for _, r := range []string{"PendingPod", "ContainerCreateError", "CrashLoopBackOff", "OOMKilled", "ImagePullBackOff", "FailedMount", "Unhealthy"} {
		if f, ok := findByRule(fs, r); ok {
			return f
		}
	}
	for _, r := range []string{"DeploymentReplica", "StatefulSetReplica", "JobFailed"} {
		if f, ok := findByRule(fs, r); ok {
			return f
		}
	}
	if f, ok := findByRule(fs, "ServiceNoEndpoint"); ok {
		return f
	}
	best := fs[0]
	for _, f := range fs[1:] {
		if model.SeverityRank(f.Severity) > model.SeverityRank(best.Severity) ||
			(model.SeverityRank(f.Severity) == model.SeverityRank(best.Severity) && f.ID < best.ID) {
			best = f
		}
	}
	return best
}

func findByRule(fs []model.Finding, rule string) (model.Finding, bool) {
	for _, f := range fs {
		if f.Rule == rule {
			return f, true
		}
	}
	return model.Finding{}, false
}

func refOf(f model.Finding) model.FindingRef {
	return model.FindingRef{ID: f.ID, Rule: f.Rule, Severity: f.Severity, Title: f.Title, Resource: f.Resource}
}
