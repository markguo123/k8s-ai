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
	// Denied 是 Code Guard 的硬拒绝结论（kind 黑名单、未 dry run、命名空间缺失等）。
	// 它优先级最高：跳过 SkipPermissions 与 RequireApproval=false，因为这两条
	// 自动化通道只免除「人工确认」，不能免除代码级门禁。
	Denied bool
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
	if in.Denied {
		return DecisionDeny
	}
	if in.Shell {
		return DecisionRequireApproval
	}
	if in.ReadOnly {
		return DecisionAuto
	}
	// strict 与 smart 在本函数里行为一致：首次 MEDIUM/HIGH/CRITICAL 一律
	// REQUIRE_APPROVAL，模式只进入审计用于观测。真正的放行来自 Agent 循环里叠加的
	// Authorization Grant 命中判断（见 Agent.approvalDecisionDetail）：Grant 只会由
	// 用户选「确认，本次不再询问」在 LOW/MEDIUM 风险时产生，HIGH 与 shell 永不命中。
	// skip/require_approval=false 只免除普通非 shell 写操作的人工确认（legacy 自动化
	// 入口），既越不过 shell 闸门，也越不过 Code Guard 的 Denied。
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
