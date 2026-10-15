package store

import (
	"context"
	"errors"

	"github.com/k8s-ai/k8s-ai/internal/session"
)

// ErrNotFound 表示记录不存在。
var ErrNotFound = errors.New("store: not found")

// ErrExists 表示记录已存在（主键/唯一约束冲突）。
var ErrExists = errors.New("store: already exists")

// Store 是四期统一的持久化门面（D-04：业务层只依赖该接口）。
//
// 实现约定：SQLite/MySQL 共用同一套逻辑迁移版本；所有写路径在进入前都应
// 对敏感字段（Secret 值 / kubeconfig / API Key）做处理，读路径按各子 Store
// 语义返回脱敏或不含敏感内容的数据。
type Store interface {
	Users() UserStore
	Roles() RoleStore
	Sessions() SessionStore
	Turns() TurnStore
	Steps() StepStore
	Audit() AuditStore
	Settings() SettingsStore
	Skills() SkillStore
	Approvals() ApprovalStore
	Memories() MemoryStore
	Retention() RetentionStore
	Clusters() ClusterStore
	Credentials() CredentialStore

	// Migrate 将 schema 升级到最新版本（幂等）。
	Migrate(ctx context.Context) error
	Close() error
}

// UserStore 管理产品内用户（P1.5）。
type UserStore interface {
	Create(ctx context.Context, u *User) error
	Get(ctx context.Context, id string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	Update(ctx context.Context, u *User) error
	List(ctx context.Context) ([]*User, error)
	Delete(ctx context.Context, id string) error
}

// RoleStore 管理产品内角色与权限（P1.5/P3.11）。
type RoleStore interface {
	Create(ctx context.Context, r *Role) error
	Get(ctx context.Context, id string) (*Role, error)
	Update(ctx context.Context, r *Role) error
	List(ctx context.Context) ([]*Role, error)
	Delete(ctx context.Context, id string) error
}

// SessionStore 持久化 Agent Session / Turn / Step（P1.7，兼容三期）。
// 复用 session.Session 作为数据模型，避免重复造类型。
type SessionStore interface {
	Create(ctx context.Context, s *session.Session) error
	Get(ctx context.Context, id string) (*session.Session, error)
	Update(ctx context.Context, s *session.Session) error
	List(ctx context.Context) ([]*session.Session, error)
	Delete(ctx context.Context, id string) error
}

// TurnStore 持久化 Agent 单轮 Turn（P1.7，多轮可回放）。
// Input/Output/Error 在落库前脱敏。
type TurnStore interface {
	Create(ctx context.Context, t *session.Turn) error
	Get(ctx context.Context, id string) (*session.Turn, error)
	Update(ctx context.Context, t *session.Turn) error
	ListBySession(ctx context.Context, sessionID string) ([]*session.Turn, error)
	// SumTokensBySession 返回会话内各轮 token 用量合计（P2.13 会话级 Token 统计）。
	SumTokensBySession(ctx context.Context, sessionID string) (session.TokenUsage, error)
	Delete(ctx context.Context, id string) error
}

// StepStore 持久化 Turn 内的 Step（P1.7），按 seq 顺序回放。
// Params/Result/Error 在落库前脱敏。
type StepStore interface {
	Create(ctx context.Context, s *session.Step) error
	Get(ctx context.Context, id string) (*session.Step, error)
	Update(ctx context.Context, s *session.Step) error
	ListByTurn(ctx context.Context, turnID string) ([]*session.Step, error)
	Delete(ctx context.Context, id string) error
}

// AuditStore 写入/查询带全链路关联的结构化审计事件（P1.11/P4.7）。
type AuditStore interface {
	Write(ctx context.Context, e *AuditEvent) error
	Get(ctx context.Context, id int64) (*AuditEvent, error)
	List(ctx context.Context) ([]*AuditEvent, error)
}

// SettingsStore 是配置中心通用键值存取（P1.10）。
type SettingsStore interface {
	Set(ctx context.Context, s *Settings) error
	Get(ctx context.Context, group, key string) (*Settings, error)
	ListByGroup(ctx context.Context, group string) ([]*Settings, error)
	Delete(ctx context.Context, group, key string) error
}

// SkillStore 管理内置/自定义/第三方 Skill 元数据（P3.5）。
type SkillStore interface {
	Upsert(ctx context.Context, s *Skill) error
	Get(ctx context.Context, name string) (*Skill, error)
	List(ctx context.Context) ([]*Skill, error)
	Delete(ctx context.Context, name string) error
}

// ApprovalStore 记录审批状态与决策依据（P4.1/P4.3/P4.4）。
type ApprovalStore interface {
	Create(ctx context.Context, a *Approval) error
	Get(ctx context.Context, id string) (*Approval, error)
	Update(ctx context.Context, a *Approval) error
	List(ctx context.Context) ([]*Approval, error)
	Delete(ctx context.Context, id string) error
}

// MemoryStore 存取按作用域隔离的长期记忆（P5.2/P5.3）。
type MemoryStore interface {
	Create(ctx context.Context, m *Memory) error
	Get(ctx context.Context, id string) (*Memory, error)
	Update(ctx context.Context, m *Memory) error
	List(ctx context.Context) ([]*Memory, error)
	Delete(ctx context.Context, id string) error
}

// RetentionStore 管理各数据类的保留/归档策略（P5.4）。
type RetentionStore interface {
	Upsert(ctx context.Context, p *RetentionPolicy) error
	Get(ctx context.Context, name string) (*RetentionPolicy, error)
	List(ctx context.Context) ([]*RetentionPolicy, error)
	Delete(ctx context.Context, name string) error
}

// ClusterStore 管理受管集群（P3.1-P3.3）。
type ClusterStore interface {
	Create(ctx context.Context, c *Cluster) error
	Get(ctx context.Context, id string) (*Cluster, error)
	Update(ctx context.Context, c *Cluster) error
	List(ctx context.Context) ([]*Cluster, error)
	Delete(ctx context.Context, id string) error
}

// CredentialStore 管理与集群分离的凭据（P3.1，加密存储）。
type CredentialStore interface {
	Create(ctx context.Context, c *Credential) error
	Get(ctx context.Context, id string) (*Credential, error)
	GetByCluster(ctx context.Context, clusterID string) (*Credential, error)
	Update(ctx context.Context, c *Credential) error
	Delete(ctx context.Context, id string) error
}

// compile-time interface checks
var (
	_ UserStore       = (*userStore)(nil)
	_ RoleStore       = (*roleStore)(nil)
	_ SessionStore    = (*sessionStore)(nil)
	_ TurnStore       = (*turnStore)(nil)
	_ StepStore       = (*stepStore)(nil)
	_ AuditStore      = (*auditStore)(nil)
	_ SettingsStore   = (*settingsStore)(nil)
	_ SkillStore      = (*skillStore)(nil)
	_ ApprovalStore   = (*approvalStore)(nil)
	_ MemoryStore     = (*memoryStore)(nil)
	_ RetentionStore  = (*retentionStore)(nil)
	_ ClusterStore    = (*clusterStore)(nil)
	_ CredentialStore = (*credentialStore)(nil)
	_ Store           = (*dbStore)(nil)
)
