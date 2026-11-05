package scanner

// CollectionRequirement 是体检采集与部署 RBAC 之间的契约。
//
// ResourceTask=true 表示该 operation 属于 resourceTasks 的固定 list 任务；
// Namespace、Events 与日志有独立调度方式，因此保留 operation 但不参与任务集合相等校验。
type CollectionRequirement struct {
	Operation    string
	APIGroup     string
	Resource     string
	Verbs        []string
	ResourceTask bool
}

// Phase1CollectionRequirements 返回本次可执行采集所需的 Kubernetes API 权限。
// 返回值是副本，调用方不能修改包级契约。
func Phase1CollectionRequirements() []CollectionRequirement {
	out := make([]CollectionRequirement, len(phase1CollectionRequirements))
	for i := range phase1CollectionRequirements {
		out[i] = phase1CollectionRequirements[i]
		out[i].Verbs = append([]string(nil), phase1CollectionRequirements[i].Verbs...)
	}
	return out
}

var phase1CollectionRequirements = []CollectionRequirement{
	{Operation: "list_namespaces", APIGroup: "", Resource: "namespaces", Verbs: []string{"list"}},
	{Operation: "list_pods", APIGroup: "", Resource: "pods", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_nodes", APIGroup: "", Resource: "nodes", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_services", APIGroup: "", Resource: "services", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_endpoint_slices", APIGroup: "discovery.k8s.io", Resource: "endpointslices", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_deployments", APIGroup: "apps", Resource: "deployments", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_replica_sets", APIGroup: "apps", Resource: "replicasets", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_stateful_sets", APIGroup: "apps", Resource: "statefulsets", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_daemon_sets", APIGroup: "apps", Resource: "daemonsets", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_jobs", APIGroup: "batch", Resource: "jobs", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_cron_jobs", APIGroup: "batch", Resource: "cronjobs", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_pvcs", APIGroup: "", Resource: "persistentvolumeclaims", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_pvs", APIGroup: "", Resource: "persistentvolumes", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_storage_classes", APIGroup: "storage.k8s.io", Resource: "storageclasses", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_volume_attachments", APIGroup: "storage.k8s.io", Resource: "volumeattachments", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_ingresses", APIGroup: "networking.k8s.io", Resource: "ingresses", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_network_policies", APIGroup: "networking.k8s.io", Resource: "networkpolicies", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_hpa", APIGroup: "autoscaling", Resource: "horizontalpodautoscalers", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_pdb", APIGroup: "policy", Resource: "poddisruptionbudgets", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_roles", APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_role_bindings", APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_cluster_roles", APIGroup: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_cluster_role_bindings", APIGroup: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_config_maps", APIGroup: "", Resource: "configmaps", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_secret_meta", APIGroup: "", Resource: "secrets", Verbs: []string{"list"}, ResourceTask: true},
	{Operation: "list_events", APIGroup: "", Resource: "events", Verbs: []string{"list"}},
	{Operation: "get_logs", APIGroup: "", Resource: "pods/log", Verbs: []string{"get"}},
}
