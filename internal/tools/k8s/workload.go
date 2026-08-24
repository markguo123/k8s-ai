package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_deployment。
type deploymentTool struct{ base }

func (t *deploymentTool) Name() string { return "get_deployment" }
func (t *deploymentTool) Description() string {
	return "获取 Deployment 的副本数、就绪状态、滚动条件与容器配置（镜像/资源限制/环境变量名）。"
}
func (t *deploymentTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("Deployment 名称")}, "namespace", "name")
}
func (t *deploymentTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	d, err := t.reader.GetDeployment(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	m := workloadBase("Deployment", d.Namespace, d.Name, d.Spec.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas)
	m["conditions"] = deploymentConditions(d.Status.Conditions)
	m["containers"] = containerSpecs(d.Spec.Template.Spec.Containers)
	m["selector"] = d.Spec.Selector
	m["strategy"] = d.Spec.Strategy
	return tools.ToolResult{
		Data: m,
		Text: fmt.Sprintf("deployment %s/%s ready=%d/%d", d.Namespace, d.Name, d.Status.ReadyReplicas, derefI32(d.Spec.Replicas)),
	}, nil
}

// get_statefulset。
type statefulSetTool struct{ base }

func (t *statefulSetTool) Name() string { return "get_statefulset" }
func (t *statefulSetTool) Description() string {
	return "获取 StatefulSet 的副本数、就绪状态与容器配置。"
}
func (t *statefulSetTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("StatefulSet 名称")}, "namespace", "name")
}
func (t *statefulSetTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	s, err := t.reader.GetStatefulSet(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	m := workloadBase("StatefulSet", s.Namespace, s.Name, s.Spec.Replicas, s.Status.ReadyReplicas, s.Status.UpdatedReplicas, s.Status.CurrentReplicas)
	m["containers"] = containerSpecs(s.Spec.Template.Spec.Containers)
	m["serviceName"] = s.Spec.ServiceName
	return tools.ToolResult{Data: m, Text: fmt.Sprintf("statefulset %s/%s ready=%d/%d", s.Namespace, s.Name, s.Status.ReadyReplicas, derefI32(s.Spec.Replicas))}, nil
}

// get_daemonset。
type daemonSetTool struct{ base }

func (t *daemonSetTool) Name() string { return "get_daemonset" }
func (t *daemonSetTool) Description() string {
	return "获取 DaemonSet 的期望/就绪/可用数量与容器配置。"
}
func (t *daemonSetTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("DaemonSet 名称")}, "namespace", "name")
}
func (t *daemonSetTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	d, err := t.reader.GetDaemonSet(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	m := workloadBase("DaemonSet", d.Namespace, d.Name, nil, d.Status.NumberReady, d.Status.UpdatedNumberScheduled, d.Status.NumberAvailable)
	m["desiredNumberScheduled"] = d.Status.DesiredNumberScheduled
	m["containers"] = containerSpecs(d.Spec.Template.Spec.Containers)
	return tools.ToolResult{Data: m, Text: fmt.Sprintf("daemonset %s/%s ready=%d/%d", d.Namespace, d.Name, d.Status.NumberReady, d.Status.DesiredNumberScheduled)}, nil
}

// get_replicaset。
type replicaSetTool struct{ base }

func (t *replicaSetTool) Name() string { return "get_replicaset" }
func (t *replicaSetTool) Description() string {
	return "获取 ReplicaSet 的副本状态与容器配置。"
}
func (t *replicaSetTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间"), "name": strProp("ReplicaSet 名称")}, "namespace", "name")
}
func (t *replicaSetTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "namespace", "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	r, err := t.reader.GetReplicaSet(ctx, args["namespace"], args["name"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	m := workloadBase("ReplicaSet", r.Namespace, r.Name, r.Spec.Replicas, r.Status.ReadyReplicas, r.Status.AvailableReplicas, r.Status.AvailableReplicas)
	m["containers"] = containerSpecs(r.Spec.Template.Spec.Containers)
	return tools.ToolResult{Data: m, Text: fmt.Sprintf("replicaset %s/%s ready=%d/%d", r.Namespace, r.Name, r.Status.ReadyReplicas, derefI32(r.Spec.Replicas))}, nil
}
