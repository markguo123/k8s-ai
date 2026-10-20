// 用户/角色管理（P3.11a /users /roles）的应用服务。
// 安全约定：密码用 argon2id 哈希落库，PasswordHash 由模型 json:"-" 遮挡，
// 任何返回体/日志/审计都不携带明文或哈希；内置 admin 用户受保护不可删除/停用/降级。
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 用户管理哨兵错误（映射到统一错误信封）。
var (
	ErrUserNotFound  = errors.New("auth: user not found")
	ErrUserExists    = errors.New("auth: user already exists")
	ErrRoleNotFound  = errors.New("auth: role not found")
	ErrInvalidUser   = errors.New("auth: invalid user data")
	ErrProtectedUser = errors.New("auth: protected user cannot be modified")
)

// minPasswordLength 是新设/重置密码的最短长度。
const minPasswordLength = 8

// allowedRoleNames 是平台开放的角色白名单：user / operator / admin 三角色；
// viewer 仅作三期兼容读取与未知角色最小权限回落，不开放新建。
var allowedRoleNames = map[string]bool{
	RoleUser:     true,
	RoleOperator: true,
	RoleAdmin:    true,
}

// CreateUserRequest 是 POST /api/v1/users 的请求体。
type CreateUserRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
	RoleID      string `json:"roleId"`
	Status      string `json:"status"`
}

// UpdateUserRequest 是 PATCH /api/v1/users/{id} 的部分更新体。
// 不支持改用户名：内置 admin 用户名受保护，普通用户改名会引入唯一性/权限表
// 关联复杂度，当前阶段不开放。
type UpdateUserRequest struct {
	DisplayName *string `json:"displayName"`
	Password    *string `json:"password"`
	RoleID      *string `json:"roleId"`
	Status      *string `json:"status"`
}

// ListUsers 返回全部用户（敏感字段不回读）。
func (s *Service) ListUsers(ctx context.Context) ([]*store.User, error) {
	list, err := s.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: list users: %w", err)
	}
	return list, nil
}

// ListRoles 返回全部角色及其权限（管理端只读视图）。
func (s *Service) ListRoles(ctx context.Context) ([]*store.Role, error) {
	list, err := s.roles.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: list roles: %w", err)
	}
	return list, nil
}

// CreateUser 校验并创建用户：用户名非空且唯一、密码最短 8 位、
// 角色必须存在且命中三角色白名单、状态仅 active|disabled。
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) (*store.User, error) {
	username := strings.TrimSpace(req.Username)
	if username == "" {
		return nil, fmt.Errorf("%w: username is required", ErrInvalidUser)
	}
	if req.Password == "" || len(req.Password) < minPasswordLength {
		return nil, fmt.Errorf("%w: password must be at least %d characters", ErrInvalidUser, minPasswordLength)
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return nil, fmt.Errorf("%w: status must be active or disabled", ErrInvalidUser)
	}
	role, err := s.validateRole(ctx, req.RoleID)
	if err != nil {
		return nil, err
	}
	existing, err := s.users.GetByUsername(ctx, username)
	if err == nil {
		return nil, fmt.Errorf("%w: %s", ErrUserExists, existing.Username)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("auth: check username: %w", err)
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	u := &store.User{
		ID:           newSecretToken(),
		Username:     username,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		PasswordHash: hash,
		RoleID:       role.ID,
		Status:       status,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("auth: create user: %w", err)
	}
	return u, nil
}

// UpdateUser 支持 displayName / password / roleID / status 的部分更新。
// 内置 admin 受保护：不可停用，也不可改到非 admin 角色。
func (s *Service) UpdateUser(ctx context.Context, id string, req UpdateUserRequest) (*store.User, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrUserNotFound
	}
	u, err := s.users.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("auth: get user: %w", err)
	}
	isBootstrapAdmin := u.Username == DefaultAdminUsername

	if req.DisplayName != nil {
		u.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Status != nil {
		status := strings.TrimSpace(*req.Status)
		if status != "active" && status != "disabled" {
			return nil, fmt.Errorf("%w: status must be active or disabled", ErrInvalidUser)
		}
		if isBootstrapAdmin && status == "disabled" {
			return nil, fmt.Errorf("%w: cannot disable the built-in admin account", ErrProtectedUser)
		}
		u.Status = status
	}
	if req.RoleID != nil {
		role, rerr := s.validateRole(ctx, *req.RoleID)
		if rerr != nil {
			return nil, rerr
		}
		if isBootstrapAdmin && role.Name != RoleAdmin {
			return nil, fmt.Errorf("%w: cannot change the built-in admin role", ErrProtectedUser)
		}
		u.RoleID = role.ID
	}
	if req.Password != nil {
		pw := *req.Password
		if pw == "" || len(pw) < minPasswordLength {
			return nil, fmt.Errorf("%w: password must be at least %d characters", ErrInvalidUser, minPasswordLength)
		}
		hash, herr := HashPassword(pw)
		if herr != nil {
			return nil, herr
		}
		u.PasswordHash = hash
	}
	if err := s.users.Update(ctx, u); err != nil {
		return nil, fmt.Errorf("auth: update user: %w", err)
	}
	return u, nil
}

// DeleteUser 删除非内置 admin 用户；内置 admin 返回 ErrProtectedUser。
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrUserNotFound
	}
	u, err := s.users.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("auth: get user: %w", err)
	}
	if u.Username == DefaultAdminUsername {
		return fmt.Errorf("%w: cannot delete the built-in admin account", ErrProtectedUser)
	}
	if err := s.users.Delete(ctx, id); err != nil {
		return fmt.Errorf("auth: delete user: %w", err)
	}
	return nil
}

// validateRole 解析并校验角色存在于角色表且名称命中三角色白名单。
func (s *Service) validateRole(ctx context.Context, roleID string) (*store.Role, error) {
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		return nil, fmt.Errorf("%w: roleId is required", ErrRoleNotFound)
	}
	role, err := s.roles.Get(ctx, roleID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrRoleNotFound
		}
		return nil, fmt.Errorf("auth: get role: %w", err)
	}
	if !allowedRoleNames[role.Name] {
		return nil, fmt.Errorf("%w: role %q is not allowed", ErrInvalidUser, role.Name)
	}
	return role, nil
}
