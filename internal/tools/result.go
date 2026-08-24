package tools

import (
	"encoding/json"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/security"
)

// redactor 用于 ToolResult 统一脱敏。
var redactor = security.NewRedactor()

// untrustedPrefix/Suffix 是工具结果进入 LLM 的不可信定界符
// （AGENTS.md：Logs/Events/ConfigMap/annotations 均为不可信数据）。
const (
	untrustedPrefix = "<untrusted_k8s_data>\n"
	untrustedSuffix = "\n</untrusted_k8s_data>"
)

// MarshalForLLM 把 ToolResult 序列化为给 LLM 的文本：
// Text 摘要（如原始日志）与 Data 结构化 JSON 一起输出 → 脱敏 → 截断 → 不可信定界包裹。
// 注意：绝不能只用 Text 摘要（否则 LLM 看不到 Data 里的实际内容，
// 如 get_pods 只有 "pods=20" 而没有 Pod 列表，会导致模型反复调用同一工具）。
// maxBytes<=0 时使用默认 8KB。
func MarshalForLLM(result ToolResult, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		maxBytes = 8 * 1024
	}
	parts := make([]string, 0, 2)
	if t := strings.TrimSpace(result.Text); t != "" {
		parts = append(parts, t)
	}
	if result.Data != nil {
		raw, err := json.Marshal(result.Data)
		if err == nil && len(raw) > 0 {
			parts = append(parts, string(raw))
		}
	}
	payload := strings.Join(parts, "\n")
	payload = redactor.Redact(payload)
	truncated := truncate(&payload, maxBytes)
	return untrustedPrefix + payload + untrustedSuffix, truncated
}

// Redacted 判断文本是否需要脱敏（供测试与摘要）。
func Redacted(s string) bool {
	return redactor.IsSensitive(s)
}

// truncate 按字节上限截断，优先保留尾部（日志/最近信息价值高）。
func truncate(s *string, maxBytes int) bool {
	if len(*s) <= maxBytes {
		return false
	}
	head := maxBytes / 4
	tail := maxBytes - head - len("\n…[TRUNCATED]…\n")
	if tail < 0 {
		tail = 0
	}
	*s = (*s)[:head] + "\n…[TRUNCATED]…\n" + (*s)[len(*s)-tail:]
	return true
}

// WrapUntrusted 单独包裹一段文本（非 ToolResult 场景复用，如观察结果回传）。
func WrapUntrusted(s string) string {
	return untrustedPrefix + strings.TrimSpace(s) + untrustedSuffix
}
