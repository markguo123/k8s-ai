package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestChatSendsTools(t *testing.T) {
	var gotBody string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse("ok")))
	})
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
		Tools: []ToolSpec{
			{Name: "get_pod", Description: "获取 Pod", Parameters: map[string]any{"type": "object"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"tools"`,
		`"type":"function"`,
		`"function":{"name":"get_pod"`,
		`"description":"获取 Pod"`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("请求体缺少 %s: %s", want, gotBody)
		}
	}
}

func TestChatNoToolsWhenEmpty(t *testing.T) {
	var gotBody string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okResponse("ok")))
	})
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, `"tools"`) {
		t.Fatalf("未传 Tools 时请求体不应带 tools 字段: %s", gotBody)
	}
}

func TestChatParsesToolCalls(t *testing.T) {
	body := `{"choices":[{"message":{"content":"","tool_calls":[` +
		`{"id":"call_1","type":"function","function":{"name":"get_pod","arguments":"{\"namespace\":\"production\",\"name\":\"payment-api\"}"}}` +
		`]}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
		Tools:    []ToolSpec{{Name: "get_pod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_pod" {
		t.Fatalf("tool call = %+v", tc)
	}
	var args map[string]string
	if err := json.Unmarshal(tc.Arguments, &args); err != nil {
		t.Fatalf("arguments 不是合法 JSON: %v", err)
	}
	if args["namespace"] != "production" || args["name"] != "payment-api" {
		t.Fatalf("arguments = %v", args)
	}
}

func TestChatEmptyContentWithoutToolCalls(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}],"usage":{}}`))
	})
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}); err == nil {
		t.Fatal("content 与 tool_calls 均为空时应报错")
	}
}

func TestToolRoleMessageMarshalsToolCallID(t *testing.T) {
	raw, err := json.Marshal(Message{Role: "tool", ToolCallID: "call_1", Content: `{"phase":"Running"}`})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"tool_call_id":"call_1"`) || !strings.Contains(s, `"role":"tool"`) {
		t.Fatalf("tool 消息缺少 tool_call_id/role: %s", s)
	}
}

func TestToolSpecsToJSON(t *testing.T) {
	s, err := ToolSpecsToJSON([]ToolSpec{{Name: "get_pod", Description: "d"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, `"name":"get_pod"`) {
		t.Fatalf("shim 工具 JSON = %s", s)
	}
}

func TestToolCallMarshalsWireFormat(t *testing.T) {
	raw, err := json.Marshal(ToolCall{
		ID:        "call_1",
		Name:      "get_resources",
		Arguments: json.RawMessage(`{"kind":"namespaces"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"id":"call_1"`,
		`"type":"function"`,
		`"function":{"name":"get_resources","arguments":"{\"kind\":\"namespaces\"}"}`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("tool_call wire 缺少 %s: %s", want, s)
		}
	}
}
