// Package deterministic 提供不依赖 LLM 的确定性体检诊断（四期 P9，需求 §4.2）。
//
// 四条硬规矩（口径来自 docs/01-产品与需求/05-体检功能优化.md）：
//  1. 只看已采集到的对象字段、事件与日志，不发起任何集群调用；
//  2. 结论只能挂真实证据 ID：没有证据就不许判 confirmed，最多 probable；
//  3. 资源关系与故障传播以 relations.Graph（真实字段出处）为准，
//     禁止用名称相似度推断因果；
//  4. unknown 是合法结论：证据不足时必须写清缺口，而不是把症状硬拗成根因。
//
// 本包只依赖 model 与 relations，不依赖 llm / diagnosis / scanner，
// 因此「无 LLM」与「有 LLM」两条路径跑的是同一套确定性结论，
// LLM 只在其上做增强，不能覆盖这里已经确认的事实。
package deterministic

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/relations"
)

// 置信度阶梯（需求 §3.3：可信度必须说明依据，不是模型自评分）。
//
// 阶梯刻意做得很粗：体检没有真值集，细到 0.83 这种数字只会给人一种假的精确感。
const (
	ConfObjectAndEvent = 0.95 // 对象状态字段 + 对应事件/日志互相印证
	ConfObjectStatus   = 0.85 // 对象状态字段直接写明原因（如 waiting.reason）
	ConfEventMessage   = 0.75 // 仅事件消息定位到原因
	ConfInferred       = 0.6  // 多个弱信号共同指向
	ConfClue           = 0.45 // 只有线索，不足以定因
	ConfUnknown        = 0.25 // 明确无法定位
)

// Result 是一个诊断器给出的确定性结论。
type Result struct {
	Diagnoser     string // 命中的诊断器 ID（注册表里的唯一标识）
	AnchorFinding string // 这条结论挂在哪个 Finding 上
	Status        string // model.RootCauseConfirmed | Probable | Unknown
	RootCause     string // 根因（一句可核验的事实陈述）
	Mechanism     string // 故障传导机制
	Summary       string // 给人读的结论段
	Impact        string // 影响面

	Confidence      float64
	ConfidenceBasis string

	EvidenceRefs      []string
	EvidenceGaps      []string
	AlternativeCauses []string
	ImpactChain       []string
	CausalEdges       []model.CausalEdge

	Investigation        []model.Command
	Remediation          []model.Command
	RemediationText      string
	RemediationDirection string
	Verification         []model.Command
}

// Diagnoser 是一个可注册、可单测的确定性诊断器。
//
// Match 是触发条件（含排除条件），Diagnose 只允许使用 Match 已经确认存在的数据。
type Diagnoser interface {
	ID() string
	Scenario() string
	Match(*Case) bool
	Diagnose(*Case) Result
}

var registry []Diagnoser

// Register 注册诊断器；注册顺序即优先级（越具体越靠前）。
func Register(d Diagnoser) {
	if d != nil && d.ID() != "" {
		registry = append(registry, d)
	}
}

// Diagnosers 返回已注册诊断器副本。
func Diagnosers() []Diagnoser {
	out := make([]Diagnoser, len(registry))
	copy(out, registry)
	return out
}

// Scenarios 返回已覆盖场景清单（文档与测试矩阵据此核对覆盖率）。
func Scenarios() []string {
	out := make([]string, 0, len(registry))
	for _, d := range registry {
		out = append(out, d.ID()+"|"+d.Scenario())
	}
	return out
}

// Engine 是一次体检的确定性诊断执行器。
type Engine struct {
	Snapshot *model.ClusterSnapshot
	Index    *model.CorrelationIndex
	Graph    *relations.Graph
	Findings []model.Finding

	byID  map[string]*model.Finding
	comps map[string]model.ComponentInfo
}

