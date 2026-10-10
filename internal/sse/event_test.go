package sse

import (
	"encoding/json"
	"testing"
)

// TestEventRequiredFields 校验所有事件类型都携带 PRD §8 必带字段。
func TestEventRequiredFields(t *testing.T) {
	types := []EventType{
		EventSessionCreated, EventTurnStarted, EventTurnCompleted,
		EventReasonStarted, EventReasonCompleted,
		EventToolStarted, EventToolCompleted, EventObservationCreated,
		EventApprovalRequired, EventApprovalApproved, EventApprovalRejected,
		EventMessageDelta, EventMessageCompleted,
		EventSessionCompleted, EventSessionFailed,
	}
	for _, typ := range types {
		e := New("s1", "t1", "st1", typ, nil)
		if e.Timestamp.IsZero() {
			t.Errorf("%s: timestamp 为零", typ)
		}
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("%s: marshal: %v", typ, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", typ, err)
		}
		for _, k := range []string{"session_id", "turn_id", "step_id", "timestamp", "event_type"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: 缺少必带字段 %q (raw=%s)", typ, k, raw)
			}
		}
		if m["event_type"] != string(typ) {
			t.Errorf("%s: event_type=%v 不匹配", typ, m["event_type"])
		}
	}
}

func TestEventSnakeCaseMarshal(t *testing.T) {
	e := New("s1", "t1", "st1", EventMessageDelta, MessageData{Role: "assistant", Delta: "hi", Index: 1})
	raw, err := Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["sessionId"]; ok {
		t.Errorf("不应出现 camelCase 字段 sessionId")
	}
	data, ok := m["data"].(map[string]any)
	if !ok {
		t.Fatalf("data 载荷缺失: %v", m)
	}
	if data["promptTokens"] != nil {
		t.Errorf("data 不应使用 camelCase 载荷键")
	}
}
