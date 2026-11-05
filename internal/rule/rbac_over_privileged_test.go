// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func wildcardPolicyRule() model.PolicyRuleInfo {
	return model.PolicyRuleInfo{
		APIGroups: []string{"*"},
		Resources: []string{"*"},
		Verbs:     []string{"*"},
	}
}

func testRoleRef(kind, name string) model.RoleRefInfo {
	return model.RoleRefInfo{APIGroup: "rbac.authorization.k8s.io", Kind: kind, Name: name}
}

func testSubjects(names ...string) []model.SubjectInfo {
	out := make([]model.SubjectInfo, 0, len(names))
	for _, name := range names {
		out = append(out, model.SubjectInfo{
			Kind:      "ServiceAccount",
			APIGroup:  "",
			Name:      name,
			Namespace: "default",
		})
	}
	return out
}

func evidenceValue(f *model.Finding, key string) string {
	for _, ev := range f.Evidence {
		if ev.Key == key {
			return ev.Value
		}
	}
	return ""
}

func hasEvidenceKeys(f *model.Finding, keys ...string) bool {
	got := make(map[string]bool, len(f.Evidence))
	for _, ev := range f.Evidence {
		got[ev.Key] = true
	}
	for _, key := range keys {
		if !got[key] {
			return false
		}
	}
	return true
}

func TestRbacOverPrivilegedClusterRoleBinding(t *testing.T) {
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		ClusterRoleBindings: []model.ClusterRoleBindingInfo{{
			Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "admins"},
			RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
			Subjects: testSubjects("admin-a", "admin-b"),
		}},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 1 {
		t.Fatalf("多个 subject 的有效 binding 应只产生一条 finding，实际 %d", len(findings))
	}
	f := findings[0]
	if f.Severity != model.SeverityHigh {
		t.Fatalf("ClusterRoleBinding 全通配应为 High，实际 %s", f.Severity)
	}
	if f.Resource.Kind != "ClusterRoleBinding" || f.Kind != "ClusterRoleBinding" || f.Resource.Name != "admins" {
		t.Fatalf("finding 必须落在 binding 上，实际 resource=%+v kind=%s", f.Resource, f.Kind)
	}
	if !hasEvidenceKeys(f, "rule", "roleRef", "binding", "subjects", "scope") {
		t.Fatalf("证据缺少必需 key：%+v", f.Evidence)
	}
	if !strings.Contains(evidenceValue(f, "roleRef"), "ClusterRole/cluster-admin") ||
		!strings.Contains(evidenceValue(f, "binding"), "ClusterRoleBinding/admins") {
		t.Fatalf("roleRef/binding 证据不清楚：%+v", f.Evidence)
	}
}

