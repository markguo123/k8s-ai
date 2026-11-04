// Package deterministic_test 用「真实流水线」验证确定性诊断：
// 规则引擎 → 关系图 → 故障聚合 → 因果边 → 确定性诊断，
// 与 internal/service 的 scan 走的是同一条链路，只是不连集群、不调模型。
package deterministic_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/deterministic"
	"github.com/k8s-ai/k8s-ai/internal/incident"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/relations"
	"github.com/k8s-ai/k8s-ai/internal/rule"
)

// oldTimestamp 是「已经超过各规则时间门槛」的创建时间（Pending/Terminating 等）。
const oldTimestamp = "2020-01-01T00:00:00Z"

// chain 是一次完整诊断流水线跑完的结果。
type chain struct {
	Snap     *model.ClusterSnapshot
	Idx      *model.CorrelationIndex
	Graph    *relations.Graph
	Findings []model.Finding
	Incs     []model.Incident
	Diags    []model.Diagnosis
}

// chainOpts 是流水线的可选注入，用来模拟「证据缺失」这类退化输入。
type chainOpts struct {
	stripEvidence bool // 模拟证据挂载失败：Finding 还在，证据没了
	skipIndex     bool // 模拟关联索引缺失（采集不完整的极端情况）
}

func runChain(t *testing.T, snap *model.ClusterSnapshot) *chain {
	t.Helper()
	return runChainWith(t, snap, chainOpts{})
}

// runChainWith 复现 scanService.Run 的诊断段（不含采集与 LLM）。
func runChainWith(t *testing.T, snap *model.ClusterSnapshot, o chainOpts) *chain {
	t.Helper()
	if snap.CollectedAt == "" {
		snap.CollectedAt = "2026-01-01T00:00:00Z"
	}
	reg := rule.NewRegistry()
	for _, rw := range rule.AllDefaultWithMeta() {
		if err := reg.RegisterWithMeta(rw.Rule, rw.Meta); err != nil {
			reg.Register(rw.Rule)
		}
	}
	var idx *model.CorrelationIndex
	if !o.skipIndex {
		idx = correlation.Build(snap)
	} else {
		idx = &model.CorrelationIndex{}
	}
	ptrs := rule.NewEngine(reg).Evaluate(context.Background(), snap, idx, model.ScanOptions{})
	findings := make([]model.Finding, 0, len(ptrs))
	for _, fp := range ptrs {
		f := *fp
		if o.stripEvidence {
			f.Evidence = nil
		}
		findings = append(findings, f)
	}
	g := relations.Build(snap, idx)
	incs := incident.Build(findings, idx)
	incident.Relate(findings, incs, g, idx)
	diags := deterministic.NewEngine(snap, idx, g, findings).Apply(incs)
	return &chain{Snap: snap, Idx: idx, Graph: g, Findings: findings, Incs: incs, Diags: diags}
}

// ---- 查询辅助：断言写得像人话，不重复写循环 ----

func (c *chain) findingsFor(ruleName string) []model.Finding {
	var out []model.Finding
	for _, f := range c.Findings {
		if f.Rule == ruleName {
			out = append(out, f)
		}
	}
	return out
}

func (c *chain) mustFinding(t *testing.T, ruleName string) model.Finding {
	t.Helper()
	fs := c.findingsFor(ruleName)
	if len(fs) == 0 {
		t.Fatalf("期望命中规则 %s，实际规则集合 = %s", ruleName, c.ruleSet())
	}
	return fs[0]
}

func (c *chain) requireNoFinding(t *testing.T, ruleName string) {
	t.Helper()
	if fs := c.findingsFor(ruleName); len(fs) != 0 {
		t.Fatalf("正常情况不应命中 %s，实际 %d 条：%s", ruleName, len(fs), fs[0].Summary)
	}
}

func (c *chain) ruleSet() string {
	var rules []string
	for _, f := range c.Findings {
		rules = append(rules, f.Rule)
	}
	if len(rules) == 0 {
		return "(无 Finding)"
	}
	return strings.Join(rules, ",")
}

// incidentOf 返回包含该规则 Finding 的故障链（root 或 member）。
func (c *chain) incidentOf(t *testing.T, ruleName string) *model.Incident {
	t.Helper()
	f := c.mustFinding(t, ruleName)
	for i := range c.Incs {
		if c.Incs[i].Root.ID == f.ID {
			return &c.Incs[i]
		}
		for _, m := range c.Incs[i].Members {
			if m.ID == f.ID {
				return &c.Incs[i]
			}
		}
	}
	t.Fatalf("规则 %s 的 Finding 不在任何故障链里", ruleName)
	return nil
}

// diagOf 返回故障链里由指定诊断器给出的结论。
func (c *chain) diagOf(t *testing.T, incidentID, diagnoserID string) model.Diagnosis {
	t.Helper()
	for _, d := range c.Diags {
		if d.IncidentID == incidentID && d.Diagnoser == diagnoserID {
			return d
		}
	}
	var got []string
	for _, d := range c.Diags {
		got = append(got, d.IncidentID+":"+d.Diagnoser)
	}
	t.Fatalf("故障链 %s 上没有诊断器 %s 的结论，实际 = %v", incidentID, diagnoserID, got)
	return model.Diagnosis{}
}

