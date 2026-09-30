package agent

import (
	"strings"
	"testing"
)

func TestBasePromptIncludesR6CreateProtocol(t *testing.T) {
	p := basePrompt()
	required := []string{
		"创建行为约束",
		"create_resource",
		"mode=dry_run",
		"proposal_id",
		"mode=apply",
		"required_inputs",
		"禁止编造默认值",
		"$GENERATE",
		"$ASK",
		"被拒绝 2 次",
		"kubectl create secret",
	}
	for _, want := range required {
		if !strings.Contains(p, want) {
			t.Errorf("basePrompt missing %q", want)
		}
	}
}

func TestBasePromptRoutesCreateIntentAwayFromKubectl(t *testing.T) {
	p := basePrompt()
	for _, want := range []string{
		"创建/部署/新建意图",
		"不要把资源创建改写成 kubectl create/apply",
		"资源创建统一使用 **create_resource**",
		"不走 kubectl create/apply",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("basePrompt 缺少创建意图路由约束 %q", want)
		}
	}
}

func TestBasePromptOrdersDryRunBeforeApply(t *testing.T) {
	p := basePrompt()
	i := strings.Index(p, "先调 **create_resource(mode=dry_run")
	j := strings.Index(p, "再调 **create_resource(mode=apply")
	if i == -1 || j == -1 || i >= j {
		t.Fatalf("basePrompt 未按 dry_run → 提案 → apply 顺序描述创建流程 (i=%d j=%d)", i, j)
	}
}

func TestShimPromptIncludesCreateProtocol(t *testing.T) {
	p := shimPrompt("[]", "create_resource")
	for _, want := range []string{
		"创建行为约束",
		"create_resource",
		"mode=dry_run",
		"mode=apply",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("shimPrompt missing %q", want)
		}
	}
}
