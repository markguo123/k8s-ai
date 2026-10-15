package service

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/k8s-ai/k8s-ai/internal/agent"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/sse"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// runScope 保存一次 Agent 运行的当前 turn/step 上下文（P1.9），
// 供转发协程给 message.delta 等流式事件补全 session/turn/step 关联字段。
// Tracer 回调在主 goroutine（ag.Chat）内触发，转发协程在 ag.Output 消费侧读取，
// 读写分离，因此用互斥锁保护。
type runScope struct {
	mu     sync.RWMutex
	sessID string
	turnID string
	stepID string
}

func (s *runScope) session() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessID
}

func (s *runScope) turn() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turnID
}

func (s *runScope) step() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stepID
}

func (s *runScope) turnStarted(turnID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.turnID = turnID
	s.stepID = ""
	s.mu.Unlock()
}

func (s *runScope) stepStarted(stepID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stepID = stepID
	s.mu.Unlock()
}

// fanOutTracer 把 Agent 生命周期观察同时转发给 Store 持久化（P1.8）
// 与统一 SSE 事件广播（P1.9）。回调为观察性质，不中断 Agent 主循环。
type fanOutTracer struct {
	scope   *runScope
	persist agent.Tracer    // 可为 nil（未装配 Store）
	sink    func(sse.Event) // 可为 nil（无事件订阅）
}

// TurnStarted 广播 turn.started 并记录当前 turn 作用域。
func (t *fanOutTracer) TurnStarted(ctx context.Context, turn *session.Turn) {
	t.scope.turnStarted(turn.ID)
	if t.persist != nil {
		t.persist.TurnStarted(ctx, turn)
	}
	t.emit(sse.New(t.scope.session(), turn.ID, "", sse.EventTurnStarted, sse.TurnData{
		Status: string(turn.Status),
		Input:  store.RedactJSONPayload(turn.Input),
	}))
}

// TurnUpdated 广播 turn.completed（State 由 status 字段承载），Tracer 恰好触发一次。
func (t *fanOutTracer) TurnUpdated(ctx context.Context, turn *session.Turn) {
	if t.persist != nil {
		t.persist.TurnUpdated(ctx, turn)
	}
	t.emit(sse.New(t.scope.session(), turn.ID, "", sse.EventTurnCompleted, sse.TurnData{
		Status: string(turn.Status),
		Output: store.RedactJSONPayload(turn.Output),
		Error:  turn.Error,
		Token:  sseTokenOf(turn.Token),
	}))
}

// StepStarted 广播 reason.started / tool.started（observe/approval/response 无
// “开始”统一事件：observation.created 与 approval.required 在结果/审批器侧发出）。
func (t *fanOutTracer) StepStarted(ctx context.Context, step *session.Step) {
	t.scope.stepStarted(step.ID)
	if t.persist != nil {
		t.persist.StepStarted(ctx, step)
	}
	var eventType sse.EventType
	var data sse.StepData
	switch step.Type {
	case session.StepReason:
		eventType = sse.EventReasonStarted
		data = sse.StepData{
			Kind: sse.StepKindReason, Status: string(step.Status), Seq: step.Seq,
		}
	case session.StepTool:
		eventType = sse.EventToolStarted
		data = sse.StepData{
			Kind: sse.StepKindTool, Status: string(step.Status), Seq: step.Seq,
			Tool: step.Tool, Params: store.RedactJSONPayload(step.Params),
		}
	default:
		return
	}
	t.emit(sse.New(t.scope.session(), step.TurnID, step.ID, eventType, data))
}

// StepUpdated 广播步骤终态：
//   - reason.completed / tool.completed 步骤结果
//   - observation.created 工具结果观察（脱敏摘要）
//   - approval.approved / approval.rejected 审批终态（decision 由 Result 推导）
func (t *fanOutTracer) StepUpdated(ctx context.Context, step *session.Step) {
	if t.persist != nil {
		t.persist.StepUpdated(ctx, step)
	}
	switch step.Type {
	case session.StepReason:
		t.emit(sse.New(t.scope.session(), step.TurnID, step.ID, sse.EventReasonCompleted, sse.StepData{
			Kind: sse.StepKindReason, Status: string(step.Status), Seq: step.Seq,
			Result: store.RedactJSONPayload(step.Result), Error: step.Error,
		}))
	case session.StepTool:
		t.emit(sse.New(t.scope.session(), step.TurnID, step.ID, sse.EventToolCompleted, sse.StepData{
			Kind: sse.StepKindTool, Status: string(step.Status), Seq: step.Seq,
			Tool: step.Tool, Result: store.RedactJSONPayload(step.Result), Error: step.Error,
		}))
	case session.StepObserve:
		t.emit(sse.New(t.scope.session(), step.TurnID, step.ID, sse.EventObservationCreated, sse.ObservationData{
			Tool: step.Tool, Result: store.RedactJSONPayload(step.Result),
			Summary: step.Summary, DisplayType: step.DisplayType, DurationMs: step.DurationMs, Risk: step.Risk,
			Data: redactAny(step.Data),
		}))
	case session.StepApproval:
		if step.Status == session.StepDone {
			t.emit(sse.New(t.scope.session(), step.TurnID, step.ID, sse.EventApprovalApproved, sse.ApprovalData{
				Decision: "approved",
			}))
			return
		}
		t.emit(sse.New(t.scope.session(), step.TurnID, step.ID, sse.EventApprovalRejected, sse.ApprovalData{
			Decision: "rejected",
		}))
	}
}

// redactAny 对任意 JSON 值做 JSON 感知脱敏（P7.4：tool_result.data 进 SSE 前
// 与结果摘要同源脱敏，Secret/kubeconfig 明文永不进入事件流）。
func redactAny(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(store.RedactJSONPayload(string(raw))), &out); err != nil {
		return nil
	}
	return out
}

func (t *fanOutTracer) emit(e sse.Event) {
	if t.sink != nil {
		t.sink(e)
	}
}

// sseTokenOf 把 session 侧 Token 用量转成统一事件的 token 载荷（全零时省略）。
func sseTokenOf(u session.TokenUsage) *sse.Token {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &sse.Token{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}
