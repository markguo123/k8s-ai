// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestHpaReplicasMismatchRule HPA 期望副本与当前副本不一致时命中；一致或期望为 0 跳过。
func TestHpaReplicasMismatchRule(t *testing.T) {
	mk := func(hpa model.HpaInfo) *RuleContext {
		return ctxFor(&model.ClusterSnapshot{Hpas: []model.HpaInfo{hpa}})
	}
	base := model.HpaInfo{Ref: model.ResourceRef{Kind: "HPA", Namespace: "default", Name: "web"}}
	// 期望 3 当前 1：命中 Medium。
	fs := (HpaReplicasMismatchRule{}).Evaluate(mk(func() model.HpaInfo {
		h := base
		h.DesiredReplicas = 3
		h.CurrentReplicas = 1
		return h
	}()))
	if len(fs) != 1 || fs[0].Severity != model.SeverityMedium {
		t.Fatalf("副本不一致应命中 Medium: %+v", fs)
	}
	// 期望==当前：跳过。
	fs = (HpaReplicasMismatchRule{}).Evaluate(mk(func() model.HpaInfo {
		h := base
		h.DesiredReplicas = 2
		h.CurrentReplicas = 2
		return h
	}()))
	if len(fs) != 0 {
		t.Fatalf("副本一致应跳过: %d", len(fs))
	}
	// 期望为 0（未采集到指标）：跳过避免误报。
	fs = (HpaReplicasMismatchRule{}).Evaluate(mk(func() model.HpaInfo {
		h := base
		h.CurrentReplicas = 1
		return h
	}()))
	if len(fs) != 0 {
		t.Fatalf("期望为 0 应跳过: %d", len(fs))
	}
}
