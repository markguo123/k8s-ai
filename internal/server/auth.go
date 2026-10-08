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

// authService 是认证端点依赖的应用服务接口（auth.Service 满足）。
type authService interface {
	Login(ctx context.Context, username, password string) (*auth.Session, error)
	Logout(token string) error
	UserByToken(ctx context.Context, token string) (*store.User, *store.Role, error)
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
