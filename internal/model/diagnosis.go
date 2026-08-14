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
	FindingID      string    `json:"findingId"`
	Summary        string    `json:"summary"`
	RootCause      string    `json:"rootCause"`
	Confidence     float64   `json:"confidence"` // 0.0–1.0
	EvidenceChain  []string  `json:"evidenceChain"`
	Impact         string    `json:"impact"`
	PossibleCauses []string  `json:"possibleCauses,omitempty"`
	Investigation  []Command `json:"investigation,omitempty"`
	Remediation    []Command `json:"remediation,omitempty"`
	Verification   []Command `json:"verification,omitempty"`
	LLMUsed        bool      `json:"llmUsed"`
	Error          string    `json:"error,omitempty"`
}
