// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestDeploymentOverReplicasRule Deployment 实际 Pod 数超过期望副本数时命中。
func TestDeploymentOverReplicasRule(t *testing.T) {
	desired := int32(2)
	mk := func(podCount int) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
			Workloads: []model.WorkloadInfo{
				{Ref: model.ResourceRef{Kind: "Deployment", Namespace: "default", Name: "web"}, DesiredReplicas: &desired},
			},
		}
		for i := 0; i < podCount; i++ {
			snap.Pods = append(snap.Pods, model.PodInfo{
				Ref:       model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "web-" + string(rune('a'+i))},
				OwnerRefs: []model.ResourceRef{{Kind: "Deployment", Namespace: "default", Name: "web"}},
			})
		}
		return ctxFor(snap)
	}
	// 3 个 Pod > 期望 2：命中。
	fs := (DeploymentOverReplicasRule{}).Evaluate(mk(3))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Severity != model.SeverityLow {
		t.Fatalf("severity = %s, want LOW", fs[0].Severity)
	}
	// 2 个 Pod == 期望 2：不报。
	if fs := (DeploymentOverReplicasRule{}).Evaluate(mk(2)); len(fs) != 0 {
		t.Fatalf("副本匹配应不报: %d", len(fs))
	}
}
