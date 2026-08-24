// Package agent 实现二期 Agent 核心：消息总线、状态机、Agentic Loop
// （Loop 在 P2 接入，P0 先落消息契约与状态定义）。
// 设计逻辑借鉴 kubectl-ai 的消息模型，但类型与语义按 k8s-ai 重新实现。
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Source 是消息来源。
type Source string

const (
	SourceUser   Source = "user"
	SourceAgent  Source = "agent"
	SourceModel  Source = "model"
	SourceSystem Source = "system"
)

// MessageType 是 Agent 与 UI（CLI/Web）之间的消息类型。
type MessageType string

const (
	MessageTypeText                 MessageType = "text"
	MessageTypeError                MessageType = "error"
	MessageTypeToolCallRequest      MessageType = "tool-call-request"
	MessageTypeToolCallResponse     MessageType = "tool-call-response"
	MessageTypeUserInputRequest     MessageType = "user-input-request"
	MessageTypeUserInputResponse    MessageType = "user-input-response"
	MessageTypeUserChoiceRequest    MessageType = "user-choice-request"
	MessageTypeUserChoiceResponse   MessageType = "user-choice-response"
	MessageTypeSessionPickerRequest MessageType = "session-picker-request"
	MessageTypeAgentState           MessageType = "agent-state"
)

// AgentState 是 Agent 对 UI 可见的状态。
type AgentState string

const (
	AgentStateIdle            AgentState = "idle"
	AgentStateInitializing    AgentState = "initializing"
	AgentStateRunning         AgentState = "running"
	AgentStateWaitingForInput AgentState = "waiting-for-input"
	AgentStateDone            AgentState = "done"
	AgentStateExited          AgentState = "exited"
)

// Message 是消息总线上的基本单元。
type Message struct {
	ID        string
	Source    Source
	Type      MessageType
	Payload   any
	Timestamp time.Time
}

// ToolCallEvent 供终端/Web 展示 [Tool] 调用行。
type ToolCallEvent struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

// UserChoiceOption 是审批/选择的可选项。
type UserChoiceOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// 审批选项值（用户 y / a / n）。
const (
	ChoiceYes           = "yes"
	ChoiceYesNoAskAgain = "yes_and_dont_ask_again"
	ChoiceNo            = "no"
)

// UserChoiceRequest 是 Agent 向 UI 发出的审批/选择请求。
type UserChoiceRequest struct {
	Prompt  string             `json:"prompt"`
	Options []UserChoiceOption `json:"options"`
}

// UserChoiceResponse 是 UI 回传的用户选择（下标对应 Options）。
type UserChoiceResponse struct {
	Choice int `json:"choice"`
}

// newID 生成本机随机 ID（消息/会话用）。
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405")))
	}
	return hex.EncodeToString(b)
}
