package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_node：单个 Node 深查。
type nodeTool struct{ base }

func (t *nodeTool) Name() string { return "get_node" }
func (t *nodeTool) Description() string {
	return "获取单个 Node 的条件（Ready/MemoryPressure 等）、容量、可分配资源与污点，用于定位调度失败/NotReady 问题。"
}
func (t *nodeTool) InputSchema() any {
	return objSchema(map[string]any{"name": strProp("节点名称")}, "name")
}
func (t *nodeTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	node, err := t.reader.GetNode(ctx, args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{Data: summarizeNode(node), Text: fmt.Sprintf("node %s conditions=%d", node.Name, len(node.Status.Conditions))}, nil
}

// get_nodes：列出全部 Node。
type nodesTool struct{ base }

func (t *nodesTool) Name() string { return "get_nodes" }
func (t *nodesTool) Description() string {
	return "列出全部 Node，返回名称/Ready 状态/是否不可调度。"
}
func (t *nodesTool) InputSchema() any {
	return objSchema(map[string]any{})
}
func (t *nodesTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	nodes, err := t.reader.ListNodes(ctx)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	items := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, summarizeNodeListItem(n))
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": "NodeList", "count": len(items), "items": items},
		Text: fmt.Sprintf("nodes=%d", len(items)),
	}, nil
}
