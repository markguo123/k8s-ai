package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// TestSessionTokenUsagePersistsAcrossTurns 覆盖 P2.13：会话级 Token 用量来自落库 Turn 的累加，
// 因此刷新/重开会话（不依赖任何内存态）仍可读。
func TestSessionTokenUsagePersistsAcrossTurns(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{
		{Content: "第一轮", Usage: llm.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
		{Content: "第二轮", Usage: llm.TokenUsage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}},
	}}
	svc, st := testChatServiceWithStore(t, fl)
	tokenizer, ok := svc.(SessionTokenizer)
	if !ok {
		t.Fatal("装配了 Store 的 ChatService 应实现 SessionTokenizer")
	}
	ctx := context.Background()
	sess, err := svc.GetOrCreateSession("token-s1", "", "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	for i, query := range []string{"第一问", "第二问"} {
		respond := make(chan string, 1)
		events, err := svc.Chat(ctx, ChatRequest{SessionID: sess.ID, Query: query}, respond)
		if err != nil {
			t.Fatalf("chat %d: %v", i, err)
		}
		for range events {
		}
	}

	got, err := tokenizer.SessionTokenUsage(ctx, sess.ID)
	if err != nil {
		t.Fatalf("session token usage: %v", err)
	}
	if got != (session.TokenUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}) {
		t.Fatalf("session token usage = %+v", got)
	}

	// 与逐轮落库记录一致：会话级统计不引入额外来源。
	turns, err := st.Turns().ListBySession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	var manual session.TokenUsage
	for _, turn := range turns {
		manual = manual.Add(turn.Token)
	}
	if manual != got {
		t.Fatalf("逐轮累加 %+v 与会话级统计 %+v 不一致", manual, got)
	}

	// 未知会话不报错，返回零值（前端据此隐藏统计）。
	empty, err := tokenizer.SessionTokenUsage(ctx, "not-exist-session")
	if err != nil || !empty.IsZero() {
		t.Fatalf("unknown session = %+v, err = %v", empty, err)
	}
}

// TestSessionTokenUsageWithoutStore 覆盖三期运行模式：无 Store 时返回零值且不报错，
// 保证未迁移环境仍可启动 Web。
func TestSessionTokenUsageWithoutStore(t *testing.T) {
	fl := &scriptedLLM{responses: []*llm.ChatResponse{{Content: "直接回答", Usage: llm.TokenUsage{TotalTokens: 1}}}}
	svc, _, _ := testChatService(t, fl)
	tokenizer, ok := svc.(SessionTokenizer)
	if !ok {
		t.Fatal("ChatService 应始终实现 SessionTokenizer（无 Store 时退化为零值）")
	}
	got, err := tokenizer.SessionTokenUsage(context.Background(), "any")
	if err != nil || !got.IsZero() {
		t.Fatalf("无 Store 时 = %+v, err = %v", got, err)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("token usage 应可序列化给 Web 快照: %v", err)
	}
}
