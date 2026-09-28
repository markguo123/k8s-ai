// 三期创建通道的提交实现（R2.3/R2.4/R2.5）：Server-Side Apply + dry run + 存在性检查。
//
// 这里是全工程唯一把 manifest 写进集群的地方（架构测试守护）。采用 SSA 而非 Create/Update，
// 是为了让字段所有权由 apiserver 裁决：k8s-ai 只声明自己写的字段，不会覆盖别人管理的字段。
package executor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// FieldManager 是 SSA 的字段归属标识（与 kubectl 的 "kubectl-cli" 区分开）。
const FieldManager = "k8s-ai"

// SecretPlaceholder 与 manifestx.PlaceholderGenerate 同值：
// manifest 里只出现占位符，真实值由执行器在提交前的深拷贝里生成（三期 D-06/D-12）。
const SecretPlaceholder = "@generate"

const (
	secretKeyLength  = 32
	secretKeyCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// stripMetadata 是提交前剥离的服务端回填字段（与 manifestx 指纹口径一致）。
var stripMetadata = []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields", "selfLink", "deletionTimestamp"}

// mappingOf 解析 GVK → GVR + scope。
func (r *Runner) mappingOf(gvk schema.GroupVersionKind) (schema.GroupVersionResource, bool, error) {
	ch, err := r.channel()
	if err != nil {
		return schema.GroupVersionResource{}, false, err
	}
	mapping, err := ch.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, false, fmt.Errorf("类型 %s/%s 在集群中不可用: %w", gvk.GroupVersion().String(), gvk.Kind, err)
	}
	return mapping.Resource, mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// MappingFor 解析 apiVersion/kind → 资源名与 scope（discovery 结论，供工具层做校验与风险判定）。
func (r *Runner) MappingFor(apiVersion, kind string) (ApplyMapping, error) {
	gvk := ParseGVK(apiVersion, kind)
	if gvk.Kind == "" || gvk.Version == "" {
		return ApplyMapping{}, fmt.Errorf("无法解析资源类型：apiVersion=%q kind=%q", apiVersion, kind)
	}
	gvr, namespaced, err := r.mappingOf(gvk)
	if err != nil {
		return ApplyMapping{}, err
	}
	return ApplyMapping{Resource: gvr.Resource, Namespaced: namespaced}, nil
}

// IsUnmappedKind 判断错误是否表示集群里没有该类型（CRD 未安装、拼错 kind 等）。
func IsUnmappedKind(err error) bool { return meta.IsNoMatchError(err) }

// ObjectStatus 是存在性检查结果（R3.6 用它把"已存在"转成 patch/update 建议）。
type ObjectStatus struct {
	Found            bool   `json:"found"`
	UID              string `json:"uid,omitempty"`
	ResourceVersion  string `json:"resourceVersion,omitempty"`
	NamespaceMissing bool   `json:"namespaceMissing,omitempty"` // 目标命名空间不存在（创建前可提示先建 ns）
}

// GetObjectStatus 查询对象是否存在（dynamic Get）。
func (r *Runner) GetObjectStatus(ctx context.Context, ref ObjectRef) (ObjectStatus, error) {
	obj, _, err := r.getObject(ctx, ref)
	if err == nil {
		return ObjectStatus{Found: true, UID: string(obj.GetUID()), ResourceVersion: obj.GetResourceVersion()}, nil
	}
	if apierrors.IsNotFound(err) {
		// apiserver 对"命名空间不存在"和普通 NotFound 不区分 reason，这里显式复查一次。
		return ObjectStatus{NamespaceMissing: r.namespaceMissing(ctx, ref)}, nil
	}
	return ObjectStatus{}, err
}

// namespaceMissing 在对象不存在时确认目标命名空间是否也不存在（"先建 ns"提示的来源）。
func (r *Runner) namespaceMissing(ctx context.Context, ref ObjectRef) bool {
	if strings.TrimSpace(ref.Namespace) == "" || strings.EqualFold(ref.Kind, "Namespace") {
		return false
	}
	_, _, err := r.getObject(ctx, ObjectRef{APIVersion: "v1", Kind: "Namespace", Name: ref.Namespace})
	return apierrors.IsNotFound(err)
}

// getObject 取回对象（未就绪判定与存在性检查共用）。
func (r *Runner) getObject(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	gvk := ParseGVK(ref.APIVersion, ref.Kind)
	if gvk.Kind == "" || gvk.Version == "" || strings.TrimSpace(ref.Name) == "" {
		return nil, schema.GroupVersionResource{}, errors.New("查询对象需要 apiVersion/kind/name")
	}
	gvr, namespaced, err := r.mappingOf(gvk)
	if err != nil {
		return nil, gvr, err
	}
	res, err := r.resource(gvr, ref.Namespace, namespaced)
	if err != nil {
		return nil, gvr, err
	}
	obj, err := res.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, gvr, err
	}
	return obj, gvr, nil
}

