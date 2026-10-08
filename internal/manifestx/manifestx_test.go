package manifestx

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// staticMapper 是离线测试用的 GVK→GVR 映射表（无集群依赖）。
type staticMapper map[schema.GroupVersionKind]Mapping

func (m staticMapper) MappingFor(gvk schema.GroupVersionKind) (Mapping, error) {
	got, ok := m[gvk]
	if !ok {
		return Mapping{}, errors.New("apiVersion/kind 未在集群中注册")
	}
	return got, nil
}

var (
	gvkConfigMap   = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	gvkSecret      = schema.GroupVersionKind{Version: "v1", Kind: "Secret"}
	gvkService     = schema.GroupVersionKind{Version: "v1", Kind: "Service"}
	gvkNamespace   = schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}
	gvkDeployment  = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	gvkClusterRole = schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}
)

func testMapper() Mapper {
	return staticMapper{
		gvkConfigMap:   {Resource: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Scope: ScopeNamespaced},
		gvkSecret:      {Resource: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, Scope: ScopeNamespaced},
		gvkService:     {Resource: schema.GroupVersionResource{Version: "v1", Resource: "services"}, Scope: ScopeNamespaced},
		gvkNamespace:   {Resource: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, Scope: ScopeCluster},
		gvkDeployment:  {Resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Scope: ScopeNamespaced},
		gvkClusterRole: {Resource: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, Scope: ScopeCluster},
	}
}

func testOpts() Options {
	return Options{Mapper: testMapper(), Limits: Limits{MaxObjects: 3, MaxBytes: 8192}, DefaultNamespace: "default"}
}

func mustParse(t *testing.T, manifest string, opts Options) []Object {
	t.Helper()
	objs, err := Parse(manifest, opts)
	if err != nil {
		t.Fatalf("Parse 意外失败: %v", err)
	}
	return objs
}

// findingCode 返回结论里是否出现某个 code，方便表驱动断言。
func hasFinding(objs []Object, code string) bool {
	for i := range objs {
		for _, f := range objs[i].Findings {
			if f.Code == code {
				return true
			}
		}
	}
	return false
}

func TestParseSingleAndMultiDocument(t *testing.T) {
	manifest := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  MODE: prod
---
# 注释文档与空文档都应被跳过

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: prod
spec:
  replicas: 2
`
	objs := mustParse(t, manifest, testOpts())
	if len(objs) != 2 {
		t.Fatalf("期望 2 个对象，实际 %d", len(objs))
	}
	if objs[0].Kind != "ConfigMap" || objs[0].Name != "app-config" {
		t.Fatalf("首个对象解析错误: %+v", objs[0])
	}
	if objs[0].GVR.Resource != "configmaps" || objs[0].Scope != ScopeNamespaced {
		t.Fatalf("映射结果错误: %+v", objs[0])
	}
	if objs[1].Index != 1 || objs[1].Kind != "Deployment" || objs[1].Namespace != "prod" {
		t.Fatalf("第二个对象解析错误: %+v", objs[1])
	}
	if HasErrors(objs) {
		t.Fatalf("合法清单不应有阻断结论: %+v", objs[1].Errors())
	}
}

func TestParseJSONDocument(t *testing.T) {
	objs := mustParse(t, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c1","namespace":"d"}}`, testOpts())
	if len(objs) != 1 || objs[0].Name != "c1" {
		t.Fatalf("JSON 文档解析失败: %+v", objs)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := map[string]struct {
		manifest string
		want     string
	}{
		"非对象文档":     {manifest: "- a\n- b\n", want: CodeInvalidManifest},
		"空清单":       {manifest: "---\n\n# 只有注释\n", want: CodeEmptyManifest},
		"语法错误":      {manifest: "apiVersion: v1\nkind: ConfigMap\n data:\n\t bad: [\n", want: CodeInvalidManifest},
		"helm 模板":   {manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.name }}\n", want: CodeTemplateExpr},
		"kustomize": {manifest: "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - cm.yaml\n", want: CodeTemplateExpr},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(tc.manifest, testOpts())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误含 %s，实际: %v", tc.want, err)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	one := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n"
	three := one + "---\n" + one + "---\n" + one
	if _, err := Parse(three, testOpts()); err != nil {
		t.Fatalf("上限为 3 时 3 个对象应通过: %v", err)
	}
	if _, err := Parse(three, Options{Mapper: testMapper(), Limits: Limits{MaxObjects: 2}}); err == nil || !strings.Contains(err.Error(), CodeTooManyObjects) {
		t.Fatalf("期望触发对象数上限，实际: %v", err)
	}
	if _, err := Parse(one, Options{Mapper: testMapper(), Limits: Limits{MaxBytes: 10}}); err == nil || !strings.Contains(err.Error(), CodeManifestTooLarge) {
		t.Fatalf("期望触发字节上限，实际: %v", err)
	}
}

func TestRequiredFieldFindings(t *testing.T) {
	objs := mustParse(t, "apiVersion: v1\nmetadata:\n  name: x\n", testOpts())
	if !hasFinding(objs, CodeMissingKind) {
		t.Fatalf("缺少 kind 未报错: %+v", objs[0].Findings)
	}
	objs = mustParse(t, "kind: ConfigMap\nmetadata:\n  name: x\n", testOpts())
	if !hasFinding(objs, CodeMissingAPIVersion) {
		t.Fatalf("缺少 apiVersion 未报错: %+v", objs[0].Findings)
	}
	objs = mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata: {}\n", testOpts())
	if !hasFinding(objs, CodeMissingName) {
		t.Fatalf("缺少 metadata.name 未报错: %+v", objs[0].Findings)
	}
	if !objs[0].HasErrors() {
		t.Fatal("缺必填字段必须阻断")
	}
}

