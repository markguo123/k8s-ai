package sse

import "encoding/json"

// 三期 WebChat 旧事件类型（兼容层常量，见 internal/service.ChatEventType）。
const (
	LegacyTypeState          = "state"
	LegacyTypeText           = "text"
	LegacyTypeDelta          = "delta"
	LegacyTypeReasoningDelta = "reasoning-delta"
	LegacyTypeProgress       = "progress"
	LegacyTypeAnswerDelta    = "answer-delta"
	LegacyTypeToolCall       = "tool-call"
	LegacyTypeToolResponse   = "tool-response"
	LegacyTypeAnswer         = "answer"
	LegacyTypeError          = "error"
	LegacyTypeChoice         = "choice-request"
)

// LegacyUsage 是三期 Token 用量线格式（camelCase，与 service.ChatEvent 载荷同构）。
type LegacyUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

// LegacyToolView 是三期工具卡视图（camelCase，与 service.ChatToolCallView 同构）。
type LegacyToolView struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Risk      string         `json:"risk,omitempty"`
	Result    string         `json:"result,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	Ok        *bool          `json:"ok,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Data      any            `json:"data,omitempty"`
}

// LegacyChoiceOption 是三期审批可选项。
type LegacyChoiceOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// LegacyChoice 是三期审批请求视图。
type LegacyChoice struct {
	Prompt  string               `json:"prompt"`
	Options []LegacyChoiceOption `json:"options"`
	Secret  bool                 `json:"secret,omitempty"`
}

// LegacyEvent 是三期旧事件线格式视图，负责与统一事件的互译（D-08 映射层）。
type LegacyEvent struct {
	Type   string          `json:"type"`
	Text   string          `json:"text,omitempty"`
	Tool   *LegacyToolView `json:"tool,omitempty"`
	Choice *LegacyChoice   `json:"choice,omitempty"`
	State  string          `json:"state,omitempty"`
	Usage  *LegacyUsage    `json:"usage,omitempty"`
}

// MapFromLegacy 把三期旧事件译成统一事件。旧流不携带 turn/step id，
// 返回事件的 session_id/turn_id/step_id 为空，消费方按上下文补全（P2.5 归一）。
func MapFromLegacy(l LegacyEvent) (Event, bool) {
	switch l.Type {
	case LegacyTypeReasoningDelta:
		return New("", "", "", EventMessageDelta, MessageData{Role: "reasoning", Delta: l.Text}), true
	case LegacyTypeAnswerDelta, LegacyTypeAnswer:
		return New("", "", "", EventMessageDelta, MessageData{Role: "assistant", Delta: l.Text}), true
	case LegacyTypeText:
		return New("", "", "", EventMessageDelta, MessageData{Role: "user", Delta: l.Text}), true
	case LegacyTypeToolCall:
		if l.Tool == nil {
			return Event{}, false
		}
		params := ""
		if raw, err := json.Marshal(l.Tool.Arguments); err == nil {
			params = string(raw)
		}
		return New("", "", "", EventToolStarted, StepData{
			Kind: StepKindTool, Status: "running", Tool: l.Tool.Name, Params: params,
		}), true
	case LegacyTypeToolResponse:
		if l.Tool == nil {
			return Event{}, false
		}
		return New("", "", "", EventObservationCreated, ObservationData{
			Tool: l.Tool.Name, Result: l.Tool.Result, Ok: l.Tool.Ok,
		}), true
	case LegacyTypeChoice:
		if l.Choice == nil {
			return Event{}, false
		}
		opts := make([]ChoiceOption, 0, len(l.Choice.Options))
		for _, o := range l.Choice.Options {
			opts = append(opts, ChoiceOption{Label: o.Label, Value: o.Value})
		}
		return New("", "", "", EventApprovalRequired, ApprovalData{
			Prompt: l.Choice.Prompt, Options: opts, Secret: l.Choice.Secret,
		}), true
	default:
		// state/progress/delta 等为阶段内附信息，无一一对应统一事件（保持旧流继续下发）。
		return Event{}, false
	}
}

