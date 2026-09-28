package executor

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"
)

var (
	errNotUnstructured = errors.New("fixture: tracker 返回了非 unstructured 对象")
	errDenied          = errors.New("cannot create resource")
)

// --- 三期创建通道测试夹具 ---------------------------------------------------

// ssaKinds 是夹具里注册到 RESTMapper 的类型（覆盖 R2 策略表用到的 Kind）。
var ssaKinds = []struct {
	apiVersion string
	kind       string
	namespaced bool
}{
	{"v1", "Pod", true},
	{"v1", "Service", true},
	{"v1", "ConfigMap", true},
	{"v1", "Secret", true},
	{"v1", "ServiceAccount", true},
	{"v1", "PersistentVolumeClaim", true},
	{"v1", "Event", true},
	{"v1", "Namespace", false},
	{"apps/v1", "Deployment", true},
	{"apps/v1", "StatefulSet", true},
	{"apps/v1", "DaemonSet", true},
	{"batch/v1", "Job", true},
	{"batch/v1", "CronJob", true},
	{"networking.k8s.io/v1", "Ingress", true},
}

// newSSARunner 装配一个只依赖 fake dynamic client 的 Runner。
//
// fake 的 ObjectTracker 不会"apply 不存在则创建"，所以这里装一个 SSA reactor：
// 不存在则创建、存在则深合并，并且尊重 dry run（不落盘），贴近 apiserver 语义。
func newSSARunner(t *testing.T, opts Options, objects ...runtime.Object) (*Runner, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	// client-go 内置 scheme 覆盖所有内置类型（含 List 种类），fake 据此推导 list kind。
	fake := dynamicfake.NewSimpleDynamicClient(clientgoscheme.Scheme, objects...)

	mapper := meta.NewDefaultRESTMapper(nil)
	for _, k := range ssaKinds {
		gvk := ParseGVK(k.apiVersion, k.kind)
		scope := meta.RESTScopeRoot
		if k.namespaced {
			scope = meta.RESTScopeNamespace
		}
		mapper.Add(gvk, scope)
	}

	installSSAReaction(fake)
	opts.Dynamic = fake
	opts.Mapper = mapper
	return NewRunner(opts), fake
}

func installSSAReaction(fake *dynamicfake.FakeDynamicClient) {
	fake.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		// namespaced 与 cluster-scoped 的 patch 动作是同一个 PatchActionImpl（后者 namespace 为空）。
		impl, ok := action.(k8stesting.PatchActionImpl)
		if !ok || impl.PatchType != types.ApplyPatchType {
			return false, nil, nil
		}
		patch := &unstructured.Unstructured{Object: map[string]any{}}
		if err := json.Unmarshal(impl.Patch, &patch.Object); err != nil {
			return true, nil, err
		}
		normalizeNumbers(patch.Object) // 贴近真实 codec：整数解码为 int64
		patch.SetName(impl.Name)
		if impl.Namespace != "" {
			patch.SetNamespace(impl.Namespace)
		}
		tracker := fake.Tracker()
		dryRun := containsString(impl.PatchOptions.DryRun, metav1.DryRunAll)

		existing, err := tracker.Get(impl.Resource, impl.Namespace, impl.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if dryRun {
				return true, patch, nil
			}
			if cerr := tracker.Create(impl.Resource, patch, impl.Namespace); cerr != nil {
				return true, nil, cerr
			}
			got, gerr := tracker.Get(impl.Resource, impl.Namespace, impl.Name, metav1.GetOptions{})
			return true, got, gerr
		}
		if err != nil {
			return true, nil, err
		}
		existingObj, ok := existing.(*unstructured.Unstructured)
		if !ok {
			return true, nil, apierrors.NewInternalError(errNotUnstructured)
		}
		merged := &unstructured.Unstructured{Object: mergeMaps(existingObj.Object, patch.Object)}
		if dryRun {
			return true, merged, nil
		}
		if uerr := tracker.Update(impl.Resource, merged, impl.Namespace); uerr != nil {
			return true, nil, uerr
		}
		got, gerr := tracker.Get(impl.Resource, impl.Namespace, impl.Name, metav1.GetOptions{})
		return true, got, gerr
	})
}

