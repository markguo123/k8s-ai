package diagnosis

import (
	"regexp"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// 只读动词白名单（排查/验证命令）。
var readOnlyVerbs = map[string]bool{
	"get": true, "describe": true, "logs": true, "events": true,
	"top": true, "explain": true, "api-resources": true, "version": true,
}

// 写动词白名单（修复命令；只展示不执行，ADR-014）。
var writeVerbs = map[string]bool{
	"edit": true, "set": true, "scale": true, "patch": true, "delete": true,
	"rollout": true, "annotate": true, "label": true, "apply": true, "create": true,
}

// 需要 -n/--namespace 的命名空间级资源（集群级资源除外）。
var namespaceScoped = map[string]bool{
	"pod": true, "pods": true, "deployment": true, "deployments": true,
	"statefulset": true, "statefulsets": true, "daemonset": true, "daemonsets": true,
	"replicaset": true, "replicasets": true, "service": true, "services": true,
	"pvc": true, "pvcs": true, "ingress": true, "ingresses": true,
	"networkpolicy": true, "networkpolicies": true, "job": true, "jobs": true,
	"cronjob": true, "cronjobs": true, "configmap": true, "configmaps": true,
	"endpoints": true, "events": true,
}

// 带值的 flag：跳过其后的参数值。
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true, "-o": true, "--output": true,
	"-l": true, "--selector": true, "-c": true, "--container": true,
	"--sort-by": true, "--field-selector": true, "--since": true, "--tail": true,
	"--context": true, "--kubeconfig": true, "--timeout": true, "--for": true,
	"--cascade": true, "--grace-period": true, "--force": true, "-w": false,
}

// validateCommand 校验 kubectl 命令：动词白名单、命名空间、资源类型与名称。
// 非法命令整体丢弃（ADR-005）。修复命令的风险由调用方补充。
func validateCommand(text string, category model.CommandCategory, ns string) (model.Command, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 512 {
		return model.Command{}, false
	}
	parts := strings.Fields(text)
	if len(parts) < 2 || parts[0] != "kubectl" {
		return model.Command{}, false
	}
	verb, rest := extractVerb(parts[1:])
	if verb == "" {
		return model.Command{}, false
	}
	switch category {
	case model.CmdInvestigation, model.CmdVerification:
		if !readOnlyVerbs[verb] {
			return model.Command{}, false
		}
	case model.CmdRemediation:
		if !writeVerbs[verb] {
			return model.Command{}, false
		}
	}
	resType, resName, cmdNS := extractResource(rest)
	if resType == "" || resName == "" {
		return model.Command{}, false
	}
	if !validResourceName.MatchString(resName) {
		return model.Command{}, false // 拒绝 <deployment-name> 等占位符/非法名称
	}
	if namespaceScoped[resType] {
		hasNS := cmdNS != "" || ns != ""
		if !hasNS {
			return model.Command{}, false
		}
	}
	return model.Command{Category: category, Text: text}, true
}

// extractVerb 跳过开头的 flag（含带值 flag），返回动词与剩余参数。
func extractVerb(tokens []string) (string, []string) {
	for len(tokens) > 0 {
		t := tokens[0]
		if !strings.HasPrefix(t, "-") {
			return t, tokens[1:]
		}
		if valueFlags[t] {
			tokens = tokens[2:]
		} else {
			tokens = tokens[1:]
		}
	}
	return "", nil
}

// extractResource 返回资源类型、名称与命令中的 namespace。
func extractResource(tokens []string) (resType, resName, ns string) {
	i := 0
	for i < len(tokens) {
		t := tokens[i]
		switch {
		case t == "-n" || t == "--namespace":
			if i+1 < len(tokens) {
				ns = tokens[i+1]
			}
			i += 2
		case strings.HasPrefix(t, "-"):
			if valueFlags[t] {
				i += 2
			} else {
				i++
			}
		case resType == "" && isKnownResource(t):
			resType = t
			i++
		case resType != "" && resName == "" && !strings.HasPrefix(t, "-"):
			resName = t
			i++
		default:
			i++
		}
	}
	return resType, resName, ns
}

func isKnownResource(t string) bool {
	return namespaceScoped[t] || t == "node" || t == "nodes" || t == "pv" || t == "pvs" ||
		t == "storageclass" || t == "storageclasses" || t == "namespace" || t == "namespaces" ||
		t == "volumeattachment" || t == "volumeattachments" || t == "secret" || t == "secrets"
}

// validResourceName 校验 Kubernetes 资源名（小写字母数字、-、.，不允许占位符/斜杠）。
var validResourceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
