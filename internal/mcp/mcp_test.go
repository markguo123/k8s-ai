package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// fakeMCPClient 是测试用 MCP 客户端。
type fakeMCPClient struct {
	name   string
	tools  []Tool
	called map[string]map[string]any
}

func (f *fakeMCPClient) Name() string { return f.name }
func (f *fakeMCPClient) Connect(context.Context) error {
	return nil
}
func (f *fakeMCPClient) Close() error { return nil }
func (f *fakeMCPClient) ListTools(context.Context) ([]Tool, error) {
	return f.tools, nil
}
func (f *fakeMCPClient) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	f.called[name] = args
	return "ok:" + name, nil
}

func TestRegisterAllAndAdapter(t *testing.T) {
	client := &fakeMCPClient{
		name:  "prometheus",
		called: map[string]map[string]any{},
		tools: []Tool{
			{Name: "query", Description: "查询指标", InputSchema: map[string]any{"type": "object"}},
			{Name: "health", Description: "健康检查"},
		},
	}
	reg := tools.NewRegistry()
	if err := RegisterAll(reg, client); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("prometheus.query"); !ok {
		t.Fatal("应注册为 prometheus.query")
	}
	adapter, _ := reg.Lookup("prometheus.query")
	if adapter.IsReadOnly(nil) {
		t.Fatal("MCP 工具默认非只读")
	}
	if adapter.Risk(nil) != model.RiskCritical {
		t.Fatal("MCP 工具默认 CRITICAL")
	}
	tr, err := adapter.Execute(context.Background(), json.RawMessage(`{"expr":"up"}`))
	if err != nil {
		t.Fatal(err)
	}
	if client.called["query"]["expr"] != "up" {
		t.Fatalf("调用参数 = %v", client.called)
	}
	if tr.Data.(map[string]any)["content"] != "ok:query" {
		t.Fatalf("result = %v", tr.Data)
	}
}

func TestRegisterAllDuplicateName(t *testing.T) {
	client := &fakeMCPClient{
		name:   "s",
		called: map[string]map[string]any{},
		tools:  []Tool{{Name: "t"}},
	}
	reg := tools.NewRegistry()
	_ = RegisterAll(reg, client)
	if err := RegisterAll(reg, client); err == nil {
		t.Fatal("重名注册应报错")
	}
}
