package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// collectChatEvents 跑一轮对话并把事件收进切片（runChatTurn 只丢事件，容量用例要看文案）。
func collectChatEvents(t *testing.T, svc ChatService, sessionID, query string) []ChatEvent {
	t.Helper()
	respond := make(chan string, 1)
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: sessionID, Query: query}, respond)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	var out []ChatEvent
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

// TestSessionCapacityNotices 锁文案判定的三条分流：超限只报 failure、达比例只报 warning、
// 非容量错误一个字都不对用户提（只进日志）。第三条最要紧——数据库抖动被说成「会话太大」，
// 用户会去新建会话，而真正该修的是库连接。
func TestSessionCapacityNotices(t *testing.T) {
	tooLarge := errors.Join(store.ErrSessionTooLarge, errors.New("超限"))

	cases := []struct {
		name         string
		rep          session.SizeReport
		err          error
		wantWarning  bool
		wantFailure  bool
		wantMentions []string
	}{
		{
			name:        "正常写入",
			rep:         session.SizeReport{Size: 1 << 20, Limit: 12 << 20},
			wantWarning: false, wantFailure: false,
		},
		{
			name:        "接近上限",
			rep:         session.SizeReport{Size: 11 << 20, Limit: 12 << 20, Warning: true},
			wantWarning: true, wantFailure: false,
			wantMentions: []string{"11 MiB", "12 MiB", "新开会话"},
		},
		{
			name:        "超限未保存",
			rep:         session.SizeReport{Size: 13 << 20, Limit: 12 << 20},
			err:         tooLarge,
			wantWarning: false, wantFailure: true,
			wantMentions: []string{"本轮回复没有存进会话", "agent.session_max_bytes", "13 MiB", "12 MiB"},
		},
		{
			name:        "非容量错误不外报",
			rep:         session.SizeReport{Size: 10, Limit: 12 << 20},
			err:         errors.New("connection reset by peer"),
			wantWarning: false, wantFailure: false,
		},
	}
	for _, tc := range cases {
		warning, failure := sessionCapacityNotices(tc.rep, tc.err, "sess-x")
		if (warning != "") != tc.wantWarning {
			t.Errorf("%s: warning = %q", tc.name, warning)
		}
		if (failure != "") != tc.wantFailure {
			t.Errorf("%s: failure = %q", tc.name, failure)
		}
		for _, want := range tc.wantMentions {
			if !strings.Contains(warning+failure, want) {
				t.Errorf("%s: 文案缺少 %q\nwarning=%q\nfailure=%q", tc.name, want, warning, failure)
			}
		}
	}
}

// TestChatTurnOverLimitStillAnswersAndTellsUser 是 P8.6 对用户的承诺：正文超上限时本轮回复
// 必须照常答完（用户已经等了几十秒），只是存不下，并且要出一条说得清原因和下一步的提示；
// 库里那一行保持上一轮的样子，绝不出现「半截正文」。
func TestChatTurnOverLimitStillAnswersAndTellsUser(t *testing.T) {
	const limit = 512
	长答 := strings.Repeat("排", 900) // 约 2.7KB，稳超 512 字节上限
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{Content: 长答, Usage: llm.TokenUsage{TotalTokens: 9}},
	}}
	f := newPlatformChatFixture(t, fl, func(cfg *config.Config) { cfg.Agent.SessionMaxBytes = limit })

	sess, err := f.svc.GetOrCreateSessionForUser("cap-1", "u1", "default", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	events := collectChatEvents(t, f.svc, sess.ID, "把 web 的排查过程完整写一遍")

	var answered, notice string
	for _, ev := range events {
		switch ev.Type {
		case ChatEventAnswer:
			if ev.Text == 长答 {
				answered = ev.Text
			}
		case ChatEventError:
			if strings.Contains(ev.Text, "agent.session_max_bytes") {
				notice = ev.Text
			}
		}
	}
	if answered == "" {
		t.Fatal("超限不该影响本轮回复送达")
	}
	if notice == "" {
		t.Fatalf("超限缺少用户可见提示，events=%+v", events)
	}
	for _, want := range []string{"本轮回复没有存进会话", "新建会话"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("提示缺少 %q: %s", want, notice)
		}
	}

	saved, err := f.st.Sessions().Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, m := range saved.Messages {
		if strings.Contains(m.Content, 长答) {
			t.Fatal("超限正文被写进了库")
		}
	}
}
