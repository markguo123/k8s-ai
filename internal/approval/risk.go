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
	"pvc": true, "pvcs": true,
	"pv": true, "pvs": true,
	"storageclass": true, "storageclasses": true,
	"namespace": true, "namespaces": true, "ns": true,
	"clusterrole": true, "clusterroles": true,
	"clusterrolebinding": true, "clusterrolebindings": true,
	"secret": true, "secrets": true,
	"cluster": true,
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
func IsReadOnlyCommand(command string) bool {
	readOnly, _ := classifyCommand(command)
	return readOnly
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

// extractVerb 提取 kubectl 后的第一个非 flag 动词（及剩余参数）。
func extractVerb(command string) (verb string, rest []string) {
	fields := strings.Fields(command)
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		if strings.HasPrefix(f, "-") {
			continue
		}
		return f, fields[i+1:]
	}
	return "", nil
}

func firstNonFlag(fields []string) string {
	for _, f := range fields {
		if !strings.HasPrefix(f, "-") {
			return f
		}
	}
	return ""
}

// firstResource 返回第一个非 flag 参数（delete 的资源类型或名称）。
func firstResource(fields []string) string {
	return firstNonFlag(fields)
}