// NewEngine 构建诊断执行器。参数允许为 nil（缺什么就少判什么，不 panic）。
func NewEngine(snap *model.ClusterSnapshot, idx *model.CorrelationIndex, g *relations.Graph, findings []model.Finding) *Engine {
	e := &Engine{
		Snapshot: snap,
		Index:    idx,
		Graph:    g,
		Findings: findings,
		byID:     make(map[string]*model.Finding, len(findings)),
		comps:    map[string]model.ComponentInfo{},
	}
	for i := range findings {
		if id := findings[i].ID; id != "" {
			e.byID[id] = &findings[i]
		}
	}
	if snap != nil {
		for _, c := range snap.Components {
			e.comps[c.Name] = c
		}
	}
	return e
}

func (e *Engine) index() *model.CorrelationIndex {
	if e.Index == nil {
		return &model.CorrelationIndex{}
	}
	return e.Index
}

// Case 是单个 Incident 的诊断上下文。
type Case struct {
	E        *Engine
	Incident model.Incident
	Root     model.Finding
	Items    []*model.Finding

	byRule map[string]*model.Finding
}

// CaseOf 组装 Incident 的诊断上下文（root + members 按 FindingID 还原为真实 Finding）。
func (e *Engine) CaseOf(inc model.Incident) *Case {
	c := &Case{E: e, Incident: inc, byRule: map[string]*model.Finding{}}
	if f := e.byID[inc.Root.ID]; f != nil {
		c.Root = *f
		c.Items = append(c.Items, f)
		c.byRule[f.Rule] = f
	}
	for _, m := range inc.Members {
		f := e.byID[m.ID]
		if f == nil {
			continue
		}
		c.Items = append(c.Items, f)
		if _, ok := c.byRule[f.Rule]; !ok {
			c.byRule[f.Rule] = f
		}
	}
	return c
}

// All 返回本故障链的全部 Finding。
func (c *Case) All() []*model.Finding { return c.Items }

// HasRule 判断本故障链是否命中某条规则。
func (c *Case) HasRule(rule string) bool { _, ok := c.byRule[rule]; return ok }

// ByRule 返回本故障链中该规则的第一条 Finding。
func (c *Case) ByRule(rule string) *model.Finding { return c.byRule[rule] }

// Finding 按 ID 取 Finding（仅限本次扫描）。
func (c *Case) Finding(id string) *model.Finding { return c.E.byID[id] }

// GlobalByRule 返回整次扫描中该规则的全部 Finding（跨故障链取证用）。
func (c *Case) GlobalByRule(rule string) []*model.Finding {
	var out []*model.Finding
	for i := range c.E.Findings {
		if c.E.Findings[i].Rule == rule {
			out = append(out, &c.E.Findings[i])
		}
	}
	return out
}

// FindingOn 返回挂在某资源上的 Finding（本故障链优先，其次整次扫描）。
func (c *Case) FindingOn(ref model.ResourceRef, rule string) *model.Finding {
	for _, f := range c.Items {
		if f.Resource.Key() == ref.Key() && (rule == "" || f.Rule == rule) {
			return f
		}
	}
	for i := range c.E.Findings {
		f := &c.E.Findings[i]
		if f.Resource.Key() == ref.Key() && (rule == "" || f.Rule == rule) {
			return f
		}
	}
	return nil
}

// Pod 返回 Pod 对象（不在快照中返回 nil）。
func (c *Case) Pod(ref model.ResourceRef) *model.PodInfo { return c.E.index().Pods[ref.Key()] }

// Workload 返回 workload 对象。
func (c *Case) Workload(ref model.ResourceRef) *model.WorkloadInfo {
	return c.E.index().Workloads[ref.Key()]
}

// Node 按节点名返回节点对象。
func (c *Case) Node(name string) *model.NodeInfo {
	if name == "" {
		return nil
	}
	return c.E.index().Node(name)
}

// Storage 返回 PVC/PV/StorageClass/VolumeAttachment 对象。
func (c *Case) Storage(ref model.ResourceRef) *model.StorageInfo {
	return c.E.index().Storage[ref.Key()]
}

