package approval

import (
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func mediumGrant() Grant {
	return Grant{
		ID:          "g1",
		TaskID:      "task-1",
		Operation:   "scale",
		Kind:        "deployments",
		Name:        "payment-api",
		Namespace:   "production",
		Cluster:     "prod",
		RiskCeiling: GrantRiskCeiling,
		CreatedAt:   time.Now(),
	}
}

func TestGrantMatchesMedium(t *testing.T) {
	g := mediumGrant()
	req := GrantRequest{
		TaskID:    "task-1",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "payment-api",
		Namespace: "production",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	}
	if !g.Matches(req) {
		t.Fatal("medium grant should match the same operation/resource")
	}
}

func TestGrantNamespaceMismatch(t *testing.T) {
	g := mediumGrant()
	req := GrantRequest{
		TaskID:    "task-1",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "payment-api",
		Namespace: "development",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	}
	if g.Matches(req) {
		t.Fatal("grant must not cross namespace")
	}
}

func TestGrantTaskMismatch(t *testing.T) {
	g := mediumGrant()
	req := GrantRequest{
		TaskID:    "task-2",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "payment-api",
		Namespace: "production",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	}
	if g.Matches(req) {
		t.Fatal("grant must not cross task")
	}
}

func TestGrantHighAndCriticalNeverMatch(t *testing.T) {
	g := mediumGrant()
	for _, risk := range []model.RiskLevel{model.RiskHigh, model.RiskCritical} {
		req := GrantRequest{
			TaskID:    "task-1",
			Operation: "scale",
			Kind:      "deployments",
			Name:      "payment-api",
			Namespace: "production",
			Cluster:   "prod",
			Risk:      risk,
		}
		if g.Matches(req) {
			t.Fatalf("grant must never auto-approve risk %s in Phase D", risk)
		}
	}
}

func TestGrantShellNeverMatches(t *testing.T) {
	g := mediumGrant()
	req := GrantRequest{
		TaskID:    "task-1",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "payment-api",
		Namespace: "production",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
		Shell:     true,
	}
	if g.Matches(req) {
		t.Fatal("shell constructs must never be auto-approved by a grant")
	}
}

func TestNewGrantEligibility(t *testing.T) {
	base := GrantRequest{TaskID: "task-1", Risk: model.RiskMedium}
	if g, ok := NewGrant(base); !ok || g.ID == "" {
		t.Fatal("medium write should produce a grant")
	}
	for _, risk := range []model.RiskLevel{model.RiskSafe, model.RiskHigh, model.RiskCritical} {
		if _, ok := NewGrant(GrantRequest{Risk: risk}); ok {
			t.Fatalf("risk %s must not be grant-eligible", risk)
		}
	}
	if _, ok := NewGrant(GrantRequest{Risk: model.RiskMedium, Shell: true}); ok {
		t.Fatal("shell write must not be grant-eligible")
	}
}

func TestMatchGrantReturnsFirst(t *testing.T) {
	g1 := mediumGrant()
	g1.ID = "g1"
	g2 := mediumGrant()
	g2.ID = "g2"
	// Make g1 non-matching by changing operation, g2 matching.
	g1.Operation = "label"
	req := GrantRequest{
		TaskID:    "task-1",
		Operation: "scale",
		Kind:      "deployments",
		Name:      "payment-api",
		Namespace: "production",
		Cluster:   "prod",
		Risk:      model.RiskMedium,
	}
	got := MatchGrant([]Grant{g1, g2}, req)
	if got == nil {
		t.Fatal("expected a matching grant")
	}
	if got.ID != "g2" {
		t.Fatalf("matched grant id = %s, want g2", got.ID)
	}
}

func TestParseCommandTarget(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    CommandTarget
	}{
		{
			name:    "scale deployment friendly syntax",
			command: "kubectl scale deployment/payment-api --replicas=5 -n prod",
			want:    CommandTarget{Operation: "scale", Kind: "deployment", Name: "payment-api", Namespace: "prod"},
		},
		{
			name:    "delete with namespace before verb",
			command: "kubectl -n prod delete pvc app-data",
			want:    CommandTarget{Operation: "delete", Kind: "pvc", Name: "app-data", Namespace: "prod"},
		},
		{
			name:    "rollout subcommand",
			command: "kubectl rollout restart deployment/web -n prod",
			want:    CommandTarget{Operation: "rollout restart", Kind: "deployment", Name: "web", Namespace: "prod"},
		},
		{
			name:    "shell pipeline is not grantable",
			command: "kubectl get pods | kubectl delete pod x",
			want:    CommandTarget{},
		},
		{
			name:    "unknown verb is not grantable",
			command: "kubectl frobnicate thing -n foo",
			want:    CommandTarget{},
		},
	}
	for _, c := range cases {
		got := ParseCommandTarget(c.command)
		if got != c.want {
			t.Errorf("%s: ParseCommandTarget(%q) = %+v, want %+v", c.name, c.command, got, c.want)
		}
	}
}
