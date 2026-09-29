package service

import (
	"context"
	"strings"
	"testing"
)

// chatApprover 的 Secret 取值通道（三期 R4.5）：事件里只能有问题，值只走 respond 通道。
func TestChatApproverRequestSecretValueRoundTrip(t *testing.T) {
	const plain = "user-typed-password"
	events := make(chan ChatEvent, 4)
	respond := make(chan string, 1)
	a := &chatApprover{events: events, respond: respond}

	go func() { respond <- plain }()
	got, err := a.RequestSecretValue(context.Background(), "请输入 password 的值")
	if err != nil {
		t.Fatal(err)
	}
	ev := <-events
	if ev.Type != ChatEventChoice || ev.Choice == nil || !ev.Choice.Secret {
		t.Fatalf("取值事件缺少 secret 标记: %+v", ev)
	}
	if len(ev.Choice.Options) != 0 {
		t.Fatalf("取值不该带是/否选项: %+v", ev.Choice.Options)
	}
	// 原值原样返回（不 trim 语义留给调用方），也不出现在事件里。
	if got != plain {
		t.Fatalf("回传值 = %q", got)
	}
	if strings.Contains(ev.Choice.Prompt, plain) {
		t.Fatal("事件里不能带明文")
	}
}

func TestChatApproverSecretValueDeclineIsNotAnError(t *testing.T) {
	for _, answer := range []string{"", "   ", "no", "NO"} {
		events := make(chan ChatEvent, 4)
		respond := make(chan string, 1)
		a := &chatApprover{events: events, respond: respond}
		go func() { respond <- answer }()
		got, err := a.RequestSecretValue(context.Background(), "请输入 password 的值")
		if err != nil {
			t.Fatalf("%q: 用户放弃不该是错误: %v", answer, err)
		}
		if got != "" {
			t.Fatalf("%q: 放弃时不该返回值，实际 %q", answer, got)
		}
		<-events
	}
}

func TestChatApproverSecretValueNeedsInteractiveChannel(t *testing.T) {
	// --skip-permissions / 自动放行通道没有真实人工审批人，取值必须直接失败，
	// 而不是静默建出一个空密码 Secret。
	for _, a := range []*chatApprover{{skip: true}, {autoApprove: true}} {
		got, err := a.RequestSecretValue(context.Background(), "请输入 password 的值")
		if err == nil || !strings.Contains(err.Error(), "$GENERATE") {
			t.Fatalf("免交互通道必须拒绝取值并建议随机生成，实际 got=%q err=%v", got, err)
		}
	}
}
