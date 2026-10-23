// Package auth 提供四期平台化的产品内用户认证（P1.5）：
// argon2id 密码哈希、内存 Session Token、/auth/login|logout|me 的业务逻辑。
// 安全约定（AGENTS.md）：密码与 Token 永不写入日志；读取 Secret 内容值仍为红线。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 默认角色名：平台只开放 user / operator / admin 三角色。
const (
	RoleAdmin    = "admin"
	RoleUser     = "user"
	RoleOperator = "operator"

	// DefaultAdminUsername 首次初始化时创建的超级管理员用户名。
	DefaultAdminUsername = "admin"
	// SessionTTL 单个登录会话的有效期。
	SessionTTL = 24 * time.Hour

	argonMemory  = 64 * 1024 // 64 MiB，argon2id 推荐下限
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// 认证错误（对外统一为 invalid credentials，避免泄漏用户是否存在）。
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrInvalidToken       = errors.New("auth: invalid session token")
	ErrTokenExpired       = errors.New("auth: session token expired")
)

// Session 是一次登录得到的会话令牌。
type Session struct {
	Token     string      `json:"token"`
	UserID    string      `json:"userId"`
	User      *store.User `json:"user"`
	CreatedAt time.Time   `json:"createdAt"`
	ExpiresAt time.Time   `json:"expiresAt"`
}

type sessionEntry struct {
	userID    string
	createdAt time.Time
	expiresAt time.Time
}

// Service 是用户认证应用服务。
type Service struct {
	users  store.UserStore
	roles  store.RoleStore
	now    func() time.Time
	mu     sync.Mutex
	tokens map[string]sessionEntry
}

// New 创建认证服务（依赖 P1.2 的 User/Role Store）。
func New(users store.UserStore, roles store.RoleStore) *Service {
	return &Service{
		users:  users,
		roles:  roles,
		now:    time.Now,
		tokens: map[string]sessionEntry{},
	}
}

