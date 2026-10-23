// Package authz 提供四期企业级授权面（P3.11 / P4.6 基础）的应用服务：
// AuthzGrant（namespace 授权与销毁能力委派）与 ApiToken（产品内持久化
// API Token）管理。只依赖 store.Store 接口；token 明文/哈希只写入、只在
// 创建时返回一次，永不回读。
package authz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 领域错误哨兵（映射到 server 层统一错误信封）。
var (
	ErrInvalid       = errors.New("authz: invalid input")
	ErrGrantNotFound = errors.New("authz: grant not found")
	ErrTokenNotFound = errors.New("authz: api token not found")
	ErrUserNotFound  = errors.New("authz: user not found")
)

// ApiToken 生命周期状态（P3.11）。
const (
	StatusActive   = "active"
	StatusRevoked  = "revoked"
	StatusDisabled = "disabled"
)

// 授权动作枚举（对齐方案 §7 与 D-17）：
//   - "*"      namespace 内全量操作（销毁类除外，需单独委派）
//   - "destroy" 销毁类写操作（delete namespace / pvc / pv）
var grantVerbs = map[string]bool{"*": true, "destroy": true}

// principalType 白名单。
var grantPrincipalTypes = map[string]bool{"user": true, "group": true}

var tokenStatuses = map[string]bool{
	StatusActive: true, StatusRevoked: true, StatusDisabled: true,
}

