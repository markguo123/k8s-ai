// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestNetworkPolicyNoDefaultDenyRule 命名空间无默认拒绝策略时命中，有则跳过。
func TestNetworkPolicyNoDefaultDenyRule(t *testing.T) {
	mk := func(netpols []model.NetworkPolicyInfo) *RuleContext {
		snap := &model.ClusterSnapshot{
			Namespaces: []model.NamespaceInfo{
				{Ref: model.ResourceRef{Kind: "Namespace", Name: "app"}},
				{Ref: model.ResourceRef{Kind: "Namespace", Name: "kube-system"}},
			},
			Pods: []model.PodInfo{
				{Ref: model.ResourceRef{Kind: "Pod", Namespace: "app", Name: "web-1"}},
				{Ref: model.ResourceRef{Kind: "Pod", Namespace: "kube-system", Name: "coredns-1"}},
			},
			NetworkPolicies: netpols,
		}
		return ctxFor(snap)
	}
	// 集群完全未采用 NetPol（无任何策略）：跳过，避免误报。
	if fs := (NetworkPolicyNoDefaultDenyRule{}).Evaluate(mk(nil)); len(fs) != 0 {
		t.Fatalf("无任何 NetPol 时应跳过: %d", len(fs))
	}
	// 存在 NetPol 但 app 命名空间无默认拒绝：app 命中，kube-system 系统命名空间跳过。
	fs := (NetworkPolicyNoDefaultDenyRule{}).Evaluate(mk([]model.NetworkPolicyInfo{
		{Ref: model.ResourceRef{Kind: "NetworkPolicy", Namespace: "app", Name: "web-only"}, PodSelector: map[string]string{"app": "web"}},
	}))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Resource.Name != "app" {
		t.Fatalf("命中命名空间 = %s, want app", fs[0].Resource.Name)
	}
	// 存在覆盖全 Pod 的默认拒绝策略：app 跳过。
	if fs := (NetworkPolicyNoDefaultDenyRule{}).Evaluate(mk([]model.NetworkPolicyInfo{
		{Ref: model.ResourceRef{Kind: "NetworkPolicy", Namespace: "app", Name: "deny-all"}, PodSelector: map[string]string{}},
	})); len(fs) != 0 {
		t.Fatalf("存在默认拒绝应不报: %d", len(fs))
	}
}
