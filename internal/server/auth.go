package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

const maxCredentialBody = 1 << 20

// authService 是认证与用户管理端点依赖的应用服务接口（auth.Service 满足）。
type authService interface {
	Login(ctx context.Context, username, password string) (*auth.Session, error)
	Logout(token string) error
	UserByToken(ctx context.Context, token string) (*store.User, *store.Role, error)
	// P6.12：登录用户自助改密（需旧密码，新密码最短 8 位，当场落库）。
	ChangePassword(ctx context.Context, userID, oldPassword, newPassword string) error
	// P3.11a：/users /roles 管理能力（handler 只依赖该接口，不触碰 Store）。
	ListUsers(ctx context.Context) ([]*store.User, error)
	CreateUser(ctx context.Context, req auth.CreateUserRequest) (*store.User, error)
	UpdateUser(ctx context.Context, id string, req auth.UpdateUserRequest) (*store.User, error)
	DeleteUser(ctx context.Context, id string) error
	ListRoles(ctx context.Context) ([]*store.Role, error)
}

// handleLogin POST /api/v1/auth/login。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCredentialBody)).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	sess, err := s.auth.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		// 不区分用户不存在/密码错误/停用，避免泄漏账号状态。
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "invalid username or password")
		return
	}
	// P1.11：登录成功写 LOGIN 事件（不含口令、不含 Token）。
	s.recordAuthEvent(r, store.ActionAuthLogin, store.AuditEventLogin, sess.User.ID)
	writeOK(w, r, http.StatusOK, map[string]any{
		"token":     sess.Token,
		"expiresAt": sess.ExpiresAt,
		"user":      sess.User,
	})
}

// handleLogout POST /api/v1/auth/logout。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	if err := s.auth.Logout(BearerTokenFrom(r.Context())); err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "no valid session")
			return
		}
		writeError(w, r, http.StatusInternalServerError, codeInternal, "logout failed")
		return
	}
	// P1.11：登出成功写 LOGOUT 事件（用户来自认证中间件注入的上下文）。
	if u := UserFrom(r.Context()); u != nil {
		s.recordAuthEvent(r, store.ActionAuthLogout, store.AuditEventLogout, u.ID)
	}
	writeOK(w, r, http.StatusOK, map[string]string{"logout": "ok"})
}

// handleMe GET /api/v1/auth/me。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	u, role, err := s.auth.UserByToken(r.Context(), BearerTokenFrom(r.Context()))
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	writeOK(w, r, http.StatusOK, map[string]any{"user": u, "role": role})
}

// handleChangePassword POST /api/v1/auth/password（P6.12 自助改密）。
// 仅登录用户可调用并主动携带旧密码；口令统一不区分账号状态，
// 旧密码错误返回 401，新密码不合规（过短/与旧密码相同）返回 400。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "auth is not configured")
		return
	}
	u := UserFrom(r.Context())
	if u == nil {
		writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "authentication required")
		return
	}
	var body struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCredentialBody)).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	if body.OldPassword == "" || body.NewPassword == "" {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "oldPassword and newPassword are required")
		return
	}
	if err := s.auth.ChangePassword(r.Context(), u.ID, body.OldPassword, body.NewPassword); err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "old password is incorrect")
		case errors.Is(err, auth.ErrInvalidUser):
			writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
		default:
			writeError(w, r, http.StatusInternalServerError, codeInternal, "password change failed")
		}
		return
	}
	// P6.12：改密成功写审计（不记录任何口令信息）。
	s.recordAuthEvent(r, store.ActionAuthPassword, store.EventTypeForAction(store.ActionAuthPassword), u.ID)
	writeOK(w, r, http.StatusOK, map[string]string{"status": "ok"})
}