// Grant 是授权委派的 API 视图：namespaces / permissions 以 []string 表达。
type Grant struct {
	ID            string     `json:"id"`
	PrincipalType string     `json:"principalType"`
	PrincipalID   string     `json:"principalId"`
	ClusterID     string     `json:"clusterId,omitempty"`
	Namespaces    []string   `json:"namespaces"`
	Permissions   []string   `json:"permissions"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	CreatedBy     string     `json:"createdBy,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Token 是 API Token 的 API 视图（TokenHash 永不回显，仅 createdAt 等元数据）。
type Token struct {
	ID         string     `json:"id"`
	UserID     string     `json:"userId"`
	Name       string     `json:"name,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	Status     string     `json:"status,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// CreateGrantRequest POST /api/v1/grants 请求体。
type CreateGrantRequest struct {
	PrincipalType string     `json:"principalType"`
	PrincipalID   string     `json:"principalId"`
	ClusterID     string     `json:"clusterId,omitempty"`
	Namespaces    []string   `json:"namespaces"`
	Permissions   []string   `json:"permissions"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
}

// UpdateGrantRequest PATCH /api/v1/grants/{id} 请求体（部分更新；ExpiresAt 为空表示保持原值）。
type UpdateGrantRequest struct {
	ClusterID   *string    `json:"clusterId"`
	Namespaces  *[]string  `json:"namespaces"`
	Permissions *[]string  `json:"permissions"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

// CreateTokenRequest POST /api/v1/users/{userId}/tokens 请求体。
type CreateTokenRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// CreateTokenResult 创建 token 的响应：明文 token 仅在此时返回一次。
type CreateTokenResult struct {
	APIToken *Token `json:"apiToken"`
	Token    string `json:"token"` // 明文 token，创建后服务端不再持有
}

// EffectiveScope 是登录用户经平台授权后的有效 Kubernetes 资源范围。
type EffectiveScope struct {
	ClusterID     string   `json:"clusterId,omitempty"`
	Namespaces    []string `json:"namespaces"`
	ClusterScoped bool     `json:"clusterScoped"`
	IsAdmin       bool     `json:"isAdmin"`
	CanWrite      bool     `json:"canWrite"`
	CanDestroy    bool     `json:"canDestroy"`
}

// Service 是授权面的应用服务。
type Service struct {
	st store.Store
}

// New 创建授权应用服务（依赖四期 Store）。
func New(st store.Store) *Service {
	return &Service{st: st}
}

// EffectiveScope 合并用户当前有效的 namespace grants。空 clusterId 的授权按
// 单集群语义匹配当前集群；user ID 与 username 都可作为 principalId 匹配。
func (s *Service) EffectiveScope(ctx context.Context, user *store.User, role *store.Role, clusterID string) (*EffectiveScope, error) {
	if user == nil || role == nil {
		return nil, fmt.Errorf("%w: user and role are required", ErrInvalid)
	}
	if permission.HasPermission(role.Permissions, "*") {
		return &EffectiveScope{
			ClusterID: clusterID, Namespaces: []string{}, ClusterScoped: true, IsAdmin: true, CanWrite: true, CanDestroy: true,
		}, nil
	}
	principals := []string{strings.TrimSpace(user.ID)}
	if username := strings.TrimSpace(user.Username); username != "" && username != user.ID {
		principals = append(principals, username)
	}
	now := time.Now().UTC()
	seenNS := map[string]bool{}
	namespaces := []string{}
	canWrite, canDestroy := false, false
	for _, principal := range principals {
		grants, err := s.st.AuthzGrants().ListByPrincipal(ctx, "user", principal)
		if err != nil {
			return nil, fmt.Errorf("authz: list grants for %s: %w", user.Username, err)
		}
		for _, raw := range grants {
			if raw.ExpiresAt != nil && !raw.ExpiresAt.After(now) {
				continue
			}
			if raw.ClusterID != "" && clusterID != "" && raw.ClusterID != clusterID {
				continue
			}
			permissions, err := unmarshalStringSlice(raw.Permissions)
			if err != nil {
				return nil, fmt.Errorf("authz: decode grant %s: %w", raw.ID, err)
			}
			if !containsString(permissions, "*") {
				if containsString(permissions, "destroy") {
					canDestroy = true
				}
				continue
			}
			namespacesRaw, err := unmarshalStringSlice(raw.Namespaces)
			if err != nil {
				return nil, fmt.Errorf("authz: decode grant %s namespaces: %w", raw.ID, err)
			}
			canWrite = true
			if containsString(permissions, "destroy") {
				canDestroy = true
			}
			for _, ns := range namespacesRaw {
				ns = strings.TrimSpace(ns)
				if ns != "" && !seenNS[ns] {
					seenNS[ns] = true
					namespaces = append(namespaces, ns)
				}
			}
		}
	}
	sort.Strings(namespaces)
	return &EffectiveScope{
		ClusterID: clusterID, Namespaces: namespaces, CanWrite: canWrite, CanDestroy: canDestroy,
	}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ---- AuthzGrant ----

// ListGrants 返回全部授权委派（含已过期项，由前端按需展示/过滤）。
func (s *Service) ListGrants(ctx context.Context) ([]*Grant, error) {
	raw, err := s.st.AuthzGrants().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Grant, 0, len(raw))
	for _, g := range raw {
		v, err := toGrantView(g)
		if err != nil {
			return nil, fmt.Errorf("authz: decode grant %s: %w", g.ID, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// GetGrant 返回单条授权委派。
func (s *Service) GetGrant(ctx context.Context, id string) (*Grant, error) {
	g, err := s.st.AuthzGrants().Get(ctx, id)
	if err != nil {
		return nil, mapGetErr(err, ErrGrantNotFound, "grant")
	}
	return toGrantView(g)
}

// CreateGrant 创建一条 namespace 授权/销毁委派，createdBy 记录操作者。
func (s *Service) CreateGrant(ctx context.Context, req CreateGrantRequest, createdBy string) (*Grant, error) {
	if err := validateGrant(req.PrincipalType, req.PrincipalID, req.Namespaces, req.Permissions, req.ExpiresAt); err != nil {
		return nil, err
	}
	namespaces, err := marshalStringSlice(req.Namespaces)
	if err != nil {
		return nil, err
	}
	permissions, err := marshalStringSlice(req.Permissions)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	g := &store.AuthzGrant{
		ID:            newID(),
		PrincipalType: strings.ToLower(strings.TrimSpace(req.PrincipalType)),
		PrincipalID:   strings.TrimSpace(req.PrincipalID),
		ClusterID:     strings.TrimSpace(req.ClusterID),
		Namespaces:    namespaces,
		Permissions:   permissions,
		ExpiresAt:     cloneTime(req.ExpiresAt),
		CreatedBy:     createdBy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.st.AuthzGrants().Create(ctx, g); err != nil {
		return nil, fmt.Errorf("authz: create grant: %w", err)
	}
	return toGrantView(g)
}

// UpdateGrant 部分更新授权委派（不可变更 principalType/principalId）。
func (s *Service) UpdateGrant(ctx context.Context, id string, req UpdateGrantRequest) (*Grant, error) {
	g, err := s.st.AuthzGrants().Get(ctx, id)
	if err != nil {
		return nil, mapGetErr(err, ErrGrantNotFound, "grant")
	}
	namespaces, err := unmarshalStringSlice(g.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("authz: decode grant %s: %w", id, err)
	}
	permissions, err := unmarshalStringSlice(g.Permissions)
	if err != nil {
		return nil, fmt.Errorf("authz: decode grant %s: %w", id, err)
	}
	clusterID := g.ClusterID
	if req.ClusterID != nil {
		clusterID = strings.TrimSpace(*req.ClusterID)
	}
	if req.Namespaces != nil {
		namespaces = *req.Namespaces
	}
	if req.Permissions != nil {
		permissions = *req.Permissions
	}
	expires := g.ExpiresAt
	if req.ExpiresAt != nil {
		expires = cloneTime(req.ExpiresAt)
	}
	if err := validateGrant(g.PrincipalType, g.PrincipalID, namespaces, permissions, expires); err != nil {
		return nil, err
	}
	if ns, err := marshalStringSlice(namespaces); err != nil {
		return nil, err
	} else {
		g.Namespaces = ns
	}
	if ps, err := marshalStringSlice(permissions); err != nil {
		return nil, err
	} else {
		g.Permissions = ps
	}
	g.ClusterID = clusterID
	g.ExpiresAt = expires
	if err := s.st.AuthzGrants().Update(ctx, g); err != nil {
		return nil, fmt.Errorf("authz: update grant: %w", err)
	}
	return toGrantView(g)
}

// DeleteGrant 撤销一条授权委派（立即生效，集群侧 RBAC 回收属 P4.6）。
func (s *Service) DeleteGrant(ctx context.Context, id string) error {
	if err := s.st.AuthzGrants().Delete(ctx, id); err != nil {
		return mapGetErr(err, ErrGrantNotFound, "grant")
	}
	return nil
}

// ---- ApiToken ----

// ListTokens 返回某用户的全部 API Token 元数据（无 TokenHash）。
func (s *Service) ListTokens(ctx context.Context, userID string) ([]*Token, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return nil, err
	}
	raw, err := s.st.ApiTokens().ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*Token, 0, len(raw))
	for _, t := range raw {
		out = append(out, toTokenView(t))
	}
	return out, nil
}

// CreateToken 为用户签发 API Token；明文仅此一次返回，服务端仅存 SHA-256。
func (s *Service) CreateToken(ctx context.Context, userID string, req CreateTokenRequest) (*Token, string, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return nil, "", err
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 64 {
		return nil, "", fmt.Errorf("%w: token name too long (max 64)", ErrInvalid)
	}
	if req.ExpiresAt != nil && time.Now().After(*req.ExpiresAt) {
		return nil, "", fmt.Errorf("%w: expiresAt must be in the future", ErrInvalid)
	}
	plain := newSecretToken()
	sum := sha256.Sum256([]byte(plain))
	now := time.Now().UTC()
	t := &store.ApiToken{
		ID:        newID(),
		UserID:    userID,
		Name:      name,
		TokenHash: hex.EncodeToString(sum[:]),
		ExpiresAt: cloneTime(req.ExpiresAt),
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.st.ApiTokens().Create(ctx, t); err != nil {
		return nil, "", fmt.Errorf("authz: create api token: %w", err)
	}
	return toTokenView(t), plain, nil
}

// UpdateTokenStatus 更新 token 生命周期状态（active / revoked / disabled）。
func (s *Service) UpdateTokenStatus(ctx context.Context, userID, id, status string) (*Token, error) {
	if err := s.requireUser(ctx, userID); err != nil {
		return nil, err
	}
	if !tokenStatuses[status] {
		return nil, fmt.Errorf("%w: invalid token status %q", ErrInvalid, status)
	}
	t, err := s.st.ApiTokens().Get(ctx, id)
	if err != nil {
		return nil, mapGetErr(err, ErrTokenNotFound, "api token")
	}
	if t.UserID != userID {
		return nil, ErrTokenNotFound
	}
	t.Status = status
	if err := s.st.ApiTokens().Update(ctx, t); err != nil {
		return nil, fmt.Errorf("authz: update api token: %w", err)
	}
	return toTokenView(t), nil
}

// DeleteToken 撤销并删除指定 token（仅限该用户名下）。
func (s *Service) DeleteToken(ctx context.Context, userID, id string) error {
	if err := s.requireUser(ctx, userID); err != nil {
		return err
	}
	t, err := s.st.ApiTokens().Get(ctx, id)
	if err != nil {
		return mapGetErr(err, ErrTokenNotFound, "api token")
	}
	if t.UserID != userID {
		return ErrTokenNotFound
	}
	if err := s.st.ApiTokens().Delete(ctx, id); err != nil {
		return fmt.Errorf("authz: delete api token: %w", err)
	}
	return nil
}

// requireUser 校验被管理用户存在，避免产生悬挂外键。
func (s *Service) requireUser(ctx context.Context, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return ErrUserNotFound
	}
	if _, err := s.st.Users().Get(ctx, userID); err != nil {
		return ErrUserNotFound
	}
	return nil
}

func validateGrant(principalType, principalID string, namespaces, permissions []string, expires *time.Time) error {
	pt := strings.ToLower(strings.TrimSpace(principalType))
	if !grantPrincipalTypes[pt] {
		return fmt.Errorf("%w: principalType must be user or group", ErrInvalid)
	}
	if strings.TrimSpace(principalID) == "" {
		return fmt.Errorf("%w: principalId is required", ErrInvalid)
	}
	if len(namespaces) == 0 {
		return fmt.Errorf("%w: namespaces is required", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, ns := range namespaces {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			return fmt.Errorf("%w: namespace must not be empty", ErrInvalid)
		}
		if seen[ns] {
			return fmt.Errorf("%w: duplicate namespace %q", ErrInvalid, ns)
		}
		seen[ns] = true
	}
	if len(permissions) == 0 {
		return fmt.Errorf("%w: permissions is required", ErrInvalid)
	}
	for _, p := range permissions {
		if !grantVerbs[p] {
			return fmt.Errorf("%w: invalid permission %q (allowed: *, destroy)", ErrInvalid, p)
		}
	}
	if expires != nil && time.Now().After(*expires) {
		return fmt.Errorf("%w: expiresAt must be in the future", ErrInvalid)
	}
	return nil
}

// mapGetErr 把 store 的 not found 错误映射为领域哨兵。
func mapGetErr(err error, notFound error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return notFound
	}
	return fmt.Errorf("authz: read %s: %w", what, err)
}

func toGrantView(g *store.AuthzGrant) (*Grant, error) {
	namespaces, err := unmarshalStringSlice(g.Namespaces)
	if err != nil {
		return nil, fmt.Errorf("authz: decode namespaces: %w", err)
	}
	permissions, err := unmarshalStringSlice(g.Permissions)
	if err != nil {
		return nil, fmt.Errorf("authz: decode permissions: %w", err)
	}
	return &Grant{
		ID:            g.ID,
		PrincipalType: g.PrincipalType,
		PrincipalID:   g.PrincipalID,
		ClusterID:     g.ClusterID,
		Namespaces:    namespaces,
		Permissions:   permissions,
		ExpiresAt:     cloneTime(g.ExpiresAt),
		CreatedBy:     g.CreatedBy,
		CreatedAt:     g.CreatedAt,
		UpdatedAt:     g.UpdatedAt,
	}, nil
}

func toTokenView(t *store.ApiToken) *Token {
	return &Token{
		ID:         t.ID,
		UserID:     t.UserID,
		Name:       t.Name,
		ExpiresAt:  cloneTime(t.ExpiresAt),
		Status:     t.Status,
		LastUsedAt: cloneTime(t.LastUsedAt),
		CreatedAt:  t.CreatedAt,
		UpdatedAt:  t.UpdatedAt,
	}
}

func marshalStringSlice(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("authz: marshal strings: %w", err)
	}
	return string(b), nil
}

func unmarshalStringSlice(s string) ([]string, error) {
	if s == "" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("authz-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func newSecretToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("at-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
