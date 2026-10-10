package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// recordingTracer 记录回调顺序与终态，供 P1.8 生命周期埋点断言使用。
type recordingTracer struct {
	turn   *session.Turn
	events []string
}

func (r *recordingTracer) TurnStarted(_ context.Context, turn *session.Turn) {
	r.turn = turn
	r.events = append(r.events, "turn_start:"+string(turn.Status))
}

func (r *recordingTracer) TurnUpdated(_ context.Context, turn *session.Turn) {
	r.events = append(r.events, "turn_update:"+string(turn.Status))
}

func (r *recordingTracer) StepStarted(_ context.Context, step *session.Step) {
	r.events = append(r.events, "step_start:"+string(step.Type))
}

func (r *recordingTracer) StepUpdated(_ context.Context, step *session.Step) {
	r.events = append(r.events, "step_update:"+string(step.Status))
}

func countEvent(events []string, prefix string) int {
	n := 0
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func TestTracerRecordsToolRunLifecycle(t *testing.T) {
	executed := 0
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "get_pod", `{"namespace":"prod","name":"web-0"}`)),
		{Content: "根因：Pod 反复重启", Usage: llm.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
	}}
	a, _ := testAgent(t, fl, []tools.Tool{&stubTool{name: "get_pod", readonly: true, executed: &executed}}, nil)
	rec := &recordingTracer{}
	a.Tracer = rec

	answer, _, err := a.Chat(context.Background(), "检查 web-0 反复重启")
	if err != nil {
		t.Fatal(err)
	}
	if rec.turn == nil || rec.turn.Status != session.TurnDone {
		t.Fatalf("turn = %+v", rec.turn)
	}
	if rec.turn.Output != answer || answer != "根因：Pod 反复重启" {
		t.Fatalf("answer = %q, output = %q", answer, rec.turn.Output)
	}
	// 首事件是 Turn 开始（running），末事件是 Turn 收尾（done）。
	if len(rec.events) == 0 || rec.events[0] != "turn_start:running" {
		t.Fatalf("first event = %+v", rec.events)
	}
	if last := rec.events[len(rec.events)-1]; last != "turn_update:done" {
		t.Fatalf("last event = %s", last)
	}
	// Reason → Tool → Observe（工具轮） + Reason → Response（回答轮）。
	if got := countEvent(rec.events, "step_start:reason"); got != 2 {
		t.Fatalf("reason steps = %d", got)
	}
	if got := countEvent(rec.events, "step_start:tool"); got != 1 {
		t.Fatalf("tool steps = %d", got)
	}
	if got := countEvent(rec.events, "step_start:observe"); got != 1 {
		t.Fatalf("observe steps = %d", got)
	}
	if got := countEvent(rec.events, "step_start:response"); got != 1 {
		t.Fatalf("response steps = %d", got)
	}
	// 每个 Step 都必须以 done/failed 收尾，不能残留 running/waiting。
	if got := countEvent(rec.events, "step_update:running") + countEvent(rec.events, "step_update:waiting"); got != 0 {
		t.Fatalf("non-terminal step updates = %d (%+v)", got, rec.events)
	}
}

func TestTracerRecordsApprovalWaitAndResume(t *testing.T) {
	executed := 0
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "scale_deployment", `{"namespace":"prod","name":"web","replicas":5}`)),
		{Content: "已扩容到 5 副本", Usage: llm.TokenUsage{TotalTokens: 4}},
	}}
	approver := &stubApprover{choices: []string{ChoiceYes}}
	a, _ := testAgent(t, fl, []tools.Tool{&stubTool{name: "scale_deployment", readonly: false, executed: &executed}}, approver)
	rec := &recordingTracer{}
	a.Tracer = rec

	_, _, err := a.Chat(context.Background(), "把 web 扩容到 5")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("tool executed = %d", executed)
	}
	if got := countEvent(rec.events, "step_start:approval"); got != 1 {
		t.Fatalf("approval steps = %d", got)
	}
	// 审批 Step 先进入 waiting，审批通过后再 done。
	seenWait := false
	for _, e := range rec.events {
		if e == "step_update:waiting" {
			seenWait = true
		}
	}
	if !seenWait {
		t.Fatalf("approval step 应进入 waiting：%+v", rec.events)
	}
	if rec.turn == nil || rec.turn.Status != session.TurnDone {
		t.Fatalf("turn = %+v", rec.turn)
	}
}

func TestTracerFailsTurnOnLLMError(t *testing.T) {
	fl := &fakeLLM{errors: []error{context.DeadlineExceeded}}
	a, _ := testAgent(t, fl, nil, nil)
	rec := &recordingTracer{}
	a.Tracer = rec

	if _, _, err := a.Chat(context.Background(), "检查集群"); err == nil {
		t.Fatal("expected LLM error")
	}
	if rec.turn == nil || rec.turn.Status != session.TurnFailed {
		t.Fatalf("turn = %+v", rec.turn)
	}
	if last := rec.events[len(rec.events)-1]; last != "turn_update:failed" {
		t.Fatalf("last event = %s", last)
	}
}
