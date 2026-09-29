// Secret 值通道（三期 R4.5，RESOURCE_CREATE_DESIGN.md §5.3）。
//
// 红线：Secret 明文值绝不进入 manifest、ToolResult.Data/Text、会话文件、审计文件与 LLM 请求。
// 模型在 manifest 里只写 manifestx.PlaceholderAsk，真实值由交互通道（CLI/Web 审批环节）
// 在审批通过后单独收集，只经内存与 context 传给一次 executor.ApplyRequest。
package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/manifestx"
)

// SecretValueKey 定位一个需要用户提供值的 Secret key。
type SecretValueKey struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

// String 返回可读定位（用于提问文案，不含任何值）。
func (k SecretValueKey) String() string {
	if k.Namespace != "" {
		return k.Kind + " " + k.Namespace + "/" + k.Name + " 的 " + k.Key
	}
	return k.Kind + " " + k.Name + " 的 " + k.Key
}

// SecretValuePrompt 是向用户索取一个 Secret 值所需的提问（不含任何值）。
type SecretValuePrompt struct {
	Key    SecretValueKey `json:"key"`
	Prompt string         `json:"prompt"`
}

// SecretValuePlanner 是写工具的可选接口：声明本次调用需要用户提供的 Secret 值。
// Agent 在人工审批通过后、工具执行前逐个取值（三期 R4.5）。
type SecretValuePlanner interface {
	// PendingSecretValues 返回待填的 Secret key 列表；ok=false 表示本次调用无需取值。
	PendingSecretValues(input json.RawMessage) ([]SecretValuePrompt, bool)
}

// PendingSecretValues 列出清单里所有值为 $ASK 的 Secret key（稳定顺序：对象序号 + key 名）。
func PendingSecretValues(objs []manifestx.Object) []SecretValueKey {
	var out []SecretValueKey
	for i := range objs {
		o := &objs[i]
		if !o.IsSecret() {
			continue
		}
		for _, k := range o.SecretKeys {
			if !k.Ask {
				continue
			}
			out = append(out, SecretValueKey{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Key: k.Key})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// secretValuesContextKey 是 context 里存放 Secret 值的键（只在内存存在一次调用）。
type secretValuesContextKey struct{}

// DataSecretTouches 是 ToolResult.Data 里承载 Secret 指纹结论的键。
// Agent 只把这一层（key/长度/SHA-256/来源）写进审计，原值不在其中（R4.6）。
const DataSecretTouches = "secretTouches"

// SecretTouch 是一个被本次调用创建/改动过的 Secret 对象的可审计结论。
type SecretTouch struct {
	Kind      string                     `json:"kind"`
	Namespace string                     `json:"namespace,omitempty"`
	Name      string                     `json:"name"`
	Keys      []executor.SecretKeyStatus `json:"keys"`
}

// SecretTouchesFromData 从工具结果里取出 Secret 指纹结论（无则返回 nil）。
func SecretTouchesFromData(data any) []SecretTouch {
	switch m := data.(type) {
	case map[string]any:
		touches, _ := m[DataSecretTouches].([]SecretTouch)
		return touches
	case *map[string]any:
		if m == nil {
			return nil
		}
		touches, _ := (*m)[DataSecretTouches].([]SecretTouch)
		return touches
	default:
		return nil
	}
}

// WithSecretValues 把用户提供的 Secret 值挂到 ctx 上，供本次工具调用消费。
func WithSecretValues(ctx context.Context, values map[SecretValueKey]string) context.Context {
	if len(values) == 0 {
		return ctx
	}
	return context.WithValue(ctx, secretValuesContextKey{}, values)
}

// SecretValuesFromContext 取回本次调用的 Secret 值（可能为 nil）。
func SecretValuesFromContext(ctx context.Context) map[SecretValueKey]string {
	if ctx == nil {
		return nil
	}
	values, _ := ctx.Value(secretValuesContextKey{}).(map[SecretValueKey]string)
	return values
}

// missingSecretValues 列出还没有取到值的 Secret key（保持待填顺序）。
func missingSecretValues(values map[SecretValueKey]string, pending []SecretValueKey) []SecretValueKey {
	if len(pending) == 0 {
		return nil
	}
	var missing []SecretValueKey
	for _, key := range pending {
		if strings.TrimSpace(values[key]) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

// secretValuesForObject 把 ctx 里的值折叠成 executor.ApplyRequest 需要的 key→value。
func secretValuesForObject(values map[SecretValueKey]string, o *manifestx.Object) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string)
	for k, v := range values {
		if k.Kind != o.Kind || k.Name != o.Name || !sameNamespace(k.Namespace, o.Namespace) {
			continue
		}
		if strings.TrimSpace(v) == "" {
			continue
		}
		out[k.Key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameNamespace 比较命名空间：空串表示"未指定"，与对象解析后的命名空间互补。
func sameNamespace(a, b string) bool {
	return a == b || a == "" || b == ""
}