func TestRbacOverPrivilegedRoleBinding(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Roles: []model.RoleInfo{{
			Ref:   model.ResourceRef{Kind: "Role", Namespace: "default", Name: "namespace-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		RoleBindings: []model.RoleBindingInfo{
			{
				Ref:      model.ResourceRef{Kind: "RoleBinding", Namespace: "default", Name: "role-grant"},
				RoleRef:  testRoleRef("Role", "namespace-admin"),
				Subjects: testSubjects("role-user"),
			},
			{
				Ref:      model.ResourceRef{Kind: "RoleBinding", Namespace: "default", Name: "cluster-role-grant"},
				RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
				Subjects: testSubjects("cluster-role-user"),
			},
		},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 2 {
		t.Fatalf("两个有效的 RoleBinding 应各产出一条 finding，实际 %d", len(findings))
	}
	for _, f := range findings {
		if f.Severity != model.SeverityMedium {
			t.Fatalf("RoleBinding 全通配应为 Medium，实际 %s", f.Severity)
		}
		if f.Resource.Kind != "RoleBinding" || f.Kind != "RoleBinding" {
			t.Fatalf("finding 必须落在 RoleBinding 上，实际 resource=%+v kind=%s", f.Resource, f.Kind)
		}
	}
}

func TestRbacOverPrivilegedUnboundRolesAreLow(t *testing.T) {
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "unbound-cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		Roles: []model.RoleInfo{
			{
				Ref:   model.ResourceRef{Kind: "Role", Namespace: "default", Name: "empty-binding"},
				Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
			},
			{
				Ref:   model.ResourceRef{Kind: "Role", Namespace: "default", Name: "blank-subject-binding"},
				Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
			},
		},
		RoleBindings: []model.RoleBindingInfo{
			{
				Ref:      model.ResourceRef{Kind: "RoleBinding", Namespace: "default", Name: "no-subjects"},
				RoleRef:  testRoleRef("Role", "empty-binding"),
				Subjects: nil,
			},
			{
				Ref:     model.ResourceRef{Kind: "RoleBinding", Namespace: "default", Name: "blank-subject"},
				RoleRef: testRoleRef("Role", "blank-subject-binding"),
				Subjects: []model.SubjectInfo{{
					Kind: "ServiceAccount", Name: "  ", Namespace: "default",
				}},
			},
		},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 3 {
		t.Fatalf("一个未绑定 ClusterRole 和两个无效主体的 Role 应各报 Low，实际 %d", len(findings))
	}
	for _, f := range findings {
		if f.Severity != model.SeverityLow {
			t.Fatalf("无有效绑定的通配角色应为 Low，实际 %s", f.Severity)
		}
		if f.Resource.Kind != "ClusterRole" && f.Resource.Kind != "Role" {
			t.Fatalf("Low finding 必须落在角色定义上，实际 %+v", f.Resource)
		}
	}
}

func TestRbacOverPrivilegedFlagsPartialWildcards(t *testing.T) {
	rules := []model.PolicyRuleInfo{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"*"}},
		{APIGroups: []string{"*"}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{""}, Resources: []string{"*"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{}, Verbs: []string{"get", "list"}, NonResourceURLs: []string{"*"}},
	}
	snap := &model.ClusterSnapshot{
		ClusterRoleBindings: []model.ClusterRoleBindingInfo{
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "verbs-only"},
				RoleRef:  testRoleRef("ClusterRole", "verbs-only"),
				Subjects: testSubjects("user-a"),
			},
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "api-group-only"},
				RoleRef:  testRoleRef("ClusterRole", "api-group-only"),
				Subjects: testSubjects("user-b"),
			},
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "resource-only"},
				RoleRef:  testRoleRef("ClusterRole", "resource-only"),
				Subjects: testSubjects("user-c"),
			},
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "precise"},
				RoleRef:  testRoleRef("ClusterRole", "precise"),
				Subjects: testSubjects("user-d"),
			},
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "non-resource"},
				RoleRef:  testRoleRef("ClusterRole", "non-resource"),
				Subjects: testSubjects("user-e"),
			},
		},
		ClusterRoles: []model.ClusterRoleInfo{
			{Ref: model.ResourceRef{Kind: "ClusterRole", Name: "verbs-only"}, Rules: []model.PolicyRuleInfo{rules[0]}},
			{Ref: model.ResourceRef{Kind: "ClusterRole", Name: "api-group-only"}, Rules: []model.PolicyRuleInfo{rules[1]}},
			{Ref: model.ResourceRef{Kind: "ClusterRole", Name: "resource-only"}, Rules: []model.PolicyRuleInfo{rules[2]}},
			{Ref: model.ResourceRef{Kind: "ClusterRole", Name: "precise"}, Rules: []model.PolicyRuleInfo{rules[3]}},
			{Ref: model.ResourceRef{Kind: "ClusterRole", Name: "non-resource"}, Rules: []model.PolicyRuleInfo{rules[4]}},
		},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 4 {
		t.Fatalf("4 条局部通配规则必须命中，仅精确授权跳过，实际 %+v", findings)
	}
	for _, f := range findings {
		if f.Severity != model.SeverityMedium {
			t.Fatalf("局部通配 ClusterRoleBinding 应为 Medium，实际 %s / %s", f.Severity, f.Title)
		}
		if strings.Contains(f.Title, "全通配") {
			t.Fatalf("局部通配不得被描述成全通配：%s", f.Title)
		}
	}
}

