package sessionapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// tokenChat 在 fakeChat 之上实现 P2.13 的可选能力 SessionTokenizer。
type tokenChat struct {
	*fakeChat
	usage session.TokenUsage

	// err 非空时模拟读取失败（例如数据库不可用）。
	err error
}

func (c *tokenChat) SessionTokenUsage(context.Context, string) (session.TokenUsage, error) {
	if c.err != nil {
		return session.TokenUsage{}, c.err
	}
	return c.usage, nil
}

// TestSessionTokenInHistoryAndList 覆盖 P2.13 的读取路径：
// 会话级 Token 用量随历史快照与会话列表下发，刷新/重开会话后前端仍可展示。
func TestSessionTokenInHistoryAndList(t *testing.T) {
	chat := &tokenChat{
		fakeChat: newFakeChat(),
		usage:    session.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
	srv := httptest.NewServer(testHandler(New(chat, "qwen-plus")))
	defer srv.Close()
	_, _ = chat.GetOrCreateSession("tok-s1", "", "qwen-plus")

	history, err := readHistory(srv.URL + "/api/v1/sessions/tok-s1/stream")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if !strings.Contains(history, `"token"`) || !strings.Contains(history, `"totalTokens":18`) {
		t.Fatalf("历史快照未携带会话级 token 用量: %s", history)
	}
	if strings.Contains(history, "prompt_tokens") {
		t.Fatalf("历史快照 token 字段应为 session.TokenUsage 契约(camelCase): %s", history)
	}

	resp := getBody(t, srv.URL+"/api/v1/sessions")
	var list []map[string]any
	if err := json.Unmarshal(resp, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, resp)
	}
	if len(list) != 1 {
		t.Fatalf("expect 1 session, got %d", len(list))
	}
	token, ok := list[0]["token"].(map[string]any)
	if !ok {
		t.Fatalf("列表项缺少 token 字段: %+v", list[0])
	}
	if token["totalTokens"].(float64) != 18 {
		t.Fatalf("列表 token = %+v", token)
	}
}

// TestSessionTokenOmitsZeroAndErrors 覆盖退化路径：无 Token 记录或读取失败时
// 不伪造 0 用量，也不让列表/快照请求失败。
func TestSessionTokenOmitsZeroAndErrors(t *testing.T) {
	cases := []struct {
		name  string
		usage session.TokenUsage
		err   error
	}{
		{name: "zero usage", usage: session.TokenUsage{}},
		{name: "provider error", err: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := &tokenChat{fakeChat: newFakeChat(), usage: tc.usage, err: tc.err}
			srv := httptest.NewServer(testHandler(New(chat, "qwen-plus")))
			defer srv.Close()
			_, _ = chat.GetOrCreateSession("tok-s2", "", "qwen-plus")

			history, err := readHistory(srv.URL + "/api/v1/sessions/tok-s2/stream")
			if err != nil {
				t.Fatalf("read history: %v", err)
			}
			if !strings.Contains(history, `"token":null`) {
				t.Fatalf("无用量时应显式 null 让前端隐藏统计: %s", history)
			}
			resp := getBody(t, srv.URL+"/api/v1/sessions")
			if strings.Contains(string(resp), `"token"`) {
				t.Fatalf("无用量时列表项不应带 token 字段: %s", resp)
			}
		})
	}
}

// TestSessionTokenAbsentForLegacyService 覆盖三期服务：未实现可选接口时字段缺省，行为不变。
func TestSessionTokenAbsentForLegacyService(t *testing.T) {
	chat := newFakeChat()
	srv := httptest.NewServer(testHandler(New(chat, "qwen-plus")))
	defer srv.Close()
	_, _ = chat.GetOrCreateSession("legacy-s1", "", "qwen-plus")

	history, err := readHistory(srv.URL + "/api/v1/sessions/legacy-s1/stream")
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if !strings.Contains(history, `"token":null`) {
		t.Fatalf("三期服务历史快照应为 token:null: %s", history)
	}
}

func getBody(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s status = %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return body
}
