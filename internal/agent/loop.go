package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
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

	// P1.8: 记录本轮运行轨迹（pending → running），供 service 层持久化回放。
	a.stepSeq = 0
	a.turn = &session.Turn{
		ID:        newID(),
		SessionID: a.Session.ID,
		UserID:    a.TurnUserID,
		Status:    session.TurnPending,
		Input:     query,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = a.turn.Start()
	a.trace().TurnStarted(ctx, a.turn)
	// 任何退出路径都要广播一次 Turn 终态；显式 Complete/Fail 的路径不受影响。
	defer func() {
		if a.turn.Status == session.TurnRunning || a.turn.Status == session.TurnWaiting {
			_ = a.turn.Fail("agent run ended without terminal turn state")
		}
		a.trace().TurnUpdated(ctx, a.turn)
	}()

	mode := a.ToolCalling
	if mode != "shim" {
		mode = "native" // auto 起始按 native，不支持时降级 shim
	}
	msgs := []llm.Message{{Role: "system", Content: a.systemPromptFor(mode)}}
	msgs = append(msgs, historyToMessages(a.Session.Messages, a.HistoryTurns)...)

	var usage llm.TokenUsage
	toolCallsTotal := 0
	turnBytes := 0
	failedToolCalls := map[string]int{}

	for iter := 0; iter < a.MaxIterations; iter++ {
		req := llm.ChatRequest{Messages: msgs}
		if mode == "native" {
			req.Tools = a.Tools.Schemas()
		} else {
			req.JSONMode = true
		}
		a.audit(ctx, audit.ActionLLMRequest, map[string]any{"mode": mode, "messages": len(msgs), "tools": len(req.Tools)})
		resp, streamed, err := a.chatOnce(ctx, mode, req)
		if err != nil {
			// auto 模式：网关不支持原生 tools 时降级 shim 重试一次。
			if mode == "native" && a.ToolCalling == "auto" && isUnsupportedToolsError(err) {
				mode = "shim"
				msgs[0] = llm.Message{Role: "system", Content: a.systemPromptFor("shim")}
				continue
			}
			a.audit(ctx, audit.ActionLLMError, map[string]any{"error": err.Error()})
			a.EmitCritical(Message{Source: SourceAgent, Type: MessageTypeError, Payload: err.Error()})
			a.turn.Token = toSessionUsage(usage)
			_ = a.turn.Fail(err.Error())
			return "", usage, err
		}
		a.audit(ctx, audit.ActionLLMResponse, map[string]any{"usage": resp.Usage, "toolCalls": len(resp.ToolCalls), "stream": streamed})
		usage = addUsage(usage, resp.Usage)

		// P1.8: 记录本轮模型思考 Step（有工具调用时记工具名，否则记正文摘要）。
		reason := a.beginStep(ctx, session.StepReason, "", "")

		step, err := a.parseStep(mode, resp)
		if err != nil {
			a.EmitCritical(Message{Source: SourceAgent, Type: MessageTypeError, Payload: err.Error()})
			a.completeStep(ctx, reason, false, err.Error())
			a.turn.Token = toSessionUsage(usage)
			_ = a.turn.Fail(err.Error())
			return "", usage, err
		}
		if step.answer != "" {
			a.Session.AddMessage(session.Message{Role: "assistant", Type: "text", Content: step.answer})
			a.completeStep(ctx, reason, true, summarizeReason(mode, resp))
			// P1.8: 最终回答输出 Step。
			response := a.beginStep(ctx, session.StepResponse, "", "")
			a.completeStep(ctx, response, true, truncateText(step.answer, 240))
			_ = a.turn.Complete(step.answer)
			a.turn.Token = toSessionUsage(usage)
			// 最终回答只经 service 的 answer 事件推送一次，避免 UI 重复渲染。
			return step.answer, usage, nil
		}
		a.completeStep(ctx, reason, true, summarizeReason(mode, resp))

		// 记录 assistant 消息（native 带 tool_calls，shim 带 JSON）。
		if mode == "native" {
			msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: step.calls})
		} else {
			msgs = append(msgs, llm.Message{Role: "assistant", Content: step.rawJSON})
		}

		// 批量审批：native 模式下一轮返回 2+ 写工具调用时，合并为一次确认。
		batchApproved, batchDeclined := a.batchApprove(ctx, step.calls)

		for _, call := range step.calls {
			toolCallsTotal++
			if toolCallsTotal > a.MaxToolCalls {
				err := fmt.Errorf("agent exceeded max tool calls (%d)", a.MaxToolCalls)
				a.turn.Token = toSessionUsage(usage)
				_ = a.turn.Fail(err.Error())
				return "", usage, err
			}
			failureKey := toolCallFailureKey(call)
			var tr tools.ToolResult
			toolExecuted := false
			// P1.8: 工具调用 Step（参数在落库前由 Store 统一脱敏）。
			toolStep := a.beginStep(ctx, session.StepTool, call.Name, string(call.Arguments))
			if failedToolCalls[failureKey] > 0 {
				// 同一工具、同一参数已经失败过，禁止原样重试；
				// 让 LLM 更换参数或选择替代工具，避免耗尽工具调用预算。
				tr = tools.ResultError("same tool call already failed; do not retry the identical call, change the arguments or choose another tool")
			} else if batchDeclined[call.ID] {
				// 批量拒绝：直接返回拒绝结果，不走 executeTool。
				key := declineKey(call.Name, call.Arguments)
				total := a.Session.Declined(key)
				tr = tools.ResultError(a.formatDeclinedMsg(call.Name, key, total))
			} else {
				if tool, ok := a.Tools.Lookup(call.Name); ok {
					// 只读工具没有审批短路，错误可以安全按签名熔断；
					// 写工具的审批拒绝仍由原有拒绝计数处理。
					toolExecuted = tool.IsReadOnly(call.Arguments)
				}
				tr = a.executeTool(ctx, call, batchApproved[call.ID])
			}
			ok := toolResultOK(tr)
			a.completeStep(ctx, toolStep, ok, truncateText(tr.Text, 240))
			// P1.8: 工具结果观察 Step（成功 done / 失败 failed）。
			observe := a.beginStep(ctx, session.StepObserve, call.Name, "")
			a.completeStep(ctx, observe, ok, truncateText(tr.Text, 240))
			if toolExecuted && toolResultHasError(tr) {
				failedToolCalls[failureKey]++
			}
			text, truncated := tools.MarshalForLLM(tr, a.ResultMaxBytes)
			turnBytes += len(text)
			budgetExceeded := turnBytes > a.TurnMaxBytes
			toolContent := text
			if budgetExceeded {
				toolContent = "turn context budget exceeded; answer with the evidence you have."
			}
			if mode == "native" {
				msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: toolContent})
				a.Session.AddMessage(session.Message{Role: "tool", Type: "tool-call-response", Name: call.Name, ToolCallID: call.ID, Content: toolContent, Arguments: call.Arguments, Data: tr.Data, Ok: &ok})
			} else {
				obs := fmt.Sprintf("工具 %s 返回：\n%s", call.Name, toolContent)
				msgs = append(msgs, llm.Message{Role: "user", Content: obs})
				a.Session.AddMessage(session.Message{Role: "user", Type: "tool-call-response", Name: call.Name, Content: obs, Arguments: call.Arguments, Data: tr.Data, Ok: &ok})
			}
			a.EmitCritical(Message{Source: SourceAgent, Type: MessageTypeToolCallResponse, Payload: map[string]any{
				"name": call.Name, "text": text, "truncated": truncated, "ok": ok, "summary": tr.Text, "data": tr.Data,
			}})
			// 写工具失败/被拒：停止执行本批剩余写调用，把决策交回模型。
			if !toolExecuted && !ok {
				break
			}
			// 单轮上下文预算：超限后停止执行本批剩余工具，让 LLM 基于现有证据回答。
			if budgetExceeded {
				break
			}
		}
	}
	err := fmt.Errorf("agent exceeded max iterations (%d)", a.MaxIterations)
	a.turn.Token = toSessionUsage(usage)
	_ = a.turn.Fail(err.Error())
	return "", usage, err
}