// --- 用例 -----------------------------------------------------------------

func TestApplyCreatesObject(t *testing.T) {
	r, fake := newSSARunner(t, Options{Namespace: "web"})
	res, err := r.Apply(context.Background(), ApplyRequest{Object: configMapContent("cm1", map[string]any{"a": "b"})})
	if err != nil {
		t.Fatal(err)
	}
	if res.Namespace != "web" || res.Name != "cm1" || res.Kind != "ConfigMap" {
		t.Fatalf("结果定位不对: %+v", res)
	}
	if res.Resource != "configmaps" || !res.Namespaced {
		t.Fatalf("映射结果不对: %+v", res)
	}
	if res.Existed {
		t.Fatal("新建对象不应 Existed=true")
	}
	if _, err := fake.Resource(gvr("", "v1", "configmaps")).Namespace("web").Get(context.Background(), "cm1", metav1.GetOptions{}); err != nil {
		t.Fatalf("对象未落盘: %v", err)
	}
}

func TestApplySendsFieldManagerAndNoDryRunByDefault(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	if _, err := r.Apply(context.Background(), ApplyRequest{Object: configMapContent("cm1", nil), Namespace: "default"}); err != nil {
		t.Fatal(err)
	}
	opts := patchOptions(t, fake)
	if opts.FieldManager != FieldManager {
		t.Fatalf("fieldManager = %q，期望 %q", opts.FieldManager, FieldManager)
	}
	if len(opts.DryRun) != 0 {
		t.Fatalf("默认不应带 dryRun: %v", opts.DryRun)
	}
}

