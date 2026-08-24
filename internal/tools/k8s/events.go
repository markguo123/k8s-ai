package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_events：按 involvedObject 过滤最近事件。
type eventsTool struct{ base }

func (t *eventsTool) Name() string { return "get_events" }
func (t *eventsTool) Description() string {
	return "获取 Events（可按 namespace/kind/name 过滤），返回 type/reason/message/count，用于定位调度失败、镜像拉取失败、驱逐等根因。"
}
func (t *eventsTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间（可选，省略=全部）"),
		"kind":      strProp("对象类型（可选，如 Pod/Node/PVC/Deployment）"),
		"name":      strProp("对象名称（可选）"),
		"limit":     intProp("返回条数上限（默认 20）"),
	})
}
func (t *eventsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	limit := 20
	if v := args["limit"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return tools.ResultError("invalid limit"), nil
		}
		limit = n
	}
	events, err := t.reader.ListEvents(ctx, args["namespace"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	kind := args["kind"]
	name := args["name"]
	items := make([]map[string]any, 0, limit)
	for _, e := range events {
		if kind != "" && !strings.EqualFold(e.InvolvedObject.Kind, kind) {
			continue
		}
		if name != "" && e.InvolvedObject.Name != name {
			continue
		}
		items = append(items, summarizeEvent(e))
		if len(items) >= limit {
			break
		}
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": "EventList", "namespace": args["namespace"], "count": len(items), "items": items},
		Text: fmt.Sprintf("events=%d", len(items)),
	}, nil
}
