// 三期创建通道的 Code Guard 门禁（R4.2）。
//
// 这里是 RESOURCE_CREATE_DESIGN.md §5.2 强化点清单的确定性实现：kind 黑名单、
// allow_kinds 白名单、cluster 授权闸门、dry-run 强制、namespace 存在性由代码裁决，
// 命中即 DENY，人工审批与 --skip-permissions 都不能放行；危险字段与未映射 Kind
// 只把风险提升到 HIGH（必人工审批、永不命中 Grant），与 §5.1 风险表和三期 D-11
// 「CRD 与未知 Kind 均可创建」保持一致。风险等级本身来自 create_risk.go。
package approval

import (
	"sort"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// GuardVerdict 是 Code Guard 的结论。
type GuardVerdict string

const (
	// GuardAllow 可自动执行（dry run 只读通道，不改变集群）。
	GuardAllow GuardVerdict = "allow"
	// GuardReview 需要人工审批（是否可被已有 Grant 放行仍由 ApprovalPolicy 决定）。
	GuardReview GuardVerdict = "review"
	// GuardDeny 硬拒绝：人工审批与 --skip-permissions 均不可放行。
	GuardDeny GuardVerdict = "deny"
)

// 门禁码（稳定契约：进入审批提案与 approval.decision 审计）。
const (
	GuardDeniedKind        = "denied_kind"
	GuardKindNotAllowed    = "kind_not_allowed"
	GuardClusterKindDenied = "cluster_kind_denied"
	GuardDryRunRequired    = "dry_run_required"
	GuardNamespaceMissing  = "namespace_missing"
	GuardDangerousField    = "dangerous_field"
	GuardUnknownKind       = "unknown_kind"
)

// GuardRequest 是一次工具调用的门禁输入。
type GuardRequest struct {
	// Subjects 是本次调用涉及的对象要素（一次调用可含多个对象）。
	Subjects []CreateSubject
	// DryRunVerified 表示这批内容已通过 server-side dry run（apply 前置条件）。
	DryRunVerified bool
}

// GuardResult 是门禁结论。Codes 去重并按字典序排列，便于审计比对。
type GuardResult struct {
	Verdict GuardVerdict    `json:"verdict"`
	Codes   []string        `json:"codes,omitempty"`
	Reasons []string        `json:"reasons,omitempty"`
	Level   model.RiskLevel `json:"level,omitempty"`
}

// Denied 报告是否硬拒绝。
func (r GuardResult) Denied() bool { return r.Verdict == GuardDeny }

// Has 报告结论里是否含某个门禁码。
func (r GuardResult) Has(code string) bool {
	for _, c := range r.Codes {
		if c == code {
			return true
		}
	}
	return false
}

// CodeGuard 裁决一次创建调用（只读通道与写通道共用同一入口）。
//
// 判定顺序：先收集全部硬拒绝项（一次性摊开，便于用户一次改对），
// 再看是否需要人工审批；只有「全部对象都是 dry run 且无门禁命中」才 ALLOW。
func CodeGuard(req GuardRequest) GuardResult {
	risk := CreateSubjectRisk(req.Subjects)
	res := GuardResult{Verdict: GuardAllow, Level: risk.Level}
	deny := false
	review := false
	add := func(code, reason string) {
		res.Codes = appendUnique(res.Codes, code)
		res.Reasons = appendUnique(res.Reasons, reason)
	}
	dryRunGap := false
	for _, s := range req.Subjects {
		subject := s.Kind
		if subject == "" {
			subject = "该对象"
		}
		if s.Namespace != "" {
			subject += "（命名空间 " + s.Namespace + "）"
		}
		if s.Denied {
			deny = true
			add(GuardDeniedKind, subject+" 命中 agent.create.deny_kinds 黑名单，禁止创建")
		}
		if s.NotAllowed {
			deny = true
			add(GuardKindNotAllowed, subject+" 不在 agent.create.allow_kinds 白名单中")
		}
		if s.ClusterKindDenied {
			deny = true
			add(GuardClusterKindDenied, subject+" 是 cluster-scoped 资源，未列入 agent.create.cluster_kinds")
		}
		if s.NamespaceMissing {
			deny = true
			add(GuardNamespaceMissing, "命名空间 "+s.Namespace+" 不存在：本次调用不自动创建命名空间，请先显式创建或改用已有命名空间")
		}
		if !s.DryRun {
			review = true
			if !req.DryRunVerified {
				dryRunGap = true
			}
		}
		if s.Unknown {
			// 未映射类型的作用域与 RBAC 影响都不可知：只提升风险，不自动放行（D-11）。
			review = true
			add(GuardUnknownKind, subject+" 在集群中找不到对应类型，风险按 HIGH 处理，必须人工确认")
		}
		if len(s.Dangerous) > 0 {
			review = true
			add(GuardDangerousField, subject+" 含特权/宿主机类字段，风险提升为 HIGH，必须人工确认")
		}
	}
	if dryRunGap {
		deny = true
		add(GuardDryRunRequired, "apply 前必须通过同一份清单的 server-side dry run：请先 mode=dry_run 产出提案并交用户确认")
	}
	switch {
	case deny:
		res.Verdict = GuardDeny
	case review:
		res.Verdict = GuardReview
	}
	sort.Strings(res.Codes)
	if res.Level == "" {
		res.Level = risk.Level
	}
	return res
}

func appendUnique(in []string, s string) []string {
	for _, item := range in {
		if item == s {
			return in
		}
	}
	return append(in, s)
}
