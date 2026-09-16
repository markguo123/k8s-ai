// Package llm 提供 OpenAI Compatible Chat 客户端（FR-015）。
// 业务层只依赖本包的 LLMClient 接口，不依赖任何 OpenAI SDK。
package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// LLMClient 大模型聊天客户端接口（FR-015）。
type LLMClient interface {
	// Chat 执行一次 Chat 补全；超时/429/5xx 的重试在实现内部处理。
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// StreamClient 是可选的流式补全能力：Client 实现，业务层按需断言。
// onDelta 在收到增量文本时同步回调（可能高频）；返回值与 Chat 同构
// （内容与 ToolCalls 已在流式过程中累积完整），上层循环无需感知流式细节。
type StreamClient interface {
	// ChatStream 流式执行一次 Chat 补全；实现内部处理流式协议（SSE）。
	// 失败时若已有增量输出，返回已累积的部分响应 + 错误；调用方可回退 Chat。
	ChatStream(ctx context.Context, req ChatRequest, onDelta func(delta string)) (*ChatResponse, error)
}

// StreamEventType 是流式响应中的语义块类型。
type StreamEventType string

const (
	StreamContentDelta   StreamEventType = "content-delta"
	StreamReasoningDelta StreamEventType = "reasoning-delta"
)

// StreamEvent 是 LLM 流中的一个语义块。原始思考内容只在客户端内部可见，业务层决定是否摘要展示。
type StreamEvent struct {
	Type StreamEventType
	Text string
}

// TypedStreamClient 是支持语义流事件的可选客户端能力。
type TypedStreamClient interface {
	ChatStreamEvents(ctx context.Context, req ChatRequest, onEvent func(StreamEvent)) (*ChatResponse, error)
}

// Message 聊天消息。
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant 消息携带的 Tool 调用
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool 消息回传的调用 ID
}

// ToolCall 是 LLM 请求执行的工具调用。
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// MarshalJSON 把 ToolCall 序列化为 OpenAI 标准 assistant tool_calls 元素：
// {"id":..., "type":"function", "function":{"name":..., "arguments":"<json string>"}}。
// vLLM/Qwen 网关按该格式校验（扁平 {id,name,arguments} 会被 400 拒绝）。
func (t ToolCall) MarshalJSON() ([]byte, error) {
	type wireFunction struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type wire struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function wireFunction `json:"function"`
	}
	return json.Marshal(wire{
		ID:   t.ID,
		Type: "function",
		Function: wireFunction{
			Name:      t.Name,
			Arguments: string(t.Arguments),
		},
	})
}

// ToolSpec 是暴露给 LLM 的工具定义（OpenAI compatible tools 数组元素）。
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ChatRequest 一次 Chat 补全请求。
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	JSONMode    bool              // true = 请求 response_format json_object（OpenAI 兼容网关普遍支持）
	Thinking    map[string]string // 标准思考参数，例如 {type: enabled}
	Tools       []ToolSpec        // 二期预留；一期恒为 nil
}

// ChatResponse 一次 Chat 补全响应。
type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall // 非空 = LLM 请求执行工具（多轮 Tool Calling）
	Usage     TokenUsage
}

// TokenUsage token 用量统计。
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// APIError 服务端返回的非成功响应（body 已脱敏，FR-015 api_key 不落错误）。
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm api error: status %d, body %s", e.StatusCode, e.Body)
}
