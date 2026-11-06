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
	if err := scope.Check("get_secret_value", json.RawMessage(`{"namespace":"mysql-dev","name":"db","keys":["password"],"reason":"test"}`)); err != nil {
		t.Fatalf("authorized namespace Secret diagnostic denied: %v", err)
	}
	if err := scope.Check("kubectl", json.RawMessage(`{"command":"kubectl get pods -n mysql-dev"}`)); err != nil {
		t.Fatalf("authorized kubectl denied: %v", err)
	}
	if err := scope.Check("kubectl", json.RawMessage(`{"command":"kubectl -n mysql-dev get pods"}`)); err != nil {
		t.Fatalf("authorized kubectl with global namespace flag denied: %v", err)
	}
}

func TestAccessScopeDeniesClusterAndForeignResources(t *testing.T) {
	scope := AccessScope{Namespaces: []string{"mysql-dev"}, CanWrite: true}
	cases := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"nodes", "get_nodes", `{}`, "集群级资源读取"},
		{"foreign namespace", "get_pods", `{"namespace":"default"}`, `namespace "default"`},
		{"all namespaces", "get_pods", `{}`, "显式 namespace"},
		{"foreign Secret", "get_secret_value", `{"namespace":"default","name":"db","keys":["password"],"reason":"test"}`, `namespace "default"`},
		{"full scan", "scan_cluster", `{}`, "集群体检"},
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

func TestAccessScopeWriteRequiresCanWrite(t *testing.T) {
	scope := AccessScope{Namespaces: []string{"test-complex"}}
	cmd := json.RawMessage(`{"command":"kubectl -n test-complex patch deployment app-backend --type=merge -p '{}'"}`)
	if err := scope.Check("kubectl", cmd); err == nil || !strings.Contains(err.Error(), "写权限") {
		t.Fatalf("write without CanWrite should be denied, got %v", err)
	}

	scope.CanWrite = true
	if err := scope.Check("kubectl", cmd); err != nil {
		t.Fatalf("write with CanWrite should be allowed into approval: %v", err)
	}
}

func TestAccessScopeDestroyRequiresCanDestroy(t *testing.T) {
	scope := AccessScope{Namespaces: []string{"test-complex"}, CanWrite: true}
	cmd := json.RawMessage(`{"command":"kubectl -n test-complex delete pvc app-data"}`)
	if err := scope.Check("kubectl", cmd); err == nil || !strings.Contains(err.Error(), "销毁权限") {
		t.Fatalf("destroy without CanDestroy should be denied, got %v", err)
	}

	scope.CanDestroy = true
	if err := scope.Check("kubectl", cmd); err != nil {
		t.Fatalf("destroy with CanDestroy should be allowed into approval: %v", err)
	}
}

func TestAccessScopeDeletePodDoesNotRequireDestroy(t *testing.T) {
	scope := AccessScope{Namespaces: []string{"test-complex"}, CanWrite: true}
	cmd := json.RawMessage(`{"command":"kubectl -n test-complex delete pod app-backend-0"}`)
	if err := scope.Check("kubectl", cmd); err != nil {
		t.Fatalf("delete pod should use normal write capability: %v", err)
	}
}

func TestAccessScopePrompt(t *testing.T) {
	prompt := (AccessScope{Namespaces: []string{"mysql-dev"}}).Prompt()
	if !strings.Contains(prompt, "mysql-dev") || !strings.Contains(prompt, "Node、PV、StorageClass") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "只读") {
		t.Fatalf("read-only scope must be explicit in prompt: %q", prompt)
	}
}
