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