// Authenticate 校验用户名与密码，成功返回用户，失败统一返回 ErrInvalidCredentials。
func (s *Service) Authenticate(ctx context.Context, username, password string) (*store.User, error) {
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	u, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if u.Status != "" && u.Status != "active" {
		return nil, ErrInvalidCredentials
	}
	ok, err := verifyPassword(u.PasswordHash, password)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// Login 校验凭据并签发会话 Token。
func (s *Service) Login(ctx context.Context, username, password string) (*Session, error) {
	u, err := s.Authenticate(ctx, username, password)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	token := newSecretToken()
	s.mu.Lock()
	s.tokens[token] = sessionEntry{
		userID:    u.ID,
		createdAt: now,
		expiresAt: now.Add(SessionTTL),
	}
	s.mu.Unlock()
	return &Session{
		Token:     token,
		UserID:    u.ID,
		User:      u,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
	}, nil
}

// Logout 使指定 Token 失效。
func (s *Service) Logout(token string) error {
	if token == "" {
		return ErrInvalidToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[token]; !ok {
		return ErrInvalidToken
	}
	delete(s.tokens, token)
	return nil
}

// UserByToken 依据 Token 解析当前用户与其角色。
func (s *Service) UserByToken(ctx context.Context, token string) (*store.User, *store.Role, error) {
	if token == "" {
		return nil, nil, ErrInvalidToken
	}
	s.mu.Lock()
	e, ok := s.tokens[token]
	s.mu.Unlock()
	if !ok {
		return nil, nil, ErrInvalidToken
	}
	if s.now().UTC().After(e.expiresAt) {
		s.mu.Lock()
		delete(s.tokens, token)
		s.mu.Unlock()
		return nil, nil, ErrTokenExpired
	}
	u, err := s.users.Get(ctx, e.userID)
	if err != nil {
		return nil, nil, ErrInvalidToken
	}
	var role *store.Role
	if u.RoleID != "" {
		if r, err := s.roles.Get(ctx, u.RoleID); err == nil {
			role = r
		}
	}
	return u, role, nil
}

// Bootstrap 初始化默认角色与 admin 用户，并对已有库执行幂等角色修复。
// admin 密码由调用方提供（环境变量/配置），不写入日志。
func (s *Service) Bootstrap(ctx context.Context, adminPassword string) error {
	return s.BootstrapWithName(ctx, DefaultAdminUsername, adminPassword)
}

// BootstrapWithName 与 Bootstrap 等价，但允许调用方指定首个管理员用户名
// （P6.12：读取 platform.auth.admin_username 配置，为空回落默认 admin）。
// 已有用户时不会重建或重命名管理员，但会修复三角色定义。
func (s *Service) BootstrapWithName(ctx context.Context, adminUsername, adminPassword string) error {
	if strings.TrimSpace(adminUsername) == "" {
		adminUsername = DefaultAdminUsername
	}
	if strings.TrimSpace(adminPassword) == "" {
		return errors.New("auth: bootstrap admin password must not be empty")
	}
	existing, err := s.users.List(ctx)
	if err != nil {
		return fmt.Errorf("auth: list users: %w", err)
	}
	if len(existing) > 0 {
		return s.reconcileRoles(ctx)
	}
	now := s.now().UTC()
	roleIDs := map[string]string{}
	for _, def := range defaultRoles() {
		id := newSecretToken()
		role := &store.Role{
			ID:          id,
			Name:        def.name,
			Description: def.description,
			Permissions: def.permissions,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.roles.Create(ctx, role); err != nil {
			return fmt.Errorf("auth: create role %s: %w", def.name, err)
		}
		roleIDs[def.name] = id
	}
	adminHash, err := HashPassword(adminPassword)
	if err != nil {
		return fmt.Errorf("auth: hash admin password: %w", err)
	}
	admin := &store.User{
		ID:           newSecretToken(),
		Username:     strings.TrimSpace(adminUsername),
		DisplayName:  "Administrator",
		PasswordHash: adminHash,
		RoleID:       roleIDs[RoleAdmin],
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.users.Create(ctx, admin); err != nil {
		return fmt.Errorf("auth: create admin user: %w", err)
	}
	return nil
}

// reconcileRoles 保证三角色存在且权限口径与当前设计一致。方法可重复执行。
func (s *Service) reconcileRoles(ctx context.Context) error {
	roles, err := s.roles.List(ctx)
	if err != nil {
		return fmt.Errorf("auth: list roles: %w", err)
	}
	byName := make(map[string]*store.Role, len(roles))
	for _, role := range roles {
		byName[role.Name] = role
	}

	now := s.now().UTC()
	for _, def := range defaultRoles() {
		role := byName[def.name]
		if role == nil {
			role = &store.Role{
				ID:          newSecretToken(),
				Name:        def.name,
				Description: def.description,
				Permissions: append([]string(nil), def.permissions...),
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := s.roles.Create(ctx, role); err != nil {
				return fmt.Errorf("auth: create missing role %s: %w", def.name, err)
			}
			byName[def.name] = role
			continue
		}
		if !sameStrings(role.Permissions, def.permissions) {
			role.Permissions = append([]string(nil), def.permissions...)
			role.Description = def.description
			role.UpdatedAt = now
			if err := s.roles.Update(ctx, role); err != nil {
				return fmt.Errorf("auth: normalize role %s: %w", def.name, err)
			}
		}
	}

	return nil
}

type roleDef struct {
	name        string
	description string
	permissions []string
}

func defaultRoles() []roleDef {
	return []roleDef{
		{name: RoleAdmin, description: "管理员：全部平台权限", permissions: []string{"*"}},
		// user/operator 的平台权限默认为空。实际集群能力由 admin 授予的
		// namespace AuthzGrant 决定，不能靠角色默认值预先放行。
		{name: RoleUser, description: "普通用户：零平台权限，按 namespace 授权使用集群能力", permissions: []string{}},
		{name: RoleOperator, description: "操作员：零平台权限，按 namespace 授权使用集群能力", permissions: []string{}},
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// HashPassword 用 argon2id 生成带参数与盐的密码哈希（P1.5 验收）。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key),
	), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	if password == "" {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: unsupported password hash format")
	}
	vParts := strings.Split(parts[2], "=")
	if len(vParts) != 2 || vParts[1] != strconv.Itoa(argon2.Version) {
		return false, errors.New("auth: unsupported argon2 version")
	}
	memory, iterations, threads, err := parseArgonParams(parts[3])
	if err != nil {
		return false, err
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("auth: decode salt: %w", err)
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("auth: decode hash: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseArgonParams(raw string) (memory uint32, iterations uint32, threads uint8, err error) {
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return 0, 0, 0, errors.New("auth: invalid argon2 params format")
		}
		n, perr := strconv.ParseUint(kv[1], 10, 32)
		if perr != nil {
			return 0, 0, 0, fmt.Errorf("auth: invalid argon2 param %s: %w", kv[0], perr)
		}
		switch kv[0] {
		case "m":
			memory = uint32(n)
		case "t":
			iterations = uint32(n)
		case "p":
			threads = uint8(n)
		default:
			return 0, 0, 0, fmt.Errorf("auth: unknown argon2 param %s", kv[0])
		}
	}
	if memory == 0 || iterations == 0 || threads == 0 {
		return 0, 0, 0, errors.New("auth: argon2 params must be positive")
	}
	return memory, iterations, threads, nil
}

func newSecretToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
