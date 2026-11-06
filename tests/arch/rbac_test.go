package arch_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/k8s-ai/k8s-ai/internal/scanner"
	k8stools "github.com/k8s-ai/k8s-ai/internal/tools/k8s"
)

// rbacRule 是 clusterrole.yaml 的最小解析结构。
type rbacRule struct {
	APIGroups []string `yaml:"apiGroups"`
	Resources []string `yaml:"resources"`
	Verbs     []string `yaml:"verbs"`
}

type rbacRole struct {
	Rules []rbacRule `yaml:"rules"`
}

type rbacManifest struct {
	Kind  string     `yaml:"kind"`
	Rules []rbacRule `yaml:"rules"`
}

type rbacRoleRef struct {
	APIGroup string `yaml:"apiGroup"`
	Kind     string `yaml:"kind"`
	Name     string `yaml:"name"`
}

type rbacSubject struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type rbacBinding struct {
	Kind     string      `yaml:"kind"`
	RoleRef  rbacRoleRef `yaml:"roleRef"`
	Subjects []rbacSubject
}

// TestRBACReadOnly 解析 deploy/base/clusterrole.yaml，断言默认清单保持只读：
// verbs 限于 {get,list,watch}；resources 不含 pods/exec、pods/portforward、pods/attach 与写能力。
// Secret 在二期放行为“元数据/键级读取”，故允许 secrets 的 get/list/watch（仍只读，SECURITY.md §3）。
// metrics.k8s.io 的 nodes/pods 只支持 get/list，供 get_node_usage 与 kubectl top 使用。
func TestRBACReadOnly(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/base/clusterrole.yaml")
	if err != nil {
		t.Fatalf("读取 clusterrole.yaml: %v", err)
	}
	var m rbacManifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 clusterrole.yaml: %v", err)
	}
	if m.Kind != "ClusterRole" || len(m.Rules) == 0 {
		t.Fatal("清单不是有效的 ClusterRole")
	}
	allowed := map[string]bool{"get": true, "list": true, "watch": true}
	for _, rule := range m.Rules {
		for _, v := range rule.Verbs {
			if !allowed[v] {
				t.Errorf("发现非只读动词 %q（resources=%v）", v, rule.Resources)
			}
		}
		for _, res := range rule.Resources {
			// Secret 允许只读（get/list/watch，由 verbs 校验保证）；仅禁止执行/端口转发/附加等能力。
			if strings.Contains(res, "/exec") || strings.Contains(res, "/portforward") || strings.Contains(res, "/attach") || strings.Contains(res, "/exec/") {
				t.Errorf("发现禁止资源 %q", res)
			}
		}
	}
}

// TestRBACInspectionResources 验证体检采集依赖的资源归属正确：
// PV/PVC 属于 Core API group；HPA、PDB、RBAC 只读权限已显式授权。
func TestRBACInspectionResources(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/base/clusterrole.yaml")
	if err != nil {
		t.Fatalf("读取 clusterrole.yaml: %v", err)
	}
	var m rbacManifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 clusterrole.yaml: %v", err)
	}

	type permissionKey struct {
		group    string
		resource string
		verb     string
	}
	granted := make(map[permissionKey]bool)
	for _, rule := range m.Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					granted[permissionKey{group: group, resource: resource, verb: verb}] = true
				}
			}
		}
	}

	for _, requirement := range scanner.Phase1CollectionRequirements() {
		for _, verb := range requirement.Verbs {
			key := permissionKey{group: requirement.APIGroup, resource: requirement.Resource, verb: verb}
			if !granted[key] {
				t.Errorf("采集契约 %s 缺少权限：apiGroups=%q resources=%q verbs=%q",
					requirement.Operation, key.group, key.resource, key.verb)
			}
		}
	}

	forbidden := []permissionKey{
		{group: "storage.k8s.io", resource: "persistentvolumes", verb: "list"},
		{group: "storage.k8s.io", resource: "persistentvolumeclaims", verb: "list"},
	}
	for _, key := range forbidden {
		if granted[key] {
			t.Errorf("资源归属错误：%q 不应出现在 apiGroups=%q", key.resource, key.group)
		}
	}
}

