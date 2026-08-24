package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// MetricsReader 是节点指标读取接口（internal/kubernetes.Client 实现）。
type MetricsReader interface {
	ListNodeMetrics(ctx context.Context) ([]metricsv1beta1.NodeMetrics, error)
}

// get_node_usage：节点资源用量（metrics API，不依赖 kubectl）。
type nodeUsageTool struct {
	base
	metrics MetricsReader
}

func (t *nodeUsageTool) Name() string { return "get_node_usage" }
func (t *nodeUsageTool) Description() string {
	return "读取全部节点的 CPU/内存使用量与占比（metrics-server 提供），用于判断节点资源压力。metrics-server 不可用时返回明确错误。"
}
func (t *nodeUsageTool) InputSchema() any {
	return objSchema(map[string]any{
		"name": strProp("节点名称（可选，省略=全部节点）"),
	})
}
func (t *nodeUsageTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	if t.metrics == nil {
		return tools.ResultError("node metrics unavailable: metrics client not connected"), nil
	}
	args, err := tools.ParseArgs(input)
	if err != nil {
		return tools.ResultError(err.Error()), nil
	}
	metrics, err := t.metrics.ListNodeMetrics(ctx)
	if err != nil {
		return tools.ResultError("get_node_usage: " + err.Error()), nil
	}
	nodes, err := t.reader.ListNodes(ctx)
	if err != nil {
		return tools.ResultError("get_node_usage: " + err.Error()), nil
	}
	allocatable := map[string]struct{ cpu, mem int64 }{}
	for _, n := range nodes {
		allocatable[n.Name] = struct{ cpu, mem int64 }{
			cpu: n.Status.Allocatable.Cpu().MilliValue(),
			mem: n.Status.Allocatable.Memory().Value(),
		}
	}
	items := make([]map[string]any, 0, len(metrics))
	var sb strings.Builder
	for _, m := range metrics {
		if want := args["name"]; want != "" && m.Name != want {
			continue
		}
		cpuMilli := m.Usage.Cpu().MilliValue()
		memBytes := m.Usage.Memory().Value()
		item := map[string]any{
			"name":          m.Name,
			"cpuMillicores": cpuMilli,
			"memoryMiB":     memBytes / (1024 * 1024),
			"timestamp":     m.Timestamp,
		}
		if cap, ok := allocatable[m.Name]; ok {
			item["cpuPercent"] = percent(cpuMilli, cap.cpu)
			item["memoryPercent"] = percent(memBytes, cap.mem)
		}
		items = append(items, item)
		sb.WriteString(fmt.Sprintf("%s: cpu=%.1f%% mem=%.1f%%\n",
			m.Name, item["cpuPercent"].(float64), item["memoryPercent"].(float64)))
	}
	return tools.ToolResult{
		Data: map[string]any{"kind": "NodeUsageList", "count": len(items), "items": items},
		Text: strings.TrimSuffix(sb.String(), "\n"),
	}, nil
}

func percent(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(used) * 100 / float64(total)
	if p < 0 {
		return 0
	}
	return p
}
