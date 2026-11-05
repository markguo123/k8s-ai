package incident

import (
	"fmt"
	"sort"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/relations"
)

// 因果边（需求 §3.3 / 方案 18 §5）。
//
// Incident.Build 只回答「这些 Finding 属于同一条故障链」，它依据的是资源归属聚合，
// 不是因果。聚合不等于因果这件事必须由这一层显式区分开，否则报告里会出现
// 「同命名空间所以 A 导致 B」这种无法自证的结论。
//
// 一条 CausalEdge 成立要同时满足三条硬条件：
//  1. 规则对合法：cause 规则在 effect 规则的「已知成因」表里（人为维护、可审计）；
//  2. 资源关系真实：两条 Finding 的资源在资源关系图上有 ≤2 跳的真实边
//     （OwnerReference / Node 调度 / Selector / 挂载 / 转发目标等，都有出处字段）；
//  3. 带传导机制：Mechanism 必须说清故障怎么传过去。
//
// 明确禁止：用名称前缀相似度、同命名空间、时间邻近推断因果。
// 由本层建立的边一律 confidence=probable——升格到 confirmed 需要证据级论证，
// 那是 deterministic 诊断器的职责（见 internal/deterministic）。

// causalCauses：effect 规则 → 允许的成因规则集合。
//
// 只收录 Kubernetes 语义上成立的传播方向。宁可少收，不要为了「图好看」造边。
var causalCauses = map[string][]string{
	"PendingPod": {
		"NodeNotReady", "NodeDiskPressure", "NodeMemoryPressure", "NodePIDPressure",
		"PVCPending", "FailedMount", "StorageClassNoProvisioner", "PdbBlocking",
		// 容器卡在拉取镜像时 Pod 会一直停在 Pending：拉取失败是因，Pending 是果。
		"ImagePullBackOff",
		// 挂载的配置对象不存在时，Pod 会长期停在 Pending/ContainerCreating：
		// 配置缺失是因，Pending 是果，不能让 Pending 顶着根因。
		"ConfigRefNotFound",
	},
	"FailedMount": {
		"PVCPending", "PVFailed", "StorageClassNoProvisioner", "VolumeAttachmentStuck",
	},
	"ContainerCreateError": {
		"PVCPending", "FailedMount", "NodeNotReady",
	},
	"CrashLoopBackOff": {
		"OOMKilled", "Unhealthy", "ConfigRefNotFound",
	},
	"ContainerHealthTimeout": {
		"ConfigRefNotFound", "NodeNotReady",
	},
	"HighRestartCount": {
		"OOMKilled", "CrashLoopBackOff", "Unhealthy",
	},
	"EvictedPod": {
		"NodeDiskPressure", "NodeMemoryPressure", "NodePIDPressure", "NodeNotReady",
	},
	"DeploymentReplica": {
		"PendingPod", "CrashLoopBackOff", "ImagePullBackOff", "Unhealthy",
		"ConfigRefNotFound", "PdbBlocking", "NodeNotReady", "ContainerCreateError",
	},
	"StatefulSetReplica": {
		"PendingPod", "CrashLoopBackOff", "ImagePullBackOff", "PVCPending",
		"Unhealthy", "ContainerCreateError",
	},
	"DaemonSetReplica": {
		"NodeNotReady", "PendingPod", "ImagePullBackOff", "Unhealthy",
	},
	"JobFailed": {
		"OOMKilled", "ImagePullBackOff", "PendingPod", "FailedMount", "ConfigRefNotFound",
	},
	"ServiceNoEndpoint": {
		"PendingPod", "CrashLoopBackOff", "Unhealthy", "ImagePullBackOff",
		"DeploymentReplica", "StatefulSetReplica", "ContainerHealthTimeout",
	},
	"IngressBackend": {
		"ServiceNoEndpoint", "ConfigRefNotFound",
	},
	"HpaReplicasMismatch": {
		"PendingPod", "CrashLoopBackOff", "ImagePullBackOff", "DeploymentReplica",
	},
	"HpaScaleTargetMissing": {
		"ConfigRefNotFound",
	},
	"PVCPending": {
		"StorageClassNoProvisioner",
	},
	"PVFailed": {
		"StorageClassNoProvisioner", "VolumeAttachmentStuck",
	},
	"VolumeAttachmentStuck": {
		"NodeNotReady",
	},
	"Unhealthy": {
		"ConfigRefNotFound", "OOMKilled",
	},
}