func toolCallFailureKey(call llm.ToolCall) string {
	return call.Name + "\x00" + strings.TrimSpace(string(call.Arguments))
}

func toolResultHasError(result tools.ToolResult) bool {
	data, ok := result.Data.(map[string]any)
	if !ok {
		return false
	}
	_, ok = data["error"]
	return ok
}

// toolResultOK 判断工具执行是否成功：无 error 键，且排除“环境缺 kubectl 二进制”
// 场景（该场景无 error 键但确实失败，前端若只看 error 会误判成功）。
func toolResultOK(result tools.ToolResult) bool {
	if toolResultHasError(result) {
		return false
	}
	if data, ok := result.Data.(map[string]any); ok {
		if kind, _ := data["kind"].(string); kind == "kubectl_binary_missing" {
			return false
		}
	}
	return true
}

// chatOnce 调用一次 LLM 补全（Agent 循环唯一入口）：
//   - native 模式且客户端支持流式（llm.StreamClient）→ ChatStream，
//     content 增量实时 Emit 到消息总线（Source=SourceModel，UI 逐 token 渲染）；
//   - shim 模式（内部 JSON 协议）不流式，避免向 UI 泄漏中间 JSON；
//   - 流式失败且尚未发出任何增量 → 回退非流式 Chat（复用其 429/5xx 重试），
//     保证兼容不支持 stream 的网关。
//
// 返回 streamed 标记本次是否走了流式路径（供审计）。
func (a *Agent) chatOnce(ctx context.Context, mode string, req llm.ChatRequest) (*llm.ChatResponse, bool, error) {
	if typed, ok := a.LLM.(llm.TypedStreamClient); mode == "native" && ok {
		return a.chatOnceTyped(ctx, typed, req)
	}
	sc, canStream := a.LLM.(llm.StreamClient)
	if mode != "native" || !canStream {
		resp, err := a.LLM.Chat(ctx, req)
		return resp, false, err
	}
	received := false
	var pending strings.Builder
	resp, err := sc.ChatStream(ctx, req, func(delta string) {
		if delta == "" {
			return
		}
		received = true
		pending.WriteString(delta)
	})
	if err != nil && !received {
		// 流式失败且未发出任何增量：回退非流式 Chat（自带重试），避免双倍 LLM 调用。
		a.audit(ctx, "stream_fallback", map[string]any{"reason": err.Error()})
		resp, err = a.LLM.Chat(ctx, req)
		return resp, false, err
	}
	if err != nil && received {
		// 流式中途失败但已发出增量：返回已累积的部分响应（可能有损），不再重试避免重复输出。
		a.audit(ctx, "stream_partial", map[string]any{"reason": err.Error()})
	}
	if resp != nil && len(resp.ToolCalls) == 0 && pending.Len() > 0 {
		a.Emit(Message{Source: SourceModel, Type: MessageTypeText, Payload: pending.String()})
	}
	return resp, true, err
}

