package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

func persistTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite("file:chat-persist-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func testChatServiceWithStore(t *testing.T, fl *scriptedLLM) (ChatService, store.Store) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	wr := &fakeWriteRunner{}
	st := persistTestStore(t)
	svc := NewChatWithStore(cfg, func() (scanner.Reader, error) { return reader, nil },
		func(llm.Options) llm.LLMClient { return fl },
		func() tools.ScanRunner { return &fakeScanRunner{} },
		func() (tools.WriteRunner, error) { return wr, nil }, st)
	return svc, st
}

func TestChatServicePersistsTurnAndSteps(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "get_resources", Arguments: json.RawMessage(`{"kind":"nodes"}`)}}},
		{Content: "集群就绪，节点数正常", Usage: llm.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
	}}
	svc, st := testChatServiceWithStore(t, fl)
	ctx := context.Background()
	sess, err := svc.GetOrCreateSession("persist-s1", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}

	respond := make(chan string, 1)
	events, err := svc.Chat(ctx, ChatRequest{SessionID: sess.ID, Query: "检查集群"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}

	// 会话快照应已落库（含最终消息）。
	got, err := st.Sessions().Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got == nil || len(got.Messages) != 3 {
		t.Fatalf("persisted session = %+v", got)
	}

	// 本轮 Turn 应为唯一一条且以 done 收尾，输出是最终回答，token 用量已回写。
	turns, err := st.Turns().ListBySession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expect 1 turn, got %d", len(turns))
	}
	turn := turns[0]
	if turn.Status != session.TurnDone || turn.Output != "集群就绪，节点数正常" {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.Token.TotalTokens != 5 {
		t.Fatalf("turn token = %+v", turn.Token)
	}

	// Step 应包含 Reason/Tool/Observe/Response，且无残留 running/waiting。
	steps, err := st.Steps().ListByTurn(ctx, turn.ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) < 5 {
		t.Fatalf("expect >= 5 steps, got %d (%+v)", len(steps), steps)
	}
	types := map[session.StepType]int{}
	for _, s := range steps {
		types[s.Type]++
		if s.Status == session.StepRunning || s.Status == session.StepWaiting {
			t.Fatalf("non-terminal step %s(%s): %+v", s.Type, s.Status, s)
		}
	}
	if types[session.StepReason] != 2 || types[session.StepTool] != 1 ||
		types[session.StepObserve] != 1 || types[session.StepResponse] != 1 {
		t.Fatalf("step types = %+v", types)
	}
	// 工具参数按顺序落在 Step 记录里（非 Secret 内容原样保留）。
	if steps[1].Type != session.StepTool || !json.Valid([]byte(steps[1].Params)) {
		t.Fatalf("tool step = %+v", steps[1])
	}
}

func TestChatServiceWithoutStoreSkipsPersistence(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{Content: "直接回答", Usage: llm.TokenUsage{TotalTokens: 1}},
	}}
	svc, _, _ := testChatService(t, fl)
	sess, err := svc.GetOrCreateSession("no-store-s1", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: sess.ID, Query: "你好"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
}
