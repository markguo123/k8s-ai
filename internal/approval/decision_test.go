package approval

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestPolicyEvaluateStrict(t *testing.T) {
	cases := []struct {
		name   string
		policy Policy
		in     Input
		want   Decision
	}{
		{
			name:   "read-only auto",
			policy: Policy{Mode: ModeStrict, RequireApproval: true},
			in:     Input{ReadOnly: true, Risk: model.RiskSafe},
			want:   DecisionAuto,
		},
		{
			name:   "shell plus skip-permissions still requires approval",
			policy: Policy{Mode: ModeStrict, RequireApproval: true, SkipPermissions: true},
			in:     Input{ReadOnly: false, Risk: model.RiskHigh, Shell: true},
			want:   DecisionRequireApproval,
		},
		{
			name:   "shell plus require_approval false still requires approval",
			policy: Policy{Mode: ModeStrict, RequireApproval: false},
			in:     Input{ReadOnly: false, Risk: model.RiskCritical, Shell: true},
			want:   DecisionRequireApproval,
		},
		{
			name:   "plain write with skip-permissions auto",
			policy: Policy{Mode: ModeStrict, RequireApproval: true, SkipPermissions: true},
			in:     Input{ReadOnly: false, Risk: model.RiskMedium},
			want:   DecisionAuto,
		},
		{
			name:   "plain write with require_approval false auto",
			policy: Policy{Mode: ModeStrict, RequireApproval: false},
			in:     Input{ReadOnly: false, Risk: model.RiskMedium},
			want:   DecisionAuto,
		},
		{
			name:   "plain write strict requires approval",
			policy: Policy{Mode: ModeStrict, RequireApproval: true},
			in:     Input{ReadOnly: false, Risk: model.RiskMedium},
			want:   DecisionRequireApproval,
		},
		{
			name:   "smart read-only auto",
			policy: Policy{Mode: ModeSmart, RequireApproval: true},
			in:     Input{ReadOnly: true, Risk: model.RiskSafe},
			want:   DecisionAuto,
		},
		{
			name:   "smart medium first write requires approval",
			policy: Policy{Mode: ModeSmart, RequireApproval: true},
			in:     Input{ReadOnly: false, Risk: model.RiskMedium},
			want:   DecisionRequireApproval,
		},
		{
			name:   "smart high first write requires approval",
			policy: Policy{Mode: ModeSmart, RequireApproval: true},
			in:     Input{ReadOnly: false, Risk: model.RiskHigh},
			want:   DecisionRequireApproval,
		},
		{
			name:   "smart critical first write requires approval",
			policy: Policy{Mode: ModeSmart, RequireApproval: true},
			in:     Input{ReadOnly: false, Risk: model.RiskCritical},
			want:   DecisionRequireApproval,
		},
		{
			name:   "smart shell never auto even with skip-permissions",
			policy: Policy{Mode: ModeSmart, RequireApproval: true, SkipPermissions: true},
			in:     Input{ReadOnly: false, Risk: model.RiskHigh, Shell: true},
			want:   DecisionRequireApproval,
		},
		{
			name:   "smart plain write with skip-permissions auto (legacy)",
			policy: Policy{Mode: ModeSmart, RequireApproval: true, SkipPermissions: true},
			in:     Input{ReadOnly: false, Risk: model.RiskMedium},
			want:   DecisionAuto,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.Evaluate(tc.in); got != tc.want {
				t.Fatalf("Evaluate(%+v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