func (a *Agent) chatOnceTyped(ctx context.Context, sc llm.TypedStreamClient, req llm.ChatRequest) (*llm.ChatResponse, bool, error) {
	received := false
	var pending strings.Builder
	resp, err := sc.ChatStreamEvents(ctx, req, func(ev llm.StreamEvent) {
		switch ev.Type {
		case llm.StreamReasoningDelta:
			if ev.Text == "" {
				return
			}
			a.Emit(Message{Source: SourceModel, Type: MessageTypeReasoningDelta, Payload: ev.Text})
		case llm.StreamContentDelta:
			if ev.Text == "" {
				return
			}
			received = true
			pending.WriteString(ev.Text)
		}
	})
	if err != nil && !received {
		a.audit(ctx, "stream_fallback", map[string]any{"reason": err.Error()})
		resp, err = a.LLM.Chat(ctx, req)
		return resp, false, err
	}
	if err != nil && received {
		a.audit(ctx, "stream_partial", map[string]any{"reason": err.Error()})
	}
	if resp != nil && len(resp.ToolCalls) == 0 && pending.Len() > 0 {
		a.Emit(Message{Source: SourceModel, Type: MessageTypeText, Payload: pending.String()})
	}
	return resp, true, err
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

// maxDeclinesBeforeBlock 是同签名写操作被用户拒绝达到多少次后，直接硬拦截并要求换方案。
// 阈值设置为 2：第一次"用户可能在犹豫，给出替代方案"；第二次"用户明确反对，绝对不能再问同一个动作"。
const maxDeclinesBeforeBlock = 2

// declineKey 构造拒绝计数 key，对 kubectl 工具做命令归一化：
// 忽略 flag 顺序、忽略 --force/--grace-period/2>/dev/null/|| true 等修饰性差异。
// 防止 LLM 用"加个 --force"绕过拒绝闸门。
// 结构化工具的 key 直接用原始 JSON arguments（同操作 = 同 JSON = 同 key）。
func declineKey(toolName string, args json.RawMessage) string {
	if toolName != "kubectl" {
		return toolName + ":" + string(args)
	}
	// 提取 command 字段。
	var raw struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &raw); err != nil || raw.Command == "" {
		return toolName + ":" + string(args)
	}
	return toolName + ":" + normalizeKubectlCommand(raw.Command)
}

