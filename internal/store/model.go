// Package store 提供四期平台化的持久化底座：统一 Store 接口、SQLite/MySQL
// 驱动约定与统一 migration（计划 P1.2/P0.3）。
//
// 安全约定（AGENTS.md）：任何落库/返回的模型都不应携带 Secret 内容值、
// kubeconfig 明文或 API Key 明文进入日志/审计/Session 上下文。敏感字段的
// 加密由四期 P3.1 负责，本包只负责结构化的存取边界。
package store

import (
	"time"

	"github.com/k8s-ai/k8s-ai/internal/skills"
)

// User 是产品内用户（D-05：仅作用于 Web/API，不进入 CLI）。
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName,omitempty"`
	PasswordHash string    `json:"-"`
	RoleID       string    `json:"roleId,omitempty"`
	Status       string    `json:"status,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Role 是产品内角色。Permission 以 JSON 数组存储，业务判定只依赖
// permission.NewEnforcer（见 P1.6），不散落 role == "admin"。
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Settings 是配置中心的通用键值对（P1.10）。值为 JSON 编码。
// ForceSensitive 为 true 时该值可被标记脱敏存储/脱敏读取。
type Settings struct {
	Group     string    `json:"group"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Sensitive bool      `json:"sensitive,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AuditEvent 是带全链路关联字段的结构化审计记录（P1.11 / v1.2 §19）。
// payload 先经 redactor 脱敏再落库（与 audit.Event 同源，但增加关联字段）。
//
// 四期 §19 分类字段（EventType/ResourceKind/... /Status）由写入方填充；
// Action 保留为「来源动作」（三期 audit.Action* 原始字符串），用于回放与
// EventType 反查，两者都不承载任何 Secret/kubeconfig 明文。
type AuditEvent struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"requestId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	TurnID    string    `json:"turnId,omitempty"`
	StepID    string    `json:"stepId,omitempty"`
	UserID    string    `json:"userId,omitempty"`
	ClusterID string    `json:"clusterId,omitempty"`
	Action    string    `json:"action"`
	Payload   string    `json:"payload,omitempty"`

	// EventType 是 §19 的事件分类（LOGIN/TOOL_CALL/APPROVAL/...），
	// 由 EventTypeForAction 或写入方显式设置。
	EventType string `json:"eventType,omitempty"`
	// ResourceKind/ResourceName/Operation 描述被操作对象（可空）。
	ResourceKind   string `json:"resourceKind,omitempty"`
	ResourceName   string `json:"resourceName,omitempty"`
	Operation      string `json:"operation,omitempty"`
	Risk           string `json:"risk,omitempty"`
	Decision       string `json:"decision,omitempty"`
	ApprovalID     string `json:"approvalId,omitempty"`
	GrantID        string `json:"grantId,omitempty"`
	MatchReason    string `json:"matchReason,omitempty"`
	ParameterSummary string `json:"parameterSummary,omitempty"`
	ResultSummary    string `json:"resultSummary,omitempty"`
	// Status 是结果状态：success | error | pending。
	Status string `json:"status,omitempty"`
}

// Skill 是四期 Skill 平台记录：复用三期 skills.Skill 作为不可执行的结构化
// 规格，叠加平台化运行元数据（enabled/hit 统计/校验状态）。
type Skill struct {
	Name             string       `json:"name"`
	Spec             skills.Skill `json:"spec"`
	Source           string       `json:"source"`
	Enabled          bool         `json:"enabled"`
	ValidationStatus string       `json:"validationStatus,omitempty"`
	HitCount         int64        `json:"hitCount"`
	LastHit          *time.Time   `json:"lastHit,omitempty"`
	LastValidation   *time.Time   `json:"lastValidation,omitempty"`
	CreatedAt        time.Time    `json:"createdAt"`
	UpdatedAt        time.Time    `json:"updatedAt"`
}

// Approval 是四期审批记录（P4）：记录一次写操作的审批状态与决策依据。
type Approval struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"sessionId,omitempty"`
	TurnID      string     `json:"turnId,omitempty"`
	ClusterID   string     `json:"clusterId,omitempty"`
	Tool        string     `json:"tool,omitempty"`
	Resource    string     `json:"resource,omitempty"`
	Params      string     `json:"params,omitempty"` // JSON，落库前脱敏
	RiskLevel   string     `json:"riskLevel,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Status      string     `json:"status"` // pending | approved | rejected
	Decision    string     `json:"decision,omitempty"`
	MatchReason string     `json:"matchReason,omitempty"`
	GrantID     string     `json:"grantId,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
}

// Memory 是长期记忆条目（P5.2）：按作用域（user/namespace/cluster）隔离。
type Memory struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId,omitempty"`
	Scope     string    `json:"scope"`
	Namespace string    `json:"namespace,omitempty"`
	ClusterID string    `json:"clusterId,omitempty"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Cluster 是受管集群（P3.1/D-02：单集群 UI，数据模型预留 cluster_id）。
type Cluster struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Context   string    `json:"context,omitempty"`
	Server    string    `json:"server,omitempty"`
	Version   string    `json:"version,omitempty"`
	Status    string    `json:"status,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Credential 是与集群分离的凭据记录（P3.1）。Kubeconfig 为加密存储后的值，
// 不得以明文返回或进入日志/审计/Session。
type Credential struct {
	ID               string    `json:"id"`
	ClusterID        string    `json:"clusterId,omitempty"`
	KubeconfigSecret string    `json:"-"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// RetentionPolicy 是某一类数据的保留策略（P5.4）。
type RetentionPolicy struct {
	Name          string    `json:"name"`
	Enabled       bool      `json:"enabled"`
	RetentionDays int       `json:"retentionDays,omitempty"`
	MaxCount      int64     `json:"maxCount,omitempty"`
	CleanupMode   string    `json:"cleanupMode,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
