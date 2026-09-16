package llm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// streamToolCallDelta 是 tool call 在流式 delta 中的增量分片。
type streamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamChunk 是 SSE 流中的一个增量块（OpenAI Compatible stream 协议）。
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			Reasoning        string                `json:"reasoning"`
			Thinking         string                `json:"thinking"`
			ToolCalls        []streamToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// streamAccumulator 把流式 chunk 累积成与非流式同构的 ChatResponse。
type streamAccumulator struct {
	content strings.Builder
	order   []int // tool call 出现顺序（index 序）
	calls   map[int]*ToolCall
	usage   TokenUsage
}

func newStreamAccumulator() *streamAccumulator {
	return &streamAccumulator{calls: map[int]*ToolCall{}}
}

// addToolCall 合并一个 tool call 增量：id/name 只在首个分片出现，arguments 分片拼接。
func (a *streamAccumulator) addToolCall(tc streamToolCallDelta) {
	existing, ok := a.calls[tc.Index]
	if !ok {
		a.calls[tc.Index] = &ToolCall{ID: tc.ID, Name: tc.Function.Name}
		if args := tc.Function.Arguments; args != "" {
			a.calls[tc.Index].Arguments = json.RawMessage(args)
		}
		a.order = append(a.order, tc.Index)
		return
	}
	if tc.ID != "" {
		existing.ID = tc.ID
	}
	if tc.Function.Name != "" {
		existing.Name = tc.Function.Name
	}
	if tc.Function.Arguments != "" {
		existing.Arguments = json.RawMessage(string(existing.Arguments) + tc.Function.Arguments)
	}
}

// response 返回当前累积结果（部分累积也有效，用于流中断时返回部分内容）。
func (a *streamAccumulator) response() *ChatResponse {
	out := &ChatResponse{Content: a.content.String(), Usage: a.usage}
	for _, idx := range a.order {
		call := *a.calls[idx]
		call.Arguments = normalizeToolArguments(call.Arguments)
		out.ToolCalls = append(out.ToolCalls, call)
	}
	return out
}

// normalizeToolArguments 处理部分 OpenAI 兼容网关对流式 arguments 的非标准包装。
// 只接受可验证的 JSON；无法安全恢复时原样返回，让工具层明确报告参数错误。
func normalizeToolArguments(raw json.RawMessage) json.RawMessage {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return json.RawMessage(text)
	}

	// 某些网关会把 JSON 参数再编码成 JSON 字符串（以引号开头）。
	if strings.HasPrefix(text, `"`) {
		var encoded string
		if json.Unmarshal([]byte(text), &encoded) == nil && json.Valid([]byte(encoded)) {
			return json.RawMessage(encoded)
		}
	}

	// 已经是合法 JSON（对象或数组），直接返回。
	if json.Valid([]byte(text)) {
		return json.RawMessage(text)
	}

	// 兼容对象后附加空引号噪声，例如 `{}""`。只在首个 JSON 值
	// 完整解析且尾部仅由引号组成时恢复，避免吞掉真实的额外参数。
	decoder := json.NewDecoder(strings.NewReader(text))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil || !json.Valid(value) {
		return json.RawMessage(text)
	}
	offset := decoder.InputOffset()
	trailing := strings.TrimSpace(text[offset:])
	if trailing != "" && strings.Trim(trailing, `"`) != "" {
		return json.RawMessage(text)
	}
	if trailing != "" && len(trailing) > 2 {
		return json.RawMessage(text)
	}
	return value
}

// parseChatStream 解析 200 流式响应：逐行读 SSE data 行，累积 content/tool_calls/usage，
// content 增量同步回调 onDelta。流中断（网络错误/非法 chunk）时返回已累积响应 + 错误。
func parseChatStream(resp *http.Response, onEvent func(StreamEvent), reasoningFields []string) (*ChatResponse, error) {
	defer resp.Body.Close()
	acc := newStreamAccumulator()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // 空行（SSE 事件分隔）或注释/心跳
		}
		if !strings.HasPrefix(line, "data:") {
			continue // 忽略非 data 行（event:/id: 等）
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return acc.response(), fmt.Errorf("parse llm stream chunk: %w", err)
		}
		if chunk.Usage != nil {
			acc.usage = TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue // include_usage 的最终 chunk：choices 为空、只带 usage
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			acc.content.WriteString(ch.Delta.Content)
			if onEvent != nil {
				onEvent(StreamEvent{Type: StreamContentDelta, Text: ch.Delta.Content})
			}
		}
		reasoning := firstReasoning(ch.Delta.ReasoningContent, ch.Delta.Reasoning, ch.Delta.Thinking, reasoningFields)
		if reasoning != "" && onEvent != nil {
			onEvent(StreamEvent{Type: StreamReasoningDelta, Text: reasoning})
		}
		for _, tc := range chunk.Choices[0].Delta.ToolCalls {
			acc.addToolCall(tc)
		}
	}
	if err := sc.Err(); err != nil {
		return acc.response(), fmt.Errorf("read llm stream: %w", err)
	}
	out := acc.response()
	// 空响应校验与非流式一致（reasoning-only 视为内容为空）。
	if strings.TrimSpace(out.Content) == "" && len(out.ToolCalls) == 0 {
		return out, fmt.Errorf("llm response content is empty")
	}
	return out, nil
}

func firstReasoning(content, reasoning, thinking string, fields []string) string {
	values := map[string]string{
		"reasoning_content": content,
		"reasoning":         reasoning,
		"thinking":          thinking,
	}
	if len(fields) == 0 {
		fields = []string{"reasoning_content", "reasoning", "thinking"}
	}
	for _, field := range fields {
		if text := values[field]; text != "" {
			return text
		}
	}
	return ""
}