// normalizeKubectlCommand 归一化 kubectl 命令字符串：
// 提取 verb + resource + name + namespace，忽略 flag 顺序和修饰性 flag。
var modifierFlags = map[string]bool{
	"--force": true, "--grace-period": true, "--timeout": true,
	"--now": true, "--wait": true, "--cascade": true,
	"--dry-run": true, "--server-side": true, "--field-manager": true,
}

func normalizeKubectlCommand(cmd string) string {
	tokens := strings.Fields(cmd)
	if len(tokens) < 2 {
		return cmd
	}
	// 提取核心部分：verb + resource + name + --namespace/-n
	var verb, resource, name, ns string
	var modifiers []string
	skipNext := false
	for i := 1; i < len(tokens); i++ { // skip "kubectl"
		tok := tokens[i]
		if skipNext {
			skipNext = false
			continue
		}
		if tok == "-n" || tok == "--namespace" {
			if i+1 < len(tokens) {
				ns = tokens[i+1]
			}
			skipNext = true
			continue
		}
		if strings.HasPrefix(tok, "--namespace=") {
			ns = tok[len("--namespace="):]
			continue
		}
		if strings.HasPrefix(tok, "-n=") {
			ns = tok[len("-n="):]
			continue
		}
		if modifierFlags[tok] || strings.HasPrefix(tok, "--force") ||
			strings.HasPrefix(tok, "--grace-period") || strings.HasPrefix(tok, "--timeout") {
			if strings.Contains(tok, "=") {
				modifiers = append(modifiers, tok)
				continue
			}
			skipNext = true
			continue
		}
		if verb == "" && !strings.HasPrefix(tok, "-") {
			verb = tok
			continue
		}
		if resource == "" && !strings.HasPrefix(tok, "-") {
			resource = tok
			continue
		}
		if name == "" && !strings.HasPrefix(tok, "-") {
			name = tok
			continue
		}
		// 其他 flags 收集（有序）
		modifiers = append(modifiers, tok)
	}
	parts := []string{verb, resource}
	if name != "" {
		parts = append(parts, name)
	}
	if ns != "" {
		parts = append(parts, "-n", ns)
	}
	// 修饰性 flag 不纳入 key（排序后忽略）
	_ = modifiers
	return strings.Join(parts, " ")
}

// batchApprove 在 native 模式下收集一轮 LLM 返回的所有待审批调用，
// 如果有 2+ 个需要审批的调用，合并为一次确认。
// 返回 approved/declined 两个 set（以 call.ID 为 key）。
// approved 中的 call 在 executeTool 中跳过单独审批；
// declined 中的 call 在 executeTool 前直接返回拒绝结果。
// 借鉴 kubectl-ai conversation.go L757-794 的批量收集 + 统一审批。
func (a *Agent) toolHasShell(t tools.Tool, args json.RawMessage) bool {
	if s, ok := t.(tools.ShellSyntaxAware); ok {
		return s.HasShellSyntax(args)
	}
	return false
}

// approvalDetail 是审批决策结果，附带 Authorization Grant 命中信息（仅审计用）。
type approvalDetail struct {
	decision    approval.Decision
	grant       *approval.Grant
	matchReason string
	guard       *approval.GuardResult
}

// guardFor 取工具自己给出的门禁要素并交给 Code Guard（三期 R4.2）。
// 未实现 tools.GuardPlanner 的工具返回 nil：它们的闸门仍是
// IsReadOnly / Risk / IsForbiddenCommand 这套二期既有链路。
func (a *Agent) guardFor(t tools.Tool, args json.RawMessage) *approval.GuardResult {
	gp, ok := t.(tools.GuardPlanner)
	if !ok {
		return nil
	}
	req, ok := gp.GuardRequest(args)
	if !ok {
		return nil
	}
	result := approval.CodeGuard(req)
	return &result
}

