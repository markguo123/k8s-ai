package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/security"
)

// llmRedactor 用于对错误消息做脱敏（api_key/回显内容不落日志）。
var llmRedactor = security.NewRedactor()

// Options 客户端配置。
type Options struct {
	Endpoint        string // 例如 http://localhost:8000/v1
	APIKey          string
	Model           string
	Temperature     float64
	MaxTokens       int
	Timeout         time.Duration // 单次调用超时（默认 120s）
	MaxRetries      int           // 额外重试次数（默认 2）
	HTTPClient      *http.Client
	Thinking        map[string]string // 标准 thinking 参数，例如 {type: enabled}
	ReasoningFields []string          // SSE delta 中的思考字段名
	DisableStream   bool              // true = ChatStream 退化为非流式（配置 llm.stream=false）
}

// Client 是 OpenAI Compatible Chat Completions 的实现。
type Client struct {
	endpoint        string
	apiKey          string
	model           string
	temperature     float64
	maxTokens       int
	timeout         time.Duration
	maxRetries      int
	http            *http.Client
	retryBase       time.Duration // 退避基数（测试可调）
	disableStream   bool
	thinking        map[string]string
	reasoningFields []string
}

// New 创建客户端并应用默认值。
func New(opts Options) *Client {
	c := &Client{
		endpoint:        strings.TrimRight(opts.Endpoint, "/"),
		apiKey:          opts.APIKey,
		model:           opts.Model,
		temperature:     opts.Temperature,
		maxTokens:       opts.MaxTokens,
		timeout:         opts.Timeout,
		maxRetries:      opts.MaxRetries,
		http:            opts.HTTPClient,
		retryBase:       200 * time.Millisecond,
		disableStream:   opts.DisableStream,
		thinking:        opts.Thinking,
		reasoningFields: opts.ReasoningFields,
	}
	if c.timeout <= 0 {
		c.timeout = 120 * time.Second
	}
	if c.maxRetries <= 0 {
		c.maxRetries = 2
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	return c
}

// chatRequestBody 是 Chat Completions 请求体。
type chatRequestBody struct {
	Model          string            `json:"model"`
	Messages       []Message         `json:"messages"`
	Temperature    float64           `json:"temperature"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	Stream         bool              `json:"stream"`
	StreamOptions  *streamOptions    `json:"stream_options,omitempty"`
	ResponseFormat *responseFormat   `json:"response_format,omitempty"`
	Thinking       map[string]string `json:"thinking,omitempty"`
	Tools          []wireTool        `json:"tools,omitempty"` // 二期 Tool Calling（native 模式）
}

// streamOptions 请求流式 usage 统计（OpenAI/vLLM 兼容：最后一个 chunk 携带 usage）。
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// wireTool 是 OpenAI 标准的 tools 数组元素：
// {"type":"function","function":{"name":...,"description":...,"parameters":...}}。
// vLLM/Qwen 网关按此格式校验（扁平格式会被拒绝）。
type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// toWireTools 把领域 ToolSpec 转为 OpenAI 标准 wire 格式。
func toWireTools(specs []ToolSpec) []wireTool {
	out := make([]wireTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, wireTool{
			Type: "function",
			Function: wireToolFunction{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  s.Parameters,
			},
		})
	}
	return out
}

// responseFormat 强制网关输出 JSON（OpenAI Compatible）。
type responseFormat struct {
	Type string `json:"type"`
}

// Chat 执行一次补全：单次调用有 timeout；429 尊重 Retry-After，
// 5xx 指数退避有限重试；其他 4xx 不重试（FR-015）。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	body := c.buildRequestBody(req, false)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal llm request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := callCtx.Err(); err != nil {
			return nil, err
		}
		resp, err := c.do(callCtx, raw)
		if err != nil {
			// 网络层错误：上下文未取消时有限重试。
			lastErr = err
			if attempt < c.maxRetries {
				if err := sleepCtx(callCtx, c.backoff(attempt)); err != nil {
					return nil, err
				}
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return parseChatResponse(resp)
		}
		apiErr := readAPIError(resp, c.apiKey)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = apiErr
			if attempt < c.maxRetries {
				delay := retryAfterDelay(resp, c.backoff(attempt))
				if err := sleepCtx(callCtx, delay); err != nil {
					return nil, err
				}
			}
			continue
		}
		return nil, apiErr // 4xx（除 429）不重试
	}
	return nil, lastErr
}

// buildRequestBody 组装 Chat Completions 请求体（stream 开关决定流式参数）。
func (c *Client) buildRequestBody(req ChatRequest, stream bool) chatRequestBody {
	if req.Model == "" {
		req.Model = c.model
	}
	body := chatRequestBody{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: c.temperature,
		Stream:      stream,
	}
	if stream {
		// 请求在最后一个 chunk 携带 usage（OpenAI/vLLM 兼容；不支持者忽略该字段）。
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.JSONMode {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if len(req.Thinking) > 0 {
		body.Thinking = maps.Clone(req.Thinking)
	} else if len(c.thinking) > 0 {
		body.Thinking = maps.Clone(c.thinking)
	}
	if len(req.Tools) > 0 {
		body.Tools = toWireTools(req.Tools)
	}
	// 单次请求可覆盖 max_tokens（诊断用它压缩生成长度，避免大模型长时间生成）。
	maxTokens := c.maxTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	if maxTokens > 0 {
		body.MaxTokens = maxTokens
	}
	return body
}

// ChatStream 流式执行一次补全（StreamClient 接口实现）。
// 协议：OpenAI Compatible SSE（data: {...} 逐 chunk，[DONE] 结束）。
// - content 增量经 onDelta 回调（同步、高频）；
// - tool_calls 按 index 累积（id/name 只在首个分片出现，arguments 分片拼接）；
// - usage 取最后一个 chunk（需网关支持 include_usage，缺失时为零值）。
// 错误语义：请求失败或流中断时返回错误；若中断前已收到增量，同时返回已累积的部分响应，
// 调用方（agent）据此决定是否回退非流式。禁用流式（llm.stream=false）时退化为 Chat + 单次回调。
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onDelta func(delta string)) (*ChatResponse, error) {
	return c.ChatStreamEvents(ctx, req, func(ev StreamEvent) {
		if ev.Type == StreamContentDelta && onDelta != nil {
			onDelta(ev.Text)
		}
	})
}

// ChatStreamEvents 流式执行一次补全，并保留内容与思考的语义边界。
func (c *Client) ChatStreamEvents(ctx context.Context, req ChatRequest, onEvent func(StreamEvent)) (*ChatResponse, error) {
	if c.disableStream {
		resp, err := c.Chat(ctx, req)
		if err == nil && resp != nil && resp.Content != "" && onEvent != nil {
			onEvent(StreamEvent{Type: StreamContentDelta, Text: resp.Content})
		}
		return resp, err
	}
	if onEvent == nil {
		return c.Chat(ctx, req)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := json.Marshal(c.buildRequestBody(req, true))
	if err != nil {
		return nil, fmt.Errorf("marshal llm request: %w", err)
	}
	resp, err := c.do(callCtx, raw)
	if err != nil {
		// 流式请求失败时尝试一次重试（覆盖网络抖动/瞬时 5xx）。
		// 重试仍失败则由调用方回退非流式 Chat。
		select {
		case <-callCtx.Done():
			return nil, callCtx.Err()
		default:
		}
		resp, err = c.do(callCtx, raw)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		// 429/5xx 重试一次（部分网关流式路径与普通路径共用限流器）。
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			select {
			case <-callCtx.Done():
				return nil, callCtx.Err()
			case <-time.After(c.retryBase):
			}
			resp, err = c.do(callCtx, raw)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode == http.StatusOK {
				return parseChatStream(resp, onEvent, c.reasoningFields)
			}
		}
		return nil, readAPIError(resp, c.apiKey)
	}
	return parseChatStream(resp, onEvent, c.reasoningFields)
}

// do 发起请求（api_key 只放在 Authorization 头，绝不出现在请求体）。
func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build llm request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return c.http.Do(req)
}

// parseChatResponse 解析 200 响应并提取内容与用量。
func parseChatResponse(resp *http.Response) (*ChatResponse, error) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read llm response: %w", err)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse llm response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm response has no choices")
	}
	toolCalls := parseToolCalls(out.Choices[0].Message.ToolCalls)
	// 思考型模型（如 Qwen3）可能只返回 reasoning_content，content 为空。
	if strings.TrimSpace(out.Choices[0].Message.Content) == "" && len(toolCalls) == 0 {
		if out.Choices[0].Message.ReasoningContent != "" || out.Choices[0].Message.Reasoning != "" {
			return nil, fmt.Errorf("llm response content is empty (reasoning only; max_tokens may be too small)")
		}
		return nil, fmt.Errorf("llm response content is empty")
	}
	return &ChatResponse{
		Content:   out.Choices[0].Message.Content,
		ToolCalls: toolCalls,
		Usage: TokenUsage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			TotalTokens:      out.Usage.TotalTokens,
		},
	}, nil
}

// parseToolCalls 把响应中的 tool_calls（arguments 为字符串）转为 []ToolCall。
func parseToolCalls(calls []struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		tc := ToolCall{ID: c.ID, Name: c.Function.Name}
		if s := strings.TrimSpace(c.Function.Arguments); s != "" {
			tc.Arguments = json.RawMessage(s)
		}
		out = append(out, tc)
	}
	return out
}

// readAPIError 读取错误响应体并脱敏：先显式剔除本客户端 api_key，
// 再跑通用脱敏模式（FR-015：api_key 绝不落错误消息/日志）。
func readAPIError(resp *http.Response, apiKey string) *APIError {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	body := string(raw)
	if apiKey != "" {
		body = strings.ReplaceAll(body, apiKey, "[REDACTED]")
	}
	return &APIError{StatusCode: resp.StatusCode, Body: llmRedactor.Redact(body)}
}

// retryAfterDelay 解析 Retry-After（秒或 HTTP 日期），解析失败用退避值。
func retryAfterDelay(resp *http.Response, fallback time.Duration) time.Duration {
	ra := resp.Header.Get("Retry-After")
	if ra == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(ra); err == nil {
		if secs < 0 {
			return fallback
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(ra); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return fallback
}

// backoff 指数退避 + 抖动：base * 2^n，封顶 10s，±25% 抖动。
func (c *Client) backoff(attempt int) time.Duration {
	d := c.retryBase * time.Duration(1<<uint(attempt))
	if d > 10*time.Second {
		d = 10 * time.Second
	}
	j := time.Duration(rand.Int63n(int64(d/4 + 1)))
	return d - j
}

// sleepCtx 可取消的等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
