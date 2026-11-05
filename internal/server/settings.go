package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// maxSettingsBody 限制配置提交体（键值对数量有限，1MB 足够且防滥用）。
const maxSettingsBody = 1 << 20

// settingsService 是配置端点依赖的应用服务接口（settings.Service 满足）。
// Handler 只依赖该接口，不直接触碰 Store（需求 v1.2 §24）。
type settingsService interface {
	GetGroup(ctx context.Context, group string) (*settings.GroupView, error)
	PatchGroup(ctx context.Context, group string, patch map[string]json.RawMessage, actor settings.Actor) (*settings.GroupView, error)
}

// auditWriter 是系统事件落库接口（service.AuditService 满足）。
type auditWriter interface {
	Record(ctx context.Context, e store.AuditEvent) error
}

// handleGetSettings GET /api/v1/settings/{group}：敏感键只回 hasValue，永不回明文。
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "settings is not configured")
		return
	}
	view, err := s.settings.GetGroup(r.Context(), r.PathValue("group"))
	if err != nil {
		writeSettingsError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, view)
}

// handlePatchSettings PATCH /api/v1/settings/{group}：请求体是扁平的键→取值对象，
// 只更新出现的键；未登记键或越界取值整批拒绝（400），成功后返回整组最新视图。
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, r, http.StatusNotImplemented, codeInternal, "settings is not configured")
		return
	}
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody)).Decode(&patch); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "invalid body: "+err.Error())
		return
	}
	if len(patch) == 0 {
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, "no settings provided")
		return
	}
	actor := settings.Actor{RequestID: RequestIDFrom(r.Context())}
	if u := UserFrom(r.Context()); u != nil {
		actor.UserID = u.ID
	}
	view, err := s.settings.PatchGroup(r.Context(), r.PathValue("group"), patch, actor)
	if err != nil {
		writeSettingsError(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, view)
}

// writeSettingsError 把配置服务错误映射为统一错误信封。
func writeSettingsError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, settings.ErrGroupNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, err.Error())
	case errors.Is(err, settings.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, codeInvalidArgument, err.Error())
	case errors.Is(err, settings.ErrCipherRequired):
		// P3.9：未装配凭据加密器时，包含敏感键的写入整批拒绝（与集群端点
		// 未装配模式一致，避免任何明文落库路径）。
		writeError(w, r, http.StatusNotImplemented, codeInternal,
			"敏感配置需要服务端先配置 K8S_AI_ENC_KEY 后才能保存，请联系平台管理员")
	default:
		writeError(w, r, http.StatusInternalServerError, codeInternal, "settings operation failed")
	}
}

// recordAuthEvent 写登录/登出系统事件（P1.11）。只记录用户身份与结果，
// 绝不记录口令或 Token；审计失败只告警，不改变认证结果。
func (s *Server) recordAuthEvent(r *http.Request, action, eventType, userID string) {
	if s.auditlog == nil {
		return
	}
	err := s.auditlog.Record(r.Context(), store.AuditEvent{
		Action:        action,
		EventType:     eventType,
		UserID:        userID,
		RequestID:     RequestIDFrom(r.Context()),
		Operation:     http.MethodPost,
		ResourceKind:  "auth",
		ResourceName:  userID,
		ResultSummary: `{"status":"success"}`,
		Status:        "success",
	})
	if err != nil {
		slog.Warn("server: 认证审计写入失败", "action", action, "error", err)
	}
}
