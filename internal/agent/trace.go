package agent

import (
	"context"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// Tracer 观察一次 Agent 运行的生命周期（P1.8），供 service 层把 Turn/Step 持久化回放。
// 所有回调都是观察性质：实现不得阻塞 Agent 主循环；Agent 本身不依赖 Store。
type Tracer interface {
	// TurnStarted 表示一轮运行已开始（Turn 已从 pending 转为 running）。
	TurnStarted(ctx context.Context, turn *session.Turn)
	// TurnUpdated 表示 Turn 状态或内容变化（完成/失败等收尾）。
	TurnUpdated(ctx context.Context, turn *session.Turn)
	// StepStarted 表示一个 Step 开始执行（已转为 running）。
	StepStarted(ctx context.Context, step *session.Step)
	// StepUpdated 表示 Step 状态或内容变化（wait/resume/succeed/fail）。
	StepUpdated(ctx context.Context, step *session.Step)
}

type nopTracer struct{}

func (nopTracer) TurnStarted(context.Context, *session.Turn) {}
func (nopTracer) TurnUpdated(context.Context, *session.Turn) {}
func (nopTracer) StepStarted(context.Context, *session.Step) {}
func (nopTracer) StepUpdated(context.Context, *session.Step) {}

// trace 返回挂载的 Tracer；未配置时为 no-op。
func (a *Agent) trace() Tracer {
	if a.Tracer == nil {
		return nopTracer{}
	}
	return a.Tracer
}

// newStep 构造当前 Turn 的一个 Step 记录（P1.8，Seq 按执行顺序递增）。
// Params 为落库前的原始文本摘要，Store 侧统一脱敏。
func (a *Agent) newStep(typ session.StepType, tool, params string) *session.Step {
	a.stepSeq++
	now := time.Now()
	return &session.Step{
		ID:        newID(),
		TurnID:    a.turn.ID,
		SessionID: a.turn.SessionID,
		Seq:       a.stepSeq,
		Type:      typ,
		Tool:      tool,
		Params:    params,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// beginStep 创建并启动一个 Step（running），随后通知 Tracer。
func (a *Agent) beginStep(ctx context.Context, typ session.StepType, tool, params string) *session.Step {
	s := a.newStep(typ, tool, params)
	_ = s.Begin()
	a.trace().StepStarted(ctx, s)
	return s
}

// waitStep 让 Step 进入等待（触发审批等需要用户输入的场景）。
func (a *Agent) waitStep(ctx context.Context, s *session.Step) {
	_ = s.Wait()
	a.trace().StepUpdated(ctx, s)
}

// completeStep 用一个最终结果收尾 Step（ok → done / failed）并通知 Tracer。
func (a *Agent) completeStep(ctx context.Context, s *session.Step, ok bool, result string) {
	if ok {
		_ = s.Succeed(result)
	} else {
		_ = s.Fail(result)
	}
	a.trace().StepUpdated(ctx, s)
}

// summarizeReason 是一轮推理 Step 的结果摘要：有工具调用时记工具名，否则记正文预览。
func summarizeReason(mode string, resp *llm.ChatResponse) string {
	if resp == nil {
		return ""
	}
	if len(resp.ToolCalls) > 0 {
		names := make([]string, 0, len(resp.ToolCalls))
		for _, c := range resp.ToolCalls {
			names = append(names, c.Name)
		}
		return "tools=" + strings.Join(names, ",") + " mode=" + mode
	}
	return truncateText(resp.Content, 240)
}

// truncateText 截断长文本，避免 Turn/Step 记录膨胀（保持可读摘要）。
func truncateText(s string, max int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// toSessionUsage 把 llm.TokenUsage 转成 session.TokenUsage（两者结构同构，
// 但 session 包不允许反向依赖 llm，只能显式转换）。
func toSessionUsage(u llm.TokenUsage) session.TokenUsage {
	return session.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}
