// Package manifestx 是三期创建通道的 manifest 底座：多文档 YAML/JSON 解析、
// GVK→GVR 映射与 scope 判定、必填字段与命名校验、kind/scope 门禁、危险字段扫描、
// canonical 指纹与 Secret 值脱敏。
//
// 安全边界：本包只解析与校验，不执行任何写操作；提交动作只发生在
// internal/executor（架构测试守护写边界）。Secret 明文值不参与
// Object 的 JSON 序列化，因此不会经工具结果进入 LLM 上下文、会话或审计。
package manifestx

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Scope 是资源作用域。
type Scope string

const (
	ScopeNamespaced Scope = "namespaced"
	ScopeCluster    Scope = "cluster"
	ScopeUnknown    Scope = "unknown"
)

// Severity 是校验结论的严重程度。error 阻断创建，warn 只进入审批提案。
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

// 校验/拒绝码：进入审批提案、审计与工具错误码，属于稳定契约。
const (
	CodeTemplateExpr        = "template_expression"        // 含 helm/kustomize 模板表达式
	CodeInvalidManifest     = "invalid_manifest"           // YAML/JSON 语法错误
	CodeEmptyManifest       = "empty_manifest"             // 没有任何有效文档
	CodeTooManyObjects      = "too_many_objects"           // 超过单次对象数上限
	CodeManifestTooLarge    = "manifest_too_large"         // 超过单次字节上限
	CodeMissingAPIVersion   = "missing_api_version"        //
	CodeMissingKind         = "missing_kind"               //
	CodeMissingName         = "missing_name"               //
	CodeInvalidName         = "invalid_name"               //
	CodeDefaultedNamespace  = "defaulted_namespace"        // namespace 缺省，按默认命名空间补齐
	CodeNamespaceOnCluster  = "namespace_on_cluster"       // cluster-scoped 资源带了 namespace
	CodeDeniedKind          = "denied_kind"                // 命中 deny_kinds 黑名单
	CodeKindNotAllowed      = "kind_not_allowed"           // allow_kinds 白名单未包含
	CodeClusterKindDenied   = "cluster_kind_denied"        // cluster-scoped 未授权
	CodeUnmappedKind        = "unmapped_kind"              // 集群里没有该类型（CRD 未安装等）
	CodeDangerousField      = "dangerous_field"            // 命中危险字段
	CodeImageUnpinned       = "image_tag_unpinned"         // 镜像缺 tag 或 :latest
	CodeSecretLiteralValue  = "secret_literal_value"       // Secret 值不是占位符
	CodeSecretNoKeys        = "secret_no_keys"             // Secret 没有任何 key
	CodeSecretInvalidData   = "secret_invalid_data"        // data 字段不是合法 base64
	CodeSecretBadDataPlaceh = "secret_placeholder_in_data" // 占位符只能用在 stringData
)

// Finding 是一条结构化校验结论（可定位字段，进入审批提案）。
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Field    string   `json:"field,omitempty"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix,omitempty"`
}

// Err 判断结论是否阻断创建。
func (f Finding) Err() bool { return f.Severity == SeverityError }

// SecretKeyFingerprint 是 Secret 单个 key 的脱敏指纹（值永不外流）。
type SecretKeyFingerprint struct {
	Key       string `json:"key"`
	Length    int    `json:"length"`
	SHA256    string `json:"sha256"`
	Source    string `json:"source"` // stringData / generate
	Generated bool   `json:"generated,omitempty"`
}

// Limits 是单次创建的资源规模上限。零值取 DefaultLimits。
type Limits struct {
	MaxObjects int
	MaxBytes   int
}

// DefaultLimits 返回与配置默认值一致的上限。
func DefaultLimits() Limits {
	return Limits{MaxObjects: 3, MaxBytes: 8192}
}

func (l Limits) withDefaults() Limits {
	if l.MaxObjects <= 0 {
		l.MaxObjects = DefaultLimits().MaxObjects
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultLimits().MaxBytes
	}
	return l
}