// maxCausalHops 是允许的最大资源关系跳数。
// 2 跳覆盖 Deployment→ReplicaSet→Pod 这类真实owner 传递；再远就已经退化成
// 「同一个集群里的两样东西」，不足以支撑因果结论。
const maxCausalHops = 2

// sameObjectRelation 是「同一个对象上两条观测之间」的传播关系类型。
//
// 这类传播不需要资源关系图：容器 OOMKilled 退出与 Pod 进入 CrashLoopBackOff
// 是同一个对象上的前后两个状态，去图上找路径反而会得出「Pod 和 Pod 有关系」。
const sameObjectRelation = "sameObject"

// Relate 为每个 Incident 补写因果边、影响链与「独立异常」标记。
//
// 就地修改 incs；g 为 nil 时不建任何边（只标 Independent 与缺口），
// 因为「没有关系图」的正确结论是「无法证实因果」，而不是「按名称猜」。
func Relate(findings []model.Finding, incs []model.Incident, g *relations.Graph, idx *model.CorrelationIndex) {
	byID := make(map[string]model.Finding, len(findings))
	for _, f := range findings {
		byID[f.ID] = f
	}
	for i := range incs {
		inc := &incs[i]
		members := append([]model.FindingRef{inc.Root}, inc.Members...)
		if inc.RootCauseStatus == "" {
			inc.RootCauseStatus = model.RootCauseUnknown
		}
		if inc.RootCauseMethod == "" {
			inc.RootCauseMethod = model.DiagnosticMethodNone
			if inc.RootCauseRule != "" {
				inc.RootCauseMethod = model.DiagnosticMethodRule
			}
		}
		inc.Independent = len(inc.Members) == 0

		edges := growCausalTree(inc, members, byID, g)
		inc.CausalEdges = mergeEdges(inc.CausalEdges, edges)
		inc.EvidenceRefs = mergeIDs(inc.EvidenceRefs, incidentEvidence(inc, byID))
		inc.ImpactChain = impactChain(inc)

		if len(inc.Members) > 0 && len(inc.CausalEdges) == 0 {
			inc.EvidenceGaps = append(inc.EvidenceGaps,
				"故障链成员按资源归属聚合，资源关系图上未找到可支撑的传播路径，因果顺序未经证实")
		}
		if g == nil {
			inc.EvidenceGaps = append(inc.EvidenceGaps, "未构建资源关系图：本故障链的因果边未经资源关系校验")
		}
	}
}

