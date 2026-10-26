package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// P3.13 平台能力测试：会话列表 / 历史 / SSE / 发消息 / 审批 / 取消 / 改名 /
// 置顶 / 删除的 owner 越权矩阵，以及 U-06（admin 跨用户操作统一留痕）。

// auditCapture 收集 webui 写出的审计事件，用于断言留痕动作与红线内容。
type auditCapture struct {
	mu     sync.Mutex
	events []store.AuditEvent
}

func (a *auditCapture) sink(_ context.Context, e store.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

// byAction 返回该动作的全部审计事件（保持写入顺序）。
func (a *auditCapture) byAction(action string) []store.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []store.AuditEvent
	for _, e := range a.events {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// eventsSnapshot 返回全部已收集事件（红线断言用）。
func (a *auditCapture) eventsSnapshot() []store.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]store.AuditEvent(nil), a.events...)
}

// payloadOf 返回最近一条该动作事件的 payload（解析失败即视为测试失败）。
func (a *auditCapture) payloadOf(t *testing.T, action string) map[string]any {
	t.Helper()
	events := a.byAction(action)
	if len(events) == 0 {
		t.Fatalf("缺少审计动作 %s，已记录 %+v", action, a.actions())
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[len(events)-1].Payload), &payload); err != nil {
		t.Fatalf("动作 %s 的 payload 不是 JSON: %v", action, err)
	}
	return payload
}

func (a *auditCapture) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.events))
	for _, e := range a.events {
		out = append(out, e.Action)
	}
	return out
}

// heldRunChat 让 Chat 一直运行到 ctx 取消或收到审批回传，用于制造
// "运行中 / 待审批"窗口，从而能真实走到 cancel 与 approve 分支。
type heldRunChat struct {
	*fakeChat
	started chan struct{}
	done    chan struct{}
	choices chan string
}

func newHeldRunChat() *heldRunChat {
	return &heldRunChat{
		fakeChat: newFakeChat(),
		started:  make(chan struct{}, 4),
		done:     make(chan struct{}, 4),
		choices:  make(chan string, 4),
	}
}

func (c *heldRunChat) Chat(ctx context.Context, _ service.ChatRequest, respond <-chan string) (<-chan service.ChatEvent, error) {
	out := make(chan service.ChatEvent)
	go func() {
		defer close(out)
		c.started <- struct{}{}
		defer func() { c.done <- struct{}{} }()
		select {
		case <-ctx.Done():
		case choice := <-respond:
			c.choices <- choice
		}
	}()
	return out, nil
}

// ownerHarness 装配一套带认证与审计的 webui 测试服务。
func ownerHarness(t *testing.T, chat chatService) (*httptest.Server, *auditCapture) {
	t.Helper()
	capture := &auditCapture{}
	srv := httptest.NewServer(New(chat, "m").
		WithAudit(capture.sink).
		AuthenticatedHandler(ownershipResolver(
			map[string]string{
				"tok-owner":    "owner",
				"tok-outsider": "outsider",
				"tok-chatperm": "chatperm",
				"tok-reader":   "reader",
				"tok-admin":    "admin",
			},
			map[string][]string{
				"owner":    nil,
				"outsider": nil,
				"chatperm": {permission.ChatRead, permission.ChatWrite},
				"reader":   {permission.ChatReadAll},
				"admin":    {"*"},
			},
		)))
	t.Cleanup(srv.Close)
	return srv, capture
}

// call 发一次带 Bearer Token 的会话请求，返回状态码与响应体。
func call(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// streamFrame 打开 SSE 连接读第一帧（历史快照）。非 200 时 frame 为 nil。
func streamFrame(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "data: ")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("SSE 首帧不是合法 JSON: %v", err)
		}
		return resp.StatusCode, frame
	}
	t.Fatal("SSE 未推送历史快照")
	return 0, nil
}

