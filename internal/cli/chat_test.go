package cli

import (
	"bufio"
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

// TestBufInput 覆盖非 TTY 回退输入：SetPrompt 后 Readline 打印提示符并按行返回。
func TestBufInput(t *testing.T) {
	var out bytes.Buffer
	in := &bufInput{r: bufio.NewReader(strings.NewReader("yes\n")), w: &out}
	in.SetPrompt("选择: ")
	line, err := in.Readline()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if line != "yes" {
		t.Fatalf("line = %q, want yes", line)
	}
	if !strings.Contains(out.String(), "选择:") {
		t.Fatalf("提示符未打印: %q", out.String())
	}
	// 第二次无提示符，回读直到 EOF。
	line, err = in.Readline()
	if err == nil {
		t.Fatal("EOF 应返回错误")
	}
	if line != "" {
		t.Fatalf("EOF 行应为空, got %q", line)
	}
}

// TestRenderMarkdown 覆盖 Markdown 渲染：空文本原样、非空渲染后非空且含原文关键词。
func TestRenderMarkdown(t *testing.T) {
	md := newMarkdownRenderer()
	if got := renderMarkdown(md, ""); got != "" {
		t.Fatalf("空文本应原样返回, got %q", got)
	}
	got := renderMarkdown(md, "| 列 A | 列 B |\n|---|---|\n| 1 | 2 |")
	if strings.TrimSpace(got) == "" {
		t.Fatal("Markdown 渲染结果不应为空")
	}
	if !strings.Contains(got, "列") {
		t.Fatalf("渲染结果应保留原文关键词: %q", got)
	}
	// 渲染器为 nil 时原样返回。
	if got := renderMarkdown(nil, "**x**"); got != "**x**" {
		t.Fatalf("nil 渲染器应原样返回, got %q", got)
	}
}
