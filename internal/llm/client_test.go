package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient 创建指向测试服务器的客户端。
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(Options{
		Endpoint:    srv.URL + "/v1",
		APIKey:      "sk-test-secret-key",
		Model:       "qwen-plus",
		Temperature: 0.1,
		MaxTokens:   1024,
		Timeout:     2 * time.Second,
		MaxRetries:  2,
	})
	c.retryBase = time.Millisecond
	return c, srv
}

func okResponse(content string) string {
	return `{"choices":[{"message":{"content":` + jsonContent(content) + `}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
}

func jsonContent(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestChatSuccess(t *testing.T) {
	var gotBody string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-secret-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		if strings.Contains(gotBody, "sk-test-secret-key") {
			t.Error("api_key 不应出现在请求体中")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse("根因：OOMKilled")))
	})
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "help"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "根因：OOMKilled" {
		t.Fatalf("content = %q", resp.Content)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if !strings.Contains(gotBody, `"model":"qwen-plus"`) || !strings.Contains(gotBody, `"temperature":0.1`) {
		t.Fatalf("请求体缺少参数: %s", gotBody)
	}
}

func TestChatRetryOn429(t *testing.T) {
	var attempts atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse("ok")))
	})
	if _, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestChatRetryOn500(t *testing.T) {
	var attempts atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse("recovered")))
	})
	resp, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "recovered" || attempts.Load() != 3 {
		t.Fatalf("resp=%+v attempts=%d", resp, attempts.Load())
	}
}

func TestChatNoRetryOn400(t *testing.T) {
	var attempts atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	})
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("400 应返回错误")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1（4xx 不重试）", attempts.Load())
	}
}

func TestChatTimeout(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(okResponse("late")))
	})
	c.timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("超时应返回错误")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("超时处理过慢")
	}
}

func TestChatMalformedJSON(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	if _, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("坏 JSON 应返回错误")
	}
}

func TestChatEmptyChoices(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("空 choices 应返回错误")
	}
}

// TestAPIKeyNotLeaked 验证 5xx 错误体回显 api_key 时，错误消息已脱敏。
func TestAPIKeyNotLeaked(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"auth failed for sk-test-secret-key"}`))
	})
	_, err := c.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if strings.Contains(err.Error(), "sk-test-secret-key") {
		t.Fatalf("错误消息泄露 api_key: %v", err)
	}
}

func TestChatContextCancel(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okResponse("x")))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Chat(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("ctx 取消应返回错误")
	}
}

// TestChatDisableThinking 验证 DisableThinking 会发送 chat_template_kwargs。
func TestChatDisableThinking(t *testing.T) {
	var gotBody string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse("ok")))
	})
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages:        []Message{{Role: "user", Content: "x"}},
		DisableThinking: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"chat_template_kwargs":{"enable_thinking":false}`) {
		t.Fatalf("缺少 chat_template_kwargs: %s", gotBody)
	}
}
