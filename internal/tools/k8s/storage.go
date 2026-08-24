package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_pvc。
type pvcTool struct{ base }

func (t *pvcTool) Name() string { return "get_pvc" }
func (t *pvcTool) Description() string {
	return "获取 PVC 的绑定阶段、容量、StorageClass 与绑定 PV，用于定位 PVC Pending/Bound 问题。"
}
func (t *pvcTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("PVC 名称")}, "namespace", "name")
}
func (t *pvcTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	p, err := t.reader.GetPersistentVolumeClaim(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{Data: summarizePVC(p), Text: fmt.Sprintf("pvc %s/%s phase=%s", p.Namespace, p.Name, p.Status.Phase)}, nil
}

// get_pv。
type pvTool struct{ base }

func (t *pvTool) Name() string { return "get_pv" }
func (t *pvTool) Description() string {
	return "获取 PV 的绑定状态、容量、StorageClass 与 ClaimRef。"
}
func (t *pvTool) InputSchema() any {
	return objSchema(map[string]any{"name": strProp("PV 名称")}, "name")
}
func (t *pvTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	p, err := t.reader.GetPersistentVolume(ctx, args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{Data: summarizePV(p), Text: fmt.Sprintf("pv %s phase=%s", p.Name, p.Status.Phase)}, nil
}

// get_storageclass。
type storageClassTool struct{ base }

func (t *storageClassTool) Name() string { return "get_storageclass" }
func (t *storageClassTool) Description() string {
	return "获取 StorageClass 的 Provisioner、回收策略与绑定模式。"
}
func (t *storageClassTool) InputSchema() any {
	return objSchema(map[string]any{"name": strProp("StorageClass 名称")}, "name")
}
func (t *storageClassTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	sc, err := t.reader.GetStorageClass(ctx, args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{Data: summarizeStorageClass(sc), Text: fmt.Sprintf("storageclass %s provisioner=%s", sc.Name, sc.Provisioner)}, nil
}
