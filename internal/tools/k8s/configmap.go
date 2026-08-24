package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_configmap：读取单个 ConfigMap（诊断配置类启动失败/崩溃）。
type configMapTool struct{ base }

func (t *configMapTool) Name() string { return "get_configmap" }
func (t *configMapTool) Description() string {
	return "读取 ConfigMap 的 data（配置内容，进入 LLM 前脱敏）。Pod 启动失败/崩溃时先看它挂载的 ConfigMap 是否有配置错误。"
}
func (t *configMapTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("ConfigMap 名称"),
	}, "namespace", "name")
}
func (t *configMapTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	cm, err := t.reader.GetConfigMap(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{
		Data: map[string]any{
			"kind": "ConfigMap", "namespace": cm.Namespace, "name": cm.Name,
			"data": cm.Data, "binaryDataKeys": keys(cm.BinaryData),
		},
		Text: fmt.Sprintf("configmap %s/%s", cm.Namespace, cm.Name),
	}, nil
}

// get_configmaps：按命名空间列出 ConfigMap 名称。
type configMapsTool struct{ base }

func (t *configMapsTool) Name() string { return "get_configmaps" }
func (t *configMapsTool) Description() string {
	return "列出命名空间内的 ConfigMap 名称，便于定位 Pod 挂载的配置。"
}
func (t *configMapsTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
	}, "namespace")
}
func (t *configMapsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	list, err := t.reader.ListConfigMaps(ctx, args["namespace"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	names := make([]string, 0, len(list))
	for _, cm := range list {
		names = append(names, cm.Name)
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": "ConfigMapList", "namespace": args["namespace"], "count": len(names), "names": names},
		Text: fmt.Sprintf("configmaps=%d", len(names)),
	}, nil
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