// foreignSurfaces 是他人会话的全部可访问面（P3.13 验收清单）。
func foreignSurfaces(base, id string) []struct {
	name   string
	method string
	url    string
	body   string
} {
	s := func(p string) string { return base + "/api/v1/sessions/" + id + p }
	return []struct {
		name   string
		method string
		url    string
		body   string
	}{
		{"历史/SSE", http.MethodGet, s("/stream"), ""},
		{"续用会话", http.MethodPost, base + "/api/v1/sessions", `{"id":"` + id + `"}`},
		{"发消息", http.MethodPost, s("/send-message"), `{"query":"hijack attempt"}`},
		{"审批", http.MethodPost, s("/approve"), `{"choice":"yes"}`},
		{"取消", http.MethodPost, s("/cancel"), `{}`},
		{"改名", http.MethodPost, s("/rename"), `{"name":"hijacked"}`},
		{"置顶", http.MethodPost, s("/pin"), `{"pinned":true}`},
		{"删除", http.MethodDelete, s(""), ""},
	}
}

func newOwnerChat() *fakeChat {
	chat := newFakeChat()
	chat.sessions["s-owner"] = &session.Session{
		ID: "s-owner", UserID: "u-owner", Name: "owner session",
		Messages: []session.Message{{Role: "user", Type: "text", Content: "keep this out of audit"}},
	}
	chat.sessions["s-other"] = &session.Session{ID: "s-other", UserID: "u-other", Name: "other session"}
	return chat
}

// TestForeignUserCannotTouchAnySessionSurface 覆盖零权限用户与只持有
// chat.read/chat.write 的用户：会话每个面都返回 404（不泄漏存在性），
// 且会话状态、Chat 运行、审计留痕都不受影响。
func TestForeignUserCannotTouchAnySessionSurface(t *testing.T) {
	for _, token := range []string{"tok-outsider", "tok-chatperm"} {
		t.Run(token, func(t *testing.T) {
			chat := newHeldRunChat()
			chat.sessions["s-owner"] = &session.Session{ID: "s-owner", UserID: "u-owner", Name: "owner session"}
			chat.sessions["s-other"] = &session.Session{ID: "s-other", UserID: "u-other", Name: "other session"}
			srv, capture := ownerHarness(t, chat)

			// 会话列表只回自己的（这两个用户都没有会话）。
			status, body := call(t, http.MethodGet, srv.URL+"/api/v1/sessions", token, "")
			if status != http.StatusOK {
				t.Fatalf("list = %d, want 200", status)
			}
			if strings.Contains(body, "s-owner") || strings.Contains(body, "s-other") {
				t.Fatalf("会话列表泄漏他人会话: %s", body)
			}

			for _, surface := range foreignSurfaces(srv.URL, "s-owner") {
				status, _ := call(t, surface.method, surface.url, token, surface.body)
				if status != http.StatusNotFound {
					t.Errorf("%s 越权 = %d, want 404", surface.name, status)
				}
			}

			chat.mu.Lock()
			sess, ok := chat.sessions["s-owner"]
			if !ok {
				chat.mu.Unlock()
				t.Fatal("越权删除竟移除了他人会话")
			}
			if sess.Name != "owner session" || sess.Pinned {
				t.Fatalf("越权改名/置顶生效: name=%q pinned=%v", sess.Name, sess.Pinned)
			}
			chat.mu.Unlock()

			if len(chat.started) != 0 {
				t.Fatal("越权发消息竟启动了 Chat 运行")
			}
			if got := capture.actions(); len(got) != 0 {
				t.Fatalf("非跨用户请求不应写审计，实得 %+v", got)
			}
		})
	}
}

