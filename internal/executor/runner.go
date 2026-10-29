package executor

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// shellBinary 是 shell 通道使用的解释器。用常量而不是字面量，让启动探测与执行点
// 保持同一个来源；tests/arch 靠 exec 调用里的 "-c" 字面量识别 shell 执行点，
// 换成常量不会削弱那条红线。
const shellBinary = "bash"

// Runner 同时实现 kubectl 执行与 client-go 结构化写操作。
type Runner struct {
	bin           string
	kubeconfig    string
	maxBytes      int
	denyOps       []string
	shellEnabled  bool
	cs            kubernetes.Interface
	ns            string
	streamTimeout time.Duration

	// 三期创建通道（R2）：dyn/mapper 可直接注入（测试），否则由 restConfig 惰性构造。
	restConfig   *rest.Config
	fieldManager string
	dyn          dynamic.Interface
	mapper       meta.RESTMapper

	// 写通道的装配结果必须跨多次调用一致：sync.Once 只保护首次装配，
	// 之后的调用要复用同一结论（否则 Once 之后会返回 nil 通道）。
	channelOnce  sync.Once
	channelValue *writeChannel
	channelErr   error
}

// NewRunner 创建执行器。
func NewRunner(opts Options) *Runner {
	r := &Runner{
		bin:           opts.KubectlBin,
		kubeconfig:    opts.Kubeconfig,
		maxBytes:      opts.MaxBytes,
		denyOps:       opts.DenyOps,
		shellEnabled:  opts.ShellEnabled,
		cs:            opts.Clientset,
		ns:            opts.Namespace,
		streamTimeout: opts.StreamTimeout,
		restConfig:    opts.RESTConfig,
		fieldManager:  opts.FieldManager,
		dyn:           opts.Dynamic,
		mapper:        opts.Mapper,
	}
	if r.bin == "" {
		r.bin = "kubectl"
	}
	if r.maxBytes <= 0 {
		r.maxBytes = 64 * 1024
	}
	if r.fieldManager == "" {
		r.fieldManager = FieldManager
	}
	// 启动探测（P6.3）：只打日志、不阻塞启动。读链路走 client-go，不依赖
	// bash/kubectl，缺它们不该让服务起不来。
	status := r.probeToolchain()
	toolchainLogOnce.Do(func() { logToolchain(status) })
	return r
}

// ToolchainStatus 是执行通道外部依赖的就绪情况（启动期可观测性用）。
type ToolchainStatus struct {
	KubectlPath  string // 解析到的 kubectl 路径；未找到时是配置里的原值
	KubectlFound bool
	ShellPath    string // 解析到的 shell 路径
	ShellFound   bool
	ShellEnabled bool
}

// probeToolchain 探测 kubectl 与 shell 是否就位。
func (r *Runner) probeToolchain() ToolchainStatus {
	st := ToolchainStatus{KubectlPath: r.bin, ShellEnabled: r.shellEnabled}
	if path, err := exec.LookPath(r.bin); err == nil {
		st.KubectlPath, st.KubectlFound = path, true
	}
	if path, err := exec.LookPath(shellBinary); err == nil {
		st.ShellPath, st.ShellFound = path, true
	}
	return st
}

// ToolchainStatus 返回当前执行器的工具链探测结果（供启动自检与冒烟核对）。
func (r *Runner) ToolchainStatus() ToolchainStatus {
	return r.probeToolchain()
}

// toolchainLogOnce 保证一次进程生命周期里只播报一次工具链结论：服务只在启动时
// 装配一个 Runner，但 NewRunner 也会被测试与工具反复调用，不去重的话会刷屏。
var toolchainLogOnce sync.Once

// logToolchain 按档位播报执行通道状态：开启态缺工具是 WARN（现场要修的），
// 关闭态缺工具是 INFO（无 shell 镜像变体的预期形态）。
func logToolchain(st ToolchainStatus) {
	if !st.ShellEnabled {
		slog.Info("executor: shell 通道已关闭（platform.exec.shell_enabled=false），含管道构造的命令将被策略级拒绝",
			"kubectl", st.KubectlPath, "kubectl_found", st.KubectlFound)
		return
	}
	if !st.KubectlFound || !st.ShellFound {
		slog.Warn("executor: 执行通道工具不完整，kubectl 命令会在执行前失败、含 shell 构造的命令无法执行",
			"kubectl", st.KubectlPath, "kubectl_found", st.KubectlFound,
			"shell", shellBinary, "shell_found", st.ShellFound)
		return
	}
	slog.Info("executor: 执行通道就绪", "kubectl", st.KubectlPath, "shell", st.ShellPath)
}

// ExecuteCommand 通用入口（kubectl argv）。
func (r *Runner) ExecuteCommand(ctx context.Context, command string) (Result, error) {
	return r.RunKubectl(ctx, command)
}