// grantRequestFor 构建当前工具调用的授权请求上下文。
// kubectl 工具从 command 文本提取 operation/kind/name/namespace；
// 结构化工具以工具名作为 operation，并从参数中取 kind/name/namespace。
// 含 shell 语法或无法可靠解析的命令标记 Shell=true，永不出现在授权匹配里。
func (a *Agent) grantRequestFor(t tools.Tool, args json.RawMessage) approval.GrantRequest {
	req := approval.GrantRequest{
		Cluster: a.Session.Cluster,
		Risk:    t.Risk(args),
	}
	if a.toolHasShell(t, args) {
		req.Shell = true
	}
	req.Operation = t.Name()
	if req.Operation != "kubectl" {
		var argsView map[string]any
		_ = json.Unmarshal(args, &argsView)
		if v, ok := argsView["kind"].(string); ok {
			req.Kind = v
		}
		if v, ok := argsView["name"].(string); ok {
			req.Name = v
		}
		if v, ok := argsView["namespace"].(string); ok && v != "" {
			req.Namespace = v
		}
		if req.Namespace == "" {
			req.Namespace = a.Session.Namespace
		}
		// 工具自己声明授权目标时以工具为准：入参里没有 kind/name 的工具
		// （如 create_resource 从 manifest 里解析）若留空 Kind，会让一条 MEDIUM 授权
		// 覆盖同风险档的任意调用；无法收敛到单一目标时直接禁用授权。
		if gt, ok := t.(tools.GrantTargeter); ok {
			kind, name, namespace, okTarget := gt.GrantTarget(args)
			if !okTarget {
				req.NoGrant = true
				return req
			}
			req.Kind, req.Name, req.Namespace = kind, name, namespace
			if req.Namespace == "" {
				req.Namespace = a.Session.Namespace
			}
		}
		return req
	}
	var raw struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &raw); err != nil || raw.Command == "" {
		req.Shell = true
		return req
	}
	target := approval.ParseCommandTarget(raw.Command)
	if target.Operation == "" {
		req.Shell = true // 无法可靠解析 → 排除授权匹配
		return req
	}
	req.Operation = target.Operation
	req.Kind = target.Kind
	req.Name = target.Name
	req.Namespace = target.Namespace
	if req.Namespace == "" {
		req.Namespace = a.Session.Namespace
	}
	return req
}

// matchedGrant 返回会话中第一条匹配当前调用的结构化授权记录，没有则 nil。
func (a *Agent) matchedGrant(t tools.Tool, args json.RawMessage) *approval.Grant {
	if a.Session == nil {
		return nil
	}
	return a.Session.FindGrant(a.grantRequestFor(t, args))
}

// approvalDecisionDetail 先走统一 ApprovalPolicy，再叠加 Phase D 的
// Authorization Grant 命中判断：仅当策略要求审批且会话中有匹配授权时
// 才降级为 auto。Shell / HIGH / CRITICAL 永不匹配授权。
func (a *Agent) approvalDecisionDetail(t tools.Tool, args json.RawMessage) approvalDetail {
	in := approval.Input{ReadOnly: t.IsReadOnly(args), Risk: t.Risk(args)}
	if a.toolHasShell(t, args) {
		in.Shell = true
	}
	guard := a.guardFor(t, args)
	if guard != nil {
		in.Denied = guard.Denied()
	}
	if a.ApprovalPolicy == nil {
		a.ApprovalPolicy = &approval.Policy{Mode: approval.ModeStrict, RequireApproval: true}
	}
	d := approvalDetail{decision: a.ApprovalPolicy.Evaluate(in), guard: guard}
	if d.decision != approval.DecisionRequireApproval {
		d.matchReason = "policy_not_require"
		return d
	}
	if g := a.matchedGrant(t, args); g != nil {
		d.decision = approval.DecisionAuto
		d.grant = g
		d.matchReason = "matched_grant"
		return d
	}
	d.matchReason = "no_grant"
	return d
}

// approvalDecision 保留旧签名供批量审批复用；内部已叠加授权匹配。
func (a *Agent) approvalDecision(t tools.Tool, args json.RawMessage) approval.Decision {
	return a.approvalDecisionDetail(t, args).decision
}

