package executor

import (
	"context"
	"time"

	"k8s.io/client-go/kubernetes"
)

// Runner 同时实现 kubectl 执行与 client-go 结构化写操作。
type Runner struct {
	bin           string
	maxBytes      int
	denyOps       []string
	cs            kubernetes.Interface
	ns            string
	streamTimeout time.Duration
}

// NewRunner 创建执行器。
func NewRunner(opts Options) *Runner {
	r := &Runner{
		bin:           opts.KubectlBin,
		maxBytes:      opts.MaxBytes,
		denyOps:       opts.DenyOps,
		cs:            opts.Clientset,
		ns:            opts.Namespace,
		streamTimeout: opts.StreamTimeout,
	}
	if r.bin == "" {
		r.bin = "kubectl"
	}
	if r.maxBytes <= 0 {
		r.maxBytes = 64 * 1024
	}
	return r
}

// ExecuteCommand 通用入口（kubectl argv）。
func (r *Runner) ExecuteCommand(ctx context.Context, command string) (Result, error) {
	return r.RunKubectl(ctx, command)
}