// growCausalTree 从根信号生长一棵候选故障树。
// 每个症状只保留一条最能解释它的入边，避免同一 Incident 内所有合法
// cause/effect 两两组合造成边爆炸。更具体的确定性诊断边会通过 mergeEdges 优先保留。
func growCausalTree(inc *model.Incident, members []model.FindingRef, byID map[string]model.Finding, g *relations.Graph) []model.CausalEdge {
	seen := map[string]bool{inc.Root.ID: true}
	for _, edge := range inc.CausalEdges {
		seen[edge.SourceFinding] = true
		seen[edge.TargetFinding] = true
	}
	order := make(map[string]int, len(members))
	for i, member := range members {
		order[member.ID] = i
	}
	var out []model.CausalEdge
	for {
		progress := false
		for _, targetRef := range members {
			if seen[targetRef.ID] {
				continue
			}
			target, ok := byID[targetRef.ID]
			if !ok {
				continue
			}
			causes := causalCauses[target.Rule]
			if len(causes) == 0 {
				continue
			}
			bestCause := -1
			bestOrder := len(members) + 1
			var best model.CausalEdge
			for _, sourceRef := range members {
				if !seen[sourceRef.ID] || sourceRef.ID == targetRef.ID {
					continue
				}
				causeIndex := indexString(causes, sourceRef.Rule)
				if causeIndex < 0 {
					continue
				}
				source, ok := byID[sourceRef.ID]
				if !ok {
					continue
				}
				edge, ok := connect(g, source.Resource, target.Resource)
				if !ok {
					continue
				}
				sourceOrder := order[sourceRef.ID]
				if bestCause < 0 || causeIndex < bestCause ||
					(causeIndex == bestCause && sourceOrder < bestOrder) {
					edge.SourceFinding = source.ID
					edge.TargetFinding = target.ID
					edge.SourceResource = source.Resource
					edge.TargetResource = target.Resource
					edge.Rule = source.Rule + "->" + target.Rule
					edge.Confidence = model.RootCauseProbable
					edge.Method = "correlation"
					edge.EvidenceRefs = mergeIDs(evidenceIDs(source), evidenceIDs(target))
					best = edge
					bestCause = causeIndex
					bestOrder = sourceOrder
				}
			}
			if bestCause >= 0 {
				out = append(out, best)
				seen[targetRef.ID] = true
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetFinding != out[j].TargetFinding {
			return out[i].TargetFinding < out[j].TargetFinding
		}
		return out[i].SourceFinding < out[j].SourceFinding
	})
	return out
}

func indexString(items []string, want string) int {
	for i, item := range items {
		if item == want {
			return i
		}
	}
	return -1
}

// connect 在资源关系图上寻找一条 ≤maxCausalHops 跳的真实关系路径。
func connect(g *relations.Graph, from, to model.ResourceRef) (model.CausalEdge, bool) {
	if from == to {
		// 同对象传播：故障传导发生在对象内部的状态之间，不经过资源关系图。
		return model.CausalEdge{
			RelationType: sameObjectRelation,
			Mechanism: fmt.Sprintf(
				"同一对象 %s 上的两条观测互相印证：前者是后者的直接前置状态", resKey(from)),
		}, true
	}
	if g == nil {
		return model.CausalEdge{}, false
	}
	if e, ok := g.DirectEdge(from, to); ok {
		return model.CausalEdge{
			RelationType: string(e.Type),
			Mechanism:    mechanism(e, from, to),
		}, true
	}
	path, ok := g.Path(from, to)
	if !ok || len(path) == 0 || len(path) > maxCausalHops {
		return model.CausalEdge{}, false
	}
	types := make([]string, 0, len(path))
	for _, e := range path {
		types = append(types, string(e.Type))
	}
	return model.CausalEdge{
		RelationType: joinArrows(types),
		Mechanism: fmt.Sprintf("%s；故障经 %d 跳资源关系由 %s 传导至 %s",
			relationText(path[0]), len(path), resKey(from), resKey(to)),
	}, true
}

// mechanism 说明这条边的故障传导机制：先陈述资源关系事实（方向取自真实边），
// 再说明故障从成因资源传导到症状资源。
func mechanism(e relations.Edge, cause, effect model.ResourceRef) string {
	return fmt.Sprintf("%s；故障由 %s 传导至 %s", relationText(e), resKey(cause), resKey(effect))
}

// relationText 按边的真实方向描述资源关系及其出处字段。
func relationText(e relations.Edge) string {
	f, t := resKey(e.From), resKey(e.To)
	var rel string
	switch e.Type {
	case relations.OwnedBy:
		rel = fmt.Sprintf("%s 的 ownerReferences 指向 %s（%s 管理 %s）", f, t, t, f)
	case relations.RunsOn:
		rel = fmt.Sprintf("%s 调度在 %s 上", f, t)
	case relations.SelectedBy:
		rel = fmt.Sprintf("%s 的 selector 命中 %s", f, t)
	case relations.HasEndpoint:
		rel = fmt.Sprintf("%s 的地址指向 %s", f, t)
	case relations.EndpointOf:
		rel = fmt.Sprintf("%s 是 %s 的 EndpointSlice", f, t)
	case relations.RoutesTo:
		rel = fmt.Sprintf("%s 的后端转发到 %s", f, t)
	case relations.Mounts:
		rel = fmt.Sprintf("%s 挂载 %s", f, t)
	case relations.BoundTo:
		rel = fmt.Sprintf("%s 绑定到 %s", f, t)
	case relations.ProvisionedBy:
		rel = fmt.Sprintf("%s 使用 %s 供给", f, t)
	case relations.Targets:
		rel = fmt.Sprintf("%s 以 %s 为目标", f, t)
	case relations.Protects:
		rel = fmt.Sprintf("%s 的驱逐预算覆盖 %s", f, t)
	case relations.AppliesTo:
		rel = fmt.Sprintf("%s 选中并作用于 %s", f, t)
	case relations.ConfigRef:
		rel = fmt.Sprintf("%s 引用配置 %s", f, t)
	case relations.SecretRef:
		rel = fmt.Sprintf("%s 引用 Secret %s（仅名称）", f, t)
	default:
		rel = fmt.Sprintf("%s 与 %s 存在 %s 关系", f, t, e.Type)
	}
	if e.Prov.Source != "" {
		rel += fmt.Sprintf("（出处：%s:%s）", e.Prov.Source, e.Prov.Field)
	}
	return rel
}

// impactChain 沿已建立的因果边从根 Finding 资源向外展开影响链。
func impactChain(inc *model.Incident) []string {
	if len(inc.CausalEdges) == 0 {
		if inc.Independent {
			return []string{resKey(inc.Root.Resource)}
		}
		return nil
	}
	bySource := map[string][]model.CausalEdge{}
	for _, e := range inc.CausalEdges {
		bySource[e.SourceFinding] = append(bySource[e.SourceFinding], e)
	}
	out := []string{resKey(inc.Root.Resource)}
	seenRes := map[string]bool{resKey(inc.Root.Resource): true}
	queue := []string{inc.Root.ID}
	seenF := map[string]bool{inc.Root.ID: true}
	for len(queue) > 0 && len(out) < 12 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range bySource[cur] {
			key := resKey(e.TargetResource)
			if !seenRes[key] {
				seenRes[key] = true
				out = append(out, key)
			}
			if !seenF[e.TargetFinding] {
				seenF[e.TargetFinding] = true
				queue = append(queue, e.TargetFinding)
			}
		}
	}
	return out
}

