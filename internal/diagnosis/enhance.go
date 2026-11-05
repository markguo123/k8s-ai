package diagnosis

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/relations"
)

// 确定性诊断层 → LLM 的增强上下文（需求 §4.2 / §4.3）。
//
// 分层口径：deterministic 包不依赖 LLM，diagnosis 包不 import deterministic 包，
// 两边只在 internal/service 里汇合。本结构是「程序已经知道的事实」的载体：
// LLM 拿到的不再是「一条 Finding + 一堆证据」，而是「资源关系 + 真实状态 +
// 程序已经给出的结论与缺口」。这直接决定增强是「补细节」还是「重新猜一遍」。
type Context struct {
	Snapshot *model.ClusterSnapshot
	Index    *model.CorrelationIndex
	Graph    *relations.Graph

	// Deterministic：incidentID → 确定性诊断结论。LLM 只能在其后追加，
	// 不能覆盖已确认事实（confirmed 的根因不允许被改写）。
	Deterministic map[string]model.Diagnosis
}

// NewWithContext 创建带确定性诊断增强结果的诊断器。
//
// dc 为 nil 时行为与 New 完全一致（保持既有调用方与测试的语义）。
func NewWithContext(opts model.LLMOptions, dc *Context) Diagnoser {
	return &diagnoser{opts: opts, system: llm.SystemPrompt(), dc: dc}
}

// enhanceText 构造单个 Incident 的增强上下文段落。
//
// 只写「快照里真实存在的东西」：关系边带出处、状态带字段路径、证据带 ID。
func (d *diagnoser) enhanceText(inc model.Incident, findingByID map[string]model.Finding) string {
	if d.dc == nil {
		return ""
	}
	var b strings.Builder

	if det, ok := d.dc.Deterministic[inc.ID]; ok {
		b.WriteString("\n[程序已给出的确定性结论]（不得推翻，只能补充细节或指出程序未覆盖的可能）\n")
		fmt.Fprintf(&b, "- 根因状态：%s　诊断器：%s\n", orNoneStr(det.RootCauseStatus), orNoneStr(det.Diagnoser))
		if det.RootCause != "" {
			fmt.Fprintf(&b, "- 根因：%s\n", det.RootCause)
		}
		if det.ConfidenceBasis != "" {
			fmt.Fprintf(&b, "- 可信度依据：%s\n", det.ConfidenceBasis)
		}
		for _, g := range det.EvidenceGaps {
			fmt.Fprintf(&b, "- 程序明确承认的证据缺口：%s\n", g)
		}
		if det.RootCauseStatus == model.RootCauseConfirmed {
			b.WriteString("- 注意：该根因已确认证据充分。若你认为不成立，必须在 summary 中给出具体证据编号，否则不得改写。\n")
		}
	}

	// 故障链成员的编号化证据：LLM 引用成员证据时必须写 <findingID>#<E-ID>。
	if len(inc.Members) > 0 {
		b.WriteString("\n[同一故障链的其他异常（引用其证据时写 findingID#证据ID）]\n")
		for _, m := range inc.Members {
			f, ok := findingByID[m.ID]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "- %s %s/%s %s（%s）证据：", m.ID, m.Resource.Namespace, m.Resource.Name, m.Rule, m.Severity)
			ids := make([]string, 0, len(f.Evidence))
			for _, e := range f.Evidence {
				ids = append(ids, m.ID+"#"+e.ID)
			}
			if len(ids) == 0 {
				b.WriteString("（无）\n")
				continue
			}
			b.WriteString(strings.Join(ids, " ") + "\n")
		}
	}

	// 资源关系：只列根因资源一跳内的真实边（含出处字段）。
	if d.dc.Graph != nil {
		refs := []model.ResourceRef{inc.Root.Resource}
		for _, m := range inc.Members {
			if len(refs) < 5 {
				refs = append(refs, m.Resource)
			}
		}
		text := d.dc.Graph.TextFor(refs, 18)
		if strings.TrimSpace(text) != "" {
			b.WriteString("\n[资源关系（来自 OwnerReference/Selector/Spec 字段，非推测）]\n")
			b.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				b.WriteString("\n")
			}
		}
	}

	// 资源真实状态与快照时间：抑制「模型编造资源」。
	if facts := d.resourceFacts(inc, findingByID); facts != "" {
		b.WriteString(facts)
	}
	if d.dc.Snapshot != nil && d.dc.Snapshot.CollectedAt != "" {
		fmt.Fprintf(&b, "\n[数据采集时间：%s；此后发生的变化不在本报告范围内]\n", d.dc.Snapshot.CollectedAt)
	}
	b.WriteString("\n[禁止事项] 不得提及本上下文未出现的资源名或命名空间；不得引用不存在的证据编号；" +
		"证据不足时把 rootCause 写为无法确定并在 uncertainty 说明缺什么，不要猜。\n")
	return b.String()
}

