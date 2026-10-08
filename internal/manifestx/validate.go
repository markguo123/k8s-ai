package manifestx

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// labelNameKinds 使用 DNS-1123 label 命名规则（不允许多段点分名）的内置类型。
//
// 上游只对少数类型收紧到 label（Service 名要拼进 DNS 域名，Namespace 名要作为资源前缀），
// 其余内置类型（ConfigMap/Secret/Pod/PVC/ServiceAccount 等）都是 DNS-1123 subdomain，
// 允许点分名。这里刻意只列确定的两个，避免把合法名字误判成错误：
// 更严格的命名校验由 apiserver 在服务端裁决。
var labelNameKinds = map[string]bool{
	"Service":   true,
	"Namespace": true,
}

// validateRequired 校验必填字段与命名规则，并回填 GVK。
func validateRequired(o *Object) []Finding {
	var findings []Finding
	if o.APIVersion == "" {
		findings = append(findings, Finding{Code: CodeMissingAPIVersion, Severity: SeverityError, Field: "apiVersion",
			Message: "缺少 apiVersion", Fix: "补上资源 API 版本，如 v1、apps/v1"})
	}
	if o.Kind == "" {
		findings = append(findings, Finding{Code: CodeMissingKind, Severity: SeverityError, Field: "kind",
			Message: "缺少 kind", Fix: "补上资源类型，如 Deployment、ConfigMap"})
	}
	if o.Name == "" {
		findings = append(findings, Finding{Code: CodeMissingName, Severity: SeverityError, Field: "metadata.name",
			Message: "缺少 metadata.name", Fix: "补上资源名（小写字母、数字、- 或 .）"})
	} else {
		findings = append(findings, validateName(o)...)
	}
	return findings
}

// validateName 按 Kind 选择 DNS-1123 subdomain / label 规则校验资源名。
func validateName(o *Object) []Finding {
	var msgs []string
	rule := "DNS-1123 subdomain"
	if labelNameKinds[o.Kind] {
		rule = "DNS-1123 label"
		msgs = validation.IsDNS1123Label(o.Name)
	} else {
		msgs = validation.IsDNS1123Subdomain(o.Name)
	}
	if len(msgs) == 0 {
		return nil
	}
	return []Finding{{Code: CodeInvalidName, Severity: SeverityError, Field: "metadata.name",
		Message: fmt.Sprintf("资源名 %q 不合法（%s）：%s", o.Name, rule, strings.Join(msgs, "; ")),
		Fix:     "只使用小写字母、数字、'-' 或 '.'，且以字母数字开头和结尾，长度不超过 253"}}
}

// applyNamespace 处理 metadata.namespace：
// namespaced 资源缺省时补默认命名空间（warn），cluster-scoped 资源带 namespace 时报错。
func applyNamespace(o *Object, defaultNS string) []Finding {
	if defaultNS == "" {
		defaultNS = "default"
	}
	switch o.Scope {
	case ScopeCluster:
		if o.Namespace != "" {
			return []Finding{{Code: CodeNamespaceOnCluster, Severity: SeverityError, Field: "metadata.namespace",
				Message: fmt.Sprintf("%s 是 cluster-scoped 资源，不能设置 metadata.namespace", o.Kind),
				Fix:     "删除 metadata.namespace"}}
		}
		return nil
	case ScopeNamespaced:
		if o.Namespace != "" {
			return nil
		}
		setNamespace(o, defaultNS)
		return []Finding{{Code: CodeDefaultedNamespace, Severity: SeverityWarn, Field: "metadata.namespace",
			Message: fmt.Sprintf("未指定 metadata.namespace，按 %q 创建", defaultNS),
			Fix:     "如需其他命名空间，请显式填写 metadata.namespace"}}
	default: // ScopeUnknown：无法判定，保持原样，由映射结论决定是否阻断
		return nil
	}
}

// setNamespace 把补齐后的命名空间写回待提交内容（指纹与提案都以此为准）。
func setNamespace(o *Object, ns string) {
	meta, _ := o.content["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		o.content["metadata"] = meta
	}
	meta["namespace"] = ns
	o.Namespace = ns
}

// scanImages 检查镜像是否显式固定 tag（设计文档 §5.2：空镜像 tag 属静态检查项）。
func scanImages(o *Object) []Finding {
	var findings []Finding
	for _, ref := range podSpecs(o.content) {
		for _, key := range []string{"containers", "initContainers"} {
			containers, _ := ref.value[key].([]any)
			for i, item := range containers {
				container, ok := item.(map[string]any)
				if !ok {
					continue
				}
				image, _ := container["image"].(string)
				if strings.TrimSpace(image) == "" {
					continue
				}
				reason, ok := imagePinned(image)
				if !ok {
					findings = append(findings, Finding{Code: CodeImageUnpinned, Severity: SeverityWarn,
						Field:   fmt.Sprintf("%s.%s[%d].image", ref.field, key, i),
						Message: fmt.Sprintf("镜像 %q %s；未固定 tag 会隐式使用 :latest，节点侧缓存可能漂移", image, reason),
						Fix:     "改用带明确版本 tag 的镜像引用"})
				}
			}
		}
	}
	return findings
}

// imagePinned 判断镜像引用是否固定了非 latest 的 tag（或使用了 digest）。
func imagePinned(image string) (reason string, ok bool) {
	if strings.Contains(image, "@") {
		return "", true
	}
	tag := image
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		tag = image[i+1:]
	} else {
		return "缺少 tag", false
	}
	if strings.EqualFold(tag, "latest") {
		return "使用了 :latest", false
	}
	return "", true
}

// containerRef 是一个容器模板位置及其字段路径。
type containerRef struct {
	field string
	value map[string]any
}

// containerPaths 找出对象里的容器列表位置（工作负载的 PodSpec 或裸 Pod）。
func containerPaths(content map[string]any) []containerRef {
	out := make([]containerRef, 0, 2)
	if spec, ok := content["spec"].(map[string]any); ok {
		if tmpl, ok := spec["template"].(map[string]any); ok {
			if podSpec, ok := tmpl["spec"].(map[string]any); ok {
				out = append(out, containerRef{"spec.template.spec", podSpec})
			}
		}
		if _, ok := spec["containers"].([]any); ok && IsPodLike(content) {
			out = append(out, containerRef{"spec", spec})
		}
	}
	return out
}

// IsPodLike 判断对象的 spec 直接就是 PodSpec（Pod / ReplicationController）。
func IsPodLike(content map[string]any) bool {
	kind, _ := content["kind"].(string)
	return kind == "Pod" || kind == "ReplicationController"
}

// podSpecs 返回对象里所有 PodSpec 及其字段路径（危险字段扫描用，兼容 CRD 常见结构）。
func podSpecs(content map[string]any) []containerRef {
	out := containerPaths(content)
	if len(out) > 0 {
		return out
	}
	// CRD 等非标准结构：把任意带 containers 的 spec 当作 PodSpec。
	if spec, ok := content["spec"].(map[string]any); ok {
		if _, has := spec["containers"].([]any); has {
			out = append(out, containerRef{"spec", spec})
		}
	}
	return out
}
