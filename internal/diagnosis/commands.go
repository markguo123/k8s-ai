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
	"secret": true, "secrets": true, "endpoints": true, "events": true,
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
// 命名空间规则（system.md §三十五：命令必须含 -n 才能独立执行）：
//   - 命令自带 -n/--namespace（含 -n= / --namespace= 形式，动词前/后均可）时，
//     必须与 Finding 命名空间一致，不一致视为编造/笔误整体丢弃；
//   - 命令缺省命名空间时自动补全 Finding 命名空间（命令规范化），保证可执行；
//   - 显式 -A/--all-namespaces 的跨命名空间查询不补全、不校验。
func validateCommand(text string, category model.CommandCategory, ns string) (model.Command, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 512 {
		return model.Command{}, false
	}
	parts := strings.Fields(text)
	if len(parts) < 2 || parts[0] != "kubectl" {
		return model.Command{}, false
	}
	verb, rest, preNS := extractVerb(parts[1:])
	if verb == "" {
		return model.Command{}, false
	}
	switch category {
	case model.CmdInvestigation, model.CmdVerification:
		if !readOnlyVerbs[verb] {
			return model.Command{}, false
		}
	case model.CmdRemediation:
		// 允许写动词；也允许只读"确认命令"（系统提示词 §三十五：命令化修复方案）。
		if !writeVerbs[verb] && !readOnlyVerbs[verb] {
			return model.Command{}, false
		}
	}
	resType, resName, cmdNS := extractResource(rest)
	if verb == "logs" && resType == "" {
		// kubectl logs <pod> [-c <container>]：logs 的位置参数是 Pod 名（无资源类型），
		// 按 Pod 处理以便后续 -n 补全（system.md §八：kubectl logs <pod> -n <ns>）。
		if p := firstPositional(rest); p != "" {
			resType, resName = "pod", p
		}
	}
	if resType == "" || resName == "" {
		return model.Command{}, false
	}
	if !validResourceName.MatchString(resName) {
		return model.Command{}, false // 拒绝 <deployment-name> 等占位符/非法名称
	}
	if namespaceScoped[resType] {
		// 动词前的 -n 优先（kubectl 全局 flag 两种位置等价，取前者的值即可）。
		if preNS != "" {
			cmdNS = preNS
		}
		switch {
		case hasAllNamespaces(parts):
			// 显式跨命名空间查询，命令自带语义，无需/无法补全单个 -n。
		case cmdNS != "":
			if ns != "" && cmdNS != ns {
				return model.Command{}, false
			}
		case ns == "":
			return model.Command{}, false
		default:
			text = injectNamespace(text, ns)
		}
	}
	return model.Command{Category: category, Text: text}, true
}

// extractVerb 跳过开头的 flag（含带值 flag），返回动词、剩余参数，
// 并提取开头的命名空间 flag（-n/--namespace 及其 = 形式）。
func extractVerb(tokens []string) (verb string, rest []string, ns string) {
	for len(tokens) > 0 {
		t := tokens[0]
		if !strings.HasPrefix(t, "-") {
			return t, tokens[1:], ns
		}
		switch {
		case t == "-n" || t == "--namespace":
			if len(tokens) >= 2 {
				ns = tokens[1]
				tokens = tokens[2:]
			} else {
				tokens = tokens[1:]
			}
		case strings.HasPrefix(t, "-n=") || strings.HasPrefix(t, "--namespace="):
			ns = t[strings.IndexByte(t, '=')+1:]
			tokens = tokens[1:]
		case valueFlags[t]:
			tokens = tokens[2:]
		default:
			tokens = tokens[1:]
		}
	}
	return "", nil, ns
}

// extractResource 返回资源类型、名称与命令中的 namespace（动词后位置）。
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
		case strings.HasPrefix(t, "-n=") || strings.HasPrefix(t, "--namespace="):
			ns = t[strings.IndexByte(t, '=')+1:]
			i++
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

// firstPositional 返回 tokens 中第一个非 flag 参数（logs 动词的 Pod 名）。
func firstPositional(tokens []string) string {
	for _, t := range tokens {
		if !strings.HasPrefix(t, "-") {
			return t
		}
	}
	return ""
}

// hasAllNamespaces 判断命令是否显式跨命名空间查询（-A/--all-namespaces）。
func hasAllNamespaces(tokens []string) bool {
	for _, t := range tokens {
		if t == "-A" || t == "--all-namespaces" || strings.HasPrefix(t, "--all-namespaces=") {
			return true
		}
	}
	return false
}

// injectNamespace 在 kubectl 后插入 -n <ns>（全局 flag 位置），
// 使命令补全命名空间后可独立执行（system.md §三十五）。
func injectNamespace(text, ns string) string {
	if ns == "" {
		return text
	}
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return text
	}
	out := make([]string, 0, len(parts)+2)
	out = append(out, parts[0], "-n", ns)
	out = append(out, parts[1:]...)
	return strings.Join(out, " ")
}

func isKnownResource(t string) bool {
	return namespaceScoped[t] || t == "node" || t == "nodes" || t == "pv" || t == "pvs" ||
		t == "storageclass" || t == "storageclasses" || t == "namespace" || t == "namespaces" ||
		t == "volumeattachment" || t == "volumeattachments" || t == "secret" || t == "secrets"
}

// validResourceName 校验 Kubernetes 资源名（小写字母数字、-、.，不允许占位符/斜杠）。
var validResourceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// commandRisk 按动词计算修复命令风险：只读确认命令为 SAFE，其余使用 LLM 提供风险。
func commandRisk(text string, risk model.RiskLevel) model.RiskLevel {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return risk
	}
	if verb, _, _ := extractVerb(fields[1:]); readOnlyVerbs[verb] {
		return model.RiskSafe
	}
	return risk
}