// requireDiagnoser 断言某规则所在故障链给出了指定诊断器结论，并返回它。
func (c *chain) requireDiagnoser(t *testing.T, ruleName, diagnoserID string) (model.Diagnosis, *model.Incident) {
	t.Helper()
	inc := c.incidentOf(t, ruleName)
	return c.diagOf(t, inc.ID, diagnoserID), inc
}

func (c *chain) hasDiagnoser(id string) bool {
	for _, d := range c.Diags {
		if d.Diagnoser == id {
			return true
		}
	}
	return false
}

func (c *chain) causalEdgeCount() int {
	n := 0
	for i := range c.Incs {
		n += len(c.Incs[i].CausalEdges)
	}
	return n
}

// evidenceUniverse 返回故障链里真实存在的证据 ID 集合。
func (c *chain) evidenceUniverse(inc *model.Incident) map[string]bool {
	out := map[string]bool{}
	add := func(id string) {
		for _, f := range c.Findings {
			if f.ID != id {
				continue
			}
			for _, e := range f.Evidence {
				out[e.ID] = true
			}
		}
	}
	add(inc.Root.ID)
	for _, m := range inc.Members {
		add(m.ID)
	}
	return out
}

// ---- 夹具构造 ----

func objRef(kind, ns, name string) model.ResourceRef {
	return model.ResourceRef{Kind: kind, Namespace: ns, Name: name}
}

func newSnapshot() *model.ClusterSnapshot {
	return &model.ClusterSnapshot{
		ServerVersion: "1.30.0",
		Namespaces: []model.NamespaceInfo{
			{Ref: objRef("Namespace", "", "prod")},
			{Ref: objRef("Namespace", "", "kube-system")},
		},
	}
}

func addPod(snap *model.ClusterSnapshot, ns, name string, mut func(*model.PodInfo)) *model.PodInfo {
	p := model.PodInfo{
		Ref:   model.ResourceRef{Kind: "Pod", Namespace: ns, Name: name, UID: "uid-" + ns + "-" + name},
		Phase: "Running",
	}
	if mut != nil {
		mut(&p)
	}
	snap.Pods = append(snap.Pods, p)
	return &snap.Pods[len(snap.Pods)-1]
}

func addNode(snap *model.ClusterSnapshot, name string, mut func(*model.NodeInfo)) *model.NodeInfo {
	n := model.NodeInfo{Ref: objRef("Node", "", name)}
	if mut != nil {
		mut(&n)
	}
	snap.Nodes = append(snap.Nodes, n)
	return &snap.Nodes[len(snap.Nodes)-1]
}

func addWorkload(snap *model.ClusterSnapshot, kind, ns, name string, mut func(*model.WorkloadInfo)) *model.WorkloadInfo {
	w := model.WorkloadInfo{Ref: objRef(kind, ns, name)}
	if mut != nil {
		mut(&w)
	}
	snap.Workloads = append(snap.Workloads, w)
	return &snap.Workloads[len(snap.Workloads)-1]
}

func addService(snap *model.ClusterSnapshot, ns, name string, selector map[string]string) *model.ServiceInfo {
	snap.Services = append(snap.Services, model.ServiceInfo{
		Ref: objRef("Service", ns, name), Selector: selector,
	})
	return &snap.Services[len(snap.Services)-1]
}

func addStorage(snap *model.ClusterSnapshot, kind, ns, name string, mut func(*model.StorageInfo)) *model.StorageInfo {
	st := model.StorageInfo{Ref: objRef(kind, ns, name), Kind: kind, Status: "Bound"}
	if mut != nil {
		mut(&st)
	}
	snap.Storage = append(snap.Storage, st)
	return &snap.Storage[len(snap.Storage)-1]
}

// addEvent 挂一条事件。InvolvedObject 必须带 UID：关联索引按 UID 建主键。
func addEvent(snap *model.ClusterSnapshot, involved model.ResourceRef, evType, reason, msg string) {
	if snap.EventsIndex == nil {
		snap.EventsIndex = map[string][]model.EventInfo{}
	}
	uid := involved.UID
	if uid == "" {
		uid = "uid-" + involved.Key()
	}
	snap.EventsIndex[uid] = append(snap.EventsIndex[uid], model.EventInfo{
		Type: evType, Reason: reason, Message: msg, Count: 3,
		FirstTimestamp: oldTimestamp, LastTimestamp: "2026-01-01T00:10:00Z",
		InvolvedObject: involved,
	})
}

// int32p 是副本数指针的简写（夹具里到处要用）。
func int32p(v int32) *int32 { return &v }

// nowRFC3339 是「刚刚」：给滚动发布宽限期这类时间门槛用。
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