// Apply 用 Server-Side Apply 提交单个对象（fieldManager=k8s-ai）。
//
// 语义：
//   - DryRun=true 时走 apiserver 全量校验但不落盘，返回的结论与真实提交一致；
//   - Existed 表示提交前集群里已有同名对象（R3.6 据此把"已存在"转成更新建议）；
//   - Verify=true 且非 dry run 时，提交后就绪判定结果挂在 Verify 上，
//     未就绪不推翻"已创建"这一事实。
func (r *Runner) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	obj, err := prepareObject(req.Object)
	if err != nil {
		return ApplyResult{}, err
	}
	gvk := ParseGVK(obj.GetAPIVersion(), obj.GetKind())
	gvr, namespaced, err := r.mappingOf(gvk)
	if err != nil {
		return ApplyResult{}, err
	}
	namespace := obj.GetNamespace()
	if namespaced {
		if namespace == "" {
			namespace = r.nsOr(req.Namespace)
		}
		obj.SetNamespace(namespace)
	} else {
		obj.SetNamespace("")
	}
	name := obj.GetName()

	var secretKeys, generatedKeys []string
	isSecret := strings.EqualFold(gvk.Kind, "Secret")
	if isSecret {
		secretKeys, generatedKeys, err = materializeSecret(obj)
		if err != nil {
			return ApplyResult{}, err
		}
	}

	res, err := r.resource(gvr, namespace, namespaced)
	if err != nil {
		return ApplyResult{}, err
	}
	// Secret 不做存在性 GET：GET 会把已有 Secret 的内容值读进本进程，
	// 而红线要求内容值永不进入结果/会话/审计（SECURITY.md §3）。
	// 因此 Secret 的 RBAC 只需 create/patch/update，覆盖风险由 R0.3 风险映射（Secret→HIGH）
	// 的人工审批兜底，而不是靠"先读后写"。
	var before ObjectStatus
	if !isSecret {
		status, statusErr := r.GetObjectStatus(ctx, ObjectRef{APIVersion: obj.GetAPIVersion(), Kind: obj.GetKind(), Namespace: namespace, Name: name})
		if statusErr != nil && !apierrors.IsNotFound(statusErr) {
			return ApplyResult{}, fmt.Errorf("检查 %s/%s 是否存在失败: %w", namespace, name, statusErr)
		}
		before = status
	}

	data, err := json.Marshal(obj.Object)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("序列化对象失败: %w", err)
	}
	opts := metav1.PatchOptions{FieldManager: r.fieldManager}
	if req.DryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	got, err := res.Patch(ctx, name, types.ApplyPatchType, data, opts)
	if err != nil {
		return ApplyResult{}, applyError(err, obj.GetKind(), namespace, name, req.DryRun)
	}

	out := ApplyResult{
		APIVersion:      got.GetAPIVersion(),
		Kind:            got.GetKind(),
		Namespace:       got.GetNamespace(),
		Name:            got.GetName(),
		Resource:        gvr.Resource,
		Namespaced:      namespaced,
		DryRun:          req.DryRun,
		Existed:         before.Found,
		UID:             string(got.GetUID()),
		ResourceVersion: got.GetResourceVersion(),
		Generation:      got.GetGeneration(),
		Summary:         observe(got).fields,
		SecretKeys:      secretKeys,
		GeneratedKeys:   generatedKeys,
	}
	if req.Verify && !req.DryRun {
		vres, verr := r.Verify(ctx, VerifyRequest{Ref: ObjectRef{APIVersion: out.APIVersion, Kind: out.Kind, Namespace: out.Namespace, Name: out.Name}, Timeout: req.VerifyTimeout})
		if verr != nil {
			out.Verify = &VerifyResult{APIVersion: out.APIVersion, Kind: out.Kind, Namespace: out.Namespace, Name: out.Name, Reason: fmt.Sprintf("就绪校验失败：%v", verr)}
		} else {
			out.Verify = &vres
		}
	}
	return out, nil
}

