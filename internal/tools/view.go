package tools

import (
	"regexp"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/executor"
)

// P7.4（ADR-034）：工具结果的展示形态由后端声明，前端只按声明渲染、不再启发式猜测。
// 取值与前端 render 组件一一对应：terminal | table | kv | events | yaml | logs | text | empty。

// 统一展示类型与错误码。terminal 是 kubectl/命令类结果的首选视图，
// 完整保留 command/stdout/stderr/exitCode，而不是把任意对象猜成 KV 或表格。
const (
	DisplayTerminal = "terminal"

	ToolStatusOK      = "ok"
	ToolStatusError   = "error"
	ToolStatusDenied  = "denied"
	ToolStatusPartial = "partial"

	ErrorRBACDenied         = "RBAC_DENIED"
	ErrorAuthentication     = "AUTHENTICATION_EXPIRED"
	ErrorNotFound           = "NOT_FOUND"
	ErrorConflict           = "CONFLICT"
	ErrorInvalidArgument    = "INVALID_ARGUMENT"
	ErrorTimeout            = "TIMEOUT"
	ErrorUnsupported        = "UNSUPPORTED"
	ErrorClusterUnavailable = "CLUSTER_UNAVAILABLE"
	ErrorToolUnavailable    = "TOOL_UNAVAILABLE"
	ErrorToolFailed         = "TOOL_ERROR"
	ErrorScopeDenied        = "SCOPE_DENIED"
)

// ToolPresentation 是 Agent、SSE 和前端共用的结果呈现/状态模型。
// 它是展示事实，不是权限事实；真实执行边界仍由 AccessScope/审批/API Server 决定。
type ToolPresentation struct {
	DisplayType string
	Status      string
	ErrorCode   string
	Command     string
	Stdout      string
	Stderr      string
	ExitCode    int
	Ok          bool
}

var yamlLikePattern = regexp.MustCompile(`(?m)^(\s*)(apiVersion:|kind:|metadata:|-\s+name:)`)
var logLikePattern = regexp.MustCompile(`(?m)^\S+\s+\d{1,2}:\d{2}:\d{2}`)

// DisplayType 返回工具结构化结果的渲染形态。
func DisplayType(data any) string {
	switch v := data.(type) {
	case executor.Result, *executor.Result:
		return DisplayTerminal
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

// Present 把任意 ToolResult 归一为稳定的展示/状态模型。
// 对 executor.Result 必须使用 terminal，并明确以 ExitCode 判断成功，
// 避免 Forbidden 之类的非零退出被误判成成功。
func Present(result ToolResult) ToolPresentation {
	p := ToolPresentation{
		DisplayType: DisplayType(result.Data),
		Status:      ToolStatusOK,
		Ok:          true,
	}
	switch v := result.Data.(type) {
	case executor.Result:
		applyExecutorPresentation(&p, v)
	case *executor.Result:
		if v != nil {
			applyExecutorPresentation(&p, *v)
		}
	default:
		if data, ok := result.Data.(map[string]any); ok {
			if kind, _ := data["kind"].(string); kind == "kubectl_binary_missing" {
				p.Status = ToolStatusError
				p.Ok = false
				p.ErrorCode = result.ErrorCode
				if p.ErrorCode == "" {
					p.ErrorCode = ErrorToolUnavailable
				}
				return p
			}
			if raw, exists := data["error"]; exists {
				message := strings.TrimSpace(toString(raw))
				p.Status = ToolStatusError
				p.Ok = false
				p.ErrorCode = result.ErrorCode
				if p.ErrorCode == "" {
					p.ErrorCode = classifyToolError(message)
				}
			}
		}
	}
	if result.ErrorCode != "" {
		p.ErrorCode = result.ErrorCode
		p.Status = statusForErrorCode(p.ErrorCode)
		p.Ok = false
	}
	return p
}

func applyExecutorPresentation(p *ToolPresentation, result executor.Result) {
	p.DisplayType = DisplayTerminal
	p.Command = result.Command
	p.Stdout = result.Stdout
	p.Stderr = result.Stderr
	p.ExitCode = result.ExitCode
	if result.StreamTimeout {
		p.Status = ToolStatusPartial
		p.Ok = true
		return
	}
	if result.ExitCode != 0 {
		p.Status = ToolStatusError
		p.Ok = false
		p.ErrorCode = classifyToolError(strings.TrimSpace(result.Stderr + "\n" + result.Stdout))
	}
}

func classifyToolError(message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case lower == "":
		return ErrorToolFailed
	case strings.Contains(lower, "forbidden"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "cannot patch"), strings.Contains(lower, "cannot create"),
		strings.Contains(lower, "cannot update"), strings.Contains(lower, "cannot delete"):
		return ErrorRBACDenied
	case strings.Contains(lower, "not found"), strings.Contains(lower, "does not exist"):
		return ErrorNotFound
	case strings.Contains(lower, "already exists"), strings.Contains(lower, "conflict"):
		return ErrorConflict
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		return ErrorTimeout
	case strings.Contains(lower, "executable file not found"), strings.Contains(lower, "kubectl not found"):
		return ErrorToolUnavailable
	case strings.Contains(lower, "unsupported"), strings.Contains(lower, "not supported"):
		return ErrorUnsupported
	case strings.Contains(lower, "invalid"), strings.Contains(lower, "required"),
		strings.Contains(lower, "unbalanced"):
		return ErrorInvalidArgument
	default:
		return ErrorToolFailed
	}
}

func statusForErrorCode(code string) string {
	if code == ErrorRBACDenied || code == ErrorAuthentication || code == ErrorScopeDenied {
		return ToolStatusDenied
	}
	return ToolStatusError
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
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
