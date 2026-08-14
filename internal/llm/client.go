package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Endpoint    string // 例如 http://localhost:8000/v1
	APIKey      string
	Model       string
	Temperature float64
	MaxTokens   int
	Timeout     time.Duration // 单次调用超时（默认 120s）
	MaxRetries  int           // 额外重试次数（默认 2）
	HTTPClient  *http.Client
}

// Client 是 OpenAI Compatible Chat Completions 的实现。
type Client struct {
	endpoint    string
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
	timeout     time.Duration
	maxRetries  int
	http        *http.Client
	retryBase   time.Duration // 退避基数（测试可调）
}

// New 创建客户端并应用默认值。
func New(opts Options) *Client {
	c := &Client{
		endpoint:    strings.TrimRight(opts.Endpoint, "/"),
		apiKey:      opts.APIKey,
		model:       opts.Model,
		temperature: opts.Temperature,
		maxTokens:   opts.MaxTokens,
		timeout:     opts.Timeout,
		maxRetries:  opts.MaxRetries,
		http:        opts.HTTPClient,
		retryBase:   200 * time.Millisecond,
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
	Model              string          `json:"model"`
	Messages           []Message       `json:"messages"`
	Temperature        float64         `json:"temperature"`
	MaxTokens          int             `json:"max_tokens,omitempty"`
	Stream             bool            `json:"stream"`
	ResponseFormat     *responseFormat `json:"response_format,omitempty"`
	ChatTemplateKwargs map[string]any  `json:"chat_template_kwargs,omitempty"`
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

	if req.Model == "" {
		req.Model = c.model
	}
	body := chatRequestBody{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: c.temperature,
		Stream:      false,
	}
	if req.JSONMode {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if req.DisableThinking {
		body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	// 单次请求可覆盖 max_tokens（诊断用它压缩生成长度，避免大模型长时间生成）。
	maxTokens := c.maxTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	if maxTokens > 0 {
		body.MaxTokens = maxTokens
	}
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
	// 思考型模型（如 Qwen3）可能只返回 reasoning_content，content 为空。
	if strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		if out.Choices[0].Message.ReasoningContent != "" {
			return nil, fmt.Errorf("llm response content is empty (reasoning only; max_tokens may be too small)")
		}
		return nil, fmt.Errorf("llm response content is empty")
	}
	return &ChatResponse{
		Content: out.Choices[0].Message.Content,
		Usage: TokenUsage{
			PromptTokens:     out.Usage.PromptTokens,
			CompletionTokens: out.Usage.CompletionTokens,
			TotalTokens:      out.Usage.TotalTokens,
		},
	}, nil
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
