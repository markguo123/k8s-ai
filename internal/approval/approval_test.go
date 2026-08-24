package approval

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestClassifyCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want model.RiskLevel
	}{
		{"kubectl get pods -n default", model.RiskSafe},
		{"kubectl logs pod-1 -n default", model.RiskSafe},
		{"kubectl scale deployment/payment-api --replicas=5", model.RiskMedium},
		{"kubectl rollout restart deployment/payment-api", model.RiskMedium},
		{"kubectl rollout undo deployment/payment-api", model.RiskHigh},
		{"kubectl patch deployment/payment-api --patch '{}'", model.RiskHigh},
		{"kubectl delete pod web-0 -n default", model.RiskHigh},
		{"kubectl delete deployment/payment-api -n production", model.RiskCritical},
		{"kubectl delete pvc data -n production", model.RiskCritical},
		{"kubectl delete namespace production", model.RiskCritical},
		{"kubectl exec pod-1 -- ls", model.RiskHigh},
		{"kubectl edit deployment/payment-api", model.RiskCritical},
	}
	for _, c := range cases {
		if got := ClassifyCommand(c.cmd); got != c.want {
			t.Errorf("%s: risk = %s, want %s", c.cmd, got, c.want)
		}
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	if !IsReadOnlyCommand("kubectl get pods") {
		t.Fatal("get 应为只读")
	}
	if IsReadOnlyCommand("kubectl delete pod x") {
		t.Fatal("delete 不应为只读")
	}
}

func TestPolicyRequiresApproval(t *testing.T) {
	p := Policy{RequireApproval: true}
	if !p.RequiresApproval() {
		t.Fatal("默认应要求审批")
	}
	skip := Policy{RequireApproval: true, SkipPermissions: true}
	if skip.RequiresApproval() {
		t.Fatal("skip-permissions 应免审批")
	}
	off := Policy{}
	if off.RequiresApproval() {
		t.Fatal("require_approval=false 应免审批")
	}
}

func TestIsDenied(t *testing.T) {
	if !IsDenied([]string{"delete namespace"}, "kubectl delete namespace production") {
		t.Fatal("应命中 deny_ops")
	}
	if IsDenied(nil, "kubectl get pods") {
		t.Fatal("空 deny_ops 不应拦截")
	}
	if IsDenied([]string{"delete namespace"}, "kubectl get namespaces") {
		t.Fatal("不应误伤只读命令")
	}
}
