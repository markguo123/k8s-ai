package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/llm"
)

// shimOutput 是 shim 模式的 LLM 输出契约。
type shimOutput struct {
	Thought string `json:"thought"`
	Action  *struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"action"`
	Answer string `json:"answer"`
}

// parseShimOutput 提取并解析 shim JSON（支持 ```json 代码块包裹与前后文本）。
func parseShimOutput(content string) (shimOutput, error) {
	var out shimOutput
	s := extractJSON(content)
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return out, fmt.Errorf("parse shim output: %w", err)
	}
	return out, nil
}

// extractJSON 从可能带 Markdown 代码块/前后文本的内容中提取 JSON 对象。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end >= start {
		return s[start : end+1]
	}
	return s
}

// isUnsupportedToolsError 判断网关是否因不支持 tools 返回错误（auto 降级用）。
func isUnsupportedToolsError(err error) bool {
	apiErr, ok := err.(*llm.APIError)
	if !ok || apiErr.StatusCode != 400 {
		return false
	}
	low := strings.ToLower(apiErr.Body)
	return strings.Contains(low, "tool") || strings.Contains(low, "function")
}