// Service 返回 Service 对象。
func (c *Case) Service(ref model.ResourceRef) *model.ServiceInfo {
	return c.E.index().Services[ref.Key()]
}

// PodOf 返回 Finding 对应的 Pod（Finding 资源不是 Pod 时返回 nil）。
func (c *Case) PodOf(f model.Finding) *model.PodInfo {
	if f.Resource.Kind != "Pod" {
		return nil
	}
	return c.Pod(f.Resource)
}

// RootPod 返回根因 Finding 对应的 Pod。
func (c *Case) RootPod() *model.PodInfo { return c.PodOf(c.Root) }

// Events 返回该资源的关联事件。
func (c *Case) Events(ref model.ResourceRef) []model.EventInfo {
	return c.E.index().EventsFor(ref)
}

// EventWith 返回第一条 reason 或 message 命中任一子串的事件。
func (c *Case) EventWith(ref model.ResourceRef, subs ...string) (model.EventInfo, bool) {
	for _, ev := range c.Events(ref) {
		if matchAny(ev.Reason, subs...) || matchAny(ev.Message, subs...) {
			return ev, true
		}
	}
	return model.EventInfo{}, false
}

// Component 返回系统组件探测结果（CoreDNS / CNI / kube-proxy 等）。
func (c *Case) Component(name string) (model.ComponentInfo, bool) {
	v, ok := c.E.comps[name]
	return v, ok
}

// Edge 返回两个资源之间的直接关系（无关系返回 false）。
func (c *Case) Edge(a, b model.ResourceRef) (relations.Edge, bool) {
	if c.E.Graph == nil {
		return relations.Edge{}, false
	}
	return c.E.Graph.DirectEdge(a, b)
}

// Connected 判断两个资源是否存在真实关系路径（判因果的准入门槛）。
func (c *Case) Connected(a, b model.ResourceRef) bool {
	if c.E.Graph == nil {
		return false
	}
	return c.E.Graph.Connected(a, b)
}

// CausalEdge 构造一条因果边；两个资源之间没有真实关系时拒绝建边（返回 false）。
func (c *Case) CausalEdge(src, dst *model.Finding, mechanism string) (model.CausalEdge, bool) {
	if src == nil || dst == nil || src.ID == "" || src.ID == dst.ID {
		return model.CausalEdge{}, false
	}
	relation, ok := c.relationPath(src.Resource, dst.Resource)
	if !ok {
		return model.CausalEdge{}, false
	}
	return model.CausalEdge{
		SourceFinding:  src.ID,
		TargetFinding:  dst.ID,
		SourceResource: src.Resource,
		TargetResource: dst.Resource,
		RelationType:   relation,
		Mechanism:      mechanism,
		Rule:           src.Rule + "->" + dst.Rule,
		EvidenceRefs:   dedupe(append(evIDs(src), evIDs(dst)...)),
		Confidence:     model.RootCauseProbable,
	}, true
}

// relationPath 返回两个资源之间的关系类型，用于让每条因果边都能自证传播路径
// （需求 §3.3）：同一对象上是 sameObject，直接关系用边类型，多跳关系把路径上
// 的边类型依次串起来（如 "ownedBy->selectedBy"）。找不到关系则返回 false。
func (c *Case) relationPath(a, b model.ResourceRef) (string, bool) {
	if a == b {
		return "sameObject", true
	}
	if c.E.Graph == nil {
		return "", false
	}
	if edge, direct := c.Edge(a, b); direct {
		return string(edge.Type), true
	}
	path, ok := c.E.Graph.Path(a, b)
	if !ok || len(path) == 0 {
		return "", false
	}
	types := make([]string, 0, len(path))
	for _, edge := range path {
		if edge.Type == "" {
			return "", false
		}
		types = append(types, string(edge.Type))
	}
	return strings.Join(types, "->"), true
}

