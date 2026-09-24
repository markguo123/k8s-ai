package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

type shellStubTool struct {
	executed *int
}

func (s *shellStubTool) Name() string { return "kubectl" }
func (s *shellStubTool) Description() string {
	return "stub shell aware tool"
}
func (s *shellStubTool) InputSchema() any {
	return map[string]any{"type": "object"}
}
func (s *shellStubTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	*s.executed++
	return tools.ToolResult{Data: map[string]any{"ok": true}}, nil
}
func (s *shellStubTool) IsReadOnly(json.RawMessage) bool { return false }
func (s *shellStubTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskHigh
}

func (s *shellStubTool) HasShellSyntax(json.RawMessage) bool { return true }

func testShellPolicyLoop(t *testing.T, policy *approval.Policy) {
	t.Helper()
	executed := 0
	reg := tools.NewRegistry()
	if err := reg.Register(&shellStubTool{executed: &executed}); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "kubectl", `{"command":"kubectl get pods | kubectl delete pod x"}`)),
		{Content: "done"},
	}}
	approver := &stubApprover{choices: []string{ChoiceNo}}
	sess := &session.Session{ID: "test"}
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        sess,
		Approver:       approver,
		ApprovalPolicy: policy,
		MaxIterations:  5,
		MaxToolCalls:   10,
		Timeout:        0,
	})
	answer, _, err := ag.Chat(context.Background(), "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "done" {
		t.Fatalf("answer = %q", answer)
	}
	if executed != 0 {
		t.Fatalf("shell command auto-executed despite bypass flag; executed = %d", executed)
	}
	if approver.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", approver.calls)
	}
}

func TestShellNotBypassedBySkipPermissions(t *testing.T) {
	testShellPolicyLoop(t, &approval.Policy{
		Mode:            approval.ModeStrict,
		RequireApproval: true,
		SkipPermissions: true,
	})
}

func TestShellNotBypassedByRequireApprovalFalse(t *testing.T) {
	testShellPolicyLoop(t, &approval.Policy{
		Mode:            approval.ModeStrict,
		RequireApproval: false,
	})
}

type decisionRecorder struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (r *decisionRecorder) Write(_ context.Context, e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Action == audit.ActionApprovalDecision {
		if p, ok := e.Payload.(map[string]any); ok {
			r.payloads = append(r.payloads, p)
		}
	}
	return nil
}

func (r *decisionRecorder) Close() error { return nil }

type riskToolStub struct {
	name     string
	risk     model.RiskLevel
	shell    bool
	executed *int
}

func (s *riskToolStub) Name() string { return s.name }
func (s *riskToolStub) Description() string {
	return "risk stub"
}
func (s *riskToolStub) InputSchema() any {
	return map[string]any{"type": "object"}
}
func (s *riskToolStub) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	*s.executed++
	return tools.ToolResult{Data: map[string]any{"ok": true}}, nil
}
func (s *riskToolStub) IsReadOnly(json.RawMessage) bool { return false }
func (s *riskToolStub) Risk(json.RawMessage) model.RiskLevel {
	return s.risk
}
func (s *riskToolStub) HasShellSyntax(_ json.RawMessage) bool { return s.shell }

func TestSmartShellNotBypassedBySkipPermissions(t *testing.T) {
	testShellPolicyLoop(t, &approval.Policy{
		Mode:            approval.ModeSmart,
		RequireApproval: true,
		SkipPermissions: true,
	})
}

func TestApprovalDecisionAuditIncludesContext(t *testing.T) {
	executed := 0
	reg := tools.NewRegistry()
	if err := reg.Register(&stubTool{name: "get_pod", readonly: true, executed: &executed}); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "get_pod", `{"name":"web-0"}`)),
		{Content: "done"},
	}}
	rec := &decisionRecorder{}
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        &session.Session{ID: "s"},
		Recorder:       rec,
		ApprovalPolicy: &approval.Policy{Mode: approval.ModeSmart, RequireApproval: true},
		MaxIterations:  5,
		MaxToolCalls:   10,
		Timeout:        0,
	})
	if _, _, err := ag.Chat(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.payloads) == 0 {
		t.Fatal("no approval.decision audit recorded")
	}
	p := rec.payloads[len(rec.payloads)-1]
	if p["approval_mode"] != approval.ModeSmart {
		t.Fatalf("approval_mode = %v, want smart", p["approval_mode"])
	}
	if p["readonly"] != true {
		t.Fatalf("readonly = %v, want true", p["readonly"])
	}
	if p["shell"] != false {
		t.Fatalf("shell = %v, want false", p["shell"])
	}
	if p["decision"] != approval.DecisionAuto {
		t.Fatalf("decision = %v, want auto", p["decision"])
	}
}

