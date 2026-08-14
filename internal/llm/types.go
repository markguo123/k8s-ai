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

// Message 聊天消息。
type Message struct {
	Role      string     `json:"role"` // system | user | assistant
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // 二期 Tool Calling 预留
}

// ToolCall 二期 Tool Calling 预留。
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolSpec 二期 Tool Calling 预留（一期恒为 nil）。
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// ChatRequest 一次 Chat 补全请求。
type ChatRequest struct {
	Model           string
	Messages        []Message
	Temperature     float64
	MaxTokens       int
	JSONMode        bool       // true = 请求 response_format json_object（OpenAI 兼容网关普遍支持）
	DisableThinking bool       // true = 发送 chat_template_kwargs.enable_thinking=false（Qwen/vLLM）
	Tools           []ToolSpec // 二期预留；一期恒为 nil
}

// ChatResponse 一次 Chat 补全响应。
type ChatResponse struct {
	Content string
	Usage   TokenUsage
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
