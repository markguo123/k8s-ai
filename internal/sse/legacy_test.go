package sse

import "testing"

func boolPtr(b bool) *bool { return &b }

// TestMapFromLegacyTodo 走查三期旧事件 → 统一事件映射覆盖。
func TestMapFromLegacy(t *testing.T) {
	cases := []struct {
		name string
		in   LegacyEvent
		want EventType
		ok   bool
	}{
		{
			name: "answer-delta → message.delta(assistant)",
			in:   LegacyEvent{Type: LegacyTypeAnswerDelta, Text: "hi"},
			want: EventMessageDelta, ok: true,
		},
		{
			name: "reasoning-delta → message.delta(reasoning)",
			in:   LegacyEvent{Type: LegacyTypeReasoningDelta, Text: "think"},
			want: EventMessageDelta, ok: true,
		},
		{
			name: "text → message.delta(user)",
			in:   LegacyEvent{Type: LegacyTypeText, Text: "echo"},
			want: EventMessageDelta, ok: true,
		},
		{
			name: "tool-call → tool.started",
			in: LegacyEvent{Type: LegacyTypeToolCall, Tool: &LegacyToolView{
				Name: "get_resources", Arguments: map[string]any{"kind": "nodes"}, Risk: "low",
			}},
			want: EventToolStarted, ok: true,
		},
		{
			name: "tool-response → observation.created",
			in: LegacyEvent{Type: LegacyTypeToolResponse, Tool: &LegacyToolView{
				Name: "get_resources", Result: "ok", Ok: boolPtr(true),
			}},
			want: EventObservationCreated, ok: true,
		},
		{
			name: "choice-request → approval.required",
			in: LegacyEvent{Type: LegacyTypeChoice, Choice: &LegacyChoice{
				Prompt: "确认?", Options: []LegacyChoiceOption{{Label: "是", Value: "yes"}},
			}},
			want: EventApprovalRequired, ok: true,
		},
		{
			name: "progress 无对应事件",
			in:   LegacyEvent{Type: LegacyTypeProgress, Text: "p"},
			ok:   false,
		},
		{
			name: "state 无对应事件",
			in:   LegacyEvent{Type: LegacyTypeState, State: "running"},
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MapFromLegacy(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.EventType != tc.want {
				t.Errorf("event_type=%q want %q", got.EventType, tc.want)
			}
		})
	}
}

func TestMapFromLegacyRole(t *testing.T) {
	got, ok := MapFromLegacy(LegacyEvent{Type: LegacyTypeReasoningDelta, Text: "x"})
	if !ok {
		t.Fatal("mapping failed")
	}
	if d := messageDataOf(got.Data); d.Role != "reasoning" || d.Delta != "x" {
		t.Errorf("reasoning 映射错误: %+v", d)
	}
	got, _ = MapFromLegacy(LegacyEvent{Type: LegacyTypeToolCall, Tool: &LegacyToolView{
		Name: "kubectl", Arguments: map[string]any{"command": "get pods"},
	}})
	if d := stepDataOf(got.Data); d.Kind != StepKindTool || d.Tool != "kubectl" || d.Params == "" {
		t.Errorf("tool 映射错误: %+v", d)
	}
}

// TestMapToLegacy 走查统一事件 → 三期旧事件覆盖。
func TestMapToLegacy(t *testing.T) {
	cases := []struct {
		name string
		in   Event
		want string
		ok   bool
	}{
		{
			name: "tool.started → tool-call",
			in: New("", "", "", EventToolStarted, StepData{
				Kind: StepKindTool, Status: "running", Tool: "get_resources", Risk: "medium",
			}),
			want: LegacyTypeToolCall, ok: true,
		},
		{
			name: "observation.created → tool-response",
			in: New("", "", "", EventObservationCreated, ObservationData{
				Tool: "get_resources", Result: "ok", Ok: boolPtr(true),
			}),
			want: LegacyTypeToolResponse, ok: true,
		},
		{
			name: "approval.required → choice-request",
			in: New("", "", "", EventApprovalRequired, ApprovalData{
				Prompt: "确认?", Options: []ChoiceOption{{Label: "是", Value: "yes"}},
			}),
			want: LegacyTypeChoice, ok: true,
		},
		{
			name: "message.delta(reasoning) → reasoning-delta",
			in:   New("", "", "", EventMessageDelta, MessageData{Role: "reasoning", Delta: "t"}),
			want: LegacyTypeReasoningDelta, ok: true,
		},
		{
			name: "message.delta(assistant) → answer-delta",
			in:   New("", "", "", EventMessageDelta, MessageData{Role: "assistant", Delta: "a"}),
			want: LegacyTypeAnswerDelta, ok: true,
		},
		{
			name: "message.delta(user) → text",
			in:   New("", "", "", EventMessageDelta, MessageData{Role: "user", Delta: "u"}),
			want: LegacyTypeText, ok: true,
		},
		{
			name: "session.created 无旧事件",
			in:   New("", "", "", EventSessionCreated, SessionData{State: "idle"}),
			ok:   false,
		},
		{
			name: "approval.approved 无旧事件",
			in:   New("", "", "", EventApprovalApproved, ApprovalData{Decision: "approved"}),
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MapToLegacy(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got.Type != tc.want {
				t.Errorf("type=%q want %q", got.Type, tc.want)
			}
			if got.Tool != nil && tc.want == LegacyTypeToolCall && got.Tool.Risk != "medium" {
				t.Errorf("risk 未保留: %q", got.Tool.Risk)
			}
		})
	}
}
