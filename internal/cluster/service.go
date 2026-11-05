package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// Service 是集群管理应用服务（P3.1-P3.3）：接收 kubeconfig → 解析校验 → 连测 →
// 加密落库 → 状态刷新。kubeconfig 明文与凭据值不进入 LLM/Session/Audit/日志。
type Service struct {
	clusters    store.ClusterStore
	credentials store.CredentialStore
	cipher      *Cipher
	connector   Connector
	audit       func(ctx context.Context, e store.AuditEvent) error
	now         func() time.Time
}

// Option 是 Service 的可选依赖注入（测试用）。
type Option func(*Service)

// WithConnector 注入连接探测实现（默认 RemoteConnector）。
func WithConnector(c Connector) Option {
	return func(s *Service) { s.connector = c }
}

// WithAudit 注入系统事件写入口（service.AuditService.Record 满足）。
func WithAudit(fn func(ctx context.Context, e store.AuditEvent) error) Option {
	return func(s *Service) { s.audit = fn }
}

// WithClock 注入时钟（测试用）。
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// New 创建集群管理服务。
func New(clusters store.ClusterStore, credentials store.CredentialStore, cipher *Cipher, opts ...Option) *Service {
	s := &Service{
		clusters:    clusters,
		credentials: credentials,
		cipher:      cipher,
		connector:   &RemoteConnector{Timeout: 30 * time.Second},
		now:         time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Create 校验并连测 kubeconfig，加密落库后登记集群元数据（P3.2 POST /clusters）。
// 未提供名称时使用 kubeconfig 当前 context 作为展示名。
func (s *Service) Create(ctx context.Context, req CreateRequest) (*store.Cluster, error) {
	if strings.TrimSpace(req.Kubeconfig) == "" {
		return nil, fmt.Errorf("%w: kubeconfig is required", ErrInvalid)
	}
	if s.cipher == nil {
		return nil, fmt.Errorf("%w: encryption not configured", ErrInvalid)
	}
	info, err := s.connector.Inspect(ctx, []byte(req.Kubeconfig))
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(info.Name)
	}
	if name == "" {
		name = info.Context
	}
	if err := s.ensureUniqueName(ctx, name); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	c := &store.Cluster{
		ID:        newID(),
		Name:      name,
		Context:   info.Context,
		Server:    info.Server,
		Version:   info.Version,
		Status:    string(StatusConnected),
		CreatedAt: now,
		UpdatedAt: now,
	}
	secret, err := s.cipher.Encrypt([]byte(req.Kubeconfig))
	if err != nil {
		return nil, err
	}
	cred := &store.Credential{
		ID:               newID(),
		ClusterID:        c.ID,
		KubeconfigSecret: secret,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	// 两个实体非同一事务写入：任一步失败都回滚已写记录，保持一致性。
	if err := s.clusters.Create(ctx, c); err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, fmt.Errorf("%w: cluster %q already exists", ErrExists, name)
		}
		return nil, fmt.Errorf("%w: create cluster: %v", ErrInvalid, err)
	}
	if err := s.credentials.Create(ctx, cred); err != nil {
		_ = s.clusters.Delete(ctx, c.ID)
		return nil, fmt.Errorf("%w: persist credential: %v", ErrInvalid, err)
	}
	s.recordChange(ctx, "create", c, "")
	return c, nil
}

// ensureUniqueName 保证受管集群展示名唯一（schema 未对 name 建唯一约束，
// 冲突语义在服务层兜底，避免同名集群混淆连接目标）。
func (s *Service) ensureUniqueName(ctx context.Context, name string) error {
	existing, err := s.clusters.List(ctx)
	if err != nil {
		return fmt.Errorf("cluster: list clusters for uniqueness: %w", err)
	}
	for _, c := range existing {
		if strings.EqualFold(strings.TrimSpace(c.Name), name) {
			return fmt.Errorf("%w: cluster %q already exists", ErrExists, name)
		}
	}
	return nil
}

// Test 仅解析 + 连测，不做任何落库（P3.2 POST /clusters/test）。
func (s *Service) Test(ctx context.Context, req TestRequest) (*Info, error) {
	if strings.TrimSpace(req.Kubeconfig) == "" {
		return nil, fmt.Errorf("%w: kubeconfig is required", ErrInvalid)
	}
	return s.connector.Inspect(ctx, []byte(req.Kubeconfig))
}