// TestRBACAgentToolResources 验证 Chat/Agent 的内置只读工具与 kubectl 只读回退路径
// 所需权限全部由默认 ClusterRole 覆盖。新增工具访问面但漏更新部署清单时，这条会先失败。
func TestRBACAgentToolResources(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/base/clusterrole.yaml")
	if err != nil {
		t.Fatalf("读取 clusterrole.yaml: %v", err)
	}
	var m rbacManifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 clusterrole.yaml: %v", err)
	}

	type permissionKey struct {
		group    string
		resource string
		verb     string
	}
	granted := make(map[permissionKey]bool)
	for _, rule := range m.Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					granted[permissionKey{group: group, resource: resource, verb: verb}] = true
				}
			}
		}
	}

	for _, requirement := range k8stools.ReadAccessRequirements() {
		for _, verb := range requirement.Verbs {
			key := permissionKey{group: requirement.APIGroup, resource: requirement.Resource, verb: verb}
			if !granted[key] {
				t.Errorf("Agent 访问契约 %s 缺少权限：apiGroups=%q resources=%q verbs=%q",
					requirement.Operation, key.group, key.resource, key.verb)
			}
		}
	}
}

// TestRBACDefaultBinding 验证默认 ClusterRoleBinding 绑定的是预期只读角色与工作负载身份。
func TestRBACDefaultBinding(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/base/clusterrolebinding.yaml")
	if err != nil {
		t.Fatalf("读取 clusterrolebinding.yaml: %v", err)
	}
	var binding rbacBinding
	if err := yaml.Unmarshal(raw, &binding); err != nil {
		t.Fatalf("解析 clusterrolebinding.yaml: %v", err)
	}
	if binding.Kind != "ClusterRoleBinding" {
		t.Fatalf("默认绑定 kind = %q，期望 ClusterRoleBinding", binding.Kind)
	}
	if binding.RoleRef.APIGroup != "rbac.authorization.k8s.io" ||
		binding.RoleRef.Kind != "ClusterRole" ||
		binding.RoleRef.Name != "aegisops-readonly" {
		t.Errorf("默认绑定 roleRef = %+v，期望 ClusterRole/aegisops-readonly", binding.RoleRef)
	}
	if len(binding.Subjects) != 1 {
		t.Fatalf("默认绑定 subjects 数量 = %d，期望 1", len(binding.Subjects))
	}
	subject := binding.Subjects[0]
	if subject.Kind != "ServiceAccount" || subject.Name != "aegisops" || subject.Namespace != "aegisops" {
		t.Errorf("默认绑定 subject = %+v，期望 ServiceAccount/aegisops/aegisops", subject)
	}
}

