// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestVolumeAttachmentStuckRule 长时间未 Attached 或有 AttachError 时命中，正常/新建跳过。
func TestVolumeAttachmentStuckRule(t *testing.T) {
	mk := func(st storageFix) *RuleContext {
		return st.ctx()
	}
	// 长时间 Detached：命中。
	fs := (VolumeAttachmentStuckRule{}).Evaluate(mk(storageFix{va: []model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "VolumeAttachment", Name: "csi-abc"}, Kind: "VolumeAttachment", Status: "Detached", CreatedAt: time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
	}}))
	if len(fs) != 1 {
		t.Fatalf("长时间 Detached 应命中: %d", len(fs))
	}
	// 存在 AttachError（Reason 非空）：立即命中，忽略时长。
	fs = (VolumeAttachmentStuckRule{}).Evaluate(mk(storageFix{va: []model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "VolumeAttachment", Name: "csi-err"}, Kind: "VolumeAttachment", Status: "Detached", Reason: "attach timeout", CreatedAt: time.Now().UTC().Format(time.RFC3339)},
	}}))
	if len(fs) != 1 {
		t.Fatalf("AttachError 应命中: %d", len(fs))
	}
	// 新建且未超阈值：跳过避免误报。
	fs = (VolumeAttachmentStuckRule{}).Evaluate(mk(storageFix{va: []model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "VolumeAttachment", Name: "csi-new"}, Kind: "VolumeAttachment", Status: "Detached", CreatedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
	}}))
	if len(fs) != 0 {
		t.Fatalf("新建 Detached 应跳过: %d", len(fs))
	}
	// 已 Attached：不命中。
	fs = (VolumeAttachmentStuckRule{}).Evaluate(mk(storageFix{va: []model.StorageInfo{
		{Ref: model.ResourceRef{Kind: "VolumeAttachment", Name: "csi-ok"}, Kind: "VolumeAttachment", Status: "Attached"},
	}}))
	if len(fs) != 0 {
		t.Fatalf("已 Attached 不命中: %d", len(fs))
	}
}

type storageFix struct {
	va []model.StorageInfo
}

func (f storageFix) ctx() *RuleContext {
	return ctxFor(&model.ClusterSnapshot{Storage: f.va})
}
