package tools

import (
	"regexp"
	"strings"
)

// P7.4（ADR-034）：工具结果的展示形态由后端声明，前端只按声明渲染、不再启发式猜测。
// 取值与前端 render 组件一一对应：table | kv | events | yaml | logs | text | empty。

var yamlLikePattern = regexp.MustCompile(`(?m)^(\s*)(apiVersion:|kind:|metadata:|-\s+name:)`)
var logLikePattern = regexp.MustCompile(`(?m)^\S+\s+\d{1,2}:\d{2}:\d{2}`)

// DisplayType 返回工具结构化结果的渲染形态。
func DisplayType(data any) string {
	switch v := data.(type) {
	case nil:
		return "empty"
	case map[string]any:
		if _, hasErr := v["error"]; hasErr {
			return "text"
		}
		if arr, ok := v["events"].([]any); ok && len(arr) > 0 {
			return "events"
		}
		if arr, ok := v["conditions"].([]any); ok && len(arr) > 0 {
			return "events"
		}
		if _, ok := v["items"]; ok {
			return "table"
		}
		scalar := 0
		for _, value := range v {
			if value == nil || !isObject(value) {
				scalar++
			}
		}
		if scalar >= 2 && scalar <= 12 {
			return "kv"
		}
		return "yaml"
	case []any:
		if len(v) == 0 {
			return "empty"
		}
		return "table"
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return "empty"
		}
		if yamlLikePattern.MatchString(text) {
			return "yaml"
		}
		if logLikePattern.MatchString(text) || strings.Contains(text, "panic") || strings.Contains(text, "ERROR") {
			return "logs"
		}
		return "text"
	default:
		return "text"
	}
}

func isObject(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

// ToolSummary 返回工具结果头部概览摘要：取人读摘要的首行、截断到 200 字符。
// 状态（成功/失败）与耗时由前端与 ok/durationMs 组合展示，避免重复拼装。
func ToolSummary(text string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	runes := []rune(line)
	if len(runes) > 200 {
		return string(runes[:200]) + "…"
	}
	return line
}
