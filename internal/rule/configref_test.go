// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestConfigRefNotFoundRule Pod 引用的 ConfigMap 不存在时命中（suggestion, Low）。
func TestConfigRefNotFoundRule(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		ConfigMaps: []model.ResourceRef{{Kind: "ConfigMap", Namespace: "default", Name: "app-cfg"}},
		Pods: []model.PodInfo{{
			Ref: model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "web-1"},
			ConfigMaps: []model.ResourceRef{
				{Kind: "ConfigMap", Namespace: "default", Name: "app-cfg"},     // 存在：跳过
				{Kind: "ConfigMap", Namespace: "default", Name: "missing-cfg"}, // 不存在：命中
				{Kind: "ConfigMap", Name: "no-ns"},                             // 无命名空间：跳过
			},
		}},
	}
	fs := (ConfigRefNotFoundRule{}).Evaluate(ctxFor(snap))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	for _, f := range fs {
		if f.Severity != model.SeverityLow {
			t.Fatalf("应为 LOW: %+v", f)
		}
	}
}
// TestConfigRefNotFoundRuleSecret Pod 引用的 Secret 名称不存在时命中（仅引用名存在性，不读内容值）。
func TestConfigRefNotFoundRuleSecret(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Secrets: []model.SecretInfo{
			{Ref: model.ResourceRef{Kind: "Secret", Namespace: "default", Name: "app-secret"}},
		},
		Pods: []model.PodInfo{{
			Ref: model.ResourceRef{Kind: "Pod", Namespace: "default", Name: "web-2"},
			SecretRefs: []model.ResourceRef{
				{Kind: "Secret", Namespace: "default", Name: "app-secret"},     // 存在：跳过
				{Kind: "Secret", Namespace: "default", Name: "missing-secret"}, // 不存在：命中
				{Kind: "Secret", Name: "no-ns"},                                // 无命名空间：跳过
			},
		}},
	}
	fs := (ConfigRefNotFoundRule{}).Evaluate(ctxFor(snap))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Severity != model.SeverityLow {
		t.Fatalf("应为 LOW: %+v", fs[0])
	}
}