func TestApplyDryRunDoesNotPersist(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	res, err := r.Apply(context.Background(), ApplyRequest{
		Object:    configMapContent("cm1", map[string]any{"a": "b"}),
		Namespace: "default",
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun {
		t.Fatal("结果应标记 DryRun")
	}
	if opts := patchOptions(t, fake); len(opts.DryRun) != 1 || opts.DryRun[0] != metav1.DryRunAll {
		t.Fatalf("dry run 没有传给 apiserver: %+v", opts)
	}
	list, err := fake.Resource(gvr("", "v1", "configmaps")).Namespace("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("dry run 不该落盘，实际 %d 个", len(list.Items))
	}
}

func TestApplyMergesIntoExistingObject(t *testing.T) {
	existing := u("apps/v1", "Deployment", "web", "api", nil, map[string]any{
		"spec":   map[string]any{"replicas": int64(1), "paused": true},
		"status": map[string]any{"readyReplicas": int64(1), "replicas": int64(1)},
	})
	r, fake := newSSARunner(t, Options{}, existing)

	obj := u("apps/v1", "Deployment", "web", "api", map[string]any{"spec": map[string]any{"replicas": int64(3)}}, nil)
	res, err := r.Apply(context.Background(), ApplyRequest{Object: obj.Object})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Existed {
		t.Fatal("已存在对象应 Existed=true")
	}
	got := getUnstructured(t, fake, gvr("apps", "v1", "deployments"), "web", "api")
	if replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas"); replicas != 3 {
		t.Fatalf("replicas 未更新: %v", replicas)
	}
	// SSA 只声明自己写的字段：patch 没提的 paused 与 status 必须保留。
	if paused, _, _ := unstructured.NestedBool(got.Object, "spec", "paused"); !paused {
		t.Fatal("SSA 不应清掉未声明的 spec.paused")
	}
	if ready, _, _ := unstructured.NestedInt64(got.Object, "status", "readyReplicas"); ready != 1 {
		t.Fatal("提交不应改写 status")
	}
}

func TestApplyStripsServerFields(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	obj := u("v1", "ConfigMap", "default", "cm1", map[string]any{"data": map[string]any{"a": "b"}}, nil)
	obj.SetUID(types.UID("11111111-1111-1111-1111-111111111111"))
	obj.SetResourceVersion("999")
	obj.SetGeneration(7)
	if err := unstructured.SetNestedField(obj.Object, map[string]any{"x": "y"}, "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), ApplyRequest{Object: obj.Object}); err != nil {
		t.Fatal(err)
	}
	got := getUnstructured(t, fake, gvr("", "v1", "configmaps"), "default", "cm1")
	if got.GetResourceVersion() == "999" {
		t.Fatal("resourceVersion 应被剥离（由集群接管）")
	}
	if got.GetUID() == "11111111-1111-1111-1111-111111111111" {
		t.Fatal("uid 应被剥离")
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "status"); found {
		t.Fatal("status 不应由创建通道写入")
	}
}

func TestApplyClusterScopedIgnoresNamespace(t *testing.T) {
	r, _ := newSSARunner(t, Options{Namespace: "ignored"})
	res, err := r.Apply(context.Background(), ApplyRequest{Object: u("v1", "Namespace", "", "prod", nil, nil).Object})
	if err != nil {
		t.Fatal(err)
	}
	if res.Namespace != "" || res.Namespaced {
		t.Fatalf("cluster-scoped 结果不该带 namespace: %+v", res)
	}
}

func TestApplyRejectsIncompleteObject(t *testing.T) {
	r, _ := newSSARunner(t, Options{})
	cases := map[string]map[string]any{
		"空内容":    {},
		"缺 kind": {"apiVersion": "v1", "metadata": map[string]any{"name": "x"}},
		"缺 name": {"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{}},
	}
	for name, content := range cases {
		if _, err := r.Apply(context.Background(), ApplyRequest{Object: content}); err == nil {
			t.Errorf("%s：应报错", name)
		}
	}
}

func TestApplyUnknownKindIsNoMatch(t *testing.T) {
	r, _ := newSSARunner(t, Options{})
	_, err := r.Apply(context.Background(), ApplyRequest{Object: u("example.com/v1", "Widget", "default", "w1", nil, nil).Object})
	if err == nil {
		t.Fatal("未注册类型应报错")
	}
	if !IsUnmappedKind(err) {
		t.Fatalf("应识别为集群里没有该类型，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("错误信息应说明类型在集群中不可用: %v", err)
	}
}

func TestApplyForbiddenIsReportedAsRBAC(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	fake.PrependReactor("patch", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "cm1", errDenied)
	})
	_, err := r.Apply(context.Background(), ApplyRequest{Object: configMapContent("cm1", nil), Namespace: "default"})
	if err == nil || !strings.Contains(err.Error(), "RBAC 拒绝") {
		t.Fatalf("应给出 RBAC 拒绝说明，实际: %v", err)
	}
}

func TestApplyWithoutChannelGivesActionableError(t *testing.T) {
	r := NewRunner(Options{})
	_, err := r.Apply(context.Background(), ApplyRequest{Object: configMapContent("cm1", nil)})
	if err == nil || !strings.Contains(err.Error(), "写通道未装配") {
		t.Fatalf("缺少集群配置时应给出可行动说明，实际: %v", err)
	}
}

func TestApplySecretOnlyAcceptsPlaceholder(t *testing.T) {
	r, _ := newSSARunner(t, Options{})

	literal := u("v1", "Secret", "web", "s1", map[string]any{"stringData": map[string]any{"pw": "hunter2"}}, nil)
	if _, err := r.Apply(context.Background(), ApplyRequest{Object: literal.Object}); err == nil ||
		!strings.Contains(err.Error(), SecretPlaceholder) {
		t.Fatalf("模型编写的 Secret 值必须被拒绝并提示占位符，实际: %v", err)
	}

	dataValues := u("v1", "Secret", "web", "s2", map[string]any{"data": map[string]any{"pw": "aHVudGVyMg=="}}, nil)
	if _, err := r.Apply(context.Background(), ApplyRequest{Object: dataValues.Object}); err == nil {
		t.Fatal("data 里的显式值必须被拒绝")
	}

	empty := u("v1", "Secret", "web", "s3", map[string]any{}, nil)
	if _, err := r.Apply(context.Background(), ApplyRequest{Object: empty.Object}); err == nil {
		t.Fatal("没有任何 key 的 Secret 应被拒绝")
	}
}

func TestApplySecretGeneratesValuesAndNeverLeaksThem(t *testing.T) {
	r, fake := newSSARunner(t, Options{})
	obj := u("v1", "Secret", "web", "s1", map[string]any{"type": "Opaque", "stringData": map[string]any{"token": SecretPlaceholder, "password": SecretPlaceholder}}, nil)
	res, err := r.Apply(context.Background(), ApplyRequest{Object: obj.Object})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.SecretKeys, ",") != "password,token" || strings.Join(res.GeneratedKeys, ",") != "password,token" {
		t.Fatalf("只应返回 key 名: %+v", res)
	}

	got := getUnstructured(t, fake, gvr("", "v1", "secrets"), "web", "s1")
	stringData, found, _ := unstructured.NestedStringMap(got.Object, "stringData")
	if !found || len(stringData) != 2 {
		t.Fatalf("Secret 未写入集群: %+v", got.Object)
	}
	for key, value := range stringData {
		if value == SecretPlaceholder || len(value) != secretKeyLength {
			t.Fatalf("%s 的生成值不符合预期: %q", key, value)
		}
	}

	// 红线：工具结果里不能出现内容值（连密文都不行）。
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range stringData {
		if strings.Contains(string(encoded), value) {
			t.Fatal("ApplyResult 泄露了 Secret 值")
		}
	}
	// 调用方传入的 map 不能被就地改写（生成的值只存在于深拷贝里）。
	if passed, _, _ := unstructured.NestedStringMap(obj.Object, "stringData"); passed["token"] != SecretPlaceholder {
		t.Fatal("Apply 不应改写调用方的对象")
	}
}

