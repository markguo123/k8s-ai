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
type Diagnosis struct {
	FindingID            string    `json:"findingId"`
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
}
