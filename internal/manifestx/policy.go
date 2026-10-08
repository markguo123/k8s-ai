package manifestx

import (
	"fmt"
	"strings"
)

// Policy 是 kind / scope 门禁（来自 agent.create.*）。
//
// 语义（三期 D-11）：deny_kinds 黑名单始终生效；allow_kinds 为空＝不限制；
// cluster_kinds 为空＝禁止一切 cluster-scoped 创建。
type Policy struct {
	DenyKinds    []string
	AllowKinds   []string
	ClusterKinds []string
}

// check 执行 kind 黑名单、可选白名单与 cluster-scoped 授权闸门。
func (p Policy) check(o *Object) []Finding {
	var findings []Finding
	if kindList(p.DenyKinds).match(o) {
		findings = append(findings, Finding{Code: CodeDeniedKind, Severity: SeverityError, Field: "kind",
			Message: fmt.Sprintf("%s 在 agent.create.deny_kinds 黑名单中，禁止创建", o.Kind),
			Fix:     "如确需创建，请由管理员调整 agent.create.deny_kinds"})
	}
	if len(p.AllowKinds) > 0 && !kindList(p.AllowKinds).match(o) {
		findings = append(findings, Finding{Code: CodeKindNotAllowed, Severity: SeverityError, Field: "kind",
			Message: fmt.Sprintf("%s 不在 agent.create.allow_kinds 白名单中", o.Kind),
			Fix:     "改用白名单内的资源类型，或由管理员扩展 agent.create.allow_kinds"})
	}
	if o.Scope == ScopeCluster && !kindList(p.ClusterKinds).match(o) {
		findings = append(findings, Finding{Code: CodeClusterKindDenied, Severity: SeverityError, Field: "kind",
			Message: fmt.Sprintf("%s 是 cluster-scoped 资源，未列入 agent.create.cluster_kinds，禁止创建", o.Kind),
			Fix:     "cluster 级资源影响全局，请改用命名空间级方案，或由管理员显式授权 agent.create.cluster_kinds"})
	}
	return findings
}

// kindList 是 kind 名单，支持 Kind、复数名、resource 名与 group/resource 形式匹配。
type kindList []string

func (l kindList) match(o *Object) bool {
	if len(l) == 0 {
		return false
	}
	wanted := make(map[string]bool, len(l))
	for _, item := range l {
		if key := normalizeKind(item); key != "" {
			wanted[key] = true
		}
	}
	for _, alias := range o.aliases() {
		if wanted[normalizeKind(alias)] {
			return true
		}
	}
	return false
}

// aliases 返回一个对象可被名单引用的所有写法。
func (o *Object) aliases() []string {
	out := make([]string, 0, 4)
	if o.Kind != "" {
		out = append(out, o.Kind, plural(o.Kind))
	}
	if o.GVR.Resource != "" {
		out = append(out, o.GVR.Resource)
		if o.GVR.Group != "" {
			out = append(out, o.GVR.Resource+"."+o.GVR.Group)
		}
	}
	return out
}

// normalizeKind 归一化名单项：去组前缀、大小写不敏感、去分隔符、单复数同形。
func normalizeKind(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.NewReplacer("-", "", "_", "", " ", "").Replace(s)
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, "s")
	s = strings.TrimSuffix(s, "es")
	return s
}

// plural 给出粗粒度复数形式（仅用于名单匹配别名）。
func plural(kind string) string {
	switch {
	case strings.HasSuffix(kind, "y"):
		return strings.TrimSuffix(kind, "y") + "ies"
	case strings.HasSuffix(kind, "s"), strings.HasSuffix(kind, "x"), strings.HasSuffix(kind, "z"):
		return kind + "es"
	default:
		return kind + "s"
	}
}
