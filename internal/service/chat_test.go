package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/agent"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

func TestSummarizeToolPrelude(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "short progress", text: "我来检查下 test 命名空间下面的服务运行情况。", want: "我来检查下 test 命名空间下面的服务运行情况。"},
		{name: "report conclusion", text: "根因是 PVC 配置错误，下面给出修复方案。", want: "根因是 PVC 配置错误，下面给出修复方案。"},
		{name: "repair wording in progress", text: "我先检查 PVC，确认是 storageClass 错误，随后给出修复方案。", want: "我先检查 PVC，确认是 storageClass 错误，随后给出修复方案。"},
		{name: "markdown report", text: "## 诊断报告\n服务异常", want: ""},
		{name: "too long", text: strings.Repeat("检查证据 ", 40), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeToolPrelude(tt.text); got != tt.want {
				t.Fatalf("summarizeToolPrelude() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPersistReasoningInsertsBeforeLatestAssistant(t *testing.T) {
	sess := &session.Session{Messages: []session.Message{
		{Role: "user", Type: "text", Content: "u1"},
		{Role: "assistant", Type: "text", Content: "a1"},
		{Role: "user", Type: "text", Content: "u2"},
		{Role: "assistant", Type: "text", Content: "a2"},
	}}
	persistReasoning(sess, "思考过程")
	if len(sess.Messages) != 5 {
		t.Fatalf("messages = %+v", sess.Messages)
	}
	if sess.Messages[3].Type != "reasoning" || sess.Messages[3].Content != "思考过程" {
		t.Fatalf("reasoning message = %+v", sess.Messages[3])
	}
	if sess.Messages[4].Content != "a2" {
		t.Fatalf("latest assistant position = %+v", sess.Messages)
	}
}

func TestTranslateReasoningDeltaEvent(t *testing.T) {
	ev, ok := translateEvent(agent.Message{Type: agent.MessageTypeReasoningDelta, Payload: "思考"})
	if !ok {
		t.Fatal("reasoning delta 应被翻译")
	}
	if ev.Type != ChatEventReasoningDelta || ev.Text != "思考" {
		t.Fatalf("event = %+v", ev)
	}
}

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
	gotToolOk := false
	gotAnswer := ""
	var gotUsage *llm.TokenUsage
	for ev := range events {
		switch ev.Type {
		case ChatEventToolCall:
			if ev.Tool != nil && ev.Tool.Name == "get_resources" {
				gotToolCall = true
			}
		case ChatEventToolResponse:
			if ev.Tool != nil && ev.Tool.Ok != nil && *ev.Tool.Ok {
				gotToolOk = true
			}
		case ChatEventAnswer:
			gotAnswer = ev.Text
			gotUsage = ev.Usage
		}
	}
	if !gotToolCall || !gotToolOk || gotAnswer != "集群有 1 个节点" {
		t.Fatalf("toolCall=%v toolOk=%v answer=%q", gotToolCall, gotToolOk, gotAnswer)
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

func TestChatServiceDisabledTools(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_resources", Arguments: json.RawMessage(`{"kind":"nodes"}`)}}},
		{Content: "done"},
	}}
	svc, cfg, _ := testChatService(t, fl)
	cfg.Agent.DisabledTools = []string{"get_resources"}
	if _, err := svc.GetOrCreateSession("s1", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "list nodes"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	gotUnknown := false
	for ev := range events {
		if ev.Type == ChatEventToolResponse && ev.Tool != nil && strings.Contains(ev.Tool.Result, "unknown tool") {
			gotUnknown = true
		}
	}
	if !gotUnknown {
		t.Fatal("disabled tool 应被移除，工具调用应返回 unknown tool")
	}
}

func TestChatServiceConfigSkipPermissions(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "kubectl", Arguments: json.RawMessage(`{"command":"kubectl scale deployment/web --replicas=2"}`)}}},
		{Content: "done"},
	}}
	svc, cfg, wr := testChatService(t, fl)
	cfg.Agent.SkipPermissions = true
	if _, err := svc.GetOrCreateSession("s1", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "scale web"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	gotChoice := false
	for ev := range events {
		if ev.Type == ChatEventChoice {
			gotChoice = true
		}
	}
	if gotChoice {
		t.Fatal("config skip_permissions=true 不应发出审批请求")
	}
	if wr.lastCommand != "kubectl scale deployment/web --replicas=2" {
		t.Fatalf("kubectl 命令未执行: %q", wr.lastCommand)
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
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "kubectl", Arguments: json.RawMessage(`{"command":"kubectl scale deployment/payment-api -n production --replicas=5"}`)}}},
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
	if !strings.Contains(wr.lastCommand, "scale") {
		t.Fatalf("写工具未执行: cmd=%q", wr.lastReplicas)
	}
}
