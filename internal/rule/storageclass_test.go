// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestStorageClassNoProvisionerRule 未配置 provisioner 的 StorageClass 命中，配置正常/显式 no-provisioner 跳过。
func TestStorageClassNoProvisionerRule(t *testing.T) {
	mk := func(scls []model.StorageInfo) *RuleContext {
		return ctxFor(&model.ClusterSnapshot{Storage: scls})
	}
	// 空 provisioner：命中。
	fs := (StorageClassNoProvisionerRule{}).Evaluate(mk([]model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "StorageClass", Name: "sc-null"}, Kind: "StorageClass", Driver: ""},
	}))
	if len(fs) != 1 {
		t.Fatalf("空 provisioner 应命中: %d", len(fs))
	}
	// 已配置 provisioner：跳过。
	if fs := (StorageClassNoProvisionerRule{}).Evaluate(mk([]model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "StorageClass", Name: "sc-ebs"}, Kind: "StorageClass", Driver: "ebs.csi.aws.com"},
	})); len(fs) != 0 {
		t.Fatalf("已配置 provisioner 应跳过: %d", len(fs))
	}
	// 显式 no-provisioner（本地存储类）：跳过。
	if fs := (StorageClassNoProvisionerRule{}).Evaluate(mk([]model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "StorageClass", Name: "local"}, Kind: "StorageClass", Driver: "kubernetes.io/no-provisioner"},
	})); len(fs) != 0 {
		t.Fatalf("显式 no-provisioner 应跳过: %d", len(fs))
	}
}