func TestSmartModeGrantAutoApprovesSecondMediumWrite(t *testing.T) {
	executed := 0
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "scale", `{"kind":"deployments","name":"web","namespace":"prod"}`)),
		toolCallResp(call("c2", "scale", `{"kind":"deployments","name":"web","namespace":"prod"}`)),
		{Content: "done"},
	}}
	ap := &stubApprover{choices: []string{ChoiceYesNoAskAgain}}
	reg := tools.NewRegistry()
	if err := reg.Register(&stubTool{name: "scale", readonly: false, executed: &executed}); err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{ID: "task", Cluster: "prod", Namespace: "prod"}
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        sess,
		Approver:       ap,
		ApprovalPolicy: &approval.Policy{Mode: approval.ModeSmart, RequireApproval: true},
		MaxIterations:  5,
		MaxToolCalls:   10,
	})
	if _, _, err := ag.Chat(context.Background(), "scale"); err != nil {
		t.Fatal(err)
	}
	if executed != 2 {
		t.Fatalf("both medium writes should execute under a matching grant, executed = %d", executed)
	}
	if ap.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", ap.calls)
	}
	if len(sess.ApprovalGrants) != 1 {
		t.Fatalf("ApprovalGrants len = %d, want 1", len(sess.ApprovalGrants))
	}
}

func TestGrantNeverAutoApprovesHighWrite(t *testing.T) {
	executed := 0
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "scale", `{"kind":"deployments","name":"web","namespace":"prod"}`)),
		{Content: "done"},
	}}
	ap := &stubApprover{choices: []string{ChoiceNo}}
	reg := tools.NewRegistry()
	if err := reg.Register(&riskToolStub{name: "scale", risk: model.RiskHigh, executed: &executed}); err != nil {
		t.Fatal(err)
	}
	g, ok := approval.NewGrant(approval.GrantRequest{
		TaskID:    "task",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "web",
		Namespace: "prod",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	})
	if !ok {
		t.Fatal("seed grant should be created")
	}
	sess := &session.Session{ID: "task", Cluster: "prod", Namespace: "prod"}
	sess.AddGrant(g)
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        sess,
		Approver:       ap,
		ApprovalPolicy: &approval.Policy{Mode: approval.ModeSmart, RequireApproval: true},
		MaxIterations:  5,
		MaxToolCalls:   10,
	})
	if _, _, err := ag.Chat(context.Background(), "scale"); err != nil {
		t.Fatal(err)
	}
	if executed != 0 {
		t.Fatalf("high risk write must not be auto-approved by a medium grant, executed = %d", executed)
	}
	if ap.calls != 1 {
		t.Fatalf("high risk write should still ask the approver, calls = %d", ap.calls)
	}
}

func TestGrantNeverAutoApprovesShellWrite(t *testing.T) {
	executed := 0
	cmd := `{"command":"kubectl scale deployment/web -n prod"}`
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "kubectl", cmd)),
		{Content: "done"},
	}}
	ap := &stubApprover{choices: []string{ChoiceNo}}
	reg := tools.NewRegistry()
	if err := reg.Register(&riskToolStub{name: "kubectl", risk: model.RiskMedium, shell: true, executed: &executed}); err != nil {
		t.Fatal(err)
	}
	g, ok := approval.NewGrant(approval.GrantRequest{
		TaskID:    "task",
		Operation: "scale",
		Kind:      "deployment",
		Name:      "web",
		Namespace: "prod",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	})
	if !ok {
		t.Fatal("seed grant should be created")
	}
	sess := &session.Session{ID: "task", Cluster: "prod", Namespace: "prod"}
	sess.AddGrant(g)
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        sess,
		Approver:       ap,
		ApprovalPolicy: &approval.Policy{Mode: approval.ModeSmart, RequireApproval: true},
		MaxIterations:  5,
		MaxToolCalls:   10,
	})
	if _, _, err := ag.Chat(context.Background(), "scale"); err != nil {
		t.Fatal(err)
	}
	if executed != 0 {
		t.Fatalf("shell write must never be auto-approved by a grant, executed = %d", executed)
	}
	if ap.calls != 1 {
		t.Fatalf("shell write should still ask the approver, calls = %d", ap.calls)
	}
}
