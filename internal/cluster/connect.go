package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Connector 解析 kubeconfig 并验证可连接性（P3.2；测试隔离点）。
type Connector interface {
	Inspect(ctx context.Context, kubeconfig []byte) (*Info, error)
}

// RemoteConnector 连接真实集群的默认 Connector。
type RemoteConnector struct {
	// Timeout 覆盖单次连测的 rest 超时；零值用 30s。
	Timeout time.Duration
}

// Inspect 先解析 kubeconfig（结构/上下文/集群），再实际连接一次拿服务器版本与就绪状态。
// 任何解析或连接失败都返回带 ErrInvalid 包装的明确错误，供 CREATE/TEST 接口使用。
func (rc *RemoteConnector) Inspect(ctx context.Context, data []byte) (*Info, error) {
	info, err := ParseKubeconfig(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	cfg, err := buildRESTConfig(data, rc.Timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: build rest config: %v", ErrInvalid, err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: create clientset: %v", ErrInvalid, err)
	}
	version, err := cs.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("%w: connect failed: %v", ErrInvalid, err)
	}
	info.Version = version.GitVersion
	info.Health = probeReadyz(ctx, cs.Discovery().RESTClient(), rc.Timeout)
	return info, nil
}

// ParseKubeconfig 校验 kubeconfig 结构并提取当前 context/cluster server（不含凭据）。
// 与内网只读门面共用 clientcmd：同一解析口径，错误路径可读。
func ParseKubeconfig(data []byte) (*Info, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("kubeconfig is empty")
	}
	cfg, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	ctxName := cfg.CurrentContext
	if ctxName == "" {
		for name := range cfg.Contexts {
			ctxName = name
			break
		}
	}
	if ctxName == "" {
		return nil, errors.New("kubeconfig has no context")
	}
	kc := cfg.Contexts[ctxName]
	if kc == nil {
		return nil, fmt.Errorf("kubeconfig context %q not found", ctxName)
	}
	cc := cfg.Clusters[kc.Cluster]
	if cc == nil {
		return nil, fmt.Errorf("kubeconfig context %q has no cluster", ctxName)
	}
	if cc.Server == "" {
		return nil, fmt.Errorf("kubeconfig cluster %q has no server", kc.Cluster)
	}
	return &Info{Name: ctxName, Context: ctxName, Server: cc.Server}, nil
}

func buildRESTConfig(data []byte, timeout time.Duration) (*rest.Config, error) {
	cfg, err := clientcmd.NewClientConfigFromBytes(data)
	if err != nil {
		return nil, err
	}
	rc, err := cfg.ClientConfig()
	if err != nil {
		return nil, err
	}
	if timeout > 0 {
		rc.Timeout = timeout
	}
	return rc, nil
}

func probeReadyz(ctx context.Context, rc rest.Interface, timeout time.Duration) string {
	req := rc.Get().AbsPath("/readyz")
	if timeout > 0 {
		req = req.Timeout(timeout)
	}
	result := req.Do(ctx)
	var code int
	result = result.StatusCode(&code)
	if result.Error() != nil {
		return "unknown"
	}
	if code == http.StatusOK {
		return "ok"
	}
	return "unknown"
}
