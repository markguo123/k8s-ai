package approval

import "github.com/k8s-ai/k8s-ai/internal/model"

// Mode selects how approval decisions are made. strict preserves the
// v1 behavior; smart is the Phase C observation mode that still requires
// first-time approval and never enables Grant auto-approval yet.
type Mode string

const (
	ModeStrict Mode = "strict"
	ModeSmart  Mode = "smart"
)

// Decision is the normalized approval outcome used by the Agent loop.
// Code Guard in the executor remains the final authority for DENY; this
// decision only determines whether the loop needs a human approver.
type Decision string

const (
	DecisionAuto            Decision = "auto"
	DecisionRequireApproval Decision = "require_approval"
	DecisionDeny            Decision = "deny"
)

// Input is the minimal per-call context the policy needs. Command/risk
// strings are intentionally omitted so approval decisions never log raw
// command text through the audit path.
type Input struct {
	ReadOnly bool
	Risk     model.RiskLevel
	Shell    bool
}

// Evaluate normalizes the v1 behavior into a strict ApprovalPolicy.
//
// Shell is always gated before skip_permissions / require_approval=false,
// matching AGENTS.md: shell constructs require a real human approver and
// are never covered by non-interactive automation.
func (p Policy) Evaluate(in Input) Decision {
	if p.Mode == "" {
		p.Mode = ModeStrict
	}
	if in.Shell {
		return DecisionRequireApproval
	}
	if in.ReadOnly {
		return DecisionAuto
	}
	// Phase C: smart 只观测，不开放 Grant AUTO。首次 MEDIUM/HIGH/CRITICAL
	// 一律 REQUIRE_APPROVAL，行为与 strict 一致；skip/require_approval=false
	// 只影响普通非 shell 写操作（legacy 自动化入口）。
	if p.Mode == ModeSmart {
		// Phase D 会在这里叠加 Authorization Grant 命中判断。
	}
	if p.SkipPermissions {
		return DecisionAuto
	}
	if !p.RequireApproval {
		// Legacy require_approval=false: plain write operations may auto
		// execute, but shell commands above never can.
		return DecisionAuto
	}
	return DecisionRequireApproval
}