func (a *Agent) batchApprove(ctx context.Context, calls []llm.ToolCall) (approved, declined map[string]bool) {
	approved = map[string]bool{}
	declined = map[string]bool{}
	if a.Approver == nil || len(calls) < 2 {
		return // 无 approver 或少于 2 个调用，走个体审批
	}
	// 收集需要审批的调用（非只读 + 未授权 + 未被硬拦截，或 shell 只读）。
	type pending struct {
		call llm.ToolCall
		tool tools.Tool
		key  string
	}
	var pendingWrites []pending
	for _, call := range calls {
		t, ok := a.Tools.Lookup(call.Name)
		if !ok || a.approvalDecision(t, call.Arguments) != approval.DecisionRequireApproval {
			continue
		}
		key := declineKey(call.Name, call.Arguments)
		if a.Session.Approved(key) {
			continue
		}
		if a.Session.Declined(key) >= maxDeclinesBeforeBlock {
			continue // 已被硬拦截，executeTool 会处理
		}
		pendingWrites = append(pendingWrites, pending{call: call, tool: t, key: key})
	}
	if len(pendingWrites) < 2 {
		return // 0 或 1 个调用需要审批，走个体审批
	}
	// 构建合并审批面板。
	var sb strings.Builder
	sb.WriteString("将执行以下操作（请逐条确认）：")
	for i, pw := range pendingWrites {
		risk := pw.tool.Risk(pw.call.Arguments)
		fmt.Fprintf(&sb, "\n\n%d. 风险等级：%s", i+1, risk)
		if d, ok := pw.tool.(tools.CommandDescriber); ok {
			if cmd, change := d.Describe(pw.call.Arguments); cmd != "" {
				sb.WriteString("\n   命令：" + cmd)
				if change != "" {
					sb.WriteString("\n   变更：" + change)
				}
			}
		} else {
			sb.WriteString("\n   工具：" + pw.call.Name)
			sb.WriteString("\n   参数：" + string(pw.call.Arguments))
		}
	}
	sb.WriteString("\n\n是否全部执行？")
	choice, err := a.Approver.Confirm(ctx, sb.String(), []UserChoiceOption{
		{Label: "是", Value: ChoiceYes},
		{Label: "是，本次会话不再询问", Value: ChoiceYesNoAskAgain},
		{Label: "否", Value: ChoiceNo},
	})
	if err != nil {
		// Approver 出错：不预授权，走个体审批兜底。
		return
	}
	switch choice {
	case ChoiceYesNoAskAgain:
		for _, pw := range pendingWrites {
			a.Session.AddApproved(pw.key)
			approved[pw.call.ID] = true
			grantAudit := map[string]any{"name": pw.call.Name, "choice": choice, "batch": true}
			if g, ok := approval.NewGrant(a.grantRequestFor(pw.tool, pw.call.Arguments)); ok {
				a.Session.AddGrant(g)
				grantAudit["grant_id"] = g.ID
				grantAudit["operation"] = g.Operation
				grantAudit["kind"] = g.Kind
				grantAudit["name"] = g.Name
				grantAudit["namespace"] = g.Namespace
			}
			a.audit(ctx, audit.ActionApprovalGranted, grantAudit)
		}
	case ChoiceYes:
		for _, pw := range pendingWrites {
			approved[pw.call.ID] = true
			a.audit(ctx, audit.ActionApprovalGranted, map[string]any{
				"name": pw.call.Name, "choice": choice, "batch": true,
			})
		}
	default:
		for _, pw := range pendingWrites {
			total := a.Session.AddDeclined(pw.key)
			a.Session.RemoveGrantMatching(a.grantRequestFor(pw.tool, pw.call.Arguments))
			declined[pw.call.ID] = true
			a.audit(ctx, audit.ActionApprovalDenied, map[string]any{
				"name": pw.call.Name, "declineCount": total, "batch": true,
			})
		}
	}
	return approved, declined
}

