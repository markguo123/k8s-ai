package manifestx

import (
	"fmt"
	"sort"
)

// 危险字段码（设计文档 §5.2）：命中即提升风险等级，并在审批提案里定位字段路径。
const (
	DangerPrivileged          = "privileged"
	DangerHostNetwork         = "host_network"
	DangerHostPID             = "host_pid"
	DangerHostIPC             = "host_ipc"
	DangerHostPath            = "host_path"
	DangerHostPort            = "host_port"
	DangerAutomountToken      = "automount_service_account_token"
	DangerPrivilegeEscalation = "allow_privilege_escalation"
)

type dangerRule struct {
	code string
	desc string
	fix  string
}

// dangerBoolFields：布尔字段为 true 时命中。
var dangerBoolFields = map[string]dangerRule{
	"privileged":                   {DangerPrivileged, "容器以 privileged 运行，等同节点 root", "去掉 securityContext.privileged，改用最小能力"},
	"hostNetwork":                  {DangerHostNetwork, "使用宿主机网络栈，可访问节点流量与端口", "去掉 spec.template.spec.hostNetwork"},
	"hostPID":                      {DangerHostPID, "共享宿主机 PID 命名空间，可看到节点进程", "去掉 hostPID"},
	"hostIPC":                      {DangerHostIPC, "共享宿主机 IPC 命名空间", "去掉 hostIPC"},
	"automountServiceAccountToken": {DangerAutomountToken, "显式开启 ServiceAccount token 自动挂载，Pod 内可持集群凭据", "保持默认或显式设为 false"},
	"allowPrivilegeEscalation":     {DangerPrivilegeEscalation, "允许容器内进程提权", "设置 securityContext.allowPrivilegeEscalation=false"},
}

// dangerPresenceFields：字段出现即命中（值不限）。
var dangerPresenceFields = map[string]dangerRule{
	"hostPath": {DangerHostPath, "挂载宿主机路径，可读取节点文件与敏感目录", "改用 emptyDir / PVC 等卷类型，或由管理员审批 hostPath"},
}

// scanDangerous 递归扫描危险字段，输出稳定的结论序列（按 code 排序）。
func scanDangerous(content map[string]any) ([]string, []Finding) {
	hits := map[string]Finding{}
	walkNodes(content, "", func(key, path string, value any) {
		if rule, ok := dangerBoolFields[key]; ok {
			if b, isBool := value.(bool); isBool && b {
				addDanger(hits, rule, path)
			}
			return
		}
		if rule, ok := dangerPresenceFields[key]; ok {
			addDanger(hits, rule, path)
			return
		}
		if key == "hostPort" && !isZeroNumber(value) {
			addDanger(hits, dangerRule{DangerHostPort, "占用宿主机端口，可能与节点服务冲突", "去掉 hostPort，使用 Service 暴露端口"}, path)
		}
	})
	codes := make([]string, 0, len(hits))
	findings := make([]Finding, 0, len(hits))
	for code := range hits {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		findings = append(findings, hits[code])
	}
	return codes, findings
}

func addDanger(hits map[string]Finding, rule dangerRule, path string) {
	if _, ok := hits[rule.code]; ok {
		return // 同一风险只报第一次命中的字段路径
	}
	// 危险字段不直接阻断创建：它把风险提升到 HIGH，由审批环节决定是否放行。
	hits[rule.code] = Finding{Code: CodeDangerousField, Severity: SeverityWarn, Field: path,
		Message: fmt.Sprintf("危险字段 %s：%s", path, rule.desc), Fix: rule.fix}
}

// walkNodes 深度遍历对象，回调每个键值对（数组元素沿用父路径）。
func walkNodes(node any, path string, fn func(key, path string, value any)) {
	switch n := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			fn(k, child, n[k])
			walkNodes(n[k], child, fn)
		}
	case []any:
		for i, item := range n {
			walkNodes(item, fmt.Sprintf("%s[%d]", path, i), fn)
		}
	}
}

// isZeroNumber 判断数值是否等价于 0（hostPort: 0 视作未设置）。
func isZeroNumber(v any) bool {
	switch n := v.(type) {
	case int:
		return n == 0
	case int32:
		return n == 0
	case int64:
		return n == 0
	case float64:
		return n == 0
	default:
		return false
	}
}