// TestReadAllAdminCannotWriteOthersSessions 覆盖只授予 chat.read.all 的账号：
// 可读历史/SSE，但每个写面仍 404；读取本身按 U-06 留痕。
func TestReadAllAdminCannotWriteOthersSessions(t *testing.T) {
	chat := newOwnerChat()
	srv, capture := ownerHarness(t, chat)

	status, frame := streamFrame(t, srv.URL+"/api/v1/sessions/s-owner/stream", "tok-reader")
	if status != http.StatusOK || frame == nil {
		t.Fatalf("read.all 读历史 = %d, want 200", status)
	}
	if frame["type"] != "history" || frame["sessionId"] != "s-owner" {
		t.Fatalf("历史快照帧异常: %+v", frame)
	}
	if _, body := call(t, http.MethodGet, srv.URL+"/api/v1/sessions", "tok-reader", ""); !strings.Contains(body, "s-other") {
		t.Fatalf("read.all 应能列出全部会话: %s", body)
	}

	for _, surface := range foreignSurfaces(srv.URL, "s-owner") {
		if surface.name == "历史/SSE" || surface.name == "续用会话" {
			continue
		}
		status, _ := call(t, surface.method, surface.url, "tok-reader", surface.body)
		if status != http.StatusNotFound {
			t.Errorf("%s 仅读管理员越权 = %d, want 404", surface.name, status)
		}
	}

	reads := capture.byAction(actionAdminSessionRead)
	if len(reads) == 0 {
		t.Fatalf("跨用户读取未留痕，已记录 %+v", capture.actions())
	}
	payload := capture.payloadOf(t, actionAdminSessionRead)
	if payload["via"] != permission.ChatReadAll || payload["targetUserId"] != "u-owner" {
		t.Fatalf("留痕 payload 异常: %+v", payload)
	}
	if payload["operatorRole"] != "reader" {
		t.Fatalf("留痕未记录操作者角色: %+v", payload)
	}
	for _, e := range capture.byAction(actionAdminSessionRead) {
		if strings.Contains(e.Payload, "keep this out of audit") {
			t.Fatal("跨用户读取留痕混入会话正文")
		}
	}
}

// TestAdminCrossUserSessionOpsAreAudited 覆盖 U-06：admin 读取、续用、发消息、
// 审批、取消、改名、置顶、删除他人会话都写审计，且 payload 不含用户内容与审批回传值。
func TestAdminCrossUserSessionOpsAreAudited(t *testing.T) {
	const (
		secretQuery  = "delete prod namespace TOPSECRETQUERY"
		secretChoice = "sk-adminsecretchoice0000"
	)
	chat := newHeldRunChat()
	chat.sessions["s-owner"] = &session.Session{ID: "s-owner", UserID: "u-owner", Name: "owner session"}
	chat.sessions["s-run"] = &session.Session{ID: "s-run", UserID: "u-owner", Name: "run session"}
	srv, capture := ownerHarness(t, chat)
	sessions := srv.URL + "/api/v1/sessions"

	// 1. 读取他人历史与事件流。
	if status, frame := streamFrame(t, sessions+"/s-owner/stream", "tok-admin"); status != http.StatusOK || frame == nil {
		t.Fatalf("admin 读历史 = %d", status)
	}
	// 2. 经已存在 session ID 续用他人会话。
	if status, _ := call(t, http.MethodPost, sessions, "tok-admin", `{"id":"s-owner"}`); status != http.StatusOK {
		t.Fatalf("admin 续用他人会话 = %d, want 200", status)
	}
	// 3+4. 发消息后代他人审批（审批值带敏感串，验红线）。
	if status, _ := call(t, http.MethodPost, sessions+"/s-owner/send-message", "tok-admin", `{"query":"`+secretQuery+`"}`); status != http.StatusAccepted {
		t.Fatalf("admin 代发消息 = %d, want 202", status)
	}
	<-chat.started
	if status, _ := call(t, http.MethodPost, sessions+"/s-owner/approve", "tok-admin", `{"choice":"`+secretChoice+`"}`); status != http.StatusOK {
		t.Fatalf("admin 代审批 = %d, want 200", status)
	}
	<-chat.done
	// 5. 另起一轮运行并由 admin 取消。
	if status, _ := call(t, http.MethodPost, sessions+"/s-run/send-message", "tok-admin", `{"query":"run please"}`); status != http.StatusAccepted {
		t.Fatalf("admin 发起第二轮 = %d, want 202", status)
	}
	<-chat.started
	if status, _ := call(t, http.MethodPost, sessions+"/s-run/cancel", "tok-admin", `{}`); status != http.StatusOK {
		t.Fatalf("admin 取消运行 = %d, want 200", status)
	}
	<-chat.done
	// 6-8. 改名、置顶、删除。
	if status, _ := call(t, http.MethodPost, sessions+"/s-owner/rename", "tok-admin", `{"name":"admin renamed"}`); status != http.StatusOK {
		t.Fatalf("admin 改名 = %d, want 200", status)
	}
	if status, _ := call(t, http.MethodPost, sessions+"/s-owner/pin", "tok-admin", `{"pinned":true}`); status != http.StatusOK {
		t.Fatalf("admin 置顶 = %d, want 200", status)
	}
	if status, _ := call(t, http.MethodDelete, sessions+"/s-owner", "tok-admin", ""); status != http.StatusOK {
		t.Fatalf("admin 删除 = %d, want 200", status)
	}

	want := map[string]struct {
		eventType string
		session   string
	}{
		actionAdminSessionRead:    {store.AuditEventSession, "s-owner"},
		actionAdminSessionMessage: {store.AuditEventSession, "s-owner"},
		actionAdminSessionApprove: {store.AuditEventApproval, "s-owner"},
		actionAdminSessionCancel:  {store.AuditEventSession, "s-run"},
		actionAdminSessionRename:  {store.AuditEventSession, "s-owner"},
		actionAdminSessionPin:     {store.AuditEventSession, "s-owner"},
		actionAdminSessionDelete:  {store.AuditEventSession, "s-owner"},
	}
	for action, expect := range want {
		events := capture.byAction(action)
		if len(events) == 0 {
			t.Fatalf("缺少 %s 审计，已记录 %+v", action, capture.actions())
		}
		// 同一动作可能覆盖多个会话（发消息在两个会话上各跑一轮），
		// 只要目标会话有一条留痕即符合预期。
		var last *store.AuditEvent
		for i := range events {
			if events[i].SessionID == expect.session {
				last = &events[i]
			}
		}
		if last == nil {
			t.Fatalf("%s 未对目标会话 %s 留痕: %+v", action, expect.session, events)
		}
		if last.EventType != expect.eventType {
			t.Errorf("%s EventType = %q, want %q", action, last.EventType, expect.eventType)
		}
		if last.ResourceKind != "Session" || last.Status != "success" {
			t.Errorf("%s 审计字段异常: %+v", action, last)
		}
		if last.UserID != "u-admin" {
			t.Errorf("%s 操作者应为 admin，实得 %q", action, last.UserID)
		}
		payload := capture.payloadOf(t, action)
		if payload["via"] != permission.ChatManageAll || payload["targetUserId"] != "u-owner" {
			t.Errorf("%s payload 异常: %+v", action, payload)
		}
	}

	// 红线：审计 payload 里既没有对话正文，也没有审批回传值。
	for _, e := range capture.eventsSnapshot() {
		if strings.Contains(e.Payload, secretQuery) || strings.Contains(e.Payload, secretChoice) {
			t.Fatalf("审计 payload 含敏感内容: action=%s payload=%s", e.Action, e.Payload)
		}
	}

	chat.mu.Lock()
	defer chat.mu.Unlock()
	if _, ok := chat.sessions["s-owner"]; ok {
		t.Fatal("admin 删除未生效")
	}
}

