// Package approval 实现二期写操作的风险分级与审批策略
// （SAFE..CRITICAL 五级，需求规格 §13.3）。
package approval

import (
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// readOnlyVerbs 是只读动词（自动执行，无需审批）。
var readOnlyVerbs = map[string]bool{
	"get": true, "describe": true, "logs": true, "events": true,
	"top": true, "explain": true, "api-resources": true, "version": true,
}

// readOnlyRolloutSubs 是 rollout 的只读子命令（history/status，借鉴 kubectl-ai）。
var readOnlyRolloutSubs = map[string]bool{
	"history": true, "status": true,
}

// writeRisks 是写动词的基础风险（delete/rollout 等按资源再细分）。
// 动词表对齐 kubectl-ai kubectl_filter.go 的 writeOps：autoscale/expose/taint/
// reconcile/certificate 等纳入审批流。
var writeRisks = map[string]model.RiskLevel{
	"scale":        model.RiskMedium,
	"annotate":     model.RiskMedium,
	"label":        model.RiskMedium,
	"set":          model.RiskMedium,
	"rollout":      model.RiskMedium,
	"autoscale":    model.RiskMedium,
	"expose":       model.RiskMedium,
	"taint":        model.RiskMedium,
	"patch":        model.RiskHigh,
	"apply":        model.RiskHigh,
	"create":       model.RiskHigh,
	"exec":         model.RiskHigh,
	"drain":        model.RiskHigh,
	"cordon":       model.RiskHigh,
	"uncordon":     model.RiskHigh,
	"reconcile":    model.RiskHigh,
	"certificate":  model.RiskHigh,
	"edit":         model.RiskCritical,
	"attach":       model.RiskCritical,
	"port-forward": model.RiskCritical,
}

// criticalDeleteResources 是 delete 命中即 CRITICAL 的资源。
var criticalDeleteResources = map[string]bool{
	"deployment": true, "deployments": true,
	"statefulset": true, "statefulsets": true,
	"service": true, "services": true,
	"pvc": true, "pvcs": true,
	"pv": true, "pvs": true,
	"storageclass": true, "storageclasses": true,
	"namespace": true, "namespaces": true, "ns": true,
	"clusterrole": true, "clusterroles": true,
	"clusterrolebinding": true, "clusterrolebindings": true,
	"secret": true, "secrets": true,
	"cluster": true,
}

// valueFlags 是需要独立值的 kubectl flag（解析 resource/subcommand 时跳过其值）。
// 不追求覆盖 kubectl 全部 flag，只覆盖全局 flag 和 delete/rollout 等分类路径上
// 可能出现在资源名或子命令之前的常用 flag；含 "=" 的 flag 由调用方直接跳过。
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true,
	"--context": true, "--kubeconfig": true,
	"--server": true, "--user": true, "--cluster": true,
	"--token": true, "--as": true, "--as-group": true,
	"--certificate-authority": true, "--client-certificate": true,
	"--client-key": true, "--request-timeout": true,
	"--tls-server-name": true, "--as-uid": true,
	"--username": true, "--password": true, "--cache-dir": true,
	"-o": true, "--output": true,
	"-l": true, "--selector": true, "--field-selector": true,
	"--template": true, "--sort-by": true, "--label-columns": true,
	"--since": true, "--since-time": true, "--tail": true,
	"--grace-period": true, "--timeout": true, "--cascade": true,
	"--requests": true, "--limits": true, "--replicas": true,
	"--min": true, "--max": true, "--cpu-percent": true, "--memory-percent": true,
	"--from": true, "--to": true, "--patch": true, "-p": true,
	"--patch-file": true, "--type": true, "--for": true,
	"--kustomize": true, "-k": true, "-f": true, "--filename": true,
}

// ClassifyCommand 解析 kubectl 命令并给出风险等级。
// 只读 → SAFE；rollout history/status → SAFE；--dry-run 的已知写动词 → SAFE；
// 删除关键资源 → CRITICAL；其余写动词按表；未知动词保守 CRITICAL。
func ClassifyCommand(command string) model.RiskLevel {
	readOnly, risk := classifyCommand(command)
	if readOnly {
		return model.RiskSafe
	}
	return risk
}

