package llm

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseChatStreamSeparatesReasoningAndContent(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"先检查 Pod"}}]}`,
		`data: {"choices":[{"delta":{"content":"最终结论"}}]}`,
		"data: [DONE]",
	}, "\n")
	var events []StreamEvent
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	result, err := parseChatStream(resp, func(event StreamEvent) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "最终结论" {
		t.Fatalf("content = %q", result.Content)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Type != StreamReasoningDelta || events[0].Text != "先检查 Pod" {
		t.Fatalf("reasoning event = %+v", events[0])
	}
	if events[1].Type != StreamContentDelta || events[1].Text != "最终结论" {
		t.Fatalf("content event = %+v", events[1])
	}
}
