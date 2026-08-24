package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// Chat 处理一条用户消息：Agentic Loop（多轮 Tool Calling），返回最终回答与累计用量。
// 单个 Tool 失败不中断 Loop；达到 max_iterations/max_tool_calls/超时 才终止。
func (a *Agent) Chat(ctx context.Context, query string) (string, llm.TokenUsage, error) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	a.SetState(AgentStateRunning)
	defer func() { a.SetState(AgentStateDone) }()

	a.Session.AddMessage(session.Message{Role: "user", Type: "text", Content: query})
	a.Emit(Message{Source: SourceUser, Type: MessageTypeText, Payload: query})
	a.audit(ctx, audit.ActionUserInput, map[string]any{"query": query})

	mode := a.ToolCalling
	if mode != "shim" {
		mode = "native" // auto 起始按 native，不支持时降级 shim
	}
	msgs := []llm.Message{{Role: "system", Content: a.systemPromptFor(mode)}}
	msgs = append(msgs, historyToMessages(a.Session.Messages, a.HistoryTurns)...)

	var usage llm.TokenUsage
	toolCallsTotal := 0
	turnBytes := 0

	for iter := 0; iter < a.MaxIterations; iter++ {
		req := llm.ChatRequest{Messages: msgs}
		if mode == "native" {
			req.Tools = a.Tools.Schemas()
		} else {
			req.JSONMode = true
		}
		a.audit(ctx, audit.ActionLLMRequest, map[string]any{"mode": mode, "messages": len(msgs), "tools": len(req.Tools)})
		resp, err := a.LLM.Chat(ctx, req)
		if err != nil {
			// auto 模式：网关不支持原生 tools 时降级 shim 重试一次。
			if mode == "native" && a.ToolCalling == "auto" && isUnsupportedToolsError(err) {
				mode = "shim"
				msgs[0] = llm.Message{Role: "system", Content: a.systemPromptFor("shim")}
				continue
			}
			a.audit(ctx, audit.ActionLLMError, map[string]any{"error": err.Error()})
			a.Emit(Message{Source: SourceAgent, Type: MessageTypeError, Payload: err.Error()})
			return "", usage, err
		}
		a.audit(ctx, audit.ActionLLMResponse, map[string]any{"usage": resp.Usage, "toolCalls": len(resp.ToolCalls)})
		usage = addUsage(usage, resp.Usage)

		step, err := a.parseStep(mode, resp)
		if err != nil {
			a.Emit(Message{Source: SourceAgent, Type: MessageTypeError, Payload: err.Error()})
			return "", usage, err
		}
		if step.answer != "" {
			a.Session.AddMessage(session.Message{Role: "assistant", Type: "text", Content: step.answer})
			// 最终回答只经 service 的 answer 事件推送一次，避免 UI 重复渲染。
			return step.answer, usage, nil
		}

		// 记录 assistant 消息（native 带 tool_calls，shim 带 JSON）。
		if mode == "native" {
			msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: step.calls})
		} else {
			msgs = append(msgs, llm.Message{Role: "assistant", Content: step.rawJSON})
		}

		for _, call := range step.calls {
			toolCallsTotal++
			if toolCallsTotal > a.MaxToolCalls {
				return "", usage, fmt.Errorf("agent exceeded max tool calls (%d)", a.MaxToolCalls)
			}
			tr := a.executeTool(ctx, call)
			text, truncated := tools.MarshalForLLM(tr, a.ResultMaxBytes)
			turnBytes += len(text)
			budgetExceeded := turnBytes > a.TurnMaxBytes
			toolContent := text
			if budgetExceeded {
				toolContent = "turn context budget exceeded; answer with the evidence you have."
			}
			if mode == "native" {
				msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: toolContent})
				a.Session.AddMessage(session.Message{Role: "tool", Type: "tool-call-response", Name: call.Name, ToolCallID: call.ID, Content: toolContent})
			} else {
				obs := fmt.Sprintf("工具 %s 返回：\n%s", call.Name, toolContent)
				msgs = append(msgs, llm.Message{Role: "user", Content: obs})
				a.Session.AddMessage(session.Message{Role: "user", Type: "tool-call-response", Name: call.Name, Content: obs})
			}
			a.Emit(Message{Source: SourceAgent, Type: MessageTypeToolCallResponse, Payload: map[string]any{
				"name": call.Name, "text": text, "truncated": truncated,
			}})
			// 单轮上下文预算：超限后停止执行本批剩余工具，让 LLM 基于现有证据回答。
			if budgetExceeded {
				break
			}
		}
	}
	return "", usage, fmt.Errorf("agent exceeded max iterations (%d)", a.MaxIterations)
}

