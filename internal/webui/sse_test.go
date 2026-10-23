package webui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/sse"
)

// sseFakeChat 是支持 SetSink 的 ChatService 桩，用于验证 Web 装配统一事件路由。
type sseFakeChat struct {
	emitter func(func(sse.Event))
}

func (f *sseFakeChat) SetSink(sink func(sse.Event)) { f.emitter(sink) }
func (f *sseFakeChat) Chat(_ context.Context, _ service.ChatRequest, _ <-chan string) (<-chan service.ChatEvent, error) {
	return make(chan service.ChatEvent), nil
}
func (f *sseFakeChat) GetOrCreateSession(id, _, _ string) (*session.Session, error) {
	return &session.Session{ID: id}, nil
}
func (f *sseFakeChat) GetSession(id string) (*session.Session, error) {
	return &session.Session{ID: id}, nil
}
func (f *sseFakeChat) GetOrCreateSessionForUser(id, userID, _, _ string) (*session.Session, error) {
	return &session.Session{ID: id, UserID: userID}, nil
}
func (f *sseFakeChat) ListSessions() ([]session.Session, error)              { return nil, nil }
func (f *sseFakeChat) ListSessionsForUser(string) ([]session.Session, error) { return nil, nil }
func (f *sseFakeChat) DeleteSession(string) error                            { return nil }
func (f *sseFakeChat) RenameSession(string, string) error                    { return nil }
func (f *sseFakeChat) PinSession(string, bool) error                         { return nil }

// TestNewAttachesSink 覆盖 P1.9 事件路由：webui.New 给服务挂 sink，
// sink 下发的统一事件能按 session_id 扇出到该会话的 SSE 客户端。
func TestNewAttachesSink(t *testing.T) {
	var sink func(sse.Event)
	fake := &sseFakeChat{emitter: func(s func(sse.Event)) { sink = s }}
	srv := New(fake, "qwen-plus")
	if sink == nil {
		t.Fatal("webui.New 未给服务装配统一事件 sink")
	}

	b := srv.broadcaster("sse-web-1")
	ch := b.add()
	defer b.remove(ch)
	ev := sse.New("sse-web-1", "t1", "st1", sse.EventToolStarted, sse.StepData{Kind: sse.StepKindTool, Tool: "kubectl"})
	sink(ev)

	select {
	case raw := <-ch:
		var got sse.Event
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("sink 广播不是合法统一事件 JSON: %v", err)
		}
		if got.SessionID != "sse-web-1" || got.EventType != sse.EventToolStarted || got.TurnID != "t1" || got.StepID != "st1" {
			t.Fatalf("sink 广播 = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sink 广播未到达会话 SSE 客户端")
	}
}
