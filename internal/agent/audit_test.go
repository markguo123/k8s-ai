package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// stubRecorder 收集审计事件动作名。
type stubRecorder struct {
	mu     sync.Mutex
	events []string
}

func (s *stubRecorder) Write(_ context.Context, e audit.Event) error {
	s.mu.Lock()
	s.events = append(s.events, e.Action)
	s.mu.Unlock()
	return nil
}

func (s *stubRecorder) Close() error { return nil }

func TestChatAuditEvents(t *testing.T) {
	executed := 0
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "get_pod", `{"name":"web-0"}`)),
		{Content: "answer"},
	}}
	reg := tools.NewRegistry()
	_ = reg.Register(&stubTool{name: "get_pod", readonly: true, executed: &executed})
	rec := &stubRecorder{}
	a := New(Options{
		LLM:           fl,
		Tools:         reg,
		Session:       &session.Session{ID: "s"},
		Recorder:      rec,
		MaxIterations: 5,
		MaxToolCalls:  10,
	})
	if _, _, err := a.Chat(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	want := []string{
		audit.ActionUserInput,
		audit.ActionLLMRequest,
		audit.ActionLLMResponse,
		audit.ActionToolCallRequest,
		audit.ActionToolCallResponse,
	}
	for _, w := range want {
		found := false
		for _, e := range rec.events {
			if e == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少审计事件 %s（已记录 %v）", w, rec.events)
		}
	}
}
