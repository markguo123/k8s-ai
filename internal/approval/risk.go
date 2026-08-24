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

// writeRisks 是写动词的基础风险（delete/rollout 等按资源再细分）。
var writeRisks = map[string]model.RiskLevel{
	"scale":        model.RiskMedium,
	"annotate":     model.RiskMedium,
	"label":        model.RiskMedium,
	"set":          model.RiskMedium,
	"rollout":      model.RiskMedium,
	"patch":        model.RiskHigh,
	"apply":        model.RiskHigh,
	"create":       model.RiskHigh,
	"exec":         model.RiskHigh,
	"drain":        model.RiskHigh,
	"cordon":       model.RiskHigh,
	"uncordon":     model.RiskHigh,
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
// 只读 → SAFE；删除关键资源 → CRITICAL；其余写动词按表；未知动词保守 CRITICAL。
func ClassifyCommand(command string) model.RiskLevel {
	verb, rest := extractVerb(command)
	if verb == "" {
		return model.RiskCritical
	}
	if readOnlyVerbs[verb] {
		return model.RiskSafe
	}
	switch verb {
	case "delete":
		res := firstResource(rest)
		if i := strings.Index(res, "/"); i >= 0 {
			res = res[:i] // kubectl delete deployment/name 的写法
		}
		if criticalDeleteResources[res] {
			return model.RiskCritical
		}
		return model.RiskHigh
	case "rollout":
		if sub := firstNonFlag(rest); sub == "undo" {
			return model.RiskHigh
		}
		return model.RiskMedium
	}
	if risk, ok := writeRisks[verb]; ok {
		return risk
	}
	return model.RiskCritical
}

// IsReadOnlyCommand 判断命令是否为只读。
func IsReadOnlyCommand(command string) bool {
	verb, _ := extractVerb(command)
	return readOnlyVerbs[verb]
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
