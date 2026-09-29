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

// guardStubTool 是一个会自己申报门禁要素的工具（模拟 create_resource）。
type guardStubTool struct {
	name     string
	executed *int
	req      approval.GuardRequest
}

func (s *guardStubTool) Name() string { return s.name }
func (s *guardStubTool) Description() string {
	return "guard stub"
}
func (s *guardStubTool) InputSchema() any { return map[string]any{"type": "object"} }
func (s *guardStubTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	*s.executed++
	return tools.ToolResult{Data: map[string]any{"ok": true}}, nil
}
func (s *guardStubTool) IsReadOnly(json.RawMessage) bool { return false }
func (s *guardStubTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskMedium
}
func (s *guardStubTool) GuardRequest(json.RawMessage) (approval.GuardRequest, bool) {
	return s.req, true
}

type deniedRecorder struct {
	mu       sync.Mutex
	denied   []map[string]any
	decision []map[string]any
}

func (r *deniedRecorder) Write(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := e.Payload.(map[string]any); !ok {
		return nil
	} else if e.Action == audit.ActionApprovalDenied {
		r.denied = append(r.denied, p)
	} else if e.Action == audit.ActionApprovalDecision {
		r.decision = append(r.decision, p)
	}
	return nil
}

func (r *deniedRecorder) Close() error { return nil }

func runGuardLoop(t *testing.T, policy *approval.Policy, req approval.GuardRequest, choice string) (*fakeLLM, int, *stubApprover, *deniedRecorder) {
	t.Helper()
	executed := 0
	reg := tools.NewRegistry()
	stub := &guardStubTool{name: "create_resource", executed: &executed, req: req}
	if err := reg.Register(stub); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "create_resource", `{"mode":"apply"}`)),
		{Content: "done"},
	}}
	ap := &stubApprover{choices: []string{choice}}
	rec := &deniedRecorder{}
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        &session.Session{ID: "task", Cluster: "prod"},
		Approver:       ap,
		Recorder:       rec,
		ApprovalPolicy: policy,
		MaxIterations:  5,
		MaxToolCalls:   10,
	})
	if _, _, err := ag.Chat(context.Background(), "create"); err != nil {
		t.Fatal(err)
	}
	return fl, executed, ap, rec
}

func TestCodeGuardDenyBlocksEvenWithSkipPermissions(t *testing.T) {
	// apply 未经同一份清单的 dry run → Code Guard DENY：
	// 即使 --skip-permissions 也不能执行，且不能去打扰审批人。
	fl, executed, ap, rec := runGuardLoop(t,
		&approval.Policy{Mode: approval.ModeStrict, RequireApproval: true, SkipPermissions: true},
		approval.GuardRequest{Subjects: []approval.CreateSubject{
			{Kind: "ConfigMap", Scope: approval.CreateScopeNamespaced, Namespace: "prod"},
		}, DryRunVerified: false}, ChoiceYes)
	if executed != 0 {
		t.Fatalf("denied call executed %d times", executed)
	}
	if ap.calls != 0 {
		t.Fatalf("denied call asked the approver %d times, want 0", ap.calls)
	}
	if len(fl.captured) < 2 {
		t.Fatal("tool result never reached the model")
	}
	toolMsg := fl.captured[1][len(fl.captured[1])-1]
	if !strings.Contains(toolMsg.Content, "CODE_GUARD") || !strings.Contains(toolMsg.Content, approval.GuardDryRunRequired) {
		t.Fatalf("tool result to model = %q", toolMsg.Content)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.denied) != 1 {
		t.Fatalf("approval.denied audit = %d, want 1", len(rec.denied))
	}
	if rec.denied[0]["reason"] != "code_guard" {
		t.Fatalf("denied reason = %v, want code_guard", rec.denied[0]["reason"])
	}
	last := rec.decision[len(rec.decision)-1]
	if last["decision"] != approval.DecisionDeny {
		t.Fatalf("decision audit = %v, want deny", last["decision"])
	}
	if last["guard_verdict"] != approval.GuardDeny {
		t.Fatalf("guard_verdict = %v, want deny", last["guard_verdict"])
	}
}

func TestCodeGuardReviewStillAsksApprover(t *testing.T) {
	// 无门禁命中的普通 apply（已过 dry run）→ REVIEW：走正常人工审批。
	_, executed, ap, _ := runGuardLoop(t,
		&approval.Policy{Mode: approval.ModeSmart, RequireApproval: true},
		approval.GuardRequest{Subjects: []approval.CreateSubject{
			{Kind: "ConfigMap", Scope: approval.CreateScopeNamespaced, Namespace: "prod"},
		}, DryRunVerified: true}, ChoiceNo)
	if ap.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", ap.calls)
	}
	if executed != 0 {
		t.Fatalf("user declined, but tool executed %d times", executed)
	}
}

func TestCodeGuardReviewBypassedByLegacyAutomationChannels(t *testing.T) {
	// 二期的两条自动化通道仍然免除普通非 shell 写操作的人工确认；
	// Code Guard 只在 DENY 时才压过它们（REVIEW 保持原行为）。
	for _, p := range []*approval.Policy{
		{Mode: approval.ModeStrict, RequireApproval: true, SkipPermissions: true},
		{Mode: approval.ModeStrict, RequireApproval: false},
	} {
		_, executed, ap, _ := runGuardLoop(t, p, approval.GuardRequest{
			Subjects: []approval.CreateSubject{
				{Kind: "ConfigMap", Scope: approval.CreateScopeNamespaced, Namespace: "prod"},
			}, DryRunVerified: true}, ChoiceYes)
		if ap.calls != 0 || executed != 1 {
			t.Fatalf("policy %+v: approver=%d executed=%d, want 0/1", p, ap.calls, executed)
		}
	}
}
