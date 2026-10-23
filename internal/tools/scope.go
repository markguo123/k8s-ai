package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/manifestx"
)

// AccessScope 是当前 Agent 运行可访问的 Kubernetes 资源范围。
// 它是执行前门禁，不是提示词约束。
type AccessScope struct {
	ClusterID     string   `json:"clusterId,omitempty"`
	Namespaces    []string `json:"namespaces"`
	ClusterScoped bool     `json:"clusterScoped"`
	IsAdmin       bool     `json:"isAdmin"`
	CanWrite      bool     `json:"canWrite"`
	CanDestroy    bool     `json:"canDestroy"`
}

var (
	localTools = map[string]bool{
		"list_skills": true,
		"get_skill":   true,
	}
	clusterOnlyTools = map[string]bool{
		"get_node":         true,
		"get_nodes":        true,
		"get_node_usage":   true,
		"get_pv":           true,
		"get_storageclass": true,
		"get_namespace":    true,
		"diagnose_node":    true,
	}
	namespaceTools = map[string]bool{
		"get_pod":            true,
		"get_pods":           true,
		"get_deployment":     true,
		"get_statefulset":    true,
		"get_daemonset":      true,
		"get_replicaset":     true,
		"get_service":        true,
		"get_endpoints":      true,
		"get_endpointslices": true,
		"get_pvc":            true,
		"get_configmap":      true,
		"get_configmaps":     true,
		"get_logs":           true,
		"get_previous_logs":  true,
		"scan_pod":           true,
		"diagnose_pod":       true,
		"diagnose_workload":  true,
	}
	clusterKinds = map[string]bool{
		"node":                      true,
		"nodes":                     true,
		"namespace":                 true,
		"namespaces":                true,
		"ns":                        true,
		"persistentvolume":          true,
		"persistentvolumes":         true,
		"pv":                        true,
		"pvs":                       true,
		"storageclass":              true,
		"storageclasses":            true,
		"sc":                        true,
		"clusterrole":               true,
		"clusterroles":              true,
		"clusterrolebinding":        true,
		"clusterrolebindings":       true,
		"customresourcedefinition":  true,
		"customresourcedefinitions": true,
		"crd":                       true,
		"crds":                      true,
	}
)