func TestRbacOverPrivilegedSkipsUnboundWhenBindingCollectionFailed(t *testing.T) {
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		Roles: []model.RoleInfo{{
			Ref:   model.ResourceRef{Kind: "Role", Namespace: "default", Name: "namespace-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		CollectionErrors: []model.CollectionError{
			{Operation: "list_cluster_role_bindings", Resource: model.ResourceRef{Kind: "ClusterRoleBinding"}},
			{Operation: "list_role_bindings", Resource: model.ResourceRef{Kind: "RoleBinding", Namespace: "default"}},
		},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 0 {
		t.Fatalf("绑定数据缺失时不得把角色误判为未绑定：%+v", findings)
	}
}

func TestRbacOverPrivilegedRespectsNamespaceScope(t *testing.T) {
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		ClusterRoleBindings: []model.ClusterRoleBindingInfo{{
			Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "cluster-admins"},
			RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
			Subjects: testSubjects("cluster-user"),
		}},
		RoleBindings: []model.RoleBindingInfo{{
			Ref:      model.ResourceRef{Kind: "RoleBinding", Namespace: "team-a", Name: "team-admin"},
			RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
			Subjects: testSubjects("team-user"),
		}},
	}
	ctx := ctxFor(snap)
	ctx.Options = model.ScanOptions{Namespace: "team-a"}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctx)
	if len(findings) != 1 {
		t.Fatalf("命名空间体检只应分析本命名空间 RoleBinding，实际 %+v", findings)
	}
	if findings[0].Resource.Kind != "RoleBinding" || findings[0].Resource.Namespace != "team-a" {
		t.Fatalf("不应把集群级绑定带入业务体检：%+v", findings[0].Resource)
	}
}

func TestRbacOverPrivilegedSkipsPodScope(t *testing.T) {
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		ClusterRoleBindings: []model.ClusterRoleBindingInfo{{
			Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "cluster-admins"},
			RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
			Subjects: testSubjects("cluster-user"),
		}},
	}
	ctx := ctxFor(snap)
	ctx.Options = model.ScanOptions{Namespace: "team-a", PodTarget: "web-0"}

	if findings := (RbacOverPrivilegedRule{}).Evaluate(ctx); len(findings) != 0 {
		t.Fatalf("单 Pod 体检不应混入集群 RBAC 结论：%+v", findings)
	}
}

func TestRbacOverPrivilegedResourceNamesPreventFullClusterClaim(t *testing.T) {
	rule := wildcardPolicyRule()
	rule.ResourceNames = []string{"payments"}
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "named-cluster-admin"},
			Rules: []model.PolicyRuleInfo{rule},
		}},
		ClusterRoleBindings: []model.ClusterRoleBindingInfo{{
			Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "named-admins"},
			RoleRef:  testRoleRef("ClusterRole", "named-cluster-admin"),
			Subjects: testSubjects("named-user"),
		}},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 1 {
		t.Fatalf("对象名受限的通配授权仍应告警，实际 %+v", findings)
	}
	if findings[0].Severity != model.SeverityMedium || strings.Contains(findings[0].Title, "全通配") {
		t.Fatalf("resourceNames 受限时不得描述成全集群全对象权限：%+v", findings[0])
	}
	if !strings.Contains(evidenceValue(findings[0], "rule"), "resourceNames=payments") {
		t.Fatalf("证据必须带上 resourceNames：%+v", findings[0].Evidence)
	}
}

func TestRbacOverPrivilegedBindingFingerprintIsStableAndDistinct(t *testing.T) {
	snap := &model.ClusterSnapshot{
		ClusterRoles: []model.ClusterRoleInfo{{
			Ref:   model.ResourceRef{Kind: "ClusterRole", Name: "cluster-admin"},
			Rules: []model.PolicyRuleInfo{wildcardPolicyRule()},
		}},
		ClusterRoleBindings: []model.ClusterRoleBindingInfo{
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "binding-a"},
				RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
				Subjects: testSubjects("same-subject"),
			},
			{
				Ref:      model.ResourceRef{Kind: "ClusterRoleBinding", Name: "binding-b"},
				RoleRef:  testRoleRef("ClusterRole", "cluster-admin"),
				Subjects: testSubjects("same-subject"),
			},
		},
	}

	findings := (RbacOverPrivilegedRule{}).Evaluate(ctxFor(snap))
	if len(findings) != 2 {
		t.Fatalf("两个 binding 应产生两条 finding，实际 %d", len(findings))
	}
	if findings[0].ID == findings[1].ID {
		t.Fatalf("不同 binding 的 fingerprint 不应相同：%s", findings[0].ID)
	}
	if evidenceValue(findings[0], "binding") == evidenceValue(findings[1], "binding") {
		t.Fatalf("不同 binding 的 binding 证据不应相同：%+v / %+v", findings[0].Evidence, findings[1].Evidence)
	}
}