// executeTool 校验并执行单个工具调用；写操作先走审批（Approver）。
// batchApproved=true 时跳过个体审批（已被批量审批预授权）。
// 错误以 ToolResult 回传 LLM，不中断 Loop。
func (a *Agent) executeTool(ctx context.Context, call llm.ToolCall, batchApproved bool) tools.ToolResult {
	t, ok := a.Tools.Lookup(call.Name)
	if !ok {
		return tools.ResultError("unknown tool: " + call.Name)
	}
	var argsView map[string]any
	_ = json.Unmarshal(call.Arguments, &argsView)
	a.EmitCritical(Message{Source: SourceAgent, Type: MessageTypeToolCallRequest, Payload: ToolCallEvent{
		Name: call.Name, Arguments: argsView, Risk: string(t.Risk(call.Arguments)),
	}})
	a.audit(ctx, audit.ActionToolCallRequest, map[string]any{"name": call.Name, "arguments": argsView})

	detail := a.approvalDecisionDetail(t, call.Arguments)
	decision := detail.decision
	mode := approval.ModeStrict
	if a.ApprovalPolicy != nil {
		mode = a.ApprovalPolicy.Mode
	}
	decisionAudit := map[string]any{
		"name":          call.Name,
		"approval_mode": mode,
		"risk":          t.Risk(call.Arguments),
		"readonly":      t.IsReadOnly(call.Arguments),
		"shell":         a.toolHasShell(t, call.Arguments),
		"decision":      decision,
		"match_reason":  detail.matchReason,
	}
	if detail.grant != nil {
		decisionAudit["grant_id"] = detail.grant.ID
	}
	if detail.guard != nil && len(detail.guard.Codes) > 0 {
		decisionAudit["guard_verdict"] = detail.guard.Verdict
		decisionAudit["guard_codes"] = detail.guard.Codes
	}
	a.audit(ctx, audit.ActionApprovalDecision, decisionAudit)
	if decision == approval.DecisionDeny {
		if detail.guard != nil {
			// Code Guard 拒绝：门禁原因原样回传模型，让它改方案而不是换个写法重试。
			a.audit(ctx, audit.ActionApprovalDenied, map[string]any{
				"name": call.Name, "reason": "code_guard", "codes": detail.guard.Codes,
			})
			// 门禁码一并回传：模型只能靠稳定代码判断该改哪一条，中文原因给人看。
			return tools.ResultError("CODE_GUARD 拒绝（不可通过审批放行）[" +
				strings.Join(detail.guard.Codes, ",") + "]：" + strings.Join(detail.guard.Reasons, "；"))
		}
		return tools.ResultError("tool call denied by approval policy")
	}
	if decision == approval.DecisionRequireApproval {
		key := declineKey(call.Name, call.Arguments)
		// 先查拒绝计数 ≥ 上限？是 → 不再打扰用户，直接返回"硬拦截 + 替代方案提示"，LLM 必须收敛到新方案。
		if a.Session.Declined(key) >= maxDeclinesBeforeBlock {
			a.audit(ctx, audit.ActionApprovalDenied, map[string]any{
				"name": call.Name, "reason": "declined twice, blocked (no user prompt)",
			})
			return tools.ResultError(a.formatBlockedDeclineMsg(call.Name, key))
		}
		if batchApproved {
			// 批量审批已预授权，跳过个体审批。
			a.audit(ctx, audit.ActionApprovalSkipped, map[string]any{"name": call.Name, "batch": true})
		} else if a.Approver != nil && !a.Session.Approved(key) {
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
			// P1.8: 审批 Step（挂起到等待用户确认）。
			approvalStep := a.beginStep(ctx, session.StepApproval, call.Name, "")
			a.waitStep(ctx, approvalStep)
			choice, err := a.Approver.Confirm(ctx, prompt, []UserChoiceOption{
				{Label: "是", Value: ChoiceYes},
				{Label: "是，本次会话不再询问", Value: ChoiceYesNoAskAgain},
				{Label: "否", Value: ChoiceNo},
			})
			if err != nil {
				a.audit(ctx, audit.ActionApprovalDenied, map[string]any{"name": call.Name, "reason": err.Error()})
				a.completeStep(ctx, approvalStep, false, err.Error())
				return tools.ResultError("approval error: " + err.Error())
			}
			switch choice {
			case ChoiceYesNoAskAgain:
				a.completeStep(ctx, approvalStep, true, "approved_forever")
				a.Session.AddApproved(key)
				grantAudit := map[string]any{"name": call.Name, "choice": choice}
				if g, ok := approval.NewGrant(a.grantRequestFor(t, call.Arguments)); ok {
					a.Session.AddGrant(g)
					grantAudit["grant_id"] = g.ID
					grantAudit["operation"] = g.Operation
					grantAudit["kind"] = g.Kind
					grantAudit["name"] = g.Name
					grantAudit["namespace"] = g.Namespace
				}
				a.audit(ctx, audit.ActionApprovalGranted, grantAudit)
			case ChoiceYes:
				a.completeStep(ctx, approvalStep, true, "approved")
				a.audit(ctx, audit.ActionApprovalGranted, map[string]any{"name": call.Name, "choice": choice})
			default:
				a.completeStep(ctx, approvalStep, false, "declined")
				total := a.Session.AddDeclined(key)
				a.Session.RemoveGrantMatching(a.grantRequestFor(t, call.Arguments))
				a.audit(ctx, audit.ActionApprovalDenied, map[string]any{
					"name": call.Name, "choice": choice, "declineCount": total,
				})
				return tools.ResultError(a.formatDeclinedMsg(call.Name, key, total))
			}
		} else {
			a.audit(ctx, audit.ActionApprovalSkipped, map[string]any{"name": call.Name})
		}
	}
	// 审批通过后才索取 Secret 明文值（三期 R4.5）：值只活在 execCtx 里，不进历史。
	execCtx, secretErr := a.collectSecretValues(ctx, t, call.Arguments)
	if secretErr != "" {
		return tools.ResultError(secretErr)
	}
	tr, err := t.Execute(execCtx, call.Arguments)
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
	if touches := tools.SecretTouchesFromData(tr.Data); len(touches) > 0 {
		// 三期 R4.6：审计只记 key/长度/SHA-256/来源，明文值从不落盘。
		a.audit(ctx, audit.ActionSecretTouched, map[string]any{
			"name": call.Name, "mark": "SECRET_TOUCHED", "secrets": touches,
		})
	}
	return tr
}