func TestApplySecretSkipsExistenceRead(t *testing.T) {
	// 已有 Secret 存在时也不应发起 GET：内容值不得进入本进程（RBAC 因此无需 secrets get）。
	r, fake := newSSARunner(t, Options{}, u("v1", "Secret", "web", "s1", map[string]any{"data": map[string]any{"pw": "cG9p"}}))
	if _, err := r.Apply(context.Background(), ApplyRequest{Object: u("v1", "Secret", "web", "s1", map[string]any{"stringData": map[string]any{"token": SecretPlaceholder}}).Object}); err != nil {
		t.Fatal(err)
	}
	for _, action := range fake.Actions() {
		if action.GetVerb() == "get" && action.GetResource().Resource == "secrets" {
			t.Fatal("创建 Secret 不应读取已有 Secret")
		}
	}
}

func TestGetObjectStatus(t *testing.T) {
	ctx := context.Background()
	r, _ := newSSARunner(t, Options{},
		u("v1", "Namespace", "", "web"),
		u("v1", "ConfigMap", "web", "cm1", map[string]any{"data": map[string]any{"a": "b"}}),
	)

	status, err := r.GetObjectStatus(ctx, ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "web", Name: "cm1"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Found || status.NamespaceMissing {
		t.Fatalf("已存在对象判定错误: %+v", status)
	}

	missing, err := r.GetObjectStatus(ctx, ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "web", Name: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Found || missing.NamespaceMissing {
		t.Fatalf("对象不存在但命名空间存在: %+v", missing)
	}

	orphan, err := r.GetObjectStatus(ctx, ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "ghost", Name: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if !orphan.NamespaceMissing {
		t.Fatalf("应提示命名空间不存在: %+v", orphan)
	}

	if _, err := r.GetObjectStatus(ctx, ObjectRef{APIVersion: "v1", Kind: "ConfigMap", Namespace: "web"}); err == nil {
		t.Fatal("缺少 name 应报错")
	}
}

func TestMappingFor(t *testing.T) {
	r, _ := newSSARunner(t, Options{})
	mapping, err := r.MappingFor("apps/v1", "Deployment")
	if err != nil {
		t.Fatal(err)
	}
	if mapping.Resource != "deployments" || !mapping.Namespaced {
		t.Fatalf("映射错误: %+v", mapping)
	}
	cluster, err := r.MappingFor("v1", "Namespace")
	if err != nil || cluster.Namespaced {
		t.Fatalf("Namespace 应为 cluster-scoped: %+v %v", cluster, err)
	}
	if _, err := r.MappingFor("", "Deployment"); err == nil {
		t.Fatal("apiVersion 为空应报错")
	}
	if _, err := r.MappingFor("v1", "Nope"); err == nil || !IsUnmappedKind(err) {
		t.Fatalf("未知类型应报 no-match，实际: %v", err)
	}
}

func TestParseGVK(t *testing.T) {
	cases := []struct {
		apiVersion, group, version string
	}{
		{"v1", "", "v1"},
		{"apps/v1", "apps", "v1"},
		{"networking.k8s.io/v1", "networking.k8s.io", "v1"},
		{" v1 ", "", "v1"},
	}
	for _, c := range cases {
		gvk := ParseGVK(c.apiVersion, "Pod")
		if gvk.Group != c.group || gvk.Version != c.version || gvk.Kind != "Pod" {
			t.Errorf("ParseGVK(%q) = %+v", c.apiVersion, gvk)
		}
	}
}

func TestGenerateSecretValue(t *testing.T) {
	first, err := GenerateSecretValue(0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateSecretValue(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != secretKeyLength || len(second) != 20 {
		t.Fatalf("长度不对: %d/%d", len(first), len(second))
	}
	if first == second {
		t.Fatal("随机值不应重复")
	}
	for _, r := range first + second {
		if !strings.ContainsRune(secretKeyCharset, r) {
			t.Fatalf("字符集外字符: %q", r)
		}
	}
}

// --- 夹具辅助 --------------------------------------------------------------

func gvr(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}

// u 构造 unstructured 对象；body 按顶层键合并进对象（spec/data/status 等）。
func u(apiVersion, kind, ns, name string, body ...map[string]any) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": apiVersion, "kind": kind}
	metadata := map[string]any{"name": name}
	if ns != "" {
		metadata["namespace"] = ns
	}
	obj["metadata"] = metadata
	for _, b := range body {
		for k, v := range b {
			obj[k] = v
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

func configMapContent(name string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{"k": "v"}
	}
	return u("v1", "ConfigMap", "", name, map[string]any{"data": data}).Object
}

func getUnstructured(t *testing.T, fake *dynamicfake.FakeDynamicClient, resource schema.GroupVersionResource, ns, name string) *unstructured.Unstructured {
	t.Helper()
	var obj *unstructured.Unstructured
	var err error
	if ns == "" {
		obj, err = fake.Resource(resource).Get(context.Background(), name, metav1.GetOptions{})
	} else {
		obj, err = fake.Resource(resource).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	}
	if err != nil {
		t.Fatalf("读取 %s/%s: %v", resource.Resource, name, err)
	}
	return obj
}

// patchOptions 取回最近一次 patch 的 PatchOptions（断言 dryRun/fieldManager）。
func patchOptions(t *testing.T, fake *dynamicfake.FakeDynamicClient) metav1.PatchOptions {
	t.Helper()
	for _, action := range fake.Actions() {
		if a, ok := action.(k8stesting.PatchActionImpl); ok {
			return a.PatchOptions
		}
	}
	t.Fatal("没有 patch 动作")
	return metav1.PatchOptions{}
}

func mergeMaps(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(existing, sub)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// normalizeNumbers 把 json.Unmarshal 产生的 float64 还原成 int64，
// 与 UnstructuredJSONScheme 的解码口径一致（否则 NestedInt64 读不到副本数等字段）。
func normalizeNumbers(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for k, item := range value {
			value[k] = normalizeNumbers(item)
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = normalizeNumbers(item)
		}
		return value
	case float64:
		if value == math.Trunc(value) && math.Abs(value) < 1<<53 {
			return int64(value)
		}
		return value
	default:
		return v
	}
}
