package approval

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func nsSubject(kind string) CreateSubject {
	return CreateSubject{Kind: kind, Scope: CreateScopeNamespaced, Namespace: "prod", DryRun: false}
}

func TestCodeGuardDeniesPolicyBreaches(t *testing.T) {
	cases := []struct {
		name        string
		req         GuardRequest
		wantVerdict GuardVerdict
		wantCode    string
	}{
		{
			name: "deny kind",
			req: GuardRequest{Subjects: []CreateSubject{func() CreateSubject {
				s := nsSubject("Secret")
				s.Denied = true
				return s
			}()}, DryRunVerified: true},
			wantCode: GuardDeniedKind,
		},
		{
			name: "kind not in allow list",
			req: GuardRequest{Subjects: []CreateSubject{func() CreateSubject {
				s := nsSubject("Deployment")
				s.NotAllowed = true
				return s
			}()}, DryRunVerified: true},
			wantCode: GuardKindNotAllowed,
		},
		{
			name: "cluster kind not authorized",
			req: GuardRequest{Subjects: []CreateSubject{func() CreateSubject {
				s := CreateSubject{Kind: "ClusterRole", Scope: CreateScopeCluster}
				s.ClusterKindDenied = true
				return s
			}()}, DryRunVerified: true},
			wantCode: GuardClusterKindDenied,
		},
		{
			name: "missing namespace",
			req: GuardRequest{Subjects: []CreateSubject{func() CreateSubject {
				s := nsSubject("ConfigMap")
				s.NamespaceMissing = true
				return s
			}()}, DryRunVerified: true},
			wantCode: GuardNamespaceMissing,
		},
		{
			name:     "apply without dry run",
			req:      GuardRequest{Subjects: []CreateSubject{nsSubject("ConfigMap")}, DryRunVerified: false},
			wantCode: GuardDryRunRequired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CodeGuard(tc.req)
			if !got.Denied() {
				t.Fatalf("verdict = %s, want deny (codes=%v)", got.Verdict, got.Codes)
			}
			if !got.Has(tc.wantCode) {
				t.Fatalf("codes = %v, want %s", got.Codes, tc.wantCode)
			}
			if strings.Join(got.Reasons, "|") == "" {
				t.Fatal("deny must carry a human readable reason")
			}
		})
	}
}

func TestCodeGuardUnknownKindAndDangerousFieldRequireReview(t *testing.T) {
	// 三期 D-11：CRD 与未知 Kind 均可创建，因此不能 DENY；但必须 REVIEW + HIGH，
	// 绝不能被降档成 ALLOW（ALLOW 意味着写操作可跳过人工审批）。
	unknown := CodeGuard(GuardRequest{
		Subjects:       []CreateSubject{{Kind: "Foo.crd.example.com", Scope: CreateScopeUnknown, Namespace: "prod", Unknown: true}},
		DryRunVerified: true,
	})
	if unknown.Verdict != GuardReview {
		t.Fatalf("unknown kind verdict = %s, want review", unknown.Verdict)
	}
	if unknown.Level != model.RiskHigh {
		t.Fatalf("unknown kind level = %s, want HIGH", unknown.Level)
	}
	if !unknown.Has(GuardUnknownKind) {
		t.Fatalf("unknown kind codes = %v", unknown.Codes)
	}

	dangerous := CodeGuard(GuardRequest{
		Subjects:       []CreateSubject{{Kind: "Deployment", Scope: CreateScopeNamespaced, Namespace: "prod", Dangerous: []string{"privileged"}}},
		DryRunVerified: true,
	})
	if dangerous.Verdict != GuardReview {
		t.Fatalf("dangerous verdict = %s, want review", dangerous.Verdict)
	}
	if dangerous.Level != model.RiskHigh {
		t.Fatalf("dangerous level = %s, want HIGH", dangerous.Level)
	}
	if !dangerous.Has(GuardDangerousField) {
		t.Fatalf("dangerous codes = %v", dangerous.Codes)
	}
}

func TestCodeGuardAllowsDryRunAndRequiresReviewForApply(t *testing.T) {
	dry := CodeGuard(GuardRequest{Subjects: []CreateSubject{{Kind: "ConfigMap", Scope: CreateScopeNamespaced, Namespace: "prod", DryRun: true}}})
	if dry.Verdict != GuardAllow || dry.Level != model.RiskSafe {
		t.Fatalf("dry run guard = %+v, want allow/SAFE", dry)
	}
	apply := CodeGuard(GuardRequest{Subjects: []CreateSubject{nsSubject("ConfigMap")}, DryRunVerified: true})
	if apply.Verdict != GuardReview {
		t.Fatalf("apply guard = %s, want review", apply.Verdict)
	}
	if len(apply.Codes) != 0 {
		t.Fatalf("clean apply guard should carry no codes, got %v", apply.Codes)
	}
	if apply.Level != model.RiskMedium {
		t.Fatalf("apply level = %s, want MEDIUM", apply.Level)
	}
}

func TestCodeGuardDenyWinsOverOtherSubjects(t *testing.T) {
	// 一次调用含多个对象时，只要有一个命中硬拒绝，整批都必须 DENY。
	res := CodeGuard(GuardRequest{Subjects: []CreateSubject{
		{Kind: "ConfigMap", Scope: CreateScopeNamespaced, Namespace: "prod", DryRun: true},
		func() CreateSubject { s := nsSubject("Secret"); s.Denied = true; return s }(),
	}, DryRunVerified: true})
	if !res.Denied() {
		t.Fatalf("mixed batch verdict = %s, want deny", res.Verdict)
	}
	if !res.Has(GuardDeniedKind) {
		t.Fatalf("mixed batch codes = %v", res.Codes)
	}
}

func TestDeniedGuardCannotBeBypassed(t *testing.T) {
	// Code Guard 的 DENY 优先于 SkipPermissions 与 require_approval=false：
	// 这两条自动化通道只免除人工确认，不能免除代码级门禁。
	policies := []Policy{
		{Mode: ModeStrict, RequireApproval: true, SkipPermissions: true},
		{Mode: ModeSmart, RequireApproval: true, SkipPermissions: true},
		{Mode: ModeStrict, RequireApproval: false},
		{Mode: ModeSmart, RequireApproval: false},
	}
	for _, p := range policies {
		got := p.Evaluate(Input{ReadOnly: false, Risk: model.RiskMedium, Denied: true})
		if got != DecisionDeny {
			t.Fatalf("policy %+v with denied guard = %s, want deny", p, got)
		}
	}
}