// llmStep 是一轮 LLM 输出的解析结果。
type llmStep struct {
	answer  string
	calls   []llm.ToolCall
	rawJSON string // shim 模式的原始 JSON（回传下一轮）
}

// parseStep 解析一轮 LLM 输出：native 读 ToolCalls，shim 解析 JSON action/answer。
func (a *Agent) parseStep(mode string, resp *llm.ChatResponse) (llmStep, error) {
	if mode == "native" {
		if len(resp.ToolCalls) == 0 {
			return llmStep{answer: strings.TrimSpace(resp.Content)}, nil
		}
		return llmStep{calls: resp.ToolCalls}, nil
	}
	out, err := parseShimOutput(resp.Content)
	if err != nil {
		return llmStep{}, err
	}
	step := llmStep{rawJSON: resp.Content}
	if strings.TrimSpace(out.Answer) != "" {
		step.answer = strings.TrimSpace(out.Answer)
		return step, nil
	}
	if out.Action == nil || strings.TrimSpace(out.Action.Name) == "" {
		return llmStep{}, fmt.Errorf("shim output has neither answer nor action: %s", resp.Content)
	}
	argsRaw, err := json.Marshal(out.Action.Arguments)
	if err != nil {
		return llmStep{}, fmt.Errorf("marshal shim arguments: %w", err)
	}
	step.calls = []llm.ToolCall{{ID: "call_shim", Name: out.Action.Name, Arguments: argsRaw}}
	return step, nil
}

// executeTool 校验并执行单个工具调用；写操作先走审批（Approver）。
// 错误以 ToolResult 回传 LLM，不中断 Loop。
func (a *Agent) executeTool(ctx context.Context, call llm.ToolCall) tools.ToolResult {
	t, ok := a.Tools.Lookup(call.Name)
	if !ok {
		return tools.ResultError("unknown tool: " + call.Name)
	}
	var argsView map[string]any
	_ = json.Unmarshal(call.Arguments, &argsView)
	a.Emit(Message{Source: SourceAgent, Type: MessageTypeToolCallRequest, Payload: ToolCallEvent{
		Name: call.Name, Arguments: argsView,
	}})
	a.audit(ctx, audit.ActionToolCallRequest, map[string]any{"name": call.Name, "arguments": argsView})

	if !t.IsReadOnly(call.Arguments) {
		key := call.Name + ":" + string(call.Arguments)
		if a.Approver != nil && !a.Session.Approved(key) {
			a.audit(ctx, audit.ActionApprovalRequested, map[string]any{"name": call.Name, "risk": t.Risk(call.Arguments)})
			prompt := fmt.Sprintf("将执行以下操作（风险等级：%s）：", t.Risk(call.Arguments))
			if d, ok := t.(tools.CommandDescriber); ok {
				if cmd, change := d.Describe(call.Arguments); cmd != "" {
					prompt += "\n\n命令：\n" + cmd
					if change != "" {
						prompt += "\n\n变更：\n" + change
					}
				}
			} else {
				prompt += "\n\n工具：" + call.Name + "\n参数：" + string(call.Arguments)
			}
			prompt += "\n\n是否执行？"
			choice, err := a.Approver.Confirm(ctx, prompt, []UserChoiceOption{
				{Label: "是", Value: ChoiceYes},
				{Label: "是，本次会话不再询问", Value: ChoiceYesNoAskAgain},
				{Label: "否", Value: ChoiceNo},
			})
			if err != nil {
				a.audit(ctx, audit.ActionApprovalDenied, map[string]any{"name": call.Name, "reason": err.Error()})
				return tools.ResultError("approval error: " + err.Error())
			}
			switch choice {
			case ChoiceYesNoAskAgain:
				a.audit(ctx, audit.ActionApprovalGranted, map[string]any{"name": call.Name, "choice": choice})
				a.Session.AddApproved(key)
			case ChoiceYes:
				a.audit(ctx, audit.ActionApprovalGranted, map[string]any{"name": call.Name, "choice": choice})
			default:
				a.audit(ctx, audit.ActionApprovalDenied, map[string]any{"name": call.Name, "choice": choice})
				return tools.ResultError("user declined: " + call.Name)
			}
		} else {
			a.audit(ctx, audit.ActionApprovalSkipped, map[string]any{"name": call.Name})
		}
	}
	tr, err := t.Execute(ctx, call.Arguments)
	if err != nil {
		tr = tools.ResultError(err.Error())
	}
	hasErr := false
	if m, ok := tr.Data.(map[string]any); ok {
		_, hasErr = m["error"]
	}
	a.audit(ctx, audit.ActionToolCallResponse, map[string]any{
		"name": call.Name, "truncated": tr.Truncated, "error": hasErr,
	})
	return tr
}

func addUsage(a, b llm.TokenUsage) llm.TokenUsage {
	return llm.TokenUsage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
}
