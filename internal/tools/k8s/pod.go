package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// get_pod：单个 Pod 深查。
type podTool struct{ base }

func (t *podTool) Name() string { return "get_pod" }
func (t *podTool) Description() string {
	return "获取单个 Pod 的状态、容器状态、重启次数与条件，用于定位 Pod 异常（如 CrashLoopBackOff/OOMKilled/Pending）。namespace 可省略：自动全集群按名称查找，支持前缀匹配（Deployment/StatefulSet 名可查到其 Pod）；多个候选时返回列表。"
}
func (t *podTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间（可选，省略=全集群按名称查找）"),
		"name":      strProp("Pod 名称"),
	}, "name")
}
func (t *podTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input, "name")
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	if ns := args["namespace"]; ns != "" {
		pod, err := t.reader.GetPod(ctx, ns, args["name"])
		if err == nil {
			return tools.ToolResult{
				Data: podWithEvents(ctx, t.reader, pod),
				Text: fmt.Sprintf("pod %s/%s phase=%s", pod.Namespace, pod.Name, pod.Status.Phase),
			}, nil
		}
		// 精确名未命中（如用 Deployment 名查）时，在该 namespace 内按前缀回退。
		if m := prefixMatches(ctx, t.reader, ns, args["name"]); len(m) == 1 {
			pod, err := t.reader.GetPod(ctx, ns, m[0]["name"].(string))
			if err == nil {
				return tools.ToolResult{
					Data: podWithEvents(ctx, t.reader, pod),
					Text: fmt.Sprintf("pod %s/%s phase=%s", pod.Namespace, pod.Name, pod.Status.Phase),
				}, nil
			}
		}
		return tools.ResultError(err.Error()), nil
	}
	// namespace 省略：全集群按名称精确查找；同名多个时返回候选，避免模型猜命名空间。
	matches := prefixMatches(ctx, t.reader, "", args["name"])
	if len(matches) == 0 {
		return tools.ResultError("pod " + args["name"] + " not found in any namespace (exact or prefix)"), nil
	}
	switch len(matches) {
	case 1:
		ns := matches[0]["namespace"].(string)
		podName := matches[0]["name"].(string)
		pod, err := t.reader.GetPod(ctx, ns, podName)
		if err != nil {
			return tools.ResultError(err.Error()), nil
		}
		return tools.ToolResult{
			Data: podWithEvents(ctx, t.reader, pod),
			Text: fmt.Sprintf("pod %s/%s phase=%s", pod.Namespace, pod.Name, pod.Status.Phase),
		}, nil
	default:
		return tools.ToolResult{
			Data: map[string]any{
				"kind":  "PodCandidates",
				"count": len(matches),
				"items": matches,
				"hint":  "多个命名空间存在同名 Pod，请指定 namespace 精确查询",
			},
			Text: fmt.Sprintf("found %d pods named %s across namespaces", len(matches), args["name"]),
		}, nil
	}
}

// prefixMatches 在 namespace（空=全部）内按"精确名优先、前缀兜底"返回候选。
func prefixMatches(ctx context.Context, reader Reader, namespace, name string) []map[string]any {
	pods, err := reader.ListPods(ctx, namespace)
	if err != nil {
		return nil
	}
	exact := make([]map[string]any, 0, 2)
	prefix := make([]map[string]any, 0, 4)
	for _, p := range pods {
		if p.Name == name {
			exact = append(exact, summarizePodListItem(p))
		} else if strings.HasPrefix(p.Name, name) {
			prefix = append(prefix, summarizePodListItem(p))
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return prefix
}

// podWithEvents 在 Pod 摘要中内联最近 Events（describe 效果，减少 LLM 往返）。
func podWithEvents(ctx context.Context, reader Reader, pod *corev1.Pod) map[string]any {
	m := summarizePod(pod)
	events, err := reader.ListEvents(ctx, pod.Namespace)
	if err != nil {
		return m
	}
	items := make([]map[string]any, 0, 10)
	for _, e := range events {
		if e.InvolvedObject.Kind == "Pod" && e.InvolvedObject.Name == pod.Name {
			items = append(items, summarizeEvent(e))
		}
	}
	if len(items) > 10 {
		items = items[len(items)-10:]
	}
	m["events"] = items
	return m
}

// get_pods：按命名空间列出 Pod（可省略 namespace = 全集群）。
type podsTool struct{ base }

func (t *podsTool) Name() string { return "get_pods" }
func (t *podsTool) Description() string {
	return "列出 Pod（可指定 namespace；省略则列出所有命名空间），返回名称/阶段/节点/重启次数。"
}
func (t *podsTool) InputSchema() any {
	return objSchema(map[string]any{"namespace": strProp("命名空间（可选，省略=全部）")})
}
func (t *podsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	args, err := tools.ParseArgs(input)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	pods, err := t.reader.ListPods(ctx, args["namespace"])
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	items := make([]map[string]any, 0, len(pods))
	for _, p := range pods {
		items = append(items, summarizePodListItem(p))
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": "PodList", "namespace": args["namespace"], "count": len(items), "items": items},
		Text: fmt.Sprintf("pods=%d", len(items)),
	}, nil
}
