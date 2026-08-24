// Package executor 是二期唯一允许执行写操作与 os/exec 的包
// （架构测试守护）：kubectl 以 argv 直调二进制（无 shell），其余走 client-go。
package executor

import (
	"k8s.io/client-go/kubernetes"
)

// Result 是一次命令执行的结果。
type Result struct {
	Command      string         `json:"command"`
	Stdout       string         `json:"stdout,omitempty"`
	Stderr       string         `json:"stderr,omitempty"`
	ExitCode     int            `json:"exitCode"`
	Truncated    bool           `json:"truncated,omitempty"`
	Verification map[string]any `json:"verification,omitempty"` // 写操作执行后的状态核对
}

// Options 是执行器装配参数。
type Options struct {
	// KubectlBin kubectl 二进制路径；空 = PATH 查找 "kubectl"。
	KubectlBin string
	// MaxBytes 单次命令输出上限（默认 64KB）。
	MaxBytes int
	// DenyOps 硬拦截子串（agent.deny_ops）。
	DenyOps []string
	// Clientset client-go 客户端（写操作）。
	Clientset kubernetes.Interface
	// Namespace 默认命名空间（结构化写操作缺省时使用）。
	Namespace string
}
