package service

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/kubernetes"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/scanner"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// newRuntimeConfigChat 装配一个可捕获 llm.Options 的 ChatService，用来断言
// "本轮生效配置"真的进了 LLM 装配面（P6.1 / D-29）。
func newRuntimeConfigChat(t *testing.T, baseline string) (ChatService, *config.Config, func() *llm.Options) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.SessionDir = t.TempDir()
	cfg.Agent.AuditDir = t.TempDir()
	cfg.LLM.Model = baseline
	cfg.LLM.Endpoint = "http://baseline.example/v1"
	reader := kubernetes.NewClientWithClientset(fake.NewSimpleClientset())
	fl := &scriptedLLM{responses: []*llm.ChatResponse{{Content: "done"}}}
	var got *llm.Options
	svc := NewChat(cfg, func() (scanner.Reader, error) { return reader, nil }, func(o llm.Options) llm.LLMClient {
		cp := o
		got = &cp
		return fl
	}, func() tools.ScanRunner { return &fakeScanRunner{} }, func() (tools.WriteRunner, error) {
		return &fakeWriteRunner{}, nil
	})
	return svc, cfg, func() *llm.Options { return got }
}

func runChatRound(t *testing.T, svc ChatService, id string) {
	t.Helper()
	if _, err := svc.GetOrCreateSession(id, "", ""); err != nil {
		t.Fatalf("session: %v", err)
	}
	events, err := svc.Chat(context.Background(), ChatRequest{SessionID: id, Query: "检查集群"}, make(chan string, 1))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}
}

// TestChatUsesRuntimeConfigPerRound 锁"页面即生效"：解析出的运行时配置进 LLM 装配，
// 且每轮都重新解析（配置中心改完下一轮立即生效，不重启进程）。
func TestChatUsesRuntimeConfigPerRound(t *testing.T) {
	svc, baseline, gotOpts := newRuntimeConfigChat(t, "baseline-model")
	calls := 0
	resolved := *baseline
	resolved.LLM.Model = "runtime-model"
	resolved.LLM.Endpoint = "http://runtime.example/v1"
	svc.(RuntimeConfigBinder).SetRuntimeConfigResolver(func(context.Context) (*config.Config, error) {
		calls++
		out := resolved
		return &out, nil
	})

	runChatRound(t, svc, "rt-1")
	got := gotOpts()
	if got == nil {
		t.Fatal("LLM 未装配")
	}
	if got.Model != "runtime-model" || got.Endpoint != "http://runtime.example/v1" {
		t.Fatalf("本轮应使用运行时配置, got model=%q endpoint=%q", got.Model, got.Endpoint)
	}
	if calls != 1 {
		t.Fatalf("每轮应解析一次配置, got %d", calls)
	}
	runChatRound(t, svc, "rt-2")
	if calls != 2 {
		t.Fatalf("第二轮应重新解析配置（页面改完即生效）, got %d", calls)
	}
}

// TestChatPropagatesRuntimeConfigError 锁"不静默回落"：解析失败必须把错误抛给调用方，
// 不能悄悄改用启动基线或内置默认模型地址。
func TestChatPropagatesRuntimeConfigError(t *testing.T) {
	svc, _, gotOpts := newRuntimeConfigChat(t, "baseline-model")
	sentinel := errors.New("model is not configured")
	svc.(RuntimeConfigBinder).SetRuntimeConfigResolver(func(context.Context) (*config.Config, error) {
		return nil, sentinel
	})
	if _, err := svc.GetOrCreateSession("rt-err", "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Chat(context.Background(), ChatRequest{SessionID: "rt-err", Query: "检查集群"}, make(chan string, 1))
	if !errors.Is(err, sentinel) {
		t.Fatalf("配置解析失败应原样上抛, got %v", err)
	}
	if gotOpts() != nil {
		t.Fatal("配置解析失败时不应继续装配 LLM")
	}
}

// TestChatWithoutResolverKeepsBaseline 锁 CLI 直用路径：未注入解析器时沿用启动基线。
func TestChatWithoutResolverKeepsBaseline(t *testing.T) {
	svc, _, gotOpts := newRuntimeConfigChat(t, "baseline-model")
	runChatRound(t, svc, "rt-base")
	got := gotOpts()
	if got == nil {
		t.Fatal("LLM 未装配")
	}
	if got.Model != "baseline-model" || got.Endpoint != "http://baseline.example/v1" {
		t.Fatalf("未注入解析器时应沿用基线, got model=%q endpoint=%q", got.Model, got.Endpoint)
	}
}
