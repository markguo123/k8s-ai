package service

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/agent"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// scriptedLLM 是 service 测试用的脚本化 LLM。
type scriptedLLM struct {
	responses []*llm.ChatResponse
	idx       int
}

func (s *scriptedLLM) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if s.idx >= len(s.responses) {
		return &llm.ChatResponse{Content: "done"}, nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

// fakeScanRunner 是诊断 Tool 的一期 ScanService 桩。
type fakeScanRunner struct{}

func (f *fakeScanRunner) Run(_ context.Context, _ model.ScanOptions) (*model.ScanResult, error) {
	return &model.ScanResult{
		Meta:    model.ScanMeta{Cluster: "prod"},
		Summary: model.ClusterSummary{Pods: 1, Nodes: 1},
		Findings: []model.Finding{
			{ID: "f1", Rule: "crash-loop", Severity: model.SeverityHigh, Title: "CrashLoopBackOff",
				Resource: model.ResourceRef{Kind: "Pod", Namespace: "production", Name: "web-0"}},
		},
	}, nil
}

// fakeWriteRunner 是写 Tool 的执行器桩。
type fakeWriteRunner struct {
	lastReplicas int32
	lastCommand  string
}

func (f *fakeWriteRunner) RunKubectl(_ context.Context, command string) (executor.Result, error) {
	f.lastCommand = command
	return executor.Result{Command: command, Stdout: "kubectl ok"}, nil
}
func (f *fakeWriteRunner) ScaleDeployment(_ context.Context, _ string, _ string, replicas int32) (executor.Result, error) {
	f.lastReplicas = replicas
	return executor.Result{Stdout: "scaled", Verification: map[string]any{"readyReplicas": int32(5)}}, nil
}
func (f *fakeWriteRunner) PatchDeployment(_ context.Context, _, _, _ string) (executor.Result, error) {
	return executor.Result{Stdout: "patched"}, nil
}
func (f *fakeWriteRunner) PatchConfigMap(_ context.Context, _, _, _ string) (executor.Result, error) {
	return executor.Result{Stdout: "configmap patched"}, nil
}
func (f *fakeWriteRunner) RestartRollout(_ context.Context, _, _ string) (executor.Result, error) {
	return executor.Result{Stdout: "restarted"}, nil
}
func (f *fakeWriteRunner) LabelResource(_ context.Context, _, _, _, _, _ string) (executor.Result, error) {
	return executor.Result{Stdout: "labeled"}, nil
}
func (f *fakeWriteRunner) DeletePod(_ context.Context, _, _ string, _ *int64) (executor.Result, error) {
	return executor.Result{Stdout: "deleted"}, nil
}

func testChatService(t *testing.T, fl *scriptedLLM) (ChatService, *config.Config, *fakeWriteRunner) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	wr := &fakeWriteRunner{}
	svc := NewChat(cfg, func() (scanner.Reader, error) { return reader, nil }, func(llm.Options) llm.LLMClient {
		return fl
	}, func() tools.ScanRunner { return &fakeScanRunner{} }, func() (tools.WriteRunner, error) {
		return wr, nil
	})
	return svc, cfg, wr
}

func TestChatServiceRoundTrip(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_resources", Arguments: json.RawMessage(`{"kind":"nodes"}`)}}},
		{Content: "集群有 1 个节点", Usage: llm.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
	}}
	svc, _, _ := testChatService(t, fl)
	sess, err := svc.GetOrCreateSession("s1", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID != "s1" {
		t.Fatalf("session id = %s", sess.ID)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "检查集群"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	gotToolCall := false
	gotAnswer := ""
	var gotUsage *llm.TokenUsage
	for ev := range events {
		switch ev.Type {
		case ChatEventToolCall:
			if ev.Tool != nil && ev.Tool.Name == "get_resources" {
				gotToolCall = true
			}
		case ChatEventAnswer:
			gotAnswer = ev.Text
			gotUsage = ev.Usage
		}
	}
	if !gotToolCall || gotAnswer != "集群有 1 个节点" {
		t.Fatalf("toolCall=%v answer=%q", gotToolCall, gotAnswer)
	}
	if gotUsage == nil || gotUsage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", gotUsage)
	}
	// 会话已持久化（3 条消息：user/tool/assistant）。
	got, err := svc.GetOrCreateSession("s1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 || got.Messages[0].Role != "user" || got.Messages[2].Role != "assistant" {
		t.Fatalf("session messages = %+v", got.Messages)
	}
}

