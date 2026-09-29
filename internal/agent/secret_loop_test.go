package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

// secretStubTool 模拟 create_resource：申报待取的 Secret 值，并记下 ctx 里实际收到的值。
type secretStubTool struct {
	executed *int
	got      map[tools.SecretValueKey]string
	pending  []tools.SecretValuePrompt
	result   *tools.ToolResult
}

func (s *secretStubTool) Name() string { return "create_resource" }
func (s *secretStubTool) Description() string {
	return "secret stub"
}
func (s *secretStubTool) InputSchema() any { return map[string]any{"type": "object"} }
func (s *secretStubTool) Execute(ctx context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	*s.executed++
	s.got = tools.SecretValuesFromContext(ctx)
	if s.result != nil {
		return *s.result, nil
	}
	return tools.ToolResult{Data: map[string]any{"ok": true}}, nil
}
func (s *secretStubTool) IsReadOnly(json.RawMessage) bool { return false }
func (s *secretStubTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskHigh
}
func (s *secretStubTool) PendingSecretValues(json.RawMessage) ([]tools.SecretValuePrompt, bool) {
	return s.pending, len(s.pending) > 0
}

// secretApprover 既能人工审批，也能安全取值（交互式 CLI/Web 才具备的能力）。
type secretApprover struct {
	values   []string
	err      error
	asks     int
	confirms int
}

func (a *secretApprover) Confirm(context.Context, string, []UserChoiceOption) (string, error) {
	a.confirms++
	return ChoiceYes, nil
}

func (a *secretApprover) RequestSecretValue(context.Context, string) (string, error) {
	a.asks++
	if a.err != nil {
		return "", a.err
	}
	if len(a.values) == 0 {
		return "", nil
	}
	value := a.values[0]
	if len(a.values) > 1 {
		a.values = a.values[1:]
	}
	return value, nil
}

// auditRecorder 收下全部审计事件，用于断言"记了什么、绝不能记什么"。
type auditRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *auditRecorder) Write(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *auditRecorder) Close() error { return nil }

func (r *auditRecorder) count(action string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Action == action {
			n++
		}
	}
	return n
}

func (r *auditRecorder) payloadWith(action string) (map[string]any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Action == action {
			if p, ok := e.Payload.(map[string]any); ok {
				return p, true
			}
		}
	}
	return nil, false
}

// leaks 报告明文是否出现在任何一条审计记录里（三期 R4.6 红线）。
func (r *auditRecorder) leaks(plain string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		encoded, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), plain) {
			return true
		}
	}
	return false
}

var secretValueKey = tools.SecretValueKey{Kind: "Secret", Namespace: "web", Name: "db", Key: "password"}

var secretPrompts = []tools.SecretValuePrompt{{
	Key:    secretValueKey,
	Prompt: "请输入 Secret web/db 的 password 的值",
}}

func runSecretLoop(t *testing.T, approver Approver, result *tools.ToolResult) (*fakeLLM, *secretStubTool, *auditRecorder) {
	t.Helper()
	executed := 0
	stub := &secretStubTool{executed: &executed, pending: secretPrompts, result: result}
	reg := tools.NewRegistry()
	if err := reg.Register(stub); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "create_resource", `{"mode":"apply"}`)),
		{Content: "done"},
	}}
	rec := &auditRecorder{}
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        &session.Session{ID: "secret", Cluster: "prod"},
		Approver:       approver,
		Recorder:       rec,
		ApprovalPolicy: &approval.Policy{Mode: approval.ModeSmart, RequireApproval: true},
		MaxIterations:  5,
		MaxToolCalls:   10,
	})
	if _, _, err := ag.Chat(context.Background(), "create"); err != nil {
		t.Fatal(err)
	}
	return fl, stub, rec
}

func TestSecretValuesReachToolThroughContextOnly(t *testing.T) {
	const plain = "user-typed-password"
	// 工具结果带上 Secret 指纹结论（key/长度/SHA-256 的构成由 executor/tools 用例覆盖）。
	touched := tools.ToolResult{Data: map[string]any{
		"ok":                    true,
		tools.DataSecretTouches: []tools.SecretTouch{{Kind: "Secret", Namespace: "web", Name: "db"}},
	}}
	ap := &secretApprover{values: []string{plain}}
	fl, stub, rec := runSecretLoop(t, ap, &touched)

	if ap.confirms != 1 || ap.asks != 1 {
		t.Fatalf("必须先人工审批再取值，实际 审批=%d 取值=%d", ap.confirms, ap.asks)
	}
	if *stub.executed != 1 || stub.got[secretValueKey] != plain {
		t.Fatalf("值没有通过 context 送达工具: executed=%d got=%v", *stub.executed, stub.got)
	}
	if rec.count(audit.ActionSecretValueRequested) != 1 {
		t.Fatalf("缺少 secret.value_requested 审计，实际 %d 条", rec.count(audit.ActionSecretValueRequested))
	}
	if rec.count(audit.ActionSecretTouched) != 1 {
		t.Fatalf("缺少 secret.touched 审计，实际 %d 条", rec.count(audit.ActionSecretTouched))
	}
	// 红线：明文既不进审计，也不进发给模型的下一条消息。
	if rec.leaks(plain) {
		t.Fatal("审计日志泄露了 Secret 明文")
	}
	for _, msgs := range fl.captured {
		for _, m := range msgs {
			if strings.Contains(m.Content, plain) {
				t.Fatal("Secret 明文进入了 LLM 请求")
			}
		}
	}
}

func TestSecretValueWithoutSafeChannelRefusesTool(t *testing.T) {
	// stubApprover 只能点头/摇头，不具备安全取值能力（--skip-permissions、cron 等通道）。
	fl, stub, rec := runSecretLoop(t, &stubApprover{choices: []string{ChoiceYes}}, nil)
	if *stub.executed != 0 {
		t.Fatal("取不到值时不能执行创建")
	}
	if rec.count(audit.ActionSecretValueRequested) != 0 {
		t.Fatal("无取值通道时不该记取值请求")
	}
	toolMsg := fl.captured[len(fl.captured)-1][len(fl.captured[len(fl.captured)-1])-1]
	if !strings.Contains(toolMsg.Content, "$GENERATE") {
		t.Fatalf("应引导模型改用执行器生成随机值，实际回传: %q", toolMsg.Content)
	}
}

func TestSecretValueEmptyAnswerCancelsCreation(t *testing.T) {
	fl, stub, rec := runSecretLoop(t, &secretApprover{values: []string{""}}, nil)
	if *stub.executed != 0 {
		t.Fatal("用户留空时不能执行创建")
	}
	toolMsg := fl.captured[len(fl.captured)-1][len(fl.captured[len(fl.captured)-1])-1]
	if !strings.Contains(toolMsg.Content, "USER_DECLINED_SECRET_VALUE") {
		t.Fatalf("用户留空必须作为显式拒绝回传给模型，实际: %q", toolMsg.Content)
	}
	if payload, ok := rec.payloadWith(audit.ActionApprovalDenied); !ok || payload["reason"] != "secret_value_not_provided" {
		t.Fatalf("缺少 secret_value_not_provided 拒绝审计: %+v", payload)
	}
}