func incidentEvidence(inc *model.Incident, byID map[string]model.Finding) []string {
	var ids []string
	ids = append(ids, evidenceIDs(byID[inc.Root.ID])...)
	for _, m := range inc.Members {
		ids = append(ids, evidenceIDs(byID[m.ID])...)
	}
	return ids
}

func evidenceIDs(f model.Finding) []string {
	ids := make([]string, 0, len(f.Evidence))
	for _, ev := range f.Evidence {
		if ev.ID != "" {
			ids = append(ids, model.GlobalEvidenceRef(f.ID, ev.ID))
		}
	}
	return ids
}

// mergeEdges 按「源 Finding + 目标 Finding + 关系类型」去重，保留先到的一条
// （deterministic 诊断器先写入的边更具体，因此 base 优先）。
func mergeEdges(base, add []model.CausalEdge) []model.CausalEdge {
	if len(base) == 0 && len(add) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []model.CausalEdge
	for _, list := range [][]model.CausalEdge{base, add} {
		for _, e := range list {
			k := e.SourceFinding + "|" + e.TargetFinding + "|" + e.RelationType
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

func mergeIDs(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func joinArrows(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "->"
		}
		out += s
	}
	return out
}

func resKey(r model.ResourceRef) string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Namespace + "/" + r.Name
}
