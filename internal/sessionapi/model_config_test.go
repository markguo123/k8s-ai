package sessionapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/settings"
)

// unconfiguredChat 模拟"模型没配好"的一轮对话：Chat 直接返回配置中心门禁错误。
type unconfiguredChat struct {
	*fakeChat
}

func (c *unconfiguredChat) Chat(context.Context, service.ChatRequest, <-chan string) (<-chan service.ChatEvent, error) {
	return nil, settings.ErrModelNotConfigured
}

// TestSendMessageModelNotConfiguredIsConflict 锁 P6.1 体验：模型未配置是配置态问题，
// 必须回 409 + 可读原因，让前端引导去配置中心，而不是 500 让用户以为后端坏了。
func TestSendMessageModelNotConfiguredIsConflict(t *testing.T) {
	fake := &unconfiguredChat{fakeChat: newFakeChat()}
	srv := httptest.NewServer(testHandler(New(fake, "m")))
	defer srv.Close()
	_, _ = fake.GetOrCreateSession("s1", "", "m")

	resp := postJSON(t, srv.URL+"/api/v1/sessions/s1/send-message", `{"query":"检查集群"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("模型未配置应 409, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析错误响应失败: %v", err)
	}
	if !strings.Contains(body["error"], "model is not configured") {
		t.Fatalf("应回报明确原因, got %q", body["error"])
	}
}

// TestSendMessageOtherErrorStaysInternalServerError 锁另一侧：其他错误仍是 500，
// 不能被 409 掩盖成配置问题。
func TestSendMessageOtherErrorStaysInternalServerError(t *testing.T) {
	fake := &failingChat{fakeChat: newFakeChat()}
	srv := httptest.NewServer(testHandler(New(fake, "m")))
	defer srv.Close()
	_, _ = fake.GetOrCreateSession("s1", "", "m")

	resp := postJSON(t, srv.URL+"/api/v1/sessions/s1/send-message", `{"query":"检查集群"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("其他 Chat 错误应 500, got %d", resp.StatusCode)
	}
}

type failingChat struct{ *fakeChat }

func (c *failingChat) Chat(context.Context, service.ChatRequest, <-chan string) (<-chan service.ChatEvent, error) {
	return nil, errors.New("reader unavailable")
}

// TestStreamDisablesProxyBuffering 锁 SSE 反代行为：响应必须带 X-Accel-Buffering: no，
// Nginx 配置遗漏时也不会被缓冲成一段一段。
func TestStreamDisablesProxyBuffering(t *testing.T) {
	fake := newFakeChat(service.ChatEvent{Type: service.ChatEventAnswer, Text: "完成"})
	srv := httptest.NewServer(testHandler(New(fake, "m")))
	defer srv.Close()
	_, _ = fake.GetOrCreateSession("s1", "", "m")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/sessions/s1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("SSE 响应应关闭代理缓冲, got %q", got)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("事件流 Content-Type 异常, got %q", got)
	}
}
