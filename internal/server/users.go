package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

const maxUserBody = 1 << 20

// handleListUsers GET /api/v1/users：用户管理列表（PasswordHash 由模型 json:"-" 遮挡，永不回读）。
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	list, err := s.auth.ListUsers(r.Context())
	if err != nil {
		slog.Warn("server: 用户列表读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "list users failed")
		return
	}
	writeOK(w, r, http.StatusOK, list)
}

// handleCreateUser POST /api/v1/users：创建用户（密码 argon2id 哈希落库）。
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	var req auth.CreateUserRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	u, err := s.auth.CreateUser(r.Context(), req)
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusCreated, u)
}

// handleUpdateUser PATCH /api/v1/users/{id}：部分更新（displayName/password/roleId/status）。
// 额外保守保护：禁止把当前登录用户自己停用或降级（防止误操作把自己锁死）。
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	var req auth.UpdateUserRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBody)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	if cur := UserFrom(r.Context()); cur != nil && cur.ID == id {
		if req.Status != nil && *req.Status == "disabled" {
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "cannot disable the current user")
			return
		}
		if req.RoleID != nil {
			role := RoleFrom(r.Context())
			if role == nil || *req.RoleID != role.ID {
				writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "cannot change own role")
				return
			}
		}
	}
	u, err := s.auth.UpdateUser(r.Context(), id, req)
	if err != nil {
		writeUserError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, u)
}

// handleDeleteUser DELETE /api/v1/users/{id}：删除非内置 admin 用户。
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	id := r.PathValue("id")
	if cur := UserFrom(r.Context()); cur != nil && cur.ID == id {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "cannot delete the current user")
		return
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		writeUserError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, map[string]string{"deleted": "ok"})
}

// handleListRoles GET /api/v1/roles：角色及其权限（管理端只读视图）。
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	list, err := s.auth.ListRoles(r.Context())
	if err != nil {
		slog.Warn("server: 角色列表读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "list roles failed")
		return
	}
	writeOK(w, r, http.StatusOK, list)
}

// writeUserError 把用户管理服务错误映射为统一错误信封。
func writeUserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidUser), errors.Is(err, auth.ErrRoleNotFound):
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
	case errors.Is(err, auth.ErrUserExists):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, auth.ErrUserNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, auth.ErrProtectedUser):
		writeError(w, r, http.StatusForbidden, codeForbidden, err.Error())
	default:
		slog.Warn("server: 用户管理操作失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "user operation failed")
	}
}

// compile-time interface check：认证服务需满足用户管理接口。
var _ userAdminService = (*auth.Service)(nil)

// userAdminService 是 /users /roles 端点依赖的最小接口（auth.Service 满足）。
type userAdminService interface {
	ListUsers(ctx context.Context) ([]*store.User, error)
	CreateUser(ctx context.Context, req auth.CreateUserRequest) (*store.User, error)
	UpdateUser(ctx context.Context, id string, req auth.UpdateUserRequest) (*store.User, error)
	DeleteUser(ctx context.Context, id string) error
	ListRoles(ctx context.Context) ([]*store.Role, error)
}