// Options 是 Parse 的装配参数。
type Options struct {
	// Policy 是 kind/scope 门禁（来自 agent.create.*）。
	Policy Policy
	// Limits 是规模上限（来自 agent.create.max_*）。
	Limits Limits
	// Mapper 解析 GVK→GVR 与 scope；nil 时跳过映射与未知类型判定
	// （仅用于无集群连接的离线校验场景）。
	Mapper Mapper
	// DefaultNamespace 是 namespaced 资源缺省 namespace 时的补齐值；空取 default。
	DefaultNamespace string
	// SecretAccess 是 agent.secret.access（off / create_only / read_write）。
	SecretAccess string
}

// Object 是一个已解析、已校验的 manifest 对象。
//
// content 故意不导出：Object 可以被安全地 JSON 序列化后交给 LLM/Web/审计，
// Secret 明文值只存在于 content 中，只有显式调用 Content() 才能取到。
type Object struct {
	Index       int                         `json:"index"`
	APIVersion  string                      `json:"apiVersion,omitempty"`
	Kind        string                      `json:"kind,omitempty"`
	Name        string                      `json:"name,omitempty"`
	Namespace   string                      `json:"namespace,omitempty"`
	GVK         schema.GroupVersionKind     `json:"gvk"`
	GVR         schema.GroupVersionResource `json:"gvr,omitempty"`
	Scope       Scope                       `json:"scope"`
	Mapped      bool                        `json:"mapped"`
	UnknownKind bool                        `json:"unknownKind"`
	Fingerprint string                      `json:"fingerprint"`
	Manifest    string                      `json:"manifest"`
	Findings    []Finding                   `json:"findings,omitempty"`
	SecretKeys  []SecretKeyFingerprint      `json:"secretKeys,omitempty"`
	Dangerous   []string                    `json:"dangerous,omitempty"`

	content map[string]any
}

// Content 返回待提交的对象内容（可能含 Secret 明文，仅限 executor 提交路径使用）。
func (o *Object) Content() map[string]any { return o.content }

// HasErrors 判断是否存在阻断创建的结论。
func (o *Object) HasErrors() bool {
	for _, f := range o.Findings {
		if f.Err() {
			return true
		}
	}
	return false
}

// Errors 返回阻断级结论。
func (o *Object) Errors() []Finding { return filterFindings(o.Findings, true) }

// Warnings 返回提示级结论。
func (o *Object) Warnings() []Finding { return filterFindings(o.Findings, false) }

// IsSecret 判断是否为 Secret 资源。
func (o *Object) IsSecret() bool { return isSecretKind(o.Kind) }

func filterFindings(in []Finding, wantErr bool) []Finding {
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		if f.Err() == wantErr {
			out = append(out, f)
		}
	}
	return out
}

// Parse 解析并校验整段 manifest（支持多文档）。
//
// 返回值语义：整段级失败（超限、模板表达式、语法错误、空文档）返回 error；
// 单对象级问题（缺字段、kind 被拒、危险字段、Secret 值非法）以 Finding 返回，
// 便于把完整清单一次性呈现给人工审批。
func Parse(manifest string, opts Options) ([]Object, error) {
	limits := opts.Limits.withDefaults()
	if limits.MaxBytes > 0 && len(manifest) > limits.MaxBytes {
		return nil, fmt.Errorf("%s: manifest 长度 %d 字节超过上限 %d", CodeManifestTooLarge, len(manifest), limits.MaxBytes)
	}
	if problem, ok := templateProblem(manifest); ok {
		return nil, fmt.Errorf("%s: %s", CodeTemplateExpr, problem)
	}
	docs, err := DecodeDocuments(manifest)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", CodeInvalidManifest, err)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("%s: manifest 不含任何资源文档", CodeEmptyManifest)
	}
	if len(docs) > limits.MaxObjects {
		return nil, fmt.Errorf("%s: 单次最多创建 %d 个对象，当前 %d 个；请拆分请求", CodeTooManyObjects, limits.MaxObjects, len(docs))
	}
	out := make([]Object, 0, len(docs))
	for i, content := range docs {
		out = append(out, buildObject(i, content, opts))
	}
	return out, nil
}

// HasErrors 判断整份清单里是否存在阻断创建的结论。
func HasErrors(objs []Object) bool {
	for i := range objs {
		if objs[i].HasErrors() {
			return true
		}
	}
	return false
}

// Fingerprints 返回清单指纹（顺序与文档一致），用于提案一致性校验。
func Fingerprints(objs []Object) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Fingerprint)
	}
	return out
}
