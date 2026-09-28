package manifestx

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Mapping 是 GVK 解析结果。
type Mapping struct {
	Resource schema.GroupVersionResource
	Scope    Scope
}

// Mapper 把 GVK 解析为资源与 scope（由 internal/executor 基于 discovery/RESTMapper 实现，
// 依赖方向保持 manifestx 不 import client-go）。
type Mapper interface {
	MappingFor(gvk schema.GroupVersionKind) (Mapping, error)
}

// ParseGVK 解析 apiVersion + kind（"v1"→core 组，"apps/v1"→apps 组）。
func ParseGVK(apiVersion, kind string) schema.GroupVersionKind {
	gv := strings.SplitN(strings.TrimSpace(apiVersion), "/", 2)
	gvk := schema.GroupVersionKind{Kind: strings.TrimSpace(kind)}
	switch len(gv) {
	case 2:
		gvk.Group, gvk.Version = strings.TrimSpace(gv[0]), strings.TrimSpace(gv[1])
	default:
		gvk.Version = strings.TrimSpace(gv[0])
	}
	return gvk
}

// clusterScopedBuiltins 是无集群连接时的兜底作用域表（只用于静态校验与风险判定；
// 有 Mapper 时以集群 discovery 结论为准）。
var clusterScopedBuiltins = map[string]bool{
	"Namespace":                        true,
	"Node":                             true,
	"PersistentVolume":                 true,
	"StorageClass":                     true,
	"ClusterRole":                      true,
	"ClusterRoleBinding":               true,
	"CustomResourceDefinition":         true,
	"APIService":                       true,
	"PriorityClass":                    true,
	"RuntimeClass":                     true,
	"MutatingWebhookConfiguration":     true,
	"ValidatingWebhookConfiguration":   true,
	"ValidatingAdmissionPolicy":        true,
	"ValidatingAdmissionPolicyBinding": true,
	"FlowSchema":                       true,
	"PriorityLevelConfiguration":       true,
	"ClusterTrustBundle":               true,
	"CertificateSigningRequest":        true,
	"CSIDriver":                        true,
	"CSINode":                          true,
}

// staticScope 给出内置常见 cluster-scoped Kind 的作用域，未知返回 ScopeUnknown。
func staticScope(gvk schema.GroupVersionKind) Scope {
	if clusterScopedBuiltins[gvk.Kind] {
		return ScopeCluster
	}
	return ScopeUnknown
}

// IsSecretKind 判断 Kind 是否为 Secret（创建通道的值脱敏判定用）。
func IsSecretKind(kind string) bool { return strings.EqualFold(strings.TrimSpace(kind), "Secret") }

func isSecretKind(kind string) bool { return IsSecretKind(kind) }
