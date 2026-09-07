package approval

import "strings"

// Policy 是写操作审批策略（agent.require_approval / --skip-permissions / deny_ops）。
type Policy struct {
	RequireApproval bool
	SkipPermissions bool
	DenyOps         []string
}

// RequiresApproval 判断写操作是否需要人类确认。
func (p Policy) RequiresApproval() bool {
	if p.SkipPermissions {
		return false
	}
	return p.RequireApproval
}

// IsDenied 判断命令是否命中 deny_ops 硬拦截（子串匹配，默认空）。
func IsDenied(denyOps []string, command string) bool {
	low := strings.ToLower(strings.TrimSpace(command))
	for _, op := range denyOps {
		if op != "" && strings.Contains(low, strings.ToLower(op)) {
			return true
		}
	}
	return false
}

// IsForbiddenCommand 判断命令是否触碰 LLM 数据安全硬红线，
// 命中则无论只读/写、无论是否审批，一律拒绝执行：
//   - 红线 1：任何形式的 Secret 资源访问——工具输出会进入 LLM 上下文，
//     违反 "Never send Kubernetes Secret data to the LLM"（AGENTS.md）。
//   - 红线 2：--raw flag——可绕过资源名直接请求任意 API 路径（含 secret data）。
//
// 匹配非 flag 的资源 token：secret、secrets、secret/name。
// 极端情况（如 -n secret 指向名为 secret 的命名空间）会误拦，
// 宁可过严不可漏放。
func IsForbiddenCommand(command string) bool {
	for _, f := range strings.Fields(command) {
		if f == "--raw" || strings.HasPrefix(f, "--raw=") {
			return true
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		if i := strings.Index(f, "/"); i >= 0 {
			f = f[:i] // secret/name 或 kind/name 写法
		}
		if lf := strings.ToLower(f); lf == "secret" || lf == "secrets" {
			return true
		}
	}
	return false
}
