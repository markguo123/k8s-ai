// Package rule 实现规则引擎（FR-013）。
// rbac_over_privileged.go：ClusterRole 过度授权（* 通配，P7.6）。
package rule

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// RbacOverPrivilegedRule ClusterRole 使用通配符（*）对 verbs/resources/apiGroups 全放行。
type RbacOverPrivilegedRule struct{}

func (RbacOverPrivilegedRule) Name() string    { return "RbacOverPrivileged" }
func (RbacOverPrivilegedRule) NeedsLogs() bool { return false }

// ruleOverPrivileged 判断单条策略规则是否过度授权。
func ruleOverPrivileged(rule model.PolicyRuleInfo) bool {
	for _, v := range rule.Verbs {
		if v == "*" {
			return true
		}
	}
	for _, r := range rule.Resources {
		if r == "*" {
			return true
		}
	}
	for _, g := range rule.APIGroups {
		if g == "*" {
			return true
		}
	}
	return false
}

func (r RbacOverPrivilegedRule) Evaluate(ctx *RuleContext) []*model.Finding {
	var out []*model.Finding
	for i := range ctx.Snapshot.ClusterRoles {
		cr := &ctx.Snapshot.ClusterRoles[i]
		for _, rule := range cr.Rules {
			if !ruleOverPrivileged(rule) {
				continue
			}
			evs := []model.Evidence{
				evidence.Derived(cr.Ref.Kind+"/"+cr.Ref.Name, "rule", fmt.Sprintf("verbs=%s resources=%s apiGroups=%s",
					strings.Join(rule.Verbs, ","), strings.Join(rule.Resources, ","), strings.Join(rule.APIGroups, ","))),
			}
			out = append(out, newFinding(ctx, r, cr.Ref, "ClusterRole 使用通配符（*）过度授权", model.SeverityMedium, evs))
			break // 一条命中即可，避免同一 Role 重复多条
		}
	}
	return out
}
