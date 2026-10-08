package skills

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestNewBuiltinLoadsFiveDomains(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin: %v", err)
	}
	got := r.List()
	if len(got) != 5 {
		t.Fatalf("加载到 %d 个 skill，want 5", len(got))
	}
	want := []string{
		"create-config",
		"create-network",
		"create-rbac",
		"create-storage",
		"create-workload",
	}
	names := make([]string, 0, len(got))
	for _, s := range got {
		if s.Source != SourceBuiltin {
			t.Errorf("skill %s source = %q, want %q", s.Name, s.Source, SourceBuiltin)
		}
		if s.Path == "" {
			t.Errorf("skill %s 未带来源路径", s.Name)
		}
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("skills = %v, want %v", names, want)
	}
}

func TestBuiltinForGVK(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin: %v", err)
	}
	cases := []struct {
		gvk  schema.GroupVersionKind
		want string
	}{
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "create-workload"},
		{schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "create-config"},
		{schema.GroupVersionKind{Group: "", Version: "v1", Kind: "PersistentVolumeClaim"}, "create-storage"},
		{schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}, "create-network"},
		{schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, "create-rbac"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			skills := r.ForGVK(tc.gvk)
			for _, s := range skills {
				if s.Name == tc.want {
					return
				}
			}
			t.Fatalf("ForGVK(%v) 未命中 %s: %+v", tc.gvk, tc.want, skills)
		})
	}
	if got := r.ForGVK(schema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "Widget"}); len(got) != 0 {
		t.Fatalf("未知 Kind 不应命中内置 skill: %+v", got)
	}
}

func TestMatchIntent(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin: %v", err)
	}
	if len(r.MatchIntent("workload")) == 0 {
		t.Fatal("意图 workload 未命中")
	}
	if len(r.MatchIntent("STORAGE")) == 0 {
		t.Fatal("意图 STORAGE 未命中（大小写不敏感）")
	}
	if got := r.MatchIntent(""); got != nil {
		t.Fatalf("空意图应返回 nil，实际 %v", got)
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	r := New()
	s := Skill{
		Name:    "x",
		Source:  SourceBuiltin,
		Kinds:   []string{"v1/ConfigMap"},
		Risk:    RiskRule{Default: RiskMedium},
		Version: "1.0.0",
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("首次注册: %v", err)
	}
	if err := r.Register(s); err == nil {
		t.Fatal("重复注册应报错")
	}
}

func TestRegisterDefaultsEmptySourceToBuiltin(t *testing.T) {
	r := New()
	s := Skill{
		Name:  "y",
		Kinds: []string{"v1/ConfigMap"},
		Risk:  RiskRule{Default: RiskMedium},
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("注册: %v", err)
	}
	for _, got := range r.List() {
		if got.Source != SourceBuiltin {
			t.Fatalf("source = %q, want %q", got.Source, SourceBuiltin)
		}
	}
}
