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

func testChatServiceWithSink(t *testing.T, fl llm.LLMClient, col *collector) ChatService {
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

// typedStreamLLM 是带语义流事件的脚本化 LLM：先下发 reasoning 增量，再下发正文。
type typedStreamLLM struct {
	reasoning []string
	content   string
}

func (t *typedStreamLLM) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: t.content, Usage: llm.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}, nil
}

func (t *typedStreamLLM) ChatStreamEvents(_ context.Context, _ llm.ChatRequest, onEvent func(llm.StreamEvent)) (*llm.ChatResponse, error) {
	for _, d := range t.reasoning {
		onEvent(llm.StreamEvent{Type: llm.StreamReasoningDelta, Text: d})
	}
	if t.content != "" {
		onEvent(llm.StreamEvent{Type: llm.StreamContentDelta, Text: t.content})
	}
	return &llm.ChatResponse{Content: t.content, Usage: llm.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}, nil
}

// TestChatServiceEventOrdering 锁定统一事件的两条契约：
//  1. 所有 message.delta 必须早于 message.completed，message.completed 早于 session.completed；
//     否则最后一段正文落在终态之后，前端会新建分片并停在“正在生成…”。
//  2. message.delta 的 index 按 role 各自从 0 计数（reasoning 分片不会推高报告分片序号）。
func TestChatServiceEventOrdering(t *testing.T) {
	fl := &typedStreamLLM{reasoning: []string{"先看", "Pod 列表"}, content: "## 巡检报告\n\n一切正常"}
	col := &collector{}
	svc := testChatServiceWithSink(t, fl, col)
	sess, err := svc.GetOrCreateSession("sse-order", "", "qwen-plus")
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

	col.mu.Lock()
	collected := append([]sse.Event(nil), col.events...)
	col.mu.Unlock()

	firstOf := func(typ sse.EventType) int {
		for i, e := range collected {
			if e.EventType == typ {
				return i
			}
		}
		return -1
	}
	lastOf := func(typ sse.EventType) int {
		last := -1
		for i, e := range collected {
			if e.EventType == typ {
				last = i
			}
		}
		return last
	}

	lastDelta := lastOf(sse.EventMessageDelta)
	completed := firstOf(sse.EventMessageCompleted)
	sessionDone := firstOf(sse.EventSessionCompleted)
	if lastDelta < 0 || completed < 0 || sessionDone < 0 {
		t.Fatalf("事件缺失：delta=%d completed=%d session=%d（%v）", lastDelta, completed, sessionDone, col.types())
	}
	if !(lastDelta < completed && completed < sessionDone) {
		t.Fatalf("终态事件顺序错误：last delta=%d message.completed=%d session.completed=%d（%v）",
			lastDelta, completed, sessionDone, col.types())
	}

	// 分片序号按 role 各自从 0 开始：reasoning 的两片是 0/1，报告正文是 0。
	seen := map[string][]int{}
	for _, e := range collected {
		if e.EventType != sse.EventMessageDelta {
			continue
		}
		d, ok := e.Data.(sse.MessageData)
		if !ok {
			t.Fatalf("message.delta data = %#v", e.Data)
		}
		seen[d.Role] = append(seen[d.Role], d.Index)
	}
	if got := seen["reasoning"]; len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("reasoning 分片序号 = %v，期望 [0 1]", got)
	}
	if got := seen["assistant"]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("assistant 分片序号 = %v，期望 [0]", got)
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