// TestOwnerSelfOperationsAreNotAudited 确认留痕只针对跨用户操作：
// owner 对自己的会话做同样的事不产生 admin 审计。
func TestOwnerSelfOperationsAreNotAudited(t *testing.T) {
	chat := newOwnerChat()
	srv, capture := ownerHarness(t, chat)
	sessions := srv.URL + "/api/v1/sessions"

	if status, _ := call(t, http.MethodPost, sessions, "tok-owner", `{"id":"s-owner"}`); status != http.StatusOK {
		t.Fatalf("owner 续用自己会话 = %d", status)
	}
	if status, _ := streamFrame(t, sessions+"/s-owner/stream", "tok-owner"); status != http.StatusOK {
		t.Fatalf("owner 读自己历史 = %d", status)
	}
	if status, _ := call(t, http.MethodPost, sessions+"/s-owner/rename", "tok-owner", `{"name":"mine"}`); status != http.StatusOK {
		t.Fatalf("owner 改名 = %d", status)
	}
	if status, _ := call(t, http.MethodPost, sessions+"/s-owner/pin", "tok-owner", `{"pinned":true}`); status != http.StatusOK {
		t.Fatalf("owner 置顶 = %d", status)
	}
	if status, _ := call(t, http.MethodDelete, sessions+"/s-owner", "tok-owner", ""); status != http.StatusOK {
		t.Fatalf("owner 删除 = %d", status)
	}
	if got := capture.actions(); len(got) != 0 {
		t.Fatalf("owner 自用操作不应留痕，实得 %+v", got)
	}
}
