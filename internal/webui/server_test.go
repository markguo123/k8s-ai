package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// fakeChat 是 webui 测试用 ChatService 桩。
type fakeChat struct {
	mu        sync.Mutex
	sessions  map[string]*session.Session
	script    []service.ChatEvent
	gotChoice string
}

func newFakeChat(script ...service.ChatEvent) *fakeChat {
	return &fakeChat{sessions: map[string]*session.Session{}, script: script}
}

func (f *fakeChat) GetOrCreateSession(id, _, _ string) (*session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok {
		return s, nil
	}
	s := &session.Session{ID: id, Name: "Session " + id}
	f.sessions[id] = s
	return s, nil
}

func (f *fakeChat) ListSessions() ([]session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]session.Session, 0, len(f.sessions))
	for _, s := range f.sessions {
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeChat) DeleteSession(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}

func (f *fakeChat) RenameSession(id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok {
		s.Name = name
	}
	return nil
}

func (f *fakeChat) PinSession(id string, pinned bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok {
		s.Pinned = pinned
	}
	return nil
}

func (f *fakeChat) Chat(ctx context.Context, req service.ChatRequest, respond <-chan string) (<-chan service.ChatEvent, error) {
	out := make(chan service.ChatEvent, len(f.script)+4)
	go func() {
		defer close(out)
		for _, e := range f.script {
			if e.Type == service.ChatEventToolResponse && e.Tool != nil {
				f.persistToolResponse(req.SessionID, e.Tool)
			}
			if e.Type == service.ChatEventChoice {
				select {
				case <-ctx.Done():
					return
				case c := <-respond:
					f.mu.Lock()
					f.gotChoice = c
					f.mu.Unlock()
					out <- service.ChatEvent{Type: service.ChatEventAnswer, Text: "approved:" + c}
					return
				}
			}
			out <- e
		}
	}()
	return out, nil
}

