package webui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/k8s-ai/k8s-ai/internal/permission"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// admin 跨用户会话操作的审计动作名（PRD §17.2：admin 对他人会话的读写、
// 运行控制与审批必须写审计；台账 U-06）。
const (
	actionAdminSessionRead    = "session.admin.read"
	actionAdminSessionMessage = "session.admin.message"
	actionAdminSessionApprove = "session.admin.approve"
	actionAdminSessionCancel  = "session.admin.cancel"
	actionAdminSessionRename  = "session.admin.rename"
	actionAdminSessionPin     = "session.admin.pin"
	actionAdminSessionDelete  = "session.admin.delete"
)

// AuditSink 写平台系统事件（与 service.AuditService.Record 同签名）。
// 未装配时跨用户操作照常执行，只是不留痕。
type AuditSink func(ctx context.Context, e store.AuditEvent) error

// WithAudit 注入审计写入。链式可选，不破坏既有构造器签名。
func (s *Server) WithAudit(sink AuditSink) *Server {
	s.audit = sink
	return s
}

// recordCrossUserAction 在操作者不是会话 owner 时记一条管理员留痕事件。
//
// 只记身份与动作，绝不记会话正文、消息内容或审批回传值：这些通道可能携带
// Secret 取值（handleApprove 的 respond 通道），明文一旦进 payload 就破了红线。
// 审计写失败不阻断业务请求，只降级为告警日志。
func (s *Server) recordCrossUserAction(r *http.Request, sess *session.Session, action, eventType string, extra map[string]any) {
	if s.audit == nil || sess == nil {
		return
	}
	principal, ok := principalFrom(r)
	if !ok || principal.User.ID == sess.UserID {
		return
	}
	payload := map[string]any{
		"targetSessionId": sess.ID,
		"targetUserId":    sess.UserID,
		"operatorRole":    principal.roleName(),
		"via":             principal.sessionAccessVia(),
	}
	for k, v := range extra {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("webui: 跨用户会话审计序列化失败", "session", sess.ID, "action", action, "error", err)
		return
	}
	if err := s.audit(r.Context(), store.AuditEvent{
		SessionID:     sess.ID,
		UserID:        principal.User.ID,
		Action:        action,
		EventType:     eventType,
		Payload:       string(raw),
		ResourceKind:  "Session",
		ResourceName:  sess.ID,
		Operation:     operationOf(action),
		ResultSummary: "admin cross-user session operation",
		Status:        "success",
	}); err != nil {
		slog.Warn("webui: 跨用户会话审计写入失败", "session", sess.ID, "action", action, "error", err)
	}
}

// operationOf 把动作名映射到审计 operation（便于按动作过滤检索）。
func operationOf(action string) string {
	for i := len(action) - 1; i >= 0; i-- {
		if action[i] == '.' {
			return action[i+1:]
		}
	}
	return action
}

func (p requestPrincipal) roleName() string {
	if p.Role == nil {
		return ""
	}
	return p.Role.Name
}

// sessionAccessVia 记录该请求凭哪条平台 Permission 获得跨用户会话能力。
func (p requestPrincipal) sessionAccessVia() string {
	switch {
	case p.canManageAllSessions():
		return permission.ChatManageAll
	case p.canReadAllSessions():
		return permission.ChatReadAll
	default:
		return "owner"
	}
}
