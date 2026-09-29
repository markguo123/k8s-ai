package approval

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestCreateRiskForTable 覆盖 RESOURCE_CREATE_DESIGN.md §5.1 的分级表。
func TestCreateRiskForTable(t *testing.T) {
	cases := []struct {
		name string
		sub  CreateSubject
		want model.RiskLevel
	}{
		{"dry run 不改变集群", CreateSubject{Kind: "Deployment", Scope: CreateScopeNamespaced, DryRun: true}, model.RiskSafe},
		{"命名空间级良性 Kind", CreateSubject{Kind: "ConfigMap", Scope: CreateScopeNamespaced}, model.RiskMedium},
		{"含值 Secret 仍是 MEDIUM", CreateSubject{Kind: "Secret", Scope: CreateScopeNamespaced, Secret: true}, model.RiskMedium},
		{"cluster-scoped 升级", CreateSubject{Kind: "ClusterRole", Scope: CreateScopeCluster}, model.RiskHigh},
		{"作用域未知升级", CreateSubject{Kind: "ConfigMap", Scope: CreateScopeUnknown}, model.RiskHigh},
		{"未映射 Kind 升级", CreateSubject{Kind: "Widget", Scope: CreateScopeNamespaced, Unknown: true}, model.RiskHigh},
		{"特权字段升级", CreateSubject{Kind: "DaemonSet", Scope: CreateScopeNamespaced, Dangerous: []string{"privileged"}}, model.RiskHigh},
		{"Skill 风险规则升级", CreateSubject{Kind: "ConfigMap", Scope: CreateScopeNamespaced, Escalations: []string{"[create-network:host_network] 启用宿主机网络"}}, model.RiskHigh},
		{"非良性 Kind 升级", CreateSubject{Kind: "HorizontalPodAutoscaler", Scope: CreateScopeNamespaced}, model.RiskHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CreateRiskLevel(tc.sub); got != tc.want {
				t.Fatalf("risk = %s, want %s (%+v)", got, tc.want, tc.sub)
			}
		})
	}
}

func TestCreateRiskForGivesReasons(t *testing.T) {
	got := CreateRiskFor(CreateSubject{Kind: "ClusterRole", Scope: CreateScopeCluster})
	if len(got.Reasons) == 0 {
		t.Fatal("cluster-scoped 必须给出可展示的原因")
	}
	if CreateRiskFor(CreateSubject{Kind: "ConfigMap", Scope: CreateScopeNamespaced}).Level != model.RiskMedium {
		t.Fatal("良性创建应为 MEDIUM")
	}
}

// TestCreateSubjectRiskTakesHighest 保证一次多对象调用按最高风险审批。
func TestCreateSubjectRiskTakesHighest(t *testing.T) {
	risk := CreateSubjectRisk([]CreateSubject{
		{Kind: "ConfigMap", Scope: CreateScopeNamespaced},
		{Kind: "Namespace", Scope: CreateScopeCluster},
		{Kind: "Secret", Scope: CreateScopeNamespaced, Secret: true},
	})
	if risk.Level != model.RiskHigh {
		t.Fatalf("risk = %s, want HIGH", risk.Level)
	}
	seen := map[string]bool{}
	for _, reason := range risk.Reasons {
		if seen[reason] {
			t.Fatalf("重复原因：%q", reason)
		}
		seen[reason] = true
	}
	if CreateSubjectRisk(nil).Level != model.RiskSafe {
		t.Fatal("空清单应为 SAFE（由调用方按无效入参拒绝）")
	}
}

// TestGrantNeverMatchesNoGrant 防止 create_resource 这类无单目标调用被既有授权静默放行。
func TestGrantNeverMatchesNoGrant(t *testing.T) {
	base := GrantRequest{TaskID: "t1", Operation: "create_resource", Kind: "ConfigMap", Name: "web", Namespace: "default", Risk: model.RiskMedium}
	grant, ok := NewGrant(base)
	if !ok {
		t.Fatal("单对象 MEDIUM 创建应能生成授权")
	}
	broad := base
	broad.Kind, broad.Name, broad.NoGrant = "", "", true
	if grant.Matches(broad) {
		t.Fatal("NoGrant 调用不得命中任何授权")
	}
	if _, ok := NewGrant(broad); ok {
		t.Fatal("NoGrant 调用不得生成新授权")
	}
	if !grant.Matches(base) {
		t.Fatal("同目标调用应命中授权")
	}
}

func TestIsBenignCreateKind(t *testing.T) {
	for _, kind := range []string{"ConfigMap", "Secret", "Deployment", "PersistentVolumeClaim"} {
		if !IsBenignCreateKind(kind) {
			t.Fatalf("%s 应在良性表中", kind)
		}
	}
	for _, kind := range []string{"Node", "CustomResourceDefinition", "Widget"} {
		if IsBenignCreateKind(kind) {
			t.Fatalf("%s 不应在良性表中", kind)
		}
	}
}