func (f *fakeChat) persistToolResponse(id string, tool *service.ChatToolCallView) {
	if id == "" || tool == nil {
		return
	}
	var args json.RawMessage
	if b, err := json.Marshal(tool.Arguments); err == nil {
		args = b
	}
	ok := false
	if tool.Ok != nil {
		ok = *tool.Ok
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, found := f.sessions[id]
	if !found {
		return
	}
	sess.AddMessage(session.Message{Role: "tool", Type: "tool-call-response", Name: tool.Name, Arguments: args, Data: tool.Data, Ok: &ok, Content: tool.Result})
}

func postJSON(t *testing.T, url string, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type cancelChat struct {
	*fakeChat
	started  chan struct{}
	canceled chan struct{}
}

func (c *cancelChat) Chat(ctx context.Context, _ service.ChatRequest, _ <-chan string) (<-chan service.ChatEvent, error) {
	out := make(chan service.ChatEvent, 4)
	go func() {
		close(c.started)
		<-ctx.Done()
		close(c.canceled)
		close(out)
	}()
	return out, nil
}

func TestIndexAndSessionCRUD(t *testing.T) {
	srv := httptest.NewServer(New(newFakeChat(), "qwen-plus").Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if !strings.Contains(string(body[:n]), "k8s-ai") {
		t.Fatal("首页缺少 k8s-ai 品牌")
	}

	resp = postJSON(t, srv.URL+"/api/sessions", `{"id":"s1","name":"incident-001"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, _ = http.Get(srv.URL + "/api/sessions")
	var list []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0]["name"] != "incident-001" {
		t.Fatalf("sessions = %v", list)
	}

	resp = postJSON(t, srv.URL+"/api/sessions/s1/rename", `{"name":"renamed"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions/s1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %v err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()
}

func TestCancelActiveRun(t *testing.T) {
	fake := &cancelChat{
		fakeChat: newFakeChat(),
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	_, _ = fake.GetOrCreateSession("s1", "", "m")
	srv := httptest.NewServer(New(fake, "m").Handler())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/sessions/s1/send-message", `{"query":"检查"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not start")
	}

	resp = postJSON(t, srv.URL+"/api/sessions/s1/cancel", `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case <-fake.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("context was not canceled")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp = postJSON(t, srv.URL+"/api/sessions/s1/cancel", `{}`)
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			break
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("active run cleanup did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startSSE 打开一条 SSE 连接：收到 history 快照后通知 connected，继续读到 want 事件。
// 必须在独立 goroutine 中调用（阻塞式）。
func startSSE(url string, connected chan<- struct{}, want string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		switch {
		case strings.Contains(data, "history"):
			close(connected)
		case strings.Contains(data, want):
			return data, nil
		}
	}
	return "", sc.Err()
}

func readHistory(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if strings.Contains(data, "history") {
			return data, nil
		}
	}
	return "", sc.Err()
}

type sseResult struct {
	data string
	err  error
}

func TestStreamAndSendMessage(t *testing.T) {
	fake := newFakeChat(
		service.ChatEvent{Type: service.ChatEventToolCall, Tool: &service.ChatToolCallView{Name: "get_resources"}},
		service.ChatEvent{Type: service.ChatEventAnswer, Text: "完成"},
	)
	srv := httptest.NewServer(New(fake, "m").Handler())
	defer srv.Close()
	_, _ = fake.GetOrCreateSession("s1", "", "m")

	connected := make(chan struct{})
	res := make(chan sseResult, 1)
	go func() {
		data, err := startSSE(srv.URL+"/api/sessions/s1/stream", connected, "完成")
		res <- sseResult{data: data, err: err}
	}()
	<-connected

	resp := postJSON(t, srv.URL+"/api/sessions/s1/send-message", `{"query":"检查"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case r := <-res:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !strings.Contains(r.data, "answer") {
			t.Fatalf("SSE 数据异常: %s", r.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到 answer 事件")
	}
}

func TestCreateResourceHistoryRecovery(t *testing.T) {
	dryData := map[string]any{
		"mode":             "dry_run",
		"status":           "validated",
		"proposalId":       "prop-1",
		"proposalManifest": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: web\ntype: Opaque\nstringData:\n  password: <generated>",
		"objects": []any{
			map[string]any{
				"kind": "Secret", "name": "db", "namespace": "web", "status": "validated",
				"secretKeys": []any{map[string]any{"key": "password", "length": 16, "sha256": "abc123"}},
			},
		},
	}
	appData := map[string]any{
		"mode": "apply", "status": "applied", "applied": 1, "refused": 0,
		"objects": []any{
			map[string]any{
				"kind": "Secret", "name": "db", "namespace": "web", "status": "applied", "uid": "uid-1",
				"verify": map[string]any{"ready": true, "reason": "created ok"},
			},
		},
		"secretTouches": []any{
			map[string]any{"kind": "Secret", "namespace": "web", "name": "db", "keys": []any{map[string]any{"key": "password", "length": 16, "sha256": "abc123"}}},
		},
	}
	approveOK := true
	script := []service.ChatEvent{
		{Type: service.ChatEventToolCall, Tool: &service.ChatToolCallView{Name: "create_resource", Arguments: map[string]any{"mode": "dry_run", "manifest": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: db"}}},
		{Type: service.ChatEventToolResponse, Tool: &service.ChatToolCallView{Name: "create_resource", Result: "dry run ok", Ok: &approveOK, Data: dryData}},
		{Type: service.ChatEventToolCall, Tool: &service.ChatToolCallView{Name: "create_resource", Arguments: map[string]any{"mode": "apply", "proposal_id": "prop-1"}}},
		{Type: service.ChatEventToolResponse, Tool: &service.ChatToolCallView{Name: "create_resource", Result: "created", Ok: &approveOK, Data: appData}},
		{Type: service.ChatEventAnswer, Text: "完成"},
	}
	fake := newFakeChat(script...)
	srv := httptest.NewServer(New(fake, "m").Handler())
	defer srv.Close()
	_, _ = fake.GetOrCreateSession("s1", "", "m")

	connected := make(chan struct{})
	res := make(chan sseResult, 1)
	go func() {
		data, err := startSSE(srv.URL+"/api/sessions/s1/stream", connected, "完成")
		res <- sseResult{data: data, err: err}
	}()
	<-connected
	resp := postJSON(t, srv.URL+"/api/sessions/s1/send-message", `{"query":"创建"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatal(r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到完成事件")
	}

	history, err := readHistory(srv.URL + "/api/sessions/s1/stream")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(history, "create_resource") || !strings.Contains(history, "proposalManifest") {
		t.Fatalf("会话历史未恢复创建提案: %s", history)
	}
	if !strings.Contains(history, "applied") || !strings.Contains(history, "ready") {
		t.Fatalf("会话历史未恢复创建结果: %s", history)
	}
	if !strings.Contains(history, "sha256") {
		t.Fatalf("会话历史应包含 Secret 指纹: %s", history)
	}
	if strings.Contains(history, "Gientech@123") {
		t.Fatalf("会话历史不该包含 Secret 明文值: %s", history)
	}
}

func TestApproveFlow(t *testing.T) {
	fake := newFakeChat(service.ChatEvent{Type: service.ChatEventChoice, Choice: &service.ChatChoice{Prompt: "是否执行？"}})
	srv := httptest.NewServer(New(fake, "m").Handler())
	defer srv.Close()
	_, _ = fake.GetOrCreateSession("s1", "", "m")

	connected := make(chan struct{})
	res := make(chan sseResult, 1)
	go func() {
		data, err := startSSE(srv.URL+"/api/sessions/s1/stream", connected, "approved:")
		res <- sseResult{data: data, err: err}
	}()
	<-connected

	resp := postJSON(t, srv.URL+"/api/sessions/s1/send-message", `{"query":"扩容"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = postJSON(t, srv.URL+"/api/sessions/s1/approve", `{"choice":"yes"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	select {
	case r := <-res:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if !strings.Contains(r.data, "approved:yes") {
			t.Fatalf("SSE 数据异常: %s", r.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到审批结果事件")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gotChoice != "yes" {
		t.Fatalf("choice = %q", fake.gotChoice)
	}
}