// Evaluate 对该 Incident 跑全部命中的诊断器（按可信度降序返回）。
func (e *Engine) Evaluate(inc model.Incident) []Result {
	c := e.CaseOf(inc)
	var out []Result
	for _, d := range registry {
		if !safeMatch(d, c) {
			continue
		}
		r := safeDiagnose(d, c)
		if r.RootCause == "" {
			continue
		}
		r.Diagnoser = d.ID()
		out = append(out, normalize(r, c))
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := statusRank(out[i].Status), statusRank(out[j].Status)
		if si != sj {
			return si > sj
		}
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Diagnoser < out[j].Diagnoser
	})
	return out
}

// Apply 对全部 Incident 跑确定性诊断：就地写入根因判定字段，
// 并返回可直接并入 ScanResult.Diagnoses 的结论列表。
func (e *Engine) Apply(incs []model.Incident) []model.Diagnosis {
	var out []model.Diagnosis
	for i := range incs {
		inc := &incs[i]
		c := e.CaseOf(*inc)
		results := e.Evaluate(*inc)
		if len(results) == 0 {
			if inc.RootCauseStatus == "" {
				inc.RootCauseStatus = model.RootCauseUnknown
				inc.RootCauseMethod = model.DiagnosticMethodNone
				inc.Confidence = ConfUnknown
				inc.ConfidenceBasis = "无诊断器覆盖该故障链"
				inc.EvidenceGaps = append(inc.EvidenceGaps,
					fmt.Sprintf("规则 %s 尚无对应的确定性诊断器，根因需人工确认", orDash(c.Root.Rule)))
			}
			continue
		}
		applyToIncident(inc, results)
		for _, r := range results {
			out = append(out, r.diagnosis(inc))
		}
	}
	return out
}

func statusRank(status string) int {
	switch status {
	case model.RootCauseConfirmed:
		return 3
	case model.RootCauseProbable:
		return 2
	case model.RootCauseUnknown:
		return 1
	default:
		return 0
	}
}

func safeMatch(d Diagnoser, c *Case) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return d.Match(c)
}

func safeDiagnose(d Diagnoser, c *Case) (r Result) {
	defer func() {
		if recover() != nil {
			r = Result{}
		}
	}()
	return d.Diagnose(c)
}

// normalize 兜住三条底线：证据 ID 必须真实存在、无证据不得 confirmed、
// 非 confirmed 结论必须说明缺口。
func normalize(r Result, c *Case) Result {
	if r.AnchorFinding == "" {
		r.AnchorFinding = c.Root.ID
	}
	if r.Status == "" {
		r.Status = model.RootCauseProbable
	}
	known := knownEvidence(c)
	var kept []string
	for _, id := range r.EvidenceRefs {
		if known[id] {
			kept = append(kept, id)
		}
	}
	r.EvidenceRefs = dedupe(kept)
	if len(r.EvidenceRefs) == 0 {
		if r.Status == model.RootCauseConfirmed {
			r.Status = model.RootCauseProbable
			r.ConfidenceBasis = strings.TrimSpace(r.ConfidenceBasis + " 结论未挂到本次扫描的真实证据 ID，由 confirmed 下调为 probable")
		}
		if r.Confidence > ConfClue {
			r.Confidence = ConfClue
		}
	}
	if r.Confidence == 0 {
		r.Confidence = ConfInferred
	}
	if r.ConfidenceBasis == "" {
		r.ConfidenceBasis = confidenceBasis(r)
	}
	if r.Status != model.RootCauseConfirmed && len(r.EvidenceGaps) == 0 {
		r.EvidenceGaps = append(r.EvidenceGaps, "现有证据不足以排除其他原因，结论按 probable 处理")
	}
	if r.RemediationDirection == "" {
		r.RemediationDirection = "按排查命令逐项确认后再执行修复动作"
	}
	if r.Summary == "" {
		r.Summary = r.RootCause
	}
	return r
}