// resourceFacts 输出故障链涉及对象的真实状态。
func (d *diagnoser) resourceFacts(inc model.Incident, findingByID map[string]model.Finding) string {
	snap := d.dc.Snapshot
	if snap == nil {
		return ""
	}
	pods := map[string]*model.PodInfo{}
	for i := range snap.Pods {
		p := &snap.Pods[i]
		pods[p.Ref.Key()] = p
	}
	nodes := map[string]*model.NodeInfo{}
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		nodes[n.Ref.Name] = n
	}
	var refs []model.ResourceRef
	refs = append(refs, inc.Root.Resource)
	for _, m := range inc.Members {
		refs = append(refs, m.Resource)
	}
	var lines []string
	for _, r := range refs {
		switch r.Kind {
		case "Pod":
			p, ok := pods[r.Key()]
			if !ok {
				lines = append(lines, fmt.Sprintf("- Pod %s/%s：快照中不存在（可能已删除）", r.Namespace, r.Name))
				continue
			}
			var cs []string
			for _, c := range p.Containers {
				st := c.State
				if st == "" {
					st = c.LastState
				}
				cs = append(cs, fmt.Sprintf("%s=%s/%s rc=%d restarts=%d", c.Name, st, orNoneStr(c.Reason), c.ExitCode, c.RestartCount))
			}
			line := fmt.Sprintf("- Pod %s/%s：phase=%s node=%s 容器[%s]", r.Namespace, r.Name, orNoneStr(p.Phase), orNoneStr(p.NodeName), strings.Join(cs, ", "))
			if len(p.Logs) > 0 {
				line += "（已采集容器日志）"
			} else {
				line += "（未采集容器日志）"
			}
			lines = append(lines, line)
		case "Node":
			n, ok := nodes[r.Name]
			if !ok {
				lines = append(lines, fmt.Sprintf("- Node %s：快照中不存在", r.Name))
				continue
			}
			lines = append(lines, fmt.Sprintf("- Node %s：%s", n.Ref.Name, strings.Join(condText(n.Conditions), "; ")))
		}
	}
	// 根因对象的事件（最多 6 条）：事件是「为什么」的主要来源。
	if d.dc.Index != nil {
		for _, e := range d.dc.Index.EventsFor(inc.Root.Resource) {
			if len(lines) >= 16 {
				break
			}
			lines = append(lines, fmt.Sprintf("- Event %s %s/%s：%s", orNoneStr(e.Type), orNoneStr(e.Reason),
				orNoneStr(e.InvolvedObject.Name), truncateText(e.Message, 160)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	var b strings.Builder
	b.WriteString("\n[资源真实状态（只读采集，不得改写）]\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

func condText(cs []model.ConditionInfo) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s=%s", c.Type, c.Status))
	}
	if len(out) == 0 {
		return []string{"无 Conditions"}
	}
	return out
}

func orNoneStr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// capText 按 token 预算截断增强文本，按 rune 边界收口（避免截出半个汉字）。
// 预算给得紧时优先保住开头的确定性结论段。
func capText(s string, maxTokens int) string {
	if maxTokens <= 0 || estimateTokens(s) <= maxTokens {
		return s
	}
	limit := maxTokens * 4
	if limit >= len(s) {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…（增强上下文已达预算上限）\n"
}
