package arch_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// TestRBACReadOnly 解析 deploy/clusterrole.yaml，断言默认清单保持只读：
// verbs 限于 {get,list,watch}；resources 不含 pods/exec、pods/portforward、pods/attach 与写能力。
// Secret 在二期放行为“元数据/键级读取”，故允许 secrets 的 get/list/watch（仍只读，SECURITY.md §3）。
func TestRBACReadOnly(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/clusterrole.yaml")
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
		for _, g := range rule.APIGroups {
			if strings.Contains(g, "metrics") {
				t.Errorf("一期不应授权 metrics API: %q", g)
			}
		}
	}
}
