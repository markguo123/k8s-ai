package service

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/sse"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// collector 是测试用统一事件收集器，sink 可能被多个 goroutine 并发调用。
type collector struct {
	mu     sync.Mutex
	events []sse.Event
}

func (c *collector) sink(e sse.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) types() []sse.EventType {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sse.EventType, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.EventType)
	}
	return out
}

func (c *collector) contains(t sse.EventType) bool {
	for _, e := range c.events {
		if e.EventType == t {
			return true
		}
	}
	return false
}

func (c *collector) event(t sse.EventType) (sse.Event, bool) {
	for _, e := range c.events {
		if e.EventType == t {
			return e, true
		}
	}
	return sse.Event{}, false
}

func testChatServiceWithSink(t *testing.T, fl *scriptedLLM, col *collector) ChatService {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	wr := &fakeWriteRunner{}
	return NewChatWithSink(cfg,
		func() (scanner.Reader, error) { return reader, nil },
		func(llm.Options) llm.LLMClient { return fl },
		func() tools.ScanRunner { return &fakeScanRunner{} },
		func() (tools.WriteRunner, error) { return wr, nil },
		col.sink)
}

// TestChatServiceEmitsUnifiedEvents 覆盖 P1.9 验收：Chat 生命周期按统一事件契约
// 下发 session/turn/reason/tool/observe/message 事件，旧 ChatEvent 流不受影响。
func TestChatServiceEmitsUnifiedEvents(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "get_resources",
				Arguments: json.RawMessage(`{"kind":"nodes","secretKey":"sk-super-secret"}`),
			}},
		},
		{Content: "集群就绪，节点数正常", Usage: llm.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
	}}
	col := &collector{}
	svc := testChatServiceWithSink(t, fl, col)
	sess, err := svc.GetOrCreateSession("sse-s1", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}

	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: sess.ID, Query: "检查集群"}, respond)
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}

	for _, typ := range []sse.EventType{
		sse.EventSessionCreated,
		sse.EventTurnStarted,
		sse.EventReasonStarted,
		sse.EventToolStarted,
		sse.EventToolCompleted,
		sse.EventObservationCreated,
		sse.EventMessageCompleted,
		sse.EventTurnCompleted,
		sse.EventSessionCompleted,
	} {
		if !col.contains(typ) {
			t.Fatalf("缺少统一事件 %q，实际 %+v", typ, col.types())
		}
	}

	// 必需字段不为空：session/turn 事件携带标识与时间戳。
	sc, _ := col.event(sse.EventSessionCreated)
	if sc.SessionID != sess.ID || sc.Timestamp.IsZero() {
		t.Fatalf("session.created 信封 = %+v", sc)
	}
	tc, ok := col.event(sse.EventTurnCompleted)
	if !ok {
		t.Fatal("缺少 turn.completed")
	}
	if tc.SessionID != sess.ID || tc.TurnID == "" || tc.EventType != sse.EventTurnCompleted {
		t.Fatalf("turn.completed 信封 = %+v", tc)
	}

	// 工具参数在进入事件流前脱敏：敏感键名整值替换，Secret 明文不出站。
	ts, _ := col.event(sse.EventToolStarted)
	d, ok := ts.Data.(sse.StepData)
	if !ok {
		t.Fatalf("tool.started data = %#v", ts.Data)
	}
	if strings.Contains(string(d.Params), "sk-super-secret") || !strings.Contains(d.Params, "[REDACTED]") {
		t.Fatalf("tool.started params 未脱敏: %q", d.Params)
	}
}

// TestRunScopeConcurrent 覆盖 runScope 在并发读写下不产生数据竞争（配合 -race），
// 且 session 字段（不可变）恒可读。
func TestRunScopeConcurrent(t *testing.T) {
	scope := &runScope{sessID: "s1"}
	var wg sync.WaitGroup
	var bad atomic.Bool
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				scope.turnStarted("turn-x")
				if scope.session() != "s1" {
					bad.Store(true)
				}
				scope.stepStarted("step-y")
				_ = scope.turn()
				_ = scope.step()
			}
		}()
	}
	wg.Wait()
	if bad.Load() {
		t.Fatal("scope session 字段被并发改写")
	}
}
