package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_service。
type serviceTool struct{ base }

func (t *serviceTool) Name() string { return "get_service" }
func (t *serviceTool) Description() string {
	return "获取 Service 的类型、ClusterIP、端口与 Selector，用于分析 Service 无流量/无法访问问题。"
}
func (t *serviceTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("Service 名称")}, "namespace", "name")
}
func (t *serviceTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	s, err := t.reader.GetService(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{Data: summarizeService(s), Text: fmt.Sprintf("service %s/%s type=%s clusterIP=%s", s.Namespace, s.Name, s.Spec.Type, s.Spec.ClusterIP)}, nil
}

// get_endpoints。
type endpointsTool struct{ base }

func (t *endpointsTool) Name() string { return "get_endpoints" }
func (t *endpointsTool) Description() string {
	return "获取 Endpoints 的后端地址与端口，判断 Service 是否有可用后端。"
}
func (t *endpointsTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("Endpoints 名称（通常等于 Service 名）")}, "namespace", "name")
}
func (t *endpointsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	e, err := t.reader.GetEndpoints(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	return tools.ToolResult{Data: summarizeEndpoints(e), Text: fmt.Sprintf("endpoints %s/%s subsets=%d", e.Namespace, e.Name, len(e.Subsets))}, nil
}

// get_endpointslices。
type endpointSlicesTool struct{ base }

func (t *endpointSlicesTool) Name() string { return "get_endpointslices" }
func (t *endpointSlicesTool) Description() string {
	return "列出 EndpointSlice（可按 service 名过滤），返回后端地址与就绪状态。"
}
func (t *endpointSlicesTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"service":   strProp("Service 名称（可选，按 kubernetes.io/service-name 标签过滤）"),
	})
}
func (t *endpointSlicesTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	slices, err := t.reader.ListEndpointSlices(ctx, args["namespace"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	items := make([]map[string]any, 0, len(slices))
	for _, es := range slices {
		if svc := args["service"]; svc != "" && es.Labels["kubernetes.io/service-name"] != svc {
			continue
		}
		items = append(items, summarizeEndpointSlice(es))
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": "EndpointSliceList", "namespace": args["namespace"], "count": len(items), "items": items},
		Text: fmt.Sprintf("endpointslices=%d", len(items)),
	}, nil
}

// get_resources：按 kind 列出资源名。
type resourcesTool struct{ base }

func (t *resourcesTool) Name() string { return "get_resources" }
func (t *resourcesTool) Description() string {
	return "按资源类型列出资源名称（kind 支持 pods/nodes/deployments/statefulsets/daemonsets/replicasets/services/endpointslices/pvcs/pvs/storageclasses/namespaces/events），可指定 namespace。"
}
func (t *resourcesTool) InputSchema() any {
	return objSchema(map[string]any{
		"kind":      strProp("资源类型，如 pods/deployments/services/pvcs/..."),
		"namespace": strProp("命名空间（可选）"),
	}, "kind")
}
func (t *resourcesTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "kind")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	kind := strings.ToLower(args["kind"])
	ns := args["namespace"]
	names := []string{}
	switch kind {
	case "pod", "pods":
		list, err := t.reader.ListPods(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "node", "nodes":
		list, err := t.reader.ListNodes(ctx)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Name)
		}
	case "deployment", "deployments":
		list, err := t.reader.ListDeployments(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "statefulset", "statefulsets":
		list, err := t.reader.ListStatefulSets(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "daemonset", "daemonsets":
		list, err := t.reader.ListDaemonSets(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "replicaset", "replicasets":
		list, err := t.reader.ListReplicaSets(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "service", "services":
		list, err := t.reader.ListServices(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "endpointslice", "endpointslices":
		list, err := t.reader.ListEndpointSlices(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "pvc", "pvcs":
		list, err := t.reader.ListPersistentVolumeClaims(ctx, ns)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Namespace+"/"+it.Name)
		}
	case "pv", "pvs":
		list, err := t.reader.ListPersistentVolumes(ctx)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Name)
		}
	case "storageclass", "storageclasses", "sc":
		list, err := t.reader.ListStorageClasses(ctx)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Name)
		}
	case "namespace", "namespaces", "ns":
		list, err := t.reader.ListNamespaces(ctx)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		for _, it := range list {
			names = append(names, it.Name)
		}
	default:
		return tools.ResultError("unsupported kind " + kind), nil
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": kind, "count": len(names), "names": names},
		Text: fmt.Sprintf("%s=%d", kind, len(names)),
	}, nil
}
