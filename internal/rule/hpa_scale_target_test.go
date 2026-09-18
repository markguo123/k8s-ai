// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestHpaScaleTargetMissingRule HPA scaleTargetRef 指向不存在的 workload 时命中。
func TestHpaScaleTargetMissingRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Workloads: []model.WorkloadInfo{
			{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "default", Name: "web"}},
		},
		Hpas: []model.HpaInfo{
			{Ref: model.ResourceRef{Kind: "HPA", Namespace: "default", Name: "web-hpa"}, ScaleTarget: "Deployment/web"},
			// 引用不存在：命中 High。
			{Ref: model.ResourceRef{Kind: "HPA", Namespace: "default", Name: "ghost-hpa"}, ScaleTarget: "Deployment/ghost"},
			// ScaleTarget 为空：跳过。
			{Ref: model.ResourceRef{Kind: "HPA", Namespace: "default", Name: "empty-hpa"}},
		},
	}
	fs := (HpaScaleTargetMissingRule{}).Evaluate(ctxFor(snap))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Resource.Name != "ghost-hpa" || fs[0].Severity != model.SeverityHigh {
		t.Fatalf("应命中 ghost-hpa High: %+v", fs)
	}
}
