// 三期创建通道的风险分级（R0.3 映射表 / R4.1 kind 级分级）。
//
// 这里是 RESOURCE_CREATE_DESIGN.md §5.1 那张表的唯一确定性实现：Code Guard 与
// 审批面板都从这里取结论，不允许在提示词里用文字描述风险。
// 现有 ClassifyCommand 把 kubectl create/apply 一律判 HIGH，无法表达
// “创建 ConfigMap” 与 “创建带 privileged 的 DaemonSet” 的真实差异。
package approval

import (
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 创建请求的作用域取值（与 internal/manifestx.Scope 的字符串值一致；
// approval 不 import manifestx，依赖方向保持 tools → approval + manifestx）。
const (
	CreateScopeNamespaced = "namespaced"
	CreateScopeCluster    = "cluster"
	CreateScopeUnknown    = "unknown"
)

// CreateSubject 是一次创建请求（单个对象）的风险要素。
type CreateSubject struct {
	// Group 是 apiVersion 里的 API 组（core 组为空串）。
	Group string
	// Kind 是资源类型（如 Deployment）。
	Kind string
	// Scope 取 CreateScope* 之一；以集群 discovery 结论为准。
	Scope string
	// Dangerous 是命中的危险字段码（manifestx.Danger* 常量）。
	Dangerous []string
	// Unknown 表示集群里没有该类型（CRD 未安装 / kind 拼错）。
	Unknown bool
	// Secret 表示被创建对象是 Secret（含值创建走 D-06 专用通道）。
	Secret bool
	// DryRun 表示本次只做 server-side 预演，不改变集群。
	DryRun bool
}

// CreateRisk 是风险分级结论（等级 + 可展示的原因）。
type CreateRisk struct {
	Level   model.RiskLevel
	Reasons []string
}

// benignCreateKinds 是已知良性 Kind（RESOURCE_CREATE_DESIGN.md §5.1 首行清单）：
// 仅在「已映射 + namespaced + 无危险字段」时把创建降为 MEDIUM。
// 未知 Kind 一律不在此表，按 HIGH 处理（三期 D-13）。
var benignCreateKinds = map[string]bool{
	"ConfigMap":             true,
	"Secret":                true,
	"Service":               true,
	"ServiceAccount":        true,
	"Deployment":            true,
	"StatefulSet":           true,
	"DaemonSet":             true,
	"Job":                   true,
	"CronJob":               true,
	"Pod":                   true,
	"Ingress":               true,
	"PersistentVolumeClaim": true,
	"NetworkPolicy":         true,
	"Role":                  true,
	"RoleBinding":           true,
}

// CreateRiskLevel 给一次创建请求分级（等价于 CreateRisk(subject).Level）。
func CreateRiskLevel(s CreateSubject) model.RiskLevel {
	return CreateRiskFor(s).Level
}

// CreateRiskFor 分级并给出人类可读原因（审批提案与审计展示用）。
//
// 规则（风险只升不降）：
//   - dry run → SAFE（不改变集群，可自动执行）；
//   - 含值 Secret → MEDIUM（值由用户或 Executor 提供，不读集群既有 Secret，D-06）；
//   - cluster-scoped → HIGH；命中危险字段 → HIGH；未映射 Kind（CRD/拼错）→ HIGH；
//   - 其余「已映射 + namespaced + 良性 Kind」→ MEDIUM（唯一可命中 Grant 的档位）；
//   - 未命中良性表的已映射 Kind → HIGH。
func CreateRiskFor(s CreateSubject) CreateRisk {
	if s.DryRun {
		return CreateRisk{Level: model.RiskSafe, Reasons: []string{"dry run 只做服务端校验，不改变集群"}}
	}
	var reasons []string
	level := model.RiskMedium
	raise := func(l model.RiskLevel, reason string) {
		if riskOrder[l] > riskOrder[level] {
			level = l
		}
		reasons = append(reasons, reason)
	}
	if s.Secret {
		reasons = append(reasons, "含值 Secret 创建：值由用户或执行器生成，不进入模型上下文")
	}
	if s.Unknown {
		raise(model.RiskHigh, "集群中找不到该类型（CRD 未安装或 kind 拼写有误）")
	}
	if s.Scope == CreateScopeCluster || s.Scope == CreateScopeUnknown {
		raise(model.RiskHigh, "cluster-scoped 或未确定作用域，影响范围超出单个命名空间")
	}
	if len(s.Dangerous) > 0 {
		raise(model.RiskHigh, "manifest 含特权/宿主机类字段（"+strings.Join(sortedUnique(s.Dangerous), ", ")+"）")
	}
	if !benignCreateKinds[s.Kind] {
		raise(model.RiskHigh, s.Kind+" 不在已知良性 Kind 表中")
	}
	return CreateRisk{Level: level, Reasons: reasons}
}

// CreateSubjectRisk 对多个对象取最高风险（一次调用可能含多个对象）。
func CreateSubjectRisk(subjects []CreateSubject) CreateRisk {
	if len(subjects) == 0 {
		return CreateRisk{Level: model.RiskSafe}
	}
	level := model.RiskSafe
	seen := map[string]bool{}
	var reasons []string
	for _, s := range subjects {
		r := CreateRiskFor(s)
		if riskOrder[r.Level] > riskOrder[level] {
			level = r.Level
		}
		for _, reason := range r.Reasons {
			if !seen[reason] {
				seen[reason] = true
				reasons = append(reasons, reason)
			}
		}
	}
	return CreateRisk{Level: level, Reasons: reasons}
}

// IsBenignCreateKind 报告 Kind 是否在良性创建表中（审批提案展示与测试断言用）。
func IsBenignCreateKind(kind string) bool { return benignCreateKinds[kind] }

func sortedUnique(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
