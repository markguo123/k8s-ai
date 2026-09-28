// Package executor 是二期唯一允许执行写操作与 os/exec 的包
// （架构测试守护）：kubectl 以 argv 直调二进制（无 shell），其余走 client-go。
package executor

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Result 是一次命令执行的结果。
type Result struct {
	Command       string         `json:"command"`
	Stdout        string         `json:"stdout,omitempty"`
	Stderr        string         `json:"stderr,omitempty"`
	ExitCode      int            `json:"exitCode"`
	Truncated     bool           `json:"truncated,omitempty"`
	StreamTimeout bool           `json:"streamTimeout,omitempty"` // 流式命令（logs -f / get -w）超时后捕获的部分输出
	Verification  map[string]any `json:"verification,omitempty"`  // 写操作执行后的状态核对
}

// Options 是执行器装配参数。
type Options struct {
	// KubectlBin kubectl 二进制路径；空 = PATH 查找 "kubectl"。
	KubectlBin string
	// Kubeconfig 传给 kubectl 子进程的 KUBECONFIG 环境变量；
	// 空 = 使用进程默认 kubeconfig 加载规则（与 client-go 保持一致）。
	Kubeconfig string
	// MaxBytes 单次命令输出上限（默认 64KB）。
	MaxBytes int
	// DenyOps 硬拦截子串（agent.deny_ops）。
	DenyOps []string
	// Clientset client-go 客户端（写操作）。
	Clientset kubernetes.Interface
	// Namespace 默认命名空间（结构化写操作缺省时使用）。
	Namespace string
	// StreamTimeout 流式命令（logs -f / get -w）的最大采样时长；<=0 取默认 7s。
	// 到时后捕获已收到的部分输出并标记 StreamTimeout，不视为错误。
	StreamTimeout time.Duration

	// --- 三期创建通道（R2）：nil 时创建/校验能力不可用，调用方得到明确错误 ---

	// RESTConfig 集群连接配置（由 internal/kubernetes.NewRESTConfig 构造，同一 kubeconfig 口径）。
	RESTConfig *rest.Config
	// Dynamic 动态客户端；测试注入用，nil 且 RESTConfig 非空时按 RESTConfig 惰性构造。
	Dynamic dynamic.Interface
	// Mapper GVK→GVR/scope 解析器；测试注入用，nil 且 RESTConfig 非空时用 discovery 构造。
	Mapper meta.RESTMapper
	// FieldManager Server-Side Apply 的 fieldManager；空取 "k8s-ai"。
	FieldManager string
}

// ObjectRef 定位一个集群对象（存在性检查与执行后校验共用）。
type ObjectRef struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// ApplyMapping 是 GVK 解析结果（executor 自有类型：工具层与 manifestx 不因此 import client-go）。
type ApplyMapping struct {
	// Resource 是复数资源名（如 "deployments"）。
	Resource string
	// Namespaced 表示资源是否属于某个命名空间。
	Namespaced bool
}

// ApplyRequest 描述一次单对象提交（内容由 manifestx 解析、校验并脱敏后交进来）。
type ApplyRequest struct {
	// Object 是 unstructured 内容；Apply 内部使用深拷贝，不会改写调用方的 map。
	Object map[string]any
	// Namespace 为空时回落到 Runner 的默认命名空间（cluster-scoped 资源忽略该字段）。
	Namespace string
	// DryRun 走 server-side dry run：apiserver 全量校验但不落盘。
	DryRun bool
	// Verify 在提交后等待对象就绪（dry run 下不生效）。
	Verify bool
	// VerifyTimeout 就绪等待上限；<=0 取默认 30s。
	VerifyTimeout time.Duration
}

// ApplyResult 是单对象的提交结果。
type ApplyResult struct {
	APIVersion      string         `json:"apiVersion,omitempty"`
	Kind            string         `json:"kind,omitempty"`
	Namespace       string         `json:"namespace,omitempty"`
	Name            string         `json:"name,omitempty"`
	Resource        string         `json:"resource,omitempty"`
	Namespaced      bool           `json:"namespaced"`
	DryRun          bool           `json:"dryRun,omitempty"`
	Existed         bool           `json:"existed"` // 提交前集群里是否已有同名对象
	UID             string         `json:"uid,omitempty"`
	ResourceVersion string         `json:"resourceVersion,omitempty"`
	Generation      int64          `json:"generation,omitempty"`
	Summary         map[string]any `json:"summary,omitempty"` // 关键状态摘要（副本数/phase 等）
	SecretKeys      []string       `json:"secretKeys,omitempty"`
	GeneratedKeys   []string       `json:"generatedKeys,omitempty"` // 由执行器生成随机值的 Secret key
	Verify          *VerifyResult  `json:"verify,omitempty"`
}

// VerifyRequest 是执行后就绪校验请求。
type VerifyRequest struct {
	Ref     ObjectRef
	Timeout time.Duration // <=0 取默认 30s
}

// VerifyResult 是就绪判定结论；未就绪时附带最近事件，便于模型继续排查。
type VerifyResult struct {
	APIVersion   string         `json:"apiVersion,omitempty"`
	Kind         string         `json:"kind,omitempty"`
	Namespace    string         `json:"namespace,omitempty"`
	Name         string         `json:"name,omitempty"`
	Ready        bool           `json:"ready"`
	Reason       string         `json:"reason"`
	TimedOut     bool           `json:"timedOut,omitempty"`
	Observations map[string]any `json:"observations,omitempty"`
	Events       []string       `json:"events,omitempty"`
	Truncated    bool           `json:"truncated,omitempty"`
}
