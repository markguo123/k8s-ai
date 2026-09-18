// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestRbacOverPrivilegedRule ClusterRole 任一规则含通配符（*）即命中；普通权限跳过。
func TestRbacOverPrivilegedRule(t *testing.T) {
	mk := func(crs ...model.ClusterRoleInfo) *RuleContext {
		return ctxFor(&model.ClusterSnapshot{ClusterRoles: crs})
	}
	// verbs 含 *：命中 Medium。
	fs := (RbacOverPrivilegedRule{}).Evaluate(mk(model.ClusterRoleInfo{
		Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "admin"},
		Rules: []model.PolicyRuleInfo{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"*"}}},
	}))
	if len(fs) != 1 || fs[0].Severity != model.SeverityMedium {
		t.Fatalf("通配 verbs 应命中 Medium: %+v", fs)
	}
	// resources 含 * 且多条规则：命中一条即 break。
	fs = (RbacOverPrivilegedRule{}).Evaluate(mk(model.ClusterRoleInfo{
		Ref: model.ResourceRef{Kind: "ClusterRole", Name: "wide"},
		Rules: []model.PolicyRuleInfo{
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"get"}},
		},
	}))
	if len(fs) != 1 {
		t.Fatalf("单 Role 应只命中一条: %d", len(fs))
	}
	// 精确授权：跳过。
	fs = (RbacOverPrivilegedRule{}).Evaluate(mk(model.ClusterRoleInfo{
		Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "reader"},
		Rules: []model.PolicyRuleInfo{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}}},
	}))
	if len(fs) != 0 {
		t.Fatalf("精确授权应跳过: %d", len(fs))
	}
}