// collectSecretValues 在审批通过后、工具执行前逐个索取 $ASK 的 Secret 明文值（三期 R4.5）。
//
// 这是 Secret 值进入执行器的唯一入口：值只存在于返回的 ctx 里，随本次工具调用结束消失，
// 不进会话文件、审计、LLM 请求，也不缓存进 Session。交互通道不具备安全取值能力时直接拒绝，
// 宁可让模型改用 $GENERATE，也不让明文值绕道进入对话历史。
func (a *Agent) collectSecretValues(ctx context.Context, t tools.Tool, input json.RawMessage) (context.Context, string) {
	planner, ok := t.(tools.SecretValuePlanner)
	if !ok {
		return ctx, ""
	}
	pending, ok := planner.PendingSecretValues(input)
	if !ok || len(pending) == 0 {
		return ctx, ""
	}
	approver, ok := a.Approver.(SecretValueApprover)
	if !ok {
		return ctx, "该创建含需要用户提供的 Secret 值，但当前运行方式无法安全取值（非交互/无人审批通道）。请改用 $GENERATE 由执行器生成随机值，或在交互式 CLI/Web 会话中重试"
	}
	values := make(map[tools.SecretValueKey]string, len(pending))
	for _, item := range pending {
		a.audit(ctx, audit.ActionSecretValueRequested, map[string]any{"tool": t.Name(), "key": item.Key})
		value, err := approver.RequestSecretValue(ctx, item.Prompt)
		if err != nil {
			return ctx, "Secret 取值失败：" + err.Error()
		}
		if strings.TrimSpace(value) == "" {
			a.audit(ctx, audit.ActionApprovalDenied, map[string]any{
				"tool": t.Name(), "key": item.Key, "reason": "secret_value_not_provided",
			})
			return ctx, "USER_DECLINED_SECRET_VALUE: 用户未提供 " + item.Key.String() +
				" 的值，本次创建已取消。不要重复追问同一个值；可改用 $GENERATE 生成随机值，或与用户确认下一步方案"
		}
		values[item.Key] = value
	}
	return tools.WithSecretValues(ctx, values), ""
}

// formatDeclinedMsg 格式化第一次/第二次"被用户拒绝"时回传给 LLM 的文本。
// 第一次提示"换低风险替代方案"，第二次到达阈值前提示"再提同一个写操作将被自动拦截"。
func (a *Agent) formatDeclinedMsg(toolName, key string, declineCount int) string {
	base := fmt.Sprintf("USER_EXPLICITLY_DECLINED: 用户否决了写操作 %s。这不是工具错误，也不是需要重试的临时故障，而是用户明确表示不同意该变更。\n下一步必须：① 不要再用相同或等效的写参数重试；② 给出至少一个更低风险/更小范围的替代性修复方案供用户选择；③ 若涉及多选参数（如 StorageClass 选项、命名空间、副本数），必须先向用户澄清再行动。", toolName)
	if declineCount >= maxDeclinesBeforeBlock-1 {
		base += fmt.Sprintf("\n\n提醒：这是该操作在本会话第 %d 次被拒绝，再提同一写操作将被系统自动拦截并强制要求换方案。", declineCount)
	}
	return base
}

// formatBlockedDeclineMsg 格式化"被拒 ≥2 次"后的硬拦截消息，明确告知 LLM 必须换方案。
func (a *Agent) formatBlockedDeclineMsg(toolName, key string) string {
	return fmt.Sprintf(
		"DECLINED_TOO_MANY_TIMES: 用户已在本会话 %d 次否决写操作 %s，系统自动拦截。\n必须：(1) 停止使用相同或等效的破坏性写参数；(2) 选择更小范围的等价替代方案（如 Patch 替代 Delete、标注替代删除、保留数据）；(3) 如果是 PVC 绑定失败类场景，优先 kubectl patch pvc 或 kubectl patch storageclass，绝不允许 kubectl delete pvc 当作首选；(4) 换方案前先向用户复述：\"我之前的 X 方案被你否决了，现改用 Y 更小范围方案，是否接受？\"。",
		maxDeclinesBeforeBlock, toolName,
	)
}

func addUsage(a, b llm.TokenUsage) llm.TokenUsage {
	return llm.TokenUsage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
}
