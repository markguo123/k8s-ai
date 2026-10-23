package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/tools"
)

func TestAgentScopeDenialSkipsToolAndApproval(t *testing.T) {
	executed := 0
	fl := &fakeLLM{responses: []*llm.ChatResponse{
		toolCallResp(call("c1", "kubectl", `{"command":"kubectl get nodes"}`)),
		{Content: "当前账号没有集群级资源授权。"},
	}}
	reg := tools.NewRegistry()
	if err := reg.Register(&shellStubTool{executed: &executed}); err != nil {
		t.Fatal(err)
	}
	approver := &stubApprover{choices: []string{ChoiceYes}}
	ag := New(Options{
		LLM:            fl,
		Tools:          reg,
		Session:        &session.Session{ID: "scope"},
		Approver:       approver,
		AccessScope:    &tools.AccessScope{Namespaces: []string{"mysql-dev"}},
		MaxIterations:  3,
		MaxToolCalls:   5,
		TurnUserID:     "u1",
		ApprovalPolicy: nil,
	})
	answer, _, err := ag.Chat(context.Background(), "看下集群有多少节点")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 0 {
		t.Fatalf("out-of-scope tool executed %d times", executed)
	}
	if approver.calls != 0 {
		t.Fatalf("scope denial should not prompt approval, calls = %d", approver.calls)
	}
	if answer == "" {
		t.Fatal("missing final response")
	}
	if prompt := ag.systemPromptFor("native"); !strings.Contains(prompt, "mysql-dev") || !strings.Contains(prompt, "Node") {
		t.Fatalf("scope was not injected into prompt: %s", prompt)
	}
	foundDenial := false
	for _, msg := range ag.Session.Messages {
		if strings.Contains(msg.Content, "SCOPE_DENIED") && strings.Contains(msg.Content, "mysql-dev") {
			foundDenial = true
		}
	}
	if !foundDenial {
		t.Fatal("scope denial was not returned to the model")
	}
}
