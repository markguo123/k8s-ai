package store

import (
	"encoding/json"
	"regexp"

	"github.com/k8s-ai/k8s-ai/internal/security"
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
