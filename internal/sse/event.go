// Package sse 提供四期统一 SSE 事件协议（PRD §8）：
//   - 统一事件信封与事件类型枚举（snake_case JSON 契约）
//   - SSE 帧序列化 / 反序列化
//   - 与三期旧事件（answer-delta / reasoning-delta / tool-call / tool-result）
//     的映射层（D-08：迁移期内旧事件仍可消费，未完成映射不得移除）
//
// 安全约定：任何写入事件流的内容在封装前必须脱敏，Secret / kubeconfig / API Key
// 明文永不进入 SSE（AGENTS.md 红线，SSE 输出 params/result 前统一 redact）。
package sse

import "time"

// EventType 是统一 SSE 事件类型（PRD §8 全量枚举）。
type EventType string

const (
	EventSessionCreated     EventType = "session.created"
	EventTurnStarted        EventType = "turn.started"
	EventTurnCompleted      EventType = "turn.completed"
	EventReasonStarted      EventType = "reason.started"
	EventReasonCompleted    EventType = "reason.completed"
	EventToolStarted        EventType = "tool.started"
	EventToolCompleted      EventType = "tool.completed"
	EventObservationCreated EventType = "observation.created"
	EventApprovalRequired   EventType = "approval.required"
	EventApprovalApproved   EventType = "approval.approved"
	EventApprovalRejected   EventType = "approval.rejected"
	EventMessageDelta       EventType = "message.delta"
	EventMessageCompleted   EventType = "message.completed"
	EventSessionCompleted   EventType = "session.completed"
	EventSessionFailed      EventType = "session.failed"
)

// Event 是统一 SSE 事件信封。JSON 字段沿用 PRD §8 的 snake_case 契约，
// 每个事件至少携带 session_id / turn_id / step_id / timestamp / event_type。
// Data 为各事件专属载荷（对应类型见下方 Data 结构体，可为 nil）。
type Event struct {
	SessionID string    `json:"session_id"`
	TurnID    string    `json:"turn_id"`
	StepID    string    `json:"step_id"`
	Timestamp time.Time `json:"timestamp"`
	EventType EventType `json:"event_type"`
	Data      any       `json:"data"`
}

// New 构造一个带当前时间戳的统一事件；Data 为各事件专属载荷（可为 nil）。
func New(sessionID, turnID, stepID string, eventType EventType, data any) Event {
	return Event{
		SessionID: sessionID,
		TurnID:    turnID,
		StepID:    stepID,
		Timestamp: time.Now(),
		EventType: eventType,
		Data:      data,
	}
}

// StepKind 是 StepData 内的步骤语义（沿用 session.StepType，但不依赖该包）。
type StepKind string

const (
	StepKindReason   StepKind = "reason"
	StepKindTool     StepKind = "tool"
	StepKindObserve  StepKind = "observe"
	StepKindApproval StepKind = "approval"
	StepKindResponse StepKind = "response"
	StepKindTurn     StepKind = "turn"
	StepKindSession  StepKind = "session"
)

// Token 是统一事件中的 token 用量（snake_case）。
type Token struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// TurnData 是 turn.* / session.* 事件的载荷。
type TurnData struct {
	Status string `json:"status"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
	Token  *Token `json:"token,omitempty"`
}

// StepData 是 reason.* / tool.* 事件的载荷。Params/Result 在封装前必须脱敏。
type StepData struct {
	Kind   StepKind `json:"kind"`
	Status string   `json:"status"`
	Seq    int      `json:"seq,omitempty"`
	Tool   string   `json:"tool,omitempty"`
	Risk   string   `json:"risk,omitempty"`
	Params string   `json:"params,omitempty"`
	Result string   `json:"result,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// ObservationData 是 observation.created 事件的载荷（工具结果观察，脱敏后摘要）。
type ObservationData struct {
	Tool   string `json:"tool,omitempty"`
	Result string `json:"result,omitempty"`
	Ok     *bool  `json:"ok,omitempty"`
}

// ChoiceOption 是审批可选项。
type ChoiceOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ApprovalData 是 approval.* 事件的载荷。Secret 为 true 时前端以密码框取值，
// 值永不回显、不入事件流。
type ApprovalData struct {
	Prompt   string         `json:"prompt,omitempty"`
	Options  []ChoiceOption `json:"options,omitempty"`
	Secret   bool           `json:"secret,omitempty"`
	Decision string         `json:"decision,omitempty"` // approved | rejected
	Resource map[string]any `json:"resource,omitempty"`
}

// MessageData 是 message.delta / message.completed 事件的载荷。
// Role 取值：assistant（回答流）/ reasoning（思考流）/ user（用户输入回显）。
type MessageData struct {
	Role  string `json:"role,omitempty"`
	Delta string `json:"delta,omitempty"`
	Index int    `json:"index,omitempty"`
}

// SessionData 是 session 级附加信息（状态/终止原因）。
type SessionData struct {
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// 编译期保证各 Data 结构体可作 JSON 序列化载荷。
var (
	_ = Token{}
	_ = TurnData{}
	_ = StepData{}
	_ = ObservationData{}
	_ = ApprovalData{}
	_ = MessageData{}
	_ = SessionData{}
)
