// Package security 提供敏感信息脱敏能力（FR-018，ADR-006）。
// 脱敏在采集边界执行：日志/Events/注解进入 Evidence 前替换为 [REDACTED]。
package security

import (
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

// Redactor 负责对不可信文本脱敏；编译后的正则只读，线程安全。
type Redactor struct {
	patterns []*regexp.Regexp
}

// NewRedactor 创建默认脱敏器。
func NewRedactor() *Redactor {
	return &Redactor{patterns: compiledPatterns}
}

// 高置信脱敏模式：命中的整段内容替换为 [REDACTED]。
var compiledPatterns = []*regexp.Regexp{
	// API Key 常见形式
	regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[:=]\s*\S+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{10,}`),
	// 密码类键值
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*\S+`),
	// Secret/Token 键值（要求有 = 或 :，避免误伤普通词）
	regexp.MustCompile(`(?i)(secret|token)\s*[:=]\s*\S+`),
	// 认证头与 Cookie
	regexp.MustCompile(`(?i)authorization\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)cookie\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]+=*`),
	// JWT（eyJ 开头三段式）
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	// 私钥块（多行）
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
}

// 连接串中的密码：user:pass@ → user:[REDACTED]@，保留用户名便于排查。
var connStringPattern = regexp.MustCompile(`(\w+://)([^:\s/]+):([^@\s/]+)@`)

// 敏感键名：命中则整个值直接替换（ConfigMap/注解等键值场景）。
var sensitiveKeyPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|credential)`)

// Redact 对自由文本执行全部模式脱敏。
func (r *Redactor) Redact(s string) string {
	for _, p := range r.patterns {
		s = p.ReplaceAllString(s, redacted)
	}
	s = connStringPattern.ReplaceAllString(s, "${1}${2}:[REDACTED]@")
	return s
}

// RedactValueByKey 对键值对的值脱敏：键名命中敏感词时整个值替换，
// 否则按自由文本脱敏（FR-018 关键词策略）。
func (r *Redactor) RedactValueByKey(key, value string) string {
	if sensitiveKeyPattern.MatchString(key) {
		return redacted
	}
	return r.Redact(value)
}

// RedactBytes 是 Redact 的 []byte 版本。
func (r *Redactor) RedactBytes(b []byte) []byte {
	return []byte(r.Redact(string(b)))
}

// IsSensitive 判断文本是否包含需要脱敏的内容（供测试与日志摘要使用）。
func (r *Redactor) IsSensitive(s string) bool {
	low := strings.ToLower(s)
	for _, p := range r.patterns {
		if p.MatchString(low) {
			return true
		}
	}
	return connStringPattern.MatchString(s)
}
