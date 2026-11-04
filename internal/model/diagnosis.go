package model

// Command is a generated kubectl command. It is display-only: nothing in the
// code base may execute it (ADR-014).
type Command struct {
	Category CommandCategory `json:"category"`
	Text     string          `json:"text"`
	Risk     RiskLevel       `json:"risk"`
}

// Diagnosis is the LLM analysis result for one Finding. On degradation
// LLMUsed is false and Error explains why (ADR-005).
// latest.json 中的该数据（二期语境 diagnosis_context）设计保持不变，
// 专供二期 Agent 的 --attach-report 或后台自动调用 scan Tool 时读取，用于继承历史诊断记忆。
//
// 四期 P9 起 Diagnosis 不再只有 LLM 一种来源（需求 §5「无 LLM 不是低质量模式」）：
// Source 说明这条结论是谁给的（确定性诊断 / LLM / 两者合并），Status 说明 LLM 这一次
// 到底是没配、超时、还是返回了不能用的东西，Validation 记录 LLM 结论过没过证据校验。
// 三条硬约束：LLM 不能覆盖已采集事实；没有证据的根因不得标 confirmed；降级原因必须可追责。
type Diagnosis struct {
	FindingID            string    `json:"findingId"`
	IncidentID           string    `json:"incidentId,omitempty"`
	Summary              string    `json:"summary"`
	RootCause            string    `json:"rootCause"`
	Confidence           float64   `json:"confidence"`                // 0.0–1.0（数值置信度）
	ConfidenceLevel      string    `json:"confidenceLevel,omitempty"` // CONFIRMED/HIGH_CONFIDENCE/POSSIBLE/UNKNOWN
	CausalChain          string    `json:"causalChain,omitempty"`     // 因果链解释（system.md §25 Diagnosis）
	Uncertainty          string    `json:"uncertainty,omitempty"`     // 不确定性说明
	EvidenceChain        []string  `json:"evidenceChain"`
	Impact               string    `json:"impact"`
	PossibleCauses       []string  `json:"possibleCauses,omitempty"`
	Investigation        []Command `json:"investigation,omitempty"`
	Remediation          []Command `json:"remediation,omitempty"`
	RemediationText      string    `json:"remediationText,omitempty"`      // 修复文字说明（做什么/为什么/预期结果）；命令只是辅助，文字永不缺失
	RemediationDirection string    `json:"remediationDirection,omitempty"` // 修复方向（无法确定精确修改时的确定性指导，永不空）
	Verification         []Command `json:"verification,omitempty"`
	LLMUsed              bool      `json:"llmUsed"`
	Error                string    `json:"error,omitempty"`

	// 四期 P9 诊断来源与校验（需求 §4.3 / §5 / §7.3）。
	Source            string               `json:"source,omitempty"`          // rule|deterministic|llm|hybrid
	Status            string               `json:"status,omitempty"`          // 见 LLMStatus* / DiagStatusDeterministic
	RootCauseStatus   string               `json:"rootCauseStatus,omitempty"` // confirmed|probable|unknown
	Diagnoser         string               `json:"diagnoser,omitempty"`       // 命中的诊断器 ID（确定性层）
	ConfidenceBasis   string               `json:"confidenceBasis,omitempty"`
	ImpactChain       []string             `json:"impactChain,omitempty"`
	AlternativeCauses []string             `json:"alternativeCauses,omitempty"`
	EvidenceGaps      []string             `json:"evidenceGaps,omitempty"`
	Validation        *DiagnosisValidation `json:"validation,omitempty"`
}

// DiagStatusDeterministic 表示这条结论完全由确定性诊断给出（未调用 LLM）。
const DiagStatusDeterministic = "deterministic"

// DiagnosisValidation 是一次「LLM 结论 vs 已采集证据」的一致性校验结果（需求 §4.3）。
//
// 校验不通过不等于 LLM 胡说，多数情况是它引用了不存在的证据 ID 或报了快照里没有的
// 资源名；无论哪种，结论都不能升格成 confirmed，必须退回确定性诊断并留下原因。
type DiagnosisValidation struct {
	Passed             bool     `json:"passed"`
	Rule               string   `json:"rule,omitempty"` // 执行校验的口径版本
	Issues             []string `json:"issues,omitempty"`
	FabricatedEvidence []string `json:"fabricatedEvidence,omitempty"` // LLM 引用了不存在的证据 ID
	UnknownResources   []string `json:"unknownResources,omitempty"`   // LLM 提到快照里不存在的资源
	CommandIssues      []string `json:"commandIssues,omitempty"`      // LLM 命令因目标/参数不完整被过滤的摘要
	Downgraded         bool     `json:"downgraded,omitempty"`         // 因证据不足下调了置信度
}
