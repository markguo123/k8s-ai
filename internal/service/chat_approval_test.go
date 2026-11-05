package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/sse"
)

func TestChatServiceShellBypassBlockedForSkipPermissions(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:        "c1",
			Name:      "kubectl",
			Arguments: json.RawMessage(`{"command":"kubectl get pods | kubectl delete pod x"}`),
		}}},
		{Content: "done"},
	}}
	svc, cfg, wr := testChatService(t, fl)
	cfg.Agent.SkipPermissions = true
	if _, err := svc.GetOrCreateSession("s1", "", "m"); err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "s1", Query: "check pods"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	gotChoice := false
	for ev := range events {
		if ev.Type == ChatEventChoice {
			gotChoice = true
			respond <- "no"
		}
	}
	if !gotChoice {
		t.Fatal("shell command should require approval even with skip_permissions=true")
	}
	if wr.lastCommand != "" {
		t.Fatalf("shell command auto-executed despite skip_permissions; cmd=%q", wr.lastCommand)
	}
}

func TestApprovalEventCarriesStructuredKubectlPatch(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{
			ID:        "c1",
			Name:      "kubectl",
			Arguments: json.RawMessage(`{"command":"kubectl -n test-complex patch deployment app-backend --type=json -p='[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/httpGet/path\",\"value\":\"/\"},{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/readinessProbe/httpGet/path\",\"value\":\"/\"}]'"}`),
		}}},
		{Content: "已拒绝执行"},
	}}
	col := &collector{}
	svc := testChatServiceWithSink(t, fl, col)
	if _, err := svc.GetOrCreateSession("approval-structured", "", "qwen-plus"); err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: "approval-structured", Query: "修复探针"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	for ev := range events {
		if ev.Type == ChatEventChoice {
			respond <- "no"
		}
	}

	approval, ok := col.event(sse.EventApprovalRequired)
	if !ok {
		t.Fatal("缺少 approval.required 事件")
	}
	data, ok := approval.Data.(sse.ApprovalData)
	if !ok {
		t.Fatalf("approval data = %#v", approval.Data)
	}
	if data.Target == nil || data.Target.Namespace != "test-complex" || data.Target.Kind != "deployment" {
		t.Fatalf("target = %+v", data.Target)
	}
	if len(data.Changes) != 2 {
		t.Fatalf("changes = %+v", data.Changes)
	}
	for _, option := range data.Options {
		if option.Value == "yes_and_dont_ask_again" {
			t.Fatal("HIGH risk approval must not offer session-wide approval")
		}
	}
}
