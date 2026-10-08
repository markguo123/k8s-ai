package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCreateToolApplyWithProposalID(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	tool := newTestCreateTool(runner, store)
	dryInput := createInput(t, cmManifest, "", nil)
	tr, err := tool.Execute(context.Background(), dryInput)
	if err != nil {
		t.Fatal(err)
	}
	data := dataOf(t, tr)
	pid := stringOf(data["proposalId"])
	if pid == "" {
		t.Fatalf("dry_run 结果缺少 proposalId：%v", data)
	}
	if manifest, ok := data["proposalManifest"].(string); !ok || manifest == "" {
		t.Fatalf("dry_run 结果缺少 proposalManifest：%v", data)
	}

	applyInput := createInput(t, "", CreateModeApply, map[string]any{"proposal_id": pid})
	if tool.IsReadOnly(applyInput) {
		t.Fatal("带 proposal_id 的 apply 必须判定为写操作")
	}
	apply, err := tool.Execute(context.Background(), applyInput)
	if err != nil {
		t.Fatal(err)
	}
	applyData := dataOf(t, apply)
	if applyData["status"] != statusApplied || applyData["applied"] != 1 {
		t.Fatalf("proposal_id apply 未创建对象：%v", applyData)
	}
	last := runner.requests[len(runner.requests)-1]
	if last.DryRun {
		t.Fatal("apply 不应再走 dry run")
	}
	meta, _ := last.Object["metadata"].(map[string]any)
	if meta == nil || stringOf(meta["name"]) != "web" {
		t.Fatalf("apply 必须复用 dry_run 保存的原始对象：%+v", last.Object)
	}
}

func TestCreateToolApplyProposalIDRejectsMismatchedManifest(t *testing.T) {
	runner, store := newFakeCreateRunner(), NewProposalStore()
	tool := newTestCreateTool(runner, store)
	tr, err := tool.Execute(context.Background(), createInput(t, cmManifest, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	pid := stringOf(dataOf(t, tr)["proposalId"])
	if pid == "" {
		t.Fatal("dry_run 未返回 proposalId")
	}
	tampered := strings.Replace(cmManifest, "mode: prod", "mode: dev", 1)
	apply, err := tool.Execute(context.Background(), createInput(t, tampered, CreateModeApply, map[string]any{"proposal_id": pid}))
	if err != nil {
		t.Fatal(err)
	}
	if got := errorCode(t, apply); got != errCreateProposalMismatch {
		t.Fatalf("code = %q", got)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("不匹配的清单不得提交：%+v", runner.requests)
	}
}
