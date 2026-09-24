// Package k8s 实现二期第一批只读 Kubernetes Tools（复用一期 kubernetes/scanner
// 只读门面，不重复实现采集逻辑）。
package k8s

import (
	"encoding/json"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// Reader 是只读 Tool 依赖的 Kubernetes 只读门面（scanner.Reader）。
type Reader = scanner.Reader

// base 提供只读工具公共行为（全部只读、风险 SAFE）。
type base struct {
	reader Reader
}

func (b *base) IsReadOnly(json.RawMessage) bool { return true }
func (b *base) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskSafe
}

func (b *base) SchemaMetadata() string {
	return "[READ ONLY][RISK: SAFE][APPROVAL REQUIRED: NO]"
}

// All 返回全部只读 Kubernetes Tools。
func All(r Reader) []tools.Tool {
	return []tools.Tool{
		&podTool{base: base{reader: r}},
		&podsTool{base: base{reader: r}},
		&nodeTool{base: base{reader: r}},
		&nodesTool{base: base{reader: r}},
		&deploymentTool{base: base{reader: r}},
		&statefulSetTool{base: base{reader: r}},
		&daemonSetTool{base: base{reader: r}},
		&replicaSetTool{base: base{reader: r}},
		&serviceTool{base: base{reader: r}},
		&endpointsTool{base: base{reader: r}},
		&endpointSlicesTool{base: base{reader: r}},
		&pvcTool{base: base{reader: r}},
		&pvTool{base: base{reader: r}},
		&storageClassTool{base: base{reader: r}},
		&eventsTool{base: base{reader: r}},
		&configMapTool{base: base{reader: r}},
		&configMapsTool{base: base{reader: r}},
		&logsTool{base: base{reader: r}},
		&logsTool{base: base{reader: r}, previous: true},
		&namespaceTool{base: base{reader: r}},
		&resourcesTool{base: base{reader: r}},
		newNodeUsageTool(r),
	}
}

// newNodeUsageTool 在 reader 支持 metrics 时启用节点资源用量工具。
func newNodeUsageTool(r Reader) tools.Tool {
	if m, ok := r.(MetricsReader); ok {
		return &nodeUsageTool{base: base{reader: r}, metrics: m}
	}
	return &nodeUsageTool{base: base{reader: r}}
}

// RegisterAll 把全部只读工具注册进注册表。
func RegisterAll(reg *tools.Registry, r Reader) error {
	for _, t := range All(r) {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("register tool %s: %w", t.Name(), err)
		}
	}
	return nil
}

// schema helpers：JSON Schema 构建。
func objSchema(props map[string]any, required ...string) map[string]any {
	req := make([]any, 0, len(required))
	for _, r := range required {
		req = append(req, r)
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
