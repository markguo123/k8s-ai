package service

import (
	"encoding/json"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// AuditScope 是一次 Agent 运行的关联上下文（四期 P1.11）：审计落库时补全
// session/turn/step 与用户/集群维度，使 v1.2 §19 的全链路关联字段可用。
type AuditScope struct {
	RequestID string
	UserID    string
	ClusterID string
	SessionID string
	TurnID    string
	StepID    string
}

// toStoreAuditEvent 把三期审计事件翻译成四期 AuditStore 记录（需求 v1.2 §19）。
//
// 口径：
//   - Action 原样保留（三期来源动作），EventType 由 store.EventTypeForAction 推导，
//     再按 payload 做两处细化：写操作的工具响应记为 RESOURCE_MUTATION，
//     产生会话级授权的审批记为 GRANT；
//   - 请求类事件的 payload 摘要写进 ParameterSummary，结果类写进 ResultSummary，
//     让 §19 的摘要字段有稳定来源；Store 侧再统一脱敏 + 截断；
//   - 任何字段都不含 Secret 值：脱敏由 Store（键名规则）与 Redactor（值特征）双重兜底。
func toStoreAuditEvent(e audit.Event, sc AuditScope) *store.AuditEvent {
	view := auditPayloadView(e.Payload)
	out := &store.AuditEvent{
		Timestamp: e.Timestamp,
		RequestID: sc.RequestID,
		SessionID: sc.SessionID,
		TurnID:    sc.TurnID,
		StepID:    sc.StepID,
		UserID:    sc.UserID,
		ClusterID: sc.ClusterID,
		Action:    e.Action,
		EventType: store.EventTypeForAction(e.Action),
	}
	if e.Payload != nil {
		if raw, err := json.Marshal(e.Payload); err == nil {
			out.Payload = string(raw)
		}
	}

	out.Operation = auditStringField(view, "operation", "tool", "name")
	out.ResourceKind = auditStringField(view, "kind", "resource_kind")
	out.ResourceName = auditStringField(view, "resource_name", "object", "target")
	out.Risk = auditStringField(view, "risk", "risk_level")
	out.Decision = auditStringField(view, "decision", "verdict")
	out.MatchReason = auditStringField(view, "match_reason")
	out.GrantID = auditStringField(view, "grant_id")
	out.ApprovalID = auditStringField(view, "approval_id")
	out.Status = auditStatusOf(view)

	switch {
	case out.GrantID != "" && e.Action == audit.ActionApprovalGranted:
		out.EventType = store.AuditEventGrant
	case e.Action == audit.ActionToolCallResponse && auditReadonlyOf(view) == auditNotReadonly:
		out.EventType = store.AuditEventResourceMutation
	}
	// 写操作事件的资源名优先取工具语义字段（kubectl 类事件只有 name）。
	if out.EventType == store.AuditEventResourceMutation && out.ResourceName == "" {
		out.ResourceName = auditStringField(view, "name")
	}

	switch {
	case auditIsResultEvent(e.Action):
		out.ResultSummary = out.Payload
	case auditIsRequestEvent(e.Action):
		out.ParameterSummary = out.Payload
	}
	return out
}

// auditPayloadView 把事件负载转成 map 视图；非对象负载返回 nil（只保留 payload 原文）。
func auditPayloadView(payload any) map[string]any {
	if m, ok := payload.(map[string]any); ok {
		return m
	}
	if payload == nil {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func auditStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type auditReadonlyState int

const (
	auditUnknownReadonly auditReadonlyState = iota
	auditNotReadonly
	auditIsReadonly
)

func auditReadonlyOf(m map[string]any) auditReadonlyState {
	v, ok := m["readonly"].(bool)
	if !ok {
		return auditUnknownReadonly
	}
	if v {
		return auditIsReadonly
	}
	return auditNotReadonly
}

// auditStatusOf 从 payload 推导结果状态：显式 status 优先，其次 error 字段。
func auditStatusOf(m map[string]any) string {
	if s := auditStringField(m, "status"); s != "" {
		return s
	}
	switch v := m["error"].(type) {
	case bool:
		if v {
			return "error"
		}
		return "success"
	case string:
		if strings.TrimSpace(v) != "" {
			return "error"
		}
		return "success"
	}
	return ""
}

// auditIsResultEvent 判断动作是否属于"结果类"（摘要进 result_summary）。
func auditIsResultEvent(action string) bool {
	switch action {
	case audit.ActionToolCallResponse, audit.ActionToolError, audit.ActionLLMResponse, audit.ActionLLMError,
		audit.ActionExecutionSucceeded, audit.ActionExecutionFailed,
		audit.ActionVerificationSucceeded, audit.ActionVerificationFailed,
		audit.ActionApprovalDenied, audit.ActionApprovalSkipped, audit.ActionApprovalGranted:
		return true
	}
	return strings.HasSuffix(action, ".response") || strings.HasSuffix(action, ".failed") ||
		strings.HasSuffix(action, ".error")
}

// auditIsRequestEvent 判断动作是否属于"请求类"（摘要进 parameter_summary）。
func auditIsRequestEvent(action string) bool {
	switch action {
	case audit.ActionToolCallRequest, audit.ActionLLMRequest, audit.ActionApprovalRequested,
		audit.ActionApprovalDecision, audit.ActionExecutionStarted, audit.ActionVerificationStarted,
		audit.ActionUserInput, audit.ActionSecretValueRequested:
		return true
	}
	return strings.HasSuffix(action, ".request") || strings.HasSuffix(action, ".requested") ||
		strings.HasSuffix(action, ".started")
}