// TestRBACWriteExample 解析 deploy/rbac-write.example.yaml（可选写权限示例，三期 R2.12）：
// verbs 限于最小集合，不含通配符与 exec/portforward/attach 子资源；
// secrets 只允许 create/patch/update（无 get/list/watch/delete）——AegisOps 不读 Secret 内容值，
// 也不删 Secret；创建通道（SSA）需要的 create 必须存在，否则示例无法支撑三期功能。
func TestRBACWriteExample(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/rbac-write.example.yaml")
	if err != nil {
		t.Fatalf("读取 rbac-write.example.yaml: %v", err)
	}
	allowed := map[string]bool{"get": true, "list": true, "watch": true, "create": true, "update": true, "patch": true, "delete": true}
	secretAllowed := map[string]bool{"create": true, "patch": true, "update": true}
	var role *rbacManifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var m rbacManifest
		if err := dec.Decode(&m); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("解析 rbac-write.example.yaml: %v", err)
		}
		if m.Kind == "ClusterRole" {
			role = &m
			break
		}
	}
	if role == nil || len(role.Rules) == 0 {
		t.Fatal("示例清单里没有 ClusterRole")
	}
	grantsCreate := false
	grantsExec := false
	secretsFound := false
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if strings.Contains(res, "/portforward") || strings.Contains(res, "/attach") {
				t.Errorf("示例清单授权了危险子资源 %q", res)
			}
			if res == "*" {
				t.Errorf("示例清单使用了通配资源 %q", res)
			}
		}
		for _, v := range rule.Verbs {
			if v == "*" {
				t.Errorf("示例清单使用了通配动词 %q（resources=%v）", v, rule.Resources)
			}
			if !allowed[v] {
				t.Errorf("示例清单出现最小集合之外的动词 %q（resources=%v）", v, rule.Resources)
			}
			if v == "create" {
				grantsCreate = true
				for _, res := range rule.Resources {
					if res == "pods/exec" {
						grantsExec = true
					}
				}
			}
		}
		for _, res := range rule.Resources {
			if res != "secrets" {
				continue
			}
			secretsFound = true
			for _, v := range rule.Verbs {
				if !secretAllowed[v] {
					t.Errorf("secrets 出现 %q：只允许 create/patch/update（不读内容值、不删除）", v)
				}
			}
		}
	}
	if !grantsCreate {
		t.Error("三期创建通道需要 create 动词，示例清单未授予")
	}
	if !grantsExec {
		t.Error("非交互 exec 需要 pods/exec 的 create 权限")
	}
	if !secretsFound {
		t.Error("示例清单应包含 secrets 的创建授权（create/patch/update），否则创建通道无法覆盖 Secret")
	}
}

// TestRBACServiceAccountContract 锁定三类运行负载的身份边界：
// 后端与巡检需要挂载 SA Token；前端与迁移 Job 都不应获得集群 API 凭据。
// SA 本身默认关闭 automount，只有实际访问 API 的 Pod 显式打开。
func TestRBACServiceAccountContract(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/base/serviceaccount.yaml")
	if err != nil {
		t.Fatalf("读取 serviceaccount.yaml: %v", err)
	}
	var sa struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Automount *bool `yaml:"automountServiceAccountToken"`
	}
	if err := yaml.Unmarshal(raw, &sa); err != nil {
		t.Fatalf("解析 serviceaccount.yaml: %v", err)
	}
	if sa.APIVersion != "v1" || sa.Kind != "ServiceAccount" {
		t.Fatalf("ServiceAccount 清单头 = %s %s", sa.APIVersion, sa.Kind)
	}
	if sa.Metadata.Name != "aegisops" || sa.Metadata.Namespace != "aegisops" {
		t.Errorf("ServiceAccount = %s/%s，期望 aegisops/aegisops", sa.Metadata.Namespace, sa.Metadata.Name)
	}
	if sa.Automount == nil || *sa.Automount {
		t.Error("ServiceAccount 默认应关闭 automountServiceAccountToken，由需要 API 的 Pod 显式开启")
	}

	for _, tc := range []struct {
		manifest       []string
		serviceAccount string
		automount      bool
	}{
		{[]string{"deploy", "allinone", "deployment.yaml"}, "aegisops", true},
		{[]string{"deploy", "separated", "deployment.yaml"}, "aegisops", true},
		{[]string{"deploy", "scan", "cronjob.yaml"}, "aegisops", true},
		{[]string{"deploy", "separated", "web-deployment.yaml"}, "", false},
		{[]string{"deploy", "base", "db-migrate-job.yaml"}, "", false},
	} {
		spec := readWorkload(t, tc.manifest...).podSpec()
		if spec.ServiceAccountName != tc.serviceAccount {
			t.Errorf("%s serviceAccountName = %q，期望 %q", tc.manifest, spec.ServiceAccountName, tc.serviceAccount)
		}
		if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken != tc.automount {
			t.Errorf("%s automountServiceAccountToken = %v，期望 %t", tc.manifest, spec.AutomountServiceAccountToken, tc.automount)
		}
	}
}