// IsReadOnlyCommand 判断命令是否为只读（自动执行，无需审批）。
// 只读动词、rollout history/status、带 --dry-run 的已知写动词均视为只读。
// 含 shell 语法的命令（pipe/heredoc/redirect/&&/|| 等）永不归类为只读，
// 必须走人工审批（AGENTS.md: shell commands are never classified as read-only auto-execute）。
func IsReadOnlyCommand(command string) bool {
	if HasShellSyntax(command) {
		return false
	}
	readOnly, _ := classifyCommand(command)
	return readOnly
}

// HasShellSyntax 判断命令字符串是否包含 shell 元字符（用于审批分类：
// 含 shell 语法的命令永不归类为只读，必须走人工审批）。
func HasShellSyntax(command string) bool {
	inSingle, inDouble := false, false
	for _, r := range command {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '|', '&', ';', '<', '>', '$', '(', ')':
			if !inSingle && !inDouble {
				return true
			}
		}
	}
	if strings.Contains(command, "`") {
		return true
	}
	return false
}

// classifyCommand 统一分类：返回 (只读, 风险等级)。
func classifyCommand(command string) (readOnly bool, risk model.RiskLevel) {
	verb, rest := extractVerb(command)
	if verb == "" {
		return false, model.RiskCritical
	}
	if readOnlyVerbs[verb] {
		return true, model.RiskSafe
	}
	dryRun := hasDryRun(command)
	// rollout 子命令细分：history/status 只读；undo 高风险；restart/pause/resume 中风险。
	if verb == "rollout" {
		sub := firstNonFlag(rest)
		if readOnlyRolloutSubs[sub] {
			return true, model.RiskSafe
		}
		if dryRun {
			return true, model.RiskSafe
		}
		if sub == "undo" {
			return false, model.RiskHigh
		}
		return false, model.RiskMedium
	}
	// 已知写动词 + --dry-run → 只读（不实际变更），借鉴 kubectl-ai。
	if dryRun {
		// delete 走 switch 特殊分支，不在 writeRisks 表内；dry-run 的 delete 同样不实际变更。
		if _, known := writeRisks[verb]; known || verb == "delete" {
			return true, model.RiskSafe
		}
	}
	switch verb {
	case "delete":
		res := firstResource(rest)
		if i := strings.Index(res, "/"); i >= 0 {
			res = res[:i] // kubectl delete deployment/name 的写法
		}
		if criticalDeleteResources[res] {
			return false, model.RiskCritical
		}
		return false, model.RiskHigh
	}
	if risk, ok := writeRisks[verb]; ok {
		return false, risk
	}
	return false, model.RiskCritical
}

// hasDryRun 检测命令是否带 --dry-run（client/server/none 均不实际变更）。
// 扫描全部字段，兼容 --dry-run 出现在任意位置。
func hasDryRun(command string) bool {
	for _, f := range strings.Fields(command) {
		if f == "--dry-run" || strings.HasPrefix(f, "--dry-run=") {
			return true
		}
	}
	return false
}

// extractVerb 提取 kubectl 后的第一个已知动词（及剩余参数）。
// 使用已知动词集合而非"第一个非 flag token"，是为了正确处理
// `kubectl -n default get pods` / `kubectl --context prod get pods` 这类
// 动词前出现全局 flag 及其独立值的合法命令。
func extractVerb(command string) (verb string, rest []string) {
	fields := strings.Fields(command)
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		if isKnownVerb(f) {
			return f, fields[i+1:]
		}
	}
	return "", nil
}

func isKnownVerb(s string) bool {
	_, isWrite := writeRisks[s]
	return readOnlyVerbs[s] || isWrite || s == "delete"
}

func firstNonFlag(fields []string) string {
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if !strings.HasPrefix(f, "-") {
			return f
		}
		if strings.Contains(f, "=") {
			continue
		}
		if valueFlags[f] && i+1 < len(fields) {
			i++
		}
	}
	return ""
}

// firstResource 返回第一个非 flag 参数（delete 的资源类型或名称）。
func firstResource(fields []string) string {
	return firstNonFlag(fields)
}
