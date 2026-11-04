package service

import (
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// mergeDiagnoses 收口「确定性诊断 × LLM 增强」两条结论线（架构图第 6/7/8 阶段）。
//
// 三条口径写死在这里：
//  1. 没配 LLM 时报告也必须有结论——确定性诊断直接当最终结论，不留空；
//  2. LLM 只能在确定性结论之后补充，不能推翻已确认事实；
//  3. LLM 结论没过一致性校验时 diagnosis 层已退回确定性结论，这里只负责记账。
func mergeDiagnoses(incs []model.Incident, det map[string]model.Diagnosis,
	llmDiags []model.Diagnosis, sum model.LLMSummary, llmEnabled bool) ([]model.Diagnosis, model.DiagnosisSummary) {
	byIncident := make(map[string]model.Diagnosis, len(llmDiags))
	for _, d := range llmDiags {
		if d.IncidentID != "" {
			byIncident[d.IncidentID] = d
		}
	}

	ds := model.DiagnosisSummary{Mode: diagnosisMode(llmEnabled, sum)}
	out := make([]model.Diagnosis, 0, len(incs))
	for _, inc := range incs {
		d, fromLLM := byIncident[inc.ID]
		detD, hasDet := det[inc.ID]
		var final model.Diagnosis
		switch {
		case fromLLM && d.LLMUsed && (d.Validation == nil || d.Validation.Passed):
			final = mergeHybrid(d, detD, hasDet)
			ds.Hybrid++
		case fromLLM:
			// 校验不通过：内容已被换成确定性结论，这里补回诊断器身份。
			final = d
			if hasDet && final.Diagnoser == "" {
				final.Diagnoser = detD.Diagnoser
			}
		case hasDet:
			final = detD
		default:
			continue
		}
		if final.IncidentID == "" {
			final.IncidentID = inc.ID
		}
		if final.FindingID == "" {
			final.FindingID = inc.Root.ID
		}
		enforceDiagnosisInvariants(&final)
		out = append(out, final)

		if hasDet {
			ds.Deterministic++
		}
		switch final.RootCauseStatus {
		case model.RootCauseConfirmed:
			ds.Confirmed++
		case model.RootCauseProbable:
			ds.Probable++
		default:
			ds.Unknown++
		}
	}

	ds.LLMCalls = sum.Calls
	ds.LLMSucceeded = sum.Succeeded
	ds.LLMFailed = sum.Failed
	ds.LLMDegraded = sum.Degraded
	ds.LLMRejected = sum.Rejected
	ds.Degradations = sum.Degradations
	return out, ds
}

// mergeHybrid 以 LLM 叙述为主体，把确定性诊断的事实并进去。
//
// 关键在方向：模型的话是「解释」，程序的字段是「事实」。已确认的根因状态、
// 证据缺口、影响链一律以程序侧为准，模型顶多补充文本。
func mergeHybrid(d model.Diagnosis, det model.Diagnosis, hasDet bool) model.Diagnosis {
	d.Source = "hybrid"
	if !hasDet {
		return d
	}
	d.Diagnoser = det.Diagnoser
	if d.ConfidenceBasis == "" {
		d.ConfidenceBasis = det.ConfidenceBasis
	}
	if len(d.ImpactChain) == 0 {
		d.ImpactChain = det.ImpactChain
	}
	d.EvidenceGaps = mergeStrings(det.EvidenceGaps, d.EvidenceGaps)
	// 根因状态取两者更强的一档：程序已确认的，模型不能降级；
	// 程序说未知的，模型可以基于文本证据升到 probable（校验层已限制它到不了 confirmed）。
	if rankRootCause(det.RootCauseStatus) > rankRootCause(d.RootCauseStatus) {
		d.RootCauseStatus = det.RootCauseStatus
		if det.RootCauseStatus == model.RootCauseConfirmed && d.Confidence < det.Confidence {
			d.Confidence = det.Confidence
		}
	}
	if det.RootCauseStatus == model.RootCauseConfirmed {
		d.ConfidenceLevel = "CONFIRMED"
	}
	return d
}

// enforceDiagnosisInvariants 是「结果校验」阶段的程序侧底线：结论必须自洽。
func enforceDiagnosisInvariants(d *model.Diagnosis) {
	if d.RootCauseStatus == "" {
		if len(d.EvidenceChain) > 0 && d.Confidence >= 0.3 {
			d.RootCauseStatus = model.RootCauseProbable
		} else {
			d.RootCauseStatus = model.RootCauseUnknown
		}
	}
	if d.Source == "" {
		if d.LLMUsed {
			d.Source = "llm"
		} else if d.Diagnoser != "" {
			d.Source = "deterministic"
		} else {
			d.Source = "rule"
		}
	}
	if d.Status == "" {
		if d.LLMUsed {
			d.Status = model.LLMStatusSuccess
		} else {
			d.Status = model.DiagStatusDeterministic
		}
	}
	// 声称已确认却不引用任何证据：这种结论不能留在报告里当事实。
	if d.RootCauseStatus == model.RootCauseConfirmed && len(d.EvidenceChain) == 0 {
		d.RootCauseStatus = model.RootCauseProbable
		if d.Validation == nil {
			d.Validation = &model.DiagnosisValidation{Rule: "p9-report-invariants-v1"}
		}
		d.Validation.Passed = false
		d.Validation.Issues = append(d.Validation.Issues, "结论标记为已确认但未引用任何证据，已下调为可能原因")
		d.Validation.Downgraded = true
		d.Confidence = minFloat(d.Confidence, 0.6)
	}
}

func rankRootCause(status string) int {
	switch status {
	case model.RootCauseConfirmed:
		return 3
	case model.RootCauseProbable:
		return 2
	default:
		return 1
	}
}

// diagnosisMode 给出本次体检的诊断口径（报告与 Web 用它解释「为什么没有 LLM 结论」）。
func diagnosisMode(llmEnabled bool, sum model.LLMSummary) string {
	if !llmEnabled || sum.Calls == 0 {
		return "rule_only"
	}
	if sum.Succeeded > 0 {
		return "llm_enhanced"
	}
	return "llm_degraded"
}

// mergeStrings 按出现顺序合并去重（确定性结论排在前面）。
func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// diagnosisAccountLine 是阶段明细里的一行人读诊断账（避免把结构体直接甩给用户）。
func diagnosisAccountLine(ds model.DiagnosisSummary) string {
	return fmt.Sprintf("确定性结论 %d（确认 %d/可能 %d/未知 %d）、LLM 调用 %d 成功 %d 退回 %d，口径 %s",
		ds.Deterministic, ds.Confirmed, ds.Probable, ds.Unknown,
		ds.LLMCalls, ds.LLMSucceeded, ds.LLMRejected, ds.Mode)
}

// indexByIncident 按故障链 ID 索引确定性结论。
func indexByIncident(diags []model.Diagnosis) map[string]model.Diagnosis {
	out := make(map[string]model.Diagnosis, len(diags))
	for _, d := range diags {
		if d.IncidentID != "" {
			out[d.IncidentID] = d
		}
	}
	return out
}

// countCausalEdges 统计故障链内已建立的因果边总数（阶段明细用）。
func countCausalEdges(incs []model.Incident) int {
	n := 0
	for _, inc := range incs {
		n += len(inc.CausalEdges)
	}
	return n
}

// conclusionLevelText 输出一行结论档位（阶段明细用，不重复渲染整段结论）。
func conclusionLevelText(r *model.ScanResult) string {
	if r.Conclusion == nil {
		return "none"
	}
	return r.Conclusion.Level
}
