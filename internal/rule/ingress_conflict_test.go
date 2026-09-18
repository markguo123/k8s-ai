// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestIngressHostConflictRule 相同 (host,path) 映射不同后端时命中冲突。
func TestIngressHostConflictRule(t *testing.T) {
	mk := func(rules []model.IngressRuleInfo) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
			Ingresses: []model.IngressInfo{
				{Ref: model.ResourceRef{Kind: "Ingress", Namespace: "default", Name: "web-ing"}, Rules: rules},
			},
		}
		return ctxFor(snap)
	}
	// 正常：不同 path 映射不同后端。
	fs := (IngressHostConflictRule{}).Evaluate(mk([]model.IngressRuleInfo{
		{Host: "a.example.com", Path: "/", Backend: "default/svc-a:80"},
		{Host: "b.example.com", Path: "/", Backend: "default/svc-b:80"},
	}))
	if len(fs) != 0 {
		t.Fatalf("正常路由应不报: %d", len(fs))
	}
	// 冲突：同一 (host,path) 映射不同后端。
	fs = (IngressHostConflictRule{}).Evaluate(mk([]model.IngressRuleInfo{
		{Host: "a.example.com", Path: "/", Backend: "default/svc-a:80"},
		{Host: "a.example.com", Path: "/", Backend: "default/svc-b:80"},
	}))
	if len(fs) != 1 {
		t.Fatalf("冲突路由应命中: %d", len(fs))
	}
}
