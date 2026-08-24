// Package audit 实现事件式审计（设计逻辑借鉴 kubectl-ai 的 journal Recorder，
// 按 k8s-ai 重新实现：JSONL 落盘、写入前脱敏）。
package audit

import "time"

// Event 是一条审计事件。
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Payload   any       `json:"payload,omitempty"`
}

// 审计动作常量（需求规格 §19.1）。
const (
	ActionSessionCreated        = "session.created"
	ActionSessionResumed        = "session.resumed"
	ActionSessionClosed         = "session.closed"
	ActionUserInput             = "user.input"
	ActionLLMRequest            = "llm.request"
	ActionLLMResponse           = "llm.response"
	ActionLLMError              = "llm.error"
	ActionToolCallRequest       = "tool-call.request"
	ActionToolCallResponse      = "tool-call.response"
	ActionToolError             = "tool.error"
	ActionPlanGenerated         = "plan.generated"
	ActionApprovalRequested     = "approval.requested"
	ActionApprovalGranted       = "approval.granted"
	ActionApprovalDenied        = "approval.denied"
	ActionApprovalSkipped       = "approval.skipped"
	ActionExecutionStarted      = "execution.started"
	ActionExecutionSucceeded    = "execution.succeeded"
	ActionExecutionFailed       = "execution.failed"
	ActionVerificationStarted   = "verification.started"
	ActionVerificationSucceeded = "verification.succeeded"
	ActionVerificationFailed    = "verification.failed"
)