func TestNameValidation(t *testing.T) {
	// Service 使用 DNS-1123 label：点分名不合法。
	objs := mustParse(t, "apiVersion: v1\nkind: Service\nmetadata:\n  name: a.b.c\n  namespace: d\n", testOpts())
	if !hasFinding(objs, CodeInvalidName) {
		t.Fatalf("Service 点分名未被拒绝: %+v", objs[0].Findings)
	}
	// ConfigMap 允许 RFC1123 subdomain。
	objs = mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app.config.v1\n  namespace: d\n", testOpts())
	if hasFinding(objs, CodeInvalidName) {
		t.Fatalf("合法名被误判: %+v", objs[0].Findings)
	}
}

func TestNamespaceDefaulting(t *testing.T) {
	objs := mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n", Options{Mapper: testMapper(), DefaultNamespace: "team-a"})
	o := objs[0]
	if o.Namespace != "team-a" {
		t.Fatalf("应补齐默认命名空间，实际 %q", o.Namespace)
	}
	if !hasFinding(objs, CodeDefaultedNamespace) {
		t.Fatalf("补齐命名空间应给出 warn: %+v", o.Findings)
	}
	if o.HasErrors() {
		t.Fatalf("命名空间补齐不应阻断: %+v", o.Errors())
	}
	// 补齐后的命名空间必须进入待提交内容与指纹。
	meta, _ := o.Content()["metadata"].(map[string]any)
	if meta["namespace"] != "team-a" {
		t.Fatalf("待提交内容未包含命名空间: %+v", meta)
	}
}

func TestClusterScopeRules(t *testing.T) {
	// cluster-scoped 且未授权 → 拒绝。
	objs := mustParse(t, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ns-a\n", testOpts())
	if !hasFinding(objs, CodeClusterKindDenied) {
		t.Fatalf("cluster-scoped 未授权应拒绝: %+v", objs[0].Findings)
	}
	// 授权后放行。
	opts := testOpts()
	opts.Policy.ClusterKinds = []string{"namespaces"}
	objs = mustParse(t, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ns-a\n", opts)
	if objs[0].HasErrors() {
		t.Fatalf("已授权的 cluster Kind 不应被拒: %+v", objs[0].Errors())
	}
	// cluster-scoped 带 namespace → 报错。
	objs = mustParse(t, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ns-a\n  namespace: default\n", opts)
	if !hasFinding(objs, CodeNamespaceOnCluster) {
		t.Fatalf("cluster-scoped 不应带 namespace: %+v", objs[0].Findings)
	}
}

func TestKindLists(t *testing.T) {
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: d\n"
	// 黑名单（复数名写法）命中。
	opts := testOpts()
	opts.Policy.DenyKinds = []string{"deployments"}
	objs := mustParse(t, manifest, opts)
	if !hasFinding(objs, CodeDeniedKind) {
		t.Fatalf("deny_kinds 未生效: %+v", objs[0].Findings)
	}
	// 白名单收紧后，名单外类型拒绝。
	opts = testOpts()
	opts.Policy.AllowKinds = []string{"ConfigMap"}
	objs = mustParse(t, manifest, opts)
	if !hasFinding(objs, CodeKindNotAllowed) {
		t.Fatalf("allow_kinds 未生效: %+v", objs[0].Findings)
	}
	objs = mustParse(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: d\n", opts)
	if objs[0].HasErrors() {
		t.Fatalf("白名单内类型应放行: %+v", objs[0].Errors())
	}
}

func TestUnmappedKindIsRejected(t *testing.T) {
	objs := mustParse(t, "apiVersion: stable.example.com/v1\nkind: CronTab\nmetadata:\n  name: ct\n  namespace: d\n", testOpts())
	if !objs[0].UnknownKind || objs[0].Mapped {
		t.Fatalf("未注册类型应标记 unknown: %+v", objs[0])
	}
	if !hasFinding(objs, CodeUnmappedKind) {
		t.Fatalf("未注册类型应拒绝: %+v", objs[0].Findings)
	}
}

func TestMappedCRDIsAllowed(t *testing.T) {
	// 已安装 CRD 能通过 discovery 映射，因此不受限制（D-11）。
	opts := testOpts()
	gvk := schema.GroupVersionKind{Group: "stable.example.com", Version: "v1", Kind: "CronTab"}
	opts.Mapper = staticMapper(map[schema.GroupVersionKind]Mapping{
		gvk: {Resource: schema.GroupVersionResource{Group: "stable.example.com", Version: "v1", Resource: "crontabs"}, Scope: ScopeNamespaced},
	})
	objs := mustParse(t, "apiVersion: stable.example.com/v1\nkind: CronTab\nmetadata:\n  name: ct\n  namespace: d\n", opts)
	if objs[0].HasErrors() {
		t.Fatalf("已安装 CRD 应可创建: %+v", objs[0].Errors())
	}
	if objs[0].GVR.Resource != "crontabs" {
		t.Fatalf("GVR 映射错误: %+v", objs[0].GVR)
	}
}

func TestParseGVK(t *testing.T) {
	if got := ParseGVK("v1", "ConfigMap"); got.Group != "" || got.Version != "v1" {
		t.Fatalf("core 组解析错误: %+v", got)
	}
	if got := ParseGVK("apps/v1", "Deployment"); got.Group != "apps" || got.Version != "v1" {
		t.Fatalf("具名组解析错误: %+v", got)
	}
}
