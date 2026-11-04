// Package relations 构建真实的 Kubernetes 资源关系图（四期 P9，需求 §3.1）。
//
// 与 CorrelationIndex 的分工要说清楚：CorrelationIndex 是「查询加速用的倒排表」，
// 本包是「带出处的关系图」——每条边都记录它是从哪个字段的哪个来源读出来的，
// 因此可以回答「你凭什么说这个 Pod 依赖那个 PVC」。
//
// 一条硬规矩：边只能来自 Kubernetes 对象里的真实字段（ownerReferences / UID /
// label selector / spec 中的引用字段 / status 中的关联信息）。禁止用名称前缀相似
// 推断关系——那是 detectWorkloadOwner 那种"给命令找个大概目标"的启发式，
// 用来判因果会稳定地产出假故障链。
package relations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// RelationType 是资源关系类型。
type RelationType string

const (
	// OwnedBy：A 的 metadata.ownerReferences 指向 B（Pod→ReplicaSet→Deployment）。
	OwnedBy RelationType = "ownedBy"
	// RunsOn：Pod 被调度到 Node（spec.nodeName）。
	RunsOn RelationType = "runsOn"
	// SelectedBy：Service 的 spec.selector 命中 Pod 标签（Kubernetes 自己的选主逻辑）。
	SelectedBy RelationType = "selectedBy"
	// HasEndpoint：EndpointSlice 的 address.targetRef 指向 Pod。
	HasEndpoint RelationType = "hasEndpoint"
	// EndpointOf：EndpointSlice 归属 Service（kubernetes.io/service-name 标签）。
	EndpointOf RelationType = "endpointOf"
	// RoutesTo：Ingress 后端引用 Service（spec.rules[].http.paths[].backend）。
	RoutesTo RelationType = "routesTo"
	// Mounts：Pod 挂载 PVC（spec.volumes[].persistentVolumeClaim.claimName）。
	Mounts RelationType = "mounts"
	// BoundTo：PVC 绑定 PV（pvc.spec.volumeName）。
	BoundTo RelationType = "boundTo"
	// ProvisionedBy：PVC/PV 使用 StorageClass（spec.storageClassName）。
	ProvisionedBy RelationType = "provisionedBy"
	// Targets：HPA 伸缩目标（spec.scaleTargetRef）。
	Targets RelationType = "targets"
	// Protects：PDB 的 spec.selector 命中 Pod。
	Protects RelationType = "protects"
	// AppliesTo：NetworkPolicy 的 spec.podSelector 命中 Pod。
	AppliesTo RelationType = "appliesTo"
	// ConfigRef：Pod 引用 ConfigMap（仅名称）。
	ConfigRef RelationType = "configRef"
	// SecretRef：Pod 引用 Secret（仅名称，绝不读取数据）。
	SecretRef RelationType = "secretRef"
)

// 关系出处来源（需求 §3.1「资源关系数据必须可以追溯来源」）。
const (
	SrcOwnerReference = "ownerReference"
	SrcUID            = "uid"
	SrcLabelSelector  = "labelSelector"
	SrcSpecField      = "specField"
	SrcObjectLabel    = "objectLabel"
)

// Provenance 说明一条边是从哪儿读出来的。
type Provenance struct {
	Source string `json:"source"`
	Field  string `json:"field"`
}

// Edge 是一条有向资源关系：From --Type--> To。
type Edge struct {
	From model.ResourceRef `json:"from"`
	To   model.ResourceRef `json:"to"`
	Type RelationType      `json:"type"`
	Prov Provenance        `json:"provenance"`
}

// String 给人读的关系描述（含出处，报告与 LLM 上下文共用）。
func (e Edge) String() string {
	return fmt.Sprintf("%s %s -> %s [%s:%s]", refName(e.From), e.Type, refName(e.To), e.Prov.Source, e.Prov.Field)
}

func refName(r model.ResourceRef) string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// Graph 是资源关系图。只读、可并发查询，不依赖标准库之外的任何组件。
type Graph struct {
	out     map[string][]Edge
	in      map[string][]Edge
	nodes   map[string]model.ResourceRef
	kindCnt map[RelationType]int
}

func newGraph() *Graph {
	return &Graph{
		out:     map[string][]Edge{},
		in:      map[string][]Edge{},
		nodes:   map[string]model.ResourceRef{},
		kindCnt: map[RelationType]int{},
	}
}

