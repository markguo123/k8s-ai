package k8s

// AccessRequirement 是 Agent 内置只读工具与 kubectl 只读回退路径所需的
// Kubernetes API 权限。它只描述内置资源；现场自行安装的 CRD 仍需由集群管理员
// 单独授予对应 group/resource 的权限。
type AccessRequirement struct {
	Operation string
	APIGroup  string
	Resource  string
	Verbs     []string
}

// ReadAccessRequirements 返回 Agent 只读调查能力所需的最小权限集合。
// 返回值是副本，调用方不能修改包级契约。
func ReadAccessRequirements() []AccessRequirement {
	out := make([]AccessRequirement, len(readAccessRequirements))
	for i := range readAccessRequirements {
		out[i] = readAccessRequirements[i]
		out[i].Verbs = append([]string(nil), readAccessRequirements[i].Verbs...)
	}
	return out
}

var readAccessRequirements = []AccessRequirement{
	{Operation: "cluster_basics", APIGroup: "", Resource: "namespaces", Verbs: []string{"get", "list", "watch"}},
	{Operation: "pods", APIGroup: "", Resource: "pods", Verbs: []string{"get", "list", "watch"}},
	{Operation: "nodes", APIGroup: "", Resource: "nodes", Verbs: []string{"get", "list", "watch"}},
	{Operation: "services", APIGroup: "", Resource: "services", Verbs: []string{"get", "list", "watch"}},
	{Operation: "endpoints", APIGroup: "", Resource: "endpoints", Verbs: []string{"get", "list", "watch"}},
	{Operation: "events", APIGroup: "", Resource: "events", Verbs: []string{"get", "list", "watch"}},
	{Operation: "configmaps", APIGroup: "", Resource: "configmaps", Verbs: []string{"get", "list", "watch"}},
	{Operation: "secret_diagnosis", APIGroup: "", Resource: "secrets", Verbs: []string{"get"}},
	{Operation: "service_accounts", APIGroup: "", Resource: "serviceaccounts", Verbs: []string{"get", "list", "watch"}},
	{Operation: "limit_ranges", APIGroup: "", Resource: "limitranges", Verbs: []string{"get", "list", "watch"}},
	{Operation: "resource_quotas", APIGroup: "", Resource: "resourcequotas", Verbs: []string{"get", "list", "watch"}},
	{Operation: "replication_controllers", APIGroup: "", Resource: "replicationcontrollers", Verbs: []string{"get", "list", "watch"}},
	{Operation: "persistent_volume_claims", APIGroup: "", Resource: "persistentvolumeclaims", Verbs: []string{"get", "list", "watch"}},
	{Operation: "persistent_volumes", APIGroup: "", Resource: "persistentvolumes", Verbs: []string{"get", "list", "watch"}},
	{Operation: "pod_logs", APIGroup: "", Resource: "pods/log", Verbs: []string{"get"}},
	{Operation: "workloads", APIGroup: "apps", Resource: "deployments", Verbs: []string{"get", "list", "watch"}},
	{Operation: "workloads", APIGroup: "apps", Resource: "replicasets", Verbs: []string{"get", "list", "watch"}},
	{Operation: "workloads", APIGroup: "apps", Resource: "statefulsets", Verbs: []string{"get", "list", "watch"}},
	{Operation: "workloads", APIGroup: "apps", Resource: "daemonsets", Verbs: []string{"get", "list", "watch"}},
	{Operation: "jobs", APIGroup: "batch", Resource: "jobs", Verbs: []string{"get", "list", "watch"}},
	{Operation: "jobs", APIGroup: "batch", Resource: "cronjobs", Verbs: []string{"get", "list", "watch"}},
	{Operation: "storage", APIGroup: "storage.k8s.io", Resource: "storageclasses", Verbs: []string{"get", "list", "watch"}},
	{Operation: "storage", APIGroup: "storage.k8s.io", Resource: "volumeattachments", Verbs: []string{"get", "list", "watch"}},
	{Operation: "autoscaling", APIGroup: "autoscaling", Resource: "horizontalpodautoscalers", Verbs: []string{"get", "list", "watch"}},
	{Operation: "disruption", APIGroup: "policy", Resource: "poddisruptionbudgets", Verbs: []string{"get", "list", "watch"}},
	{Operation: "rbac", APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verbs: []string{"get", "list", "watch"}},
	{Operation: "rbac", APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verbs: []string{"get", "list", "watch"}},
	{Operation: "rbac", APIGroup: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get", "list", "watch"}},
	{Operation: "rbac", APIGroup: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Verbs: []string{"get", "list", "watch"}},
	{Operation: "service_discovery", APIGroup: "discovery.k8s.io", Resource: "endpointslices", Verbs: []string{"get", "list", "watch"}},
	{Operation: "networking", APIGroup: "networking.k8s.io", Resource: "ingresses", Verbs: []string{"get", "list", "watch"}},
	{Operation: "networking", APIGroup: "networking.k8s.io", Resource: "networkpolicies", Verbs: []string{"get", "list", "watch"}},
	{Operation: "scheduling", APIGroup: "scheduling.k8s.io", Resource: "priorityclasses", Verbs: []string{"get", "list", "watch"}},
	{Operation: "runtime", APIGroup: "node.k8s.io", Resource: "runtimeclasses", Verbs: []string{"get", "list", "watch"}},
	{Operation: "api_health", APIGroup: "apiregistration.k8s.io", Resource: "apiservices", Verbs: []string{"get", "list", "watch"}},
	{Operation: "node_usage", APIGroup: "metrics.k8s.io", Resource: "nodes", Verbs: []string{"get", "list"}},
	{Operation: "pod_usage", APIGroup: "metrics.k8s.io", Resource: "pods", Verbs: []string{"get", "list"}},
}
