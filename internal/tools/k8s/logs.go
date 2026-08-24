package k8s

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// logsTool 实现 get_logs / get_previous_logs（previous=true 时取上次容器日志）。
type logsTool struct {
	base
	previous bool
}

func (t *logsTool) Name() string {
	if t.previous {
		return "get_previous_logs"
	}
	return "get_logs"
}
func (t *logsTool) Description() string {
	if t.previous {
		return "获取 Pod 容器上一次崩溃前的日志（--previous），用于分析 CrashLoopBackOff/OOMKilled 根因。"
	}
	return "获取 Pod 容器日志（尾部截断），用于分析运行期错误。"
}
func (t *logsTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace":    strProp("命名空间"),
		"pod":          strProp("Pod 名称"),
		"container":    strProp("容器名（可选，单容器 Pod 可省略）"),
		"tailLines":    intProp("日志尾部行数（默认 100）"),
		"sinceSeconds": intProp("仅取最近 N 秒的日志（可选）"),
	}, "namespace", "pod")
}
func (t *logsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "pod")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	tail := int64(100)
	if v := args["tailLines"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return tools.ResultError("invalid tailLines"), nil
		}
		tail = n
	}
	var since *int64
	if v := args["sinceSeconds"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return tools.ResultError("invalid sinceSeconds"), nil
		}
		since = &n
	}
	raw, err := t.reader.GetPodLogs(ctx, args["namespace"], args["pod"], args["container"], model.LogOptions{
		Container:    args["container"],
		TailLines:    &tail,
		SinceSeconds: since,
		Previous:     t.previous,
		MaxBytes:     64 * 1024,
		MaxLineBytes: 0,
	})
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	content := string(raw)
	return tools.ToolResult{
		Data: map[string]any{
			"namespace": args["namespace"], "pod": args["pod"], "container": args["container"],
			"previous": t.previous, "truncated": len(raw) >= 64*1024,
		},
		Text: content,
	}, nil
}
