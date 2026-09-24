package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// WriteRunner 是写操作执行器接口（internal/executor 实现，架构测试守护写边界）。
// 当前只有 RunKubectl 被实际调用；其他方法为未来预留（scan 后自动修复等内部场景）。
type WriteRunner interface {
	RunKubectl(ctx context.Context, command string) (executor.Result, error)
	ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) (executor.Result, error)
	PatchDeployment(ctx context.Context, namespace, name, patch string) (executor.Result, error)
	PatchConfigMap(ctx context.Context, namespace, name, patch string) (executor.Result, error)
	RestartRollout(ctx context.Context, namespace, name string) (executor.Result, error)
	LabelResource(ctx context.Context, kind, namespace, name, key, value string) (executor.Result, error)
	DeletePod(ctx context.Context, namespace, name string, gracePeriodSeconds *int64) (executor.Result, error)
}

// CommandDescriber 是写工具的可选接口：为审批卡片提供人类可读的命令与变更说明。
// kubectlTool 不实现此接口——命令字符串已在 parameters.command 里，审批面板直接展示即可。
type CommandDescriber interface {
	Describe(input json.RawMessage) (command, change string)
}

// NewWriteTools 创建写操作 Tool：只有一个通用 kubectl 工具。
//
// 历史：v2.0.8 之前 shell 被 parse.go 硬拒，无法用 kubectl 构造写命令，
// 因此被迫注册了 6 个结构化写工具（scale_deployment/patch_deployment 等）走 client-go typed client。
// v2.0.8 放开 shell 后，kubectl 工具已能覆盖全部写操作，6 个结构化工具成为过渡产物被删除。
// executor.Runner 的 typed client 实现保留（WriteRunner 接口不动），留作未来内部场景。
func NewWriteTools(runner WriteRunner) []Tool {
	return []Tool{
		&kubectlTool{runner: runner},
	}
}

// kubectlTool：唯一的写操作工具。
//
// 覆盖全部写操作（scale/patch/restart/label/delete/apply/edit…）和读操作（get/list/describe/logs/events）。
// 读命令（get/describe/list/logs/events，且不含 shell 语法）自动执行；
// 写命令及所有含 shell 语法的命令必须走人工审批。
// 禁止：交互式命令（edit/attach/port-forward/proxy/run/cp/debug）、读取 Secret 资源、--raw 参数。
type kubectlTool struct{ runner WriteRunner }

// SchemaMetadata 说明 kubectl 工具的读写/风险动态性：命令是否只读取决于具体命令，
// 写命令及所有含 shell 构造的命令都必须走人工审批。
func (t *kubectlTool) SchemaMetadata() string {
	return "[READ/WRITE][RISK: DEPENDS ON COMMAND][APPROVAL REQUIRED FOR WRITE OR SHELL]"
}

func (t *kubectlTool) Name() string { return "kubectl" }
func (t *kubectlTool) Description() string {
	return "执行 kubectl 命令。支持 shell 构造（pipe |、heredoc <<EOF、redirect >、&& / ||、command substitution $() 等）。\n只读命令（get/describe/list/logs/events，且不含 shell 语法）自动执行；写命令及所有含 shell 语法的命令必须走人工审批。\n禁止：交互式命令（edit/attach/port-forward/proxy/run/cp/debug）、读取 Secret 资源、--raw 参数。\n流式命令（logs -f/get -w）以固定超时采样部分输出。输出自动脱敏并截断。\n写命令安全红线（务必遵守）：(1) delete/删除类操作永远是最后选择，PVC/PV/Deployment/StatefulSet/Service/Namespace 的删除必须先给出更小范围的 patch/annotate/rollout 替代方案并征得用户同意，特别是 PVC 绑定失败必须先 patch storageClassName，只有被 immutable 拒绝+用户明确同意的情况下才允许提出 delete pvc；(2) 任何 delete pvc / delete pv / delete namespace 这种 CRITICAL 级别的删除，必须在同一个回答里同时列出 patch 替代方案给用户选择，不能只提删除。"
}
func (t *kubectlTool) InputSchema() any {
	return objSchema(map[string]any{
		"command": strProp("完整 kubectl 命令，如 kubectl get pods -n default 或 kubectl scale deployment/payment-api --replicas=5"),
	}, "command")
}
func (t *kubectlTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	res, err := t.runner.RunKubectl(ctx, args["command"])
	if err != nil {
		msg := err.Error()
		// v2.0.9: 区分"环境缺 kubectl 二进制"和"命令本身写错/集群问题"，
		// 前者不是 LLM 能修好的，不能让 LLM 反复重试。
		if isKubectlMissingError(msg) {
			return ToolResult{
				Text: "SYSTEM_UNAVAILABLE",
				Data: map[string]any{
					"kind":    "kubectl_binary_missing",
					"message": "未检测到 kubectl 二进制，写操作不可用。请安装 kubectl 或在配置中设置 kubernetes.kubectl_path。只读操作（scan_cluster / get_pod / get_deployment 等）继续可用，它们走 client-go typed client 不依赖 kubectl 二进制。",
				},
				Truncated: true,
			}, nil
		}
		return ResultError("kubectl: " + msg), nil
	}
	return ToolResult{
		Text: extractKubectlSummary(res),
		Data: res,
	}, nil
}

// isKubectlMissingError 检测 executor 返回的错误是否为"kubectl 二进制不存在"。
// 错误来源：executor/kubectl.go exec.LookPath 检测（L41-43）。
func isKubectlMissingError(msg string) bool {
	for _, kw := range []string{"not found in PATH", "executable file not found", "kubectl not found"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}
func (t *kubectlTool) IsReadOnly(input json.RawMessage) bool {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return false
	}
	return approval.IsReadOnlyCommand(args["command"])
}
func (t *kubectlTool) Risk(input json.RawMessage) model.RiskLevel {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return model.RiskCritical
	}
	return approval.ClassifyCommand(args["command"])
}

// extractKubectlSummary 从 executor.Result 中提取给 LLM 和 Web UI 的一行摘要文本。
// 成功时：取 Stdout 前 200 字符（去除空行）；失败时：拼接 ExitCode + Stderr。
func extractKubectlSummary(res executor.Result) string {
	if res.ExitCode != 0 {
		if res.Stderr != "" {
			return "exit=" + itoa(res.ExitCode) + " stderr: " + firstLine(res.Stderr)
		}
		return "exit=" + itoa(res.ExitCode)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		out = strings.TrimSpace(res.Stderr)
	}
	if out == "" {
		return "ok"
	}
	// 取前 200 字符做摘要，太长截断。
	runes := []rune(out)
	if len(runes) > 200 {
		out = string(runes[:200]) + "…"
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
