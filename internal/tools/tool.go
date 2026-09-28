// Package tools 定义二期 Tool 契约与注册表（设计逻辑借鉴 kubectl-ai，
// 类型与安全语义按 k8s-ai 重新实现：富接口 IsReadOnly/Risk）。
package tools

import (
	"context"
	"encoding/json"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// Tool 是暴露给 LLM 的统一工具契约。
type Tool interface {
	// Name 是工具标识（camel_case，传给 LLM）。
	Name() string
	// Description 说明何时使用该工具。
	Description() string
	// InputSchema 是入参 JSON Schema（OpenAI 兼容）。
	InputSchema() any
	// Execute 执行工具调用；错误以 error 返回，不中断 Agent Loop。
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
	// IsReadOnly 基于入参判断该调用是否只读；无法确定时返回 false（安全侧）。
	IsReadOnly(input json.RawMessage) bool
	// Risk 返回该调用的风险等级（审批展示/审计用）。
	Risk(input json.RawMessage) model.RiskLevel
}

// ToolResult 是工具返回结果，统一经过截断/脱敏/定界后进入 LLM 与 UI。
type ToolResult struct {
	Data      any    // 结构化数据，JSON 序列化后给 LLM
	Text      string // 人读摘要（终端/Web 展示）
	Truncated bool   // Data/Text 是否因超限被截断
	Redacted  bool   // 是否发生了脱敏
}

// MetadataDescriber 是可选接口：为模型可见的 Tool Schema 提供静态安全元数据前缀。
// 该 metadata 仅用于帮助模型选择工具，不作为安全边界；实际安全判断始终以
// Tool.IsReadOnly / Tool.Risk 为最终依据（Agent Loop 不变）。
type MetadataDescriber interface {
	// SchemaMetadata 返回形如 "[READ ONLY][RISK: SAFE][APPROVAL REQUIRED: NO]" 的稳定前缀。
	SchemaMetadata() string
}

// GrantTargeter 是写工具的可选接口：为「本次会话不再询问」提供精确授权目标。
// 返回 ok=false 表示这次调用无法收敛成单个 kind/name（例如一次创建多个对象），
// 此时 Agent 不会生成、也不会复用任何授权记录，每次都重新走人工确认。
type GrantTargeter interface {
	// GrantTarget 返回本次调用的授权目标（kind/name/namespace）。
	GrantTarget(input json.RawMessage) (kind, name, namespace string, ok bool)
}

// ResultError 包装工具级错误：回传 LLM 继续调查，不中断 Loop。
func ResultError(msg string) ToolResult {
	return ToolResult{Data: map[string]any{"error": msg}, Text: "错误：" + msg}
}
