package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/llm"
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