func (g *Graph) add(from, to model.ResourceRef, t RelationType, prov Provenance) {
	if from.Name == "" || to.Name == "" || from.Key() == to.Key() {
		return
	}
	e := Edge{From: from, To: to, Type: t, Prov: prov}
	g.out[from.Key()] = append(g.out[from.Key()], e)
	g.in[to.Key()] = append(g.in[to.Key()], e)
	g.nodes[from.Key()] = from
	g.nodes[to.Key()] = to
	g.kindCnt[t]++
}

// Out 返回从该资源出发的边。
func (g *Graph) Out(key string) []Edge { return g.out[key] }

// In 返回指向该资源的边。
func (g *Graph) In(key string) []Edge { return g.in[key] }

// All 返回全部边（按 from/type 稳定排序，报告渲染与测试断言用）。
func (g *Graph) All() []Edge {
	var out []Edge
	for _, es := range g.out {
		out = append(out, es...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From.Key() != out[j].From.Key() {
			return out[i].From.Key() < out[j].From.Key()
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].To.Key() < out[j].To.Key()
	})
	return out
}

// EdgeCount 是图中边的总数。
func (g *Graph) EdgeCount() int {
	n := 0
	for _, es := range g.kindCnt {
		n += es
	}
	return n
}

// KindCounts 返回各关系类型的边数（报告可用来证明"关系不是编的"）。
func (g *Graph) KindCounts() map[RelationType]int {
	out := make(map[RelationType]int, len(g.kindCnt))
	for k, v := range g.kindCnt {
		out[k] = v
	}
	return out
}

// Connected 判断两个资源之间是否存在真实关系路径（任意方向、任意跳数）。
//
// 这是「能不能谈因果」的准入门槛：没有路径的两个 Finding，哪怕同处一个命名空间，
// 也不许连因果边（需求 §3.2）。
func (g *Graph) Connected(a, b model.ResourceRef) bool {
	_, ok := g.Path(a, b)
	return ok
}

// Path 用 BFS 找一条从 a 到 b 的关系路径（边方向不敏感）。
// 返回值是路径上依次经过的边；起点即终点时返回 (nil, true)。
func (g *Graph) Path(a, b model.ResourceRef) ([]Edge, bool) {
	start, target := a.Key(), b.Key()
	if start == target {
		return nil, true
	}
	if _, ok := g.nodes[start]; !ok {
		return nil, false
	}
	prev := map[string]Edge{}
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.neighbors(cur) {
			next := otherEnd(e, cur)
			if next == "" || seen[next] {
				continue
			}
			seen[next] = true
			prev[next] = e
			if next == target {
				var path []Edge
				for k := target; k != start; {
					pe, ok := prev[k]
					if !ok {
						return nil, false
					}
					path = append([]Edge{pe}, path...)
					k = otherEnd(pe, k)
				}
				return path, true
			}
			queue = append(queue, next)
		}
	}
	return nil, false
}

// otherEnd 返回边上的另一端；key 不在这条边上时返回空串。
func otherEnd(e Edge, key string) string {
	switch key {
	case e.From.Key():
		return e.To.Key()
	case e.To.Key():
		return e.From.Key()
	}
	return ""
}

func (g *Graph) neighbors(key string) []Edge {
	es := make([]Edge, 0, len(g.out[key])+len(g.in[key]))
	es = append(es, g.out[key]...)
	es = append(es, g.in[key]...)
	return es
}

// DirectEdge 返回两个资源之间的直接关系（任一方向）。找不到返回 false。
func (g *Graph) DirectEdge(a, b model.ResourceRef) (Edge, bool) {
	for _, e := range g.out[a.Key()] {
		if e.To.Key() == b.Key() {
			return e, true
		}
	}
	for _, e := range g.in[a.Key()] {
		if e.From.Key() == b.Key() {
			return e, true
		}
	}
	return Edge{}, false
}

// EdgesOfType 返回某一类型的全部边（诊断器批量核对关系用）。
func (g *Graph) EdgesOfType(t RelationType) []Edge {
	var out []Edge
	for _, e := range g.All() {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// TextFor 渲染若干资源的关系摘要，供 LLM 上下文使用（需求 §4.1「Kubernetes 资源关系」）。
//
// 只输出这些资源自身的一跳关系，并按 max 截断：把整张图塞进 prompt 既超预算，
// 又会让模型在无关边上自由发挥。
func (g *Graph) TextFor(refs []model.ResourceRef, max int) string {
	if max <= 0 {
		max = 24
	}
	seen := map[string]bool{}
	var lines []string
	for _, r := range refs {
		for _, e := range g.neighbors(r.Key()) {
			key := e.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			lines = append(lines, "- "+key)
			if len(lines) >= max {
				lines = append(lines, fmt.Sprintf("- （关系共 %d 条，已截断）", g.EdgeCount()))
				return strings.Join(lines, "\n")
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}