// prepareObject 深拷贝并剥离服务端回填字段（不修改调用方的 map）。
func prepareObject(content map[string]any) (*unstructured.Unstructured, error) {
	if len(content) == 0 {
		return nil, errors.New("对象内容为空")
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("对象内容不可序列化: %w", err)
	}
	var copy map[string]any
	if err := json.Unmarshal(raw, &copy); err != nil {
		return nil, fmt.Errorf("对象内容不可序列化: %w", err)
	}
	obj := &unstructured.Unstructured{Object: copy}
	delete(copy, "status")
	meta := obj.Object["metadata"]
	if m, ok := meta.(map[string]any); ok {
		for _, f := range stripMetadata {
			delete(m, f)
		}
	}
	if obj.GetAPIVersion() == "" || obj.GetKind() == "" || obj.GetName() == "" {
		return nil, errors.New("对象缺少 apiVersion/kind/metadata.name，无法提交")
	}
	return obj, nil
}

// materializeSecret 把 stringData 里的 @generate 换成 crypto/rand 生成的随机值。
//
// 红线（AGENTS.md LLM Safety）：Secret 内容值不得由模型编写，也不得回流到
// 工具结果 / 会话 / 审计。因此这里只接受占位符，且只写入深拷贝后的对象。
// 返回 key 列表与其中由执行器生成的 key（都不含值）。
func materializeSecret(obj *unstructured.Unstructured) (keys, generated []string, err error) {
	if data, ok, _ := unstructured.NestedMap(obj.Object, "data"); ok && len(data) > 0 {
		names := make([]string, 0, len(data))
		for k := range data {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, nil, fmt.Errorf("Secret 的 data 含显式值（%s）；只允许 stringData 里的 %s 占位符", strings.Join(names, ", "), SecretPlaceholder)
	}
	stringData, ok, _ := unstructured.NestedStringMap(obj.Object, "stringData")
	if !ok || len(stringData) == 0 {
		return nil, nil, errors.New("Secret 没有可创建的 key；请在 stringData 里为每个 key 填 " + SecretPlaceholder)
	}
	out := make(map[string]any, len(stringData))
	for _, name := range sortedMapKeys(stringData) {
		value := stringData[name]
		if value != SecretPlaceholder {
			return nil, nil, fmt.Errorf("Secret 的 stringData.%s 含模型编写的值；请改为 %s，由执行器生成随机值", name, SecretPlaceholder)
		}
		secret, gerr := GenerateSecretValue(secretKeyLength)
		if gerr != nil {
			return nil, nil, gerr
		}
		out[name] = secret
		generated = append(generated, name)
	}
	if err := unstructured.SetNestedStringMap(obj.Object, toStringAnyMap(out), "stringData"); err != nil {
		return nil, nil, fmt.Errorf("写入生成的 Secret 值失败: %w", err)
	}
	return sortedMapKeysAny(out), generated, nil
}

// GenerateSecretValue 用 crypto/rand 生成长度为 n 的随机密钥（字符集避免 shell/URL 特殊字符）。
func GenerateSecretValue(n int) (string, error) {
	if n <= 0 {
		n = secretKeyLength
	}
	max := big.NewInt(int64(len(secretKeyCharset)))
	var sb strings.Builder
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("生成随机密钥失败: %w", err)
		}
		sb.WriteByte(secretKeyCharset[idx.Int64()])
	}
	return sb.String(), nil
}

// applyError 把 apiserver 错误转成对模型友好的说明（保留原始错误用于审计）。
func applyError(err error, kind, namespace, name string, dryRun bool) error {
	subject := fmt.Sprintf("%s %s/%s", strings.ToLower(kind), namespace, name)
	if namespace == "" {
		subject = fmt.Sprintf("%s %s", strings.ToLower(kind), name)
	}
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("RBAC 拒绝创建 %s：%w（真实权限边界来自集群 RBAC，k8s-ai 不会绕过）", subject, err)
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return fmt.Errorf("%s 未通过 apiserver 校验：%w", subject, err)
	case apierrors.IsConflict(err):
		return fmt.Errorf("%s 存在并发修改，请重试：%w", subject, err)
	case apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%s 已存在（非本工具管理）：%w", subject, err)
	default:
		verb := "提交"
		if dryRun {
			verb = "预演"
		}
		return fmt.Errorf("%s %s 失败: %w", verb, subject, err)
	}
}

func toStringAnyMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMapKeysAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
