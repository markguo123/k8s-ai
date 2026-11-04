// Package rule 实现规则引擎（FR-013）。
// rbac_over_privileged.go：基于实际 RoleBinding / ClusterRoleBinding 的 RBAC 过度授权检测。
package rule

import (
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// RbacOverPrivilegedRule 检测实际 Binding 授予主体的通配权限。
// 它只声明“Binding 声明了这项权限”，不把无法验证的外部用户/组当成已存在主体。
type RbacOverPrivilegedRule struct{}

func (RbacOverPrivilegedRule) Name() string    { return "RbacOverPrivileged" }
func (RbacOverPrivilegedRule) NeedsLogs() bool { return false }

type wildcardRisk int

const (
	wildcardNone wildcardRisk = iota
	wildcardBroad
	wildcardFull
)

// wildcardRiskOf 区分“全通配”和“局部通配”。
// Kubernetes 的任一维度为 * 都可能显著扩大权限，不能要求三个维度同时为 * 才告警。
func wildcardRiskOf(rule model.PolicyRuleInfo) wildcardRisk {
	nonResourceFull := hasString(rule.Verbs, "*") && hasString(rule.NonResourceURLs, "*")
	resourceFull := hasString(rule.Verbs, "*") &&
		hasString(rule.Resources, "*") &&
		hasString(rule.APIGroups, "*") &&
		len(rule.ResourceNames) == 0
	if nonResourceFull || resourceFull {
		return wildcardFull
	}
	if hasString(rule.Verbs, "*") ||
		hasString(rule.Resources, "*") ||
		hasString(rule.APIGroups, "*") ||
		hasString(rule.NonResourceURLs, "*") {
		return wildcardBroad
	}
	return wildcardNone
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func highestWildcardRule(rules []model.PolicyRuleInfo) (model.PolicyRuleInfo, wildcardRisk) {
	best := wildcardNone
	var matched model.PolicyRuleInfo
	for _, rule := range rules {
		if risk := wildcardRiskOf(rule); risk > best {
			best = risk
			matched = rule
		}
	}
	return matched, best
}

func roleRefFor(kind, name string) model.RoleRefInfo {
	return model.RoleRefInfo{Kind: kind, Name: name}
}

func (r RbacOverPrivilegedRule) Evaluate(ctx *RuleContext) []*model.Finding {
	if ctx.Options.PodTarget != "" {
		return nil
	}
	namespaceOnly := ctx.Options.Namespace != ""

	clusterRoles := make(map[string]*model.ClusterRoleInfo, len(ctx.Snapshot.ClusterRoles))
	for i := range ctx.Snapshot.ClusterRoles {
		role := &ctx.Snapshot.ClusterRoles[i]
		clusterRoles[role.Ref.Name] = role
	}
	roles := make(map[string]*model.RoleInfo, len(ctx.Snapshot.Roles))
	for i := range ctx.Snapshot.Roles {
		role := &ctx.Snapshot.Roles[i]
		roles[namespacedRoleKey(role.Ref.Namespace, role.Ref.Name)] = role
	}

	boundClusterRoles := map[string]bool{}
	boundRoles := map[string]bool{}
	var out []*model.Finding

	if !namespaceOnly {
		for i := range ctx.Snapshot.ClusterRoleBindings {
			binding := &ctx.Snapshot.ClusterRoleBindings[i]
			subjects := validBindingSubjects(binding.Subjects)
			if len(subjects) == 0 || binding.RoleRef.Kind != "ClusterRole" {
				continue
			}
			role := clusterRoles[binding.RoleRef.Name]
			if role == nil {
				continue
			}
			boundClusterRoles[role.Ref.Name] = true
			rule, risk := highestWildcardRule(role.Rules)
			if risk == wildcardNone {
				continue
			}
			out = append(out, newFinding(
				ctx,
				r,
				binding.Ref,
				clusterBindingTitle(risk),
				clusterBindingSeverity(risk),
				bindingEvidence(binding.Ref, binding.RoleRef, subjects, rule, "cluster"),
			))
		}
	}

	for i := range ctx.Snapshot.RoleBindings {
		binding := &ctx.Snapshot.RoleBindings[i]
		if namespaceOnly && binding.Ref.Namespace != ctx.Options.Namespace {
			continue
		}
		subjects := validBindingSubjects(binding.Subjects)
		if len(subjects) == 0 {
			continue
		}
		var rules []model.PolicyRuleInfo
		switch binding.RoleRef.Kind {
		case "Role":
			role := roles[namespacedRoleKey(binding.Ref.Namespace, binding.RoleRef.Name)]
			if role == nil {
				continue
			}
			boundRoles[namespacedRoleKey(role.Ref.Namespace, role.Ref.Name)] = true
			rules = role.Rules
		case "ClusterRole":
			role := clusterRoles[binding.RoleRef.Name]
			if role == nil {
				continue
			}
			boundClusterRoles[role.Ref.Name] = true
			rules = role.Rules
		default:
			continue
		}
		rule, risk := highestWildcardRule(rules)
		if risk == wildcardNone {
			continue
		}
		out = append(out, newFinding(
			ctx,
			r,
			binding.Ref,
			roleBindingTitle(risk),
			roleBindingSeverity(risk),
			bindingEvidence(binding.Ref, binding.RoleRef, subjects, rule, binding.Ref.Namespace),
		))
	}

	if namespaceOnly {
		return out
	}

	for i := range ctx.Snapshot.ClusterRoles {
		role := &ctx.Snapshot.ClusterRoles[i]
		if boundClusterRoles[role.Ref.Name] {
			continue
		}
		rule, risk := highestWildcardRule(role.Rules)
		if risk != wildcardFull || collectionOperationFailed(ctx.Snapshot, "list_cluster_role_bindings") {
			continue
		}
		out = append(out, newFinding(
			ctx,
			r,
			role.Ref,
			"ClusterRole 定义全通配权限但未绑定有效主体",
			model.SeverityLow,
			unboundRoleEvidence(role.Ref, roleRefFor("ClusterRole", role.Ref.Name), rule),
		))
	}

	for i := range ctx.Snapshot.Roles {
		role := &ctx.Snapshot.Roles[i]
		key := namespacedRoleKey(role.Ref.Namespace, role.Ref.Name)
		if boundRoles[key] {
			continue
		}
		rule, risk := highestWildcardRule(role.Rules)
		if risk != wildcardFull || collectionOperationFailed(ctx.Snapshot, "list_role_bindings") {
			continue
		}
		out = append(out, newFinding(
			ctx,
			r,
			role.Ref,
			"Role 定义全通配权限但未绑定有效主体",
			model.SeverityLow,
			unboundRoleEvidence(role.Ref, roleRefFor("Role", role.Ref.Name), rule),
		))
	}

	return out
}

func clusterBindingTitle(risk wildcardRisk) string {
	if risk == wildcardFull {
		return "ClusterRoleBinding 将全通配 ClusterRole 授予主体"
	}
	return "ClusterRoleBinding 将通配符权限授予主体"
}

func roleBindingTitle(risk wildcardRisk) string {
	if risk == wildcardFull {
		return "RoleBinding 将全通配角色授予主体"
	}
	return "RoleBinding 将通配符权限授予主体"
}

func clusterBindingSeverity(risk wildcardRisk) model.Severity {
	if risk == wildcardFull {
		return model.SeverityHigh
	}
	return model.SeverityMedium
}

func roleBindingSeverity(risk wildcardRisk) model.Severity {
	if risk == wildcardFull {
		return model.SeverityMedium
	}
	return model.SeverityLow
}

func collectionOperationFailed(snap *model.ClusterSnapshot, operation string) bool {
	if snap == nil {
		return true
	}
	for _, item := range snap.CollectionErrors {
		if item.Operation == operation {
			return true
		}
	}
	return false
}

func namespacedRoleKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func validBindingSubjects(subjects []model.SubjectInfo) []model.SubjectInfo {
	out := make([]model.SubjectInfo, 0, len(subjects))
	for _, subject := range subjects {
		if strings.TrimSpace(subject.Name) == "" {
			continue
		}
		out = append(out, subject)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func bindingEvidence(
	binding model.ResourceRef,
	roleRef model.RoleRefInfo,
	subjects []model.SubjectInfo,
	rule model.PolicyRuleInfo,
	scope string,
) []model.Evidence {
	bindingSource := refEvidenceSource(binding)
	return []model.Evidence{
		evidence.Derived(refEvidenceSource(model.ResourceRef{
			Kind: roleRef.Kind,
			Name: roleRef.Name,
		}), "rule", policyRuleValue(rule)),
		evidence.Derived(bindingSource+"/roleRef", "roleRef", roleRefValue(roleRef)),
		evidence.Derived(bindingSource, "binding", refEvidenceSource(binding)),
		evidence.Derived(bindingSource+"/subjects", "subjects", subjectsValue(subjects)),
		evidence.Derived(bindingSource+"/scope", "scope", scope),
	}
}

func unboundRoleEvidence(
	roleRef model.ResourceRef,
	ref model.RoleRefInfo,
	rule model.PolicyRuleInfo,
) []model.Evidence {
	source := refEvidenceSource(roleRef)
	return []model.Evidence{
		evidence.Derived(source, "rule", policyRuleValue(rule)),
		evidence.Derived(source+"/roleRef", "roleRef", roleRefValue(ref)),
		evidence.Derived(source+"/binding", "binding", "none"),
		evidence.Derived(source+"/subjects", "subjects", "none"),
		evidence.Derived(source+"/scope", "scope", "unbound"),
	}
}

func policyRuleValue(rule model.PolicyRuleInfo) string {
	return fmt.Sprintf(
		"verbs=%s resources=%s resourceNames=%s apiGroups=%s nonResourceURLs=%s",
		strings.Join(rule.Verbs, ","),
		strings.Join(rule.Resources, ","),
		strings.Join(rule.ResourceNames, ","),
		strings.Join(rule.APIGroups, ","),
		strings.Join(rule.NonResourceURLs, ","),
	)
}

func roleRefValue(ref model.RoleRefInfo) string {
	if ref.APIGroup == "" {
		return ref.Kind + "/" + ref.Name
	}
	return ref.APIGroup + "/" + ref.Kind + "/" + ref.Name
}

func subjectsValue(subjects []model.SubjectInfo) string {
	parts := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Namespace == "" {
			parts = append(parts, subject.Kind+"/"+subject.Name)
			continue
		}
		parts = append(parts, subject.Kind+"/"+subject.Namespace+"/"+subject.Name)
	}
	return evidence.TruncateValue(model.Evidence{Value: strings.Join(parts, ",")}, 2048).Value
}

func refEvidenceSource(ref model.ResourceRef) string {
	if ref.Namespace == "" {
		return ref.Kind + "/" + ref.Name
	}
	return ref.Kind + "/" + ref.Namespace + "/" + ref.Name
}
