package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// stubChatService 是 CLI 测试用的 ChatService 桩（内存会话 + 脚本事件）。
type stubChatService struct {
	events []service.ChatEvent
	mgr    *session.Manager
}

func newStubChatService(events []service.ChatEvent) *stubChatService {
	return &stubChatService{events: events, mgr: session.NewManager(session.NewMemoryStore())}
}

func (s *stubChatService) Chat(_ context.Context, _ service.ChatRequest, _ <-chan string) (<-chan service.ChatEvent, error) {
	out := make(chan service.ChatEvent, len(s.events)+1)
	for _, e := range s.events {
		out <- e
	}
	close(out)
	return out, nil
}

func (s *stubChatService) GetOrCreateSession(id, ns, model string) (*session.Session, error) {
	return s.mgr.NewSession(id, "", ns, model)
}

func (s *stubChatService) ListSessions() ([]session.Session, error) {
	list, err := s.mgr.List()
	if err != nil {
		return nil, err
	}
	out := make([]session.Session, 0, len(list))
	for _, sess := range list {
		out = append(out, *sess)
	}
	return out, nil
}

func (s *stubChatService) DeleteSession(id string) error {
	return s.mgr.Delete(id)
}

func (s *stubChatService) ClearSession(id string) error {
	sess, err := s.mgr.Get(id)
	if err != nil {
		return err
	}
	sess.ClearMessages()
	return s.mgr.Update(sess)
}

func (s *stubChatService) RenameSession(id, name string) error {
	sess, err := s.mgr.Get(id)
	if err != nil {
		return err
	}
	sess.Name = name
	return s.mgr.Update(sess)
}

func chatTestDeps(stub *stubChatService) Dependencies {
	return Dependencies{
		Version: "test",
		LoadConfig: func(config.LoadOptions) (*config.Config, error) {
			cfg, err := config.Load(config.LoadOptions{NoEnv: true})
			if err != nil {
				return nil, err
			}
			return cfg, nil
		},
		NewScan: nil,
		NewChat: func(*config.Config) service.ChatService { return stub },
	}
}

func TestChatSingleShotJSON(t *testing.T) {
	stub := newStubChatService([]service.ChatEvent{
		{Type: service.ChatEventToolCall, Tool: &service.ChatToolCallView{Name: "get_resources", Arguments: map[string]any{"kind": "nodes"}}},
		{Type: service.ChatEventAnswer, Text: "集群有 1 个节点", Usage: &llm.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}},
	})
	root := NewRootCmd(chatTestDeps(stub))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"chat", "--format", "json", "--session", "s1", "检查集群"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{`"sessionId": "s1"`, `"answer": "集群有 1 个节点"`, `"toolCalls"`, `"executed": false`} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 输出缺少 %s: %s", want, s)
		}
	}
}

func TestChatListAndDeleteSessions(t *testing.T) {
	stub := newStubChatService(nil)
	_, _ = stub.GetOrCreateSession("incident-001", "", "m")
	root := NewRootCmd(chatTestDeps(stub))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"chat", "--list-sessions"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "incident-001") {
		t.Fatalf("列表缺少会话: %s", out.String())
	}

	out.Reset()
	root2 := NewRootCmd(chatTestDeps(stub))
	root2.SetOut(&out)
	root2.SetArgs([]string{"chat", "--delete-session", "incident-001"})
	if err := root2.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deleted") {
		t.Fatalf("删除输出 = %s", out.String())
	}
	list, _ := stub.ListSessions()
	if len(list) != 0 {
		t.Fatal("删除后会话应清空")
	}
}

func TestChatInteractiveExit(t *testing.T) {
	stub := newStubChatService(nil)
	root := NewRootCmd(chatTestDeps(stub))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetIn(strings.NewReader("exit\n"))
	root.SetArgs([]string{"chat"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
