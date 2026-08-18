package diagnosis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// llmOutput 是 LLM 必须输出的结构化诊断（FR-016 JSON Schema）。
type llmOutput struct {
	Summary              string   `json:"summary"`
	RootCause            string   `json:"rootCause"`
	Confidence           float64  `json:"confidence"`
	ConfidenceLevel      string   `json:"confidenceLevel"` // CONFIRMED/HIGH_CONFIDENCE/POSSIBLE/UNKNOWN
	CausalChain          string   `json:"causalChain"`
	EvidenceChain        []string `json:"evidenceChain"`
	Impact               string   `json:"impact"`
	PossibleCauses       []string `json:"possibleCauses"`
	Investigation        []string `json:"investigation"`
	Remediation          []string `json:"remediation"`
	RemediationText      string   `json:"remediationText"` // 修复文字说明（必填：做什么/为什么/预期结果）
	Verification         []string `json:"verification"`
	Risk                 string   `json:"risk"`
	Uncertainty          string   `json:"uncertainty"`
	RemediationDirection string   `json:"remediationDirection,omitempty"`
}

// parseLLMOutput 提取并校验 JSON（支持 ```json 代码块包裹）。
func parseLLMOutput(content string) (*llmOutput, error) {
	s := extractJSON(content)
	var out llmOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("parse llm json: %w", err)
	}
	if err := validateSchema(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// extractJSON 从可能带 Markdown 代码块/前后文本的内容中提取 JSON 对象。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end >= start {
		return s[start : end+1]
	}
	return s
}

// validateSchema 校验必填字段与取值范围（ADR-005 第一层）。
func validateSchema(o *llmOutput) error {
	if strings.TrimSpace(o.Summary) == "" {
		return errors.New("llm output missing summary")
	}
	if strings.TrimSpace(o.RootCause) == "" {
		return errors.New("llm output missing rootCause")
	}
	if o.Confidence < 0 || o.Confidence > 1 {
		return errors.New("llm output confidence out of range")
	}
	if o.ConfidenceLevel != "" {
		switch o.ConfidenceLevel {
		case "CONFIRMED", "HIGH_CONFIDENCE", "POSSIBLE", "UNKNOWN":
		default:
			return fmt.Errorf("llm output invalid confidenceLevel %q", o.ConfidenceLevel)
		}
	}
	if o.Risk != "" {
		switch model.RiskLevel(o.Risk) {
		case model.RiskSafe, model.RiskLow, model.RiskMedium, model.RiskHigh, model.RiskCritical:
		default:
			return fmt.Errorf("llm output invalid risk %q", o.Risk)
		}
	}
	return nil
}

// buildDiagnosis 把 LLM 输出映射为 Diagnosis，并做 Evidence ID 与命令校验（ADR-005）。
func buildDiagnosis(f model.Finding, out *llmOutput) model.Diagnosis {
	d := model.Diagnosis{
		FindingID:            f.ID,
		Summary:              out.Summary,
		RootCause:            out.RootCause,
		Confidence:           out.Confidence,
		ConfidenceLevel:      out.ConfidenceLevel,
		CausalChain:          out.CausalChain,
		Impact:               out.Impact,
		PossibleCauses:       out.PossibleCauses,
		Uncertainty:          out.Uncertainty,
		RemediationText:      out.RemediationText,
		RemediationDirection: out.RemediationDirection,
		LLMUsed:              true,
	}
	d.EvidenceChain = filterEvidenceRefs(f.Evidence, out.EvidenceChain)
	for _, text := range out.Investigation {
		if cmd, ok := validateCommand(text, model.CmdInvestigation, f.Resource.Namespace); ok {
			d.Investigation = append(d.Investigation, cmd)
		}
	}
	for _, text := range out.Remediation {
		if cmd, ok := validateCommand(text, model.CmdRemediation, f.Resource.Namespace); ok {
			cmd.Risk = commandRisk(text, remediationRisk(out.Risk))
			d.Remediation = append(d.Remediation, cmd)
		}
	}
	for _, text := range out.Verification {
		if cmd, ok := validateCommand(text, model.CmdVerification, f.Resource.Namespace); ok {
			d.Verification = append(d.Verification, cmd)
		}
	}
	return d
}

// filterEvidenceRefs 只保留真实存在的 E-ID，丢弃编造的引用（ADR-005）。
func filterEvidenceRefs(evidence []model.Evidence, refs []string) []string {
	valid := map[string]bool{}
	for _, e := range evidence {
		valid[e.ID] = true
	}
	var out []string
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if valid[r] {
			out = append(out, r)
		}
	}
	return out
}

// remediationRisk 修复命令风险：LLM 未提供合法值时按动词计算。
func remediationRisk(risk string) model.RiskLevel {
	switch model.RiskLevel(risk) {
	case model.RiskSafe, model.RiskLow, model.RiskMedium, model.RiskHigh, model.RiskCritical:
		return model.RiskLevel(risk)
	default:
		return model.RiskMedium
	}
}
