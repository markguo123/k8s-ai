// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestPdbBlockingRule PDB disruptionsAllowed=0 且有 selector 时命中；允许驱逐或无选择器跳过。
func TestPdbBlockingRule(t *testing.T) {
	mk := func(p model.PdbInfo) *RuleContext {
		return ctxFor(&model.ClusterSnapshot{Pdbs: []model.PdbInfo{p}})
	}
	base := model.PdbInfo{Ref: model.ResourceRef{Kind: "PDB", Namespace: "default", Name: "web-pdb"}, Selector: map[string]string{"app": "web"}}
	// disruptionsAllowed=0：命中 High。
	fs := (PdbBlockingRule{}).Evaluate(mk(base))
	if len(fs) != 1 || fs[0].Severity != model.SeverityHigh {
		t.Fatalf("禁止驱逐应命中 High: %+v", fs)
	}
	// disruptionsAllowed>0：跳过。
	fs = (PdbBlockingRule{}).Evaluate(mk(func() model.PdbInfo {
		p := base
		p.DisruptionsAllowed = 2
		return p
	}()))
	if len(fs) != 0 {
		t.Fatalf("允许驱逐应跳过: %d", len(fs))
	}
	// 无 selector：跳过（不影响任何 Pod，误报价值低）。
	fs = (PdbBlockingRule{}).Evaluate(mk(func() model.PdbInfo {
		p := base
		p.Selector = nil
		return p
	}()))
	if len(fs) != 0 {
		t.Fatalf("无 selector 应跳过: %d", len(fs))
	}
}
