package executor

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Runner 同时实现 kubectl 执行与 client-go 结构化写操作。
type Runner struct {
	bin           string
	kubeconfig    string
	maxBytes      int
	denyOps       []string
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
	return r
}

// ExecuteCommand 通用入口（kubectl argv）。
func (r *Runner) ExecuteCommand(ctx context.Context, command string) (Result, error) {
	return r.RunKubectl(ctx, command)
}
