package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRecorderWritesRedactedJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	r, err := NewFileRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Write(context.Background(), Event{
		Action:  ActionToolCallRequest,
		Payload: map[string]any{"name": "get_pod", "token": "sk-top-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "sk-top-secret") {
		t.Fatalf("审计未脱敏: %s", s)
	}
	if !strings.Contains(s, "[REDACTED]") || !strings.Contains(s, ActionToolCallRequest) {
		t.Fatalf("审计事件异常: %s", s)
	}
	if !strings.Contains(s, "\n") {
		t.Fatal("应为 JSONL（每事件一行）")
	}
}

func TestFileRecorderAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	r, err := NewFileRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Write(context.Background(), Event{Action: ActionUserInput, Payload: "q1"})
	_ = r.Write(context.Background(), Event{Action: ActionLLMRequest, Payload: "q2"})
	_ = r.Close()
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("应为 2 行事件: %q", raw)
	}
}

func TestLogRecorder(t *testing.T) {
	r := &LogRecorder{}
	if err := r.Write(context.Background(), Event{Action: ActionPlanGenerated, Payload: "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