// List 返回受管集群元数据（不含凭据）。
func (s *Service) List(ctx context.Context) ([]*store.Cluster, error) {
	return s.clusters.List(ctx)
}

// Get 返回单个受管集群元数据（不含凭据）。
func (s *Service) Get(ctx context.Context, id string) (*store.Cluster, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: cluster id is required", ErrInvalid)
	}
	c, err := s.clusters.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: cluster %q not found", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("cluster: get cluster: %w", err)
	}
	return c, nil
}

// Current 返回"当前集群"的实时状态（P3.3 GET /clusters/current 无 id 形态）。
// 单集群 UI：取最近创建的受管集群刷新并返回；无任何集群时返回 ErrNotFound。
func (s *Service) Current(ctx context.Context) (*StatusView, error) {
	clusters, err := s.clusters.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster: list clusters: %w", err)
	}
	if len(clusters) == 0 {
		return nil, ErrNotFound
	}
	pick := clusters[0]
	for _, c := range clusters[1:] {
		if c.CreatedAt.After(pick.CreatedAt) {
			pick = c
		}
	}
	return s.Refresh(ctx, pick.ID)
}

// Refresh 用加密凭据解密后重连一次，刷新版本与连接状态（P3.3 GET /clusters/{id}）。
// 连测失败不视为内部错误：状态置为 disconnected/error 并带原因返回。
func (s *Service) Refresh(ctx context.Context, id string) (*StatusView, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	cred, err := s.credentials.GetByCluster(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("%w: cluster %q has no credential", ErrNotFound, id)
		}
		return nil, fmt.Errorf("cluster: load credential: %w", err)
	}
	plain, err := s.decrypt(cred)
	if err != nil {
		c.Status = string(StatusError)
		_ = s.clusters.Update(ctx, c)
		return nil, err
	}
	info, err := s.connector.Inspect(ctx, plain)
	if err != nil {
		c.Status = string(StatusDisconnected)
		_ = s.clusters.Update(ctx, c)
		return &StatusView{Cluster: c, Info: Info{Context: c.Context, Server: c.Server, Health: "down"}}, nil
	}
	c.Version = info.Version
	c.Status = string(StatusConnected)
	c.UpdatedAt = s.now().UTC()
	if err := s.clusters.Update(ctx, c); err != nil {
		return nil, fmt.Errorf("cluster: update status: %w", err)
	}
	return &StatusView{Cluster: c, Info: *info}, nil
}

// Delete 删除集群元数据与加密凭据，并写 cluster.change 审计（P1.11 补齐）。
func (s *Service) Delete(ctx context.Context, id string) error {
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	// CredentialStore.Delete 按凭据主键删除：先按 clusterID 取出再删。
	if cred, err := s.credentials.GetByCluster(ctx, id); err == nil {
		if err := s.credentials.Delete(ctx, cred.ID); err != nil {
			return fmt.Errorf("cluster: delete credential: %w", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("cluster: load credential: %w", err)
	}
	if err := s.clusters.Delete(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("cluster: delete cluster: %w", err)
	}
	s.recordChange(ctx, "delete", c, "")
	return nil
}

func (s *Service) decrypt(cred *store.Credential) ([]byte, error) {
	if s.cipher == nil {
		return nil, fmt.Errorf("%w: encryption not configured", ErrInvalid)
	}
	return s.cipher.Decrypt(cred.KubeconfigSecret)
}

// recordChange 写 CLUSTER_CHANGE 审计：只含集群身份与操作，不含任何凭据内容。
func (s *Service) recordChange(ctx context.Context, operation string, c *store.Cluster, message string) {
	if s.audit == nil {
		return
	}
	var summary string
	if message != "" {
		summary = message
	} else {
		summary = fmt.Sprintf(`{"name":%q,"context":%q,"server":%q}`, c.Name, c.Context, c.Server)
	}
	_ = s.audit(ctx, store.AuditEvent{
		Action:        store.ActionClusterChange,
		EventType:     store.AuditEventClusterChange,
		ClusterID:     c.ID,
		Operation:     operation,
		ResourceKind:  "cluster",
		ResourceName:  c.Name,
		ResultSummary: summary,
		Status:        "success",
	})
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cluster-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
