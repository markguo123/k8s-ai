// 三期 R3.10：create_resource 的装配语义（agent.create.enabled / agent.disabled_tools）。
//
// 这里跑完整的一轮 Chat，用「工具是否存在」作为可观察结论：
// 未注册时 Agent 会回 unknown tool，已注册时 dry run 会给出 validated 提案。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// createWriteRunner 让测试写执行器同时满足 tools.CreateRunner（创建通道的执行面）。
type createWriteRunner struct{ *fakeWriteRunner }

func (createWriteRunner) MappingFor(apiVersion, kind string) (executor.ApplyMapping, error) {
	if apiVersion == "v1" && kind == "ConfigMap" {
		return executor.ApplyMapping{Resource: "configmaps", Namespaced: true}, nil
	}
	return executor.ApplyMapping{}, errors.New("no matches for kind " + kind)
}

func (createWriteRunner) GetObjectStatus(context.Context, executor.ObjectRef) (executor.ObjectStatus, error) {
	return executor.ObjectStatus{}, nil
}

func (createWriteRunner) Apply(_ context.Context, req executor.ApplyRequest) (executor.ApplyResult, error) {
	kind, _ := req.Object["kind"].(string)
	name, _ := req.Object["metadata"].(map[string]any)["name"].(string)
	return executor.ApplyResult{Kind: kind, Name: name, Namespace: req.Namespace, DryRun: req.DryRun, UID: "uid-" + name}, nil
}

// runCreateDryRun 跑一轮「模型调用 create_resource dry_run」，返回该工具的结果文本。
func runCreateDryRun(t *testing.T, enabled bool, disabledTools []string) string {
	t.Helper()
	arguments, err := json.Marshal(map[string]string{
		"manifest": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: web\n  namespace: default\ndata:\n  mode: prod\n",
		"mode":     "dry_run",
	})
	if err != nil {
		t.Fatal(err)
	}
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "create_resource", Arguments: arguments}}},
		{Content: "done"},
	}}
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	cfg.Agent.Create.Enabled = enabled
	cfg.Agent.DisabledTools = disabledTools
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	svc := NewChat(cfg, func() (scanner.Reader, error) { return reader, nil },
		func(llm.Options) llm.LLMClient { return fl },
		func() tools.ScanRunner { return &fakeScanRunner{} },
		func() (tools.WriteRunner, error) { return createWriteRunner{&fakeWriteRunner{}}, nil })
	if _, err := svc.GetOrCreateSession("s1", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "创建一个 ConfigMap"}, make(chan string, 1))
	if err != nil {
		t.Fatal(err)
	}
	result := ""
	for ev := range events {
		if ev.Type == ChatEventToolResponse && ev.Tool != nil && ev.Tool.Name == "create_resource" {
			result = ev.Tool.Result
		}
	}
	return result
}

func TestChatServiceCreateToolAssembly(t *testing.T) {
	t.Run("开关打开时挂载并可 dry run", func(t *testing.T) {
		result := runCreateDryRun(t, true, nil)
		if strings.Contains(result, "unknown tool") {
			t.Fatalf("agent.create.enabled=true 时应挂载 create_resource：%q", result)
		}
		if !strings.Contains(result, "dry_run") || !strings.Contains(result, "validated") {
			t.Fatalf("dry run 应返回已校验提案：%q", result)
		}
	})
	t.Run("disabled_tools 能摘掉它", func(t *testing.T) {
		if result := runCreateDryRun(t, true, []string{"create_resource"}); !strings.Contains(result, "unknown tool") {
			t.Fatalf("agent.disabled_tools 应摘掉 create_resource：%q", result)
		}
	})
	t.Run("开关关闭时不挂载", func(t *testing.T) {
		if result := runCreateDryRun(t, false, nil); !strings.Contains(result, "unknown tool") {
			t.Fatalf("agent.create.enabled=false 时不得挂载 create_resource：%q", result)
		}
	})
}