func TestChatApproverSkip(t *testing.T) {
	ap := &chatApprover{events: make(chan ChatEvent, 4), skip: true}
	choice, err := ap.Confirm(context.Background(), "p", nil)
	if err != nil || choice != "yes" {
		t.Fatalf("skip 模式应直接放行: choice=%q err=%v", choice, err)
	}
}

func TestChatApproverInteractiveAndCancel(t *testing.T) {
	events := make(chan ChatEvent, 4)
	respond := make(chan string, 1)
	ap := &chatApprover{events: events, respond: respond}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ap.Confirm(context.Background(), "是否执行？", []agent.UserChoiceOption{{Label: "是", Value: "yes"}})
	}()
	ev := <-events
	if ev.Type != ChatEventChoice || ev.Choice == nil || len(ev.Choice.Options) != 1 {
		t.Fatalf("choice event = %+v", ev)
	}
	respond <- "yes"
	<-done

	ctx, cancel := context.WithCancel(context.Background())
	respond2 := make(chan string)
	ap2 := &chatApprover{events: events, respond: respond2}
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		_, _ = ap2.Confirm(ctx, "p", nil)
	}()
	<-events
	cancel()
	<-done2
}

func TestChatServiceSessionManagement(t *testing.T) {
	svc, _, _ := testChatService(t, &scriptedLLM{})
	_, _ = svc.GetOrCreateSession("a", "", "m")
	_, _ = svc.GetOrCreateSession("b", "", "m")
	list, err := svc.ListSessions()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d err=%v", len(list), err)
	}
	if err := svc.ClearSession("a"); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetOrCreateSession("a", "", "")
	if len(got.Messages) != 0 {
		t.Fatal("clear 后消息应为空")
	}
	if err := svc.DeleteSession("b"); err != nil {
		t.Fatal(err)
	}
	list, _ = svc.ListSessions()
	if len(list) != 1 {
		t.Fatalf("delete 后 list = %d", len(list))
	}
}

func TestChatServiceScanClusterTool(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "scan_cluster", Arguments: json.RawMessage(`{"namespace":"production"}`)}}},
		{Content: "发现 1 个问题"},
	}}
	svc, _, _ := testChatService(t, fl)
	if _, err := svc.GetOrCreateSession("s1", "", "m"); err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "检查集群"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	gotScan := false
	gotAnswer := ""
	for ev := range events {
		switch ev.Type {
		case ChatEventToolCall:
			if ev.Tool != nil && ev.Tool.Name == "scan_cluster" {
				gotScan = true
			}
		case ChatEventAnswer:
			gotAnswer = ev.Text
		}
	}
	if !gotScan || gotAnswer != "发现 1 个问题" {
		t.Fatalf("scan=%v answer=%q", gotScan, gotAnswer)
	}
}

func TestChatServiceWriteApproval(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "scale_deployment", Arguments: json.RawMessage(`{"namespace":"production","name":"payment-api","replicas":5}`)}}},
		{Content: "已扩容到 5 副本"},
	}}
	svc, _, wr := testChatService(t, fl)
	if _, err := svc.GetOrCreateSession("s1", "", "m"); err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "扩容 payment-api"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	gotChoice := false
	gotAnswer := ""
	for ev := range events {
		switch ev.Type {
		case ChatEventChoice:
			gotChoice = true
			respond <- "yes"
		case ChatEventAnswer:
			gotAnswer = ev.Text
		}
	}
	if !gotChoice || gotAnswer != "已扩容到 5 副本" {
		t.Fatalf("choice=%v answer=%q", gotChoice, gotAnswer)
	}
	if wr.lastReplicas != 5 {
		t.Fatalf("写工具未执行: replicas=%d", wr.lastReplicas)
	}
}