func confidenceBasis(r Result) string {
	switch r.Status {
	case model.RootCauseConfirmed:
		return fmt.Sprintf("依据本次扫描采集到的 %d 条证据（对象状态字段与事件/日志互相印证）", len(r.EvidenceRefs))
	case model.RootCauseUnknown:
		return "证据不足，无法定位根因"
	default:
		return fmt.Sprintf("依据本次扫描采集到的 %d 条证据，但仍存在未验证的可能原因", len(r.EvidenceRefs))
	}
}

func knownEvidence(c *Case) map[string]bool {
	out := map[string]bool{}
	for _, f := range c.Items {
		for _, ev := range f.Evidence {
			out[ev.ID] = true
		}
	}
	return out
}

func applyToIncident(inc *model.Incident, results []Result) {
	primary := results[0]
	inc.RootCauseStatus = primary.Status
	inc.RootCause = primary.RootCause
	inc.RootCauseRule = primary.Diagnoser
	if inc.RootCauseMethod == "" || inc.RootCauseMethod == model.DiagnosticMethodNone {
		inc.RootCauseMethod = model.DiagnosticMethodRule
	}
	inc.Confidence = primary.Confidence
	inc.ConfidenceBasis = primary.ConfidenceBasis
	if len(primary.ImpactChain) > 0 {
		inc.ImpactChain = primary.ImpactChain
	}
	inc.EvidenceRefs = dedupe(append(append([]string{}, primary.EvidenceRefs...), inc.EvidenceRefs...))
	inc.EvidenceGaps = dedupe(append(append([]string{}, primary.EvidenceGaps...), inc.EvidenceGaps...))
	inc.CausalEdges = mergeEdges(inc.CausalEdges, primary.CausalEdges)
	var alts []string
	for _, r := range results {
		alts = append(alts, r.AlternativeCauses...)
	}
	if len(alts) > 0 {
		inc.AlternativeCauses = dedupe(alts)
	}
}

func mergeEdges(base, add []model.CausalEdge) []model.CausalEdge {
	seen := map[string]bool{}
	var out []model.CausalEdge
	for _, group := range [][]model.CausalEdge{base, add} {
		for _, e := range group {
			key := e.SourceFinding + ">" + e.TargetFinding + ">" + e.RelationType
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}

// diagnosis 把确定性结论转成报告里的 Diagnosis 条目（Source=deterministic，LLMUsed=false）。
func (r Result) diagnosis(inc *model.Incident) model.Diagnosis {
	fid := r.AnchorFinding
	if fid == "" {
		fid = inc.Root.ID
	}
	return model.Diagnosis{
		FindingID:            fid,
		IncidentID:           inc.ID,
		Summary:              r.Summary,
		RootCause:            r.RootCause,
		Confidence:           r.Confidence,
		ConfidenceLevel:      confidenceLevel(r.Status, r.Confidence),
		ConfidenceBasis:      r.ConfidenceBasis,
		CausalChain:          r.Mechanism,
		EvidenceChain:        r.EvidenceRefs,
		Impact:               r.Impact,
		ImpactChain:          r.ImpactChain,
		AlternativeCauses:    r.AlternativeCauses,
		EvidenceGaps:         r.EvidenceGaps,
		Investigation:        r.Investigation,
		Remediation:          r.Remediation,
		RemediationText:      r.RemediationText,
		RemediationDirection: r.RemediationDirection,
		Verification:         r.Verification,
		LLMUsed:              false,
		Source:               "deterministic",
		Status:               model.DiagStatusDeterministic,
		RootCauseStatus:      r.Status,
		Diagnoser:            r.Diagnoser,
	}
}

func confidenceLevel(status string, conf float64) string {
	switch {
	case status == model.RootCauseConfirmed:
		return "CONFIRMED"
	case status == model.RootCauseProbable && conf >= ConfEventMessage:
		return "HIGH_CONFIDENCE"
	case status == model.RootCauseProbable:
		return "POSSIBLE"
	default:
		return "UNKNOWN"
	}
}
