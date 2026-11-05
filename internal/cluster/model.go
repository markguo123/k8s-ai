// Package cluster 提供四期平台化的集群管理能力（P3.1-P3.3）：
// 受管集群元数据、与集群分离的加密凭据、kubeconfig 解析/校验/连接测试与状态刷新。
//
// 安全约定（AGENTS.md LLM Safety）：kubeconfig 明文只在本包内临时出现；落库前
// 必须经 Cipher 加密，读接口永不回文明文，凭据值不进入 LLM/Session/Audit/日志。
package cluster

import (
	"errors"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 服务层统一错误（供 API Handler 做状态码映射）。
var (
	ErrInvalid  = errors.New("cluster: invalid request")
	ErrExists   = errors.New("cluster: already exists")
	ErrNotFound = errors.New("cluster: not found")
)

// Status 是受管集群的连接状态。
type Status string

const (
	StatusUnknown      Status = "unknown"
	StatusConnected    Status = "connected"
	StatusDisconnected Status = "disconnected"
	StatusError        Status = "error"
)

// Info 是一次 kubeconfig 解析/连测得到的集群信息（P3.2/P3.3）。
type Info struct {
	Name    string `json:"name,omitempty"`
	Context string `json:"context,omitempty"`
	Server  string `json:"server,omitempty"`
	Version string `json:"version,omitempty"`
	Health  string `json:"health,omitempty"`
}

// CreateRequest 是 POST /api/v1/clusters 的请求体。
type CreateRequest struct {
	Name       string `json:"name,omitempty"`
	Kubeconfig string `json:"kubeconfig"`
}

// TestRequest 是 POST /api/v1/clusters/test 的请求体。
type TestRequest struct {
	Kubeconfig string `json:"kubeconfig"`
}

// StatusView 是 GET /api/v1/clusters/current 的响应载体。
type StatusView struct {
	Cluster *store.Cluster `json:"cluster"`
	Info    Info           `json:"info"`
}

