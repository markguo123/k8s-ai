package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// stubTool 是测试用最小 Tool 实现。
type stubTool struct {
	name string
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) InputSchema() any    { return map[string]any{"type": "object"} }
func (s *stubTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	return ToolResult{Data: map[string]any{"ok": true}}, nil
}
func (s *stubTool) IsReadOnly(input json.RawMessage) bool { return true }
func (s *stubTool) Risk(input json.RawMessage) model.RiskLevel {
	return model.RiskSafe
}

func TestRegistryRegisterLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "get_pod"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubTool{name: "get_pod"}); err == nil {
		t.Fatal("重名注册应报错")
	}
	if err := r.Register(&stubTool{name: ""}); err == nil {
		t.Fatal("空名注册应报错")
	}
	if _, ok := r.Lookup("get_pod"); !ok {
		t.Fatal("lookup get_pod 失败")
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("lookup 不存在的工具应失败")
	}
}

func TestRegistrySchemasSorted(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&stubTool{name: "z_tool"})
	_ = r.Register(&stubTool{name: "a_tool"})
	specs := r.Schemas()
	if len(specs) != 2 {
		t.Fatalf("schemas = %d", len(specs))
	}
	if specs[0].Name != "a_tool" || specs[1].Name != "z_tool" {
		t.Fatalf("顺序未排序: %+v", specs)
	}
	if specs[0].Parameters == nil {
		t.Fatal("schema 缺少 parameters")
	}
}

func TestRegistryRemove(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "get_pod"}); err != nil {
		t.Fatal(err)
	}
	r.Remove("get_pod")
	if _, ok := r.Lookup("get_pod"); ok {
		t.Fatal("Remove 后仍能 lookup 到工具")
	}
	r.Remove("missing") // 不存在应静默忽略
}

func TestMarshalForLLM(t *testing.T) {
	out, truncated := MarshalForLLM(ToolResult{
		Data: map[string]any{"phase": "Running", "secret": "sk-test-secret-value"},
	}, 0)
	if strings.Contains(out, "sk-test-secret-value") {
		t.Fatalf("Secret 未脱敏: %s", out)
	}
	if !strings.HasPrefix(out, untrustedPrefix) || !strings.HasSuffix(out, untrustedSuffix) {
		t.Fatalf("缺少不可信定界: %s", out)
	}
	if truncated {
		t.Fatal("短内容不应截断")
	}
}

func TestMarshalForLLMIncludesTextAndData(t *testing.T) {
	out, _ := MarshalForLLM(ToolResult{
		Text: "pods=2",
		Data: map[string]any{"count": 2, "items": []string{"a", "b"}},
	}, 0)
	if !strings.Contains(out, "pods=2") {
		t.Fatalf("缺少 Text 摘要: %s", out)
	}
	if !strings.Contains(out, `"items"`) || !strings.Contains(out, `"a"`) {
		t.Fatalf("缺少 Data 结构化内容: %s", out)
	}
}

func TestMarshalForLLMTruncates(t *testing.T) {
	long := strings.Repeat("x", 10*1024)
	out, truncated := MarshalForLLM(ToolResult{Data: map[string]any{"log": long}}, 512)
	if !truncated {
		t.Fatal("超长内容应截断")
	}
	if len(out) > 1024 {
		t.Fatalf("截断后仍过长: %d", len(out))
	}
	if !strings.Contains(out, "[TRUNCATED]") {
		t.Fatalf("缺少截断标记: %s", out[:120])
	}
}

func TestResultError(t *testing.T) {
	tr := ResultError("boom")
	if tr.Data.(map[string]any)["error"] != "boom" {
		t.Fatalf("ResultError = %+v", tr)
	}
}
