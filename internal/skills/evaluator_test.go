package skills

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func evalGVK(group, version, kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
}

func validateContent(t *testing.T, r Registry, gvk schema.GroupVersionKind, content map[string]any, scope Scope) []Finding {
	t.Helper()
	v, ok := r.(Validator)
	if !ok {
		t.Fatalf("Registry 未实现 Validator: %T", r)
	}
	return v.Validate(gvk, content, scope)
}

func hasRule(finds []Finding, rule string) *Finding {
	for i := range finds {
		if finds[i].Rule == rule {
			return &finds[i]
		}
	}
	return nil
}

func TestEvaluateUnpinnedImageProducesSuggestion(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "web", "image": "registry:5000/dbaas-system/mysql:latest"},
					},
				},
			},
		},
	}
	finds := validateContent(t, r, evalGVK("apps", "v1", "Deployment"), content, ScopeNamespaced)
	f := hasRule(finds, "image_unpinned")
	if f == nil {
		t.Fatalf("未命中 image_unpinned: %+v", finds)
	}
	if f.Risk != "" {
		t.Fatalf("未固定镜像只应建议，不应升级，got risk %q", f.Risk)
	}
	if f.Fix == "" {
		t.Fatal("image_unpinned 应带 fix")
	}
}

func TestEvaluatePrivilegedContainerRaisesToHigh(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"spec": map[string]any{
			"hostNetwork": false,
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":            "web",
							"image":           "example.com/web:1.0",
							"resources":       map[string]any{"limits": map[string]any{"cpu": "1"}},
							"securityContext": map[string]any{"privileged": true},
						},
					},
				},
			},
		},
	}
	finds := validateContent(t, r, evalGVK("apps", "v1", "Deployment"), content, ScopeNamespaced)
	f := hasRule(finds, "privileged_container")
	if f == nil {
		t.Fatalf("未命中 privileged_container: %+v", finds)
	}
	if f.Risk != RiskHigh {
		t.Fatalf("privileged 必须升档 HIGH, got %q", f.Risk)
	}
}

func TestEvaluateHostNetworkRaisesToHigh(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"spec": map[string]any{
			"hostNetwork": true,
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":      "web",
							"image":     "example.com/web:1.0",
							"resources": map[string]any{"limits": map[string]any{"cpu": "1"}},
						},
					},
				},
			},
		},
	}
	finds := validateContent(t, r, evalGVK("apps", "v1", "DaemonSet"), content, ScopeNamespaced)
	f := hasRule(finds, "host_network")
	if f == nil {
		t.Fatalf("未命中 host_network: %+v", finds)
	}
	if f.Risk != RiskHigh {
		t.Fatalf("hostNetwork 必须升档 HIGH, got %q", f.Risk)
	}
}

func TestEvaluateClusterRoleRaisesClusterRisk(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"rules": []any{map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}}},
	}
	finds := validateContent(t, r, evalGVK("rbac.authorization.k8s.io", "v1", "ClusterRole"), content, ScopeCluster)
	if f := hasRule(finds, "cluster_admin_like"); f == nil || f.Risk != RiskHigh {
		t.Fatalf("cluster-scoped 权限未触发 cluster_admin_like: %+v", finds)
	}
}

func TestEvaluateClusterScopedStorageRaisesToHigh(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"spec": map[string]any{
			"storageClassName": "fast",
			"accessModes":      []any{"ReadWriteOnce"},
			"resources":        map[string]any{"requests": map[string]any{"storage": "10Gi"}},
		},
	}
	for _, scope := range []Scope{ScopeCluster, ScopeUnknown} {
		finds := validateContent(t, r, evalGVK("", "v1", "PersistentVolume"), content, scope)
		f := hasRule(finds, "cluster_storage")
		if f == nil || f.Risk != RiskHigh {
			t.Fatalf("scope=%s 未触发 cluster_storage 升档: %+v", scope, finds)
		}
	}
}

func TestEvaluateWellFormedConfigMapHasNoFindings(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"data":     map[string]any{"mode": "prod"},
		"metadata": map[string]any{"labels": map[string]any{"app": "web"}},
	}
	finds := validateContent(t, r, evalGVK("", "v1", "ConfigMap"), content, ScopeNamespaced)
	if len(finds) != 0 {
		t.Fatalf("合规 ConfigMap 不应有 finding: %+v", finds)
	}
}

func TestEvaluateMissingResourcesSuggestion(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "web", "image": "example.com/web:1.0"}},
				},
			},
		},
	}
	finds := validateContent(t, r, evalGVK("apps", "v1", "Deployment"), content, ScopeNamespaced)
	if f := hasRule(finds, "resources_declared"); f == nil {
		t.Fatalf("未命中 resources_declared: %+v", finds)
	}
}
