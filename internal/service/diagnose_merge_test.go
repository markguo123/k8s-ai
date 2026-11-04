package service

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestHybridDiagnosisKeepsLLMPlanAndSafeCommandBoundary(t *testing.T) {
	llm := model.Diagnosis{
		IncidentID: "I-1", FindingID: "F-1", LLMUsed: true,
		Summary:              "外部 topic 不存在导致启动 panic",
		RootCause:            "RocketMQ topic 不存在，应用启动时 panic",
		RemediationText:      "**修复目标**：恢复消息消费者。\n\n1. 确认 topic 名称。\n2. 在 RocketMQ 创建 topic。\n\n**预期结果**：Pod Ready。",
		RemediationDirection: "topic 规格与消费组需人工确认；确认前不生成写命令。",
		Investigation: []model.Command{{
			Category: model.CmdInvestigation,
			Text:     "kubectl -n agentops get configmap naviapp-backend-env -o yaml",
			Risk:     model.RiskSafe,
		}},
		Verification: []model.Command{{
			Category: model.CmdVerification,
			Text:     "kubectl -n agentops get deployment naviapp-backend",
			Risk:     model.RiskSafe,
		}},
	}
	det := model.Diagnosis{
		IncidentID: "I-1", FindingID: "F-1",
		RemediationText:      "重启 Pod。",
		RemediationDirection: "排查日志后重启。",
		Remediation: []model.Command{{
			Category: model.CmdRemediation,
			Text:     "kubectl -n agentops rollout restart deployment naviapp-backend",
			Risk:     model.RiskLow,
		}},
	}

	got := mergeHybrid(llm, det, true)
	if !strings.Contains(got.RemediationText, "修复目标") || !strings.Contains(got.RemediationDirection, "不生成写命令") {
		t.Fatalf("LLM 完整文字方案必须覆盖确定性兜底：%+v", got)
	}
	if len(got.Remediation) != 0 {
		t.Fatalf("LLM 明确不给写命令时不得合并确定性写命令：%+v", got.Remediation)
	}
	if len(got.Investigation) != 1 || len(got.Verification) != 1 {
		t.Fatalf("LLM 排查/验证命令必须保留：%+v", got)
	}
}

func TestFinalDiagnosisBecomesIncidentRootProjection(t *testing.T) {
	incs := []model.Incident{{
		ID:                "I-1",
		RootCause:         "容器反复退出",
		RootCauseRule:     "POD_CRASHLOOP",
		RootCauseStatus:   model.RootCauseProbable,
		RootCauseMethod:   model.DiagnosticMethodRule,
		Confidence:        0.6,
		ConfidenceBasis:   "规则命中",
		EvidenceGaps:      []string{"旧缺口"},
		AlternativeCauses: []string{"旧猜测"},
	}}
	ds := []model.Diagnosis{{
		IncidentID:      "I-1",
		RootCause:       "RocketMQ topic 不存在导致启动 panic",
		RootCauseStatus: model.RootCauseConfirmed,
		Source:          "hybrid",
		Diagnoser:       "POD_CRASHLOOP",
		Confidence:      0.92,
		ConfidenceBasis: "日志与事件互证",
		EvidenceChain:   []string{"F-1#E2", "F-1#E5"},
		EvidenceGaps:    []string{"topic 规格未确认"},
		ImpactChain:     []string{"Pod/prod/api", "Deployment/prod/api"},
	}}

	applyDiagnosesToIncidents(incs, ds)
	got := incs[0]
	if got.RootCause != ds[0].RootCause || got.RootCauseStatus != model.RootCauseConfirmed ||
		got.RootCauseMethod != "hybrid" || got.Confidence != 0.92 {
		t.Fatalf("Incident 必须投影最终诊断根因：%+v", got)
	}
	if len(got.EvidenceRefs) != 2 || got.EvidenceRefs[0] != "F-1#E2" {
		t.Fatalf("Incident 证据引用必须来自最终诊断：%+v", got.EvidenceRefs)
	}
	if len(got.EvidenceGaps) != 1 || got.EvidenceGaps[0] != "topic 规格未确认" {
		t.Fatalf("Incident 证据缺口必须投影最终诊断：%+v", got.EvidenceGaps)
	}
}
