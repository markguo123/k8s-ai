package store

import (
	"encoding/json"
	"regexp"

	"github.com/k8s-ai/k8s-ai/internal/security"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// redactor 对审计/审批敏感字段统一脱敏（与 audit/session 同一策略）。
var auditRedactor = security.NewRedactor()

// sensitiveKeyName 覆盖配置/审批/审计 JSON 负载中的敏感键名（含 kubeconfig）。
var sensitiveKeyName = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|credential|kubeconfig|client[_-]?secret)`)

// redactJSONPayload 对 JSON 负载做 JSON 感知脱敏：
//   - 敏感键名的值整体替换为 [REDACTED]（含 kubeconfig，保证审计/审批零明文）；
//   - 非敏感键名的字符串再走 security.Redactor 兜底（JWT/bearer/API Key 等）。
//
// 若负载不是合法 JSON，退回纯文本脱敏。
func redactJSONPayload(payload string) string {
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return auditRedactor.Redact(payload)
	}
	redactInPlace(&v)
	if out, err := json.Marshal(v); err == nil {
		return string(out)
	}
	return auditRedactor.Redact(payload)
}

// RedactJSONPayload 是对外统一 JSON 脱敏入口（SSE 事件流 / 审批 / 审计等所有
// 出站负载共用同一策略）：敏感键名整值替换 + 自由文本兜底，Secret 明文不落任何出口。
func RedactJSONPayload(payload string) string {
	return redactJSONPayload(payload)
}

func redactInPlace(v *any) {
	switch val := (*v).(type) {
	case map[string]any:
		for k, item := range val {
			if sensitiveKeyName.MatchString(k) {
				val[k] = "[REDACTED]"
				continue
			}
			redactInPlace(&item)
			val[k] = item
		}
	case []any:
		for i := range val {
			redactInPlace(&val[i])
		}
	case string:
		*v = auditRedactor.Redact(val)
	}
}

// redactSessionForStore 返回会话的落库副本：消息正文、摘要、工具调用参数与
// 结构化结果统一脱敏后再序列化。
//
// 与 internal/session 文件 Store 的 write 同一口径（AGENTS.md：敏感信息在进入
// 会话文件前必须脱敏），且只作用于副本——调用方内存中的会话保持原样，当前这轮
// 对话的展示与 LLM 上下文不受影响。
func redactSessionForStore(sess *session.Session) *session.Session {
	if sess == nil {
		return nil
	}
	out := *sess
	// 会话名同样可能被人贴进凭据（改名入口是自由文本），落库前一起脱敏。
	out.Name = auditRedactor.Redact(sess.Name)
	if sess.Messages == nil {
		return &out
	}
	out.Messages = make([]session.Message, len(sess.Messages))
	for i, m := range sess.Messages {
		m.Content = auditRedactor.Redact(m.Content)
		m.Summary = auditRedactor.Redact(m.Summary)
		if len(m.Arguments) > 0 {
			m.Arguments = json.RawMessage(redactJSONPayload(string(m.Arguments)))
		}
		if m.Data != nil {
			if data, err := json.Marshal(m.Data); err == nil {
				var decoded any
				if json.Unmarshal([]byte(redactJSONPayload(string(data))), &decoded) == nil {
					m.Data = decoded
				}
			}
		}
		out.Messages[i] = m
	}
	return &out
}