// MapToLegacy 把统一事件降级为三期旧事件，供迁移期旧客户端继续消费。
// 无对应旧事件（如 session.created / turn.* / approval 终态）返回 ok=false，
// 调用方应保持统一事件流原样转发，不丢弃旧流。
func MapToLegacy(e Event) (LegacyEvent, bool) {
	switch e.EventType {
	case EventReasonStarted:
		// 思考过程以 message.delta(role=reasoning) 流式交付，起止事件无旧事件承载。
		return LegacyEvent{}, false
	case EventToolStarted:
		d := stepDataOf(e.Data)
		risk := ""
		if d.Risk != "" {
			risk = d.Risk
		}
		return LegacyEvent{Type: LegacyTypeToolCall, Tool: &LegacyToolView{
			Name: d.Tool, Arguments: parseArgs(d.Params), Risk: risk,
		}}, true
	case EventObservationCreated:
		d := observationDataOf(e.Data)
		ok := d.Ok
		return LegacyEvent{Type: LegacyTypeToolResponse, Tool: &LegacyToolView{
			Name: d.Tool, Result: d.Result, Summary: d.Result, Ok: ok,
		}}, true
	case EventApprovalRequired:
		d := approvalDataOf(e.Data)
		opts := make([]LegacyChoiceOption, 0, len(d.Options))
		for _, o := range d.Options {
			opts = append(opts, LegacyChoiceOption{Label: o.Label, Value: o.Value})
		}
		return LegacyEvent{Type: LegacyTypeChoice, Choice: &LegacyChoice{
			Prompt: d.Prompt, Options: opts, Secret: d.Secret,
		}}, true
	case EventMessageDelta:
		d := messageDataOf(e.Data)
		switch d.Role {
		case "reasoning":
			return LegacyEvent{Type: LegacyTypeReasoningDelta, Text: d.Delta}, true
		case "user":
			return LegacyEvent{Type: LegacyTypeText, Text: d.Delta}, true
		default:
			return LegacyEvent{Type: LegacyTypeAnswerDelta, Text: d.Delta}, true
		}
	default:
		return LegacyEvent{}, false
	}
}

func parseArgs(params string) map[string]any {
	var m map[string]any
	if params == "" {
		return m
	}
	_ = json.Unmarshal([]byte(params), &m)
	return m
}

func stepDataOf(v any) StepData {
	if d, ok := v.(StepData); ok {
		return d
	}
	if m, ok := v.(map[string]any); ok {
		d := StepData{}
		if s, ok := m["kind"].(string); ok {
			d.Kind = StepKind(s)
		}
		if s, ok := m["status"].(string); ok {
			d.Status = s
		}
		if f, ok := m["seq"].(float64); ok {
			d.Seq = int(f)
		}
		if s, ok := m["tool"].(string); ok {
			d.Tool = s
		}
		if s, ok := m["risk"].(string); ok {
			d.Risk = s
		}
		if s, ok := m["params"].(string); ok {
			d.Params = s
		}
		if s, ok := m["result"].(string); ok {
			d.Result = s
		}
		if s, ok := m["error"].(string); ok {
			d.Error = s
		}
		return d
	}
	return StepData{}
}

func observationDataOf(v any) ObservationData {
	if d, ok := v.(ObservationData); ok {
		return d
	}
	if m, ok := v.(map[string]any); ok {
		d := ObservationData{}
		if s, ok := m["tool"].(string); ok {
			d.Tool = s
		}
		if s, ok := m["result"].(string); ok {
			d.Result = s
		}
		if b, ok := m["ok"].(bool); ok {
			d.Ok = &b
		}
		return d
	}
	return ObservationData{}
}

func approvalDataOf(v any) ApprovalData {
	if d, ok := v.(ApprovalData); ok {
		return d
	}
	if m, ok := v.(map[string]any); ok {
		d := ApprovalData{}
		if s, ok := m["prompt"].(string); ok {
			d.Prompt = s
		}
		if b, ok := m["secret"].(bool); ok {
			d.Secret = b
		}
		if s, ok := m["decision"].(string); ok {
			d.Decision = s
		}
		if arr, ok := m["options"].([]any); ok {
			for _, item := range arr {
				if om, ok := item.(map[string]any); ok {
					o := ChoiceOption{}
					if s, ok := om["label"].(string); ok {
						o.Label = s
					}
					if s, ok := om["value"].(string); ok {
						o.Value = s
					}
					d.Options = append(d.Options, o)
				}
			}
		}
		return d
	}
	return ApprovalData{}
}

func messageDataOf(v any) MessageData {
	if d, ok := v.(MessageData); ok {
		return d
	}
	if m, ok := v.(map[string]any); ok {
		d := MessageData{}
		if s, ok := m["role"].(string); ok {
			d.Role = s
		}
		if s, ok := m["delta"].(string); ok {
			d.Delta = s
		}
		if f, ok := m["index"].(float64); ok {
			d.Index = int(f)
		}
		return d
	}
	return MessageData{}
}
