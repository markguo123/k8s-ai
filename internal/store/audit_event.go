// 四期审计事件分类（需求 v1.2 §19）。类型常量放在 Store 侧，因为它是持久化
// 契约的唯一所有者：internal/audit（Agent 事件源）、internal/server（系统事件）
// 与统一迁移的历史回填都复用这一份映射，避免出现第二套事件枚举。
package store

import "strings"

// §19 要求的最小事件类型集合。
const (
	AuditEventLogin            = "LOGIN"
	AuditEventLogout           = "LOGOUT"
	AuditEventSession          = "SESSION"
	AuditEventApproval         = "APPROVAL"
	AuditEventGrant            = "GRANT"
	AuditEventToolCall         = "TOOL_CALL"
	AuditEventResourceMutation = "RESOURCE_MUTATION"
	AuditEventConfigChange     = "CONFIG_CHANGE"
	AuditEventSkillRun         = "SKILL_RUN"
	AuditEventClusterChange    = "CLUSTER_CHANGE"
)

// 扩展事件类型：§19 的"至少包含"允许实现补充，便于完整回放。
const (
	AuditEventUserInput  = "USER_INPUT"
	AuditEventLLMCall    = "LLM_CALL"
	AuditEventSecretRead = "SECRET_READ"
	// AuditEventReport 是报告下载类事件（P8.5，方案 15 §12.4）：admin 跨归属读别人的
	// 报告必须留痕，而 EventTypeForAction 只认 llm./agent. 前缀，不登记就会全归 OTHER，
	// 审计页面按类型筛选时这批事件等于不存在。
	AuditEventReport = "REPORT"
	AuditEventOther  = "OTHER"
)

// eventTypeByActionPrefix 把三期 audit.Action* 前缀映射到四期事件类型。
// 匹配顺序：先精确命中（eventTypeByAction），再按前缀从长到短命中。
var eventTypeByAction = map[string]string{
	"session.created":        AuditEventSession,
	"session.resumed":        AuditEventSession,
	"session.closed":         AuditEventSession,
	"user.input":             AuditEventUserInput,
	"plan.generated":         AuditEventLLMCall,
	"approval.decision":      AuditEventApproval,
	"approval.granted":       AuditEventApproval,
	"approval.denied":        AuditEventApproval,
	"approval.skipped":       AuditEventApproval,
	"execution.started":      AuditEventResourceMutation,
	"execution.succeeded":    AuditEventResourceMutation,
	"execution.failed":       AuditEventResourceMutation,
	"tool-call.request":      AuditEventToolCall,
	"tool-call.response":     AuditEventToolCall,
	"tool.error":             AuditEventToolCall,
	"config.change":          AuditEventConfigChange,
	"skill.run":              AuditEventSkillRun,
	"cluster.change":         AuditEventClusterChange,
	"auth.login":             AuditEventLogin,
	"auth.logout":            AuditEventLogout,
	"auth.password":          AuditEventLogin,
	"secret.value_requested": AuditEventSecretRead,
	"secret.touched":         AuditEventSecretRead,
	"report.download":        AuditEventReport,
	// 删除低频且不可逆，必须与下载同一事件类型进事件中心；漏掉这一行的话动作字面量照样
	// 写库，只是EventTypeForAction 把它静默归成 OTHER（P8.14，方案 15 v1.12 §0 第 5 条）。
	"report.delete": AuditEventReport,
	// 停止体检是「有人主动中断了一次对集群的采集」，与业务读写无关，归 OTHER 但必须留痕。
	"scan.cancel":            AuditEventOther,
	"verification.started":   AuditEventResourceMutation,
	"verification.succeeded": AuditEventResourceMutation,
	"verification.failed":    AuditEventResourceMutation,
	"stream_fallback":        AuditEventLLMCall,
	"stream_partial":         AuditEventLLMCall,
}

// 系统动作字面量（与 internal/audit 的事件源保持同一拼写）。
const (
	ActionConfigChange  = "config.change"
	ActionSkillRun      = "skill.run"
	ActionClusterChange = "cluster.change"
	ActionAuthLogin     = "auth.login"
	ActionAuthLogout    = "auth.logout"
	ActionAuthPassword  = "auth.password"
	// ActionReportDownload 是报告下载动作（含 admin 跨归属读取）。
	ActionReportDownload = "report.download"
	// ActionReportDelete 是报告清理动作（P8.14）。与下载只记跨 owner 不同，删除**每一条**
	// 都记：删自己的报告虽然也是日常动作，但它不可逆，事后要能回答「谁在什么时候清掉了哪份」。
	ActionReportDelete = "report.delete"
	// ActionScanCancel 是发起人主动停止一次正在执行的体检（P9.11）。停止的是「对集群的
	// 一次采集」，不是某个 Kubernetes 资源，因此它没有资源字段可记，靠 request_id 串起请求。
	ActionScanCancel = "scan.cancel"
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
