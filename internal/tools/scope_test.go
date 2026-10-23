package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccessScopeAllowsAuthorizedNamespace(t *testing.T) {
	scope := NormalizeScope(AccessScope{Namespaces: []string{"mysql-dev", "mysql-dev"}})
	if err := scope.Check("get_pods", json.RawMessage(`{"namespace":"mysql-dev"}`)); err != nil {
		t.Fatalf("authorized namespace denied: %v", err)
	}
	if err := scope.Check("kubectl", json.RawMessage(`{"command":"kubectl get pods -n mysql-dev"}`)); err != nil {
		t.Fatalf("authorized kubectl denied: %v", err)
	}
	if err := scope.Check("kubectl", json.RawMessage(`{"command":"kubectl -n mysql-dev get pods"}`)); err != nil {
		t.Fatalf("authorized kubectl with global namespace flag denied: %v", err)
	}
}

func TestAccessScopeDeniesClusterAndForeignResources(t *testing.T) {
	scope := AccessScope{Namespaces: []string{"mysql-dev"}}
	cases := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"nodes", "get_nodes", `{}`, "集群级资源读取"},
		{"foreign namespace", "get_pods", `{"namespace":"default"}`, `namespace "default"`},
		{"all namespaces", "get_pods", `{}`, "显式 namespace"},
		{"full scan", "scan_cluster", `{}`, "全集群巡检"},
		{"resource kind", "get_resources", `{"kind":"nodes"}`, "集群级资源读取"},
		{"kubectl nodes", "kubectl", `{"command":"kubectl get nodes"}`, "集群级资源读取"},
		{"kubectl shell", "kubectl", `{"command":"kubectl get pods -n mysql-dev | head"}`, "shell 构造"},
		{"manifest foreign namespace", "create_resource", `{"manifest":"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n  namespace: default\n"}`, `namespace "default"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scope.Check(tc.tool, json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), "SCOPE_DENIED") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want SCOPE_DENIED containing %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "mysql-dev") {
				t.Fatalf("error should list authorized namespaces: %v", err)
			}
		})
	}
}

func TestAccessScopeAdminIsUnrestricted(t *testing.T) {
	scope := AccessScope{IsAdmin: true, ClusterScoped: true}
	if err := scope.Check("get_nodes", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := scope.Check("get_pods", json.RawMessage(`{"namespace":"anything"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestAccessScopePrompt(t *testing.T) {
	prompt := (AccessScope{Namespaces: []string{"mysql-dev"}}).Prompt()
	if !strings.Contains(prompt, "mysql-dev") || !strings.Contains(prompt, "Node、PV、StorageClass") {
		t.Fatalf("prompt = %q", prompt)
	}
}
