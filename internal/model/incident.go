package model

// Incident 是将因果相关的 Findings 聚合后的诊断单元：
// Finding → Correlation → Incident → LLM 诊断（同一故障链只调用一次 LLM）。
// Root 为最底层可证实原因，Members 为派生症状（不单独分析/不重复扣分）。
//
// 四期 P9 起 Incident 同时承载「根因判定」这一层（需求 §3.3）：
//   - RootCauseStatus 只有 confirmed / probable / unknown 三种，unknown 是合法结论，
//     不允许为了报告好看把症状硬拗成根因；
//   - 可信度来自「诊断器声明的证据充分度」而非模型自评分，见 deterministic 包；
//   - CausalEdges/ImpactChain 必须由真实资源关系 + 真实证据支撑，同命名空间不构成因果。
type Incident struct {
	ID       string       `json:"id"` // 取 Root Finding 的稳定指纹
	Title    string       `json:"title"`
	Severity Severity     `json:"severity"`
	Root     FindingRef   `json:"root"`
	Members  []FindingRef `json:"members"`

	// 根因判定（确定性诊断层写入；LLM 只能在其后追加增强）。
	RootCauseStatus   string       `json:"rootCauseStatus,omitempty"` // confirmed|probable|unknown
	RootCause         string       `json:"rootCause,omitempty"`
	RootCauseRule     string       `json:"rootCauseRule,omitempty"`   // 命中的诊断器 ID
	RootCauseMethod   string       `json:"rootCauseMethod,omitempty"` // rule|llm|hybrid|none
	Confidence        float64      `json:"confidence,omitempty"`
	ConfidenceBasis   string       `json:"confidenceBasis,omitempty"` // 可信度依据（可读，不是拍脑袋百分比）
	CausalEdges       []CausalEdge `json:"causalEdges,omitempty"`
	ImpactChain       []string     `json:"impactChain,omitempty"`
	EvidenceRefs      []string     `json:"evidenceRefs,omitempty"`
	EvidenceGaps      []string     `json:"evidenceGaps,omitempty"`
	AlternativeCauses []string     `json:"alternativeCauses,omitempty"`
	Independent       bool         `json:"independent,omitempty"` // 独立异常（未与任何资源形成故障链）
}

// 根因状态取值。
const (
	RootCauseConfirmed = "confirmed"
	RootCauseProbable  = "probable"
	RootCauseUnknown   = "unknown"
)

// 根因判定来源。
const (
	DiagnosticMethodNone   = "none"
	DiagnosticMethodRule   = "rule"   // 仅确定性诊断
	DiagnosticMethodLLM    = "llm"    // 仅 LLM（确定性诊断未覆盖时）
	DiagnosticMethodHybrid = "hybrid" // 确定性诊断 + LLM 增强
)

// CausalEdge 是两条 Finding 之间的一条因果假设：source 故障导致 target 症状。
//
// 与 CorrelationIndex 的区别要说清楚：CorrelationIndex 表达的是 Kubernetes 的
// 资源依赖（Pod 依赖 PVC），CausalEdge 表达的是故障传播（PVC Pending 让 Pod
// 挂载失败）。每条边都必须带 Mechanism（故障传导机制）与 EvidenceRefs，
// 否则就不该出现在报告里。
type CausalEdge struct {
	SourceFinding  string      `json:"sourceFinding"`
	TargetFinding  string      `json:"targetFinding"`
	SourceResource ResourceRef `json:"sourceResource"`
	TargetResource ResourceRef `json:"targetResource"`
	RelationType   string      `json:"relationType"` // 资源关系类型，见 relations 包
	Mechanism      string      `json:"mechanism"`    // 故障如何从 source 传到 target
	Rule           string      `json:"rule,omitempty"`
	EvidenceRefs   []string    `json:"evidenceRefs,omitempty"`
	Confidence     string      `json:"confidence"` // confirmed|probable
}
