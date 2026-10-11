// 四期审计事件分类（需求 v1.2 §19）。类型常量放在 Store 侧，因为它是持久化
// 契约的唯一所有者：internal/audit（Agent 事件源）、internal/server（系统事件）
// 与统一迁移的历史回填都复用这一份映射，避免出现第二套事件枚举。
package store

import "strings"

// §19 要求的最小事件类型集合。
const (
	AuditEventLogin             = "LOGIN"
	AuditEventLogout            = "LOGOUT"
	AuditEventSession           = "SESSION"
	AuditEventApproval          = "APPROVAL"
	AuditEventGrant             = "GRANT"
	AuditEventToolCall          = "TOOL_CALL"
	AuditEventResourceMutation  = "RESOURCE_MUTATION"
	AuditEventConfigChange      = "CONFIG_CHANGE"
	AuditEventSkillRun          = "SKILL_RUN"
	AuditEventClusterChange     = "CLUSTER_CHANGE"
)

// 扩展事件类型：§19 的"至少包含"允许实现补充，便于完整回放。
const (
	AuditEventUserInput  = "USER_INPUT"
	AuditEventLLMCall    = "LLM_CALL"
	AuditEventSecretRead = "SECRET_READ"
	AuditEventOther      = "OTHER"
)

// eventTypeByActionPrefix 把三期 audit.Action* 前缀映射到四期事件类型。
// 匹配顺序：先精确命中（eventTypeByAction），再按前缀从长到短命中。
var eventTypeByAction = map[string]string{
	"session.created":         AuditEventSession,
	"session.resumed":         AuditEventSession,
	"session.closed":          AuditEventSession,
	"user.input":              AuditEventUserInput,
	"plan.generated":          AuditEventLLMCall,
	"approval.decision":       AuditEventApproval,
	"approval.granted":        AuditEventApproval,
	"approval.denied":         AuditEventApproval,
	"approval.skipped":        AuditEventApproval,
	"execution.started":       AuditEventResourceMutation,
	"execution.succeeded":     AuditEventResourceMutation,
	"execution.failed":        AuditEventResourceMutation,
	"tool-call.request":       AuditEventToolCall,
	"tool-call.response":      AuditEventToolCall,
	"tool.error":              AuditEventToolCall,
	"config.change":           AuditEventConfigChange,
	"skill.run":               AuditEventSkillRun,
	"cluster.change":          AuditEventClusterChange,
	"auth.login":              AuditEventLogin,
	"auth.logout":             AuditEventLogout,
	"secret.value_requested":  AuditEventSecretRead,
	"secret.touched":          AuditEventSecretRead,
	"verification.started":    AuditEventResourceMutation,
	"verification.succeeded":  AuditEventResourceMutation,
	"verification.failed":     AuditEventResourceMutation,
	"stream_fallback":         AuditEventLLMCall,
	"stream_partial":          AuditEventLLMCall,
}

// 系统动作字面量（与 internal/audit 的事件源保持同一拼写）。
const (
	ActionConfigChange = "config.change"
	ActionSkillRun     = "skill.run"
	ActionClusterChange = "cluster.change"
	ActionAuthLogin    = "auth.login"
	ActionAuthLogout   = "auth.logout"
)

// EventTypeForAction 返回三期动作对应的四期事件类型；未知动作归入 OTHER。
func EventTypeForAction(action string) string {
	if t, ok := eventTypeByAction[action]; ok {
		return t
	}
	switch {
	case strings.HasPrefix(action, "llm."):
		return AuditEventLLMCall
	case strings.HasPrefix(action, "agent."):
		return AuditEventSession
	default:
		return AuditEventOther
	}
}