// NormalizeScope 清理 namespace 列表并保持稳定排序。
func NormalizeScope(s AccessScope) AccessScope {
	seen := map[string]bool{}
	out := make([]string, 0, len(s.Namespaces))
	for _, ns := range s.Namespaces {
		ns = strings.TrimSpace(ns)
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	sort.Strings(out)
	s.Namespaces = out
	return s
}

// Prompt 提供给模型的授权范围说明。安全边界仍由 Check 强制执行。
func (s AccessScope) Prompt() string {
	if s.IsAdmin || s.ClusterScoped {
		return "## 当前授权范围\n当前账号拥有集群级管理权限，可查询全部命名空间及 Node、PV、StorageClass 等集群资源。"
	}
	if len(s.Namespaces) == 0 {
		return "## 当前授权范围\n当前账号尚未获得任何 namespace 授权。不要调用 Kubernetes 查询工具；直接说明当前没有可查询范围，并提示用户联系管理员授权。工具返回 SCOPE_DENIED 时，用自然、专业的业务语言向用户说明，不要输出内部错误码。"
	}
	return fmt.Sprintf("## 当前授权范围\n当前账号仅可访问以下 namespace：%s。不要查询其他 namespace，也不要查询 Node、PV、StorageClass、Namespace 等集群级资源。超出范围时，直接说明当前授权范围并提示联系管理员调整授权，不要假设或编造数据。工具返回 SCOPE_DENIED 时，用自然、专业的业务语言向用户说明，不要输出内部错误码。",
		strings.Join(s.Namespaces, "、"))
}

// Check 在执行工具前校验资源作用域。error 文本是给模型与用户的稳定拒绝原因。
func (s AccessScope) Check(toolName string, input json.RawMessage) error {
	s = NormalizeScope(s)
	if s.IsAdmin || s.ClusterScoped {
		return nil
	}
	if localTools[toolName] {
		return nil
	}
	if clusterOnlyTools[toolName] {
		return s.deny("集群级资源读取权限")
	}

	var args map[string]any
	_ = json.Unmarshal(input, &args)
	if toolName == "kubectl" {
		return s.checkKubectl(stringArg(args, "command"))
	}
	if toolName == "create_resource" {
		return s.checkManifest(stringArg(args, "manifest"))
	}
	if toolName == "get_resources" {
		kind := strings.ToLower(stringArg(args, "kind"))
		if clusterKinds[kind] {
			return s.deny("集群级资源读取权限")
		}
	}
	if toolName == "get_events" {
		kind := strings.ToLower(stringArg(args, "kind"))
		if clusterKinds[kind] && stringArg(args, "namespace") == "" {
			return s.deny("集群事件读取权限")
		}
	}
	if toolName == "scan_cluster" {
		ns := strings.TrimSpace(stringArg(args, "namespace"))
		if ns == "" {
			return s.deny("全集群巡检权限")
		}
		return s.checkNamespace(ns)
	}
	if namespaceTools[toolName] || toolName == "get_resources" || toolName == "get_events" {
		ns := strings.TrimSpace(stringArg(args, "namespace"))
		if ns == "" {
			return s.deny("显式 namespace 查询范围；当前不允许跨 namespace 查询")
		}
		return s.checkNamespace(ns)
	}
	return s.deny("该工具的资源作用域权限")
}

func (s AccessScope) checkNamespace(ns string) error {
	for _, allowed := range s.Namespaces {
		if ns == allowed {
			return nil
		}
	}
	return s.deny(fmt.Sprintf("对 namespace %q 的访问权限", ns))
}

func (s AccessScope) deny(requirement string) error {
	if len(s.Namespaces) == 0 {
		return fmt.Errorf("SCOPE_DENIED: 当前请求需要%s。当前账号尚未获得 namespace 授权，无法执行 Kubernetes 查询；请联系管理员完成授权后重试。", requirement)
	}
	return fmt.Errorf("SCOPE_DENIED: 当前请求超出授权范围，需要%s。当前账号已授权的 namespace 为：%s。请将问题限定在已授权范围内；如需访问其他 namespace 或集群级资源（如 Node、PV、StorageClass），请联系管理员调整授权。",
		requirement, strings.Join(s.Namespaces, "、"))
}

func (s AccessScope) checkManifest(manifest string) error {
	manifest = strings.TrimSpace(manifest)
	if manifest == "" {
		// apply 只携带 proposal_id 时，清单已在 dry-run 阶段完成 scope 校验。
		return nil
	}
	objs, err := manifestx.Parse(manifest, manifestx.Options{})
	if err != nil {
		return s.deny("可校验资源作用域的 manifest")
	}
	for _, obj := range objs {
		if obj.Scope == manifestx.ScopeCluster {
			return s.deny("cluster-scoped 资源变更权限")
		}
		ns := strings.TrimSpace(obj.Namespace)
		if ns == "" {
			ns = "default"
		}
		if err := s.checkNamespace(ns); err != nil {
			return err
		}
	}
	return nil
}

func (s AccessScope) checkKubectl(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return s.deny("可校验资源作用域的 kubectl 命令")
	}
	if approval.HasShellSyntax(command) {
		return s.deny("不含 shell 构造且可校验范围的 kubectl 命令")
	}
	argv, err := executor.ParseCommand(command)
	if err != nil {
		return s.deny("可解析资源作用域的 kubectl 命令")
	}
	verbIdx := -1
	for i, token := range argv {
		if token == "get" || token == "describe" || token == "logs" || token == "events" {
			verbIdx = i
			break
		}
	}
	if verbIdx < 0 {
		return s.deny("可确定资源作用域的 kubectl 命令")
	}
	ns := ""
	for i := 0; i < len(argv); i++ {
		token := argv[i]
		switch {
		case token == "-A" || token == "--all-namespaces":
			return s.deny("全命名空间查询权限")
		case token == "-n" || token == "--namespace":
			if i+1 < len(argv) {
				ns = argv[i+1]
				i++
			}
		case strings.HasPrefix(token, "--namespace="):
			ns = strings.TrimPrefix(token, "--namespace=")
		}
	}
	resource := ""
	for i := verbIdx + 1; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "-") {
			continue
		}
		resource = argv[i]
		break
	}
	resource = strings.ToLower(strings.SplitN(resource, "/", 2)[0])
	if clusterKinds[resource] && ns == "" {
		return s.deny("集群级资源读取权限")
	}
	if ns == "" {
		return s.deny("显式 namespace 查询范围；当前不允许跨 namespace 查询")
	}
	return s.checkNamespace(ns)
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}
