package agent

import (
	"testing"
	"time"
)

func TestNewAgentStateAndEmit(t *testing.T) {
	a := New(Options{})
	if a.ID == "" {
		t.Fatal("agent ID 不应为空")
	}
	if a.State != AgentStateIdle {
		t.Fatalf("初始状态 = %s", a.State)
	}
	a.SetState(AgentStateRunning)
	if a.State != AgentStateRunning {
		t.Fatalf("状态 = %s", a.State)
	}
	// 先消费 SetState 广播的状态事件。
	select {
	case m := <-a.Output:
		if m.Type != MessageTypeAgentState {
			t.Fatalf("首条消息应为状态事件: %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("状态事件未送达")
	}
	a.EmitText("调查中……")
	select {
	case m := <-a.Output:
		if m.Type != MessageTypeText || m.Source != SourceAgent {
			t.Fatalf("消息 = %+v", m)
		}
		if m.ID == "" || m.Timestamp.IsZero() {
			t.Fatalf("消息缺少 ID/时间戳: %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("消息未送达")
	}
}

func TestSetStateBroadcastsStateEvent(t *testing.T) {
	a := New(Options{})
	a.SetState(AgentStateRunning)
	select {
	case m := <-a.Output:
		if m.Type != MessageTypeAgentState || m.Payload != AgentStateRunning {
			t.Fatalf("状态事件 = %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("状态事件未送达")
	}
	// 同状态重复设置不重复广播。
	a.SetState(AgentStateRunning)
	select {
	case m := <-a.Output:
		t.Fatalf("不应重复广播: %+v", m)
	default:
	}
}

func TestEmitNonBlockingWhenFull(t *testing.T) {
	a := New(Options{})
	for i := 0; i < cap(a.Output)+10; i++ {
		a.EmitText("x")
	}
	// 通道满后 Emit 不阻塞（丢弃），Agent 继续工作。
	a.SetState(AgentStateDone)
	if a.State != AgentStateDone {
		t.Fatalf("状态 = %s", a.State)
	}
}

func TestChoiceConstants(t *testing.T) {
	req := UserChoiceRequest{
		Prompt: "是否执行？",
		Options: []UserChoiceOption{
			{Label: "是", Value: ChoiceYes},
			{Label: "是，本次会话不再询问", Value: ChoiceYesNoAskAgain},
			{Label: "否", Value: ChoiceNo},
		},
	}
	if len(req.Options) != 3 || req.Options[1].Value != ChoiceYesNoAskAgain {
		t.Fatalf("审批选项 = %+v", req.Options)
	}
}
