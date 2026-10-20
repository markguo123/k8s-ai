package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/authz"
)

// maxAuthzBody 限制授权面请求体大小。
const maxAuthzBody = 1 << 20

// authzService 是 /grants /users/{id}/tokens 端点依赖的最小接口（authz.Service 满足）。
type authzService interface {
	ListGrants(ctx context.Context) ([]*authz.Grant, error)
	GetGrant(ctx context.Context, id string) (*authz.Grant, error)
	CreateGrant(ctx context.Context, req authz.CreateGrantRequest, createdBy string) (*authz.Grant, error)
	UpdateGrant(ctx context.Context, id string, req authz.UpdateGrantRequest) (*authz.Grant, error)
	DeleteGrant(ctx context.Context, id string) error
	ListTokens(ctx context.Context, userID string) ([]*authz.Token, error)
	CreateToken(ctx context.Context, userID string, req authz.CreateTokenRequest) (*authz.Token, string, error)
	UpdateTokenStatus(ctx context.Context, userID, id, status string) (*authz.Token, error)
	DeleteToken(ctx context.Context, userID, id string) error
}

// handleListGrants GET /api/v1/grants：全部授权委派（管理员视图）。
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	list, err := s.authzSvc.ListGrants(r.Context())
	if err != nil {
		slog.Warn("server: 授权委派列表读取失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "list grants failed")
		return
	}
	writeOK(w, r, http.StatusOK, list)
}

// handleGetGrant GET /api/v1/grants/{id}：单条授权委派。
func (s *Server) handleGetGrant(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	g, err := s.authzSvc.GetGrant(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, g)
}

// handleCreateGrant POST /api/v1/grants：创建 namespace 授权/销毁委派。
func (s *Server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	var req authz.CreateGrantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthzBody)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	createdBy := ""
	if u := UserFrom(r.Context()); u != nil {
		createdBy = u.ID
	}
	g, err := s.authzSvc.CreateGrant(r.Context(), req, createdBy)
	if err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusCreated, g)
}

// handleUpdateGrant PATCH /api/v1/grants/{id}：部分更新（集群/namespace/权限/有效期）。
func (s *Server) handleUpdateGrant(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	var req authz.UpdateGrantRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthzBody)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	g, err := s.authzSvc.UpdateGrant(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, g)
}

// handleDeleteGrant DELETE /api/v1/grants/{id}：撤销授权委派。
func (s *Server) handleDeleteGrant(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	if err := s.authzSvc.DeleteGrant(r.Context(), r.PathValue("id")); err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, map[string]string{"deleted": "ok"})
}

// handleListTokens GET /api/v1/users/{userID}/tokens：用户 API Token 元数据列表。
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	list, err := s.authzSvc.ListTokens(r.Context(), r.PathValue("userID"))
	if err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, list)
}

// handleCreateToken POST /api/v1/users/{userID}/tokens：签发 API Token，明文仅返回一次。
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	var req authz.CreateTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthzBody)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	token, plain, err := s.authzSvc.CreateToken(r.Context(), r.PathValue("userID"), req)
	if err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusCreated, authz.CreateTokenResult{APIToken: token, Token: plain})
}

// handlePatchToken PATCH /api/v1/users/{userID}/tokens/{id}：更新生命周期状态。
func (s *Server) handlePatchToken(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthzBody)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	token, err := s.authzSvc.UpdateTokenStatus(r.Context(), r.PathValue("userID"), r.PathValue("id"), req.Status)
	if err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, token)
}

// handleDeleteToken DELETE /api/v1/users/{userID}/tokens/{id}：撤销并删除 API Token。
func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	if s.authzSvc == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "authz is not configured")
		return
	}
	if err := s.authzSvc.DeleteToken(r.Context(), r.PathValue("userID"), r.PathValue("id")); err != nil {
		writeAuthzError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, map[string]string{"deleted": "ok"})
}

// writeAuthzError 把授权面服务错误映射为统一错误信封。
func writeAuthzError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authz.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
	case errors.Is(err, authz.ErrGrantNotFound), errors.Is(err, authz.ErrTokenNotFound), errors.Is(err, authz.ErrUserNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error())
	default:
		slog.Warn("server: 授权面操作失败", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "authz operation failed")
	}
}

// compile-time interface check：认证授权服务需满足 server 依赖的最小接口。
var _ authzService = (*authz.Service)(nil)
